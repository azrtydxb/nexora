# M7 T14: OpenSearch paging with a unique tiebreaker (#26)

Status: open
Created: 2026-09-14

## Description

Implements Task 14 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Re-read `opensearch.go` at the M6 head (M6 #54, #56, #61 change the filters) and keep M6's filter code.
- [x] Add to `opensearch_test.go` (imports `encoding/base64`, `encoding/json`):
- [x] Create `e2e/querylog_paging_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/querylog -run TestOpenSearchSortHasUniqueTiebreaker'`. Expect FAIL `sort lacks the _id tiebreaker`. Run `scripts/dev-exec.sh 'make e2e-build && go test -count=1 ./e2e -run TestOpenSearchPagesRecordsSharingAMillisecond'`. Expect FAIL `paged r
- [x] In `opensearch.go`, set `"sort": []map[string]any{{"@timestamp": map[string]any{"order": "desc"}}, {"_id": map[string]any{"order": "asc"}}},` and delete the `debt:` comment. In cursor decoding:
- [x] Run both commands again and expect PASS. Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/querylog/... && go test -count=1 ./e2e -run "TestQueryLogBackends|TestQueryLogCategoryAttribution"'` and expect PASS.

## Evidence

Run in the second dev pod (`NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh '...'`), 2026-09-14/15.

- Re-read: m7 at 3ac5883 does not yet carry M6 T5's filter changes; only the `sort` entry and cursor decoding changed, filter code untouched.
- Red: `go test -count=1 ./mgmt/internal/querylog -run TestOpenSearchSortHasUniqueTiebreaker` -> `FAIL ... sort lacks the _id tiebreaker: {..."sort":[{"@timestamp":"desc"}]...}`.
- Red: `make e2e-build && go test -count=1 ./e2e -run TestOpenSearchPagesRecordsSharingAMillisecond` -> `FAIL ... paged records map[r0.paging.test.:true], want all 3 sharing one millisecond`.
- Green: same unit test -> `ok github.com/piwi3910/nexora/mgmt/internal/querylog 0.042s`; same e2e -> `ok github.com/piwi3910/nexora/e2e 3.769s`.
- Regression: `go test -count=1 ./mgmt/internal/querylog/...` -> `ok ... 0.031s`; `go test -count=1 ./e2e -run "TestQueryLogBackends|TestQueryLogCategoryAttribution"` -> `ok github.com/piwi3910/nexora/e2e 28.199s`.
- `gofmt -l` clean; `go vet ./mgmt/internal/querylog/ ./e2e/` clean.
- Not yet committed (the lead commits).

