# M3 Task 3: Infrastructure cache, RRset cache, root hints, work budget

Status: open
Created: 2026-09-13

## Description

Implement Task 3 ("Infrastructure cache, RRset cache, root hints, work budget") of milestone M3 exactly as specified in
`.procoder/plans/nexora-v1-m3.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 3 in `.procoder/plans/nexora-v1-m3.md` is done as written (deviations recorded in the plan first)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- RED: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib recursor::infra_tests'` -> unresolved imports `super::budget`, `super::infra`, `super::roothints`, `super::rrcache`.
- GREEN: same command -> `test result: ok. 8 passed; 0 failed`.
- `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` clean; `cargo fmt -p nexora-engine --check` clean; `cargo test --locked -p nexora-engine --lib` -> `76 passed; 0 failed; 1 ignored`.
- As-built notes recorded in the plan (is_empty/len, derives, debt on non-atomic infra updates).
- Not committed (lead commits serially).
