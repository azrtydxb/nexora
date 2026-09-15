# M9 T7: NexoraEngineGroup controller

Status: open
Created: 2026-09-15

## Description

Implements Task 7 of `.procoder/plans/nexora-m9-platform.md` (spec `.procoder/specs/nexora-m9-platform.md`, milestone M9 Platform). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `operator/internal/controller/enginegroup/controller_test.go`:
- [x] Register the controller in `setupControllers` in `main.go`:

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'cd operator && go vet ./internal/controller/...'`
  with only the test file → `no non-test Go files in .../enginegroup` (package missing).
- Green: `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'make operator-test'` → every package `ok`,
  `ok github.com/piwi3910/nexora/operator/internal/controller/enginegroup 117.138s`; `-v` run shows all
  nine `TestEngineGroup*` tests PASS.
- Mutation: revoking the previous token without waiting for `previousJoinTokenRevokeAt` →
  `--- FAIL: TestEngineGroupRotatesJoinToken`; reverted, suite green again.
- `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'cd operator && go vet ./... && go build ./cmd/nexora-operator'`
  → success; `gofmt -l operator` → clean.
- Plan text of Task 7 updated with an "As built" list (merge-patched status, reasons for API refusals,
  controller-clock expiry, deletion by `status.groupID`).
- Not yet committed (lead commits).
