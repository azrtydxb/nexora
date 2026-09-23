# M8 T29: GUI Oblivious DoH settings

Status: open
Created: 2026-09-15

## Description

Implements Task 29 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create `web/e2e/screens/42-odoh.spec.ts`:
- [ ] Run the GUI coverage run for this spec and expect FAIL: region "Oblivious DoH" not found.
- [ ] Implement `OdohSection.tsx` (a `section` with `aria-label="Oblivious DoH"`) in `SettingsPage.tsx`:
- [ ] Run `cd web && pnpm run typecheck && pnpm run lint` and the GUI run, and expect PASS.
- [ ] Report the paths. The lead commits `M8 T29: GUI Oblivious DoH settings`.

## Evidence
