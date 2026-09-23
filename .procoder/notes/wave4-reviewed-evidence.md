# Wave 4 integration evidence — still in progress

Main remains `f9c1653`, with staged and unstaged candidate changes. No serving
migration, deployment, frontend cutover or formal task/milestone closure occurred.
Only exact snapshot-relative agent deltas were imported; historical M8/M10 trees
remain untouched. This note supplements, not replaces, preserved failed runs.

## Executed supported checks

- Control lifetime: bounded rollback/failed-BEGIN retirement, known TLS wrapper
  unwrapping and joined UPDATE workers imported. Targeted store race **7.966s**;
  real non-reading gRPC shutdown cases **5.425s**. Broader race: control
  **162.742s**, store **64.721s**, dynupdate **14.319s**, zone **52.290s**, xfrin
  **25.921s**, stats **24.112s**. Log: `/tmp/nexora-wave4/control-lifetime-linux.log`.
- Earlier enabled product rerun completed: GUI **332.16s**, complementary native
  E2E **1297.461s**. This predates subsequent integrations, including lifetime
  fixes; it is not final combined acceptance. Log: `product-linux.log` in wave4.
- M8/auth exact deltas imported. Targeted Linux race: auth **78.646s**, API
  **53.329s**, store **45.219s**, docs **1.070s**, fixture tests **1.122s**. Actual
  lost-trust appearance/hold-down/recovery browser test passed **9.28s**; exact
  ZONEMD product test passed **4.09s**. Log: `auth-m8-linux.log`.
- Original-handler ZONEMD mutant and both lost-trust browser mutants produced
  genuine assertion failures, not setup/compile failures. Log:
  `acceptance-negative-reviewed-linux.log`. Earlier `kubectl cp deploy/toolbox`
  failed because cp requires a pod, not a deployment; that attempt is not evidence.
  Corrected transfer used exec stdin. Mutations were Go overlays, not source edits.
- AI/query-log 22-file delta imported. All AI packages and builtin query-log Linux
  race suites passed. Real OpenSearch/ClickHouse/Loki conformance, including exact
  nanosecond Loki boundaries, passed **74.801s**, with no skipped cases in that
  selected run. Log: `ai-querylog-linux.log`. New independent review still found
  the accounting defect below; passing tests do not resolve it.
- Lifecycle migration **1306**, package and bounded one-attempt Store wrapper/tests
  imported. Store/M8 focused Linux race passed **35.755s**, full failover race
  **108.608s**, vet passed. Log: `lifecycle-linux.log`. Historical 1301 fixture now
  seeds old SQL rather than calling new-schema CRUD, compares every original
  column including nulls, and separately asserts additive lifecycle/reservations.
  Other incompatible-history tests retain their full-row comparisons.
- Engine attribution/exporter ten-file delta imported. Supported Linux targeted
  and all-targets tests passed, including original **400ms / 20,000 records / no
  drops**, separate 250ms test, publication-bound identities and allocation/lifetime
  coverage. First pipeline ended RED on Clippy `items_after_test_module`. Parent
  moved the unchanged memory-limit function before the test module; process tests
  and full all-targets Clippy then passed. Logs: `engine-attribution-linux.log`,
  `engine-clippy-fixed-linux.log`. No reference-hardware performance claim.
- Read-only serving preflight passed four distinct identities, both endpoint sets,
  node capacity, management/configuration and direct/VIP UDP/TCP. Log:
  `preflight-readonly.log`. Disposable helper built directly, without dev-sync.

All paths above are under `/tmp/nexora-wave4/` unless written absolutely. Retained
Playwright artifacts remain behind their private per-invocation ancestors.

## Detached runtime execution and corrections

The frozen detached candidate is at
`/tmp/nexora-wave4/runtime-detached-files/deploy/failover`; its 12-file delta was
imported. Linux runtime/lease race, vet and Python contracts passed in the dedicated
`/work/nexora-wave4-runtime` tree. Live opt-in integration was separately executed
by the parent on worker21, with real etcd/API-server and the actual classic-TC
kernel driver, exclusively inside owned detached namespaces.

Three failed attempts remain preserved:

1. `runtime-actual-linux.log`: a named namespace's bind-mounted FD renders its
   pathname, not `net:[inode]`. The harness now compares pinned device/inode to
   current and initial namespace identities; a regression checks mismatches.
2. `runtime-actual-reviewed-linux.log`: iproute2 reports local peers by `link` name,
   not necessarily `link_index`. Inventory now resolves only within the exact
   five-link inventory, requires reciprocal peers and rejects conflicting name/
   index representations. All external/master/address/extra-link denials remain.
3. `runtime-inventory-actual-linux.log`: trusted-path checks correctly reject a
   root-owned tree underneath writable `/tmp`. The parent moved private artifacts
   to root-owned `/opt/nexora-wave4-runtime`; validation was not relaxed.

