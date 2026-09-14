# M6 T20: Access control GUI and zone allow-query editor

Status: done (pending commit)
Created: 2026-09-14

## Description

Implements Task 20 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `web/e2e/screens/30-access-control-split.spec.ts`:
- [x] Run
- [x] Implement `web/src/pages/AccessControlPage.tsx`:
- [x] In `web/src/pages/ZoneTransfersTab.tsx`, add to the form a "Query access" group before
- [x] Run the same command and expect `03`, `18`, `19` and `30` to pass. Run
- [x] Report the paths. Commit message: `gui: separate recursion and authoritative access, zone allow-query`.

## Evidence

- Red: pod copy with HEAD pages + new specs (engine lib in /work/nexora did not compile at the time
  because of another agent's WIP, so bin/nexora-engine from the earlier build was reused),
  `NEXORA_E2E_BIN_DIR=<copy>/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m`:
  `03-access-control` and `30-access-control-split` FAIL on
  `getByRole('heading', { name: 'Recursion and resolver access' })` element(s) not found; 31 passed.
- Green: same run with the new pages: `33 passed (4.3m)` including 03, 18 (both), 19 and 30.
  `TestGUICoverage` itself still FAILs only on 6 uncovered operations owned by later tasks
  (getDashboardHealth/Series/Top, getEngineLogs, getEngineMetrics, getVersion).
- Spec 30 needs `NEXORA_E2E_ACL_ZONE`, seeded by Task 16's `e2e/gui_seed_access_test.go` (wave 3).
  Both runs used a private pod copy (`/tmp/t20-*`, since removed) with that seed added exactly as Task
  16 specifies; the shared checkout does not contain it, so spec 30 fails in the shared tree until
  Task 16 lands.
- `cd web && pnpm run typecheck` clean; `pnpm run lint`: eslint clean, `permission parity: 120
operations match`, check-help OK.
