# M7 T15: PKCS#11 session recovery (#27)

Status: open
Created: 2026-09-14

## Description

Implements Task 15 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/secrets/export_test.go`:
- [x] Add to `pkcs11_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/secrets -run TestPKCS11RecoversFromInvalidatedSessions'`. Expect FAIL `unseal 0 after the reset: "" pkcs11: 0xB3: CKR_SESSION_HANDLE_INVALID`.
- [x] In `pkcs11.go`:
- [x] In `secrets.go` `Unseal`, pass `ErrBackendUnavailable` from the unwrap through (a lost token is not a forged envelope).
- [x] Run `scripts/dev-exec.sh 'go test -count=1 -race ./mgmt/internal/secrets/... ./mgmt/internal/dnssec/...'` and expect all to pass, including `TestPKCS11SigningKeysStayInToken` and `TestSweepLeavesOtherInstallationsTokenKeys`.

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/secrets -run TestPKCS11RecoversFromInvalidatedSessions'` -> `--- FAIL: TestPKCS11RecoversFromInvalidatedSessions (0.06s) pkcs11_test.go:149: unseal 0 after the reset: "" envelope authentication failed` (Unseal hid the CKR_SESSION_HANDLE_INVALID).
- Green: same command with `-v` -> `--- PASS: TestPKCS11RecoversFromInvalidatedSessions (0.27s)`.
- `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test -count=1 -race ./mgmt/internal/secrets/... ./mgmt/internal/dnssec/...'` -> `ok .../mgmt/internal/secrets 1.956s`, `ok .../mgmt/internal/dnssec 54.313s` (includes TestPKCS11SigningKeysStayInToken, TestSweepLeavesOtherInstallationsTokenKeys).
- `go vet ./mgmt/internal/secrets/` OK in the pod; `gofmt -l mgmt/internal/secrets` empty.
- Deviations (plan text updated): the dev pod runs as root, so the test writes a wrong PIN instead of chmod 0; `Unseal` in `secrets.go` now returns `ErrBackendUnavailable` from the unwrap as is; the unused `module` field was left out; slot lookup and login are shared helpers `findSlot`/`login`.
