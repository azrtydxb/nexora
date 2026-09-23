# FG-05 kernel expiry candidate

This directory contains an unaccepted Linux TC eBPF gate candidate, a narrow
libbpf driver, and a parent-run namespace smoke test. It is not an election,
platform adapter, production deployment, or proof of full HA. All changes are
confined here; the root Go module has no new dependencies.

Parent real-engine run `57f032b7` is historical: review found its verifier did
not join captured sockets to backend query records. Corrected run `fb374f92`
passed the exact socket/backend/OTLP join for 80 flows across two groups, two
clients, two backends per group and five transports, including source policy,
signed large payload/truncation and an independent management attachment.
Echo/negative controls passed separately. These are forwarding results, not
managed-control continuity, FG-05 acceptance or platform acceptance. Parent also
passed this classic-TC expiry smoke on Linux; exact evidence and outstanding
boundaries are in `.procoder/notes/wave3-reviewed-evidence.md`.
Stable assignments remain `.136` A/C and `.139` B/D. This candidate rewrites no
addresses, ports or payloads, terminates no TLS, changes no CNI and touches no
trust material.

## Enforcement and its limits

`gate.bpf.c` runs on classic TC egress of an exclusively assigned, dedicated
frontend veth. **Every frame** is dropped unless map key zero contains a valid
window: nonzero capture, positive deadline, at most five seconds from capture,
and `capture <= bpf_ktime_get_boot_ns() < deadline`. ARP, broadcast gratuitous
ARP, IPv4, IPv6, fragments and all transport payloads use the same decision.
There is no packet-parser bypass. An absent key denies. A missing map/object,
unsupported clock/query/helper, verifier/load failure or foreign attachment
prevents startup; the adapter must keep the interface DOWN.

The map is a fresh, unpinned one-element HASH, so replacement publishes a whole
window rather than tearing two fields in an ARRAY. No wall clock, relative TTL
update, signal handler or userspace watchdog participates in packet expiry.
An update delayed until after expiry can insert only the original absolute
window: the kernel still drops it. SIGSTOP, EOF, crash and SIGKILL cannot detach
the filter; its reference retains the map until the owned link/namespace dies.
No automatic restart reuses that interface or map.

This is a fence against paused/stale cooperative runtime code, not hostile root.
CAP_NET_ADMIN/BPF or host privilege can remove any software gate. Before readiness,
the driver checks the exact assignment alias, veth type, DOWN state, no foreign
qdisc, XDP or TCX attachment, and rejects an existing clsact hook. TCX query
support is mandatory. The object must have exactly the expected program and
unpinned map ABI before load; object bytes remain trusted executable code and
must be distributed/verified with the driver, never supplied by a tenant.

Namespace creation, exclusive administration and attachment provenance are the
platform's responsibility. A matching alias is an assignment check, not an
identity authority. No concurrent link replacement, attachment change, alternate
VIP egress, AF_XDP/XDP_TX, raw `PACKET_QDISC_BYPASS`, offload or privileged direct
transmit is permitted. Restrict capabilities and packet-socket access accordingly.
A driver cannot prove exclusivity against a concurrent privileged administrator.
Management egress must be on a separate interface and must not carry VIP data or
advertisements. This candidate does not gate backend direct replies, already
forwarded packets or already queued traffic beyond the egress hook.

## Platform adapter interface contract

1. Create a new, exclusively owned frontend network namespace, different from
   PID 1's namespace, with a dedicated veth in DOWN state. Preserve the independent
   management attachment. Assign `IFLA_IFALIAS=nexora-fence:<random-owner-token>`
   (at least 16 token characters). Keep the namespace reference and ownership
   journal; assign one gate per frontend egress path. No host interfaces qualify.
2. Use Linux with matching zero-offset userspace/kernel CLOCK_BOOTTIME. The driver
   refuses missing time-namespace introspection, nonzero offsets, or different
   current/child time namespaces. Never unshare/change clocks after validation.
3. In that namespace, start `fence-driver IFACE ALIAS /trusted/path/gate.bpf.o`
   with private stdin/stdout pipes. A supervisor must retain the actual process
   identity. `READY ifindex=... program=... horizon_ns=5000000000` means an empty,
   denying map is attached. Only then may the adapter bring the veth UP. Any
   failure retains DOWN state; no fallback path is authorized.
4. Send `CAPTURE\n` **before** starting an allocator acquisition/renewal operation.
   Read `TICKET <capture_ns> <absolute_deadline_ns>`. Bind that exact ticket to
   namespace instance, host boot ID, group, generation, owner and allocator
   operation. There is one outstanding ticket; another CAPTURE replaces it.
5. Only a successful matching allocator authorization may send
   `ARM <capture_ns>\n`. `ARMED` means the original window was inserted, not that
   the lease remains current or that packets now pass. A delayed map update or
   response can already have expired. Unknown/expired/consumed tickets return
   `REJECTED`; there is no automatic renewal. Never capture a fresh ticket to
   reuse an old allocator response. Stdin is a trusted adapter interface; this
   driver does not authenticate DB grants or construct production election.
