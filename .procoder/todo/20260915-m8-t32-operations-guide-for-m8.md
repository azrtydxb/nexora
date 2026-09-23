# M8 T32: Operations guide for M8

Status: open
Created: 2026-09-15

## Description

Implements Task 32 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create `deploy/deploytest/m8_docs_test.go`:
- [ ] Run `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestOperationsGuideCoversM8 -count=1'`
- [ ] Write the four sections after "Encrypted DNS: DoT, DoH and DoQ":
- [ ] Run `scripts/dev-exec.sh 'go test ./deploy/deploytest -count=1'`, then
- [ ] Report the paths. The lead commits `M8 T32: operations guide for mDNS, ZONEMD, ODoH and catalog zones`.

## Evidence
