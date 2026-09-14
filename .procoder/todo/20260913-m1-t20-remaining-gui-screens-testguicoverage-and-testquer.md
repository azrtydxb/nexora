# M1 Task 20: Remaining GUI screens, `TestGUICoverage` and `TestQueryLogBackends`

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 20 ("Remaining GUI screens, `TestGUICoverage` and `TestQueryLogBackends`") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 20 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] `TestGUICoverage` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestQueryLogBackends` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- `go vet ./e2e/` before `openapi.go`: `undefined: harness.RepoRoot` (expected compile failure).
- `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_OPENSEARCH_URL=http://opensearch.nexora.svc:9200 NEXORA_E2E_JAEGER_QUERY_URL=http://jaeger.observability.svc:16686 NEXORA_E2E_BIN_DIR=/work/nexora/bin go test -count=1 -v -run "TestGUICoverage|TestQueryLogBackends" ./e2e/'`:
  Playwright `11 passed`; `--- PASS: TestGUICoverage (44.38s)`, `--- PASS: TestQueryLogBackends/builtin (6.11s)`, `--- PASS: TestQueryLogBackends/opensearch (7.60s)`, `ok github.com/piwi3910/nexora/e2e 58.099s`.
- After the SetupPage race fix: `go test -count=3 -run "TestGUICoverage|TestAuthRBACAuditOIDC" ./e2e/` in the pod: 3/3 PASS each, `ok ... 178.401s`.
- `go test -run TestOperationCoverageMatching ./e2e/harness/`: `ok`; mutating the empty-segment rule in `MatchOperation` makes it FAIL.
- `scripts/dev-exec.sh make web-test`: typecheck, eslint, `permission parity: 42 operations match`, vite build — exit 0.
- Not committed (the lead commits serially).

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in 5531d62
