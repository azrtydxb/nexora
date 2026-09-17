# Opt-in two-host transport fixture

This is executable laboratory code, **not actual Nexora policy evidence**.
Two-host DR tuple verification passed on kw workers 21/22; failed setup/checksum
experiments and pending controls are recorded in
`.procoder/notes/wave2-integration.md`. Parent alone runs live experiments.
No SSH is implemented. No host link, route, sysctl, firewall, IPVS table, Cilium
configuration or existing namespace is changed. The only host network resources
are two exclusive UDP sockets bound to operator-supplied literal IPv4 addresses.

Each group has its own switch, client and backend namespaces on **both** hosts:

| Role    | Left host                                   | Right host            |
| ------- | ------------------------------------------- | --------------------- |
| Switch  | bridge, VXLAN, IPVS DR frontend             | bridge, VXLAN         |
| Backend | a, 198.18.0.3                               | b, 198.18.0.4         |
| Client  | c, 198.18.0.10                              | d, 198.18.0.11        |
| VIP     | 198.18.0.100 on switch and backend loopback | backend loopback only |

Both groups deliberately reuse inner addresses. They have separate namespaces,
VNIs, UDP ports and Unix socketpairs. No default route exists in these namespaces.
Backend ARP suppression keeps the VIP on loopback; connected /24 routes return
replies directly to clients with the VIP source. No LAN interface is attached.
VXLAN is created inside the switch namespace. An isolated underlay veth connects
it to a separate relay namespace (`198.19.0.1/30` and `.2/30`), avoiding Linux
VXLAN's wildcard UDP bind collision. Endpoint veth checksum/segmentation offloads
are disabled with ethtool because the userspace carrier cannot preserve kernel
offload metadata. The UDP relay exchanges VXLAN datagrams through a Unix socketpair
with the host relay. Host sockets use fixed peers, no address/port reuse, strict
VNI/header/EtherType/length validation and IPv4 PMTU discovery with DF. This is a
userspace-assisted VXLAN experiment, not a kernel-only production attachment.
Invalid incoming frames fail the run rather than being silently ignored.

## Prerequisites and exact procedure

Two distinct Linux hosts with existing reachable IPv4 addresses; root; Python 3;
iproute2 with VXLAN/network namespaces; IPVS DR/rr kernel support; ipvsadm;
sysctl; ethtool; tcpdump with immediate mode; **iputils arping**; iptables for SNAT
control. Tools are checked before namespace creation. Captures and decoding use
`tcpdump -Z root` inside this root-only fixture to keep evidence directories private.
Parent must verify host identities, free ports and firewall permission for the two
UDP ports in both directions. Binding detects local socket collisions; plan
validation cannot prove physical host separation or remote firewall reachability.
Never use real VIPs or persistent engine trust/state. Underlay PMTU must be at
least inner MTU + 50 (1450 with the sample); live MTU validation remains required.

Build/copy the same repository and Linux probe to each host through the parent's
normal reviewed mechanism. Generate one disposable fixture certificate on the
parent's test machine, copy that fixture directory to both hosts, and remove its
private key after both runs. This does not use or regenerate cluster trust:

```sh
go build -o /tmp/failover-probe ./deploy/failoverlab/cmd/probe
umask 077
mkdir /tmp/fx-tls
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=dsr-lab.test \
  -addext subjectAltName=DNS:dsr-lab.test \
  -keyout /tmp/fx-tls/key.pem -out /tmp/fx-tls/cert.pem
```

Copy `plan.example.json` to `/tmp/fx-plan.json`; replace documentation addresses
with the **existing literal host addresses**, choose an eight-character unique
run ID and reserve two distinct high UDP ports/VNIs. Use the identical plan on
both hosts. Validation is offline and unprivileged:

```sh
python3 deploy/failoverlab/crosshost/lab.py validate /tmp/fx-plan.json
```

Start on right and left in separate terminals, substituting only `--role` and
`--evidence` on right (`right`, `/tmp/fx-right`). Evidence directories must not
exist. Keep supervisors running until both hosts finish their probes:

