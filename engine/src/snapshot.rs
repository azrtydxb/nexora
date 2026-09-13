//! Snapshot validation, blob verification, persistence and atomic application.

use crate::proto::{BlobRef, ConfigSnapshot, UpstreamProtocol};
use crate::runtime::Runtime;
use arc_swap::ArcSwap;
use prost::Message;
use sha2::{Digest, Sha256};
use std::io::Write;
use std::net::SocketAddr;
use std::path::{Path, PathBuf};
use std::sync::Arc;

pub const SNAPSHOT_FILE: &str = "snapshot.binpb";
const MIN_CACHE_BYTES: u64 = 1 << 20;
const TIMEOUT_MS: std::ops::RangeInclusive<u32> = 50..=5000;

#[derive(Debug, thiserror::Error)]
pub enum SnapshotError {
    #[error("invalid snapshot: {0}")]
    Invalid(String),
    #[error("blob {sha256}: {reason}")]
    Blob { sha256: String, reason: String },
    #[error("io: {0}")]
    Io(#[from] std::io::Error),
    #[error("decode: {0}")]
    Decode(#[from] prost::DecodeError),
}

pub trait BlobSource {
    fn read(&self, r: &BlobRef) -> Result<Vec<u8>, SnapshotError>;
}

/// Blobs stored as `<dir>/<sha256>`, verified on read.
pub struct DirBlobs {
    pub dir: PathBuf,
}

impl BlobSource for DirBlobs {
    fn read(&self, r: &BlobRef) -> Result<Vec<u8>, SnapshotError> {
        if !is_sha256_hex(&r.sha256) {
            return Err(SnapshotError::Blob {
                sha256: r.sha256.clone(),
                reason: "sha256 must be 64 lowercase hex".into(),
            });
        }
        let bytes = match std::fs::read(self.dir.join(&r.sha256)) {
            Ok(b) => b,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                return Err(SnapshotError::Blob {
                    sha256: r.sha256.clone(),
                    reason: "blob not present".into(),
                });
            }
            Err(e) => return Err(e.into()),
        };
        verify_blob(r, &bytes)?;
        Ok(bytes)
    }
}

pub fn verify_blob(r: &BlobRef, bytes: &[u8]) -> Result<(), SnapshotError> {
    if bytes.len() as u64 != r.size || hex::encode(Sha256::digest(bytes)) != r.sha256 {
        return Err(SnapshotError::Blob {
            sha256: r.sha256.clone(),
            reason: "sha256 mismatch".into(),
        });
    }
    Ok(())
}

pub fn validate(s: &ConfigSnapshot, applied_version: u64) -> Result<(), SnapshotError> {
    let invalid = |m: String| Err(SnapshotError::Invalid(m));
    if s.version <= applied_version {
        return invalid(format!(
            "version {} is not newer than applied {applied_version}",
            s.version
        ));
    }
    if s.cache.as_ref().map_or(0, |c| c.max_bytes) < MIN_CACHE_BYTES {
        return invalid("cache.max_bytes must be >= 1048576".into());
    }
    crate::acl::Acl::parse(&s.acl_allow_cidrs).map_err(SnapshotError::Invalid)?;
    for u in &s.upstreams {
        if !TIMEOUT_MS.contains(&u.timeout_ms) {
            return invalid(format!(
                "upstream {} timeout_ms {} outside 50..=5000",
                u.id, u.timeout_ms
            ));
        }
        match UpstreamProtocol::try_from(u.protocol) {
            Ok(UpstreamProtocol::Udp | UpstreamProtocol::Tcp | UpstreamProtocol::Dot) => {
                if u.address.parse::<SocketAddr>().is_err() {
                    return invalid(format!(
                        "upstream {} address {} must be ip:port",
                        u.id, u.address
                    ));
                }
                if u.protocol == UpstreamProtocol::Dot as i32 && u.tls_server_name.is_empty() {
                    return invalid(format!("upstream {} tls_server_name required", u.id));
                }
            }
            Ok(UpstreamProtocol::Doh) => {
                if !u.doh_url.starts_with("https://") {
                    return invalid(format!("upstream {} doh_url must be https", u.id));
                }
            }
            Ok(UpstreamProtocol::Unspecified) | Err(_) => {
                return invalid(format!("upstream {} protocol unspecified", u.id));
            }
        }
    }
    let blobs = s
        .filter
        .iter()
        .flat_map(|f| f.blocklists.iter().chain(&f.allowlists));
    for b in blobs {
        if !is_sha256_hex(&b.sha256) {
            return invalid(format!("sha256 {} must be 64 lowercase hex", b.sha256));
        }
    }
    crate::snapshot_m3::validate_m3(s).map_err(SnapshotError::Invalid)?;
    crate::authoritative::loader::validate(&s.auth_zones).map_err(SnapshotError::Invalid)?;
    Ok(())
}

/// Also keeps blob names safe to join onto a directory.
pub(crate) fn is_sha256_hex(h: &str) -> bool {
    h.len() == 64
        && h.bytes()
            .all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c))
}

/// Writes `state_dir/snapshot.binpb` atomically: temp file, fsync, rename, fsync dir.
pub fn persist(state_dir: &Path, s: &ConfigSnapshot) -> std::io::Result<()> {
    let tmp = state_dir.join(format!("{SNAPSHOT_FILE}.tmp"));
    let mut f = std::fs::File::create(&tmp)?;
    f.write_all(&s.encode_to_vec())?;
    f.sync_all()?;
    drop(f);
    std::fs::rename(&tmp, state_dir.join(SNAPSHOT_FILE))?;
    std::fs::File::open(state_dir)?.sync_all()
}

/// The persisted snapshot, or `None` when none was written yet.
pub fn load(state_dir: &Path) -> Result<Option<ConfigSnapshot>, SnapshotError> {
    match load_file(&state_dir.join(SNAPSHOT_FILE)) {
        Err(SnapshotError::Io(e)) if e.kind() == std::io::ErrorKind::NotFound => Ok(None),
        other => other.map(Some),
    }
}

pub fn load_file(path: &Path) -> Result<ConfigSnapshot, SnapshotError> {
    Ok(ConfigSnapshot::decode(std::fs::read(path)?.as_slice())?)
}

#[derive(Debug, PartialEq)]
pub enum ApplyOutcome {
    Applied {
        version: u64,
        persist_error: Option<String>,
    },
    Rejected {
        version: u64,
        reason: String,
    },
}

/// Validates and builds `s`, swaps it in, then persists it. A rejected
/// snapshot leaves `current` untouched; a persist failure is reported but the
/// new runtime stays applied.
pub fn apply(
    current: &ArcSwap<Runtime>,
    s: ConfigSnapshot,
    blobs: &dyn BlobSource,
    state_dir: Option<&Path>,
) -> ApplyOutcome {
    let version = s.version;
    let previous = current.load_full();
    let built =
        validate(&s, previous.version).and_then(|()| Runtime::build(&s, blobs, Some(&previous)));
    let rt = match built {
        Ok(rt) => rt,
        Err(e) => {
            return ApplyOutcome::Rejected {
                version,
                reason: e.to_string(),
            };
        }
    };
    current.store(Arc::new(rt));
    let persist_error = state_dir.and_then(|d| persist(d, &s).err().map(|e| e.to_string()));
    ApplyOutcome::Applied {
        version,
        persist_error,
    }
}
