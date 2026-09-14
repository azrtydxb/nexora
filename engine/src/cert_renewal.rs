//! Engine certificate renewal: timing, CSR generation, verification of the issued certificate and
//! the atomic swap of `state_dir/identity`. Runs on the control runtime only.
//!
//! A renewed identity is staged in `identity.new` and promoted to `identity` only after a control
//! stream authenticated with it, so the previous identity stays usable until the new certificate
//! is confirmed working (the management plane supersedes older certificates on that `Connect`).

use crate::proto::CertificateIssued;
use rcgen::PublicKeyData;
use rustls::RootCertStore;
use rustls::pki_types::{CertificateDer, UnixTime};
use rustls::server::WebPkiClientVerifier;
use std::fs::{self, File, OpenOptions};
use std::io::{self, Write};
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

const IDENTITY_FILES: [&str; 4] = ["cert.pem", "key.pem", "ca.pem", "engine_id"];

/// True from 2/3 of the certificate lifetime onwards (and for inverted validity).
pub fn renewal_due(not_before: SystemTime, not_after: SystemTime, now: SystemTime) -> bool {
    let Ok(lifetime) = not_after.duration_since(not_before) else {
        return true;
    };
    now >= not_before + lifetime * 2 / 3
}

/// NotBefore and NotAfter of the first certificate in `cert_pem`.
pub fn cert_validity(cert_pem: &str) -> Option<(SystemTime, SystemTime)> {
    let (_, pem) = x509_parser::pem::parse_x509_pem(cert_pem.as_bytes()).ok()?;
    let cert = pem.parse_x509().ok()?;
    let at = |t: i64| UNIX_EPOCH + Duration::from_secs(t.max(0) as u64);
    Some((
        at(cert.validity().not_before.timestamp()),
        at(cert.validity().not_after.timestamp()),
    ))
}

/// The serial of the first certificate in `cert_pem` as lowercase hex without leading zeros
/// (the management plane's notation), or `?` when it does not parse.
pub fn cert_serial(cert_pem: &str) -> String {
    x509_parser::pem::parse_x509_pem(cert_pem.as_bytes())
        .ok()
        .and_then(|(_, pem)| Some(pem.parse_x509().ok()?.serial.to_str_radix(16)))
        .unwrap_or_else(|| "?".into())
}

/// A fresh ECDSA P-256 key and a CSR whose subject CN is the engine id.
pub fn new_csr(engine_id: &str) -> Result<(Vec<u8>, rcgen::KeyPair), rcgen::Error> {
    let key = rcgen::KeyPair::generate_for(&rcgen::PKCS_ECDSA_P256_SHA256)?;
    let mut params = rcgen::CertificateParams::default();
    params.distinguished_name = rcgen::DistinguishedName::new();
    params
        .distinguished_name
        .push(rcgen::DnType::CommonName, engine_id);
    let csr = params.serialize_request(&key)?;
    Ok((csr.der().to_vec(), key))
}

/// Checks an issued certificate against the pending request and returns it as PEM: the CA must be
/// the pinned CA (`ca_pem`), the certificate must carry `key`'s public key and the engine id as
/// subject CN, and it must verify as a client certificate under that CA at `now`.
pub fn verify_issued(
    engine_id: &str,
    ca_pem: &str,
    key: &rcgen::KeyPair,
    issued: &CertificateIssued,
    now: SystemTime,
) -> Result<String, String> {
    let pinned = pem::parse(ca_pem).map_err(|e| format!("stored CA: {e}"))?;
    if issued.ca_der != pinned.contents() {
        return Err("CA differs from the pinned CA".into());
    }
    let (_, cert) = x509_parser::parse_x509_certificate(&issued.cert_der)
        .map_err(|e| format!("certificate does not parse: {e}"))?;
    if cert.public_key().raw != key.subject_public_key_info().as_slice() {
        return Err("public key differs from the requested key".into());
    }
    let cn = cert
        .subject()
        .iter_common_name()
        .next()
        .and_then(|c| c.as_str().ok());
    if cn != Some(engine_id) {
        return Err(format!("subject CN {cn:?} is not the engine id"));
    }
    let mut roots = RootCertStore::empty();
    roots
        .add(CertificateDer::from(issued.ca_der.as_slice()).into_owned())
        .map_err(|e| format!("CA: {e}"))?;
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let verifier = WebPkiClientVerifier::builder_with_provider(Arc::new(roots), provider)
        .build()
        .map_err(|e| format!("CA: {e}"))?;
    let since_epoch = now.duration_since(UNIX_EPOCH).unwrap_or_default();
    verifier
        .verify_client_cert(
            &CertificateDer::from(issued.cert_der.as_slice()),
            &[],
            UnixTime::since_unix_epoch(since_epoch),
        )
        .map_err(|e| format!("does not verify under the pinned CA: {e}"))?;
    Ok(pem::encode(&pem::Pem::new(
        "CERTIFICATE",
        issued.cert_der.clone(),
    )))
}

