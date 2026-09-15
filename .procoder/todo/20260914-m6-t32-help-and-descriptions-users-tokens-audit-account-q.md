# M6 T32: Help and descriptions: users, tokens, audit, account, query log, dashboard, sign-in

Status: open
Created: 2026-09-14

## Description

Implements Task 32 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Set `pages` in `web/src/help/catalog/admin.ts` to the nine files. Run
- [x] Add entries and `HelpTip`s for every listed control. Required verbatim:
- [x] Apply the Query log description.
- [x] Run `cd web && node scripts/check-help.mjs && pnpm run typecheck && pnpm run lint` and the
- [ ] Report the paths. Commit message: `gui: help for users, tokens, audit, account, query log and dashboard`.

## Evidence

- Red: with `pages` set and no entries, `node scripts/check-help.mjs` listed 27 missing entries across the
  nine files (for example `pages/QueryLogPage.tsx: control "querylog-name" has no help entry`).
- Green: 37 controls on the 9 admin pages have entries and HelpTips (the 8 query log multi-selects,
  `querylog-live` and `user-disabled` became checkable), plus 6 column header tips. Verbatim
  `querylog-name`, `querylog-source`, `password-new` entries present.
- `node scripts/check-help.mjs` over a copy with only the admin area listed: `check-help: 37 controls on 9 pages have help`, exit 0.
  The full run still exits 1, only for pages of Tasks 28-30 still in progress (DnssecPage, FilteringPage,
  PoliciesPage, RewritesPage, RpzPage, ZoneDnssecTab, ZoneTransfersTab); no admin page and no duplicate id.
- `pnpm run typecheck`: pass. `pnpm run lint`: eslint and `permission parity: 138 operations match` pass;
  check-help fails only as above. `npx eslint` on the changed files: 0 errors.
- Dev pod private copy (`/work/t32-nexora`, `CARGO_TARGET_DIR=/work/t32-target`, deleted afterwards):
  `make e2e-build` exit 0; `go test ./e2e -run TestGUICoverage -count=1 -timeout 45m`: 37 passed, 7 failed.
  Specs 00, 01, 06, 07, 08, 10, 11, 23, 24, 25, 26, 27 and 33 all passed. Failures are outside this task:
  04 (Task 29 description not yet applied), 16 and 17 (Task 28: `getByLabel` strict mode, two elements),
  34 (version reload hint), 50 and 59 (M11 AI work in progress).
