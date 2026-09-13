# M1 Task 14: Authentication and authorization — passwords, sessions, API tokens, RBAC, setup token, OIDC

Status: closed 2026-09-13
Created: 2026-09-13

## Description

Implement Task 14 ("Authentication and authorization — passwords, sessions, API tokens, RBAC, setup token, OIDC") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 14 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] `TestArgon2idHashAndVerify` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestOIDCLoginAndProviderDown` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestRBACMatrix` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestSessionsTokensAndDisabledUsers` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestSetupTokenIsSingleAndConsumedOnce` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Failing first: `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/auth/...'` -> `undefined: auth.HashPassword` (and the rest of the auth API).
- Plan deviations (plan text updated): Task 10 had not run, so this task created the harness process lifecycle (`FreePort`, `Bin`, `Start`, `Proc`), `StartOIDCFixture` and the `nexora-fixture oidc` subcommand with Task 10's signatures; tests build only `bin/nexora-fixture` instead of `make e2e-build`. Additive API: `Principal.TokenName`, `auth.UserColumns`/`auth.ScanUser`, `auth.SafeReturnTo`; a token authenticates with min(token role, owner's current role); `CompleteSetup` writes a `completeSetup` audit row.
- `scripts/dev-exec.sh 'go build -o bin/nexora-fixture ./e2e/fixtures/cmd/nexora-fixture && go test -race -count=1 -v ./mgmt/internal/auth/...'` -> `--- PASS: TestArgon2idHashAndVerify`, `TestRBACMatrix`, `TestSetupTokenIsSingleAndConsumedOnce`, `TestSessionsTokensAndDisabledUsers`, `TestOIDCLoginAndProviderDown`; `ok github.com/piwi3910/nexora/mgmt/internal/auth 26.885s`; no data races.
- `gofmt -l mgmt e2e` empty, `go vet ./mgmt/... ./e2e/...` clean; commit gate passed.
- Committed e746461.
