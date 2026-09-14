# nexora-m10-querylog-backends

Status: complete

## Problem

Nexora has two query-log backends. The builtin ring is per instance, is lost on restart and suits
single-instance installs only. OpenSearch needs a 2–3 GiB JVM per node and is the heaviest part of a
small install. Operators asked for two more options (issues #35 and #36 in azrtydxb/nexora):

- **ClickHouse** gives cheap storage and fast exact aggregates at ISP scale.
- **Loki** is light and Grafana-native, and many homelabs already run it. kw runs Loki 3.6.7 in
  `monitoring`.

The roadmap puts this in M10, after M6 has extended the backend contract. That contract now covers
partial name match, multi-value filters, decision reason fields, cursor paging and dashboard Top-N
lists. A new backend is only useful if it behaves exactly like the existing ones: the GUI, the
dashboard and the kw acceptance tests must not care which backend answers. Today no test states that
contract as one suite. The builtin and OpenSearch backends are each checked by their own hand-written
cases.

## Users

- **Homelab operator with Grafana/Loki already running:** points the collector at Loki, sets
  `NEXORA_QUERYLOG_BACKEND=loki`, and gets the full query log, filters and dashboard top lists with no
  new stateful service.
- **ISP / enterprise operator:** runs ClickHouse for millions of records per day, with a fixed schema
  and TTL, and exact top lists over 7 days within the dashboard timeout.
- **Nexora maintainers:** need one conformance suite that every backend must pass, so a fifth
  backend, or a change to one of the four, cannot drift.
- **Owner of kw:** keeps the console on OpenSearch. ClickHouse and Loki are fed by the same collector
  and proven against live engine traffic, with no DNS loss during the deploy.

## In scope

- [S-1] **ClickHouse ingestion.** The OpenTelemetry Collector's `clickhouse` exporter (contrib
  0.160.0, native protocol) writes engine query-log records into the table querylog in database nexora, with
  `create_schema: false`.
  - Nexora owns the table DDL in `deploy/clickhouse/querylog.sql`. It uses the exporter's v0.160.0
    logs column set, so the exporter's INSERT column list matches.
  - It adds these columns:
    - a `RowID UUID DEFAULT generateUUIDv4()` tiebreaker;
    - `MATERIALIZED` typed columns for every attribute the adapter reads;
    - a `ngrambf_v1` skip index on `lower(Name)`;
    - a `bloom_filter` index on `Client`;
    - `TTL toDateTime(Timestamp) + INTERVAL 7 DAY` with `ttl_only_drop_parts = 1`.
  - Design choice: collector exporter instead of direct insert by the management plane.
    - The topology matches OpenSearch: engines already send OTLP to one collector, and that collector
      also carries traces.
    - The collector owns batching, the retry queue and back-pressure.
    - The management plane stays read-only and stateless: it holds no write credentials and is not on
      the ingest path.
    - Multi-instance management planes cannot double-insert.
  - The cost is coupling to the exporter's column set. The pinned collector version and the e2e
    conformance run through a real collector prove the coupling holds.
