<!-- operations: Key storage -->

# Authoritative zones

Engines serve authoritative zones: primary zones edited in Nexora or through dynamic updates, and
secondary zones transferred from their primaries. Queries for hosted names are answered from the
zone, never forwarded.

## Records

Records of a primary zone are edited on the zone's Records tab. Every change publishes a new
configuration version; the SOA serial is managed for you. Zone edits do not clear the resolver cache.

## Transfers

Outgoing transfers (AXFR and IXFR) are allowed only from the zone's allowed transfer networks, and
optionally only when signed with the zone's transfer TSIG key. Notify targets are told when the zone
changes. A secondary zone transfers from its primaries and accepts NOTIFY only from them.

## TSIG

TSIG keys are shared secrets that sign zone transfers, NOTIFY messages and dynamic updates. Secrets are
stored encrypted and shown only once, when the key is created. TSIG keys need key storage: without a
key-encryption key file or a PKCS#11 token the management plane refuses them. Keep the key-encryption
key backed up separately from the database.

## Dynamic updates

RFC 2136 dynamic updates change records of a primary zone. Only updates signed with one of the zone's
update TSIG keys are accepted; with no key selected, updates are refused.

## Zone files

A primary zone can be imported from a zone file in standard master-file format and exported as one,
for backups or moving a zone between servers.
