# M8 T4: ZONEMD digest and verification in the management plane

Status: open
Created: 2026-09-15

## Description

Implements Task 4 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/zonemd/zonemd_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/zonemd -count=1'` and expect FAIL: the
- [x] Implement `zonemd.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/zonemd -count=1 -v'` and expect PASS with
- [ ] Report the paths. The lead commits `M8 T4: ZONEMD digest and verification (Go)`.

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test ./mgmt/internal/zonemd -count=1'`
  failed to compile (`undefined: SchemeSimple`, `undefined: Digest`, `undefined: Verify`, ...).
- Green: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test ./mgmt/internal/zonemd -count=1 -v && go vet ./mgmt/internal/zonemd'`
  gave `zonemd_test.go:57: checked 4`, `--- PASS: TestDigestMatchesRFC8976AppendixA`,
  `--- PASS: TestVerifyRules`, `ok github.com/piwi3910/nexora/mgmt/internal/zonemd`; vet clean;
  `gofmt -l mgmt/internal/zonemd` empty.
- Mutation check (private copy `/work/m8-t4`, removed afterwards): dropping RDATA-name lowercasing,
  dropping duplicate removal, reversing the owner-length order, and skipping the serial check were
  each killed by the tests.
- Commit pending (the lead commits).
