# M6 T1: Contract fields 700–799 and the M6 architecture text

Status: open
Created: 2026-09-14

## Description

Implements Task 1 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/control/contract_m6_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/control -run TestContractM6FieldsRoundTrip -count=1'`
- [x] Edit `proto/nexora/control/v1/control.proto`:
- [x] Regenerate in the dev pod and copy back:
- [x] Add `..Default::default()` as the last field of every `ResolverConfig { .. }` literal in the six
- [x] Run
- [x] Edit `docs/architecture.md`:
- [x] Run `grep -c '700-799' docs/architecture.md; grep -c 'authoritative_allow_cidrs' docs/architecture.md; grep -c '/resolution' docs/architecture.md`
- [ ] Report the paths under Files. The lead commits

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./mgmt/internal/control -run TestContractM6FieldsRoundTrip -count=1'` ->
  `FAIL [build failed]`, `contract_m6_test.go:15:3: unknown field AuthoritativeAllowCidrs in struct literal of type controlv1.ConfigSnapshot`.
- Regenerated with the plan's `protoc` command in the pod; `control.pb.go` copied back
  (`control_grpc.pb.go` regenerated identical: the service did not change).
- First `cargo build --locked -p nexora-engine --all-targets` failed with
  `E0004 non-exhaustive patterns: Some(server_message::Msg::LogRequest(_))` at `engine/src/control.rs:753`;
  added a `LogRequest | None` arm with a `debt:` comment (plan Task 1 text updated).
- Green: `go test ./mgmt/internal/control -run TestContractM6FieldsRoundTrip -count=1` -> `ok ... 0.022s`;
  `cargo build --locked -p nexora-engine --all-targets` -> `Finished dev profile`;
  `cargo fmt -p nexora-engine --check`, `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings`,
  `go vet ./gen/... ./mgmt/internal/control/`, `gofmt -l` -> clean.
- `go build ./mgmt/... ./gen/...` clean; `go test ./gen/... ./mgmt/internal/control/ -count=1` -> both `ok`.
- `grep -c` for `700-799`, `authoritative_allow_cidrs`, `/resolution` in `docs/architecture.md` -> 2, 2, 1;
  `scripts/pc-format.sh docs/architecture.md` run.
