# M1 Task 3: Zero-copy query parser, EDNS/cookies, and the fuzz workflow

Status: open
Created: 2026-09-13

## Description

Implement Task 3 ("Zero-copy query parser, EDNS/cookies, and the fuzz workflow") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 3 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [ ] Rust tests pass in the dev pod: `compression_pointer_in_question_is_formerr`, `counts_exceeding_packet_are_formerr`, `edns_opt_with_cookie_is_parsed`, `escaped_dot_and_binary_label_round_trip_in_key`, `formerr_reply_echoes_id`, `label_over_63_and_name_over_255_are_formerr`, `parses_simple_query_and_lowercases_key`, `reply_limit_rules`, `response_bit_is_rejected`, `server_cookie_is_deterministic_per_client_and_changes_with_ip`, `short_packet_is_too_short`, `unknown_opcode_is_notimp`
- [ ] procoder gate clean over the changed files; work committed

## Evidence

