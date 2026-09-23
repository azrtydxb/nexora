# M8 T23: End-to-end ZONEMD for primaries, secondaries and RPZ

Status: open
Created: 2026-09-15

## Description

Implements Task 23 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `e2e/zonemd_test.go` with `TestZonemdGeneratedForPrimaryZones` and
- [x] Create `e2e/rpz_zonemd_test.go` with `TestRPZZonemdVerification`:
- [x] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e -run "TestZonemdGeneratedForPrimaryZones|TestZonemdSecondaryVerification|TestRPZZonemdVerification" -count=1 -v'`
- [ ] Report the paths. The lead commits `M8 T23: e2e ZONEMD for primaries, secondaries and RPZ`.

## Evidence

- Binaries built privately in toolbox-m7: `CARGO_TARGET_DIR=/work/target-m8-t23 cargo build --locked --release -p nexora-engine`, `go build` of nexora-mgmt and nexora-fixture into `/work/bin-m8-t23` (log `/work/log-m8-t23-build.txt`).
- `NEXORA_E2E_BIN_DIR=/work/bin-m8-t23 go test ./e2e -run "TestZonemdGeneratedForPrimaryZones|TestZonemdSecondaryVerification|TestRPZZonemdVerification" -count=1 -v`: `--- PASS: TestRPZZonemdVerification (43.04s)`, `--- PASS: TestZonemdGeneratedForPrimaryZones (4.46s)`, `--- PASS: TestZonemdSecondaryVerification (4.77s)`, `ok github.com/piwi3910/nexora/e2e 52.300s`.
- First run failed on two assumptions of the plan text, fixed in the tests: the record API refuses ZONEMD with 422 `unsupported_type` (not 400); a never-loaded secondary reports serial 1, so the plain primary uses serial 7.
- Mutation check (private copy, mgmt ignoring a failed verification and skipping `zonemd.Apply`): `zonemd_test.go:236: ldns-verify-zone -Z: exit status 112` and `zonemd_test.go:279: condition not met within 30s: zone {... Serial:2018031901 ... ZonemdStatus:failed ...}`; both FAIL.
- ldns-verify-zone -Z reports `No ZONEMD matching the zone data was found` (exit 112) on an ldns-signzone zone with one owner changed, so the oracle detects a stale digest.
- `gofmt -l e2e` clean, `go vet ./e2e` clean.
