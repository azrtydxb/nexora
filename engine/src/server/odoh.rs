//! RFC 9230 Oblivious DoH target and proxy.
//!
//! The target decrypts `application/oblivious-dns-message` queries with key pairs derived from
//! the seeds the management plane pushes (`OdohKeys`), and seals the answers. The proxy relays an
//! encrypted body to an allow-listed target without any client information. Both run on the DoH
//! listener's own HTTP request path, never on the UDP hot path.

use crate::proto;
use crate::upstream::{client_tls_config, doh::resolve_pins};
use arc_swap::ArcSwap;
use bytes::Bytes;
use http::header::{ACCEPT, CONTENT_TYPE as CONTENT_TYPE_HEADER, HeaderValue};
use http::{Response, StatusCode};
use http_body_util::Full;
use odoh_rs::{
    ObliviousDoHConfig, ObliviousDoHConfigs, ObliviousDoHKeyPair, ObliviousDoHMessage,
    ObliviousDoHMessagePlaintext, OdohSecret, ResponseNonce, compose, decrypt_query,
    encrypt_response, parse,
};
use rand::RngExt;
use std::net::{Ipv6Addr, SocketAddr};
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

pub const CONTENT_TYPE: &str = "application/oblivious-dns-message";
pub const CONFIGS_PATH: &str = "/.well-known/odohconfigs";
/// A DNS message plus the ODoH framing, HPKE encapsulated key and AEAD tag.
pub const MAX_BODY: usize = 65_535 + 1_024;

/// RFC 9230 §9 mandatory suite: DHKEM(X25519, HKDF-SHA256), HKDF-SHA256, AES-128-GCM.
const KEM_X25519: u16 = 0x0020;
const KDF_SHA256: u16 = 0x0001;
const AEAD_AES128GCM: u16 = 0x0001;
const SEED_LEN: usize = 32;
const QUERY_TYPE: u8 = 1;
const DEFAULT_PROXY_TIMEOUT: Duration = Duration::from_millis(2_000);
const MIN_PROXY_TIMEOUT_MS: u64 = 100;
const MAX_PROXY_TIMEOUT_MS: u64 = 10_000;
const PROXY_STATUS: &str = "proxy-status";

struct Key {
    pair: ObliviousDoHKeyPair,
    id: Vec<u8>,
    publish_after: i64,
    not_after: i64,
}

/// The engine's ODoH key pairs, newest first, derived from the pushed seeds.
#[derive(Default)]
pub struct Keyring {
    keys: Vec<Key>,
}

impl Keyring {
    pub fn empty() -> Keyring {
        Keyring::default()
    }

    pub fn from_proto(k: &proto::OdohKeys) -> Result<Keyring, String> {
        let keys = k
            .keys
            .iter()
            .enumerate()
            .map(|(i, key)| {
                if key.seed.len() != SEED_LEN {
                    return Err(format!("odoh key {i}: seed is not {SEED_LEN} octets"));
                }
                let pair = ObliviousDoHKeyPair::from_parameters(
                    KEM_X25519,
                    KDF_SHA256,
                    AEAD_AES128GCM,
                    &key.seed,
                );
                let id = pair
                    .public()
                    .identifier()
                    .map_err(|e| format!("odoh key {i}: {e}"))?;
                Ok(Key {
                    pair,
                    id,
                    publish_after: key.publish_after_unix,
                    not_after: key.not_after_unix,
                })
            })
            .collect::<Result<Vec<_>, String>>()?;
        Ok(Keyring { keys })
    }

    /// The RFC 9230 §6 `ObliviousDoHConfigs` of the published, unexpired keys, newest first;
    /// `None` when no key is published.
    pub fn configs(&self, now_unix: i64) -> Option<Bytes> {
        let listed: Vec<ObliviousDoHConfig> = self
            .keys
            .iter()
            .filter(|k| k.publish_after <= now_unix && now_unix < k.not_after)
            .map(|k| ObliviousDoHConfig::from(k.pair.public().clone()))
            .collect();
        if listed.is_empty() {
            return None;
        }
        compose(&ObliviousDoHConfigs::from(listed))
            .ok()
            .map(|b| b.freeze())
    }
}

