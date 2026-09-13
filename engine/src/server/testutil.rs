//! Test doubles shared by the stream, DoT, DoH and DoQ listener tests.

use super::{Answerer, ClientInfo};
use hickory_proto::op::{Message, MessageType, OpCode, Query};
use hickory_proto::rr::{Name, RData, Record, RecordType, rdata::A};
use std::net::{IpAddr, Ipv4Addr};

/// Answers every query with `<qname> 300 A 192.0.2.1` and, for IPv4 clients,
/// `<qname> 60 A <client ip>` so tests can observe the client address the transport resolved.
pub struct EchoAnswerer;

impl Answerer for EchoAnswerer {
    async fn answer(&self, client: ClientInfo, query: &[u8], out: &mut Vec<u8>) {
        out.clear();
        if query.len() < 12 {
            return;
        }
        let q = Message::from_vec(query).expect("test query parses");
        let mut m = Message::response(q.metadata.id, OpCode::Query);
        m.metadata.recursion_desired = q.metadata.recursion_desired;
        m.metadata.recursion_available = true;
        m.queries = q.queries.clone();
        let name = q.queries[0].name().clone();
        m.answers.push(Record::from_rdata(
            name.clone(),
            300,
            RData::A(A(Ipv4Addr::new(192, 0, 2, 1))),
        ));
        if let IpAddr::V4(v4) = client.addr.ip() {
            m.answers
                .push(Record::from_rdata(name, 60, RData::A(A(v4))));
        }
        out.extend_from_slice(&m.to_vec().expect("encode"));
    }
}

pub fn test_query(id: u16, name: &str) -> Vec<u8> {
    let mut m = Message::new(id, MessageType::Query, OpCode::Query);
    m.metadata.recursion_desired = true;
    m.queries
        .push(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
    m.to_vec().unwrap()
}
