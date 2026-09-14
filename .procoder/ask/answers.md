# What a human decided

Written 2026-09-14 16:05 UTC. procoder reads this
file to avoid asking a question twice; edit an answer here to change what
it believes. Reword the question and it will be asked again.

## [decision] decisions.md

Key: 24d31879fc98
Question: Nexora v1 spec interview (2026-09-13)

All answers recorded in .procoder/specs/nexora-v1.md.

Answer: Not a question — a log entry; every spec decision was answered by the user in the interview and is recorded in .procoder/specs/nexora-v1.md

## [decision] decisions.md

Key: 429b03f7094a
Question: Engine language and base

- Rust engine on hickory-proto codec, own server loop/cache/resolver (recommended)
- Rust, adopt hickory-server/recursor more wholesale
- C++ fresh engine (not dns-c), sanitizers + fuzzing from day one

**Answer (2026-09-13):** Rust + hickory-proto codec; own server loop, cache, resolver.

Answer: Rust engine on hickory-proto codec, own server loop/cache/resolver (chosen by the user via the question tool on 2026-09-13)

## [decision] decisions.md

Key: 6b55131ee473
Question: Static egress IP for Nexora engines on kw (UniFi DNS interception bypass)

- Cilium Egress Gateway: one static IP for all engines via a gateway node (needs enable-bpf-masquerade + enable-ipv4-egress-gateway, rolling cilium restart; gateway node is a single point of failure in OSS Cilium)
- Multus + macvlan: engines get their own LAN IPs from a small static range (new cluster component; engine gains an outbound source-address setting; no single point of failure)
- kube-vip egress annotation on the DNS Service (only works for the engine on the VIP node; not viable for a DaemonSet)

Answer: None of the options — the user disabled the UniFi DNS content filter (the redirect source) instead; kw nodes now use 192.168.10.1 as resolver and kw runs recursive mode (issue #1, 2026-09-14).

## [decision] decisions.md

Key: 716c6aaeda1d
Question: Enlarge kube-vip pool and add second Nexora DNS IP

- Apply now: pool 192.168.10.120-137,139-154 (skip .138), kube-vip per-service election on, second default-group DNS IP 192.168.10.139 on a different node than .136 (user confirms UniFi DHCP excludes 139-154)
- Wait: user first checks UniFi DHCP range/fixed IPs and the .138 device
- Different range: user provides another block

**Answer (2026-09-14):** Apply now — user confirmed UniFi DHCP excludes 192.168.10.139-154.

Answer: Apply now — user confirmed UniFi DHCP excludes 192.168.10.139-154 (2026-09-14); applied: pool 120-137,139-154, svc_election=true, vip_nodename=spec.nodeName, nexora-dns-2 on 192.168.10.139.
