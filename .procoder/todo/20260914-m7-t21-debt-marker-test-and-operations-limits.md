# M7 T21: Debt marker test and operations limits

Status: open
Created: 2026-09-14

## Description

Implements Task 21 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `deploy/deploytest/debt_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./deploy/deploytest -run TestM7DebtMarkersResolved'`. Expect PASS once Tasks 3–20 are in. If a marker remains, the task that owns the file is not done: send it back rather than editing that file here.
- [x] In `docs/operations.md` "Known limitations": (no lifted entries were listed; added the signed-zone rebuild, OpenSearch `_id` / `indices.id_field_data.enabled` and `recursor_cache_max_bytes` entries)
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./deploy/deploytest/...'` and expect PASS.

## Evidence

- `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test -count=1 ./deploy/deploytest -run TestM7DebtMarkersResolved -v'` -> `--- PASS: TestM7DebtMarkersResolved`, `ok github.com/piwi3910/nexora/deploy/deploytest`. No red phase: Tasks 3-20 were already committed.
- Mutation check (scratch copy of the referenced files): re-adding "loads the whole zone per record edit" to service.go, "the index is resolved against the runtime current at drain time" to otlp.rs, and removing the build.go debt marker gave three failures, `FAIL m/deploy/deploytest`.
- `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test -count=1 ./deploy/deploytest/...'` -> `ok github.com/piwi3910/nexora/deploy/deploytest 1.143s`.
- Lead note from Task 14 is done: the Known limitations entry says paging sorts by `_id`, needs `indices.id_field_data.enabled` (true by default in OpenSearch 3.x), and searches fail with HTTP 400 when it is off.
- Commit left to the lead.
- Full suites in toolbox-m7: `go vet ./...` exit 0; `go test -count=1 ./mgmt/... ./deploy/...` exit 0 (24 packages ok, no FAIL); `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` exit 0; `cargo test --locked -p nexora-engine` exit 0 (unit tests: 255 passed, 0 failed, 1 ignored; every integration binary ok).
