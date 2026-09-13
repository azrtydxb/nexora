//! RPZ zone lifecycle: file zones from snapshot blobs, transfer zones refreshed from their
//! primary, last-good copies under `state_dir/rpz/`, in-memory TSIG keys, status and metrics.

use super::RpzState;
use super::index::{RpzSet, RpzZoneIndex};
use super::parse::{parse_rpz_records, parse_rpz_text};
use super::transfer::{Timers, ZoneData, serial_gt, soa_serial, timers_from_soa, transfer};
use super::tsig::{TsigAlg, TsigKey};
use crate::proto::{self, TsigAlgorithm, rpz_zone::Source};
use crate::runtime::Runtime;
use crate::snapshot::BlobSource;
use arc_swap::ArcSwap;
use hickory_proto::rr::{Name, RData, Record};
use hickory_proto::serialize::binary::{BinDecodable, BinDecoder, BinEncodable};
use parking_lot::Mutex;
use prometheus_client::metrics::counter::Counter;
use prometheus_client::metrics::family::Family;
use prometheus_client::metrics::gauge::Gauge;
use prometheus_client::registry::Registry;
use rustc_hash::FxHashMap;
use std::io::Write;
use std::net::SocketAddr;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::sync::atomic::Ordering;
use std::time::{Duration, Instant};
use tokio::sync::Notify;
use zeroize::Zeroizing;

#[derive(Clone)]
pub enum RpzSourceConfig {
    File(Arc<RpzZoneIndex>),
    Transfer {
        primary: SocketAddr,
        tsig_key_name: String,
        tsig_algorithm: i32,
        min_refresh_seconds: u32,
    },
}

#[derive(Clone)]
pub struct RpzZoneConfig {
    pub id: String,
    pub origin: Name,
    pub policy_override: i32,
    pub refresh_nonce: u64,
    pub source: RpzSourceConfig,
}

/// RPZ configuration of a snapshot in snapshot order; file zones are decompressed, parsed and
/// indexed here, so a bad zone rejects the snapshot.
pub fn zone_configs(
    s: &proto::ConfigSnapshot,
    blobs: &dyn BlobSource,
) -> Result<Vec<RpzZoneConfig>, String> {
    s.rpz_zones
        .iter()
        .enumerate()
        .map(|(i, z)| {
            let err = |e: String| format!("rpz_zones[{i}]: {e}");
            // The id names a file under state_dir/rpz/.
            if z.id.is_empty() || !z.id.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-') {
                return Err(err("id must be letters, digits and '-'".into()));
            }
            let origin = Name::from_ascii(&z.name)
                .map_err(|e| err(e.to_string()))?
                .to_lowercase();
            let source = match &z.source {
                Some(Source::File(f)) => {
                    let blob = f
                        .blob
                        .as_ref()
                        .ok_or_else(|| err("file source without blob".into()))?;
                    let bytes = blobs.read(blob).map_err(|e| err(e.to_string()))?;
                    let text = zstd::decode_all(bytes.as_slice())
                        .map_err(|e| err(format!("zstd: {e}")))?;
                    let text = String::from_utf8(text)
                        .map_err(|_| err("zone text is not UTF-8".into()))?;
                    let parsed = parse_rpz_text(&origin, &text).map_err(err)?;
                    RpzSourceConfig::File(Arc::new(RpzZoneIndex::build(
                        &z.id,
                        &parsed,
                        z.policy_override,
                    )))
                }
                Some(Source::Transfer(t)) => RpzSourceConfig::Transfer {
                    primary: t
                        .primary
                        .parse()
                        .map_err(|_| err(format!("primary is not ip:port: {}", t.primary)))?,
                    tsig_key_name: t.tsig_key_name.clone(),
                    tsig_algorithm: t.tsig_algorithm,
                    min_refresh_seconds: t.min_refresh_seconds,
                },
                None => return Err(err("no source".into())),
            };
            Ok(RpzZoneConfig {
                id: z.id.clone(),
                origin,
                policy_override: z.policy_override,
                refresh_nonce: z.refresh_nonce,
                source,
            })
        })
        .collect()
}

struct ZoneTask {
    cfg: RpzZoneConfig,
    /// Transfer zones: the records IXFR applies to.
    data: Option<Arc<ZoneData>>,
    index: Option<Arc<RpzZoneIndex>>,
    /// Hits of indexes this zone served before the current one.
    hits_base: u64,
    last_success: i64,
    last_error: String,
    stale: bool,
    failures: u64,
    next_refresh: Instant,
    timers: Timers,
}

