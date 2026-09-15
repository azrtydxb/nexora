# M6 T22: Engine modal with metrics, logs and queries

Status: open
Created: 2026-09-14

## Description

Implements Task 22 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `e2e/engine_logs_test.go` (and `e2e/gui_seed_engines_test.go`, `web/e2e/screens/32-engine-modal.spec.ts`):
- [x] Run (red)
- [x] Implement `web/src/components/EngineModal.tsx`:
- [ ] Run the same command and expect PASS for `TestEngineLogsFromRealEngine`, `05-engines`,
      `20-fleet` and `32-engine-modal` (all four pass; `TestGUICoverage` as a whole still fails on
      spec 30, see Evidence). `pnpm run typecheck && pnpm run lint` pass.
- [ ] Report the paths. Commit message: `gui: engine modal with metrics, logs and queries`.

## Evidence

Verified in a private copy in the dev pod (`/work/nexora-t22` = HEAD 49cca08 + the Task 22 files, own
`CARGO_TARGET_DIR`/bin dir, deleted afterwards) because the shared tree carries other tasks' in-progress work.

- Red: `make e2e-build CARGO_TARGET_DIR=/work/target-t22 BIN=/work/bin-t22 && NEXORA_E2E_BIN_DIR=/work/bin-t22 go test ./e2e -run "TestEngineLogsFromRealEngine|TestGUICoverage" -count=1 -timeout 60m`
  - both spec 32 tests failed waiting for `engine-modal-open-gui-engine-3` / `-4` (expected).
  - `TestEngineLogsFromRealEngine` failed on "serving version": a managed engine logs
    `nexora-engine: applied version N` (engine/src/control.rs:885); test and spec switched to
    "applied version", plan text updated.
- Green (same command): `✓ 05-engines`, `✓ 20-fleet`, `✓ 32-engine-modal` (both tests), 34 passed, 1 failed:
  `30-access-control-split.spec.ts` "missing environment variable NEXORA_E2E_ACL_ZONE" — no seed in
  HEAD sets it (Task 20's, fails identically in the red run without Task 22), so the coverage assertion
  after Playwright did not run.
- `go test ./e2e -run '^TestEngineLogsFromRealEngine$' -count=1 -v` -> `--- PASS: TestEngineLogsFromRealEngine (6.04s)`.
- `cd web && pnpm run typecheck && pnpm run lint` -> pass (`permission parity: 120 operations match`, check-help ok).
- `gofmt -l` clean, `go vet ./e2e/` ok; prettier applied to the web files.
