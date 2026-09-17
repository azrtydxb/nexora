-- Nexora query-log table for ClickHouse 26.8.
--
-- The OpenTelemetry Collector's clickhouse exporter (contrib v0.160.0) inserts into this table and
-- must not create it: the first 16 columns are the exporter's logs column set with its types and
-- codecs. RowID breaks ties between records of one nanosecond for cursor paging, and the
-- materialized columns hold every attribute the management plane reads.
--
-- Applied by scripts/kw-deploy.sh and by the e2e harness (StartClickHouse). Every statement must
-- stay idempotent: re-applying this file to an existing table is a no-op. A later schema change
-- ships as a new file with its own upgrade note.

CREATE DATABASE IF NOT EXISTS nexora;

CREATE TABLE IF NOT EXISTS nexora.querylog (
    `Timestamp` DateTime64(9) CODEC(Delta(8), ZSTD(1)),
    `TraceId` String CODEC(ZSTD(1)),
    `SpanId` String CODEC(ZSTD(1)),
    `TraceFlags` UInt8,
    `SeverityText` LowCardinality(String) CODEC(ZSTD(1)),
    `SeverityNumber` UInt8,
    `ServiceName` LowCardinality(String) CODEC(ZSTD(1)),
    `Body` String CODEC(ZSTD(1)),
    `ResourceSchemaUrl` LowCardinality(String) CODEC(ZSTD(1)),
    `ResourceAttributes` Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    `ScopeSchemaUrl` LowCardinality(String) CODEC(ZSTD(1)),
    `ScopeName` String CODEC(ZSTD(1)),
    `ScopeVersion` LowCardinality(String) CODEC(ZSTD(1)),
    `ScopeAttributes` Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    `LogAttributes` Map(LowCardinality(String), String) CODEC(ZSTD(1)),
    `EventName` String CODEC(ZSTD(1)),
    `RowID` UUID DEFAULT generateUUIDv4(),
    `Name` String MATERIALIZED LogAttributes['dns.question.name'] CODEC(ZSTD(1)),
    `Client` String MATERIALIZED LogAttributes['client.address'] CODEC(ZSTD(1)),
    `QType` LowCardinality(String) MATERIALIZED LogAttributes['dns.question.type'],
    `RCode` LowCardinality(String) MATERIALIZED LogAttributes['dns.response.code'],
    `Cache` LowCardinality(String) MATERIALIZED LogAttributes['nexora.cache'],
    `Filter` LowCardinality(String) MATERIALIZED if(LogAttributes['nexora.filter'] != '', LogAttributes['nexora.filter'], LogAttributes['nexora.filter.result']),
    `ListID` String MATERIALIZED LogAttributes['nexora.filter.list_id'] CODEC(ZSTD(1)),
    `Category` LowCardinality(String) MATERIALIZED LogAttributes['nexora.filter.category'],
    `Source` LowCardinality(String) MATERIALIZED LogAttributes['nexora.filter.source'],
    `Rule` String MATERIALIZED LogAttributes['nexora.filter.rule'] CODEC(ZSTD(1)),
    `PolicyGroupID` String MATERIALIZED LogAttributes['nexora.policy.group'] CODEC(ZSTD(1)),
    `RPZZoneID` String MATERIALIZED LogAttributes['nexora.rpz_zone'] CODEC(ZSTD(1)),
    `RPZAction` LowCardinality(String) MATERIALIZED if(LogAttributes['nexora.rpz'] = 'none', '', LogAttributes['nexora.rpz']),
    `ACLRefused` LowCardinality(String) MATERIALIZED LogAttributes['nexora.acl.refused'],
    `UpstreamsRaced` Int64 MATERIALIZED toInt64OrZero(LogAttributes['nexora.upstream_raced']),
    `Upstream` String MATERIALIZED LogAttributes['nexora.upstream'] CODEC(ZSTD(1)),
    `Transport` LowCardinality(String) MATERIALIZED LogAttributes['nexora.transport'],
    `EngineID` String MATERIALIZED if(LogAttributes['nexora.engine.id'] != '', LogAttributes['nexora.engine.id'], ResourceAttributes['nexora.engine.id']) CODEC(ZSTD(1)),
    `DurationUS` Int64 MATERIALIZED toInt64OrZero(LogAttributes['nexora.duration_us']),
    INDEX idx_name lower(Name) TYPE ngrambf_v1(3, 65536, 3, 0) GRANULARITY 1,
    INDEX idx_client Client TYPE bloom_filter(0.01) GRANULARITY 1
) ENGINE = MergeTree
PARTITION BY toDate(Timestamp)
ORDER BY (toStartOfFiveMinutes(Timestamp), ServiceName, Timestamp)
TTL toDateTime(Timestamp) + INTERVAL 7 DAY
SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;
