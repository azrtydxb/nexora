# M7 T13: GetBlob streams from PostgreSQL (#25)

Status: open
Created: 2026-09-14

## Description

Implements Task 13 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Add to `control_test.go` (imports `runtime`, `time`, `crypto/sha256`):
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/control -run "TestGetBlob"'`. Expect `TestGetBlobDoesNotHoldWholeBlob` to FAIL with `heap 6x MiB while a 64 MiB blob is mid-stream`; `TestGetBlobChunkBoundaries` passes today and guards the rewrite.
- [ ] Rewrite the body of `GetBlob` after authentication and the `sha256RE` check (delete the `debt:` comment):
- [ ] Create `mgmt/migrations/00801_blobs_storage_external.sql`:
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 -race ./mgmt/internal/control/... ./mgmt/internal/store/...'` and expect all to pass, including `TestGetBlobStreamsMiBChunks` and the lifecycle GetBlob tests.

## Evidence

