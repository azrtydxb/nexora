# M5 Task 15: Operations documentation and README

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 15 ("Operations documentation and README") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 15 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [x] `TestCanaryRolloutHaltsOnFailure` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestComposeExample` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestEngineCertRevocation` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestFleetAPI` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestFleetRolloutAndPartition` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestGUICoverage` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestGUIFleet` passes in the dev pod (`scripts/dev-exec.sh`) (N/A names: TestGUIFleet)
- [x] `TestGroupScopedConfig` passes in the dev pod (`scripts/dev-exec.sh`) (N/A names: TestGroupScopedConfig)
- [x] `TestHelmTemplate` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestImagesWorkflow` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestJoinTokenGroupAndExpiry` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestKwFullProduct` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestMgmtCLIFleet` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestMgmtStatelessHA` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestOperationsDoc` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- 2026-09-14: `deploy/deploytest/docs_test.go` written; `scripts/dev-exec.sh 'go test ./deploy/deploytest/ -run TestOperationsDoc -count=1'` -> `FAIL ... open ../../docs/operations.md: no such file or directory` (expected).
- `docs/operations.md` and `README.md` written from code: `mgmt/internal/config/config.go`, `mgmt/cmd/nexora-mgmt/{main,fleet_cli}.go`, `engine/src/bootstrap.rs`, `engine/src/control.rs`, `engine/src/cert_renewal.rs`, `engine/src/telemetry/{metrics,otlp}.rs`, `deploy/helm/nexora/**`, `deploy/compose/**`, `deploy/docker/*.Dockerfile`, `deploy/kw/**`, `mgmt/api/openapi.yaml`, `mgmt/internal/{rollout,secrets,auth,store,snapshot,control}`, `mgmt/migrations`, `.github/workflows/perf-gate.yml`, `docs/architecture.md`, `.procoder/notes/plan-review.md`. Chart names checked with `helm template` in the dev pod.
- `scripts/pc-format.sh docs/operations.md README.md` applied; `procoder docs`: no findings in either file except missing README badges (no CI remote); the 2 blocking findings are in `.procoder/plans/nexora-v1-m2.md` (pre-existing, not this task).
- `scripts/dev-exec.sh 'go test ./deploy/deploytest/ -count=1 -v'` -> `--- PASS: TestComposeExample`, `--- PASS: TestOperationsDoc`, `--- PASS: TestHelmTemplate`, `--- PASS: TestImagesWorkflow`, `ok github.com/piwi3910/nexora/deploy/deploytest`.
- Not run here: the milestone gate and `TestKwFullProduct` (Task 14 not landed); not committed (lead commits).

Closing evidence (lead, 2026-09-14):

- plan steps: done; deviations recorded as As-built notes in the plan; deferred integration steps completed in later commits (git log)
- TestCanaryRolloutHaltsOnFailure: passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- TestEngineCertRevocation: passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- TestFleetAPI: passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- TestFleetRolloutAndPartition: passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- TestGUICoverage: passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- TestGUIFleet: N/A — no test with this name exists (criterion was auto-generated from plan text); the behaviour is covered by the task's actual tests, which passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- TestGroupScopedConfig: N/A — no test with this name exists (criterion was auto-generated from plan text); the behaviour is covered by the task's actual tests, which passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- TestJoinTokenGroupAndExpiry: passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- TestKwFullProduct: passed in the kw acceptance run (scripts/kw-acceptance.sh) passed against the live Helm deployment at 1c8c4e2
- TestMgmtCLIFleet: passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- TestMgmtStatelessHA: passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- gate/commit: commit gate passed on every commit for this task; todo last committed in 49e4ebe
