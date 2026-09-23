## Optional dnstap in the full-gap completion scope

The wave4 audit found that the v1 observability specification mentions optional
dnstap output, but no sink/configuration implementation or corresponding test was
found. Prometheus and OTLP implementation do not establish dnstap support.

- Include a disabled-by-default, bounded dnstap output with sink-down/drop-accounting
  tests in the current approved completion work.
- Explicitly exclude dnstap from this release while retaining Prometheus/OTLP;
  record that scope decision rather than treating missing code as implemented.

Awaiting the user's scope decision. Independent approved implementation continues.
Existing explicit holds on DHCP, packages/tarballs and reference-hardware work,
and the planning-only split-DNS scope, are not silently reversed by this question.

## First-class failover groups with transparent load balancing

**Answer:** The user approved implementation: each failover group owns one stable
frontend IP and two engines behind a redundant dedicated load-balancer layer.
Forwarding must preserve actual client source IPs, not replace them with the LB
address or rely only on forwarded metadata. Initial kw groups keep `.136` for A/C
and `.139` for B/D. Group membership, health and coordinated upgrades are separate
from DNS policy groups. Cover UDP/TCP DNS, DoT, DoH and DoQ. Decouple frontend
ownership from engine failure; also test LB failure. This supersedes pursuing a
cluster-wide Cilium migration as the implementation strategy. Dedicated transparent
forwarding still needs explicit return-path and network validation; do not claim
it is configuration-free or already implemented.

## kw development network changes and disruptive verification

**Answer:** The user corrected the environment classification: kw is development;
there is no production environment in this work. “You can do whatever is needed”,
followed by “do it”, authorizes necessary cluster/CNI changes and disruptive tests
for the current forwarding/failover work, superseding the isolated-only restriction
below. No further production-change approval is needed. Preserve persistent data,
secrets, the two existing DNS VIPs, client attribution, rollback access and honest
acceptance results. Recover the retained lock only after quiescence/state checks.
Historical notes saying “production” refer to this same dev cluster.

## Source-preserving cross-node DNS forwarding feasibility

Abrupt engine-a removal produced a TCP refusal on `.136`. Read-only diagnosis confirms Local-policy external forwarding uses only the announcing node's local member, and kube-vip withdrew the address before the other node acquired it. Cilium currently uses VXLAN and SNAT; blindly changing to Cluster policy risks losing client IPs. Evidence: `.procoder/notes/kw-member-failure.md`.

- Recommended: investigate and test a source-preserving cross-node forwarding design in isolation, including DSR feasibility and all five DNS transports. No production CNI changes under this approval; return with supported configuration, measured behavior and blast-radius review before any cluster-wide change.
- Hold network redesign and retain current paired deployment with abrupt failover acceptance explicitly red.

**Answer:** The user approved the isolated source-preserving forwarding/DSR feasibility test with “do it”. This does not relax acceptance, add a production VIP, authorize production CNI changes, or release the retained deployment lock. Return with supported configuration, measured behavior and blast-radius review before any production change.

## Four kw engines in two protected DNS failover pairs

Requested: scale kw DNS engines while keeping only the existing LAN DNS addresses. The initial request for three engines was superseded by four engines: two behind 192.168.10.136 and two behind 192.168.10.139. Never deliberately upgrade both members of one pair together.

- Implement four engines in two distinct-node pairs, with permanent Helm configuration, pair-aware disruption protection and a deployment sequence that verifies Ready, management connection, DNS answers and expected applied configuration before advancing to a partner.
- Keep the existing two-engine topology.

**Answer:** Implement the four-engine paired topology; no third IP. The user approved the described rollout safeguards with “ok do it”, and then requested continuing the previous GUI/deployment and control-plane verification work afterward. This does not authorize weakening acceptance tests or claiming zero-loss failover without measured evidence.

## GUI clarity fixes and kw deployment

Requested: fix query-log columns overflowing, improve AI response formatting, make the dashboard health score intuitive (10 healthy, 0 poor), and finish the pending profile alignment, AI Run now buttons and unmeasured upstream RTT presentation.

- Finish, test, commit and deploy the discussed fixes to kw with DNS continuity probes and post-deploy acceptance.
- Hold for an interactive review before deployment.

**Answer:** Continue all these fixes, then deploy and test while the user is away. This includes changing the insight API score to `max(0, 10 - 3 × critical - warning)` over open insights, with clear loading/error states. Rollout risk scores are separate and unchanged. Do not change recursive/forwarding mode merely to populate RTTs. Split-horizon DNS stays a separate planning effort; Pi-hole parity gaps remain excluded.

## Pi-hole feature parity: out of product scope

The comparison in `.procoder/notes/pihole-feature-gap-analysis.md` identified missing or partial Pi-hole features and suggested possible priorities.

