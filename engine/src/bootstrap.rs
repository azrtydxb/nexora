//! `engine.toml`: host-level settings that are not part of the snapshot.

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
