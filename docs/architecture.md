# Nexora architecture

The spec is `.procoder/specs/nexora-v1.md` (what and why). This document is the
settled _how_: every plan and every implementation task inherits it. Change it
first, then the code.

## Repository layout

```
proto/nexora/control/v1/control.proto   engine <-> management plane contract
engine/                                 Rust crate `nexora-engine` (binary + lib)
  src/main.rs                           CLI entry
  src/lib.rs                            module declarations only
  src/wire.rs                           zero-copy query parser, response writers, RR walker
  src/edns.rs                           OPT parsing/writing, DNS cookies (RFC 7873/9018)
  src/clock.rs                          coarse monotonic seconds clock
  src/cache.rs                          wire-format response cache
  src/acl.rs                            client CIDR allow list
  src/filter.rs                         blocklist/allowlist matcher, per-client policy (M2)
  src/upstream/{mod,udp,tcp,dot,doh}.rs forwarding transports + health
  src/inflight.rs                       cross-worker request coalescing
  src/runtime.rs                        applied-config state (ArcSwap<Runtime>)
  src/server/{mod,udp,tcp}.rs           per-core listeners, query pipeline
  src/server/{dot,doh,doq}.rs           (M2) encrypted client listeners
  src/server/{stream,proxy,tls,rewrite}.rs (M2) shared length-prefixed stream server,
                                        PROXY v2 parser, in-memory certificate store,
                                        rewrite answer synthesis
  src/control.rs                        management-plane client (enroll, stream, blobs)
  src/snapshot.rs                       snapshot validation + persistence
  src/snapshot_m3.rs                    (M3) validation of recursion/DNSSEC/RPZ snapshot fields
  src/telemetry/{metrics,querylog,otlp}.rs
  src/recursor/…                        (M3) iterative resolver, DNSSEC validation, RPZ
  src/authoritative/…                   (M4) zone serving, transfers, updates, signing
  src/cert_renewal.rs                   (M5) certificate renewal timing, CSR, atomic identity swap
  fuzz/                                 cargo-fuzz targets
  build.rs                              tonic-prost codegen from proto/
gen/go/nexora/control/v1/               generated Go protobuf/gRPC (committed)
mgmt/                                   Go management plane (module root is repo root)
  cmd/nexora-mgmt/main.go
  api/openapi.yaml                      HTTP API source of truth
  migrations/*.sql                      goose migrations (embedded)
  internal/config                       env configuration
  internal/secrets                      (M3) NXE1 envelope encryption under the KEK
  internal/store                        pgx pool, migrations, queries
  internal/pki                          CA, engine/server certificates
  internal/control                      gRPC EngineControl server, engine hub
  internal/snapshot                     snapshot builder + publish/notify
  internal/auth                         users, sessions, tokens, RBAC, OIDC, audit
  internal/api                          oapi-codegen strict server + handlers
  internal/blocklist                    list fetcher/parser
  internal/querylog                     query-log backends (builtin OTLP receiver, OpenSearch)
  internal/stats                        engine stats samples
  internal/rollout                      (M5) staged rollout state machine, creation, controller
  internal/fleet                        (M5) engine groups, engine views and targets, join tokens,
                                        certificates, fleet metrics
  internal/webui                        embedded GUI dist
web/                                    React + Vite GUI
  src/api/schema.d.ts                   generated from mgmt/api/openapi.yaml
  e2e/                                  Playwright tests
e2e/                                    Go end-to-end suite (`go test ./e2e/...`)
  harness/                              starts real processes, fixtures, clients
  fixtures/cmd/nexora-fixture/          fixture upstream/blocklist/OIDC/HTTP servers
bench/                                  dnsperf corpora, perfgate tool
deploy/docker/                          engine.Dockerfile, mgmt.Dockerfile
deploy/compose/                         docker-compose example
deploy/helm/nexora/                     Helm chart
deploy/kw/                              manifests for the kw test deployment
deploy/deploytest/                      helm/compose/workflow/docs static tests
deploy/dev/                             dev toolbox image + pod
.github/workflows/                      ci.yml, fuzz.yml, perf-gate.yml, images.yml
```

Go module: `github.com/piwi3910/nexora` at the repository root (`go.mod`),
covering `mgmt/`, `e2e/`, `bench/`, `gen/go/`.

## Development workflow

- The engine is Linux-only. All builds and tests run in the dev pod on kw
  (`deploy/dev/`), image `192.168.10.131/azrtydxb/nexora-dev:<tag>`. Source is
  synced with `scripts/dev-sync.sh` (rsync over `kubectl exec`), commands run
  with `scripts/dev-exec.sh <cmd>`.
