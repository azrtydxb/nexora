# M7 T8: RPZ IXFR keys only for deleted owners (#19)

Status: open
Created: 2026-09-14

## Description

Implements Task 8 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] In `transfer.rs`, add the test counter and count every `record_key` call:
- [x] Add to `transfer_tests.rs`:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib ixfr_builds_keys_only'`. Expect FAIL `built 100001 record keys for a one-record deletion`.
- [x] In `apply_ixfr`, build the owner set next to `deletions`, then filter by it before building a key:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib recursor::rpz && cargo test --locked -p nexora-engine --test rpz_pipeline'` and expect all to pass.

## Evidence

All commands ran with `NEXORA_DEV_DEPLOY=toolbox-m7` (second dev pod), 2026-09-15.

- Red: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib ixfr_builds_keys_only'` (counter and test added, `apply_ixfr` unchanged) →
  `panicked at engine/src/recursor/rpz/transfer_tests.rs:227:5: built 100001 record keys for a one-record deletion`; `test result: FAILED. 0 passed; 1 failed`.
- Green (owner prefilter added, `debt:` comment removed):
  `cargo test --locked -p nexora-engine --lib recursor::rpz` → `test result: ok. 17 passed; 0 failed` (includes `ixfr_builds_keys_only_for_records_at_deleted_owners` and the existing `ixfr_applies_deletions_and_additions`, `axfr_with_tsig_then_incremental_update`);
  `cargo test --locked -p nexora-engine --test rpz_pipeline` → `test result: ok. 1 passed; 0 failed`.
- `rustfmt --edition 2024 --check` on both files: clean. `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings`: finished, no warnings.
- The break the test catches: removing `deleted_owners.contains(&r.name) &&` from the retain predicate returns the red result above (one key per zone record). The "record not in the zone" error path is unchanged (same count check).
- Not committed yet (lead commits).
