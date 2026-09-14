# M2 Task 11: E2E harness encrypted clients and `TestEncryptedTransports`

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 11 ("E2E harness encrypted clients and `TestEncryptedTransports`") of milestone M2 exactly as specified in
`.procoder/plans/nexora-v1-m2.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 11 in `.procoder/plans/nexora-v1-m2.md` is done as written (deviations recorded in the plan first)
- [x] `TestEncryptedTransports` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Red: `go vet ./e2e/` before the harness existed — `fx.SetRecords undefined (type *harness.DNSFixture has no field or method SetRecords)`.
- `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=bin go test -count=1 -v ./e2e/ -run TestEncryptedTransports'` — first run failed only `no-key-material-on-engine-disk`: the plan's `body[:40]` is the fixed PKCS#8 P-256 header and matched `state/identity/key.pem` (test bug). Replaced by `dnsTLSSecrets` (private scalar raw/hex/base64/PEM span, initial and rotated key, all files); plan updated. Then `--- PASS: TestEncryptedTransports (3.34s)` with all six subtests PASS.
- Mutation: writing the serving key DER to `state/leak.bin` made `no-key-material-on-engine-disk` FAIL ("contains DNS TLS key material"); reverted.
- `go test -count=4 -run "TestPerClientPolicy|TestEncryptedTransports"` — 4/4 PASS each.
- `go test ./e2e/fixtures/...` — `TestDNSFixtureStaticRecords` PASS.
- Not committed (lead commits).

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in 392f6f4
