//! The DNS serving certificate for DoT, DoH and DoQ: held only in memory, swapped
//! atomically so every new handshake uses the latest installed certificate.

use crate::proto;
use crate::telemetry::metrics::ENCRYPTED;
use arc_swap::ArcSwapOption;
use rustls::pki_types::{CertificateDer, PrivateKeyDer, pem::PemObject};
use rustls::server::{ClientHello, ResolvesServerCert};
use rustls::sign::CertifiedKey;
use sha2::{Digest, Sha256};
use std::sync::Arc;
use zeroize::Zeroize;

struct Installed {
    key: Arc<CertifiedKey>,
    fingerprint: String,
}

/// The installed certificate; empty until the first successful install, and a
/// handshake with an empty store fails.
pub struct CertStore {
    current: ArcSwapOption<Installed>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct InstalledInfo {
    pub fingerprint_sha256: String,
    pub not_after_unix: i64,
}

impl std::fmt::Debug for CertStore {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("CertStore")
            .field("fingerprint", &self.fingerprint())
            .finish()
    }
}

/// The crypto provider of every client-facing TLS listener.
pub fn provider() -> Arc<rustls::crypto::CryptoProvider> {
    Arc::new(rustls::crypto::aws_lc_rs::default_provider())
}

fn leaf_fingerprint(leaf: &CertificateDer<'_>) -> String {
    hex::encode(Sha256::digest(leaf.as_ref()))
}

impl Default for CertStore {
    fn default() -> Self {
        Self::new()
    }
}

impl CertStore {
    pub fn new() -> Self {
        Self {
            current: ArcSwapOption::empty(),
        }
    }

    pub fn is_empty(&self) -> bool {
        self.current.load().is_none()
    }

    /// Lowercase hex SHA-256 of the installed leaf certificate's DER.
    pub fn fingerprint(&self) -> Option<String> {
        self.current.load().as_ref().map(|i| i.fingerprint.clone())
    }

    /// Validates the chain (leaf first) and key and installs them for new handshakes;
    /// on error the previous certificate stays installed.
    pub fn install_pem(
        &self,
        chain_pem: &[u8],
        key_pem: &[u8],
        now_unix: i64,
    ) -> Result<InstalledInfo, String> {
        let chain: Vec<CertificateDer<'static>> = CertificateDer::pem_slice_iter(chain_pem)
            .collect::<Result<_, _>>()
            .map_err(|_| "invalid certificate PEM".to_string())?;
        let leaf = chain
            .first()
            .ok_or_else(|| "no certificate in chain".to_string())?;
        let (_, parsed) = x509_parser::parse_x509_certificate(leaf.as_ref())
            .map_err(|_| "invalid certificate".to_string())?;
        let not_after_unix = parsed.validity().not_after.timestamp();
        if not_after_unix <= now_unix {
            return Err(format!("certificate expired at {not_after_unix}"));
        }
        let fingerprint = leaf_fingerprint(leaf);
        let key = PrivateKeyDer::from_pem_slice(key_pem)
            .map_err(|_| "invalid private key".to_string())?;
        let certified = CertifiedKey::from_der(chain, key, &provider()).map_err(|e| match e {
            rustls::Error::InconsistentKeys(_) => {
                "private key does not match certificate".to_string()
            }
            _ => "invalid private key".to_string(),
        })?;
        self.current.store(Some(Arc::new(Installed {
            key: Arc::new(certified),
            fingerprint: fingerprint.clone(),
        })));
        ENCRYPTED.set_tls_not_after(not_after_unix);
        Ok(InstalledInfo {
            fingerprint_sha256: fingerprint,
            not_after_unix,
        })
    }

