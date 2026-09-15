# M11 T16: Configuration assistant

Status: done
Created: 2026-09-15

## Description

Implements Task 16 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `assistant_test.go` with `TestAssistantPlanValidation`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/assistant -count=1'`, expect FAIL
- [x] Implement the handlers:
- [x] Create `e2e/ai_assistant_test.go` with `TestAIConfigAssistant`, following the spec criterion:
- [x] Run the e2e test (built into a private bin dir instead of the shared `make e2e-build`) and expect PASS.
- [x] Report the paths. Commit message: `M11 T16: configuration assistant`.

## Evidence

- Red: `go test ./mgmt/internal/ai/assistant -count=1` → `no non-test Go files ... [build failed]` (package absent).
- Green: `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/assistant -count=1'` → `ok ... assistant 2.478s`.
- `go test ./mgmt/internal/api -count=1` → `ok ... api 126.959s`; `gofmt -l` on the task's files: clean;
  `go vet ./mgmt/...` fails only in `mgmt/internal/ai/upstreampred` (Task 17, not this task).
- E2E, first run (private bin dir `/tmp/t16-bin-*`, deleted afterwards): FAIL at
  `apply = {Results:[{Status:open}]}` because `licenseOperations` in `mgmt/internal/api/ai_proposals.go`
  (Task 6) omitted `createPolicyGroup`, so the operator's `acknowledge_license` was not merged and `adult`
  (enabled non-commercial `oisd-nsfw`) answered `license_acknowledgement_required`. Reported instead of
  edited (another task's file); fixed by the lead in 52b049b.
- E2E, after 52b049b: `NEXORA_E2E_BIN_DIR=$B go test ./e2e -run '^TestAIConfigAssistant$' -count=1 -v`
  → `--- PASS: TestAIConfigAssistant (5.38s)` / `ok github.com/piwi3910/nexora/e2e 5.402s`.
- Re-checked after the fix: `gofmt -l mgmt e2e` clean, `go vet ./mgmt/internal/ai/assistant
  ./mgmt/internal/api ./mgmt/cmd/nexora-mgmt ./e2e` → VET_OK, `go test ./mgmt/internal/ai/assistant
  -count=1` → ok 3.329s.
