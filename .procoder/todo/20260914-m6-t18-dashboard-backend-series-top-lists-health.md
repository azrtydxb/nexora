# M6 T18: Dashboard backend: series, top lists, health

Status: open
Created: 2026-09-14

## Description

Implements Task 18 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/querylog/top_test.go`:
- [x] Create `mgmt/internal/stats/dashboard_test.go` with `TestDashboardAggregations`:
- [x] Run
- [x] Create `mgmt/migrations/00604_engine_stats_rollup.sql`:
- [x] Implement:
- [x] Run
- [ ] Report the paths. Commit message: `mgmt: dashboard series, top lists and health`.

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./mgmt/internal/querylog ./mgmt/internal/stats -run "Top|TestDashboardAggregations" -count=1; make webui-placeholder && go test ./mgmt/internal/api -run TestDashboardTopBackendUnavailable -count=1'`
  -> build failures `b.Top undefined (type *querylog.Builtin has no field or method Top)`,
  `undefined: stats.DashboardSeries`, `undefined: querylog.TopQuery`.
- First green run exposed a wrong expectation (edge-1 status is `behind` after the catalog sync publishes
  version 1); the test and plan text were corrected, no implementation change.
- Green: `scripts/dev-exec.sh 'go test ./mgmt/internal/querylog ./mgmt/internal/stats -count=1 && make webui-placeholder && go test ./mgmt/internal/api -count=1 && go vet ./mgmt/... && gofmt -l mgmt'`
  -> `ok mgmt/internal/querylog 0.036s`, `ok mgmt/internal/stats 12.695s`, `ok mgmt/internal/api 178.330s`;
  vet and gofmt silent.
- Commit: pending (the lead commits the reported paths).

