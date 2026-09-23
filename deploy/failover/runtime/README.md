# Detached FG04/FG05 runtime candidate

Follow-up: [ACTIVE-LAB.md](ACTIVE-LAB.md) documents the separate opt-in dual-egress
lab candidate. This detached path and its original limitations remain unchanged;
run its frozen pre-followup snapshot first. Neither candidate establishes HA.

This runtime joins the existing owned-stage adapter, real private-pipe DriverGate,
its exact BootClock, and strict HTTPS Kubernetes Lease controller. It is an
explicitly enabled, bounded **detached lab**, with no frontend activation path.
It does not close FG04/FG05 or establish production HA.

`fg-runtime --execute-detached-lab --config /trusted/lab.json` is the only
executable entry. Unknown, duplicate (including case variants), oversized or
unsupported configuration is rejected. No ambient kubeconfig, credentials,
proxy, insecure TLS, shell command, DNS retry or serving fallback is discovered.
`FileConfig` documents the JSON fields; durations are integer nanoseconds. The
parent harness creates a complete configuration against its fresh local API
server. There is intentionally no usable production example or proof boolean.

## What is integrated

1. Inventory the calling namespace: only loopback and two addressless reciprocal
   local veth pairs (`fg0/fp0`, `mg0/mp0`) are allowed. The API origin must be
   `https://127.0.0.1:<port>`. External peers, masters, extra links and VIP addresses
   are rejected. Exclusive administration is still required after inventory.
2. Launch trusted `platform/cmd/stage_session.py` using private deadline-capable
   pipes. That helper creates its own private mount/network/PID namespaces and
   calls the existing platform provisioner and actual adapter. All three outer
   peers stay DOWN. The fence binds the complete manifest identity and attachment
   inodes. Baseline owned DOWN dummies must read back correctly before staging.
3. Launch the actual trusted driver in the calling lab namespace, construct one
   fresh holder and one controller paired with the exact returned BootClock.
   The runtime never brings `fg0` UP. Initial quarantine and every CAS/ARM use the
   existing controller with no wrapping, replacement clock or fake authorization.
4. Stop on the first error, late/partial acknowledgement, expired prior window,
   cancellation, or a no-ARM result after previously acknowledging ARM. No
   automatic reacquisition/reconnect or source/config replacement occurs.
5. On every exit, attempt driver Close and then independently contained stage
   cleanup using separate bounded contexts. A failed DENY cannot suppress stage
   cleanup; stage safety does not depend on DENY. Exact hooks and their maps are
   retained. No cooperative lock is treated as fencing, and no reservation or
   deployment lock is released. `CLEANED <nonce>` is emitted only after owned
   namespace cleanup; the receiver also requires successful exit and EOF.

`Report` contains historical step/ARM counts. `lab-stopped` means this bounded
lab completed and both shutdown calls succeeded; it is not proof of current
inactivity, eligibility, withdrawal or HA. Any ambiguity returns nonzero/failed.
A protocol nonce correlates two operations; it does not authenticate hostile code.
Only immutable administrator-owned code and directories are supported. Install
source, its imported modules, Python, driver and BPF object under canonical trusted
paths; `/tmp` and symlink ancestors deliberately fail the runtime path checks.
The Python standard library, interpreter and fixed Linux utilities are part of
that trusted installation. No hostile-root or concurrent-admin guarantee exists.

The run loop is bounded to two minutes. Driver I/O/reap are each at most five
seconds; detached stage operation/reap at most one minute each. Total setup and
teardown have separate budgets, not just the loop duration. Each stage exchange
uses the caller deadline; after failure the separate reap may consume one further
stage timeout. With the runtime's cleanup context, stage Close is bounded by two
stage timeouts. Driver Close additionally consumes its configured reap timeout.
Bounds assume ordinary syscall/scheduler progress; they do not prove frozen-VM
behavior. Stage requests and responses are at most 128 bytes and cannot recover
from ambiguity. Helper stdin EOF/process termination never enables traffic.

## Transmission inventory and unsupported integration

| Path                                                                          | Detached lab containment                                    | Missing active-frontend integration                                                        |
| ----------------------------------------------------------------------------- | ----------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| Client-facing frontend egress, ARP/GARP                                       | Stage outer peer DOWN; driver test pair has both ends local | Gate every advertising/data egress before UP                                               |
| Requests arriving at a stale frontend MAC, then DR-forwarded toward a backend | Stage frontend peer DOWN and IPVS destinations weight zero  | Backend-facing frontend egress must also be fenced; client-facing TC alone is insufficient |
| Backend direct replies, delayed queued requests/replies                       | Backend outer peers DOWN; owned VIP dummies remain DOWN     | Eligibility/withdrawal and measured bounded post-gate drain, including existing sessions   |
| Management                                                                    | Separate addressless mg0/mp0 probe pair; API on loopback    | Independent real engine management attachment and route/anti-spoofing contract             |
| Other links, external veth peers, master/bridge paths                         | Rejected by lab inventory                                   | Inventory all attachment/offload/XDP/raw bypass paths and enforce privilege restrictions   |

