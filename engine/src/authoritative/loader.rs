//! Builds the runtime's [`AuthSet`] from `ConfigSnapshot.auth_zones`: unchanged zones are reused,
//! zones the previous runtime holds advance by their listed deltas, others load from their image.
//! Runs inside `Runtime::build` (control apply, off the query path).

use super::name::from_ascii;
use super::nzf::{self, Kind, NzfError, Parsed};
use super::set::AuthSet;
use super::zone::{DeltaRecords, OwnedRecord, Zone, ZoneError};
use crate::proto;
use crate::snapshot::{BlobSource, SnapshotError, is_sha256_hex};
use std::collections::HashSet;
use std::net::SocketAddr;
use std::sync::Arc;

/// Decompressed NZF1 blob ceiling.
const MAX_ZONE_BLOB: usize = 1 << 30;

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct LoadCounts {
    pub full: u64,
    pub delta: u64,
    pub reused: u64,
}

pub struct Loaded {
    pub set: AuthSet,
    pub counts: LoadCounts,
    /// Lowercase wire origin and serial of zones that are new or whose serial changed.
    pub changed: Vec<(Box<[u8]>, u32)>,
}

#[derive(Debug, thiserror::Error)]
pub enum LoadError {
    #[error("zone blob: {0}")]
    Blob(SnapshotError),
    #[error("zone {zone}: {err}")]
    Nzf { zone: String, err: NzfError },
    #[error("zone {zone}: {err}")]
    Zone { zone: String, err: ZoneError },
    #[error("{0}")]
    Chain(String),
}

/// Structural checks of every hosted zone; a failure rejects the whole snapshot.
pub fn validate(zones: &[proto::AuthZone]) -> Result<(), String> {
    let mut names = HashSet::new();
    for z in zones {
        let n = &z.name;
        if !n.ends_with('.') || n == "." || n.to_ascii_lowercase() != *n || from_ascii(n).is_none()
        {
            return Err(format!(
                "auth zone {n:?}: name must be absolute, lowercase and not the root"
            ));
        }
        if !names.insert(n.as_str()) {
            return Err(format!("auth zone {n} listed twice"));
        }
        if !matches!(
            proto::AuthZoneKind::try_from(z.kind),
            Ok(proto::AuthZoneKind::Primary | proto::AuthZoneKind::Secondary)
        ) {
            return Err(format!("auth zone {n}: kind unspecified"));
        }
        match &z.image {
            Some(b) if is_sha256_hex(&b.sha256) => {}
            _ => return Err(format!("auth zone {n}: image blob missing or not 64 hex")),
        }
        for d in &z.deltas {
            if !d.blob.as_ref().is_some_and(|b| is_sha256_hex(&b.sha256)) {
                return Err(format!(
                    "auth zone {n}: delta {}->{} blob missing or not 64 hex",
                    d.from_serial, d.to_serial
                ));
            }
        }
        if z.deltas
            .windows(2)
            .any(|w| w[0].to_serial != w[1].from_serial)
        {
            return Err(format!("auth zone {n}: delta chain is not contiguous"));
        }
        if z.deltas.last().is_some_and(|d| d.to_serial != z.serial) {
            return Err(format!("auth zone {n}: last delta does not end at serial"));
        }
        let offset = z.image_delta_offset as usize;
        if offset > z.deltas.len() {
            return Err(format!("auth zone {n}: image_delta_offset out of range"));
        }
        let on_image = match z.deltas.get(offset) {
            Some(d) => d.from_serial == z.image_serial,
            None => z.image_serial == z.serial,
        };
        if !on_image {
            return Err(format!(
                "auth zone {n}: deltas after the image do not start at image_serial"
            ));
        }
        let transfer = z.transfer.as_ref().map_or(&[][..], |t| &t.allow_cidrs[..]);
        if let Some(c) = transfer.iter().find(|c| c.parse::<ipnet::IpNet>().is_err()) {
            return Err(format!("auth zone {n}: transfer CIDR {c:?} invalid"));
        }
        let addrs = z.notify.iter().map(|t| &t.address).chain(&z.primaries);
        for a in addrs {
            if a.parse::<SocketAddr>().is_err() {
                return Err(format!("auth zone {n}: address {a:?} must be ip:port"));
            }
        }
    }
    Ok(())
}

