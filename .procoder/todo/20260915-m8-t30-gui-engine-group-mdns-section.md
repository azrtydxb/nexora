# M8 T30: GUI engine group mDNS section

Status: open
Created: 2026-09-15

## Description

Implements Task 30 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create the seed (engine group `gui-mdns`, no engines) and `web/e2e/screens/43-mdns.spec.ts`:
- [ ] Run the GUI coverage run for this spec and expect FAIL: region "mDNS" not found.
- [ ] Implement the section (`aria-label="mDNS"`):
- [ ] Run `cd web && pnpm run typecheck && pnpm run lint` and the GUI run, and expect PASS.
- [ ] Report the paths. The lead commits `M8 T30: GUI engine group mDNS section`.

## Evidence
