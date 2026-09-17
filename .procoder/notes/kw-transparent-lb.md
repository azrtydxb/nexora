# Dedicated transparent failover-group LB proof

## Status

User-approved implementation direction: first-class failover groups, each with its
own IP, two engines and redundant dedicated transparent load balancing. Initial
kw IPs remain `.136` and `.139`; Cilium VXLAN is not being migrated. Specification
and plan: `../specs/nexora-failover-groups.md` and
`../plans/nexora-failover-groups.md`.

**Basic isolated IPVS Direct Routing proof passed. Product implementation remains
unfinished.** No group database/API/UI or redundant LB controller has been deployed.
IPVS Direct Routing is direct-server-return at the dedicated LB layer; it is not
the rejected cluster-wide Cilium DSR configuration change.

## Experiment on 2026-09-17

Host: kw dev `worker-21` (`192.168.10.104`), ARM64 kernel
`6.12.58-current-rockchip64`. Kernel configuration reports `CONFIG_IP_VS=m` and
IPVS modules are available. Downloaded/extracted the distro `ipvsadm` package under
`/tmp/nexora-fg-proof/package`, **not** installed as a host service. Existing kernel
modules may be loaded by IPVS tooling; no module unloading or host IPVS-table flush.

The probe binary was built in the existing Linux toolbox using the repository's
Go dependencies. Five isolated network namespaces contained:

- LB namespace and internal bridge: `198.18.0.2`; sole advertising frontend
  `198.18.0.100`.
- Backend namespaces: `.3` and `.4`, each with `.100/32` on loopback and scoped ARP
  suppression. DNS/TLS/QUIC fixture listeners run inside these namespaces.
- Client namespaces: `.10` and `.11`.

All addresses belong to the isolated benchmark subnet `198.18.0.0/24`. No veth,
bridge, route or address was connected to the host LAN. No existing VIP, Cilium
configuration, engine, Service, identity or persistent state was modified.
IPVS services exist only inside the owned LB namespace; backend selection uses
round-robin Direct Routing (`-g`), without NAT or application proxying.

Two clients each sent two rounds of UDP/53, TCP/53, DoT TCP/853, HTTP/2 DoH TCP/443
and DoQ UDP/853. Each query had a five-second deadline and no application retry or
transport fallback. Lab-only TLS root/name verification remained enabled. Backend
TXT answers contain the observed source IP; backend logs also record source port.

Result: **20 successful transactions**. Both backends served all five transports.
Client and backend packet captures were compared as transport/IP/port flow tuples:

```text
PASS: 20 distinct client-to-VIP flows match backend source/destination IP and port exactly.
PASS: reverse flows reach clients from the group VIP and original service port.
PASS: both backends served all five transports.
```

The tuple comparison establishes address/port preservation for these sampled flows,
not a packet-loss count or payload-equivalence proof. Protocol clients independently
validated DNS answers and encrypted connections.

## Failed setup/evidence checks retained

- First launch failed because the copied test binary lacked executable permission;
  the fixture never served queries. Fixed mode on that test binary only.
- First successful 20-query batch had incomplete packet files: tcpdump readers
  were stopped before consuming their buffered capture rings. Tuple verifier
  rejected the capture (only four distinct flows). Kept that batch separately;
  added immediate capture mode and a bounded reader-drain interval, then executed
  a new 20-query batch. The passing packet result refers only to the second batch.
- No DNS failure was converted to success by query retries.

## Evidence and cleanup

Local artifacts: `/tmp/nexora-fg-proof/{main.go,run.sh,verify.py,build.log,run.log,
backend-a.log,backend-b.log,packets-a.txt,packets-b.txt,packets-c.txt,packets-d.txt,
packet-verification.txt,kw-postproof.log}`. Remote directory at the same path on
worker-21 additionally contains PCAPs and preserved `batch1/` plus
`run-permission-failure.log`. Lab private keys remain outside the repository.

The shell fixture checks for existing namespace names before creation and tracks
only namespaces it creates. Its cleanup terminates processes in those namespaces
and removes them. Post-run inspection found no `nxfg-*` namespaces or host links;
paired kw preflight passed afterward. Existing retained rollout lock was untouched.
The experiment used temporary prototypes, not a shipped or reusable production
controller. The follow-up below promotes the isolated fixture into the repository;
T1 still requires cross-host and actual-engine attachment proof.

## Review and repository verification

Adversarial review: the tuple verifier compares sets, not packet counts, and a
single-host bridge cannot establish cross-host LAN or LB-HA behavior. The result
is therefore explicitly limited to sampled transparency, not lossless forwarding
or failover acceptance. Temporary fixture shell syntax and ShellCheck passed.
`procoder test` was run: laptop-wide suite remains red with 231 Go failures
(including Helm render tests) and Rust `libc::mmsghdr` compilation failure. These
are separate from the successful Linux-host forwarding experiment.

## Repository harness follow-up

`deploy/failoverlab/` now contains a reusable opt-in namespace runner, five-transport
probe and regression-tested tuple verifier. Runs have unique namespaces/evidence
paths and generate then remove their own lab key. HTTP/2 and DNS question/ID/rcode
checks are explicit; redirects are rejected. The verifier correlates backend
socket ports with captures and rejects incomplete or kernel-dropped captures.

Final worker-21 results: 20 DR transactions and all five source-loss negative
controls passed. `/tmp/nexora-failoverlab-final.log` records both. Remote evidence:
`/tmp/nexora-failoverlab.IAqM3pXo` (DR),
`/tmp/nexora-failoverlab.Af6SBY8i` (negative). No `fg-*` namespaces remained;
paired preflight passed (`/tmp/nexora-failoverlab-postpreflight.log`). No existing
VIP, engine, Cilium configuration or retained lock was changed.

A failed negative-control implementation is retained:
`/tmp/nexora-failoverlab-snat.log`, remote `/tmp/nexora-failoverlab.1YnVTFit`.
The attempted IPVS NAT + LB POSTROUTING rule did not rewrite UDP, and the harness
correctly failed with `negative control incorrectly accepted udp`. It is not a
passing fault-injection result. The corrected control explicitly injects SNAT
inside the client namespace (`.10` to `.12`) before the same DR path. Each
transport must receive the specific source mismatch; timeouts/TLS errors cannot
satisfy the negative assertion. This tests the detector, not deployed LB NAT.

Linux race probe/model tests passed (1.029s/6.359s); seven Python verifier tests
passed even with `python -O`; ShellCheck passed. Log:
`/tmp/nexora-failoverlab-reviewed-linux.log`. Adversarial review also hardened
cleanup to attempt every owned namespace and key removal despite deletion errors.
The full laptop Procoder suite remains red: 233 Go failures and Rust `mmsghdr`.
No successful forwarding test closes HA, cross-host or product acceptance.

## Remaining gates

- Cross-host forwarding and actual engine attachment: current Cilium pod interfaces
  are not the tested L2 backend network. Validate secondary attachment, return-path
  selection, ARP suppression and co-hosted group isolation before selecting adapter.
- LB HA/election/fencing, health checks, drain/re-admission, backend/LB abrupt failure
  and partition behavior; no failover claim follows from this forwarding test.
- More than tiny replies, MTU and
  fragmentation, reused sessions, packet contents and actual Nexora policy/log
  attribution with multiple client identities.
- API/OpenAPI, GUI, controller and guarded migration. Desired-state persistence
  exists at `39f1b50`, but is not deployed (see `failover-model.md`).
- Strict acceptance and bootstrap/control-plane fixes remain separate unfinished
  work. Earlier failed live failover/CNI-migration evidence is not superseded by
  this isolated forwarding success.
