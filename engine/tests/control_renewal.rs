//! The engine's control loop against a fake management plane: renewal at 2/3 of the lifetime,
//! operator rotation, revocation (with the fallback from a refused renewed identity), and the
//! fallback from a renewed identity failing for another reason.

use nexora_engine::bootstrap::Bootstrap;
use nexora_engine::control::{self, Identity, load_identity, save_identity};
use nexora_engine::proto::certificate_request::Reason;
use nexora_engine::proto::engine_control_server::{EngineControl, EngineControlServer};
use nexora_engine::proto::engine_message::Msg;
use nexora_engine::proto::server_message::Msg as ServerMsg;
use nexora_engine::proto::{
    BlobChunk, CertificateIssued, EngineMessage, EnrollRequest, EnrollResponse, GetBlobRequest,
    RenewCertificate, ServerMessage,
};
use nexora_engine::server::Shared;
use nexora_engine::server::tls::CertStore;
use std::os::unix::fs::PermissionsExt;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::{Duration, SystemTime, UNIX_EPOCH};
use tokio::sync::mpsc;
use tokio_stream::wrappers::{ReceiverStream, TcpListenerStream};
use tonic::transport::{Certificate, Server, ServerTlsConfig};
use tonic::{Request, Response, Status, Streaming};

const ENGINE_ID: &str = "0b7c1f5e-8f4f-4d47-9a55-3f4f0f6d2c11";
const REVOKED: &str = "certificate revoked";

fn set_validity(p: &mut rcgen::CertificateParams, not_before: SystemTime, not_after: SystemTime) {
    let dt = |t: SystemTime| {
        let secs = t.duration_since(UNIX_EPOCH).unwrap().as_secs() as i64;
        x509_parser::time::ASN1Time::from_timestamp(secs)
            .unwrap()
            .to_datetime()
    };
    p.not_before = dt(not_before);
    p.not_after = dt(not_after);
}

fn p256() -> rcgen::KeyPair {
    rcgen::KeyPair::generate_for(&rcgen::PKCS_ECDSA_P256_SHA256).unwrap()
}

struct Ca {
    params: rcgen::CertificateParams,
    key: rcgen::KeyPair,
    cert: rcgen::Certificate,
}

impl Ca {
    fn new() -> Ca {
        let key = p256();
        let mut params = rcgen::CertificateParams::default();
        params.is_ca = rcgen::IsCa::Ca(rcgen::BasicConstraints::Unconstrained);
        params
            .distinguished_name
            .push(rcgen::DnType::CommonName, "fake nexora ca");
        let cert = params.self_signed(&key).unwrap();
        Ca { params, key, cert }
    }

    fn issue(
        &self,
        params: rcgen::CertificateParams,
        public_key: &impl rcgen::PublicKeyData,
    ) -> rcgen::Certificate {
        params
            .signed_by(
                public_key,
                &rcgen::Issuer::from_params(&self.params, &self.key),
            )
            .unwrap()
    }

    /// Signs an engine CSR for an hour as a client certificate, like the management plane.
    fn sign_csr(&self, csr_der: &[u8]) -> Vec<u8> {
        let csr = rcgen::CertificateSigningRequestParams::from_der(&csr_der.to_vec().into())
            .expect("engine CSR parses");
        let mut params = csr.params;
        params.extended_key_usages = vec![rcgen::ExtendedKeyUsagePurpose::ClientAuth];
        let now = SystemTime::now();
        set_validity(
            &mut params,
            now - Duration::from_secs(60),
            now + Duration::from_secs(3600),
        );
        self.issue(params, &csr.public_key).der().to_vec()
    }
}

#[derive(Debug, PartialEq)]
enum Event {
    Connect { n: usize, serial: String },
    CertRequest { n: usize, reason: i32 },
}

struct FakeMgmt {
    ca: Arc<Ca>,
    conns: AtomicUsize,
    events: mpsc::UnboundedSender<Event>,
}

#[tonic::async_trait]
impl EngineControl for FakeMgmt {
    async fn enroll(&self, _: Request<EnrollRequest>) -> Result<Response<EnrollResponse>, Status> {
        Err(Status::unimplemented("enroll"))
    }

    type ConnectStream = ReceiverStream<Result<ServerMessage, Status>>;

