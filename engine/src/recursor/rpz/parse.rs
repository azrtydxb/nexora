//! RPZ zone text / records -> trigger/action rules.

use hickory_proto::rr::{Name, RData, Record, RecordType};
use hickory_proto::serialize::txt::Parser;
use ipnet::{IpNet, Ipv4Net, Ipv6Net};
use std::net::{Ipv4Addr, Ipv6Addr};
use std::sync::Arc;

#[derive(Clone, Debug, PartialEq)]
pub enum CnameTarget {
    Name(Name),
    /// `*.suffix`: the target is the query name with `suffix` appended.
    WildcardSuffix(Name),
}

#[derive(Clone, Debug, PartialEq)]
pub struct LocalData {
    pub records: Vec<(RecordType, u32, RData)>,
    pub cname: Option<(u32, CnameTarget)>,
}

#[derive(Clone, Debug, PartialEq)]
pub enum RpzAction {
    Nxdomain,
    Nodata,
    Passthru,
    Drop,
    TcpOnly,
    LocalData(Arc<LocalData>),
}

impl RpzAction {
    /// Query-log code: 0 none, then the discriminant + 1 (`nxdomain` = 1 .. `local_data` = 6).
    pub fn log_code(&self) -> u8 {
        match self {
            RpzAction::Nxdomain => 1,
            RpzAction::Nodata => 2,
            RpzAction::Passthru => 3,
            RpzAction::Drop => 4,
            RpzAction::TcpOnly => 5,
            RpzAction::LocalData(_) => 6,
        }
    }
}

#[derive(Clone, Debug, PartialEq)]
pub enum Trigger {
    Qname { name: Name, wildcard: bool },
    ClientIp(IpNet),
    ResponseIp(IpNet),
    Nsdname { name: Name, wildcard: bool },
    Nsip(IpNet),
}

#[derive(Clone, Debug)]
pub struct ParsedRpz {
    pub origin: Name,
    pub serial: u32,
    pub soa: Record,
    pub rules: Vec<(Trigger, RpzAction)>,
    pub skipped: u64,
    pub records: u64,
}

/// Parses RPZ zone text. `$INCLUDE` is refused (the zone comes from the management plane or a
/// transfer, never from local files).
pub fn parse_rpz_text(origin: &Name, text: &str) -> Result<ParsedRpz, String> {
    if text.lines().any(|l| {
        l.trim_start()
            .get(..8)
            .is_some_and(|d| d.eq_ignore_ascii_case("$INCLUDE"))
    }) {
        return Err("$INCLUDE is not allowed in RPZ zones".into());
    }
    let parse = |t: &str| {
        Parser::new(t, None, Some(origin.clone()))
            .parse()
            .map_err(|e| e.to_string())
    };
    let sets = match parse(text) {
        Ok((_, sets)) => sets,
        // No $TTL and no explicit TTL on the first record: RFC 2308 default = SOA MINIMUM.
        Err(e) if e.contains("ttl not specified") => {
            let (_, probe) = parse(&format!("$TTL 0\n{text}"))?;
            let minimum = probe
                .values()
                .flat_map(|s| s.records_without_rrsigs())
                .find_map(|r| match &r.data {
                    RData::SOA(soa) if r.name == *origin => Some(soa.minimum),
                    _ => None,
                })
                .ok_or("RPZ zone has no SOA")?;
            parse(&format!("$TTL {minimum}\n{text}"))?.1
        }
        Err(e) => return Err(e),
    };
    let records: Vec<Record> = sets
        .into_values()
        .flat_map(|s| s.records_without_rrsigs().cloned().collect::<Vec<_>>())
        .collect();
    parse_rpz_records(origin, &records)
}

/// Builds rules from zone records (a transfer or parsed text). Owners outside `origin` and
/// undecodable triggers are counted in `skipped`.
pub fn parse_rpz_records(origin: &Name, records: &[Record]) -> Result<ParsedRpz, String> {
    let origin = origin.to_lowercase();
    let soa = records
        .iter()
        .find(|r| r.record_type() == RecordType::SOA && r.name == origin)
        .ok_or("RPZ zone has no SOA")?
        .clone();
    let serial = match &soa.data {
        RData::SOA(s) => s.serial,
        _ => unreachable!("record type checked"),
    };
    // Group the records of one owner (order of first appearance).
    let mut owners: Vec<(Name, Vec<&Record>)> = Vec::new();
    let mut index: rustc_hash::FxHashMap<Name, usize> = Default::default();
    let mut count = 0u64;
    let mut skipped = 0u64;
    for r in records {
        let apex = r.name == origin;
        if apex && matches!(r.record_type(), RecordType::SOA | RecordType::NS) {
            continue;
        }
        if matches!(
            r.record_type(),
            RecordType::RRSIG | RecordType::NSEC | RecordType::NSEC3
        ) {
            continue;
        }
        count += 1;
        if apex || !origin.zone_of(&r.name) {
            skipped += 1;
            continue;
        }
        let owner = r.name.to_lowercase();
        match index.get(&owner) {
            Some(&i) => owners[i].1.push(r),
            None => {
                index.insert(owner.clone(), owners.len());
                owners.push((owner, vec![r]));
            }
        }
    }
    let mut rules = Vec::with_capacity(owners.len());
    for (owner, recs) in owners {
        match rule(&origin, &owner, &recs) {
            Some(r) => rules.push(r),
            None => skipped += recs.len() as u64,
        }
    }
    Ok(ParsedRpz {
        origin,
        serial,
        soa,
        rules,
        skipped,
        records: count,
    })
}

