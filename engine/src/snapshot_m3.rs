//! Validation of the M3 `ConfigSnapshot` fields (recursion, forward zones, DNSSEC, RPZ).

use crate::proto::{
    ConfigSnapshot, ResolutionMode, RpzPolicyOverride, TsigAlgorithm, rpz_zone::Source,
};
use crate::recursor::memory::{MAX_CACHE_MAX_BYTES, MIN_CACHE_MAX_BYTES};
use hickory_proto::rr::Name;
use std::collections::HashSet;
use std::net::{IpAddr, SocketAddr};

/// Checks the M3 fields of `s`; the error is `"<field path>: <reason>"`.
pub fn validate_m3(s: &ConfigSnapshot) -> Result<(), String> {
    if ResolutionMode::try_from(s.resolution_mode).is_err() {
        return Err(format!(
            "resolution_mode: unknown value {}",
            s.resolution_mode
        ));
    }
    if let Some(r) = &s.recursion {
        for (i, h) in r.root_hints.iter().enumerate() {
            fqdn(&h.name, &format!("recursion.root_hints[{i}].name"))?;
            for (j, a) in h.addresses.iter().enumerate() {
                if a.parse::<IpAddr>().is_err() {
                    return Err(format!(
                        "recursion.root_hints[{i}].addresses[{j}]: not an IP address: {a}"
                    ));
                }
            }
        }
        if r.max_upstream_queries > 1000 {
            return Err(format!(
                "recursion.max_upstream_queries: {} not in 1..=1000",
                r.max_upstream_queries
            ));
        }
        if r.max_delegation_depth > 64 {
            return Err(format!(
                "recursion.max_delegation_depth: {} not in 1..=64",
                r.max_delegation_depth
            ));
        }
        if r.cache_max_bytes != 0
            && !(MIN_CACHE_MAX_BYTES..=MAX_CACHE_MAX_BYTES).contains(&r.cache_max_bytes)
        {
            return Err(format!(
                "recursion.cache_max_bytes: {} not 0 or in {MIN_CACHE_MAX_BYTES}..={MAX_CACHE_MAX_BYTES}",
                r.cache_max_bytes
            ));
        }
        if r.authority_port > u32::from(u16::MAX) {
            return Err(format!(
                "recursion.authority_port: {} not in 0..=65535",
                r.authority_port
            ));
        }
    }
    let mut zones = HashSet::new();
    for (i, z) in s.forward_zones.iter().enumerate() {
        let lower = fqdn(&z.domain, &format!("forward_zones[{i}].domain"))?;
        if !zones.insert(lower.clone()) {
            return Err(format!("forward_zones[{i}].domain: duplicate {lower}"));
        }
        if z.addresses.is_empty() {
            return Err(format!(
                "forward_zones[{i}].addresses: at least one required"
            ));
        }
        for (j, a) in z.addresses.iter().enumerate() {
            if a.parse::<SocketAddr>().is_err() {
                return Err(format!(
                    "forward_zones[{i}].addresses[{j}]: not ip:port: {a}"
                ));
            }
        }
    }
    if let Some(d) = &s.dnssec {
        for (i, t) in d.trust_anchors.iter().enumerate() {
            fqdn(&t.zone, &format!("dnssec.trust_anchors[{i}].zone"))?;
            parse_ds(&t.ds).map_err(|e| format!("dnssec.trust_anchors[{i}].ds: {e}"))?;
        }
        for (i, n) in d.negative_trust_anchors.iter().enumerate() {
            fqdn(
                &n.domain,
                &format!("dnssec.negative_trust_anchors[{i}].domain"),
            )?;
            if n.expires_unix <= 0 {
                return Err(format!(
                    "dnssec.negative_trust_anchors[{i}].expires_unix: must be > 0"
                ));
            }
        }
    }
    if s.dnssec_validate_forwarded && !s.dnssec.as_ref().is_some_and(|d| d.validation) {
        return Err("dnssec_validate_forwarded: requires dnssec.validation".into());
    }
    let mut ids = HashSet::new();
    for (i, z) in s.rpz_zones.iter().enumerate() {
        if z.id.is_empty() {
            return Err(format!("rpz_zones[{i}].id: required"));
        }
        if !ids.insert(z.id.as_str()) {
            return Err(format!("rpz_zones[{i}].id: duplicate {}", z.id));
        }
        fqdn(&z.name, &format!("rpz_zones[{i}].name"))?;
        if RpzPolicyOverride::try_from(z.policy_override).is_err() {
            return Err(format!(
                "rpz_zones[{i}].policy_override: unknown value {}",
                z.policy_override
            ));
        }
        match &z.source {
            None => return Err(format!("rpz_zones[{i}]: no source")),
            Some(Source::File(f)) => match &f.blob {
                None => return Err(format!("rpz_zones[{i}].file.blob: required")),
                Some(b) if !crate::snapshot::is_sha256_hex(&b.sha256) => {
                    return Err(format!(
                        "rpz_zones[{i}].file.blob.sha256: must be 64 lowercase hex"
                    ));
                }
                Some(_) => {}
            },
            Some(Source::Transfer(t)) => {
                if t.primary.parse::<SocketAddr>().is_err() {
                    return Err(format!(
                        "rpz_zones[{i}].transfer.primary: not ip:port: {}",
                        t.primary
                    ));
                }
                match TsigAlgorithm::try_from(t.tsig_algorithm) {
                    Err(_) => {
                        return Err(format!(
                            "rpz_zones[{i}].transfer.tsig_algorithm: unknown value {}",
                            t.tsig_algorithm
                        ));
                    }
                    Ok(TsigAlgorithm::None) => {}
                    Ok(_) if t.tsig_key_name.is_empty() => {
                        return Err(format!(
                            "rpz_zones[{i}].transfer.tsig_key_name: required with tsig_algorithm"
                        ));
                    }
                    Ok(_) => {
                        fqdn(
                            &t.tsig_key_name,
                            &format!("rpz_zones[{i}].transfer.tsig_key_name"),
                        )?;
                    }
                }
            }
        }
    }
    Ok(())
}

