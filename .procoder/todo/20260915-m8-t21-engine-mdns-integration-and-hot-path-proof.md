# M8 T21: Engine mDNS integration and hot-path proof

Status: open
Created: 2026-09-15

## Description

Implements Task 21 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create `engine/tests/mdns_pipeline.rs`:
- [ ] Add to `engine/src/mdns/mod.rs`:
- [ ] In `engine/tests/hot_path_alloc.rs`, set in the snapshot `cache_hit_path_does_not_allocate` and
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test mdns_pipeline; cargo test --locked -p nexora-engine --lib mdns::tests'`
- [ ] Implement:
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test mdns_pipeline && cargo test --locked -p nexora-engine --lib mdns:: && cargo test --locked -p nexora-engine --test hot_path_alloc && cargo test --locked -p nexora-engine --test server_pipeline && cargo test --locked -p nexora-engine --test snapshot_apply && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`
- [ ] Report the paths. The lead commits `M8 T21: mDNS route, reflector lifecycle and hot-path proof`.

## Evidence
