# M11 T15: Filter and policy recommendations agent

Status: open
Created: 2026-09-15

## Description

Implements Task 15 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `aggregate_test.go` with `TestAggregateGroupStats`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/filterrec -run TestAggregate -count=1'`, expect
- [x] Create `agent_test.go` with `TestFilterRecommendationAgent`, following the spec criterion. The
- [x] Add the registration file. Run
- [ ] Report the paths. Commit message: `M11 T15: filter and policy recommendation agent`.

## Evidence

- `aggregate_test.go` `TestAggregateGroupStats` written first; `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/filterrec -run TestAggregate -count=1'` -> FAIL `undefined: filterrec.Aggregate` (also GroupStats, NameCount); after `aggregate.go` -> `ok`.
- `agent_test.go` `TestFilterRecommendationAgent` run without `agent.go` -> FAIL `undefined: filterrec.Agent`; with `agent.go` -> `--- PASS: TestFilterRecommendationAgent (4.00s)`. It covers: 3 open proposals; enable impact 30 of 110 = 27.3% while the model sent 999999; block is `appendAiRpzRules` nxdomain `tracker.aif.test`; safe search is `updatePolicyGroup` youtube strict; a 2nd run gives 3 rows (3 refreshed); after Dismiss a 3rd run gives 3 rows, 2 open, 1 suppressed; 3 model calls.
- `scripts/dev-exec.sh 'gofmt -l mgmt && go vet ./mgmt/... && go test ./mgmt/internal/ai/filterrec -count=1'`: `go vet ./mgmt/...` failed only in another agent's unfinished `mgmt/internal/ai/upstreampred/agent.go:160` (Task 17: `proposal.Upsert` returns 3 values). Re-run excluding it: `gofmt -l mgmt` printed nothing, `go vet $(go list ./mgmt/... | grep -v /ai/upstreampred)` gave `VETOK`, `go build ./mgmt/cmd/nexora-mgmt` succeeded, and `go test ./mgmt/internal/ai/filterrec -count=1` gave `ok ... 8.608s`.
- Remaining: the lead commits the paths.
