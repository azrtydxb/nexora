# M6 T25: Dashboard GUI

Status: open
Created: 2026-09-14

## Description

Implements Task 25 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `web/e2e/screens/27-dashboard.spec.ts`:
- [x] Run
- [x] Implement `web/src/api/dashboard.ts` per Interfaces, and `web/src/pages/DashboardPage.tsx`:
- [x] Run the same command and expect `01-dashboard.spec.ts` and `27-dashboard.spec.ts` to pass. Run
- [x] Report the paths. Commit message: `gui: dashboard ranges, traffic, latency, filtering, DNSSEC, top lists, fleet and health`.

## Evidence

Verified in a private pod copy (`/work/nexora-t25` = HEAD 41f8af5 + this task's three web files, own
`CARGO_TARGET_DIR`), because the shared tree carried other wave-3 tasks' in-progress work
(`QueryLogPage.tsx` failed `tsc`). The copy was deleted afterwards.

- Red: `make e2e-build && NEXORA_E2E_BIN_DIR=... go test ./e2e -run TestGUICoverage -count=1 -timeout 45m`
  with the spec only (glob narrowed to `[02][157]-*` in the copy): `27-dashboard.spec.ts` failed with
  `getByTestId('dashboard-section-traffic')` "element(s) not found"; `01-dashboard` passed.
- Green (same narrowed run): `✓ 01-dashboard.spec.ts`, `✓ 27-dashboard.spec.ts (5.4s)`.
- Green (full `[0-9][0-9]-*` glob, 34 specs): `✓ 1 e2e/screens/01-dashboard.spec.ts`,
  `✓ 31 e2e/screens/27-dashboard.spec.ts (5.4s)`; `33 passed, 1 failed`. The failure is
  `30-access-control-split.spec.ts`: `missing environment variable NEXORA_E2E_ACL_ZONE` (its seed is
  not in HEAD; not this task). `21-engine-group-scope` failed only in the narrowed run (it needs the
  `gui-edge` group from `20-fleet`) and passed in the full run.
- `cd web && pnpm run typecheck && pnpm run lint` in the copy: pass (`permission parity: 120
operations match`, `check-help: 0 controls on 0 pages have help`).
- `prettier --write` on the three files; `eslint` on them in the shared tree: clean.