- Code is edited on the laptop (the source of truth; the pod copy is
  overwritten by every sync). Generated sources that are committed
  (`gen/go/`, `mgmt/internal/api/gen.go`, `web/src/api/schema.d.ts`) are
  generated on the laptop with `make generate`, which needs only buf, protoc,
  Go and pnpm — never inside the pod.
- `make` targets (run inside the dev pod): `make proto`, `make engine-test`,
  `make mgmt-test`, `make web-test`, `make e2e`, `make lint`, `make build`.
- Images build on the kw BuildKit via `scripts/build-image.sh` and push to
  Nexus `:5000`; deployments pull from `192.168.10.131/azrtydxb/...`.

## Engine

### Process and threads

- `nexora-engine --config /etc/nexora/engine.toml`.
- `workers` threads (default: available cores). Each worker runs a tokio
  `current_thread` runtime and owns one `SO_REUSEPORT` UDP socket and one
  `SO_REUSEPORT` TCP listener per configured listen address.
- UDP receive path: non-blocking socket registered with `AsyncFd`; on
  readiness, `recvmmsg` up to 64 datagrams into per-worker preallocated
  buffers; each datagram is processed synchronously when it can be answered
  from ACL/filter/cache; responses are batched with `sendmmsg`. Cache misses
  spawn a `spawn_local` task that resolves upstream and sends its own reply —
  a miss never blocks the reader.
- A dedicated `nexora-telemetry` thread drains query-log rings and exports.
- A dedicated `nexora-control` tokio multi-thread runtime (2 threads) runs the
  management-plane client and metrics HTTP server.

### Bootstrap file (`engine.toml`)

```toml
node_name = "engine-1"                       # required, [a-z0-9-]{1,63}
state_dir = "/var/lib/nexora"                # required
management_urls = ["https://mgmt:9443"]      # required unless standalone_snapshot set
join_token_file = "/etc/nexora/join-token"   # read only when state_dir/identity missing
listen_udp = ["0.0.0.0:53", "[::]:53"]
listen_tcp = ["0.0.0.0:53", "[::]:53"]
metrics_listen = "0.0.0.0:9153"
workers = 0                                  # 0 = number of CPUs
standalone_snapshot = ""                     # path to a binary ConfigSnapshot; disables mgmt
standalone_blob_dir = ""                     # blobs for standalone mode
# M2: encrypted client transports
listen_dot = []                              # e.g. ["0.0.0.0:853"]
listen_doh = []                              # e.g. ["0.0.0.0:443"]
listen_doq = []                              # e.g. ["0.0.0.0:853"] (UDP)
doh_path = "/dns-query"                      # must start with '/'
proxy_protocol_dot = false                   # expect a PROXY v2 header on DoT connections
proxy_protocol_doh = false                   # expect a PROXY v2 header on DoH connections
proxy_protocol_trusted_cidrs = []            # required (non-empty) when either flag is true
tls_cert_file = ""                           # standalone mode only; set together with tls_key_file
tls_key_file = ""                            # standalone mode only; reloaded on SIGHUP
```

In standalone mode `SIGHUP` reloads the snapshot file. Listen addresses are
host concerns and are not part of the snapshot.

### Hot path rules

- No logging, no allocation, no locks held across packets on the cache-hit
  path. Enforced by `cache_hit_path_does_not_allocate` (counting allocator).
- Config is read through `ArcSwap<Runtime>::load()` once per packet.
- Counters are per-worker `CachePadded<AtomicU64>` arrays summed at scrape.

### Wire handling

- `wire::parse_query(&[u8]) -> Result<QueryView<'_>, ParseError>` validates
  the header (QR=0, QDCOUNT=1, ANCOUNT=0, NSCOUNT=0, ARCOUNT<=1), the question
  name (no compression, labels <= 63, total <= 255 octets), and an optional OPT
  record. It produces a lowercase `NameKey` (inline `[u8; 255]`, no allocation).
- Opcodes other than QUERY -> NOTIMP (M4 adds NOTIFY and UPDATE).
- Malformed header with >= 12 bytes -> FORMERR echoing the ID; < 12 -> drop.
- hickory-proto (`0.26`) decodes upstream responses (validation, CNAME and SOA
  extraction) and is used for tests/fixtures; the query fast path and cache
  writer are Nexora code.

### Cache

- Key: `(NameKey, qtype, qclass, DO bit, CD bit)`.
- Value: `Arc<CachedResponse>` holding the response wire bytes without the OPT
  record, the question name lowercased, offsets of every TTL field, the
  insertion time (coarse seconds), the minimum TTL, the stale deadline.
- Store: `quick_cache::sync::Cache` weighted by entry bytes, capacity
  `cache.max_bytes`.
