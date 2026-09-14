//! Trust anchors: DS anchors delivered by the management plane, merged with RFC 5011 automated
//! rollover state persisted in `state_dir/trust-anchors.json`.

use super::validator::{Fetcher, TrustPoints, ds_from_text};
use super::verify::{DsMatch, match_ds, revoked_key_signs, rrsig, verify_rrset};
use crate::proto;
use crate::recursor::metrics::RecursorMetrics;
use crate::snapshot_m3::parse_ds;
use arc_swap::ArcSwap;
use hickory_proto::dnssec::rdata::{DNSKEY, DNSSECRData};
use hickory_proto::dnssec::{Algorithm, DigestType, PublicKeyBuf, Verifier};
use hickory_proto::rr::{Name, RData, RecordType};
use hickory_proto::serialize::binary::BinEncodable;
use parking_lot::Mutex;
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::path::PathBuf;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};

#[derive(Serialize, Deserialize, Clone, Copy, Debug, PartialEq, Eq)]
pub enum KeyState {
    Configured,
    AddPend,
    Valid,
    Missing,
    Revoked,
}

#[derive(Serialize, Deserialize, Clone, Debug)]
pub struct AnchorKey {
    pub key_tag: u16,
    pub algorithm: u8,
    /// The configured DS text, for keys that came from the management plane.
    pub ds: Option<String>,
    /// Base64 of the DNSKEY RDATA (REVOKE bit clear) once the key was observed.
    pub dnskey_b64: Option<String>,
    pub state: KeyState,
    pub hold_down_until: i64,
    pub from_config: bool,
}

#[derive(Serialize, Deserialize, Clone, Debug, Default)]
pub struct ZoneAnchors {
    pub keys: Vec<AnchorKey>,
    pub last_success: i64,
    pub next_refresh: i64,
    pub last_error: String,
}

#[derive(Serialize, Deserialize, Clone, Debug, Default)]
pub struct AnchorFile {
    pub zones: BTreeMap<String, ZoneAnchors>,
}

pub const ADD_HOLD_DOWN: i64 = 30 * 86_400;
pub const REMOVE_HOLD_DOWN: i64 = 30 * 86_400;
const DAY: i64 = 86_400;
const HOUR: i64 = 3600;

/// One authenticated look at a zone's DNSKEY RRset.
pub struct Observation<'a> {
    /// The RRset's keys; self-signed revoked keys are passed with the REVOKE bit cleared.
    pub dnskeys: &'a [DNSKEY],
    pub validated_by_trusted: bool,
    /// Key tags (of the REVOKE-cleared form) of keys whose revoked form signs the RRset.
    pub revoked_self_signed: &'a [u16],
    pub orig_ttl: u32,
    pub sig_expiration: i64,
}

fn b64(k: &DNSKEY) -> String {
    data_encoding::BASE64.encode(&k.to_bytes().unwrap_or_default())
}

fn dnskey_from_b64(s: &str) -> Option<DNSKEY> {
    let rdata = data_encoding::BASE64.decode(s.as_bytes()).ok()?;
    if rdata.len() < 5 || rdata[2] != 3 {
        return None;
    }
    let flags = u16::from_be_bytes([rdata[0], rdata[1]]);
    Some(DNSKEY::with_flags(
        flags,
        PublicKeyBuf::new(rdata[4..].to_vec(), Algorithm::from_u8(rdata[3])),
    ))
}

fn ds_matches(ds_text: &str, zone: &Name, k: &DNSKEY) -> bool {
    let Ok((tag, alg, dtype, digest)) = parse_ds(ds_text) else {
        return false;
    };
    k.calculate_key_tag().ok() == Some(tag)
        && u8::from(k.algorithm()) == alg
        && k.to_digest(zone, DigestType::from(dtype))
            .is_ok_and(|d| d.as_ref() == digest.as_slice())
}

/// RFC 5011 §2.3 active refresh interval.
pub fn refresh_interval(orig_ttl: u32, sig_expiration: i64, now: i64) -> i64 {
    HOUR.max(
        (15 * DAY)
            .min(i64::from(orig_ttl) / 2)
            .min((sig_expiration - now) / 2),
    )
}

/// RFC 5011 §2.3 retry interval after a failed refresh.
pub fn retry_interval(orig_ttl: u32, sig_expiration: i64, now: i64) -> i64 {
    HOUR.max(
        DAY.min(i64::from(orig_ttl) / 10)
            .min((sig_expiration - now) / 10),
    )
}

