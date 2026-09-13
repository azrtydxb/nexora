# M1 Task 15: OpenAPI spec, HTTP API handlers, embedded GUI serving and `serve`/`user create`

Status: closed 2026-09-13
Created: 2026-09-13

## Description

Implement Task 15 ("OpenAPI spec, HTTP API handlers, embedded GUI serving and `serve`/`user create`") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 15 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] `TestCSRFAndDatabaseDown` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestPermissionsCoverEveryOperation` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestSetupCRUDConflictAuditAndRBAC` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Failing first: not captured as a separate run — the test file, spec and handlers were written before the first `go test` of the package. The first real run failed `TestPermissionsCoverEveryOperation` for every operation (embedded spec had upper-cased operationIds), fixed by the codegen `compatibility` option.
- Generated with oapi-codegen v2.8.0 (same version as the dev pod's `oapi-codegen`): `cd mgmt/api && oapi-codegen -config oapi-codegen.yaml openapi.yaml`.
- Plan deviations (plan text updated): see the "Built notes" bullet in Task 15 (method-name casing in the strict middleware, local cookie/302 response types, 4 MiB body cap, JSON 404 for unknown API routes, database check violations -> 400, row-lock revision checks, `revokeJoinToken` via `Mutate`, sessions deleted on disable/password change, `stats.Record` uses database time).
- `scripts/dev-exec.sh 'go test -race -count=1 -v ./mgmt/internal/api/...'` -> `--- PASS: TestPermissionsCoverEveryOperation`, `--- PASS: TestSetupCRUDConflictAuditAndRBAC`, `--- PASS: TestCSRFAndDatabaseDown`; `ok github.com/piwi3910/nexora/mgmt/internal/api 12.781s`.
- `scripts/dev-exec.sh 'make mgmt-test'` -> every package `ok` (api, auth, config, control, pki, snapshot, store, gen); the target still exits 1 on `FAIL ./bench/... [setup failed]` because `bench/` does not exist until Task 21.
- Extra checks (temporary tests, not committed): resolver settings/access control/allowlist/filter-list CRUD, 404/400 for missing/bad ids, refresh without fetcher -> 400, last-admin demote/delete -> 409, operator API token -> 403 on audit and 401 after revoke, OIDC start with OIDC disabled -> 503, SPA fallback with `Cache-Control: no-cache`, logout -> 401 on `/auth/me`; `serve` printed `setup token:`, `http listening on`, `grpc listening on`, health 200, exit 0 on cancel; `user create --admin ...` -> `user created: <uuid>`.
- `gofmt -l mgmt e2e` empty, `go vet ./mgmt/... ./e2e/...` clean.