impl ZoneTask {
    fn new(cfg: RpzZoneConfig) -> Self {
        let timers = timers_from_soa(0, 0, 0, min_refresh(&cfg));
        ZoneTask {
            cfg,
            data: None,
            index: None,
            hits_base: 0,
            last_success: 0,
            last_error: String::new(),
            stale: false,
            failures: 0,
            next_refresh: Instant::now(),
            timers,
        }
    }

    fn set_index(&mut self, index: Arc<RpzZoneIndex>) {
        if let Some(old) = self.index.replace(index) {
            self.hits_base += old.hits.load(Ordering::Relaxed);
        }
    }

    fn set_data(&mut self, data: Arc<ZoneData>) {
        if let Some(RData::SOA(s)) = data
            .records
            .iter()
            .map(|r| &r.data)
            .find(|d| matches!(d, RData::SOA(_)))
        {
            self.timers = timers_from_soa(
                s.refresh as u32,
                s.retry as u32,
                s.expire as u32,
                min_refresh(&self.cfg),
            );
        }
        self.data = Some(data);
    }
}

fn min_refresh(cfg: &RpzZoneConfig) -> u32 {
    match cfg.source {
        RpzSourceConfig::Transfer {
            min_refresh_seconds,
            ..
        } => min_refresh_seconds,
        RpzSourceConfig::File(_) => 0,
    }
}

fn transfer_fields(cfg: &RpzZoneConfig) -> Option<(SocketAddr, &str, i32)> {
    match &cfg.source {
        RpzSourceConfig::Transfer {
            primary,
            tsig_key_name,
            tsig_algorithm,
            ..
        } => Some((*primary, tsig_key_name.as_str(), *tsig_algorithm)),
        RpzSourceConfig::File(_) => None,
    }
}

pub struct RpzManager {
    dir: Option<PathBuf>,
    zones: Mutex<Vec<ZoneTask>>,
    keys: Mutex<FxHashMap<String, TsigKey>>,
    notify: Notify,
    refreshing: tokio::sync::Mutex<()>,
}

impl RpzManager {
    /// `state_dir`: last-good copies live in `state_dir/rpz/`; `None` keeps nothing on disk.
    pub fn new(state_dir: Option<&Path>) -> Self {
        RpzManager {
            dir: state_dir.map(|d| d.join("rpz")),
            zones: Mutex::new(Vec::new()),
            keys: Mutex::new(FxHashMap::default()),
            notify: Notify::new(),
            refreshing: tokio::sync::Mutex::new(()),
        }
    }

    /// Takes the configuration of an applied snapshot: publishes file zones and persisted
    /// transfer copies immediately and schedules transfers whose source changed.
    pub fn apply_config(&self, state: &RpzState, zones: &[RpzZoneConfig]) {
        let mut wake = false;
        {
            let mut tasks = self.zones.lock();
            let mut old: FxHashMap<String, ZoneTask> =
                tasks.drain(..).map(|t| (t.cfg.id.clone(), t)).collect();
            for cfg in zones {
                let prev = old.remove(&cfg.id);
                let task = match &cfg.source {
                    RpzSourceConfig::File(index) => {
                        let mut t = prev
                            .filter(|p| matches!(p.cfg.source, RpzSourceConfig::File(_)))
                            .unwrap_or_else(|| ZoneTask::new(cfg.clone()));
                        if !t.index.as_ref().is_some_and(|i| Arc::ptr_eq(i, index)) {
                            t.set_index(index.clone());
                            t.last_success = crate::clock::unix_now();
                        }
                        t.cfg = cfg.clone();
                        t
                    }
                    RpzSourceConfig::Transfer { .. } => {
                        match prev.filter(|p| {
                            transfer_fields(&p.cfg).is_some() && p.cfg.origin == cfg.origin
                        }) {
                            Some(mut t) => {
                                if transfer_fields(&t.cfg) != transfer_fields(cfg)
                                    || t.cfg.refresh_nonce != cfg.refresh_nonce
                                {
                                    t.next_refresh = Instant::now();
                                    wake = true;
                                }
                                if t.cfg.policy_override != cfg.policy_override
                                    && let Some(data) = &t.data
                                    && let Ok(parsed) =
                                        parse_rpz_records(&cfg.origin, &data.records)
                                {
                                    t.set_index(Arc::new(RpzZoneIndex::build(
                                        &cfg.id,
                                        &parsed,
                                        cfg.policy_override,
                                    )));
                                }
                                t.cfg = cfg.clone();
                                t
                            }
                            None => {
                                let mut t = ZoneTask::new(cfg.clone());
                                self.load_persisted(&mut t);
                                wake = true;
                                t
                            }
                        }
                    }
                };
                tasks.push(task);
            }
            self.remove_orphans(&tasks);
            publish(state, &tasks);
        }
        if wake {
            self.notify.notify_one();
        }
    }

