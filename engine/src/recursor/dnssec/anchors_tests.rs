#![allow(clippy::cloned_ref_to_slice_refs)] // plan-given test literals

use super::anchors::*;
use super::testsign::TestKey;
use super::validator::{FetchError, FetchedSet, Fetcher};
use crate::proto;
use crate::recursor::LocalBoxFuture;
use crate::recursor::metrics::RecursorMetrics;
use hickory_proto::dnssec::rdata::DNSKEY;
use hickory_proto::op::ResponseCode;
use hickory_proto::rr::{Name, Record, RecordType};
use std::cell::RefCell;

const DAY: i64 = 86_400;
const T0: i64 = 1_800_000_000;

fn ds_text(k: &TestKey) -> String {
    let ds = k.ds();
    format!(
        "{} {} {} {}",
        ds.key_tag(),
        u8::from(ds.algorithm()),
        u8::from(ds.digest_type()),
        data_encoding::HEXUPPER.encode(ds.digest())
    )
}

fn obs<'a>(keys: &'a [DNSKEY], revoked: &'a [u16]) -> Observation<'a> {
    Observation {
        dnskeys: keys,
        validated_by_trusted: true,
        revoked_self_signed: revoked,
        orig_ttl: 172_800,
        sig_expiration: T0 + 10 * DAY,
    }
}

fn store_with(k: &TestKey) -> (tempfile::TempDir, std::sync::Arc<TrustAnchorStore>) {
    let dir = tempfile::tempdir().unwrap();
    let s = TrustAnchorStore::open(Some(dir.path().join("trust-anchors.json")));
    s.merge_config(
        &[proto::TrustAnchor {
            zone: ".".into(),
            ds: ds_text(k),
        }],
        true,
        T0,
    )
    .unwrap();
    (dir, s)
}

fn state_of(s: &TrustAnchorStore, tag: u16) -> Option<KeyState> {
    s.status()
        .into_iter()
        .find(|a| a.key_tag == tag as u32)
        .map(|a| match a.state {
            x if x == proto::TrustAnchorState::Configured as i32 => KeyState::Configured,
            x if x == proto::TrustAnchorState::AddPend as i32 => KeyState::AddPend,
            x if x == proto::TrustAnchorState::Valid as i32 => KeyState::Valid,
            x if x == proto::TrustAnchorState::Missing as i32 => KeyState::Missing,
            _ => KeyState::Revoked,
        })
}

#[test]
fn configured_ds_becomes_valid_when_observed() {
    let old = TestKey::generate(".", true);
    let (_d, s) = store_with(&old);
    let tag = old.dnskey.calculate_key_tag().unwrap();
    assert_eq!(state_of(&s, tag), Some(KeyState::Configured));
    s.record_observation(&Name::root(), &obs(&[old.dnskey.clone()], &[]), T0)
        .unwrap();
    assert_eq!(state_of(&s, tag), Some(KeyState::Valid));
}

#[test]
fn new_key_waits_add_hold_down_then_becomes_trusted() {
    let old = TestKey::generate(".", true);
    let new = TestKey::generate(".", true);
    let (_d, s) = store_with(&old);
    let keys = [old.dnskey.clone(), new.dnskey.clone()];
    let new_tag = new.dnskey.calculate_key_tag().unwrap();
    s.record_observation(&Name::root(), &obs(&keys, &[]), T0)
        .unwrap();
    assert_eq!(state_of(&s, new_tag), Some(KeyState::AddPend));
    assert_eq!(
        s.trust_points().zones[0].1.key_count(),
        1,
        "AddPend is not trusted"
    );
    s.record_observation(&Name::root(), &obs(&keys, &[]), T0 + 29 * DAY)
        .unwrap();
    assert_eq!(state_of(&s, new_tag), Some(KeyState::AddPend));
    s.record_observation(&Name::root(), &obs(&keys, &[]), T0 + 30 * DAY)
        .unwrap();
    assert_eq!(state_of(&s, new_tag), Some(KeyState::Valid));
    assert_eq!(s.trust_points().zones[0].1.key_count(), 2);
}

