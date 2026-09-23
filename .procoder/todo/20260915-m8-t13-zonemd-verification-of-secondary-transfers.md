# M8 T13: ZONEMD verification of secondary transfers

Status: implemented (awaiting lead commit)
Created: 2026-09-15

## Description

Implements Task 13 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Add to `refresh_test.go` (reuse its fake primary and store setup):
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/xfrin -run TestRefreshDiscardsTransferFailingZonemd -count=1'`
- [x] Implement:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/xfrin ./mgmt/internal/zone -count=1'` and
- [ ] Report the paths. The lead commits `M8 T13: ZONEMD verification of secondary transfers`.

## Evidence

- Pod toolbox-m7 (synced): `go vet ./mgmt/internal/xfrin ./mgmt/internal/zone && go test ./mgmt/internal/xfrin ./mgmt/internal/zone -count=1`
  -> `ok mgmt/internal/xfrin 20.781s`, `ok mgmt/internal/zone 43.977s`; `gofmt -l` on the changed files: empty.
- Mutation (private copy /work/m8-t13, removed afterwards): skipping the StatusFailed discard ->
  `refresh_test.go:223: refresh error <nil>` in both ixfr=false and ixfr=true subtests.
- Mutation: status write inside `Mutate` replaced with `error(nil)` -> `refresh_test.go:217: first load: not_checked serial 1` (both subtests).
- The test counts incremental answers (4 with ixfr=true, 0 without), so each subtest proves the path it names.
- Remaining: the lead commits `M8 T13: ZONEMD verification of secondary transfers`.
