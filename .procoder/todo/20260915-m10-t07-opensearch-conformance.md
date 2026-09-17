# M10 T7: OpenSearch conformance

Status: open
Created: 2026-09-15

## Description

Implements Task 7 of `.procoder/plans/nexora-m10-querylog-backends.md` (spec `.procoder/specs/nexora-m10-querylog-backends.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/querylog/e2e/opensearch_conformance_test.go` (moved from the plan's `e2e/querylog_conformance_opensearch_test.go`: the top-level `e2e` package cannot import `mgmt/internal/...`)
- [x] Run
- [ ] Report the paths and any divergence found. Commit message: `e2e: opensearch query-log conformance`.

## Evidence

- Plan's location fails to compile: `go vet ./e2e` → `use of internal package github.com/piwi3910/nexora/mgmt/internal/config not allowed`. Test moved to `mgmt/internal/querylog/e2e` (skips without `NEXORA_E2E_BIN_DIR`); `Makefile` `e2e` target runs that package too.
- `NEXORA_DEV_DEPLOY=toolbox-m10 scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./mgmt/internal/querylog/e2e -run TestQueryLogConformanceOpenSearch -count=1 -v -timeout 20m'` → all 14 subtests PASS, `ok github.com/piwi3910/nexora/mgmt/internal/querylog/e2e 6.056s`. No divergence; `opensearch.go` unchanged.
- Mutation check: `lte` → `lt` in the `@timestamp` range of `opensearch.go` → `--- FAIL: TestQueryLogConformanceOpenSearch (60.61s)` (edge-to record never visible); reverted.
- Without `NEXORA_E2E_BIN_DIR`: `--- SKIP: TestQueryLogConformanceOpenSearch`.
- Cleanup: after the run `_cat/indices/nexora-conformance-*` lists 0 indices (the collector appends `-yyyy.mm.dd`, so the `<index>*` delete is needed).
- `gofmt -l mgmt/internal/querylog/e2e` → no output; `go vet ./mgmt/internal/querylog/e2e ./mgmt/internal/querylog/querylogtest` → ok. `golangci-lint` is not installed in toolbox-m10.
