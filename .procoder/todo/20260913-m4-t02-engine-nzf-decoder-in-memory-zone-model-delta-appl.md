# M4 Task 2: Engine NZF decoder, in-memory zone model, delta application

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 2 ("Engine NZF decoder, in-memory zone model, delta application") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 2 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Red: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib authoritative::zone_tests'` → `error[E0432]: unresolved import super::name::from_ascii` (and nzf/zone imports).
- Green: same command with filter `authoritative::` → `test result: ok. 9 passed; 0 failed` (4 plan tests + canon-key parity with Go goldens, name helpers, parser bounds, SOA/delete/RRSIG errors, NSEC/NSEC3 indexes).
- `cargo fmt --all -- --check` clean; `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` clean.
- Fuzz: `cd engine/fuzz && cargo +nightly fuzz run nzf_parse <tmp corpus seeded from engine/fuzz/corpus/nzf_parse> -- -max_total_time=60` → `Done 187917 runs in 61 second(s)`, no crash.
- Not committed (lead commits).

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in 7f6798b
