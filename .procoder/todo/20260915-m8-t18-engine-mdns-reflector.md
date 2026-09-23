# M8 T18: Engine mDNS reflector

Status: open
Created: 2026-09-15

## Description

Implements Task 18 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Add this test module to `reflector.rs`:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib mdns::reflector'` and expect
- [x] Implement:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib mdns:: && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`
- [ ] Report the paths. The lead commits `M8 T18: engine mDNS reflector`.

## Evidence

All commands ran in pod toolbox-m7, in the private copy `/work/m8-t18` with `CARGO_TARGET_DIR=/work/target-m8-t18`.

- Red: `reflector.rs` reduced to its doc comment and the test module, then
  `cargo test --locked -p nexora-engine --lib mdns::reflector` failed to compile with
  `error[E0433]: cannot find type Dedupe in this scope` and `cannot find function from_local` (exit 101).
- Green: `cargo test --locked -p nexora-engine --lib mdns::` passed with
  `test result: ok. 5 passed; 0 failed; 0 ignored; 0 measured; 267 filtered out`.
- `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` passed (exit 0). This needed
  Task 19's unfinished `engine/tests/odoh_pipeline.rs` and `engine/tests/common/` removed from the
  private copy, because they do not compile yet.
- `rustfmt --edition 2024 --check engine/src/mdns/reflector.rs` passed (exit 0).
- Mutation checks:
  - disabling the duplicate-digest rejection made the test fail with
    `an echo inside the window is dropped`;
  - making the IPv4 match in `from_local` always false made the test fail with
    `assertion failed: from_local("10.254.2.1"...)`.
