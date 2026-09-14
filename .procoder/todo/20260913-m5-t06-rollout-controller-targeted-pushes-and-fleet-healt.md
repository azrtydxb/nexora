# M5 Task 6: Rollout controller, targeted pushes and fleet health

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 6 ("Rollout controller, targeted pushes and fleet health") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 6 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [x] `TestControllerDrivesUnderAdvisoryLock` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestHealthAndDisconnectedGauge` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestHubTargetsCanariesOnly` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

Implemented 2026-09-14, not committed (lead commits).

- Names: `TestControllerDrivesCanaryUnderAdvisoryLock`, `TestEngineStatusTargetsAndMetrics`, `TestCanaryVersionReachesOnlyCanariesUntilHealthy`, `TestKeysFilteredToTargetSnapshot`; extra `TestTwoControllersNeverDoubleAdvance`, `TestControllerRestartResumesFromDatabase`.
- Mutation: advisory lock + group lock + FOR UPDATE removed -> `TestTwoControllersNeverDoubleAdvance` FAIL (repeated `verifying->verifying` writes); restored -> ok. `Target` giving non-canaries the canary version -> `TestCanaryVersionReachesOnlyCanariesUntilHealthy` FAIL `non-canary received`; restored -> ok.
- `scripts/dev-exec.sh 'go vet ./mgmt/... ./e2e/... && go test ./mgmt/... -count=1'` -> every package `ok`.
- `make e2e-build BIN=/tmp/m5ctl/bin` then targeted e2e (`TestInvalidSnapshotRejected|TestMgmtStatelessHA|TestPerClientPolicy|TestAuthoritativeZonePropagation`) -> PASS. Full e2e: see final report.
- Full e2e: `NEXORA_E2E_BIN_DIR=/tmp/m5ctl/bin NEXORA_E2E_OPENSEARCH_URL=... NEXORA_E2E_JAEGER_QUERY_URL=... go test ./e2e/... -count=1 -v` -> `ok github.com/piwi3910/nexora/e2e 296.245s` and every e2e subpackage ok; TestKwSmoke/TestKwSmokeM4 SKIP (NEXORA_KW_* not set). A first run failed TestSecondaryAndDynamicUpdate (key filter too strict, fixed) and TestOTelSinkDownNoBackpressure (QPS flake, passed on rerun).

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in c44e988
