# M8 T16: ODoH in the management plane: API, key push, snapshot

Status: open
Created: 2026-09-15

## Description

Implements Task 16 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/api/odoh_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api -run TestOdohSettingsAPI -count=1; go test ./mgmt/internal/control -run TestHubOffersOdohKeysOnNotify -count=1'`
- [x] Implement:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api ./mgmt/internal/control ./mgmt/internal/snapshot -count=1 && go vet ./mgmt/...'`
- [ ] Report the paths. The lead commits `M8 T16: ODoH settings API, key push and snapshot config`.

## Evidence

- Red (private copy /work/m8-t16red, hub.go and snapshot.go at HEAD, odoh_service.go, keys_odoh.go and
  snapshot/odoh.go removed): `go test ./mgmt/internal/api -run TestOdohSettingsAPI` -> build failed,
  `undefined: api.NewODoHService`; `go test ./mgmt/internal/control -run TestHubOffersOdohKeysOnNotify`
  -> `h.ODoH undefined`, `undefined: control.ODoHKeyLoader`.
- Green (toolbox-m7, /work/nexora): `--- PASS: TestOdohSettingsAPI (3.83s)`;
  `ok github.com/piwi3910/nexora/mgmt/internal/control 3.869s`; `ok .../mgmt/internal/odoh 6.683s`.
- Mutations (private copy /work/m8-t16mut), each killed: no `rotateOdohKeyScheduled` write in
  `Keys.Rotate` -> `odoh_test.go:242: scheduled rotation audit rows: []`; no digest check in
  `offerOdohKeys` -> `keys_odoh_test.go:121: an engine was sent an empty key set`; `AddOdoh` not
  setting `snap.Odoh` -> `odoh_test.go:209: snapshot odoh <nil>`.
- Full: `go test ./mgmt/internal/api ./mgmt/internal/control ./mgmt/internal/snapshot -count=1` in
  /work/nexora -> control ok 97.2s, snapshot ok 30.1s, api FAIL only in Task 14's in-progress
  `TestZonemdAPI` and `TestCatalogMembershipRules` (`zonemd_api_test.go`); api in a private copy
  without that file -> `ok github.com/piwi3910/nexora/mgmt/internal/api 108.706s`.
  `go vet ./mgmt/...` -> rc 0. gofmt -l on the touched packages: clean.
- Commit pending (the lead commits).
