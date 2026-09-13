# M3 Task 14: Acceptance tests — TestRecursionRootHints, TestSpoofedReplyRejected, TestDNSSECValidation, TestRPZPolicy

Status: open
Created: 2026-09-13

## Description

Implement Task 14 ("Acceptance tests — TestRecursionRootHints, TestSpoofedReplyRejected, TestDNSSECValidation, TestRPZPolicy") of milestone M3 exactly as specified in
`.procoder/plans/nexora-v1-m3.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 14 in `.procoder/plans/nexora-v1-m3.md` is done as written (deviations recorded in the plan first)
- [ ] `TestDNSSECValidation` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestRPZPolicy` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestRecursionRootHints` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestSpoofedReplyRejected` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

