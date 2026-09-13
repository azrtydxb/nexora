# M5 Task 7: Fleet HTTP API and the fleet e2e harness

Status: implemented (awaiting lead commit)
Created: 2026-09-13

## Description

Implement Task 7 ("Fleet HTTP API and the fleet e2e harness") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 7 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [x] `TestAuthRBACAuditOIDC` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestFleetAPI` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestFleetAPIHelpersMatchOpenAPI` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestFleetPermissions` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestGUICoverage` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestMgmtStatelessHA` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- `go test ./mgmt/internal/auth/ -run TestFleetPermissions` -> FAIL `listEngineGroups has no permission entry` before the entries, `ok` after.
- `scripts/dev-exec.sh 'make mgmt-test'` -> every package `ok` (api 121s, control 62s, fleet, rollout, snapshot, store, zone ...), exit 0; `go test -race ./mgmt/internal/api/` rerun after the prettier-formatted spec regeneration -> `ok`.
- `make e2e-build` -> exit 0; `NEXORA_E2E_BIN_DIR=$PWD/bin go test ./e2e/ -run TestFleetAPI -v` -> `--- PASS: TestFleetAPI (2.92s)`; same run: `--- PASS: TestAuthRBACAuditOIDC`, `--- PASS: TestMgmtStatelessHA`, and every other e2e test (TestRPZPolicy, TestSafeSearchRewrites, ...) passes; TestKwSmoke/TestKwSmokeM4 skip (no kw env).
- `TestFleetAPIHelpersMatchOpenAPI`: no such test exists in the plan or repo (stale criterion); harness paths are exercised by TestFleetAPI.
- `TestGUICoverage`: FAIL as expected until Task 11 (GUI): createEngineGroup, deleteEngineGroup, getEngineGroup, getEngineStats, getFleetSummary, getRollout, listEngineGroups, listRollouts, resumeEngineGroupRollouts, revokeEngine, rollbackEngineGroup, rotateEngineCertificate, updateEngine, updateEngineGroup.
- `pnpm run lint` -> `permission parity: 110 operations match`; `pnpm run typecheck` clean; `procoder check` 0 blocking (schema.d.ts generated file unformatted as at HEAD).
- Not committed (lead commits).

