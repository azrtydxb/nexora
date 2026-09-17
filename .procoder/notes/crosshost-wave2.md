# FG-01 cross-host implementation — wave 2

Implemented an opt-in local-per-host supervisor in `deploy/failoverlab/crosshost/`.
No AGENTS.md was present in this worktree. Read parallel-integration checkpoint,
failover sprint plan, same-host runner/probe/verifier, engine bootstrap/main,
snapshot validation and graceful-shutdown fixture. No SSH, cluster mutation,
shared toolbox, live experiment, deployment, trust regeneration, commits or task
closure occurred. Existing same-host behavior and verifier are unchanged.

Two co-hosted groups each have separate namespaces, UDP port, VNI and Unix
socketpair. Clients and backends are explicitly split left/right. Kernel VXLAN
sockets originate in switch namespaces, while exclusive fixed-peer host UDP
sockets relay encapsulated frames without touching host links/routes. Private
VIPs never attach to LAN devices. This userspace relay is an experimental
transport boundary, not production attachment acceptance.

Implemented strict offline plans, collision refusal, namespace-local IPVS DR,
backend ARP suppression, no default routes, per-host source-loss controls,
duplicate-advertisement controls, exact ownership cleanup, process supervision,
PCAP/socket/ARP/command evidence and two-host merge using the unchanged five-
transport verifier. Probe attempts are single-use and failures stop the run.
Source-loss addresses differ by host to avoid ambiguous return ARP. Switch MACs
differ across hosts; expected frontend MACs differ across groups. Evidence is
never reused; positive verification requires successful supervisor completion,
original probes, cleanup and existing tuple/drop/socket assertions.

Offline validation on macOS:

- `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s deploy/failoverlab/crosshost -v`
- `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s deploy/failoverlab -v`
- `python3 deploy/failoverlab/crosshost/lab.py validate deploy/failoverlab/crosshost/plan.example.json`
- `git diff --check`

All passed at implementation review. Linux namespace/IPVS/VXLAN/ARP/relay execution
has **not** been run; parent alone runs that validation and integration. No Go,
PostgreSQL or Rust suite was attempted, so no unsupported platform pass is claimed.

Actual engine remains unresolved, deliberately no echo-as-policy claim. Verified
real launch is `nexora-engine --config FILE`, strict bootstrap TOML, protobuf
ConfigSnapshot plus validated blobs, explicit standalone TLS/listeners and isolated
state. Follow-up must generate a valid disposable policy snapshot and verify
client-dependent responses and actual engine logs; current echo verifier cannot
assert those semantics. Do not use production trust/identities or management URLs.

Other open gates: supported Linux execution, live hostile cross-group injection,
PMTU/large signed replies/fragmentation, source-selected multi-route return,
independent physical-host identity evidence, frontend HA/fencing, partitions,
established sessions and performance. Duplicate-ARP live controls and offline VNI
rejection are implemented but are not proof of all cross-group policy isolation.
No FG-01 exit or release readiness claim is made. Exact parent run and cleanup
commands, requirements and failure handling are in crosshost/README.md.
