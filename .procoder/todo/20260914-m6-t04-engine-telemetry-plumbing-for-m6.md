# M6 T4: Engine telemetry plumbing for M6

Status: open
Created: 2026-09-14

## Description

Implements Task 4 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Append to `engine/tests/telemetry_export.rs`, and add the six new fields to the `record` helper
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test telemetry_export m6_'` and
- [x] Implement `engine/src/telemetry/querylog.rs`: the constants, `FilterSource` with `as_str`, and
- [x] Implement `log_record` in `engine/src/telemetry/otlp.rs` with the new parameter. After the
- [x] Create `engine/src/telemetry/process.rs`:
- [x] In `engine/src/telemetry/metrics.rs`:
- [x] In `engine/src/server/mod.rs` `Scope::finish`, call `self.ctx.counters().observe_record(&r)`
- [x] In `engine/src/upstream/mod.rs`:
- [x] In `engine/src/lib.rs`, add below `VERSION`:
- [x] Run
- [x] Run
- [x] Report the paths (lead commits). Commit message: `engine: M6 query record fields, stats fields, counters, parallel strategy variant`.

## Evidence

- Red: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test telemetry_export m6_'` failed
  to compile: `struct QueryRecord has no field named filter_source` (plus unresolved `FilterSource`,
  `NO_RULE`, `NO_RPZ_ZONE`, `ACL_AUTHORITATIVE` and `log_record` taking 5 arguments).
- Green: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test telemetry_export && cargo test --locked -p nexora-engine --test hot_path_alloc && cargo test --locked -p nexora-engine --test upstream_udp_tcp'`:
  telemetry_export `ok. 10 passed` (4 `m6_` tests), hot_path_alloc `ok. 2 passed`
  (`cache_hit_path_does_not_allocate ... ok`), upstream_udp_tcp `ok. 6 passed`.
- `scripts/dev-exec.sh 'cargo fmt --all && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings && cargo test --locked -p nexora-engine --all-targets'`:
  clippy clean; every target `ok` (lib `242 passed; 1 ignored`, 17 integration test binaries, 0
  failed). `cargo fmt --all -- --check` clean on the laptop.
- The extra test `m6_observe_record_counts_route_miss_latency_acl_and_rewrites` first failed on the
  plan's literal route rule (an ACL refusal counted as `recursive`); the rule now counts `5 + route`
  only for cache misses (plan text updated).
- `NEXORA_COMMIT=abc123 cargo build -p nexora-engine`: `nexora-engine -V` prints
  `nexora-engine dev`, `--version` prints `nexora-engine dev abc123`.