`runtime-trusted-path-linux.log` then passed detached runtime and owned cleanup:
**48 steps, 27 ARM operations**. This proves the detached integration path, not
active DR fencing or production HA. The first actual driver/API packet run failed
on the same local-peer-name assumption. Parent added strict `labActualPeer` resolution
and its regression. `driver-api-reviewed-linux.log` then passed the real API CAS,
actual binding, quarantine/ARM, ARP/GARP/data expiry, independent management,
ownership loss, cancellation and restart refusal in **15.63s**. Owned cleanup also
passed. This is still detached driver verification, not DR/HA acceptance.

Private `/usr/sbin` overlays supply already packaged tools only within mount
namespaces. No host package installation, serving network change or trust change
occurred. Synthetic lab keys disappear with private `/run`. Parent changes to
`runtime/{lab.py,test_lab.py,inventory.go,runtime_test.go}` must be preserved when
merging the active-runtime follow-up; do not overwrite them with its older base.

## Concrete remaining work and ownership

- Independent AI review (`review-ai-followup.result`) found P2: a five-second
  accounting timeout can forget known model usage, letting a subsequent request
  dispatch against stale totals. Parent reproduced the genuine defect in Linux:
  `ai-accounting-negative-linux.log` failed in **7.75s**, with the later call
  returning nil error and provider calls increasing to two. The receipts v2
  candidate is delivered but not imported or PostgreSQL-executed. It addresses
  shared-pool starvation, cancellation during setup, pre-dispatch rechecking and
  populated receipt downgrade refusal. Migration **1307** remains reserved. Known usage
  loss is distinct from an unpromised exact ceiling on unknown in-flight usage.
- Active-v1 runtime candidate: **19 exact source/doc files imported**, with before/
  after SHA checks, preserving newer parent detached fixes. Frozen source is in
  `/tmp/nexora-wave4/runtime-active-v1-files`; imported list is
  `runtime-active-v1-import.txt`. It is NOT Linux-executed or accepted. Inspection
  already finds repeated named-namespace FD `readlink` and `link_index`-only
  assumptions in the NEW active harness/platform validator; fix with strict kernel
  identity/reciprocal-peer regressions before claiming active execution.
  `runtime-broker-followup` is implementing unprivileged owner/root helper separation
  against a saved active-v1 baseline. Clock/drain/platform proof and HA matrix remain.
- Lifecycle's Go release path requires a trusted verifier which is still absent.
  Raw withdrawal SQL credentials are not an independent proof boundary. No
  production publication/release caller was supplied by the lifecycle library.
- Fresh `publication` lane owns core/config/main/control provenance wiring and
  migration **1308** if needed. It must bind authenticated sessions to independently
  verified pod/container identity, not UUID-only Hello claims or guessed node names.
- Fresh `failover-api-ui` lane owns audited desired-state API/OpenAPI/generated
  contracts and UI. It must not expose caller-supplied authority or fake withdrawal.
  Both fresh trees use `integrated-snapshot.patch`, which includes extensive parent
  work; import only new deltas.
- Corrected CI coverage/trust exact delta imported, excluding its handoff. Parent
  `make ci-coverage-test` and four-workflow `actionlint` passed; logs/zero exit files
  are `ci-offline-reviewed` and `ci-actionlint-reviewed`. Test scripts marked
  executable consistently with their shebangs. This verifies offline contracts,
  NOT authenticated registry HTTPS or real workflow execution. The earlier default
  CA probe remains red; no insecure retry or credential/CA regeneration occurred.
- Final combined suites, independent reviews, Procoder reports, reviewed commits,
  guarded serial serving rollout and full acceptance remain open. Historical abrupt
  failover failure remains RED. Retained lock requires quiescence and exact CAS.

Optional dnstap awaits the scope answer in `../ask/decisions.md`. Explicit hardware,
packaging and DHCP holds remain; split DNS is planning-only and Pi-hole excluded.
No test may hide failed DNS attempts or weaken acceptance assertions.

## Latest handoffs and gate boundary

- API/UI delivered `failover-api-ui/wave4-delivery/NEW-delta.patch`, not imported.
  Seven required central auth registry entries are separately prepared in
  `REQUIRED-auth-permissions.patch`; without them every new endpoint is forbidden.
  Parent must review/integrate those entries with the surface, then execute real
  PostgreSQL and browser tests. No permission fallback is authorized.
- Publication delivered `.publication-handoff/new-deltas.patch` and manifest,
  not imported. Default-disabled wiring/1308 still requires provenance, network,
  clock, service-account and real PostgreSQL execution review.
- AI cumulative v2 patch is `.followup-ai/followup.delta.patch`; v1-to-v2 patch
  is `.followup-ai/v2-from-v1.delta.patch`, both in the original AI agent tree.
- Scoped Procoder check before the active-v1 import reported seven formatting-clean
  files, 114 hygiene findings and zero blocking findings. It ALSO reported **311 Go
  test failures and unsupported Darwin Rust compilation**, not a passing suite.
  It is not a final combined gate. Final review/test/check remain required.
- Audit report now identifies its historical snapshot and uses valid parent note
  links. Whitespace-only process telemetry EOF cleanup does not change its tested
  conversion logic. No serving mutation, new commit or formal closure occurred.
