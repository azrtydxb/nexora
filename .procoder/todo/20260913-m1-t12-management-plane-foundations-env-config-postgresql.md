# M1 Task 12: Management plane foundations — env config, PostgreSQL store and schema, PKI, CLI

Status: open
Created: 2026-09-13

## Description

Implement Task 12 ("Management plane foundations — env config, PostgreSQL store and schema, PKI, CLI") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 12 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [ ] `TestCAInitLoadAndIssue` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestJoinTokenFormat` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestLoadDefaults` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestLoadValidation` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestMigrateSeedsAndMapsErrors` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

