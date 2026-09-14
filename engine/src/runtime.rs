//! The applied configuration workers read once per packet through `ArcSwap`.

use crate::acl::Acl;
use crate::authoritative::loader::{self, LoadCounts};
use crate::authoritative::set::AuthSet;
use crate::cache::{Cache, CacheSettings};
use crate::filter::calibrate::{self, Calibration};
use crate::filter::index::IndexError;
use crate::filter::lists::{self, SnapshotLists};
use crate::filter::memory::{self, BuildMemory};
use crate::filter::{BlockMode, BlockReply, FilterIndex, PolicyTable};
use crate::proto::{self, ConfigSnapshot, UpstreamProtocol, UpstreamStrategy};
use crate::recursor::dispatch::ResolutionRuntime;
use crate::snapshot::{BlobSource, SnapshotError};
use crate::upstream::{Protocol, Strategy, UpstreamSet, UpstreamSpec};
use std::sync::Arc;
use std::time::Duration;

const INITIAL_CACHE_BYTES: u64 = 16 << 20;

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct TelemetrySettings {
    pub otlp_endpoint: String,
    pub trace_sample_one_in: u32,
    pub trace_slow_threshold_us: u32,
    pub querylog_to_management: bool,
}

pub struct Runtime {
    pub version: u64,
    pub acl: Acl,
    /// Every block and allow list of the snapshot; policies decide through views over it.
    pub filter_index: Arc<FilterIndex>,
    /// [`SnapshotLists::key`] of `filter_index`: a snapshot with the same key reuses the index.
    pub filter_key: String,
    /// The index memory cap in force.
    pub filter_max_bytes: u64,
    /// Index plus views.
    pub filter_memory_bytes: u64,
    /// Decision time of `filter_index`, measured after its build.
    pub filter_calibration: Calibration,
    /// Per-client policy groups and rewrites; the global view for clients in no group.
    pub policy: PolicyTable,
    /// `l:<SnapshotLists::key>`, `g:<key>` per policy-group cache partition,
    /// `r:<ResolutionRuntime::config_key>`, then `u:<upstreams_key>`.
    pub filter_hashes: Vec<String>,
    pub cache: Arc<Cache>,
    pub upstreams: Arc<UpstreamSet>,
    pub telemetry: TelemetrySettings,
    /// Resolution mode, forward zones, DNSSEC and RPZ settings (M3).
    pub resolution: Arc<ResolutionRuntime>,
    /// Hosted zones (M4); unchanged zones share their `Arc<Zone>` with the previous runtime.
    pub auth: Arc<AuthSet>,
    /// How the zones of this runtime were loaded, counted once by `authoritative::after_apply`.
    pub auth_loads: LoadCounts,
    /// Lowercase wire origin and serial of zones that are new or changed serial in this runtime.
    pub auth_changed: Vec<(Box<[u8]>, u32)>,
}

impl Runtime {
    /// Before any snapshot: every client is REFUSED and nothing is forwarded.
    pub fn initial() -> Runtime {
        let index = Arc::new(FilterIndex::empty());
        Runtime {
            version: 0,
            acl: Acl::parse(&[]).expect("empty acl"),
            policy: PolicyTable::global_only(
                Arc::new(index.view(&[], &[])),
                BlockReply {
                    mode: BlockMode::NullIp,
                    ttl: 0,
                },
            ),
            filter_index: index,
            filter_key: String::new(),
            filter_max_bytes: lists::DEFAULT_MAX_BYTES,
            filter_memory_bytes: 0,
            filter_calibration: Calibration::default(),
            filter_hashes: Vec::new(),
            cache: Arc::new(Cache::new(CacheSettings {
                max_bytes: INITIAL_CACHE_BYTES,
                min_ttl: 0,
                max_ttl: 86400,
                negative_max_ttl: 3600,
                stale_window: 0,
            })),
            upstreams: Arc::new(UpstreamSet::new(Vec::new(), Strategy::Ordered, None)),
            telemetry: TelemetrySettings::default(),
            resolution: Arc::new(ResolutionRuntime::default()),
            auth: Arc::new(AuthSet::empty()),
            auth_loads: LoadCounts::default(),
            auth_changed: Vec::new(),
        }
    }