- [S-2] **ClickHouse search (`querylog.ClickHouse.Search`).** It goes over the ClickHouse HTTP
  interface with plain `net/http`, and every user value is a typed query parameter (`{name:Type}` plus
  `param_name`). No value is ever interpolated into SQL.
  - **Name:** `lower(Name) LIKE {pattern:String}`, where the pattern is `%` + the lowercased fragment
    without one trailing dot + `%`, with `\`, `%` and `_` escaped.
  - **Multi-value filters:** `IN {values:Array(String)}`.
  - **Policy group:** `global` matches `''`.
  - **Filter:** the `Filter` materialized column coalesces the nexora.filter and nexora.filter.result attributes.
  - **Order:** `Timestamp DESC, RowID ASC`.
  - **Cursor:** base64url JSON `[timestamp_ns, "rowid"]`, applied as a keyset
    `(Timestamp < t) OR (Timestamp = t AND RowID > id)`.
  - `Limit` and its default and maximum are the shared querylog constants (100 and 1000).
  - Time precision is kept to the nanosecond.
- [S-3] **ClickHouse Top (method Top of the ClickHouse adapter, implementing the post-M6 Topper interface).**
  - The query: `SELECT key, count() AS c ... WHERE <time and filter clauses> AND key != '' GROUP BY key ORDER BY c DESC, key ASC LIMIT {limit:UInt32}`.
  - `key` is `Name`, `Client` or `Category`.
  - The counts are exact.
- [S-4] **Loki ingestion.** The collector's `otlphttp/loki` exporter sends to Loki's native OTLP
  endpoint (`<loki>/otlp`).
  - The Loki pipeline runs `transform/loki`, which sets the log body to
    `Concat([client.address, dns.question.name, dns.question.type, nexora.transport, nexora.engine.id], " ")`.
    Design choice: Loki drops entries of one stream with equal timestamp and equal line, and the engine
    body is only the query name. This keeps two clients asking the same name in the same microsecond.
  - Log attributes arrive as Loki structured metadata with dots replaced by `_`, for example
    `dns_question_name`.
  - `service.name=nexora-engine` becomes the only index label, `service_name`. No Loki configuration
    change is needed: kw's Loki runs with `auth_enabled: false` and `allow_structured_metadata: true`.
- [S-5] **Loki search (`querylog.Loki.Search`).** It uses plain `net/http` to
  `/loki/api/v1/query_range` with `direction=backward`, and sends
  `X-Loki-Response-Encoding-Flags: categorize-labels` so each entry carries its structured metadata.
  - **Query:** `NEXORA_LOKI_SELECTOR` followed by label-filter stages.
    - The name uses `dns_question_name=~"(?i)^.*<QuoteMeta(fragment without trailing dot)>.*$"`.
    - Multi-value filters use `label=~"^(?:<QuoteMeta(v1)>|<QuoteMeta(v2)>)$"`.
    - `global` is the empty alternative.
    - The filter matches `nexora_filter` or `nexora_filter_result`.
    - Every string literal is written with Go strconv.Quote escaping.
  - **Time range:** `start = From` (or `To − NEXORA_LOKI_LOOKBACK` when From is zero) and
    `end = To + 1ns`.
  - **Order:** timestamp descending, then a tiebreak key ascending. The key is the lowercase hex
    SHA-256 of the line and the sorted structured metadata.
  - **Cursor:** base64url JSON `["<timestamp_ns>", "<key>"]`. The next page requests
    `end = cursor_ns + 1ns` and discards entries newer than the cursor, or at the cursor with a key
    ≤ the cursor key. This is correct whether Loki treats `end` as inclusive or exclusive.
  - **Page completeness:**
    - A response holding exactly the requested entries may cut the group at its oldest nanosecond, so
      that group is not used.
    - The request size grows from `limit + 1` by doubling, up to Loki's 5,000
      `max_entries_limit_per_query`, until a complete page plus one is known.
- [S-6] **Loki Top (method Top of the Loki adapter, implementing the post-M6 Topper interface).** It uses instant queries at
  `time = To` over `[<To − From + 1ns in ns>ns]`, so the window is [From, To] as in the builtin. It
  runs in two steps, which give exact ties ordered by key ascending:
  1. `topk(k, sum by (<label>) (count_over_time(<selector> <filters> | <label>!="" [<range>])))` gives
     the k-th count `c`.
  2. `sum by (<label>) (count_over_time(...)) >= c` gives every candidate. The adapter sorts by count
     descending, then key ascending, and cuts at k.
  - **Series-limit fallback:** when Loki answers either step with its series-limit error (HTTP 400
    containing `maximum of series`), both steps are rerun per partition of the key.
    - There are 37 partitions: the last alphanumeric character of the key (0–9, a–z, case-insensitive,
      ignoring a trailing dot), plus "none".
    - At most 6 partition queries run concurrently.
    - The global threshold is the k-th largest count across the partitions' step-1 results.
    - The results are merged the same way as step 2.
  - A partition that still hits the limit gives `ErrBackendUnavailable` with the message
    `loki series limit: raise max_query_series`.
  - Design choice: this keeps the stock Loki limits (`max_query_series` 500) in the shared `monitoring`
    Loki instead of changing its tenant limits. Exact counts are kept wherever Loki can compute them.

- [S-7] **Conformance suite (`mgmt/internal/querylog/querylogtest`).**
  - `Dataset(run string, base time.Time)` builds one canonical OTLP request, grouped per engine id.
  - `Run(t, Harness)` checks a backend against a `querylog.Builtin` fed the same dataset. The builtin
    is the reference.
  - Subtests: `partial-name`, `multi-value`, `reason-fields`, `policy-group-global`,
    `filter-generations`, `time-range-inclusive`, `paging-no-gaps-no-duplicates` (limits 1, 2, 7 and
    1000), `invalid-cursor`, `top-name`, `top-blocked`, `top-client`, `top-category-skips-empty`,
    `top-ties-key-ascending` and `top-time-range-inclusive`.
  - It runs for builtin (unit test) and for OpenSearch, ClickHouse and Loki (e2e), which receive the
    dataset through a real `otelcol-contrib`.
- [S-8] **Configuration and wiring.**
  - `NEXORA_QUERYLOG_BACKEND` accepts `clickhouse` and `loki`.
  - New variables: `NEXORA_CLICKHOUSE_URL` (required), `NEXORA_CLICKHOUSE_DATABASE` (`nexora`),
    `NEXORA_CLICKHOUSE_TABLE` (`querylog`), `NEXORA_CLICKHOUSE_USERNAME` (`default`),
    `NEXORA_CLICKHOUSE_PASSWORD_FILE`, `NEXORA_LOKI_URL` (required),
    `NEXORA_LOKI_SELECTOR` (`{service_name="nexora-engine"}`), `NEXORA_LOKI_TENANT` (`X-Scope-OrgID`,
    empty sends none), `NEXORA_LOKI_USERNAME`, `NEXORA_LOKI_PASSWORD_FILE` and `NEXORA_LOKI_LOOKBACK`
    (`168h`).
  - `nexora-mgmt serve` builds the matching adapter. Snapshots send engines to the OTLP endpoint (not
    the management plane) for every backend except `builtin`.
  - `searchQueryLog` reports `backend` as `clickhouse` or `loki`, and `getDashboardTop` reports
    `available: true`.
- [S-9] **Helm chart.**
  - `mgmt.querylog.backend` enum gains `clickhouse | loki`.
  - `mgmt.querylog.clickhouse.{url, database, table, username, passwordSecret: {name, key}}`.
  - `mgmt.querylog.loki.{url, selector, tenant, username, passwordSecret: {name, key}, lookback}`.
  - The chart has env entries and a read-only Secret volume at `/etc/nexora/querylog/password`.
    `required` messages name the missing URL.
  - `values.schema.json` is updated. If M9 shipped an installation CRD that enumerates query-log
    backends, it gains the same two backends and fields.
- [S-10] **kw deployment.**
  - `deploy/kw/clickhouse.yaml` holds the StatefulSet/Service `clickhouse` in namespace `nexora`:
    - image `192.168.10.131/clickhouse/clickhouse-server:26.8.4.11` (the 26.8 LTS);
    - a 20 Gi `longhorn-single` volume;
    - users `nexora_writer` (INSERT on nexora.querylog) and `nexora_reader` (SELECT), with passwords
      from Secret `nexora-clickhouse` via `from_env`;
    - `default` limited to localhost.
  - `scripts/kw-deploy.sh` creates that Secret when missing, applies the manifest and applies
    `deploy/clickhouse/querylog.sql` through `clickhouse client` in `clickhouse-0`. The SQL is
    idempotent.
  - `deploy/kw/otelcol.yaml` fans logs out to three pipelines: `logs` (OpenSearch, unchanged),
    `logs/clickhouse` and `logs/loki` (`http://loki.monitoring.svc:3100/otlp`).
  - The kw management plane stays on `opensearch`.
  - `TestKwQueryLogBackends` proves ClickHouse and Loki against live traffic.