- Serving copies the bytes into the worker output buffer, patches ID, copies
  the client's question name casing, subtracts elapsed seconds from every TTL,
  appends an OPT record when the client sent one, and sets TC with an empty
  answer when the result exceeds the client's limit (EDNS size, or 512 without
  EDNS; UDP responses are capped at 1232).
- Positive TTL = min RR TTL clamped to `[min_ttl, max_ttl]`; negative
  (NXDOMAIN/NODATA) TTL = min(SOA TTL, SOA MINIMUM) clamped to
  `negative_max_ttl`; no SOA -> not cached; TTL 0, TC=1, SERVFAIL, REFUSED ->
  not cached.
- Serve-stale (RFC 8767): entries are kept `stale_window` seconds past expiry
  and served with TTL 30 only when resolution fails.
- A snapshot with unchanged cache settings keeps the cache; it is cleared when
  the filter lists, policy-group partitions, resolution/DNSSEC/RPZ settings,
  upstream strategy or any upstream change (zone edits do not clear it).

### Upstreams (forwarding)

- Protocols: UDP (TCP retry on TC), TCP, DoT (tokio-rustls, persistent
  pipelined connection per worker), DoH (reqwest HTTP/2 POST
  `application/dns-message`).
- UDP sockets: per worker, per upstream pool of 16 connected sockets bound to
  kernel-random ports; each socket is replaced after 1024 queries. Query IDs are
  random (`rand` OS-seeded CSPRNG) and unique per socket. A reply is accepted
  only if it arrives on the socket the query went out on (connected: source
  address/port enforced by the kernel), has QR=1, a pending ID, and a question
  equal (case-insensitively) to the query's. Anything else is dropped and
  counted in `nexora_upstream_mismatched_replies_total`.
- Strategy `ordered` (first healthy by position) or `fastest` (lowest EWMA RTT
  among healthy, alpha 0.2).
- Per-attempt timeout = upstream `timeout_ms` (default 250). Overall deadline
  2000 ms. Three consecutive failures mark an upstream down for 5 s, then one
  probe query is allowed through.
- Coalescing: `inflight::InFlight` (64 mutex-protected shards of
  `HashMap<CacheKey, Arc<Pending>>`, `Pending` wraps a `tokio::sync::watch`).
  The first miss is the leader; later misses subscribe. The leader inserts into
  the cache before removing the in-flight entry, so late arrivals hit cache.

### Filtering

- Blocklist content is fetched and normalised by the management plane and
  delivered as blobs: zstd-compressed UTF-8, one lowercase ASCII (punycode)
  domain per line, no trailing dot, sorted, unique. Identified by SHA-256 hex
  of the compressed bytes.
- An entry blocks the domain and all subdomains. Allowlist beats blocklist.
- Matching walks label suffixes of the query name against an
  `FxHashSet<Box<[u8]>>` of lowercase wire names (borrowed lookup, no alloc).
- Answers whose CNAME chain reaches a blocked name are blocked.
- Block response: `null_ip` (A 0.0.0.0 / AAAA :: , other types NODATA),
  `nxdomain`, or `refused`; TTL `block_ttl`.

### ACL

Queries from addresses outside `acl_allow_cidrs` get REFUSED. The management
plane seeds 127.0.0.0/8, ::1/128, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16,
100.64.0.0/10, fc00::/7, fe80::/10.

### Snapshot application

- Validate (version > applied, addresses parse, CIDRs parse, cache
  `max_bytes >= 1048576`, upstream timeouts 50..=5000, DoH URL https, blob
  hashes 64 hex and present or fetchable and matching).
- Build the new `Runtime` (including filter sets) on the control runtime, then
  `ArcSwap::store`. In-flight queries keep the `Arc` they loaded.
- Persist `state_dir/snapshot.binpb` via write-to-temp + fsync + rename.
- Other engine local state: `state_dir/identity/`, `state_dir/blobs/`, and
  (M3) `state_dir/trust-anchors.json` (RFC 5011 trust-anchor state) and
  `state_dir/rpz/<zone id>.zone` (last good copy of each transfer RPZ zone).
- Ack `Applied{version}` or `Rejected{version, reason}`; a rejected snapshot
  leaves the previous runtime in place.

### Telemetry

