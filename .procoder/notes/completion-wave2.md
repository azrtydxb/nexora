# Completion inventory — wave 2, 2026-09-17

Baseline: `504a006`, inspected from `/tmp/nexora-wave2/audit`. This refresh
supersedes stale implementation-gap statements in `completion-audit.md`, not its
historical results or task records. Read `parallel-integration.md` for the parent’s
Linux evidence. No AGENTS.md was found in this worktree or its wave2 parent.
Only the assigned notes, sprint plan and historical failure test are changed.
No task/issue/sprint closures, commits, deployment, shared toolbox, secret reads,
trust generation, split DNS or Cilium migration occurred.

## Branch provenance and preservation

| Source inspected read-only                  | State                                                                          | Interpretation                                                                                                                                                 |
| ------------------------------------------- | ------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| main / this worktree                        | `504a006`                                                                      | New CP/eligibility/operator changes landed; earlier inventory’s blanket CP gaps are stale                                                                      |
| `/Users/pascal/Development/nexora-m8`       | `m8` / `3fb09b9`, dirty                                                        | 22 commits beyond main, substantial M8 backend/engine/tests plus uncommitted T21 and GUI test work; preserve all                                               |
| `/Users/pascal/Development/nexora-m10`      | `m10` / `d0009ce`                                                              | Existing ClickHouse/Loki/conformance/chart/harness/deploy code; integration, not greenfield work                                                               |
| `/Users/pascal/Development/nexora-merge`    | `merge-m10` / `b3507bb`, `MERGE_HEAD=d0009ce89e795817f8950e06148acb6a55585bfc` | Unfinished M10 merge with staged files; no unmerged entries in observed status does not mean finished; do not reset, abort or continue it from this assignment |
| `m7` / `b64a749`, `m9` / `c002b43`          | Both ancestors of `504a006`                                                    | Their implementation has reached main; stale open tasks do not justify reimplementation                                                                        |
| `/tmp/nexora-wave2/{control,crosshost,m10}` | Separate active worktrees based on `504a006`                                   | Parent-owned pending integration; neither their eventual patches nor unrun tests are baseline evidence                                                         |

M8 dirty tracked paths observed: `.procoder/notes/plan-review.md`,
`.procoder/plans/nexora-m8-dns-protocols.md`, `engine/src/authoritative/state.rs`,
`engine/src/mdns/mod.rs`, `engine/src/recursor/dispatch.rs`, `engine/src/runtime.rs`,
`engine/src/server/mod.rs`, `engine/src/snapshot.rs`,
`engine/src/telemetry/metrics.rs`, `engine/tests/common/mod.rs`,
`engine/tests/hot_path_alloc.rs`. Untracked: `engine/tests/mdns_pipeline.rs`,
`e2e/gui_seed_m8_{catalog,mdns,rpz,zonemd}_test.go`, and
`web/e2e/screens/{40-zonemd,41-catalog-zones,42-odoh,43-mdns}.spec.ts`.
These are observations, not permission to overwrite or clean them. The reviewed
T21 diff adds `.local` routing, gateway/reflector lifecycle, cache invalidation
on enablement and hot-path cases; its existence is not Linux verification.

## M8 exact remaining integration and acceptance work

