<!-- operations: Key storage -->

# DNSSEC

Nexora validates signed answers when it resolves names, and signs the primary zones it hosts.

## Validation

Validation (on by default) checks the signatures of answers found by recursion and by forward zones
that have validation turned on. **Validate forwarded answers** (also on by default) extends it to
answers from the upstream forwarders in forward mode, up to the root trust anchor. An answer whose
signatures do not verify is bogus and the client gets SERVFAIL.

The DNSSEC page counts each resolver's outcomes since it started: secure answers verified up to a
trust anchor, insecure answers come from provably unsigned zones, bogus answers failed verification,
and indeterminate answers could not be checked. A rising bogus count points to a broken zone or an
upstream that strips signatures.

## Trust anchors

Validation chains end at a trust anchor. The IANA root keys are built in. With automated trust anchor
updates (RFC 5011, on by default), Nexora follows root key rollovers: a new root key is trusted after
a 30-day hold-down, and a revoked key is dropped after 30 days. A red banner on the DNSSEC page means
this refresh is failing, usually because the servers cannot reach the root servers.

Add a zone's DS record (key tag, algorithm, digest type and hex digest) as a trust anchor to validate a
private signed hierarchy that is not reachable from the root.

## Negative trust anchors

A negative trust anchor serves the answers of a domain and its subdomains without validation until it
expires, at most 30 days ahead. Use it for a zone with broken signatures that you cannot fix, and give
it a reason and a short lifetime (1 day by default).

## Signing

Online signing signs a primary zone whenever it changes (algorithm 13 by default, or 8; NSEC or NSEC3
without extra iterations or salt). Signatures are valid for 14 days and renewed when less than 7 days
remain. Signing needs key storage: without a key-encryption key file or a PKCS#11 token, Nexora
refuses DNSSEC signing, TSIG keys and RPZ TSIG secrets. Private keys are never stored in plaintext.
Losing the key-encryption key makes sealed keys unreadable, so back it up separately from the
database.

## Rollovers

A ZSK rollover pre-publishes the new key before it signs with it. A KSK rollover signs with both KSKs
until the parent publishes the new DS record. Publish the DS records shown for the zone at the parent
(your registrar or the parent zone's operator) and confirm that the parent serves them.
