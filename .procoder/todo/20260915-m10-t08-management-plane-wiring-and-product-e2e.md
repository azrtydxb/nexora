# M10 T8: Management plane wiring and product e2e

Status: open
Created: 2026-09-15

## Description

Implements Task 8 of `.procoder/plans/nexora-m10-querylog-backends.md` (spec `.procoder/specs/nexora-m10-querylog-backends.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/cmd/nexora-mgmt/querylog_test.go`:
- [x] Run `scripts/dev-exec.sh 'make webui-placeholder && go test ./mgmt/cmd/nexora-mgmt -run TestServeBuildsQueryLogBackend -count=1'`
- [x] In `main.go`:
- [x] Run the step-2 command and expect PASS.
- [x] In `mgmt/api/openapi.yaml`, set the query-log page `backend` property to
- [x] Create `e2e/harness/backends_test.go`:
- [x] Create `e2e/querylog_backends_helper_test.go`. `queryLogMgmtOptions(t, env, backend)` returns
- [x] In `e2e/gui_test.go` `TestQueryLogBackends`:
- [x] In `e2e/filter_attribution_test.go`, make the loop list the four backends and build options with
- [x] Run
- [x] Run `scripts/dev-exec.sh 'make webui-placeholder && go test ./mgmt/... -count=1'` and expect PASS.
- [ ] Report the paths. Commit message: `mgmt: clickhouse and loki query-log backends wired end to end`.

## Evidence

All commands ran in pod toolbox-m10 (`NEXORA_DEV_DEPLOY=toolbox-m10`), with the private binary directory
`/work/m10t08-bin` (`make e2e-build BIN=/work/m10t08-bin`) instead of `/work/nexora/bin`.

- Red: `scripts/dev-exec.sh 'make webui-placeholder && go test ./mgmt/cmd/nexora-mgmt -run TestServeBuildsQueryLogBackend -count=1'`
  -> FAIL `undefined: buildQueryLog`, `undefined: queryLogToManagement`.
- Green: same command -> `ok github.com/piwi3910/nexora/mgmt/cmd/nexora-mgmt 0.048s`; `gofmt -l`, `go vet` clean.
- `gen.go`: `go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config <private copy, output /tmp/m10t08-gen/gen.go> openapi.yaml`
  in the pod, copied back; diff = the `// Backend builtin, opensearch, clickhouse or loki` comment plus the embedded spec.
  `schema.d.ts`: the pod's `openapi-typescript` into `/tmp/m10t08-gen`; whitespace-normalised diff = the one
  `/** @description builtin, opensearch, clickhouse or loki */` line (the pod has no prettier), applied with the committed formatting.
- `go test ./e2e/harness -run "TestHarnessStartsClickHouseAndLoki|TestOtelcolConfigValidates" -count=1 -v`
  -> `--- PASS: TestHarnessStartsClickHouseAndLoki (12.28s)`, `--- PASS: TestOtelcolConfigValidates`.
- First product run: clickhouse and loki PASS (all subtests); opensearch FAIL (`dashboard-top` never saw a 5-count
  name, `partial-name` TUBE.TEST and attribution `www.custom.attr.test` matched earlier runs' records in the shared index).
  Fix: private per-run OpenSearch index pre-created with an `@timestamp` date mapping (two further failures showed
  "all shards failed" on the auto-created index and "No mapping found for [@timestamp]" on an unmapped one).
- `NEXORA_E2E_BIN_DIR=/work/m10t08-bin go test ./e2e -run "TestQueryLogBackends|TestQueryLogCategoryAttribution" -count=1 -v -timeout 60m`
  -> `ok github.com/piwi3910/nexora/e2e 125.880s`: builtin, opensearch, clickhouse, loki each PASS with the Playwright run,
  partial-name, multi-value, dashboard-top; attribution PASS for all four.
- `make webui-placeholder && go test ./mgmt/... -count=1` -> every package ok (exit 0).
- `golangci-lint run ./mgmt/cmd/nexora-mgmt/... ./e2e/ ./e2e/harness/` (laptop) -> no finding in the changed lines
  (pre-existing errcheck/staticcheck findings elsewhere).
- Re-run after T9/T10/T11 and the Loki partition commit (`889ba81`), with a rebuilt `nexora-mgmt`:
  `go test ./e2e -run "^(TestQueryLogBackends|TestQueryLogCategoryAttribution|TestGUICoverage)$" -count=1 -v -timeout 50m`
  -> `ok github.com/piwi3910/nexora/e2e 359.562s` (`--- PASS: TestGUICoverage (234.95s)`, all four backends with every
  subtest PASS). No `nexora-e2e-*` index remains in the shared OpenSearch afterwards.