    /// Builds the runtime for a validated snapshot, carrying upstream health
    /// and (when its settings are unchanged) the cache over from `previous`; the filter index
    /// build is guarded by the engine's own cgroup memory limit.
    pub fn build(
        s: &ConfigSnapshot,
        blobs: &dyn BlobSource,
        previous: Option<&Runtime>,
    ) -> Result<Runtime, SnapshotError> {
        Self::build_with(s, blobs, previous, &BuildMemory::cgroup(memory::CGROUP_DIR))
    }

    /// [`Runtime::build`] with the memory guard `memory`, whose limit also sets the default index
    /// cap.
    pub fn build_with(
        s: &ConfigSnapshot,
        blobs: &dyn BlobSource,
        previous: Option<&Runtime>,
        memory: &BuildMemory,
    ) -> Result<Runtime, SnapshotError> {
        let acl = Acl::parse(&s.acl_allow_cidrs).map_err(SnapshotError::Invalid)?;

        let specs = s
            .upstreams
            .iter()
            .map(upstream_spec)
            .collect::<Result<Vec<_>, _>>()?;
        let strategy = match s.resolver.as_ref().map(|r| r.strategy()) {
            Some(UpstreamStrategy::Fastest) => Strategy::Fastest,
            _ => Strategy::Ordered,
        };
        let upstreams = Arc::new(UpstreamSet::new(
            specs,
            strategy,
            previous.map(|p| &*p.upstreams),
        ));

        let c = s.cache.unwrap_or_default();
        let settings = CacheSettings {
            max_bytes: c.max_bytes,
            min_ttl: c.min_ttl,
            max_ttl: c.max_ttl,
            negative_max_ttl: c.negative_max_ttl,
            stale_window: c.stale_window,
        };
        let reused = previous.filter(|p| p.cache.settings() == settings);
        let cache = match reused {
            Some(p) => p.cache.clone(),
            None => Arc::new(Cache::new(settings)),
        };

        let f = s.filter.clone().unwrap_or_default();
        let mode = match f.block_mode() {
            proto::BlockMode::Nxdomain => BlockMode::NxDomain,
            proto::BlockMode::Refused => BlockMode::Refused,
            proto::BlockMode::NullIp | proto::BlockMode::Unspecified => BlockMode::NullIp,
        };
        let lists = SnapshotLists::collect(s).map_err(SnapshotError::Invalid)?;
        let filter_max_bytes = match s.filter_index_max_bytes {
            0 => lists::default_max_bytes(memory.limit()),
            n => n,
        };
        let (filter_index, filter_calibration) =
            match previous.filter(|p| p.filter_key == lists.key) {
                Some(p) => (p.filter_index.clone(), p.filter_calibration.clone()),
                None => {
                    let index = Arc::new(lists.build_index(
                        blobs,
                        filter_max_bytes,
                        lists::build_threads(),
                        memory,
                    )?);
                    let calibration = calibrate::measure(&index);
                    lists::release_freed_memory();
                    (index, calibration)
                }
            };
        let block = BlockReply {
            mode,
            ttl: f.block_ttl,
        };
        let global = Arc::new(filter_index.view(
            &lists.indexes(&filter_index, &lists.global_block),
            &lists.indexes(&filter_index, &lists.global_allow),
        ));
        let policy = PolicyTable::build(s, &lists, &filter_index, global, block)
            .map_err(SnapshotError::Invalid)?;
        let filter_memory_bytes = filter_index.memory_bytes() + policy.views_memory_bytes();
        if filter_memory_bytes > filter_max_bytes {
            return Err(SnapshotError::Invalid(
                IndexError::OverCap {
                    needed: filter_memory_bytes,
                    cap: filter_max_bytes,
                }
                .to_string(),
            ));
        }
        let resolution =
            Arc::new(ResolutionRuntime::build(s, blobs).map_err(SnapshotError::Invalid)?);
        let filter_hashes: Vec<String> = std::iter::once(format!("l:{}", lists.key))
            .chain(policy.partition_keys().iter().map(|k| format!("g:{k}")))
            .chain(std::iter::once(format!("r:{}", resolution.config_key)))
            .chain(std::iter::once(format!("u:{}", upstreams_key(s))))
            .collect();
        if let Some(p) = reused
            && p.filter_hashes != filter_hashes
        {
            // Cached answers must be re-filtered: CNAME cloaking is checked on the
            // miss path, per cache partition; resolution, DNSSEC, RPZ and upstream
            // settings change answers too.
            cache.clear();
        }

        // Zone data never enters `filter_hashes`: a zone edit does not clear the response cache.
        let empty = AuthSet::empty();
        let loaded = loader::load(previous.map_or(&empty, |p| &*p.auth), &s.auth_zones, blobs)
            .map_err(|e| SnapshotError::Invalid(e.to_string()))?;

        let t = s.telemetry.clone().unwrap_or_default();
        Ok(Runtime {
            version: s.version,
            acl,
            filter_key: lists.key,
            filter_index,
            filter_max_bytes,
            filter_memory_bytes,
            filter_calibration,
            policy,
            filter_hashes,
            cache,
            upstreams,
            telemetry: TelemetrySettings {
                otlp_endpoint: t.otlp_endpoint,
                trace_sample_one_in: t.trace_sample_one_in,
                trace_slow_threshold_us: t.trace_slow_threshold_us,
                querylog_to_management: t.querylog_to_management,
            },
            resolution,
            auth: Arc::new(loaded.set),
            auth_loads: loaded.counts,
            auth_changed: loaded.changed,
        })
    }
}

