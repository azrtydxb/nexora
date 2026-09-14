## Engine language and base

- Rust engine on hickory-proto codec, own server loop/cache/resolver (recommended)
- Rust, adopt hickory-server/recursor more wholesale
- C++ fresh engine (not dns-c), sanitizers + fuzzing from day one

**Answer (2026-09-13):** Rust + hickory-proto codec; own server loop, cache, resolver.

## Nexora v1 spec interview (2026-09-13)

All answers recorded in .procoder/specs/nexora-v1.md.

## Static egress IP for Nexora engines on kw (UniFi DNS interception bypass)

- Cilium Egress Gateway: one static IP for all engines via a gateway node (needs enable-bpf-masquerade + enable-ipv4-egress-gateway, rolling cilium restart; gateway node is a single point of failure in OSS Cilium)
- Multus + macvlan: engines get their own LAN IPs from a small static range (new cluster component; engine gains an outbound source-address setting; no single point of failure)
- kube-vip egress annotation on the DNS Service (only works for the engine on the VIP node; not viable for a DaemonSet)

## Enlarge kube-vip pool and add second Nexora DNS IP

- Apply now: pool 192.168.10.120-137,139-154 (skip .138), kube-vip per-service election on, second default-group DNS IP 192.168.10.139 on a different node than .136 (user confirms UniFi DHCP excludes 139-154)
- Wait: user first checks UniFi DHCP range/fixed IPs and the .138 device
- Different range: user provides another block

**Answer (2026-09-14):** Apply now — user confirmed UniFi DHCP excludes 192.168.10.139-154.

## Zero-downtime kw deploys (issue #53)

- Rolling fix, pass = a client using both .136 and .139 never fails
- Stricter: zero lost queries per IP
- Change the design

**Answer (2026-09-14):** Stricter — zero lost queries on each DNS address during a redeploy.

## Break the DNS loop before UniFi upstream points to Nexora

- CoreDNS: kw.local -> 192.168.10.1, everything else -> 1.1.1.1/8.8.8.8; nodes back to 1.1.1.1/8.8.8.8; engines recursion with fallback to 1.1.1.1/8.8.8.8 (not on DNSSEC bogus)
- Engines only (no cluster/node change)
- Also other UniFi-local zones besides kw.local

**Answer (2026-09-14):** Engines fallback only — engines recurse first and fall back to 1.1.1.1/8.8.8.8 when recursion fails (not on DNSSEC bogus); CoreDNS and nodes stay on 192.168.10.1.

## Scope of the recursion fallback change

- Minimal: fallback always on in the engine (no setting/API/GUI), plus engine pod dnsConfig 1.1.1.1/8.8.8.8
- Full: keep as a configurable setting with API, GUI toggle and tests (current agent scope)
- Container dnsConfig only, no fallback code

**Answer (2026-09-14):** None of these — the user meant the engine container resolver order 127.0.0.1, 8.8.8.8, 1.1.1.1 (pod DNS config, no code). Fallback code agent stopped and reverted.

## How engines reach cluster names with resolver order 127.0.0.1, 8.8.8.8, 1.1.1.1

- Use addresses instead of cluster names: management URL https://192.168.10.135:9443 (already in the gRPC certificate) and a fixed ClusterIP for the collector
- Nexora forward zone cluster.local -> CoreDNS 10.43.0.10 (also answers cluster names to LAN clients)
- Put CoreDNS first: 10.43.0.10, 127.0.0.1, 8.8.8.8

**Answer (2026-09-14):** None — user said stop, no changes, revert all (scope being clarified).

## Scope of "revert all"

- Only the DNS resolver / fallback work (already reverted); keep the paused rolling-deploy work for later
- Also discard the uncommitted rolling-deploy work (issue #53 stays open)
- Also undo today's applied kw network changes (kube-vip per-service election, vip_nodename, pool 139-154, second DNS IP .139)

**Answer (2026-09-14):** Only the DNS resolver work (already reverted). Keep the paused, uncommitted rolling-deploy work; kw stays as is.

## Nexora recursion ACL on kw

- Keep current ACL (all private ranges incl. 192.168.0.0/16)
- Restrict to 192.168.10.0/24 plus the cluster's own ranges (loopback, pod network 10.42.0.0/16)
- Restrict to 192.168.10.0/24 plus other named subnets

**Answer (2026-09-14):** Keep all private IP ranges allowed (current default ACL); no change.

## Uncached recursion latency before switching the home network (avg 1.2 s vs ~60-100 ms)

- Switch now in forward mode: Nexora forwards to 1.1.1.1/8.8.8.8 with DNSSEC validation and all filtering; later move back to recursive once faster
- Switch now in recursive mode and accept slow first lookups while the cache warms
- Don't switch yet: first improve recursion (parallel DNSSEC fetches, fastest-server selection, prefetch) and re-measure

**Answer (2026-09-14):** Switch now in recursive mode (accept slow first lookups) and in parallel improve recursion latency (parallel DNSSEC fetches, fastest-server selection) without prefetching.
