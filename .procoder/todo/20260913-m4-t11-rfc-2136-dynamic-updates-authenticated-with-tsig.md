# M4 Task 11: RFC 2136 dynamic updates authenticated with TSIG

Status: open
Created: 2026-09-13

## Description

Implement Task 11 ("RFC 2136 dynamic updates authenticated with TSIG") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 11 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [x] `TestPrerequisitesRFC2136` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestSecondaryAndDynamicUpdate` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestUpdateSectionAndSerial` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

Management-plane side done (2026-09-14); engine side (update.rs, opcode 5 slow path, UpdateForwarder, UpdateResult handling) not yet built, so the task stays open.

- `scripts/dev-exec.sh 'go test ./mgmt/internal/dynupdate/ -count=1'` before `apply.go`: FAIL `no non-test Go files`; after: `go test -race ./mgmt/internal/dynupdate/ -count=1 -v` — PASS TestPrerequisitesRFC2136, TestUpdateSectionAndSerial, TestTSIGAndUpdatePolicy.
- `go test -race ./mgmt/internal/control/ -run "TestNotifyAndUpdate|TestEnroll"` — PASS (results channel, receive loop not blocked, 16-update concurrency bound, 65535-byte bound).
- `scripts/dev-exec.sh make e2e-build` then `go test ./e2e/ -run TestSecondaryAndDynamicUpdate -count=1` — FAIL: `signed update: rcode NOTIMP` (engine answers UPDATE with NOTIMP; engine forwarding not implemented yet).

Engine side done (2026-09-14): `engine/src/authoritative/update.rs` (+ `update_tests.rs`, plus `auth_state_forwards_on_the_attached_stream_and_completes_by_request_id`), `SlowKind::Update` from `dispatch::unparsed` on every transport, `AuthState: UpdateForwarder` (pending waiters by request id, 5 s timeout, cap 1024, failed at once on detach), `ServerMsg::UpdateResult` -> `complete_update`, `nexora_auth_updates_total{result=applied|rejected|refused|failed}`.

- `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib -- update_tests'` — `test result: ok. 4 passed`.
- `scripts/dev-exec.sh make e2e-build` then `NEXORA_E2E_BIN_DIR=/work/nexora/bin go test -count=3 ./e2e/ -run "TestSecondaryAndDynamicUpdate|TestAXFRIXFROut|TestAuthoritativeZonePropagation" -v` — `--- PASS: TestSecondaryAndDynamicUpdate` x3; `ok github.com/piwi3910/nexora/e2e 44.782s`.
- `scripts/dev-exec.sh make engine-test` — all suites ok incl. hot_path_alloc; fmt and clippy `-D warnings` clean.