#[test]
fn pending_key_that_disappears_is_forgotten_and_unvalidated_observations_change_nothing() {
    let old = TestKey::generate(".", true);
    let new = TestKey::generate(".", true);
    let (_d, s) = store_with(&old);
    let new_tag = new.dnskey.calculate_key_tag().unwrap();
    let mut o = obs(&[], &[]);
    let both = [old.dnskey.clone(), new.dnskey.clone()];
    o.dnskeys = &both;
    o.validated_by_trusted = false;
    s.record_observation(&Name::root(), &o, T0).unwrap();
    assert_eq!(
        state_of(&s, new_tag),
        None,
        "an RRset not signed by a trusted key is ignored"
    );
    s.record_observation(&Name::root(), &obs(&both, &[]), T0)
        .unwrap();
    s.record_observation(&Name::root(), &obs(&[old.dnskey.clone()], &[]), T0 + DAY)
        .unwrap();
    assert_eq!(state_of(&s, new_tag), None);
}

#[test]
fn revoked_self_signed_key_is_revoked_and_removed_after_hold_down() {
    let old = TestKey::generate(".", true);
    let (_d, s) = store_with(&old);
    let tag = old.dnskey.calculate_key_tag().unwrap();
    s.record_observation(&Name::root(), &obs(&[old.dnskey.clone()], &[]), T0)
        .unwrap();
    s.record_observation(&Name::root(), &obs(&[old.dnskey.clone()], &[tag]), T0 + DAY)
        .unwrap();
    assert_eq!(state_of(&s, tag), Some(KeyState::Revoked));
    assert_eq!(
        s.trust_points().zones.len(),
        0,
        "a zone whose only key is revoked has no trust point"
    );
    s.record_observation(&Name::root(), &obs(&[], &[]), T0 + DAY + 30 * DAY)
        .unwrap();
    assert_eq!(state_of(&s, tag), None);
}

#[test]
fn state_survives_restart_and_mgmt_removal_drops_zone() {
    let old = TestKey::generate(".", true);
    let (d, s) = store_with(&old);
    let tag = old.dnskey.calculate_key_tag().unwrap();
    s.record_observation(&Name::root(), &obs(&[old.dnskey.clone()], &[]), T0)
        .unwrap();
    drop(s);
    let s = TrustAnchorStore::open(Some(d.path().join("trust-anchors.json")));
    assert_eq!(state_of(&s, tag), Some(KeyState::Valid));
    s.merge_config(&[], true, T0 + 1).unwrap();
    assert!(s.status().is_empty());
}

#[test]
fn refresh_and_retry_intervals_follow_rfc5011_section_2_3() {
    assert_eq!(refresh_interval(172_800, T0 + 10 * DAY, T0), DAY); // min(15d, ttl/2=1d, exp/2=5d)
    assert_eq!(refresh_interval(600, T0 + 10 * DAY, T0), 3600); // floor 1h
    assert_eq!(retry_interval(172_800, T0 + 10 * DAY, T0), 17_280); // min(1d, ttl/10, exp/10)
    assert_eq!(retry_interval(600, T0 + 10 * DAY, T0), 3600);
}

struct Dnskeys(RefCell<Option<Vec<Record>>>);

impl Fetcher for Dnskeys {
    fn fetch<'a>(
        &'a self,
        name: &'a Name,
        rtype: RecordType,
    ) -> LocalBoxFuture<'a, Result<FetchedSet, FetchError>> {
        assert!(name.is_root() && rtype == RecordType::DNSKEY);
        let r = self
            .0
            .borrow()
            .clone()
            .map(|answers| FetchedSet {
                rcode: ResponseCode::NoError,
                answers,
                authorities: vec![],
            })
            .ok_or(FetchError::Unreachable);
        Box::pin(async move { r })
    }
}

fn signed_rrset(keys: &[&TestKey], signers: &[&TestKey], now: i64) -> Vec<Record> {
    let set: Vec<Record> = keys.iter().map(|k| k.dnskey_record()).collect();
    let mut out = set.clone();
    for s in signers {
        out.push(s.sign(&set, (now - 3600) as u32, (now + 10 * DAY) as u32));
    }
    out
}

