# M6 T29: Help and descriptions: filtering pages

Status: open
Created: 2026-09-14

## Description

Implements Task 29 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Set `pages` in `web/src/help/catalog/filtering.ts` to the five page files. Run
- [x] Add to `web/e2e/screens/04-filtering.spec.ts`, after navigation:
- [x] Add entries and `HelpTip`s for every listed control. Required verbatim:
- [x] Apply the Filtering description and link row. Replace "management plane" and "ships to every
- [x] Run `cd web && node scripts/check-help.mjs && pnpm run typecheck && pnpm run lint` and the
- [ ] Report the paths. Commit message: `gui: help and operator wording for filtering pages`.

## Evidence

- Red, check-help: after setting `pages`, `node scripts/check-help.mjs` listed 27 controls of the five
  files as `has no help entry` (for example `pages/FilteringPage.tsx: control "list-url" has no help entry`).
- Red, e2e (2026-09-15, private copy `/work/t29-nexora` holding specs 00 and 04 only):
  `make e2e-build && go test ./e2e -run TestGUICoverage` failed in `04-filtering.spec.ts`:
  `getByText('Domains on the allowlist are never blocked')` element(s) not found.
- Green, laptop: `cd web && node scripts/check-help.mjs && pnpm run typecheck && pnpm run lint` —
  `permission parity: 138 operations match`, `check-help: 162 controls on 45 pages have help`, exit 0.
- Green, e2e (private copy with specs 00, 04, 12, 13, 15, 21, 22, 28; then 00, 20, 21 because 21
  selects the `gui-edge` group that 20 creates): 04, 12 (both tests), 13, 15 (both), 20, 21, 22 (all
  four) and 28 passed. The run's coverage check failed only because the copy omitted the other specs
  (expected with a subset). Private copy and target dir deleted afterwards.
- Deviation: tips add `Help: <label>` buttons, which Playwright's substring `getByLabel` also matches,
  so specs 12, 13, 15, 21 and 22 now use `getByLabel("…", { exact: true })` (plan text updated).
