# M6 T30: Help and descriptions: zone pages

Status: open
Created: 2026-09-14

## Description

Implements Task 30 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Set `pages` in `web/src/help/catalog/zones.ts` to the eight files. Run
- [x] Add entries and `HelpTip`s for every listed control. Required verbatim:
- [x] Apply the Zones description.
- [x] Run `cd web && node scripts/check-help.mjs && pnpm run typecheck && pnpm run lint` and the
- [ ] Report the paths. Commit message: `gui: help for zones, transfers, TSIG and DNSSEC signing`.

## Evidence

- RED: with `pages` set and no entries, `cd web && node scripts/check-help.mjs` listed 27 zone-page
  controls without a help entry, e.g. `check-help: pages/ZoneTransfersTab.tsx: control "zone-allow-query" has no help entry`.
- GREEN (laptop): `node scripts/check-help.mjs` reports no zone-page problems; `npx eslint` on the changed
  files is clean; `tsc -b --noEmit` has no errors in them (the shared tree fails typecheck only on another
  task's in-progress `src/components/ai/ProposalCard.tsx`).
- GREEN (dev pod, private copy of HEAD plus this task's files in `/tmp/nexora-t30`):
  `node scripts/check-help.mjs && pnpm run typecheck && pnpm run lint` ->
  `check-help: 28 controls on 8 pages have help`, `permission parity: 138 operations match`, exit 0.
- E2E (same private copy, other specs removed to run only this task's, own CARGO_TARGET_DIR/bin):
  `make e2e-build && NEXORA_E2E_BIN_DIR=/tmp/nexora-t30/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m`
  -> `00-setup: 1 passed`; `18-zones, 19-zone-security, 30-access-control-split: 4 passed (28.6s)`. The test
  then failed only its operation-coverage assertion (105 operations uncovered), expected with the other
  specs removed. Copy deleted afterwards. Full-suite TestGUICoverage still to run by the lead after commit.
