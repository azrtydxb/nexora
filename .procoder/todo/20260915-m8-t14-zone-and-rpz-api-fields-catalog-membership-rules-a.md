# M8 T14: Zone and RPZ API fields, catalog membership rules and hook points

Status: open
Created: 2026-09-15

## Description

Implements Task 14 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/api/zonemd_api_test.go` with `TestZonemdAPI` on the API test server the
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api -run "TestZonemdAPI|TestCatalogMembershipRules" -count=1'`
- [x] Implement in `zone/service.go`:
- [x] Map the fields in `api/zones.go` and `api/rpz.go` (create, update, get, list), and
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api ./mgmt/internal/zone ./mgmt/internal/snapshot ./mgmt/internal/stats -count=1 && go vet ./mgmt/...'`
- [ ] Report the paths. The lead commits `M8 T14: ZONEMD and catalog fields in the zone and RPZ API`.

## Evidence

- Red, pod toolbox-m7: `go test ./mgmt/internal/api -run "TestZonemdAPI|TestCatalogMembershipRules" -count=1`
  -> FAIL `create: map[... zonemd_generate:false zonemd_status: ...]` and `create member: map[... catalog_zone_id:<nil> ...]`.
- Green: same command -> `ok github.com/piwi3910/nexora/mgmt/internal/api 9.709s`.
- Full: `go test ./mgmt/internal/api ./mgmt/internal/zone ./mgmt/internal/snapshot ./mgmt/internal/stats ./mgmt/internal/catzone ./mgmt/internal/store -count=1`
  -> all `ok` (api 243s, zone 68s, snapshot 33s, stats 27s, catzone 17s, store 56s); `go vet ./mgmt/...` clean; `gofmt -l` empty.
- Mutations (private copy /work/m8-t14): update hook given no old id -> FAIL `hook calls [[A] [B] [B]], want [[A] [A B] [B]]`;
  toggle without `Force` -> FAIL `zonemd_generate toggle wrote seq 1 -> 1, want a forced rebuild`.
- Commit pending: the lead commits `M8 T14: ZONEMD and catalog fields in the zone and RPZ API`.
