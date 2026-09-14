# M7 T6: Renewal fallback and unstorable identities (#13, #14)

Status: open
Created: 2026-09-14

## Description

Implements Task 6 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Re-read `engine/src/control.rs` at the M6 head (M6 #65 adds engine log streaming to `session`) and keep M6's additions.
- [x] Add to `engine/tests/control_renewal.rs` a second fake and a test (reuse `Ca`, `p256`, `set_validity`, `serial_of`, `next`, `Event`):
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test control_renewal staged_certificate_failing'`. Expect FAIL at the second `assert_eq!`: connection 1 presents the staged serial again.
- [x] Create `engine/tests/control_enroll.rs`. It uses the `Ca` helpers copied from `control_renewal.rs` (a test file cannot import another). An `EnrollingMgmt` counts `enroll` calls and answers each with engine id `11111111-2222-3333-4444-555555555555`, `ca.sign_csr(&csr_der)` and `ca.cert.der()`. Its `c
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test control_enroll'`. Expect FAIL with `left: 3, right: 1` (enrolls at 0 s, about 0.5 s and about 1.5 s).
- [x] In `obtain_identity`, hold `let mut unsaved: Option<Identity> = None;` outside the loop. At the top of each iteration, after `recover_identity`/`load_identity`:
- [x] In `run`, add `let mut fallback_from: Option<Identity> = None;` before the loop.
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test control_renewal && cargo test --locked -p nexora-engine --test control_enroll && cargo test --locked -p nexora-engine --test control_unit'`. Expect all to pass, `renews_rotates_and_backs_off_when_revoked` included.

## Evidence

All commands run with `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh '...'` (pod toolbox-m7).

- Re-read `engine/src/control.rs` at the m7 head: M6 #65 log streaming is not in this branch yet (the `LogRequest` `debt:` arm is unchanged); nothing of M6 removed.
- Red, `cargo test --locked -p nexora-engine --test control_renewal staged_certificate_failing`: FAILED at the second `assert_eq!` ("after a non-authentication failure the current identity is tried"; connection 1 presented the staged serial `56b348ec…` instead of the current `7348856…`).
- Red, `cargo test --locked -p nexora-engine --test control_enroll`: FAILED `left: 3, right: 1` ("an unstorable identity is kept, not enrolled again").
- Green, `cargo test --locked -p nexora-engine --test control_renewal`: `test result: ok. 2 passed; 0 failed` (includes `renews_rotates_and_backs_off_when_revoked`).
- Green, `cargo test --locked -p nexora-engine --test control_enroll`: `test result: ok. 1 passed; 0 failed`.
- Green, `cargo test --locked -p nexora-engine --test control_unit`: `test result: ok. 3 passed; 0 failed`; `--lib control::`: `1 passed`.
- `rustfmt --edition 2024 --check` on the three files: clean. `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings`: Finished, no warnings (after `#[allow(clippy::too_many_arguments)]` on `session`, as elsewhere in the engine).
- Both `debt:` markers (#13 renewal branch, #14 unstorable identity) removed.
- Deviation (plan updated): the `Renewed` branch resets `fallback_from = None`, since it `continue`s before the fallback update; the `!matches!(err, Renewed)` guard is therefore dropped as unreachable.
- Not committed (lead commits).

