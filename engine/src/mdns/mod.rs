//! mDNS (RFC 6762) gateway and reflector.

pub mod gateway;
pub mod iface;
pub mod reflector;

use crate::proto;
use crate::recursor::dispatch::{MissAnswer, MissQuery, RouteTaken, SecurityTag, build_response};
use crate::telemetry::querylog::NO_RPZ_ZONE;
use bytes::Bytes;
use gateway::{Gateway, Outcome, Target};
use hickory_proto::op::ResponseCode;
use hickory_proto::rr::Record;
use iface::IfaceTarget;
use reflector::Reflector;
use std::sync::{Arc, Mutex};
use std::time::Duration;

/// Collection timeout when the snapshot leaves it at 0.
pub const DEFAULT_TIMEOUT_MS: u32 = 500;
const TIMEOUT_MS: std::ops::RangeInclusive<u32> = 100..=5000;
const MAX_INTERFACES: usize = 64;

/// The snapshot's `MdnsConfig` with its interfaces resolved on this engine.
#[derive(Default)]
pub struct MdnsRuntime {
    /// The gateway while it is enabled; the recursor dispatch holds a clone.
    pub gateway: Option<Arc<Gateway>>,
    /// Cache identity includes configured names and resolved addresses, not just enabled state.
    pub gateway_key: String,
    /// Reflection interfaces present on the engine; empty while reflection is off.
    pub reflect: Vec<IfaceTarget>,
    /// Identity of `reflect`: the reflector restarts only when it changes. Empty while off.
    pub reflect_key: String,
    /// Configured interface names absent on the engine (gateway and reflection), sorted.
    pub missing: Vec<String>,
}

impl MdnsRuntime {
    pub fn off() -> MdnsRuntime {
        MdnsRuntime::default()
    }

    /// Validates `c` and looks its interfaces up now; an absent interface is skipped and listed
    /// in `missing`.
    pub fn build(c: Option<&proto::MdnsConfig>) -> Result<MdnsRuntime, String> {
        let Some(c) = c else {
            return Ok(MdnsRuntime::off());
        };
        validate(c)?;
        let mut missing = Vec::new();
        let mut resolve = |names: &[String]| -> Vec<IfaceTarget> {
            names
                .iter()
                .filter_map(|n| {
                    let found = iface::lookup(n);
                    if found.is_none() && !missing.contains(n) {
                        missing.push(n.clone());
                    }
                    found
                })
                .collect()
        };
        let mut gateway_key = String::new();
        let gateway = c.enabled.then(|| {
            let targets: Vec<Target> = resolve(&c.interfaces)
                .iter()
                .flat_map(Target::for_iface)
                .collect();
            let ms = match c.timeout_ms {
                0 => DEFAULT_TIMEOUT_MS,
                n => n,
            };
            gateway_key = format!("{:?}/{targets:?}/{ms}", c.interfaces);
            Arc::new(Gateway::new(targets, Duration::from_millis(u64::from(ms))))
        });
        let reflect = if c.reflect {
            resolve(&c.reflect_interfaces)
        } else {
            Vec::new()
        };
        let reflect_key = reflect
            .iter()
            .map(|i| format!("{}#{}/{:?}/{:?}", i.name, i.index, i.v4, i.v6_link_local))
            .collect::<Vec<_>>()
            .join(",");
        missing.sort();
        Ok(MdnsRuntime {
            gateway,
            gateway_key,
            reflect,
            reflect_key,
            missing,
        })
    }

    /// Appends `nexora_mdns_queries_total{result}` and `nexora_mdns_interface_missing{interface}`
    /// in OpenMetrics text form.
    pub fn render(&self, out: &mut String) {
        use std::fmt::Write as _;
        use std::sync::atomic::Ordering;
        let c = &gateway::COUNTERS;
        out.push_str("# HELP nexora_mdns_queries mDNS gateway queries by result.\n");
        out.push_str("# TYPE nexora_mdns_queries counter\n");
        for (result, v) in [
            ("answered", &c.answered),
            ("unanswered", &c.unanswered),
            ("dropped", &c.dropped),
        ] {
            let v = v.load(Ordering::Relaxed);
            let _ = writeln!(out, "nexora_mdns_queries_total{{result=\"{result}\"}} {v}");
        }
        out.push_str(
            "# HELP nexora_mdns_interface_missing 1 while a configured mDNS interface does not exist on the engine.\n",
        );
        out.push_str("# TYPE nexora_mdns_interface_missing gauge\n");
        // Names are validated to [A-Za-z0-9_.:@-]: nothing to escape.
        for name in &self.missing {
            let _ = writeln!(
                out,
                "nexora_mdns_interface_missing{{interface=\"{name}\"}} 1"
            );
        }
    }
}

