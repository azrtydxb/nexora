# M6 T10: Engine metrics API

Status: open
Created: 2026-09-14

## Description

Implements Task 10 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/fleet/metrics_test.go`, following `TestSeriesDerivesRatesAndSkipsCounterResets`
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/fleet -run TestEngineMetricsSeries -count=1'`
- [x] Implement `mgmt/internal/fleet/metrics.go`:
- [x] Implement `GetEngineMetrics`: windows `5m`, `1h` and `24h` (400 for others, default `1h`), 404
- [ ] Run
- [ ] Report the paths. Commit message: `fleet: engine metrics series`.

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./mgmt/internal/fleet -run TestEngineMetricsSeries -count=1'` ->
  `mgmt/internal/fleet/metrics_test.go:48:18: undefined: fleet.EngineMetrics`, `FAIL [build failed]`.
- Green: same command -> `ok github.com/piwi3910/nexora/mgmt/internal/fleet 3.668s`.
- Full plan command in the shared checkout: fleet `ok` (17.984s); `./mgmt/internal/api` did not build
  because Task 5 is mid-edit (`handlers_admin.go:267: unknown field QType in struct literal of type
querylog.Query`), not this task's files.
- So the api package was run on a HEAD snapshot (`git archive HEAD`) plus this task's three files, in
  the dev pod (`go test ./mgmt/internal/fleet ./mgmt/internal/api -count=1`): fleet `ok 20.720s`; api
  `FAIL` with exactly one failure, `TestM6OperationsAreRoutedAndAuthenticated: GET
/engines/00000000-0000-0000-0000-00000000abcd/metrics?window=5m is not routed (404)`. The handler now
  answers an unknown engine with 404 `not_found` as the plan requires; Task 2's contract test reads any
  404 as a route miss. That test file is not owned by Task 10; left for the lead (plan text notes it).
- `go vet ./mgmt/internal/fleet ./mgmt/internal/api` on the snapshot: clean. `gofmt -l` on changed files:
  clean.
