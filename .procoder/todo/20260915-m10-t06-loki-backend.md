# M10 T6: Loki backend

Status: open
Created: 2026-09-15

## Description

Implements Task 6 of `.procoder/plans/nexora-m10-querylog-backends.md` (spec `.procoder/specs/nexora-m10-querylog-backends.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/querylog/loki_test.go`. A fake Loki (`httptest`) records each request's
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/querylog -run "TestLoki" -count=1'` and expect FAIL:
- [x] Implement `loki_logql.go`:
- [x] Implement `loki.go`:
- [x] Create `e2e/harness/loki.go`. `StartLoki(o)`:
- [x] Create `mgmt/internal/querylog/e2e/loki_conformance_test.go`:
- [x] Run the step-2 command and expect PASS. Then run
- [x] Add the comment
- [ ] Report the paths. Commit message: `querylog: loki backend`.

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m10 scripts/dev-exec.sh 'go test ./mgmt/internal/querylog -run "TestLoki" -count=1'`
  → `undefined: querylog.NewLoki` … `FAIL github.com/piwi3910/nexora/mgmt/internal/querylog [build failed]`.
- Green: same command → `ok github.com/piwi3910/nexora/mgmt/internal/querylog 0.687s` (all six
  `TestLoki*` PASS).
- Mutation checks: dropping the oldest-group skip fails `TestLokiPagingTiesAtOneNanosecond`
  (5 records); a partition concurrency of 37 fails `TestLokiTopPartitionFallback` (28 in flight).
- e2e: `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./mgmt/internal/querylog/e2e -run TestQueryLogConformanceLoki -count=1 -v -timeout 25m'`
  → build exit 0, all 14 suite subtests and `top-partitioned` PASS, `ok … querylog/e2e 30.316s`.
- `go test ./mgmt/internal/querylog/... -count=1` → ok; `go vet ./e2e/harness ./mgmt/internal/querylog/...`
  clean (with Task 5's `clickhouse.go` present); `gofmt -l` empty; `golangci-lint run` reports nothing
  in the Loki files.
- Loki 3.6.7 probes (local loki in toolbox-m10): series-limit text is `maximum number of series`;
  `[Nns]` ranges are rejected, `ms` accepted; ranges are left-open; `time`/`offset` keep ns. Plan
  Task 6 updated accordingly.
- Follow-up (lead decision after commit 11da7ee): names partition by first character after an
  optional `www.`, clients and categories keep the last character (`LokiPartitionRegexes(field)`).
  `TestLokiPartitionsCoverEveryKey` checks samples (`www.`, digits, `_` labels, `xn--`, root `.`) and
  10,000 random keys per scheme; mutation (naive `(?:www\.)?w.*` for `w`) fails it. Re-run:
  `go test ./mgmt/internal/querylog/... -count=1` ok; `go vet ./e2e/harness ./mgmt/internal/querylog/...`
  clean; gofmt empty; `NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./mgmt/internal/querylog/e2e -run TestQueryLogConformanceLoki -v`
  → all subtests PASS, `top-partitioned` now by name with `MaxQuerySeries: 5` (`ok … 29.980s`).
