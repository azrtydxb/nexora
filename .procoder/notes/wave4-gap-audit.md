# Wave 4 requirement audit

Parent-saved read-only agent report. Findings describe its input snapshot;
[reviewed integration evidence](wave4-reviewed-evidence.md) records later changes.
The scope statements below refer to the audit lane, not subsequent integration.

The snapshot contains substantial implementation across M1–M11, including integrated M8/M10 work and the final control fixes. It does **not** establish completed failover HA, current-release acceptance, or milestone completion.

**Audit scope and changes**

- Assigned worktree: `/tmp/nexora-wave4/audit`, supplied candidate based on `f9c1653`.
- **New files: none. Modified files: none. Deleted files: none.**
- This report was not written to disk.
- No tests, deployments, network experiments, commits, merges, credential access, or workflow closures were performed.
- The agent reported reading its supplied Procoder 3.6.0 skill and applying the independent-review guidance through read-only milestone reviews.
- Inventory covers all **11 specs, 16 plans, 229 todos, 27 notes and two decision/answer files**. Current source and tests were inspected against their requirements.
- Limitation: this is an exhaustive **requirement/task inventory**, not independent line-by-line verification of every historical code listing and transcript embedded in the large plans, nor every product source file. Referenced external raw logs were not reopened.

The four passes were: scoped inventory and implementation mapping; independent-style reread; adversarial review with concrete regression requirements; bounded report reconciliation. No automated Procoder check/test/review PASS is claimed.

**How to read the matrix**

- **Implementation:** inspected code or test definitions.
- **Execution:** a specifically attributed recorded run; never inferred from a filename or checkbox.
- **Acceptance:** satisfaction of the actual criterion on the required artifact/topology.
- **C:** missing or mismatched code.
- **T:** missing or insufficient test coverage.
- **E:** execution remains required.
- **A:** deployment/product acceptance remains required.
- **P:** planning-only, excluded, or awaiting a scope decision.

Unless explicitly stated otherwise, current-candidate execution and acceptance remain open.

**Evidence baseline and reconciliation**

The principal current evidence source is [wave3-reviewed-evidence.md](wave3-reviewed-evidence.md), read alongside the integration notes and current source.

| Evidence                                                                                         | Supported conclusion                                                                                                                  | Boundary                                                                                                    |
| ------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| Wave3 complete GUI: 83 screen cases plus setup, 335.169s; remaining E2E shard: 1298.388s         | Together cover the full enabled E2E package on the commit-barrier/UI candidate                                                        | **Before final control fixes.** Live opt-in skips remain unaccepted                                         |
| Wave3 combined management/race, corrected real-backend conformance 51.458s, harness race 22.243s | Recorded supported Linux execution after prerequisite corrections                                                                     | Earlier prerequisite failures remain preserved; not current-release deployment                              |
| Combined Rust all-targets with isolated target directory                                         | Recorded combined engine execution, including integrated M8                                                                           | Does not establish every specification criterion; see the 400ms/250ms mismatch                              |
| Final operator race/envtest using Kubernetes 1.34.1 assets                                       | Operator controllers, API persistence and rendering executed                                                                          | Not real CNPG failover, backup recovery, or production continuity                                           |
| Cross-host **`fb374f92`**: 80 joined flows                                                       | Two groups, two clients, both backends, five transports; actual socket/capture/backend/OTLP attribution and signed payload/truncation | Standalone engine attachment, not managed-control continuity, general PMTU, reused sessions, or frontend HA |
| Cross-host controls **`fb04bedb`**, **`1119d895`**                                               | Exact translated-source and assigned-MAC negative controls                                                                            | Separate echo/control evidence; not production failure acceptance                                           |
| Classic-TC kernel smoke on worker21                                                              | Default denial, authorized traffic, paused/dead-owner expiry and separate management path exercised                                   | One bounded hook primitive; not all-path fencing or two-owner HA                                            |
| Platform staging: 25 mutations, repeat/restart/cleanup                                           | Actual inactive staging and owned cleanup exercised after alias fix                                                                   | `active:false`; no engine traffic or production fence integration                                           |
| Lease/binding Linux race 101.368s and vet                                                        | Library, child-pipe behavior and corrected absolute-clock test executed                                                               | Actual driver + Kubernetes API + active frontend integration absent                                         |
| Historical paired `sha-809cf3a`, revision 35, strict acceptance 459.272s                         | Four-engine paired product acceptance after recovery                                                                                  | Rollout originally failed bootstrap; not unattended success                                                 |
| Historical abrupt engine-a failure                                                               | `.136` TCP refusal occurred; progression stopped and lock retained                                                                    | Remains **RED**; later healthy preflight does not erase it                                                  |

Current source contains `displaced_keys_test.go`, `stream_lifetime.go`, and their associated fixes. Thus the reviewed note’s “follow-up work in control-final” wording is stale as an implementation statement. **Linux verification of those integrated fixes is pending**, as supplied by the user.

Other historical claims requiring reconciliation, without editing their records:

| Historical statement                                                       | Current interpretation                                                                                                |
| -------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| M8 unintegrated; missing ODoH/catalog/mDNS/UI                              | Superseded by current implementation and later Linux/browser evidence                                                 |
| M10 requires branch integration                                            | Superseded; reviewed M10 deltas are already present                                                                   |
| Bootstrap convergence unimplemented                                        | Superseded by bounded fixed-target wait; unattended live acceptance remains open                                      |
| NOTIFY/UPDATE/log ownership fencing absent                                 | Superseded by transactional fencing; latest control execution and all-replica acceptance remain                       |
| Actual-engine attribution established by `57f032b7`                        | Insufficient verifier; use corrected `fb374f92`                                                                       |
| Platform staging remains RED; Linux fence build absent                     | Superseded by later successful bounded Linux experiments                                                              |
| Lease clock test skipped/invalid procfs path                               | Superseded by corrected no-skip Linux run and negative control                                                        |
| Recursor capacity telemetry/sampling missing                               | Superseded by actual byte telemetry and sampling                                                                      |
| HA test grants an extra second; filter-fleet acceptance can pass vacuously | Both corrected in current tests                                                                                       |
| Compose never executed                                                     | Historical novanas execution exists; final-image proof remains                                                        |
| Toolbox never rebuilt                                                      | M10 records a historical isolated build/selftest; later private-PATH runs do not establish current shared-image state |
| 183 todos / 15 plans                                                       | Current inventory is 229 / 16                                                                                         |
| Native-routing migration remains next action                               | Superseded by current instruction to preserve VXLAN                                                                   |
| Unqualified rollback or key-regeneration guidance                          | Must not override retained-lock/quiescence/CAS and identity/trust/data preservation                                   |

