# M6 T34: Deploy M6 to kw and run acceptance

Status: open
Created: 2026-09-14

## Description

Implements Task 34 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Run `grep -n "NEXORA_REPOSITORY_URL" deploy/kw/values-kw.yaml` and expect FAIL (no match). Add
- [ ] After the lead has committed Tasks 1–33, build the images:
- [ ] Start the DNS probe, then run `scripts/kw-deploy.sh` (it runs 5 queries/s to 192.168.10.136 and
- [ ] Run `scripts/kw-acceptance.sh` and expect PASS for `TestKwSmoke` (including
- [ ] On kw, verify by hand in `https://nexora.kw.local`:
- [ ] Report the paths, the image tag and the probe and acceptance results. The lead closes #54–#67

## Evidence

