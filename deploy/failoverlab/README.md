# Transparent forwarding laboratory

This is an opt-in **same-host namespace fixture**, not a deployable LB or Nexora
engine. It tests IPVS Direct Routing without touching host interfaces, host IPVS
services, existing VIPs, Cilium or engine state. It may load kernel modules.

Build on Linux for the target architecture:

```sh
go build -o /tmp/failover-probe ./deploy/failoverlab/cmd/probe
go test -race ./deploy/failoverlab/...
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s deploy/failoverlab -v
```

Requires root, Linux network namespaces/veth/bridge/IPVS, iproute2, sysctl,
OpenSSL, tcpdump, Python 3 and ipvsadm. The negative control also needs iptables.
Supply an existing ipvsadm binary; the harness does not install packages.

```sh
sudo env FAILOVER_LAB_ENABLE=yes bash deploy/failoverlab/run.sh \
  /tmp/failover-probe /usr/sbin/ipvsadm dr
sudo env FAILOVER_LAB_ENABLE=yes bash deploy/failoverlab/run.sh \
  /tmp/failover-probe /usr/sbin/ipvsadm snat
```

Each invocation creates a private evidence directory printed as `EVIDENCE=...`
and unique owned namespaces. The trap stops their processes, deletes only those
namespaces and removes the generated lab private key. PCAPs, backend socket logs,
capture drop reports and IPVS counters remain for inspection. Do not put the
fixture on a LAN or attach it to an existing pod. SIGKILL cannot run cleanup;
inspect the printed directory's matching `fg-<suffix>-*` namespaces before manual
recovery. It is an isolated test, not a deployment-lock owner.

## Assertions

DR mode: two clients, two rounds, five transports (UDP/TCP DNS, DoT, HTTP/2 DoH,
DoQ). Every transaction checks its DNS response and expected socket-observed
source IP. TLS is verified with a one-day generated fixture certificate. No TLS
termination at the LB, redirects, resolver fallback or application-level retries.
The verifier requires 20 bidirectional source/destination IP+port tuples at both
client and backend boundaries, two flows per client/service, and socket logs
showing all transports on both backends. It rejects incomplete captures and
nonzero capture drops. Verification uses explicit errors, not Python assertions
that disappear under `python -O`.

SNAT mode: inject source loss at the client namespace POSTROUTING boundary
(`.10` becomes `.12`), keeping the same DR datapath. Each transport must fail with
the specific observed `.12` source mismatch; timeout/TLS errors do not count as
successful fault detection. This is a source-loss detector control, **not** a
claim about where an actual deployed LB performs SNAT.

## Limits

Flow-set equality does not measure packet loss, connection continuity or full
payload equivalence. These are small fixture TXT answers, not actual Nexora policy
attribution, large DNSSEC responses or MTU coverage. There is no cross-host
attachment, frontend election/fencing, engine admission/drain, LB failure test or
live migration here. Those gates remain required before deployment/acceptance.