pub fn apply_rfc5011(z: &mut ZoneAnchors, zone: &Name, obs: &Observation<'_>, now: i64) {
    if !obs.validated_by_trusted {
        z.last_error = "DNSKEY RRset not validated by a trusted key".into();
        return;
    }
    let seen: Vec<(u16, u8, &DNSKEY)> = obs
        .dnskeys
        .iter()
        .filter(|k| k.secure_entry_point() && k.zone_key())
        .filter_map(|k| {
            k.calculate_key_tag()
                .ok()
                .map(|t| (t, u8::from(k.algorithm()), k))
        })
        .collect();
    for key in z.keys.iter_mut() {
        let hit = seen.iter().find(|(t, a, k)| {
            *t == key.key_tag
                && *a == key.algorithm
                && (key.dnskey_b64.is_none() || key.dnskey_b64.as_deref() == Some(&b64(k)))
        });
        match (key.state, hit) {
            (_, Some(_))
                if obs.revoked_self_signed.contains(&key.key_tag)
                    && key.state != KeyState::Revoked =>
            {
                key.state = KeyState::Revoked;
                key.hold_down_until = now + REMOVE_HOLD_DOWN;
            }
            (KeyState::Configured, Some((_, _, k))) => {
                if key
                    .ds
                    .as_deref()
                    .map(|d| ds_matches(d, zone, k))
                    .unwrap_or(true)
                {
                    key.state = KeyState::Valid;
                    key.dnskey_b64 = Some(b64(k));
                }
            }
            (KeyState::AddPend, Some(_)) if now >= key.hold_down_until => {
                key.state = KeyState::Valid
            }
            (KeyState::Missing, Some(_)) => key.state = KeyState::Valid,
            (KeyState::Valid, None) => key.state = KeyState::Missing,
            _ => {}
        }
    }
    // AddPend keys that vanished return to Start (forgotten); Revoked keys past the remove
    // hold-down are removed
    z.keys.retain(|k| {
        !(k.state == KeyState::AddPend
            && !seen
                .iter()
                .any(|(t, a, _)| *t == k.key_tag && *a == k.algorithm))
            && !(k.state == KeyState::Revoked && now >= k.hold_down_until)
    });
    for (t, a, k) in &seen {
        let known = z.keys.iter().any(|x| x.key_tag == *t && x.algorithm == *a);
        if !known && !k.revoke() && !obs.revoked_self_signed.contains(t) {
            z.keys.push(AnchorKey {
                key_tag: *t,
                algorithm: *a,
                ds: None,
                dnskey_b64: Some(b64(k)),
                state: KeyState::AddPend,
                hold_down_until: now + ADD_HOLD_DOWN,
                from_config: false,
            });
        }
    }
    z.last_success = now;
    z.last_error.clear();
    z.next_refresh = now + refresh_interval(obs.orig_ttl, obs.sig_expiration, now);
}

pub struct TrustAnchorStore {
    /// `None`: memory only.
    path: Option<PathBuf>,
    file: Mutex<AnchorFile>,
    points: ArcSwap<TrustPoints>,
    rfc5011: AtomicBool,
    /// Why the persisted state could not be read; reported on the zones of the next merge.
    load_error: Mutex<String>,
}

fn zone_key(zone: &Name) -> String {
    zone.to_lowercase().to_ascii()
}

impl TrustAnchorStore {
    pub fn open(path: Option<PathBuf>) -> Arc<Self> {
        let (file, load_error) = match path.as_ref().map(std::fs::read) {
            None => (AnchorFile::default(), String::new()),
            Some(Err(e)) if e.kind() == std::io::ErrorKind::NotFound => {
                (AnchorFile::default(), String::new())
            }
            Some(Err(e)) => (
                AnchorFile::default(),
                format!("reading trust anchor state: {e}"),
            ),
            Some(Ok(bytes)) => match serde_json::from_slice(&bytes) {
                Ok(f) => (f, String::new()),
                Err(e) => (
                    AnchorFile::default(),
                    format!("corrupt trust anchor state: {e}"),
                ),
            },
        };
        let store = TrustAnchorStore {
            path,
            file: Mutex::new(file),
            points: ArcSwap::from_pointee(TrustPoints::default()),
            rfc5011: AtomicBool::new(true),
            load_error: Mutex::new(load_error),
        };
        store.republish(&store.file.lock());
        Arc::new(store)
    }

