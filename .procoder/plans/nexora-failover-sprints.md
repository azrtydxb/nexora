# Remaining failover work — sprint plan

Status: proposed sprint allocation, not an activated Procoder sprint.
Baseline: `504a006` (integrated eligibility, bootstrap convergence, session ownership
and operator regression fixes; includes the earlier harness and desired-state model).
Scope: finish the approved transparent failover groups and the rollout/control-plane
work needed to deploy and accept them. Parent: `nexora-failover-groups.md`, T1–T6.
No `.procoder/sprints/` or backlog exists at this checkpoint; existing standalone
M9 T12 and M11 T32 tasks remain open. Formal opening, pulling and closing belong
to the human-invoked `/procoder:` workflow; this document does not change those states.

## Wave 2 execution checkpoint

See `../notes/wave2-integration.md` for integrated M10/operator and expanded
control-ownership test evidence, plus the two-host DR fixture's 40 verified flows.
The actual-engine attachment and cross-host negative controls remain open; the
Sprint 1 network gate has **not** passed. Later HA/lifecycle/API/UI/migration
sprints remain dependency-gated. No formal sprint/milestone closure is implied.

## Baseline: do not schedule completed work again

- Implemented/tested: persisted desired groups, exact two-member and unique-IP/member
  constraints, generation CAS, conservative observed status; not deployed.
- Implemented/tested: reusable same-host IPVS DR fixture, 20 five-transport flows,
  source-loss controls, packet/socket tuple checks. Not cross-host or LB HA proof.
- Existing kw dev deployment: four persistent identities, `.136` A/C and `.139`
  B/D, serial guarded rollout, Cilium VXLAN. Revalidate live inventory before any
  mutation; this planning session makes no live-state claim beyond prior evidence.
- Prior strict product acceptance passed, but abrupt engine-a failover remains RED.
  Unattended live bootstrap remains unverified; bounded convergence is now
  implemented/tested. Session ownership is partially fenced; see CP-02 below.
- Full laptop suite remains RED (235 Go failures, Rust `mmsghdr`); scoped Linux
  results are green. Neither result is a substitute for a final full supported-suite run.

## Refresh at 504a006 — implementation is not acceptance

Evidence: `.procoder/notes/parallel-integration.md` records parent-run supported
Linux management/rollout/chart, control/stats/failover race and operator envtest
passes. The earlier engine baseline passed before those patches. These are scoped
results, not a fresh full engine/web/e2e/release pass. Current inventory and exact
branch integration order: `.procoder/notes/completion-wave2.md`.

| Story    | Implemented/tested at baseline or in this audit                                                                           | Still required                                                                                                                    |
| -------- | ------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------- |
| FG-01    | Same-host fixture and tuple verifier                                                                                      | Cross-host actual engines, policy attribution and return-path/cleanup proof; wave2 work is pending integration, not accepted here |
| FG-02    | Pure fail-closed placement/config eligibility and tests landed                                                            | Trusted observation adapter, real identity/node/config/freshness evidence                                                         |
| FG-03    | Historical notes/test reviewed and copied in this audit; failure-test safety regressions pass locally                     | Parent review/integration, supported Linux race rerun and complete baseline; no live disruption rerun                             |
| FG-04–08 | Desired-state reservations and conservative status only                                                                   | Owned platform reconciler, independent HA/dataplane fencing, lifecycle/deletion, API and GUI                                      |
| CP-01    | Fixed-target bounded bootstrap acknowledgement wait and negative tests landed                                             | Unattended immutable-SHA deployment acceptance without retries/manual rescue                                                      |
| CP-02    | UUID session migration; DB fencing of disconnect/ACK/rejection/stats/TLS/renewal/hello landed with race/negative evidence | Stale NOTIFY/UPDATE callbacks, pending logs and remaining memory effects; upgrade every management replica, then full acceptance  |
| FG-09–10 | Guarded paired rollout already exists                                                                                     | Dedicated frontend chart and staged cutover/rollback after all prerequisite gates                                                 |
| FG-11–12 | Historical strict product pass and abrupt engine-a failure RED preserved                                                  | Full failure matrix and release acceptance; no formal closure                                                                     |

M8 (`3fb09b9` plus dirty work) and M10 (`d0009ce`) contain substantial code absent
from main. Review and integrate that work; do not rebuild it from main-only absence.
Preserve the M8 dirty worktree and `nexora-merge` unfinished M10 merge.

## Capacity and execution policy

