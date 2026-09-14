# M6 T15: Parallel upstream race in the engine

Status: open
Created: 2026-09-14

## Description

Implements Task 15 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `engine/tests/upstream_parallel.rs`. Copy the helpers `query`, `counter` and `local`
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test upstream_parallel'` and
- [x] Implement `race` in `engine/src/upstream/mod.rs`:
- [x] In `engine/src/snapshot.rs` `validate`, add (dropped: contradicts the committed "engines cap at 8"
      contract and Task 4's test; plan text updated)
- [x] Run
- [ ] Report the paths. Commit message: `engine: parallel upstream strategy`. (paths reported; the lead
      commits)

## Evidence

- While Task 12's edits left the shared checkout uncompilable, red/green ran in a private pod copy
  (`/tmp/t15-work`: `git archive HEAD` plus this task's files, `CARGO_TARGET_DIR=/tmp/t15-target`).
- Red (dispatch to `race` disabled): `cargo test --locked -p nexora-engine --test upstream_parallel`:
  `parallel_returns_first_valid_answer_and_updates_every_upstream` panicked at line 93
  `left: "slow" right: "fast"`; `parallel_max_limits_to_lowest_rtt` panicked `left: 1 right: 2` on
  `got.raced`; `test result: FAILED. 0 passed; 2 failed`.
- Green (private copy): upstream_parallel `ok. 2 passed` (5 repeated runs, all `ok`),
  upstream_udp_tcp `ok. 6 passed`, server_pipeline `ok. 4 passed`, clippy `-D warnings` clean,
  `cargo fmt --check` clean; `cargo test --locked --no-fail-fast -p nexora-engine`: every binary `ok`
  (lib `242 passed; 1 ignored`, telemetry_export `10 passed`, hot_path_alloc `2 passed`, 0 failed).
- A `parallel_max > 8` rejection in `snapshot::validate` turned Task 4's
  `m6_parallel_strategy_maps_with_capped_max` red (it applies `parallel_max: 20`, expects
  `Parallel { max: 8 }`), so the rejection was removed; `snapshot.rs` is unchanged.
- Shared checkout, plan command:
  `scripts/dev-exec.sh 'cargo test ... --test upstream_parallel; ... --test upstream_udp_tcp; ... --test server_pipeline; cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`:
  `ok. 2 passed`, `ok. 6 passed`, `ok. 4 passed`, clippy `Finished` with no errors.
