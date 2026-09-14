# M5 Task 8: Engine lifecycle in the management plane

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 8 ("Engine lifecycle in the management plane") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 8 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [x] `TestConsumeJoinToken` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestIssueEngineCert` passes in the dev pod (`scripts/dev-exec.sh`) (N/A name; covered by TestEngineCertificateRenewalPointIsInTheFuture, TestCertificateIssuanceRateLimitIsPerEngine)
- [x] `TestMgmtCLIFleet` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestRevocationChecker` passes in the dev pod (`scripts/dev-exec.sh`) (N/A name; covered by TestEngineCertRevocation (e2e) and the control lifecycle tests)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- `scripts/dev-exec.sh 'go test ./mgmt/internal/control/ -v -run "TestConsumeJoinToken|TestCertificateRenewalRotationAndRevocation"'` -> `--- PASS: TestConsumeJoinToken (2.23s)`, `--- PASS: TestCertificateRenewalRotationAndRevocation (2.40s)` (vet failed first with `s.EngineCertTTL undefined`).
- `go test ./mgmt/internal/api/ -run TestEngineLifecycleAuditAndHashedJoinTokens` (in make mgmt-test) -> ok: secret stored only as SHA-256, revoke revokes all certificates and notifies, 409 engine_revoked, every lifecycle action audited.
- `go test ./mgmt/internal/config/` -> ok (NEXORA_ENGINE_CERT_TTL default 2160h, 10s and unparsable refused).
- `make mgmt-test` -> exit 0; `go test ./e2e/ -run TestMgmtCLIFleet -v` -> `--- PASS: TestMgmtCLIFleet (3.26s)`; full e2e suite green except TestGUICoverage (Task 11 operations).
- `TestIssueEngineCert` / `TestRevocationChecker`: not in the plan or repo (stale criteria); covered by TestCertificateRenewalRotationAndRevocation.
- Not committed (lead commits).

- Pre-release sweep (2026-09-14): issuance limit moved from per stream to per engine (`engines.cert_renewed_at`, migration 00501, checked under the engine row lock). Red: `go test -run TestCertificateIssuanceRateLimitIsPerEngine ./mgmt/internal/control/` -> `a reconnected stream got a second certificate within the renewal interval`; green: `go test -race -count=1 ./mgmt/internal/control/ ./mgmt/internal/store/ ./mgmt/internal/fleet/` -> ok; `TestEngineCertRevocation` (e2e, 60 s TTL renewals) -> PASS (103.25s).

Closing evidence (lead, 2026-09-14):

- TestIssueEngineCert: N/A — no test with this name exists (criterion auto-generated from plan text); covered by TestEngineCertificateRenewalPointIsInTheFuture, TestCertificateIssuanceRateLimitIsPerEngine, which passed in the full suite passed in the dev pod during the pre-release hardening sweep at 93dcd04
- TestRevocationChecker: N/A — no test with this name exists (criterion auto-generated from plan text); covered by TestEngineCertRevocation (e2e) and the control lifecycle tests, which passed in the full suite passed in the dev pod during the pre-release hardening sweep at 93dcd04
- gate/commit: commit gate passed; committed in 93dcd04 and follow-ups
