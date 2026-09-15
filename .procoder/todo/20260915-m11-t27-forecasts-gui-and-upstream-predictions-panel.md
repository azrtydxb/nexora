# M11 T27: Forecasts GUI and upstream predictions panel

Status: open
Created: 2026-09-15

## Description

Implements Task 27 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create `e2e/gui_seed_ai_forecasts_test.go`. With `harness.PGExec` it inserts:
- [ ] Create `web/e2e/screens/55-ai-forecasts.spec.ts`. At 1280 and 400 px as viewer:
- [ ] Run the Task 24 commands. Expect FAIL, implement, and expect specs 55, 02 and 17 to PASS.
- [ ] Report the paths. Commit message: `M11 T27: AI forecasts GUI`.

## Evidence

Built and tested in a private copy of the tree in the `toolbox` dev pod (`/work/t27`, its own
`bin/`), deleted afterwards, so the concurrent M11 GUI agents do not share build outputs.

- `cd web && pnpm run typecheck && pnpm run lint` (in `/work/t27`): passed —
  `permission parity: 138 operations match`, `check-help: 165 controls on 46 pages have help`
  (168 after the page's kind select landed, checked on the laptop).
- RED, with the stub `AiForecastsPage`:
  `go test ./e2e -run TestGUICoverage -count=1` →
  `✘ 52 e2e/screens/55-ai-forecasts.spec.ts:5:3 › AI forecasts at 1280px`,
  `✘ 53 … at 400px`, both
  `expect(getByTestId('ai-forecast-e662a0bf-…')).toContainText("degrading") … element(s) not found`
  — the seed's forecast rows exist, the page renders no card.
- GREEN: see the run recorded in the final report.