#[tokio::test(flavor = "current_thread")]
async fn refresh_authenticates_rollover_and_self_signed_revocation() {
    let mut old = TestKey::generate(".", true);
    let new = TestKey::generate(".", true);
    let (_d, s) = store_with(&old);
    let metrics = RecursorMetrics::default();
    let root = Name::root();
    let old_tag = old.dnskey.calculate_key_tag().unwrap();
    let new_tag = new.dnskey.calculate_key_tag().unwrap();
    assert_eq!(s.due(T0), vec![root.clone()]);

    // an RRset signed only by the untrusted new key is refused
    let f = Dnskeys(RefCell::new(Some(signed_rrset(&[&old, &new], &[&new], T0))));
    refresh_zone(&s, &root, &f, &metrics, T0).await;
    assert_eq!(state_of(&s, new_tag), None);
    assert_eq!(
        metrics
            .trust_anchor_refresh_failures
            .load(std::sync::atomic::Ordering::Relaxed),
        1
    );

    // signed by the configured key: old becomes Valid, new starts its hold-down
    *f.0.borrow_mut() = Some(signed_rrset(&[&old, &new], &[&old], T0));
    refresh_zone(&s, &root, &f, &metrics, T0).await;
    assert_eq!(state_of(&s, old_tag), Some(KeyState::Valid));
    assert_eq!(state_of(&s, new_tag), Some(KeyState::AddPend));
    assert!(s.due(T0 + 3599).is_empty(), "next refresh scheduled");
    *f.0.borrow_mut() = Some(signed_rrset(&[&old, &new], &[&old], T0 + 30 * DAY));
    refresh_zone(&s, &root, &f, &metrics, T0 + 30 * DAY).await;
    assert_eq!(state_of(&s, new_tag), Some(KeyState::Valid));

    // old publishes itself revoked and signs with its revoked form; new (now trusted) signs too
    old.set_revoked();
    let now = T0 + 31 * DAY;
    *f.0.borrow_mut() = Some(signed_rrset(&[&old, &new], &[&old, &new], now));
    refresh_zone(&s, &root, &f, &metrics, now).await;
    assert_eq!(state_of(&s, old_tag), Some(KeyState::Revoked));
    assert_eq!(state_of(&s, new_tag), Some(KeyState::Valid));
    assert_eq!(s.trust_points().zones[0].1.key_count(), 1);

    // unreachable: failure recorded, retried later
    *f.0.borrow_mut() = None;
    refresh_zone(&s, &root, &f, &metrics, now + DAY).await;
    let status = s.status();
    assert!(status[0].last_error.contains("network error"), "{status:?}");
}

#[tokio::test(flavor = "current_thread")]
async fn refresh_accepts_revocation_of_the_only_trusted_key() {
    let mut only = TestKey::generate(".", true);
    let newcomer = TestKey::generate(".", true);
    let (_d, s) = store_with(&only);
    let metrics = RecursorMetrics::default();
    let root = Name::root();
    let tag = only.dnskey.calculate_key_tag().unwrap();
    let only_ds = ds_text(&only);
    let f = Dnskeys(RefCell::new(Some(signed_rrset(&[&only], &[&only], T0))));
    refresh_zone(&s, &root, &f, &metrics, T0).await;
    assert_eq!(state_of(&s, tag), Some(KeyState::Valid));

    // The only trusted key revokes itself; a new key appears, signed by nothing trusted.
    only.set_revoked();
    let now = T0 + DAY;
    *f.0.borrow_mut() = Some(signed_rrset(&[&only, &newcomer], &[&only], now));
    refresh_zone(&s, &root, &f, &metrics, now).await;
    assert_eq!(
        state_of(&s, tag),
        Some(KeyState::Revoked),
        "{:?}",
        s.status()
    );
    assert_eq!(
        s.trust_points().zones.len(),
        0,
        "no trusted key is left for the root"
    );
    assert_eq!(s.lost_trust_points(), vec![root.clone()]);
    assert_eq!(
        state_of(&s, newcomer.dnskey.calculate_key_tag().unwrap()),
        None,
        "a key vouched for only by a revoked key is not added"
    );
    assert_eq!(
        metrics
            .trust_anchor_refresh_failures
            .load(std::sync::atomic::Ordering::Relaxed),
        0
    );

    // An operator adds the newcomer as a trust anchor: the root has a trust point again.
    s.merge_config(
        &[
            proto::TrustAnchor {
                zone: ".".into(),
                ds: only_ds,
            },
            proto::TrustAnchor {
                zone: ".".into(),
                ds: ds_text(&newcomer),
            },
        ],
        true,
        now,
    )
    .unwrap();
    assert_eq!(state_of(&s, tag), Some(KeyState::Revoked));
    assert!(s.lost_trust_points().is_empty());
}
