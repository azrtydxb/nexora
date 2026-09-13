# M5 Task 10: Fleet acceptance tests

Status: open
Created: 2026-09-13

## Description

Implement Task 10 ("Fleet acceptance tests") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 10 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [ ] `TestAuthoritativeZonePropagation` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestCanaryRolloutHaltsOnFailure` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestEngineCertRevocation` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestFleetAPI` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestFleetRolloutAndPartition` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestGroupScopedConfig` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestInvalidSnapshotRejected` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestJoinTokenGroupAndExpiry` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestMgmtCLIFleet` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestMgmtStatelessHA` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