---

**Highest-priority actionable gaps beyond failover**

These findings are not new task closures or authorization to change scope.

| ID    | Requirement                     | Finding and classification                                                                                                                                                               | Exact next owner/file scope and regression                                                                                                                                                                                                                     |
| ----- | ------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| W4-01 | CP-02; V1 S-14/S-23             | **E/A:** final TSIG-displacement and non-reading-peer shutdown fixes are integrated but unverified on Linux                                                                              | Control owner reviews `mgmt/internal/control/{hub,server,secret_fence,stream_lifetime}.go`; parent runs full control race plus `TestDisplacedTSIGRecovery`, `TestConnectNonReadingPeerShutdown`, commit-barrier and ownership cases                            |
| W4-02 | M6 S-3; categories S-6          | **C/T:** RPZ UUID is resolved against the set current at export time; publication reorder can mislabel an earlier decision. Old filter-generation events can lose list/category metadata | Engine telemetry owner: `engine/src/{runtime.rs,telemetry/querylog.rs,telemetry/otlp.rs}`, RPZ identity plumbing, `engine/tests/telemetry_export.rs`. Queue event, reorder/remove/rebuild, then require exact original attribution without hot-path allocation |
| W4-03 | M7 S-17/T9                      | **T/A:** criterion specifies a **400ms** collector, 20,000 records and no drops; current test uses **250ms**. Historical 400ms stress includes a dropped 1,000-record batch              | Engine/test owner: `engine/tests/telemetry_export.rs`, exporter scheduling/accounting. Execute the original criterion and diagnose capacity/scheduling honestly. Preserve the 250ms result and historical failure separately                                   |
| W4-04 | M8 S-4                          | **C/T:** explicit ZONEMD record rejection returns and asserts **422 `unsupported_type`**, versus specified **400 `invalid_request`**                                                     | Zone API owner: record-validation path, `mgmt/internal/api/zonemd_api_test.go`, `e2e/zonemd_test.go`. Require exact status/code and unchanged records/version                                                                                                  |
| W4-05 | M8 S-13                         | **T:** migration tests check database defaults but do not build the required post-upgrade snapshot                                                                                       | Store/snapshot owner: `mgmt/internal/store/m8_migration_test.go` and snapshot fixtures. Assert nil mDNS/ODoH, OFF existing RPZ verification and preserved effective behavior                                                                                   |
| W4-06 | M8 S-14/T32/T34                 | **C/T:** operations content exists, but `TestOperationsGuideCoversM8` and `TestKwSmokeM8` are absent                                                                                     | Docs/E2E owners: new `deploy/deploytest/m8_docs_test.go`, new `e2e/kw_smoke_m8_test.go`, `scripts/kw-acceptance.sh`; parent owns live execution                                                                                                                |
| W4-07 | M11 S-3                         | **C/T:** search uses full translated filters; summary top lists use only time range and filter result                                                                                    | Querylog/AI owners: `mgmt/internal/querylog/backend.go`, four adapters, `mgmt/internal/ai/qlsearch/qlsearch.go`. Selected-client A plus dominant unrelated B must exclude B from summary aggregates across all backends                                        |
| W4-08 | M11 S-1                         | **C/T:** budget is checked before queueing, not again at dispatch or between validation attempts                                                                                         | AI owner: `limits.go`, `generate.go`, limits tests; durable admission/accounting if required. Hold request A, queue B, let A exhaust budget, then require B makes no model call. Add cross-instance and retry-cutoff cases                                     |
| W4-09 | M11 S-1 privacy                 | **T; possible C:** address classification is separate from provider transport. Checked-address versus contacted-address, redirects and SDK retry invariants are unproved                 | AI/security owner: `privacy.go`, `provider.go`, transport integration/tests. Private-then-public resolution and private-to-public redirect regressions. **No exploit or exfiltration was demonstrated**                                                        |
| W4-10 | V1 S-21                         | **C/P:** optional dnstap appears in the spec but no sink/configuration/test implementation was found                                                                                     | Product owner resolves retained optional scope explicitly. If retained: telemetry owner adds bounded output, sink-down/drop accounting and docs; do not silently call it excluded                                                                              |
| W4-11 | V1 S-5                          | **T/A:** fleet acceptance uses three local processes, not three separate hosts; DNS sampling starts after disconnection observation                                                      | Fleet/E2E owner: `e2e/fleet_test.go` and managed host harness. Three independent hosts, exact ACKs, monitoring before management disruption and all original samples retained                                                                                  |
| W4-12 | M7 S-9                          | **T:** lost-trust banner code exists; ordinary trust-anchor editing does not test loss and recovery of the trust point                                                                   | GUI/E2E owner: DNSSEC/dashboard screen and dedicated seed. Assert banner appears and clears using isolated fixture state; never revoke real root trust                                                                                                         |
| W4-13 | M6 S-15                         | **C/T:** TCP-peer-based account throttling collapses clients behind a reverse proxy                                                                                                      | Auth owner: `mgmt/internal/auth/account.go`, request-address boundary/configuration. Trusted-hop and forged-header tests, distinct proxied clients and concurrent failures; never trust arbitrary forwarded headers                                            |
| W4-14 | V1 S-16; M7 S-1; categories S-9 | **E/A/P:** absolute reference performance and A/A noise evidence remain absent                                                                                                           | Performance/CI owner: `bench/`, `.github/workflows/perf-gate.yml`; actual reference box and load-host evidence. Keep 1M QPS, p99 <500µs and 5% thresholds                                                                                                      |
| W4-15 | Cross-milestone verification    | **C/T:** ordinary CI omits full E2E/browser, deployment race, failover Python/C and M8 unit execution                                                                                    | CI owner: `.github/workflows/ci.yml`, `Makefile`, explicit test selection. Manual Linux passes do not provide ongoing CI coverage                                                                                                                              |