- [S-11] **Local e2e.**
  - The dev toolbox image pins `clickhouse` 26.8.4.11 (static arm64 binary) and `loki` 3.6.7 (the kw
    version), and `scripts/dev-selftest.sh` checks both.
  - The harness gains `StartClickHouse` and `StartLoki`. Both run on free loopback ports in a temp dir.
    `StartLoki` has an optional `MaxQuerySeries`.
  - `OtelcolConfig` gains ClickHouse, Loki and `OpenSearchIndex` fields. `MgmtOptions` gains the
    ClickHouse and Loki environment.
  - `TestQueryLogBackends` and `TestQueryLogCategoryAttribution` run for `clickhouse` and `loki` next
    to `builtin` and `opensearch`.
- [S-12] **Documentation.**
  - `docs/operations.md` "Query logs, traces and OTLP" describes both backends: every variable,
    collector exporter snippets, the schema file, Loki requirements, retention and the limits.
  - `docs/architecture.md` lists the variables, the design and the kw topology.
  - `deploy/kw/README.md` lists the new ClickHouse manifest.
  - `web/src/help/topics/observability.md` names all four backends.

## Out of scope

- Switching the kw management plane off OpenSearch, or removing OpenSearch from kw.
- Changing the shared Loki in `monitoring` (limits, runtime config, retention), or deploying a second
  Loki on kw.
- ClickHouse clusters, replication (`ReplicatedMergeTree`), sharding, TLS to ClickHouse on kw, and
  schema migrations beyond the idempotent `CREATE ... IF NOT EXISTS` file. A later schema change ships
  as a new SQL file with its own upgrade note.
- A ClickHouse or Loki service in the Helm chart or Docker Compose. The chart only configures the
  management plane to read an existing service, and the operations guide shows the collector config.
- Engine changes: engines keep emitting the same OTLP records.
- New API operations, new GUI pages, or new query-log fields beyond the post-M6 contract.
- Loki approximate aggregations (`approx_topk`), which need Loki configuration changes.
- Query-log retention settings in the API or GUI. Retention is the backend's own (ClickHouse TTL
  7 days, Loki 168 h on kw).
- DHCP, AI and the other roadmap milestones.

## Constraints

- **Contract:** the backend contract is the committed post-M6 and post-M7 `mgmt/internal/querylog`
  package. The contract includes:
  - `Backend` (`Name`, `Search`), `Topper` (`Top`), `Query` with slice filters, `Record` with the
    reason fields, `TopQuery` and `TopEntry`;
  - `ErrBackendUnavailable` and `ErrInvalidCursor`;
  - the M7 OpenSearch cursor `[timestamp, _id]`.

  M10 adds no field to them. Where a committed name differs from this spec, the committed code wins and
  the conformance suite follows it.

- **Identical semantics** means that for every conformance query, the concatenated pages of a backend
  equal the builtin reference, as a sequence of millisecond-truncated timestamps and as a multiset of
  records.
  - Order within one millisecond is backend-defined: OpenSearch stores milliseconds, and each backend
    has its own tiebreak.
  - The builtin reference is fed in ascending time order, so its newest-first order is time order.
  - Top lists are identical including the key-ascending order of ties.
- **Timeouts:**
  - Search 5 s on every backend, like OpenSearch in M6.
  - ClickHouse Top 5 s.
  - Loki Top 15 s in total (two steps, and up to 74 partition queries), so the dashboard's four top
    lists fit its page load.
- **Errors:** transport errors, HTTP 5xx and HTTP 429 become `ErrBackendUnavailable`, which the API
  maps to 503 `querylog_unavailable` and dashboard top lists to `available: false`.
  - Other 4xx answers are plain errors with the backend's message.
  - One exception: a Loki series-limit 400 triggers the partition fallback in Top.