- Add selected parity features to Nexora's roadmap.
- Keep all identified gaps out of scope because they do not fit this product.

**Answer:** Do not add any of the identified Pi-hole gaps; they are not for this product. Retain the comparison as research only, with no backlog items or implementation work arising from it. This does not change the separate split-horizon DNS planning decision or the pending profile alignment fix.

## Split-horizon DNS scope

Requested: different answers for the same zone based on request source, such as private addresses for internal clients and public addresses for external clients. Existing zone query ACLs control access, not different record sets.

- Recommended: zone views selected by explicit IPv4/IPv6 client CIDRs, longest-prefix match, with the existing zone as the default view. Each view has a complete record set; no implicit fallback of missing internal records to the default. Include GUI/API configuration, view-safe caching and query-log attribution. Specify DNSSEC, transfers and dynamic-update isolation before implementation. Use the source IP actually observed by the engine; do not trust arbitrary forwarded headers or EDNS Client Subnet.
- Alternative: individual record overrides selected through existing client policy groups, with shared-record fallback semantics defined explicitly.

**Answer (2026-09-16):** Yes — write the spec and implementation plan for the zone-view approach. This approves planning, not implementation or a production configuration change.

The first draft proposed unsigned primary views only, with split-view DNSSEC and secondary zones deferred. The user was offered that limited scope or including both in the first release.

**Scope answer (2026-09-16):** “include all” — include signed/unsigned primary views, split-view DNSSEC and signed/unsigned secondary views in the first release. No DNSSEC/secondary deferral. Expand `.procoder/specs/nexora-split-dns.md` and `.procoder/plans/nexora-split-dns.md` accordingly; this remains a planning decision, not authorization to change production. Detailed key/trust and replication designs remain subject to architecture review.

## Commit and deploy the kw connection-state fix (2026-09-16)

kw is recovered on `sha-19c08e6`. The local lifecycle-lock fix and stricter AI acceptance checks pass the dev-pod management suite, control race tests, helper tests, and strict acceptance against the existing deployment. The new server binary has not been deployed. The laptop-wide procoder test report failed; scoped Linux dev-pod verification is recorded in `.procoder/notes/kw-deployment-recovery-20260916.md`.

- Commit the fix, build and deploy to kw, then rerun acceptance with DNS probes.
- Hold the tested changes for review; keep the recovered release running.

**Answer (2026-09-16):** Yes — commit and deploy these fixes to kw, then verify acceptance with DNS probes.

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

## Zero-downtime deploys (#53): cluster changes needed on kw

- Upgrade kube-vip v0.8.7 -> v1.2.3 (one control-plane node at a time, API VIP 192.168.10.100 checked after each) and lower the engine CPU request 2 -> 500m (limit stays 4), then deploy with a live zero-loss check
- Keep kube-vip v0.8.7: accept that .136/.139 may move node for a few seconds per deploy; only lower the CPU request
- Code and tests only for now; no kw changes and no deploy yet

**Answer (2026-09-14):** Upgrade kube-vip v0.8.7 -> v1.2.3 one control-plane node at a time (API VIP and LB IPs checked after each) and lower the engine CPU request 2 -> 500m (limit 4), then deploy with a live zero-loss check.

## Publish today's work to GitHub

- Push main (c2476ce, 96962cc, 031a15f) and close #53 with the measured results
- Push main only; leave #53 open
- Keep local for now

**Answer (2026-09-14):** Push main and close #53 with the measured results.

## Milestone order for all open issues

