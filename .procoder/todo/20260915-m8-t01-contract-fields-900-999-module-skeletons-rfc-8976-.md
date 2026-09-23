# M8 T1: Contract fields 900–999, module skeletons, RFC 8976 vectors, architecture text

Status: done
Created: 2026-09-15

## Description

Implements Task 1 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/control/contract_m8_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/control -run "TestContractM8|TestSnapshotCannotReachOdohKeys" -count=1'`.
- [x] Edit `control.proto`:
- [x] Regenerate in the dev pod and copy back:
- [x] Create the engine skeletons:
- [x] In `engine/src/control.rs`, extend the no-op arm to
- [x] Create `e2e/testdata/rfc8976/a1.zone`, `a2.zone` and `a3.zone`:
- [x] Create `e2e/testdata/rfc8976/genhex/main.go`:
- [x] Run `scripts/dev-exec.sh 'go run ./e2e/testdata/rfc8976/genhex e2e/testdata/rfc8976'` and
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/control -run "TestContractM8|TestSnapshotCannotReachOdohKeys" -count=1 && cargo build --locked -p nexora-engine --all-targets'`
- [x] Edit `docs/architecture.md`:
- [x] Run `grep -c '900-999' docs/architecture.md; grep -c 'odoh_keys' docs/architecture.md; grep -c 'zones/catalogs' docs/architecture.md`
- [x] Report the paths under Files. The lead commits

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test ./mgmt/internal/control -run "TestContractM8|TestSnapshotCannotReachOdohKeys" -count=1'` -> compile failure (`unknown field ZonemdVerify in struct literal`, `undefined: controlv1.OdohProxyTarget`).
- Regenerated with pod protoc 3.21.12 (same as committed header), copied back with `kubectl ... tar -C /work/nexora -cf - gen/go`.
- Green: same test -> `ok github.com/piwi3910/nexora/mgmt/internal/control 0.011s`.
- `go run ./e2e/testdata/rfc8976/genhex e2e/testdata/rfc8976` -> `a1 6 records`, `a2 21 records`, `a3 10 records`; `.hex` copied back.
- `cargo build --locked -p nexora-engine --all-targets` first failed on three struct literals missing the new prost fields (rpz manager status, transfer_tests, snapshot_m3); fixed, then clean. `cargo fmt --all -- --check` OK; `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` clean.
- `grep -c` counts on docs/architecture.md: `900-999` 1, `odoh_keys` 3, `zones/catalogs` 1; `scripts/pc-format.sh docs/architecture.md` made no change.
- Not committed (the lead commits).
