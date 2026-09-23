//! The management-plane client: join token parsing, CA-pinned enrollment, identity storage, the
//! `Connect` loop (snapshot apply/ack/reject, stats), and blob fetching.

use crate::bootstrap::Bootstrap;
use crate::cert_renewal;
use crate::proto::certificate_request::Reason;
use crate::proto::engine_control_client::EngineControlClient;
use crate::proto::engine_message::Msg;
use crate::proto::server_message::Msg as ServerMsg;
use crate::proto::{
    Applied, CertificateRequest, ConfigSnapshot, EngineMessage, EnrollRequest, GetBlobRequest,
    Hello, Rejected,
};
use crate::server::Shared;
use crate::server::tls::CertStore;
use crate::snapshot::{self, ApplyOutcome, DirBlobs, SnapshotError, verify_blob};
use rand::RngExt;
use rustls::client::WebPkiServerVerifier;
use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::crypto::CryptoProvider;
use rustls::pki_types::{CertificateDer, ServerName, UnixTime};
use rustls::{DigitallySignedStruct, RootCertStore, SignatureScheme};
use sha2::{Digest, Sha256};
use std::os::unix::fs::DirBuilderExt;
use std::path::Path;
use std::sync::atomic::Ordering;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime};
use tokio::sync::mpsc;
use tonic::transport::{Certificate, Channel, ClientTlsConfig, Endpoint};

const CONNECT_TIMEOUT: Duration = Duration::from_secs(3);
const ENROLL_TIMEOUT: Duration = Duration::from_secs(10);
const STATS_INTERVAL: Duration = Duration::from_secs(10);
const ENGINE_VERSION: &str = crate::VERSION;
/// A certificate request younger than this is not repeated.
const PENDING_CSR_TTL: Duration = Duration::from_secs(30);

