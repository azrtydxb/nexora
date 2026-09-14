# M3 Task 13: Private DNS hierarchy fixture and harness helpers (fake root/TLD/leaf, signed zones, spoofer, BIND primary)

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 13 ("Private DNS hierarchy fixture and harness helpers (fake root/TLD/leaf, signed zones, spoofer, BIND primary)") of milestone M3 exactly as specified in
`.procoder/plans/nexora-v1-m3.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 13 in `.procoder/plans/nexora-v1-m3.md` is done as written (deviations recorded in the plan first)
- [x] `TestBigAnswerTruncatesOverUDPAndCompletesOverTCP` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestForwarderEndpointAnswersRecursivelyWithSignatures` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestNamedHarnessServesTSIGAXFRAndReloads` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestNegativeAnswersCarryDenialProofs` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestRootReferralCarriesSignedDSAndRootDSMatches` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestSignedGoodVerifiesAndBadDoesNot` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestSpoofServerSendsForgeriesBeforeRealAnswer` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Environment (dev pod): 127.0.53.x binds for UDP and TCP on the same port (Linux lo 127.0.0.1/8); `named` 9.20.26 serves TSIG AXFR unprivileged (`su dev -c 'named -g -c conf -u dev'`, high port); `named -u root` exits with a fatal error, so the harness passes `-u dev` as root.
- Red: `scripts/dev-exec.sh 'go test -count=1 ./e2e/harness/ -run "Test(NamedHarness|WriteKEK)"'` -> `env.StartNamed undefined`, `undefined: WriteKEK` (build failed). The authhier package was written before its test file was run, so its red step was not observed.
- Green: `scripts/dev-exec.sh 'B=$(mktemp -d) && go build -o $B/nexora-fixture ./e2e/fixtures/cmd/nexora-fixture && go vet ./e2e/fixtures/cmd/nexora-fixture ./e2e/fixtures/authhier ./e2e/harness && NEXORA_E2E_BIN_DIR=$B go test -race -count=1 -v ./e2e/fixtures/authhier/ ./e2e/harness/ -run "Test(Root|Signed|Negative|Big|Spoof|Forwarder|NamedHarness|WriteKEK|Lame|Delv|HierarchyHarness)"'` -> 11 PASS, `ok .../e2e/fixtures/authhier 3.219s`, `ok .../e2e/harness 1.417s`.
- Repeats: `go test -count=3 ./e2e/fixtures/...` ok; harness Named/WriteKEK/Hierarchy tests `-count=5` ok.
- Independent validator: delv through the forwarder under the per-run root DS reports `; fully validated` (www/alias.good.test, www.n3.test), `; negative response, fully validated` (nope.good.test NSEC, nope.n3.test NSEC3), `; unsigned answer` (plain, glueless, lame) and `resolution failed: RRSIG failed to verify` for www.bad.test.
- Lint: `golangci-lint run ./e2e/fixtures/... ./e2e/harness/...` no findings in the new files; `gofmt -l e2e` empty; `procoder check` on the changed files: 11 clean.
- Not verified here: `(*Hierarchy).ConfigureRecursion` (needs Task 11's `/resolution` and `/dnssec/trust-anchors`; exercised by Task 14).

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in 3f99d75
