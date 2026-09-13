# nexora-v1-m4 — implementation plan

Status: draft
Spec: .procoder/specs/nexora-v1.md

## Goal

Ship milestone M4 "Authoritative" (S-3, S-8, S-17, S-18, S-19, S-23): engines serve zones managed in Nexora authoritatively, transfer them to secondaries, pull secondary zones, accept TSIG-signed RFC 2136 updates, import/export BIND zone files, and serve online-signed DNSSEC data whose private keys live KEK-encrypted in PostgreSQL or inside a PKCS#11 HSM.

## Architecture

The management plane owns all zone state in PostgreSQL: records, a per-zone journal of serial diffs, DNSSEC keys and signatures. On every change it rebuilds the zone's _served_ RR set (signed when DNSSEC is on), diffs it against the previous one, writes the diff as an NZF delta blob and periodically a full NZF image blob into the M1 `blobs` table, and references both as `BlobRef`s from the `ConfigSnapshot`; engines fetch them with the M1 blob path, apply deltas incrementally in `Runtime::build`, answer hosted names from an in-memory canonical-order tree in `server::handle_packet` right after parsing (before the ACL, M2 policy, M3 RPZ, cache and resolution stages), stream AXFR/IXFR from the same deltas over TCP and DoT, send NOTIFY, and forward incoming NOTIFY and UPDATE messages to the management plane over the existing `Connect` stream. TSIG secrets reach engines only in the in-memory `KeyMaterial` control message, following M3's `RpzTsigKeys`; DNSSEC private keys never leave the management plane.

The engine query pipeline after M4 (M1-M3 stages unchanged apart from the moved cookie steps): `wire::parse_query` → EDNS reply limit and OPT → bad-cookie FORMERR and server cookie (moved above the ACL so authoritative answers carry cookies) → **authoritative stage**: when `!rt.auth.is_empty()`, `authoritative::dispatch::fast` answers a hosted name (`FastOutcome::Reply`, query log `nexora.cache=auth`), refuses AXFR over UDP, or hands a transfer to `FastOutcome::Slow`; otherwise the query continues → ACL (REFUSED) → M2 `rt.policy.select`/`check` → M3 RPZ query phase → cache → `resolve_miss` (M3 `recursor::dispatch::resolve_miss`). Messages `parse_query` rejects with FORMERR/NOTIMP (opcodes NOTIFY and UPDATE, IXFR queries with NSCOUNT=1, queries whose additional section carries TSIG) first go to `authoritative::dispatch::unparsed`, which answers them inline or returns `FastOutcome::Slow`; everything else keeps M1's error reply.

### Reconciled with M1-M3 code

Design-level changes against the original M4 plan (HEAD 083f943 plus M3 Task 5's query-path shape from `.procoder/plans/nexora-v1-m3.md`):

1. **Authoritative answering sits inside `server::handle_packet`**, not in `server/udp.rs`/`tcp.rs`: all transports (UDP, TCP, DoT, DoH, DoQ) share that function, and the M3 pipeline already runs ACL → policy → RPZ → cache there. The stage runs after the cookie steps (moved above the ACL) and before the ACL; the cache-hit path gains one `AuthSet::is_empty` load. `FastOutcome` gains `Slow(authoritative::dispatch::SlowJob)` (NOTIFY needing no reply wait is answered inline; UPDATE waits for the management plane; transfers produce several messages). Multi-message transfers are written by `server::stream::serve_dns_stream` through a new `Answerer::answer_frames` method (default: one frame from `answer`), so AXFR/IXFR work on TCP and DoT; DoH and DoQ answer AXFR/IXFR with REFUSED; UDP keeps FORMERR for AXFR and a single SOA for IXFR.
2. **Zone blobs are ordinary M1 blobs.** Images and deltas are zstd NZF1 bytes stored with `store.PutBlob` in `blobs`, referenced by the existing `BlobRef` message (no `ZoneBlobRef`, no `zone_blobs` table, no GetBlob change, no separate GC: `blocklist.Fetcher.collectBlobs` keeps blobs referenced by kept snapshots' `auth_zones` and by `zone_images`/`zone_journal`). `control::fetch_blobs` downloads every missing referenced blob; the loader runs synchronously inside `Runtime::build` over `snapshot::BlobSource` (`DirBlobs` verifies size and SHA-256) and reads only the blobs it applies.
3. **Zone mutations publish through `snapshot.Mutate`** (change rows → `auth.WriteAudit` → `snapshot.Build` → `config_versions` → `pg_notify`), with `auth.Actor` and `auth.Change`, instead of the assumed `zone.Publisher`/`zone.Auditor`; `snapshot.Build` calls `snapshot.AddAuthZones`. Zone errors reuse `store.ErrNotFound`/`store.ErrConflict`. Unit tests use a new helper `storetest.New(t) *store.Store` (fresh PostgreSQL from `harness.StartPostgres`, migrated), since no `storetest.NewPool` exists.
4. **Key storage extends `mgmt/internal/secrets`** (M3 already implements the NXE1 file-KEK envelope there) instead of creating `mgmt/internal/keystore`: `secrets.Open(secrets.Config)` adds the PKCS#11 backend (envelope wrap byte 2), DNSSEC signing-key generation/signers and backend selection; `secrets.LoadKEKFile` keeps working, `api.Deps.Secrets` stays the single handle, and the M3 API message for `key_storage_unconfigured` is unchanged.
5. **Engine TSIG reuses M3's implementation.** `engine/src/recursor/rpz/tsig.rs` (ring HMAC, request signing, multi-message response verification) moves to `engine/src/tsig.rs` (`recursor/rpz/mod.rs` re-exports it as `rpz::tsig`, so M3 call sites compile unchanged) and gains HMAC-SHA384, server-side request verification, stream signing, TSIG error responses and the in-memory `KeyRing`. No `hmac`/`sha1` crates are added: HMAC and NSEC3 SHA-1 use `ring`, which the engine already pins. `TsigAlgorithm` gains `TSIG_ALGORITHM_HMAC_SHA384 = 3` (M3's RPZ validation keeps accepting only SHA-256/512).
6. **`KeyMaterial` follows the `RpzTsigKeys` delivery pattern:** the complete TSIG key set without a generation counter, loaded by `control.TSIGKeys` (unsealed per load), digest-deduplicated per subscriber, sent before the snapshot on `Connect` and offered on every hub broadcast. TSIG key create/delete therefore run through `snapshot.Mutate` (publishing a config version) instead of a `key_material_state` table and a `nexora_keys` channel. Replies to engine requests (`UpdateResult`) use a new per-subscriber results channel, because the capacity-1 `out` channel keeps only the latest snapshot.
7. **Engine process state for M4 lives in `Shared.auth: Arc<authoritative::AuthState>`** (key ring, control-stream sender for NOTIFY/UPDATE forwarding, pending update waiters, control-runtime handle), created by `Shared::new`/`Shared::with_recursor` like M3's `Shared.recursor`. `Runtime.auth: Arc<AuthSet>` (plus load counts and changed zones) is built in `Runtime::build(s, blobs, previous)`; `authoritative::after_apply(&shared, &runtime)` runs at the call sites of M3's `shared.recursor.sync` (control apply, persisted and standalone snapshots) to count loads and send NOTIFY. Metrics are added to `telemetry::metrics::Metrics` (`auth` counters, `WorkerCounters.auth_answers`), so `render`/`stats` keep their M3 signatures.
8. **HTTP conventions:** every non-2xx response references `#/components/responses/Error`; zone validation failures are 422 with a specific `code` and the `Error` schema gains an optional `details: [{line, message}]`; `jsonOnly` keeps the 4 MiB body cap except `POST /api/v1/zones/{zoneId}/import` (64 MiB).
9. **Tests use the real harness:** `harness.New`, `StartPostgres`, `InitCA`, `StartMgmt(pg, ca, MgmtOptions{ExtraEnv})`, `Bootstrap`, `StartManagedEngine`, `WaitEngine`, `(*API).Must/Do` with paths relative to `/api/v1`, `(*Engine).Metric`, `harness.WriteKEK`, `(*Env).FreePort`; `harness.QueryOpts` gains `DO`; M3's BIND helper is generalised as `(*Env).StartNamedConfig`. Fixture secrets are computed, never literal.
10. **GUI follows the M2/M3 layout:** pages in `web/src/pages/`, hooks in `web/src/api/zones.ts`, routes in `web/src/app/router.tsx`, nav in `web/src/components/layout/AppShell.tsx`, Playwright specs `web/e2e/screens/18-zones.spec.ts` and `19-zone-security.spec.ts` counted by request-based `TestGUICoverage` (no annotations, no Vitest: the web package has no unit-test runner). `TestGUICoverage` gets `NEXORA_KEK_FILE`, so M3's `15-rpz.spec.ts` saves its TSIG transfer zone instead of asserting the 503 (still covered by `mgmt/internal/api/resolution_test.go`).
11. **kw:** the engine DaemonSet's DNS LoadBalancer already exposes TCP 53 with `externalTrafficPolicy: Local`, so only `deploy/kw/mgmt.yaml` (secret `nexora-kek`) and `scripts/kw-deploy.sh` (creates the secret) change. The repository has no Helm chart or compose file; M4 does not add them. The smoke test is `TestKwSmokeM4` in package `e2e`, reusing `loadKwEnv`/`kwLogin`/`kwWaitApplied`.

### Design decisions (not settled by docs/architecture.md)

1. **Mgmt pulls secondary zones, not engines.** PostgreSQL is the single source of truth (GUI, export, IXFR-out journal, snapshot versioning all need the data there); one pull per zone instead of N engine pulls keeps load on external primaries flat and every engine on the same serial; engines keep no zone state outside the snapshot. Engines receive NOTIFY (they are the addresses primaries know) and forward `NotifyReceived`; one mgmt instance per zone refreshes under `pg_try_advisory_lock(hashtext('zone_refresh:'||id))`, like blocklist fetches.
2. **Engines send NOTIFY, after applying the new version.** The source address then matches the primary address secondaries are configured with, and a secondary that reacts immediately transfers from an engine that already holds the new serial. Every engine notifies (duplicates are harmless).
3. **Engines serve AXFR/IXFR from the deltas listed in the snapshot**; the journal of record lives in `zone_journal` in Postgres; the last 32 journal entries (plus everything after the current image) are listed per zone. Requests older than that fall back to AXFR-style full responses (RFC 1995 §4).
4. **NZF1 binary zone format** (Task 1) carries full images and deltas; blobs are zstd-compressed and addressed by SHA-256 hex of the compressed bytes, as blocklist blobs are. A new image is written when 64 deltas or deltas totalling a quarter of the image size accumulated.
5. **Proto numbering:** M4 owns field numbers 200-299 in `ConfigSnapshot` and in both `Connect` stream envelope `oneof`s (`ServerMessage`, `EngineMessage`); nothing in `control.proto` uses 200-299 at 083f943. New messages number from 1.
6. **Migrations** `mgmt/migrations/00400_zones.sql` and `00401_dnssec.sql` (after M1 `00001`, M2 `00200`, M3 `00300`-`00302`).
7. **Go DNS library:** `github.com/miekg/dns v1.1.73` (already in `go.mod`) for RDATA parsing/printing, TSIG, AXFR/IXFR client, DNSSEC signing, NSEC3 hashing. The zone-file _lexer_ (directives, parentheses, comments, owner inheritance, `$INCLUDE`/`$GENERATE` refusal) is Nexora code; RDATA text of each logical record goes through miekg's parser.
8. **Engine TSIG is Nexora code** (M3's `ring`-based module, change 5), cross-checked against miekg-generated vectors. Algorithms: hmac-sha256, hmac-sha384, hmac-sha512 only.
9. **TSIG verification happens on the engine** (it must sign the response); mgmt re-verifies forwarded UPDATEs (defence in depth) and authorises the key against the zone.
10. **Authoritative answers ignore `acl_allow_cidrs`, M2 filtering/rewrites, M3 RPZ and the response cache**: the ACL restricts recursion/forwarding only; hosted data is the operator's own. `RA` is set when the client passes the ACL. Query-log `nexora.cache` gains value `auth`.
11. **Authoritative behaviour details:** CNAME/DNAME chains are followed across hosted zones up to 8 hops and never into recursion; loops → SERVFAIL; DNAME result > 255 octets → YXDOMAIN; ANY → the lowest-numbered RRset at the name (RFC 8482); additional-section processing only for referral glue; DS at a cut is answered from the parent zone when it is hosted; expired secondary → SERVFAIL; UDP AXFR → FORMERR; UDP IXFR → single SOA; transfers split into messages ≤ 16384 octets, each TSIG-signed.
12. **Queries carrying TSIG** are accepted for hosted zones only (verified, response signed); otherwise REFUSED.
13. **Dynamic updates:** unsigned → REFUSED; bad TSIG → NOTAUTH with TSIG error; key not in zone's `update.tsig_key_ids` → REFUSED; zone not hosted → NOTAUTH; secondary zone → REFUSED; engine waits 5 s for `UpdateResult`, otherwise SERVFAIL. Updates touching DNSKEY/RRSIG/NSEC/NSEC3/NSEC3PARAM/CDS/CDNSKEY or types outside the managed list → REFUSED.
14. **Zone data model:** SOA fields live on the `zones` row (SOA is not a record); owners stored case-preserved in miekg presentation form, uniqueness on `lower(owner)`; writing a record sets the TTL of its whole RRset; every record change bumps the zone `revision`; records carry their own `revision` (stale → 409); import replaces all records and requires the zone revision.
15. **Serial policy:** primaries increment by one (RFC 1982, `4294967295 + 1 = 0`); an imported SOA serial is adopted only when RFC 1982-greater than the current one.
16. **Key storage:** envelope layout `NXE1` (M3 `mgmt/internal/secrets`). File KEK = 32 bytes base64 in `NEXORA_KEK_FILE`. Both backends may be configured at once; DNSSEC keys use the zone's `key_backend` (`kek` | `pkcs11`, default `pkcs11` when configured); TSIG secrets are wrapped by the file KEK when set, else by a non-extractable AES-256 key `nexora-kek` (CKA_ID `nexora-kek-v1`) in the token. With neither configured, creating TSIG keys or enabling DNSSEC returns **503 `key_storage_unconfigured`**. Partial PKCS#11 configuration refuses to start.
17. **DNSSEC timings:** RRSIG inception now−1 h, expiration now+14 d minus `fnv32a(owner|type) mod 3600` s, re-signed when < 7 d remain; DNSKEY/CDS/CDNSKEY TTL = SOA TTL; NSEC/NSEC3 TTL = min(SOA TTL, SOA MINIMUM). ZSK pre-publish: publish → activate after DNSKEY TTL + propagation delay → retire → remove after max zone TTL + propagation delay; automatic every `zsk_lifetime_days` (default 90, 0 = manual). KSK double-signature: new KSK signs the DNSKEY RRset at once, CDS/CDNSKEY advertise KSKs with `ds_state=pending`; the operator confirms the parent DS (`confirmZoneKskDs`); the old KSK is removed after `parent_ds_ttl_seconds` + propagation delay. Algorithm rollovers are refused (422 `algorithm_rollover_unsupported`). NSEC3: SHA-1, 0 iterations, empty salt, no opt-out.
18. **Engine metrics added** (prometheus-client counters registered without `_total`, every label value created at zero): `nexora_auth_zones`, `nexora_auth_zone_loads_total{kind="full|delta|reused"}`, `nexora_auth_answers_total{result="answer|nodata|nxdomain|referral|servfail"}`, `nexora_auth_transfers_total{type="axfr|ixfr",result="full|incremental|uptodate|refused"}`, `nexora_auth_notify_sent_total{result="acked|rejected|timeout|nokey"}`, `nexora_auth_notify_received_total{result="forwarded|refused|dropped"}`, `nexora_auth_updates_total{result="forwarded|refused|notauth|servfail"}`.
19. **New Go packages:** `mgmt/internal/nzf`, `zone`, `zonefile`, `tsigkey`, `dnssec`, `xfrin`, `dynupdate`, `store/storetest`; `mgmt/internal/secrets` is extended. **New engine files** under `engine/src/authoritative/`, plus `engine/src/tsig.rs` (moved) and `engine/src/tsig_tests.rs`.
20. **GUI routes:** `/zones`, `/zones/tsig-keys`, `/zones/:zoneId` (tabs Records, Transfers, DNSSEC, Import/Export). Export content type `text/plain; charset=utf-8`. Import body limit 64 MiB, 1,000,000 records.

## Constraints

Copied from the spec (verbatim):

- DNS engine: Rust, hickory-proto used as wire codec only; server loop, cache and resolver are Nexora code.
- Management plane is stateless: all state in PostgreSQL, any number of instances behind a load balancer, engines may connect to any instance.
- The query path never logs synchronously, never touches a database, and never performs per-packet heap allocation on the cache-hit path.
- [S-23] Key storage: DNSSEC private keys and TSIG secrets encrypted in PostgreSQL under a key-encryption key (env var or file), with a PKCS#11 HSM backend as an alternative in v1. Engines receive key material only over mTLS and hold it in memory only.
- Engine local state: last applied config snapshot on disk (without key material).
- Concurrent edits: two operators editing the same zone or policy get optimistic-concurrency conflicts, not lost writes.
- Zones: SOA serial arithmetic per RFC 1982 including wraparound; IXFR falls back to AXFR when history is missing; dynamic updates to signed zones are re-signed.
- CNAME/DNAME chains: followed to a bounded depth; loops → SERVFAIL.
- Every test that asserts "does not happen" first asserts the positive path in the same run, so a harness failure cannot pass a negative check.

From docs/architecture.md (binding):

- Go module `github.com/piwi3910/nexora`; generated Go protobuf package `github.com/piwi3910/nexora/gen/go/nexora/control/v1` (`controlv1`); goose migrations embedded from `mgmt/migrations/`; HTTP API under `/api/v1`, OpenAPI 3.1 at `mgmt/api/openapi.yaml`, errors `{"code": "...", "message": "..."}`, stale `revision` → 409 `conflict`; permissions keyed by operationId in `mgmt/internal/auth/permissions.go` (mirrored in `web/src/auth/permissions.ts`, checked by `pnpm lint`); every config mutation runs in one transaction: change rows → audit row → build snapshot → insert `config_versions` → `pg_notify('nexora_config', version)`.
- Env vars `NEXORA_KEK_FILE`, `NEXORA_PKCS11_MODULE`, `NEXORA_PKCS11_TOKEN_LABEL`, `NEXORA_PKCS11_PIN_FILE`. Secrets come from files, never from the database in plaintext.
- Engine hot path: config read via `ArcSwap<Runtime>::load()` once per packet; counters are per-worker `CachePadded<AtomicU64>`; snapshot applies atomically, is persisted to `state_dir/snapshot.binpb`, acked `Applied`/`Rejected`. Enforced by `cache_hit_path_does_not_allocate`.
- All builds/tests run in the dev pod: every command below runs from the repository root on the laptop as `scripts/dev-exec.sh <cmd>` (which syncs the tree first). `git` commands run on the laptop.
- Versions pinned for M4: Go `github.com/miekg/dns v1.1.73` and `github.com/klauspost/compress v1.20.0` (both already in `go.mod`), new `github.com/miekg/pkcs11 v1.1.2`; Rust: no new crates — `ring =0.17.14` (HMAC, SHA-1), `sha2 0.11`, `zeroize 1.9.0`, `data-encoding 2`, `zstd 0.14`, `rand 0.10`, `hickory-proto =0.26.3` (`dnssec-ring`, tests and slow paths only). Dev image already contains `named`, `delv`, `dig`, `nsupdate`, `ldns-compare-zones`, `softhsm2-util`, `/usr/lib/softhsm/libsofthsm2.so`.

Plan-wide conventions (from M3, still binding):

- Engine commands use the workspace form `cargo test --locked -p nexora-engine --lib <filter>`; the whole engine suite is `make engine-test`.
- Generated sources are regenerated with the pinned toolchains in the dev pod and copied back to the laptop: protobuf with `scripts/dev-exec.sh 'protoc -I proto --go_out=gen/go --go_opt=paths=source_relative --go-grpc_out=gen/go --go-grpc_opt=paths=source_relative proto/nexora/control/v1/control.proto'`, the HTTP server with `scripts/dev-exec.sh 'cd mgmt/api && oapi-codegen -config oapi-codegen.yaml openapi.yaml'`, each followed by a copy back, e.g. `kubectl --context kw -n nexora-dev exec -i deploy/toolbox -c toolbox -- tar -C /work/nexora -cf - gen/go/nexora/control/v1 mgmt/internal/api/gen.go | tar -xf -`; `web/src/api/schema.d.ts` with `pnpm --dir web run gen:api` on the laptop. "Regenerate the API" below means the last two.
- Test secrets are derived at run time (`base64.StdEncoding.EncodeToString([]byte("fixture-…"))`, `btoa("fixture-…")`, random bytes), never written as literals.

Consumed from M1-M3 (reconciled against 083f943 and M3 Task 5; if a name here and the code disagree, the code's name wins and the M4 behaviour stays):

- Engine (crate `nexora_engine`): `crate::proto` (prost, e.g. `proto::AuthZone`, enums without prefix); `wire::{parse_query, QueryView { id, flags, qname, key, qtype, qclass, question_end, opt }, ParseError::{TooShort, FormErr, NotImp, IsResponse}, write_error_reply, write_rcode_reply, RCODE_*}`; `edns::{ReplyOpt, write_opt, reply_limit, server_cookie, Transport::{Udp, Tcp, Dot, Doh, Doq}}`; `runtime::Runtime { version, acl, filter, policy, filter_hashes, filter_stats, cache, upstreams, telemetry, resolution }` with `Runtime::initial()` and `Runtime::build(s, blobs: &dyn snapshot::BlobSource, previous: Option<&Runtime>) -> Result<Runtime, SnapshotError>`; `snapshot::{validate, apply, BlobSource { fn read(&self, r: &BlobRef) -> Result<Vec<u8>, SnapshotError> }, DirBlobs, verify_blob, SnapshotError::{Invalid, Blob}}`; `snapshot_m3::validate_m3` (called from `snapshot::validate`); `control::{fetch_blobs, session, apply_snapshot}` (`session`'s `mpsc::channel::<EngineMessage>(16)` sender and `ServerMsg` match with the `RpzTsigKeys` arm); `server::{Shared { runtime, inflight, metrics, querylog, cookie_secret, engine_id, node_name, mgmt_channel, recursor }, Shared::new, Shared::with_recursor, WorkerCtx, handle_packet, FastOutcome::{Reply, Drop, Miss, Rewrite}, resolve_miss, Answerer, WorkerAnswerer, ClientInfo, stream::serve_dns_stream, udp::run_udp, rewrite::WorkerRewriteCtx}`; `recursor::RecursorState::sync` call sites; `recursor::rpz::tsig::{TsigAlg, TsigKey { name: Name, alg, secret: Zeroizing<Vec<u8>> }, sign_request, sign_response, TsigVerifier, extract_mac, TsigError, FUDGE}`; `telemetry::metrics::{Metrics { workers, .. }, WorkerCounters}`; `telemetry::querylog::{QueryRecord, CacheOutcome}`.
- Proto: `ServerMessage`/`EngineMessage` with `oneof msg`; `BlobRef { sha256, size, name }`; `TsigAlgorithm { NONE, HMAC_SHA256, HMAC_SHA512 }`; `GetBlob`.
- Mgmt: `store.Store { Pool }`, `(*Store).InTx`, `store.PutBlob(ctx, tx, data) (sha, size, err)`, `store.MapError`, `store.ErrNotFound`/`ErrConflict`; `snapshot.Mutate(ctx, st, cfg BuildConfig, a auth.Actor, fn func(tx pgx.Tx) (auth.Change, error)) (uint64, error)`, `snapshot.Build`, `snapshot.EnsureInitial`, `snapshot.Latest`; `auth.Actor { Type ("user"|"api_token"|"system"), ID, Name }`, `auth.Change { Action, TargetType, TargetID, Before, After }`, `auth.Permissions`; `secrets.{Box, LoadKEKFile, ErrUnconfigured, ErrKEKMismatch, ErrBackendUnavailable, WrapFileKEK, WrapPKCS11}`; `control.{Hub (RPZTsig, broadcast, loadKeys), subscriber (out, keys, offerKeys), Server (Connect send loop, receive, OnStats), RPZTsig}`; `blocklist.Fetcher.collectBlobs`; `api.{Deps (Store, Build, Secrets, ..), handlers.mutate, invalid, mapError, writeError, jsonOnly, maxBodyBytes}`; API test helpers `newAPI(t)`, `newAPIWith(t, adjust)`, `roleClients(t)`, `(*client).do`; `dnssecconf.{ValidateDomain, IsIPPort}`; `rpz.TsigPurpose`; `config.Config.KEKFile`.
- E2E harness (package `harness`): `New(t) *Env` (`T`, `Dir`), `(*Env).StartPostgres() *Postgres` (`URL`), `(*Env).InitCA() *CA`, `(*Env).StartMgmt(pg, ca, MgmtOptions{ExtraEnv []string}) *Mgmt` (`BaseURL`, `GRPCURL`), `(*Mgmt).SetupToken(t)`, `Bootstrap(t, env, token, baseURL) *API` (bearer admin), `(*API).Must(method, path, body, out, want)`, `(*API).Do(method, path, body, out) (int, error)` (path relative to `/api/v1`; fields `Base`, `HC`, `Bearer`), `(*API).CreateJoinToken()`, `(*API).LatestVersion()`, `(*API).WaitEngine(node, timeout, cond)`, `(*Env).StartManagedEngine(node, grpcURLs, token) *Engine` (`DNS` UDP+TCP address, `Metrics`, `StateDir`), `(*Engine).Metric(t, name, labels) float64`, `Query`/`MustQuery(t, server, name, qtype, QueryOpts{TCP, EDNSSize, Cookie, Timeout})`, `Eventually(t, timeout, func() error)`, `EventuallyTrue(t, timeout, func() bool, msg)`, `WriteKEK(t)`, `(*Env).FreePort()`, `(*Env).StartNamed(zoneName, zoneText) *Named` (`Addr`, `KeyName`, `KeySecretB64`), `(*Env).Start`, `RunPlaywright`; package `e2e` helper `waitLatestApplied(t, api, nodes...)`; kw helpers `loadKwEnv`, `kwLogin`, `kwWaitApplied`.
- GUI: `web/src/api/client.ts` (`api`, `unwrap`, `ApiError`), `components/ui/*` (Radix `Select`, `Dialog`, `Tabs`, `Table`), `web/e2e/fixtures.ts` (`test`, `expect`, `env`, `login`), nav test ids `nav-<route>`, destructive confirm `data-testid="confirm-delete"`; `TestGUICoverage` runs `web/e2e/screens/[01][0-9]-*.spec.ts` and counts recorded `/api/v1` browser requests per operation.

## Task 1: Contract additions and the NZF zone format

Files:

- `proto/nexora/control/v1/control.proto` — M4 messages and fields (modify)
- `gen/go/nexora/control/v1/*.pb.go` — regenerated
- `mgmt/internal/control/contract_m4_test.go` — field-number guard (package `control_test`, like `contract_m3_test.go`)
- `mgmt/internal/nzf/format.go` — NZF1 types, encoder, decoder
- `mgmt/internal/nzf/canon.go` — canonical name key and record ordering (RFC 4034 §6)
- `mgmt/internal/nzf/rr.go` — miekg `dns.RR` ↔ `nzf.Record`
- `mgmt/internal/nzf/blob.go` — zstd compression + SHA-256
- `mgmt/internal/nzf/nzf_test.go` — unit + golden tests
- `mgmt/internal/zone/serial.go`, `mgmt/internal/zone/serial_test.go` — RFC 1982
- `testdata/nzf/basic-full.nzf`, `basic-delta.nzf`, `basic-after.nzf`, `big-full.nzf` — goldens shared with the engine

`go.mod` needs no change: `github.com/miekg/dns v1.1.73` and `github.com/klauspost/compress v1.20.0` are already required (M3, M1).

Interfaces:

```go
package nzf
type Record struct { Owner []byte; Type, Class uint16; TTL uint32; RData []byte }
type Image struct { Origin []byte; Serial uint32; Records []Record }
type Delta struct { Origin []byte; FromSerial, ToSerial uint32; Deleted, Added []Record }
func EncodeFull(img Image) ([]byte, error)
func EncodeDelta(d Delta) ([]byte, error)
func Decode(raw []byte) (kind byte, img *Image, d *Delta, err error)
func CanonicalKey(wire []byte) []byte
func SortRecords(rs []Record)
func FromRR(rr dns.RR) (Record, error)
func ToRR(r Record) (dns.RR, error)
func Compress(raw []byte) (data []byte, sha256hex string, err error)
func Decompress(data []byte, maxSize int) ([]byte, error)
const KindFull, KindDelta byte = 1, 2

package zone
func SerialLess(a, b uint32) bool
func SerialNext(s uint32) uint32
```

Proto messages `AuthZone`, `ZoneDelta`, `TransferPolicy`, `NotifyTarget`, `TsigSecret`, `KeyMaterial`, `NotifyReceived`, `UpdateRequest`, `UpdateResult` and enum `AuthZoneKind`; images and deltas reuse M1's `BlobRef`; M3's `TsigAlgorithm` gains `TSIG_ALGORITHM_HMAC_SHA384 = 3`; fields `ConfigSnapshot.auth_zones = 200`, `ServerMessage.key_material = 200`, `ServerMessage.update_result = 201`, `EngineMessage.notify_received = 200`, `EngineMessage.update_request = 201`. No field in `control.proto` uses 200-299 before this task.

NZF1 layout (all integers big-endian, names uncompressed wire format):

```
header:
  magic        4   "NZF1"
  kind         u8  1 = full image, 2 = delta
  reserved     u8  0
  origin_len   u8
  origin       origin_len octets (lowercase)
  serial       u32 full: image serial; delta: to_serial
  from_serial  u32 full: 0; delta: from_serial
  count_a      u32 full: number of records; delta: number of deleted records
  count_b      u32 full: 0; delta: number of added records
record (count_a + count_b times):
  owner_len    u8
  owner        owner_len octets (case preserved, absolute, within origin)
  type         u16
  class        u16 (1)
  ttl          u32
  rdlen        u16
  rdata        rdlen octets (uncompressed)
ordering:
  full: all records (SOA included) by (CanonicalKey(owner), type, rdata bytes)
  delta: deleted[0] = SOA at from_serial, then sorted; added[0] = SOA at to_serial, then sorted
blob:
  zstd frame (checksum flag on) of the NZF bytes; identified by lowercase hex SHA-256 of the compressed bytes
```

- [ ] Write the failing field-number guard `mgmt/internal/control/contract_m4_test.go`:

```go
package control_test

import (
	"testing"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestM4ContractFieldNumbers(t *testing.T) {
	snap := (&controlv1.ConfigSnapshot{}).ProtoReflect().Descriptor()
	srv := (&controlv1.ServerMessage{}).ProtoReflect().Descriptor()
	eng := (&controlv1.EngineMessage{}).ProtoReflect().Descriptor()
	cases := []struct {
		msg   protoreflect.MessageDescriptor
		field protoreflect.Name
		num   protoreflect.FieldNumber
	}{
		{snap, "auth_zones", 200},
		{srv, "key_material", 200},
		{srv, "update_result", 201},
		{eng, "notify_received", 200},
		{eng, "update_request", 201},
	}
	for _, c := range cases {
		f := c.msg.Fields().ByName(c.field)
		if f == nil {
			t.Fatalf("%s.%s missing", c.msg.FullName(), c.field)
		}
		if f.Number() != c.num {
			t.Errorf("%s.%s = %d, want %d", c.msg.FullName(), c.field, f.Number(), c.num)
		}
	}
	sha384 := controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA384.Descriptor().Values().ByName("TSIG_ALGORITHM_HMAC_SHA384")
	if sha384 == nil || sha384.Number() != 3 {
		t.Fatalf("TsigAlgorithm HMAC_SHA384 = %v, want 3", sha384)
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/control/ -run TestM4ContractFieldNumbers -count=1` — expect FAIL to compile with "undefined: controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA384".
- [ ] Add `TSIG_ALGORITHM_HMAC_SHA384 = 3;` to the existing `enum TsigAlgorithm`, append the M4 section to `control.proto`, and add the five fields to `ConfigSnapshot` / the envelopes' `oneof msg`:

```proto
// ---- M4 (authoritative). Field numbers 200-299 in ConfigSnapshot and in both
// Connect stream envelopes are reserved for M4.

enum AuthZoneKind {
  AUTH_ZONE_KIND_UNSPECIFIED = 0;
  AUTH_ZONE_KIND_PRIMARY = 1;
  AUTH_ZONE_KIND_SECONDARY = 2;
}

// Image and delta blobs are M1 BlobRefs: sha256 = lowercase hex of the zstd-compressed NZF1 bytes,
// size = compressed octets, name = "<zone>@<serial>" (informational). Fetched with GetBlob.
message ZoneDelta {
  uint32 from_serial = 1;
  uint32 to_serial = 2;
  BlobRef blob = 3;
}

message TransferPolicy {
  repeated string allow_cidrs = 1; // empty = transfers refused
  string tsig_key = 2;             // absolute lowercase key name; empty = no TSIG required
}

message NotifyTarget {
  string address = 1; // "ip:port"
  string tsig_key = 2;
}

message AuthZone {
  string name = 1;               // absolute, lowercase, trailing dot
  AuthZoneKind kind = 2;
  uint32 serial = 3;
  BlobRef image = 4;             // full image at image_serial
  uint32 image_serial = 5;
  repeated ZoneDelta deltas = 6; // contiguous chain; last to_serial == serial
  uint32 image_delta_offset = 7; // deltas[image_delta_offset..] apply on top of image
  TransferPolicy transfer = 8;
  repeated NotifyTarget notify = 9;
  repeated string primaries = 10;       // secondary: "ip:port" accepted as NOTIFY sources
  repeated string update_tsig_keys = 11; // key names allowed to UPDATE; empty = updates refused
  bool expired = 12;                    // secondary past SOA expire: SERVFAIL
}

message TsigSecret {
  string name = 1;             // absolute, lowercase
  TsigAlgorithm algorithm = 2; // HMAC_SHA256 | HMAC_SHA384 | HMAC_SHA512
  bytes secret = 3;
}

// The complete TSIG key set, like RpzTsigKeys: sent on Connect before the snapshot and whenever the
// set changes. Only on the Connect stream; held in engine memory; never part of ConfigSnapshot,
// config_versions or state_dir.
message KeyMaterial {
  repeated TsigSecret tsig_keys = 1;
}

message NotifyReceived {
  string zone = 1;
  string source = 2; // "ip:port"
  uint32 serial = 3;
  bool has_serial = 4;
}

message UpdateRequest {
  string request_id = 1;
  string zone = 2;
  string client = 3;   // "ip:port"
  bytes message = 4;   // complete UPDATE wire message including its TSIG RR
  string tsig_key = 5; // key that verified on the engine
}

message UpdateResult {
  string request_id = 1;
  uint32 rcode = 2;
  string detail = 3;
}
```

```proto
// in message ConfigSnapshot:
  repeated AuthZone auth_zones = 200;
// in ServerMessage oneof msg:
    KeyMaterial key_material = 200;
    UpdateResult update_result = 201;
// in EngineMessage oneof msg:
    NotifyReceived notify_received = 200;
    UpdateRequest update_request = 201;
```

- [ ] Regenerate the Go protobuf code (plan-wide convention, protoc command plus copy back), then `scripts/dev-exec.sh go test ./mgmt/internal/control/ -run TestM4ContractFieldNumbers -count=1` — expect PASS; run `scripts/dev-exec.sh cargo build --locked -p nexora-engine` — expect success (new prost fields default to empty; M3's matches on `TsigAlgorithm` in `snapshot_m3.rs` and `recursor/rpz/manager.rs` have catch-all arms, so RPZ keeps accepting only SHA-256/512).
- [ ] Write the failing `mgmt/internal/zone/serial_test.go`:

```go
package zone

import "testing"

func TestSerialArithmeticRFC1982(t *testing.T) {
	cases := []struct {
		a, b uint32
		less bool
	}{
		{1, 2, true},
		{2, 1, false},
		{7, 7, false},
		{4294967295, 0, true},
		{0, 4294967295, false},
		{4294967295, 2147483646, true},
		{0, 2147483648, false}, // distance exactly 2^31 is undefined: not less either way
		{2147483648, 0, false},
	}
	for _, c := range cases {
		if got := SerialLess(c.a, c.b); got != c.less {
			t.Errorf("SerialLess(%d, %d) = %v, want %v", c.a, c.b, got, c.less)
		}
	}
	if SerialNext(4294967295) != 0 {
		t.Fatalf("SerialNext must wrap 4294967295 to 0")
	}
	if !SerialLess(4294967295, SerialNext(4294967295)) {
		t.Fatalf("the wrapped serial must be RFC 1982-greater")
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/zone/ -run TestSerialArithmeticRFC1982 -count=1` — expect FAIL with "undefined: SerialLess".
- [ ] Implement `mgmt/internal/zone/serial.go`:

```go
package zone

// SerialLess reports a < b under RFC 1982 with SERIAL_BITS = 32.
func SerialLess(a, b uint32) bool {
	if a == b {
		return false
	}
	const half = uint32(1) << 31
	return (a < b && b-a < half) || (a > b && a-b > half)
}

// SerialNext is the next primary serial; it wraps 4294967295 -> 0.
func SerialNext(s uint32) uint32 { return s + 1 }
```

- [ ] Run the serial test again — expect PASS.
- [ ] Write the failing `mgmt/internal/nzf/nzf_test.go`:

```go
package nzf

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/miekg/dns"
)

var update = flag.Bool("update", false, "rewrite testdata/nzf goldens")

func golden(name string) string { return filepath.Join("..", "..", "..", "testdata", "nzf", name) }

// basicZone is shared with the engine tests (engine/src/authoritative/*_tests.rs)
// and with the DNSSEC signer goldens (Task 12). Do not reorder or edit without
// regenerating every golden.
func basicZone(serial uint32, variant string) []string {
	rrs := []string{
		fmt.Sprintf("example.test. 3600 IN SOA ns1.example.test. hostmaster.example.test. %d 7200 3600 1209600 300", serial),
		"example.test. 3600 IN NS ns1.example.test.",
		"example.test. 3600 IN NS ns2.example.test.",
		"example.test. 3600 IN MX 10 mail.example.test.",
		"ns1.example.test. 3600 IN A 192.0.2.1",
		"ns2.example.test. 3600 IN AAAA 2001:db8::2",
		"mail.example.test. 3600 IN A 192.0.2.25",
		"www.example.test. 300 IN A 192.0.2.10",
		"alias.example.test. 300 IN CNAME www.example.test.",
		"loop1.example.test. 300 IN CNAME loop2.example.test.",
		"loop2.example.test. 300 IN CNAME loop1.example.test.",
		"*.wild.example.test. 300 IN TXT \"wildcard\"",
		"a.b.c.example.test. 300 IN A 192.0.2.20",
		"sub.example.test. 3600 IN NS ns.sub.example.test.",
		"sub.example.test. 3600 IN DS 60485 13 2 D4B7D520E7BB5F0F67674A0CCEB1E3E0614B93C4F9E99B8383F6A1E4469DA50A",
		"ns.sub.example.test. 3600 IN A 192.0.2.53",
		"insecure.example.test. 3600 IN NS ns.insecure.example.test.",
		"ns.insecure.example.test. 3600 IN A 192.0.2.54",
		"dn.example.test. 300 IN DNAME example.net.",
		"_sip._tcp.example.test. 300 IN SRV 10 60 5060 sip.example.test.",
	}
	for i := 0; i < 40; i++ {
		rrs = append(rrs, fmt.Sprintf("big.example.test. 300 IN TXT \"%03d%s\"", i, bytes.Repeat([]byte("x"), 100)))
	}
	if variant == "before" {
		rrs = append(rrs, "www.example.test. 300 IN A 192.0.2.11")
	} else {
		rrs = append(rrs, "new.example.test. 300 IN A 192.0.2.12")
	}
	return rrs
}

func records(t *testing.T, lines []string) []Record {
	t.Helper()
	var out []Record
	for _, l := range lines {
		rr, err := dns.NewRR(l)
		if err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		r, err := FromRR(rr)
		if err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		out = append(out, r)
	}
	return out
}

func wire(t *testing.T, name string) []byte {
	t.Helper()
	b := make([]byte, 256)
	n, err := dns.PackDomainName(name, b, 0, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	return b[:n]
}

func checkGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(golden(name), got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden(name))
	if err != nil {
		t.Fatalf("read golden %s (run with -update once): %v", name, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s differs from golden (%d vs %d bytes)", name, len(got), len(want))
	}
}

func TestCanonicalOrderRFC4034(t *testing.T) {
	ordered := []string{"example.", "a.example.", "yljkjljk.a.example.", "Z.a.example.",
		"zABC.a.EXAMPLE.", "z.example.", "\\001.z.example.", "*.z.example.", "\\200.z.example."}
	for i := 1; i < len(ordered); i++ {
		a, b := CanonicalKey(wire(t, ordered[i-1])), CanonicalKey(wire(t, ordered[i]))
		if bytes.Compare(a, b) >= 0 {
			t.Fatalf("%s must sort before %s", ordered[i-1], ordered[i])
		}
	}
}

func TestFullImageRoundTripAndGolden(t *testing.T) {
	img := Image{Origin: wire(t, "example.test."), Serial: 2026091301, Records: records(t, basicZone(2026091301, "before"))}
	raw, err := EncodeFull(img)
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "basic-full.nzf", raw)
	kind, back, _, err := Decode(raw)
	if err != nil || kind != KindFull {
		t.Fatalf("decode: kind=%d err=%v", kind, err)
	}
	if back.Serial != 2026091301 || len(back.Records) != len(img.Records) {
		t.Fatalf("round trip: serial=%d records=%d", back.Serial, len(back.Records))
	}
	for i := 1; i < len(back.Records); i++ {
		if bytes.Compare(CanonicalKey(back.Records[i-1].Owner), CanonicalKey(back.Records[i].Owner)) > 0 {
			t.Fatalf("records not in canonical order at %d", i)
		}
	}
}

func TestDeltaGoldenAndAfterImage(t *testing.T) {
	d := Delta{
		Origin: wire(t, "example.test."), FromSerial: 2026091301, ToSerial: 2026091302,
		Deleted: records(t, []string{basicZone(2026091301, "before")[0], "www.example.test. 300 IN A 192.0.2.11"}),
		Added:   records(t, []string{basicZone(2026091302, "after")[0], "new.example.test. 300 IN A 192.0.2.12"}),
	}
	raw, err := EncodeDelta(d)
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "basic-delta.nzf", raw)
	after, err := EncodeFull(Image{Origin: wire(t, "example.test."), Serial: 2026091302, Records: records(t, basicZone(2026091302, "after"))})
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "basic-after.nzf", after)
	var big []string
	big = append(big, "big.test. 3600 IN SOA ns1.big.test. hostmaster.big.test. 1 7200 3600 1209600 300", "big.test. 3600 IN NS ns1.big.test.")
	for i := 0; i < 2000; i++ {
		big = append(big, fmt.Sprintf("h%04d.big.test. 300 IN A 198.51.%d.%d", i, i/256, i%256))
	}
	bigRaw, err := EncodeFull(Image{Origin: wire(t, "big.test."), Serial: 1, Records: records(t, big)})
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "big-full.nzf", bigRaw)
}

func TestDecodeRejectsMalformed(t *testing.T) {
	raw, _ := EncodeFull(Image{Origin: wire(t, "example.test."), Serial: 1, Records: records(t, basicZone(1, "before"))})
	for name, mutate := range map[string]func([]byte) []byte{
		"truncated":     func(b []byte) []byte { return b[:len(b)-1] },
		"trailing":      func(b []byte) []byte { return append(b, 0) },
		"bad magic":     func(b []byte) []byte { b[0] = 'X'; return b },
		"huge count":    func(b []byte) []byte { o := 7 + int(b[6]) + 8; b[o], b[o+1] = 0xff, 0xff; return b },
		"compressed owner": func(b []byte) []byte { o := 7 + int(b[6]) + 16; b[o+1] = 0xc0; return b },
	} {
		c := mutate(append([]byte(nil), raw...))
		if _, _, _, err := Decode(c); err == nil {
			t.Errorf("%s: Decode accepted malformed input", name)
		}
	}
}

func TestOutOfZoneOwnerRefused(t *testing.T) {
	_, err := EncodeFull(Image{Origin: wire(t, "example.test."), Serial: 1, Records: records(t, []string{"www.example.org. 300 IN A 192.0.2.1"})})
	if err == nil {
		t.Fatal("record outside origin accepted")
	}
}

func TestCompressIsContentAddressed(t *testing.T) {
	raw, _ := EncodeFull(Image{Origin: wire(t, "example.test."), Serial: 1, Records: records(t, basicZone(1, "before"))})
	data, sum, err := Compress(raw)
	if err != nil || len(sum) != 64 {
		t.Fatalf("compress: %v %q", err, sum)
	}
	back, err := Decompress(data, 1<<30)
	if err != nil || !bytes.Equal(back, raw) {
		t.Fatalf("decompress: %v", err)
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/nzf/ -count=1` — expect FAIL with "undefined: FromRR".
- [ ] Implement `canon.go`:

```go
package nzf

import (
	"bytes"
	"sort"
)

// CanonicalKey maps an uncompressed wire name to a byte string whose bytewise
// order is the RFC 4034 §6.1 canonical order: labels from the root, lowercase,
// each label terminated by 0x00; label octets 0x00 and 0x01 are escaped to
// 0x01 0x01 and 0x01 0x02 so a shorter label sorts before its extensions.
func CanonicalKey(wire []byte) []byte {
	var starts [128]int
	n := 0
	for i := 0; i < len(wire) && wire[i] != 0; i += int(wire[i]) + 1 {
		starts[n] = i
		n++
	}
	key := make([]byte, 0, len(wire)+n)
	for k := n - 1; k >= 0; k-- {
		o := starts[k]
		for _, b := range wire[o+1 : o+1+int(wire[o])] {
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			switch b {
			case 0:
				key = append(key, 1, 1)
			case 1:
				key = append(key, 1, 2)
			default:
				key = append(key, b)
			}
		}
		key = append(key, 0)
	}
	return key
}

func SortRecords(rs []Record) {
	keys := make([][]byte, len(rs))
	for i := range rs {
		keys[i] = CanonicalKey(rs[i].Owner)
	}
	idx := make([]int, len(rs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ra, rb := rs[idx[a]], rs[idx[b]]
		if c := bytes.Compare(keys[idx[a]], keys[idx[b]]); c != 0 {
			return c < 0
		}
		if ra.Type != rb.Type {
			return ra.Type < rb.Type
		}
		return bytes.Compare(ra.RData, rb.RData) < 0
	})
	sorted := make([]Record, len(rs))
	for i, j := range idx {
		sorted[i] = rs[j]
	}
	copy(rs, sorted)
}
```

- [ ] Implement `rr.go`: `FromRR` packs with `dns.PackRR(rr, buf, 0, nil, false)` into a 65535+255-octet buffer, measures the owner with `dns.PackDomainName(rr.Header().Name, tmp, 0, nil, false)`, and slices owner / type / class / ttl / rdlen / rdata from the packed bytes; `ToRR` re-assembles the wire RR and calls `dns.UnpackRR(b, 0)`.
- [ ] Implement `format.go`. Encoder: validate origin (non-root, ≤ 255 octets); for each record validate owner is `origin` or ends with `origin` at a label boundary (case-insensitive), class 1, rdata ≤ 65535; sort full images with `SortRecords`; for deltas require `Deleted[0]` and `Added[0]` to be SOA at the origin, keep them first and sort the rest. Decoder: check magic `NZF1`, kind ∈ {1,2}, reserved 0; reject `count_a + count_b > remaining/11`; validate every owner (no octet ≥ 0x40 as a label length, labels ≤ 63, total ≤ 255, within origin); reject `rdlen` beyond the buffer and any trailing bytes; for kind 1 require exactly one SOA whose owner equals origin and whose serial field equals the header serial.
      Built (as implemented): the encoder applies the same checks as the decoder (one apex SOA carrying the header serial in full images; delta `Deleted[0]`/`Added[0]` SOA serials equal `FromSerial`/`ToSerial`) so it never emits a blob the decoder refuses; the decoder also rejects full images with non-zero `from_serial`/`count_b` and deltas whose leading SOAs are missing or disagree with the header; all decode errors wrap `nzf.ErrMalformed`. Adding the envelope `oneof` variants makes `control.rs`'s `ServerMsg` match non-exhaustive, so Task 1 adds one no-op arm `Some(ServerMsg::KeyMaterial(_) | ServerMsg::UpdateResult(_)) => {}` (replaced in Task 4).
- [ ] Implement `blob.go` with `zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderCRC(true))`, `EncodeAll`, `sha256.Sum256` over the compressed bytes, `hex.EncodeToString`; `Decompress` uses `zstd.NewReader(nil, zstd.WithDecoderMaxMemory(uint64(maxSize)))` and `DecodeAll`.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/nzf/ -count=1 -update` once to write the four goldens, then `scripts/dev-exec.sh go test ./mgmt/internal/nzf/ -count=1` — expect PASS.
- [ ] Commit: `git add proto gen mgmt/internal/control/contract_m4_test.go mgmt/internal/nzf mgmt/internal/zone/serial*.go testdata/nzf && git commit -m "feat(m4): control contract for authoritative zones and NZF1 zone format"`.

## Task 2: Engine NZF decoder, in-memory zone model, delta application

Files:

- `engine/src/lib.rs` — declare `pub mod authoritative;` (modify)
- `engine/src/authoritative/mod.rs` — module declarations and RR type constants
- `engine/src/authoritative/name.rs` — label offsets, lowercase copy, canonical key, `from_ascii` test helper
- `engine/src/authoritative/nzf.rs` — zero-copy NZF1 parser + zstd decompression
- `engine/src/authoritative/zone.rs` — `Zone`, `Node`, `RRset`, build, finalize, apply delta
- `engine/src/authoritative/zone_tests.rs` — unit tests against `testdata/nzf/*`

Interfaces:

```rust
// authoritative/mod.rs
pub const T_A: u16 = 1; pub const T_NS: u16 = 2; pub const T_CNAME: u16 = 5; pub const T_SOA: u16 = 6;
pub const T_PTR: u16 = 12; pub const T_MX: u16 = 15; pub const T_TXT: u16 = 16; pub const T_AAAA: u16 = 28;
pub const T_LOC: u16 = 29; pub const T_SRV: u16 = 33; pub const T_NAPTR: u16 = 35; pub const T_DNAME: u16 = 39;
pub const T_OPT: u16 = 41; pub const T_DS: u16 = 43; pub const T_SSHFP: u16 = 44; pub const T_RRSIG: u16 = 46;
pub const T_NSEC: u16 = 47; pub const T_DNSKEY: u16 = 48; pub const T_NSEC3: u16 = 50; pub const T_NSEC3PARAM: u16 = 51;
pub const T_TLSA: u16 = 52; pub const T_CDS: u16 = 59; pub const T_CDNSKEY: u16 = 60; pub const T_SVCB: u16 = 64;
pub const T_HTTPS: u16 = 65; pub const T_TSIG: u16 = 250; pub const T_IXFR: u16 = 251; pub const T_AXFR: u16 = 252;
pub const T_ANY: u16 = 255; pub const T_CAA: u16 = 257;

// authoritative/name.rs
pub fn label_offsets(wire: &[u8], out: &mut [u16; 128]) -> usize;
pub fn lowercase_into<'b>(wire: &[u8], buf: &'b mut [u8; 255]) -> &'b [u8];
pub fn canon_key(wire: &[u8], out: &mut Vec<u8>);
pub fn is_subdomain(child: &[u8], parent: &[u8]) -> bool; // case-insensitive, label boundary
pub fn from_ascii(s: &str) -> Option<Vec<u8>>;             // no escapes; tests and config only

// authoritative/nzf.rs
pub struct RecordRef<'a> { pub owner: &'a [u8], pub rtype: u16, pub class: u16, pub ttl: u32, pub rdata: &'a [u8] }
pub enum Kind { Full, Delta }
pub struct Parsed<'a> { pub kind: Kind, pub origin: &'a [u8], pub serial: u32, pub from_serial: u32, pub a: Vec<RecordRef<'a>>, pub b: Vec<RecordRef<'a>> }
pub enum NzfError { Truncated, BadMagic, BadKind, BadName, OutOfZone, BadRecord, Trailing, TooLarge, Zstd(String) } // BadRecord: class != 1, or a full image with delta header fields
pub fn parse(buf: &[u8]) -> Result<Parsed<'_>, NzfError>;
pub fn decompress(blob: &[u8], max_size: usize) -> Result<Vec<u8>, NzfError>;

// authoritative/zone.rs
pub struct RRset { pub rtype: u16, pub ttl: u32, pub rdata: Vec<Box<[u8]>>, pub sigs: Vec<Box<[u8]>> }
pub struct Node { pub owner: Box<[u8]>, pub rrsets: Vec<RRset>, pub flags: u8 }
pub const NODE_CUT: u8 = 1; pub const NODE_WILDCARD_CHILD: u8 = 2; pub const NODE_BELOW_CUT: u8 = 4;
pub struct OwnedRecord { pub owner: Box<[u8]>, pub rtype: u16, pub ttl: u32, pub rdata: Box<[u8]> }
pub struct DeltaRecords { pub from_serial: u32, pub to_serial: u32, pub deleted: Vec<OwnedRecord>, pub added: Vec<OwnedRecord> }
pub enum ZoneError { NotFull, NotDelta, NoSoa, BadSoa, OriginMismatch, SerialMismatch { have: u32, delta_from: u32 }, DeleteAbsent, BadRrsig } // BadSoa: SOA RDATA malformed or serial != header serial
impl Zone {
    pub fn from_image(p: &nzf::Parsed<'_>) -> Result<Zone, ZoneError>;
    pub fn apply(&self, d: &nzf::Parsed<'_>) -> Result<Zone, ZoneError>;
    pub fn origin(&self) -> &[u8]; pub fn origin_labels(&self) -> usize; pub fn serial(&self) -> u32;
    pub fn apex(&self) -> &Node; pub fn soa_rdata(&self) -> &[u8];
    pub fn node(&self, wire: &[u8]) -> Option<&Node>;
    pub fn node_by_key(&self, key: &[u8]) -> Option<&Node>; // canonical key lookup (Task 3 lookup)
    pub fn records_sorted(&self) -> Vec<OwnedRecord>; // canonical order, RRSIGs as records
    pub fn is_signed(&self) -> bool;                    // apex has DNSKEY
    pub fn nsec_covering(&self, key: &[u8]) -> Option<&Node>;
    pub fn nsec3_param(&self) -> Option<&[u8]>;
    pub fn nsec3_node(&self, hash: &[u8; 20]) -> Option<&Node>;
    pub fn nsec3_covering(&self, hash: &[u8; 20]) -> Option<&Node>;
    pub fn nodes_below(&self, key: &[u8]) -> impl Iterator<Item = &Node>;
    pub deltas: Vec<std::sync::Arc<DeltaRecords>>, // IXFR history, set by the loader (Task 4)
    pub expired: bool,                              // set by the loader
}
impl Node { pub fn get(&self, rtype: u16) -> Option<&RRset>; pub fn is_cut(&self) -> bool; pub fn is_empty(&self) -> bool; }
```

- [ ] Create `engine/src/authoritative/mod.rs` with the constants above, `pub mod name; pub mod nzf; pub mod zone;` and `#[cfg(test)] mod zone_tests;`, add `pub mod authoritative;` to `lib.rs`, and write the failing `zone_tests.rs`:

```rust
use super::name::from_ascii;
use super::nzf::{self, NzfError};
use super::zone::{Zone, ZoneError, NODE_BELOW_CUT, NODE_WILDCARD_CHILD};

pub(crate) const FULL: &[u8] = include_bytes!(concat!(env!("CARGO_MANIFEST_DIR"), "/../testdata/nzf/basic-full.nzf"));
pub(crate) const DELTA: &[u8] = include_bytes!(concat!(env!("CARGO_MANIFEST_DIR"), "/../testdata/nzf/basic-delta.nzf"));
pub(crate) const AFTER: &[u8] = include_bytes!(concat!(env!("CARGO_MANIFEST_DIR"), "/../testdata/nzf/basic-after.nzf"));

fn w(name: &str) -> Vec<u8> {
    from_ascii(name).unwrap()
}

#[test]
fn full_image_builds_empty_non_terminals_cuts_and_wildcards() {
    let z = Zone::from_image(&nzf::parse(FULL).unwrap()).unwrap();
    assert_eq!(z.serial(), 2026091301);
    assert_eq!(z.origin(), &w("example.test.")[..]);
    assert!(z.node(&w("b.c.example.test.")).unwrap().is_empty(), "b.c is an empty non-terminal");
    assert!(z.node(&w("c.example.test.")).unwrap().is_empty(), "c is an empty non-terminal");
    assert!(z.node(&w("sub.example.test.")).unwrap().is_cut());
    assert!(!z.apex().is_cut(), "apex NS is not a cut");
    assert_ne!(z.node(&w("ns.sub.example.test.")).unwrap().flags & NODE_BELOW_CUT, 0);
    assert_ne!(z.node(&w("wild.example.test.")).unwrap().flags & NODE_WILDCARD_CHILD, 0);
    assert!(z.node(&w("nothere.example.test.")).is_none());
    assert!(z.node(&w("WWW.EXAMPLE.TEST.")).is_some(), "lookup is case-insensitive");
    assert!(!z.is_signed());
}

#[test]
fn delta_application_equals_after_image() {
    let base = Zone::from_image(&nzf::parse(FULL).unwrap()).unwrap();
    let applied = base.apply(&nzf::parse(DELTA).unwrap()).unwrap();
    let after = Zone::from_image(&nzf::parse(AFTER).unwrap()).unwrap();
    assert_eq!(applied.serial(), 2026091302);
    assert_eq!(applied.records_sorted(), after.records_sorted());
    assert_eq!(base.serial(), 2026091301, "apply must not mutate the source zone");
    assert!(base.node(&w("new.example.test.")).is_none());
}

#[test]
fn delta_from_wrong_serial_is_rejected() {
    let base = Zone::from_image(&nzf::parse(FULL).unwrap()).unwrap();
    let once = base.apply(&nzf::parse(DELTA).unwrap()).unwrap();
    match once.apply(&nzf::parse(DELTA).unwrap()) {
        Err(ZoneError::SerialMismatch { have, delta_from }) => {
            assert_eq!((have, delta_from), (2026091302, 2026091301))
        }
        other => panic!("expected SerialMismatch, got {:?}", other.map(|z| z.serial())),
    }
}

#[test]
fn parser_rejects_malformed_blobs() {
    let mut t = FULL.to_vec();
    t.truncate(t.len() - 1);
    assert!(matches!(nzf::parse(&t), Err(NzfError::Truncated)));
    let mut trailing = FULL.to_vec();
    trailing.push(0);
    assert!(matches!(nzf::parse(&trailing), Err(NzfError::Trailing)));
    let mut magic = FULL.to_vec();
    magic[0] = b'X';
    assert!(matches!(nzf::parse(&magic), Err(NzfError::BadMagic)));
    let mut comp = FULL.to_vec();
    let first_owner = 7 + comp[6] as usize + 16;
    comp[first_owner + 1] = 0xc0;
    assert!(matches!(nzf::parse(&comp), Err(NzfError::BadName)));
    assert!(nzf::decompress(b"not zstd", 1 << 20).is_err());
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib authoritative::zone_tests` — expect FAIL with "unresolved import".
- [ ] Implement `name.rs`. `canon_key` is the Rust twin of Go `nzf.CanonicalKey` (Task 1) and must produce identical bytes:

```rust
pub fn label_offsets(wire: &[u8], out: &mut [u16; 128]) -> usize {
    let (mut i, mut n) = (0usize, 0usize);
    while i < wire.len() && wire[i] != 0 && n < 128 {
        out[n] = i as u16;
        n += 1;
        i += wire[i] as usize + 1;
    }
    n
}

pub fn canon_key(wire: &[u8], out: &mut Vec<u8>) {
    let mut offs = [0u16; 128];
    let n = label_offsets(wire, &mut offs);
    for k in (0..n).rev() {
        let o = offs[k] as usize;
        for &b in &wire[o + 1..o + 1 + wire[o] as usize] {
            match b.to_ascii_lowercase() {
                0 => out.extend_from_slice(&[1, 1]),
                1 => out.extend_from_slice(&[1, 2]),
                c => out.push(c),
            }
        }
        out.push(0);
    }
}

pub fn is_subdomain(child: &[u8], parent: &[u8]) -> bool {
    if parent.len() > child.len() {
        return false;
    }
    let start = child.len() - parent.len();
    if !child[start..].eq_ignore_ascii_case(parent) {
        return false;
    }
    let mut offs = [0u16; 128];
    let n = label_offsets(child, &mut offs);
    start == child.len() - 1 || offs[..n].iter().any(|&o| o as usize == start)
}
```

- [ ] Implement `nzf.rs` to the Task 1 layout: fixed-size header reads with `Truncated` on short input; origin and every owner validated by a walk that rejects length octets ≥ 0x40 (`BadName`), labels > 63, names > 255 octets or running past their declared length; owners checked with `is_subdomain(owner, origin)` (`OutOfZone`); `count_a + count_b` bounded by `remaining / 11` (`TooLarge`); leftover bytes → `Trailing`. `decompress` streams through `zstd::stream::read::Decoder` limited to `max_size + 1` octets (`bulk::decompress` would allocate `max_size` up front), mapping errors to `Zstd` and oversize output to `TooLarge`. Built (as implemented): class != 1 → `BadRecord`; the cargo-fuzz target `engine/fuzz/fuzz_targets/nzf_parse.rs` (seed corpus `engine/fuzz/corpus/nzf_parse/`) runs `parse`, `Zone::from_image` and `apply` on arbitrary bytes.
- [ ] Implement `zone.rs`. Storage: `nodes: BTreeMap<Box<[u8]>, Arc<Node>>` keyed by `canon_key(owner)`, `nsec: BTreeSet<Box<[u8]>>` (keys of nodes that carry NSEC), `nsec3: BTreeMap<[u8; 20], Box<[u8]>>` (hash decoded from the base32hex first label → node key), `nsec3param: Option<Box<[u8]>>`. `Node::get` ignores RRsets whose `rdata` is empty (signature-only placeholders). Insert and finalize:

```rust
fn insert(nodes: &mut BTreeMap<Box<[u8]>, Arc<Node>>, r: &RecordRef<'_>, key: &mut Vec<u8>) -> Result<(), ZoneError> {
    key.clear();
    canon_key(r.owner, key);
    let slot = nodes
        .entry(key.clone().into_boxed_slice())
        .or_insert_with(|| Arc::new(Node { owner: r.owner.into(), rrsets: Vec::new(), flags: 0 }));
    let node = Arc::make_mut(slot);
    let (rtype, is_sig) = if r.rtype == T_RRSIG {
        if r.rdata.len() < 18 {
            return Err(ZoneError::BadRrsig);
        }
        (u16::from_be_bytes([r.rdata[0], r.rdata[1]]), true)
    } else {
        (r.rtype, false)
    };
    let idx = match node.rrsets.iter().position(|s| s.rtype == rtype) {
        Some(i) => i,
        None => {
            let at = node.rrsets.partition_point(|s| s.rtype < rtype);
            node.rrsets.insert(at, RRset { rtype, ttl: r.ttl, rdata: Vec::new(), sigs: Vec::new() });
            at
        }
    };
    let set = &mut node.rrsets[idx];
    let list = if is_sig { &mut set.sigs } else { set.ttl = r.ttl; &mut set.rdata };
    if !list.iter().any(|d| &**d == r.rdata) {
        list.push(r.rdata.into());
    }
    Ok(())
}

fn finalize(&mut self) -> Result<(), ZoneError> {
    self.nodes.retain(|_, n| n.rrsets.iter().any(|s| !s.rdata.is_empty() || !s.sigs.is_empty()));
    let origin_labels = self.origin_labels;
    let owners: Vec<Box<[u8]>> = self.nodes.values().map(|n| n.owner.clone()).collect();
    let mut key = Vec::with_capacity(512);
    for owner in owners {
        let mut offs = [0u16; 128];
        let n = label_offsets(&owner, &mut offs);
        for k in 1..n.saturating_sub(origin_labels) {
            let anc = &owner[offs[k] as usize..];
            key.clear();
            canon_key(anc, &mut key);
            self.nodes
                .entry(key.clone().into_boxed_slice())
                .or_insert_with(|| Arc::new(Node { owner: anc.into(), rrsets: Vec::new(), flags: 0 }));
        }
    }
    let apex_key = { let mut k = Vec::new(); canon_key(&self.origin, &mut k); k };
    let apex = self.nodes.get(apex_key.as_slice()).ok_or(ZoneError::NoSoa)?;
    if apex.get(T_SOA).map(|s| s.rdata.len()) != Some(1) {
        return Err(ZoneError::NoSoa);
    }
    let cuts: Vec<Box<[u8]>> = self.nodes.iter()
        .filter(|(k, n)| k.as_ref() != apex_key.as_slice() && n.get(T_NS).is_some())
        .map(|(k, _)| k.clone())
        .collect();
    let keys: Vec<Box<[u8]>> = self.nodes.keys().cloned().collect();
    for k in &keys {
        let mut flags = 0u8;
        if cuts.iter().any(|c| c == k) { flags |= NODE_CUT; }
        if cuts.iter().any(|c| k.len() > c.len() && k.starts_with(c)) { flags |= NODE_BELOW_CUT; }
        let mut wk = k.to_vec();
        wk.extend_from_slice(b"*\0");
        if self.nodes.contains_key(wk.as_slice()) { flags |= NODE_WILDCARD_CHILD; }
        let node = self.nodes.get_mut(k).unwrap();
        if node.flags != flags { Arc::make_mut(node).flags = flags; }
    }
    self.nsec = self.nodes.iter().filter(|(_, n)| n.get(T_NSEC).is_some()).map(|(k, _)| k.clone()).collect();
    self.nsec3 = self.nodes.iter()
        .filter(|(_, n)| n.get(T_NSEC3).is_some())
        .filter_map(|(k, n)| decode_b32hex_label(&n.owner).map(|h| (h, k.clone())))
        .collect();
    self.nsec3param = self.nodes.get(apex_key.as_slice())
        .and_then(|n| n.get(T_NSEC3PARAM)).map(|s| s.rdata[0].clone());
    Ok(())
}
```

The `cuts.iter().any` scans are O(nodes × cuts); replace them with a single ordered pass (keep a stack of the most recent cut key and test `starts_with`) — the BTreeMap iterates in canonical order so every occluded name directly follows its cut. `decode_b32hex_label` decodes a 32-character RFC 4648 base32hex first label (case-insensitive) into 20 bytes, `None` otherwise. `from_image` requires `Kind::Full`, inserts all records, sets `serial`, `origin_labels`, and calls `finalize`. `apply` requires `Kind::Delta`, `origin` equality (case-insensitive) and `from_serial == self.serial`, clones the `BTreeMap` (Arc clones only), removes each deleted record (RRSIG from `sigs` of its covered type, others from `rdata`; missing → `DeleteAbsent`), inserts added records, sets `serial = p.serial` and calls `finalize`. `nsec_covering(key)` returns the greatest NSEC key ≤ `key`, wrapping to the last entry; `nsec3_covering(h)` likewise over hashes with strict `<` (a match is returned by `nsec3_node`).

Built (as implemented): `finalize` also rejects an apex SOA whose serial differs from the zone serial (`BadSoa`) and an SOA at any other node (`NoSoa`); RRSIG RDATA shorter than 19 octets (18 fixed + root signer) is `BadRrsig`; `apply` returns a zone with empty `deltas` and `expired = false` (the loader sets both); `nodes_below(key)` yields strict descendants; `records_sorted` gives RRSIG records their RRset's TTL. `zone_tests.rs` adds Go/Rust canonical-order parity over all goldens, parser bounds (every truncated prefix, counts, out-of-zone, class, root origin, decompress limit), SOA/delete/RRSIG errors and the NSEC/NSEC3 indexes.

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib authoritative::zone_tests` — expect PASS.
- [ ] Commit: `git add engine/src/lib.rs engine/src/authoritative && git commit -m "feat(engine): NZF1 parser and in-memory authoritative zone model"`.

## Task 3: Engine authoritative answers (unsigned)

Files:

- `engine/src/authoritative/msg.rs` — `Question` parser for slow paths and tests (header, one question, optional OPT, IXFR authority SOA, trailing TSIG position), RR walker
- `engine/src/authoritative/set.rs` — `AuthSet`: zone lookup by longest suffix
- `engine/src/authoritative/lookup.rs` — RFC 1034 §4.3.2 / RFC 4592 / RFC 6672 name walk
- `engine/src/authoritative/writer.rs` — response writer with question-suffix compression and truncation
- `engine/src/authoritative/answer.rs` — `respond`: AA, referrals + glue, wildcards, CNAME/DNAME chains, NXDOMAIN/NODATA with SOA, ANY
- `engine/src/authoritative/answer_tests.rs`

`engine/Cargo.toml` needs no change: `hickory-proto =0.26.3` with `dnssec-ring` is already a normal and a dev dependency.

Interfaces:

```rust
// msg.rs
pub struct EdnsInfo { pub udp_size: u16, pub do_bit: bool }
pub struct Question<'a> {
    pub id: u16, pub opcode: u8, pub flags: u16,
    pub qname: &'a [u8],           // as received (client casing), uncompressed
    pub qtype: u16, pub qclass: u16,
    pub question_end: usize,       // offset after the question section
    pub edns: Option<EdnsInfo>,
    pub ixfr_serial: Option<u32>,  // SOA serial from the authority section of an IXFR query
    pub tsig_at: Option<usize>,    // offset of a trailing TSIG RR
}
pub enum MsgError { Short, Header, Name, Counts, Rr }
impl<'a> Question<'a> { pub fn parse(msg: &'a [u8]) -> Result<Self, MsgError>; pub fn rd(&self) -> bool; }
pub struct RrAt { pub start: usize, pub name_end: usize, pub rtype: u16, pub class: u16, pub ttl: u32, pub rdata: std::ops::Range<usize> }
pub fn walk_rrs(msg: &[u8], from: usize, count: usize) -> Result<(Vec<RrAt>, usize), MsgError>; // follows compression pointers backwards only, max 64 jumps

// set.rs
pub struct AuthSet { /* FxHashMap<Box<[u8]>, Arc<Zone>> keyed by lowercase origin wire */ }
impl AuthSet {
    pub fn empty() -> AuthSet;
    pub fn from_zones(zones: Vec<Arc<Zone>>) -> Result<AuthSet, String>; // duplicate origins → Err
    pub fn is_empty(&self) -> bool;
    pub fn get(&self, lower_origin: &[u8]) -> Option<&Arc<Zone>>;
    pub fn find(&self, lower_qname: &[u8]) -> Option<&Arc<Zone>>;
    pub fn find_for_query(&self, lower_qname: &[u8], qtype: u16) -> Option<&Arc<Zone>>;
    pub fn zones(&self) -> impl Iterator<Item = &Arc<Zone>>;
}

// lookup.rs
pub enum Lookup<'z> {
    Exact(&'z Node),
    Wildcard { closest_encloser: &'z Node, wildcard: &'z Node },
    NxDomain { closest_encloser: &'z Node },
    Delegation(&'z Node),
    Dname(&'z Node),
}
impl Zone { pub fn lookup(&self, lower_qname: &[u8], qtype: u16) -> Lookup<'_>; }

// answer.rs
pub struct Limits { pub max_len: usize, pub recursion_available: bool }
pub enum Served { Done(usize), NotHosted }
pub fn respond(set: &AuthSet, q: &Question<'_>, out: &mut [u8], limits: Limits) -> Served; // Task 13 adds DNSSEC records inside this function when DO=1
```

`respond` writes header + question + answer/authority/additional without OPT; the caller appends OPT, so `max_len` already excludes the OPT size.

- [ ] Add `pub mod msg; pub mod set; pub mod lookup; pub mod writer; pub mod answer; #[cfg(test)] mod answer_tests;` to `mod.rs` and write the failing `answer_tests.rs`:

```rust
use super::answer::{respond, Limits, Served};
use super::msg::Question;
use super::set::AuthSet;
use super::zone::Zone;
use super::{nzf, zone_tests::FULL};
use hickory_proto::op::{Message, MessageType, OpCode, Query, ResponseCode};
use hickory_proto::rr::{Name, RData, Record, RecordType};
use std::sync::Arc;

pub(crate) fn basic_set() -> AuthSet {
    let z = Zone::from_image(&nzf::parse(FULL).unwrap()).unwrap();
    AuthSet::from_zones(vec![Arc::new(z)]).unwrap()
}

pub(crate) fn ask_with(set: &AuthSet, name: &str, qtype: RecordType, max_len: usize) -> Message {
    let mut m = Message::new(0x1234, MessageType::Query, OpCode::Query);
    m.metadata.recursion_desired = true;
    m.add_query(Query::query(Name::from_ascii(name).unwrap(), qtype));
    let bytes = m.to_vec().unwrap();
    let q = Question::parse(&bytes).unwrap();
    let mut out = vec![0u8; 65535];
    match respond(set, &q, &mut out, Limits { max_len, recursion_available: false }) {
        Served::Done(n) => Message::from_vec(&out[..n]).unwrap(),
        Served::NotHosted => panic!("{name} not hosted"),
    }
}

fn ask(set: &AuthSet, name: &str, qtype: RecordType) -> Message {
    ask_with(set, name, qtype, 4096)
}

pub(crate) fn types(rrs: &[Record]) -> Vec<RecordType> {
    rrs.iter().map(|r| r.record_type()).collect()
}

#[test]
fn exact_answer_is_authoritative_and_echoes_client_case() {
    let r = ask(&basic_set(), "WWW.Example.Test.", RecordType::A);
    assert!(r.metadata.authoritative);
    assert_eq!(r.metadata.response_code, ResponseCode::NoError);
    assert_eq!(r.metadata.id, 0x1234);
    assert_eq!(types(&r.answers), vec![RecordType::A, RecordType::A]);
    assert_eq!(r.queries[0].name().to_ascii(), "WWW.Example.Test.");
}

#[test]
fn nxdomain_has_soa_with_negative_ttl() {
    let r = ask(&basic_set(), "nope.example.test.", RecordType::A);
    assert_eq!(r.metadata.response_code, ResponseCode::NXDomain);
    assert!(r.metadata.authoritative);
    assert!(r.answers.is_empty());
    assert_eq!(types(&r.authorities), vec![RecordType::SOA]);
    assert_eq!(r.authorities[0].ttl, 300, "min(SOA TTL 3600, MINIMUM 300)");
}

#[test]
fn nodata_for_existing_name_and_empty_non_terminal() {
    for name in ["www.example.test.", "b.c.example.test.", "wild.example.test."] {
        let r = ask(&basic_set(), name, RecordType::MX);
        assert_eq!(r.metadata.response_code, ResponseCode::NoError, "{name}");
        assert!(r.metadata.authoritative, "{name}");
        assert!(r.answers.is_empty(), "{name}");
        assert_eq!(types(&r.authorities), vec![RecordType::SOA], "{name}");
    }
}

#[test]
fn nxdomain_below_empty_non_terminal_without_wildcard() {
    let r = ask(&basic_set(), "x.b.c.example.test.", RecordType::A);
    assert_eq!(r.metadata.response_code, ResponseCode::NXDomain);
}

#[test]
fn referral_below_apex_has_ns_and_glue_without_aa() {
    let r = ask(&basic_set(), "host.sub.example.test.", RecordType::A);
    assert!(!r.metadata.authoritative);
    assert_eq!(r.metadata.response_code, ResponseCode::NoError);
    assert!(r.answers.is_empty());
    assert_eq!(types(&r.authorities), vec![RecordType::NS], "no DS without DO");
    assert_eq!(r.additionals.len(), 1);
    assert_eq!(r.additionals[0].name.to_ascii(), "ns.sub.example.test.");
}

#[test]
fn glue_is_not_served_as_authoritative_data() {
    let r = ask(&basic_set(), "ns.sub.example.test.", RecordType::A);
    assert!(!r.metadata.authoritative, "occluded name must produce a referral");
    assert_eq!(types(&r.authorities), vec![RecordType::NS]);
}

#[test]
fn ds_at_cut_is_answered_from_parent_side() {
    let r = ask(&basic_set(), "sub.example.test.", RecordType::DS);
    assert!(r.metadata.authoritative);
    assert_eq!(types(&r.answers), vec![RecordType::DS]);
}

#[test]
fn wildcard_synthesis_uses_query_name() {
    let r = ask(&basic_set(), "anything.wild.example.test.", RecordType::TXT);
    assert!(r.metadata.authoritative);
    assert_eq!(types(&r.answers), vec![RecordType::TXT]);
    assert_eq!(r.answers[0].name.to_ascii(), "anything.wild.example.test.");
    let nodata = ask(&basic_set(), "anything.wild.example.test.", RecordType::A);
    assert_eq!(nodata.metadata.response_code, ResponseCode::NoError);
    assert_eq!(types(&nodata.authorities), vec![RecordType::SOA]);
}

#[test]
fn cname_chain_is_followed_inside_hosted_data() {
    let r = ask(&basic_set(), "alias.example.test.", RecordType::A);
    assert!(r.metadata.authoritative);
    assert_eq!(types(&r.answers), vec![RecordType::CNAME, RecordType::A, RecordType::A]);
    let c = ask(&basic_set(), "alias.example.test.", RecordType::CNAME);
    assert_eq!(types(&c.answers), vec![RecordType::CNAME]);
}

#[test]
fn cname_loop_is_servfail() {
    let r = ask(&basic_set(), "loop1.example.test.", RecordType::A);
    assert_eq!(r.metadata.response_code, ResponseCode::ServFail);
}

#[test]
fn dname_synthesises_cname_with_dname_ttl() {
    let r = ask(&basic_set(), "host.dn.example.test.", RecordType::A);
    assert!(r.metadata.authoritative);
    assert_eq!(types(&r.answers), vec![RecordType::DNAME, RecordType::CNAME]);
    match r.answers[1].data() {
        RData::CNAME(c) => assert_eq!(c.0.to_ascii(), "host.example.net."),
        other => panic!("{other:?}"),
    }
    assert_eq!(r.answers[1].ttl, 300);
    assert_eq!(r.answers[1].name.to_ascii(), "host.dn.example.test.");
}

#[test]
fn dname_owner_itself_is_not_rewritten() {
    let r = ask(&basic_set(), "dn.example.test.", RecordType::DNAME);
    assert_eq!(types(&r.answers), vec![RecordType::DNAME]);
}

#[test]
fn any_query_returns_a_single_rrset() {
    let r = ask(&basic_set(), "example.test.", RecordType::ANY);
    let t = types(&r.answers);
    assert!(!t.is_empty());
    assert!(t.iter().all(|x| *x == t[0]), "RFC 8482: one RRset, got {t:?}");
}

#[test]
fn oversized_answer_is_truncated() {
    let r = ask_with(&basic_set(), "big.example.test.", RecordType::TXT, 1232);
    assert!(r.metadata.truncation);
    assert!(r.answers.is_empty());
    assert_eq!(r.queries.len(), 1);
    let full = ask_with(&basic_set(), "big.example.test.", RecordType::TXT, 65535);
    assert!(!full.metadata.truncation);
    assert_eq!(full.answers.len(), 40);
}

#[test]
fn names_outside_hosted_zones_are_not_hosted() {
    let s = basic_set();
    let mut m = Message::new(1, MessageType::Query, OpCode::Query);
    m.add_query(Query::query(Name::from_ascii("example.org.").unwrap(), RecordType::A));
    let b = m.to_vec().unwrap();
    let q = Question::parse(&b).unwrap();
    let mut out = vec![0u8; 512];
    assert!(matches!(respond(&s, &q, &mut out, Limits { max_len: 512, recursion_available: true }), Served::NotHosted));
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib authoritative::answer_tests` — expect FAIL with "unresolved import".
- [ ] Implement `msg.rs`: require ≥ 12 octets (`Short`); QDCOUNT == 1 (`Counts`); qname uncompressed with labels ≤ 63 and ≤ 255 octets (`Name`); ANCOUNT must be 0 except for NOTIFY (opcode 4) and UPDATE (opcode 5) which are parsed by their own handlers from `question_end`; NSCOUNT must be 0 except qtype IXFR (exactly one SOA, serial read from rdata offset `rdlen-20`) and UPDATE; the additional section may hold one OPT (class = UDP size, TTL bit 15 = DO) and one TSIG which must be the last RR (`tsig_at` = its start). `walk_rrs` decodes names with pointer targets strictly lower than the current position and at most 64 jumps.
- [ ] Implement `set.rs` (`FxHashMap<Box<[u8]>, Arc<Zone>>`, borrowed `&[u8]` lookups):

```rust
pub fn find(&self, lower: &[u8]) -> Option<&Arc<Zone>> {
    let mut i = 0usize;
    loop {
        if let Some(z) = self.zones.get(&lower[i..]) {
            return Some(z);
        }
        let l = lower[i] as usize;
        if l == 0 {
            return None;
        }
        i += l + 1;
    }
}

pub fn find_for_query(&self, lower: &[u8], qtype: u16) -> Option<&Arc<Zone>> {
    let z = self.find(lower)?;
    if qtype == T_DS && z.origin() == lower {
        let parent = &lower[lower[0] as usize + 1..];
        if let Some(p) = self.find(parent) {
            return Some(p);
        }
    }
    Some(z)
}
```

- [ ] Implement `lookup.rs`:

```rust
impl Zone {
    pub fn lookup(&self, qname: &[u8], qtype: u16) -> Lookup<'_> {
        let mut offs = [0u16; 128];
        let total = label_offsets(qname, &mut offs);
        let mut key = Vec::with_capacity(qname.len() + 16);
        let mut encloser = self.apex();
        for depth in (self.origin_labels() + 1)..=total {
            let name = &qname[offs[total - depth] as usize..];
            key.clear();
            canon_key(name, &mut key);
            let Some(node) = self.node_by_key(&key) else {
                if encloser.flags & NODE_WILDCARD_CHILD != 0 {
                    key.clear();
                    canon_key(&encloser.owner, &mut key);
                    key.extend_from_slice(b"*\0");
                    if let Some(w) = self.node_by_key(&key) {
                        return Lookup::Wildcard { closest_encloser: encloser, wildcard: w };
                    }
                }
                return Lookup::NxDomain { closest_encloser: encloser };
            };
            let is_qname = depth == total;
            if node.is_cut() && !(is_qname && qtype == T_DS) {
                return Lookup::Delegation(node);
            }
            if !is_qname && node.get(T_DNAME).is_some() {
                return Lookup::Dname(node);
            }
            encloser = node;
        }
        Lookup::Exact(encloser)
    }
}
```

The lowercase qname is passed in; canonical keys are case-insensitive anyway. Allocation note: `key` is a per-call `Vec`; replace it with a 512-octet stack buffer (`arrayvec`-free: `[u8; 512]` + length) so the authoritative path performs no heap allocation.

- [ ] Implement `writer.rs`. `Writer::new(out, limit, q)` writes the 12-octet header (ID from query, QR=1, opcode copied, RD copied, AA/TC/RA/rcode set by `finish`), copies the question verbatim (client casing), and records `qname` offsets. `put_name(name)` compresses only against suffixes of the question name:

```rust
fn put_name(&mut self, name: &[u8]) -> Result<(), Overflow> {
    let mut offs = [0u16; 128];
    let n = label_offsets(name, &mut offs);
    let q = &self.buf[12..12 + self.qname_len];
    for k in 0..n {
        let suffix = &name[offs[k] as usize..];
        if suffix.len() > q.len() {
            continue;
        }
        let at = q.len() - suffix.len();
        if q[at..].eq_ignore_ascii_case(suffix) && self.qname_label_starts[..self.qname_labels].contains(&(at as u16)) {
            let prefix = &name[..offs[k] as usize];
            self.put(prefix)?;
            return self.put(&(0xC000u16 | (12 + at) as u16).to_be_bytes());
        }
    }
    self.put(name)
}
```

`rr(section, owner, rtype, ttl, rdata)` writes owner, type, class 1, TTL, rdlength, rdata; on `Overflow` it restores `len` to the value before the RR. `truncate()` resets `len` to the end of the question, zeroes counts and sets TC. `finish(rcode, aa, ra)` patches flags and counts and returns the length.

- [ ] Implement `answer.rs`:
  - lowercase the qname into a stack buffer; `set.find_for_query` → `NotHosted` when `None`; zone `expired` → SERVFAIL, AA=0.
  - chain loop, `MAX_CHAIN = 8`, `seen: [([u8; 255], u8); 9]` of lowercase names:
    - `Delegation(cut)`: on hop 0 write the cut's NS RRset to authority, then for each NS target inside the zone (`is_subdomain(target, origin)`) look up the target node directly by key (bypassing cut logic) and write its A then AAAA RRsets to additional; AA=0; NOERROR. After hop 0, stop with what is written.
    - `Dname(node)`: write DNAME RRset (owner = node owner); synthesise with the function below; on `None` return YXDOMAIN; write CNAME `qname → target` with the DNAME TTL (rdata = target wire); continue the chain with target.
    - `Exact(node)` / `Wildcard{wildcard}` (owner written = current name): `node.is_empty()` → NODATA; qtype ANY → first RRset with non-empty rdata; node has CNAME and qtype ∉ {CNAME, ANY} → write CNAME, continue with its target; RRset of qtype → write it, NOERROR; otherwise NODATA.
    - `NxDomain` → NXDOMAIN.
    - NODATA / NXDOMAIN write the zone SOA to authority with TTL `min(soa ttl, MINIMUM)` (MINIMUM = last 4 octets of SOA rdata); the rcode and SOA come from the zone of the last lookup in the chain.
    - continuing: lowercase target; if already in `seen` → reset writer to question only and return SERVFAIL; if `set.find(target)` is `None` → stop NOERROR; after `MAX_CHAIN` hops stop NOERROR with the partial chain.
  - AA = 1 unless the first lookup was a delegation; RA = `limits.recursion_available`.
  - `Overflow` in answer or authority → `truncate()`; in additional → stop adding additional records.

```rust
/// qname = <prefix>.<dname owner>  ->  <prefix>.<dname target>; None when > 255 octets.
fn synth_dname(qname: &[u8], owner: &[u8], target: &[u8], out: &mut [u8; 255]) -> Option<usize> {
    let prefix_len = qname.len() - owner.len();
    let total = prefix_len + target.len();
    if total > 255 {
        return None;
    }
    out[..prefix_len].copy_from_slice(&qname[..prefix_len]);
    out[prefix_len..total].copy_from_slice(target);
    Some(total)
}
```

Built (as implemented): the stack key is `name::canon_key_buf(wire, &mut [u8; name::KEY_BUF]) -> usize` (`KEY_BUF = 512`), used by `lookup` and by `Zone::node`, so no lookup allocates. `Question` and `EdnsInfo` are `Copy`; `MsgError` is a `thiserror` enum; UPDATE messages may carry other additional RRs; `walk_rrs` bounds `count` by the remaining octets / 11 before allocating. `Writer::new` returns `None` when header + question do not fit (then `respond` returns `Served::Done(0)`, which the caller drops), copies RD and CD, and `Writer::reset()` drops RRs without TC (loop SERVFAIL). A qclass other than IN is `NotHosted`. An overflow in answer/authority answers TC with NOERROR; a CNAME/DNAME target in an expired zone stops the chain with NOERROR. `answer::step` writes a followed target into a caller buffer (no large enum variant). `answer_tests.rs` adds `question_parser_reads_ixfr_serial_and_trailing_tsig` and `chains_stop_at_names_outside_hosted_zones_and_at_cuts`.

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib authoritative::answer_tests` — expect PASS.
- [ ] Commit: `git add engine/src/authoritative && git commit -m "feat(engine): authoritative answers with referrals, wildcards, CNAME/DNAME and negative responses"`.

## Task 4: Engine runtime integration — incremental zone loading and the authoritative pipeline stage

Files:

- `engine/src/authoritative/loader.rs` — build an `AuthSet` from `ConfigSnapshot.auth_zones` over `snapshot::BlobSource`, reusing and incrementally updating zones
- `engine/src/authoritative/loader_tests.rs`
- `engine/src/authoritative/state.rs` — `AuthState` (process-wide M4 state) and `after_apply`
- `engine/src/authoritative/dispatch.rs` — `fast` (hosted queries that parsed) and `unparsed` (messages `parse_query` rejects; filled by Tasks 7, 8, 10, 11)
- `engine/tests/authoritative_pipeline.rs` — hosted answers through `handle_packet` on a running worker
- `engine/src/runtime.rs` — `Runtime.auth`, `Runtime.auth_loads`, `Runtime.auth_changed` built in `Runtime::build` (modify)
- `engine/src/snapshot.rs` — `validate` calls `authoritative::loader::validate` after `validate_m3` (modify)
- `engine/src/control.rs` — `fetch_blobs` also collects `auth_zones` image and delta refs; `apply_snapshot` calls `authoritative::after_apply` after an `Applied` outcome (modify)
- `engine/src/main.rs` — hands the control runtime handle to `shared.auth` and calls `after_apply` for the persisted or standalone snapshot (modify)
- `engine/src/server/mod.rs` — `Shared.auth`, cookie steps moved above the ACL, the authoritative stage and the unparsed-message hook in `handle_packet` (modify)
- `engine/src/telemetry/metrics.rs` — `Metrics.auth: AuthCounters`, `WorkerCounters.auth_answers`, rendering of `nexora_auth_zones`, `nexora_auth_zone_loads_total{kind}`, `nexora_auth_answers_total{result}` (modify)
- `engine/src/telemetry/querylog.rs` — `CacheOutcome::Auth` (`"auth"`) (modify)

Interfaces:

```rust
// loader.rs
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)] pub struct LoadCounts { pub full: u64, pub delta: u64, pub reused: u64 }
pub struct Loaded { pub set: AuthSet, pub counts: LoadCounts, pub changed: Vec<(Box<[u8]>, u32)> } // zones whose serial changed or that are new
#[derive(Debug)] pub enum LoadError { Blob(crate::snapshot::SnapshotError), Nzf { zone: String, err: NzfError }, Zone { zone: String, err: ZoneError }, Chain(String) } // + Display
pub fn load(prev: &AuthSet, zones: &[proto::AuthZone], blobs: &dyn crate::snapshot::BlobSource) -> Result<Loaded, LoadError>;
pub fn validate(zones: &[proto::AuthZone]) -> Result<(), String>;

// state.rs
pub struct AuthState { /* control: OnceLock<tokio::runtime::Handle>, applied_version: AtomicU64; Tasks 6, 10, 11 add keyring, control-stream sender, update waiters */ }
impl AuthState {
    pub fn new() -> Arc<AuthState>;
    pub fn set_control_runtime(&self, handle: tokio::runtime::Handle);
}
/// Once per runtime version: adds `rt.auth_loads` to `shared.metrics.auth`; Task 8 sends NOTIFY for `rt.auth_changed`.
pub fn after_apply(shared: &Shared, rt: &Runtime);

// dispatch.rs
pub enum AuthOutcome { NotHosted, Reply(usize) } // Task 8 adds Slow(SlowJob)
pub fn fast(ctx: &WorkerCtx, rt: &Runtime, view: &QueryView<'_>, packet: &[u8], client: SocketAddr, transport: Transport, out: &mut [u8], opt: Option<&ReplyOpt>) -> AuthOutcome;
pub fn unparsed(ctx: &WorkerCtx, rt: &Runtime, packet: &[u8], client: SocketAddr, transport: Transport, out: &mut [u8]) -> Option<AuthOutcome>; // None in this task
impl<'a> Question<'a> { pub fn from_query(view: &QueryView<'a>, raw: &'a [u8]) -> Question<'a>; }

// runtime.rs (added fields)
pub struct Runtime { /* M1-M3 fields */ pub auth: Arc<AuthSet>, pub auth_loads: LoadCounts, pub auth_changed: Vec<(Box<[u8]>, u32)> }
// server/mod.rs (added field; Shared::new and Shared::with_recursor create it with AuthState::new())
pub struct Shared { /* M1-M3 fields */ pub auth: Arc<crate::authoritative::state::AuthState> }
// telemetry/metrics.rs
pub struct AuthCounters { pub loads: [AtomicU64; 3] /* full, delta, reused */, /* Tasks 8, 10, 11 add transfers, notify_sent, notify_received, updates */ }
// WorkerCounters gains `pub auth_answers: [Counter; 5]` indexed answer, nodata, nxdomain, referral, servfail
```

- [ ] Add `pub mod loader; pub mod state; pub mod dispatch; #[cfg(test)] mod loader_tests;` to `authoritative/mod.rs` and write the failing `loader_tests.rs`:

```rust
use super::loader::{load, validate, LoadError};
use super::set::AuthSet;
use super::zone_tests::{DELTA, FULL};
use crate::proto;
use crate::snapshot::{BlobSource, SnapshotError};
use sha2::{Digest, Sha256};
use std::cell::RefCell;
use std::collections::HashMap;

struct MapBlobs {
    blobs: HashMap<String, Vec<u8>>,
    read: RefCell<Vec<String>>,
}

impl MapBlobs {
    fn new(raws: &[&[u8]]) -> (Self, Vec<proto::BlobRef>) {
        let mut blobs = HashMap::new();
        let mut refs = Vec::new();
        for raw in raws {
            let data = zstd::bulk::compress(raw, 3).unwrap();
            let sha = hex::encode(Sha256::digest(&data));
            refs.push(proto::BlobRef { sha256: sha.clone(), size: data.len() as u64, name: String::new() });
            blobs.insert(sha, data);
        }
        (MapBlobs { blobs, read: RefCell::new(Vec::new()) }, refs)
    }
}

impl BlobSource for MapBlobs {
    fn read(&self, r: &proto::BlobRef) -> Result<Vec<u8>, SnapshotError> {
        self.read.borrow_mut().push(r.sha256.clone());
        self.blobs.get(&r.sha256).cloned().ok_or_else(|| SnapshotError::Blob { sha256: r.sha256.clone(), reason: "blob not present".into() })
    }
}

fn zone_msg(serial: u32, image: &proto::BlobRef, deltas: Vec<proto::ZoneDelta>, offset: u32) -> proto::AuthZone {
    proto::AuthZone {
        name: "example.test.".into(),
        kind: proto::AuthZoneKind::Primary as i32,
        serial,
        image: Some(image.clone()),
        image_serial: 2026091301,
        deltas,
        image_delta_offset: offset,
        ..Default::default()
    }
}

#[test]
fn second_version_is_applied_as_a_delta_without_reading_the_image() {
    let (blobs, refs) = MapBlobs::new(&[FULL, DELTA]);
    let v1 = vec![zone_msg(2026091301, &refs[0], vec![], 0)];
    validate(&v1).unwrap();
    let first = load(&AuthSet::empty(), &v1, &blobs).unwrap();
    assert_eq!((first.counts.full, first.counts.delta), (1, 0));
    assert_eq!(first.changed.len(), 1);

    let d = proto::ZoneDelta { from_serial: 2026091301, to_serial: 2026091302, blob: Some(refs[1].clone()) };
    let v2 = vec![zone_msg(2026091302, &refs[0], vec![d], 0)];
    validate(&v2).unwrap();
    blobs.read.borrow_mut().clear();
    let second = load(&first.set, &v2, &blobs).unwrap();
    assert_eq!((second.counts.full, second.counts.delta), (0, 1));
    assert_eq!(*blobs.read.borrow(), vec![refs[1].sha256.clone()]);
    let z = second.set.get(b"\x07example\x04test\x00").unwrap();
    assert_eq!(z.serial(), 2026091302);
    assert_eq!(z.deltas.len(), 1, "IXFR history retained");

    let third = load(&second.set, &v2, &blobs).unwrap();
    assert_eq!(third.counts.reused, 1);
    assert!(third.changed.is_empty());
}

#[test]
fn fresh_engine_builds_from_image_plus_deltas() {
    let (blobs, refs) = MapBlobs::new(&[FULL, DELTA]);
    let d = proto::ZoneDelta { from_serial: 2026091301, to_serial: 2026091302, blob: Some(refs[1].clone()) };
    let loaded = load(&AuthSet::empty(), &[zone_msg(2026091302, &refs[0], vec![d], 0)], &blobs).unwrap();
    assert_eq!(loaded.counts.full, 1);
    assert_eq!(loaded.set.get(b"\x07example\x04test\x00").unwrap().serial(), 2026091302);
}

#[test]
fn missing_blob_and_broken_chain_are_rejected() {
    let (blobs, mut refs) = MapBlobs::new(&[FULL]);
    let good = refs[0].clone();
    refs[0].sha256 = "0".repeat(64);
    assert!(matches!(load(&AuthSet::empty(), &[zone_msg(2026091301, &refs[0], vec![], 0)], &blobs), Err(LoadError::Blob(_))));
    let gap = proto::ZoneDelta { from_serial: 2026091300, to_serial: 2026091302, blob: Some(good.clone()) };
    assert!(validate(&[zone_msg(2026091302, &good, vec![gap], 0)]).is_err());
    let mut dup = vec![zone_msg(2026091301, &good, vec![], 0)];
    dup.push(dup[0].clone());
    assert!(validate(&dup).is_err(), "duplicate zone names");
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib authoritative::loader_tests` — expect FAIL with "unresolved import `super::loader`".
- [ ] Implement `validate`: names parse via `name::from_ascii`, are lowercase, absolute, not the root; no duplicates; `image` present with 64-hex `sha256`; every delta has a blob with 64-hex sha; `deltas[i].to_serial == deltas[i+1].from_serial`; if deltas non-empty the last `to_serial == serial`; `image_delta_offset <= deltas.len()`; `deltas[image_delta_offset..]` start at `image_serial` (or are empty with `image_serial == serial`); `transfer.allow_cidrs` parse as `ipnet::IpNet`; `notify[].address` and `primaries[]` parse as `SocketAddr`. Call it from `snapshot::validate` right after `crate::snapshot_m3::validate_m3(s)` (`.map_err(SnapshotError::Invalid)?`) so a bad zone rejects the whole snapshot.
- [ ] Implement `load` per zone (blobs are compressed NZF1; `DirBlobs::read` already verified size and SHA-256):
  1. `prev.get(name)` with equal serial and identical delta blob list → reuse the `Arc`, `reused += 1`.
  2. else if a previous zone exists and some index `i` has `deltas[i].from_serial == old.serial()` → read `deltas[i..]`, `nzf::decompress` (max 1 GiB), `old.apply` in order; any error falls through to 3; success → `delta += 1`.
  3. else read the image, `Zone::from_image`, require `serial == image_serial`, apply `deltas[image_delta_offset..]`; `full += 1`.
  4. require the final serial == `serial`, else `LoadError::Chain`.
  5. fill `zone.deltas` with an `Arc<DeltaRecords>` for every listed delta, reusing entries of the previous zone by `(from, to)` and reading only missing ones; set `zone.expired`.
  6. push `(name, serial)` to `changed` when the zone is new or its serial differs.
- [ ] Wire the runtime: in `Runtime::build`, `let empty = AuthSet::empty(); let loaded = authoritative::loader::load(previous.map_or(&empty, |p| &*p.auth), &s.auth_zones, blobs).map_err(|e| SnapshotError::Invalid(e.to_string()))?;` and set `auth: Arc::new(loaded.set)`, `auth_loads: loaded.counts`, `auth_changed: loaded.changed` (the reused `Arc<Zone>`s make an unchanged zone free); `Runtime::initial()` uses `Arc::new(AuthSet::empty())`, zero counts, no changes. Zone data never touches `filter_hashes`, so an edit does not clear the response cache. In `control::fetch_blobs`, chain `snap.auth_zones.iter().flat_map(|z| z.image.iter().chain(z.deltas.iter().filter_map(|d| d.blob.as_ref())))` into the collected refs.
- [ ] Implement `state.rs`: `AuthState::new()` (empty `OnceLock` handle, `applied_version = 0`); `after_apply(shared, rt)` returns at once when `rt.version <= applied_version` (compare-and-swap), otherwise adds `rt.auth_loads` to `shared.metrics.auth.loads`. Call sites (the same three places that call M3's `shared.recursor.sync`): `control::apply_snapshot` after `ApplyOutcome::Applied` with `&shared.runtime.load()`; `main.rs`, right after the control runtime is built, `shared.auth.set_control_runtime(control.handle().clone())` and then `after_apply(&shared, &shared.runtime.load())` (covers the persisted and the standalone snapshot); the standalone SIGHUP reload path after `apply_standalone`.
- [ ] Write the failing pipeline test `engine/tests/authoritative_pipeline.rs` (the ACL excludes the test client, so a forwarded name is REFUSED while the hosted zone still answers):

```rust
use hickory_proto::op::{Edns, Message, MessageType, OpCode, Query, ResponseCode};
use hickory_proto::rr::{Name, RecordType};
use hickory_proto::serialize::binary::{BinDecodable, BinEncodable};
use nexora_engine::bootstrap::Bootstrap;
use nexora_engine::proto::*;
use nexora_engine::server::tls::CertStore;
use nexora_engine::server::{Shared, spawn_workers};
use nexora_engine::snapshot::{ApplyOutcome, DirBlobs, apply};
use sha2::Digest;
use std::net::{SocketAddr, UdpSocket};
use std::sync::Arc;
use std::time::Duration;

const FULL: &[u8] = include_bytes!("../../testdata/nzf/basic-full.nzf");

fn start(acl: &str) -> SocketAddr {
    let port = std::net::TcpListener::bind("127.0.0.1:0").unwrap().local_addr().unwrap().port();
    let dir = tempfile::tempdir().unwrap().keep();
    let boot: Bootstrap = toml::from_str(&format!(
        "node_name = \"t\"\nstate_dir = \"{}\"\nlisten_udp = [\"127.0.0.1:{port}\"]\nlisten_tcp = [\"127.0.0.1:{port}\"]\nworkers = 2\nstandalone_snapshot = \"x\"\n",
        dir.display()
    ))
    .unwrap();
    let z = zstd::encode_all(FULL, 3).unwrap();
    let sha = hex::encode(sha2::Sha256::digest(&z));
    std::fs::write(dir.join(&sha), &z).unwrap();
    let shared = Shared::new(2);
    let snap = ConfigSnapshot {
        version: 1,
        resolver: Some(ResolverConfig { strategy: UpstreamStrategy::Ordered as i32 }),
        cache: Some(CacheConfig { max_bytes: 8 << 20, max_ttl: 86400, negative_max_ttl: 3600, ..Default::default() }),
        acl_allow_cidrs: vec![acl.into()],
        filter: Some(FilterConfig { block_mode: BlockMode::NullIp as i32, block_ttl: 60, ..Default::default() }),
        telemetry: Some(TelemetryConfig::default()),
        auth_zones: vec![AuthZone {
            name: "example.test.".into(),
            kind: AuthZoneKind::Primary as i32,
            serial: 2026091301,
            image: Some(BlobRef { sha256: sha, size: z.len() as u64, name: "example.test.@2026091301".into() }),
            image_serial: 2026091301,
            ..Default::default()
        }],
        ..Default::default()
    };
    assert!(matches!(apply(&shared.runtime, snap, &DirBlobs { dir: dir.clone() }, None), ApplyOutcome::Applied { .. }));
    spawn_workers(shared, &boot, Arc::new(CertStore::new())).unwrap();
    std::thread::sleep(Duration::from_millis(200));
    format!("127.0.0.1:{port}").parse().unwrap()
}

fn ask(server: SocketAddr, name: &str, cookie: bool) -> (Message, Vec<u8>) {
    let c = UdpSocket::bind("127.0.0.1:0").unwrap();
    c.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
    let mut m = Message::new(0x4d34, MessageType::Query, OpCode::Query);
    m.metadata.recursion_desired = true;
    m.add_query(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
    let mut e = Edns::new();
    e.set_max_payload(1232);
    m.set_edns(e);
    let mut wire = m.to_bytes().unwrap();
    if cookie {
        // append a client cookie option (code 10, 8 octets) to the OPT RR at the end of the message
        let n = wire.len();
        let rdlen = u16::from_be_bytes([wire[n - 2], wire[n - 1]]) + 12;
        wire[n - 2..].copy_from_slice(&rdlen.to_be_bytes());
        wire.extend_from_slice(&[0, 10, 0, 8, 1, 2, 3, 4, 5, 6, 7, 8]);
    }
    c.send_to(&wire, server).unwrap();
    let mut buf = [0u8; 4096];
    let (n, _) = c.recv_from(&mut buf).unwrap();
    (Message::from_bytes(&buf[..n]).unwrap(), buf[..n].to_vec())
}

#[test]
fn hosted_names_answer_authoritatively_before_the_acl() {
    let srv = start("10.0.0.0/8");
    let (hosted, raw) = ask(srv, "www.example.test.", true);
    assert_eq!(hosted.metadata.response_code, ResponseCode::NoError);
    assert!(hosted.metadata.authoritative);
    assert!(!hosted.metadata.recursion_available, "the client is outside the ACL");
    assert_eq!(hosted.answers.len(), 2);
    assert!(hosted.edns.is_some(), "OPT echoed");
    let cookie_option = [0u8, 10, 0, 24, 1, 2, 3, 4, 5, 6, 7, 8];
    assert!(raw.windows(cookie_option.len()).any(|w| w == cookie_option), "client + server cookie on authoritative answers");
    let (forwarded, _) = ask(srv, "www.example.org.", false);
    assert_eq!(forwarded.metadata.response_code, ResponseCode::Refused, "names outside hosted zones still pass the ACL");
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --test authoritative_pipeline` — expect FAIL at the `ResponseCode::NoError` assertion with `left: Refused` (the stage is not wired yet; `AuthZone` compiles since Task 1).
- [ ] Implement `dispatch::fast`:

```rust
pub fn fast(ctx: &WorkerCtx, rt: &Runtime, view: &QueryView<'_>, packet: &[u8], client: SocketAddr, transport: Transport, out: &mut [u8], opt: Option<&ReplyOpt>) -> AuthOutcome {
    let q = Question::from_query(view, packet);
    if rt.auth.find_for_query(view.key.as_wire(), q.qtype).is_none() {
        return AuthOutcome::NotHosted;
    }
    if q.qtype == T_AXFR && transport == Transport::Udp {
        return AuthOutcome::Reply(wire::write_rcode_reply(view, wire::RCODE_FORMERR, out, opt));
    }
    let opt_len = opt.map_or(0, ReplyOpt::wire_len);
    let limits = Limits { max_len: out.len().saturating_sub(opt_len), recursion_available: rt.acl.allows(client.ip()) };
    match answer::respond(&rt.auth, &q, out, limits) {
        Served::Done(mut n) => {
            count_answer(ctx, &out[..n]);
            if let Some(o) = opt {
                n += edns::write_opt(&mut out[n..], o);
                let ar = u16::from_be_bytes([out[10], out[11]]) + 1;
                out[10..12].copy_from_slice(&ar.to_be_bytes());
            }
            AuthOutcome::Reply(n)
        }
        Served::NotHosted => AuthOutcome::NotHosted,
    }
}
```

`count_answer` increments `ctx.counters().auth_answers[i]` (SERVFAIL → servfail, NXDOMAIN → nxdomain, AA=0 → referral, ANCOUNT=0 → nodata, else answer). `Question::from_query` copies `id`, `flags`, `qname`, `qtype`, `qclass`, `question_end` and `edns` (`udp_size`, `do_bit` from `view.opt`) with `ixfr_serial: None`, `tsig_at: None`. `unparsed` returns `None` in this task.

- [ ] Wire `server::handle_packet`: move the bad-cookie FORMERR check and the server-cookie block (unchanged code) above `if !rt.acl.allows(client.ip())`, and insert between them and the ACL check:

```rust
if !rt.auth.is_empty() {
    match authoritative::dispatch::fast(ctx, rt, &q, packet, client, transport, &mut out[..limit], opt.as_ref()) {
        AuthOutcome::NotHosted => {}
        AuthOutcome::Reply(n) => {
            rec.cache = CacheOutcome::Auth;
            return reply(&scope, rec, out, n);
        }
    }
}
```

In the `Err(e)` arm of `wire::parse_query` (after the `TooShort | IsResponse` drop), call `authoritative::dispatch::unparsed(ctx, rt, packet, client, transport, out)` first when `!rt.auth.is_empty()`; `Some(AuthOutcome::Reply(n))` is finished with `scope.finish` and returned, `None`/`NotHosted` falls through to M1's FORMERR/NOTIMP. `Shared::new` and `Shared::with_recursor` set `auth: AuthState::new()`. `CacheOutcome::Auth` renders as `"auth"` in `as_str` (OTLP `nexora.cache`).

- [ ] Metrics: `Metrics::new` creates `auth: AuthCounters::default()` and `WorkerCounters.auth_answers`; `render` registers gauge `nexora_auth_zones` (`rt.auth.zones().count()`), counter `nexora_auth_zone_loads{kind}` (full, delta, reused) and `nexora_auth_answers{result}` (sum over workers; the encoder appends `_total`), creating every label value at zero.
      Built (as implemented): `authoritative/mod.rs` re-exports `after_apply`; `AuthState::control_runtime()` returns the handle. `validate` also requires `kind` PRIMARY or SECONDARY. `load` checks each delta blob's header (kind, from/to serial) against its `ZoneDelta` and the image origin against the zone name; a zone whose serial and history are unchanged but whose `expired` flag changed is cloned (counted `reused`), and one with the same serial but a different history window keeps its data and only rebuilds `deltas` (counted `reused`). `dispatch::fast` answers a hosted AXFR over UDP with FORMERR and any other AXFR/IXFR with REFUSED until Task 8 routes transfers (`FastOutcome::Slow`, `SlowJob` and `Answerer::answer_frames` stay Task 8's, as its Files list says). `WorkerCtx::counters` is public. Metrics labels are `telemetry::metrics::{AUTH_LOAD_KINDS, AUTH_ANSWER_RESULTS}`, rendered by `Metrics::register_auth` and covered by `auth_families_render_every_label_at_zero`. `engine/tests/hot_path_alloc.rs` counts allocations per thread (thread-local counter, so concurrent tests cannot reset each other) and adds `authoritative_answer_path_does_not_allocate` (CNAME chain, referral with glue, wildcard, NXDOMAIN through `handle_packet`). Also (lead request, all resolution modes): `cache::write_cached` clears AA, so forwarded, recursive, CD pass-through, stale and RPZ replies never carry an upstream's AA; only the authoritative stage sets it (`cache::tests::upstream_aa_is_cleared_on_every_served_reply`).

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib authoritative` and `scripts/dev-exec.sh cargo test --locked -p nexora-engine --test authoritative_pipeline` — expect PASS; run `scripts/dev-exec.sh make engine-test` — expect PASS including `cache_hit_path_does_not_allocate` (`hot_path_alloc.rs`), `server_pipeline` and `policy_pipeline` (snapshots without `auth_zones` leave `rt.auth` empty, so the stage is one length check).
- [ ] Commit: `git add engine && git commit -m "feat(engine): load authoritative zones incrementally and answer them before the ACL"`.

## Task 5: Management-plane zones, records, served-image builder, journal, snapshot and API

Files:

- `mgmt/migrations/00400_zones.sql` — zones, records, images, journal (blobs live in M1's `blobs`), TSIG keys
- `mgmt/internal/store/storetest/storetest.go` — `New(t) *store.Store`: a fresh PostgreSQL (`harness.StartPostgres`), opened and migrated
- `mgmt/internal/zone/model.go` — `Zone`, `Record`, inputs, `ManagedTypes`
- `mgmt/internal/zone/errors.go` — `ErrNotFound`/`ErrConflict` (the `store` sentinels), `ErrReadOnly`, `ValidationError`
- `mgmt/internal/zone/validate.go` — name, RDATA, CNAME/DNAME, apex NS rules
- `mgmt/internal/zone/service.go` — CRUD, each mutation one `snapshot.Mutate` transaction
- `mgmt/internal/zone/build.go` — `Rebuild`: served set, diff, serial, journal, images (via `store.PutBlob`), trimming
- `mgmt/internal/zone/service_test.go`
- `mgmt/internal/snapshot/authzones.go`, `mgmt/internal/snapshot/authzones_test.go` — fill `ConfigSnapshot.auth_zones`
- `mgmt/internal/snapshot/snapshot.go` — `Build` calls `AddAuthZones` (modify)
- `mgmt/internal/blocklist/fetcher.go` — `collectBlobs` keeps blobs referenced by `auth_zones`, `zone_images` and `zone_journal` (modify)
- `mgmt/api/openapi.yaml` — zone and record operations; optional `details` on `Error` (modify)
- `mgmt/internal/api/zones.go` — strict-server handlers
- `mgmt/internal/api/server.go` — `Deps.Zones`, `mapError` cases for `*zone.ValidationError` and `zone.ErrReadOnly` (modify)
- `mgmt/internal/auth/permissions.go`, `web/src/auth/permissions.ts` — operation roles (modify)
- `mgmt/cmd/nexora-mgmt/main.go` — construct `zone.Service` and pass it in `api.Deps` (modify)
- `mgmt/internal/api/gen.go`, `web/src/api/schema.d.ts` — regenerated
- `e2e/harness/dnsclient.go` — `QueryOpts.DO` (modify)
- `e2e/harness/authdns.go` — `WaitDNSAnswer`
- `e2e/authoritative_test.go` — `startAuthEnv` helpers and `TestAuthoritativeZonePropagation`

Interfaces:

```go
package storetest
func New(t *testing.T) *store.Store // harness.New(t).StartPostgres(), store.Open, Migrate, closed on cleanup

package zone
// Actors are auth.Actor values: API callers pass PrincipalFrom(ctx).Actor(); the management plane uses
// auth.Actor{Type: "system", ID: "xfrin", Name: "system:xfrin"} and, for dynamic updates,
// auth.Actor{Type: "system", ID: "tsig:<key>@<engine id>", Name: "tsig:<key>"}.
type Signer interface { // implemented by dnssec (Task 12); nil-safe: unsigned zones skip it
	Sign(ctx context.Context, tx pgx.Tx, z *Zone, rrs []dns.RR, now time.Time) (served []dns.RR, err error)
	ResignSOA(ctx context.Context, tx pgx.Tx, z *Zone, served []dns.RR, now time.Time) ([]dns.RR, error)
}
type Service struct{ Store *store.Store; Build snapshot.BuildConfig; Signer Signer; Now func() time.Time }
var ManagedTypes = map[uint16]bool{dns.TypeA: true, dns.TypeAAAA: true, dns.TypeCNAME: true, dns.TypeDNAME: true, dns.TypeMX: true, dns.TypeNS: true, dns.TypePTR: true, dns.TypeSRV: true, dns.TypeTXT: true, dns.TypeCAA: true, dns.TypeSSHFP: true, dns.TypeTLSA: true, dns.TypeHTTPS: true, dns.TypeSVCB: true, dns.TypeDS: true, dns.TypeNAPTR: true, dns.TypeLOC: true}
type SOA struct{ MName, RName string; Refresh, Retry, Expire, Minimum, TTL uint32 }
type Endpoint struct{ Address string `json:"address"`; TSIGKeyID *uuid.UUID `json:"tsig_key_id"` }
type Zone struct {
	ID uuid.UUID; Name, Kind string; Revision int64; Serial uint32; DefaultTTL uint32; SOA SOA
	TransferAllowCIDRs []netip.Prefix; TransferTSIGKeyID *uuid.UUID; Notify []Endpoint; UpdateTSIGKeyIDs []uuid.UUID
	Primaries []Endpoint; CurrentSeq, ImageSeq int64; Loaded, Expired bool
	LastRefreshAt, LastSuccessAt, NextRefreshAt, ExpiresAt *time.Time; LastError, LastTrigger string
	DNSSECEnabled bool; CreatedAt, UpdatedAt time.Time
}
type Record struct{ ID uuid.UUID; ZoneID uuid.UUID; Name string; Type string; TTL uint32; Data string; Revision int64 }
type CreateZoneInput struct{ Name, Kind string; DefaultTTL uint32; SOA SOA; Nameservers []string; Primaries []Endpoint; Transfer TransferInput; Notify []Endpoint; UpdateTSIGKeyIDs []uuid.UUID }
type TransferInput struct{ AllowCIDRs []string; TSIGKeyID *uuid.UUID }
type UpdateZoneInput struct{ Revision int64; DefaultTTL *uint32; SOA *SOA; Primaries *[]Endpoint; Transfer *TransferInput; Notify *[]Endpoint; UpdateTSIGKeyIDs *[]uuid.UUID }
type RecordInput struct{ Name, Type string; TTL uint32; Data string }
type ValidationError struct{ Code, Message string; Details []LineError }
type LineError struct{ Line int `json:"line"`; Message string `json:"message"` }
var ErrNotFound, ErrConflict = store.ErrNotFound, store.ErrConflict // wrapped with detail; API maps them as for every M1-M3 resource
var ErrReadOnly = errors.New("zone is a secondary zone and read-only")

func (s *Service) CreateZone(ctx context.Context, actor auth.Actor, in CreateZoneInput) (*Zone, error)
func (s *Service) GetZone(ctx context.Context, id uuid.UUID) (*Zone, error)
func (s *Service) ListZones(ctx context.Context) ([]Zone, error)
func (s *Service) UpdateZone(ctx context.Context, actor auth.Actor, id uuid.UUID, in UpdateZoneInput) (*Zone, error)
func (s *Service) DeleteZone(ctx context.Context, actor auth.Actor, id uuid.UUID, revision int64) error
func (s *Service) ListRecords(ctx context.Context, zoneID uuid.UUID, name, rtype string, after string, limit int) ([]Record, string, error)
func (s *Service) CreateRecord(ctx context.Context, actor auth.Actor, zoneID uuid.UUID, in RecordInput) (*Record, error)
func (s *Service) UpdateRecord(ctx context.Context, actor auth.Actor, zoneID, recordID uuid.UUID, revision int64, in RecordInput) (*Record, error)
func (s *Service) DeleteRecord(ctx context.Context, actor auth.Actor, zoneID, recordID uuid.UUID, revision int64) error
// Mutate is the single transactional path used by records, import (Task 9), xfr-in (Task 10), dynamic updates (Task 11), DNSSEC (Task 12/14).
// It runs snapshot.Mutate(ctx, s.Store, s.Build, actor, …): lock the zone row, fn, Rebuild, then return
// auth.Change{Action: auditAction, TargetType: "zone", TargetID: zone id, Before: before, After: after}.
func (s *Service) Mutate(ctx context.Context, zoneID uuid.UUID, fn func(tx pgx.Tx, z *Zone) (auditAction string, before, after any, opts RebuildOptions, err error), actor auth.Actor) (*Zone, error)
type RebuildOptions struct{ Serial *uint32; Force bool }
func Rebuild(ctx context.Context, tx pgx.Tx, signer Signer, z *Zone, opts RebuildOptions, now time.Time) (changed bool, err error)
func LoadServed(ctx context.Context, tx pgx.Tx, z *Zone) ([]dns.RR, error) // decode image + journal deltas
func SetRecords(ctx context.Context, tx pgx.Tx, zoneID uuid.UUID, rrs []dns.RR) error  // replace all rows

package snapshot
func AddAuthZones(ctx context.Context, tx pgx.Tx, snap *controlv1.ConfigSnapshot) error
```

OpenAPI operations: `listZones`, `createZone`, `getZone`, `updateZone`, `deleteZone`, `listZoneRecords`, `createZoneRecord`, `updateZoneRecord`, `deleteZoneRecord`.

- [ ] Write `mgmt/migrations/00400_zones.sql`:

```sql
-- +goose Up
CREATE TABLE tsig_keys (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name            text NOT NULL UNIQUE CHECK (name ~ '^([a-z0-9_-]{1,63}\.)+$'),
    algorithm       text NOT NULL CHECK (algorithm IN ('hmac-sha256', 'hmac-sha384', 'hmac-sha512')),
    secret_envelope bytea NOT NULL CHECK (substring(secret_envelope from 1 for 4) = 'NXE1'::bytea),
    revision        bigint NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE zones (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                 text NOT NULL UNIQUE CHECK (name ~ '\.$' AND name <> '.'),
    kind                 text NOT NULL CHECK (kind IN ('primary', 'secondary')),
    revision             bigint NOT NULL DEFAULT 1,
    serial               bigint NOT NULL DEFAULT 1 CHECK (serial BETWEEN 0 AND 4294967295),
    default_ttl          integer NOT NULL DEFAULT 3600 CHECK (default_ttl >= 0),
    soa_mname            text NOT NULL,
    soa_rname            text NOT NULL,
    soa_refresh          integer NOT NULL DEFAULT 10800 CHECK (soa_refresh > 0),
    soa_retry            integer NOT NULL DEFAULT 3600 CHECK (soa_retry > 0),
    soa_expire           integer NOT NULL DEFAULT 1209600 CHECK (soa_expire > 0),
    soa_minimum          integer NOT NULL DEFAULT 3600 CHECK (soa_minimum >= 0),
    soa_ttl              integer NOT NULL DEFAULT 3600 CHECK (soa_ttl >= 0),
    transfer_allow_cidrs cidr[] NOT NULL DEFAULT '{}',
    transfer_tsig_key_id uuid REFERENCES tsig_keys(id),
    notify_targets       jsonb NOT NULL DEFAULT '[]',
    update_tsig_key_ids  uuid[] NOT NULL DEFAULT '{}',
    primaries            jsonb NOT NULL DEFAULT '[]',
    current_seq          bigint NOT NULL DEFAULT 0,
    image_seq            bigint NOT NULL DEFAULT 0,
    loaded               boolean NOT NULL DEFAULT false,
    last_refresh_at      timestamptz,
    last_success_at      timestamptz,
    next_refresh_at      timestamptz,
    expires_at           timestamptz,
    expired              boolean NOT NULL DEFAULT false,
    last_error           text NOT NULL DEFAULT '',
    last_trigger         text NOT NULL DEFAULT '',
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE zone_records (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    zone_id    uuid NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
    owner      text NOT NULL,
    rtype      integer NOT NULL CHECK (rtype BETWEEN 1 AND 65535),
    ttl        integer NOT NULL CHECK (ttl >= 0),
    rdata      text NOT NULL,
    rdata_wire bytea NOT NULL,
    revision   bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX zone_records_rr ON zone_records (zone_id, lower(owner), rtype, sha256(rdata_wire));
CREATE INDEX zone_records_owner ON zone_records (zone_id, lower(owner));

CREATE TABLE zone_images (
    zone_id     uuid NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
    seq         bigint NOT NULL,
    serial      bigint NOT NULL,
    blob_sha256 text NOT NULL REFERENCES blobs(sha256),
    raw_size    bigint NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (zone_id, seq)
);

CREATE TABLE zone_journal (
    zone_id     uuid NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
    seq         bigint NOT NULL,
    from_serial bigint NOT NULL,
    to_serial   bigint NOT NULL,
    blob_sha256 text NOT NULL REFERENCES blobs(sha256),
    raw_size    bigint NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (zone_id, seq)
);

-- +goose Down
DROP TABLE zone_journal;
DROP TABLE zone_images;
DROP TABLE zone_records;
DROP TABLE zones;
DROP TABLE tsig_keys;
```

`seq` (monotonic per zone) orders journal and images because serials wrap. `tsig_keys.secret_envelope` holds an NXE1 envelope from `mgmt/internal/secrets`, checked like M3's `rpz_zones.tsig_secret_envelope`.

- [ ] Write `mgmt/internal/store/storetest/storetest.go`:

```go
// Package storetest opens a fresh, migrated database for package tests.
package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// New starts a PostgreSQL instance for this test, migrates it and returns the store.
func New(t *testing.T) *store.Store {
	t.Helper()
	pg := harness.New(t).StartPostgres()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}
```

- [ ] Write the failing `mgmt/internal/zone/service_test.go`:

```go
package zone_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/nzf"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

var actor = auth.Actor{Type: "user", ID: "test", Name: "test"}

func newService(t *testing.T) *zone.Service {
	return &zone.Service{Store: storetest.New(t), Now: time.Now}
}

// published returns the number of config versions and the audit actions in order.
func published(t *testing.T, s *zone.Service) (int, []string) {
	t.Helper()
	ctx := context.Background()
	var versions int
	if err := s.Store.Pool.QueryRow(ctx, `SELECT count(*) FROM config_versions`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	var actions []string
	if err := s.Store.Pool.QueryRow(ctx, `SELECT coalesce(array_agg(action ORDER BY id), '{}') FROM audit_log WHERE target_type = 'zone'`).Scan(&actions); err != nil {
		t.Fatal(err)
	}
	return versions, actions
}

func createZone(t *testing.T, s *zone.Service, name string) *zone.Zone {
	t.Helper()
	z, err := s.CreateZone(context.Background(), actor, zone.CreateZoneInput{
		Name: name, Kind: "primary", DefaultTTL: 300,
		SOA:         zone.SOA{MName: "ns1." + name, RName: "hostmaster." + name},
		Nameservers: []string{"ns1." + name},
	})
	if err != nil {
		t.Fatalf("CreateZone: %v", err)
	}
	return z
}

func TestCreateRecordBumpsSerialJournalAndPublishes(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	z := createZone(t, s, "unit.test.")
	if z.Serial != 1 || z.CurrentSeq != 1 || z.ImageSeq != 1 {
		t.Fatalf("new zone: serial=%d seq=%d image=%d", z.Serial, z.CurrentSeq, z.ImageSeq)
	}
	versionsBefore, _ := published(t, s)
	if _, err := s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "www.unit.test.", Type: "A", TTL: 300, Data: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	z2, _ := s.GetZone(ctx, z.ID)
	if z2.Serial != 2 || z2.CurrentSeq != 2 || z2.Revision <= z.Revision {
		t.Fatalf("after record: serial=%d seq=%d revision %d->%d", z2.Serial, z2.CurrentSeq, z.Revision, z2.Revision)
	}
	var from, to int64
	var blob []byte
	err := s.Store.Pool.QueryRow(ctx, `SELECT j.from_serial, j.to_serial, b.data FROM zone_journal j JOIN blobs b ON b.sha256 = j.blob_sha256 WHERE j.zone_id=$1 AND j.seq=2`, z.ID).Scan(&from, &to, &blob)
	if err != nil || from != 1 || to != 2 {
		t.Fatalf("journal row: %d->%d err=%v", from, to, err)
	}
	raw, err := nzf.Decompress(blob, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	_, _, d, err := nzf.Decode(raw)
	if err != nil || len(d.Added) != 2 || d.Added[0].Type != dns.TypeSOA || d.Added[1].Type != dns.TypeA {
		t.Fatalf("delta: %+v err=%v", d, err)
	}
	versions, actions := published(t, s)
	if versions != versionsBefore+1 || len(actions) != 2 || actions[0] != "createZone" || actions[1] != "createZoneRecord" {
		t.Fatalf("config versions %d -> %d, audit=%v", versionsBefore, versions, actions)
	}
}

func TestStaleRecordRevisionConflicts(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	z := createZone(t, s, "conflict.test.")
	other := auth.Actor{Type: "user", ID: "other", Name: "other"}
	r, err := s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "www.conflict.test.", Type: "A", TTL: 300, Data: "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateRecord(ctx, actor, z.ID, r.ID, r.Revision, zone.RecordInput{Name: "www.conflict.test.", Type: "A", TTL: 300, Data: "192.0.2.2"}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	_, err = s.UpdateRecord(ctx, other, z.ID, r.ID, r.Revision, zone.RecordInput{Name: "www.conflict.test.", Type: "A", TTL: 300, Data: "192.0.2.3"})
	if !errors.Is(err, zone.ErrConflict) {
		t.Fatalf("stale update: got %v, want ErrConflict", err)
	}
	if err := s.DeleteRecord(ctx, other, z.ID, r.ID, r.Revision); !errors.Is(err, zone.ErrConflict) {
		t.Fatalf("stale delete: got %v", err)
	}
}

func TestValidationRules(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	z := createZone(t, s, "rules.test.")
	mustCode := func(in zone.RecordInput, code string) {
		t.Helper()
		_, err := s.CreateRecord(ctx, actor, z.ID, in)
		var ve *zone.ValidationError
		if !errors.As(err, &ve) || ve.Code != code {
			t.Fatalf("%+v: got %v, want %s", in, err, code)
		}
	}
	if _, err := s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "www.rules.test.", Type: "A", TTL: 300, Data: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	mustCode(zone.RecordInput{Name: "www.rules.test.", Type: "CNAME", TTL: 300, Data: "other.example."}, "cname_conflict")
	mustCode(zone.RecordInput{Name: "www.example.org.", Type: "A", TTL: 300, Data: "192.0.2.1"}, "out_of_zone")
	mustCode(zone.RecordInput{Name: "x.rules.test.", Type: "HINFO", TTL: 300, Data: "a b"}, "unsupported_type")
	mustCode(zone.RecordInput{Name: "rules.test.", Type: "SOA", TTL: 300, Data: "a. b. 1 2 3 4 5"}, "unsupported_type")
	mustCode(zone.RecordInput{Name: "x.rules.test.", Type: "A", TTL: 300, Data: "not-an-ip"}, "invalid_rdata")
	recs, _, _ := s.ListRecords(ctx, z.ID, "rules.test.", "NS", "", 10)
	var ve *zone.ValidationError
	if err := s.DeleteRecord(ctx, actor, z.ID, recs[0].ID, recs[0].Revision); !errors.As(err, &ve) || ve.Code != "last_apex_ns" {
		t.Fatalf("deleting the last apex NS: got %v, want last_apex_ns", err)
	}
}

func TestSerialWrapsAroundRFC1982(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	z := createZone(t, s, "wrap.test.")
	if _, err := s.Store.Pool.Exec(ctx, `UPDATE zones SET serial = 4294967295 WHERE id = $1`, z.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "a.wrap.test.", Type: "A", TTL: 300, Data: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	z2, _ := s.GetZone(ctx, z.ID)
	if z2.Serial != 0 {
		t.Fatalf("serial after 4294967295 = %d, want 0", z2.Serial)
	}
	var from, to int64
	_ = s.Store.Pool.QueryRow(ctx, `SELECT from_serial, to_serial FROM zone_journal WHERE zone_id=$1 ORDER BY seq DESC LIMIT 1`, z.ID).Scan(&from, &to)
	if from != 4294967295 || to != 0 {
		t.Fatalf("journal %d->%d", from, to)
	}
}

func TestRebuildWithoutChangesKeepsSerial(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	z := createZone(t, s, "same.test.")
	tx, _ := s.Store.Pool.Begin(ctx)
	defer tx.Rollback(ctx)
	changed, err := zone.Rebuild(ctx, tx, nil, z, zone.RebuildOptions{}, time.Now())
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/zone/ -count=1` — expect FAIL with "undefined: zone.Service".
- [ ] Implement `validate.go`: names absolute (append the zone name to relative input only in the GUI, the API requires absolute), inside the zone (`dns.IsSubDomain(zone, name)`), type in `ManagedTypes` (SOA and DNSSEC-generated types → `unsupported_type`), RDATA parsed with `dns.NewRR(fmt.Sprintf("%s %d IN %s %s", name, ttl, typ, data))` (`invalid_rdata` with the parser message), normalised `data` = `rr.String()` after the fourth tab-separated field; CNAME may not coexist with other types at the owner and vice versa (`cname_conflict`); no records strictly below a DNAME owner (`dname_occludes`); DS only at a delegation owner (`ds_not_at_delegation`); the apex keeps ≥ 1 NS (`last_apex_ns`); TTL ≤ 2147483647.
- [ ] Implement `service.go`. Every mutation is one `snapshot.Mutate(ctx, s.Store, s.Build, actor, fn)` call whose `fn`: `SELECT … FROM zones WHERE id=$1 FOR UPDATE` (missing → `fmt.Errorf("zone %s: %w", id, store.ErrNotFound)`) → mutate rows (records: `UPDATE … WHERE id=$1 AND revision=$2` with 0 rows → `fmt.Errorf("record revision %d is stale: %w", rev, store.ErrConflict)`, rows exist check → `store.ErrNotFound`) → set the RRset TTL to the written TTL (`UPDATE zone_records SET ttl=$1, revision=revision+1 WHERE zone_id=$2 AND lower(owner)=lower($3) AND rtype=$4 AND ttl<>$1`) → `UPDATE zones SET revision=revision+1` → `Rebuild` → return `auth.Change{Action: <operationId>, TargetType: "zone", TargetID: <zone id>, Before, After}`; `snapshot.Mutate` then writes the audit row, builds the snapshot (which includes `auth_zones`, see below), inserts `config_versions` and notifies, all in the same transaction. Record mutations on `kind='secondary'` zones return `ErrReadOnly`. `CreateZone` inserts the row and one NS record per nameserver, then `Rebuild` with `Force: true`. `UpdateZone` / `DeleteZone` require the zone revision. `rdata_wire` is `nzf.FromRR(rr).RData`.
- [ ] Implement `build.go`:

```go
const (
	journalListed   = 32  // deltas advertised in the snapshot beyond the image
	journalKept     = 100 // rows kept in zone_journal
	imageEveryDelta = 64
)

func Rebuild(ctx context.Context, tx pgx.Tx, signer Signer, z *Zone, opts RebuildOptions, now time.Time) (bool, error) {
	desired, err := desiredRRs(ctx, tx, z) // SOA from zone row (serial = z.Serial) + zone_records
	if err != nil {
		return false, err
	}
	if z.DNSSECEnabled && signer != nil {
		if desired, err = signer.Sign(ctx, tx, z, desired, now); err != nil {
			return false, err
		}
	}
	var previous []dns.RR
	if z.CurrentSeq > 0 {
		if previous, err = LoadServed(ctx, tx, z); err != nil {
			return false, err
		}
	}
	deleted, added := diffIgnoringSOA(previous, desired) // multiset diff on nzf wire bytes; SOA and RRSIG(SOA) excluded
	soaChanged := z.CurrentSeq == 0 || !soaFieldsEqual(previous, desired)
	if len(deleted) == 0 && len(added) == 0 && !soaChanged && !opts.Force && opts.Serial == nil {
		return false, nil
	}
	oldSerial := z.Serial
	newSerial := SerialNext(oldSerial)
	if z.Kind == "secondary" {
		newSerial = *opts.Serial
	} else if opts.Serial != nil && SerialLess(oldSerial, *opts.Serial) {
		newSerial = *opts.Serial
	}
	if z.CurrentSeq == 0 && opts.Serial == nil {
		newSerial = oldSerial // first version keeps the initial serial
	}
	desired = setSOASerial(desired, newSerial)
	if z.DNSSECEnabled && signer != nil {
		if desired, err = signer.ResignSOA(ctx, tx, z, desired, now); err != nil {
			return false, err
		}
	}
	seq := z.CurrentSeq + 1
	if z.CurrentSeq > 0 {
		deleted = append([]dns.RR{soaOf(previous), rrsigsOfSOA(previous)...}, deleted...)
		added = append([]dns.RR{soaOf(desired), rrsigsOfSOA(desired)...}, added...)
		raw, err := encodeDelta(z.Name, oldSerial, newSerial, deleted, added)
		if err != nil {
			return false, err
		}
		sha, err := putBlob(ctx, tx, raw)
		if err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO zone_journal (zone_id, seq, from_serial, to_serial, blob_sha256, raw_size) VALUES ($1,$2,$3,$4,$5,$6)`,
			z.ID, seq, oldSerial, newSerial, sha, len(raw)); err != nil {
			return false, err
		}
	}
	if needImage(ctx, tx, z, seq) { // image_seq == 0, seq-image_seq >= imageEveryDelta, or journal bytes since image*4 > image raw_size
		raw, err := encodeFull(z.Name, newSerial, desired)
		if err != nil {
			return false, err
		}
		sha, err := putBlob(ctx, tx, raw)
		if err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO zone_images (zone_id, seq, serial, blob_sha256, raw_size) VALUES ($1,$2,$3,$4,$5)`, z.ID, seq, newSerial, sha, len(raw)); err != nil {
			return false, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM zone_images WHERE zone_id=$1 AND seq < $2`, z.ID, seq); err != nil {
			return false, err
		}
		z.ImageSeq = seq
	}
	if _, err := tx.Exec(ctx, `DELETE FROM zone_journal WHERE zone_id=$1 AND seq <= $2 AND seq <= $3`, z.ID, seq-journalKept, z.ImageSeq); err != nil {
		return false, err
	}
	z.Serial, z.CurrentSeq = newSerial, seq
	_, err = tx.Exec(ctx, `UPDATE zones SET serial=$2, current_seq=$3, image_seq=$4, updated_at=now() WHERE id=$1`, z.ID, newSerial, seq, z.ImageSeq)
	return true, err
}
```

`LoadServed` reads the image blob at `image_seq` from `blobs` and applies `zone_journal` rows with `seq > image_seq` in order. `putBlob` compresses with `nzf.Compress` and stores the compressed bytes with M1's `store.PutBlob(ctx, tx, data)` (content-addressed, `on conflict do nothing`), returning its SHA-256.

- [ ] Extend `blocklist.Fetcher.collectBlobs` (M1's hourly blob GC under `nexora:blob-gc`): add every `snap.GetAuthZones()` image and delta `sha256` of the kept config versions to `keep`, and add `and sha256 not in (select blob_sha256 from zone_images) and sha256 not in (select blob_sha256 from zone_journal)` to the delete. No separate zone blob GC exists.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/zone/ -count=1` — expect PASS.
- [ ] Write the failing `mgmt/internal/snapshot/authzones_test.go`:

```go
package snapshot_test

import (
	"context"
	"testing"
	"time"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

func TestAddAuthZonesListsImageAndContiguousDeltas(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	actor := auth.Actor{Type: "user", ID: "t", Name: "t"}
	s := &zone.Service{Store: st, Now: time.Now}
	z, err := s.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "snap.test.", Kind: "primary", DefaultTTL: 300,
		SOA: zone.SOA{MName: "ns1.snap.test.", RName: "h.snap.test."}, Nameservers: []string{"ns1.snap.test."}})
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"} {
		if _, err := s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "www.snap.test.", Type: "A", TTL: 300, Data: ip}); err != nil {
			t.Fatal(err)
		}
	}
	tx, _ := st.Pool.Begin(ctx)
	defer tx.Rollback(ctx)
	snap := &controlv1.ConfigSnapshot{}
	if err := snapshot.AddAuthZones(ctx, tx, snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.AuthZones) != 1 {
		t.Fatalf("zones: %d", len(snap.AuthZones))
	}
	az := snap.AuthZones[0]
	if az.Name != "snap.test." || az.Serial != 4 || az.ImageSerial != 1 || az.ImageDeltaOffset != 0 || len(az.Deltas) != 3 {
		t.Fatalf("auth zone: %+v", az)
	}
	for i, d := range az.Deltas {
		if d.FromSerial != uint32(i+1) || d.ToSerial != uint32(i+2) || len(d.Blob.Sha256) != 64 {
			t.Fatalf("delta %d: %+v", i, d)
		}
	}
	var stored int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM blobs WHERE sha256 = ANY($1)`, []string{az.Image.Sha256, az.Deltas[2].Blob.Sha256}).Scan(&stored); err != nil || stored != 2 {
		t.Fatalf("image and delta blobs in blobs: %d %v", stored, err)
	}
	_, latest, err := snapshot.Latest(ctx, tx)
	if err != nil || len(latest.AuthZones) != 1 || latest.AuthZones[0].Serial != 4 {
		t.Fatalf("published snapshot does not carry the zone: %v %v", latest.GetAuthZones(), err)
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/snapshot/ -run TestAddAuthZones -count=1` — expect FAIL with "undefined: snapshot.AddAuthZones".
- [ ] Implement `AddAuthZones`: for each zone with `kind='primary' OR loaded`, ordered by name, emit `AuthZone` with `image` = `BlobRef{Sha256, Size (from blobs.size), Name: "<zone>@<serial>"}` from `zone_images` at `image_seq`, deltas `SELECT … FROM zone_journal WHERE zone_id=$1 AND seq > LEAST($image_seq, $current_seq - 32) ORDER BY seq` (each with its `BlobRef`), `image_delta_offset` = count of listed rows with `seq <= image_seq`, transfer CIDRs, notify/primaries addresses, TSIG key names resolved via `tsig_keys`, `update_tsig_keys`, `expired`. Call it at the end of `snapshot.Build` (after `ApplyResolution`), so every published version carries the zones. Engines fetch the blobs with M1's unchanged `GetBlob` (`select data from blobs`).
- [ ] Run the snapshot test again — expect PASS.
- [ ] Add the OpenAPI operations (JSON bodies; `revision` on every editable resource):

```yaml
/zones:
  get:
    {
      operationId: listZones,
      tags: [zones],
      responses:
        {
          "200":
            {
              description: zones,
              content:
                {
                  application/json:
                    {
                      schema:
                        {
                          type: array,
                          items: { $ref: "#/components/schemas/Zone" },
                        },
                    },
                },
            },
        },
    }
  post:
    operationId: createZone
    tags: [zones]
    requestBody:
      {
        required: true,
        content:
          {
            application/json:
              { schema: { $ref: "#/components/schemas/ZoneCreate" } },
          },
      }
    responses:
      "201":
        {
          description: created,
          content:
            {
              application/json:
                { schema: { $ref: "#/components/schemas/Zone" } },
            },
        }
      "409": { $ref: "#/components/responses/Error" }
      "422": { $ref: "#/components/responses/Error" }
/zones/{zoneId}:
  parameters:
    [
      {
        name: zoneId,
        in: path,
        required: true,
        schema: { type: string, format: uuid },
      },
    ]
  get:
    {
      operationId: getZone,
      tags: [zones],
      responses:
        {
          "200":
            {
              description: zone,
              content:
                {
                  application/json:
                    { schema: { $ref: "#/components/schemas/Zone" } },
                },
            },
          "404": { $ref: "#/components/responses/Error" },
        },
    }
  patch:
    operationId: updateZone
    tags: [zones]
    requestBody:
      {
        required: true,
        content:
          {
            application/json:
              { schema: { $ref: "#/components/schemas/ZoneUpdate" } },
          },
      }
    responses:
      {
        "200":
          {
            description: zone,
            content:
              {
                application/json:
                  { schema: { $ref: "#/components/schemas/Zone" } },
              },
          },
        "409": { $ref: "#/components/responses/Error" },
        "422": { $ref: "#/components/responses/Error" },
      }
  delete:
    operationId: deleteZone
    tags: [zones]
    parameters:
      [
        {
          name: revision,
          in: query,
          required: true,
          schema: { type: integer, format: int64 },
        },
      ]
    responses:
      {
        "204": { description: deleted },
        "409": { $ref: "#/components/responses/Error" },
      }
/zones/{zoneId}/records:
  parameters:
    [
      {
        name: zoneId,
        in: path,
        required: true,
        schema: { type: string, format: uuid },
      },
    ]
  get:
    operationId: listZoneRecords
    tags: [zones]
    parameters:
      - { name: name, in: query, schema: { type: string } }
      - { name: type, in: query, schema: { type: string } }
      - { name: cursor, in: query, schema: { type: string } }
      - {
          name: limit,
          in: query,
          schema: { type: integer, minimum: 1, maximum: 1000, default: 200 },
        }
    responses:
      {
        "200":
          {
            description: page,
            content:
              {
                application/json:
                  { schema: { $ref: "#/components/schemas/RecordPage" } },
              },
          },
      }
  post:
    operationId: createZoneRecord
    tags: [zones]
    requestBody:
      {
        required: true,
        content:
          {
            application/json:
              { schema: { $ref: "#/components/schemas/RecordInput" } },
          },
      }
    responses:
      {
        "201":
          {
            description: created,
            content:
              {
                application/json:
                  { schema: { $ref: "#/components/schemas/Record" } },
              },
          },
        "422": { $ref: "#/components/responses/Error" },
      }
/zones/{zoneId}/records/{recordId}:
  parameters:
    - {
        name: zoneId,
        in: path,
        required: true,
        schema: { type: string, format: uuid },
      }
    - {
        name: recordId,
        in: path,
        required: true,
        schema: { type: string, format: uuid },
      }
  put:
    operationId: updateZoneRecord
    tags: [zones]
    requestBody:
      {
        required: true,
        content:
          {
            application/json:
              {
                schema:
                  {
                    allOf: [{ $ref: "#/components/schemas/RecordInput" }],
                    required: [revision],
                    properties: { revision: { type: integer, format: int64 } },
                  },
              },
          },
      }
    responses:
      {
        "200":
          {
            description: updated,
            content:
              {
                application/json:
                  { schema: { $ref: "#/components/schemas/Record" } },
              },
          },
        "409": { $ref: "#/components/responses/Error" },
        "422": { $ref: "#/components/responses/Error" },
      }
  delete:
    operationId: deleteZoneRecord
    tags: [zones]
    parameters:
      [
        {
          name: revision,
          in: query,
          required: true,
          schema: { type: integer, format: int64 },
        },
      ]
    responses:
      {
        "204": { description: deleted },
        "409": { $ref: "#/components/responses/Error" },
      }
```

Every error response above is M1's `#/components/responses/Error`; M4 adds an optional `details: { type: array, items: { type: object, required: [line, message], properties: { line: { type: integer }, message: { type: string } } } }` property to the `Error` schema. Schemas: `Zone` (id, name, kind `primary|secondary`, revision, serial, default_ttl, soa{mname,rname,refresh,retry,expire,minimum,ttl}, transfer{allow_cidrs[], tsig_key_id|null}, notify[{address, tsig_key_id|null}], update{tsig_key_ids[]}, primaries[{address, tsig_key_id|null}], secondary_status{last_refresh_at, last_success_at, next_refresh_at, expires_at, expired, last_error, last_trigger}|null, dnssec_enabled, created_at, updated_at); `ZoneCreate` (name, kind, default_ttl, soa{mname,rname,+optional timers}, nameservers[] (primary, min 1), primaries[] (secondary, min 1), transfer, notify, update); `ZoneUpdate` (revision required + optional fields of ZoneCreate except name/kind/nameservers); `RecordInput` (name, type enum of the 17 managed types, ttl 0..2147483647, data); `Record` (RecordInput + id, revision); `RecordPage` (items, next_cursor|null); `ValidationError` response `{code, message, details: [{line, message}]}`.

- [ ] Implement `mgmt/internal/api/zones.go` (handlers call `h.d.Zones` with `PrincipalFrom(ctx).Actor()`; request bodies validated with `invalid(...)` as in `rpz.go`). `ErrNotFound`/`ErrConflict` are the `store` sentinels, so `mapError` already answers 404 `not_found` / 409 `conflict`; add two cases to `mapError` in `server.go`: `errors.Is(err, zone.ErrReadOnly)` → 422 `zone_read_only`, and `errors.As(err, &zve)` with `zve *zone.ValidationError` → 422 with `zve.Code`, `zve.Message` and `details` (write `Error{Code, Message, Details}`). `api.Deps` gains `Zones *zone.Service`. Register permissions in `mgmt/internal/auth/permissions.go` and `web/src/auth/permissions.ts`: `listZones`, `getZone`, `listZoneRecords` → viewer; `createZone`, `updateZone`, `deleteZone`, `createZoneRecord`, `updateZoneRecord`, `deleteZoneRecord` → operator. Regenerate the API. In `main.go` pass `Zones: &zone.Service{Store: st, Build: build, Now: time.Now}` in `api.Deps`.
- [ ] Add `DO bool` to `harness.QueryOpts` in `e2e/harness/dnsclient.go` (`Query` adds EDNS when `o.EDNSSize > 0 || o.Cookie != nil || o.DO` and calls `m.SetEdns0(size, o.DO)`), and write `e2e/harness/authdns.go`:

```go
package harness

import (
	"testing"
	"time"

	"github.com/miekg/dns"
)

// WaitDNSAnswer polls name/qtype over UDP every 100 ms until ok accepts the reply, failing the test
// with the last reply or error after timeout.
func WaitDNSAnswer(t *testing.T, server, name string, qtype uint16, timeout time.Duration, ok func(*dns.Msg) bool) *dns.Msg {
	t.Helper()
	var last *dns.Msg
	var lastErr error
	deadline := time.Now().Add(timeout)
	for {
		r, _, err := Query(t, server, name, qtype, QueryOpts{Timeout: 300 * time.Millisecond})
		if err == nil && ok(r) {
			return r
		}
		last, lastErr = r, err
		if time.Now().After(deadline) {
			t.Fatalf("%s %s @%s not satisfied within %s; last=%v err=%v", name, dns.TypeToString[qtype], server, timeout, last, lastErr)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
```

- [ ] Write the failing `e2e/authoritative_test.go` (its helpers are shared by the M4 e2e tests):

```go
package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
)

type zoneResp struct {
	ID       string `json:"id"`
	Serial   uint32 `json:"serial"`
	Revision int64  `json:"revision"`
}

// authEnv is a management plane (bootstrapped admin API) with managed engines.
type authEnv struct {
	env     *harness.Env
	pg      *harness.Postgres
	ca      *harness.CA
	mg      *harness.Mgmt
	api     *harness.API
	engines []*harness.Engine
}

// startAuthEnv starts PostgreSQL, one management plane with extraEnv and one managed engine per
// node name, and waits until every engine applied the latest version.
func startAuthEnv(t *testing.T, extraEnv []string, nodes ...string) authEnv {
	t.Helper()
	e := authEnv{env: harness.New(t)}
	e.pg, e.ca = e.env.StartPostgres(), e.env.InitCA()
	e.mg = e.env.StartMgmt(e.pg, e.ca, harness.MgmtOptions{ExtraEnv: extraEnv})
	e.api = harness.Bootstrap(t, e.env, e.mg.SetupToken(t), e.mg.BaseURL)
	for _, n := range nodes {
		e.engines = append(e.engines, e.env.StartManagedEngine(n, []string{e.mg.GRPCURL}, e.api.CreateJoinToken()))
	}
	waitLatestApplied(t, e.api, nodes...)
	return e
}

func createPrimaryZone(t *testing.T, api *harness.API, name string, extra map[string]any) zoneResp {
	t.Helper()
	body := map[string]any{
		"name": name, "kind": "primary", "default_ttl": 300,
		"soa":         map[string]any{"mname": "ns1." + name, "rname": "hostmaster." + name},
		"nameservers": []string{"ns1." + name},
	}
	for k, v := range extra {
		body[k] = v
	}
	var z zoneResp
	api.Must(http.MethodPost, "/zones", body, &z, http.StatusCreated)
	return z
}

func addRecord(t *testing.T, api *harness.API, zoneID, name, typ, data string) {
	t.Helper()
	api.Must(http.MethodPost, "/zones/"+zoneID+"/records", map[string]any{"name": name, "type": typ, "ttl": 300, "data": data}, nil, http.StatusCreated)
}

func TestAuthoritativeZonePropagation(t *testing.T) {
	e := startAuthEnv(t, nil, "auth-1", "auth-2")
	api := e.api

	z := createPrimaryZone(t, api, "prop.test.", nil)
	addRecord(t, api, z.ID, "www.prop.test.", "A", "192.0.2.10")
	addRecord(t, api, z.ID, "child.prop.test.", "NS", "ns.child.prop.test.")
	addRecord(t, api, z.ID, "ns.child.prop.test.", "A", "192.0.2.53")
	created := time.Now()

	for i, eng := range e.engines {
		r := harness.WaitDNSAnswer(t, eng.DNS, "www.prop.test.", dns.TypeA, 5*time.Second-time.Since(created), func(m *dns.Msg) bool {
			return m.Rcode == dns.RcodeSuccess && len(m.Answer) == 1
		})
		if !r.Authoritative {
			t.Fatalf("engine %d answered without AA: %v", i, r)
		}
		if a, ok := r.Answer[0].(*dns.A); !ok || a.A.String() != "192.0.2.10" {
			t.Fatalf("engine %d answer: %v", i, r.Answer)
		}
		nx := harness.MustQuery(t, eng.DNS, "missing.prop.test.", dns.TypeA, harness.QueryOpts{TCP: true})
		if nx.Rcode != dns.RcodeNameError || !nx.Authoritative || len(nx.Ns) != 1 || nx.Ns[0].Header().Rrtype != dns.TypeSOA {
			t.Fatalf("engine %d NXDOMAIN: %v", i, nx)
		}
		ref := harness.MustQuery(t, eng.DNS, "host.child.prop.test.", dns.TypeA, harness.QueryOpts{TCP: true})
		if ref.Authoritative || len(ref.Ns) != 1 || len(ref.Extra) < 1 {
			t.Fatalf("engine %d referral: %v", i, ref)
		}
	}

	before := e.engines[0].Metric(t, "nexora_auth_zone_loads_total", map[string]string{"kind": "delta"})
	addRecord(t, api, z.ID, "api.prop.test.", "AAAA", "2001:db8::10")
	edited := time.Now()
	for _, eng := range e.engines {
		harness.WaitDNSAnswer(t, eng.DNS, "api.prop.test.", dns.TypeAAAA, 5*time.Second-time.Since(edited), func(m *dns.Msg) bool {
			return m.Authoritative && len(m.Answer) == 1
		})
	}
	if after := e.engines[0].Metric(t, "nexora_auth_zone_loads_total", map[string]string{"kind": "delta"}); after <= before {
		t.Fatalf("edit was not applied incrementally: delta loads %v -> %v", before, after)
	}
	if n := e.engines[0].Metric(t, "nexora_auth_zones", nil); n != 1 {
		t.Fatalf("nexora_auth_zones = %v, want 1", n)
	}
}
```

- [ ] Run `scripts/dev-exec.sh make e2e-build` then `scripts/dev-exec.sh go test ./e2e/ -run TestAuthoritativeZonePropagation -count=1` — before the handlers are registered expect FAIL with "POST /zones: status 404"; after the implementation steps above expect PASS.
      Built (as implemented): (1) `needImage` also requires the deltas since the image to reach `imageMinDeltaBytes` (64 KiB) before the quarter-of-the-image rule applies — without the floor every change to a small zone writes an image and `TestAddAuthZonesListsImageAndContiguousDeltas` (image serial 1 after three deltas) cannot hold. (2) When the zone row's serial differs from the served SOA serial (changed outside `Rebuild`, as `TestSerialWrapsAroundRFC1982` does), the delta's leading deleted SOA carries the row serial and a full image is written as well. (3) Record rules live in exported `zone.ParseRecord` (per record) and `zone.CheckSet` (whole record set, reused by Import); every record mutation checks the resulting set. Extra codes: `invalid_name`, `invalid_ttl`, `invalid_soa`, `invalid_zone`, `invalid_endpoint`, `invalid_cidr`, `unknown_tsig_key`, `dname_conflict`, `invalid_cursor`. `ListRecords` pages by an opaque cursor over `(lower(owner), rtype, id)`. (4) OpenAPI: `secondary_status` is omitted for primary zones instead of `null`; `updateZoneRecord` takes schema `RecordUpdate` (RecordInput fields + revision) instead of an `allOf`; SOA timers are optional in `ZoneSOAInput` (defaults on create, current values on update); 400/404 responses are listed where they occur. (5) Delta `BlobRef.name` is `<zone>@<to_serial>`. (6) `TestAuthoritativeZonePropagation` calls `waitLatestApplied` after the `www` answers and before the NXDOMAIN/referral assertions: `www` can answer from the version before the glue record exists. (7) The API unit-test helper `newAPIWith` sets `Deps.Zones`.
- [ ] Commit: `git add mgmt web/src/api/schema.d.ts web/src/auth/permissions.ts e2e && git commit -m "feat(mgmt): zones and records with journaled NZF blobs, snapshot delivery and zone API"`.

## Task 6: Key storage (secrets: PKCS#11 backend, DNSSEC signing keys), TSIG keys, KeyMaterial delivery

M3 already ships `mgmt/internal/secrets` (NXE1 envelope, file KEK from `NEXORA_KEK_FILE`, `ErrUnconfigured`/`ErrKEKMismatch`/`ErrBackendUnavailable`, wrap byte 1, 503 `key_storage_unconfigured` in `api.mapError`) and its tests (`TestEnvelopeRoundTripBindsPurposeAndDetectsTamper`, `TestWrongKEKIsReported`, `TestUnconfiguredBoxRefusesSecrets`, `TestKEKFileValidation`). This task extends that package instead of adding a new one, and delivers TSIG keys the way M3 delivers RPZ TSIG keys.

Files:

- `mgmt/internal/config/config.go`, `mgmt/internal/config/config_test.go` — `PKCS11Module`, `PKCS11TokenLabel`, `PKCS11PinFile` + all-or-none validation (modify)
- `mgmt/internal/secrets/secrets.go` — `Config`, `Open`, backend selection, wrap byte 2 in `Seal`/`Unseal`, `Close` (modify; `LoadKEKFile(path)` becomes `Open(Config{KEKFile: path})`)
- `mgmt/internal/secrets/pkcs11.go` (`//go:build cgo`) — token session pool, key generation, HSM signer, HSM AES wrap key; `mgmt/internal/secrets/pkcs11_nocgo.go` (`//go:build !cgo`) — stub whose `openHSM` refuses, so `CGO_ENABLED=0` builds (the current mgmt image) still compile but cannot use PKCS#11
- `e2e/harness/softhsm.go` — `harness.InitSoftHSM(t, label) SoftHSM{Module, Label, PinFile, Conf}` and `(SoftHSM).Env()`; `e2e/key_storage_test.go` — `TestTSIGKeysWithPKCS11OnlyKeyStorage` (mgmt process with only a SoftHSM token: wrap key created at startup, TSIG envelope wrap byte 2; mgmt without key storage answers 503)
- `mgmt/internal/secrets/signing.go` — `StoredKey`, `GenerateSigningKey`, `Signer`, `DestroySigningKey` (KEK backend: enveloped PKCS#8)
- `mgmt/internal/secrets/signing_test.go`, `mgmt/internal/secrets/pkcs11_test.go`
- `mgmt/internal/tsigkey/service.go`, `mgmt/internal/tsigkey/service_test.go` — TSIG key CRUD through `snapshot.Mutate`, in-use check; `mgmt/internal/zone/service.go` — `checkKeys` locks referenced keys `FOR SHARE` so a concurrent delete cannot miss a new reference (modify)
- `mgmt/internal/api/tsig_keys_test.go`, `mgmt/internal/api/api_test.go` (`Deps.TSIGKeys` default) — API, RBAC, write-only secret, audit redaction, 503
- `mgmt/internal/control/tsigkeys.go`, `mgmt/internal/control/tsigkeys_test.go` — `TSIGKeys` loader building `KeyMaterial` (pattern of `rpztsig.go`)
- `mgmt/internal/control/hub.go`, `mgmt/internal/control/server.go` — `Hub.TSIGKeys`, per-subscriber `keyMaterial` channel with digest, sent before the snapshot on `Connect` and offered on every broadcast (modify)
- `mgmt/api/openapi.yaml`, `mgmt/internal/api/tsig_keys.go`, `mgmt/internal/api/server.go` (`Deps.TSIGKeys`), `mgmt/internal/auth/permissions.go`, `web/src/auth/permissions.ts` — `listTsigKeys`, `createTsigKey`, `deleteTsigKey` (modify/create)
- `mgmt/cmd/nexora-mgmt/main.go` — `secrets.Open` with the PKCS#11 settings, `EnsureHSMWrapKey` under an advisory lock, `hub.TSIGKeys` (modify)
- `engine/src/tsig.rs` — moved from `engine/src/recursor/rpz/tsig.rs` (`git mv`), gains `KeyRing` and `TsigAlg::HmacSha384`
- `engine/src/recursor/rpz/mod.rs` — `pub mod tsig;` becomes `pub use crate::tsig;` (modify)
- `engine/src/lib.rs` — `pub mod tsig;` and `#[cfg(test)] mod tsig_tests;` (modify)
- `engine/src/tsig_tests.rs` — key ring tests (Task 7 appends the TSIG protocol tests)
- `engine/src/authoritative/state.rs` — `AuthState.keyring: Arc<KeyRing>` (modify)
- `engine/src/control.rs` — `ServerMsg::KeyMaterial` arm next to the `RpzTsigKeys` arm (modify)
- `go.mod`, `go.sum` — `github.com/miekg/pkcs11 v1.1.2`

Interfaces:

```go
package secrets
type Backend string
const (BackendKEK Backend = "kek"; BackendPKCS11 Backend = "pkcs11")
// Existing: ErrUnconfigured, ErrKEKMismatch, ErrBackendUnavailable, WrapFileKEK = 1, WrapPKCS11 = 2, Box, LoadKEKFile, (*Box).Configured, Seal, Unseal.
type Config struct{ KEKFile, PKCS11Module, PKCS11TokenLabel, PKCS11PinFile string }
func Open(cfg Config) (*Box, error)       // "" everywhere: an unconfigured Box; partial PKCS#11 settings: error
func (b *Box) Close() error
func (b *Box) Configured() bool            // file KEK or PKCS#11 (nil-safe, as today)
func (b *Box) HasBackend(be Backend) bool
func (b *Box) DefaultBackend() Backend     // pkcs11 when configured, else kek
func (b *Box) EnsureHSMWrapKey(ctx context.Context) error
type StoredKey struct{ Backend Backend; Algorithm uint8; KeyRef []byte; Envelope []byte; PublicKey string }
func (b *Box) GenerateSigningKey(ctx context.Context, be Backend, alg uint8) (StoredKey, error) // alg 13 (P-256) or 8 (RSA-2048)
func (b *Box) Signer(k StoredKey) (crypto.Signer, func(), error)                               // release func zeroes decrypted material
func (b *Box) DestroySigningKey(k StoredKey) error
func (b *Box) PKCS11KeyAttributes(keyRef []byte) (extractable, sensitive bool, err error)
func (b *Box) PKCS11WrapKeyAttributes() (extractable, sensitive bool, err error)
func SigningKeyPurpose(keyRef []byte) string                  // "nexora/dnssec/v1:<hex key_ref>"
func TSIGPurpose(id uuid.UUID, name, algorithm string) string // "nexora/tsig/v1:<id>:<name>:<algorithm>" — binds the envelope to its row
// Open (and LoadKEKFile) refuse a KEK or PIN file with any "other" permission bit, group write, or
// group read unless the file's group is the process's effective group (Kubernetes fsGroup volumes).

package tsigkey
type Key struct{ ID uuid.UUID; Name, Algorithm string; Revision int64; CreatedAt time.Time }
type Created struct{ Key; Secret string } // base64, returned once
var ErrInUse = errors.New("tsig key is in use")
var ErrInvalid = errors.New("invalid tsig key") // wrapped with the reason; API 400 invalid_request
type Service struct{ Store *store.Store; Build snapshot.BuildConfig; Box *secrets.Box }
func (s *Service) Create(ctx context.Context, actor auth.Actor, name, algorithm, secretB64 string) (*Created, error)
func (s *Service) List(ctx context.Context) ([]Key, error)
func (s *Service) Delete(ctx context.Context, actor auth.Actor, id uuid.UUID, revision int64) error
func (s *Service) Secret(ctx context.Context, q snapshot.Querier, id uuid.UUID) (name, algorithm string, secret []byte, err error)

package control
type TSIGKeys struct{ /* st *store.Store; box *secrets.Box */ }
func NewTSIGKeys(st *store.Store, box *secrets.Box) *TSIGKeys
func (k *TSIGKeys) Load(ctx context.Context) (*controlv1.KeyMaterial, string, error) // complete set and digest ("" when empty)
// Hub gains `TSIGKeys *TSIGKeys`; subscriber gains `keyMaterial chan *controlv1.KeyMaterial` (capacity 1) and `keyMaterialDigest string`.
```

```rust
// engine/src/tsig.rs (additions to M3's module)
pub enum TsigAlg { HmacSha256, HmacSha384, HmacSha512 } // name() "hmac-sha384.", ring HMAC_SHA384
impl TsigAlg { pub fn from_proto(a: i32) -> Option<TsigAlg>; }
#[derive(Default)] pub struct KeyRing { /* ArcSwap<FxHashMap<Box<[u8]>, Arc<TsigKey>>> keyed by lowercase wire name */ }
impl KeyRing {
    pub fn apply(&self, km: proto::KeyMaterial); // replaces the whole set; keys with an unknown algorithm or bad name are skipped
    pub fn get(&self, lower_wire_name: &[u8]) -> Option<Arc<TsigKey>>;
    pub fn len(&self) -> usize;
}
```

NXE1 envelope layout (unchanged from M3; M4 adds wrap 2):

```
offset  size  field
0       4     "NXE1"
4       1     wrap: 1 = file KEK, 2 = PKCS#11 AES key "nexora-kek"
5       8     kek_id: SHA-256(KEK)[0:8] (file) / SHA-256("pkcs11:" || token label || ":nexora-kek-v1")[0:8]
13      12    dek_nonce
25      48    wrapped_dek = AES-256-GCM(KEK, dek_nonce, DEK[32], aad = "NXE1-dek")
73      12    data_nonce
85      n+16  ciphertext = AES-256-GCM(DEK, data_nonce, plaintext, aad = purpose)
purposes: "nexora/rpz-tsig/v1:<zone uuid>" (M3), "nexora/tsig/v1:<key uuid>:<key name>:<algorithm>", "nexora/dnssec/v1:<hex key_ref>"
```

- [x] Write the failing `mgmt/internal/secrets/signing_test.go` (package `secrets_test`; `writeKEK(t, n)` is M3's helper in `secrets_test.go`):

```go
package secrets_test

import (
	"crypto/x509"
	"errors"
	"strings"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/secrets"
)

func TestOpenValidatesBackends(t *testing.T) {
	box, err := secrets.Open(secrets.Config{})
	if err != nil || box.Configured() {
		t.Fatalf("empty config: configured=%v err=%v", box.Configured(), err)
	}
	if _, err := box.GenerateSigningKey(t.Context(), secrets.BackendKEK, 13); !errors.Is(err, secrets.ErrUnconfigured) {
		t.Fatalf("GenerateSigningKey without key storage: %v", err)
	}
	if _, err := secrets.Open(secrets.Config{PKCS11Module: "/usr/lib/softhsm/libsofthsm2.so"}); err == nil || !strings.Contains(err.Error(), "must be set together") {
		t.Fatalf("partial PKCS#11 configuration: %v", err)
	}
	kek, err := secrets.Open(secrets.Config{KEKFile: writeKEK(t, 32)})
	if err != nil || !kek.HasBackend(secrets.BackendKEK) || kek.HasBackend(secrets.BackendPKCS11) || kek.DefaultBackend() != secrets.BackendKEK {
		t.Fatalf("file KEK backends: %v", err)
	}
	if _, err := kek.GenerateSigningKey(t.Context(), secrets.BackendPKCS11, 13); !errors.Is(err, secrets.ErrBackendUnavailable) {
		t.Fatalf("PKCS#11 key without a token: %v", err)
	}
}

func TestKEKSigningKeyIsEnvelopedPKCS8(t *testing.T) {
	box, err := secrets.Open(secrets.Config{KEKFile: writeKEK(t, 32)})
	if err != nil {
		t.Fatal(err)
	}
	for _, alg := range []uint8{13, 8} {
		k, err := box.GenerateSigningKey(t.Context(), secrets.BackendKEK, alg)
		if err != nil {
			t.Fatal(err)
		}
		if len(k.KeyRef) != 16 || k.Envelope == nil || k.PublicKey == "" {
			t.Fatalf("alg %d: %+v", alg, k)
		}
		der, err := box.Unseal(secrets.SigningKeyPurpose(k.KeyRef), k.Envelope)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := x509.ParsePKCS8PrivateKey(der); err != nil {
			t.Fatalf("alg %d: envelope does not hold PKCS#8: %v", alg, err)
		}
		verifySignerMatchesDNSKEY(t, box, k)
	}
}
```

`verifySignerMatchesDNSKEY` lives in `pkcs11_test.go` (below).

- [x] Write the failing `mgmt/internal/secrets/pkcs11_test.go` (token PINs are derived at run time):

```go
package secrets_test

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
)

func softhsmConfig(t *testing.T) secrets.Config {
	t.Helper()
	dir := t.TempDir()
	tokens := filepath.Join(dir, "tokens")
	if err := os.MkdirAll(tokens, 0o700); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(dir, "softhsm2.conf")
	if err := os.WriteFile(conf, []byte("directories.tokendir = "+tokens+"\nobjectstore.backend = file\nlog.level = ERROR\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOFTHSM2_CONF", conf)
	userPIN, soPIN := strings.Repeat("7", 6), strings.Repeat("8", 6)
	if out, err := exec.Command("softhsm2-util", "--init-token", "--free", "--label", "nexora-test", "--pin", userPIN, "--so-pin", soPIN).CombinedOutput(); err != nil {
		t.Fatalf("softhsm2-util: %v\n%s", err, out)
	}
	pin := filepath.Join(dir, "pin")
	if err := os.WriteFile(pin, []byte(userPIN+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return secrets.Config{PKCS11Module: "/usr/lib/softhsm/libsofthsm2.so", PKCS11TokenLabel: "nexora-test", PKCS11PinFile: pin}
}

func verifySignerMatchesDNSKEY(t *testing.T, box *secrets.Box, k secrets.StoredKey) {
	t.Helper()
	signer, release, err := box.Signer(k)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	key := &dns.DNSKEY{Hdr: dns.RR_Header{Name: "keys.test.", Rrtype: dns.TypeDNSKEY, Class: dns.ClassINET, Ttl: 300},
		Flags: 257, Protocol: 3, Algorithm: k.Algorithm, PublicKey: k.PublicKey}
	a := &dns.A{Hdr: dns.RR_Header{Name: "www.keys.test.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300}, A: net.ParseIP("192.0.2.1")}
	sig := &dns.RRSIG{KeyTag: key.KeyTag(), SignerName: "keys.test.", Algorithm: k.Algorithm,
		Inception: uint32(time.Now().Add(-time.Hour).Unix()), Expiration: uint32(time.Now().Add(time.Hour).Unix())}
	if err := sig.Sign(signer, []dns.RR{a}); err != nil {
		t.Fatalf("sign alg %d: %v", k.Algorithm, err)
	}
	if err := sig.Verify(key, []dns.RR{a}); err != nil {
		t.Fatalf("verify alg %d: %v", k.Algorithm, err)
	}
}

func TestPKCS11SigningKeysStayInToken(t *testing.T) {
	box, err := secrets.Open(softhsmConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	for _, alg := range []uint8{13, 8} {
		k, err := box.GenerateSigningKey(t.Context(), secrets.BackendPKCS11, alg)
		if err != nil {
			t.Fatal(err)
		}
		if k.Envelope != nil {
			t.Fatalf("alg %d: PKCS#11 key has an envelope", alg)
		}
		verifySignerMatchesDNSKEY(t, box, k)
		extractable, sensitive, err := box.PKCS11KeyAttributes(k.KeyRef)
		if err != nil || extractable || !sensitive {
			t.Fatalf("alg %d: extractable=%v sensitive=%v err=%v", alg, extractable, sensitive, err)
		}
		if err := box.DestroySigningKey(k); err != nil {
			t.Fatal(err)
		}
		if _, _, err := box.PKCS11KeyAttributes(k.KeyRef); err == nil {
			t.Fatalf("alg %d: key still present after destroy", alg)
		}
	}
}

func TestPKCS11WrapsEnvelopesWhenNoKEKFile(t *testing.T) {
	box, err := secrets.Open(softhsmConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	if !box.Configured() || box.DefaultBackend() != secrets.BackendPKCS11 {
		t.Fatal("a PKCS#11-only box must be configured with the pkcs11 default backend")
	}
	if err := box.EnsureHSMWrapKey(t.Context()); err != nil {
		t.Fatal(err)
	}
	env, err := box.Seal(secrets.TSIGPurpose([16]byte{1}, "x.", "hmac-sha256"), []byte("hsm-wrapped"))
	if err != nil || env[4] != secrets.WrapPKCS11 {
		t.Fatalf("seal: %v wrap=%d", err, env[4])
	}
	got, err := box.Unseal(secrets.TSIGPurpose([16]byte{1}, "x.", "hmac-sha256"), env)
	if err != nil || string(got) != "hsm-wrapped" {
		t.Fatalf("unseal: %q %v", got, err)
	}
}
```

- [x] Run `scripts/dev-exec.sh go test ./mgmt/internal/secrets/ -count=1` — expect FAIL with "undefined: secrets.Open".
- [x] Refactor `secrets.go` onto one sealing path used by both wrappers (M3's layout and tests stay green):

```go
func sealWith(wrap byte, kekID []byte, wrapDEK func(nonce, dek []byte) ([]byte, error), purpose string, plaintext []byte) ([]byte, error) {
	dek := make([]byte, 32)
	defer clear(dek)
	dekNonce, dataNonce := make([]byte, 12), make([]byte, 12)
	for _, b := range [][]byte{dek, dekNonce, dataNonce} {
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
	}
	wrapped, err := wrapDEK(dekNonce, dek)
	if err != nil {
		return nil, err
	}
	if len(wrapped) != 48 {
		return nil, fmt.Errorf("wrapped DEK is %d bytes, want 48", len(wrapped))
	}
	block, _ := aes.NewCipher(dek)
	gcm, _ := cipher.NewGCM(block)
	out := make([]byte, 0, 85+len(plaintext)+16)
	out = append(out, "NXE1"...)
	out = append(out, wrap)
	out = append(out, kekID[:8]...)
	out = append(out, dekNonce...)
	out = append(out, wrapped...)
	out = append(out, dataNonce...)
	return gcm.Seal(out, dataNonce, plaintext, []byte(purpose)), nil
}
```

`Unseal` checks magic and length ≥ 101, selects the wrapper by byte 4 (`ErrBackendUnavailable` when that backend is not configured), compares `kek_id` (`ErrKEKMismatch`), unwraps the DEK with aad `NXE1-dek`, opens the ciphertext with aad = purpose, and `clear`s the DEK. `Seal` uses the file KEK when configured, else the HSM wrap key, else `ErrUnconfigured`. `Open` loads the KEK file exactly as `LoadKEKFile` does today, requires the three PKCS#11 settings together (`NEXORA_PKCS11_MODULE, NEXORA_PKCS11_TOKEN_LABEL and NEXORA_PKCS11_PIN_FILE must be set together`) and opens the token; `LoadKEKFile(path)` returns `Open(Config{KEKFile: path})`.

- [x] Implement `pkcs11.go`:

```go
type HSM struct {
	ctx   *pkcs11.Ctx
	slot  uint
	label string
	pool  chan pkcs11.SessionHandle
}

func openHSM(module, label, pinFile string) (*HSM, error) {
	pinRaw, err := os.ReadFile(pinFile)
	if err != nil {
		return nil, fmt.Errorf("NEXORA_PKCS11_PIN_FILE: %w", err)
	}
	p := pkcs11.New(module)
	if p == nil {
		return nil, fmt.Errorf("NEXORA_PKCS11_MODULE %q could not be loaded", module)
	}
	if err := p.Initialize(); err != nil {
		return nil, fmt.Errorf("pkcs11 initialize: %w", err)
	}
	slots, err := p.GetSlotList(true)
	if err != nil {
		return nil, err
	}
	h := &HSM{ctx: p, label: label, pool: make(chan pkcs11.SessionHandle, 4)}
	found := false
	for _, s := range slots {
		if ti, err := p.GetTokenInfo(s); err == nil && strings.TrimRight(ti.Label, " \x00") == label {
			h.slot, found = s, true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("NEXORA_PKCS11_TOKEN_LABEL %q: no such token", label)
	}
	for i := 0; i < cap(h.pool); i++ {
		sh, err := p.OpenSession(h.slot, pkcs11.CKF_SERIAL_SESSION|pkcs11.CKF_RW_SESSION)
		if err != nil {
			return nil, err
		}
		if i == 0 {
			if err := p.Login(sh, pkcs11.CKU_USER, strings.TrimSpace(string(pinRaw))); err != nil && err != pkcs11.Error(pkcs11.CKR_USER_ALREADY_LOGGED_IN) {
				return nil, fmt.Errorf("pkcs11 login: %w", err)
			}
		}
		h.pool <- sh
	}
	return h, nil
}

func (h *HSM) with(fn func(sh pkcs11.SessionHandle) error) error {
	sh := <-h.pool
	defer func() { h.pool <- sh }()
	return fn(sh)
}

var oidP256 = []byte{0x06, 0x08, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07}

func (h *HSM) generate(alg uint8, id []byte) (publicKey string, err error) {
	err = h.with(func(sh pkcs11.SessionHandle) error {
		common := []*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_TOKEN, true), pkcs11.NewAttribute(pkcs11.CKA_ID, id), pkcs11.NewAttribute(pkcs11.CKA_LABEL, "nexora-dnssec")}
		priv := append([]*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_PRIVATE, true), pkcs11.NewAttribute(pkcs11.CKA_SIGN, true),
			pkcs11.NewAttribute(pkcs11.CKA_SENSITIVE, true), pkcs11.NewAttribute(pkcs11.CKA_EXTRACTABLE, false)}, common...)
		switch alg {
		case dns.ECDSAP256SHA256:
			pub := append([]*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_KEY_TYPE, pkcs11.CKK_EC), pkcs11.NewAttribute(pkcs11.CKA_EC_PARAMS, oidP256), pkcs11.NewAttribute(pkcs11.CKA_VERIFY, true)}, common...)
			pubH, _, err := h.ctx.GenerateKeyPair(sh, []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_EC_KEY_PAIR_GEN, nil)}, pub, priv)
			if err != nil {
				return err
			}
			attrs, err := h.ctx.GetAttributeValue(sh, pubH, []*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_EC_POINT, nil)})
			if err != nil {
				return err
			}
			point := attrs[0].Value
			var inner []byte
			if _, err := asn1.Unmarshal(point, &inner); err == nil {
				point = inner
			}
			if len(point) != 65 || point[0] != 4 {
				return fmt.Errorf("pkcs11: unexpected EC point length %d", len(point))
			}
			publicKey = base64.StdEncoding.EncodeToString(point[1:])
		case dns.RSASHA256:
			pub := append([]*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_KEY_TYPE, pkcs11.CKK_RSA), pkcs11.NewAttribute(pkcs11.CKA_MODULUS_BITS, 2048),
				pkcs11.NewAttribute(pkcs11.CKA_PUBLIC_EXPONENT, []byte{1, 0, 1}), pkcs11.NewAttribute(pkcs11.CKA_VERIFY, true)}, common...)
			pubH, _, err := h.ctx.GenerateKeyPair(sh, []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_RSA_PKCS_KEY_PAIR_GEN, nil)}, pub, priv)
			if err != nil {
				return err
			}
			attrs, err := h.ctx.GetAttributeValue(sh, pubH, []*pkcs11.Attribute{pkcs11.NewAttribute(pkcs11.CKA_PUBLIC_EXPONENT, nil), pkcs11.NewAttribute(pkcs11.CKA_MODULUS, nil)})
			if err != nil {
				return err
			}
			publicKey = rsaDNSKEYPublic(attrs[0].Value, attrs[1].Value)
		default:
			return fmt.Errorf("unsupported DNSSEC algorithm %d", alg)
		}
		return nil
	})
	return publicKey, err
}

// RFC 3110 §2: exponent length octet, exponent, modulus.
func rsaDNSKEYPublic(e, n []byte) string {
	e, n = bytes.TrimLeft(e, "\x00"), bytes.TrimLeft(n, "\x00")
	buf := append([]byte{byte(len(e))}, e...)
	return base64.StdEncoding.EncodeToString(append(buf, n...))
}

var sha256DigestInfo = []byte{0x30, 0x31, 0x30, 0x0d, 0x06, 0x09, 0x60, 0x86, 0x48, 0x01, 0x65, 0x03, 0x04, 0x02, 0x01, 0x05, 0x00, 0x04, 0x20}

type hsmSigner struct {
	h   *HSM
	id  []byte
	alg uint8
	pub crypto.PublicKey
}

func (s *hsmSigner) Public() crypto.PublicKey { return s.pub }

func (s *hsmSigner) Sign(_ io.Reader, digest []byte, _ crypto.SignerOpts) (sig []byte, err error) {
	err = s.h.with(func(sh pkcs11.SessionHandle) error {
		priv, err := s.h.findOne(sh, pkcs11.CKO_PRIVATE_KEY, s.id)
		if err != nil {
			return err
		}
		switch s.alg {
		case dns.ECDSAP256SHA256:
			if err := s.h.ctx.SignInit(sh, []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_ECDSA, nil)}, priv); err != nil {
				return err
			}
			raw, err := s.h.ctx.Sign(sh, digest)
			if err != nil {
				return err
			}
			if len(raw) != 64 {
				return fmt.Errorf("pkcs11: ECDSA signature is %d bytes", len(raw))
			}
			sig, err = asn1.Marshal(struct{ R, S *big.Int }{new(big.Int).SetBytes(raw[:32]), new(big.Int).SetBytes(raw[32:])})
			return err
		case dns.RSASHA256:
			if err := s.h.ctx.SignInit(sh, []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_RSA_PKCS, nil)}, priv); err != nil {
				return err
			}
			sig, err = s.h.ctx.Sign(sh, append(append([]byte(nil), sha256DigestInfo...), digest...))
			return err
		}
		return fmt.Errorf("unsupported algorithm %d", s.alg)
	})
	return sig, err
}
```

`findOne` runs `FindObjectsInit` on `{CKA_CLASS, CKA_ID}`, `FindObjects(sh, 2)`, `FindObjectsFinal`, and requires exactly one handle. `PKCS11KeyAttributes` reads `CKA_EXTRACTABLE` and `CKA_SENSITIVE` of the private key. `DestroySigningKey` destroys private and public objects with that `CKA_ID`. The HSM wrap key: `EnsureHSMWrapKey` finds `CKO_SECRET_KEY` with `CKA_ID "nexora-kek-v1"` or creates it with `CKM_AES_KEY_GEN` and attributes `CKA_CLASS=CKO_SECRET_KEY, CKA_KEY_TYPE=CKK_AES, CKA_VALUE_LEN=32, CKA_TOKEN=true, CKA_PRIVATE=true, CKA_SENSITIVE=true, CKA_EXTRACTABLE=false, CKA_ENCRYPT=true, CKA_DECRYPT=true, CKA_LABEL="nexora-kek"`; wrapping uses `params := pkcs11.NewGCMParams(dekNonce, []byte("NXE1-dek"), 128)`, `EncryptInit(sh, []*pkcs11.Mechanism{pkcs11.NewMechanism(pkcs11.CKM_AES_GCM, params)}, handle)`, `Encrypt(sh, dek)`, `params.Free()` (unwrap: `DecryptInit`/`Decrypt`). `main.go` calls `EnsureHSMWrapKey` while holding `pg_advisory_lock(hashtext('pkcs11_wrap_key'))` so instances never create two.

- [x] Implement `signing.go`: `GenerateSigningKey` returns `ErrUnconfigured` for an unconfigured box and `ErrBackendUnavailable` for a backend that is not configured; alg 13 → `ecdsa.GenerateKey(elliptic.P256(), rand.Reader)`, public = `priv.PublicKey.ECDH()` bytes without the leading 0x04; alg 8 → `rsa.GenerateKey(rand.Reader, 2048)`, public = `rsaDNSKEYPublic(big.NewInt(int64(E)).Bytes(), N.Bytes())`; private = `x509.MarshalPKCS8PrivateKey` sealed with `SigningKeyPurpose(key_ref)`, `key_ref` = 16 random bytes; the PKCS#11 backend calls `HSM.generate` with `key_ref` as `CKA_ID`. `Signer` unseals, parses PKCS#8, returns the key and a release func that `clear`s the DER buffer (PKCS#11: an `hsmSigner`, no-op release).
- [x] Add the settings to `mgmt/internal/config` (`NEXORA_PKCS11_MODULE`, `NEXORA_PKCS11_TOKEN_LABEL`, `NEXORA_PKCS11_PIN_FILE`; all or none, same message as `secrets.Open`, with a `config_test.go` case). In `main.go` replace `secrets.LoadKEKFile(cfg.KEKFile)` with `secrets.Open(secrets.Config{KEKFile: cfg.KEKFile, PKCS11Module: cfg.PKCS11Module, PKCS11TokenLabel: cfg.PKCS11TokenLabel, PKCS11PinFile: cfg.PKCS11PinFile})` (exit non-zero on error, `defer box.Close()`), and change the unconfigured log line to `key storage: none configured; RPZ TSIG secrets, TSIG keys and DNSSEC signing are refused`.
- [x] Run `scripts/dev-exec.sh go test ./mgmt/internal/secrets/ ./mgmt/internal/config/ -count=1` — expect PASS (M3's secrets tests included).
- [x] Write the failing `mgmt/internal/tsigkey/service_test.go` and `mgmt/internal/control/tsigkeys_test.go`:

```go
package tsigkey_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/tsigkey"
)

var admin = auth.Actor{Type: "user", ID: "admin", Name: "admin"}

func kekBox(t *testing.T) *secrets.Box {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.Open(secrets.Config{KEKFile: p})
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestCreateStoresOnlyEnvelopeAndPublishes(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	s := &tsigkey.Service{Store: st, Box: kekBox(t)}
	var before int
	st.Pool.QueryRow(ctx, `SELECT count(*) FROM config_versions`).Scan(&before)
	c, err := s.Create(ctx, admin, "xfr-key.", "hmac-sha256", "")
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := base64.StdEncoding.DecodeString(c.Secret)
	if len(secret) != 32 {
		t.Fatalf("generated secret length %d", len(secret))
	}
	var dump string
	st.Pool.QueryRow(ctx, `SELECT string_agg(t::text, E'\n') FROM tsig_keys t`).Scan(&dump)
	if bytes.Contains([]byte(dump), []byte(c.Secret)) || bytes.Contains([]byte(dump), []byte(hex.EncodeToString(secret))) {
		t.Fatal("plaintext TSIG secret stored in tsig_keys")
	}
	var after int
	st.Pool.QueryRow(ctx, `SELECT count(*) FROM config_versions`).Scan(&after)
	if after != before+1 {
		t.Fatalf("creating a key must publish a config version so engines get the new KeyMaterial: %d -> %d", before, after)
	}
	provided := base64.StdEncoding.EncodeToString([]byte("fixture-tsig-key-provided"))
	if p, err := s.Create(ctx, admin, "given-key.", "hmac-sha384", provided); err != nil || p.Secret != provided {
		t.Fatalf("provided secret: %+v %v", p, err)
	}
	if _, err := s.Create(ctx, admin, "Bad Name", "hmac-sha256", ""); err == nil {
		t.Fatal("invalid key name accepted")
	}
	if _, err := s.Create(ctx, admin, "md5-key.", "hmac-md5", ""); err == nil {
		t.Fatal("hmac-md5 accepted")
	}
}

func TestUnconfiguredKeyStorageRefuses(t *testing.T) {
	box, _ := secrets.Open(secrets.Config{})
	s := &tsigkey.Service{Store: storetest.New(t), Box: box}
	if _, err := s.Create(context.Background(), admin, "k.", "hmac-sha256", ""); !errors.Is(err, secrets.ErrUnconfigured) {
		t.Fatalf("got %v, want ErrUnconfigured", err)
	}
}

func TestDeleteInUseKeyFails(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	s := &tsigkey.Service{Store: st, Box: kekBox(t)}
	c, err := s.Create(ctx, admin, "used.", "hmac-sha256", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `INSERT INTO zones (name, kind, soa_mname, soa_rname, transfer_tsig_key_id) VALUES ('u.test.', 'primary', 'ns.u.test.', 'h.u.test.', $1)`, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, admin, c.ID, c.Revision); !errors.Is(err, tsigkey.ErrInUse) {
		t.Fatalf("got %v, want ErrInUse", err)
	}
}
```

```go
package control_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/tsigkey"
)

func TestTSIGKeysLoadUnsealsAndDigestTracksChanges(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	box := kekBox(t) // M3 helper in rpztsig_test.go
	admin := auth.Actor{Type: "user", ID: "admin", Name: "admin"}
	loader := control.NewTSIGKeys(st, box)
	if km, digest, err := loader.Load(ctx); err != nil || len(km.TsigKeys) != 0 || digest != "" {
		t.Fatalf("empty set: %v %q %v", km, digest, err)
	}
	s := &tsigkey.Service{Store: st, Box: box}
	secret := base64.StdEncoding.EncodeToString([]byte("fixture-tsig-key-xfr"))
	if _, err := s.Create(ctx, admin, "xfr-key.", "hmac-sha512", secret); err != nil {
		t.Fatal(err)
	}
	km, first, err := loader.Load(ctx)
	if err != nil || len(km.TsigKeys) != 1 || first == "" {
		t.Fatalf("one key: %v %q %v", km, first, err)
	}
	k := km.TsigKeys[0]
	if k.Name != "xfr-key." || k.Algorithm != controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA512 || !bytes.Equal(k.Secret, []byte("fixture-tsig-key-xfr")) {
		t.Fatalf("key material: name=%q alg=%v", k.Name, k.Algorithm)
	}
	if _, err := s.Create(ctx, admin, "second.", "hmac-sha256", ""); err != nil {
		t.Fatal(err)
	}
	if _, second, _ := loader.Load(ctx); second == first {
		t.Fatal("digest did not change with the key set")
	}
}
```

- [x] Run `scripts/dev-exec.sh go test ./mgmt/internal/tsigkey/ ./mgmt/internal/control/ -run 'TestCreateStores|TestUnconfiguredKey|TestDeleteInUse|TestTSIGKeysLoad' -count=1` — expect FAIL with "undefined: tsigkey.Service".
- [x] Implement `tsigkey.Service`: names lowercased, absolute, `^([a-z0-9_-]{1,63}\.)+$`; algorithms hmac-sha256/384/512; empty secret → random bytes of the HMAC output size (32/48/64); a provided secret must decode from base64 to 16..64 bytes; seal with `Box.Seal(secrets.TSIGPurpose(id, name, algorithm), secret)` (id generated in Go and inserted explicitly) before the transaction (an unconfigured box refuses without writing, as M3's `rpz.go` does); `Create`/`Delete` run in `snapshot.Mutate(ctx, s.Store, s.Build, actor, …)` with `auth.Change{Action: "createTsigKey"|"deleteTsigKey", TargetType: "tsig_key", TargetID: id, After: Key (never the secret)}`, so the new config version's `pg_notify` makes every hub re-offer `KeyMaterial`. `Delete` refuses when referenced by `zones.transfer_tsig_key_id`, `update_tsig_key_ids`, or any `notify_targets` / `primaries` element's `tsig_key_id` (`ErrInUse` → 409 `tsig_key_in_use`); stale revision → `store.ErrConflict`. `Secret` unseals one key for the management plane's own TSIG use (Tasks 10, 11).
- [x] Implement `control/tsigkeys.go` like `rpztsig.go`: select every `tsig_keys` row ordered by name, unseal, map the algorithm to `controlv1.TsigAlgorithm`, digest = SHA-256 of the deterministic marshalling (cleared after hashing). In `hub.go` add `TSIGKeys *TSIGKeys`, `loadKeyMaterial` (logs errors, never keys) and `subscriber.offerKeyMaterial(km, digest)` (same replace-pending logic as `offerKeys`); `broadcast` offers it after the RPZ keys. In `server.go` `Connect` offers it before the snapshot (next to the RPZ keys) and the send loop drains `sub.keyMaterial` with the same priority as `sub.keys`, sending `ServerMessage{Msg: &controlv1.ServerMessage_KeyMaterial{KeyMaterial: km}}`.
- [x] Add OpenAPI: `GET /tsig-keys` `listTsigKeys` → `[TsigKey{id,name,algorithm,revision,created_at}]`; `POST /tsig-keys` `createTsigKey` body `{name, algorithm, secret?}` → 201 `TsigKeyCreated` (TsigKey + `secret`), 400, 503; `DELETE /tsig-keys/{keyId}?revision=` `deleteTsigKey` → 204, 409 (`conflict` or `tsig_key_in_use`); all errors `#/components/responses/Error`. `mapError` gains `errors.Is(err, tsigkey.ErrInUse)` → 409 `tsig_key_in_use`; `secrets.ErrUnconfigured` already maps to 503 `key_storage_unconfigured`, and `secrets.ErrBackendUnavailable` maps to 503 `key_backend_unavailable`. Permissions: `listTsigKeys` viewer, `createTsigKey` and `deleteTsigKey` admin (Go and `permissions.ts`). `api.Deps` gains `TSIGKeys *tsigkey.Service`; `main.go` passes `&tsigkey.Service{Store: st, Build: build, Box: box}` and sets `hub.TSIGKeys = control.NewTSIGKeys(st, box)`. Regenerate the API.
- [x] Run `scripts/dev-exec.sh go test ./mgmt/internal/tsigkey/ ./mgmt/internal/control/ ./mgmt/internal/api/ -count=1` — expect PASS.
- [ ] Move the engine TSIG module: `git mv engine/src/recursor/rpz/tsig.rs engine/src/tsig.rs`, add `pub mod tsig;` to `engine/src/lib.rs`, and replace `pub mod tsig;` in `engine/src/recursor/rpz/mod.rs` with `pub use crate::tsig;` (M3's `use super::tsig::…` in `manager.rs`, `transfer.rs` and `transfer_tests.rs` keep compiling). Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::rpz` — expect PASS (pure move).
- [ ] Write the failing `engine/src/tsig_tests.rs` (declare `#[cfg(test)] mod tsig_tests;` in `lib.rs`):

```rust
use crate::proto;
use crate::tsig::{KeyRing, TsigAlg};

fn km(algorithm: proto::TsigAlgorithm) -> proto::KeyMaterial {
    proto::KeyMaterial {
        tsig_keys: vec![proto::TsigSecret { name: "xfr-key.".into(), algorithm: algorithm as i32, secret: vec![7u8; 32] }],
    }
}

#[test]
fn key_ring_replaces_the_set_skips_unknown_algorithms_and_redacts_debug() {
    let ring = KeyRing::default();
    ring.apply(km(proto::TsigAlgorithm::HmacSha256));
    let k = ring.get(b"\x07xfr-key\x00").expect("key by lowercase wire name");
    assert_eq!(k.alg, TsigAlg::HmacSha256);
    assert_eq!(&k.secret[..], &[7u8; 32][..]);
    assert!(!format!("{k:?}").contains("7, 7"), "secret bytes must not appear in Debug output");
    ring.apply(km(proto::TsigAlgorithm::HmacSha384));
    assert_eq!(ring.get(b"\x07xfr-key\x00").unwrap().alg, TsigAlg::HmacSha384);
    ring.apply(km(proto::TsigAlgorithm::None));
    assert!(ring.get(b"\x07xfr-key\x00").is_none(), "a key without a supported algorithm is skipped");
    ring.apply(km(proto::TsigAlgorithm::HmacSha512));
    assert_eq!(ring.len(), 1);
    ring.apply(proto::KeyMaterial { tsig_keys: vec![] });
    assert!(ring.get(b"\x07xfr-key\x00").is_none(), "removed keys disappear");
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib tsig_tests` — expect FAIL with "unresolved import `crate::tsig::KeyRing`".
- [ ] Implement in `tsig.rs`: `TsigAlg::HmacSha384` (`name()` = `"hmac-sha384."`, `ring()` = `hmac::HMAC_SHA384`), `TsigAlg::from_proto`, and `KeyRing` (`ArcSwap<FxHashMap<Box<[u8]>, Arc<TsigKey>>>`; `apply` builds a new map from `Name::from_ascii(name)`, keyed by `name.to_lowercase().to_bytes()`, moving each secret into `Zeroizing<Vec<u8>>`; M3's manual `Debug` for `TsigKey` already prints `<redacted>`). `AuthState` gains `pub keyring: Arc<KeyRing>`; `control::session` handles `Some(ServerMsg::KeyMaterial(km)) => shared.auth.keyring.apply(km)` next to the `RpzTsigKeys` arm (never logged). The key ring is never written to `state_dir`; `snapshot.binpb` holds key names only.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib tsig_tests` and `scripts/dev-exec.sh make engine-test` — expect PASS.
- [ ] Commit: `git add mgmt engine go.mod go.sum web/src/api/schema.d.ts web/src/auth/permissions.ts && git commit -m "feat(m4): PKCS#11 key storage and signing keys in secrets, TSIG keys, in-memory KeyMaterial on engines"`.

## Task 7: Engine TSIG (RFC 8945) and signed queries to hosted zones

Files:

- `mgmt/internal/nzf/tsigvectors_test.go` — writes miekg-signed TSIG vectors (`-update`)
- `testdata/tsig/query-hmac-sha256.bin`, `response-hmac-sha256.bin`, `query-hmac-sha512.bin` — vectors
- `engine/src/tsig.rs` — server side added to M3's module: find, verify requests, stream signing, error responses (modify)
- `engine/src/tsig_tests.rs` — appended protocol tests (modify)
- `engine/src/authoritative/dispatch.rs` — `unparsed` answers TSIG-signed queries to hosted zones (modify)

No crate is added: M3's module already computes HMAC with `ring::hmac` and builds the RFC 8945 MAC inputs (`variables`, `response_input`, `append_tsig`); `sign_request`, `sign_response(wire, key, time_signed, prior_mac, first, unsigned_before)` and `TsigVerifier` stay as they are.

Interfaces:

```rust
// engine/src/tsig.rs (additions; existing: TsigAlg, TsigKey, TsigError, FUDGE, skip_name, extract_mac, sign_request, sign_response, TsigVerifier, KeyRing)
pub struct TsigRecord { pub start: usize, pub key_name: Name, pub alg_name: Name, pub time_signed: u64, pub fudge: u16, pub mac: Vec<u8>, pub original_id: u16, pub error: u16, pub other: Vec<u8> }
pub enum TsigFailure { FormErr, BadKey, BadSig, BadTime { key: Arc<TsigKey>, request_mac: Vec<u8>, time_signed: u64 } }
pub enum Verified { Unsigned, Signed { key: Arc<TsigKey>, request_mac: Vec<u8> } }
pub fn find_tsig(msg: &[u8]) -> Result<Option<TsigRecord>, TsigError>;      // TsigError::Malformed: TSIG not last, twice, or class != ANY
pub fn verify_request(msg: &[u8], ring: &KeyRing, now: u64) -> Result<Verified, TsigFailure>;
pub fn sign_response_error(wire: &mut Vec<u8>, key: &TsigKey, time_signed: u64, request_mac: &[u8], error: u16, other: &[u8]) -> Vec<u8>;
pub struct StreamSigner { /* key: Arc<TsigKey>, prev_mac: Vec<u8>, first: bool */ }
impl StreamSigner { pub fn new(key: Arc<TsigKey>, request_mac: Vec<u8>) -> Self; pub fn sign(&mut self, msg: &mut Vec<u8>, now: u64); }
pub fn error_response(request: &[u8], failure: &TsigFailure, now: u64) -> Vec<u8>; // NOTAUTH + TSIG error (16 BADSIG, 17 BADKEY, 18 BADTIME)
```

- [ ] Write the vector generator `mgmt/internal/nzf/tsigvectors_test.go` (lives beside the other goldens; it writes only with `-update` and otherwise checks the files exist):

```go
package nzf

import (
	"encoding/base64"
	"os"
	"testing"

	"github.com/miekg/dns"
)

const tsigTime = 1757750400

func tsigSecret(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestTSIGVectors(t *testing.T) {
	q := new(dns.Msg)
	q.Id = 0x1234
	q.SetQuestion("xfr.test.", dns.TypeSOA)
	q.SetTsig("xfr-key.", dns.HmacSHA256, 300, tsigTime)
	qwire, qmac, err := dns.TsigGenerate(q, tsigSecret(32), "", false)
	if err != nil {
		t.Fatal(err)
	}
	r := new(dns.Msg)
	r.SetReply(q)
	r.Authoritative = true
	soa, _ := dns.NewRR("xfr.test. 300 IN SOA ns1.xfr.test. hostmaster.xfr.test. 7 7200 3600 1209600 300")
	r.Answer = []dns.RR{soa}
	r.Extra = nil
	r.SetTsig("xfr-key.", dns.HmacSHA256, 300, tsigTime)
	rwire, _, err := dns.TsigGenerate(r, tsigSecret(32), qmac, false)
	if err != nil {
		t.Fatal(err)
	}
	q5 := new(dns.Msg)
	q5.Id = 0x4321
	q5.SetQuestion("xfr.test.", dns.TypeAXFR)
	q5.SetTsig("sha512-key.", dns.HmacSHA512, 300, tsigTime)
	q5wire, _, err := dns.TsigGenerate(q5, tsigSecret(64), "", false)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"query-hmac-sha256.bin": qwire, "response-hmac-sha256.bin": rwire, "query-hmac-sha512.bin": q5wire}
	for name, data := range files {
		path := "../../../testdata/tsig/" + name
		if *update {
			os.MkdirAll("../../../testdata/tsig", 0o755)
			if err := os.WriteFile(path, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s missing (run with -update once): %v", name, err)
		}
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/nzf/ -run TestTSIGVectors -count=1 -update` — expect PASS and three files under `testdata/tsig/`.
- [ ] Append the failing protocol tests to `engine/src/tsig_tests.rs` (merge the `use` lines with the key ring test's):

```rust
use crate::tsig::{find_tsig, sign_response, verify_request, StreamSigner, TsigFailure, Verified};
use hickory_proto::rr::Name;

const Q: &[u8] = include_bytes!(concat!(env!("CARGO_MANIFEST_DIR"), "/../testdata/tsig/query-hmac-sha256.bin"));
const R: &[u8] = include_bytes!(concat!(env!("CARGO_MANIFEST_DIR"), "/../testdata/tsig/response-hmac-sha256.bin"));
const Q512: &[u8] = include_bytes!(concat!(env!("CARGO_MANIFEST_DIR"), "/../testdata/tsig/query-hmac-sha512.bin"));
const T0: u64 = 1757750400;

pub(crate) fn test_ring() -> KeyRing {
    let ring = KeyRing::default();
    ring.apply(proto::KeyMaterial {
        tsig_keys: vec![
            proto::TsigSecret { name: "xfr-key.".into(), algorithm: proto::TsigAlgorithm::HmacSha256 as i32, secret: (0u8..32).collect() },
            proto::TsigSecret { name: "sha512-key.".into(), algorithm: proto::TsigAlgorithm::HmacSha512 as i32, secret: (0u8..64).collect() },
        ],
    });
    ring
}

#[test]
fn verifies_miekg_signed_queries() {
    for (msg, name) in [(Q, "xfr-key."), (Q512, "sha512-key.")] {
        match verify_request(msg, &test_ring(), T0 + 10) {
            Ok(Verified::Signed { key, .. }) => assert_eq!(key.name, Name::from_ascii(name).unwrap()),
            Ok(Verified::Unsigned) => panic!("unsigned"),
            Err(_) => panic!("verification failed"),
        }
    }
}

#[test]
fn time_outside_fudge_is_badtime() {
    assert!(matches!(verify_request(Q, &test_ring(), T0 + 301), Err(TsigFailure::BadTime { .. })));
    assert!(matches!(verify_request(Q, &test_ring(), T0 - 301), Err(TsigFailure::BadTime { .. })));
}

#[test]
fn modified_message_is_badsig() {
    let mut q = Q.to_vec();
    q[2] ^= 0x01; // flip RD
    assert!(matches!(verify_request(&q, &test_ring(), T0), Err(TsigFailure::BadSig)));
}

#[test]
fn unknown_key_is_badkey() {
    assert!(matches!(verify_request(Q, &KeyRing::default(), T0), Err(TsigFailure::BadKey)));
}

#[test]
fn unsigned_message_is_reported_unsigned() {
    let t = find_tsig(Q).unwrap().unwrap();
    let mut plain = Q[..t.start].to_vec();
    plain[11] -= 1;
    assert!(matches!(verify_request(&plain, &test_ring(), T0), Ok(Verified::Unsigned)));
}

#[test]
fn response_signature_is_byte_identical_to_miekg() {
    let Ok(Verified::Signed { key, request_mac }) = verify_request(Q, &test_ring(), T0) else { panic!("verify") };
    let t = find_tsig(R).unwrap().unwrap();
    let mut unsigned = R[..t.start].to_vec();
    let ar = u16::from_be_bytes([unsigned[10], unsigned[11]]) - 1;
    unsigned[10..12].copy_from_slice(&ar.to_be_bytes());
    sign_response(&mut unsigned, &key, T0, &request_mac, true, &[]);
    assert_eq!(unsigned, R);
}

#[test]
fn stream_signer_chains_macs() {
    let Ok(Verified::Signed { key, request_mac }) = verify_request(Q, &test_ring(), T0) else { panic!("verify") };
    let t = find_tsig(R).unwrap().unwrap();
    let base = R[..t.start].to_vec();
    let mut s = StreamSigner::new(key.clone(), request_mac.clone());
    let (mut m1, mut m2) = (base.clone(), base.clone());
    for m in [&mut m1, &mut m2] {
        let ar = u16::from_be_bytes([m[10], m[11]]) - 1;
        m[10..12].copy_from_slice(&ar.to_be_bytes());
    }
    s.sign(&mut m1, T0);
    s.sign(&mut m2, T0);
    assert_eq!(m1, R, "first message is a normal response signature");
    assert_ne!(find_tsig(&m2).unwrap().unwrap().mac, find_tsig(&m1).unwrap().unwrap().mac, "second MAC covers the first");
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib tsig_tests` — expect FAIL with "unresolved import `crate::tsig::find_tsig`".
- [ ] Implement the server side in `tsig.rs` on M3's primitives (MAC inputs per RFC 8945 §4.3.3 and §5.3 are M3's `variables` and `response_input`; `ring::hmac::verify` compares in constant time):

```rust
/// Request MAC input: the message without its TSIG RR, ARCOUNT decremented, ID = original ID, then
/// the TSIG variables of the request (M3's `variables(key, time, error, other, false)`).
fn request_input(msg: &[u8], t: &TsigRecord, key: &TsigKey) -> Zeroizing<Vec<u8>> {
    let mut m = Zeroizing::new(msg[..t.start].to_vec());
    m[0..2].copy_from_slice(&t.original_id.to_be_bytes());
    let ar = u16::from_be_bytes([m[10], m[11]]) - 1;
    m[10..12].copy_from_slice(&ar.to_be_bytes());
    m.extend(variables(key, t.time_signed, t.error, &t.other, false));
    m
}
```

`find_tsig` walks all sections with M3's `skip_name` (like `tsig_offset`); a TSIG anywhere but last, more than one TSIG, or class ≠ ANY → `TsigError::Malformed`; it returns the record parsed by M3's `parse_tsig` plus its `start`. `verify_request`: no TSIG → `Unsigned`; malformed → `FormErr`; key lookup `ring.get(&key_name.to_lowercase().to_bytes())` → `BadKey`; algorithm name must equal the key's `alg.name()` (case-insensitive) → `BadKey`; MAC length must equal the full digest length → `BadSig`; `hmac::verify` over `request_input` → `BadSig`; then `|now - time_signed| > fudge` → `BadTime { key, request_mac, time_signed }`. `sign_response_error` is M3's `sign_response` with a non-zero error and `other` (both covered by the variables). `StreamSigner::sign` calls `sign_response(msg, &key, now, &prev_mac, first, &[])` and keeps the returned MAC, `first = false` after the first message. `error_response` builds the header (ID, QR, opcode copied, rcode NOTAUTH = 9), copies the question, and appends a TSIG RR with an empty MAC for BADKEY/BADSIG, or signs it with `sign_response_error(error = 18, other = 48-bit server time)` for BADTIME.

- [ ] Extend `dispatch::unparsed`: parse with `msg::Question::parse`; when `tsig_at` is set, opcode is QUERY and qtype ∉ {AXFR, IXFR}: zone not hosted → `AuthOutcome::Reply` of an unsigned REFUSED; `verify_request(packet, &ctx.shared.auth.keyring, clock::unix_now())` failure → `error_response`; `Unsigned` cannot happen here; success → `answer::respond` into a `Vec` with `max_len` = the transport limit (`edns::reply_limit`, UDP capped at 1232) minus the TSIG size (key name + alg name + 16 + digest length), then `sign_response(&mut v, &key, now, &request_mac, true, &[])`; the bytes are copied into `out` (they fit by construction) and returned as `AuthOutcome::Reply(n)`, which `handle_packet` sends and logs like any fast-path reply.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib tsig_tests` and `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::rpz` — expect PASS.
- As built (reconciled with the code): Task 6's engine steps (module move, `TsigAlg::HmacSha384`/`from_proto`/`digest_len`, `KeyRing`, `AuthState.keyring`, the `KeyMaterial` arm in `control::session`) landed with this task because Task 6 was not committed. M3's private `variables` gained a `fudge` argument so verification uses the request's fudge; `TsigKey::lower_wire_name` was added; `find_tsig` is its own section walk. The signed-query TSIG reserve is key name + algorithm name + 10 (RR header) + 16 + digest; the reply echoes OPT (`udp_size` 1232) when the query had one. Extra tests: `tsig_not_last_or_twice_is_formerr`, `error_responses_carry_notauth_and_the_tsig_error`.
- [ ] Commit: `git add engine testdata/tsig mgmt/internal/nzf/tsigvectors_test.go && git commit -m "feat(engine): server-side TSIG on M3's module, cross-checked against miekg vectors"`.

## Task 8: Zone transfers out (AXFR/IXFR, ACL + TSIG) and NOTIFY to secondaries

Files:

- `engine/src/authoritative/xfr.rs` — transfer authorisation, plan, message stream
- `engine/src/authoritative/xfr_tests.rs`
- `engine/src/authoritative/notify_out.rs` — NOTIFY sender with retries
- `engine/src/authoritative/notify_out_tests.rs`
- `engine/src/authoritative/dispatch.rs` — AXFR/IXFR routing: `AuthOutcome::Slow`, `SlowJob`, `run_slow` (modify)
- `engine/src/authoritative/zone.rs`, `engine/src/authoritative/loader.rs` — `DeltaRecords::from_parsed`; `Zone` transfer policy and notify targets from `AuthZone` (modify)
- `engine/src/authoritative/state.rs` — `after_apply` sends NOTIFY for `rt.auth_changed` (modify)
- `engine/src/server/mod.rs` — `FastOutcome::Slow`, `Answerer::answer_frames` (default one frame), `WorkerAnswerer` runs slow jobs (modify)
- `engine/src/server/stream.rs` — `serve_dns_stream` writes every frame of `answer_frames` in order (modify)
- `engine/src/server/udp.rs`, `engine/src/server/rewrite.rs` — handle `FastOutcome::Slow` (modify)
- `engine/src/telemetry/metrics.rs` — `nexora_auth_transfers_total`, `nexora_auth_notify_sent_total` (modify)
- `e2e/harness/named.go` — `(*Env).StartNamedConfig` (primary and secondary zones, keys, updates, also-notify); M3's `StartNamed` becomes a wrapper (modify)
- `e2e/xfr_test.go` — `TestAXFRIXFROut`

Interfaces:

```rust
// xfr.rs
pub const MAX_XFR_MESSAGE: usize = 16384;
pub enum Plan { Refused(u8 /* rcode */), TsigError(TsigFailure), UpToDate(Arc<Zone>), Full(Arc<Zone>), Incremental(Arc<Zone>, usize /* first delta index */) }
pub fn authorize_and_plan(rt: &Runtime, ring: &crate::tsig::KeyRing, raw: &[u8], q: &Question<'_>, client: IpAddr, tcp: bool, now: u64) -> (Plan, Option<(Arc<crate::tsig::TsigKey>, Vec<u8>)>);
pub fn messages(plan: &Plan, raw: &[u8], q: &Question<'_>, tsig: Option<(Arc<crate::tsig::TsigKey>, Vec<u8>)>, now: u64) -> Vec<Vec<u8>>; // each ≤ MAX_XFR_MESSAGE before TSIG
// notify_out.rs
pub struct NotifyJob { pub zone: Box<[u8]>, pub soa_rr: Vec<u8> /* owner+type+class+ttl+rdlen+rdata */, pub target: SocketAddr, pub key: Option<Arc<crate::tsig::TsigKey>> }
pub enum NotifyResult { Acked { attempts: u32 }, Rejected(u8), Timeout, NoKey }
pub async fn send_notify(job: NotifyJob, first_timeout: Duration, attempts: u32) -> NotifyResult; // production: 2 s doubling, 5 attempts
// dispatch.rs
pub enum AuthOutcome { NotHosted, Reply(usize), Slow(SlowJob) }
pub enum SlowKind { Transfer } // Task 11 adds Update
pub struct SlowJob { pub query: Box<[u8]>, pub client: SocketAddr, pub transport: Transport, pub kind: SlowKind }
pub async fn run_slow(ctx: Rc<WorkerCtx>, rt: Arc<Runtime>, job: SlowJob) -> Vec<Vec<u8>>; // complete messages in send order; empty = send nothing
// server/mod.rs
pub enum FastOutcome { Reply(usize), Drop, Miss(MissJob), Rewrite(rewrite::RewriteJob), Slow(authoritative::dispatch::SlowJob) }
pub trait Answerer {
    async fn answer(&self, client: ClientInfo, query: &[u8], out: &mut Vec<u8>);
    /// Stream transports (TCP, DoT): every message of the reply in order. Default: the one `answer` message.
    async fn answer_frames(&self, client: ClientInfo, query: &[u8], frames: &mut Vec<Vec<u8>>);
}
```

```go
package harness
// NamedKey is a TSIG key named knows; SecretB64 is the base64 secret.
type NamedKey struct{ Name, Algorithm, SecretB64 string }
// NamedZone is Type "primary" (Text = zone file; optional AllowTransferKey, AllowUpdateKey, AlsoNotify "ip:port")
// or "secondary" (Primary "ip:port", optional KeyName for the transfer).
type NamedZone struct{ Name, Type, Text, Primary, KeyName, AllowTransferKey, AllowUpdateKey, AlsoNotify string }
type NamedConfig struct{ Keys []NamedKey; Zones []NamedZone }
func (e *Env) StartNamedConfig(c NamedConfig) *Named // Named{Addr, KeyName, KeySecretB64, Proc} as in M3
```

- [ ] Write the failing `engine/src/authoritative/xfr_tests.rs` (declare `pub mod xfr; pub mod notify_out; #[cfg(test)] mod xfr_tests; #[cfg(test)] mod notify_out_tests;`):

```rust
use super::set::AuthSet;
use super::xfr::{messages, plan_for_tests, Plan, MAX_XFR_MESSAGE};
use super::zone::{DeltaRecords, Zone};
use super::zone_tests::{DELTA, FULL};
use super::{msg::Question, nzf};
use hickory_proto::op::{Message, MessageType, OpCode, Query, ResponseCode};
use hickory_proto::rr::rdata::SOA;
use hickory_proto::rr::{DNSClass, Name, RData, Record, RecordType};
use std::sync::Arc;

const BIG: &[u8] = include_bytes!(concat!(env!("CARGO_MANIFEST_DIR"), "/../testdata/nzf/big-full.nzf"));

fn zone_after_delta() -> Arc<Zone> {
    let base = Zone::from_image(&nzf::parse(FULL).unwrap()).unwrap();
    let parsed = nzf::parse(DELTA).unwrap();
    let mut z = base.apply(&parsed).unwrap();
    z.deltas = vec![Arc::new(DeltaRecords::from_parsed(&parsed))];
    Arc::new(z)
}

fn query(name: &str, qtype: RecordType, client_serial: Option<u32>) -> Vec<u8> {
    let mut m = Message::new(0x5151, MessageType::Query, OpCode::Query);
    m.add_query(Query::query(Name::from_ascii(name).unwrap(), qtype));
    if let Some(s) = client_serial {
        let soa = SOA::new(Name::from_ascii("ns1.example.test.").unwrap(), Name::from_ascii("h.example.test.").unwrap(), s, 0, 0, 0, 0);
        let mut r = Record::from_rdata(Name::from_ascii(name).unwrap(), 0, RData::SOA(soa));
        r.dns_class = DNSClass::IN;
        m.authorities.push(r);
    }
    m.to_vec().unwrap()
}

fn answers(plan: &Plan, raw: &[u8]) -> (Vec<RecordType>, usize) {
    let q = Question::parse(raw).unwrap();
    let msgs = messages(plan, raw, &q, None, 0);
    let mut types = Vec::new();
    for m in &msgs {
        assert!(m.len() <= MAX_XFR_MESSAGE, "message of {} octets", m.len());
        let parsed = Message::from_vec(m).unwrap();
        assert_eq!(parsed.metadata.response_code, ResponseCode::NoError);
        assert!(parsed.metadata.authoritative);
        types.extend(parsed.answers.iter().map(|r| r.record_type()));
    }
    (types, msgs.len())
}

#[test]
fn axfr_is_bracketed_by_soa() {
    let z = zone_after_delta();
    let raw = query("example.test.", RecordType::AXFR, None);
    let (types, _) = answers(&Plan::Full(z.clone()), &raw);
    assert_eq!(types.first(), Some(&RecordType::SOA));
    assert_eq!(types.last(), Some(&RecordType::SOA));
    assert_eq!(types.iter().filter(|t| **t == RecordType::SOA).count(), 2);
    assert_eq!(types.len(), z.records_sorted().len() + 1);
}

#[test]
fn ixfr_plans_follow_rfc1995() {
    let set = AuthSet::from_zones(vec![zone_after_delta()]).unwrap();
    let known = query("example.test.", RecordType::IXFR, Some(2026091301));
    assert!(matches!(plan_for_tests(&set, &known), Plan::Incremental(_, 0)));
    let current = query("example.test.", RecordType::IXFR, Some(2026091302));
    assert!(matches!(plan_for_tests(&set, &current), Plan::UpToDate(_)));
    let unknown = query("example.test.", RecordType::IXFR, Some(2026090000));
    assert!(matches!(plan_for_tests(&set, &unknown), Plan::Full(_)), "history missing: AXFR-style fallback");
}

#[test]
fn incremental_ixfr_sequence() {
    let set = AuthSet::from_zones(vec![zone_after_delta()]).unwrap();
    let raw = query("example.test.", RecordType::IXFR, Some(2026091301));
    let plan = plan_for_tests(&set, &raw);
    let (types, _) = answers(&plan, &raw);
    use RecordType::{A, SOA as S};
    assert_eq!(types, vec![S, S, A, S, A, S], "current SOA, old SOA, deletions, new SOA, additions, current SOA");
}

#[test]
fn large_zone_is_split_into_multiple_messages() {
    let z = Arc::new(Zone::from_image(&nzf::parse(BIG).unwrap()).unwrap());
    let raw = query("big.test.", RecordType::AXFR, None);
    let (types, count) = answers(&Plan::Full(z), &raw);
    assert!(count > 1, "2002 records must not fit one 16 KiB message");
    assert_eq!(types.len(), 2003);
}
```

`plan_for_tests(set, raw)` is a `#[cfg(test)]` helper in `xfr.rs` that runs the plan logic with an open ACL, no TSIG requirement and TCP.

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib authoritative::xfr_tests` — expect FAIL with "unresolved import `super::xfr`".
- [ ] Implement `xfr.rs`:
  - authorisation in this order: zone hosted with `origin == qname` else NOTAUTH; `expired` → SERVFAIL; client IP outside `transfer.allow_cidrs` (empty list refuses all) → REFUSED; when `transfer.tsig_key` is set: unsigned → REFUSED, verification failure → `TsigError`, verified key name ≠ policy key → REFUSED; AXFR over UDP → FORMERR.
  - IXFR: `ixfr_serial` missing → FORMERR; `!SerialLess(client, zone.serial)` → `UpToDate`; index `i` with `deltas[i].from_serial == client` and `deltas[i..]` contiguous to `zone.serial` → `Incremental(i)`; otherwise `Full`. On UDP anything but `UpToDate` is answered as a single SOA (the client retries over TCP).
  - `messages`: header copies ID, QR=1, AA=1, opcode 0; the first message carries the question, later ones QDCOUNT=0; records are appended through `writer.rs` (question-suffix compression) until the next RR would exceed `MAX_XFR_MESSAGE`, then a new message starts. `Full`: SOA, every record of `records_sorted()` except the apex SOA, SOA. `Incremental(i)`: current SOA, then for each delta: its `deleted` (first is old SOA) and `added` (first is new SOA), then current SOA. `UpToDate`: one message with the current SOA. With TSIG, each message is signed by one `StreamSigner`.
  - metrics: axfr→`result="full"`, ixfr→`incremental|full|uptodate`, any refusal→`result="refused"`.
- [ ] Add `DeltaRecords::from_parsed` in `zone.rs` (owned copies of `Parsed.a` / `Parsed.b`) and have the loader (Task 4) use it. `Zone` gains `transfer_allow: Vec<ipnet::IpNet>`, `transfer_key: Option<Box<[u8]>>` (lowercase wire name) and `notify: Vec<(SocketAddr, Option<Box<[u8]>>)>`, set by the loader from `AuthZone.transfer` and `AuthZone.notify`.
- [ ] Route transfers. In `dispatch::fast`, a hosted AXFR/IXFR over `Transport::Tcp` or `Transport::Dot` returns `AuthOutcome::Slow(SlowJob { query: packet.into(), client, transport, kind: SlowKind::Transfer })`, over `Doh`/`Doq` a REFUSED reply; `dispatch::unparsed` does the same for an IXFR (NSCOUNT=1) on streams and answers it inline on UDP (a single SOA, or the refusal from `authorize_and_plan`). `handle_packet` maps `AuthOutcome::Slow(job)` to `FastOutcome::Slow(job)`. `run_slow` for `Transfer`: `Question::parse`, `authorize_and_plan(&rt, &ctx.shared.auth.keyring, &job.query, &q, job.client.ip(), true, clock::unix_now())`, `messages(...)`, count `ctx.shared.metrics.auth.transfers`. `Answerer` gains `answer_frames` with the default body `let mut out = Vec::new(); self.answer(client, query, &mut out).await; if !out.is_empty() { frames.push(out); }`; `WorkerAnswerer` overrides it to push every message of `run_slow` for `FastOutcome::Slow` (and its `answer` takes the first message). `serve_dns_stream`'s per-query task calls `answer_frames` and sends each frame (length prefix + message, at most 65535 octets) to the writer channel in order, holding its pipelining permit until the last frame is queued. `server/udp.rs` spawns `run_slow` for `FastOutcome::Slow` and sends the first message with `send_reply`; `rewrite.rs`'s re-entry treats `FastOutcome::Slow(_)` like `Drop`.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib authoritative::xfr_tests` — expect PASS.
- [ ] Write the failing `engine/src/authoritative/notify_out_tests.rs`:

```rust
use super::notify_out::{send_notify, NotifyJob, NotifyResult};
use std::time::Duration;
use tokio::net::UdpSocket;

fn soa_rr() -> Vec<u8> {
    let mut v = b"\x07example\x04test\x00".to_vec();
    v.extend_from_slice(&[0, 6, 0, 1, 0, 0, 0x0e, 0x10]);
    let rdata = b"\x03ns1\x07example\x04test\x00\x01h\x07example\x04test\x00\x00\x00\x00\x07\x00\x00\x1c\x20\x00\x00\x0e\x10\x00\x12\x75\x00\x00\x00\x01\x2c";
    v.extend_from_slice(&(rdata.len() as u16).to_be_bytes());
    v.extend_from_slice(rdata);
    v
}

#[tokio::test]
async fn retries_until_the_secondary_answers() {
    let secondary = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let target = secondary.local_addr().unwrap();
    let server = tokio::spawn(async move {
        let mut buf = [0u8; 1500];
        let (_, _) = secondary.recv_from(&mut buf).await.unwrap(); // drop the first attempt
        let (n, from) = secondary.recv_from(&mut buf).await.unwrap();
        assert_eq!((buf[2] >> 3) & 0x0f, 4, "opcode NOTIFY");
        assert_ne!(buf[2] & 0x04, 0, "AA set");
        let mut resp = buf[..n].to_vec();
        resp[2] |= 0x80; // QR
        resp[6..8].copy_from_slice(&[0, 0]); // no answer section
        let qend = 12 + 14 + 4;
        resp.truncate(qend);
        secondary.send_to(&resp, from).await.unwrap();
    });
    let job = NotifyJob { zone: b"\x07example\x04test\x00".to_vec().into(), soa_rr: soa_rr(), target, key: None };
    match send_notify(job, Duration::from_millis(50), 5).await {
        NotifyResult::Acked { attempts } => assert_eq!(attempts, 2),
        _ => panic!("expected ack on the second attempt"),
    }
    server.await.unwrap();
}

#[tokio::test]
async fn gives_up_after_the_attempt_budget() {
    let silent = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let job = NotifyJob { zone: b"\x07example\x04test\x00".to_vec().into(), soa_rr: soa_rr(), target: silent.local_addr().unwrap(), key: None };
    assert!(matches!(send_notify(job, Duration::from_millis(20), 3).await, NotifyResult::Timeout));
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib authoritative::notify_out_tests` — expect FAIL with "unresolved import `super::notify_out`".
- [ ] Implement `notify_out.rs`: bind an unconnected UDP socket on the unspecified address of the target's family, `connect(target)`; message = random ID (`rand`), flags opcode 4 + AA, QDCOUNT 1 (zone, SOA, IN), ANCOUNT 1 (`soa_rr`); TSIG-signed with `crate::tsig::sign_request(&mut msg, &key, now)` when `key` is set. For attempt `k` (0-based) wait `first_timeout * 2^k` for a datagram with matching ID, QR=1, opcode 4 (and, when signed, `TsigVerifier::new((*key).clone(), request_mac.clone()).verify(&reply, now)` succeeds); ignore other datagrams; NOERROR → `Acked{attempts: k+1}`; other rcode → `Rejected(rcode)`.
- [ ] In `authoritative::after_apply` (Task 4's hook, once per runtime version), for each `(zone, serial)` in `rt.auth_changed` and each of that zone's `notify` targets: resolve the key name through `shared.auth.keyring` (named but absent → count `NoKey`, send nothing), build the `NotifyJob` from the zone's apex SOA, spawn `send_notify(job, Duration::from_secs(2), 5)` on the control runtime handle stored in `AuthState` and count the result in `shared.metrics.auth.notify_sent`. Without a stored handle (only before `main.rs` builds the control runtime) the jobs are skipped; `main.rs` calls `after_apply` once the handle is set. On process start the first applied snapshot marks every zone changed, so secondaries are notified after restarts.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib authoritative` — expect PASS.
- [ ] Generalise M3's BIND helper in `e2e/harness/named.go`. Replace the `namedConf` template with the builder below, keep `Named`, `writeFile`, `Stop` and the port-retry and run-as-`dev` logic, add a `file string` field (the first primary zone's file, used by `UpdateZone`), and let `serving` probe `version.bind. CH TXT` over TCP when `n.zone` is empty (secondary-only instances). `StartNamed(zoneName, zoneText)` keeps its signature and behaviour: it draws the random `rpz-key.` secret as today and returns `e.StartNamedConfig(NamedConfig{Keys: []NamedKey{{Name: "rpz-key.", Algorithm: "hmac-sha256", SecretB64: secret}}, Zones: []NamedZone{{Name: zoneName, Type: "primary", Text: zoneText, AllowTransferKey: "rpz-key."}}})` with `KeyName`/`KeySecretB64` set.

```go
// NamedKey is a TSIG key named knows; SecretB64 is the base64 secret.
type NamedKey struct{ Name, Algorithm, SecretB64 string }

// NamedZone is one zone: Type "primary" (Text is the zone file; AllowTransferKey restricts
// transfers to that key, otherwise any client may transfer; AllowUpdateKey enables RFC 2136
// updates with that key; AlsoNotify is "ip:port") or "secondary" (Primary is "ip:port", KeyName
// signs the transfer).
type NamedZone struct{ Name, Type, Text, Primary, KeyName, AllowTransferKey, AllowUpdateKey, AlsoNotify string }

// NamedConfig configures StartNamedConfig.
type NamedConfig struct {
	Keys  []NamedKey
	Zones []NamedZone
}

// StartNamedConfig starts `named` with c on a free loopback port, retrying when the port was
// taken, and waits until it serves: SOA of its first primary zone, or CHAOS version.bind.
func (e *Env) StartNamedConfig(c NamedConfig) *Named {
	t := e.T
	t.Helper()
	dir, err := os.MkdirTemp("", "nexora-named-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	n := &Named{dir: dir, cred: pgCredential(t)}
	t.Cleanup(func() {
		n.Stop()
		_ = os.RemoveAll(dir)
	})
	if n.cred != nil {
		if err := os.Chown(dir, int(n.cred.Uid), int(n.cred.Gid)); err != nil {
			t.Fatal(err)
		}
	}
	var body strings.Builder
	for _, k := range c.Keys {
		fmt.Fprintf(&body, "key %q { algorithm %s; secret %q; };\n", k.Name, k.Algorithm, k.SecretB64)
	}
	for _, z := range c.Zones {
		name := dns.Fqdn(z.Name)
		file := strings.TrimSuffix(name, ".") + ".db"
		switch z.Type {
		case "primary":
			n.writeFile(t, file, z.Text)
			if n.zone == "" {
				n.zone, n.file = name, file
			}
			extra := ""
			if z.AllowTransferKey != "" {
				extra += fmt.Sprintf(" allow-transfer { key %q; };", z.AllowTransferKey)
			}
			if z.AllowUpdateKey != "" {
				extra += fmt.Sprintf(" allow-update { key %q; };", z.AllowUpdateKey)
			}
			if z.AlsoNotify != "" {
				host, port, err := net.SplitHostPort(z.AlsoNotify)
				if err != nil {
					t.Fatal(err)
				}
				extra += fmt.Sprintf(" notify explicit; also-notify { %s port %s; };", host, port)
			}
			fmt.Fprintf(&body, "zone %q { type primary; file %q;%s };\n", name, filepath.Join(dir, file), extra)
		case "secondary":
			host, port, err := net.SplitHostPort(z.Primary)
			if err != nil {
				t.Fatal(err)
			}
			key := ""
			if z.KeyName != "" {
				key = fmt.Sprintf(" key %q", z.KeyName)
			}
			fmt.Fprintf(&body, "zone %q { type secondary; primaries { %s port %s%s; }; file %q; };\n", name, host, port, key, filepath.Join(dir, file))
		default:
			t.Fatalf("named zone %s: unknown type %q", name, z.Type)
		}
	}
	for attempt := 1; ; attempt++ {
		port := e.FreePort()
		conf := fmt.Sprintf(`options {
	directory %[1]q;
	listen-on port %[2]d { 127.0.0.1; };
	listen-on-v6 { none; };
	pid-file "%[1]s/named.pid";
	session-keyfile "%[1]s/session.key";
	managed-keys-directory %[1]q;
	recursion no;
	notify no;
	allow-transfer { any; };
	ixfr-from-differences yes;
	dnssec-validation no;
};
controls { };
%[3]s`, dir, port, body.String())
		n.writeFile(t, "named.conf", conf)
		args := []string{"-g", "-c", filepath.Join(dir, "named.conf")}
		if n.cred != nil {
			args = append(args, "-u", "dev")
		}
		n.Addr = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		n.Proc = e.Start("named", args, nil)
		if n.serving() {
			return n
		}
		n.Proc.Stop()
		if attempt == namedAttempts {
			t.Fatalf("named did not start on %s after %d attempts:\n%s", n.Addr, attempt, tail(n.Proc.LogPath, 50))
		}
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./e2e/harness/ -run TestNamed -count=1` — expect PASS (M3's `named_test.go` exercises `StartNamed` through the wrapper).
- [ ] Write the failing `e2e/xfr_test.go`:

```go
package e2e

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
)

type tsigKeyResp struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Secret   string `json:"secret"`
	Revision int64  `json:"revision"`
}

func axfr(addr, zone string, key *tsigKeyResp) ([]dns.RR, error) {
	m := new(dns.Msg)
	m.SetAxfr(zone)
	tr := &dns.Transfer{DialTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second}
	if key != nil {
		m.SetTsig(key.Name, dns.HmacSHA256, 300, time.Now().Unix())
		tr.TsigSecret = map[string]string{key.Name: key.Secret}
	}
	ch, err := tr.In(m, addr)
	if err != nil {
		return nil, err
	}
	var out []dns.RR
	for env := range ch {
		if env.Error != nil {
			return out, env.Error
		}
		out = append(out, env.RR...)
	}
	return out, nil
}

func soaSerial(addr, zone string) (uint32, error) {
	m := new(dns.Msg)
	m.SetQuestion(zone, dns.TypeSOA)
	r, _, err := (&dns.Client{Timeout: time.Second}).Exchange(m, addr)
	if err != nil {
		return 0, err
	}
	if r.Rcode != dns.RcodeSuccess || len(r.Answer) == 0 {
		return 0, fmt.Errorf("SOA %s: rcode %s, %d answers", zone, dns.RcodeToString[r.Rcode], len(r.Answer))
	}
	soa, ok := r.Answer[0].(*dns.SOA)
	if !ok {
		return 0, fmt.Errorf("SOA %s: answer %v", zone, r.Answer[0])
	}
	return soa.Serial, nil
}

func getZone(t *testing.T, api *harness.API, id string) zoneResp {
	t.Helper()
	var z zoneResp
	api.Must(http.MethodGet, "/zones/"+id, nil, &z, http.StatusOK)
	return z
}

func waitSerial(t *testing.T, addr, zone string, want uint32) {
	t.Helper()
	harness.Eventually(t, 15*time.Second, func() error {
		got, err := soaSerial(addr, zone)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("serial %d, want %d", got, want)
		}
		return nil
	})
}

func TestAXFRIXFROut(t *testing.T) {
	e := startAuthEnv(t, []string{"NEXORA_KEK_FILE=" + harness.WriteKEK(t)}, "xfr-1")
	api, eng := e.api, e.engines[0]

	var key tsigKeyResp
	api.Must(http.MethodPost, "/tsig-keys", map[string]any{"name": "xfr-key.", "algorithm": "hmac-sha256"}, &key, http.StatusCreated)
	z := createPrimaryZone(t, api, "xfr.test.", map[string]any{
		"transfer": map[string]any{"allow_cidrs": []string{"127.0.0.1/32"}, "tsig_key_id": key.ID},
	})
	addRecord(t, api, z.ID, "ns1.xfr.test.", "A", "192.0.2.1")
	harness.WaitDNSAnswer(t, eng.DNS, "ns1.xfr.test.", dns.TypeA, 5*time.Second, func(m *dns.Msg) bool { return len(m.Answer) == 1 })

	// positive: TSIG-signed AXFR from an allowed address succeeds
	rrs, err := axfr(eng.DNS, "xfr.test.", &key)
	if err != nil || len(rrs) < 4 || rrs[0].Header().Rrtype != dns.TypeSOA || rrs[len(rrs)-1].Header().Rrtype != dns.TypeSOA {
		t.Fatalf("signed AXFR: %d records, err=%v", len(rrs), err)
	}
	// negative: the same transfer without TSIG is refused
	if _, err := axfr(eng.DNS, "xfr.test.", nil); err == nil || !strings.Contains(err.Error(), "bad xfr rcode: 5") {
		t.Fatalf("unsigned AXFR: got %v, want REFUSED", err)
	}

	named := e.env.StartNamedConfig(harness.NamedConfig{
		Keys:  []harness.NamedKey{{Name: "xfr-key.", Algorithm: "hmac-sha256", SecretB64: key.Secret}},
		Zones: []harness.NamedZone{{Name: "xfr.test.", Type: "secondary", Primary: eng.DNS, KeyName: "xfr-key."}},
	})
	initial := getZone(t, api, z.ID)
	waitSerial(t, named.Addr, "xfr.test.", initial.Serial)

	// the secondary's address is known only now: add it as a NOTIFY target
	api.Must(http.MethodPatch, "/zones/"+z.ID, map[string]any{
		"revision": initial.Revision,
		"notify":   []map[string]any{{"address": named.Addr, "tsig_key_id": key.ID}},
	}, nil, http.StatusOK)
	waitLatestApplied(t, api, "xfr-1")

	ixfrBefore := eng.Metric(t, "nexora_auth_transfers_total", map[string]string{"type": "ixfr", "result": "incremental"})
	addRecord(t, api, z.ID, "www.xfr.test.", "A", "192.0.2.80")
	edited := getZone(t, api, z.ID)
	if edited.Serial == initial.Serial {
		t.Fatalf("zone serial did not advance on edit")
	}
	// SOA refresh is 10800 s: only NOTIFY makes the secondary transfer within 15 s
	waitSerial(t, named.Addr, "xfr.test.", edited.Serial)
	r := harness.MustQuery(t, named.Addr, "www.xfr.test.", dns.TypeA, harness.QueryOpts{})
	if len(r.Answer) != 1 {
		t.Fatalf("secondary does not serve the new record: %v", r)
	}
	if after := eng.Metric(t, "nexora_auth_transfers_total", map[string]string{"type": "ixfr", "result": "incremental"}); after <= ixfrBefore {
		t.Fatalf("secondary did not use an incremental IXFR (%v -> %v)", ixfrBefore, after)
	}
	if sent := eng.Metric(t, "nexora_auth_notify_sent_total", map[string]string{"result": "acked"}); sent < 1 {
		t.Fatalf("no acknowledged NOTIFY")
	}
}
```

- [ ] Run `scripts/dev-exec.sh make e2e-build` then `scripts/dev-exec.sh go test ./e2e/ -run TestAXFRIXFROut -count=1` — expect PASS (before the engine routing step it fails with "signed AXFR").
- As built (reconciled with the code): every transfer message repeats the question (RFC 5936 §2.2.1 allows it) instead of QDCOUNT=0, so owner names keep question-suffix compression; `xfr::messages` splits on uncompressed RR sizes and writes each message with `writer.rs` (no `Writer` change). Each delta side is emitted SOA first, and a history whose deltas lack an SOA on either side falls back to `Full`. `Plan` carries `xfr::Signing = Option<(Arc<TsigKey>, Vec<u8>)>`; `xfr::count` records the metric. `dispatch::fast` answers an IXFR without the client SOA over UDP with FORMERR. The loader's reuse path also compares the transfer policy and NOTIFY targets, so a PATCH that changes only them takes effect without a serial change (`transfer_policy_and_notify_targets_follow_the_snapshot_without_a_serial_change`); `loader::validate` rejects malformed TSIG key names. `AuthState` keeps a separate `notified_version`, so `after_apply` before the control runtime exists counts loads but leaves NOTIFY to the call made once the handle is set. `NotifyResult` derives `Debug, PartialEq, Eq`; `AuthCounters.notify_sent` is an `Arc` shared with the spawned NOTIFY tasks. Extra tests in `xfr_tests::authorization` (ACL/TSIG decisions, signed multi-message stream verified by `TsigVerifier`, signed SOA and AXFR through `handle_packet`/`run_slow`).
- [ ] Commit: `git add engine e2e && git commit -m "feat(engine): AXFR/IXFR out with ACL and TSIG over TCP and DoT, NOTIFY to secondaries"`.

## Task 9: BIND zone file import and export

Files:

- `mgmt/internal/zonefile/lexer.go` — logical lines: comments, quotes, escapes, parentheses, owner inheritance
- `mgmt/internal/zonefile/parse.go` — directives, owner/TTL/class/type resolution, RDATA via miekg, zone rules
- `mgmt/internal/zonefile/export.go` — stable BIND output
- `mgmt/internal/zonefile/zonefile_test.go`
- `mgmt/internal/zonefile/testdata/all-types.zone`, `testdata/all-types.export.golden`
- `mgmt/internal/zone/import.go` — `Service.Import`, `Service.Export`
- `mgmt/api/openapi.yaml`, `mgmt/internal/api/zonefile.go`, `mgmt/internal/auth/permissions.go`, `web/src/auth/permissions.ts` — `importZoneFile`, `exportZoneFile` (modify/create)
- `mgmt/internal/api/server.go` — `jsonOnly` allows 64 MiB for `POST /api/v1/zones/{zoneId}/import` (modify)
- `e2e/testdata/zones/roundtrip.test.zone`
- `e2e/zonefile_test.go` — `TestZoneFileRoundTrip`

Interfaces:

```go
package zonefile
type Options struct{ AllowedTypes map[uint16]bool; MaxRecords int }
type Result struct{ Origin string; DefaultTTL uint32; SOA *dns.SOA; Records []dns.RR }
type LineError struct{ Line int; Message string }
type Errors []LineError // implements error: "line N: message; ..."
func Parse(r io.Reader, origin string, opts Options) (*Result, error)
func Export(w io.Writer, origin string, defaultTTL uint32, soa *dns.SOA, records []dns.RR) error

package zone
type ImportResult struct{ Zone *Zone; RecordsImported int }
func (s *Service) Import(ctx context.Context, actor auth.Actor, zoneID uuid.UUID, revision int64, content string) (*ImportResult, error)
func (s *Service) Export(ctx context.Context, zoneID uuid.UUID, w io.Writer) error
```

- [ ] Write `mgmt/internal/zonefile/testdata/all-types.zone`:

```
; every managed type, relative names, parentheses and escapes
$ORIGIN example.test.
$TTL 3600
@       IN SOA ns1 hostmaster (
                2026091301 ; serial
                7200       ; refresh
                3600       ; retry
                1209600    ; expire
                300 )      ; minimum
        IN NS    ns1
        IN NS    ns2.example.test.
        IN MX    10 mail
        IN TXT   "v=spf1 -all" "second; string"
        IN CAA   0 issue "letsencrypt.org"
        IN HTTPS 1 . alpn="h2,h3" port=443
ns1     IN A     192.0.2.1
ns2 300 IN AAAA  2001:db8::2
mail    IN A     192.0.2.25
www     IN CNAME @
alias   IN DNAME example.net.
_sip._tcp IN SRV 10 60 5060 sip
sip     IN A     192.0.2.60
ptr     IN PTR   host.example.net.
_443._tcp.www IN TLSA 3 1 1 (
                0C72AC70B745AC19998811B131D662C9AC69DBDBE7CB23E5B514B56664C5D3D6 )
host    IN SSHFP 4 2 123456789ABCDEF67890123456789ABCDEF67890123456789ABCDEF123456789
svc     IN SVCB  1 svc.example.net. alpn=h2 ipv4hint=192.0.2.80
naptr   IN NAPTR 100 10 "S" "SIP+D2U" "" _sip._udp.example.test.
loc     IN LOC   52 22 23.000 N 4 53 32.000 E -2.00m 0.00m 10000m 10m
sub     IN NS    ns.sub
sub     IN DS    60485 13 2 D4B7D520E7BB5F0F67674A0CCEB1E3E0614B93C4F9E99B8383F6A1E4469DA50A
ns.sub  IN A     192.0.2.53
escaped\.dot IN TXT "label with an escaped dot"
Bin\000ary 1d IN A 192.0.2.98
```

- [ ] Write the failing `mgmt/internal/zonefile/zonefile_test.go` (external package `zonefile_test`: `zone` imports `zonefile` for `Import`, so an internal test importing `zone.ManagedTypes` would be an import cycle):

```go
package zonefile_test

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
	"github.com/piwi3910/nexora/mgmt/internal/zonefile"
)

var update = flag.Bool("update", false, "rewrite export golden")

func opts() zonefile.Options { return zonefile.Options{AllowedTypes: zone.ManagedTypes, MaxRecords: 1000000} }

func TestParseAllTypes(t *testing.T) {
	f, _ := os.Open("testdata/all-types.zone")
	defer f.Close()
	res, err := zonefile.Parse(f, "example.test.", opts())
	if err != nil {
		t.Fatal(err)
	}
	if res.SOA == nil || res.SOA.Serial != 2026091301 || res.SOA.Ns != "ns1.example.test." || res.SOA.Minttl != 300 {
		t.Fatalf("SOA: %v", res.SOA)
	}
	if res.DefaultTTL != 3600 || len(res.Records) != 24 {
		t.Fatalf("default ttl %d, %d records", res.DefaultTTL, len(res.Records))
	}
	seen := map[uint16]bool{}
	for _, rr := range res.Records {
		seen[rr.Header().Rrtype] = true
	}
	for typ := range zone.ManagedTypes {
		if !seen[typ] {
			t.Errorf("type %s not parsed", dns.TypeToString[typ])
		}
	}
	byName := func(name string, typ uint16) dns.RR {
		for _, rr := range res.Records {
			if rr.Header().Name == name && rr.Header().Rrtype == typ {
				return rr
			}
		}
		t.Fatalf("%s %s missing", name, dns.TypeToString[typ])
		return nil
	}
	if c := byName("www.example.test.", dns.TypeCNAME).(*dns.CNAME); c.Target != "example.test." {
		t.Fatalf("@ in rdata: %q", c.Target)
	}
	if byName("ns2.example.test.", dns.TypeAAAA).Header().Ttl != 300 {
		t.Fatal("explicit TTL ignored")
	}
	if byName("Bin\\000ary.example.test.", dns.TypeA).Header().Ttl != 86400 {
		t.Fatal("1d TTL unit not parsed")
	}
	if txt := byName("example.test.", dns.TypeTXT).(*dns.TXT); len(txt.Txt) != 2 || txt.Txt[1] != "second; string" {
		t.Fatalf("quoted semicolon treated as comment: %q", txt.Txt)
	}
	byName("escaped\\.dot.example.test.", dns.TypeTXT)
}

func TestParseRefusals(t *testing.T) {
	cases := map[string]struct{ file, want string }{
		"include":     {"$ORIGIN example.test.\n$TTL 60\n$INCLUDE other.zone\n", "line 3: $INCLUDE is not supported"},
		"generate":    {"$ORIGIN example.test.\n$TTL 60\n$GENERATE 1-10 h$ A 192.0.2.$\n", "line 3: $GENERATE is not supported"},
		"type":        {"$ORIGIN example.test.\n$TTL 60\nx IN HINFO a b\n", "line 3: unsupported record type HINFO"},
		"dnssec":      {"$ORIGIN example.test.\n$TTL 60\n@ IN DNSKEY 257 3 13 AAAA\n", "line 3: unsupported record type DNSKEY"},
		"class":       {"$ORIGIN example.test.\n$TTL 60\nx CH A 192.0.2.1\n", "line 3: only class IN is supported"},
		"outside":     {"$ORIGIN example.test.\n$TTL 60\nwww.example.org. IN A 192.0.2.1\n", "line 3: www.example.org. is outside example.test."},
		"no ttl":      {"$ORIGIN example.test.\nx IN A 192.0.2.1\n", "line 2: no TTL"},
		"paren":       {"$ORIGIN example.test.\n$TTL 60\nx IN TXT ( \"a\"\n", "unbalanced '('"},
		"bad rdata":   {"$ORIGIN example.test.\n$TTL 60\nx IN A 999.1.1.1\n", "line 3:"},
		"second soa":  {"$ORIGIN example.test.\n$TTL 60\n@ SOA a. b. 1 2 3 4 5\n@ SOA a. b. 2 2 3 4 5\n", "line 4: duplicate SOA"},
		"missing soa": {"$ORIGIN example.test.\n$TTL 60\n@ NS ns1\n", "no SOA record at example.test."},
	}
	for name, c := range cases {
		_, err := zonefile.Parse(strings.NewReader(c.file), "example.test.", opts())
		var le zonefile.Errors
		if err == nil || !strings.Contains(err.Error(), c.want) || (strings.HasPrefix(c.want, "line") && !errors.As(err, &le)) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}

func TestExportIsStableAndReparses(t *testing.T) {
	f, _ := os.Open("testdata/all-types.zone")
	res, err := zonefile.Parse(f, "example.test.", opts())
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	var a, b bytes.Buffer
	shuffled := append([]dns.RR(nil), res.Records...)
	for i, j := 0, len(shuffled)-1; i < j; i, j = i+1, j-1 {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	}
	if err := zonefile.Export(&a, "example.test.", res.DefaultTTL, res.SOA, res.Records); err != nil {
		t.Fatal(err)
	}
	if err := zonefile.Export(&b, "example.test.", res.DefaultTTL, res.SOA, shuffled); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("export depends on input order")
	}
	if *update {
		os.WriteFile("testdata/all-types.export.golden", a.Bytes(), 0o644)
	}
	golden, _ := os.ReadFile("testdata/all-types.export.golden")
	if !bytes.Equal(a.Bytes(), golden) {
		t.Fatalf("export differs from golden:\n%s", a.String())
	}
	again, err := zonefile.Parse(bytes.NewReader(a.Bytes()), "example.test.", opts())
	if err != nil || len(again.Records) != len(res.Records) {
		t.Fatalf("re-parse: %v (%d records)", err, len(again.Records))
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/zonefile/ -count=1` — expect FAIL with "undefined: Parse".
- [ ] Implement `lexer.go`:

```go
type logicalLine struct {
	line       int
	ownerBlank bool
	fields     []string // quoted strings keep their quotes and escapes
}

func splitLines(r io.Reader) ([]logicalLine, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var out []logicalLine
	var cur logicalLine
	depth, n := 0, 0
	for sc.Scan() {
		n++
		text := sc.Text()
		if depth == 0 {
			cur = logicalLine{line: n, ownerBlank: len(text) > 0 && (text[0] == ' ' || text[0] == '\t')}
		}
		var tok strings.Builder
		flush := func() {
			if tok.Len() > 0 {
				cur.fields = append(cur.fields, tok.String())
				tok.Reset()
			}
		}
		inQuote, escaped := false, false
	scan:
		for i := 0; i < len(text); i++ {
			c := text[i]
			switch {
			case escaped:
				tok.WriteByte(c)
				escaped = false
			case c == '\\':
				tok.WriteByte(c)
				escaped = true
			case inQuote:
				tok.WriteByte(c)
				if c == '"' {
					inQuote = false
					flush()
				}
			case c == '"':
				flush()
				tok.WriteByte(c)
				inQuote = true
			case c == ';':
				break scan
			case c == '(':
				flush()
				depth++
			case c == ')':
				flush()
				if depth == 0 {
					return nil, Errors{{Line: n, Message: "unbalanced ')'"}}
				}
				depth--
			case c == ' ' || c == '\t' || c == '\r':
				flush()
			default:
				tok.WriteByte(c)
			}
		}
		if inQuote {
			return nil, Errors{{Line: n, Message: "unterminated quoted string"}}
		}
		flush()
		if depth == 0 && len(cur.fields) > 0 {
			out = append(out, cur)
		}
	}
	if depth != 0 {
		return nil, Errors{{Line: cur.line, Message: "unbalanced '('"}}
	}
	return out, sc.Err()
}
```

- [ ] Implement `parse.go`:
  - directives (first field starting with `$`, case-insensitive): `$ORIGIN <name>` (relative values are appended to the current origin; must stay inside the zone), `$TTL <ttl>`, `$INCLUDE` → `$INCLUDE is not supported`, `$GENERATE` → `$GENERATE is not supported`, anything else → `unknown directive`.
  - owner: `@` → origin; blank (leading whitespace) → previous owner (`no previous owner` on the first record); names not ending in an unescaped `.` get `.` + origin appended (a trailing `.` preceded by an odd number of `\` is escaped).
  - after the owner, up to two fields in either order: a TTL (`^[0-9]+[smhdwSMHDW0-9]*$`, BIND units, max 2147483647) and a class (`IN`; `CH`, `HS`, `CS`, `ANY` → `only class IN is supported`); then the type (`dns.StringToType[upper]` or `TYPEnnn`); type not in `AllowedTypes` (SOA handled separately) → `unsupported record type X`.
  - TTL precedence: explicit, `$TTL`, last explicit TTL, else `no TTL`.
  - RDATA: `text := fmt.Sprintf("$ORIGIN %s\n%s %d IN %s %s\n", curOrigin, owner, ttl, typ, strings.Join(rdata, " "))`, parsed with `zp := dns.NewZoneParser(strings.NewReader(text), curOrigin, ""); rr, ok := zp.Next()`; `!ok` → `zp.Err()` message on this line.
  - zone rules: owner inside the zone origin (`X is outside Z`); exactly one SOA, at the apex (`duplicate SOA`, `no SOA record at Z`); at least one apex NS (`no NS records at Z`); records collected as `Errors` (up to 100) and returned together; more than `MaxRecords` → `zone file has more than N records`.
- [ ] Implement `export.go`: header `$ORIGIN <origin>` and `$TTL <defaultTTL>`; SOA first, then apex NS, then all other records ordered by `(nzf.CanonicalKey(owner wire), type, rdata wire)`; owner written relative (`@` for apex, trailing `.<origin>` removed), a line per record `owner<TAB>ttl<TAB>IN<TAB>TYPE<TAB>rdata` where rdata is the part of `rr.String()` after the fourth tab.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/zonefile/ -count=1 -update` then without `-update` — expect PASS.
- [ ] Implement `zone.Service.Import` through `Service.Mutate`: zone kind must be primary (`ErrReadOnly`), revision must match (`ErrConflict`), `zonefile.Parse` failures → `ValidationError{Code: "zone_file_invalid", Details: …}`, the `validate.go` rules (CNAME exclusivity, DNAME occlusion, DS placement) applied to the whole parsed set with the same codes, then `SetRecords` (delete all rows, insert parsed records), SOA fields `mname, rname, refresh, retry, expire, minimum, ttl` copied to the zone row, `default_ttl` from `$TTL` when present, `RebuildOptions{Serial: &soa.Serial}` (adopted only when RFC 1982-greater, else the next serial). `Export` loads zone row and records and calls `zonefile.Export`.
- [ ] Add OpenAPI: `POST /zones/{zoneId}/import` `importZoneFile` body `{revision: int64, content: string}` → 200 `{zone: Zone, records_imported: int}`, 409, 422 (`zone_file_invalid` with `details`, or `zone_read_only`), all errors `#/components/responses/Error`; `GET /zones/{zoneId}/export` `exportZoneFile` → 200 `text/plain; charset=utf-8` (the strict server's `ExportZoneFile200TextResponse`) with `Content-Disposition: attachment; filename="<zone>zone"`. M1's `jsonOnly` caps every body at `maxBodyBytes` (4 MiB) before routing; add `const maxZoneImportBytes = 64 << 20` and use it when `r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/zones/") && strings.HasSuffix(r.URL.Path, "/import")`; a larger body still maps to 400 `request body too large`. Permissions: `importZoneFile` operator, `exportZoneFile` viewer (Go and `permissions.ts`). Regenerate the API.
- [ ] Write `e2e/testdata/zones/roundtrip.test.zone` as the content of `all-types.zone` with every `example.test` replaced by `roundtrip.test`, and the failing `e2e/zonefile_test.go`:

```go
package e2e

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piwi3910/nexora/e2e/harness"
)

func TestZoneFileRoundTrip(t *testing.T) {
	e := startAuthEnv(t, nil)
	api := e.api

	original, err := os.ReadFile("testdata/zones/roundtrip.test.zone")
	if err != nil {
		t.Fatal(err)
	}
	z := createPrimaryZone(t, api, "roundtrip.test.", nil)
	var imported struct {
		Zone            zoneResp `json:"zone"`
		RecordsImported int      `json:"records_imported"`
	}
	api.Must(http.MethodPost, "/zones/"+z.ID+"/import", map[string]any{"revision": z.Revision, "content": string(original)}, &imported, http.StatusOK)
	if imported.RecordsImported != 24 || imported.Zone.Serial != 2026091301 {
		t.Fatalf("import: %+v", imported)
	}

	// negative after positive: a stale revision is a conflict, not a lost write
	if status, _ := api.Do(http.MethodPost, "/zones/"+z.ID+"/import", map[string]any{"revision": z.Revision, "content": string(original)}, nil); status != http.StatusConflict {
		t.Fatalf("stale import: status %d, want 409", status)
	}

	req, _ := http.NewRequest(http.MethodGet, api.Base+"/api/v1/zones/"+z.ID+"/export", nil)
	req.Header.Set("Authorization", "Bearer "+api.Bearer)
	resp, err := api.HC.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("export: %v %v", err, resp)
	}
	exported, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	dir := t.TempDir()
	a, b := filepath.Join(dir, "original.zone"), filepath.Join(dir, "exported.zone")
	os.WriteFile(a, original, 0o644)
	os.WriteFile(b, exported, 0o644)
	out, err := exec.Command("ldns-compare-zones", "-a", "-s", "-e", a, b).CombinedOutput()
	if err != nil {
		t.Fatalf("zones differ (exit %v):\n%s\n--- exported ---\n%s", err, out, exported)
	}
	if !strings.Contains(string(out), "+0") || !strings.Contains(string(out), "-0") || !strings.Contains(string(out), "~0") {
		t.Fatalf("unexpected ldns-compare-zones output: %s", out)
	}
}
```

- [ ] Run `scripts/dev-exec.sh make e2e-build` then `scripts/dev-exec.sh go test ./e2e/ -run TestZoneFileRoundTrip -count=1` — expect PASS (before the API step it fails with "POST /zones/…/import: status 404").
      Built (as implemented): (1) the lexer does not split a token at a quote, so `alpn="h2,h3"` stays one field (the plan's flush at `"` turned it into `alpn= "h2,h3"`, which miekg rejects). (2) File-level problems (`no SOA record at Z`, `no NS records at Z`) are `LineError{Line: 0}` and print without a `line N:` prefix. (3) `SetRecords` stores a repeated record once (BIND behaviour) instead of failing on the unique index. (4) The strict server's export type is `ExportZoneFile200TextplainCharsetUtf8Response`; `Content-Disposition` is declared as a response header in `openapi.yaml`. (5) `Service.Import` also refuses SOA timers of 0 (refresh/retry/expire) with `invalid_soa`.
- [ ] Commit: `git add mgmt web/src/api/schema.d.ts web/src/auth/permissions.ts e2e && git commit -m "feat(mgmt): BIND zone file import and stable export"`.

## Task 10: Secondary zones — engine NOTIFY intake, management-plane AXFR/IXFR pulls and SOA timers

Files:

- `engine/src/authoritative/notify_in.rs`, `engine/src/authoritative/notify_in_tests.rs` — accept NOTIFY, reply, forward
- `engine/src/authoritative/dispatch.rs` — opcode 4 routing in `unparsed` (modify)
- `engine/src/authoritative/state.rs` — `AuthState` holds the control-stream sender and implements `NotifySink` (modify)
- `engine/src/control.rs` — `session` raises its `EngineMessage` channel from 16 to 1024, attaches the sender to `shared.auth` after connecting and detaches it when the stream ends (modify)
- `engine/src/telemetry/metrics.rs` — `nexora_auth_notify_received_total` (modify)
- `mgmt/internal/xfrin/ixfr.go` — interpret IXFR/AXFR answer streams
- `mgmt/internal/xfrin/refresh.go` — SOA check, transfer, apply, timers
- `mgmt/internal/xfrin/scheduler.go` — due-zone loop, `LISTEN nexora_zone_refresh`, advisory locks
- `mgmt/internal/xfrin/ixfr_test.go`, `mgmt/internal/xfrin/refresh_test.go`
- `mgmt/internal/control/server.go` — `Server.OnNotify` called from `receive` for `EngineMessage_NotifyReceived` (modify)
- `mgmt/internal/zone/service.go` — secondary create/update validation, `Service.RefreshNow` (modify)
- `mgmt/api/openapi.yaml`, `mgmt/internal/api/zones.go`, `mgmt/internal/auth/permissions.go`, `web/src/auth/permissions.ts` — `refreshZone` (modify)
- `mgmt/cmd/nexora-mgmt/main.go` — start the scheduler (modify)

Interfaces:

```rust
pub trait NotifySink: Send + Sync { fn notify(&self, ev: proto::NotifyReceived) -> bool; } // false = queue full or disconnected
pub fn handle_notify(raw: &[u8], client: SocketAddr, set: &AuthSet, ring: &crate::tsig::KeyRing, sink: &dyn NotifySink, now: u64) -> Vec<u8>;
// state.rs
impl AuthState {
    pub fn attach(&self, tx: tokio::sync::mpsc::Sender<proto::EngineMessage>); // control::session, after connecting
    pub fn detach(&self);                                                     // control::session, when the stream ends
}
impl NotifySink for AuthState { /* try_send EngineMessage{notify_received} on the attached sender */ }
```

```go
package xfrin
type Diff struct{ FromSerial, ToSerial uint32; Deleted, Added []dns.RR }
type Answer struct{ UpToDate bool; Full []dns.RR /* includes SOA first */; Diffs []Diff; Serial uint32 }
func Interpret(rrs []dns.RR, requestedSerial uint32, ixfr bool) (*Answer, error)
type Refresher struct{ Store *store.Store; Zones *zone.Service; TSIG *tsigkey.Service; Now func() time.Time; Dial time.Duration }
func (r *Refresher) Refresh(ctx context.Context, zoneID uuid.UUID, trigger string) error // trigger: "timer" | "notify" | "manual" | "create"
type Scheduler struct{ Store *store.Store; Refresher *Refresher; Tick time.Duration }
func (s *Scheduler) Run(ctx context.Context) error
func (s *Scheduler) Notify(ctx context.Context, zoneName, source string) error // validates source against primaries, sets next_refresh_at=now(), pg_notify('nexora_zone_refresh', id)

package control
// Server gains OnNotify func(ctx context.Context, engineID string, ev *controlv1.NotifyReceived), set in main.go to Scheduler.Notify.
```

- [ ] Write the failing `mgmt/internal/xfrin/ixfr_test.go`:

```go
package xfrin

import (
	"testing"

	"github.com/miekg/dns"
)

func rr(t *testing.T, s string) dns.RR {
	t.Helper()
	r, err := dns.NewRR(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func soa(t *testing.T, serial string) dns.RR {
	return rr(t, "up.test. 300 IN SOA ns.up.test. h.up.test. "+serial+" 3600 600 86400 300")
}

func TestInterpretIncremental(t *testing.T) {
	a1, a2 := rr(t, "a.up.test. 300 IN A 192.0.2.1"), rr(t, "b.up.test. 300 IN A 192.0.2.2")
	stream := []dns.RR{soa(t, "3"), soa(t, "1"), a1, soa(t, "2"), soa(t, "2"), soa(t, "3"), a2, soa(t, "3")}
	ans, err := Interpret(stream, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if ans.UpToDate || ans.Full != nil || len(ans.Diffs) != 2 || ans.Serial != 3 {
		t.Fatalf("%+v", ans)
	}
	if ans.Diffs[0].FromSerial != 1 || ans.Diffs[0].ToSerial != 2 || len(ans.Diffs[0].Deleted) != 1 || len(ans.Diffs[0].Added) != 0 {
		t.Fatalf("diff 0: %+v", ans.Diffs[0])
	}
	if ans.Diffs[1].FromSerial != 2 || len(ans.Diffs[1].Added) != 1 {
		t.Fatalf("diff 1: %+v", ans.Diffs[1])
	}
}

func TestInterpretAXFRStyleAndUpToDate(t *testing.T) {
	full := []dns.RR{soa(t, "9"), rr(t, "up.test. 300 IN NS ns.up.test."), rr(t, "ns.up.test. 300 IN A 192.0.2.53"), soa(t, "9")}
	ans, err := Interpret(full, 1, true)
	if err != nil || ans.Full == nil || len(ans.Full) != 3 || ans.Serial != 9 {
		t.Fatalf("axfr-style: %+v %v", ans, err)
	}
	ans, err = Interpret([]dns.RR{soa(t, "9")}, 9, true)
	if err != nil || !ans.UpToDate {
		t.Fatalf("up to date: %+v %v", ans, err)
	}
	if _, err := Interpret([]dns.RR{soa(t, "9"), rr(t, "x.up.test. 300 IN A 192.0.2.9")}, 1, false); err == nil {
		t.Fatal("AXFR without closing SOA accepted")
	}
	if _, err := Interpret([]dns.RR{soa(t, "3"), soa(t, "1"), soa(t, "2"), soa(t, "5"), soa(t, "3")}, 1, true); err == nil {
		t.Fatal("non-contiguous IXFR accepted")
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/xfrin/ -run TestInterpret -count=1` — expect FAIL with "undefined: Interpret".
- [ ] Implement `Interpret`:

```go
func Interpret(rrs []dns.RR, requested uint32, ixfr bool) (*Answer, error) {
	if len(rrs) == 0 {
		return nil, errors.New("empty transfer")
	}
	first, ok := rrs[0].(*dns.SOA)
	if !ok {
		return nil, errors.New("transfer does not start with SOA")
	}
	serial := first.Serial
	if len(rrs) == 1 {
		if ixfr && !zone.SerialLess(requested, serial) {
			return &Answer{UpToDate: true, Serial: serial}, nil
		}
		return nil, errors.New("transfer truncated after first SOA")
	}
	last, ok := rrs[len(rrs)-1].(*dns.SOA)
	if !ok || last.Serial != serial {
		return nil, errors.New("transfer does not end with the starting SOA")
	}
	if _, second := rrs[1].(*dns.SOA); !ixfr || !second {
		body := rrs[1 : len(rrs)-1]
		for _, r := range body {
			if r.Header().Rrtype == dns.TypeSOA {
				return nil, errors.New("SOA inside AXFR body")
			}
		}
		return &Answer{Full: append([]dns.RR{first}, body...), Serial: serial}, nil
	}
	var diffs []Diff
	i := 1
	expect := requested
	for i < len(rrs)-1 {
		from, ok := rrs[i].(*dns.SOA)
		if !ok || from.Serial != expect {
			return nil, fmt.Errorf("IXFR sequence does not continue from serial %d", expect)
		}
		d := Diff{FromSerial: from.Serial}
		i++
		for ; i < len(rrs)-1 && rrs[i].Header().Rrtype != dns.TypeSOA; i++ {
			d.Deleted = append(d.Deleted, rrs[i])
		}
		to, ok := rrs[i].(*dns.SOA)
		if !ok || i == len(rrs)-1 {
			return nil, errors.New("IXFR sequence without new SOA")
		}
		d.ToSerial = to.Serial
		i++
		for ; i < len(rrs)-1 && rrs[i].Header().Rrtype != dns.TypeSOA; i++ {
			d.Added = append(d.Added, rrs[i])
		}
		diffs = append(diffs, d)
		expect = d.ToSerial
	}
	if expect != serial {
		return nil, fmt.Errorf("IXFR ends at %d, SOA says %d", expect, serial)
	}
	return &Answer{Diffs: diffs, Serial: serial}, nil
}
```

- [ ] Run the interpret tests — expect PASS.
- [ ] Write the failing `mgmt/internal/xfrin/refresh_test.go` with an in-process miekg primary:

```go
package xfrin_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/xfrin"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

var actor = auth.Actor{Type: "user", ID: "t", Name: "t"}

type fakePrimary struct {
	mu      sync.Mutex
	serial  uint32
	records []string
	history map[uint32][2][]string // from serial -> (deleted, added) to serial+1
	queries []uint16
}

func (p *fakePrimary) soa() dns.RR {
	r, _ := dns.NewRR("up.test. 300 IN SOA ns.up.test. h.up.test. " + itoa(p.serial) + " 3600 600 86400 300")
	return r
}

func (p *fakePrimary) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	p.mu.Lock()
	defer p.mu.Unlock()
	q := req.Question[0]
	p.queries = append(p.queries, q.Qtype)
	switch q.Qtype {
	case dns.TypeSOA:
		m := new(dns.Msg)
		m.SetReply(req)
		m.Authoritative = true
		m.Answer = []dns.RR{p.soa()}
		w.WriteMsg(m)
	case dns.TypeAXFR, dns.TypeIXFR:
		var rrs []dns.RR
		rrs = append(rrs, p.soa())
		if q.Qtype == dns.TypeIXFR {
			from := req.Ns[0].(*dns.SOA).Serial
			if h, ok := p.history[from]; ok && from+1 == p.serial {
				old, _ := dns.NewRR("up.test. 300 IN SOA ns.up.test. h.up.test. " + itoa(from) + " 3600 600 86400 300")
				rrs = append(rrs, old)
				rrs = append(rrs, parse(h[0])...)
				rrs = append(rrs, p.soa())
				rrs = append(rrs, parse(h[1])...)
				rrs = append(rrs, p.soa())
				ch := make(chan *dns.Envelope, 1)
				ch <- &dns.Envelope{RR: rrs}
				close(ch)
				(&dns.Transfer{}).Out(w, req, ch)
				return
			}
		}
		rrs = append(rrs, parse(p.records)...)
		rrs = append(rrs, p.soa())
		ch := make(chan *dns.Envelope, 1)
		ch <- &dns.Envelope{RR: rrs}
		close(ch)
		(&dns.Transfer{}).Out(w, req, ch)
	}
}

func TestRefreshAXFRThenIXFRThenUpToDate(t *testing.T) {
	ctx := context.Background()
	p := &fakePrimary{serial: 10, records: []string{"up.test. 300 IN NS ns.up.test.", "ns.up.test. 300 IN A 192.0.2.53", "a.up.test. 300 IN A 192.0.2.1"}, history: map[uint32][2][]string{}}
	addr := startPrimary(t, p)
	st := storetest.New(t)
	zs := &zone.Service{Store: st, Now: time.Now}
	z, err := zs.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "up.test.", Kind: "secondary", Primaries: []zone.Endpoint{{Address: addr}}})
	if err != nil {
		t.Fatal(err)
	}
	r := &xfrin.Refresher{Store: st, Zones: zs, Now: time.Now, Dial: 2 * time.Second}
	if err := r.Refresh(ctx, z.ID, "timer"); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	got, _ := zs.GetZone(ctx, z.ID)
	if !got.Loaded || got.Serial != 10 || got.LastTrigger != "timer" || got.NextRefreshAt.Sub(*got.LastSuccessAt) != 3600*time.Second || got.ExpiresAt.Sub(*got.LastSuccessAt) != 86400*time.Second {
		t.Fatalf("after AXFR: %+v", got)
	}

	p.mu.Lock()
	p.history[10] = [2][]string{{"a.up.test. 300 IN A 192.0.2.1"}, {"b.up.test. 300 IN A 192.0.2.2"}}
	p.records = []string{"up.test. 300 IN NS ns.up.test.", "ns.up.test. 300 IN A 192.0.2.53", "b.up.test. 300 IN A 192.0.2.2"}
	p.serial = 11
	p.queries = nil
	p.mu.Unlock()
	if err := r.Refresh(ctx, z.ID, "notify"); err != nil {
		t.Fatalf("ixfr refresh: %v", err)
	}
	recs, _, _ := zs.ListRecords(ctx, z.ID, "b.up.test.", "A", "", 10)
	gone, _, _ := zs.ListRecords(ctx, z.ID, "a.up.test.", "A", "", 10)
	got, _ = zs.GetZone(ctx, z.ID)
	if got.Serial != 11 || len(recs) != 1 || len(gone) != 0 || p.queries[len(p.queries)-1] != dns.TypeIXFR {
		t.Fatalf("after IXFR: serial=%d b=%d a=%d queries=%v", got.Serial, len(recs), len(gone), p.queries)
	}
	var from, to int64
	st.Pool.QueryRow(ctx, `SELECT from_serial, to_serial FROM zone_journal WHERE zone_id=$1 ORDER BY seq DESC LIMIT 1`, z.ID).Scan(&from, &to)
	if from != 10 || to != 11 {
		t.Fatalf("journal keeps the primary's serials: %d->%d", from, to)
	}

	p.mu.Lock()
	p.queries = nil
	p.mu.Unlock()
	if err := r.Refresh(ctx, z.ID, "timer"); err != nil {
		t.Fatal(err)
	}
	if len(p.queries) != 1 || p.queries[0] != dns.TypeSOA {
		t.Fatalf("up-to-date refresh must stop after the SOA query: %v", p.queries)
	}
}

func TestRefreshFailureRetriesAndExpires(t *testing.T) {
	ctx := context.Background()
	p := &fakePrimary{serial: 5, records: []string{"up.test. 300 IN NS ns.up.test."}}
	addr := startPrimary(t, p)
	st := storetest.New(t)
	zs := &zone.Service{Store: st, Now: time.Now}
	z, _ := zs.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "up.test.", Kind: "secondary", Primaries: []zone.Endpoint{{Address: addr}}})
	r := &xfrin.Refresher{Store: st, Zones: zs, Now: time.Now, Dial: 500 * time.Millisecond}
	if err := r.Refresh(ctx, z.ID, "timer"); err != nil {
		t.Fatal(err)
	}
	dead := deadAddr(t)
	st.Pool.Exec(ctx, `UPDATE zones SET primaries = jsonb_build_array(jsonb_build_object('address', $2::text)), expires_at = now() - interval '1 second' WHERE id=$1`, z.ID, dead)
	if err := r.Refresh(ctx, z.ID, "timer"); err == nil {
		t.Fatal("refresh against a dead primary succeeded")
	}
	got, _ := zs.GetZone(ctx, z.ID)
	if !got.Expired || got.LastError == "" || got.NextRefreshAt.Sub(*got.LastRefreshAt) != 600*time.Second {
		t.Fatalf("after failure: expired=%v err=%q next=%v", got.Expired, got.LastError, got.NextRefreshAt)
	}
}
```

Helpers in the same file: `itoa` (`strconv.FormatUint`), `parse` (`dns.NewRR` over a slice), `startPrimary` (starts `dns.Server` on UDP and TCP at `127.0.0.1:0` with handler `p`, returns `"127.0.0.1:port"`, shuts down on cleanup), `deadAddr` (binds and closes a TCP listener, returns its address).

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/xfrin/ -count=1` — expect FAIL with "undefined: xfrin.Refresher".
- [ ] Implement `Refresher.Refresh`: load the zone (`kind='secondary'`); for each primary in order: SOA query over UDP (TCP on TC) with TSIG when `tsig_key_id` is set (secret via `tsigkey.Service.Secret`; `m.SetTsig(name, dns.HmacSHA256|384|512, 300, now)`, `Client.TsigSecret`), timeout `Dial`; if loaded and `!zone.SerialLess(local, remote)` → up to date; otherwise transfer over TCP with `dns.Transfer` (`SetIxfr(name, local, mname, rname)` when loaded, else `SetAxfr`), collect envelopes, `Interpret`; IXFR `NOTIMP`/`FORMERR`/interpretation errors retry once with AXFR. Apply through `zone.Service.Mutate` (actor `auth.Actor{Type: "system", ID: "xfrin", Name: "system:xfrin"}`, audit action `refreshZone`): full → `SetRecords` with every non-SOA record (all RR types accepted for secondaries, including DNSSEC records); diffs → delete/insert rows per diff; SOA fields copied; `RebuildOptions{Serial: &answer.Serial}`. On success in the same transaction: `loaded=true, last_refresh_at=now, last_success_at=now, next_refresh_at=now+refresh, expires_at=now+expire, expired=false, last_error='', last_trigger=trigger`. On failure (all primaries): `last_refresh_at=now, next_refresh_at=now+retry (600 s default before first load), last_error=<message>`, and when `expires_at <= now` set `expired=true` and publish so engines answer SERVFAIL. SOA timers use the values received from the primary.
- [ ] Implement `Scheduler.Run`: every `Tick` (5 s) and on each `nexora_zone_refresh` notification, `SELECT id FROM zones WHERE kind='secondary' AND (next_refresh_at IS NULL OR next_refresh_at <= now()) ORDER BY next_refresh_at NULLS FIRST LIMIT 20`; per zone acquire `pg_try_advisory_lock(hashtext('zone_refresh:'||id))` on a dedicated connection from `Store.Pool` (as `blocklist.lockList` does), `Refresh`, unlock. `Notify` looks up the secondary zone by name, checks that the source IP equals the IP of one of its primaries (else ignore and count), sets `next_refresh_at = now()`, `last_trigger` for the next run = `notify`, and `pg_notify('nexora_zone_refresh', id)`. `CreateZone` for secondaries validates ≥ 1 primary `ip:port`, uses placeholder SOA fields until the first transfer, and triggers `pg_notify`.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/xfrin/ -count=1` — expect PASS.
- [ ] Write the failing `engine/src/authoritative/notify_in_tests.rs` (declare `pub mod notify_in; #[cfg(test)] mod notify_in_tests;`):

```rust
use crate::tsig::KeyRing;
use super::notify_in::{handle_notify, NotifySink};
use super::nzf;
use super::set::AuthSet;
use super::zone::Zone;
use super::zone_tests::FULL;
use crate::proto;
use hickory_proto::op::{Message, MessageType, OpCode, Query, ResponseCode};
use hickory_proto::rr::{Name, RecordType};
use std::sync::{Arc, Mutex};

#[derive(Default)]
struct Captured(Mutex<Vec<proto::NotifyReceived>>);
impl NotifySink for Captured {
    fn notify(&self, ev: proto::NotifyReceived) -> bool {
        self.0.lock().unwrap().push(ev);
        true
    }
}

fn secondary_set(primary: &str) -> AuthSet {
    let mut z = Zone::from_image(&nzf::parse(FULL).unwrap()).unwrap();
    z.set_secondary_primaries(vec![primary.parse().unwrap()]);
    AuthSet::from_zones(vec![Arc::new(z)]).unwrap()
}

fn notify_msg(zone: &str) -> Vec<u8> {
    let mut m = Message::new(0x7777, MessageType::Query, OpCode::Notify);
    m.metadata.authoritative = true;
    m.add_query(Query::query(Name::from_ascii(zone).unwrap(), RecordType::SOA));
    m.to_vec().unwrap()
}

#[test]
fn notify_from_primary_is_acked_and_forwarded() {
    let sink = Captured::default();
    let set = secondary_set("192.0.2.53:53");
    let resp = handle_notify(&notify_msg("example.test."), "192.0.2.53:40000".parse().unwrap(), &set, &KeyRing::default(), &sink, 0);
    let r = Message::from_vec(&resp).unwrap();
    assert_eq!(r.metadata.op_code, OpCode::Notify);
    assert_eq!(r.metadata.message_type, MessageType::Response);
    assert_eq!(r.metadata.response_code, ResponseCode::NoError);
    assert_eq!(r.metadata.id, 0x7777);
    let evs = sink.0.lock().unwrap();
    assert_eq!(evs.len(), 1);
    assert_eq!(evs[0].zone, "example.test.");
    assert_eq!(evs[0].source, "192.0.2.53:40000");
}

#[test]
fn notify_from_other_source_or_for_unknown_zone_is_refused() {
    let sink = Captured::default();
    let set = secondary_set("192.0.2.53:53");
    let ok = handle_notify(&notify_msg("example.test."), "192.0.2.53:1".parse().unwrap(), &set, &KeyRing::default(), &sink, 0);
    assert_eq!(Message::from_vec(&ok).unwrap().metadata.response_code, ResponseCode::NoError);
    let other = handle_notify(&notify_msg("example.test."), "198.51.100.1:53".parse().unwrap(), &set, &KeyRing::default(), &sink, 0);
    assert_eq!(Message::from_vec(&other).unwrap().metadata.response_code, ResponseCode::Refused);
    let unknown = handle_notify(&notify_msg("example.org."), "192.0.2.53:53".parse().unwrap(), &set, &KeyRing::default(), &sink, 0);
    assert_eq!(Message::from_vec(&unknown).unwrap().metadata.response_code, ResponseCode::NotAuth);
    assert_eq!(sink.0.lock().unwrap().len(), 1, "only the accepted NOTIFY is forwarded");
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib authoritative::notify_in_tests` — expect FAIL with "unresolved import `super::notify_in`".
- [ ] Implement: `Zone` gains `kind` and `primaries: Vec<SocketAddr>` (set by the loader from `AuthZone`; `set_secondary_primaries` is the test setter). `handle_notify`: parse with `msg::Question` (opcode 4, qtype SOA, class IN) else FORMERR; zone hosted with `origin == qname` else NOTAUTH; zone kind primary → REFUSED; TSIG present → verify (`error_response` on failure) and accept when the key belongs to one of the zone's primaries; otherwise the source IP must equal a primary's IP → else REFUSED; accepted → response header with QR, AA, opcode 4, question copied (TSIG-signed when the request was), serial taken from an answer-section SOA when present, `sink.notify(...)` (`false` → `result="dropped"`, otherwise `forwarded`). Route opcode 4 in `dispatch::unparsed` (every transport; `parse_query` answers NOTIMP for opcode 4). Count `nexora_auth_notify_received{result}` in `metrics.auth`. The production `NotifySink` is `AuthState`: `try_send` of `EngineMessage { msg: Some(Msg::NotifyReceived(ev)) }` on the sender `control::session` attached (capacity 1024; `false` when detached or full). `unparsed` passes `&ctx.shared.auth` as the sink and returns the response as `AuthOutcome::Reply`. Mgmt: `Server.receive` gains `case *controlv1.EngineMessage_NotifyReceived:` calling `s.OnNotify(ctx, sub.engineID, m.NotifyReceived)` when set (errors are logged, the stream continues), and `main.go` sets `controlServer.OnNotify` to call `scheduler.Notify(ctx, ev.Zone, ev.Source)`.
- [ ] Add OpenAPI `POST /zones/{zoneId}/refresh` `refreshZone` → 202 (sets `next_refresh_at = now()`, `last_trigger = manual`, `pg_notify`), 422 `zone_not_secondary` (`#/components/responses/Error`); permission operator (Go and `permissions.ts`). Regenerate the API. In `main.go` build `xfrin.Refresher{Store: st, Zones: zones, TSIG: tsigKeys, Now: time.Now, Dial: 5 * time.Second}` and `xfrin.Scheduler{Store: st, Refresher: refresher, Tick: 5 * time.Second}` (sharing the `zone.Service` and `tsigkey.Service` passed in `api.Deps`) and `go scheduler.Run(ctx)`.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib authoritative` and `scripts/dev-exec.sh go test ./mgmt/... -count=1` — expect PASS.
- As built (management plane; recorded after implementation):
  - Pending refresh requests live in migration `mgmt/migrations/00401_zone_refresh_requests.sql`: `zones.refresh_trigger` (`''|notify|manual|create`, the trigger the scheduler passes to the next run, instead of overloading `last_trigger`) and `zones.refresh_requests` (a counter `Refresh` reads first; a request arriving during a refresh keeps `next_refresh_at = now()` instead of being overwritten by the new timer).
  - `zone` gains `RefreshChannel`, `RequestRefresh(ctx, q Execer, id, trigger)` (used by `CreateZone`, `RefreshNow` and `Scheduler.Notify`), `GetZoneByName`, `LoadRecords` and `ApplyRecordChanges(ctx, tx, zoneID, deleted, added)` (delete on owner/type/RDATA ignoring TTL; insert-or-retime; RRset TTL follows), shared by IXFR application and dynamic updates.
  - `ZoneCreate` no longer requires `soa` and `default_ttl` (secondaries take the primary's SOA; the e2e test posts none); a primary without `soa` is 400, `default_ttl` defaults to 3600. `refreshZone` 422 is `zone_not_secondary`.
  - `Server.OnNotify` returns `error` (logged at info). `NotifyIgnored` (`nexora_mgmt_notify_ignored_total`) is registered in `main.go`.
  - Refresher: with a primary TSIG key the SOA response must be signed and every transfer message must verify (`dns.Transfer` rejects unsigned messages); transfers are bounded to `zone.MaxImportRecords` RRs, 30 s per message and 10 min overall; every transferred RR must be class IN inside the zone; received SOA refresh/retry/expire are floored at 5 s. Scheduler runs at most 8 refreshes in parallel and re-checks due-ness under the advisory lock.
  - Extra tests: `TestRefreshWithTSIGRequiresSignedPrimary`, `TestSchedulerLoadsOnCreateAndRefreshesOnNotify`. The plan's refresh test read `p.queries` without the mutex (race detector); reads now take `p.mu`.
- As built (engine; recorded after implementation):
  - `Zone` gains `secondary: bool` (instead of a `kind` enum), `primaries: Vec<SocketAddr>` and `update_keys: Vec<Box<[u8]>>` (lowercase wire key names), set by the loader's `Policy`; `validate` also checks `update_tsig_keys` names.
  - `handle_notify` verifies a present TSIG first (failure → `tsig::error_response`) and signs every later response with it. A verified TSIG does not replace the source check: `AuthZone` does not say which key a primary uses, so the source IP must equal a primary's IP either way (`debt:` comment in `notify_in.rs`).
  - `dispatch::unparsed` routes on the header opcode before `Question::parse`. The forwarded/dropped count is a `CountingSink` wrapping `AuthState`; non-NOERROR replies count `result="refused"`. `name::to_ascii` renders the zone name for `NotifyReceived`.
- [ ] Commit: `git add engine mgmt web/src/api/schema.d.ts web/src/auth/permissions.ts && git commit -m "feat(m4): secondary zones pulled by the management plane with NOTIFY forwarded by engines"`.

## Task 11: RFC 2136 dynamic updates authenticated with TSIG

Files:

- `engine/src/authoritative/update.rs`, `engine/src/authoritative/update_tests.rs` — receive, authenticate, forward, respond
- `engine/src/authoritative/dispatch.rs` — opcode 5 → `AuthOutcome::Slow(SlowJob { kind: SlowKind::Update, .. })` (modify)
- `engine/src/authoritative/state.rs` — `AuthState` implements `UpdateForwarder`: pending waiters by `request_id`, 5 s timeout, `complete_update` (modify)
- `engine/src/control.rs` — `ServerMsg::UpdateResult(r) => shared.auth.complete_update(r)` (modify)
- `engine/src/telemetry/metrics.rs` — `nexora_auth_updates_total` (modify)
- `mgmt/internal/dynupdate/apply.go`, `mgmt/internal/dynupdate/apply_test.go` — prerequisites and update section in one transaction
- `mgmt/internal/control/hub.go`, `mgmt/internal/control/server.go` — `subscriber.results` channel (capacity 64) drained by the send loop; `Server.OnUpdate` called from `receive` for `EngineMessage_UpdateRequest` (modify)
- `mgmt/cmd/nexora-mgmt/main.go` — `controlServer.OnUpdate` wired to `dynupdate.Applier.Apply` (modify)
- `e2e/secondary_update_test.go` — `TestSecondaryAndDynamicUpdate`

Interfaces:

```rust
pub trait UpdateForwarder: Send + Sync {
    fn forward(&self, req: proto::UpdateRequest) -> Pin<Box<dyn Future<Output = Option<proto::UpdateResult>> + Send + '_>>; // None = disconnected or 5 s timeout
}
pub async fn handle_update(raw: Vec<u8>, client: SocketAddr, rt: Arc<Runtime>, ring: Arc<crate::tsig::KeyRing>, fwd: Arc<dyn UpdateForwarder>, now: u64) -> Vec<u8>;
// state.rs
impl UpdateForwarder for AuthState { /* sends EngineMessage{update_request} on the attached sender; None when detached or after 5 s */ }
impl AuthState { pub fn complete_update(&self, r: proto::UpdateResult); }
// dispatch.rs
pub enum SlowKind { Transfer, Update }
```

```go
package dynupdate
type Applier struct{ Zones *zone.Service; TSIG *tsigkey.Service; Now func() time.Time; TSIGCheck bool }
func (a *Applier) Apply(ctx context.Context, engineID string, req *controlv1.UpdateRequest) *controlv1.UpdateResult

package control
// Server gains OnUpdate func(ctx context.Context, engineID string, req *controlv1.UpdateRequest) *controlv1.UpdateResult;
// subscriber gains results chan *controlv1.ServerMessage (capacity 64; never replaced like out).
```

- [ ] Write the failing `mgmt/internal/dynupdate/apply_test.go`:

```go
package dynupdate_test

import (
	"context"
	"testing"
	"time"

	"github.com/miekg/dns"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/dynupdate"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

var actor = auth.Actor{Type: "user", ID: "t", Name: "t"}

func rr(t *testing.T, s string) dns.RR {
	t.Helper()
	r, err := dns.NewRR(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func setup(t *testing.T) (*dynupdate.Applier, *zone.Service, *zone.Zone) {
	t.Helper()
	ctx := context.Background()
	zs := &zone.Service{Store: storetest.New(t), Now: time.Now}
	z, err := zs.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "dyn.test.", Kind: "primary", DefaultTTL: 300,
		SOA: zone.SOA{MName: "ns1.dyn.test.", RName: "h.dyn.test."}, Nameservers: []string{"ns1.dyn.test."}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zs.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "old.dyn.test.", Type: "A", TTL: 300, Data: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	return &dynupdate.Applier{Zones: zs, Now: time.Now, TSIGCheck: false}, zs, z
}

func apply(t *testing.T, a *dynupdate.Applier, build func(m *dns.Msg)) uint32 {
	t.Helper()
	m := new(dns.Msg)
	m.SetUpdate("dyn.test.")
	build(m)
	wire, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	res := a.Apply(context.Background(), "engine-1", &controlv1.UpdateRequest{RequestId: "r1", Zone: "dyn.test.", Client: "127.0.0.1:5000", Message: wire, TsigKey: "ddns-key."})
	return res.Rcode
}

func count(t *testing.T, zs *zone.Service, z *zone.Zone, name, typ string) int {
	recs, _, _ := zs.ListRecords(context.Background(), z.ID, name, typ, "", 100)
	return len(recs)
}

func TestPrerequisitesRFC2136(t *testing.T) {
	a, zs, z := setup(t)
	cases := []struct {
		name  string
		build func(m *dns.Msg)
		rcode int
	}{
		{"rrset exists passes", func(m *dns.Msg) { m.RRsetUsed([]dns.RR{rr(t, "old.dyn.test. 0 IN A 0.0.0.0")}); m.Insert([]dns.RR{rr(t, "p1.dyn.test. 300 IN A 192.0.2.10")}) }, dns.RcodeSuccess},
		{"rrset exists fails", func(m *dns.Msg) { m.RRsetUsed([]dns.RR{rr(t, "none.dyn.test. 0 IN A 0.0.0.0")}); m.Insert([]dns.RR{rr(t, "p2.dyn.test. 300 IN A 192.0.2.11")}) }, dns.RcodeNXRrset},
		{"name not in use fails", func(m *dns.Msg) { m.NameNotUsed([]dns.RR{rr(t, "old.dyn.test. 0 IN A 0.0.0.0")}) }, dns.RcodeYXDomain},
		{"name in use fails", func(m *dns.Msg) { m.NameUsed([]dns.RR{rr(t, "none.dyn.test. 0 IN A 0.0.0.0")}) }, dns.RcodeNameError},
		{"rrset not used fails", func(m *dns.Msg) { m.RRsetNotUsed([]dns.RR{rr(t, "old.dyn.test. 0 IN A 0.0.0.0")}) }, dns.RcodeYXRrset},
		{"value-dependent mismatch", func(m *dns.Msg) { m.Used([]dns.RR{rr(t, "old.dyn.test. 0 IN A 192.0.2.99")}) }, dns.RcodeNXRrset},
		{"out of zone", func(m *dns.Msg) { m.Insert([]dns.RR{rr(t, "x.example.org. 300 IN A 192.0.2.1")}) }, dns.RcodeNotZone},
		{"dnssec type refused", func(m *dns.Msg) { m.Insert([]dns.RR{rr(t, "dyn.test. 300 IN DNSKEY 257 3 13 AAAA")}) }, dns.RcodeRefused},
	}
	for _, c := range cases {
		if got := apply(t, a, c.build); got != uint32(c.rcode) {
			t.Errorf("%s: rcode %s, want %s", c.name, dns.RcodeToString[int(got)], dns.RcodeToString[c.rcode])
		}
	}
	if count(t, zs, z, "p1.dyn.test.", "A") != 1 || count(t, zs, z, "p2.dyn.test.", "A") != 0 {
		t.Fatal("prerequisite failure must not apply the update section")
	}
}

func TestUpdateSectionAndSerial(t *testing.T) {
	a, zs, z := setup(t)
	ctx := context.Background()
	before, _ := zs.GetZone(ctx, z.ID)
	if rc := apply(t, a, func(m *dns.Msg) {
		m.Insert([]dns.RR{rr(t, "host.dyn.test. 300 IN A 192.0.2.20"), rr(t, "host.dyn.test. 300 IN A 192.0.2.21")})
		m.Remove([]dns.RR{rr(t, "old.dyn.test. 300 IN A 192.0.2.1")})
	}); rc != dns.RcodeSuccess {
		t.Fatalf("rcode %d", rc)
	}
	after, _ := zs.GetZone(ctx, z.ID)
	if count(t, zs, z, "host.dyn.test.", "A") != 2 || count(t, zs, z, "old.dyn.test.", "A") != 0 || after.Serial != zone.SerialNext(before.Serial) {
		t.Fatalf("update not applied atomically: serial %d->%d", before.Serial, after.Serial)
	}
	apply(t, a, func(m *dns.Msg) { m.RemoveRRset([]dns.RR{rr(t, "dyn.test. 0 IN NS ns1.dyn.test.")}) })
	if count(t, zs, z, "dyn.test.", "NS") != 1 {
		t.Fatal("apex NS RRset must not be deletable")
	}
	apply(t, a, func(m *dns.Msg) { m.Insert([]dns.RR{rr(t, "host.dyn.test. 300 IN CNAME elsewhere.example.")}) })
	if count(t, zs, z, "host.dyn.test.", "CNAME") != 0 {
		t.Fatal("CNAME added to a name with other data (RFC 2136 §3.4.2.2 ignores it)")
	}
	apply(t, a, func(m *dns.Msg) { m.RemoveName([]dns.RR{rr(t, "host.dyn.test. 0 IN A 0.0.0.0")}) })
	if count(t, zs, z, "host.dyn.test.", "") != 0 {
		t.Fatal("delete all RRsets from a name")
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/dynupdate/ -count=1` — expect FAIL with "undefined: dynupdate.Applier".
- [ ] Implement `Apply`: `msg.Unpack`; opcode 5, one zone entry of type SOA class IN matching `req.Zone` else FORMERR; when `TSIGCheck` (true in production) re-verify with `dns.TsigVerify(req.Message, secretB64, "", false)` (failure → NOTAUTH) and require the key id to be in the zone's `update_tsig_key_ids` (REFUSED); zone kind secondary → REFUSED. Inside `zone.Service.Mutate` (`FOR UPDATE` lock, actor `auth.Actor{Type: "system", ID: "tsig:<key>@<engineID>", Name: "tsig:<key>"}`, audit action `dynamicUpdate`):
  - build the current RRset map `(lower owner, type) → []dns.RR`, including the synthetic apex SOA.
  - prerequisites (RFC 2136 §3.2) over `msg.Answer`: TTL ≠ 0 → FORMERR; owner outside zone → NOTZONE; class ANY: rdlength ≠ 0 → FORMERR; type ANY → name must exist else NXDOMAIN; other type → RRset must exist else NXRRSET. Class NONE: rdlength ≠ 0 → FORMERR; type ANY → name must not exist else YXDOMAIN; other type → RRset must not exist else YXRRSET. Class IN: collect into temporary RRsets and compare each for exact rdata-set equality else NXRRSET. Other classes → FORMERR.
  - prescan (§3.4.1) over `msg.Ns`: owner outside zone → NOTZONE; class IN with meta types (ANY, AXFR, IXFR, MAILA, MAILB) → FORMERR; class ANY requires TTL 0 and rdlength 0 → FORMERR; class NONE requires TTL 0 → FORMERR; types DNSKEY, RRSIG, NSEC, NSEC3, NSEC3PARAM, CDS, CDNSKEY or outside `zone.ManagedTypes ∪ {SOA}` → REFUSED.
  - apply (§3.4.2): class IN — SOA replaces zone SOA fields only when its serial is RFC 1982-greater; CNAME added only if the name has no other data, other types ignored at a CNAME name; identical rdata only updates TTL; the RRset TTL is set to the new TTL. Class ANY type ANY → delete every RRset at the name except apex SOA and NS; class ANY type T → delete the RRset (apex SOA/NS ignored). Class NONE → delete the matching RR (SOA ignored, the last apex NS ignored).
  - no net change → return NOERROR without rebuilding; otherwise `Rebuild` (the signed zone is re-signed by the Task 12 signer inside it), publish.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/dynupdate/ -count=1` — expect PASS.
- [ ] Write the failing `engine/src/authoritative/update_tests.rs` (declare `pub mod update; #[cfg(test)] mod update_tests;`):

```rust
use crate::tsig::{find_tsig, sign_request, KeyRing, TsigVerifier};
use crate::tsig_tests::test_ring;
use super::update::{handle_update_with_set, UpdateForwarder};
use super::{answer_tests::basic_set, set::AuthSet};
use crate::proto;
use hickory_proto::op::{Message, MessageType, OpCode, Query, ResponseCode};
use hickory_proto::rr::{Name, RecordType};
use std::future::Future;
use std::pin::Pin;
use std::sync::{Arc, Mutex};

struct Fwd {
    seen: Mutex<Vec<proto::UpdateRequest>>,
    reply: Option<u32>,
}
impl UpdateForwarder for Fwd {
    fn forward(&self, req: proto::UpdateRequest) -> Pin<Box<dyn Future<Output = Option<proto::UpdateResult>> + Send + '_>> {
        let id = req.request_id.clone();
        self.seen.lock().unwrap().push(req);
        let reply = self.reply;
        Box::pin(async move { reply.map(|rcode| proto::UpdateResult { request_id: id, rcode, detail: String::new() }) })
    }
}

const NOW: u64 = 1757750400;

fn update_msg() -> Vec<u8> {
    let mut m = Message::new(0x2222, MessageType::Query, OpCode::Update);
    m.add_query(Query::query(Name::from_ascii("example.test.").unwrap(), RecordType::SOA));
    m.to_vec().unwrap()
}

fn set_allowing(key: &str) -> AuthSet {
    basic_set().with_update_keys("example.test.", vec![key.to_string()])
}

#[tokio::test]
async fn unsigned_update_is_refused_without_forwarding() {
    let fwd = Arc::new(Fwd { seen: Mutex::new(vec![]), reply: Some(0) });
    let resp = handle_update_with_set(update_msg(), "127.0.0.1:5353".parse().unwrap(), Arc::new(set_allowing("xfr-key.")), Arc::new(test_ring()), fwd.clone(), NOW).await;
    assert_eq!(Message::from_vec(&resp).unwrap().metadata.response_code, ResponseCode::Refused);
    assert!(fwd.seen.lock().unwrap().is_empty());
}

#[tokio::test]
async fn signed_update_is_forwarded_and_response_is_signed() {
    let ring = test_ring();
    let key = ring.get(b"\x07xfr-key\x00").unwrap();
    let mut msg = update_msg();
    let mac = sign_request(&mut msg, &key, NOW);
    let fwd = Arc::new(Fwd { seen: Mutex::new(vec![]), reply: Some(0) });
    let resp = handle_update_with_set(msg, "127.0.0.1:5353".parse().unwrap(), Arc::new(set_allowing("xfr-key.")), Arc::new(ring), fwd.clone(), NOW).await;
    let parsed = Message::from_vec(&resp).unwrap();
    assert_eq!(parsed.metadata.response_code, ResponseCode::NoError);
    assert_eq!(parsed.metadata.op_code, OpCode::Update);
    assert!(find_tsig(&resp).unwrap().is_some(), "response carries TSIG");
    TsigVerifier::new((*key).clone(), mac.clone()).verify(&resp, NOW).expect("response TSIG verifies");
    let seen = fwd.seen.lock().unwrap();
    assert_eq!(seen.len(), 1);
    assert_eq!(seen[0].zone, "example.test.");
    assert_eq!(seen[0].tsig_key, "xfr-key.");
}

#[tokio::test]
async fn key_not_allowed_is_refused_and_timeout_is_servfail() {
    let ring = test_ring();
    let key = ring.get(b"\x0asha512-key\x00").unwrap();
    let mut msg = update_msg();
    sign_request(&mut msg, &key, NOW);
    let fwd = Arc::new(Fwd { seen: Mutex::new(vec![]), reply: Some(0) });
    let resp = handle_update_with_set(msg, "127.0.0.1:1".parse().unwrap(), Arc::new(set_allowing("xfr-key.")), Arc::new(test_ring()), fwd.clone(), NOW).await;
    assert_eq!(Message::from_vec(&resp).unwrap().metadata.response_code, ResponseCode::Refused);

    let key = ring.get(b"\x07xfr-key\x00").unwrap();
    let mut msg = update_msg();
    sign_request(&mut msg, &key, NOW);
    let silent = Arc::new(Fwd { seen: Mutex::new(vec![]), reply: None });
    let resp = handle_update_with_set(msg.clone(), "127.0.0.1:1".parse().unwrap(), Arc::new(set_allowing("xfr-key.")), Arc::new(test_ring()), silent.clone(), NOW).await;
    assert_eq!(Message::from_vec(&resp).unwrap().metadata.response_code, ResponseCode::ServFail, "no UpdateResult within 5 s");

    let resp = handle_update_with_set(msg, "127.0.0.1:1".parse().unwrap(), Arc::new(set_allowing("xfr-key.")), Arc::new(KeyRing::default()), silent, NOW).await;
    assert_eq!(Message::from_vec(&resp).unwrap().metadata.response_code, ResponseCode::NotAuth, "unknown key is BADKEY/NOTAUTH");
}
```

`AuthSet::with_update_keys` is a `#[cfg(test)]` helper that clones the set replacing a zone's `update_tsig_keys`; `handle_update_with_set` is the testable core of `handle_update` (takes the `AuthSet` instead of `Runtime`).

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib authoritative::update_tests` — expect FAIL with "unresolved import `super::update`".
- [ ] Implement `update.rs`: header opcode 5, ZOCOUNT 1, zone type SOA class IN else FORMERR; zone hosted with `origin == zname` else NOTAUTH; zone kind secondary → REFUSED; `verify_request`: `Unsigned` → REFUSED (unsigned response), failure → `error_response`; verified key name not in the zone's `update_tsig_keys` → REFUSED (signed); forward `UpdateRequest{request_id: 32 random hex, zone, client, message: raw, tsig_key}`; `None` → SERVFAIL; result → response with `rcode` from mgmt. The response copies ID, opcode 5, QR=1, the zone section, zero other counts, and is signed with the request MAC. Metric `nexora_auth_updates_total{result}` (`metrics.auth.updates`). Production forwarder is `AuthState`: `parking_lot::Mutex<FxHashMap<String, oneshot::Sender<proto::UpdateResult>>>`, `try_send` of `EngineMessage{update_request}` on the attached sender (detached or full → `None` immediately), `tokio::time::timeout(5 s)` removing the waiter on expiry; `control::session`'s `ServerMsg::UpdateResult(r)` arm calls `complete_update(r)`. In `dispatch::unparsed`, opcode 5 returns `AuthOutcome::Slow(SlowJob { kind: SlowKind::Update, .. })` on every transport; `run_slow` runs `handle_update(job.query.into(), job.client, rt, ctx.shared.auth.keyring.clone(), ctx.shared.auth.clone(), clock::unix_now())` and returns its single message (UDP: `server/udp.rs` sends it from the spawned task; streams: one frame).
- [ ] Mgmt delivery: `subscriber` gains `results chan *controlv1.ServerMessage` (capacity 64) and `Server.receive` gains `case *controlv1.EngineMessage_UpdateRequest:` which, when `s.OnUpdate` is set, runs `go func() { res := s.OnUpdate(ctx4s, sub.engineID, req); select { case sub.results <- &controlv1.ServerMessage{Msg: &controlv1.ServerMessage_UpdateResult{UpdateResult: res}}: default: } }()` with a 4 s context, so receiving continues while the update applies; the `Connect` send loop gains `case msg := <-sub.results:` (results never go through `out`, whose capacity-1 latest-wins slot would let a snapshot replace them). `main.go` sets `controlServer.OnUpdate` to `(&dynupdate.Applier{Zones: zones, TSIG: tsigKeys, Now: time.Now, TSIGCheck: true}).Apply`.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib authoritative::update_tests` — expect PASS.
- [ ] Write the failing `e2e/secondary_update_test.go`:

```go
package e2e

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
)

func TestSecondaryAndDynamicUpdate(t *testing.T) {
	e := startAuthEnv(t, []string{"NEXORA_KEK_FILE=" + harness.WriteKEK(t)}, "upd-1")
	api, eng := e.api, e.engines[0]

	t.Run("secondary zone follows NOTIFY from external primary", func(t *testing.T) {
		updSecret := base64.StdEncoding.EncodeToString([]byte("fixture-update-key-for-named"))
		named := e.env.StartNamedConfig(harness.NamedConfig{
			Keys: []harness.NamedKey{{Name: "upd-key.", Algorithm: "hmac-sha256", SecretB64: updSecret}},
			Zones: []harness.NamedZone{{Name: "upstream.test.", Type: "primary", AllowUpdateKey: "upd-key.", AlsoNotify: eng.DNS,
				Text: "$TTL 60\n@ SOA ns.upstream.test. h.upstream.test. 1 3600 600 86400 60\n@ NS ns.upstream.test.\nns A 192.0.2.53\na A 192.0.2.1\n"}},
		})
		var z zoneResp
		api.Must(http.MethodPost, "/zones", map[string]any{"name": "upstream.test.", "kind": "secondary", "primaries": []map[string]any{{"address": named.Addr}}}, &z, http.StatusCreated)
		harness.WaitDNSAnswer(t, eng.DNS, "a.upstream.test.", dns.TypeA, 15*time.Second, func(m *dns.Msg) bool { return m.Authoritative && len(m.Answer) == 1 })

		m := new(dns.Msg)
		m.SetUpdate("upstream.test.")
		m.Insert([]dns.RR{mustRR(t, "b.upstream.test. 60 IN A 192.0.2.77")})
		m.SetTsig("upd-key.", dns.HmacSHA256, 300, time.Now().Unix())
		c := &dns.Client{TsigSecret: map[string]string{"upd-key.": updSecret}}
		if r, _, err := c.Exchange(m, named.Addr); err != nil || r.Rcode != dns.RcodeSuccess {
			t.Fatalf("update external primary: %v %v", r, err)
		}
		// the SOA refresh is 3600 s: only the forwarded NOTIFY makes the change appear within 10 s
		harness.WaitDNSAnswer(t, eng.DNS, "b.upstream.test.", dns.TypeA, 10*time.Second, func(m *dns.Msg) bool { return len(m.Answer) == 1 })
		var got struct {
			SecondaryStatus struct {
				LastTrigger string `json:"last_trigger"`
			} `json:"secondary_status"`
		}
		api.Must(http.MethodGet, "/zones/"+z.ID, nil, &got, http.StatusOK)
		if got.SecondaryStatus.LastTrigger != "notify" {
			t.Fatalf("refresh was not triggered by NOTIFY: %q", got.SecondaryStatus.LastTrigger)
		}
		if v := eng.Metric(t, "nexora_auth_notify_received_total", map[string]string{"result": "forwarded"}); v < 1 {
			t.Fatalf("engine did not forward NOTIFY")
		}
	})

	t.Run("TSIG-signed update applies, unsigned is refused", func(t *testing.T) {
		var key tsigKeyResp
		api.Must(http.MethodPost, "/tsig-keys", map[string]any{"name": "ddns-key.", "algorithm": "hmac-sha256"}, &key, http.StatusCreated)
		createPrimaryZone(t, api, "dyn.test.", map[string]any{"update": map[string]any{"tsig_key_ids": []string{key.ID}}})
		harness.WaitDNSAnswer(t, eng.DNS, "dyn.test.", dns.TypeSOA, 5*time.Second, func(m *dns.Msg) bool { return m.Authoritative && len(m.Answer) == 1 })
		if eng.Metric(t, "nexora_control_connected", nil) != 1 {
			t.Fatal("engine control stream is not connected")
		}

		signed := new(dns.Msg)
		signed.SetUpdate("dyn.test.")
		signed.Insert([]dns.RR{mustRR(t, "host1.dyn.test. 300 IN A 192.0.2.10")})
		signed.SetTsig("ddns-key.", dns.HmacSHA256, 300, time.Now().Unix())
		c := &dns.Client{Net: "udp", TsigSecret: map[string]string{"ddns-key.": key.Secret}, Timeout: 8 * time.Second}
		harness.Eventually(t, 10*time.Second, func() error { // KeyMaterial with the new key may still be in flight
			r, _, err := c.Exchange(signed.Copy(), eng.DNS)
			if err != nil {
				return err
			}
			if r.Rcode != dns.RcodeSuccess {
				return fmt.Errorf("signed update: rcode %s", dns.RcodeToString[r.Rcode])
			}
			return nil
		})
		harness.WaitDNSAnswer(t, eng.DNS, "host1.dyn.test.", dns.TypeA, 5*time.Second, func(m *dns.Msg) bool { return len(m.Answer) == 1 })

		unsigned := new(dns.Msg)
		unsigned.SetUpdate("dyn.test.")
		unsigned.Insert([]dns.RR{mustRR(t, "host2.dyn.test. 300 IN A 192.0.2.11")})
		r, _, err := (&dns.Client{Timeout: 5 * time.Second}).Exchange(unsigned, eng.DNS)
		if err != nil || r.Rcode != dns.RcodeRefused {
			t.Fatalf("unsigned update: rcode=%v err=%v, want REFUSED", r, err)
		}
		waitLatestApplied(t, api, "upd-1")
		if nx := harness.MustQuery(t, eng.DNS, "host2.dyn.test.", dns.TypeA, harness.QueryOpts{TCP: true}); nx.Rcode != dns.RcodeNameError {
			t.Fatalf("unsigned update was applied: %v", nx)
		}
	})
}

func mustRR(t *testing.T, s string) dns.RR {
	t.Helper()
	r, err := dns.NewRR(s)
	if err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return r
}
```

- As built (management plane; recorded after implementation):
  - `Applier.Apply` also rejects messages over 65535 bytes (FORMERR), requires the TSIG owner to equal `req.TsigKey` and the key's algorithm to match (NOTAUTH), and re-checks the key against the locked zone row inside the transaction. Invalid SOA timers in an update are REFUSED; an update that would break `zone.CheckSet` (DS/DNAME rules) is REFUSED and rolled back. Inserted owners are stored lowercase.
  - Per engine the control server bounds updates: 16 applying at once (further ones REFUSED "too many concurrent updates"), a token bucket of 50/s with burst 100 (REFUSED "update rate limit exceeded"), request ids ≤ 64 bytes; without `OnUpdate` the reply is NOTIMP. Test `TestNotifyAndUpdateAreForwardedWithoutBlockingTheStream` (`mgmt/internal/control/forward_test.go`, fixture hook `setupServers`).
  - Extra test `TestTSIGAndUpdatePolicy` (unsigned REFUSED, key outside the zone policy REFUSED, wrong secret NOTAUTH, key differing from the engine's NOTAUTH, allowed key applied). The plan's case struct used `rcode uint32` (does not compile as a map index); it is `int`.
- As built (engine; recorded after implementation):
  - `handle_update` returns `(Vec<u8>, usize)` (the response and its `AuthCounters::updates` slot, counted in `run_slow`); `handle_update_with_set` takes `impl Deref<Target = AuthSet>` and returns only the response. Order: parse (FORMERR) → TSIG verify (failure → `error_response`) → zone hosted (NOTAUTH) → unsigned (REFUSED) → secondary or key not allowed (REFUSED); every response after a verified TSIG is signed. Result rcodes above 15 become SERVFAIL.
  - `nexora_auth_updates_total{result}` labels: `applied`, `rejected` (mgmt non-NOERROR), `refused` (engine refused before forwarding), `failed` (no result).
  - `AuthState` caps pending updates at 1024 (further ones SERVFAIL without forwarding); `detach` clears pending waiters so they fail at once. Extra test `auth_state_forwards_on_the_attached_stream_and_completes_by_request_id`.
- [ ] Run `scripts/dev-exec.sh make e2e-build` then `scripts/dev-exec.sh go test ./e2e/ -run TestSecondaryAndDynamicUpdate -count=1` — expect PASS.
- [ ] Commit: `git add engine mgmt e2e && git commit -m "feat(m4): TSIG-authenticated RFC 2136 updates applied transactionally by the management plane"`.

## Task 12: Management-plane online DNSSEC signer

Files:

- `mgmt/migrations/00402_dnssec.sql` — `zone_dnssec`, `dnssec_keys`, `zone_signatures` (00401 was taken by Task 10's `zone_refresh_requests`)
- `mgmt/internal/dnssec/sign.go` — pure signer: DNSKEY/CDS/CDNSKEY, NSEC or NSEC3 chain, RRSIG reuse and refresh
- `mgmt/internal/dnssec/canonical.go` — RRset digest, cut/occlusion analysis
- `mgmt/internal/dnssec/sign_test.go` — validation of output, reuse
- `mgmt/internal/dnssec/validate_test.go` — `TestSignedZonesValidateWithBIND`: `dnssec-verify` over the signed zone and `delv` (KSK trust anchor) against `named` serving it, ECDSA NSEC/NSEC3 and RSA NSEC3
- `mgmt/internal/dnssec/store.go` — `zone.Signer` implementation backed by Postgres and `secrets.Box` (new package `dnssec`, distinct from M3's `dnssecconf` validation helpers)
- `mgmt/internal/dnssec/enable.go` — `Enable(ctx, tx, …)`: settings row, first KSK + ZSK
- `mgmt/internal/dnssec/maintainer.go` — signature refresh loop under `pg_try_advisory_lock(hashtext('dnssec:'||zone_id))` (Task 14 adds rollover transitions)
- `mgmt/internal/dnssec/store_test.go`
- `mgmt/internal/zone/service.go`, `mgmt/internal/zone/model.go` — load `dnssec_enabled` from `zone_dnssec` (modify)
- `mgmt/cmd/nexora-mgmt/main.go` — `Signer: &dnssec.Store{Box: box}` on the shared `zone.Service`, start the `Maintainer` (modify)

As built: the signed goldens `testdata/nzf/signed-nsec-full.nzf` / `signed-nsec3-full.nzf` were committed with Task 13 from `testdata/nzf/gen-signed` (fixed Ed25519 keys plus `signed-anchor.key`, which the engine's delv test trusts); the signer does not overwrite them (random keys would break that test), so `writeGolden` and `-update` are dropped and the signer's output is validated with BIND's tools instead. `SignRRset` was dropped: `ResignSOA` uses the same cache-aware signing path as `Sign`.

Interfaces:

```go
package dnssec
const (Validity = 14 * 24 * time.Hour; RefreshBefore = 7 * 24 * time.Hour; InceptionSkew = time.Hour)
type Key struct{ ID string; Role string /* ksk|zsk */; Algorithm uint8; DNSKEY *dns.DNSKEY; Signer crypto.Signer; Signs bool; InCDS bool }
type SigKey struct{ Owner string; Type uint16; KeyTag uint16 }
type CachedSig struct{ Digest [32]byte; RRSIG *dns.RRSIG }
type Input struct{ Origin string; Records []dns.RR; Keys []Key; NSEC3 bool; Cache map[SigKey]CachedSig; Now time.Time }
type Output struct{ Served []dns.RR; Cache map[SigKey]CachedSig; NextRefresh time.Time; Reused, Created int }
func Sign(in Input) (*Output, error)
type Settings struct{ Algorithm uint8; NSECMode string; KeyBackend secrets.Backend; PropagationDelay, ParentDSTTL time.Duration; ZSKLifetimeDays int }
type Store struct{ Box *secrets.Box }
func (s *Store) Sign(ctx context.Context, tx pgx.Tx, z *zone.Zone, rrs []dns.RR, now time.Time) ([]dns.RR, error)
func (s *Store) ResignSOA(ctx context.Context, tx pgx.Tx, z *zone.Zone, served []dns.RR, now time.Time) ([]dns.RR, error)
func Enable(ctx context.Context, tx pgx.Tx, box *secrets.Box, zoneID uuid.UUID, st Settings) error
func Disable(ctx context.Context, tx pgx.Tx, box *secrets.Box, zoneID uuid.UUID) error
var ErrNotPrimary error
type Maintainer struct{ Store *store.Store; Zones *zone.Service; Tick time.Duration }
func (m *Maintainer) Run(ctx context.Context) error
```

- [ ] Write `mgmt/migrations/00401_dnssec.sql`:

```sql
-- +goose Up
CREATE TABLE zone_dnssec (
    zone_id                   uuid PRIMARY KEY REFERENCES zones(id) ON DELETE CASCADE,
    enabled                   boolean NOT NULL DEFAULT false,
    algorithm                 smallint NOT NULL DEFAULT 13 CHECK (algorithm IN (8, 13)),
    nsec_mode                 text NOT NULL DEFAULT 'nsec3' CHECK (nsec_mode IN ('nsec', 'nsec3')),
    key_backend               text NOT NULL CHECK (key_backend IN ('kek', 'pkcs11')),
    propagation_delay_seconds integer NOT NULL DEFAULT 3600 CHECK (propagation_delay_seconds BETWEEN 1 AND 604800),
    parent_ds_ttl_seconds     integer NOT NULL DEFAULT 86400 CHECK (parent_ds_ttl_seconds BETWEEN 1 AND 604800),
    zsk_lifetime_days         integer NOT NULL DEFAULT 90 CHECK (zsk_lifetime_days BETWEEN 0 AND 3650),
    next_maintenance_at       timestamptz
);

CREATE INDEX zone_dnssec_due ON zone_dnssec (next_maintenance_at) WHERE enabled;

CREATE TABLE dnssec_keys (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    zone_id          uuid NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
    role             text NOT NULL CHECK (role IN ('ksk', 'zsk')),
    algorithm        smallint NOT NULL CHECK (algorithm IN (8, 13)),
    key_tag          integer NOT NULL,
    public_key       text NOT NULL,
    backend          text NOT NULL CHECK (backend IN ('kek', 'pkcs11')),
    key_ref          bytea NOT NULL UNIQUE,
    private_envelope bytea,
    state            text NOT NULL CHECK (state IN ('published', 'active', 'retired', 'removed')),
    ds_state         text NOT NULL DEFAULT 'none' CHECK (ds_state IN ('none', 'pending', 'seen')),
    published_at     timestamptz NOT NULL DEFAULT now(),
    activated_at     timestamptz,
    retired_at       timestamptz,
    removed_at       timestamptz,
    ds_seen_at       timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CHECK (state = 'removed' OR (backend = 'kek') = (private_envelope IS NOT NULL))
);
CREATE INDEX dnssec_keys_zone ON dnssec_keys (zone_id, state);

CREATE TABLE zone_signatures (
    zone_id      uuid NOT NULL REFERENCES zones(id) ON DELETE CASCADE,
    owner        text NOT NULL,
    type_covered integer NOT NULL,
    key_tag      integer NOT NULL,
    rrset_digest bytea NOT NULL,
    expiration   timestamptz NOT NULL,
    rrsig        bytea NOT NULL,
    PRIMARY KEY (zone_id, owner, type_covered, key_tag)
);

-- +goose Down
DROP TABLE zone_signatures;
DROP TABLE dnssec_keys;
DROP TABLE zone_dnssec;
```

- [ ] Write the failing `mgmt/internal/dnssec/sign_test.go`:

```go
package dnssec

import (
	"crypto"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

var t0 = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

func testKeys(t *testing.T) []Key {
	t.Helper()
	var keys []Key
	for _, role := range []string{"ksk", "zsk"} {
		flags := uint16(256)
		if role == "ksk" {
			flags = 257
		}
		k := &dns.DNSKEY{Hdr: dns.RR_Header{Name: "example.test.", Rrtype: dns.TypeDNSKEY, Class: dns.ClassINET, Ttl: 3600}, Flags: flags, Protocol: 3, Algorithm: dns.ECDSAP256SHA256}
		priv, err := k.Generate(256)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, Key{ID: role, Role: role, Algorithm: dns.ECDSAP256SHA256, DNSKEY: k, Signer: priv.(crypto.Signer), Signs: true, InCDS: role == "ksk"})
	}
	return keys
}

// basicRecords mirrors nzf.basicZone(2026091301, "before").
func basicRecords(t *testing.T) []dns.RR {
	t.Helper()
	lines := []string{
		"example.test. 3600 IN SOA ns1.example.test. hostmaster.example.test. 2026091301 7200 3600 1209600 300",
		"example.test. 3600 IN NS ns1.example.test.", "example.test. 3600 IN NS ns2.example.test.",
		"example.test. 3600 IN MX 10 mail.example.test.", "ns1.example.test. 3600 IN A 192.0.2.1",
		"ns2.example.test. 3600 IN AAAA 2001:db8::2", "mail.example.test. 3600 IN A 192.0.2.25",
		"www.example.test. 300 IN A 192.0.2.10", "www.example.test. 300 IN A 192.0.2.11",
		"alias.example.test. 300 IN CNAME www.example.test.", "*.wild.example.test. 300 IN TXT \"wildcard\"",
		"a.b.c.example.test. 300 IN A 192.0.2.20", "sub.example.test. 3600 IN NS ns.sub.example.test.",
		"sub.example.test. 3600 IN DS 60485 13 2 D4B7D520E7BB5F0F67674A0CCEB1E3E0614B93C4F9E99B8383F6A1E4469DA50A",
		"ns.sub.example.test. 3600 IN A 192.0.2.53", "insecure.example.test. 3600 IN NS ns.insecure.example.test.",
		"ns.insecure.example.test. 3600 IN A 192.0.2.54", "dn.example.test. 300 IN DNAME example.net.",
	}
	var out []dns.RR
	for _, l := range lines {
		rr, err := dns.NewRR(l)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, rr)
	}
	return out
}

func rrsets(served []dns.RR) map[string][]dns.RR {
	m := map[string][]dns.RR{}
	for _, rr := range served {
		if rr.Header().Rrtype == dns.TypeRRSIG {
			continue
		}
		k := strings.ToLower(rr.Header().Name) + "/" + dns.TypeToString[rr.Header().Rrtype]
		m[k] = append(m[k], rr)
	}
	return m
}

func sigsFor(served []dns.RR, owner string, typ uint16) []*dns.RRSIG {
	var out []*dns.RRSIG
	for _, rr := range served {
		if s, ok := rr.(*dns.RRSIG); ok && strings.EqualFold(s.Hdr.Name, owner) && s.TypeCovered == typ {
			out = append(out, s)
		}
	}
	return out
}

func verifyAll(t *testing.T, out *Output, keys []Key, now time.Time) {
	t.Helper()
	byTag := map[uint16]*dns.DNSKEY{}
	for _, k := range keys {
		byTag[k.DNSKEY.KeyTag()] = k.DNSKEY
	}
	for name, set := range rrsets(out.Served) {
		owner, typ := set[0].Header().Name, set[0].Header().Rrtype
		occluded := strings.HasSuffix(owner, ".sub.example.test.") || strings.HasSuffix(owner, ".insecure.example.test.")
		delegation := (owner == "sub.example.test." || owner == "insecure.example.test.") && typ == dns.TypeNS
		sigs := sigsFor(out.Served, owner, typ)
		if occluded || delegation {
			if len(sigs) != 0 {
				t.Errorf("%s must not be signed", name)
			}
			continue
		}
		if len(sigs) == 0 {
			t.Errorf("%s is unsigned", name)
			continue
		}
		for _, s := range sigs {
			if err := s.Verify(byTag[s.KeyTag], set); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			if !s.ValidityPeriod(now) {
				t.Errorf("%s: signature not valid at %v", name, now)
			}
			wantKSK := typ == dns.TypeDNSKEY || typ == dns.TypeCDS || typ == dns.TypeCDNSKEY
			if (byTag[s.KeyTag].Flags == 257) != wantKSK {
				t.Errorf("%s signed by the wrong key role", name)
			}
		}
	}
}

func TestSignNSECChainAndSignatures(t *testing.T) {
	keys := testKeys(t)
	out, err := Sign(Input{Origin: "example.test.", Records: basicRecords(t), Keys: keys, NSEC3: false, Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	verifyAll(t, out, keys, t0)
	sets := rrsets(out.Served)
	if len(sets["example.test./DNSKEY"]) != 2 || len(sets["example.test./CDS"]) != 1 || len(sets["example.test./CDNSKEY"]) != 1 {
		t.Fatalf("apex key RRsets: %v", sets["example.test./DNSKEY"])
	}
	// NSEC chain: every authoritative owner (no ENTs, no glue) once, closing at the apex
	var owners []string
	for _, rr := range out.Served {
		if n, ok := rr.(*dns.NSEC); ok {
			owners = append(owners, n.Hdr.Name)
			if n.Hdr.Ttl != 300 {
				t.Errorf("NSEC TTL %d, want min(SOA TTL, MINIMUM) = 300", n.Hdr.Ttl)
			}
		}
	}
	if len(owners) != 11 {
		t.Fatalf("NSEC owners %d: %v", len(owners), owners)
	}
	for _, n := range out.Served {
		if nsec, ok := n.(*dns.NSEC); ok && nsec.Hdr.Name == "sub.example.test." {
			if fmt.Sprint(nsec.TypeBitMap) != fmt.Sprint([]uint16{dns.TypeNS, dns.TypeDS, dns.TypeRRSIG, dns.TypeNSEC}) {
				t.Fatalf("delegation NSEC bitmap %v", nsec.TypeBitMap)
			}
		}
		if nsec, ok := n.(*dns.NSEC); ok && nsec.Hdr.Name == "insecure.example.test." {
			if fmt.Sprint(nsec.TypeBitMap) != fmt.Sprint([]uint16{dns.TypeNS, dns.TypeRRSIG, dns.TypeNSEC}) {
				t.Fatalf("insecure delegation NSEC bitmap %v", nsec.TypeBitMap)
			}
		}
	}
}

func TestSignNSEC3IncludesEmptyNonTerminalsWithZeroIterations(t *testing.T) {
	keys := testKeys(t)
	out, err := Sign(Input{Origin: "example.test.", Records: basicRecords(t), Keys: keys, NSEC3: true, Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	verifyAll(t, out, keys, t0)
	hashes := map[string]bool{}
	for _, rr := range out.Served {
		switch v := rr.(type) {
		case *dns.NSEC3:
			if v.Iterations != 0 || v.SaltLength != 0 || v.Flags != 0 || v.Hash != dns.SHA1 {
				t.Fatalf("NSEC3 parameters: %v", v)
			}
			hashes[strings.ToUpper(strings.SplitN(v.Hdr.Name, ".", 2)[0])] = true
		case *dns.NSEC:
			t.Fatal("NSEC present in NSEC3 zone")
		case *dns.NSEC3PARAM:
			if v.Hdr.Name != "example.test." || v.Iterations != 0 || v.Salt != "" {
				t.Fatalf("NSEC3PARAM %v", v)
			}
		}
	}
	for _, name := range []string{"example.test.", "b.c.example.test.", "c.example.test.", "wild.example.test.", "sub.example.test."} {
		if !hashes[dns.HashName(name, dns.SHA1, 0, "")] {
			t.Errorf("no NSEC3 for %s", name)
		}
	}
	if hashes[dns.HashName("ns.sub.example.test.", dns.SHA1, 0, "")] {
		t.Error("occluded glue has an NSEC3")
	}
}

func TestSignatureReuseAndRefresh(t *testing.T) {
	keys := testKeys(t)
	first, err := Sign(Input{Origin: "example.test.", Records: basicRecords(t), Keys: keys, NSEC3: true, Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	day1, _ := Sign(Input{Origin: "example.test.", Records: basicRecords(t), Keys: keys, NSEC3: true, Cache: first.Cache, Now: t0.Add(24 * time.Hour)})
	if day1.Created != 0 || day1.Reused != first.Created {
		t.Fatalf("day 1: created=%d reused=%d (first created %d)", day1.Created, day1.Reused, first.Created)
	}
	if !day1.NextRefresh.Before(t0.Add(Validity-RefreshBefore)) || day1.NextRefresh.Before(t0.Add(Validity-RefreshBefore-time.Hour)) {
		t.Fatalf("next refresh %v", day1.NextRefresh)
	}
	day8, _ := Sign(Input{Origin: "example.test.", Records: basicRecords(t), Keys: keys, NSEC3: true, Cache: first.Cache, Now: t0.Add(8 * 24 * time.Hour)})
	if day8.Reused != 0 {
		t.Fatalf("day 8: %d signatures reused past the 7-day refresh point", day8.Reused)
	}
	changed := append(basicRecords(t), mustRR(t, "new.example.test. 300 IN A 192.0.2.99"))
	edit, _ := Sign(Input{Origin: "example.test.", Records: changed, Keys: keys, NSEC3: true, Cache: first.Cache, Now: t0.Add(time.Hour)})
	if edit.Created == 0 || edit.Created > 6 {
		t.Fatalf("one added record re-signed %d RRsets (want its A, its NSEC3, the neighbouring NSEC3 and none else)", edit.Created)
	}
}

func mustRR(t *testing.T, s string) dns.RR {
	rr, err := dns.NewRR(s)
	if err != nil {
		t.Fatal(err)
	}
	return rr
}
```

The 11 NSEC owners are: apex, alias, dn, insecure, mail, ns1, ns2, sub, www, `*.wild`, `a.b.c` (no empty non-terminals, no occluded glue).

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/dnssec/ -count=1` — expect FAIL with "undefined: Sign".
- [ ] Implement `canonical.go` and `sign.go`:
  - group input by `(lower owner, type)`; drop any input DNSKEY/RRSIG/NSEC/NSEC3/NSEC3PARAM/CDS/CDNSKEY (they are generated).
  - cuts = non-apex owners with NS; occluded = owners strictly below a cut; authoritative owners = all other owners (delegation owners included).
  - apex additions: DNSKEY RRset of every key passed in (TTL = SOA TTL); for keys with `InCDS`: CDS (`dnskey.ToDS(dns.SHA256)` retyped as CDS) and CDNSKEY (TTL = SOA TTL).
  - negative TTL = `min(SOA TTL, SOA MINIMUM)`.
  - NSEC mode: sort authoritative owners by `nzf.CanonicalKey`; each gets `NSEC{NextDomain: next owner (wrap to apex), TypeBitMap: sorted types present + RRSIG + NSEC}`; at a delegation the bitmap is NS, DS (when present), RRSIG, NSEC — RRSIG is listed because the NSEC at the delegation is itself signed (RFC 4035 §2.3).
  - NSEC3 mode: set = authoritative owners ∪ empty non-terminals between them and the apex (not occluded); owner label = `strings.ToLower(dns.HashName(name, dns.SHA1, 0, ""))`; sorted by hash; `NSEC3{Hash: 1, Flags: 0, Iterations: 0, SaltLength: 0, Salt: "", HashLength: 20, NextDomain: next hash (uppercase base32hex as miekg expects), TypeBitMap: types (+RRSIG when the owner has a signed RRset)}`; ENTs have an empty bitmap; apex `NSEC3PARAM{Hash: 1, Flags: 0, Iterations: 0, Salt: ""}` with negative TTL.
  - signing set: every RRset at authoritative owners except NS at delegations; at delegations DS and NSEC are signed; occluded names are never signed; DNSKEY/CDS/CDNSKEY with `Signs` KSKs, all others with `Signs` ZSKs.
  - reuse: digest = SHA-256 over the RRset's records packed uncompressed with lowercase owner, sorted bytewise; reuse `Cache[{owner, type, tag}]` when digests match, the algorithm matches, inception is not in the future and `Expiration - now > RefreshBefore + 1h` (the extra hour is the jitter range, so one refresh pass renews a whole batch instead of publishing a version per jittered expiration); otherwise sign (the RRSIG header TTL is the RRset TTL):

```go
func newSig(origin string, set []dns.RR, k Key, now time.Time) (*dns.RRSIG, error) {
	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(set[0].Header().Name)))
	binary.Write(h, binary.BigEndian, set[0].Header().Rrtype)
	jitter := time.Duration(h.Sum32()%3600) * time.Second
	sig := &dns.RRSIG{
		Algorithm:  k.Algorithm,
		KeyTag:     k.DNSKEY.KeyTag(),
		SignerName: origin,
		Inception:  uint32(now.Add(-InceptionSkew).Unix()),
		Expiration: uint32(now.Add(Validity - jitter).Unix()),
	}
	if err := sig.Sign(k.Signer, set); err != nil {
		return nil, fmt.Errorf("sign %s/%s with key %d: %w", set[0].Header().Name, dns.TypeToString[set[0].Header().Rrtype], sig.KeyTag, err)
	}
	return sig, nil
}
```

- `NextRefresh` = earliest `Expiration - RefreshBefore` over all signatures in the output; `Served` = input records + generated records + RRSIGs.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/dnssec/ -count=1` — expect PASS, including `TestSignedZonesValidateWithBIND`.
- [ ] Write the failing `mgmt/internal/dnssec/store_test.go`:

```go
package dnssec_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/dnssec"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

func TestEnableSignsAndEditsAreResigned(t *testing.T) {
	ctx := context.Background()
	actor := auth.Actor{Type: "user", ID: "t", Name: "t"}
	kek := make([]byte, 32)
	if _, err := rand.Read(kek); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(kek)), 0o600); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.Open(secrets.Config{KEKFile: p})
	if err != nil {
		t.Fatal(err)
	}
	st := storetest.New(t)
	zs := &zone.Service{Store: st, Signer: &dnssec.Store{Box: box}, Now: time.Now}
	z, err := zs.CreateZone(ctx, actor, zone.CreateZoneInput{Name: "signed.test.", Kind: "primary", DefaultTTL: 300,
		SOA: zone.SOA{MName: "ns1.signed.test.", RName: "h.signed.test."}, Nameservers: []string{"ns1.signed.test."}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = zs.Mutate(ctx, z.ID, func(tx pgx.Tx, z *zone.Zone) (string, any, any, zone.RebuildOptions, error) {
		err := dnssec.Enable(ctx, tx, box, z.ID, dnssec.Settings{Algorithm: 13, NSECMode: "nsec3", KeyBackend: secrets.BackendKEK, PropagationDelay: time.Hour, ParentDSTTL: 24 * time.Hour, ZSKLifetimeDays: 90})
		z.DNSSECEnabled = true
		return "updateZoneDnssec", nil, nil, zone.RebuildOptions{Force: true}, err
	}, actor)
	if err != nil {
		t.Fatal(err)
	}
	var keys int
	st.Pool.QueryRow(ctx, `SELECT count(*) FROM dnssec_keys WHERE zone_id=$1 AND state='active' AND private_envelope IS NOT NULL`, z.ID).Scan(&keys)
	if keys != 2 {
		t.Fatalf("active enveloped keys: %d", keys)
	}
	if _, err := zs.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "www.signed.test.", Type: "A", TTL: 300, Data: "192.0.2.1"}); err != nil {
		t.Fatal(err)
	}
	tx, _ := st.Pool.Begin(ctx)
	defer tx.Rollback(ctx)
	fresh, _ := zs.GetZone(ctx, z.ID)
	served, err := zone.LoadServed(ctx, tx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	var dnskeys []dns.RR
	var wwwA []dns.RR
	var wwwSig, soaSig *dns.RRSIG
	var soa *dns.SOA
	for _, rr := range served {
		switch v := rr.(type) {
		case *dns.DNSKEY:
			dnskeys = append(dnskeys, v)
		case *dns.A:
			if v.Hdr.Name == "www.signed.test." {
				wwwA = append(wwwA, v)
			}
		case *dns.SOA:
			soa = v
		case *dns.RRSIG:
			if v.Hdr.Name == "www.signed.test." && v.TypeCovered == dns.TypeA {
				wwwSig = v
			}
			if v.TypeCovered == dns.TypeSOA {
				soaSig = v
			}
		}
	}
	if wwwSig == nil || soaSig == nil || soa.Serial != fresh.Serial {
		t.Fatalf("served set not re-signed: wwwSig=%v soaSig=%v", wwwSig, soaSig)
	}
	for _, sig := range []*dns.RRSIG{wwwSig} {
		ok := false
		for _, k := range dnskeys {
			if k.(*dns.DNSKEY).KeyTag() == sig.KeyTag && sig.Verify(k.(*dns.DNSKEY), wwwA) == nil {
				ok = true
			}
		}
		if !ok {
			t.Fatal("www A RRSIG does not verify")
		}
	}
	if err := soaSig.Verify(findKey(dnskeys, soaSig.KeyTag), []dns.RR{soa}); err != nil {
		t.Fatalf("SOA signature is stale after the serial bump: %v", err)
	}
}

func findKey(keys []dns.RR, tag uint16) *dns.DNSKEY {
	for _, k := range keys {
		if d := k.(*dns.DNSKEY); d.KeyTag() == tag {
			return d
		}
	}
	return nil
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/dnssec/ -run TestEnableSigns -count=1` — expect FAIL with "undefined: dnssec.Store".
- [ ] Implement `enable.go`: refuse with `secrets.ErrUnconfigured` when the box is unconfigured, `secrets.ErrBackendUnavailable` when the requested backend is not; upsert `zone_dnssec` (`enabled=true`); when the zone has no non-removed keys, `GenerateSigningKey` twice (KSK flags 257, ZSK 256), compute key tags from the DNSKEY, insert both `state='active', activated_at=now()`, KSK `ds_state='pending'`. `Disable` sets `enabled=false`, marks keys `removed` (destroying HSM objects, nulling envelopes) and deletes `zone_signatures`.
- [ ] Implement `store.go` `Store.Sign`: load settings and keys (`state IN ('published','active','retired')`), build `dnssec.Key`s via `Box.Signer` (releasing all at the end of the call), `Signs` = `state='active'`, `InCDS` = `role='ksk' AND state='active' AND ds_state='pending'`; load cache from `zone_signatures`; call `Sign`; replace changed rows in `zone_signatures` (delete rows whose `(owner,type,tag)` is absent from the output); `UPDATE zone_dnssec SET next_maintenance_at = LEAST(next_refresh, rollover next)` (rollover time filled by Task 14). `ResignSOA` re-signs the SOA RRset with active ZSKs through the same cache-aware path (reusing the cached signature when the SOA is unchanged), updates its cache rows and lowers `next_maintenance_at` when needed. Only active keys are unsealed (`Box.Signer`); PKCS#11 keys sign inside the token. `Enable` refuses secondary zones (`ErrNotPrimary`) and avoids key tags the zone already uses. `Maintainer` (5 s): due zones (`enabled AND next_maintenance_at <= now()`), per zone `pg_try_advisory_lock(hashtext('dnssec:'||zone_id))`, re-check due-ness, `zone.Service.Mutate` (actor `system:dnssec`, action `dnssecMaintenance`) whose `Rebuild` refreshes the due signatures. Extra tests: `TestDynamicUpdateToSignedZoneIsResignedIncrementally`, `TestPKCS11KeysSignInsideTheToken` (SoftHSM, alg 13 and 8, non-extractable keys, `Disable` destroys them), `TestEnableRefusesWithoutKeyStorageOrOnSecondaries`, `TestMaintainerRefreshesSignaturesUnderAdvisoryLock`. `zone.Service` loads `DNSSECEnabled` with `LEFT JOIN zone_dnssec d ON d.zone_id = z.id` (`COALESCE(d.enabled, false)`).
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/dnssec/ ./mgmt/internal/zone/ ./mgmt/internal/dynupdate/ -count=1` — expect PASS.
- [ ] Commit: `git add mgmt testdata/nzf && git commit -m "feat(mgmt): online DNSSEC signing with NSEC/NSEC3 and signature reuse"`.

## Task 13: Engine DNSSEC answers from pre-signed zone data

Files:

- `engine/src/authoritative/nsec3.rs` — RFC 5155 hash (SHA-1 from `ring`), base32hex
- `engine/src/authoritative/dnssec.rs` — proof selection: RRSIGs, NSEC and NSEC3 denial, DS/no-DS at referrals
- `engine/src/authoritative/answer.rs` — call the proofs when DO=1 and the zone is signed (modify)
- `engine/src/authoritative/dnssec_tests.rs`
- `engine/src/authoritative/zone.rs` — NSEC3 owner labels decode with `nsec3::decode_b32hex` (modify)
- `testdata/nzf/gen-signed/main.go` — test-only generator (miekg/dns, Ed25519 KSK+ZSK from fixture seeds, deterministic) of `testdata/nzf/signed-nsec-full.nzf`, `signed-nsec3-full.nzf` and the trust anchor `signed-anchor.key`; the Task 12 signer does not exist when Task 13 is built. Run `go run ./testdata/nzf/gen-signed` from the repository root.
- `engine/tests/authoritative_pipeline.rs` — `signed_answers_validate_with_delv`: a real engine serving both signed images, every answer kind checked with `delv +root=example.test -a <anchor>` (modify)
- `engine/tests/hot_path_alloc.rs` — the authoritative allocation test also runs DO=1 queries against both signed images (modify)
- `engine/src/tsig.rs`, `engine/src/authoritative/state.rs`, `engine/src/authoritative/notify_out_tests.rs` — plan-review follow-up: NOTIFY waiting for `KeyMaterial` after a restart (modify)

`engine/Cargo.toml` needs no change: `ring =0.17.14` provides SHA-1 and `hickory-proto =0.26.3` with `dnssec-ring` is already a dev dependency, so the tests decode RRSIG/NSEC/NSEC3.

Interfaces:

```rust
// nsec3.rs
pub fn hash(lower_wire_name: &[u8], iterations: u16, salt: &[u8]) -> [u8; 20];
pub fn b32hex(hash: &[u8; 20]) -> [u8; 32];            // lowercase
pub fn decode_b32hex(label: &[u8]) -> Option<[u8; 20]>;
pub struct Params<'a> { pub iterations: u16, pub salt: &'a [u8] } // borrowed: no allocation on the query path
impl<'a> Params<'a> { pub fn parse(nsec3param_rdata: &'a [u8]) -> Option<Params<'a>>; } // None unless hash algorithm SHA-1
// dnssec.rs
pub enum Denial<'q, 'z> { NxDomain { qname: &'q [u8], closest_encloser: &'z Node }, NoData { node: &'z Node, qname: &'q [u8] }, WildcardAnswer { qname: &'q [u8], closest_encloser: &'z Node }, WildcardNoData { qname: &'q [u8], closest_encloser: &'z Node, wildcard: &'z Node }, InsecureReferral { cut: &'z Node } }
pub struct Proofs<'z>; // Default; fixed array of proof nodes, deduplicated by identity
impl<'z> Proofs<'z> { pub fn write(&mut self, w: &mut Writer<'_>) -> Result<(), Overflow>; } // NSEC/NSEC3 RRsets + RRSIGs to authority, then empty
pub fn add_denial<'z>(zone: &'z Zone, d: Denial<'_, 'z>, proofs: &mut Proofs<'z>); // collects; written once the answer section is complete (a wildcard CNAME's proof must follow the whole chain)
pub fn write_rrset_signed(w: &mut Writer<'_>, section: Section, owner: &[u8], set: &RRset, dnssec: bool) -> Result<(), Overflow>;
pub fn write_rrset_signed_ttl(w: &mut Writer<'_>, section: Section, owner: &[u8], set: &RRset, ttl: u32, dnssec: bool) -> Result<(), Overflow>; // negative SOA TTL
```

- [ ] Write the failing `engine/src/authoritative/dnssec_tests.rs` (declare `pub mod nsec3; pub mod dnssec; #[cfg(test)] mod dnssec_tests;`):

```rust
use super::answer::{respond, Limits, Served};
use super::msg::Question;
use super::set::AuthSet;
use super::zone::Zone;
use super::{nsec3, nzf};
use hickory_proto::dnssec::rdata::DNSSECRData;
use hickory_proto::op::{Edns, Message, MessageType, OpCode, Query, ResponseCode};
use hickory_proto::rr::{Name, RData, Record, RecordType};
use std::sync::Arc;

const NSEC: &[u8] = include_bytes!(concat!(env!("CARGO_MANIFEST_DIR"), "/../testdata/nzf/signed-nsec-full.nzf"));
const NSEC3: &[u8] = include_bytes!(concat!(env!("CARGO_MANIFEST_DIR"), "/../testdata/nzf/signed-nsec3-full.nzf"));

fn set(img: &[u8]) -> AuthSet {
    AuthSet::from_zones(vec![Arc::new(Zone::from_image(&nzf::parse(img).unwrap()).unwrap())]).unwrap()
}

fn ask(set: &AuthSet, name: &str, qtype: RecordType, dnssec_ok: bool) -> Message {
    let mut m = Message::new(9, MessageType::Query, OpCode::Query);
    m.add_query(Query::query(Name::from_ascii(name).unwrap(), qtype));
    let mut e = Edns::new();
    e.set_dnssec_ok(dnssec_ok);
    e.set_max_payload(4096);
    m.set_edns(e);
    let b = m.to_vec().unwrap();
    let q = Question::parse(&b).unwrap();
    let mut out = vec![0u8; 65535];
    match respond(set, &q, &mut out, Limits { max_len: 16384, recursion_available: false }) {
        Served::Done(n) => Message::from_vec(&out[..n]).unwrap(),
        Served::NotHosted => panic!("not hosted"),
    }
}

fn count(rrs: &[Record], t: RecordType) -> usize {
    rrs.iter().filter(|r| r.record_type() == t).count()
}

fn covered(rrs: &[Record]) -> Vec<RecordType> {
    rrs.iter().filter_map(|r| match r.data() { RData::DNSSEC(DNSSECRData::RRSIG(s)) => Some(s.input().type_covered), _ => None }).collect() // hickory 0.26: `match &r.data`
}

#[test]
fn rfc5155_appendix_a_hash_vector() {
    let h = nsec3::hash(b"\x07example\x00", 12, &[0xaa, 0xbb, 0xcc, 0xdd]);
    assert_eq!(&nsec3::b32hex(&h), b"0p9mhaveqvm6t7vbl5lop2u3t2rp3tom");
    assert_eq!(nsec3::decode_b32hex(b"0P9MHAVEQVM6T7VBL5LOP2U3T2RP3TOM"), Some(h));
}

#[test]
fn positive_answer_carries_rrsig_only_with_do() {
    for img in [NSEC, NSEC3] {
        let s = set(img);
        let with = ask(&s, "www.example.test.", RecordType::A, true);
        assert_eq!(count(&with.answers, RecordType::A), 2);
        assert_eq!(covered(&with.answers), vec![RecordType::A]);
        let without = ask(&s, "www.example.test.", RecordType::A, false);
        assert_eq!(count(&without.answers, RecordType::RRSIG), 0);
        assert_eq!(count(&without.authorities, RecordType::RRSIG), 0);
    }
}

#[test]
fn nsec_nxdomain_and_nodata_proofs() {
    let s = set(NSEC);
    let nx = ask(&s, "nope.example.test.", RecordType::A, true);
    assert_eq!(nx.metadata.response_code, ResponseCode::NXDomain);
    let n = count(&nx.authorities, RecordType::NSEC);
    assert!((1..=2).contains(&n), "NSEC covering qname and wildcard, got {n}");
    assert!(covered(&nx.authorities).contains(&RecordType::SOA));
    assert_eq!(covered(&nx.authorities).iter().filter(|t| **t == RecordType::NSEC).count(), n);

    let nodata = ask(&s, "www.example.test.", RecordType::MX, true);
    assert_eq!(nodata.metadata.response_code, ResponseCode::NoError);
    let nsecs: Vec<_> = nodata.authorities.iter().filter(|r| r.record_type() == RecordType::NSEC).collect();
    assert_eq!(nsecs.len(), 1);
    assert_eq!(nsecs[0].name.to_ascii(), "www.example.test.");
}

#[test]
fn nsec3_nxdomain_has_closest_encloser_proof() {
    let s = set(NSEC3);
    let nx = ask(&s, "x.nope.example.test.", RecordType::A, true);
    assert_eq!(nx.metadata.response_code, ResponseCode::NXDomain);
    let owners: Vec<String> = nx.authorities.iter().filter(|r| r.record_type() == RecordType::NSEC3).map(|r| r.name.to_ascii()).collect();
    assert!((2..=3).contains(&owners.len()), "{owners:?}");
    let apex_hash = String::from_utf8(nsec3::b32hex(&nsec3::hash(b"\x07example\x04test\x00", 0, &[])).to_vec()).unwrap();
    assert!(owners.iter().any(|o| o.starts_with(&apex_hash)), "closest encloser (apex) NSEC3 must match exactly: {owners:?}");
}

#[test]
fn nsec3_nodata_at_empty_non_terminal() {
    let s = set(NSEC3);
    let r = ask(&s, "b.c.example.test.", RecordType::A, true);
    assert_eq!(r.metadata.response_code, ResponseCode::NoError);
    let ent = String::from_utf8(nsec3::b32hex(&nsec3::hash(b"\x01b\x01c\x07example\x04test\x00", 0, &[])).to_vec()).unwrap();
    let owners: Vec<String> = r.authorities.iter().filter(|x| x.record_type() == RecordType::NSEC3).map(|x| x.name.to_ascii()).collect();
    assert_eq!(owners.len(), 1);
    assert!(owners[0].starts_with(&ent));
}

#[test]
fn wildcard_answer_has_signature_with_fewer_labels_and_denial_of_qname() {
    for img in [NSEC, NSEC3] {
        let r = ask(&set(img), "anything.wild.example.test.", RecordType::TXT, true);
        assert_eq!(count(&r.answers, RecordType::TXT), 1);
        let sig = r.answers.iter().find_map(|x| match x.data() { RData::DNSSEC(DNSSECRData::RRSIG(s)) => Some(s.input().num_labels), _ => None }).unwrap();
        assert_eq!(sig, 3, "labels of *.wild.example.test minus the wildcard");
        assert!(count(&r.authorities, RecordType::NSEC) + count(&r.authorities, RecordType::NSEC3) >= 1, "proof that the exact name does not exist");
    }
}

#[test]
fn referrals_include_ds_or_proof_of_no_ds() {
    for img in [NSEC, NSEC3] {
        let s = set(img);
        let secure = ask(&s, "host.sub.example.test.", RecordType::A, true);
        assert!(!secure.metadata.authoritative);
        assert_eq!(count(&secure.authorities, RecordType::DS), 1);
        assert!(covered(&secure.authorities).contains(&RecordType::DS));
        assert_eq!(covered(&secure.authorities).iter().filter(|t| **t == RecordType::NS).count(), 0, "delegation NS is unsigned");
        let insecure = ask(&s, "host.insecure.example.test.", RecordType::A, true);
        assert_eq!(count(&insecure.authorities, RecordType::DS), 0);
        assert_eq!(count(&insecure.authorities, RecordType::NSEC) + count(&insecure.authorities, RecordType::NSEC3), 1);
    }
}
```

The committed test file matches on the `data` field (`match &r.data`; hickory 0.26 has no `Record::data()`) and adds `ds_query_at_an_unsigned_delegation_is_a_signed_nodata`, `cds_and_cdnskey_are_served_as_signed_data` and `truncates_when_proofs_do_not_fit`.

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib authoritative::dnssec_tests` — expect FAIL with "file not found for module `dnssec`" / "`nsec3`".
- [ ] Implement `nsec3.rs`:

```rust
use ring::digest::{Context, SHA1_FOR_LEGACY_USE_ONLY};

fn sha1(a: &[u8], b: &[u8]) -> [u8; 20] {
    let mut c = Context::new(&SHA1_FOR_LEGACY_USE_ONLY);
    c.update(a);
    c.update(b);
    let mut out = [0u8; 20];
    out.copy_from_slice(c.finish().as_ref());
    out
}

pub fn hash(name: &[u8], iterations: u16, salt: &[u8]) -> [u8; 20] {
    let mut out = sha1(name, salt);
    for _ in 0..iterations {
        out = sha1(&out, salt);
    }
    out
}

const ALPHABET: &[u8; 32] = b"0123456789abcdefghijklmnopqrstuv";

pub fn b32hex(hash: &[u8; 20]) -> [u8; 32] {
    let mut out = [0u8; 32];
    let mut bits: u64 = 0;
    let (mut nbits, mut o) = (0u32, 0usize);
    for &b in hash {
        bits = (bits << 8) | b as u64;
        nbits += 8;
        while nbits >= 5 {
            nbits -= 5;
            out[o] = ALPHABET[((bits >> nbits) & 31) as usize];
            o += 1;
        }
    }
    out
}

pub fn decode_b32hex(label: &[u8]) -> Option<[u8; 20]> {
    if label.len() != 32 {
        return None;
    }
    let mut out = [0u8; 20];
    let mut bits: u64 = 0;
    let (mut nbits, mut o) = (0u32, 0usize);
    for &c in label {
        let v = match c.to_ascii_lowercase() {
            d @ b'0'..=b'9' => d - b'0',
            l @ b'a'..=b'v' => l - b'a' + 10,
            _ => return None,
        };
        bits = (bits << 5) | v as u64;
        nbits += 5;
        if nbits >= 8 {
            nbits -= 8;
            out[o] = (bits >> nbits) as u8;
            o += 1;
        }
    }
    Some(out)
}
```

The `hash` input name is the lowercase wire form (callers lowercase first). Hashing honours the zone's NSEC3PARAM iterations and salt, so secondary zones from external primaries are served correctly too.

- [ ] Implement `dnssec.rs` and wire it into `answer.rs` (active only when the query has DO=1 and `zone.is_signed()`):
  - every RRset written to answer or authority is followed by its `sigs` as RRSIG records (owner = the written owner; for wildcard synthesis the owner is the query name and the RRSIG labels field already reflects the wildcard); referral NS and glue are written without signatures.
  - NSEC zones: `NxDomain` → NSEC covering qname (`nsec_covering(canon_key(qname))`) plus NSEC covering `*.<closest encloser>`, deduplicated; `NoData` → NSEC at the node (for an empty non-terminal: the NSEC covering it); `WildcardAnswer` → NSEC covering qname; `WildcardNoData` → NSEC covering qname + NSEC at the wildcard; `InsecureReferral` → NSEC at the cut.
  - NSEC3 zones (params from NSEC3PARAM): hash names with `nsec3::hash`; closest-encloser proof = NSEC3 matching the closest encloser + NSEC3 covering the next-closer name (the ancestor of qname one label below the encloser); `NxDomain` → proof + NSEC3 covering `*.<closest encloser>`; `NoData` → NSEC3 matching qname; `WildcardAnswer` → NSEC3 covering the next-closer name; `WildcardNoData` → proof + NSEC3 matching the wildcard; `InsecureReferral` → NSEC3 matching the cut. Deduplicate by owner.
  - negative answers keep the SOA and add its RRSIG before the proofs; secure referrals add the DS RRset and its RRSIG to authority.
  - overflow of any DNSSEC record in answer/authority → truncation (TC) as in Task 3.
  - proofs are collected in `Proofs` during the chain and written before `Writer::finish` (a referral writes its no-DS proof before the glue).
- [ ] Plan-review follow-up (NOTIFY after restart): `KeyRing` gains a `tokio::sync::watch` counter bumped by `apply` and `async fn wait_for(&self, lower_wire_name, wait) -> Option<Arc<TsigKey>>`; `after_apply` spawns every NOTIFY and a target whose key is missing waits up to `NOTIFY_KEY_WAIT` (60 s) for `KeyMaterial` before counting `nokey`. Tests `key_ring_wait_sees_later_key_material_and_is_bounded` and `notify_needing_a_key_is_sent_once_key_material_arrives` (fails with the old immediate `nokey`).
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib authoritative`, `--test authoritative_pipeline`, `--test hot_path_alloc` — expect PASS.
- [ ] Commit: `git add engine && git commit -m "feat(engine): serve RRSIGs and NSEC/NSEC3 proofs from pre-signed zones"`.

## Task 14: Key rollovers, CDS/CDNSKEY, DNSSEC API, and the signing/key-storage acceptance tests

Files:

- `mgmt/internal/dnssec/rollover.go`, `mgmt/internal/dnssec/rollover_test.go` — pure key-state machine
- `mgmt/internal/dnssec/maintainer.go` — 5 s loop: due zones, rollover transitions, signature refresh (exists since Task 12 with the signature refresh under the advisory lock and field `Zones *zone.Service`; Task 14 adds the rollover transitions and may switch it to `Service`)
- `mgmt/internal/dnssec/service.go` — `Get`, `Update`, `StartRollover`, `ConfirmDS` for the API
- `mgmt/api/openapi.yaml`, `mgmt/internal/api/dnssec_zone.go`, `mgmt/internal/api/server.go` (`Deps.ZoneDNSSEC`), `mgmt/internal/auth/permissions.go`, `web/src/auth/permissions.ts` — `getZoneDnssec`, `updateZoneDnssec`, `startZoneKeyRollover`, `confirmZoneKskDs` (modify/create; `dnssec.go` keeps M3's resolver DNSSEC handlers)
- `mgmt/cmd/nexora-mgmt/main.go` — start the maintainer (modify)
- `e2e/harness/dnssec.go` — `Delv`, `WriteTrustAnchors` (the SoftHSM token comes from the existing `harness.InitSoftHSM`/`SoftHSM.Env`)
- `e2e/dnssec_signing_test.go` — `TestDNSSECSigningRollover`, `TestKeyStorageBackends` (`e2e/dnssec_test.go` already holds M3's `TestDNSSECValidation`)
- `mgmt/internal/dnssec/lifecycle.go`, `lifecycle_test.go`, `service_test.go`, `mgmt/migrations/00403_dnssec_key_lifecycle.sql`, `mgmt/internal/secrets/{signing,pkcs11,pkcs11_nocgo}.go` — PKCS#11 key lifecycle (review fixes, below)
- `proto/nexora/control/v1/control.proto` (`AuthZone.primary_tsig_keys = 200`), `mgmt/internal/snapshot/authzones.go`, `engine/src/authoritative/{loader,zone,notify_in}.rs`, `e2e/harness/named.go` (`NamedZone.NotifyKey`), `e2e/secondary_update_test.go` — NOTIFY bound to the primary's TSIG key (review GAP)

Review fixes folded into this task (from the Task 12 report and `.procoder/notes/plan-review.md`):

- `Disable(ctx, tx, zoneID)` (no box) and the maintainer's removals never touch the token inside the transaction: they queue PKCS#11 key refs in `dnssec_key_destruction`; `DestroyPending(ctx, st, box)` destroys and dequeues them after commit (idempotent, retried every maintainer tick; `Service.Update` runs it after its commit). `TestDisableDestroysTokenKeysOnlyAfterCommit` injects a COMMIT failure (deferred FK violation) and proves the keys survive, then that a failed destroy stays queued.
- Token keys whose generating transaction rolled back: `SweepTokenOrphans(ctx, st, box, grace)` lists token objects labelled `nexora-dnssec` (`Box.PKCS11SigningKeyRefs`), records unreferenced ones in `dnssec_token_orphans` and destroys them once unreferenced for `OrphanGrace` (1 h); the maintainer sweeps every 10 min. `TestSweepDestroysTokenKeysOfRolledBackTransactions`.
- `AuthZone.primary_tsig_keys` (parallel to `primaries`, empty when no primary has a key): a NOTIFY from a keyed primary's address must be signed with that key, otherwise REFUSED.
- CDS/CDNSKEY advertise only the newest active KSK while its DS is pending (the initial KSK stays pending until confirmed, so advertising every pending KSK would put two keys in CDS during a rollover). `View.DS` is the DS of that newest active KSK. `Store.Sign` sets `next_maintenance_at = min(signature refresh, Advance next)` (now when a transition is already due). Extra API error codes: 422 `dnssec_disabled`, `zone_not_primary`, `invalid_dnssec_settings`.

Interfaces:

```go
package dnssec
type KeyState struct{ ID, Role, State, DSState string; PublishedAt time.Time; ActivatedAt, RetiredAt, RemovedAt, DSSeenAt *time.Time }
type Policy struct{ DNSKEYTTL, MaxZoneTTL, Propagation, ParentDSTTL time.Duration; ZSKLifetime time.Duration /* 0 = manual */ }
type Actions struct{ CreateZSK bool }
func Advance(keys []KeyState, p Policy, now time.Time) (out []KeyState, act Actions, next time.Time)
type Service struct{ Store *store.Store; Box *secrets.Box; Zones *zone.Service }
type View struct{ Enabled bool; Settings Settings; Keys []KeyView; DS []string; DNSKEYs []string }
type KeyView struct{ ID, Role, State, DSState, Backend string; Algorithm uint8; KeyTag uint16; Flags uint16; PublicKey string; PublishedAt time.Time; ActivatedAt, RetiredAt, RemovedAt *time.Time }
func (s *Service) Get(ctx context.Context, zoneID uuid.UUID) (*View, error)
func (s *Service) Update(ctx context.Context, actor auth.Actor, zoneID uuid.UUID, revision int64, enabled bool, st Settings) (*View, error)
func (s *Service) StartRollover(ctx context.Context, actor auth.Actor, zoneID uuid.UUID, role string) (*View, error)
func (s *Service) ConfirmDS(ctx context.Context, actor auth.Actor, zoneID, keyID uuid.UUID) (*View, error)
type Maintainer struct{ Store *store.Store; Service *Service; Tick time.Duration }
func (m *Maintainer) Run(ctx context.Context) error
func Disable(ctx context.Context, tx pgx.Tx, zoneID uuid.UUID) error
func DestroyPending(ctx context.Context, st *store.Store, box *secrets.Box) error
func SweepTokenOrphans(ctx context.Context, st *store.Store, box *secrets.Box, grace time.Duration) (int, error)
```

- [ ] Write the failing `mgmt/internal/dnssec/rollover_test.go`:

```go
package dnssec

import (
	"testing"
	"time"
)

func ptr(t time.Time) *time.Time { return &t }

func stateOf(keys []KeyState, id string) string {
	for _, k := range keys {
		if k.ID == id {
			return k.State
		}
	}
	return "missing"
}

var policy = Policy{DNSKEYTTL: time.Hour, MaxZoneTTL: 24 * time.Hour, Propagation: time.Hour, ParentDSTTL: 24 * time.Hour, ZSKLifetime: 90 * 24 * time.Hour}

func TestZSKPrePublishTimeline(t *testing.T) {
	start := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	keys := []KeyState{
		{ID: "ksk1", Role: "ksk", State: "active", DSState: "seen", ActivatedAt: ptr(start.Add(-400 * 24 * time.Hour))},
		{ID: "zsk1", Role: "zsk", State: "active", ActivatedAt: ptr(start.Add(-10 * 24 * time.Hour))},
		{ID: "zsk2", Role: "zsk", State: "published", PublishedAt: start},
	}
	out, _, next := Advance(keys, policy, start.Add(119*time.Minute))
	if stateOf(out, "zsk2") != "published" || stateOf(out, "zsk1") != "active" || !next.Equal(start.Add(2*time.Hour)) {
		t.Fatalf("before DNSKEY TTL + propagation: %v next=%v", out, next)
	}
	out, _, next = Advance(keys, policy, start.Add(2*time.Hour))
	if stateOf(out, "zsk2") != "active" || stateOf(out, "zsk1") != "retired" || !next.Equal(start.Add(2*time.Hour+25*time.Hour)) {
		t.Fatalf("activation: %v next=%v", out, next)
	}
	out, _, _ = Advance(out, policy, start.Add(27*time.Hour-time.Second))
	if stateOf(out, "zsk1") != "retired" {
		t.Fatal("old ZSK removed before max zone TTL + propagation")
	}
	out, _, _ = Advance(out, policy, start.Add(27*time.Hour))
	if stateOf(out, "zsk1") != "removed" || stateOf(out, "ksk1") != "active" {
		t.Fatalf("removal: %v", out)
	}
}

func TestKSKDoubleSignatureTimeline(t *testing.T) {
	start := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	keys := []KeyState{
		{ID: "ksk1", Role: "ksk", State: "active", DSState: "seen", ActivatedAt: ptr(start.Add(-400 * 24 * time.Hour))},
		{ID: "ksk2", Role: "ksk", State: "active", DSState: "pending", ActivatedAt: ptr(start)},
		{ID: "zsk1", Role: "zsk", State: "active", ActivatedAt: ptr(start.Add(-time.Hour))},
	}
	out, _, _ := Advance(keys, policy, start.Add(30*24*time.Hour))
	if stateOf(out, "ksk1") != "active" || stateOf(out, "ksk2") != "active" {
		t.Fatal("both KSKs sign until the parent DS is confirmed")
	}
	seen := start.Add(31 * 24 * time.Hour)
	keys[1].DSState, keys[1].DSSeenAt = "seen", ptr(seen)
	out, _, next := Advance(keys, policy, seen.Add(time.Hour))
	if stateOf(out, "ksk1") != "active" || !next.Equal(seen.Add(25*time.Hour)) {
		t.Fatalf("waiting for parent DS TTL + propagation: %v next=%v", out, next)
	}
	out, _, _ = Advance(keys, policy, seen.Add(25*time.Hour))
	if stateOf(out, "ksk1") != "removed" || stateOf(out, "ksk2") != "active" {
		t.Fatalf("completion: %v", out)
	}
}

func TestAutomaticZSKRolloverAtLifetime(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	keys := []KeyState{
		{ID: "ksk1", Role: "ksk", State: "active", DSState: "seen", ActivatedAt: ptr(now.Add(-400 * 24 * time.Hour))},
		{ID: "zsk1", Role: "zsk", State: "active", ActivatedAt: ptr(now.Add(-89 * 24 * time.Hour))},
	}
	_, act, next := Advance(keys, policy, now)
	if act.CreateZSK || !next.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("day 89: create=%v next=%v", act.CreateZSK, next)
	}
	_, act, _ = Advance(keys, policy, now.Add(24*time.Hour))
	if !act.CreateZSK {
		t.Fatal("ZSK lifetime reached but no rollover requested")
	}
	manual := policy
	manual.ZSKLifetime = 0
	if _, act, _ = Advance(keys, manual, now.Add(365*24*time.Hour)); act.CreateZSK {
		t.Fatal("lifetime 0 means manual rollovers only")
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/dnssec/ -run 'TestZSK|TestKSK|TestAutomatic' -count=1` — expect FAIL with "undefined: Advance".
- [ ] Implement `Advance` (copy input; `next` = zero time when nothing is pending):
  - ZSK: a `published` ZSK while another ZSK is `active` becomes `active` at `PublishedAt + DNSKEYTTL + Propagation` (the old active ZSK becomes `retired` with `RetiredAt = now`); a `retired` ZSK becomes `removed` at `RetiredAt + MaxZoneTTL + Propagation`.
  - KSK: with two `active` KSKs, when the newer has `DSState=seen`, the older becomes `removed` at `DSSeenAt + ParentDSTTL + Propagation`.
  - automatic: when `ZSKLifetime > 0`, exactly one active ZSK, no `published` ZSK, `now >= ActivatedAt + ZSKLifetime` → `CreateZSK`; otherwise `next` includes that instant.
  - `next` = earliest pending transition instant.
- [ ] Run the rollover tests — expect PASS.
- [ ] Implement `service.go` and `maintainer.go`:
  - `Update`: enabling calls `dnssec.Enable` (503 `key_storage_unconfigured` / `key_backend_unavailable` when the `secrets.Box` refuses); algorithm change while enabled → 422 `algorithm_rollover_unsupported`; `nsec_mode` change allowed (rebuild); disabling calls `Disable`; zone revision required; all through `zone.Service.Mutate` with `Force: true`.
  - `StartRollover(zsk)`: refuse 409 `rollover_in_progress` when a ZSK is `published`/`retired`; generate a new ZSK `state='published'`. `StartRollover(ksk)`: refuse when two KSKs are active; generate a KSK `state='active', ds_state='pending'`. `ConfirmDS(keyID)`: key must be an active KSK with `ds_state='pending'` → `seen`, `ds_seen_at=now()`.
  - `Maintainer.Run` every 5 s: `SELECT zone_id FROM zone_dnssec WHERE enabled AND next_maintenance_at <= now() LIMIT 20`, per zone under `pg_try_advisory_lock(hashtext('dnssec:'||zone_id))`: `Mutate` (actor `auth.Actor{Type: "system", ID: "dnssec", Name: "system:dnssec"}`, audit action `dnssecMaintenance`) → load key states, policy (`DNSKEYTTL` = SOA TTL, `MaxZoneTTL` = max TTL of served records, settings), `Advance`, persist state changes (`removed` keys: `DestroySigningKey` / `private_envelope = NULL`, `removed_at`), `CreateZSK` → generate a published ZSK, `Rebuild` (re-signs; refreshes expiring signatures), `next_maintenance_at = LEAST(signature refresh, Advance next)`.
- [ ] Add OpenAPI: `GET /zones/{zoneId}/dnssec` `getZoneDnssec` → `ZoneDnssec{enabled, algorithm, nsec_mode, key_backend, propagation_delay_seconds, parent_ds_ttl_seconds, zsk_lifetime_days, keys[{id, role, algorithm, key_tag, flags, state, ds_state, backend, public_key, published_at, activated_at, retired_at, removed_at}], ds[string], dnskeys[string]}`; `PUT /zones/{zoneId}/dnssec` `updateZoneDnssec` body `{revision, enabled, algorithm (8|13, default 13), nsec_mode (nsec|nsec3, default nsec3), key_backend (kek|pkcs11, default `Box.DefaultBackend()`), propagation_delay_seconds, parent_ds_ttl_seconds, zsk_lifetime_days}` → 200 / 409 / 422 / 503 `key_storage_unconfigured`; `POST /zones/{zoneId}/dnssec/rollovers` `startZoneKeyRollover` body `{role: zsk|ksk}` → 202 `ZoneDnssec` / 409 `rollover_in_progress`; `POST /zones/{zoneId}/dnssec/rollovers/ds-published` `confirmZoneKskDs` body `{key_id}` → 200 / 422. All errors reference `#/components/responses/Error`; the specific codes are returned by `mapError` cases for the `dnssec` package's sentinel errors (`ErrAlgorithmRollover` → 422 `algorithm_rollover_unsupported`, `ErrRolloverInProgress` → 409 `rollover_in_progress`, `ErrNotPendingKSK` → 422 `ksk_not_pending`). Permissions: `getZoneDnssec` viewer; the other three operator (Go and `permissions.ts`). `Zone.dnssec_enabled` reflects the setting. `api.Deps` gains `ZoneDNSSEC *dnssec.Service`; `main.go` builds `&dnssec.Service{Store: st, Box: box, Zones: zones}` and runs `(&dnssec.Maintainer{Store: st, Service: svc, Tick: 5 * time.Second}).Run(ctx)` in a goroutine. Regenerate the API.
- [ ] Write `e2e/harness/dnssec.go`:

```go
package harness

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type TrustAnchor struct {
	Flags     uint16
	Protocol  uint8
	Algorithm uint8
	PublicKey string
}

// WriteTrustAnchors writes a BIND trust-anchors file for zone and returns its path.
func WriteTrustAnchors(t *testing.T, zone string, anchors []TrustAnchor) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("trust-anchors {\n")
	for _, a := range anchors {
		fmt.Fprintf(&b, "\t%q static-key %d %d %d %q;\n", zone, a.Flags, a.Protocol, a.Algorithm, a.PublicKey)
	}
	b.WriteString("};\n")
	p := filepath.Join(t.TempDir(), "anchors.conf")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Delv validates name/qtype against server using only the given trust anchor file rooted at root.
func Delv(t *testing.T, server, anchorFile, root, name, qtype string) string {
	t.Helper()
	host, port, err := net.SplitHostPort(server)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("delv", "-4", "@"+host, "-p", port, "-a", anchorFile, "+root="+root, name, qtype).CombinedOutput()
	if err != nil && len(out) == 0 {
		t.Fatalf("delv: %v", err)
	}
	return string(out)
}

// SoftHSM is an initialised SoftHSM token for NEXORA_PKCS11_* and SOFTHSM2_CONF.
type SoftHSM struct{ Module, Label, PinFile, Conf string }

// SoftHSMToken initialises a fresh token in a temporary directory; its PINs are derived here, never
// written into test sources.
func SoftHSMToken(t *testing.T) SoftHSM {
	t.Helper()
	dir := t.TempDir()
	tokens := filepath.Join(dir, "tokens")
	if err := os.MkdirAll(tokens, 0o700); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(dir, "softhsm2.conf")
	if err := os.WriteFile(conf, []byte("directories.tokendir = "+tokens+"\nobjectstore.backend = file\nlog.level = ERROR\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	userPIN, soPIN := strings.Repeat("5", 6), strings.Repeat("6", 6)
	cmd := exec.Command("softhsm2-util", "--init-token", "--free", "--label", "nexora-e2e", "--pin", userPIN, "--so-pin", soPIN)
	cmd.Env = append(os.Environ(), "SOFTHSM2_CONF="+conf)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("softhsm2-util: %v\n%s", err, out)
	}
	pin := filepath.Join(dir, "pin")
	if err := os.WriteFile(pin, []byte(userPIN+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return SoftHSM{Module: "/usr/lib/softhsm/libsofthsm2.so", Label: "nexora-e2e", PinFile: pin, Conf: conf}
}
```

- [ ] Write the failing `e2e/dnssec_test.go`:

```go
package e2e

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
)

type dnssecKey struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Algorithm uint8  `json:"algorithm"`
	KeyTag    uint16 `json:"key_tag"`
	Flags     uint16 `json:"flags"`
	State     string `json:"state"`
	DSState   string `json:"ds_state"`
	PublicKey string `json:"public_key"`
}

type dnssecView struct {
	Enabled bool        `json:"enabled"`
	Keys    []dnssecKey `json:"keys"`
	DS      []string    `json:"ds"`
}

func dnssecState(t *testing.T, api *harness.API, zoneID string) dnssecView {
	t.Helper()
	var v dnssecView
	api.Must(http.MethodGet, "/zones/"+zoneID+"/dnssec", nil, &v, http.StatusOK)
	return v
}

func keysWith(v dnssecView, role, state string) []dnssecKey {
	var out []dnssecKey
	for _, k := range v.Keys {
		if k.Role == role && k.State == state {
			out = append(out, k)
		}
	}
	return out
}

func anchorsFor(t *testing.T, zone string, keys []dnssecKey) string {
	var a []harness.TrustAnchor
	for _, k := range keys {
		a = append(a, harness.TrustAnchor{Flags: 257, Protocol: 3, Algorithm: k.Algorithm, PublicKey: k.PublicKey})
	}
	return harness.WriteTrustAnchors(t, zone, a)
}

func createSignedZone(t *testing.T, api *harness.API, name, backend string) (string, dnssecView) {
	t.Helper()
	var z zoneResp
	api.Must(http.MethodPost, "/zones", map[string]any{
		"name": name, "kind": "primary", "default_ttl": 2,
		"soa":         map[string]any{"mname": "ns1." + name, "rname": "hostmaster." + name, "ttl": 2, "minimum": 2},
		"nameservers": []string{"ns1." + name},
	}, &z, http.StatusCreated)
	for _, r := range [][3]string{{"ns1." + name, "A", "192.0.2.1"}, {"www." + name, "A", "192.0.2.10"}, {"*.wild." + name, "TXT", "\"wild\""}} {
		api.Must(http.MethodPost, "/zones/"+z.ID+"/records", map[string]any{"name": r[0], "type": r[1], "ttl": 2, "data": r[2]}, nil, http.StatusCreated)
	}
	api.Must(http.MethodGet, "/zones/"+z.ID, nil, &z, http.StatusOK)
	api.Must(http.MethodPut, "/zones/"+z.ID+"/dnssec", map[string]any{
		"revision": z.Revision, "enabled": true, "algorithm": 13, "nsec_mode": "nsec3", "key_backend": backend,
		"propagation_delay_seconds": 2, "parent_ds_ttl_seconds": 2, "zsk_lifetime_days": 0,
	}, nil, http.StatusOK)
	return z.ID, dnssecState(t, api, z.ID)
}

func validates(t *testing.T, server, anchors, zone, stage string) {
	t.Helper()
	root := strings.TrimSuffix(zone, ".")
	for _, q := range []struct{ name, qtype, want string }{
		{"www." + zone, "A", "; fully validated"},
		{"nosuch." + zone, "A", "; negative response, fully validated"},
		{"x.wild." + zone, "TXT", "; fully validated"},
	} {
		out := harness.Delv(t, server, anchors, root, q.name, q.qtype)
		// delv prints ";; resolution failed: ncache nxdomain" above a validated denial, so only a
		// whole "; fully validated" line counts, and "failed" is fatal for positive answers only.
		failed := strings.Contains(out, "resolution failed") && !strings.Contains(out, "resolution failed: ncache")
		if !strings.Contains("\n"+out, "\n"+q.want+"\n") || failed {
			t.Fatalf("%s: delv %s %s:\n%s", stage, q.name, q.qtype, out)
		}
	}
}

func queryDO(t *testing.T, server, name string, qtype uint16) *dns.Msg {
	t.Helper()
	return harness.MustQuery(t, server, name, qtype, harness.QueryOpts{TCP: true, DO: true, EDNSSize: 4096})
}

func TestDNSSECSigningRollover(t *testing.T) {
	e := startAuthEnv(t, []string{"NEXORA_KEK_FILE=" + harness.WriteKEK(t)}, "sign-1")
	api, eng := e.api, e.engines[0]

	zoneID, st := createSignedZone(t, api, "signed.test.", "kek")
	oldKSK := keysWith(st, "ksk", "active")
	oldZSK := keysWith(st, "zsk", "active")
	if len(oldKSK) != 1 || len(oldZSK) != 1 || len(st.DS) != 1 {
		t.Fatalf("initial keys: %+v", st)
	}
	anchors := anchorsFor(t, "signed.test.", oldKSK)
	harness.EventuallyTrue(t, 10*time.Second, func() bool {
		return strings.Contains(harness.Delv(t, eng.DNS, anchors, "signed.test", "www.signed.test.", "A"), "; fully validated")
	}, "signed.test validates on the engine")
	validates(t, eng.DNS, anchors, "signed.test.", "initial")

	// ZSK pre-publish rollover; validation must hold at every intermediate state
	api.Must(http.MethodPost, "/zones/"+zoneID+"/dnssec/rollovers", map[string]any{"role": "zsk"}, nil, http.StatusAccepted)
	deadline := time.Now().Add(120 * time.Second)
	var newZSK dnssecKey
	for {
		st = dnssecState(t, api, zoneID)
		validates(t, eng.DNS, anchors, "signed.test.", fmt.Sprintf("zsk rollover %+v", st.Keys))
		active := keysWith(st, "zsk", "active")
		removed := keysWith(st, "zsk", "removed")
		if len(active) == 1 && active[0].ID != oldZSK[0].ID && len(removed) == 1 && removed[0].ID == oldZSK[0].ID {
			newZSK = active[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ZSK rollover did not complete: %+v", st.Keys)
		}
		time.Sleep(2 * time.Second)
	}
	harness.EventuallyTrue(t, 10*time.Second, func() bool {
		for _, rr := range queryDO(t, eng.DNS, "www.signed.test.", dns.TypeA).Answer {
			if s, ok := rr.(*dns.RRSIG); ok && s.KeyTag == newZSK.KeyTag {
				return true
			}
		}
		return false
	}, "www.signed.test. signed by the new ZSK")
	validates(t, eng.DNS, anchors, "signed.test.", "after zsk rollover")

	// KSK double-signature rollover with CDS publication
	api.Must(http.MethodPost, "/zones/"+zoneID+"/dnssec/rollovers", map[string]any{"role": "ksk"}, nil, http.StatusAccepted)
	st = dnssecState(t, api, zoneID)
	var newKSK dnssecKey
	for _, k := range keysWith(st, "ksk", "active") {
		if k.ID != oldKSK[0].ID {
			newKSK = k
		}
	}
	harness.EventuallyTrue(t, 10*time.Second, func() bool {
		m := harness.MustQuery(t, eng.DNS, "signed.test.", dns.TypeCDS, harness.QueryOpts{TCP: true})
		if len(m.Answer) != 1 {
			return false
		}
		cds, ok := m.Answer[0].(*dns.CDS)
		return ok && cds.KeyTag == newKSK.KeyTag
	}, "CDS advertises the new KSK")
	newAnchors := anchorsFor(t, "signed.test.", []dnssecKey{newKSK})
	validates(t, eng.DNS, anchors, "signed.test.", "double signature, old anchor")
	validates(t, eng.DNS, newAnchors, "signed.test.", "double signature, new anchor")
	api.Must(http.MethodPost, "/zones/"+zoneID+"/dnssec/rollovers/ds-published", map[string]any{"key_id": newKSK.ID}, nil, http.StatusOK)
	harness.EventuallyTrue(t, 60*time.Second, func() bool {
		return len(keysWith(dnssecState(t, api, zoneID), "ksk", "removed")) == 1
	}, "old KSK removed")
	harness.EventuallyTrue(t, 10*time.Second, func() bool {
		return len(harness.MustQuery(t, eng.DNS, "signed.test.", dns.TypeDNSKEY, harness.QueryOpts{TCP: true}).Answer) == 2
	}, "DNSKEY RRset holds the new KSK and ZSK only")
	validates(t, eng.DNS, newAnchors, "signed.test.", "after ksk rollover")
}

func TestKeyStorageBackends(t *testing.T) {
	kekFile := harness.WriteKEK(t)
	hsm := harness.SoftHSMToken(t)
	e := startAuthEnv(t, []string{
		"NEXORA_KEK_FILE=" + kekFile,
		"NEXORA_PKCS11_MODULE=" + hsm.Module,
		"NEXORA_PKCS11_TOKEN_LABEL=" + hsm.Label,
		"NEXORA_PKCS11_PIN_FILE=" + hsm.PinFile,
		"SOFTHSM2_CONF=" + hsm.Conf,
	}, "keys-1")
	api, eng := e.api, e.engines[0]

	var key tsigKeyResp
	api.Must(http.MethodPost, "/tsig-keys", map[string]any{"name": "disk-check.", "algorithm": "hmac-sha256"}, &key, http.StatusCreated)
	createPrimaryZone(t, api, "tsig-user.test.", map[string]any{"update": map[string]any{"tsig_key_ids": []string{key.ID}}})

	for _, c := range []struct{ zone, backend string }{{"kek.test.", "kek"}, {"hsm.test.", "pkcs11"}} {
		_, st := createSignedZone(t, api, c.zone, c.backend)
		anchors := anchorsFor(t, c.zone, keysWith(st, "ksk", "active"))
		harness.EventuallyTrue(t, 15*time.Second, func() bool {
			return strings.Contains(harness.Delv(t, eng.DNS, anchors, strings.TrimSuffix(c.zone, "."), "www."+c.zone, "A"), "; fully validated")
		}, c.zone+" validates")
		validates(t, eng.DNS, anchors, c.zone, c.backend)
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, e.pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	kekRaw, err := os.ReadFile(kekFile)
	if err != nil {
		t.Fatal(err)
	}
	kek, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(kekRaw)))
	if err != nil {
		t.Fatal(err)
	}
	var dump string
	if err := conn.QueryRow(ctx, `SELECT string_agg(k::text, E'\n') FROM dnssec_keys k`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(ctx, `SELECT k.backend, k.key_ref, k.private_envelope FROM dnssec_keys k JOIN zones z ON z.id = k.zone_id WHERE z.name IN ('kek.test.', 'hsm.test.')`)
	if err != nil {
		t.Fatal(err)
	}
	var privateScalars [][]byte
	var kekKeys, hsmKeys int
	for rows.Next() {
		var backend string
		var ref, env []byte
		if err := rows.Scan(&backend, &ref, &env); err != nil {
			t.Fatal(err)
		}
		switch backend {
		case "kek":
			kekKeys++
			der := openNXE1(t, kek, "nexora/dnssec/v1:"+hex.EncodeToString(ref), env)
			priv, err := x509.ParsePKCS8PrivateKey(der)
			if err != nil {
				t.Fatalf("KEK envelope does not decrypt to PKCS#8: %v", err)
			}
			d := priv.(*ecdsa.PrivateKey).D.FillBytes(make([]byte, 32))
			privateScalars = append(privateScalars, d, der)
		case "pkcs11":
			hsmKeys++
			if env != nil {
				t.Fatal("PKCS#11 key row carries a private envelope")
			}
		}
	}
	rows.Close()
	if kekKeys != 2 || hsmKeys != 2 {
		t.Fatalf("keys per backend: kek=%d pkcs11=%d", kekKeys, hsmKeys)
	}
	for _, secret := range privateScalars {
		for _, form := range []string{string(secret), hex.EncodeToString(secret), base64.StdEncoding.EncodeToString(secret)} {
			if strings.Contains(dump, form) {
				t.Fatal("plaintext private key material found in dnssec_keys")
			}
		}
	}

	tsigSecret, err := base64.StdEncoding.DecodeString(key.Secret)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := append([][]byte{tsigSecret, []byte(key.Secret), []byte(hex.EncodeToString(tsigSecret))}, privateScalars...)
	sawSnapshot := false
	err = filepath.WalkDir(eng.StateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, "snapshot.binpb") {
			sawSnapshot = bytes.Contains(data, []byte("tsig-user"))
		}
		for _, f := range forbidden {
			if bytes.Contains(data, f) {
				t.Fatalf("key material found on engine disk in %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawSnapshot {
		t.Fatal("engine snapshot with the zones was not found on disk (positive check)")
	}

	t.Run("refuses secrets without key storage", func(t *testing.T) {
		bare := startAuthEnv(t, nil)
		status, err := bare.api.Do(http.MethodPost, "/tsig-keys", map[string]any{"name": "k.", "algorithm": "hmac-sha256"}, nil)
		if status != http.StatusServiceUnavailable || err == nil || !strings.Contains(err.Error(), "key_storage_unconfigured") {
			t.Fatalf("tsig key without key storage: %d %v", status, err)
		}
		z := createPrimaryZone(t, bare.api, "nokeys.test.", nil)
		status, err = bare.api.Do(http.MethodPut, "/zones/"+z.ID+"/dnssec", map[string]any{"revision": z.Revision, "enabled": true}, nil)
		if status != http.StatusServiceUnavailable || err == nil || !strings.Contains(err.Error(), "key_storage_unconfigured") {
			t.Fatalf("dnssec without key storage: %d %v", status, err)
		}
	})
}

// openNXE1 decrypts an NXE1 file-KEK envelope independently of mgmt code.
func openNXE1(t *testing.T, kek []byte, purpose string, env []byte) []byte {
	t.Helper()
	if len(env) < 101 || string(env[:4]) != "NXE1" || env[4] != 1 {
		t.Fatalf("not a file-KEK NXE1 envelope")
	}
	kb, err := aes.NewCipher(kek)
	if err != nil {
		t.Fatal(err)
	}
	kg, err := cipher.NewGCM(kb)
	if err != nil {
		t.Fatal(err)
	}
	dek, err := kg.Open(nil, env[13:25], env[25:73], []byte("NXE1-dek"))
	if err != nil {
		t.Fatalf("unwrap DEK: %v", err)
	}
	db, err := aes.NewCipher(dek)
	if err != nil {
		t.Fatal(err)
	}
	dg, err := cipher.NewGCM(db)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := dg.Open(nil, env[73:85], env[85:], []byte(purpose))
	if err != nil {
		t.Fatalf("open envelope: %v", err)
	}
	return plain
}
```

- [ ] Run `scripts/dev-exec.sh make e2e-build` then `scripts/dev-exec.sh go test ./e2e/ -run 'TestDNSSECSigningRollover|TestKeyStorageBackends' -count=1 -timeout 15m` — expect PASS (before the API step they fail with "PUT /zones/…/dnssec: status 404").
- [ ] Commit: `git add mgmt web/src/api/schema.d.ts web/src/auth/permissions.ts e2e && git commit -m "feat(mgmt): ZSK pre-publish and KSK double-signature rollovers with CDS, DNSSEC API"`.

## Task 15: GUI `/zones`

The screens follow the M2/M3 GUI layout (pages in `web/src/pages/`, TanStack Query hooks over `api`/`unwrap` in `web/src/api/`, Radix components from `components/ui`) and are covered by request-based `TestGUICoverage`: every M4 operation must be issued by the browser in `web/e2e/screens/18-zones.spec.ts` or `19-zone-security.spec.ts` (the coverage glob `[01][0-9]-*.spec.ts` stops at 19). The web package has no unit-test runner, so the 409 conflict dialog is tested in Playwright.

Files:

- `web/src/api/zones.ts` — hooks for zones, records, import/export, refresh, zone DNSSEC and TSIG keys
- `web/src/lib/zoneRdataHints.ts` — placeholder/help text per managed type
- `web/src/pages/ZonesPage.tsx` — zone list, "New zone" dialog (primary/secondary)
- `web/src/pages/ZoneDetailPage.tsx` — header (serial, kind, status), tabs, "Delete zone"
- `web/src/pages/ZoneRecordsTab.tsx` — record table with type filter and "Load more"
- `web/src/pages/ZoneRecordEditor.tsx` — per-type form, revision conflict dialog
- `web/src/pages/ZoneTransfersTab.tsx` — transfer ACL + TSIG, notify targets, update keys, primaries and refresh status for secondaries
- `web/src/pages/ZoneDnssecTab.tsx` — enable form, keys table, DS records, rollover and DS-confirmation actions
- `web/src/pages/ZoneImportExportTab.tsx` — file/paste import with line errors, export download
- `web/src/pages/TsigKeysPage.tsx` — list, create (secret shown once with copy), delete
- `web/src/app/router.tsx` — routes `zones`, `zones/tsig-keys`, `zones/:zoneId` (modify)
- `web/src/components/layout/AppShell.tsx` — nav item `{ route: "zones", path: "/zones", label: "Zones", icon: Globe, op: "listZones" }` after "DNSSEC" (modify)
- `web/e2e/screens/18-zones.spec.ts`, `web/e2e/screens/19-zone-security.spec.ts`
- `web/e2e/screens/15-rpz.spec.ts` — the TSIG transfer zone now saves (modify)
- `e2e/gui_test.go` — `TestGUICoverage`'s management plane gets `NEXORA_KEK_FILE` (modify)

Interfaces:

```ts
// web/src/api/zones.ts
export function useZones(): UseQueryResult<Schemas["Zone"][]>;
export function useZone(zoneId: string): UseQueryResult<Schemas["Zone"]>;
// "Load more" pages with useInfiniteQuery; the cursor is the page param.
export function useRecords(
  zoneId: string,
  filter: { name?: string; type?: string },
): UseInfiniteQueryResult<InfiniteData<Schemas["RecordPage"]>>;
export function useSaveRecord(
  zoneId: string,
): UseMutationResult<
  Schemas["Record"],
  ApiError,
  { id?: string; revision?: number; input: Schemas["RecordInput"] }
>;
export function useZoneDnssec(
  zoneId: string,
): UseQueryResult<Schemas["ZoneDnssec"]>;
export function useTsigKeys(): UseQueryResult<Schemas["TsigKey"][]>;
// ApiError (web/src/api/client.ts) gains `details?: { line: number; message: string }[]`, filled by `unwrap` from the Error body.
// web/src/pages/ZoneRecordEditor.tsx
export function ZoneRecordEditor(props: {
  zoneId: string;
  zoneName: string;
  record?: Schemas["Record"];
  onClose(): void;
}): JSX.Element;
```

- [x] Make `TestGUICoverage` start its management plane with key storage — in `e2e/gui_test.go`: `env.StartMgmt(pg, ca, harness.MgmtOptions{OIDC: oidc, OIDCAdminGroup: "nexora-admins", ExtraEnv: []string{"NEXORA_KEK_FILE=" + harness.WriteKEK(t)}})` — and update M3's `web/e2e/screens/15-rpz.spec.ts`: in the transfer-zone dialog, replace

```ts
await dialog.getByRole("button", { name: "Save" }).click();
// TestGUICoverage's management plane runs without NEXORA_KEK_FILE.
await expect(dialog.getByRole("alert")).toContainText(
  "Key storage is not configured on the management plane (NEXORA_KEK_FILE)",
);
await dialog.getByLabel("TSIG algorithm").click();
await page.getByRole("option", { name: "None", exact: true }).click();
```

with

```ts
// TestGUICoverage's management plane has key storage since M4, so the TSIG secret is sealed and
// saved; the 503 path without NEXORA_KEK_FILE stays covered by mgmt/internal/api/resolution_test.go.
```

so the following `Policy override` selection and `Save` store the transfer zone with `hmac-sha256`, `rpz-key.` and the derived secret (the row assertions after it stay).

- [x] Write the failing `web/e2e/screens/18-zones.spec.ts`:

```ts
import { test, expect, env, login } from "../fixtures";

const rowName = (text: string) => new RegExp(text.replaceAll(".", "\\."));

test("operator creates a zone, edits records, resolves a revision conflict, imports and exports", async ({
  page,
}) => {
  // TestGUICoverage counts the browser requests below: listZones, createZone, getZone, listZoneRecords,
  // createZoneRecord, updateZoneRecord, deleteZoneRecord, updateZone, importZoneFile, exportZoneFile.
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  const zone = `gui-${Date.now()}.test.`;
  await page.getByTestId("nav-zones").click();
  await expect(page.getByRole("heading", { name: "Zones" })).toBeVisible();

  await page.getByRole("button", { name: "New zone" }).click();
  const create = page.getByRole("dialog", { name: "New zone" });
  await create.getByLabel("Zone name").fill(zone);
  await create.getByLabel("Primary name server").fill(`ns1.${zone}`);
  await create.getByLabel("Responsible mailbox").fill(`hostmaster.${zone}`);
  await create.getByLabel("Name servers").fill(`ns1.${zone}`);
  await create.getByRole("button", { name: "Create" }).click();
  await expect(page.getByRole("heading", { name: zone })).toBeVisible();

  await page.getByRole("button", { name: "Add record" }).click();
  let editor = page.getByRole("dialog", { name: "Record" });
  await editor.getByLabel("Name").fill("www");
  await editor.getByLabel("Type").click();
  await page.getByRole("option", { name: "A", exact: true }).click();
  await editor.getByLabel("Data").fill("192.0.2.10");
  await editor.getByRole("button", { name: "Save" }).click();
  await expect(
    page.getByRole("row", { name: /www.*192\.0\.2\.10/ }),
  ).toBeVisible();

  await page
    .getByRole("row", { name: /www.*192\.0\.2\.10/ })
    .getByRole("button", { name: "Edit" })
    .click();
  editor = page.getByRole("dialog", { name: "Record" });
  // another writer changes the record behind the open editor (page.request shares the session cookie)
  const zoneId = page.url().split("/zones/")[1];
  const list = await (
    await page.request.get(
      `/api/v1/zones/${zoneId}/records?name=www.${zone}&type=A`,
    )
  ).json();
  const rec = list.items[0];
  const bump = await page.request.put(
    `/api/v1/zones/${zoneId}/records/${rec.id}`,
    {
      data: {
        name: rec.name,
        type: "A",
        ttl: 300,
        data: "192.0.2.99",
        revision: rec.revision,
      },
    },
  );
  expect(bump.status()).toBe(200);
  await editor.getByLabel("Data").fill("192.0.2.11");
  await editor.getByRole("button", { name: "Save" }).click();
  const conflict = page.getByRole("alertdialog");
  await expect(conflict).toContainText("changed by someone else");
  await expect(conflict).toContainText("192.0.2.99");
  await conflict.getByRole("button", { name: "Reload" }).click();
  await editor.getByLabel("Data").fill("192.0.2.11");
  await editor.getByRole("button", { name: "Save" }).click();
  const www = page.getByRole("row", { name: /www.*192\.0\.2\.11/ });
  await expect(www).toBeVisible();
  await www.getByRole("button", { name: "Delete" }).click();
  await page.getByTestId("confirm-delete").click();
  await expect(page.getByRole("row", { name: /www/ })).toHaveCount(0);

  await page.getByRole("tab", { name: "Transfers" }).click();
  await page.getByLabel("Allowed transfer networks").fill("192.0.2.0/24");
  await page.getByRole("button", { name: "Save settings" }).click();
  await expect(page.getByText("Settings saved")).toBeVisible();

  await page.getByRole("tab", { name: "Import/Export" }).click();
  const origin = `$ORIGIN ${zone}\n$TTL 300\n@ SOA ns1 hostmaster 5 7200 3600 1209600 300\n@ NS ns1\nns1 A 192.0.2.1\n`;
  await page.getByLabel("Zone file").fill(`${origin}bad A 999.0.0.1\n`);
  await page.getByRole("button", { name: "Import" }).click();
  await expect(page.getByRole("alert")).toContainText("line 6:");
  await page.getByLabel("Zone file").fill(`${origin}imported A 192.0.2.50\n`);
  await page.getByRole("button", { name: "Import" }).click();
  await expect(page.getByText("Imported 3 records")).toBeVisible();
  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "Export" }).click();
  expect((await download).suggestedFilename()).toBe(`${zone}zone`);

  await page.getByTestId("nav-zones").click();
  await expect(page.getByRole("row", { name: rowName(zone) })).toBeVisible();
});

test("viewer sees zones read-only", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_VIEWER_USER"),
    env("NEXORA_E2E_VIEWER_PASSWORD"),
  );
  await page.getByTestId("nav-zones").click();
  await expect(page.getByRole("heading", { name: "Zones" })).toBeVisible();
  await expect(page.getByRole("button", { name: "New zone" })).toHaveCount(0);
});
```

- [x] Write the failing `web/e2e/screens/19-zone-security.spec.ts`:

```ts
import { test, expect, env, login } from "../fixtures";

test("admin manages TSIG keys, zone signing, rollovers, a secondary refresh and zone deletion", async ({
  page,
}) => {
  // TestGUICoverage counts: listTsigKeys, createTsigKey, deleteTsigKey, getZoneDnssec, updateZoneDnssec,
  // startZoneKeyRollover, confirmZoneKskDs, refreshZone, deleteZone.
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  const suffix = Date.now();
  const keyName = `gui-key-${suffix}.`;
  await page.getByTestId("nav-zones").click();
  await page.getByRole("link", { name: "TSIG keys" }).click();
  await page.getByRole("button", { name: "New TSIG key" }).click();
  const newKey = page.getByRole("dialog", { name: "New TSIG key" });
  await newKey.getByLabel("Key name").fill(keyName);
  await newKey.getByRole("button", { name: "Create" }).click();
  await expect(newKey).toContainText("This secret is shown once");
  await newKey.getByRole("button", { name: "Done" }).click();
  const keyRow = page.getByRole("row", {
    name: new RegExp(keyName.replaceAll(".", "\\.")),
  });
  await keyRow.getByRole("button", { name: "Delete" }).click();
  await page.getByTestId("confirm-delete").click();
  await expect(keyRow).toHaveCount(0);

  const signed = `signed-${suffix}.test.`;
  await page.getByTestId("nav-zones").click();
  await page.getByRole("button", { name: "New zone" }).click();
  let create = page.getByRole("dialog", { name: "New zone" });
  await create.getByLabel("Zone name").fill(signed);
  await create.getByLabel("Primary name server").fill(`ns1.${signed}`);
  await create.getByLabel("Responsible mailbox").fill(`hostmaster.${signed}`);
  await create.getByLabel("Name servers").fill(`ns1.${signed}`);
  await create.getByRole("button", { name: "Create" }).click();
  await expect(page.getByRole("heading", { name: signed })).toBeVisible();

  await page.getByRole("tab", { name: "DNSSEC" }).click();
  await page.getByRole("button", { name: "Enable signing" }).click();
  const keys = page.getByRole("table", { name: "Signing keys" });
  await expect(keys).toContainText("ksk");
  await expect(page.getByText(/IN DS \d+ 13 2/)).toBeVisible();
  await page.getByRole("button", { name: "Roll ZSK" }).click();
  await expect(keys).toContainText("published");
  await page
    .getByRole("button", { name: "Parent DS published" })
    .first()
    .click();
  await expect(keys).toContainText("seen");
  await page.getByRole("button", { name: "Roll KSK" }).click();
  await page
    .getByRole("alertdialog")
    .getByRole("button", { name: "Confirm" })
    .click();
  await expect(keys).toContainText("pending");

  const pulled = `pulled-${suffix}.test.`;
  await page.getByTestId("nav-zones").click();
  await page.getByRole("button", { name: "New zone" }).click();
  create = page.getByRole("dialog", { name: "New zone" });
  await create.getByLabel("Kind").click();
  await page.getByRole("option", { name: "Secondary", exact: true }).click();
  await create.getByLabel("Zone name").fill(pulled);
  await create.getByLabel("Primaries").fill("127.0.0.1:1");
  await create.getByRole("button", { name: "Create" }).click();
  await page.getByRole("tab", { name: "Transfers" }).click();
  await page.getByRole("button", { name: "Refresh now" }).click();
  await expect(page.getByText("Refresh requested")).toBeVisible();
  await page.getByRole("button", { name: "Delete zone" }).click();
  await page.getByTestId("confirm-delete").click();
  await expect(page).toHaveURL(/\/zones$/);
  await expect(page.getByRole("link", { name: pulled })).toHaveCount(0);
});
```

- [x] Run `scripts/dev-exec.sh make e2e-build` then `scripts/dev-exec.sh go test ./e2e/ -run TestGUICoverage -count=1` — expect FAIL: Playwright times out on `getByTestId("nav-zones")` and the coverage report lists the M4 operations (e.g. "listZones (GET /zones)") as uncovered.
- [x] Implement `web/src/api/zones.ts` in the style of `resolution.ts` (`useQuery`/`useMutation` over `api.GET/POST/PUT/PATCH/DELETE` with `unwrap`, query keys `["zones"]`, `["zones", id]`, `["zones", id, "records", filter]`, `["zones", id, "dnssec"]`, `["tsig-keys"]`, invalidation on success), and extend `ApiError`/`unwrap` in `client.ts` with `details`. Export downloads fetch `/api/v1/zones/{id}/export` with `credentials: "same-origin"` and save through a Blob URL on an `<a download="<zone>zone">`.
- [x] Implement the pages:
  - `ZonesPage`: heading "Zones", table (name link, kind, serial, DNSSEC badge, secondary status: last success / expired), link "TSIG keys", "New zone" dialog (title "New zone"; Kind select Primary/Secondary; primary: Zone name, Default TTL, Primary name server, Responsible mailbox, Name servers; secondary: Zone name, Primaries `ip:port` + optional TSIG key), buttons hidden unless `createZone` is permitted (`permissions.ts`).
  - `ZoneDetailPage` (`/zones/:zoneId`): heading = zone name, serial and kind, Radix `Tabs` Records / Transfers / DNSSEC / Import/Export, "Delete zone" with the shared destructive confirm (`data-testid="confirm-delete"`), navigating to `/zones` afterwards.
  - `ZoneRecordsTab`: names shown relative to the zone (`@` for apex), type filter, "Load more" by cursor, "Add record", per-row "Edit"/"Delete"; secondary zones read-only.
  - `ZoneRecordEditor` (dialog title "Record"): fields Name (relative input, sent absolute), Type (Radix select of the 17 managed types), TTL, Data (placeholder from `zoneRdataHints`); 422 shows `message` under Data; 409 opens an `alertdialog` "This record was changed by someone else" showing the current server value (refetched with `listZoneRecords` by name and type) with buttons Reload (replaces form values and revision) and Cancel. The `alertdialog` is rendered inside the Record dialog in place of the form (no nested modal); the Data input also runs a client-side per-type check (`checkRdata` in `zoneRdataHints.ts`: IPv4/IPv6, absolute target names, numeric fields, hex) whose message shows under the field before any request.
  - `ZoneTransfersTab`: Allowed transfer networks (comma-separated input), transfer TSIG key select, notify targets list, update TSIG keys multi-select; secondaries: primaries list, status fields and "Refresh now" (`refreshZone`, then "Refresh requested"); "Save settings" sends `updateZone` with the zone `revision`, shows "Settings saved", and a 409 shows a reload banner.
  - `ZoneDnssecTab`: enable form (algorithm 13 default / 8, NSEC3 default / NSEC, key backend, propagation delay, parent DS TTL, ZSK lifetime days) with "Enable signing"; a 503 shows the server `message` (M3's "Key storage is not configured on the management plane (NEXORA_KEK_FILE)"); keys table with accessible name "Signing keys" (role, algorithm, tag, state, DS state, backend); DS records (`<zone> IN DS …`) with copy buttons; "Roll ZSK", "Roll KSK" (confirmation `alertdialog` with "Confirm"), "Parent DS published" for each active KSK with `ds_state=pending`, listed under the DS records rather than inside the keys table (so the table's state assertions only see key states).
  - `ZoneImportExportTab`: textarea labelled "Zone file" + file picker, "Import" (sends the zone revision; 422 renders `details` as "line N: message" in a `role="alert"` list; success shows "Imported N records"), "Export" button.
  - `TsigKeysPage` (`/zones/tsig-keys`): table, "New TSIG key" dialog (Key name, algorithm select, optional secret) whose success view shows "This secret is shown once", the secret with a copy button, a BIND `key {}` snippet and "Done"; per-row "Delete" with the destructive confirm (sends `revision`).
- [x] Run `scripts/dev-exec.sh make web-test` — expect PASS (typecheck, lint including `check-permissions.mjs`, build); then `scripts/dev-exec.sh make e2e-build` and `scripts/dev-exec.sh go test ./e2e/ -run TestGUICoverage -count=1` — expect PASS with no uncovered operation.
- [ ] Commit: `git add web e2e/gui_test.go && git commit -m "feat(web): zones, records, transfers, DNSSEC, import/export and TSIG key screens"`.

## Task 16: kw deployment key storage and smoke subtests

kw today (M2 Task 13): `nexora-engine` is a DaemonSet whose DNS LoadBalancer `nexora-dns` (192.168.10.136) already exposes `dns-udp` and `dns-tcp` on 53 with `externalTrafficPolicy: Local` (engines see real client addresses, so transfer ACLs and NOTIFY sources work); `nexora-mgmt` serves the GUI/API only at `https://nexora.kw.local` (Secure cookies) and gRPC on 192.168.10.135:9443; images are built and applied by `scripts/kw-deploy.sh`, which also creates the secrets `nexora-ca` and `nexora-dns-tls` when absent. The repository has no Helm chart or compose file, so M4 changes only the kw manifests and the deploy script.

Files:

- `deploy/kw/mgmt.yaml` — volume `kek` from secret `nexora-kek` (`defaultMode: 0440`) mounted read-only at `/etc/nexora/kek`, env `NEXORA_KEK_FILE=/etc/nexora/kek/kek` (modify)
- `scripts/kw-deploy.sh` — create `nexora-kek` once, like `nexora-ca` (modify)
- `deploy/kw/README.md` — the `nexora-kek` secret under "Secrets" and the `TestKwSmokeM4` command (modify)
- `e2e/kw_smoke_m4_test.go` — `TestKwSmokeM4` with subtests `zones`, `axfr`, `dnssec` (package `e2e`, skipped unless the kw variables are set, like `TestKwSmoke`)

Interfaces:

```go
// package e2e; environment as for TestKwSmoke (printed by scripts/kw-deploy.sh): NEXORA_KW_DNS_ADDR (192.168.10.136:53),
// NEXORA_KW_API_URL (https://nexora.kw.local), NEXORA_KW_API_CA_FILE, NEXORA_KW_ADMIN_PASSWORD_FILE, NEXORA_KW_ENGINES, ...
func TestKwSmokeM4(t *testing.T)
```

- [ ] Write the failing `e2e/kw_smoke_m4_test.go`:

```go
package e2e

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
)

// TestKwSmokeM4 checks authoritative serving, AXFR over the TCP LoadBalancer and online signing on
// the kw deployment. It deletes its zone when it ends.
func TestKwSmokeM4(t *testing.T) {
	env := loadKwEnv(t)
	api := kwLogin(t, env)
	name := fmt.Sprintf("smoke-%d.nexora-smoke.test.", time.Now().Unix())
	var zone zoneResp
	api.Must(http.MethodPost, "/zones", map[string]any{
		"name": name, "kind": "primary", "default_ttl": 60,
		"soa":         map[string]any{"mname": "ns1." + name, "rname": "hostmaster." + name},
		"nameservers": []string{"ns1." + name},
		"transfer":    map[string]any{"allow_cidrs": []string{"0.0.0.0/0"}},
	}, &zone, http.StatusCreated)
	t.Cleanup(func() {
		var z zoneResp
		api.Must(http.MethodGet, "/zones/"+zone.ID, nil, &z, http.StatusOK)
		api.Must(http.MethodDelete, fmt.Sprintf("/zones/%s?revision=%d", zone.ID, z.Revision), nil, nil, http.StatusNoContent)
	})
	api.Must(http.MethodPost, "/zones/"+zone.ID+"/records", map[string]any{"name": "www." + name, "type": "A", "ttl": 60, "data": "192.0.2.10"}, nil, http.StatusCreated)
	kwWaitApplied(t, api, env.engines)

	t.Run("zones", func(t *testing.T) {
		r := harness.WaitDNSAnswer(t, env.dnsAddr, "www."+name, dns.TypeA, 15*time.Second, func(m *dns.Msg) bool {
			return m.Authoritative && len(m.Answer) == 1
		})
		if a, ok := r.Answer[0].(*dns.A); !ok || a.A.String() != "192.0.2.10" {
			t.Fatalf("answer: %v", r.Answer)
		}
	})

	t.Run("axfr", func(t *testing.T) {
		m := new(dns.Msg)
		m.SetAxfr(name)
		ch, err := (&dns.Transfer{DialTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second}).In(m, env.dnsAddr)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for e := range ch {
			if e.Error != nil {
				t.Fatal(e.Error)
			}
			n += len(e.RR)
		}
		if n < 4 {
			t.Fatalf("AXFR over the TCP LoadBalancer returned %d records", n)
		}
	})

	t.Run("dnssec", func(t *testing.T) {
		var z zoneResp
		api.Must(http.MethodGet, "/zones/"+zone.ID, nil, &z, http.StatusOK)
		if status, err := api.Do(http.MethodPut, "/zones/"+zone.ID+"/dnssec", map[string]any{"revision": z.Revision, "enabled": true}, nil); status != http.StatusOK {
			t.Fatalf("enable DNSSEC on kw (is the nexora-kek secret mounted?): %d %v", status, err)
		}
		kwWaitApplied(t, api, env.engines)
		harness.Eventually(t, 30*time.Second, func() error {
			r, _, err := harness.Query(t, env.dnsAddr, "www."+name, dns.TypeA, harness.QueryOpts{TCP: true, DO: true, EDNSSize: 4096})
			if err != nil {
				return err
			}
			for _, rr := range r.Answer {
				if _, ok := rr.(*dns.RRSIG); ok {
					return nil
				}
			}
			return fmt.Errorf("no RRSIG in %v", r.Answer)
		})
	})
}
```

- [ ] Run the smoke test against the current (M3) deployment, from the dev pod with the environment of `deploy/kw/README.md`: `scripts/dev-exec.sh env NEXORA_KW_DNS_ADDR=192.168.10.136:53 NEXORA_KW_API_URL=https://nexora.kw.local NEXORA_KW_API_CA_FILE=/work/kw-cluster-ca.crt NEXORA_KW_ENCRYPTED_ADDR=192.168.10.136 NEXORA_KW_CA_FILE=/work/kw-ca.crt NEXORA_KW_DNS_TLS_NAME=dns.nexora.kw.local NEXORA_KW_ENGINES=8 NEXORA_KW_MGMT_LB_IP=192.168.10.135 NEXORA_KW_ADMIN_PASSWORD_FILE=/work/kw-admin-password go test -count=1 -v -run TestKwSmokeM4 ./e2e/` — expect FAIL with "POST /zones: status 404".
- [ ] In `scripts/kw-deploy.sh`, next to the `nexora-ca` block, create the key-encryption key once without printing it: `if ! k get secret nexora-kek >/dev/null 2>&1; then openssl rand -base64 32 | k create secret generic nexora-kek --from-file=kek=/dev/stdin; fi`. In `deploy/kw/mgmt.yaml` add `- { name: NEXORA_KEK_FILE, value: /etc/nexora/kek/kek }` to the env list, `- { name: kek, mountPath: /etc/nexora/kek, readOnly: true }` to `volumeMounts` and `- name: kek` / `secret: { secretName: nexora-kek, defaultMode: 0440 }` to `volumes` (the pod runs as 65532 with `fsGroup: 65532`, as the `ca` volume). Document `nexora-kek` in `deploy/kw/README.md` under "Secrets" (never in git; losing it makes RPZ TSIG secrets, TSIG keys and KEK-backed DNSSEC keys unreadable) and add the `TestKwSmokeM4` command above to the smoke-test section.
- [ ] Deploy: `scripts/kw-deploy.sh` (builds and pushes both images tagged `sha-<7>`, creates `nexora-kek`, applies `deploy/kw/*.yaml`, runs the idempotent `bootstrap.sh`, waits for `rollout status deployment/nexora-mgmt` and `daemonset/nexora-engine`) — expect both rollouts to complete and the final `NEXORA_KW_DNS_ADDR=192.168.10.136:53` line; migrations `00400`/`00401` run at `nexora-mgmt serve` start.
- [ ] Re-run the smoke command — expect PASS for `TestKwSmokeM4/zones`, `/axfr`, `/dnssec`; then run `-run 'TestKwSmoke$'` with the same environment — expect PASS (M1/M2 behaviour unchanged).
- [ ] Commit: `git add deploy/kw scripts/kw-deploy.sh e2e/kw_smoke_m4_test.go && git commit -m "deploy(kw): key-encryption key secret and M4 smoke subtests"`.
