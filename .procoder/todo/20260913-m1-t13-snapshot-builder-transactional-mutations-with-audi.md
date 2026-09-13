# M1 Task 13: Snapshot builder, transactional mutations with audit, and the EngineControl gRPC server

Status: closed 2026-09-13
Created: 2026-09-13

## Description

Implement Task 13 ("Snapshot builder, transactional mutations with audit, and the EngineControl gRPC server") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 13 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] `TestBuildMapsEveryTable` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestConnectWithoutClientCertIsUnauthenticated` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestEnrollConnectPushAckReject` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestGetBlobStreamsMiBChunks` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestMutateWritesRowsAuditVersionAndNotifies` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestNotifyFansOutToEngineOnOtherInstance` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestVersionAheadIsFlaggedNotDowngraded` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Failing first: `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/snapshot/... ./mgmt/internal/control/...'` -> `no required module provides package github.com/piwi3910/nexora/mgmt/internal/auth` (setup failed).
- Plan deviations (plan text updated): `serve` adds `keepalive.EnforcementPolicy{MinTime: 5s, PermitWithoutStream: true}` (Task 16 engine pings every 10 s); hub LISTENs on a connection outside the pool (a pooled one blocked `Store.Close` in test cleanup; the first run hung until the 240 s timeout); `Connect` upserts its `instances` row before setting `connected_instance` (FK) and rejects a Hello `engine_id` differing from the cert CN; Applied/Rejected raise the subscriber version so the hub never re-pushes them; `GetBlob` also requires a live engine row and a well-formed sha256; `Latest` returns `store.ErrNotFound` when empty; graceful stop falls back to `Stop` after 5 s.
- `scripts/dev-exec.sh 'go test -race -count=1 -v ./mgmt/... ./gen/...'` -> `--- PASS: TestMutateWritesRowsAuditVersionAndNotifies`, `TestBuildMapsEveryTable`, `TestEnrollConnectPushAckReject`, `TestVersionAheadIsFlaggedNotDowngraded`, `TestConnectWithoutClientCertIsUnauthenticated`, `TestGetBlobStreamsMiBChunks`, `TestNotifyFansOutToEngineOnOtherInstance`; `ok .../snapshot 6.673s`, `ok .../control 14.038s`; no data races.
- `serve` smoke in the pod: `grpc listening on 127.0.0.1:40779`, SIGTERM -> exit 0; `config_versions` = `1|initialSnapshot config 1`, one `instances` row.
- `gofmt -l mgmt` empty, `go vet ./mgmt/... ./e2e/...` clean; `procoder check` -> 0 blocking.
- Committed ea2f618.
