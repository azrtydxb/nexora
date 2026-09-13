# M1 Task 18: Query-log backends, fleet metrics, shared test services, and the observability acceptance tests

Status: open
Created: 2026-09-13

## Description

Implement Task 18 ("Query-log backends, fleet metrics, shared test services, and the observability acceptance tests") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 18 (all but the `kubectl apply` of `deploy/dev/dev-pod.yaml`, deferred so the toolbox pod is not recreated under concurrent agents; recorded in the plan) in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] `TestBuiltinRingSearchAndCapacity` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestCollectorExportsFleetMetrics` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestOTelSinkDownNoBackpressure` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestObservabilityMetricsTraces` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestOpenSearchQueriesAttributesAndReportsUnavailable` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestParseRealOutput` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestParseRejectsGarbage` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Red: `go vet ./mgmt/internal/querylog/ ./mgmt/internal/stats/` before implementing -> `undefined: querylog.NewBuiltin`, `undefined: stats.NewCollector`.
- `scripts/dev-exec.sh 'go test -count=1 ./mgmt/... ./bench/...'` -> all `ok` (querylog, stats, api, auth, blocklist, config, control, pki, snapshot, store, perfgate, dnsperf).
- `scripts/dev-exec.sh 'export NEXORA_E2E_OPENSEARCH_URL=http://opensearch.nexora.svc.cluster.local:9200 NEXORA_E2E_JAEGER_QUERY_URL=http://jaeger.observability.svc.cluster.local:16686; make e2e-build && go test -count=1 -v -run TestObservabilityMetricsTraces ./e2e/'` -> `--- PASS: TestObservabilityMetricsTraces (13.87s)`.
- `scripts/dev-exec.sh 'go test -count=1 -v -run TestOTelSinkDownNoBackpressure ./e2e/'` -> `median QPS collector up=123034 down=175880`, `--- PASS: TestOTelSinkDownNoBackpressure (54.35s)`.
- Shared services: `kubectl --context kw apply -f deploy/kw/namespace.yaml -f deploy/kw/opensearch.yaml && kubectl --context kw -n nexora rollout status statefulset/opensearch` -> rolled out; from the toolbox pod `_cluster/health` -> `"status":"green"`, Jaeger `/api/services` -> JSON, TCP connect to `jaeger.observability.svc.cluster.local:4317` ok.
- OpenSearch document shape and `.keyword` filters verified by feeding otelcol-contrib 0.160's `opensearch` exporter an OTLP log and querying OpenSearch 3.8.0 (bare `term` on `attributes.dns.response.code` -> 0 hits; probe index deleted).
- `procoder check` over the changed files -> 16 clean, 0 unformatted, 0 blocking.
- Open: `kubectl --context kw apply -f deploy/dev/dev-pod.yaml` (recreates the toolbox pod) not yet run.
