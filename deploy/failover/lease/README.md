# FG-05 Kubernetes Lease CAS candidate

This is an unactivated library candidate on base `f9c1653`, confined to this
directory. It uses the root Go module, standard library and existing x/sys/unix
dependency. It does not provision cluster resources, discover credentials, activate
frontends, invoke SSH, sync workspaces or perform deployments.
The adjacent fence files are read-only references. The parent reports that its
kernel smoke passed paused/dead-owner absolute CLOCK_BOOTTIME expiry at five
seconds with separate management traffic unaffected. This binding candidate does not establish
kernel integration proof or production HA.

## Protocol and proof assumptions

An administrator must provision exactly one coordination.k8s.io/v1 Lease with
the configured namespace/name. Its metadata annotations must include:

```
failover.nexora.io/protocol: nexora-kernel-ticket-v1
failover.nexora.io/epoch: "0"
failover.nexora.io/nonce: <64 lowercase hex characters from 32 random bytes>
```

The holderIdentity may initially be empty. Each process incarnation calls
`NewHolderID` once and uses that random identity, never a management instance ID
or an identity saved across restart. The library never creates or deletes a
Lease. It pins the first valid UID, treats a 404 or later UID replacement as
terminal, and rejects unknown protocols and malformed protocol fields.
Provisioning/recovery after deletion is an explicit external responsibility;
every newly constructed controller still quarantines.

`Step` serializes local operations and issues at most one GET and one conditional
PUT. Startup first requests DENY. Acquisition requires observations of the same
UID/resourceVersion spanning at least `MaxKernelWindow + Config.Margin` on the
injected local boottime clock. Every observed version change resets the interval;
every error cancels it and requests DENY. No observed errors are hidden by retry.
Kubernetes must serialize all successful writes and never reuse resourceVersions
for different revisions. Reads must describe genuine committed revisions; a stale
GET cannot authorize a grant because the PUT still needs its exact UID/RV.
Intermediate writes cannot restore the old RV. No wall-clock renewTime or
leaseDurationSeconds comparison participates in takeover. Those fields, if
present, are preserved as telemetry only.

Each successful PUT changes both a random nonce and a checked unsigned epoch.
The reply must contain the expected UID, holder, nonce and epoch, and a different
nonempty RV. An API no-op or mismatched acknowledgement cannot arm. Own renewal
requires a previous locally acknowledged operation and a fresh GET matching its
UID/RV, holder, nonce and epoch. Any intervening change/error removes the renewal
shortcut and requires a new full quarantine, even if the holder still matches.

Immediately before a PUT, CAPTURE obtains a ticket in the same zero-offset
CLOCK_BOOTTIME domain as the kernel. Capture must fall between the controller's
before/after clock reads; deadline must be greater than capture and at most five
seconds later. Only a fully acknowledged matching CAS permits ARM of that exact
ticket, before its original deadline. No late/ambiguous/conflicted response may
capture another ticket or reuse authorization. The kernel must enforce the
original deadline even if ARM or the controller pauses. Epoch overflow, clock
regression, invalid bounds, and malformed acknowledgements fail closed.

Why the interval covers a previous owner: the previous ticket was captured
before its write. The successor's first observation of that revision is after
that write, so waiting the maximum possible remaining ticket lifetime plus the
explicit margin covers it. If a renewal happens, the revision changes and either
resets observation or conflicts with the successor's PUT. Even an ambiguous
committed PUT starts this same revision-based quarantine. A delayed response
cannot extend its original ticket. This argument requires **all writers** to
use this protocol and all frontend paths to obey the gate.

Five seconds of one host's clock must expire before another host's configured
quarantine ends, with a conservatively proven clock-rate/skew allowance and
post-gate packet/advertisement drain in the margin. CLOCK_BOOTTIME includes
suspend but alone proves neither clock-rate bounds nor safe VM pause/migration.
Clocks must not freeze/rewind relative to externally observable forwarding.
Unsupported platforms require an external fence. The one-second margin used by
deterministic tests is a test input, **not** a measured deployment value.

## Interfaces and opt-in binding

`Authority` is injectable for deterministic faults. `HTTPSAuthority` implements
real GET/PUT against an immutable HTTPS origin/namespace/name. It uses normal
certificate and hostname validation (TLS >= 1.2), optionally cloned supplied CA
roots, no proxies, no redirects, bounded dial/handshake/header/body/total I/O,
64 KiB body limits, strict duplicate-free/depth-bounded JSON, and generic errors
that do not expose bearer credentials, response bodies or request URLs. PUT
preserves server fields and unrelated annotations, carries exact UID/RV, and
does not replay requests or retry conflicts. Only HTTP 200 is acknowledged.

