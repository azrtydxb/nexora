# M7 T3: BADVERS for EDNS versions other than 0 (#21)

Status: open
Created: 2026-09-14

## Description

Implements Task 3 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Add to `engine/tests/server_pipeline.rs` (after `malformed_and_notimp`):
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test server_pipeline edns_version_above_zero_gets_badvers'`. Expect FAIL at `assert_eq!(u16::from(..), 16)` with left `0`: today the query is forwarded and answered with version 0.
- [x] In `engine/src/server/mod.rs`, split `Scope::finish` so the rcode can be given:
- [x] In `handle_packet`, directly after `let mut opt = q.opt.map(|o| ReplyOpt { .. });` and before the `bad_cookie_len` check, replace the `// debt: EDNS versions other than 0 ...` comment with:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test server_pipeline && cargo test --locked -p nexora-engine --test hot_path_alloc && cargo test --locked -p nexora-engine --lib edns'` and expect all to pass.
- [x] Run `scripts/dev-exec.sh 'cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'` and expect no warnings. Run `rg -n "EDNS versions other than 0" engine/src` and expect no match.

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test server_pipeline edns_version_above_zero_gets_badvers'` -> FAILED at `engine/tests/server_pipeline.rs:259` "version 1: BADVERS", left: 0, right: 16 (query forwarded, answered as version 0).
- Green: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test server_pipeline'` -> `test result: ok. 5 passed; 0 failed` (includes `edns_version_above_zero_gets_badvers`).
- `cargo test --locked -p nexora-engine --test hot_path_alloc` -> `test result: ok. 2 passed; 0 failed` (`cache_hit_path_does_not_allocate`, `authoritative_answer_path_does_not_allocate`).
- `cargo test --locked -p nexora-engine --lib edns` -> `test result: ok. 5 passed; 0 failed; 241 filtered out`.
- `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> `Finished`, no warnings (a first run failed only on Task 6's in-progress `engine/src/control.rs` too_many_arguments; re-run clean).
- `rg -n "EDNS versions other than 0" engine/src` -> no match.
- `rustfmt --edition 2024 --check engine/src/server/mod.rs engine/tests/server_pipeline.rs` -> clean.
- Deviation: `ext_rcode: RCODE_BADVERS >> 4` without the plan's `as u8` cast (the constant is already u8; clippy::unnecessary_cast). Plan text updated.
- Not yet committed (lead commits).

