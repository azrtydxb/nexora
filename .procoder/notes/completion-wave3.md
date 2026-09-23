# Completion wave 3 — acceptance and implementation audit

Audit date: 2026-09-17. Assigned worktree: `/tmp/nexora-wave3/audit`.
Baseline: `f9c1653ac1096ea17be62c587d86d8a2536d4872`.
This is a read-only product audit with one documentation output, not milestone,
sprint, release or deployment acceptance. No source, test expectations, task
states, other worktrees or git state were changed. Other wave3 implementation
worktrees are intentionally not evidence of this baseline.

Post-baseline implementation and parent-operated Linux results are recorded in
`wave3-integration.md` and `wave3-reviewed-evidence.md`. Their scoped passes do not
close the baseline ledger's remaining release/HA criteria.

## Evidence rules and provenance

The ledger separates **I** (implementation inspected), **T** (test implementation
and/or a specifically attributed recorded run), **D** (deployment at a named
historical SHA), and **A** (acceptance of the actual criterion). A test name or
checked todo proves neither execution nor complete acceptance. “Recorded PASS”
below means evidence reported by the cited repository note/task, not a rerun or
independent recovery of its ephemeral logs. No current live state is asserted.
Absent means absent in the inspected scope; preserved M8 code is distinguished
from code integrated into this baseline. Open governance records are not missing
implementation. Closed records are historical decisions, not fresh certification.

Sources: every spec and plan in `.procoder/{specs,plans}/`, all baseline standalone
todos, `.procoder/notes/{roadmap,completion-audit,completion-wave2,wave2-integration}.md`,
the feature source/tests referenced below, and preserved M8 at
`/Users/pascal/Development/nexora-m8`. No `.procoder/backlog/` exists; failover
“sprints” are proposed plan stories, not activated sprint records. No applicable
AGENTS.md was found in the assigned tree/ancestor locations checked. The supplied
procoder skill was read; independent read-only audits followed its parallel-work
instruction. No agent wrote product files or touched another tree.

### Baseline delta and evidence hierarchy

- `wave2-integration.md` supersedes older “integrate M10” and “fence stale
  NOTIFY/UPDATE/log replies” next-action wording: both are in `f9c1653`.
  M10 was transported as a reviewed patch; missing branch ancestry is not missing
  source. Operator ClickHouse/Loki validation, CRDs, render/persistence coverage
  and retained-PVC credential fail-closed behavior are also integrated.
- That note records full baseline `504a006` Linux e2e PASS (1389.569s), followed by
  integrated scoped race, full management, real backend conformance (49.743s),
  operator envtest/race, and real query-log product/browser PASS (71.025s).
  It does **not** record a full combined `f9c1653` release suite or deployment.
- Its laptop procoder result is RED: **241 Go failures plus Rust
  `libc::mmsghdr` compilation failure**, superseding the earlier 235 count for
  that run only. Earlier 229/230/235 runs retain their own environments and dates.
  Supported scoped Linux passes do not erase laptop failures or certify unrun suites.
- `kw-rollout-integration.md` records four-engine paired `sha-809cf3a`, revision
  35, strict acceptance PASS (459.272s), after a bootstrap propagation failure and
  explicit inspected CAS recovery. This is a recovered rollout, not unattended
  zero-exit deployment. CP-01 code subsequently landed; live proof remains open.
- `kw-member-failure.md` preserves abrupt engine-a RED: TCP refusal on `.136`
  after deletion, monitoring stopped, lock retained, no B/C/D disruption. Later
  recovered preflight is separate. The note's claim “bootstrap fix unimplemented”
  is historical and now stale; the measured failure is not stale or erased.
- `kw-deployment-recovery-20260916.md` records earlier two-engine `sha-4b99d80`
  strict acceptance (154.531s), not current four-engine or cross-instance-fenced
  acceptance. `kw-failover-readiness.md` describes an earlier reader-only stage;
  its “integration unfinished” is superseded by the assembled rollout code.

## FG/CP story ledger and failover acceptance

Unless explicitly stated, D and A remain unproven for the dedicated frontend
architecture. Existing paired Services are a deployed historical prerequisite,
not implementation of redundant dedicated frontends.

| Story / plan mapping | I and T at baseline                                                                                                                                                                                                                                                                                                                                                                          | D / A and exact remaining requirement                                                                                                                                                                                                                                                                                                                                                                            |
| -------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| FG-01 / T1           | `deploy/failoverlab/run.sh`, `verify.py`, `cmd/probe/`, `crosshost/{lab.py,verify_pair.py,test_lab.py}` implement same-host and userspace-carried two-host IPVS DR fixtures. Wave2 records 20 exact bidirectional tuples/group, 40 total, all five transports, two clients/both backends; final eight crosshost unit tests passed.                                                           | Lab only, echo probe. Actual Nexora attachment, source-policy/log attribution, large signed answers, truncation/MTU, reused connections and QUIC session behavior remain absent/unverified. SNAT negative control stopped on missing iptables; duplicate advertiser control unrun. No production attachment or HA acceptance.                                                                                    |
| FG-02 / T2,T4        | `mgmt/internal/failover/{groups,eligibility,status}.go`, their tests, `mgmt/migrations/01300_failover_groups.sql`: exactly two persistent IDs, unique membership/IP, generation CAS, conservative status and pure placement/freshness/config equivalence. `EvaluateEligibility` has no production caller in baseline. Recorded Linux failover/race evidence is in `parallel-integration.md`. | Trusted UUID→pod UID→physical node binding, actual applied-version digest provenance, freshness lifecycle and bound direct-DNS observation adapter absent. Self-reported node/policy membership is insufficient; pure supplied structs do not authenticate observations.                                                                                                                                         |
| FG-03                | Hardened `deploy/kwrollout/failure_live_test.go` and imported RED notes are integrated; wave2 integrated kwrollout race PASS supersedes “parent integration pending.” Safety tests check exact fleet/images, OnDelete, lock, post-observation state and cancellation.                                                                                                                        | Full combined supported baseline and per-failure classification still required. No new live failure pass; this audit does not execute the disruption harness.                                                                                                                                                                                                                                                    |
| FG-04 / T4           | Laboratory namespace cleanup/reconciliation only. `deploy/failover/` is absent.                                                                                                                                                                                                                                                                                                              | Production owned-object platform adapter absent: idempotent IPVS/address/interface/route attachment, interrupted apply, unsupported kernel/malicious input rejection and crash recovery. Lab tooling is not this adapter.                                                                                                                                                                                        |
| FG-05 / T4           | Cooperative deployment lock exists in `deploy/kwrollout/lock.go`; no dedicated frontend HA implementation.                                                                                                                                                                                                                                                                                   | Independent redundant LB owner election **and dataplane fencing** absent. Need two nodes, paused/stale owner, partitions/control-plane outage, withdrawal/takeover timings, no duplicate advertisement. A ConfigMap CAS is not a dataplane fence.                                                                                                                                                                |
| FG-06 / T2,T4        | Desired IP/member reservations and read-only observation projection exist. `status.go` exposes no observation writer; model intentionally exposes no delete.                                                                                                                                                                                                                                 | Authenticated owner/generation-fenced observations, drain/re-admission, tombstones, withdrawal acknowledgement and safe reservation reuse absent. Do not enable delete before old advertisement is proven gone; empty pools must not spill across groups.                                                                                                                                                        |
| FG-07 / T3           | Internal domain persistence only. No failover-group paths in `mgmt/api/openapi.yaml` or production API wiring.                                                                                                                                                                                                                                                                               | Admin-authorized audited create/list/detail/update and lifecycle-safe delete, generated Go/TS contracts, honest owner/eligibility/status, real DB authorization/CAS/collision/withdrawal tests absent. CRUD cannot grant frontend ownership.                                                                                                                                                                     |
| FG-08 / T3           | Existing policy engine-group UI is separate. No dedicated failover frontend GUI in `web/src`.                                                                                                                                                                                                                                                                                                | List/detail/edit/delete flows, navigation/help and real API-backed browser tests for permissions/conflicts/pending withdrawal/unknown/stale status absent.                                                                                                                                                                                                                                                       |
| CP-01                | `deploy/kwrollout/bootstrap-convergence.{sh,jq}`, `bootstrap_convergence_test.go`, `deploy/kw/bootstrap.sh`: bounded fixed-target ACK convergence, pinned identities, cancellation and negative paths.                                                                                                                                                                                       | No new unattended immutable-SHA deployment evidence. Permanently stale/rejected target must fail at deadline and retain lock. Never substitute republish or DNS retries for convergence.                                                                                                                                                                                                                         |
| CP-02                | `01301_engine_connection_session.sql`, `mgmt/internal/control/{ownership,server,hub,logs,dnstls}.go`, `ownership_test.go`; `mgmt/cmd/nexora-mgmt/main.go` wires `NotifyTx` and `ApplyFenced`. NOTIFY scheduling, UPDATE mutation/audit/publication, log persistence and TLS result/cache changes are fenced. Wave2 records integrated Linux race and real control-product passes.            | All management replicas must run fencing-aware binaries; mixed versions unsafe. Already committed scheduler jobs and queued outbound material are not retroactively revoked; cross-process caches remain ephemeral. This is database mutation ownership, not universal send revocation or dataplane fencing. Final combined and all-replica acceptance remain open; do not reimplement completed callback fixes. |
| FG-09 / T5           | `deploy/helm/nexora/templates/{_failover.tpl,engine-workloads.yaml}`, `deploy/kw/values-pairs.yaml`, `deploy/kwrollout/`, deploy/preflight scripts implement guarded serial paired rollout with OnDelete and `--wait=legacy`.                                                                                                                                                                | Dedicated frontend chart/image/staging and new topology probes absent. Existing paired workflow must be extended only after adapter/HA/lifecycle/API gates; preserve DNS/source/identity/capacity/management checks.                                                                                                                                                                                             |
| FG-10 / T5           | Existing legacy→paired migration and retained non-expiring CAS lock are real code with interruption tests and historical recovered rollout.                                                                                                                                                                                                                                                  | Dedicated frontend advertisement cutover/rollback absent. Stage new attachments; withdraw old owner before activate; `.136` A/C then `.139` B/D, all identities/trust preserved. Commit/build/deploy and lock recovery belong to parent, not this audit.                                                                                                                                                         |
| FG-11 / T6           | Strict product tests, five-transport lab and single-member opt-in harness exist, with historical RED and finite successful samples.                                                                                                                                                                                                                                                          | Full actual-engine two-client/two-group matrix, each A/B/C/D and each LB, planned drain, ownership/node/network partition and empty pools unperformed and partly unwritten. Freeze thresholds/windows before destructive runs; separate existing-session losses from new flows.                                                                                                                                  |
| FG-12 / T6           | `scripts/kw-acceptance.sh`, operations docs and historical acceptance reports exist.                                                                                                                                                                                                                                                                                                         | Full final supported Linux Go/Rust/web/operator/chart/product suite, exact immutable deployed SHA, strict acceptance and current recovery/drain/frontend runbooks required. No task/sprint closure here.                                                                                                                                                                                                         |

Spec `nexora-failover-groups.md` acceptance mapping is exhaustive: AC1 is partial
FG-02/06/07; AC2 is partial persistence with FG-07/08 absent; AC3 and AC4 are
bounded lab evidence only under FG-01/11; AC5 retains engine-a RED and unperformed
remaining cases; AC6 requires absent FG-05 and FG-11; AC7 has paired guards but
needs independent frontend upgrade/fencing; AC8 requires FG-09/10; AC9 requires
FG-03/12. None is promoted to dedicated-frontend acceptance here.

Historical `nexora-kw-failover-pairs.md` AC1–4 have chart/runtime behavior and
negative tests (`deploy/deploytest/helm_failover_test.go`, rollout assembly,
fleet/endpoints/run/lock tests). AC5 and AC7 have recorded four-engine/strict
`809cf3a` evidence, not fresh inventory. AC6 is explicitly incomplete/RED for
abrupt failure and unattended completion. Its Service/Local/announcer placement
mechanism is superseded by the dedicated frontend spec; pair membership, tuples,
identity, serial safety and evidence requirements survive unchanged.

### Cross-host failure provenance

Wave2 records setup RED `1149b19e` (VXLAN listener collision/tcpdump user), UDP RED
`3c1ab31a` (checksum offload metadata lost), corrected DR PASS `5b307575`, and SNAT
setup RED `2a179c8b` (missing iptables). Final corrections use isolated relay
namespaces, endpoint-only offload changes and up-front prerequisites. No raw
failure is rewritten as success; guard-only later changes are not another live
run. Privilege/kernel/tool prerequisites are external; implementing engine
attachment and additional assertions is ordinary unperformed engineering.