`LaunchDriver(ctx, DriverOptions)` is an explicit Linux-only binding candidate.
It returns one `DriverGate` and its exact paired `BootClock`; pass these to `New`
once. A second controller or a different clock is rejected. The controller still
performs at most one CAS per Step. No executable/CLI, frontend activation,
namespace-entry command, network administration, default runtime, shell fallback,
credential lookup or retry loop is provided.

The caller must already run inside its exclusively owned network namespace and
supply `Enabled: true`, the exact boot UUID, `net:[inode]` and `time:[inode]`
identities, numeric real/effective UID, interface, `nexora-fence:` ownership alias,
and positive IO/reap timeouts (each at most one minute). UID describes Unix
caller identity (now root-only for this privileged lab binding), **not** the Kubernetes Lease UID: the latter remains
pinned to the first valid GET by each new controller. Namespace strings are
identity assertions, not proof of ownership. Provisioning and exclusive
administration are parent responsibilities. Launch rejects PID1's net namespace;
the driver independently repeats this check. PID1 must therefore represent the
forbidden initial/management namespace in the parent's lab environment.

Driver and object paths must be absolute, canonical, regular, non-symlink trusted
files owned by root; each ancestor must be a root-owned directory. Code ownership
is checked separately from caller identity. An unprivileged-owner/privileged-helper
split is unsupported; nonroot callers are refused. Group/other-writable files or ancestors are rejected, including
`/tmp`. The driver must be executable. These checks supplement the requirement
that the same UID/root cannot concurrently replace code, object, namespace or
frontend; they are not a defense against a malicious administrator or ACL-based
write access. The launcher does not change UID, groups or capabilities: explicit
provisioning of the entire privilege scope remains external. The child inherits
only the already-entered namespaces and execution privileges, an empty environment,
private stdin/stdout pipes and discarded stderr; no shell is involved.

The trusted adjacent driver checks interface alias, veth type and DOWN state,
foreign XDP/qdisc/TCX hooks, and fresh object/map ABI. It attaches **classic TC
egress**, not TCX; TCX queries reject foreign hooks. READY records its interface
index/program ID. The adapter never reuses maps, detaches programs or destroys
qdiscs. Shutdown leaves the kernel attachment in place; parent-owned namespace
teardown is a separate operation. No code here performs it.

| Method  | Exact driver protocol                                                            |
| ------- | -------------------------------------------------------------------------------- |
| Startup | `READY ifindex=<positive int32> program=<positive uint32> horizon_ns=5000000000` |
| Capture | `CAPTURE` -> `TICKET <captured_ns> <deadline_ns>`                                |
| Arm     | `ARM <same captured_ns>` -> `ARMED`, no recapture                                |
| Deny    | `DENY` -> `DENIED`, invalidating outstanding ticket                              |

Responses require exact spaces, LF, canonical positive decimals (at most 20
digits), no extra fields/CR/control bytes, and at most 126 bytes before LF.
Tickets must strictly advance and have the driver's exact five-second horizon
without overflow. Only one ticket can be outstanding. ARM validates the entire
original Ticket before writing; a mismatch, duplicate capture/arm, replayed
ticket, parse failure, EOF, timeout or cancellation permanently poisons the
connection, closes both pipe ends and kills the child. No reconnect, resync,
retry, or fresh-window substitution occurs. This unsequenced protocol requires
the trusted driver to emit exactly one synchronous reply per command; it cannot
authenticate a compromised driver emitting carefully timed forged replies.

Every exchange bounds lock acquisition and read/write with the smaller of its
context and IOTimeout. Actual `os.File` pipe deadlines must be supported, and
context cancellation also closes the descriptors. The test-only child exercises
blocked reads and a pipe-filling write. A dedicated Wait reaps the sole child;
Close attempts DENY, poisons, kills and waits at most ReapTimeout. Thus Close's
configured wait bound is IOTimeout + ReapTimeout. A reap timeout is an explicit
error, not proof of process exit or inactivity. As with ordinary Go deadlines,
these are bounds while the runtime/kernel can schedule and complete local
syscalls, not a promise during a frozen VM or uninterruptible kernel operation.

