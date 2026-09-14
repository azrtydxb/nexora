# M7 T21: Debt marker test and operations limits

Status: open
Created: 2026-09-14

## Description

Implements Task 21 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create `deploy/deploytest/debt_test.go`:
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./deploy/deploytest -run TestM7DebtMarkersResolved'`. Expect PASS once Tasks 3–20 are in. If a marker remains, the task that owns the file is not done: send it back rather than editing that file here.
- [ ] In `docs/operations.md` "Known limitations":
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./deploy/deploytest/...'` and expect PASS.

## Evidence

