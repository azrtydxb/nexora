# M7 T17: Record edits without loading the zone (#29)

Status: open
Created: 2026-09-14

## Description

Implements Task 17 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/zone/edit_test.go` (package `zone_test`, reusing `newService`, `createZone` and `actor` from `service_test.go`):
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/zone -run "TestRecordEditDoesNotLoadWholeZone|TestIncrementalEditsMatchFullRebuild|TestOwnerScopedChecksKeepRules"'`. Expect `TestRecordEditDoesNotLoadWholeZone` to FAIL with `a record edit read 20xx zone_records rows`. The other two pass to
- [x] In `validate.go`, factor `CheckSet`'s rules into per-owner functions. `CheckSet` keeps calling them over the whole set.
- [x] In `service.go`, add `checkOwners`. It reads only these rows and applies the same rules and error codes:
- [x] In `CreateRecord`/`UpdateRecord`/`DeleteRecord`:
- [x] In `build.go`'s `Rebuild`, add the incremental path at the top:
- [x] Run `scripts/dev-exec.sh 'go test -count=1 -race ./mgmt/internal/zone/... ./mgmt/internal/dnssec/... ./mgmt/internal/dynupdate/... ./mgmt/internal/xfrin/...'` and expect all to pass. Run `scripts/dev-exec.sh 'make e2e-build && go test -count=1 ./e2e -run "TestAuthoritativeZonePropagation|TestZoneFileRoundTrip|TestSecondaryAndDynamicUpdate"'` and expect PASS.
- [ ] Committed (lead).

## Evidence

All commands in the second dev pod (`NEXORA_DEV_DEPLOY=toolbox-m7`).

- Red: `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/zone -run "TestRecordEditDoesNotLoadWholeZone|TestIncrementalEditsMatchFullRebuild|TestOwnerScopedChecksKeepRules"'` before wiring the edit path: `--- FAIL: TestRecordEditDoesNotLoadWholeZone ... a record edit read 2002 zone_records rows`; the other two passed.
- Green: `go test -count=1 ./mgmt/internal/zone` -> `ok github.com/piwi3910/nexora/mgmt/internal/zone 30.269s`.
- Mutation check: delta `Added` truncated to half -> `TestIncrementalEditsMatchFullRebuild` FAIL (`+ n18.inc.test. 120 IN A 192.0.2.199` ...); DNAME descendant query skipped -> `TestOwnerScopedChecksKeepRules` FAIL (`got <nil>, want dname_occludes`). Both mutations reverted.
- `go test -count=1 -race ./mgmt/internal/zone/... ./mgmt/internal/dnssec/... ./mgmt/internal/dynupdate/... ./mgmt/internal/xfrin/...` -> ok zone 47.5s, dnssec 45.5s, dynupdate 12.2s, xfrin 15.0s.
- `make e2e-build && go test -count=1 -v ./e2e -run "TestAuthoritativeZonePropagation|TestZoneFileRoundTrip"` -> both `--- PASS`, `ok github.com/piwi3910/nexora/e2e 11.424s`; `-run TestSecondaryAndDynamicUpdate` -> `--- PASS` (the plan's `TestDynamicUpdate` does not exist; this is the dynamic update e2e test).
- `gofmt -l mgmt/internal/zone` empty; `go vet ./mgmt/internal/zone/` clean.
- Deviations (plan text updated): `mgmt/internal/zone/model.go` touched for `RebuildOptions.Edit`/`EditDelta`; apex NS count comes from the owner/ancestor query (apex always included) instead of a separate count; DNAME descendant check selects one name (`LIMIT 1`) for the error message; `rebuildEdit` reads the served serial at `current_seq` to keep the forced image when the row serial was changed outside `Rebuild`; test uses a bulk insert plus `RebuildWithImageForTest`.
