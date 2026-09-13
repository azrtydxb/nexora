# M3 Task 11: Management plane — migrations, OpenAPI operations, handlers, snapshot builder, stats ingestion

Status: open
Created: 2026-09-13

## Description

Implement Task 11 ("Management plane — migrations, OpenAPI operations, handlers, snapshot builder, stats ingestion") of milestone M3 exactly as specified in
`.procoder/plans/nexora-v1-m3.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 11 in `.procoder/plans/nexora-v1-m3.md` is done as written (deviations recorded in the plan first)
- [ ] `TestApplyM3` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestPackIsZstdOfContent` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestValidateDS` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestValidateDomainAndForwardAddresses` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestValidateZoneCountsRecordsAndSerial` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestValidateZoneRejectsIncludeMissingSOAAndSyntax` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