For W4-08, distinguish allowed uncertainty from already in-flight token usage from **starting additional work after exhaustion is known**. Current sequential budget tests do not cover that distinction.

For W4-09, current source does perform endpoint classification on each generation attempt. The gap is transport binding and proof; it is not absence of all privacy validation.

---

**M1–M5 requirement matrix**

V1 IDs refer to `.procoder/specs/nexora-v1.md`. All 82 historical task records are closed; that state does not waive the findings above or establish current acceptance.

| Milestone / requirement IDs       | Existing implementation and test definitions                                                                           | Remaining work / next owner                                                                                                                        |
| --------------------------------- | ---------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| M1 S-1                            | Cache/upstream/coalescing implementation; `TestForwardCacheTTL`, `TestUpstreamFailover`, `TestDedupAllWaitersAnswered` | **E/A:** parent final binary/E2E verification; no foundation rebuild                                                                               |
| M1 S-4                            | Subscription fetch/parse/last-good behavior; `e2e/blocklist_test.go`                                                   | **E/A:** final product run, preserving allow overrides and failed-refresh behavior                                                                 |
| M1/M2 S-6                         | Wire/EDNS/cookies, UDP/TCP/DoT/DoH/DoQ; EDNS and encrypted-transport E2E                                               | **E/A:** final transport parity; one-hour fuzz evidence remains separate from smoke tests; failover sessions belong to FG-11                       |
| M1 S-12/S-22                      | Off-path query log, builtin/OpenSearch and now M10 adapters; query-log product/browser tests                           | **C/T:** W4-02 attribution boundaries; **E/A:** final four-backend proof                                                                           |
| M1 S-13/S-21                      | Prometheus, OTLP, tracing, bounded export/drop counters; observability E2E                                             | W4-03 and W4-10; parent must retain actual Collector/trace execution                                                                               |
| M1 S-14                           | API/store/snapshots/control, invalid-snapshot and HA tests                                                             | Strict shared 10-second recovery deadline is now implemented; W4-01 verification remains                                                           |
| M1 S-15                           | Resource GUI and `TestGUICoverage`                                                                                     | Prior complete GUI execution exists; final candidate/live UI evidence remains                                                                      |
| M1 S-16                           | Relative and absolute benchmark tooling/workflows                                                                      | W4-14; skip or non-reference measurements cannot close the criterion                                                                               |
| M1 S-20                           | Local/OIDC authentication, RBAC, tokens, audit; auth E2E                                                               | W4-13 proxy-throttle debt; parent reruns actual auth/audit behavior                                                                                |
| M2 S-10/S-11                      | CIDR-selected policy, cache isolation, rewrites and safe search; policy/rewrite E2E                                    | **E/A:** final verification. These do not implement split DNS                                                                                      |
| M3 S-2/S-7                        | Iterative resolver, anti-spoofing, DNSSEC validation and trust anchors; recursion/DNSSEC hierarchy tests               | **E/A:** real fixtures and final engine; W4-12 lost-trust browser coverage                                                                         |
| M3 S-9                            | RPZ parsing, sources, policy, AXFR/IXFR and last-good behavior                                                         | W4-02 exported identity; M8 extends integrity verification                                                                                         |
| M4 S-3                            | Authoritative zones, atomic rebuild/publication and AA behavior                                                        | **E/A:** `TestAuthoritativeZonePropagation` on current managed engines                                                                             |
| M4 S-17/S-18                      | AXFR/IXFR/NOTIFY, secondary refresh and TSIG UPDATE                                                                    | Control fencing is integrated; parent reruns BIND/UPDATE and current control regressions                                                           |
| M4 S-19                           | Zone import/export and streaming paths                                                                                 | **E/A:** `TestZoneFileRoundTrip` with real comparison tools                                                                                        |
| M4 S-8/S-23                       | Online signing, KEK/PKCS#11 storage, rollover and in-memory engine key delivery                                        | **E/A:** final SoftHSM/delv/storage and W4-01 secret-delivery proof; no live trust inspection needed for implementation                            |
| M5 S-5                            | Fleet API, per-group snapshots, canary/rollout state, persistent identity, renewal/revoke/rotation                     | W4-11 separate-host proof; current deployment and all-replica ownership acceptance                                                                 |
| M5 T1–T15 distribution/operations | Fleet contracts/schema, CLI/GUI, images, Compose, Helm and documentation present                                       | Current immutable multi-arch artifacts, final Compose run and guarded release evidence; historical eight-engine deployment is not current topology |

**Filter categories — all S-1–S-10 and T1–T20**

| IDs              | Implementation/evidence                                                                                                      | Remaining work / next owner                                                                          |
| ---------------- | ---------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| S-1/S-2/S-3      | Shared exact index, collision confirmation, allocation/reuse and memory/build tests in `engine/src/filter/` and engine tests | Parent release-mode budget execution; preserve distinction between index size and rebuild peak       |
| S-4/S-5/S-7/S-10 | Read-only catalog, license acknowledgement, toggles, extraction, policies and GUI                                            | Implemented; final product/browser and live catalog evidence                                         |
| S-6/S-8          | Attribution fields, category counters and index metrics                                                                      | W4-02 publication-bound attribution regression                                                       |
| S-9              | Synthetic benchmark and live filter-fleet acceptance                                                                         | W4-14 hardware proof; current strengthened UUID/freshness acceptance requires a new guarded live run |

Historical measurements are workload-specific: the recorded 5.136M corpus includes a **124ns whole-Zipf mean**; **32.9ns** describes repeated decisions, not that mean. Release timing tests using selected/best measurements are not sustained reference-host proof.

The current live filter test now requires independently supplied UUID/name/group inventory, exact nonempty cardinality, pinned target versions, fresh positive samples and UUID-keyed reports. Its earlier vacuity defect should not be assigned again.

