# M4 Task 5: Management-plane zones, records, served-image builder, journal, snapshot and API

Status: open
Created: 2026-09-13

## Description

Implement Task 5 ("Management-plane zones, records, served-image builder, journal, snapshot and API") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 5 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [x] `TestAddAuthZones` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestAddAuthZonesListsImageAndContiguousDeltas` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestAuthoritativeZonePropagation` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestCreateRecordBumpsSerialJournalAndPublishes` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestRebuildWithoutChangesKeepsSerial` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestSerialWrapsAroundRFC1982` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestStaleRecordRevisionConflicts` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestValidationRules` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- Red first: `scripts/dev-exec.sh 'go test ./mgmt/internal/zone/ -count=1'` -> FAIL build: `undefined: zone.Service`.
- `scripts/dev-exec.sh 'go test ./mgmt/internal/zone/ ./mgmt/internal/snapshot/ ./mgmt/internal/blocklist/ -count=1'` -> ok zone, ok snapshot, ok blocklist.
- `scripts/dev-exec.sh 'go test ./mgmt/... ./e2e/harness/ -count=1'` -> all ok (incl. api TestPermissionsCoverEveryOperation); `gofmt -l mgmt e2e bench` empty; `go vet ./...` clean; `pnpm lint` -> "permission parity: 88 operations match".
- API regenerated: oapi-codegen v2.8.0 in the pod (gen.go copied back), `pnpm --dir web run gen:api` on the laptop.
- `make e2e-build` (engine at 87c30f7) then `go test ./e2e/ -run "TestAuthoritativeZonePropagation|TestZoneFileRoundTrip" -count=2 -v` -> PASS x2 each.
- Deviations recorded in the plan ("Built (as implemented)" under Task 5). TestGUICoverage stays red for the new zone operations until Task 15 (GUI).
- Not committed (lead commits).