## Excluded and planning-only tracks

`nexora-split-dns.md` and its plan are draft/planning-only here. S-1–S-13 cover
schema/API, longest-prefix selection, ACL isolation, answers, cache isolation,
GUI, telemetry, authenticated per-view transfers/NOTIFY, UPDATE, independent
DNSSEC, mixed-version activation, bounds and primary/secondary lifecycle. All 15
acceptance bullets (including independent signed-secondary interoperability,
kind conversion, two-source kw proof and benchmarks) remain future work. None is
closed by ordinary zones, TSIG, DNSSEC or per-client policy already existing.
Limits require review; real deployment CIDRs/records are inputs, not permission
to guess. No split-DNS implementation is authorized in this wave.

`roadmap.md` pending #2/#6/#7/#8 reference-performance proof needs controlled x86
hardware; this is distinct from writing/running available laboratory tests.
#34 DHCP, #38 deb/rpm and #39 tarballs remain pending decisions, not silently
included requirements. #4 and #40 are historically excluded. The roadmap's old
unqualified `helm rollback` recipe is not authorization to bypass current retained
CAS lock/quiescence/serial rollout safeguards. CNI migration, extra client IPs,
resolver-mode changes and trust regeneration remain excluded.

## M1–M5: all v1 acceptance criteria

All 82 M1–M5 todos are historically closed (23/13/15/16/15). Spec `nexora-v1.md`
being “complete” denotes the specification's state; its unchecked acceptance
boxes do not mean the implemented product is absent. Each named acceptance test
below exists. I is present and T is historical/source-inspected except where a
coverage gap is called out. No row claims a new f9c1653 run, D or A.

| Milestone / spec criterion                                  | Implementation and behavioral test                                                                                                            | Acceptance qualification                                                                                                                                                                            |
| ----------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| M1 S-1 forwarding/cache TTL                                 | `engine/src/cache.rs`, `engine/src/upstream/`; `e2e/forward_test.go:TestForwardCacheTTL`                                                      | Checks upstream count and decreasing TTL; implemented.                                                                                                                                              |
| M1 S-1 blackhole failover ≤1s                               | Same source; `TestUpstreamFailover`                                                                                                           | 50 unique queries, secondary use, success/deadline assertions exist.                                                                                                                                |
| M1 S-1 1,000 coalesced misses                               | `engine/src/inflight.rs`; `TestDedupAllWaitersAnswered`                                                                                       | Synchronized callers and exactly one upstream request checked.                                                                                                                                      |
| M1 S-4 subscriptions/allow override/failed refresh          | `mgmt/internal/blocklist/{fetcher,parse}.go`, engine filter; `e2e/blocklist_test.go:TestBlocklistSubscription`                                | Category index supersedes original FilterSet; refresh retention implemented.                                                                                                                        |
| M1 S-6 EDNS/truncation/TCP/cookies                          | `engine/src/{wire,edns}.rs`, server; `e2e/edns_test.go:TestEDNSTruncationTCP`                                                                 | Test/source exist; preserve no truncated-cache acceptance.                                                                                                                                          |
| M1 S-6 one-hour parser fuzz                                 | `engine/fuzz/`, `.github/workflows/fuzz.yml`                                                                                                  | Historical M1 61-second smoke is not one hour. M7 T22 records successful dispatched fuzz and older scheduled run; qualifying current-head scheduled evidence remains unperformed.                   |
| M1 S-14 invalid config retains last good                    | `engine/src/{runtime,snapshot,control}.rs`; `e2e/control_test.go:TestInvalidSnapshotRejected`                                                 | API reason, metric, persisted snapshot, DNS continuity/recovery checked.                                                                                                                            |
| M1 S-14 management HA ≤10s                                  | Management control/persistence; `e2e/control_test.go:TestMgmtStatelessHA`                                                                     | **Gap:** lines 122–126 add one second to deadline and reject only >11s. Wave2 real test PASS does not prove exact 10s contract.                                                                     |
| M1 S-20 RBAC/audit/OIDC/local fallback                      | `mgmt/internal/auth/`, API, `web/src/auth/`; `e2e/auth_test.go:TestAuthRBACAuditOIDC`                                                         | API and Playwright evidence exists historically; exact-release mapping outstanding.                                                                                                                 |
| M1 S-12/S-22 query reaches GUI ≤10s, both backends          | `mgmt/internal/querylog/`, `engine/src/telemetry/querylog.rs`, `web/src/pages/QueryLogPage.tsx`; `e2e/gui_test.go:TestQueryLogBackends`       | Wave2 real integrated four-backend product/browser PASS; not full release acceptance.                                                                                                               |
| M1 S-21 collector outage ≤5% QPS loss/drop count            | Telemetry; `e2e/observability_test.go:TestOTelSinkDownNoBackpressure`                                                                         | Proves collector receiving first; compares medians of three up/down rounds and checks drops. M5 T6 records an earlier QPS failure before later pass; not all attempts green.                        |
| M1 S-13/S-21 metrics and SERVFAIL trace                     | Engine telemetry and management stats; `TestObservabilityMetricsTraces`                                                                       | Real Collector/Jaeger run prerequisites; test existence is not current sink proof.                                                                                                                  |
| M1 S-15 every shipped API resource in GUI                   | `web/src/pages/`, `web/e2e/screens/`; `TestGUICoverage`                                                                                       | Screen behaviors and operation coverage exist; operation counts alone do not prove UX.                                                                                                              |
| M1 S-16 relative ≤5%, absolute ≥1M QPS/p99 <500µs           | `bench/cmd/perfgate/`, `.github/workflows/perf-gate.yml`                                                                                      | **Unaccepted:** reference job conditional on `vars.NEXORA_REFERENCE_HOST`; no provisioned reference acceptance. A/A calibration open; historical identical-binary 7.53% failure retained.           |
| M2 S-6 DoT/DoH GET+POST/DoQ parity                          | `engine/src/server/{dot,doh,doq}.rs`; `e2e/encrypted_transports_test.go:TestEncryptedTransports`                                              | Implemented; historical `sha-360b8cf` kw evidence is not this baseline deployment.                                                                                                                  |
| M2 S-10 client CIDR policy/cache isolation                  | Engine filter/cache and policy API; `e2e/per_client_policy_test.go:TestPerClientPolicy`, `engine/tests/policy_pipeline.rs`                    | Historical forced partition-0 mutation failed; meaningful isolation tests exist.                                                                                                                    |
| M2 S-11 safe search/custom rewrite                          | `engine/src/server/rewrite.rs`, API policies/rewrites; `e2e/safe_search_rewrites_test.go:TestSafeSearchRewrites`                              | Implemented with historical suite/live smoke.                                                                                                                                                       |
| M3 S-2 root recursion/spoof rejection                       | `engine/src/recursor/{iterate,transport,infra,rrcache,roothints}.rs`; `e2e/recursion_test.go:TestRecursionRootHints,TestSpoofedReplyRejected` | Wrong ID/port/question and nonvacuity assertions exist.                                                                                                                                             |
| M3 S-7 secure AD/bogus SERVFAIL/insecure AD0                | Engine recursor DNSSEC and management dnssecconf; `e2e/dnssec_test.go:TestDNSSECValidation`                                                   | Forwarding and recursive validation historically exercised.                                                                                                                                         |
| M3 S-9 RPZ file/AXFR policy                                 | `engine/src/recursor/rpz/`, `mgmt/internal/rpz/`; `e2e/rpz_test.go:TestRPZPolicy`                                                             | Last-good/TSIG/snapshot paths implemented; M8 ZONEMD separate.                                                                                                                                      |
| M4 S-3 AA propagation ≤5s                                   | `mgmt/internal/zone/`, `engine/src/authoritative/`; `e2e/authoritative_test.go:TestAuthoritativeZonePropagation`                              | Shared strict five-second create/edit deadline across engines.                                                                                                                                      |
| M4 S-17 NOTIFY/IXFR to BIND                                 | Authoritative `xfr.rs`, `notify_out.rs`; `e2e/xfr_test.go:TestAXFRIXFROut`                                                                    | Real BIND interoperability test exists.                                                                                                                                                             |
| M4 S-18 secondary NOTIFY/authenticated UPDATE               | Authoritative notify/update/state; `mgmt/internal/{xfrin,dynupdate}/`; `e2e/secondary_update_test.go:TestSecondaryAndDynamicUpdate`           | T11 intermediate NOTIMP/not-built wording superseded by later implementation/evidence. Wave2 integrated real product PASS.                                                                          |
| M4 S-8 signing and rollover validates                       | `mgmt/internal/dnssec/`, authoritative DNSSEC/NSEC3; `e2e/dnssec_signing_test.go:TestDNSSECSigningRollover`                                   | Real delv and negative-answer/signature mutation evidence; current supported run needed.                                                                                                            |
| M4 S-23/S-8 protected key backends/no engine disk keys      | `mgmt/internal/secrets/`, key delivery; `TestKeyStorageBackends`, `harness.InitSoftHSM`                                                       | KEK/PKCS11 signing/delv tests exist; real SoftHSM required, no live trust inspection authorized.                                                                                                    |
| M4 S-19 zone-file round trip                                | `mgmt/internal/zonefile/`, zone export; `e2e/zonefile_test.go:TestZoneFileRoundTrip`                                                          | Uses real `ldns-compare-zones`, not only string equality.                                                                                                                                           |
| M5 S-5 three separate hosts/config ACK/management partition | `mgmt/internal/{rollout,fleet}/`, engine control/renewal; `e2e/fleet_test.go:TestFleetRolloutAndPartition`                                    | **Gap:** three local managed processes with distinct state, not separate hosts. Checks 10s DNS survival/reconnect; kw full-product topology/cert test does not fill separate-host outage criterion. |

M5 plan coverage beyond S-5: T1–3 architecture, `00500_fleet.sql` and
`mgmt/internal/control/contract_m5_test.go`; T4–6 rollout state/canary/DB ownership,
`mgmt/internal/rollout/`, `snapshot/publish_m5_test.go`, `control/fleet_push_test.go`;
T7–9 fleet API/tokens/revoke/rotate/renew, `mgmt/internal/api/fleet_*.go`,
`control/lifecycle_test.go`, `engine/tests/control_renewal.rs`; T10 product
`e2e/{fleet_api,fleet_canary,fleet_cert,cli_fleet}_test.go`; T11 fleet pages/browser
specs; T12–15 images/Compose/Helm/kw/operations all exist. T12's historical “Compose
not executed” is superseded by M7 T20's actual novanas run on `sha-1cc283d`, not by
current-image evidence. M5 T14's `sha-93dcd04` eight-engine deployment and corrected
metrics acceptance remain historical; its initial summed-duplicate-metrics failure
must not disappear. All five implementation plans have source coverage; no request
to recreate these completed foundations follows from unchecked spec boxes.

## Filter categories: all acceptance criteria and plan coverage

`nexora-filter-categories.md` has 20 plan tasks and no standalone category task
series; M6 T9 is later UX. Intermediate unchecked/still-to-do plan text is partly
superseded by “As built” paragraphs and source. No baseline deployment is certified.

