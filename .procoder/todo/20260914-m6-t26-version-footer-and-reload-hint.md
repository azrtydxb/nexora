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


## Follow-up (2026-09-15): 34-version failed in the full run

Root cause: the hint compared only commits, and `web/vite.config.ts` stamps `__NEXORA_COMMIT__` from
`NEXORA_COMMIT`, which `make e2e-build` does not set (the T26 evidence run set it by hand). With an
empty GUI commit the mismatch test was always false, so `version-reload-hint` never rendered.
`VersionFooter` now treats a differing version as a mismatch too (both stamps come from the same
image build; an unstamped, empty stamp still never counts), so the spec's mocked `v9.9.9` against the
GUI build's `dev` shows the hint on an unstamped build as well.

Evidence (private pod copy `/work/qlfix`): full `TestGUICoverage` ->
`✓ 34-version.spec.ts › sidebar shows the version, details and the reload hint on a mismatch (3.0s)`;
red before the change in the same tree (`getByTestId('version-reload-hint')` element(s) not found).
`cd web && pnpm run typecheck && pnpm run lint` -> clean, `permission parity: 138 operations match`.
