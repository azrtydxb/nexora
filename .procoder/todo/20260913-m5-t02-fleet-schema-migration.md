# M5 Task 2: Fleet schema migration

Status: open
Created: 2026-09-13

## Description

Implement Task 2 ("Fleet schema migration") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 2 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [ ] `TestFleetMigration` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestFleetMigrationDownUp` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

Implemented 2026-09-14, not committed (lead commits).

- Red: `scripts/dev-exec.sh 'go test ./mgmt/internal/store/ -run TestFleetMigration -count=1'` -> build failed, `undefined: store.DefaultEngineGroupID`.
- Green: same command -> `--- PASS: TestFleetMigration`, `--- PASS: TestFleetMigrationBackfillsCertificatesAndDownUp`, `--- PASS: TestFleetMigrationPopulatedM4Database`, `ok github.com/piwi3910/nexora/mgmt/internal/store 6.800s`.
- Added `TestFleetMigrationPopulatedM4Database` (plan updated): populated M4 database (engines incl. soft-deleted, join token, upstream, policy group + rewrites, config versions 1-2) migrates cleanly; engines and token land in `default`; group snapshot/rollout/stable_version backfilled; certificates backfilled; down to 403 again works.
- The todo's `TestFleetMigrationDownUp` is the plan's `TestFleetMigrationBackfillsCertificatesAndDownUp`.
- `scripts/dev-exec.sh make mgmt-test` (race, all packages) -> 23 packages `ok`, exit 0.
