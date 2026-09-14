# M3 Task 8: Trust anchor store with RFC 5011 automated rollover

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 8 ("Trust anchor store with RFC 5011 automated rollover") of milestone M3 exactly as specified in
`.procoder/plans/nexora-v1-m3.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 8 in `.procoder/plans/nexora-v1-m3.md` is done as written (deviations recorded in the plan first)
- [x] `TestKey` passes in the dev pod (`scripts/dev-exec.sh`) — anchor store tests pass; engine integration not built (Task 5 missing) (N/A names: TestKey)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib recursor::dnssec::anchors_tests'` -> `test result: ok. 7 passed; 0 failed`.
- Mutation check: making AddPend keys valid before the hold-down failed `new_key_waits_add_hold_down_then_becomes_trusted`; reverted.
- OPEN: `refresh_loop`, `spawn_background`, `RecursorState::sync`, `control.rs`/`main.rs` calls and `telemetry/metrics.rs` trust-anchor metrics/Stats wait for Task 5 (`RecursorState`, `Shared.recursor`, `RoutedFetcher`). `refresh_zone` implements the refresh itself.
- `cargo fmt -p nexora-engine --check` clean (dev pod); `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` reports nothing under `recursor/dnssec` (the crate still fails clippy on the concurrent RPZ work in `rpz/apply.rs` and `rpz/tsig.rs`); `cargo test --locked -p nexora-engine --lib` -> `117 passed; 0 failed; 2 ignored`.
- Not committed (lead commits serially).

- Integration steps completed together with M3 Task 5 (see Task 5 As-built notes in the plan): `refresh_loop`/`spawn_background` (in `recursor/mod.rs`), `RecursorState::sync` called from `control.rs` and `main.rs`, trust-anchor metrics and `Stats.recursion`/`Stats.dnssec`. Verified by `recursor::dispatch_tests` (6 passed), `tests/rpz_pipeline.rs` (1 passed), `cache_hit_path_does_not_allocate` (forward, recursive+validation, RPZ triggers) and `make engine-test` exit 0. Not committed (lead commits).

Closing evidence (lead, 2026-09-14):

- plan steps: done; deviations recorded as As-built notes in the plan; deferred integration steps completed in later commits (git log)
- TestKey: N/A — no test with this name exists (criterion was auto-generated from plan text); the behaviour is covered by the task's actual tests, which passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- gate/commit: commit gate passed on every commit for this task; todo last committed in 54eaf2b
