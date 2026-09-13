# M5 Task 8: Engine lifecycle in the management plane

Status: open
Created: 2026-09-13

## Description

Implement Task 8 ("Engine lifecycle in the management plane") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 8 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [ ] `TestConsumeJoinToken` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestIssueEngineCert` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestMgmtCLIFleet` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestRevocationChecker` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

