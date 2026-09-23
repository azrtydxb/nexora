# M8 T9: ODoH settings and keys in the management plane

Status: open
Created: 2026-09-15

## Description

Implements Task 9 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/odoh/odoh_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/odoh -count=1'` and expect FAIL: the package
- [x] Implement `settings.go`:
- [x] Implement `keys.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/odoh -count=1 && go vet ./mgmt/internal/odoh'`
- [ ] Report the paths. The lead commits `M8 T9: ODoH settings and rotating sealed keys`.

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test ./mgmt/internal/odoh -count=1'` ->
  `odoh_test.go:38:8: undefined: Keys` ... `FAIL ... [build failed]`.
- Green: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test ./mgmt/internal/odoh -count=1 && go vet ./mgmt/internal/odoh'`
  -> `ok github.com/piwi3910/nexora/mgmt/internal/odoh 13.000s`, vet clean; `gofmt -l mgmt/internal/odoh` empty.
  Tests: `TestOdohKeyRotation`, `TestValidateOdohSettings`, `TestOdohSettingsRevision` (added: GetSettings,
  UpdateSettings revision bump and stale-revision `store.ErrConflict`).
- Mutation: expired-key delete disabled (`not_after <= $1 - interval '1000 days'`) -> `TestOdohKeyRotation` FAIL;
  reverted -> PASS.
- Commit pending (lead commits).
