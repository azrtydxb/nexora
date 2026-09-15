# M11 T12: Natural-language query-log search

Status: open
Created: 2026-09-15

## Description

Implements Task 12 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/ai/qlsearch/qlsearch_test.go` with `TestQueryLogSearchTranslation`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/qlsearch -count=1'` and expect FAIL:
- [x] Implement `StartAiQueryLogSearch`:
- [x] Create `e2e/ai_querylog_test.go` with `TestAIQueryLogSearch`:
- [x] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e -run "TestAIQueryLogSearch|TestAIOpenAICompatibleWire" -count=1 -v'`
- [x] Report the paths. Commit message: `M11 T12: natural-language query log search`.

## Evidence

- Before `qlsearch.go` existed, `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/qlsearch -count=1'` failed with `FAIL ... [build failed]` ("no non-test Go files"). The plan expected `undefined: qlsearch.New`; this is the same missing implementation. After implementing: `ok github.com/piwi3910/nexora/mgmt/internal/ai/qlsearch 4.608s` (TestQueryLogSearchTranslation, TestQueryLogSearchDefaultsToLastHour).
- Mutation check: changing the 7-day cap to 9 days makes TestQueryLogSearchTranslation FAIL, and changing the default range to 2 h makes TestQueryLogSearchDefaultsToLastHour FAIL. Both were reverted.
- Other agents' uncommitted work (T13 anomaly, T16 assistant) did not build in the shared tree. So verification ran in a private copy in the pod: `git archive HEAD` plus the T12 files, at `/work/t12-src`. There, `go vet ./mgmt/internal/ai/qlsearch ./mgmt/internal/api ./mgmt/cmd/nexora-mgmt ./e2e` passed. `go test ./mgmt/internal/api -run "AI|Ai|M11|Contract"` gave `ok 19.692s`, and the qlsearch tests gave `ok`.
- Private e2e build: the engine was built with `CARGO_TARGET_DIR=/work/target-t12`. Mgmt and the fixture were built into `/work/e2ebin-t12`. Then `NEXORA_E2E_BIN_DIR=/work/e2ebin-t12 go test ./e2e -run "TestAIQueryLogSearch|TestAIOpenAICompatibleWire" -count=1 -v` gave `--- PASS: TestAIOpenAICompatibleWire (3.80s)` (no skip), `--- PASS: TestAIQueryLogSearch (4.91s)` and `ok github.com/piwi3910/nexora/e2e 8.732s`. The private dirs were deleted afterwards. `make e2e-build` was not used because it writes the shared `bin/` and `target/`.
- `gofmt -l` is clean on every changed Go file.
- Plan text updated with an "As built" note under the Task 12 Interfaces. It covers the JSON tags, the range defaults (request range, then last hour), catalog-named lists, the top-list debt, and the skip replaced by a fatal assertion.
- Not committed (the lead commits).

