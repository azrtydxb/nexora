# M6 T7: Account self-service backend

Status: open
Created: 2026-09-14

## Description

Implements Task 7 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/api/account_test.go`:
- [x] Create `mgmt/internal/auth/account_test.go` (`TestLoginThrottleIsPerUsernameAndClient`)
- [x] Run `scripts/dev-exec.sh 'make webui-placeholder && go test ./mgmt/internal/api -run TestAccountSelfService -count=1'`
- [x] Create `mgmt/migrations/00602_user_profile.sql`:
- [x] Implement `mgmt/internal/auth/account.go`:
- [x] Implement `mgmt/internal/api/account.go`:
- [x] Run
- [ ] Report the paths. Commit message: `auth: self-service profile, password change, failure throttling`.
      (paths reported; the lead commits)

## Evidence

- Red (HEAD plus only `mgmt/internal/api/account_test.go`, isolated copy in the dev pod because the
  shared tree briefly did not build from another task's in-progress querylog edits):
  `go test ./mgmt/internal/api -run TestAccountSelfService -count=1` ->
  `account_test.go:37: last_login_at not set by login` / `FAIL`.
- Mutation (full implementation with `and client = $2` removed from the throttle count):
  `go test ./mgmt/internal/auth -run TestLoginThrottleIsPerUsernameAndClient -count=1` ->
  `admin from another address locked out: too many failed attempts; try again later` / `FAIL`.
- Green: `scripts/dev-exec.sh 'make webui-placeholder && go test ./mgmt/internal/api ./mgmt/internal/auth -count=1'`
  -> `ok github.com/piwi3910/nexora/mgmt/internal/api 85.522s`,
  `ok github.com/piwi3910/nexora/mgmt/internal/auth 23.929s` (includes `TestAccountSelfService`,
  `TestSetupCRUDConflictAuditAndRBAC`, `TestSessionsTokensAndDisabledUsers`,
  `TestLoginThrottleIsPerUsernameAndClient`, all `--- PASS` in a `-v` run).
- `gofmt -l mgmt/internal/auth mgmt/internal/api` empty; `go vet ./mgmt/internal/auth ./mgmt/internal/api`
  clean; `golangci-lint run` on both packages reports no finding in the files of this task.
- Deviation: the throttle is keyed on username and client address (spec S-15, lead decision);
  `Login` and `ChangePassword` gain a `client` argument. Plan text updated.