Planning assumption: one implementation owner, two-week sprint timeboxes, seven
engineering days of planned work plus three days for review, test runs and defects.
This is a capacity model, not a delivery-date promise or measured velocity. Re-size
at the first review; do not compress safety gates to fit a date. Only Sprint 1 is
ready to select now; Sprints 2–5 are a dependency-gated forecast.

Sizes below are relative uncertainty/effort: S = focused change, M = multi-file
feature/test slice, L = high-risk integration; split L work into runnable daily
checkpoints during sprint opening. Roles denote responsibility, not extra staff.
One person can execute them serially. With a second implementer, independent
control-plane/API work may proceed in a separate worktree; no parallel kw mutations.
Limit work in progress to one network change and one independent code-only story.

If a spike fails its gate, retain its evidence, stop dependent work and replan.
A failed experiment can finish an investigation, but cannot close the forwarding
acceptance criterion. No automatic architecture switch or CNI migration.

## Sprint 1 — prove the deployable network boundary

Goal: demonstrate that real cross-host engine attachment can preserve client tuples
without disturbing the overlay/control plane. No movement of existing DNS VIPs.

### FG-01 — cross-host attachment and return path (L, network owner, T1)

Files: `deploy/failoverlab/`, `deploy/failover/`, `deploy/kw/`,
`.procoder/notes/kw-transparent-lb.md`.

Steps: inventory actual interfaces/routes and address ownership; allocate isolated
backend test addresses without guessing unused LAN IPs; extend the harness across
hosts; attach a disposable actual Nexora engine namespace; verify ARP suppression,
source-selected return routing and two co-hosted engine namespaces for different
groups. Preserve management/control connectivity and owned-resource cleanup.

Exit: two clients reach both backends across nodes on all five transports; original
IP/port and reverse VIP/port match packet/socket evidence; actual engine policy/log
attribution agrees. Cross-group leakage and duplicate advertisement tests fail
closed. Record capability/privilege requirements and cleanup, including failures.

### FG-02 — topology and eligibility contract (M, management owner, T2/T4)

Files: `mgmt/internal/failover/`, `deploy/kwrollout/`, `deploy/failover/`.

Remaining steps (pure validator is implemented/tested): wire trusted persistent
identity-to-pod/node binding, freshness, effective configuration comparison and
reason codes; retain the distinction between admission, desired state and observed
eligibility. Exercise the implemented pure validation/state tests through adapters.

Exit: prefixed/self-reported names cannot prove node separation; same physical node,
unknown identity, stale report, revoked identity and mismatched applied policy/config
are ineligible. Decide the snapshot-equivalence rule explicitly; same policy group
alone is insufficient. Adapter binding tests follow FG-01, not fabricated observations.

### FG-03 — evidence and verification baseline (S, verification owner)

Files: outstanding `deploy/kwrollout/failure_live_test.go`,
`.procoder/notes/kw-member-failure.md`, `.procoder/notes/kw-dsr-feasibility.md`,
`scripts/dev-exec.sh`, relevant test/documentation files.

Steps: parent reviews/integrates the historical evidence and hardened harness copied
by wave2 audit; preserve the original main untracked copies. Reproduce
full supported Linux test baseline; classify laptop failures versus code defects.
Do not rewrite raw Linux Helm goldens to make the laptop green.

Exit: every baseline failure has a reproducible command, environment and next action;
old abrupt-failure evidence is preserved; new regressions block progression.

**Sprint gate:** FG-01 passes and FG-02 has a tested contract. If cross-host proof
fails, Sprint 2 network implementation is blocked; independent diagnostics may continue.

## Sprint 2 — safe frontend ownership and lifecycle

Entry: Sprint 1 network gate. Goal: an operational adapter and redundant frontend
that remain safe under interrupted work and loss of ownership, before GUI exposure.

### FG-04 — narrowly scoped platform adapter (M, network owner, T4)

Depends: FG-01, FG-02. Files: `deploy/failover/`, deployment image/chart sources.
Implement idempotent owned IPVS/interface/address/route reconciliation and backend
attachment. Test interrupted apply, repeated apply, unexpected existing resources,
unsupported kernels and malicious configuration. No global flush or arbitrary
shell/config execution. Exit: only assigned objects change; crash/restart converges
or fails closed with actionable status and recoverable evidence.

### FG-05 — independent LB HA and fencing (L, network owner, T4)

