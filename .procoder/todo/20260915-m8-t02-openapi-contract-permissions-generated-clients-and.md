# M8 T2: OpenAPI contract, permissions, generated clients and stubs

Status: done
Created: 2026-09-15

## Description

Implements Task 2 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/api/m8_contract_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api -run TestM8OperationsDeclared -count=1'`
- [x] Add to `components/schemas` in `openapi.yaml`:
- [x] Extend existing schemas:
- [x] Add the paths:
- [x] Add the seven operationIds with their roles to `mgmt/internal/auth/permissions.go` and
- [x] Regenerate: `cd mgmt/api && oapi-codegen -config oapi-codegen.yaml openapi.yaml` and
- [x] Add `CatalogZones CatalogZoneService`, `ODoH ODoHService` and both interfaces to `server.go`. Create `catalog_zones.go` and `odoh.go`, each implementing its
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api -run "TestM8OperationsDeclared|TestPermissionsCoverEveryOperation" -count=1 && go vet ./mgmt/...'`
- [x] Report the paths. The lead commits `M8 T2: OpenAPI contract, permissions, generated clients, stubs`.

## Evidence

- Red: `go test ./mgmt/internal/api -run TestM8OperationsDeclared -count=1` -> `getOdohSettings: role , want viewer` (map iteration order; roles are strings).
- gen.go: `go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config oapi-codegen.yaml openapi.yaml` in the pod, copied back; regenerating after formatting openapi.yaml produced a byte-identical gen.go.
- schema.d.ts: `pnpm run gen:api` in the pod, copied back, formatted with `scripts/pc-format.sh` (+337 lines).
- Green: `go test ./mgmt/internal/api -run "TestM8OperationsDeclared|TestPermissionsCoverEveryOperation" -count=1` -> `ok ... 2.651s`; `go vet ./mgmt/...` clean; `pnpm run typecheck` clean; `pnpm run lint` -> `permission parity: 145 operations match`, `check-help: 172 controls on 49 pages have help`.
- Deviation: list response is a plain array like listZones (plan text updated).
