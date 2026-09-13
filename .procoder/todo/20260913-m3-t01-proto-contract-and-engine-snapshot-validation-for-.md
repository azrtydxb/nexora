# M3 Task 1: Proto contract and engine snapshot validation for M3 fields

Status: open
Created: 2026-09-13

## Description

Implement Task 1 ("Proto contract and engine snapshot validation for M3 fields") of milestone M3 exactly as specified in
`.procoder/plans/nexora-v1-m3.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 1 in `.procoder/plans/nexora-v1-m3.md` is done as written (deviations recorded in the plan first)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- Plan deviations recorded first in `.procoder/plans/nexora-v1-m3.md` (binding plan-review correction): `ConfigSnapshot.dnssec_validate_forwarded = 105` + rule "requires dnssec.validation"; `metrics.rs` Stats literal and `proto.rs` clippy allow added to Files; later tasks 5/7/11/12/14/15 carry the setting. `procoder plan check nexora-v1-m3` -> COMPLETE.
- RED: `scripts/dev-exec.sh 'go test ./mgmt/internal/control/ -run "TestM3ContractFieldNumbers|TestSnapshotCannotReachRpzTsigKeys"'` -> FAIL before the proto change.
- Regenerated in the pod (protoc 3.21.12, protoc-gen-go v1.36.12, protoc-gen-go-grpc 1.6.2) into a temp dir and copied back; only `control.pb.go` changed.
- GREEN: `go test ./mgmt/internal/control/` -> `ok`; `make mgmt-test` -> exit 0.
- RED: `cargo test --locked -p nexora-engine --lib snapshot_m3::tests` -> "cannot find function `validate_m3` in this scope"; GREEN -> `test result: ok. 9 passed`.
- `cargo test --locked -p nexora-engine --test snapshot_apply` -> `test result: ok. 6 passed`.
- `cargo test --locked -p nexora-engine --all-targets` -> all `test result: ok`; `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> clean; `cargo fmt --all -- --check` -> clean; `go vet ./...` -> clean.
- `make e2e-build` + `go test -count=1 ./e2e/...` -> all M1/M2 tests pass except TestQueryLogBackends/opensearch and TestObservabilityMetricsTraces, which failed only because the pod lacked NEXORA_E2E_OPENSEARCH_URL / NEXORA_E2E_JAEGER_QUERY_URL; rerun with the dev-pod.yaml values -> `ok github.com/piwi3910/nexora/e2e 28.140s`.
- `procoder check` over changed files: 0 unformatted; the 2 BLOCKING secret findings are the pre-existing Task 11 test literal `tsig_secret` in the plan (present at HEAD), not introduced here.
- Not committed (lead commits serially).
