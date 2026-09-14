# M7 T6: Renewal fallback and unstorable identities (#13, #14)

Status: open
Created: 2026-09-14

## Description

Implements Task 6 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Re-read `engine/src/control.rs` at the M6 head (M6 #65 adds engine log streaming to `session`) and keep M6's additions.
- [ ] Add to `engine/tests/control_renewal.rs` a second fake and a test (reuse `Ca`, `p256`, `set_validity`, `serial_of`, `next`, `Event`):
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test control_renewal staged_certificate_failing'`. Expect FAIL at the second `assert_eq!`: connection 1 presents the staged serial again.
- [ ] Create `engine/tests/control_enroll.rs`. It uses the `Ca` helpers copied from `control_renewal.rs` (a test file cannot import another). An `EnrollingMgmt` counts `enroll` calls and answers each with engine id `11111111-2222-3333-4444-555555555555`, `ca.sign_csr(&csr_der)` and `ca.cert.der()`. Its `c
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test control_enroll'`. Expect FAIL with `left: 3, right: 1` (enrolls at 0 s, about 0.5 s and about 1.5 s).
- [ ] In `obtain_identity`, hold `let mut unsaved: Option<Identity> = None;` outside the loop. At the top of each iteration, after `recover_identity`/`load_identity`:
- [ ] In `run`, add `let mut fallback_from: Option<Identity> = None;` before the loop.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test control_renewal && cargo test --locked -p nexora-engine --test control_enroll && cargo test --locked -p nexora-engine --test control_unit'`. Expect all to pass, `renews_rotates_and_backs_off_when_revoked` included.

## Evidence