- Prometheus on `metrics_listen` at `/metrics`. Names:
  `nexora_queries_total{transport,rcode}`, `nexora_query_duration_seconds`
  (histogram, buckets 50µs..2s), `nexora_cache_hits_total`,
  `nexora_cache_misses_total`, `nexora_cache_stale_served_total`,
  `nexora_cache_entries`, `nexora_cache_bytes`, `nexora_filter_blocked_total`,
  `nexora_upstream_up{upstream}`, `nexora_upstream_rtt_seconds{upstream}`,
  `nexora_upstream_queries_total{upstream}`,
  `nexora_upstream_failures_total{upstream}`,
  `nexora_upstream_mismatched_replies_total`,
  `nexora_export_dropped_total{signal}`, `nexora_config_version`,
  `nexora_control_connected`. M2 adds `transport` values `dot|doh|doq` to
  `nexora_queries_total` and:
  `nexora_tls_handshakes_total{transport,result="ok|failed|no_certificate"}`,
  `nexora_encrypted_connections{transport}`,
  `nexora_doh_requests_total{method="GET|POST|other",status}`,
  `nexora_doq_protocol_errors_total`,
  `nexora_proxy_protocol_rejected_total{transport="dot|doh",reason="untrusted_peer|invalid_header|timeout"}`,
  `nexora_tls_certificate_not_after_seconds`,
  `nexora_tls_material_updates_total{result="applied|rejected"}`,
  `nexora_filter_rewritten_total`.
- Query log: each worker pushes a fixed-size `QueryRecord` into a lock-free
  `crossbeam_queue::ArrayQueue` (capacity 65536); on full, the record is
  dropped and `nexora_export_dropped_total{signal="logs"}` increments.
- The telemetry thread batches records (max 1000 or 1 s) into OTLP LogRecords
  sent to `telemetry.otlp_endpoint` over gRPC. Attributes:
  `client.address`, `dns.question.name`, `dns.question.type`,
  `dns.response.code`, `nexora.cache` (`hit|miss|stale`), `nexora.filter`
  (`none|blocked|allowed|rewritten`), `nexora.policy.group` (policy group id,
  empty for global clients), `nexora.upstream`, `nexora.duration_us`,
  `nexora.transport`, `nexora.engine.id`. Resource `service.name=nexora-engine`.
- Traces: a query becomes a trace when `trace_sample_one_in` selects it, or
  its duration exceeds `trace_slow_threshold_us`, or its rcode is SERVFAIL.
  Spans (`dns.query` root; children `nexora.filter`, `nexora.cache`,
  `nexora.upstream`) are synthesised from stage timestamps recorded in the
  `QueryRecord`.
- OTLP metrics: the same counters pushed every 15 s.
- An unreachable exporter endpoint drops data; export never blocks workers.

## Contract (`proto/nexora/control/v1/control.proto`)

- `EngineControl.Enroll` — TLS (server auth only; the engine pins the CA
  fingerprint carried in the join token). Join token format:
  `nxj1.<secret base32>.<sha256 hex of CA cert DER>`.
- `EngineControl.Connect` — mTLS bidi stream. Engine sends `Hello` first; the
  server replies with the latest `ConfigSnapshot` when the engine's version is
  older; afterwards every new version is pushed. Engine sends `Applied` /
  `Rejected`, and `Stats` every 10 s.
- `EngineControl.GetBlob` — mTLS server stream of 1 MiB chunks.
- Engine identity: ECDSA P-256 key generated locally; CSR in `Enroll`; the
  certificate CN is the engine UUID. Stored under `state_dir/identity/`.
- Field numbering: fields a milestone adds to an existing message use its
  own range — M2 300-399, M3 100-199, M4 200-299, M5 500 and up; new messages
  number from 1. M2 fields added to M1 messages use numbers 300-399;
  `TlsMaterial` travels only on `Connect` and is held in engine memory.
- M2: `ConfigSnapshot.policy_groups` / `rewrite_sets` /
  `global_rewrite_set_ids` carry per-client policy (safe search is expanded by
  the management plane into rewrite sets); `Hello.tls_fingerprint_sha256`
  reports the installed DNS serving certificate; the server sends
  `TlsMaterial` when it differs and the engine answers `TlsMaterialResult`.
- M3: `ConfigSnapshot.resolution_mode` / `recursion` / `forward_zones` /
  `dnssec` / `rpz_zones` / `dnssec_validate_forwarded` (100-105),
  `Stats.recursion` / `dnssec` / `rpz_zones` (100-102),
  `ServerMessage.rpz_tsig_keys` (100). RPZ TSIG secrets travel only on
  `Connect` (`RpzTsigKeys`) and are held in engine memory; they are never part
  of `ConfigSnapshot`, `config_versions` or `state_dir`. RPZ file zones are
  blobs fetched with `GetBlob`. `dnssec_validate_forwarded` (management
  default `true`; requires `dnssec.validation`) makes the engine validate
  answers from the global upstreams too, fetching DS/DNSKEY through the
  forwarder up to the root trust anchor.