- **No new Go module dependency:** both adapters use `net/http` and `encoding/json`. Design choice:
  `clickhouse-go` would add a large dependency tree for two read-only queries, and the HTTP interface
  supports typed parameters.
- **Secrets** come only from files and Kubernetes Secrets (`nexora-clickhouse` in `nexora`), never from
  the repository or the management database.
  - The management plane uses the read-only `nexora_reader`.
  - The collector uses the insert-only `nexora_writer`.
- **kw deploys:**
  - `scripts/kw-deploy.sh` with zero lost probe queries on 192.168.10.136 and 192.168.10.139, then
    `scripts/kw-acceptance.sh`; roll back with `helm rollback nexora` on any loss or failure.
  - 192.168.10.136 and .139 never change.
  - Nothing in `monitoring` changes.
- **Images and binaries:** images are pulled through Nexus `192.168.10.131` and built with kw
  BuildKit. Every binary and image version is pinned: ClickHouse 26.8.4.11, Loki 3.6.7,
  otelcol-contrib 0.160.0.
- **Migrations:** the range 01100–01109 is reserved for M10, and M10 needs none, because it adds no
  PostgreSQL state.
- **Existing tests:** every existing Go, Rust and Playwright test keeps passing, including
  `TestGUICoverage`. Tests only gain backends; none is weakened.
- **Implementation rules:**
  - Builds and tests run in the kw dev pod (`scripts/dev-exec.sh`).
  - Implementers never commit.
  - TDD: red, then green.
  - Every negative assertion first asserts the positive path.
  - Ceilings carry `debt:` markers.

## Interfaces

- **Go (`package querylog`):**
  - `type ClickHouse struct`, `func NewClickHouse(cfg config.ClickHouseConfig) (*ClickHouse, error)`,
    `Name() == "clickhouse"`, `Search`, `Top`.
  - `type Loki struct`, `func NewLoki(cfg config.LokiConfig) (*Loki, error)`, `Name() == "loki"`,
    `Search`, `Top`.
  - Compile-time assertions `var _ Topper = (*ClickHouse)(nil)` and `var _ Topper = (*Loki)(nil)`.
- **Go (`package querylogtest`):**
  - `type Harness struct { Backend querylog.Backend; Ingest func(t *testing.T, engineID string, req *collogspb.ExportLogsServiceRequest); Visible func(t *testing.T, want int) }`.
  - `func Dataset(run string, base time.Time) []EngineBatch` (`EngineBatch{EngineID string; Req *collogspb.ExportLogsServiceRequest}`).
  - `func Window(base time.Time) (from, to time.Time)`.
  - `func Run(t testing.TB, run string, base time.Time, h Harness)`, `func OTLPExport(t testing.TB, grpcAddr string, batches []EngineBatch)` and `func PollVisible(b querylog.Backend, base time.Time) func(t *testing.T, want int)`.
- **Go (`package config`):**
  - `ClickHouseConfig{URL, Database, Table, Username, PasswordFile string}`.
  - `LokiConfig{URL, Selector, Tenant, Username, PasswordFile string; Lookback time.Duration}`.
  - new fields ClickHouse and Loki on `config.Config`.
- **Management environment:** `NEXORA_QUERYLOG_BACKEND` (`builtin | opensearch | clickhouse | loki`)
  and the [S-8] variables. The load errors are:
  - `NEXORA_CLICKHOUSE_URL is required when NEXORA_QUERYLOG_BACKEND=clickhouse`;
  - `NEXORA_LOKI_URL is required when NEXORA_QUERYLOG_BACKEND=loki`;
  - `NEXORA_LOKI_SELECTOR must be a LogQL stream selector in braces`;
  - `NEXORA_LOKI_LOOKBACK must be a duration between 1h and 721h`.
- **HTTP API:** no new operation or field. The query-log page field backend gains the values `clickhouse` and
  `loki` in its description.
- **Files:**
  - `deploy/clickhouse/querylog.sql` (idempotent DDL: `CREATE DATABASE IF NOT EXISTS nexora`,
    `CREATE TABLE IF NOT EXISTS nexora.querylog`);
  - `deploy/kw/clickhouse.yaml`;
  - collector pipelines `logs/clickhouse` and `logs/loki` in `deploy/kw/otelcol.yaml`.
- **Harness (`e2e/harness`):**
  - `StartClickHouse() *ClickHouse{HTTPURL, NativeAddr, Database, Table, WriterUser, WriterPassword, ReaderUser, ReaderPasswordFile}`.
  - `StartLoki(LokiOptions{MaxQuerySeries int}) *Loki{URL}`.
  - `OtelcolConfig` gains `OpenSearchIndex`, `ClickHouseNative`, `ClickHouseUser`,
    `ClickHousePassword` and `LokiURL`.
  - `MgmtOptions` gains `ClickHouseURL`, `ClickHousePasswordFile` and `LokiURL`.
- **Helm values:** the [S-9] keys.
- **Toolbox image:** `deploy/dev/Dockerfile` ARGs `CLICKHOUSE_VERSION=26.8.4.11` and
  `LOKI_VERSION=3.6.7`, and the next unused `toolbox-N` tag.
- **kw acceptance environment:** `NEXORA_KW_OPENSEARCH_URL`, `NEXORA_KW_CLICKHOUSE_URL`,
  `NEXORA_KW_CLICKHOUSE_PASSWORD_FILE` and `NEXORA_KW_LOKI_URL`, passed by `scripts/kw-acceptance.sh`.

