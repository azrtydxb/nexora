# nexora-m6-operator-ux — implementation plan

Status: draft
Spec: .procoder/specs/nexora-m6-operator-ux.md

## Goal

Ship milestone M6 Operator UX (GitHub issues #54–#67) as one coherent release. It covers the query log
(partial names, multi-value filters, decision reasons), the dashboard, navigation and page text, help,
the resolver/authoritative access split, the `parallel` upstream strategy, the engine modal with
metrics and logs, account self-service and build version display. The work is cut into
file-disjoint tasks that parallel agents can build wave by wave.

## Architecture

Three contract tasks come first and freeze every shared name:

- the proto fields 700–799 and the architecture text (Task 1);
- the OpenAPI operations, permissions, generated code and 501 stubs in feature-owned files (Task 2);
- the GUI e2e seed registry (Task 3).

Feature work then splits by layer:

- **Engine:**
  - Task 4 plumbs the new `QueryRecord` fields, OTLP attributes, `Stats` fields, counters and the
    `Strategy::Parallel` variant.
  - Later tasks fill in attribution (12), the log ring buffer (13), the race (15) and the ACL split
    (16).
- **Management plane:** each operation family owns its handler file, its store/service package and its
  migration.
- **GUI:** each page or component is owned by one task per wave. The shell files `AppShell.tsx` and
  `router.tsx` are edited by Tasks 21 → 24 → 26 in sequence. Help and page descriptions come last,
  split by page group, once no other task edits those pages.

### Decisions not settled by the spec (made here, binding for the tasks)

- **Proto numbers:**
  - `ConfigSnapshot`: `authoritative_allow_cidrs = 700`, `authoritative_acl_set = 701`.
  - `AuthZone`: `allow_query_cidrs = 700`, `update_allow_cidrs = 701`.
  - `UPSTREAM_STRATEGY_PARALLEL = 3`; `ResolverConfig.parallel_max = 700`.
  - `ServerMessage.log_request = 700`, `EngineMessage.log_batch = 700`.
  - `Stats` fields 700–714 in the order of the spec's Interfaces line.
  - `RecursionStats.upstream_timeouts = 700`, `UpstreamStatus.race_wins_total = 700`.
  - New messages `LogRequest`, `LogLine`, `LogBatch`; enum `LogLevel`.
- **Query-log attributes:** the engine emits attribution only when set.
  - `nexora.filter.source` is one of `blocklist|category|allowlist|rpz|rewrite|acl`.
  - `nexora.filter.rule` is the matched listed suffix in text form without the trailing dot; for
    rewrites it is the rule name (`*.base` or exact).
  - `nexora.filter.list_id` is emitted for allowed hits too.
  - `nexora.rpz_zone` carries the RPZ zone id, `nexora.acl.refused` is `recursion|authoritative`, and
    `nexora.upstream_raced` is an integer (emitted only when above 1).
- **Engine record fields:** `QueryRecord` gains:
  - `filter_rule_offset: u8`: the octet offset of the matched suffix in the wire name;
    `u8::MAX` = none.
  - `filter_source: FilterSource` (`None, Blocklist, Category, Allowlist, Rpz, Rewrite, Acl`).
  - `rewrite_wildcard: bool`.
  - `rpz_zone: u16`: `u16::MAX` = none.
  - `acl_refused: u8`: 0 none, 1 recursion, 2 authoritative.
  - `upstream_raced: u8`.
- **Decision cache slot:** `ListHit` gains `offset: u8`. The 64-octet slot stores it in the `kind`
  octet as `kind | offset << 2`, which is valid because cached names are at most 48 octets. The slot
  layout and size do not change.
- **`FilterView`** gains `first_allow: Box<[u16]>`, the first allow list in position order per set,
  so `FilterDecision::Allowed(ListHit)` names the allowlist.
- **Engine log levels:** the crate-level `eprintln!` shadow (`engine/src/lib.rs`, `#[macro_export]`)
  classifies a line as `warn` when it contains `error`, `failed`, `rejected`, `cannot` or `invalid`
  (ASCII case-insensitive), otherwise `info`. New code may use `nexora_engine::log_error!`,
  `log_warn!`, `log_info!` and `log_debug!`.
- **Log routing:**
  1. A REST call inserts nothing and runs `pg_notify('nexora_engine_logs', <json request>)`.
  2. Every instance's hub sends `LogRequest` to a matching subscriber.
  3. The receiving instance writes the `LogBatch` bytes into `engine_log_replies` and notifies
     `nexora_engine_logs_done` with the request id.
  4. The waiting handler on any instance reads and deletes the row.
  5. When the requester itself holds the stream, the reply short-circuits through an in-process
     waiter map.
  6. The wait is 5 s. A missing live stream (`engines.connected_instance` NULL or a stale heartbeat)
     is checked first and gives 409.
- **Parallel race:**
  - Candidates are admitted in fastest order and capped at `min(parallel_max or 8, 8)`.
  - Each attempt runs as its own `tokio::task::spawn_local` task. It owns its `Rc<Transport>`, the
    query as `Bytes`, the `Question`, the per-attempt timeout and its `Arc<Health>`, and sends
    `(index, result)` on a `tokio::sync::mpsc` channel of capacity 8.
  - The race returns on the first acceptable reply. The other tasks keep running to completion or
    timeout and settle their own health. Their replies are dropped because the receiver is gone.
  - Allocation (one task per attempt) happens only on the forwarded-miss path.
  - Health updates move from `forward` into `upstream::settle(health, result, rtt, now)`, used by
    every strategy.
- **Dashboard storage:**
  - `stats.Record` also upserts `engine_stats_rollup` keyed by `date_bin('5 minutes', now(), 'epoch')`,
    replacing the stored sample, and prunes rows older than 8 days on the existing prune tick.
  - Ranges 15m/1h/6h/24h read `engine_stats` with steps of 10 s/30 s/3 min/10 min. The 7d range reads
    the rollup with a 1 h step.
- **Account lockout:** `auth_failures` rows are counted in the same statement that inserts them.
  More than 10 rows for the lower-cased username in 15 minutes gives `auth.ErrTooManyAttempts`.
  Rows older than 15 minutes are deleted on every insert. A successful login does not clear the
  counter.
- **Help coverage:** `web/scripts/check-help.mjs` checks the files listed in each area catalogue's
  exported `pages` array. From Task 33 on, it also fails when any `web/src/pages/*.tsx` or
  `web/src/components/**/*.tsx` file with a form control is in no area. Each control is matched by
  `HelpTip` `id` or `for` props. Dynamic ids use a `data-help="<catalogue id>"` attribute.
- **Default expansion:**
  - The Filtering nav parent starts expanded when localStorage has no state.
  - Filter categories start collapsed.
- **Version:** `GET /version` is viewer, not public, because it lists engine versions.
  `NEXORA_REPOSITORY_URL` is an optional environment variable; kw sets
  `https://github.com/azrtydxb/nexora` through `mgmt.extraEnv`.

### Wave order and file ownership

A task may start when every task in earlier waves is committed. Tasks in one wave never edit the same
file. Each task's `Files:` line is its exclusive ownership for its wave.

| Wave | Tasks (parallel)                                                                                                                                                                            | Shared files it serialises                                                                                                                                                                                                                                          |
| ---- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 0    | 1 contract proto/docs, 2 OpenAPI contract, 3 e2e seed registry                                                                                                                              | `control.proto`, `gen/go`, `docs/architecture.md`; `openapi.yaml`, `gen.go`, `schema.d.ts`, both permission maps; `e2e/gui_test.go`                                                                                                                                 |
| 1    | 4 engine telemetry plumbing, 5 query-log backends/API, 6 access split mgmt, 7 account backend, 8 version and build, 9 collapsible categories GUI, 10 engine metrics API, 11 help foundation | `engine/src/server/mod.rs`, `metrics.rs`, `runtime.rs`, `upstream/mod.rs`, `lib.rs`, `main.rs` (4); querylog package, `handlers_admin.go`, `e2e/gui_test.go` (5); `handlers_dns.go`, `snapshot.go` (6); `mgmt/cmd/nexora-mgmt/main.go` (8); `web/package.json` (11) |
| 2    | 12 engine attribution, 13 engine log buffer, 14 mgmt log broker, 15 parallel race engine, 18 dashboard backend, 20 access control GUI, 21 account GUI                                       | `server/mod.rs` (12); `lib.rs`, `main.rs`, `control.rs`, `metrics.rs` (13); `mgmt/cmd/nexora-mgmt/main.go` (14); `upstream/mod.rs`, `snapshot.rs` (15); querylog package (18); `router.tsx`, `AppShell.tsx` (21)                                                    |
| 3    | 16 ACL split engine and e2e, 17 parallel mgmt and e2e, 19 query log GUI, 22 engine modal GUI, 24 navigation shell, 25 dashboard GUI                                                         | `server/mod.rs`, `snapshot.rs`, `hot_path_alloc.rs` (16); `handlers_dns.go`, `snapshot.go` (17); `router.tsx`, `AppShell.tsx` (24)                                                                                                                                  |
| 4    | 26 version footer GUI, 27 parallel settings GUI                                                                                                                                             | `AppShell.tsx` (26); `SettingsPage.tsx` (27)                                                                                                                                                                                                                        |
| 5    | 28 help resolver pages, 29 help filtering pages, 30 help zone pages, 31 help fleet pages, 32 help admin and observability pages                                                             | each owns its pages and one catalogue area file                                                                                                                                                                                                                     |
| 6    | 23 operations guide, 33 help gate and specs                                                                                                                                                 | `docs/operations.md` (23); `check-help.mjs`, `web/package.json` (33)                                                                                                                                                                                                |
| 7    | 34 kw deployment and acceptance                                                                                                                                                             | `deploy/kw/values-kw.yaml`                                                                                                                                                                                                                                          |

Dependencies beyond the wave rule (a task may name a later interface only through its `Interfaces:`
line):

- 12, 13 and 15 consume Task 4.
- 14 consumes Task 1's log messages.
- 16 consumes 4, 6 and 12.
- 17 consumes 6 and 15.
- 18 consumes 5.
- 19 consumes 5 and 12.
- 20 consumes 6.
- 21 consumes 7.
- 22 consumes 10, 13 and 14.
- 24 consumes 11 and 21.
- 25 consumes 18.
- 26 consumes 8 and 24.
- 27 consumes 17.
- 28–32 consume 11 and every earlier page change.
- 23 and 33 consume 28–32.
- 34 consumes everything.

### Spec coverage

| Spec item                                          | Tasks               |
| -------------------------------------------------- | ------------------- |
| S-1 partial name                                   | 5, 19               |
| S-2 multi-value filters                            | 2, 5, 19            |
| S-3 decision reason                                | 1, 4, 5, 12, 16, 19 |
| S-4 dashboard                                      | 1, 2, 4, 18, 25     |
| S-5 Forwarding & recursion                         | 1, 24               |
| S-6 help                                           | 11, 28–33           |
| S-7 page descriptions                              | 28–33               |
| S-8 collapsible navigation, document titles        | 24                  |
| S-9 collapsible categories                         | 9                   |
| S-10 access split                                  | 1, 2, 6, 16, 20     |
| S-11 migration without behaviour change            | 6, 16               |
| S-12 parallel strategy                             | 1, 2, 4, 15, 17, 27 |
| S-13 engine modal, metrics                         | 1, 2, 4, 10, 22     |
| S-14 engine logs                                   | 1, 2, 13, 14, 22    |
| S-15 account                                       | 2, 7, 21            |
| S-16 version                                       | 2, 8, 26, 34        |
| `TestGUICoverage`, kw acceptance, operations guide | 3, 23, 33, 34       |

## Constraints

Copied from the spec (binding for every task):

- "Hot path rules from `docs/architecture.md` stay binding: no logging, allocation or lock on the
  cache-hit path, and `cache_hit_path_does_not_allocate` keeps passing."
- "`TestGUICoverage` requires every OpenAPI operation, new ones included, to be requested by a
  Playwright screen spec. Its spec glob widens from `[012][0-9]-*.spec.ts` to `[0-9][0-9]-*.spec.ts`,
  because new specs are numbered from 25 up."
- "Every new operation has a role in `mgmt/internal/auth/permissions.go` and the same role in
  `web/src/auth/permissions.ts`."
- "Every page, the modal and the help pages work at 400 px width and in dark and light themes."
- "Every existing Go, Rust and Playwright test keeps passing; tests are updated only where M6 changes
  a label, a test id or a heading."
- "Proto fields added by M6 to existing messages use **700–799**; new messages number from 1."
- Migrations: `00600_access_split.sql`, `00601_parallel_strategy.sql`, `00602_user_profile.sql`,
  `00603_engine_log_replies.sql`, `00604_engine_stats_rollup.sql`.
- Rolling upgrades:
  - "An engine without M6 ignores the new snapshot fields."
  - "An M6 engine given a snapshot without `authoritative_acl_set` answers hosted zones to everyone."
- "Password rules: minimum 12 characters, argon2id as today, no secret in audit rows or logs."
- AI is not part of M6.
- Out of scope:
  - RRL, a drop-instead-of-REFUSED option, and allow-query per engine group or listener.
  - A per-forward-zone strategy.
  - Breached-password checks, and session or own-token lists.
  - An OTLP log backend, SSE, and audit of log reads.
  - An OpenSearch index template.

Project rules (from `.procoder/notes/implementer-brief.md` and `docs/architecture.md`):

- Builds and tests run in the kw dev pod: `scripts/dev-exec.sh '<command>'`.
  - `gen/go/nexora/control/v1/*.pb.go` are regenerated in the dev pod with its pinned plugins and
    copied back (`make proto` lines 10-13).
  - `mgmt/internal/api/gen.go` (`cd mgmt/api && oapi-codegen -config oapi-codegen.yaml openapi.yaml`)
    and `web/src/api/schema.d.ts` (`cd web && pnpm run gen:api`) are generated on the laptop.
- Implementers never commit. They list changed paths in the final report, and the lead commits with
  `scripts/commit-paths.sh`. The "commit" step of each task means "report the paths".
- TDD: write the failing test, run it, see the stated failure, implement, see it pass. Never weaken or
  delete a test.
- Run formatters and linters on changed files:
  - `cargo fmt`
  - `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings`
  - `gofmt -l`, `go vet ./...`
  - `cd web && pnpm run typecheck && pnpm run lint`
  - `scripts/pc-format.sh <md files>`
- Rust 1.97, edition 2024. Go module `github.com/piwi3910/nexora`. Playwright specs import
  `{ test, expect, env, login, logout }` from `../fixtures`.
- Every test that asserts "does not happen" first asserts the positive path in the same run.
- Mark deliberate ceilings with `debt:` comments naming the ceiling and the revisit condition.
- Fixed identifiers:
  - Error codes: `engine_disconnected`, `engine_timeout`, `engine_unsupported`,
    `invalid_current_password`, `too_many_attempts`, `managed_by_identity_provider`.
  - Audit actions: `updateCurrentUser`, `changeOwnPassword`, and `updateAccessControl` (unchanged).
  - Metrics: `nexora_acl_refused_total{acl}`, `nexora_upstream_race_wins_total{upstream}`,
    `nexora_upstream_race_duration_seconds`, `nexora_log_lines_dropped_total`.
  - localStorage keys: `nexora-nav-filtering`, `nexora-categories-expanded`.
  - Test ids named in each task.

## Task 1: Contract fields 700–799 and the M6 architecture text

Files:

- `proto/nexora/control/v1/control.proto`: M6 fields and log messages.
- `gen/go/nexora/control/v1/control.pb.go` and `gen/go/nexora/control/v1/control_grpc.pb.go`:
  regenerated.
- `docs/architecture.md`: the settled M6 design.
- `engine/tests/server_pipeline.rs`, `engine/tests/policy_pipeline.rs`,
  `engine/tests/graceful_shutdown.rs`, `engine/tests/snapshot_apply.rs`, `engine/tests/rpz_pipeline.rs`,
  `engine/tests/authoritative_pipeline.rs`: add `..Default::default()` to `ResolverConfig` literals,
  which gain a field.
- `engine/src/telemetry/metrics.rs`: only `..Default::default()` in the `Stats`, `UpstreamStatus` and
  `RecursionStats` literals.
- `mgmt/internal/control/contract_m6_test.go`: created; the round-trip test.

Interfaces: produces the proto names used by Tasks 4, 6, 10, 12–18 (Rust `nexora_engine::proto::*`, Go
`controlv1.*`):

```proto
// ConfigSnapshot
repeated string authoritative_allow_cidrs = 700; // hosted-zone query ACL default; used only when authoritative_acl_set
bool authoritative_acl_set = 701;                // false (pre-M6 management): hosted zones answer every client
// AuthZone
repeated string allow_query_cidrs = 700;         // empty: ConfigSnapshot.authoritative_allow_cidrs
repeated string update_allow_cidrs = 701;        // empty: any source (TSIG still required)
// UpstreamStrategy
UPSTREAM_STRATEGY_PARALLEL = 3;
// ResolverConfig
uint32 parallel_max = 700;                       // 0 = every candidate; engines cap at 8
// EngineMessage oneof
LogBatch log_batch = 700;
// ServerMessage oneof
LogRequest log_request = 700;
// Stats
map<string, uint64> queries_by_rcode = 700;      // NOERROR FORMERR SERVFAIL NXDOMAIN NOTIMP REFUSED other
map<string, uint64> queries_by_transport = 701;  // udp tcp dot doh doq
repeated uint64 miss_duration_bucket_counts = 702; // cumulative, bounds = duration_bucket_bounds_us, cache miss/stale only
uint64 filter_rewritten_total = 703;
map<string, uint64> answers_by_route = 704;      // cache authoritative blocked rewritten rpz forwarded recursive forward_zone
uint64 resolution_failures_total = 705;
double process_cpu_seconds_total = 706;
uint64 process_resident_bytes = 707;
uint64 memory_limit_bytes = 708;                 // 0 = no cgroup limit
map<string, uint64> open_connections = 709;      // tcp dot doh doq
int64 started_unix_ms = 710;
map<string, uint64> acl_refused = 711;           // recursion authoritative
int64 tls_certificate_not_after_unix = 712;      // 0 = no DNS serving certificate
uint64 log_lines_dropped_total = 713;
repeated uint64 race_duration_bucket_counts = 714; // cumulative, same bounds
// RecursionStats
uint64 upstream_timeouts = 700;
// UpstreamStatus
uint64 race_wins_total = 700;

enum LogLevel { LOG_LEVEL_UNSPECIFIED = 0; LOG_LEVEL_ERROR = 1; LOG_LEVEL_WARN = 2; LOG_LEVEL_INFO = 3; LOG_LEVEL_DEBUG = 4; }
message LogRequest { string request_id = 1; uint64 after_seq = 2; LogLevel min_level = 3; uint32 limit = 4; string contains = 5; }
message LogLine { uint64 seq = 1; int64 unix_ms = 2; LogLevel level = 3; string message = 4; }
message LogBatch { string request_id = 1; repeated LogLine lines = 2; uint64 last_seq = 3; uint64 oldest_seq = 4; }
```

- [ ] Create `mgmt/internal/control/contract_m6_test.go`:
  ```go
  package control_test

  import (
  	"testing"

  	"google.golang.org/protobuf/proto"

  	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
  )

  // M6 fields use 700-799 and survive a round trip; a renumbering or a missing field fails here.
  func TestContractM6FieldsRoundTrip(t *testing.T) {
  	snap := &controlv1.ConfigSnapshot{
  		AuthoritativeAllowCidrs: []string{"10.0.0.0/8"}, AuthoritativeAclSet: true,
  		Resolver:  &controlv1.ResolverConfig{Strategy: controlv1.UpstreamStrategy_UPSTREAM_STRATEGY_PARALLEL, ParallelMax: 2},
  		AuthZones: []*controlv1.AuthZone{{Name: "kw.test.", AllowQueryCidrs: []string{"192.168.0.0/16"}, UpdateAllowCidrs: []string{"127.0.0.1/32"}}},
  	}
  	stats := &controlv1.Stats{
  		QueriesByRcode: map[string]uint64{"NOERROR": 3}, QueriesByTransport: map[string]uint64{"udp": 3},
  		MissDurationBucketCounts: []uint64{1}, FilterRewrittenTotal: 1, AnswersByRoute: map[string]uint64{"cache": 2},
  		ResolutionFailuresTotal: 1, ProcessCpuSecondsTotal: 1.5, ProcessResidentBytes: 1 << 20, MemoryLimitBytes: 1 << 30,
  		OpenConnections: map[string]uint64{"dot": 1}, StartedUnixMs: 42, AclRefused: map[string]uint64{"recursion": 1},
  		TlsCertificateNotAfterUnix: 99, LogLinesDroppedTotal: 5, RaceDurationBucketCounts: []uint64{2},
  		Recursion: &controlv1.RecursionStats{UpstreamTimeouts: 7},
  		Upstreams: []*controlv1.UpstreamStatus{{Name: "a", RaceWinsTotal: 4}},
  	}
  	req := &controlv1.ServerMessage{Msg: &controlv1.ServerMessage_LogRequest{LogRequest: &controlv1.LogRequest{
  		RequestId: "r1", AfterSeq: 9, MinLevel: controlv1.LogLevel_LOG_LEVEL_WARN, Limit: 1000, Contains: "snapshot"}}}
  	batch := &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_LogBatch{LogBatch: &controlv1.LogBatch{
  		RequestId: "r1", Lines: []*controlv1.LogLine{{Seq: 10, UnixMs: 1, Level: controlv1.LogLevel_LOG_LEVEL_INFO, Message: "m"}}, LastSeq: 10, OldestSeq: 1}}}
  	for _, m := range []proto.Message{snap, stats, req, batch} {
  		b, err := proto.Marshal(m)
  		if err != nil {
  			t.Fatal(err)
  		}
  		back := m.ProtoReflect().New().Interface()
  		if err := proto.Unmarshal(b, back); err != nil || !proto.Equal(m, back) {
  			t.Fatalf("round trip of %T: %v", m, err)
  		}
  	}
  	fields := map[string]int32{
  		"authoritative_allow_cidrs": 700, "authoritative_acl_set": 701,
  	}
  	desc := snap.ProtoReflect().Descriptor().Fields()
  	for name, num := range fields {
  		if f := desc.ByName(protoreflectName(name)); f == nil || int32(f.Number()) != num {
  			t.Fatalf("ConfigSnapshot.%s must be field %d", name, num)
  		}
  	}
  }
  ```
  and add the helper at the end of the same file:
  ```go
  func protoreflectName(s string) protoreflect.Name { return protoreflect.Name(s) }
  ```
  with the import `"google.golang.org/protobuf/reflect/protoreflect"`.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/control -run TestContractM6FieldsRoundTrip -count=1'`
      and expect FAIL: it does not compile, with `unknown field AuthoritativeAllowCidrs in struct literal`.
- [ ] Edit `proto/nexora/control/v1/control.proto`:
  - Add every field and message of this task's Interfaces block to the named messages, each with a
    trailing `// M6` comment.
  - Above the new messages, add the comment block
    `// M6 operator UX: fields added to existing messages use 700-799.`
- [ ] Regenerate in the dev pod and copy back:
      `scripts/dev-exec.sh 'protoc -I proto --go_out=gen/go --go_opt=paths=source_relative --go-grpc_out=gen/go --go-grpc_opt=paths=source_relative proto/nexora/control/v1/control.proto'`.
      Then `kubectl --context kw -n nexora-dev exec deploy/toolbox -c toolbox -- tar -C /work/nexora -cf - gen/go | tar -xf -`
      (the context, namespace and deployment `scripts/dev-exec.sh` uses).
- [ ] Add `..Default::default()` as the last field of every `ResolverConfig { .. }` literal in the six
      engine test files listed under Files. Add it too to the `Stats { .. }`, `UpstreamStatus { .. }` and
      `RecursionStats { .. }` literals in `engine/src/telemetry/metrics.rs` (lines 769, 793, 828 before the
      edit).
- [ ] Run
      `scripts/dev-exec.sh 'go test ./mgmt/internal/control -run TestContractM6FieldsRoundTrip -count=1 && cargo build --locked -p nexora-engine --all-targets'`
      and expect PASS and a clean build.
- [ ] Edit `docs/architecture.md`:
  - In `## Contract`, after the filter categories bullet, add:
    ```markdown
    - M6 operator UX: fields added to existing messages use 700-799:
      `ConfigSnapshot.authoritative_allow_cidrs` (700) and `authoritative_acl_set` (701),
      `AuthZone.allow_query_cidrs` (700) and `update_allow_cidrs` (701), `UPSTREAM_STRATEGY_PARALLEL`
      and `ResolverConfig.parallel_max` (700), `Stats` 700-714 (per-rcode, per-transport, uncached
      latency buckets, rewrites, answers by route, resolution failures, process CPU and memory,
      open connections, start time, ACL refusals, certificate expiry, dropped log lines, race
      latency buckets), `RecursionStats.upstream_timeouts` and `UpstreamStatus.race_wins_total`
      (700). `ServerMessage.log_request` / `EngineMessage.log_batch` (700) read the engine's log
      ring buffer on demand (`LogRequest`, `LogBatch`, `LogLine`, `LogLevel`).
    ```
  - Replace the `### ACL` section body with:
    ```markdown
    Two client ACLs. Recursion access: queries for names that are not hosted (cache, forwarding,
    recursion, rewrites, filtering, RPZ) from addresses outside `acl_allow_cidrs` (the management
    plane's `access_control.allow_cidrs` followed by the engine group's `extra_acl_cidrs`) get
    REFUSED. Authoritative access: a query matching a hosted zone is checked against the zone's
    `allow_query_cidrs`, or when empty `authoritative_allow_cidrs`, and refused outside it; a
    snapshot without `authoritative_acl_set` allows every client. RA is set only for clients the
    recursion ACL allows. Transfers keep `TransferPolicy`; UPDATE additionally requires the sender in
    `update_allow_cidrs` when non-empty. Refusals count in `nexora_acl_refused_total{acl}` and carry
    `nexora.acl.refused` in the query log. The management plane seeds recursion access with
    127.0.0.0/8, ::1/128, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, fc00::/7,
    fe80::/10 and authoritative access with 0.0.0.0/0, ::/0.
    ```
  - In `### Upstreams (forwarding)`, replace the strategy bullet with:
    ```markdown
    - Strategy `ordered` (first healthy by position), `fastest` (lowest EWMA RTT among healthy, alpha
      0.2) or `parallel` (the admitted candidates in fastest order, at most `parallel_max` or 8, are
      queried at once; the first NOERROR/NXDOMAIN reply wins, other rcodes and errors win only when
      every attempt failed; unfinished attempts are drained off the reply path and still update
      health; `nexora_upstream_race_wins_total{upstream}`, `nexora_upstream_race_duration_seconds`).
    ```
  - In `### Telemetry`, append to the attribute list:
    `` `nexora.filter.source`, `nexora.filter.rule` (blocked, allowed, rewritten, RPZ and ACL outcomes), `nexora.filter.list_id` also for allowed queries, `nexora.rpz_zone`, `nexora.acl.refused`, `nexora.upstream_raced` ``.
  - Add a bullet:
    ```markdown
    - Engine log: every `eprintln!` in the engine crate also appends to a ring of 2,000 lines (512
      octets each, 100 lines/s with a burst of 200, dropped lines counted in
      `nexora_log_lines_dropped_total`), with join tokens, API tokens, PEM blocks and
      `secret=`/`password=` values masked. Nothing on the query path logs.
    ```
  - In `## GUI`, replace the routes sentence with:
    ```markdown
    Routes: `/login`, `/setup`, `/` (dashboard), `/query-log`, `/resolution` ("Forwarding &
    recursion"; `/upstreams` redirects), `/access-control`, `/filtering` ("Blocklist / allowlist"),
    `/filtering/categories`, `/policies`, `/rewrites`, `/zones`, `/zones/tsig-keys`,
    `/zones/:zoneId`, `/rpz`, `/dnssec`, `/engines` (`?engine=<id>` opens the engine modal),
    `/engines/groups/:id`, `/engines/nodes/:id`, `/engines/rollouts/:id`, `/users`, `/api-tokens`,
    `/audit`, `/settings`, `/account`, `/help`, `/help/:topic`. Help text lives in
    `web/src/help/catalog/` and `web/src/help/topics/`; `pnpm lint` fails on a form control without
    help.
    ```
  - In `## Management plane`, add:
    ```markdown
    - M6: engine logs are read through `pg_notify('nexora_engine_logs', request)`; the instance
      holding the engine's stream sends `LogRequest` and stores the `LogBatch` in the unlogged table
      `engine_log_replies` (notify `nexora_engine_logs_done`). `engine_stats_rollup` keeps the newest
      sample per engine per 5 minutes for 8 days. `auth_failures` counts failed logins and password
      changes per username and client address (more than 10 in 15 minutes: 429; the key includes the client IP so a remote attacker cannot lock out the admin). `NEXORA_REPOSITORY_URL` is shown in
      the GUI version details.
    ```
- [ ] Run `grep -c '700-799' docs/architecture.md; grep -c 'authoritative_allow_cidrs' docs/architecture.md; grep -c '/resolution' docs/architecture.md`
      and expect three non-zero counts. Run `scripts/pc-format.sh docs/architecture.md`.
- [ ] Report the paths under Files. The lead commits
      `contract: M6 proto fields 700-799, log messages, architecture`.

## Task 2: OpenAPI contract, permissions, generated clients and stubs

Files:

- `mgmt/api/openapi.yaml`: every M6 schema and operation.
- `mgmt/internal/api/gen.go`, `web/src/api/schema.d.ts`: regenerated.
- `mgmt/internal/auth/permissions.go`, `web/src/auth/permissions.ts`: roles.
- `mgmt/internal/api/server.go`: `Deps.EngineLogs`.
- `mgmt/internal/api/handlers_admin.go`: `SearchQueryLog` compiles against the array parameters.
- Created with 501 stubs, owned by later tasks: `mgmt/internal/api/dashboard_m6.go` (Task 18),
  `mgmt/internal/api/engine_metrics.go` (Task 10), `mgmt/internal/api/engine_logs.go` (Task 14),
  `mgmt/internal/api/account.go` (Task 7), `mgmt/internal/api/version.go` (Task 8).
- `mgmt/internal/api/m6_contract_test.go`: created.
- `web/src/lib/preferences.ts`: created; the preferences reader used by Tasks 19 and 21.

Interfaces (OpenAPI component and operation names are the Go and TS names after generation):

- **Query parameters of `searchQueryLog`:**
  - `qtype`, `rcode`, `cache`, `filter`, `category`, `source`, `list_id`, `policy_group` and
    `engine_id` are each
    `{ in: query, style: form, explode: true, schema: { type: array, maxItems: 32, items: { type: string } } }`.
    `cache` items use enum `[hit, miss, stale, none, auth]`, `filter` items
    `[none, blocked, allowed, rewritten]`, and `source` items
    `[blocklist, category, allowlist, rpz, rewrite, acl]`. `policy_group` items are a UUID or
    `global`.
  - `name` gets the description "Case-insensitive substring of the query name; a trailing dot is
    ignored."
  - Responses gain `"400"`.
- **`QueryLogRecord`** gains required `source` (enum above plus `""`), `list_name`, `rule`,
  `policy_group_id`, `policy_group_name`, `rpz_zone_id`, `rpz_zone_name`, `rpz_action`,
  `rewrite_answer` (strings) and `upstreams_raced` (integer). `cache` enum adds `auth`.
- **`AccessControl`** gains `authoritative_allow_cidrs: { type: array, items: { type: string } }`.
  It is not required, so an older client's PUT keeps the current value, and every response fills it.
- **Zones:**
  - `Zone` gains required `allow_query_cidrs` (array of string).
  - `ZoneCreate` and `ZoneUpdate` gain optional `allow_query_cidrs`.
  - `ZoneUpdatePolicy` gains optional `allow_cidrs` (array of string, "Sources allowed to send
    updates; empty allows any source. TSIG is always required.").
- **`ResolverSettings`:** `strategy` enum `[ordered, fastest, parallel]`, and
  `parallel_max: { type: integer, minimum: 0, maximum: 8 }` (not required; responses always fill it).
- **`User`** gains required `display_name` (string), `last_login_at` (`[string, "null"]` date-time)
  and `preferences` (`$ref: UserPreferences`).
- **`UserPreferences`:**
  `{ required: [theme, time_zone, clock_24h, querylog_live], properties: { theme: { enum: [system, light, dark] }, time_zone: { type: string, maxLength: 64, description: "IANA name; empty uses the browser" }, clock_24h: { type: boolean }, querylog_live: { type: boolean } } }`.
- **`CurrentUserUpdate`:**
  `{ required: [revision], properties: { revision: int64, email: string, display_name: { type: string, maxLength: 64 }, preferences: $ref UserPreferences } }`.
- **`PasswordChange`:**
  `{ required: [current_password, new_password], properties: { current_password: string, new_password: { type: string, minLength: 12 }, revoke_other_sessions: { type: boolean, default: true } } }`.
- **`VersionInfo`:**
  `{ required: [version, commit, build_date, repository_url, engines], properties: { version, commit, build_date, repository_url: string, engines: array of { required: [version, count], version: string, count: integer } } }`.
- **`DashboardSeries`:**
  `{ required: [range, step_seconds, points, engines] }`.
  - `range` is an enum of 15m, 1h, 6h, 24h, 7d.
  - `points[]` requires `at, qps, qps_by_transport, qps_by_rcode, p50_ms, p95_ms, p99_ms, miss_p50_ms, miss_p95_ms, miss_p99_ms, cache_hit_ratio, cache_miss_ratio, cache_stale_ratio, blocked_qps, rewritten_qps, blocked_by_category, answers_by_route, recursion_upstream_qps, recursion_timeouts_qps, lame_marked, resolution_failures_qps, dnssec_secure_qps, dnssec_insecure_qps, dnssec_bogus_qps`.
    The `*_by_*` fields are objects with `additionalProperties: { type: number }`.
  - `engines[]` requires `engine_id, node_name, cache_entries, cache_bytes, filter_index_bytes`.
- **`DashboardTop`:**
  `{ required: [range, available, domains, blocked_domains, clients, categories] }`. Each list is an
  array of `{ required: [key, count], key: string, count: int64 }`.
- **`DashboardHealth`:**
  `{ required: [engines, groups, alerts] }`.
  - `engines[]` requires `id, node_name, engine_group_name, status, qps, p99_ms, cache_hit_ratio, filter_index_bytes, applied_version, target_version`.
  - `groups[]` requires `id, name, engines, connected`.
  - `alerts[]` requires `kind` (enum engine_disconnected, category_stale, upstream_down,
    certificate_expiring, trust_anchor_refresh_failed, export_dropped), `severity` (enum warning,
    critical), `subject` and `message`.
- **`EngineMetrics`:**
  `{ required: [window, samples, upstreams, started_at, restarts, filter_index] }`.
  - `samples[]` requires `at, qps, p50_ms, p99_ms, cache_hit_ratio, servfail_ratio, nxdomain_ratio, refused_ratio, blocked_qps, cpu_cores, resident_bytes, memory_limit_bytes, connections`.
    `connections` is an object with number values.
  - `upstreams[]` is `{ name, samples: [{ at, rtt_ms, failures_per_second, race_wins_per_second }] }`.
  - `started_at` is `[string, "null"]`. `filter_index` uses the same shape as `EngineStats.filter_index`.
- **`EngineLogs`:**
  `{ required: [engine_id, lines, last_seq, oldest_seq], lines: array of { required: [seq, time, level, message], seq: int64, time: date-time, level: { enum: [error, warn, info, debug] }, message: string } }`.
- **Operations:**
  - `getDashboardSeries`: `GET /dashboard/series`, `range` query enum, 200/400.
  - `getDashboardTop`: `GET /dashboard/top`, `range` plus `limit` (1..50, default 10), 200/400.
  - `getDashboardHealth`: `GET /dashboard/health`, 200.
  - `getEngineMetrics`: `GET /engines/{id}/metrics`, `window` enum [5m, 1h, 24h], 200/400/404.
  - `getEngineLogs`: `GET /engines/{id}/logs`, with `after` (int64, min 0), `level` (enum
    error|warn|info|debug), `q` (string, maxLength 128) and `limit` (1..1000, default 1000).
    Responses 200/404/409/501/504.
  - `updateCurrentUser`: `PUT /auth/me`, body `CurrentUserUpdate`, 200 `User`, 400/401/409.
  - `changeOwnPassword`: `POST /auth/me/password`, body `PasswordChange`, 204/400/401/403/409/429.
  - `getVersion`: `GET /version`, 200 `VersionInfo`.
- **Roles** in both permission maps:

  | Role           | Operations                                                                                                                                |
  | -------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
  | `RoleViewer`   | `getDashboardSeries`, `getDashboardTop`, `getDashboardHealth`, `getEngineMetrics`, `updateCurrentUser`, `changeOwnPassword`, `getVersion` |
  | `RoleOperator` | `getEngineLogs`                                                                                                                           |

- **Go:**
  - `api.Deps.EngineLogs EngineLogReader`.
  - `type EngineLogReader interface { Read(ctx context.Context, engineID uuid.UUID, req *controlv1.LogRequest) (*controlv1.LogBatch, error) }`
    in `mgmt/internal/api/engine_logs.go`.
  - Sentinel errors in `mgmt/internal/api/engine_logs.go`: `ErrEngineDisconnected`,
    `ErrEngineTimeout`, `ErrEngineUnsupported`, which the stub maps with `coded()`.
- **TS:**
  - `web/src/lib/preferences.ts` exports
    `type Preferences = Schemas["UserPreferences"]`, `defaultPreferences: Preferences`,
    `usePreferences(): Preferences` (reads the `["me"]` query data through `useCurrentUser`) and
    `formatTimestamp(d: Date, p: Preferences, withDate?: boolean): string`.

- [ ] Create `mgmt/internal/api/m6_contract_test.go`:
  ```go
  package api_test

  import (
  	"net/http"
  	"testing"
  )

  // Every M6 operation is routed, authenticated and answers (a 501 stub until its task lands, never a 404).
  func TestM6OperationsAreRoutedAndAuthenticated(t *testing.T) {
  	e := newAPI(t)
  	anon := e.client(t)
  	paths := []struct{ method, path string }{
  		{"GET", "/dashboard/series?range=1h"}, {"GET", "/dashboard/top?range=1h"}, {"GET", "/dashboard/health"},
  		{"GET", "/engines/00000000-0000-0000-0000-00000000abcd/metrics?window=5m"},
  		{"GET", "/engines/00000000-0000-0000-0000-00000000abcd/logs"},
  		{"PUT", "/auth/me"}, {"POST", "/auth/me/password"}, {"GET", "/version"},
  	}
  	for _, p := range paths {
  		if code := anon.do(p.method, p.path, map[string]any{}, nil); code != http.StatusUnauthorized {
  			t.Fatalf("%s %s without a session -> %d, want 401", p.method, p.path, code)
  		}
  	}
  	admin := e.client(t)
  	if code := admin.do("POST", "/setup", map[string]any{"token": e.setup, "username": "admin", "email": "a@example.test", "password": "admin-password-1"}, nil); code != http.StatusCreated {
  		t.Fatalf("setup -> %d", code)
  	}
  	for _, p := range paths {
  		if code := admin.do(p.method, p.path, map[string]any{}, nil); code == http.StatusNotFound || code == http.StatusMethodNotAllowed {
  			t.Fatalf("%s %s is not routed (%d)", p.method, p.path, code)
  		}
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'make webui-placeholder && go test ./mgmt/internal/api -run "TestM6OperationsAreRoutedAndAuthenticated|TestPermissionsCoverEveryOperation" -count=1'`
      and expect FAIL: `GET /dashboard/series without a session -> 404, want 401`.
- [ ] Edit `mgmt/api/openapi.yaml` with every schema and operation in Interfaces:
  - Place the query-log changes in the existing `/query-log` block (line 2717) and the schema changes
    in the existing schemas.
  - Add the new paths after `/dashboard` (line 1969), `/engines/{id}/stats` (line 3819), `/auth/me`
    (line 1906) and `/health` (line 1806).
  - Error responses use `$ref: "#/components/responses/Error"`.
- [ ] Regenerate on the laptop: `cd mgmt/api && oapi-codegen -config oapi-codegen.yaml openapi.yaml`
      and `cd web && pnpm run gen:api`.
- [ ] Add the eight operation ids with their roles to `mgmt/internal/auth/permissions.go` (the viewer
      block near line 48 and the operator block near line 91) and to `web/src/auth/permissions.ts`.
- [ ] In `mgmt/internal/api/server.go` add the field to `Deps`:
      `EngineLogs EngineLogReader // nil: getEngineLogs answers 501 engine_unsupported`.
- [ ] Create the five stub files. Each stub returns `nil, coded(http.StatusNotImplemented, "not_implemented", "not implemented yet")`. For example, `mgmt/internal/api/version.go`:
  ```go
  package api

  import (
  	"context"
  	"net/http"
  )

  func (h *handlers) GetVersion(ctx context.Context, _ GetVersionRequestObject) (GetVersionResponseObject, error) {
  	return nil, coded(http.StatusNotImplemented, "not_implemented", "not implemented yet")
  }
  ```
  The other files hold, with the generated request and response object names:
  - `dashboard_m6.go`: `GetDashboardSeries`, `GetDashboardTop`, `GetDashboardHealth`.
  - `engine_metrics.go`: `GetEngineMetrics`.
  - `engine_logs.go`: `GetEngineLogs`, plus the `EngineLogReader` interface and the three sentinel
    errors.
  - `account.go`: `UpdateCurrentUser`, `ChangeOwnPassword`.
- [ ] In `mgmt/internal/api/handlers_admin.go` `SearchQueryLog`, keep the current single-value
      behaviour until Task 5. Map each new array parameter with
      `first := func(v *[]string) string { if v == nil || len(*v) == 0 { return "" }; return strings.TrimSpace((*v)[0]) }`,
      and fill the new record fields with empty values.
- [ ] Create `web/src/lib/preferences.ts`:
  ```ts
  import type { components } from "@/api/schema";
  import { useCurrentUser } from "@/auth/AuthProvider";

  export type Preferences = components["schemas"]["UserPreferences"];

  export const defaultPreferences: Preferences = {
    theme: "system",
    time_zone: "",
    clock_24h: false,
    querylog_live: true,
  };

  /** The signed-in user's preferences, or the defaults before the profile has loaded. */
  export function usePreferences(): Preferences {
    const { user } = useCurrentUser();
    return { ...defaultPreferences, ...(user?.preferences ?? {}) };
  }

  /** A timestamp in the user's time zone and clock; an unknown zone falls back to the browser's. */
  export function formatTimestamp(
    d: Date,
    p: Preferences,
    withDate = false,
  ): string {
    const opts: Intl.DateTimeFormatOptions = {
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hour12: !p.clock_24h,
      ...(withDate
        ? { year: "numeric", month: "2-digit", day: "2-digit" }
        : {}),
    };
    try {
      return new Intl.DateTimeFormat(undefined, {
        ...opts,
        timeZone: p.time_zone || undefined,
      }).format(d);
    } catch {
      return new Intl.DateTimeFormat(undefined, opts).format(d);
    }
  }
  ```
  Confirm the generated type import path by checking how `web/src/api/client.ts` imports `schema`, and
  use the same import.
- [ ] Run
      `scripts/dev-exec.sh 'make webui-placeholder && go build ./... && go test ./mgmt/internal/api -run "TestM6OperationsAreRoutedAndAuthenticated|TestPermissionsCoverEveryOperation" -count=1'`
      and `cd web && pnpm run typecheck && pnpm run lint`. Expect PASS.
- [ ] Report the paths. Commit message: `api: M6 OpenAPI contract, permissions, stubs`.

## Task 3: GUI e2e seed registry and wider spec glob

Files:

- `e2e/gui_test.go`: widen the glob and run the registered seeds.
- `e2e/gui_seed_test.go`: created; the registry type and function.

Interfaces: produces the following, which Tasks 19, 20, 21, 22, 25, 26, 27 and 33 use from their own
`e2e/gui_seed_<feature>_test.go` files through `func init() { registerGUISeed(seedX) }`:

```go
type guiSeedEnv struct {
	T      *testing.T
	Env    *harness.Env
	Mgmt   *harness.Mgmt
	Admin  *harness.API
	Engine *harness.Engine // gui-engine
	Web    *harness.HTTPFixture
	DNS    string // fixture upstream UDP address
	Vars   map[string]string
}
func registerGUISeed(f func(guiSeedEnv))
```

- [ ] Create `e2e/gui_seed_test.go`:
  ```go
  package e2e

  import (
  	"testing"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  // guiSeedEnv is what TestGUICoverage hands each registered seed before the screen specs run.
  type guiSeedEnv struct {
  	T      *testing.T
  	Env    *harness.Env
  	Mgmt   *harness.Mgmt
  	Admin  *harness.API
  	Engine *harness.Engine
  	Web    *harness.HTTPFixture
  	DNS    string
  	Vars   map[string]string
  }

  var guiSeeds []func(guiSeedEnv)

  // registerGUISeed adds a seed; seeds run in file-name order (Go runs init functions by file name).
  func registerGUISeed(f func(guiSeedEnv)) { guiSeeds = append(guiSeeds, f) }

  func TestGUISeedRegistryRunsInOrder(t *testing.T) {
  	saved := guiSeeds
  	defer func() { guiSeeds = saved }()
  	guiSeeds = nil
  	var got []int
  	registerGUISeed(func(guiSeedEnv) { got = append(got, 1) })
  	registerGUISeed(func(guiSeedEnv) { got = append(got, 2) })
  	runGUISeeds(guiSeedEnv{T: t, Vars: map[string]string{}})
  	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
  		t.Fatalf("seeds ran as %v", got)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'go test ./e2e -run TestGUISeedRegistryRunsInOrder -count=1'` and expect
      FAIL: `undefined: runGUISeeds`.
- [ ] Add to `e2e/gui_seed_test.go`:
  ```go
  func runGUISeeds(s guiSeedEnv) {
  	for _, f := range guiSeeds {
  		f(s)
  	}
  }
  ```
- [ ] In `e2e/gui_test.go` `TestGUICoverage`:
  - Name the fixture upstream `fx`, which it already is.
  - Just before `time.Sleep(12 * time.Second)`, insert
    `runGUISeeds(guiSeedEnv{T: t, Env: env, Mgmt: mgmt, Admin: admin, Engine: eng, Web: web, DNS: fx.UDP, Vars: vars})`.
  - Replace the glob `"web/e2e/screens/[012][0-9]-*.spec.ts"` with `"web/e2e/screens/[0-9][0-9]-*.spec.ts"`.
- [ ] Run
      `scripts/dev-exec.sh 'go vet ./e2e && go test ./e2e -run TestGUISeedRegistryRunsInOrder -count=1'`
      and expect PASS.
- [ ] Report the paths. Commit message: `e2e: GUI seed registry and screen spec glob 00-99`.

## Task 4: Engine telemetry plumbing for M6

Files:

- `engine/src/telemetry/querylog.rs`: new `QueryRecord` fields and `FilterSource`.
- `engine/src/telemetry/otlp.rs`: new attributes and the `log_record` signature.
- `engine/src/telemetry/metrics.rs`: counters, M6 `Stats` fields and Prometheus names.
- `engine/src/telemetry/process.rs`: created; process CPU, RSS and cgroup limit readers.
- `engine/src/telemetry/mod.rs`: `pub mod process;`.
- `engine/src/server/mod.rs`: `Scope::finish` counts by route, uncached latency and ACL refusals;
  `note_answer` copies `raced`.
- `engine/src/server/tcp.rs`: TCP connection guard.
- `engine/src/upstream/mod.rs`: `Strategy::Parallel { max }` (served as `Fastest` until Task 15),
  `Forwarded.raced`, `Health.race_wins`, `RACE` histogram.
- `engine/src/runtime.rs`: map `UPSTREAM_STRATEGY_PARALLEL` and `parallel_max`.
- `engine/src/lib.rs`: `pub const COMMIT`.
- `engine/build.rs`: `rerun-if-env-changed`.
- `engine/src/main.rs`: `--version` shows the commit.
- `engine/tests/telemetry_export.rs`: record literal and new tests.

Interfaces (consumed by Tasks 12, 13, 15, 16):

```rust
// crate::telemetry::querylog
pub const NO_RULE: u8 = u8::MAX;
pub const NO_RPZ_ZONE: u16 = u16::MAX;
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum FilterSource { None, Blocklist, Category, Allowlist, Rpz, Rewrite, Acl }
impl FilterSource { pub fn as_str(self) -> &'static str } // "", "blocklist", "category", "allowlist", "rpz", "rewrite", "acl"
pub const ACL_NONE: u8 = 0; pub const ACL_RECURSION: u8 = 1; pub const ACL_AUTHORITATIVE: u8 = 2;
// QueryRecord gains (after filter_generation):
pub filter_source: FilterSource, pub filter_rule_offset: u8, pub rewrite_wildcard: bool,
pub rpz_zone: u16, pub acl_refused: u8, pub upstream_raced: u8,
// crate::telemetry::otlp
pub fn log_record(r: &QueryRecord, upstream_name: &str, engine_id: &str, policy_group: &str,
                  filter_list: Option<&ListMeta>, rpz_zone_id: &str) -> LogRecord;
// crate::telemetry::metrics
pub const ANSWER_ROUTES: [&str; 8] = ["cache", "authoritative", "blocked", "rewritten", "rpz", "forwarded", "recursive", "forward_zone"];
impl WorkerCounters { pub fn observe_record(&self, r: &QueryRecord); } // route, miss latency, acl refusals, rewrites
// crate::telemetry::process
pub fn cpu_seconds() -> f64; pub fn resident_bytes() -> u64; pub fn memory_limit_bytes() -> u64; pub fn started_unix_ms() -> i64;
// crate::upstream
pub enum Strategy { Ordered, Fastest, Parallel { max: u8 } }
pub struct Forwarded { pub response: Bytes, pub upstream_index: usize, pub rtt: Duration, pub raced: u8 }
pub struct Health { /* existing */ pub race_wins: AtomicU64 }
pub static RACE: RaceHistogram; // observe(Duration), cumulative(&self) -> Vec<u64>
// crate
pub const COMMIT: &str; // option_env!("NEXORA_COMMIT") or ""
```

- [ ] Append to `engine/tests/telemetry_export.rs`, and add the six new fields to the `record` helper
      with `FilterSource::None, NO_RULE, false, NO_RPZ_ZONE, 0, 1`:
  ```rust
  #[test]
  fn m6_attribution_attributes_are_emitted_only_when_set() {
      use nexora_engine::filter::index::{ListKind, ListMeta};
      use nexora_engine::telemetry::querylog::{ACL_AUTHORITATIVE, FilterSource};
      let attrs = |lr: &opentelemetry_proto::tonic::logs::v1::LogRecord| {
          lr.attributes
              .iter()
              .map(|kv| (kv.key.clone(), format!("{:?}", kv.value)))
              .collect::<std::collections::BTreeMap<_, _>>()
      };
      let plain = attrs(&log_record(&record(0), "fx", "e", "", None, ""));
      for k in ["nexora.filter.source", "nexora.filter.rule", "nexora.rpz_zone", "nexora.acl.refused", "nexora.upstream_raced"] {
          assert!(!plain.contains_key(k), "{k} on an unfiltered record");
      }
      let meta = ListMeta { id: "allowlist".into(), category: "".into(), category_slot: 0, kind: ListKind::Allow, invalid_lines: 0 };
      let mut allowed = record(0);
      allowed.filter = FilterOutcome::Allowed;
      allowed.filter_source = FilterSource::Allowlist;
      allowed.filter_list = 0;
      allowed.filter_rule_offset = 8; // "\x07example\x03com" -> "com"
      let a = attrs(&log_record(&allowed, "fx", "e", "", Some(&meta), ""));
      assert!(a["nexora.filter.source"].contains("allowlist"), "{a:?}");
      assert!(a["nexora.filter.rule"].contains("\"com\""), "{a:?}");
      assert!(a["nexora.filter.list_id"].contains("allowlist"), "{a:?}");
      let mut rewrite = record(0);
      rewrite.filter = FilterOutcome::Rewritten;
      rewrite.filter_source = FilterSource::Rewrite;
      rewrite.filter_rule_offset = 8;
      rewrite.rewrite_wildcard = true;
      assert!(attrs(&log_record(&rewrite, "fx", "e", "", None, ""))["nexora.filter.rule"].contains("*.com"));
      let mut refused = record(5);
      refused.filter_source = FilterSource::Acl;
      refused.acl_refused = ACL_AUTHORITATIVE;
      refused.upstream_raced = 3;
      let r = attrs(&log_record(&refused, "", "e", "", None, "rpz-zone-id"));
      assert!(r["nexora.acl.refused"].contains("authoritative") && r["nexora.filter.source"].contains("acl"), "{r:?}");
      assert!(r["nexora.upstream_raced"].contains("IntValue(3)"), "{r:?}");
      assert!(r["nexora.rpz_zone"].contains("rpz-zone-id"), "{r:?}");
  }

  #[test]
  fn m6_stats_fields_are_filled() {
      let shared = shared_with_endpoint("");
      let rt = shared.runtime.load();
      let s = shared.metrics.stats(&rt, &shared.recursor);
      assert_eq!(s.queries_by_rcode.len(), 7);
      assert_eq!(s.queries_by_transport.len(), 5);
      assert_eq!(s.answers_by_route.len(), 8);
      assert_eq!(s.miss_duration_bucket_counts.len(), s.duration_bucket_bounds_us.len());
      assert_eq!(s.race_duration_bucket_counts.len(), s.duration_bucket_bounds_us.len());
      assert_eq!(s.acl_refused.len(), 2);
      assert_eq!(s.open_connections.len(), 4);
      assert!(s.started_unix_ms > 0 && s.process_resident_bytes > 0 && s.process_cpu_seconds_total >= 0.0);
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test telemetry_export m6_'` and
      expect FAIL: it does not compile, with `struct QueryRecord has no field named filter_source`.
- [ ] Implement `engine/src/telemetry/querylog.rs`: the constants, `FilterSource` with `as_str`, and
      the six fields. Update every `QueryRecord { .. }` construction, found with
      `grep -rn "filter_generation:" engine/src`, so the new fields start as
      `FilterSource::None, NO_RULE, false, NO_RPZ_ZONE, ACL_NONE, 1` (`Scope::record` in
      `engine/src/server/mod.rs`, `engine/src/server/rewrite.rs`).
- [ ] Implement `log_record` in `engine/src/telemetry/otlp.rs` with the new parameter. After the
      existing attributes:
  - when `r.filter_source != FilterSource::None`, push `nexora.filter.source`;
  - when `r.filter_rule_offset != NO_RULE`, push `nexora.filter.rule` =
    `(if r.rewrite_wildcard { "*." } else { "" }) + presentation(&r.name.as_wire()[offset..]).trim_end_matches('.')`
    (an offset past the name length gives no attribute);
  - when `r.acl_refused != 0`, push `nexora.acl.refused` = `recursion` or `authoritative`;
  - when `!rpz_zone_id.is_empty()`, push `nexora.rpz_zone`;
  - when `r.upstream_raced > 1`, push `nexora.upstream_raced` as `IntValue`.

  Keep `nexora.filter.list_id`/`nexora.filter.category` for any `Some(meta)`. In `Exporter::drain`:
  - pass the rpz zone id from `shared.recursor.rpz.set.load().zones.get(usize::from(r.rpz_zone)).map_or("", |z| z.id.as_str())`
    when `r.rpz_zone != NO_RPZ_ZONE`;
  - attach `filter_list` whenever `r.filter_list != NO_FILTER_LIST` and the generation matches, not
    only for blocked records.

- [ ] Create `engine/src/telemetry/process.rs`:
  ```rust
  //! Process CPU, resident memory, cgroup memory limit and start time for `Stats` (off the query path).
  use std::sync::OnceLock;
  use std::time::{SystemTime, UNIX_EPOCH};

  static STARTED: OnceLock<i64> = OnceLock::new();

  /// Milliseconds since the epoch at the first call (main calls it at start).
  pub fn started_unix_ms() -> i64 {
      *STARTED.get_or_init(|| SystemTime::now().duration_since(UNIX_EPOCH).map_or(0, |d| d.as_millis() as i64))
  }

  /// utime + stime of this process in seconds, from /proc/self/stat (fields 14 and 15, clock ticks of 100 Hz).
  pub fn cpu_seconds() -> f64 {
      let Ok(stat) = std::fs::read_to_string("/proc/self/stat") else { return 0.0 };
      let Some(rest) = stat.rsplit_once(')').map(|(_, r)| r) else { return 0.0 };
      let f: Vec<&str> = rest.split_whitespace().collect();
      let ticks = |i: usize| f.get(i).and_then(|v| v.parse::<u64>().ok()).unwrap_or(0);
      (ticks(11) + ticks(12)) as f64 / 100.0
  }

  /// Resident set size in bytes from /proc/self/statm (pages x 4096).
  pub fn resident_bytes() -> u64 {
      std::fs::read_to_string("/proc/self/statm")
          .ok()
          .and_then(|s| s.split_whitespace().nth(1).and_then(|v| v.parse::<u64>().ok()))
          .map_or(0, |pages| pages * 4096)
  }

  /// The cgroup v2 memory limit, 0 when unlimited or unknown.
  pub fn memory_limit_bytes() -> u64 {
      std::fs::read_to_string("/sys/fs/cgroup/memory.max")
          .ok()
          .and_then(|s| s.trim().parse::<u64>().ok())
          .unwrap_or(0)
  }
  ```
  Add a `debt:` comment on the 100 Hz and 4096-octet assumptions (true on kw arm64 and x86_64 Linux;
  revisit for an engine on a kernel with other `CLK_TCK` or page size). Call `started_unix_ms()` at
  the start of `main` in `engine/src/main.rs`.
- [ ] In `engine/src/telemetry/metrics.rs`:
  - Add to `WorkerCounters`: `answers_by_route: [Counter; 8]`, `miss_duration_buckets: [Counter; 16]`,
    `acl_refused: [Counter; 2]`, `filter_rewritten: Counter`.
  - Implement `observe_record`:
    1. The route slot is 0 for cache `Hit|Stale`, 1 for `Auth`, 2 for filter `Blocked`, 3 for
       `Rewritten`, 4 when `rpz_action` is not 0 and not the passthru or disabled code, and otherwise
       `5 + route`.
    2. Add to `miss_duration_buckets` when cache is `Miss|Stale`.
    3. Add to `acl_refused[acl_refused - 1]` when non-zero.
    4. Add to `filter_rewritten` for `Rewritten`.
  - Sum the counters in `Totals`.
  - Add `tcp` as a fourth slot of `ENCRYPTED.connections`, with `ConnectionGuard::new(Transport::Tcp)`
    mapped to slot 3.
  - Register `nexora_acl_refused{acl}`, `nexora_upstream_race_wins{upstream}` (from
    `Health::race_wins`), `nexora_upstream_race_duration_seconds` (histogram from `upstream::RACE`),
    and `nexora_answers{route}`.
  - Fill the Stats fields 700–714, plus `RecursionStats.upstream_timeouts` from
    `m.upstream_timeouts`, `UpstreamStatus.race_wins_total`, `tls_certificate_not_after_unix` from
    `ENCRYPTED.tls_not_after`, and `log_lines_dropped_total: 0` (Task 13 fills it).
- [ ] In `engine/src/server/mod.rs` `Scope::finish`, call `self.ctx.counters().observe_record(&r)`
      after `observe`. In `note_answer`, set `rec.upstream_raced = ans.raced`, and extend the answer type
      from the forward call so `WorkerForward` stores `raced: Cell<u8>` copied from `Forwarded.raced`. In
      `engine/src/server/tcp.rs`, hold `ConnectionGuard::new(Transport::Tcp)` inside the spawned stream
      task.
- [ ] In `engine/src/upstream/mod.rs`:
  - Add `Parallel { max: u8 }`, which `order` sorts as `Fastest`.
  - Add `raced: 1` in `forward`'s `Forwarded`.
  - Add `race_wins: AtomicU64` to `Health`.
  - Add `pub static RACE: RaceHistogram` with the `DURATION_BOUNDS_US` buckets (non-cumulative
    `AtomicU64` array, `cumulative()` for `Stats`).

  In `engine/src/runtime.rs` map `Some(UpstreamStrategy::Parallel)` to
  `Strategy::Parallel { max: resolver.parallel_max.min(8) as u8 }`, and keep the strategy with `max`
  in `upstreams_key`.

- [ ] In `engine/src/lib.rs`, add below `VERSION`:
  ```rust
  /// The full commit hash: `NEXORA_COMMIT` at compile time, empty for an unstamped build.
  pub const COMMIT: &str = match option_env!("NEXORA_COMMIT") {
      Some(v) => v,
      None => "",
  };
  ```
  Add `println!("cargo:rerun-if-env-changed=NEXORA_VERSION");` and
  `println!("cargo:rerun-if-env-changed=NEXORA_COMMIT");` to `engine/build.rs`. In
  `engine/src/main.rs`, keep `#[command(version = nexora_engine::VERSION)]` and set the long version
  on the built command in `main`:
  `Cli::command().long_version(format!("{} {}", nexora_engine::VERSION, nexora_engine::COMMIT).trim_end().to_owned())`
  before parsing with `Cli::from_arg_matches`. `-V` keeps printing `nexora-engine <VERSION>`, and
  `--version` also shows the commit.
- [ ] Run
      `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test telemetry_export && cargo test --locked -p nexora-engine --test hot_path_alloc && cargo test --locked -p nexora-engine --test upstream_udp_tcp'`
      and expect PASS, with `cache_hit_path_does_not_allocate` still passing.
- [ ] Run
      `scripts/dev-exec.sh 'cargo fmt --all && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings && cargo test --locked -p nexora-engine --all-targets'`
      and expect PASS.
- [ ] Report the paths. Commit message: `engine: M6 query record fields, stats fields, counters, parallel strategy variant`.

## Task 5: Query log backends and API: partial names, multi-value filters, reason fields

Files:

- `mgmt/internal/querylog/backend.go`: slice filters and new record fields.
- `mgmt/internal/querylog/builtin.go`: set membership and new attributes.
- `mgmt/internal/querylog/opensearch.go`: wildcard, terms and new attributes.
- `mgmt/internal/querylog/builtin_test.go`, `mgmt/internal/querylog/opensearch_test.go`: unit tests,
  and existing tests moved to slice fields.
- `mgmt/internal/api/handlers_admin.go`: `SearchQueryLog` parameters and name resolution.
- `mgmt/internal/api/querylog_resolve.go`: created; list, policy group, RPZ zone and rewrite name
  resolution.
- `mgmt/internal/api/querylog_test.go`: created.
- `e2e/gui_test.go`: `TestQueryLogBackends` subtests only.
- `e2e/filter_attribution_test.go`: extend `TestQueryLogCategoryAttribution` (its engine parts pass
  after Task 12; the assertions are written here and marked with the Task 12 dependency in the report).

Interfaces (consumed by Tasks 18, 19):

```go
// package querylog
type Query struct {
	From, To time.Time
	Client, Name string
	QTypes, RCodes, Caches, Filters, Categories, Sources, ListIDs, PolicyGroups, EngineIDs []string
	Limit int
	Cursor string
}
type Record struct {
	Time time.Time
	Client, Name, QType, RCode, Cache, Filter, Upstream, Transport, EngineID string
	ListID, Category string
	Source, Rule, PolicyGroupID, RPZZoneID, RPZAction, ACLRefused string
	UpstreamsRaced int64
	DurationUS int64
}
// EscapeWildcard escapes \, * and ? for an OpenSearch wildcard value.
func EscapeWildcard(s string) string
// package api
func resolveRecordNames(ctx context.Context, q store.PolicyQuerier, cat *catalog.Catalog, recs []querylog.Record) ([]QueryLogRecord, error)
```

Policy group filter value `global` matches records with an empty `PolicyGroupID`.

- [ ] Add to `mgmt/internal/querylog/opensearch_test.go`:
  ```go
  func TestOpenSearchNameWildcardEscaped(t *testing.T) {
  	var body string
  	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  		b, _ := io.ReadAll(r.Body)
  		body = string(b)
  		w.Header().Set("Content-Type", "application/json")
  		fmt.Fprint(w, `{"hits":{"hits":[]}}`)
  	}))
  	defer srv.Close()
  	os, err := querylog.NewOpenSearch(config.OpenSearchConfig{URL: srv.URL, Index: "nexora-querylog-*"})
  	if err != nil {
  		t.Fatal(err)
  	}
  	if _, err := os.Search(context.Background(), querylog.Query{Name: "A*b?c\\.", Limit: 5,
  		QTypes: []string{"A", "AAAA"}, Sources: []string{"allowlist"}}); err != nil {
  		t.Fatal(err)
  	}
  	for _, want := range []string{
  		`"wildcard":{"attributes.dns.question.name.keyword":{"case_insensitive":true,"value":"*A\\*b\\?c\\\\*"}}`,
  		`"terms":{"attributes.dns.question.type.keyword":["A","AAAA"]}`,
  		`"terms":{"attributes.nexora.filter.source.keyword":["allowlist"]}`,
  	} {
  		if !strings.Contains(body, want) {
  			t.Fatalf("body lacks %s:\n%s", want, body)
  		}
  	}
  	if strings.Contains(body, "match_phrase") {
  		t.Fatalf("name still uses match_phrase: %s", body)
  	}
  }
  ```
  and to `mgmt/internal/querylog/builtin_test.go`:
  ```go
  func TestBuiltinPartialNamesAndMultiValues(t *testing.T) {
  	b := querylog.NewBuiltin(10)
  	req := request("www.you-1.tube.test.", "you-1.test.", "x*y-1.test.", "other.test.")
  	lr := req.ResourceLogs[0].ScopeLogs[0].LogRecords
  	lr[1].Attributes[3] = str("dns.response.code", "NXDOMAIN")
  	lr[2].Attributes[2] = str("dns.question.type", "AAAA")
  	lr[0].Attributes = append(lr[0].Attributes, str("nexora.filter.source", "allowlist"), str("nexora.filter.rule", "tube.test"), str("nexora.policy.group", "g1"))
  	b.Ingest("e1", req)
  	names := func(q querylog.Query) []string {
  		q.Limit = 10
  		p, err := b.Search(context.Background(), q)
  		if err != nil {
  			t.Fatal(err)
  		}
  		var out []string
  		for _, r := range p.Records {
  			out = append(out, r.Name)
  		}
  		sort.Strings(out)
  		return out
  	}
  	if got := names(querylog.Query{Name: "YOU-1."}); len(got) != 2 {
  		t.Fatalf("partial, case-insensitive, trailing dot: %v", got)
  	}
  	if got := names(querylog.Query{Name: "x*y"}); len(got) != 1 || got[0] != "x*y-1.test." {
  		t.Fatalf("literal *: %v", got)
  	}
  	if got := names(querylog.Query{Name: "x?y"}); len(got) != 0 {
  		t.Fatalf("? is literal: %v", got)
  	}
  	if got := names(querylog.Query{RCodes: []string{"NXDOMAIN", "NOERROR"}, QTypes: []string{"AAAA"}}); len(got) != 1 || got[0] != "x*y-1.test." {
  		t.Fatalf("OR within, AND across: %v", got)
  	}
  	if got := names(querylog.Query{Sources: []string{"allowlist"}, PolicyGroups: []string{"g1"}}); len(got) != 1 {
  		t.Fatalf("source and policy group: %v", got)
  	}
  	if got := names(querylog.Query{PolicyGroups: []string{"global"}}); len(got) != 3 {
  		t.Fatalf("global policy group: %v", got)
  	}
  	p, _ := b.Search(context.Background(), querylog.Query{Sources: []string{"allowlist"}, Limit: 1})
  	if p.Records[0].Rule != "tube.test" || p.Records[0].PolicyGroupID != "g1" {
  		t.Fatalf("reason fields: %+v", p.Records[0])
  	}
  }
  ```
- [ ] Create `mgmt/internal/api/querylog_test.go`:
  ```go
  package api_test

  import (
  	"context"
  	"net/http"
  	"testing"

  	"github.com/piwi3910/nexora/mgmt/internal/api"
  	"github.com/piwi3910/nexora/mgmt/internal/querylog"
  )

  type recordingBackend struct{ got querylog.Query }

  func (r *recordingBackend) Name() string { return "recording" }
  func (r *recordingBackend) Search(_ context.Context, q querylog.Query) (querylog.Page, error) {
  	r.got = q
  	return querylog.Page{}, nil
  }

  func TestSearchQueryLogRepeatedParameters(t *testing.T) {
  	rb := &recordingBackend{}
  	e := newAPIWith(t, func(d *api.Deps) { d.QueryLog = rb })
  	c := e.client(t)
  	if code := c.do("POST", "/setup", map[string]any{"token": e.setup, "username": "admin", "email": "a@example.test", "password": "admin-password-1"}, nil); code != http.StatusCreated {
  		t.Fatalf("setup -> %d", code)
  	}
  	if code := c.do("GET", "/query-log?qtype=A&qtype=AAAA&rcode=NXDOMAIN&source=allowlist&policy_group=global&engine_id=e1&name=You", nil, nil); code != http.StatusOK {
  		t.Fatalf("repeated -> %d", code)
  	}
  	if len(rb.got.QTypes) != 2 || rb.got.RCodes[0] != "NXDOMAIN" || rb.got.Sources[0] != "allowlist" || rb.got.PolicyGroups[0] != "global" || rb.got.EngineIDs[0] != "e1" || rb.got.Name != "You" {
  		t.Fatalf("query: %+v", rb.got)
  	}
  	if code := c.do("GET", "/query-log?qtype=MX", nil, nil); code != http.StatusOK || len(rb.got.QTypes) != 1 {
  		t.Fatalf("single value -> %d %+v", code, rb.got)
  	}
  	many := "/query-log?"
  	for i := 0; i < 33; i++ {
  		many += "qtype=A&"
  	}
  	if code := c.do("GET", many, nil, nil); code != http.StatusBadRequest {
  		t.Fatalf("33 values -> %d, want 400", code)
  	}
  	if code := c.do("GET", "/query-log?source=bogus", nil, nil); code != http.StatusBadRequest {
  		t.Fatalf("unknown source -> %d, want 400", code)
  	}
  }
  ```
- [ ] In `e2e/gui_test.go` `TestQueryLogBackends`, after the Playwright run, add:
  ```go
  			t.Run("partial-name", func(t *testing.T) {
  				n := strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
  				for _, q := range []string{"www.you-" + n + ".tube.test.", "you-" + n + ".test.", "x*y-" + n + ".test."} {
  					harness.MustQuery(t, eng.DNS, q, dns.TypeA, harness.QueryOpts{})
  				}
  				search := func(frag string) []string {
  					var page struct{ Records []struct{ Name string } `json:"records"` }
  					api.Must("GET", "/query-log?limit=50&name="+url.QueryEscape(frag), nil, &page, 200)
  					var out []string
  					for _, r := range page.Records {
  						out = append(out, r.Name)
  					}
  					sort.Strings(out)
  					return out
  				}
  				harness.EventuallyTrue(t, 30*time.Second, func() bool { return len(search("you-"+n)) == 2 }, "you-<n> finds both names")
  				if got := search("TUBE.TEST"); len(got) == 0 || got[len(got)-1] != "www.you-"+n+".tube.test." {
  					t.Fatalf("case-insensitive: %v", got)
  				}
  				if got := search("x*y-" + n); len(got) != 1 {
  					t.Fatalf("literal *: %v", got)
  				}
  				if got := search("x?y-" + n); len(got) != 0 {
  					t.Fatalf("? must be literal: %v", got)
  				}
  				if got := search("absent-" + n); len(got) != 0 {
  					t.Fatalf("negative: %v", got)
  				}
  			})
  			t.Run("multi-value", func(t *testing.T) {
  				n := strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
  				harness.MustQuery(t, eng.DNS, "mv-a-"+n+".test.", dns.TypeA, harness.QueryOpts{})
  				harness.MustQuery(t, eng.DNS, "mv-aaaa-"+n+".test.", dns.TypeAAAA, harness.QueryOpts{})
  				harness.MustQuery(t, eng.DNS, "mv-mx-"+n+".test.", dns.TypeMX, harness.QueryOpts{})
  				count := func(query string) int {
  					var page struct{ Records []struct{ Name string } `json:"records"` }
  					api.Must("GET", "/query-log?limit=50&name=mv-&"+query, nil, &page, 200)
  					c := 0
  					for _, r := range page.Records {
  						if strings.Contains(r.Name, n) {
  							c++
  						}
  					}
  					return c
  				}
  				harness.EventuallyTrue(t, 30*time.Second, func() bool { return count("qtype=A&qtype=AAAA") == 2 }, "OR within qtype")
  				if c := count("qtype=A&qtype=AAAA&rcode=NXDOMAIN"); c != 0 {
  					t.Fatalf("AND across fields: %d", c)
  				}
  				if c := count("qtype=MX"); c != 1 {
  					t.Fatalf("single value: %d", c)
  				}
  			})
  ```
  with imports `net/url` and `sort`. The fixture DNS answers NOERROR for these names (the existing
  test relies on it).
- [ ] Extend `TestQueryLogCategoryAttribution` in `e2e/filter_attribution_test.go`:
  - Add to `attributedRecord` the fields `Source, ListName "list_name", Rule, PolicyGroupID "policy_group_id", PolicyGroupName "policy_group_name", RPZZoneName "rpz_zone_name", RewriteAnswer "rewrite_answer"`.
  - Then assert:
    1. A custom list: create it with `s.api.Must("POST", "/filter-lists", {"name":"custom-attr","kind":"block","url": s.lists.URL("custom-attr"),...})`,
       serving `custom.attr.test`. Querying `www.custom.attr.test.` yields `Source=="blocklist"`,
       `ListName=="custom-attr"` and `Rule=="custom.attr.test"`.
    2. The gambling record yields `Source=="category"` and `ListName` equal to the catalog source name
       of `hagezi-gambling`.
    3. Adding `allow.casino.attr.test` to the global allowlist (the existing allowlist operation used
       by `web/e2e/screens/04-filtering.spec.ts`) makes `allow.casino.attr.test.` yield
       `Source=="allowlist"`, `Rule=="allow.casino.attr.test"` and `PolicyGroupName==""`.
    4. A policy group for `127.0.0.2/32` with allowlist `grp.casino.attr.test`, queried with
       `udpFrom(t, net.ParseIP("127.0.0.2"), ...)`, yields `Source=="allowlist"` and
       `PolicyGroupName` equal to the group name.
    5. A global rewrite `rw.attr.test A 192.0.2.55` yields `Source=="rewrite"`, `Rule=="rw.attr.test"`
       and `RewriteAnswer=="A 192.0.2.55"`.

  Each check first asserts the DNS answer (positive path).

- [ ] Run
      `scripts/dev-exec.sh 'go test ./mgmt/internal/querylog -run "TestOpenSearchNameWildcardEscaped|TestBuiltinPartialNamesAndMultiValues" -count=1; make webui-placeholder && go test ./mgmt/internal/api -run TestSearchQueryLogRepeatedParameters -count=1'`
      and expect FAIL with `unknown field QTypes in struct literal`.
- [ ] Implement `mgmt/internal/querylog/backend.go` with the new `Query`/`Record` and
      `func EscapeWildcard(s string) string { return strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?").Replace(s) }`.
- [ ] Implement `mgmt/internal/querylog/builtin.go`:
  - `recordFromAttributes` reads `nexora.filter.source`, `nexora.filter.rule`, `nexora.policy.group`,
    `nexora.rpz_zone`, `nexora.rpz` (as `RPZAction`, with `none` mapped to empty), `nexora.acl.refused`
    and `nexora.upstream_raced` (int).
  - `matches` uses `in := func(v string, set []string) bool { return len(set) == 0 || slices.Contains(set, v) }`
    for each slice.
  - `PolicyGroups` treats `global` as `""`.
  - `Name` trims one trailing `.` from the fragment before the lowercase substring test.
  - `EngineIDs` compares with `r.EngineID`.
- [ ] Implement `mgmt/internal/querylog/opensearch.go`:
  - The name uses
    `{"wildcard":{"attributes.dns.question.name.keyword":{"value":"*"+EscapeWildcard(strings.TrimSuffix(q.Name,"."))+"*","case_insensitive":true}}}`,
    with the comment `// debt: leading-wildcard on .keyword scans every term of the day's index; revisit with an n-gram sub-field when a daily index passes 50M documents or searches exceed the 5 s timeout on kw`.
  - Each slice becomes `{"terms":{"attributes.<field>.keyword":[...]}}` for these fields:
    `dns.question.type`, `dns.response.code`, `nexora.cache`, `nexora.filter.category`,
    `nexora.filter.source`, `nexora.filter.list_id`, `nexora.engine.id`.
  - `Filters` uses a `bool.should` of two `terms` queries (`nexora.filter.keyword` and
    `nexora.filter.result.keyword`).
  - `PolicyGroups` uses a `bool.should` of `terms` on `nexora.policy.group.keyword` for the non-global
    ids and, for `global`, `{"bool":{"must_not":{"exists":{"field":"attributes.nexora.policy.group"}}}}`
    or an empty term.
  - Extend `osSource.Attributes` with the new keys.
- [ ] Implement `SearchQueryLog` in `mgmt/internal/api/handlers_admin.go`:
  - Trim and drop empty values, and deduplicate each slice.
  - Return `invalid("at most 32 values per parameter")` above 32.
  - Validate `source`, `cache` and `filter` values against their enums with 400 `invalid_request`.
  - Map records through `resolveRecordNames`.

  Create `mgmt/internal/api/querylog_resolve.go`:
  - One `select id::text, name from filter_lists where id::text = any($1)`, where names of the form
    `catalog:<cat>:<src>` become the catalog source's display name through `cat.Source(cat, src)`
    (use the catalog accessor that `mgmt/internal/api/filtercategories.go` uses).
  - `allowlist` → `Global allowlist`.
  - `group-allow:<sha>` → `<policy group name> allowlist`.
  - `select id::text, name from policy_groups where id::text = any($1)`.
  - `select id::text, name from rpz_zones where id::text = any($1)`.
  - For `source == "rewrite"`:
    `select type || ' ' || value from rewrites where name = $1 and group_id is not distinct from nullif($2,'')::uuid order by type, value limit 1`.
  - Each lookup runs once per page.

- [ ] Run
      `scripts/dev-exec.sh 'go test ./mgmt/internal/querylog -count=1 && make webui-placeholder && go test ./mgmt/internal/api -run "TestSearchQueryLogRepeatedParameters|TestPermissionsCoverEveryOperation" -count=1'`
      and expect PASS.
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestQueryLogBackends -count=1 -timeout 30m'`
      and expect PASS for `builtin/partial-name`, `builtin/multi-value`, `opensearch/partial-name` and
      `opensearch/multi-value`. `TestQueryLogCategoryAttribution` passes once Task 12 is committed: run it
      in the Task 12 verification.
- [ ] Report the paths. Commit message: `querylog: partial names, multi-value filters, reason fields`.

## Task 6: Access control split in the management plane

Files:

- `mgmt/migrations/00600_access_split.sql`: created.
- `mgmt/internal/api/handlers_dns.go`: `GetAccessControl`/`UpdateAccessControl` only.
- `mgmt/internal/zone/model.go`, `mgmt/internal/zone/service.go`: `AllowQueryCIDRs`,
  `UpdateAllowCIDRs`.
- `mgmt/internal/api/zones.go`: API mapping.
- `mgmt/internal/snapshot/snapshot.go`: authoritative ACL fields in `BuildForGroup`.
- `mgmt/internal/snapshot/authzones.go`: zone fields.
- `mgmt/internal/api/access_split_test.go`: created.
- `mgmt/internal/store/access_split_migration_test.go`: created.

Interfaces:

- `zone.Zone.AllowQueryCIDRs []netip.Prefix` and `zone.Zone.UpdateAllowCIDRs []netip.Prefix`.
- `zone.CreateZoneInput.AllowQueryCIDRs []string`, `zone.UpdateZoneInput.AllowQueryCIDRs *[]string`,
  `zone.UpdateInput.AllowCIDRs []string`, following the existing input shape for updates.
- Snapshot fields `AuthoritativeAllowCidrs` and `AuthoritativeAclSet=true` on every snapshot this
  management plane builds.
- `AuthZone.AllowQueryCidrs` and `UpdateAllowCidrs` as canonical prefixes.

- [ ] Create `mgmt/internal/store/access_split_migration_test.go`:
  ```go
  package store_test

  import (
  	"context"
  	"testing"
  	"time"

  	"github.com/jackc/pgx/v5/stdlib"
  	"github.com/pressly/goose/v3"

  	"github.com/piwi3910/nexora/e2e/harness"
  	"github.com/piwi3910/nexora/mgmt/internal/store"
  	"github.com/piwi3910/nexora/mgmt/migrations"
  )

  // An install at 00503 with a custom ACL keeps it as recursion access and gets "any" authoritative access.
  func TestAccessSplitMigrationKeepsBehaviour(t *testing.T) {
  	pg := harness.New(t).StartPostgres()
  	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
  	defer cancel()
  	st, err := store.Open(ctx, pg.URL)
  	if err != nil {
  		t.Fatal(err)
  	}
  	defer st.Close()
  	db := stdlib.OpenDBFromPool(st.Pool)
  	defer db.Close()
  	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
  	if err != nil {
  		t.Fatal(err)
  	}
  	if _, err := provider.UpTo(ctx, 503); err != nil {
  		t.Fatal(err)
  	}
  	if _, err := st.Pool.Exec(ctx, `update access_control set allow_cidrs = array['192.0.2.0/24']::cidr[]`); err != nil {
  		t.Fatal(err)
  	}
  	if err := st.Migrate(ctx); err != nil {
  		t.Fatal(err)
  	}
  	var rec, auth []string
  	if err := st.Pool.QueryRow(ctx, `select array(select c::text from unnest(allow_cidrs) c), array(select c::text from unnest(authoritative_allow_cidrs) c) from access_control`).Scan(&rec, &auth); err != nil {
  		t.Fatal(err)
  	}
  	if len(rec) != 1 || rec[0] != "192.0.2.0/24" {
  		t.Fatalf("recursion ACL changed: %v", rec)
  	}
  	if len(auth) != 2 || auth[0] != "0.0.0.0/0" || auth[1] != "::/0" {
  		t.Fatalf("authoritative default: %v", auth)
  	}
  }
  ```
- [ ] Create `mgmt/internal/api/access_split_test.go`:
  ```go
  package api_test

  import (
  	"net/http"
  	"testing"

  	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
  	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
  )

  func TestAccessControlSplitAPI(t *testing.T) {
  	e := newAPI(t)
  	c := e.client(t)
  	if code := c.do("POST", "/setup", map[string]any{"token": e.setup, "username": "admin", "email": "a@example.test", "password": "admin-password-1"}, nil); code != http.StatusCreated {
  		t.Fatalf("setup -> %d", code)
  	}
  	var ac struct {
  		AllowCidrs     []string `json:"allow_cidrs"`
  		AuthCidrs      []string `json:"authoritative_allow_cidrs"`
  		Revision       int64    `json:"revision"`
  	}
  	c.do("GET", "/access-control", nil, &ac)
  	if len(ac.AuthCidrs) != 2 || ac.AuthCidrs[0] != "0.0.0.0/0" {
  		t.Fatalf("default authoritative access: %+v", ac)
  	}
  	if code := c.do("PUT", "/access-control", map[string]any{"allow_cidrs": ac.AllowCidrs, "authoritative_allow_cidrs": []string{"192.168.0.0/16"}, "revision": ac.Revision}, &ac); code != http.StatusOK {
  		t.Fatalf("update -> %d", code)
  	}
  	if code := c.do("PUT", "/access-control", map[string]any{"allow_cidrs": ac.AllowCidrs, "revision": ac.Revision}, &ac); code != http.StatusOK || len(ac.AuthCidrs) != 1 {
  		t.Fatalf("omitting authoritative_allow_cidrs keeps it: %d %+v", code, ac)
  	}
  	if code := c.do("PUT", "/access-control", map[string]any{"allow_cidrs": ac.AllowCidrs, "authoritative_allow_cidrs": []string{"nope"}, "revision": ac.Revision}, nil); code != http.StatusBadRequest {
  		t.Fatalf("invalid cidr -> %d", code)
  	}
  	var z struct {
  		ID       string   `json:"id"`
  		Revision int64    `json:"revision"`
  		Allow    []string `json:"allow_query_cidrs"`
  	}
  	if code := c.do("POST", "/zones", map[string]any{"name": "kw.test.", "kind": "primary", "nameservers": []string{"ns1.kw.test."},
  		"soa": map[string]any{"mname": "ns1.kw.test.", "rname": "hostmaster.kw.test."}, "allow_query_cidrs": []string{"10.0.0.0/8"},
  		"update": map[string]any{"tsig_key_ids": []string{}, "allow_cidrs": []string{"127.0.0.1/32"}}}, &z); code != http.StatusCreated {
  		t.Fatalf("create zone -> %d", code)
  	}
  	if len(z.Allow) != 1 {
  		t.Fatalf("zone allow_query_cidrs: %+v", z)
  	}
  	if code := c.do("PATCH", "/zones/"+z.ID, map[string]any{"revision": z.Revision, "allow_query_cidrs": []string{"bad"}}, nil); code != http.StatusBadRequest {
  		t.Fatalf("invalid zone cidr -> %d", code)
  	}
  	snap, err := snapshot.Latest(e.ctx, e.st)
  	if err != nil {
  		t.Fatal(err)
  	}
  	if !snap.AuthoritativeAclSet || len(snap.AuthoritativeAllowCidrs) != 1 || snap.AuthoritativeAllowCidrs[0] != "192.168.0.0/16" {
  		t.Fatalf("snapshot authoritative ACL: %v %v", snap.AuthoritativeAclSet, snap.AuthoritativeAllowCidrs)
  	}
  	var hosted *controlv1.AuthZone
  	for _, az := range snap.AuthZones {
  		if az.Name == "kw.test." {
  			hosted = az
  		}
  	}
  	if hosted == nil || len(hosted.AllowQueryCidrs) != 1 || hosted.UpdateAllowCidrs[0] != "127.0.0.1/32" {
  		t.Fatalf("snapshot zone: %+v", hosted)
  	}
  	var audit struct{ Events []struct{ Action string } `json:"events"` }
  	c.do("GET", "/audit?limit=20", nil, &audit)
  	found := false
  	for _, ev := range audit.Events {
  		found = found || ev.Action == "updateAccessControl"
  	}
  	if !found {
  		t.Fatalf("no updateAccessControl audit row: %+v", audit)
  	}
  }
  ```
  Check the zone creation body against `web/e2e/screens/18-zones.spec.ts` and `e2e/authoritative_test.go`
  `createPrimaryZone`, and the audit list response shape against `listAuditEvents` in
  `mgmt/api/openapi.yaml`, and adapt the literal field names to those.
- [ ] Run
      `scripts/dev-exec.sh 'make webui-placeholder && go test ./mgmt/internal/api -run TestAccessControlSplitAPI -count=1; go test ./mgmt/internal/store -run TestAccessSplitMigrationKeepsBehaviour -count=1'`
      and expect FAIL: `default authoritative access: {...AuthCidrs:[]}` and
      `column "authoritative_allow_cidrs" does not exist`.
- [ ] Create `mgmt/migrations/00600_access_split.sql`:
  ```sql
  -- +goose Up
  -- M6: authoritative query access separate from recursion access. Existing installs keep
  -- allow_cidrs as recursion access and answer hosted zones to everyone, as before.
  ALTER TABLE access_control
      ADD COLUMN authoritative_allow_cidrs cidr[] NOT NULL DEFAULT array['0.0.0.0/0', '::/0']::cidr[];
  ALTER TABLE zones
      ADD COLUMN allow_query_cidrs cidr[] NOT NULL DEFAULT '{}',
      ADD COLUMN update_allow_cidrs cidr[] NOT NULL DEFAULT '{}';

  -- +goose Down
  ALTER TABLE zones DROP COLUMN update_allow_cidrs, DROP COLUMN allow_query_cidrs;
  ALTER TABLE access_control DROP COLUMN authoritative_allow_cidrs;
  ```
- [ ] In `mgmt/internal/api/handlers_dns.go`:
  - Extend `accessControlSelect`/`scanAccessControl` with
    `array(select host(c) || '/' || masklen(c) from unnest(authoritative_allow_cidrs) with ordinality u(c, n) order by n)`.
  - In `UpdateAccessControl`, validate `AuthoritativeAllowCidrs` with the same
    `netip.ParsePrefix`/`Masked()` loop when present.
  - Write `authoritative_allow_cidrs = coalesce($2::cidr[], authoritative_allow_cidrs)`.
  - Keep the audit action `updateAccessControl` with Before/After including both lists.
- [ ] In `mgmt/internal/zone/model.go` and `mgmt/internal/zone/service.go`:
  - Add the fields.
  - Parse both lists with `parseCIDRs` (error code `invalid_cidr`, which the API maps to 400).
  - Store them in `CreateZone` (insert columns) and `UpdateZone` (`set("allow_query_cidrs", cidrs)`
    and `set("update_allow_cidrs", cidrs)` when present).
  - Read them in the zone select.

  In `mgmt/internal/api/zones.go`, map `AllowQueryCidrs` both ways and `Update.AllowCidrs` both ways.
  Responses always carry an array, never null.

- [ ] In `mgmt/internal/snapshot/snapshot.go` `BuildForGroup`, after the recursion ACL query, add:
  ```go
  	if err := tx.QueryRow(ctx, `select array(select host(c) || '/' || masklen(c)
  		from access_control, unnest(authoritative_allow_cidrs) with ordinality as u(c, n) order by n)`).Scan(&snap.AuthoritativeAllowCidrs); err != nil {
  		return nil, fmt.Errorf("authoritative access control: %w", err)
  	}
  	snap.AuthoritativeAclSet = true
  ```
  In `mgmt/internal/snapshot/authzones.go`, select `allow_query_cidrs` and `update_allow_cidrs` as
  `host/masklen` text arrays into `AllowQueryCidrs` and `UpdateAllowCidrs`.
- [ ] Run
      `scripts/dev-exec.sh 'make webui-placeholder && go test ./mgmt/internal/api ./mgmt/internal/store ./mgmt/internal/snapshot ./mgmt/internal/zone -count=1'`
      and expect PASS, including `TestBuildMapsEveryTable` and the new tests.
- [ ] Report the paths. Commit message: `mgmt: authoritative query access and per-zone allow-query`.

## Task 7: Account self-service backend

Files:

- `mgmt/migrations/00602_user_profile.sql`: created.
- `mgmt/internal/auth/service.go`: `User` fields, `UserColumns`, `Login` throttling and
  `last_login_at`.
- `mgmt/internal/auth/account.go`: created; profile update, password change, failure counter.
- `mgmt/internal/api/account.go`: `UpdateCurrentUser`, `ChangeOwnPassword` (replacing the Task 2
  stubs).
- `mgmt/internal/api/handlers_auth.go`: `apiUser` fills the new fields; `Login` maps
  `ErrTooManyAttempts`.
- `mgmt/internal/api/server.go`: only the `mapError` case for `auth.ErrTooManyAttempts` → 429
  `too_many_attempts`.
- `mgmt/internal/api/account_test.go`: created.

Interfaces (consumed by Task 21 through the API):

```go
// package auth
var ErrTooManyAttempts = errors.New("too many failed attempts; try again later")
var ErrInvalidCurrentPassword = errors.New("current password is wrong")
var ErrManagedByIdentityProvider = errors.New("managed by your identity provider")
type Preferences struct { Theme string `json:"theme"`; TimeZone string `json:"time_zone"`; Clock24h bool `json:"clock_24h"`; QuerylogLive bool `json:"querylog_live"` }
// User gains: DisplayName string; LastLoginAt *time.Time; Preferences Preferences
type ProfileUpdate struct { Revision int64; Email, DisplayName *string; Preferences *Preferences }
func (s *Service) UpdateProfile(ctx context.Context, p Principal, in ProfileUpdate) (User, error)
func (s *Service) ChangePassword(ctx context.Context, p Principal, sessionToken, current, next string, revokeOthers bool) error
```

- [ ] Create `mgmt/internal/api/account_test.go`:
  ```go
  package api_test

  import (
  	"net/http"
  	"strings"
  	"testing"
  )

  func TestAccountSelfService(t *testing.T) {
  	e := newAPI(t)
  	admin := e.client(t)
  	if code := admin.do("POST", "/setup", map[string]any{"token": e.setup, "username": "admin", "email": "a@example.test", "password": "admin-password-1"}, nil); code != http.StatusCreated {
  		t.Fatalf("setup -> %d", code)
  	}
  	if code := admin.do("POST", "/users", map[string]any{"username": "vera", "email": "v@example.test", "password": "viewer-password-1", "role": "viewer"}, nil); code != http.StatusCreated {
  		t.Fatalf("create viewer -> %d", code)
  	}
  	login := func(pw string) (*client, int) {
  		c := e.client(t)
  		return c, c.do("POST", "/auth/login", map[string]any{"username": "vera", "password": pw}, nil)
  	}
  	vera, code := login("viewer-password-1")
  	if code != http.StatusOK {
  		t.Fatalf("login -> %d", code)
  	}
  	other, _ := login("viewer-password-1")
  	type me struct {
  		Role, Email, DisplayName string
  		Disabled                 bool
  		Revision                 int64
  		Preferences              map[string]any
  		LastLoginAt              *string `json:"last_login_at"`
  	}
  	var u me
  	vera.do("GET", "/auth/me", nil, &u)
  	if u.LastLoginAt == nil {
  		t.Fatal("last_login_at not set by login")
  	}
  	body := map[string]any{"revision": u.Revision, "email": "vera@example.test", "display_name": "Vera",
  		"preferences": map[string]any{"theme": "dark", "time_zone": "Europe/Brussels", "clock_24h": true, "querylog_live": false},
  		"role": "admin", "disabled": true, "username": "root"}
  	if code := vera.do("PUT", "/auth/me", body, &u); code != http.StatusOK {
  		t.Fatalf("update profile -> %d", code)
  	}
  	if u.Role != "viewer" || u.Disabled || u.Email != "vera@example.test" || u.Preferences["time_zone"] != "Europe/Brussels" {
  		t.Fatalf("self-service changed privileged fields or missed allowed ones: %+v", u)
  	}
  	if code := vera.do("PUT", "/auth/me", map[string]any{"revision": 1, "email": "x@example.test"}, nil); code != http.StatusConflict {
  		t.Fatalf("stale revision -> %d", code)
  	}
  	pw := func(c *client, cur, next string) int {
  		return c.do("POST", "/auth/me/password", map[string]any{"current_password": cur, "new_password": next, "revoke_other_sessions": true}, nil)
  	}
  	if code := pw(vera, "wrong-password-xx", "viewer-password-2"); code != http.StatusForbidden {
  		t.Fatalf("wrong current -> %d", code)
  	}
  	if code := pw(vera, "viewer-password-1", "short"); code != http.StatusBadRequest {
  		t.Fatalf("short -> %d", code)
  	}
  	if code := pw(vera, "viewer-password-1", "viewer-password-1"); code != http.StatusBadRequest {
  		t.Fatalf("unchanged -> %d", code)
  	}
  	if code := pw(vera, "viewer-password-1", "viewer-password-2"); code != http.StatusNoContent {
  		t.Fatalf("change -> %d", code)
  	}
  	if code := vera.do("GET", "/auth/me", nil, nil); code != http.StatusOK {
  		t.Fatalf("current session revoked: %d", code)
  	}
  	if code := other.do("GET", "/auth/me", nil, nil); code != http.StatusUnauthorized {
  		t.Fatalf("other session kept: %d", code)
  	}
  	if _, code := login("viewer-password-2"); code != http.StatusOK {
  		t.Fatalf("new password login -> %d", code)
  	}
  	var tok struct{ Token string `json:"token"` }
  	if code := admin.do("POST", "/api-tokens", map[string]any{"name": "t1", "role": "viewer"}, &tok); code != http.StatusCreated {
  		t.Fatalf("token -> %d", code)
  	}
  	bearer := e.client(t)
  	bearer.hc.Transport = bearerTransport(tok.Token)
  	if code := bearer.do("POST", "/auth/me/password", map[string]any{"current_password": "admin-password-1", "new_password": "admin-password-2"}, nil); code != http.StatusForbidden {
  		t.Fatalf("api token password change -> %d", code)
  	}
  	for i := 0; i < 11; i++ {
  		login("not-the-password-" + strings.Repeat("x", i))
  	}
  	if _, code := login("viewer-password-2"); code != http.StatusTooManyRequests {
  		t.Fatalf("12th attempt after 11 failures -> %d, want 429", code)
  	}
  	var rows int
  	if err := e.st.Pool.QueryRow(e.ctx, `select count(*) from audit_log where diff::text ilike '%password-%'`).Scan(&rows); err != nil || rows != 0 {
  		t.Fatalf("audit rows mention a password: %d %v", rows, err)
  	}
  	var actions int
  	e.st.Pool.QueryRow(e.ctx, `select count(*) from audit_log where action in ('updateCurrentUser','changeOwnPassword')`).Scan(&actions)
  	if actions != 2 {
  		t.Fatalf("audit actions: %d", actions)
  	}
  	if _, err := e.st.Pool.Exec(e.ctx, `update users set source = 'oidc', password_hash = null where username = 'admin'`); err != nil {
  		t.Fatal(err)
  	}
  	if code := pw(admin, "admin-password-1", "admin-password-2"); code != http.StatusConflict {
  		t.Fatalf("oidc password change -> %d", code)
  	}
  	if code := admin.do("PUT", "/auth/me", map[string]any{"revision": 1, "email": "changed@example.test"}, nil); code != http.StatusConflict {
  		t.Fatalf("oidc email change -> %d", code)
  	}
  }
  ```
  and, in the same file, `bearerTransport`:
  ```go
  type bearerTransport string

  func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
  	r = r.Clone(r.Context())
  	r.Header.Set("Authorization", "Bearer "+string(b))
  	return http.DefaultTransport.RoundTrip(r)
  }
  ```
  Check the `createApiToken` path and response field names in `mgmt/api/openapi.yaml` (`/api-tokens`),
  and adapt the literal to them.
- [ ] Run `scripts/dev-exec.sh 'make webui-placeholder && go test ./mgmt/internal/api -run TestAccountSelfService -count=1'`
      and expect FAIL: `last_login_at not set by login`.
- [ ] Create `mgmt/migrations/00602_user_profile.sql`:
  ```sql
  -- +goose Up
  ALTER TABLE users
      ADD COLUMN display_name text NOT NULL DEFAULT '' CHECK (length(display_name) <= 64),
      ADD COLUMN last_login_at timestamptz,
      ADD COLUMN preferences jsonb NOT NULL DEFAULT '{}';
  CREATE TABLE auth_failures (
      username text NOT NULL,
      at       timestamptz NOT NULL DEFAULT now()
  );
  CREATE INDEX auth_failures_username_at ON auth_failures (username, at);

  -- +goose Down
  DROP TABLE auth_failures;
  ALTER TABLE users DROP COLUMN preferences, DROP COLUMN last_login_at, DROP COLUMN display_name;
  ```
- [ ] Implement `mgmt/internal/auth/account.go`:
  - `recordFailure(ctx, username)` runs
    `with d as (delete from auth_failures where at < now() - interval '15 minutes'), i as (insert into auth_failures(username) values (lower($1))) select 1`.
  - `tooMany(ctx, username)` returns true when
    `select count(*) from auth_failures where username = lower($1) and at > now() - interval '15 minutes'`
    is above 10.
  - `UpdateProfile`:
    - it runs `select ... for update` and checks `revision`;
    - for `source='oidc'`, a changed email or display name returns `ErrManagedByIdentityProvider`;
    - it validates the theme enum, `time_zone` through `time.LoadLocation` (empty allowed) and the
      64-character display name, returning `validationError` (400);
    - it runs `update users set email, display_name, preferences, revision = revision + 1`;
    - it writes the audit row `updateCurrentUser` with Before/After of email, display name and
      preferences only.
  - `ChangePassword`:
    - an API token principal gets `ErrForbidden`, and an OIDC user `ErrManagedByIdentityProvider`;
    - `tooMany` gives `ErrTooManyAttempts`;
    - a failing `VerifyPassword` records a failure and returns `ErrInvalidCurrentPassword`;
    - a new password shorter than `MinPasswordLength` gives `ErrWeakPassword`, and one equal to the
      current password gives a `validationError`;
    - it hashes and updates;
    - with `revokeOthers`, it runs `delete from sessions where user_id = $1 and token_hash <> $2`
      with `hashToken(sessionToken)`;
    - it writes the audit row `changeOwnPassword` with `Before: nil, After: map[string]any{"revoked_other_sessions": revokeOthers}`.

  In `Service.Login`, check `tooMany` first (`ErrTooManyAttempts`), record a failure on
  `ErrInvalidCredentials`, and set `last_login_at = now()` on success. Extend `UserColumns` and
  `ScanUser` with `display_name, last_login_at, preferences`.

- [ ] Implement `mgmt/internal/api/account.go`:
  - `UpdateCurrentUser` decodes only `revision`, `email`, `display_name` and `preferences` into
    `auth.ProfileUpdate`, and ignores every other body key.
  - `ChangeOwnPassword` reads the session cookie through `requestFrom(ctx).Cookie(auth.SessionCookieName)`.
  - `ErrInvalidCurrentPassword` maps to `coded(403, "invalid_current_password", ...)`,
    `ErrManagedByIdentityProvider` to `coded(409, "managed_by_identity_provider", ...)`, and
    `ErrTooManyAttempts` to 429 `too_many_attempts` through `mapError` in
    `mgmt/internal/api/server.go`.

  `apiUser` in `mgmt/internal/api/handlers_auth.go` fills `DisplayName`, `LastLoginAt` and
  `Preferences`, with the defaults `system`, `""`, `false`, `true` for missing keys.

- [ ] Run
      `scripts/dev-exec.sh 'make webui-placeholder && go test ./mgmt/internal/api ./mgmt/internal/auth -count=1'`
      and expect PASS, including `TestSetupCRUDConflictAuditAndRBAC` and `TestSessionsTokensAndDisabledUsers`.
- [ ] Report the paths. Commit message: `auth: self-service profile, password change, failure throttling`.

## Task 8: Version endpoint and build stamping

Files:

- `mgmt/internal/api/version.go`: `GetVersion` (replacing the stub) and `var Commit, BuildDate, RepositoryURL = "", "", ""`.
- `mgmt/internal/api/version_test.go`: created.
- `mgmt/cmd/nexora-mgmt/main.go`: `commit`, `buildDate` vars and `NEXORA_REPOSITORY_URL`.
- `mgmt/internal/config/config.go`: `RepositoryURL`.
- `deploy/docker/mgmt.Dockerfile`, `deploy/docker/engine.Dockerfile`: build args.
- `scripts/build-image.sh`: `COMMIT`, `BUILD_DATE`.
- `.github/workflows/images.yml`: build args.
- `web/vite.config.ts`: `define`.
- `web/src/build-info.d.ts`: created; the constant declarations.
- `deploy/deploytest/buildinfo_test.go`: created.

Interfaces (consumed by Task 26):

- The GUI globals `__NEXORA_VERSION__`, `__NEXORA_COMMIT__` and `__NEXORA_BUILD_DATE__`, all strings
  (`dev`, `""`, `""` when unset).
- `GET /version` → `VersionInfo`.

- [ ] Create `deploy/deploytest/buildinfo_test.go`:
  ```go
  package deploytest

  import (
  	"os"
  	"path/filepath"
  	"strings"
  	"testing"
  )

  func TestDockerfilesStampBuildInfo(t *testing.T) {
  	root := filepath.Join("..", "..")
  	read := func(p string) string {
  		b, err := os.ReadFile(filepath.Join(root, p))
  		if err != nil {
  			t.Fatal(err)
  		}
  		return string(b)
  	}
  	for file, wants := range map[string][]string{
  		"deploy/docker/mgmt.Dockerfile":   {"ARG VERSION=dev", "ARG COMMIT=", "ARG BUILD_DATE=", "-X main.commit=${COMMIT}", "-X main.buildDate=${BUILD_DATE}", "NEXORA_COMMIT=${COMMIT}"},
  		"deploy/docker/engine.Dockerfile": {"ARG COMMIT=", `NEXORA_COMMIT="${COMMIT}"`},
  		"scripts/build-image.sh":          {"build-arg:COMMIT=", "build-arg:BUILD_DATE="},
  		".github/workflows/images.yml":    {"COMMIT=${{ github.sha }}", "BUILD_DATE="},
  		"web/vite.config.ts":              {"__NEXORA_VERSION__", "__NEXORA_COMMIT__", "__NEXORA_BUILD_DATE__"},
  	} {
  		body := read(file)
  		for _, w := range wants {
  			if !strings.Contains(body, w) {
  				t.Errorf("%s lacks %q", file, w)
  			}
  		}
  	}
  }
  ```
  and `mgmt/internal/api/version_test.go`:
  ```go
  package api_test

  import (
  	"net/http"
  	"testing"

  	"github.com/piwi3910/nexora/mgmt/internal/api"
  	"github.com/piwi3910/nexora/mgmt/internal/store"
  	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
  )

  func TestVersionEndpoint(t *testing.T) {
  	api.Version, api.Commit, api.BuildDate, api.RepositoryURL = "v6.0.0", "f0ee3a1c0ffee0000000000000000000000000aa", "2026-09-14T12:00:00Z", "https://github.com/azrtydxb/nexora"
  	t.Cleanup(func() { api.Version, api.Commit, api.BuildDate, api.RepositoryURL = "dev", "", "", "" })
  	e := newAPI(t)
  	c := e.client(t)
  	if code := c.do("POST", "/setup", map[string]any{"token": e.setup, "username": "admin", "email": "a@example.test", "password": "admin-password-1"}, nil); code != http.StatusCreated {
  		t.Fatalf("setup -> %d", code)
  	}
  	for i, v := range []string{"sha-f0ee3a1", "sha-f0ee3a1", "sha-0000001"} {
  		id := storetest.InsertEngine(t, e.st, "v"+string(rune('a'+i)), store.DefaultEngineGroupID)
  		if _, err := e.st.Pool.Exec(e.ctx, `update engines set engine_version = $2 where id = $1`, id, v); err != nil {
  			t.Fatal(err)
  		}
  	}
  	var got struct {
  		Version       string `json:"version"`
  		Commit        string `json:"commit"`
  		BuildDate     string `json:"build_date"`
  		RepositoryURL string `json:"repository_url"`
  		Engines       []struct {
  			Version string `json:"version"`
  			Count   int    `json:"count"`
  		} `json:"engines"`
  	}
  	if code := c.do("GET", "/version", nil, &got); code != http.StatusOK {
  		t.Fatalf("version -> %d", code)
  	}
  	if got.Version != "v6.0.0" || len(got.Commit) != 40 || got.BuildDate == "" || got.RepositoryURL == "" {
  		t.Fatalf("build info: %+v", got)
  	}
  	if len(got.Engines) != 2 || got.Engines[0].Version != "sha-f0ee3a1" || got.Engines[0].Count != 2 {
  		t.Fatalf("engine versions (most common first): %+v", got.Engines)
  	}
  }
  ```
- [ ] Run
      `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestDockerfilesStampBuildInfo -count=1; make webui-placeholder && go test ./mgmt/internal/api -run TestVersionEndpoint -count=1'`
      and expect FAIL: `lacks "ARG COMMIT="` and `undefined: api.Commit`.
- [ ] Implement `GetVersion`: `select engine_version, count(*) from engines where deleted_at is null and engine_version <> '' group by 1 order by 2 desc, 1`.
      Declare the vars in `mgmt/internal/api/version.go`, next to `GetVersion`. In `mgmt/cmd/nexora-mgmt/main.go`, add
      `var commit = ""` and `var buildDate = ""` next to `version`, and set `api.Commit`, `api.BuildDate`
      and `api.RepositoryURL = cfg.RepositoryURL` after loading the config. Read `NEXORA_REPOSITORY_URL`
      in `mgmt/internal/config/config.go` (optional, must start with `https://` when set). The `version`
      subcommand prints `nexora-mgmt %s %s\n` with version and commit.
- [ ] Dockerfiles:
  - In `deploy/docker/mgmt.Dockerfile`, the web stage gets `ARG VERSION=dev`, `ARG COMMIT=` and
    `ARG BUILD_DATE=`, and `RUN NEXORA_VERSION=${VERSION} NEXORA_COMMIT=${COMMIT} NEXORA_BUILD_DATE=${BUILD_DATE} pnpm run build`.
    The build stage gets the same args and
    `-ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildDate=${BUILD_DATE}"`.
  - In `deploy/docker/engine.Dockerfile`, add `ARG COMMIT=` and
    `NEXORA_VERSION="${VERSION}" NEXORA_COMMIT="${COMMIT}" cargo build ...`.
- [ ] In `scripts/build-image.sh`, compute `commit=$(git -C "$context" rev-parse HEAD 2>/dev/null || echo "")`
      and `build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)`, and add `--opt build-arg:COMMIT="$commit"` and
      `--opt build-arg:BUILD_DATE="$build_date"`. In `.github/workflows/images.yml`, step `build`, set:
  ```yaml
  build-args: |
    VERSION=${{ steps.meta.outputs.version }}
    COMMIT=${{ github.sha }}
    BUILD_DATE=${{ github.event.head_commit.timestamp }}
  ```
- [ ] In `web/vite.config.ts`, add
      `define: { __NEXORA_VERSION__: JSON.stringify(process.env.NEXORA_VERSION ?? "dev"), __NEXORA_COMMIT__: JSON.stringify(process.env.NEXORA_COMMIT ?? ""), __NEXORA_BUILD_DATE__: JSON.stringify(process.env.NEXORA_BUILD_DATE ?? "") },`.
      Create `web/src/build-info.d.ts`:
  ```ts
  declare const __NEXORA_VERSION__: string;
  declare const __NEXORA_COMMIT__: string;
  declare const __NEXORA_BUILD_DATE__: string;
  ```
- [ ] Run
      `scripts/dev-exec.sh 'go test ./deploy/deploytest -count=1 && make webui-placeholder && go test ./mgmt/internal/api -run "TestVersionEndpoint|TestCSRFAndDatabaseDown" -count=1 && cd web && pnpm run typecheck && pnpm run build'`
      and expect PASS, including `TestImagesWorkflow`.
- [ ] Report the paths. Commit message: `build: stamp version, commit and build date; GET /version`.

## Task 9: Collapsible filter categories

Files:

- `web/src/pages/FilterCategoriesPage.tsx`: collapsed rows, expand state, search, deep link.
- `web/e2e/screens/22-filter-categories.spec.ts`: extended.

Interfaces:

- Test ids:
  - `category-<key>` (region, unchanged), `category-toggle-<key>` (row header button with
    `aria-expanded`), `category-summary-<key>`, `categories-expand-all`, `categories-collapse-all`,
    `categories-search`.
  - The switch keeps the accessible name `Enable <name>`, and source rows keep `source-row-<key>`
    and `source-toggle-<key>`.
- localStorage key `nexora-categories-expanded`: a JSON array of category keys.

- [ ] Rewrite the start of the first test in `web/e2e/screens/22-filter-categories.spec.ts` so that,
      after the heading check and before `gambling()` assertions, it runs:
  ```ts
  // Collapsed by default: the source table is hidden, the summary and switch are in the row.
  await page.evaluate(() =>
    localStorage.removeItem("nexora-categories-expanded"),
  );
  await page.reload();
  await expect(page.getByTestId("category-summary-gambling")).toContainText(
    /\d+ of \d+ sources on/,
  );
  await expect(page.getByTestId("source-row-hagezi-gambling")).toBeHidden();
  await expect(page.getByTestId("category-toggle-gambling")).toHaveAttribute(
    "aria-expanded",
    "false",
  );
  await gamblingSwitch().click(); // toggling from the collapsed row
  await expect(gamblingSwitch()).toBeChecked();
  await gamblingSwitch().click();
  await expect(gamblingSwitch()).not.toBeChecked();
  await expect(page.getByTestId("source-row-hagezi-gambling")).toBeHidden();
  await page.getByTestId("category-toggle-gambling").click();
  await expect(page.getByTestId("category-toggle-gambling")).toHaveAttribute(
    "aria-expanded",
    "true",
  );
  await expect(page.getByTestId("source-row-hagezi-gambling")).toBeVisible();
  ```
  Keep the rest of the test, which toggles sources inside the expanded gambling category. Before the
  ads-tracking notice steps (which click a source toggle inside it), add
  `await page.getByTestId("category-toggle-ads-tracking").click();`. Append a new test:
  ```ts
  test("categories expand all, collapse all, remember state, search and deep link", async ({
    page,
  }) => {
    await login(
      page,
      env("NEXORA_E2E_VIEWER_USER"),
      env("NEXORA_E2E_VIEWER_PASSWORD"),
    );
    await page.goto("/filtering/categories");
    await page.getByTestId("categories-collapse-all").click();
    await expect(
      page.locator('[data-testid^="source-row-"]').first(),
    ).toBeHidden();
    await page.getByTestId("categories-expand-all").click();
    await expect(page.getByTestId("source-row-hagezi-gambling")).toBeVisible();
    await page.reload();
    await expect(page.getByTestId("source-row-hagezi-gambling")).toBeVisible();
    await page.getByTestId("categories-collapse-all").click();
    await page.reload();
    await expect(page.getByTestId("source-row-hagezi-gambling")).toBeHidden();
    await page.getByTestId("categories-search").fill("hagezi-tif");
    await expect(page.getByTestId("category-malware")).toBeVisible();
    await expect(page.getByTestId("category-gambling")).toBeHidden();
    await page.goto("/filtering/categories#category-malware");
    await expect(page.getByTestId("category-toggle-malware")).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    await page.setViewportSize({ width: 400, height: 800 });
    await expect(page.getByTestId("category-toggle-malware")).toBeVisible();
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    );
    expect(overflow).toBe(false);
  });
  ```
- [ ] Run
      `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m'`
      and expect FAIL in `22-filter-categories.spec.ts`: `category-summary-gambling` not found. A
      missing-operation failure for M6 operations is expected until Task 33.
- [ ] Implement in `web/src/pages/FilterCategoriesPage.tsx`:
  - `CategoryCard` renders a header row: a `<button data-testid="category-toggle-<key>" aria-expanded aria-controls="category-sources-<key>">`
    with a chevron (`ChevronRight`, rotated when open), the name `h2` and the description; a
    `<span data-testid="category-summary-<key>">` with `N of M sources on` plus total entries when
    known; the stale/error `StatusDot`; the non-commercial marker when any source has
    `commercial_use === false`; and the existing `Switch`, outside the button so it is reachable
    separately.
  - The source table renders only when expanded, inside `id="category-sources-<key>"`.
  - The page keeps `expanded: Set<string>` in state, initialised from localStorage key
    `nexora-categories-expanded` (`JSON.parse` in try/catch, ignoring non-arrays) plus the key from
    `location.hash` `#category-<key>`, and writes back on change in try/catch.
  - The toolbar has `categories-search` (an `Input` filtering by category name, key, source name or
    key, case-insensitive), `categories-expand-all` and `categories-collapse-all`.
  - A search match expands the matching categories without persisting them.
  - The header row uses `flex flex-wrap gap-2`, so it wraps at 400 px.
- [ ] Run the same `TestGUICoverage` command and expect `22-filter-categories.spec.ts` to pass. Run
      `cd web && pnpm run typecheck && pnpm run lint`.
- [ ] Report the paths. Commit message: `gui: collapsible filter categories`.

## Task 10: Engine metrics API

Files:

- `mgmt/internal/fleet/metrics.go`: created; the series from `Stats` samples.
- `mgmt/internal/fleet/metrics_test.go`: created.
- `mgmt/internal/api/engine_metrics.go`: `GetEngineMetrics` (replacing the stub).

Interfaces (consumed by Task 22 through `GET /engines/{id}/metrics`):

```go
type MetricsPoint struct {
	At time.Time
	QPS, P50Ms, P99Ms, CacheHitRatio, ServfailRatio, NXDomainRatio, RefusedRatio, BlockedQPS, CPUCores float64
	ResidentBytes, MemoryLimitBytes uint64
	Connections map[string]uint64
}
type UpstreamPoint struct { At time.Time; RTTMs, FailuresPerSecond, RaceWinsPerSecond float64 }
type EngineMetricsData struct {
	Points []MetricsPoint
	Upstreams map[string][]UpstreamPoint
	StartedAt *time.Time
	Restarts int
}
func EngineMetrics(ctx context.Context, q store.PolicyQuerier, engineID uuid.UUID, window time.Duration) (EngineMetricsData, error)
func percentile(bounds, a, b []uint64, p float64) float64 // shared with Task 18 through fleet.Percentile
func Percentile(bounds, cumulativeA, cumulativeB []uint64, p float64) float64
```

- [ ] Create `mgmt/internal/fleet/metrics_test.go`, following `TestSeriesDerivesRatesAndSkipsCounterResets`
      in `mgmt/internal/fleet/stats_test.go` for the store setup:
  ```go
  func TestEngineMetricsSeries(t *testing.T) {
  	st := storetest.New(t)
  	engineID := storetest.InsertEngine(t, st, "metrics", store.DefaultEngineGroupID)
  	bounds := []uint64{1000, 10000, 100000}
  	at := time.Now().Add(-4 * time.Minute)
  	sample := func(i int, queries, servfail, nx, refused, blocked uint64, started int64, cpu float64) {
  		s := &controlv1.Stats{
  			UnixMs: at.Add(time.Duration(i) * 10 * time.Second).UnixMilli(), QueriesTotal: queries, ServfailTotal: servfail,
  			FilterBlockedTotal: blocked, DurationBucketBoundsUs: bounds, DurationBucketCounts: []uint64{queries / 2, queries * 99 / 100, queries},
  			QueriesByRcode: map[string]uint64{"NXDOMAIN": nx, "REFUSED": refused, "SERVFAIL": servfail}, CacheHitsTotal: queries / 2, CacheMissesTotal: queries / 2,
  			StartedUnixMs: started, ProcessCpuSecondsTotal: cpu, ProcessResidentBytes: 100 << 20, MemoryLimitBytes: 1 << 30,
  			OpenConnections: map[string]uint64{"dot": 3},
  			Upstreams: []*controlv1.UpstreamStatus{{Name: "fx", RttUs: 2500, FailuresTotal: uint64(i), RaceWinsTotal: uint64(2 * i)}},
  		}
  		insertSample(t, st, engineID, at.Add(time.Duration(i)*10*time.Second), s)
  	}
  	sample(0, 1000, 0, 0, 0, 0, 1, 1.0)
  	sample(1, 2000, 10, 100, 50, 200, 1, 3.0)
  	sample(2, 50, 0, 0, 0, 0, 2, 0.1) // restart: counters went down, start time changed
  	sample(3, 1050, 10, 0, 0, 100, 2, 1.1)
  	d, err := fleet.EngineMetrics(context.Background(), st.Pool, engineID, 5*time.Minute)
  	if err != nil {
  		t.Fatal(err)
  	}
  	if len(d.Points) != 2 || d.Restarts != 1 {
  		t.Fatalf("points %d restarts %d", len(d.Points), d.Restarts)
  	}
  	p := d.Points[0]
  	if p.QPS != 100 || p.ServfailRatio != 0.01 || p.NXDomainRatio != 0.1 || p.RefusedRatio != 0.05 || p.BlockedQPS != 20 || p.CPUCores != 0.2 {
  		t.Fatalf("rates: %+v", p)
  	}
  	if p.P99Ms != 10 || p.P50Ms != 1 || p.Connections["dot"] != 3 || p.MemoryLimitBytes != 1<<30 {
  		t.Fatalf("latency/resources: %+v", p)
  	}
  	if u := d.Upstreams["fx"]; len(u) != 2 || u[0].RTTMs != 2.5 || u[0].FailuresPerSecond != 0.1 || u[0].RaceWinsPerSecond != 0.2 {
  		t.Fatalf("upstreams: %+v", d.Upstreams)
  	}
  	if d.StartedAt == nil || d.StartedAt.UnixMilli() != 2 {
  		t.Fatalf("started: %v", d.StartedAt)
  	}
  }
  ```
  Implement `insertSample(t, st *store.Store, id uuid.UUID, at time.Time, s *controlv1.Stats)` in the
  test file as `insert into engine_stats(engine_id, at, stats) values ($1, $2, $3)` with
  `proto.Marshal(s)`, as `TestSeriesDerivesRatesAndSkipsCounterResets` does inline.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/fleet -run TestEngineMetricsSeries -count=1'`
      and expect FAIL: `undefined: fleet.EngineMetrics`.
- [ ] Implement `mgmt/internal/fleet/metrics.go`:
  - Load samples ordered by `at` within the window.
  - For each consecutive pair with `b.QueriesTotal >= a.QueriesTotal` and the same `StartedUnixMs`,
    emit a point at `b.at` with rates `delta/seconds`: `QPS`, `BlockedQPS`, `CPUCores` (from the
    `ProcessCpuSecondsTotal` delta), and ratios over the query delta for SERVFAIL, NXDOMAIN and
    REFUSED from `QueriesByRcode`, with `ServfailTotal` as the fallback when the map is empty.
  - `P50Ms` and `P99Ms` come from `Percentile` over `DurationBucketCounts`; resources and connections
    come from `b`.
  - Otherwise count a restart when `StartedUnixMs` changed or counters went down.
  - Upstream points are per name, from the `RttUs/1000` gauge and the deltas of `FailuresTotal` and
    `RaceWinsTotal`.
  - `StartedAt` comes from the newest sample.
  - `Percentile` returns the first bound whose delta reaches `p` of the total delta, in ms.
  - `mgmt/internal/fleet/stats.go` stays unchanged: its `point` keeps its own p99 code.
- [ ] Implement `GetEngineMetrics`: windows `5m`, `1h` and `24h` (400 for others, default `1h`), 404
      through `getEngine`, `filter_index` via `fleet.LatestFilterIndex`, and the map to the generated
      `EngineMetrics`.
- [ ] Run
      `scripts/dev-exec.sh 'go test ./mgmt/internal/fleet -count=1 && make webui-placeholder && go test ./mgmt/internal/api -count=1'`
      and expect PASS.
- [ ] Report the paths. Commit message: `fleet: engine metrics series`.

## Task 11: Help foundation: tooltip component, catalogue, help pages, lint check

Files:

- `web/src/components/HelpTip.tsx`: created.
- `web/src/help/catalog/types.ts`, `web/src/help/catalog/index.ts`: created.
- Created as empty areas, filled by Tasks 28–32: `web/src/help/catalog/resolver.ts`,
  `web/src/help/catalog/filtering.ts`, `web/src/help/catalog/zones.ts`,
  `web/src/help/catalog/fleet.ts`, `web/src/help/catalog/admin.ts`.
- `web/src/help/topics/index.ts`: created; the topic list and imports with `?raw`.
- Topics, created with headings and first content from `docs/operations.md`:
  `web/src/help/topics/resolution.md`, `dnssec.md`, `filtering.md`, `zones.md`, `fleet.md`,
  `access-control.md`, `users.md`, `observability.md`.
- `web/src/pages/HelpPage.tsx`: created; `/help` index and `/help/:topic`.
- `web/src/components/ListEditor.tsx`: an optional `help` prop.
- `web/scripts/check-help.mjs`: created.
- `web/package.json`, `web/pnpm-lock.yaml`: the `react-markdown` dependency and the lint script.
- `deploy/deploytest/help_test.go`, `web/src/vite-env.d.ts`: created.

Interfaces (consumed by Tasks 24, 28–33):

```ts
// web/src/help/catalog/types.ts
export type HelpTopic =
  | "resolution"
  | "dnssec"
  | "filtering"
  | "zones"
  | "fleet"
  | "access-control"
  | "users"
  | "observability";
export type HelpEntry = {
  text: string;
  default?: string;
  range?: string;
  effect?: string;
  topic?: HelpTopic;
  anchor?: string;
};
export type HelpArea = { pages: string[]; entries: Record<string, HelpEntry> }; // pages: paths relative to web/src
// web/src/help/catalog/index.ts
export const help: Record<string, HelpEntry>; // merged areas; a duplicate id throws at module load
export function helpFor(id: string): HelpEntry | undefined;
// web/src/components/HelpTip.tsx
export function HelpTip(props: {
  id: string;
  label?: string;
  className?: string;
}): JSX.Element;
// renders <button type="button" aria-label={`Help: ${label ?? id}`} data-testid={`help-${id}`} aria-describedby={`help-text-${id}`}>
// with a Radix Tooltip (opens on hover and focus; tap toggles a controlled open state), content id `help-text-${id}`,
// side "top", collisionPadding 8, max-w-xs; a "Learn more" link to `/help/${topic}#${anchor}` when topic is set.
// web/src/pages/HelpPage.tsx
export default function HelpPage(): JSX.Element; // route element for "/help" and "/help/:topic"
// web/src/components/ListEditor.tsx: new optional prop  help?: string  (renders <HelpTip id={help}/> next to the input label)
```

The markdown topic files start with the line `<!-- operations: <heading text of docs/operations.md> -->`.

- [ ] Create `deploy/deploytest/help_test.go`:
  ```go
  package deploytest

  import (
  	"os"
  	"path/filepath"
  	"regexp"
  	"strings"
  	"testing"
  )

  var opsRef = regexp.MustCompile(`^<!-- operations: (.+) -->`)

  func TestHelpTopicsReferenceOperationsDoc(t *testing.T) {
  	root := filepath.Join("..", "..")
  	ops, err := os.ReadFile(filepath.Join(root, "docs/operations.md"))
  	if err != nil {
  		t.Fatal(err)
  	}
  	headings := map[string]bool{}
  	for _, l := range strings.Split(string(ops), "\n") {
  		if strings.HasPrefix(l, "#") {
  			headings[strings.TrimSpace(strings.TrimLeft(l, "#"))] = true
  		}
  	}
  	topics, _ := filepath.Glob(filepath.Join(root, "web/src/help/topics/*.md"))
  	if len(topics) < 8 {
  		t.Fatalf("want 8 help topics, have %d", len(topics))
  	}
  	for _, p := range topics {
  		b, _ := os.ReadFile(p)
  		m := opsRef.FindStringSubmatch(strings.SplitN(string(b), "\n", 2)[0])
  		if m == nil {
  			t.Errorf("%s does not start with <!-- operations: ... -->", filepath.Base(p))
  			continue
  		}
  		for _, h := range strings.Split(m[1], ";") {
  			if !headings[strings.TrimSpace(h)] {
  				t.Errorf("%s names missing docs/operations.md heading %q", filepath.Base(p), h)
  			}
  		}
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestHelpTopicsReferenceOperationsDoc -count=1'`
      and expect FAIL: `want 8 help topics, have 0`.
- [ ] Create the eight topic files. Each has the first line naming headings that exist in
      `docs/operations.md`:
  - `resolution.md`: `<!-- operations: Performance tuning; Known limitations -->`
  - `dnssec.md`: `<!-- operations: Key storage -->`
  - `filtering.md`: `<!-- operations: Filter categories; Filter index performance -->`
  - `zones.md`: `<!-- operations: Key storage -->`
  - `fleet.md`: `<!-- operations: Engine groups and staged rollouts; Engine lifecycle; Enrolling engines -->`
  - `access-control.md`: `<!-- operations: First-run setup and access -->`
  - `users.md`: `<!-- operations: First-run setup and access -->`
  - `observability.md`: `<!-- operations: Monitoring and alerts; Query logs, traces and OTLP -->`

  Each has an `# <Title>` heading and `##` sections whose ids are the anchors that later tasks use:
  - resolution: `resolution-mode`, `qname-minimisation`, `forward-zones`, `upstreams`, `strategy`,
    `parallel`
  - dnssec: `validation`, `trust-anchors`, `negative-trust-anchors`, `signing`, `rollovers`
  - filtering: `blocklists`, `categories-and-licenses`, `allowlist`, `policies`, `rewrites`,
    `safe-search`, `rpz`
  - zones: `records`, `transfers`, `tsig`, `dynamic-updates`, `zone-files`
  - fleet: `engine-groups`, `rollouts`, `certificates`, `engine-logs`
  - access-control: `recursion-access`, `authoritative-access`, `zone-allow-query`
  - users: `roles`, `api-tokens`, `oidc`, `account`
  - observability: `query-log`, `dashboard`, `metrics`, `traces`

  The text restates the facts of those operations sections in operator language. Task 33 checks
  that nothing contradicts them.

- [ ] Run `cd web && pnpm add react-markdown@10` and confirm the version resolved in `pnpm-lock.yaml`.
      Create `web/src/help/topics/index.ts`:
  ```ts
  import type { HelpTopic } from "../catalog/types";
  import resolution from "./resolution.md?raw";
  import dnssec from "./dnssec.md?raw";
  import filtering from "./filtering.md?raw";
  import zones from "./zones.md?raw";
  import fleet from "./fleet.md?raw";
  import accessControl from "./access-control.md?raw";
  import users from "./users.md?raw";
  import observability from "./observability.md?raw";

  export const topics: { key: HelpTopic; title: string; body: string }[] = [
    { key: "resolution", title: "Forwarding & recursion", body: resolution },
    { key: "dnssec", title: "DNSSEC", body: dnssec },
    { key: "filtering", title: "Filtering", body: filtering },
    { key: "zones", title: "Authoritative zones", body: zones },
    { key: "fleet", title: "Fleet", body: fleet },
    { key: "access-control", title: "Access control", body: accessControl },
    { key: "users", title: "Users and API tokens", body: users },
    {
      key: "observability",
      title: "Query log and observability",
      body: observability,
    },
  ];
  ```
  Create `web/src/vite-env.d.ts` containing `/// <reference types="vite/client" />`, so `?raw`
  imports typecheck. `web/src/build-info.d.ts` belongs to Task 8.
- [ ] Create `web/src/pages/HelpPage.tsx`. It renders `PageHeader title="Help"` with a topic list at
      `/help`. At `/help/:topic` it renders the markdown through `react-markdown`, with a `components.h2`
      override that sets `id` to the slugified heading text, then scrolls to `location.hash` after render.
      It uses `prose`-free Tailwind classes (`max-w-prose text-sm leading-6 [&_h2]:mt-6 [&_h2]:text-lg [&_code]:font-mono`),
      and an unknown topic shows `Navigate to="/help"`.
- [ ] Create `web/src/components/HelpTip.tsx` per Interfaces, using `Tooltip`, `TooltipTrigger` and
      `TooltipContent` from `@/components/ui/tooltip` and `Info` from `lucide-react`. Use a `useState`
      `open` with `onOpenChange`, plus `onClick={() => setOpen((o) => !o)}` for touch. The trigger is
      `h-4 w-4 text-muted-foreground`. Content shows `text`, then `Default: …`, `Range: …` and `Effect: …`
      lines when set, then the Learn more link. An unknown `id` renders nothing in production and throws
      in `import.meta.env.DEV`.
- [ ] Create the empty area files, for example `web/src/help/catalog/resolver.ts`:
  ```ts
  import type { HelpArea } from "./types";

  export const resolverHelp: HelpArea = { pages: [], entries: {} };
  ```
  Also create `filteringHelp`, `zonesHelp`, `fleetHelp` and `adminHelp`. `index.ts` merges their
  entries, throwing on a duplicate id, and exports `areas` for the check script.
- [ ] Create `web/scripts/check-help.mjs`:
  1. Parse each `web/src/help/catalog/*.ts` area file with regular expressions for `pages: [ ... ]`
     string literals and entry keys `"id": {` or `id: {`.
  2. For each listed page file, collect control ids: `id="x"` on `Input`, `SelectTrigger`, `Switch`,
     `Textarea`, `input` and `textarea` elements, `htmlFor="x"`, and `data-help="x"`.
  3. Require each id to be a catalogue key and to appear as `HelpTip id="x"` (or `help="x"` on
     `ListEditor`) in the same file.
  4. Print `check-help: <file>: control "<id>" has no help entry` or `... no HelpTip`, and exit 1 on
     any failure.
  5. With `--all`, also require every `web/src/pages/*.tsx` and `web/src/components/**/*.tsx` file
     containing one of those control patterns to be listed in some area.

  Change the `lint` script in `web/package.json` to
  `eslint . && node scripts/check-permissions.mjs && node scripts/check-help.mjs`.

- [ ] Add a self-test by running
      `cd web && printf 'import { Input } from "@/components/ui/input";\nexport const X = () => <Input id="probe-field" />;\n' > src/pages/ProbeHelp.tsx`,
      temporarily adding `"pages/ProbeHelp.tsx"` to `resolverHelp.pages`, then running
      `node scripts/check-help.mjs`. Expect exit 1 with `control "probe-field" has no help entry`. Then
      remove `src/pages/ProbeHelp.tsx` and restore `pages: []`.
- [ ] In `web/src/components/ListEditor.tsx`, add the optional `help?: string` prop and render
      `{help && <HelpTip id={help} label={inputLabel} />}` beside the input `Label`.
- [ ] Run
      `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestHelpTopicsReferenceOperationsDoc -count=1 && cd web && pnpm install --frozen-lockfile && pnpm run typecheck && pnpm run lint && pnpm run build'`
      and expect PASS.
- [ ] Report the paths. Commit message: `gui: help tooltip, catalogue, help pages and help lint`.

## Task 12: Engine attribution of allowed, rewritten and RPZ decisions

Files:

- `engine/src/filter/index.rs`: `ListHit.offset`, `FilterDecision::Allowed(ListHit)`,
  `FilterView.first_allow`, and the level passed to `for_each_match`'s visitor.
- `engine/src/filter/decisions.rs`: the offset and allow hits kept in the slot.
- `engine/src/filter.rs`: `Verdict::Allowed(ListHit)`, `Verdict::Rewrite` carrying the rule, and
  `RewriteTable::lookup` returning the matched base offset.
- `engine/src/server/mod.rs`: record source, list, rule and RPZ zone.
- `engine/src/server/rewrite.rs`: record the rewrite rule on the CNAME path.
- `engine/src/recursor/dispatch.rs`: RPZ zone index on response-phase hits.
- `engine/src/filter/synth.rs`: `view_with` for tests.
- `engine/tests/attribution.rs`, `engine/tests/pipeline/mod.rs`: created.
- `engine/src/filter/oracle.rs`: only the `FilterDecision::Allowed` pattern updates the compiler
  requires.

Interfaces (consumed by Task 16):

```rust
// crate::filter::index
pub struct ListHit { pub list: u16, pub set: u32, pub offset: u8 } // offset: octet index of the matched suffix in the wire name
pub enum FilterDecision { None, Blocked(ListHit), Allowed(ListHit) }
// crate::filter
pub enum Verdict<'a> { Pass, Allowed(ListHit), Blocked(ListHit), Rewrite { answer: &'a RewriteAnswer, offset: u8, wildcard: bool } }
impl RewriteTable { pub fn lookup(&self, wire_name: &[u8]) -> Option<(&RewriteAnswer, u8, bool)> }
// crate::server (private helper, same file)
fn record_hit(ctx: &WorkerCtx, policy: &EffectivePolicy, hit: ListHit, allowed: bool, rec: &mut QueryRecord);
// crate::recursor::dispatch  Answer gains  pub rpz_zone: u16  (NO_RPZ_ZONE when none)
```

- [ ] Create `engine/tests/attribution.rs`, following the snapshot setup of
      `engine/tests/policy_pipeline.rs` (`fake_upstream`, blob writing, `apply`, `spawn_workers`) and the
      record capture used by `engine/tests/telemetry_export.rs` (a collector `Sink` receiving OTLP logs):

  ```rust
  //! Attribution of blocked, allowed, rewritten and RPZ decisions, through the decision cache.
  use nexora_engine::filter::index::{FilterDecision, ListHit};
  use nexora_engine::filter::decisions::DecisionCache;
  use nexora_engine::filter::{domain_to_wire, synth};

  #[test]
  fn allowed_hits_carry_list_and_rule_through_the_decision_cache() {
      // One block list listing ads.example.test and one allow list listing ok.ads.example.test.
      let view = synth::view_with(&[("block-1", false, &["ads.example.test"]), ("allow-1", true, &["ok.ads.example.test"])]);
      let cache = DecisionCache::new(16);
      let blocked = domain_to_wire(b"x.ads.example.test").unwrap();
      let allowed = domain_to_wire(b"www.ok.ads.example.test").unwrap();
      for round in 0..2 {
          match cache.decide(&view, &blocked) {
              FilterDecision::Blocked(ListHit { offset, .. }) => assert_eq!(offset, 2, "round {round}: suffix after \\x01x"),
              other => panic!("round {round}: {other:?}"),
          }
          match cache.decide(&view, &allowed) {
              FilterDecision::Allowed(hit) => {
                  assert_eq!(hit.offset, 4, "round {round}: suffix after \\x03www");
                  assert_eq!(view.index().lists()[usize::from(hit.list)].id.as_ref(), "allow-1");
              }
              other => panic!("round {round}: {other:?}"),
          }
      }
      assert!(cache.hits() >= 2, "second round came from the cache");
  }

  #[test]
  fn rewrite_and_rpz_attribution_in_query_records() {
      let rig = pipeline::Rig::start(pipeline::RigOptions {
          rewrites: &[("rw.attr.test", "A", "192.0.2.55"), ("*.wild.attr.test", "A", "192.0.2.56")],
          rpz_file: Some("$ORIGIN rpz.attr.\nblocked.attr.test CNAME .\n"),
          ..Default::default()
      });
      rig.query_a("rw.attr.test.");
      rig.query_a("deep.x.wild.attr.test.");
      rig.query_a("blocked.attr.test.");
      let recs = rig.records(3);
      let by = |n: &str| recs.iter().find(|r| r.name == n).unwrap_or_else(|| panic!("{n} missing: {recs:?}"));
      assert_eq!((by("rw.attr.test.").source.as_str(), by("rw.attr.test.").rule.as_str()), ("rewrite", "rw.attr.test"));
      assert_eq!(by("deep.x.wild.attr.test.").rule, "*.wild.attr.test");
      let rpz = by("blocked.attr.test.");
      assert_eq!((rpz.source.as_str(), rpz.rpz_zone.as_str()), ("rpz", rig.rpz_zone_id()));
  }
  ```
  - Add the support module `engine/tests/pipeline/mod.rs`, created by this task and listed as
    `mod pipeline;` in `attribution.rs`. It has `Rig`, `RigOptions` and a `records(n)` helper that
    reads `LogRecord` attributes from an OTLP sink (the `Sink`/`LogsService` implementation copied
    from `engine/tests/telemetry_export.rs`) into
    `struct Rec { name, source, rule, rpz_zone: String }`.
  - Add `synth::view_with(&[(id, allow, names)]) -> FilterView` to `engine/src/filter/synth.rs`
    (owned by this task) using the same list building `synth` already offers for `filter_bench`.

- [ ] Run
      `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test attribution'` and expect FAIL: it
      does not compile, with `no field offset on type ListHit`.
- [ ] Implement:
  - **`engine/src/filter/index.rs`:**
    - `for_each_match(name_wire, visit: impl FnMut(u32, u8) -> bool)` passes the level's start
      offset (`Levels::start(level)` as `u8`).
    - `FilterView::decide` builds `ListHit { list, set, offset }`. Allow hits use `first_allow[set]`.
    - `FilterView::build` fills `first_allow` from the allow lists in position order, the same way
      `first_list` uses block lists.
    - `memory_bytes` includes `first_allow`.
  - **`engine/src/filter/decisions.rs`:**
    - The slot stores `kind: kind | offset << 2`, and `list` and `set` for both blocked and allowed.
    - The hit path rebuilds `Blocked` or `Allowed` from `slot.kind & 3` and `slot.kind >> 2`.
    - Keep `#[repr(C, align(64))]` and `const _: () = assert!(size_of::<Slot>() == 64);`.
  - **`engine/src/filter.rs`:**
    - `RewriteTable::lookup` returns the offset of the matched key: 0 for exact, `pos` of the wildcard
      base, with `wildcard: true`.
    - `Verdict` changes shape per Interfaces.
    - `EffectivePolicy::verdict` maps them.
  - **`engine/src/server/mod.rs`:**
    - `Verdict::Allowed(hit)` calls `record_hit(ctx, policy, hit, true, &mut rec)`, which sets
      `rec.filter = Allowed`, `filter_list`, `filter_generation`, `filter_rule_offset = hit.offset`
      and `filter_source = Allowlist`.
    - `record_block` becomes `record_hit(.., false, ..)`, which also sets
      `filter_source = if policy.filter().categories(hit) != 0 { Category } else { Blocklist }` and
      the offset.
    - The rewrite arms set `filter_source = Rewrite`, `filter_rule_offset = offset` and
      `rewrite_wildcard = wildcard`.
    - Every RPZ query-phase branch that sets `rpz_action` also sets
      `rec.rpz_zone = zone as u16` and `rec.filter_source = FilterSource::Rpz`. `note_answer` copies
      `ans.rpz_zone`, and sets `Rpz` when it is not `NO_RPZ_ZONE`.
  - **`engine/src/server/rewrite.rs`:** copy the offset and wildcard flag into its record.
  - **`engine/src/recursor/dispatch.rs`:** store the zone index wherever `rpz_action` is assigned on
    the response phase (lines 449-526).
- [ ] Run
      `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test attribution && cargo test --locked -p nexora-engine --lib filter:: && cargo test --locked -p nexora-engine --test hot_path_alloc && cargo test --locked -p nexora-engine --test telemetry_export'`
      and expect PASS, with `cache_hit_path_does_not_allocate`, `filter_index_matches_filterset_semantics`
      and `colliding_names_never_share_a_decision` all green.
- [ ] Run
      `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestQueryLogCategoryAttribution -count=1 -timeout 30m'`
      and expect PASS for `builtin` and `opensearch` with the Task 5 assertions.
- [ ] Run
      `scripts/dev-exec.sh 'cargo fmt --all && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`.
      Report the paths. Commit message: `engine: attribute allowed, rewritten and RPZ decisions`.

## Task 13: Engine log ring buffer and LogRequest handling

Files:

- `engine/src/telemetry/logbuf.rs`: created; ring, rate cap, redaction, request filtering.
- `engine/src/telemetry/mod.rs`: `pub mod logbuf;`.
- `engine/src/lib.rs`: the `eprintln!` shadow and the `log_*!` macros, declared before every `mod`.
- `engine/src/main.rs`: `use nexora_engine::eprintln;`.
- `engine/src/control.rs`: handle `ServerMsg::LogRequest`.
- `engine/src/telemetry/metrics.rs`: only `log_lines_dropped_total` in `Stats` and the
  `nexora_log_lines_dropped` counter registration.
- `engine/tests/logbuf.rs`: created.

Interfaces (consumed by Task 14 through the contract messages):

```rust
// crate::telemetry::logbuf
pub const CAPACITY: usize = 2000;
pub const LINE_MAX: usize = 512;
pub const RATE_PER_SEC: u32 = 100;
pub const BURST: u32 = 200;
pub struct LogBuffer { /* parking_lot::Mutex<Ring>, token bucket, dropped: AtomicU64 */ }
impl LogBuffer {
    pub fn new(capacity: usize, rate_per_sec: u32, burst: u32) -> LogBuffer;
    pub fn push(&self, level: LogLevel, message: &str, now_ms: i64);
    pub fn read(&self, req: &LogRequest) -> LogBatch; // lines with seq > after_seq, level <= min_level (ERROR=1 is most severe), contains (ASCII case-insensitive), at most limit (0 -> 1000)
    pub fn dropped(&self) -> u64;
}
pub static GLOBAL: std::sync::LazyLock<LogBuffer>;
pub fn redact(message: &str) -> std::borrow::Cow<'_, str>;
pub fn classify(message: &str) -> LogLevel;
pub fn emit(level: Option<LogLevel>, args: std::fmt::Arguments<'_>); // writes the line to stderr, then pushes the redacted line
// crate root macros (#[macro_export]): eprintln!, log_error!, log_warn!, log_info!, log_debug!
```

- [ ] Create `engine/tests/logbuf.rs`:
  ```rust
  use nexora_engine::proto::{LogLevel, LogRequest};
  use nexora_engine::telemetry::logbuf::{LogBuffer, classify, redact};

  #[test]
  fn log_buffer_is_bounded_and_rate_capped() {
      let b = LogBuffer::new(10, 5, 8);
      for i in 0..20 {
          b.push(LogLevel::Info, &format!("line {i}"), 1_000);
      }
      assert_eq!(b.dropped(), 12, "burst of 8 admitted in the first second, 12 dropped");
      for i in 0..20 {
          b.push(LogLevel::Info, &format!("later {i}"), 5_000 + i64::from(i as u8));
      }
      let all = b.read(&LogRequest { limit: 1000, min_level: LogLevel::Debug as i32, ..Default::default() });
      assert_eq!(all.lines.len(), 10, "ring holds at most its capacity");
      assert!(all.oldest_seq > 1, "old lines were overwritten");
      assert_eq!(all.last_seq, all.lines.last().unwrap().seq);
      let long = "x".repeat(2000);
      b.push(LogLevel::Warn, &long, 60_000);
      let tail = b.read(&LogRequest { after_seq: all.last_seq, limit: 5, min_level: LogLevel::Debug as i32, ..Default::default() });
      assert_eq!(tail.lines[0].message.len(), 512, "a line is truncated to 512 octets");
  }

  #[test]
  fn log_lines_are_redacted() {
      for (input, secret) in [
          ("join with nxj1.ABCDEFGHIJKLMNOP.0123abcd", "ABCDEFGHIJKLMNOP"),
          ("token nxt_ABCDEFGHIJKLMNOPQRSTUV used", "ABCDEFGHIJKLMNOPQRSTUV"),
          ("-----BEGIN PRIVATE KEY-----\nMIIEvQ\n-----END PRIVATE KEY-----", "MIIEvQ"),
          ("tsig secret=c2VjcmV0 password=hunter2hunter2", "hunter2hunter2"),
      ] {
          let out = redact(input);
          assert!(!out.contains(secret), "{input:?} -> {out:?}");
          assert!(out.contains("[redacted]"), "{out:?}");
      }
      assert_eq!(redact("serving version 7"), "serving version 7");
  }

  #[test]
  fn log_request_returns_filtered_lines_after_cursor() {
      let b = LogBuffer::new(100, 1000, 1000);
      b.push(classify("nexora-engine: serving version 3"), "nexora-engine: serving version 3", 1);
      b.push(classify("nexora-engine: snapshot rejected: bad cidr"), "nexora-engine: snapshot rejected: bad cidr", 2);
      b.push(LogLevel::Debug, "nexora-engine: debug detail", 3);
      b.push(LogLevel::Error, "nexora-engine: control stream error", 4);
      let warn = b.read(&LogRequest { min_level: LogLevel::Warn as i32, limit: 10, ..Default::default() });
      assert_eq!(warn.lines.iter().map(|l| l.seq).collect::<Vec<_>>(), vec![2, 4], "warn and error only");
      let search = b.read(&LogRequest { min_level: LogLevel::Debug as i32, contains: "SERVING".into(), limit: 10, ..Default::default() });
      assert_eq!(search.lines.len(), 1);
      let after = b.read(&LogRequest { after_seq: 2, min_level: LogLevel::Debug as i32, limit: 10, ..Default::default() });
      assert_eq!(after.lines.iter().map(|l| l.seq).collect::<Vec<_>>(), vec![3, 4]);
      assert_eq!(after.oldest_seq, 1);
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test logbuf'` and expect FAIL:
      `could not find logbuf in telemetry`.
- [ ] Implement `engine/src/telemetry/logbuf.rs`:
  - The ring is a `VecDeque<LogLine>` with capacity `CAPACITY`, plus `next_seq: u64` starting at 1.
  - The token bucket refills `rate_per_sec` per 1,000 ms up to `burst`. A push without a token adds 1
    to `dropped`.
  - `push` truncates to `LINE_MAX` octets at a char boundary.
  - `redact` replaces:
    - `nxj1\.[A-Za-z0-9]+` with `nxj1.[redacted]`;
    - `nxt_[A-Za-z0-9]+` with `nxt_[redacted]`;
    - `-----BEGIN [^-]+-----.*?-----END [^-]+-----` (dot matches newline) with `[redacted PEM]`,
      where the output keeps the `[redacted` prefix;
    - `(secret|password|key)=\S+` with `$1=[redacted]`.

    Use the `regex` crate if it is already in `engine/Cargo.toml`; otherwise hand-written scanning,
    with no new dependency.

  - `classify` implements the rule in the plan decisions.
  - `emit` formats into a stack `String`, which is acceptable because this is never on the query path,
    writes `eprintln` to stderr through `std::io::Write` on `stderr().lock()`, then pushes to `GLOBAL`
    with the redacted message.
  - `engine/src/lib.rs` declares, before the first `pub mod`:
    ```rust
    #[macro_export]
    macro_rules! eprintln {
        ($($arg:tt)*) => { $crate::telemetry::logbuf::emit(None, format_args!($($arg)*)) };
    }
    #[macro_export]
    macro_rules! log_error { ($($arg:tt)*) => { $crate::telemetry::logbuf::emit(Some($crate::proto::LogLevel::Error), format_args!($($arg)*)) }; }
    #[macro_export]
    macro_rules! log_warn { ($($arg:tt)*) => { $crate::telemetry::logbuf::emit(Some($crate::proto::LogLevel::Warn), format_args!($($arg)*)) }; }
    #[macro_export]
    macro_rules! log_info { ($($arg:tt)*) => { $crate::telemetry::logbuf::emit(Some($crate::proto::LogLevel::Info), format_args!($($arg)*)) }; }
    #[macro_export]
    macro_rules! log_debug { ($($arg:tt)*) => { $crate::telemetry::logbuf::emit(Some($crate::proto::LogLevel::Debug), format_args!($($arg)*)) }; }
    ```
    `emit` with `None` uses `classify`. Confirm with `cargo expand` or a unit test that an existing
    call such as `eprintln!("nexora-engine: tcp listener: {e}")` in `engine/src/server/tcp.rs` now
    reaches `GLOBAL`. Add the test `crate_eprintln_is_captured` in `engine/tests/logbuf.rs`, which
    calls `nexora_engine::eprintln!("nexora-engine: probe {}", 1)` and reads it back from
    `nexora_engine::telemetry::logbuf::GLOBAL`.
- [ ] In `engine/src/control.rs` `session`, add the receive arm:
  ```rust
              Some(ServerMsg::LogRequest(req)) => {
                  let batch = crate::telemetry::logbuf::GLOBAL.read(&req);
                  let _ = tx.try_send(EngineMessage { msg: Some(Msg::LogBatch(batch)) });
              }
  ```
  `read` echoes `request_id`. A full queue drops the reply, and the management plane times out with 504. Add `use nexora_engine::eprintln;` at the top of `engine/src/main.rs`. In
  `engine/src/telemetry/metrics.rs`, set `log_lines_dropped_total: logbuf::GLOBAL.dropped()` and
  register `nexora_log_lines_dropped` as a `ConstCounter`.
- [ ] Run
      `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test logbuf && cargo test --locked -p nexora-engine --test hot_path_alloc && cargo test --locked -p nexora-engine --test control_unit && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`
      and expect PASS.
- [ ] Report the paths. Commit message: `engine: bounded log ring buffer served over the control stream`.

## Task 14: Management plane engine log broker

Files:

- `mgmt/internal/control/logs.go`: created; `LogBroker`.
- `mgmt/internal/control/server.go`: the `LogBatch` receive case and the broker field.
- `mgmt/internal/control/hub.go`: the logs channels, the subscriber `logs` queue, and sending
  `LogRequest`.
- `mgmt/migrations/00603_engine_log_replies.sql`: created.
- `mgmt/internal/api/engine_logs.go`: `GetEngineLogs` (replacing the stub).
- `mgmt/cmd/nexora-mgmt/main.go`: wire the broker into the hub, server and `api.Deps.EngineLogs`.
- `mgmt/internal/control/logs_test.go`: created.

Interfaces (consumed by Task 22 through `GET /engines/{id}/logs`):

```go
// package control
const ChannelEngineLogs = "nexora_engine_logs"          // payload: {"request_id","engine_id","after_seq","min_level","limit","contains"}
const ChannelEngineLogsDone = "nexora_engine_logs_done" // payload: request id
type LogBroker struct { /* st *store.Store; mu; waiters map[string]chan *controlv1.LogBatch */ }
func NewLogBroker(st *store.Store) *LogBroker
func (b *LogBroker) Read(ctx context.Context, engineID uuid.UUID, req *controlv1.LogRequest) (*controlv1.LogBatch, error) // implements api.EngineLogReader
var ErrEngineDisconnected, ErrEngineTimeout = errors.New("engine is not connected"), errors.New("engine did not answer in time")
func (h *Hub) SetLogBroker(b *LogBroker)
func (s *Server) SetLogBroker(b *LogBroker)
```

`LogBroker.Read`:

1. Checks the engine is connected:
   `select connected_instance is not null and i.heartbeat_at > now() - interval '15 seconds' from engines e left join instances i on i.id = e.connected_instance where e.id = $1 and e.deleted_at is null`.
   A missing row gives `store.ErrNotFound`; false gives `ErrEngineDisconnected`.
2. Registers a waiter and sends `pg_notify(ChannelEngineLogs, json)`.
3. Waits up to 5 s for either the in-process waiter or `ChannelEngineLogsDone`.
4. On done, runs `delete from engine_log_replies where request_id = $1 returning batch` and
   unmarshals.

The hub, on `ChannelEngineLogs`, offers `LogRequest` to subscribers with `s.id == engine_id` through a
non-blocking `logs` channel of capacity 4. On a `LogBatch`, the server calls `broker.Deliver(batch)`:
an in-process waiter gets it directly; otherwise the broker inserts
`insert into engine_log_replies(request_id, batch) values ($1, $2) on conflict do nothing`, deletes
rows older than 60 s, and notifies `ChannelEngineLogsDone`.

- [ ] Create `mgmt/internal/control/logs_test.go` in package `control_test`, reusing `setupServers`,
      `enroll` and `recvMsg` from the package's tests:
  ```go
  func TestEngineLogsRoutedAcrossInstances(t *testing.T) {
  	brokers := []*control.LogBroker{}
  	f := setupServers(t, 2, func(st *store.Store, h *control.Hub) {
  		b := control.NewLogBroker(st)
  		brokers = append(brokers, b)
  		h.SetLogBroker(b)
  	}, func(s *control.Server) {
  		s.SetLogBroker(brokers[len(brokers)-1])
  	})
  	client, id := f.enroll(t, f.addr[1]) // the engine's stream lives on instance B
  	stream, err := client.Connect(f.ctx)
  	if err != nil {
  		t.Fatal(err)
  	}
  	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, NodeName: "e1", EngineVersion: "m6"}}})
  	recvSnapshot(t, stream)
  	go func() {
  		for {
  			m, err := stream.Recv()
  			if err != nil {
  				return
  			}
  			if req := m.GetLogRequest(); req != nil {
  				_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_LogBatch{LogBatch: &controlv1.LogBatch{
  					RequestId: req.RequestId, LastSeq: 7, OldestSeq: 1,
  					Lines: []*controlv1.LogLine{{Seq: 7, UnixMs: 1, Level: controlv1.LogLevel_LOG_LEVEL_INFO, Message: "nexora-engine: serving version 1 " + req.Contains}},
  				}}})
  			}
  		}
  	}()
  	engineID := uuid.MustParse(id)
  	var batch *controlv1.LogBatch
  	harness.Eventually(t, 10*time.Second, func() error {
  		var err error
  		batch, err = brokers[0].Read(f.ctx, engineID, &controlv1.LogRequest{Contains: "via-A", Limit: 10})
  		return err
  	})
  	if len(batch.Lines) != 1 || !strings.Contains(batch.Lines[0].Message, "via-A") {
  		t.Fatalf("batch through instance A: %+v", batch)
  	}
  	if _, err := brokers[1].Read(f.ctx, engineID, &controlv1.LogRequest{Limit: 10}); err != nil {
  		t.Fatalf("stream holder reads directly: %v", err)
  	}
  	silent, silentID := f.enroll(t, f.addr[0])
  	ss, _ := silent.Connect(f.ctx)
  	_ = ss.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: silentID, NodeName: "e2", EngineVersion: "m6"}}})
  	recvSnapshot(t, ss)
  	start := time.Now()
  	if _, err := brokers[1].Read(f.ctx, uuid.MustParse(silentID), &controlv1.LogRequest{Limit: 10}); !errors.Is(err, control.ErrEngineTimeout) || time.Since(start) < 4*time.Second {
  		t.Fatalf("silent engine -> %v after %s", err, time.Since(start))
  	}
  	gone, goneID := f.enroll(t, f.addr[0])
  	_ = gone
  	if _, err := brokers[0].Read(f.ctx, uuid.MustParse(goneID), &controlv1.LogRequest{Limit: 10}); !errors.Is(err, control.ErrEngineDisconnected) {
  		t.Fatalf("never-connected engine -> %v", err)
  	}
  }
  ```
  The two instance-hook closures run in instance order. When `setupServers` calls both hooks per
  instance in another order, index the broker by the `store.Store` pointer instead.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/control -run TestEngineLogsRoutedAcrossInstances -count=1'`
      and expect FAIL: `undefined: control.NewLogBroker`.
- [ ] Create `mgmt/migrations/00603_engine_log_replies.sql`:
  ```sql
  -- +goose Up
  -- Replies to engine log requests, handed from the instance holding the engine's stream to the
  -- instance that serves the HTTP request. Transient: unlogged, deleted when read, pruned after 60 s.
  CREATE UNLOGGED TABLE engine_log_replies (
      request_id uuid PRIMARY KEY,
      batch      bytea NOT NULL,
      created_at timestamptz NOT NULL DEFAULT now()
  );

  -- +goose Down
  DROP TABLE engine_log_replies;
  ```
- [ ] Implement `mgmt/internal/control/logs.go` per Interfaces.
  - In `mgmt/internal/control/hub.go`:
    - Listen also on the two channels.
    - In `handle`, process `ChannelEngineLogs` payloads as JSON before the uuid parse, calling
      `s.offerLog(req)` on matching subscribers.
    - Process `ChannelEngineLogsDone` by calling `broker.Done(requestID)`, which wakes a local waiter
      that then reads the table.
    - Add `logs chan *controlv1.LogRequest` (capacity 4) to `subscriber`, and a `case r := <-sub.logs`
      in the server's send goroutine that sends
      `&controlv1.ServerMessage{Msg: &controlv1.ServerMessage_LogRequest{LogRequest: r}}`.
  - In `mgmt/internal/control/server.go` `receive`, add
    `case *controlv1.EngineMessage_LogBatch: if s.logs != nil { s.logs.Deliver(ctx, m.LogBatch) }`.
- [ ] Implement `GetEngineLogs` in `mgmt/internal/api/engine_logs.go`:
  1. Parse `after`, `level` (default `debug`, mapped to `LOG_LEVEL_DEBUG`), `q` and `limit`
     (default 1000).
  2. Return 404 through `getEngine`.
  3. A nil `h.d.EngineLogs` gives `coded(501, "engine_unsupported", "this engine version cannot send logs")`.
  4. Map `control.ErrEngineDisconnected` to 409 `engine_disconnected` and `control.ErrEngineTimeout`
     to 504 `engine_timeout`. When the engine's `engine_version` is non-empty and not `dev`, and its
     Hello came from a build without log support (no reply within the timeout and the version sorts
     below the first M6 tag), return 501 `engine_unsupported`.
  5. Map lines to `EngineLogs` with `time.UnixMilli`.

  Wire it in `mgmt/cmd/nexora-mgmt/main.go`:
  `logs := control.NewLogBroker(st); hub.SetLogBroker(logs); controlServer.SetLogBroker(logs); deps.EngineLogs = logs`.

- [ ] Run
      `scripts/dev-exec.sh 'go test ./mgmt/internal/control -count=1 && make webui-placeholder && go test ./mgmt/internal/api -count=1'`
      and expect PASS.
- [ ] Report the paths. Commit message: `mgmt: route engine log requests over the control stream`.

## Task 15: Parallel upstream race in the engine

Files:

- `engine/src/upstream/mod.rs`: `forward` dispatches `Strategy::Parallel` to `race`; `settle`, `owned_attempt`, `Waiters::len`.
- `engine/src/upstream/udp.rs`: `UdpPool::pending`.
- `engine/src/snapshot.rs`: reject `parallel_max > 8`.
- `engine/tests/upstream_parallel.rs`: created.

Interfaces (consumed by Tasks 16, 17):

```rust
pub const PARALLEL_LIMIT: usize = 8;
pub fn settle(health: &Health, result: &Result<Duration, UpstreamError>, now: u32); // record_success/record_failure + queries counter
async fn race(set: &UpstreamSet, worker: &WorkerUpstreams, query: &[u8], question: &Question, max: usize) -> Result<Forwarded, UpstreamError>;
async fn owned_attempt(transport: Rc<Transport>, query: Bytes, question: Question, timeout: Duration) -> Result<Bytes, UpstreamError>; // 'static, used by race
impl WorkerUpstreams { pub fn pending_waiters(&self) -> usize } // sum of UDP pool waiters, for tests
fn acceptable(response: &[u8]) -> bool; // rcode NOERROR (0) or NXDOMAIN (3), TC clear
```

- [ ] Create `engine/tests/upstream_parallel.rs`. Copy the helpers `query`, `counter` and `local`
      from `engine/tests/upstream_udp_tcp.rs`, as test crates cannot share private helpers, and add:
  ```rust
  /// A UDP fake upstream: answers every query after `delay` with `rcode`, counting queries.
  async fn fake(delay: Duration, rcode: u8) -> (SocketAddr, Arc<AtomicU64>) {
      let sock = Arc::new(UdpSocket::bind("127.0.0.1:0").await.unwrap());
      let addr = sock.local_addr().unwrap();
      let count = Arc::new(AtomicU64::new(0));
      let c = count.clone();
      tokio::task::spawn_local(async move {
          let mut buf = [0u8; 1500];
          loop {
              let Ok((n, peer)) = sock.recv_from(&mut buf).await else { return };
              c.fetch_add(1, Ordering::Relaxed);
              let mut reply = buf[..n].to_vec();
              reply[2] |= 0x80; // QR
              reply[3] = (reply[3] & 0xf0) | rcode;
              let s = sock.clone();
              tokio::task::spawn_local(async move {
                  tokio::time::sleep(delay).await;
                  let _ = s.send_to(&reply, peer).await;
              });
          }
      });
      (addr, count)
  }

  fn spec(id: &str, addr: SocketAddr) -> UpstreamSpec {
      UpstreamSpec { id: id.into(), name: id.into(), protocol: Protocol::Udp, addr: Some(addr), tls_server_name: String::new(),
          doh_url: String::new(), timeout: Duration::from_millis(400), ca_pem: String::new() }
  }

  #[tokio::test(flavor = "current_thread")]
  async fn parallel_returns_first_valid_answer_and_updates_every_upstream() {
      local(async {
          let (fast, fast_n) = fake(Duration::from_millis(5), 0).await;
          let (slow, slow_n) = fake(Duration::from_millis(120), 0).await;
          let (fail, fail_n) = fake(Duration::from_millis(1), 2).await; // SERVFAIL, fastest of all
          let set = UpstreamSet::new(vec![spec("slow", slow), spec("fail", fail), spec("fast", fast)], Strategy::Parallel { max: 0 }, None);
          let worker = WorkerUpstreams::new(counter());
          let (q, question) = query("race.example.");
          let started = std::time::Instant::now();
          let got = upstream::forward(&set, &worker, &q, &question).await.unwrap();
          assert_eq!(set.specs[got.upstream_index].id, "fast");
          assert_eq!(got.raced, 3);
          assert!(started.elapsed() < Duration::from_millis(100), "did not wait for the slow upstream");
          tokio::time::sleep(Duration::from_millis(300)).await; // losers drain
          for (i, n) in [(0, &slow_n), (1, &fail_n), (2, &fast_n)] {
              assert_eq!(n.load(Ordering::Relaxed), 1, "{} received the query", set.specs[i].id);
              assert!(set.health[i].queries.load(Ordering::Relaxed) + set.health[i].failures.load(Ordering::Relaxed) >= 1, "{} health updated", set.specs[i].id);
          }
          assert!(set.health[0].ewma_rtt_us.load(Ordering::Relaxed) >= 100_000, "the slow loser's RTT was recorded");
          assert_eq!(set.health[2].race_wins.load(Ordering::Relaxed), 1);
          assert_eq!(worker.pending_waiters(), 0, "no waiter leaked");

          // Fast upstream gone: the slow NOERROR wins over the early SERVFAIL.
          let set2 = UpstreamSet::new(vec![spec("slow", slow), spec("fail", fail)], Strategy::Parallel { max: 0 }, None);
          let (q2, question2) = query("race2.example.");
          let got2 = upstream::forward(&set2, &worker, &q2, &question2).await.unwrap();
          assert_eq!(set2.specs[got2.upstream_index].id, "slow");
          assert_eq!(got2.response[3] & 0x0f, 0);
          tokio::time::sleep(Duration::from_millis(50)).await;
          assert_eq!(worker.pending_waiters(), 0);
      })
      .await;
  }

  #[tokio::test(flavor = "current_thread")]
  async fn parallel_max_limits_to_lowest_rtt() {
      local(async {
          let (a, a_n) = fake(Duration::from_millis(1), 0).await;
          let (b, b_n) = fake(Duration::from_millis(1), 0).await;
          let (c, c_n) = fake(Duration::from_millis(1), 0).await;
          let set = UpstreamSet::new(vec![spec("a", a), spec("b", b), spec("c", c)], Strategy::Parallel { max: 2 }, None);
          set.health[0].record_success(Duration::from_millis(40));
          set.health[1].record_success(Duration::from_millis(5));
          set.health[2].record_success(Duration::from_millis(20));
          let worker = WorkerUpstreams::new(counter());
          let (q, question) = query("max.example.");
          let got = upstream::forward(&set, &worker, &q, &question).await.unwrap();
          assert_eq!(got.raced, 2);
          tokio::time::sleep(Duration::from_millis(50)).await;
          assert_eq!((a_n.load(Ordering::Relaxed), b_n.load(Ordering::Relaxed), c_n.load(Ordering::Relaxed)), (0, 1, 1));
      })
      .await;
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test upstream_parallel'` and
      expect FAIL:
      `assertion left == right failed: left: 1, right: 3` on `got.raced`, because the variant still
      forwards like `Fastest`.
- [ ] Implement `race` in `engine/src/upstream/mod.rs`:
  1. `set.order(now, &mut order)`, keep the indexes whose `health.admit(now)` is true, and truncate
     to `if max == 0 { PARALLEL_LIMIT } else { max.min(PARALLEL_LIMIT) }`. An empty list returns
     `NoneAvailable`, and a single candidate goes through the existing ordered path.
  2. `let (tx, mut rx) = tokio::sync::mpsc::channel(PARALLEL_LIMIT);`. For each candidate, clone
     `worker.transport(spec)` (`Rc<Transport>`), `Bytes::copy_from_slice(query)` once shared by all,
     `set.health[i].clone()` and the timeout `spec.timeout.min(remaining(start)?)`. Then
     `tokio::task::spawn_local(async move { let t0 = Instant::now(); let r = owned_attempt(transport, q, question, timeout).await; settle(&health, &r.as_ref().map(|_| t0.elapsed()).map_err(Clone::clone), clock::now_secs()); let _ = tx.send((i, r, t0.elapsed())).await; })`.
     Make `UpstreamError: Clone`, which its variants allow.
  3. Drop the original `tx`, then loop on `rx.recv()` until `None`:
     - `Ok(resp)` with `acceptable(&resp)`: add 1 to `set.health[i].race_wins`, observe the race
       duration into `RACE`, and return `Ok(Forwarded { response: resp, upstream_index: i, rtt, raced: n })`.
     - `Ok(resp)` not acceptable: keep it as `best` when `best` holds no response.
     - `Err(e)`: keep `e` as `last_err`.

     When the channel closes, return `best` as `Ok(Forwarded{..})` so the client sees the upstream's
     SERVFAIL or REFUSED as with ordered, otherwise `Err(last_err)`, or `Deadline` when the overall
     deadline passed.

  4. `owned_attempt` is the body of today's `attempt` over an owned transport: UDP `pool.exchange`
     with TC fallback to `tcp::exchange_tcp`, TCP, DoT and DoH. `attempt` calls it with a borrowed
     `Rc` clone, so the ordered and fastest paths share the code.

  Refactor `forward`'s success and failure recording into `settle` without changing ordered or
  fastest behaviour. `settle` increments `queries` for every completed attempt, so
  `nexora_upstream_queries_total` counts every attempt, as the spec says. `pending_waiters` sums the
  waiter counts of the worker's UDP pools. Add `pub fn pending(&self) -> usize` to
  `engine/src/upstream/udp.rs`, returning the sum of `waiters.len()` over its sockets (add `len()` to
  `Waiters` in `engine/src/upstream/mod.rs`), and add `engine/src/upstream/udp.rs` to this task's
  files.

- [ ] In `engine/src/snapshot.rs` `validate`, add
      `if s.resolver.as_ref().is_some_and(|r| r.parallel_max > 8) { return Err(SnapshotError::Invalid("parallel_max must be 0..=8".into())); }`,
      and add a case in `engine/tests/snapshot_apply.rs` only if that file already has a table of invalid
      snapshots. Otherwise add `parallel_max_above_eight_is_rejected` to `engine/tests/upstream_parallel.rs`.
- [ ] Run
      `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test upstream_parallel && cargo test --locked -p nexora-engine --test upstream_udp_tcp && cargo test --locked -p nexora-engine --test server_pipeline && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`
      and expect PASS.
- [ ] Report the paths. Commit message: `engine: parallel upstream strategy`.

## Task 16: Access control split in the engine, hot-path proof and e2e

Files:

- `engine/src/acl.rs`: `allows_all`, `any()`.
- `engine/src/runtime.rs`: `authoritative_acl`.
- `engine/src/snapshot.rs`: validation of the new CIDR fields.
- `engine/src/authoritative/zone.rs`, `engine/src/authoritative/loader.rs`: per-zone `allow_query` and
  `update_allow`.
- `engine/src/authoritative/dispatch.rs`: authoritative ACL check and refusal record.
- `engine/src/authoritative/update.rs`: source check.
- `engine/src/authoritative/update_tests.rs`: `update_source_cidrs_refuse_outside_senders`.
- `engine/src/server/mod.rs`: recursion refusal record.
- `engine/tests/authoritative_acl.rs`: created.
- `engine/tests/hot_path_alloc.rs`: extended.
- `e2e/access_split_test.go`: created.
- `e2e/gui_seed_access_test.go`: created; zone and ACL data for spec 30, run by Task 20's spec.

Interfaces:

```rust
impl Acl { pub fn any() -> Acl; pub fn allows_all(&self) -> bool } // allows_all: contains 0.0.0.0/0 and ::/0
// Runtime gains  pub authoritative_acl: Acl  (Acl::any() when !authoritative_acl_set)
// Zone gains  pub allow_query: Option<Acl>, pub update_allow: Option<Acl>  (None = inherit / any)
```

- [ ] Create `engine/tests/authoritative_acl.rs`. Copy `start_zone` and `ask` from
      `engine/tests/authoritative_pipeline.rs`, parameterised as
      `start_split(recursion: &str, authoritative: Option<&str>, zone_allow: &[&str])`, which sets
      `authoritative_allow_cidrs`, `authoritative_acl_set = authoritative.is_some()` and
      `AuthZone.allow_query_cidrs`:
  ```rust
  #[test]
  fn authoritative_acl_order_and_ra() {
      // Client is 127.0.0.1. Recursion ACL excludes it; authoritative default is "any".
      let srv = start_split("10.0.0.0/8", Some("0.0.0.0/0"), &[]);
      let (hosted, _) = ask(srv, "www.example.test.", false);
      assert_eq!(hosted.metadata.response_code, ResponseCode::NoError);
      assert!(!hosted.metadata.recursion_available, "RA only for recursion clients");
      let (other, _) = ask(srv, "www.example.org.", false);
      assert_eq!(other.metadata.response_code, ResponseCode::Refused, "no recursion for outside clients");
      // Zone override excludes the client: hosted names are refused too.
      let srv = start_split("10.0.0.0/8", Some("0.0.0.0/0"), &["192.168.0.0/16"]);
      assert_eq!(ask(srv, "www.example.test.", false).0.metadata.response_code, ResponseCode::Refused);
      // Inside client (recursion allowed) with a zone override that allows it: answer with RA.
      let srv = start_split("127.0.0.0/8", Some("192.0.2.0/24"), &["127.0.0.1/32"]);
      let (inside, _) = ask(srv, "www.example.test.", false);
      assert_eq!(inside.metadata.response_code, ResponseCode::NoError);
      assert!(inside.metadata.recursion_available);
  }

  #[test]
  fn snapshot_without_authoritative_acl_answers_every_client() {
      let srv = start_split("10.0.0.0/8", None, &[]); // pre-M6 management: authoritative_acl_set false
      assert_eq!(ask(srv, "www.example.test.", false).0.metadata.response_code, ResponseCode::NoError);
      let srv = start_split("10.0.0.0/8", Some("192.0.2.0/24"), &[]);
      assert_eq!(ask(srv, "www.example.test.", false).0.metadata.response_code, ResponseCode::Refused, "the set flag applies the list");
  }
  ```
  In `engine/src/authoritative/update_tests.rs`, add `update_source_cidrs_refuse_outside_senders`,
  following the existing signed-update test at line 50. It sends the same signed UPDATE with the zone's
  `update_allow` set to `192.0.2.0/24` from `127.0.0.1` and expects REFUSED, then with `127.0.0.0/8`
  and expects NOERROR (positive path first).
- [ ] Extend `cache_hit_path_does_not_allocate` in `engine/tests/hot_path_alloc.rs`:
  - set `authoritative_allow_cidrs: vec!["0.0.0.0/0".into(), "::/0".into()]` and
    `authoritative_acl_set: true`;
  - use `resolver: Some(ResolverConfig { strategy: UpstreamStrategy::Parallel as i32, parallel_max: 2, ..Default::default() })`;
  - add `ok.ads.hot.test` to the group allowlist, and add a measured query for it next to the blocked
    one through `measure_blocked`'s loop;
  - add a hosted-zone measurement through `measure_auth` with the "any" ACL.

  The assertions stay `allocations == 0`.

- [ ] Run
      `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test authoritative_acl; cargo test --locked -p nexora-engine --test hot_path_alloc'`
      and expect FAIL: `assertion left == right failed: left: NoError, right: Refused` in
      `authoritative_acl_order_and_ra`, because the zone override is ignored.
- [ ] Implement:
  - `Acl::any()`, and `allows_all()`, computed once in `parse` when both zero-length prefixes are
    present.
  - `Runtime::build_with` parses `authoritative_allow_cidrs` when `authoritative_acl_set`, else
    `Acl::any()`. `engine/src/snapshot.rs` validates the list and every zone's two lists with
    `Acl::parse`.
  - `engine/src/authoritative/loader.rs` builds `allow_query` and `update_allow` from the non-empty
    lists.
  - In `engine/src/authoritative/dispatch.rs`, change `fast` and `signed_query` to take
    `rec: &mut QueryRecord` from `handle_packet`. After the zone is found and before transfers and
    answers:
    ```rust
        let auth_acl = zone.allow_query.as_ref().unwrap_or(&rt.authoritative_acl);
        if !auth_acl.allows_all() && !auth_acl.allows(client.ip()) {
            rec.acl_refused = ACL_AUTHORITATIVE;
            rec.filter_source = FilterSource::Acl;
            let n = wire::write_rcode_reply(view, wire::RCODE_REFUSED, out, opt);
            return AuthOutcome::Reply(n);
        }
    ```
    Match the argument names of `wire::write_rcode_reply` used in `engine/src/server/mod.rs`
    (`&q, wire::RCODE_REFUSED, &mut out[..limit], opt.as_ref()`). The AXFR/IXFR paths keep their
    `transfer_allow` check unchanged and are not subject to allow-query.
  - In `engine/src/server/mod.rs`, the recursion refusal sets `rec.acl_refused = ACL_RECURSION` and
    `rec.filter_source = FilterSource::Acl` before `reply`. The comment "Hosted zones answer every
    client" becomes "Hosted zones are checked against their allow-query ACL; the recursion ACL
    guards everything else."
  - In `engine/src/authoritative/update.rs` `respond`, after zone lookup:
    `if zone.update_allow.as_ref().is_some_and(|a| !a.allows(client.ip())) { return refused }`, and
    count it in `AUTH_UPDATE_RESULTS` `refused`.
- [ ] Create `e2e/access_split_test.go`:
  ```go
  func TestAuthoritativeAccessSplit(t *testing.T) {
  	a := startAuthEnv(t, nil, "acl-engine")
  	createPrimaryZone(t, a.api, "split.test.", map[string]any{})
  	addRecord(t, a.api, "split.test.", "www", "A", "192.0.2.10")
  	// Recursion access: only 127.0.0.2/32; authoritative default: any.
  	var ac struct {
  		AllowCidrs []string `json:"allow_cidrs"`
  		Revision   int64    `json:"revision"`
  	}
  	a.api.Must("GET", "/access-control", nil, &ac, 200)
  	a.api.Must("PUT", "/access-control", map[string]any{"allow_cidrs": []string{"127.0.0.2/32"}, "authoritative_allow_cidrs": []string{"0.0.0.0/0", "::/0"}, "revision": ac.Revision}, nil, 200)
  	waitLatestApplied(t, a.api, "acl-engine")
  	inside, outside := net.ParseIP("127.0.0.2"), net.ParseIP("127.0.0.3")
  	transports := map[string]func(ip net.IP, name string) *dns.Msg{
  		"udp": func(ip net.IP, n string) *dns.Msg { return udpFrom(t, ip, a.eng.DNS, n, dns.TypeA) },
  		"tcp": func(ip net.IP, n string) *dns.Msg { return tcpFrom(t, ip, a.eng.DNS, n, dns.TypeA) },
  		"dot": func(ip net.IP, n string) *dns.Msg { return dotFrom(t, a, ip, n) },
  		"doh": func(ip net.IP, n string) *dns.Msg { return dohFrom(t, a, ip, n) },
  	}
  	for name, ask := range transports {
  		t.Run(name, func(t *testing.T) {
  			if r := ask(inside, "www.split.test."); r.Rcode != dns.RcodeSuccess || !r.RecursionAvailable {
  				t.Fatalf("inside hosted: %v", r)
  			}
  			if r := ask(inside, harness.UniqueName("rec")); r.Rcode != dns.RcodeSuccess {
  				t.Fatalf("inside recursion: %v", r)
  			}
  			if r := ask(outside, "www.split.test."); r.Rcode != dns.RcodeSuccess || r.RecursionAvailable {
  				t.Fatalf("outside hosted under any: %v", r)
  			}
  			if r := ask(outside, harness.UniqueName("rec")); r.Rcode != dns.RcodeRefused {
  				t.Fatalf("outside recursion must be refused: %v", r)
  			}
  		})
  	}
  	var z struct {
  		ID       string `json:"id"`
  		Revision int64  `json:"revision"`
  	}
  	a.api.Must("GET", "/zones/by-name/split.test.", nil, &z, 200)
  	a.api.Must("PATCH", "/zones/"+z.ID, map[string]any{"revision": z.Revision, "allow_query_cidrs": []string{"127.0.0.2/32"}}, nil, 200)
  	waitLatestApplied(t, a.api, "acl-engine")
  	for name, ask := range transports {
  		if r := ask(inside, "www.split.test."); r.Rcode != dns.RcodeSuccess {
  			t.Fatalf("%s inside after override: %v", name, r)
  		}
  		if r := ask(outside, "www.split.test."); r.Rcode != dns.RcodeRefused {
  			t.Fatalf("%s outside after override must be refused: %v", name, r)
  		}
  	}
  	harness.EventuallyTrue(t, 30*time.Second, func() bool {
  		var page struct{ Records []struct{ Source, Rule, Client string } `json:"records"` }
  		a.api.Must("GET", "/query-log?source=acl&limit=100", nil, &page, 200)
  		kinds := map[string]bool{}
  		for _, r := range page.Records {
  			kinds[r.Rule] = true
  		}
  		return kinds["recursion"] && kinds["authoritative"]
  	}, "query log records both refusal kinds")
  	if v := a.eng.Metric(t, "nexora_acl_refused_total", map[string]string{"acl": "authoritative"}); v < 4 {
  		t.Fatalf("nexora_acl_refused_total{acl=authoritative} = %v", v)
  	}
  }
  ```
  Implement the helpers `tcpFrom`, `dotFrom` and `dohFrom` in `e2e/access_split_test.go`. Model
  `tcpFrom` on `udpFrom` in `e2e/per_client_policy_test.go`, using a `net.Dialer{LocalAddr: &net.TCPAddr{IP: ip}}`
  with `dns.Client{Net: "tcp", Dialer: ...}`. Build `dotFrom`/`dohFrom` from
  `harness.EncryptedClient{LocalIP: ip, RootCAs: ..., ServerName: ...}` as the `doh-policy-uses-tcp-peer`
  subtest does. Start the engine with `harness.EngineOptions{DoT: true, DoH: true}` by extending
  `startAuthEnv`'s call inside this file through `env.StartManagedEngineWith`. Look up the zone id
  with `GET /zones` and a name match if `/zones/by-name` is not an operation in
  `mgmt/api/openapi.yaml`. The query-log rule for ACL refusals is `recursion` or `authoritative` per
  the plan decisions.
- [ ] Create `e2e/gui_seed_access_test.go`, which registers a seed that creates the primary zone
      `gui-acl.test.` (with `createPrimaryZone`) and sets `vars["NEXORA_E2E_ACL_ZONE"] = "gui-acl.test."`.
- [ ] Run
      `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --all-targets && make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run "TestAuthoritativeAccessSplit|TestAuthoritativeZonePropagation|TestAXFRIXFROut|TestSecondaryAndDynamicUpdate" -count=1 -timeout 40m'`
      and expect PASS.
- [ ] Run `scripts/dev-exec.sh 'cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`.
      Report the paths. Commit message: `engine: split recursion and authoritative access, per-zone allow-query`.

## Task 17: Parallel strategy in the management plane and its e2e proof

Files:

- `mgmt/migrations/00601_parallel_strategy.sql`: created.
- `mgmt/internal/api/handlers_dns.go`: only `resolverColumns`, `scanResolver`, `validateResolver` and
  `UpdateResolverSettings`.
- `mgmt/internal/snapshot/snapshot.go`: `strategies` map and `parallel_max`.
- `e2e/upstream_parallel_test.go`: created.

Interfaces:

- `resolver_settings.parallel_max`.
- `ResolverSettings.ParallelMax *int` in the generated Go type.
- Snapshot `Resolver.ParallelMax`.

- [ ] Create `e2e/upstream_parallel_test.go`:
  ```go
  func TestUpstreamParallelStrategy(t *testing.T) {
  	env := harness.New(t)
  	pg := env.StartPostgres()
  	mgmt := env.StartMgmt(pg, env.InitCA(), harness.MgmtOptions{})
  	api := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
  	api.DisableForwardedValidation()
  	slow, fast := env.StartDNSFixture(), env.StartDNSFixture()
  	slow.SetDelay(t, 150*time.Millisecond)
  	api.Must("POST", "/upstreams", map[string]any{"name": "slow", "protocol": "udp", "address": slow.UDP, "timeout_ms": 1000, "enabled": true, "position": 0}, nil, 201)
  	api.Must("POST", "/upstreams", map[string]any{"name": "fast", "protocol": "udp", "address": fast.UDP, "timeout_ms": 1000, "enabled": true, "position": 1}, nil, 201)
  	eng := env.StartManagedEngine("par-engine", []string{mgmt.GRPCURL}, api.CreateJoinToken())
  	var rs map[string]any
  	api.Must("GET", "/resolver-settings", nil, &rs, 200)
  	rs["strategy"], rs["parallel_max"] = "parallel", 2
  	api.Must("PUT", "/resolver-settings", rs, &rs, 200)
  	if rs["strategy"] != "parallel" || rs["parallel_max"].(float64) != 2 {
  		t.Fatalf("settings: %v", rs)
  	}
  	rs["parallel_max"] = 9
  	if code, _ := api.ErrorCode("PUT", "/resolver-settings", rs); code != 400 {
  		t.Fatalf("parallel_max 9 -> %d", code)
  	}
  	v := api.LatestVersion()
  	api.WaitEngine("par-engine", 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v })
  	name := harness.UniqueName("race")
  	start := time.Now()
  	if r := harness.MustQuery(t, eng.DNS, name, dns.TypeA, harness.QueryOpts{}); r.Rcode != dns.RcodeSuccess {
  		t.Fatalf("race query: %v", r)
  	}
  	if d := time.Since(start); d > 120*time.Millisecond {
  		t.Fatalf("parallel waited for the slow upstream: %s", d)
  	}
  	harness.EventuallyTrue(t, 5*time.Second, func() bool {
  		return eng.Metric(t, "nexora_upstream_race_wins_total", map[string]string{"upstream": "fast"}) >= 1
  	}, "race win counted for fast")
  	harness.EventuallyTrue(t, 30*time.Second, func() bool {
  		var page struct{ Records []struct{ Upstream string; UpstreamsRaced int `json:"upstreams_raced"` } `json:"records"` }
  		api.Must("GET", "/query-log?limit=5&name="+strings.TrimSuffix(name, "."), nil, &page, 200)
  		return len(page.Records) == 1 && page.Records[0].Upstream == "fast" && page.Records[0].UpstreamsRaced == 2
  	}, "query log shows the winner and the raced count")
  }

  func TestUpstreamParallelLatency(t *testing.T) {
  	env := harness.New(t)
  	pg := env.StartPostgres()
  	mgmt := env.StartMgmt(pg, env.InitCA(), harness.MgmtOptions{})
  	api := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
  	api.DisableForwardedValidation()
  	slow, fast := env.StartDNSFixture(), env.StartDNSFixture()
  	slow.SetDelay(t, 150*time.Millisecond)
  	api.Must("POST", "/upstreams", map[string]any{"name": "slow", "protocol": "udp", "address": slow.UDP, "timeout_ms": 1000, "enabled": true, "position": 0}, nil, 201)
  	api.Must("POST", "/upstreams", map[string]any{"name": "fast", "protocol": "udp", "address": fast.UDP, "timeout_ms": 1000, "enabled": true, "position": 1}, nil, 201)
  	eng := env.StartManagedEngine("lat-engine", []string{mgmt.GRPCURL}, api.CreateJoinToken())
  	measure := func(strategy string) (p95, p99 time.Duration) {
  		var rs map[string]any
  		api.Must("GET", "/resolver-settings", nil, &rs, 200)
  		rs["strategy"], rs["parallel_max"] = strategy, 0
  		api.Must("PUT", "/resolver-settings", rs, nil, 200)
  		v := api.LatestVersion()
  		api.WaitEngine("lat-engine", 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v })
  		var d []time.Duration
  		for i := 0; i < 2000; i++ {
  			_, rtt, err := harness.Query(t, eng.DNS, harness.UniqueName("lat-"+strategy), dns.TypeA, harness.QueryOpts{Timeout: 3 * time.Second})
  			if err != nil {
  				t.Fatalf("%s query %d: %v", strategy, i, err)
  			}
  			d = append(d, rtt)
  		}
  		slices.Sort(d)
  		return d[len(d)*95/100], d[len(d)*99/100]
  	}
  	o95, o99 := measure("ordered")
  	p95, p99 := measure("parallel")
  	t.Logf("ordered p95 %s p99 %s; parallel p95 %s p99 %s (uncached, one upstream +150 ms)", o95, o99, p95, p99)
  	if p95 >= o95 || p99 >= o99 {
  		t.Fatalf("parallel is not faster: ordered p95 %s p99 %s, parallel p95 %s p99 %s", o95, o99, p95, p99)
  	}
  }
  ```
  `SetDelay` wraps the fixture's `POST /delay` control endpoint (`e2e/fixtures/cmd/nexora-fixture/dns.go`
  line 144). Use the existing harness wrapper if `e2e/harness/fixture.go` has one; otherwise add
  `func (f *DNSFixture) SetDelay(t *testing.T, d time.Duration)` to `e2e/harness/fixture.go` and add
  that path to Files.
- [ ] Run
      `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestUpstreamParallelStrategy -count=1 -timeout 20m'`
      and expect FAIL: `PUT /resolver-settings -> 500` (the database check rejects `parallel`).
- [ ] Create `mgmt/migrations/00601_parallel_strategy.sql`:
  ```sql
  -- +goose Up
  ALTER TABLE resolver_settings DROP CONSTRAINT IF EXISTS resolver_settings_strategy_check;
  ALTER TABLE resolver_settings
      ADD CONSTRAINT resolver_settings_strategy_check CHECK (strategy IN ('ordered', 'fastest', 'parallel')),
      ADD COLUMN parallel_max integer NOT NULL DEFAULT 0 CHECK (parallel_max BETWEEN 0 AND 8);

  -- +goose Down
  UPDATE resolver_settings SET strategy = 'fastest' WHERE strategy = 'parallel';
  ALTER TABLE resolver_settings DROP COLUMN parallel_max;
  ALTER TABLE resolver_settings DROP CONSTRAINT resolver_settings_strategy_check;
  ALTER TABLE resolver_settings ADD CONSTRAINT resolver_settings_strategy_check CHECK (strategy IN ('ordered', 'fastest'));
  ```
  Confirm the constraint name with
  `scripts/dev-exec.sh "psql ... -c '\d resolver_settings'"` against a migrated test database (the
  inline check in `00001_init.sql` is named `resolver_settings_strategy_check` by PostgreSQL).
- [ ] In `mgmt/internal/api/handlers_dns.go`:
  - Add `parallel_max` to `resolverColumns`/`scanResolver`.
  - Have `validateResolver` reject `ParallelMax` outside 0..8 with
    `invalid("parallel_max must be between 0 and 8")` and give the strategy message
    `strategy must be ordered, fastest or parallel`.
  - `UpdateResolverSettings` writes `parallel_max = coalesce($N, parallel_max)`.

  In `mgmt/internal/snapshot/snapshot.go`, add `"parallel": controlv1.UpstreamStrategy_UPSTREAM_STRATEGY_PARALLEL`
  to `strategies`, select `parallel_max`, and set `snap.Resolver.ParallelMax`.

- [ ] Run
      `scripts/dev-exec.sh 'make webui-placeholder && go test ./mgmt/internal/api ./mgmt/internal/snapshot -count=1 && make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run "TestUpstreamParallelStrategy|TestUpstreamParallelLatency|TestUpstreamFailover" -count=1 -timeout 40m -v'`
      and expect PASS, with the latency log line recorded in the task's todo evidence.
- [ ] Report the paths. Commit message: `mgmt: parallel strategy setting and e2e latency proof`.

## Task 18: Dashboard backend: series, top lists, health

Files:

- `mgmt/migrations/00604_engine_stats_rollup.sql`: created.
- `mgmt/internal/stats/stats.go`: `Record` also writes the rollup and prunes it.
- `mgmt/internal/stats/dashboard_series.go`: created.
- `mgmt/internal/stats/dashboard_health.go`: created.
- `mgmt/internal/stats/dashboard_test.go`: created.
- `mgmt/internal/querylog/backend.go`, `mgmt/internal/querylog/builtin.go`,
  `mgmt/internal/querylog/opensearch.go`: `Top`.
- `mgmt/internal/querylog/top_test.go`: created.
- `mgmt/internal/api/dashboard_m6.go`: the three handlers.
- `mgmt/internal/api/dashboard_m6_test.go`: created.

Interfaces (consumed by Task 25 through the API):

```go
// package querylog
type TopField string // "name" | "client" | "category"
type TopQuery struct { From, To time.Time; Field TopField; Filters []string; Limit int }
type TopEntry struct { Key string; Count int64 }
type Topper interface { Top(ctx context.Context, q TopQuery) ([]TopEntry, error) } // builtin and OpenSearch implement it; Noop does not
// package stats
type Range string // "15m" "1h" "6h" "24h" "7d"
func DashboardSeries(ctx context.Context, q store.PolicyQuerier, r Range, now time.Time) (SeriesData, error)
func DashboardHealth(ctx context.Context, q store.PolicyQuerier, now time.Time) (HealthData, error)
```

- [ ] Create `mgmt/internal/querylog/top_test.go`:
  ```go
  func TestBuiltinTop(t *testing.T) {
  	b := querylog.NewBuiltin(20)
  	req := request("a.test.", "a.test.", "b.test.", "blocked.test.", "blocked.test.", "blocked.test.")
  	for _, lr := range req.ResourceLogs[0].ScopeLogs[0].LogRecords[3:] {
  		lr.Attributes = append(lr.Attributes, str("nexora.filter.result", "blocked"), str("nexora.filter.category", "ads-tracking"))
  	}
  	b.Ingest("e1", req)
  	top := func(q querylog.TopQuery) []querylog.TopEntry {
  		q.Limit = 2
  		out, err := b.Top(context.Background(), q)
  		if err != nil {
  			t.Fatal(err)
  		}
  		return out
  	}
  	if got := top(querylog.TopQuery{Field: "name"}); got[0].Key != "blocked.test." || got[0].Count != 3 || got[1].Key != "a.test." {
  		t.Fatalf("top names: %+v", got)
  	}
  	if got := top(querylog.TopQuery{Field: "name", Filters: []string{"blocked"}}); len(got) != 1 {
  		t.Fatalf("top blocked: %+v", got)
  	}
  	if got := top(querylog.TopQuery{Field: "category"}); len(got) != 1 || got[0].Key != "ads-tracking" {
  		t.Fatalf("top categories skip empty keys: %+v", got)
  	}
  }
  ```
  Add `TestOpenSearchTopAggregation` to the same file. It asserts the request body contains
  `"aggs":{"top":{"terms":{"field":"attributes.dns.question.name.keyword","size":2}}}` and `"size":0`,
  and decodes the `aggregations.top.buckets` of a fake response. A 500 response gives
  `ErrBackendUnavailable`.
- [ ] Create `mgmt/internal/stats/dashboard_test.go` with `TestDashboardAggregations`:
  - Use `storetest.New`, two engines via `storetest.InsertEngine`, and one connected via
    `storetest.ConnectEngine`.
  - Insert raw `engine_stats` samples 10 s apart for the last 2 minutes, with `Stats` M6 maps set
    (`QueriesByTransport`, `QueriesByRcode`, `AnswersByRoute`, `MissDurationBucketCounts`,
    `FilterRewrittenTotal`, `Dnssec{Secure,Insecure,Bogus}`, `Recursion{UpstreamQueries,UpstreamTimeouts,LameMarked}`,
    `FilterIndex.BlockedByCategory`, `TlsCertificateNotAfterUnix: now+3 days`,
    `Upstreams{Name:"fx", Up:false}`, `ExportDroppedTotal{"logs": 5}` increasing).
  - Insert `engine_stats_rollup` rows at 1 h spacing over 6 days.
  - Mark one `filter_categories` source stale through the store function `TestCollectorExportsCategoryStaleness`
    uses.

  Assert:
  - `DashboardSeries(15m)` has points whose `QPS`, `QPSByTransport["udp"]`, `QPSByRcode["NXDOMAIN"]`,
    `P95Ms`, `MissP95Ms`, `AnswersByRoute["cache"]`, `DNSSECBogusQPS` and
    `BlockedByCategory["ads-tracking"]` equal the rates computed by hand from the inserted deltas;
    `StepSeconds == 10`; and per-engine `CacheBytes`.
  - `DashboardSeries(7d)` reads the rollup, with `StepSeconds == 3600` and more than 100 points.
  - `DashboardHealth` has alerts of every kind: `engine_disconnected` for the unconnected engine,
    `category_stale`, `upstream_down` for `fx`, `certificate_expiring` with severity `critical` under
    7 days, `trust_anchor_refresh_failed` when a `TrustAnchorStatus.LastError` is set, and
    `export_dropped` when `ExportDroppedTotal` grew. Also check the groups table counts.

  Write concrete expected numbers in the test from the inserted samples. For example, 1,000 more
  queries per 10 s gives `QPS == 100`.

  Create `mgmt/internal/api/dashboard_m6_test.go` with `TestDashboardTopBackendUnavailable`:
  `newAPIWith` sets `d.QueryLog` to a backend whose `Top` returns `querylog.ErrBackendUnavailable`,
  and `GET /dashboard/top?range=1h` must return 200 with `"available":false` and empty lists. A `Noop`
  backend also gives `available:false`, and `range=2d` gives 400.

- [ ] Run
      `scripts/dev-exec.sh 'go test ./mgmt/internal/querylog ./mgmt/internal/stats -run "Top|TestDashboardAggregations" -count=1; make webui-placeholder && go test ./mgmt/internal/api -run TestDashboardTopBackendUnavailable -count=1'`
      and expect FAIL: `b.Top undefined` and `undefined: stats.DashboardSeries`.
- [ ] Create `mgmt/migrations/00604_engine_stats_rollup.sql`:
  ```sql
  -- +goose Up
  -- The newest engine Stats sample per engine per 5 minutes, kept 8 days for the 7-day dashboard range.
  CREATE TABLE engine_stats_rollup (
      engine_id uuid NOT NULL REFERENCES engines (id) ON DELETE CASCADE,
      bucket    timestamptz NOT NULL,
      stats     bytea NOT NULL,
      PRIMARY KEY (engine_id, bucket)
  );

  -- +goose Down
  DROP TABLE engine_stats_rollup;
  ```
- [ ] Implement:
  - **`stats.Record`:** in the same call, run
    `insert into engine_stats_rollup(engine_id, bucket, stats) values ($1, date_bin('5 minutes', now(), timestamptz 'epoch'), $2) on conflict (engine_id, bucket) do update set stats = excluded.stats`.
    On the existing prune tick, also delete rollup rows older than 8 days.
  - **`DashboardSeries`:**
    - Steps are 15m→10 s, 1h→30 s, 6h→3 min, 24h→10 min, and 7d→1 h from the rollup.
    - Per engine, compute rates between consecutive samples inside each step bucket, skipping counter
      resets as `fleet.Series` does.
    - Sum rates across engines per bucket.
    - Percentiles use `fleet.Percentile` over the summed bucket deltas.
    - Ratios are summed hits over summed lookups.
    - Per-engine cache and filter index figures come from each engine's newest sample.
  - **`DashboardHealth`:**
    - Engines use the status rules of `mgmt/internal/fleet/engines.go` (reuse its engine view query
      function), with QPS, p99 and hit ratio from `fleet.Series(ctx, q, id, 1*time.Minute)`'s last
      point.
    - Groups come from `engine_groups` with counts.
    - The alert rules are listed in the test above.
    - Certificate expiry: `warning` under 14 days, `critical` under 7.
  - **`Top`:**
    - Builtin scans the ring newest first within From/To, counts with a map, and sorts by count desc
      then key.
    - OpenSearch runs `size:0` with a `terms` aggregation on the keyword field (name → `dns.question.name`,
      client → `client.address`, category → `nexora.filter.category`), the same time and filter
      clauses as `Search`, and `min_doc_count: 1`, dropping the empty key.
  - **`mgmt/internal/api/dashboard_m6.go`:** validate the range; `GetDashboardTop` runs four `Top`
    calls (domains, blocked domains, clients, categories). `available` is false when the backend is not
    a `querylog.Topper` or returns `ErrBackendUnavailable`.
- [ ] Run
      `scripts/dev-exec.sh 'go test ./mgmt/internal/querylog ./mgmt/internal/stats -count=1 && make webui-placeholder && go test ./mgmt/internal/api -count=1'`
      and expect PASS.
- [ ] Report the paths. Commit message: `mgmt: dashboard series, top lists and health`.

## Task 19: Query log GUI: partial names, multi-select, URL state, Reason column

Files:

- `web/src/pages/QueryLogPage.tsx`: filters, URL state, Reason column, row detail, time preferences.
- `web/src/components/MultiSelect.tsx`: created.
- `web/e2e/screens/10-query-log.spec.ts`: partial-name search.
- `web/e2e/screens/23-querylog-category.spec.ts`: the category filter through the multi-select.
- `web/e2e/screens/25-querylog-filters.spec.ts`: created.
- `web/e2e/screens/26-querylog-reason.spec.ts`: created.
- `e2e/gui_seed_querylog_test.go`: created; seeds typed and allowlisted queries.

Interfaces:

```ts
// web/src/components/MultiSelect.tsx
export function MultiSelect(props: {
  id: string; // data-testid of the trigger; options get `${id}-option-${value}`; clear button `${id}-clear`; search `${id}-search`
  label: string;
  options: { value: string; label?: string }[];
  value: string[];
  onChange: (next: string[]) => void;
  searchable?: boolean; // default: options.length > 8
}): JSX.Element; // trigger text: "Any" | "A, AAAA" (<= 2 values) | "3 selected"
```

- URL parameters mirror the API: `name`, `client`, and repeated `qtype`, `rcode`, `cache`, `filter`,
  `category`, `source`, `list_id`, `policy_group` and `engine_id`.
- Test ids:
  - The existing `querylog-*` ids, plus `querylog-source`, `querylog-policy-group`,
    `querylog-engine`, `querylog-reason` (cell), `querylog-detail` (expanded row) and
    `querylog-row-toggle`.
  - Seed vars: `NEXORA_E2E_QL_MULTI_PREFIX` (a name prefix whose A, AAAA and MX queries the seed made,
    with NOERROR answers) and `NEXORA_E2E_ALLOW_QUERY_NAME` (a query allowed by the global allowlist
    entry `allow.gui.test`).

- [ ] Create `e2e/gui_seed_querylog_test.go`:
  ```go
  package e2e

  import (
  	"strings"
  	"time"

  	"github.com/miekg/dns"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  func init() { registerGUISeed(seedQueryLog) }

  func seedQueryLog(s guiSeedEnv) {
  	prefix := strings.TrimSuffix(harness.UniqueName("qlmulti"), ".example.")
  	harness.MustQuery(s.T, s.Engine.DNS, prefix+"-a.example.", dns.TypeA, harness.QueryOpts{})
  	harness.MustQuery(s.T, s.Engine.DNS, prefix+"-aaaa.example.", dns.TypeAAAA, harness.QueryOpts{})
  	harness.MustQuery(s.T, s.Engine.DNS, prefix+"-mx.example.", dns.TypeMX, harness.QueryOpts{})
  	s.Vars["NEXORA_E2E_QL_MULTI_PREFIX"] = prefix
  	var allow struct {
  		Domains  []string `json:"domains"`
  		Revision int64    `json:"revision"`
  	}
  	s.Admin.Must("GET", "/allowlist", nil, &allow, 200)
  	s.Admin.Must("PUT", "/allowlist", map[string]any{"domains": append(allow.Domains, "allow.gui.test"), "revision": allow.Revision}, nil, 200)
  	name := "www.allow.gui.test."
  	harness.EventuallyTrue(s.T, 30*time.Second, func() bool {
  		return harness.MustQuery(s.T, s.Engine.DNS, name, dns.TypeA, harness.QueryOpts{}).Rcode == dns.RcodeSuccess
  	}, "allowlisted name resolves")
  	s.Vars["NEXORA_E2E_ALLOW_QUERY_NAME"] = strings.TrimSuffix(name, ".")
  }
  ```
  (`getAllowlist`/`updateAllowlist`, schema `Allowlist {domains, revision}`). An allowlist match is
  attributed as `allowlist` whether or not a block list also lists the name.
- [ ] Update `web/e2e/screens/10-query-log.spec.ts`. After the existing search, add:
  ```ts
  const full = env("NEXORA_E2E_QUERY_NAME");
  const fragment = full.slice(2, 10).toUpperCase();
  await page.getByTestId("querylog-name").fill(fragment);
  await expect(page.getByTestId("querylog-name")).toHaveAttribute(
    "placeholder",
    /part of a name/i,
  );
  await page.getByTestId("querylog-search").click();
  await expect(
    page.getByTestId("querylog-row").filter({ hasText: full }).first(),
  ).toBeVisible();
  ```
  Create `web/e2e/screens/25-querylog-filters.spec.ts`:
  ```ts
  import { test, expect, env, login } from "../fixtures";

  test("query log multi-select filters combine and round-trip through the URL", async ({
    page,
  }) => {
    await login(
      page,
      env("NEXORA_E2E_OPERATOR_USER"),
      env("NEXORA_E2E_OPERATOR_PASSWORD"),
    );
    const prefix = env("NEXORA_E2E_QL_MULTI_PREFIX");
    await page.getByTestId("nav-query-log").click();
    await page.getByTestId("querylog-name").fill(prefix);
    await page.getByTestId("querylog-qtype").click();
    await page.getByTestId("querylog-qtype-option-A").click();
    await page.getByTestId("querylog-qtype-option-AAAA").click();
    await page.keyboard.press("Escape");
    await expect(page.getByTestId("querylog-qtype")).toContainText("A, AAAA");
    await page.getByTestId("querylog-rcode").click();
    await page.getByTestId("querylog-rcode-option-NOERROR").click();
    await page.getByTestId("querylog-rcode-option-NXDOMAIN").click();
    await page.keyboard.press("Escape");
    await page.getByTestId("querylog-search").click();
    const rows = page.getByTestId("querylog-row").filter({ hasText: prefix });
    await expect(rows).toHaveCount(2);
    await expect(rows.filter({ hasText: "-mx." })).toHaveCount(0);
    await expect(page).toHaveURL(/qtype=A&qtype=AAAA/);
    await page.reload();
    await expect(page.getByTestId("querylog-qtype")).toContainText("A, AAAA");
    await expect(
      page.getByTestId("querylog-row").filter({ hasText: prefix }),
    ).toHaveCount(2);
    await page.getByTestId("querylog-qtype").click();
    await page.getByTestId("querylog-qtype-clear").click();
    await page.keyboard.press("Escape");
    await page.getByTestId("querylog-search").click();
    await expect(
      page.getByTestId("querylog-row").filter({ hasText: prefix }),
    ).toHaveCount(3);
    await expect(page).not.toHaveURL(/qtype=/);
    await page.setViewportSize({ width: 400, height: 800 });
    await expect(page.getByTestId("querylog-search")).toBeVisible();
  });
  ```
  Create `web/e2e/screens/26-querylog-reason.spec.ts`:
  ```ts
  import { test, expect, env, login } from "../fixtures";

  test("query log shows the decision reason and row detail", async ({
    page,
  }) => {
    await login(
      page,
      env("NEXORA_E2E_OPERATOR_USER"),
      env("NEXORA_E2E_OPERATOR_PASSWORD"),
    );
    await page.getByTestId("nav-query-log").click();
    await page
      .getByTestId("querylog-name")
      .fill(env("NEXORA_E2E_CATEGORY_QUERY_NAME"));
    await page.getByTestId("querylog-search").click();
    const blocked = page.getByTestId("querylog-row").first();
    await expect(blocked.getByTestId("querylog-reason")).toContainText(
      /Category · .+ · malware/,
    );
    await blocked.getByTestId("querylog-row-toggle").click();
    await expect(page.getByTestId("querylog-detail")).toContainText(
      "malware.gui.test",
    );
    await expect(page.getByTestId("querylog-detail")).toContainText("Global");

    await page
      .getByTestId("querylog-name")
      .fill(env("NEXORA_E2E_ALLOW_QUERY_NAME"));
    await page.getByTestId("querylog-source").click();
    await page.getByTestId("querylog-source-option-allowlist").click();
    await page.keyboard.press("Escape");
    await page.getByTestId("querylog-search").click();
    await expect(
      page.getByTestId("querylog-row").first().getByTestId("querylog-reason"),
    ).toContainText("Allowlist · Global allowlist · allow.gui.test");
    await expect(page).toHaveURL(/source=allowlist/);
  });
  ```
  In `web/e2e/screens/23-querylog-category.spec.ts`, replace
  `getByRole("option", { name: "malware", exact: true })` with
  `page.getByTestId("querylog-category-option-malware")`, followed by `page.keyboard.press("Escape")`.
- [ ] Run
      `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m'`
      and expect FAIL in `25-querylog-filters.spec.ts`: `querylog-qtype-option-A` not found. Operations
      that only later tasks cover may also be listed as missing.
- [ ] Create `web/src/components/MultiSelect.tsx` from Radix `Dialog`-free primitives. Use a `Popover`
      built from the existing `web/src/components/ui/select.tsx` styling, or a button plus
      `role="listbox"` panel: a checkbox per option (`role="option"`, `aria-selected`), a search input when
      `searchable`, a clear button, closing on Escape and outside click, and the trigger text rules from
      Interfaces. It is keyboard reachable (arrow keys move focus, Space toggles), and the panel is
      `max-h-72 overflow-auto` and full width under 640 px.
- [ ] In `web/src/pages/QueryLogPage.tsx`:
  - `type Filters` holds `string[]` for every select field. State is read from and written to
    `useSearchParams` on search, reset and paging; a `useEffect` re-applies it on URL change. The
    cursor stays in state.
  - `FilterSelect` is replaced by `MultiSelect` for type, response, cache, filter, category, source
    (options `blocklist, category, allowlist, rpz, rewrite, acl`), policy group (options from
    `GET /policy-groups` plus `global`) and engine (options from `useEngines`).
  - The API call passes arrays (openapi-fetch serialises repeated parameters for `explode: true`).
  - The Name input gets `placeholder="Part of a name, e.g. youtube"`.
  - Column "Category" is replaced by "Reason": `querylog-reason` shows
    `<Source label> · <list_name | rpz_zone_name | rule> · <category>`. Labels are:
    - `blocklist` → "Blocklist";
    - `category` → "Category";
    - `allowlist` → "Allowlist · <list_name> · <rule>";
    - `rpz` → "RPZ · <rpz_zone_name> · <rpz_action>";
    - `rewrite` → "Rewrite · <rule> → <rewrite_answer>";
    - `acl` → "Refused · <rule> access";
    - otherwise "—".
  - A `querylog-row-toggle` button (chevron, `aria-expanded`) in the first cell opens a full-width
    `querylog-detail` row listing rule, list name and id, policy group (name or "Global"), RPZ zone and
    action, upstream and `upstreams_raced`, and engine id.
  - Timestamps use `formatTimestamp(new Date(r.time), usePreferences())` from
    `web/src/lib/preferences.ts`, and the initial `live` state is `usePreferences().querylog_live`.
  - The table keeps horizontal scroll inside its own container at 400 px.
- [ ] Run the same `TestGUICoverage` command and expect specs 10, 23, 24, 25 and 26 to pass. Run
      `cd web && pnpm run typecheck && pnpm run lint`.
- [ ] Report the paths. Commit message: `gui: query log multi-select, URL state and decision reason`.

## Task 20: Access control GUI and zone allow-query editor

Files:

- `web/src/pages/AccessControlPage.tsx`: two ACL sections and the zone summary.
- `web/src/pages/ZoneTransfersTab.tsx`: allow-query and update sources.
- `web/e2e/screens/03-access-control.spec.ts`: new headings.
- `web/e2e/screens/30-access-control-split.spec.ts`: created.

Interfaces:

- Test ids:
  - `acl-*` for recursion access (unchanged).
  - `authacl-input`, `authacl-add`, `authacl-save`, `authacl-row-<cidr>` for authoritative access
    (the `ListEditor` `prefix="authacl"`).
  - `zone-access-summary`, and `zone-access-row-<zone name>` inside it.
  - `zone-allow-query` (input), `zone-update-allow` (input).
- Section headings: "Recursion and resolver access", "Authoritative query access", "Zone transfers and
  updates".

- [ ] Create `web/e2e/screens/30-access-control-split.spec.ts`:
  ```ts
  import { test, expect, env, login } from "../fixtures";

  test("operator edits recursion and authoritative access and a zone override", async ({
    page,
  }) => {
    await login(
      page,
      env("NEXORA_E2E_OPERATOR_USER"),
      env("NEXORA_E2E_OPERATOR_PASSWORD"),
    );
    await page.getByTestId("nav-access-control").click();
    await expect(
      page.getByRole("heading", { name: "Recursion and resolver access" }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Authoritative query access" }),
    ).toBeVisible();
    await expect(page.getByTestId("authacl-row-0.0.0.0/0")).toBeVisible();
    await page.getByTestId("authacl-input").fill("198.51.100.0/24");
    await page.getByTestId("authacl-add").click();
    await page.getByTestId("authacl-save").click();
    await expect(page.getByText("Authoritative access saved")).toBeVisible();
    await page.reload();
    await expect(page.getByTestId("authacl-row-198.51.100.0/24")).toBeVisible();
    const zone = env("NEXORA_E2E_ACL_ZONE");
    await expect(
      page
        .getByTestId("zone-access-summary")
        .getByTestId(`zone-access-row-${zone}`),
    ).toContainText("Inherits authoritative access");

    await page
      .getByTestId("zone-access-summary")
      .getByRole("link", { name: zone })
      .click();
    await page.getByRole("tab", { name: "Transfers" }).click();
    await page
      .getByTestId("zone-allow-query")
      .fill("10.0.0.0/8, 192.168.0.0/16");
    await page.getByTestId("zone-update-allow").fill("127.0.0.1/32");
    await page.getByRole("button", { name: "Save settings" }).click();
    await expect(page.getByText("Settings saved")).toBeVisible();
    await page.getByTestId("nav-access-control").click();
    await expect(page.getByTestId(`zone-access-row-${zone}`)).toContainText(
      "10.0.0.0/8, 192.168.0.0/16",
    );
    await expect(page.getByTestId(`zone-access-row-${zone}`)).toContainText(
      "127.0.0.1/32",
    );
    await page.setViewportSize({ width: 400, height: 800 });
    await expect(page.getByTestId("authacl-input")).toBeVisible();
  });
  ```
  In `web/e2e/screens/03-access-control.spec.ts`, add after the navigation:
  `await expect(page.getByRole("heading", { name: "Recursion and resolver access" })).toBeVisible();`.
- [ ] Run
      `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m'`
      and expect FAIL in `30-access-control-split.spec.ts`: heading "Recursion and resolver access" not
      found.
- [ ] Implement `web/src/pages/AccessControlPage.tsx`:
  - The `PageHeader` title stays "Access control". Its description is "Who may use this resolver, and
    who may query the zones Nexora hosts. Addresses outside a list are refused."
  - Section 1 `h2` "Recursion and resolver access", with the text "Clients that may get answers from
    the cache, forwarding, recursion, rewrites and filtering." and the existing `ListEditor`
    (`prefix="acl"`).
  - Section 2 `h2` "Authoritative query access", with the text "Clients that may query hosted zones.
    A zone can narrow or widen this under Zones, Transfers. 0.0.0.0/0 and ::/0 allow everyone." and a
    `ListEditor` `prefix="authacl"`, `savedText="Authoritative access saved"`, whose `onSave` PUTs
    `{allow_cidrs, authoritative_allow_cidrs, revision}`.
  - Section 3 `h2` "Zone transfers and updates", a read-only `zone-access-summary` table from
    `useZones`. Columns: zone (link to `/zones/<id>`), allow-query ("Inherits authoritative access" or
    the list), transfers (allowed CIDRs and TSIG key name, or "Refused"), updates (TSIG key names and
    source CIDRs, or "Refused").
- [ ] In `web/src/pages/ZoneTransfersTab.tsx`, add to the form a "Query access" group before
      "Outgoing transfers":
  - `Input id="zone-allow-query" data-testid="zone-allow-query"` labelled "Allowed query networks",
    with help text "Empty uses the global authoritative query access." It is split with `splitList`
    and sent as `allow_query_cidrs`.
  - In "Dynamic updates", `Input id="zone-update-allow" data-testid="zone-update-allow"` labelled
    "Allowed update sources", with help text "Empty allows any source; a TSIG key is always
    required." It is sent as `update.allow_cidrs`.
- [ ] Run the same command and expect `03`, `18`, `19` and `30` to pass. Run
      `cd web && pnpm run typecheck && pnpm run lint`.
- [ ] Report the paths. Commit message: `gui: separate recursion and authoritative access, zone allow-query`.

## Task 21: Account GUI: profile page, change password dialog, menu items

Files:

- `web/src/pages/AccountPage.tsx`: created.
- `web/src/components/ChangePasswordDialog.tsx`: created.
- `web/src/lib/theme.ts`: apply the profile theme.
- `web/src/components/layout/AppShell.tsx`: the `UserMenu` items only.
- `web/src/app/router.tsx`: the `/account` route only.
- `web/e2e/screens/33-account.spec.ts`: created.
- `e2e/gui_seed_account_test.go`: created.

Interfaces:

- Test ids:
  - Menu: `menu-profile`, `menu-change-password`.
  - Page: `account-page`, `account-email`, `account-display-name`, `account-theme`,
    `account-time-zone`, `account-clock-24h`, `account-querylog-live`, `account-save`,
    `account-idp-note`.
  - Dialog: `password-current`, `password-new`, `password-confirm`, `password-revoke-others`,
    `password-save`, `password-error`.
- `useTheme()` keeps its signature and also applies `preferences.theme` when the profile loads
  (`system` follows `prefers-color-scheme`).
- Seed vars: `NEXORA_E2E_ACCOUNT_USER` (`pat`, viewer, password `pat-password-e2e-1`) and
  `NEXORA_E2E_ACCOUNT_PASSWORD`.

- [ ] Create `e2e/gui_seed_account_test.go`:
  ```go
  package e2e

  func init() { registerGUISeed(seedAccount) }

  func seedAccount(s guiSeedEnv) {
  	s.Admin.Must("POST", "/users", map[string]any{"username": "pat", "email": "pat@example.test", "password": "pat-password-e2e-1", "role": "viewer"}, nil, 201)
  	s.Vars["NEXORA_E2E_ACCOUNT_USER"] = "pat"
  	s.Vars["NEXORA_E2E_ACCOUNT_PASSWORD"] = "pat-password-e2e-1"
  }
  ```
  Create `web/e2e/screens/33-account.spec.ts`:
  ```ts
  import { test, expect, env, login, logout } from "../fixtures";

  test("user edits the profile and changes the password", async ({ page }) => {
    const user = env("NEXORA_E2E_ACCOUNT_USER");
    const old = env("NEXORA_E2E_ACCOUNT_PASSWORD");
    const next = "pat-password-e2e-2";
    await login(page, user, old);
    await page.getByTestId("user-menu").click();
    await page.getByTestId("menu-profile").click();
    await expect(page).toHaveURL(/\/account$/);
    await expect(page.getByTestId("account-page")).toContainText("viewer");
    await page.getByTestId("account-email").fill("pat.changed@example.test");
    await page.getByTestId("account-time-zone").fill("Europe/Brussels");
    await page.getByTestId("account-clock-24h").click();
    await page.getByTestId("account-save").click();
    await expect(page.getByText("Profile saved")).toBeVisible();
    await page.reload();
    await expect(page.getByTestId("account-email")).toHaveValue(
      "pat.changed@example.test",
    );
    await expect(page.getByTestId("account-time-zone")).toHaveValue(
      "Europe/Brussels",
    );

    await page.getByTestId("user-menu").click();
    await page.getByTestId("menu-change-password").click();
    await page.getByTestId("password-current").fill("definitely-wrong-1");
    await page.getByTestId("password-new").fill(next);
    await page.getByTestId("password-confirm").fill(next);
    await page.getByTestId("password-save").click();
    await expect(page.getByTestId("password-error")).toContainText(
      "Current password is wrong",
    );
    await page.getByTestId("password-current").fill(old);
    await page.getByTestId("password-save").click();
    await expect(page.getByText("Password changed")).toBeVisible();
    await logout(page);
    await login(page, user, next);
    await logout(page);
  });

  test("OIDC users cannot change their password here", async ({ page }) => {
    await page.goto("/login");
    await page.getByTestId("login-oidc").click();
    await expect(page.getByTestId("user-menu")).toContainText(
      env("NEXORA_E2E_OIDC_USER"),
    );
    await page.getByTestId("user-menu").click();
    await expect(page.getByTestId("menu-profile")).toBeVisible();
    await expect(page.getByTestId("menu-change-password")).toHaveCount(0);
    await page.getByTestId("menu-profile").click();
    await expect(page.getByTestId("account-idp-note")).toContainText(
      "Managed by your identity provider",
    );
    await expect(page.getByTestId("account-email")).toBeDisabled();
  });
  ```
  Check the OIDC login steps against `web/e2e/screens/11-oidc.spec.ts` and copy its flow if the
  fixture needs more than the `login-oidc` click.
- [ ] Run
      `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m'`
      and expect FAIL in `33-account.spec.ts`: `menu-profile` not found.
- [ ] Implement:
  - **`web/src/pages/AccountPage.tsx`:**
    - A `PageHeader` "Your account" with the description "Your sign-in details and how Nexora shows
      information to you."
    - A card of facts (username, role, source, created, last sign-in) using `Fact` from
      `web/src/components/common.tsx`.
    - A form: email and display name are disabled for OIDC users, with the `account-idp-note` "Managed
      by your identity provider". Preferences: theme `Select` (System, Light, Dark), time zone `Input`
      (IANA name, placeholder "Browser time zone"), 24-hour clock `Switch`, query log live by default
      `Switch`.
    - Save sends `PUT /auth/me` with revision, invalidates `["me"]` and shows `SavedNote` "Profile
      saved". A 409 shows the reload message through `ErrorAlert`.
  - **`web/src/components/ChangePasswordDialog.tsx`:**
    - Three password inputs (`autoComplete="current-password"` and `new-password`), minimum 12
      characters, a confirm-match check client-side, and a "Sign out my other sessions" checkbox
      (default on).
    - `POST /auth/me/password` maps the codes: 403 `invalid_current_password` → "Current password is
      wrong", 429 → "Too many attempts. Try again in 15 minutes.", 400 → the server message.
    - Success closes the dialog with the toast text "Password changed", rendered as a `SavedNote` in
      the menu header area.
  - **`web/src/components/layout/AppShell.tsx` `UserMenu`:** add `MenuItem data-testid="menu-profile"`
    ("Profile", navigates to `/account`) and, when `user.source === "local"`,
    `MenuItem data-testid="menu-change-password"` ("Change password", opens the dialog). They go
    before the theme toggle.
  - **`web/src/app/router.tsx`:** add `{ path: "account", element: <AccountPage /> }`.
  - **`web/src/lib/theme.ts`:** add `applyThemePreference(theme: "system" | "light" | "dark")`, called
    from `AccountPage` after save and from `UserMenu` when `user.preferences.theme` changes. The
    toggle also PUTs the preference when the user is loaded (best effort; a failed save keeps the
    local toggle).
- [ ] Run the same command and expect `33-account.spec.ts`, `06-users.spec.ts`, `11-oidc.spec.ts` and
      `web/e2e/auth.spec.ts` (through `TestAuthRBACAuditOIDC`) to pass:
      `scripts/dev-exec.sh 'NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run "TestGUICoverage|TestAuthRBACAuditOIDC" -count=1 -timeout 60m'`.
- [ ] Report the paths. Commit message: `gui: account profile and password change`.

## Task 22: Engine modal with metrics, logs and queries

Files:

- `web/src/components/EngineModal.tsx`: created.
- `web/src/api/fleet.ts`: `useEngineMetrics`, `useEngineLogs`.
- `web/src/pages/EnginesPage.tsx`: open the modal from rows.
- `web/src/pages/EngineGroupPage.tsx`: open the modal from the group's engine list.
- `web/e2e/screens/32-engine-modal.spec.ts`: created.
- `e2e/engine_logs_test.go`, `e2e/gui_seed_engines_test.go`: created.

Interfaces:

```ts
export function useEngineMetrics(
  id: string,
  window: StatsWindow,
): UseQueryResult<Schemas["EngineMetrics"]>; // refetchInterval 15_000
export function useEngineLogs(
  id: string,
  opts: {
    level: "error" | "warn" | "info" | "debug";
    q: string;
    paused: boolean;
  },
): {
  lines: Schemas["EngineLogs"]["lines"];
  error: unknown;
  droppedOlder: boolean;
};
// polls every 2 s with after=<last_seq>, keeps at most 2,000 lines, first request limit 1000
export function EngineModal(props: { engineIds: string[] }): JSX.Element | null; // reads ?engine= from useSearchParams
```

- Test ids:
  - `engine-modal`, `engine-modal-close`, `engine-modal-next`, `engine-modal-prev`,
    `engine-modal-full-page`.
  - Tabs: `engine-tab-metrics`, `engine-tab-logs`, `engine-tab-queries`.
  - Metrics: `engine-metrics-window`, charts `engine-chart-qps`, `engine-chart-latency`,
    `engine-chart-ratios`, `engine-chart-upstreams`, `engine-chart-resources`,
    `engine-chart-connections`; facts `engine-uptime`, `engine-restarts`.
  - Logs: `engine-logs-level`, `engine-logs-search`, `engine-logs-pause`, `engine-logs-follow`,
    `engine-log-line`, `engine-logs-unsupported`.
  - Queries: `engine-queries-row`, `engine-queries-open-log`.
  - Row open buttons: `engine-modal-open-<node>` on both pages.

- [ ] Create `e2e/engine_logs_test.go`:
  ```go
  func TestEngineLogsFromRealEngine(t *testing.T) {
  	env := harness.New(t)
  	pg := env.StartPostgres()
  	mgmt := env.StartMgmt(pg, env.InitCA(), harness.MgmtOptions{})
  	api := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
  	env.StartManagedEngine("log-engine", []string{mgmt.GRPCURL}, api.CreateJoinToken())
  	v := api.LatestVersion()
  	e := api.WaitEngine("log-engine", 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v })
  	var logs struct {
  		Lines []struct {
  			Seq     int64  `json:"seq"`
  			Level   string `json:"level"`
  			Message string `json:"message"`
  		} `json:"lines"`
  		LastSeq int64 `json:"last_seq"`
  	}
  	harness.EventuallyTrue(t, 15*time.Second, func() bool {
  		code, _ := api.Do("GET", "/engines/"+e.ID+"/logs?q=serving%20version", nil, &logs)
  		return code == 200 && len(logs.Lines) > 0
  	}, "engine log line through getEngineLogs")
  	if !strings.Contains(logs.Lines[0].Message, "serving version") || logs.Lines[0].Level != "info" {
  		t.Fatalf("line: %+v", logs.Lines[0])
  	}
  	if code, _ := api.Do("GET", fmt.Sprintf("/engines/%s/logs?after=%d", e.ID, logs.LastSeq), nil, &logs); code != 200 {
  		t.Fatalf("cursor read -> %d", code)
  	}
  	for _, l := range logs.Lines {
  		if l.Seq <= logs.LastSeq-int64(len(logs.Lines)) {
  			t.Fatalf("cursor returned an old line: %+v", l)
  		}
  	}
  }
  ```
  Use the engine id field name that `harness.EngineView` exposes (check `e2e/harness/mgmt.go` line 263).
  Create `e2e/gui_seed_engines_test.go`. Spec 05 deletes `gui-engine-2` and spec 20 revokes
  `gui-engine` before spec 32 runs, so the modal needs its own engines:
  ```go
  package e2e

  import (
  	"time"

  	"github.com/miekg/dns"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  func init() { registerGUISeed(seedEngines) }

  func seedEngines(s guiSeedEnv) {
  	e3 := s.Env.StartManagedEngine("gui-engine-3", []string{s.Mgmt.GRPCURL}, s.Admin.CreateJoinToken())
  	s.Env.StartManagedEngine("gui-engine-4", []string{s.Mgmt.GRPCURL}, s.Admin.CreateJoinToken())
  	v := s.Admin.LatestVersion()
  	for _, n := range []string{"gui-engine-3", "gui-engine-4"} {
  		s.Admin.WaitEngine(n, 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == v })
  	}
  	harness.MustQuery(s.T, e3.DNS, harness.UniqueName("modal"), dns.TypeA, harness.QueryOpts{})
  }
  ```
  Create `web/e2e/screens/32-engine-modal.spec.ts`:
  ```ts
  import { test, expect, env, login } from "../fixtures";

  test("engine modal: open, deep link, step, tabs with data, close", async ({
    page,
  }) => {
    await login(
      page,
      env("NEXORA_E2E_OPERATOR_USER"),
      env("NEXORA_E2E_OPERATOR_PASSWORD"),
    );
    await page.getByTestId("nav-engines").click();
    await page.getByTestId("engine-modal-open-gui-engine-3").click();
    const modal = page.getByTestId("engine-modal");
    await expect(modal).toContainText("gui-engine-3");
    await expect(page).toHaveURL(/[?&]engine=/);
    const url = page.url();
    await expect(page.getByTestId("engine-chart-qps")).toBeVisible();
    for (const w of ["5m", "1h", "24h"]) {
      await page.getByTestId("engine-metrics-window").click();
      await page.getByRole("option", { name: w, exact: true }).click();
      await expect(page.getByTestId("engine-chart-latency")).toBeVisible();
    }
    await page.getByTestId("engine-tab-logs").click();
    await expect(page.getByTestId("engine-log-line").first()).toBeVisible({
      timeout: 20_000,
    });
    await page.getByTestId("engine-logs-search").fill("serving version");
    await expect(page.getByTestId("engine-log-line").first()).toContainText(
      "serving version",
    );
    await page.getByTestId("engine-logs-pause").click();
    await page.getByTestId("engine-tab-queries").click();
    await expect(page.getByTestId("engine-queries-row").first()).toBeVisible();
    await page.getByTestId("engine-modal-next").click();
    await expect(modal).not.toContainText("gui-engine-3");
    await page.getByTestId("engine-modal-prev").click();
    await expect(modal).toContainText("gui-engine-3");
    await page.keyboard.press("Escape");
    await expect(modal).toBeHidden();
    await page.goto(url);
    await expect(page.getByTestId("engine-modal")).toBeVisible();
    await page.goBack();
    await expect(page.getByTestId("engine-modal")).toBeHidden();
    await page.setViewportSize({ width: 400, height: 800 });
    await page.goto(url);
    await expect(page.getByTestId("engine-tab-metrics")).toBeVisible();
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    );
    expect(overflow).toBe(false);
  });

  test("engine modal opens from an engine group's engine list", async ({
    page,
  }) => {
    await login(
      page,
      env("NEXORA_E2E_OPERATOR_USER"),
      env("NEXORA_E2E_OPERATOR_PASSWORD"),
    );
    await page.goto("/engines/groups/00000000-0000-0000-0000-000000000001");
    await page.getByTestId("engine-modal-open-gui-engine-4").click();
    await expect(page.getByTestId("engine-modal")).toContainText(
      "gui-engine-4",
    );
  });
  ```
- [ ] Run
      `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run "TestEngineLogsFromRealEngine|TestGUICoverage" -count=1 -timeout 60m'`
      and expect FAIL: `TestEngineLogsFromRealEngine` passes once Tasks 13 and 14 are in, and
      `32-engine-modal.spec.ts` fails with `engine-modal-open-gui-engine` not found.
- [ ] Implement `web/src/components/EngineModal.tsx`:
  - A `Dialog` (`max-w-5xl`, full screen under 640 px) controlled by `?engine=`. Close removes the
    parameter with `navigate(-1)` when the previous history entry was this page, otherwise a replace.
  - Next and previous walk `engineIds`.
  - The header uses `EngineStatusBadge`, the group name, version, applied/target version and
    `formatAgo(last_seen_at)`, with the `engine-modal-full-page` link to `/engines/nodes/<id>`.
  - Tabs: the Metrics tab is a window `Select` plus Recharts line charts of the `EngineMetrics` series,
    with empty states. The Logs tab uses `useEngineLogs`, a level `Select`, a search `Input` sent as
    `q` (debounced 300 ms), pause/resume, follow (auto scroll), a monospace list with level colour,
    and messages mapped to codes: 409 "Engine is not connected", 504 "Engine did not answer", 501 in
    `engine-logs-unsupported` "This engine version cannot send logs". The Queries tab shows
    `GET /query-log?engine_id=<id>&limit=20` rows and a link to `/query-log?engine_id=<id>`.
  - `EnginesPage.tsx` and `EngineGroupPage.tsx` render `<EngineModal engineIds={visibleIds} />` and a
    `Button variant="ghost" data-testid={`engine-modal-open-${e.node_name}`}` on the node name cell
    that sets `?engine=<id>`. The existing Details links stay.
- [ ] Run the same command and expect PASS for `TestEngineLogsFromRealEngine`, `05-engines`,
      `20-fleet` and `32-engine-modal`. Run `cd web && pnpm run typecheck && pnpm run lint`.
- [ ] Report the paths. Commit message: `gui: engine modal with metrics, logs and queries`.

## Task 23: Operations guide for M6

Files: `docs/operations.md` (the M6 settings and behaviour).

Interfaces: the headings `## Access control`, `## Engine logs and metrics` and `## Account and version`
(new), which help topics may reference after this task.

- [ ] Run
      `grep -c "authoritative_allow_cidrs\|parallel_max\|/api/v1/engines/{id}/logs\|NEXORA_REPOSITORY_URL\|Forwarding & recursion" docs/operations.md`
      and expect FAIL: `0`.
- [ ] Edit `docs/operations.md`:
  - **`### Management plane environment`:** add `NEXORA_REPOSITORY_URL` (optional; when set, the GUI
    links the build commit to `<url>/commit/<sha>`).
  - **`## First-run setup and access`:** replace "add upstreams under `/upstreams`" with "add upstreams
    under Forwarding & recursion (`/resolution`)".
  - **New `## Access control`:**
    - recursion access, meaning `PUT /api/v1/access-control` `allow_cidrs` plus engine group extra
      CIDRs;
    - authoritative query access, `authoritative_allow_cidrs`, default `0.0.0.0/0, ::/0`;
    - per-zone `allow_query_cidrs` (empty inherits);
    - update source CIDRs;
    - the check order and RA rule from `docs/architecture.md` `### ACL`;
    - `nexora_acl_refused_total{acl}`;
    - the migration note: "Upgrading keeps your current list as recursion access and answers hosted
      zones to everyone, as before."
  - **`## Performance tuning` Upstreams bullet:** add `parallel` with `parallel_max`, the load and
    privacy warning, and the metrics `nexora_upstream_race_wins_total` and
    `nexora_upstream_race_duration_seconds`.
  - **New `## Engine logs and metrics`:**
    - the 2,000-line ring, the rate cap and `nexora_log_lines_dropped_total`, and redaction;
    - `GET /api/v1/engines/{id}/logs` (operator; `after`, `level`, `q`, `limit`; 409/504/501 meanings);
    - `GET /api/v1/engines/{id}/metrics` (viewer; windows 5m/1h/24h);
    - the engine modal (`/engines?engine=<id>`).
  - **`## Monitoring and alerts`:** add the dashboard ranges, `engine_stats_rollup` retention of
    8 days, and the health alert kinds and thresholds (certificate warning under 14 days, critical
    under 7).
  - **`### Query logs, traces and OTLP`:**
    - partial name matching;
    - repeated filter parameters;
    - the reason fields and attributes `nexora.filter.source`, `nexora.filter.rule`,
      `nexora.rpz_zone`, `nexora.acl.refused` and `nexora.upstream_raced`;
    - the OpenSearch wildcard `debt:` note.
  - **New `## Account and version`:**
    - `PUT /api/v1/auth/me`, `POST /api/v1/auth/me/password`;
    - the lockout (more than 10 failures per username in 15 minutes gives 429);
    - `GET /api/v1/version`;
    - the build arguments `VERSION`, `COMMIT` and `BUILD_DATE`.
  - **`## Known limitations`:** add "Engine logs hold only the last 2,000 lines per engine and are lost
    on restart." and "The query log name filter scans every term of a day's OpenSearch index; very
    large daily indices may reach the 5 s timeout."
- [ ] Run the grep again and expect a count of 5 or more. Run
      `scripts/pc-format.sh docs/operations.md` and
      `scripts/dev-exec.sh 'go test ./deploy/deploytest -run "TestOperationsDoc|TestHelpTopicsReferenceOperationsDoc" -count=1'`,
      and expect PASS.
- [ ] Report the paths. Commit message: `docs: operations guide for M6`.

## Task 24: Navigation shell: Forwarding & recursion, Filtering group, help routes, document titles

Files:

- `web/src/components/layout/AppShell.tsx`: nav data and rendering, `PageHeader` document title,
  help link.
- `web/src/app/router.tsx`: `/resolution`, the `/upstreams` redirect, `/help`, `/help/:topic`.
- `web/src/pages/UpstreamsPage.tsx`: title, subtitle, "Upstream forwarders" section.
- `web/src/pages/ResolutionSection.tsx`: heading "Resolution mode".
- `web/src/pages/FilteringPage.tsx`: title "Blocklist / allowlist" only.
- `web/e2e/auth.spec.ts`, `web/e2e/screens/02-upstreams.spec.ts`,
  `web/e2e/screens/17-resolution.spec.ts`, `web/e2e/screens/21-engine-group-scope.spec.ts`,
  `web/e2e/screens/04-filtering.spec.ts`: new labels and test ids.
- `web/e2e/screens/28-navigation.spec.ts`: created.

Interfaces:

```ts
type NavLeaf = {
  route: string;
  path: string;
  label: string;
  icon: LucideIcon;
  op: OperationId;
  end?: boolean;
};
type NavParent = {
  route: string;
  label: string;
  icon: LucideIcon;
  children: NavLeaf[];
};
type NavItem = NavLeaf | NavParent;
```

- Test ids:
  - `nav-resolution` (was `nav-upstreams`), `nav-filtering-group` (parent button, `aria-expanded`),
    `nav-help`.
  - Children keep `nav-filtering`, `nav-filter-categories`, `nav-policies` and `nav-rpz`.
- localStorage key `nexora-nav-filtering` (`open` or `closed`).
- `PageHeader` sets `document.title = `${title} · Nexora``.

- [ ] Create `web/e2e/screens/28-navigation.spec.ts`:
  ```ts
  import { test, expect, env, login } from "../fixtures";

  test("forwarding and recursion rename, redirect and the collapsible Filtering group", async ({
    page,
  }) => {
    await login(
      page,
      env("NEXORA_E2E_VIEWER_USER"),
      env("NEXORA_E2E_VIEWER_PASSWORD"),
    );
    await expect(page.getByTestId("nav-resolution")).toHaveText(
      /Forwarding & recursion/,
    );
    await page.getByTestId("nav-resolution").click();
    await expect(page).toHaveURL(/\/resolution$/);
    await expect(page.getByRole("heading", { level: 1 })).toHaveText(
      "Forwarding & recursion",
    );
    await expect(page).toHaveTitle("Forwarding & recursion · Nexora");
    for (const h of [
      "Resolution mode",
      "Forward zones",
      "Upstream forwarders",
    ]) {
      await expect(
        page.getByRole("heading", { name: h, exact: true }),
      ).toBeVisible();
    }
    await page.goto("/upstreams");
    await expect(page).toHaveURL(/\/resolution$/);

    const parent = page.getByTestId("nav-filtering-group");
    await expect(parent).toHaveAttribute("aria-expanded", "true");
    for (const id of [
      "nav-filtering",
      "nav-filter-categories",
      "nav-policies",
      "nav-rpz",
    ]) {
      await expect(page.getByTestId(id)).toBeVisible();
    }
    await expect(page.getByTestId("nav-filtering")).toHaveText(
      /Blocklist \/ allowlist/,
    );
    await parent.click();
    await expect(parent).toHaveAttribute("aria-expanded", "false");
    await expect(page.getByTestId("nav-rpz")).toBeHidden();
    await page.reload();
    await expect(page.getByTestId("nav-filtering-group")).toHaveAttribute(
      "aria-expanded",
      "false",
    );
    await page.goto("/rpz");
    await expect(page.getByTestId("nav-filtering-group")).toHaveAttribute(
      "aria-expanded",
      "true",
    );
    await expect(page.getByTestId("nav-rpz")).toHaveAttribute(
      "aria-current",
      "page",
    );
    await page.getByTestId("nav-filtering").click();
    await expect(page.getByRole("heading", { level: 1 })).toHaveText(
      "Blocklist / allowlist",
    );
    await expect(page).toHaveTitle("Blocklist / allowlist · Nexora");

    await page.setViewportSize({ width: 400, height: 800 });
    await page.goto("/");
    await page.getByTestId("nav-filtering-group").scrollIntoViewIfNeeded();
    if (
      (await page
        .getByTestId("nav-filtering-group")
        .getAttribute("aria-expanded")) === "false"
    ) {
      await page.getByTestId("nav-filtering-group").click();
    }
    await page.getByTestId("nav-policies").scrollIntoViewIfNeeded();
    await page.getByTestId("nav-policies").click();
    await expect(page).toHaveURL(/\/policies$/);
    await page.evaluate(() => localStorage.removeItem("nexora-nav-filtering"));
  });
  ```
  In `web/e2e/auth.spec.ts`, `02-upstreams.spec.ts`, `17-resolution.spec.ts` and
  `21-engine-group-scope.spec.ts`, replace `nav-upstreams` with `nav-resolution`. In `21`, replace
  `getByRole("region", { name: "Upstreams", exact: true })` with the region name "Upstream
  forwarders". Update any heading text assertions `17-resolution.spec.ts` makes on "Resolution" to
  "Resolution mode" (check with `grep -n "Resolution\|Upstreams" web/e2e -r`). In
  `04-filtering.spec.ts`, update a heading assertion on "Filtering" if present.
- [ ] Run
      `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m'`
      and expect FAIL in `28-navigation.spec.ts` and `02-upstreams.spec.ts`: `nav-resolution` not found.
- [ ] Implement in `web/src/components/layout/AppShell.tsx`:
  - In the Resolver group, `{ route: "resolution", path: "/resolution", label: "Forwarding & recursion", icon: Server, op: "listUpstreams" }`
    first. Then `{ route: "filtering-group", label: "Filtering", icon: Funnel, children: [ { route: "filtering", path: "/filtering", label: "Blocklist / allowlist", icon: ListX, op: "listFilterLists", end: true }, { route: "filter-categories", ... label "Categories" }, { route: "policies", ... }, { route: "rpz", ... } ] }`.
    Then Rewrites, DNSSEC, Zones, Access control, Settings. RPZ moves out of the flat list.
  - `NavParent` renders a `button data-testid="nav-filtering-group" aria-expanded aria-controls`,
    with a chevron and an active style when `useLocation()` matches a child path. The children list is
    indented (`md:pl-4`) and hidden when closed. It is hidden entirely when no child passes `useCan`.
    The open state is initialised from localStorage (try/catch, default open) and forced open while
    the route is a child. Toggling writes `open`/`closed` in try/catch. The mobile horizontal strip
    renders children inline after the parent button.
  - A `nav-help` link to `/help` (`LifeBuoy` icon, no `op`: always visible to signed-in users) at the
    end of the Overview group.
  - `PageHeader` gets `useEffect(() => { document.title = `${title} · Nexora`; }, [title])`. The login
    and setup pages keep their own title handling.
  - `NavLink` children get `aria-current="page"` from react-router's active state (already the
    default) and `data-testid={`nav-${item.route}`}`.
- [ ] In `web/src/app/router.tsx`:
  - Replace `{ path: "upstreams", element: <UpstreamsPage /> }` with
    `{ path: "resolution", element: <UpstreamsPage /> }` and
    `{ path: "upstreams", element: <Navigate to="/resolution" replace /> }`.
  - Add `{ path: "help", element: <HelpPage /> }` and `{ path: "help/:topic", element: <HelpPage /> }`.

  In `web/src/pages/UpstreamsPage.tsx`, set the title "Forwarding & recursion" and the description
  "How Nexora resolves names: recursion from the root or forwarding, conditional forward zones, and
  the upstream forwarders used in forward mode." Rename the section to `aria-label="Upstream forwarders"`
  with `h2` "Upstream forwarders". In `web/src/pages/ResolutionSection.tsx`, set the `h2` to
  "Resolution mode" and `aria-label="Resolution mode"`. In `web/src/pages/FilteringPage.tsx`, set the
  `PageHeader` title to "Blocklist / allowlist" (the description changes in Task 29).

- [ ] Run the same command and expect specs 02, 04, 12, 15, 17, 21, 22 and 28 to pass, and
      `TestAuthRBACAuditOIDC` to pass. Run `cd web && pnpm run typecheck && pnpm run lint`.
- [ ] Report the paths. Commit message: `gui: Forwarding & recursion, collapsible Filtering menu, help routes`.

## Task 25: Dashboard GUI

Files:

- `web/src/pages/DashboardPage.tsx`: range selector and every section.
- `web/src/api/dashboard.ts`: created; hooks.
- `web/e2e/screens/01-dashboard.spec.ts`: still passes, with its tile ids kept.
- `web/e2e/screens/27-dashboard.spec.ts`: created.

Interfaces:

```ts
export type DashboardRange = "15m" | "1h" | "6h" | "24h" | "7d";
export function useDashboardSeries(
  range: DashboardRange,
): UseQueryResult<Schemas["DashboardSeries"]>; // refetch 10_000 for <= 1h, 60_000 otherwise
export function useDashboardTop(
  range: DashboardRange,
): UseQueryResult<Schemas["DashboardTop"]>;
export function useDashboardHealth(): UseQueryResult<
  Schemas["DashboardHealth"]
>; // refetch 15_000
```

- The existing ids stay: `dashboard-qps`, `dashboard-cache-hit-ratio`, `dashboard-engines`,
  `dashboard-chart`, `dashboard-upstream-<name>`.
- New ids:
  - Selector: `dashboard-range`, with options `dashboard-range-<range>`; `dashboard-auto-refresh`.
  - Sections: `dashboard-section-traffic`, `-latency`, `-cache`, `-resolution`, `-filtering`,
    `-dnssec`, `-top`, `-fleet`, `-health`.
  - Contents: `dashboard-top-unavailable`, `dashboard-alert`, `dashboard-fleet-row-<node>`.
- The range lives in the URL parameter `range`.

- [ ] Create `web/e2e/screens/27-dashboard.spec.ts`:
  ```ts
  import { test, expect, env, login } from "../fixtures";

  const sections = [
    "traffic",
    "latency",
    "cache",
    "resolution",
    "filtering",
    "dnssec",
    "top",
    "fleet",
    "health",
  ];

  test("dashboard ranges and sections render with data", async ({ page }) => {
    await login(
      page,
      env("NEXORA_E2E_VIEWER_USER"),
      env("NEXORA_E2E_VIEWER_PASSWORD"),
    );
    await page.getByTestId("nav-dashboard").click();
    for (const s of sections) {
      await expect(page.getByTestId(`dashboard-section-${s}`)).toBeVisible();
    }
    for (const r of ["15m", "1h", "6h", "24h", "7d"]) {
      await page.getByTestId("dashboard-range").click();
      await page.getByTestId(`dashboard-range-${r}`).click();
      await expect(page).toHaveURL(new RegExp(`range=${r}`));
      await expect(
        page
          .getByTestId("dashboard-section-traffic")
          .locator(".recharts-surface")
          .first(),
      ).toBeVisible();
    }
    await page.getByTestId("dashboard-range").click();
    await page.getByTestId("dashboard-range-15m").click();
    await expect(page.getByTestId("dashboard-section-top")).toContainText(
      env("NEXORA_E2E_QUERY_NAME").split(".")[0],
    );
    await expect(
      page
        .getByTestId("dashboard-fleet-row-gui-engine")
        .or(page.getByTestId("dashboard-fleet-row-gui-engine-2"))
        .first(),
    ).toBeVisible();
    await expect(page.getByTestId("dashboard-alert").first()).toBeVisible();
    await page.setViewportSize({ width: 400, height: 900 });
    for (const s of sections) {
      await expect(page.getByTestId(`dashboard-section-${s}`)).toBeVisible();
    }
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    );
    expect(overflow).toBe(false);
  });
  ```
  The engine deleted by `05-engines.spec.ts` produces an `engine_disconnected` alert, which is why the
  alert assertion holds.
- [ ] Run
      `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m'`
      and expect FAIL in `27-dashboard.spec.ts`: `dashboard-section-traffic` not found.
- [ ] Implement `web/src/api/dashboard.ts` per Interfaces, and `web/src/pages/DashboardPage.tsx`:
  - Keep the four tiles.
  - Add the `dashboard-range` `Select` and an auto-refresh `Switch` (default on).
  - Sections as cards in a `grid gap-4 lg:grid-cols-2`, each with an empty state ("No samples in this
    range yet") and error handling through `ErrorAlert`:
    - Traffic: stacked area of `qps_by_transport` and lines of `qps_by_rcode`.
    - Latency: p50/p95/p99 overall and uncached.
    - Cache: hit/miss/stale ratios, plus a per-engine entries and bytes table.
    - Resolution: stacked `answers_by_route`, recursion upstream QPS, timeouts, lame and failures.
    - Filtering: blocked and rewritten QPS, stacked `blocked_by_category`, per-engine filter index
      bytes.
    - DNSSEC: secure/insecure/bogus.
    - Top: four lists from `useDashboardTop`, with `dashboard-top-unavailable` "The query log backend
      is unavailable" when `available` is false.
    - Fleet: the engine table from `useDashboardHealth` with `EngineStatusBadge`, plus group counts.
    - Health: `dashboard-alert` rows with a severity `StatusDot`, or "Everything looks healthy".
  - Colours come from the existing CSS chart variables, so both themes work. The charts use
    `ResponsiveContainer` with `height={220}`.
  - The header description becomes "Traffic, latency, filtering and health across all resolvers.
    Pick a time range; charts refresh automatically."
- [ ] Run the same command and expect `01-dashboard.spec.ts` and `27-dashboard.spec.ts` to pass. Run
      `cd web && pnpm run typecheck && pnpm run lint`.
- [ ] Report the paths. Commit message: `gui: dashboard ranges, traffic, latency, filtering, DNSSEC, top lists, fleet and health`.

## Task 26: Version footer and reload hint

Files:

- `web/src/components/VersionFooter.tsx`: created.
- `web/src/components/layout/AppShell.tsx`: render the footer in `Sidebar`.
- `web/e2e/screens/34-version.spec.ts`: created.
- `e2e/kw_smoke_test.go`: the subtest `version-footer-matches-health`.

Interfaces:

- Test ids: `version-footer`, `version-footer-link` (short hash), `version-details` (popover),
  `version-reload-hint`, `version-info-button` (compact layout).
- The footer text is `Nexora ${version}` plus ` · ${commit.slice(0,7)}` when the version does not
  start with `sha-`.

- [ ] Create `web/e2e/screens/34-version.spec.ts`:
  ```ts
  import { test, expect, env, login } from "../fixtures";

  test("sidebar shows the version, details and the reload hint on a mismatch", async ({
    page,
  }) => {
    await login(
      page,
      env("NEXORA_E2E_VIEWER_USER"),
      env("NEXORA_E2E_VIEWER_PASSWORD"),
    );
    const footer = page.getByTestId("version-footer");
    await expect(footer).toContainText("Nexora");
    await footer.click();
    const details = page.getByTestId("version-details");
    await expect(details).toContainText("Management plane");
    await expect(details).toContainText("GUI build");
    await expect(details).toContainText("Engines");
    await expect(page.getByTestId("version-reload-hint")).toHaveCount(0);

    await page.route("**/api/v1/version", async (route) => {
      const res = await route.fetch();
      const body = await res.json();
      await route.fulfill({
        response: res,
        json: {
          ...body,
          version: "v9.9.9",
          commit: "0123456789abcdef0123456789abcdef01234567",
        },
      });
    });
    await page.reload();
    await expect(page.getByTestId("version-reload-hint")).toContainText(
      "New version available",
    );
    await expect(page.getByTestId("version-footer")).toContainText(
      "v9.9.9 · 0123456",
    );

    await page.setViewportSize({ width: 400, height: 800 });
    await page.getByTestId("version-info-button").click();
    await expect(page.getByTestId("version-details")).toBeVisible();
  });
  ```
  The GUI build under e2e has `__NEXORA_COMMIT__ = ""` and `version: dev`. The hint appears only when
  both commits are non-empty and differ, so the mocked response must also make the GUI side
  non-empty. Build the e2e GUI with `NEXORA_COMMIT=e2e0000000000000000000000000000000000000`: add it
  to the `web-build` invocation in this spec's run command below. The real mgmt response then carries
  commit `""` (unstamped e2e binary), which shows no hint, and the mocked one shows it.
- [ ] Add to `e2e/kw_smoke_test.go` in `TestKwSmoke`, next to the health version check:
  ```go
  	t.Run("version-footer-matches-health", func(t *testing.T) {
  		var v struct {
  			Version string `json:"version"`
  			Commit  string `json:"commit"`
  		}
  		mustGetJSON(t, kw, "/api/v1/version", &v)
  		if v.Version != version || len(v.Commit) != 40 || !strings.HasPrefix(v.Commit, strings.TrimPrefix(version, "sha-")) {
  			t.Fatalf("/version %+v does not match /health version %q", v, version)
  		}
  	})
  ```
  Use the authenticated client and JSON helper that `TestKwSmoke` already uses for API calls, and
  replace `mustGetJSON` and `kw` with those names.
- [ ] Run
      `scripts/dev-exec.sh 'NEXORA_COMMIT=e2e0000000000000000000000000000000000000 make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m'`
      and expect FAIL in `34-version.spec.ts`: `version-footer` not found.
- [ ] Implement `web/src/components/VersionFooter.tsx`:
  - It uses `useQuery(["version"], GET /version, { staleTime: 60_000, refetchInterval: 300_000 })`.
    `repository_url` makes the short hash a link `${repository_url}/commit/${commit}`.
  - A `Tooltip`-style popover (Radix `Tooltip` controlled by click) shows version, full commit, build
    date, the management plane version, the GUI build (`__NEXORA_VERSION__` and `__NEXORA_COMMIT__`),
    and engines as `N × version` lines, with "Engines run different versions" when more than one.
  - The hint shows when both commits are non-empty and differ: `version-reload-hint` "New version
    available – reload", with a button calling `location.reload()`.
  - A failed query shows the GUI build only.
  - In `AppShell.tsx` `Sidebar`, render it at the bottom of the `md:h-screen` column (`mt-auto`). In
    the mobile strip, render an `Info` icon button `version-info-button` that opens the same
    popover.
- [ ] Run the same command and expect `34-version.spec.ts` to pass. Run
      `cd web && pnpm run typecheck && pnpm run lint`.
- [ ] Report the paths. Commit message: `gui: version footer with details and reload hint`.

## Task 27: Parallel strategy in Settings

Files:

- `web/src/pages/SettingsPage.tsx`: strategy option, `parallel_max`, warning.
- `web/e2e/screens/31-upstream-parallel.spec.ts`: created.

Interfaces: test ids `settings-strategy-parallel` (select item), `settings-parallel-max`,
`settings-parallel-warning`.

- [ ] Create `web/e2e/screens/31-upstream-parallel.spec.ts`:
  ```ts
  import { test, expect, env, login } from "../fixtures";

  test("operator selects the parallel strategy with a cap", async ({
    page,
  }) => {
    await login(
      page,
      env("NEXORA_E2E_OPERATOR_USER"),
      env("NEXORA_E2E_OPERATOR_PASSWORD"),
    );
    await page.getByTestId("nav-settings").click();
    await page.getByTestId("settings-strategy").click();
    await page.getByTestId("settings-strategy-parallel").click();
    await expect(page.getByTestId("settings-parallel-warning")).toContainText(
      "every upstream",
    );
    await expect(page.getByTestId("settings-parallel-warning")).toContainText(
      "privacy",
    );
    await page.getByTestId("settings-parallel-max").fill("9");
    await page.getByTestId("settings-save").click();
    await expect(page.getByText(/between 0 and 8/)).toBeVisible();
    await page.getByTestId("settings-parallel-max").fill("2");
    await page.getByTestId("settings-save").click();
    await expect(page.getByText("Settings saved")).toBeVisible();
    await page.reload();
    await expect(page.getByTestId("settings-strategy")).toContainText(
      "Parallel",
    );
    await expect(page.getByTestId("settings-parallel-max")).toHaveValue("2");
    await page.getByTestId("settings-strategy").click();
    await page.getByRole("option", { name: /Ordered/ }).click();
    await page.getByTestId("settings-save").click();
    await expect(page.getByText("Settings saved")).toBeVisible();
  });
  ```
- [ ] Run
      `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m'`
      and expect FAIL in `31-upstream-parallel.spec.ts`: `settings-strategy-parallel` not found.
- [ ] In `web/src/pages/SettingsPage.tsx`:
  - Add `SelectItem value="parallel" data-testid="settings-strategy-parallel"` labelled "Parallel
    (race upstreams)".
  - `Form` gains `parallel_max: string`. A `Field` "Parallel upstreams" (`settings-parallel-max`,
    number 0–8, hint "0 races every healthy upstream (at most 8).") shows only for `parallel`.
  - `Alert` `settings-parallel-warning`: "Each uncached query goes to every upstream in the race at
    once, multiplying upstream load; some public resolvers rate-limit. Every raced provider sees the
    query, which matters for privacy."
  - Submit sends `parallel_max: Number(form.parallel_max)`. The server's 400 message is shown through
    the existing error alert.
- [ ] Run the same command and expect `09-settings.spec.ts` and `31-upstream-parallel.spec.ts` to
      pass. Run `cd web && pnpm run typecheck && pnpm run lint`.
- [ ] Report the paths. Commit message: `gui: parallel upstream strategy setting`.

## Task 28: Help and descriptions: resolver pages

Files:

- `web/src/help/catalog/resolver.ts`: entries and `pages`.
- `web/src/help/topics/resolution.md`, `web/src/help/topics/dnssec.md`,
  `web/src/help/topics/access-control.md`: content.
- `web/src/pages/UpstreamsPage.tsx`, `web/src/pages/ResolutionSection.tsx`,
  `web/src/pages/ForwardZonesSection.tsx`, `web/src/pages/SettingsPage.tsx`,
  `web/src/pages/DnssecPage.tsx`, `web/src/pages/AccessControlPage.tsx`: a `HelpTip` per control and
  column header, plus header text.

Interfaces:

- Catalogue ids are the control ids already in these files (`settings-strategy`, `upstream-timeout`,
  and so on). `ListEditor` instances use `help="acl-cidr-input"` and `help="authacl-input"`.
- Column headers whose meaning is not obvious use ids `<page>-col-<name>`, for example
  `upstreams-col-rtt`.
- The Settings description becomes exactly: "Resolver behaviour for every client: upstream selection,
  cache, blocking and telemetry. Saving publishes a new configuration version."

- [ ] Set `pages` in `web/src/help/catalog/resolver.ts` to
      `["pages/UpstreamsPage.tsx", "pages/ResolutionSection.tsx", "pages/ForwardZonesSection.tsx", "pages/SettingsPage.tsx", "pages/DnssecPage.tsx", "pages/AccessControlPage.tsx"]`.
- [ ] Run `cd web && node scripts/check-help.mjs` and expect FAIL (exit 1) with lines such as
      `check-help: pages/SettingsPage.tsx: control "settings-strategy" has no help entry` for every
      control in the six files. Save the list: it is this task's inventory.
- [ ] For every listed id, add an entry to `resolverHelp.entries` and a `<HelpTip id="<id>" label="<visible label>" />`
      beside its `Label`, in the `Field` helpers' label slot. Extend the local `Field` components in
      `SettingsPage.tsx` and `ResolutionSection.tsx` with an optional `help?: string` prop that renders the
      tip. Validation messages stay below the input, and the tip sits in the label row, so it never covers
      them. Write each entry from the defaults and ranges in `mgmt/api/openapi.yaml` and
      `docs/operations.md`. These entries are required verbatim:
  ```ts
  "settings-strategy": {
    text: "How Nexora picks among the enabled upstreams in forward mode. Ordered tries them in list order, Fastest tries the lowest measured round-trip time first, Parallel sends each uncached query to several upstreams at once and uses the first valid answer.",
    default: "Ordered",
    effect: "Parallel multiplies upstream load and shares every query with each raced provider. Changing the strategy publishes a new configuration and clears the cache.",
    topic: "resolution",
    anchor: "strategy",
  },
  "settings-parallel-max": {
    text: "How many upstreams one uncached query is raced across, lowest round-trip time first.",
    default: "0 (every healthy upstream, at most 8)",
    range: "0–8",
    topic: "resolution",
    anchor: "parallel",
  },
  "authacl-input": {
    text: "Networks that may query the zones Nexora hosts. A zone's own allow-query list replaces this for that zone.",
    default: "0.0.0.0/0 and ::/0 (everyone)",
    effect: "Clients outside the list get REFUSED for hosted names. Recursion is controlled separately.",
    topic: "access-control",
    anchor: "authoritative-access",
  },
  "acl-cidr-input": {
    text: "Networks that may use this resolver: cache, forwarding, recursion, rewrites and filtering. Engine groups can add networks.",
    default: "Loopback and private ranges",
    effect: "Clients outside the list get REFUSED for every name that is not hosted.",
    topic: "access-control",
    anchor: "recursion-access",
  },
  ```
- [ ] Write the topic sections named in Task 11 for `resolution.md`, `dnssec.md` and
      `access-control.md`: `resolution-mode`, `qname-minimisation`, `forward-zones`, `upstreams`,
      `strategy`, `parallel`; `validation`, `trust-anchors`, `negative-trust-anchors`, `signing`,
      `rollovers`; `recursion-access`, `authoritative-access`, `zone-allow-query`. Each is one to three
      paragraphs, uses the same defaults as the catalogue entries, and follows the architecture rules in
      `docs/architecture.md` (`### ACL`, `### Upstreams (forwarding)`).
- [ ] Set the `SettingsPage` description to the Interfaces text.
- [ ] Run
      `cd web && node scripts/check-help.mjs && pnpm run typecheck && pnpm run lint`, and expect PASS.
      Then run
      `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m'`
      and expect specs 02, 03, 09, 16, 17, 21, 28, 30 and 31 to pass.
- [ ] Report the paths. Commit message: `gui: help for forwarding, recursion, DNSSEC, settings and access control`.

## Task 29: Help and descriptions: filtering pages

Files:

- `web/src/help/catalog/filtering.ts`: entries and `pages`.
- `web/src/help/topics/filtering.md`: content.
- `web/src/pages/FilteringPage.tsx`, `web/src/pages/FilterCategoriesPage.tsx`,
  `web/src/pages/PoliciesPage.tsx`, `web/src/pages/RewritesPage.tsx`, `web/src/pages/RpzPage.tsx`:
  tips and header text.
- `web/e2e/screens/04-filtering.spec.ts`: text assertions for the new description.

Interfaces:

- The Filtering description is exactly: "Block unwanted domains by subscribing to blocklists, which
  Nexora downloads and keeps up to date. Domains on the allowlist are never blocked, even when a
  blocklist or filter category contains them."
- A link row under it: "Prefer ready-made lists? Turn on Filter categories. For different rules per
  device or network, use Policies." Its links are `filtering-link-categories` (to
  `/filtering/categories`) and `filtering-link-policies` (to `/policies`).
- The filter categories switches get `data-help="category-toggle"` and `data-help="source-toggle"`,
  with one entry each.

- [ ] Set `pages` in `web/src/help/catalog/filtering.ts` to the five page files. Run
      `cd web && node scripts/check-help.mjs` and expect FAIL listing the controls of those files (for
      example `pages/FilteringPage.tsx: control "list-url" has no help entry`).
- [ ] Add to `web/e2e/screens/04-filtering.spec.ts`, after navigation:
  ```ts
  await expect(page.getByRole("heading", { level: 1 })).toHaveText(
    "Blocklist / allowlist",
  );
  await expect(
    page.getByText("Domains on the allowlist are never blocked"),
  ).toBeVisible();
  await expect(page.getByTestId("filtering-link-categories")).toHaveAttribute(
    "href",
    "/filtering/categories",
  );
  await expect(page.getByTestId("filtering-link-policies")).toHaveAttribute(
    "href",
    "/policies",
  );
  await page.getByTestId("help-list-url").hover();
  await expect(page.getByRole("tooltip")).toContainText("downloads");
  ```
  Run the `TestGUICoverage` command of Task 28 and expect FAIL in `04-filtering.spec.ts`:
  "Domains on the allowlist are never blocked" not found.
- [ ] Add entries and `HelpTip`s for every listed control. Required verbatim:
  ```ts
  "list-url": {
    text: "The address of a blocklist in hosts, domain-per-line or Adblock format. Nexora downloads it, keeps the last good copy when a download fails, and refreshes it on the interval below.",
    effect: "Adding, removing or changing a list rebuilds the filter index on every resolver.",
    topic: "filtering",
    anchor: "blocklists",
  },
  "category-toggle": {
    text: "Turns every enabled source of this category on or off for clients that are in no policy group. Sources marked not free for commercial use ask for confirmation.",
    default: "Off",
    effect: "Rebuilds the filter index on every resolver.",
    topic: "filtering",
    anchor: "categories-and-licenses",
  },
  ```
  Write the `filtering.md` sections `blocklists`, `categories-and-licenses`, `allowlist`, `policies`,
  `rewrites`, `safe-search` and `rpz` from `docs/operations.md` `## Filter categories` and
  `docs/architecture.md` `### Filtering`. Allowlist precedence reads "Allowlist entries beat every
  blocklist, category and policy block."
- [ ] Apply the Filtering description and link row. Replace "management plane" and "ships to every
      engine" wherever they appear in section texts of the five files.
- [ ] Run `cd web && node scripts/check-help.mjs && pnpm run typecheck && pnpm run lint` and the
      `TestGUICoverage` command, and expect specs 04, 12, 13, 15, 22 and 28 to pass.
- [ ] Report the paths. Commit message: `gui: help and operator wording for filtering pages`.

## Task 30: Help and descriptions: zone pages

Files:

- `web/src/help/catalog/zones.ts`: entries and `pages`.
- `web/src/help/topics/zones.md`: content.
- `web/src/pages/ZonesPage.tsx`, `web/src/pages/ZoneDetailPage.tsx`, `web/src/pages/ZoneRecordsTab.tsx`,
  `web/src/pages/ZoneRecordEditor.tsx`, `web/src/pages/ZoneTransfersTab.tsx`,
  `web/src/pages/ZoneDnssecTab.tsx`, `web/src/pages/ZoneImportExportTab.tsx`,
  `web/src/pages/TsigKeysPage.tsx`: tips and header text.

Interfaces:

- The Zones description is exactly: "Authoritative zones Nexora serves: primary zones edited here or
  through dynamic updates, and secondary zones transferred from their primaries."
- The record type-dependent inputs in `ZoneRecordEditor.tsx` use `data-help="record-data"` with one
  entry that points to `web/src/lib/zoneRdataHints.ts` hints in its text.

- [ ] Set `pages` in `web/src/help/catalog/zones.ts` to the eight files. Run
      `cd web && node scripts/check-help.mjs` and expect FAIL listing their controls (for example
      `pages/ZoneTransfersTab.tsx: control "zone-allow-query" has no help entry`).
- [ ] Add entries and `HelpTip`s for every listed control. Required verbatim:
  ```ts
  "zone-allow-query": {
    text: "Networks that may query this zone. Empty uses the global authoritative query access under Access control.",
    default: "Empty (inherit)",
    effect: "Clients outside the list get REFUSED for names in this zone; transfers keep their own list.",
    topic: "access-control",
    anchor: "zone-allow-query",
  },
  "zone-transfer-allow": {
    text: "Networks that may transfer this zone with AXFR or IXFR. Empty refuses every transfer; a TSIG key additionally requires signed requests.",
    default: "Empty (transfers refused)",
    topic: "zones",
    anchor: "transfers",
  },
  "zone-update-allow": {
    text: "Source networks that may send dynamic updates. Empty allows any source; a TSIG key from the list below is always required.",
    default: "Empty (any source)",
    topic: "zones",
    anchor: "dynamic-updates",
  },
  ```
  Write the `zones.md` sections `records`, `transfers`, `tsig`, `dynamic-updates` and `zone-files` from
  `docs/architecture.md` and `docs/operations.md` `## Key storage`.
- [ ] Apply the Zones description.
- [ ] Run `cd web && node scripts/check-help.mjs && pnpm run typecheck && pnpm run lint` and the
      `TestGUICoverage` command, and expect specs 18, 19 and 30 to pass.
- [ ] Report the paths. Commit message: `gui: help for zones, transfers, TSIG and DNSSEC signing`.

## Task 31: Help: fleet pages and the engine modal

Files:

- `web/src/help/catalog/fleet.ts`: entries and `pages`.
- `web/src/help/topics/fleet.md`: content.
- `web/src/pages/EnginesPage.tsx`, `web/src/pages/EngineGroupPage.tsx`,
  `web/src/pages/EngineDetailPage.tsx`, `web/src/pages/RolloutPage.tsx`,
  `web/src/components/fleet.tsx`, `web/src/components/EngineModal.tsx`: tips (these pages keep the
  word "engine").

Interfaces: the rollout health settings in `EngineGroupPage.tsx` use their existing control ids as
catalogue ids.

- [ ] Set `pages` in `web/src/help/catalog/fleet.ts` to the six files. Run
      `cd web && node scripts/check-help.mjs` and expect FAIL listing their controls.
- [ ] Add entries and `HelpTip`s for every listed control. Required verbatim for the health window
      control, whatever its id is in `EngineGroupPage.tsx` (for example `group-health-window`):
  ```ts
  "group-health-window": {
    text: "How long canary engines must run a new configuration before the rollout continues. Each canary needs at least two stats samples in the window, and a SERVFAIL ratio above the limit halts the rollout.",
    default: "30 seconds",
    topic: "fleet",
    anchor: "rollouts",
  },
  ```
  In `EngineModal.tsx`, add an entry `engine-logs-level` explaining that levels come from the engine's
  own log (error, warn, info, debug), that the engine keeps its last 2,000 lines, and that older lines
  are dropped. Write the `fleet.md` sections `engine-groups`, `rollouts`, `certificates` and
  `engine-logs` from `docs/operations.md` `## Engine groups and staged rollouts` and
  `## Engine lifecycle`.
- [ ] Run `cd web && node scripts/check-help.mjs && pnpm run typecheck && pnpm run lint` and the
      `TestGUICoverage` command, and expect specs 05, 20 and 32 to pass.
- [ ] Report the paths. Commit message: `gui: help for engines, engine groups and rollouts`.

## Task 32: Help and descriptions: users, tokens, audit, account, query log, dashboard, sign-in

Files:

- `web/src/help/catalog/admin.ts`: entries and `pages`.
- `web/src/help/topics/users.md`, `web/src/help/topics/observability.md`: content.
- `web/src/pages/UsersPage.tsx`, `web/src/pages/ApiTokensPage.tsx`, `web/src/pages/AuditPage.tsx`,
  `web/src/pages/AccountPage.tsx`, `web/src/components/ChangePasswordDialog.tsx`,
  `web/src/pages/QueryLogPage.tsx`, `web/src/pages/DashboardPage.tsx`, `web/src/pages/LoginPage.tsx`,
  `web/src/pages/SetupPage.tsx`: tips and header text.

Interfaces: the Query log description is exactly "Every DNS query Nexora answered, newest first, with
why it was blocked, allowed, rewritten or refused."

- [ ] Set `pages` in `web/src/help/catalog/admin.ts` to the nine files. Run
      `cd web && node scripts/check-help.mjs` and expect FAIL listing their controls (for example
      `pages/QueryLogPage.tsx: control "querylog-name" has no help entry`).
- [ ] Add entries and `HelpTip`s for every listed control. Required verbatim:
  ```ts
  "querylog-name": {
    text: "Matches part of a query name, ignoring case and a trailing dot: \"tube\" finds youtube.com and www.youtube.com.",
    topic: "observability",
    anchor: "query-log",
  },
  "querylog-source": {
    text: "Why a query was decided: a blocklist subscription, a filter category source, the allowlist, a response policy zone, a rewrite, or an access control refusal. Several values match any of them.",
    topic: "observability",
    anchor: "query-log",
  },
  "password-new": {
    text: "At least 12 characters and different from your current password. Your other signed-in sessions end when \"Sign out my other sessions\" is on.",
    range: "12 characters or more",
    topic: "users",
    anchor: "account",
  },
  ```
  The `MultiSelect` triggers in `QueryLogPage.tsx` carry `data-help` with their test ids. Write the
  `users.md` sections `roles`, `api-tokens`, `oidc` and `account`, and the `observability.md` sections
  `query-log`, `dashboard`, `metrics` and `traces`, from `docs/operations.md`
  `## First-run setup and access`, `## Monitoring and alerts` and `### Query logs, traces and OTLP`.
- [ ] Apply the Query log description.
- [ ] Run `cd web && node scripts/check-help.mjs && pnpm run typecheck && pnpm run lint` and the
      `TestGUICoverage` command, and expect specs 00, 01, 06, 07, 08, 10, 11, 23, 24, 25, 26, 27 and 33 to
      pass.
- [ ] Report the paths. Commit message: `gui: help for users, tokens, audit, account, query log and dashboard`.

## Task 33: Help gate, help and page header specs, full GUI coverage

Files:

- `web/scripts/check-help.mjs`: `--all` enforced in lint.
- `web/package.json`: the lint script uses `--all`.
- `web/e2e/screens/29-help.spec.ts`: created.
- `web/e2e/screens/35-page-headers.spec.ts`: created.

Interfaces: none produced. This task consumes every earlier test id.

- [ ] Run `cd web && node scripts/check-help.mjs --all` and expect PASS. If it fails, the listed file
      belongs to an area whose task missed it: add it to that area's `pages` and entries in this task, and
      add the area file to this task's report.
- [ ] Change the `lint` script in `web/package.json` to
      `eslint . && node scripts/check-permissions.mjs && node scripts/check-help.mjs --all`. Prove it bites:
      temporarily add `<Input id="gate-probe" />` to `web/src/pages/AuditPage.tsx`, run
      `cd web && pnpm run lint`, expect FAIL with `control "gate-probe" has no help entry`, then remove
      the probe.
- [ ] Create `web/e2e/screens/29-help.spec.ts`:

  ```ts
  import { test, expect, env, login } from "../fixtures";

  const samples: [string, string][] = [
    ["/settings", "settings-strategy"],
    ["/resolution", "upstream-add"],
    ["/access-control", "authacl-input"],
    ["/filtering", "list-url"],
    ["/query-log", "querylog-name"],
    ["/users", "user-add"],
  ];

  test("help tips open on hover and focus and link to help pages", async ({
    page,
  }) => {
    await login(
      page,
      env("NEXORA_E2E_ADMIN_USER"),
      env("NEXORA_E2E_ADMIN_PASSWORD"),
    );
    for (const [path, id] of samples) {
      await page.goto(path);
      const tip = page.getByTestId(`help-${id}`).first();
      if (!(await tip.isVisible())) {
        // Dialog-only fields: open the page's create dialog first.
        await page
          .getByTestId(id.replace(/-(url|input)$/, "-add"))
          .first()
          .click();
      }
      await tip.hover();
      await expect(page.getByRole("tooltip")).toBeVisible();
      await page.mouse.move(0, 0);
      await tip.focus();
      await expect(page.getByRole("tooltip")).toBeVisible();
      await expect(tip).toHaveAttribute("aria-describedby", `help-text-${id}`);
      await page.keyboard.press("Escape");
    }
    await page.goto("/settings");
    await page.getByTestId("help-settings-strategy").hover();
    await page
      .getByRole("tooltip")
      .getByRole("link", { name: "Learn more" })
      .click();
    await expect(page).toHaveURL(/\/help\/resolution#strategy$/);
    await expect(page.locator("#strategy")).toBeInViewport();
    for (const theme of ["light", "dark"]) {
      await page.evaluate((t) => {
        document.documentElement.classList.toggle("dark", t === "dark");
      }, theme);
      await page.setViewportSize({ width: 400, height: 800 });
      await page.goto("/help/filtering#allowlist");
      await expect(
        page.getByRole("heading", { name: /Allowlist/i }),
      ).toBeVisible();
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth > window.innerWidth,
      );
      expect(overflow).toBe(false);
    }
    await page.getByTestId("nav-help").scrollIntoViewIfNeeded();
    await page.goto("/help");
    for (const t of [
      "Forwarding & recursion",
      "DNSSEC",
      "Filtering",
      "Authoritative zones",
      "Fleet",
      "Access control",
      "Users and API tokens",
      "Query log and observability",
    ]) {
      await expect(page.getByRole("link", { name: t })).toBeVisible();
    }
  });
  ```

  Replace the sample ids with ones present on the page without a dialog wherever
  `node scripts/check-help.mjs --all` shows a page-level control. The spec's rule is one sample per
  page group, reachable in at most one click.

  Create `web/e2e/screens/35-page-headers.spec.ts`:

  ```ts
  import { test, expect, env, login } from "../fixtures";

  const jargonFree = [
    "/",
    "/query-log",
    "/resolution",
    "/access-control",
    "/filtering",
    "/filtering/categories",
    "/policies",
    "/rewrites",
    "/rpz",
    "/dnssec",
    "/zones",
    "/zones/tsig-keys",
    "/users",
    "/api-tokens",
    "/audit",
    "/settings",
    "/account",
    "/help",
  ];

  test("page headers use operator wording", async ({ page }) => {
    await login(
      page,
      env("NEXORA_E2E_ADMIN_USER"),
      env("NEXORA_E2E_ADMIN_PASSWORD"),
    );
    for (const path of jargonFree) {
      await page.goto(path);
      const header = page.locator("main h1").first().locator("xpath=..");
      await expect(page.locator("main h1").first()).toBeVisible();
      const text = (await header.innerText()).toLowerCase();
      expect(text, path).not.toContain("management plane");
      expect(text, path).not.toMatch(/\bengines?\b/);
    }
    await page.goto("/filtering");
    await expect(
      page.getByText("Block unwanted domains by subscribing to blocklists"),
    ).toBeVisible();
    await expect(page.getByTestId("filtering-link-categories")).toBeVisible();
    await expect(page.getByTestId("filtering-link-policies")).toBeVisible();
  });
  ```

- [ ] Run
      `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 60m'`
      and expect PASS. The test would fail listing every OpenAPI operation without a covering spec, and
      none remain after Tasks 19–27. Also run
      `scripts/dev-exec.sh 'NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -count=1 -timeout 120m'`
      (the whole Go e2e suite, including `TestQueryLogBackends`, `TestAuthRBACAuditOIDC`,
      `TestQueryLogCategoryAttribution`, `TestAuthoritativeAccessSplit`, `TestUpstreamParallelStrategy`
      and `TestEngineLogsFromRealEngine`), plus `scripts/dev-exec.sh 'make engine-test && make mgmt-test && make web-test && make lint'`.
      Expect PASS for all.
- [ ] Report the paths. Commit message: `gui: help coverage gate, help and page header specs`.

## Task 34: Deploy M6 to kw and run acceptance

Files:

- `deploy/kw/values-kw.yaml`: `NEXORA_REPOSITORY_URL` through `mgmt.extraEnv`.
- `.procoder/notes/plan-review.md`: the M6 deployment record.

Interfaces: none.

- [ ] Run `grep -n "NEXORA_REPOSITORY_URL" deploy/kw/values-kw.yaml` and expect FAIL (no match). Add
      to `mgmt.extraEnv` in `deploy/kw/values-kw.yaml`:
  ```yaml
  - name: NEXORA_REPOSITORY_URL
    value: https://github.com/azrtydxb/nexora
  ```
  Run `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestHelmTemplate -count=1'` and expect
  PASS.
- [ ] After the lead has committed Tasks 1–33, build the images:
      `scripts/build-image.sh -f deploy/docker/engine.Dockerfile -n nexora-engine -t sha-$(git rev-parse --short HEAD)` and
      `scripts/build-image.sh -f deploy/docker/mgmt.Dockerfile -n nexora-mgmt -t sha-$(git rev-parse --short HEAD)`.
      Expect both builds to end with `pull as 192.168.10.131/azrtydxb/<name>:sha-<7>`.
- [ ] Start the DNS probe, then run `scripts/kw-deploy.sh` (it runs 5 queries/s to 192.168.10.136 and
      192.168.10.139). Expect `0 lost` on both addresses. On any lost query, run `helm rollback nexora`,
      record the cause in `.procoder/notes/plan-review.md` under `## M6 (2026-09-14)`, and stop.
- [ ] Run `scripts/kw-acceptance.sh` and expect PASS for `TestKwSmoke` (including
      `version-footer-matches-health`), `TestKwFullProduct` and `TestKwFilterCategories`. On failure, run
      `helm rollback nexora` and record why, as above.
- [ ] On kw, verify by hand in `https://nexora.kw.local`:
  1. The query log Name filter `you` lists youtube.com lookups (OpenSearch backend).
  2. The engine modal Logs tab for `nexora-engine-a` shows live lines.
  3. The footer shows `Nexora sha-<7>` matching `/api/v1/health`.

  Record the observations and the probe summary in `.procoder/notes/plan-review.md`.

- [ ] Report the paths, the image tag and the probe and acceptance results. The lead closes #54–#67
      with the commit and these proofs.
