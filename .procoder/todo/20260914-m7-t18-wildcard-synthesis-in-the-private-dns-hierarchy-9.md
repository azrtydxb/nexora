# M7 T18: Wildcard synthesis in the private DNS hierarchy (#9)

Status: open
Created: 2026-09-14

## Description

Implements Task 18 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] In `spec.go`:
- [ ] Add to `authhier_test.go` `TestWildcardAnswerCarriesExpandedSignatureAndNextCloserProof`:
- [ ] Add three subtests to `TestDNSSECValidation` in `e2e/dnssec_test.go`:
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./e2e/fixtures/authhier -run TestWildcardAnswer'`. Expect FAIL: the query for `x.w.good.test.` returns NXDOMAIN.
- [ ] In `server.go` `authoritative`, before the NXDOMAIN branch, add RFC 1034 §4.3.3 synthesis:
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./e2e/fixtures/authhier/... && make e2e-build && go test -count=1 ./e2e -run "TestDNSSECValidation|TestRecursionRootHints|TestSpoofedReplyRejected"'` and expect every test to pass. `TestDelvValidatesTheHierarchyIndependently` also validates the new records.

## Evidence

