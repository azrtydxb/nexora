# M1 Task 8: Per-core UDP/TCP listeners, query pipeline, counters, query-log ring and standalone engine binary

Status: open
Created: 2026-09-13

## Description

Implement Task 8 ("Per-core UDP/TCP listeners, query pipeline, counters, query-log ring and standalone engine binary") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 8 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [ ] Rust tests pass in the dev pod: `acl_refuses_and_blocklist_blocks`, `cache_hit_path_does_not_allocate`, `large_answer_truncates_over_udp_and_completes_over_tcp`, `malformed_and_notimp`, `miss_then_hit_with_decremented_ttl_and_one_upstream_query`
- [ ] procoder gate clean over the changed files; work committed

## Evidence

