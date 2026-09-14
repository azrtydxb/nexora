# M4 Task 3: Engine authoritative answers (unsigned)

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 3 ("Engine authoritative answers (unsigned)") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 3 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [x] procoder gate clean over the changed files; work committed (fmt + clippy clean; commit left to the lead)

## Evidence

- Red: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib authoritative::answer_tests'` → `error[E0583]: file not found for module answer/lookup/msg/set/writer`.
- Green: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib authoritative'` → `test result: ok. 29 passed; 0 failed` (includes answer_tests, loader_tests, zone_tests).
- `cargo fmt --all -- --check` exit 0; `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` clean.
- Deviations recorded in the plan ("Built (as implemented)" under Task 3).
- M1-M3 e2e (release engine + current mgmt, BIN=/tmp/authm4/bin, NEXORA_E2E_* set by hand because the toolbox pod lacks them): 17 tests incl. TestForwardCacheTTL, TestDNSSECValidation, TestRPZPolicy, TestRecursionRootHints, TestEncryptedTransports, TestPerClientPolicy → `ok github.com/piwi3910/nexora/e2e 158.134s`. GUI and kw tests not run.

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in 87c30f7
