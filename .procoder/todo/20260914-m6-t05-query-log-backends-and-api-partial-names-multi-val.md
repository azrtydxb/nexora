# M6 T5: Query log backends and API: partial names, multi-value filters, reason fields

Status: open
Created: 2026-09-14

## Description

Implements Task 5 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Add to `mgmt/internal/querylog/opensearch_test.go`:
- [x] Create `mgmt/internal/api/querylog_test.go`:
- [x] In `e2e/gui_test.go` `TestQueryLogBackends`, after the Playwright run, add:
- [x] Extend `TestQueryLogCategoryAttribution` in `e2e/filter_attribution_test.go`:
- [x] Run
- [x] Implement `mgmt/internal/querylog/backend.go` with the new `Query`/`Record` and
- [x] Implement `mgmt/internal/querylog/builtin.go`:
- [x] Implement `mgmt/internal/querylog/opensearch.go`:
- [x] Implement `SearchQueryLog` in `mgmt/internal/api/handlers_admin.go`:
- [x] Run
- [x] Run `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestQueryLogBackends -count=1 -timeout 30m'`
- [ ] Report the paths. Commit message: `querylog: partial names, multi-value filters, reason fields`.

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./mgmt/internal/querylog -run "TestOpenSearchNameWildcardEscaped|TestBuiltinPartialNamesAndMultiValues" -count=1; make webui-placeholder && go test ./mgmt/internal/api -run TestSearchQueryLogRepeatedParameters -count=1'`
  -> build failed: `unknown field QTypes in struct literal of type querylog.Query` (both packages).
- Green: `scripts/dev-exec.sh 'go test ./mgmt/internal/querylog -count=1 && make webui-placeholder && go test ./mgmt/internal/api -run "TestSearchQueryLogRepeatedParameters|TestPermissionsCoverEveryOperation" -count=1'`
  -> `ok .../mgmt/internal/querylog`, `--- PASS: TestPermissionsCoverEveryOperation`, `--- PASS: TestSearchQueryLogRepeatedParameters`, `ok .../mgmt/internal/api`.
- Added `TestSearchQueryLogResolvesNames` (list, catalog source, global/group allowlist, policy group, RPZ zone,
  group-over-global rewrite answers against Postgres): `go test ./mgmt/internal/api -run TestSearchQueryLog -count=1` -> `ok`.
- e2e: `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestQueryLogBackends -count=1 -timeout 30m'`
  -> PASS builtin/partial-name, builtin/multi-value, opensearch/partial-name, opensearch/multi-value; `ok github.com/piwi3910/nexora/e2e 22.832s`.
- `gofmt -l mgmt/internal/querylog mgmt/internal/api e2e` empty; `go vet` on querylog, api and e2e clean.
- `TestQueryLogCategoryAttribution` assertions are written (compile, vet clean) but NOT run: they depend on the engine
  attribution of Task 12; run them in the Task 12 verification.