    /// Connection 0 (the aging certificate) answers the renewal request; connection 1 (the
    /// renewed certificate) asks for rotation and answers it; later connections are refused.
    async fn connect(
        &self,
        req: Request<Streaming<EngineMessage>>,
    ) -> Result<Response<Self::ConnectStream>, Status> {
        let certs = req
            .peer_certs()
            .ok_or_else(|| Status::unauthenticated("client certificate required"))?;
        let (_, leaf) = x509_parser::parse_x509_certificate(&certs[0]).unwrap();
        let n = self.conns.fetch_add(1, Ordering::SeqCst);
        let _ = self.events.send(Event::Connect {
            n,
            serial: leaf.serial.to_str_radix(16),
        });
        if n >= 2 {
            return Err(Status::permission_denied(REVOKED));
        }
        let mut inbound = req.into_inner();
        let (tx, rx) = mpsc::channel(8);
        let (ca, events) = (self.ca.clone(), self.events.clone());
        tokio::spawn(async move {
            if n == 1 {
                let renew = ServerMsg::RenewCertificate(RenewCertificate {
                    reason: Reason::Rotate as i32,
                });
                let _ = tx.send(Ok(ServerMessage { msg: Some(renew) })).await;
            }
            while let Ok(Some(m)) = inbound.message().await {
                if let Some(Msg::CertRequest(cr)) = m.msg {
                    let _ = events.send(Event::CertRequest {
                        n,
                        reason: cr.reason,
                    });
                    let issued = ServerMsg::CertIssued(CertificateIssued {
                        cert_der: ca.sign_csr(&cr.csr_der),
                        ca_der: ca.cert.der().to_vec(),
                    });
                    let _ = tx.send(Ok(ServerMessage { msg: Some(issued) })).await;
                }
            }
        });
        Ok(Response::new(ReceiverStream::new(rx)))
    }

    type GetBlobStream = ReceiverStream<Result<BlobChunk, Status>>;

    async fn get_blob(
        &self,
        _: Request<GetBlobRequest>,
    ) -> Result<Response<Self::GetBlobStream>, Status> {
        Err(Status::not_found("no blobs"))
    }
}

fn serial_of(cert_pem: &str) -> String {
    nexora_engine::cert_renewal::cert_serial(cert_pem)
}

