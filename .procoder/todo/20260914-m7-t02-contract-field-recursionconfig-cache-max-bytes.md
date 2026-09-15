# M7 T2: Contract field RecursionConfig.cache_max_bytes

Status: open
Created: 2026-09-14

## Description

Implements Task 2 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Run `rg -n "cache_max_bytes" proto/nexora/control/v1/control.proto` and expect no match.
- [x] In `message RecursionConfig`, after `authority_port = 6;`, add:
- [x] Regenerate with the `make proto` protoc command (no `make generate` exists; run in the dev pod, copied back) and expect `gen/go/nexora/control/v1/control.pb.go` to contain `CacheMaxBytes`.
- [x] Run `scripts/dev-exec.sh 'cargo build --locked -p nexora-engine && go build ./... && cargo test --locked -p nexora-engine --test proto_roundtrip'` and expect success. The Rust struct gains the field through `build.rs`. Existing struct literals use `..Default::default()`; fix any that do not by addin

## Evidence

- Before: `rg -n "cache_max_bytes" proto/nexora/control/v1/control.proto` -> no match (exit 1).
- Added `uint64 cache_max_bytes = 800;` with the M7 comment to `message RecursionConfig`.
- Generated in pod toolbox-m7 (protoc 3.21.12, protoc-gen-go v1.36.12, the versions in the committed
  headers; laptop has protoc 33.4) with the `make proto` protoc command, copied back via tar.
  `grep -c CacheMaxBytes gen/go/nexora/control/v1/control.pb.go` -> 3; `control_grpc.pb.go` unchanged.
- Red: `cargo build --locked -p nexora-engine --all-targets` -> `error[E0063]: missing field
cache_max_bytes` at `engine/src/recursor/dispatch_tests.rs:64` and `engine/src/snapshot_m3.rs:199`.
  Fixed both with `cache_max_bytes: 0` (plan Files line updated).
- Green (`NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh`, after `make webui-placeholder`, needed
  for `go build ./...` in a fresh pod): `cargo build --locked -p nexora-engine` Finished;
  `go build ./...` ok; `cargo test --locked -p nexora-engine --test proto_roundtrip` -> `test result:
ok. 1 passed`; `--lib snapshot_m3` -> 9 passed; `--lib dispatch_tests` -> 6 passed.
- `cargo fmt --all -- --check` ok; `cargo clippy --locked -p nexora-engine --all-targets -- -D
warnings` Finished with no warnings; `go vet ./gen/... ./mgmt/internal/snapshot/...` ok.
