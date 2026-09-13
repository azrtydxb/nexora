# M5 Task 12: Release images workflow and docker-compose example

Status: open
Created: 2026-09-13

## Description

Implement Task 12 ("Release images workflow and docker-compose example") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 12 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [ ] `TestComposeExample` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestImagesWorkflow` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