/// The key ring shared by every DoH listener; replaced whole on each `OdohKeys` message.
#[derive(Default)]
pub struct OdohState {
    keys: ArcSwap<Keyring>,
}

impl OdohState {
    /// Installs a new key set. An invalid set keeps the previous ring (the management plane
    /// resends the complete set on every change).
    pub fn set_keys(&self, k: &proto::OdohKeys) {
        match Keyring::from_proto(k) {
            Ok(ring) => self.keys.store(Arc::new(ring)),
            Err(e) => crate::log_warn!("odoh keys rejected: {e}"),
        }
    }

    pub fn keyring(&self) -> Arc<Keyring> {
        self.keys.load_full()
    }
}

#[derive(Debug, PartialEq, Eq)]
pub enum Reject {
    UnknownKey,
    Malformed,
}

impl Reject {
    /// RFC 9230 §4.3: 401 for an unknown key, 400 for anything that does not decrypt or parse.
    pub fn status(&self) -> StatusCode {
        match self {
            Reject::UnknownKey => StatusCode::UNAUTHORIZED,
            Reject::Malformed => StatusCode::BAD_REQUEST,
        }
    }
}

/// A decrypted query and the state needed to seal its response.
pub struct Opened {
    /// The plaintext DNS query (secret: never logged).
    pub query: Vec<u8>,
    plain: ObliviousDoHMessagePlaintext,
    secret: OdohSecret,
}

impl Drop for Opened {
    fn drop(&mut self) {
        zeroize::Zeroize::zeroize(&mut self.secret);
    }
}

/// Decrypts an ODoH query body with a key that has not expired (a key is accepted before it is
/// published, so engines hold it before clients learn it).
pub fn open(keys: &Keyring, body: &[u8], now_unix: i64) -> Result<Opened, Reject> {
    if body.first() != Some(&QUERY_TYPE) {
        return Err(Reject::Malformed);
    }
    let mut buf = body;
    let msg: ObliviousDoHMessage = parse(&mut buf).map_err(|_| Reject::Malformed)?;
    if !buf.is_empty() {
        return Err(Reject::Malformed);
    }
    let key = keys
        .keys
        .iter()
        .find(|k| now_unix < k.not_after && k.id == msg.key_id())
        .ok_or(Reject::UnknownKey)?;
    let (plain, secret) = decrypt_query(&msg, &key.pair).map_err(|_| Reject::Malformed)?;
    Ok(Opened {
        query: plain.clone().into_msg().to_vec(),
        plain,
        secret,
    })
}

impl Opened {
    /// Encrypts `response` for the client that sent the query (padding 0).
    pub fn seal(self, response: &[u8]) -> Result<Vec<u8>, Reject> {
        let nonce: ResponseNonce = rand::rng().random();
        let msg = encrypt_response(
            &self.plain,
            &ObliviousDoHMessagePlaintext::new(response, 0),
            self.secret,
            nonce,
        )
        .map_err(|_| Reject::Malformed)?;
        compose(&msg)
            .map(|b| b.to_vec())
            .map_err(|_| Reject::Malformed)
    }
}

/// One allow-listed ODoH target with its HTTPS client.
pub struct ProxyTarget {
    /// Lowercase name or IPv4 literal, or an IPv6 literal in brackets.
    pub host: String,
    pub port: u16,
    pub client: reqwest::Client,
}

pub struct ProxyConfig {
    pub targets: Vec<ProxyTarget>,
    pub timeout: Duration,
}

