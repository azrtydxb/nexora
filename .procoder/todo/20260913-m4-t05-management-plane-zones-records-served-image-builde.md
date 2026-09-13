# M4 Task 5: Management-plane zones, records, served-image builder, journal, snapshot and API

Status: open
Created: 2026-09-13

## Description

Implement Task 5 ("Management-plane zones, records, served-image builder, journal, snapshot and API") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 5 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [ ] `TestAddAuthZones` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestAddAuthZonesListsImageAndContiguousDeltas` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestAuthoritativeZonePropagation` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestCreateRecordBumpsSerialJournalAndPublishes` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestRebuildWithoutChangesKeepsSerial` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestSerialWrapsAroundRFC1982` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestStaleRecordRevisionConflicts` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestValidationRules` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

