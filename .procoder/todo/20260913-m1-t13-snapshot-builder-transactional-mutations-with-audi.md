# M1 Task 13: Snapshot builder, transactional mutations with audit, and the EngineControl gRPC server

Status: open
Created: 2026-09-13

## Description

Implement Task 13 ("Snapshot builder, transactional mutations with audit, and the EngineControl gRPC server") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 13 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [ ] `TestBuildMapsEveryTable` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestConnectWithoutClientCertIsUnauthenticated` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestEnrollConnectPushAckReject` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestGetBlobStreamsMiBChunks` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestMutateWritesRowsAuditVersionAndNotifies` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestNotifyFansOutToEngineOnOtherInstance` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestVersionAheadIsFlaggedNotDowngraded` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

