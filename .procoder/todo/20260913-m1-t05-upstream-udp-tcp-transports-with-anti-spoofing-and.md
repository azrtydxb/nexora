# M1 Task 5: Upstream UDP/TCP transports with anti-spoofing and health scoring

Status: open
Created: 2026-09-13

## Description

Implement Task 5 ("Upstream UDP/TCP transports with anti-spoofing and health scoring") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 5 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [ ] Rust tests pass in the dev pod: `health_marks_down_after_three_failures_and_admits_one_probe`, `ids_random_ports_spread_and_sockets_rotate`, `reply_from_other_source_port_never_accepted`, `spoofed_wrong_id_and_wrong_question_are_dropped_and_counted`, `strategies_order_candidates`, `tcp_exchange_validates_question`
- [ ] procoder gate clean over the changed files; work committed

## Evidence

