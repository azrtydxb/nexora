# M8 T25: End-to-end catalog zones with BIND

Status: open
Created: 2026-09-15

## Description

Implements Task 25 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create `e2e/catalog_zones_test.go`:
- [ ] Extend `named.go` rendering for `Catalogs` as the Interfaces line says, and add a unit case to
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e -run "TestCatalogZoneProducer|TestCatalogZoneConsumer" -count=1 -v'`
- [ ] Report the paths. The lead commits `M8 T25: e2e catalog zones with BIND`.

## Evidence
