# M3 Task 2: Outbound transport with spoofing defences and recursor metrics

Status: open
Created: 2026-09-13

## Description

Implement Task 2 ("Outbound transport with spoofing defences and recursor metrics") of milestone M3 exactly as specified in
`.procoder/plans/nexora-v1-m3.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 2 in `.procoder/plans/nexora-v1-m3.md` is done as written (deviations recorded in the plan first)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- RED: `cargo test --locked -p nexora-engine --lib recursor::transport::tests` -> "cannot find type `OutboundQuery`/`Transport`/`RecursorMetrics`".
- GREEN: same command -> `test result: ok. 4 passed` (spoofed_replies_are_dropped_and_real_reply_accepted, truncated_udp_reply_retries_over_tcp, query_carries_random_id_edns_1232_do_and_rd_clear, reply_matching_rules).
- Plan text for the TCP retry updated to match (TCP query counted in upstream_queries; malformed/I-O/deadline error mapping).
- clippy -D warnings, fmt, full engine test suite and M1/M2 e2e: see M3 Task 1 evidence (same run).
- Not committed (lead commits serially).
