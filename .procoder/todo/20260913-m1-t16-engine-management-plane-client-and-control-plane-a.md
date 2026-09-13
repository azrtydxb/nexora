# M1 Task 16: Engine management-plane client and control-plane acceptance tests

Status: open
Created: 2026-09-13

## Description

Implement Task 16 ("Engine management-plane client and control-plane acceptance tests") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 16 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [ ] `TestInvalidSnapshotRejected` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestMgmtStatelessHA` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] Rust tests pass in the dev pod: `backoff_grows_jitters_and_caps`, `identity_round_trip_with_private_key_mode`, `join_token_parsing`
- [ ] procoder gate clean over the changed files; work committed

## Evidence