**M6 — all S-1–S-16**

| IDs             | Existing implementation/tests                                                                          | Remaining work / next owner                                                                                |
| --------------- | ------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------- |
| S-1/S-2         | Partial-name/multi-value querylog API/backends and browser cases                                       | Final four-backend execution; M11’s broader-filter aggregation gap is separate                             |
| S-3             | Detailed decision reasons and `e2e/filter_attribution_test.go`                                         | W4-02; ordinary attribution passes do not cover queued records across publication                          |
| S-4             | Dashboard APIs, series/top lists and UI                                                                | Final execution; large-fleet sample-decoding debt below                                                    |
| S-5/S-6/S-7/S-8 | Forwarding/recursion navigation, redirects, help catalog/gate, descriptions and collapsible navigation | Implemented; final browser/live UX evidence                                                                |
| S-9             | Collapsed category UX and tests                                                                        | Implemented; no duplicate UI task                                                                          |
| S-10/S-11       | Resolver/authoritative ACL split and preserving migration; `e2e/access_split_test.go`                  | Final migration/runtime verification                                                                       |
| S-12            | Parallel upstream selection and product/browser tests                                                  | Historical latency execution exists; preserve actual workload attribution and final candidate verification |
| S-13/S-14       | Engine modal/metrics, bounded logs and broker                                                          | W4-01 current ownership verification; retain masking, bounded storage and unavailable states               |
| S-15            | Account profile/password API/UI                                                                        | W4-13 proxy-aware throttle boundary                                                                        |
| S-16            | Build stamping, version API/footer/reload hint                                                         | Exact final image/API/GUI version correspondence remains deployment acceptance                             |

**M7 — all S-1–S-22**

| IDs                 | Existing implementation/tests                                                                    | Remaining work / next owner                                                                                                  |
| ------------------- | ------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------- |
| S-1                 | CI/images/fuzz/performance definitions and historical ARC evidence                               | Current artifact/run evidence; W4-14/W4-15. Historical ARC failures need fresh diagnosis before being called current outages |
| S-2                 | Compose verification and historical novanas `sha-1cc283d` execution                              | Parent final-image Compose acceptance                                                                                        |
| S-3/S-4             | Wildcard hierarchy fixture; bounded collector restart                                            | Implemented; ordinary Linux regression execution                                                                             |
| S-5/S-6             | Merged ACL ranges/binary search; off-worker transfer planning/encoding                           | Implemented with behavior/allocation tests                                                                                   |
| S-7/S-8             | Certificate fallback and unstorable identity retention                                           | Implemented; final lifecycle verification without reenrollment or trust replacement                                          |
| S-9                 | Last trusted-key revocation and lost-trust reporting                                             | W4-12 browser proof                                                                                                          |
| S-10/S-11/S-12/S-13 | Byte-bounded denial cache, atomic infra updates, recursor budget, owner-local IXFR deletion keys | Implemented; final engine/API/snapshot verification                                                                          |
| S-14/S-15           | Buffer pools and BADVERS wire behavior                                                           | Implemented; final allocation/wire tests                                                                                     |
| S-16                | Runtime-version upstream/group labels                                                            | Bounded history remains; W4-02 covers other attribution identities                                                           |
| S-17                | Four concurrent exports, bounded queue                                                           | W4-03 exact 400ms criterion remains unaccepted                                                                               |
| S-18/S-19/S-20      | Streamed blobs, stable OpenSearch paging, PKCS#11 session recovery                               | Parent real database/backend/SoftHSM execution; retain memory and invalid-session assertions                                 |
| S-21/S-22           | Streaming export and incremental unsigned edits                                                  | Implemented; signed/ZONEMD whole-zone rebuild remains explicit debt                                                          |

**M8 — all S-1–S-14 and T1–T34**

| IDs / tasks                    | Existing implementation and recorded execution                                                 | Remaining work / next owner                                                                                   |
| ------------------------------ | ---------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
| S-1, T06/T17/T21/T26           | mDNS gateway/runtime/cache identity and real namespace E2E                                     | **E/A:** final candidate Linux run; no LAN multicast claim on ordinary kw pod networking                      |
| S-2, T18/T21/T26               | Reflector lifecycle, shutdown, loop/disable tests                                              | **E:** preserve separate IPv4/IPv6 assertions and exact membership parsing                                    |
| S-3, T10/T26                   | Namespace harness and fixture responders                                                       | Linux capabilities are execution prerequisites; Darwin is not a substitute                                    |
| S-4, T04/T05/T12/T23           | Go/Rust ZONEMD digest, primary rebuild and signed/unsigned tests                               | **C/T:** W4-04 exact API contract mismatch                                                                    |
| S-5/S-6, T13/T20/T23           | Secondary/RPZ verification-before-replacement and last-good/persisted-copy tests               | **E/A:** final transfer regressions. ZONEMD integrity is not new DNSSEC authentication of transfers           |
| S-7/S-9, T07/T19/T24           | ODoH target/proxy, independent HPKE client, allowlist/header/error tests                       | **E/A:** final wire and live smoke. Target sees the proxy identity; do not invent original-client attribution |
| S-8, T09/T16/T24               | Sealed seeds, publication delay/expiry and fenced control delivery                             | W4-01; queued/in-flight material is not retroactively retracted                                               |
| S-10/S-11, T08/T14/T15/T22/T25 | Catalog producer/consumer, stable labels, transactional hooks and real BIND product tests      | **E/A:** current candidate interoperability and live smoke                                                    |
| S-12, T02/T27–T31              | Five feature UI slices, RBAC/contracts/help/screens 40–44                                      | Prior full GUI and seven local unit passes exist; current/live acceptance remains                             |
| S-13, T01/T03                  | Migrations 01302–01305 preserve 01300/01301; legacy-history refusal and row-preservation tests | **T:** W4-05 snapshot assertion. Legacy refusal is not an automatic transition                                |
| S-14, T32                      | Four operations sections and relevant caveats exist                                            | **T:** W4-06 missing documentation gate                                                                       |
| S-14, T34                      | Guarded deployment infrastructure exists                                                       | **C/T:** missing `TestKwSmokeM8` and wiring                                                                   |
| T33/T34                        | Prior combined engine, management, protocols and GUI runs                                      | **E/A:** post-control-final full execution, immutable deployment, scratch cleanup and strict acceptance       |

