# M8 T20: Engine RPZ ZONEMD verification

Status: open
Created: 2026-09-15

## Description

Implements Task 20 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Add to the RPZ manager tests:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib rpz_transfer_failing_zonemd_keeps_last_good'`
- [x] Implement:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib rpz && cargo test --locked -p nexora-engine --test rpz_pipeline && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`
- [ ] Report the paths. The lead commits `M8 T20: RPZ transfer ZONEMD verification`.

## Evidence

- Test `rpz_transfer_failing_zonemd_keeps_last_good` in `engine/src/recursor/rpz/transfer_tests.rs`.
- Red (pod toolbox-m7, `CARGO_TARGET_DIR=/work/target-m8-t20 cargo test --locked -p nexora-engine --lib rpz_transfer_failing_zonemd_keeps_last_good`):
  FAILED at transfer_tests.rs:474, left `(1, 0)` right `(1, 3)` (verdict not reported).
- Green, same command: `test result: ok. 1 passed; 0 failed`.
- Mutation (failing verdict applied instead of returned, private copy /work/m8-t20): FAILED at
  `assertion failed: mgr.manager.refresh_now(&mgr, "z1").await.is_err()`.
- Full check in private copy /work/m8-t20 (other wave-2 agents' in-progress engine files reset to HEAD,
  since the shared tree's `server/doh.rs` did not compile):
  `cargo test --locked -p nexora-engine --lib rpz` -> `ok. 21 passed; 0 failed`;
  `cargo test --locked -p nexora-engine --test rpz_pipeline` -> `ok. 1 passed; 0 failed`;
  `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> clean;
  `cargo fmt --check -p nexora-engine` -> clean. Exit 0.
- Not committed (lead commits `M8 T20: RPZ transfer ZONEMD verification`).
