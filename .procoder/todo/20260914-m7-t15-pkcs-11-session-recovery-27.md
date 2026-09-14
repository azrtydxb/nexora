# M7 T15: PKCS#11 session recovery (#27)

Status: open
Created: 2026-09-14

## Description

Implements Task 15 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create `mgmt/internal/secrets/export_test.go`:
- [ ] Add to `pkcs11_test.go`:
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/secrets -run TestPKCS11RecoversFromInvalidatedSessions'`. Expect FAIL `unseal 0 after the reset: "" pkcs11: 0xB3: CKR_SESSION_HANDLE_INVALID`.
- [ ] In `pkcs11.go`:
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 -race ./mgmt/internal/secrets/... ./mgmt/internal/dnssec/...'` and expect all to pass, including `TestPKCS11SigningKeysStayInToken` and `TestSweepLeavesOtherInstallationsTokenKeys`.

## Evidence

