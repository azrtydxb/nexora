# M6 T6: Access control split in the management plane

Status: open
Created: 2026-09-14

## Description

Implements Task 6 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/store/access_split_migration_test.go`:
- [x] Create `mgmt/internal/api/access_split_test.go`:
- [x] Run
- [x] Create `mgmt/migrations/00600_access_split.sql`:
- [x] In `mgmt/internal/api/handlers_dns.go`:
- [x] In `mgmt/internal/zone/model.go` and `mgmt/internal/zone/service.go`:
- [x] In `mgmt/internal/snapshot/snapshot.go` `BuildForGroup`, after the recursion ACL query, add:
- [x] Run
- [x] Report the paths. Commit message: `mgmt: authoritative query access and per-zone allow-query`.

## Evidence

- Red: `scripts/dev-exec.sh 'make webui-placeholder && go test ./mgmt/internal/api -run TestAccessControlSplitAPI -count=1; go test ./mgmt/internal/store -run TestAccessSplitMigrationKeepsBehaviour -count=1'`
  failed with `default authoritative access: {AllowCidrs:[127.0.0.0/8 ...] AuthCidrs:[] Revision:1}` and
  `ERROR: column "authoritative_allow_cidrs" does not exist (SQLSTATE 42703)`.
- Green: the shared checkout's `mgmt/internal/api` did not build because of other wave-1 tasks' in-flight
  edits (`handlers_admin.go` querylog fields, `handlers_auth.go` Login arity), so the plan command ran in the
  dev pod on an isolated copy (`git archive HEAD` plus this task's nine files, `/tmp/nexora-t06-*`):
  `go vet ./mgmt/internal/zone ./mgmt/internal/snapshot ./mgmt/internal/store ./mgmt/internal/api` clean;
  `go test ./mgmt/internal/api ./mgmt/internal/store ./mgmt/internal/snapshot ./mgmt/internal/zone -count=1`
  gave `ok` for all four packages; `-v` shows `--- PASS: TestAccessControlSplitAPI`,
  `--- PASS: TestAccessSplitMigrationKeepsBehaviour`, `--- PASS: TestBuildMapsEveryTable`.
- `gofmt -l` on the changed Go files: no output.
- Deviation: invalid zone CIDRs are validated in `zones.go` (400 `invalid_request`) because
  `zone.ValidationError` maps to 422; plan text updated.
