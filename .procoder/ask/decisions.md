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
