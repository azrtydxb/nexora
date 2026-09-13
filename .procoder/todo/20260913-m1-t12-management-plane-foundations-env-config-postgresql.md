# M1 Task 12: Management plane foundations — env config, PostgreSQL store and schema, PKI, CLI

Status: closed 2026-09-13
Created: 2026-09-13

## Description

Implement Task 12 ("Management plane foundations — env config, PostgreSQL store and schema, PKI, CLI") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 12 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] `TestCAInitLoadAndIssue` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestJoinTokenFormat` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestLoadDefaults` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestLoadValidation` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestMigrateSeedsAndMapsErrors` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Failing first: `scripts/dev-exec.sh 'go test ./mgmt/internal/config/... ./mgmt/internal/pki/... ./mgmt/internal/store/...'` -> `no required module provides package github.com/piwi3910/nexora/e2e/harness` / `no non-test Go files` (build failed for all three).
- Plan deviations (plan text updated): `e2e/harness/harness.go` subset (`New`, `Env`, `Eventually`) and `postgres.go` built ahead of Task 10 with Task 10's signatures; Postgres data dir under `os.MkdirTemp` (t.TempDir parents are 0700 root, unreadable by `dev`); goose `NewProvider` API instead of the global setters; `InitCA` creates `outDir`; `MapError` also maps 57P01-03, connect/net errors, EOF and a closed pool to `ErrUnavailable`.
- `scripts/dev-exec.sh 'go test -race -count=1 -v ./mgmt/... ./gen/...'` -> `--- PASS: TestLoadDefaults`, `TestLoadValidation`, `TestCAInitLoadAndIssue`, `TestJoinTokenFormat`, `TestMigrateSeedsAndMapsErrors (5.14s)`; `ok .../config`, `ok .../pki`, `ok .../store`.
- CLI smoke in the pod: `ca init --out` -> `ca fingerprint: <64 hex>`, `ca.crt` 0644 / `ca.key` 0600; second run -> `refusing to overwrite`, exit 1; unknown command -> usage, exit 2; `migrate` twice against a throwaway cluster -> `migrations applied` both times.
- `gofmt -l mgmt e2e` empty, `go vet ./mgmt/... ./e2e/...` clean; `procoder check` -> 0 blocking.
- Committed 015453a (packages, harness Postgres, go.mod/go.sum) and 2b97124 (CLI with `migrate` and `ca init`).
