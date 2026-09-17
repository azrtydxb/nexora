# Abrupt member failure: acceptance red

On 2026-09-16, ran the opt-in `TestMemberFailureLive` against engine a on production
`sha-809cf3a` (Helm revision 35). The harness acquired the deployment lock, verified the
paired fleet and current management configuration, sampled both VIPs independently over
UDP/TCP, then submitted a zero-grace pod deletion with UID/resourceVersion preconditions.
It never deleted persistent state or stopped another member.

The test failed: TCP to `192.168.10.136:53` returned connection refused at
`2026-09-16T23:42:18.4688+04:00`, after a UDP query at 23:42:18.464841 succeeded.
The first sampled failure cancelled recovery observation, joined the monitor and retained
`nexora-deploy-lock`. No b/c/d failure test was attempted. Do not clear this result by rerunning
or weakening thresholds. Pod failure is not a node-partition test, and API deletion does not
by itself prove the instant the old process stopped.

Deleted pod: `nexora-engine-a-wr756`, UID `5b16cff6-66b6-4011-8cd7-8649fa6251f0`.
Replacement: `nexora-engine-a-gzgv5`, initially not Ready at 7 seconds. Subsequent read-only
paired preflight passed: four distinct identities, complete endpoints, current management/config
and direct/VIP DNS. The observed `.136` lease holder after recovery was master-11; `.139`
remained master-13. This is consistent with a VIP/endpoint convergence window, but the exact
packet-path cause is not established. No kube-vip/Cilium tuning was applied.

Evidence:

- `/tmp/nexora-failure-a.log`: live test failed 3.44s; exact sampled refusal retained.
- `/tmp/nexora-postfailure.log`: early recovery preflight failed while a was not yet Ready.
- `/tmp/nexora-postfailure-recovered.log`: later read-only paired preflight passed.
- `/tmp/nexora-vip-failure-logs.log`: collected kube-vip diagnostics.
- `/tmp/nexora-failure-harness-final.log`: Linux race suite (live disruption skipped without opt-in).

The harness now also explicitly checks all four controllers are OnDelete before disruption.
No second disruption was run after that additional guard. Production image and Helm revision
are unchanged. Earlier strict product acceptance remains a valid separate pass, not proof of
abrupt failover continuity. The bootstrap propagation timing fix remains unimplemented; this
session prioritized the newly measured availability failure and recovery.

## Read-only forwarding diagnosis

The collected kube-vip logs confirm that master-12 lost its last local endpoint and withdrew
`.136` at 19:42:18 UTC. Master-11 acquired the lease at 19:42:19.652882 UTC and then added
and advertised the address. The failed TCP connect was at 19:42:18.4688 UTC, before that
acquisition. These logs establish a handoff window, not its exact client-visible duration.
No packet capture from the failing request exists, so the RST's precise origin is unproven.

Cilium 1.19.4 runtime inspection confirms VXLAN tunnelling, kube-proxy replacement, SNAT
load-balancer mode and iptables masquerading. The post-recovery service maps show that the
external `.136:53` TCP/UDP entries on master-12 contain only its local engine a; the `/i`
internal entries contain a and c. On master-11, external entries contain only c. Thus the
healthy remote partner is not an external forwarding fallback on the old announcing node.
This explains why adding a second endpoint did not eliminate dependence on VIP handoff.

Evidence: `/tmp/nexora-cilium-status.txt`, `/tmp/nexora-cilium-config.json`,
`/tmp/nexora-cilium-services-master12.txt`, `/tmp/nexora-cilium-services-master11.txt`,
`/tmp/nexora-dns-services.json`. No production mutation or further disruption was performed.

Do not switch to Cluster policy under the observed SNAT mode as a quick fix: remote-backend
traffic can lose the client source address, violating policy/log attribution requirements.
Shorter election timers cannot remove asynchronous endpoint, election and neighbor convergence.
A source-preserving cross-node forwarding design (for example a validated DSR configuration)
needs an isolated feasibility test and separate review of CNI-wide blast radius before rollout.
It must cover UDP/TCP DNS, DoT, DoH and DoQ, original client attribution, and abrupt failures;
DSR alone must not be described as a zero-loss guarantee. The retained lock remains untouched.
