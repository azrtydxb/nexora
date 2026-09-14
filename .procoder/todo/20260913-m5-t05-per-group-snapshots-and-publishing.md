# M5 Task 5: Per-group snapshots and publishing

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 5 ("Per-group snapshots and publishing") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 5 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [x] `TestPublishPerGroup` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestScopeMerge` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

Implemented 2026-09-14, not committed (lead commits).

- The todo's `TestPublishPerGroup`/`TestScopeMerge` are the plan's single `TestPublishPerEngineGroup` (per-group upstreams inherit/override, rollout states, immediate on unchanged content, rollback, pause, `Latest`, unknown version).
- `scripts/dev-exec.sh 'go test ./mgmt/internal/snapshot/ ./mgmt/internal/store/ ./mgmt/internal/tsigkey/ ./mgmt/internal/blocklist/ -count=1'` -> all `ok` (incl. new `TestCollectBlobsKeepsStableGroupSnapshots`).
- `go vet ./mgmt/... ./e2e/...` clean. Deviations recorded in the plan (lock mode, blob GC, config_versions.snapshot readers).

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in c44e988
