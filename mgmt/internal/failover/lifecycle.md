# FG-02 / FG-06 package integration contract

This package implements desired lifecycle, retained reservations, generation-fenced
eligibility publication and a withdrawal-verifier boundary. It does not grant VIP
ownership, implement a kernel fence, prove withdrawal, or establish production HA.
No entrypoint, control package, API, OpenAPI, UI or deployment is changed here.

## API consumer changes

`Group.Lifecycle` is persisted and returned by Create/Get/List/Update. Values are
`active`, `draining`, `withdrawn`, `deleting`, `deleted`. Create always assigns
`active`, generation 1, a fresh ID and reservations, ignoring caller lifecycle and
ID fields. Active means desired participation, never healthy or advertised.

Update keeps the IP immutable. Rename and swapping the same two members preserve
lifecycle but increment generation. Changing identities reserves the new members,
retains every removed member, and enters whole-pair `draining`. Updates during
`draining`/`deleting`/`deleted` reject with ErrInvalid; they cannot cancel a drain.
Generation mismatch remains store.ErrConflict. This deliberately conservative
whole-pair drain can interrupt availability; orchestration must not treat this as
a rolling partner replacement primitive.

BeginDrain and RequestDeletion increment generation with CAS. Neither releases
anything. RequestDeletion is pending work, not HTTP DELETE success implying IP
reuse. Resume only accepts `withdrawn`, increments generation and requires a fresh
controller registration before observations can publish again. Current desired
members remain reserved during withdrawal; only removed members are released.

CompleteWithdrawal alone may finish a drain/deletion, with the verifier boundary
below. Deleted groups remain immutable tombstones, retain member/history/evidence
rows and can be fetched by ID. List excludes tombstones. Live names, IPs and engine
reservations may be reused only after completed verified deletion; the replacement
has a different group UUID. No generation/owner token from its predecessor applies.
Physical group deletion and reservation reassignment are rejected by DB triggers.
Downgrade 1306 refuses populated lifecycle state; unused test installations can
roll back. No migration history was renumbered or removed.

All desired mutations take the caller's existing transaction. Keep existing admin
authorization and WriteAudit in that transaction; any error must roll back. No
method silently commits an API mutation. CompleteWithdrawal requires a one-attempt
transaction because an external verifier may have a durable effect; do not wrap it
in Store.InTx's automatic retry loop. Return results only after confirmed commit.
An ambiguous commit needs inspection; it is not permission to reuse anything.

## Explicit trusted-source boundary

Migration 1306 authorizes **zero** sources. There is no permissive default
callback or configurable `Fenced=true` flag. The original PostgreSQL LOGIN
`session_user`, its actual role OID and an authorization incarnation must match
`failover_sources`. SET ROLE does not change that identity. Dropping/recreating a
same-name role changes the OID. Every update of identity/purpose/incarnation rotates
the authorization UUID; delete/regrant also creates a new UUID. SELECT FOR SHARE
serializes publication with authorization removal. Authority tables are schema-qualified;
trusted operations pin transaction-local search_path to pg_catalog, public, pg_temp
so temporary tables cannot shadow shared fleet inventory helpers. This package
assumes the repository's public database schema. Role/allowlist provisioning is
out-of-band operator authority, never CRUD input.

Use three distinct non-superuser LOGINs and pools: `controller`, `collector`,
`withdrawal`. A LOGIN has exactly one purpose. API/engine/reader identities must
not own these tables or have source-registration rights, CREATE ROLE, superuser,
BYPASSRLS, SET SESSION AUTHORIZATION, DDL/TRUNCATE, or membership allowing them to
assume trusted connection identities. Source logins must not be migration owners.
Migration ownership is not the application security boundary. Parent must provision
and test these permissions before enabling the entrypoints.

Grant sources SELECT on the authorization table and UPDATE **only on lock_marker**
for PostgreSQL row-lock privilege. Do not grant them changes to role_oid, role_name,
purpose or incarnation. lock_marker has no authorization meaning. Restrict other
DML by purpose: controller current/history publisher writes; collector publication
and sequence writes; withdrawal receipts, terminal lifecycle and reservation
removal. Only withdrawal may INSERT receipts or DELETE reservations. API writers
need desired group/member DML and reservation INSERT/no-op UPDATE for the reservation
trigger; they must not delete reservations or write observations/publications.
Read inventory/snapshot/certificate tables and lock permissions are also required
by the collector. The owner of the DB and trusted verifier process remain trusted;
this is not protection against a compromised DB superuser or injected Go code.

