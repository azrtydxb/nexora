# nexora-v1

Status: complete

## Problem

Three prior attempts (Nexora TS, dns-go, dns-c — see ~/Development/nexora-reference)
each delivered part of the product but none delivered a DNS server that is both
feature-rich and fast. Nexora had the right features and GUI but collapsed under
hot-path logging, DB writes and re-encoding; dns-go measured 2K QPS because
upstream lookups blocked socket readers; dns-c was single-listener, insecure
(predictable txn IDs/ports) and never got a control interface. Across all three,
features were documented and shown in the GUI but never wired into the query path.

## Users

Two deployment audiences, both first-class in v1:

- **Homelab / prosumer operator** — runs Nexora on one or a few boxes for a
  home or small network. Needs a GUI-first setup, ad/malware blocklists,
  per-client visibility (query log), local zones for internal names, and
  low resource use.
- **ISP / high-scale operator** — runs a fleet of engines serving many
  clients. Needs high QPS and low tail latency, full recursion, central
  fleet management, metrics for their monitoring stack, and safe config
  rollout across nodes.

## In scope

v1 is delivered in five milestones. The engine <-> management-plane contract
is multi-node-capable from M1 even though fleet features land in M5.

- **M1 Forwarder + fast path:** S-1, S-4, S-12, S-13, S-14, S-15, S-16, S-20, S-21, S-22, and the
  UDP/TCP/EDNS0 part of S-6.
- **M2 Client transports + policy:** DoT/DoH/DoQ part of S-6, S-10, S-11.
- **M3 Recursion + validation:** S-2, S-7, S-9.
- **M4 Authoritative:** S-3, S-8, S-23, S-17, S-18, S-19.
- **M5 Fleet:** S-5.

- [S-1] Caching forwarding resolver (upstreams over UDP/TCP and encrypted transports).
- [S-2] Full iterative recursion from root hints.
- [S-3] Authoritative serving of locally managed zones.
- [S-4] Blocklist subscriptions (hosts, AdBlock, domain-list formats) fetched
  from URLs on a schedule, with allowlist overrides.
- [S-5] Multi-node: one management plane controlling N engines on different hosts.
- [S-6] Client-facing transports: UDP/TCP 53 with EDNS0 (truncation + TCP
  fallback, DNS cookies), DoT (853), DoH (443), DoQ.
- [S-7] DNSSEC validation for recursive and forwarded answers (bogus ->
  SERVFAIL, AD bit on secure).
- [S-8] Online DNSSEC signing of authoritative zones with key management.
- [S-9] RPZ policy zones loaded from file or AXFR/IXFR.
- [S-10] Per-client policy: filtering lists/rules selected by client CIDR or group.
- [S-11] Safe-search enforcement and custom DNS rewrites.
- [S-12] Query log: per-query records (client, name, type, rcode, latency,
  cache/filter outcome) captured off the query path and browsable in the GUI.
- [S-13] Metrics: Prometheus endpoint on engine and management plane.
- [S-14] Go management plane: API, storage, auth, config push to engines.
- [S-15] React GUI covering every in-scope feature of the milestone shipped.
- [S-16] Performance gate: dnsperf benchmark with enforced targets (see Constraints).
- [S-17] Zone transfers out: AXFR/IXFR to secondaries, NOTIFY sent on change.
- [S-18] Secondary zones: AXFR/IXFR in from external primaries, incoming NOTIFY
  handled; dynamic updates (RFC 2136) authenticated with TSIG.
- [S-19] BIND-format zone file import and export.
- [S-20] Authentication and authorization: local users, roles (admin,
  operator, viewer), optional OIDC SSO, API tokens, audit log of every change.
- [S-21] Observability export: OTLP logs/metrics/traces to an OpenTelemetry
  Collector, Prometheus scrape endpoints, optional dnstap output, sampled
  per-query traces (head sampling + always slow/SERVFAIL).
- [S-23] Key storage: DNSSEC private keys and TSIG secrets encrypted in
  PostgreSQL under a key-encryption key (env var or file), with a PKCS#11 HSM
  backend as an alternative in v1. Engines receive key material only over
  mTLS and hold it in memory only.
- [S-22] Query-log backends read by the GUI: Elasticsearch/OpenSearch adapter
  and a built-in fixed-size store for small deployments.

## Out of scope

- DHCP server (Nexora only accepts RFC 2136 updates from existing DHCP servers).
- mDNS, ZONEMD, ODoH, catalog zones.
- Engine on Windows or macOS (Linux only; no macOS dev build promise).
- Kubernetes operator / CRDs (Helm chart only).
- deb/rpm packages and static binary tarballs.
- Storing query logs in PostgreSQL; ClickHouse and Loki query-backend adapters
  (the interface allows them later).