    /// Installs material pushed over the control stream after checking its declared
    /// fingerprint; the key bytes are zeroized before returning.
    pub fn install_material(
        &self,
        mut m: proto::TlsMaterial,
        now_unix: i64,
    ) -> proto::TlsMaterialResult {
        let result = check_fingerprint(&m).and_then(|()| {
            self.install_pem(&m.certificate_chain_pem, &m.private_key_pem, now_unix)
        });
        m.private_key_pem.zeroize();
        let applied = result.is_ok();
        ENCRYPTED.tls_update(applied);
        proto::TlsMaterialResult {
            fingerprint_sha256: m.fingerprint_sha256,
            applied,
            error: result.err().unwrap_or_default(),
        }
    }
}

fn check_fingerprint(m: &proto::TlsMaterial) -> Result<(), String> {
    let leaf = CertificateDer::pem_slice_iter(&m.certificate_chain_pem)
        .next()
        .ok_or_else(|| "no certificate in chain".to_string())?
        .map_err(|_| "invalid certificate PEM".to_string())?;
    if leaf_fingerprint(&leaf) != m.fingerprint_sha256 {
        return Err("fingerprint mismatch".into());
    }
    Ok(())
}

impl ResolvesServerCert for CertStore {
    fn resolve(&self, _hello: ClientHello<'_>) -> Option<Arc<CertifiedKey>> {
        self.current.load().as_ref().map(|i| i.key.clone())
    }
}

/// A TLS 1.2 + 1.3 server config resolving its certificate from `store` on every handshake.
pub fn stream_server_config(store: Arc<CertStore>, alpn: &[&[u8]]) -> Arc<rustls::ServerConfig> {
    let mut cfg = rustls::ServerConfig::builder_with_provider(provider())
        .with_protocol_versions(&[&rustls::version::TLS13, &rustls::version::TLS12])
        .expect("aws-lc-rs supports TLS 1.2 and 1.3")
        .with_no_client_auth()
        .with_cert_resolver(store);
    cfg.alpn_protocols = alpn.iter().map(|p| p.to_vec()).collect();
    Arc::new(cfg)
}

/// The DoQ server config: TLS 1.3 only, ALPN `doq`, no 0-RTT, certificate from `store`.
pub fn quic_server_config(store: Arc<CertStore>) -> quinn::ServerConfig {
    let mut tls = rustls::ServerConfig::builder_with_provider(provider())
        .with_protocol_versions(&[&rustls::version::TLS13])
        .expect("aws-lc-rs supports TLS 1.3")
        .with_no_client_auth()
        .with_cert_resolver(store);
    tls.alpn_protocols = vec![b"doq".to_vec()];
    tls.max_early_data_size = 0; // no 0-RTT: DNS queries must not be replayable
    let crypto = quinn::crypto::rustls::QuicServerConfig::try_from(tls)
        .expect("aws-lc-rs has TLS13_AES_128_GCM_SHA256");
    let mut cfg = quinn::ServerConfig::with_crypto(Arc::new(crypto));
    let mut transport = quinn::TransportConfig::default();
    transport
        .max_concurrent_bidi_streams(100u32.into())
        .max_concurrent_uni_streams(0u32.into())
        .max_idle_timeout(Some(
            std::time::Duration::from_secs(30)
                .try_into()
                .expect("30 s fits a VarInt"),
        ));
    cfg.transport_config(Arc::new(transport));
    cfg.migration(false); // per-worker SO_REUSEPORT endpoints cannot follow a migrated 4-tuple
    cfg
}

#[cfg(test)]
mod tests {
    use super::*;
    use rustls::pki_types::{CertificateDer, ServerName};
    use std::sync::Arc;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    fn self_signed(name: &str) -> (Vec<u8>, Vec<u8>, CertificateDer<'static>) {
        let ck = rcgen::generate_simple_self_signed(vec![name.to_string()]).unwrap();
        (
            ck.cert.pem().into_bytes(),
            ck.signing_key.serialize_pem().into_bytes(),
            ck.cert.der().clone(),
        )
    }

    fn now() -> i64 {
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_secs() as i64
    }

