# M4 Task 4: Engine runtime integration — incremental zone loading and dispatch before recursion

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 4 ("Engine runtime integration — incremental zone loading and dispatch before recursion") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 4 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [x] procoder gate clean over the changed files; work committed (fmt + clippy clean; commit left to the lead)

## Evidence

- Red: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test authoritative_pipeline'` with the stage disabled → `assertion left == right failed, left: Refused, right: NoError`.
- Green: same command → `test result: ok. 1 passed`; `--test hot_path_alloc` → `2 passed` (`cache_hit_path_does_not_allocate`, `authoritative_answer_path_does_not_allocate`).
- `scripts/dev-exec.sh 'make engine-test'` → exit 0, every target `test result: ok` (lib 155 passed, 1 ignored).
- AA fix (lead request): `cache::tests::upstream_aa_is_cleared_on_every_served_reply` failed before (`uncached reply keeps AA`), passes after.
- `cargo fmt --all -- --check` exit 0; clippy `-D warnings` clean.
- M1-M3 e2e (release engine + current mgmt, BIN=/tmp/authm4/bin, NEXORA_E2E_* set by hand because the toolbox pod lacks them): 17 tests incl. TestForwardCacheTTL, TestDNSSECValidation, TestRPZPolicy, TestRecursionRootHints, TestEncryptedTransports, TestPerClientPolicy → `ok github.com/piwi3910/nexora/e2e 158.134s`. GUI and kw tests not run.

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in 87c30f7
