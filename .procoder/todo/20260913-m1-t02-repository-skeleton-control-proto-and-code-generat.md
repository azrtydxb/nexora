# M1 Task 2: Repository skeleton, control proto and code generation

Status: closed 2026-09-13
Created: 2026-09-13

## Description

Implement Task 2 ("Repository skeleton, control proto and code generation") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 2 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] `TestConfigSnapshotRoundTrip` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] Rust tests pass in the dev pod: `config_snapshot_round_trips`, `main`
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Failing first: `scripts/dev-exec.sh go test ./gen/...` (no go.mod) -> `pattern ./gen/...: directory prefix gen does not contain main module`, FAIL; `scripts/dev-exec.sh cargo test -p nexora-engine --test proto_roundtrip` (no Cargo.toml) -> ``could not find `Cargo.toml` ``, exit 101.
- Codegen in the pod (protoc v3.21.12, protoc-gen-go v1.36.12, protoc-gen-go-grpc v1.6.2) + `go mod tidy` + `cargo generate-lockfile` (301 packages), copied back with `kubectl ... exec deploy/toolbox -c toolbox -- tar ... | tar -xf -`.
- First `cargo test` failed with E0592 duplicate `connect` (rpc Connect vs tonic transport constructor); fixed with `build_transport(false)`, plan updated.
- `scripts/dev-exec.sh 'go test ./gen/... && go vet ./gen/... && gofmt -l gen'` -> `ok  github.com/piwi3910/nexora/gen/go/nexora/control/v1 0.011s`, no vet/gofmt output.
- `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine'` -> `src/lib.rs` ok 0 passed; `src/main.rs` ok 0 passed; `test config_snapshot_round_trips ... ok` (1 passed); doc-tests ok.
- `cargo fmt --all -- --check` clean; `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> Finished, no warnings; `nexora-engine --help` shows `--config <CONFIG> [default: /etc/nexora/engine.toml]`.
- procoder check: `9 clean, 0 unformatted ... (0 blocking)`; committed 2844b8e.

