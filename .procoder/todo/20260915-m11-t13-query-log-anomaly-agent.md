# M11 T13: Query-log anomaly agent

Status: open
Created: 2026-09-15

## Description

Implements Task 13 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/ai/anomaly/detect_test.go` with the five detector tests. Each builds records
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/anomaly -run Detector -count=1'` and expect
- [x] Create `mgmt/internal/ai/anomaly/agent_test.go` with `TestAnomalyAgentLLMOnlyOnChange`:
- [x] Add the registration file, then create `e2e/ai_anomaly_test.go` with `TestAIQueryLogAnomalies`:
- [x] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e -run TestAIQueryLogAnomalies -count=1 -v'` and expect PASS.
- [ ] Report the paths. Commit message: `M11 T13: query log anomaly agent`.

## Evidence

- Detectors: `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/anomaly -run Detector -count=1'` first FAILED
  (`no non-test Go files in .../ai/anomaly`, i.e. `anomaly.Detect` undefined), then after `detect.go`:
  `ok github.com/piwi3910/nexora/mgmt/internal/ai/anomaly` (5 detector tests plus TestEntropyAndRegistrableParent PASS).
- Agent: `agent_test.go` failed to compile before `agent.go` (`undefined: anomaly.Agent`); after:
  `--- PASS: TestAnomalyAgentLLMOnlyOnChange (6.96s)`, `--- PASS: TestAnomalyAgentOutcomes (4.39s)`.
  Mutation: dropping the pending-change memory (`a.pending = changed && ...`) fails run 4
  (`Outcome:no_change`); restored.
- E2E (private bin dir `/work/tmp-m11t13-bin`, shared engine binary copied, dir deleted afterwards):
  `NEXORA_E2E_BIN_DIR=$B go test ./e2e -run "TestAIQueryLogAnomalies|TestAIDisabledChangesNothing" -count=1 -v`
  → `--- PASS: TestAIQueryLogAnomalies (7.81s)`, `--- PASS: TestAIDisabledChangesNothing (66.59s)` (the
  `agentRegistered` condition removed, the agent-run check now always runs).
- `go test ./mgmt/cmd/nexora-mgmt/ ./mgmt/internal/ai/... -count=1` → every ai package ok (cmd has no test files; it builds); `gofmt -l` clean; `go vet` on
  `./e2e/ ./mgmt/cmd/nexora-mgmt/ ./mgmt/internal/ai/anomaly/` clean.
- Deviation (plan text updated): detector severities are fixed per type, so the "call after a severity
  change" case is exercised as a candidate-set change (new `nxdomain_burst` id) held back by
  MinLLMInterval and explained on the next allowed run. `Agent` gained `Interval`.
- Remaining: commit by the lead (`M11 T13: query log anomaly agent`).
