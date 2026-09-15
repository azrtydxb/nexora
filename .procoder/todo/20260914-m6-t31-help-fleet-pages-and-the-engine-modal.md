# M6 T31: Help: fleet pages and the engine modal

Status: open
Created: 2026-09-14

## Description

Implements Task 31 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Set `pages` in `web/src/help/catalog/fleet.ts` to the six files. Run
      `node scripts/check-help.mjs` and see it fail listing their controls.
- [x] Add entries and `HelpTip`s for every listed control. Required verbatim for the health window
      control (`enginegroup-health-window`), plus `engine-logs-level` in `EngineModal.tsx`; `fleet.md`
      sections `engine-groups`, `rollouts`, `certificates`, `engine-logs` written.
- [x] Run `cd web && node scripts/check-help.mjs && pnpm run typecheck && pnpm run lint` and the
      `TestGUICoverage` command; specs 05, 20 and 32 pass (fleet area clean; the whole check and
      `TestGUICoverage` still fail on other in-progress tasks' pages, see Evidence).
- [ ] Report the paths. Commit message: `gui: help for engines, engine groups and rollouts`.

## Evidence

- Red: after setting `pages`, `node scripts/check-help.mjs` exited 1 with 18 fleet lines, e.g.
  `check-help: pages/EngineGroupPage.tsx: control "enginegroup-acl" has no help entry` and
  `check-help: pages/EnginesPage.tsx: control "jointoken-max-uses" has no help entry`. NumberField ids
  (health window, apply timeout, SERVFAIL, min queries) and the log level select were invisible to the
  check; they now carry `data-help`.
- Green (laptop, `web/`): `node scripts/check-help.mjs 2>&1 | grep -E "Engine|Rollout|fleet"` prints
  nothing (no fleet-area problems). The full check still exits 1 only on pages owned by Tasks 28–30,
  which are in progress in parallel (DnssecPage, FilteringPage, PoliciesPage, RewritesPage, RpzPage,
  ZoneDnssecTab, ZoneTransfersTab), so `pnpm run lint` exits 1 on those lines;
  `npx eslint .` exit 0, `node scripts/check-permissions.mjs` "permission parity: 138 operations match",
  `pnpm run typecheck` passes.
- Pod (private copy `/work/t31-nexora`, `CARGO_TARGET_DIR=/work/t31-target`, deleted afterwards):
  `make e2e-build` exit 0, then
  `NEXORA_E2E_BIN_DIR=/work/t31-nexora/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m`:
  `✓ 05-engines.spec.ts › engines and join tokens`, `✓ 20-fleet.spec.ts › admin manages engine groups,
  engines, rollouts and certificates`, `✓ 32-engine-modal.spec.ts` (both tests); summary
  `7 failed, 37 passed`, the failures in 04 (Task 29), 16 and 17 (Task 28; `getByLabel('Zone')` /
  `getByLabel('Mode')` strict-mode violations, the HelpTip `aria-label="Help: …"` also matches), 34
  (version) and 50/59 (M11 AI), none in fleet pages. The test run FAILs overall.