/// Builds the set for `zones` (already validated), reusing what `prev` holds.
pub fn load(
    prev: &AuthSet,
    zones: &[proto::AuthZone],
    blobs: &dyn BlobSource,
) -> Result<Loaded, LoadError> {
    let mut counts = LoadCounts::default();
    let mut changed = Vec::new();
    let mut out = Vec::with_capacity(zones.len());
    for z in zones {
        let origin: Box<[u8]> = from_ascii(&z.name)
            .ok_or_else(|| LoadError::Chain(format!("auth zone {:?}: bad name", z.name)))?
            .into();
        let old = prev.get(&origin);
        if old.is_none_or(|o| o.serial() != z.serial) {
            changed.push((origin.clone(), z.serial));
        }
        let zone = load_zone(z, &origin, old, blobs, &mut counts)?;
        out.push(zone);
    }
    let set = AuthSet::from_zones(out).map_err(LoadError::Chain)?;
    Ok(Loaded {
        set,
        counts,
        changed,
    })
}

fn same_history(old: &Zone, z: &proto::AuthZone) -> bool {
    old.deltas.len() == z.deltas.len()
        && old
            .deltas
            .iter()
            .zip(&z.deltas)
            .all(|(a, b)| a.from_serial == b.from_serial && a.to_serial == b.to_serial)
}

fn load_zone(
    z: &proto::AuthZone,
    origin: &[u8],
    old: Option<&Arc<Zone>>,
    blobs: &dyn BlobSource,
    counts: &mut LoadCounts,
) -> Result<Arc<Zone>, LoadError> {
    let name = &z.name;
    if let Some(old) = old
        && old.serial() == z.serial
        && same_history(old, z)
    {
        counts.reused += 1;
        if old.expired == z.expired {
            return Ok(old.clone());
        }
        let mut zone = (**old).clone();
        zone.expired = z.expired;
        return Ok(Arc::new(zone));
    }

    // Deltas parsed while loading, kept for the IXFR history.
    let mut parsed_deltas: Vec<Arc<DeltaRecords>> = Vec::new();
    let mut zone = None;
    if let Some(old) = old {
        if old.serial() == z.serial {
            // Same data, different history window.
            counts.reused += 1;
            zone = Some((**old).clone());
        } else if let Some(i) = z.deltas.iter().position(|d| d.from_serial == old.serial()) {
            let mut staged = Vec::new();
            if let Ok(Some(applied)) = apply_deltas(old, &z.deltas[i..], name, blobs, &mut staged) {
                counts.delta += 1;
                parsed_deltas = staged;
                zone = Some(applied);
            }
        }
    }
    let mut zone = match zone {
        Some(zone) => zone,
        None => {
            let image = z
                .image
                .as_ref()
                .ok_or_else(|| LoadError::Chain(format!("auth zone {name}: no image")))?;
            let raw = read(blobs, image, name)?;
            let p = parse(&raw, name)?;
            let base = Zone::from_image(&p).map_err(|err| zone_err(name, err))?;
            if base.origin() != origin {
                return Err(zone_err(name, ZoneError::OriginMismatch));
            }
            if base.serial() != z.image_serial {
                return Err(LoadError::Chain(format!(
                    "auth zone {name}: image serial {} but image_serial {}",
                    base.serial(),
                    z.image_serial
                )));
            }
            let rest = &z.deltas[(z.image_delta_offset as usize).min(z.deltas.len())..];
            let zone = apply_deltas(&base, rest, name, blobs, &mut parsed_deltas)?;
            counts.full += 1;
            zone.unwrap_or(base)
        }
    };
    if zone.serial() != z.serial {
        return Err(LoadError::Chain(format!(
            "auth zone {name}: loaded serial {} but snapshot serial {}",
            zone.serial(),
            z.serial
        )));
    }

    let mut history = Vec::with_capacity(z.deltas.len());
    for d in &z.deltas {
        let known = parsed_deltas
            .iter()
            .chain(old.map_or(&[][..], |o| &o.deltas[..]))
            .find(|r| r.from_serial == d.from_serial && r.to_serial == d.to_serial);
        let rec = match known {
            Some(r) => r.clone(),
            None => {
                let raw = read_delta(blobs, d, name)?;
                let p = parse(&raw, name)?;
                check_delta_header(&p, d, name)?;
                Arc::new(owned_delta(&p))
            }
        };
        history.push(rec);
    }
    zone.deltas = history;
    zone.expired = z.expired;
    Ok(Arc::new(zone))
}

