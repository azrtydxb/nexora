# M7 T10: Recursor memory budget, atomic infra updates, uncapped NSEC cache (#16, #17, #18)

Status: open
Created: 2026-09-14

## Description

Implements Task 10 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Add accessors on the current `RrCache` (`pub fn weight(&self) -> u64 { self.entries.weight() }`, `pub fn len(&self) -> usize { self.entries.len() }`, `pub fn set_capacity(&self, c: u64) { self.entries.set_capacity(c) }`) and the test module at the end of `rrcache.rs`:
- [ ] Add at the end of `infra.rs`:
- [ ] Add at the end of `nsec_cache.rs`:
- [ ] Add to the existing tests in `snapshot_m3.rs`, starting from the snapshot the module's tests already build (the one with `recursion: Some(RecursionConfig { .. })`):
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib -- rrset_cache_is_bounded_by_bytes concurrent_updates_are_not_lost aggressive_nsec_caches cache_max_bytes_outside_range'`. Expect four FAILs:
- [ ] Create `engine/src/recursor/memory.rs`:
- [ ] `rrcache.rs`:
- [ ] `infra.rs`:
- [ ] `nsec_cache.rs`:
- [ ] `iterate.rs`: remove `INFRA_CAPACITY`, `RRSET_CAPACITY` and their `debt:` comment; `Recursor::new` builds `InfraCache::new(memory::shares(0).infra)` and `RrCache::new(memory::shares(0).rrset)`. Add `pub cache_max_bytes: u64` to `RecursionParams`, set from `c.cache_max_bytes` (0 when `c` is `None`).
- [ ] `RecursorState::sync(&runtime)` in `recursor/mod.rs`: read the runtime's `RecursionParams::cache_max_bytes` (the params `recursor/dispatch.rs` builds from the snapshot), then call `set_capacity` with `memory::shares(..)` on `recursor.rrcache`, `recursor.infra` and the validator's aggressive NSEC cac
- [ ] `snapshot_m3.rs`: in the `recursion` block, add
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib recursor:: && cargo test --locked -p nexora-engine --lib snapshot_m3 && cargo test --locked -p nexora-engine --test hot_path_alloc && cargo test --locked -p nexora-engine --test rpz_pipeline'` and expect all to pass. Run `scripts/d

## Evidence

