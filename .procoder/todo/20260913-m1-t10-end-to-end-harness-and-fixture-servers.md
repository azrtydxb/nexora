# M1 Task 10: End-to-end harness and fixture servers

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 10 ("End-to-end harness and fixture servers") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 10 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] `TestDNSFixtureBehaviours` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestHarnessOtelcolStarts` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestHarnessPostgres` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestHarnessStandaloneEngineAnswersViaFixture` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./e2e/fixtures/... ./e2e/harness/...'` -> build failed with
  `undefined: dnsConfig`, `undefined: startDNSFixture`, `env.StartDNSFixture undefined`,
  `undefined: harness.BaseSnapshot` (the harness package already existed from Tasks 12-15, so the
  plan's "no required module provides package" message did not apply).
- Green (dev pod, fixture built into a temp dir as `NEXORA_E2E_BIN_DIR`):
  `go test -count=1 -race -v -run "Fixture|Postgres|Otelcol" ./e2e/harness/ ./e2e/fixtures/...` ->
  `--- PASS: TestDNSFixtureBehaviours`, `--- PASS: TestHarnessPostgres`,
  `--- PASS: TestHarnessOtelcolStarts`, `--- PASS: TestHarnessFixtureClients`,
  `ok github.com/piwi3910/nexora/e2e/fixtures/cmd/nexora-fixture`.
- Otelcol with every optional exporter (file, opensearch, otlp/jaeger) plus Restart and Stop started
  under otelcol-contrib 0.160.0 (throwaway test, not committed).
- `TestHarnessStandaloneEngineAnswersViaFixture` NOT yet green: with the in-progress Task 8 engine
  binary in the pod it started the engine (`nexora-engine: serving version 1`), the query was
  answered and counted by the fixture, then failed at `scrape metrics: ... connection refused` —
  the `/metrics` endpoint is Task 9. Re-run once Tasks 8 and 9 are committed:
  `scripts/dev-exec.sh 'make e2e-build && go test -count=1 ./e2e/harness/...'`.
- Consumers unbroken: `go test -count=1 ./mgmt/...` in the pod -> all packages `ok`.
- `make -n mgmt-test` -> `go test -race -count=1 ./mgmt/... ./gen/...` (no `./bench/...` while it
  does not exist).
- `go vet ./e2e/harness/ ./e2e/fixtures/...`, `gofmt -l e2e` clean; `procoder check` over the
  changed files -> `13 clean, 0 unformatted ... (0 blocking)`.

Closing evidence (lead, 2026-09-14):

- TestHarnessStandaloneEngineAnswersViaFixture: passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- gate/commit: commit gate passed on every commit for this task; todo last committed in 6f26c58
