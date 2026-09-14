# M1 Task 11: Forwarding acceptance tests over the wire

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 11 ("Forwarding acceptance tests over the wire") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 11 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] `TestDedupAllWaitersAnswered` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestEDNSTruncationTCP` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestForwardCacheTTL` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestMain` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestUpstreamFailover` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Harness precondition: `scripts/dev-exec.sh 'make e2e-build && go test -count=1 ./e2e/harness/...'` -> `ok github.com/piwi3910/nexora/e2e/harness` (TestHarnessStandaloneEngineAnswersViaFixture passes).
- First run of the plan's tests against the committed engine: `TestForwardCacheTTL/{udp,dot,doh}` and `TestUpstreamFailover` PASS; `TestEDNSTruncationTCP` FAIL (`engine did not retry a truncated upstream reply over TCP: 0 answers`); `TestDedupAllWaitersAnswered` FAIL (`1000 of 1000 clients were not answered`, all `rcode=SERVFAIL`).
- Root causes (neither an engine bug): (1) the harness started the DNS fixture's TCP listener on a different port from UDP, so the engine's RFC-correct TCP retry to the upstream address hit a closed port -> `e2e/harness/fixture.go` now shares one port for UDP and TCP (and `TestHarnessFixtureClients` iterates a slice instead of a map keyed by address); (2) the dedup test injected a 500 ms delay against the harness default 250 ms per-attempt timeout, which by design fails the single upstream attempt -> the test's upstream uses `TimeoutMs = 1500`. Assertions unchanged (1000 answers, exactly 1 upstream query). Plan updated.
- Mutation 1 (every miss leads + no cache insert; mutant built in a throwaway pod copy): `second query reached the upstream: count=2` (udp/dot/doh), `big answer fetched 3 times`, `upstream queries = 1000, want exactly 1`.
- Mutation 2 (`forward` returns the first error): `query 0 got SERVFAIL while secondary is healthy`.
- Green: `scripts/dev-exec.sh 'make e2e-build && go test -count=1 -run "TestForwardCacheTTL|TestUpstreamFailover|TestDedupAllWaitersAnswered|TestEDNSTruncationTCP" -v ./e2e/'` 5 consecutive runs -> `ok github.com/piwi3910/nexora/e2e 8.9s`; `go test -count=10 -run "TestDedupAllWaitersAnswered|TestEDNSTruncationTCP" ./e2e/` -> ok; `go test -count=1 ./e2e/harness/... ./e2e/fixtures/...` -> ok, ok. TestMain compiled and ran in every e2e run.
