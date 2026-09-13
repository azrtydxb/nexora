//! The hosted zones of one runtime, found by longest suffix of the query name.

use super::T_DS;
use super::zone::Zone;
use rustc_hash::FxHashMap;
use std::sync::Arc;

#[derive(Default)]
pub struct AuthSet {
    /// Keyed by lowercase wire origin.
    zones: FxHashMap<Box<[u8]>, Arc<Zone>>,
}

impl AuthSet {
    pub fn empty() -> AuthSet {
        AuthSet::default()
    }

    /// Fails on two zones with the same origin.
    pub fn from_zones(zones: Vec<Arc<Zone>>) -> Result<AuthSet, String> {
        let mut map = FxHashMap::default();
        for z in zones {
            let origin: Box<[u8]> = z.origin().into();
            if let Some(dup) = map.insert(origin, z) {
                return Err(format!(
                    "zone {} is listed twice",
                    String::from_utf8_lossy(dup.origin()).escape_default()
                ));
            }
        }
        Ok(AuthSet { zones: map })
    }

    pub fn is_empty(&self) -> bool {
        self.zones.is_empty()
    }

    pub fn get(&self, lower_origin: &[u8]) -> Option<&Arc<Zone>> {
        self.zones.get(lower_origin)
    }

    /// The zone with the longest origin that `lower` (a lowercase wire name) equals or lies below.
    pub fn find(&self, lower: &[u8]) -> Option<&Arc<Zone>> {
        let mut i = 0usize;
        while i < lower.len() {
            if let Some(z) = self.zones.get(&lower[i..]) {
                return Some(z);
            }
            let l = lower[i] as usize;
            if l == 0 {
                return None;
            }
            i += l + 1;
        }
        None
    }

    /// [`find`](Self::find), except that DS at a hosted zone's apex belongs to the hosted parent.
    pub fn find_for_query(&self, lower: &[u8], qtype: u16) -> Option<&Arc<Zone>> {
        let z = self.find(lower)?;
        if qtype == T_DS && z.origin() == lower && lower.len() > 1 {
            let parent = &lower[lower[0] as usize + 1..];
            if let Some(p) = self.find(parent) {
                return Some(p);
            }
        }
        Some(z)
    }

    pub fn zones(&self) -> impl Iterator<Item = &Arc<Zone>> {
        self.zones.values()
    }
}