| Criterion                                                  | I / T paths                                                                                            | Remaining or qualification                                                                                                                                                                                                                                                             |
| ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| S-1/2/3 exact FilterSet semantics                          | `engine/src/filter/{index,oracle,names}.rs`; `filter_index_matches_filterset_semantics`                | Property oracle exists.                                                                                                                                                                                                                                                                |
| S-1/2 memory/build/clean/cold/repeated budgets             | `engine/examples/filter_bench.rs`, `engine/tests/filter_index_budget.rs`, CI release test              | Debug skips timing; release test uses minimum of three build measurements. Historical 5.136M corpus: 118.2MB, 1.72s, 274ns cold, 103ns clean, 32.9ns repeated; **124ns whole Zipf average**. Uncommitted tree on `92a8ece`, not f9c1653 proof.                                         |
| S-1 <10% memory across groups                              | `TestFilterIndexSharedAcrossGroups` in budget test                                                     | Positive baseline and <1.10 ratio assertion present.                                                                                                                                                                                                                                   |
| S-2 allocation-free cache hits                             | `engine/tests/hot_path_alloc.rs`                                                                       | Actual allocation test; supported Linux run needed.                                                                                                                                                                                                                                    |
| S-3 unchanged-list reuse                                   | `engine/tests/snapshot_apply.rs:unchanged_lists_reuse_index`                                           | Runtime identity/reuse tested.                                                                                                                                                                                                                                                         |
| S-4 global/per-group blocking UDP/TCP/DoH                  | `mgmt/internal/catalog/`, category snapshots; `e2e/filter_categories_test.go:TestFilterCategories`     | Real fixture/managed-engine test exists.                                                                                                                                                                                                                                               |
| S-4/10 source/category disable, license, read-only catalog | `mgmt/internal/api/{filtercategories,policyvalidate}.go`; `TestFilterCategoryToggles`                  | License/audit/read-only guards present.                                                                                                                                                                                                                                                |
| S-5 archive member isolation                               | `mgmt/internal/blocklist/archive.go` and tests; `e2e/filter_attribution_test.go`                       | Missing member/leak assertions exist.                                                                                                                                                                                                                                                  |
| S-6 source/category attribution/backend filtering          | Telemetry querylog/OTLP, management adapters; `TestQueryLogCategoryAttribution`                        | T14 intermediate backend-mapping gap stale; wave2 integration exercises adapters.                                                                                                                                                                                                      |
| S-7 catalog/source/license/policy/query GUI                | Category/policy/query/engine detail pages, `web/src/components/categories.tsx`; screen specs 22 and 23 | Query-log/index checks moved to `23-querylog-category.spec.ts`; not missing because spec names 22 only.                                                                                                                                                                                |
| S-8 entries/bytes/build/category metrics                   | Engine telemetry, `mgmt/internal/stats/collector.go`, observability tests                              | Source/coverage exists; current fleet measurements unperformed.                                                                                                                                                                                                                        |
| S-9 relative perf gate and kw budgets                      | `bench/cmd/perfgate/{filter,filter_test}.go`, perf workflow, `e2e/kw_filter_categories_test.go`        | Historical `sha-ce26a9e-fix2` includes uncommitted fixes, eight engines ~117.78MB/1.71–1.81s/no OOM. **Gap:** budget loop skips disconnected engines, keys report by node name, never requires expected/nonzero cardinality. Empty/missing/duplicate fleet can evade per-engine proof. |

Plan T1–20 cover architecture, packed storage/prefetch, synthetic corpus, proto
600+ fields, list collection/cgroup caps, runtime reuse, telemetry/calibration,
fixed catalog/sync/`00503_filter_categories.sql`, snapshots, archive fetch, guarded
API, attribution, GUI, product tests, perf and kw integration. All have corresponding
source; remaining work is stronger fleet-budget validation and release/performance
proof, not rebuilding categories.

## M6: operator UX S1–S16

All named Go acceptance definitions exist. 34 todos: 31 open, three done variants
(T20/T23/T28); done-but-uncommitted language is stale. I exists for all rows; T is
source/historical except the scoped wave2 passes noted above. D/A at f9c1653 absent.

| Stories                                          | Source / test evidence                                                                                          | Remaining acceptance                                                                                              |
| ------------------------------------------------ | --------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------- |
| S1 partial names, S2 repeated multivalue filters | Querylog OpenSearch escaping test, API `TestSearchQueryLogRepeatedParameters`, `e2e/gui_test.go`, screens 10/25 | Wave2 four-backend product/browser PASS; final OR-within/AND-across and URL-state release proof.                  |
| S3 decision/source reasons                       | `e2e/filter_attribution_test.go`, `mgmt/internal/api/querylog_resolve.go`, screen 26                            | T16 “rule mapping missing” stale. Old browser timing failure not current failure.                                 |
| S4 dashboard                                     | `mgmt/internal/stats/dashboard_test.go`, API `dashboard_m6_test.go`, screen 27                                  | Full windows/alerts/unavailable/browser proof; wave2 dashboard-top pass narrower.                                 |
| S5/S8 navigation/grouping, S7 headers            | Screens 28/35, settings/filter pages                                                                            | Current desktop/mobile deployed UX mapping.                                                                       |
| S6 help                                          | `web/scripts/check-help.mjs`, `web/src/help/`, screen 29, `deploy/deploytest/help_test.go`                      | Current lint/GUI/help links.                                                                                      |
| S9 collapsible categories                        | `FilterCategoriesPage.tsx`, screen 22                                                                           | Persisted expansion/deep links/OISD notice current acceptance.                                                    |
| S10 ACL split, S11 compatible migration          | `e2e/access_split_test.go`, API/store access-split tests, screen 30, engine pipeline/old-snapshot tests         | Four-transport source/RA/refusal/log and migration proof on combined engine.                                      |
| S12 parallel upstream                            | `e2e/upstream_parallel_test.go`, screen 31, engine parallel tests                                               | 2,000-query kw-hardware p95/p99 comparison is separate required evidence.                                         |
| S13 modal/metrics, S14 bounded logs              | Fleet metrics tests, screen 32, `control/logs_test.go`, `e2e/engine_logs_test.go`, engine ring tests            | Wave2 real-engine logs/control PASS; full tabs/deep links/narrow viewport and deployed mapping remain.            |
| S15 account/password                             | API account tests, `AccountPage.tsx`, screen 33                                                                 | RBAC/password/audit/browser release proof.                                                                        |
| S16 version/reload                               | API version test, deploy buildinfo test, screen 34, kw smoke                                                    | Immutable image/API/footer/health correspondence; reload behavior.                                                |
| All stories final gate                           | `TestGUICoverage`, deploy/acceptance scripts, operations                                                        | T34 empty evidence; full GUI, guarded zero-loss deployment, strict acceptance/manual UX unperformed for baseline. |

## M7: all 22 hardening stories

23 todos: 22 open; T9 done retains a real stress failure. M7 code is integrated.
Open issue states must not prompt duplicate implementation. D/A at baseline unproved.

| Story                                             | Existing source / behavioral coverage                                                                 | Remaining or contrary evidence                                                                                                                                                                                                    |
| ------------------------------------------------- | ----------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| S1 CI/images/fuzz/perf                            | `.github/workflows/{ci,images,fuzz,perf-gate}.yml`, T22                                               | Historical CI at `15978fd`, dispatched fuzz success, older scheduled success; amd64 images/perf queued. Old ARC `10.43.0.1:443` no-route is not a verified current outage. Need exact-head artifacts/scheduled fuzz/perf verdict. |
| S2 Compose                                        | `scripts/compose-verify.sh`, deploy compose test, operations/T20                                      | Actual 12-step novanas run/cleanup `sha-1cc283d`; final-image run remains.                                                                                                                                                        |
| S3 DNSSEC wildcard                                | `e2e/fixtures/authhier/authhier_test.go`, `e2e/dnssec_test.go`                                        | Expanded signature/next closer/NODATA/bogus denial tests exist.                                                                                                                                                                   |
| S4 collector restart                              | `e2e/harness/harness_test.go`                                                                         | Port-holder delayed restart behavior exists.                                                                                                                                                                                      |
| S5 merged ACL ranges                              | `engine/src/acl.rs`                                                                                   | Randomized equivalence/range tests exist.                                                                                                                                                                                         |
| S6 transfers off worker                           | `engine/src/authoritative/xfr_tests.rs`                                                               | `transfer_is_built_off_the_worker` exists.                                                                                                                                                                                        |
| S7 renewal fallback, S8 unstorable identity       | `engine/tests/{control_renewal,control_enroll}.rs`                                                    | Failure/fallback/no reenrollment tests exist.                                                                                                                                                                                     |
| S9 last trusted-key revocation                    | Engine recursor DNSSEC and telemetry tests                                                            | T7 records metric/engine PASS and GUI alert implementation but **no lost-trust Playwright test**.                                                                                                                                 |
| S10 >1024 denial cache                            | Recursor NSEC cache tests                                                                             | 5,000-denial regression exists.                                                                                                                                                                                                   |
| S11 atomic infra counters                         | `engine/src/recursor/infra.rs`                                                                        | Eight-thread/800,000-count regression exists.                                                                                                                                                                                     |
| S12 recursion memory cap                          | Recursor `rrcache.rs`, `engine/tests/snapshot_m3.rs`, API resolution tests, screen 17, proto field800 | Setting already exists; M11 comment claiming missing M7 setting is stale.                                                                                                                                                         |
| S13 incremental RPZ IXFR                          | Authoritative transfer tests                                                                          | Owner-local key-build regression exists.                                                                                                                                                                                          |
| S14 pooled TCP/DoQ                                | `engine/tests/hot_path_alloc.rs`                                                                      | Allocation assertions need supported Linux verification.                                                                                                                                                                          |
| S15 BADVERS                                       | `engine/tests/server_pipeline.rs`                                                                     | EDNS-version regression exists.                                                                                                                                                                                                   |
| S16 version-correct labels, S17 concurrent export | `engine/tests/telemetry_export.rs`                                                                    | T9 retains **35/36 stress passes, one dropped 1,000-record batch at capacity**. Diagnose eviction vs timing without retries masking it or lowering assertions.                                                                    |
| S18 streamed GetBlob                              | `mgmt/internal/control/control_test.go`                                                               | Heap-bound/chunk tests; wave2 control package race PASS is scoped.                                                                                                                                                                |
| S19 stable OpenSearch paging                      | Querylog OpenSearch tests, `e2e/querylog_paging_test.go`                                              | Same-millisecond ordering regression exists.                                                                                                                                                                                      |
| S20 PKCS11 session recovery                       | `mgmt/internal/secrets/pkcs11_test.go`                                                                | Invalid session/PIN unavailable/no-deadlock test needs actual SoftHSM.                                                                                                                                                            |
| S21 streamed zone export                          | API zonefile/zone export tests, `e2e/zonefile_test.go`                                                | Streaming/stability/ldns behavior covered.                                                                                                                                                                                        |
| S22 incremental record edits                      | Zone `edit_test.go`, `build.go`, signed-zone tests                                                    | Small-select/random edit/invariants present; signed-zone debt exception explicit.                                                                                                                                                 |
| Final gate                                        | `deploy/deploytest/debt_test.go`, T23                                                                 | Full combined suite, key storage, GUI, CI/images, Compose, strict release evidence and governance remain.                                                                                                                         |

## M9: platform S1–S14

All 12 task records are open, despite implementation and historical test results.
T1/T10 awaiting-lead-commit wording is stale. Wave2 full real operator envtest/race
PASS covers APIs, controllers and rendering; it is not live CNPG/release acceptance.

| Stories                                                 | Implementation / test paths                                                                  | D / A boundary                                                                                                                                                                 |
| ------------------------------------------------------- | -------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| S1 packaging/RBAC/image/CRDs                            | `deploy/deploytest/{operator_chart,workflow,buildinfo}_test.go`, installation `rbac_test.go` | Present; exact-image workflow artifacts still separate.                                                                                                                        |
| S2 validation/rendered values                           | `operator/api/v1alpha1/validation_test.go`, `operator/internal/render/values_test.go`        | Includes baseline ClickHouse/Loki fields/generated CRDs, not missing.                                                                                                          |
| S3 render/apply/prune/retain                            | Render/installation controller tests                                                         | Parity/ownership/foreign namespace/render failure/database-key retention covered.                                                                                              |
| S4 no-overwrite key creation                            | `operator/internal/keys/keys_test.go`, installation tests                                    | Tests exist; no audit-time real secret operations.                                                                                                                             |
| S5 guarded isolated installation, S13 full operator e2e | `operator/test/kw/kw_operator_test.go`, `scripts/kw-operator-e2e.sh`                         | T10 historical all-subtests PASS at `dev-m9-8483b44`; fresh socket and dnsperf criteria retain meaning. Not baseline live pass.                                                |
| S6 status/image tags                                    | Installation controller tests                                                                | Health/default immutable-tag guards present.                                                                                                                                   |
| S7 group lifecycle, S8 token/delete lifecycle           | Enginegroup controller tests                                                                 | Partial-field/conflict/duplicate/default-group/retain/expiry/grace/revoke-before-replace/cap tests present.                                                                    |
| S9 bootstrap/system users                               | Auth/config/API/store tests, `e2e/bootstrap_token_test.go`, screen 71, Helm bootstrap test   | Source implemented despite open tasks.                                                                                                                                         |
| S10 CNPG HA, S11 backup/restore                         | `deploy/deploytest/helm_cnpg_test.go`, kw `cnpg-failover`/`cnpg-backup-restore`              | Unit/envtest cannot substitute for actual CNPG/MinIO scenario.                                                                                                                 |
| S12 operations                                          | `platform_docs_test.go`, architecture/operations                                             | Documentation gates exist.                                                                                                                                                     |
| S14 production unchanged/release                        | T12, guarded deploy/acceptance scripts                                                       | Final e2e/operator/current SHA/zero-loss/strict acceptance and evidence of **no production NexoraInstallation CR**. Do not adopt production operator to finish this criterion. |

