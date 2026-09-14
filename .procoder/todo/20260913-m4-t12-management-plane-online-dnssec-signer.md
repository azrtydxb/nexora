# M4 Task 12: Management-plane online DNSSEC signer

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 12 ("Management-plane online DNSSEC signer") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 12 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [x] `TestEnableSigns` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestEnableSignsAndEditsAreResigned` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestSignNSEC3IncludesEmptyNonTerminalsWithZeroIterations` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestSignNSECChainAndSignatures` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestSignatureReuseAndRefresh` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed (gate clean: yes; commit left to the lead)

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./mgmt/internal/dnssec/ -count=1'` before sign.go -> `undefined: Sign` (build failed).
- `scripts/dev-exec.sh 'go test ./mgmt/internal/dnssec/ -count=1 -v'` -> PASS: TestSignNSECChainAndSignatures, TestSignNSEC3IncludesEmptyNonTerminalsWithZeroIterations, TestSignatureReuseAndRefresh, TestSignedZonesValidateWithBIND (ecdsa-nsec, ecdsa-nsec3, rsa-nsec3: dnssec-verify + delv against named), TestEnableSignsAndEditsAreResigned, TestDynamicUpdateToSignedZoneIsResignedIncrementally, TestPKCS11KeysSignInsideTheToken, TestEnableRefusesWithoutKeyStorageOrOnSecondaries, TestMaintainerRefreshesSignaturesUnderAdvisoryLock; `ok github.com/piwi3910/nexora/mgmt/internal/dnssec 18.693s`.
- Mutation checks: dropping NSEC3 empty non-terminals fails TestSignedZonesValidateWithBIND (nsec3 subtests); ResignSOA returning the served set unchanged fails TestEnableSignsAndEditsAreResigned ("SOA signature is stale"); ignoring the advisory lock fails TestMaintainerRefreshesSignaturesUnderAdvisoryLock.
- `scripts/dev-exec.sh 'go test ./mgmt/internal/dnssec/ ./mgmt/internal/zone/ ./mgmt/internal/dynupdate/ ./mgmt/internal/xfrin/ ./mgmt/internal/api/ ./mgmt/internal/zonefile/ ./mgmt/internal/snapshot/ ./mgmt/internal/control/ ./mgmt/internal/store/... -count=1'` -> all ok.
- `go vet ./mgmt/...` (pod) -> ok; `gofmt -l mgmt` -> empty; `procoder check` -> 0 unformatted, 0 blocking; `procoder lint` -> 0 findings.
- Deviations (recorded in the plan): migration 00402 (00401 taken); Task 13 goldens kept (not overwritten), BIND validation test instead; SignRRset dropped; reuse needs > RefreshBefore + 1h; Maintainer (refresh + advisory lock) added now.

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in f04a92b
