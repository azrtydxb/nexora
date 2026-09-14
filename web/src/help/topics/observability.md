<!-- operations: Monitoring and alerts; Query logs, traces and OTLP -->

# Query log and observability

## Query log

Engines send a record of every query to the query log without slowing answers; when the queue is full,
records are dropped instead. The built-in backend keeps the newest records in memory on each management
instance, only for its own engines, and loses them on restart, so it suits single-instance installs.
With OpenSearch, engines send their records through an OpenTelemetry Collector and every instance
searches the same indices. Each record shows why a query was blocked, allowed, rewritten or refused.

## Dashboard

The dashboard charts queries, latency, cache hits and outcomes from the statistics every engine reports
every 10 seconds, with five-minute summaries kept for 8 days. Its health panel lists disconnected
engines, stale categories, upstreams that are down, DNS certificates expiring within 14 days, trust
anchor refresh failures and dropped telemetry.

## Metrics

The management plane and every engine serve Prometheus metrics. Useful signals include
`nexora_mgmt_engines_disconnected`, halted rollouts, `nexora_mgmt_filter_list_stale`,
`nexora_upstream_up == 0`, certificates close to expiry and increases of `nexora_export_dropped_total`.
Engines also push OTLP metrics every 15 seconds to their OTLP endpoint.

## Traces

A query becomes a trace when the sampling rate selects it (0, the default, samples none), when it takes
longer than the slow threshold (100 ms by default), or when it ends in SERVFAIL. Traces go to the
engine group's OTLP endpoint, else the one in Settings. An unreachable endpoint drops data and counts it;
it never slows queries.