fn write_synced(path: &Path, bytes: &[u8]) -> io::Result<()> {
    let mut f = OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(path)?;
    f.write_all(bytes)?;
    f.sync_all()
}

fn paths(state_dir: &Path) -> (PathBuf, PathBuf, PathBuf) {
    (
        state_dir.join("identity"),
        state_dir.join("identity.new"),
        state_dir.join("identity.old"),
    )
}

/// The directory a renewed, not yet confirmed identity is staged in.
pub fn staged_dir(state_dir: &Path) -> PathBuf {
    state_dir.join("identity.new")
}

fn complete(dir: &Path) -> bool {
    IDENTITY_FILES.iter().all(|f| dir.join(f).exists())
}

fn remove_if_present(dir: &Path) -> io::Result<()> {
    match fs::remove_dir_all(dir) {
        Err(e) if e.kind() != io::ErrorKind::NotFound => Err(e),
        _ => Ok(()),
    }
}

/// Writes the new pair with copies of the other identity files (every file 0600, synced) to
/// `identity.new.tmp` and renames it to `identity.new`, so a present `identity.new` is always
/// complete. `identity` is not touched.
pub fn stage_identity(state_dir: &Path, cert_pem: &[u8], key_pem: &[u8]) -> io::Result<()> {
    let (cur, new, _) = paths(state_dir);
    let tmp = state_dir.join("identity.new.tmp");
    remove_if_present(&tmp)?;
    remove_if_present(&new)?;
    fs::DirBuilder::new().mode(0o700).create(&tmp)?;
    for entry in fs::read_dir(&cur)? {
        let entry = entry?;
        let name = entry.file_name();
        if name != "cert.pem" && name != "key.pem" && !name.to_string_lossy().ends_with(".tmp") {
            write_synced(&tmp.join(&name), &fs::read(entry.path())?)?;
        }
    }
    write_synced(&tmp.join("cert.pem"), cert_pem)?;
    write_synced(&tmp.join("key.pem"), key_pem)?;
    File::open(&tmp)?.sync_all()?;
    fs::rename(&tmp, &new)?;
    File::open(state_dir)?.sync_all()
}

/// Renames `identity` -> `identity.old` and `identity.new` -> `identity`, then removes the old one.
/// Without a complete `identity.new` (e.g. an engine sharing the state directory promoted it
/// already) nothing is touched and the error is `NotFound`.
pub fn promote_identity(state_dir: &Path) -> io::Result<()> {
    let (cur, new, old) = paths(state_dir);
    if !complete(&new) {
        return Err(io::Error::new(
            io::ErrorKind::NotFound,
            "no staged identity to promote",
        ));
    }
    if old.exists() {
        fs::remove_dir_all(&old)?;
    }
    fs::rename(&cur, &old)?;
    fs::rename(&new, &cur)?;
    File::open(state_dir)?.sync_all()?;
    fs::remove_dir_all(&old)
}

/// Stages the new pair and promotes it at once.
pub fn swap_identity(state_dir: &Path, cert_pem: &[u8], key_pem: &[u8]) -> io::Result<()> {
    stage_identity(state_dir, cert_pem, key_pem)?;
    promote_identity(state_dir)
}

/// Removes a staged identity the management plane refused.
pub fn discard_staged(state_dir: &Path) -> io::Result<()> {
    remove_if_present(&staged_dir(state_dir))
}

