# M2 Task 9: Management plane DNS TLS certificate loading, push and status

Status: open
Created: 2026-09-13

## Description

Implement Task 9 ("Management plane DNS TLS certificate loading, push and status") of milestone M2 exactly as specified in
`.procoder/plans/nexora-v1-m2.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 9 in `.procoder/plans/nexora-v1-m2.md` is done as written (deviations recorded in the plan first)
- [ ] `TestDNSTLSFanout` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestDNSTLSFanoutPushesOnlyWhenFingerprintDiffers` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestDNSTLSWatcher` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestDNSTLSWatcherRotationKeepsLastGood` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestDnsTlsStatusWithoutCertificate` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestIssueAndLoadDNSTLS` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

