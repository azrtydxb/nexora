# M1 Task 11: Forwarding acceptance tests over the wire

Status: open
Created: 2026-09-13

## Description

Implement Task 11 ("Forwarding acceptance tests over the wire") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 11 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [ ] `TestDedupAllWaitersAnswered` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestEDNSTruncationTCP` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestForwardCacheTTL` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestMain` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestUpstreamFailover` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

