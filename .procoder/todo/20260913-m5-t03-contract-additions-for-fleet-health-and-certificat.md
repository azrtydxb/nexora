# M5 Task 3: Contract additions for fleet health and certificate renewal

Status: open
Created: 2026-09-13

## Description

Implement Task 3 ("Contract additions for fleet health and certificate renewal") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 3 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [ ] `TestFleetContractRoundTrip` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

Implemented 2026-09-14, not committed (lead commits).

- Red: `scripts/dev-exec.sh 'go test ./mgmt/internal/control/ -run TestM5ContractFieldNumbers -count=1'` -> build failed (undefined oneof wrappers).
- Codegen in the dev pod with protoc 3.21.12, protoc-gen-go v1.36.12, protoc-gen-go-grpc 1.6.2 (laptop plugins are v1.36.11/1.6.1); only `control.pb.go` changed. Plan text updated.
- Green: `scripts/dev-exec.sh 'go test ./mgmt/internal/control/ -run TestM5ContractFieldNumbers -count=1 && cargo fmt --check -p nexora-engine && cargo check --locked -p nexora-engine'` -> `ok`, `Finished`.
- `scripts/dev-exec.sh make engine-test` -> exit 0; `make mgmt-test` -> exit 0.
- The todo's `TestFleetContractRoundTrip` is the plan's `TestM5ContractFieldNumbers` (field numbers plus wire round trip).
