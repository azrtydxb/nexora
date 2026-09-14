# M7 T8: RPZ IXFR keys only for deleted owners (#19)

Status: open
Created: 2026-09-14

## Description

Implements Task 8 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] In `transfer.rs`, add the test counter and count every `record_key` call:
- [ ] Add to `transfer_tests.rs`:
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib ixfr_builds_keys_only'`. Expect FAIL `built 100001 record keys for a one-record deletion`.
- [ ] In `apply_ixfr`, build the owner set next to `deletions`, then filter by it before building a key:
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib recursor::rpz && cargo test --locked -p nexora-engine --test rpz_pipeline'` and expect all to pass.

## Evidence

