# M7 T20: The Compose example on novanas (#5)

Status: open
Created: 2026-09-14

## Description

Implements Task 20 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] In `TestComposeExample`, append:
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./deploy/deploytest -run TestComposeExample'` and expect FAIL on the docs assertion.
- [ ] Create `scripts/compose-verify.sh` (mode 0755). It runs the documented commands verbatim on the host, with the documented `.env` settings changed:
- [ ] Run `scripts/compose-verify.sh piwi@192.168.10.211` from the laptop, after `images.yml` has pushed `sha-<7>` for the commit being verified.
- [ ] Update `docs/operations.md`:
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./deploy/deploytest/...'` and expect PASS. Run `ssh piwi@192.168.10.211 'docker ps -a --format "{{.Names}}"; ls -d ~/nexora-compose-verify 2>&1'` and expect no `nexora-verify` containers and `No such file or directory`.

## Evidence