    /// Applies the configured DS anchors: adds new ones, drops zones no longer configured and
    /// configured keys whose DS was removed (unless RFC 5011 authenticated them as `Valid`).
    pub fn merge_config(
        &self,
        anchors: &[proto::TrustAnchor],
        rfc5011: bool,
        now: i64,
    ) -> std::io::Result<()> {
        self.rfc5011.store(rfc5011, Ordering::Relaxed);
        let mut configured: BTreeMap<String, Vec<(String, u16, u8)>> = BTreeMap::new();
        for a in anchors {
            let (Ok(zone), Ok((tag, alg, _, _))) = (Name::from_ascii(&a.zone), parse_ds(&a.ds))
            else {
                continue;
            };
            configured
                .entry(zone_key(&zone))
                .or_default()
                .push((a.ds.clone(), tag, alg));
        }
        let load_error = std::mem::take(&mut *self.load_error.lock());
        let mut f = self.file.lock();
        f.zones.retain(|z, _| configured.contains_key(z));
        for (zone, dss) in configured {
            let za = f.zones.entry(zone).or_insert_with(|| ZoneAnchors {
                next_refresh: now,
                last_error: load_error.clone(),
                ..Default::default()
            });
            za.keys.retain(|k| {
                !k.from_config
                    || k.state == KeyState::Valid
                    || dss
                        .iter()
                        .any(|(_, t, a)| *t == k.key_tag && *a == k.algorithm)
            });
            for (text, tag, alg) in dss {
                if !za
                    .keys
                    .iter()
                    .any(|k| k.key_tag == tag && k.algorithm == alg)
                {
                    za.keys.push(AnchorKey {
                        key_tag: tag,
                        algorithm: alg,
                        ds: Some(text),
                        dnskey_b64: None,
                        state: KeyState::Configured,
                        hold_down_until: 0,
                        from_config: true,
                    });
                }
            }
        }
        self.republish(&f);
        self.save(&mut f)
    }

    /// Configured DS plus RFC 5011 `Valid`/`Missing` keys; never `AddPend` or `Revoked`. With
    /// RFC 5011 disabled only the configured DS anchors are trusted.
    pub fn trust_points(&self) -> Arc<TrustPoints> {
        self.points.load_full()
    }

    pub fn rfc5011_enabled(&self) -> bool {
        self.rfc5011.load(Ordering::Relaxed)
    }

    pub fn record_observation(
        &self,
        zone: &Name,
        obs: &Observation<'_>,
        now: i64,
    ) -> std::io::Result<()> {
        let mut f = self.file.lock();
        let Some(z) = f.zones.get_mut(&zone_key(zone)) else {
            return Ok(());
        };
        apply_rfc5011(z, zone, obs, now);
        self.republish(&f);
        self.save(&mut f)
    }

    pub fn record_failure(&self, zone: &Name, error: &str, now: i64) {
        let mut f = self.file.lock();
        if let Some(z) = f.zones.get_mut(&zone_key(zone)) {
            z.last_error = error.to_string();
            z.next_refresh = now + retry_interval(0, now, now);
        }
    }

    /// Zones whose RFC 5011 refresh is due.
    pub fn due(&self, now: i64) -> Vec<Name> {
        if !self.rfc5011_enabled() {
            return Vec::new();
        }
        self.file
            .lock()
            .zones
            .iter()
            .filter(|(_, z)| z.next_refresh <= now)
            .filter_map(|(name, _)| Name::from_ascii(name).ok())
            .collect()
    }

    pub fn status(&self) -> Vec<proto::TrustAnchorStatus> {
        let f = self.file.lock();
        f.zones
            .iter()
            .flat_map(|(zone, z)| {
                z.keys.iter().map(move |k| proto::TrustAnchorStatus {
                    zone: zone.clone(),
                    key_tag: u32::from(k.key_tag),
                    algorithm: u32::from(k.algorithm),
                    state: match k.state {
                        KeyState::Configured => proto::TrustAnchorState::Configured,
                        KeyState::AddPend => proto::TrustAnchorState::AddPend,
                        KeyState::Valid => proto::TrustAnchorState::Valid,
                        KeyState::Missing => proto::TrustAnchorState::Missing,
                        KeyState::Revoked => proto::TrustAnchorState::Revoked,
                    } as i32,
                    last_refresh_success_unix: z.last_success,
                    hold_down_until_unix: k.hold_down_until,
                    last_error: z.last_error.clone(),
                })
            })
            .collect()
    }

