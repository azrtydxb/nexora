# M6 T21: Account GUI: profile page, change password dialog, menu items

Status: open
Created: 2026-09-14

## Description

Implements Task 21 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `e2e/gui_seed_account_test.go`:
- [x] Run
- [x] Implement:
- [x] Run the same command and expect `33-account.spec.ts`, `06-users.spec.ts`, `11-oidc.spec.ts` and
- [ ] Report the paths. Commit message: `gui: account profile and password change`. (paths reported; the
      lead commits)

## Evidence

- Build note: `make e2e-build` failed in the shared pod on another wave-2 task's in-progress engine edit
  (`engine/src/main.rs: unresolved import nexora_engine::eprintln`). The runs therefore used a private
  bin dir `/tmp/t21-bin` with the pod's existing `bin/nexora-engine` (built 2026-09-14 19:50) plus a
  fresh `make web-build` and `go build` of `nexora-mgmt` and `nexora-fixture`.
- Red: `NEXORA_E2E_BIN_DIR=/tmp/t21-bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m` with the
  spec and seed only: both `33-account.spec.ts` tests failed, "waiting for getByTestId('menu-profile')".
- Green: `make web-build && go build -o /tmp/t21-bin/nexora-mgmt ./mgmt/cmd/nexora-mgmt &&
  NEXORA_E2E_BIN_DIR=/tmp/t21-bin go test ./e2e -run "TestGUICoverage|TestAuthRBACAuditOIDC" -count=1 -timeout 60m`:
  playwright "32 passed, 1 failed"; `33-account.spec.ts` (both tests), `06-users.spec.ts` and
  `11-oidc.spec.ts` passed. The one failure is `30-access-control-split.spec.ts` (Task 20, "missing
  environment variable NEXORA_E2E_ACL_ZONE"), so `TestGUICoverage` as a whole still FAILS until Task 20
  lands.
- `go test ./e2e -run "^TestAuthRBACAuditOIDC$" -count=1 -v`: `--- PASS: TestAuthRBACAuditOIDC (16.58s)`.
- Laptop: `pnpm run typecheck` clean; `pnpm run lint` clean ("permission parity: 120 operations match",
  check-help ok); prettier check clean on changed files; `gofmt -l` and `go vet ./e2e` clean.
