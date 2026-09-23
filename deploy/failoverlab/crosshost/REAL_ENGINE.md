# Disposable real Nexora engine acceptance

Implemented acceptance target; Linux/cross-host execution is **pending parent**.
The existing echo pass is not product evidence. This target starts the actual
compiled engine in standalone mode, with two independent groups and two clients,
using the existing `lab.py` namespace owner. No separate namespace library exists
in this baseline. No engine enrollment, production identity, existing trust,
LANVIP, host interface/route, CNI, cluster or retained lock is touched.

## Build and run (parent-owned Linux)

Build the same Linux engine for both hosts. The verifier requires identical hashes.
Use the existing [cross-host procedure](README.md) for plan creation, host identity,
prerequisites, captures, serial probes and cleanup. Real mode additionally requires
an ECDSA fixture produced by the new exclusive lab generator:

```sh
cargo build --locked -p nexora-engine --release
go build -o /tmp/failover-probe ./deploy/failoverlab/cmd/probe
/tmp/failover-probe lab-tls /tmp/fx-engine-tls
```

Copy this **new disposable lab directory only** to the second host using the
parent's reviewed mechanism. It contains a one-day P-256 certificate/key and a
certificate-bound lab marker. Existing directories cannot be reused or regenerated;
real mode refuses unmarked/replaced trust before creating network resources.
The README's older RSA echo certificate is not valid for this target. Remove the
lab key manually after both supervisors stop; no key is copied into evidence.

On both hosts, using corresponding roles and new evidence directories:

```sh
sudo env FAILOVER_CROSSHOST_ENABLE=yes FAILOVER_REAL_ENGINE_ENABLE=yes \
  python3 deploy/failoverlab/crosshost/lab.py run /tmp/fx-plan.json \
  --role left --evidence /tmp/fx-engine-left --mode dr \
  --probe /tmp/failover-probe --ipvs /usr/sbin/ipvsadm \
  --tls /tmp/fx-engine-tls --engine /absolute/path/to/nexora-engine
```

Wait for **both READY lines**. Probe left, require its `probe-exit=0`, then probe
right and require its zero. Issue finish only after **both** successful probes:

```sh
sudo python3 deploy/failoverlab/crosshost/lab.py control /tmp/fx-engine-left probe
sudo cat /tmp/fx-engine-left/probe-exit
# Perform the equivalent right probe and inspect its exit before finishing either.
sudo python3 deploy/failoverlab/crosshost/lab.py control /tmp/fx-engine-left finish
```

Require both supervisors to exit zero, copy their stopped evidence directories,
then run the same verifier, which selects engine acceptance from both manifests:

```sh
python3 deploy/failoverlab/crosshost/verify_pair.py \
  /tmp/fx-engine-left /tmp/fx-engine-right /tmp/fx-engine-verified
```

Do not repeat a failed probe or overwrite an evidence directory. Preserve failures.

## Executable integration contract

Each backend receives an additional owned `mg0` veth to a new per-group `-mgmt`
namespace. Engine `198.19.1.1/30` reaches collector/upstream `.2/30` independently
of the DR segment. No default route or host networking changes are made. The real
`/ready` endpoint is read over this attachment at startup and finish, while actual
OTLP and upstream traffic exercise it during queries. This proves a disposable
management attachment, **not** an enrolled management control stream's continuity.

`engine-prepare` writes protobuf `ConfigSnapshot`, strict TOML, and fresh state/blob
directories under new evidence directories. Five listeners bind only the lab VIP.
Inherited `NEXORA_*` overrides are removed from spawned processes. Acceptance
requires applied version 1, exact READY listeners, installed TLS and lifecycle health.
There are no management URLs or join tokens. Standalone engines have no enrolled ID;
OTLP resource node names identify each disposable backend.

Source `/32` policies select different rewrite sets. For both rounds and all five
transports, g1 returns `203.0.1.10`/`.11`, g2 returns `203.0.2.10`/`.11` according to
the original `.10`/`.11` client. Every client socket binds its specified source IP.
Cross-group delivery or source loss fails the answer and/or log checks.

A second two-round set uses >2400-byte TXT+RRSIG replies from the isolated TCP
upstream. TCP, DoT, HTTP/2 DoH and DoQ must retain and cryptographically verify the
large reply against the lab certificate's pinned public key. UDP advertises 1232
bytes and **must truncate**. Each is an original request; no transport fallback or
query retry masks failure. These exercise segmentation over the configured inner
MTU, not arbitrary PMTU or IP fragmentation. Signatures test payload integrity,
not engine DNSSEC chain validation: forwarded validation is disabled in this
isolated snapshot and no public or existing trust anchor is changed.

The collector persists actual OTLP protobuf-JSON and fsyncs before ACK. A failed
write fails export. The pair verifier requires forty exact bidirectional tuples
per group (eighty total), twenty query logs per backend, exact original query
counts, client IP, group, transport, type, response code, filter result and resource
identity. Every backend must serve each client/transport/payload combination.
Each original policy and signed-payload result also records its actual socket's
`local_ip`, `local_port`, `remote_ip`, `remote_port`, and DNS `question_type`.
UDP/TCP/DoT use the connection passed to the DNS exchange; HTTP/2 uses `GotConn`;
DoQ uses the packet socket passed to QUIC and the connection's remote address.
Source ports are absent from Nexora query logs. The verifier joins each original
socket tuple to exactly one captured backend, then requires that backend's OTLP
record for the same client, policy group, transport, question and type. Backend
identity remains in the exact-count comparison. Socket reuse/collisions, swapped
backend records, missing tuples and extra flows fail closed. Ports never come
from echo response strings.

Rebuild the probe and rerun both hosts after this verifier fix. The old 80-flow
run `57f032b7` lacks original socket evidence and cannot establish this joint
proof; do not retrofit ports into that evidence or reuse it as acceptance.

The supervisor permits a bounded telemetry drain after probes and refuses finish
without all local query records. Child death, health failure, malformed/missing
records, capture loss and cleanup errors remain red. Namespace deletion continues
after other cleanup errors; SIGKILL escalation makes cleanup red. READY is not
acceptance: only the stopped pair verifier can report the bounded result.

## Controls and remaining work

Real mode accepts only `--mode dr`. Run SNAT and duplicate advertisement controls
as fresh **echo** experiments. SNAT must finish each DNS/TLS exchange and report
exactly the translated `.12`/`.13` address in a structured mismatch; timeout, TLS
failure, `.120`, wrong transport and duplicate results fail the control. This tests
the source-loss detector, not production frontend SNAT or engine negative policy.
Duplicate mode must observe exactly the frontend and both assigned backend MACs;
an arbitrary extra responder is not a control pass. iputils `arping -V` is checked;
SNAT needs executable iptables plus namespace-local NAT/conntrack/SNAT kernel
support. Neither control can enter positive pair acceptance.

Linux/cross-host real-engine acceptance and both negative controls remain unrun in
this worktree. Managed control-stream continuity, ACL/rate-limit variants,
multi-route source return, independent host attestation, sustained isolation,
TCP/QUIC connection reuse/continuity, arbitrary PMTU, frontend HA/fencing and
performance remain gates. FG-01 is open; no production adapter or migration is
claimed. Parent owns supported Linux execution and integration.
