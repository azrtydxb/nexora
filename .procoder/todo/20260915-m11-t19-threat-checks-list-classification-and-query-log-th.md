# M11 T19: Threat checks, list classification and query-log threat labels

Status: done (not committed — the lead commits)
Created: 2026-09-15

## Description

Implements Task 19 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `check_test.go` with `TestThreatCheck` (plus `TestThreatCheckRejectsUnknownName` for the
      re-asked answer, which needs its own model script).
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/threat -run TestThreatCheck -count=1'`: FAIL
      (`no non-test Go files`) before `check.go`, PASS after.
- [x] Create `classify_test.go` with `TestSampleIsUniformAndStable` and `TestListClassificationSampling`.
- [x] Implement the handlers and the `querylog_resolve.go` change: one `CachedVerdicts` query per page
      for the page's names, `threat` null otherwise and no lookup at all while AI is off.
- [x] Create `e2e/ai_threat_test.go` with `TestAIThreatLabelsInQueryLog`.
- [x] Run the e2e test (private binaries, the shared `bin/` untouched) and expect PASS.
- [x] Report the paths. Commit message: `M11 T19: threat checks, list classification, query log threat labels`.

## Evidence

Red first:

```
$ scripts/dev-exec.sh 'go test ./mgmt/internal/ai/threat -run TestThreatCheck -count=1'
github.com/piwi3910/nexora/mgmt/internal/ai/threat: no non-test Go files in /work/nexora/mgmt/internal/ai/threat
FAIL

$ scripts/dev-exec.sh 'go test ./mgmt/internal/ai/threat -run "TestSampleIsUniformAndStable|TestListClassificationSampling" -count=1'
classify_test.go:36:16: undefined: threat.Sample ... undefined: threat.ClassifyAgent ... undefined: threat.GetClassification
FAIL
```

Green:

```
$ scripts/dev-exec.sh 'go test ./mgmt/internal/ai/threat -count=1 -v'
--- PASS: TestThreatCheck (2.25s)
--- PASS: TestThreatCheckRejectsUnknownName (2.26s)
--- PASS: TestSampleIsUniformAndStable (0.01s)
--- PASS: TestListClassificationSampling (2.33s)
ok  	github.com/piwi3910/nexora/mgmt/internal/ai/threat	6.868s

$ scripts/dev-exec.sh 'go test ./mgmt/internal/api -run "TestQueryLogRecordThreat|TestStartAiThreatCheckLimits" -count=1 -v'
--- PASS: TestQueryLogRecordThreat (2.40s)
--- PASS: TestStartAiThreatCheckLimits (2.73s)
ok  	github.com/piwi3910/nexora/mgmt/internal/api	5.207s

$ scripts/dev-exec.sh 'mkdir -p /work/t19-bin && go build -o /work/t19-bin/nexora-mgmt ./mgmt/cmd/nexora-mgmt \
    && go build -o /work/t19-bin/nexora-fixture ./e2e/fixtures/cmd/nexora-fixture'
$ scripts/dev-exec.sh 'NEXORA_E2E_BIN_DIR=/work/t19-bin go test ./e2e -run TestAIThreatLabelsInQueryLog -count=1 -v'
--- PASS: TestAIThreatLabelsInQueryLog (8.86s)
ok  	github.com/piwi3910/nexora/e2e	8.884s
(/work/t19-bin removed afterwards; the shared /work/nexora/bin was never written)

$ scripts/dev-exec.sh 'go test ./mgmt/internal/ai/... ./mgmt/internal/api/... -count=1'
ok  (every package, including ai 63.6s, ai/threat 21.2s and api 186.5s)

$ scripts/dev-exec.sh 'gofmt -l <changed files>; go vet ./mgmt/internal/ai/threat/... ./mgmt/internal/api/... ./mgmt/cmd/...'
(no output)
```

## Paths

Created: `mgmt/migrations/01207_ai_threat.sql`, `mgmt/internal/ai/threat/check.go`,
`mgmt/internal/ai/threat/check_test.go`, `mgmt/internal/ai/threat/classify.go`,
`mgmt/internal/ai/threat/classify_test.go`, `mgmt/internal/api/ai_threat_test.go`,
`mgmt/cmd/nexora-mgmt/ai_threat.go`, `e2e/ai_threat_test.go`.

Modified: `mgmt/internal/api/ai_threat.go` (the stubs became the handlers),
`mgmt/internal/api/querylog_resolve.go` (threat labels, one extra argument),
`mgmt/internal/api/handlers_admin.go` (one call site),
`.procoder/plans/nexora-m11-ai.md` (Task 19 text made truthful).
