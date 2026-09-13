# M2 Task 3: Engine certificate store and TlsMaterial over the control stream

Status: open
Created: 2026-09-13

## Description

Implement Task 3 ("Engine certificate store and TlsMaterial over the control stream") of milestone M2 exactly as specified in
`.procoder/plans/nexora-v1-m2.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 3 in `.procoder/plans/nexora-v1-m2.md` is done as written (deviations recorded in the plan first)
- [ ] procoder gate clean over the changed files; work committed (fmt/clippy clean; commit left to the lead)

## Evidence

- Red: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib server::tls'` -> compile FAIL, `cannot find type CertStore`, `cannot find function stream_server_config`.
- Green: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine'` -> all suites ok; lib `31 passed` incl. `install_rejects_mismatch_expired_and_bad_fingerprint` and `rotation_applies_to_new_handshakes_without_rebuilding_config`.
- `grep -rn private_key_pem engine/src/snapshot.rs` -> exit 1 (nothing found).
- `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> exit 0.
- e2e forwarding/control tests as in Task 2 evidence (run after Task 3 changes) -> PASS.
- Deviations recorded in the plan: fingerprint via M1 `sha2`+`hex` (no direct aws-lc-rs dep, rustls `aws_lc_rs` feature enabled); `spawn_workers` third parameter deferred to Task 4 (no unused parameter); `clock::unix_now` added.