## M10: query-log backends S1–S12

All 12 tasks remain open; T1–11 implementation is in baseline. `d0009ce` branch
integration advice is superseded; leave preserved unfinished merge tree alone.

| Stories                    | Source / test evidence                                                                                                             | Acceptance boundary                                                                                                                                           |
| -------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| S1–3 ClickHouse            | `deploy/clickhouse/querylog.sql`, querylog `clickhouse.go`, tests                                                                  | Parameterized search/cursors/top/errors/schema covered; real 26.8.4.11 collector-ingestion conformance recorded PASS.                                         |
| S4–6 Loki                  | Querylog `loki.go`, tests                                                                                                          | Quoting/same-ns tie/two-stage top/partition fallback/concurrency/errors covered; real 3.6.7 conformance recorded PASS.                                        |
| S7 equivalence             | `mgmt/internal/querylog/conformance_test.go`, `querylog/e2e/*_conformance_test.go`                                                 | Fixed expectations and intentionally divergent backend test; real external-backend conformance 49.743s PASS.                                                  |
| S8 config/serve/product    | Config tests, `mgmt/cmd/nexora-mgmt/querylog_test.go`, GUI/filter-attribution product tests                                        | Integrated four-backend real product/browser 71.025s PASS; not final full suite.                                                                              |
| S9 Helm                    | `deploy/deploytest/helm_querylog_test.go`                                                                                          | Secret-file/URL/schema/default adapter checks and wave2 chart pass.                                                                                           |
| S10 kw fan-out/equivalence | `deploy/kw/{clickhouse,otelcol}.yaml`, `kw_querylog_test.go`, querylog e2e `kw_querylog_backends_test.go`, guarded support scripts | No wave2 live M10 install. Need retained-state credential guards, 20-record equality/completed-window top10, zero-loss guarded release and strict acceptance. |
| S11 pinned toolbox/harness | Toolbox definition, `scripts/dev-selftest.sh`, `e2e/harness/backends_test.go`                                                      | Private `/work/tools/nexora-m10` staging enabled real conformance; toolbox image **not rebuilt/deployed**. Selftest acceptance distinct.                      |
| S12 docs/help              | `docs_querylog_test.go`, `web/src/help/topics/observability.md`, operations/architecture                                           | T11 unchecked help edit is stale; content exists.                                                                                                             |
| Final T12                  | Full-suite/release workflow                                                                                                        | Unperformed final release, no deployment/closure claim.                                                                                                       |

## M11: AI S1–S15

32 todos: 21 open, 11 other nonclosed variants. T22's dependency on T18–21 is
stale; T24/T26 and other done/awaiting-commit wording does not describe baseline
absence. Every named Go acceptance definition exists. Suggest-only/RBAC/audit
constraints remain required, not optional after a historical pass.

| Story                                                   | Implementation / test evidence                                                                                     | Acceptance boundary                                                                                                                                                                                                                   |
| ------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| S1 off/config/privacy/generation/limits/scheduler/tasks | Config AI tests; `mgmt/internal/ai/{privacy,generate,limits,scheduler,tasks}_test.go`, `e2e/ai_foundation_test.go` | Historical full e2e and wave2 management PASS; exact-release smoke still separate.                                                                                                                                                    |
| S2 validated proposal/replay                            | `ai/proposal/{validate,injection}_test.go`, `e2e/ai_suggest_only_test.go`                                          | Malicious output/RBAC/audit/stale revision/all-agent no-write tests exist.                                                                                                                                                            |
| S3 natural-language search                              | `ai/qlsearch/qlsearch_test.go`, `e2e/ai_querylog_test.go`                                                          | Validation/default range/real-engine task coverage exists.                                                                                                                                                                            |
| S4 anomaly                                              | `ai/anomaly/{detect,agent}_test.go`, `e2e/ai_anomaly_test.go`                                                      | Threshold/change/model failure/resolve/product tests.                                                                                                                                                                                 |
| S5 insights                                             | `ai/insight/{detect,agent}_test.go`                                                                                | Deterministic scoring/correlation validation.                                                                                                                                                                                         |
| S6 filter recommendations                               | `ai/filterrec/agent_test.go`                                                                                       | Computed impact/dedup/dismiss cooldown.                                                                                                                                                                                               |
| S7 assistant                                            | `ai/assistant/assistant_test.go`, `e2e/ai_assistant_test.go`                                                       | Proposal supersession/RBAC/user isolation.                                                                                                                                                                                            |
| S8 upstream prediction                                  | `ai/upstreampred/{trend,agent}_test.go`                                                                            | Slope/step/periodicity/sufficiency/unsafe-disable tests.                                                                                                                                                                              |
| S9 rollout risk                                         | `ai/rolloutrisk/{features,agent}_test.go`, `e2e/ai_rollout_risk_test.go`                                           | History/consistent scores/skips/nonblocking rollouts.                                                                                                                                                                                 |
| S10 threat/classification                               | `ai/threat/{check,classify}_test.go`, `e2e/ai_threat_test.go`                                                      | Bounds/cache/sampling/no model on read path.                                                                                                                                                                                          |
| S11 capacity                                            | `ai/capacity/{project,agent}_test.go`, `agent.go:94`                                                               | **Absent recursor-cache sampling.** Engine recursion-cache observed-byte telemetry needed; resolver `Stats.cache_bytes` is not that measurement. M7 limit setting exists. Seeded screen 55 forecast is not end-to-end sampling proof. |
| S12 RPZ suggestions                                     | `ai/proposal/{validate,rpz}_test.go`, `ai/rpzsuggest/`, `e2e/ai_rpz_test.go`                                       | Exclusions/valid zone/recreate-retain rules covered.                                                                                                                                                                                  |
| S13 MCP                                                 | `e2e/mcp_test.go`, MCP server/stdio                                                                                | SDK/RBAC/audit/origin/read-only/AI-off cases exist.                                                                                                                                                                                   |
| S14 GUI                                                 | `web/src/pages/ai/`, screens 50–60                                                                                 | All feature screens present; current full operation/behavior/browser release mapping outstanding.                                                                                                                                     |
| S15 deployment/docs                                     | `deploy/deploytest/{ai,ai_docs}_test.go`, Helm/kw values, `e2e/kw_ai_test.go`                                      | T32 creation checkbox stale. Secret/MCP optional wiring implemented. Private fastllm/model/structured-mode/input+reasoning tokens/no invalid output exact-release acceptance remains.                                                 |
| Final T32                                               | Scripts/docs and recorded 1361.894s /78 PASS historical suite                                                      | Full combined baseline, guarded zero-loss deploy, strict AI acceptance/manual UX at current origin required. Historical kw.local URLs superseded by documented kw.watteel.lab; never rewrite past results as later runs.              |

## M8: preserved implementation, every acceptance family and T1–T34

Preserved tree `/Users/pascal/Development/nexora-m8` remains at `3fb09b9`, with 22
commits outside baseline and merge base `15978fd5c6b4c7c2d782ab32e36f607a1fb40106`.
M8 implementation is **not integrated into f9c1653**. Paths in this section refer
to that preserved tree unless explicitly called baseline. All 34 M8 todos there
remain open. T1–20/T22–23 contain historical committed implementation/test evidence;
T21/T24–34 evidence sections are empty, but some contain dirty test/implementation
work. No M8 D/A is established. Owner-coordinated integration must preserve dirty
work; neither main-only absence nor open status justifies rebuilding it.

| Spec requirement                     | Preserved I / T                                                                                                                                                                                            | Exact remaining acceptance                                                                                                                                                                                                          |
| ------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| S-13 contract/key isolation          | `mgmt/internal/control/contract_m8_test.go`, `TestContractM8FieldsRoundTrip`, `TestSnapshotCannotReachOdohKeys`                                                                                            | Combined regeneration/round trips preserving existing field numbers and baseline operations.                                                                                                                                        |
| S-13 compatible migration/defaults   | `mgmt/migrations/01300_zonemd.sql`, `mgmt/migrations/01301_catalog_zones.sql`, `mgmt/migrations/01302_odoh.sql`, `mgmt/migrations/01303_engine_group_mdns.sql`; `mgmt/internal/store/m8_migration_test.go` | Collides with baseline 01300/01301. Test starts `UpTo(899)`, not current lineage, and **does not build/assert required post-migration snapshot with nil mDNS/ODoH and RPZ OFF**. Spec's 00900–00903 numbering also stale.           |
| S-4/5 digest correctness             | `mgmt/internal/zonemd/`, `engine/src/zonemd.rs`, `e2e/testdata/rfc8976/`                                                                                                                                   | Historical vector/mutation tests; integrated rerun.                                                                                                                                                                                 |
| S-4 primary generation/signing/IXFR  | Zone/DNSSEC services; `e2e/zonemd_test.go:TestZonemdGeneratedForPrimaryZones`                                                                                                                              | **Contract mismatch:** spec requires API `400 invalid_request`; `mgmt/internal/api/zonemd_api_test.go:41` and `e2e/zonemd_test.go:255` assert `422 unsupported_type`. T23 records changed assertion. Exact criterion not satisfied. |
| S-5 secondary verification/last good | `mgmt/internal/xfrin/refresh_test.go:TestRefreshDiscardsTransferFailingZonemd`, product `TestZonemdSecondaryVerification`                                                                                  | Rerun with baseline fenced scheduler; maintain rejected transfer/last-good/retry behavior.                                                                                                                                          |
| S-6 RPZ verification                 | `engine/src/recursor/rpz/transfer_tests.rs`, `e2e/rpz_zonemd_test.go`                                                                                                                                      | Recorded 21 RPZ unit tests/pipeline/product proof; integrated run and RPZ UI/status missing.                                                                                                                                        |
| S-1 gateway                          | `engine/src/mdns/{gateway,iface}.rs`; dirty runtime/dispatch wiring                                                                                                                                        | Core TTL/reply/collection/cap tests recorded; no product `e2e/mdns_test.go` for real gateway UDP/TCP/PTR merge/NXDOMAIN/precedence/metrics.                                                                                         |
| S-1 default-off, S-1/S-7 hot path    | Dirty `engine/tests/{mdns_pipeline,hot_path_alloc}.rs` and runtime wiring                                                                                                                                  | No T21 recorded pass; `TestMdnsOffForwardsLocalNames` absent; prove both-feature zero allocations and disabled ordinary DNS behavior.                                                                                               |
| S-2 reflector                        | `engine/src/mdns/reflector.rs`, `0c3b1bc` plus Linux pinning `bb16479`                                                                                                                                     | Unit/mutation evidence not real cross-interface reflection/loop/duplicate/disabled-control proof; `TestMdnsReflectorNetLab` absent.                                                                                                 |
| S-3 multicast fixture                | Namespace harness/fixture; recorded `TestNetLabVethMulticast` PASS 2.012s                                                                                                                                  | Fixture transport is not actual gateway/reflector. Supported Linux namespace/interface execution needed; kw only settings without hostNetwork.                                                                                      |
| S-7/8 ODoH target/rotation           | `engine/src/server/odoh.rs`, pipeline tests, `mgmt/internal/odoh/`, control key delivery, API/snapshot                                                                                                     | Product `e2e/odoh_test.go:TestODoHTargetAndProxy` absent: two managed engines/independent circl client/key publish delay/old-key continuity/error contracts.                                                                        |
| S-9 ODoH proxy privacy/errors        | Codec/proxy/pipeline tests, independent `e2e/harness/odoh*.go`                                                                                                                                             | Same absent product proof must assert no client header/address leakage, exact status/timeout and denied/malformed targets.                                                                                                          |
| S-10 catalog producer                | `mgmt/internal/catzone/{codec,service}.go` and tests                                                                                                                                                       | `e2e/catalog_zones_test.go` absent; `e2e/harness/named.go` lacks Catalogs rendering; actual BIND consumption/lifecycle proof missing.                                                                                               |
| S-11 catalog consumer                | `TestConsumerReconcile`, `mgmt/internal/xfrin/scheduler.go:AfterRefresh`, main wiring                                                                                                                      | T15 explicitly lacks dedicated AfterRefresh test; T25 BIND creation/removal/label reset/broken catalog/recovery/managed guards/delete retention absent.                                                                             |
| S-12 API/roles/audit                 | API catalog/ZONEMD/ODoH/mDNS tests, generated contracts                                                                                                                                                    | Historical scoped/isolated-copy results; integrated seven-operation RBAC/audit/snapshot proof and exact HTTP mismatch above remain.                                                                                                 |
| S-12 GUI/help/mobile                 | Untracked seed files and screen specs 40–43 only                                                                                                                                                           | Feature components/pages/navigation/help absent; RPZ screen44 absent. No desktop/400px pass.                                                                                                                                        |
| S-14 docs/deploy/kw                  | Generic deployment machinery exists                                                                                                                                                                        | M8 operations/help/docs regression, `e2e/kw_smoke_m8_test.go`, script inclusion absent; no M8 deployment/probe/strict cleanup proof.                                                                                                |

