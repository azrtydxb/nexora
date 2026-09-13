# M3 Task 9: RPZ policy engine — parsing, triggers, precedence, actions and query-path hooks

Status: open
Created: 2026-09-13

## Description

Implement Task 9 ("RPZ policy engine — parsing, triggers, precedence, actions and query-path hooks") of milestone M3 exactly as specified in
`.procoder/plans/nexora-v1-m3.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] (library steps done; integration steps pending Task 5) Every step of Task 9 in `.procoder/plans/nexora-v1-m3.md` is done as written (deviations recorded in the plan first)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- Library built: `engine/src/recursor/rpz/{mod,parse,index,apply,rpz_tests}.rs`, `pub mod rpz;` in `engine/src/recursor/mod.rs`.
- `scripts/dev-exec.sh 'CARGO_TARGET_DIR=/tmp/rpz-target cargo test --locked -p nexora-engine --lib recursor::rpz::rpz_tests'` before implementation: FAIL `unresolved import super::apply`; after: `test result: ok. 8 passed; 0 failed`.
- `make engine-test` (CARGO_TARGET_DIR=/tmp/rpz-target): exit 0, 14 `test result: ok`, `cache_hit_path_does_not_allocate ... ok`.
- clippy `--all-targets`: no warnings under `recursor/rpz/`.
- NOT DONE: query-path / dispatch integration (`server/mod.rs`, `dispatch.rs`, `RecursorState.rpz`) — blocked on Task 5 (no `RecursorState`, `dispatch.rs`, `Shared.recursor` yet); plan marks those steps "Integration (after Task 5)". Not committed (lead commits).
