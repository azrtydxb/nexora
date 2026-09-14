# nexora-m7-hardening

Status: complete

## Problem

v1 shipped with two claims that were never proven on real infrastructure and 21 deliberate `debt:`
limits. The CI, image, fuzz and perf workflows are meant to run on the ARC runners (#3). `ci` and
`images.yml` are green on every push since 2026-09-14 03:10 (runs 34801672072 onward). The nightly
`fuzz.yml` and `perf-gate.yml` have never run, because the repository was pushed after their
02:17 and 03:00 schedules.

The Docker Compose example has never been started (#5). The only Docker host in reach, novanas
(192.168.10.211), is x86_64 (Debian 13, Docker 29.4.1, Compose v5.1.3). It does not trust the kw
Nexus registry certificate, so the documented `docker compose up` cannot even pull the images
there.

The debt markers (#9–#29) are ceilings an operator or test can hit today:

- a zone with more than 1,024 NSEC records is only partly cached;
- one slow OTLP collector drops query logs;
- a reordered upstream list mislabels query-log records;
- EDNS1 queries get a version-0 answer instead of BADVERS;
- the OpenSearch query log can skip records at a page boundary;
- an invalidated HSM session fails every call until restart;
- every record edit loads the whole zone.

M7 runs after M6 (Operator UX), so these are closed before the protocol, platform and AI milestones
build on the same code.

## Users

- **Homelab operator on Docker:** follows `docs/operations.md` and needs `deploy/compose` to come up
  as written: setup token, engine enrollment, DNS answers, backup and restore.
- **ISP / enterprise operator:** needs the engine to behave predictably at scale:
  - hundreds of ACL CIDRs;
  - large zones, transfers, RPZ IXFR and signed-zone NSEC caching;
  - a bounded, configurable recursor memory;
  - query logs that survive a slow collector and page without gaps;
  - HSMs that recover without a restart.
- **Nexora maintainers:** need every workflow proven on the ARC runners, and a test for each lifted
  limit.

## In scope

- [S-1] (#3) Every workflow proven on the ARC runners:
  - `ci` and `.github/workflows/images.yml` green on the M7 head commit;
  - one scheduled `fuzz` run completed successfully;
  - `perf-gate` executed end to end, once by `workflow_dispatch` and once by its nightly schedule.
  - Anything red is fixed.
  - Design choice: whether the dispatched relative run's 5% verdict holds is issue #6's measurement,
    reported there. It does not decide #3. The run must still build both engines, run all 9 rounds
    of both benchmarks and produce the comparison artifacts.
- [S-2] (#5) The Compose example started for real on novanas.
  - `scripts/compose-verify.sh <ssh target>` runs the `docs/operations.md` Compose commands on the
    host: Postgres, CA, migrations, mgmt, engine, collector; setup token; join token; enrollment;
    DNS answers; backup and restore.
  - The compose files and docs are fixed wherever a step fails as written.
  - Design choice: the host's Docker daemon is not reconfigured. The script copies the images from
    Nexus with `crane pull --platform linux/<host arch>` on the laptop and `docker load` on the
    host, and the operations guide documents this path for hosts that do not trust the registry.
- [S-3] (#9) The private DNS hierarchy fixture synthesises wildcard answers:
  - RFC 1034 §4.3.3 synthesis, with signed RRSIGs whose labels field is below the owner's label
    count, plus the next-closer NSEC/NSEC3 proof;
  - a zone that omits that proof.
  - End-to-end tests prove the engine's wildcard validation.
- [S-4] (#10) The e2e harness restarts the collector on the same port. It retries for up to 10 s
  while another process holds the port, then fails with a message naming the port.
- [S-5] (#11) The engine ACL lookup becomes a binary search over sorted, merged address ranges (one
  table per family). This keeps the lookup logarithmic in the number of CIDRs.
  - Design choice: merged ranges instead of a prefix trie. They are simpler, allocation-free, and
    the allow list only needs "is covered".
- [S-6] (#12) Zone transfers (AXFR/IXFR) are authorised, planned and encoded on tokio's blocking
  pool (`spawn_blocking`), not on the query worker thread.
- [S-7] (#13) When a staged renewed certificate fails for a reason other than PermissionDenied or
  Unauthenticated:
  - the next attempt uses the current identity;
  - if that attempt reaches the control stream, the staged certificate is discarded (a new renewal
    follows because renewal is still due);
  - if both fail, the engine alternates between them with backoff.
- [S-8] (#14) An enrolled identity that cannot be stored is kept in memory. Storing it is retried
  with backoff, and the engine never enrolls again in that process.
- [S-9] (#15) RFC 5011 §2.1 revocation of the only trusted key is accepted.
  - A DNSKEY RRset that verifies only with a revoked, self-signed key (whose REVOKE-cleared form is
    trusted) records that revocation and nothing else.
  - As the trust anchor store already does (RFC 5011 §5), a zone left with no trusted key has no
    trust point.
- [S-10] (#16) The aggressive NSEC cache has no per-zone record cap. Its size is bounded in bytes by
  the recursor cache budget ([S-12]); whole zones are evicted oldest first.
- [S-11] (#17) Infrastructure cache updates are atomic across workers, using the
  `quick_cache::sync::Cache::entry` in-place update (a vacant key is filled through its placeholder
  guard).
- [S-12] (#18) Configurable recursor memory:
  - A new setting, `recursor_cache_max_bytes`, is exposed in the API, GUI, snapshot and engine.
    Default 64 MiB, valid 4 MiB to 16 GiB.
  - It bounds the RRset cache (3/4), the aggressive NSEC cache (3/16) and the infrastructure caches
    (1/16) by estimated bytes instead of fixed entry counts.
  - A snapshot changes the limit in place (`set_capacity`) without clearing the caches.
- [S-13] (#19) RPZ IXFR application builds a string key only for zone records whose owner name
  appears among the difference's deletions, not for every zone record.
- [S-14] (#20, #22) Per-worker buffer pools:
  - TCP and DoT streams (#22) and DoQ (#20) take their query buffer, 64 KiB answer buffer and frame
    buffer from a per-worker pool and return them after the write;
  - the pool keeps at most 256 buffers, and a buffer grown past 65,537 octets is not kept.
- [S-15] (#21) A query whose OPT record carries an EDNS version other than 0 is answered with
  extended RCODE BADVERS (16):
  - header RCODE 0, OPT extended RCODE octet 1, OPT version 0, no answer;
  - before the cookie, authoritative, ACL, filter, cache and resolution stages;
  - logged and counted with rcode 16.
- [S-16] (#23) Query-log records carry the upstream and policy-group labels of the runtime that
  answered them.
  - Each runtime keeps the label tables of the 8 newest runtime versions.
  - The exporter resolves a record by its `config_version`, and emits an empty label when that
    version is no longer kept.
- [S-17] (#24) The OTLP exporter runs up to 4 batch exports concurrently. It drains the ring again
  straight away while at least a full batch waits.
- [S-18] (#25) `GetBlob` streams a blob from PostgreSQL in 1 MiB `substring` reads, never holding the
  whole blob. Blob storage is set to `EXTERNAL` so a substring read touches only its chunk.
- [S-19] (#26) OpenSearch query-log pages sort by `@timestamp` descending and then `_id` ascending,
  so records sharing a millisecond are never skipped.
  - A one-element cursor from an older instance is upgraded to `[timestamp, ""]`.
- [S-20] (#27) The PKCS#11 session pool replaces a session that fails with an invalidation error:
  - the errors are `CKR_SESSION_HANDLE_INVALID`, `CKR_SESSION_CLOSED`, `CKR_DEVICE_REMOVED`,
    `CKR_TOKEN_NOT_PRESENT` and `CKR_USER_NOT_LOGGED_IN`;
  - the replacement looks the slot up again by label and re-reads the PIN file to log in again;
  - the failed call is retried once.
- [S-21] (#28) Zone export streams from the database to the HTTP response without loading the zone.
  - Output order: the SOA from the zone row, then records ordered by apex first, then lowercase
    owner in byte order, then type, then RDATA.
  - Design choice: export is the ceiling the marker names; import keeps its documented 64 MiB body
    and 1,000,000-record limits.
- [S-22] (#29) Record edits in `zone.Service` no longer load the whole zone:
  - validation reads only the edited owners, their ancestors (DNAME) and the apex NS count;
  - for unsigned primary zones, the journal delta is built from the edited RRsets, and the full
    record set is read only when an image is due.
  - Design choice: DNSSEC-signed zones keep the full rebuild in `mgmt/internal/zone/build.go`. NSEC/NSEC3 chain maintenance needs
    the ordered owner set, and `zone_signatures` already limits re-signing. That remainder is
    recorded as a narrower `debt:` marker with its reason in the issue.

## Out of scope

- Issues labelled `pending-decision`: #2, #6 (perf-gate A/A noise and the 5% threshold), #7, #8,
  #34, #38, #39.
- Import streaming larger than the 64 MiB request body.
- Incremental rebuilds of DNSSEC-signed zones.
- Dynamic updates and inbound transfers that load a zone (`dynupdate`, `xfrin`).
- A prefix trie.
- Pooling UDP buffers, which are already per-worker preallocated.
- Changing the novanas Docker daemon configuration, and running Compose in CI.
- New GitHub workflows, runner scale sets or cluster components.
- Every M8–M11 feature.

## Constraints

- Hot path rules from `docs/architecture.md` stay binding: no logging, allocation or locks held
  across packets on the cache-hit path. `cache_hit_path_does_not_allocate`,
  `authoritative_answer_path_does_not_allocate` and the blocked-reply measurement in
  `engine/tests/hot_path_alloc.rs` keep passing. The BADVERS check and the ACL range search add no
  allocation.
- Every existing test keeps passing unchanged, including `TestGUICoverage`, the full local e2e suite
  and `scripts/kw-acceptance.sh`.
- M7 builds on M6 as committed. When M6 changed a function or file named here, the task adapts to
  the committed code and updates its plan text.
- Numbering, unless M6 as built already took these numbers (then the next free range, recorded in
  `docs/architecture.md`):
  - proto fields added to existing messages use 800–899;
  - migrations are `00800_recursor_cache_max_bytes.sql` and `00801_blobs_storage_external.sql`.
- kw rules from the roadmap:
  - deploy only after every test passes, through `scripts/kw-deploy.sh` with the DNS probe;
  - never change 192.168.10.136 or 192.168.10.139;
  - roll back on any lost query or failed acceptance.
- novanas is used only through `scripts/compose-verify.sh`:
  - non-default ports: DNS 15353, HTTP 18080, gRPC 19443, metrics 19153, OTLP 14317;
  - everything under `~/nexora-compose-verify`;
  - `docker compose down -v` at the end, plus removal of the images the run added.
- Secrets stay out of the repository. The verification script generates the Postgres password and
  admin password at run time and deletes them with the directory.
- Rust 1.97.1, edition 2024, `cargo clippy -D warnings`; Go module `github.com/piwi3910/nexora`.

## Interfaces

- **Contract:** `RecursionConfig.cache_max_bytes = 800` (uint64; 0 means 64 MiB; otherwise
  4,194,304 to 17,179,869,184).
- **API:** the `ResolutionSettings` schema in `mgmt/api/openapi.yaml` gains recursor_cache_max_bytes, an integer (int64) from 4194304 to
  17179869184, required in responses and in `PUT /resolution/settings`, default 67108864.
- **GUI:** the resolution settings section gains "Recursor cache memory (MiB)". Tooltip, following
  M6 #58: "Memory for the RRset, aggressive NSEC and server caches of recursive resolution".
- **Engine query path:**
  - BADVERS replies as in [S-15];
  - `Acl::allows` keeps its signature;
  - `Acl::v4_ranges() -> usize` and `Acl::v6_ranges() -> usize` report merged range counts.
- **Engine internals:**
  - `server::buffers::{take() -> Vec<u8>, give(Vec<u8>)}`, a thread-local pool with
    `POOL_MAX = 256` and `BUFFER_CAPACITY = 65_537`;
  - `telemetry::otlp::MAX_INFLIGHT = 4`;
  - `runtime::LabelHistory` with `LABEL_HISTORY = 8`;
  - `recursor::memory` split constants `RRSET_SHARE = 12/16`, `NSEC_SHARE = 3/16`,
    `INFRA_SHARE = 1/16`.
- **Management plane:**
  - `control.Server.GetBlob` behaviour unchanged on the wire (1 MiB chunks, NotFound);
  - `querylog.OpenSearch` cursor becomes a JSON array `[timestamp, _id]`;
  - `zone.Service.ExportTo(ctx, zoneID, w io.Writer) error`;
  - `GET /zones/{zoneId}/export` keeps `text/plain` and `Content-Disposition` but has no
    `Content-Length`.
- **Scripts:** `scripts/compose-verify.sh <user@host>`, which exits 0 only when every step passes and
  prints one `ok <step>` line per step.
- **Test fixture:**
  - `authhier` zones gain `*.w.good.test.` (A 192.0.2.60, TXT "wild") and `*.w.n3.test.`
    (A 192.0.2.61);
  - a zone `wild.test.` whose zone spec in `e2e/fixtures/authhier/spec.go` sets a new OmitWildcardProof flag.
- **Harness:** `(*Otelcol).Restart(e)` retries the same port for 10 s.

## Data

- **PostgreSQL:**
  - `resolution_settings.recursor_cache_max_bytes bigint not null default 67108864` with a check
    of 4194304 to 17179869184 (migration 00800);
  - `ALTER TABLE blobs ALTER COLUMN data SET STORAGE EXTERNAL` (migration 00801; it applies to rows
    written afterwards, and older rows still stream correctly, only slower).
  - No other schema change.
- **Engine memory:**
  - recursor caches weighted by estimated bytes;
  - label history of at most 8 upstream-name and policy-group-id tables per runtime;
  - at most 256 × 65,537 octets of pooled stream buffers per worker.
- **Engine state directory:** unchanged. An unstorable identity lives only in memory until stored.
- **OpenSearch:** unchanged documents. Queries add an `_id` sort, which needs
  `indices.id_field_data.enabled` (default `true`, verified on kw's OpenSearch 3.8.0 on 2026-09-14).
- **Compose:** named volumes `pgdata`, `ca`, `enginestate` as today. Verification runs use project
  name `nexora-verify` so the volumes are removed with `down -v`.

## Edge cases

- **BADVERS ([S-15]):**
  - EDNS version 255 also gets BADVERS;
  - a malformed OPT keeps FORMERR;
  - a version-1 query to a hosted zone gets BADVERS, not an authoritative answer;
  - a version-1 query without a cookie still gets BADVERS.
- **ACL ([S-5]):**
  - overlapping and adjacent CIDRs merge;
  - `0.0.0.0/0` and `::/0` cover everything;
  - IPv4-mapped IPv6 clients use the v4 table;
  - an empty list refuses everything;
  - host bits in a CIDR are truncated as today.
- **Transfers ([S-6]):** a client that disconnects while the transfer builds wastes one blocking
  task. The blocking pool is tokio's default (512 threads), and transfers are rare and need ACL and
  TSIG.
- **Renewal ([S-7]):**
  - with the management plane down, both identities fail and the staged one is kept;
  - an engine sharing the state directory that stages another certificate meanwhile: only the
    refused one is discarded, as today.
- **Enrollment ([S-8]):**
  - a state directory that becomes writable later: the in-memory identity is stored with the same
    engine id;
  - a process restart before storing re-enrolls once, which is accepted.
- **RFC 5011 ([S-9]):**
  - a revoked key whose self-signature does not verify is ignored;
  - a revoked key that is not trusted changes nothing;
  - revoking the root's only key makes validation insecure (RFC 5011 §5), and the trust anchor
    status shows `Revoked`. Because this silently turns DNSSEC validation off, the engine also
    raises `nexora_dnssec_trust_point_lost{zone}` (gauge 1) and the DNSSEC page shows a
    destructive alert until an operator adds a trust anchor (lead decision 2026-09-14).
- **NSEC cache ([S-10]):**
  - a single zone's denials larger than the whole NSEC budget are cached up to the budget;
  - expired entries are pruned before eviction.
- **Recursor budget ([S-12]):**
  - lowering the setting evicts at once, raising it keeps entries;
  - `0` from an older management plane means the default.
- **RPZ IXFR ([S-13]):**
  - deletions with owner-name case differences still match (keys stay lowercase);
  - a deletion of a record that is not in the zone keeps the existing error.
- **Buffer pool ([S-14]):**
  - a zone transfer's many frames return their buffers after each write;
  - a stream that closes with queued frames drops them (they are not returned, which is bounded);
  - a DoQ query above 65,535 octets keeps the protocol error.
- **Label history ([S-16]):**
  - two reloads inside one 100 ms drain window still resolve, because history is built at runtime
    construction, not at drain;
  - a record older than 8 versions gets empty labels.
- **Concurrent exports ([S-17]):**
  - batches can arrive at the collector out of order (records carry timestamps);
  - the export queue stays bounded at 8 queued batches plus 4 in flight.
- **Blob streaming ([S-18]):**
  - an empty blob sends no chunk, an exact multiple of 1 MiB sends no empty trailing chunk;
  - a blob garbage-collected mid-stream ends the stream with NotFound, and the engine's size and
    SHA-256 check rejects the partial blob and retries.
- **OpenSearch paging ([S-19]):**
  - indices from before `nexora-querylog-v2` sort the same way;
  - a cursor that is not base64 JSON is still `ErrInvalidCursor`.
- **PKCS#11 ([S-20]):**
  - a token removed for good: the reconnect fails, the call returns `ErrBackendUnavailable`, the
    pool keeps 4 handles and nothing deadlocks;
  - four concurrent failures on one event rebuild once (generation counter).
- **Zone export ([S-21]):**
  - owners with escaped octets sort byte-wise and stay reparseable;
  - a zone deleted between the 404 check and the stream ends with a truncated body and a logged
    error;
  - a client that disconnects cancels the query.
- **Zone edits ([S-22]):**
  - an update that moves a record to another owner checks both owners;
  - a DNAME added above existing names, or a name added below a DNAME, still fails
    `dname_occludes`;
  - the last apex NS is still protected;
  - an image falling due during an incremental edit reads the full set once.
- **Wildcard fixture ([S-3]):**
  - a query for the wildcard owner itself is an exact match;
  - a name below an existing non-wildcard name is not synthesised (RFC 4592 closest encloser).
- **Compose ([S-2]):**
  - port 53 on the host is left alone;
  - the engine applies a version above the restored database's, so after restore the script
    follows the documented recovery (publish until the engines are `current`) before the final DNS
    check.

## Failure modes

- **ARC runner or Nexus unavailable during the proof:** the run is re-dispatched. A red job caused
  by the workflow itself is fixed in the workflow, and the fix is proven by a new green run.
- **Fuzz finds a crash:** the input becomes a regression case in `engine/fuzz/corpus/parse_query`
  plus a unit test in `wire.rs`, the parser is fixed, and the nightly run is repeated.
- **novanas unreachable, or Docker Hub rate-limited:** the script stops at the failing step with its
  output, leaves nothing running (a trap runs `down -v`), and is re-run.
- **A documented Compose command fails as written:** the compose file or the doc is fixed and the
  whole script re-runs from a clean host.
- **OpenSearch with `indices.id_field_data.enabled=false`:** search returns HTTP 400.
  `querylog.OpenSearch` returns the error and `docs/operations.md` names the setting.
- **HSM gone for good:** calls fail with `ErrBackendUnavailable` and a restart is still possible.
- **Collector permanently slow or down:** exports fail at most 4 at a time, queued batches are
  dropped oldest first and counted in `nexora_export_dropped_total{signal="logs"}`, and workers are
  never blocked.
- **Blocking pool saturated by transfers:** later transfers wait. DNS queries on the workers are
  unaffected.
- **Recursor budget too small for the workload:** more cache misses and upstream queries, never an
  error.

## Acceptance criteria

- [ ] [S-1] #3 proof, recorded in the #3 closing comment with run URLs:
  - `gh run list -R azrtydxb/nexora --workflow ci.yml --commit <M7 head>` and the same for
    `images.yml` show `success`;
  - `gh run list -R azrtydxb/nexora --workflow fuzz.yml --event schedule --limit 1` shows a
    completed `success` run of the one-hour `parse_query` job;
  - `gh run list -R azrtydxb/nexora --workflow perf-gate.yml --event schedule --limit 1` shows
    `success`;
  - the `workflow_dispatch` perf-gate run shows the build step, 9 interleaved rounds of both
    benchmarks and uploaded `perf-relative` artifacts, with the gate verdict posted on #6.
  - Fails if any of those runs is missing or red for a reason other than the #6 verdict.
- [ ] [S-2] `scripts/compose-verify.sh piwi@192.168.10.211` exits 0 on a clean novanas. It prints
      `ok` for:
  - pull, up, setup-token, setup, join-token, engine-enrolled, dns-answers, otel-logs, backup,
    restore, restored-dns and cleanup.
  - Proof: `dig @192.168.10.211 -p 15353 www.compose.test A` answers 192.0.2.10 before the backup
    and again after the restore.
  - Fails if any documented command in "Install with Docker Compose" or "Plain PostgreSQL and
    Compose" has to be changed at run time without being changed in the docs.
  - `TestComposeExample` additionally fails if `docs/operations.md` still says the example "is not
    started".
- [ ] [S-3] Fixture test `TestWildcardAnswerCarriesExpandedSignatureAndNextCloserProof` fails if a
      synthesised answer lacks the expanded RRSIG or the next-closer denial. `TestDNSSECValidation`
      fails if either case is answered differently:
  - Subtest `wildcard answer validates with AD`: `x.w.good.test` and `x.w.n3.test` return AD=1 with
    192.0.2.60 and 192.0.2.61.
  - Subtest `wildcard NODATA is proven`: `x.w.good.test AAAA` returns NOERROR, no answer, AD=1.
  - Subtest `wildcard answer without next-closer proof is bogus`: `x.w.wild.test` returns SERVFAIL
    with EDE 12.
- [ ] [S-4] `TestHarnessOtelcolRestartWaitsForPortHolder` holds the stopped collector's port for
      1.5 s during `Restart`. It fails if `Restart` gives up instead of waiting.
- [ ] [S-5] `.github/workflows/ci.yml` runs `cargo test -p nexora-engine --lib acl::tests`, which includes Rust tests `acl::tests::overlapping_cidrs_merge_into_ranges` and
      `acl::tests::range_search_matches_linear_scan` run 1,000 random CIDRs × 10,000 addresses per
      family against the v1 linear scan. They fail if a range count is not merged or any decision
      differs. `cache_hit_path_does_not_allocate` still passes.
- [ ] [S-6] `.github/workflows/ci.yml` runs `cargo test -p nexora-engine --lib transfer_is_built_off_the_worker`, which includes Rust test `authoritative::xfr_tests::authorization::transfer_is_built_off_the_worker`, which
      fails if a task spawned on the same `current_thread` runtime cannot run before `run_slow` returns
      a zone transfer.
- [ ] [S-7] `.github/workflows/ci.yml` runs `cargo test -p nexora-engine --test control_renewal`, which includes Rust test `staged_certificate_failing_otherwise_falls_back_and_is_discarded` in
      `engine/tests/control_renewal.rs` fails if, after a staged certificate is answered with
      `Unavailable`, the next connection presents it again instead of the current certificate, or if
      `identity.new` survives a working current identity.
- [ ] [S-8] `.github/workflows/ci.yml` runs `cargo test -p nexora-engine --test control_enroll`, which includes Rust test `unstorable_identity_is_not_enrolled_again` in `engine/tests/control_enroll.rs`
      fails if the fake management plane sees a second `Enroll` while `state_dir/identity` is blocked
      by a file, or if the stored identity's engine id differs from the first enrollment's once the
      block is removed.
- [ ] [S-9] `.github/workflows/ci.yml` runs `cargo test -p nexora-engine --lib anchors_tests`, which includes Rust test `refresh_accepts_revocation_of_the_only_trusted_key` fails if an RRset signed
      only by the revoked form of the only trusted key does not mark that key `Revoked` and remove the
      trust point, or if a new key in that RRset is added.
- [ ] [S-10] `.github/workflows/ci.yml` runs `cargo test -p nexora-engine --lib nsec_cache`, which includes Rust test `aggressive_nsec_caches_more_than_1024_denials_of_one_zone` fails if an
      NXDOMAIN covered by the 4,000th of 5,000 cached NSEC records is not synthesised.
- [ ] [S-11] `.github/workflows/ci.yml` runs `cargo test -p nexora-engine --lib infra::tests`, which includes Rust test `infra::tests::concurrent_updates_are_not_lost` (8 threads × 100,000 updates
      of one server) fails if the final count differs from 800,000.
- [ ] [S-12] Budget and setting are proven end to end. Each test fails if its setting is not carried,
      not applied or not persisted:
  - Rust `rrcache::tests::rrset_cache_is_bounded_by_bytes` fails if the RRset cache's weight
    exceeds its byte capacity or ignores `set_capacity`.
  - `snapshot_m3` test `cache_max_bytes_outside_range_is_rejected` fails if values outside the
    range are accepted.
  - Go `TestResolutionSettingsRecursorCacheMaxBytes` (API round trip, 400 `invalid_request` below 4 MiB, snapshot
    field 800).
  - Playwright `17-resolution.spec.ts` sets 128 MiB and reloads to see it kept.
- [ ] [S-13] `.github/workflows/ci.yml` runs `cargo test -p nexora-engine --lib transfer_tests`, which includes Rust test `ixfr_builds_keys_only_for_records_at_deleted_owners` applies a 1-record
      deletion to a 100,000-record zone. It fails if more keys are built than the records at the
      deleted owner plus the deletions.
- [ ] [S-14] `.github/workflows/ci.yml` runs `cargo test -p nexora-engine --test hot_path_alloc`, which includes `stream_answers_reuse_pooled_buffers` (TCP, 1,000
      pipelined cache hits after 64 warm-up queries) and `doq_answers_reuse_pooled_buffers` (200 DoQ
      cache hits after warm-up) fail if any allocation of 60,000 octets or more (a 64 KiB buffer) happens on the measuring
      thread.
- [ ] [S-15] `.github/workflows/ci.yml` runs `cargo test -p nexora-engine --test server_pipeline`, which includes Rust test `edns_version_above_zero_gets_badvers` in `engine/tests/server_pipeline.rs`
      fails if an EDNS version 1 query is not answered with extended RCODE 16 (header RCODE 0, OPT
      version 0, no answers), or if the upstream is queried. A version 0 query in the same test still
      gets NOERROR.
- [ ] [S-16] `.github/workflows/ci.yml` runs `cargo test -p nexora-engine --test telemetry_export`, which includes Rust test `upstream_label_uses_the_runtime_that_answered` in
      `engine/tests/telemetry_export.rs` fails if a record answered under version 1 (upstreams
      `[fixture]`) is exported with another label after version 2 reorders the upstreams to
      `[other, fixture]`.
- [ ] [S-17] `.github/workflows/ci.yml` runs `cargo test -p nexora-engine --test telemetry_export`, which includes Rust test `slow_collector_does_not_drop_with_concurrent_exports` (collector delay
      400 ms, 20,000 records) fails if any log is dropped or the collector never sees more than one
      concurrent export.
- [ ] [S-18] Go tests fail if the server holds the blob or a boundary sends a wrong chunk count, and
      `TestGetBlobStreamsMiBChunks` still passes:
  - `TestGetBlobDoesNotHoldWholeBlob` fails if the test process's `HeapAlloc` after `runtime.GC()`
    exceeds 40 MiB while a 64 MiB blob is mid-stream;
  - `TestGetBlobChunkBoundaries` covers 0 octets, 1 MiB and 2 MiB.
- [ ] [S-19] Pagination is proven in unit and e2e tests:
  - Go `TestOpenSearchSortHasUniqueTiebreaker` fails if the query body lacks the `_id` sort, the
    cursor does not round-trip two values, or a one-element cursor is rejected.
  - e2e `TestOpenSearchPagesRecordsSharingAMillisecond` indexes 3 documents with one `@timestamp`
    into the shared OpenSearch. It fails if paging with limit 1 does not return all 3.
- [ ] [S-20] Go `TestPKCS11RecoversFromInvalidatedSessions` fails if any call errors after the test
      closes all sessions on SoftHSM2 (8 unseals and signing-key generations). It also fails if, with
      the PIN file unreadable, the call does not return `ErrBackendUnavailable` or later calls deadlock.
- [ ] [S-21] Export streams without breaking the round trip:
  - Go `TestExportZoneFileStreams` fails if the export response sets `Content-Length`;
  - `TestExportToIsStableAndReparses` fails if two exports of the same records inserted in a
    different order differ, or the output does not reparse;
  - `TestZoneFileRoundTrip` still passes `ldns-compare-zones`.
- [ ] [S-22] Edits are proven not to load the zone, and the delta stays exact:
  - Go `TestRecordEditDoesNotLoadWholeZone` (2,000 records) fails if a create, update or delete of
    an unsigned zone record runs a `zone_records` select returning more than 16 rows.
  - `TestIncrementalEditsMatchFullRebuild` fails if, after 200 random edits, `zone.LoadServed`
    differs from the full record set.
  - `TestOwnerScopedChecksKeepRules` fails if `dname_occludes`, `ds_not_at_delegation`,
    `cname_conflict` or `last_apex_ns` can be bypassed.
  - The signed-zone tests in `mgmt/internal/dnssec` still pass.
- [ ] [S-3] [S-4] [S-5] [S-6] [S-7] [S-8] [S-9] [S-10] [S-11] [S-12] [S-13] [S-14] [S-15] [S-16] [S-17] [S-18]
      [S-19] [S-20] [S-21] [S-22] Go test `TestM7DebtMarkersResolved` in `deploy/deploytest` reads the
      files of the 21 markers of #9–#29 and fails if any original marker text remains. The only
      exception is the rewritten signed-zone marker of [S-22] in `mgmt/internal/zone/build.go`. Each
      issue is closed with a comment naming the commit and the test, and
      `gh issue list -R azrtydxb/nexora --state open` lists none of #3, #5, #9–#29.

## Open questions
