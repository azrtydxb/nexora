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
/// A best measured server slower than this is raced against an unmeasured one.
const EXPLORE_ABOVE_MS: f32 = 40.0;
/// Servers raced against the primary when none of the candidates is measured yet (the first
/// queries to a zone, such as the root at start).
const COLD_RACERS: usize = 2;
/// How long each further server of a race waits for the ones already asked to answer.
pub const RACE_STAGGER: Duration = Duration::from_millis(20);

/// The servers to ask for one step: `primary` at once, then each of `racers` `RACE_STAGGER`
/// after the previous one unless a reply has arrived; the first valid reply wins.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Plan {
    pub primary: IpAddr,
    pub racers: Vec<IpAddr>,
}

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

    /// Records a server that lost a race after `elapsed` without answering: only an unmeasured
    /// server is given an estimate (twice its wait), so it is not raced again at every step and
    /// is measured properly once it looks fastest. The estimate is a lower bound, so the RTO stays
    /// at least the unmeasured one.
    pub fn record_lost(&self, ip: IpAddr, elapsed: Duration) {
        let srtt = elapsed.as_secs_f32() * 2000.0;
        self.update(ip, |e| {
            if e.has_sample() {
                return;
            }
            e.srtt_ms = srtt.max(0.001);
            e.rttvar_ms = ((UNKNOWN_RTO_MS as f32 - e.srtt_ms) / 4.0).max(e.srtt_ms / 2.0);
            let rto = (e.srtt_ms + 4.0 * e.rttvar_ms).round() as u32;
            e.rto_ms = rto.clamp(MIN_RTO_MS, MAX_RTO_MS);
        });
    }

    /// The server to ask for `zone` (see `plan`).
    pub fn select(
        &self,
        candidates: &[IpAddr],
        zone: &Name,
        now: u64,
        rng: &mut impl rand::Rng,
    ) -> Option<IpAddr> {
        self.plan(candidates, zone, now, rng).map(|p| p.primary)
    }

    /// The servers to ask for `zone`. Lame servers never; backed-off servers only when every other
    /// server is backed off too (the one whose backoff ends first is probed, alone). Otherwise the
    /// fastest measured server (smoothed RTT, or the doubled RTO after a timeout); it is raced
    /// against a random unmeasured server when it is slower than `EXPLORE_ABOVE_MS`, and with no
    /// measured server two random unmeasured ones race.
    pub fn plan(
        &self,
        candidates: &[IpAddr],
        zone: &Name,
        now: u64,
        rng: &mut impl rand::Rng,
    ) -> Option<Plan> {
        let usable: Vec<IpAddr> = candidates
            .iter()
            .copied()
            .filter(|ip| !self.is_lame(*ip, zone, now))
            .collect();
        let mut measured: Vec<(IpAddr, f32)> = Vec::new();
        let mut unmeasured: Vec<IpAddr> = Vec::new();
        for &ip in &usable {
            match self.entry(ip) {
                Some(e) if now < e.backoff_until => {}
                Some(e) if e.has_sample() && e.consecutive_timeouts > 0 => {
                    measured.push((ip, e.rto_ms as f32))
                }
                Some(e) if e.has_sample() => measured.push((ip, e.srtt_ms)),
                _ => unmeasured.push(ip),
            }
        }
        // unmeasured servers that timed out are the last to be tried
        unmeasured.sort_by_key(|ip| self.entry(*ip).map_or(0, |e| e.consecutive_timeouts));
        let fresh = unmeasured
            .iter()
            .take_while(|ip| self.entry(**ip).is_none_or(|e| e.consecutive_timeouts == 0))
            .count();
        fn pick(rng: &mut impl rand::Rng, from: &[IpAddr]) -> Option<IpAddr> {
            match from.len() {
                0 => None,
                n => Some(from[(rng.next_u32() as usize) % n]),
            }
        }
        let best = measured.iter().copied().min_by(|a, b| a.1.total_cmp(&b.1));
        match best {
            Some((primary, key)) => {
                let racers = if key > EXPLORE_ABOVE_MS {
                    pick(rng, &unmeasured[..fresh]).into_iter().collect()
                } else {
                    Vec::new()
                };
                Some(Plan { primary, racers })
            }
            None if !unmeasured.is_empty() => {
                let from = if fresh > 0 {
                    &unmeasured[..fresh]
                } else {
                    &unmeasured[..]
                };
                let primary = pick(rng, from)?;
                let mut rest: Vec<IpAddr> = unmeasured[..fresh]
                    .iter()
                    .copied()
                    .filter(|ip| *ip != primary)
                    .collect();
                let mut racers = Vec::new();
                while racers.len() < COLD_RACERS
                    && let Some(ip) = pick(rng, &rest)
                {
                    rest.retain(|r| *r != ip);
                    racers.push(ip);
                }
                Some(Plan { primary, racers })
            }
            None => usable
                .into_iter()
                .min_by_key(|ip| self.entry(*ip).map_or(0, |e| e.backoff_until))
                .map(|primary| Plan {
                    primary,
                    racers: Vec::new(),
                }),
        }
    }

    pub fn len(&self) -> usize {
        self.servers.len()
    }

    pub fn is_empty(&self) -> bool {
        self.servers.is_empty()
    }
}
