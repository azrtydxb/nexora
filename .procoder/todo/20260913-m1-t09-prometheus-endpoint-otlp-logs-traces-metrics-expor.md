# M1 Task 9: Prometheus endpoint, OTLP logs/traces/metrics export and stats snapshot

Status: closed 2026-09-13
Created: 2026-09-13

## Description

Implement Task 9 ("Prometheus endpoint, OTLP logs/traces/metrics export and stats snapshot") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 9 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] Rust tests pass in the dev pod: `log_record_attributes_and_trace_rules`, `logs_and_servfail_traces_reach_the_collector`, `metrics_endpoint_exposes_every_architecture_name`, `unreachable_collector_drops_with_counter_and_never_blocks_push` (plus `failed_export_counts_every_record_of_the_batch`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Failing first: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test telemetry_export'` -> E0432 ``unresolved import `nexora_engine::telemetry::otlp` `` / `serve_metrics`, E0599 no method `stats`.
- `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test telemetry_export'` -> `test result: ok. 5 passed; 0 failed`.
- `scripts/dev-exec.sh 'make engine-test'` -> every binary `test result: ok` (lib 22 passed incl. `presentation_escapes_and_roots`, server_pipeline 4, hot_path_alloc 1, telemetry_export 5, ...), no FAILED.
- `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> Finished, no warnings; `cargo fmt -p nexora-engine --check` clean.
- Mutation check: replacing the failed-export `drop(Signal::Logs, n)` with a no-op made `failed_export_counts_every_record_of_the_batch` FAIL (`4 passed; 1 failed`); reverted. The plan's unreachable-collector test alone passes via ring overflow, hence the added test.
- Smoke: standalone `nexora-engine` in the pod served version 1; `curl /metrics` -> `200`, `content-type: application/openmetrics-text; version=1.0.0; charset=utf-8`, `nexora_queries_total{transport="udp",rcode="SERVFAIL"} 1` after one dig, histogram buckets 50us..2s + Inf; `/nope` -> 404.
- Plan deviations (plan text updated): OpenMetrics content type; `nexora_config_version` from `rt.version`; rcode/transport label values; `Metrics::totals`/`Totals`, `RCODE_LABELS`, `Transport::as_str`; `nexora.cache` span end `filter_us + cache_us`; drain loop also wakes on export completion; spans dropped with their batch counted as traces; exports bounded by `tokio::time::timeout`; fifth test.
