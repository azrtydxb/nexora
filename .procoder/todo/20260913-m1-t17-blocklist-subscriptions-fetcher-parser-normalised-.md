# M1 Task 17: Blocklist subscriptions — fetcher, parser, normalised blobs, and the subscription acceptance test

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 17 ("Blocklist subscriptions — fetcher, parser, normalised blobs, and the subscription acceptance test") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 17 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] `TestBlocklistSubscription` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestNormalizeAndCompressAreDeterministic` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestParseFormats` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Red: `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/blocklist/...'` -> `no non-test Go files in /work/nexora/mgmt/internal/blocklist` / `FAIL [build failed]`; `scripts/dev-exec.sh bash -c 'make e2e-build && go test -count=1 -run TestBlocklistSubscription ./e2e/'` -> `POST /filter-lists/<id>/refresh: status 400 ... "filter list fetching is not enabled on this instance"`.
- Green: `scripts/dev-exec.sh bash -c 'go test -count=1 ./mgmt/internal/blocklist/... ./mgmt/internal/api/... ./mgmt/internal/snapshot/...'` -> `ok .../blocklist`, `ok .../api`, `ok .../snapshot` (TestParseFormats, TestNormalizeAndCompressAreDeterministic pass).
- `scripts/dev-exec.sh bash -c 'make e2e-build && go test -count=1 -v -run TestBlocklistSubscription ./e2e/'` -> `--- PASS: TestBlocklistSubscription (3.52s)`; `go test -count=3 -run TestBlocklistSubscription ./e2e/` -> `ok github.com/piwi3910/nexora/e2e 12.704s`.
- `procoder check <changed files>` -> `6 clean, 0 unformatted, 0 unchecked` (0 blocking); `procoder lint` -> `0 finding(s)`; `golangci-lint run ./mgmt/internal/blocklist/...` -> `0 issues`; `go vet` clean.
- Deviations recorded in the plan's Task 17 section (RefreshNow waits for the list lock; GC/publish serialised on the config_version lock; 4 concurrent attempts; entry_count = unique domains; IDNA only for non-ASCII names).
