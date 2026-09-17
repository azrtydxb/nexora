# Failover groups with transparent frontends

## Goal and authorization

Implement the user's approved first-class failover groups: one stable frontend IP,
two engines, and redundant dedicated load balancing. Approval is recorded in
`../ask/decisions.md`. This replaces the old kw Service/Local-specific architecture,
not its identity, policy, sequencing or evidence requirements. Implementation and
dev-cluster testing are authorized; no new product-scope approval is required.

## Contract

- Each group has a stable ID, unique name, unique frontend IP, exactly two distinct
  persistent engine IDs, and an independently reported frontend/LB health state.
- An engine belongs to at most one failover group in this first version. Members
  occupy distinct failure-domain nodes. Availability membership is separate from
  policy membership; both members must serve equivalent effective policy and
  authoritative configuration before becoming eligible.
- The initial kw groups are `dns136` (`192.168.10.136`, A/C) and `dns139`
  (`192.168.10.139`, B/D). No third client-facing IP, no client/DHCP changes.
- One frontend covers UDP/53, TCP/53, TCP/853, TCP/443 and UDP/853. TCP/TLS/QUIC
  payloads pass through to engines without LB TLS termination or DNS rewriting.
- Transparency means engines observe the original client source IP and source
  port, clients receive replies from the selected group IP/port, and client-based
  policy, ACLs, rate limits and logs continue to use that identity. SNAT, arbitrary
  forwarded headers, EDNS Client Subnet and PROXY metadata alone do not satisfy it.
- Backend eligibility changes do not move the frontend IP. Losing one engine must
  leave the LB able to direct new traffic to its partner. Empty eligible membership
  fails closed; never spill traffic to another group or an unrelated resolver.
- At least two LB instances on distinct nodes can serve each frontend. Only the
  elected owner advertises it. Frontend ownership is fenced independently of engine
  readiness; test loss of ownership and partition scenarios. A backend process
  failure is not a reason to withdraw the VIP while a healthy partner remains.
- Drain before planned member maintenance. Never deliberately update both partners
  together. Failed measurements halt progression; preserve previous failure evidence.
- Report desired/applied generation, engine eligibility/reasons, frontend owner,
  degraded/unavailable state and rollout status in API/UI. Do not infer health from
  pod Ready or number of configured members alone. Mutations require existing admin
  authorization, audit, validation and optimistic concurrency.

## Datapath boundary

Keep the existing Cilium VXLAN cluster network. Evaluate an isolated Linux IPVS
Direct Routing frontend first: the LB rewrites only the L2 destination, backend
namespaces accept the group VIP on a non-advertising local address and reply
straight to clients. IPVS handles UDP and TCP without terminating TLS/QUIC.
This is a candidate, not a claim that it works with current pod attachments.

Validate dedicated backend L2 attachments, VIP ARP suppression, source-selected
return routes and anti-spoofing without altering management/control traffic or
allowing two advertisers. Engine c/d share a host today but belong to different
groups: isolated network namespaces must keep VIP/listener/state ownership distinct.
Any secondary backend addresses are infrastructure endpoints, not new client DNS
frontends, and must be allocated collision-safely before attachment to a real LAN.
Do not assume the existing pod overlay is a shared backend L2 segment.

A platform adapter owns only explicitly assigned interfaces, addresses, routes and
IPVS services. Never flush host firewall/IPVS tables, alter unrelated Services, or
use a cluster-wide CNI migration as an implicit dependency. Privileged operations
must be narrowly scoped, idempotent and recoverable. Configuration alone cannot
provide zero-loss abrupt failover or migrate existing TCP/QUIC session state.

## Acceptance criteria

1. Model/API reject duplicate IPs, duplicate or reused members, same-node partners,
   invalid addresses, stale edits and incompatible policy/configuration eligibility.
2. Persistence, API/OpenAPI/generated clients and GUI expose create/edit/list/detail
   and honest health for groups independently from policy engine groups.
3. Packet-level and engine-log evidence proves original IP/port at each member and
   group IP/port on replies for all five transports, with no metadata substitution.
4. Prove both backend paths, not only a lucky local engine. Include two independent
   client IPs, client-specific policy, large DNSSEC replies, UDP truncation, MTU,
   TCP connection reuse and QUIC sessions. No transport fallback masks a failed test.
5. Stop each engine individually: group VIP owner stays stable and new traffic can
   reach the other member. Record all query failures, detection time and recovery;
   do not count successful retry as original-request success.
6. Stop each LB instance individually and exercise ownership/partition handling;
   no persistent duplicate VIP advertisement, cross-group leakage or false healthy
   status. Report established-connection losses separately from new connections.
7. Planned engine/LB upgrades obey independent ownership and partner-health gates.
   Failure/interruption retains the lock; explicit inspected recovery uses CAS.
8. Guarded migration retains both current IPs, all four persistent identities and
   trust material. Exactly one advertisement system owns each IP at any point;
   rollback restores the original frontend without touching engine data.
9. Linux tests, chart checks and strict product acceptance run; successful lab
   forwarding is not release acceptance. Earlier red failover evidence remains.

## Non-goals

No new policy groups, resolver-mode changes, split-DNS implementation, Pi-hole
features, load-balancer TLS termination or claims of uninterrupted existing
connections after backend death. Generalized N-member groups can follow separately.

## Engineering questions

Choose the final transparent datapath only after isolated and cross-host proof.
Determine platform attachment/return-path mechanics, LB election fencing, health
thresholds and connection-state behavior through measured tests before rollout.
These are unresolved engineering gates, not missing user authorization.
