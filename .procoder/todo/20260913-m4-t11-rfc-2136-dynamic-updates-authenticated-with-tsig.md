# M4 Task 11: RFC 2136 dynamic updates authenticated with TSIG

Status: open
Created: 2026-09-13

## Description

Implement Task 11 ("RFC 2136 dynamic updates authenticated with TSIG") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 11 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [ ] `TestPrerequisitesRFC2136` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestSecondaryAndDynamicUpdate` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestUpdateSectionAndSerial` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

