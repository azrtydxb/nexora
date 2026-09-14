# M2 Task 4: DoT and DoH listeners

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 4 ("DoT and DoH listeners") of milestone M2 exactly as specified in
`.procoder/plans/nexora-v1-m2.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 4 in `.procoder/plans/nexora-v1-m2.md` is done as written (deviations recorded in the plan first)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- 2026-09-13 `scripts/dev-exec.sh 'cargo test -p nexora-engine --lib server::doh'` before the handler: FAIL `error[E0425]: cannot find function \`handle\` in this scope`(x3) and`min_ttl`.
- `cargo test --locked -p nexora-engine --lib -- server:: encrypted` after: `test result: ok. 14 passed; 0 failed` (doh x4 incl. `http2_get_and_post_over_tls`, dot `dot_end_to_end_with_proxy_header`, stream, proxy, tls, `encrypted_metrics_render`).
- Mutation: with `ENCRYPTED.register(&mut reg)` removed, `encrypted_metrics_render ... FAILED`.
- Isolated tree (HEAD + Task 4/5 changes only, Task 6 in-progress edits excluded), `CARGO_TARGET_DIR=/work/target-iso-t45`: `cargo fmt --all -- --check` OK; `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` clean; `cargo test --locked -p nexora-engine --all-targets`: lib `39 passed; 1 ignored`, all 12 integration binaries ok (listen_port_zero 2 passed, server_pipeline 4 passed, hot_path_alloc 1 passed, cache_alloc 1 passed); release build OK.
- M1 e2e with that release engine: `go test -run "TestForwardCacheTTL|TestUpstreamFailover|TestDedupAllWaitersAnswered|TestEDNSTruncationTCP|TestInvalidSnapshotRejected|TestBlocklistSubscription" ./e2e/` all PASS (TestObservabilityMetricsTraces not run: NEXORA_E2E_JAEGER_QUERY_URL unset in the pod).
- Not committed (lead commits).

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in 85dbb4b
