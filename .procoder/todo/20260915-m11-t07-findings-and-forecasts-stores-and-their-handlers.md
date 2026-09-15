# M11 T7: Findings and forecasts stores and their handlers

Status: open
Created: 2026-09-15

## Description

Implements Task 7 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/ai/finding/finding_test.go` with `TestSyncLifecycle`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/finding -count=1'` and expect FAIL:
- [x] Create `mgmt/internal/ai/forecast/forecast_test.go` with `TestLatestPerSubject`: two forecasts for
- [x] Add `TestExplainKeepsDetectorSeverity` to `finding_test.go`.
- [x] Create `mgmt/internal/api/ai_findings_test.go` with `TestFindingHandlers` over `NewHandler`, with
- [ ] Run `scripts/dev-exec.sh 'gofmt -l mgmt && go vet ./mgmt/... && go test ./mgmt/internal/ai/... ./mgmt/internal/api/... -count=1'` and expect PASS (in the shared tree: blocked on Task 6's `Deps.AI`; verified on a pod copy, see Evidence).
- [x] Report the paths. Commit message: `M11 T7: AI findings and forecasts`.

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/finding ./mgmt/internal/ai/forecast -count=1'` →
  `no non-test Go files` / `[build failed]` for both packages (nothing defined yet).
- Green: the same command → `ok .../ai/finding 10.833s`, `ok .../ai/forecast 5.702s`.
- Red: `scripts/dev-exec.sh 'go test ./mgmt/internal/api -run TestFindingHandlers -count=1'` →
  `d.AI undefined (type *api.Deps has no field or method AI)` (Task 6 adds `Deps.AI`, not yet in the tree).
- Green (pod copy `/tmp/t7-*` of the synced tree, with Task 6's planned `server.go` change applied only
  there: `Deps.AI *AIRuntime` and `aiRuntime()` returning it): `gofmt -l mgmt` empty, `go vet ./mgmt/...`
  clean, `go test ./mgmt/internal/ai/... ./mgmt/internal/api/... -count=1` → `ok .../ai 59.356s`,
  `ok .../ai/finding 16.950s`, `ok .../ai/forecast 10.490s`, `ok .../ai/proposal 17.232s`,
  `ok .../api 198.395s`.
- Shared tree after Task 6's `server.go` landed: `scripts/dev-exec.sh 'go test ./mgmt/internal/api -run TestFindingHandlers -count=1 && go test ./mgmt/internal/ai/finding ./mgmt/internal/ai/forecast -count=1'` → `ok .../api 7.455s`, `ok .../ai/finding 5.431s`, `ok .../ai/forecast 2.916s`. The full `./mgmt/...` run in the shared tree is left to the lead because Tasks 5, 6 and 8 are still editing.