#[derive(Debug, thiserror::Error)]
pub enum ControlError {
    #[error("join token: {0}")]
    JoinToken(String),
    #[error("enroll: {0}")]
    Enroll(String),
    #[error("tls: {0}")]
    Tls(String),
    #[error("grpc: {0}")]
    Grpc(#[from] tonic::Status),
    #[error("transport: {0}")]
    Transport(#[from] tonic::transport::Error),
    #[error("io: {0}")]
    Io(#[from] std::io::Error),
    #[error("certificate renewed; reconnecting with the new identity")]
    Renewed,
}

pub struct JoinToken {
    pub secret: String,
    pub ca_fingerprint: String,
}

/// Parses `nxj1.<base32 secret>.<64 lowercase hex CA sha256>`, ignoring surrounding whitespace.
pub fn parse_join_token(s: &str) -> Result<JoinToken, ControlError> {
    let mut parts = s.trim().split('.');
    let (Some("nxj1"), Some(secret), Some(fp), None) =
        (parts.next(), parts.next(), parts.next(), parts.next())
    else {
        return Err(ControlError::JoinToken(
            "must be nxj1.<secret>.<ca sha256>".into(),
        ));
    };
    let base32 = |c: u8| c.is_ascii_uppercase() || (b'2'..=b'7').contains(&c);
    if secret.is_empty() || !secret.bytes().all(base32) {
        return Err(ControlError::JoinToken("secret must be base32".into()));
    }
    if !is_sha256_hex(fp) {
        return Err(ControlError::JoinToken(
            "CA fingerprint must be 64 lowercase hex".into(),
        ));
    }
    Ok(JoinToken {
        secret: secret.to_owned(),
        ca_fingerprint: fp.to_owned(),
    })
}

fn is_sha256_hex(h: &str) -> bool {
    h.len() == 64
        && h.bytes()
            .all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c))
}

#[derive(Clone)]
pub struct Identity {
    pub engine_id: String,
    pub cert_pem: String,
    pub key_pem: String,
    pub ca_pem: String,
}

const ID_FILES: [&str; 4] = ["cert.pem", "key.pem", "ca.pem", "engine_id"];

/// The stored identity, or `None` before enrollment. `engine_id` is written last, so a
/// partially written identity reads as absent.
pub fn load_identity(state_dir: &Path) -> std::io::Result<Option<Identity>> {
    load_identity_dir(&state_dir.join("identity"))
}

fn load_identity_dir(dir: &Path) -> std::io::Result<Option<Identity>> {
    let read = |name: &str| std::fs::read_to_string(dir.join(name));
    let engine_id = match read("engine_id") {
        Ok(id) => id.trim().to_owned(),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(e) => return Err(e),
    };
    Ok(Some(Identity {
        engine_id,
        cert_pem: read("cert.pem")?,
        key_pem: read("key.pem")?,
        ca_pem: read("ca.pem")?,
    }))
}

/// Writes the identity under `state_dir/identity/`, every file mode 0600 and atomically replaced.
pub fn save_identity(state_dir: &Path, id: &Identity) -> std::io::Result<()> {
    let dir = state_dir.join("identity");
    std::fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(&dir)?;
    let contents = [&id.cert_pem, &id.key_pem, &id.ca_pem, &id.engine_id];
    for (name, data) in ID_FILES.iter().zip(contents) {
        crate::statefs::write_atomic(&dir.join(name), data.as_bytes())?;
    }
    Ok(())
}

/// The state directory lock ([`crate::statefs::StateLock`]) that serializes every read and change
/// of `identity` and `identity.new` with an engine sharing the directory (rolling update), so no
/// process reads a half-promoted identity or enrolls twice.
async fn identity_lock(boot: &Bootstrap) -> Option<crate::statefs::StateLock> {
    match crate::statefs::StateLock::acquire_async(&boot.state_dir).await {
        Ok(lock) => Some(lock),
        Err(e) => {
            eprintln!("nexora-engine: state directory lock: {e}");
            None
        }
    }
}

/// 500 ms × 2^attempt, capped at 30 s, with ±20 % jitter.
pub fn backoff(attempt: u32) -> Duration {
    reconnect_delay(None, attempt, rand::rng().random_range(0.0..=1.0))
}

/// True for the management plane's refusal of this engine's certificate.
fn refused(s: &tonic::Status) -> bool {
    s.code() == tonic::Code::PermissionDenied
        && matches!(
            s.message(),
            "certificate revoked" | "unknown or deleted engine"
        )
}

/// The wait before the next connection attempt: 300 s ±10 % after a refused certificate,
/// otherwise [`backoff`] with `jitter` (0.0..=1.0) chosen by the caller.
pub fn reconnect_delay(status: Option<&tonic::Status>, attempt: u32, jitter: f64) -> Duration {
    if status.is_some_and(refused) {
        return Duration::from_secs_f64(300.0 * (0.9 + 0.2 * jitter));
    }
    let base = (500u64 << attempt.min(16)).min(30_000) as f64;
    Duration::from_millis((base * (0.8 + 0.4 * jitter)) as u64)
}

/// Host (without IPv6 brackets) and port of an `https://host:port` URL.
fn host_port(url: &str) -> Result<(String, u16), ControlError> {
    let uri: tonic::codegen::http::Uri = url
        .parse()
        .map_err(|e| ControlError::Enroll(format!("url {url}: {e}")))?;
    let host = uri
        .host()
        .ok_or_else(|| ControlError::Enroll(format!("url {url} has no host")))?;
    let host = host
        .trim_start_matches('[')
        .trim_end_matches(']')
        .to_owned();
    Ok((host, uri.port_u16().unwrap_or(443)))
}

/// Accepts the server only when its presented chain carries a certificate with the pinned
/// SHA-256 and the end entity verifies against that certificate as the sole trust anchor.
#[derive(Debug)]
struct PinnedCaVerifier {
    fingerprint: String,
    provider: Arc<CryptoProvider>,
    ca: Mutex<Option<CertificateDer<'static>>>,
}

impl ServerCertVerifier for PinnedCaVerifier {
    fn verify_server_cert(
        &self,
        end_entity: &CertificateDer<'_>,
        intermediates: &[CertificateDer<'_>],
        server_name: &ServerName<'_>,
        ocsp_response: &[u8],
        now: UnixTime,
    ) -> Result<ServerCertVerified, rustls::Error> {
        let mismatch = || rustls::Error::General("CA fingerprint mismatch".into());
        let ca = std::iter::once(end_entity)
            .chain(intermediates)
            .find(|c| hex::encode(Sha256::digest(c.as_ref())) == self.fingerprint)
            .ok_or_else(mismatch)?
            .clone()
            .into_owned();
        let mut roots = RootCertStore::empty();
        roots.add(ca.clone()).map_err(|_| mismatch())?;
        WebPkiServerVerifier::builder_with_provider(Arc::new(roots), self.provider.clone())
            .build()
            .map_err(|e| rustls::Error::General(e.to_string()))?
            .verify_server_cert(end_entity, intermediates, server_name, ocsp_response, now)?;
        *self.ca.lock().unwrap_or_else(|p| p.into_inner()) = Some(ca);
        Ok(ServerCertVerified::assertion())
    }

    fn verify_tls12_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        rustls::crypto::verify_tls12_signature(
            message,
            cert,
            dss,
            &self.provider.signature_verification_algorithms,
        )
    }

