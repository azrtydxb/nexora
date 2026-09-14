//! RRSIG verification (RFC 4035 §5.3) over hickory-proto's `Verifier`, validity windows with
//! clock skew, and DS matching (RFC 4035 §5.2, RFC 4509 §3).

use hickory_proto::dnssec::rdata::{DNSKEY, DNSSECRData, DS, RRSIG};
use hickory_proto::dnssec::{Algorithm, DigestType, Verifier};
use hickory_proto::rr::{DNSClass, Name, RData, Record};

/// RSASHA256, ECDSAP256SHA256, ECDSAP384SHA384, ED25519. Anything else is treated as insecure.
pub const SUPPORTED_ALGORITHMS: [u8; 4] = [8, 13, 14, 15];

#[derive(Debug, PartialEq, Eq, Clone, Copy)]
pub enum Validity {
    Valid,
    NotYetValid,
    Expired,
    Malformed,
}

/// RFC 4034 §3.1.5 arithmetic modulo 2^32; skew is 10% of the signature lifetime clamped to
/// 300..=3600 s so signatures on hosts with modest clock skew still validate near both edges.
pub fn within_validity(inception: u32, expiration: u32, now_unix: u64) -> Validity {
    let now = now_unix as u32;
    let span = expiration.wrapping_sub(inception);
    if span == 0 || span > i32::MAX as u32 {
        return Validity::Malformed;
    }
    let skew = (span / 10).clamp(300, 3600);
    if (now.wrapping_sub(inception.wrapping_sub(skew)) as i32) < 0 {
        return Validity::NotYetValid;
    }
    if (expiration.wrapping_add(skew).wrapping_sub(now) as i32) < 0 {
        return Validity::Expired;
    }
    Validity::Valid
}

pub fn algorithm_supported(a: Algorithm) -> bool {
    SUPPORTED_ALGORITHMS.contains(&u8::from(a))
}

pub fn digest_supported(d: DigestType) -> bool {
    matches!(
        d,
        DigestType::SHA1 | DigestType::SHA256 | DigestType::SHA384
    )
}

#[derive(Debug, PartialEq, Eq, Clone, Copy)]
pub enum VerifyError {
    NoRrsig,
    NoMatchingKey,
    UnsupportedAlgorithm,
    Expired,
    NotYetValid,
    BadSignature,
    SignerMismatch,
    LabelCount,
}

impl VerifyError {
    /// Higher is more specific; the most specific failure over all candidate RRSIGs is reported.
    fn rank(self) -> u8 {
        match self {
            VerifyError::BadSignature => 7,
            VerifyError::Expired => 6,
            VerifyError::NotYetValid => 5,
            VerifyError::NoMatchingKey => 4,
            VerifyError::UnsupportedAlgorithm => 3,
            VerifyError::SignerMismatch => 2,
            VerifyError::LabelCount => 1,
            VerifyError::NoRrsig => 0,
        }
    }
}

#[derive(Debug, Clone, Copy)]
pub struct VerifiedSig {
    pub key_tag: u16,
    pub expiration: u32,
    pub original_ttl: u32,
    pub wildcard_expanded: bool,
}

/// The RRSIG rdata of `r`, if it is an RRSIG record.
pub fn rrsig(r: &Record) -> Option<&RRSIG> {
    match &r.data {
        RData::DNSSEC(DNSSECRData::RRSIG(s)) => Some(s),
        _ => None,
    }
}

/// Verifies `rrset` (one owner, one type) with one of `rrsigs` made by `zone` with one of `keys`.
pub fn verify_rrset(
    rrset: &[Record],
    rrsigs: &[Record],
    keys: &[DNSKEY],
    zone: &Name,
    now_unix: u64,
) -> Result<VerifiedSig, VerifyError> {
    verify_inner(rrset, rrsigs, keys, zone, now_unix, false)
}

/// The signature with which `key`, which carries the REVOKE flag, signs `rrset` itself
/// (RFC 5011 §2.1).
pub fn revoked_key_verifies(
    rrset: &[Record],
    rrsigs: &[Record],
    key: &DNSKEY,
    zone: &Name,
    now_unix: u64,
) -> Option<VerifiedSig> {
    key.revoke()
        .then(|| {
            verify_inner(
                rrset,
                rrsigs,
                std::slice::from_ref(key),
                zone,
                now_unix,
                true,
            )
            .ok()
        })
        .flatten()
}

