# M8 T22: Catalog zone API and management plane wiring

Status: open
Created: 2026-09-15

## Description

Implements Task 22 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/api/catalog_zones_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api -run TestCatalogZonesAPI -count=1'` and
- [x] Implement the adapter:
- [x] In `main.go`, construct:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api -count=1 && go build ./mgmt/cmd/nexora-mgmt && go vet ./mgmt/...'`
- [ ] Report the paths. The lead commits `M8 T22: catalog zone API and M8 service wiring`.

## Evidence

Tests ran in pod toolbox-m7 in a private copy `/work/m8-t22` (wave-3 in-progress files of other tasks at HEAD), `GOFLAGS=-buildvcs=false`.

- Red: `go test ./mgmt/internal/api -run "TestCatalogZonesAPI|TestImportRefusesProducerCatalog" -count=1` with a nil adapter: `catalog_zones_test.go:53: producer without transfer CIDRs: 501`. With the adapter and `zone/import.go` at HEAD: `TestCatalogZonesAPI` passes, `TestImportRefusesProducerCatalog` fails `import into a producer catalog: 200 map[records_imported:2 ...]`.
- Green: `gofmt -l mgmt` (empty); `go test ./mgmt/internal/api ./mgmt/internal/odoh ./mgmt/internal/zone -count=1`: `ok api 342.279s`, `ok zone 104.774s`, odoh first failed on a created_at tie in the new audit assertion (test clock fixed by advancing a minute), rerun `ok odoh 11.020s`; `go build -o /work/bin-m8-t22/nexora-mgmt ./mgmt/cmd/nexora-mgmt && go vet ./mgmt/...`: OK.
- Mutations: forced rotation without its audit row -> `odoh_test.go:96: forced rotation audit: "" "" no rows in result set`; adapter without the 400 validation mapping -> `catalog_zones_test.go:53: producer without transfer CIDRs: 422`; import without `checkRecordsWritable` -> 200 (the red run above).