RotatePublisher needs controller authority, current group generation and expected
previous epoch (zero at first registration). It creates a random session and keeps
all preceding owners/sessions with their **original generation**. These tokens
fence management reports only. They do not authorize packets or replace the
frontend enforcement controller. Every desired generation change requires another
controller rotation; editing an old token's generation cannot bypass this.

## Collection/publication wiring

ObservationReader now implements EvidenceCollector. Its existing Observe method
remains a read-only projection; Collect exposes the bracketed Pod/Node/input reads
to CollectAndPublish. The ConfigMap schema adds `connectionSession` (UUID). Legacy
records can still be read by Observe, but cannot publish without a session and
container binding. No Ready-only or lease-ACK-only healthy observation is emitted.
No new writer to failover_observations is provided; frontend status remains unknown
without independently implemented frontend telemetry.

Configure the existing HTTPS reader explicitly, then call CollectAndPublish with
a dedicated collector pool, controller token, strictly increasing per-session
sequence and a positive freshness budget no greater than 30 seconds. Sequence is
claimed and the previous publication deleted **before** external collection. A
crashed worker leaves an empty pool. Collection executes once; errors commit empty
evidence, never retry DNS or preserve a last-good pool. Overlapping/delayed workers
cannot overwrite the latest claimed sequence. A failed/ambiguous commit returns
empty eligibility. Missing dependencies, stale source incarnation, generation,
owner/session or management session fail closed.

Publication rechecks engines, certificate revocation/validity, connection session,
last_seen, persistence errors, policy membership, the existing fleet target
algorithm and trusted snapshot digests. Inventory clock deadlines survive DB lock
waits. Original independent observation timestamps remain unchanged. Group-wide
placement/config failures empty the pool; individual proven health/revocation
failures may leave the healthy partner. Collection/deletion/drain do not manufacture
frontend health.

ReadPublished takes a short READ COMMITTED transaction and rechecks source
incarnation, current group/owner/generation, sessions/revocation and configuration.
Missing, expired or future-dated evidence is denied at the database clock. Commit/release its locks promptly.
Use only this checked read, never the raw JSON table, to obtain current eligibility.
It returns telemetry, not a durable permit to forward. Reevaluation and measured
runtime expiry/reconciliation remain required: a database commit after a read can
invalidate that read, and no database transaction fences a Linux transmitter.

The provisioner that writes the trusted ConfigMap must still prove persistent
engine identity-to-Pod/container-to-authenticated connection correlation, current
session ACK provenance and direct-backend probe provenance. The database alone does
not know which container holds an engine's private identity, nor whether a retained
applied_version was ACKed on the current connection. Neither an engine name/label
nor a self-reported session/digest satisfies that obligation. Only the separately
trusted provisioner may write that input. Parent must wire this source and RBAC;
no source is implicitly discovered or authorized by this package.

## Withdrawal proof seam (runtime acceptance remains open)

CompleteWithdrawal requires a provisioned withdrawal LOGIN, exact current
controller token, pending drain/deletion, opaque nonempty bounded proof bytes and
an explicit non-nil WithdrawalVerifier (typed nil is also rejected). It locks the
group/source/publisher and constructs WithdrawalScope from the exact current group,
all historical reporting grants, and all retained member IDs. A verifier must
independently authenticate evidence against that entire scope and ensure **every**
old advertiser/backend writer, including paused/crashed/pre-registration writers,
is durably unable to reactivate. Delayed operations/reboot must not undo that proof.
A timestamp, grace period, lease ACK, claimant assertion or local boolean cannot
implement the verifier. The reporting history is not an exhaustive inventory of
physical writers; the enforcement controller must establish that inventory.

There is intentionally no production verifier implementation here. Do not wire an
always-success callback or expose opaque proof submission as self-service admin
approval. Test callbacks are explicitly synthetic transaction fixtures. Until a
real enforcement-controller integration has passed packet/runtime acceptance,
keep CompleteWithdrawal unreachable from product entrypoints; reservations persist.

On verified completion the transaction stores a digest/source/generation/epoch
receipt and changes lifecycle. Caller audit joins it atomically. Proof payloads
and credentials are not persisted here; the trusted runtime must retain its own
inspectable evidence and a durable fence record. If the DB transaction later fails,
external withdrawal may remain in force but reservations remain held: conservative
recovery, never automatic release or activation. Parent must supply request bounds,
verifier transport/authentication and recovery against actual runtime state.
