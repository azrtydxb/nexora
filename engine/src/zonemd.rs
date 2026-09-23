//! RFC 8976 zone digests (ZONEMD): digest and verify.
//!
//! The canonical form follows RFC 8976 §3.3: owners lowercased, RDATA names lowercased for the
//! RFC 4034 §6.2 types (as corrected by RFC 6840 §5.1), RRs sorted in RFC 4034 §6.1 order, then by
//! type and RDATA octets, duplicates included once, apex ZONEMD and its RRSIGs excluded. The
//! management plane (`mgmt/internal/zonemd`) implements the same digest; both are pinned to the
//! RFC 8976 Appendix A vectors. Verification runs on the control runtime, never on the query path.

use hickory_proto::dnssec::rdata::DNSSECRData;
use hickory_proto::rr::{Name, RData, Record};
use hickory_proto::serialize::binary::{BinEncodable, BinEncoder, NameEncoding};
use sha2::{Digest as _, Sha384, Sha512};

use crate::proto::{ZonemdStatus, ZonemdVerify};

pub const TYPE_ZONEMD: u16 = 63;
pub const SCHEME_SIMPLE: u8 = 1;
pub const HASH_SHA384: u8 = 1;
pub const HASH_SHA512: u8 = 2;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum VerifyMode {
    Off,
    IfPresent,
    Required,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Verdict {
    Off,
    Absent,
    Verified,
    Failed(String),
}

impl VerifyMode {
    /// `ZONEMD_VERIFY_UNSPECIFIED` (an older management plane) and unknown values mean off.
    pub fn from_proto(v: i32) -> VerifyMode {
        if v == ZonemdVerify::IfPresent as i32 {
            VerifyMode::IfPresent
        } else if v == ZonemdVerify::Required as i32 {
            VerifyMode::Required
        } else {
            VerifyMode::Off
        }
    }
}

impl Verdict {
    /// The `ZonemdStatus` value and the error text (empty unless failed).
    pub fn to_proto(&self) -> (i32, String) {
        match self {
            Verdict::Off => (ZonemdStatus::Off as i32, String::new()),
            Verdict::Absent => (ZonemdStatus::Absent as i32, String::new()),
            Verdict::Verified => (ZonemdStatus::Verified as i32, String::new()),
            Verdict::Failed(e) => (ZonemdStatus::Failed as i32, e.clone()),
        }
    }
}

/// One RR in canonical wire form with its sort key.
struct Canonical {
    /// Lowercase owner labels, root side first: `Vec` ordering is RFC 4034 §6.1 ordering.
    labels: Vec<Vec<u8>>,
    rtype: u16,
    wire: Vec<u8>,
    /// Offset of the RDATA in `wire`.
    rdata: usize,
}

fn canonical(origin: &Name, records: &[Record]) -> Result<Vec<Canonical>, String> {
    let mut out = Vec::with_capacity(records.len());
    for r in records {
        if !origin.zone_of(&r.name) {
            continue;
        }
        let rtype = u16::from(r.record_type());
        let covers_zonemd = matches!(&r.data, RData::DNSSEC(DNSSECRData::RRSIG(s)) if u16::from(s.input().type_covered) == TYPE_ZONEMD);
        if r.name == *origin && (rtype == TYPE_ZONEMD || covers_zonemd) {
            continue;
        }
        let mut wire = Vec::new();
        let mut enc = BinEncoder::new(&mut wire);
        // Canonical form lowercases RDATA names of the types hickory decodes (RFC 4034 §6.2).
        enc.set_canonical_form(true);
        enc.set_name_encoding(NameEncoding::UncompressedLowercase);
        r.emit(&mut enc)
            .map_err(|e| format!("encode {} {}: {e}", r.name, r.record_type()))?;
        let rdata = r.name.iter().map(|l| l.len() + 1).sum::<usize>() + 1 + 10;
        if rdata > wire.len() {
            return Err(format!(
                "encode {} {}: short record",
                r.name,
                r.record_type()
            ));
        }
        if let RData::Unknown { .. } = r.data {
            lowercase_opaque_names(rtype, &mut wire[rdata..]);
        }
        let labels = r
            .name
            .iter()
            .rev()
            .map(|l| l.to_ascii_lowercase())
            .collect();
        out.push(Canonical {
            labels,
            rtype,
            wire,
            rdata,
        });
    }
    out.sort_by(|a, b| {
        a.labels
            .cmp(&b.labels)
            .then(a.rtype.cmp(&b.rtype))
            .then_with(|| a.wire[a.rdata..].cmp(&b.wire[b.rdata..]))
    });
    // Equal owner, type, class and RDATA are digested once (the TTL does not distinguish them).
    out.dedup_by(|b, a| {
        a.wire[..a.rdata - 6] == b.wire[..b.rdata - 6] && a.wire[a.rdata..] == b.wire[b.rdata..]
    });
    Ok(out)
}

/// Lowercases the RDATA names of RFC 4034 §6.2 types that hickory keeps as opaque bytes.
/// Names carrying compression pointers are left unchanged (RFC 3597 §4 forbids them there).
fn lowercase_opaque_names(rtype: u16, rd: &mut [u8]) {
    let (skip, names) = match rtype {
        3 | 4 | 7 | 8 | 9 | 30 | 39 => (0, 1), // MD MF MB MG MR NXT DNAME
        14 | 17 => (0, 2),                     // MINFO RP
        18 | 21 | 36 => (2, 1),                // AFSDB RT KX
        26 => (2, 2),                          // PX
        38 => match rd.first() {
            // A6: prefix length, address suffix octets, prefix name when the prefix is not empty
            Some(&p) if p > 0 && p <= 128 => (1 + (128 - usize::from(p)).div_ceil(8), 1),
            _ => return,
        },
        _ => return,
    };
    let mut pos = skip;
    for _ in 0..names {
        loop {
            let Some(&len) = rd.get(pos) else { return };
            if len == 0 {
                pos += 1;
                break;
            }
            if len & 0xc0 != 0 {
                return;
            }
            let end = pos + 1 + usize::from(len);
            let Some(label) = rd.get_mut(pos + 1..end) else {
                return;
            };
            label.make_ascii_lowercase();
            pos = end;
        }
    }
}

/// The RFC 8976 SIMPLE-scheme digest of the zone at `origin` with SHA-384 or SHA-512.
pub fn digest(origin: &Name, records: &[Record], hash: u8) -> Result<Vec<u8>, String> {
    let rrs = canonical(origin, records)?;
    hash_canonical(&rrs, hash)
}

fn hash_canonical(rrs: &[Canonical], hash: u8) -> Result<Vec<u8>, String> {
    match hash {
        HASH_SHA384 => {
            let mut h = Sha384::new();
            rrs.iter().for_each(|c| h.update(&c.wire));
            Ok(h.finalize().to_vec())
        }
        HASH_SHA512 => {
            let mut h = Sha512::new();
            rrs.iter().for_each(|c| h.update(&c.wire));
            Ok(h.finalize().to_vec())
        }
        _ => Err(format!("unsupported hash algorithm {hash}")),
    }
}

/// Verifies the apex ZONEMD RRs of `records` (RFC 8976 §4). The first usable RR whose digest
/// matches verifies the zone; otherwise the verdict names the last reason.
pub fn verify(origin: &Name, records: &[Record], mode: VerifyMode) -> Verdict {
    if mode == VerifyMode::Off {
        return Verdict::Off;
    }
    let zonemds: Vec<&[u8]> = records
        .iter()
        .filter(|r| r.name == *origin && u16::from(r.record_type()) == TYPE_ZONEMD)
        .map(|r| match &r.data {
            RData::Unknown { rdata, .. } => rdata.anything.as_slice(),
            _ => &[],
        })
        .collect();
    if zonemds.is_empty() {
        return match mode {
            VerifyMode::IfPresent => Verdict::Absent,
            _ => Verdict::Failed("no apex ZONEMD".into()),
        };
    }
    let soa_serial = records.iter().find_map(|r| match &r.data {
        RData::SOA(soa) if r.name == *origin => Some(soa.serial),
        _ => None,
    });
    let mut rrs: Option<Vec<Canonical>> = None;
    let mut reason = String::from("no supported ZONEMD");
    for rd in &zonemds {
        if rd.len() < 6 {
            reason = "no supported ZONEMD".into();
            continue;
        }
        if zonemds
            .iter()
            .filter(|o| o.len() >= 6 && o[4..6] == rd[4..6])
            .count()
            > 1
        {
            reason = "duplicate scheme and hash".into();
            continue;
        }
        if soa_serial != Some(u32::from_be_bytes([rd[0], rd[1], rd[2], rd[3]])) {
            reason = "serial mismatch".into();
            continue;
        }
        let want_len = match (rd[4], rd[5]) {
            (SCHEME_SIMPLE, HASH_SHA384) => 48,
            (SCHEME_SIMPLE, HASH_SHA512) => 64,
            _ => {
                reason = "no supported ZONEMD".into();
                continue;
            }
        };
        if rd.len() - 6 != want_len {
            reason = "digest length mismatch".into();
            continue;
        }
        if rrs.is_none() {
            match canonical(origin, records) {
                Ok(c) => rrs = Some(c),
                Err(e) => return Verdict::Failed(e),
            }
        }
        match hash_canonical(rrs.as_deref().unwrap_or_default(), rd[5]) {
            Ok(d) if d == rd[6..] => return Verdict::Verified,
            Ok(_) => reason = "digest mismatch".into(),
            Err(e) => reason = e,
        }
    }
    Verdict::Failed(reason)
}

#[cfg(test)]
mod tests {
    use super::*;
    use hickory_proto::rr::{Name, RData, Record, RecordType};
    use hickory_proto::serialize::binary::BinDecodable;

    fn vector(name: &str) -> Vec<Record> {
        let path = format!(
            "{}/../e2e/testdata/rfc8976/{name}.hex",
            env!("CARGO_MANIFEST_DIR")
        );
        std::fs::read_to_string(&path)
            .unwrap()
            .lines()
            .map(|l| Record::from_bytes(&hex_decode(l)).unwrap())
            .collect()
    }

    fn hex_decode(s: &str) -> Vec<u8> {
        (0..s.len())
            .step_by(2)
            .map(|i| u8::from_str_radix(&s[i..i + 2], 16).unwrap())
            .collect()
    }

    fn zonemd_rdata(r: &Record) -> Option<&[u8]> {
        match &r.data {
            RData::Unknown { rdata, .. } if u16::from(r.record_type()) == TYPE_ZONEMD => {
                Some(&rdata.anything)
            }
            _ => None,
        }
    }

    #[test]
    fn digest_matches_rfc8976_appendix_a() {
        let origin = Name::from_ascii("example.").unwrap();
        let mut checked = 0;
        for name in ["a1", "a2", "a3"] {
            let rrs = vector(name);
            for r in &rrs {
                let Some(rd) = zonemd_rdata(r) else { continue };
                if r.name != origin
                    || rd[4] != SCHEME_SIMPLE
                    || !(rd[5] == HASH_SHA384 || rd[5] == HASH_SHA512)
                {
                    continue;
                }
                assert_eq!(
                    digest(&origin, &rrs, rd[5]).unwrap(),
                    rd[6..].to_vec(),
                    "{name} hash {}",
                    rd[5]
                );
                checked += 1;
            }
            assert_eq!(
                verify(&origin, &rrs, VerifyMode::Required),
                Verdict::Verified,
                "{name}"
            );
        }
        assert_eq!(checked, 4);
    }

    #[test]
    fn verify_rules() {
        let origin = Name::from_ascii("example.").unwrap();
        let base = vector("a1");
        let is_apex_zonemd =
            |r: &Record| u16::from(r.record_type()) == TYPE_ZONEMD && r.name == origin;
        let with_apex = |f: &dyn Fn(&mut Vec<u8>)| -> Vec<Record> {
            base.iter()
                .map(|r| {
                    if !is_apex_zonemd(r) {
                        return r.clone();
                    }
                    let mut rd = zonemd_rdata(r).unwrap().to_vec();
                    f(&mut rd);
                    Record::from_rdata(
                        r.name.clone(),
                        r.ttl,
                        RData::Unknown {
                            code: RecordType::Unknown(TYPE_ZONEMD),
                            rdata: hickory_proto::rr::rdata::NULL::with(rd),
                        },
                    )
                })
                .collect()
        };
        let without: Vec<Record> = base
            .iter()
            .filter(|r| !is_apex_zonemd(r))
            .cloned()
            .collect();
        assert_eq!(
            verify(&origin, &base, VerifyMode::IfPresent),
            Verdict::Verified
        );
        assert_eq!(
            verify(&origin, &with_apex(&|rd| rd[6] ^= 1), VerifyMode::Off),
            Verdict::Off
        );
        assert!(
            matches!(verify(&origin, &with_apex(&|rd| rd[3] ^= 1), VerifyMode::IfPresent), Verdict::Failed(e) if e.contains("serial"))
        );
        assert!(
            matches!(verify(&origin, &with_apex(&|rd| rd[6] ^= 1), VerifyMode::IfPresent), Verdict::Failed(e) if e.contains("digest"))
        );
        assert!(matches!(
            verify(
                &origin,
                &with_apex(&|rd| rd[5] = 200),
                VerifyMode::IfPresent
            ),
            Verdict::Failed(_)
        ));
        assert!(matches!(
            verify(
                &origin,
                &with_apex(&|rd| rd.truncate(6 + 11)),
                VerifyMode::IfPresent
            ),
            Verdict::Failed(_)
        ));
        let mut dup = base.clone();
        dup.extend(
            with_apex(&|rd| rd[7] ^= 1)
                .into_iter()
                .filter(|r| is_apex_zonemd(r)),
        );
        assert!(
            matches!(verify(&origin, &dup, VerifyMode::IfPresent), Verdict::Failed(e) if e.contains("duplicate"))
        );
        assert_eq!(
            verify(&origin, &without, VerifyMode::IfPresent),
            Verdict::Absent
        );
        assert!(matches!(
            verify(&origin, &without, VerifyMode::Required),
            Verdict::Failed(_)
        ));
        assert_eq!(VerifyMode::from_proto(0), VerifyMode::Off);
        assert_eq!(Verdict::Verified.to_proto().0, 3);
    }

    // Catches: RDATA names of RFC 4034 §6.2 types hickory keeps opaque (DNAME, RP, AFSDB) digested
    // with their zone-file case, so the engine and the management plane disagree on such zones.
    #[test]
    fn opaque_rdata_names_are_lowercased() {
        let origin = Name::from_ascii("example.").unwrap();
        let zone = |dname: &[u8], rp: &[u8], afsdb: &[u8], txt: &str| -> Vec<Record> {
            let opaque = |owner: &str, code: u16, rd: &[u8]| {
                Record::from_rdata(
                    Name::from_ascii(owner).unwrap(),
                    300,
                    RData::Unknown {
                        code: RecordType::Unknown(code),
                        rdata: hickory_proto::rr::rdata::NULL::with(rd.to_vec()),
                    },
                )
            };
            vec![
                opaque("d.example.", 39, dname),
                opaque("r.example.", 17, rp),
                opaque("a.example.", 18, afsdb),
                Record::from_rdata(
                    Name::from_ascii("t.example.").unwrap(),
                    300,
                    RData::TXT(hickory_proto::rr::rdata::TXT::new(vec![txt.into()])),
                ),
            ]
        };
        let lower = zone(
            b"\x01x\x07example\x00",
            b"\x01m\x00\x01t\x00",
            b"\x00\x01\x01h\x00",
            "Case",
        );
        let upper = zone(
            b"\x01X\x07EXAMPLE\x00",
            b"\x01M\x00\x01T\x00",
            b"\x00\x01\x01H\x00",
            "Case",
        );
        let text_differs = zone(
            b"\x01x\x07example\x00",
            b"\x01m\x00\x01t\x00",
            b"\x00\x01\x01h\x00",
            "case",
        );
        let d = digest(&origin, &lower, HASH_SHA384).unwrap();
        assert_ne!(
            d,
            digest(&origin, &text_differs, HASH_SHA384).unwrap(),
            "TXT case is data"
        );
        assert_eq!(d, digest(&origin, &upper, HASH_SHA384).unwrap());
    }
}
