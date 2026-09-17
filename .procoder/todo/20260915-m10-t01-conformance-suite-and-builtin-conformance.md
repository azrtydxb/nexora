# M10 T1: Conformance suite and builtin conformance

Status: open
Created: 2026-09-15

## Description

Implements Task 1 of `.procoder/plans/nexora-m10-querylog-backends.md` (spec `.procoder/specs/nexora-m10-querylog-backends.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/querylog/conformance_test.go`:
- [x] Run
- [x] Create `dataset.go`. Each record is an OTLP `LogRecord` with the engine's attribute keys (see
- [x] Create `conformance.go`. `Run(tb, run, base, h)`:
- [x] Add to `conformance.go`: `OTLPExport` dials `grpcAddr` with `grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))` and calls `collogspb.NewLogsServiceClient(conn).Export` once per batch in order, failing the test on error. `PollVisible(b, base)` returns a closure that polls
- [x] Run the command from step 2 and expect PASS. Then run
- [x] Report the paths. Commit message: `querylog: conformance suite with builtin reference`.

## Evidence

All commands run with `NEXORA_DEV_DEPLOY=toolbox-m10` (M10 worktree dev pod). Not committed yet (lead commits).

- Red: `scripts/dev-exec.sh 'go test ./mgmt/internal/querylog -run "TestQueryLogConformanceBuiltin|TestConformanceDetectsDivergence" -count=1'` with only `conformance_test.go` present -> `FAIL ... no required module provides package github.com/piwi3910/nexora/mgmt/internal/querylog/querylogtest`.
- Green: same command -> `ok  github.com/piwi3910/nexora/mgmt/internal/querylog 0.107s`; all 14 subtests PASS for builtin and for the positive run of the divergence test.
- Mutation check: `lossy` made lossless (`&& false`) -> `FAIL: TestConformanceDetectsDivergence ... conformance suite passed a backend that loses a record per page`; restored.
- `gofmt -l mgmt/internal/querylog mgmt/internal/config && go vet ./mgmt/internal/querylog/... ./mgmt/internal/config/...` -> no gofmt output, vet clean.
- `golangci-lint run ./mgmt/internal/querylog/...` -> only 4 pre-existing errcheck findings in `opensearch_test.go` (not touched).
- Plan text updated to match: `testing.TB` harness functions, `lossy` embeds `*querylog.Builtin`, `Name: <run>`, `x_y_` case, Top expectations include `tie-<run>` (count 3), positive wider-window checks.