async fn next(events: &mut mpsc::UnboundedReceiver<Event>) -> Event {
    tokio::time::timeout(Duration::from_secs(15), events.recv())
        .await
        .expect("fake management plane saw no event within 15 s")
        .expect("event channel open")
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn renews_rotates_and_backs_off_when_revoked() {
    // The fake server needs a process provider; the engine client selects ring itself.
    let _ = rustls::crypto::ring::default_provider().install_default();
    let ca = Arc::new(Ca::new());
    let now = SystemTime::now();

    let server_key = p256();
    let mut server_params = rcgen::CertificateParams::new(vec!["127.0.0.1".to_string()]).unwrap();
    server_params.extended_key_usages = vec![rcgen::ExtendedKeyUsagePurpose::ServerAuth];
    let server_cert = ca.issue(server_params, &server_key);

    // 100 s into a 150 s lifetime: renewal is due at once, and the certificate is still valid.
    let engine_key = p256();
    let mut engine_params = rcgen::CertificateParams::default();
    engine_params
        .distinguished_name
        .push(rcgen::DnType::CommonName, ENGINE_ID);
    engine_params.extended_key_usages = vec![rcgen::ExtendedKeyUsagePurpose::ClientAuth];
    set_validity(
        &mut engine_params,
        now - Duration::from_secs(100),
        now + Duration::from_secs(50),
    );
    let engine_cert = ca.issue(engine_params, &engine_key);

    let state = tempfile::tempdir().unwrap();
    let first = Identity {
        engine_id: ENGINE_ID.into(),
        cert_pem: engine_cert.pem(),
        key_pem: engine_key.serialize_pem(),
        ca_pem: ca.cert.pem(),
    };
    save_identity(state.path(), &first).unwrap();

    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let (events_tx, mut events) = mpsc::unbounded_channel();
    let fake = FakeMgmt {
        ca: ca.clone(),
        conns: AtomicUsize::new(0),
        events: events_tx,
    };
    let tls = ServerTlsConfig::new()
        .identity(tonic::transport::Identity::from_pem(
            server_cert.pem(),
            server_key.serialize_pem(),
        ))
        .client_ca_root(Certificate::from_pem(ca.cert.pem()));
    let server = tokio::spawn(
        Server::builder()
            .tls_config(tls)
            .unwrap()
            .add_service(EngineControlServer::new(fake))
            .serve_with_incoming(TcpListenerStream::new(listener)),
    );

    let boot: Bootstrap = toml::from_str(&format!(
        "node_name = \"renew-1\"\nstate_dir = {:?}\nmanagement_urls = [\"https://{addr}\"]\n",
        state.path()
    ))
    .unwrap();
    let shared = Shared::new(1);
    let engine = tokio::spawn(control::run(
        shared.clone(),
        boot,
        Arc::new(CertStore::new()),
    ));

    let first_serial = serial_of(&first.cert_pem);
    assert_eq!(
        next(&mut events).await,
        Event::Connect {
            n: 0,
            serial: first_serial.clone()
        }
    );
    assert_eq!(
        next(&mut events).await,
        Event::CertRequest {
            n: 0,
            reason: Reason::Renewal as i32
        }
    );

    // Reconnects at once with the renewed identity, which is promoted once the stream is up.
    let Event::Connect { n: 1, serial } = next(&mut events).await else {
        panic!("second connection expected");
    };
    assert_ne!(serial, first_serial, "the renewed certificate is presented");
    assert_eq!(
        next(&mut events).await,
        Event::CertRequest {
            n: 1,
            reason: Reason::Rotate as i32
        }
    );
    let renewed = load_identity(state.path()).unwrap().unwrap();
    assert_eq!(serial_of(&renewed.cert_pem), serial);
    assert_ne!(
        renewed.key_pem, first.key_pem,
        "a renewal never reuses the key"
    );
    assert_eq!(renewed.ca_pem, first.ca_pem);
    assert_eq!(renewed.engine_id, ENGINE_ID);

    // The rotated certificate is refused: the engine falls back to the renewed identity at once,
    // which is refused too, and then backs off for about five minutes while still serving.
    let Event::Connect {
        n: 2,
        serial: rotated,
    } = next(&mut events).await
    else {
        panic!("third connection expected");
    };
    assert_ne!(rotated, serial);
    assert_eq!(
        next(&mut events).await,
        Event::Connect {
            n: 3,
            serial: serial.clone()
        }
    );
    let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
    while !shared.metrics.control_revoked.load(Ordering::Relaxed) {
        assert!(
            tokio::time::Instant::now() < deadline,
            "control_revoked not set"
        );
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    assert!(!shared.metrics.control_connected.load(Ordering::Relaxed));
    assert_eq!(shared.metrics.cert_renewals.load(Ordering::Relaxed), 1);
    tokio::time::sleep(Duration::from_secs(1)).await;
    assert!(
        events.try_recv().is_err(),
        "no retry before the revoked backoff"
    );

    let kept = load_identity(state.path()).unwrap().unwrap();
    assert_eq!(
        serial_of(&kept.cert_pem),
        serial,
        "the working identity is kept"
    );
    assert!(
        !state.path().join("identity.new").exists(),
        "the refused identity is discarded"
    );
    assert!(!state.path().join("identity.old").exists());
    let mode = std::fs::metadata(state.path().join("identity/key.pem"))
        .unwrap()
        .permissions()
        .mode();
    assert_eq!(mode & 0o777, 0o600);

    engine.abort();
    server.abort();
}

/// Refuses the staged certificate with a non-authentication status and accepts every other one.
struct StagedUnavailableMgmt {
    staged_serial: String,
    conns: AtomicUsize,
    events: mpsc::UnboundedSender<Event>,
}

#[tonic::async_trait]
impl EngineControl for StagedUnavailableMgmt {
    async fn enroll(&self, _: Request<EnrollRequest>) -> Result<Response<EnrollResponse>, Status> {
        Err(Status::unimplemented("enroll"))
    }

    type ConnectStream = ReceiverStream<Result<ServerMessage, Status>>;

    async fn connect(
        &self,
        req: Request<Streaming<EngineMessage>>,
    ) -> Result<Response<Self::ConnectStream>, Status> {
        let certs = req
            .peer_certs()
            .ok_or_else(|| Status::unauthenticated("client certificate required"))?;
        let (_, leaf) = x509_parser::parse_x509_certificate(&certs[0]).unwrap();
        let serial = leaf.serial.to_str_radix(16);
        let n = self.conns.fetch_add(1, Ordering::SeqCst);
        let _ = self.events.send(Event::Connect {
            n,
            serial: serial.clone(),
        });
        if serial == self.staged_serial {
            return Err(Status::unavailable("certificate not usable yet"));
        }
        let (tx, rx) = mpsc::channel(8);
        let mut inbound = req.into_inner();
        tokio::spawn(async move {
            while let Ok(Some(_)) = inbound.message().await {}
            drop(tx);
        });
        Ok(Response::new(ReceiverStream::new(rx)))
    }

    type GetBlobStream = ReceiverStream<Result<BlobChunk, Status>>;

    async fn get_blob(
        &self,
        _: Request<GetBlobRequest>,
    ) -> Result<Response<Self::GetBlobStream>, Status> {
        Err(Status::not_found("no blobs"))
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn staged_certificate_failing_otherwise_falls_back_and_is_discarded() {
    let _ = rustls::crypto::ring::default_provider().install_default();
    let ca = Ca::new();
    let now = SystemTime::now();
    let server_key = p256();
    let mut sp = rcgen::CertificateParams::new(vec!["127.0.0.1".to_string()]).unwrap();
    sp.extended_key_usages = vec![rcgen::ExtendedKeyUsagePurpose::ServerAuth];
    let server_cert = ca.issue(sp, &server_key);
    // Both certificates are young (renewal not due), so no certificate request interferes.
    let engine_cert = |key: &rcgen::KeyPair| {
        let mut p = rcgen::CertificateParams::default();
        p.distinguished_name
            .push(rcgen::DnType::CommonName, ENGINE_ID);
        p.extended_key_usages = vec![rcgen::ExtendedKeyUsagePurpose::ClientAuth];
        set_validity(
            &mut p,
            now - Duration::from_secs(60),
            now + Duration::from_secs(3600),
        );
        ca.issue(p, key)
    };
    let (current_key, staged_key) = (p256(), p256());
    let (current, staged) = (engine_cert(&current_key), engine_cert(&staged_key));
    let state = tempfile::tempdir().unwrap();
    save_identity(
        state.path(),
        &Identity {
            engine_id: ENGINE_ID.into(),
            cert_pem: current.pem(),
            key_pem: current_key.serialize_pem(),
            ca_pem: ca.cert.pem(),
        },
    )
    .unwrap();
    nexora_engine::cert_renewal::stage_identity(
        state.path(),
        staged.pem().as_bytes(),
        staged_key.serialize_pem().as_bytes(),
    )
    .unwrap();

    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let (events_tx, mut events) = mpsc::unbounded_channel();
    let fake = StagedUnavailableMgmt {
        staged_serial: serial_of(&staged.pem()),
        conns: AtomicUsize::new(0),
        events: events_tx,
    };
    let tls = ServerTlsConfig::new()
        .identity(tonic::transport::Identity::from_pem(
            server_cert.pem(),
            server_key.serialize_pem(),
        ))
        .client_ca_root(Certificate::from_pem(ca.cert.pem()));
    let server = tokio::spawn(
        Server::builder()
            .tls_config(tls)
            .unwrap()
            .add_service(EngineControlServer::new(fake))
            .serve_with_incoming(TcpListenerStream::new(listener)),
    );
    let boot: Bootstrap = toml::from_str(&format!(
        "node_name = \"fallback-1\"\nstate_dir = {:?}\nmanagement_urls = [\"https://{addr}\"]\n",
        state.path()
    ))
    .unwrap();
    let shared = Shared::new(1);
    let engine = tokio::spawn(control::run(
        shared.clone(),
        boot,
        Arc::new(CertStore::new()),
    ));

    assert_eq!(
        next(&mut events).await,
        Event::Connect {
            n: 0,
            serial: serial_of(&staged.pem())
        }
    );
    assert_eq!(
        next(&mut events).await,
        Event::Connect {
            n: 1,
            serial: serial_of(&current.pem())
        },
        "after a non-authentication failure the current identity is tried"
    );
    let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
    while nexora_engine::cert_renewal::staged_dir(state.path()).exists() {
        assert!(
            tokio::time::Instant::now() < deadline,
            "the staged certificate was not discarded"
        );
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    assert_eq!(
        serial_of(&load_identity(state.path()).unwrap().unwrap().cert_pem),
        serial_of(&current.pem())
    );

    engine.abort();
    server.abort();
}
