//! RPZ action -> response synthesis.

use super::index::RpzZoneIndex;
use super::parse::{CnameTarget, RpzAction};
use hickory_proto::op::{Message, OpCode, Query, ResponseCode};
use hickory_proto::rr::{Name, RData, Record, RecordType, rdata::CNAME};

/// Extended DNS Error (RFC 8914). Only `code` goes on the wire; `text` is for logs and tests.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Ede {
    pub code: u16,
    pub text: String,
}

/// EDE 15 "Blocked".
pub const EDE_BLOCKED: u16 = 15;
/// EDE 4 "Forged Answer".
pub const EDE_FORGED: u16 = 4;

#[derive(Debug)]
pub enum PolicyOutcome {
    /// A complete response (ID 0, lowercase question, no OPT).
    Respond {
        wire: Vec<u8>,
        ede: Ede,
    },
    Drop,
    /// NOERROR with TC=1 and no records: the client retries over TCP.
    Truncate {
        wire: Vec<u8>,
    },
    Passthru,
    /// Local data CNAME: answer `cname` and resolve `target` normally.
    ChaseCname {
        cname: Box<Record>,
        target: Name,
    },
}

pub fn apply_action(
    qname: &Name,
    qtype: RecordType,
    over_tcp: bool,
    zone: &RpzZoneIndex,
    action: &RpzAction,
) -> PolicyOutcome {
    let qname = qname.to_lowercase();
    let ede = |code| Ede {
        code,
        text: format!("rpz {}", zone.origin),
    };
    match action {
        RpzAction::Passthru => PolicyOutcome::Passthru,
        RpzAction::Drop => PolicyOutcome::Drop,
        RpzAction::TcpOnly if over_tcp => PolicyOutcome::Passthru,
        RpzAction::TcpOnly => {
            let mut m = response(&qname, qtype, ResponseCode::NoError);
            m.metadata.truncation = true;
            PolicyOutcome::Truncate { wire: encode(&m) }
        }
        RpzAction::Nxdomain | RpzAction::Nodata => {
            let rcode = match action {
                RpzAction::Nxdomain => ResponseCode::NXDomain,
                _ => ResponseCode::NoError,
            };
            let mut m = response(&qname, qtype, rcode);
            m.authorities.push(negative_soa(zone));
            PolicyOutcome::Respond {
                wire: encode(&m),
                ede: ede(EDE_BLOCKED),
            }
        }
        RpzAction::LocalData(data) => {
            if let Some((ttl, target)) = &data.cname {
                let target = match target {
                    CnameTarget::Name(n) => n.clone(),
                    CnameTarget::WildcardSuffix(suffix) => {
                        match qname.clone().append_name(suffix) {
                            Ok(n) => n,
                            // Name too long: nothing sensible to synthesise.
                            Err(_) => {
                                let mut m = response(&qname, qtype, ResponseCode::NXDomain);
                                m.authorities.push(negative_soa(zone));
                                return PolicyOutcome::Respond {
                                    wire: encode(&m),
                                    ede: ede(EDE_FORGED),
                                };
                            }
                        }
                    }
                };
                let cname = Box::new(Record::from_rdata(
                    qname,
                    *ttl,
                    RData::CNAME(CNAME(target.clone())),
                ));
                return PolicyOutcome::ChaseCname { cname, target };
            }
            let mut m = response(&qname, qtype, ResponseCode::NoError);
            m.answers = data
                .records
                .iter()
                .filter(|(t, _, _)| qtype == RecordType::ANY || *t == qtype)
                .map(|(_, ttl, rdata)| Record::from_rdata(qname.clone(), *ttl, rdata.clone()))
                .collect();
            if m.answers.is_empty() {
                m.authorities.push(negative_soa(zone));
            }
            PolicyOutcome::Respond {
                wire: encode(&m),
                ede: ede(EDE_FORGED),
            }
        }
    }
}

fn response(qname: &Name, qtype: RecordType, rcode: ResponseCode) -> Message {
    let mut m = Message::response(0, OpCode::Query);
    m.metadata.recursion_available = true;
    m.metadata.authoritative = false;
    m.metadata.authentic_data = false;
    m.metadata.response_code = rcode;
    m.queries.push(Query::query(qname.clone(), qtype));
    m
}

/// The zone SOA with TTL = min(SOA TTL, SOA MINIMUM) (RFC 2308).
fn negative_soa(zone: &RpzZoneIndex) -> Record {
    let mut soa = zone.soa.clone();
    if let RData::SOA(s) = &soa.data {
        soa.ttl = soa.ttl.min(s.minimum);
    }
    soa
}

fn encode(m: &Message) -> Vec<u8> {
    // Every part was decoded from or built with valid names and rdata.
    m.to_vec().expect("RPZ response encodes")
}
