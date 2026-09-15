# M11 T14: Dashboard insight agent

Status: open
Created: 2026-09-15

## Description

Implements Task 14 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/ai/insight/detect_test.go` with `TestInsightDetectors` (and `TestInsightScore`).
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/insight -count=1'` and expect FAIL, implement `detect.go`, expect PASS.
- [x] Create `agent_test.go` with `TestDashboardInsightCorrelation`, using the fake provider; implement `agent.go`.
- [x] Implement `GetAiInsights` with `TestGetAiInsights`.
- [ ] Report the paths. Commit message: `M11 T14: dashboard insight agent` (the lead commits).

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/insight -count=1'` with a package-clause-only
  `detect.go` → `undefined: insight.Detect` / `undefined: insight.Score` (build failed). Green after
  `detect.go`: `ok github.com/piwi3910/nexora/mgmt/internal/ai/insight 2.366s`.
- Red: `go test ./mgmt/internal/ai/insight -run Correlation` → `undefined: insight.Agent`. Green after
  `agent.go`: `ok ... insight 6.142s`.
- Red: `go test ./mgmt/internal/api -run TestGetAiInsights` → `empty insights = 501`. Green after the handler.
- `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/insight ./mgmt/internal/api -run "Insight" -count=1 && go vet ./mgmt/internal/ai/insight ./mgmt/internal/api ./mgmt/cmd/nexora-mgmt && go build -o /dev/null ./mgmt/cmd/nexora-mgmt'`
  → `ok .../ai/insight 15.454s`, `ok .../api 19.090s`, vet and build clean.
- `gofmt -l` clean; `golangci-lint run ./mgmt/internal/ai/insight/...` → `0 issues.`
- Plan deviations recorded in the Task 14 text: EngineMetrics over 24 h (not 1 h), 15% SERVFAIL in the
  detector test (8% is below the 10% critical rule), engine subject is the node name, `llm_at`
  recovered from `ai_agent_runs`, derived `engines` detail.