    /// Replaces the in-memory key set (secrets are moved, never copied); zones whose key
    /// changed refresh at once. Keys are never logged, persisted or reported.
    pub fn set_tsig_keys(&self, keys: proto::RpzTsigKeys) {
        let mut next = FxHashMap::default();
        for k in keys.keys {
            let alg = match TsigAlgorithm::try_from(k.algorithm) {
                Ok(TsigAlgorithm::HmacSha256) => TsigAlg::HmacSha256,
                Ok(TsigAlgorithm::HmacSha512) => TsigAlg::HmacSha512,
                _ => continue,
            };
            let Ok(name) = Name::from_ascii(&k.key_name) else {
                continue;
            };
            next.insert(
                k.zone_id,
                TsigKey {
                    name,
                    alg,
                    secret: Zeroizing::new(k.secret),
                },
            );
        }
        let changed: Vec<String> = {
            let old = self.keys.lock();
            let same = |a: &TsigKey, b: &TsigKey| {
                a.name == b.name && a.alg == b.alg && *a.secret == *b.secret
            };
            let mut ids: Vec<String> = next
                .iter()
                .filter(|(id, k)| !old.get(*id).is_some_and(|o| same(o, k)))
                .map(|(id, _)| id.clone())
                .collect();
            ids.extend(old.keys().filter(|id| !next.contains_key(*id)).cloned());
            ids
        };
        *self.keys.lock() = next;
        if !changed.is_empty() {
            let now = Instant::now();
            for t in self
                .zones
                .lock()
                .iter_mut()
                .filter(|t| changed.contains(&t.cfg.id))
            {
                t.next_refresh = now;
            }
            self.notify.notify_one();
        }
    }

    fn key_for(&self, cfg: &RpzZoneConfig) -> Result<Option<TsigKey>, String> {
        let Some((_, key_name, alg)) = transfer_fields(cfg) else {
            return Ok(None);
        };
        let want = match TsigAlgorithm::try_from(alg) {
            Ok(TsigAlgorithm::HmacSha256) => TsigAlg::HmacSha256,
            Ok(TsigAlgorithm::HmacSha512) => TsigAlg::HmacSha512,
            _ => return Ok(None),
        };
        let keys = self.keys.lock();
        let key = keys
            .get(&cfg.id)
            .ok_or("tsig key material not received from the management plane")?;
        match Name::from_ascii(key_name) {
            Ok(name) if name == key.name && key.alg == want => Ok(Some(key.clone())),
            _ => Err("tsig key does not match the zone configuration".into()),
        }
    }