| Tasks  | Existing branch implementation/evidence                                                                                                         | Remaining work                                                                                                                                                                                                 |
| ------ | ----------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| T1–3   | `f08c686`: proto fields 900–901, generated API/types/permissions, model/migration scaffold                                                      | Resolve migration-number collisions before any combined migration; regenerate combined contracts, preserve main fields                                                                                         |
| T4–11  | Go/Rust RFC 8976 digest/vector tests; mDNS gateway/interface core; ODoH codec/keys; catalog codec; namespace lab and ODoH client                | Rerun on integrated Linux tree; fixtures/core tests do not establish product interoperability                                                                                                                  |
| T12–17 | ZONEMD rebuild/signing/secondary verification; API membership; catalog service; ODoH settings/rotation/key push; group mDNS validation/snapshot | Preserve transactional audit, last-good-copy and default-off behavior through merge; run management/database tests                                                                                             |
| T18–20 | Reflector (`0c3b1bc`, Linux pinning `bb16479`), ODoH engine integration (`1384552`), RPZ verification (`ed7c11d`) and tests                     | Linux multicast/lifecycle and combined snapshot/control regression; do not treat unsupported macOS socket pinning as a reason to remove guards                                                                 |
| T21    | Dirty engine integration and `mdns_pipeline.rs`, listed above                                                                                   | Review/adopt separately with owner coordination; run mdns, hot-path, server and recursion suites; not missing everywhere and not a certified pass                                                              |
| T22    | `4920292`: catalog API/service wiring, import guard and ODoH rotation loop/audit                                                                | Combined API/control tests, especially callback ownership; retain main session fencing                                                                                                                         |
| T23    | `3fb09b9`: `e2e/zonemd_test.go`, `rpz_zonemd_test.go`                                                                                           | Recorded branch Linux PASS: three product tests, 52.300s; mutations failed as expected. Rerun on combined build, not rewrite tests                                                                             |
| T24    | ODoH client and engine pipeline exist                                                                                                           | `e2e/odoh_test.go` / `TestODoHTargetAndProxy` absent in inspected M8 worktree: implement product target/proxy/key-rotation interoperability acceptance                                                         |
| T25    | Catalog management service exists                                                                                                               | `e2e/catalog_zones_test.go` absent; BIND harness `named.go` lacks planned Catalogs rendering. Add producer/consumer BIND interoperability, negative/reconcile tests                                            |
| T26    | Namespace lab and fixture exist; T21 dirty implementation                                                                                       | `e2e/mdns_test.go` absent: gateway, reflector and disabled `.local` forwarding product tests; supported Linux namespace/multicast prerequisites                                                                |
| T27–30 | Untracked GUI seeds and screen specs 40–43 exist                                                                                                | No matching ZONEMD/ODoH/catalog-zone/mDNS feature components found in `web/src`; implement cards/pages/settings/navigation/help and API calls, then run real GUI coverage. Existing specs are not delivered UI |
| T31    | Untracked RPZ GUI seed exists                                                                                                                   | `44-rpz-zonemd.spec.ts` absent; RPZ verification editor/status/help and browser proof still required                                                                                                           |
| T32    | Architecture text exists                                                                                                                        | `deploy/deploytest/m8_docs_test.go` and operations sections pending; documentation is not feature completion                                                                                                   |
| T33    | Scoped branch evidence exists                                                                                                                   | Full integrated engine/mgmt/web/lint/e2e and hot-path verification; attach evidence without changing task states here                                                                                          |
| T34    | Current general acceptance scripts exist                                                                                                        | `e2e/kw_smoke_m8_test.go` and script inclusion pending, then parent-controlled guarded immutable-SHA deployment and strict acceptance; no deployment authorized here                                           |

M8 integration hazards, in order:

1. Main owns `01300_failover_groups.sql` and `01301_engine_connection_session.sql`;
   M8 owns `01300_zonemd.sql`, `01301_catalog_zones.sql`, `01302_odoh.sql`,
   `01303_engine_group_mdns.sql`. Unique filenames do not prevent duplicate goose
   versions. Preserve main’s established versions. Before assigning a new ordered
   M8 range above main (for example 01302–01305 if still free), parent must establish
   whether M8 versions have run in any database that must survive. If so, design a
   migration-history transition; do not casually rename applied migrations. The
   branch’s `TestM8MigrationKeepsBehaviour` starts at version 899 and cannot alone
   prove upgrade from `504a006`: add upgrade coverage retaining failover rows,
   identity/config state and session-token columns plus a fresh-install check.
2. Both histories touch `mgmt/internal/control/hub.go` and
   `mgmt/internal/stats/resolution.go`. Compose ODoH key delivery with new session
   ownership; keep stats on the same fenced transaction. A clean textual merge
   would not prove old streams cannot send new key/config/log callbacks.
3. Overlapping OpenAPI/generated API/TS, `mgmt/cmd/nexora-mgmt/main.go` and
   architecture docs require semantic review. Regenerate from combined source,
   keeping failover/session/AI behavior; do not take a branch’s generated file
   wholesale. These also overlap M10 wiring. Proto additions must preserve all
   existing field numbers and defaults.
