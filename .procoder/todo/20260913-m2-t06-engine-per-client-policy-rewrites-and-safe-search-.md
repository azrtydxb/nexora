# M2 Task 6: Engine per-client policy, rewrites and safe-search answers

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 6 ("Engine per-client policy, rewrites and safe-search answers") of milestone M2 exactly as specified in
`.procoder/plans/nexora-v1-m2.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 6 in `.procoder/plans/nexora-v1-m2.md` is done as written (deviations recorded in the plan first)
- [x] procoder gate clean over the changed files; work committed (fmt/clippy clean; commit left to the lead per implementer brief)

## Evidence

- `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --no-fail-fast'` — every binary `ok`: lib 45 passed (incl. `filter::policy_tests` x4, `server::rewrite::tests` x2), `hot_path_alloc` 1 passed (`cache_hit_path_does_not_allocate` from policy-group client 10.1.2.3), `policy_pipeline` 2 passed, `server_pipeline` 4, `snapshot_apply` 6, `telemetry_export` 5, `inflight` 3, `upstream_udp_tcp` 6, `upstream_encrypted` 3, `listen_port_zero` 2, `control_unit` 3, `cache_alloc` 1, `proto_roundtrip` 1.
- Mutation: forcing `CacheKey.partition` to 0 makes `groups_never_see_each_others_cached_answers` FAIL ("strict client was served the global cache entry").
- `scripts/dev-exec.sh 'cargo fmt -p nexora-engine --check; cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'` — clean.
- `scripts/dev-exec.sh 'make e2e-build'` then `go test ./e2e/ -run '^(TestForwardCacheTTL|TestUpstreamFailover|TestDedupAllWaitersAnswered|TestBlocklistSubscription|TestEDNSTruncationTCP|TestInvalidSnapshotRejected)$'` — `ok github.com/piwi3910/nexora/e2e 16.358s`; `TestQueryLogBackends/builtin` and `TestOTelSinkDownNoBackpressure` PASS (opensearch/jaeger subtests need env vars not set in the running pod).
- Note: one full run saw `cache_alloc` fail once and pass on rerun (it counts allocations from all threads; pre-existing flake, file untouched).

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in 85dbb4b