    fn verify_tls13_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        rustls::crypto::verify_tls13_signature(
            message,
            cert,
            dss,
            &self.provider.signature_verification_algorithms,
        )
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        self.provider
            .signature_verification_algorithms
            .supported_schemes()
    }
}

fn to_pem(tag: &str, der: &[u8]) -> String {
    pem::encode(&pem::Pem::new(tag, der))
}

/// Connects to the management plane once and returns the CA certificate (PEM) whose SHA-256 is
/// `fingerprint`, after verifying the server against it.
pub async fn fetch_pinned_ca(url: &str, fingerprint: &str) -> Result<String, ControlError> {
    let (host, port) = host_port(url)?;
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let verifier = Arc::new(PinnedCaVerifier {
        fingerprint: fingerprint.to_owned(),
        provider: provider.clone(),
        ca: Mutex::new(None),
    });
    let mut config = rustls::ClientConfig::builder_with_provider(provider)
        .with_safe_default_protocol_versions()
        .map_err(|e| ControlError::Tls(e.to_string()))?
        .dangerous()
        .with_custom_certificate_verifier(verifier.clone())
        .with_no_client_auth();
    config.alpn_protocols = vec![b"h2".to_vec()];
    let name = ServerName::try_from(host.clone())
        .map_err(|e| ControlError::Tls(format!("server name {host}: {e}")))?;
    let handshake = async {
        let tcp = tokio::net::TcpStream::connect((host.as_str(), port)).await?;
        tokio_rustls::TlsConnector::from(Arc::new(config))
            .connect(name, tcp)
            .await
    };
    match tokio::time::timeout(CONNECT_TIMEOUT, handshake).await {
        Err(_) => return Err(ControlError::Tls(format!("{url}: handshake timed out"))),
        Ok(Err(e)) => {
            return Err(ControlError::Tls(
                match e.get_ref().and_then(|i| i.downcast_ref::<rustls::Error>()) {
                    Some(rustls::Error::General(m)) => m.clone(),
                    _ => e.to_string(),
                },
            ));
        }
        Ok(Ok(_)) => {}
    }
    let ca = verifier.ca.lock().unwrap_or_else(|p| p.into_inner()).take();
    ca.map(|der| to_pem("CERTIFICATE", &der))
        .ok_or_else(|| ControlError::Tls("CA fingerprint mismatch".into()))
}

/// Enrolls against `url`: pins the CA from the token, sends a fresh P-256 CSR, and returns the
/// identity the management plane issued.
pub async fn enroll(
    url: &str,
    token: &JoinToken,
    node_name: &str,
) -> Result<Identity, ControlError> {
    let ca_pem = fetch_pinned_ca(url, &token.ca_fingerprint).await?;
    let tls_err = |e: rcgen::Error| ControlError::Tls(e.to_string());
    let key = rcgen::KeyPair::generate_for(&rcgen::PKCS_ECDSA_P256_SHA256).map_err(tls_err)?;
    let csr = rcgen::CertificateParams::default()
        .serialize_request(&key)
        .map_err(tls_err)?;
    let (host, _) = host_port(url)?;
    let channel = Endpoint::from_shared(url.to_owned())?
        .connect_timeout(CONNECT_TIMEOUT)
        .timeout(ENROLL_TIMEOUT)
        .tls_config(
            ClientTlsConfig::new()
                .ca_certificate(Certificate::from_pem(&ca_pem))
                .domain_name(host),
        )?
        .connect()
        .await?;
    let resp = EngineControlClient::new(channel)
        .enroll(EnrollRequest {
            join_secret: token.secret.clone(),
            node_name: node_name.to_owned(),
            csr_der: csr.der().to_vec(),
            engine_version: ENGINE_VERSION.to_owned(),
        })
        .await?
        .into_inner();
    if resp.engine_id.is_empty() || resp.certificate_der.is_empty() {
        return Err(ControlError::Enroll(
            "response lacks engine_id or certificate".into(),
        ));
    }
    if hex::encode(Sha256::digest(&resp.ca_certificate_der)) != token.ca_fingerprint {
        return Err(ControlError::Enroll(
            "response CA differs from the pinned CA".into(),
        ));
    }
    Ok(Identity {
        engine_id: resp.engine_id,
        cert_pem: to_pem("CERTIFICATE", &resp.certificate_der),
        key_pem: key.serialize_pem(),
        ca_pem,
    })
}