impl ProxyConfig {
    /// `None` when the proxy is off; an error when it is on without targets or with a target
    /// that does not parse.
    pub fn build(c: &proto::OdohConfig) -> Result<Option<ProxyConfig>, String> {
        if !c.proxy_enabled {
            return Ok(None);
        }
        if c.proxy_targets.is_empty() {
            return Err("odoh proxy enabled without targets".into());
        }
        let pins = resolve_pins().map_err(|e| e.to_string())?;
        let targets = c
            .proxy_targets
            .iter()
            .map(|t| {
                let (host, port) = parse_host(&t.host)
                    .ok_or_else(|| format!("odoh proxy target {:?} is not host[:port]", t.host))?;
                let mut tls = (*client_tls_config(&t.ca_pem)
                    .map_err(|e| format!("odoh proxy target {host}: {e}"))?)
                .clone();
                tls.alpn_protocols = vec![b"h2".to_vec(), b"http/1.1".to_vec()];
                let mut builder = reqwest::Client::builder()
                    .tls_backend_preconfigured(tls)
                    .redirect(reqwest::redirect::Policy::none())
                    .no_proxy();
                for (name, ip) in &pins {
                    builder = builder.resolve(name, SocketAddr::new(*ip, port));
                }
                let client = builder
                    .build()
                    .map_err(|e| format!("odoh proxy target {host}: {e}"))?;
                Ok(ProxyTarget { host, port, client })
            })
            .collect::<Result<Vec<_>, String>>()?;
        let timeout = match u64::from(c.proxy_timeout_ms) {
            0 => DEFAULT_PROXY_TIMEOUT,
            ms => Duration::from_millis(ms.clamp(MIN_PROXY_TIMEOUT_MS, MAX_PROXY_TIMEOUT_MS)),
        };
        Ok(Some(ProxyConfig { targets, timeout }))
    }

    /// The target matching `targethost` by lowercase host and port (no port means 443).
    pub fn allowed(&self, targethost: &str) -> Option<&ProxyTarget> {
        let (host, port) = parse_host(targethost)?;
        self.targets
            .iter()
            .find(|t| t.host == host && t.port == port)
    }
}

/// Parses `name`, `name:port`, `[v6]` or `[v6]:port` into a lowercase host and a port (default
/// 443). Names hold only letters, digits, `-`, `_` and `.`, so userinfo, paths, queries and
/// fragments never parse.
fn parse_host(s: &str) -> Option<(String, u16)> {
    let s = s.to_ascii_lowercase();
    let (host, port) = match s.strip_prefix('[') {
        Some(rest) => {
            let (addr, after) = rest.split_once(']')?;
            addr.parse::<Ipv6Addr>().ok()?;
            let port = match after {
                "" => None,
                p => Some(p.strip_prefix(':')?),
            };
            (format!("[{addr}]"), port)
        }
        None => {
            let (name, port) = match s.split_once(':') {
                Some((n, p)) => (n, Some(p)),
                None => (s.as_str(), None),
            };
            let valid = !name.is_empty()
                && name.len() <= 253
                && name
                    .bytes()
                    .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.'));
            if !valid {
                return None;
            }
            (name.to_string(), port)
        }
    };
    let port = match port {
        None => 443,
        Some(p) if !p.is_empty() && p.bytes().all(|b| b.is_ascii_digit()) => p.parse().ok()?,
        Some(_) => return None,
    };
    (port != 0).then_some((host, port))
}

/// Built from the snapshot's `OdohConfig`; absent config means both roles off.
pub struct OdohRuntime {
    pub target_enabled: bool,
    pub proxy: Option<ProxyConfig>,
}

impl OdohRuntime {
    pub fn off() -> OdohRuntime {
        OdohRuntime {
            target_enabled: false,
            proxy: None,
        }
    }

    pub fn build(c: Option<&proto::OdohConfig>) -> Result<OdohRuntime, String> {
        let Some(c) = c else {
            return Ok(OdohRuntime::off());
        };
        Ok(OdohRuntime {
            target_enabled: c.target_enabled,
            proxy: ProxyConfig::build(c)?,
        })
    }
}

