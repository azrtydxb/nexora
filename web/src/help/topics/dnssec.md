<!-- operations: Key storage -->

# DNSSEC

Nexora validates signed answers when it resolves names, and signs the primary zones it hosts.

## Validation

Validation checks the signatures of answers found by recursion and by forward zones that have
validation turned on. An answer whose signatures do not verify is bogus and the client gets SERVFAIL.
The DNSSEC page shows the validation outcomes of each engine since it started.

## Trust anchors

Validation chains end at a trust anchor. The IANA root keys ship built in, and engines follow root
key rollovers automatically. Add a zone's DS record as a trust anchor to validate a private signed
hierarchy that is not reachable from the root.

## Negative trust anchors

A negative trust anchor serves a domain's answers without validation until it expires. Use it for a
zone with broken signatures that you cannot fix, and give it a reason and a short lifetime.

## Signing

Online signing signs a primary zone whenever it changes (algorithm 13 by default, or 8; NSEC or NSEC3
without extra iterations or salt). Signatures are valid for 14 days and renewed when less than 7 days
remain. Signing needs key storage: without a key-encryption key file or a PKCS#11 token the management
plane refuses DNSSEC signing, TSIG keys and RPZ TSIG secrets. Private keys are never stored in
plaintext. Losing the key-encryption key makes sealed keys unreadable, so back it up separately from
the database.

## Rollovers

A ZSK rollover pre-publishes the new key before it signs with it. A KSK rollover signs with both KSKs
until the parent publishes the new DS record. Publish the DS records shown for the zone at the parent
(your registrar or the parent zone's operator) and confirm that the parent serves them.