/// A mutually authenticated channel to `url`.
pub async fn channel(url: &str, id: &Identity) -> Result<Channel, ControlError> {
    let (host, _) = host_port(url)?;
    Ok(Endpoint::from_shared(url.to_owned())?
        .connect_timeout(CONNECT_TIMEOUT)
        .http2_keep_alive_interval(Duration::from_secs(10))
        .keep_alive_timeout(Duration::from_secs(5))
        .tls_config(
            ClientTlsConfig::new()
                .ca_certificate(Certificate::from_pem(&id.ca_pem))
                .identity(tonic::transport::Identity::from_pem(
                    &id.cert_pem,
                    &id.key_pem,
                ))
                .domain_name(host),
        )?
        .connect()
        .await?)
}

/// Downloads every blob `snap` references that `blob_dir` lacks, verifying each before it lands
/// under its hash.
pub async fn fetch_blobs(
    client: &mut EngineControlClient<Channel>,
    snap: &ConfigSnapshot,
    blob_dir: &Path,
) -> Result<(), SnapshotError> {
    let refs = snap
        .filter
        .iter()
        .flat_map(|f| {
            f.blocklists.iter().chain(&f.allowlists).chain(
                f.blocklist_refs
                    .iter()
                    .chain(&f.allowlist_refs)
                    .filter_map(|r| r.blob.as_ref()),
            )
        })
        .chain(snap.policy_groups.iter().flat_map(|g| {
            g.blocklists
                .iter()
                .chain(g.blocklist_refs.iter().filter_map(|r| r.blob.as_ref()))
        }))
        .chain(snap.rpz_zones.iter().filter_map(|z| match &z.source {
            Some(crate::proto::rpz_zone::Source::File(f)) => f.blob.as_ref(),
            _ => None,
        }))
        .chain(snap.auth_zones.iter().flat_map(|z| {
            z.image
                .iter()
                .chain(z.deltas.iter().filter_map(|d| d.blob.as_ref()))
        }))
        // Collected so no closure is held across an await (keeps the future `Send`).
        .collect::<Vec<_>>();
    // The refs repeat the M1 blobs: fetch each blob once.
    let mut seen = std::collections::HashSet::new();
    let refs: Vec<_> = refs
        .into_iter()
        .filter(|r| seen.insert(r.sha256.as_str()))
        .collect();
    for r in refs {
        let fail = |reason: String| SnapshotError::Blob {
            sha256: r.sha256.clone(),
            reason,
        };
        if !is_sha256_hex(&r.sha256) {
            return Err(fail("sha256 must be 64 lowercase hex".into()));
        }
        let path = blob_dir.join(&r.sha256);
        if tokio::fs::try_exists(&path).await? {
            continue;
        }
        let mut stream = client
            .get_blob(GetBlobRequest {
                sha256: r.sha256.clone(),
            })
            .await
            .map_err(|s| fail(format!("fetch: {}", s.message())))?
            .into_inner();
        let mut data = Vec::new();
        while let Some(chunk) = stream
            .message()
            .await
            .map_err(|s| fail(format!("fetch: {}", s.message())))?
        {
            if (data.len() + chunk.data.len()) as u64 > r.size {
                return Err(fail("larger than its declared size".into()));
            }
            data.extend_from_slice(&chunk.data);
        }
        verify_blob(r, &data)?;
        tokio::fs::create_dir_all(blob_dir).await?;
        // A unique temporary name: an engine sharing this state directory (rolling update) may
        // fetch the same blob at the same time.
        let tmp = crate::statefs::unique_tmp(&path);
        tokio::fs::write(&tmp, &data).await?;
        if let Err(e) = tokio::fs::rename(&tmp, &path).await {
            let _ = tokio::fs::remove_file(&tmp).await;
            return Err(e.into());
        }
    }
    Ok(())
}

