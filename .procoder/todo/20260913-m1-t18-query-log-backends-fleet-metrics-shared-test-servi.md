# M1 Task 18: Query-log backends, fleet metrics, shared test services, and the observability acceptance tests

Status: open
Created: 2026-09-13

## Description

Implement Task 18 ("Query-log backends, fleet metrics, shared test services, and the observability acceptance tests") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 18 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [ ] `TestBuiltinRingSearchAndCapacity` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestCollectorExportsFleetMetrics` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestOTelSinkDownNoBackpressure` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestObservabilityMetricsTraces` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestOpenSearchQueriesAttributesAndReportsUnavailable` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestParseRealOutput` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestParseRejectsGarbage` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