/// RFC 9230 §4.1 `targethost` and `targetpath`, percent-decoded. `Ok(None)` when there is no
/// `targethost` (not a proxy request); an error when a parameter is missing, repeated or
/// malformed.
#[allow(clippy::result_unit_err)] // the only failure is "400 http_request_error"
pub fn parse_proxy_params(query: Option<&str>) -> Result<Option<(String, String)>, ()> {
    let mut host = None;
    let mut path = None;
    for kv in query.unwrap_or("").split('&') {
        let slot = if let Some(v) = kv.strip_prefix("targethost=") {
            (&mut host, v)
        } else if let Some(v) = kv.strip_prefix("targetpath=") {
            (&mut path, v)
        } else {
            continue;
        };
        if slot.0.replace(percent_decode(slot.1)?).is_some() {
            return Err(());
        }
    }
    let Some(host) = host else {
        return Ok(None);
    };
    let path = path.ok_or(())?;
    parse_host(&host).ok_or(())?;
    // A visible-ASCII absolute path, so the target URL always parses.
    if !path.starts_with('/') || !path.bytes().all(|b| b.is_ascii_graphic() && b != b'#') {
        return Err(());
    }
    Ok(Some((host, path)))
}

fn percent_decode(s: &str) -> Result<String, ()> {
    let mut out = Vec::with_capacity(s.len());
    let mut bytes = s.bytes();
    while let Some(b) = bytes.next() {
        if b != b'%' {
            out.push(b);
            continue;
        }
        let hex = [bytes.next().ok_or(())?, bytes.next().ok_or(())?];
        let hex = std::str::from_utf8(&hex).map_err(|_| ())?;
        out.push(u8::from_str_radix(hex, 16).map_err(|_| ())?);
    }
    String::from_utf8(out).map_err(|_| ())
}

fn empty(status: StatusCode) -> http::response::Builder {
    Response::builder().status(status)
}

pub fn reject_response(r: Reject) -> Response<Full<Bytes>> {
    empty(r.status())
        .body(Full::new(Bytes::new()))
        .expect("static response")
}

/// An RFC 9209 `Proxy-Status` error response.
pub fn proxy_error(status: StatusCode, error: &str) -> Response<Full<Bytes>> {
    empty(status)
        .header(PROXY_STATUS, format!("nexora; error={error}"))
        .body(Full::new(Bytes::new()))
        .expect("static response")
}

/// POSTs the encrypted body to the target with only `content-type` and `accept`, and relays the
/// target's status, content type and body (redirects included, unfollowed).
pub async fn forward(
    target: &ProxyTarget,
    targetpath: &str,
    body: Bytes,
    timeout: Duration,
) -> Response<Full<Bytes>> {
    let url = format!("https://{}:{}{}", target.host, target.port, targetpath);
    let mut resp = match target
        .client
        .post(url)
        .header(CONTENT_TYPE_HEADER, CONTENT_TYPE)
        .header(ACCEPT, CONTENT_TYPE)
        .timeout(timeout)
        .body(body)
        .send()
        .await
    {
        Ok(r) => r,
        Err(e) if e.is_builder() => {
            return proxy_error(StatusCode::BAD_REQUEST, "http_request_error");
        }
        Err(e) => return proxy_error(StatusCode::BAD_GATEWAY, error_kind(&e)),
    };
    let status = resp.status();
    let content_type: Option<HeaderValue> = resp.headers().get(CONTENT_TYPE_HEADER).cloned();
    let mut out = Vec::new();
    loop {
        match resp.chunk().await {
            Ok(Some(chunk)) if out.len() + chunk.len() > MAX_BODY => {
                return proxy_error(StatusCode::BAD_GATEWAY, "http_response_body_size");
            }
            Ok(Some(chunk)) => out.extend_from_slice(&chunk),
            Ok(None) => break,
            Err(e) => return proxy_error(StatusCode::BAD_GATEWAY, error_kind(&e)),
        }
    }
    let mut b = empty(status).header(
        PROXY_STATUS,
        format!("nexora; received-status={}", status.as_u16()),
    );
    if let Some(ct) = content_type {
        b = b.header(CONTENT_TYPE_HEADER, ct);
    }
    b.body(Full::new(Bytes::from(out)))
        .expect("relayed status and header")
}

