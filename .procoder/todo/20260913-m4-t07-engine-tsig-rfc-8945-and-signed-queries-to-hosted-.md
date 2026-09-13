# M4 Task 7: Engine TSIG (RFC 8945) and signed queries to hosted zones

Status: open
Created: 2026-09-13

## Description

Implement Task 7 ("Engine TSIG (RFC 8945) and signed queries to hosted zones") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 7 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [ ] `TestTSIGVectors` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

