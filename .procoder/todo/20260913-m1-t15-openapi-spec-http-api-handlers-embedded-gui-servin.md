# M1 Task 15: OpenAPI spec, HTTP API handlers, embedded GUI serving and `serve`/`user create`

Status: open
Created: 2026-09-13

## Description

Implement Task 15 ("OpenAPI spec, HTTP API handlers, embedded GUI serving and `serve`/`user create`") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 15 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [ ] `TestCSRFAndDatabaseDown` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestPermissionsCoverEveryOperation` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestSetupCRUDConflictAuditAndRBAC` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