    /// One refresh cycle of transfer zone `id`; `Ok(true)` when new data was published.
    pub async fn refresh_now(&self, state: &RpzState, id: &str) -> Result<bool, String> {
        let _serial = self.refreshing.lock().await;
        let (cfg, data, key) = {
            let tasks = self.zones.lock();
            let t = tasks
                .iter()
                .find(|t| t.cfg.id == id)
                .ok_or_else(|| format!("unknown rpz zone {id}"))?;
            if transfer_fields(&t.cfg).is_none() {
                return Err(format!("rpz zone {id} is not a transfer zone"));
            }
            (t.cfg.clone(), t.data.clone(), self.key_for(&t.cfg))
        };
        let (primary, _, _) = transfer_fields(&cfg).expect("checked above");
        let result = async {
            let key = key?;
            let (remote, soa) = soa_serial(primary, &cfg.origin, key.as_ref()).await?;
            if let Some(d) = &data
                && !serial_gt(remote, d.serial)
            {
                return Ok((None, soa));
            }
            let zone = transfer(primary, &cfg.origin, data.as_deref(), key.as_ref()).await?;
            let parsed = parse_rpz_records(&cfg.origin, &zone.records)?;
            let index = Arc::new(RpzZoneIndex::build(&cfg.id, &parsed, cfg.policy_override));
            if let Some(dir) = &self.dir {
                persist(dir, &cfg.id, &zone.records).map_err(|e| format!("persist: {e}"))?;
            }
            Ok((Some((zone, index)), soa))
        }
        .await;

        let now = crate::clock::unix_now();
        let mut tasks = self.zones.lock();
        let Some(t) = tasks.iter_mut().find(|t| t.cfg.id == id) else {
            return result.map(|(new, _)| new.is_some());
        };
        match result {
            Ok((new, soa)) => {
                let published = new.is_some();
                match new {
                    Some((zone, index)) => {
                        t.set_data(Arc::new(zone));
                        t.set_index(index);
                    }
                    None => {
                        t.timers = timers_from_soa(
                            soa.refresh as u32,
                            soa.retry as u32,
                            soa.expire as u32,
                            min_refresh(&t.cfg),
                        );
                    }
                }
                t.last_success = now;
                t.last_error.clear();
                t.stale = false;
                t.next_refresh = Instant::now() + Duration::from_secs(t.timers.refresh);
                if published {
                    publish(state, &tasks);
                }
                Ok(published)
            }
            Err(e) => {
                t.last_error.clone_from(&e);
                t.failures += 1;
                t.next_refresh = Instant::now() + Duration::from_secs(t.timers.retry);
                t.stale = now.saturating_sub(t.last_success) > t.timers.expire as i64;
                Err(e)
            }
        }
    }

    /// Timer loop (runs on the `nexora-recursor` thread): refreshes due transfer zones and
    /// clears the response cache when one published new data.
    pub async fn run(&self, state: &RpzState, runtime: &ArcSwap<Runtime>) {
        loop {
            let next = self
                .zones
                .lock()
                .iter()
                .filter(|t| transfer_fields(&t.cfg).is_some())
                .map(|t| t.next_refresh)
                .min();
            match next {
                Some(at) => {
                    let wait = at.saturating_duration_since(Instant::now());
                    if !wait.is_zero() {
                        let _ = tokio::time::timeout(wait, self.notify.notified()).await;
                    }
                }
                None => self.notify.notified().await,
            }
            let now = Instant::now();
            let due: Vec<String> = self
                .zones
                .lock()
                .iter()
                .filter(|t| transfer_fields(&t.cfg).is_some() && t.next_refresh <= now)
                .map(|t| t.cfg.id.clone())
                .collect();
            let mut published = false;
            for id in due {
                published |= self.refresh_now(state, &id).await.unwrap_or(false);
            }
            if published {
                runtime.load().cache.clear();
            }
        }
    }

    /// Per-zone status in configuration order.
    pub fn status(&self) -> Vec<proto::RpzZoneStatus> {
        self.zones
            .lock()
            .iter()
            .map(|t| proto::RpzZoneStatus {
                id: t.cfg.id.clone(),
                serial: t.index.as_ref().map_or(0, |i| i.serial),
                records: t.index.as_ref().map_or(0, |i| i.records),
                skipped: t.index.as_ref().map_or(0, |i| i.skipped),
                hits: t.hits_base
                    + t.index
                        .as_ref()
                        .map_or(0, |i| i.hits.load(Ordering::Relaxed)),
                last_success_unix: t.last_success,
                last_error: t.last_error.clone(),
                stale: t.stale,
            })
            .collect()
    }

