# M4 Task 1: Contract additions and the NZF zone format

Status: open
Created: 2026-09-13

## Description

Implement Task 1 ("Contract additions and the NZF zone format") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 1 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [x] `TestCanonicalOrderRFC4034` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestCompressIsContentAddressed` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestDecodeRejectsMalformed` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestDeltaGoldenAndAfterImage` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestFullImageRoundTripAndGolden` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestM4ContractFieldNumbers` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestOutOfZoneOwnerRefused` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestSerialArithmeticRFC1982` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./mgmt/internal/control/ -run TestM4ContractFieldNumbers -count=1'` → build failed "undefined: controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA384"; `go test ./mgmt/internal/zone/` → "undefined: SerialLess"; nzf → "undefined: Record/FromRR".
- Codegen in pod (protoc 3.21.12, protoc-gen-go v1.36.12, protoc-gen-go-grpc 1.6.2), copied back with kubectl tar: only `control.pb.go` changed.
- Goldens written with `go test ./mgmt/internal/nzf -count=1 -update` (deterministic bytes, no zstd), then verified in the pod.
- Green (pod): `go test -race -count=1 ./mgmt/internal/nzf/ ./mgmt/internal/zone/ ./gen/...` → ok ×3; `go test -count=1 ./mgmt/internal/control/ -run TestM4ContractFieldNumbers -v` → `--- PASS: TestM4ContractFieldNumbers`; all six nzf tests and TestSerialArithmeticRFC1982 PASS; `gofmt -l` empty; `go vet` clean; `go build ./...` ok.
- Engine still builds: `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` clean (needed one no-op `ServerMsg` arm in `engine/src/control.rs`, recorded in the plan).
- Not committed (lead commits).

