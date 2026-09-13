# M4 Task 14: Key rollovers, CDS/CDNSKEY, DNSSEC API, and the signing/key-storage acceptance tests

Status: open
Created: 2026-09-13

## Description

Implement Task 14 ("Key rollovers, CDS/CDNSKEY, DNSSEC API, and the signing/key-storage acceptance tests") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 14 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [x] `TestAutomatic` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestAutomaticZSKRolloverAtLifetime` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestDNSSECSigningRollover` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestKSK` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestKSKDoubleSignatureTimeline` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestKeyStorageBackends` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestZSK` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestZSKPrePublishTimeline` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- `scripts/dev-exec.sh 'go test ./mgmt/internal/dnssec/ -run "TestZSK|TestKSK|TestAutomatic" -count=1'` failed first with "undefined: Advance", then PASS (3 tests).
- `go test ./mgmt/internal/dnssec/ -count=1 -v`: PASS incl. TestDisableDestroysTokenKeysOnlyAfterCommit (injected COMMIT failure, keys survive), TestSweepDestroysTokenKeysOfRolledBackTransactions, TestServiceRolloversAreScheduledAndGuarded, TestPKCS11KeysSignInsideTheToken, TestMaintainerRefreshesSignaturesUnderAdvisoryLock.
- `cargo test --locked -p nexora-engine --lib notify_from_keyed`: ok (notify_from_keyed_primary_requires_that_key); loader test covers primary_tsig_keys.
- `go test ./e2e/ -run "TestDNSSECSigningRollover|TestKeyStorageBackends|TestSecondaryAndDynamicUpdate" -count=1 -v`: PASS (22.70s / 6.35s / 8.15s); delv validates at every rollover stage; NOTIFY signed with another valid key and unsigned are REFUSED after the positive case.
- Full run `make engine-test && make mgmt-test && make e2e-build && go test -count=1 -timeout 60m ./e2e/...` (OpenSearch/Jaeger env set): engine 195 passed, every mgmt package ok, e2e: only TestGUICoverage FAIL (19 M4 zone/TSIG/DNSSEC operations uncovered until Task 15).
- cargo fmt --check, clippy -D warnings, gofmt, go vet, pnpm lint (permission parity 96 operations): clean.
- Not committed (lead commits).

