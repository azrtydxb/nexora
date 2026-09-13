# M1 Task 7: Filter sets, ACL, applied runtime and snapshot validation/persistence

Status: closed 2026-09-13
Created: 2026-09-13

## Description

Implement Task 7 ("Filter sets, ACL, applied runtime and snapshot validation/persistence") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 7 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] Rust tests pass in the dev pod: `acl_matches_v4_v6_and_mapped`, `filter_subdomains_allowlist_invalid_lines_and_cloaking`, `in_flight_holders_keep_old_runtime_and_cache_survives_unchanged_settings`, `invalid_snapshots_are_rejected_and_previous_runtime_kept`, `persist_failure_still_applies_and_reports`, `valid_snapshot_applies_persists_and_reloads`
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Failing first: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test snapshot_apply'` -> E0432 ``unresolved import `nexora_engine::acl` `` / `filter` / `runtime` / `snapshot`.
- Plan deviations (plan text updated): `filter::BlockMode` discriminants equal the proto values; reasons prefixed `upstream {id}`; `DirBlobs::read` also refuses non-hex names (path safety); `decide` keeps walking past a blocked suffix for a shorter allowlisted one; proto `BLOCK_MODE_UNSPECIFIED` maps to `NullIp`; `filter_hashes` are `b:`/`a:`-prefixed.
- `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test snapshot_apply'` -> `test result: ok. 6 passed; 0 failed`.
- Mutation check: commenting out `verify_blob` in `DirBlobs::read` made `invalid_snapshots_are_rejected_and_previous_runtime_kept` FAIL (`5 passed; 1 failed`); reverted.
- `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> Finished, no warnings; `cargo fmt --all -- --check` clean; `procoder check <changed files>` -> `7 clean, 0 unformatted, 0 unchecked` (0 blocking).
