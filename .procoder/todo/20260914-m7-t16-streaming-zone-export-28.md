# M7 T16: Streaming zone export (#28)

Status: open
Created: 2026-09-14

## Description

Implements Task 16 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/zone/export_test.go`:
- [x] Create `mgmt/internal/api/zonefile_test.go`: `TestExportZoneFileStreams`.
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/zone -run TestExportToIsStableAndReparses; go test -count=1 ./mgmt/internal/api -run TestExportZoneFileStreams'`. Expect a compile FAIL `s.ExportTo undefined` for the first. For the second, expect FAIL `ContentLength = <n>, want -1`.
- [x] In `zonefile/export.go`, extract the per-record line writing of `Export` into `Writer` (header `$ORIGIN`/`$TTL`, `SOA`, `Record` with the relative owner names and presentation `rdataText` Export already uses, `Flush`). `Export` sorts as before and writes through a `Writer`; `TestExportIsStableAndRep
- [x] In `zone/import.go`, replace `Export` (delete the `debt:` comment) with `ExportTo`. Keep `Export(ctx, zoneID, w)` as a one-line wrapper if other callers use it.
- [x] In `api/zonefile.go`, keep the `GetZone` 404 check. Then stream:
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/zone/... ./mgmt/internal/zonefile/... ./mgmt/internal/api/... && make e2e-build && go test -count=1 ./e2e -run TestZoneFileRoundTrip'` and expect all to pass.

## Evidence

All commands ran in the second dev pod (`NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh`).

- Red, before the implementation: `go test -count=1 ./mgmt/internal/zone -run TestExportToIsStableAndReparses; go test -count=1 ./mgmt/internal/api -run TestExportZoneFileStreams`
  - `mgmt/internal/zone/export_test.go:31:15: s.ExportTo undefined (type *zone.Service has no field or method ExportTo)` / `FAIL ... [build failed]`
  - `--- FAIL: TestExportZoneFileStreams (9.35s)  zonefile_test.go:52: ContentLength = 3240, want -1`
- Green, same command after the change: `ok github.com/piwi3910/nexora/mgmt/internal/zone 8.279s`, `ok github.com/piwi3910/nexora/mgmt/internal/api 7.724s`; `go vet` on the three packages clean, `gofmt -l` clean.
- `go test -count=1 ./mgmt/internal/zone/... ./mgmt/internal/zonefile/... ./mgmt/internal/api/... && make e2e-build && go test -count=1 ./e2e -run TestZoneFileRoundTrip`:
  - `ok .../mgmt/internal/zone 25.491s`, `ok .../mgmt/internal/zonefile 0.030s` (golden `TestExportIsStableAndReparses` unchanged), `ok .../mgmt/internal/api 68.885s`
  - `--- PASS: TestZoneFileRoundTrip (3.94s)`, `ok github.com/piwi3910/nexora/e2e 3.956s`
- Deviations (plan text updated): the API test's one record is a >2 KiB TXT, since net/http sets `Content-Length` itself for bodies under its 2 KiB buffer; `Parse` uses `ManagedTypes`/`MaxImportRecords` options; no `Export` wrapper (no other caller); row decoding inline in `import.go` (`service.go` belongs to Task 17).
- Not committed (lead commits).
