# kw dev native-routing change

## Authorization and staged scope

kw is development, not production. The user authorized necessary CNI changes and
disruptive verification; see the superseding answer in `../ask/decisions.md`.

Stage 1 uses `deploy/kw/cilium-native-dsr.yaml` with Cilium 1.19.4 over the existing
Helm values: native routing and automatic direct routes on the common node LAN;
existing Services retain SNAT. OPT DSR becomes available only through explicit
Service annotations. No global DSR switch, identity/IPAM change, new DNS VIP,
engine rollout or persistent-state deletion. Cilium agents are OnDelete for
explicit one-at-a-time replacement. Subsequent steps must test real cross-node
forwarding before changing the two DNS Services to Cluster policy.

## Preconditions and rollback

- Paired preflight passed before mutation; four engines Ready and OnDelete.
- Cilium release revision 5 deployed; Nexora revision 35 deployed.
- No local deployment/failure clients or toolbox rollout commands found.
- SSH by literal node IP and passwordless sudo verified on all eight nodes.
- All eight nodes share `192.168.10.0/24`; Cilium IPAM is cluster-pool
  `10.42.0.0/16`. **Kubernetes node podCIDRs differ from CiliumNode allocations**;
  retain Cilium IPAM and derive routes from CiliumNode, never Kubernetes podCIDRs.
- Current values/manifests/config and health snapshots saved privately in
  `/tmp/nexora-kw-network/`. Cilium rollback target is revision 5. If API pod
  networking breaks, use the literal-IP API endpoint and host SSH. Restore old
  Helm configuration before serial agent replacements; do not delete BPF state,
  CiliumNode allocations, trust material or application state.
- Retained Nexora lock may be recovered only with exact UID/resourceVersion/
  protocol/owner CAS after quiescence checks. Keep ownership during migration;
  failures retain ownership. A lock is cooperative, not server-side CNI fencing.

## Verification plan

Monitor both existing VIPs over UDP/TCP with configured expected answers and no
query retries; stop progression on first failure and preserve evidence. Agent
Ready is insufficient: inspect actual status/datapath and require paired direct
DNS/management/config checks after each replacement before advancing. A failed
migration stage triggers diagnosis/rollback, not an automatic next-node change.

Cross-node DSR must subsequently verify all five DNS transports and source IPs;
then measure paired member loss independently of normal product acceptance. No
zero-loss or complete failover claim is made by this configuration change.

## Current result

Prepared and rendered; deployment result pending. Raw evidence directory:
`/tmp/nexora-kw-network/`. No acceptance closure is implied.
