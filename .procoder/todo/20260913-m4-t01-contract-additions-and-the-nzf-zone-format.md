# M4 Task 1: Contract additions and the NZF zone format

Status: open
Created: 2026-09-13

## Description

Implement Task 1 ("Contract additions and the NZF zone format") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 1 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [ ] `TestCanonicalOrderRFC4034` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestCompressIsContentAddressed` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestDecodeRejectsMalformed` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestDeltaGoldenAndAfterImage` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestFullImageRoundTripAndGolden` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestM4FieldNumbers` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestOutOfZoneOwnerRefused` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestSerialArithmeticRFC1982` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

