# M11 T17: Upstream health prediction agent

Status: open
Created: 2026-09-15

## Description

Implements Task 17 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `trend_test.go` with `TestUpstreamTrendFeatures`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/upstreampred -run TestUpstreamTrend -count=1'`,
- [x] Create `agent_test.go` with `TestUpstreamPredictionAgent`:
- [x] Add the registration file. Run `scripts/dev-exec.sh 'gofmt -l mgmt && go vet ./mgmt/... && go test ./mgmt/internal/ai/upstreampred -count=1'` and expect PASS.
- [ ] Report the paths. Commit message: `M11 T17: upstream health prediction agent`.

## Evidence

- `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/upstreampred -run TestUpstreamTrend -count=1'` before trend.go: FAIL `undefined: upstreampred.Compute`; after: `ok github.com/piwi3910/nexora/mgmt/internal/ai/upstreampred 0.017s`.
- `TestUpstreamPredictionAgent` before agent.go: FAIL `undefined: upstreampred.Agent`; after: PASS.
- Mutation: removing the only-enabled-upstream check fails the test (`model calls: 2, want 3 (disable re-asked)`); restored.
- `scripts/dev-exec.sh 'gofmt -l mgmt && go vet ./mgmt/... && go test ./mgmt/internal/ai/upstreampred -count=1'`: no gofmt output, vet clean, `ok github.com/piwi3910/nexora/mgmt/internal/ai/upstreampred 2.570s`.
- Not committed (lead commits: `M11 T17: upstream health prediction agent`).

