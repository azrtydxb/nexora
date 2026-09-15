# M7 T23: Milestone verification, kw deployment and issue closing

Status: open
Created: 2026-09-14

## Description

Implements Task 23 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Run the full local suites in the dev pod and expect every one to pass. `TestGUICoverage` and `TestKeyStorageBackends` are included in the e2e run.
- [ ] Deploy to kw per the roadmap:
- [ ] Re-run `scripts/compose-verify.sh piwi@192.168.10.211` with `NEXORA_TAG` set to the deployed image tag and expect exit 0.
- [ ] Close the issues with a comment naming the commit and proof:
- [ ] Run `gh issue list -R azrtydxb/nexora --state open --limit 200 --json number -q '.[].number'` and expect none of 3, 5, 9–29 listed. Set `Status: implemented` in this plan.

## Evidence