| Task | Preserved commit/work and next action                                                                        |
| ---- | ------------------------------------------------------------------------------------------------------------ |
| T1   | `f08c686` contract/modules/vectors; regenerate/rerun combined.                                               |
| T2   | `f08c686` OpenAPI/generated Go/TS/permissions; historical 145-operation parity, retain newer baseline APIs.  |
| T3   | `f08c686` migrations/models; applied-history decision and baseline-upgrade/snapshot regression.              |
| T4   | `ffba330` Go digest vectors/mutation proof; integrate.                                                       |
| T5   | `5ee1a2e` Rust digest; three tests/clippy in isolated copy recorded.                                         |
| T6   | `cc05fde` gateway core; four tests, real product multicast still absent.                                     |
| T7   | `9c8d5a0` ODoH codec/proxy; five tests/upstream coverage recorded.                                           |
| T8   | `6b3430e` catalog codec; deterministic/broken-catalog tests.                                                 |
| T9   | `bd1a028` settings/sealed rotating keys; rotation/revision tests.                                            |
| T10  | `d99d94e` namespace/fixture; actual fixture multicast proof.                                                 |
| T11  | `e49b5bb` independent ODoH client; config/key-ID tests; throwaway roundtrip explicitly uncommitted.          |
| T12  | `50f12ad` rebuild/signing; signature/serial/mutation evidence.                                               |
| T13  | `7c7717f` secondary verification; actual AXFR/IXFR coverage.                                                 |
| T14  | `54289f4` zone/RPZ/catalog API; force rebuild/null membership/hook tests; HTTP mismatch unresolved.          |
| T15  | `3b554b7` catalog service; AfterRefresh product proof missing.                                               |
| T16  | `c4c04bf` ODoH API/key push/snapshot; historical full API run excluded another unfinished task file.         |
| T17  | `d5e02be` mDNS group API/snapshot; private-copy pass excluded unfinished catalog files.                      |
| T18  | `0c3b1bc`, `bb16479` reflector/pinning; retain Linux guard; no product reflection acceptance.                |
| T19  | `1384552` engine target/proxy; three pipeline, three upstream, twelve snapshot tests recorded.               |
| T20  | `ed7c11d` RPZ verification; memory/persistence last-good tests.                                              |
| T21  | Dirty runtime and pipeline/hot-path work; review/adopt/test separately, not certified PASS.                  |
| T22  | `4920292` API/service/main wiring; historical package/mutation evidence; integrate session-fenced callbacks. |
| T23  | `3fb09b9` three product ZONEMD tests PASS 52.300s; not exact API status acceptance.                          |
| T24  | ODoH product test absent; reuse existing independent client.                                                 |
| T25  | Catalog product test and BIND catalog renderer absent.                                                       |
| T26  | Actual-engine multicast product tests absent.                                                                |
| T27  | ZONEMD seed/screen present untracked; `web/src/pages/ZoneZonemdCard.tsx`/help absent.                        |
| T28  | Catalog seed/screen present untracked; `web/src/pages/CatalogZonesPage.tsx`/route/navigation/help absent.    |
| T29  | ODoH screen present untracked; `web/src/pages/OdohSection.tsx`/help absent.                                  |
| T30  | mDNS seed/screen present untracked; `web/src/pages/EngineGroupMdnsSection.tsx`/help absent.                  |
| T31  | RPZ seed untracked; verification editor/status/help and `web/e2e/screens/44-rpz-zonemd.spec.ts` absent.      |
| T32  | Four operations sections/help links and `deploy/deploytest/m8_docs_test.go` absent.                          |
| T33  | Full combined engine/management/web/lint/product/hot-path verification unperformed.                          |
| T34  | `TestKwSmokeM8` implementation/script wiring absent; parent deployment/strict acceptance unperformed.        |

Dirty tracked paths preserved: `.procoder/notes/plan-review.md`,
`.procoder/plans/nexora-m8-dns-protocols.md`, `engine/src/authoritative/state.rs`,
`engine/src/mdns/mod.rs`, `engine/src/recursor/dispatch.rs`, `engine/src/runtime.rs`,
`engine/src/server/mod.rs`, `engine/src/snapshot.rs`, `engine/src/telemetry/metrics.rs`,
`engine/tests/common/mod.rs`, `engine/tests/hot_path_alloc.rs`.
Untracked: `engine/tests/mdns_pipeline.rs`,
`e2e/gui_seed_m8_{catalog,mdns,rpz,zonemd}_test.go`,
`web/e2e/screens/{40-zonemd,41-catalog-zones,42-odoh,43-mdns}.spec.ts`.

Before assigning a new migration range above main, parent needs authoritative
knowledge of whether any M8 versions ran in a database that must survive. If yes,
a migration-history transition is required; simply renaming applied files is
unsafe. Preserve baseline failover rows, identity/config and session-token columns
in a real baseline-upgrade regression plus fresh install. Compose ODoH delivery and
stats with baseline ownership fencing; regenerate combined OpenAPI/proto/Go/TS,
never replace newer generated files wholesale. This is a real integration gate,
not permission to alter the preserved branch or to regenerate trust.

## Smallest next runnable slices and prerequisites

Each numbered item is a bounded implementation or verification unit, not a new
closure decision. Test commands are parent handoff suggestions, **not executed
results**. Parent owns Linux/live execution and integration; authors can write
ordinary source/test changes in separately assigned trees without cluster access.

1. **Strict 10-second HA assertion:** fix deadline arithmetic in
   `e2e/control_test.go`, add deterministic deadline coverage if practical, then
   parent runs `go test ./e2e -run '^TestMgmtStatelessHA$' -count=1 -v` with rebuilt
   real binaries. No infrastructure decision needed to write the fix.
2. **Complete filter fleet budget evidence:** extract validation around
   `e2e/kw_filter_categories_test.go` to require expected persistent IDs, nonempty
   complete fresh stats, reject disconnected/missing/duplicate observations, and
   avoid node-name map collapse. Pure negative cases runnable independently;
   live measurement remains parent-owned. Keep all numeric budgets intact.
3. **Lost-trust browser behavior:** extend `web/e2e/screens/16-dnssec.spec.ts` with
   isolated lost/healthy anchor observations, proving alert appears and clears.
   Do not revoke real root trust. Unit metric tests already exist.
4. **Recursion-cache telemetry:** add an explicit observed-byte measurement from
   actual recursor usage through stats/proto/management, preserving field numbers;
   test measured values and older-stats compatibility. Then a separate capacity
   sampler slice reads current M7 limit, stores daily usage and verifies idempotence.
   No model provider, live Secret or cluster is needed for these implementations.
5. **M7 export failure diagnosis:** reproduce exact-capacity
   `engine/tests/telemetry_export.rs` failure on supported Linux; classify legitimate
   bounded eviction versus defective loss/timing. Preserve the 35/36 observation,
   all batches and assertions; repeated attempts are evidence, not an automatic
   pass policy. Do not preselect a weaker success threshold.
6. **M8 migration regression and exact API rejection:** migration inventory is
   parent input; fixture/assertion work can start now. Independently implement the
   specified `400 invalid_request` ZONEMD rejection and precise API/product checks,
   or obtain an explicit specification decision; never silently bless 422.
7. **BIND catalog renderer:** add deterministic rendering and unit tests in
   `e2e/harness/named.go`; then independent producer and consumer product cases,
   including actual AfterRefresh reconciliation. BIND runtime needed only to run.
8. **ODoH product proof:** implement T24 with existing circl helper and real managed
   engines; rotation/privacy/error contracts are the work, not missing crypto.
9. **mDNS adoption and two tests:** separately review preserved dirty T21; parent
   runs `cargo test --locked -p nexora-engine --test mdns_pipeline`, library mdns,
   hot-path and full engine tests. Implement gateway and reflector product cases
   separately; Linux namespaces/multicast are execution prerequisites. Add
   `TestMdnsOffForwardsLocalNames` independently using an ordinary standalone
   engine; that negative case does not need multicast namespaces.
10. **Five independent M8 UI slices:** T27–31 each combines feature/API/help with
    desktop/mobile behaviors using preserved seeds/screens. Add missing RPZ screen.
    API contracts needed for integrated execution, not permission to erase seeds.
11. **M8 operations/docs and smoke wiring:** T32 static documentation/tests is
    independent; T34 smoke/test-selection implementation precedes parent live run.
    Check each expected test is actually listed/run: Go regex exit0 matching a
    subset is not proof that missing ODoH/catalog/mDNS tests exist.
12. **Failover trusted observation adapter:** bind authoritative persistent IDs to
    exact pod/node incarnation, current target/applied digests and direct-DNS
    observations; exercise expiry/replacement/unknown/revoked/config mismatch
    through the adapter, not invented eligible structs. Baseline pure evaluator
    already exists. Real topology confirmation belongs to parent.
13. **Actual-engine cross-host fixture:** reuse bounded DR fixture, add genuine
    source-policy/query-log assertions and negative controls, then large signed
    replies/truncation/MTU/session tests. Runtime prerequisites: two isolated Linux
    hosts, namespace/IPVS capabilities, tcpdump/ethtool/arping and iptables for SNAT;
    no LAN attachment guessed from apparently free addresses. No CNI migration.
14. **M5 separate-host acceptance harness:** preserve current local regression;
    add explicit host independence/same-version ACK and management-outage DNS
    assertions. Isolated independent hosts required for execution, not for design
    and harness implementation. Do not disrupt production from this audit.
15. **Frontend adapter→fencing→lifecycle:** after FG-01 gate, build owned-resource
    idempotent reconciliation and interruption tests; specify stale-owner exclusion
    before HA coding; only then safe drain/withdrawal/tombstone reuse. API/UI and
    chart/cutover follow dependency order. These are engineering work, not merely
    missing environment credentials.
16. **M10 toolbox artifact proof:** source wiring exists; build immutable toolbox
    image and exercise pinned-version/missing-binary selftest in an authorized
    independent build location. Private binary staging does not certify image.
17. **Release evidence reconciliation:** rebuild final integrated assets/binaries,
    run full Linux suites and exact immutable artifacts, then parent-only guarded
    deployment/acceptance and manual UX. Preserve all prior failures and distinguish
    recovered final health from successful original workflow. No closure edits.

External prerequisites: authoritative M8 applied migration history; supported
Linux sockets/namespace/kernel behavior; PostgreSQL/Collector/OpenSearch/ClickHouse/
Loki/BIND/delv/ldns/SoftHSM/browser fixtures; controlled x86/reference perf hardware
and kw latency environment; authorized isolated operator/CNPG/MinIO environment;
ARC/scheduled workflow execution; novanas Compose and private fastllm for their
specific live criteria. Old infrastructure failures need fresh read-only diagnosis
before being labelled current outages. Missing test code, GUI, adapters, telemetry,
full-suite execution and evidence mapping are **unperformed work**, not inherently
external blockers. Foreground locks and live ownership checks cannot be inferred
from this read-only audit.

Suggested parent verification after implementation and normal builds: `make
engine-test`, `make mgmt-test`, `make web-test`, `make lint`, `make operator-test`,
`go test -race ./deploy/kwrollout ./mgmt/internal/control ./mgmt/internal/stats
./mgmt/internal/failover -count=1`, real backend conformance, release-mode
`cargo test --locked --release -p nexora-engine --test filter_index_budget`, and
`NEXORA_E2E_BIN_DIR="$PWD/bin" go test ./e2e/... -count=1 -timeout 120m`.
All live opt-ins must remain disabled during ordinary suite runs. Inspect intended
runners/configuration first; these commands do not authorize dev-exec/dev-sync,
shared toolbox access, cluster mutations or SSH in this wave's assigned worktree.

## Exact standalone task-state inventory

This inventory is read from baseline records, not rewritten states. Checked/open
counts are literal checklist syntax, not acceptance verdicts. Evidence “present”
means text exists, not that all required proof exists. M8 has no baseline todo
series; its preserved 34-task ledger is above. FG/CP are proposed plan stories.

