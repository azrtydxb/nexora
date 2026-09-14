# M5 Task 9: Engine side of renewal, rotation, revocation and fleet health

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 9 ("Engine side of renewal, rotation, revocation and fleet health") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 9 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [x] procoder gate clean over the changed files; work committed (gate clean 2026-09-14; commit left to the lead)

## Evidence

- 2026-09-14 red: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib cert_renewal'` -> ``error[E0425]: cannot find function `renewal_due` in this scope``; `cargo test --locked -p nexora-engine --lib -- revoked_or_unknown_engine_backs_off_five_minutes node_name_env_override_is_validated` -> ``cannot find function `apply_env_overrides` `` / ``cannot find function `reconnect_delay` ``.
- green: `cargo test --locked -p nexora-engine --lib -- revoked_or_unknown_engine_backs_off_five_minutes node_name_env_override_is_validated cert_renewal` -> `test result: ok. 8 passed`.
- fake control server: `cargo test --locked -p nexora-engine --test control_renewal` -> `test result: ok. 1 passed` (renewal, rotation, refused staged identity fallback, revoked backoff). Mutation check: disabling the staged-refusal fallback -> FAIL (no event within 15 s); disabling promotion -> FAIL (serial mismatch).
- full: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --all-targets'` -> every suite `test result: ok` (lib 203 passed, 1 ignored); `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> no findings; `cargo fmt --all -- --check` -> clean.
- `procoder check` over the 7 changed engine files -> `7 clean, 0 unformatted, 0 unchecked` (0 blocking).
- Deviations recorded in the plan (Task 9 "As built" note): staged identity confirmed before promotion, `subject_public_key_info` instead of `public_key_der`, webpki verification of the issued certificate.

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in c302c1f