    #[test]
    fn install_rejects_mismatch_expired_and_bad_fingerprint() {
        let store = CertStore::new();
        assert!(store.is_empty());
        let (c1, k1, _) = self_signed("dns.test");
        let (_, k2, _) = self_signed("dns.test");
        assert_eq!(
            store.install_pem(&c1, &k2, now()).unwrap_err(),
            "private key does not match certificate"
        );
        assert!(store.is_empty());
        let info = store.install_pem(&c1, &k1, now()).unwrap();
        assert_eq!(info.fingerprint_sha256.len(), 64);
        assert_eq!(
            store.fingerprint().as_deref(),
            Some(info.fingerprint_sha256.as_str())
        );

        let key = rcgen::KeyPair::generate().unwrap();
        let mut params = rcgen::CertificateParams::new(vec!["dns.test".to_string()]).unwrap();
        params.not_before = rcgen::date_time_ymd(2019, 1, 1);
        params.not_after = rcgen::date_time_ymd(2020, 1, 1);
        let old = params.self_signed(&key).unwrap();
        let err = store
            .install_pem(old.pem().as_bytes(), key.serialize_pem().as_bytes(), now())
            .unwrap_err();
        assert!(err.starts_with("certificate expired at "), "{err}");
        assert_eq!(
            store.fingerprint().as_deref(),
            Some(info.fingerprint_sha256.as_str()),
            "failed install keeps previous"
        );

        let (c3, k3, _) = self_signed("dns.test");
        let r = store.install_material(
            proto::TlsMaterial {
                certificate_chain_pem: c3,
                private_key_pem: k3,
                fingerprint_sha256: "00".repeat(32),
            },
            now(),
        );
        assert!(!r.applied);
        assert_eq!(r.error, "fingerprint mismatch");

        let text = crate::telemetry::metrics::Metrics::new(1).render(
            &crate::runtime::Runtime::initial(),
            &crate::recursor::RecursorState::new(None),
        );
        assert!(
            text.contains("nexora_tls_material_updates_total{result=\"rejected\"} "),
            "{text}"
        );
        assert!(
            text.contains("nexora_tls_certificate_not_after_seconds "),
            "{text}"
        );
    }

    #[tokio::test]
    async fn rotation_applies_to_new_handshakes_without_rebuilding_config() {
        let store = Arc::new(CertStore::new());
        let acceptor =
            tokio_rustls::TlsAcceptor::from(stream_server_config(store.clone(), &[b"dot"]));
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move {
            loop {
                let (tcp, _) = listener.accept().await.unwrap();
                let acceptor = acceptor.clone();
                tokio::spawn(async move {
                    if let Ok(mut s) = acceptor.accept(tcp).await {
                        let mut b = [0u8; 1];
                        let _ = s.read_exact(&mut b).await;
                        let _ = s.write_all(&b).await;
                    }
                });
            }
        });
        async fn handshake(
            addr: std::net::SocketAddr,
            root: &CertificateDer<'static>,
        ) -> Result<CertificateDer<'static>, std::io::Error> {
            let mut roots = rustls::RootCertStore::empty();
            roots.add(root.clone()).unwrap();
            let cfg = rustls::ClientConfig::builder_with_provider(provider())
                .with_safe_default_protocol_versions()
                .unwrap()
                .with_root_certificates(roots)
                .with_no_client_auth();
            let tcp = tokio::net::TcpStream::connect(addr).await?;
            let s = tokio_rustls::TlsConnector::from(Arc::new(cfg))
                .connect(ServerName::try_from("dns.test").unwrap(), tcp)
                .await?;
            Ok(s.get_ref().1.peer_certificates().unwrap()[0].clone())
        }
        let (c1, k1, d1) = self_signed("dns.test");
        assert!(
            handshake(addr, &d1).await.is_err(),
            "no certificate installed: handshake must fail"
        );
        store.install_pem(&c1, &k1, now()).unwrap();
        assert_eq!(handshake(addr, &d1).await.unwrap(), d1);
        let (c2, k2, d2) = self_signed("dns.test");
        store.install_pem(&c2, &k2, now()).unwrap();
        assert_eq!(handshake(addr, &d2).await.unwrap(), d2);
    }
}
