# M3 Task 11: Management plane — migrations, OpenAPI operations, handlers, snapshot builder, stats ingestion

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 11 ("Management plane — migrations, OpenAPI operations, handlers, snapshot builder, stats ingestion") of milestone M3 exactly as specified in
`.procoder/plans/nexora-v1-m3.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 11 in `.procoder/plans/nexora-v1-m3.md` is done as written (deviations recorded in the plan first)
- [x] `TestApplyResolution` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestPackIsZstdOfContentAndPurposeNamesZone` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestValidateDS` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestValidateDomainAndForwardAddresses` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestValidateZoneCountsRecordsAndSerial` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestValidateZoneRejectsIncludeMissingSOASyntaxAndSize` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed (lead commits)

## Evidence

- Red first: `scripts/dev-exec.sh 'go test ./mgmt/internal/secrets/ ./mgmt/internal/rpz/ ./mgmt/internal/dnssecconf/ ./mgmt/internal/snapshot/'` -> build failures `undefined: ValidateZone`, `undefined: store.ResolutionRows`, no non-test files in secrets; `go vet` of control/stats tests -> `undefined: control.NewRPZTsig`, `undefined: stats.RecordM3`; `go build ./mgmt/...` after regenerating gen.go -> `missing method CreateForwardZone`.
- Green: same packages `ok`; `go test ./mgmt/internal/api/ ./mgmt/internal/stats/ -run 'TestResolutionDnssecAndRPZAPI|TestRecordM3|TestPermissionsCoverEveryOperation'` -> ok; `TestResolutionAndRPZLifecycleWithKeyStorage` -> PASS.
- `scripts/dev-exec.sh 'make mgmt-test'` -> every mgmt/gen/bench package `ok` (race, count=1).
- `pnpm run lint` in the pod -> `permission parity: 77 operations match`.
- Blob GC regression: `TestCollectBlobsKeepsRPZZoneFiles` fails without the fetcher fix (`violates foreign key constraint "rpz_zones_blob_sha256_fkey"`), passes with it.
- e2e: see the task report (full `go test ./e2e/...` run in the dev pod).

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in f1f244f
