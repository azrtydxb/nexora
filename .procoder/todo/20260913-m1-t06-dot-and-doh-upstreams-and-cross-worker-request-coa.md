# M1 Task 6: DoT and DoH upstreams, and cross-worker request coalescing

Status: open
Created: 2026-09-13

## Description

Implement Task 6 ("DoT and DoH upstreams, and cross-worker request coalescing") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 6 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [ ] Rust tests pass in the dev pod: `doh_posts_dns_message_over_http2`, `dot_pipelines_fifty_concurrent_queries_over_one_connection`, `dot_rejects_untrusted_certificate`, `dropped_leader_wakes_followers_with_servfail_and_frees_key`, `follower_joining_after_value_sent_still_gets_it`, `thousand_waiters_on_many_threads_all_get_the_answer`
- [ ] procoder gate clean over the changed files; work committed

## Evidence

