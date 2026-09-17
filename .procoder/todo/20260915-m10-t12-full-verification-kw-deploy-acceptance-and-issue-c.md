# M10 T12: Full verification, kw deploy, acceptance and issue close

Status: open
Created: 2026-09-15

## Description

Implements Task 12 of `.procoder/plans/nexora-m10-querylog-backends.md` (spec `.procoder/specs/nexora-m10-querylog-backends.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Run
- [ ] Run
- [ ] Run `scripts/kw-deploy.sh` with the DNS probe (5 queries/s each to 192.168.10.136 and 192.168.10.139)
- [ ] Wait 3 minutes after the collector rollout. Run `scripts/kw-acceptance.sh` and expect PASS,
- [ ] Close issues #35 and #36 in `azrtydxb/nexora` with
- [ ] Report the proof. No commit.

## Evidence
