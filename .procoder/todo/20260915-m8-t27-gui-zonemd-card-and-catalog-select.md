# M8 T27: GUI ZONEMD card and catalog select

Status: open
Created: 2026-09-15

## Description

Implements Task 27 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create `e2e/gui_seed_m8_zonemd_test.go`. `init() { registerGUISeed(seedZonemd) }`, where
- [ ] Create `web/e2e/screens/40-zonemd.spec.ts`:
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e -run TestGUICoverage -count=1'` with
- [ ] Implement `ZoneZonemdCard.tsx`:
- [ ] Run `cd web && pnpm run typecheck && pnpm run lint`, then the GUI run above, and expect PASS.
- [ ] Report the paths. The lead commits `M8 T27: GUI ZONEMD card and catalog select`.

## Evidence
