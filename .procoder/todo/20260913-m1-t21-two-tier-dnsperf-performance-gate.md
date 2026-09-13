# M1 Task 21: Two-tier dnsperf performance gate

Status: done
Created: 2026-09-13

## Description

Implement Task 21 ("Two-tier dnsperf performance gate") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 21 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] `TestAbsoluteThresholds` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestCompareUsesMedianAndFivePercentBoundary` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./bench/...'` before `gate.go` -> `undefined: Compare`, `undefined: Absolute` (build failed).
- Green: `scripts/dev-exec.sh 'go test -count=1 ./bench/...'` -> `ok github.com/piwi3910/nexora/bench/cmd/perfgate`, `ok github.com/piwi3910/nexora/bench/dnsperf` (TestCompareUsesMedianAndFivePercentBoundary, TestAbsoluteThresholds, TestParseRealOutput, TestParseRejectsGarbage).
- bench/dnsperf was created here (Task 18 not yet run); testdata captured from real dnsperf 2.14.0 in the pod per Task 18's step.
- Plan run step (binaries in /tmp/t21bin instead of shared bin/): `perfgate run --names 2000 --seconds 5 --workers 2` -> `"QPS": 135518.33`, completed 678055.
- PR tier in the dev pod (arm64, 8 cores, load avg ~7 from concurrent agents; NOT the reference box), identical binary as base and head, 10000 names, 20 s, 2 workers, alternating order: QPS 144220 / 168780 / 182348 / 184372 / 194808 / 170488, p99 1.5-1.6 ms, 0 lost; `perfgate compare` -> `base median 184372 QPS, head median 170488 QPS, drop 7.53% (limit 5.00%): FAIL`, exit 1 — noise on the shared pod exceeds 5% (debt comment in perf-gate.yml). `perfgate absolute` exits 1 with both reasons as expected for this box.
- `actionlint -config-file .github/actionlint.yaml .github/workflows/*.yml` (v1.7.12, go install in the pod) -> no output; laptop actionlint 1.7.12 with shellcheck -> no output.
- gofmt/go vet clean over bench; procoder check -> 0 blocking.
