# M4 Task 6: Key storage (KEK envelope, PKCS#11), TSIG keys, KeyMaterial delivery

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 6 ("Key storage (KEK envelope, PKCS#11), TSIG keys, KeyMaterial delivery") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 6 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [x] `TestCreateStoresOnlyEnvelopeAndBumpsGeneration` passes in the dev pod (`scripts/dev-exec.sh`) (N/A names: TestCreateStoresOnlyEnvelopeAndBumpsGeneration)
- [x] `TestDeleteInUseKeyFails` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestEnvelopeRoundTripBindsPurposeAndDetectsTamper` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestKEKFileValidation` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestKEKSigningKeyIsEnvelopedPKCS8` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestPKCS11SigningKeysStayInToken` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestPKCS11WrapsEnvelopesWhenNoKEKFile` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestUnconfiguredKeyStorageRefuses` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestUnconfiguredStoreRefusesSecrets` passes in the dev pod (`scripts/dev-exec.sh`) (N/A names: TestUnconfiguredStoreRefusesSecrets)
- [x] `TestWrongKEKIsReported` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

Management-plane half of Task 6 (engine half — tsig.rs move, KeyRing, ServerMsg::KeyMaterial arm — is
built by the concurrent engine agent under `engine/**` and is not verified here). Not committed (lead commits).

- `TestCreateStoresOnlyEnvelopeAndBumpsGeneration` is named `TestCreateStoresOnlyEnvelopeAndPublishes` in the plan
  (no generation counter; a config version is published) — passes. `TestUnconfiguredStoreRefusesSecrets` is M3's
  `TestUnconfiguredBoxRefusesSecrets` — passes.
- Added security tests: `TestKEKFilePermissions` (world/group-readable KEK refused), `TestPKCS11PinFileIsRequiredAndChecked`,
  `TestSwappedEnvelopeOrIdentityIsRejected` (envelope moved between rows / algorithm changed fails), swap check in
  `TestKEKSigningKeyIsEnvelopedPKCS8`, audit + snapshot redaction in `TestCreateStoresOnlyEnvelopeAndPublishes`,
  `TestTSIGKeyAPISecretsAreWriteOnly` (list and /audit never return the secret, RBAC, 409 tsig_key_in_use),
  `TestTSIGKeyCreateWithoutKeyStorageIs503`, `TestKeyMaterialIsSentBeforeTheSnapshotAndOnChange`,
  e2e `TestTSIGKeysWithPKCS11OnlyKeyStorage`.
- `scripts/dev-exec.sh 'go test ./mgmt/internal/secrets/ -count=1'` before implementation: FAIL (undefined: secrets.Open ...).
- `scripts/dev-exec.sh 'gofmt -l mgmt e2e bench; go vet ./... ; go test -count=1 ./mgmt/...'`: no gofmt output, vet OK,
  every mgmt package `ok` (api, auth, blocklist, config, control, dnssecconf, nzf, pki, querylog, rpz, secrets, snapshot, stats, store, tsigkey, zone, zonefile).
- `scripts/dev-exec.sh 'CGO_ENABLED=0 go build ./mgmt/...'`: OK (stub backend).
- `NEXORA_E2E_BIN_DIR=<fresh mgmt build> go test -count=1 -run TestTSIGKeysWithPKCS11OnlyKeyStorage ./e2e/`: ok.
- `pnpm --dir web run lint`: permission parity: 91 operations match.

Closing evidence (lead, 2026-09-14):

- plan steps: done; deviations recorded as As-built notes in the plan; deferred integration steps completed in later commits (git log)
- TestCreateStoresOnlyEnvelopeAndBumpsGeneration: N/A — no test with this name exists (criterion was auto-generated from plan text); the behaviour is covered by the task's actual tests, which passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- TestUnconfiguredStoreRefusesSecrets: N/A — no test with this name exists (criterion was auto-generated from plan text); the behaviour is covered by the task's actual tests, which passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- gate/commit: commit gate passed on every commit for this task; todo last committed in 0cc688e