/// Applies `deltas` in order to `base` (`None` when there are none); every parsed delta is pushed
/// to `staged`.
fn apply_deltas(
    base: &Zone,
    deltas: &[proto::ZoneDelta],
    name: &str,
    blobs: &dyn BlobSource,
    staged: &mut Vec<Arc<DeltaRecords>>,
) -> Result<Option<Zone>, LoadError> {
    let mut cur: Option<Zone> = None;
    for d in deltas {
        let raw = read_delta(blobs, d, name)?;
        let p = parse(&raw, name)?;
        check_delta_header(&p, d, name)?;
        let next = cur
            .as_ref()
            .unwrap_or(base)
            .apply(&p)
            .map_err(|err| zone_err(name, err))?;
        staged.push(Arc::new(owned_delta(&p)));
        cur = Some(next);
    }
    Ok(cur)
}

fn read(blobs: &dyn BlobSource, r: &proto::BlobRef, name: &str) -> Result<Vec<u8>, LoadError> {
    let compressed = blobs.read(r).map_err(LoadError::Blob)?;
    nzf::decompress(&compressed, MAX_ZONE_BLOB).map_err(|err| LoadError::Nzf {
        zone: name.to_owned(),
        err,
    })
}

fn read_delta(
    blobs: &dyn BlobSource,
    d: &proto::ZoneDelta,
    name: &str,
) -> Result<Vec<u8>, LoadError> {
    let blob = d.blob.as_ref().ok_or_else(|| {
        LoadError::Chain(format!(
            "auth zone {name}: delta {}->{} has no blob",
            d.from_serial, d.to_serial
        ))
    })?;
    read(blobs, blob, name)
}

fn parse<'a>(raw: &'a [u8], name: &str) -> Result<Parsed<'a>, LoadError> {
    nzf::parse(raw).map_err(|err| LoadError::Nzf {
        zone: name.to_owned(),
        err,
    })
}

fn check_delta_header(p: &Parsed<'_>, d: &proto::ZoneDelta, name: &str) -> Result<(), LoadError> {
    if p.kind != Kind::Delta || p.from_serial != d.from_serial || p.serial != d.to_serial {
        return Err(LoadError::Chain(format!(
            "auth zone {name}: delta blob does not match {}->{}",
            d.from_serial, d.to_serial
        )));
    }
    Ok(())
}

fn zone_err(name: &str, err: ZoneError) -> LoadError {
    LoadError::Zone {
        zone: name.to_owned(),
        err,
    }
}

fn owned_delta(p: &Parsed<'_>) -> DeltaRecords {
    let own = |rs: &[nzf::RecordRef<'_>]| {
        rs.iter()
            .map(|r| OwnedRecord {
                owner: r.owner.into(),
                rtype: r.rtype,
                ttl: r.ttl,
                rdata: r.rdata.into(),
            })
            .collect()
    };
    DeltaRecords {
        from_serial: p.from_serial,
        to_serial: p.serial,
        deleted: own(&p.a),
        added: own(&p.b),
    }
}