The runtime stage provider is **not** a driver-backed provider for active DR.
The stage manifest is the fixed detached dns136 fixture, not a caller-supplied
production manifest. Detached containment stays closed regardless of Lease/driver state. The gate
packet test is a different, addressless namespace topology. Connecting these
components does not establish a common serving dataplane.

Direct production binding remains refused: the platform requires an UP attachment
with a full-manifest platform alias, while the driver requires a fresh DOWN veth
with a fence alias and no prior hook. The platform also rejects an extra management
link. A reviewed preprovisioner must reconcile these ownership/ordering contracts,
supply independently trusted engine placement/eligibility and all-path fencing,
and prove clock-rate, VM pause/resume and post-gate drain bounds. A positive lab
`Margin` is scheduling input, **not measured evidence**. Production cannot be
selected by setting booleans, increasing a duration or relaxing inventory.

## Local checks and parent-only execution

Nonprivileged checks from the repository root:

```sh
GOPROXY=off GOSUMDB=off go test -race ./deploy/failover/runtime/... -count=1
python3 -B -m unittest discover -s deploy/failover/runtime -v
python3 -B -m unittest discover -s deploy/failover/platform -v
go vet ./deploy/failover/runtime/...
ruff check deploy/failover/runtime deploy/failover/platform
```

The separate new `lease/integration_actual_test.go` is opt-in Linux code. It uses
actual HTTPS API CAS (including stale RV rejection), actual driver/clock,
quarantine and ARM, real peer ARP/GARP/data reception/absence, independent
management frames, expiry, ownership change, cancellation, stale ARM poisoning
and restart refusal. Expected poisoned Close remains explicitly uncertain; it
is not converted to an inactivity acknowledgement. No test starts an engine.

Parent prerequisites: supported Linux BPF/TCX/libbpf and zero-offset clocks;
preloaded IPVS/RR/dummy/veth capabilities; fixed `/usr/sbin/ip`, `ipvsadm`, `sysctl`
and `/usr/bin/unshare`, `mount`; real `etcd`, `kube-apiserver`, OpenSSL and Python.
No prerequisite is installed or silently skipped. Use an already provisioned
root-owned immutable source tree such as `/opt/nexora-lab` and binaries under
trusted canonical paths. Build on the parent-supported Linux host:

```sh
make -C deploy/failover/fence smoke-build
GOPROXY=off GOSUMDB=off go build -o /opt/nexora-lab/fg-runtime ./deploy/failover/runtime/cmd/fg-runtime
GOPROXY=off GOSUMDB=off go test -c -o /opt/nexora-lab/lease-integration.test ./deploy/failover/lease
```

Supply actual absolute paths below; each command creates a fresh lab. Invoke the
canonical real Python executable. Preserve each invocation's full output and exit
status separately, including failures:

```sh
sudo /usr/bin/python3 /opt/nexora-lab/deploy/failover/runtime/lab.py \
  --execute --scenario runtime --executable /opt/nexora-lab/fg-runtime \
  --driver /opt/nexora-lab/deploy/failover/fence/fence-driver \
  --object /opt/nexora-lab/deploy/failover/fence/gate.bpf.o \
  --etcd /opt/lab-tools/etcd --apiserver /opt/lab-tools/kube-apiserver \
  --openssl /usr/bin/openssl
sudo /usr/bin/python3 /opt/nexora-lab/deploy/failover/runtime/lab.py \
  --execute --scenario driver --executable /opt/nexora-lab/lease-integration.test \
  --driver /opt/nexora-lab/deploy/failover/fence/fence-driver \
  --object /opt/nexora-lab/deploy/failover/fence/gate.bpf.o \
  --etcd /opt/lab-tools/etcd --apiserver /opt/lab-tools/kube-apiserver \
  --openssl /usr/bin/openssl
```

The harness verifies inherited original namespace descriptors and private mount
propagation before mounting a private 256 MiB `/run`. It starts real etcd and
kube-apiserver exclusively inside a newly owned addressless namespace, with only
loopback API listeners. It creates synthetic temporary lab trust/token material,
a Namespace and a Lease there; it never reads existing credentials or cluster
state. An explicit readiness poll precedes single-attempt resource writes.
Private data/trust disappear with the outer namespace; server logs are bounded
and emitted for parent retention. A timeout/kill escalation is a failure. Exact
owned namespace inode is checked before deletion; no host/global flush occurs.
Outer PID isolation contains descendant processes after failure or termination.

The real API harness is newly supplied and **not executed here**. API-server flag
compatibility, trusted-path packaging, utility output and nested namespace
behavior must be verified by the parent, with original failures retained. The
existing isolated platform test remains separately runnable and its historical
wave3 PASS is not a PASS for these new harnesses. Cross-host pause/death/partition,
actual DR stale-MAC forwarding negative controls, management-stream continuity,
exact engine attribution and clock/drain acceptance remain open.
