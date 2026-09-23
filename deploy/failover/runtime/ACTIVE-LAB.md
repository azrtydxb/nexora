# Isolated active bridge candidate — parent execution pending

This is an opt-in review candidate, not production activation, FG04/FG05 completion,
engine eligibility, or HA acceptance. The detached harness remains a separate
first parent run. The frozen pre-followup archive the first run intended to keep was never
written, so the sources are only those in this tree at the commit that adds this file. Run the
harness from a separate trusted installation; do not overlay it onto subsequent work.

The new `active_lab.py` creates fresh private mount/network/PID isolation, a fresh
nested network namespace, six addressless reciprocal local veths and a real
loopback-only API server. It cannot accept existing namespaces, links, production
manifests or external peers. It assigns no frontend or serving IP address. A
synthetic local route for `198.18.100.53/32` feeds a single UDP/53 IPVS service;
`198.18.101.2/32` and a permanent neighbor route DR frames to the addressless
backend receiver. These routes/services exist only inside the new lab namespace.
No engine or real management service is launched.

The existing platform Adapter remains strict and unchanged. A separate
`platform/active_lab.py` owns this new manifest/state machine:

1. Create all links DOWN; record exact namespace inode identity, interface indices,
   reciprocal peers, MACs and run identity. Hash that complete manifest into exact
   fence aliases. Both the adapter and driver use those aliases unchanged. The
   management attachment is `mg0/mp0`, distinct from `fg0/fp0` and `bg0/bp0`.
2. Reject extra/addressed/replaced links, external peers, masters, XDP, TCX,
   foreign classic TC/qdiscs, netfilter and effective inherited cgroup BPF hooks,
   unsupported offload state, foreign routes/rules/IPVS state. Segmentation and
   receive coalescing subfeatures are disabled and rechecked, as is hardware TC.
   Veth software checksum metadata is permitted; physical/NIC offload is unsupported. Only a visible
   cgroup-v2 hierarchy is supported. Unsupported utility/schema/query results fail.
3. One driver loads one fresh program and one fresh map, then installs that SAME
   program on `fg0` and `bg0` while both remain DOWN. Empty map means DENY. Only
   after both attachments read back the same program ID can it emit `READY2`.
   Partial install retains the first empty-map hook, keeps both links DOWN, and
   never emits readiness. No adoption, replacement, pins, detach or qdisc deletion.
4. The exact paired Go DriverGate acknowledges DENY, then the adapter validates
   both hooks and raises both links. A failure at any boundary terminates before
   CAS; an already-UP link is still denied. The adapter never arms a window.
5. The existing controller quarantines and performs CAPTURE before its real HTTPS
   CAS. ARM uses that immutable absolute capture/deadline, never now-plus-TTL.
   One map replacement governs both packet hooks; these are not two independently
   armed gates or a claim of simultaneous packet processing. A packet already past
   a hook is outside that decision. No renewal occurs in this bounded fixture.

The frontend hook gates advertisements and outgoing data. The backend-facing
frontend hook gates DR transmission even when a client keeps sending to the old
frontend MAC. Management frames use their independent ungated pair. This does
not gate a backend's eventual direct reply after an earlier accepted request;
backend eligibility, existing sessions and post-gate drain remain open requirements.

## Trust and privilege boundary

**Privileged helper / unprivileged owner separation remains unsupported.** This
candidate requires an explicitly invoked, trusted root lab owner. There is no
setuid service, socket broker, file-capability grant or ambient activation path.
Root ownership of immutable code/ancestors is checked separately from caller UID;
nonroot LaunchDriver callers are now refused. Install the driver, object,
interpreter, scripts/imports and owner under canonical administrator-owned paths.
A tenant-owned helper/object or writable ancestor is not supported. Root can still
modify root-owned code; immutable here is a trusted-admin deployment assumption,
not protection against hostile root or a writable root mount.

The harness invokes `/usr/bin/setpriv` **before Go starts**, removing CAP_NET_RAW
from the bounding set and all inheritable/ambient capabilities, and setting
no-new-privileges. The owner checks CAP_NET_RAW absence in every thread's effective,
permitted, inheritable, bounding and ambient sets. Its driver and activation child
inherit that restriction. The separate parent packet probe is trusted test
instrumentation and has raw-socket privilege; it never sets PACKET_QDISC_BYPASS.
The root owner still has privileges needed for this explicit lab. It is not an
unprivileged controller security boundary.

Exclusive trusted administration must prevent topology/hook replacement after
inventory, raw/qdisc-bypass senders, BPF/map mutation, offload changes, AF_XDP,
AF_PACKET bypass, alternative transmit paths and module changes. Inventory is not
atomic attestation against another privileged writer. Driver exchanges use private
bounded pipes, one outstanding ticket, exact acknowledgements and terminal poison.
Dual-driver protocol errors clear its shared map before rejection when possible;
uncertain deletion still relies on absolute expiry. Owner death/EOF retains hooks.
No lease reservation, deployment lock or cooperative lock is released.

## Parent-only execution