## Data

- **ClickHouse `nexora.querylog`** (owned by Nexora's DDL, written by the collector, read by the
  management plane):
  - **Exporter v0.160.0 columns:** `Timestamp DateTime64(9)`, `TraceId`, `SpanId`, `TraceFlags`,
    `SeverityText`, `SeverityNumber`, `ServiceName`, `Body`, `ResourceSchemaUrl`,
    `ResourceAttributes Map(LowCardinality(String), String)`, `ScopeSchemaUrl`, `ScopeName`,
    `ScopeVersion`, `ScopeAttributes`, `LogAttributes Map(LowCardinality(String), String)` and
    `EventName`.
  - **Tiebreaker:** `RowID UUID DEFAULT generateUUIDv4()`.
  - **Materialized columns:**

    | Column           | Type                     | Source attribute                                          |
    | ---------------- | ------------------------ | --------------------------------------------------------- |
    | `Name`           | `String`                 | `dns.question.name`                                       |
    | `Client`         | `String`                 | `client.address`                                          |
    | `QType`          | `LowCardinality(String)` | `dns.question.type`                                       |
    | `RCode`          | `LowCardinality(String)` | `dns.response.code`                                       |
    | `Cache`          | `LowCardinality(String)` | `nexora.cache`                                            |
    | `Filter`         | `LowCardinality(String)` | `nexora.filter`, else `nexora.filter.result`              |
    | `ListID`         | `String`                 | `nexora.filter.list_id`                                   |
    | `Category`       | `LowCardinality(String)` | `nexora.filter.category`                                  |
    | `Source`         | `LowCardinality(String)` | `nexora.filter.source`                                    |
    | `Rule`           | `String`                 | `nexora.filter.rule`                                      |
    | `PolicyGroupID`  | `String`                 | `nexora.policy.group`                                     |
    | `RPZZoneID`      | `String`                 | `nexora.rpz_zone`                                         |
    | `RPZAction`      | `LowCardinality(String)` | `nexora.rpz`, with `none` stored as empty                 |
    | `ACLRefused`     | `LowCardinality(String)` | `nexora.acl.refused`                                      |
    | `UpstreamsRaced` | `Int64`                  | `nexora.upstream_raced`, through `toInt64OrZero`          |
    | `Upstream`       | `String`                 | `nexora.upstream`                                         |
    | `Transport`      | `LowCardinality(String)` | `nexora.transport`                                        |
    | `EngineID`       | `String`                 | `nexora.engine.id` log attribute, else resource attribute |
    | `DurationUS`     | `Int64`                  | `nexora.duration_us`, through `toInt64OrZero`             |

  - **Indexes:** `idx_name lower(Name) TYPE ngrambf_v1(3, 65536, 3, 0) GRANULARITY 1` and
    `idx_client Client TYPE bloom_filter(0.01) GRANULARITY 1`.
  - **Layout:** `ENGINE = MergeTree`, `PARTITION BY toDate(Timestamp)`,
    `ORDER BY (toStartOfFiveMinutes(Timestamp), ServiceName, Timestamp)`,
    `TTL toDateTime(Timestamp) + INTERVAL 7 DAY` and
    `SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1`.
  - Rows are never updated. The TTL drops whole daily parts.
- **Loki (shared `monitoring` Loki, written by the collector, read by the management plane):**
  - One stream `{service_name="nexora-engine"}` per tenant.
  - The line is `client name type transport engine_id`.
  - Every OTLP log and resource attribute is structured metadata under its `_` name:
    `client_address`, `dns_question_name`, `dns_question_type`, `dns_response_code`, `nexora_cache`,
    `nexora_filter`, `nexora_filter_result`, `nexora_filter_list_id`, `nexora_filter_category`,
    `nexora_filter_source`, `nexora_filter_rule`, `nexora_policy_group`, `nexora_rpz_zone`,
    `nexora_rpz`, `nexora_acl_refused`, `nexora_upstream`, `nexora_upstream_raced`,
    `nexora_transport`, `nexora_engine_id` and `nexora_duration_us`.
  - Retention is Loki's own: 168 h on kw.
- **Kubernetes:**
  - Secret `nexora-clickhouse` in `nexora`, with keys `writer-password` and `reader-password`
    (32 random bytes, base64), created once by `scripts/kw-deploy.sh`.
  - PVC `data-clickhouse-0`, 20 Gi `longhorn-single`.
- **Conformance dataset** (`querylogtest.Dataset`). Every name ends in `-<run>.test.`, the window is
  `[base, base + 60 s]`, and records are fed in ascending time:
  - partial-name records `www.you-<run>.tube.test.`, `you-<run>.test.`, `x*y-<run>.test.`,
    `x%y_<run>.test.` and `X?Y-<run>.TEST.`;
  - one record per decision source: blocklist with list id and rule, category with `gambling`,
    allowlist in policy group `g1`, rpz with zone id and action `nxdomain`, rewrite with rule, and acl
    with `authoritative`;
  - one record carrying the OpenSearch-renamed `nexora.filter.result` instead of `nexora.filter`;
  - one raced record (`nexora.upstream_raced=3`, `nexora.duration_us=1234`, transport `tcp`);
  - three `tie-<run>` records in one microsecond from three clients;
  - Top records: `top-a` ×4, `top-b` ×3 (blocked, category `ads-tracking`), `top-c` ×3 and `top-d`
    ×1;
  - records exactly at `base` and at `base + 60 s` (whole milliseconds), plus one record 1 ms before
    and one 1 ms after the window;
  - two engines, `e1` and `e2`.
