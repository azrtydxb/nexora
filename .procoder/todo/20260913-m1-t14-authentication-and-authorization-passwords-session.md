# M1 Task 14: Authentication and authorization — passwords, sessions, API tokens, RBAC, setup token, OIDC

Status: open
Created: 2026-09-13

## Description

Implement Task 14 ("Authentication and authorization — passwords, sessions, API tokens, RBAC, setup token, OIDC") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 14 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [ ] `TestArgon2idHashAndVerify` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestOIDCLoginAndProviderDown` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestRBACMatrix` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestSessionsTokensAndDisabledUsers` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestSetupTokenIsSingleAndConsumedOnce` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

