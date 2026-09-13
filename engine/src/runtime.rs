//! The applied configuration workers read once per packet through `ArcSwap`.

use crate::acl::Acl;
use crate::cache::{Cache, CacheSettings};
use crate::filter::{self, BlockMode, FilterSet, ListStats};
use crate::proto::{self, ConfigSnapshot, UpstreamProtocol, UpstreamStrategy};
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
    pub filter: FilterSet,
    /// `b:<sha256>` per blocklist then `a:<sha256>` per allowlist.
    pub filter_hashes: Vec<String>,
    pub filter_stats: ListStats,
    pub cache: Arc<Cache>,
    pub upstreams: Arc<UpstreamSet>,
    pub telemetry: TelemetrySettings,
}

impl Runtime {
    /// Before any snapshot: every client is REFUSED and nothing is forwarded.
    pub fn initial() -> Runtime {
        Runtime {
            version: 0,
            acl: Acl::parse(&[]).expect("empty acl"),
            filter: FilterSet::empty(),
            filter_hashes: Vec::new(),
            filter_stats: ListStats::default(),
            cache: Arc::new(Cache::new(CacheSettings {
                max_bytes: INITIAL_CACHE_BYTES,
                min_ttl: 0,
                max_ttl: 86400,
                negative_max_ttl: 3600,
                stale_window: 0,
            })),
            upstreams: Arc::new(UpstreamSet::new(Vec::new(), Strategy::Ordered, None)),
            telemetry: TelemetrySettings::default(),
        }
    }

    /// Builds the runtime for a validated snapshot, carrying upstream health
    /// and (when its settings are unchanged) the cache over from `previous`.
    pub fn build(
        s: &ConfigSnapshot,
        blobs: &dyn BlobSource,
        previous: Option<&Runtime>,
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
        let read_all = |refs: &[proto::BlobRef]| {
            refs.iter()
                .map(|r| {
                    filter::decode_blob(&blobs.read(r)?).map_err(|e| SnapshotError::Blob {
                        sha256: r.sha256.clone(),
                        reason: format!("zstd: {e}"),
                    })
                })
                .collect::<Result<Vec<_>, SnapshotError>>()
        };
        let mode = match f.block_mode() {
            proto::BlockMode::Nxdomain => BlockMode::NxDomain,
            proto::BlockMode::Refused => BlockMode::Refused,
            proto::BlockMode::NullIp | proto::BlockMode::Unspecified => BlockMode::NullIp,
        };
        let (filter, filter_stats) = FilterSet::build(
            &read_all(&f.blocklists)?,
            &read_all(&f.allowlists)?,
            mode,
            f.block_ttl,
        );
        let filter_hashes: Vec<String> = f
            .blocklists
            .iter()
            .map(|b| format!("b:{}", b.sha256))
            .chain(f.allowlists.iter().map(|a| format!("a:{}", a.sha256)))
            .collect();
        if let Some(p) = reused
            && p.filter_hashes != filter_hashes
        {
            // Cached answers must be re-filtered: CNAME cloaking is checked on the miss path.
            cache.clear();
        }

        let t = s.telemetry.clone().unwrap_or_default();
        Ok(Runtime {
            version: s.version,
            acl,
            filter,
            filter_hashes,
            filter_stats,
            cache,
            upstreams,
            telemetry: TelemetrySettings {
                otlp_endpoint: t.otlp_endpoint,
                trace_sample_one_in: t.trace_sample_one_in,
                trace_slow_threshold_us: t.trace_slow_threshold_us,
                querylog_to_management: t.querylog_to_management,
            },
        })
    }
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
