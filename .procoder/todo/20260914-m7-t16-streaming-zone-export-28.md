# M7 T16: Streaming zone export (#28)

Status: open
Created: 2026-09-14

## Description

Implements Task 16 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create `mgmt/internal/zone/export_test.go`:
- [ ] Create `mgmt/internal/api/zonefile_test.go`: `TestExportZoneFileStreams`.
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/zone -run TestExportToIsStableAndReparses; go test -count=1 ./mgmt/internal/api -run TestExportZoneFileStreams'`. Expect a compile FAIL `s.ExportTo undefined` for the first. For the second, expect FAIL `ContentLength = <n>, want -1`.
- [ ] In `zonefile/export.go`, extract the per-record line writing of `Export` into `Writer` (header `$ORIGIN`/`$TTL`, `SOA`, `Record` with the relative owner names and presentation `rdataText` Export already uses, `Flush`). `Export` sorts as before and writes through a `Writer`; `TestExportIsStableAndRep
- [ ] In `zone/import.go`, replace `Export` (delete the `debt:` comment) with `ExportTo`. Keep `Export(ctx, zoneID, w)` as a one-line wrapper if other callers use it.
- [ ] In `api/zonefile.go`, keep the `GetZone` 404 check. Then stream:
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/zone/... ./mgmt/internal/zonefile/... ./mgmt/internal/api/... && make e2e-build && go test -count=1 ./e2e -run TestZoneFileRoundTrip'` and expect all to pass.

## Evidence

