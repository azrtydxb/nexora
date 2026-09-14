# M7 T19: Collector restart waits for its port (#10)

Status: open
Created: 2026-09-14

## Description

Implements Task 19 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Add to `harness_test.go`:
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./e2e/harness -run TestHarnessOtelcolRestartWaitsForPortHolder'`. Expect FAIL `otelcol-contrib exited: ... address already in use`.
- [ ] Replace `Restart` (delete the `debt:` comment):
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./e2e/harness -run Otelcol && make e2e-build && go test -count=1 ./e2e -run TestOTelSinkDownNoBackpressure'` and expect PASS.

## Evidence

