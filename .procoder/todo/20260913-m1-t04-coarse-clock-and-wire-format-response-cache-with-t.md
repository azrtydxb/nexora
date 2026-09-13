# M1 Task 4: Coarse clock and wire-format response cache with the allocation guard

Status: closed 2026-09-13
Created: 2026-09-13

## Description

Implement Task 4 ("Coarse clock and wire-format response cache with the allocation guard") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 4 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] Rust tests pass in the dev pod: `cache_lookup_and_serve_do_not_allocate`, `expired_entry_is_stale_within_window_then_miss`, `keys_differ_by_do_and_cd_bits`, `negative_answer_uses_soa_minimum_and_no_soa_is_not_cached`, `oversize_for_limit_sets_tc_with_empty_answer`, `serves_with_patched_id_client_casing_and_decremented_ttl`, `ttl_zero_servfail_and_tc_are_not_cached`
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Failing first: `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib cache::` and `--test cache_alloc` -> E0433 ``cannot find type `Cache` / `CacheKey` / `Lookup` `` and E0425 `write_cached` (module files existed, so not "unresolved import").
- Plan deviations (plan text updated): `start_ticker()` returns `()` (an idempotent call cannot return the one JoinHandle twice; the only consumer ignores it); `CacheKey` derives Debug (`assert_ne!`); an OPT that is not the last RR makes the response `NotCacheable("opt not last")` so TTL offsets and compression pointers stay valid; `write_cached` returns 0 when even the TC reply does not fit; tests ported to hickory-proto 0.26.3's API.
- `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib cache::` -> `test result: ok. 6 passed; 0 failed`.
- `scripts/dev-exec.sh cargo test --locked -p nexora-engine --test cache_alloc` -> `test cache_lookup_and_serve_do_not_allocate ... ok`, `1 passed`.
- Mutation check: a `black_box(vec![0u8; 1])` added to `write_cached` made `cache_alloc` fail with `cache hit path allocated`; reverted.
- `scripts/dev-exec.sh cargo test --locked -p nexora-engine --all-targets` -> lib `21 passed; 1 ignored`, main 0, cache_alloc 1 passed, proto_roundtrip 1 passed.
- `cargo fmt --all -- --check` clean; `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> Finished, no warnings.
- procoder gate clean at commit; committed db32b07.