/// Enrolls when needed, then keeps a control stream to one of `management_urls` forever. Serving
/// from the last applied snapshot continues whatever happens to the stream.
pub async fn run(shared: Arc<Shared>, boot: Bootstrap, cert_store: Arc<CertStore>) {
    let mut identity = obtain_identity(&boot).await;
    shared.engine_id.store(Arc::new(identity.engine_id.clone()));
    let staged_dir = cert_renewal::staged_dir(&boot.state_dir);
    let mut attempt = 0;
    // The renewed identity that failed for a reason other than authentication: the next attempt
    // uses the current identity instead.
    let mut fallback_from: Option<Identity> = None;
    for url in boot.management_urls.iter().cycle() {
        let lock = identity_lock(&boot).await;
        match load_identity(&boot.state_dir) {
            Ok(Some(id)) => identity = id,
            Ok(None) => {}
            Err(e) => eprintln!("nexora-engine: identity unreadable, using the loaded one: {e}"),
        }
        // A renewed identity is tried first; the current one stays on disk until it works.
        let staged = match fallback_from {
            Some(_) => None,
            None => match load_identity_dir(&staged_dir) {
                Ok(staged) => staged,
                Err(e) => {
                    eprintln!("nexora-engine: renewed identity unreadable: {e}");
                    None
                }
            },
        };
        drop(lock);
        let id = staged.as_ref().unwrap_or(&identity);
        let err = session(
            &shared,
            &boot,
            &cert_store,
            id,
            staged.is_some(),
            fallback_from.as_ref(),
            url,
            &mut attempt,
        )
        .await;
        shared
            .metrics
            .control_connected
            .store(false, Ordering::Relaxed);
        shared.mgmt_channel.store(None);
        let status = match &err {
            ControlError::Grpc(s) => Some(s),
            _ => None,
        };
        if matches!(err, ControlError::Renewed) {
            eprintln!("nexora-engine: control stream to {url}: {err}");
            // The newly staged identity is tried next, not skipped for an older failed one.
            fallback_from = None;
            attempt = 0;
            continue;
        }
        let staged_refused = staged.is_some()
            && staged_dir.exists()
            && status.is_some_and(|s| {
                matches!(
                    s.code(),
                    tonic::Code::PermissionDenied | tonic::Code::Unauthenticated
                )
            });
        if staged_refused {
            eprintln!(
                "nexora-engine: renewed certificate refused by {url}: {err}; keeping the current identity"
            );
            let lock = identity_lock(&boot).await;
            // Only the refused certificate: an engine sharing the state directory may have staged
            // another one meanwhile.
            let discarded = match (load_identity_dir(&staged_dir), &staged) {
                (Ok(Some(on_disk)), Some(refused)) if on_disk.cert_pem != refused.cert_pem => {
                    Ok(())
                }
                _ => cert_renewal::discard_staged(&boot.state_dir),
            };
            drop(lock);
            match discarded {
                Ok(()) => continue,
                Err(e) => eprintln!("nexora-engine: discard renewed identity: {e}"),
            }
        }
        // A staged certificate failing for another reason: the next attempt uses the current
        // identity, which discards the staged one if it reaches the stream. With both failing,
        // they alternate.
        fallback_from = match (&staged, staged_refused) {
            (Some(s), false) => Some(s.clone()),
            _ => None,
        };
        let revoked = status.is_some_and(refused);
        shared
            .metrics
            .control_revoked
            .store(revoked, Ordering::Relaxed);
        let delay = reconnect_delay(status, attempt, rand::rng().random_range(0.0..=1.0));
        if revoked {
            eprintln!(
                "nexora-engine: control stream to {url}: {err}; serving the last applied snapshot, retrying in {}s (recovery needs state_dir/identity removed and a new join token)",
                delay.as_secs()
            );
        } else {
            eprintln!("nexora-engine: control stream to {url}: {err}");
        }
        tokio::time::sleep(delay).await;
        attempt = attempt.saturating_add(1);
    }
}

