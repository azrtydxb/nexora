//! Validation through a forwarder (`ConfigSnapshot.dnssec_validate_forwarded`): the engine sends
//! its own query (random ID, RD=1, EDNS 1232, DO=1, CD=1) so the forwarder returns unvalidated
//! data with RRSIGs, and fetches the chain of trust (DS/DNSKEY) through the same forwarder.

use super::validator::{FetchError, FetchedSet};
use crate::recursor::EDNS_BUFFER;
use bytes::Bytes;
use hickory_proto::op::{Edns, Message, MessageType, OpCode, Query, ResponseCode};
use hickory_proto::rr::{Name, RecordType};
use rand::Rng;
use std::future::Future;

/// A query for `qname`/`qtype` that asks the forwarder for DNSSEC records without validating.
pub fn validation_query(qname: &Name, qtype: RecordType) -> Vec<u8> {
    let mut msg = Message::new(
        rand::rng().next_u32() as u16,
        MessageType::Query,
        OpCode::Query,
    );
    msg.metadata.recursion_desired = true;
    msg.metadata.checking_disabled = true;
    msg.queries.push(Query::query(qname.clone(), qtype));
    let mut edns = Edns::new();
    edns.set_max_payload(EDNS_BUFFER);
    edns.set_dnssec_ok(true);
    msg.edns = Some(edns);
    // a name that decoded from the wire always encodes
    msg.to_vec().unwrap_or_default()
}

/// Decodes a forwarder's reply to [`validation_query`]. Replies that are undecodable, for another
/// question, or with an rcode other than NOERROR/NXDOMAIN count as unreachable.
pub fn fetched_set(
    reply: &[u8],
    qname: &Name,
    qtype: RecordType,
) -> Result<FetchedSet, FetchError> {
    let m = Message::from_vec(reply).map_err(|_| FetchError::Unreachable)?;
    let same_question = m.metadata.message_type == MessageType::Response
        && m.queries.len() == 1
        && m.queries[0].name() == qname
        && m.queries[0].query_type() == qtype;
    if !same_question
        || !matches!(
            m.metadata.response_code,
            ResponseCode::NoError | ResponseCode::NXDomain
        )
    {
        return Err(FetchError::Unreachable);
    }
    Ok(FetchedSet {
        rcode: m.metadata.response_code,
        answers: m.answers,
        authorities: m.authorities,
    })
}

/// Fetches `name`/`rtype` by handing a [`validation_query`] to `send` (e.g. the global upstreams).
pub async fn fetch_via<F, Fut>(
    send: F,
    name: &Name,
    rtype: RecordType,
) -> Result<FetchedSet, FetchError>
where
    F: FnOnce(Vec<u8>) -> Fut,
    Fut: Future<Output = Result<Bytes, String>>,
{
    let reply = send(validation_query(name, rtype))
        .await
        .map_err(|_| FetchError::Unreachable)?;
    fetched_set(&reply, name, rtype)
}
