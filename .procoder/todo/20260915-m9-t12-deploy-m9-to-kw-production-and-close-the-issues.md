# M9 T12: Deploy M9 to kw production and close the issues

Status: open
Created: 2026-09-15

## Description

Implements Task 12 of `.procoder/plans/nexora-m9-platform.md` (spec `.procoder/specs/nexora-m9-platform.md`, milestone M9 Platform). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Confirm the production Helm render is unchanged: run
- [ ] After the lead has committed Tasks 1–11, run `make e2e` and `make operator-test` in the dev pod
- [ ] Start the DNS probe, then run `scripts/kw-deploy.sh`, which runs 5 queries/s to 192.168.10.136 and
- [ ] Run `scripts/kw-acceptance.sh` and expect PASS for `TestKwSmoke`, `TestKwFullProduct` and
- [ ] Verify that `kubectl --context kw -n nexora get nexorainstallations` prints `No resources found` and
- [ ] Report the paths and results. The lead closes #37 and #41 with the commit and the test names

## Evidence