4. Review the dirty T21 changes separately from the committed branch. Test that
   disabled mDNS/ODoH preserve ordinary forwarding, recursion, ACLs, DNSSEC,
   authoritative/cache hot paths and allocation constraints. Preserve dirty GUI
   specifications as unfinished tests; do not mark absent components implemented.

## M6/M7/M9/M11 task and acceptance mismatches

Counts re-read at this baseline; no statuses changed. The exhaustive per-task
links remain in `completion-audit.md`. “Open” includes qualified open statuses.

| Milestone | Records / open / other non-closed | Current mismatch and next evidence                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        |
| --------- | --------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| M6        | 34 / 31 / 3                       | T1–33 code/tests largely exist despite open records. T16’s query-log rule mapping blocker is stale (`mgmt/internal/api/querylog_resolve.go`). T20/T23/T28 retain done/commit-pending wording. T27’s older GUI timeout is not unconditional failure of later builds. T34 has empty evidence: map repository URL/version footer and manual UX to the deployed SHA, prove unattended guarded deployment and strict acceptance                                                                                                                |
| M7        | 23 / 22 / 1                       | `m7` is integrated. T9 done record retains 35/36 stress result at exact exporter capacity: rerun and classify eviction vs timing, do not suppress it. T7 lost-trust browser alert proof remains absent. T22 records ARC amd64 routing failure and incomplete image/perf/post-head scheduled fuzz proof; infrastructure health was not rechecked. T23 full Linux/key-storage/GUI, compose verification and deployment/acceptance mapping remain open                                                                                       |
| M9        | 12 / 12 / 0                       | Operator/controllers/chart already integrated. T10 even says all subtests green on kw `dev-m9-8483b44`, but remains open. T7 revoke-before-rotate/token-cap regression exists. `504a006` fixes missing AI Secret/MCP optional fields and real Linux envtest/render checks passed per parent. T12 still needs full e2e plus deployment, strict acceptance and no production `NexoraInstallation` adoption evidence; operator unit/envtest pass is insufficient                                                                             |
| M11       | 32 / 21 / 11                      | T22 dependency on T18–21 is stale: code exists. T24/T26 retain “implemented, not committed”; nine other records retain done variants. T32 checks one historical full-suite box but still lists `kw_ai_test.go`/values as undone although both exist. Map later strict acceptance to exact SHA/AI behavior; no Secret values needed. Capacity sampler’s `recursor_cache` omission is real; its comment claiming M7 T12 settings do not exist is stale, but engine bytes telemetry is still needed before capacity sampling can be accepted |

Historical `kw.local` task URLs are stale relative to `kw.watteel.lab`; use the
current documented origin when parent records release evidence. Do not rewrite
old evidence as if it had run against a later SHA. M11 T32 records an older full
Linux e2e pass (1361.894s), not a post-504a006 full release pass. Prior recovered
strict product acceptance does not convert failed rollout or abrupt failover
attempts into passes.

## Failover sprint delta

- FG-02 pure eligibility is implemented/tested at `504a006`: trusted placement,
  effective configuration and real observation adapter remain pending.
- CP-01 fixed published target, pinned identities and bounded ACK convergence are
  implemented/tested; unattended live deploy remains unverified.
- CP-02 UUID-per-stream ownership fences database writes, with transactional stats,
  pool saturation, reused instance and red/green stale ACK evidence. Remaining
  stale NOTIFY/UPDATE callbacks, pending logs/in-memory effects and all-replica
  upgrade acceptance are explicit. Session tokens do not fence dataplane owners.
- FG-01 cross-host actual-engine evidence is still a gate; same-host synthetic
  tuple proof cannot substitute. FG-04–08 reconciliation, independent HA/fencing,
  lifecycle/deletion and API/GUI remain required. FG-09–12 cannot proceed to live
  cutover/matrix until prerequisites pass. Existing paired rollout is real code,
  but it is not dedicated frontend HA.
- FG-03 now has reviewed historical files and concrete test fixes below; parent
  Linux integration and full suite remain required. This does not close FG-03.

## Historical evidence import and safety fixes