    /// Registers `nexora_rpz_*` per zone (label `zone` = zone origin).
    pub fn register_metrics(&self, reg: &mut Registry) {
        let hits = Family::<Vec<(&'static str, String)>, Counter>::default();
        let failures = Family::<Vec<(&'static str, String)>, Counter>::default();
        let serial = Family::<Vec<(&'static str, String)>, Gauge>::default();
        let records = Family::<Vec<(&'static str, String)>, Gauge>::default();
        let stale = Family::<Vec<(&'static str, String)>, Gauge>::default();
        let last_success = Family::<Vec<(&'static str, String)>, Gauge>::default();
        for t in self.zones.lock().iter() {
            let l = vec![("zone", t.cfg.origin.to_string())];
            let idx = t.index.as_ref();
            hits.get_or_create(&l)
                .inc_by(t.hits_base + idx.map_or(0, |i| i.hits.load(Ordering::Relaxed)));
            failures.get_or_create(&l).inc_by(t.failures);
            serial
                .get_or_create(&l)
                .set(idx.map_or(0, |i| i64::from(i.serial)));
            records
                .get_or_create(&l)
                .set(idx.map_or(0, |i| i.records as i64));
            stale.get_or_create(&l).set(i64::from(t.stale));
            last_success.get_or_create(&l).set(t.last_success);
        }
        reg.register("nexora_rpz_hits", "RPZ trigger matches", hits);
        reg.register(
            "nexora_rpz_refresh_failures",
            "Failed RPZ zone refreshes",
            failures,
        );
        reg.register(
            "nexora_rpz_zone_serial",
            "SOA serial of the served RPZ zone",
            serial,
        );
        reg.register(
            "nexora_rpz_zone_records",
            "Records in the served RPZ zone",
            records,
        );
        reg.register(
            "nexora_rpz_zone_stale",
            "1 when the RPZ zone is past its SOA expire without a refresh",
            stale,
        );
        reg.register(
            "nexora_rpz_zone_last_success_timestamp_seconds",
            "Unix time of the last successful RPZ zone load or refresh",
            last_success,
        );
    }

    fn load_persisted(&self, t: &mut ZoneTask) {
        let Some(dir) = &self.dir else { return };
        let path = dir.join(format!("{}.zone", t.cfg.id));
        let Ok(bytes) = std::fs::read(&path) else {
            return;
        };
        let loaded = decode_records(&bytes).and_then(|records| {
            let parsed = parse_rpz_records(&t.cfg.origin, &records)?;
            Ok((
                ZoneData {
                    serial: parsed.serial,
                    records,
                },
                parsed,
            ))
        });
        match loaded {
            Ok((data, parsed)) => {
                t.set_index(Arc::new(RpzZoneIndex::build(
                    &t.cfg.id,
                    &parsed,
                    t.cfg.policy_override,
                )));
                t.set_data(Arc::new(data));
                t.last_success = std::fs::metadata(&path)
                    .and_then(|m| m.modified())
                    .ok()
                    .and_then(|m| m.duration_since(std::time::UNIX_EPOCH).ok())
                    .map_or(0, |d| d.as_secs() as i64);
            }
            Err(e) => t.last_error = format!("persisted copy unreadable: {e}"),
        }
    }

    /// Deletes last-good copies of zones that are no longer transfer zones.
    fn remove_orphans(&self, tasks: &[ZoneTask]) {
        let Some(dir) = &self.dir else { return };
        let Ok(entries) = std::fs::read_dir(dir) else {
            return;
        };
        for e in entries.flatten() {
            let name = e.file_name();
            let Some(id) = name.to_str().and_then(|n| n.strip_suffix(".zone")) else {
                continue;
            };
            if !tasks
                .iter()
                .any(|t| t.cfg.id == id && transfer_fields(&t.cfg).is_some())
            {
                let _ = std::fs::remove_file(e.path());
            }
        }
    }
}

fn publish(state: &RpzState, tasks: &[ZoneTask]) {
    state.publish(RpzSet::new(
        tasks.iter().filter_map(|t| t.index.clone()).collect(),
    ));
}

/// Last-good copy: each record as `u32 length ‖ uncompressed wire`, written atomically.
fn persist(dir: &Path, id: &str, records: &[Record]) -> std::io::Result<()> {
    std::fs::create_dir_all(dir)?;
    let tmp = dir.join(format!("{id}.zone.tmp"));
    let mut f = std::io::BufWriter::new(std::fs::File::create(&tmp)?);
    for r in records {
        let wire = r.to_bytes().map_err(std::io::Error::other)?;
        f.write_all(&(wire.len() as u32).to_be_bytes())?;
        f.write_all(&wire)?;
    }
    let f = f.into_inner().map_err(|e| e.into_error())?;
    f.sync_all()?;
    drop(f);
    std::fs::rename(&tmp, dir.join(format!("{id}.zone")))?;
    std::fs::File::open(dir)?.sync_all()
}

fn decode_records(mut bytes: &[u8]) -> Result<Vec<Record>, String> {
    let mut out = Vec::new();
    while !bytes.is_empty() {
        let (len, rest) = bytes.split_first_chunk::<4>().ok_or("truncated")?;
        let len = u32::from_be_bytes(*len) as usize;
        let wire = rest.get(..len).ok_or("truncated")?;
        out.push(Record::read(&mut BinDecoder::new(wire)).map_err(|e| e.to_string())?);
        bytes = &rest[len..];
    }
    Ok(out)
}
