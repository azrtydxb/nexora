# nexora-v1-m3 — implementation plan

Status: draft
Spec: .procoder/specs/nexora-v1.md

## Goal

Ship milestone M3 "Recursion + validation": full iterative recursion from root hints (S-2), DNSSEC validation of recursive and forwarded answers (S-7), and RPZ policy zones loaded from file or AXFR/IXFR (S-9), wired into the engine query path, the management-plane API, the GUI (`/rpz`, `/dnssec`, resolution settings on `/upstreams`), metrics, the kw deployment, and the acceptance tests `TestRecursionRootHints`, `TestSpoofedReplyRejected`, `TestDNSSECValidation`, `TestRPZPolicy`.

## Architecture

All engine code for M3 lives under `engine/src/recursor/` (per docs/architecture.md): a transport (`transport.rs`: random ID, random source port, 0x20, strict reply matching, TCP on TC, EDNS0 1232 + DO), an infrastructure cache (`infra.rs`), an RRset cache (`rrcache.rs`), root hints (`roothints.rs`), a work budget (`budget.rs`), the iterative resolver (`iterate.rs`), mode/route selection and the miss-path entry point (`dispatch.rs`), DNSSEC (`dnssec/{verify,denial,validator,anchors,nsec_cache}.rs`) and RPZ (`rpz/{parse,index,apply,transfer,tsig,manager}.rs`). hickory-proto 0.26.3 (`dnssec-ring`) is used only as the message codec, zone-text parser and signature primitive (`Verifier::verify_rrsig`, `DS::covers`, `Nsec3HashAlgorithm::hash`); all resolution, chain-of-trust, denial-proof, RFC 5011, RPZ and TSIG logic is Nexora code.

Process-wide state that must survive snapshot swaps (infrastructure cache, RRset cache, key cache, trust-anchor store, RPZ zone data, in-memory RPZ TSIG keys) lives in one `recursor::RecursorState`, held as `server::Shared.recursor: Arc<RecursorState>` (created in `main.rs` from `boot.state_dir`; `Shared::new(workers)` keeps its M1 signature and creates an in-memory state for tests). Per-snapshot settings live in `runtime::Runtime.resolution: Arc<recursor::dispatch::ResolutionRuntime>`, built in `Runtime::build(s, blobs, previous)` and swapped atomically with the rest of the runtime.

The query pipeline after M3, in order (M1/M2 stages unchanged): `wire::parse_query` → ACL (REFUSED) → cookies → M2 `rt.policy.select(client)` / `policy.check(name)` (blocks and rewrites are final; CNAME rewrites keep using `rewrite::run_rewrite_job`, whose `WorkerRewriteCtx::resolve` re-enters `handle_packet`/`resolve_miss`, so rewrite targets are recursed, validated and RPZ-checked) → RPZ query phase (QNAME and CLIENT-IP triggers, only when `shared.recursor.rpz_query_triggers` is set) → cache lookup with `CacheKey::in_partition(&q, policy.cache_partition())` → `FastOutcome::Miss(MissJob)` → `server::resolve_miss` → in-flight coalescing on the partitioned key → `recursor::dispatch::resolve_miss` (route: longest forward zone, else resolution mode) → DNSSEC validation (recursive route and forward zones with `validate`) → RPZ response phase → M2 CNAME-cloaking check against the client's filter (`leader_answer`) → cache insert in the policy partition → `cache::write_cached` with an EDE option when one applies.

The management plane adds migrations `00300_resolution.sql`, `00301_dnssec.sql`, `00302_rpz.sql`, an envelope-encryption helper `mgmt/internal/secrets` (KEK from `NEXORA_KEK_FILE`), OpenAPI operations for resolution settings, forward zones, DNSSEC settings/trust anchors/NTAs/status and RPZ zones, snapshot-builder code filling the new `ConfigSnapshot` fields, delivery of RPZ TSIG secrets as a `ServerMessage.rpz_tsig_keys` control message, and ingestion of the new `Stats` fields. The e2e suite never touches the internet: `nexora-fixture authhier` serves a private hierarchy (fake root `.`, `test.`, leaf zones, signed with miekg/dns under a per-run test trust anchor, plus a spoofing server and a validating-forwarder endpoint) on loopback addresses `127.0.53.0/24` sharing one kernel-chosen port, which the engine reaches through the snapshot's root-hint override and `authority_port`; a BIND `named` child process serves an RPZ zone over AXFR/IXFR with TSIG.

Interfaces consumed from M1/M2 (reconciled against the committed code at `85dbb4b`; if a name here and the code disagree, the code's name wins and the M3 behaviour stays):

- Rust (crate `nexora_engine`): `crate::proto` (`engine/src/proto.rs` = `tonic::include_proto!("nexora.control.v1")`; snake_case fields, prost enums without their prefix, e.g. `proto::ResolutionMode::Recursive`); `runtime::Runtime { version, acl, filter, policy, filter_hashes: Vec<String>, filter_stats, cache: Arc<Cache>, upstreams: Arc<UpstreamSet>, telemetry }` with `Runtime::initial()` and `Runtime::build(s: &ConfigSnapshot, blobs: &dyn BlobSource, previous: Option<&Runtime>) -> Result<Runtime, SnapshotError>` (clears a reused cache when `filter_hashes` changes); `snapshot::{validate(s, applied_version) -> Result<(), SnapshotError>, apply(current: &ArcSwap<Runtime>, s, blobs: &dyn BlobSource, state_dir: Option<&Path>) -> ApplyOutcome, persist, load, BlobSource { fn read(&self, r: &BlobRef) -> Result<Vec<u8>, SnapshotError> }, DirBlobs { dir }, SnapshotError::{Invalid(String), Blob{..}}}`; `control::{run, fetch_blobs(client, snap, blob_dir), session, apply_snapshot}` (the `ServerMsg` match in `session`, the `Stats` ticker calling `shared.metrics.stats(&shared.runtime.load())`); `server::{Shared { runtime, inflight, metrics, querylog, cookie_secret, engine_id, node_name, mgmt_channel }, Shared::new(workers) -> Arc<Shared>, WorkerCtx { index, shared, upstreams: WorkerUpstreams }, handle_packet(ctx, rt, packet, client, transport, out) -> FastOutcome, FastOutcome::{Reply(usize), Drop, Miss(MissJob), Rewrite(RewriteJob)}, MissJob { query, client, transport, key, question, limit, opt, started, filter_us, cache_us }, resolve_miss(ctx: Rc<WorkerCtx>, rt: Arc<Runtime>, job: MissJob) -> Vec<u8>, leader_answer, spawn_workers}`; `filter::{EffectivePolicy::{check, filter, cache_partition}, PolicyTable::select(ip) -> (&EffectivePolicy, Option<u16>), Verdict::{Pass, Allowed, Blocked, Rewrite}}`; `cache::{CacheKey::in_partition(&QueryView, u16), Cache::{lookup, insert(key, upstream, q, now), clear}, Lookup::{Fresh, Stale, Miss}, prepare_uncached(upstream, q), write_cached(entry, q, now, mode, out, limit, opt), ServeMode}`; `wire::{NameKey::as_wire, QueryView { id, flags, qname, key, qtype, qclass, .. }::{rd, cd, do_bit}, parse_query, walk_response, write_rcode_reply, RCODE_*}`; `edns::{ReplyOpt { udp_size, do_bit, ext_rcode, cookie }, ReplyOpt::wire_len, write_opt(out, opt) -> usize, Transport::{Udp, Tcp, Dot, Doh, Doq}}`; `upstream::{forward(set: &UpstreamSet, worker: &WorkerUpstreams, query: &[u8], question: &Question) -> Result<Forwarded { response: Bytes, upstream_index, rtt }, UpstreamError>, Question { key, qtype, qclass }, UpstreamSet, WorkerUpstreams::new(Arc<CachePadded<AtomicU64>>)}`; `telemetry::metrics::Metrics::{render(&self, rt) -> String, stats(&self, rt) -> Stats}` (prometheus-client registry; counters are registered without `_total`, which the encoder appends); `telemetry::querylog::{QueryRecord, CacheOutcome, FilterOutcome, NO_POLICY_GROUP}`; `telemetry::otlp::log_record`; `clock::{now_secs, unix_now}`; `inflight::{InFlight::join, Join::{Leader, Follower}, Resolution}`.
- Rust crates already in `engine/Cargo.toml`: `hickory-proto =0.26.3` (`std`), `tokio =1.53` (`rt`, `rt-multi-thread`, `net`, `time`, `sync`, `macros`, `io-util`, `fs`), `rand 0.10` (`rand::rng()`, `rand::Rng` for `next_u32`, `rand::RngExt`, `rand::SeedableRng`, `rand::rngs::StdRng`), `quick_cache`, `arc-swap`, `rustc-hash`, `ipnet`, `parking_lot`, `zstd`, `serde`, `bytes`, `sha2`, `hex`, `zeroize`, `crossbeam-utils`, `prometheus-client`; dev `tempfile`. There is no `futures` dependency: M3 uses `recursor::LocalBoxFuture<'a, T> = Pin<Box<dyn Future<Output = T> + 'a>>`.
- Go (module `github.com/piwi3910/nexora`): `controlv1` (`gen/go/nexora/control/v1`); `store.Store { Pool }`, `(*Store).InTx`, `store.MapError`, `store.ErrNotFound`/`ErrConflict`, `store.PolicyQuerier`; `snapshot.Mutate(ctx, st, cfg BuildConfig, a auth.Actor, fn func(tx pgx.Tx) (auth.Change, error)) (uint64, error)` (change → `auth.WriteAudit` → `snapshot.Build(ctx, tx, version, cfg)` → `config_versions` (which stores the marshalled snapshot) → `pg_notify('nexora_config', version)`); `auth.Change { Action, TargetType, TargetID string; Before, After any }`; `auth.Permissions map[string]Role` with `auth.RoleViewer`/`RoleOperator`/`RoleAdmin`, mirrored in `web/src/auth/permissions.ts` (checked by `web/scripts/check-permissions.mjs`); API: `api.Deps`, `type handlers struct{ d Deps }`, `(h *handlers) mutate(ctx, fn)`, `invalid(format, args...)` (400 `invalid_request`), `mapError`, `writeError(w, status, code, message)`, request bodies capped at `maxBodyBytes` (4 MiB), generated `<Op>RequestObject`/`<Op><status>JSONResponse`; test helpers in `mgmt/internal/api` (`newAPI(t)`, `(*apiEnv).client(t)`, `(*client).do(method, path, body, out) int`, `roleClients(t) (operator, viewer *client)`, `apiErr`); `control.Hub` (`NewHub`, `Run`, `broadcast`, `subscriber` with a capacity-1 `out`), `control.Server` (`NewServer(st, ca, hub, instanceID, dnsTLS)`, field `OnStats func(ctx context.Context, engineID string, s *controlv1.Stats)`, `Connect` send loop), `control.DNSTLSFanout` (the M2 in-memory key-delivery pattern); `stats.Record(ctx, st, engineID, s)`; `config.Load(getenv)`; migrations `mgmt/migrations/00001_init.sql`, `00200_policy_rewrites_tls.sql` (tables `blobs(sha256, size, data, created_at)`, `engines(id, node_name, ..)`).
- e2e harness (package `harness`): `harness.New(t) *Env` (fields `T`, `Dir`), `(*Env).StartPostgres() *Postgres` (field `URL`), `(*Env).InitCA() *CA`, `(*Env).StartMgmt(pg, ca, MgmtOptions{ExtraEnv []string, ..}) *Mgmt` (fields `BaseURL`, `GRPCURL`), `(*Mgmt).SetupToken(t)`, `harness.Bootstrap(t, env, setupToken, baseURL) *API`, `(*API).Must(method, path, body, out any, want int)` / `Do(method, path, body, out) (int, error)` with `path` relative to `/api/v1`, `(*API).CreateJoinToken()`, `(*API).LatestVersion()`, `(*API).WaitEngine(nodeName, timeout, cond func(EngineView) bool)`, `(*Env).StartManagedEngine(nodeName, grpcURLs, joinToken) *Engine` (fields `DNS`, `Metrics`, `StateDir`, `Proc`), `(*Engine).Metric(t, name, labels) float64`, `(*Env).Start(name, args, env []string) *Proc`, `(*Proc).WaitReady(timeout) map[string]string` / `Addr(ready, key)` / `Stop()` / `Signal(sig)`, `(*Env).Bin(name)`, `(*Env).FreePort() int`, `harness.Eventually(t, timeout, cond func() error)`, `harness.RunPlaywright`; fixture binary `e2e/fixtures/cmd/nexora-fixture/main.go` dispatching `os.Args[1]` in a `switch` to `runX(args []string) (stop func(), addrs string, err error)` and printing `READY <key=addr ...>`; `TestGUICoverage` (`e2e/gui_test.go`) runs `web/e2e/screens/[01][0-9]-*.spec.ts` and counts the browser's recorded `/api/v1` requests (`web/e2e/fixtures.ts` exports `test`, `expect`, `env`, `login`, `logout`; users `NEXORA_E2E_ADMIN_USER`, `NEXORA_E2E_OPERATOR_USER`, `NEXORA_E2E_VIEWER_USER` with `_PASSWORD`; the destructive confirm button has `data-testid="confirm-delete"`; nav links `data-testid="nav-<route>"`; selects are Radix `Select`s opened by `click()` then `getByRole("option")`). M2 screen specs end at `14-settings-dns-tls.spec.ts`.
- kw: namespace `nexora`; `deploy/kw/engine.yaml` (ConfigMap `nexora-engine-config`, Deployment `nexora-engine` labelled `app.kubernetes.io/name: nexora-engine`, `state` volume `emptyDir` at `/var/lib/nexora`, Services `nexora-dns` (192.168.10.136) and `nexora-engine-metrics`), `deploy/kw/mgmt.yaml`, image placeholder `NEXORA_TAG` substituted by `scripts/kw-deploy.sh [--tag TAG] [--skip-build]`; `TestKwSmoke` in `e2e/kw_smoke_test.go` run from the dev pod with `NEXORA_KW_DNS_ADDR` and `NEXORA_KW_API_URL`; admin credentials in Secret `nexora-admin`.
- M4 (`.procoder/plans/nexora-v1-m4.md` Task 6) defines the key-storage design M3 pulls forward: NXE1 envelope, file KEK = 32 bytes base64 in `NEXORA_KEK_FILE`, `kek_id = SHA-256(KEK)[0:8]`, errors `ErrUnconfigured`/`ErrKEKMismatch`/`ErrBackendUnavailable`, 503 `key_storage_unconfigured`, and `harness.WriteKEK(t)`.

Reconciled with M1/M2 code (design-level changes to the original M3 plan):

1. **RPZ TSIG secrets never enter `ConfigSnapshot`.** `config_versions` stores every marshalled snapshot in PostgreSQL and the engine persists `snapshot.binpb`, so the original `RpzTransferSource.tsig_secret` (plus `strip_key_material`) would have put key material in the database. The secret is sealed with `mgmt/internal/secrets` (the NXE1 envelope of M4 Task 6, file-KEK wrap `1`; without `NEXORA_KEK_FILE` the API refuses with 503 `key_storage_unconfigured`) into `rpz_zones.tsig_secret_envelope`, and delivered to engines only as `ServerMessage.rpz_tsig_keys = 100` (`RpzTsigKeys`), following M2's `TlsMaterial` pattern: held in engine memory (`Zeroizing`), never written to `state_dir`, never logged. M4 Task 6 builds `keystore.Store` on `secrets.Box` (same layout, errors and purposes format), so envelopes sealed in M3 stay readable.
2. **Global forwarding keeps M1's path.** `Route::Forward` sends the client's query bytes through `upstream::forward` exactly as M1 does, and answers from the global upstreams are not DNSSEC-validated; validation applies to the recursive route and to forward zones with `validate = true`. With validation on by default, validating global forwards would SERVFAIL every M1/M2 managed-engine test (their fixture upstream serves synthetic unsigned data under the real root anchor) and would change the forwarded bytes the perf gate measures.
3. **Resolution, forward zones, DNSSEC and RPZ are global, not per policy group.** M2 policy decisions (block, rewrite) run first and are final; RPZ applies to every client whose verdict is `Pass` or `Allowed`; recursion answers are cached in the client's M2 cache partition like forwarded answers, so the CNAME-cloaking check keeps its per-partition meaning. Answers changed by RPZ, and misses carrying a query-phase RPZ decision, bypass in-flight coalescing and are never cached.
4. **No `policy_generation` stamp on `CachedResponse`.** `Runtime::build` appends `r:<ResolutionRuntime::config_key>` to `filter_hashes`, so M2's existing clear-on-change also clears the cache when resolution, forward zones, DNSSEC settings, NTAs or RPZ configuration change; an RPZ transfer that publishes new data clears `shared.runtime.load().cache`; a leader skips its cache insert when `shared.recursor.rpz` or `shared.runtime` changed while it resolved. The cache-hit path gains one `AtomicBool` load when no RPZ zone has query triggers, and nothing else.
5. **RPZ file zones are ordinary blobs.** `RpzFileSource` carries a `BlobRef` fetched by `control::fetch_blobs` and parsed in `Runtime::build` (a bad zone rejects the snapshot, as M2's policy blobs do). `snapshot::apply` keeps its signature; after every applied snapshot the three call sites (`control::apply_snapshot`, and `main.rs` for the persisted and standalone snapshots) call `shared.recursor.sync(&runtime)`, which merges trust anchors and hands RPZ configuration to the `RpzManager`. Trust-anchor refresh and RPZ transfers run on one background thread `nexora-recursor` (a `current_thread` runtime), because resolution futures are `!Send` like the workers'.
6. **EDE on the wire carries the INFO-CODE only.** `ReplyOpt` gains `ede: Option<u16>` so it stays `Copy` and allocation-free; `recursor::dispatch::Ede { code, text }` keeps the text for tests and logs.
7. **RPZ upload size.** API bodies are capped at 4 MiB (`maxBodyBytes`), so `RpzZoneFile.content` is limited to 3 MiB (JSON escaping headroom) instead of 50 MiB.
8. **kw keeps the M1 engine state layout.** Engine `state_dir` stays an `emptyDir` (plan-review: M5 moves it to `hostPath`; a shared `hostPath` under a rolling Deployment would let two pods share one identity), so RFC 5011 state and RPZ last-good copies do not survive an engine pod restart on kw until M5 (the configured DS anchors and a fresh transfer take over). kw has no NetworkPolicy, and none is added: an Egress-type policy selecting the engines would also cut their management-plane and OTLP connections.
9. **Harness and fixtures follow M1's READY-line design.** `nexora-fixture authhier` binds its shared port itself (port 0 on the first address, then the others, retrying on `EADDRINUSE`) and prints `READY port=.. stats=..` instead of receiving a pre-picked port; BIND cannot listen on port 0, so `StartNamed` uses `(*Env).FreePort()` and retries on a bind failure. Acceptance tests use M1's bootstrap flow (`StartPostgres`, `InitCA`, `StartMgmt`, `Bootstrap`, `StartManagedEngine`, `WaitEngine`) with `NEXORA_KEK_FILE` from `harness.WriteKEK(t)`.
10. **GUI coverage is request-based.** The Playwright tests are `web/e2e/screens/15-rpz.spec.ts`, `16-dnssec.spec.ts` and `17-resolution.spec.ts`, run by `TestGUICoverage` (whose management plane has no KEK, so the RPZ screen test asserts the 503 message and saves a transfer zone without TSIG); no annotations are used.

Decisions made in this plan that docs/architecture.md does not settle (Task 1 records the contract, key-material and engine-state ones there):

1. Snapshot field numbers 100–104 (`ConfigSnapshot`), 100–102 (`Stats`) and 100 (`ServerMessage.rpz_tsig_keys`); new messages number from 1.
2. `RecursionConfig.authority_port` (default 53) lets the private e2e hierarchy run on loopback without binding port 53; root-hint override is `RecursionConfig.root_hints`.
3. Forward zones carry their own `ip:port` servers (UDP with TCP fallback via the recursor transport) and a per-zone `validate` flag (default off, so internal zones below signed public names do not go bogus). A forward zone beats the resolution mode in both modes.
4. Resolution deadline 4000 ms; unknown-server RTO 376 ms; RFC 6298 RTO clamp 50–3000 ms; backoff after 3 consecutive timeouts `5 s << (n-3)` capped at 300 s; lame marks 900 s; server choice random within 400 ms of the best RTO; glueless NS resolution capped at 3 names per cut.
5. 0x20 is strict: a case-mismatched reply is dropped, never retried without 0x20. Source ports are chosen randomly in 1024–65535 in user space before `connect`. Randomness comes from `rand::rng()` (the OS-seeded CSPRNG M1's upstream IDs use).
6. QNAME minimisation uses QTYPE A for hidden labels and falls back to the full name on NXDOMAIN for an intermediate name (relaxed RFC 9156 mode).
7. DNSSEC: signature skew = 10% of the RRSIG lifetime clamped to 300–3600 s; algorithms exactly {8, 13, 14, 15} (RSASHA512 and others → insecure); DS digests SHA-1/256/384 with SHA-1 ignored when a stronger digest exists; NSEC3 iterations > 50 → insecure (EDE 27), > 150 → bogus.
8. CD=1 skips validation entirely; AD is set only when the query had DO or AD; the cached wire keeps AD and `cache::write_cached` clears it for clients that set neither DO nor AD.
9. Trust anchors from mgmt are DS records; IANA KSK-2017 (20326) and KSK-2024 (38696) are seeded by migration; RFC 5011 state lives in `state_dir/trust-anchors.json`; 5011-learned `Valid` keys survive removal of the mgmt DS; the GUI raises a rollover alarm after 72 h without a successful refresh or with no usable root key.
10. NTAs require an expiry at most 30 days ahead; expired NTAs are deleted by a mgmt job so a new config version is pushed.
11. RPZ runs after Nexora's blocklists/policies/rewrites; RPZ rewrites always apply (no "break-dnssec" exception) and clear AD; NSDNAME/NSIP triggers only fire in recursive mode; RPZ-rewritten answers are not cached; cache invalidation is change 4 above.
12. RPZ EDE codes: 15 (Blocked) for NXDOMAIN/NODATA, 4 (Forged Answer) for local data.
13. RPZ file zones are zstd-compressed zone text blobs validated by mgmt with miekg/dns (`$INCLUDE` refused on both sides); transfer zones persist the last good copy as `state_dir/rpz/<id>.zone`; an expired zone is marked stale but keeps serving.
14. TSIG (HMAC-SHA256/512) is implemented in Nexora code over `ring::hmac`; the secret is sealed in `rpz_zones.tsig_secret_envelope` with purpose `nexora/rpz-tsig/v1:<zone uuid>`, write-only in the API, absent from audit rows and snapshots, and delivered by `RpzTsigKeys` (change 1).
15. Resolution settings and forward zones are managed on the existing `/upstreams` screen; RPZ zone order is an explicit `position` with a reorder operation.
16. Migrations `00300_resolution.sql`, `00301_dnssec.sql`, `00302_rpz.sql`; per-engine DNSSEC/RPZ status is upserted into `engine_dnssec_status` / `engine_rpz_status` from every `Stats` message.
17. The e2e hierarchy runs on `127.0.53.0/24` from `nexora-fixture authhier` (miekg/dns signing, ECDSA P-256), with a spoofing server and a validating-forwarder endpoint; the RPZ primary is a real BIND `named` child process with TSIG and `ixfr-from-differences`.
18. kw: engine state stays `emptyDir` until M5 (change 8); the M3 smoke test is `TestKwSmokeM3` in `e2e/kw_smoke_m3_test.go`, skipped unless the kw environment variables are set, like M1's `TestKwSmoke`.

## Constraints

Copied verbatim from `.procoder/specs/nexora-v1.md` and `docs/architecture.md`:

- DNS engine: Rust, hickory-proto used as wire codec only; server loop, cache and resolver are Nexora code.
- Platform: engine targets Linux only (free to use SO_REUSEPORT, recvmmsg, io_uring and other Linux-specific APIs). Management plane and GUI ship as Linux builds/containers.
- Management plane: Go.
- GUI: React on Vite (not Next.js).
- Management plane is stateless: all state in PostgreSQL, any number of instances behind a load balancer, engines may connect to any instance.
- Security: DNS upstream queries use random transaction IDs and random source ports and match responses on address + ID + question (fixes dns-c's poisoning hole); resolvers refuse recursion to clients outside configured ACLs by default; no default passwords (first admin set at install).
- The query path never logs synchronously, never touches a database, and never performs per-packet heap allocation on the cache-hit path.
- **Engine local state:** last applied config snapshot on disk (without key material); in-memory cache; DNSSEC trust anchors.
- **Name case:** cache and policy keys are case-insensitive; 0x20-randomised queries get the client's casing back.
- **CNAME/DNAME chains:** followed to a bounded depth; loops → SERVFAIL.
- **DNSSEC:** unsupported algorithms → insecure, not bogus; clock skew near signature validity edges; NSEC3 with excessive iterations treated per RFC 9276.
- **Root/authoritative servers unreachable or lame:** marked in an infrastructure cache with backoff; recursion tries remaining servers.
- **Blocklist or RPZ source fetch fails:** keep last good version, surface staleness in GUI and metrics.
- **Trust anchor rollover:** RFC 5011 automated updates; failure alerts before the old anchor expires.
- Every test that asserts "does not happen" first asserts the positive path in the same run, so a harness failure cannot pass a negative check.
- No logging, no allocation, no locks held across packets on the cache-hit path. Enforced by `cache_hit_path_does_not_allocate` (counting allocator).
- Config is read through `ArcSwap<Runtime>::load()` once per packet.
- The engine is Linux-only. All builds and tests run in the dev pod on kw (`deploy/dev/`), image `192.168.10.131/azrtydxb/nexora-dev:<tag>`. Source is synced with `scripts/dev-sync.sh` (rsync over `kubectl exec`), commands run with `scripts/dev-exec.sh <cmd>`.
- HTTP API: `/api/v1`, OpenAPI 3.1 at `mgmt/api/openapi.yaml`, JSON errors `{"code": "...", "message": "..."}`. Editable resources carry `revision`; a stale revision returns 409 `conflict`.
- Every config mutation runs in one transaction: change rows -> write audit row -> build snapshot -> insert `config_versions` -> `pg_notify('nexora_config', version)`.
- Roles: `viewer` (read everything except users, tokens, audit), `operator` (+ DNS configuration mutations), `admin` (everything). Permissions keyed by OpenAPI operationId in `mgmt/internal/auth/permissions.go`.
- Pinned versions for this milestone: Rust 1.97, hickory-proto 0.26.3 with features `dnssec-ring`, ring 0.17.14, Go 1.27, miekg/dns v1.1.73.

Plan-wide conventions (decided here):

- Every command step runs from the repository root on the laptop as `scripts/dev-exec.sh <cmd>` (which syncs the tree into the dev pod first). Engine commands use the workspace form `cargo test --locked -p nexora-engine ...`.
- Generated sources are regenerated with the pinned toolchains in the dev pod and copied back to the laptop, as M2 did: protobuf with `scripts/dev-exec.sh 'protoc -I proto --go_out=gen/go --go_opt=paths=source_relative --go-grpc_out=gen/go --go-grpc_opt=paths=source_relative proto/nexora/control/v1/control.proto'`, the HTTP server with `scripts/dev-exec.sh 'cd mgmt/api && oapi-codegen -config oapi-codegen.yaml openapi.yaml'`, each followed by a copy back of the generated paths, e.g. `kubectl --context kw -n nexora-dev exec -i deploy/toolbox -c toolbox -- tar -C /work/nexora -cf - gen/go/nexora/control/v1 mgmt/internal/api/gen.go | tar -xf -`; `web/src/api/schema.d.ts` with `pnpm --dir web run gen:api` on the laptop.
- New `ConfigSnapshot` fields use numbers 100–104, new `Stats` fields 100–102 and the new `ServerMessage` field 100 (M3's range 100–199; M2 owns 300–399, M4 200–299, M5 500+).
- Resolution deadline for one client query in recursive mode: 4000 ms (`recursor::RESOLUTION_DEADLINE`). Outgoing EDNS buffer: 1232 (`recursor::EDNS_BUFFER`). CNAME/DNAME depth: 16 (`recursor::MAX_CNAME_DEPTH`).
- Migrations for M3 are `mgmt/migrations/00300_resolution.sql`, `00301_dnssec.sql`, `00302_rpz.sql` (goose, `-- +goose Up` / `-- +goose Down`).

## Task 1: Proto contract and engine snapshot validation for M3 fields

Files:

- `proto/nexora/control/v1/control.proto` — new messages/enums, `ConfigSnapshot` fields 100–104, `Stats` fields 100–102, `ServerMessage.rpz_tsig_keys = 100`.
- `gen/go/nexora/control/v1/control.pb.go` — regenerated in the dev pod and copied back (`control_grpc.pb.go` is unchanged because the service is unchanged).
- `mgmt/internal/control/contract_m3_test.go` — field-number and key-isolation guard test.
- `engine/src/snapshot_m3.rs` — `validate_m3` (validation of the new fields only) and `parse_ds`.
- `engine/src/snapshot.rs` — `validate` calls `validate_m3`; `is_sha256_hex` becomes `pub(crate)`.
- `engine/src/control.rs` — `fetch_blobs` also fetches RPZ file blobs; the `ServerMsg` match in `session` gains a no-op `RpzTsigKeys` arm (Task 10 replaces it).
- `engine/src/lib.rs` — `pub mod recursor;` and `pub mod snapshot_m3;`.
- `engine/src/recursor/mod.rs` — module declarations, M3 constants and `LocalBoxFuture`.
- `engine/Cargo.toml` — `hickory-proto` features `["std", "dnssec-ring"]` (in `[dependencies]` and `[dev-dependencies]`), `ring = "=0.17.14"`, `data-encoding = "2"`, `serde_json = "1"`.
- `docs/architecture.md` — M3 contract, key-material and state-directory decisions.

Interfaces:

- `pub fn snapshot_m3::validate_m3(s: &proto::ConfigSnapshot) -> Result<(), String>`
- `pub fn snapshot_m3::parse_ds(text: &str) -> Result<(u16, u8, u8, Vec<u8>), String>` — `"<key tag> <algorithm> <digest type> <hex digest>"`.
- `recursor::{RESOLUTION_DEADLINE: Duration = 4000 ms, EDNS_BUFFER: u16 = 1232, MAX_CNAME_DEPTH: u8 = 16, DEFAULT_MAX_UPSTREAM_QUERIES: u32 = 100, DEFAULT_MAX_DELEGATION_DEPTH: u32 = 32}`
- `pub type recursor::LocalBoxFuture<'a, T> = std::pin::Pin<Box<dyn std::future::Future<Output = T> + 'a>>;`
- Go: `controlv1.ResolutionMode_RESOLUTION_MODE_RECURSIVE`, `controlv1.RecursionConfig`, `controlv1.ForwardZone`, `controlv1.DnssecConfig`, `controlv1.RpzZone` (oneof `RpzZone_File` / `RpzZone_Transfer`), `controlv1.RpzTsigKeys`, `controlv1.ServerMessage_RpzTsigKeys`, `controlv1.Stats.Recursion/Dnssec/RpzZones`.
- Proto (literal, appended to `control.proto`):

```proto
// ---------------------------------------------------------------------------
// M3: recursion, DNSSEC validation, RPZ. Fields added to earlier messages use numbers 100-199.
// ---------------------------------------------------------------------------

enum ResolutionMode {
  RESOLUTION_MODE_UNSPECIFIED = 0; // engine treats as FORWARD
  RESOLUTION_MODE_FORWARD = 1;
  RESOLUTION_MODE_RECURSIVE = 2;
}

message RootHint {
  string name = 1;               // e.g. "a.root-servers.net."
  repeated string addresses = 2; // IPv4 or IPv6 literals, no port
}

message RecursionConfig {
  repeated RootHint root_hints = 1; // empty: compiled-in IANA hints
  bool qname_minimisation = 2;      // RFC 9156
  bool aggressive_nsec = 3;         // RFC 8198, off by default
  uint32 max_upstream_queries = 4;  // 0 -> 100; valid 1..=1000
  uint32 max_delegation_depth = 5;  // 0 -> 32; valid 1..=64
  uint32 authority_port = 6;        // 0 -> 53; port used for every authoritative server
}

message ForwardZone {
  string domain = 1;             // "corp.example."
  repeated string addresses = 2; // "ip:port", UDP with TCP fallback on TC
  bool validate = 3;             // false: answers under this zone are treated as insecure
}

message TrustAnchor {
  string zone = 1; // "."
  string ds = 2;   // "20326 8 2 E06D44B8..."
}

message NegativeTrustAnchor {
  string domain = 1;
  int64 expires_unix = 2;
}

message DnssecConfig {
  bool validation = 1;
  repeated TrustAnchor trust_anchors = 2;
  repeated NegativeTrustAnchor negative_trust_anchors = 3;
  bool rfc5011 = 4;
}

enum RpzPolicyOverride {
  RPZ_POLICY_OVERRIDE_GIVEN = 0;
  RPZ_POLICY_OVERRIDE_DISABLED = 1; // match counted, no action
  RPZ_POLICY_OVERRIDE_NXDOMAIN = 2;
  RPZ_POLICY_OVERRIDE_NODATA = 3;
  RPZ_POLICY_OVERRIDE_PASSTHRU = 4;
  RPZ_POLICY_OVERRIDE_DROP = 5;
  RPZ_POLICY_OVERRIDE_TCP_ONLY = 6;
}

enum TsigAlgorithm {
  TSIG_ALGORITHM_NONE = 0;
  TSIG_ALGORITHM_HMAC_SHA256 = 1;
  TSIG_ALGORITHM_HMAC_SHA512 = 2;
}

message RpzFileSource {
  BlobRef blob = 1; // zstd-compressed zone text, fetched via GetBlob like filter lists
}

message RpzTransferSource {
  string primary = 1; // "ip:port"
  string tsig_key_name = 2;
  TsigAlgorithm tsig_algorithm = 3; // the secret arrives only in RpzTsigKeys
  uint32 min_refresh_seconds = 4;   // 0 -> 60
}

message RpzZone {
  string id = 1;   // UUID
  string name = 2; // zone origin, "rpz.example."
  oneof source {
    RpzFileSource file = 3;
    RpzTransferSource transfer = 4;
  }
  RpzPolicyOverride policy_override = 5;
  uint64 refresh_nonce = 6; // incremented by POST /rpz-zones/{id}/refresh
}

// TSIG secrets of RPZ transfer zones. Sent only on the Connect stream; held in engine memory only;
// never part of ConfigSnapshot, never persisted by the engine.
message RpzTsigKey {
  string zone_id = 1; // RpzZone.id
  string key_name = 2;
  TsigAlgorithm algorithm = 3;
  bytes secret = 4;
}

message RpzTsigKeys {
  repeated RpzTsigKey keys = 1; // the complete set; an absent zone has no key
}

message RecursionStats {
  uint64 upstream_queries = 1;
  uint64 mismatched_replies = 2;
  uint64 tcp_fallbacks = 3;
  uint64 work_limit_exceeded = 4;
  uint32 infra_entries = 5;
  uint64 lame_marked = 6;
}

enum TrustAnchorState {
  TRUST_ANCHOR_STATE_UNSPECIFIED = 0;
  TRUST_ANCHOR_STATE_CONFIGURED = 1;
  TRUST_ANCHOR_STATE_ADD_PEND = 2;
  TRUST_ANCHOR_STATE_VALID = 3;
  TRUST_ANCHOR_STATE_MISSING = 4;
  TRUST_ANCHOR_STATE_REVOKED = 5;
}

message TrustAnchorStatus {
  string zone = 1;
  uint32 key_tag = 2;
  uint32 algorithm = 3;
  TrustAnchorState state = 4;
  int64 last_refresh_success_unix = 5;
  int64 hold_down_until_unix = 6;
  string last_error = 7;
}

message DnssecStats {
  uint64 secure = 1;
  uint64 insecure = 2;
  uint64 bogus = 3;
  uint64 indeterminate = 4;
  repeated TrustAnchorStatus trust_anchors = 5;
  uint32 active_negative_trust_anchors = 6;
}

message RpzZoneStatus {
  string id = 1;
  uint32 serial = 2;
  uint64 records = 3;
  uint64 skipped = 4;
  uint64 hits = 5;
  int64 last_success_unix = 6;
  string last_error = 7;
  bool stale = 8;
}
```

and, inside the existing messages (M1/M2 fields unchanged):

```proto
message ServerMessage {
  oneof msg {
    ConfigSnapshot snapshot = 1;
    VersionAhead version_ahead = 2;
    RpzTsigKeys rpz_tsig_keys = 100;   // M3: never persisted, never part of ConfigSnapshot
    TlsMaterial tls_material = 300;    // M2: never persisted, never part of ConfigSnapshot
  }
}

message ConfigSnapshot {
  // ... M1 fields 1-8 and M2 fields 300-302 unchanged ...
  ResolutionMode resolution_mode = 100;
  RecursionConfig recursion = 101;
  repeated ForwardZone forward_zones = 102;
  DnssecConfig dnssec = 103;
  repeated RpzZone rpz_zones = 104; // ordered: index 0 has the highest precedence
}

message Stats {
  // ... M1 fields 1-14 unchanged ...
  RecursionStats recursion = 100;
  DnssecStats dnssec = 101;
  repeated RpzZoneStatus rpz_zones = 102;
}
```

- [ ] Write the failing guard test `mgmt/internal/control/contract_m3_test.go`:

```go
package control_test

import (
	"testing"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestM3ContractFieldNumbers(t *testing.T) {
	snap := (&controlv1.ConfigSnapshot{}).ProtoReflect().Descriptor()
	stats := (&controlv1.Stats{}).ProtoReflect().Descriptor()
	srv := (&controlv1.ServerMessage{}).ProtoReflect().Descriptor()
	cases := []struct {
		msg   protoreflect.MessageDescriptor
		field protoreflect.Name
		num   protoreflect.FieldNumber
	}{
		{snap, "resolution_mode", 100},
		{snap, "recursion", 101},
		{snap, "forward_zones", 102},
		{snap, "dnssec", 103},
		{snap, "rpz_zones", 104},
		{stats, "recursion", 100},
		{stats, "dnssec", 101},
		{stats, "rpz_zones", 102},
		{srv, "rpz_tsig_keys", 100},
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
}

// The snapshot is stored in config_versions and persisted on engine disk: it must never be able to
// carry RPZ TSIG secrets.
func TestSnapshotCannotReachRpzTsigKeys(t *testing.T) {
	seen := map[protoreflect.FullName]bool{}
	var walk func(m protoreflect.MessageDescriptor)
	walk = func(m protoreflect.MessageDescriptor) {
		if seen[m.FullName()] {
			return
		}
		seen[m.FullName()] = true
		if m.FullName() == "nexora.control.v1.RpzTsigKeys" || m.FullName() == "nexora.control.v1.RpzTsigKey" {
			t.Fatalf("ConfigSnapshot reaches %s", m.FullName())
		}
		fields := m.Fields()
		for i := 0; i < fields.Len(); i++ {
			if name := fields.Get(i).Name(); name == "secret" || name == "tsig_secret" {
				t.Fatalf("%s has a %s field", m.FullName(), name)
			}
			if sub := fields.Get(i).Message(); sub != nil {
				walk(sub)
			}
		}
	}
	walk((&controlv1.ConfigSnapshot{}).ProtoReflect().Descriptor())
	if !seen["nexora.control.v1.RpzTransferSource"] {
		t.Fatalf("walk did not reach RpzTransferSource; the positive path is broken")
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/control/ -run 'TestM3ContractFieldNumbers|TestSnapshotCannotReachRpzTsigKeys'` — expect FAIL with "nexora.control.v1.ConfigSnapshot.resolution_mode missing".
- [ ] Add the proto definitions above to `proto/nexora/control/v1/control.proto` and regenerate Go code with the pinned toolchain in the dev pod: `scripts/dev-exec.sh 'protoc -I proto --go_out=gen/go --go_opt=paths=source_relative --go-grpc_out=gen/go --go-grpc_opt=paths=source_relative proto/nexora/control/v1/control.proto'`, then `kubectl --context kw -n nexora-dev exec -i deploy/toolbox -c toolbox -- tar -C /work/nexora -cf - gen/go/nexora/control/v1 | tar -xf -` — expect exit 0 and a diff in `gen/go/nexora/control/v1/control.pb.go` containing `ResolutionMode_RESOLUTION_MODE_RECURSIVE` whose header still names `protoc-gen-go v1.36.12` and `protoc v3.21.12`.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/control/ -run 'TestM3ContractFieldNumbers|TestSnapshotCannotReachRpzTsigKeys'` — expect PASS.
- [ ] Keep the engine compiling against the regenerated types (prost regenerates through `engine/build.rs`): in `engine/src/control.rs` `session`, change the arm `None => {}` of `match msg.msg` to `Some(ServerMsg::RpzTsigKeys(_)) | None => {}`; in `fetch_blobs`, chain the RPZ file blobs into `refs`: `.chain(snap.rpz_zones.iter().filter_map(|z| match &z.source { Some(crate::proto::rpz_zone::Source::File(f)) => f.blob.as_ref(), _ => None }))`. Run `scripts/dev-exec.sh cargo build --locked -p nexora-engine --all-targets` — expect exit 0 (every M1/M2 `ConfigSnapshot` literal uses `..Default::default()`).
- [ ] Create `engine/src/snapshot_m3.rs` containing only the test module below (and add `pub mod snapshot_m3;` to `engine/src/lib.rs`):

```rust
#[cfg(test)]
mod tests {
    use super::*;
    use crate::proto::{
        BlobRef, ConfigSnapshot, DnssecConfig, ForwardZone, NegativeTrustAnchor, RecursionConfig,
        RootHint, RpzFileSource, RpzTransferSource, RpzZone, TrustAnchor, TsigAlgorithm,
        rpz_zone::Source,
    };

    fn ok_snapshot() -> ConfigSnapshot {
        ConfigSnapshot {
            resolution_mode: crate::proto::ResolutionMode::Recursive as i32,
            recursion: Some(RecursionConfig {
                root_hints: vec![RootHint { name: "a.root.test.".into(), addresses: vec!["127.0.53.1".into(), "::1".into()] }],
                qname_minimisation: true,
                aggressive_nsec: false,
                max_upstream_queries: 100,
                max_delegation_depth: 32,
                authority_port: 5353,
            }),
            forward_zones: vec![ForwardZone { domain: "corp.example.".into(), addresses: vec!["10.0.0.1:53".into()], validate: false }],
            dnssec: Some(DnssecConfig {
                validation: true,
                trust_anchors: vec![TrustAnchor { zone: ".".into(), ds: "20326 8 2 E06D44B80B8F1D39A95C0B0D7C65D08458E880409BBC683457104237C7F8EC8D".into() }],
                negative_trust_anchors: vec![NegativeTrustAnchor { domain: "broken.example.".into(), expires_unix: 4_000_000_000 }],
                rfc5011: true,
            }),
            rpz_zones: vec![
                RpzZone { id: "11111111-1111-1111-1111-111111111111".into(), name: "rpz.file.".into(), source: Some(Source::File(RpzFileSource { blob: Some(BlobRef { sha256: "a".repeat(64), size: 10, name: "rpz.file.".into() }) })), policy_override: 0, refresh_nonce: 0 },
                RpzZone { id: "22222222-2222-2222-2222-222222222222".into(), name: "rpz.axfr.".into(), source: Some(Source::Transfer(RpzTransferSource { primary: "127.0.0.1:5300".into(), tsig_key_name: "rpz-key.".into(), tsig_algorithm: TsigAlgorithm::HmacSha256 as i32, min_refresh_seconds: 0 })), policy_override: 0, refresh_nonce: 0 },
            ],
            ..Default::default()
        }
    }

    #[test]
    fn accepts_valid_m3_fields() {
        assert_eq!(validate_m3(&ok_snapshot()), Ok(()));
    }

    #[test]
    fn rejects_bad_root_hint_address() {
        let mut s = ok_snapshot();
        s.recursion.as_mut().unwrap().root_hints[0].addresses[0] = "127.0.53.1:53".into();
        assert_eq!(validate_m3(&s).unwrap_err(), "recursion.root_hints[0].addresses[0]: not an IP address: 127.0.53.1:53");
    }

    #[test]
    fn rejects_limits_out_of_range() {
        let mut s = ok_snapshot();
        s.recursion.as_mut().unwrap().max_upstream_queries = 1001;
        assert_eq!(validate_m3(&s).unwrap_err(), "recursion.max_upstream_queries: 1001 not in 1..=1000");
        let mut s = ok_snapshot();
        s.recursion.as_mut().unwrap().max_delegation_depth = 65;
        assert_eq!(validate_m3(&s).unwrap_err(), "recursion.max_delegation_depth: 65 not in 1..=64");
    }

    #[test]
    fn rejects_duplicate_forward_zone() {
        let mut s = ok_snapshot();
        s.forward_zones.push(ForwardZone { domain: "CORP.example.".into(), addresses: vec!["10.0.0.2:53".into()], validate: false });
        assert_eq!(validate_m3(&s).unwrap_err(), "forward_zones[1].domain: duplicate corp.example.");
    }

    #[test]
    fn rejects_forward_zone_without_port() {
        let mut s = ok_snapshot();
        s.forward_zones[0].addresses[0] = "10.0.0.1".into();
        assert_eq!(validate_m3(&s).unwrap_err(), "forward_zones[0].addresses[0]: not ip:port: 10.0.0.1");
    }

    #[test]
    fn rejects_malformed_trust_anchor() {
        let mut s = ok_snapshot();
        s.dnssec.as_mut().unwrap().trust_anchors[0].ds = "20326 8 2 ZZ".into();
        assert_eq!(validate_m3(&s).unwrap_err(), "dnssec.trust_anchors[0].ds: digest is not hex");
    }

    #[test]
    fn rejects_rpz_without_source_bad_blob_and_keyless_tsig() {
        let mut s = ok_snapshot();
        s.rpz_zones[0].source = None;
        assert_eq!(validate_m3(&s).unwrap_err(), "rpz_zones[0]: no source");
        let mut s = ok_snapshot();
        if let Some(Source::File(f)) = s.rpz_zones[0].source.as_mut() { f.blob.as_mut().unwrap().sha256 = "A".repeat(64); }
        assert_eq!(validate_m3(&s).unwrap_err(), "rpz_zones[0].file.blob.sha256: must be 64 lowercase hex");
        let mut s = ok_snapshot();
        if let Some(Source::Transfer(t)) = s.rpz_zones[1].source.as_mut() { t.tsig_key_name = String::new(); }
        assert_eq!(validate_m3(&s).unwrap_err(), "rpz_zones[1].transfer.tsig_key_name: required with tsig_algorithm");
    }

    #[test]
    fn rejects_duplicate_rpz_id() {
        let mut s = ok_snapshot();
        s.rpz_zones[1].id = s.rpz_zones[0].id.clone();
        assert_eq!(validate_m3(&s).unwrap_err(), "rpz_zones[1].id: duplicate 11111111-1111-1111-1111-111111111111");
    }
}
```

- [ ] Add `hickory-proto` feature `dnssec-ring` (both entries), `ring = "=0.17.14"`, `data-encoding = "2"` and `serde_json = "1"` to `engine/Cargo.toml`; record them in the workspace lock file with `scripts/dev-exec.sh cargo fetch` (without `--locked`; it adds `serde_json` and marks `ring`/`data-encoding` as direct dependencies) and copy it back with `kubectl --context kw -n nexora-dev exec -i deploy/toolbox -c toolbox -- tar -C /work/nexora -cf - Cargo.lock | tar -xf -`; then run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib snapshot_m3::tests` — expect FAIL with "cannot find function `validate_m3` in this scope".
- [ ] Implement `validate_m3` in `engine/src/snapshot_m3.rs` above the test module. Rules, checked in this order, each returning the exact error strings asserted above (field path, colon, message):
  - `recursion` (when present): each `root_hints[i].name` parses with `hickory_proto::rr::Name::from_ascii` and is FQDN; each `addresses[j]` parses as `std::net::IpAddr` (`"recursion.root_hints[{i}].addresses[{j}]: not an IP address: {v}"`); `max_upstream_queries` is 0 or 1..=1000; `max_delegation_depth` is 0 or 1..=64; `authority_port` is 0..=65535.
  - `forward_zones[i]`: `domain` parses as FQDN; lowercased duplicates rejected (`"forward_zones[{i}].domain: duplicate {lower}"`); `addresses` non-empty, each parses as `std::net::SocketAddr` (`"forward_zones[{i}].addresses[{j}]: not ip:port: {v}"`).
  - `dnssec.trust_anchors[i]`: `zone` FQDN; `ds` parses with `parse_ds` (four whitespace-separated fields; key tag `u16`; algorithm `u8`; digest type `u8`; digest hex via `data_encoding::HEXUPPER_PERMISSIVE`, error `"digest is not hex"`; digest length 20 for type 1, 32 for type 2, 48 for type 4, error `"digest length {n} does not match digest type {t}"`).
  - `dnssec.negative_trust_anchors[i]`: `domain` FQDN, `expires_unix > 0`.
  - `rpz_zones[i]`: `id` non-empty and unique; `name` FQDN; `source` present (`"rpz_zones[{i}]: no source"`); file: `blob` present and `crate::snapshot::is_sha256_hex(&blob.sha256)` (`"rpz_zones[{i}].file.blob.sha256: must be 64 lowercase hex"`); transfer: `primary` parses as `SocketAddr`; `tsig_algorithm` is a known `TsigAlgorithm`; when it is not `None`, `tsig_key_name` is non-empty (`"rpz_zones[{i}].transfer.tsig_key_name: required with tsig_algorithm"`) and FQDN.
- [ ] In `engine/src/snapshot.rs`: make `is_sha256_hex` `pub(crate)` and end `validate` with `crate::snapshot_m3::validate_m3(s).map_err(SnapshotError::Invalid)?;` before `Ok(())`.
- [ ] Create `engine/src/recursor/mod.rs` with the constants and `LocalBoxFuture` listed under Interfaces and `pub mod` lines for the submodules added by later tasks, starting with none; add `pub mod recursor;` to `engine/src/lib.rs`.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib snapshot_m3::tests` — expect PASS (8 tests); run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --test snapshot_apply` — expect PASS (M1 validation unchanged).
- [ ] Update `docs/architecture.md`: in "Contract", add an M3 bullet — `ConfigSnapshot.resolution_mode`/`recursion`/`forward_zones`/`dnssec`/`rpz_zones` (100–104), `Stats.recursion`/`dnssec`/`rpz_zones` (100–102), `ServerMessage.rpz_tsig_keys` (100): RPZ TSIG secrets travel only on `Connect` and are held in engine memory; RPZ file zones are blobs fetched with `GetBlob`. In "Management plane", change `NEXORA_KEK_FILE (M4)` to `NEXORA_KEK_FILE (M3: RPZ TSIG secrets sealed by internal/secrets; M4 adds DNSSEC and TSIG keys)` and add `internal/secrets` (NXE1 envelope encryption under the KEK) to the repository layout. In "Engine", add `state_dir/trust-anchors.json` and `state_dir/rpz/<zone id>.zone` to the engine's local state.
- [ ] Commit: `git add proto gen/go mgmt/internal/control/contract_m3_test.go engine/Cargo.toml Cargo.lock engine/src/lib.rs engine/src/snapshot.rs engine/src/snapshot_m3.rs engine/src/control.rs engine/src/recursor/mod.rs docs/architecture.md && git commit -m "feat(proto): M3 recursion, DNSSEC and RPZ contract"`.

## Task 2: Outbound transport with spoofing defences and recursor metrics

Files:

- `engine/src/recursor/metrics.rs` — `RecursorMetrics` atomics for every M3 counter.
- `engine/src/recursor/transport.rs` — one query/response exchange with an authoritative server or forward-zone server.
- `engine/src/recursor/transport/tests.rs` — unit tests with in-test fake servers.
- `engine/src/recursor/mod.rs` — `pub mod metrics; pub mod transport;`.

Interfaces:

```rust
// metrics.rs
#[derive(Default)]
pub struct RecursorMetrics {
    pub upstream_queries: AtomicU64,
    pub upstream_timeouts: AtomicU64,
    pub mismatched_id: AtomicU64,
    pub mismatched_question: AtomicU64,
    pub mismatched_case: AtomicU64,
    pub malformed_replies: AtomicU64,
    pub tcp_fallbacks: AtomicU64,
    pub edns_fallbacks: AtomicU64,
    pub lame_marked: AtomicU64,
    pub limit_queries: AtomicU64,
    pub limit_delegation_depth: AtomicU64,
    pub limit_cname_depth: AtomicU64,
    pub cname_loops: AtomicU64,
    pub resolutions_recursive: AtomicU64,
    pub resolutions_forward_zone: AtomicU64,
    pub resolution_failures: AtomicU64,
    pub dnssec_secure: AtomicU64,
    pub dnssec_insecure: AtomicU64,
    pub dnssec_bogus: AtomicU64,
    pub dnssec_indeterminate: AtomicU64,
    pub dnssec_bogus_by_ede: [AtomicU64; 32],
    pub dnssec_aggressive_synthesized: AtomicU64,
    pub trust_anchor_refresh_failures: AtomicU64,
}
impl RecursorMetrics { pub fn inc(c: &AtomicU64) { c.fetch_add(1, Ordering::Relaxed); } }

// transport.rs
pub struct OutboundQuery<'a> {
    pub server: SocketAddr,
    pub qname: &'a Name,
    pub qtype: RecordType,
    pub recursion_desired: bool,  // true only for forward-zone servers
    pub edns: bool,               // OPT with payload EDNS_BUFFER
    pub dnssec_ok: bool,
    pub checking_disabled: bool,
    pub use_0x20: bool,
    pub timeout: Duration,
}
pub struct Exchange { pub message: Message, pub rtt: Duration, pub via_tcp: bool }
#[derive(Debug)]
pub enum ExchangeError { Timeout, Network(std::io::ErrorKind), TcpFailed(String) }
pub struct Transport { metrics: Arc<RecursorMetrics> }
impl Transport {
    pub fn new(metrics: Arc<RecursorMetrics>) -> Self;
    pub async fn exchange(&self, q: &OutboundQuery<'_>) -> Result<Exchange, ExchangeError>;
}
pub fn randomise_case(name: &Name, rng: &mut impl rand::Rng) -> Name;
pub fn reply_matches(sent_id: u16, sent_name: &Name, sent_type: RecordType, strict_case: bool, reply: &Message) -> Result<(), Mismatch>;
#[derive(Debug, PartialEq)] pub enum Mismatch { Id, Question, Case, NotResponse }
```

- [ ] Create `engine/src/recursor/transport/tests.rs` and declare it from `transport.rs` with `#[cfg(test)] mod tests;` (transport.rs otherwise empty):

```rust
use super::*;
use hickory_proto::op::{Message, MessageType, OpCode, Query};
use hickory_proto::rr::{rdata::A, DNSClass, Name, RData, Record, RecordType};
use std::net::{Ipv4Addr, SocketAddr};
use std::sync::atomic::Ordering;
use std::sync::Arc;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, UdpSocket};

fn answer(id: u16, name: &Name, ip: Ipv4Addr, tc: bool) -> Vec<u8> {
    let mut m = Message::response(id, OpCode::Query);
    m.metadata.authoritative = true;
    m.metadata.truncation = tc;
    m.queries.push(Query::query(name.clone(), RecordType::A));
    if !tc {
        m.answers.push(Record::from_rdata(name.clone(), 300, RData::A(A(ip))));
    }
    m.to_vec().unwrap()
}

fn outbound<'a>(server: SocketAddr, name: &'a Name) -> OutboundQuery<'a> {
    OutboundQuery { server, qname: name, qtype: RecordType::A, recursion_desired: false, edns: true, dnssec_ok: true, checking_disabled: false, use_0x20: true, timeout: Duration::from_millis(800) }
}

#[tokio::test(flavor = "current_thread")]
async fn spoofed_replies_are_dropped_and_real_reply_accepted() {
    let server = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let addr = server.local_addr().unwrap();
    tokio::spawn(async move {
        let mut buf = [0u8; 1500];
        let (n, peer) = server.recv_from(&mut buf).await.unwrap();
        let q = Message::from_vec(&buf[..n]).unwrap();
        let id = q.metadata.id;
        let sent = q.queries[0].name().clone();
        // wrong ID
        server.send_to(&answer(id.wrapping_add(1), &sent, Ipv4Addr::new(6, 6, 6, 6), false), peer).await.unwrap();
        // right ID, wrong question
        server.send_to(&answer(id, &Name::from_ascii("evil.example.").unwrap(), Ipv4Addr::new(6, 6, 6, 6), false), peer).await.unwrap();
        // right ID, 0x20 casing not preserved
        server.send_to(&answer(id, &sent.to_lowercase(), Ipv4Addr::new(6, 6, 6, 6), false), peer).await.unwrap();
        // genuine reply
        server.send_to(&answer(id, &sent, Ipv4Addr::new(192, 0, 2, 1), false), peer).await.unwrap();
    });
    let metrics = Arc::new(RecursorMetrics::default());
    let t = Transport::new(metrics.clone());
    let name = Name::from_ascii("www.abcdefghijklmnop.example.").unwrap();
    let ex = t.exchange(&outbound(addr, &name)).await.unwrap();
    assert_eq!(ex.message.answers[0].data, RData::A(A(Ipv4Addr::new(192, 0, 2, 1))));
    assert!(!ex.via_tcp);
    assert_eq!(metrics.mismatched_id.load(Ordering::Relaxed), 1);
    assert_eq!(metrics.mismatched_question.load(Ordering::Relaxed), 1);
    // the all-lowercase spoof differs from the randomised case unless the RNG produced all-lowercase,
    // which for 20 letters happens with probability 2^-20; the test name has 20 letters.
    assert_eq!(metrics.mismatched_case.load(Ordering::Relaxed), 1);
}

#[tokio::test(flavor = "current_thread")]
async fn truncated_udp_reply_retries_over_tcp() {
    let udp = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let addr = udp.local_addr().unwrap();
    let tcp = TcpListener::bind(addr).await.unwrap();
    tokio::spawn(async move {
        let mut buf = [0u8; 1500];
        let (n, peer) = udp.recv_from(&mut buf).await.unwrap();
        let q = Message::from_vec(&buf[..n]).unwrap();
        udp.send_to(&answer(q.metadata.id, q.queries[0].name(), Ipv4Addr::UNSPECIFIED, true), peer).await.unwrap();
    });
    tokio::spawn(async move {
        let (mut s, _) = tcp.accept().await.unwrap();
        let len = s.read_u16().await.unwrap() as usize;
        let mut buf = vec![0u8; len];
        s.read_exact(&mut buf).await.unwrap();
        let q = Message::from_vec(&buf).unwrap();
        let out = answer(q.metadata.id, q.queries[0].name(), Ipv4Addr::new(192, 0, 2, 2), false);
        s.write_u16(out.len() as u16).await.unwrap();
        s.write_all(&out).await.unwrap();
    });
    let metrics = Arc::new(RecursorMetrics::default());
    let name = Name::from_ascii("big.example.").unwrap();
    let ex = Transport::new(metrics.clone()).exchange(&outbound(addr, &name)).await.unwrap();
    assert!(ex.via_tcp);
    assert_eq!(ex.message.answers[0].data, RData::A(A(Ipv4Addr::new(192, 0, 2, 2))));
    assert_eq!(metrics.tcp_fallbacks.load(Ordering::Relaxed), 1);
}

#[tokio::test(flavor = "current_thread")]
async fn query_carries_random_id_edns_1232_do_and_rd_clear() {
    let server = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let addr = server.local_addr().unwrap();
    let (tx, rx) = tokio::sync::oneshot::channel::<(Message, SocketAddr)>();
    tokio::spawn(async move {
        let mut buf = [0u8; 1500];
        let (n, peer) = server.recv_from(&mut buf).await.unwrap();
        tx.send((Message::from_vec(&buf[..n]).unwrap(), peer)).unwrap();
    });
    let name = Name::from_ascii("example.").unwrap();
    let t = Transport::new(Arc::new(RecursorMetrics::default()));
    let mut q = outbound(addr, &name);
    q.timeout = Duration::from_millis(100);
    let _ = t.exchange(&q).await;
    let (m, peer) = rx.await.unwrap();
    assert_eq!(m.metadata.message_type, MessageType::Query);
    assert!(!m.metadata.recursion_desired);
    let edns = m.edns.expect("OPT present");
    assert_eq!(edns.max_payload(), 1232);
    assert!(edns.flags().dnssec_ok);
    assert_ne!(peer.port(), 0);
    assert_eq!(m.queries[0].query_class(), DNSClass::IN);
}

#[test]
fn reply_matching_rules() {
    let sent = Name::from_ascii("wWw.ExAmple.").unwrap();
    let ok = Message::from_vec(&answer(7, &sent, Ipv4Addr::LOCALHOST, false)).unwrap();
    assert_eq!(reply_matches(7, &sent, RecordType::A, true, &ok), Ok(()));
    assert_eq!(reply_matches(8, &sent, RecordType::A, true, &ok), Err(Mismatch::Id));
    let lower = Message::from_vec(&answer(7, &sent.to_lowercase(), Ipv4Addr::LOCALHOST, false)).unwrap();
    assert_eq!(reply_matches(7, &sent, RecordType::A, true, &lower), Err(Mismatch::Case));
    assert_eq!(reply_matches(7, &sent, RecordType::A, false, &lower), Ok(()));
    assert_eq!(reply_matches(7, &sent, RecordType::AAAA, true, &ok), Err(Mismatch::Question));
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::transport::tests` — expect FAIL with "cannot find type `RecursorMetrics` in this scope".
- [ ] Create `engine/src/recursor/metrics.rs` with the struct above (all fields `AtomicU64`, `Default` derived; `dnssec_bogus_by_ede` default via `std::array::from_fn(|_| AtomicU64::new(0))` in a manual `Default` impl because arrays of 32 atomics do not derive).
- [ ] Implement `transport.rs`:
  - `randomise_case`: for every label byte in `b'a'..=b'z' | b'A'..=b'Z'` draw one bit from `rng.next_u32()` (refilled every 32 bits) and set upper case when 1; rebuild with `Name::from_labels`, keep FQDN.
  - `reply_matches` order: `message_type != Response` → `NotResponse`; `metadata.id != sent_id` → `Id`; `queries.len() != 1`, type, class IN, or `!name.eq_ignore_ascii_case` (compare `to_lowercase()` values) → `Question`; `strict_case && !queries[0].name().eq_case(sent_name)` → `Case`.
  - `exchange`: `let mut rng = rand::rng();` (the OS-seeded CSPRNG M1's upstream IDs use; `rand::Rng::next_u32`); `id = rng.next_u32() as u16`; `wire_name = if use_0x20 { randomise_case } else { qname.clone() }`; build `Message::new(id, MessageType::Query, OpCode::Query)` with `recursion_desired`, `checking_disabled`, one `Query::query(wire_name, qtype)`, and when `edns` an `Edns::new()` with `set_max_payload(EDNS_BUFFER)` and `set_dnssec_ok(dnssec_ok)`.
  - UDP socket: bind to the unspecified address of the server's family on a random port — up to 10 attempts at `1024 + rng.next_u32() % 64512`, then port 0 — then `connect(server)` so the kernel drops datagrams from any other address/port. Send once; loop `recv` under `tokio::time::timeout_at(start + timeout)`. For each datagram: `< 12` bytes → `malformed_replies += 1`, continue; `Message::from_vec` error → `malformed_replies += 1`, continue; `reply_matches(.., strict_case = use_0x20, ..)` error → increment `mismatched_id` / `mismatched_question` (also for `NotResponse`) / `mismatched_case`, continue; otherwise accept. Deadline expiry → `upstream_timeouts += 1`, `Err(Timeout)`. `upstream_queries += 1` per datagram sent.
  - If the accepted reply has `truncation`: `tcp_fallbacks += 1`; open `TcpStream::connect(server)` under the remaining deadline (minimum 500 ms), send the same query with a fresh random ID prefixed by a big-endian `u16` length, read one length-prefixed reply, apply `reply_matches` (mismatch → `Err(TcpFailed("reply mismatch"))`), return `via_tcp: true`.
  - `rtt` is measured from send to accepted reply.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::transport::tests` — expect PASS (4 tests).
- [ ] Commit: `git add engine/src/recursor && git commit -m "feat(recursor): outbound transport with 0x20, strict matching and TCP fallback"`.

## Task 3: Infrastructure cache, RRset cache, root hints, work budget

Files:

- `engine/src/recursor/infra.rs` — per-server RTT/RTO, timeouts, backoff, EDNS support, lame (server, zone) marks.
- `engine/src/recursor/rrcache.rs` — RRset cache with RFC 2181 credibility ranking and DNSSEC status.
- `engine/src/recursor/roothints.rs` — compiled-in IANA root hints (IPv4 + IPv6) and snapshot override.
- `engine/src/recursor/budget.rs` — per-client-query work limits.
- `engine/src/recursor/infra_tests.rs` — unit tests for all four (declared `#[cfg(test)] mod infra_tests;` in `mod.rs`).
- `engine/src/recursor/mod.rs` — module declarations.

Interfaces:

```rust
// infra.rs
pub struct InfraCache { /* quick_cache::sync::Cache<IpAddr, InfraEntry>, quick_cache::sync::Cache<(IpAddr, Name), u64 /*lame until*/> */ }
#[derive(Clone, Copy, Debug)]
pub struct InfraEntry { pub srtt_ms: f32, pub rttvar_ms: f32, pub rto_ms: u32, pub consecutive_timeouts: u8, pub backoff_until: u64, pub no_edns: bool }
impl InfraCache {
    pub fn new(capacity: usize) -> Self;
    pub fn rto(&self, ip: IpAddr) -> Duration;                       // 376 ms for unknown servers
    pub fn record_rtt(&self, ip: IpAddr, rtt: Duration);
    pub fn record_timeout(&self, ip: IpAddr, now: u64);
    pub fn is_backed_off(&self, ip: IpAddr, now: u64) -> bool;
    pub fn mark_lame(&self, ip: IpAddr, zone: &Name, now: u64);      // 900 s
    pub fn is_lame(&self, ip: IpAddr, zone: &Name, now: u64) -> bool;
    pub fn set_no_edns(&self, ip: IpAddr);
    pub fn no_edns(&self, ip: IpAddr) -> bool;
    pub fn select(&self, candidates: &[IpAddr], zone: &Name, now: u64, rng: &mut impl rand::Rng) -> Option<IpAddr>;
    pub fn len(&self) -> usize;
}
// rrcache.rs
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub enum Credibility { Additional = 1, Glue = 2, AuthorityNonAa = 3, AnswerNonAa = 4, AuthorityAa = 5, AnswerAa = 6 }
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum DnssecStatus { Unchecked, Secure, Insecure, Bogus }
pub struct CachedRrset { pub records: Vec<Record>, pub rrsigs: Vec<Record>, pub expires_at: u64, pub credibility: Credibility, pub dnssec: DnssecStatus }
pub struct RrCache { /* quick_cache::sync::Cache<(Name, RecordType), Arc<CachedRrset>> keyed by lowercase name */ }
impl RrCache {
    pub fn new(max_entries: usize) -> Self;
    pub fn insert(&self, records: Vec<Record>, rrsigs: Vec<Record>, credibility: Credibility, dnssec: DnssecStatus, now: u64) -> bool; // false when an unexpired higher-credibility entry exists
    pub fn get(&self, name: &Name, rtype: RecordType, now: u64) -> Option<Arc<CachedRrset>>;
    pub fn set_dnssec(&self, name: &Name, rtype: RecordType, status: DnssecStatus);
}
// roothints.rs
pub struct RootHints { pub servers: Vec<(Name, Vec<IpAddr>)> }
impl RootHints { pub fn iana() -> Self; pub fn from_config(hints: &[proto::RootHint]) -> Self; pub fn addresses(&self, ipv6: bool) -> Vec<IpAddr>; }
// budget.rs
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Limit { UpstreamQueries, DelegationDepth, CnameDepth }
pub struct WorkBudget { queries: Cell<u32>, max_queries: u32, max_depth: u32, in_progress: RefCell<Vec<(Name, RecordType)>> }
impl WorkBudget {
    pub fn new(max_queries: u32, max_depth: u32) -> Self;
    pub fn spend_query(&self) -> Result<(), Limit>;
    pub fn check_depth(&self, depth: u32) -> Result<(), Limit>;
    pub fn enter(&self, name: &Name, rtype: RecordType) -> bool; // false = cycle
    pub fn leave(&self, name: &Name, rtype: RecordType);
    pub fn queries_used(&self) -> u32;
}
```

- [ ] Create `engine/src/recursor/infra_tests.rs`:

```rust
use super::budget::{Limit, WorkBudget};
use super::infra::InfraCache;
use super::roothints::RootHints;
use super::rrcache::{Credibility, DnssecStatus, RrCache};
use hickory_proto::rr::{rdata::{A, NS}, Name, RData, Record, RecordType};
use rand::SeedableRng;
use std::net::{IpAddr, Ipv4Addr};
use std::time::Duration;

fn ip(last: u8) -> IpAddr { IpAddr::V4(Ipv4Addr::new(192, 0, 2, last)) }

#[test]
fn rto_follows_rfc6298_smoothing() {
    let c = InfraCache::new(1000);
    assert_eq!(c.rto(ip(1)), Duration::from_millis(376));
    c.record_rtt(ip(1), Duration::from_millis(100)); // srtt 100, rttvar 50 -> rto 300
    assert_eq!(c.rto(ip(1)), Duration::from_millis(300));
    c.record_rtt(ip(1), Duration::from_millis(100)); // rttvar 37.5 -> rto 250
    assert_eq!(c.rto(ip(1)), Duration::from_millis(250));
}

#[test]
fn three_timeouts_back_off_exponentially_and_rto_doubles() {
    let c = InfraCache::new(1000);
    c.record_rtt(ip(2), Duration::from_millis(100));
    c.record_timeout(ip(2), 1000);
    assert_eq!(c.rto(ip(2)), Duration::from_millis(600));
    assert!(!c.is_backed_off(ip(2), 1000));
    c.record_timeout(ip(2), 1000);
    c.record_timeout(ip(2), 1000);
    assert!(c.is_backed_off(ip(2), 1004));
    assert!(!c.is_backed_off(ip(2), 1005));
    c.record_timeout(ip(2), 1005); // 4th consecutive -> 10 s
    assert!(c.is_backed_off(ip(2), 1014));
    assert!(!c.is_backed_off(ip(2), 1015));
    c.record_rtt(ip(2), Duration::from_millis(80));
    assert!(!c.is_backed_off(ip(2), 1005));
}

#[test]
fn lame_marks_are_per_zone_and_expire() {
    let c = InfraCache::new(1000);
    let zone = Name::from_ascii("example.").unwrap();
    c.mark_lame(ip(3), &zone, 100);
    assert!(c.is_lame(ip(3), &zone, 999));
    assert!(!c.is_lame(ip(3), &Name::from_ascii("other.").unwrap(), 101));
    assert!(!c.is_lame(ip(3), &zone, 1000));
}

#[test]
fn select_skips_lame_and_backed_off_but_probes_when_all_are_down() {
    let c = InfraCache::new(1000);
    let zone = Name::from_ascii("example.").unwrap();
    let mut rng = rand::rngs::StdRng::seed_from_u64(1);
    c.mark_lame(ip(4), &zone, 0);
    for _ in 0..3 { c.record_timeout(ip(5), 0); }
    c.record_rtt(ip(6), Duration::from_millis(30));
    for _ in 0..20 { assert_eq!(c.select(&[ip(4), ip(5), ip(6)], &zone, 1, &mut rng), Some(ip(6))); }
    for _ in 0..3 { c.record_timeout(ip(6), 1); }
    assert_eq!(c.select(&[ip(4), ip(5), ip(6)], &zone, 2, &mut rng), Some(ip(5)));
    assert_eq!(c.select(&[ip(4)], &zone, 2, &mut rng), None);
}

#[test]
fn select_picks_randomly_within_400ms_band() {
    let c = InfraCache::new(1000);
    let zone = Name::root();
    c.record_rtt(ip(7), Duration::from_millis(20));
    c.record_rtt(ip(8), Duration::from_millis(60));
    for _ in 0..10 { c.record_rtt(ip(9), Duration::from_millis(900)); }
    let mut rng = rand::rngs::StdRng::seed_from_u64(7);
    let mut seen = std::collections::HashSet::new();
    for _ in 0..200 { seen.insert(c.select(&[ip(7), ip(8), ip(9)], &zone, 0, &mut rng).unwrap()); }
    assert!(seen.contains(&ip(7)) && seen.contains(&ip(8)));
    assert!(!seen.contains(&ip(9)));
}

#[test]
fn rrcache_credibility_ranking_and_expiry() {
    let c = RrCache::new(1000);
    let n = Name::from_ascii("ns.example.").unwrap();
    let glue = vec![Record::from_rdata(n.clone(), 3600, RData::A(A(Ipv4Addr::new(6, 6, 6, 6))))];
    let auth = vec![Record::from_rdata(n.clone(), 60, RData::A(A(Ipv4Addr::new(192, 0, 2, 53))))];
    assert!(c.insert(auth.clone(), vec![], Credibility::AnswerAa, DnssecStatus::Unchecked, 100));
    assert!(!c.insert(glue, vec![], Credibility::Glue, DnssecStatus::Unchecked, 100));
    assert_eq!(c.get(&Name::from_ascii("NS.Example.").unwrap(), RecordType::A, 159).unwrap().records, auth);
    assert!(c.get(&n, RecordType::A, 160).is_none());
    let zone = Name::from_ascii("example.").unwrap();
    let ns = vec![Record::from_rdata(zone.clone(), 0, RData::NS(NS(n.clone())))];
    assert!(!c.insert(ns, vec![], Credibility::AuthorityAa, DnssecStatus::Unchecked, 100), "TTL 0 is never cached");
}

#[test]
fn iana_root_hints_have_13_servers_with_v4_and_v6() {
    let h = RootHints::iana();
    assert_eq!(h.servers.len(), 13);
    assert_eq!(h.addresses(false).len(), 13);
    assert_eq!(h.addresses(true).len(), 26);
    assert!(h.addresses(false).contains(&"198.41.0.4".parse().unwrap()));
    assert!(h.addresses(true).contains(&"2001:7fd::1".parse().unwrap()));
}

#[test]
fn work_budget_limits_queries_depth_and_detects_cycles() {
    let b = WorkBudget::new(3, 2);
    assert_eq!(b.spend_query(), Ok(()));
    assert_eq!(b.spend_query(), Ok(()));
    assert_eq!(b.spend_query(), Ok(()));
    assert_eq!(b.spend_query(), Err(Limit::UpstreamQueries));
    assert_eq!(b.check_depth(2), Ok(()));
    assert_eq!(b.check_depth(3), Err(Limit::DelegationDepth));
    let n = Name::from_ascii("ns.loop.example.").unwrap();
    assert!(b.enter(&n, RecordType::A));
    assert!(!b.enter(&Name::from_ascii("NS.loop.example.").unwrap(), RecordType::A));
    b.leave(&n, RecordType::A);
    assert!(b.enter(&n, RecordType::A));
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::infra_tests` — expect FAIL with "unresolved import `super::budget`".
- [ ] Implement `infra.rs`. RTT update (RFC 6298, α=1/8, β=1/4): first sample `srtt=r, rttvar=r/2`; later `rttvar = 0.75*rttvar + 0.25*|srtt-r|; srtt = 0.875*srtt + 0.125*r`; `rto_ms = clamp(round(srtt + 4*rttvar), 50, 3000)`; a sample resets `consecutive_timeouts=0, backoff_until=0`. Timeout: `rto_ms = min(rto_ms*2, 3000)` (unknown server starts at 376), `consecutive_timeouts += 1`; when `>= 3`, `backoff_until = now + min(5 << (consecutive_timeouts - 3), 300)`. `is_backed_off` is `now < backoff_until`. `select`:

```rust
pub fn select(&self, candidates: &[IpAddr], zone: &Name, now: u64, rng: &mut impl rand::Rng) -> Option<IpAddr> {
    let usable: Vec<IpAddr> = candidates.iter().copied().filter(|ip| !self.is_lame(*ip, zone, now)).collect();
    let up: Vec<(IpAddr, u32)> = usable.iter().copied().filter(|ip| !self.is_backed_off(*ip, now))
        .map(|ip| (ip, self.rto(ip).as_millis() as u32)).collect();
    if up.is_empty() {
        // every non-lame server is backed off: probe the one whose backoff ends first
        return usable.into_iter().min_by_key(|ip| self.entry(*ip).map(|e| e.backoff_until).unwrap_or(0));
    }
    let best = up.iter().map(|(_, r)| *r).min().unwrap();
    let band: Vec<IpAddr> = up.into_iter().filter(|(_, r)| *r <= best + 400).map(|(ip, _)| ip).collect();
    Some(band[(rng.next_u32() as usize) % band.len()])
}
```

Lame keys store `zone.to_lowercase()`; lame expiry `now + 900`.

- [ ] Implement `rrcache.rs`: key `(records[0].name.to_lowercase(), records[0].record_type())`; `expires_at = now + min(min TTL over records, 86400)`; TTL 0 → not inserted, returns false; insert refuses when the existing entry is unexpired and `existing.credibility > credibility`; `Bogus` entries are stored with `expires_at = now + min(ttl, 60)`; `get` returns `None` after `now >= expires_at` and never returns entries whose credibility is `Additional` or `Glue` when called through `get` (glue is read by `get_glue(name, rtype, now)` which returns any credibility — add that method too).
- [ ] Implement `roothints.rs` with this table (IANA `named.root`, 2024):

```rust
const IANA: [(&str, &str, &str); 13] = [
    ("a.root-servers.net.", "198.41.0.4", "2001:503:ba3e::2:30"),
    ("b.root-servers.net.", "170.247.170.2", "2801:1b8:10::b"),
    ("c.root-servers.net.", "192.33.4.12", "2001:500:2::c"),
    ("d.root-servers.net.", "199.7.91.13", "2001:500:2d::d"),
    ("e.root-servers.net.", "192.203.230.10", "2001:500:a8::e"),
    ("f.root-servers.net.", "192.5.5.241", "2001:500:2f::f"),
    ("g.root-servers.net.", "192.112.36.4", "2001:500:12::d0d"),
    ("h.root-servers.net.", "198.97.190.53", "2001:500:1::53"),
    ("i.root-servers.net.", "192.36.148.17", "2001:7fe::53"),
    ("j.root-servers.net.", "192.58.128.30", "2001:503:c27::2:30"),
    ("k.root-servers.net.", "193.0.14.129", "2001:7fd::1"),
    ("l.root-servers.net.", "199.7.83.42", "2001:500:9f::42"),
    ("m.root-servers.net.", "202.12.27.33", "2001:dc3::35"),
];
```

`addresses(ipv6)` returns all IPv4 addresses plus IPv6 addresses only when `ipv6` is true; `from_config` with an empty slice returns `iana()`.

- [ ] Implement `budget.rs`: `spend_query` increments then errors when the count exceeds `max_queries`; `check_depth(d)` errors when `d > max_depth`; `enter` compares lowercase names and pushes on success.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::infra_tests` — expect PASS (8 tests).
- [ ] Commit: `git add engine/src/recursor && git commit -m "feat(recursor): infrastructure cache, rrset cache, root hints and work budget"`.

## Task 4: Iterative resolver (delegations, bailiwick, glueless NS, CNAME/DNAME, QNAME minimisation, limits)

Files:

- `engine/src/recursor/testnet.rs` — `#[cfg(test)]` fake authoritative servers built from zone text, bound to `127.0.54.x` on one shared port.
- `engine/src/recursor/iterate.rs` — `Recursor` and the iterative algorithm.
- `engine/src/recursor/iterate_tests.rs` — unit tests.
- `engine/src/recursor/mod.rs` — declarations; `RecursorState` skeleton.

Interfaces:

```rust
// iterate.rs
pub struct RecursionParams {
    pub root_hints: Arc<RootHints>,
    pub qname_minimisation: bool,
    pub max_upstream_queries: u32,
    pub max_delegation_depth: u32,
    pub authority_port: u16,
}
impl RecursionParams { pub fn from_config(c: Option<&proto::RecursionConfig>) -> Self; }
#[derive(Debug, Clone)]
pub struct Resolution {
    pub rcode: ResponseCode,
    pub answers: Vec<Record>,      // CNAME/DNAME chain + final RRset, RRSIGs included
    pub authorities: Vec<Record>,  // SOA + NSEC/NSEC3 (+RRSIGs) for negative answers
    pub zone: Name,                // zone cut that produced the final answer
    pub ns_names: Vec<Name>,       // NS set of that zone (RPZ NSDNAME)
    pub ns_addrs: Vec<IpAddr>,     // addresses of that NS set (RPZ NSIP)
}
#[derive(Debug, Clone, PartialEq)]
pub enum RecursionError { Limit(Limit), CnameLoop, NoReachableAuthority, Deadline }
pub struct Recursor { pub transport: Transport, pub infra: InfraCache, pub rrcache: RrCache, pub metrics: Arc<RecursorMetrics>, pub ipv6: AtomicBool } // `Transport` is recursor::transport::Transport, not edns::Transport
impl Recursor {
    pub fn new(metrics: Arc<RecursorMetrics>) -> Self;
    pub async fn resolve(&self, qname: &Name, qtype: RecordType, p: &RecursionParams, budget: &WorkBudget) -> Result<Resolution, RecursionError>;
    pub async fn fetch(&self, qname: &Name, qtype: RecordType, p: &RecursionParams, budget: &WorkBudget) -> Result<Resolution, RecursionError>; // no CNAME chasing, used by the validator for DS/DNSKEY
    pub fn detect_ipv6(&self);
}
pub fn in_bailiwick(zone: &Name, owner: &Name) -> bool; // zone.zone_of(owner), case-insensitive
// testnet.rs (cfg(test))
pub struct FakeZone { pub origin: &'static str, pub ip: Ipv4Addr, pub text: String, pub behaviour: Behaviour }
#[derive(Clone, Copy, PartialEq)] pub enum Behaviour { Normal, Silent, Refused }
pub struct FakeNet { pub port: u16, pub queries: Arc<Mutex<Vec<(Ipv4Addr, Name, RecordType)>>> }
impl FakeNet { pub async fn start(zones: Vec<FakeZone>) -> FakeNet; pub fn params(&self, root_ip: Ipv4Addr) -> RecursionParams; pub fn queries_to(&self, ip: Ipv4Addr) -> usize; }
```

- [ ] Create `engine/src/recursor/testnet.rs`. Each zone is parsed with `hickory_proto::serialize::txt::Parser::new(text, None, Some(origin))`. The port is chosen by binding `127.0.54.1:0`, then binding every other zone IP (UDP and TCP) on that port, retrying from scratch up to 20 times on `AddrInUse`. Answer logic per received query (`q`, zone `z`):

```rust
fn answer(z: &ParsedZone, q: &Message) -> Message {
    let query = &q.queries[0];
    let qname = query.name().to_lowercase();
    let mut r = Message::response(q.metadata.id, OpCode::Query);
    r.queries.push(query.clone()); // echo the exact casing (0x20)
    if z.behaviour == Behaviour::Refused || !z.origin.zone_of(&qname) {
        r.metadata.response_code = ResponseCode::Refused;
        return r;
    }
    // delegation: deepest NS owner strictly below the origin that is an ancestor-or-self of qname
    if let Some(cut) = z.cuts.iter().filter(|c| c.zone_of(&qname)).max_by_key(|c| c.num_labels()) {
        if !(query.query_type() == RecordType::DS && &qname == cut) {
            r.authorities.extend(z.rrset(cut, RecordType::NS));
            for ns in z.rrset(cut, RecordType::NS) {
                if let RData::NS(NS(target)) = &ns.data {
                    r.additionals.extend(z.rrset(target, RecordType::A)); // glue as written, even out of bailiwick
                }
            }
            return r;
        }
    }
    r.metadata.authoritative = true;
    // DNAME at an ancestor
    for anc in ancestors(&qname) {
        if let Some(d) = z.rrset(&anc, RecordType::DNAME).first() {
            if anc != qname {
                if let RData::DNAME(DNAME(target)) = &d.data {
                    r.answers.push(d.clone());
                    let keep = (qname.num_labels() - anc.num_labels()) as usize;
                    let synth = Name::from_labels(qname.iter().take(keep)).unwrap().append_domain(target).unwrap();
                    r.answers.push(Record::from_rdata(qname.clone(), 0, RData::CNAME(CNAME(synth))));
                    return r;
                }
            }
        }
    }
    let exact = z.rrset(&qname, query.query_type());
    if !exact.is_empty() { r.answers.extend(exact); return r; }
    let cname = z.rrset(&qname, RecordType::CNAME);
    if !cname.is_empty() { r.answers.extend(cname); return r; }
    r.authorities.extend(z.rrset(&z.origin, RecordType::SOA));
    if !z.names.contains(&qname) && !z.names.iter().any(|n| qname.zone_of(n)) {
        r.metadata.response_code = ResponseCode::NXDomain;
    }
    r
}
```

`Behaviour::Silent` never replies. TCP accepts one length-prefixed query per connection and uses the same `answer`. Every received query is appended to `queries`. `params(root_ip)` returns hints `[("root.fake.", [root_ip])]`, `qname_minimisation: true`, limits 100/32, `authority_port: port`.

- [ ] Create `engine/src/recursor/iterate_tests.rs`:

```rust
use super::budget::{Limit, WorkBudget};
use super::iterate::{RecursionError, Recursor};
use super::metrics::RecursorMetrics;
use super::testnet::{Behaviour, FakeNet, FakeZone};
use hickory_proto::op::ResponseCode;
use hickory_proto::rr::{rdata::A, Name, RData, RecordType};
use std::net::Ipv4Addr;
use std::sync::Arc;

const SOA: &str = "@ 300 IN SOA ns hostmaster 1 3600 600 86400 300\n";

fn hierarchy(extra_example: &str, zones_extra: Vec<FakeZone>) -> Vec<FakeZone> {
    let mut v = vec![
        FakeZone { origin: ".", ip: Ipv4Addr::new(127, 0, 54, 1), behaviour: Behaviour::Normal, text: format!(
            "{SOA}@ 300 IN NS root.fake.\nroot.fake. 300 IN A 127.0.54.1\n\
             test. 300 IN NS ns1.test.\ntest. 300 IN NS ns2.test.\nns1.test. 300 IN A 127.0.54.9\nns2.test. 300 IN A 127.0.54.2\n") },
        FakeZone { origin: "test.", ip: Ipv4Addr::new(127, 0, 54, 2), behaviour: Behaviour::Normal, text: format!(
            "{SOA}@ 300 IN NS ns2\nns2 300 IN A 127.0.54.2\n\
             example 300 IN NS ns.example\nns.example 300 IN A 127.0.54.3\n\
             glueless 300 IN NS ns.provider.example.test.\n\
             poison 300 IN NS ns.poison\nns.poison 300 IN A 127.0.54.5\n\
             deep.a.b.c.d.e 300 IN A 192.0.2.77\n") },
        FakeZone { origin: "example.test.", ip: Ipv4Addr::new(127, 0, 54, 3), behaviour: Behaviour::Normal, text: format!(
            "{SOA}@ 300 IN NS ns\nns 300 IN A 127.0.54.3\nwww 300 IN A 192.0.2.10\nalias 300 IN CNAME www\n\
             ext 300 IN CNAME www.glueless.test.\nloop1 300 IN CNAME loop2\nloop2 300 IN CNAME loop1\n\
             old 300 IN DNAME example.test.\nns.provider 300 IN A 127.0.54.4\n{extra_example}") },
        FakeZone { origin: "glueless.test.", ip: Ipv4Addr::new(127, 0, 54, 4), behaviour: Behaviour::Normal, text: format!(
            "{SOA}@ 300 IN NS ns.provider.example.test.\nwww 300 IN A 192.0.2.20\n") },
        FakeZone { origin: "poison.test.", ip: Ipv4Addr::new(127, 0, 54, 5), behaviour: Behaviour::Normal, text: format!(
            "{SOA}@ 300 IN NS ns\nns 300 IN A 127.0.54.5\n\
             sub 300 IN NS ns.example.test.\nns.example.test. 300 IN A 127.0.54.66\n\
             www 300 IN A 192.0.2.30\n") },
        FakeZone { origin: "test.", ip: Ipv4Addr::new(127, 0, 54, 9), behaviour: Behaviour::Silent, text: String::new() },
    ];
    v.extend(zones_extra);
    v
}

async fn resolve(net: &FakeNet, r: &Recursor, name: &str, t: RecordType) -> Result<super::iterate::Resolution, RecursionError> {
    let p = net.params(Ipv4Addr::new(127, 0, 54, 1));
    let b = WorkBudget::new(p.max_upstream_queries, p.max_delegation_depth);
    r.resolve(&Name::from_ascii(name).unwrap(), t, &p, &b).await
}

fn a(ip: [u8; 4]) -> RData { RData::A(A(Ipv4Addr::from(ip))) }

#[tokio::test(flavor = "current_thread")]
async fn follows_delegations_from_root_hints() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let res = resolve(&net, &r, "www.example.test.", RecordType::A).await.unwrap();
    assert_eq!(res.rcode, ResponseCode::NoError);
    assert_eq!(res.answers.last().unwrap().data, a([192, 0, 2, 10]));
    assert_eq!(res.zone, Name::from_ascii("example.test.").unwrap());
    assert_eq!(res.ns_addrs, vec!["127.0.54.3".parse::<std::net::IpAddr>().unwrap()]);
}

#[tokio::test(flavor = "current_thread")]
async fn silent_server_is_skipped_and_backed_off() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    for i in 0..8 {
        let name = format!("nx{i}.test.");
        let res = resolve(&net, &r, &name, RecordType::A).await.unwrap();
        assert_eq!(res.rcode, ResponseCode::NXDomain, "{name}");
    }
    assert!(net.queries_to(Ipv4Addr::new(127, 0, 54, 9)) <= 3, "silent server backed off after 3 timeouts");
}

#[tokio::test(flavor = "current_thread")]
async fn glueless_delegation_resolves_ns_address() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let res = resolve(&net, &r, "www.glueless.test.", RecordType::A).await.unwrap();
    assert_eq!(res.answers.last().unwrap().data, a([192, 0, 2, 20]));
}

#[tokio::test(flavor = "current_thread")]
async fn out_of_bailiwick_glue_is_ignored() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    // poison.test's referral for sub.poison.test carries glue ns.example.test A 127.0.54.66 (outside poison.test)
    let _ = resolve(&net, &r, "www.sub.poison.test.", RecordType::A).await;
    let res = resolve(&net, &r, "ns.example.test.", RecordType::A).await.unwrap();
    assert_eq!(res.answers.last().unwrap().data, a([127, 0, 54, 3]));
    assert!(r.rrcache.get_glue(&Name::from_ascii("ns.example.test.").unwrap(), RecordType::A, 0)
        .map(|s| s.records.iter().all(|rec| rec.data != a([127, 0, 54, 66]))).unwrap_or(true));
}

#[tokio::test(flavor = "current_thread")]
async fn chases_cname_across_zones_and_dname() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let res = resolve(&net, &r, "ext.example.test.", RecordType::A).await.unwrap();
    assert_eq!(res.answers.len(), 2);
    assert_eq!(res.answers[1].data, a([192, 0, 2, 20]));
    let res = resolve(&net, &r, "www.old.example.test.", RecordType::A).await.unwrap();
    assert_eq!(res.answers.last().unwrap().data, a([192, 0, 2, 10]));
}

#[tokio::test(flavor = "current_thread")]
async fn cname_loop_and_depth_limit_fail() {
    let mut chain = String::new();
    for i in 0..17 { chain.push_str(&format!("c{i} 300 IN CNAME c{}.example.test.\n", i + 1)); }
    chain.push_str("c17 300 IN A 192.0.2.99\n");
    let net = FakeNet::start(hierarchy(&chain, vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    assert_eq!(resolve(&net, &r, "loop1.example.test.", RecordType::A).await.unwrap_err(), RecursionError::CnameLoop);
    assert_eq!(resolve(&net, &r, "c0.example.test.", RecordType::A).await.unwrap_err(), RecursionError::Limit(Limit::CnameDepth));
    assert!(resolve(&net, &r, "c2.example.test.", RecordType::A).await.is_ok(), "15 hops is within the limit");
}

#[tokio::test(flavor = "current_thread")]
async fn qname_minimisation_hides_full_name_from_root_and_tld() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let res = resolve(&net, &r, "deep.a.b.c.d.e.test.", RecordType::A).await.unwrap();
    assert_eq!(res.answers.last().unwrap().data, a([192, 0, 2, 77]));
    let q = net.queries.lock().unwrap().clone();
    let root_names: Vec<_> = q.iter().filter(|(ip, _, _)| *ip == Ipv4Addr::new(127, 0, 54, 1)).map(|(_, n, _)| n.to_lowercase()).collect();
    assert!(root_names.iter().all(|n| n == &Name::from_ascii("test.").unwrap()), "root saw {root_names:?}");
}

#[tokio::test(flavor = "current_thread")]
async fn query_budget_stops_amplification() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let mut p = net.params(Ipv4Addr::new(127, 0, 54, 1));
    p.max_upstream_queries = 2;
    let b = WorkBudget::new(p.max_upstream_queries, p.max_delegation_depth);
    let err = r.resolve(&Name::from_ascii("www.glueless.test.").unwrap(), RecordType::A, &p, &b).await.unwrap_err();
    assert_eq!(err, RecursionError::Limit(Limit::UpstreamQueries));
    assert!(b.queries_used() <= 3);
}

#[tokio::test(flavor = "current_thread")]
async fn all_servers_refused_is_no_reachable_authority() {
    let net = FakeNet::start(vec![FakeZone { origin: ".", ip: Ipv4Addr::new(127, 0, 54, 1), behaviour: Behaviour::Refused, text: format!("{SOA}") }]).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    assert_eq!(resolve(&net, &r, "x.test.", RecordType::A).await.unwrap_err(), RecursionError::NoReachableAuthority);
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::iterate_tests` — expect FAIL with "unresolved import `super::iterate`".
- [ ] Implement `iterate.rs`. `resolve` wraps `resolve_chain` in `tokio::time::timeout(RESOLUTION_DEADLINE)` (`Deadline` on expiry). All outgoing queries go through one helper that calls `budget.spend_query()?` first and always sets `dnssec_ok: true`, `use_0x20: true`, `recursion_desired: false`, `edns: !infra.no_edns(ip)`, `timeout: infra.rto(ip)`, `server: SocketAddr::new(ip, p.authority_port)`. Recursive calls are `Box::pin`ned; randomness comes from `rand::rng()`. The algorithm:
  1. `resolve_chain(qname, qtype, depth)`: loop over the CNAME/DNAME chain with `seen: Vec<Name>` (lowercase). For each step call `resolve_one(sname, qtype)`. If the result's answers contain a CNAME owned by `sname` (and `qtype != CNAME`) or a DNAME owned by a proper ancestor of `sname`: target = CNAME target, or synthesised name `sname` minus the DNAME owner plus the DNAME target (a synthesised name longer than 255 octets → `rcode = YXDomain`, return). `hops += 1`; `hops > MAX_CNAME_DEPTH` → `metrics.limit_cname_depth += 1`, `Err(Limit(CnameDepth))`; target already in `seen` → `metrics.cname_loops += 1`, `Err(CnameLoop)`. If the response already contains in-bailiwick records for the target (same zone), take them without a new query; otherwise continue the loop with `sname = target`. Append every step's answers to the accumulated `answers`.
  2. `resolve_one(sname, stype)`: exact unexpired `rrcache.get` hit of credibility `>= AnswerNonAa` → return it. Otherwise `cut = closest_cut(sname)`: walk `sname` and its ancestors, the first cached NS RRset with at least one cached address (glue or authoritative) is the cut; none → root cut from `p.root_hints.addresses(ipv6)`.
  3. Loop with `delegations = 0`, `labels = if p.qname_minimisation { cut.zone.num_labels() + 1 } else { sname.num_labels() }`:
     - `query_name = if labels < sname.num_labels() { sname.trim_to(labels) } else { sname }`, `query_type = if query_name == sname { stype } else { RecordType::A }` (RFC 9156 §2.3 recommends A for hidden labels).
     - `ip = infra.select(&cut.addrs, &cut.zone, now, &mut rand::rng())` (`now = clock::unix_now() as u64`); `None` → resolve missing NS addresses (step 4); still `None` → `Err(NoReachableAuthority)`.
     - On `ExchangeError::Timeout`, `ExchangeError::Network(_)` (e.g. ICMP port unreachable, network unreachable for IPv6) or `ExchangeError::TcpFailed(_)` → `infra.record_timeout`, retry with the next selection (at most `cut.addrs.len() + 2` attempts per step before `NoReachableAuthority`). On success `infra.record_rtt`. `FORMERR`/`NOTIMP` to an EDNS query → `infra.set_no_edns(ip)`, `metrics.edns_fallbacks += 1`, retry the same server once.
     - Classify with `classify(&msg, &cut.zone, &query_name, query_type)`:

```rust
enum Class { Answer, Referral(Name), NoData, NxDomain, Lame }
fn classify(m: &Message, zone: &Name, qn: &Name, qt: RecordType) -> Class {
    let rc = m.metadata.response_code;
    if rc == ResponseCode::NXDomain { return Class::NxDomain; }
    if rc != ResponseCode::NoError { return Class::Lame; }
    let has_answer = m.answers.iter().any(|r| r.name.to_lowercase() == qn.to_lowercase()
        && (r.record_type() == qt || r.record_type() == RecordType::CNAME))
        || m.answers.iter().any(|r| r.record_type() == RecordType::DNAME && r.name.zone_of(qn));
    if has_answer { return Class::Answer; }
    let ns_owner = m.authorities.iter().filter(|r| r.record_type() == RecordType::NS).map(|r| r.name.to_lowercase()).next();
    if let Some(owner) = ns_owner {
        // a referral must go strictly down, stay inside the queried zone and lead towards qname
        if !m.metadata.authoritative && in_bailiwick(zone, &owner) && owner != zone.to_lowercase() && owner.zone_of(&qn.to_lowercase()) {
            return Class::Referral(owner);
        }
        if !m.metadata.authoritative { return Class::Lame; } // upward or sideways referral
    }
    if m.metadata.authoritative { Class::NoData } else { Class::Lame }
}
```

     - `Answer`: keep only answer records with `in_bailiwick(&cut.zone, &r.name)` (RRSIGs included); cache them `AnswerAa`/`AnswerNonAa`; if `query_name != sname` (minimised step) → `labels += 1`, continue; else return `Resolution { rcode: NoError, answers, authorities: in-bailiwick SOA/NSEC/NSEC3/RRSIG, zone: cut.zone, ns_names: cut.names, ns_addrs: cut.addrs }`.
     - `Referral(owner)`: `delegations += 1`; `budget.check_depth(delegations)` → `metrics.limit_delegation_depth += 1`, `Err(Limit(DelegationDepth))`. New cut: NS names from the authority NS RRset; glue = additional A/AAAA whose owner is one of those NS names **and** `in_bailiwick(&cut.zone, owner)` (records failing the check are discarded, never cached); cache NS as `AuthorityNonAa`, glue as `Glue`; DS/NSEC/NSEC3/RRSIG from the authority section cached as `AuthorityNonAa` with `DnssecStatus::Unchecked`; `labels = owner.num_labels() + 1`, continue.
     - `NoData`: if `query_name != sname` → empty non-terminal, `labels += 1`, continue; else return `NoError` with the SOA/NSEC authority section.
     - `NxDomain`: if `query_name != sname` → disable minimisation for this lookup (`labels = sname.num_labels()`), continue (relaxed RFC 9156 §2.3 fallback for broken servers); else return `NXDomain` with authority section.
     - `Lame`: `infra.mark_lame(ip, &cut.zone, now)`, `metrics.lame_marked += 1`, select another server.

4. Missing NS addresses: for up to 3 NS names without cached addresses, skip names where `!budget.enter(name, A)` (cycle), otherwise `Box::pin(self.resolve_chain(ns_name, A, depth + 1))` (and `AAAA` when `ipv6`), `budget.leave`, add addresses to `cut.addrs`. Errors other than `Limit` are ignored for that NS name; `Limit` propagates.

- `fetch` is `resolve_one` without the CNAME loop. `detect_ipv6` sets `ipv6` when `std::net::UdpSocket::bind("[::]:0")` followed by `connect("[2001:500:2::c]:53")` succeeds (no packet is sent).
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::iterate_tests` — expect PASS (9 tests).
- [ ] Commit: `git add engine/src/recursor && git commit -m "feat(recursor): iterative resolution with bailiwick checks, QNAME minimisation and limits"`.

## Task 5: Mode selection, forward zones and query-path integration

Files:

- `engine/src/recursor/dispatch.rs` — `ResolutionRuntime`, `ForwardZones`, `Route`, `ForwardUpstream`, `MissQuery`, `MissAnswer`, `resolve_miss`, response building.
- `engine/src/recursor/dispatch_tests.rs` — unit tests (declared `#[cfg(test)] mod dispatch_tests;` in `mod.rs`).
- `engine/src/recursor/mod.rs` — `RecursorState`.
- `engine/src/recursor/dnssec/mod.rs`, `engine/src/recursor/dnssec/validator.rs`, `engine/src/recursor/dnssec/anchors.rs` — `DnssecRuntime` and the constructors Tasks 7–8 fill.
- `engine/src/runtime.rs` — `Runtime.resolution: Arc<ResolutionRuntime>` built in `Runtime::build`; `r:` entry in `filter_hashes`.
- `engine/src/server/mod.rs` — `Shared.recursor`, `Shared::with_recursor`, `WorkerForward`, the leader path of `resolve_miss` calls `recursor::dispatch::resolve_miss`.
- `engine/src/cache.rs` — `write_cached` clears AD for clients that set neither DO nor AD.
- `engine/src/edns.rs` — `ReplyOpt.ede: Option<u16>` written as option 15.
- `engine/src/main.rs` — creates the `RecursorState` from `boot.state_dir` and calls `detect_ipv6`.
- `engine/src/telemetry/metrics.rs` — `render`/`stats` take the `RecursorState`; exposition of every `RecursorMetrics` counter.
- `engine/src/control.rs`, `engine/src/server/tls.rs`, `engine/tests/telemetry_export.rs` — call sites of `render`/`stats` and the `QueryRecord` literal.
- `engine/src/telemetry/querylog.rs`, `engine/src/telemetry/otlp.rs` — `QueryRecord.route`, `QueryRecord.dnssec`, `QueryRecord.rpz_action` + OTLP attributes.

Interfaces:

```rust
// dispatch.rs
#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)] pub enum Mode { #[default] Forward, Recursive }
pub struct ForwardZone { pub domain: Name, pub servers: Vec<SocketAddr>, pub validate: bool }
#[derive(Default)] pub struct ForwardZones { by_wire: FxHashMap<Box<[u8]>, usize>, pub zones: Vec<ForwardZone> }
impl ForwardZones { pub fn build(z: &[proto::ForwardZone]) -> Result<Self, String>; pub fn longest_match(&self, qname_wire_lower: &[u8]) -> Option<&ForwardZone>; }
pub enum Route<'a> { Forward, Recursive, ForwardZone(&'a ForwardZone) }
#[derive(Default)]
pub struct ResolutionRuntime { pub mode: Mode, pub params: Arc<RecursionParams>, pub forward_zones: ForwardZones, pub dnssec: Arc<crate::recursor::dnssec::DnssecRuntime>, pub config_key: String }
impl ResolutionRuntime {
    pub fn build(s: &proto::ConfigSnapshot, blobs: &dyn crate::snapshot::BlobSource) -> Result<Self, String>;
    pub fn route(&self, qname_wire_lower: &[u8]) -> Route<'_>;
}
/// SHA-256 hex over the prost encoding of resolution_mode, recursion, forward_zones, dnssec and rpz_zones.
pub fn config_key(s: &proto::ConfigSnapshot) -> String;
/// Sends a complete DNS query (the client's bytes on the forward route) to the global upstreams.
pub trait ForwardUpstream { fn forward<'a>(&'a self, query_wire: &'a [u8]) -> LocalBoxFuture<'a, Result<Bytes, String>>; }
#[derive(Clone, Debug, Default)] pub enum RpzPending { #[default] None }
pub struct MissQuery<'a> { pub qname: Name, pub qtype: RecordType, pub client_ip: IpAddr, pub dnssec_ok: bool, pub checking_disabled: bool, pub authentic_data: bool, pub over_tcp: bool, pub query: &'a [u8], pub rpz: RpzPending }
impl<'a> MissQuery<'a> { pub fn from_view(q: &QueryView<'_>, query: &'a [u8], client_ip: IpAddr, transport: Transport, rpz: RpzPending) -> Option<Self>; } // None when the name does not decode
#[derive(Clone, Debug, PartialEq, Eq)] pub struct Ede { pub code: u16, pub text: String }
#[derive(Clone, Copy, Debug, PartialEq, Eq)] pub enum RouteTaken { Forward = 0, Recursive = 1, ForwardZone = 2 }
#[derive(Clone, Copy, Debug, PartialEq, Eq)] pub enum SecurityTag { None = 0, Secure = 1, Insecure = 2, Bogus = 3, Indeterminate = 4 }
pub struct MissAnswer {
    pub wire: Bytes,              // full response: the upstream's bytes on the forward route, else ID 0, lowercase question, no OPT
    pub cacheable: bool,
    pub failed: bool,             // no usable answer: the caller serves stale or SERVFAIL (wire is a SERVFAIL)
    pub ede: Option<Ede>,
    pub drop: bool,               // RPZ DROP
    pub route: RouteTaken,
    pub security: SecurityTag,
    pub rpz_action: u8,           // 0 none, then RpzAction discriminant + 1
}
pub async fn resolve_miss(rt: &ResolutionRuntime, state: &RecursorState, upstream: &dyn ForwardUpstream, q: &MissQuery<'_>) -> MissAnswer;
pub fn build_response(q: &MissQuery<'_>, rcode: ResponseCode, answers: &[Record], authorities: &[Record], secure: bool) -> Vec<u8>;
// mod.rs
pub struct RecursorState {
    pub metrics: Arc<RecursorMetrics>,
    pub recursor: Recursor,
    pub validator: dnssec::validator::Validator,          // Task 7
    pub anchors: Arc<dnssec::anchors::TrustAnchorStore>,  // Task 8
}
impl RecursorState { pub fn new(state_dir: Option<&Path>) -> Arc<Self>; } // None: nothing persisted (tests, Shared::new)
// server/mod.rs
pub struct Shared { /* M1/M2 fields */ pub recursor: Arc<RecursorState> }
impl Shared { pub fn new(workers: usize) -> Arc<Shared> /* = with_recursor(workers, RecursorState::new(None)) */; pub fn with_recursor(workers: usize, recursor: Arc<RecursorState>) -> Arc<Shared>; }
pub struct WorkerForward<'a> { pub set: &'a UpstreamSet, pub worker: &'a WorkerUpstreams, pub upstream_index: Cell<u8> }
impl ForwardUpstream for WorkerForward<'_> { /* wire::parse_query(query) -> Question; upstream::forward(set, worker, query, &question) */ }
// edns.rs
pub struct ReplyOpt { pub udp_size: u16, pub do_bit: bool, pub ext_rcode: u8, pub cookie: Option<([u8; 8], [u8; 16])>, pub ede: Option<u16> } // option 15, INFO-CODE only, no EXTRA-TEXT
// telemetry/metrics.rs
impl Metrics { pub fn render(&self, rt: &Runtime, recursor: &RecursorState) -> String; pub fn stats(&self, rt: &Runtime, recursor: &RecursorState) -> Stats; }
```

In this task `validator` and `anchors` are declared but `resolve_miss` does not call them yet (Tasks 7–8 add those calls); `DnssecRuntime` is created here as `#[derive(Default)] pub struct DnssecRuntime { pub validation: bool, pub ntas: Vec<(Name, i64)>, pub anchors: Vec<proto::TrustAnchor>, pub rfc5011: bool }` in `engine/src/recursor/dnssec/mod.rs`, built from `s.dnssec` in `ResolutionRuntime::build`.

- [ ] Create `engine/src/recursor/dispatch_tests.rs`:

```rust
use super::dispatch::*;
use super::testnet::{Behaviour, FakeNet, FakeZone};
use super::{LocalBoxFuture, RecursorState};
use crate::proto;
use crate::snapshot::DirBlobs;
use bytes::Bytes;
use hickory_proto::op::{Message, Query, ResponseCode};
use hickory_proto::rr::{rdata::A, Name, RData, RecordType};
use std::cell::Cell;
use std::net::Ipv4Addr;

const SOA: &str = "@ 300 IN SOA ns hostmaster 1 3600 600 86400 300\n";

struct NoUpstream(Cell<u32>);
impl ForwardUpstream for NoUpstream {
    fn forward<'a>(&'a self, _query: &'a [u8]) -> LocalBoxFuture<'a, Result<Bytes, String>> {
        self.0.set(self.0.get() + 1);
        Box::pin(async { Err("no upstream in this test".to_string()) })
    }
}

fn no_blobs() -> DirBlobs { DirBlobs { dir: std::path::PathBuf::from("/nonexistent/nexora-test-blobs") } }

async fn net() -> FakeNet {
    FakeNet::start(vec![
        FakeZone { origin: ".", ip: Ipv4Addr::new(127, 0, 54, 1), behaviour: Behaviour::Normal, text: format!("{SOA}@ 300 IN NS root.fake.\nroot.fake. 300 IN A 127.0.54.1\ncorp 300 IN NS ns.corp\nns.corp 300 IN A 127.0.54.3\npublic 300 IN NS ns.public\nns.public 300 IN A 127.0.54.3\n") },
        FakeZone { origin: "public.", ip: Ipv4Addr::new(127, 0, 54, 3), behaviour: Behaviour::Normal, text: format!("{SOA}@ 300 IN NS ns\nns 300 IN A 127.0.54.3\nwww 300 IN A 192.0.2.10\n") },
        FakeZone { origin: "corp.", ip: Ipv4Addr::new(127, 0, 54, 7), behaviour: Behaviour::Normal, text: format!("{SOA}@ 300 IN NS ns\nwww 300 IN A 10.0.0.10\n") },
    ]).await
}

fn snapshot(mode: proto::ResolutionMode, port: u16) -> proto::ConfigSnapshot {
    proto::ConfigSnapshot {
        resolution_mode: mode as i32,
        recursion: Some(proto::RecursionConfig { root_hints: vec![proto::RootHint { name: "root.fake.".into(), addresses: vec!["127.0.54.1".into()] }], qname_minimisation: true, aggressive_nsec: false, max_upstream_queries: 100, max_delegation_depth: 32, authority_port: port as u32 }),
        forward_zones: vec![proto::ForwardZone { domain: "corp.".into(), addresses: vec![format!("127.0.54.7:{port}")], validate: false }],
        ..Default::default()
    }
}

fn query_wire(name: &str) -> Vec<u8> {
    let mut m = Message::query();
    m.metadata.recursion_desired = true;
    m.queries.push(Query::query(Name::from_ascii(name).unwrap(), RecordType::A));
    m.to_vec().unwrap()
}

fn q<'a>(name: &str, wire: &'a [u8], dnssec_ok: bool) -> MissQuery<'a> {
    MissQuery { qname: Name::from_ascii(name).unwrap(), qtype: RecordType::A, client_ip: "127.0.0.1".parse().unwrap(), dnssec_ok, checking_disabled: false, authentic_data: false, over_tcp: false, query: wire, rpz: RpzPending::None }
}

fn wire(n: &str) -> Vec<u8> { Name::from_ascii(n).unwrap().to_lowercase().to_bytes().unwrap() }

#[test]
fn route_prefers_longest_forward_zone_then_mode() {
    let mut s = snapshot(proto::ResolutionMode::Recursive, 53);
    s.forward_zones.push(proto::ForwardZone { domain: "lab.corp.".into(), addresses: vec!["127.0.0.9:53".into()], validate: true });
    let rt = ResolutionRuntime::build(&s, &no_blobs()).unwrap();
    match rt.route(&wire("a.lab.corp.")) { Route::ForwardZone(z) => assert_eq!(z.domain, Name::from_ascii("lab.corp.").unwrap()), _ => panic!("expected lab.corp.") }
    match rt.route(&wire("corp.")) { Route::ForwardZone(z) => assert_eq!(z.domain, Name::from_ascii("corp.").unwrap()), _ => panic!("expected corp.") }
    assert!(matches!(rt.route(&wire("xcorp.")), Route::Recursive));
    let rt = ResolutionRuntime::build(&snapshot(proto::ResolutionMode::Unspecified, 53), &no_blobs()).unwrap();
    assert!(matches!(rt.route(&wire("www.public.")), Route::Forward));
    assert_ne!(config_key(&snapshot(proto::ResolutionMode::Forward, 53)), config_key(&snapshot(proto::ResolutionMode::Recursive, 53)), "a mode change must change the cache-invalidation key");
}

#[tokio::test(flavor = "current_thread")]
async fn recursive_mode_resolves_without_upstreams_and_forward_zone_overrides() {
    let net = net().await;
    let dir = tempfile::tempdir().unwrap();
    let state = RecursorState::new(Some(dir.path()));
    let rt = ResolutionRuntime::build(&snapshot(proto::ResolutionMode::Recursive, net.port), &no_blobs()).unwrap();
    let up = NoUpstream(Cell::new(0));
    let w = query_wire("www.public.");
    let a = resolve_miss(&rt, &state, &up, &q("www.public.", &w, false)).await;
    let m = Message::from_vec(&a.wire).unwrap();
    assert_eq!(m.metadata.response_code, ResponseCode::NoError);
    assert!(m.metadata.recursion_available);
    assert_eq!(m.answers[0].data, RData::A(A(Ipv4Addr::new(192, 0, 2, 10))));
    assert_eq!(a.route, RouteTaken::Recursive);
    assert!(a.cacheable && !a.failed);
    let w = query_wire("www.corp.");
    let b = resolve_miss(&rt, &state, &up, &q("www.corp.", &w, false)).await;
    let m = Message::from_vec(&b.wire).unwrap();
    assert_eq!(m.answers[0].data, RData::A(A(Ipv4Addr::new(10, 0, 0, 10))), "forward zone beats recursion (root delegates corp. elsewhere)");
    assert_eq!(b.route, RouteTaken::ForwardZone);
    assert_eq!(up.0.get(), 0, "global upstreams never used in recursive mode");
}

#[tokio::test(flavor = "current_thread")]
async fn forward_mode_sends_client_query_to_upstreams_and_failure_is_servfail_with_ede() {
    let state = RecursorState::new(None);
    let rt = ResolutionRuntime::build(&snapshot(proto::ResolutionMode::Forward, 53), &no_blobs()).unwrap();
    let up = NoUpstream(Cell::new(0));
    let w = query_wire("www.public.");
    let a = resolve_miss(&rt, &state, &up, &q("www.public.", &w, false)).await;
    assert_eq!(up.0.get(), 1);
    let m = Message::from_vec(&a.wire).unwrap();
    assert_eq!(m.metadata.response_code, ResponseCode::ServFail);
    assert_eq!(a.ede, Some(Ede { code: 22, text: "no reachable authority".into() }));
    assert!(a.failed && !a.cacheable);
    assert_eq!(a.route, RouteTaken::Forward);
}

#[tokio::test(flavor = "current_thread")]
async fn root_unreachable_is_servfail_ede_22() {
    let state = RecursorState::new(None);
    let mut s = snapshot(proto::ResolutionMode::Recursive, 1);
    s.recursion.as_mut().unwrap().root_hints[0].addresses = vec!["127.0.54.250".into()];
    let rt = ResolutionRuntime::build(&s, &no_blobs()).unwrap();
    let w = query_wire("www.public.");
    let a = resolve_miss(&rt, &state, &NoUpstream(Cell::new(0)), &q("www.public.", &w, false)).await;
    assert_eq!(Message::from_vec(&a.wire).unwrap().metadata.response_code, ResponseCode::ServFail);
    assert_eq!(a.ede.unwrap().code, 22);
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::dispatch_tests` — expect FAIL with "unresolved import `super::dispatch`".
- [ ] Implement `dispatch.rs`:
  - `ForwardZones::build` stores `domain.to_lowercase().to_bytes()` (uncompressed wire) → index. `longest_match` walks label offsets from the start of the name (offset 0, then `offset += 1 + wire[offset]` until the root label) and returns the first hit, which is the longest suffix; no allocation.
  - `route`: `longest_match` first; else `Mode::Recursive → Route::Recursive`, `Mode::Forward → Route::Forward`. `ResolutionMode::Unspecified` maps to `Mode::Forward`. `ResolutionRuntime::default()` (used by `Runtime::initial()`) is forward mode with no zones.
  - `config_key`: `sha2::Sha256` over `resolution_mode.to_be_bytes()`, then `prost::Message::encode_to_vec` of `recursion`, each `forward_zones` entry, `dnssec` and each `rpz_zones` entry, each prefixed by its `u32` length; hex-encoded.
  - `MissQuery::from_view`: `Name::from_vec(q.key.as_wire())` (the lowercase key), `dnssec_ok = q.do_bit()`, `checking_disabled = q.cd()`, `authentic_data = q.flags & 0x0020 != 0`, `over_tcp = transport != Transport::Udp`.
  - `resolve_miss`:
    - `Route::Recursive`: `WorkBudget::new(params.max_upstream_queries, params.max_delegation_depth)`; `state.recursor.resolve`; `metrics.resolutions_recursive += 1`.
    - `Route::ForwardZone(z)`: `metrics.resolutions_forward_zone += 1`; `Transport::exchange` with `recursion_desired: true`, `use_0x20: true`, `dnssec_ok: true`, `checking_disabled: true`, server chosen with `infra.select` over `z.servers` IPs (port taken from the matching `SocketAddr`), up to `z.servers.len()` attempts; answer/authority sections copied as received.
    - `Route::Forward`: `upstream.forward(q.query)` with the client's query bytes (M1 behaviour, byte for byte); `Ok(bytes)` → `MissAnswer { wire: bytes, cacheable: true, failed: false, route: Forward, security: None, .. }` without decoding (the M1 cache insert decides cacheability); `Err` → failure.
    - Failures build a SERVFAIL with `build_response`, set `failed: true`, `cacheable: false`, `metrics.resolution_failures += 1` and EDE: `NoReachableAuthority`/forward error → `22 "no reachable authority"`; `Deadline` → `22 "resolution deadline exceeded"`; `Limit(_)` → `metrics.limit_queries += 1` when `UpstreamQueries`, EDE `0 "work limit exceeded"`; `CnameLoop` → `0 "CNAME loop"`.
    - Success on the recursive and forward-zone routes: `build_response` then `cacheable = rcode ∈ {NoError, NXDomain}`.
  - `build_response`: `Message::response(0, OpCode::Query)`, RA=1, RD=1, AA=0, `authentic_data = secure && (q.dnssec_ok || q.authentic_data)`, CD copied from the query, question with lowercase `q.qname`; when `!q.dnssec_ok`, drop RRSIG/NSEC/NSEC3 records unless `q.qtype` is that type (RFC 4035 §3.2.1); `to_vec()`.
- [ ] Implement `RecursorState::new(state_dir)` (metrics, `Recursor::new`; in this task also `Validator::new(metrics.clone())` and `TrustAnchorStore::open(state_dir.map(|d| d.join("trust-anchors.json")))` as empty types with those constructors in `dnssec/validator.rs` and `dnssec/anchors.rs`, whose bodies Tasks 7–8 fill; `open(None)` keeps state in memory only).
- [ ] Integrate into the engine:
  - `runtime.rs`: `Runtime` gains `pub resolution: Arc<ResolutionRuntime>`; `Runtime::initial()` uses `Arc::new(ResolutionRuntime::default())`; `Runtime::build` sets `resolution: Arc::new(ResolutionRuntime::build(s, blobs).map_err(SnapshotError::Invalid)?)` and appends `format!("r:{}", resolution.config_key)` to `filter_hashes`, so the M2 clear-on-change also runs when any M3 section changes.
  - `edns.rs`: `ReplyOpt.ede: Option<u16>`; `wire_len` adds 6 when set; `write_opt` appends option code 15, length 2, INFO-CODE and bumps RDLENGTH. Every `ReplyOpt { .. }` literal (`server/mod.rs` `handle_packet`, the `edns.rs` tests) sets `ede: None`.
  - `cache.rs` `write_cached`: after the header is copied, `if q.flags & 0x0020 == 0 && !q.do_bit() { out[3] &= !0x20; }` (one byte, no allocation).
  - `server/mod.rs`: `Shared` gains `pub recursor: Arc<RecursorState>`, `Shared::new(workers)` becomes `Shared::with_recursor(workers, RecursorState::new(None))`. Add `WorkerForward` (its `forward` parses the question from the query bytes with `wire::parse_query`, calls `upstream::forward`, stores `upstream_index` clamped to `u8::MAX - 1` and maps the error with `to_string()`). In `resolve_miss`, the `Join::Leader` arm becomes:

```rust
Join::Leader(guard) => {
    let forward = WorkerForward { set: &rt.upstreams, worker: &ctx.upstreams, upstream_index: Cell::new(u8::MAX) };
    let Some(mq) = MissQuery::from_view(&q, &job.query, job.client.ip(), job.transport, RpzPending::None) else {
        guard.complete(Resolution::ServFail);
        return Vec::new();
    };
    let ans = recursor::dispatch::resolve_miss(&rt.resolution, &ctx.shared.recursor, &forward, &mq).await;
    rec.upstream = forward.upstream_index.get();
    (rec.route, rec.dnssec, rec.rpz_action) = (ans.route as u8, ans.security as u8, ans.rpz_action);
    ede = ans.ede.as_ref().map(|e| e.code);
    let answer = if ans.failed || ans.drop {
        None
    } else if ans.cacheable {
        leader_answer(&ctx, &rt, policy, &q, job.key, ans.wire, &mut rec)
    } else {
        Some(ans.wire)
    };
    dropped = ans.drop;
    guard.complete(match &answer { Some(bytes) => Resolution::Answer(bytes.clone()), None => Resolution::ServFail });
    answer
}
```

- [ ] Finish the integration:
  - In that arm `ede: Option<u16>` and `dropped: bool` are declared before the `match`; after it, `if dropped { return Vec::new(); }` and the serve step uses `let opt = job.opt.map(|o| ReplyOpt { ede, ..o });`. `leader_answer` inserts into the cache only when `Arc::ptr_eq(&rt, &ctx.shared.runtime.load_full())` still holds (a snapshot applied during resolution may have cleared the cache).
  - `main.rs`: `let shared = Shared::with_recursor(boot.worker_count(), RecursorState::new(Some(&boot.state_dir))); shared.recursor.recursor.detect_ipv6();`.
  - `telemetry/metrics.rs`: `render(&self, rt, recursor)` and `stats(&self, rt, recursor)`; update the call sites `scrape` (`shared.metrics.render(&shared.runtime.load(), &shared.recursor)`), the `encrypted_metrics_render` test and `server/tls.rs` test (`&RecursorState::new(None)`), `control.rs` ticker (`shared.metrics.stats(&shared.runtime.load(), &shared.recursor)`) and `engine/tests/telemetry_export.rs`. `render` registers (counters without the `_total` suffix, every label value created even at zero so the e2e harness finds the family): `nexora_recursor_upstream_queries_total`, `nexora_recursor_upstream_timeouts_total`, `nexora_recursor_mismatched_replies_total{reason="id|question|case|malformed"}`, `nexora_recursor_tcp_fallback_total`, `nexora_recursor_edns_fallback_total`, `nexora_recursor_lame_servers_total`, `nexora_recursor_work_limit_exceeded_total{limit="upstream_queries|delegation_depth|cname_depth"}`, `nexora_recursor_cname_loops_total`, `nexora_resolutions_total{route="recursive|forward_zone"}`, `nexora_resolution_failures_total`, gauge `nexora_recursor_infra_entries`, `nexora_dnssec_validations_total{result="secure|insecure|bogus|indeterminate"}`, `nexora_dnssec_bogus_total{ede="<code>"}` (only non-zero codes), `nexora_dnssec_aggressive_synthesized_total`, `nexora_dnssec_trust_anchor_refresh_failures_total`.
  - `telemetry/querylog.rs`: `QueryRecord` gains `route: u8`, `dnssec: u8`, `rpz_action: u8` (set to 0 in `Scope::record` and in the `record` literal of `engine/tests/telemetry_export.rs`); `otlp::log_record` adds attributes `nexora.route` (`forward|recursive|forward_zone`), `nexora.dnssec` (`none|secure|insecure|bogus|indeterminate`), `nexora.rpz` (`none|nxdomain|nodata|passthru|drop|tcp_only|local_data|disabled`).
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::dispatch_tests` — expect PASS (4 tests).
- [ ] Run the whole engine suite to prove the hot path is untouched: `scripts/dev-exec.sh make engine-test` — expect PASS including `cache_hit_path_does_not_allocate`, `policy_pipeline` and `server_pipeline` (forward mode is the default and forwards the client's bytes).
- [ ] Commit: `git add engine && git commit -m "feat(engine): resolution mode selection, forward zones and recursive miss path"`.

## Task 6: DNSSEC primitives — signature verification, validity windows, DS matching, NSEC/NSEC3 denial

Files:

- `engine/src/recursor/dnssec/mod.rs` — module declarations (`verify`, `denial`, `validator`, `anchors`, `nsec_cache`, `#[cfg(test)] testsign`), `DnssecRuntime`.
- `engine/src/recursor/dnssec/verify.rs` — RRSIG verification over hickory's `Verifier`, RFC 4034 §3.1.5 time arithmetic with skew, algorithm/digest support, DS matching.
- `engine/src/recursor/dnssec/denial.rs` — canonical ordering, NSEC (RFC 4035 §5.4) and NSEC3 (RFC 5155 §8, RFC 9276) proofs.
- `engine/src/recursor/dnssec/testsign.rs` — `#[cfg(test)]` signing helpers (ECDSA P-256 via `EcdsaSigningKey`).
- `engine/src/recursor/dnssec/primitives_tests.rs` — unit tests.

Interfaces:

```rust
// verify.rs
pub const SUPPORTED_ALGORITHMS: [u8; 4] = [8, 13, 14, 15];
#[derive(Debug, PartialEq, Eq, Clone, Copy)] pub enum Validity { Valid, NotYetValid, Expired, Malformed }
pub fn within_validity(inception: u32, expiration: u32, now_unix: u64) -> Validity;
pub fn algorithm_supported(a: Algorithm) -> bool;          // u8::from(a) in SUPPORTED_ALGORITHMS
pub fn digest_supported(d: DigestType) -> bool;             // SHA1, SHA256, SHA384
#[derive(Debug, PartialEq, Eq, Clone, Copy)]
pub enum VerifyError { NoRrsig, NoMatchingKey, UnsupportedAlgorithm, Expired, NotYetValid, BadSignature, SignerMismatch, LabelCount }
#[derive(Debug, Clone, Copy)] pub struct VerifiedSig { pub key_tag: u16, pub expiration: u32, pub original_ttl: u32, pub wildcard_expanded: bool }
pub fn verify_rrset(rrset: &[Record], rrsigs: &[Record], keys: &[DNSKEY], zone: &Name, now_unix: u64) -> Result<VerifiedSig, VerifyError>;
pub fn capped_ttl(rrset_ttl: u32, sig: &VerifiedSig, now_unix: u64) -> u32; // min(ttl, original_ttl, expiration - now)
#[derive(Debug)] pub enum DsMatch { Matched(Vec<DNSKEY>), NoSupported, NoMatch }
pub fn match_ds(zone: &Name, dnskeys: &[DNSKEY], ds: &[DS]) -> DsMatch;
// denial.rs
pub const NSEC3_INSECURE_ITERATIONS: u16 = 50;
pub const NSEC3_BOGUS_ITERATIONS: u16 = 150;
#[derive(Debug, PartialEq, Eq, Clone, Copy)]
pub enum Denial { Proven, ProvenOptOut, NotProven(&'static str), InsecureIterations(u16), BogusIterations(u16) }
pub fn canonical_cmp(a: &Name, b: &Name) -> Ordering;
pub fn nsec_covers(owner: &Name, next: &Name, name: &Name) -> bool;
pub fn nsec_proves_nxdomain(qname: &Name, nsecs: &[(Name, NSEC)]) -> Denial;
pub fn nsec_proves_nodata(qname: &Name, qtype: RecordType, nsecs: &[(Name, NSEC)]) -> Denial;
pub fn nsec_proves_no_closer_match(qname: &Name, rrsig_labels: u8, nsecs: &[(Name, NSEC)]) -> Denial;
pub fn nsec3_proves_nxdomain(qname: &Name, zone: &Name, nsec3s: &[(Name, NSEC3)]) -> Denial;
pub fn nsec3_proves_nodata(qname: &Name, qtype: RecordType, zone: &Name, nsec3s: &[(Name, NSEC3)]) -> Denial;
// testsign.rs (cfg(test))
pub struct TestKey { pub zone: Name, pub dnskey: DNSKEY, key: EcdsaSigningKey }
impl TestKey {
    pub fn generate(zone: &str, ksk: bool) -> Self;
    pub fn ds(&self) -> DS;
    pub fn dnskey_record(&self) -> Record;
    pub fn sign(&self, rrset: &[Record], inception: u32, expiration: u32) -> Record;
}
pub fn nsec_chain(zone: &str, names: &[(&str, &[RecordType])]) -> Vec<(Name, NSEC)>;
pub fn nsec3_chain(zone: &str, names: &[(&str, &[RecordType])], iterations: u16, opt_out: bool) -> Vec<(Name, NSEC3)>;
```

- [ ] Create `engine/src/recursor/dnssec/testsign.rs`:

```rust
use hickory_proto::dnssec::crypto::EcdsaSigningKey;
use hickory_proto::dnssec::rdata::{sig::SigInput, DNSSECRData, DNSKEY, DS, NSEC, NSEC3, RRSIG};
use hickory_proto::dnssec::{Algorithm, DigestType, Nsec3HashAlgorithm, SigningKey, TBS};
use hickory_proto::rr::{DNSClass, Name, RData, Record, RecordType, SerialNumber};

pub struct TestKey { pub zone: Name, pub dnskey: DNSKEY, key: EcdsaSigningKey }

impl TestKey {
    pub fn generate(zone: &str, ksk: bool) -> Self {
        let pkcs8 = EcdsaSigningKey::generate_pkcs8(Algorithm::ECDSAP256SHA256).unwrap();
        let key = EcdsaSigningKey::from_pkcs8(&pkcs8, Algorithm::ECDSAP256SHA256).unwrap();
        let dnskey = DNSKEY::new(true, ksk, false, key.to_public_key().unwrap());
        TestKey { zone: Name::from_ascii(zone).unwrap(), dnskey, key }
    }
    pub fn ds(&self) -> DS {
        let digest = self.dnskey.to_digest(&self.zone, DigestType::SHA256).unwrap();
        DS::new(self.dnskey.calculate_key_tag().unwrap(), Algorithm::ECDSAP256SHA256, DigestType::SHA256, digest.as_ref().to_vec())
    }
    pub fn dnskey_record(&self) -> Record {
        Record::from_rdata(self.zone.clone(), 3600, RData::DNSSEC(DNSSECRData::DNSKEY(self.dnskey.clone())))
    }
    pub fn sign(&self, rrset: &[Record], inception: u32, expiration: u32) -> Record {
        let first = &rrset[0];
        let input = SigInput {
            type_covered: first.record_type(),
            algorithm: Algorithm::ECDSAP256SHA256,
            num_labels: first.name.num_labels(),
            original_ttl: first.ttl,
            sig_expiration: SerialNumber::new(expiration),
            sig_inception: SerialNumber::new(inception),
            key_tag: self.dnskey.calculate_key_tag().unwrap(),
            signer_name: self.zone.clone(),
        };
        let tbs = TBS::from_input(&first.name, DNSClass::IN, &input, rrset.iter()).unwrap();
        let sig = self.key.sign(&tbs).unwrap();
        Record::from_rdata(first.name.clone(), first.ttl, RData::DNSSEC(DNSSECRData::RRSIG(RRSIG::from_sig(input, sig))))
    }
}

pub fn nsec_chain(zone: &str, names: &[(&str, &[RecordType])]) -> Vec<(Name, NSEC)> {
    let mut v: Vec<(Name, Vec<RecordType>)> = names.iter().map(|(n, t)| (Name::from_ascii(n).unwrap(), t.to_vec())).collect();
    v.sort_by(|a, b| super::denial::canonical_cmp(&a.0, &b.0));
    let _ = zone;
    (0..v.len()).map(|i| {
        let next = v[(i + 1) % v.len()].0.clone();
        let mut types = v[i].1.clone();
        types.extend([RecordType::RRSIG, RecordType::NSEC]);
        (v[i].0.clone(), NSEC::new(next, types))
    }).collect()
}

pub fn nsec3_chain(zone: &str, names: &[(&str, &[RecordType])], iterations: u16, opt_out: bool) -> Vec<(Name, NSEC3)> {
    let z = Name::from_ascii(zone).unwrap();
    let mut hashed: Vec<(Vec<u8>, Vec<RecordType>)> = names.iter().map(|(n, t)| {
        let h = Nsec3HashAlgorithm::SHA1.hash(&[], &Name::from_ascii(n).unwrap(), iterations).unwrap();
        (h.as_ref().to_vec(), t.to_vec())
    }).collect();
    hashed.sort();
    (0..hashed.len()).map(|i| {
        let next = hashed[(i + 1) % hashed.len()].0.clone();
        let label = data_encoding::BASE32HEX_NOPAD.encode(&hashed[i].0).to_ascii_lowercase();
        let owner = Name::from_ascii(&label).unwrap().append_domain(&z).unwrap();
        (owner, NSEC3::new(Nsec3HashAlgorithm::SHA1, opt_out, iterations, vec![], next, hashed[i].1.clone()))
    }).collect()
}
```

- [ ] Create `engine/src/recursor/dnssec/primitives_tests.rs`:

```rust
use super::denial::*;
use super::testsign::{nsec3_chain, nsec_chain, TestKey};
use super::verify::*;
use hickory_proto::dnssec::{Algorithm, DigestType};
use hickory_proto::rr::{rdata::A, Name, RData, Record, RecordType};
use std::cmp::Ordering;
use std::net::Ipv4Addr;

const NOW: u32 = 1_800_000_000;

fn a_rrset(name: &str) -> Vec<Record> {
    vec![Record::from_rdata(Name::from_ascii(name).unwrap(), 300, RData::A(A(Ipv4Addr::new(192, 0, 2, 1))))]
}

#[test]
fn validity_window_with_skew_and_wraparound() {
    let inc = NOW - 86_400; let exp = NOW + 86_400; // span 2 days -> skew clamps to 3600
    assert_eq!(within_validity(inc, exp, NOW as u64), Validity::Valid);
    assert_eq!(within_validity(inc, exp, (exp + 3600) as u64), Validity::Valid);
    assert_eq!(within_validity(inc, exp, (exp + 3601) as u64), Validity::Expired);
    assert_eq!(within_validity(inc, exp, (inc - 3600) as u64), Validity::Valid);
    assert_eq!(within_validity(inc, exp, (inc - 3601) as u64), Validity::NotYetValid);
    // span 1 hour -> skew clamps up to 300
    assert_eq!(within_validity(NOW, NOW + 3600, (NOW + 3900) as u64), Validity::Valid);
    assert_eq!(within_validity(NOW, NOW + 3600, (NOW + 3901) as u64), Validity::Expired);
    // RFC 1982 wraparound: inception just before 2^32, expiration after
    assert_eq!(within_validity(0xFFFF_FF00, 0x0001_0000, 0x1_0000_0010), Validity::Valid);
    assert_eq!(within_validity(NOW + 10, NOW, NOW as u64), Validity::Malformed);
}

#[test]
fn only_algorithms_8_13_14_15_are_supported() {
    for (a, ok) in [(Algorithm::RSASHA256, true), (Algorithm::ECDSAP256SHA256, true), (Algorithm::ECDSAP384SHA384, true), (Algorithm::ED25519, true), (Algorithm::RSASHA1, false), (Algorithm::RSASHA512, false), (Algorithm::Unknown(16), false)] {
        assert_eq!(algorithm_supported(a), ok, "{a:?}");
    }
    assert!(digest_supported(DigestType::SHA256));
    assert!(!digest_supported(DigestType::Unknown(3)));
}

#[test]
fn verifies_good_signature_and_rejects_tampering_expiry_and_wrong_key() {
    let zsk = TestKey::generate("example.", false);
    let other = TestKey::generate("example.", false);
    let rrset = a_rrset("www.example.");
    let sig = zsk.sign(&rrset, NOW - 3600, NOW + 86_400);
    let zone = Name::from_ascii("example.").unwrap();
    let v = verify_rrset(&rrset, &[sig.clone()], &[zsk.dnskey.clone()], &zone, NOW as u64).unwrap();
    assert!(!v.wildcard_expanded);
    let mut tampered = rrset.clone();
    tampered[0].data = RData::A(A(Ipv4Addr::new(6, 6, 6, 6)));
    assert_eq!(verify_rrset(&tampered, &[sig.clone()], &[zsk.dnskey.clone()], &zone, NOW as u64).unwrap_err(), VerifyError::BadSignature);
    assert_eq!(verify_rrset(&rrset, &[sig.clone()], &[other.dnskey.clone()], &zone, NOW as u64).unwrap_err(), VerifyError::NoMatchingKey);
    assert_eq!(verify_rrset(&rrset, &[sig.clone()], &[zsk.dnskey.clone()], &zone, (NOW + 86_400 + 7200) as u64).unwrap_err(), VerifyError::Expired);
    assert_eq!(verify_rrset(&rrset, &[], &[zsk.dnskey.clone()], &zone, NOW as u64).unwrap_err(), VerifyError::NoRrsig);
    assert_eq!(verify_rrset(&rrset, &[sig], &[zsk.dnskey.clone()], &Name::from_ascii("other.").unwrap(), NOW as u64).unwrap_err(), VerifyError::SignerMismatch);
}

#[test]
fn ttl_is_capped_by_original_ttl_and_expiration() {
    let zsk = TestKey::generate("example.", false);
    let rrset = a_rrset("www.example.");
    let sig = zsk.sign(&rrset, NOW - 3600, NOW + 100);
    let v = verify_rrset(&rrset, &[sig], &[zsk.dnskey.clone()], &Name::from_ascii("example.").unwrap(), NOW as u64).unwrap();
    assert_eq!(capped_ttl(3600, &v, NOW as u64), 100);
    assert_eq!(capped_ttl(50, &v, NOW as u64), 50);
}

#[test]
fn ds_matching() {
    let ksk = TestKey::generate("example.", true);
    let other = TestKey::generate("example.", true);
    let zone = Name::from_ascii("example.").unwrap();
    match match_ds(&zone, &[ksk.dnskey.clone(), other.dnskey.clone()], &[ksk.ds()]) { DsMatch::Matched(k) => assert_eq!(k, vec![ksk.dnskey.clone()]), m => panic!("{m:?}") }
    assert!(matches!(match_ds(&zone, &[other.dnskey.clone()], &[ksk.ds()]), DsMatch::NoMatch));
    let unsupported = hickory_proto::dnssec::rdata::DS::new(1, Algorithm::Unknown(200), DigestType::SHA256, vec![0; 32]);
    assert!(matches!(match_ds(&zone, &[ksk.dnskey.clone()], &[unsupported]), DsMatch::NoSupported));
}

#[test]
fn canonical_order_rfc4034_section_6_1() {
    let names = ["example.", "a.example.", "yljkjljk.a.example.", "Z.a.example.", "zABC.a.EXAMPLE.", "z.example.", "\\001.z.example.", "*.z.example.", "\\200.z.example."];
    let parsed: Vec<Name> = names.iter().map(|n| Name::from_ascii(n).unwrap()).collect();
    for w in parsed.windows(2) { assert_eq!(canonical_cmp(&w[0], &w[1]), Ordering::Less, "{} < {}", w[0], w[1]); }
}

#[test]
fn nsec_nxdomain_and_nodata_proofs() {
    let chain = nsec_chain("example.", &[("example.", &[RecordType::SOA, RecordType::NS]), ("a.example.", &[RecordType::A]), ("d.example.", &[RecordType::A, RecordType::TXT])]);
    let q = Name::from_ascii("b.example.").unwrap();
    assert_eq!(nsec_proves_nxdomain(&q, &chain), Denial::Proven);
    assert_eq!(nsec_proves_nxdomain(&q, &chain[1..2]), Denial::NotProven("no NSEC covers the wildcard"));
    assert_eq!(nsec_proves_nxdomain(&Name::from_ascii("a.example.").unwrap(), &chain), Denial::NotProven("name exists"));
    assert_eq!(nsec_proves_nodata(&Name::from_ascii("d.example.").unwrap(), RecordType::AAAA, &chain), Denial::Proven);
    assert_eq!(nsec_proves_nodata(&Name::from_ascii("d.example.").unwrap(), RecordType::TXT, &chain), Denial::NotProven("type present in bitmap"));
}

#[test]
fn nsec3_nxdomain_nodata_opt_out_and_rfc9276_iterations() {
    let names: [(&str, &[RecordType]); 3] = [("example.", &[RecordType::SOA, RecordType::NS]), ("a.example.", &[RecordType::A]), ("c.example.", &[RecordType::A])];
    let zone = Name::from_ascii("example.").unwrap();
    let q = Name::from_ascii("b.example.").unwrap();
    let chain = nsec3_chain("example.", &names, 0, false);
    assert_eq!(nsec3_proves_nxdomain(&q, &zone, &chain), Denial::Proven);
    assert_eq!(nsec3_proves_nodata(&Name::from_ascii("a.example.").unwrap(), RecordType::TXT, &zone, &chain), Denial::Proven);
    assert_eq!(nsec3_proves_nodata(&Name::from_ascii("a.example.").unwrap(), RecordType::A, &zone, &chain), Denial::NotProven("type present in bitmap"));
    let opt_out = nsec3_chain("example.", &names, 0, true);
    assert_eq!(nsec3_proves_nodata(&Name::from_ascii("sub.b.example.").unwrap(), RecordType::DS, &zone, &opt_out), Denial::ProvenOptOut);
    assert_eq!(nsec3_proves_nxdomain(&q, &zone, &nsec3_chain("example.", &names, 51, false)), Denial::InsecureIterations(51));
    assert_eq!(nsec3_proves_nxdomain(&q, &zone, &nsec3_chain("example.", &names, 151, false)), Denial::BogusIterations(151));
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::dnssec::primitives_tests` — expect FAIL with "unresolved import `super::denial`".
- [ ] Implement `within_validity` (RFC 4034 §3.1.5 arithmetic modulo 2^32; skew is 10% of the signature lifetime clamped to 300..=3600 s so signatures on hosts with modest clock skew still validate near both edges):

```rust
pub fn within_validity(inception: u32, expiration: u32, now_unix: u64) -> Validity {
    let now = now_unix as u32;
    let span = expiration.wrapping_sub(inception);
    if span == 0 || span > i32::MAX as u32 { return Validity::Malformed; }
    let skew = (span / 10).clamp(300, 3600);
    if (now.wrapping_sub(inception.wrapping_sub(skew)) as i32) < 0 { return Validity::NotYetValid; }
    if (expiration.wrapping_add(skew).wrapping_sub(now) as i32) < 0 { return Validity::Expired; }
    Validity::Valid
}
```

- [ ] Implement `verify_rrset`: filter `rrsigs` to RRSIG records whose `type_covered == rrset[0].record_type()` and owner equals the RRset owner (case-insensitive); none → `NoRrsig`. For each candidate: signer name must equal `zone` → else remember `SignerMismatch`; `!algorithm_supported` → remember `UnsupportedAlgorithm`; `num_labels > owner.num_labels()` (owner labels excluding a leading `*`) → `LabelCount`; `within_validity` not `Valid` → remember `Expired`/`NotYetValid`; keys = `keys` with `zone_key()`, `!revoke()`, same algorithm, `calculate_key_tag() == key_tag`; none → remember `NoMatchingKey`; for each key `key.verify_rrsig(&owner, DNSClass::IN, rrsig, rrset.iter())` → first `Ok` returns `VerifiedSig { wildcard_expanded: num_labels < owner labels, .. }`, all `Err` → remember `BadSignature`. When no candidate verifies, return the remembered error with precedence `BadSignature > Expired > NotYetValid > NoMatchingKey > UnsupportedAlgorithm > SignerMismatch > LabelCount` (the most specific failure). `capped_ttl` returns `min(rrset_ttl, original_ttl, expiration.wrapping_sub(now) as u32)`.
- [ ] Implement `match_ds`: `supported` = DS with `algorithm_supported` and `digest_supported`; empty → `NoSupported`; when a SHA-256 or SHA-384 DS exists, ignore SHA-1 DS for the same key tag (RFC 4509 §3); `Matched` keys are those with `ds.covers(zone, key) == Ok(true)` and `secure_entry_point` ignored (any zone key may be referenced); none → `NoMatch`.
- [ ] Implement `denial.rs`:

```rust
pub fn canonical_cmp(a: &Name, b: &Name) -> Ordering {
    let al: Vec<Vec<u8>> = a.iter().map(|l| l.to_ascii_lowercase()).collect();
    let bl: Vec<Vec<u8>> = b.iter().map(|l| l.to_ascii_lowercase()).collect();
    for (x, y) in al.iter().rev().zip(bl.iter().rev()) {
        match x.cmp(y) { Ordering::Equal => continue, o => return o }
    }
    al.len().cmp(&bl.len())
}

pub fn nsec_covers(owner: &Name, next: &Name, name: &Name) -> bool {
    if canonical_cmp(owner, next) == Ordering::Less {
        canonical_cmp(owner, name) == Ordering::Less && canonical_cmp(name, next) == Ordering::Less
    } else {
        // last NSEC in the zone wraps to the apex
        canonical_cmp(owner, name) == Ordering::Less || canonical_cmp(name, next) == Ordering::Less
    }
}

fn nsec3_hash_label(name: &Name, n: &NSEC3) -> Option<Vec<u8>> {
    n.hash_algorithm().hash(n.salt(), name, n.iterations()).ok().map(|d| d.as_ref().to_vec())
}

fn nsec3_owner_hash(owner: &Name) -> Option<Vec<u8>> {
    let first = owner.iter().next()?;
    data_encoding::BASE32HEX_NOPAD.decode(&first.to_ascii_uppercase()).ok()
}

fn nsec3_covers_hash(owner_hash: &[u8], next: &[u8], h: &[u8]) -> bool {
    if owner_hash < next { owner_hash < h && h < next } else { owner_hash < h || h < next }
}
```

- `nsec_proves_nxdomain`: any NSEC owner equal to `qname` → `NotProven("name exists")`; need one NSEC with `nsec_covers(owner, next, qname)` (else `NotProven("no NSEC covers qname")`); closest encloser = the longer of the common ancestors of `qname` with that NSEC's owner and with its next name; need an NSEC covering or matching-without-the-type `*.<closest encloser>` (else `NotProven("no NSEC covers the wildcard")`). Ignore NSECs whose bitmap has `NS` without `SOA` when proving names below their owner (ancestor delegation, `is_ancestor_delegation`).
- `nsec_proves_nodata`: an NSEC whose owner equals `qname` with neither `qtype` nor `CNAME` in `type_set()` → `Proven` (`qtype` present → `NotProven("type present in bitmap")`); for `qtype == DS` the NSEC must not contain `SOA` (must be the parent side); wildcard NODATA: a covering NSEC for `qname` plus an NSEC matching `*.<closest encloser>` without `qtype` → `Proven`.
- `nsec_proves_no_closer_match(qname, rrsig_labels, nsecs)`: next closer name = `qname.trim_to(rrsig_labels + 1)` must be covered by an NSEC.
- NSEC3 functions first check iterations of every supplied record: any `> NSEC3_BOGUS_ITERATIONS` → `BogusIterations(n)`; any `> NSEC3_INSECURE_ITERATIONS` → `InsecureIterations(n)` (RFC 9276 §3.2); unknown hash algorithm or non-empty flags other than opt-out → ignore that record.
- `nsec3_proves_nxdomain`: find closest encloser `ce` by walking `qname`'s ancestors down to `zone` and taking the first whose hash matches an NSEC3 owner hash; `nc` = the ancestor of `qname` one label longer than `ce`; require an NSEC3 covering `H(nc)` (`NotProven("next closer not covered")`); if that NSEC3 has opt-out → `ProvenOptOut`; require an NSEC3 covering `H(*.ce)` (`NotProven("wildcard not covered")`); else `Proven`.
- `nsec3_proves_nodata`: an NSEC3 matching `H(qname)` whose bitmap lacks `qtype` and `CNAME` → `Proven` (present → `NotProven("type present in bitmap")`); if no match and `qtype == DS`: closest encloser proof where the NSEC3 covering the next closer has opt-out → `ProvenOptOut` (RFC 5155 §8.6); otherwise `NotProven("no matching NSEC3")`.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::dnssec::primitives_tests` — expect PASS (8 tests).
- [ ] Commit: `git add engine/src/recursor/dnssec && git commit -m "feat(dnssec): signature verification, DS matching and NSEC/NSEC3 denial proofs"`.

## Task 7: Validator — chain of trust, EDE, NTAs, CD/AD, forward-zone validation, aggressive NSEC

Files:

- `engine/src/recursor/dnssec/validator.rs` — `Validator`, `Fetcher`, chain-of-trust walk, per-response validation, EDE mapping.
- `engine/src/recursor/dnssec/nsec_cache.rs` — RFC 8198 aggressive use of validated NSEC/NSEC3.
- `engine/src/recursor/dnssec/validator_tests.rs` — unit tests with an in-memory signed hierarchy.
- `engine/src/recursor/dispatch.rs` — `RoutedFetcher`; validation, CD and NTA handling in `resolve_miss`; aggressive-NSEC lookup before recursion.

Interfaces:

```rust
// validator.rs
pub struct FetchedSet { pub rcode: ResponseCode, pub answers: Vec<Record>, pub authorities: Vec<Record> }
#[derive(Debug, Clone)] pub enum FetchError { Unreachable, Limit }
pub trait Fetcher { fn fetch<'a>(&'a self, name: &'a Name, rtype: RecordType) -> crate::recursor::LocalBoxFuture<'a, Result<FetchedSet, FetchError>>; }
#[derive(Debug, Clone)] pub enum TrustPoint { Ds(Vec<DS>), Keys(Vec<DNSKEY>) }
#[derive(Debug, Clone, Default)] pub struct TrustPoints { pub zones: Vec<(Name, TrustPoint)> }
impl TrustPoints { pub fn from_config(anchors: &[proto::TrustAnchor]) -> Self; pub fn closest(&self, name: &Name) -> Option<&(Name, TrustPoint)>; }
#[derive(Debug, Clone, PartialEq, Eq)] pub enum Security { Secure, Insecure(Option<Ede>), Bogus(Ede), Indeterminate(Ede) }
pub struct ValidationInput<'a> { pub qname: &'a Name, pub qtype: RecordType, pub rcode: ResponseCode, pub answers: &'a [Record], pub authorities: &'a [Record] }
pub struct ValidationResult { pub security: Security, pub ttl_cap: Option<u32> }
pub struct Validator { metrics: Arc<RecursorMetrics>, zones: quick_cache::sync::Cache<Name, Arc<(ZoneState, u64)>>, pub nsec: AggressiveNsecCache }
#[derive(Debug, Clone)] pub enum ZoneState { Secure(Vec<DNSKEY>), Insecure(Option<Ede>), Bogus(Ede) }
impl Validator {
    pub fn new(metrics: Arc<RecursorMetrics>) -> Self;
    pub async fn validate(&self, input: &ValidationInput<'_>, trust: &TrustPoints, ntas: &[(Name, i64)], fetcher: &dyn Fetcher, now_unix: u64) -> ValidationResult;
    pub async fn zone_state(&self, zone: &Name, trust: &TrustPoints, fetcher: &dyn Fetcher, now_unix: u64) -> ZoneState;
}
pub fn under_nta(name: &Name, ntas: &[(Name, i64)], now_unix: u64) -> bool;
// nsec_cache.rs
pub struct AggressiveNsecCache { /* per zone: BTreeMap of canonical key -> (owner, NSEC, rrsig, expires); SOA record per zone */ }
impl AggressiveNsecCache {
    pub fn new(max_zones: usize) -> Self;
    pub fn insert_secure(&self, zone: &Name, soa: &[Record], denial_records: &[Record], now_unix: u64);
    pub fn synthesize(&self, qname: &Name, qtype: RecordType, now_unix: u64) -> Option<(ResponseCode, Vec<Record>)>; // authorities incl. SOA, NSEC(3), RRSIGs
}
// dispatch.rs
pub struct RoutedFetcher<'a> { pub rt: &'a ResolutionRuntime, pub state: &'a RecursorState, pub upstream: &'a dyn ForwardUpstream, pub budget: &'a WorkBudget }
impl Fetcher for RoutedFetcher<'_> { /* route(name) -> Recursor::fetch | forward-zone exchange | upstream.forward of a dispatch-built query with DO=1 CD=1 */ }
```

EDE codes produced (RFC 8914): 1 Unsupported DNSKEY Algorithm (insecure), 2 Unsupported DS Digest Type (insecure), 6 DNSSEC Bogus (bad signature), 7 Signature Expired, 8 Signature Not Yet Valid, 9 DNSKEY Missing, 10 RRSIGs Missing, 12 NSEC Missing, 22 No Reachable Authority, 23 Network Error (indeterminate), 27 Unsupported NSEC3 Iterations Value (insecure above 50, bogus above 150).

- [ ] Create `engine/src/recursor/dnssec/validator_tests.rs`:

```rust
use super::testsign::{nsec_chain, TestKey};
use super::validator::*;
use crate::recursor::dispatch::Ede;
use crate::recursor::metrics::RecursorMetrics;
use crate::recursor::LocalBoxFuture;
use hickory_proto::dnssec::rdata::{DNSSECRData, DS};
use hickory_proto::dnssec::{Algorithm, DigestType};
use hickory_proto::op::ResponseCode;
use hickory_proto::rr::{rdata::{A, NS, SOA}, Name, RData, Record, RecordType};
use std::cell::RefCell;
use std::collections::HashMap;
use std::net::Ipv4Addr;
use std::sync::Arc;

const NOW: u64 = 1_800_000_000;
const INC: u32 = (NOW - 3600) as u32;
const EXP: u32 = (NOW + 86_400) as u32;

fn n(s: &str) -> Name { Name::from_ascii(s).unwrap() }
fn rec(name: &str, data: RData) -> Record { Record::from_rdata(n(name), 300, data) }

#[derive(Default)]
struct Mock { sets: HashMap<(Name, RecordType), FetchedSet>, calls: RefCell<u32> }
impl Fetcher for Mock {
    fn fetch<'a>(&'a self, name: &'a Name, rtype: RecordType) -> LocalBoxFuture<'a, Result<FetchedSet, FetchError>> {
        *self.calls.borrow_mut() += 1;
        let r = self.sets.get(&(name.to_lowercase(), rtype)).map(|s| FetchedSet { rcode: s.rcode, answers: s.answers.clone(), authorities: s.authorities.clone() }).ok_or(FetchError::Unreachable);
        Box::pin(async move { r })
    }
}

struct World { root: TestKey, example: TestKey, mock: Mock, trust: TrustPoints }

fn signed(key: &TestKey, set: Vec<Record>) -> Vec<Record> { let s = key.sign(&set, INC, EXP); let mut v = set; v.push(s); v }

fn world() -> World {
    let root = TestKey::generate(".", true);
    let example = TestKey::generate("example.", true);
    let mut mock = Mock::default();
    let ok = |answers: Vec<Record>| FetchedSet { rcode: ResponseCode::NoError, answers, authorities: vec![] };
    mock.sets.insert((n("."), RecordType::DNSKEY), ok(signed(&root, vec![root.dnskey_record()])));
    mock.sets.insert((n("example."), RecordType::DS), ok(signed(&root, vec![rec("example.", RData::DNSSEC(DNSSECRData::DS(example.ds())))])));
    mock.sets.insert((n("example."), RecordType::DNSKEY), ok(signed(&example, vec![example.dnskey_record()])));
    // plain.example. is an unsigned delegation: NODATA for DS with a signed NSEC proving NS without DS
    let chain = nsec_chain("example.", &[("example.", &[RecordType::SOA, RecordType::NS, RecordType::DNSKEY]), ("plain.example.", &[RecordType::NS]), ("www.example.", &[RecordType::A])]);
    let soa = rec("example.", RData::SOA(SOA::new(n("ns.example."), n("h.example."), 1, 3600, 600, 86400, 300)));
    let mut auth = signed(&example, vec![soa.clone()]);
    let plain_nsec = rec("plain.example.", RData::DNSSEC(DNSSECRData::NSEC(chain.iter().find(|(o, _)| *o == n("plain.example.")).unwrap().1.clone())));
    auth.extend(signed(&example, vec![plain_nsec]));
    mock.sets.insert((n("plain.example."), RecordType::DS), FetchedSet { rcode: ResponseCode::NoError, answers: vec![], authorities: auth });
    // www.example. is not a zone cut: NODATA for DS with an NSEC whose bitmap has no NS
    let mut www_auth = signed(&example, vec![soa.clone()]);
    let www_nsec = rec("www.example.", RData::DNSSEC(DNSSECRData::NSEC(chain.iter().find(|(o, _)| *o == n("www.example.")).unwrap().1.clone())));
    www_auth.extend(signed(&example, vec![www_nsec]));
    mock.sets.insert((n("www.example."), RecordType::DS), FetchedSet { rcode: ResponseCode::NoError, answers: vec![], authorities: www_auth });
    // nodenial.example.: parent returns NODATA for DS without any NSEC
    mock.sets.insert((n("nodenial.example."), RecordType::DS), FetchedSet { rcode: ResponseCode::NoError, answers: vec![], authorities: signed(&example, vec![soa]) });
    let trust = TrustPoints { zones: vec![(Name::root(), TrustPoint::Ds(vec![root.ds()]))] };
    World { root, example, mock, trust }
}

fn input<'a>(qname: &'a Name, rcode: ResponseCode, answers: &'a [Record], authorities: &'a [Record]) -> ValidationInput<'a> {
    ValidationInput { qname, qtype: RecordType::A, rcode, answers, authorities }
}

#[tokio::test(flavor = "current_thread")]
async fn secure_answer_validates() {
    let w = world();
    let v = Validator::new(Arc::new(RecursorMetrics::default()));
    let q = n("www.example.");
    let answers = signed(&w.example, vec![rec("www.example.", RData::A(A(Ipv4Addr::new(192, 0, 2, 10))))]);
    let r = v.validate(&input(&q, ResponseCode::NoError, &answers, &[]), &w.trust, &[], &w.mock, NOW).await;
    assert_eq!(r.security, Security::Secure);
    let _ = &w.root;
}

#[tokio::test(flavor = "current_thread")]
async fn broken_signature_is_bogus_ede_6() {
    let w = world();
    let v = Validator::new(Arc::new(RecursorMetrics::default()));
    let q = n("www.example.");
    let mut answers = signed(&w.example, vec![rec("www.example.", RData::A(A(Ipv4Addr::new(192, 0, 2, 10))))]);
    answers[0].data = RData::A(A(Ipv4Addr::new(6, 6, 6, 6)));
    let r = v.validate(&input(&q, ResponseCode::NoError, &answers, &[]), &w.trust, &[], &w.mock, NOW).await;
    assert_eq!(r.security, Security::Bogus(Ede { code: 6, text: "bad signature for www.example. A".into() }));
}

#[tokio::test(flavor = "current_thread")]
async fn expired_signature_is_bogus_ede_7() {
    let w = world();
    let v = Validator::new(Arc::new(RecursorMetrics::default()));
    let q = n("www.example.");
    let set = vec![rec("www.example.", RData::A(A(Ipv4Addr::new(192, 0, 2, 10))))];
    let mut answers = set.clone();
    answers.push(w.example.sign(&set, INC - 90_000, INC - 86_400));
    let r = v.validate(&input(&q, ResponseCode::NoError, &answers, &[]), &w.trust, &[], &w.mock, NOW).await;
    assert!(matches!(r.security, Security::Bogus(Ede { code: 7, .. })), "{:?}", r.security);
}

#[tokio::test(flavor = "current_thread")]
async fn missing_rrsig_in_signed_zone_is_bogus_ede_10() {
    let w = world();
    let v = Validator::new(Arc::new(RecursorMetrics::default()));
    let q = n("www.example.");
    let answers = vec![rec("www.example.", RData::A(A(Ipv4Addr::new(192, 0, 2, 10))))];
    let r = v.validate(&input(&q, ResponseCode::NoError, &answers, &[]), &w.trust, &[], &w.mock, NOW).await;
    assert!(matches!(r.security, Security::Bogus(Ede { code: 10, .. })), "{:?}", r.security);
}

#[tokio::test(flavor = "current_thread")]
async fn proven_unsigned_delegation_is_insecure_and_unproven_is_bogus_ede_12() {
    let w = world();
    let v = Validator::new(Arc::new(RecursorMetrics::default()));
    let q = n("www.plain.example.");
    let answers = vec![rec("www.plain.example.", RData::A(A(Ipv4Addr::new(192, 0, 2, 20))))];
    let r = v.validate(&input(&q, ResponseCode::NoError, &answers, &[]), &w.trust, &[], &w.mock, NOW).await;
    assert_eq!(r.security, Security::Insecure(None));
    let q = n("www.nodenial.example.");
    let answers = vec![rec("www.nodenial.example.", RData::A(A(Ipv4Addr::new(192, 0, 2, 21))))];
    let r = v.validate(&input(&q, ResponseCode::NoError, &answers, &[]), &w.trust, &[], &w.mock, NOW).await;
    assert!(matches!(r.security, Security::Bogus(Ede { code: 12, .. })), "{:?}", r.security);
}

#[tokio::test(flavor = "current_thread")]
async fn unsupported_ds_algorithm_is_insecure_ede_1() {
    let mut w = world();
    let bad = DS::new(4242, Algorithm::Unknown(200), DigestType::SHA256, vec![0; 32]);
    w.mock.sets.insert((n("example."), RecordType::DS), FetchedSet { rcode: ResponseCode::NoError, answers: signed(&w.root, vec![rec("example.", RData::DNSSEC(DNSSECRData::DS(bad)))]), authorities: vec![] });
    let v = Validator::new(Arc::new(RecursorMetrics::default()));
    let q = n("www.example.");
    let answers = signed(&w.example, vec![rec("www.example.", RData::A(A(Ipv4Addr::new(192, 0, 2, 10))))]);
    let r = v.validate(&input(&q, ResponseCode::NoError, &answers, &[]), &w.trust, &[], &w.mock, NOW).await;
    assert!(matches!(r.security, Security::Insecure(Some(Ede { code: 1, .. }))), "{:?}", r.security);
}

#[tokio::test(flavor = "current_thread")]
async fn negative_trust_anchor_makes_bogus_zone_insecure_without_fetching() {
    let w = world();
    let v = Validator::new(Arc::new(RecursorMetrics::default()));
    let q = n("www.example.");
    let answers = vec![rec("www.example.", RData::A(A(Ipv4Addr::new(6, 6, 6, 6))))];
    let ntas = vec![(n("example."), (NOW + 60) as i64)];
    let r = v.validate(&input(&q, ResponseCode::NoError, &answers, &[]), &w.trust, &ntas, &w.mock, NOW).await;
    assert_eq!(r.security, Security::Insecure(None));
    assert_eq!(*w.mock.calls.borrow(), 0);
    assert!(!under_nta(&q, &ntas, NOW + 61), "expired NTA no longer applies");
}

#[tokio::test(flavor = "current_thread")]
async fn signed_nxdomain_validates_and_feeds_aggressive_cache() {
    let w = world();
    let v = Validator::new(Arc::new(RecursorMetrics::default()));
    let chain = nsec_chain("example.", &[("example.", &[RecordType::SOA, RecordType::NS, RecordType::DNSKEY]), ("plain.example.", &[RecordType::NS]), ("www.example.", &[RecordType::A])]);
    let soa = rec("example.", RData::SOA(SOA::new(n("ns.example."), n("h.example."), 1, 3600, 600, 86400, 300)));
    let mut auth = signed(&w.example, vec![soa]);
    for (owner, nsec) in &chain { auth.extend(signed(&w.example, vec![Record::from_rdata(owner.clone(), 300, RData::DNSSEC(DNSSECRData::NSEC(nsec.clone())))])); }
    let q = n("abc.example.");
    let r = v.validate(&input(&q, ResponseCode::NXDomain, &[], &auth), &w.trust, &[], &w.mock, NOW).await;
    assert_eq!(r.security, Security::Secure);
    let (rcode, records) = v.nsec.synthesize(&n("abd.example."), RecordType::A, NOW).expect("synthesised");
    assert_eq!(rcode, ResponseCode::NXDomain);
    assert!(records.iter().any(|r| r.record_type() == RecordType::SOA));
    assert!(v.nsec.synthesize(&n("www.example."), RecordType::A, NOW).is_none(), "existing name is never denied");
    let stripped: Vec<Record> = auth.iter().filter(|r| r.record_type() != RecordType::NSEC).cloned().collect();
    let r = v.validate(&input(&n("abe.example."), ResponseCode::NXDomain, &[], &stripped), &w.trust, &[], &w.mock, NOW).await;
    assert!(matches!(r.security, Security::Bogus(Ede { code: 12, .. })), "{:?}", r.security);
    let _ = NS(n("x."));
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::dnssec::validator_tests` — expect FAIL with "cannot find type `FetchedSet` in this scope".
- [ ] Implement `Validator::zone_state(zone)` (results cached in `zones` until `now + min(DNSKEY TTL, 3600)`; `Bogus` for 60 s):
  1. `(tp_name, tp) = trust.closest(zone)`; none → `Insecure(None)`.
  2. Start at the trust point: fetch `DNSKEY tp_name`; keys authorised by the trust point (`TrustPoint::Keys` equal keys, or `match_ds` against `TrustPoint::Ds`); `NoMatch`/empty → `Bogus(9 "no DNSKEY matches trust anchor for <zone>")`; `NoSupported` → `Insecure(Some(1))`; the DNSKEY RRset must verify with one of the authorised keys (`verify_rrset`) → otherwise `Bogus` per the `VerifyError` mapping; state = `Secure(all zone keys in the RRset)`.
  3. Walk labels from `tp_name` towards `zone`, one label at a time (`child = zone.trim_to(cur.num_labels() + 1)`): fetch `DS child`.
     - DS RRset present: verify with the current keys (failure → `Bogus`); `match_ds(child, DNSKEY child)` — `Matched(keys)` and the child DNSKEY RRset verifies with one of them → `Secure(child keys)`; `NoSupported` → `Insecure(Some(Ede{code: 2 or 1}))` (2 when every DS digest is unsupported, 1 otherwise); `NoMatch` → `Bogus(9)`.
     - NODATA / NXDOMAIN: verify the authority NSEC/NSEC3 records with the current keys; `nsec_proves_nodata(child, DS)` / `nsec3_proves_nodata` → `Proven` or `ProvenOptOut`: if the matching NSEC bitmap has `NS` → `Insecure(None)` (unsigned delegation) and stop; if no `NS` bit (empty non-terminal or name inside the same zone) → keep the current keys and continue; `InsecureIterations(n)` → `Insecure(Some(27))`; `BogusIterations(n)` → `Bogus(27)`; `NotProven` → `Bogus(12 "no proof of missing DS for <child>")`. NXDOMAIN with a valid proof → stop, keep current keys (names below do not exist).
     - `FetchError` → return `Bogus(Ede{23, "network error fetching DS <child>"})` wrapped by the caller as `Indeterminate`.
- [ ] Implement `Validator::validate`:
  1. `under_nta(qname, ntas, now)` (NTA domain is an ancestor-or-self and `expires_unix > now`) → `Insecure(None)` without fetching.
  2. Group `answers` and `authorities` into RRsets by `(owner lowercase, type)`, RRSIGs attached by `type_covered`.
  3. For every answer RRset (the CNAME/DNAME chain and the final set): signer = RRSIG `signer_name` if any RRSIG exists, else the zone of the owner is computed by `zone_state(owner)` walking to the deepest secure zone reached. `zone_state(signer)` must be `Secure(keys)` → `verify_rrset`; RRSIG missing while the zone is `Secure` → `Bogus(10 "RRSIGs missing for <owner> <type>")`; `Insecure` → that RRset is insecure; wildcard-expanded signatures require `nsec_proves_no_closer_match` / NSEC3 next-closer coverage from the authority section, else `Bogus(12)`. Synthesised CNAMEs from a validated DNAME are accepted without RRSIG.
  4. Negative answers (`NXDomain`, or `NoError` with no RRset for `qname`/`qtype` at the end of the chain): the SOA and every NSEC/NSEC3 RRset in `authorities` are verified as in step 3; then `nsec_proves_nxdomain` / `nsec_proves_nodata` (or the NSEC3 variants when NSEC3 records are present) for the last name in the chain → `Proven` → secure; `ProvenOptOut` → `Insecure(None)`; iteration outcomes as in `zone_state`; `NotProven` → `Bogus(12 "NSEC missing: <reason>")`. When the proof is `Proven` and the records verified, call `self.nsec.insert_secure(zone, soa, denial_records, now)`.
  5. Combine: any `Bogus` → `Bogus` (first error), any `Indeterminate` → `Indeterminate`, any `Insecure` → `Insecure` (first EDE), else `Secure`; `ttl_cap = min capped_ttl` over verified RRsets. Update `metrics.dnssec_secure/insecure/bogus/indeterminate` and `dnssec_bogus_by_ede[code]` (codes ≥ 32 counted in slot 0).
- [ ] Implement `AggressiveNsecCache` (`parking_lot::Mutex<HashMap<Name, ZoneDenials>>`, max 10 000 zones, oldest zone evicted): `insert_secure` stores each NSEC (owner, next, bitmap, record + RRSIG, `expires = now + min(TTL, SOA minimum)`) and each NSEC3 by owner hash, plus the SOA RRset with RRSIG. `synthesize(qname, qtype)`: find the deepest cached zone containing `qname`; with cached NSECs run `nsec_proves_nxdomain` (→ `NXDomain`) then `nsec_proves_nodata` (→ `NoError`) over the unexpired records; for NSEC3 zones the same with NSEC3 functions, never using opt-out records; return the SOA + the NSEC/NSEC3 records used + RRSIGs; `None` when no proof or no SOA; increment `dnssec_aggressive_synthesized`.
- [ ] Wire into `dispatch::resolve_miss`:
  - `validation_enabled = rt.dnssec.validation && match route { Route::Recursive => true, Route::ForwardZone(z) => z.validate, Route::Forward => false }` (Architecture change 2: answers from the global upstreams keep M1's unvalidated path).
  - Before resolving, when `rt.params.aggressive_nsec && validation_enabled && !q.checking_disabled`, `state.validator.nsec.synthesize(..)` → `Some` returns that answer (`security = Secure`, cacheable).
  - After a successful resolution on a validating route: when `q.checking_disabled` → no validation, `SecurityTag::None`, AD=0 (RFC 4035 §3.2.2 CD honoured; the M1 cache key already separates CD). Otherwise run `validator.validate` with `TrustPoints` from `state.anchors.trust_points()` (Task 8; until then `TrustPoints::from_config(&rt.dnssec.anchors)`), `rt.dnssec.ntas`, and `RoutedFetcher`. `Secure` → `build_response(.., secure: true)`; `Insecure(ede)` → AD=0 and EDE attached; `Bogus(ede)`/`Indeterminate(ede)` → SERVFAIL, `failed: false` (the SERVFAIL is the answer, stale data must not be served), `cacheable: false`, EDE attached.
  - `RoutedFetcher::fetch` builds `FetchedSet` from the route of the fetched name: `Route::Recursive` → `recursor.fetch(name, rtype, params, budget)`; `Route::ForwardZone` → the forward-zone exchange with DO=1, CD=1; `Route::Forward` (a validating forward zone below a name the global upstreams answer, e.g. the root DS/DNSKEY in forward mode) → `upstream.forward` of a query built with `hickory_proto::op::Message` (random ID from `rand::rng()`, RD=1, EDNS 1232, DO=1, CD=1); `WorkerForward` parses the question from those bytes, so reply matching stays M1's. An upstream that strips RRSIGs produces `Bogus(10)`.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::dnssec::validator_tests` — expect PASS (8 tests).
- [ ] Commit: `git add engine/src/recursor && git commit -m "feat(dnssec): chain-of-trust validation, EDE, NTAs and aggressive NSEC"`.

## Task 8: Trust anchor store with RFC 5011 automated rollover

Files:

- `engine/src/recursor/dnssec/anchors.rs` — persisted anchor state in `state_dir/trust-anchors.json`, merge with mgmt-delivered anchors, RFC 5011 state machine, refresh scheduling, status for `Stats`.
- `engine/src/recursor/dnssec/anchors_tests.rs` — unit tests.
- `engine/src/recursor/mod.rs` — `RecursorState::sync` and `spawn_background` (thread `nexora-recursor`).
- `engine/src/control.rs` — `apply_snapshot` calls `shared.recursor.sync` after an applied snapshot; the 10 s `Stats` include `DnssecStats` and `RecursionStats`.
- `engine/src/main.rs` — calls `sync` after the persisted and standalone snapshots apply; calls `recursor::spawn_background(shared.clone())`.
- `engine/src/telemetry/metrics.rs` — `nexora_dnssec_trust_anchor_keys{zone,state}`, `nexora_dnssec_trust_anchor_last_refresh_success_timestamp_seconds{zone}`, `nexora_dnssec_negative_trust_anchors`; `Stats.recursion`, `Stats.dnssec`.

Interfaces:

```rust
#[derive(Serialize, Deserialize, Clone, Copy, Debug, PartialEq, Eq)]
pub enum KeyState { Configured, AddPend, Valid, Missing, Revoked }
#[derive(Serialize, Deserialize, Clone, Debug)]
pub struct AnchorKey { pub key_tag: u16, pub algorithm: u8, pub ds: Option<String>, pub dnskey_b64: Option<String>, pub state: KeyState, pub hold_down_until: i64, pub from_config: bool }
#[derive(Serialize, Deserialize, Clone, Debug, Default)]
pub struct ZoneAnchors { pub keys: Vec<AnchorKey>, pub last_success: i64, pub next_refresh: i64, pub last_error: String }
#[derive(Serialize, Deserialize, Clone, Debug, Default)]
pub struct AnchorFile { pub zones: BTreeMap<String, ZoneAnchors> }
pub const ADD_HOLD_DOWN: i64 = 30 * 86_400;
pub const REMOVE_HOLD_DOWN: i64 = 30 * 86_400;
pub struct Observation<'a> { pub dnskeys: &'a [DNSKEY], pub validated_by_trusted: bool, pub revoked_self_signed: &'a [u16], pub orig_ttl: u32, pub sig_expiration: i64 }
pub fn apply_rfc5011(z: &mut ZoneAnchors, zone: &Name, obs: &Observation<'_>, now: i64);
pub fn refresh_interval(orig_ttl: u32, sig_expiration: i64, now: i64) -> i64;   // RFC 5011 §2.3 active refresh
pub fn retry_interval(orig_ttl: u32, sig_expiration: i64, now: i64) -> i64;
pub struct TrustAnchorStore { path: PathBuf, file: parking_lot::Mutex<AnchorFile>, points: arc_swap::ArcSwap<TrustPoints>, rfc5011: AtomicBool }
impl TrustAnchorStore {
    pub fn open(path: Option<PathBuf>) -> Arc<Self>;                 // None: memory only; missing/corrupt file -> empty state, error kept in last_error
    pub fn merge_config(&self, anchors: &[proto::TrustAnchor], rfc5011: bool, now: i64) -> std::io::Result<()>;
    pub fn trust_points(&self) -> Arc<TrustPoints>;                  // Configured(DS) + Valid(DNSKEY) + Missing keys; never AddPend/Revoked
    pub fn record_observation(&self, zone: &Name, obs: &Observation<'_>, now: i64) -> std::io::Result<()>;
    pub fn record_failure(&self, zone: &Name, error: &str, now: i64);
    pub fn status(&self) -> Vec<proto::TrustAnchorStatus>;
}
pub async fn refresh_loop(shared: Arc<crate::server::Shared>); // runs on the nexora-recursor thread
// mod.rs
impl RecursorState { pub fn sync(&self, rt: &crate::runtime::Runtime); }
pub fn spawn_background(shared: Arc<crate::server::Shared>) -> std::thread::JoinHandle<()>;
```

- [ ] Create `engine/src/recursor/dnssec/anchors_tests.rs`:

```rust
use super::anchors::*;
use super::testsign::TestKey;
use crate::proto;
use hickory_proto::dnssec::rdata::DNSKEY;
use hickory_proto::rr::Name;

const DAY: i64 = 86_400;
const T0: i64 = 1_800_000_000;

fn ds_text(k: &TestKey) -> String {
    let ds = k.ds();
    format!("{} {} {} {}", ds.key_tag(), u8::from(ds.algorithm()), u8::from(ds.digest_type()), data_encoding::HEXUPPER.encode(ds.digest()))
}

fn obs<'a>(keys: &'a [DNSKEY], revoked: &'a [u16]) -> Observation<'a> {
    Observation { dnskeys: keys, validated_by_trusted: true, revoked_self_signed: revoked, orig_ttl: 172_800, sig_expiration: T0 + 10 * DAY }
}

fn store_with(k: &TestKey) -> (tempfile::TempDir, std::sync::Arc<TrustAnchorStore>) {
    let dir = tempfile::tempdir().unwrap();
    let s = TrustAnchorStore::open(Some(dir.path().join("trust-anchors.json")));
    s.merge_config(&[proto::TrustAnchor { zone: ".".into(), ds: ds_text(k) }], true, T0).unwrap();
    (dir, s)
}

fn state_of(s: &TrustAnchorStore, tag: u16) -> Option<KeyState> {
    s.status().into_iter().find(|a| a.key_tag == tag as u32).map(|a| match a.state {
        x if x == proto::TrustAnchorState::Configured as i32 => KeyState::Configured,
        x if x == proto::TrustAnchorState::AddPend as i32 => KeyState::AddPend,
        x if x == proto::TrustAnchorState::Valid as i32 => KeyState::Valid,
        x if x == proto::TrustAnchorState::Missing as i32 => KeyState::Missing,
        _ => KeyState::Revoked,
    })
}

#[test]
fn configured_ds_becomes_valid_when_observed() {
    let old = TestKey::generate(".", true);
    let (_d, s) = store_with(&old);
    let tag = old.dnskey.calculate_key_tag().unwrap();
    assert_eq!(state_of(&s, tag), Some(KeyState::Configured));
    s.record_observation(&Name::root(), &obs(&[old.dnskey.clone()], &[]), T0).unwrap();
    assert_eq!(state_of(&s, tag), Some(KeyState::Valid));
}

#[test]
fn new_key_waits_add_hold_down_then_becomes_trusted() {
    let old = TestKey::generate(".", true);
    let new = TestKey::generate(".", true);
    let (_d, s) = store_with(&old);
    let keys = [old.dnskey.clone(), new.dnskey.clone()];
    let new_tag = new.dnskey.calculate_key_tag().unwrap();
    s.record_observation(&Name::root(), &obs(&keys, &[]), T0).unwrap();
    assert_eq!(state_of(&s, new_tag), Some(KeyState::AddPend));
    assert_eq!(s.trust_points().zones[0].1.key_count(), 1, "AddPend is not trusted");
    s.record_observation(&Name::root(), &obs(&keys, &[]), T0 + 29 * DAY).unwrap();
    assert_eq!(state_of(&s, new_tag), Some(KeyState::AddPend));
    s.record_observation(&Name::root(), &obs(&keys, &[]), T0 + 30 * DAY).unwrap();
    assert_eq!(state_of(&s, new_tag), Some(KeyState::Valid));
    assert_eq!(s.trust_points().zones[0].1.key_count(), 2);
}

#[test]
fn pending_key_that_disappears_is_forgotten_and_unvalidated_observations_change_nothing() {
    let old = TestKey::generate(".", true);
    let new = TestKey::generate(".", true);
    let (_d, s) = store_with(&old);
    let new_tag = new.dnskey.calculate_key_tag().unwrap();
    let mut o = obs(&[], &[]);
    let both = [old.dnskey.clone(), new.dnskey.clone()];
    o.dnskeys = &both;
    o.validated_by_trusted = false;
    s.record_observation(&Name::root(), &o, T0).unwrap();
    assert_eq!(state_of(&s, new_tag), None, "an RRset not signed by a trusted key is ignored");
    s.record_observation(&Name::root(), &obs(&both, &[]), T0).unwrap();
    s.record_observation(&Name::root(), &obs(&[old.dnskey.clone()], &[]), T0 + DAY).unwrap();
    assert_eq!(state_of(&s, new_tag), None);
}

#[test]
fn revoked_self_signed_key_is_revoked_and_removed_after_hold_down() {
    let old = TestKey::generate(".", true);
    let (_d, s) = store_with(&old);
    let tag = old.dnskey.calculate_key_tag().unwrap();
    s.record_observation(&Name::root(), &obs(&[old.dnskey.clone()], &[]), T0).unwrap();
    s.record_observation(&Name::root(), &obs(&[old.dnskey.clone()], &[tag]), T0 + DAY).unwrap();
    assert_eq!(state_of(&s, tag), Some(KeyState::Revoked));
    assert_eq!(s.trust_points().zones.len(), 0, "a zone whose only key is revoked has no trust point");
    s.record_observation(&Name::root(), &obs(&[], &[]), T0 + DAY + 30 * DAY).unwrap();
    assert_eq!(state_of(&s, tag), None);
}

#[test]
fn state_survives_restart_and_mgmt_removal_drops_zone() {
    let old = TestKey::generate(".", true);
    let (d, s) = store_with(&old);
    let tag = old.dnskey.calculate_key_tag().unwrap();
    s.record_observation(&Name::root(), &obs(&[old.dnskey.clone()], &[]), T0).unwrap();
    drop(s);
    let s = TrustAnchorStore::open(Some(d.path().join("trust-anchors.json")));
    assert_eq!(state_of(&s, tag), Some(KeyState::Valid));
    s.merge_config(&[], true, T0 + 1).unwrap();
    assert!(s.status().is_empty());
}

#[test]
fn refresh_and_retry_intervals_follow_rfc5011_section_2_3() {
    assert_eq!(refresh_interval(172_800, T0 + 10 * DAY, T0), DAY);          // min(15d, ttl/2=1d, exp/2=5d)
    assert_eq!(refresh_interval(600, T0 + 10 * DAY, T0), 3600);             // floor 1h
    assert_eq!(retry_interval(172_800, T0 + 10 * DAY, T0), 17_280);         // min(1d, ttl/10, exp/10)
    assert_eq!(retry_interval(600, T0 + 10 * DAY, T0), 3600);
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::dnssec::anchors_tests` — expect FAIL with "cannot find struct, variant or union type `Observation` in this scope".
- [ ] Implement `apply_rfc5011` (no-op except `last_error = "DNSKEY RRset not validated by a trusted key"` when `!obs.validated_by_trusted`):

```rust
pub fn apply_rfc5011(z: &mut ZoneAnchors, zone: &Name, obs: &Observation<'_>, now: i64) {
    if !obs.validated_by_trusted { z.last_error = "DNSKEY RRset not validated by a trusted key".into(); return; }
    let seen: Vec<(u16, u8, &DNSKEY)> = obs.dnskeys.iter().filter(|k| k.secure_entry_point() && k.zone_key())
        .filter_map(|k| k.calculate_key_tag().ok().map(|t| (t, u8::from(k.algorithm()), k))).collect();
    for key in z.keys.iter_mut() {
        let hit = seen.iter().find(|(t, a, k)| *t == key.key_tag && *a == key.algorithm
            && (key.dnskey_b64.is_none() || key.dnskey_b64.as_deref() == Some(&b64(k))));
        match (key.state, hit) {
            (_, Some(_)) if obs.revoked_self_signed.contains(&key.key_tag) && key.state != KeyState::Revoked => {
                key.state = KeyState::Revoked; key.hold_down_until = now + REMOVE_HOLD_DOWN;
            }
            (KeyState::Configured, Some((_, _, k))) => {
                if key.ds.as_deref().map(|d| ds_matches(d, zone, k)).unwrap_or(true) { key.state = KeyState::Valid; key.dnskey_b64 = Some(b64(k)); }
            }
            (KeyState::AddPend, Some(_)) if now >= key.hold_down_until => key.state = KeyState::Valid,
            (KeyState::Missing, Some(_)) => key.state = KeyState::Valid,
            (KeyState::Valid, None) => key.state = KeyState::Missing,
            _ => {}
        }
    }
    // AddPend keys that vanished return to Start (forgotten); Revoked keys past the remove hold-down are removed
    z.keys.retain(|k| !(k.state == KeyState::AddPend && !seen.iter().any(|(t, a, _)| *t == k.key_tag && *a == k.algorithm))
        && !(k.state == KeyState::Revoked && now >= k.hold_down_until));
    for (t, a, k) in &seen {
        let known = z.keys.iter().any(|x| x.key_tag == *t && x.algorithm == *a);
        if !known && !k.revoke() {
            z.keys.push(AnchorKey { key_tag: *t, algorithm: *a, ds: None, dnskey_b64: Some(b64(k)), state: KeyState::AddPend, hold_down_until: now + ADD_HOLD_DOWN, from_config: false });
        }
    }
    z.last_success = now;
    z.last_error.clear();
    z.next_refresh = now + refresh_interval(obs.orig_ttl, obs.sig_expiration, now);
}
```

`b64(k)` is `data_encoding::BASE64.encode(&k.to_bytes().unwrap())`; `ds_matches` parses the DS text with `snapshot_m3::parse_ds` and compares key tag/algorithm and `k.to_digest(zone, digest_type)` bytes. The caller (`refresh_loop`) verifies that a REVOKE-flagged key signs the DNSKEY RRset (RFC 5011 §2.1), then passes that key in `dnskeys` with the REVOKE bit cleared (`DNSKEY::with_flags(flags & !0x0080, public_key)`) and lists the key tag of that cleared form in `revoked_self_signed`, so it matches the stored anchor.

- [ ] Implement intervals: `refresh_interval = max(3600, min(15*DAY, orig_ttl/2, (sig_expiration-now)/2))`; `retry_interval = max(3600, min(DAY, orig_ttl/10, (sig_expiration-now)/10))`.
- [ ] Implement `TrustAnchorStore`: `merge_config` adds a `Configured` key (`from_config: true`, key tag/algorithm from the DS) for every configured DS not already present by key tag+algorithm; removes zones not present in `anchors`; removes `from_config` keys whose DS is no longer configured unless their state is `Valid` (an authenticated rollover key stays); persists with write-temp + `fsync` + rename (same pattern as `snapshot.binpb`) and republishes `points`. `trust_points()` builds `TrustPoint::Ds` from `Configured` keys and `TrustPoint::Keys` from `Valid`/`Missing` keys (both kinds are merged per zone; add `TrustPoint::key_count(&self) -> usize` counting DS entries plus keys); zones with nothing trusted are omitted. `status()` maps to `proto::TrustAnchorStatus`.
- [ ] Implement `refresh_loop(shared)`: every 60 s, `let rt = shared.runtime.load_full();` and for each zone with `rfc5011` enabled and `now >= next_refresh`: fetch `DNSKEY <zone>` through a `RoutedFetcher { rt: &rt.resolution, state: &shared.recursor, upstream: &WorkerForward { set: &rt.upstreams, worker: &upstreams, upstream_index: Cell::new(u8::MAX) }, budget: &WorkBudget::new(100, 32) }`, where `upstreams = WorkerUpstreams::new(Arc::new(CachePadded::new(AtomicU64::new(0))))` is created once for the thread; verify the RRset with current trust points (`validated_by_trusted`), find `REVOKE`-flagged SEP keys whose own signature over the RRset verifies (`revoked_self_signed`), take `orig_ttl`/`sig_expiration` from the verifying RRSIG, and call `record_observation`. On any failure call `record_failure` (sets `last_error`, `next_refresh = now + retry_interval`, increments `metrics.trust_anchor_refresh_failures`). `dispatch::resolve_miss` now uses `state.anchors.trust_points()`.
- [ ] Implement `recursor::spawn_background(shared)`: a thread named `nexora-recursor` with a `tokio::runtime::Builder::new_current_thread().enable_all()` runtime and a `LocalSet` running `refresh_loop(shared)` (resolution futures are `!Send`, like the workers').
- [ ] Implement `RecursorState::sync(&self, rt)`: `self.anchors.merge_config(&rt.resolution.dnssec.anchors, rt.resolution.dnssec.rfc5011, clock::unix_now())`; a persist failure is kept in the zone's `last_error` (reported through `DnssecStats.trust_anchors[].last_error`) and never rejects the snapshot. Call it in `control.rs` `apply_snapshot` inside the `spawn_blocking` closure when `snapshot::apply` returned `ApplyOutcome::Applied` (`shared.recursor.sync(&shared.runtime.load_full())`), and in `main.rs` after `report` succeeds for the persisted snapshot and in `apply_standalone`; `main.rs` calls `recursor::spawn_background(shared.clone())` after the workers start.
- [ ] `Metrics::stats(rt, recursor)` gains `recursion: Some(RecursionStats { .. })` (from `RecursorMetrics` and `recursor.recursor.infra.len()`) and `dnssec: Some(DnssecStats { .. })` (counters, `recursor.anchors.status()`, `active_negative_trust_anchors` = NTAs in `rt.resolution.dnssec.ntas` expiring after now); `render` exposes the trust-anchor metrics listed under Files.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::dnssec::anchors_tests` — expect PASS (6 tests).
- [ ] Commit: `git add engine && git commit -m "feat(dnssec): RFC 5011 trust anchor store persisted in state_dir"`.

## Task 9: RPZ policy engine — parsing, triggers, precedence, actions and query-path hooks

Files:

- `engine/src/recursor/rpz/mod.rs` — declarations.
- `engine/src/recursor/rpz/parse.rs` — RPZ zone text/records → rules.
- `engine/src/recursor/rpz/index.rs` — `RpzZoneIndex`, `RpzSet`, trigger lookup and precedence.
- `engine/src/recursor/rpz/apply.rs` — action → response synthesis.
- `engine/src/recursor/rpz/rpz_tests.rs` — unit tests.
- `engine/src/recursor/mod.rs` — `RecursorState.rpz`, `rpz_query_triggers`, `publish_rpz`.
- `engine/src/recursor/dispatch.rs` — `RpzPending::{Deferred, Apply}`; query-phase CNAME chasing and the response-phase check after validation.
- `engine/src/server/mod.rs` — query-phase check before the cache lookup; `MissJob.rpz`; RPZ-affected misses bypass in-flight coalescing and the cache.

Interfaces:

```rust
// parse.rs
#[derive(Clone, Debug, PartialEq)] pub enum CnameTarget { Name(Name), WildcardSuffix(Name) }
#[derive(Clone, Debug, PartialEq)] pub struct LocalData { pub records: Vec<(RecordType, u32, RData)>, pub cname: Option<(u32, CnameTarget)> }
#[derive(Clone, Debug, PartialEq)] pub enum RpzAction { Nxdomain, Nodata, Passthru, Drop, TcpOnly, LocalData(Arc<LocalData>) }
#[derive(Clone, Debug, PartialEq)] pub enum Trigger { Qname { name: Name, wildcard: bool }, ClientIp(ipnet::IpNet), ResponseIp(ipnet::IpNet), Nsdname { name: Name, wildcard: bool }, Nsip(ipnet::IpNet) }
#[derive(Clone, Debug)] pub struct ParsedRpz { pub origin: Name, pub serial: u32, pub soa: Record, pub rules: Vec<(Trigger, RpzAction)>, pub skipped: u64, pub records: u64 }
pub fn parse_rpz_text(origin: &Name, text: &str) -> Result<ParsedRpz, String>;
pub fn parse_rpz_records(origin: &Name, records: &[Record]) -> Result<ParsedRpz, String>;
pub fn decode_ip_trigger(labels: &[String]) -> Option<ipnet::IpNet>; // labels in zone order, e.g. ["32","66","2","0","192"]
// index.rs
pub struct RpzZoneIndex { pub id: String, pub origin: Name, pub soa: Record, pub serial: u32, pub records: u64, pub skipped: u64, pub policy_override: i32, pub hits: AtomicU64, /* lookup tables */ }
impl RpzZoneIndex { pub fn build(id: &str, parsed: &ParsedRpz, policy_override: i32) -> Self; pub fn has_query_triggers(&self) -> bool; pub fn has_response_triggers(&self) -> bool; }
pub struct RpzSet { pub zones: Vec<Arc<RpzZoneIndex>>, pub has_query_triggers: bool, pub has_response_triggers: bool }
#[derive(Debug)] pub enum QueryPhase<'a> { NoMatch, Hit { zone: usize, action: &'a RpzAction }, Deferred { zone: usize, action: &'a RpzAction } }
impl RpzSet {
    pub fn new(zones: Vec<Arc<RpzZoneIndex>>) -> Self;
    pub fn check_query(&self, qname_wire_lower: &[u8], client: IpAddr) -> QueryPhase<'_>;
    pub fn check_response(&self, upto_zone: usize, chain: &[Name], answers: &[Record], ns_names: &[Name], ns_addrs: &[IpAddr]) -> Option<(usize, &RpzAction)>;
    pub fn effective_action<'a>(&'a self, zone: usize, action: &'a RpzAction) -> Option<RpzAction>; // applies policy_override; None = DISABLED
}
// apply.rs
#[derive(Debug)] pub enum PolicyOutcome { Respond { wire: Vec<u8>, ede: Ede }, Drop, Truncate { wire: Vec<u8> }, Passthru, ChaseCname { cname: Record, target: Name } }
pub fn apply_action(qname: &Name, qtype: RecordType, over_tcp: bool, zone: &RpzZoneIndex, action: &RpzAction) -> PolicyOutcome;
// dispatch.rs (replaces Task 5's single-variant enum)
#[derive(Clone, Debug, Default)] pub enum RpzPending { #[default] None, Deferred { zone: usize, action: RpzAction }, Apply { zone: usize, action: RpzAction } }
// mod.rs
// RecursorState gains: pub rpz: arc_swap::ArcSwap<rpz::index::RpzSet>, pub rpz_query_triggers: AtomicBool
impl RecursorState { pub fn publish_rpz(&self, set: rpz::index::RpzSet); } // stores the set, then rpz_query_triggers = set.has_query_triggers
// server/mod.rs
// MissJob gains: pub rpz: RpzPending
```

Precedence (draft-vixie-dnsop-dns-rpz): zones in snapshot order, the first zone with a matching trigger wins; within one zone CLIENT-IP > QNAME > response IP > NSDNAME > NSIP; exact QNAME/NSDNAME beats wildcard, deeper wildcard beats shallower; longest IP prefix wins. QNAME triggers are also checked against every CNAME target in the resolved chain. RPZ runs after Nexora's own blocklist/allowlist, per-client policy and rewrites (M1/M2), so a Nexora block decision is final.

- [ ] Create `engine/src/recursor/rpz/rpz_tests.rs`:

```rust
use super::apply::*;
use super::index::*;
use super::parse::*;
use crate::proto::RpzPolicyOverride;
use hickory_proto::op::{Message, ResponseCode};
use hickory_proto::rr::{rdata::{A, CNAME}, Name, RData, Record, RecordType};
use std::net::{IpAddr, Ipv4Addr};
use std::sync::Arc;

const ZONE: &str = "$TTL 60
@ SOA ns.rpz. hostmaster.rpz. 7 60 60 86400 60
@ NS ns.rpz.
bad.example CNAME .
*.bad.example CNAME *.
pass.bad.example CNAME rpz-passthru.
nodata.example CNAME *.
drop.example CNAME rpz-drop.
tcp.example CNAME rpz-tcp-only.
local.example A 10.9.9.9
local.example TXT \"blocked\"
alias.example CNAME walled.garden.
wild.example CNAME *.garden.
32.66.2.0.192.rpz-ip CNAME .
48.zz.db8.2001.rpz-ip CNAME .
32.2.0.0.127.rpz-client-ip CNAME rpz-tcp-only.
ns.evil.rpz-nsdname CNAME .
24.0.113.203.rpz-nsip CNAME .
garbage.rpz-ip CNAME .
";

fn n(s: &str) -> Name { Name::from_ascii(s).unwrap() }
fn w(s: &str) -> Vec<u8> { n(s).to_lowercase().to_bytes().unwrap() }
fn client(last: u8) -> IpAddr { IpAddr::V4(Ipv4Addr::new(127, 0, 0, last)) }

fn zone(id: &str, origin: &str, text: &str, ov: RpzPolicyOverride) -> Arc<RpzZoneIndex> {
    let o = n(origin);
    Arc::new(RpzZoneIndex::build(id, &parse_rpz_text(&o, text).unwrap(), ov as i32))
}

#[test]
fn parses_triggers_actions_and_counts_skipped() {
    let p = parse_rpz_text(&n("rpz.local."), ZONE).unwrap();
    assert_eq!(p.serial, 7);
    assert_eq!(p.skipped, 1);
    assert!(p.rules.contains(&(Trigger::ResponseIp("2001:db8::/48".parse().unwrap()), RpzAction::Nxdomain)));
    assert!(p.rules.contains(&(Trigger::Qname { name: n("bad.example."), wildcard: true }, RpzAction::Nodata)));
    assert!(p.rules.contains(&(Trigger::Nsdname { name: n("ns.evil."), wildcard: false }, RpzAction::Nxdomain)));
    assert!(p.rules.contains(&(Trigger::Nsip("203.0.113.0/24".parse().unwrap()), RpzAction::Nxdomain)));
    assert!(p.rules.contains(&(Trigger::ClientIp("127.0.0.2/32".parse().unwrap()), RpzAction::TcpOnly)));
}

#[test]
fn include_directive_is_rejected() {
    let err = parse_rpz_text(&n("rpz.local."), "$INCLUDE /etc/passwd\n@ SOA a. b. 1 1 1 1 1\n").unwrap_err();
    assert_eq!(err, "$INCLUDE is not allowed in RPZ zones");
}

#[test]
fn ip_trigger_decoding() {
    let l = |s: &str| s.split('.').map(String::from).collect::<Vec<_>>();
    assert_eq!(decode_ip_trigger(&l("24.0.2.0.192")), Some("192.0.2.0/24".parse().unwrap()));
    assert_eq!(decode_ip_trigger(&l("128.1.zz.2001")), Some("2001::1/128".parse().unwrap()));
    assert_eq!(decode_ip_trigger(&l("33.1.2.0.192")), None);
    assert_eq!(decode_ip_trigger(&l("24.0.2.0.300")), None);
}

#[test]
fn qname_exact_wildcard_and_client_ip_precedence() {
    let set = RpzSet::new(vec![zone("z", "rpz.local.", ZONE, RpzPolicyOverride::Given)]);
    assert!(matches!(set.check_query(&w("bad.example."), client(1)), QueryPhase::Hit { zone: 0, action: RpzAction::Nxdomain }));
    assert!(matches!(set.check_query(&w("x.y.bad.example."), client(1)), QueryPhase::Hit { action: RpzAction::Nodata, .. }));
    assert!(matches!(set.check_query(&w("pass.bad.example."), client(1)), QueryPhase::Hit { action: RpzAction::Passthru, .. }));
    assert!(matches!(set.check_query(&w("notbad.example."), client(1)), QueryPhase::NoMatch));
    assert!(matches!(set.check_query(&w("bad.example."), client(2)), QueryPhase::Hit { action: RpzAction::TcpOnly, .. }), "CLIENT-IP beats QNAME");
}

#[test]
fn response_triggers_ip_before_nsdname_before_nsip() {
    let set = RpzSet::new(vec![zone("z", "rpz.local.", ZONE, RpzPolicyOverride::Given)]);
    let ans = vec![Record::from_rdata(n("www.example."), 60, RData::A(A(Ipv4Addr::new(192, 0, 2, 66))))];
    let chain = vec![n("www.example.")];
    assert!(matches!(set.check_response(1, &chain, &ans, &[], &[]), Some((0, RpzAction::Nxdomain))));
    let ok = vec![Record::from_rdata(n("www.example."), 60, RData::A(A(Ipv4Addr::new(192, 0, 2, 67))))];
    assert!(set.check_response(1, &chain, &ok, &[], &[]).is_none());
    assert!(set.check_response(1, &chain, &ok, &[n("NS.evil.")], &[]).is_some());
    assert!(set.check_response(1, &chain, &ok, &[], &["203.0.113.9".parse().unwrap()]).is_some());
    let cname_chain = vec![n("www.example."), n("bad.example.")];
    assert!(matches!(set.check_response(1, &cname_chain, &ok, &[], &[]), Some((0, RpzAction::Nxdomain))), "QNAME trigger on a CNAME target");
}

#[test]
fn zone_order_decides_and_earlier_response_triggers_defer() {
    let allow = "@ SOA a. b. 1 60 60 60 60\nallow.example CNAME rpz-passthru.\n";
    let deny = "@ SOA a. b. 1 60 60 60 60\nallow.example CNAME .\nx.example CNAME .\n";
    let set = RpzSet::new(vec![zone("a", "allow.rpz.", allow, RpzPolicyOverride::Given), zone("d", "deny.rpz.", deny, RpzPolicyOverride::Given)]);
    assert!(matches!(set.check_query(&w("allow.example."), client(1)), QueryPhase::Hit { zone: 0, action: RpzAction::Passthru }));
    let set = RpzSet::new(vec![zone("d", "deny.rpz.", deny, RpzPolicyOverride::Given), zone("a", "allow.rpz.", allow, RpzPolicyOverride::Given)]);
    assert!(matches!(set.check_query(&w("allow.example."), client(1)), QueryPhase::Hit { zone: 0, action: RpzAction::Nxdomain }));
    let ipzone = "@ SOA a. b. 1 60 60 60 60\n32.66.2.0.192.rpz-ip CNAME *.\n";
    let set = RpzSet::new(vec![zone("i", "ip.rpz.", ipzone, RpzPolicyOverride::Given), zone("d", "deny.rpz.", deny, RpzPolicyOverride::Given)]);
    assert!(matches!(set.check_query(&w("x.example."), client(1)), QueryPhase::Deferred { zone: 1, .. }));
    let ans = vec![Record::from_rdata(n("x.example."), 60, RData::A(A(Ipv4Addr::new(192, 0, 2, 66))))];
    assert!(matches!(set.check_response(1, &[n("x.example.")], &ans, &[], &[]), Some((0, RpzAction::Nodata))));
}

#[test]
fn actions_synthesise_responses() {
    let z = zone("z", "rpz.local.", ZONE, RpzPolicyOverride::Given);
    let msg = |o: PolicyOutcome| match o { PolicyOutcome::Respond { wire, ede } => (Message::from_vec(&wire).unwrap(), ede), other => panic!("{other:?}") };
    let (m, ede) = msg(apply_action(&n("bad.example."), RecordType::A, false, &z, &RpzAction::Nxdomain));
    assert_eq!(m.metadata.response_code, ResponseCode::NXDomain);
    assert_eq!(m.authorities[0].record_type(), RecordType::SOA);
    assert_eq!(ede.code, 15);
    let local = match parse_rpz_text(&n("rpz.local."), ZONE).unwrap().rules.into_iter().find(|(t, _)| *t == Trigger::Qname { name: n("local.example."), wildcard: false }).unwrap().1 { a @ RpzAction::LocalData(_) => a, other => panic!("{other:?}") };
    let (m, ede) = msg(apply_action(&n("LOCAL.example."), RecordType::A, false, &z, &local));
    assert_eq!(m.answers[0].data, RData::A(A(Ipv4Addr::new(10, 9, 9, 9))));
    assert_eq!(m.answers[0].ttl, 60);
    assert_eq!(ede.code, 4);
    let (m, _) = msg(apply_action(&n("local.example."), RecordType::AAAA, false, &z, &local));
    assert_eq!((m.metadata.response_code, m.answers.len()), (ResponseCode::NoError, 0));
    assert!(matches!(apply_action(&n("tcp.example."), RecordType::A, false, &z, &RpzAction::TcpOnly), PolicyOutcome::Truncate { .. }));
    assert!(matches!(apply_action(&n("tcp.example."), RecordType::A, true, &z, &RpzAction::TcpOnly), PolicyOutcome::Passthru));
    assert!(matches!(apply_action(&n("drop.example."), RecordType::A, false, &z, &RpzAction::Drop), PolicyOutcome::Drop));
    let wild = parse_rpz_text(&n("rpz.local."), ZONE).unwrap().rules.into_iter().find(|(t, _)| *t == Trigger::Qname { name: n("wild.example."), wildcard: false }).unwrap().1;
    match apply_action(&n("wild.example."), RecordType::A, false, &z, &wild) {
        PolicyOutcome::ChaseCname { cname, target } => { assert_eq!(target, n("wild.example.garden.")); assert_eq!(cname.data, RData::CNAME(CNAME(n("wild.example.garden.")))); }
        other => panic!("{other:?}"),
    }
}

#[test]
fn policy_override_replaces_or_disables_action() {
    let set = RpzSet::new(vec![zone("z", "rpz.local.", ZONE, RpzPolicyOverride::Nodata)]);
    assert_eq!(set.effective_action(0, &RpzAction::Nxdomain), Some(RpzAction::Nodata));
    let set = RpzSet::new(vec![zone("z", "rpz.local.", ZONE, RpzPolicyOverride::Disabled)]);
    assert_eq!(set.effective_action(0, &RpzAction::Nxdomain), None);
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::rpz::rpz_tests` — expect FAIL with "unresolved import `super::apply`".
- [ ] Implement `parse.rs`:
  - `parse_rpz_text`: if any line, after trimming leading whitespace, starts with `$INCLUDE` (ASCII case-insensitive) → `Err("$INCLUDE is not allowed in RPZ zones")`; parse with `Parser::new(text, None, Some(origin.clone())).parse()`; flatten the `RecordSet`s into records and call `parse_rpz_records`.
  - `parse_rpz_records`: the apex SOA is required (`Err("RPZ zone has no SOA")`); apex NS/SOA are not rules. Every other owner must be under `origin` (else `skipped += 1`); relative labels = owner minus origin. Suffix dispatch on the last relative label: `rpz-client-ip` → `ClientIp`, `rpz-ip` → `ResponseIp`, `rpz-nsip` → `Nsip` (labels before the suffix through `decode_ip_trigger`, `None` → skipped), `rpz-nsdname` → `Nsdname` over the preceding labels (leading `*` → wildcard), anything else → `Qname` (leading `*` → wildcard). Records of one owner are grouped into one action: a single CNAME whose target (with `origin` stripped when it ends in `origin`) is the root → `Nxdomain`; `*.` → `Nodata`; `rpz-passthru.` or equal to the trigger name → `Passthru`; `rpz-drop.` → `Drop`; `rpz-tcp-only.` → `TcpOnly`; target whose first label is `*` → `LocalData { cname: WildcardSuffix(target minus the "*" label) }`; other CNAME target → `LocalData { cname: Name(target) }`; non-CNAME records → `LocalData { records }`; CNAME mixed with other types → skipped. `records` counts every non-apex record read.
  - `decode_ip_trigger`: first label = prefix length; IPv4 when exactly 5 labels, all octets decimal ≤ 255, prefix 1..=32, octets given least-significant first; IPv6 otherwise: remaining labels are hex words least-significant first, at most one `zz` meaning the run of zero words needed to make 8, prefix 1..=128; the network is truncated to the prefix (`IpNet::trunc`).
- [ ] Implement `index.rs` lookup tables per zone: `qname_exact`, `qname_wild`, `nsdname_exact`, `nsdname_wild` as `FxHashMap<Box<[u8]>, RpzAction>` keyed by lowercase wire names; `client_ip`, `resp_ip`, `nsip` as `IpTable { v4: FxHashMap<(u8, u32), RpzAction>, v6: FxHashMap<(u8, u128), RpzAction>, v4_lens: Vec<u8>, v6_lens: Vec<u8> }` with prefix lengths sorted descending; lookup masks the address for each present length (longest first). Name lookup over wire bytes without allocation:

```rust
fn lookup_name<'a>(exact: &'a FxHashMap<Box<[u8]>, RpzAction>, wild: &'a FxHashMap<Box<[u8]>, RpzAction>, wire: &[u8]) -> Option<&'a RpzAction> {
    if let Some(a) = exact.get(wire) { return Some(a); }
    // proper suffixes, longest (deepest) first: skip the first label, then the next ...
    let mut off = 0usize;
    while wire[off] != 0 {
        off += 1 + wire[off] as usize;
        if let Some(a) = wild.get(&wire[off..]) { return Some(a); }
    }
    None
}
```

`wild` keys are the wire form of the name after the `*` label, so `*.bad.example` matches `x.bad.example` and `x.y.bad.example` but not `bad.example`. `check_query`: iterate zones in order; for zone `i` check `client_ip` then QNAME; on a hit return `Deferred { zone: i, .. }` if any zone `< i` `has_response_triggers()`, else `Hit`. `check_response(upto_zone, ..)`: for zones `0..upto_zone` (when called for `NoMatch`, `upto_zone = zones.len()`), per zone in precedence order: QNAME over `chain[1..]`, response IP over A/AAAA answers, NSDNAME over `ns_names` (lowercased wire, computed once per call), NSIP over `ns_addrs`; first hit returns. `effective_action`: `policy_override` `GIVEN` → clone; `DISABLED` → `None`; others → the corresponding fixed action. Each hit increments `hits`.

- [ ] Implement `apply.rs`: responses are `Message::response(0, Query)`, RA=1, AA=0, AD=0, question `qname` lowercased. `Nxdomain`/`Nodata` → rcode NXDOMAIN/NOERROR with the zone SOA in the authority section (TTL = min(SOA TTL, SOA minimum)), EDE `{15, "rpz <origin>"}`. `LocalData`: records of `qtype` (or all when `qtype == ANY`) with owner = `qname` and their own TTL → EDE `{4, "rpz <origin>"}`; with `cname` → `ChaseCname { cname: CNAME record qname → target (WildcardSuffix: qname labels + suffix), target }`; no records of `qtype` and no CNAME → NOERROR/NODATA with SOA and EDE 4. `TcpOnly` over UDP → `Truncate` (NOERROR, TC=1, no records); over TCP → `Passthru`. `Drop` → `Drop`. `Passthru` → `Passthru`.
- [ ] Wire into the query path:
  - `server/mod.rs` `handle_packet`, after the M2 verdict `match` (so only `Pass`/`Allowed` reach it) and before `CacheKey::in_partition`:

```rust
let recursor = &ctx.shared.recursor;
if recursor.rpz_query_triggers.load(Ordering::Relaxed) {
    let set = recursor.rpz.load();
    let pending = match set.check_query(q.key.as_wire(), client.ip()) {
        QueryPhase::NoMatch => RpzPending::None,
        QueryPhase::Hit { zone, action } => match set.effective_action(zone, action) {
            None | Some(RpzAction::Passthru) => RpzPending::None,
            Some(action) => RpzPending::Apply { zone, action },
        },
        QueryPhase::Deferred { zone, action } => RpzPending::Deferred { zone, action: action.clone() },
    };
    match pending {
        RpzPending::None => {}
        RpzPending::Apply { zone, action } if !matches!(&action, RpzAction::LocalData(d) if d.cname.is_some()) => {
            let qname = Name::from_vec(q.key.as_wire()).unwrap_or_else(|_| Name::root()); // allocates only on an RPZ hit
            match apply_action(&qname, RecordType::from(q.qtype), transport != Transport::Udp, &set.zones[zone], &action) {
                PolicyOutcome::Respond { wire, ede } => return rpz_reply(&scope, rec, &q, &wire, Some(ede.code), &action, out, limit, opt),
                PolicyOutcome::Truncate { wire } => return rpz_reply(&scope, rec, &q, &wire, None, &action, out, limit, opt),
                PolicyOutcome::Drop => return FastOutcome::Drop,
                PolicyOutcome::Passthru | PolicyOutcome::ChaseCname { .. } => {}
            }
        }
        pending => return FastOutcome::Miss(MissJob { query: packet.into(), client, transport, key: CacheKey::in_partition(&q, policy.cache_partition()), question: Question { key: q.key, qtype: q.qtype, qclass: q.qclass }, limit, opt, started: scope.started, filter_us: rec.filter_us, cache_us: 0, rpz: pending }),
    }
}
```

- [ ] Complete the RPZ wiring:
  - `rpz_reply` (a private helper next to `reply`) builds `cache::prepare_uncached(wire, q)`, writes it with `cache::write_cached(&entry, q, 0, ServeMode::Fresh, out, limit, opt.map(|o| ReplyOpt { ede, ..o }).as_ref())`, sets `rec.rpz_action` and finishes like `reply`. The M1 `MissJob` literal at the end of `handle_packet` sets `rpz: RpzPending::None`.
  - `server/mod.rs` `resolve_miss`: when `job.rpz` is not `RpzPending::None`, skip `ctx.shared.inflight.join` (the answer is policy-specific), call `dispatch::resolve_miss` directly with `MissQuery { rpz: job.rpz.clone(), .. }` and never insert into the cache. For ordinary misses the leader records `let rpz_at_start = ctx.shared.recursor.rpz.load_full();` before resolving and passes the answer to `leader_answer` only when `Arc::ptr_eq(&rpz_at_start, &ctx.shared.recursor.rpz.load_full())` still holds (otherwise it serves the answer uncached).
  - `dispatch::resolve_miss`: `RpzPending::Apply` whose `apply_action` yields `ChaseCname { cname, target }` → resolve `target` on its own route (with validation as usual), prepend `cname` to the answers, EDE `{4, "rpz <origin>"}`, `cacheable: false`. After validation, and only when `rpz.has_response_triggers` or the query carries `RpzPending::Deferred`: decode the answer (`Message::from_vec` of the upstream bytes on the forward route), `chain` = `qname` plus CNAME targets in `answers`; `check_response(deferred zone or zones.len(), ..)` with `Resolution.ns_names/ns_addrs` (empty for forward routes, so NSDNAME/NSIP triggers only fire in recursive mode); a hit (or else the deferred action) goes through `effective_action` + `apply_action`; policy results set `cacheable: false`, `rpz_action`, `ede`, `drop`.
  - No cache stamping: cache invalidation for RPZ changes is Architecture change 4 (`r:` key in `filter_hashes` for snapshot changes, cache clear on transfer publication in Task 10, the `rpz_at_start` check above).
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::rpz::rpz_tests` — expect PASS (8 tests).
- [ ] Run `scripts/dev-exec.sh make engine-test` — expect PASS including `cache_hit_path_does_not_allocate`.
- [ ] Commit: `git add engine/src && git commit -m "feat(rpz): RPZ triggers, precedence and actions on the query path"`.

## Task 10: RPZ sources — blob files, AXFR/IXFR with TSIG, SOA refresh, last-good persistence

Files:

- `engine/src/recursor/rpz/tsig.rs` — RFC 8945 TSIG signing of requests and verification of (multi-message) responses with `ring::hmac`.
- `engine/src/recursor/rpz/transfer.rs` — SOA check, IXFR (RFC 1995) with AXFR fallback, RFC 1982 serial comparison, timers.
- `engine/src/recursor/rpz/manager.rs` — per-zone lifecycle, `RpzSet` publication, persistence under `state_dir/rpz/`, in-memory TSIG keys, status.
- `engine/src/recursor/rpz/transfer_tests.rs` — unit tests with an in-test primary.
- `engine/src/recursor/dispatch.rs` — `ResolutionRuntime.rpz: Vec<RpzZoneConfig>` built from `s.rpz_zones` (file zones parsed from their blobs).
- `engine/src/recursor/mod.rs` — `RecursorState.rpz_manager`, `set_rpz_tsig_keys`; `sync` and `spawn_background` drive the manager.
- `engine/src/control.rs` — applies `ServerMessage.rpz_tsig_keys`; `Stats.rpz_zones`.
- `engine/src/telemetry/metrics.rs` — `nexora_rpz_hits_total{zone}`, `nexora_rpz_zone_serial{zone}`, `nexora_rpz_zone_records{zone}`, `nexora_rpz_zone_stale{zone}`, `nexora_rpz_zone_last_success_timestamp_seconds{zone}`, `nexora_rpz_refresh_failures_total{zone}` (`zone` label = zone origin; counters registered without `_total`).

Interfaces:

```rust
// tsig.rs
#[derive(Clone, Copy, Debug, PartialEq, Eq)] pub enum TsigAlg { HmacSha256, HmacSha512 }
#[derive(Clone)] pub struct TsigKey { pub name: Name, pub alg: TsigAlg, pub secret: zeroize::Zeroizing<Vec<u8>> } // manual Debug prints "secret: <redacted>"
#[derive(Debug, PartialEq, Eq)] pub enum TsigError { Missing, BadKey, BadSig, BadTime, Malformed, TooManyUnsigned }
pub const FUDGE: u16 = 300;
pub fn skip_name(b: &[u8], i: usize) -> Option<usize>;
pub fn extract_mac(wire: &[u8]) -> Option<Vec<u8>>; // MAC field of a trailing TSIG RR
pub fn sign_request(wire: &mut Vec<u8>, key: &TsigKey, time_signed: u64) -> Vec<u8>;                         // appends TSIG RR, returns MAC
pub fn sign_response(wire: &mut Vec<u8>, key: &TsigKey, time_signed: u64, prior_mac: &[u8], first: bool, unsigned_before: &[u8]) -> Vec<u8>; // test primary + completeness
pub struct TsigVerifier { key: TsigKey, prior_mac: Vec<u8>, first: bool, unsigned: Vec<u8>, unsigned_count: u32 }
impl TsigVerifier {
    pub fn new(key: TsigKey, request_mac: Vec<u8>) -> Self;
    pub fn verify(&mut self, wire: &[u8], now: u64) -> Result<(), TsigError>;
    pub fn finish(&self) -> Result<(), TsigError>; // the last message must have been signed
}
// transfer.rs
#[derive(Clone, Debug, PartialEq)] pub struct ZoneData { pub serial: u32, pub records: Vec<Record> } // records exclude the trailing SOA duplicate
pub fn serial_gt(a: u32, b: u32) -> bool;
#[derive(Debug, PartialEq, Eq)] pub struct Timers { pub refresh: u64, pub retry: u64, pub expire: u64 }
pub fn timers_from_soa(refresh: u32, retry: u32, expire: u32, min_refresh: u32) -> Timers;
pub fn apply_ixfr(current: &ZoneData, answers: &[Record]) -> Result<ZoneData, String>;
pub async fn soa_serial(primary: SocketAddr, zone: &Name, key: Option<&TsigKey>) -> Result<(u32, SOA), String>;
pub async fn transfer(primary: SocketAddr, zone: &Name, current: Option<&ZoneData>, key: Option<&TsigKey>) -> Result<ZoneData, String>;
// dispatch.rs
#[derive(Clone)] pub enum RpzSourceConfig { File(Arc<RpzZoneIndex>), Transfer { primary: SocketAddr, tsig_key_name: String, tsig_algorithm: i32, min_refresh_seconds: u32 } }
#[derive(Clone)] pub struct RpzZoneConfig { pub id: String, pub origin: Name, pub policy_override: i32, pub refresh_nonce: u64, pub source: RpzSourceConfig }
// ResolutionRuntime gains: pub rpz: Vec<RpzZoneConfig>  (snapshot order)
// manager.rs
pub struct RpzManager { /* dir: Option<PathBuf>, Mutex<Vec<ZoneTask>> in config order, Mutex<FxHashMap<String, TsigKey>>, tokio::sync::Notify */ }
impl RpzManager {
    pub fn new(state_dir: Option<&Path>) -> Self;
    pub fn apply_config(&self, state: &RecursorState, zones: &[RpzZoneConfig]); // publishes file zones and persisted transfer copies immediately
    pub fn set_tsig_keys(&self, keys: &proto::RpzTsigKeys);                    // replaces the in-memory key set; zones whose key changed refresh at once
    pub async fn refresh_now(&self, state: &RecursorState, id: &str) -> Result<bool, String>; // Ok(true) when new data was published
    pub async fn run(&self, shared: Arc<crate::server::Shared>);              // timer loop on the nexora-recursor thread
    pub fn status(&self) -> Vec<proto::RpzZoneStatus>;
}
// mod.rs
// RecursorState gains: pub rpz_manager: RpzManager
impl RecursorState { pub fn set_rpz_tsig_keys(&self, keys: &proto::RpzTsigKeys); }
```

- [ ] Create `engine/src/recursor/rpz/transfer_tests.rs`:

```rust
use super::transfer::*;
use super::tsig::*;
use crate::proto;
use crate::recursor::dispatch::ResolutionRuntime;
use crate::recursor::RecursorState;
use crate::snapshot::DirBlobs;
use hickory_proto::op::{Message, OpCode, Query};
use hickory_proto::rr::{rdata::{CNAME, SOA}, Name, RData, Record, RecordType};
use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;
use zeroize::Zeroizing;

fn n(s: &str) -> Name { Name::from_ascii(s).unwrap() }
fn key() -> TsigKey { TsigKey { name: n("rpz-key."), alg: TsigAlg::HmacSha256, secret: Zeroizing::new(vec![0x42; 32]) } }
fn soa(serial: u32) -> Record { Record::from_rdata(n("rpz.test."), 60, RData::SOA(SOA::new(n("ns.rpz.test."), n("h.rpz.test."), serial, 2, 1, 30, 60))) }
fn block(name: &str) -> Record { Record::from_rdata(n(&format!("{name}.rpz.test.")), 60, RData::CNAME(CNAME(Name::root()))) }

/// Serves AXFR/IXFR/SOA over TCP from `zone` (serial, records); each AXFR is split into one message per record and TSIG-signed
/// on the first and last message only, exercising the unsigned-intermediate rule.
async fn primary(zone: Arc<Mutex<(u32, Vec<Record>)>>, signing: Option<TsigKey>) -> std::net::SocketAddr {
    let l = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = l.local_addr().unwrap();
    tokio::spawn(async move {
        loop {
            let (mut s, _) = l.accept().await.unwrap();
            let zone = zone.clone();
            let signing = signing.clone();
            tokio::spawn(async move {
                let len = s.read_u16().await.unwrap() as usize;
                let mut buf = vec![0u8; len];
                s.read_exact(&mut buf).await.unwrap();
                let req = Message::from_vec(&buf).unwrap();
                let request_mac = req_mac(&buf);
                let (serial, records) = zone.lock().unwrap().clone();
                let qt = req.queries[0].query_type();
                let mut bodies: Vec<Vec<Record>> = vec![];
                if qt == RecordType::SOA { bodies.push(vec![soa(serial)]); }
                else { bodies.push(vec![soa(serial)]); for r in records { bodies.push(vec![r]); } bodies.push(vec![soa(serial)]); }
                let mut prior = request_mac;
                let mut unsigned = Vec::new();
                let last = bodies.len() - 1;
                for (i, body) in bodies.into_iter().enumerate() {
                    let mut m = Message::response(req.metadata.id, OpCode::Query);
                    m.metadata.authoritative = true;
                    m.queries.push(Query::query(n("rpz.test."), qt));
                    m.answers = body;
                    let mut wire = m.to_vec().unwrap();
                    if let Some(k) = &signing {
                        if i == 0 || i == last {
                            prior = sign_response(&mut wire, k, now(), &prior, i == 0, &unsigned);
                            unsigned.clear();
                        } else {
                            unsigned.extend_from_slice(&wire);
                        }
                    }
                    s.write_u16(wire.len() as u16).await.unwrap();
                    s.write_all(&wire).await.unwrap();
                }
            });
        }
    });
    addr
}

fn now() -> u64 { std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_secs() }

/// MAC of the TSIG RR at the end of a signed request, empty when unsigned.
fn req_mac(wire: &[u8]) -> Vec<u8> { extract_mac(wire).unwrap_or_default() }

#[test]
fn serial_arithmetic_rfc1982() {
    assert!(serial_gt(2, 1));
    assert!(serial_gt(0, 0xFFFF_FFFF));
    assert!(!serial_gt(1, 1));
    assert!(serial_gt(0x7FFF_FFFF, 0));
    assert!(!serial_gt(0x8000_0000, 0), "distance 2^31 is undefined and treated as not greater");
    assert!(!serial_gt(1, 2));
}

#[test]
fn timers_respect_min_refresh() {
    assert_eq!(timers_from_soa(2, 1, 30, 60), Timers { refresh: 60, retry: 60, expire: 60 });
    assert_eq!(timers_from_soa(3600, 600, 86400, 60), Timers { refresh: 3600, retry: 600, expire: 86400 });
}

#[test]
fn tsig_sign_and_verify_detects_tampering_wrong_key_and_time() {
    let mut req = Message::query().to_vec().unwrap();
    let mac = sign_request(&mut req, &key(), 1_800_000_000);
    let mut resp = Message::response(0, OpCode::Query).to_vec().unwrap();
    sign_response(&mut resp, &key(), 1_800_000_000, &mac, true, &[]);
    let mut v = TsigVerifier::new(key(), mac.clone());
    assert_eq!(v.verify(&resp, 1_800_000_100), Ok(()));
    let mut tampered = resp.clone();
    tampered[3] ^= 0x01;
    assert_eq!(TsigVerifier::new(key(), mac.clone()).verify(&tampered, 1_800_000_100), Err(TsigError::BadSig));
    let wrong = TsigKey { secret: Zeroizing::new(vec![0x43; 32]), ..key() };
    assert_eq!(TsigVerifier::new(wrong, mac.clone()).verify(&resp, 1_800_000_100), Err(TsigError::BadSig));
    assert_eq!(TsigVerifier::new(key(), mac.clone()).verify(&resp, 1_800_000_301), Err(TsigError::BadTime));
    let unsigned = Message::response(0, OpCode::Query).to_vec().unwrap();
    let mut v = TsigVerifier::new(key(), mac);
    assert_eq!(v.verify(&unsigned, 1_800_000_000), Err(TsigError::Missing), "first message must be signed");
}

#[test]
fn ixfr_applies_deletions_and_additions() {
    let cur = ZoneData { serial: 1, records: vec![soa(1), block("a"), block("b")] };
    // IXFR: new SOA, old SOA, deleted..., new SOA, added..., new SOA
    let answers = vec![soa(2), soa(1), block("a"), soa(2), block("c"), soa(2)];
    let next = apply_ixfr(&cur, &answers).unwrap();
    assert_eq!(next.serial, 2);
    assert!(next.records.contains(&block("b")) && next.records.contains(&block("c")) && !next.records.contains(&block("a")));
    // AXFR-style reply to an IXFR request replaces the zone
    let full = apply_ixfr(&cur, &[soa(3), block("z"), soa(3)]).unwrap();
    assert_eq!(full.records, vec![soa(3), block("z")]);
    assert!(apply_ixfr(&cur, &[soa(1)]).unwrap() == cur, "single SOA with current serial = up to date");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn axfr_with_tsig_then_incremental_update() {
    let zone = Arc::new(Mutex::new((5u32, vec![block("a"), block("b")])));
    let addr = primary(zone.clone(), Some(key())).await;
    let z = transfer(addr, &n("rpz.test."), None, Some(&key())).await.unwrap();
    assert_eq!(z.serial, 5);
    assert_eq!(z.records.len(), 3);
    let unsigned_primary = primary(zone.clone(), None).await;
    assert!(transfer(unsigned_primary, &n("rpz.test."), None, Some(&key())).await.unwrap_err().contains("TSIG"));
    assert_eq!(soa_serial(addr, &n("rpz.test."), Some(&key())).await.unwrap().0, 5);

fn transfer_snapshot(primary: SocketAddr, tsig: bool) -> proto::ConfigSnapshot {
    proto::ConfigSnapshot {
        rpz_zones: vec![proto::RpzZone {
            id: "z1".into(),
            name: "rpz.test.".into(),
            source: Some(proto::rpz_zone::Source::Transfer(proto::RpzTransferSource {
                primary: primary.to_string(),
                tsig_key_name: if tsig { "rpz-key.".into() } else { String::new() },
                tsig_algorithm: if tsig { proto::TsigAlgorithm::HmacSha256 as i32 } else { 0 },
                min_refresh_seconds: 1,
            })),
            policy_override: 0,
            refresh_nonce: 0,
        }],
        ..Default::default()
    }
}

fn resolution(s: &proto::ConfigSnapshot) -> ResolutionRuntime {
    ResolutionRuntime::build(s, &DirBlobs { dir: "/nonexistent/nexora-test-blobs".into() }).unwrap()
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn manager_keeps_last_good_zone_when_primary_fails_and_reloads_from_disk() {
    let zone = Arc::new(Mutex::new((5u32, vec![block("a")])));
    let addr = primary(zone.clone(), None).await;
    let dir = tempfile::tempdir().unwrap();
    let state = RecursorState::new(Some(dir.path()));
    state.rpz_manager.apply_config(&state, &resolution(&transfer_snapshot(addr, false)).rpz);
    assert!(state.rpz_manager.refresh_now(&state, "z1").await.unwrap(), "first transfer publishes");
    assert_eq!(state.rpz.load().zones.len(), 1);
    assert!(dir.path().join("rpz/z1.zone").exists());
    let dead: SocketAddr = "127.0.0.1:1".parse().unwrap();
    state.rpz_manager.apply_config(&state, &resolution(&transfer_snapshot(dead, false)).rpz);
    assert!(state.rpz_manager.refresh_now(&state, "z1").await.is_err());
    assert_eq!(state.rpz.load().zones.len(), 1, "last good zone kept");
    let st = &state.rpz_manager.status()[0];
    assert!(!st.last_error.is_empty());
    assert_eq!(st.serial, 5);
    let state2 = RecursorState::new(Some(dir.path()));
    state2.rpz_manager.apply_config(&state2, &resolution(&transfer_snapshot(dead, false)).rpz);
    assert_eq!(state2.rpz.load().zones.len(), 1, "persisted zone served after restart before any transfer");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn tsig_zone_waits_for_key_material_held_in_memory_only() {
    let zone = Arc::new(Mutex::new((9u32, vec![block("a")])));
    let addr = primary(zone.clone(), Some(key())).await;
    let dir = tempfile::tempdir().unwrap();
    let state = RecursorState::new(Some(dir.path()));
    state.rpz_manager.apply_config(&state, &resolution(&transfer_snapshot(addr, true)).rpz);
    let err = state.rpz_manager.refresh_now(&state, "z1").await.unwrap_err();
    assert!(err.contains("tsig key material not received"), "{err}");
    state.set_rpz_tsig_keys(&proto::RpzTsigKeys { keys: vec![proto::RpzTsigKey { zone_id: "z1".into(), key_name: "rpz-key.".into(), algorithm: proto::TsigAlgorithm::HmacSha256 as i32, secret: vec![0x42; 32] }] });
    assert!(state.rpz_manager.refresh_now(&state, "z1").await.unwrap());
    assert_eq!(state.rpz_manager.status()[0].serial, 9);
    for entry in walk(dir.path()) {
        let bytes = std::fs::read(&entry).unwrap();
        assert!(!bytes.windows(32).any(|w| w == [0x42u8; 32]), "{} holds the TSIG secret", entry.display());
    }
}

fn walk(dir: &std::path::Path) -> Vec<std::path::PathBuf> {
    let mut out = Vec::new();
    for e in std::fs::read_dir(dir).unwrap() {
        let p = e.unwrap().path();
        if p.is_dir() { out.extend(walk(&p)); } else { out.push(p); }
    }
    out
}
```

- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::rpz::transfer_tests` — expect FAIL with "cannot find type `TsigKey` in this scope".
- [ ] Implement `tsig.rs`. Wire helpers:

```rust
pub fn skip_name(b: &[u8], mut i: usize) -> Option<usize> {
    loop {
        let len = *b.get(i)? as usize;
        if len & 0xC0 == 0xC0 { b.get(i + 1)?; return Some(i + 2); }
        if len & 0xC0 != 0 { return None; }
        i += 1;
        if len == 0 { return Some(i); }
        i += len;
        if i > b.len() { return None; }
    }
}

/// Offset of the last RR in the additional section when it is a TSIG RR.
fn tsig_offset(b: &[u8]) -> Option<usize> {
    if b.len() < 12 { return None; }
    let count = |o: usize| u16::from_be_bytes([b[o], b[o + 1]]) as usize;
    let (qd, an, ns, ar) = (count(4), count(6), count(8), count(10));
    if ar == 0 { return None; }
    let mut i = 12;
    for _ in 0..qd { i = skip_name(b, i)? + 4; }
    let mut last = i;
    for _ in 0..(an + ns + ar) {
        last = i;
        i = skip_name(b, i)?;
        let rdlen = u16::from_be_bytes([*b.get(i + 8)?, *b.get(i + 9)?]) as usize;
        i += 10 + rdlen;
        if i > b.len() { return None; }
    }
    let t = skip_name(b, last)?;
    (u16::from_be_bytes([b[t], b[t + 1]]) == 250).then_some(last)
}

fn variables(key: &TsigKey, time_signed: u64, error: u16, other: &[u8], timers_only: bool) -> Vec<u8> {
    let mut v = Vec::new();
    if !timers_only {
        v.extend(key.name.to_lowercase().to_bytes().unwrap());
        v.extend(255u16.to_be_bytes()); // CLASS ANY
        v.extend(0u32.to_be_bytes());   // TTL
        v.extend(Name::from_ascii(key.alg.name()).unwrap().to_bytes().unwrap());
    }
    v.extend(&time_signed.to_be_bytes()[2..]); // 48-bit time
    v.extend(FUDGE.to_be_bytes());
    if !timers_only {
        v.extend(error.to_be_bytes());
        v.extend((other.len() as u16).to_be_bytes());
        v.extend(other);
    }
    v
}
```

`TsigAlg::name()` → `"hmac-sha256."` / `"hmac-sha512."`; ring algorithm `hmac::HMAC_SHA256` / `HMAC_SHA512`. Request MAC input = message (as sent, ARCOUNT not yet incremented) ‖ `variables(.., timers_only=false)`. Response MAC input (RFC 8945 §5.3): `u16 len(prior_mac) ‖ prior_mac ‖ unsigned_before ‖ message_without_tsig ‖ variables(.., timers_only = !first)` where `message_without_tsig` has ARCOUNT decremented and the ID replaced by the TSIG original ID. The appended RR: owner = key name wire, TYPE 250, CLASS 255, TTL 0, RDATA = algorithm name wire ‖ 48-bit time ‖ fudge ‖ `u16` MAC size ‖ MAC ‖ original ID ‖ error 0 ‖ other len 0; ARCOUNT incremented. `TsigVerifier::verify`: `tsig_offset` absent → if `first` → `Missing`; else append the whole message to `unsigned`, `unsigned_count += 1`, `> 99` → `TooManyUnsigned`, return `Ok`. Present → parse the RR (algorithm name must equal the key's, owner must equal key name, case-insensitive, else `BadKey`), rebuild the MAC input as above, `ring::hmac::verify` (constant time) → `BadSig`; `|now - time_signed| > fudge` → `BadTime`; then `prior_mac = mac`, `first = false`, clear `unsigned`, `unsigned_count = 0`. `finish` returns `Missing` when `unsigned_count > 0`.

- [ ] Implement `transfer.rs`: `serial_gt(a, b) = a != b && (a.wrapping_sub(b) as i32) > 0` (the undefined 2^31 distance counts as not greater); `timers_from_soa` floors each value at `min_refresh` (0 → 60). `soa_serial`: UDP query for `SOA` (Transport-style random ID from `rand::rng()`), TCP retry on TC; with a key the query is TSIG-signed and the reply verified. `transfer`: open TCP; with `current` send IXFR (question `IXFR`, authority section = `current` SOA) otherwise AXFR; sign when `key`; read length-prefixed messages until the second occurrence of the SOA whose serial equals the first answer's serial (for IXFR: a single SOA equal to the current serial → up to date); every message rcode must be NOERROR (`NOTIMP`/`REFUSED`/`FORMERR` to IXFR → retry once with AXFR); verify each message with `TsigVerifier` and call `finish`; TSIG failures produce `Err(format!("TSIG verification failed: {e:?}"))`; overall deadline 60 s; limit 10 000 000 records. `apply_ixfr` implements RFC 1995 §4: `[SOA n]` only with serial == current → `current.clone()`; `[SOA n, SOA old, …]` → sequences of (old SOA, deletions, new SOA, additions) applied in order, verifying each old SOA serial equals the running serial; `[SOA n, non-SOA…, SOA n]` → full replacement (records = everything before the trailing SOA).
- [ ] Implement `ResolutionRuntime.rpz` in `dispatch.rs`: for each `s.rpz_zones[i]` (snapshot order) — file source: `blobs.read(blob)` → `zstd::decode_all` → UTF-8 → `parse_rpz_text(origin, text)` → `Arc::new(RpzZoneIndex::build(id, &parsed, policy_override))`; any error returns `Err(format!("rpz_zones[{i}]: {error}"))`, which `Runtime::build` turns into a rejected snapshot; transfer source: `RpzSourceConfig::Transfer` with `primary` parsed as `SocketAddr`.
- [ ] Implement `manager.rs`:
  - `apply_config` (called from `RecursorState::sync` after every applied snapshot): zones not present in the config are dropped (their `state_dir/rpz/<id>.zone` deleted). File zones take the prebuilt index. Transfer zones create or update a `ZoneTask`; if `state_dir/rpz/<id>.zone` exists and no data is loaded yet, parse it (`parse_rpz_text` with `$ORIGIN` taken from the zone name) and serve it immediately. A changed `refresh_nonce` or primary schedules an immediate refresh (`notify_one`). Then publish.
  - Keys: `set_tsig_keys` converts each `RpzTsigKey` into a `TsigKey` (secret moved into `Zeroizing`, name via `Name::from_ascii`, unknown algorithms skipped) keyed by `zone_id`, replaces the map and notifies. A transfer zone with `tsig_algorithm != NONE` and no key in the map fails its refresh with `"tsig key material not received from the management plane"` (the last good copy keeps serving); a key whose name or algorithm differs from the zone's configuration fails with `"tsig key does not match the zone configuration"`. Keys are never logged, persisted or included in `status()`.
  - A refresh of one transfer zone: `soa_serial`; `serial_gt(remote, local)` or no local data → `transfer`; success → build `RpzZoneIndex`, publish, persist records one per line with `format!("{record}\n")` via write-temp + fsync + rename (same pattern as `snapshot::persist`), `last_success = now`, `stale = false`, `next_refresh = now + refresh`, `Ok(true)`; up to date → `Ok(false)`; failure → `last_error`, refresh-failure counter `+= 1`, `next_refresh = now + retry`, and `stale = true` once `now - last_success > expire` — the last good data keeps serving (never dropped).
  - `run(shared)`: loop forever — wait for the earliest `next_refresh` or a notification (`tokio::time::timeout` around `Notify::notified`), refresh every due zone, and when any refresh returned `Ok(true)` call `shared.runtime.load().cache.clear()` so answers cached under the old RPZ data are not served.
  - Publication: rebuild `RpzSet::new(zones in config order)` from every zone with data and `state.publish_rpz(set)`.
  - `refresh_now` performs one refresh cycle synchronously and returns its result. `status()` produces `proto::RpzZoneStatus` per zone in config order (hits from the index).
- [ ] `mod.rs`: `RecursorState` gains `pub rpz_manager: RpzManager` (created with the same `state_dir`); `set_rpz_tsig_keys` delegates to it; `sync` also calls `self.rpz_manager.apply_config(self, &rt.resolution.rpz)`; `spawn_background` also runs `shared.recursor.rpz_manager.run(shared.clone())` on the `nexora-recursor` `LocalSet`.
- [ ] `control.rs` `session`: replace the Task 1 no-op arm with `Some(ServerMsg::RpzTsigKeys(keys)) => shared.recursor.set_rpz_tsig_keys(&keys),` (no log line carries the message); `Metrics::stats` sets `rpz_zones: recursor.rpz_manager.status()` and `render` exposes the RPZ metrics listed under Files.
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib recursor::rpz::transfer_tests` — expect PASS (7 tests); run `scripts/dev-exec.sh make engine-test` — expect PASS.
- [ ] Commit: `git add engine/src && git commit -m "feat(rpz): file and AXFR/IXFR sources with in-memory TSIG keys and last-good persistence"`.

## Task 11: Management plane — key storage helper, migrations, OpenAPI operations, handlers, snapshot builder, TSIG key delivery, stats ingestion

Files:

- `mgmt/internal/secrets/secrets.go`, `mgmt/internal/secrets/secrets_test.go` — NXE1 envelope encryption under the file KEK (the file-KEK half of M4 Task 6's `keystore`).
- `mgmt/internal/config/config.go` — `KEKFile` from `NEXORA_KEK_FILE`.
- `mgmt/migrations/00300_resolution.sql`, `mgmt/migrations/00301_dnssec.sql`, `mgmt/migrations/00302_rpz.sql` — tables below.
- `mgmt/internal/rpz/validate.go`, `mgmt/internal/rpz/validate_test.go` — RPZ zone text validation, zstd packing, TSIG purpose.
- `mgmt/internal/dnssecconf/ds.go`, `mgmt/internal/dnssecconf/ds_test.go` — DS/NTA/root-hint/forward-zone input validation.
- `mgmt/internal/store/resolution.go`, `mgmt/internal/store/blobs.go` — pgx queries for the new tables; `PutBlob`.
- `mgmt/internal/snapshot/resolution.go`, `mgmt/internal/snapshot/resolution_test.go`, `mgmt/internal/snapshot/ntaexpiry.go` — row → proto conversion, expired-NTA job; `mgmt/internal/snapshot/snapshot.go` — `Build` calls it.
- `mgmt/internal/control/rpztsig.go`, `mgmt/internal/control/rpztsig_test.go` — `RPZTsig` key loader; `mgmt/internal/control/hub.go`, `mgmt/internal/control/server.go` — `RpzTsigKeys` delivery.
- `mgmt/internal/api/resolution.go`, `mgmt/internal/api/dnssec.go`, `mgmt/internal/api/rpz.go`, `mgmt/internal/api/resolution_test.go` — strict-server handlers and API test; `mgmt/internal/api/server.go` — `Deps.Secrets`, `mapError` cases.
- `mgmt/internal/auth/permissions.go`, `web/src/auth/permissions.ts` — permission entries (kept in parity for `pnpm lint`).
- `mgmt/internal/stats/resolution.go`, `mgmt/internal/stats/resolution_test.go` — `Stats` fields 100–102 → `engine_dnssec_status`, `engine_rpz_status`.
- `mgmt/cmd/nexora-mgmt/main.go` — KEK loading, `Deps.Secrets`, `hub.RPZTsig`, `OnStats`, NTA expiry job.
- `mgmt/api/openapi.yaml`, `mgmt/internal/api/gen.go` (oapi-codegen in the dev pod, copied back), `web/src/api/schema.d.ts` (`pnpm --dir web run gen:api`).

Interfaces:

```go
// mgmt/internal/secrets — M4 Task 6's keystore.Store wraps *Box as its file-KEK backend.
var (
	ErrUnconfigured       = errors.New("key storage unconfigured: set NEXORA_KEK_FILE")
	ErrKEKMismatch        = errors.New("envelope was sealed under a different key-encryption key")
	ErrBackendUnavailable = errors.New("requested key backend is not configured")
)
const (
	WrapFileKEK byte = 1 // NXE1 byte 4
	WrapPKCS11  byte = 2 // reserved for M4
)
type Box struct{ kek, kekID []byte }
func LoadKEKFile(path string) (*Box, error)                           // "" -> unconfigured Box
func (b *Box) Configured() bool                                       // false for nil
func (b *Box) Seal(purpose string, plaintext []byte) ([]byte, error)  // ErrUnconfigured without a KEK
func (b *Box) Unseal(purpose string, envelope []byte) ([]byte, error)
// mgmt/internal/rpz
type Summary struct{ Serial uint32; Records int }
const MaxZoneBytes = 3 << 20
func ValidateZone(origin, content string) (Summary, error)
func Pack(content string) []byte                 // zstd SpeedDefault, deterministic
func TsigPurpose(zoneID uuid.UUID) string        // "nexora/rpz-tsig/v1:<uuid>"
// mgmt/internal/dnssecconf
func ValidateDS(ds string) error
func ValidateDomain(name string) (fqdnLower string, err error)
func ValidateForwardAddresses(addrs []string) error
func ValidateRootHints(h []RootHint) error
type RootHint struct{ Name string `json:"name"`; Addresses []string `json:"addresses"` }
var IANARootAnchors = []string{
    "20326 8 2 E06D44B80B8F1D39A95C0B0D7C65D08458E880409BBC683457104237C7F8EC8D",
    "38696 8 2 683D2D0ACB8C9B712A1948B27F741219298D0A450D612C483AF444A4C0FB2B16",
}
// mgmt/internal/store
var ErrLastRootAnchor = errors.New("the last trust anchor for the root zone cannot be deleted")
type RootHint = dnssecconf.RootHint
type ResolutionSettings struct{ Mode string; QnameMinimisation, AggressiveNSEC bool; MaxUpstreamQueries, MaxDelegationDepth, AuthorityPort int32; RootHints []RootHint; Revision int64 }
type ForwardZone struct{ ID uuid.UUID; Domain string; Addresses []string; Validate bool; Revision int64 }
type DnssecSettings struct{ Validation, RFC5011 bool; Revision int64 }
type TrustAnchor struct{ ID uuid.UUID; Zone, DS, Source string; CreatedAt time.Time }
type NegativeTrustAnchor struct{ ID uuid.UUID; Domain, Reason, CreatedBy string; ExpiresAt, CreatedAt time.Time }
type RPZZone struct {
	ID                                          uuid.UUID
	Name                                        string
	Position                                    int32
	SourceType                                  string // file | transfer
	BlobSHA256                                  *string
	BlobSize                                    int64
	FileRecords                                 *int32
	PrimaryAddress, TSIGKeyName, TSIGAlgorithm  *string
	TSIGSecretEnvelope                          []byte // NXE1; nil on update keeps the stored envelope
	MinRefreshSeconds                           int32
	PolicyOverride                              string
	RefreshNonce, Revision                      int64
}
type RPZEngineStatus struct{ EngineID uuid.UUID; EngineName string; Serial, Records, Skipped, Hits int64; LastSuccessAt *time.Time; LastError string; Stale bool }
type EngineDnssecStatus struct{ EngineID uuid.UUID; EngineName string; Stats []byte /* protojson DnssecStats */; ReportedAt time.Time }
type ResolutionRows struct{ Resolution ResolutionSettings; ForwardZones []ForwardZone; Dnssec DnssecSettings; Anchors []TrustAnchor; NTAs []NegativeTrustAnchor; RPZ []RPZZone }
func GetResolutionSettings(ctx context.Context, q PolicyQuerier) (ResolutionSettings, error)
func UpdateResolutionSettings(ctx context.Context, tx pgx.Tx, s ResolutionSettings) (ResolutionSettings, error)
func ListForwardZones(ctx context.Context, q PolicyQuerier) ([]ForwardZone, error)
func GetForwardZone(ctx context.Context, q PolicyQuerier, id uuid.UUID) (ForwardZone, error)
func CreateForwardZone(ctx context.Context, tx pgx.Tx, z ForwardZone) (ForwardZone, error)
func UpdateForwardZone(ctx context.Context, tx pgx.Tx, z ForwardZone) (ForwardZone, error)
func DeleteForwardZone(ctx context.Context, tx pgx.Tx, id uuid.UUID, revision int64) error
func GetDnssecSettings(ctx context.Context, q PolicyQuerier) (DnssecSettings, error)
func UpdateDnssecSettings(ctx context.Context, tx pgx.Tx, s DnssecSettings) (DnssecSettings, error)
func ListTrustAnchors(ctx context.Context, q PolicyQuerier) ([]TrustAnchor, error)
func CreateTrustAnchor(ctx context.Context, tx pgx.Tx, zone, ds string) (TrustAnchor, error)
func DeleteTrustAnchor(ctx context.Context, tx pgx.Tx, id uuid.UUID) (TrustAnchor, error)
func ListNegativeTrustAnchors(ctx context.Context, q PolicyQuerier) ([]NegativeTrustAnchor, error)
func CreateNegativeTrustAnchor(ctx context.Context, tx pgx.Tx, n NegativeTrustAnchor) (NegativeTrustAnchor, error)
func DeleteNegativeTrustAnchor(ctx context.Context, tx pgx.Tx, id uuid.UUID) (NegativeTrustAnchor, error)
func DeleteExpiredNegativeTrustAnchors(ctx context.Context, tx pgx.Tx, now time.Time) (int64, error)
func ListRPZZones(ctx context.Context, q PolicyQuerier) ([]RPZZone, error)
func GetRPZZone(ctx context.Context, q PolicyQuerier, id uuid.UUID) (RPZZone, error)
func CreateRPZZone(ctx context.Context, tx pgx.Tx, z RPZZone) (RPZZone, error) // uses z.ID when set; position = max + 1
func UpdateRPZZone(ctx context.Context, tx pgx.Tx, z RPZZone) (RPZZone, error)
func DeleteRPZZone(ctx context.Context, tx pgx.Tx, id uuid.UUID, revision int64) error
func SetRPZZoneFile(ctx context.Context, tx pgx.Tx, id uuid.UUID, revision int64, sha256 string, records int32) (RPZZone, error)
func BumpRPZRefreshNonce(ctx context.Context, tx pgx.Tx, id uuid.UUID) (RPZZone, error)
func ReorderRPZZones(ctx context.Context, tx pgx.Tx, ids []uuid.UUID) error
func ListRPZEngineStatus(ctx context.Context, q PolicyQuerier) (map[uuid.UUID][]RPZEngineStatus, error)
func ListEngineDnssecStatus(ctx context.Context, q PolicyQuerier) ([]EngineDnssecStatus, error)
func LoadResolution(ctx context.Context, q PolicyQuerier) (ResolutionRows, error)
func PutBlob(ctx context.Context, tx pgx.Tx, data []byte) (sha256 string, size int64, err error)
// mgmt/internal/snapshot
func ApplyResolution(s *controlv1.ConfigSnapshot, rows store.ResolutionRows, now time.Time)
func RunNTAExpiry(ctx context.Context, st *store.Store, cfg BuildConfig) // every 60 s until ctx ends
// mgmt/internal/control
type RPZTsig struct{ /* st *store.Store; box *secrets.Box */ }
func NewRPZTsig(st *store.Store, box *secrets.Box) *RPZTsig
func (r *RPZTsig) Load(ctx context.Context) (*controlv1.RpzTsigKeys, string, error) // digest "" when no zone has a key
// Hub gains the field `RPZTsig *RPZTsig` (nil: nothing is sent)
// mgmt/internal/api
// Deps gains `Secrets *secrets.Box` (nil behaves as unconfigured)
// mgmt/internal/stats
func RecordM3(ctx context.Context, st *store.Store, engineID string, s *controlv1.Stats) error
```

NXE1 envelope (identical to M4 Task 6; M3 only writes wrap `1`): `"NXE1"` (4) ‖ wrap (1) ‖ `kek_id = SHA-256(KEK)[0:8]` (8) ‖ `dek_nonce` (12) ‖ `wrapped_dek = AES-256-GCM(KEK, dek_nonce, DEK[32], aad "NXE1-dek")` (48) ‖ `data_nonce` (12) ‖ `AES-256-GCM(DEK, data_nonce, plaintext, aad = purpose)` (n+16).

OpenAPI operations (operationId → method path; `get*`/`list*` → viewer, all others → operator):

| operationId               | method + path                              | success | errors                                        |
| ------------------------- | ------------------------------------------ | ------- | --------------------------------------------- |
| getResolutionSettings     | GET /resolution                            | 200     |                                               |
| updateResolutionSettings  | PUT /resolution                            | 200     | 400, 409                                      |
| listForwardZones          | GET /forward-zones                         | 200     |                                               |
| createForwardZone         | POST /forward-zones                        | 201     | 400, 409                                      |
| updateForwardZone         | PUT /forward-zones/{id}                    | 200     | 400, 404, 409                                 |
| deleteForwardZone         | DELETE /forward-zones/{id}?revision=       | 204     | 404, 409                                      |
| getDnssecSettings         | GET /dnssec/settings                       | 200     |                                               |
| updateDnssecSettings      | PUT /dnssec/settings                       | 200     | 400, 409                                      |
| getDnssecStatus           | GET /dnssec/status                         | 200     |                                               |
| listTrustAnchors          | GET /dnssec/trust-anchors                  | 200     |                                               |
| createTrustAnchor         | POST /dnssec/trust-anchors                 | 201     | 400, 409                                      |
| deleteTrustAnchor         | DELETE /dnssec/trust-anchors/{id}          | 204     | 404, 409 `last_root_anchor`                   |
| listNegativeTrustAnchors  | GET /dnssec/negative-trust-anchors         | 200     |                                               |
| createNegativeTrustAnchor | POST /dnssec/negative-trust-anchors        | 201     | 400, 409                                      |
| deleteNegativeTrustAnchor | DELETE /dnssec/negative-trust-anchors/{id} | 204     | 404                                           |
| listRpzZones              | GET /rpz-zones                             | 200     |                                               |
| createRpzZone             | POST /rpz-zones                            | 201     | 400, 409, 503 `key_storage_unconfigured`      |
| getRpzZone                | GET /rpz-zones/{id}                        | 200     | 404                                           |
| updateRpzZone             | PUT /rpz-zones/{id}                        | 200     | 400, 404, 409, 503 `key_storage_unconfigured` |
| deleteRpzZone             | DELETE /rpz-zones/{id}?revision=           | 204     | 404, 409                                      |
| uploadRpzZoneFile         | PUT /rpz-zones/{id}/file                   | 200     | 400, 404, 409                                 |
| refreshRpzZone            | POST /rpz-zones/{id}/refresh               | 202     | 404, 409 (file zone)                          |
| reorderRpzZones           | PUT /rpz-zones/order                       | 204     | 400                                           |

Every error response is `$ref: "#/components/responses/Error"`; path parameters use `#/components/parameters/Id`, the delete revisions `#/components/parameters/Revision`; tag `resolution`, `dnssec` or `rpz`.

Schemas (literal, under `components.schemas`):

```yaml
ResolutionSettings:
  type: object
  required:
    [
      mode,
      qname_minimisation,
      aggressive_nsec,
      max_upstream_queries,
      max_delegation_depth,
      authority_port,
      root_hints,
      revision,
    ]
  properties:
    mode: { type: string, enum: [forward, recursive] }
    qname_minimisation: { type: boolean }
    aggressive_nsec: { type: boolean }
    max_upstream_queries: { type: integer, minimum: 1, maximum: 1000 }
    max_delegation_depth: { type: integer, minimum: 1, maximum: 64 }
    authority_port: { type: integer, minimum: 1, maximum: 65535 }
    root_hints:
      { type: array, items: { $ref: "#/components/schemas/RootHint" } }
    revision: { type: integer, format: int64 }
RootHint:
  type: object
  required: [name, addresses]
  properties:
    name: { type: string }
    addresses: { type: array, minItems: 1, items: { type: string } }
ForwardZoneInput:
  type: object
  required: [domain, addresses, validate]
  properties:
    domain: { type: string, maxLength: 255 }
    addresses:
      {
        type: array,
        minItems: 1,
        maxItems: 16,
        items: { type: string, description: "ip:port" },
      }
    validate: { type: boolean }
ForwardZoneUpdate:
  allOf:
    - { $ref: "#/components/schemas/ForwardZoneInput" }
    - type: object
      required: [revision]
      properties:
        revision: { type: integer, format: int64 }
ForwardZone:
  type: object
  required: [id, domain, addresses, validate, revision]
  properties:
    id: { type: string, format: uuid }
    domain: { type: string }
    addresses: { type: array, items: { type: string } }
    validate: { type: boolean }
    revision: { type: integer, format: int64 }
DnssecSettings:
  type: object
  required: [validation, rfc5011, revision]
  properties:
    validation: { type: boolean }
    rfc5011: { type: boolean }
    revision: { type: integer, format: int64 }
TrustAnchorInput:
  type: object
  required: [zone, ds]
  properties:
    zone: { type: string, maxLength: 255 }
    ds: { type: string, maxLength: 200 }
TrustAnchor:
  type: object
  required: [id, zone, ds, source, created_at]
  properties:
    id: { type: string, format: uuid }
    zone: { type: string }
    ds: { type: string }
    source: { type: string, enum: [iana, operator] }
    created_at: { type: string, format: date-time }
NegativeTrustAnchorInput:
  type: object
  required: [domain, expires_at]
  properties:
    domain: { type: string, maxLength: 255 }
    reason: { type: string, maxLength: 500, default: "" }
    expires_at:
      {
        type: string,
        format: date-time,
        description: "in the future and at most 30 days ahead",
      }
NegativeTrustAnchor:
  type: object
  required: [id, domain, reason, expires_at, created_by, created_at]
  properties:
    id: { type: string, format: uuid }
    domain: { type: string }
    reason: { type: string }
    expires_at: { type: string, format: date-time }
    created_by: { type: string }
    created_at: { type: string, format: date-time }
DnssecStatus:
  type: object
  required: [engines]
  properties:
    engines:
      type: array
      items:
        type: object
        required:
          [
            engine_id,
            engine_name,
            reported_at,
            secure,
            insecure,
            bogus,
            indeterminate,
            active_negative_trust_anchors,
            trust_anchors,
          ]
        properties:
          engine_id: { type: string, format: uuid }
          engine_name: { type: string }
          reported_at: { type: string, format: date-time }
          secure: { type: integer, format: int64 }
          insecure: { type: integer, format: int64 }
          bogus: { type: integer, format: int64 }
          indeterminate: { type: integer, format: int64 }
          active_negative_trust_anchors: { type: integer }
          trust_anchors:
            type: array
            items:
              type: object
              required:
                [
                  zone,
                  key_tag,
                  algorithm,
                  state,
                  last_refresh_success,
                  hold_down_until,
                  last_error,
                ]
              properties:
                zone: { type: string }
                key_tag: { type: integer }
                algorithm: { type: integer }
                state:
                  {
                    type: string,
                    enum: [configured, add_pend, valid, missing, revoked],
                  }
                last_refresh_success:
                  { type: [string, "null"], format: date-time }
                hold_down_until: { type: [string, "null"], format: date-time }
                last_error: { type: string }
RpzZoneInput:
  type: object
  required: [name, source_type, min_refresh_seconds, policy_override]
  properties:
    name: { type: string, maxLength: 255 }
    source_type: { type: string, enum: [file, transfer] }
    primary: { type: [string, "null"], description: "ip:port, transfer only" }
    tsig_key_name: { type: [string, "null"] }
    tsig_algorithm:
      { type: [string, "null"], enum: [hmac-sha256, hmac-sha512, null] }
    tsig_secret:
      {
        type: [string, "null"],
        writeOnly: true,
        description: "base64, 16-64 bytes; stored sealed under NEXORA_KEK_FILE; omit on update to keep the stored secret",
      }
    min_refresh_seconds: { type: integer, minimum: 1, maximum: 86400 }
    policy_override:
      {
        type: string,
        enum: [given, disabled, nxdomain, nodata, passthru, drop, tcp_only],
      }
RpzZoneUpdate:
  type: object
  required: [min_refresh_seconds, policy_override, revision]
  properties:
    primary: { type: [string, "null"] }
    tsig_key_name: { type: [string, "null"] }
    tsig_algorithm:
      { type: [string, "null"], enum: [hmac-sha256, hmac-sha512, null] }
    tsig_secret: { type: [string, "null"], writeOnly: true }
    min_refresh_seconds: { type: integer, minimum: 1, maximum: 86400 }
    policy_override:
      {
        type: string,
        enum: [given, disabled, nxdomain, nodata, passthru, drop, tcp_only],
      }
    revision: { type: integer, format: int64 }
RpzZone:
  type: object
  required:
    [
      id,
      name,
      position,
      source_type,
      primary,
      tsig_key_name,
      tsig_algorithm,
      tsig_secret_set,
      min_refresh_seconds,
      policy_override,
      file_records,
      revision,
      status,
    ]
  properties:
    id: { type: string, format: uuid }
    name: { type: string }
    position: { type: integer }
    source_type: { type: string, enum: [file, transfer] }
    primary: { type: [string, "null"] }
    tsig_key_name: { type: [string, "null"] }
    tsig_algorithm: { type: [string, "null"] }
    tsig_secret_set: { type: boolean }
    min_refresh_seconds: { type: integer }
    policy_override: { type: string }
    file_records: { type: [integer, "null"] }
    revision: { type: integer, format: int64 }
    status:
      type: array
      items:
        type: object
        required:
          [
            engine_id,
            engine_name,
            serial,
            records,
            skipped,
            hits,
            last_success,
            last_error,
            stale,
          ]
        properties:
          engine_id: { type: string, format: uuid }
          engine_name: { type: string }
          serial: { type: integer, format: int64 }
          records: { type: integer, format: int64 }
          skipped: { type: integer, format: int64 }
          hits: { type: integer, format: int64 }
          last_success: { type: [string, "null"], format: date-time }
          last_error: { type: string }
          stale: { type: boolean }
RpzZoneFile:
  type: object
  required: [content, revision]
  properties:
    content: { type: string, maxLength: 3145728 }
    revision: { type: integer, format: int64 }
RpzZoneOrder:
  type: object
  required: [ids]
  properties:
    ids: { type: array, items: { type: string, format: uuid } }
```

Migrations (literal):

```sql
-- 00300_resolution.sql
-- +goose Up
CREATE TABLE resolution_settings (
    singleton            boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    mode                 text NOT NULL DEFAULT 'forward' CHECK (mode IN ('forward', 'recursive')),
    qname_minimisation   boolean NOT NULL DEFAULT true,
    aggressive_nsec      boolean NOT NULL DEFAULT false,
    max_upstream_queries integer NOT NULL DEFAULT 100 CHECK (max_upstream_queries BETWEEN 1 AND 1000),
    max_delegation_depth integer NOT NULL DEFAULT 32 CHECK (max_delegation_depth BETWEEN 1 AND 64),
    authority_port       integer NOT NULL DEFAULT 53 CHECK (authority_port BETWEEN 1 AND 65535),
    root_hints           jsonb NOT NULL DEFAULT '[]'::jsonb,
    revision             bigint NOT NULL DEFAULT 1,
    updated_at           timestamptz NOT NULL DEFAULT now()
);
INSERT INTO resolution_settings DEFAULT VALUES;

CREATE TABLE forward_zones (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    domain     text NOT NULL UNIQUE,
    addresses  text[] NOT NULL CHECK (cardinality(addresses) BETWEEN 1 AND 16),
    validate   boolean NOT NULL DEFAULT false,
    revision   bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE forward_zones;
DROP TABLE resolution_settings;
```

```sql
-- 00301_dnssec.sql
-- +goose Up
CREATE TABLE dnssec_settings (
    singleton  boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    validation boolean NOT NULL DEFAULT true,
    rfc5011    boolean NOT NULL DEFAULT true,
    revision   bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO dnssec_settings DEFAULT VALUES;

CREATE TABLE trust_anchors (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    zone       text NOT NULL,
    ds         text NOT NULL,
    source     text NOT NULL DEFAULT 'operator' CHECK (source IN ('iana', 'operator')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (zone, ds)
);
INSERT INTO trust_anchors (zone, ds, source) VALUES
    ('.', '20326 8 2 E06D44B80B8F1D39A95C0B0D7C65D08458E880409BBC683457104237C7F8EC8D', 'iana'),
    ('.', '38696 8 2 683D2D0ACB8C9B712A1948B27F741219298D0A450D612C483AF444A4C0FB2B16', 'iana');

CREATE TABLE negative_trust_anchors (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    domain     text NOT NULL UNIQUE,
    reason     text NOT NULL DEFAULT '' CHECK (length(reason) <= 500),
    expires_at timestamptz NOT NULL,
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE engine_dnssec_status (
    engine_id   uuid PRIMARY KEY REFERENCES engines (id) ON DELETE CASCADE,
    stats       jsonb NOT NULL,
    reported_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE engine_dnssec_status;
DROP TABLE negative_trust_anchors;
DROP TABLE trust_anchors;
DROP TABLE dnssec_settings;
```

```sql
-- 00302_rpz.sql
-- +goose Up
CREATE TABLE rpz_zones (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                 text NOT NULL UNIQUE,
    position             integer NOT NULL,
    source_type          text NOT NULL CHECK (source_type IN ('file', 'transfer')),
    blob_sha256          text REFERENCES blobs (sha256),
    file_records         integer,
    primary_address      text,
    tsig_key_name        text,
    tsig_algorithm       text CHECK (tsig_algorithm IN ('hmac-sha256', 'hmac-sha512')),
    -- NXE1 envelope from internal/secrets; never plaintext.
    tsig_secret_envelope bytea CHECK (tsig_secret_envelope IS NULL OR substring(tsig_secret_envelope from 1 for 4) = 'NXE1'::bytea),
    min_refresh_seconds  integer NOT NULL DEFAULT 60 CHECK (min_refresh_seconds BETWEEN 1 AND 86400),
    policy_override      text NOT NULL DEFAULT 'given' CHECK (policy_override IN ('given', 'disabled', 'nxdomain', 'nodata', 'passthru', 'drop', 'tcp_only')),
    refresh_nonce        bigint NOT NULL DEFAULT 0,
    revision             bigint NOT NULL DEFAULT 1,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    CHECK ((source_type = 'transfer' AND primary_address IS NOT NULL AND blob_sha256 IS NULL)
        OR (source_type = 'file' AND primary_address IS NULL AND tsig_algorithm IS NULL)),
    CHECK ((tsig_algorithm IS NULL) = (tsig_key_name IS NULL)),
    CHECK (tsig_secret_envelope IS NULL OR tsig_algorithm IS NOT NULL)
);
CREATE UNIQUE INDEX rpz_zones_position ON rpz_zones (position);

CREATE TABLE engine_rpz_status (
    engine_id       uuid NOT NULL REFERENCES engines (id) ON DELETE CASCADE,
    rpz_zone_id     uuid NOT NULL REFERENCES rpz_zones (id) ON DELETE CASCADE,
    serial          bigint NOT NULL,
    records         bigint NOT NULL,
    skipped         bigint NOT NULL,
    hits            bigint NOT NULL,
    last_success_at timestamptz,
    last_error      text NOT NULL DEFAULT '',
    stale           boolean NOT NULL DEFAULT false,
    reported_at     timestamptz NOT NULL,
    PRIMARY KEY (engine_id, rpz_zone_id)
);

-- +goose Down
DROP TABLE engine_rpz_status;
DROP TABLE rpz_zones;
```

- [ ] Create `mgmt/internal/secrets/secrets_test.go`:

```go
package secrets_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/piwi3910/nexora/mgmt/internal/secrets"
)

func writeKEK(t *testing.T, n int) string {
	t.Helper()
	key := make([]byte, n)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnvelopeRoundTripBindsPurposeAndDetectsTamper(t *testing.T) {
	box, err := secrets.LoadKEKFile(writeKEK(t, 32))
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("super-secret-tsig-bytes")
	env, err := box.Seal("nexora/rpz-tsig/v1:z1", secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(env, secret) || string(env[:4]) != "NXE1" || env[4] != secrets.WrapFileKEK || len(env) != 85+len(secret)+16 {
		t.Fatalf("envelope layout wrong: %x", env[:13])
	}
	got, err := box.Unseal("nexora/rpz-tsig/v1:z1", env)
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("unseal: %q %v", got, err)
	}
	if _, err := box.Unseal("nexora/rpz-tsig/v1:z2", env); err == nil {
		t.Fatal("purpose is not bound to the ciphertext")
	}
	env[len(env)-1] ^= 1
	if _, err := box.Unseal("nexora/rpz-tsig/v1:z1", env); err == nil {
		t.Fatal("tampering not detected")
	}
}

func TestWrongKEKIsReported(t *testing.T) {
	a, _ := secrets.LoadKEKFile(writeKEK(t, 32))
	b, _ := secrets.LoadKEKFile(writeKEK(t, 32))
	env, err := a.Seal("p", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Unseal("p", env); !errors.Is(err, secrets.ErrKEKMismatch) {
		t.Fatalf("got %v, want ErrKEKMismatch", err)
	}
	env[4] = secrets.WrapPKCS11
	if _, err := a.Unseal("p", env); !errors.Is(err, secrets.ErrBackendUnavailable) {
		t.Fatalf("got %v, want ErrBackendUnavailable for an HSM-wrapped envelope", err)
	}
}

func TestUnconfiguredBoxRefusesSecrets(t *testing.T) {
	box, err := secrets.LoadKEKFile("")
	if err != nil {
		t.Fatal(err)
	}
	if box.Configured() {
		t.Fatal("empty path reports configured")
	}
	if _, err := box.Seal("p", []byte("x")); !errors.Is(err, secrets.ErrUnconfigured) {
		t.Fatalf("Seal: %v", err)
	}
	var none *secrets.Box
	if none.Configured() {
		t.Fatal("nil box reports configured")
	}
	if _, err := none.Seal("p", []byte("x")); !errors.Is(err, secrets.ErrUnconfigured) {
		t.Fatalf("nil Seal: %v", err)
	}
}

func TestKEKFileValidation(t *testing.T) {
	if _, err := secrets.LoadKEKFile(writeKEK(t, 16)); err == nil || !strings.Contains(err.Error(), "32 bytes") {
		t.Fatalf("16-byte KEK: %v", err)
	}
	if _, err := secrets.LoadKEKFile(filepath.Join(t.TempDir(), "missing")); err == nil || !strings.Contains(err.Error(), "NEXORA_KEK_FILE") {
		t.Fatalf("missing KEK file: %v", err)
	}
}
```

- [ ] Create `mgmt/internal/rpz/validate_test.go`:

```go
package rpz

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
)

const zone = `$TTL 60
@ SOA ns.rpz. hostmaster.rpz. 7 60 60 86400 60
@ NS ns.rpz.
bad.example CNAME .
32.66.2.0.192.rpz-ip CNAME .
local.example A 10.9.9.9
`

func TestValidateZoneCountsRecordsAndSerial(t *testing.T) {
	s, err := ValidateZone("rpz.file.test.", zone)
	if err != nil {
		t.Fatalf("ValidateZone: %v", err)
	}
	if s.Serial != 7 || s.Records != 3 {
		t.Fatalf("summary = %+v, want serial 7 records 3", s)
	}
}

func TestValidateZoneRejectsIncludeMissingSOASyntaxAndSize(t *testing.T) {
	for name, text := range map[string]string{
		"include": "$INCLUDE /etc/passwd\n" + zone,
		"no soa":  "$TTL 60\nbad.example CNAME .\n",
		"syntax":  zone + "broken.example IN A not-an-ip\n",
		"size":    zone + strings.Repeat("; padding\n", MaxZoneBytes/10+1),
	} {
		if _, err := ValidateZone("rpz.file.test.", text); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	_, err := ValidateZone("rpz.file.test.", "$INCLUDE x\n")
	if err == nil || !strings.Contains(err.Error(), "$INCLUDE is not allowed") {
		t.Fatalf("include error = %v", err)
	}
}

func TestPackIsZstdOfContentAndPurposeNamesZone(t *testing.T) {
	d, _ := zstd.NewReader(nil)
	out, err := d.DecodeAll(Pack(zone), nil)
	if err != nil || !bytes.Equal(out, []byte(zone)) {
		t.Fatalf("round trip failed: %v", err)
	}
	if !bytes.Equal(Pack(zone), Pack(zone)) {
		t.Fatal("Pack is not deterministic")
	}
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	if TsigPurpose(id) != "nexora/rpz-tsig/v1:11111111-1111-1111-1111-111111111111" {
		t.Fatalf("purpose = %q", TsigPurpose(id))
	}
}
```

- [ ] Create `mgmt/internal/dnssecconf/ds_test.go`:

```go
package dnssecconf

import "testing"

func TestValidateDS(t *testing.T) {
	for _, ok := range IANARootAnchors {
		if err := ValidateDS(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "20326 8 2", "20326 8 2 ZZ", "70000 8 2 E06D", "20326 8 2 E06D44"} {
		if err := ValidateDS(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestValidateDomainAndForwardAddresses(t *testing.T) {
	if d, err := ValidateDomain("Corp.Example"); err != nil || d != "corp.example." {
		t.Fatalf("ValidateDomain = %q, %v", d, err)
	}
	if _, err := ValidateDomain("bad..name"); err == nil {
		t.Fatal("expected error for empty label")
	}
	if err := ValidateForwardAddresses([]string{"10.0.0.1:53", "[fd00::1]:5353"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateForwardAddresses([]string{"10.0.0.1"}); err == nil {
		t.Fatal("expected error for missing port")
	}
	if err := ValidateRootHints([]RootHint{{Name: "a.root.test.", Addresses: []string{"127.0.53.1:53"}}}); err == nil {
		t.Fatal("expected error for root hint with port")
	}
}
```

- [ ] Create `mgmt/internal/snapshot/resolution_test.go`:

```go
package snapshot_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func TestApplyResolution(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	sha := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	primary := "127.0.0.1:5300"
	alg := "hmac-sha256"
	keyName := "rpz-key."
	envelope := []byte("NXE1-sealed-secret-bytes")
	rows := store.ResolutionRows{
		Resolution:   store.ResolutionSettings{Mode: "recursive", QnameMinimisation: true, MaxUpstreamQueries: 100, MaxDelegationDepth: 32, AuthorityPort: 5353, RootHints: []store.RootHint{{Name: "a.root.test.", Addresses: []string{"127.0.53.1"}}}},
		ForwardZones: []store.ForwardZone{{Domain: "corp.example.", Addresses: []string{"10.0.0.1:53"}, Validate: true}},
		Dnssec:       store.DnssecSettings{Validation: true, RFC5011: true},
		Anchors:      []store.TrustAnchor{{Zone: ".", DS: "20326 8 2 E06D44B80B8F1D39A95C0B0D7C65D08458E880409BBC683457104237C7F8EC8D"}},
		NTAs: []store.NegativeTrustAnchor{
			{Domain: "broken.example.", ExpiresAt: now.Add(time.Hour)},
			{Domain: "expired.example.", ExpiresAt: now.Add(-time.Second)},
		},
		RPZ: []store.RPZZone{
			{ID: uuid.MustParse("22222222-2222-2222-2222-222222222222"), Name: "rpz.axfr.test.", Position: 2, SourceType: "transfer", PrimaryAddress: &primary, TSIGAlgorithm: &alg, TSIGKeyName: &keyName, TSIGSecretEnvelope: envelope, MinRefreshSeconds: 5, PolicyOverride: "given"},
			{ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), Name: "rpz.file.test.", Position: 1, SourceType: "file", BlobSHA256: &sha, BlobSize: 42, MinRefreshSeconds: 60, PolicyOverride: "nxdomain", RefreshNonce: 3},
			{ID: uuid.MustParse("33333333-3333-3333-3333-333333333333"), Name: "rpz.empty.test.", Position: 3, SourceType: "file", MinRefreshSeconds: 60, PolicyOverride: "given"},
		},
	}
	s := &controlv1.ConfigSnapshot{}
	snapshot.ApplyResolution(s, rows, now)
	if s.ResolutionMode != controlv1.ResolutionMode_RESOLUTION_MODE_RECURSIVE || s.Recursion.AuthorityPort != 5353 || len(s.Recursion.RootHints) != 1 {
		t.Fatalf("recursion = %v %v", s.ResolutionMode, s.Recursion)
	}
	if len(s.ForwardZones) != 1 || !s.ForwardZones[0].Validate {
		t.Fatalf("forward zones = %v", s.ForwardZones)
	}
	if len(s.Dnssec.NegativeTrustAnchors) != 1 || s.Dnssec.NegativeTrustAnchors[0].ExpiresUnix != now.Add(time.Hour).Unix() {
		t.Fatalf("expired NTAs must be omitted: %v", s.Dnssec.NegativeTrustAnchors)
	}
	if len(s.RpzZones) != 2 {
		t.Fatalf("a file zone without an uploaded file must be omitted: %v", s.RpzZones)
	}
	blob := s.RpzZones[0].GetFile().GetBlob()
	if s.RpzZones[0].Name != "rpz.file.test." || blob.GetSha256() != sha || blob.GetSize() != 42 || s.RpzZones[0].PolicyOverride != controlv1.RpzPolicyOverride_RPZ_POLICY_OVERRIDE_NXDOMAIN || s.RpzZones[0].RefreshNonce != 3 {
		t.Fatalf("rpz[0] = %v", s.RpzZones[0])
	}
	tr := s.RpzZones[1].GetTransfer()
	if tr.GetPrimary() != primary || tr.GetTsigAlgorithm() != controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA256 || tr.GetTsigKeyName() != keyName {
		t.Fatalf("rpz[1] = %v", s.RpzZones[1])
	}
	raw, err := proto.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, envelope) {
		t.Fatal("the sealed TSIG secret reached the snapshot")
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/secrets/ ./mgmt/internal/rpz/ ./mgmt/internal/dnssecconf/ ./mgmt/internal/snapshot/` — expect FAIL with "undefined: secrets.LoadKEKFile" (and `undefined: ValidateZone`, `undefined: snapshot.ApplyResolution`).
- [ ] Implement `mgmt/internal/secrets/secrets.go`. `LoadKEKFile("")` returns `&Box{}`; otherwise read the file (`NEXORA_KEK_FILE: <os error>` on failure), `strings.TrimSpace`, `base64.StdEncoding.DecodeString`, exactly 32 bytes else `NEXORA_KEK_FILE must contain 32 bytes, base64-encoded (openssl rand -base64 32)`; `kekID = sha256(key)[:8]`. Sealing (identical layout to M4 Task 6's `sealWith`):

```go
func (b *Box) Seal(purpose string, plaintext []byte) ([]byte, error) {
	if !b.Configured() {
		return nil, ErrUnconfigured
	}
	dek := make([]byte, 32)
	defer clear(dek)
	dekNonce, dataNonce := make([]byte, 12), make([]byte, 12)
	for _, buf := range [][]byte{dek, dekNonce, dataNonce} {
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
	}
	kekGCM, err := gcm(b.kek)
	if err != nil {
		return nil, err
	}
	wrapped := kekGCM.Seal(nil, dekNonce, dek, []byte("NXE1-dek"))
	dataGCM, err := gcm(dek)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 85+len(plaintext)+16)
	out = append(out, "NXE1"...)
	out = append(out, WrapFileKEK)
	out = append(out, b.kekID...)
	out = append(out, dekNonce...)
	out = append(out, wrapped...)
	out = append(out, dataNonce...)
	return dataGCM.Seal(out, dataNonce, plaintext, []byte(purpose)), nil
}

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
```

- [ ] Implement `Unseal`: `!Configured()` → `ErrUnconfigured`; length < 101 or magic ≠ `NXE1` → `errors.New("not an NXE1 envelope")`; byte 4 ≠ `WrapFileKEK` → `ErrBackendUnavailable`; bytes 5..13 ≠ `kekID` → `ErrKEKMismatch`; open `wrapped` (bytes 25..73) with the KEK, nonce bytes 13..25 and aad `NXE1-dek`; open bytes 85.. with the DEK, nonce bytes 73..85 and aad = purpose; `clear` the DEK; any GCM failure → `errors.New("envelope authentication failed")`.
- [ ] Implement `mgmt/internal/rpz/validate.go`: reject any line whose trimmed prefix is `$INCLUDE` (case-insensitive) with `errors.New("$INCLUDE is not allowed in RPZ zones")`; content longer than `MaxZoneBytes` → `fmt.Errorf("zone file exceeds %d bytes", MaxZoneBytes)`; parse with `dns.NewZoneParser(strings.NewReader(content), dns.Fqdn(origin), "")` and `zp.SetIncludeAllowed(false)`; iterate `zp.Next()`; the first parse error → `fmt.Errorf("line %d: %v", ...)`; the apex SOA is required (`"zone has no SOA at the apex"`) and supplies `Serial`; `Records` counts non-apex records. `Pack` uses a package-level `zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))` and `EncodeAll`. `TsigPurpose(id)` = `"nexora/rpz-tsig/v1:" + id.String()`.
- [ ] Implement `dnssecconf`: `ValidateDS` = four fields, key tag 0..65535, algorithm and digest type 0..255, hex digest with length 40/64/96 hex characters for types 1/2/4 (`digest is not hex`, `digest length N does not match digest type T`); `ValidateDomain` uses `dns.IsDomainName` and returns lowercase FQDN; `ValidateForwardAddresses` uses `net.SplitHostPort` + `netip.ParseAddr` (`addresses[i]: not ip:port: V`); `ValidateRootHints` requires FQDN names and bare IP addresses (`root_hints[i].addresses[j]: not an IP address: V`).
- [ ] Write the three migrations literally as above.
- [ ] Implement `store/resolution.go` and `store/blobs.go`: row structs and functions from Interfaces; revision-checked updates and deletes follow M2's `missingOrStale` (zero rows → `ErrNotFound` or `ErrConflict`); `LoadResolution` selects everything (`forward_zones ORDER BY domain`, `trust_anchors ORDER BY zone, ds`, `negative_trust_anchors ORDER BY domain`, `rpz_zones` left-joined to `blobs` for `BlobSize`, `ORDER BY position`); `DeleteTrustAnchor` returns `ErrLastRootAnchor` when the row is the only one with `zone = '.'` (checked under `SELECT ... FOR UPDATE`); `ReorderRPZZones` requires `ids` to be exactly the set of zone IDs (else `fmt.Errorf("%w: ids must list every RPZ zone exactly once", ErrConflict)`) and rewrites positions `1..n` after negating them (unique index); `PutBlob` computes SHA-256 hex and runs `insert into blobs(sha256, size, data) values ($1, $2, $3) on conflict do nothing`.
- [ ] Implement `snapshot/resolution.go` `ApplyResolution`: mode `recursive` → `RESOLUTION_MODE_RECURSIVE`, otherwise `RESOLUTION_MODE_FORWARD`; NTAs with `ExpiresAt <= now` omitted; RPZ zones sorted by `Position`; file zones without `BlobSHA256` omitted, otherwise `RpzFileSource{Blob: &BlobRef{Sha256, Size, Name: zone name}}`; policy override and TSIG algorithm strings map to the proto enums; `TSIGSecretEnvelope` is never read. In `snapshot.Build`, after `buildPolicy`, call `rows, err := store.LoadResolution(ctx, tx)` (wrap errors as `resolution: %w`) and `ApplyResolution(snap, rows, time.Now())`.
- [ ] Implement `snapshot/ntaexpiry.go` `RunNTAExpiry`: every 60 s, `Mutate(ctx, st, cfg, auth.Actor{Type: "system", ID: "nta-expiry", Name: "system"}, fn)` where `fn` takes `pg_try_advisory_xact_lock(hashtext('nexora:nta_expiry'))` (not acquired → `errNothingToExpire`), calls `store.DeleteExpiredNegativeTrustAnchors(ctx, tx, time.Now())` (0 rows → `errNothingToExpire`, which rolls back without publishing) and returns `auth.Change{Action: "expireNegativeTrustAnchors", TargetType: "negative_trust_anchor", TargetID: "expired", After: map[string]int64{"deleted": n}}`; `errNothingToExpire` is not logged, other errors are logged with `slog.Warn`.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/secrets/ ./mgmt/internal/rpz/ ./mgmt/internal/dnssecconf/ ./mgmt/internal/snapshot/` — expect PASS.
- [ ] Create `mgmt/internal/control/rpztsig_test.go`:

```go
package control_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/rpz"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

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
	box, err := secrets.LoadKEKFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestRPZTsigLoadUnsealsKeysAndDigestTracksChanges(t *testing.T) {
	ctx := context.Background()
	pg := harness.New(t).StartPostgres()
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	box := kekBox(t)
	src := control.NewRPZTsig(st, box)
	keys, digest, err := src.Load(ctx)
	if err != nil || len(keys.Keys) != 0 || digest != "" {
		t.Fatalf("empty load: %v %q %v", keys, digest, err)
	}

	secret := []byte("0123456789abcdef0123456789abcdef")
	id := uuid.New()
	primary, name, alg := "127.0.0.1:5300", "rpz-key.", "hmac-sha256"
	err = st.InTx(ctx, func(tx pgx.Tx) error {
		env, err := box.Seal(rpz.TsigPurpose(id), secret)
		if err != nil {
			return err
		}
		_, err = store.CreateRPZZone(ctx, tx, store.RPZZone{ID: id, Name: "rpz.axfr.test.", SourceType: "transfer", PrimaryAddress: &primary,
			TSIGKeyName: &name, TSIGAlgorithm: &alg, TSIGSecretEnvelope: env, MinRefreshSeconds: 60, PolicyOverride: "given"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	keys, digest, err = src.Load(ctx)
	if err != nil || len(keys.Keys) != 1 || digest == "" {
		t.Fatalf("load: %v %q %v", keys, digest, err)
	}
	k := keys.Keys[0]
	if k.ZoneId != id.String() || k.KeyName != name || k.Algorithm != controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA256 || !bytes.Equal(k.Secret, secret) {
		t.Fatalf("key = %v", k)
	}
	var stored []byte
	if err := st.Pool.QueryRow(ctx, "select tsig_secret_envelope from rpz_zones where id = $1", id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, secret) {
		t.Fatal("plaintext TSIG secret stored in rpz_zones")
	}
	if _, _, err := control.NewRPZTsig(st, kekBox(t)).Load(ctx); !errors.Is(err, secrets.ErrKEKMismatch) {
		t.Fatalf("load under another KEK: %v, want ErrKEKMismatch", err)
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/control/ -run TestRPZTsig` — expect FAIL with "undefined: control.NewRPZTsig".
- [ ] Implement `control/rpztsig.go`: `Load` selects `id, tsig_key_name, tsig_algorithm, tsig_secret_envelope from rpz_zones where tsig_secret_envelope is not null order by id`, unseals each with `rpz.TsigPurpose(id)` (errors returned wrapped as `rpz zone <id>: %w`), builds `RpzTsigKeys`, and returns `digest = hex(sha256(proto.MarshalOptions{Deterministic: true}.Marshal(keys)))` when `len(keys.Keys) > 0`, else `""`. Wire delivery in `hub.go` and `server.go`: `subscriber` gains `keys chan *controlv1.RpzTsigKeys` (capacity 1) and `keysDigest string`, with `offerKeys(k *controlv1.RpzTsigKeys, digest string)` that returns when `digest == keysDigest`, else records the digest, drains an undelivered value and sends; `Hub` gains the field `RPZTsig *RPZTsig`; `broadcast` — after offering the snapshot — loads the keys once (`slog.Warn("load rpz tsig keys", "err", err)` on error, never the keys) and offers them to every subscriber; `Server.Connect` offers the loaded keys to the new subscriber before offering the latest snapshot, and its send loop gains `case k := <-sub.keys:` sending `&controlv1.ServerMessage{Msg: &controlv1.ServerMessage_RpzTsigKeys{RpzTsigKeys: k}}`.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/control/` — expect PASS (M1/M2 control tests included).
- [ ] Create `mgmt/internal/api/resolution_test.go`:

```go
package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestResolutionDnssecAndRPZAPI(t *testing.T) {
	op, viewer := roleClients(t)
	var e apiErr

	var res map[string]any
	if code := op.do(http.MethodGet, "/resolution", nil, &res); code != 200 || res["mode"] != "forward" || res["authority_port"].(float64) != 53 {
		t.Fatalf("get resolution = %d %v", code, res)
	}
	res["mode"] = "recursive"
	res["root_hints"] = []map[string]any{{"name": "a.root.test.", "addresses": []string{"127.0.53.1:53"}}}
	if code := op.do(http.MethodPut, "/resolution", res, &e); code != 400 || e.Code != "invalid_request" || !strings.Contains(e.Message, "not an IP address") {
		t.Fatalf("root hint with port = %d %+v", code, e)
	}
	res["root_hints"] = []map[string]any{{"name": "a.root.test.", "addresses": []string{"127.0.53.1"}}}
	if code := op.do(http.MethodPut, "/resolution", res, &res); code != 200 || res["revision"].(float64) != 2 {
		t.Fatalf("update resolution = %d %v", code, res)
	}
	if code := viewer.do(http.MethodPut, "/resolution", res, &e); code != 403 {
		t.Fatalf("viewer update = %d", code)
	}

	var anchors []map[string]any
	if code := viewer.do(http.MethodGet, "/dnssec/trust-anchors", nil, &anchors); code != 200 || len(anchors) != 2 || anchors[0]["source"] != "iana" {
		t.Fatalf("seeded anchors = %d %v", code, anchors)
	}
	if code := op.do(http.MethodDelete, "/dnssec/trust-anchors/"+anchors[0]["id"].(string), nil, nil); code != 204 {
		t.Fatalf("delete first root anchor = %d", code)
	}
	if code := op.do(http.MethodDelete, "/dnssec/trust-anchors/"+anchors[1]["id"].(string), nil, &e); code != 409 || e.Code != "last_root_anchor" {
		t.Fatalf("delete last root anchor = %d %+v", code, e)
	}

	far := time.Now().Add(31 * 24 * time.Hour).UTC().Format(time.RFC3339)
	if code := op.do(http.MethodPost, "/dnssec/negative-trust-anchors", map[string]any{"domain": "broken.example", "reason": "x", "expires_at": far}, &e); code != 400 {
		t.Fatalf("NTA 31 days ahead = %d %+v", code, e)
	}
	soon := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	var nta map[string]any
	if code := op.do(http.MethodPost, "/dnssec/negative-trust-anchors", map[string]any{"domain": "Broken.Example", "reason": "x", "expires_at": soon}, &nta); code != 201 || nta["domain"] != "broken.example." || nta["created_by"] != "opal" {
		t.Fatalf("NTA = %d %v", code, nta)
	}

	var zone map[string]any
	if code := op.do(http.MethodPost, "/rpz-zones", map[string]any{"name": "rpz.file.test.", "source_type": "file", "policy_override": "given", "min_refresh_seconds": 60}, &zone); code != 201 {
		t.Fatalf("create file zone = %d %v", code, zone)
	}
	body := map[string]any{"content": "$INCLUDE /etc/passwd\n", "revision": zone["revision"]}
	if code := op.do(http.MethodPut, "/rpz-zones/"+zone["id"].(string)+"/file", body, &e); code != 400 || !strings.Contains(e.Message, "$INCLUDE is not allowed") {
		t.Fatalf("upload $INCLUDE = %d %+v", code, e)
	}
	transfer := map[string]any{"name": "rpz.axfr.test.", "source_type": "transfer", "primary": "127.0.0.1:5300", "tsig_key_name": "rpz-key.",
		"tsig_algorithm": "hmac-sha256", "tsig_secret": "MDEyMzQ1Njc4OWFiY2RlZg==", "policy_override": "given", "min_refresh_seconds": 60}
	if code := op.do(http.MethodPost, "/rpz-zones", transfer, &e); code != 503 || e.Code != "key_storage_unconfigured" {
		t.Fatalf("TSIG secret without NEXORA_KEK_FILE = %d %+v", code, e)
	}
	var zones []map[string]any
	if code := viewer.do(http.MethodGet, "/rpz-zones", nil, &zones); code != 200 || len(zones) != 1 {
		t.Fatalf("a refused create must not leave a row: %d %v", code, zones)
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/api/ -run TestResolutionDnssecAndRPZAPI` — expect FAIL with a 404 `not_found` for `GET /resolution` ("get resolution = 404").
- [ ] Add the OpenAPI paths and schemas above to `mgmt/api/openapi.yaml`; regenerate the server in the dev pod and copy it back (`scripts/dev-exec.sh 'cd mgmt/api && oapi-codegen -config oapi-codegen.yaml openapi.yaml'`, then `kubectl --context kw -n nexora-dev exec -i deploy/toolbox -c toolbox -- tar -C /work/nexora -cf - mgmt/internal/api/gen.go | tar -xf -`) and the GUI types on the laptop (`pnpm --dir web run gen:api`) — expect `go build ./mgmt/...` to FAIL with "does not implement StrictServerInterface (missing method CreateForwardZone)".
- [ ] Implement the handlers in `api/resolution.go`, `api/dnssec.go`, `api/rpz.go` with M2's conventions: reads use `h.d.Store.Pool`; every mutation goes through `h.mutate(ctx, func(tx pgx.Tx) (auth.Change, error))` returning `auth.Change{Action: "<operationId>", TargetType: "resolution" | "forward_zone" | "dnssec" | "trust_anchor" | "negative_trust_anchor" | "rpz_zone", TargetID, Before, After}` where `Before`/`After` are the API output types (which carry `tsig_secret_set`, never the secret); validation failures return `invalid(...)`. Rules:
  - `updateResolutionSettings`: `dnssecconf.ValidateRootHints`, each root-hint name through `ValidateDomain`, limits as in the schema.
  - `createForwardZone`/`updateForwardZone`: `ValidateDomain`, `ValidateForwardAddresses`; a duplicate domain is 409 `conflict`.
  - `createTrustAnchor`: `ValidateDomain(zone)`, `ValidateDS(ds)` (stored uppercase), `source = operator`; `deleteTrustAnchor` maps `store.ErrLastRootAnchor`.
  - `createNegativeTrustAnchor`: `ValidateDomain`; `expires_at` must be after now and at most 30 days ahead (`expires_at must be in the future and at most 30 days ahead`); `created_by = PrincipalFrom(ctx).Actor().Name`.
  - `createRpzZone`/`updateRpzZone`: `ValidateDomain(name)`; transfer zones need `primary` as `ip:port`; `tsig_algorithm` requires `tsig_key_name` (`ValidateDomain`) and, on create, `tsig_secret`; a provided `tsig_secret` must decode from base64 to 16..=64 bytes and is sealed with `h.d.Secrets.Seal(rpz.TsigPurpose(id), secret)` (the create handler assigns `id := uuid.New()` first) before the transaction starts, so an unconfigured box fails with `secrets.ErrUnconfigured` and nothing is written; clearing `tsig_algorithm` on update clears the key name and envelope; `source_type` never changes on update; file zones reject transfer fields.
  - `uploadRpzZoneFile`: file zones only (409 `conflict` for transfer zones); `rpz.ValidateZone(zone.Name, body.Content)`; `sha, _, err := store.PutBlob(ctx, tx, rpz.Pack(body.Content))`; `store.SetRPZZoneFile(ctx, tx, id, body.Revision, sha, int32(summary.Records))`.
  - `refreshRpzZone`: transfer zones only; `store.BumpRPZRefreshNonce`; 202 with an empty body.
  - `reorderRpzZones`: `store.ReorderRPZZones`; 204.
  - `getRpzZone`/`listRpzZones` join `store.ListRPZEngineStatus` into `status` and set `tsig_secret_set = TSIGSecretEnvelope != nil`; `getDnssecStatus` decodes each `engine_dnssec_status.stats` with `protojson.Unmarshal` into `controlv1.DnssecStats` and maps states to the lowercase enum (`0` unix times → `null`).
- [ ] In `api/server.go`: `Deps` gains `Secrets *secrets.Box`; `mapError` gains, before the default case, `errors.Is(err, secrets.ErrUnconfigured)` → `writeError(w, http.StatusServiceUnavailable, "key_storage_unconfigured", "Key storage is not configured on the management plane (NEXORA_KEK_FILE)")` and `errors.Is(err, store.ErrLastRootAnchor)` → `writeError(w, http.StatusConflict, "last_root_anchor", store.ErrLastRootAnchor.Error())`.
- [ ] Add every operationId to `mgmt/internal/auth/permissions.go` and `web/src/auth/permissions.ts`: `getResolutionSettings`, `listForwardZones`, `getDnssecSettings`, `getDnssecStatus`, `listTrustAnchors`, `listNegativeTrustAnchors`, `listRpzZones`, `getRpzZone` → viewer; the other fifteen → operator.
- [ ] Implement `stats/resolution.go` `RecordM3`: when `s.Dnssec != nil`, `insert into engine_dnssec_status(engine_id, stats, reported_at) values ($1, $2, now()) on conflict (engine_id) do update set stats = excluded.stats, reported_at = excluded.reported_at` with `protojson.Marshal(s.Dnssec)`; for each `s.RpzZones` whose `id` parses as a UUID, upsert `engine_rpz_status` with `insert ... select ... where exists (select 1 from rpz_zones where id = $2)` so deleted zones are skipped (`last_success_at` null for 0).
- [ ] Create `mgmt/internal/stats/resolution_test.go`:

```go
package stats_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/e2e/harness"
	"github.com/piwi3910/nexora/mgmt/internal/stats"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func TestRecordM3UpsertsStatusAndSkipsUnknownZones(t *testing.T) {
	ctx := context.Background()
	pg := harness.New(t).StartPostgres()
	st, err := store.Open(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var engineID string
	if err := st.Pool.QueryRow(ctx, "insert into engines(node_name, certificate_serial) values ('e1', '01') returning id::text").Scan(&engineID); err != nil {
		t.Fatal(err)
	}
	var zone store.RPZZone
	if err := st.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		zone, err = store.CreateRPZZone(ctx, tx, store.RPZZone{Name: "rpz.file.test.", SourceType: "file", MinRefreshSeconds: 60, PolicyOverride: "given"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	s := &controlv1.Stats{
		Dnssec:   &controlv1.DnssecStats{Secure: 4, TrustAnchors: []*controlv1.TrustAnchorStatus{{Zone: ".", KeyTag: 20326, Algorithm: 8, State: controlv1.TrustAnchorState_TRUST_ANCHOR_STATE_VALID}}},
		RpzZones: []*controlv1.RpzZoneStatus{{Id: zone.ID.String(), Serial: 7, Records: 3}, {Id: uuid.NewString(), Serial: 9}, {Id: "not-a-uuid"}},
	}
	for i := 0; i < 2; i++ {
		if err := stats.RecordM3(ctx, st, engineID, s); err != nil {
			t.Fatal(err)
		}
	}
	dnssec, err := store.ListEngineDnssecStatus(ctx, st.Pool)
	if err != nil || len(dnssec) != 1 || dnssec[0].EngineName != "e1" {
		t.Fatalf("dnssec status = %v %v", dnssec, err)
	}
	byZone, err := store.ListRPZEngineStatus(ctx, st.Pool)
	if err != nil || len(byZone) != 1 || len(byZone[zone.ID]) != 1 || byZone[zone.ID][0].Serial != 7 {
		t.Fatalf("rpz status = %v %v", byZone, err)
	}
}
```

- [ ] Wire `mgmt/cmd/nexora-mgmt/main.go` and `config.go`: `Config.KEKFile = getenv("NEXORA_KEK_FILE")`; in `serve`, `box, err := secrets.LoadKEKFile(cfg.KEKFile)` (return the error) and `log.Printf("key storage: none configured; RPZ TSIG secrets are refused")` when `!box.Configured()`; `hub.RPZTsig = control.NewRPZTsig(st, box)` before `go hub.Run(ctx)`; `api.Deps{..., Secrets: box}`; `go snapshot.RunNTAExpiry(ctx, st, build)`; the `OnStats` closure also calls `_ = stats.RecordM3(ctx, st, engineID, s)`.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/api/ ./mgmt/internal/stats/ -run 'TestResolutionDnssecAndRPZAPI|TestRecordM3|TestPermissionsCoverEveryOperation'` — expect PASS.
- [ ] Run `scripts/dev-exec.sh make mgmt-test` — expect PASS (migrations apply on a fresh database in every store test); run `scripts/dev-exec.sh 'cd web && pnpm install --frozen-lockfile && pnpm run lint'` — expect PASS (permission parity).
- [ ] Commit: `git add mgmt web/src/api/schema.d.ts web/src/auth/permissions.ts && git commit -m "feat(mgmt): resolution, DNSSEC and RPZ API with sealed TSIG secrets"`.

## Task 12: GUI — `/rpz`, `/dnssec`, resolution settings and forward zones on `/upstreams`

Files:

- `web/src/api/resolution.ts` — TanStack Query hooks over the generated `openapi-fetch` client (`api`, `unwrap` from `@/api/client`).
- `web/src/pages/RpzPage.tsx` — ordered zone table (up/down buttons), per-engine status, create/edit dialog, file upload dialog, refresh, delete.
- `web/src/pages/DnssecPage.tsx` — validation settings, trust anchors, NTAs, per-engine validation stats and anchor states with rollover warning.
- `web/src/pages/ResolutionSection.tsx` — mode, QNAME minimisation, aggressive NSEC, limits, authority port, root-hint override.
- `web/src/pages/ForwardZonesSection.tsx` — forward zone CRUD.
- `web/src/pages/UpstreamsPage.tsx` — renders the two sections above the M1 upstream list.
- `web/src/app/router.tsx` — routes `rpz` and `dnssec` under the authenticated `AppShell`.
- `web/src/components/layout/AppShell.tsx` — `Resolver` nav items `{ route: "rpz", path: "/rpz", label: "RPZ", icon: ShieldBan, op: "listRpzZones" }` and `{ route: "dnssec", path: "/dnssec", label: "DNSSEC", icon: BadgeCheck, op: "getDnssecSettings" }` (lucide-react).
- `web/e2e/screens/15-rpz.spec.ts`, `web/e2e/screens/16-dnssec.spec.ts`, `web/e2e/screens/17-resolution.spec.ts` — Playwright tests run by `TestGUICoverage`.

Interfaces:

```ts
// web/src/api/resolution.ts (Schemas = components["schemas"] from @/api/client)
export function useResolutionSettings(): UseQueryResult<
  Schemas["ResolutionSettings"]
>;
export function useUpdateResolutionSettings(): UseMutationResult<
  Schemas["ResolutionSettings"],
  ApiError,
  Schemas["ResolutionSettings"]
>;
export function useForwardZones(): UseQueryResult<Schemas["ForwardZone"][]>;
export function useCreateForwardZone(): UseMutationResult<
  Schemas["ForwardZone"],
  ApiError,
  Schemas["ForwardZoneInput"]
>;
export function useUpdateForwardZone(): UseMutationResult<
  Schemas["ForwardZone"],
  ApiError,
  { id: string; body: Schemas["ForwardZoneUpdate"] }
>;
export function useDeleteForwardZone(): UseMutationResult<
  void,
  ApiError,
  { id: string; revision: number }
>;
export function useDnssecSettings(): UseQueryResult<Schemas["DnssecSettings"]>;
export function useUpdateDnssecSettings(): UseMutationResult<
  Schemas["DnssecSettings"],
  ApiError,
  Schemas["DnssecSettings"]
>;
export function useDnssecStatus(): UseQueryResult<Schemas["DnssecStatus"]>; // refetchInterval 10_000
export function useTrustAnchors(): UseQueryResult<Schemas["TrustAnchor"][]>;
export function useCreateTrustAnchor(): UseMutationResult<
  Schemas["TrustAnchor"],
  ApiError,
  Schemas["TrustAnchorInput"]
>;
export function useDeleteTrustAnchor(): UseMutationResult<
  void,
  ApiError,
  string
>;
export function useNegativeTrustAnchors(): UseQueryResult<
  Schemas["NegativeTrustAnchor"][]
>;
export function useCreateNegativeTrustAnchor(): UseMutationResult<
  Schemas["NegativeTrustAnchor"],
  ApiError,
  Schemas["NegativeTrustAnchorInput"]
>;
export function useDeleteNegativeTrustAnchor(): UseMutationResult<
  void,
  ApiError,
  string
>;
export function useRpzZones(): UseQueryResult<Schemas["RpzZone"][]>; // refetchInterval 10_000
export function useRpzZone(id: string): UseQueryResult<Schemas["RpzZone"]>; // enabled while the edit dialog is open
export function useCreateRpzZone(): UseMutationResult<
  Schemas["RpzZone"],
  ApiError,
  Schemas["RpzZoneInput"]
>;
export function useUpdateRpzZone(): UseMutationResult<
  Schemas["RpzZone"],
  ApiError,
  { id: string; body: Schemas["RpzZoneUpdate"] }
>;
export function useDeleteRpzZone(): UseMutationResult<
  void,
  ApiError,
  { id: string; revision: number }
>;
export function useUploadRpzZoneFile(): UseMutationResult<
  Schemas["RpzZone"],
  ApiError,
  { id: string; body: Schemas["RpzZoneFile"] }
>;
export function useRefreshRpzZone(): UseMutationResult<void, ApiError, string>;
export function useReorderRpzZones(): UseMutationResult<
  void,
  ApiError,
  string[]
>;
```

- [ ] Create `web/e2e/screens/15-rpz.spec.ts`:

```ts
import { test, expect, env, login } from "../fixtures";

const ZONE = `$TTL 60
@ SOA ns.rpz. hostmaster.rpz. 7 60 60 86400 60
@ NS ns.rpz.
bad.example CNAME .
local.example A 10.9.9.9
`;

const rowName = (name: string) => new RegExp(name.replaceAll(".", "\\."));

test("operator manages RPZ zones: file upload, transfer zone, order, refresh, delete", async ({
  page,
}) => {
  // TestGUICoverage counts the browser requests below: listRpzZones, createRpzZone, uploadRpzZoneFile,
  // getRpzZone (Edit), updateRpzZone, reorderRpzZones, refreshRpzZone, deleteRpzZone.
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.getByTestId("nav-rpz").click();
  await expect(
    page.getByRole("heading", { name: "Response policy zones" }),
  ).toBeVisible();
  const suffix = Date.now();
  const fileName = `rpz-file-${suffix}.test.`;
  const axfrName = `rpz-axfr-${suffix}.test.`;

  await page.getByRole("button", { name: "New zone" }).click();
  let dialog = page.getByRole("dialog", { name: "New RPZ zone" });
  await dialog.getByLabel("Zone name").fill(fileName);
  await dialog.getByLabel("Source").click();
  await page.getByRole("option", { name: "File", exact: true }).click();
  await dialog.getByRole("button", { name: "Save" }).click();
  const fileRow = page.getByRole("row", { name: rowName(fileName) });
  await expect(fileRow).toContainText("no file");

  await fileRow
    .getByRole("button", { name: `Upload file for ${fileName}` })
    .click();
  const upload = page.getByRole("dialog", { name: "Upload zone file" });
  await upload.getByLabel("Zone file").setInputFiles({
    name: "bad.zone",
    mimeType: "text/plain",
    buffer: Buffer.from("$INCLUDE /etc/passwd\n"),
  });
  await upload.getByRole("button", { name: "Upload" }).click();
  await expect(upload.getByRole("alert")).toContainText(
    "$INCLUDE is not allowed",
  );
  await upload.getByLabel("Zone file").setInputFiles({
    name: "rpz.zone",
    mimeType: "text/plain",
    buffer: Buffer.from(ZONE),
  });
  await upload.getByRole("button", { name: "Upload" }).click();
  await expect(fileRow).toContainText("2 records");

  await page.getByRole("button", { name: "New zone" }).click();
  dialog = page.getByRole("dialog", { name: "New RPZ zone" });
  await dialog.getByLabel("Zone name").fill(axfrName);
  await dialog.getByLabel("Source").click();
  await page
    .getByRole("option", { name: "Zone transfer", exact: true })
    .click();
  await dialog.getByLabel("Primary").fill("127.0.0.1:5300");
  await dialog.getByLabel("TSIG algorithm").click();
  await page.getByRole("option", { name: "hmac-sha256", exact: true }).click();
  await dialog.getByLabel("TSIG key name").fill("rpz-key.");
  await dialog
    .getByLabel("TSIG secret (base64)")
    .fill("MDEyMzQ1Njc4OWFiY2RlZg==");
  await dialog.getByRole("button", { name: "Save" }).click();
  // TestGUICoverage's management plane runs without NEXORA_KEK_FILE.
  await expect(dialog.getByRole("alert")).toContainText(
    "Key storage is not configured on the management plane (NEXORA_KEK_FILE)",
  );
  await dialog.getByLabel("TSIG algorithm").click();
  await page.getByRole("option", { name: "None", exact: true }).click();
  await dialog.getByLabel("Policy override").click();
  await page.getByRole("option", { name: "NXDOMAIN", exact: true }).click();
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog).toBeHidden();
  const axfrRow = page.getByRole("row", { name: rowName(axfrName) });
  await expect(axfrRow).toContainText("127.0.0.1:5300");

  const ours = page.getByRole("row", {
    name: new RegExp(`rpz-(file|axfr)-${suffix}`),
  });
  await expect(ours.first()).toContainText(fileName);
  await axfrRow.getByRole("button", { name: `Move ${axfrName} up` }).click();
  await expect(ours.first()).toContainText(axfrName);

  await axfrRow.getByRole("button", { name: `Edit ${axfrName}` }).click();
  const edit = page.getByRole("dialog", { name: "Edit RPZ zone" });
  await expect(edit.getByLabel("Primary")).toHaveValue("127.0.0.1:5300");
  await edit.getByLabel("Minimum refresh (seconds)").fill("120");
  await edit.getByRole("button", { name: "Save" }).click();
  await expect(edit).toBeHidden();
  await expect(axfrRow).toContainText("120 s");

  await axfrRow.getByRole("button", { name: `Refresh ${axfrName}` }).click();
  await expect(page.getByText("Refresh requested")).toBeVisible();

  await fileRow.getByRole("button", { name: `Delete ${fileName}` }).click();
  await page.getByTestId("confirm-delete").click();
  await expect(fileRow).toHaveCount(0);
});

test("viewer sees RPZ zones read-only", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_VIEWER_USER"),
    env("NEXORA_E2E_VIEWER_PASSWORD"),
  );
  await page.getByTestId("nav-rpz").click();
  await expect(
    page.getByRole("heading", { name: "Response policy zones" }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "New zone" })).toHaveCount(0);
});
```

- [ ] Create `web/e2e/screens/16-dnssec.spec.ts`:

```ts
import { test, expect, env, login } from "../fixtures";

test("operator edits DNSSEC settings, trust anchors and negative trust anchors", async ({
  page,
}) => {
  // covers getDnssecSettings, updateDnssecSettings, getDnssecStatus, listTrustAnchors, createTrustAnchor,
  // deleteTrustAnchor, listNegativeTrustAnchors, createNegativeTrustAnchor, deleteNegativeTrustAnchor
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.getByTestId("nav-dnssec").click();
  await expect(page.getByRole("heading", { name: "DNSSEC" })).toBeVisible();

  const settings = page.getByRole("region", { name: "Validation settings" });
  const rfc5011 = settings.getByRole("switch", {
    name: "Automated trust anchor updates (RFC 5011)",
  });
  await expect(rfc5011).toBeChecked();
  await rfc5011.click();
  await settings.getByRole("button", { name: "Save settings" }).click();
  await expect(settings.getByText("Settings saved")).toBeVisible();
  await page.reload();
  await expect(
    page
      .getByRole("region", { name: "Validation settings" })
      .getByRole("switch", {
        name: "Automated trust anchor updates (RFC 5011)",
      }),
  ).not.toBeChecked();

  const anchors = page.getByRole("region", { name: "Trust anchors" });
  await expect(anchors.getByRole("row", { name: /20326 8 2/ })).toContainText(
    "IANA",
  );
  await expect(anchors.getByRole("row", { name: /38696 8 2/ })).toContainText(
    "IANA",
  );
  await anchors.getByRole("button", { name: "Add trust anchor" }).click();
  let dialog = page.getByRole("dialog", { name: "Add trust anchor" });
  await dialog.getByLabel("Zone").fill("example.");
  await dialog.getByLabel("DS record").fill("1 13 2 ZZ");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog.getByRole("alert")).toContainText("digest");
  await dialog
    .getByLabel("DS record")
    .fill(
      "12345 13 2 0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
    );
  await dialog.getByRole("button", { name: "Save" }).click();
  const anchor = anchors.getByRole("row", { name: /12345 13 2/ });
  await expect(anchor).toContainText("Operator");
  await anchor
    .getByRole("button", { name: "Delete trust anchor 12345 for example." })
    .click();
  await page.getByTestId("confirm-delete").click();
  await expect(anchor).toHaveCount(0);

  const ntas = page.getByRole("region", { name: "Negative trust anchors" });
  await ntas.getByRole("button", { name: "Add negative trust anchor" }).click();
  dialog = page.getByRole("dialog", { name: "Add negative trust anchor" });
  await dialog.getByLabel("Domain").fill("broken.example");
  await dialog.getByLabel("Reason").fill("expired signatures at provider");
  await dialog.getByLabel("Expires in").click();
  await page.getByRole("option", { name: "1 day", exact: true }).click();
  await dialog.getByRole("button", { name: "Save" }).click();
  const nta = ntas.getByRole("row", { name: /broken\.example\./ });
  await expect(nta).toContainText("expired signatures at provider");
  await nta
    .getByRole("button", {
      name: "Delete negative trust anchor broken.example.",
    })
    .click();
  await page.getByTestId("confirm-delete").click();
  await expect(nta).toHaveCount(0);

  await expect(
    page.getByRole("region", { name: "Validation by engine" }),
  ).toContainText("gui-engine");
});
```

- [ ] Create `web/e2e/screens/17-resolution.spec.ts`:

```ts
import { test, expect, env, login } from "../fixtures";

test("operator edits resolution settings and forward zones", async ({
  page,
}) => {
  // covers getResolutionSettings, updateResolutionSettings, listForwardZones, createForwardZone,
  // updateForwardZone, deleteForwardZone
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.getByTestId("nav-upstreams").click();

  const card = page.getByRole("region", { name: "Resolution" });
  await expect(card.getByLabel("Mode")).toContainText("Forward");
  await card
    .getByLabel("Maximum upstream queries per client query")
    .fill("150");
  await card.getByRole("button", { name: "Add root hint" }).click();
  await card.getByLabel("Root hint name 1").fill("a.root.test.");
  await card.getByLabel("Root hint addresses 1").fill("127.0.53.1:53");
  await card.getByRole("button", { name: "Save resolution settings" }).click();
  await expect(card.getByRole("alert")).toContainText("not an IP address");
  await card.getByLabel("Root hint addresses 1").fill("127.0.53.1");
  await card.getByRole("button", { name: "Save resolution settings" }).click();
  await expect(card.getByText("Resolution settings saved")).toBeVisible();
  await page.reload();
  await expect(
    page
      .getByRole("region", { name: "Resolution" })
      .getByLabel("Maximum upstream queries per client query"),
  ).toHaveValue("150");

  const zones = page.getByRole("region", { name: "Forward zones" });
  const domain = `corp-${Date.now()}.example`;
  await zones.getByRole("button", { name: "New forward zone" }).click();
  let dialog = page.getByRole("dialog", { name: "New forward zone" });
  await dialog.getByLabel("Domain").fill(domain);
  await dialog.getByLabel("Servers").fill("10.0.0.1");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog.getByRole("alert")).toContainText("not ip:port");
  await dialog.getByLabel("Servers").fill("10.0.0.1:53, 10.0.0.2:53");
  await dialog.getByRole("button", { name: "Save" }).click();
  const row = zones.getByRole("row", {
    name: new RegExp(domain.replaceAll(".", "\\.")),
  });
  await expect(row).toContainText("10.0.0.2:53");

  await row.getByRole("button", { name: `Edit ${domain}.` }).click();
  dialog = page.getByRole("dialog", { name: "Edit forward zone" });
  await dialog.getByRole("switch", { name: "Validate DNSSEC" }).click();
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(row).toContainText("validated");

  await row.getByRole("button", { name: `Delete ${domain}.` }).click();
  await page.getByTestId("confirm-delete").click();
  await expect(row).toHaveCount(0);
});
```

- [ ] Run `scripts/dev-exec.sh bash -c 'make e2e-build && NEXORA_E2E_BIN_DIR=bin go test -count=1 ./e2e/ -run TestGUICoverage'` — expect FAIL in the Playwright run of `15-rpz.spec.ts` with `getByTestId('nav-rpz')` not found (the route and nav item do not exist).
- [ ] Implement `web/src/api/resolution.ts` hooks in the style of `web/src/api/policies.ts` (query keys `["resolution"]`, `["forward-zones"]`, `["dnssec", "settings"]`, `["dnssec", "status"]`, `["dnssec", "anchors"]`, `["dnssec", "ntas"]`, `["rpz-zones"]`, `["rpz-zones", id]`; every mutation invalidates its list key; errors surface through `ErrorAlert` from `@/components/common`, which renders `role="alert"` with the server `message`).
- [ ] Implement the pages with the exact accessible names used in the tests (mutating controls hidden unless `useCan` allows the operation, as the M2 pages do):
  - `RpzPage`: heading "Response policy zones"; button "New zone"; table rows ordered by `position` showing name, source ("File"/"Zone transfer"), `N records` or "no file" (file) or primary (transfer), override, "secret set" badge when `tsig_secret_set`, `min_refresh_seconds` as `N s`, and per-engine serial/last success/stale badge (red "stale" when any engine reports `stale`, amber when `last_error` is non-empty); row buttons `Move <name> up`, `Move <name> down` (call `reorderRpzZones` with the full new ID order), `Edit <name>` (opens "Edit RPZ zone" and fetches `GET /rpz-zones/{id}`), `Upload file for <name>` (file zones), `Refresh <name>` (transfer zones; a `role="status"` note "Refresh requested"), `Delete <name>` (`ConfirmDialog`).
  - RPZ zone dialog ("New RPZ zone" / "Edit RPZ zone"): labels "Zone name", "Source" (options "File", "Zone transfer"; create only), "Primary", "TSIG algorithm" (options "None", "hmac-sha256", "hmac-sha512"), "TSIG key name" and "TSIG secret (base64)" (shown when an algorithm is chosen; on edit the secret field is empty and its placeholder says "leave empty to keep the stored secret"), "Policy override" (options "Given", "Disabled", "NXDOMAIN", "NODATA", "PASSTHRU", "DROP", "TCP-only"), "Minimum refresh (seconds)"; the dialog stays open and shows the API error on failure. Upload dialog "Upload zone file": label "Zone file" (`<input type="file">` read with `File.text()`), button "Upload".
  - `DnssecPage`: heading "DNSSEC"; `<section aria-label="Validation settings">` with switches "Validate answers" and "Automated trust anchor updates (RFC 5011)", button "Save settings" and `SavedNote` "Settings saved"; `<section aria-label="Trust anchors">` table (zone, DS, source badge "IANA"/"Operator", `Delete trust anchor <key tag> for <zone>`), button "Add trust anchor" (dialog "Add trust anchor", labels "Zone", "DS record"); `<section aria-label="Negative trust anchors">` table (domain, reason, expiry, creator, `Delete negative trust anchor <domain>`), button "Add negative trust anchor" (dialog labels "Domain", "Reason", "Expires in" with options "1 hour", "1 day", "7 days", "30 days" converted to `expires_at`); `<section aria-label="Validation by engine">` listing each engine name with secure/insecure/bogus/indeterminate counts and trust anchor key states; a red banner "Trust anchor refresh failing" when any anchor for `.` has a `last_error` and `last_refresh_success` older than 72 h, or when no key for `.` is in state `valid`/`configured`.
  - `ResolutionSection` (`<section aria-label="Resolution">`): labels "Mode" (options "Forward", "Recursive"), "QNAME minimisation", "Aggressive NSEC caching", "Maximum upstream queries per client query", "Maximum delegation depth", "Authority port", root-hint rows ("Add root hint", "Root hint name N", "Root hint addresses N" comma-separated, "Remove root hint N"), button "Save resolution settings" and `SavedNote` "Resolution settings saved"; help text states that forward zones override the mode and that DNSSEC validation applies to recursion and validating forward zones.
  - `ForwardZonesSection` (`<section aria-label="Forward zones">`): "New forward zone", dialogs "New forward zone" / "Edit forward zone" with labels "Domain", "Servers" (comma-separated `ip:port`), switch "Validate DNSSEC"; row buttons `Edit <domain>`, `Delete <domain>`; row badge "validated" when `validate`.
  - `router.tsx` gains `{ path: "rpz", element: <RpzPage /> }` and `{ path: "dnssec", element: <DnssecPage /> }`; `AppShell.tsx` gains the two nav items listed under Files (after "Rewrites").
- [ ] Run `scripts/dev-exec.sh make web-test` — expect PASS (typecheck, lint incl. permission parity, build).
- [ ] Run `scripts/dev-exec.sh bash -c 'make e2e-build && NEXORA_E2E_BIN_DIR=bin go test -count=1 ./e2e/ -run TestGUICoverage'` — expect PASS (all 23 new operationIds covered; the three new specs green).
- [ ] Commit: `git add web && git commit -m "feat(web): RPZ, DNSSEC and resolution screens"`.

## Task 13: Private DNS hierarchy fixture and harness helpers (fake root/TLD/leaf, signed zones, spoofer, BIND primary)

Files:

- `e2e/fixtures/authhier/spec.go` — `Spec`, `ZoneSpec`, `DefaultSpec`.
- `e2e/fixtures/authhier/sign.go` — key generation, DS, RRSIG, NSEC and NSEC3 chains with miekg/dns.
- `e2e/fixtures/authhier/server.go` — shared-port binding, per-zone authoritative UDP/TCP servers, spoofing behaviour, validating-forwarder endpoint, stats HTTP.
- `e2e/fixtures/authhier/authhier_test.go` — in-process tests.
- `e2e/fixtures/cmd/nexora-fixture/authhier.go` — `runAuthhier`; `e2e/fixtures/cmd/nexora-fixture/main.go` — `case "authhier"` and usage line.
- `e2e/harness/hierarchy.go` — `StartHierarchy`, `ConfigureRecursion`, `Stats`.
- `e2e/harness/named.go` — `StartNamed` (BIND 9 primary with TSIG, IXFR from differences).
- `e2e/harness/named_test.go` — harness self-test for BIND.
- `e2e/harness/kek.go` — `WriteKEK` (the helper M4 Task 8 names; M4 extends this file).

Interfaces:

```go
// package authhier
type ZoneSpec struct {
	Origin          string   `json:"origin"`
	ServerIP        string   `json:"server_ip"`
	Signed          bool     `json:"signed"`
	NSEC3Iterations int      `json:"nsec3_iterations"` // -1: NSEC; >= 0: NSEC3 with this iteration count
	BreakSignatures bool     `json:"break_signatures"` // flips the last byte of every RRSIG over A/AAAA/TXT
	Spoof           bool     `json:"spoof"`
	Records         []string `json:"records"`          // presentation format, owner names absolute
}
type Spec struct {
	Port        int        `json:"port"` // 0: bind the first zone's address on a kernel-chosen port, then every other address on it
	ForwarderIP string     `json:"forwarder_ip"`
	Zones       []ZoneSpec `json:"zones"`
}
type Ready struct {
	Port      int        `json:"port"`
	RootDS    string     `json:"root_ds"`
	RootHints []RootHint `json:"root_hints"`
	Forwarder string     `json:"forwarder"`
	StatsURL  string     `json:"stats_url"`
}
type RootHint struct{ Name string `json:"name"`; Addresses []string `json:"addresses"` }
type Stats struct{ Queries map[string]int `json:"queries"`; SpoofsSent int `json:"spoofs_sent"` }
type Hierarchy struct{ /* servers, stats listener, counters */ }
func DefaultSpec(port int) Spec
func Start(ctx context.Context, spec Spec, now time.Time) (*Hierarchy, Ready, error)
func (h *Hierarchy) Close()
func (h *Hierarchy) Stats() Stats
// e2e/fixtures/cmd/nexora-fixture
func runAuthhier(args []string) (stop func(), addrs string, err error) // --ready-file F; addrs = "port=<n> stats=<host:port>"
// package harness
type Hierarchy struct{ Ready authhier.Ready }
func (e *Env) StartHierarchy() *Hierarchy
func (h *Hierarchy) Stats(t *testing.T) authhier.Stats
func (h *Hierarchy) ConfigureRecursion(t *testing.T, api *API, nodeName string) // PUT /resolution recursive + hints + port, POST root DS trust anchor, waits until nodeName applied the latest version
type Named struct{ Addr, KeyName, KeySecretB64 string; Proc *Proc; dir string }
func (e *Env) StartNamed(zoneName, zoneText string) *Named
func (n *Named) UpdateZone(t *testing.T, zoneText string) // rewrites the zone file and sends SIGHUP
func (n *Named) Stop()
func WriteKEK(t *testing.T) string // base64 of 32 random bytes in a 0600 temp file
```

`DefaultSpec` (all servers on `127.0.53.0/24`, one shared port):

| origin           | server     | signed              | notes                                                                                                                                                                                                                            |
| ---------------- | ---------- | ------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `.`              | 127.0.53.1 | NSEC                | delegates `test.` (NS `ns.test.` glue 127.0.53.2) with DS                                                                                                                                                                        |
| `test.`          | 127.0.53.2 | NSEC                | delegates `good` (glue 127.0.53.3, DS), `bad` (127.0.53.4, DS), `n3` (127.0.53.8, DS), `plain` (127.0.53.5, no DS), `glueless` (NS `ns2.plain.test.`, no glue, no DS), `spoof` (127.0.53.7, no DS), `poison` (127.0.53.9, no DS) |
| `good.test.`     | 127.0.53.3 | NSEC                | `www A 192.0.2.10`, `alias CNAME www.good.test.`, `big TXT` ×40 strings of 60 octets (response > 1232 octets), `ns A 127.0.53.3`                                                                                                 |
| `bad.test.`      | 127.0.53.4 | NSEC, broken        | `www A 192.0.2.11`                                                                                                                                                                                                               |
| `n3.test.`       | 127.0.53.8 | NSEC3, 0 iterations | `www A 192.0.2.40`                                                                                                                                                                                                               |
| `plain.test.`    | 127.0.53.5 | no                  | `www A 192.0.2.12`, `mail A 192.0.2.13`, `ip A 192.0.2.66`, `pass A 192.0.2.14`, `later A 192.0.2.15`, `ns2 A 127.0.53.6`                                                                                                        |
| `glueless.test.` | 127.0.53.6 | no                  | `www A 192.0.2.20`                                                                                                                                                                                                               |
| `spoof.test.`    | 127.0.53.7 | no, spoofing        | `www A 192.0.2.77`                                                                                                                                                                                                               |
| `poison.test.`   | 127.0.53.9 | no                  | `www A 192.0.2.30`, `sub NS ns.good.test.` with out-of-bailiwick glue `ns.good.test. A 127.0.53.66`                                                                                                                              |

Forwarder endpoint `127.0.53.100` answers RD=1 queries with RA=1 from the deepest zone in the hierarchy containing the name (DS queries from the parent zone), including RRSIG/NSEC when DO=1.

- [ ] Create `e2e/fixtures/authhier/authhier_test.go`:

```go
package authhier

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func start(t *testing.T) (*Hierarchy, Ready) {
	t.Helper()
	h, r, err := Start(context.Background(), DefaultSpec(0), time.Now())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(h.Close)
	if r.Port == 0 {
		t.Fatal("Ready.Port is 0: the shared port was not reported")
	}
	return h, r
}

func ask(t *testing.T, server string, port int, name string, qtype uint16, do bool, network string) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(name, qtype)
	m.RecursionDesired = false
	m.SetEdns0(1232, do)
	c := &dns.Client{Net: network, Timeout: 2 * time.Second}
	r, _, err := c.Exchange(m, net.JoinHostPort(server, strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("%s %s @%s: %v", name, dns.TypeToString[qtype], server, err)
	}
	return r
}

func keysOf(r *dns.Msg) []*dns.DNSKEY {
	var out []*dns.DNSKEY
	for _, rr := range r.Answer {
		if k, ok := rr.(*dns.DNSKEY); ok {
			out = append(out, k)
		}
	}
	return out
}

func verifyA(t *testing.T, r *dns.Msg, keys []*dns.DNSKEY) error {
	t.Helper()
	var set []dns.RR
	var sig *dns.RRSIG
	for _, rr := range r.Answer {
		switch v := rr.(type) {
		case *dns.A:
			set = append(set, v)
		case *dns.RRSIG:
			sig = v
		}
	}
	if sig == nil {
		t.Fatal("no RRSIG")
	}
	for _, k := range keys {
		if k.KeyTag() == sig.KeyTag {
			return sig.Verify(k, set)
		}
	}
	t.Fatal("no key with matching tag")
	return nil
}

func TestRootReferralCarriesSignedDSAndRootDSMatches(t *testing.T) {
	_, ready := start(t)
	r := ask(t, "127.0.53.1", ready.Port, "www.good.test.", dns.TypeA, true, "udp")
	if r.Authoritative || len(r.Ns) == 0 {
		t.Fatalf("expected referral, got %v", r)
	}
	var hasDS, hasSig bool
	for _, rr := range r.Ns {
		hasDS = hasDS || rr.Header().Rrtype == dns.TypeDS
		hasSig = hasSig || rr.Header().Rrtype == dns.TypeRRSIG
	}
	if !hasDS || !hasSig {
		t.Fatalf("referral lacks DS/RRSIG: %v", r.Ns)
	}
	keys := ask(t, "127.0.53.1", ready.Port, ".", dns.TypeDNSKEY, true, "udp")
	var matched bool
	for _, k := range keysOf(keys) {
		ds := k.ToDS(dns.SHA256)
		text := fmt.Sprintf("%d %d %d %s", ds.KeyTag, ds.Algorithm, ds.DigestType, strings.ToUpper(ds.Digest))
		if k.Flags == 257 && text == ready.RootDS {
			matched = true
		}
	}
	if !matched {
		t.Fatalf("root DS %q matches no root KSK", ready.RootDS)
	}
}

func TestSignedGoodVerifiesAndBadDoesNot(t *testing.T) {
	_, ready := start(t)
	good := ask(t, "127.0.53.3", ready.Port, "www.good.test.", dns.TypeA, true, "udp")
	if err := verifyA(t, good, keysOf(ask(t, "127.0.53.3", ready.Port, "good.test.", dns.TypeDNSKEY, true, "udp"))); err != nil {
		t.Fatalf("good.test signature: %v", err)
	}
	bad := ask(t, "127.0.53.4", ready.Port, "www.bad.test.", dns.TypeA, true, "udp")
	if err := verifyA(t, bad, keysOf(ask(t, "127.0.53.4", ready.Port, "bad.test.", dns.TypeDNSKEY, true, "udp"))); err == nil {
		t.Fatal("bad.test signature unexpectedly verifies")
	}
}

func TestNegativeAnswersCarryDenialProofs(t *testing.T) {
	_, ready := start(t)
	nx := ask(t, "127.0.53.3", ready.Port, "nope.good.test.", dns.TypeA, true, "udp")
	if nx.Rcode != dns.RcodeNameError || countType(nx.Ns, dns.TypeNSEC) < 2 {
		t.Fatalf("NSEC NXDOMAIN proof missing: %v", nx)
	}
	n3 := ask(t, "127.0.53.8", ready.Port, "nope.n3.test.", dns.TypeA, true, "udp")
	if n3.Rcode != dns.RcodeNameError || countType(n3.Ns, dns.TypeNSEC3) < 2 {
		t.Fatalf("NSEC3 NXDOMAIN proof missing: %v", n3)
	}
	ds := ask(t, "127.0.53.2", ready.Port, "plain.test.", dns.TypeDS, true, "udp")
	if len(ds.Answer) != 0 || countType(ds.Ns, dns.TypeNSEC) != 1 {
		t.Fatalf("insecure delegation proof missing: %v", ds)
	}
}

func countType(rrs []dns.RR, t uint16) int {
	n := 0
	for _, rr := range rrs {
		if rr.Header().Rrtype == t {
			n++
		}
	}
	return n
}

func TestBigAnswerTruncatesOverUDPAndCompletesOverTCP(t *testing.T) {
	_, ready := start(t)
	udp := ask(t, "127.0.53.3", ready.Port, "big.good.test.", dns.TypeTXT, false, "udp")
	if !udp.Truncated {
		t.Fatal("expected TC=1 over UDP")
	}
	tcp := ask(t, "127.0.53.3", ready.Port, "big.good.test.", dns.TypeTXT, false, "tcp")
	if tcp.Truncated || len(tcp.Answer) != 40 {
		t.Fatalf("tcp answer: tc=%v n=%d", tcp.Truncated, len(tcp.Answer))
	}
}

func TestSpoofServerSendsForgeriesBeforeRealAnswer(t *testing.T) {
	h, ready := start(t)
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.53.7"), Port: ready.Port})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	q := new(dns.Msg)
	q.SetQuestion("wWw.SpOof.test.", dns.TypeA)
	q.Id = 4242
	b, _ := q.Pack()
	conn.Write(b)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	var got []*dns.Msg
	buf := make([]byte, 1500)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			break
		}
		m := new(dns.Msg)
		if m.Unpack(buf[:n]) == nil {
			got = append(got, m)
		}
	}
	if len(got) != 4 {
		t.Fatalf("received %d replies on the query socket, want 4 (wrong id, wrong question, wrong case, real)", len(got))
	}
	last := got[len(got)-1]
	if last.Id != 4242 || last.Question[0].Name != "wWw.SpOof.test." || last.Answer[0].(*dns.A).A.String() != "192.0.2.77" {
		t.Fatalf("real answer wrong: %v", last)
	}
	if h.Stats().SpoofsSent != 4 {
		t.Fatalf("spoofs_sent = %d, want 4 (3 on socket + 1 from another port)", h.Stats().SpoofsSent)
	}
}

func TestForwarderEndpointAnswersRecursivelyWithSignatures(t *testing.T) {
	_, ready := start(t)
	host, portStr, _ := net.SplitHostPort(ready.Forwarder)
	port, _ := strconv.Atoi(portStr)
	m := new(dns.Msg)
	m.SetQuestion("www.good.test.", dns.TypeA)
	m.SetEdns0(1232, true)
	r, _, err := (&dns.Client{Timeout: 2 * time.Second}).Exchange(m, net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil || !r.RecursionAvailable || countType(r.Answer, dns.TypeRRSIG) != 1 {
		t.Fatalf("forwarder answer: %v %v", r, err)
	}
}
```

- [ ] Create `e2e/harness/named_test.go`:

```go
package harness

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestNamedHarnessServesTSIGAXFRAndReloads(t *testing.T) {
	env := New(t)
	zone := "$TTL 60\n@ SOA ns.rpz.axfr.test. h.rpz.axfr.test. 1 2 1 30 60\n@ NS ns.rpz.axfr.test.\nns A 127.0.0.1\nblocked-axfr.plain.test CNAME .\n"
	n := env.StartNamed("rpz.axfr.test.", zone)
	axfr := func() []dns.RR {
		tr := &dns.Transfer{TsigSecret: map[string]string{n.KeyName: n.KeySecretB64}}
		m := new(dns.Msg)
		m.SetAxfr("rpz.axfr.test.")
		m.SetTsig(n.KeyName, dns.HmacSHA256, 300, time.Now().Unix())
		ch, err := tr.In(m, n.Addr)
		if err != nil {
			t.Fatalf("axfr: %v", err)
		}
		var rrs []dns.RR
		for env := range ch {
			if env.Error != nil {
				t.Fatalf("axfr envelope: %v", env.Error)
			}
			rrs = append(rrs, env.RR...)
		}
		return rrs
	}
	if got := len(axfr()); got != 5 {
		t.Fatalf("axfr records = %d, want 5 (SOA NS A CNAME SOA)", got)
	}
	n.UpdateZone(t, "$TTL 60\n@ SOA ns.rpz.axfr.test. h.rpz.axfr.test. 2 2 1 30 60\n@ NS ns.rpz.axfr.test.\nns A 127.0.0.1\nblocked-axfr.plain.test CNAME .\nnew.plain.test CNAME .\n")
	Eventually(t, 5*time.Second, func() error {
		if got := len(axfr()); got != 6 {
			return fmt.Errorf("after reload axfr records = %d, want 6", got)
		}
		return nil
	})
	unsigned := new(dns.Msg)
	unsigned.SetAxfr("rpz.axfr.test.")
	ch, err := (&dns.Transfer{}).In(unsigned, n.Addr)
	if err == nil {
		for e := range ch {
			if e.Error == nil && len(e.RR) > 0 {
				t.Fatal("unsigned AXFR must be refused")
			}
		}
	}
}

func TestWriteKEKIs32Base64Bytes(t *testing.T) {
	raw, err := os.ReadFile(WriteKEK(t))
	if err != nil {
		t.Fatal(err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != 32 {
		t.Fatalf("KEK file holds %d bytes (%v)", len(key), err)
	}
}
```

- [ ] Run `scripts/dev-exec.sh go test ./e2e/fixtures/authhier/ ./e2e/harness/ -run 'Test(Root|Signed|Negative|Big|Spoof|Forwarder|NamedHarness|WriteKEK)'` — expect FAIL with "undefined: Start" (and `undefined: WriteKEK`).
- [ ] Implement `sign.go` (miekg/dns). Per signed zone: KSK `DNSKEY{Flags: 257, Protocol: 3, Algorithm: dns.ECDSAP256SHA256}` and ZSK `Flags: 256`, both `Generate(256)`; group records into RRsets by `(lowercase owner, type)`; sign every authoritative RRset except delegation NS and glue with the ZSK, the DNSKEY RRset with the KSK; `Inception = now - 1h`, `Expiration = now + 24h`, `SignerName = origin`, `OrigTtl = rrset TTL`, `Labels = dns.CountLabel(owner)` (minus one for a leading `*`). `BreakSignatures` flips the last byte of the base64-decoded signature of RRSIGs covering A/AAAA/TXT. DS for a child = `childKSK.ToDS(dns.SHA256)` inserted into the parent zone before the parent is signed (children are built first: `Start` signs zones sorted by label count descending). `Ready.RootDS` is the root KSK DS rdata `"<tag> 13 2 <HEX>"`. Canonical order for NSEC:

```go
func canonicalLess(a, b string) bool {
	la, lb := dns.SplitDomainName(strings.ToLower(a)), dns.SplitDomainName(strings.ToLower(b))
	for i, j := len(la)-1, len(lb)-1; i >= 0 && j >= 0; i, j = i-1, j-1 {
		x, y := unescape(la[i]), unescape(lb[j])
		if c := bytes.Compare(x, y); c != 0 {
			return c < 0
		}
	}
	return len(la) < len(lb)
}
```

(`unescape` turns `\DDD` and `\X` escapes into raw octets.) NSEC chain: owner names = every authoritative owner plus delegation points (bitmap `NS DS RRSIG NSEC` for signed children, `NS RRSIG NSEC` for unsigned ones); apex bitmap includes `SOA NS DNSKEY RRSIG NSEC`. NSEC3 (`NSEC3Iterations >= 0`): `dns.HashName(owner, dns.SHA1, iterations, "")` owners `<hash>.<origin>`, empty-non-terminal hashes included, NSEC3PARAM at apex, flags 0.

- [ ] Implement `server.go`: for every zone a `dns.Server` on UDP and TCP at `ServerIP:Port` with a handler implementing: REFUSED outside the origin; referral (AA=0, NS in authority, DS + RRSIG or the delegation's NSEC + RRSIG when the zone is signed and DO=1, glue from the zone's records for NS targets); DS queries at a cut answered authoritatively from the parent; exact answers with RRSIGs when DO=1; CNAME answers; NODATA with SOA (+NSEC/NSEC3 proof); NXDOMAIN with SOA + covering NSEC + wildcard-covering NSEC (NSEC) or closest-encloser match + next-closer cover + wildcard cover (NSEC3); UDP responses larger than the client EDNS size (or 512) are returned with TC=1 and no records; the reply echoes the query name's exact casing (`r.Question = q.Question`). `Spoof` zones: the UDP handler writes, in order and through the same `dns.ResponseWriter`: (1) the real answer with `Id ^ 0xFFFF` and `A 6.6.6.6`; (2) correct ID with question name `evil.spoof.test.` and `A 6.6.6.6`; (3) correct ID with the query name in all lower case and `A 6.6.6.6` (a 0x20 case mismatch whenever the query had upper-case letters); (4) from a second UDP socket bound to `ServerIP:0` (different source port) to the query's source address: correct ID and question with `A 6.6.6.6`; then after 30 ms (5) the real answer — `SpoofsSent` increments for (1)–(4). Every query increments `Queries[ServerIP]`. Forwarder at `ForwarderIP:Port`: resolves by selecting the deepest zone whose origin contains the name (for DS, the parent of the deepest), answering as that zone would authoritatively but with AA=0, RA=1 and following in-hierarchy CNAMEs. Stats HTTP server on `127.0.0.1:0` serving `GET /stats` as JSON.
- [ ] Implement the shared port in `server.go` `Start`: when `spec.Port == 0`, bind UDP on the first zone's `ServerIP:0`, then TCP on that port, then UDP+TCP for every other zone address and the forwarder on the same port; on `EADDRINUSE` close everything and start over (at most 20 attempts); `Ready.Port` is the port finally used, `Ready.RootHints` holds the root zone's NS names with address `127.0.53.1`, `Ready.Forwarder` is `ForwarderIP:Port`.
- [ ] Implement `e2e/fixtures/cmd/nexora-fixture/authhier.go`: `runAuthhier` parses `--ready-file`, calls `authhier.Start(context.Background(), authhier.DefaultSpec(0), time.Now())`, writes `Ready` as JSON to the ready file (write to `<file>.tmp`, rename) and returns `stop = h.Close` and `addrs = fmt.Sprintf("port=%d stats=%s", r.Port, strings.TrimPrefix(r.StatsURL, "http://"))`; add `case "authhier": stop, addrs, err = runAuthhier(os.Args[2:])` to `main.go` and the usage line `nexora-fixture authhier --ready-file F`.
- [ ] Implement `e2e/harness/hierarchy.go`: `StartHierarchy` runs `e.Start("nexora-fixture", []string{"authhier", "--ready-file", <e.Dir>/authhier-<n>.json}, nil)`, waits for `WaitReady(20 * time.Second)` and decodes the ready file into `Ready`. `Stats` GETs `Ready.StatsURL + "/stats"` with the harness `fixtureCall`. `ConfigureRecursion`:

```go
func (h *Hierarchy) ConfigureRecursion(t *testing.T, api *API, nodeName string) {
	t.Helper()
	var res map[string]any
	api.Must("GET", "/resolution", nil, &res, http.StatusOK)
	res["mode"] = "recursive"
	res["qname_minimisation"] = true
	res["aggressive_nsec"] = false
	res["authority_port"] = h.Ready.Port
	res["root_hints"] = h.Ready.RootHints
	api.Must("PUT", "/resolution", res, nil, http.StatusOK)
	api.Must("POST", "/dnssec/trust-anchors", map[string]string{"zone": ".", "ds": h.Ready.RootDS}, nil, http.StatusCreated)
	v := api.LatestVersion()
	api.WaitEngine(nodeName, 15*time.Second, func(e EngineView) bool { return e.AppliedVersion >= v })
}
```

- [ ] Implement `e2e/harness/named.go`: a directory from `os.MkdirTemp(e.Dir, "named-")` holding `zone.db` and `named.conf`:

```go
const namedConf = `options {
	directory "{{.Dir}}";
	listen-on port {{.Port}} { 127.0.0.1; };
	listen-on-v6 { none; };
	pid-file "{{.Dir}}/named.pid";
	recursion no;
	notify no;
	allow-transfer { key "{{.KeyName}}"; };
	ixfr-from-differences yes;
	dnssec-validation no;
};
key "{{.KeyName}}" {
	algorithm hmac-sha256;
	secret "{{.Secret}}";
};
zone "{{.Zone}}" {
	type primary;
	file "{{.Dir}}/zone.db";
};
`
```

- [ ] Finish `StartNamed`: render `namedConf` with `KeyName = "rpz-key."`, a random 32-byte base64 secret and `Port = e.FreePort()` (BIND cannot listen on port 0). Start `e.Start("named", []string{"-g", "-c", <dir>/named.conf, "-u", <current user name from os/user>}, nil)` (`Bin` resolves `named` from `$PATH`; the dev pod ships `bind9`); wait up to 10 s for a TCP SOA query to `127.0.0.1:Port` to succeed; when the process exits first (the port was taken meanwhile), pick a new port and retry, at most 3 times. `UpdateZone` rewrites `zone.db` and calls `n.Proc.Signal(syscall.SIGHUP)`; `Stop` calls `n.Proc.Stop()`.
- [ ] Implement `e2e/harness/kek.go` `WriteKEK`: 32 bytes from `crypto/rand`, base64-encoded with a trailing newline into `filepath.Join(t.TempDir(), "kek")` with mode `0600`.
- [ ] Run `scripts/dev-exec.sh go test ./e2e/fixtures/authhier/ ./e2e/harness/ -run 'Test(Root|Signed|Negative|Big|Spoof|Forwarder|NamedHarness|WriteKEK)'` — expect PASS (8 tests); then `scripts/dev-exec.sh go vet ./e2e/fixtures/cmd/nexora-fixture` — expect exit 0.
- [ ] Commit: `git add e2e/fixtures e2e/harness && git commit -m "test(e2e): private signed DNS hierarchy fixture, BIND primary and KEK harness helpers"`.

## Task 14: Acceptance tests — TestRecursionRootHints, TestSpoofedReplyRejected, TestDNSSECValidation, TestRPZPolicy

Files:

- `e2e/m3_helpers_test.go` — DNS query helper with DO/CD/TCP/source-address options, EDE extraction, `setupRecursion`.
- `e2e/recursion_test.go` — `TestRecursionRootHints`, `TestSpoofedReplyRejected`.
- `e2e/dnssec_test.go` — `TestDNSSECValidation`.
- `e2e/rpz_test.go` — `TestRPZPolicy`.

Interfaces:

```go
// package e2e (helper names avoid M1/M2's aRecord, firstA, question, udpFrom, waitLatestApplied)
const m3Engine = "engine-m3"
type qopt struct {
	DO, CD, TCP bool
	Source      string // local IP to bind, "" = default
	Timeout     time.Duration
}
type recursionEnv struct {
	env *harness.Env
	pg  *harness.Postgres
	api *harness.API
	eng *harness.Engine
	h   *harness.Hierarchy
}
func setupRecursion(t *testing.T) recursionEnv
func query(t *testing.T, server, name string, qtype uint16, o qopt) *dns.Msg
func queryErr(server, name string, qtype uint16, o qopt) (*dns.Msg, error)
func aValues(m *dns.Msg) []string
func edeCode(m *dns.Msg) (uint16, bool)
func wantA(t *testing.T, m *dns.Msg, want string)
func waitApplied(t *testing.T, api *harness.API)
```

- [ ] Create `e2e/recursion_test.go`:

```go
package e2e

import (
	"testing"

	"github.com/miekg/dns"
)

func TestRecursionRootHints(t *testing.T) {
	r := setupRecursion(t)
	addr := r.eng.DNS

	var upstreams []map[string]any
	r.api.Must("GET", "/upstreams", nil, &upstreams, 200)
	if len(upstreams) != 0 {
		t.Fatalf("test requires no forwarders configured, found %d", len(upstreams))
	}

	t.Run("resolves from root hints", func(t *testing.T) {
		before := r.h.Stats(t).Queries["127.0.53.1"]
		wantA(t, query(t, addr, "www.good.test", dns.TypeA, qopt{}), "192.0.2.10")
		if r.h.Stats(t).Queries["127.0.53.1"] <= before {
			t.Fatal("fake root was never queried")
		}
		if r.eng.Metric(t, "nexora_resolutions_total", map[string]string{"route": "recursive"}) < 1 {
			t.Fatal("nexora_resolutions_total{route=recursive} did not increase")
		}
	})
	t.Run("cname chain", func(t *testing.T) {
		m := query(t, addr, "alias.good.test", dns.TypeA, qopt{})
		wantA(t, m, "192.0.2.10")
		if _, ok := m.Answer[0].(*dns.CNAME); !ok {
			t.Fatalf("first answer is not the CNAME: %v", m.Answer)
		}
	})
	t.Run("glueless delegation", func(t *testing.T) {
		wantA(t, query(t, addr, "www.glueless.test", dns.TypeA, qopt{}), "192.0.2.20")
	})
	t.Run("tcp fallback on truncated authoritative reply", func(t *testing.T) {
		m := query(t, addr, "big.good.test", dns.TypeTXT, qopt{TCP: true})
		if m.Rcode != dns.RcodeSuccess || len(m.Answer) != 40 {
			t.Fatalf("big TXT over TCP: rcode=%s n=%d", dns.RcodeToString[m.Rcode], len(m.Answer))
		}
		if r.eng.Metric(t, "nexora_recursor_tcp_fallback_total", nil) < 1 {
			t.Fatal("engine did not retry the truncated authoritative reply over TCP")
		}
	})
	t.Run("nxdomain", func(t *testing.T) {
		if m := query(t, addr, "nope.plain.test", dns.TypeA, qopt{}); m.Rcode != dns.RcodeNameError {
			t.Fatalf("rcode = %s", dns.RcodeToString[m.Rcode])
		}
	})
	t.Run("out-of-bailiwick glue is not used", func(t *testing.T) {
		wantA(t, query(t, addr, "www.poison.test", dns.TypeA, qopt{}), "192.0.2.30")
		_ = query(t, addr, "www.sub.poison.test", dns.TypeA, qopt{})
		wantA(t, query(t, addr, "ns.good.test", dns.TypeA, qopt{}), "127.0.53.3")
		if n := r.h.Stats(t).Queries["127.0.53.66"]; n != 0 {
			t.Fatalf("engine sent %d queries to the poisoned glue address", n)
		}
	})
	t.Run("fixture servers preserve 0x20 case so no case mismatches occur", func(t *testing.T) {
		if n := r.eng.Metric(t, "nexora_recursor_mismatched_replies_total", map[string]string{"reason": "case"}); n != 0 {
			t.Fatalf("case mismatches = %v, want 0 against honest servers", n)
		}
	})
}

func TestSpoofedReplyRejected(t *testing.T) {
	r := setupRecursion(t)
	addr := r.eng.DNS

	// positive path first: the hierarchy and recursion work in this run
	wantA(t, query(t, addr, "www.good.test", dns.TypeA, qopt{}), "192.0.2.10")

	m := query(t, addr, "www.spoof.test", dns.TypeA, qopt{})
	wantA(t, m, "192.0.2.77")
	for _, v := range aValues(m) {
		if v == "6.6.6.6" {
			t.Fatal("spoofed address served")
		}
	}
	st := r.h.Stats(t)
	if st.SpoofsSent < 4 {
		t.Fatalf("fixture sent %d spoofs, want >= 4", st.SpoofsSent)
	}
	for _, reason := range []string{"id", "question", "case"} {
		if r.eng.Metric(t, "nexora_recursor_mismatched_replies_total", map[string]string{"reason": reason}) < 1 {
			t.Fatalf("mismatched reply with wrong %s was not counted", reason)
		}
	}
	spoofQueries := st.Queries["127.0.53.7"]
	again := query(t, addr, "www.spoof.test", dns.TypeA, qopt{})
	wantA(t, again, "192.0.2.77")
	if got := r.h.Stats(t).Queries["127.0.53.7"]; got != spoofQueries {
		t.Fatalf("second query reached the authoritative server (%d -> %d); the cached answer must be the real one", spoofQueries, got)
	}
	if again.Answer[len(again.Answer)-1].Header().Ttl >= m.Answer[len(m.Answer)-1].Header().Ttl+1 {
		t.Fatal("cached TTL not decremented")
	}
}
```

- [ ] Create `e2e/dnssec_test.go`:

```go
package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

func TestDNSSECValidation(t *testing.T) {
	r := setupRecursion(t)
	addr := r.eng.DNS

	t.Run("signed zone returns AD", func(t *testing.T) {
		m := query(t, addr, "www.good.test", dns.TypeA, qopt{DO: true})
		wantA(t, m, "192.0.2.10")
		if !m.AuthenticatedData {
			t.Fatal("AD=0 for a secure answer")
		}
		if n := query(t, addr, "www.n3.test", dns.TypeA, qopt{DO: true}); !n.AuthenticatedData {
			t.Fatal("AD=0 for NSEC3-signed zone")
		}
		nx := query(t, addr, "nope.good.test", dns.TypeA, qopt{DO: true})
		if nx.Rcode != dns.RcodeNameError || !nx.AuthenticatedData {
			t.Fatalf("NXDOMAIN proof: rcode=%s ad=%v", dns.RcodeToString[nx.Rcode], nx.AuthenticatedData)
		}
	})
	t.Run("broken signature returns SERVFAIL with EDE", func(t *testing.T) {
		m := query(t, addr, "www.bad.test", dns.TypeA, qopt{DO: true})
		if m.Rcode != dns.RcodeServerFailure || len(aValues(m)) != 0 {
			t.Fatalf("bogus data served: rcode=%s answers=%v", dns.RcodeToString[m.Rcode], aValues(m))
		}
		if code, ok := edeCode(m); !ok || code != 6 {
			t.Fatalf("EDE = %d (present %v), want 6", code, ok)
		}
		if r.eng.Metric(t, "nexora_dnssec_validations_total", map[string]string{"result": "bogus"}) < 1 {
			t.Fatal("bogus validation not counted")
		}
	})
	t.Run("CD bit returns unvalidated data without AD", func(t *testing.T) {
		m := query(t, addr, "www.bad.test", dns.TypeA, qopt{DO: true, CD: true})
		wantA(t, m, "192.0.2.11")
		if m.AuthenticatedData {
			t.Fatal("AD=1 with CD=1 on bogus data")
		}
	})
	t.Run("unsigned zone returns AD=0", func(t *testing.T) {
		m := query(t, addr, "www.plain.test", dns.TypeA, qopt{DO: true})
		wantA(t, m, "192.0.2.12")
		if m.AuthenticatedData {
			t.Fatal("AD=1 for an insecure answer")
		}
	})
	t.Run("negative trust anchor", func(t *testing.T) {
		var nta struct {
			ID string `json:"id"`
		}
		r.api.Must("POST", "/dnssec/negative-trust-anchors", map[string]any{"domain": "bad.test.", "reason": "e2e", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, &nta, 201)
		waitApplied(t, r.api)
		m := query(t, addr, "www.bad.test", dns.TypeA, qopt{DO: true})
		wantA(t, m, "192.0.2.11")
		if m.AuthenticatedData {
			t.Fatal("AD=1 under an NTA")
		}
		r.api.Must("DELETE", "/dnssec/negative-trust-anchors/"+nta.ID, nil, nil, 204)
		waitApplied(t, r.api)
		if m := query(t, addr, "www.bad.test", dns.TypeA, qopt{DO: true}); m.Rcode != dns.RcodeServerFailure {
			t.Fatalf("after NTA removal rcode = %s, want SERVFAIL (cached insecure answer must not be reused)", dns.RcodeToString[m.Rcode])
		}
	})
	t.Run("forwarded answers are validated", func(t *testing.T) {
		var fz struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		}
		r.api.Must("POST", "/forward-zones", map[string]any{"domain": "test.", "addresses": []string{r.h.Ready.Forwarder}, "validate": true}, &fz, 201)
		waitApplied(t, r.api)
		before := r.eng.Metric(t, "nexora_resolutions_total", map[string]string{"route": "forward_zone"})
		good := query(t, addr, "www.good.test", dns.TypeA, qopt{DO: true})
		wantA(t, good, "192.0.2.10")
		if !good.AuthenticatedData {
			t.Fatal("forwarded secure answer lacks AD")
		}
		if bad := query(t, addr, "www.bad.test", dns.TypeA, qopt{DO: true}); bad.Rcode != dns.RcodeServerFailure {
			t.Fatalf("forwarded bogus answer rcode = %s", dns.RcodeToString[bad.Rcode])
		}
		if after := r.eng.Metric(t, "nexora_resolutions_total", map[string]string{"route": "forward_zone"}); after <= before {
			t.Fatalf("forward zone route not used (%v -> %v)", before, after)
		}
		r.api.Must("DELETE", fmt.Sprintf("/forward-zones/%s?revision=%d", fz.ID, fz.Revision), nil, nil, 204)
	})
	t.Run("status reports trust anchors", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, func() error {
			var st struct {
				Engines []struct {
					Secure       int64 `json:"secure"`
					TrustAnchors []struct {
						Zone string `json:"zone"`
					} `json:"trust_anchors"`
				} `json:"engines"`
			}
			if _, err := r.api.Do("GET", "/dnssec/status", nil, &st); err != nil {
				return err
			}
			if len(st.Engines) != 1 || st.Engines[0].Secure < 1 || len(st.Engines[0].TrustAnchors) < 1 {
				return fmt.Errorf("status = %+v", st)
			}
			return nil
		})
	})
}
```

- [ ] Create `e2e/rpz_test.go`:

```go
package e2e

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

const rpzFileZone = `$TTL 60
@ SOA ns.rpz.file.test. h.rpz.file.test. 1 60 60 86400 60
@ NS ns.rpz.file.test.
www.plain.test CNAME .
mail.plain.test A 10.9.9.9
pass.plain.test CNAME rpz-passthru.
32.66.2.0.192.rpz-ip CNAME *.
32.2.0.0.127.rpz-client-ip CNAME rpz-tcp-only.
`

func rpzAxfrZone(serial int, extra string) string {
	return fmt.Sprintf("$TTL 60\n@ SOA ns.rpz.axfr.test. h.rpz.axfr.test. %d 2 1 30 60\n@ NS ns.rpz.axfr.test.\nns A 127.0.0.1\npass.plain.test CNAME .\nblocked-axfr.plain.test A 10.8.8.8\nns2.plain.test.rpz-nsdname CNAME .\n%s", serial, extra)
}

type rpzZone struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Status   []struct {
		Serial    int64  `json:"serial"`
		LastError string `json:"last_error"`
	} `json:"status"`
}

func TestRPZPolicy(t *testing.T) {
	r := setupRecursion(t)
	addr := r.eng.DNS

	// positive path before any policy exists
	wantA(t, query(t, addr, "www.plain.test", dns.TypeA, qopt{}), "192.0.2.12")
	wantA(t, query(t, addr, "www.glueless.test", dns.TypeA, qopt{}), "192.0.2.20")
	wantA(t, query(t, addr, "later.plain.test", dns.TypeA, qopt{}), "192.0.2.15")
	wantA(t, query(t, addr, "www.good.test", dns.TypeA, qopt{Source: "127.0.0.2"}), "192.0.2.10")

	named := r.env.StartNamed("rpz.axfr.test.", rpzAxfrZone(1, ""))

	var file rpzZone
	r.api.Must("POST", "/rpz-zones", map[string]any{"name": "rpz.file.test.", "source_type": "file", "policy_override": "given", "min_refresh_seconds": 60}, &file, 201)
	r.api.Must("PUT", "/rpz-zones/"+file.ID+"/file", map[string]any{"content": rpzFileZone, "revision": file.Revision}, &file, 200)
	var axfr rpzZone
	r.api.Must("POST", "/rpz-zones", map[string]any{"name": "rpz.axfr.test.", "source_type": "transfer", "primary": named.Addr, "tsig_key_name": named.KeyName, "tsig_algorithm": "hmac-sha256", "tsig_secret": named.KeySecretB64, "policy_override": "given", "min_refresh_seconds": 1}, &axfr, 201)
	waitApplied(t, r.api)
	harness.Eventually(t, 20*time.Second, func() error {
		var z rpzZone
		if _, err := r.api.Do("GET", "/rpz-zones/"+axfr.ID, nil, &z); err != nil {
			return err
		}
		if len(z.Status) != 1 || z.Status[0].Serial != 1 {
			return fmt.Errorf("status %+v", z.Status)
		}
		return nil
	})

	t.Run("tsig secret is stored sealed and never in a snapshot", func(t *testing.T) {
		secret, err := base64.StdEncoding.DecodeString(named.KeySecretB64)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx, r.pg.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(context.WithoutCancel(ctx))
		var rows string
		if err := conn.QueryRow(ctx, "select string_agg(t::text, E'\\n') from rpz_zones t").Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rows, "rpz.axfr.test.") {
			t.Fatalf("rpz_zones dump lacks the transfer zone: %s", rows)
		}
		if strings.Contains(rows, named.KeySecretB64) || strings.Contains(rows, hex.EncodeToString(secret)) {
			t.Fatal("plaintext TSIG secret stored in rpz_zones")
		}
		var leaked bool
		if err := conn.QueryRow(ctx, "select coalesce(bool_or(position($1::bytea in snapshot) > 0), false) from config_versions", secret).Scan(&leaked); err != nil {
			t.Fatal(err)
		}
		if leaked {
			t.Fatal("TSIG secret found in a stored config version")
		}
	})
	t.Run("file zone qname NXDOMAIN with EDE", func(t *testing.T) {
		m := query(t, addr, "www.plain.test", dns.TypeA, qopt{})
		if m.Rcode != dns.RcodeNameError {
			t.Fatalf("rcode = %s, want NXDOMAIN", dns.RcodeToString[m.Rcode])
		}
		if code, ok := edeCode(m); !ok || code != 15 {
			t.Fatalf("EDE = %d %v, want 15", code, ok)
		}
	})
	t.Run("file zone local data", func(t *testing.T) {
		wantA(t, query(t, addr, "mail.plain.test", dns.TypeA, qopt{}), "10.9.9.9")
	})
	t.Run("zone order: passthru in first zone beats NXDOMAIN in second", func(t *testing.T) {
		wantA(t, query(t, addr, "pass.plain.test", dns.TypeA, qopt{}), "192.0.2.14")
	})
	t.Run("response IP trigger NODATA", func(t *testing.T) {
		m := query(t, addr, "ip.plain.test", dns.TypeA, qopt{})
		if m.Rcode != dns.RcodeSuccess || len(aValues(m)) != 0 {
			t.Fatalf("rcode=%s answers=%v, want NODATA", dns.RcodeToString[m.Rcode], aValues(m))
		}
	})
	t.Run("axfr zone local data", func(t *testing.T) {
		wantA(t, query(t, addr, "blocked-axfr.plain.test", dns.TypeA, qopt{}), "10.8.8.8")
	})
	t.Run("axfr zone NSDNAME trigger overrides a previously cached answer", func(t *testing.T) {
		if m := query(t, addr, "www.glueless.test", dns.TypeA, qopt{}); m.Rcode != dns.RcodeNameError {
			t.Fatalf("rcode = %s, want NXDOMAIN", dns.RcodeToString[m.Rcode])
		}
	})
	t.Run("client-ip trigger forces TCP", func(t *testing.T) {
		udp := query(t, addr, "www.good.test", dns.TypeA, qopt{Source: "127.0.0.2"})
		if !udp.Truncated || len(udp.Answer) != 0 {
			t.Fatalf("UDP from 127.0.0.2: tc=%v answers=%d", udp.Truncated, len(udp.Answer))
		}
		wantA(t, query(t, addr, "www.good.test", dns.TypeA, qopt{Source: "127.0.0.2", TCP: true}), "192.0.2.10")
	})
	t.Run("incremental transfer picks up a change", func(t *testing.T) {
		named.UpdateZone(t, rpzAxfrZone(2, "later.plain.test A 10.7.7.7\n"))
		harness.Eventually(t, 20*time.Second, func() error {
			m, err := queryErr(addr, "later.plain.test", dns.TypeA, qopt{})
			if err != nil {
				return err
			}
			if v := aValues(m); len(v) != 1 || v[0] != "10.7.7.7" {
				return fmt.Errorf("answers %v", v)
			}
			return nil
		})
	})
	t.Run("failed refresh keeps last good zone and reports error", func(t *testing.T) {
		named.Stop()
		r.api.Must("POST", "/rpz-zones/"+axfr.ID+"/refresh", map[string]any{}, nil, 202)
		harness.Eventually(t, 30*time.Second, func() error {
			var z rpzZone
			if _, err := r.api.Do("GET", "/rpz-zones/"+axfr.ID, nil, &z); err != nil {
				return err
			}
			if len(z.Status) != 1 || z.Status[0].LastError == "" {
				return fmt.Errorf("status %+v", z.Status)
			}
			return nil
		})
		wantA(t, query(t, addr, "later.plain.test", dns.TypeA, qopt{}), "10.7.7.7")
		wantA(t, query(t, addr, "mail.plain.test", dns.TypeA, qopt{}), "10.9.9.9")
	})
}
```

- [ ] Run `scripts/dev-exec.sh go vet ./e2e/` — expect FAIL with "undefined: setupRecursion".
- [ ] Create `e2e/m3_helpers_test.go`:

```go
package e2e

import (
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

const m3Engine = "engine-m3"

type qopt struct {
	DO, CD, TCP bool
	Source      string
	Timeout     time.Duration
}

type recursionEnv struct {
	env *harness.Env
	pg  *harness.Postgres
	api *harness.API
	eng *harness.Engine
	h   *harness.Hierarchy
}

func queryErr(server, name string, qtype uint16, o qopt) (*dns.Msg, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), qtype)
	m.RecursionDesired = true
	m.CheckingDisabled = o.CD
	m.SetEdns0(1232, o.DO)
	c := &dns.Client{Timeout: o.Timeout}
	if c.Timeout == 0 {
		c.Timeout = 5 * time.Second
	}
	if o.TCP {
		c.Net = "tcp"
	}
	if o.Source != "" {
		if o.TCP {
			c.Dialer = &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(o.Source)}, Timeout: c.Timeout}
		} else {
			c.Dialer = &net.Dialer{LocalAddr: &net.UDPAddr{IP: net.ParseIP(o.Source)}, Timeout: c.Timeout}
		}
	}
	r, _, err := c.Exchange(m, server)
	return r, err
}

func query(t *testing.T, server, name string, qtype uint16, o qopt) *dns.Msg {
	t.Helper()
	r, err := queryErr(server, name, qtype, o)
	if err != nil {
		t.Fatalf("query %s %s: %v", name, dns.TypeToString[qtype], err)
	}
	return r
}

func aValues(m *dns.Msg) []string {
	var out []string
	for _, rr := range m.Answer {
		if a, ok := rr.(*dns.A); ok {
			out = append(out, a.A.String())
		}
	}
	return out
}

func edeCode(m *dns.Msg) (uint16, bool) {
	if opt := m.IsEdns0(); opt != nil {
		for _, o := range opt.Option {
			if e, ok := o.(*dns.EDNS0_EDE); ok {
				return e.InfoCode, true
			}
		}
	}
	return 0, false
}

func wantA(t *testing.T, m *dns.Msg, want string) {
	t.Helper()
	got := aValues(m)
	if m.Rcode != dns.RcodeSuccess || len(got) == 0 || got[len(got)-1] != want {
		t.Fatalf("want A %s, got rcode=%s answers=%v", want, dns.RcodeToString[m.Rcode], got)
	}
}

// waitApplied waits until the M3 engine applied the newest config version.
func waitApplied(t *testing.T, api *harness.API) {
	t.Helper()
	v := api.LatestVersion()
	api.WaitEngine(m3Engine, 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion >= v })
}

// setupRecursion starts PostgreSQL, a management plane with key storage, one managed engine and the
// private hierarchy, and switches the engine to recursion from the hierarchy's root.
func setupRecursion(t *testing.T) recursionEnv {
	t.Helper()
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	mgmt := env.StartMgmt(pg, ca, harness.MgmtOptions{ExtraEnv: []string{"NEXORA_KEK_FILE=" + harness.WriteKEK(t)}})
	api := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
	eng := env.StartManagedEngine(m3Engine, []string{mgmt.GRPCURL}, api.CreateJoinToken())
	waitApplied(t, api)
	h := env.StartHierarchy()
	h.ConfigureRecursion(t, api, m3Engine)
	return recursionEnv{env: env, pg: pg, api: api, eng: eng, h: h}
}
```

- [ ] Run `scripts/dev-exec.sh bash -c 'make e2e-build && NEXORA_E2E_BIN_DIR=bin go test -count=3 ./e2e/ -run "TestRecursionRootHints|TestSpoofedReplyRejected|TestDNSSECValidation|TestRPZPolicy"'` — expect PASS three times in a row (no flakes).
- [ ] Run the full suite: `scripts/dev-exec.sh make e2e` — expect PASS, including the M1/M2 tests (forward mode is still the default and forwards the client's bytes unvalidated).
- [ ] Commit: `git add e2e && git commit -m "test(e2e): recursion, spoofing, DNSSEC validation and RPZ acceptance tests"`.

## Task 15: kw deployment update and kw smoke subtests

Files:

- `e2e/kw_smoke_m3_test.go` — `TestKwSmokeM3` with subtests `recursion`, `dnssec`, `rpz`, `metrics` (package `e2e`, skipped unless the kw variables are set, like M1's `TestKwSmoke`).
- `deploy/kw/README.md` — the M3 smoke command and the M3 limits on kw.

No manifest changes: `deploy/kw/engine.yaml` and `deploy/kw/mgmt.yaml` already take the image tag from `scripts/kw-deploy.sh` (`NEXORA_TAG`); migrations `00300`–`00302` run when `nexora-mgmt serve` starts (`store.Migrate`); recursion needs only egress to UDP/TCP 53, which kw allows (no NetworkPolicy exists in namespace `nexora`); engine state stays `emptyDir` (Architecture change 8); `NEXORA_KEK_FILE` is not configured on kw in M3, so TSIG-protected RPZ transfers are refused there with 503 until M4 Task 16 mounts the `nexora-kek` secret (the smoke test uses a file zone).

Interfaces:

```go
// package e2e
// env: NEXORA_KW_DNS_ADDR (192.168.10.136:53), NEXORA_KW_API_URL (http://nexora.kw.local),
//      NEXORA_KW_ADMIN_PASSWORD (from Secret nexora-admin), NEXORA_KW_ADMIN_USER (default "admin"),
//      NEXORA_KW_ENGINE_METRICS_URL (default http://nexora-engine-metrics.nexora.svc.cluster.local:9153/metrics)
func TestKwSmokeM3(t *testing.T)
```

- [ ] Create `e2e/kw_smoke_m3_test.go`:

```go
package e2e

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/piwi3910/nexora/e2e/harness"
)

// TestKwSmokeM3 checks M3 on the kw deployment from inside the cluster network: recursion from the
// real root, DNSSEC validation, an RPZ file zone and the M3 metrics. It restores the resolution
// settings and deletes its RPZ zone when it ends.
func TestKwSmokeM3(t *testing.T) {
	dnsAddr, apiURL, password := os.Getenv("NEXORA_KW_DNS_ADDR"), strings.TrimSuffix(os.Getenv("NEXORA_KW_API_URL"), "/"), os.Getenv("NEXORA_KW_ADMIN_PASSWORD")
	if dnsAddr == "" || apiURL == "" || password == "" {
		t.Skip("NEXORA_KW_DNS_ADDR, NEXORA_KW_API_URL and NEXORA_KW_ADMIN_PASSWORD are not set")
	}
	user := os.Getenv("NEXORA_KW_ADMIN_USER")
	if user == "" {
		user = "admin"
	}
	metricsURL := os.Getenv("NEXORA_KW_ENGINE_METRICS_URL")
	if metricsURL == "" {
		metricsURL = "http://nexora-engine-metrics.nexora.svc.cluster.local:9153/metrics"
	}
	api := harness.New(t).NewAPI(apiURL)
	api.Must("POST", "/auth/login", map[string]string{"username": user, "password": password}, nil, http.StatusOK)

	ask := func(name string, qtype uint16, do bool) (*dns.Msg, error) {
		m := new(dns.Msg)
		m.SetQuestion(dns.Fqdn(name), qtype)
		m.SetEdns0(1232, do)
		r, _, err := (&dns.Client{Timeout: 5 * time.Second}).Exchange(m, dnsAddr)
		return r, err
	}

	var original map[string]any
	api.Must("GET", "/resolution", nil, &original, http.StatusOK)
	t.Cleanup(func() {
		var cur map[string]any
		api.Must("GET", "/resolution", nil, &cur, http.StatusOK)
		original["revision"] = cur["revision"]
		api.Must("PUT", "/resolution", original, nil, http.StatusOK)
	})
	recursive := map[string]any{}
	for k, v := range original {
		recursive[k] = v
	}
	recursive["mode"] = "recursive"
	recursive["root_hints"] = []any{}
	recursive["authority_port"] = 53
	api.Must("PUT", "/resolution", recursive, nil, http.StatusOK)

	t.Run("recursion", func(t *testing.T) {
		harness.Eventually(t, 60*time.Second, func() error {
			r, err := ask("www.iana.org", dns.TypeA, false)
			if err != nil {
				return err
			}
			if r.Rcode != dns.RcodeSuccess || len(r.Answer) == 0 {
				return fmt.Errorf("rcode %s answers %d", dns.RcodeToString[r.Rcode], len(r.Answer))
			}
			return nil
		})
	})

	t.Run("dnssec", func(t *testing.T) {
		harness.Eventually(t, 30*time.Second, func() error {
			r, err := ask("www.iana.org", dns.TypeA, true)
			if err != nil {
				return err
			}
			if !r.AuthenticatedData {
				return fmt.Errorf("www.iana.org not AD (rcode %s)", dns.RcodeToString[r.Rcode])
			}
			return nil
		})
		r, err := ask("dnssec-failed.org", dns.TypeA, true)
		if err != nil || r.Rcode != dns.RcodeServerFailure {
			t.Fatalf("dnssec-failed.org: %v %v, want SERVFAIL", r, err)
		}
		var st struct {
			Engines []struct {
				EngineName   string `json:"engine_name"`
				TrustAnchors []struct {
					Zone  string `json:"zone"`
					State string `json:"state"`
				} `json:"trust_anchors"`
			} `json:"engines"`
		}
		harness.Eventually(t, 30*time.Second, func() error {
			if _, err := api.Do("GET", "/dnssec/status", nil, &st); err != nil {
				return err
			}
			if len(st.Engines) == 0 {
				return fmt.Errorf("no engine reported DNSSEC status")
			}
			for _, e := range st.Engines {
				usable := false
				for _, a := range e.TrustAnchors {
					usable = usable || (a.Zone == "." && (a.State == "valid" || a.State == "configured"))
				}
				if !usable {
					return fmt.Errorf("engine %s has no usable root trust anchor: %+v", e.EngineName, e.TrustAnchors)
				}
			}
			return nil
		})
	})

	t.Run("rpz", func(t *testing.T) {
		if r, err := ask("example.org", dns.TypeA, false); err != nil || r.Rcode != dns.RcodeSuccess {
			t.Fatalf("positive path: example.org %v %v", r, err)
		}
		var z struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		}
		api.Must("POST", "/rpz-zones", map[string]any{"name": "rpz.kwsmoke.nexora.", "source_type": "file", "policy_override": "given", "min_refresh_seconds": 60}, &z, http.StatusCreated)
		zone := "$TTL 60\n@ SOA ns.rpz.kwsmoke.nexora. h.rpz.kwsmoke.nexora. 1 60 60 86400 60\n@ NS ns.rpz.kwsmoke.nexora.\nexample.org CNAME .\n"
		api.Must("PUT", "/rpz-zones/"+z.ID+"/file", map[string]any{"content": zone, "revision": z.Revision}, &z, http.StatusOK)
		t.Cleanup(func() { api.Must("DELETE", fmt.Sprintf("/rpz-zones/%s?revision=%d", z.ID, z.Revision), nil, nil, http.StatusNoContent) })
		harness.Eventually(t, 60*time.Second, func() error {
			r, err := ask("example.org", dns.TypeA, false)
			if err != nil {
				return err
			}
			if r.Rcode != dns.RcodeNameError {
				return fmt.Errorf("rcode %s, want NXDOMAIN", dns.RcodeToString[r.Rcode])
			}
			return nil
		})
	})

	t.Run("metrics", func(t *testing.T) {
		resp, err := http.Get(metricsURL)
		if err != nil {
			t.Fatalf("%s: %v", metricsURL, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		for _, name := range []string{"nexora_resolutions_total", "nexora_recursor_upstream_queries_total", "nexora_dnssec_validations_total", "nexora_rpz_zone_serial", "nexora_dnssec_trust_anchor_keys"} {
			if !strings.Contains(string(body), name) {
				t.Fatalf("%s lacks %s", metricsURL, name)
			}
		}
	})
}
```

- [ ] Run against the currently deployed (M2) build to prove the smoke test detects the missing feature: `scripts/dev-exec.sh env NEXORA_KW_DNS_ADDR=192.168.10.136:53 NEXORA_KW_API_URL=http://nexora.kw.local NEXORA_KW_ADMIN_PASSWORD="$(kubectl --context kw -n nexora get secret nexora-admin -o jsonpath='{.data.password}' | base64 -d)" go test -count=1 -v -run TestKwSmokeM3 ./e2e/` — expect FAIL with "GET /resolution: status 404".
- [ ] Deploy the M3 build: `scripts/kw-deploy.sh` (builds and pushes `nexora-engine` and `nexora-mgmt` tagged `sha-<7>`, applies `deploy/kw/*.yaml` with that tag, reruns the idempotent `bootstrap.sh`, waits for both rollouts) — expect `deployment "nexora-mgmt" successfully rolled out`, `deployment "nexora-engine" successfully rolled out` and the final `NEXORA_KW_DNS_ADDR=192.168.10.136:53` line.
- [ ] Re-run the smoke command from the first step — expect PASS for `TestKwSmokeM3/recursion`, `/dnssec`, `/rpz`, `/metrics`; then run M1/M2's `scripts/dev-exec.sh env NEXORA_KW_DNS_ADDR=192.168.10.136:53 NEXORA_KW_API_URL=http://nexora.kw.local go test -count=1 -v -run 'TestKwSmoke$' ./e2e/` — expect PASS (the cleanup restored forward mode).
- [ ] Update `deploy/kw/README.md`: add the `TestKwSmokeM3` command above (with the `nexora-admin` password lookup) under the smoke-test paragraph, and to "Known limits": "M3: engine state is still an `emptyDir`, so RFC 5011 trust-anchor state and RPZ last-good zone copies are rebuilt after an engine pod restart (M5 moves state to `hostPath`)" and "M3: `NEXORA_KEK_FILE` is not mounted, so RPZ zones with TSIG are refused (M4 adds the `nexora-kek` secret)".
- [ ] Commit: `git add e2e/kw_smoke_m3_test.go deploy/kw/README.md && git commit -m "test(kw): M3 smoke subtests for recursion, DNSSEC, RPZ and metrics"`.
