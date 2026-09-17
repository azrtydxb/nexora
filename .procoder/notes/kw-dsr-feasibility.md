# Isolated source-preserving forwarding experiment

## Scope and verdict

The user approved this isolated feasibility test with “do it”. No production CNI,
Service, VIP, engine, or deployment-lock mutation was performed. Production abrupt
member-failure acceptance remains **red**; the retained lock was not released.

**Native-routing DSR preserved the external client IP across all five transports
in a local cross-node fixture.** This is forwarding feasibility, not Nexora product
acceptance, kube-vip continuity, or a production migration recommendation.

**Geneve DSR remains unverified:** the local Docker Desktop kernel rejected tunnel
device creation. No transport result was obtained for that variant.

## Supported configurations

Source: Cilium **v1.19.4** release documentation,
<https://github.com/cilium/cilium/blob/v1.19.4/Documentation/network/kubernetes/kubeproxy-free.rst>,
sections “Direct Server Return”, “Hybrid DSR and SNAT Mode”, and
“Annotation-based DSR and SNAT Mode”. Downloaded source retained at
`/tmp/nexora-cilium-1.19.4-kubeproxy-free.rst`.

- The documented support matrix excludes DSR with **VXLAN tunneling**, kw's
  current mode. This is not a safe Service-only toggle.
- Supported documented candidates include native routing with OPT dispatch and
  Geneve tunneling with Geneve dispatch.
- Hybrid mode retains SNAT for UDP, so it does not meet UDP DNS/DoQ client-IP
  requirements.
- Per-Service forwarding annotations require agent support/configuration and must
  be chosen at Service creation; changing them on a live Service breaks connections.
  Annotation mode is not an escape from the routing-mode compatibility constraints.

## Experiment and evidence

Local Docker Desktop, ARM64, Linux `7.0.12-linuxkit`; three-node kind cluster
`nexora-dsr-lab`, Kubernetes `v1.33.1`, Cilium `1.19.4`. Dedicated kubeconfig:
`/tmp/nexora-dsr-lab/kubeconfig`. Every Kubernetes/Helm command explicitly selected
this file and `kind-nexora-dsr-lab`; the normal context remained `kw`.

Topology:

- External Docker client, outside Kubernetes: `172.23.0.10`.
- Ingress node: `nexora-dsr-lab-worker`, `172.23.0.3`, **no local backend**.
- Only backend: `nexora-dsr-lab-worker2`, pod `10.244.2.182` for the successful run.
- Service: Cluster external traffic policy, five ports, ExternalIP equal to the
  ingress node's existing isolated Docker address. No LAN address advertisement,
  new production VIP, or kube-vip installation.
- The Go DNS fixture returns its observed socket peer IP in a TXT answer. The
  client requires an exact expected address and exits at the first error, with
  five-second per-request deadlines and no retry or transport fallback.
- TLS uses a newly generated **lab-only** certificate with a dedicated trusted
  root and checked `dsr-lab.test` name, not disabled certificate verification.
  This creates no production trust material.

Initial Geneve configuration: `routingMode=tunnel`, `tunnelProtocol=geneve`,
`loadBalancer.mode=dsr`, `loadBalancer.dsrDispatch=geneve`,
`kubeProxyReplacement=true`, `ipam.mode=kubernetes`. Helm readiness passed, but
workload readiness did not. Agent log:

```text
failed to setup geneve tunnel device: setting up geneve device: creating geneve device: creating device cilium_geneve: invalid argument
```

This is an environment/configuration initialization failure; the exact kernel
limitation was not isolated. Cilium pod Ready alone did not prove a working datapath.

A **separate native-routing variant** used `routingMode=native`,
`loadBalancer.dsrDispatch=opt`, `autoDirectNodeRoutes=true`,
`ipv4NativeRoutingCIDR=10.244.0.0/16`, and `loadBalancer.mode=dsr`. Lab agents were
restarted to consume changed configuration. Backend readiness passed. Service
maps on the ingress node showed all five ExternalIP frontends selecting only the
remote backend. Probe exited **0**:

```text
PASS transport=udp source=172.23.0.10 target=172.23.0.3
PASS transport=tcp source=172.23.0.10 target=172.23.0.3
PASS transport=dot source=172.23.0.10 target=172.23.0.3
HTTP protocol=HTTP/2.0
PASS transport=doh source=172.23.0.10 target=172.23.0.3
PASS transport=doq source=172.23.0.10 target=172.23.0.3
```

Negative control: changed only the lab forwarding mode to SNAT, restarted lab
agents, and used a fresh external client `172.23.0.11`. The same strict probe
exited **1** on its first query, as expected:

```text
udp source mismatch: [udp.dsr-lab.test. 0 IN TXT "172.23.0.3"] expected 172.23.0.11
```

This confirms the assertion detects source loss rather than merely DNS reachability.
Later transports were not sampled in the negative control after the first failure.

Raw local artifacts under `/tmp/nexora-dsr-lab/`: `main.go`, `Dockerfile`,
`fixture.yaml`, `create.log`, `cilium-install.log`, `geneve-failure.log`,
`native-install.log`, `native-status.log`, `native-services.log`, `native-probe.log`,
`snat-install.log`, `snat-probe.log`, `backend-final.log`, `checksums.txt`, and
`cleanup.log`. The prototype compiled with the repository's existing Go dependencies
using `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build`; it is not production code.
Private lab key/certificate and kubeconfig remain local, not in this repository.

Cleanup removed the created kind cluster, three experiment client containers and
fixture image. `kind get clusters` reported no clusters; default context was `kw`.
Existing Docker workloads were not stopped.

## Limits and blast-radius review

- Only **one query per transport**, with small TXT replies, on a synthetic backend.
  No engine query-log/policy attribution, DNSSEC-sized answers, MTU/fragmentation,
  sustained traffic, connection reuse, backend failure, node failure, or packet
  capture was tested. No zero-loss claim follows.
- ExternalIP interception exercised remote forwarding, not kube-vip lease/ARP
  handoff. DSR does not eliminate endpoint convergence, stale backend selection,
  established-flow loss, or announcing-node failure.
- Native routing would require validation of pod routes, return paths, IP-option
  handling, MTU, filtering and reverse-path checks on the real underlay. Successful
  Docker-bridge traffic does not validate the LAN fabric.
- Geneve migration would affect the shared cluster CNI, not just Nexora: tunnel
  protocol, agent rollout, firewall/UDP reachability, MTU, other Services and
  rollback all need dedicated review. Neither routing change is authorized.
- Preserve two production VIPs, persistent identities, client attribution and
  strict acceptance. A production design must also address stable announcement
  and endpoint convergence, not only remote forwarding.

## Repository verification

`procoder test` was run after recording the experiment: the laptop-wide suite
remains red with 231 Go failures (including the three Helm rendering tests) and
Rust `libc::mmsghdr` compilation failure. This is not a repository-wide green claim.
The successful Linux experiment above is separate, scoped evidence. No application
source was changed for this experiment.

Adversarial review identified the key false-positive boundary: an ExternalIP on a
live ingress node bypasses kube-vip election, and small successful answers do not
exercise endpoint loss or MTU limits. Those remain explicit untested gates, not
inferred successes.

## Next gate

Use a separate Linux VM/cluster with working Geneve support to test that candidate,
then exercise real Nexora attribution, large messages, reused connections, paired
member loss and announcer loss with packet capture and independent probes. Record
all losses; do not retry failures away. Return with those measurements and a
cluster-wide migration/rollback review before requesting any production change.
The current experiment does **not** justify deploying DSR to kw.
