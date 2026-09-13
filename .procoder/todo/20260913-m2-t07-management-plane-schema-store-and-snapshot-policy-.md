# M2 Task 7: Management plane schema, store and snapshot policy section

Status: open
Created: 2026-09-13

## Description

Implement Task 7 ("Management plane schema, store and snapshot policy section") of milestone M2 exactly as specified in
`.procoder/plans/nexora-v1-m2.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 7 in `.procoder/plans/nexora-v1-m2.md` is done as written (deviations recorded in the plan first)
- [x] `TestBuildPolicySection` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestPolicyGroups` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestPolicyGroupsRevisionAndCIDRUniqueness` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestRewritesScopes` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestRewritesScopesAndGlobalSafeSearch` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- Failing first: `scripts/dev-exec.sh 'go test ./mgmt/internal/store/ -run "TestPolicyGroups|TestRewritesScopes"'` -> `undefined: store.ErrCIDRInUse`, `undefined: store.UpdatePolicyGroup`, `FAIL ... store [build failed]`; `go test ./mgmt/internal/snapshot/ -run TestBuildPolicySection` -> `undefined: snapshot.SafeSearchYouTubeStrict`, `[build failed]`.
- Pass: `scripts/dev-exec.sh 'go vet ./mgmt/internal/snapshot/ ./mgmt/internal/store/ ./mgmt/internal/blocklist/ && go test -count=1 ./mgmt/internal/snapshot/ ./mgmt/internal/store/ ./mgmt/internal/blocklist/'` -> `ok snapshot 4.678s`, `ok store 8.800s`, `ok blocklist 0.016s`.
- The changed fetcher due-list SQL was executed against a migrated database in a throwaway test (removed afterwards) -> ok.
- `scripts/dev-exec.sh 'make mgmt-test'` (race) -> ok for api, auth, blocklist, config, control, pki, querylog, snapshot, stats, store, gen, bench.
- Deviations recorded in the plan under Task 7 "As built": block-kind check for group filter lists, `store.GetRewrite`, advisory lock in `checkCNAME`, blob GC keeps group blobs.
- Not committed (the lead commits serially per the implementer brief).