- M5: `EngineMessage.cert_request` (500), `ServerMessage.cert_issued` (500) and
  `ServerMessage.renew_certificate` (501) carry certificate renewal and
  rotation; M5 adds no `ConfigSnapshot` or `Stats` field (fleet health is
  derived from the M1 `Stats` samples).

## Management plane

- Stateless; any number of instances. Configuration via environment:
  `NEXORA_DATABASE_URL`, `NEXORA_HTTP_LISTEN` (`:8080`), `NEXORA_GRPC_LISTEN`
  (`:9443`), `NEXORA_CA_CERT_FILE`, `NEXORA_CA_KEY_FILE`,
  `NEXORA_GRPC_SERVER_NAMES` (comma-separated SANs for the gRPC server cert,
  issued from the CA at start), `NEXORA_PUBLIC_URL`, `NEXORA_SECURE_COOKIES`
  (`true`), `NEXORA_OIDC_ISSUER`, `NEXORA_OIDC_CLIENT_ID`,
  `NEXORA_OIDC_CLIENT_SECRET_FILE`, `NEXORA_OIDC_ADMIN_GROUP`,
  `NEXORA_OIDC_OPERATOR_GROUP`, `NEXORA_QUERYLOG_BACKEND` (`builtin` |
  `opensearch`), `NEXORA_QUERYLOG_BUILTIN_CAPACITY` (`200000`),
  `NEXORA_OPENSEARCH_URL`, `NEXORA_OPENSEARCH_INDEX`
  (`nexora-querylog-*`), `NEXORA_OPENSEARCH_USERNAME`,
  `NEXORA_OPENSEARCH_PASSWORD_FILE`, `NEXORA_OTLP_ENDPOINT`,
  `NEXORA_DNS_TLS_CERT_FILE`, `NEXORA_DNS_TLS_KEY_FILE`,
  `NEXORA_DNS_TLS_RELOAD_INTERVAL` (`30s`) (M2; the DNS serving certificate
  for DoT/DoH/DoQ, pushed to engines, never stored in PostgreSQL),
  `NEXORA_KEK_FILE` (M3: RPZ TSIG secrets sealed by internal/secrets; M4 adds
  DNSSEC and TSIG keys), `NEXORA_PKCS11_MODULE` / `_TOKEN_LABEL` /
  `_PIN_FILE` (M4).
- Secrets come from files, never from the database in plaintext.
- `nexora-mgmt serve | migrate | ca init --out <dir> | user create --admin`;
  M2 adds
  `ca issue-dns --ca-cert <file> --ca-key <file> --names <list> [--days 90] --out <dir>`.
- M2 management metrics: `nexora_mgmt_dns_tls_reload_errors_total`,
  `nexora_mgmt_dns_tls_not_after_seconds`.
- Every config mutation runs in one transaction: change rows -> write audit
  row -> build snapshot -> insert `config_versions` -> `pg_notify(
'nexora_config', version)`. Every instance LISTENs and pushes to the engines
  connected to it.
- Blocklist fetches take `pg_try_advisory_lock(hashtext('filter_list:'||id))`
  so only one instance fetches each list.
