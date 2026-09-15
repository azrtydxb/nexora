# M11 T11: GUI foundation for AI

Status: open
Created: 2026-09-15

## Description

Implements Task 11 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `web/e2e/screens/50-ai-status.spec.ts`:
- [x] Create `web/e2e/screens/59-ai-disabled.spec.ts`:
- [x] In `e2e/gui_test.go` start `ai := env.StartOpenAIFixture()` before mgmt, append
- [x] Implement the hooks, components, pages, routes, nav group and help area.
- [ ] Run `cd web && pnpm run typecheck && pnpm run lint`, then (typecheck and lint pass; spec 59 passes; spec 50 blocked on Task 20 at the Run click)
- [ ] Report the paths. Commit message: `M11 T11: GUI foundation for AI`.

## Evidence

- Red (private pod copy before implementation, `make e2e-build && go test ./e2e -run '^TestGUICoverage$' -count=1`):
  `4 failed, 40 passed` — 50 (both widths) fail on missing `ai-status-model`, 59 on missing `ai-off`;
  34-version also failed (not this task).
- `cd web && pnpm run typecheck && pnpm run lint`: pass (`permission parity: 138 operations match`,
  `check-help: 162 controls on 45 pages have help`).
- Green run (private copy of the tree including Task 9's uncommitted wiring): `5 failed, 39 passed`.
  - 59-ai-disabled: PASS.
  - 50-ai-status (1280 and 400): model `fake-qwen`, endpoint `127.0.0.1`, no key text, budget and the
    `ai-agent-capacity_forecast` row pass; the `ai-agent-run` button is disabled because no
    `capacity_forecast` agent is registered yet (Task 9 reports it `enabled: false`, `runAiAgent` 404),
    so the 202 wait times out. Needs Task 20.
  - 16-dnssec, 17-resolution: strict-mode `getByLabel` collisions with new HelpTips from the
    concurrent M6 help tasks; 34-version: also failed in the red run. Not this task.
  - The coverage assertion did not run (Playwright failed first), so the uncovered AI operation list
    was not recorded.
