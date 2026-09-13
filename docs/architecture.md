# Nexora architecture

The spec is `.procoder/specs/nexora-v1.md` (what and why). This document is the
settled *how*: every plan and every implementation task inherits it. Change it
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
  src/server/{mod,udp,tcp}.rs           per-core listeners (M2 adds dot, doh, doq)
  src/control.rs                        management-plane client (enroll, stream, blobs)
  src/snapshot.rs                       snapshot validation + persistence
  src/telemetry/{metrics,querylog,otlp}.rs
  src/recursor/…                        (M3) iterative resolver, DNSSEC validation, RPZ
  src/authoritative/…                   (M4) zone serving, transfers, updates, signing
  fuzz/                                 cargo-fuzz targets
  build.rs                              tonic-prost codegen from proto/
gen/go/nexora/control/v1/               generated Go protobuf/gRPC (committed)
mgmt/                                   Go management plane (module root is repo root)
  cmd/nexora-mgmt/main.go
  api/openapi.yaml                      HTTP API source of truth
  migrations/*.sql                      goose migrations (embedded)
  internal/config                       env configuration
  internal/store                        pgx pool, migrations, queries
  internal/pki                          CA, engine/server certificates
  internal/control                      gRPC EngineControl server, engine hub
  internal/snapshot                     snapshot builder + publish/notify
  internal/auth                         users, sessions, tokens, RBAC, OIDC, audit
  internal/api                          oapi-codegen strict server + handlers
  internal/blocklist                    list fetcher/parser
  internal/querylog                     query-log backends (builtin OTLP receiver, OpenSearch)
  internal/stats                        engine stats samples
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
  `nexora_control_connected`.
- Query log: each worker pushes a fixed-size `QueryRecord` into a lock-free
  `crossbeam_queue::ArrayQueue` (capacity 65536); on full, the record is
  dropped and `nexora_export_dropped_total{signal="logs"}` increments.
- The telemetry thread batches records (max 1000 or 1 s) into OTLP LogRecords
  sent to `telemetry.otlp_endpoint` over gRPC. Attributes:
  `client.address`, `dns.question.name`, `dns.question.type`,
  `dns.response.code`, `nexora.cache` (`hit|miss|stale`), `nexora.filter`
  (`none|blocked|allowed`), `nexora.upstream`, `nexora.duration_us`,
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
  `NEXORA_KEK_FILE` (M4), `NEXORA_PKCS11_MODULE` / `_TOKEN_LABEL` /
  `_PIN_FILE` (M4).
- Secrets come from files, never from the database in plaintext.
- `nexora-mgmt serve | migrate | ca init --out <dir> | user create --admin`.
- Every config mutation runs in one transaction: change rows -> write audit
  row -> build snapshot -> insert `config_versions` -> `pg_notify(
  'nexora_config', version)`. Every instance LISTENs and pushes to the engines
  connected to it.
- Blocklist fetches take `pg_try_advisory_lock(hashtext('filter_list:'||id))`
  so only one instance fetches each list.
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
+ openapi-fetch client generated from `mgmt/api/openapi.yaml`, Recharts.
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

## Deployment on kw

Namespace `nexora`: CNPG cluster `nexora-db`, `nexora-mgmt` Deployment (2
replicas) behind ingress `nexora.kw.local` via ingress-nginx and a
LoadBalancer for gRPC 9443, `nexora-engine` Deployment (3 replicas, one per
node) with LoadBalancer services for DNS, OpenTelemetry Collector sending
traces to Jaeger in `observability`, OpenSearch single node for the query log.