fn rule(origin: &Name, owner: &Name, recs: &[&Record]) -> Option<(Trigger, RpzAction)> {
    let rel = label_count(owner) - label_count(origin);
    let labels: Vec<&[u8]> = owner.iter().take(rel).collect();
    let (last, before) = labels.split_last()?;
    let text = |ls: &[&[u8]]| -> Option<Vec<String>> {
        ls.iter()
            .map(|l| std::str::from_utf8(l).ok().map(str::to_ascii_lowercase))
            .collect()
    };
    let name_trigger = |ls: &[&[u8]]| -> Option<(Name, bool)> {
        match ls.split_first() {
            Some((first, rest)) if *first == b"*" => {
                Some((Name::from_labels(rest.iter().copied()).ok()?, true))
            }
            _ => Some((Name::from_labels(ls.iter().copied()).ok()?, false)),
        }
    };
    let trigger = match last.to_ascii_lowercase().as_slice() {
        b"rpz-client-ip" => Trigger::ClientIp(decode_ip_trigger(&text(before)?)?),
        b"rpz-ip" => Trigger::ResponseIp(decode_ip_trigger(&text(before)?)?),
        b"rpz-nsip" => Trigger::Nsip(decode_ip_trigger(&text(before)?)?),
        b"rpz-nsdname" => {
            let (name, wildcard) = name_trigger(before)?;
            Trigger::Nsdname { name, wildcard }
        }
        _ => {
            let (name, wildcard) = name_trigger(&labels)?;
            Trigger::Qname { name, wildcard }
        }
    };
    let trigger_name = match &trigger {
        Trigger::Qname { name, .. } | Trigger::Nsdname { name, .. } => Some(name.clone()),
        _ => None,
    };
    let cnames: Vec<&&Record> = recs
        .iter()
        .filter(|r| r.record_type() == RecordType::CNAME)
        .collect();
    let action = match cnames.as_slice() {
        [] => RpzAction::LocalData(Arc::new(LocalData {
            records: recs
                .iter()
                .map(|r| (r.record_type(), r.ttl, r.data.clone()))
                .collect(),
            cname: None,
        })),
        [c] if recs.len() == 1 => {
            let RData::CNAME(target) = &c.data else {
                return None;
            };
            let mut target = target.0.to_lowercase();
            if origin.zone_of(&target) && !origin.is_root() {
                target = Name::from_labels(
                    target
                        .iter()
                        .take(label_count(&target) - label_count(origin)),
                )
                .ok()?;
            }
            let first = target.iter().next();
            if target.is_root() {
                RpzAction::Nxdomain
            } else if label_count(&target) == 1 && first == Some(&b"*"[..]) {
                RpzAction::Nodata
            } else if target == Name::from_ascii("rpz-passthru.").ok()?
                || Some(&target) == trigger_name.as_ref()
            {
                RpzAction::Passthru
            } else if target == Name::from_ascii("rpz-drop.").ok()? {
                RpzAction::Drop
            } else if target == Name::from_ascii("rpz-tcp-only.").ok()? {
                RpzAction::TcpOnly
            } else if first == Some(&b"*"[..]) {
                let suffix = Name::from_labels(target.iter().skip(1)).ok()?;
                RpzAction::LocalData(Arc::new(LocalData {
                    records: Vec::new(),
                    cname: Some((c.ttl, CnameTarget::WildcardSuffix(suffix))),
                }))
            } else {
                RpzAction::LocalData(Arc::new(LocalData {
                    records: Vec::new(),
                    cname: Some((c.ttl, CnameTarget::Name(target))),
                }))
            }
        }
        _ => return None, // CNAME mixed with other data, or several CNAMEs
    };
    Some((trigger, action))
}

/// Labels including a leading `*` (`Name::num_labels` does not count it).
fn label_count(n: &Name) -> usize {
    n.iter().count()
}

/// Decodes the labels of an IP trigger owner (before the `rpz-*ip` suffix, zone order):
/// prefix length first, then the address least-significant part first.
pub fn decode_ip_trigger(labels: &[String]) -> Option<IpNet> {
    let (prefix, parts) = labels.split_first()?;
    if prefix.is_empty() || !prefix.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    let prefix: u8 = prefix.parse().ok()?;
    let decimal =
        |p: &String| !p.is_empty() && p.len() <= 3 && p.bytes().all(|b| b.is_ascii_digit());
    if parts.len() == 4
        && parts
            .iter()
            .all(|p| decimal(p) && p.parse::<u16>().is_ok_and(|v| v <= 255))
    {
        if !(1..=32).contains(&prefix) {
            return None;
        }
        let mut octets = [0u8; 4];
        for (i, p) in parts.iter().rev().enumerate() {
            octets[i] = p.parse().ok()?;
        }
        return Some(IpNet::V4(
            Ipv4Net::new(Ipv4Addr::from(octets), prefix).ok()?.trunc(),
        ));
    }
    if !(1..=128).contains(&prefix) || parts.is_empty() || parts.len() > 8 {
        return None;
    }
    let zz = parts
        .iter()
        .filter(|p| p.eq_ignore_ascii_case("zz"))
        .count();
    if zz > 1 || (zz == 0 && parts.len() != 8) {
        return None;
    }
    let mut words = Vec::with_capacity(8);
    for p in parts.iter().rev() {
        if p.eq_ignore_ascii_case("zz") {
            words.extend(std::iter::repeat_n(0u16, 8 - (parts.len() - 1)));
        } else {
            if p.is_empty() || p.len() > 4 {
                return None;
            }
            words.push(u16::from_str_radix(p, 16).ok()?);
        }
    }
    if words.len() != 8 {
        return None;
    }
    let mut w = [0u16; 8];
    w.copy_from_slice(&words);
    Some(IpNet::V6(
        Ipv6Net::new(Ipv6Addr::from(w), prefix).ok()?.trunc(),
    ))
}
