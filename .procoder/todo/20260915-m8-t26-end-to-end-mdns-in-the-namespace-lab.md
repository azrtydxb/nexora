# M8 T26: End-to-end mDNS in the namespace lab

Status: open
Created: 2026-09-15

## Description

Implements Task 26 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create `e2e/mdns_test.go`:
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e -run "TestMdnsGatewayNetLab|TestMdnsReflectorNetLab|TestMdnsOffForwardsLocalNames" -count=1 -v'`
- [ ] Report the paths. The lead commits `M8 T26: e2e mDNS gateway and reflector in the namespace lab`.

## Evidence
