# M6 T24: Navigation shell: Forwarding & recursion, Filtering group, help routes, document titles

Status: open
Created: 2026-09-14

## Description

Implements Task 24 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `web/e2e/screens/28-navigation.spec.ts`:
- [x] Run
- [x] Implement in `web/src/components/layout/AppShell.tsx`:
- [x] In `web/src/app/router.tsx`:
- [x] Run the same command and expect specs 02, 04, 12, 15, 17, 21, 22 and 28 to pass, and
- [ ] Report the paths. Commit message: `gui: Forwarding & recursion, collapsible Filtering menu, help routes`.

## Evidence

Both runs in a private pod copy (`/work/t24-nav`: HEAD 49cca08 + this task's files, own bin and
target dirs; deleted afterwards) because the shared tree carries other tasks' in-progress engine work.

- Red (HEAD app + new/updated specs):
  `make e2e-build && NEXORA_E2E_BIN_DIR=/work/t24-nav/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m`
  -> `5 failed, 29 passed`: 02, 17, 21 time out `waiting for getByTestId('nav-resolution')`, 28 fails on
  `nav-resolution`; 30-access-control-split fails `missing environment variable NEXORA_E2E_ACL_ZONE`
  (Task 16's seed, not yet built).
- Green (with the implementation):
  `CARGO_TARGET_DIR=/work/t24-nav/target make e2e-build && NEXORA_E2E_BIN_DIR=/work/t24-nav/bin go test ./e2e -run 'TestGUICoverage|TestAuthRBACAuditOIDC' -count=1 -timeout 45m -v`
  -> `--- PASS: TestAuthRBACAuditOIDC (17.67s)`; TestGUICoverage `1 failed, 33 passed`: 02, 04, 12, 15, 17,
  21, 22, 28 and all others pass; the only failure is 30-access-control-split
  (`NEXORA_E2E_ACL_ZONE`, Task 16), so `--- FAIL: TestGUICoverage` until Task 16 lands.
- `cd web && pnpm run typecheck && pnpm run lint` -> pass (`permission parity: 120 operations match`).
- Plan text updated: `NavLeaf.op` is optional (Help has none); the open state opens on entering a child
  route and can still be collapsed there, instead of being pinned open.
