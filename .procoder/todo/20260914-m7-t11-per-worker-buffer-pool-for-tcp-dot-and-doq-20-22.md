# M7 T11: Per-worker buffer pool for TCP, DoT and DoQ (#20, #22)

Status: open
Created: 2026-09-14

## Description

Implements Task 11 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] In `hot_path_alloc.rs`, add a big-allocation counter next to `ALLOCS`: `static BIG: Cell<usize>`, incremented by the armed allocator when `layout.size() >= 60_000` in `alloc` and `realloc`.
- [x] Add a helper `stream_setup() -> (Arc<Shared>, Vec<u8> /* query */)`. It applies a snapshot with `acl_allow_cidrs: ["127.0.0.0/8"]`, an 8 MiB cache and no filter, and inserts a cached answer for `hot.example. A` for client `127.0.0.1` exactly as `measure` does (`CacheKey::in_partition(&v, policy.cach
- [x] Add the stream test:
- [x] Add `doq_answers_reuse_pooled_buffers` the same way:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test hot_path_alloc -- stream_answers_reuse_pooled_buffers doq_answers_reuse_pooled_buffers'`. Expect FAIL on both assertions, with `left` about 2,000 (answer buffer and frame) and about 200.
- [x] Create `engine/src/server/buffers.rs`:
- [x] In `WorkerAnswerer::answer_frames`, replace `let mut out = vec![0u8; 65535];` with `let mut out = buffers::take(); out.resize(65535, 0);`. `answer` already resizes the `out` it is given.
- [x] In `stream.rs`:
- [x] In `doq.rs`:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test hot_path_alloc && cargo test --locked -p nexora-engine --lib server:: && cargo test --locked -p nexora-engine --test upstream_encrypted && cargo test --locked -p nexora-engine --test authoritative_pipeline'` and expect all to pass

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'cd engine && cargo test --locked -p nexora-engine --test hot_path_alloc -- stream_answers_reuse_pooled_buffers doq_answers_reuse_pooled_buffers'` -> both FAILED: "a stream query allocated a 64 KiB buffer" left: 1000, right: 0; "a DoQ query allocated a 64 KiB buffer" left: 200, right: 0.
- Green: `cargo test --locked -p nexora-engine --test hot_path_alloc` -> `test result: ok. 4 passed; 0 failed` (both new tests plus `cache_hit_path_does_not_allocate`, `authoritative_answer_path_does_not_allocate`).
- `cargo test --locked -p nexora-engine --lib server::` -> `test result: ok. 19 passed; 0 failed; 237 filtered out` (includes `pipelined_queries_all_answered_on_one_stream`, `one_stream_per_query_and_nonzero_id_closes_connection`, new `oversized_stream_closes_connection_with_protocol_error` and `server::buffers::tests::pool_reuses_bounded_and_rejects_grown_buffers`).
- `cargo test --locked -p nexora-engine --test upstream_encrypted` -> `test result: ok. 3 passed; 0 failed`.
- `cargo test --locked -p nexora-engine --test authoritative_pipeline` -> `test result: ok. 2 passed; 0 failed` (zone transfers over TCP).
- `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> clean (all commands with `NEXORA_DEV_DEPLOY=toolbox-m7`, in `engine/`).
- `rustfmt --edition 2024 --check` on the five changed Rust files -> clean. No `debt:` left in `stream.rs`/`doq.rs`.
- Deviations (plan text updated): round trips are helper async fns; DoQ reads with `read_chunk`; `answer_frames` returns the buffer on a slow (transfer) outcome; pre-fix stream count was 1,000, not ~2,000; added pool unit test and DoQ oversized-stream test.
- Not yet committed (lead commits).
