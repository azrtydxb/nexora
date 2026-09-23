# Wave 4 — completion loop in progress

This is an execution ledger, not a completion claim. Main remains on `f9c1653`
with the combined wave3 candidate staged. No serving deployment, migration,
frontend cutover or formal task/sprint/milestone closure has occurred here.

## Inventory and parallel ownership

Procoder status reports **127 open standalone tasks**, no active sprint and no
backlog. Proposed failover sprints remain forecasts, not activated records.
Read-only full-roadmap reconciliation completed in `/tmp/nexora-wave4/audit`;
its matrix is `wave4-gap-audit.md`. The full inventory has 229 todos: 82 historically
closed, 127 open and 20 other nonclosed. Code, executed verification, deployment
acceptance and missing implementation remain separate evidence categories.

Separate worktrees/branches contain these lanes:

| Lane                     | Scope                                                                     | Required convergence                                                         |
| ------------------------ | ------------------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| `wave4/audit`            | Read-only specs/plans/tasks/source/evidence audit                         | Requirement-level remaining-gap matrix, not checkbox inference               |
| `wave4/runtime`          | Failover runtime, owned platform integration and opt-in integration tests | Actual gate/Lease/platform integration; no activation from incomplete proofs |
| `wave4/lifecycle`        | Failover eligibility/lifecycle and reserved migration `01306`             | Authenticated observations, drain/tombstones and retained reservations       |
| `wave4/control-review`   | Read-only review of control-final delta                                   | Concrete defects and verification boundaries                                 |
| `wave4/control-lifetime` | Control lifetime and narrowly related store cleanup                       | Bounded rollback and joined asynchronous UPDATE work                         |

Parent exclusively owns main integration, shared toolbox, SSH and cluster/network
operations. Source snapshots contain prior parent work: import only exact new
lane deltas, never entire worktree HEAD diffs. Do not sync over running test trees.
Original dirty M8 and the unfinished historical M10 merge remain preserved.

## Verification and new defects

- Earlier commit-barrier/UI candidate: complete GUI shard passed 83 screen cases
  plus setup (335.169s); complementary every-other-E2E shard passed (1298.388s).
  Live opt-in skips are not acceptance. Operator race/envtest also passed.
- Control-final imported five files: pending TSIG replacement now invalidates
  deduplication until acknowledged commit, including empty-set recovery; handler
  shutdown no longer waits for RPC-blocked Send before returning.
- Initial Linux shutdown fixture failed because it counted bootstrap key messages
  and assumed the next blocked Send was another TSIG message. The corrected
  observer starts at its 2 MiB quota filler and watches every subsequent Send;
  a queued bootstrap snapshot may encounter the exhausted quota first. The peer
  remains non-reading and the actual flow-control/cleanup assertions are intact.
- Corrected targeted Linux race passed **7.585s**; ten repeated race runs passed
  **70.786s**. Logs: `/tmp/nexora-wave4/control-{targeted,repeated}-linux.log`.
  The original fixture failure remains in `control-final-linux.log` under wave3.
- Fresh independent review found unbounded rollback and unjoined asynchronous
  UPDATE work. Parent additionally found blocking TLS graceful-close and missing
  failed-BEGIN retirement coverage. The six-file control-lifetime follow-up is
  now imported: bounded rollback, uncertain connection retirement with known TLS
  wrapper unwrapping, failed-BEGIN cleanup and joined UPDATE callbacks. Targeted
  Linux store race passed **7.966s**, real non-reading gRPC lifecycle cases passed
  **5.425s**; the broader affected race suite is running in the integrated tree.
  Evidence: `/tmp/nexora-wave4/control-lifetime-linux.{log,exit}`. These changes
  still need final review and combined product verification.
- The prior complete enabled product rerun finished: control race **154.289s**,
  GUI **332.16s**, complementary native E2E **1297.461s**. Evidence:
  `/tmp/nexora-wave4/product-linux.{log,exit}`. That tree is no longer running;
  these results predate the new control-lifetime changes and cannot certify them.
- Deliberate digest/shutdown mutants are isolated in
  `/work/nexora-wave4-control-negative`. Initial attempts lacked test-only source
  dependencies and are not negative-control proof. The completed fixture run is
  recorded separately as `control-negative-reviewed-linux.log`.
- Lease bindings passed Linux race **101.368s** and vet with the positive clock
  test executed, not skipped. Restoring the invalid per-thread procfs path fails
  the strengthened clock test. This is not actual kernel/API-server integration.
- Full laptop Procoder test remains RED: 281 Go failures and Rust `libc::mmsghdr`.
  Preserve real Linux Helm goldens; classify environment failures honestly.

## Additional gap lanes and follow-ups

Completed candidate handoffs await exact-delta review/integration for lifecycle
(migration 1306), AI/query-log, M8 contracts, auth/lost-trust UI and detached runtime.
None constitutes acceptance. Lifecycle notes flag an old-schema M8 fixture that
must seed old SQL directly rather than call new-schema CRUD.

Engine follow-up is authorized to edit the deciding worker/recursor/mDNS callers,
not merely telemetry consumers, to preserve publication-bound attribution. Its
bounded exporter scheduler fix and restored original 400ms/20,000-record criterion
are unexecuted candidates; new attribution regressions intentionally expose the
still-missing implementation. CI follow-up now covers replacing insecure CA trust
fetching with explicitly provisioned trusted input, without credential access or
invented infrastructure. Both continue in their original isolated worktrees.

Runtime's detached candidate is frozen at
`/tmp/nexora-wave4/runtime-detached-files/deploy/failover`. A separate follow-up in
its existing worktree owns all four failover implementation directories to build
the isolated active/all-egress bridge and address helper privilege separation.
No production activation is permitted by missing clock/drain/platform evidence.

Read-only serving preflight passed after these test operations: four distinct
identities, both endpoint sets, configuration, direct/VIP UDP/TCP and node capacity.
Evidence `/tmp/nexora-wave4/preflight-readonly.{log,exit}`; no serving mutation.
The helper was built directly without the destructive dev-sync wrapper.

Optional dnstap scope is asked in `../ask/decisions.md`, not silently implemented
or called complete. Explicit hardware/packaging/DHCP holds remain intact.

## Remaining acceptance boundaries

Dedicated frontend runtime must fence **all relevant transmit paths**: frontend
TC egress alone cannot exclude stale DR forwarding through another backend egress.
Management connectivity remains independent. Kernel attachment is classic TC;
foreign TCX checks do not mean TCX attachment. Lease acknowledgement is historical
operation evidence, not current authority, eligibility or proof of packet fencing.

Trusted publication, lifecycle withdrawal/reuse, audited API/GUI, dedicated chart,
guarded migration, unattended bootstrap, all-management-replica upgrade and the
full member/LB/node/partition/session failure matrix remain open. Historical abrupt
engine-a failure remains RED. No tests may hide failed DNS attempts or weaken their
assertions. Existing `.136` A/C and `.139` B/D, identities, trust, data, VXLAN and
serial rollout gates remain mandatory. Retained-lock recovery requires verified
quiescence and exact CAS, never age-based removal.

Split DNS remains planning-only and Pi-hole parity excluded. Formal Procoder state
changes require the human-invoked slash workflow; this ledger does not bypass it.
