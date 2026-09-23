# M8 T10: Network namespace lab and mDNS fixture

Status: open
Created: 2026-09-15

## Description

Implements Task 10 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `e2e/harness/netlab_test.go`:
- [x] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e/harness -run TestNetLabVethMulticast -count=1 -v'`
- [x] Implement `netlab.go`:
- [x] Implement `mdns.go` with `golang.org/x/net/ipv4` and `ipv6` packet conns:
- [x] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e/harness -run TestNetLabVethMulticast -count=1 -v'`
- [ ] Report the paths. The lead commits `M8 T10: network namespace lab and mDNS fixture`.

## Evidence

Pod toolbox-m7 (securityContext adds NET_ADMIN; `unshare -Urn` works without a pod change; /work is PVC `work-m7`).

- Red: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test ./e2e/harness -run TestNetLabVethMulticast -count=1 -v'`
  → `FAIL github.com/piwi3910/nexora/e2e/harness [build failed]` (`undefined: InNetLab`, `e.NewNetLab undefined`, `e.RunBin undefined`).
- Green (private copy `/work/m8-t10` without Task 11's in-progress `e2e/harness/odoh*.go`, which did not compile;
  fixture only, instead of the shared `make e2e-build`):
  `go build -o /work/bin-m8-t10/nexora-fixture ./e2e/fixtures/cmd/nexora-fixture && NEXORA_E2E_BIN_DIR=/work/bin-m8-t10 go test ./e2e/harness -run TestNetLabVethMulticast -count=1 -v`
  → `--- PASS: TestNetLabVethMulticast (1.98s)` / `ok github.com/piwi3910/nexora/e2e/harness 2.012s`.
- Mutation: responder answering every name → `FAIL ... an unknown name was answered`.
- Ad-hoc lab run: non-legacy multicast query (from :5353) → `ANSWER printer.local. 120 IN A 10.254.0.9`, `PACKETS 1`;
  legacy TXT → `PACKETS 1`; responder logged `READY 10.254.0.2`, `GOT 10.254.0.1:5353 printer.local. A`.
- `go vet ./e2e/harness ./e2e/fixtures/cmd/nexora-fixture` clean; `gofmt -l e2e` empty.
