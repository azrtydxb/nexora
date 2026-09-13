# M4 Task 9: BIND zone file import and export

Status: open
Created: 2026-09-13

## Description

Implement Task 9 ("BIND zone file import and export") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 9 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [ ] `TestExportIsStableAndReparses` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestParseAllTypes` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestParseRefusals` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestZoneFileRoundTrip` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

