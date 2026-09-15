# M7 T18: Wildcard synthesis in the private DNS hierarchy (#9)

Status: open
Created: 2026-09-14

## Description

Implements Task 18 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] In `spec.go`:
- [x] Add to `authhier_test.go` `TestWildcardAnswerCarriesExpandedSignatureAndNextCloserProof`:
- [x] Add three subtests to `TestDNSSECValidation` in `e2e/dnssec_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./e2e/fixtures/authhier -run TestWildcardAnswer'`. Expect FAIL: the query for `x.w.good.test.` returns NXDOMAIN.
- [x] In `server.go` `authoritative`, before the NXDOMAIN branch, add RFC 1034 §4.3.3 synthesis:
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./e2e/fixtures/authhier/... && make e2e-build && go test -count=1 ./e2e -run "TestDNSSECValidation|TestRecursionRootHints|TestSpoofedReplyRejected"'` and expect every test to pass. `TestDelvValidatesTheHierarchyIndependently` also validates the new records.

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test -count=1 ./e2e/fixtures/authhier -run TestWildcardAnswer'` -> `authhier_test.go:143: x.w.good.test. A rcode = NXDOMAIN, want NOERROR`, FAIL.
- Green: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test -count=1 -v ./e2e/fixtures/authhier/...'` -> all 9 tests PASS, including `TestWildcardAnswerCarriesExpandedSignatureAndNextCloserProof` and `TestDelvValidatesTheHierarchyIndependently` (delv present; new cases x.w.good.test A / AAAA, x.w.n3.test A fully validated, x.w.wild.test A resolution failed); `ok github.com/piwi3910/nexora/e2e/fixtures/authhier 3.336s`.
- `go vet ./e2e/fixtures/authhier/ ./e2e/` clean; `gofmt -l e2e` empty.
- `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test -count=1 ./e2e -run "TestDNSSECValidation|TestRecursionRootHints|TestSpoofedReplyRejected" -v'` -> every test and subtest PASS (new: wildcard answer validates with AD, wildcard NODATA is proven, wildcard answer without next-closer proof is bogus with EDE 12); `ok github.com/piwi3910/nexora/e2e 21.825s`.
- Deviation: miekg `RRSIG.Verify` rejects an RRset whose owner differs from the RRSIG owner (`dns: bad rrset`), so the test rewrites copies of both the A and the RRSIG back to `*.w.good.test.`; plan text updated.
- Not committed (lead commits).

