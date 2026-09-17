# Failover group implementation plan

Specification: `../specs/nexora-failover-groups.md`. Status: implementation authorized;
no API/UI/datapath completion claimed. Keep the existing kw VXLAN recovery state.
Remaining work is allocated in [the sprint plan](nexora-failover-sprints.md), with
cross-host/HA gates before migration and explicit bootstrap/control-plane work.

## T1 — prove dedicated transparent forwarding

Files: `deploy/failoverlab/` and `../notes/kw-transparent-lb.md`.

Build an opt-in isolated namespace fixture using Linux IPVS Direct Routing and two
backends. Reuse the existing five-transport DNS probe dependencies. Keep the entire
lab off the LAN with benchmark-only addresses. Test both backend paths, original
source tuples, group reply address, payload integrity and a negative source-loss
control. Capture packets and clean up only owned namespaces/processes. Then prove
cross-host attachment/return routing without changing Cilium or existing VIPs.
Document kernel capabilities and all failed cases. Do not select a deployable
adapter based only on successful same-host namespace traffic.

## T2 — domain model and persistence

Files: `mgmt/internal/failover/`, `mgmt/migrations/`, `mgmt/internal/store/`.

Implement group IDs/names/frontend addresses, exactly two persistent engine
members, unique membership/IP constraints, optimistic generation updates and
separate desired/observed state. Keep policy engine groups unchanged. Add tests
for invalid/reused identities, address collisions, same-node members and stale
writes; migrations preserve existing engines/configuration.

Progress: desired-state persistence and observation projection are implemented in
`mgmt/internal/failover/` and migration `01300_failover_groups.sql`. Database
constraints enforce exactly two members and unique cross-group membership; writes
use generation checks and preserve the frontend IP. Admission rejects unknown,
revoked/deleted and same-reported-node members and conservatively requires the same
policy engine group. No observation writer or delete operation is exposed: safe
frontend withdrawal/fencing must precede deletion/reuse. Actual node placement and
live policy/config equivalence remain adapter gates; reported node names alone do
not establish failure domains. The initial accepted address family is IPv4 only.
T2 is not closed; API/UI and operational integration remain absent. See
`../notes/failover-model.md` for verification and review boundaries.

## T3 — API and UI

Files: `mgmt/internal/api/`, `mgmt/api/openapi.yaml`, generated API clients,
`web/src/api/`, `web/src/pages/`, `web/e2e/`.

Expose admin-authorized audited group CRUD and status. Add list/detail/edit views,
member eligibility reasons, frontend owner and desired/applied generation. Test
validation, authorization, unavailable/loading states, stale edits and real API
round trips; no healthy label derived only from configured membership.

## T4 — LB adapter, HA ownership and backend eligibility

Files: `deploy/failover/`, `mgmt/internal/failover/`, deployment container/chart files.

Use the T1-verified platform mechanism. Reconcile only owned IPVS services/network
objects; never flush global tables. Add independent frontend election/fencing,
backend health/config checks, drain and re-admission behavior, explicit empty-pool
failure, and status reporting. Verify failover with two LB instances and engine
network isolation. Test stale owner, partition, interrupted reconciliation,
malicious configuration and unsupported kernel/network rejection.

## T5 — guarded migration and rollout

Files: `deploy/helm/nexora/`, `deploy/kw/`, `deploy/kwrollout/`,
`scripts/kw-deploy.sh`, `scripts/kw-preflight.sh`, chart goldens/tests.

Stage the LB layer and backend attachments before moving either existing VIP.
Remove old advertisement ownership before activating new ownership; retain an
explicit bounded rollback. Preserve four persistent engine IDs/state and trust.
Update preflight assumptions from Service/Local to the dedicated frontend contract,
not by deleting source-IP/health checks. Keep group upgrades serialized and the
non-expiring CAS lock. Test interruption at every ownership transition.

## T6 — acceptance and operational evidence

Files: `e2e/`, `deploy/kwrollout/`, `docs/operations.md`, `deploy/kw/README.md`,
`../notes/kw-transparent-lb.md`.

Measure UDP/TCP/DoT/DoH/DoQ with two client identities, large replies, reused
connections and both backends. Validate engine policy/log attribution. Disrupt
each engine and each LB independently; prove backend failure does not move a
healthy frontend. Record packet loss/detection/recovery and connection losses
without retries hiding failures. Run strict product acceptance and Linux suites,
then Procoder review/test/check before claiming completion. Preserve all previous
red evidence; do not close bootstrap/control-plane work through this task.
