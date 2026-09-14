# M7 T4: ACL as merged ranges with binary search (#11)

Status: open (verified, awaiting commit)
Created: 2026-09-14

## Description

Implements Task 4 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Re-read `engine/src/acl.rs` at the M6 head. If M6 #63 split the ACL into resolver and authoritative lists, apply this task to the type that holds CIDRs and keep M6's API.
- [x] Add the counters with the current representation, `pub fn v4_ranges(&self) -> usize { self.v4.len() }` and the same for v6, then add this test module at the end of `acl.rs`:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib acl::tests'`. Expect FAIL in `overlapping_cidrs_merge_into_ranges` with `left: 4, right: 2` (the linear representation keeps every CIDR).
- [x] Replace the struct and `allows` with ranges and remove the `debt:` doc lines:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib acl::tests && cargo test --locked -p nexora-engine --test snapshot_apply && cargo test --locked -p nexora-engine --test hot_path_alloc'` and expect every test to pass.

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib acl::tests'` with counters on the linear representation -> `overlapping_cidrs_merge_into_ranges` FAILED `left: 4, right: 2`; `range_search_matches_linear_scan` FAILED "overlapping random v4 CIDRs merge"; `test result: FAILED. 0 passed; 2 failed`.
- Green: same command after the merged-range implementation -> `test result: ok. 2 passed; 0 failed`.
- `cargo test --locked -p nexora-engine --test snapshot_apply` -> `test result: ok. 12 passed; 0 failed`.
- `cargo test --locked -p nexora-engine --test hot_path_alloc` -> `test result: ok. 2 passed; 0 failed`.
- `cargo fmt -p nexora-engine --check`: no diff in `engine/src/acl.rs`.
- `cargo clippy --locked -p nexora-engine --all-targets`: no findings in `acl.rs`. The `-D warnings` run cannot compile the crate at the moment because of Task 8's in-progress `engine/src/recursor/rpz/transfer.rs` (unused `deleted_owners`); re-run at the lead's commit.
- The `debt:` marker on `Acl::allows` is removed.
