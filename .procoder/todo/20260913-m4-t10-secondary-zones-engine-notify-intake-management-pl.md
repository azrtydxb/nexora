# M4 Task 10: Secondary zones — engine NOTIFY intake, management-plane AXFR/IXFR pulls and SOA timers

Status: open
Created: 2026-09-13

## Description

Implement Task 10 ("Secondary zones — engine NOTIFY intake, management-plane AXFR/IXFR pulls and SOA timers") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 10 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [ ] `TestInterpret` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestInterpretAXFRStyleAndUpToDate` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestInterpretIncremental` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestRefreshAXFRThenIXFRThenUpToDate` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestRefreshFailureRetriesAndExpires` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

