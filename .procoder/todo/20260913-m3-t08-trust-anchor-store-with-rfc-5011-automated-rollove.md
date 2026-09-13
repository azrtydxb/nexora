# M3 Task 8: Trust anchor store with RFC 5011 automated rollover

Status: open
Created: 2026-09-13

## Description

Implement Task 8 ("Trust anchor store with RFC 5011 automated rollover") of milestone M3 exactly as specified in
`.procoder/plans/nexora-v1-m3.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 8 in `.procoder/plans/nexora-v1-m3.md` is done as written (deviations recorded in the plan first)
- [ ] `TestKey` passes in the dev pod (`scripts/dev-exec.sh`) — anchor store tests pass; engine integration not built (Task 5 missing)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib recursor::dnssec::anchors_tests'` -> `test result: ok. 7 passed; 0 failed`.
- Mutation check: making AddPend keys valid before the hold-down failed `new_key_waits_add_hold_down_then_becomes_trusted`; reverted.
- OPEN: `refresh_loop`, `spawn_background`, `RecursorState::sync`, `control.rs`/`main.rs` calls and `telemetry/metrics.rs` trust-anchor metrics/Stats wait for Task 5 (`RecursorState`, `Shared.recursor`, `RoutedFetcher`). `refresh_zone` implements the refresh itself.
- `cargo fmt -p nexora-engine --check` clean (dev pod); `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` reports nothing under `recursor/dnssec` (the crate still fails clippy on the concurrent RPZ work in `rpz/apply.rs` and `rpz/tsig.rs`); `cargo test --locked -p nexora-engine --lib` -> `117 passed; 0 failed; 2 ignored`.
- Not committed (lead commits serially).