`BootClock.Now` calls Linux CLOCK_BOOTTIME directly and returns absolute kernel
nanoseconds. It validates the supplied boot ID, current and child time namespace
identity for both the current thread and process leader, and complete zero
monotonic/boottime offsets before and after each read. Linux exposes those offsets
at `/proc/self/timens_offsets`, not the per-thread procfs path; matching all four
namespace identities binds that process-level file to the sampled thread domain. Unknown/missing/nonzero offsets, identity replacement, overflow or clock
regression latch terminal; Now returns zero and Err records the failure. Gate
exchanges also check the paired clock and caller/child namespace identities.
There is no wall-clock, elapsed-since-start, offset translation or unsupported-OS
fallback. Namespace/offset drift is refused; this is **not** an independent
oscillator-rate measurement or an atomic kernel attestation. The earlier
clock-rate, VM pause/migration, exclusive administration and drain assumptions
remain unchanged. For thread-specific namespace entry, the parent must keep its
controller goroutine locked to that OS thread; this package never calls setns.

Gate errors are potentially effective operations, not proof of inactivity. Any
Capture/Arm/Deny failure leaves the controller terminal, requests DENY where
possible, and reports uncertainty; a failed DENY cannot be reported as inactive.
A separate bounded context is used for DENY after authority cancellation. Gate,
clock and authority implementations must honor their contracts; a blocking
injected implementation cannot be made safe by assuming context cancellation
interrupts it. Kernel expiry remains necessary even when userspace never resumes.

`Result.CASAcknowledged` and `TicketArmed` are historical operation acknowledgements,
not current Lease authority, frontend activity, independently verified fencing,
node health, or eligibility. There is no Active/Leased health status. Actual
frontend activation, eligibility/engine attestation, all-writer authorization,
RBAC, root/capability/bypass prevention, exclusive provisioning, independent
management attachment, teardown and multi-host platform acceptance remain outside
this candidate.

## Validation commands

Run from the repository root, with a writable Go cache if required:

```sh
export GOCACHE=/tmp/nexora-lease-gocache
# Pass 1: deterministic controller schedules and faults
go test ./deploy/failover/lease -run 'Test(Startup|Two|Renewal|Stale|Partition|Delayed|Deletion|Incarnation|Ticket|Paused|Concurrent|Invalid|Unchanged|Canceled|Reappearance|Driver|ZeroOffsets|Unsupported|Linux)' -count=1
# Pass 2: real TLS, strict HTTP client, malformed transport cases
go test ./deploy/failover/lease -run 'Test(TLS|HTTP|RealTLS)' -count=1 -timeout=120s
# Pass 3: race detector and static analysis
go test -race ./deploy/failover/lease -count=1 -timeout=240s
go vet ./deploy/failover/lease
GOOS=linux GOARCH=amd64 go test -c ./deploy/failover/lease -o /tmp/lease-binding-linux.test
# Pass 4: adversarial source/test review; rerun after fixes
go test -race ./deploy/failover/lease -count=1 -timeout=240s
```

TLS tests run the real HTTP transport, TLS handshake and certificate validation
over `net.Pipe`, using httptest's test-only certificate. Only the dial endpoint is
substituted in test code. The initial TCP-listener run was rejected by this
macOS sandbox (`bind: operation not permitted`); no TCP/network or Kubernetes
cluster result is claimed. The production constructor exposes no dial override.
Parent-supported Linux runs must repeat these commands and integrate the actual
driver/boottime adapter, API server, paused processes, expiry and separate
management traffic. Those integration results are still missing.

Binding follow-up evidence and import deltas are in `HANDOFF-binding.md`, separate
from the previous `HANDOFF.md`. macOS real child-pipe tests and Linux compilation
are not Linux pipe-deadline or kernel binding proof. Parent subsequently ran the
full Linux race suite and vet successfully (101.368s), including real child-pipe
deadlines and the corrected positive absolute-clock test without skips. Restoring
the incorrect per-thread offset path makes the strengthened clock test fail.
Actual trusted-driver execution through this binding in an owned lab namespace,
Kubernetes API CAS and packet expiry remain pending.

The optional `LabBackendInterface` / `LabBackendAlias` pair selects only the
[isolated active lab](../runtime/ACTIVE-LAB.md) dual-driver protocol. It requires
READY2 with distinct interface indices and one program ID. Both hooks share one
absolute window/map. Detached runtime configuration refuses this pair. Root lab
owner capability removal is performed before Go starts by that parent harness;
LaunchDriver itself never elevates privileges or discovers an ambient helper.
