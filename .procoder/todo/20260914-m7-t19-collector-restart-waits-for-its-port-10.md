# M7 T19: Collector restart waits for its port (#10)

Status: open
Created: 2026-09-14

## Description

Implements Task 19 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Add to `harness_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./e2e/harness -run TestHarnessOtelcolRestartWaitsForPortHolder'`. Expect FAIL `otelcol-contrib exited: ... address already in use`.
- [x] Replace `Restart` (delete the `debt:` comment):
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./e2e/harness -run Otelcol && make e2e-build && go test -count=1 ./e2e -run TestOTelSinkDownNoBackpressure'` and expect PASS.

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test -count=1 ./e2e/harness -run TestHarnessOtelcolRestartWaitsForPortHolder'` -> FAIL, collector log `listen tcp 127.0.0.1:38625: bind: address already in use`.
- Green: `go vet ./e2e/harness` clean, `gofmt -l e2e/harness` empty; `go test -count=1 ./e2e/harness -run Otelcol -v` -> `--- PASS: TestHarnessOtelcolStarts (0.44s)`, `--- PASS: TestHarnessOtelcolRestartWaitsForPortHolder (2.29s)`, `ok github.com/piwi3910/nexora/e2e/harness 2.738s`.
- `make e2e-build` failed in the pod on another task's in-progress edit (`mgmt/internal/zone/build.go:100: opts.Edit undefined`, Task 17), so the pod's bin dir kept the mgmt binary built at 20:07 (engine rebuilt 20:13). `NEXORA_E2E_BIN_DIR=/work/nexora/bin go test -count=1 ./e2e -run TestOTelSinkDownNoBackpressure` (calls `col.Restart(env)`, harness compiled fresh) -> `ok github.com/piwi3910/nexora/e2e 55.898s`. Re-run after Task 17 lands to prove the full `make e2e-build` chain.
