# M4 Task 6: Key storage (KEK envelope, PKCS#11), TSIG keys, KeyMaterial delivery

Status: open
Created: 2026-09-13

## Description

Implement Task 6 ("Key storage (KEK envelope, PKCS#11), TSIG keys, KeyMaterial delivery") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 6 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [ ] `TestCreateStoresOnlyEnvelopeAndBumpsGeneration` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestDeleteInUseKeyFails` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestEnvelopeRoundTripBindsPurposeAndDetectsTamper` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestKEKFileValidation` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestKEKSigningKeyIsEnvelopedPKCS8` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestPKCS11SigningKeysStayInToken` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestPKCS11WrapsEnvelopesWhenNoKEKFile` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestUnconfiguredKeyStorageRefuses` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestUnconfiguredStoreRefusesSecrets` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestWrongKEKIsReported` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

