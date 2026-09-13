# M1 Task 19: GUI foundation, auth screens, Playwright harness and `TestAuthRBACAuditOIDC`

Status: done
Created: 2026-09-13

## Description

Implement Task 19 ("GUI foundation, auth screens, Playwright harness and `TestAuthRBACAuditOIDC`") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 19 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] `TestAuthRBACAuditOIDC` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Red: `go vet ./e2e/` before the harness -> `e2e/auth_test.go:13:12: env.InitCA undefined` (RunPlaywright/StartMgmt/Bootstrap also missing).
- Red (OIDC outage after discovery): first pod run of `TestAuthRBACAuditOIDC` -> `auth.spec.ts` 2 passed, `auth-oidc-down.spec.ts` failed (`login-error` not found: cached discovery redirected to the dead provider). Added the assertion to `TestOIDCLoginAndProviderDown` -> `service_test.go:176: provider down after discovery -> <nil>` (FAIL); fixed with a discovery probe in `OIDC.Start` -> `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/auth/ ./mgmt/internal/api/'` -> `ok .../auth 8.242s`, `ok .../api 5.276s`.
- Green: `scripts/dev-exec.sh 'make web-build && go build -o /tmp/t19bin/nexora-mgmt ./mgmt/cmd/nexora-mgmt && go build -o /tmp/t19bin/nexora-fixture ./e2e/fixtures/cmd/nexora-fixture && NEXORA_E2E_BIN_DIR=/tmp/t19bin go test -count=1 -v -run TestAuthRBACAuditOIDC ./e2e/'` -> Playwright `2 passed` + `1 passed`, `--- PASS: TestAuthRBACAuditOIDC (14.65s)`. Binaries built directly instead of `make e2e-build` so concurrent engine work could not break the run.
- `scripts/dev-exec.sh make web-test` -> exit 0 (typecheck, eslint, `permission parity: 42 operations match`, vite build).
- Parity check mutation: `listJoinTokens: "operator"` in permissions.ts -> `permission parity check failed: listJoinTokens: permissions.ts operator, Go admin` (exit 1); reverted.
- `pnpm audit` -> `No known vulnerabilities found` (react-router ^7.18.2).
- Screens reviewed in light and dark at 1440 px and 400 px with a throwaway Playwright screenshot run (not committed).
