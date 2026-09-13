# M5 Task 5: Per-group snapshots and publishing

Status: open
Created: 2026-09-13

## Description

Implement Task 5 ("Per-group snapshots and publishing") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 5 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [ ] `TestPublishPerGroup` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestScopeMerge` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

