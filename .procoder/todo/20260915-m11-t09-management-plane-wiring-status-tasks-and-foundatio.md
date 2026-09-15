# M11 T9: Management plane wiring, status, tasks and foundation e2e

Status: open
Created: 2026-09-15

## Description

Implements Task 9 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/api/ai_status_test.go` with `TestAiStatusAndTasks` over `NewHandler`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api -run TestAiStatusAndTasks -count=1'` and expect
- [x] Implement `ai_status.go`, `ai_tasks.go` and the `AIRuntime` fields.
- [x] Create `mgmt/cmd/nexora-mgmt/ai.go` and wire `serve` in `main.go` after the query-log backend is
- [x] Create `e2e/ai_foundation_test.go` with three tests.
- [x] Run
- [x] Report the paths. Commit message: `M11 T9: wire AI into nexora-mgmt, status and tasks`.

## Evidence

- `scripts/dev-exec.sh 'go test ./mgmt/internal/api -run TestAiStatusAndTasks -count=1'` before the handlers: `FAIL ... status while off = 501 {"code":"not_implemented","message":"getAiStatus is not implemented yet"}`; after: `ok github.com/piwi3910/nexora/mgmt/internal/api 3.103s`.
- `scripts/dev-exec.sh 'go build ./mgmt/... && go vet ./mgmt/...'`: success.
- `scripts/dev-exec.sh 'go test ./mgmt/internal/api/... ./mgmt/cmd/... ./mgmt/internal/ai/... -count=1'`: `ok mgmt/internal/api 355.565s`, `ok mgmt/internal/ai 56.740s`, finding, forecast, proposal ok.
- Private e2e build (engine `CARGO_TARGET_DIR=/work/target-m11t9`, mgmt and fixture into `/work/bin-m11t9`, `go vet ./e2e/`), then `NEXORA_E2E_BIN_DIR=/work/bin-m11t9 go test ./e2e -run "TestAIDisabledChangesNothing|TestApplyAiProposalsReplaysThroughAPI|TestAIOpenAICompatibleWire" -count=1 -v`: `SKIP TestAIOpenAICompatibleWire (needs M11 Task 12)`, `PASS TestAIDisabledChangesNothing (70.44s)`, `PASS TestApplyAiProposalsReplaysThroughAPI (7.59s)`, `ok github.com/piwi3910/nexora/e2e 83.102s`. Private binaries and target dir deleted afterwards.
- Deviation (plan updated): `AIRuntime.Agents` (registered agents) added; status reports only registered, configured-on agents as enabled and `runAiAgent` answers 404 for unregistered agents. The fixture-contact positive path of `TestAIDisabledChangesNothing` is conditional on `querylog_anomalies` being registered until Task 13; the status and metrics positive path always runs.
- Not committed (the lead commits).

