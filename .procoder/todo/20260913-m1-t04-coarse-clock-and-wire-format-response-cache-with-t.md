# M1 Task 4: Coarse clock and wire-format response cache with the allocation guard

Status: open
Created: 2026-09-13

## Description

Implement Task 4 ("Coarse clock and wire-format response cache with the allocation guard") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 4 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [ ] Rust tests pass in the dev pod: `cache_lookup_and_serve_do_not_allocate`, `expired_entry_is_stale_within_window_then_miss`, `keys_differ_by_do_and_cd_bits`, `negative_answer_uses_soa_minimum_and_no_soa_is_not_cached`, `oversize_for_limit_sets_tc_with_empty_answer`, `serves_with_patched_id_client_casing_and_decremented_ttl`, `ttl_zero_servfail_and_tc_are_not_cached`
- [ ] procoder gate clean over the changed files; work committed

## Evidence