The missing live smoke should cover the planned ZONEMD/AXFR verification, catalog member PTR, ODoH after its normal publication delay, and scratch empty-group mDNS configuration roundtrip. It must restore scratch state and must not manipulate database timestamps to bypass ODoH publication timing.

Migration evidence that kw had applied versions only through 1207 pertains to that recorded database inspection. It is not permission to rename history in another M8 database.

**M9 — all S-1–S-14**

| IDs       | Existing implementation/tests                                                                      | Remaining work / next owner                                                                                  |
| --------- | -------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| S-1/S-2   | Operator module/image/chart, CRDs/deepcopy, strict parity including AI/MCP and M10 querylog fields | Parent current generation/artifact checks; absent-field historical claims are superseded                     |
| S-3       | Render/apply/prune, ownership and no-mutation-on-render-error tests                                | Final operator execution; preserve raw Helm goldens                                                          |
| S-4       | Key creation and incomplete-existing-secret refusal                                                | Parent isolated key-retention acceptance; never regenerate existing trust to pass                            |
| S-5/S-6   | Workload/join-token gating, rollout behavior and truthful status                                   | Current isolated rolling-update/continuity proof; Ready is insufficient                                      |
| S-7/S-8   | Group reconciliation, CAS, token rotation/revocation and orphan recovery                           | Current isolated API/lifecycle execution                                                                     |
| S-9       | Bootstrap token/system users, API restrictions and Helm wiring                                     | Final combined bootstrap proof; first human admin remains an explicit manual step                            |
| S-10/S-11 | CNPG HA/backups/restore rendering and real isolated test definitions                               | Parent actual CNPG election and backup/restore execution; envtest does not supply these                      |
| S-12      | Platform docs and documentation tests                                                              | Final documentation gate                                                                                     |
| S-13      | `operator/test/kw/kw_operator_test.go` and owned-namespace runner                                  | Historical all-subtest run exists at another artifact; repeat current candidate with cleanup/retention proof |
| S-14      | Guarded continuity scripts                                                                         | Current release acceptance. **Production adoption into `NexoraInstallation` is excluded**                    |

The operator’s latest recorded race/envtest pass supersedes earlier pending-Linux render notes. It does not establish current production backup policy or HA.

**M10 — all S-1–S-12**

| IDs         | Existing implementation/tests                                                                   | Remaining work / next owner                                                                                        |
| ----------- | ----------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| S-1/S-2/S-3 | ClickHouse schema/ingestion, parameterized search/cursors and Top                               | Current real conformance/live retained-state ingestion; CREATE-only schema setup is not a general upgrade migrator |
| S-4/S-5     | Loki OTLP transformation, search/escaping/paging/error handling                                 | Current live fan-out; identical timestamp+line dedup remains a documented limit                                    |
| S-6         | Loki Top, ties and bounded partition fallback                                                   | Querylog owner: exact time-boundary regression; code documents counting less than 1ms after `To`                   |
| S-7         | Shared conformance and intentionally divergent-backend negative control                         | Parent real four-backend execution with explicit tool prerequisites                                                |
| S-8         | Adapter configuration/wiring and four-backend product/browser tests                             | Final combined execution; external backends must not accidentally enable management-memory ingestion               |
| S-9         | Helm secret-file wiring, schema and operator parity                                             | Final render/generation checks                                                                                     |
| S-10        | kw ClickHouse/collector manifests, strict 20-query equality/top-list test and acceptance wiring | Parent guarded current-release deployment and live equality; preserve existing credentials/PVC data                |
| S-11        | Pinned tools, harness and selftest                                                              | Establish current immutable toolbox/PATH provenance; historical isolated build is not current environment proof    |
| S-12        | Operations/help and docs tests                                                                  | Final docs gate; content is present                                                                                |

No shared-Loki mutation, extra Loki installation, replicated ClickHouse, kw ClickHouse TLS, or retention GUI should be silently added as an M10 requirement.

**M11 — all S-1–S-15**

| IDs     | Existing implementation/tests                                                                           | Remaining work / next owner                                                                                                                 |
| ------- | ------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| S-1     | Provider/configuration, disabled behavior, typed generation, scheduler/tasks/pruning, metrics and tests | W4-08 budget dispatch; W4-09 transport privacy proof; final Linux/product execution                                                         |
| S-2     | Validated proposals, ordinary-handler replay with human credentials, audit and suggest-only tests       | Final current-candidate negative/positive proof; preserve no-agent-writes invariant                                                         |
| S-3     | Natural-language search translation, result URL and summary                                             | W4-07 exact-filter aggregation                                                                                                              |
| S-4     | Anomaly detector/model fallback and tests                                                               | Implemented; newest-5,000-record ceiling limits fleet coverage                                                                              |
| S-5     | Insight agent/dashboard                                                                                 | Implemented; 24-hour per-engine decoding is scale debt                                                                                      |
| S-6     | Recommendations, dedupe/cooldown and license-aware apply                                                | Implemented; newest-50,000-record ceiling and human licence acknowledgement remain                                                          |
| S-7     | Assistant/session ownership, supersession and explicit apply                                            | Implemented; prior fixture/UI races corrected and recorded                                                                                  |
| S-8/S-9 | Upstream predictions and rollout-risk computation/UI                                                    | Final execution; preserve sole-upstream protection and nonblocking rollout assertions                                                       |
| S-10    | Threat checks/classification/cache/UI                                                                   | Implemented; unclassified-list `null` versus array compatibility debt remains                                                               |
| S-11    | Capacity projection plus actual recursor byte/limit sampling                                            | Historical missing-sampler claim superseded; final telemetry/model/live proof remains                                                       |
| S-12    | RPZ suggestions/exclusions/apply/recreate                                                               | Implemented; sampling and model-word-sensitive fingerprint limits remain                                                                    |
| S-13    | MCP authentication, role filtering, read-only default, origin check and stdio bridge                    | Final current-release MCP execution; MCP origin checks do not prove provider privacy                                                        |
| S-14    | AI pages/components and screens 50–60                                                                   | Prior full GUI pass; current/live acceptance still separate                                                                                 |
| S-15    | AI Secret references, alerts, docs, kw smoke and acceptance wiring                                      | Missing work is current guarded deployment/private-model/token/invalid-output/manual-UI acceptance, not creating already-present smoke code |

