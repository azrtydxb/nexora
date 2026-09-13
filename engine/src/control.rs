//! The management-plane client: join token parsing, CA-pinned enrollment, identity storage, the
//! `Connect` loop (snapshot apply/ack/reject, stats), and blob fetching.

use crate::bootstrap::Bootstrap;
use crate::proto::engine_control_client::EngineControlClient;
use crate::proto::engine_message::Msg;
use crate::proto::server_message::Msg as ServerMsg;
use crate::proto::{
    Applied, ConfigSnapshot, EngineMessage, EnrollRequest, GetBlobRequest, Hello, Rejected,
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
use std::io::Write;
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::path::Path;
use std::sync::atomic::Ordering;
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tokio::sync::mpsc;
use tonic::transport::{Certificate, Channel, ClientTlsConfig, Endpoint};

const CONNECT_TIMEOUT: Duration = Duration::from_secs(3);
const ENROLL_TIMEOUT: Duration = Duration::from_secs(10);
const STATS_INTERVAL: Duration = Duration::from_secs(10);
const ENGINE_VERSION: &str = crate::VERSION;

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
    let dir = state_dir.join("identity");
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
        let path = dir.join(name);
        let tmp = dir.join(format!("{name}.tmp"));
        let _ = std::fs::remove_file(&tmp);
        let mut f = std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&tmp)?;
        f.write_all(data.as_bytes())?;
        f.sync_all()?;
        std::fs::rename(&tmp, &path)?;
    }
    std::fs::File::open(&dir)?.sync_all()
}

/// 500 ms × 2^attempt, capped at 30 s, with ±20 % jitter.
pub fn backoff(attempt: u32) -> Duration {
    let base = (500u64 << attempt.min(16)).min(30_000) as f64;
    Duration::from_millis((base * rand::rng().random_range(0.8..=1.2)) as u64)
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
        .flat_map(|f| f.blocklists.iter().chain(&f.allowlists))
        .chain(snap.policy_groups.iter().flat_map(|g| &g.blocklists))
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
        let tmp = blob_dir.join(format!("{}.tmp", r.sha256));
        tokio::fs::write(&tmp, &data).await?;
        tokio::fs::rename(&tmp, &path).await?;
    }
    Ok(())
}

/// Enrolls when needed, then keeps a control stream to one of `management_urls` forever.
pub async fn run(shared: Arc<Shared>, boot: Bootstrap, cert_store: Arc<CertStore>) {
    let identity = obtain_identity(&boot).await;
    shared.engine_id.store(Arc::new(identity.engine_id.clone()));
    let mut attempt = 0;
    for url in boot.management_urls.iter().cycle() {
        let err = session(&shared, &boot, &cert_store, &identity, url, &mut attempt).await;
        shared
            .metrics
            .control_connected
            .store(false, Ordering::Relaxed);
        shared.mgmt_channel.store(None);
        eprintln!("nexora-engine: control stream to {url}: {err}");
        tokio::time::sleep(backoff(attempt)).await;
        attempt = attempt.saturating_add(1);
    }
}

async fn obtain_identity(boot: &Bootstrap) -> Identity {
    let mut attempt = 0;
    loop {
        match load_identity(&boot.state_dir) {
            Ok(Some(id)) => return id,
            Ok(None) => {}
            Err(e) => eprintln!("nexora-engine: identity unreadable: {e}"),
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
                            // debt: an identity that cannot be stored is re-enrolled on every
                            // retry, leaving orphan engine rows; revisit if state dirs fail in practice.
                            Err(e) => eprintln!("nexora-engine: store identity: {e}"),
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
        tokio::time::sleep(backoff(attempt)).await;
        attempt = attempt.saturating_add(1);
    }
}

fn stream_closed() -> ControlError {
    ControlError::Grpc(tonic::Status::unavailable("control stream closed"))
}

/// One control stream; returns why it ended.
async fn session(
    shared: &Arc<Shared>,
    boot: &Bootstrap,
    cert_store: &CertStore,
    id: &Identity,
    url: &str,
    attempt: &mut u32,
) -> ControlError {
    let ch = match channel(url, id).await {
        Ok(ch) => ch,
        Err(e) => return e,
    };
    let mut client = EngineControlClient::new(ch.clone());
    let (tx, rx) = mpsc::channel::<EngineMessage>(16);
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
    shared.mgmt_channel.store(Some(Arc::new(ch)));
    shared
        .metrics
        .control_connected
        .store(true, Ordering::Relaxed);
    *attempt = 0;

    let ticker = tokio::spawn({
        let (tx, shared) = (tx.clone(), shared.clone());
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
            // M4 contract (Task 1); TSIG keys arrive with M4 Task 6, update results with Task 11.
            Some(ServerMsg::KeyMaterial(_) | ServerMsg::UpdateResult(_)) => {}
            None => {}
        }
    };
    ticker.abort();
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
