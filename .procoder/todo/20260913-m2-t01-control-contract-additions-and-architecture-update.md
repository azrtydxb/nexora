# M2 Task 1: Control contract additions and architecture update

Status: open
Created: 2026-09-13

## Description

Implement Task 1 ("Control contract additions and architecture update") of milestone M2 exactly as specified in
`.procoder/plans/nexora-v1-m2.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 1 in `.procoder/plans/nexora-v1-m2.md` is done as written (deviations recorded in the plan first)
- [x] `TestM2ContractFieldNumbers` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestSnapshotCannotReachTlsMaterial` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- Plan reconciled first: every M1 name/signature/path in `.procoder/plans/nexora-v1-m2.md` (Tasks 1-13) checked against committed M1 code (cbec8dc) and corrected; M5 field range 500+ recorded. `launcher.sh plan check nexora-v1-m2` -> `COMPLETE`.
- Failing first: `scripts/dev-exec.sh 'go test ./mgmt/internal/control/ -run "TestM2ContractFieldNumbers|TestSnapshotCannotReachTlsMaterial"'` -> `contract_m2_test.go:59: walk did not reach PolicyGroup; the positive path is broken`, `FAIL github.com/piwi3910/nexora/mgmt/internal/control`.
- Codegen in the pod (protoc 3.21.12, protoc-gen-go v1.36.12, protoc-gen-go-grpc 1.6.2) with the `make proto` protoc line only; copied back with `kubectl ... exec deploy/toolbox -c toolbox -- tar ... | tar -xf -`. Only `control.pb.go` changed.
- `engine/src/control.rs`: `Hello.tls_fingerprint_sha256: String::new()` and `Some(ServerMsg::TlsMaterial(_)) | None => {}` (Task 3 replaces).
- Pass: same go test -v -> `--- PASS: TestM2ContractFieldNumbers`, `--- PASS: TestSnapshotCannotReachTlsMaterial`, `ok ... control 0.015s`.
- `scripts/dev-exec.sh 'cargo fmt --all -- --check; cargo clippy --locked -p nexora-engine --all-targets -- -D warnings; cargo test --locked -p nexora-engine'` -> fmt clean, clippy Finished with no warnings, every `test result: ok` (lib 22 passed, all integration binaries ok, 0 failed).
- `scripts/dev-exec.sh 'make webui-placeholder && go vet ./gen/... ./mgmt/internal/control/ && gofmt -l gen mgmt; go test -count=1 ./gen/... ./mgmt/...'` -> no vet/gofmt output; ok for gen/controlv1, api, auth, blocklist, config, control, pki, querylog, snapshot, stats, store.
- Not committed (the lead commits serially per the implementer brief).
