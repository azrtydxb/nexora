# M2 Task 12: `TestPerClientPolicy` and `TestSafeSearchRewrites`

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 12 ("`TestPerClientPolicy` and `TestSafeSearchRewrites`") of milestone M2 exactly as specified in
`.procoder/plans/nexora-v1-m2.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 12 in `.procoder/plans/nexora-v1-m2.md` is done as written (deviations recorded in the plan first)
- [x] `TestBlocklistSubscription` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestEncryptedTransports` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestForwardCacheTTL` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestPerClientPolicy` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestSafeSearchRewrites` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- First runs exposed test races, not product bugs: "open (most specific CIDR) = 0.0.0.0" and "exact beats wildcard = 192.168.1.50" came from asserting before the last mutation's config version was applied (the same test passed on rerun). Added `waitLatestApplied` (waits for `GET /engines` applied_version >= latest); plan updated. `go test -count=3 -run TestSafeSearchRewrites` 3/3 PASS; `-count=4 -run TestPerClientPolicy` 4/4 PASS.
- Mutation `PolicyTable::select` -> `return (&self.global, None)`: `TestPerClientPolicy` FAIL "condition not met within 10s: not yet: wide group blocks ads.example.test". Reverted.
- Mutation `EffectivePolicy::check` without the rewrite lookup: `TestSafeSearchRewrites` FAIL "not yet: google safe search applied". Reverted.
- Query-log `rewritten` end to end (openapi enum, gen.go via oapi-codegen v2.8.0 in the pod, schema.d.ts via pnpm gen:api, Query log filter option and badge). Assertion added to `TestSafeSearchRewrites` (`GET /query-log?filter=rewritten&name=` for nas.home.test and www.google.com). Mutation mapping `FilterOutcome::Rewritten => "none"` in the engine: FAIL "query log lists nas.home.test as rewritten". Reverted.
- Full suite (`NEXORA_E2E_OPENSEARCH_URL`, `NEXORA_E2E_JAEGER_QUERY_URL` exported): `make engine-test` EXIT=0 (82 passed, 0 failed); `make mgmt-test` EXIT=0 (13 ok packages, 0 FAIL); `make web-test` EXIT=0; `make e2e-build` EXIT=0; `go test -count=1 -timeout 60m ./e2e/... ./bench/...` — `ok e2e 176.766s`, fixture, harness, perfgate, dnsperf ok.
- Not committed (lead commits).

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in 392f6f4