Depends: FG-04. Files: `deploy/failover/`, `mgmt/internal/failover/`, lab tests.
Implement two LB instances on distinct nodes, ownership acquisition/loss and stale
owner exclusion. Specify the partition and lease-loss behavior before coding.
A cooperative Kubernetes lock is not proof of data-plane fencing.
Exit: paused/stale owner, process/node loss, asymmetric partition and control-plane
outage cannot leave persistent duplicate advertising. Backend failure does not
move a healthy frontend. Capture detection, withdrawal and takeover timing.

### FG-06 — eligibility, drain and safe deletion (M, management/network owner, T2/T4)

Depends: FG-02, FG-04; withdrawal/reuse acceptance also requires FG-05.
Files: `mgmt/internal/failover/`, `mgmt/migrations/`, `deploy/failover/`.
Implement health/config eligibility, drain/re-admission, authenticated/fenced
observation writes, tombstones and withdrawal acknowledgement. Retain IP/member
reservations until old ownership is proven gone; include stale generation/owner
rejection and crash recovery. Exit: empty pools fail closed without spillover;
no delete/recreate or member-edit path bypasses drain, fencing or reservations.

**Sprint gate:** two real LB instances pass ownership/partition tests, and lifecycle
operations cannot free a still-advertised IP. API deletion stays unavailable until
this passes. If capacity is exceeded, carry FG-06 explicitly; do not silently defer it.

## Sprint 3 — product surface and control-plane reliability

Goal: users can manage groups with truthful status, and deployment can converge
without manual retries. Control-plane diagnostics may start earlier in parallel.

### FG-07 — audited API and generated contracts (M, management owner, T3)

Depends: FG-02, FG-06. Files: `mgmt/internal/api/`, `mgmt/api/openapi.yaml`,
generated Go/TypeScript clients and API tests.
Implement create/list/detail/update/safe-delete with admin authorization, audit in
the mutation transaction, validation and generation conflicts. Return desired/applied
generation, frontend owner, eligibility reasons and unknown/degraded/unavailable.
Exit: real database round trips cover authorization, collisions, stale writes,
withdrawal-pending deletion and unavailable dependencies; no request grants ownership.

### FG-08 — group GUI and browser acceptance (M, web owner, T3)

Depends: FG-07. Files: `web/src/api/`, `web/src/pages/`, navigation/help, `web/e2e/`.
Implement list/detail/edit and guarded deletion independently of DNS policy groups.
Exit: real API-backed browser tests cover CRUD, stale edit, pending withdrawal,
validation, permissions, loading/unavailable/stale telemetry and accessibility;
configured membership or Ready pods alone never produce a healthy frontend label.

### CP-01 — bounded bootstrap convergence (M, control owner)

Depends: no network dependency; required before Sprint 4 live deployment.
Files: `deploy/kw/bootstrap.sh`, `deploy/kwrollout/`, bootstrap/script tests.
Implemented/tested at `504a006`: bounded control-plane convergence against a
fixed published target, pinned existing identities, target changes and cancellation.
Remaining: parent-run unattended live deployment convergence.
Exit: delayed acknowledgements converge; permanently stale/rejected
configuration fails at the deadline and retains the lock. Never retry or hide DNS
failures. Distinguish waiting for acknowledgement from republishing configuration.

### CP-02 — cross-instance ownership regression (M, control owner)

Depends: no network dependency. Files: `mgmt/internal/control/`,
`mgmt/internal/store/`, multi-instance integration tests.
Partial implementation/test evidence at `504a006`: per-stream UUID database
ownership and transactional stats fencing, including reused instance IDs and
red/green stale ACK regression. Remaining callbacks/logs/in-memory effects and
all-replica upgrade acceptance are explicit gaps. Prove
that late disconnects, stale heartbeats and configuration acknowledgements cannot
clobber a newer owner. Implement the demonstrated root-cause fix, not another
process-local mutex. Exit: regression is red without the fix and green with it;
otherwise keep this explicitly unresolved and block final reliability closure.

**Capacity rule:** FG-07/08 and CP-01/02 are two workstreams, not four simultaneous
commitments for one owner. If combined sizing exceeds seven days, prioritize
CP-01/02 then FG-07; carry FG-08 into a continuation sprint before Sprint 4. This
forecast may therefore require an additional sprint; no fixed five-sprint promise.

**Sprint gate:** complete API/UI integration and CP-01/02 evidence. No manual recovery
or later green acceptance may stand in for unattended convergence.

## Sprint 4 — guarded migration of the two existing frontends

Entry: network/HA/lifecycle, API/UI and convergence gates. Goal: replace VIP ownership
one group at a time, without changing client addressing or persistent engine state.

### FG-09 — chart, preflight and staged rollout integration (M, deployment owner, T5)

