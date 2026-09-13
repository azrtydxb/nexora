//! `engine.toml`: host-level settings that are not part of the snapshot.

use crate::server::proxy::ProxyPolicy;
use anyhow::{Context, bail};
use std::net::SocketAddr;
use std::path::{Path, PathBuf};

#[derive(Debug, Clone, serde::Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Bootstrap {
    pub node_name: String,
    pub state_dir: PathBuf,
    #[serde(default)]
    pub management_urls: Vec<String>,
    #[serde(default = "default_join_token_file")]
    pub join_token_file: PathBuf,
    #[serde(default = "default_listen")]
    pub listen_udp: Vec<SocketAddr>,
    #[serde(default = "default_listen")]
    pub listen_tcp: Vec<SocketAddr>,
    #[serde(default = "default_metrics_listen")]
    pub metrics_listen: SocketAddr,
    #[serde(default)]
    pub workers: usize,
    #[serde(default)]
    pub standalone_snapshot: String,
    #[serde(default)]
    pub standalone_blob_dir: String,
    /// DNS-over-TLS listeners, e.g. `["0.0.0.0:853"]`.
    #[serde(default)]
    pub listen_dot: Vec<SocketAddr>,
    /// DNS-over-HTTPS listeners, e.g. `["0.0.0.0:443"]`.
    #[serde(default)]
    pub listen_doh: Vec<SocketAddr>,
    /// DNS-over-QUIC listeners (UDP), e.g. `["0.0.0.0:853"]`.
    #[serde(default)]
    pub listen_doq: Vec<SocketAddr>,
    #[serde(default = "default_doh_path")]
    pub doh_path: String,
    #[serde(default)]
    pub proxy_protocol_dot: bool,
    #[serde(default)]
    pub proxy_protocol_doh: bool,
    #[serde(default)]
    pub proxy_protocol_trusted_cidrs: Vec<String>,
    /// Standalone mode only; set together with `tls_key_file`.
    #[serde(default)]
    pub tls_cert_file: String,
    /// Standalone mode only; reloaded on SIGHUP.
    #[serde(default)]
    pub tls_key_file: String,
}

fn default_join_token_file() -> PathBuf {
    PathBuf::from("/etc/nexora/join-token")
}

fn default_listen() -> Vec<SocketAddr> {
    vec![
        SocketAddr::from(([0, 0, 0, 0], 53)),
        SocketAddr::from(([0u16; 8], 53)),
    ]
}

fn default_doh_path() -> String {
    "/dns-query".into()
}

fn default_metrics_listen() -> SocketAddr {
    SocketAddr::from(([0, 0, 0, 0], 9153))
}

pub fn load(path: &Path) -> anyhow::Result<Bootstrap> {
    let text = std::fs::read_to_string(path).with_context(|| format!("read {}", path.display()))?;
    let b: Bootstrap =
        toml::from_str(&text).with_context(|| format!("parse {}", path.display()))?;
    let name_ok = (1..=63).contains(&b.node_name.len())
        && b.node_name
            .bytes()
            .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == b'-');
    if !name_ok {
        bail!("node_name {:?} must match [a-z0-9-]{{1,63}}", b.node_name);
    }
    if b.state_dir.as_os_str().is_empty() {
        bail!("state_dir must be set");
    }
    if b.management_urls.is_empty() && !b.is_standalone() {
        bail!("management_urls must be set unless standalone_snapshot is set");
    }
    if let Some(u) = b
        .management_urls
        .iter()
        .find(|u| !u.starts_with("https://"))
    {
        bail!("management_urls entry {u:?} must start with https://");
    }
    if !b.doh_path.starts_with('/') {
        bail!("doh_path must start with '/'");
    }
    if (b.proxy_protocol_dot || b.proxy_protocol_doh)
        && let Err(e) = ProxyPolicy::new(&b.proxy_protocol_trusted_cidrs)
    {
        bail!("{e}");
    }
    if b.tls_cert_file.is_empty() != b.tls_key_file.is_empty() {
        bail!("tls_cert_file and tls_key_file must be set together");
    }
    if !b.tls_cert_file.is_empty() && !b.is_standalone() {
        bail!("tls_cert_file is only valid with standalone_snapshot");
    }
    Ok(b)
}

impl Bootstrap {
    pub fn worker_count(&self) -> usize {
        match self.workers {
            0 => std::thread::available_parallelism().map_or(1, |n| n.get()),
            n => n,
        }
    }

    pub fn is_standalone(&self) -> bool {
        !self.standalone_snapshot.is_empty()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn bootstrap_rejects_proxy_without_trusted_cidrs() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("engine.toml");
        let base = "node_name = \"e\"\nstate_dir = \"/tmp/x\"\nstandalone_snapshot = \"/tmp/s\"\nlisten_dot = [\"127.0.0.1:853\"]\nproxy_protocol_dot = true\n";
        std::fs::write(&path, base).unwrap();
        let e = load(&path).unwrap_err();
        assert!(
            format!("{e:#}").contains("proxy_protocol_trusted_cidrs must not be empty"),
            "{e:#}"
        );
        std::fs::write(
            &path,
            format!("{base}proxy_protocol_trusted_cidrs = [\"10.0.0.0/8\"]\n"),
        )
        .unwrap();
        let b = load(&path).unwrap();
        assert_eq!(b.proxy_protocol_trusted_cidrs, ["10.0.0.0/8"]);
    }
}
