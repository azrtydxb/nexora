# M3 Task 5: Mode selection, forward zones and query-path integration

Status: open
Created: 2026-09-13

## Description

Implement Task 5 ("Mode selection, forward zones and query-path integration") of milestone M3 exactly as specified in
`.procoder/plans/nexora-v1-m3.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 5 in `.procoder/plans/nexora-v1-m3.md` is done as written (deviations recorded in the plan first)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- Built `engine/src/recursor/dispatch.rs` + `dispatch_tests.rs`, `RecursorState` (`recursor/mod.rs`), `Runtime.resolution`, `Shared.recursor`/`with_recursor`/`WorkerForward`, `ReplyOpt.ede`, AD clearing in `write_cached`, metrics/Stats, `QueryRecord.route/dnssec/rpz_action` + OTLP attributes, plus the deferred integration steps of Tasks 7-10; one `Ede` type. Deviations recorded in the plan's Task 5 As-built notes.
- `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib recursor::dispatch_tests'` -> `test result: ok. 6 passed; 0 failed` (red-before-green not observed: dispatch.rs was written before the test file).
- `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings`: clean; `cargo fmt`: applied.
- `make engine-test`: exit 0 (lib `125 passed; 1 ignored`, `cache_hit_path_does_not_allocate ... ok`, `rpz_pipeline 1 passed`).
- Not committed (lead commits).
