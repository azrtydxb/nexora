# M11 T28: Rollout risk GUI

Status: open
Created: 2026-09-15

## Description

Implements Task 28 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `e2e/gui_seed_ai_rollout_risk_test.go`:
- [x] Create `web/e2e/screens/56-ai-rollout-risk.spec.ts`. At 1280 and 400 px as viewer, open
- [x] Run the Task 24 commands. Expect FAIL, implement, and expect specs 56 and 20 to PASS.
- [ ] Report the paths. Commit message: `M11 T28: rollout risk GUI`.

## Evidence

- `cd web && pnpm run typecheck` (laptop): clean. `pnpm exec eslint` on the three web files: clean.
  `gofmt -l` and `go vet ./e2e/`: clean.
- Red (private pod tree = `git archive HEAD` + seed + spec, `CARGO_TARGET_DIR=/work/target make
web-build e2e-build && NEXORA_E2E_BIN_DIR=... go test ./e2e -run TestGUICoverage -count=1`):
  `2 failed, 54 passed`; both failures are 56-ai-rollout-risk at line 17 (`ai-risk-score` not found).
- Green (same tree + `RolloutRiskCard.tsx` and the `RolloutPage.tsx` mount): `1 failed, 57 passed`;
  `56-ai-rollout-risk` 1280 and 400 px pass, `20-fleet` passes. The one failure is
  `54-ai-assistant` (Task 26's spec) waiting for the transient `ai-task-status` "Thinking"; it passed
  in the red run and touches no Task 28 file. Because Playwright failed, the operation coverage
  assertion did not run in the green run.
- Private pod tree and logs deleted afterwards.
