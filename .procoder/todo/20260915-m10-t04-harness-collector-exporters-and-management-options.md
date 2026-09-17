# M10 T4: Harness collector exporters and management options

Status: open
Created: 2026-09-15

## Description

Implements Task 4 of `.procoder/plans/nexora-m10-querylog-backends.md` (spec `.procoder/specs/nexora-m10-querylog-backends.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `e2e/harness/otelcol_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./e2e/harness -run TestOtelcolConfigValidates -count=1'` and expect
- [x] Implement in `otelcol.go`:
- [x] In `mgmt.go`, after the OpenSearch env line, add:
- [x] Run the command from step 2 and expect PASS. Then run
- [x] Report the paths. Commit message: `e2e harness: collector exporters for clickhouse and loki`.

## Evidence

All commands run with `NEXORA_DEV_DEPLOY=toolbox-m10`. Not committed yet (lead commits).

- Red: `scripts/dev-exec.sh 'go test ./e2e/harness -run TestOtelcolConfigValidates -count=1'` -> `undefined: RenderOtelcolConfig` (and unknown OtelcolConfig fields), build failed.
- Green: same command -> `ok  github.com/piwi3910/nexora/e2e/harness 0.234s` (includes `otelcol-contrib validate` 0.160.0 of the full config, and the bare config without clickhouse/loki).
- OTTL is really parsed by `validate`: the rendered config with `log.bogus` instead of `log.body` -> `unable to parse OTTL statement ... segment "bogus" ... is not a valid path`, exit 1.
- `make e2e-build` -> exit 0; `NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestQueryLogBackends -count=1 -v` -> `--- PASS: TestQueryLogBackends/builtin (9.34s)`.
- Same run: `--- FAIL: TestQueryLogBackends/opensearch/partial-name` at `gui_test.go:166 case-insensitive: [www.you-254115... www.you-860371...]`: pre-existing, data-dependent assertion (it sorts every `TUBE.TEST` hit in the shared `nexora-querylog-v2` index and expects the current run's name last; older runs with a larger random suffix break it). Not caused by this change: the default rendered index and pipelines are unchanged. Not fixed here (outside Task 4 files).
- `go vet ./e2e/...` clean; `go test ./e2e/harness -count=1` -> `ok` (5.6 s); `gofmt -l e2e/harness` empty.
