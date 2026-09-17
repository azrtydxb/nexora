# M10 T11: Operations guide and help topic

Status: open
Created: 2026-09-15

## Description

Implements Task 11 of `.procoder/plans/nexora-m10-querylog-backends.md` (spec `.procoder/specs/nexora-m10-querylog-backends.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `deploy/deploytest/docs_querylog_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestOperationsDocNamesQueryLogSettings -count=1'`
- [x] Edit `docs/operations.md`.
- [ ] Edit `web/src/help/topics/observability.md`: one short paragraph per backend in operator
- [ ] Run
- [ ] Report the paths. Commit message: `docs: clickhouse and loki query-log backends`.

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m10 scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestOperationsDocNamesQueryLogSettings -count=1'`
  failed with `docs/operations.md does not name NEXORA_CLICKHOUSE_URL` (plus the other 10 variables,
  the five required strings and the help topic).
- `docs/operations.md`: env table rows for all 11 variables (defaults from `mgmt/internal/config/config.go`),
  ClickHouse, Loki and Choosing a backend bullets in "Query logs, traces and OTLP", three Known
  limitations entries; formatted with `scripts/pc-format.sh docs/operations.md`. Statements checked
  against `mgmt/internal/querylog/{clickhouse,loki,loki_logql}.go`, `deploy/clickhouse/querylog.sql`
  and `e2e/harness/{otelcol,clickhouse,loki}.go`. Loki name partitions documented as first character
  after an optional `www.` (the code in `loki_logql.go`), clients and categories as last character.
- After the edit: `go test ./deploy/deploytest -run "TestOperationsDocNamesQueryLogSettings|TestOperationsDoc$|TestHelpTopicsReferenceOperationsDoc" -count=1 -v`:
  `TestOperationsDoc` PASS, `TestHelpTopicsReferenceOperationsDoc` PASS,
  `TestOperationsDocNamesQueryLogSettings` FAIL only on `observability help topic lacks ClickHouse` / `Loki`.
- Open: the help topic edit was outside this run's assigned files; the full package run also fails
  `TestKwClickHouseManifest` and `TestKwCollectorFansOutQueryLogs` (Task 10 not yet written).
