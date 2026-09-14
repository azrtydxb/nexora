# M7 T4: ACL as merged ranges with binary search (#11)

Status: open
Created: 2026-09-14

## Description

Implements Task 4 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Re-read `engine/src/acl.rs` at the M6 head. If M6 #63 split the ACL into resolver and authoritative lists, apply this task to the type that holds CIDRs and keep M6's API.
- [ ] Add the counters with the current representation, `pub fn v4_ranges(&self) -> usize { self.v4.len() }` and the same for v6, then add this test module at the end of `acl.rs`:
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib acl::tests'`. Expect FAIL in `overlapping_cidrs_merge_into_ranges` with `left: 4, right: 2` (the linear representation keeps every CIDR).
- [ ] Replace the struct and `allows` with ranges and remove the `debt:` doc lines:
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib acl::tests && cargo test --locked -p nexora-engine --test snapshot_apply && cargo test --locked -p nexora-engine --test hot_path_alloc'` and expect every test to pass.

## Evidence

