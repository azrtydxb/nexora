# M6 T14: Management plane engine log broker

Status: open
Created: 2026-09-14

## Description

Implements Task 14 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/control/logs_test.go` in package `control_test`, reusing `setupServers`,
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/control -run TestEngineLogsRoutedAcrossInstances -count=1'`
- [x] Create `mgmt/migrations/00603_engine_log_replies.sql`:
- [x] Implement `mgmt/internal/control/logs.go` per Interfaces.
- [x] Create `mgmt/internal/api/engine_logs_test.go` (`TestGetEngineLogsMapsRequestsAndErrors`)
- [x] Implement `GetEngineLogs` in `mgmt/internal/api/engine_logs.go`:
- [x] Run
- [ ] Report the paths. Commit message: `mgmt: route engine log requests over the control stream`.
      (paths reported; the lead commits)

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./mgmt/internal/control -run TestEngineLogsRoutedAcrossInstances -count=1'`
  -> `logs_test.go:20:16: undefined: control.NewLogBroker` / `FAIL [build failed]`.
- Green: same command -> `--- PASS: TestEngineLogsRoutedAcrossInstances (9.21s)`.
- Mutations (each reverted): the hub not calling `LogBroker.Done` ->
  `logs_test.go:49: condition not met within 10s: engine did not answer in time` / `FAIL`;
  `preM6Engine` always false -> `TestGetEngineLogsMapsRequestsAndErrors` `FAIL`.
- Plan command: `scripts/dev-exec.sh 'go test ./mgmt/internal/control -count=1 && make webui-placeholder && go test ./mgmt/internal/api -count=1'`
  -> `ok github.com/piwi3910/nexora/mgmt/internal/control 64.752s`; api failed only in
  `TestDashboardTopBackendUnavailable` (`dashboard_m6_test.go:53`, Task 18's in-progress file, not
  this task). Rerun `go test ./mgmt/internal/api -count=1 -skip TestDashboard` ->
  `ok github.com/piwi3910/nexora/mgmt/internal/api 122.161s` (includes
  `TestGetEngineLogsMapsRequestsAndErrors` and `TestM6OperationsAreRoutedAndAuthenticated`).
- `gofmt -l` on the changed files empty; `go vet ./mgmt/...` clean; `go build ./mgmt/...` ok.
  `golangci-lint` is not installed in the dev pod.
- Deviations (plan text updated): replies are accepted only for request ids sent on that stream
  (security: an engine cannot write arbitrary rows); pre-M6 detection uses the newest `engine_stats`
  sample lacking `started_unix_ms`, because image tags `sha-<7>` do not order; `api.ErrEngine*`
  alias the control sentinels; an added API handler test.

