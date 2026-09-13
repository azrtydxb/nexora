//! DNSSEC validation (RFC 4033-4035, 5155, 6840, 8198, 9276) and RFC 5011 trust anchors.

pub mod anchors;
pub mod denial;
pub mod forward;
pub mod nsec_cache;
pub mod validator;
pub mod verify;

#[cfg(test)]
mod anchors_tests;
#[cfg(test)]
mod primitives_tests;
#[cfg(test)]
pub(crate) mod testsign;
#[cfg(test)]
mod validator_tests;

use crate::proto;
use hickory_proto::rr::Name;

/// An RFC 8914 Extended DNS Error. Only the INFO-CODE goes on the wire; the text is for tests and
/// logs.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Ede {
    pub code: u16,
    pub text: String,
}

impl Ede {
    pub fn new(code: u16, text: impl Into<String>) -> Self {
        Ede {
            code,
            text: text.into(),
        }
    }
}

/// Per-snapshot DNSSEC settings.
#[derive(Default, Debug, Clone)]
pub struct DnssecRuntime {
    pub validation: bool,
    /// Validate answers of the global forward route (`ConfigSnapshot.dnssec_validate_forwarded`).
    pub validate_forwarded: bool,
    /// Negative trust anchors: lowercase domain and expiry (unix seconds).
    pub ntas: Vec<(Name, i64)>,
    pub anchors: Vec<proto::TrustAnchor>,
    pub rfc5011: bool,
}

impl DnssecRuntime {
    /// Built from a snapshot that `snapshot::validate` accepted.
    pub fn build(s: &proto::ConfigSnapshot) -> Result<Self, String> {
        let Some(d) = &s.dnssec else {
            return Ok(DnssecRuntime::default());
        };
        let ntas = d
            .negative_trust_anchors
            .iter()
            .map(|n| {
                Name::from_ascii(&n.domain)
                    .map(|name| (name.to_lowercase(), n.expires_unix))
                    .map_err(|e| format!("dnssec.negative_trust_anchors: {}: {e}", n.domain))
            })
            .collect::<Result<_, _>>()?;
        Ok(DnssecRuntime {
            validation: d.validation,
            validate_forwarded: d.validation && s.dnssec_validate_forwarded,
            ntas,
            anchors: d.trust_anchors.clone(),
            rfc5011: d.rfc5011,
        })
    }
}