/// Parses `"<key tag> <algorithm> <digest type> <hex digest>"` into its parts.
pub fn parse_ds(text: &str) -> Result<(u16, u8, u8, Vec<u8>), String> {
    let fields: Vec<&str> = text.split_whitespace().collect();
    let [tag, alg, dtype, digest] = fields[..] else {
        return Err("want \"<key tag> <algorithm> <digest type> <hex digest>\"".into());
    };
    let tag = tag
        .parse::<u16>()
        .map_err(|_| format!("key tag {tag} is not a u16"))?;
    let alg = alg
        .parse::<u8>()
        .map_err(|_| format!("algorithm {alg} is not a u8"))?;
    let dtype = dtype
        .parse::<u8>()
        .map_err(|_| format!("digest type {dtype} is not a u8"))?;
    let digest = data_encoding::HEXUPPER_PERMISSIVE
        .decode(digest.as_bytes())
        .map_err(|_| "digest is not hex".to_string())?;
    let want = match dtype {
        1 => 20,
        2 => 32,
        4 => 48,
        t => return Err(format!("unsupported digest type {t}")),
    };
    if digest.len() != want {
        return Err(format!(
            "digest length {} does not match digest type {dtype}",
            digest.len()
        ));
    }
    Ok((tag, alg, dtype, digest))
}

