# M7 T7: RFC 5011 revocation of the only trusted key (#15)

Status: open
Created: 2026-09-14

## Description

Implements Task 7 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Add to `anchors_tests.rs`:
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib refresh_accepts_revocation_of_the_only_trusted_key'`. Expect FAIL `left: Some(Valid), right: Some(Revoked)`: the RRset is refused as "not validated by a trusted key".
- [ ] In `verify.rs`, add `revoked_key_verifies`, which returns `key.revoke().then(|| verify_inner(rrset, rrsigs, std::slice::from_ref(key), zone, now_unix, true).ok()).flatten()`. Make `revoked_key_signs` return `revoked_key_verifies(..).is_some()`.
- [ ] In `refresh_zone`, replace the `let Ok(v) = verify_rrset(...) else { return fail(...) };` statement with a match whose `Err` arm handles a self-revocation by a trusted key:
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib recursor::dnssec'` and expect all to pass, including `refresh_authenticates_rollover_and_self_signed_revocation`.
- [ ] Lost trust point alert (spec [S-9] edge case, lead decision 2026-09-14). Extend the test above with `assert_eq!(s.lost_trust_points(), vec![root.clone()]);`. Expect FAIL (no such method). Then:

## Evidence

