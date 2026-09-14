# M6 T13: Engine log ring buffer and LogRequest handling

Status: open
Created: 2026-09-14

## Description

Implements Task 13 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `engine/tests/logbuf.rs`:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test logbuf'` and expect FAIL:
- [x] Implement `engine/src/telemetry/logbuf.rs`:
- [x] In `engine/src/control.rs` `session`, add the receive arm:
- [x] Run
- [ ] Report the paths. Commit message: `engine: bounded log ring buffer served over the control stream`.

## Evidence

- Red: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test logbuf'` before the
  implementation: `error[E0433]: cannot find logbuf in telemetry` and `cannot find eprintln in nexora_engine`.
- Green, in the shared checkout: `cargo test --locked -p nexora-engine --test logbuf` ->
  `test result: ok. 4 passed; 0 failed`.
- The full plan command could not compile in the shared checkout (Task 12's in-progress
  `FilterDecision::Allowed(ListHit)` change broke `server/rewrite.rs` tests), so it ran in the dev pod
  on a copy of HEAD plus only the Task 13 files (`/work/t13-logbuf-8231`, own target dir, removed after):
  - `cargo test --locked -p nexora-engine --test logbuf` -> `ok. 4 passed; 0 failed`
  - `cargo test --locked -p nexora-engine --lib logbuf` (`plain_eprintln_inside_the_crate_reaches_the_ring`) -> `ok. 1 passed`
  - `cargo test --locked -p nexora-engine --test hot_path_alloc` -> `ok. 2 passed; 0 failed`
  - `cargo test --locked -p nexora-engine --test control_unit` -> `ok. 3 passed; 0 failed`
  - `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> `Finished`, no warnings.
- Not committed (the lead commits); last criterion stays open.
