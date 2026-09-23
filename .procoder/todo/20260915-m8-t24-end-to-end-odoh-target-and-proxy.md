# M8 T24: End-to-end ODoH target and proxy

Status: open
Created: 2026-09-15

## Description

Implements Task 24 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create `e2e/odoh_test.go` with `TestODoHTargetAndProxy`:
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e -run TestODoHTargetAndProxy -count=1 -v'`
- [ ] Report the paths. The lead commits `M8 T24: e2e ODoH target and proxy`.

## Evidence
