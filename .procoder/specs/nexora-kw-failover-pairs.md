# kw DNS failover pairs

## Goal

Run four engines behind the existing two DNS addresses, with two disjoint, distinct-node pairs. Planned upgrades must not deliberately replace both members of a pair concurrently. The user's approval is recorded in `.procoder/ask/decisions.md`.

## Scope

- 192.168.10.136 selects only pair `dns136`; 192.168.10.139 selects only pair `dns139`.
- Four persistent engine identities, two per pair; no new LAN address and no DHCP changes.
- Preserve DNS policies, engine-group configuration and client source addresses. Failover pairs are Kubernetes availability partitions, not new DNS policy groups.
- Preserve existing engine identities and storage on master-12 and master-13. New engines must not share identity/state directories with each other, including when colocated across different pairs.
- Each pair spans two nodes eligible to announce its VIP. Current kube-vip runs on master-11/12/13 only; do not place a Local-service failover member on an ineligible worker and claim failover protection.
- Pair-specific PodDisruptionBudgets protect voluntary evictions. They do not serialize controller-driven updates; the deployment workflow must do that independently.
- Fail closed on partner health, DNS, management connectivity or applied-version failures. Preserve the healthy partner and require explicit resumption after diagnosis.

## Deployment contract

Stage new engines and verify their readiness, unique identities, DNS answers and applied configuration before changing live Service membership. Preserve existing Service names, addresses and `externalTrafficPolicy: Local`. Never switch a selector to a label absent from the existing serving pods before replacement endpoints are verified.

Freeze unattended engine template updates by default. The supported deployment workflow updates one engine at a time using a same-node surge where possible, waits for the new pod to serve the expected configuration and for its partner to remain healthy, then advances. A shared Kubernetes lock prevents two deployment invocations from operating on partners concurrently; failures retain diagnosable state rather than automatically deleting a lock owned by another process.

A failed verification must halt further engine updates. Retrying the workflow must inspect actual live state, not presume the previous step succeeded. Management replicas may roll separately while engines retain their last good snapshots.

## Acceptance criteria

1. Helm renders four distinct engine workloads and exactly two DNS LoadBalancer Services. Each Service selects exactly its two declared members and retains all existing DNS transports and client-source preservation.
2. Pair members have different node pins. New colocated engines have different hostPath identities; existing paths remain unchanged. Invalid/missing/duplicate pair members fail rendering.
3. Both pair budgets require one available member. Routine chart upgrades alone cannot concurrently roll all four engines.
4. Automated rollout tests prove that an unhealthy partner, a failed DNS probe, a disconnected/stale engine, or a concurrent deployment attempt prevents advancing to the partner. The tests must exercise failure paths, not just inspect script text.
5. Live checks show four connected, unique engines on the expected configuration; EndpointSlices map each VIP to its own pair. DNS probes cover both VIPs and each member.
6. Planned rolling upgrades and controlled single-member failure exercises preserve at least one verified member per pair. Record sampled DNS failures and recovery duration honestly; ARP failover is not a mathematical zero-packet-loss guarantee.
7. Full strict kw acceptance passes with the expected engine count of four. Existing propagation failures and performance-budget failures remain visible until resolved, not relaxed away.

## Non-goals

No third VIP, new DNS policy groups, resolver mode changes, Pi-hole parity work or split-horizon DNS implementation. The earlier GUI changes remain deployed; remaining GUI/deployment verification continues after this topology work.

## Open questions

None about the requested topology or authorization. Implementation feasibility and rollout/failover measurements are engineering checks, not presumed successful outcomes.