Proposal application still has a recorded process-death limitation: per-action progress is not durably reported independently of the outer claim. Reapplication may encounter already-applied actions as stale. API owner should add a mid-apply process-death regression before changing progress persistence; preserve ordinary-handler authorization and audit.

---

**Failover groups and proposed sprint matrix**

All rows remain open as stories. The existing non-expiring **deployment lock** is distinct from the proposed frontend **Lease/kernel-ticket protocol**. Neither should be substituted for the other.

| Story / parent task | Current implementation and execution                                                                                                   | Remaining gap                                                                                                                                                           | Dependency / next owner and exact scope                                                                                     |
| ------------------- | -------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------- |
| FG-01 / T1          | Same-host and two-host DR fixtures; corrected 80-flow actual-engine attribution                                                        | Managed control attachment, production attachment/return path, host provenance, ACL/rate-limit variants, sustained isolation and session/PMTU coverage                  | Network/E2E owner: `deploy/failoverlab/{cmd/probe,crosshost}/`, managed harness; parent executes                            |
| FG-02 / T2/T4       | Desired model, pure eligibility and trusted Kubernetes observation reader                                                              | **No authoritative publisher or production caller.** UUID→Pod/container/Node correlation, actual applied digest provenance, RBAC/client/scheduling/expiry wiring absent | Management/platform owners: `mgmt/internal/failover/`, control/provisioning correlation, deployment RBAC. Feeds FG-04/06/07 |
| FG-03               | Historical failures and newer Linux evidence retained                                                                                  | Final exact candidate baseline; classify actual failures separately from prerequisites/skips                                                                            | Parent verification: evidence manifests and supported suites                                                                |
| FG-04 / T4          | Inactive owned IPVS/address/route adapter; real stage/repeat/recovery/cleanup                                                          | Production `Fence.run_closed`, all-writer serialization, allocation/anti-spoofing and separate management attachment integration                                        | Network owner: `deploy/failover/platform/`, provisioning/image/chart boundary; depends FG-01/02                             |
| FG-05 / T4          | Classic-TC expiry primitive; Lease CAS library; strict child-pipe/clock binding                                                        | Actual driver/API/frontend integration, two LB instances, privilege separation, all-path enforcement, measured clock/drain margin, successor/partition tests            | Network/security owner: `deploy/failover/{fence,lease,platform}/`; depends FG-04                                            |
| FG-06 / T2/T4       | Reservations and conservative observed-state reader                                                                                    | Health admission, expiry enforcement, drain/readmission, fenced observation writes, tombstones and withdrawal acknowledgements                                          | Management/network owners: `mgmt/internal/failover/`, new migration, `deploy/failover/`; FG-02/04 and FG-05 for reuse       |
| FG-07 / T3          | Desired-state library only                                                                                                             | Failover OpenAPI/API/generated clients, admin RBAC, same-transaction audit, CAS and withdrawal-pending deletion                                                         | API owner: `mgmt/api/openapi.yaml`, `mgmt/internal/api/`, generated clients; depends FG-06                                  |
| FG-08 / T3          | Existing policy/fleet GUI is a different feature                                                                                       | Dedicated group list/detail/edit/delete/status/help and real API-backed browser tests                                                                                   | Web owner: `web/src/{api,pages,help}/`, navigation, `web/e2e/`; depends FG-07                                               |
| CP-01               | Fixed-target bounded bootstrap convergence with negative tests                                                                         | Unattended immutable-artifact rollout that exits zero without manual rescue                                                                                             | Parent deployment owner: `deploy/kw/bootstrap.sh`, `deploy/kwrollout/`; gates FG-09/10                                      |
| CP-02               | Persisted session fencing, transactional callbacks/logs, fresh-secret commit barriers, TLS recovery, final displacement/shutdown fixes | Final Linux execution and all-management-replica upgrade acceptance                                                                                                     | Control owner and parent: `mgmt/internal/control/`, real adapters/E2E; gates reliability acceptance                         |
| FG-09 / T5          | Existing paired chart/OnDelete/serial rollout/lock guards                                                                              | Dedicated frontend/attachment chart, staged deployment and new topology probes                                                                                          | Deployment owner: `deploy/helm/nexora/`, `deploy/deploytest/`, `deploy/kw/`, `deploy/kwrollout/`; FG-04–08, CP-01           |
| FG-10 / T5          | Legacy paired migration/recovery mechanisms                                                                                            | Explicit stage→withdraw old→activate new→verify/rollback transitions and interruption recovery                                                                          | Deployment owner: migration runner/tests; FG-09 and prior gates. `.136` A/C first, then `.139` B/D                          |
| FG-11 / T6          | Bounded forwarding experiments; historical abrupt engine-a RED                                                                         | Every member/LB/node/network loss case, empty pool, ownership loss, reused sessions, drain and frozen timing thresholds                                                 | Parent verification: `e2e/`, `deploy/kwrollout/`, failover lab; depends FG-10                                               |
| FG-12 / T6          | Strict acceptance scripts and prior unrelated artifact passes                                                                          | Final supported suites, immutable deployed artifact, complete live matrix/runbooks and later human workflow evidence                                                    | Parent release owner; depends FG-11 and CP-01/02                                                                            |

Two integration hazards deserve explicit regression scope:

1. `failover.Update` presently deletes/reinserts membership reservations immediately. That is a desired-state operation with no active frontend caller today. **Do not expose it as safe active membership editing** until FG-06 retains old reservations through drain and independently proven withdrawal.
2. The staging adapter rejects the additional management link used by the successful engine fixture. The platform, fence and engine experiments therefore do not yet compose into one proven system.

