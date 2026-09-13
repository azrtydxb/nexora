# M1 Task 9: Prometheus endpoint, OTLP logs/traces/metrics export and stats snapshot

Status: open
Created: 2026-09-13

## Description

Implement Task 9 ("Prometheus endpoint, OTLP logs/traces/metrics export and stats snapshot") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 9 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [ ] Rust tests pass in the dev pod: `log_record_attributes_and_trace_rules`, `logs_and_servfail_traces_reach_the_collector`, `metrics_endpoint_exposes_every_architecture_name`, `unreachable_collector_drops_with_counter_and_never_blocks_push`
- [ ] procoder gate clean over the changed files; work committed

## Evidence