/// SHA-256 hex over the prost encoding of the upstream strategy and every upstream: answers
/// resolved through other upstreams (an engine moved into another engine group) must not be served.
fn upstreams_key(s: &ConfigSnapshot) -> String {
    use prost::Message as _;
    use sha2::{Digest, Sha256};
    let mut h = Sha256::new();
    h.update(s.resolver.as_ref().map_or(0, |r| r.strategy).to_be_bytes());
    for u in &s.upstreams {
        let bytes = u.encode_to_vec();
        h.update((bytes.len() as u32).to_be_bytes());
        h.update(bytes);
    }
    hex::encode(h.finalize())
}

fn upstream_spec(u: &proto::Upstream) -> Result<UpstreamSpec, SnapshotError> {
    let protocol = match UpstreamProtocol::try_from(u.protocol) {
        Ok(UpstreamProtocol::Udp) => Protocol::Udp,
        Ok(UpstreamProtocol::Tcp) => Protocol::Tcp,
        Ok(UpstreamProtocol::Dot) => Protocol::Dot,
        Ok(UpstreamProtocol::Doh) => Protocol::Doh,
        _ => {
            return Err(SnapshotError::Invalid(format!(
                "upstream {} protocol unspecified",
                u.id
            )));
        }
    };
    let addr = match protocol {
        Protocol::Doh => None,
        _ => Some(u.address.parse().map_err(|_| {
            SnapshotError::Invalid(format!(
                "upstream {} address {} must be ip:port",
                u.id, u.address
            ))
        })?),
    };
    Ok(UpstreamSpec {
        id: u.id.clone(),
        name: u.name.clone(),
        protocol,
        addr,
        tls_server_name: u.tls_server_name.clone(),
        doh_url: u.doh_url.clone(),
        timeout: Duration::from_millis(u64::from(u.timeout_ms)),
        ca_pem: u.ca_certificate_pem.clone(),
    })
}
