# M1 Task 5: Upstream UDP/TCP transports with anti-spoofing and health scoring

Status: closed 2026-09-13
Created: 2026-09-13

## Description

Implement Task 5 ("Upstream UDP/TCP transports with anti-spoofing and health scoring") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 5 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] Rust tests pass in the dev pod: `health_marks_down_after_three_failures_and_admits_one_probe`, `ids_random_ports_spread_and_sockets_rotate`, `reply_from_other_source_port_never_accepted`, `spoofed_wrong_id_and_wrong_question_are_dropped_and_counted`, `strategies_order_candidates`, `tcp_exchange_validates_question`
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Failing first: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test upstream_udp_tcp'` -> E0432 ``unresolved import `nexora_engine::upstream` `` and E0433 ``cannot find `upstream` in `nexora_engine` ``.
- First implementation with independent `rand::rng().random::<u16>()` IDs failed `ids_random_ports_spread_and_sockets_rotate` with `ids repeat too often` (birthday bound: ~88.4% distinct of 16,448 draws < 90%). Plan updated: IDs come from a CSPRNG-keyed 16-bit Feistel permutation per pool (`IdSource`), re-keyed every 65,536 IDs.
- Other plan deviations (plan text updated): tests ported to hickory-proto 0.26.3 API; `>= POOL_SIZE + 1` written `> POOL_SIZE` (clippy); `Waiters` shared helper (for DoT reuse); reader of a retired socket woken via `Notify`; waiter guard removes entries on timeout/cancel; forward attempt guard records failure for a dropped admitted attempt; transports rebuilt when an id's spec changes; `record_failure` re-arms at >= 3.
- `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test upstream_udp_tcp'` -> `test result: ok. 6 passed; 0 failed`.
- Mutation check: replacing the question/ID/QR check in `Waiters::deliver` with `true` made `spoofed_wrong_id_and_wrong_question_are_dropped_and_counted` FAIL; reverted.
- `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --all-targets'` -> lib 21 passed (1 ignored), cache_alloc 1, proto_roundtrip 1, upstream_udp_tcp 6 passed.
- `cargo fmt --all` clean; `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> Finished, no warnings.
