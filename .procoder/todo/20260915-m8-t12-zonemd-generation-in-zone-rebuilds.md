# M8 T12: ZONEMD generation in zone rebuilds

Status: open
Created: 2026-09-15

## Description

Implements Task 12 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/zone/zonemd_build_test.go` (package `zone_test`, using `storetest.New`, a
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/zone -run TestRebuildAddsVerifiableZonemd -count=1'`
- [x] In `zone.Rebuild`, for `z.ZonemdGenerate`:
- [x] Implement `SignZONEMD` in `dnssec/store.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/zone ./mgmt/internal/dnssec -count=1'` and
- [ ] Report the paths. The lead commits `M8 T12: ZONEMD generation for primary zones`.

## Evidence

- Red (previous attempt, recorded in the plan): `go test ./mgmt/internal/zone -run TestRebuildAddsVerifiableZonemd -count=1`
  failed with `version 0: {Status:failed Err:no apex ZONEMD}` before `Rebuild` generated ZONEMD.
- Green, pod toolbox-m7: `go vet ./mgmt/internal/zone ./mgmt/internal/dnssec` clean, `gofmt -l` empty,
  `go test ./mgmt/internal/zone ./mgmt/internal/dnssec -count=1` ->
  `ok mgmt/internal/zone 45.573s`, `ok mgmt/internal/dnssec 40.451s`.
- Mutation 1 (private copy /work/m8-t12): `SignZONEMD` call removed from `Rebuild` -> signed=true FAIL
  `ZONEMD RRSIG missing or invalid` (the placeholder's signature does not verify).
- Mutation 2: ZONEMD dropped from `isSOAData` -> both subtests FAIL `version 0: {Status:failed Err:serial mismatch}`.
- Commit pending: the lead commits `M8 T12: ZONEMD generation for primary zones`.
