# M8 T31: GUI RPZ ZONEMD verification

Status: open
Created: 2026-09-15

## Description

Implements Task 31 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create the seed and `web/e2e/screens/44-rpz-zonemd.spec.ts`:
- [ ] Run the GUI coverage run for this spec and expect FAIL: `rpz-zonemd-verify` not found.
- [ ] Implement:
- [ ] Run `cd web && pnpm run typecheck && pnpm run lint` and the GUI run, and expect PASS.
- [ ] Report the paths. The lead commits `M8 T31: GUI RPZ ZONEMD verification`.

## Evidence
