# M9 T3: Bootstrap token and system users in the management plane

Status: open
Created: 2026-09-15

## Description

Implements Task 3 of `.procoder/plans/nexora-m9-platform.md` (spec `.procoder/specs/nexora-m9-platform.md`, milestone M9 Platform). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/auth/bootstrap_test.go`:
- [x] Implement `mgmt/internal/auth/bootstrap.go`. In one `s.st.InTx`:
- [x] In `mgmt/internal/auth/service.go` change the three `select count(*) from users` statements to
- [x] Create `mgmt/internal/api/system_user_test.go`:
- [x] In `handlers_admin.go`, after `lockUser` in `UpdateUser` and `DeleteUser`, return
- [x] Add `TestConfigBootstrapToken` to `mgmt/internal/config/config_test.go`: with only the required
- [x] In `main.go` `serve`, register `auth.BootstrapTokenErrors` next to `pki.DNSTLSReloadErrors`, and
- [x] Create `e2e/bootstrap_token_test.go`:
- [x] In `web/src/pages/UsersPage.tsx`, render the row actions only when `u.source !== "system"`. Create
- [x] Add `NEXORA_BOOTSTRAP_TOKEN_FILE` and `NEXORA_BOOTSTRAP_TOKEN_RELOAD_INTERVAL` (`30s`) to the

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'go test ./mgmt/internal/auth -run "Bootstrap|SystemUsers" -count=1'`
  → build failed `svc.EnsureBootstrapToken undefined`, `undefined: auth.BootstrapUsername`. Green after `bootstrap.go` and
  the `service.go` changes: `--- PASS` for TestBootstrapTokenEnsuresSystemUser, TestBootstrapTokenRotation,
  TestBootstrapTokenRefusesHumanUser, TestSetupRequiredIgnoresSystemUsers.
- Red: `go test ./mgmt/internal/api -run TestSystemUserIsReadOnlyInAPI -count=1` → `update system user -> 200`.
  Green after `handlers_admin.go`: `ok .../mgmt/internal/api 2.924s`.
- Red: `go test ./mgmt/internal/config -run TestConfigBootstrapToken` → build failed `c.BootstrapTokenFile undefined`;
  green: `ok .../mgmt/internal/config`.
- e2e: `make e2e-build && go test ./e2e -run TestBootstrapTokenFleetBootstrap -count=1` → `ok .../e2e 3.347s`. With the
  `go authSvc.RunBootstrapToken` line in `main.go` commented out (temporarily) → `FAIL ... condition not met within 10s:
not yet: the bootstrap token authenticates`.
- Spec 71: throwaway e2e test running `00-setup` + `71-system-user` (deleted) → red before the UsersPage change
  (`toHaveCount` `unexpected value "2"`), pass after. Full `go test ./e2e -run "^TestGUICoverage$" -count=1 -timeout 40m`:
  `71-system-user` ✓ and 33 specs passed; 1 failed, `30-access-control-split.spec.ts`
  (`missing environment variable NEXORA_E2E_ACL_ZONE`). That spec comes from M6 T20, nothing on m9 seeds that variable,
  and T3 does not touch it.
- `go vet ./...` → clean; `go test -race -count=1 ./mgmt/...` → every package ok; `gofmt -l mgmt e2e` → no output.
- `cd web && pnpm run typecheck && pnpm run lint` → pass (`permission parity: 120 operations match`).
- Not committed (lead commits).
