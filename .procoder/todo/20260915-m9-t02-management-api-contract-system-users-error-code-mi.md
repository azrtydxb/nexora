# M9 T2: Management API contract: system users, error code, migration 01000

Status: open
Created: 2026-09-15

## Description

Implements Task 2 of `.procoder/plans/nexora-m9-platform.md` (spec `.procoder/specs/nexora-m9-platform.md`, milestone M9 Platform). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/store/system_users_migration_test.go`:
- [x] Create `mgmt/migrations/01000_system_users.sql`:
- [x] In `mgmt/api/openapi.yaml`, change the `User` schema's `source` to
- [x] In `web/src/pages/UsersPage.tsx` replace
- [x] Run `scripts/dev-exec.sh 'go vet ./... && go test -count=1 ./mgmt/internal/api/... ./mgmt/internal/store/...'`

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'go test ./mgmt/internal/store -run TestSystemUsersMigration -count=1'`
  → `FAIL ... system user refused after 01000: ERROR: new row for relation "users" violates check constraint "users_source_check"`.
- Green (after `01000_system_users.sql`): same command → `ok github.com/piwi3910/nexora/mgmt/internal/store 2.221s`.
- Regenerated `mgmt/internal/api/gen.go` with
  `go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config oapi-codegen.yaml openapi.yaml`
  (constants now `UserSourceLocal`, `UserSourceOidc`, `UserSourceSystem`; no other users of the old names);
  `web/src/api/schema.d.ts` with `pnpm run gen:api` (openapi-typescript 7.13.0): `source: "local" | "oidc" | "system"`.
- `cd web && pnpm run typecheck && pnpm run lint` → pass (`permission parity: 120 operations match`).
- `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'go vet ./...; go test -count=1 ./mgmt/internal/api/... ./mgmt/internal/store/...'`
  → vet clean; `ok .../mgmt/internal/api 70.301s`, `ok .../mgmt/internal/store 24.354s`.
- `gofmt -l mgmt/internal/store mgmt/internal/api` → no output.
- Not committed (lead commits).