    fn republish(&self, f: &AnchorFile) {
        let rfc5011 = self.rfc5011_enabled();
        let mut points = TrustPoints::default();
        for (zone, z) in &f.zones {
            let Ok(name) = Name::from_ascii(zone) else {
                continue;
            };
            for k in &z.keys {
                let as_ds = if rfc5011 {
                    k.state == KeyState::Configured
                } else {
                    k.from_config && k.state != KeyState::Revoked
                };
                if as_ds {
                    if let Some(ds) = k.ds.as_deref().and_then(|d| ds_from_text(d).ok()) {
                        points.add_ds(&name, ds);
                    }
                } else if rfc5011
                    && matches!(k.state, KeyState::Valid | KeyState::Missing)
                    && let Some(key) = k.dnskey_b64.as_deref().and_then(dnskey_from_b64)
                {
                    points.add_key(&name, key);
                }
            }
        }
        self.points.store(Arc::new(points));
    }

    /// [`crate::statefs::write_atomic`], like `snapshot.binpb`. A failure is reported on every zone.
    fn save(&self, f: &mut AnchorFile) -> std::io::Result<()> {
        let Some(path) = &self.path else {
            return Ok(());
        };
        let result = serde_json::to_vec_pretty(&*f)
            .map_err(std::io::Error::other)
            .and_then(|bytes| crate::statefs::write_atomic(path, &bytes));
        if let Err(e) = &result {
            for z in f.zones.values_mut() {
                z.last_error = format!("persisting trust anchor state: {e}");
            }
        }
        result
    }
}

/// One RFC 5011 refresh of `zone`: fetch its DNSKEY RRset, authenticate it with the current
/// trust points, detect self-signed revocations and record the observation.
///
/// debt: a revocation is only accepted when the RRset also verifies with another trusted key
/// (the normal rollover order). Revisit if a zone must be able to revoke its only trusted key.
pub async fn refresh_zone(
    store: &TrustAnchorStore,
    zone: &Name,
    fetcher: &dyn Fetcher,
    metrics: &RecursorMetrics,
    now: i64,
) {
    let fail = |msg: &str| {
        store.record_failure(zone, msg, now);
        RecursorMetrics::inc(&metrics.trust_anchor_refresh_failures);
    };
    let set = match fetcher.fetch(zone, RecordType::DNSKEY).await {
        Ok(s) => s,
        Err(_) => return fail(&format!("network error fetching DNSKEY {zone}")),
    };
    let records: Vec<_> = set
        .answers
        .iter()
        .filter(|r| r.name == *zone && r.record_type() == RecordType::DNSKEY)
        .cloned()
        .collect();
    let sigs: Vec<_> = set
        .answers
        .iter()
        .filter(|r| {
            r.name == *zone
                && rrsig(r).is_some_and(|s| s.input().type_covered == RecordType::DNSKEY)
        })
        .cloned()
        .collect();
    let keys: Vec<DNSKEY> = records
        .iter()
        .filter_map(|r| match &r.data {
            RData::DNSSEC(DNSSECRData::DNSKEY(k)) => Some(k.clone()),
            _ => None,
        })
        .collect();
    let points = store.trust_points();
    let Some((_, tp)) = points.zones.iter().find(|(z, _)| z == zone) else {
        return fail(&format!("no trusted key for {zone}"));
    };
    let mut authorised = match match_ds(zone, &keys, &tp.ds) {
        DsMatch::Matched(k) => k,
        _ => Vec::new(),
    };
    authorised.extend(keys.iter().filter(|k| tp.keys.contains(k)).cloned());
    let now_unix = now.max(0) as u64;
    let Ok(v) = verify_rrset(&records, &sigs, &authorised, zone, now_unix) else {
        return fail("DNSKEY RRset not validated by a trusted key");
    };
    let mut observed = Vec::with_capacity(keys.len());
    let mut revoked = Vec::new();
    for k in keys {
        if !k.revoke() {
            observed.push(k);
        } else if k.secure_entry_point() && revoked_key_signs(&records, &sigs, &k, zone, now_unix) {
            let cleared = DNSKEY::with_flags(k.flags() & !0x0080, k.public_key().clone());
            if let Ok(tag) = cleared.calculate_key_tag() {
                revoked.push(tag);
                observed.push(cleared);
            }
        }
    }
    let obs = Observation {
        dnskeys: &observed,
        validated_by_trusted: true,
        revoked_self_signed: &revoked,
        orig_ttl: v.original_ttl,
        sig_expiration: i64::from(v.expiration),
    };
    if let Err(e) = store.record_observation(zone, &obs, now) {
        fail(&format!("persisting trust anchor state: {e}"));
    }
}