/// `v` as a lowercase fully-qualified name, or an error naming `path`.
fn fqdn(v: &str, path: &str) -> Result<String, String> {
    match Name::from_ascii(v) {
        Ok(n) if n.is_fqdn() => Ok(v.to_ascii_lowercase()),
        _ => Err(format!("{path}: not a fully-qualified domain name: {v}")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::proto::{
        BlobRef, ConfigSnapshot, DnssecConfig, ForwardZone, NegativeTrustAnchor, RecursionConfig,
        RootHint, RpzFileSource, RpzTransferSource, RpzZone, TrustAnchor, TsigAlgorithm,
        rpz_zone::Source,
    };

    fn ok_snapshot() -> ConfigSnapshot {
        ConfigSnapshot {
            resolution_mode: crate::proto::ResolutionMode::Recursive as i32,
            recursion: Some(RecursionConfig {
                root_hints: vec![RootHint {
                    name: "a.root.test.".into(),
                    addresses: vec!["127.0.53.1".into(), "::1".into()],
                }],
                qname_minimisation: true,
                aggressive_nsec: false,
                max_upstream_queries: 100,
                max_delegation_depth: 32,
                authority_port: 5353,
                cache_max_bytes: 0,
            }),
            forward_zones: vec![ForwardZone {
                domain: "corp.example.".into(),
                addresses: vec!["10.0.0.1:53".into()],
                validate: false,
            }],
            dnssec: Some(DnssecConfig {
                validation: true,
                trust_anchors: vec![TrustAnchor {
                    zone: ".".into(),
                    ds:
                        "20326 8 2 E06D44B80B8F1D39A95C0B0D7C65D08458E880409BBC683457104237C7F8EC8D"
                            .into(),
                }],
                negative_trust_anchors: vec![NegativeTrustAnchor {
                    domain: "broken.example.".into(),
                    expires_unix: 4_000_000_000,
                }],
                rfc5011: true,
            }),
            rpz_zones: vec![
                RpzZone {
                    id: "11111111-1111-1111-1111-111111111111".into(),
                    name: "rpz.file.".into(),
                    source: Some(Source::File(RpzFileSource {
                        blob: Some(BlobRef {
                            sha256: "a".repeat(64),
                            size: 10,
                            name: "rpz.file.".into(),
                        }),
                    })),
                    policy_override: 0,
                    refresh_nonce: 0,
                },
                RpzZone {
                    id: "22222222-2222-2222-2222-222222222222".into(),
                    name: "rpz.axfr.".into(),
                    source: Some(Source::Transfer(RpzTransferSource {
                        primary: "127.0.0.1:5300".into(),
                        tsig_key_name: "rpz-key.".into(),
                        tsig_algorithm: TsigAlgorithm::HmacSha256 as i32,
                        min_refresh_seconds: 0,
                    })),
                    policy_override: 0,
                    refresh_nonce: 0,
                },
            ],
            dnssec_validate_forwarded: true,
            ..Default::default()
        }
    }

    #[test]
    fn accepts_valid_m3_fields() {
        assert_eq!(validate_m3(&ok_snapshot()), Ok(()));
    }

    #[test]
    fn rejects_bad_root_hint_address() {
        let mut s = ok_snapshot();
        s.recursion.as_mut().unwrap().root_hints[0].addresses[0] = "127.0.53.1:53".into();
        assert_eq!(
            validate_m3(&s).unwrap_err(),
            "recursion.root_hints[0].addresses[0]: not an IP address: 127.0.53.1:53"
        );
    }

    #[test]
    fn rejects_limits_out_of_range() {
        let mut s = ok_snapshot();
        s.recursion.as_mut().unwrap().max_upstream_queries = 1001;
        assert_eq!(
            validate_m3(&s).unwrap_err(),
            "recursion.max_upstream_queries: 1001 not in 1..=1000"
        );
        let mut s = ok_snapshot();
        s.recursion.as_mut().unwrap().max_delegation_depth = 65;
        assert_eq!(
            validate_m3(&s).unwrap_err(),
            "recursion.max_delegation_depth: 65 not in 1..=64"
        );
    }

    #[test]
    fn cache_max_bytes_outside_range_is_rejected() {
        for (bytes, ok) in [
            (0u64, true),
            (4 << 20, true),
            (64 << 20, true),
            (16 << 30, true),
            ((4 << 20) - 1, false),
            ((16 << 30) + 1, false),
        ] {
            let mut s = ok_snapshot();
            s.recursion.as_mut().unwrap().cache_max_bytes = bytes;
            assert_eq!(validate_m3(&s).is_ok(), ok, "cache_max_bytes {bytes}");
        }
    }

    #[test]
    fn rejects_duplicate_forward_zone() {
        let mut s = ok_snapshot();
        s.forward_zones.push(ForwardZone {
            domain: "CORP.example.".into(),
            addresses: vec!["10.0.0.2:53".into()],
            validate: false,
        });
        assert_eq!(
            validate_m3(&s).unwrap_err(),
            "forward_zones[1].domain: duplicate corp.example."
        );
    }

    #[test]
    fn rejects_forward_zone_without_port() {
        let mut s = ok_snapshot();
        s.forward_zones[0].addresses[0] = "10.0.0.1".into();
        assert_eq!(
            validate_m3(&s).unwrap_err(),
            "forward_zones[0].addresses[0]: not ip:port: 10.0.0.1"
        );
    }

    #[test]
    fn rejects_malformed_trust_anchor() {
        let mut s = ok_snapshot();
        s.dnssec.as_mut().unwrap().trust_anchors[0].ds = "20326 8 2 ZZ".into();
        assert_eq!(
            validate_m3(&s).unwrap_err(),
            "dnssec.trust_anchors[0].ds: digest is not hex"
        );
    }

    #[test]
    fn rejects_rpz_without_source_bad_blob_and_keyless_tsig() {
        let mut s = ok_snapshot();
        s.rpz_zones[0].source = None;
        assert_eq!(validate_m3(&s).unwrap_err(), "rpz_zones[0]: no source");
        let mut s = ok_snapshot();
        if let Some(Source::File(f)) = s.rpz_zones[0].source.as_mut() {
            f.blob.as_mut().unwrap().sha256 = "A".repeat(64);
        }
        assert_eq!(
            validate_m3(&s).unwrap_err(),
            "rpz_zones[0].file.blob.sha256: must be 64 lowercase hex"
        );
        let mut s = ok_snapshot();
        if let Some(Source::Transfer(t)) = s.rpz_zones[1].source.as_mut() {
            t.tsig_key_name = String::new();
        }
        assert_eq!(
            validate_m3(&s).unwrap_err(),
            "rpz_zones[1].transfer.tsig_key_name: required with tsig_algorithm"
        );
    }

    #[test]
    fn rejects_duplicate_rpz_id() {
        let mut s = ok_snapshot();
        s.rpz_zones[1].id = s.rpz_zones[0].id.clone();
        assert_eq!(
            validate_m3(&s).unwrap_err(),
            "rpz_zones[1].id: duplicate 11111111-1111-1111-1111-111111111111"
        );
    }

    #[test]
    fn validate_forwarded_requires_validation() {
        let mut s = ok_snapshot();
        s.dnssec.as_mut().unwrap().validation = false;
        assert_eq!(
            validate_m3(&s).unwrap_err(),
            "dnssec_validate_forwarded: requires dnssec.validation"
        );
        s.dnssec = None;
        assert_eq!(
            validate_m3(&s).unwrap_err(),
            "dnssec_validate_forwarded: requires dnssec.validation"
        );
        s.dnssec_validate_forwarded = false;
        assert_eq!(validate_m3(&s), Ok(()));
    }
}
