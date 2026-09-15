# M6 T27: Parallel strategy in Settings

Status: open
Created: 2026-09-14

## Description

Implements Task 27 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `web/e2e/screens/31-upstream-parallel.spec.ts`:
- [x] Run (red)
- [x] In `web/src/pages/SettingsPage.tsx`:
- [x] Run the same command and expect `09-settings.spec.ts` and `31-upstream-parallel.spec.ts` to pass
- [ ] Report the paths. Commit message: `gui: parallel upstream strategy setting`. (lead commits)

## Evidence

Runs used a private tree copy in the `toolbox` pod (`/work/t27`, `CARGO_TARGET_DIR=/work/t27-target`,
bin `/work/t27/bin`; deleted afterwards):
`make e2e-build && NEXORA_E2E_BIN_DIR=/work/t27/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m`

- Red (before the page change): `✘ 31-upstream-parallel.spec.ts` with
  `waiting for getByTestId('settings-strategy-parallel')` / `Test timeout of 60000ms exceeded`.
- Green: `✓ 09-settings.spec.ts` (2.6s), `✓ 31-upstream-parallel.spec.ts` (3.5s); `40 passed, 1 failed`.
  The one failure is `34-version.spec.ts` (M6 Task 26, in progress by another agent, not this task), so
  `TestGUICoverage` as a whole was FAIL at the time.
- Laptop `cd web && pnpm run typecheck && pnpm run lint`: clean (`permission parity: 138 operations match`);
  prettier `--check` on both files clean.
