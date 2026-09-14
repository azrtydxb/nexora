# M3 Task 4: Iterative resolver (delegations, bailiwick, glueless NS, CNAME/DNAME, QNAME minimisation, limits)

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 4 ("Iterative resolver (delegations, bailiwick, glueless NS, CNAME/DNAME, QNAME minimisation, limits)") of milestone M3 exactly as specified in
`.procoder/plans/nexora-v1-m3.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 4 in `.procoder/plans/nexora-v1-m3.md` is done as written (deviations recorded in the plan first)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- RED: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib recursor::iterate_tests'` -> unresolved import `super::iterate`.
- GREEN: same command -> `test result: ok. 10 passed` (9 plan tests + `unrelated_answer_records_are_not_cached`, which failed first with "stuffed record cached" before answer scrubbing). `recursor::` run 5x -> `22 passed` each time (no flakes).
- Mutation: removing the glue bailiwick check makes `out_of_bailiwick_glue_is_ignored` fail ("out-of-bailiwick glue was cached"); reverted.
- clippy -D warnings (all targets) clean; `cargo fmt --check` clean; `cargo test --locked -p nexora-engine --lib` -> `76 passed; 0 failed; 1 ignored`.
- Deviations (hickory has no DNAME RDATA, per-step failover, SERVFAIL not lame, answer scrubbing, DS-only-for-owner) recorded in the plan's Task 4 "As built" notes.
- Not committed (lead commits serially).

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in 083f943
