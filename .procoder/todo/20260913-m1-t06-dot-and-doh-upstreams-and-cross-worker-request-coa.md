# M1 Task 6: DoT and DoH upstreams, and cross-worker request coalescing

Status: closed 2026-09-13
Created: 2026-09-13

## Description

Implement Task 6 ("DoT and DoH upstreams, and cross-worker request coalescing") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 6 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] Rust tests pass in the dev pod: `doh_posts_dns_message_over_http2`, `dot_pipelines_fifty_concurrent_queries_over_one_connection`, `dot_rejects_untrusted_certificate`, `dropped_leader_wakes_followers_with_servfail_and_frees_key`, `follower_joining_after_value_sent_still_gets_it`, `thousand_waiters_on_many_threads_all_get_the_answer`
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Failing first: `scripts/dev-exec.sh 'cargo test -p nexora-engine --test inflight --test upstream_encrypted'` -> E0432 ``unresolved import `nexora_engine::inflight` `` and ``unresolved imports `nexora_engine::upstream::doh`, `nexora_engine::upstream::dot` ``.
- Plan deviations (plan text updated): tests ported to hickory-proto 0.26.3; test `self_signed()` installs ring as the default rustls provider (aws-lc-rs is also compiled in via reqwest); `InFlight` wraps `Arc<Inner>`; DoH uses `tls_backend_preconfigured(client_tls_config(ca_pem) + ALPN h2)` because reqwest 0.13.5 lacks `tls_built_in_root_certs`; DoH body capped at 65,535 bytes; DoT connection slot is an async mutex held across connect.
- `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test inflight --test upstream_encrypted --test upstream_udp_tcp'` -> `3 passed`, `3 passed`, `6 passed`.
- Mutation check: removing the map removal in `LeaderGuard::finish` made `dropped_leader_wakes_followers_with_servfail_and_frees_key` and `thousand_waiters_on_many_threads_all_get_the_answer` FAIL; reverted.
- `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --all-targets'` -> lib 21 passed (1 ignored), cache_alloc 1, inflight 3, proto_roundtrip 1, upstream_encrypted 3, upstream_udp_tcp 6 passed.
- `cargo fmt --all --check` clean; `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> Finished, no warnings.
