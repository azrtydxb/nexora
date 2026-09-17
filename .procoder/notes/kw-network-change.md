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

**Attempted, failed verification, and restored on 2026-09-17.** Native-routing
cutover is not accepted. Cilium is now revision **10**, all eight agents verified
`Network: Tunnel [vxlan]` with successful status commands. Agents remain OnDelete;
`deploy/kw/cilium-vxlan-recovery.yaml` records that recovery configuration. Nexora
remains revision 35, image `sha-809cf3a`; no engine or DNS Service was changed.

Sequence and observations:

1. After paired preflight/quiescence checks, manually CAS-transferred the retained
   lock from the failed member test to a fresh network-session owner. The patch
   tested UID, resourceVersion, protocol and exact old owner. Ownership remains
   retained, not released; the snapshot and new owner are in the private evidence
   directory.
2. Cilium revision 6 failed on Helm 4 server-side field conflicts with the original
   `cilium` manager. Verified routing/strategy were still unchanged. Explicit
   `--server-side=false` used the client-side Helm update path for revision 7;
   no force ownership or object replacement was used.
3. Converted only workers 21, 22 and 23, one at a time. Workers 21/22 passed paired
   preflight. After deleting worker-23's agent, the temporary runner stopped on
   an empty replacement-pod list (JSONPath indexed element zero too early).
   Replacement subsequently became Ready with native routing. An independent
   paired preflight then **failed** toolbox UDP DNS to `.136` with a deadline.
   Toolbox is on worker-23; laptop VIP queries still returned the expected answer.
   This exposes a mixed-mode pod-path failure; exact dropped-packet location was
   not established. No other worker or master was converted.
4. Restored the saved VXLAN configuration while preserving OnDelete, then restored
   worker-23's agent. Monitoring captured a real laptop UDP `.136` timeout at
   `2026-09-17T10:29:12.381065+04:00`, lasting `2.000992291s`; recovery progression
   stopped. The exact cause of that transient is unproven. After renewed status
   and paired recovery checks passed, explicitly restored workers 22 and 21.
   Each passed paired preflight. This was recovery, not retrying the failed
   migration to declare it green.
5. Final paired preflight passed; all eight Cilium agents report VXLAN. The
   non-Running/non-Succeeded pod inventory matches the before snapshot. Full
   product acceptance and abrupt-failure tests were not rerun.

Raw evidence directory: `/tmp/nexora-kw-network/`. Key files:
`migration-ssa-conflict.log`, `migration-native.log`,
`migration-native-dns.log`, `preflight-worker23.log`, `restore-agents.log`,
`restore23-dns.log`, `preflight-restore23.log`, `restore-remaining.log`,
`preflight-final.log`, `final-<node>.log`, and `config-final.json`.
Cancellation-generated terminal DNS samples are retained separately from the
actual two-second timeout; neither is relabelled a successful sample.

Verification: the Linux toolbox race suite passed (`kwrollout` 24.673s,
`deploytest` 6.951s); both candidate/recovery Helm renders passed. The full laptop
`procoder test` remains red (231 Go failures and Rust `libc::mmsghdr`).
Adversarial finding: the isolated homogeneous native lab did not validate a live
mixed VXLAN/native migration, and the initial readiness poll mishandled an empty
list. Recovery polling handled an empty list without weakening DNS checks.

Next work is a revised migration procedure with mixed-mode packet capture or an
explicit bounded dev maintenance cutover, plus real five-transport DSR attribution
and failover measurements. The user already authorized necessary dev disruption;
this is an engineering gap, not an approval blocker. Do not reapply the experimental
overlay using the failed serial procedure. Bootstrap convergence remains pending.
