<!-- operations: Key storage -->

# Authoritative zones

Nexora serves authoritative zones: primary zones edited here or through dynamic updates, and
secondary zones transferred from their primaries. Queries for hosted names are answered from the
zone, never forwarded, and are checked against the zone's query access (see Access control). A zone
applies to every engine group or to one, chosen with its Engine group field.

## Records

A primary zone starts with its SOA (primary name server and responsible mailbox) and apex NS
records. Records are edited on the zone's Records tab. Owner names are written relative to the zone
(`@` for the apex, `www` for `www.example.com.`) or absolute with a trailing dot, and the data uses
zone-file presentation format; the editor shows the format for each record type and checks the value
before saving, and the server checks it again. New records start with the zone's default TTL (3600
seconds unless you set another).

Every change increases the SOA serial, publishes a new configuration version and sends NOTIFY to
the zone's notify targets. When two people edit the same record, the second save is refused and the
editor shows the current value so nothing is overwritten silently. Records of a secondary zone come
from its primaries and cannot be edited.

## Transfers

Outgoing transfers (AXFR, and IXFR when the history allows it) are allowed only from the zone's
allowed transfer networks; an empty list refuses every transfer. Selecting a transfer TSIG key also
requires every request to be signed with that key. Notify targets are sent a NOTIFY whenever the zone
changes, so secondaries fetch the change at once instead of waiting for their refresh timer.

A secondary zone checks the SOA serial of its primaries on the zone's SOA refresh timer, and at once
when a primary sends NOTIFY; NOTIFY from any other address is ignored. It transfers with IXFR when it
can and falls back to AXFR. The Transfers tab shows the last and next refresh, the last error and when
the zone expires, and Refresh now checks the primaries immediately. Query access and transfer access
are separate lists.

## TSIG

TSIG keys are shared secrets that sign zone transfers, NOTIFY messages and dynamic updates. A key
has a name, an HMAC algorithm (hmac-sha256 by default, hmac-sha384 or hmac-sha512) and a base64
secret of 16 to 64 bytes; leave the secret empty to generate one. The peer must configure the same
name, algorithm and secret. The secret is shown once, when the key is created, together with a BIND
configuration snippet; it cannot be retrieved later.

TSIG secrets and DNSSEC private keys are never stored in plaintext, so they need key storage: a
key-encryption key file (`NEXORA_KEK_FILE`) or a PKCS#11 token. Without either, Nexora refuses TSIG
keys, RPZ TSIG secrets and DNSSEC signing. Every management instance needs the same key-encryption
key, and losing it makes the sealed secrets unreadable, so back it up separately from the database.
A key still referenced by a zone cannot be deleted.

## Dynamic updates

RFC 2136 dynamic updates change the records of a primary zone. An update is accepted only when it is
signed with one of the zone's update TSIG keys; with no key selected, every update is refused. The
allowed update sources narrow this further to the listed networks; empty allows any source, but the
TSIG signature is still required. Accepted updates increase the serial and notify secondaries like an
edit in the GUI.

## Zone files

A primary zone can be imported from a zone file in BIND master-file format, pasted or opened from a
file. Importing replaces every record and the SOA fields of the zone in one change, and adopts the
file's SOA serial when it is newer than the zone's. `$INCLUDE` and `$GENERATE` are refused, and a file
may hold at most 1,000,000 records. When lines are invalid, nothing is imported and the problems are
listed with their line numbers.

Export downloads the zone as a BIND master file with its SOA and every record, for backups or for
moving a zone to another server.
