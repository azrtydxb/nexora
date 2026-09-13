# M4 Task 14: Key rollovers, CDS/CDNSKEY, DNSSEC API, and the signing/key-storage acceptance tests

Status: open
Created: 2026-09-13

## Description

Implement Task 14 ("Key rollovers, CDS/CDNSKEY, DNSSEC API, and the signing/key-storage acceptance tests") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 14 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [ ] `TestAutomatic` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestAutomaticZSKRolloverAtLifetime` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestDNSSECSigningRollover` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestKSK` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestKSKDoubleSignatureTimeline` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestKeyStorageBackends` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestZSK` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestZSKPrePublishTimeline` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