```sh
sudo env FAILOVER_CROSSHOST_ENABLE=yes python3 deploy/failoverlab/crosshost/lab.py \
  run /tmp/fx-plan.json --role left --evidence /tmp/fx-left \
  --probe /tmp/failover-probe --ipvs /usr/sbin/ipvsadm --tls /tmp/fx-tls
```

After **both** print READY, run this on left, wait for its `probe-exit` file to
contain `0`, then run the corresponding command on right and require its `0`:

```sh
sudo python3 deploy/failoverlab/crosshost/lab.py control /tmp/fx-left probe
sudo cat /tmp/fx-left/probe-exit
```

Do not issue a second probe command: attempts are consumed even on failure.
Each host probes both groups, two rounds of all five transports without fallback
or application retries. The original verifier requires 20 exact bidirectional
flows per group and ten socket-observed queries on each backend. Serial host
probe execution ensures round-robin gives both backends every transport.

After both successful probe-exit files exist, finish each host locally:

```sh
sudo python3 deploy/failoverlab/crosshost/lab.py control /tmp/fx-left finish
```

Wait for each supervisor to exit zero. Copy right evidence to the review machine
as `/tmp/fx-right` (preserve left evidence separately), then run:

```sh
python3 deploy/failoverlab/crosshost/verify_pair.py \
  /tmp/fx-left /tmp/fx-right /tmp/fx-verified
```

## Controls, evidence and recovery

Use new run IDs and evidence directories for each independent control. Repeat
the procedure with `--mode snat` on **both** hosts: all five transports must fail
with the explicit observed `.12` (left) or `.13` (right) source mismatch; timeout/TLS failures are red.
`--mode duplicate` deliberately enables backend VIP ARP replies and requires
iputils arping to observe the frontend MAC plus an extra responder in each group.
Normal DR/SNAT runs require exactly the frontend MAC. Duplicate mode runs no DNS
probes and must never be passed to the positive verifier. Foreign VNI and malformed
frame controls are offline unit tests; hostile live packet injection/cross-group
policy attribution are still gates, not claimed passes.

Evidence includes exact plan/role/mode, command journal, owned namespace list,
address/routes/neighbors, ARP responses, raw PCAPs, capture drop reports, backend
socket logs, original probe output/exit, cleanup errors and merged tuple reports.
Keep supervisor stdout/stderr too. No successful retry can overwrite an attempt.
A failed supervisor causes the other host's attempts to fail; parent should stop
both and retain evidence, not restart into the same directories.

SIGINT/SIGTERM trigger cleanup: stop/reap owned subprocesses, finalize captures,
then delete only successfully created namespaces in reverse order. An existing
namespace fails creation and is never added to cleanup ownership. Failed deletion
makes cleanup red. SIGKILL/power loss require manual recovery: review `owned.json`,
`commands.jsonl`, process command lines and namespace ownership before deleting
anything. There is deliberately no prefix-wide deletion, global flush or automatic
stale-resource takeover. Remove the shared disposable TLS key manually only after
both hosts stop; evidence contains no copied key.

## Remaining gates

Actual engine attachment is **not implemented**. Inspected `engine/src/main.rs`
launches `nexora-engine --config /absolute/engine.toml`; `bootstrap.rs` accepts
strict TOML with `state_dir`, `standalone_snapshot`, `standalone_blob_dir`, explicit
five-transport listeners and paired TLS files. `snapshot.rs` reads a protobuf
`ConfigSnapshot` and validates version/cache/ACL/blob integrity. A TOML-only echo
substitution would not be a valid engine fixture. A follow-up needs a generated,
validated disposable snapshot, distinct client policies, query-log attribution,
isolated state and a response verifier that understands those policies. Existing
probe TXT source echoes cannot provide that evidence. No production identities,
join tokens, management URLs or state directories belong in that fixture.

Linux execution, PMTU/fragmentation/large DNSSEC, source-selected multi-route
return, independent host-identity attestation, sustained cross-group leakage,
frontend HA/fencing, failure/session continuity and performance remain unresolved.
This change neither closes FG-01 nor authorizes cluster attachment or migration.

Offline checks:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s deploy/failoverlab/crosshost -v
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s deploy/failoverlab -v
```
