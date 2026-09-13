# M4 Task 9: BIND zone file import and export

Status: open
Created: 2026-09-13

## Description

Implement Task 9 ("BIND zone file import and export") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 9 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [x] `TestExportIsStableAndReparses` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestParseAllTypes` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestParseRefusals` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestZoneFileRoundTrip` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- First run (laptop, after writing the implementation): `TestExportIsStableAndReparses` FAIL (golden absent); the undefined-Parse red step was not run separately. TestZoneFileRoundTrip was not run before the import API existed.
- `go test ./mgmt/internal/zonefile/ -count=1 -update` wrote `testdata/all-types.export.golden`; `scripts/dev-exec.sh 'go test ./mgmt/internal/zonefile/ ./mgmt/internal/zone/ ./mgmt/internal/api/ -count=1'` -> ok x3.
- `scripts/dev-exec.sh 'go test ./e2e/ -run TestZoneFileRoundTrip -count=2 -v'` -> PASS (ldns-compare-zones -a -s -e reports +0 -0 ~0).
- Deviations recorded in the plan ("Built (as implemented)" under Task 9). TestGUICoverage stays red for importZoneFile/exportZoneFile until Task 15.
- Not committed (lead commits).
