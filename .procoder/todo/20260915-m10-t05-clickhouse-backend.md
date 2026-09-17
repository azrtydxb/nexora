# M10 T5: ClickHouse backend

Status: open
Created: 2026-09-15

## Description

Implements Task 5 of `.procoder/plans/nexora-m10-querylog-backends.md` (spec `.procoder/specs/nexora-m10-querylog-backends.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/querylog/clickhouse_test.go` with these tests:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/querylog -run "TestClickHouse" -count=1'` and expect
- [x] Create `deploy/clickhouse/querylog.sql` following the spec's Data section exactly:
- [x] Implement `mgmt/internal/querylog/clickhouse.go`:
- [x] Create `e2e/harness/clickhouse.go`. `StartClickHouse`:
- [x] Create `mgmt/internal/querylog/e2e/clickhouse_conformance_test.go`:
- [x] Run the step-2 command and expect PASS. Then run
- [x] Add the comment
- [ ] Report the paths. Commit message: `querylog: clickhouse backend`. (paths reported; the lead commits)

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m10 scripts/dev-exec.sh 'go test ./mgmt/internal/querylog -run "TestClickHouse" -count=1'`
  -> FAIL `undefined: querylog.ClickHouse` / `undefined: querylog.NewClickHouse` / `undefined: querylog.ClickHouseColumns`.
- Green: same command -> `ok github.com/piwi3910/nexora/mgmt/internal/querylog` (6 tests: QueryIsParameterised,
  ArrayParamEscapesQuotes, CursorKeyset, TopSQL, Errors, SchemaHasAdapterColumns).
- Mutation: `params["name"] = likeParam(name)` (no escaped-format escaping) -> FAIL
  `param_name "%a\\%b\\_c\\\\'); drop%", want "%a\\\\%b..."`; restored.
- Escaping verified on ClickHouse 26.8.4.11 (`clickhouse local`): the doubly escaped `%a\\%b\\_c\\\\'); drop%`
  matches the stored name `A%b_c\'); drop.x`, the singly escaped pattern matches 0 rows.
- `querylog.sql` applied twice with `clickhouse local` and twice by `StartClickHouse`: no error.
- Grants: by hand with the harness XML, reader SELECT ok / INSERT code 497; writer INSERT ok / SELECT code 497.
- e2e: `NEXORA_DEV_DEPLOY=toolbox-m10 scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./mgmt/internal/querylog/e2e -run TestQueryLogConformanceClickHouse -count=1 -v -timeout 20m'`
  -> build exit 0; `--- PASS: TestQueryLogConformanceClickHouse (6.77s)` with all 14 subtests PASS.
- `go test ./mgmt/internal/querylog/... ./e2e/harness -count=1` -> ok (all packages).
- `gofmt -l mgmt/internal/querylog e2e/harness` and `go vet ./mgmt/internal/querylog/... ./e2e/harness` -> no output.
- `golangci-lint run --max-issues-per-linter 0 --max-same-issues 0 ./mgmt/internal/querylog/... ./e2e/harness/...` (laptop;
  not installed in the pod) -> no finding in the ClickHouse files (pre-existing errcheck findings elsewhere).
