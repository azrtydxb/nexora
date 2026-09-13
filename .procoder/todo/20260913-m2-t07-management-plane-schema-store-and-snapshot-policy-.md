# M2 Task 7: Management plane schema, store and snapshot policy section

Status: open
Created: 2026-09-13

## Description

Implement Task 7 ("Management plane schema, store and snapshot policy section") of milestone M2 exactly as specified in
`.procoder/plans/nexora-v1-m2.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 7 in `.procoder/plans/nexora-v1-m2.md` is done as written (deviations recorded in the plan first)
- [ ] `TestBuildPolicySection` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestPolicyGroups` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestPolicyGroupsRevisionAndCIDRUniqueness` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestRewritesScopes` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestRewritesScopesAndGlobalSafeSearch` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

