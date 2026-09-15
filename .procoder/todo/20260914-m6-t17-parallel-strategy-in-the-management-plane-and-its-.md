# M6 T17: Parallel strategy in the management plane and its e2e proof

Status: open
Created: 2026-09-14

## Description

Implements Task 17 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `e2e/upstream_parallel_test.go`:
- [x] Run
- [x] Create `mgmt/migrations/00601_parallel_strategy.sql`:
- [x] In `mgmt/internal/api/handlers_dns.go`:
- [x] Run
- [x] Report the paths. Commit message: `mgmt: parallel strategy setting and e2e latency proof`.

## Evidence

Built in dev pod `toolbox` with private binaries (`CARGO_TARGET_DIR=/work/target-t17`, `/tmp/t17-bin`,
both deleted afterwards) instead of `make e2e-build`, so the shared `/work/nexora/bin` was not touched.

- Red: `NEXORA_E2E_BIN_DIR=/tmp/t17-bin go test ./e2e -run 'TestUpstreamParallelStrategy$' -count=1 -timeout 20m`
  -> `upstream_parallel_test.go:34: PUT /resolver-settings: status 400, want 200 ... violates check constraint
  "resolver_settings_strategy_check"` (plan said 500; `store.MapError` maps check violations to 400).
- `go vet ./mgmt/internal/api ./mgmt/internal/snapshot ./e2e` clean; `gofmt -l` empty.
- `go test ./mgmt/internal/api ./mgmt/internal/snapshot ./mgmt/internal/store -count=1` -> all `ok`
  (store's `MigrateDownTo(403)` exercises the 00601 Down).
- Green: `NEXORA_E2E_BIN_DIR=/tmp/t17-bin go test ./e2e -run "TestUpstreamParallelStrategy|TestUpstreamParallelLatency|TestUpstreamFailover" -count=1 -timeout 40m -v`
  -> `--- PASS: TestUpstreamFailover (1.13s)`, `--- PASS: TestUpstreamParallelStrategy (4.65s)`,
  `--- PASS: TestUpstreamParallelLatency (309.56s)`, `ok github.com/piwi3910/nexora/e2e 315.369s`.
- Latency log line: `ordered p95 154.829005ms p99 158.706384ms; parallel p95 2.016552ms p99 5.664077ms (uncached, one upstream +150 ms)`.
- Not yet committed (the lead commits).
