# FG-02 pure eligibility contract

This slice adds `EvaluateEligibility` in `mgmt/internal/failover/eligibility.go`
and focused tests. It has no production caller, IO, backend writer, frontend
ownership authority or deployment effect. No task/sprint closure is implied.

## Gap addressed

Desired group admission checks self-reported engine node names and policy group
membership. Those values cannot prove physical failure-domain separation or that
the running engines have applied equivalent effective configuration. The new
helper requires explicit trusted platform binding and observed configuration.

## Contract

- Exactly the two desired persistent UUIDs must appear, once each. Extra, missing
  or duplicate evidence denies the entire pool. Output follows desired member
  order and an empty eligible pool is explicit, never a fallback resolver.
- The trusted adapter must bind each persistent engine UUID to its current pod UID
  and real platform node UID and/or name. This initial Kubernetes contract uses
  canonical nonzero UUID UIDs and canonical DNS node names. A valid name cannot
  mask a malformed UID. Engine-reported `node_name` is not a source of proof.
- Both placements must be fresh and comparable. Equal node UIDs, equal node names,
  reused pod UIDs, conflicting aliases or incomparable UID-only/name-only evidence
  deny both members. UID/name identifiers must describe physical failure domains
  within the same platform, not virtual names for the same host.
- Inventory, binding, readiness, management, direct backend DNS and applied snapshot
  observations have independent freshness checks. A zero timestamp or any future
  timestamp is invalid. The inclusive maximum-age boundary is accepted; the caller
  supplies a positive age budget and explicit current time.
- Missing/stale placement denies the pair even when one partner's health is good.
  With both placements proven, revocation, deletion, stale inventory, failed/stale
  health checks or an unapplied snapshot remove only the affected member. A valid
  partner can remain eligible. If both fail, `empty_pool` is reported.
- Policy UUIDs must be nonzero and equal, preserving conservative admission.
  Both target digests must be valid lowercase SHA-256 and equal. Each eligible
  member must acknowledge its exact positive target version and content digest.
  Different publication versions across members are permitted only with equal
  content. Digests use `snapshot.ContentDigest`, covering effective policy and
  authoritative configuration; equal versions or policy IDs alone do not suffice.
- Stable denial codes identify individual and pair failures. Invalid structural
  input stops evidence evaluation. Other checks accumulate deterministic reasons.

## Adapter integration pending

The adapter must authenticate the UUID-to-pod binding using trusted platform and
management data, resolve actual physical node placement, read revocation/deletion
and current policy/target state, and associate health/config observations with the
same pod incarnation. Discard old observations after pod changes. Resolve applied
content from an acknowledged version and trusted snapshot storage, never infer it
from the desired target or readiness. Direct DNS must probe the bound backend,
not the group VIP. The helper validates supplied values; it cannot authenticate
arbitrary caller-provided structs or verify a digest's provenance.

The adapter must reevaluate and expire decisions on changes/timeouts, select
measured freshness budgets, and enforce an empty pool without cross-group spill.
No prior placement cache or partition grace is implemented: losing either trusted
binding eventually denies the pair. Recovery/availability tradeoffs require the
future adapter's evidence lifecycle. This helper does not prove original source
IP **and port**, transparent replies, transport coverage, VIP ownership/fencing,
HA, safe migration or cross-host datapath behavior. Those gates remain pending.

## Verification

Local commands used `GOCACHE=/tmp/nexora-eligibility-go-cache GOPROXY=off`:

- `go test ./mgmt/internal/failover -run 'TestEligibility|TestValidation|TestStatus' -count=1`: passed.
- `go test -race ./mgmt/internal/failover -run 'TestEligibility|TestValidation|TestStatus' -count=1 -coverprofile=/tmp/nexora-eligibility-coverage.out`: passed (1.876s).
- `go vet ./mgmt/internal/failover`: passed.
- `go tool cover -func=/tmp/nexora-eligibility-coverage.out`: all six functions in
  `eligibility.go` have 100% statement coverage. Package coverage is 53.8%; these
  focused runs deliberately do not execute database tests.
- `gofmt -l` on both Go files and `git diff --no-index --check /dev/null` on each
  new file: clean. Implementation and tests were reread for adversarial cases;
  fresh health cannot rehabilitate an expired binding, and malformed/default
  evidence cannot admit a backend.

Linux and PostgreSQL integration tests remain parent-owned. No goldens, dependency
files, trust material or regression expectations were modified. Procoder report
/check/test tools are unavailable in this session; procoder was not invoked via
shell, as instructed.