async fn obtain_identity(boot: &Bootstrap) -> Identity {
    let mut attempt = 0;
    // Enrolled but not yet stored: saving is retried, and the engine never enrolls again.
    let mut unsaved: Option<Identity> = None;
    loop {
        // Held through enrollment: an engine starting beside this one waits and loads the result.
        let lock = identity_lock(boot).await;
        if let Err(e) = cert_renewal::recover_identity(&boot.state_dir) {
            eprintln!("nexora-engine: recover identity: {e}");
        }
        match load_identity(&boot.state_dir) {
            Ok(Some(id)) => return id,
            Ok(None) => {}
            Err(e) => eprintln!("nexora-engine: identity unreadable: {e}"),
        }
        if let Some(id) = unsaved.take() {
            match save_identity(&boot.state_dir, &id) {
                Ok(()) => {
                    eprintln!("nexora-engine: stored identity {}", id.engine_id);
                    return id;
                }
                Err(e) => {
                    eprintln!("nexora-engine: store identity (retrying, not re-enrolling): {e}");
                    unsaved = Some(id);
                    drop(lock);
                    tokio::time::sleep(backoff(attempt)).await;
                    attempt = attempt.saturating_add(1);
                    continue;
                }
            }
        }
        let token = std::fs::read_to_string(&boot.join_token_file)
            .map_err(ControlError::from)
            .and_then(|t| parse_join_token(&t));
        match token {
            Ok(token) => {
                for url in &boot.management_urls {
                    match enroll(url, &token, &boot.node_name).await {
                        Ok(id) => match save_identity(&boot.state_dir, &id) {
                            Ok(()) => {
                                eprintln!("nexora-engine: enrolled as {}", id.engine_id);
                                return id;
                            }
                            Err(e) => {
                                eprintln!("nexora-engine: store identity (kept in memory): {e}");
                                unsaved = Some(id);
                                break;
                            }
                        },
                        Err(e) => eprintln!("nexora-engine: enroll via {url}: {e}"),
                    }
                }
            }
            Err(e) => eprintln!(
                "nexora-engine: join token {}: {e}",
                boot.join_token_file.display()
            ),
        }
        drop(lock);
        tokio::time::sleep(backoff(attempt)).await;
        attempt = attempt.saturating_add(1);
    }
}

fn stream_closed() -> ControlError {
    ControlError::Grpc(tonic::Status::unavailable("control stream closed"))
}

type PendingKey = Arc<parking_lot::Mutex<Option<(rcgen::KeyPair, Instant)>>>;

/// A certificate request for a fresh key when `reason` is rotation or renewal is due, unless a
/// request younger than [`PENDING_CSR_TTL`] is pending. The key stays in memory until issued.
fn certificate_request(
    pending: &PendingKey,
    id: &Identity,
    reason: Reason,
) -> Option<EngineMessage> {
    let due = reason == Reason::Rotate
        || cert_renewal::cert_validity(&id.cert_pem)
            .is_some_and(|(nb, na)| cert_renewal::renewal_due(nb, na, SystemTime::now()));
    let mut pending = pending.lock();
    if !due
        || pending
            .as_ref()
            .is_some_and(|(_, at)| at.elapsed() < PENDING_CSR_TTL)
    {
        return None;
    }
    match cert_renewal::new_csr(&id.engine_id) {
        Ok((csr_der, key)) => {
            *pending = Some((key, Instant::now()));
            Some(EngineMessage {
                msg: Some(Msg::CertRequest(CertificateRequest {
                    csr_der,
                    reason: reason as i32,
                })),
            })
        }
        Err(e) => {
            eprintln!("nexora-engine: certificate request: {e}");
            None
        }
    }
}

