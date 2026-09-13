# M3 Task 6: DNSSEC primitives — signature verification, validity windows, DS matching, NSEC/NSEC3 denial

Status: open
Created: 2026-09-13

## Description

Implement Task 6 ("DNSSEC primitives — signature verification, validity windows, DS matching, NSEC/NSEC3 denial") of milestone M3 exactly as specified in
`.procoder/plans/nexora-v1-m3.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 6 in `.procoder/plans/nexora-v1-m3.md` is done as written (deviations recorded in the plan first)
- [x] `TestKey` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- RED: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib recursor::dnssec::primitives_tests'` -> `file not found for module denial/verify`, `cannot find function within_validity`.
- First GREEN attempt: 6 passed, 2 failed: the plan's 1-hour skew assertion contradicted the 10% formula, and hickory-proto rejects `\\001` in `Name::from_ascii`; tests corrected (plan updated, see As built).
- GREEN: same command -> `test result: ok. 8 passed; 0 failed`.
- `cargo fmt -p nexora-engine --check` clean (dev pod); `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` reports nothing under `recursor/dnssec` (the crate still fails clippy on the concurrent RPZ work in `rpz/apply.rs` and `rpz/tsig.rs`); `cargo test --locked -p nexora-engine --lib` -> `117 passed; 0 failed; 2 ignored`.
- Not committed (lead commits serially).
