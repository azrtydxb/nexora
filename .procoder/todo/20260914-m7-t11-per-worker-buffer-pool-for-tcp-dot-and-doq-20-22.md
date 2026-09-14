# M7 T11: Per-worker buffer pool for TCP, DoT and DoQ (#20, #22)

Status: open
Created: 2026-09-14

## Description

Implements Task 11 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] In `hot_path_alloc.rs`, add a big-allocation counter next to `ALLOCS`: `static BIG: Cell<usize>`, incremented by the armed allocator when `layout.size() >= 60_000` in `alloc` and `realloc`.
- [ ] Add a helper `stream_setup() -> (Arc<Shared>, Vec<u8> /* query */)`. It applies a snapshot with `acl_allow_cidrs: ["127.0.0.0/8"]`, an 8 MiB cache and no filter, and inserts a cached answer for `hot.example. A` for client `127.0.0.1` exactly as `measure` does (`CacheKey::in_partition(&v, policy.cach
- [ ] Add the stream test:
- [ ] Add `doq_answers_reuse_pooled_buffers` the same way:
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test hot_path_alloc -- stream_answers_reuse_pooled_buffers doq_answers_reuse_pooled_buffers'`. Expect FAIL on both assertions, with `left` about 2,000 (answer buffer and frame) and about 200.
- [ ] Create `engine/src/server/buffers.rs`:
- [ ] In `WorkerAnswerer::answer_frames`, replace `let mut out = vec![0u8; 65535];` with `let mut out = buffers::take(); out.resize(65535, 0);`. `answer` already resizes the `out` it is given.
- [ ] In `stream.rs`:
- [ ] In `doq.rs`:
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test hot_path_alloc && cargo test --locked -p nexora-engine --lib server:: && cargo test --locked -p nexora-engine --test upstream_encrypted && cargo test --locked -p nexora-engine --test authoritative_pipeline'` and expect all to pass

## Evidence

