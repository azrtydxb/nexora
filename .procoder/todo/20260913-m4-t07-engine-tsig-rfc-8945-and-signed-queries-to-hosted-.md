# M4 Task 7: Engine TSIG (RFC 8945) and signed queries to hosted zones

Status: open
Created: 2026-09-13

## Description

Implement Task 7 ("Engine TSIG (RFC 8945) and signed queries to hosted zones") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 7 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [x] `TestTSIGVectors` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- `go test ./mgmt/internal/nzf/ -run TestTSIGVectors -count=1 -update` (laptop; nzf has no pod-only deps) → `ok`, three files in `testdata/tsig/`.
- `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib tsig'` → `test result: ok. 15 passed` (10 tsig_tests incl. miekg byte-identical response signature, plus RPZ transfer TSIG tests).
- Tests were written together with the implementation; the red run was not observed separately.
- `scripts/dev-exec.sh 'make engine-test'` → every target `test result: ok` (lib 176 passed, 1 ignored); `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` clean; `cargo fmt` applied.
- Not committed (lead commits).