The observation reader’s repeated Kubernetes reads detect moving inputs but are not an atomic Kubernetes/database transaction or durable authority. Its caller must discard errors, expire evidence, and never retain a previous eligible pool on failure.

**Failover acceptance coverage**

| Spec criteria                                    | Current disposition                                                                              |
| ------------------------------------------------ | ------------------------------------------------------------------------------------------------ |
| AC1 model/admission/eligibility rejection        | Partial model/pure-reader implementation; publisher, runtime and API rejection paths missing     |
| AC2 persistence/API/UI                           | Persistence present; dedicated API/UI absent                                                     |
| AC3 exact tuple and engine-log attribution       | Bounded corrected standalone lab proof                                                           |
| AC4 both paths, large replies, MTU and sessions  | Both paths and signed payload/truncation proven in lab; general PMTU and reused sessions missing |
| AC5 each engine loss with stable frontend owner  | Historical engine-a case RED; dedicated frontend implementation/matrix missing                   |
| AC6 LB loss/partition/no duplicate advertisement | Primitive tests only; two-owner active acceptance missing                                        |
| AC7 safe upgrades                                | Existing paired guards; frontend ownership/drain integration missing                             |
| AC8 guarded migration and rollback               | Dedicated frontend migration absent                                                              |
| AC9 Linux/chart/strict acceptance                | Scoped prior execution; current final-candidate and deployed acceptance incomplete               |

Historical pair AC1–4 have chart/runtime tests; AC5/7 have historical four-engine acceptance. Pair AC6 remains incomplete for abrupt failure and unattended rollout. The superseded Service/Local mechanism does not supersede identities, client tuples or serial safety.

---

**Security, testing and platform debt**

| Debt / limit                                                                              | Consequence                                                                                | Next owner/scope                                                                                                                                                               |
| ----------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| CI downloads a CA using `curl -sk`, then trusts it                                        | Trust bootstrap is unauthenticated in the inspected workflows                              | CI/security: independently provisioned or pinned CA; failed-fetch/wrong-CA tests in `.github/workflows/{ci,fuzz,perf-gate,images}.yml`. No current trust changes by this audit |
| Image workflow uses insecure registry configuration; SBOM/provenance disabled             | Artifact verification is narrower than a hardened supply-chain claim                       | CI/release owner: explicit transport/provenance policy and artifact checks; distinguish hardening from milestone scope                                                         |
| `telemetry/process.rs` assumes 100Hz ticks and 4096-byte pages                            | Other supported Linux configurations can produce wrong CPU/RSS and capacity data           | Engine owner: runtime system values and conversion regressions; verify cgroup layout assumptions                                                                               |
| Linux-specific engine; privileged netns/BPF/IPVS prerequisites                            | Darwin compilation is unsupported-platform evidence, not a Linux product failure or a pass | Verification owner: supported runner classification; no unauthorized macOS port                                                                                                |
| Fence requires classic-TC path, exclusive administration and no alternate transmit bypass | Kernel smoke does not prove privileged-bypass resistance                                   | Platform/security: capability separation, supported-kernel matrix and path-specific negative fixtures                                                                          |
| Physical clock rate, VM pause/resume and post-gate drain bounds unmeasured                | Positive margin configuration alone does not prove safe takeover                           | Network/platform owner: measurement and supported-platform contract before FG-05 acceptance                                                                                    |
| Builtin querylog is per-instance/in-memory                                                | Not a shared durable backend for management HA                                             | Retain documented external-backend requirement                                                                                                                                 |
| Loki identical-event dedup and sub-millisecond Top boundary                               | Not universally exact event retention/window aggregation                                   | Querylog owner: boundary/collision fixtures and explicit result semantics                                                                                                      |
| OpenSearch wildcard scans and `_id` paging prerequisites                                  | Large indices/configurations can fail or time out                                          | Querylog owner: measured scale threshold and prerequisite checks                                                                                                               |
| Dashboard/insight decode 8,640 samples per engine/day                                     | Large-fleet scalability unproved                                                           | Stats/AI owner: measured workload, then rollup/query redesign if required                                                                                                      |
| Signed/ZONEMD zone edits rebuild the zone                                                 | Incremental unsigned edit proof does not apply                                             | Zone owner: retained ceiling and measured large-zone tests                                                                                                                     |
| PostgreSQL pooled query parameters may retain large uploaded blobs                        | Memory bounded by pool size × large input, rather than only active operation               | Store owner: explicit retention measurement before redesign                                                                                                                    |
| Recursive IPv6 may stay disabled until restart after detection failure                    | Recovery behavior is narrower than dynamic network recovery                                | Recursor owner: controlled restoration/reprobe design if required                                                                                                              |
| Operator bootstrap admin token uses in-cluster HTTP                                       | Network protection remains a deployment security boundary                                  | Platform/security design; NetworkPolicy is explicitly outside M9’s promised scope                                                                                              |
| CNPG in-tree Barman configuration has a recorded migration trigger                        | Backup capability is not a complete production recovery policy                             | Platform owner: plugin transition when applicable; parent test restores/CA/KEK backup evidence                                                                                 |
| Procoder version/runner coverage limitations in historical notes                          | Zero checked files or unsupported review commands are not clean gates                      | Parent workflow owner: supported commands, explicit file coverage and preserved diagnostic results                                                                             |

Operational documentation also needs bounded reconciliation before release: generic PROXY-based source guidance must not be used to satisfy transparent failover, and KEK error recovery must not suggest replacing a key needed to decrypt retained data.

**Planning-only and excluded requirements**

- Split DNS remains **planning-only**, including unsigned/signed primary and secondary views. Its S-1–S-13 and planned tests are not satisfied by existing policies, ACLs, zones or DNSSEC. Architecture review and implementation authorization remain separate.
- Pi-hole parity is excluded; its comparison note creates no backlog obligation.
- DHCP #34, packages #38 and tarballs #39 remain pending decisions.
- Performance #2/#6/#7/#8 requires the actual reference environment; no laptop/virtual substitute.
- Windows/macOS engine support is excluded.
- Preserve Cilium VXLAN; do not revive the failed native-routing migration.
- No extra client-facing DNS IP, resolver-mode change, generalized N-member failover, or lossless established-session promise.
- M8 does not promise LAN multicast on current kw pods, ODoH client operation, catalog extensions/mass-deletion protection, or DNSSEC transfer validation through ZONEMD.
- M9 does not authorize production operator adoption.
- AI remains suggest-only; no automatic application or reasoning-text exposure.