/// One control stream; returns why it ended. `staged` marks a renewed identity, promoted once the
/// management plane accepts the stream. `discard_staged` is a renewed identity that failed before;
/// once this stream is accepted it is discarded if `identity.new` still holds it.
#[allow(clippy::too_many_arguments)]
async fn session(
    shared: &Arc<Shared>,
    boot: &Bootstrap,
    cert_store: &CertStore,
    id: &Identity,
    staged: bool,
    discard_staged: Option<&Identity>,
    url: &str,
    attempt: &mut u32,
) -> ControlError {
    let ch = match channel(url, id).await {
        Ok(ch) => ch,
        Err(e) => return e,
    };
    let mut client = EngineControlClient::new(ch.clone());
    let (tx, rx) = mpsc::channel::<EngineMessage>(1024);
    let hello = Msg::Hello(Hello {
        engine_id: id.engine_id.clone(),
        node_name: boot.node_name.clone(),
        applied_version: shared.runtime.load().version,
        engine_version: ENGINE_VERSION.to_owned(),
        tls_fingerprint_sha256: cert_store.fingerprint().unwrap_or_default(),
    });
    if tx.send(EngineMessage { msg: Some(hello) }).await.is_err() {
        return stream_closed();
    }
    let mut inbound = match client
        .connect(tokio_stream::wrappers::ReceiverStream::new(rx))
        .await
    {
        Ok(r) => r.into_inner(),
        Err(s) => return s.into(),
    };
    eprintln!("nexora-engine: control connected to {url}");
    if let Some(failed) = discard_staged {
        let lock = identity_lock(boot).await;
        // Only the certificate that failed: an engine sharing the state directory may have staged
        // another one meanwhile.
        if let Ok(Some(on_disk)) = load_identity_dir(&cert_renewal::staged_dir(&boot.state_dir))
            && on_disk.cert_pem == failed.cert_pem
            && let Err(e) = cert_renewal::discard_staged(&boot.state_dir)
        {
            eprintln!("nexora-engine: discard renewed identity: {e}");
        }
        drop(lock);
    }
    if staged {
        let lock = identity_lock(boot).await;
        // Only the identity this stream authenticated with: an engine sharing the state directory
        // may have promoted or replaced the staged one meanwhile.
        let promoted = match load_identity_dir(&cert_renewal::staged_dir(&boot.state_dir)) {
            Ok(Some(on_disk)) if on_disk.cert_pem == id.cert_pem => {
                cert_renewal::promote_identity(&boot.state_dir)
            }
            _ => Err(std::io::Error::new(
                std::io::ErrorKind::NotFound,
                "staged identity no longer on disk (promoted by another engine on this state directory)",
            )),
        };
        drop(lock);
        match promoted {
            Ok(()) => {
                shared.metrics.cert_renewals.fetch_add(1, Ordering::Relaxed);
                eprintln!(
                    "nexora-engine: certificate renewed (serial {})",
                    cert_renewal::cert_serial(&id.cert_pem)
                );
            }
            Err(e) => eprintln!("nexora-engine: promote renewed identity: {e}"),
        }
    }
    shared
        .metrics
        .control_revoked
        .store(false, Ordering::Relaxed);
    shared.mgmt_channel.store(Some(Arc::new(ch)));
    // NOTIFY and UPDATE forwarding from the workers use this stream while it is up.
    shared.auth.attach(tx.clone());
    shared
        .metrics
        .control_connected
        .store(true, Ordering::Relaxed);
    *attempt = 0;

    let pending: PendingKey = Arc::default();
    if let Some(req) = certificate_request(&pending, id, Reason::Renewal)
        && tx.send(req).await.is_err()
    {
        shared.auth.detach();
        return stream_closed();
    }
    let ticker = tokio::spawn({
        let (tx, shared, pending, id) = (tx.clone(), shared.clone(), pending.clone(), id.clone());
        async move {
            let mut every = tokio::time::interval(STATS_INTERVAL);
            every.tick().await;
            loop {
                every.tick().await;
                let stats = shared
                    .metrics
                    .stats(&shared.runtime.load(), &shared.recursor);
                let msg = EngineMessage {
                    msg: Some(Msg::Stats(stats)),
                };
                if tx.send(msg).await.is_err() {
                    return;
                }
                if let Some(req) = certificate_request(&pending, &id, Reason::Renewal)
                    && tx.send(req).await.is_err()
                {
                    return;
                }
            }
        }
    });
    let err = loop {
        let msg = match inbound.message().await {
            Ok(Some(m)) => m,
            Ok(None) => break stream_closed(),
            Err(s) => break s.into(),
        };
        match msg.msg {
            Some(ServerMsg::Snapshot(snap)) => {
                let reply = apply_snapshot(shared, client.clone(), &boot.state_dir, snap).await;
                if tx.send(reply).await.is_err() {
                    break stream_closed();
                }
            }
            Some(ServerMsg::VersionAhead(v)) => eprintln!(
                "nexora-engine: management plane at version {} is behind engine version {}",
                v.server_version,
                shared.runtime.load().version
            ),
            Some(ServerMsg::TlsMaterial(m)) => {
                // The key bytes are never logged and never reach the persisted snapshot.
                let result = cert_store.install_material(m, crate::clock::unix_now());
                eprintln!(
                    "nexora-engine: tls material {} applied={} error={:?}",
                    result.fingerprint_sha256, result.applied, result.error
                );
                let reply = EngineMessage {
                    msg: Some(Msg::TlsMaterialResult(result)),
                };
                if tx.send(reply).await.is_err() {
                    break stream_closed();
                }
            }
            // The secrets are never logged and never persisted.
            Some(ServerMsg::RpzTsigKeys(keys)) => shared.recursor.rpz.set_tsig_keys(keys),
            // Hosted-zone TSIG keys: never logged, never persisted.
            Some(ServerMsg::KeyMaterial(km)) => shared.auth.keyring.apply(km),
            Some(ServerMsg::UpdateResult(r)) => shared.auth.complete_update(r),
            Some(ServerMsg::RenewCertificate(_)) => {
                if let Some(req) = certificate_request(&pending, id, Reason::Rotate)
                    && tx.send(req).await.is_err()
                {
                    break stream_closed();
                }
            }
            Some(ServerMsg::CertIssued(issued)) => {
                let Some((key, _)) = pending.lock().take() else {
                    eprintln!(
                        "nexora-engine: rejected issued certificate: no certificate request pending"
                    );
                    continue;
                };
                let verified = cert_renewal::verify_issued(
                    &id.engine_id,
                    &id.ca_pem,
                    &key,
                    &issued,
                    SystemTime::now(),
                );
                match verified {
                    Ok(cert_pem) => {
                        // The private key reaches disk only now, 0600, staged beside the current identity.
                        let key_pem = zeroize::Zeroizing::new(key.serialize_pem());
                        let lock = identity_lock(boot).await;
                        let staged = cert_renewal::stage_identity(
                            &boot.state_dir,
                            cert_pem.as_bytes(),
                            key_pem.as_bytes(),
                        );
                        drop(lock);
                        match staged {
                            Ok(()) => break ControlError::Renewed,
                            Err(e) => eprintln!("nexora-engine: store renewed identity: {e}"),
                        }
                    }
                    Err(reason) => {
                        eprintln!("nexora-engine: rejected issued certificate: {reason}")
                    }
                }
            }
            // A full queue drops the reply; the management plane times out with 504.
            Some(ServerMsg::LogRequest(req)) => {
                let batch = crate::telemetry::logbuf::GLOBAL.read(&req);
                let _ = tx.try_send(EngineMessage {
                    msg: Some(Msg::LogBatch(batch)),
                });
            }
            Some(ServerMsg::OdohKeys(k)) => shared.odoh.set_keys(&k),
            None => {}
        }
    };
    ticker.abort();
    shared.auth.detach();
    err
}

