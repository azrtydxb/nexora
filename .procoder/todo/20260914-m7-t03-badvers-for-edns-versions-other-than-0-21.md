# M7 T3: BADVERS for EDNS versions other than 0 (#21)

Status: open
Created: 2026-09-14

## Description

Implements Task 3 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Add to `engine/tests/server_pipeline.rs` (after `malformed_and_notimp`):
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test server_pipeline edns_version_above_zero_gets_badvers'`. Expect FAIL at `assert_eq!(u16::from(..), 16)` with left `0`: today the query is forwarded and answered with version 0.
- [ ] In `engine/src/server/mod.rs`, split `Scope::finish` so the rcode can be given:
- [ ] In `handle_packet`, directly after `let mut opt = q.opt.map(|o| ReplyOpt { .. });` and before the `bad_cookie_len` check, replace the `// debt: EDNS versions other than 0 ...` comment with:
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test server_pipeline && cargo test --locked -p nexora-engine --test hot_path_alloc && cargo test --locked -p nexora-engine --lib edns'` and expect all to pass.
- [ ] Run `scripts/dev-exec.sh 'cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'` and expect no warnings. Run `rg -n "EDNS versions other than 0" engine/src` and expect no match.

## Evidence