**Formal workflow state**

| Milestone | Historically closed | Open-prefix | Other nonclosed |   Total |
| --------- | ------------------: | ----------: | --------------: | ------: |
| M1        |                  23 |           0 |               0 |      23 |
| M2        |                  13 |           0 |               0 |      13 |
| M3        |                  15 |           0 |               0 |      15 |
| M4        |                  16 |           0 |               0 |      16 |
| M5        |                  15 |           0 |               0 |      15 |
| M6        |                   0 |          31 |               3 |      34 |
| M7        |                   0 |          22 |               1 |      23 |
| M8        |                   0 |          29 |               5 |      34 |
| M9        |                   0 |          12 |               0 |      12 |
| M10       |                   0 |          12 |               0 |      12 |
| M11       |                   0 |          21 |              11 |      32 |
| **Total** |              **82** |     **127** |          **20** | **229** |

“Other nonclosed” includes done/implemented/awaiting-commit variants. These are not treated as formal closures.

Eight specs say complete; split DNS is draft, and the two failover specs have no comparable status declaration. M1–M5 plans say implemented; categories/M6–M11 remain draft. The pair plan’s “not deployed” header is historical. The failover sprint document explicitly remains **proposed**; no `.procoder/backlog/` or activated sprint records exist.

No checkbox, task, sprint or milestone should be marked done from this report or source presence.

**Runnable parent verification**

These commands are **handoff instructions, not executed results**. Run in the parent’s isolated supported Linux environment with freshly built candidate binaries, real web assets, required fixture tools, independent artifact directories and live opt-ins disabled for ordinary tests.

First verify the final control fixes:

```sh
make e2e-build

go test -race ./mgmt/internal/control -count=1 -timeout 20m

go test -race ./mgmt/internal/control -count=1 -timeout 20m \
  -run 'TestDisplacedTSIGRecovery|TestConnectNonReadingPeerShutdown|TestCancelableReceiveLifetime|TestGRPCReturnReleasesTransportWorkers|TestCommitBarrier|TestConnectionOwnershipAcrossServers|TestFreshSecret|TestOdoh'
```

Then execute the combined supported suite:

```sh
make engine-test
cargo test --locked --release -p nexora-engine --test filter_index_budget

go test -race -count=1 \
  ./mgmt/... ./gen/... ./bench/... ./deploy/... ./e2e/harness/...

make web-test
(cd web && pnpm exec playwright test e2e/unit)

make operator-test
make lint

NEXORA_E2E_BIN_DIR="$PWD/bin" \
  go test -count=1 -v -timeout 120m \
  ./e2e/... ./mgmt/internal/querylog/e2e/...
```

Focused M8 checks:

```sh
go test ./mgmt/internal/store -run '^TestM8' -count=1 -v

go test ./mgmt/internal/stats \
  -run '^TestRecordM3ZonemdUsesCallerTransaction$' -count=1 -v

NEXORA_E2E_BIN_DIR="$PWD/bin" \
  go test ./e2e -count=1 -v -timeout 30m \
  -run 'TestMdns|TestODoHTargetAndProxy|TestCatalogZone|TestZonemd|TestRPZZonemd'
```

Failover library/fixture checks:

```sh
go test -race ./deploy/failover/lease -count=1 -timeout 240s
go vet ./deploy/failover/lease

go test -race ./deploy/failoverlab/cmd/probe -count=1

PYTHONDONTWRITEBYTECODE=1 \
  python3 -O -m unittest discover -s deploy/failoverlab -v

PYTHONDONTWRITEBYTECODE=1 \
  python3 -O -m unittest discover -s deploy/failoverlab/crosshost -v

python3 -B -m unittest discover -s deploy/failover/platform -v
make -C deploy/failover/fence test
```

Parent-only isolated kernel/staging commands and prerequisites are documented in:

- `deploy/failover/fence/README.md`
- `deploy/failover/platform/README.md`
- `deploy/failover/lease/README.md`
- `deploy/failoverlab/crosshost/REAL_ENGINE.md`

Those experiments must be composed and tested together before they support FG-04/05 acceptance. No existing command currently supplies the missing full active-frontend integration.

After adding missing regressions, verify each expected test **exists and executes**. A successful `go test -run` with “no tests to run” is not evidence. Specifically retain separate results for:

- Original **400ms** OTLP criterion.
- Exact M8 **400 `invalid_request`** rejection.
- Post-migration snapshot behavior.
- `TestOperationsGuideCoversM8`.
- `TestKwSmokeM8`.
- AI queued-budget, cross-instance-budget and exact-filter aggregation cases.
- Provider transport privacy.
- Publication-bound attribution and lost-trust UI behavior.

Live operator/CNPG, multi-host management, performance, frontend failure and release acceptance remain separate parent-owned stages. Before any later live rollout, preserve the non-expiring lock, verified quiescence/CAS recovery, serial partners, `.136` A/C, `.139` B/D, persistent identities/trust/data, exact client attribution and strict failure handling.

**Recommended next ownership order**

1. Parent verifies the integrated control-final fixes and records the exact candidate.
2. Independent code slices address W4-02–09 and W4-11–13; scope owner resolves optional dnstap explicitly.
3. Failover owners implement the authoritative publisher and compose provisioning, staging, gate and Lease into one inactive-first runtime.
4. Prove active ownership, lifecycle and reservation safety before exposing failover CRUD/UI.
5. Add guarded frontend migration only after those gates; then execute the frozen failure matrix.
6. Parent performs current immutable-artifact deployment and acceptance. Formal workflow reconciliation remains a later human action.

**Implementation status:** extensive candidate present, with the concrete gaps above.

**Execution status:** substantial prior Linux evidence; final-control/current-candidate verification pending.

**Acceptance status:** dedicated frontend HA, complete release acceptance and milestone completion remain unestablished.