Read `/Users/pascal/Development/nexora/deploy/kwrollout/failure_live_test.go` and
`.procoder/notes/{kw-member-failure,kw-dsr-feasibility}.md` without editing them.
Copied the notes verbatim; their dates/results, ephemeral `/tmp` artifact links
and then-current statements remain historical. In particular, the member-failure
note’s “bootstrap fix unimplemented” predates `504a006`, and DSR’s suggested next
experiment is not authorization for Cilium migration. Raw logs were not recovered
or revalidated; the copied report is not a fresh live result. No secrets copied.
Main’s untracked originals remain in place.

Imported the opt-in historical failure test and fixed these concrete defects:

- Four arbitrary `OnDelete` controllers were enough; now require the exact fleet
  names/namespace, no duplicates/deletion, one engine template each, and image
  equality. An OnDelete template with a staged new image must not turn deletion
  into an accidental upgrade.
- `CheckBoundFleet` intentionally allows replacements and does not check image or
  Service mode. Failure-test-specific validation rejects changed images, replaced
  untargeted partners and legacy/incomplete topology during recovery and at exit.
- Successful VIP samples after the initial recovery could release the lock despite
  subsequent fleet/management/node drift. Reinspect and validate all those gates
  after the observation window, with monitoring still running, before release.
- Monitoring now checks lock ownership every probe cycle. Early-failure cleanup
  cancels outstanding work before joining; no deferred unlock was introduced.

Retained: explicit single-member opt-in; UID/resourceVersion preconditioned delete;
first DNS error cancels with no retry; five-minute overall deadline; production
lock retained on every failure; no persistent-state deletion. This harness samples
UDP/TCP VIP DNS, not all five protocols, node partitions or zero-loss HA. Kubernetes
observations and lock checks are not an atomic dataplane fence; no such claim is
made. Local tests exercise safety decisions, not actual deletion/recovery timing.

## Prioritized runnable next actions (parent owns Linux/integration)

1. Integrate/review current wave2 control and this historical-test patch in a fresh
   integration worktree; preserve all other worktrees and main untracked copies.
   With live opt-ins empty, run:
   `NEXORA_KW_FAILURE_MEMBER= NEXORA_KW_LOCK_TEST= go test -race ./deploy/kwrollout ./mgmt/internal/control ./mgmt/internal/stats ./mgmt/internal/failover -count=1`.
   Review all ownership callbacks before declaring CP-02 complete. No cluster
   access is needed for ordinary fixture/database tests.
2. Complete the existing M10 integration in the parent’s chosen fresh worktree,
   using `d0009ce` and the wave2 M10 review, while leaving `nexora-merge` intact.
   Validate `go test ./mgmt/internal/querylog/... ./mgmt/internal/config ./mgmt/cmd/nexora-mgmt ./deploy/deploytest -count=1`
   and `make e2e-build`, then
   `NEXORA_E2E_BIN_DIR="$PWD/bin" go test ./e2e -run 'TestQueryLogBackends|TestQueryLogCategoryAttribution' -count=1 -v`.
   Conformance needs actual backend binaries/services, not skipped tests. M10 T12
   full/live acceptance is pending even though T1–11 have code. This order reduces
   shared main/OpenAPI/e2e/chart conflicts before adding M8.
3. Resolve M8 migration history/range, then integrate committed `3fb09b9` on that
   combined baseline; retain CP fixes and regenerate combined contracts with
   `make proto`. Run `go test ./mgmt/... ./e2e/harness -count=1` and
   `cargo test --locked -p nexora-engine --all-targets` on supported Linux.
   Add and run the new baseline-upgrade regression before accepting migrations.
4. Review/adopt dirty T21 with its owner, then run
   `cargo test --locked -p nexora-engine --test mdns_pipeline`,
   `cargo test --locked -p nexora-engine --lib mdns::`,
   `cargo test --locked -p nexora-engine --test hot_path_alloc` and the full engine
   suite. Implement pending T24–26 and missing BIND harness rendering. After
   `make e2e-build`, run
   `NEXORA_E2E_BIN_DIR="$PWD/bin" go test ./e2e -run 'TestZonemdGeneratedForPrimaryZones|TestZonemdSecondaryVerification|TestRPZZonemdVerification|TestODoHTargetAndProxy|TestCatalogZoneProducer|TestCatalogZoneConsumer|TestMdnsGatewayNetLab|TestMdnsReflectorNetLab|TestMdnsOffForwardsLocalNames' -count=1 -v`.
   **Check every expected test actually appears**: Go exits zero for a regex
   matching only a subset, so this command is not completion evidence until the
   absent tests are implemented and individually reported.
