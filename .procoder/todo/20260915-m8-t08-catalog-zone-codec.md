# M8 T8: Catalog zone codec

Status: open
Created: 2026-09-15

## Description

Implements Task 8 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/catzone/codec_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/catzone -count=1'` and expect FAIL: the
- [x] Implement `codec.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/catzone -count=1'` and expect PASS.
- [ ] Report the paths. The lead commits `M8 T8: catalog zone codec`.

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test ./mgmt/internal/catzone -count=1'` with only codec_test.go -> `FAIL github.com/piwi3910/nexora/mgmt/internal/catzone [build failed]` (undefined: Label, Parse, Member).
- Green: same command after codec.go -> `--- PASS: TestCatalogBuildIsStable`, `--- PASS: TestCatalogParseBrokenRules`, `ok github.com/piwi3910/nexora/mgmt/internal/catzone 0.006s`.
- `go vet ./mgmt/internal/catzone` (pod) clean; `gofmt -l` empty; `golangci-lint run ./mgmt/internal/catzone/...` (laptop) -> `0 issues.`
- Commit pending: the lead commits `M8 T8: catalog zone codec`.
