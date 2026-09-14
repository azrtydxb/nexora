# M1 Task 8: Per-core UDP/TCP listeners, query pipeline, counters, query-log ring and standalone engine binary

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 8 ("Per-core UDP/TCP listeners, query pipeline, counters, query-log ring and standalone engine binary") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 8 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] Rust tests pass in the dev pod: `acl_refuses_and_blocklist_blocks`, `cache_hit_path_does_not_allocate`, `large_answer_truncates_over_udp_and_completes_over_tcp`, `malformed_and_notimp`, `miss_then_hit_with_decremented_ttl_and_one_upstream_query`
- [x] procoder gate clean over the changed files; work committed

## Evidence

Closing evidence (lead, 2026-09-14):

- plan steps: done; deviations recorded as As-built notes in the plan; deferred integration steps completed in later commits (git log)
- acl_refuses_and_blocklist_blocks, cache_hit_path_does_not_allocate, large_answer_truncates_over_udp_and_completes_over_tcp, malformed_and_notimp, miss_then_hit_with_decremented_ttl_and_one_upstream_query: passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- gate/commit: commit gate passed on every commit for this task; todo last committed in 18b1908
