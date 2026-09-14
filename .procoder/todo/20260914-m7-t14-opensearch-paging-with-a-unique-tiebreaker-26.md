# M7 T14: OpenSearch paging with a unique tiebreaker (#26)

Status: open
Created: 2026-09-14

## Description

Implements Task 14 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Re-read `opensearch.go` at the M6 head (M6 #54, #56, #61 change the filters) and keep M6's filter code.
- [ ] Add to `opensearch_test.go` (imports `encoding/base64`, `encoding/json`):
- [ ] Create `e2e/querylog_paging_test.go`:
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/querylog -run TestOpenSearchSortHasUniqueTiebreaker'`. Expect FAIL `sort lacks the _id tiebreaker`. Run `scripts/dev-exec.sh 'make e2e-build && go test -count=1 ./e2e -run TestOpenSearchPagesRecordsSharingAMillisecond'`. Expect FAIL `paged r
- [ ] In `opensearch.go`, set `"sort": []map[string]any{{"@timestamp": map[string]any{"order": "desc"}}, {"_id": map[string]any{"order": "asc"}}},` and delete the `debt:` comment. In cursor decoding:
- [ ] Run both commands again and expect PASS. Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/querylog/... && go test -count=1 ./e2e -run "TestQueryLogBackends|TestQueryLogCategoryAttribution"'` and expect PASS.

## Evidence

