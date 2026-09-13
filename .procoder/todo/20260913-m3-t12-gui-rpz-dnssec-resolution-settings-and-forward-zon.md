# M3 Task 12: GUI — `/rpz`, `/dnssec`, resolution settings and forward zones on `/upstreams`

Status: open
Created: 2026-09-13

## Description

Implement Task 12 ("GUI — `/rpz`, `/dnssec`, resolution settings and forward zones on `/upstreams`") of milestone M3 exactly as specified in
`.procoder/plans/nexora-v1-m3.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 12 in `.procoder/plans/nexora-v1-m3.md` is done as written (deviations recorded in the plan first)
- [x] `TestGUICoverage` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- 2026-09-13 `scripts/dev-exec.sh 'make web-test'` — PASS (typecheck, eslint, `permission parity: 77 operations match`, vite build).
- `make e2e-build` could not complete in the shared tree: `engine/src/recursor/dnssec/mod.rs` (concurrent M3 Tasks 6-8 work, uncommitted) references missing modules. The engine was therefore built from `git archive HEAD` (f1f244f) in `/tmp/gui-t12-head` (`cargo build --locked --release -p nexora-engine`, OK), web + mgmt + fixture from the working tree via `make web-build` + `go build` into `/tmp/gui-t12-bin`.
- `NEXORA_E2E_BIN_DIR=/tmp/gui-t12-bin go test -v -count=1 ./e2e/ -run '^TestGUICoverage$'` — `19 passed (45.2s)` incl. 15-rpz (2 tests), 16-dnssec, 17-resolution; `--- PASS: TestGUICoverage (65.95s)` (all 77 operations covered).
- `NEXORA_E2E_OPENSEARCH_URL=http://opensearch.nexora.svc:9200 NEXORA_E2E_JAEGER_QUERY_URL=http://jaeger.observability.svc:16686 NEXORA_E2E_BIN_DIR=/tmp/gui-t12-bin go test -v -count=1 ./e2e/ -run '^TestAuthRBACAuditOIDC$'` — `--- PASS: TestAuthRBACAuditOIDC (14.93s)`.
- Red run of TestGUICoverage before implementation was not executed (specs and screens were written in one pass).
- Not committed (lead commits).

