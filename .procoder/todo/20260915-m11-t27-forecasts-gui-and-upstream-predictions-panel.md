# M11 T27: Forecasts GUI and upstream predictions panel

Status: open
Created: 2026-09-15

## Description

Implements Task 27 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `e2e/gui_seed_ai_forecasts_test.go`. With `harness.PGExec` it inserts:
- [x] Create `web/e2e/screens/55-ai-forecasts.spec.ts`. At 1280 and 400 px as viewer:
- [x] Run the Task 24 commands. Expect FAIL, implement, and expect specs 55, 02 and 17 to PASS.
- [ ] Report the paths. Commit message: `M11 T27: AI forecasts GUI` (the lead commits).

## Evidence

Built and tested in a private copy of the tree in the `toolbox` dev pod (`/work/t27`, its own
`bin/`), deleted afterwards, so the concurrent M11 GUI agents do not share build outputs. Specs
`53-ai-recommendations` and `54-ai-assistant`, which belong to the agents working Tasks 25 and 26 and
were picked up by the sync from the shared checkout, were removed from that private copy.

- `cd web && pnpm run typecheck && pnpm run lint` in `/work/t27`: passed —
  `permission parity: 138 operations match`, `check-help: 166 controls on 46 pages have help`.
  `gofmt -l e2e/` clean, `go vet ./e2e/` clean.
- RED, with the stub `AiForecastsPage`: `go test ./e2e -run TestGUICoverage -count=1` →
  `✘ 52 e2e/screens/55-ai-forecasts.spec.ts:5:3 › AI forecasts at 1280px`, `✘ 53 … at 400px`, both
  `expect(getByTestId('ai-forecast-e662a0bf-…')).toContainText("degrading") … element(s) not found`
  — the seed's forecast rows exist, the page renders no card.
- An intermediate run caught a real defect in the seed, not in the page: `ai-capacity-days` read
  `no exhaustion in sight` because `50-ai-status.spec.ts` runs the `capacity_forecast` agent, whose
  newer `filter_index` row wins `forecast.Latest`. The seed moved to `recursor_cache`, the one
  resource that agent does not sample, and carries a `debt:` for when M7 Task 12 changes that.
- GREEN: `go test ./e2e -run TestGUICoverage -count=1 -timeout 40m` →
  `✓ 48 e2e/screens/55-ai-forecasts.spec.ts:5:3 › AI forecasts at 1280px (2.5s)`,
  `✓ 49 … at 400px (2.3s)`, `✓ 2 e2e/screens/02-upstreams.spec.ts`,
  `✓ 19 e2e/screens/17-resolution.spec.ts`; `3 failed, 49 passed (3.8m)`.
  The three are not this task's: `34-version` (failed identically in the RED run) and
  `60-ai-viewer` ×2, which needs Task 25's recommendations page. `TestGUICoverage` itself still
  reports FAIL, as expected while the other wave-4 tasks' operations are uncovered.