- **No PostgreSQL change.** The management plane stores nothing new.

## Edge cases

- **Name fragments:**
  - `%`, `_` and `\` (ClickHouse LIKE) and regex metacharacters `*`, `?`, `.`, `(`, `[`, `|` and `\`
    (Loki) match literally.
  - An empty fragment does not filter.
  - A trailing dot is ignored.
  - Mixed case matches.
  - A fragment containing `"` or a newline is a valid literal in both.
- **Multi-value filters:**
  - Values containing regex or LIKE metacharacters are quoted.
  - `policy_group=global` together with `g1` matches both.
  - An engine that omits an attribute counts as the empty value, in Loki (a missing structured
    metadata field) and in ClickHouse (a missing map key gives `''`).
- **Filter generations:** a record carrying `nexora.filter.result` (OpenSearch-renamed) instead of
  `nexora.filter` still matches `filter=blocked` on ClickHouse and Loki.
- **Paging ties:**
  - Three records in one microsecond page without gap or duplicate at limit 1 on every backend.
  - A Loki response whose oldest nanosecond group is cut by the entry limit triggers a larger request.
  - More than 5,000 entries sharing one nanosecond give `ErrBackendUnavailable`, with a `debt:` marker
    (unreachable at engine microsecond resolution below 5,000 queries per microsecond).
- **Cursors:**
  - A cursor that is not base64url JSON of the backend's shape gives `ErrInvalidCursor` (400).
  - A ClickHouse cursor used against Loki is also invalid (a different shape).
  - The M7 OpenSearch cursor is unchanged.
- **Time bounds:**
  - `From` and `To` are inclusive on all four backends, in Search and in Top.
  - Zero `From` on Loki uses `To − NEXORA_LOKI_LOOKBACK`. Zero `To` means now.
  - On ClickHouse, zero bounds do not filter.
- **Top:**
  - Empty keys (records without a category) are never listed.
  - Ties at the k-th place are resolved by key ascending.
  - Fewer distinct keys than k returns all of them.
  - `Filters` applies to `Filter` as in Search.
  - On Loki, a window with no records returns an empty list: step 1 is empty, so step 2 is skipped.
- **Loki partitions:**
  - Keys ending in a non-alphanumeric character, or empty after the trailing dot is removed, fall in
    the "none" partition.
  - The partition regexes cover every string exactly once.
  - IPv6 client keys end in a hex digit or `:`.
- **Loki dedupe:** two records with equal nanosecond, client, name, type, transport and engine
  collapse into one. This is documented as a Loki limit with a `debt:` marker (it needs a duplicate
  query in the same microsecond from the same client).
- **ClickHouse Map values:** integer attributes arrive as decimal strings. A non-numeric
  `nexora.duration_us` gives 0.
- **ClickHouse schema re-apply:** re-running `querylog.sql` on an existing table is a no-op and does
  not fail.
- **Collector restart during a kw deploy:** records queued in memory can be lost for every exporter
  alike. `TestKwQueryLogBackends` compares a window that ends before the deploy's collector restart
  settles (it waits 3 minutes after rollout).
- **Mixed-version management instances during a rolling deploy:** kw stays on OpenSearch, so no
  instance changes backend mid-rollout.

## Failure modes

- **ClickHouse down or slow (over 5 s):** Search gives 503 `querylog_unavailable`, and dashboard top
  lists give `available: false`. The collector's `clickhouse` exporter retries with its sending queue,
  then drops and counts in `otelcol_exporter_send_failed_log_records`. OpenSearch and Loki pipelines
  are unaffected because they are separate pipelines.
- **ClickHouse table or database missing** (`X-ClickHouse-Exception-Code` 60 or 81): a plain error
  (500 `internal`). The management log names `apply deploy/clickhouse/querylog.sql`.
- **ClickHouse wrong password** (HTTP 403, code 516): a plain error. The log names
  `NEXORA_CLICKHOUSE_PASSWORD_FILE`.
- **ClickHouse disk full or TTL lagging:** inserts fail in the collector, while reads keep working.
- **Loki down, slow or 429:** 503 `querylog_unavailable` and `available: false`. The `otlphttp/loki`
  exporter retries and drops.
- **Loki series limit:** the partition fallback runs. A partition still over the limit gives 503 with
  `loki series limit: raise max_query_series` in the management log.
- **Loki older than 3.0, or `allow_structured_metadata: false`:** the OTLP push is rejected, the
  collector logs the error, and Search returns no records. `docs/operations.md` states the requirement.
- **Loki rejects old samples** (older than `reject_old_samples_max_age`): only data older than 168 h is
  affected, and engines never buffer that long.
- **Invalid selector or LogQL** (Loki HTTP 400 that is not a series limit): a plain error whose message
  contains Loki's parse error.
- **Password file unreadable at start:** `nexora-mgmt serve` exits with
  `NEXORA_CLICKHOUSE_PASSWORD_FILE: <err>` or `NEXORA_LOKI_PASSWORD_FILE: <err>`.
- **Dev toolbox without the binaries:** `StartClickHouse` and `StartLoki` fail the test with
  `clickhouse (or loki) not found: rebuild the toolbox image (deploy/dev/Dockerfile)`. The tests never
  skip.
- **kw ClickHouse pod not ready during deploy:** `scripts/kw-deploy.sh` waits up to 5 minutes for
  rollout, then fails before touching the Helm release. DNS is unaffected.

## Acceptance criteria

- [ ] [S-7] `TestQueryLogConformanceBuiltin` (`go test ./mgmt/internal/querylog`) runs `querylogtest.Run`
      against a second builtin fed through `Ingest`. Every subtest also checks fixed expectations, not
      only equality with the reference:
  - `partial-name` finds two `you-<run>` records;
  - `top-ties-key-ascending` gives `top-a:4, top-b:3, top-c:3`;
  - `paging-no-gaps-no-duplicates` returns all dataset records at limits 1, 2, 7 and 1000.

  Fails if the suite passes a backend that returns a whole-name match, a gap or duplicate at a page
  boundary, or ties in non-key order. `TestConformanceDetectsDivergence` wraps the builtin in a
  backend that drops the last record of every page, and asserts `querylogtest.Run` reports a failure
  through a fake `testing.TB`.

- [ ] [S-7] `TestQueryLogConformanceOpenSearch` (e2e, shared OpenSearch, unique index
      `nexora-conformance-<run>-*`, dataset sent through `otelcol-contrib`) passes every subtest.
      Fails if OpenSearch diverges from the builtin reference on any subtest.
- [ ] [S-1] [S-2] [S-3] [S-7] [S-11] `TestQueryLogConformanceClickHouse` (e2e) starts ClickHouse
      26.8.4.11 from the toolbox, applies `deploy/clickhouse/querylog.sql` twice (the second run must
      succeed), and sends the dataset through `otelcol-contrib` with the `clickhouse` exporter and
      `create_schema: false`. The adapter reads as `nexora_reader`, and every subtest passes. Fails if
      the exporter's INSERT does not match the table, if an attribute is lost, or if any subtest
      diverges.
- [ ] [S-2] [S-3] Unit tests in `mgmt/internal/querylog/clickhouse_test.go`, against an `httptest`
      server:
  - `TestClickHouseQueryIsParameterised` asserts that the name, every multi-value filter and the
    cursor reach ClickHouse only as `param_*` values, never inside the SQL text. A fragment
    `a%b_c\'); DROP` appears only escaped in `param_name`.
  - `TestClickHouseCursorKeyset` asserts `[ns, rowid]` round-trip and a garbage cursor giving
    `ErrInvalidCursor`.
  - `TestClickHouseTopSQL` asserts `ORDER BY c DESC, key ASC` and that empty keys are excluded.
  - `TestClickHouseErrors` asserts that a closed server, 503 and 429 are `ErrBackendUnavailable`,
    while 404 with exception code 60 is not.
  - `TestClickHouseSchemaHasAdapterColumns` parses `deploy/clickhouse/querylog.sql` and asserts every
    column the adapter selects or filters on exists.

  Fails if a value is interpolated into SQL, a cursor is accepted with the wrong shape, or an error
  class is mapped wrongly.

- [ ] [S-4] [S-5] [S-6] [S-7] [S-11] `TestQueryLogConformanceLoki` (e2e) starts Loki 3.6.7 from the
      toolbox with kw's `limits_config` and sends the dataset through `otelcol-contrib` with
      `otlphttp/loki` and `transform/loki`. Every subtest passes. A second run, `top-partitioned`,
      restarts Loki with `MaxQuerySeries: 3`: it first asserts that a plain
      `sum by (dns_question_name)` query gets the series-limit error, then asserts `Top` still equals
      the reference. Fails if Loki diverges on any subtest, the fallback is not exercised, or the
      same-microsecond `tie-<run>` records collapse.
- [ ] [S-5] [S-6] Unit tests in `mgmt/internal/querylog/loki_test.go`:
  - `TestLokiLogQLQuotesValues` asserts the selector, `(?i)` substring regex, quoted alternations,
    `global` as the empty alternative, the `nexora_filter` / `nexora_filter_result` either-match,
    `start`/`end = To+1ns`, and the `categorize-labels` header.
  - `TestLokiPagingTiesAtOneNanosecond` uses a fake Loki holding 5 entries in one nanosecond and a
    response cut at the limit, and pages at limit 2 without gap or duplicate.
  - `TestLokiTopTwoStepTies` asserts ties sorted by key ascending and the `>= c` second step.
  - `TestLokiTopPartitionFallback` makes the fake answer the series-limit 400 for unpartitioned
    queries, then asserts merged partition results equal the unpartitioned expectation and at most 6
    concurrent requests.
  - `TestLokiPartitionsCoverEveryKey` checks 10,000 random keys, including empty, trailing-dot,
    uppercase, IPv6 and non-ASCII keys, and requires each to match exactly one partition regex.
  - `TestLokiErrors` asserts that a closed server, 503 and 429 are `ErrBackendUnavailable`, a parse
    error 400 is not, and a partition still over the limit is `ErrBackendUnavailable`.

  Fails if any quoting, tie, fallback or error rule is broken.

- [ ] [S-8] `TestLoadQueryLogBackends` in `mgmt/internal/config/config_test.go` asserts:
  - the defaults of every [S-8] variable;
  - the required-URL errors for `clickhouse` and `loki`;
  - selector without braces rejected;
  - lookback `30m` and `800h` rejected;
  - `builtin` and `opensearch` unchanged.

  `TestServeBuildsQueryLogBackend` in `mgmt/cmd/nexora-mgmt` asserts that `clickhouse` and `loki`
  build adapters named `clickhouse` and `loki` and set `QueryLogToManagement` false. Fails if a backend
  name is accepted without its URL, or engines are pointed at the management plane for a non-builtin
  backend.

- [ ] [S-8] [S-11] `TestQueryLogBackends` runs the subtests `clickhouse` and `loki` next to `builtin`
      and `opensearch`, with engine → collector → backend → management plane → `e2e/querylog.spec.ts`,
      and passes these subtests for all four:
  - `partial-name` and `multi-value` (from M6);
  - `dashboard-top`: after 5 queries for `dt-<n>.test.` and 2 for another name, `GET /dashboard/top?range=15m`
    returns `available: true` with `dt-<n>.test.` counted 5.

  `TestQueryLogCategoryAttribution` runs for `clickhouse` and `loki` with the M6 reason assertions.
  Fails if a new backend is missing from the GUI flow, reason fields are missing, or top lists are
  unavailable.

- [ ] [S-9] `TestHelmQueryLogBackends` in `deploy/deploytest` renders the chart:
  - with `backend=clickhouse` it asserts `NEXORA_CLICKHOUSE_URL`, `_DATABASE`, `_TABLE`, `_USERNAME`
    and `_PASSWORD_FILE=/etc/nexora/querylog/password` from the named Secret key;
  - with `backend=loki` it asserts `NEXORA_LOKI_URL`, `_SELECTOR`, `_TENANT` and `_LOOKBACK`;
  - a missing URL fails `helm template` with the `required` message;
  - `helm lint --strict` passes;
  - the kw render still has `NEXORA_QUERYLOG_BACKEND=opensearch`.

  Fails if a value is not rendered, the schema rejects the new enum values, or kw changes backend.

- [ ] [S-10] `TestKwClickHouseManifest` in `deploy/deploytest` statically asserts that
      `deploy/kw/clickhouse.yaml` has:
  - namespace `nexora`;
  - image `192.168.10.131/clickhouse/clickhouse-server:26.8.4.11`;
  - a `longhorn-single` 20 Gi claim;
  - passwords only through `from_env` from Secret `nexora-clickhouse`;
  - `default` user networks limited to `::1` and `127.0.0.1`;
  - `nexora_reader` with SELECT only and `nexora_writer` with INSERT only;
  - resource requests and limits.

  `TestKwCollectorFansOutQueryLogs` asserts that `deploy/kw/otelcol.yaml` has pipelines `logs`
  (OpenSearch, unchanged processors), `logs/clickhouse` (`create_schema: false`, `nexora_writer` from
  env) and `logs/loki` (`http://loki.monitoring.svc:3100/otlp` with `transform/loki`), and that
  `otelcol-contrib validate` accepts the config in the dev pod. Fails if a secret is inlined or a
  pipeline is missing.

- [ ] [S-10] `TestKwQueryLogBackends`, run by `scripts/kw-acceptance.sh` after the M10
      `scripts/kw-deploy.sh`, has two parts:
  - It sends 20 unique names through 192.168.10.136 and .139, and requires OpenSearch, ClickHouse and
    Loki to return the same 20 records (name, client, type, rcode, engine id, filter) within 60 s.
  - For a 10-minute window ending 3 minutes before the test, it requires the three backends' top-10
    names and top-10 clients to be equal in keys, order and counts.

  The M10 deploy loses zero probe queries on both addresses, and the full `scripts/kw-acceptance.sh`
  passes. Fails if any backend misses a record, the top lists differ, or a probe query is lost.

- [ ] [S-11] `scripts/dev-selftest.sh` gains pinned-version checks: it exits non-zero when `clickhouse` is missing or does not report 26.8.4.11, or `loki` is missing or does not report 3.6.7 (proven by running it with a PATH lacking each binary), and exits 0 in the rebuilt toolbox. `TestHarnessStartsClickHouseAndLoki` in
      `e2e/harness` starts both, runs `SELECT 1` as `nexora_reader` and gets Loki `/ready`. It fails
      with the named rebuild message when a binary is absent from `PATH`. Fails if a binary is
      missing, at the wrong version, or not startable on free ports.
- [ ] [S-12] `TestOperationsDocNamesQueryLogSettings` in `deploy/deploytest` asserts:
  - every `NEXORA_CLICKHOUSE_*` and `NEXORA_LOKI_*` name in `mgmt/internal/config/config.go` appears in
    `docs/operations.md` and `docs/architecture.md`;
  - `docs/operations.md` names `deploy/clickhouse/querylog.sql`, `create_schema: false`, `/otlp` and
    `allow_structured_metadata`;
  - `web/src/help/topics/observability.md` names ClickHouse and Loki.

  `TestHelpTopicsReferenceOperationsDoc` still passes. Fails if a setting or requirement is
  undocumented.

## Open questions