Depends: FG-04–08, CP-01. Files: `deploy/helm/nexora/`, `deploy/deploytest/`,
`deploy/kw/`, `deploy/kwrollout/`, `scripts/kw-deploy.sh`, `scripts/kw-preflight.sh`.
Stage backend attachments and redundant LBs before switching any advertisement.
Update topology probes for the new frontend contract, retaining direct/frontend DNS,
source attribution, identity, freshness, capacity and management gates.
Exit: Linux chart goldens/schema and interruption tests pass; one partner at a time,
`OnDelete`, `--wait=legacy` and non-expiring ownership/CAS safeguards remain intact.

### FG-10 — cutover and bounded rollback (L, deployment owner, T5)

Depends: FG-09 and all Sprint 3 exit criteria.
Files: migration runner/tests in `deploy/kwrollout/`, `deploy/kw/`, operational notes.
Inspect current lock owner and quiescence; recover only through verified CAS.
Commit/build first. Rehearse every transition: staged, old advertisement withdrawn,
new ownership activated, verified, and rollback. Then migrate `.136` A/C, gate it,
and only then `.139` B/D. No third client-facing DNS IP.
Exit: exactly one advertisement system owns each IP, both initial mappings and all
four persistent identities/trust/data survive; interrupt-at-each-step tests and
bounded rollback pass. Any DNS failure halts migration and retains the lock; a
recovered fleet does not retroactively make the failed attempt successful.

**Sprint gate:** a fully guarded deployment exits zero with both new frontends and
complete measurements. A merely healthy final cluster is insufficient.

## Sprint 5 — measured failure acceptance and operational closure

### FG-11 — full transport and failure matrix (L, verification owner, T6)

Depends: FG-10. Files: `e2e/`, `deploy/kwrollout/`, `deploy/failoverlab/`, evidence.
Run both clients against both groups/backends on UDP/TCP DNS, DoT, HTTP/2 DoH and
DoQ. Include client-specific policy/log identity, large signed replies, truncation,
MTU/fragmentation, reused TCP/QUIC sessions and planned draining.
Then fail each of A/B/C/D and each LB individually; test node/network partitions,
ownership loss and empty pools. Restore and verify between cases; stop on failure.
Exit: retain all original attempts and packet/query measurements; report new-flow
and established-session failures separately, no fallback or successful retry counted
as the original success. Confirm backend loss leaves frontend ownership stable.

Before the first destructive matrix run, freeze documented acceptance thresholds
and observation windows in the spec/tests. Do not select tolerances after observing
loss or weaken existing strict DNS gates; no unsupported lossless-session claim.

### FG-12 — strict product acceptance and governance (M, release owner, T6)

Depends: FG-11, CP-01, CP-02. Files: `scripts/kw-acceptance.sh`, `docs/operations.md`,
`deploy/kw/README.md`, `.procoder/notes/`, existing M9 T12 and M11 T32 evidence.
Run full supported Linux Go/Rust/web/operator/chart suites and strict live acceptance
against the deployed immutable SHA. Record laptop results separately and resolve
real regressions. Refresh recovery, ownership, drain, upgrade and troubleshooting
runbooks; map existing M9/M11 criteria to evidence instead of reimplementing them.
Exit: review/test/check evidence attached, deploy and acceptance distinguished,
formal task/sprint/release closures only through the Procoder human workflow after
its gates pass. Do not claim release readiness while required suites remain red.

## Dependency summary and exclusions

Critical path: FG-01 → FG-04 → FG-05/06 → FG-07/08 → FG-09/10 → FG-11/12.
FG-02 feeds FG-04/06/07. CP-01 gates FG-09/10; CP-02 gates Sprint 3 exit and final
reliability acceptance. FG-03 establishes the verification/evidence baseline early.

Split-horizon DNS stays a separate planning-only track: architecture review can be
scheduled independently, but its implementation is not included or authorized here.
Pi-hole parity and Cilium migration remain excluded. General IPv6 frontend support
is not promised by the current IPv4 adapter/model; expand only after verified scope
and datapath work, not by accepting unsupported addresses in the API.

## Definition of done for every selected story

- Acceptance assertions implemented and exercised, including negative/error paths.
- Reviewer reread, adversarial defect hunt and final polish recorded.
- Supported Linux verification plus Procoder review/test/check; report failures honestly.
- Versioned evidence and docs; no private keys, credentials or transcript dumps.
- Distinguish implemented, tested, deployed and accepted; no automatic checkbox closure.
- Network changes preserve raw failure evidence, rollback and the retained-lock protocol.