| Milestone | Closed | Open | Other nonclosed | Total |
| --------- | -----: | ---: | --------------: | ----: |
| M1        |     23 |    0 |               0 |    23 |
| M2        |     13 |    0 |               0 |    13 |
| M3        |     15 |    0 |               0 |    15 |
| M4        |     16 |    0 |               0 |    16 |
| M5        |     15 |    0 |               0 |    15 |
| M6        |      0 |   31 |               3 |    34 |
| M7        |      0 |   22 |               1 |    23 |
| M9        |      0 |   12 |               0 |    12 |
| M10       |      0 |   12 |               0 |    12 |
| M11       |      0 |   21 |              11 |    32 |

| Task record                                                                               | Exact recorded status                                                                                     | Checked/unchecked criteria | Evidence text |
| ----------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- | -------------------------: | ------------- |
| [M1 T01](../todo/20260913-m1-t01-dev-pod-sync-exec-scripts-and-makefile.md)               | closed 2026-09-13                                                                                         |                        2/0 | present       |
| [M1 T02](../todo/20260913-m1-t02-repository-skeleton-control-proto-and-code-generat.md)   | closed 2026-09-13                                                                                         |                        4/0 | present       |
| [M1 T03](../todo/20260913-m1-t03-zero-copy-query-parser-edns-cookies-and-the-fuzz-w.md)   | closed 2026-09-13                                                                                         |                        3/0 | present       |
| [M1 T04](../todo/20260913-m1-t04-coarse-clock-and-wire-format-response-cache-with-t.md)   | closed 2026-09-13                                                                                         |                        3/0 | present       |
| [M1 T05](../todo/20260913-m1-t05-upstream-udp-tcp-transports-with-anti-spoofing-and.md)   | closed 2026-09-13                                                                                         |                        3/0 | present       |
| [M1 T06](../todo/20260913-m1-t06-dot-and-doh-upstreams-and-cross-worker-request-coa.md)   | closed 2026-09-13                                                                                         |                        3/0 | present       |
| [M1 T07](../todo/20260913-m1-t07-filter-sets-acl-applied-runtime-and-snapshot-valid.md)   | closed 2026-09-13                                                                                         |                        3/0 | present       |
| [M1 T08](../todo/20260913-m1-t08-per-core-udp-tcp-listeners-query-pipeline-counters.md)   | closed 2026-09-14                                                                                         |                        3/0 | present       |
| [M1 T09](../todo/20260913-m1-t09-prometheus-endpoint-otlp-logs-traces-metrics-expor.md)   | closed 2026-09-13                                                                                         |                        3/0 | present       |
| [M1 T10](../todo/20260913-m1-t10-end-to-end-harness-and-fixture-servers.md)               | closed 2026-09-14                                                                                         |                        6/0 | present       |
| [M1 T11](../todo/20260913-m1-t11-forwarding-acceptance-tests-over-the-wire.md)            | closed 2026-09-14                                                                                         |                        7/0 | present       |
| [M1 T12](../todo/20260913-m1-t12-management-plane-foundations-env-config-postgresql.md)   | closed 2026-09-13                                                                                         |                        7/0 | present       |
| [M1 T13](../todo/20260913-m1-t13-snapshot-builder-transactional-mutations-with-audi.md)   | closed 2026-09-13                                                                                         |                        9/0 | present       |
| [M1 T14](../todo/20260913-m1-t14-authentication-and-authorization-passwords-session.md)   | closed 2026-09-13                                                                                         |                        7/0 | present       |
| [M1 T15](../todo/20260913-m1-t15-openapi-spec-http-api-handlers-embedded-gui-servin.md)   | closed 2026-09-13                                                                                         |                        5/0 | present       |
| [M1 T16](../todo/20260913-m1-t16-engine-management-plane-client-and-control-plane-a.md)   | closed 2026-09-14                                                                                         |                        5/0 | present       |
| [M1 T17](../todo/20260913-m1-t17-blocklist-subscriptions-fetcher-parser-normalised-.md)   | closed 2026-09-14                                                                                         |                        5/0 | present       |
| [M1 T18](../todo/20260913-m1-t18-query-log-backends-fleet-metrics-shared-test-servi.md)   | closed 2026-09-14                                                                                         |                        9/0 | present       |
| [M1 T19](../todo/20260913-m1-t19-gui-foundation-auth-screens-playwright-harness-and.md)   | closed 2026-09-14                                                                                         |                        3/0 | present       |
| [M1 T20](../todo/20260913-m1-t20-remaining-gui-screens-testguicoverage-and-testquer.md)   | closed 2026-09-14                                                                                         |                        4/0 | present       |
| [M1 T21](../todo/20260913-m1-t21-two-tier-dnsperf-performance-gate.md)                    | closed 2026-09-14                                                                                         |                        4/0 | present       |
| [M1 T22](../todo/20260913-m1-t22-container-images-and-ci-workflows.md)                    | closed 2026-09-14                                                                                         |                        3/0 | present       |
| [M1 T23](../todo/20260913-m1-t23-first-deployment-to-kw-and-testkwsmoke.md)               | closed 2026-09-14                                                                                         |                        4/0 | present       |
| [M2 T01](../todo/20260913-m2-t01-control-contract-additions-and-architecture-update.md)   | closed 2026-09-14                                                                                         |                        4/0 | present       |
| [M2 T02](../todo/20260913-m2-t02-engine-listener-config-proxy-v2-parser-and-the-sha.md)   | closed 2026-09-14                                                                                         |                        3/0 | present       |
| [M2 T03](../todo/20260913-m2-t03-engine-certificate-store-and-tlsmaterial-over-the-.md)   | closed 2026-09-14                                                                                         |                        2/0 | present       |
| [M2 T04](../todo/20260913-m2-t04-dot-and-doh-listeners.md)                                | closed 2026-09-14                                                                                         |                        2/0 | present       |
| [M2 T05](../todo/20260913-m2-t05-doq-listener-rfc-9250.md)                                | closed 2026-09-14                                                                                         |                        2/0 | present       |
| [M2 T06](../todo/20260913-m2-t06-engine-per-client-policy-rewrites-and-safe-search-.md)   | closed 2026-09-14                                                                                         |                        2/0 | present       |
| [M2 T07](../todo/20260913-m2-t07-management-plane-schema-store-and-snapshot-policy-.md)   | closed 2026-09-14                                                                                         |                        7/0 | present       |
| [M2 T08](../todo/20260913-m2-t08-openapi-operations-and-handlers-for-policies-rewri.md)   | closed 2026-09-14                                                                                         |                        4/0 | present       |
| [M2 T09](../todo/20260913-m2-t09-management-plane-dns-tls-certificate-loading-push-.md)   | closed 2026-09-14                                                                                         |                        8/0 | present       |
| [M2 T10](../todo/20260913-m2-t10-gui-screens-policies-rewrites-and-settings-tls-sec.md)   | closed 2026-09-14                                                                                         |                        4/0 | present       |
| [M2 T11](../todo/20260913-m2-t11-e2e-harness-encrypted-clients-and-testencryptedtra.md)   | closed 2026-09-14                                                                                         |                        3/0 | present       |
| [M2 T12](../todo/20260913-m2-t12-testperclientpolicy-and-testsafesearchrewrites.md)       | closed 2026-09-14                                                                                         |                        7/0 | present       |
| [M2 T13](../todo/20260913-m2-t13-kw-deployment-smoke-subtests-gui-coverage-and-perf.md)   | closed 2026-09-14                                                                                         |                        4/0 | present       |
| [M3 T01](../todo/20260913-m3-t01-proto-contract-and-engine-snapshot-validation-for-.md)   | closed 2026-09-14                                                                                         |                        2/0 | present       |
| [M3 T02](../todo/20260913-m3-t02-outbound-transport-with-spoofing-defences-and-recu.md)   | closed 2026-09-14                                                                                         |                        2/0 | present       |
| [M3 T03](../todo/20260913-m3-t03-infrastructure-cache-rrset-cache-root-hints-work-b.md)   | closed 2026-09-14                                                                                         |                        2/0 | present       |
| [M3 T04](../todo/20260913-m3-t04-iterative-resolver-delegations-bailiwick-glueless-.md)   | closed 2026-09-14                                                                                         |                        2/0 | present       |
| [M3 T05](../todo/20260913-m3-t05-mode-selection-forward-zones-and-query-path-integr.md)   | closed 2026-09-14                                                                                         |                        2/0 | present       |
| [M3 T06](../todo/20260913-m3-t06-dnssec-primitives-signature-verification-validity-.md)   | closed 2026-09-14                                                                                         |                        3/0 | present       |
| [M3 T07](../todo/20260913-m3-t07-validator-chain-of-trust-ede-ntas-cd-ad-forward-mo.md)   | closed 2026-09-14                                                                                         |                        3/0 | present       |
| [M3 T08](../todo/20260913-m3-t08-trust-anchor-store-with-rfc-5011-automated-rollove.md)   | closed 2026-09-14                                                                                         |                        3/0 | present       |
| [M3 T09](../todo/20260913-m3-t09-rpz-policy-engine-parsing-triggers-precedence-acti.md)   | closed 2026-09-14                                                                                         |                        2/0 | present       |
| [M3 T10](../todo/20260913-m3-t10-rpz-sources-blob-files-axfr-ixfr-with-tsig-soa-ref.md)   | closed 2026-09-14                                                                                         |                        2/0 | present       |
| [M3 T11](../todo/20260913-m3-t11-management-plane-migrations-openapi-operations-han.md)   | closed 2026-09-14                                                                                         |                        8/0 | present       |
| [M3 T12](../todo/20260913-m3-t12-gui-rpz-dnssec-resolution-settings-and-forward-zon.md)   | closed 2026-09-14                                                                                         |                        3/0 | present       |
| [M3 T13](../todo/20260913-m3-t13-private-dns-hierarchy-fixture-and-harness-helpers-.md)   | closed 2026-09-14                                                                                         |                        9/0 | present       |
| [M3 T14](../todo/20260913-m3-t14-acceptance-tests-testrecursionroothints-testspoofe.md)   | closed 2026-09-14                                                                                         |                        6/0 | present       |
| [M3 T15](../todo/20260913-m3-t15-kw-deployment-update-and-kw-smoke-subtests.md)           | closed 2026-09-14                                                                                         |                        3/0 | present       |
| [M4 T01](../todo/20260913-m4-t01-contract-additions-and-the-nzf-zone-format.md)           | closed 2026-09-14                                                                                         |                       10/0 | present       |
| [M4 T02](../todo/20260913-m4-t02-engine-nzf-decoder-in-memory-zone-model-delta-appl.md)   | closed 2026-09-14                                                                                         |                        2/0 | present       |
| [M4 T03](../todo/20260913-m4-t03-engine-authoritative-answers-unsigned.md)                | closed 2026-09-14                                                                                         |                        2/0 | present       |
| [M4 T04](../todo/20260913-m4-t04-engine-runtime-integration-incremental-zone-loadin.md)   | closed 2026-09-14                                                                                         |                        2/0 | present       |
| [M4 T05](../todo/20260913-m4-t05-management-plane-zones-records-served-image-builde.md)   | closed 2026-09-14                                                                                         |                       10/0 | present       |
| [M4 T06](../todo/20260913-m4-t06-key-storage-kek-envelope-pkcs-11-tsig-keys-keymate.md)   | closed 2026-09-14                                                                                         |                       12/0 | present       |
| [M4 T07](../todo/20260913-m4-t07-engine-tsig-rfc-8945-and-signed-queries-to-hosted-.md)   | closed 2026-09-14                                                                                         |                        3/0 | present       |
| [M4 T08](../todo/20260913-m4-t08-zone-transfers-out-axfr-ixfr-acl-tsig-and-notify-t.md)   | closed 2026-09-14                                                                                         |                        3/0 | present       |
| [M4 T09](../todo/20260913-m4-t09-bind-zone-file-import-and-export.md)                     | closed 2026-09-14                                                                                         |                        6/0 | present       |
| [M4 T10](../todo/20260913-m4-t10-secondary-zones-engine-notify-intake-management-pl.md)   | closed 2026-09-14                                                                                         |                        7/0 | present       |
| [M4 T11](../todo/20260913-m4-t11-rfc-2136-dynamic-updates-authenticated-with-tsig.md)     | closed 2026-09-14                                                                                         |                        5/0 | present       |
| [M4 T12](../todo/20260913-m4-t12-management-plane-online-dnssec-signer.md)                | closed 2026-09-14                                                                                         |                        7/0 | present       |
| [M4 T13](../todo/20260913-m4-t13-engine-dnssec-answers-from-pre-signed-zone-data.md)      | closed 2026-09-14                                                                                         |                        2/0 | present       |
| [M4 T14](../todo/20260913-m4-t14-key-rollovers-cds-cdnskey-dnssec-api-and-the-signi.md)   | closed 2026-09-14                                                                                         |                       10/0 | present       |
| [M4 T15](../todo/20260913-m4-t15-gui-zones.md)                                            | closed 2026-09-14                                                                                         |                        3/0 | present       |
| [M4 T16](../todo/20260913-m4-t16-kw-deployment-helm-compose-key-storage-smoke-subte.md)   | closed 2026-09-14                                                                                         |                        3/0 | present       |
| [M5 T01](../todo/20260913-m5-t01-settle-the-fleet-design-in-docs-architecture-md.md)      | closed 2026-09-14                                                                                         |                        3/0 | present       |
| [M5 T02](../todo/20260913-m5-t02-fleet-schema-migration.md)                               | closed 2026-09-14                                                                                         |                        4/0 | present       |
| [M5 T03](../todo/20260913-m5-t03-contract-additions-for-fleet-health-and-certificat.md)   | closed 2026-09-14                                                                                         |                        3/0 | present       |
| [M5 T04](../todo/20260913-m5-t04-rollout-state-machine.md)                                | closed 2026-09-14                                                                                         |                       11/0 | present       |
| [M5 T05](../todo/20260913-m5-t05-per-group-snapshots-and-publishing.md)                   | closed 2026-09-14                                                                                         |                        4/0 | present       |
| [M5 T06](../todo/20260913-m5-t06-rollout-controller-targeted-pushes-and-fleet-healt.md)   | closed 2026-09-14                                                                                         |                        5/0 | present       |
| [M5 T07](../todo/20260913-m5-t07-fleet-http-api-and-the-fleet-e2e-harness.md)             | closed 2026-09-14                                                                                         |                        8/0 | present       |
| [M5 T08](../todo/20260913-m5-t08-engine-lifecycle-in-the-management-plane.md)             | closed 2026-09-14                                                                                         |                        6/0 | present       |
| [M5 T09](../todo/20260913-m5-t09-engine-side-of-renewal-rotation-revocation-and-fle.md)   | closed 2026-09-14                                                                                         |                        2/0 | present       |
| [M5 T10](../todo/20260913-m5-t10-fleet-acceptance-tests.md)                               | closed 2026-09-14                                                                                         |                       12/0 | present       |
| [M5 T11](../todo/20260913-m5-t11-fleet-gui.md)                                            | closed 2026-09-14                                                                                         |                        4/0 | present       |
| [M5 T12](../todo/20260913-m5-t12-release-images-workflow-and-docker-compose-example.md)   | closed 2026-09-14                                                                                         |                        4/0 | present       |
| [M5 T13](../todo/20260913-m5-t13-helm-chart-finalisation.md)                              | closed 2026-09-14                                                                                         |                        5/0 | present       |
| [M5 T14](../todo/20260913-m5-t14-final-kw-deployment-and-testkwfullproduct.md)            | closed 2026-09-14                                                                                         |                        5/0 | present       |
| [M5 T15](../todo/20260913-m5-t15-operations-documentation-and-readme.md)                  | closed 2026-09-14                                                                                         |                       17/0 | present       |
| [M6 T01](../todo/20260914-m6-t01-contract-fields-700-799-and-the-m6-architecture-te.md)   | open                                                                                                      |                        8/1 | present       |
| [M6 T02](../todo/20260914-m6-t02-openapi-contract-permissions-generated-clients-and.md)   | open                                                                                                      |                       11/0 | present       |
| [M6 T03](../todo/20260914-m6-t03-gui-e2e-seed-registry-and-wider-spec-glob.md)            | open                                                                                                      |                        6/0 | present       |
| [M6 T04](../todo/20260914-m6-t04-engine-telemetry-plumbing-for-m6.md)                     | open                                                                                                      |                       12/0 | present       |
| [M6 T05](../todo/20260914-m6-t05-query-log-backends-and-api-partial-names-multi-val.md)   | open                                                                                                      |                       11/1 | present       |
| [M6 T06](../todo/20260914-m6-t06-access-control-split-in-the-management-plane.md)         | open                                                                                                      |                        9/0 | present       |
| [M6 T07](../todo/20260914-m6-t07-account-self-service-backend.md)                         | open                                                                                                      |                        7/1 | present       |
| [M6 T08](../todo/20260914-m6-t08-version-endpoint-and-build-stamping.md)                  | open                                                                                                      |                        7/1 | present       |
| [M6 T09](../todo/20260914-m6-t09-collapsible-filter-categories.md)                        | open                                                                                                      |                        4/1 | present       |
| [M6 T10](../todo/20260914-m6-t10-engine-metrics-api.md)                                   | open                                                                                                      |                        4/2 | present       |
| [M6 T11](../todo/20260914-m6-t11-help-foundation-tooltip-component-catalogue-help-p.md)   | open                                                                                                      |                       12/0 | present       |
| [M6 T12](../todo/20260914-m6-t12-engine-attribution-of-allowed-rewritten-and-rpz-de.md)   | open                                                                                                      |                        5/1 | present       |
| [M6 T13](../todo/20260914-m6-t13-engine-log-ring-buffer-and-logrequest-handling.md)       | open                                                                                                      |                        5/1 | present       |
| [M6 T14](../todo/20260914-m6-t14-management-plane-engine-log-broker.md)                   | open                                                                                                      |                        7/1 | present       |
| [M6 T15](../todo/20260914-m6-t15-parallel-upstream-race-in-the-engine.md)                 | open                                                                                                      |                        5/1 | present       |
| [M6 T16](../todo/20260914-m6-t16-access-control-split-in-the-engine-hot-path-proof-.md)   | open (blocked on the query-log rule mapping outside Task 16's files)                                      |                        7/1 | present       |
| [M6 T17](../todo/20260914-m6-t17-parallel-strategy-in-the-management-plane-and-its-.md)   | open                                                                                                      |                        6/0 | present       |
| [M6 T18](../todo/20260914-m6-t18-dashboard-backend-series-top-lists-health.md)            | open                                                                                                      |                        6/1 | present       |
| [M6 T19](../todo/20260914-m6-t19-query-log-gui-partial-names-multi-select-url-state.md)   | open                                                                                                      |                        6/1 | present       |
| [M6 T20](../todo/20260914-m6-t20-access-control-gui-and-zone-allow-query-editor.md)       | done (pending commit)                                                                                     |                        6/0 | present       |
| [M6 T21](../todo/20260914-m6-t21-account-gui-profile-page-change-password-dialog-me.md)   | open                                                                                                      |                        4/1 | present       |
| [M6 T22](../todo/20260914-m6-t22-engine-modal-with-metrics-logs-and-queries.md)           | open                                                                                                      |                        3/2 | present       |
| [M6 T23](../todo/20260914-m6-t23-operations-guide-for-m6.md)                              | done (not committed; the lead commits)                                                                    |                        3/1 | present       |
| [M6 T24](../todo/20260914-m6-t24-navigation-shell-forwarding-recursion-filtering-gr.md)   | open                                                                                                      |                        5/1 | present       |
| [M6 T25](../todo/20260914-m6-t25-dashboard-gui.md)                                        | open                                                                                                      |                        5/0 | present       |
| [M6 T26](../todo/20260914-m6-t26-version-footer-and-reload-hint.md)                       | open                                                                                                      |                        5/1 | present       |
| [M6 T27](../todo/20260914-m6-t27-parallel-strategy-in-settings.md)                        | open                                                                                                      |                        4/1 | present       |
| [M6 T28](../todo/20260914-m6-t28-help-and-descriptions-resolver-pages.md)                 | done (awaiting the lead's commit)                                                                         |                        7/0 | present       |
| [M6 T29](../todo/20260914-m6-t29-help-and-descriptions-filtering-pages.md)                | open                                                                                                      |                        5/1 | present       |
| [M6 T30](../todo/20260914-m6-t30-help-and-descriptions-zone-pages.md)                     | open                                                                                                      |                        4/1 | present       |
| [M6 T31](../todo/20260914-m6-t31-help-fleet-pages-and-the-engine-modal.md)                | open                                                                                                      |                        3/1 | present       |
| [M6 T32](../todo/20260914-m6-t32-help-and-descriptions-users-tokens-audit-account-q.md)   | open                                                                                                      |                        4/1 | present       |
| [M6 T33](../todo/20260914-m6-t33-help-gate-help-and-page-header-specs-full-gui-cove.md)   | open                                                                                                      |                        5/0 | present       |
| [M6 T34](../todo/20260914-m6-t34-deploy-m6-to-kw-and-run-acceptance.md)                   | open                                                                                                      |                        0/6 | empty         |
| [M7 T01](../todo/20260914-m7-t01-settle-the-m7-design-in-docs-architecture-md.md)         | open                                                                                                      |                       10/0 | present       |
| [M7 T02](../todo/20260914-m7-t02-contract-field-recursionconfig-cache-max-bytes.md)       | open                                                                                                      |                        4/0 | present       |
| [M7 T03](../todo/20260914-m7-t03-badvers-for-edns-versions-other-than-0-21.md)            | open                                                                                                      |                        6/0 | present       |
| [M7 T04](../todo/20260914-m7-t04-acl-as-merged-ranges-with-binary-search-11.md)           | open (verified, awaiting commit)                                                                          |                        5/0 | present       |
| [M7 T05](../todo/20260914-m7-t05-zone-transfers-off-the-worker-thread-12.md)              | open                                                                                                      |                        4/0 | present       |
| [M7 T06](../todo/20260914-m7-t06-renewal-fallback-and-unstorable-identities-13-14.md)     | open                                                                                                      |                        8/0 | present       |
| [M7 T07](../todo/20260914-m7-t07-rfc-5011-revocation-of-the-only-trusted-key-15.md)       | open                                                                                                      |                        6/1 | present       |
| [M7 T08](../todo/20260914-m7-t08-rpz-ixfr-keys-only-for-deleted-owners-19.md)             | open                                                                                                      |                        5/0 | present       |
| [M7 T09](../todo/20260914-m7-t09-query-log-labels-by-config-version-and-concurrent-.md)   | done                                                                                                      |                        7/0 | present       |
| [M7 T10](../todo/20260914-m7-t10-recursor-memory-budget-atomic-infra-updates-uncapp.md)   | open (verified, awaiting commit)                                                                          |                       13/0 | present       |
| [M7 T11](../todo/20260914-m7-t11-per-worker-buffer-pool-for-tcp-dot-and-doq-20-22.md)     | open                                                                                                      |                       10/0 | present       |
| [M7 T12](../todo/20260914-m7-t12-recursor-cache-max-bytes-in-the-api-snapshot-and-g.md)   | open                                                                                                      |                        9/1 | present       |
| [M7 T13](../todo/20260914-m7-t13-getblob-streams-from-postgresql-25.md)                   | open                                                                                                      |                        5/0 | present       |
| [M7 T14](../todo/20260914-m7-t14-opensearch-paging-with-a-unique-tiebreaker-26.md)        | open                                                                                                      |                        6/0 | present       |
| [M7 T15](../todo/20260914-m7-t15-pkcs-11-session-recovery-27.md)                          | open                                                                                                      |                        6/0 | present       |
| [M7 T16](../todo/20260914-m7-t16-streaming-zone-export-28.md)                             | open                                                                                                      |                        7/0 | present       |
| [M7 T17](../todo/20260914-m7-t17-record-edits-without-loading-the-zone-29.md)             | open                                                                                                      |                        7/1 | present       |
| [M7 T18](../todo/20260914-m7-t18-wildcard-synthesis-in-the-private-dns-hierarchy-9.md)    | open                                                                                                      |                        6/0 | present       |
| [M7 T19](../todo/20260914-m7-t19-collector-restart-waits-for-its-port-10.md)              | open                                                                                                      |                        4/0 | present       |
| [M7 T20](../todo/20260914-m7-t20-the-compose-example-on-novanas-5.md)                     | open                                                                                                      |                        6/0 | present       |
| [M7 T21](../todo/20260914-m7-t21-debt-marker-test-and-operations-limits.md)               | open                                                                                                      |                        4/0 | present       |
| [M7 T22](../todo/20260914-m7-t22-workflows-proven-on-the-arc-runners-3.md)                | open                                                                                                      |                        0/5 | present       |
| [M7 T23](../todo/20260914-m7-t23-milestone-verification-kw-deployment-and-issue-clo.md)   | open                                                                                                      |                        0/5 | empty         |
| [M9 T01](../todo/20260915-m9-t01-operator-contract-architecture-module-skeleton-crd.md)   | open (implemented, awaiting lead commit)                                                                  |                        9/0 | present       |
| [M9 T02](../todo/20260915-m9-t02-management-api-contract-system-users-error-code-mi.md)   | open                                                                                                      |                        5/0 | present       |
| [M9 T03](../todo/20260915-m9-t03-bootstrap-token-and-system-users-in-the-management.md)   | open                                                                                                      |                       10/0 | present       |
| [M9 T04](../todo/20260915-m9-t04-nexora-chart-bootstrap-token-value-cnpg-ha-backups.md)   | open                                                                                                      |                        6/0 | present       |
| [M9 T05](../todo/20260915-m9-t05-operator-key-material-and-the-management-api-clien.md)   | open                                                                                                      |                        5/1 | present       |
| [M9 T06](../todo/20260915-m9-t06-rendering-the-nexora-chart-from-a-nexorainstallati.md)   | open                                                                                                      |                        3/0 | present       |
| [M9 T07](../todo/20260915-m9-t07-nexoraenginegroup-controller.md)                         | open                                                                                                      |                        2/0 | present       |
| [M9 T08](../todo/20260915-m9-t08-operator-packaging-image-workflow-operator-chart-a.md)   | open                                                                                                      |                        5/0 | present       |
| [M9 T09](../todo/20260915-m9-t09-nexorainstallation-controller.md)                        | open                                                                                                      |                        3/0 | present       |
| [M9 T10](../todo/20260915-m9-t10-kw-operator-end-to-end-in-nexora-optest.md)              | open (every subtest green on kw with `dev-m9-8483b44`; the test and plan changes await the lead's commit) |                        6/0 | present       |
| [M9 T11](../todo/20260915-m9-t11-operations-guide-kw-readme-and-docs-test.md)             | open                                                                                                      |                        4/0 | present       |
| [M9 T12](../todo/20260915-m9-t12-deploy-m9-to-kw-production-and-close-the-issues.md)      | open                                                                                                      |                        0/6 | empty         |
| [M10 T01](../todo/20260915-m10-t01-conformance-suite-and-builtin-conformance.md)          | open                                                                                                      |                        7/0 | present       |
| [M10 T02](../todo/20260915-m10-t02-configuration-and-architecture-text.md)                | open                                                                                                      |                        7/0 | present       |
| [M10 T03](../todo/20260915-m10-t03-clickhouse-and-loki-binaries-in-the-dev-toolbox.md)    | open                                                                                                      |                        8/0 | present       |
| [M10 T04](../todo/20260915-m10-t04-harness-collector-exporters-and-management-options.md) | open                                                                                                      |                        6/0 | present       |
| [M10 T05](../todo/20260915-m10-t05-clickhouse-backend.md)                                 | open                                                                                                      |                        8/1 | present       |
| [M10 T06](../todo/20260915-m10-t06-loki-backend.md)                                       | open                                                                                                      |                        8/1 | present       |
| [M10 T07](../todo/20260915-m10-t07-opensearch-conformance.md)                             | open                                                                                                      |                        2/1 | present       |
| [M10 T08](../todo/20260915-m10-t08-management-plane-wiring-and-product-e2e.md)            | open                                                                                                      |                       11/1 | present       |
| [M10 T09](../todo/20260915-m10-t09-helm-chart.md)                                         | open                                                                                                      |                        6/1 | present       |
| [M10 T10](../todo/20260915-m10-t10-kw-clickhouse-collector-fan-out-and-kw-acceptance.md)  | open                                                                                                      |                       11/1 | present       |
| [M10 T11](../todo/20260915-m10-t11-operations-guide-and-help-topic.md)                    | open                                                                                                      |                        3/3 | present       |
| [M10 T12](../todo/20260915-m10-t12-full-verification-kw-deploy-acceptance-and-issue-c.md) | open                                                                                                      |                        0/6 | empty         |
| [M11 T01](../todo/20260915-m11-t01-m11-architecture-text.md)                              | open                                                                                                      |                        4/1 | present       |
| [M11 T02](../todo/20260915-m11-t02-openapi-contract-permissions-generated-clients-stu.md) | done (verified; awaiting lead commit)                                                                     |                        9/0 | present       |
| [M11 T03](../todo/20260915-m11-t03-ai-service-foundation.md)                              | done (verified, awaiting lead commit)                                                                     |                       12/0 | present       |
| [M11 T04](../todo/20260915-m11-t04-scripted-fake-openai-compatible-fixture.md)            | open                                                                                                      |                        4/1 | present       |
| [M11 T05](../todo/20260915-m11-t05-agent-scheduler-async-tasks-and-retention-pruning.md)  | open                                                                                                      |                        6/1 | present       |
| [M11 T06](../todo/20260915-m11-t06-proposals-action-validation-and-replay-apply.md)       | open                                                                                                      |                        9/1 | present       |
| [M11 T07](../todo/20260915-m11-t07-findings-and-forecasts-stores-and-their-handlers.md)   | open                                                                                                      |                        6/1 | present       |
| [M11 T08](../todo/20260915-m11-t08-helm-chart-ai-and-mcp-wiring-alerts.md)                | open                                                                                                      |                        3/1 | present       |
| [M11 T09](../todo/20260915-m11-t09-management-plane-wiring-status-tasks-and-foundatio.md) | open                                                                                                      |                        7/0 | present       |
| [M11 T10](../todo/20260915-m11-t10-mcp-server-and-stdio-bridge.md)                        | done                                                                                                      |                        6/0 | present       |
| [M11 T11](../todo/20260915-m11-t11-gui-foundation-for-ai.md)                              | open                                                                                                      |                        4/2 | present       |
| [M11 T12](../todo/20260915-m11-t12-natural-language-query-log-search.md)                  | open                                                                                                      |                        6/0 | present       |
| [M11 T13](../todo/20260915-m11-t13-query-log-anomaly-agent.md)                            | open                                                                                                      |                        5/1 | present       |
| [M11 T14](../todo/20260915-m11-t14-dashboard-insight-agent.md)                            | open                                                                                                      |                        4/1 | present       |
| [M11 T15](../todo/20260915-m11-t15-filter-and-policy-recommendations-agent.md)            | open                                                                                                      |                        4/1 | present       |
| [M11 T16](../todo/20260915-m11-t16-configuration-assistant.md)                            | done                                                                                                      |                        6/0 | present       |
| [M11 T17](../todo/20260915-m11-t17-upstream-health-prediction-agent.md)                   | open                                                                                                      |                        4/1 | present       |
| [M11 T18](../todo/20260915-m11-t18-rollout-risk-agent.md)                                 | done                                                                                                      |                        7/0 | present       |
| [M11 T19](../todo/20260915-m11-t19-threat-checks-list-classification-and-query-log-th.md) | done (not committed — the lead commits)                                                                   |                        7/0 | present       |
| [M11 T20](../todo/20260915-m11-t20-capacity-forecast-agent.md)                            | open                                                                                                      |                        4/1 | present       |
| [M11 T21](../todo/20260915-m11-t21-rpz-suggestions-agent.md)                              | done (not committed — the lead commits)                                                                   |                        6/0 | present       |
| [M11 T22](../todo/20260915-m11-t22-suggest-only-proof-prompt-injection-test-and-viewe.md) | open (e2e run blocked on M11 Tasks 18-21)                                                                 |                        3/2 | present       |
| [M11 T23](../todo/20260915-m11-t23-insights-gui-and-dashboard-ai-card.md)                 | done (not committed; the lead commits)                                                                    |                        5/0 | present       |
| [M11 T24](../todo/20260915-m11-t24-query-log-gui-ask-box-anomaly-banner-threat-badge.md)  | implemented, not committed (the lead commits)                                                             |                        3/1 | present       |
| [M11 T25](../todo/20260915-m11-t25-recommendations-gui.md)                                | open                                                                                                      |                        3/1 | present       |
| [M11 T26](../todo/20260915-m11-t26-assistant-gui.md)                                      | implemented, not committed (the lead commits)                                                             |                        3/1 | present       |
| [M11 T27](../todo/20260915-m11-t27-forecasts-gui-and-upstream-predictions-panel.md)       | open                                                                                                      |                        3/1 | present       |
| [M11 T28](../todo/20260915-m11-t28-rollout-risk-gui.md)                                   | open                                                                                                      |                        3/1 | present       |
| [M11 T29](../todo/20260915-m11-t29-threat-check-dialog-and-list-classification-gui.md)    | open                                                                                                      |                        3/1 | present       |
| [M11 T30](../todo/20260915-m11-t30-rpz-suggestions-gui.md)                                | open                                                                                                      |                        3/1 | present       |
| [M11 T31](../todo/20260915-m11-t31-operations-guide-and-ai-help-topic.md)                 | done (not committed; the lead commits)                                                                    |                        4/1 | present       |
| [M11 T32](../todo/20260915-m11-t32-full-suite-kw-smoke-test-deploy-and-acceptance.md)     | open                                                                                                      |                        1/7 | present       |

## Audit verification, review and handoff

Only `.procoder/notes/completion-wave3.md` was produced in the assigned tree.
No commits/pushes, task/sprint closure, live/cluster/SSH access, shared toolbox,
dev-sync/dev-exec, secret reads, trust regeneration or external-tree mutation.
The retained CAS lock, exact client tuples, serial paired rollout, OnDelete and
`--wait=legacy` remain unchanged. Parent owns integration and supported/live tests.

Commands/observations in this audit:

- Read-only `git rev-parse HEAD`, `git status --short`, `git worktree list`, and
  preserved-tree status/log/diff-stat/merge-base/rev-list established scope and
  provenance; `cat`, `sed`, `rg --files`, `rg -n` and Python read-only inventory
  traced criteria to source/tests. No index/worktree administration commands ran.
- Python generated the 195-record task index directly from baseline statuses and
  checked all 195 relative task links resolve. It checked M1–M11, categories,
  FG-01–12 and CP-01–02 coverage markers; PASS. These are document consistency
  checks, not product behavior tests.
- Independent read-only reviewer passes rechecked M1–M5/categories and preserved
  M8 against the draft. Adversarial review retained exact timing/status-code
  mismatches, vacuous fleet-budget risk, separate-host gap, migration-lineage and
  snapshot omissions, exporter RED and reference-hardware gaps. Final polish
  corrected relative source paths and made default-off mDNS a separate runnable
  test slice. No weakened assertions or new retry policy was introduced.
- `procoder version`: 1.0.2. `procoder review`: unsupported, exit2/usage. Manual
  reviewer/adversarial passes were performed; no tool review PASS claimed.
- `procoder test .procoder/notes/completion-wave3.md`: terminated at 40 seconds
  without output. No completed test report or behavioral pass is claimed.
  Environment cleared NEXORA_KW_* opt-ins, used GOPROXY=off and temporary GOCACHE;
  supported Linux/product/live acceptance remains parent-owned and unverified here.
- First `procoder check .procoder/notes/completion-wave3.md`: reported unformatted
  Markdown, then timed out at 40 seconds. `procoder format` subsequently returned
  a Prettier 3.9.6 formatted result; reviewed whitespace/table changes are applied.
  Final gate outcome is recorded below; the initial gate is not called green.
- Earlier command-discovery `procoder review --help` printed usage; the combined
  shell invocation reached `procoder check --help` and was interrupted with exit130
  after no report. Do not infer its later `procoder test --help` was executed.
  `ps` was denied by the sandbox. Missing config/AGENTS/backlog/deploy-failover
  searches returned absence; large initial reads were followed by focused reads.

Handoff: the milestone tables distinguish delivered code, existing test coverage,
attributed historical runs, deployed SHAs and unmet acceptance. Implement the small
independent slices above in assigned trees; obtain migration history before schema
integration; retain all red evidence; then run final combined supported suites.
Live prerequisites and immutable-image guarded deployment remain parent work.
No closure can be inferred from this audit or from stale open/closed task states.

Final verification: post-format `procoder check
.procoder/notes/completion-wave3.md` exited **0**: 1 clean, 0 unformatted,
0 unchecked, 0 out of scope; seven informational hygiene findings, zero blocking.
Informational findings concern the existing missing `*.test` ignore entry, three
Dockerfile lint notices and three missing Procoder templates; none was changed
outside this note's scope. `prettier --check .procoder/notes/completion-wave3.md`
passed. `git diff --no-index --check /dev/null
.procoder/notes/completion-wave3.md` emitted no whitespace diagnostics (exit1 is
the expected new-file difference). Final Python link/inventory checks passed.
`git status --short` showed only this new note and HEAD stayed at f9c1653.
The test timeout and unsupported review command remain limitations, not passes.
