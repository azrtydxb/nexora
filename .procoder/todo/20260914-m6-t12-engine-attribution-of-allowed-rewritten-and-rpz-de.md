# M6 T12: Engine attribution of allowed, rewritten and RPZ decisions

Status: open
Created: 2026-09-14

## Description

Implements Task 12 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `engine/tests/attribution.rs`, following the snapshot setup of
- [x] Run
- [x] Implement:
- [x] Run
- [x] Run
- [ ] Run (fmt and clippy done; commit is the lead's)

## Evidence

- Red: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test attribution'` failed to
  compile: `struct ListHit does not have a field named offset`,
  `cannot find function view_with in module synth`, `expected tuple struct or tuple variant, found unit variant FilterDecision::Allowed`.
- Green: `cargo test --locked -p nexora-engine --test attribution`: `test result: ok. 2 passed`.
- `cargo test --locked -p nexora-engine --lib filter::`: `ok. 29 passed`, including
  `filter_index_matches_filterset_semantics` and `colliding_names_never_share_a_decision`.
- `cargo test --locked -p nexora-engine --test hot_path_alloc`: `ok. 2 passed`
  (`cache_hit_path_does_not_allocate` ok).
- `cargo test --locked -p nexora-engine --test telemetry_export`: `ok. 10 passed` (before Task 15's
  `parallel_max > 8` validation landed in the shared tree; afterwards
  `m6_parallel_strategy_maps_with_capped_max` fails because that test applies `parallel_max: 20`,
  which is Task 15's change, not this task's).
- Full `cargo test --locked -p nexora-engine`: lib `ok. 243 passed`; attribution, authoritative_pipeline,
  cache_alloc, control_renewal, control_unit, filter_index_budget, graceful_shutdown, hot_path_alloc,
  inflight, listen_port_zero, logbuf, policy_pipeline, proto_roundtrip, rpz_pipeline,
  server_pipeline, snapshot_apply, upstream_encrypted, upstream_udp_tcp all ok.
- `make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestQueryLogCategoryAttribution -count=1 -timeout 30m`:
  `--- PASS: TestQueryLogCategoryAttribution (25.96s)` (builtin and opensearch).
- `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings`: clean. `rustfmt --edition 2024`
  on the changed files.
- Follow-up fix (found by Task 19): the miss path dropped fast-path attribution (an allowlisted
  name's first, uncached query logged filter "none"). `MissJob` now carries the fast path's
  `QueryRecord`. Red: `allowlisted_cache_miss_keeps_its_filter_attribution` failed with
  `left: ("miss", "none", "", "")`. Green: `--test attribution` `ok. 3 passed`, `--test hot_path_alloc`
  `ok. 2 passed`, `--test telemetry_export` `ok. 10 passed`; server_pipeline, rpz_pipeline,
  policy_pipeline, inflight ok; clippy `-D warnings` clean; `cargo fmt --check` clean; `go vet ./e2e` ok.
