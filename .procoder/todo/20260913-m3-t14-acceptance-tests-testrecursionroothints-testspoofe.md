# M3 Task 14: Acceptance tests — TestRecursionRootHints, TestSpoofedReplyRejected, TestDNSSECValidation, TestRPZPolicy

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 14 ("Acceptance tests — TestRecursionRootHints, TestSpoofedReplyRejected, TestDNSSECValidation, TestRPZPolicy") of milestone M3 exactly as specified in
`.procoder/plans/nexora-v1-m3.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 14 in `.procoder/plans/nexora-v1-m3.md` is done as written (deviations recorded in the plan first)
- [x] `TestDNSSECValidation` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestRPZPolicy` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestRecursionRootHints` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestSpoofedReplyRejected` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Files: `e2e/m3_helpers_test.go`, `e2e/recursion_test.go`, `e2e/dnssec_test.go`, `e2e/rpz_test.go` (plan code, plus two
  positive guards so negatives are non-vacuous: case-mismatch counter shown live via the spoofing server; stored
  snapshots shown to carry `rpz.axfr.test.` and the TSIG key name before the secret-absence check). Plan text updated.
- `scripts/dev-exec.sh 'make e2e-build'` then `NEXORA_E2E_BIN_DIR=bin go test -v -count=3 -timeout 20m ./e2e/ -run "^(TestRecursionRootHints|TestSpoofedReplyRejected|TestDNSSECValidation|TestRPZPolicy)$"`
  -> 12/12 PASS (`ok github.com/piwi3910/nexora/e2e 124.774s`), all subtests including forward-zone and forward-mode validation.
- `make engine-test` exit 0 (15 binaries, 163 passed, 0 failed); `make mgmt-test` exit 0 (16 packages ok);
  `make web-test` exit 0; `make e2e-build` exit 0.
- `NEXORA_E2E_OPENSEARCH_URL=http://opensearch.nexora.svc:9200 NEXORA_E2E_JAEGER_QUERY_URL=http://jaeger.observability.svc:16686 go test -count=1 -timeout 60m ./e2e/... ./bench/...`
  -> exit 0: `ok e2e 231.512s`, `ok e2e/fixtures/authhier`, `ok e2e/fixtures/cmd/nexora-fixture`, `ok e2e/harness`,
  `ok bench/cmd/perfgate`, `ok bench/dnsperf`.
- No product bug surfaced; `ConfigureRecursion` worked unchanged. Not committed (lead commits).

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in b643000
