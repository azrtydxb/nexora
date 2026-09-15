# M6 T33: Help gate, help and page header specs, full GUI coverage

Status: open
Created: 2026-09-14

## Description

Implements Task 33 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Run `cd web && node scripts/check-help.mjs --all` and expect PASS. If it fails, the listed file
- [x] Change the `lint` script in `web/package.json` to
- [x] Create `web/e2e/screens/29-help.spec.ts`:
- [x] Run
- [x] Report the paths. Commit message: `gui: help coverage gate, help and page header specs`.

## Evidence

2026-09-15 verification sweep (commit b8a46e7 `gui: help coverage gate, help and page header specs`).
- `cd web && node scripts/check-help.mjs --all` -> `check-help: 172 controls on 49 pages have help`.
- lint script now ends `node scripts/check-help.mjs --all`; probe `<Input id="gate-probe" />` in AuditPage.tsx -> `pnpm run lint` FAIL `check-help: pages/AuditPage.tsx: control "gate-probe" has no help entry`; probe removed.
- `web/e2e/screens/29-help.spec.ts` (samples adapted to page-level controls, plan text updated) and `35-page-headers.spec.ts` created.
- dev pod: `make e2e-build && go test ./e2e -run "TestGUICoverage|TestQueryLogBackends"` -> `ok github.com/piwi3910/nexora/e2e 278.790s`; full `go test -v ./e2e/...` -> `--- PASS: TestGUICoverage (258.16s)` (no uncovered OpenAPI operation), 64 Playwright specs passed.
- `make lint` rc=0, `cargo test --locked -p nexora-engine` rc=0, `go test -race -count=1 ./mgmt/... ./deploy/...` rc=0 (40 packages ok), `pnpm run typecheck && pnpm run build` rc=0.
- Paths: web/package.json, web/e2e/screens/29-help.spec.ts, web/e2e/screens/35-page-headers.spec.ts, .procoder/plans/nexora-m6-operator-ux.md.

