# M1 Task 16: Engine management-plane client and control-plane acceptance tests

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 16 ("Engine management-plane client and control-plane acceptance tests") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 16 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] `TestInvalidSnapshotRejected` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestMgmtStatelessHA` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] Rust tests pass in the dev pod: `backoff_grows_jitters_and_caps`, `identity_round_trip_with_private_key_mode`, `join_token_parsing`
- [x] procoder gate clean over the changed files; work committed

## Evidence

Closing evidence (lead, 2026-09-14):

- plan steps: done; deviations recorded as As-built notes in the plan; deferred integration steps completed in later commits (git log)
- TestInvalidSnapshotRejected: passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- TestMgmtStatelessHA: passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- backoff_grows_jitters_and_caps, identity_round_trip_with_private_key_mode, join_token_parsing: passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- gate/commit: commit gate passed on every commit for this task; todo last committed in 18b1908
