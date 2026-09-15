//! Enrollment against a fake management plane: an identity that cannot be stored stays in memory
//! and is stored once the state directory allows it, without enrolling again.

use nexora_engine::bootstrap::Bootstrap;
use nexora_engine::control::{self, load_identity};
use nexora_engine::proto::engine_control_server::{EngineControl, EngineControlServer};
use nexora_engine::proto::{
    BlobChunk, EngineMessage, EnrollRequest, EnrollResponse, GetBlobRequest, ServerMessage,
};
use nexora_engine::server::Shared;
use nexora_engine::server::tls::CertStore;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::{Duration, SystemTime, UNIX_EPOCH};
use tokio_stream::wrappers::{ReceiverStream, TcpListenerStream};
use tonic::transport::{Server, ServerTlsConfig};
use tonic::{Request, Response, Status, Streaming};

const ENGINE_ID: &str = "11111111-2222-3333-4444-555555555555";

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

/// Answers every enrollment with the same engine id and refuses every control stream.
struct EnrollingMgmt {
    ca: Arc<Ca>,
    enrolls: Arc<AtomicUsize>,
}

#[tonic::async_trait]
impl EngineControl for EnrollingMgmt {
    async fn enroll(
        &self,
        req: Request<EnrollRequest>,
    ) -> Result<Response<EnrollResponse>, Status> {
        self.enrolls.fetch_add(1, Ordering::SeqCst);
        let csr_der = req.into_inner().csr_der;
        Ok(Response::new(EnrollResponse {
            engine_id: ENGINE_ID.into(),
            certificate_der: self.ca.sign_csr(&csr_der),
            ca_certificate_der: self.ca.cert.der().to_vec(),
        }))
    }

    type ConnectStream = ReceiverStream<Result<ServerMessage, Status>>;

    async fn connect(
        &self,
        _: Request<Streaming<EngineMessage>>,
    ) -> Result<Response<Self::ConnectStream>, Status> {
        Err(Status::unavailable("down"))
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
async fn unstorable_identity_is_not_enrolled_again() {
    let _ = rustls::crypto::ring::default_provider().install_default();
    let ca = Arc::new(Ca::new());
    let server_key = p256();
    let mut sp = rcgen::CertificateParams::new(vec!["127.0.0.1".to_string()]).unwrap();
    sp.extended_key_usages = vec![rcgen::ExtendedKeyUsagePurpose::ServerAuth];
    let server_cert = ca.issue(sp, &server_key);

    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    let enrolls = Arc::new(AtomicUsize::new(0));
    let fake = EnrollingMgmt {
        ca: ca.clone(),
        enrolls: enrolls.clone(),
    };
    // The chain carries the CA, so the engine finds the pinned certificate in it.
    let tls = ServerTlsConfig::new().identity(tonic::transport::Identity::from_pem(
        format!("{}{}", server_cert.pem(), ca.cert.pem()),
        server_key.serialize_pem(),
    ));
    let server = tokio::spawn(
        Server::builder()
            .tls_config(tls)
            .unwrap()
            .add_service(EngineControlServer::new(fake))
            .serve_with_incoming(TcpListenerStream::new(listener)),
    );

    let state = tempfile::tempdir().unwrap();
    // A regular file where the identity directory must be created: saving fails.
    std::fs::write(state.path().join("identity"), b"blocked").unwrap();
    let token = state.path().join("join-token");
    let fp = hex::encode(<sha2::Sha256 as sha2::Digest>::digest(ca.cert.der()));
    std::fs::write(
        &token,
        format!("nxj1.MFRGGZDFMZTWQ2LKNNWG23TPOBYXE43U.{fp}\n"),
    )
    .unwrap();
    let boot: Bootstrap = toml::from_str(&format!(
        "node_name = \"enroll-1\"\nstate_dir = {:?}\nmanagement_urls = [\"https://{addr}\"]\njoin_token_file = {:?}\n",
        state.path(),
        token
    ))
    .unwrap();
    let engine = tokio::spawn(control::run(
        Shared::new(1),
        boot,
        Arc::new(CertStore::new()),
    ));

    tokio::time::sleep(Duration::from_secs(4)).await;
    assert_eq!(
        enrolls.load(Ordering::SeqCst),
        1,
        "an unstorable identity is kept, not enrolled again"
    );
    std::fs::remove_file(state.path().join("identity")).unwrap();
    let deadline = tokio::time::Instant::now() + Duration::from_secs(40);
    let stored = loop {
        if let Ok(Some(id)) = load_identity(state.path()) {
            break id;
        }
        assert!(
            tokio::time::Instant::now() < deadline,
            "identity never stored"
        );
        tokio::time::sleep(Duration::from_millis(100)).await;
    };
    assert_eq!(stored.engine_id, ENGINE_ID);
    assert_eq!(enrolls.load(Ordering::SeqCst), 1);

    engine.abort();
    server.abort();
}
