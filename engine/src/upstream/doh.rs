//! DNS over HTTPS: RFC 8484 POST over HTTP/2.

use super::{Question, UpstreamError, client_tls_config, tcp};
use bytes::Bytes;
use std::net::{IpAddr, SocketAddr};
use std::time::Duration;

const HEADER_LEN: usize = 12;
const DNS_MESSAGE: &str = "application/dns-message";
const MAX_REPLY: usize = u16::MAX as usize;

pub struct DohClient {
    client: reqwest::Client,
    url: reqwest::Url,
}

impl DohClient {
    /// `NEXORA_DOH_RESOLVE` (`host=ip,...`) pins host names to addresses, for
    /// certificate names that are not in DNS.
    pub fn new(url: &str, ca_pem: &str) -> Result<DohClient, UpstreamError> {
        let url = reqwest::Url::parse(url)
            .ok()
            .filter(|u| u.scheme() == "https" && u.host_str().is_some())
            .ok_or_else(|| UpstreamError::Tls("doh url must be https".into()))?;
        let mut tls = (*client_tls_config(ca_pem)?).clone();
        tls.alpn_protocols = vec![b"h2".to_vec()];
        let mut builder = reqwest::Client::builder()
            .http2_prior_knowledge()
            .tls_backend_preconfigured(tls)
            .pool_max_idle_per_host(1);
        if let Ok(pins) = std::env::var("NEXORA_DOH_RESOLVE") {
            let port = url.port_or_known_default().unwrap_or(443);
            for pin in pins.split(',').map(str::trim).filter(|p| !p.is_empty()) {
                let (host, ip) = pin
                    .split_once('=')
                    .and_then(|(h, ip)| Some((h.trim(), ip.trim().parse::<IpAddr>().ok()?)))
                    .ok_or_else(|| {
                        std::io::Error::new(
                            std::io::ErrorKind::InvalidInput,
                            format!("NEXORA_DOH_RESOLVE entry {pin:?} is not host=ip"),
                        )
                    })?;
                builder = builder.resolve(host, SocketAddr::new(ip, port));
            }
        }
        let client = builder
            .build()
            .map_err(|e| UpstreamError::Tls(e.to_string()))?;
        Ok(DohClient { client, url })
    }

    /// POSTs `query` with ID 0 and returns the validated reply with the query's ID.
    pub async fn exchange(
        &self,
        query: &[u8],
        question: &Question,
        timeout: Duration,
    ) -> Result<Bytes, UpstreamError> {
        if query.len() < HEADER_LEN || query.len() > MAX_REPLY {
            return Err(UpstreamError::Malformed);
        }
        let mut body = query.to_vec();
        body[..2].fill(0);
        tokio::time::timeout(timeout, async {
            let mut resp = self
                .client
                .post(self.url.clone())
                .header(reqwest::header::CONTENT_TYPE, DNS_MESSAGE)
                .header(reqwest::header::ACCEPT, DNS_MESSAGE)
                .body(body)
                .send()
                .await
                .map_err(http_error)?;
            if resp.status() != reqwest::StatusCode::OK {
                return Err(UpstreamError::Http(resp.status().as_u16()));
            }
            let mut reply = Vec::with_capacity(512);
            while let Some(chunk) = resp.chunk().await.map_err(http_error)? {
                if reply.len() + chunk.len() > MAX_REPLY {
                    return Err(UpstreamError::Malformed);
                }
                reply.extend_from_slice(&chunk);
            }
            if !tcp::reply_matches(&reply, 0, question) {
                return Err(UpstreamError::Malformed);
            }
            reply[..2].copy_from_slice(&query[..2]);
            Ok(Bytes::from(reply))
        })
        .await
        .map_err(|_| UpstreamError::Timeout)?
    }
}

fn http_error(e: reqwest::Error) -> UpstreamError {
    if e.is_timeout() {
        UpstreamError::Timeout
    } else {
        UpstreamError::Io(std::io::Error::other(e))
    }
}