5. Implement M8 GUI T27–31 using the preserved seeds/specs; add missing RPZ spec,
   then `make web-test`, `make e2e-build` and
   `NEXORA_E2E_BIN_DIR="$PWD/bin" go test ./e2e -run TestGUICoverage -count=1 -v`.
   Add T32 docs/tests and T34 smoke/script wiring. Run `make engine-test`,
   `make mgmt-test`, `make web-test`, `make lint`, `make operator-test`, and full
   `NEXORA_E2E_BIN_DIR="$PWD/bin" go test ./e2e/... -count=1 -timeout 120m` after
   rebuilding the final tree. These are parent commands, not runs from this audit.
6. Independently review crosshost work and obtain isolated Linux actual-engine
   evidence for FG-01. Only then progress adapter → HA/fencing/lifecycle → API/UI
   → chart/cutover → failure matrix. Freeze thresholds before disruptive tests.
   Parent must inspect retained lock/quiescence and satisfy guarded release
   prerequisites before considering any `kw-deploy.sh`/`kw-acceptance.sh` or
   `TestMemberFailureLive` run. This note supplies no live-run authorization.
7. Reconcile M6/M7/M9/M11 acceptance against the final tested/deployed SHA and
   collect CI/images/scheduled-fuzz/perf/compose evidence. Only the authorized
   governance workflow may subsequently change task/issue/sprint closure states.

Genuine blockers: unresolved migration-history compatibility prevents safe M8
schema integration; missing actual cross-host/HA/fencing proof prevents transparent
failover acceptance; Linux namespace/backend/reference hardware and parent-owned
live access are external prerequisites; recorded ARC routing failure needs fresh
read-only diagnosis before being called a current outage. Pending engineering is
not an external blocker: M8 GUI/interoperability/smoke code, callback fencing,
M10 integration and evidence reconciliation can proceed now in isolated trees.
macOS PostgreSQL initdb and Rust `libc::mmsghdr` limitations do not authorize
weakening tests or claim supported Linux is failing.

## Verification performed here

- `env NEXORA_KW_FAILURE_MEMBER= NEXORA_KW_LOCK_TEST= GOCACHE=/tmp/nexora-wave2-audit-go-cache GOPROXY=off go test ./deploy/kwrollout -run '^(TestMemberFailureSafetyGuards|TestMemberFailureLive)$' -count=1 -v`
  passed (3.695s); 15 guard subcases passed and malformed JSON rejected; live test
  explicitly skipped. No cluster operations ran.
- Supported Linux/PostgreSQL/Rust/operator/browser/live suites were not run here.
  Parent baseline results are attributed to `parallel-integration.md`, including
  the separate operator pass and the timed-out earlier operator invocation.
- Laptop-wide historical result remains 235 Go failures and Rust `mmsghdr`
  compilation failure; it was not rerun or relabeled green by this audit.
- Final focused race run: same opt-outs/cache/proxy settings, command
  `go test -race ./deploy/kwrollout -run '^(TestMemberFailureSafetyGuards|TestMemberFailureLive)$' -count=1 -v`
  passed (1.613s), all 15 subcases; live test skipped.
- Broader local attempt with the same environment:
  `go test -race ./deploy/kwrollout -count=1` **FAILED** (1.470s) in
  `TestAssembledRuntimeMigration/success`: `httptest` could not bind
  `tcp6 [::1]:0` (`operation not permitted`) under this sandbox. Log:
  `/tmp/nexora-wave2-audit-rollout.log`. This is a local socket restriction;
  the full package remains unverified here. Do not infer other tests passed.
- `gofmt` applied; tracked and new-file `git diff --check` passed. Both imported
  notes compare byte-for-byte with main’s originals, and all three original
  historical files still appear untracked in main. No external worktree edited.
