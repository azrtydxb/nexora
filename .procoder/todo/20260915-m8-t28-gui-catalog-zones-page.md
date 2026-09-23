# M8 T28: GUI Catalog zones page

Status: open
Created: 2026-09-15

## Description

Implements Task 28 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create the seed: a producer catalog `gui-seeded.test.` with member zone `gui-member.test.`, and a
- [ ] Create `web/e2e/screens/41-catalog-zones.spec.ts`:
- [ ] Run the GUI coverage run for this spec and expect FAIL: `nav-catalog-zones` not found.
- [ ] Implement:
- [ ] Run `cd web && pnpm run typecheck && pnpm run lint` and the GUI run, and expect PASS.
- [ ] Report the paths. The lead commits `M8 T28: GUI catalog zones page`.

## Evidence
