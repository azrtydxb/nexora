# M2 Task 9: Management plane DNS TLS certificate loading, push and status

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 9 ("Management plane DNS TLS certificate loading, push and status") of milestone M2 exactly as specified in
`.procoder/plans/nexora-v1-m2.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 9 in `.procoder/plans/nexora-v1-m2.md` is done as written (deviations recorded in the plan first)
- [x] `TestDNSTLSFanout` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestDNSTLSFanoutPushesOnlyWhenFingerprintDiffers` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestDNSTLSWatcher` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestDNSTLSWatcherRotationKeepsLastGood` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestDnsTlsStatusWithoutCertificate` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestIssueAndLoadDNSTLS` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./mgmt/internal/pki/ -run "TestIssueAndLoadDNSTLS|TestDNSTLSWatcher"; go test ./mgmt/internal/control/ -run TestDNSTLSFanout'` -> build FAIL `ca.IssueDNSServerCert undefined (type *pki.CA has no field or method IssueDNSServerCert)`, `undefined: pki.DNSTLSMaterial` (control test, same missing type as the plan's stated `control.NewDNSTLSFanout`).
- Green: same command with `-race -v` -> `--- PASS: TestIssueAndLoadDNSTLS`, `--- PASS: TestDNSTLSWatcherRotationKeepsLastGood`, `--- PASS: TestDNSTLSFanoutPushesOnlyWhenFingerprintDiffers`.
- `scripts/dev-exec.sh 'go test ./mgmt/internal/api/ ./mgmt/internal/config/ -run "TestDnsTlsStatusWithoutCertificate|TestLoad" -v'` -> `--- PASS: TestDnsTlsStatusWithoutCertificate (3.85s)`, `--- PASS: TestLoadDefaults`, `--- PASS: TestLoadValidation` (new cases: cert only, key only, 500ms interval).
- Generation: oapi-codegen v2.8.0 in pod (output to a temp dir, copied back); regenerating after prettier-formatting openapi.yaml gave a byte-identical gen.go; `cd web && pnpm run gen:api` (openapi-typescript 7.13.0); `node scripts/check-permissions.mjs` -> `permission parity: 54 operations match`.
- `scripts/dev-exec.sh 'go vet ./mgmt/... && make mgmt-test'` -> all ok (api 49.5s, auth, blocklist, config, control, pki, querylog, snapshot, stats, store, gen, bench).
- CLI smoke in pod: `ca init` then `ca issue-dns --names dns.nexora.test,127.0.0.1` -> `wrote .../tls.crt and .../tls.key (fingerprint 86c5...)`, tls.crt 0644 with 2 certificates, tls.key 0600 `BEGIN PRIVATE KEY`, SAN `DNS:dns.nexora.test, IP Address:127.0.0.1`, notAfter +90d; missing flags -> usage, exit 1.
- `procoder check` over changed files -> 15 clean, 0 unformatted, 0 blocking.
- Engine-side acceptance (real TlsMaterialResult from an engine) not exercised: engine Task 3 is uncommitted; covered by M2 Task 11 e2e.
- Not committed (the lead commits serially per the implementer brief).

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in 2f7cb07
