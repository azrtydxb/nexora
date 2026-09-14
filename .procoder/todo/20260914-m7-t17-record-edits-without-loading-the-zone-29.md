# M7 T17: Record edits without loading the zone (#29)

Status: open
Created: 2026-09-14

## Description

Implements Task 17 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create `mgmt/internal/zone/edit_test.go` (package `zone_test`, reusing `newService`, `createZone` and `actor` from `service_test.go`):
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/zone -run "TestRecordEditDoesNotLoadWholeZone|TestIncrementalEditsMatchFullRebuild|TestOwnerScopedChecksKeepRules"'`. Expect `TestRecordEditDoesNotLoadWholeZone` to FAIL with `a record edit read 20xx zone_records rows`. The other two pass to
- [ ] In `validate.go`, factor `CheckSet`'s rules into per-owner functions. `CheckSet` keeps calling them over the whole set.
- [ ] In `service.go`, add `checkOwners`. It reads only these rows and applies the same rules and error codes:
- [ ] In `CreateRecord`/`UpdateRecord`/`DeleteRecord`:
- [ ] In `build.go`'s `Rebuild`, add the incremental path at the top:
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 -race ./mgmt/internal/zone/... ./mgmt/internal/dnssec/... ./mgmt/internal/dynupdate/... ./mgmt/internal/xfrin/...'` and expect all to pass. Run `scripts/dev-exec.sh 'make e2e-build && go test -count=1 ./e2e -run "TestAuthoritativeZonePropagation|TestZoneFil

## Evidence

