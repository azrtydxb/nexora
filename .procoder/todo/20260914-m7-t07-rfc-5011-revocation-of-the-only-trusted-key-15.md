# M7 T7: RFC 5011 revocation of the only trusted key (#15)

Status: open
Created: 2026-09-14

## Description

Implements Task 7 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Add to `anchors_tests.rs`:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib refresh_accepts_revocation_of_the_only_trusted_key'`. Expect FAIL `left: Some(Valid), right: Some(Revoked)`: the RRset is refused as "not validated by a trusted key".
- [x] In `verify.rs`, add `revoked_key_verifies`, which returns `key.revoke().then(|| verify_inner(rrset, rrsigs, std::slice::from_ref(key), zone, now_unix, true).ok()).flatten()`. Make `revoked_key_signs` return `revoked_key_verifies(..).is_some()`.
- [x] In `refresh_zone`, replace the `let Ok(v) = verify_rrset(...) else { return fail(...) };` statement with a match whose `Err` arm handles a self-revocation by a trusted key:
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib recursor::dnssec'` and expect all to pass, including `refresh_authenticates_rollover_and_self_signed_revocation`.
- [x] Lost trust point alert (spec [S-9] edge case, lead decision 2026-09-14). Extend the test above with `assert_eq!(s.lost_trust_points(), vec![root.clone()]);`. Expect FAIL (no such method). Then:
- [ ] Committed (lead).

## Evidence

All commands run with `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh '<cmd>'` (pod toolbox-m7).

- Red: `cargo test --locked -p nexora-engine --lib refresh_accepts_revocation_of_the_only_trusted_key` -> FAILED at anchors_tests.rs:263 `left: Some(Valid) right: Some(Revoked)`, last_error "DNSKEY RRset not validated by a trusted key".
- Red (alert): same test with `s.lost_trust_points()` -> `error[E0599]: no method named lost_trust_points`.
- Red (metric): `cargo test --locked -p nexora-engine --lib lost_trust_point_raises_its_gauge` -> FAILED (no `nexora_dnssec_trust_point_lost` in the rendered text).
- Green: `cargo test --locked -p nexora-engine --lib recursor::dnssec` -> `test result: ok. 29 passed; 0 failed`.
- Green: `cargo test --locked -p nexora-engine --lib anchors_tests` -> `test result: ok. 8 passed; 0 failed` (includes `refresh_accepts_revocation_of_the_only_trusted_key`, `refresh_authenticates_rollover_and_self_signed_revocation`).
- Green: `cargo test --locked -p nexora-engine --lib telemetry::metrics` -> `test result: ok. 3 passed; 0 failed`.
- `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> no findings; `rustfmt --edition 2024` applied on the laptop.
- Web: `pnpm typecheck` clean; `pnpm lint` -> `permission parity: 120 operations match`; prettier applied on the laptop.
- No proto/OpenAPI change: the GUI derives the lost condition from the existing `DnssecStats.trust_anchors` states (plan text updated). No Playwright spec covers the alert (the e2e stack cannot revoke a root key; `16-dnssec.spec.ts` is not this task's file).
