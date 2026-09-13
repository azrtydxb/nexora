# M2 Task 5: DoQ listener (RFC 9250)

Status: open
Created: 2026-09-13

## Description

Implement Task 5 ("DoQ listener (RFC 9250)") of milestone M2 exactly as specified in
`.procoder/plans/nexora-v1-m2.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 5 in `.procoder/plans/nexora-v1-m2.md` is done as written (deviations recorded in the plan first)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- 2026-09-13 `scripts/dev-exec.sh 'cargo test -p nexora-engine --lib server::doq'` before: FAIL `cannot find function \`decode_query\``, `bind_doq`, `endpoint_config`, `quic_server_config`, `run_doq`, type `DoqFrameError`.
- After: `cargo test --locked -p nexora-engine --lib server::doq`: `test result: ok. 2 passed; 0 failed`.
- `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` exit 0 (isolated tree, see Task 4 evidence); full `--all-targets` suite green there; `listen_port_zero::encrypted_listeners_on_port_zero_are_bound_by_every_worker_and_reported` passes (DoQ port held by both workers).
- Not committed (lead commits).