/// RFC 9209 error type of a failed request to a target.
fn error_kind(e: &reqwest::Error) -> &'static str {
    if e.is_timeout() {
        return "connection_timeout";
    }
    let mut next: Option<&(dyn std::error::Error + 'static)> = Some(e);
    while let Some(err) = next {
        if err.is::<rustls::Error>() {
            return "tls_protocol_error";
        }
        // io::Error::source skips its payload, and the connector nests io errors, so a rustls
        // error from the TLS stream is reached through get_ref.
        next = match err.downcast_ref::<std::io::Error>() {
            Some(io) => io
                .get_ref()
                .map(|inner| inner as &(dyn std::error::Error + 'static)),
            None => err.source(),
        };
    }
    "destination_unavailable"
}

const ROLES: [&str; 2] = ["target", "proxy"];
const STATUSES: [&str; 11] = [
    "200", "400", "401", "403", "404", "405", "413", "415", "502", "503", "other",
];

/// `nexora_odoh_requests_total{role,status}`.
pub struct Counters {
    requests: [[AtomicU64; STATUSES.len()]; ROLES.len()],
}

pub static COUNTERS: Counters = Counters {
    requests: [const { [const { AtomicU64::new(0) }; STATUSES.len()] }; ROLES.len()],
};

impl Counters {
    /// Counts one response; `role` is `"target"` or `"proxy"`.
    pub fn count(&self, role: &'static str, status: StatusCode) {
        let Some(ri) = ROLES.iter().position(|r| *r == role) else {
            debug_assert!(false, "unknown odoh role {role}");
            return;
        };
        let code = status.as_str();
        let si = STATUSES
            .iter()
            .position(|s| *s == code)
            .unwrap_or(STATUSES.len() - 1);
        self.requests[ri][si].fetch_add(1, Ordering::Relaxed);
    }

    /// Appends the counter family in OpenMetrics text form.
    pub fn render(&self, out: &mut String) {
        use std::fmt::Write as _;
        out.push_str(
            "# HELP nexora_odoh_requests Oblivious DoH requests by role and HTTP status.\n",
        );
        out.push_str("# TYPE nexora_odoh_requests counter\n");
        for (ri, role) in ROLES.iter().enumerate() {
            for (si, status) in STATUSES.iter().enumerate() {
                let v = self.requests[ri][si].load(Ordering::Relaxed);
                let _ = writeln!(
                    out,
                    "nexora_odoh_requests_total{{role=\"{role}\",status=\"{status}\"}} {v}"
                );
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::proto;
    use odoh_rs::{
        ObliviousDoHConfigs, ObliviousDoHMessage, ObliviousDoHMessagePlaintext, OdohSecret,
        compose, decrypt_response, encrypt_query, parse,
    };

    fn keys(seeds: &[(u8, i64, i64)]) -> Keyring {
        Keyring::from_proto(&proto::OdohKeys {
            keys: seeds
                .iter()
                .map(|&(b, publish_after_unix, not_after_unix)| proto::OdohKey {
                    seed: vec![b; 32],
                    publish_after_unix,
                    not_after_unix,
                })
                .collect(),
        })
        .unwrap()
    }

    fn client_query(
        configs: &Bytes,
        query: &[u8],
    ) -> (Vec<u8>, ObliviousDoHMessagePlaintext, OdohSecret) {
        let cfgs: ObliviousDoHConfigs = parse(&mut configs.clone()).unwrap();
        let cfg = cfgs
            .supported()
            .into_iter()
            .next()
            .expect("a supported config");
        let plain = ObliviousDoHMessagePlaintext::new(query, 0);
        let (msg, secret) = encrypt_query(&plain, &cfg.into(), &mut rand::rng()).unwrap();
        (compose(&msg).unwrap().to_vec(), plain, secret)
    }

    #[test]
    fn target_round_trip() {
        let ring = keys(&[(7, 0, i64::MAX)]);
        let configs = ring.configs(1_000).expect("published");
        let query = b"\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x03www\x07example\x04test\x00\x00\x01\x00\x01";
        let (body, plain, secret) = client_query(&configs, query);
        let opened = open(&ring, &body, 1_000).unwrap();
        assert_eq!(opened.query, query.to_vec());
        let response = b"\x12\x34\x81\x80\x00\x01\x00\x00\x00\x00\x00\x00";
        let sealed = opened.seal(response).unwrap();
        let msg: ObliviousDoHMessage = parse(&mut Bytes::from(sealed)).unwrap();
        assert_eq!(
            decrypt_response(&plain, &msg, secret)
                .unwrap()
                .into_msg()
                .to_vec(),
            response.to_vec()
        );
    }

    #[test]
    fn rejections_map_to_status() {
        let ring = keys(&[(7, 0, 2_000), (8, 5_000, 9_000)]);
        let configs = ring.configs(1_000).unwrap();
        let cfgs: ObliviousDoHConfigs = parse(&mut configs.clone()).unwrap();
        assert_eq!(
            cfgs.supported().len(),
            1,
            "a key before publish_after is not listed"
        );
        let (body, _, _) = client_query(
            &configs,
            b"\x00\x01\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00\x01\x00\x01",
        );
        assert!(open(&ring, &body, 1_000).is_ok());
        assert_eq!(
            open(&ring, &body, 2_001).err(),
            Some(Reject::UnknownKey),
            "an expired key is unknown"
        );
        let mut garbled = body.clone();
        let last = garbled.len() - 1;
        garbled[last] ^= 1;
        assert_eq!(open(&ring, &garbled, 1_000).err(), Some(Reject::Malformed));
        let mut other_key = body.clone();
        other_key[3] ^= 1; // inside the key id
        assert_eq!(
            open(&ring, &other_key, 1_000).err(),
            Some(Reject::UnknownKey)
        );
        assert_eq!(open(&ring, b"\x01", 1_000).err(), Some(Reject::Malformed));
        assert_eq!(Reject::UnknownKey.status(), http::StatusCode::UNAUTHORIZED);
        assert_eq!(Reject::Malformed.status(), http::StatusCode::BAD_REQUEST);
        assert!(Keyring::empty().configs(1_000).is_none());
    }

    #[test]
    fn proxy_target_matching() {
        let cfg = ProxyConfig::build(&proto::OdohConfig {
            proxy_enabled: true,
            proxy_targets: vec![
                proto::OdohProxyTarget {
                    host: "odoh.example".into(),
                    ca_pem: String::new(),
                },
                proto::OdohProxyTarget {
                    host: "[2001:db8::1]:8443".into(),
                    ca_pem: String::new(),
                },
            ],
            ..Default::default()
        })
        .unwrap()
        .expect("proxy on");
        assert!(cfg.allowed("odoh.example").is_some());
        assert!(cfg.allowed("ODOH.example:443").is_some());
        assert!(cfg.allowed("odoh.example:8443").is_none());
        assert!(cfg.allowed("[2001:db8::1]:8443").is_some());
        assert!(cfg.allowed("evil.example").is_none());
        assert_eq!(
            parse_proxy_params(Some("targethost=odoh.example&targetpath=%2Fdns-query")),
            Ok(Some(("odoh.example".to_string(), "/dns-query".to_string())))
        );
        assert_eq!(parse_proxy_params(Some("dns=AAAB")), Ok(None));
        assert_eq!(parse_proxy_params(None), Ok(None));
        assert!(parse_proxy_params(Some("targethost=odoh.example")).is_err());
        assert!(parse_proxy_params(Some("targethost=odoh.example&targetpath=dns-query")).is_err());
        assert!(
            parse_proxy_params(Some("targethost=user%40odoh.example&targetpath=%2Fq")).is_err()
        );
        assert!(parse_proxy_params(Some("targethost=odoh.example%2Fx&targetpath=%2Fq")).is_err());
        assert!(
            ProxyConfig::build(&proto::OdohConfig {
                proxy_enabled: true,
                ..Default::default()
            })
            .is_err()
        );
        assert!(
            ProxyConfig::build(&proto::OdohConfig::default())
                .unwrap()
                .is_none()
        );
        let err = proxy_error(http::StatusCode::FORBIDDEN, "http_request_denied");
        assert_eq!(
            err.headers()["proxy-status"],
            "nexora; error=http_request_denied"
        );
    }

    fn proxy_to(port: u16, ca_pem: &str) -> ProxyConfig {
        ProxyConfig::build(&proto::OdohConfig {
            proxy_enabled: true,
            proxy_targets: vec![proto::OdohProxyTarget {
                host: format!("127.0.0.1:{port}"),
                ca_pem: ca_pem.to_string(),
            }],
            proxy_timeout_ms: 300,
            ..Default::default()
        })
        .unwrap()
        .unwrap()
    }

    fn proxy_status(resp: &Response<Full<Bytes>>) -> &str {
        resp.headers()[PROXY_STATUS].to_str().unwrap()
    }

    /// A TLS target on 127.0.0.1 that answers a relayed request with a redirect after checking the
    /// proxy sent nothing but the body and the ODoH headers; otherwise 418 with the header names.
    async fn redirecting_target(ck: &rcgen::CertifiedKey<rcgen::KeyPair>) -> std::net::SocketAddr {
        use hyper::service::service_fn;
        use hyper_util::rt::{TokioExecutor, TokioIo};
        let store = Arc::new(crate::server::tls::CertStore::new());
        store
            .install_pem(
                ck.cert.pem().as_bytes(),
                ck.signing_key.serialize_pem().as_bytes(),
                crate::clock::unix_now(),
            )
            .unwrap();
        let acceptor = tokio_rustls::TlsAcceptor::from(crate::server::tls::stream_server_config(
            store,
            &[b"h2", b"http/1.1"],
        ));
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move {
            loop {
                let (tcp, _) = listener.accept().await.unwrap();
                let acceptor = acceptor.clone();
                tokio::spawn(async move {
                    let Ok(tls) = acceptor.accept(tcp).await else {
                        return;
                    };
                    let svc = service_fn(|req: http::Request<hyper::body::Incoming>| async move {
                        let mut names: Vec<&str> =
                            req.headers().keys().map(|k| k.as_str()).collect();
                        names.sort_unstable();
                        let seen = names.join(",");
                        let ok = req.method() == http::Method::POST
                            && req.uri().path() == "/dns-query"
                            // content-length is framing added by hyper, not client data.
                            && names == ["accept", "content-length", "content-type"]
                            && req.headers()[CONTENT_TYPE_HEADER] == CONTENT_TYPE;
                        let body = http_body_util::BodyExt::collect(req.into_body())
                            .await
                            .unwrap()
                            .to_bytes();
                        let status = if ok && body.as_ref() == b"sealed" {
                            StatusCode::TEMPORARY_REDIRECT
                        } else {
                            StatusCode::IM_A_TEAPOT
                        };
                        Ok::<_, std::convert::Infallible>(
                            Response::builder()
                                .status(status)
                                .header("location", "https://elsewhere.example/dns-query")
                                .header(CONTENT_TYPE_HEADER, CONTENT_TYPE)
                                .body(Full::new(if status == StatusCode::TEMPORARY_REDIRECT {
                                    Bytes::from_static(b"moved")
                                } else {
                                    Bytes::from(seen)
                                }))
                                .unwrap(),
                        )
                    });
                    let _ = hyper_util::server::conn::auto::Builder::new(TokioExecutor::new())
                        .serve_connection(TokioIo::new(tls), svc)
                        .await;
                });
            }
        });
        addr
    }

    #[tokio::test]
    async fn forward_relays_target_response_and_maps_failures() {
        let ck = rcgen::generate_simple_self_signed(vec!["127.0.0.1".to_string()]).unwrap();
        let addr = redirecting_target(&ck).await;

        let cfg = proxy_to(addr.port(), &ck.cert.pem());
        let target = cfg.allowed(&format!("127.0.0.1:{}", addr.port())).unwrap();
        let resp = forward(
            target,
            "/dns-query",
            Bytes::from_static(b"sealed"),
            cfg.timeout,
        )
        .await;
        if resp.status() != StatusCode::TEMPORARY_REDIRECT {
            let body = http_body_util::BodyExt::collect(resp.into_body())
                .await
                .unwrap();
            panic!(
                "target rejected the relay; it saw headers {:?}",
                body.to_bytes()
            );
        }
        assert_eq!(proxy_status(&resp), "nexora; received-status=307");
        assert_eq!(resp.headers()[CONTENT_TYPE_HEADER], CONTENT_TYPE);
        let body = http_body_util::BodyExt::collect(resp.into_body())
            .await
            .unwrap();
        assert_eq!(body.to_bytes().as_ref(), b"moved");

        // The same target without its CA: the certificate does not verify.
        let untrusted = proxy_to(addr.port(), "");
        let resp = forward(
            &untrusted.targets[0],
            "/dns-query",
            Bytes::from_static(b"sealed"),
            untrusted.timeout,
        )
        .await;
        assert_eq!(resp.status(), StatusCode::BAD_GATEWAY);
        assert_eq!(proxy_status(&resp), "nexora; error=tls_protocol_error");

        // Accepts TCP and never speaks.
        let silent = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let slow = proxy_to(silent.local_addr().unwrap().port(), &ck.cert.pem());
        let resp = forward(&slow.targets[0], "/dns-query", Bytes::new(), slow.timeout).await;
        assert_eq!(resp.status(), StatusCode::BAD_GATEWAY);
        assert_eq!(proxy_status(&resp), "nexora; error=connection_timeout");

        let closed = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
        let port = closed.local_addr().unwrap().port();
        drop(closed);
        let gone = proxy_to(port, &ck.cert.pem());
        let resp = forward(&gone.targets[0], "/dns-query", Bytes::new(), gone.timeout).await;
        assert_eq!(resp.status(), StatusCode::BAD_GATEWAY);
        assert_eq!(proxy_status(&resp), "nexora; error=destination_unavailable");
        drop(silent);
    }

    #[test]
    fn counters_render_by_role_and_status() {
        let c = Counters {
            requests: [const { [const { AtomicU64::new(0) }; STATUSES.len()] }; ROLES.len()],
        };
        c.count("target", StatusCode::OK);
        c.count("proxy", StatusCode::FORBIDDEN);
        c.count("proxy", StatusCode::IM_A_TEAPOT);
        let mut out = String::new();
        c.render(&mut out);
        assert!(out.contains("nexora_odoh_requests_total{role=\"target\",status=\"200\"} 1\n"));
        assert!(out.contains("nexora_odoh_requests_total{role=\"proxy\",status=\"403\"} 1\n"));
        assert!(out.contains("nexora_odoh_requests_total{role=\"proxy\",status=\"other\"} 1\n"));
        assert!(out.contains("nexora_odoh_requests_total{role=\"target\",status=\"403\"} 0\n"));
    }
}