6. `DENY\n` deletes the key and invalidates the outstanding ticket (`DENIED`). A
   pause before processing DENY still relies on the previously recorded bound.
   Withdrawal acknowledgements and status must use independently verified state;
   a successful pipe write, process death or DB lease deletion is not a fence.
7. For teardown, stop senders and bring the owned link DOWN; verify namespace and
   ownership identity before deleting that exact owned link/namespace. The driver
   never detaches filters or deletes qdiscs. A restart on an existing hook refuses
   even if its alias matches. Do not delete/recreate the link to evade an old
   allocator bound. Preserve failed cleanup evidence and reservations.

No packet-forwarding/IPVS configuration is implemented here. Pair this contract
with the separate platform-adapter lane; do not merge files from that lane here.

## Required consensus and time contract

The allocator must serialize grants per group and durably retain a conservative
upper bound for **every possibly authorized window**, including ambiguous RPCs,
delayed responses, crashes and renewals. It must not authorize a successor before
that bound plus the measured post-gate packet/advertisement drain margin. A DB
lease alone does not fence this dataplane. Old generation responses must remain
bound to their original ticket, boot and namespace instance.

Do not compare raw boottime timestamps across hosts. For example, if a ticket is
captured before the allocator receives its request, and the local boottime clock
has a proved minimum rate `r_min` relative to the allocator's conservative real
elapsed-time domain, waiting at least `5 seconds / r_min` from request receipt,
plus allocator clock-error and measured network/queue margins, bounds that ticket.
The allocator must retain the maximum bound across all uncertain grants. Its own
clock-rate/failover assumptions need equally conservative treatment. Earlier
revocation is allowed only with independent fencing evidence; an RPC ACK is not
sufficient.

CLOCK_BOOTTIME includes suspend, but that API property alone does not establish
bounded physical clock drift or correctness across VM pause, host suspend,
hypervisor migration and resume. Measure these on each supported platform and
prove the common clock domain. If a machine can freeze/rewind this clock while a
remote allocator continues and later transmit, **this candidate cannot guarantee
HA safety** without an additional external fence. No assumption is silently
replaced with a mutex or watchdog. Such a platform remains unsupported until
its conservative bound is proven. Packet delivery after the gate also needs a
bounded drain model; TC is not proof of instantaneous network-wide withdrawal.

## Exact build and test handoff

Parent-only disposable Linux execution; no SSH, cluster, toolbox or live commands
were executed by this lane. Prerequisites:

- Linux 6.6 or newer with BPF syscall, SCHED_CLS/clsact, network namespaces, veth,
  TCX query support and readable time namespace offset state; relevant kernel
  configuration and LSM policy must permit loading and attaching this program.
- Clang/LLVM with BPF target and Linux UAPI headers including `BPF_TCX_INGRESS` and
  `BPF_TCX_EGRESS`; libbpf 1.3+ development headers/library, libelf, zlib, pkg-config,
  make and a C compiler. No CO-RE/vmlinux BTF dependency and no Go dependency.
- Python 3.11+, iproute2 (`ip`), root in a disposable Linux environment with
  namespace mount permissions, CAP_NET_ADMIN and the capabilities required by
  its BPF policy. Restricted containers that lack these are unsupported tests.

From the repository root, build without changing system dependencies here:

```sh
make -C deploy/failover/fence test
make -C deploy/failover/fence smoke-build
# On Debian multiarch layouts, if asm/types.h is missing, supply e.g.:
# make -C deploy/failover/fence smoke-build BPF_INCLUDES=-I/usr/include/x86_64-linux-gnu
sudo python3 deploy/failover/fence/smoke.py --execute-isolated-kernel-test
```

Use the target architecture's UAPI include directory, not the example blindly.
Build succeeds only with real libbpf/Linux headers; no fake userspace driver is
substituted. Preserve stdout/stderr, exit code, source/binary hashes, kernel and
tool versions in parent-owned evidence. The smoke runner creates one random
`nxfg05-*` namespace and two veth pairs wholly inside it; it assigns only a new
alias and no live VIP/address/route. Cleanup deletes only its newly created
namespace, including after a test failure. A kill escalation is itself a failure.
There is no global tc deletion, host flush or trust generation.

The smoke test requires rejection of missing-map and invalid-helper objects,
foreign alias and existing hook; then verifies real peer reception of ARP, GARP
and UDP frames. It checks default denial, authorized delivery, delivery with owner
paused before expiry, denial with owner still SIGSTOP after expiry, resumed stale
ARM denial, a pre-lease ticket delayed past expiry, fresh authorization, DENY,
and live-window process exit followed by kernel expiry. Independent management
frames must pass at every stage. No shell echo or userspace clock predicate is
counted as packet proof. Python optimization is rejected so assertions cannot be
silently disabled. Unsupported prerequisites fail, never report a skipped PASS.

A single namespace smoke run is not a cross-host successor/partition test, a
clock-drift/suspend measurement or platform acceptance. Foreign TCX/XDP, time
namespace and other supported-kernel variants need parent-run negative fixtures
in addition to this first smoke. Full two-owner consensus/election, asymmetric
partitions, DB outages, node loss, host suspend/VM pause, lifecycle and actual
frontend traffic integration remain required.