- M4 online DNSSEC signing (`internal/dnssec`): primary zones with signing
  enabled are signed inside the zone rebuild transaction (KSK/ZSK, algorithm
  13 default or 8, NSEC or NSEC3 with zero iterations and empty salt);
  RRSIGs are valid 14 days, renewed once less than 7 days remain, and cached
  in `zone_signatures` so an edit re-signs only the RRsets it touched. Private
  keys stay in `internal/secrets` (unsealed only for the signing call, or used
  inside the PKCS#11 token). Engines receive the signed data as NZF
  images/deltas. The refresh loop takes
  `pg_try_advisory_lock(hashtext('dnssec:'||zone_id))` so only one instance
  signs each zone.
- HTTP API: `/api/v1`, OpenAPI 3.1 at `mgmt/api/openapi.yaml`, JSON errors
  `{"code": "...", "message": "..."}`. Editable resources carry `revision`; a
  stale revision returns 409 `conflict`.
- Auth: session cookie `nexora_session` (HttpOnly, SameSite=Lax, Secure when
  `NEXORA_SECURE_COOKIES`), bearer API tokens `nxt_<base32>`, OIDC
  authorization-code + PKCE. Passwords argon2id (m=64MiB, t=3, p=2).
  Mutating requests must send `Content-Type: application/json` (CSRF
  defence with SameSite=Lax).
- Roles: `viewer` (read everything except users, tokens, audit), `operator`
  (+ DNS configuration mutations), `admin` (everything). Permissions keyed by
  OpenAPI operationId in `mgmt/internal/auth/permissions.go`.
- First run: when no users exist, the instance that inserts the single
  `setup_tokens` row logs `setup token: <token>`; `POST /api/v1/setup`
  consumes it.
- The builtin query-log backend is an OTLP `LogsService` on the gRPC port
  (mTLS, engine identities) with an in-memory ring per instance; it suits
  single-instance deployments. OpenSearch deployments send engine OTLP to an
  OpenTelemetry Collector whose `opensearch` exporter writes
  `nexora-querylog-YYYY.MM.DD`; the adapter queries `attributes.*` fields.

## GUI

Vite 8, React 19, react-router 7, TanStack Query 5, Tailwind 4, Radix-based
components ported from the first Nexora (`components/ui`), openapi-typescript

- openapi-fetch client generated from `mgmt/api/openapi.yaml`, Recharts.
  Routes: `/login`, `/setup`, `/` (dashboard), `/query-log`, `/upstreams`,
  `/access-control`, `/filtering`, `/policies` (M2), `/rewrites` (M2), `/zones`
  (M4), `/rpz` (M3), `/dnssec` (M3/M4), `/engines`, `/users`, `/api-tokens`,
  `/audit`, `/settings`. The build is embedded into `nexora-mgmt`.

## End-to-end harness

- `e2e/harness` starts real binaries as child processes (engine, mgmt,
  `nexora-fixture`, `postgres` via `initdb`/`pg_ctl` into a temp dir,
  `otelcol-contrib`) on random free ports inside the dev pod. External shared
  services for tests come from environment variables:
  `NEXORA_E2E_OPENSEARCH_URL`, `NEXORA_E2E_JAEGER_QUERY_URL`.
- Binaries come from `NEXORA_E2E_BIN_DIR` (default `target/release` and
  `bin/`), built by `make e2e-build`.
- Every test that asserts "does not happen" first asserts the positive path in
  the same run, so a harness failure cannot pass a negative check.

## Fleet (M5)

### Engine groups and scoping

- `engine_groups` holds the server-side fleet partition. The group `default`
  (`00000000-0000-0000-0000-000000000001`) always exists and cannot be renamed
  or deleted. Every engine belongs to one group (`engines.engine_group_id`); a
  join token names the group an enrolling engine lands in, and
  `PATCH /api/v1/engines/{id}` moves an engine. Engine groups are unrelated to
  M2 policy groups (client-side, selected by source CIDR).
- Scoped tables carry `engine_group_id uuid NULL` (NULL = every group):
  `upstreams`, `filter_lists`, `policy_groups`, `rewrites` (only global
  rewrites; a rewrite inside a policy group follows that policy group),
  `forward_zones`, `zones`, `rpz_zones`. Names stay unique across the fleet.
- A group's snapshot contains the global rows plus the group's rows. Upstreams
  follow `engine_groups.upstream_mode`: `inherit` = the group's upstreams
  first, then the global ones; `override` = only the group's upstreams.
  `access_control.allow_cidrs` is followed by `engine_groups.extra_acl_cidrs`;
  a non-empty `engine_groups.otlp_endpoint` replaces the global OTLP endpoint.
  Resolution, DNSSEC, resolver/cache, block mode, allowlist and global safe
  search settings are fleet-wide. A policy group may select only filter lists
  that are global or in its own engine group. RPZ TSIG keys and hosted-zone
  TSIG keys (`RpzTsigKeys`, `KeyMaterial`) are filtered per engine to the
  zones in its target snapshot.
- Host concerns stay in `engine.toml`. `NEXORA_ENGINE_NODE_NAME` overrides
  `node_name`. Per-engine state set through the API is the group and
  `engines.labels` (string -> string; key of 1-63 characters from `a-z`,
  `0-9`, `.`, `/`, `-` that starts and ends with `a-z` or `0-9`; value at most
  63 characters; at most 32 labels). `nexora.io/canary=true` makes an engine preferred for canary
  selection.

### Versions and snapshots

- `config_versions.version` stays one global sequence
  (`pg_advisory_xact_lock(hashtext('nexora:config_version'))`). Every publish
  writes one
  `group_snapshots(version, engine_group_id, snapshot, content_sha256)` row per
  engine group; `content_sha256` is the SHA-256 of the
  deterministic encoding with `version` and `created_unix_ms` zeroed.
  `config_versions.snapshot` is NULL from M5 on; `snapshot.Latest` returns the
  default group's newest group snapshot.
- `engine_groups.stable_version` is the newest version whose rollout completed
  for that group. Rollback and republish copy an existing group snapshot into
  a new version (re-encoded with the new number), because engines apply only a
  version greater than the one they run.

### Rollouts

- Each group snapshot gets one `rollouts` row. Kinds: `change` (a config
  mutation), `rollback`, `republish` (engine moved into the group). Group
  parameters: `rollout_strategy` (`all_at_once` | `canary`), `canary_count`,
  `canary_percent`, `ack_timeout_seconds` (60), `health_window_seconds` (30),
  `max_servfail_ratio` (0.05), `min_health_queries` (100), copied into
  `rollouts.params` at creation.
- A `change` whose `content_sha256` equals the content of the group's stable
  version, `rollback`, `republish` and test-only raw publishes are immediate:
  strategy `all_at_once`, not held by `rollouts_paused`. An `all_at_once`
  rollout is inserted in `rolling`; a `canary` change is inserted in `pending`.
- States: `pending` -> `canary` -> `verifying` -> `rolling` -> `completed`;
  `canary`/`verifying`/`rolling` -> `halted`; `halted` -> `rolled_back`; every
  non-terminal state -> `superseded`. At most one rollout per group is in
  `canary`/`verifying`/`rolling`.
- `pending` waits while the group has `rollouts_paused`, else selects
  canaries: connected engines, `nexora.io/canary=true` first, then by node
  name; size max(`canary_count`, ceil(`canary_percent`% of connected)), at
  least 1, at most connected-1 when two or more are connected.
- `canary`: a canary rejecting the version -> `halted`; all canaries applied
  -> `verifying`; `ack_timeout_seconds` elapsed -> `halted`. `verifying`:
  after `health_window_seconds`, each canary needs at least 2 `engine_stats`
  samples from the newest sample at most 60 s before the phase start onwards
  (else `halted`, "stopped reporting"); with at least `min_health_queries`
  queries a SERVFAIL/queries ratio above `max_servfail_ratio` -> `halted`;
  otherwise `rolling`. `rolling`: a rejection -> `halted`; every connected
  engine applied -> `completed` (sets `stable_version`); `ack_timeout_seconds`
  elapsed -> `halted`. Disconnected engines get the version on reconnect.
- Creation supersedes the group's open rollouts: a paused, non-immediate
  `change` supersedes only `pending`; a `rollback` marks `halted` rollouts
  `rolled_back`, supersedes the rest and sets `rollouts_paused`; anything else
  supersedes `pending`/`canary`/`verifying`/`rolling`/`halted`.
  `resume-rollouts` clears `rollouts_paused` and publishes a fresh version.
- Target version of an engine, given the group's newest non-superseded
  rollout R: R `rolling`/`completed`/`rolled_back` -> R.version; R
  `canary`/`verifying` and the engine is a canary -> R.version; R `halted` and
  the engine applied R.version -> R.version; otherwise `stable_version`. An
  engine whose applied version is above its target is flagged `version_ahead`
  and never pushed.
- Every instance runs `rollout.Controller`: tick `NEXORA_ROLLOUT_TICK` plus
  LISTEN `nexora_rollout`. Per open rollout: `BEGIN`,
  `pg_try_advisory_xact_lock(hashtext('nexora:rollout:' || id))` (skip when not
  acquired), the engine group row `FOR NO KEY UPDATE` (the order rollout
  creation locks them in), `SELECT ... FOR UPDATE`, `rollout.Step` with `now()` from
  PostgreSQL, `UPDATE`, `pg_notify('nexora_rollout', engine_group_id)`,
  `COMMIT`. The hub LISTENs on `nexora_rollout` and pushes to its connected
  engines of that group whose target is above the version last sent; acks and
  rejections notify `nexora_rollout` so controllers step at once.

### Fleet health

- Engine `status`: `revoked` (engine revoked), `ahead` (`version_ahead` or
  applied above target), `disconnected` (no live stream: `connected_instance`
  NULL or its instance heartbeat older than 15 s), `rejected`
  (`rejected_version` above applied), `current` (applied equals target),
  `behind`.
- Metrics on every instance, read from PostgreSQL at scrape time:
  `nexora_mgmt_engines{engine_group,status}`,
  `nexora_mgmt_engines_disconnected` (non-revoked, non-deleted engines without
  a live stream whose `last_seen_at`, or `enrolled_at` when never seen, is
  older than 60 s), `nexora_mgmt_rollouts{engine_group,state}` (non-terminal
  and halted).

### Engine lifecycle

- Join tokens: `name`, `engine_group_id`, `labels`, `expires_at` (TTL 60 s ..
  1 year), `max_uses` (NULL = unlimited), `uses`, `revoked_at`. Enroll errors
  (`PermissionDenied`): `join token unknown`, `join token expired`,
  `join token exhausted`, `join token revoked`.
- `engine_certificates` columns: `serial`, `engine_id`, `not_before`,
  `not_after`, `issued_at`, `revoked_at`, `revoke_reason`; serial lowercase hex. Lifetime
  `NEXORA_ENGINE_CERT_TTL`. `engines.certificate_serial` holds the newest
  issued serial.
- Every `Connect`, `GetBlob` and builtin `LogsService/Export` call looks up
  the caller's certificate serial: unknown engine or deleted engine ->
  `PermissionDenied` `unknown or deleted engine`; revoked engine, revoked or
  unknown serial, or a serial of another engine -> `PermissionDenied`
  `certificate revoked`. A `Connect` with a serial marks the engine's older
  unrevoked serials `superseded`.
- Renewal: from 2/3 of the lifetime the engine sends
  `CertificateRequest{csr_der, reason: RENEWAL}` (CSR CN = engine id, new
  P-256 key); the instance issues (at most once per engine per 10 s) and
  answers `CertificateIssued{cert_der, ca_der}`; the engine swaps
  `state_dir/identity` atomically (`identity.new` -> `identity`) and
  reconnects.
- Rotation: `POST /api/v1/engines/{id}/rotate-certificate` sets
  `cert_rotate_requested_at` and notifies `nexora_engine_rotate`; the instance
  holding the stream (and every `Connect` while the request is outstanding)
  sends `RenewCertificate{reason: ROTATE}`; issuing clears it.
- Revocation: `POST /api/v1/engines/{id}/revoke` sets `engines.revoked_at`,
  revokes every certificate (`revoked`) and notifies `nexora_engine_revoked`;
  instances end that engine's streams with `PermissionDenied`
  `certificate revoked`. A revoked engine keeps serving its last snapshot,
  sets `nexora_control_revoked 1` and retries every 300 s (±10%); joining
  again needs `state_dir/identity` removed and a new join token.
  `DELETE /api/v1/engines/{id}` revokes and sets `deleted_at`.

### Distribution

- `.github/workflows/images.yml` builds `nexora-engine` and `nexora-mgmt`
  natively on `arc-azrtydxb-publish` (arm64) and `arc-azrtydxb-amd64-publish`
  (amd64), pushes by digest to `192.168.10.131:5000/azrtydxb`, and merges
  digests into `:sha-<7>` (`:v*` on tags) plus `:main` on main.
- `deploy/compose/` runs PostgreSQL, one mgmt, one engine (profile `engine`)
  and an optional OpenTelemetry Collector (profile `otel`).
- `deploy/helm/nexora`: mgmt Deployment (`migrate` init container), one
  engine workload per engine group (`DaemonSet` or `Deployment`, node
  selector, hostPath state), per-group DNS Service, CNPG `Cluster` or an
  external database secret, optional collector, ServiceMonitor,
  PrometheusRule, `values.schema.json`. Static checks live in
  `deploy/deploytest`.
- CLI: `nexora-mgmt engine-group create`, `nexora-mgmt join-token create`,
  `nexora-mgmt ca init --if-missing`.

## Deployment on kw

Namespace `nexora`. `scripts/kw-deploy.sh` applies `deploy/kw/namespace.yaml`,
`opensearch.yaml`, `cnpg-cluster.yaml` (CNPG `nexora-db`, 2 instances),
`otelcol.yaml` (traces to `jaeger.observability.svc:4317`, query logs to
OpenSearch) and `blocklist.yaml`, then installs `deploy/helm/nexora` with
`deploy/kw/values-kw.yaml`: `nexora-mgmt` Deployment (2 replicas) behind ingress
`nexora.kw.local` (class `nginx`, ClusterIssuer `cluster-ca`, HTTPS only) and a
gRPC LoadBalancer `192.168.10.135:9443`; engine DaemonSets per engine group,
selected by node label `nexora.io/engine-group`: `nexora-engine` (group
`default`, every node without the label, DNS/DoT/DoH/DoQ LoadBalancer
`nexora-dns` `192.168.10.136`, `externalTrafficPolicy: Local`) and
`nexora-engine-edge-b` (group `edge-b`, nodes labelled `edge-b`: `worker-24`,
`worker-25`; LoadBalancer `nexora-dns-edge-b` `192.168.10.137`,
`externalTrafficPolicy: Cluster`); engine state on hostPath
`/var/lib/nexora/<workload>`; ServiceMonitor and PrometheusRule in `monitoring`
with label `release: kps`. `deploy/kw/bootstrap.sh` configures the API (admin,
upstreams, block list, RPZ, engine group `edge-b`, join token secrets
`nexora-join-token` and `nexora-join-token-edge-b`). Acceptance:
`scripts/kw-acceptance.sh` runs `TestKwSmoke` (which includes `TestKwSmokeM4`)
and `TestKwFullProduct`.
