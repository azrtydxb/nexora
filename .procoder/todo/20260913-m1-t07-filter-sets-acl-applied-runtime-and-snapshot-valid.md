# M1 Task 7: Filter sets, ACL, applied runtime and snapshot validation/persistence

Status: open
Created: 2026-09-13

## Description

Implement Task 7 ("Filter sets, ACL, applied runtime and snapshot validation/persistence") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 7 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [ ] Rust tests pass in the dev pod: `acl_matches_v4_v6_and_mapped`, `filter_subdomains_allowlist_invalid_lines_and_cloaking`, `in_flight_holders_keep_old_runtime_and_cache_survives_unchanged_settings`, `invalid_snapshots_are_rejected_and_previous_runtime_kept`, `persist_failure_still_applies_and_reports`, `valid_snapshot_applies_persists_and_reloads`
- [ ] procoder gate clean over the changed files; work committed

## Evidence

