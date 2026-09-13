//! Infrastructure cache: per-server RTT/RTO (RFC 6298), timeout backoff, EDNS support and lame
//! (server, zone) marks, used to choose which authoritative server to ask next.

use hickory_proto::rr::Name;
use quick_cache::sync::Cache;
use std::net::IpAddr;
use std::time::Duration;

/// RTO for a server never measured (the RFC 6298 initial 1 s is too slow for DNS).
const UNKNOWN_RTO_MS: u32 = 376;
const MIN_RTO_MS: u32 = 50;
const MAX_RTO_MS: u32 = 3000;
/// Consecutive timeouts before a server is backed off.
const BACKOFF_AFTER: u8 = 3;
const BACKOFF_BASE_SECS: u64 = 5;
const BACKOFF_MAX_SECS: u64 = 300;
const LAME_SECS: u64 = 900;
/// Servers whose RTO is within this band of the best one are chosen at random.
const SELECT_BAND_MS: u32 = 400;

#[derive(Clone, Copy, Debug)]
pub struct InfraEntry {
    pub srtt_ms: f32,
    pub rttvar_ms: f32,
    pub rto_ms: u32,
    pub consecutive_timeouts: u8,
    pub backoff_until: u64,
    pub no_edns: bool,
}

impl InfraEntry {
    fn unknown() -> Self {
        Self {
            srtt_ms: 0.0,
            rttvar_ms: 0.0,
            rto_ms: UNKNOWN_RTO_MS,
            consecutive_timeouts: 0,
            backoff_until: 0,
            no_edns: false,
        }
    }

    fn has_sample(&self) -> bool {
        self.srtt_ms > 0.0 || self.rttvar_ms > 0.0
    }
}

pub struct InfraCache {
    servers: Cache<IpAddr, InfraEntry>,
    /// (server, lowercase zone) -> lame until (Unix seconds).
    lame: Cache<(IpAddr, Name), u64>,
}

impl InfraCache {
    pub fn new(capacity: usize) -> Self {
        Self {
            servers: Cache::new(capacity),
            lame: Cache::new(capacity),
        }
    }

    fn entry(&self, ip: IpAddr) -> Option<InfraEntry> {
        self.servers.get(&ip)
    }

    // debt: read-modify-write is not atomic across workers; a lost update only skews one RTT
    // sample or timeout count. Revisit if server selection shows measurable flapping.
    fn update(&self, ip: IpAddr, f: impl FnOnce(&mut InfraEntry)) {
        let mut e = self.entry(ip).unwrap_or_else(InfraEntry::unknown);
        f(&mut e);
        self.servers.insert(ip, e);
    }

    pub fn rto(&self, ip: IpAddr) -> Duration {
        let ms = self.entry(ip).map_or(UNKNOWN_RTO_MS, |e| e.rto_ms);
        Duration::from_millis(u64::from(ms))
    }

    pub fn record_rtt(&self, ip: IpAddr, rtt: Duration) {
        let r = rtt.as_secs_f32() * 1000.0;
        self.update(ip, |e| {
            if e.has_sample() {
                e.rttvar_ms = 0.75 * e.rttvar_ms + 0.25 * (e.srtt_ms - r).abs();
                e.srtt_ms = 0.875 * e.srtt_ms + 0.125 * r;
            } else {
                e.srtt_ms = r;
                e.rttvar_ms = r / 2.0;
            }
            let rto = (e.srtt_ms + 4.0 * e.rttvar_ms).round() as u32;
            e.rto_ms = rto.clamp(MIN_RTO_MS, MAX_RTO_MS);
            e.consecutive_timeouts = 0;
            e.backoff_until = 0;
        });
    }

    pub fn record_timeout(&self, ip: IpAddr, now: u64) {
        self.update(ip, |e| {
            e.rto_ms = (e.rto_ms.saturating_mul(2)).min(MAX_RTO_MS);
            e.consecutive_timeouts = e.consecutive_timeouts.saturating_add(1);
            if e.consecutive_timeouts >= BACKOFF_AFTER {
                let shift = u32::from(e.consecutive_timeouts - BACKOFF_AFTER).min(16);
                e.backoff_until = now + (BACKOFF_BASE_SECS << shift).min(BACKOFF_MAX_SECS);
            }
        });
    }

    pub fn is_backed_off(&self, ip: IpAddr, now: u64) -> bool {
        self.entry(ip).is_some_and(|e| now < e.backoff_until)
    }

    pub fn mark_lame(&self, ip: IpAddr, zone: &Name, now: u64) {
        self.lame.insert((ip, zone.to_lowercase()), now + LAME_SECS);
    }

    pub fn is_lame(&self, ip: IpAddr, zone: &Name, now: u64) -> bool {
        self.lame
            .get(&(ip, zone.to_lowercase()))
            .is_some_and(|until| now < until)
    }

    pub fn set_no_edns(&self, ip: IpAddr) {
        self.update(ip, |e| e.no_edns = true);
    }

    pub fn no_edns(&self, ip: IpAddr) -> bool {
        self.entry(ip).is_some_and(|e| e.no_edns)
    }

    /// A server to ask for `zone`: lame servers never; backed-off servers only when every other
    /// server is backed off too (the one whose backoff ends first is probed); otherwise a random
    /// choice among the servers within `SELECT_BAND_MS` of the lowest RTO.
    pub fn select(
        &self,
        candidates: &[IpAddr],
        zone: &Name,
        now: u64,
        rng: &mut impl rand::Rng,
    ) -> Option<IpAddr> {
        let usable: Vec<IpAddr> = candidates
            .iter()
            .copied()
            .filter(|ip| !self.is_lame(*ip, zone, now))
            .collect();
        let up: Vec<(IpAddr, u32)> = usable
            .iter()
            .copied()
            .filter(|ip| !self.is_backed_off(*ip, now))
            .map(|ip| (ip, self.rto(ip).as_millis() as u32))
            .collect();
        let Some(best) = up.iter().map(|(_, r)| *r).min() else {
            return usable
                .into_iter()
                .min_by_key(|ip| self.entry(*ip).map_or(0, |e| e.backoff_until));
        };
        let band: Vec<IpAddr> = up
            .into_iter()
            .filter(|(_, r)| *r <= best + SELECT_BAND_MS)
            .map(|(ip, _)| ip)
            .collect();
        Some(band[(rng.next_u32() as usize) % band.len()])
    }

    pub fn len(&self) -> usize {
        self.servers.len()
    }

    pub fn is_empty(&self) -> bool {
        self.servers.is_empty()
    }
}