- Management-plane-managed PostgreSQL HA (operator's responsibility).
- Reuse of dns-c engine code or dns-go/Nexora runtime code (reference only;
  tests, corpora, schema and GUI components may be ported).

## Constraints

- DNS engine: Rust, hickory-proto used as wire codec only; server loop, cache
  and resolver are Nexora code.
- Platform: engine targets Linux only (free to use SO_REUSEPORT, recvmmsg,
  io_uring and other Linux-specific APIs). Management plane and GUI ship as
  Linux builds/containers.
- Deployment: multi-node from v1 — one management plane controls N engines
  across hosts; single-host deployment is the N=1 case of the same model.
- Management plane: Go.
- GUI: React on Vite (not Next.js).
- Performance (reference box: 8-core x86_64 Linux, 10GbE, dnsperf from a
  separate load host): cache-hit throughput >= 1,000,000 QPS; p99 latency
  < 500 us at sustained load; a > 5% regression against the recorded baseline
  fails the gate. This implies batched I/O (recvmmsg/sendmmsg or io_uring)
  from M1.
- Performance gate is two-tier: every PR runs a relative dnsperf regression
  benchmark on a CI runner (fails on > 5% drop vs main's baseline); the
  absolute 1M QPS / p99 < 500 us gate runs on a self-hosted reference box
  nightly and before every release tag.
- Management plane is stateless: all state in PostgreSQL, any number of
  instances behind a load balancer, engines may connect to any instance.
- Distribution: multi-arch (amd64, arm64) OCI container images for engine and
  management plane, a docker-compose example, and a Helm chart.
- Security: DNS upstream queries use random transaction IDs and random source
  ports and match responses on address + ID + question (fixes dns-c's
  poisoning hole); resolvers refuse recursion to clients outside configured
  ACLs by default; no default passwords (first admin set at install).
- The query path never logs synchronously, never touches a database, and never
  performs per-packet heap allocation on the cache-hit path.

## Interfaces

- **Engine <-> management plane:** gRPC over mTLS, engine dials out to the
  management plane (no inbound control port on engines). Management plane
  streams versioned config snapshots; engine acks the applied version (or a
  rejection with reason) and streams stats and health back. Engines persist
  their last applied snapshot locally and keep serving on it when the
  management plane is unreachable.
- **Management plane API:** HTTP JSON API described by an OpenAPI spec (the
  GUI's typed client is generated from it). Auth via session cookie (GUI),
  OIDC, or bearer API tokens.
- **GUI:** React + Vite single-page app served by the management plane.
- **Observability:** OTLP (gRPC/HTTP) export for logs, metrics, traces;
  Prometheus `/metrics` on engine and management plane; dnstap (optional).
- **DNS:** UDP/TCP 53, DoT 853, DoH 443, DoQ; AXFR/IXFR, NOTIFY, RFC 2136 +
  TSIG.

## Data

- **PostgreSQL** (owned by management plane): engines/nodes, config
  snapshots with version history, forwarders, filter lists and policies,
  zones and records, DNSSEC keys, TSIG keys, users/roles/tokens, audit log.
- **Engine local state:** last applied config snapshot on disk (without key
  material); in-memory
  cache; DNSSEC trust anchors.
- **Query logs:** not stored in PostgreSQL. Emitted as OTLP logs via an
  OpenTelemetry Collector to the operator's backend; GUI reads through a
  pluggable query-backend interface (v1 adapters: Elasticsearch/OpenSearch,
  built-in fixed-size store).

## Edge cases

- **Hostile or malformed packets:** truncated headers, compression pointer
  loops or forward pointers, names > 255 octets, labels > 63, counts that
  exceed the packet, inbound QR=1 → FORMERR or silent drop, never a panic or
  unbounded work. Unknown opcodes → NOTIMP.
- **Name case:** cache and policy keys are case-insensitive; 0x20-randomised
  queries get the client's casing back.
- **Labels containing escaped dots or binary octets** round-trip unchanged
  (dns-c flattened them).
- **Cache TTLs:** TTLs are decremented on every served answer; TTL 0 answers
  are not cached; negative answers cached per SOA minimum (RFC 2308).
- **Thundering herd:** N concurrent misses for the same (name, type, class)
  send one upstream query and all N clients get the answer (dns-c's dedup
  waiters hung).
- **EDNS sizing:** answers larger than the client's advertised buffer (or 1232
  without EDNS) are truncated with TC=1 and served in full over TCP.
- **CNAME/DNAME chains:** followed to a bounded depth; loops → SERVFAIL.
- **Filtering:** blocklists with millions of entries; wildcard/subdomain
  matches; allowlist beats blocklist; per-client policy beats global; invalid
  lines in a list are skipped and counted, not fatal; CNAME-cloaked trackers
  (blocked name reached via CNAME) are blocked.
- **Config swap under load:** a new snapshot applies atomically; in-flight
  queries finish on the old snapshot; no query sees a half-applied config.
- **Stale engine:** an engine reconnecting with an old snapshot version is
  brought to current; an engine reporting a newer version than the database
  (restored backup) is flagged, not silently downgraded.
- **Concurrent edits:** two operators editing the same zone or policy get
  optimistic-concurrency conflicts, not lost writes.
- **Zones:** SOA serial arithmetic per RFC 1982 including wraparound; IXFR
  falls back to AXFR when history is missing; dynamic updates to signed zones
  are re-signed.
- **DNSSEC:** unsupported algorithms → insecure, not bogus; clock skew near
  signature validity edges; NSEC3 with excessive iterations treated per
  RFC 9276.
- **Encrypted transports:** DoH GET and POST, HTTP/2 multiplexed streams; DoQ
  one stream per query; TLS certificate rotation without restart.

## Failure modes

- **Management plane unreachable:** engines keep serving on the last applied
  snapshot; stats and logs buffer in bounded memory and drop oldest with a
  counter; reconnect with jittered backoff.
- **Engine restart without management plane:** loads last snapshot from disk
  and serves before connecting.
- **PostgreSQL unavailable:** API writes return 503 with a clear error; GUI
  shows degraded state; engines unaffected.
- **Invalid snapshot:** engine rejects it, keeps the previous one, reports the
  reason; management plane shows the node as rejected for that version.
- **Snapshot disk write fails:** engine keeps serving the in-memory config and
  reports the error.
- **Upstream slow or dead:** per-upstream timeouts and health scoring route
  around it; if all upstreams fail, serve stale cached answers (RFC 8767)
  where available, otherwise SERVFAIL.
- **Root/authoritative servers unreachable or lame:** marked in an
  infrastructure cache with backoff; recursion tries remaining servers.
- **Blocklist or RPZ source fetch fails:** keep last good version, surface
  staleness in GUI and metrics.
- **OpenTelemetry Collector or dnstap sink down/slow:** export is dropped from
  a bounded buffer with a drop counter; never back-pressures the query path.
- **Elasticsearch/OpenSearch unavailable:** GUI query-log view shows backend
  unavailable; all other GUI features work.
- **OIDC provider down:** local users can still log in.
- **Memory pressure:** cache is bounded by configured bytes and evicts;
  engine never grows unbounded.
- **Trust anchor rollover:** RFC 5011 automated updates; failure alerts
  before the old anchor expires.

## Acceptance criteria

All criteria below are verified by automated tests. The end-to-end suite is
Go (`go test ./e2e/...`) driving real engines, management plane, and fixture
upstreams/secondaries in containers over the wire; GUI tests are Playwright,
invoked from Go wrappers so each criterion has one `TestXxx` name. Engine unit
tests (`cargo test`) back these but criteria cite the black-box test.

- [ ] [S-1] A cache miss is forwarded to a configured upstream and the second identical query is answered from cache with a decremented TTL — `TestForwardCacheTTL`; fails if the second query reaches the upstream or TTL is unchanged.
- [ ] [S-1] With the primary upstream blackholed, queries succeed via the secondary within 1 s — `TestUpstreamFailover`; fails if any query SERVFAILs while a healthy upstream exists.
- [ ] [S-1] 1,000 concurrent misses for one name produce exactly one upstream query and 1,000 answers — `TestDedupAllWaitersAnswered`; fails if any client times out or upstream count > 1.
- [ ] [S-4] A subscribed hosts/AdBlock/domain list blocks its names, an allowlisted name in it resolves, and a failed refresh keeps the previous list — `TestBlocklistSubscription`; fails if a listed name resolves or a fetch error empties the list.
- [ ] [S-6] Answers larger than the client buffer come back TC=1 over UDP and complete over TCP; DNS cookies are echoed — `TestEDNSTruncationTCP`; fails if a truncated answer is cached or served without TC.
- [ ] [S-6] The same query succeeds over DoT, DoH (GET and POST) and DoQ — `TestEncryptedTransports`; fails if any transport returns a different answer than UDP.
- [ ] [S-6] Fuzzing the packet parser for 1 hour finds no panic — workflow `.github/workflows/fuzz.yml` (`cargo fuzz run parse_query`); fails if any crash or timeout occurs.
- [ ] [S-2] With no forwarders configured, names resolve from root hints, and a spoofed reply with a wrong ID, port or question is rejected — `TestRecursionRootHints` and `TestSpoofedReplyRejected`; fails if the spoofed answer is cached.
- [ ] [S-7] A signed zone returns AD=1, a deliberately broken signature returns SERVFAIL, an unsigned zone returns AD=0 — `TestDNSSECValidation`; fails if bogus data is served.
- [ ] [S-9] An RPZ zone loaded by file and by AXFR rewrites a matching query to NXDOMAIN or a local answer per its policy — `TestRPZPolicy`; fails if the matching query resolves normally.
- [ ] [S-10] Two clients in different CIDR groups get different filtering results for the same name — `TestPerClientPolicy`; fails if both get the same answer.
- [ ] [S-11] With safe search on, `www.google.com` resolves to the SafeSearch address, and a custom rewrite returns its configured record — `TestSafeSearchRewrites`; fails if the original address is returned.
- [ ] [S-3] A zone created via the API answers authoritatively (AA=1) on every engine within 5 s — `TestAuthoritativeZonePropagation`; fails if any engine answers without AA or after 5 s.
- [ ] [S-17] Editing a primary zone sends NOTIFY and a BIND secondary picks up the change via IXFR — `TestAXFRIXFROut`; fails if the secondary's serial does not advance.
- [ ] [S-18] A secondary zone pulled from an external primary updates on NOTIFY, and a TSIG-signed RFC 2136 update adds a record while an unsigned one is REFUSED — `TestSecondaryAndDynamicUpdate`; fails if the unsigned update is applied.
- [ ] [S-8] A zone with signing enabled validates with `delv` and still validates after a ZSK rollover — `TestDNSSECSigningRollover`; fails if `delv` reports bogus at any point.
- [ ] [S-23] [S-8] A zone signed with keys in the PKCS#11 backend (SoftHSM fixture) and a zone signed with KEK-encrypted Postgres keys both validate with `delv`, the `dnssec_keys` table contains no plaintext private key, and engine snapshot files on disk contain no key material — `TestKeyStorageBackends`; fails if a plaintext key is found in the database or on engine disk, or either backend fails to sign.
- [ ] [S-19] Importing a BIND zone file and exporting it yields the same record set (compared by `ldns-compare-zones`) — `TestZoneFileRoundTrip`; fails if any record differs.
- [ ] [S-5] With three engines on separate hosts, one config change reaches all three and each acks the same version; stopping the management plane leaves all three answering — `TestFleetRolloutAndPartition`; fails if any engine stops answering or acks a different version.
- [ ] [S-14] An invalid snapshot is rejected by the engine, which keeps serving the previous config and reports the reason in the API — `TestInvalidSnapshotRejected`; fails if the engine applies it or stops serving.
- [ ] [S-14] Two management plane instances behind a load balancer serve the API and an engine fails over between them — `TestMgmtStatelessHA`; fails if killing one instance breaks the API or the engine's control stream for more than 10 s.
- [ ] [S-20] A viewer cannot change config (403), an operator can, every change appears in the audit log with actor and diff, OIDC login works, and local login still works with the OIDC provider down — `TestAuthRBACAuditOIDC` (Go API + Playwright); fails if a viewer write succeeds or a change has no audit entry.
- [ ] [S-12] [S-22] A query made against the engine appears in the GUI query log within 10 s using both the built-in store and the OpenSearch adapter — `TestQueryLogBackends` (Playwright); fails if the query is missing from either backend.
- [ ] [S-21] With the OpenTelemetry Collector stopped, engine QPS stays within 5% of baseline and the export drop counter increases — `TestOTelSinkDownNoBackpressure`; fails if QPS drops > 5% or the counter stays at zero.
- [ ] [S-13] [S-21] Engine and management plane expose Prometheus `/metrics` with QPS, latency histogram, cache hit ratio, and upstream health; a SERVFAIL query produces a trace in Jaeger — `TestObservabilityMetricsTraces`; fails if any of these metrics is missing or the trace is absent.
- [ ] [S-15] Every API resource in the milestone being shipped has a GUI screen exercised by a Playwright test — `TestGUICoverage` compares OpenAPI operations to Playwright-covered routes; fails if an operation has no covering test.
- [ ] [S-16] PR CI runs the relative dnsperf benchmark and fails on a > 5% QPS drop; the nightly reference-box job enforces cache-hit >= 1,000,000 QPS and p99 < 500 us — workflow `.github/workflows/perf-gate.yml`; fails if either threshold is missed.

## Open questions

