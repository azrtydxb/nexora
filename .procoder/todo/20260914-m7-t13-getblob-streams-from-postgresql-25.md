# M7 T13: GetBlob streams from PostgreSQL (#25)

Status: open
Created: 2026-09-14

## Description

Implements Task 13 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Add to `control_test.go` (imports `runtime`, `time`, `crypto/sha256`):
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/control -run "TestGetBlob"'`. Expect `TestGetBlobDoesNotHoldWholeBlob` to FAIL with `heap 6x MiB while a 64 MiB blob is mid-stream`; `TestGetBlobChunkBoundaries` passes today and guards the rewrite.
- [x] Rewrite the body of `GetBlob` after authentication and the `sha256RE` check (delete the `debt:` comment):
- [x] Create `mgmt/migrations/00801_blobs_storage_external.sql`:
- [x] Run `scripts/dev-exec.sh 'go test -count=1 -race ./mgmt/internal/control/... ./mgmt/internal/store/...'` and expect all to pass, including `TestGetBlobStreamsMiBChunks` and the lifecycle GetBlob tests.

## Evidence

All commands ran in the second dev pod (`NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh '...'`).

- Red: `go test -count=1 ./mgmt/internal/control -run "TestGetBlob"` before the rewrite:
  `--- FAIL: TestGetBlobDoesNotHoldWholeBlob` `control_test.go:291: heap 199 MiB while a 64 MiB blob is mid-stream`;
  `TestGetBlobStreamsMiBChunks` and `TestGetBlobChunkBoundaries` passed.
- Green: same command after the rewrite: all three `--- PASS`, `ok github.com/piwi3910/nexora/mgmt/internal/control 10.872s`.
- Flake found and fixed in the test: in 2 of 26 full `-race` runs the heap read 75 MiB. With a temporary
  heap log it was 9-14 MiB normally. The cause is pgx v5.11 `ExtendedQueryBuilder.reset`, which truncates
  `ParamValues` to `[0:0]` and keeps the backing array. The pool connection that ran the test's own 64 MiB
  insert kept the encoded parameter until its next query. The test now inserts on an acquired connection
  and closes it with `Hijack().Close`. The 40 MiB assertion is unchanged; plan text updated.
- After the test fix: `go vet ./mgmt/internal/control/` clean and
  `go test -count=1 -race ./mgmt/internal/control/... ./mgmt/internal/store/...` 9/9 runs
  `ok .../mgmt/internal/control ~55s`, `ok .../mgmt/internal/store ~22s`. This includes
  `TestGetBlobStreamsMiBChunks`, the lifecycle GetBlob tests and migration 00801 (applied by every fixture's `st.Migrate`).
- Mutation: the old whole-blob `select data` body with the final test gives
  `control_test.go:300: heap 134 MiB while a 64 MiB blob is mid-stream` (FAIL); restored.
- `gofmt -l mgmt/internal/control` empty.
- Not done here (lead): commit.

