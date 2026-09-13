# M1 Task 10: End-to-end harness and fixture servers

Status: open
Created: 2026-09-13

## Description

Implement Task 10 ("End-to-end harness and fixture servers") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 10 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [ ] `TestDNSFixtureBehaviours` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestHarnessOtelcolStarts` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestHarnessPostgres` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestHarnessStandaloneEngineAnswersViaFixture` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

