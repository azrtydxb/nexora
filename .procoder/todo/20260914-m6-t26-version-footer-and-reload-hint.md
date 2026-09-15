# M6 T26: Version footer and reload hint

Status: open
Created: 2026-09-14

## Description

Implements Task 26 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `web/e2e/screens/34-version.spec.ts`:
- [x] Add to `e2e/kw_smoke_test.go` in `TestKwSmoke`, next to the health version check:
- [x] Run
- [x] Implement `web/src/components/VersionFooter.tsx`:
- [x] Run the same command and expect `34-version.spec.ts` to pass. Run
- [ ] Report the paths. Commit message: `gui: version footer with details and reload hint`.

## Evidence

- Red (private tree copy `/work/t26/nexora`, private `CARGO_TARGET_DIR=/work/t26/target`, `BIN=/work/t26/bin`, dev pod `toolbox`):
  `NEXORA_COMMIT=e2e0000000000000000000000000000000000000 make e2e-build BIN=/work/t26/bin CARGO_TARGET_DIR=/work/t26/target && NEXORA_E2E_BIN_DIR=/work/t26/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m`
  -> `34-version.spec.ts` FAIL: `getByTestId('version-footer')` element(s) not found (also failing: 26-querylog-reason, 31-upstream-parallel, both other agents' in-progress tasks).
- Green (same command, fresh copy with the implementation): Playwright `41 passed (3.1m)`, including
  `✓ e2e/screens/34-version.spec.ts › sidebar shows the version, details and the reload hint on a mismatch`.
  `TestGUICoverage` still FAILs only on `18 OpenAPI operations have no covering Playwright test`, all M11 AI
  operations (`getAiStatus`, `listAiProposals`, ...) from the committed M11 T2 contract whose GUI is later M11 work;
  `getVersion` is covered. Private dirs deleted afterwards.
- `cd web && pnpm run typecheck && pnpm run lint` -> pass (`permission parity: 138 operations match`).
- `go vet ./e2e` in the pod -> clean; `gofmt -l e2e/kw_smoke_test.go` -> clean. The kw smoke subtest
  `version-footer-matches-health` is not run here (needs the kw deployment; Task 34).
- Not committed (lead commits).

