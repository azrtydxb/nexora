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
