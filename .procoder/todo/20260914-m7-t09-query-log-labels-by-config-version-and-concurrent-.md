# M7 T9: Query-log labels by config version and concurrent exports (#23, #24)

Status: open
Created: 2026-09-14

## Description

Implements Task 9 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Extend `Sink` in `telemetry_export.rs` with `upstreams: Arc<parking_lot::Mutex<Vec<String>>>`, `delay: Duration`, `active: Arc<AtomicUsize>` and `peak: Arc<AtomicUsize>`. In `LogsService::export`:
- [ ] Add the two tests:
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test telemetry_export'`. Expect both new tests to FAIL:
- [ ] In `runtime.rs`:
- [ ] In `otlp.rs`, replace `upstream_name` (and the policy-group lookup in `drain`) with:
- [ ] Still in `otlp.rs`, add `pub const MAX_INFLIGHT: usize = 4;`.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test telemetry_export && cargo test --locked -p nexora-engine --test snapshot_apply && cargo test --locked -p nexora-engine --test hot_path_alloc'` and expect all to pass.

## Evidence

