# M8 T34: Deploy M8 to kw and run acceptance

Status: open
Created: 2026-09-15

## Description

Implements Task 34 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create `e2e/kw_smoke_m8_test.go` with `TestKwSmokeM8`. It is gated like `TestKwSmokeM4` (the same
- [ ] Add `TestKwSmokeM8` to the `go test -run` list in `scripts/kw-acceptance.sh`.
- [ ] Run `scripts/dev-exec.sh 'go vet ./e2e/...'` and expect a clean result.
- [ ] Build and push the images for the M8 head with `scripts/build-image.sh`. Run
- [ ] Run `scripts/kw-acceptance.sh` and expect `TestKwSmoke`, `TestKwFullProduct`,
- [ ] Close #30, #31, #32 and #33, each with a comment naming the commit, the proving tests (the spec's
- [ ] Report the deploy output summary, the probe counts and the issue URLs.

## Evidence