/// Whether `key`, which carries the REVOKE flag, signs `rrset` itself (RFC 5011 §2.1).
pub fn revoked_key_signs(
    rrset: &[Record],
    rrsigs: &[Record],
    key: &DNSKEY,
    zone: &Name,
    now_unix: u64,
) -> bool {
    revoked_key_verifies(rrset, rrsigs, key, zone, now_unix).is_some()
}

fn verify_inner(
    rrset: &[Record],
    rrsigs: &[Record],
    keys: &[DNSKEY],
    zone: &Name,
    now_unix: u64,
    allow_revoked: bool,
) -> Result<VerifiedSig, VerifyError> {
    let Some(first) = rrset.first() else {
        return Err(VerifyError::NoRrsig);
    };
    let owner = &first.name;
    let rtype = first.record_type();
    let owner_labels = owner.num_labels();
    let mut err = VerifyError::NoRrsig;
    let remember = |e: VerifyError, err: &mut VerifyError| {
        if e.rank() > err.rank() {
            *err = e;
        }
    };
    let candidates = rrsigs
        .iter()
        .filter(|r| r.name == *owner)
        .filter_map(rrsig)
        .filter(|s| s.input().type_covered == rtype);
    for sig in candidates {
        let input = sig.input();
        if input.signer_name != *zone {
            remember(VerifyError::SignerMismatch, &mut err);
            continue;
        }
        if !algorithm_supported(input.algorithm) {
            remember(VerifyError::UnsupportedAlgorithm, &mut err);
            continue;
        }
        if input.num_labels > owner_labels {
            remember(VerifyError::LabelCount, &mut err);
            continue;
        }
        match within_validity(
            input.sig_inception.get(),
            input.sig_expiration.get(),
            now_unix,
        ) {
            Validity::Valid => {}
            Validity::NotYetValid => {
                remember(VerifyError::NotYetValid, &mut err);
                continue;
            }
            Validity::Expired | Validity::Malformed => {
                remember(VerifyError::Expired, &mut err);
                continue;
            }
        }
        let mut matched = false;
        for key in keys.iter().filter(|k| {
            k.zone_key()
                && (allow_revoked || !k.revoke())
                && k.algorithm() == input.algorithm
                && k.calculate_key_tag().ok() == Some(input.key_tag)
        }) {
            matched = true;
            if key
                .verify_rrsig(owner, DNSClass::IN, sig, rrset.iter())
                .is_ok()
            {
                return Ok(VerifiedSig {
                    key_tag: input.key_tag,
                    expiration: input.sig_expiration.get(),
                    original_ttl: input.original_ttl,
                    wildcard_expanded: input.num_labels < owner_labels,
                });
            }
        }
        remember(
            if matched {
                VerifyError::BadSignature
            } else {
                VerifyError::NoMatchingKey
            },
            &mut err,
        );
    }
    Err(err)
}

/// `min(ttl, original_ttl, expiration - now)` (RFC 4035 §5.3.3).
pub fn capped_ttl(rrset_ttl: u32, sig: &VerifiedSig, now_unix: u64) -> u32 {
    let left = sig.expiration.wrapping_sub(now_unix as u32);
    let left = if (left as i32) < 0 { 0 } else { left };
    rrset_ttl.min(sig.original_ttl).min(left)
}

#[derive(Debug)]
pub enum DsMatch {
    Matched(Vec<DNSKEY>),
    NoSupported,
    NoMatch,
}

/// The zone keys of `dnskeys` referenced by a supported DS record.
pub fn match_ds(zone: &Name, dnskeys: &[DNSKEY], ds: &[DS]) -> DsMatch {
    let supported: Vec<&DS> = ds
        .iter()
        .filter(|d| algorithm_supported(d.algorithm()) && digest_supported(d.digest_type()))
        .collect();
    if supported.is_empty() {
        return DsMatch::NoSupported;
    }
    // RFC 4509 §3: SHA-1 DS records are ignored when a stronger digest exists for the key.
    let usable = supported.iter().filter(|d| {
        d.digest_type() != DigestType::SHA1
            || !supported
                .iter()
                .any(|o| o.key_tag() == d.key_tag() && o.digest_type() != DigestType::SHA1)
    });
    let mut matched: Vec<DNSKEY> = Vec::new();
    for d in usable {
        for k in dnskeys {
            if k.algorithm() == d.algorithm()
                && k.calculate_key_tag().ok() == Some(d.key_tag())
                && d.covers(zone, k).unwrap_or(false)
                && !matched.contains(k)
            {
                matched.push(k.clone());
            }
        }
    }
    if matched.is_empty() {
        DsMatch::NoMatch
    } else {
        DsMatch::Matched(matched)
    }
}