/// Finish or discard an interrupted swap. Call before loading the identity. A complete
/// `identity.new` next to an intact `identity` is a renewal awaiting confirmation and is kept.
pub fn recover_identity(state_dir: &Path) -> io::Result<()> {
    let (cur, new, old) = paths(state_dir);
    if !cur.exists() {
        if complete(&new) {
            fs::rename(&new, &cur)?;
        } else if old.exists() {
            fs::rename(&old, &cur)?;
        }
        if state_dir.exists() {
            File::open(state_dir)?.sync_all()?;
        }
    }
    remove_if_present(&old)?;
    remove_if_present(&state_dir.join("identity.new.tmp"))?;
    if new.exists() && !complete(&new) {
        fs::remove_dir_all(&new)?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use std::time::{Duration, UNIX_EPOCH};
    use x509_parser::prelude::FromDer;

    fn ca(name: &str) -> (rcgen::Certificate, rcgen::KeyPair, rcgen::CertificateParams) {
        let key = rcgen::KeyPair::generate_for(&rcgen::PKCS_ECDSA_P256_SHA256).unwrap();
        let mut params = rcgen::CertificateParams::default();
        params.is_ca = rcgen::IsCa::Ca(rcgen::BasicConstraints::Unconstrained);
        params
            .distinguished_name
            .push(rcgen::DnType::CommonName, name);
        let cert = params.self_signed(&key).unwrap();
        (cert, key, params)
    }

    fn sign(
        csr_der: &[u8],
        issuer: &(rcgen::Certificate, rcgen::KeyPair, rcgen::CertificateParams),
    ) -> Vec<u8> {
        let mut csr =
            rcgen::CertificateSigningRequestParams::from_der(&csr_der.to_vec().into()).unwrap();
        csr.params.extended_key_usages = vec![rcgen::ExtendedKeyUsagePurpose::ClientAuth];
        let iss = rcgen::Issuer::from_params(&issuer.2, &issuer.1);
        csr.signed_by(&iss).unwrap().der().to_vec()
    }

    #[test]
    fn issued_certificate_must_match_pinned_ca_key_and_engine_id() {
        let id = "0b7c1f5e-8f4f-4d47-9a55-3f4f0f6d2c11";
        let pinned = ca("nexora ca");
        let ca_pem = pinned.0.pem();
        let (csr, key) = new_csr(id).unwrap();
        let now = SystemTime::now();
        let good = CertificateIssued {
            cert_der: sign(&csr, &pinned),
            ca_der: pinned.0.der().to_vec(),
        };
        let pem = verify_issued(id, &ca_pem, &key, &good, now).unwrap();
        assert_eq!(
            pem::parse(&pem).unwrap().contents(),
            good.cert_der.as_slice()
        );

        let other = ca("other ca");
        let foreign_ca = CertificateIssued {
            cert_der: sign(&csr, &other),
            ca_der: other.0.der().to_vec(),
        };
        let e = verify_issued(id, &ca_pem, &key, &foreign_ca, now).unwrap_err();
        assert!(e.contains("CA differs from the pinned CA"), "{e}");

        let forged = CertificateIssued {
            cert_der: sign(&csr, &other),
            ca_der: pinned.0.der().to_vec(),
        };
        let e = verify_issued(id, &ca_pem, &key, &forged, now).unwrap_err();
        assert!(e.contains("does not verify under the pinned CA"), "{e}");

        let (_, other_key) = new_csr(id).unwrap();
        let e = verify_issued(id, &ca_pem, &other_key, &good, now).unwrap_err();
        assert!(e.contains("public key differs"), "{e}");

        let e = verify_issued("another-engine", &ca_pem, &key, &good, now).unwrap_err();
        assert!(e.contains("is not the engine id"), "{e}");

        let later = now + Duration::from_secs(10_000 * 86_400 * 365);
        let e = verify_issued(id, &ca_pem, &key, &good, later).unwrap_err();
        assert!(e.contains("does not verify"), "{e}");
    }

    #[test]
    fn staged_identity_survives_recovery_until_promoted_or_discarded() {
        let dir = tempfile::tempdir().unwrap();
        identity(dir.path(), b"old-cert", b"old-key");
        stage_identity(dir.path(), b"new-cert", b"new-key").unwrap();
        recover_identity(dir.path()).unwrap();
        let id = dir.path().join("identity");
        assert_eq!(fs::read(id.join("cert.pem")).unwrap(), b"old-cert");
        let staged = staged_dir(dir.path());
        assert_eq!(fs::read(staged.join("key.pem")).unwrap(), b"new-key");
        assert_eq!(fs::read(staged.join("engine_id")).unwrap(), b"engine-uuid");

        discard_staged(dir.path()).unwrap();
        discard_staged(dir.path()).unwrap();
        assert!(!staged.exists());
        assert_eq!(fs::read(id.join("key.pem")).unwrap(), b"old-key");

        stage_identity(dir.path(), b"new-cert", b"new-key").unwrap();
        promote_identity(dir.path()).unwrap();
        assert_eq!(fs::read(id.join("key.pem")).unwrap(), b"new-key");
        assert!(!staged.exists());
        assert!(!dir.path().join("identity.old").exists());

        // A second promotion (another engine on this state directory already promoted) must
        // leave the identity in place.
        let e = promote_identity(dir.path()).unwrap_err();
        assert_eq!(e.kind(), io::ErrorKind::NotFound);
        assert_eq!(fs::read(id.join("key.pem")).unwrap(), b"new-key");
    }

    #[test]
    fn renewal_due_from_two_thirds_of_lifetime() {
        let nb = UNIX_EPOCH + Duration::from_secs(1_000);
        let na = nb + Duration::from_secs(90);
        assert!(!renewal_due(nb, na, nb + Duration::from_secs(59)));
        assert!(renewal_due(nb, na, nb + Duration::from_secs(60)));
        assert!(renewal_due(nb, na, na + Duration::from_secs(1)));
        assert!(renewal_due(na, nb, nb), "inverted validity must renew");
    }

    #[test]
    fn csr_carries_engine_id_and_p256_key() {
        let id = "0b7c1f5e-8f4f-4d47-9a55-3f4f0f6d2c11";
        let (der, key) = new_csr(id).unwrap();
        let (_, csr) =
            x509_parser::certification_request::X509CertificationRequest::from_der(&der).unwrap();
        let cn = csr
            .certification_request_info
            .subject
            .iter_common_name()
            .next()
            .unwrap();
        assert_eq!(cn.as_str().unwrap(), id);
        csr.verify_signature().unwrap();
        assert_eq!(
            csr.certification_request_info.subject_pki.raw,
            key.subject_public_key_info().as_slice()
        );
    }

    fn identity(dir: &std::path::Path, cert: &[u8], key: &[u8]) {
        let id = dir.join("identity");
        fs::create_dir_all(&id).unwrap();
        fs::write(id.join("cert.pem"), cert).unwrap();
        fs::write(id.join("key.pem"), key).unwrap();
        fs::write(id.join("ca.pem"), b"ca").unwrap();
        fs::write(id.join("engine_id"), b"engine-uuid").unwrap();
    }

    #[test]
    fn swap_identity_replaces_pair_and_keeps_the_rest() {
        let dir = tempfile::tempdir().unwrap();
        identity(dir.path(), b"old-cert", b"old-key");
        swap_identity(dir.path(), b"new-cert", b"new-key").unwrap();
        let id = dir.path().join("identity");
        assert_eq!(fs::read(id.join("cert.pem")).unwrap(), b"new-cert");
        assert_eq!(fs::read(id.join("key.pem")).unwrap(), b"new-key");
        assert_eq!(fs::read(id.join("ca.pem")).unwrap(), b"ca");
        assert_eq!(fs::read(id.join("engine_id")).unwrap(), b"engine-uuid");
        assert!(!dir.path().join("identity.new").exists());
        assert!(!dir.path().join("identity.old").exists());
        use std::os::unix::fs::PermissionsExt;
        assert_eq!(
            fs::metadata(id.join("key.pem"))
                .unwrap()
                .permissions()
                .mode()
                & 0o777,
            0o600
        );
    }

    #[test]
    fn recover_identity_after_crash_between_renames() {
        let dir = tempfile::tempdir().unwrap();
        identity(dir.path(), b"old-cert", b"old-key");
        // crash after `identity` -> `identity.old`, before `identity.new` -> `identity`
        fs::rename(dir.path().join("identity"), dir.path().join("identity.old")).unwrap();
        let new = dir.path().join("identity.new");
        fs::create_dir_all(&new).unwrap();
        for (name, data) in [
            ("cert.pem", &b"new-cert"[..]),
            ("key.pem", b"new-key"),
            ("ca.pem", b"ca"),
            ("engine_id", b"engine-uuid"),
        ] {
            fs::write(new.join(name), data).unwrap();
        }
        recover_identity(dir.path()).unwrap();
        assert_eq!(
            fs::read(dir.path().join("identity/cert.pem")).unwrap(),
            b"new-cert"
        );
        assert!(!dir.path().join("identity.old").exists());

        // crash while staging: `identity` intact, half-written `identity.new`
        fs::create_dir_all(dir.path().join("identity.new")).unwrap();
        recover_identity(dir.path()).unwrap();
        assert_eq!(
            fs::read(dir.path().join("identity/cert.pem")).unwrap(),
            b"new-cert"
        );
        assert!(!dir.path().join("identity.new").exists());
    }
}
