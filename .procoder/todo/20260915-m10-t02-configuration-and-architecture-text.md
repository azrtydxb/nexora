# M10 T2: Configuration and architecture text

Status: open
Created: 2026-09-15

## Description

Implements Task 2 of `.procoder/plans/nexora-m10-querylog-backends.md` (spec `.procoder/specs/nexora-m10-querylog-backends.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Add to `mgmt/internal/config/config_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/config -run TestLoadQueryLogBackends -count=1'`
- [x] Implement in `config.go`:
- [x] Run the command from step 2 and expect PASS. Then run
- [x] Edit `docs/architecture.md`:
- [x] Run `scripts/pc-format.sh docs/architecture.md`.
- [x] Report the paths. Commit message: `config: clickhouse and loki query-log settings`.

## Evidence

All commands run with `NEXORA_DEV_DEPLOY=toolbox-m10`. Not committed yet (lead commits).

- Red: `scripts/dev-exec.sh 'go test ./mgmt/internal/config -run TestLoadQueryLogBackends -count=1'` -> `c.ClickHouse undefined (type config.Config has no field or method ClickHouse)`, build failed.
- Green: same command -> `ok  github.com/piwi3910/nexora/mgmt/internal/config 0.004s`; `go test ./mgmt/internal/config -count=1` -> `ok`; `go vet ./mgmt/internal/config` clean; `gofmt -l` empty.
- Existing `TestLoadValidation` "bad backend" case changed from `loki` (now valid) to `elastic`.
- `scripts/pc-format.sh docs/architecture.md` run; architecture gains the M10 env variables, the query-log backend paragraph, the harness binaries and the kw ClickHouse/Loki topology.
