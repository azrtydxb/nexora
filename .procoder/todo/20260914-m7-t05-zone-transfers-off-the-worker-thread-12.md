# M7 T5: Zone transfers off the worker thread (#12)

Status: open
Created: 2026-09-14

## Description

Implements Task 5 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Add inside `mod authorization` in `xfr_tests.rs`:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib transfer_is_built_off_the_worker'`. Expect FAIL with `round 0: nothing else ran on the worker`: today `run_slow` never yields for transfers.
- [x] In `dispatch.rs`, move the body of the `SlowKind::Transfer` arm (the `Question::parse` FORMERR reply, `authorize_and_plan`, `xfr::count`, `xfr::messages`) into `fn build_transfer(rt: &Runtime, shared: &Shared, job: &SlowJob) -> Vec<Vec<u8>>`. Use `shared.auth.keyring` and `shared.metrics.auth`, and 
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib authoritative:: && cargo test --locked -p nexora-engine --test authoritative_pipeline'` and expect every test to pass, including `signed_queries_and_transfers_through_the_pipeline`.

## Evidence

All runs in the second dev pod (`NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh ...`), 2026-09-14/15.

- Red: `cargo test --locked -p nexora-engine --lib transfer_is_built_off_the_worker` before the change:
  `panicked at engine/src/authoritative/xfr_tests.rs:312:21: round 0: nothing else ran on the worker while the transfer was built` / `test result: FAILED. 0 passed; 1 failed`.
- The first test version (from the plan) was flaky once implemented (2 of 15 runs of `--lib authoritative::` failed): the blocking pool could finish the build before the worker's first poll of the join handle. The test now uses a one-thread blocking pool held by a job only the worker's other local task releases (plan text updated). Re-checked red with `run_slow` temporarily calling `build_transfer` inline: `round 0: nothing else ran on the worker while the transfer was built` / `test result: FAILED. 0 passed; 1 failed` (no hang); restored.
- Green: `cargo test --locked -p nexora-engine --lib transfer_is_built_off_the_worker`: `test result: ok. 1 passed; 0 failed`.
- `cargo test --locked -p nexora-engine --lib authoritative::` 15 consecutive runs: all `test result: ok. 59 passed; 0 failed`.
- `cargo test --locked -p nexora-engine --test authoritative_pipeline`: `test result: ok. 2 passed; 0 failed` (includes `signed_queries_and_transfers_through_the_pipeline`).
- `cargo clippy --locked -p nexora-engine --lib -- -D warnings`: clean. `--all-targets` currently fails only in other wave-1 tasks' in-progress test files (`recursor/rpz/transfer_tests.rs` `KEYS_BUILT`, `tests/telemetry_export.rs`), not in this task's files. `rustfmt --check` on both files: clean.
- `debt:` comment removed from `engine/src/authoritative/dispatch.rs` (grep finds none).
- Not committed (lead commits).

