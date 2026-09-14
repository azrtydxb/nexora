# M5 Task 10: Fleet acceptance tests

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 10 ("Fleet acceptance tests") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 10 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [x] `TestAuthoritativeZonePropagation` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestCanaryRolloutHaltsOnFailure` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestEngineCertRevocation` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestFleetAPI` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestFleetRolloutAndPartition` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestGroupScopedConfig` (named `TestEngineGroupScopedConfig` in the plan and code) passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestInvalidSnapshotRejected` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestJoinTokenGroupAndExpiry` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestMgmtCLIFleet` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestMgmtStatelessHA` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Deviations recorded in the plan: fleet-3 joins a second engine group in `TestFleetRolloutAndPartition` (all three ack the same global version); product fixes `engine/src/runtime.rs` (cache cleared on upstream/strategy change, unit test `upstream_changes_clear_cached_answers`) and `mgmt/internal/pki/pki.go` (NotBefore backdate `min(1h, validity/10)`, unit test `TestEngineCertificateRenewalPointIsInTheFuture`; the 1 h backdate made 60 s certificates renew in a loop).
- Red first: `TestEngineGroupScopedConfig` failed 5/5 with `want A 192.0.2.102, got ... [192.0.2.101]` before the cache fix; `TestEngineCertRevocation` failed with engines never applying a version under a renewal storm before the pki fix; the new engine and pki unit tests failed before their fixes.
- `scripts/dev-exec.sh 'NEXORA_E2E_BIN_DIR=$PWD/bin go test ./e2e/ -run "TestFleetRolloutAndPartition|TestEngineGroupScopedConfig|TestJoinTokenGroupAndExpiry|TestCanaryRolloutHaltsOnFailure" -count=3 -v'` -> 12x `--- PASS`, `ok github.com/piwi3910/nexora/e2e 322.075s`.
- `... -run TestEngineCertRevocation -count=3` -> 3x `--- PASS`, `ok ... 309.807s`.
- Mutations: `a.Proc.Kill()` -> sleep: FAIL `fleet-1 still reports a control stream`; `max_servfail_ratio` 1: FAIL `rollout 7 is completed (), waiting for [halted]`; `CheckCertificate` accepting revoked: FAIL `export as rev-1: rpc error: code = OK`. All restored and rebuilt.
- `make engine-test` all `test result: ok`; `make mgmt-test` all ok except a transient `TestOperationsDoc` (another agent's untracked Task 15 docs test; passes on rerun).
- `make e2e-build` + `go test -count=1 ./e2e/...` in three chunks (harness/fixtures packages ok; every e2e test `--- PASS` including `TestMgmtStatelessHA` and `TestGUICoverage`; `TestKwSmoke*` SKIP without kw env).
- Not committed (lead commits).

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in d15f28e