/// The snapshot rules of the management plane's `ValidateMdns`.
pub fn validate(c: &proto::MdnsConfig) -> Result<(), String> {
    if c.timeout_ms != 0 && !TIMEOUT_MS.contains(&c.timeout_ms) {
        return Err(format!("timeout_ms {} outside 100..=5000", c.timeout_ms));
    }
    if c.enabled && c.interfaces.is_empty() {
        return Err("interfaces must name at least one interface when enabled".into());
    }
    if c.reflect && c.reflect_interfaces.len() < 2 {
        return Err("reflect_interfaces must name at least two interfaces when reflect".into());
    }
    for (field, names) in [
        ("interfaces", &c.interfaces),
        ("reflect_interfaces", &c.reflect_interfaces),
    ] {
        if names.len() > MAX_INTERFACES {
            return Err(format!("{field} lists more than {MAX_INTERFACES}"));
        }
        for (i, n) in names.iter().enumerate() {
            let valid = (1..=15).contains(&n.len())
                && n.bytes()
                    .all(|b| b.is_ascii_alphanumeric() || b"_.:@-".contains(&b));
            if !valid {
                return Err(format!(
                    "{field}: {n:?} must be 1-15 characters of A-Z, a-z, 0-9, '_', '.', ':', '@', '-'"
                ));
            }
            if names[..i].contains(n) {
                return Err(format!("{field}: {n:?} is listed twice"));
            }
        }
    }
    Ok(())
}

/// The running reflector, which survives snapshot swaps.
#[derive(Default)]
pub struct MdnsState {
    reflector: Mutex<Option<(String, Reflector)>>,
}

impl MdnsState {
    /// Stops and starts the reflector on `handle` (the control runtime) only when
    /// `rt.reflect_key` changed. A reflector that fails to start is retried on the next sync.
    pub fn sync(&self, rt: &MdnsRuntime, handle: &tokio::runtime::Handle) {
        let mut cur = self.reflector.lock().unwrap_or_else(|e| e.into_inner());
        let running = cur.as_ref().map(|(k, _)| k.as_str());
        if running == Some(rt.reflect_key.as_str())
            || (running.is_none() && rt.reflect_key.is_empty())
        {
            return;
        }
        if let Some((_, r)) = cur.take() {
            r.stop();
        }
        if rt.reflect_key.is_empty() {
            return;
        }
        match Reflector::start(handle, rt.reflect.clone()) {
            Ok(r) => *cur = Some((rt.reflect_key.clone(), r)),
            Err(e) => crate::log_warn!("mdns reflector not started: {e}"),
        }
    }
}

impl Drop for MdnsState {
    fn drop(&mut self) {
        if let Some((_, reflector)) = self
            .reflector
            .get_mut()
            .unwrap_or_else(|e| e.into_inner())
            .take()
        {
            reflector.stop();
        }
    }
}

/// True when the name is `local.` or under it. Allocation-free.
pub fn is_local_name(qname_wire_lower: &[u8]) -> bool {
    let mut off = 0usize;
    let mut last = None;
    while let Some(&len) = qname_wire_lower.get(off) {
        if len == 0 {
            return last.is_some_and(|start: usize| {
                qname_wire_lower.get(start + 1..off) == Some(b"local".as_slice())
            });
        }
        last = Some(off);
        off += 1 + usize::from(len);
    }
    false
}

/// The gateway result for `q`: the answer records (`Ok`, resolved on as any answer), or the
/// final answer: NXDOMAIN without SOA (never cached) when nothing answered, SERVFAIL when busy.
pub fn answer(q: &MissQuery<'_>, outcome: Outcome) -> Result<Vec<Record>, MissAnswer> {
    let (rcode, failed) = match outcome {
        Outcome::Answers(records) => return Ok(records),
        Outcome::NoAnswer => (ResponseCode::NXDomain, false),
        Outcome::Busy => (ResponseCode::ServFail, true),
    };
    Err(MissAnswer {
        wire: Bytes::from(build_response(q, rcode, &[], &[], false)),
        cacheable: false,
        failed,
        ede: None,
        drop: false,
        route: RouteTaken::Forward,
        security: SecurityTag::None,
        rpz_action: 0,
        rpz_zone: NO_RPZ_ZONE,
        rpz_identity: None,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::proto::MdnsConfig;

    #[test]
    fn validate_rules() {
        let ok = MdnsConfig {
            enabled: true,
            interfaces: vec!["lo".into()],
            timeout_ms: 0,
            reflect: false,
            reflect_interfaces: vec![],
        };
        assert!(validate(&ok).is_ok());
        for bad in [
            MdnsConfig {
                interfaces: vec![],
                ..ok.clone()
            },
            MdnsConfig {
                interfaces: vec!["eth0/1".into()],
                ..ok.clone()
            },
            MdnsConfig {
                interfaces: vec!["a-very-long-interface".into()],
                ..ok.clone()
            },
            MdnsConfig {
                timeout_ms: 99,
                ..ok.clone()
            },
            MdnsConfig {
                timeout_ms: 5001,
                ..ok.clone()
            },
            MdnsConfig {
                reflect: true,
                reflect_interfaces: vec!["lo".into()],
                ..ok.clone()
            },
        ] {
            assert!(validate(&bad).is_err(), "{bad:?}");
        }
        let rt = MdnsRuntime::build(Some(&MdnsConfig {
            interfaces: vec!["lo".into(), "nxnope0".into()],
            ..ok
        }))
        .unwrap();
        assert!(rt.gateway.is_some());
        assert_eq!(rt.missing, vec!["nxnope0".to_string()]);
        assert!(MdnsRuntime::build(None).unwrap().gateway.is_none());
        assert!(is_local_name(b"\x07printer\x05local\x00") && is_local_name(b"\x05local\x00"));
        assert!(!is_local_name(b"\x05local\x04test\x00") && !is_local_name(b"\x08notlocal\x00"));
    }
}
