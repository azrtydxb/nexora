# M8 T17: mDNS settings in the management plane

Status: open
Created: 2026-09-15

## Description

Implements Task 17 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/api/engine_group_mdns_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api -run TestEngineGroupMdnsAPI -count=1'` and
- [x] Implement:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api ./mgmt/internal/fleet ./mgmt/internal/snapshot -count=1 && go vet ./mgmt/...'`
- [ ] Report the paths. The lead commits `M8 T17: engine group mDNS settings`.

## Evidence

- Red (toolbox-m7, /work/nexora): `go test ./mgmt/internal/api -run TestEngineGroupMdnsAPI -count=1`
  -> `engine_group_mdns_test.go:24: one reflect iface: 201` (map order; same reason as the plan's `no interfaces: 201`).
- Green: the shared tree did not build (another wave's in-progress `catalog_zones_test.go` references the
  missing `api.NewCatalogZoneService`), so the run used a private copy `/work/m8-t17` with those two
  untracked catalog files removed:
  `go test ./mgmt/internal/api ./mgmt/internal/fleet ./mgmt/internal/snapshot -count=1 && go vet ./mgmt/...`
  -> `ok mgmt/internal/api 335.213s`, `ok mgmt/internal/fleet 17.191s`, `ok mgmt/internal/snapshot 22.496s`, vet clean, EXIT 0.
- Mutation: `AddMdns(snap, group)` replaced by `_ = group` in the private copy -> FAIL
  `engine_group_mdns_test.go:36: group snapshot mdns <nil>`.
- gofmt -l on the changed Go files: clean.
