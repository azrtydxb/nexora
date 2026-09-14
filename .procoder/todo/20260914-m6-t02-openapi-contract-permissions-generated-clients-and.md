# M6 T2: OpenAPI contract, permissions, generated clients and stubs

Status: open
Created: 2026-09-14

## Description

Implements Task 2 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/api/m6_contract_test.go`:
- [x] Run `scripts/dev-exec.sh 'make webui-placeholder && go test ./mgmt/internal/api -run "TestM6OperationsAreRoutedAndAuthenticated|TestPermissionsCoverEveryOperation" -count=1'`
- [x] Edit `mgmt/api/openapi.yaml` with every schema and operation in Interfaces:
- [x] Regenerate on the laptop: `cd mgmt/api && oapi-codegen -config oapi-codegen.yaml openapi.yaml`
- [x] Add the eight operation ids with their roles to `mgmt/internal/auth/permissions.go` (the viewer
- [x] In `mgmt/internal/api/server.go` add the field to `Deps`:
- [x] Create the five stub files. Each stub returns `nil, coded(http.StatusNotImplemented, "not_implemented", "not implemented yet")`. For example, `mgmt/internal/api/version.go`:
- [x] In `mgmt/internal/api/handlers_admin.go` `SearchQueryLog`, keep the current single-value
- [x] Create `web/src/lib/preferences.ts`:
- [x] Run
- [x] Report the paths. Commit message: `api: M6 OpenAPI contract, permissions, stubs`. (paths reported; the lead commits)

## Evidence

- Red: `scripts/dev-exec.sh 'make webui-placeholder && go test ./mgmt/internal/api -run "TestM6OperationsAreRoutedAndAuthenticated|TestPermissionsCoverEveryOperation" -count=1'`
  -> `FAIL: m6_contract_test.go:20: GET /dashboard/series?range=1h without a session -> 404, want 401`.
- Regenerated on the laptop: `go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config oapi-codegen.yaml openapi.yaml`
  (the PATH binary v2.6.0 rejects `type: [string, "null"]`) and `pnpm run gen:api` (openapi-typescript 7.13.0).
- Green: `scripts/dev-exec.sh 'make webui-placeholder && go build ./... && go test ./mgmt/internal/api -run "TestM6OperationsAreRoutedAndAuthenticated|TestPermissionsCoverEveryOperation" -count=1 -v'`
  -> `--- PASS: TestPermissionsCoverEveryOperation`, `--- PASS: TestM6OperationsAreRoutedAndAuthenticated`, `ok github.com/piwi3910/nexora/mgmt/internal/api`.
- Regression: `scripts/dev-exec.sh 'go vet ./mgmt/... && go test ./mgmt/internal/api/... ./mgmt/internal/auth/... ./mgmt/internal/querylog/... -count=1'`
  -> `ok .../api 42.096s`, `ok .../auth 9.220s`, `ok .../querylog 0.034s`. `go vet ./e2e/... ./deploy/...` clean, `gofmt -l mgmt` empty.
- `cd web && pnpm run typecheck && pnpm run lint` -> clean, `permission parity: 120 operations match`.
- Deviation: `web/src/pages/QueryLogPage.tsx` sends the single-select filters as one-element arrays so the
  GUI type-checks (plan Files updated).
