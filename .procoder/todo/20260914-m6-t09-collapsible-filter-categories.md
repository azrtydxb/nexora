# M6 T9: Collapsible filter categories

Status: open
Created: 2026-09-14

## Description

Implements Task 9 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Rewrite the start of the first test in `web/e2e/screens/22-filter-categories.spec.ts` so that,
- [x] Run
- [x] Implement in `web/src/pages/FilterCategoriesPage.tsx`:
- [x] Run the same `TestGUICoverage` command and expect `22-filter-categories.spec.ts` to pass. Run
- [ ] Report the paths. Commit message: `gui: collapsible filter categories`.

## Evidence

- Red: the main pod tree `/work/nexora` did not build (other wave-1 agents' engine work in progress:
  `NO_RULE` not found in `engine/src/server/mod.rs`), so both runs used an isolated pod copy
  `/work/nexora-t09` (`git archive HEAD` plus this task's files, `CARGO_TARGET_DIR=/work/target-t09`):
  `make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora-t09/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m`
  with the new spec only: `3 failed, 27 passed`; `22-filter-categories.spec.ts:25` -
  `category-summary-gambling` element(s) not found.
- Green, same command with `FilterCategoriesPage.tsx`: `30 passed (1.6m)`, all four
  `22-filter-categories.spec.ts` tests pass (including the 400 px no-overflow check). The Go test still
  FAILs only with the expected "8 OpenAPI operations have no covering Playwright test" (M6 operations,
  covered by Task 33).
- `cd web && pnpm run typecheck` clean; `pnpm run lint` clean (`permission parity: 120 operations match`);
  prettier applied to both files.
- Deviation (plan text updated): the viewer test expands malware before asserting "abuse.ch"; the `h2`
  wraps the toggle button (valid HTML accordion pattern); row non-commercial badge
  `category-noncommercial-<key>`.
- Left open: commit (the lead commits).

