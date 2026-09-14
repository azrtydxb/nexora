# M7 T9: Query-log labels by config version and concurrent exports (#23, #24)

Status: done
Created: 2026-09-14

## Description

Implements Task 9 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Extend `Sink` in `telemetry_export.rs` with `upstreams: Arc<parking_lot::Mutex<Vec<String>>>`, `delay: Duration`, `active: Arc<AtomicUsize>` and `peak: Arc<AtomicUsize>`. In `LogsService::export`:
- [x] Add the two tests:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test telemetry_export'`. Expect both new tests to FAIL:
- [x] In `runtime.rs`:
- [x] In `otlp.rs`, replace `upstream_name` (and the policy-group lookup in `drain`) with:
- [x] Still in `otlp.rs`, add `pub const MAX_INFLIGHT: usize = 4;`.
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test telemetry_export && cargo test --locked -p nexora-engine --test snapshot_apply && cargo test --locked -p nexora-engine --test hot_path_alloc'` and expect all to pass.

## Evidence

All commands run in the second dev pod (`NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh ...`).

- Red: `cargo test --locked -p nexora-engine --test telemetry_export` -> `test result: FAILED. 6 passed; 2 failed`:
  `upstream_label_uses_the_runtime_that_answered` with `left: ["other"] right: ["fixture"]`;
  `slow_collector_does_not_drop_with_concurrent_exports` with `logs dropped behind a 400 ms collector left: 8000 right: 0`.
- Green: `cargo test --locked -p nexora-engine --test telemetry_export` -> `test result: ok. 8 passed; 0 failed`;
  `--test snapshot_apply` -> `ok. 12 passed`; `--test hot_path_alloc` -> `ok. 2 passed`;
  `--lib telemetry` -> `ok. 4 passed`.
- `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> clean; `rustfmt --edition 2024 --check` on the three files -> clean.
- Stability: 35 of 36 repeated runs of the telemetry_export binary passed; one run at pod load average 12 on 8 cores
  dropped one 1000-record batch in `slow_collector_does_not_drop_with_concurrent_exports`. The test sits exactly at
  capacity (4 exports x 1 per 400 ms = 10 batches/s arriving at 10/s); the spec requires oldest-first eviction at 8
  queued batches, so the exporter was not changed to backpressure into the ring.
- Deviations (plan text updated): groups enumerated via `policy.group(i)` with `map_while` (`PolicyTable` has no
  group count; `filter.rs` is not this task's file); the immediate re-drain loop is bounded to `MAX_QUEUED_BATCHES`
  iterations so a ring that never forms batches (logs off) cannot starve the select loop. Both `debt:` markers in
  `otlp.rs` are removed.