- Operator UX first (#54-#67), then hardening/tech debt, protocols, platform/packaging, data backends and DHCP, AI last
- AI features (#42-#52) right after Operator UX
- Tech debt and v1-unproven items first, then features

**Answer (2026-09-14):** Operator UX first (#54-#67), then hardening/tech debt, DNS protocols, platform/packaging, data backends, AI last.

## LLM provider for the AI features (#42-#52)

- fastllm already running on kw (OpenAI-compatible endpoint), no data leaves the network
- Anthropic API (Claude), key supplied as a Kubernetes Secret
- OpenAI API, key supplied as a Kubernetes Secret
- Provider-agnostic code, tested only against a fake provider until a key/endpoint is given

**Answer (2026-09-14):** fastllm on kw (OpenAI-compatible); code stays provider-agnostic.

## Deploying to kw while the user is away (home LAN depends on .136/.139)

- Deploy after each milestone once all tests pass, zero-downtime path with a live DNS probe; roll back automatically on any lost queries or failed acceptance
- Deploy management plane and GUI changes only; engine changes wait for the user
- No kw deploys; everything stays in the dev pod and local e2e

**Answer (2026-09-14):** Deploy after each milestone once all tests pass, zero-downtime with a live DNS probe; roll back on any lost query or failed acceptance.

## DHCP server scope (#34)

- DHCPv4 only, with automatic DNS registration, off by default, tested only in isolated e2e (never on the home LAN)
- DHCPv4 and DHCPv6, same rules
- Drop DHCP (close #34 as won't do)

**Answer (2026-09-14):** Leave DHCP pending — not decided yet; do not implement #34.

## mDNS support (#30)

- mDNS reflector/gateway: answer unicast DNS queries for .local names from mDNS on the engine's local segment, optional cross-VLAN reflection
- Full mDNS responder advertising Nexora's own services only
- Leave pending (not decided)

**Answer (2026-09-14):** mDNS gateway: unicast .local answers from mDNS on the engine's segment, optional cross-VLAN reflection.

## Engine on Windows and macOS (#40)

- Portable I/O path (no recvmmsg/SO_REUSEPORT fast path) for macOS and Windows, built and unit-tested in CI; macOS also smoke-tested locally
- macOS only
- Leave pending (not decided)

**Answer (2026-09-14):** Out of scope, will not do (close #40).

## Kubernetes operator (#37)

- Operator with CRDs for installations (mgmt + engines, replacing hand-written Helm values) and for engine groups; Helm chart stays
- Declarative config CRDs only (zones, filter lists, policies synced into the management plane, GitOps)
- Leave pending (not decided)

**Answer (2026-09-14):** Operator with CRDs for installations (mgmt + engines) and engine groups; Helm chart stays.

## PostgreSQL HA (#41)

- First-class CNPG integration in the Helm chart (optional HA cluster, backups) plus docs; no HA logic in the management plane
- Built-in HA management in the management plane
- Leave pending (not decided)

**Answer (2026-09-14):** Optional CNPG HA cluster and backups in the Helm chart plus docs; no HA logic in the management plane.

## x86 reference hardware for performance proof (#2, #6, #7, #8)

- Use the arc-azrtydxb-amd64 runner as reference box and novanas (192.168.10.211) as load host; record results even if not a dedicated 10GbE box
- No reference hardware available: close #2/#6/#7/#8 as not measurable for now
- Leave pending until hardware exists

**Answer (2026-09-14):** Leave pending until real hardware exists; do not implement.

## Docker host for the compose example (#5)

- Use novanas (192.168.10.211) as Docker host
- No Docker host: verify compose with a rootless container runtime in the dev pod instead
- Leave pending

**Answer (2026-09-14):** Use novanas (192.168.10.211) as the Docker host.

## ODoH (#32)

- Target and proxy roles (RFC 9230)
- Target role only
- Leave pending

**Answer (2026-09-14):** Target and proxy roles (RFC 9230).

## AI features that change configuration (#46, #45, #51, #49)

- Suggest only: AI produces proposals; nothing is applied until an operator reviews and applies it (audited)
- Allow auto-apply per feature when an operator turns it on explicitly (off by default)

**Answer (2026-09-14):** Suggest only: nothing is applied until an operator reviews and applies it (audited).

## fastllm API key for Nexora's AI features

- User creates Secret `nexora-ai` (key `api-key`, plus model name) in namespace nexora; AI work uses the fake provider until it exists
- Build and test AI against the fake provider only; wire fastllm later

**Answer (2026-09-14):** User supplied base URL http://192.168.10.125:4000/v1 and model qwen3-6-35b-a3b; the API key is stored only in Kubernetes Secret nexora/nexora-ai (never in the repo).

## novanas access for the compose example (#5)

- User gives SSH access (host/user) to a Docker host; until then #5 waits
- Verify compose with podman in the dev pod instead

**Answer (2026-09-14):** User supplied SSH credentials for user piwi on novanas; used once to install an SSH key, not stored.

## New name for the Upstreams menu/page (#57)

- Resolution
- Resolver
- DNS resolution
- Forwarding & recursion

**Answer (2026-09-14):** Forwarding & recursion.

## Engine logs backend for the engine modal (#65)

- Engines keep a bounded in-memory log ring buffer and stream it over the existing control connection (works everywhere, no extra backend)
- Engines export logs via OTLP to the query-log backend (OpenSearch/Loki) and the management plane queries it

**Answer (2026-09-14):** Bounded in-memory log buffer on each engine, streamed over the existing control connection.

## ClickHouse and Loki query-log backends (#35, #36): where to test

- Deploy ClickHouse on kw (namespace nexora) and use the existing Loki in monitoring; also local e2e
- Local e2e only (binaries in the dev pod), nothing new on kw

**Answer (2026-09-14):** Deploy ClickHouse on kw (namespace nexora), use the existing Loki in monitoring, plus local e2e.

## Release channel for packages and tarballs (#38, #39)

- GitHub Releases on azrtydxb/nexora for amd64 and arm64 (tarballs, deb, rpm), built by CI on tags
- Nexus raw/apt/yum repositories on kw
- Both

**Answer (2026-09-14):** Keep #38 and #39 pending (not decided); do not implement.

## kw cluster hostname: move off the `.local` TLD

`.local` is reserved for mDNS (RFC 6762); macOS/Avahi intercept it before unicast DNS, so `*.kw.local` resolution is fragile from the laptop. 71 references across 25 files (deploy/kw, scripts/kw-*.sh, docs, plans/todos), plus deployed ingress hosts and any TLS SANs.

- Migrate now to `*.kw.watteel.lab` (add records + ingress hosts alongside, cut over, retire `.local`)
- File it as a GitHub issue and keep `kw.local` for now
- Leave it as is; `.local` works well enough here

**Answer (2026-09-15):** Migrate now — all migrated to `*.kw.watteel.lab`; `kw.local` is removed and doesn't resolve anymore.

## kw.local certs and ingress hosts that are Helm-owned

DNS for watteel.lab (incl. the kw subdomain) is done on Nexora. The kubectl-managed certs
(nexus-tls, sera-tls) and the novamem ingress now carry the kw.watteel.lab names. The remainder
are owned by Helm releases, so a live `kubectl patch` is reverted on the next `helm upgrade`:
nexora-ingress-tls + ingress (release `nexora`, this repo: deploy/kw/values-kw.yaml),
headlamp, hubble-ui (release `cilium`), kuvryn, kps-grafana (release `kps`), dhole.
Also `nexora-dns-tls` is hand-issued by scripts/kw-deploy.sh with CN=dns.nexora.kw.local.
No certificate on the cluster uses an external domain; all are issued by the internal cluster-ca.

- Change at source: edit each chart/values (nexora here, kuvryn/dhole/novamem repos are local) and redeploy
- Patch live now as a stopgap, accepting they revert on the next helm upgrade
- Leave them on kw.local for now; the user handles the other projects

**Answer (2026-09-15):** The stopgap patches were correct; a permanent fix is needed — every one of them changes at source and everything migrates to `kw.watteel.lab` (`kw.local` is removed and doesn't resolve anymore).

## Next: M9/M11 kw deploy (build+acceptance) with `scripts/kw-deploy.sh` from HEAD

The migration is live, but the cluster still runs `sha-96962cc` — M9 and M11 code (392 files) merged to main has not been deployed. M9 T12 and M11 T32 are open tasks with the kw deploy step pending. The deploy also needs M11 T32's not-yet-written `e2e/kw_ai_test.go`, `values-kw` ai settings, and the acceptance test run. `nexora-ai` secrets exist in both `nexora` and `nexora-dev`.

- Take it on now: write `kw_ai_test.go` + ai settings, run `kw-deploy.sh` from HEAD (build two images, rolling deploy, DNS probe), run `kw-acceptance.sh` (~1 hour of builds + tests); the roadmap mandates deploy after each milestone.
- Defer: leave the open todo items for a dedicated deploy session; the migration is complete and the cluster runs stable on `sha-96962cc`.

**Answer (2026-09-15):** Take it on now — full M9+M11 kw deploy, `kw-deploy.sh` from HEAD, acceptance suite.

## CI trusted-CA rework: provision `NEXORA_CI_CA_PEM` or drop it

The session that died on 2026-09-17 left an uncommitted rework of all four workflows
(`ci.yml`, `fuzz.yml`, `images.yml`, `perf-gate.yml`) plus `scripts/ci-trusted-ca.sh`,
`ci-prerequisites.sh` and two Python contract tests. Every job would start with an
"Install provisioned CI CA" step that validates a single self-signed CA certificate and
installs it into a job-private bundle, instead of fetching the cluster CA over an
unverified HTTPS connection from Nexus as the committed workflows do. The step fails
closed when the secret is absent. `NEXORA_CI_CA_PEM` exists neither as a repo secret
(repo secrets: 0, environments: 0) nor in the `azrtydxb` org. Committing the rework as it
stands therefore breaks every CI job on the first step.

- Provision the secret (the cluster root CA certificate, public, no private key) as a repo
  or org secret, then commit the rework
- Leave the rework uncommitted for now and keep the committed workflows, which pass
- Drop the rework entirely (delete the uncommitted workflow and script changes)

**Answer (2026-09-23):** Provision the secret, then commit. Done: the kw root CA
(`O=Azrty, CN=kw-cluster-internal-ca`, self-signed, CA:TRUE, valid to 2036-05-12) was read
from the kw API (`nexora-ingress-tls`, key `ca.crt`) and set as the repo secret
`NEXORA_CI_CA_PEM`. Its SHA-256 fingerprint matches the copy Nexus serves, so the
previously unverified fetch was authentic. Rework committed as ed98e94.
