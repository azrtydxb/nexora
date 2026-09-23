# M8 T3: Migrations and model fields

Status: done
Created: 2026-09-15

## Description

Implements Task 3 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/store/m8_migration_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/store -run TestM8MigrationKeepsBehaviour -count=1'`.
- [x] Create `mgmt/migrations/01300_zonemd.sql`:
- [x] Create `mgmt/migrations/01301_catalog_zones.sql`:
- [x] Create `mgmt/migrations/01302_odoh.sql`:
- [x] Create `mgmt/migrations/01303_engine_group_mdns.sql`:
- [x] Add the Interfaces fields to `zone.Zone`, `CreateZoneInput`, `UpdateZoneInput` and `Signer` in
- [x] Extend `fleet.EngineGroup`, `scanEngineGroup`, `CreateEngineGroup` and `UpdateEngineGroup` with
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/store -run TestM8MigrationKeepsBehaviour -count=1 && go test ./mgmt/internal/zone ./mgmt/internal/fleet ./mgmt/internal/dnssec -count=1 && go vet ./mgmt/...'`
- [x] Report the paths. The lead commits `M8 T3: migrations 01300-01303 and model fields`.

## Evidence

- Red: `go test ./mgmt/internal/store -run TestM8MigrationKeepsBehaviour -count=1` -> `ERROR: column "zonemd_generate" does not exist (SQLSTATE 42703)`.
- Numbering: main has 00800-00801 and 01200-01207, nothing in 01300-00999; 01300-01303 kept.
- Green: `go test ./mgmt/internal/store -run TestM8MigrationKeepsBehaviour -count=1` -> `ok ... 2.189s`; `go test ./mgmt/internal/zone ./mgmt/internal/fleet ./mgmt/internal/dnssec -count=1` -> ok 29.3s / 11.5s / 32.7s; `go build ./...` and `go vet ./mgmt/...` clean.
- Deviation: the plan test expected `if_present` on existing rows, contradicting the spec (existing rows `off`); the test asserts `off` for existing rows and `if_present` for rows created after the migration (plan text updated).
