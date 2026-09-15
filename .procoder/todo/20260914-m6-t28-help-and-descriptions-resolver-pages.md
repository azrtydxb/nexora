# M6 T28: Help and descriptions: resolver pages

Status: done (awaiting the lead's commit)
Created: 2026-09-14

## Description

Implements Task 28 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Set `pages` in `web/src/help/catalog/resolver.ts` to the six resolver page files.
- [x] Run `cd web && node scripts/check-help.mjs` and expect FAIL (exit 1) listing every control of
      those files (the task's inventory).
- [x] For every listed id, add an entry to `resolverHelp.entries` and a `<HelpTip id="<id>" label="<visible label>" />`
      in the label row (the `Field`, `SwitchField` and DNSSEC `toggle` helpers take the tip as a
      `ReactNode` prop), plus tips on the non-obvious column headers.
- [x] Write the topic sections named in Task 11 for `resolution.md`, `dnssec.md` and
      `access-control.md`.
- [x] Set the `SettingsPage` description to the Interfaces text, and reword the "engines"/"management
      plane" section, dialog and hint texts of the six files.
- [x] Run `node scripts/check-help.mjs`, `pnpm run typecheck`, `pnpm run lint` and `TestGUICoverage`.
- [x] Report the paths. Commit message: `gui: help for forwarding, recursion, DNSSEC, settings and access control`.

## Evidence

Inventory (red): `cd web && node scripts/check-help.mjs` after setting `pages` printed
`check-help: pages/SettingsPage.tsx: control "settings-strategy" has no help entry` and 31 more lines
for the six files; exit 1.

Green, laptop (`/Users/pascal/Development/nexora/web`):

- `node scripts/check-help.mjs` -> `check-help: 162 controls on 45 pages have help` (exit 0); the six
  resolver pages alone: `check-help: 32 controls on 6 pages have help`.
- `pnpm run typecheck` -> clean; `pnpm run lint` -> `eslint` clean, `permission parity: 138 operations
match`, `check-help: 162 controls on 45 pages have help`.
- `npx prettier --check` on every changed `.tsx`, `.ts`, `.md` and spec file -> "All matched files use
  Prettier code style!".

Dev pod (`scripts/dev-exec.sh`, private copy `/work/t28-nexora` with `CARGO_TARGET_DIR=/work/t28-target`
and `NEXORA_E2E_BIN_DIR=/work/t28-nexora/bin`), at HEAD `1908ac4` plus this task's changes:

- `make e2e-build` -> exit 0; `go test ./e2e -run TestGUICoverage -count=1 -timeout 45m` -> 43 passed,
  1 failed, `43 passed (3.2m)`. Every spec of this task passed: 02-upstreams, 03-access-control,
  09-settings, 16-dnssec, 17-resolution, 21-engine-group-scope, 28-navigation,
  30-access-control-split, 31-upstream-parallel.
- Pre-existing, not from this task: `34-version.spec.ts` (`version-reload-hint` not found) and
  `TestHelpTopicsReferenceOperationsDoc` (`ai.md names missing docs/operations.md heading "AI"`, from
  M11 Task 11; `docs/operations.md` is Task 23's file).

Deviation: `web/e2e/screens/16-dnssec.spec.ts` and `web/e2e/screens/17-resolution.spec.ts` needed
`getByLabel("…", { exact: true })`, because Playwright's `getByLabel` also matches the new info
icon's `aria-label` ("Help: Domain") as a substring. Proven: the run before the change failed both
specs with `strict mode violation: … getByLabel('Zone') resolved to 2 elements`; after it both pass.
The plan text of Task 28 records this.
