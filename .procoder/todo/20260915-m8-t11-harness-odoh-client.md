# M8 T11: Harness ODoH client

Status: open
Created: 2026-09-15

## Description

Implements Task 11 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `e2e/harness/odoh_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./e2e/harness -run TestODoHConfigParsingAndKeyID -count=1'`
- [x] Run `go get github.com/cloudflare/circl@v1.6.5` on the laptop. Implement `odoh.go` with
- [x] Run `scripts/dev-exec.sh 'go test ./e2e/harness -run TestODoHConfigParsingAndKeyID -count=1 && go vet ./e2e/harness'`
- [ ] Report the paths. The lead commits `M8 T11: harness ODoH client (circl HPKE)`.

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test ./e2e/harness -run TestODoHConfigParsingAndKeyID -count=1'`
  -> `undefined: ParseODoHConfigs`, `undefined: hkdfKeyID`, `FAIL ... [build failed]`.
- `go get github.com/cloudflare/circl@v1.6.5 && go mod tidy` (laptop): go.mod gains only
  `github.com/cloudflare/circl v1.6.5` (direct require); go.sum gains only its two lines.
- Green: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test ./e2e/harness -run TestODoHConfigParsingAndKeyID -count=1 && go vet ./e2e/harness'`
  -> `ok github.com/piwi3910/nexora/e2e/harness 0.021s`, vet exit 0. `gofmt -l` clean.
- Extra (not committed): a throwaway round-trip test in a private pod copy (`/work/m8-t11`, removed)
  decrypted `ODoHQuery`'s message with a circl HPKE receiver and answered with an RFC 9230 §6.4
  sealed response carrying 5 zero padding octets -> `--- PASS: TestScratchODoHRoundTrip`.
  Interop against the engine remains Task 24.
- Commit: pending (the lead commits `M8 T11: harness ODoH client (circl HPKE)`).
