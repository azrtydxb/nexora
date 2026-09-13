# M1 Task 2: Repository skeleton, control proto and code generation

Status: open
Created: 2026-09-13

## Description

Implement Task 2 ("Repository skeleton, control proto and code generation") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 2 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [ ] `TestConfigSnapshotRoundTrip` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] Rust tests pass in the dev pod: `config_snapshot_round_trips`, `main`
- [ ] procoder gate clean over the changed files; work committed

## Evidence