Nothing in this document authorizes this lane to run Linux, kernel, API-server,
SSH or toolbox operations. These commands are for the parent on its disposable
supported Linux host, after the separately frozen detached run. Use a canonical
trusted installation such as `/opt/nexora-lab`, with root-owned non-writable
ancestors. The source path `/tmp/...` deliberately fails runtime trust checks.

Prerequisites: the detached harness tools, real etcd/kube-apiserver/OpenSSL,
preloaded veth/IPVS/RR, supported BPF boottime helper/classic TC and TCX query,
`/usr/sbin/{ip,tc,sysctl,ethtool,ipvsadm,nft,bpftool}`, `/usr/bin/{unshare,mount,setpriv}`,
visible cgroup-v2 without effective BPF programs, private mount propagation and
zero-offset boot/time namespaces. No packages/modules are installed by the runner.
Tools, Python stdlib and dynamic libraries are part of the trusted installation.

```sh
make -C deploy/failover/fence all fence-driver-partial
GOPROXY=off GOSUMDB=off go build -o /opt/nexora-lab/fg-active-lab ./deploy/failover/runtime/cmd/fg-active-lab
```

Run each scenario separately, retaining complete output/exit status and kernel,
source, object, binary and utility versions. Replace only trusted tool paths:

```sh
sudo /usr/bin/python3 -I -B /opt/nexora-lab/deploy/failover/runtime/active_lab.py \
  --execute --scenario expiry --executable /opt/nexora-lab/fg-active-lab \
  --driver /opt/nexora-lab/deploy/failover/fence/fence-driver \
  --object /opt/nexora-lab/deploy/failover/fence/gate.bpf.o \
  --etcd /opt/lab-tools/etcd --apiserver /opt/lab-tools/kube-apiserver \
  --openssl /usr/bin/openssl
```

Repeat the command with `stop`, `death`, `delayed`, `partial`, and `frontend-only`.
Run `attach-partial` with `--driver .../fence-driver-partial` (the deliberately
faulted build). The ordinary owner never selects fault code.

| Scenario       | Required real behavior                                                                                                                                                       |
| -------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| expiry         | Initial frontend/DR denial; real CAS/ARM; actual ARP/GARP/data and stale-MAC IPVS DR delivery; both deny after the original deadline; management always delivers             |
| stop           | Same, with the owner SIGSTOPped through expiry, then resumed for bounded shutdown                                                                                            |
| death          | Same, with exact owner SIGKILL/reap before expiry; retained kernel hooks expire without owner cleanup                                                                        |
| delayed        | Real CAS acknowledgement is deliberately withheld six seconds; the original five-second ticket expires and cannot receive a fresh TTL; both paths remain denied              |
| partial        | A specific fault after first link UP is required; no CAS/ARM, frontend denial, backend still DOWN, management delivers                                                       |
| attach-partial | Specific exit 42 after first hook install, no READY, both links DOWN, first hook retained/second absent, restart refuses adoption                                            |
| frontend-only  | Intentionally omit backend gate and never ARM: frontend ARP/GARP/data must be denied **while real stale-MAC DR must leak**; this proves sensitivity to the original mismatch |

Packet captures are receive-side AF_PACKET observations beyond the veth egress
hook, with fresh exact markers and DR backend-MAC/VIP checks. No source transmit
return code is treated as delivery/denial evidence. This fixture uses synthetic
UDP traffic and management Ethernet frames, not real-engine DNS or management
stream continuity. Finite observation windows are test inputs, not drain bounds.
Any unexpected schema, timeout, missing capability or cleanup error is a failed
run, not a skip. No result from these unexecuted scenarios is claimed here.

## Remaining supported-host evidence and interfaces

Parent must first establish actual C/libbpf build, verifier/load and exact `tc`,
`ethtool`, `bpftool`, IPVS, API-server and nested namespace compatibility. Require
all seven fresh scenarios and retain the negative controls. Expand supported-host
inventory/permission evidence for every intended kernel and veth implementation;
never silently relax a strict check to make a run green.

Production remains unavailable. It needs an authenticated privileged broker with
fixed root-owned code/object, immutable manifest/namespace binding, restricted
commands, capability-free unprivileged owner and exclusive-writer lifecycle;
real independent engine-management attachment/anti-spoofing and management-stream
proof; a supported production two-egress topology and backend eligibility/withdrawal
interface; real engine attribution, protocol/session tests and stale-MAC negative
controls; allocator successor/partition/uncertain-CAS tests; and an enforceable
physical-platform contract for clock rate, suspend, VM freeze/migration and queues.

Zero time-namespace offset and monotonic samples do not bound oscillator rate.
The lab's 100 ms Margin is merely quarantine scheduling input, **not measured
assurance**. The absolute kernel deadline cannot revoke packets already queued
past either hook, remote work already accepted, NIC/device queues, or backend
replies/sessions. A finite measurement campaign cannot prove every hardware/VM
condition. Production needs justified worst-case clock/drain bounds and explicit
host restrictions, including no externally visible forwarding while its clock
is frozen. Guessed margins, successful lab samples, and booleans cannot enable it.