async fn apply_snapshot(
    shared: &Arc<Shared>,
    mut client: EngineControlClient<Channel>,
    state_dir: &Path,
    snap: ConfigSnapshot,
) -> EngineMessage {
    let version = snap.version;
    let blob_dir = state_dir.join("blobs");
    let outcome = match fetch_blobs(&mut client, &snap, &blob_dir).await {
        Err(e) => ApplyOutcome::Rejected {
            version,
            reason: e.to_string(),
        },
        Ok(()) => {
            let (shared, state_dir) = (shared.clone(), state_dir.to_owned());
            tokio::task::spawn_blocking(move || {
                let outcome = snapshot::apply(
                    &shared.runtime,
                    snap,
                    &DirBlobs { dir: blob_dir },
                    Some(&state_dir),
                );
                if matches!(outcome, ApplyOutcome::Applied { .. }) {
                    shared.recursor.sync(&shared.runtime.load_full());
                    crate::authoritative::after_apply(&shared, &shared.runtime.load());
                }
                outcome
            })
            .await
            .unwrap_or_else(|e| ApplyOutcome::Rejected {
                version,
                reason: format!("apply failed: {e}"),
            })
        }
    };
    let msg = match outcome {
        ApplyOutcome::Applied {
            version,
            persist_error,
        } => {
            shared
                .metrics
                .config_version
                .store(version, Ordering::Relaxed);
            eprintln!("nexora-engine: applied version {version}");
            if let Some(e) = &persist_error {
                eprintln!("nexora-engine: snapshot not persisted: {e}");
            }
            Msg::Applied(Applied {
                version,
                persist_error: persist_error.unwrap_or_default(),
            })
        }
        ApplyOutcome::Rejected { version, reason } => {
            eprintln!("nexora-engine: rejected version {version}: {reason}");
            Msg::Rejected(Rejected { version, reason })
        }
    };
    EngineMessage { msg: Some(msg) }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn revoked_or_unknown_engine_backs_off_five_minutes() {
        let revoked = tonic::Status::permission_denied("certificate revoked");
        let unknown = tonic::Status::permission_denied("unknown or deleted engine");
        for s in [&revoked, &unknown] {
            assert_eq!(reconnect_delay(Some(s), 0, 0.0), Duration::from_secs(270));
            assert_eq!(reconnect_delay(Some(s), 7, 1.0), Duration::from_secs(330));
        }
        let flaky = tonic::Status::unavailable("connection refused");
        assert!(reconnect_delay(Some(&flaky), 0, 0.5) <= Duration::from_millis(600));
        assert!(reconnect_delay(None, 20, 1.0) <= Duration::from_secs(36));
    }
}
