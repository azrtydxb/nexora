# nexora-m10-querylog-backends — implementation plan

Status: draft
Spec: .procoder/specs/nexora-m10-querylog-backends.md

## Goal

Ship milestone M10 (GitHub issues #35 ClickHouse and #36 Loki): two query-log backends that implement
the full post-M6 backend contract with the same semantics as builtin and OpenSearch, proven by one
conformance suite across all four backends, local e2e, and live kw traffic.

## Architecture

Engines keep sending OTLP logs to the OpenTelemetry Collector. The collector fans each record out to
OpenSearch, to ClickHouse (the contrib `clickhouse` exporter into a Nexora-owned table), and to Loki's
native OTLP endpoint (with a body transform). The management plane gains two read-only adapters in
`mgmt/internal/querylog` (`ClickHouse`, `Loki`). Both use plain `net/http`, both implement `Backend`
and `Topper`, and both are chosen by `NEXORA_QUERYLOG_BACKEND`.

A new package `mgmt/internal/querylog/querylogtest` holds the canonical dataset and the conformance
suite. The suite compares any backend with a builtin reference fed the same dataset. It runs as a unit
test for builtin and as e2e tests for OpenSearch, ClickHouse and Loki, each of which receives the
dataset through a real `otelcol-contrib`.

### Decisions not settled by the spec (made here, binding for the tasks)

- **Package layout:**
  - `mgmt/internal/querylog/clickhouse.go` holds the adapter.
  - `mgmt/internal/querylog/loki.go` holds the adapter and `loki_logql.go` holds the LogQL building,
    quoting and partitions.
  - `querylogtest` imports only `querylog`, the OTLP protos and `testing`, never `e2e/harness`, so the
    unit test can use it.
- **Shared constants:** the adapters use the existing unexported `defaultLimit` (100) and `maxLimit`
  (1000) of `builtin.go`. The timeouts are `clickHouseTimeout = 5 * time.Second`,
  `lokiSearchTimeout = 5 * time.Second` and `lokiTopTimeout = 15 * time.Second`.
- **Conformance comparison:**
  - `querylogtest.Run` walks every query at limits 1, 2, 7 and 1000 by following `NextCursor`.
  - It compares the concatenation with the reference's limit-1000 page. The comparison is a sequence
    of `Time.Truncate(time.Millisecond)` values that must match exactly, and a multiset of
    `recordKey(r)`.
  - `recordKey` joins every `Record` field except `Time`, plus the millisecond time, with `\x1f`.
  - A page may never repeat a record key already seen, unless the dataset holds that key twice. The
    dataset holds no duplicates.
- **Isolation on shared services:**
  - OpenSearch conformance writes to index `nexora-conformance-<run>` (collector
    `logs_index: nexora-conformance-<run>`, adapter index `nexora-conformance-<run>*`). The test
    deletes the index at cleanup with `DELETE /nexora-conformance-<run>*`.
  - ClickHouse and Loki conformance use fresh local processes per test.
- **Dataset time:**
  - `base = time.Now().UTC().Truncate(time.Millisecond).Add(-5 * time.Minute)`.
  - The window is `[base, base+60s]`. Loki accepts it (within `reject_old_samples_max_age`), and the
    Top window is under the 1 h instant split interval.
- **Visibility:**
  - `Harness.Visible(t, want)` polls `Search{From, To, Limit: 1000}` every 250 ms for up to 60 s,
    until `want` records are visible.
  - `want` counts only records inside the window: the dataset length minus the 2 out-of-window
    records.
- **Loki tiebreak key:** `hex(sha256(line + "\x00" + k1 + "=" + v1 + "\x00" + ...))` with structured
  metadata keys sorted, and the value of `nexora_engine_id` taken once.
- **Loki partitions:**
  - Partition `i` of `0..35` has the label value regex `(?i)^.*<c>\.?$`, where `<c>` is
    `"0123456789abcdefghijklmnopqrstuvwxyz"[i]`.
  - Partition 36 ("none") has the regex `(?i)^(?:|\.|.*[^0-9a-z]\.?)$`.
  - `LokiPartitionRegexes()` returns them.
- **ClickHouse users in the harness:** `nexora_writer` / `writer-e2e` and `nexora_reader` with a
  random password written to `<dir>/reader-password`. They are defined in a `users.d/nexora.xml`
  that has the same structure as the kw ConfigMap.
- **kw secrets for the collector:** `nexora-otelcol` gets `CLICKHOUSE_WRITER_PASSWORD` from Secret
  `nexora-clickhouse` key `writer-password`. The exporter config uses `${env:CLICKHOUSE_WRITER_PASSWORD}`.
- **Toolbox image tag:** the next unused `toolbox-N` in `192.168.10.131/azrtydxb/nexora-dev`, found
  with `curl -sk https://192.168.10.131/v2/azrtydxb/nexora-dev/tags/list`. It is applied to
  `deploy/dev/dev-pod.yaml`, and with `kubectl set image` to any other running `toolbox-*`
  deployment in `nexora-dev`.

### Wave order and file ownership

A task may start when every task of every earlier wave is committed. Tasks in one wave never edit the
same file. Each task's `Files:` line is its exclusive ownership.

| Wave | Tasks (parallel)                                                                                             | Shared files it serialises                                                                                                                                                                 |
| ---- | ------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 0    | 1 conformance suite, 2 configuration and architecture text, 3 toolbox binaries, 4 harness collector and mgmt | `docs/architecture.md`, `config.go` (2); `deploy/dev/*` (3); `e2e/harness/otelcol.go`, `e2e/harness/mgmt.go` (4)                                                                           |
| 1    | 5 ClickHouse backend, 6 Loki backend, 7 OpenSearch conformance                                               | `querylog/clickhouse*.go`, `deploy/clickhouse/querylog.sql`, `e2e/harness/clickhouse.go` (5); `querylog/loki*.go`, `e2e/harness/loki.go` (6); `querylog/opensearch.go` (7)                 |
| 2    | 8 management plane wiring and product e2e, 9 Helm chart, 10 kw manifests and acceptance, 11 operations docs  | `mgmt/cmd/nexora-mgmt/main.go`, `e2e/gui_test.go`, `e2e/filter_attribution_test.go`, `openapi.yaml` (8); chart files (9); `deploy/kw/*`, `scripts/kw-*.sh` (10); `docs/operations.md` (11) |
| 3    | 12 full verification, kw deploy, acceptance and issue close                                                  | none (report only)                                                                                                                                                                         |

Dependencies beyond the wave rule:

- 5 and 6 consume 1 (suite), 2 (config types), 3 (binaries) and 4 (collector fields).
- 7 consumes 1 and 4.
- 8 consumes 2, 5 and 6.
- 9 consumes 2 (names only).
- 10 consumes 5 (SQL, adapter) and 6 (adapter).
- 11 consumes 2, 5 and 6.
- 12 consumes everything.

### Spec coverage

| Spec item                    | Tasks         |
| ---------------------------- | ------------- |
| S-1 ClickHouse ingestion     | 4, 5, 10      |
| S-2 ClickHouse search        | 5             |
| S-3 ClickHouse Top           | 5             |
| S-4 Loki ingestion           | 4, 6, 10      |
| S-5 Loki search              | 6             |
| S-6 Loki Top                 | 6             |
| S-7 conformance suite        | 1, 5, 6, 7    |
| S-8 configuration and wiring | 2, 8          |
| S-9 Helm chart               | 9             |
| S-10 kw deployment           | 10, 12        |
| S-11 local e2e               | 3, 4, 5, 6, 8 |
| S-12 documentation           | 2, 10, 11     |

## Constraints

Copied from the spec (binding for every task):

- The backend contract is the committed post-M6 and post-M7 `mgmt/internal/querylog` package:
  - `Backend` (`Name`, `Search`), `Topper` (`Top`), `Query` with slice filters, `Record` with reason
    fields, `TopQuery` and `TopEntry`;
  - `ErrBackendUnavailable` and `ErrInvalidCursor`;
  - the M7 OpenSearch cursor `[timestamp, _id]`.

  M10 adds no field to them. Where a committed name differs from this plan, the committed code wins and
  the conformance suite follows it.

- "Identical semantics" means that, for every conformance query, the concatenated pages of a backend
  equal the builtin reference:
  - as a sequence of millisecond-truncated timestamps;
  - as a multiset of records;
  - with order within one millisecond backend-defined;
  - with Top lists identical, including the key-ascending order of ties.
- Timeouts:
  - Search 5 s on every backend.
  - ClickHouse Top 5 s.
  - Loki Top 15 s in total.
- Error mapping:
  - Transport errors, HTTP 5xx and HTTP 429 are `ErrBackendUnavailable`.
  - Other 4xx answers are plain errors.
  - One exception: a Loki series-limit 400 triggers the partition fallback.
- No new Go module dependency: both adapters use `net/http` and `encoding/json`.
- Secrets come only from files and Kubernetes Secrets (`nexora-clickhouse` in `nexora`). The
  management plane uses `nexora_reader` and the collector `nexora_writer`.
- kw deploys:
  - `scripts/kw-deploy.sh` with zero lost probe queries on 192.168.10.136 and 192.168.10.139, then
    `scripts/kw-acceptance.sh`.
  - `helm rollback nexora` on any loss or failure.
  - 192.168.10.136 and .139 never change, and nothing in `monitoring` changes.
- Pinned versions: ClickHouse 26.8.4.11, Loki 3.6.7, otelcol-contrib 0.160.0. Images come through
  Nexus `192.168.10.131`.
- Migrations: 01100–01109 is reserved for M10, and none is needed.
- Every existing Go, Rust and Playwright test keeps passing, including `TestGUICoverage`. Tests only
  gain backends.

Project rules (from `.procoder/notes/implementer-brief.md` and `docs/architecture.md`):

- **Where things run:**
  - Builds and tests run in the kw dev pod: `scripts/dev-exec.sh '<command>'`.
  - e2e needs `make e2e-build` and `NEXORA_E2E_BIN_DIR=/work/nexora/bin`.
  - `mgmt/internal/api/gen.go` and `web/src/api/schema.d.ts` are generated on the laptop.
- **Commits:** implementers never commit. They report the changed paths, and the lead commits with
  `scripts/commit-paths.sh`.
- **TDD:** write the failing test, run it, see the stated failure, implement, see it pass. Never
  weaken or delete a test.
- **Formatters and linters** on changed files:
  - `gofmt -l`, `go vet ./...`, `golangci-lint run` on the changed packages;
  - `scripts/pc-format.sh <md and yaml files>`;
  - `cd web && pnpm run lint` when a web file changes.
- **Tests:**
  - Every test that asserts "does not happen" first asserts the positive path in the same run.
  - Mark deliberate ceilings with `debt:` comments naming the ceiling and the revisit condition.
- **Go:** module `github.com/piwi3910/nexora`, Go 1.27.

## Task 1: Conformance suite and builtin conformance

Files:

- `mgmt/internal/querylog/querylogtest/dataset.go`: created. The canonical dataset and window.
- `mgmt/internal/querylog/querylogtest/conformance.go`: created. `Run` and its subtests.
- `mgmt/internal/querylog/conformance_test.go`: created. `TestQueryLogConformanceBuiltin` and
  `TestConformanceDetectsDivergence`.

Interfaces (consumed by Tasks 5, 6 and 7):

```go
package querylogtest

type EngineBatch struct {
	EngineID string
	Req      *collogspb.ExportLogsServiceRequest
}

// Harness adapts one backend to the suite. Ingest delivers one batch attributed to EngineID
// (builtin: Builtin.Ingest; others: OTLP export to a collector with EngineID as the
// nexora.engine.id log and resource attribute). Visible blocks until want in-window records
// are searchable.
type Harness struct {
	Backend querylog.Backend
	Ingest  func(t *testing.T, engineID string, req *collogspb.ExportLogsServiceRequest)
	Visible func(t *testing.T, want int)
}

func Dataset(run string, base time.Time) []EngineBatch // ascending time across all batches
func Window(base time.Time) (from, to time.Time)       // base, base+60s
func InWindow(batches []EngineBatch, base time.Time) int
func Run(t testing.TB, run string, base time.Time, h Harness)
func OTLPExport(t testing.TB, grpcAddr string, batches []EngineBatch) // plain-text OTLP gRPC export
func PollVisible(b querylog.Backend, base time.Time) func(t *testing.T, want int) // 250 ms polls, 60 s
```

- [ ] Create `mgmt/internal/querylog/conformance_test.go`:
  ```go
  package querylog_test

  import (
  	"context"
  	"strconv"
  	"testing"
  	"time"

  	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"

  	"github.com/piwi3910/nexora/mgmt/internal/querylog"
  	"github.com/piwi3910/nexora/mgmt/internal/querylog/querylogtest"
  )

  func builtinHarness(b *querylog.Builtin) querylogtest.Harness {
  	return querylogtest.Harness{
  		Backend: b,
  		Ingest: func(_ *testing.T, id string, req *collogspb.ExportLogsServiceRequest) { b.Ingest(id, req) },
  		Visible: func(*testing.T, int) {},
  	}
  }

  func TestQueryLogConformanceBuiltin(t *testing.T) {
  	run := strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
  	base := time.Now().UTC().Truncate(time.Millisecond).Add(-5 * time.Minute)
  	querylogtest.Run(t, run, base, builtinHarness(querylog.NewBuiltin(10_000)))
  }

  // lossy drops the last record of every non-final page: a paging bug the suite must catch.
  type lossy struct{ querylog.Backend }

  func (l lossy) Search(ctx context.Context, q querylog.Query) (querylog.Page, error) {
  	p, err := l.Backend.Search(ctx, q)
  	if err == nil && p.NextCursor != "" && len(p.Records) > 1 {
  		p.Records = p.Records[:len(p.Records)-1]
  	}
  	return p, err
  }

  type recordingTB struct {
  	testing.TB
  	failed bool
  }

  func (r *recordingTB) Errorf(string, ...any) { r.failed = true }
  func (r *recordingTB) Fatalf(string, ...any) { r.failed = true; panic(errStop) }
  func (r *recordingTB) Helper()               {}

  var errStop = new(struct{})

  func TestConformanceDetectsDivergence(t *testing.T) {
  	base := time.Now().UTC().Truncate(time.Millisecond).Add(-5 * time.Minute)
  	good := querylog.NewBuiltin(10_000)
  	h := builtinHarness(good)
  	querylogtest.Run(t, "pos", base, h) // positive path: the unmodified builtin passes
  	bad := querylog.NewBuiltin(10_000)
  	h = builtinHarness(bad)
  	h.Backend = lossy{bad}
  	rec := &recordingTB{TB: t}
  	func() {
  		defer func() { _ = recover() }()
  		querylogtest.Run(rec, "neg", base, h)
  	}()
  	if !rec.failed {
  		t.Fatal("conformance suite passed a backend that loses a record per page")
  	}
  }
  ```
  `Run` takes `testing.TB`. Its subtests run through a helper `sub(tb, name, fn)`: it calls `t.Run`
  when `tb` is a `*testing.T`, and otherwise calls `fn(tb)` directly, so the recording TB sees every
  failure.
- [ ] Run
      `scripts/dev-exec.sh 'go test ./mgmt/internal/querylog -run "TestQueryLogConformanceBuiltin|TestConformanceDetectsDivergence" -count=1'`
      and expect FAIL: `package github.com/piwi3910/nexora/mgmt/internal/querylog/querylogtest is not in std` (no such package).
- [ ] Create `dataset.go`. Each record is an OTLP `LogRecord` with the engine's attribute keys (see
      `engine/src/telemetry/otlp.rs` `log_record`), built with `str(k, v)` and `integer(k, n)`. Its
      `TimeUnixNano` is `base + offset`. Every batch's resource carries `service.name=nexora-engine`
      and `nexora.engine.id=<engine>`, and every record also carries the log attribute
      `nexora.engine.id`. The records, in ascending offset order:

      | Offset (µs)  | Engine | Name                        | Attributes beyond the defaults (client 10.0.0.1, A, NOERROR, cache miss, filter none, transport udp, duration 100, policy group "", rpz none) |
                  | ------------ | ------ | --------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------- |
                  | −1,000       | e1     | `before-<run>.test.`        | (outside the window)                                                                                                                          |
                  | 0            | e1     | `edge-from-<run>.test.`     | exactly at From                                                                                                                               |
                  | 1,000,000    | e1     | `www.you-<run>.tube.test.`  | filter allowed, source allowlist, list_id `allow-g1`, rule `tube.test`, policy group `g1`                                                     |
                  | 2,000,000    | e1     | `you-<run>.test.`           | rcode NXDOMAIN, cache hit, client 10.0.0.2                                                                                                    |
                  | 3,000,000    | e2     | `x*y-<run>.test.`           | type AAAA                                                                                                                                     |
                  | 4,000,000    | e2     | `x%y_<run>.test.`           | (LIKE metacharacters)                                                                                                                         |
                  | 5,000,000    | e1     | `X?Y-<run>.TEST.`           | type MX                                                                                                                                       |
                  | 6,000,000    | e1     | `casino-<run>.test.`        | filter blocked, source category, list_id `l-gambling`, category `gambling`                                                                    |
                  | 7,000,000    | e2     | `ads-<run>.test.`           | filter blocked, source blocklist, list_id `l-custom`, rule `ads-<run>.test`, policy group `g1`                                                 |
                  | 8,000,000    | e1     | `renamed-<run>.test.`       | `nexora.filter.result=blocked` instead of `nexora.filter`, source blocklist                                                                   |
                  | 9,000,000    | e1     | `rpz-<run>.test.`           | rcode NXDOMAIN, source rpz, `nexora.rpz_zone=z1`, `nexora.rpz=nxdomain`                                                                       |
                  | 10,000,000   | e2     | `rw-<run>.test.`            | source rewrite, rule `rw-<run>.test`                                                                                                          |
                  | 11,000,000   | e1     | `acl-<run>.test.`           | rcode REFUSED, source acl, `nexora.acl.refused=authoritative`                                                                                 |
                  | 12,000,000   | e2     | `race-<run>.test.`          | upstream `fx`, `nexora.upstream_raced=3` (int), duration 1234, transport tcp                                                                  |
                  | 13,000,000   | e1     | `tie-<run>.test.`           | client 10.0.0.11                                                                                                                              |
                  | 13,000,000   | e1     | `tie-<run>.test.`           | client 10.0.0.12                                                                                                                              |
                  | 13,000,000   | e2     | `tie-<run>.test.`           | client 10.0.0.13                                                                                                                              |
                  | 20,000,000 + i·100,000 | e1/e2 alternating | `top-a-<run>.test.` ×4, `top-b-<run>.test.` ×3, `top-c-<run>.test.` ×3, `top-d-<run>.test.` ×1 | clients 10.0.1.<i>; `top-b` also filter blocked, category `ads-tracking`, source category |
                  | 60,000,000   | e2     | `edge-to-<run>.test.`       | exactly at To                                                                                                                                 |
                  | 60,001,000   | e2     | `after-<run>.test.`         | (outside the window)                                                                                                                          |

                  `Dataset` returns one `EngineBatch` per maximal run of consecutive records with the same engine,
                  so ingestion stays in ascending time. The `top-*` records use distinct clients 10.0.1.0–10.0.1.10,
                  so no two records share a key.

- [ ] Create `conformance.go`. `Run(tb, run, base, h)`:
  1. Ingest every batch into `h` and into a fresh reference `querylog.NewBuiltin(10_000)`, in order.
  2. Call `h.Visible(tb, InWindow(...))`.
  3. For each named case, build a `querylog.Query` with `From, To = Window(base)`. Cases whose name
     matching is not what they test also set `Name: "-<run>."`.
  4. Compare with `compareSearch(tb, h.Backend, ref, q)`: walk limits 1, 2, 7 and 1000, following
     cursors up to 200 pages, and apply the millisecond-sequence and multiset rules from Architecture.
  5. Also assert the fixed expectations:
     - `partial-name`:
       - `Name: "you-"+run` → 2 records;
       - `Name: "TUBE.TEST."` with `Name` containing the run → 1;
       - `x*y-`+run → 1;
       - `x?y-`+run → 1 (`X?Y` literal, case-insensitive);
       - `x_y`+run → 0 (`_` is literal);
       - `x%y_`+run → 1;
       - `absent-`+run → 0.
     - `multi-value`:
       - `QTypes {A, AAAA}` with `Name` `x` + run-suffix fragments → only A/AAAA;
       - `RCodes {NXDOMAIN}` + `QTypes {A}` → `you-` and `rpz-` (2);
       - `Caches {hit}` → 1.
     - `reason-fields`: `Sources {allowlist}` → 1 record with `Rule == "tube.test"`,
       `PolicyGroupID == "g1"` and `ListID == "allow-g1"`. Also check `rpz` (`RPZZoneID == "z1"`,
       `RPZAction == "nxdomain"`), `acl` (`ACLRefused == "authoritative"`) and `race`
       (`UpstreamsRaced == 3`, `DurationUS == 1234`, `Transport == "tcp"`).
     - `policy-group-global`: `PolicyGroups {global}` → every in-window record except the two in
       `g1`. `{global, g1}` → all of them.
     - `filter-generations`: `Filters {blocked}` includes `renamed-<run>`.
     - `time-range-inclusive`: the full window includes `edge-from` and `edge-to` and excludes
       `before` and `after`.
     - `paging-no-gaps-no-duplicates`: all in-window records at each limit. The three `tie-` records
       are all present at limit 1.
     - `invalid-cursor`: `Cursor: "not-a-cursor"` → `errors.Is(err, querylog.ErrInvalidCursor)`.
     - `top-name`: Top `Field name, Limit 3` → `top-a:4`, then `top-b:3` and `top-c:3` in key order.
     - `top-ties-key-ascending`: `Limit 2` → `top-a:4, top-b:3` (not `top-c`).
     - `top-blocked`: `Filters {blocked}`, `Field name` → `top-b` counted 3 and `casino`, `ads`,
       `renamed` counted 1.
     - `top-client`: `Field client, Limit 1` → `10.0.0.1` with the exact reference count.
     - `top-category-skips-empty`: `Field category` → exactly `ads-tracking:3, gambling:1`, no empty
       key.
     - `top-time-range-inclusive`: `From`/`To` set to the offsets of `edge-from`/`edge-to` with
       `Field name` includes both edges and excludes `before` and `after`.

     Every Top case compares with `ref.Top` for equality (`reflect.DeepEqual`) before the fixed
     expectation. Top results are filtered to keys containing `<run>` before comparison, except
     `top-client` and `top-category-skips-empty`, which run in a fresh backend (ClickHouse, Loki) or
     a unique index.
- [ ] Add to `conformance.go`: `OTLPExport` dials `grpcAddr` with `grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))` and calls `collogspb.NewLogsServiceClient(conn).Export` once per batch in order, failing the test on error. `PollVisible(b, base)` returns a closure that polls `b.Search(Query{From, To: Window(base), Limit: 1000})` every 250 ms for up to 60 s until `len(Records) >= want`, then fails with the last count.
- [ ] Run the command from step 2 and expect PASS. Then run
      `scripts/dev-exec.sh 'gofmt -l mgmt/internal/querylog && go vet ./mgmt/internal/querylog/...'`
      and expect no output.
- [ ] Report the paths. Commit message: `querylog: conformance suite with builtin reference`.

## Task 2: Configuration and architecture text

Files:

- `mgmt/internal/config/config.go`: the `ClickHouseConfig` and `LokiConfig` types, loading and
  validation.
- `mgmt/internal/config/config_test.go`: `TestLoadQueryLogBackends`.
- `docs/architecture.md`: management environment list, query-log backend paragraph, harness and kw
  deployment sections.

Interfaces (consumed by Tasks 5, 6, 8 and 9):

```go
package config

// ClickHouseConfig configures the ClickHouse query-log backend (HTTP interface).
type ClickHouseConfig struct {
	URL, Database, Table, Username, PasswordFile string
}

// LokiConfig configures the Loki query-log backend.
type LokiConfig struct {
	URL, Selector, Tenant, Username, PasswordFile string
	Lookback                                      time.Duration
}

// Config gains:
//	ClickHouse ClickHouseConfig
//	Loki       LokiConfig
```

- [ ] Add to `mgmt/internal/config/config_test.go`:
  ```go
  func TestLoadQueryLogBackends(t *testing.T) {
  	base := map[string]string{"NEXORA_DATABASE_URL": "postgres://x", "NEXORA_CA_CERT_FILE": "c", "NEXORA_CA_KEY_FILE": "k"}
  	load := func(extra map[string]string) (config.Config, error) {
  		m := maps.Clone(base)
  		maps.Copy(m, extra)
  		return config.Load(func(k string) string { return m[k] })
  	}
  	c, err := load(map[string]string{"NEXORA_QUERYLOG_BACKEND": "clickhouse", "NEXORA_CLICKHOUSE_URL": "http://ch:8123"})
  	if err != nil || c.ClickHouse.Database != "nexora" || c.ClickHouse.Table != "querylog" || c.ClickHouse.Username != "default" {
  		t.Fatalf("clickhouse defaults: %+v %v", c.ClickHouse, err)
  	}
  	c, err = load(map[string]string{"NEXORA_QUERYLOG_BACKEND": "loki", "NEXORA_LOKI_URL": "http://loki:3100", "NEXORA_LOKI_TENANT": "t1"})
  	if err != nil || c.Loki.Selector != `{service_name="nexora-engine"}` || c.Loki.Lookback != 168*time.Hour || c.Loki.Tenant != "t1" {
  		t.Fatalf("loki defaults: %+v %v", c.Loki, err)
  	}
  	for _, tc := range []struct {
  		env  map[string]string
  		want string
  	}{
  		{map[string]string{"NEXORA_QUERYLOG_BACKEND": "clickhouse"}, "NEXORA_CLICKHOUSE_URL is required when NEXORA_QUERYLOG_BACKEND=clickhouse"},
  		{map[string]string{"NEXORA_QUERYLOG_BACKEND": "loki"}, "NEXORA_LOKI_URL is required when NEXORA_QUERYLOG_BACKEND=loki"},
  		{map[string]string{"NEXORA_QUERYLOG_BACKEND": "loki", "NEXORA_LOKI_URL": "http://l", "NEXORA_LOKI_SELECTOR": `service_name="x"`}, "NEXORA_LOKI_SELECTOR must be a LogQL stream selector in braces"},
  		{map[string]string{"NEXORA_QUERYLOG_BACKEND": "loki", "NEXORA_LOKI_URL": "http://l", "NEXORA_LOKI_LOOKBACK": "30m"}, "NEXORA_LOKI_LOOKBACK must be a duration between 1h and 721h"},
  		{map[string]string{"NEXORA_QUERYLOG_BACKEND": "loki", "NEXORA_LOKI_URL": "http://l", "NEXORA_LOKI_LOOKBACK": "800h"}, "NEXORA_LOKI_LOOKBACK must be a duration between 1h and 721h"},
  		{map[string]string{"NEXORA_QUERYLOG_BACKEND": "elastic"}, "NEXORA_QUERYLOG_BACKEND must be builtin, opensearch, clickhouse or loki"},
  	} {
  		if _, err := load(tc.env); err == nil || !strings.Contains(err.Error(), tc.want) {
  			t.Errorf("%v -> %v, want %q", tc.env, err, tc.want)
  		}
  	}
  	if c, err := load(nil); err != nil || c.QueryLogBackend != "builtin" {
  		t.Fatalf("builtin default unchanged: %v %v", c.QueryLogBackend, err)
  	}
  	if _, err := load(map[string]string{"NEXORA_QUERYLOG_BACKEND": "opensearch", "NEXORA_OPENSEARCH_URL": "http://os"}); err != nil {
  		t.Fatalf("opensearch unchanged: %v", err)
  	}
  }
  ```
  The imports gain `maps`, `strings` and `time`. Adapt `base` to the file's existing required-variable
  helper if one exists.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/config -run TestLoadQueryLogBackends -count=1'`
      and expect FAIL: `c.ClickHouse undefined`.
- [ ] Implement in `config.go`:
  - The two types and fields.
  - Loading with `get` defaults: `NEXORA_CLICKHOUSE_URL`, `NEXORA_CLICKHOUSE_DATABASE` (`nexora`),
    `NEXORA_CLICKHOUSE_TABLE` (`querylog`), `NEXORA_CLICKHOUSE_USERNAME` (`default`),
    `NEXORA_CLICKHOUSE_PASSWORD_FILE`, `NEXORA_LOKI_URL`,
    `NEXORA_LOKI_SELECTOR` (`{service_name="nexora-engine"}`), `NEXORA_LOKI_TENANT`,
    `NEXORA_LOKI_USERNAME`, `NEXORA_LOKI_PASSWORD_FILE` and `NEXORA_LOKI_LOOKBACK` (`168h`, parsed
    with `time.ParseDuration`, valid from 1 h to 721 h).
  - The backend switch gains `clickhouse` and `loki` cases with the required-URL errors.
  - The selector check is `strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}")` after
    `strings.TrimSpace`.
  - The default error becomes `NEXORA_QUERYLOG_BACKEND must be builtin, opensearch, clickhouse or loki, got %q`.
  - Trailing `/` is trimmed from both URLs.
- [ ] Run the command from step 2 and expect PASS. Then run
      `scripts/dev-exec.sh 'go test ./mgmt/internal/config -count=1'` and expect PASS (the existing
      tests still pass; update an assertion only if it pinned the old default error text, and say so
      in the report).
- [ ] Edit `docs/architecture.md`:
  - **Management plane environment list:** after `NEXORA_OPENSEARCH_PASSWORD_FILE`, add
    `NEXORA_QUERYLOG_BACKEND` (`builtin` | `opensearch` | `clickhouse` | `loki`) and every [S-8]
    variable with its default.
  - **After the OpenSearch paragraph:** add an M10 paragraph covering:
    - ClickHouse through the collector `clickhouse` exporter into the Nexora-owned
      `deploy/clickhouse/querylog.sql` table (`create_schema: false`, `RowID` tiebreaker,
      materialized columns, 7-day TTL), read over HTTP with typed parameters as `nexora_reader`;
    - Loki through `otlphttp` to `<loki>/otlp` with the `transform/loki` body, structured metadata
      names with `_`, the tiebreak key cursor, the two-step Top and its 37-partition series-limit
      fallback;
    - `querylogtest` as the conformance contract with builtin as the reference.
  - **End-to-end harness section:** name the `clickhouse` and `loki` toolbox binaries.
  - **Deployment on kw section:** name `clickhouse.yaml` (`nexora_writer` / `nexora_reader`, Secret
    `nexora-clickhouse`), the collector's three log pipelines, the shared Loki
    `loki.monitoring.svc:3100`, and that the kw management plane stays on OpenSearch.
- [ ] Run `scripts/pc-format.sh docs/architecture.md`.
- [ ] Report the paths. Commit message: `config: clickhouse and loki query-log settings`.

## Task 3: ClickHouse and Loki binaries in the dev toolbox

Files:

- `deploy/dev/Dockerfile`: `CLICKHOUSE_VERSION` and `LOKI_VERSION` ARGs and downloads.
- `deploy/dev/dev-pod.yaml`: the new toolbox image tag.
- `scripts/dev-selftest.sh`: pinned-version checks.

Interfaces (consumed by Tasks 5, 6 and 8): `clickhouse` (the multi-call binary: `clickhouse server`,
`clickhouse client`) and `loki` on `PATH` in the toolbox.

- [ ] Add to `scripts/dev-selftest.sh`, before the `cargo fuzz` check:
  ```bash
  check clickhouse --version
  clickhouse --version 2>/dev/null | grep -q '26\.8\.4\.11' || {
  	echo "WRONG or MISSING clickhouse (want 26.8.4.11)"
  	fail=1
  }
  check loki --version
  loki --version 2>/dev/null | grep -q 'version 3\.6\.7' || {
  	echo "WRONG or MISSING loki (want 3.6.7)"
  	fail=1
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'bash scripts/dev-selftest.sh'` in the current toolbox and expect exit 1
      with `WRONG or MISSING clickhouse` and `WRONG or MISSING loki` (the red step).
- [ ] Edit `deploy/dev/Dockerfile`:
  - Add `ARG CLICKHOUSE_VERSION=26.8.4.11` and `ARG LOKI_VERSION=3.6.7`.
  - Add `unzip` to the apt list.
  - Append to the download `RUN`:
    ```dockerfile
     && curl -fsSL https://github.com/ClickHouse/ClickHouse/releases/download/v${CLICKHOUSE_VERSION}-lts/clickhouse-common-static-${CLICKHOUSE_VERSION}-arm64.tgz \
          | tar -xz --strip-components=3 -C /usr/local/bin clickhouse-common-static-${CLICKHOUSE_VERSION}/usr/bin/clickhouse \
     && curl -fsSL -o /tmp/loki.zip https://github.com/grafana/loki/releases/download/v${LOKI_VERSION}/loki-linux-arm64.zip \
     && unzip -p /tmp/loki.zip loki-linux-arm64 > /usr/local/bin/loki && chmod +x /usr/local/bin/loki && rm /tmp/loki.zip
    ```
  - If the ClickHouse release tag has no `-lts` suffix, use the tag listed at
    `https://github.com/ClickHouse/ClickHouse/releases?q=26.8.4.11`, and record the URL in the report.
- [ ] Find the next unused tag:
      `curl -sk https://192.168.10.131/v2/azrtydxb/nexora-dev/tags/list | jq -r '.tags[]' | grep '^toolbox-' | sort -V | tail -1`.
      Then build and push with kw BuildKit, following `azrtydxb/internal-lab` `scripts/dev-build.sh`
      (context `deploy/dev`, platform `linux/arm64`, output
      `192.168.10.131:5000/azrtydxb/nexora-dev:toolbox-<N>`). Expect the push to succeed.
- [ ] Set `image: 192.168.10.131/azrtydxb/nexora-dev:toolbox-<N>` in `deploy/dev/dev-pod.yaml`.
      Apply it with `kubectl --context kw apply -f deploy/dev/dev-pod.yaml`. For every other
      deployment listed by `kubectl --context kw -n nexora-dev get deploy -o name | grep toolbox-`,
      run `kubectl --context kw -n nexora-dev set image deploy/<name> toolbox=192.168.10.131/azrtydxb/nexora-dev:toolbox-<N>`.
      Wait with `kubectl --context kw -n nexora-dev rollout status deploy/toolbox --timeout=10m`.
- [ ] Run `scripts/dev-exec.sh 'bash scripts/dev-selftest.sh'` and expect exit 0 with
      `ok: clickhouse --version` and `ok: loki --version` lines. Then run
      `scripts/dev-exec.sh 'env PATH=/usr/bin:/bin bash scripts/dev-selftest.sh; echo exit=$?'` and
      expect `WRONG or MISSING clickhouse` and `exit=1` (the negative branch).
- [ ] Run `scripts/pc-format.sh deploy/dev/dev-pod.yaml`.
- [ ] Report the paths and the image tag. Commit message: `dev: clickhouse and loki in the toolbox image`.

## Task 4: Harness collector exporters and management options

Files:

- `e2e/harness/otelcol.go`: `OtelcolConfig` fields and template for OpenSearch index, ClickHouse and
  Loki.
- `e2e/harness/otelcol_test.go`: created. `TestOtelcolConfigValidates`.
- `e2e/harness/mgmt.go`: `MgmtOptions` ClickHouse and Loki environment.

Interfaces (consumed by Tasks 5, 6, 7 and 8):

```go
type OtelcolConfig struct {
	OpenSearchURL, JaegerOTLP string
	DebugFile                 string
	OpenSearchIndex           string // default "nexora-querylog-v2"
	ClickHouseNative          string // host:port of the native protocol; "" omits the exporter
	ClickHouseDatabase        string // default "nexora"
	ClickHouseTable           string // default "querylog"
	ClickHouseUser            string
	ClickHousePassword        string
	LokiURL                   string // base URL, e.g. http://127.0.0.1:3100; "" omits the exporter
}

// RenderOtelcolConfig renders the collector config for cfg with the OTLP gRPC receiver on grpc.
func RenderOtelcolConfig(cfg OtelcolConfig, grpc string) ([]byte, error)

type MgmtOptions struct {
	// existing fields ...
	ClickHouseURL, ClickHousePasswordFile, LokiURL string
}
```

- [ ] Create `e2e/harness/otelcol_test.go`:
  ```go
  package harness

  import (
  	"os"
  	"os/exec"
  	"path/filepath"
  	"strings"
  	"testing"
  )

  func TestOtelcolConfigValidates(t *testing.T) {
  	cfg := OtelcolConfig{OpenSearchURL: "http://127.0.0.1:9200", OpenSearchIndex: "nexora-conformance-1",
  		ClickHouseNative: "127.0.0.1:9000", ClickHouseUser: "nexora_writer", ClickHousePassword: "pw",
  		LokiURL: "http://127.0.0.1:3100"}
  	raw, err := RenderOtelcolConfig(cfg, "127.0.0.1:4317")
  	if err != nil {
  		t.Fatal(err)
  	}
  	s := string(raw)
  	for _, want := range []string{
  		`logs_index: "nexora-conformance-1"`,
  		"clickhouse:", `endpoint: "tcp://127.0.0.1:9000"`, "create_schema: false", `logs_table_name: "querylog"`, `database: "nexora"`,
  		"logs/clickhouse:", "otlphttp/loki:", `endpoint: "http://127.0.0.1:3100/otlp"`, "transform/loki:", "logs/loki:",
  		`set(log.body, Concat([log.attributes["client.address"], log.attributes["dns.question.name"], log.attributes["dns.question.type"], log.attributes["nexora.transport"], log.attributes["nexora.engine.id"]], " "))`,
  	} {
  		if !strings.Contains(s, want) {
  			t.Errorf("config lacks %s:\n%s", want, s)
  		}
  	}
  	p := filepath.Join(t.TempDir(), "c.yaml")
  	if err := os.WriteFile(p, raw, 0o600); err != nil {
  		t.Fatal(err)
  	}
  	if out, err := exec.Command("otelcol-contrib", "validate", "--config", p).CombinedOutput(); err != nil {
  		t.Fatalf("otelcol-contrib validate: %v\n%s", err, out)
  	}
  	bare, _ := RenderOtelcolConfig(OtelcolConfig{}, "127.0.0.1:4317")
  	if strings.Contains(string(bare), "clickhouse") || strings.Contains(string(bare), "loki") {
  		t.Fatalf("exporters must be omitted when unset:\n%s", bare)
  	}
  }
  ```
  Use the OTTL context form (`log.body` / `log.attributes`, or `body` / `attributes` under
  `context: log`) that `otelcol-contrib validate` 0.160.0 accepts. If the second form is required,
  change the expected string in the test first, keeping the same five attributes in the same order.
- [ ] Run `scripts/dev-exec.sh 'go test ./e2e/harness -run TestOtelcolConfigValidates -count=1'` and expect
      FAIL: `undefined: RenderOtelcolConfig`.
- [ ] Implement in `otelcol.go`:
  - Move the template execution into `RenderOtelcolConfig`, and call it from `StartOtelcol`.
  - Defaults: `OpenSearchIndex` `nexora-querylog-v2`, `ClickHouseDatabase` `nexora`,
    `ClickHouseTable` `querylog`.
  - Template additions under `exporters:`:
    ```yaml
    {{- if .ClickHouseNative}}
      clickhouse:
        endpoint: "tcp://{{.ClickHouseNative}}"
        database: "{{.ClickHouseDatabase}}"
        logs_table_name: "{{.ClickHouseTable}}"
        username: "{{.ClickHouseUser}}"
        password: "{{.ClickHousePassword}}"
        create_schema: false
        timeout: 5s
        retry_on_failure: { enabled: true, initial_interval: 1s, max_interval: 5s, max_elapsed_time: 60s }
    {{- end}}
    {{- if .LokiURL}}
      otlphttp/loki:
        endpoint: "{{.LokiURL}}/otlp"
        tls: { insecure: true }
    {{- end}}
    ```
  - `processors:` gets `transform/loki` when `LokiURL` is set, with one `context: log` statement
    setting the body as asserted in the test.
  - `service.pipelines` gets `logs/clickhouse: { receivers: [otlp], processors: [batch], exporters: [clickhouse] }`
    and `logs/loki: { receivers: [otlp], processors: [batch, transform/loki], exporters: [otlphttp/loki] }`.
  - `logs_index` uses `{{.OpenSearchIndex}}`.
- [ ] In `mgmt.go`, after the OpenSearch env line, add:
  - `NEXORA_CLICKHOUSE_URL` when `ClickHouseURL` is set, with `NEXORA_CLICKHOUSE_USERNAME=nexora_reader`
    and `NEXORA_CLICKHOUSE_PASSWORD_FILE` when that file is set;
  - `NEXORA_LOKI_URL` when `LokiURL` is set.
- [ ] Run the command from step 2 and expect PASS. Then run
      `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run "TestQueryLogBackends/builtin" -count=1 -timeout 20m'`
      and expect PASS (the collector template refactor and management options do not change
      existing flows).
- [ ] Report the paths. Commit message: `e2e harness: collector exporters for clickhouse and loki`.

## Task 5: ClickHouse backend

Files:

- `deploy/clickhouse/querylog.sql`: created. The idempotent DDL.
- `mgmt/internal/querylog/clickhouse.go`: created. The adapter (`Search`, `Top`).
- `mgmt/internal/querylog/clickhouse_test.go`: created. Unit tests.
- `e2e/harness/clickhouse.go`: created. `StartClickHouse`.
- `e2e/querylog_conformance_clickhouse_test.go`: created. `TestQueryLogConformanceClickHouse`.

Interfaces (consumed by Tasks 8 and 10):

```go
package querylog
type ClickHouse struct { /* unexported */ }
func NewClickHouse(cfg config.ClickHouseConfig) (*ClickHouse, error)
func (*ClickHouse) Name() string // "clickhouse"
func (*ClickHouse) Search(ctx context.Context, q Query) (Page, error)
func (*ClickHouse) Top(ctx context.Context, q TopQuery) ([]TopEntry, error)
var _ Topper = (*ClickHouse)(nil)

package harness
type ClickHouse struct {
	HTTPURL, NativeAddr, Database, Table   string
	WriterUser, WriterPassword, ReaderUser string
	ReaderPasswordFile                     string
	Proc                                   *Proc
}
func (e *Env) StartClickHouse() *ClickHouse // applies deploy/clickhouse/querylog.sql twice
```

- [ ] Create `mgmt/internal/querylog/clickhouse_test.go` with these tests:
  - **`TestClickHouseQueryIsParameterised`:**
    - An `httptest` server records `r.URL.Query()` and the SQL body, and answers
      `X-ClickHouse-Format: JSONEachRow` with two rows:
      `{"t":"1757844000123456789","rowid":"0b0c...","name":"a.test.","client":"10.0.0.1","qtype":"A","rcode":"NOERROR","cache":"miss","filter":"none","list_id":"","category":"","source":"","rule":"","policy_group":"","rpz_zone":"","rpz_action":"","acl_refused":"","upstreams_raced":0,"upstream":"fx","transport":"udp","engine_id":"e1","duration_us":77}`.
    - Search with `Name: "a%b_c\\'); DROP"`, `QTypes {A, AAAA}`, `PolicyGroups {global, g1}` and
      `Limit: 1`.
    - Assert:
      - the SQL body contains `{name:String}`, `QType IN {qtypes:Array(String)}` and
        `ORDER BY Timestamp DESC, RowID ASC`;
      - the SQL does not contain `DROP` or `a%b`;
      - `param_name == "%a\\%b\\_c\\\\'); drop%"`;
      - `param_qtypes == "['A','AAAA']"` (ClickHouse array literal with `'` and `\` escaped);
      - `param_policy_groups` contains `''` and `'g1'`;
      - `database=nexora` and HTTP basic auth or the `X-ClickHouse-User` header with the configured
        username;
      - the page has 1 record with `Time.UnixNano() == 1757844000123456789` and `DurationUS == 77`,
        and a non-empty `NextCursor` (limit+1 rows came back).
  - **`TestClickHouseCursorKeyset`:**
    - The decoded `NextCursor` is `[1757844000123456789,"0b0c..."]`.
    - A follow-up Search sends `param_cursor_t` and `param_cursor_id` and the SQL
      `(Timestamp < fromUnixTimestamp64Nano({cursor_t:Int64}) OR (Timestamp = fromUnixTimestamp64Nano({cursor_t:Int64}) AND RowID > {cursor_id:UUID}))`.
    - The cursors `"x"`, a base64 `[1]` and a base64 `["a","b"]` each give `ErrInvalidCursor`
      without any HTTP request (the server counts requests).
  - **`TestClickHouseTopSQL`:** Top `Field "category"`, `Filters {blocked}`, `Limit 5` → the SQL
    contains `Category AS key`, `key != ''`, `Filter IN {filters:Array(String)}`,
    `ORDER BY c DESC, key ASC` and `LIMIT {limit:UInt32}`. A response `{"key":"ads","c":"3"}` decodes
    to `TopEntry{Key: "ads", Count: 3}`. `Field "bogus"` is an error without a request.
  - **`TestClickHouseErrors`:**
    - A closed server, a 503 and a 429 each give `errors.Is(err, ErrBackendUnavailable)`.
    - A 404 with header `X-ClickHouse-Exception-Code: 60` gives an error that is not
      `ErrBackendUnavailable` and whose text contains `apply deploy/clickhouse/querylog.sql`.
    - A 403 with code 516 names `NEXORA_CLICKHOUSE_PASSWORD_FILE`.
    - A 200 success is checked first (positive path).
  - **`TestClickHouseSchemaHasAdapterColumns`:** read `../../../deploy/clickhouse/querylog.sql` and
    extract column names with the regex ``(?m)^\s*`?([A-Za-z_][A-Za-z0-9_.]*)`?\s+[A-Z]``. Assert
    that it contains each of `Timestamp`, `TraceId`, `SpanId`, `TraceFlags`, `SeverityText`,
    `SeverityNumber`, `ServiceName`, `Body`, `ResourceSchemaUrl`, `ResourceAttributes`,
    `ScopeSchemaUrl`, `ScopeName`, `ScopeVersion`, `ScopeAttributes`, `LogAttributes`, `EventName`,
    `RowID`, and each name in the adapter's exported-for-test `querylog.ClickHouseColumns()`. Also
    assert it contains `create_schema` nowhere, `IF NOT EXISTS` twice, `TTL toDateTime(Timestamp) + INTERVAL 7 DAY`
    and `ngrambf_v1`.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/querylog -run "TestClickHouse" -count=1'` and expect
      FAIL: `undefined: querylog.NewClickHouse`.
- [ ] Create `deploy/clickhouse/querylog.sql` following the spec's Data section exactly:
  - `CREATE DATABASE IF NOT EXISTS nexora;`
  - `CREATE TABLE IF NOT EXISTS nexora.querylog (...)`, where the first 16 columns copy the v0.160.0
    exporter types and codecs from
    `https://raw.githubusercontent.com/open-telemetry/opentelemetry-collector-contrib/v0.160.0/exporter/clickhouseexporter/internal/sqltemplates/logs_table.sql`,
    without the `__otel_materialized_*` columns and without that file's indexes. Then add:
    - `RowID UUID DEFAULT generateUUIDv4()`;
    - the materialized columns of the spec table, for example
      `` `Filter` LowCardinality(String) MATERIALIZED if(LogAttributes['nexora.filter'] != '', LogAttributes['nexora.filter'], LogAttributes['nexora.filter.result']) ``,
      `` `EngineID` String MATERIALIZED if(LogAttributes['nexora.engine.id'] != '', LogAttributes['nexora.engine.id'], ResourceAttributes['nexora.engine.id']) ``
      and `` `RPZAction` LowCardinality(String) MATERIALIZED if(LogAttributes['nexora.rpz'] = 'none', '', LogAttributes['nexora.rpz']) ``;
    - the two indexes;
    - `ENGINE = MergeTree PARTITION BY toDate(Timestamp) ORDER BY (toStartOfFiveMinutes(Timestamp), ServiceName, Timestamp) TTL toDateTime(Timestamp) + INTERVAL 7 DAY SETTINGS index_granularity = 8192, ttl_only_drop_parts = 1;`
  - Header comment: the file is applied by `scripts/kw-deploy.sh` and by the e2e harness, and must
    stay idempotent.
- [ ] Implement `mgmt/internal/querylog/clickhouse.go`:
  - **`NewClickHouse`** reads the password file (`NEXORA_CLICKHOUSE_PASSWORD_FILE: %w`), builds
    `http.Client{Timeout: clickHouseTimeout}`, and keeps the URL, database, table (validated against
    `^[A-Za-z_][A-Za-z0-9_]*$`, else an error) and user.
  - **`do(ctx, sql string, params map[string]string, out func(dec *json.Decoder) error)`:**
    - POSTs the SQL as the body to `URL/?database=<db>&default_format=JSONEachRow&param_<k>=<v>...`,
      with `X-ClickHouse-User`, and `X-ClickHouse-Key` when a password is set.
    - A transport error, a status ≥ 500 or a 429 gives `fmt.Errorf("%w: ...", ErrBackendUnavailable)`.
    - Another non-200 status gives an error with the first 512 bytes of the body. Exception code 60
      or 81 adds `(apply deploy/clickhouse/querylog.sql)`, and 516 adds
      `(check NEXORA_CLICKHOUSE_PASSWORD_FILE)`.
  - **`arrayParam([]string) string`** builds `['a','b']`, escaping `\` as `\\` and `'` as `\'`.
  - **`likeParam(fragment)`** does `"%" + escape(strings.ToLower(strings.TrimSuffix(f, "."))) + "%"`,
    with escape replacing `\`→`\\`, `%`→`\%` and `_`→`\_`.
  - **`where(q Query)`** builds clauses and parameters:
    - `Timestamp >= fromUnixTimestamp64Nano({from:Int64})` and `<= {to}` for non-zero bounds;
    - `Client = {client:String}` when `Client` is set;
    - `lower(Name) LIKE {name:String}`;
    - one `IN` per non-empty slice, on `QType`, `RCode`, `Cache`, `Filter`, `Category`, `Source`,
      `ListID`, `EngineID` and `PolicyGroupID` (with `global` mapped to `''`).
  - **Search** selects `toString(toUnixTimestamp64Nano(Timestamp)) AS t, toString(RowID) AS rowid`
    and the typed columns under JSON names, with `ORDER BY Timestamp DESC, RowID ASC LIMIT {limit:UInt32}`
    at limit+1. `NextCursor` is set from the last kept row when limit+1 rows arrived.
  - **Top** uses the `key` mapping `name→Name`, `client→Client`, `category→Category`, and
    `count()` output as `toString(count())`, parsed with `strconv.ParseInt`. `TopQuery.Limit <= 0`
    uses 10.
  - `func ClickHouseColumns() []string` returns every column name the adapter references.
- [ ] Create `e2e/harness/clickhouse.go`. `StartClickHouse`:
  1. Fail with `clickhouse not found: rebuild the toolbox image (deploy/dev/Dockerfile)` when
     `exec.LookPath("clickhouse")` fails.
  2. Write a `config.xml` into a temp dir: `http_port` and `tcp_port` from `e.FreePort()`,
     `listen_host 127.0.0.1`, `path`/`tmp_path`/`user_files_path` under the dir, logger to
     `<dir>/clickhouse.log` at level `warning`, `mark_cache_size 268435456`.
  3. Write `users.d/nexora.xml`:
     - `default` with no password and `<networks><ip>127.0.0.1</ip></networks>`;
     - `nexora_writer` with password `writer-e2e`;
     - `nexora_reader` with a random password, which is written to `<dir>/reader-password`.
  4. Start `clickhouse server --config-file=<dir>/config.xml` through `e.Start`, and wait until
     `GET http://127.0.0.1:<http>/ping` returns `Ok.` within 30 s.
  5. Apply `deploy/clickhouse/querylog.sql`, located with `runtime.Caller` relative to the repo root,
     twice with `clickhouse client --port <tcp> --multiquery --queries-file <sql>`. Fail on a non-zero
     exit, with the output.
  6. Run `GRANT INSERT ON nexora.querylog TO nexora_writer` and
     `GRANT SELECT ON nexora.querylog TO nexora_reader` as `default`, with `access_management` enabled
     for `default` in `users.d`.
- [ ] Create `e2e/querylog_conformance_clickhouse_test.go`:
  ```go
  func TestQueryLogConformanceClickHouse(t *testing.T) {
  	env := harness.New(t)
  	ch := env.StartClickHouse()
  	col := env.StartOtelcol(harness.OtelcolConfig{ClickHouseNative: ch.NativeAddr, ClickHouseUser: ch.WriterUser, ClickHousePassword: ch.WriterPassword, DebugFile: env.Dir + "/otel.jsonl"})
  	backend, err := querylog.NewClickHouse(config.ClickHouseConfig{URL: ch.HTTPURL, Database: "nexora", Table: "querylog", Username: ch.ReaderUser, PasswordFile: ch.ReaderPasswordFile})
  	if err != nil {
  		t.Fatal(err)
  	}
  	run := harness.UniqueName("conf")
  	base := time.Now().UTC().Truncate(time.Millisecond).Add(-5 * time.Minute)
  	querylogtest.Run(t, strings.TrimSuffix(run, "."), base, querylogtest.Harness{
  		Backend: backend,
  		Ingest: func(t *testing.T, _ string, req *collogspb.ExportLogsServiceRequest) {
  			querylogtest.OTLPExport(t, col.OTLPGRPC, []querylogtest.EngineBatch{{Req: req}})
  		},
  		Visible: querylogtest.PollVisible(backend, base),
  	})
  }
  ```
- [ ] Run the step-2 command and expect PASS. Then run
      `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestQueryLogConformanceClickHouse -count=1 -v -timeout 20m'`
      and expect PASS for every subtest listed in Task 1. On a divergence, fix the adapter or the SQL,
      never the suite.
- [ ] Add the comment
      `// debt: materialized columns cover the post-M6 attributes only; a new engine attribute needs an ALTER TABLE ADD COLUMN in querylog.sql and an upgrade note`
      above the column list in `clickhouse.go`.
- [ ] Report the paths. Commit message: `querylog: clickhouse backend`.

## Task 6: Loki backend

Files:

- `mgmt/internal/querylog/loki.go`: created. The adapter (`Search`, `Top`, HTTP).
- `mgmt/internal/querylog/loki_logql.go`: created. Quoting, filter stages, partitions and tiebreak
  key.
- `mgmt/internal/querylog/loki_test.go`: created. Unit tests.
- `e2e/harness/loki.go`: created. `StartLoki`.
- `e2e/querylog_conformance_loki_test.go`: created. `TestQueryLogConformanceLoki`.

Interfaces (consumed by Tasks 8 and 10):

```go
package querylog
type Loki struct { /* unexported */ }
func NewLoki(cfg config.LokiConfig) (*Loki, error)
func (*Loki) Name() string // "loki"
func (*Loki) Search(ctx context.Context, q Query) (Page, error)
func (*Loki) Top(ctx context.Context, q TopQuery) ([]TopEntry, error)
func LokiPartitionRegexes() []string // 37 regexes; index 36 is "none"
var _ Topper = (*Loki)(nil)

package harness
type LokiOptions struct{ MaxQuerySeries int } // 0 = kw default 500
type Loki struct{ URL string; Proc *Proc }
func (e *Env) StartLoki(o LokiOptions) *Loki
```

- [ ] Create `mgmt/internal/querylog/loki_test.go`. A fake Loki (`httptest`) records each request's
      path, `query`, `start`, `end`, `time`, `limit`, `direction` and headers. It answers from
      closures per test in the `categorize-labels` shape:
      `{"status":"success","data":{"resultType":"streams","result":[{"stream":{"service_name":"nexora-engine"},"values":[["<ns>","<line>",{"structuredMetadata":{"dns_question_name":"a.test.",...}}]]}],"encodingFlags":["categorize-labels"]}}`.
      For metric queries it uses `resultType: "vector"`. The tests:
  - **`TestLokiLogQLQuotesValues`:**
    - Search with `Name "A(b).c."`, `QTypes {A, "AA|AA"}`, `PolicyGroups {global, g1}`,
      `Filters {blocked}`, `Client "10.0.0.1"`, `From`/`To` set, and tenant `t1`.
    - Assert the query contains:
      - `{service_name="nexora-engine"}`;
      - `dns_question_name=~"(?i)^.*a\\(b\\)\\.c.*$"` (fragment lowercased is not required, `(?i)`
        does it; the trailing dot is removed and the metacharacters quoted, then Go-quoted);
      - `dns_question_type=~"^(?:A|AA\\|AA)$"`;
      - `nexora_policy_group=~"^(?:|g1)$"`;
      - `(nexora_filter=~"^(?:blocked)$" or nexora_filter_result=~"^(?:blocked)$")`;
      - `client_address="10.0.0.1"`.
    - Assert `start == From.UnixNano()`, `end == To.UnixNano()+1`, `direction == backward`, and the
      headers `X-Scope-OrgID: t1` and `X-Loki-Response-Encoding-Flags: categorize-labels`.
    - Assert that a zero From gives `start == To − 168h`.
  - **`TestLokiPagingTiesAtOneNanosecond`:**
    - The fake holds 5 entries at one ns `T` and 2 older entries. It honours `limit` and `end`
      (exclusive) by returning the newest `limit` entries with `ts < end`, sorted by ts desc and
      then insertion order, so a limit-sized response cuts the tie group.
    - Page with `Limit 2` until `NextCursor == ""`.
    - Assert 7 distinct records, no duplicate, and within `T` ascending tiebreak keys.
    - Assert that at least one request asked for a limit above 3 (the growth rule).
    - Assert that 5,001 entries at one ns give `ErrBackendUnavailable` and the text
      `more than 5000 entries share one timestamp`.
  - **`TestLokiTopTwoStepTies`:**
    - The fake answers `topk(2, ...)` with `[{x:3},{c:4}]`, and the `>= 3` query with
      `[{b:3},{x:3},{c:4}]`.
    - Top `Limit 2` returns `c:4, b:3`.
    - The first query contains `topk(2, sum by (dns_question_name) (count_over_time(`,
      `dns_question_name!=""` and `[<ns>ns]` with `ns == To−From+1`, and `time == To.UnixNano()`.
    - The second query ends in `) >= 3`.
    - An empty step-1 vector gives `[]` and no second request.
  - **`TestLokiTopPartitionFallback`:**
    - The fake answers HTTP 400 `maximum of series (500) reached for a single query` to any query
      without a `=~"(?i)^` partition matcher on the key label.
    - For partitioned queries it computes the answers from an in-memory map of 200 names with
      counts `1 + i%7`. It tracks the maximum number of in-flight requests.
    - Top `Limit 10` equals the expectation computed directly from the map: count desc, key asc,
      first 10.
    - The maximum in-flight count is ≤ 6, and exactly 37 step-1 partition queries were sent.
    - A fake that also rejects partitioned queries gives `ErrBackendUnavailable` with
      `loki series limit: raise max_query_series`.
  - **`TestLokiPartitionsCoverEveryKey`:**
    - Compile `LokiPartitionRegexes()` with Go `regexp`.
    - For 10,000 keys from `math/rand/v2` with a fixed seed (lengths 0–20 over
      `[0-9a-zA-Z.:\-_é]`), plus `""`, `"."`, `"a."`, `"A"`, `"::1"`, `"fe80::a"`, `"x-"` and `"é"`,
      assert exactly one regex matches.
    - The positive path first: `"a.test."` matches index 20 (`t`).
  - **`TestLokiErrors`:**
    - The positive path first: a 200 success.
    - A closed server, a 503 and a 429 each give `ErrBackendUnavailable`.
    - A 400 `parse error at line 1` is a plain error containing `parse error`.
    - An invalid cursor gives `ErrInvalidCursor` with no request.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/querylog -run "TestLoki" -count=1'` and expect FAIL:
      `undefined: querylog.NewLoki`.
- [ ] Implement `loki_logql.go`:
  - **`lokiQuote(s) string`** is `strconv.Quote(s)`.
  - **`lokiAny(values []string) string`** is `"^(?:" + strings.Join(QuoteMeta each, "|") + ")$"`.
  - **`lokiLabel`** maps Record fields to structured metadata names (the spec Data list).
  - **`lokiStages(q Query) string`** builds `| label="value"` for `Client` and `| label=~<quoted regex>`
    for `Name` and each slice. `Filters` becomes `| (nexora_filter=~R or nexora_filter_result=~R)`,
    and `PolicyGroups` maps `global` to `""`.
  - **`LokiPartitionRegexes()`** returns the regexes from the Architecture decision.
  - **`lokiKey(line string, md map[string]string) string`** follows the Architecture decision.
  - **`recordFromLoki(ts int64, line string, md map[string]string) Record`** takes `Filter` from
    `nexora_filter`, else `nexora_filter_result`. `RPZAction` maps `none` to `""`, and
    `UpstreamsRaced` and `DurationUS` go through `strconv.ParseInt` with 0 on error.
- [ ] Implement `loki.go`:
  - **`NewLoki`** reads the password file (`NEXORA_LOKI_PASSWORD_FILE: %w`) and uses an
    `http.Client` without a global timeout (contexts carry them).
  - **`get(ctx, path, params) (*lokiResponse, error)`** sends basic auth when a username is set,
    `X-Scope-OrgID` when a tenant is set, and `X-Loki-Response-Encoding-Flags: categorize-labels`.
    - A transport error, a status ≥ 500 or a 429 gives `ErrBackendUnavailable`.
    - A 400 whose body contains `maximum of series` gives the unexported `errLokiSeriesLimit`.
    - Another non-200 status is a plain error with the body's first 512 bytes.
  - **Search** runs inside `context.WithTimeout(ctx, lokiSearchTimeout)`:
    1. Decode the cursor (`[string ns, string key]`, where the ns string parses as int64), else
       `ErrInvalidCursor`.
    2. Set `end` to `To+1` (with To zero meaning now), or to `cursorNs+1`.
    3. Loop with `n := limit + 1`. Query `/loki/api/v1/query_range` with `limit=n` and
       `direction=backward`. Flatten the streams into entries `(ns, key, record)`, and drop entries
       with `ns > cursorNs`, or `ns == cursorNs && key <= cursorKey`.
    4. If the response holds `n` raw entries, drop the whole group at the oldest raw ns
       (incomplete).
    5. Sort by ns desc, then key asc. If the kept entries number fewer than `limit+1` and the
       response was full, set `n = min(2n, 5000)` and repeat. When `n` is already 5000 and a single
       ns filled the response, return
       `fmt.Errorf("%w: more than 5000 entries share one timestamp", ErrBackendUnavailable)` with the
       comment
       `// debt: 5000 entries in one nanosecond is unreachable at microsecond engine timestamps; revisit if Loki raises max_entries_limit_per_query handling`.
    6. The page is the first `limit`. `NextCursor` is `[ns, key]` of the last kept entry when a
       `limit+1`-th exists.
  - **Top** runs inside `context.WithTimeout(ctx, lokiTopTimeout)`:
    - `metric(stages, extra string) string` gives
      `sum by (L) (count_over_time(<selector> <stages> | L!="" <extra> [<ns>ns]))`.
    - Step 1 runs `topk(k, metric)` on `/loki/api/v1/query` with `time=To`, and `c` is the minimum
      value of the result.
    - Step 2 runs `metric >= c`.
    - On `errLokiSeriesLimit`, run step 1 for each of the 37 partitions (the extra stage
      `| L=~"<partition regex>"`) through a semaphore of 6, take the k-th largest count across all
      partition results as `c`, then run step 2 per partition and merge.
    - A partition error `errLokiSeriesLimit` gives
      `fmt.Errorf("%w: loki series limit: raise max_query_series", ErrBackendUnavailable)`.
    - Sort by count desc, then key asc, and cut at k. Values parse as float64, then `int64`.
- [ ] Create `e2e/harness/loki.go`. `StartLoki(o)`:
  1. Fail with `loki not found: rebuild the toolbox image (deploy/dev/Dockerfile)` when absent.
  2. Write a config with:
     - `auth_enabled: false`;
     - `server.http_listen_port` and `grpc_listen_port` from `FreePort`, and
       `http_listen_address: 127.0.0.1`;
     - `common.path_prefix` in the temp dir with `storage.filesystem` and
       `ring.kvstore.store: inmemory`, `replication_factor: 1`,
       `instance_addr: 127.0.0.1`;
     - `ingester.lifecycler.min_ready_duration: 0s` and `final_sleep: 0s`;
     - `schema_config` with `from: "2024-01-01"`, `store: tsdb`, `object_store: filesystem`,
       `schema: v13` and index `prefix: index_`, `period: 24h`;
     - a `limits_config` copied from kw's Loki (`allow_structured_metadata: true`,
       `max_cache_freshness_per_query: 10m`, `query_timeout: 300s`, `reject_old_samples: true`,
       `reject_old_samples_max_age: 168h`, `retention_period: 168h`,
       `split_queries_by_interval: 15m`, `volume_enabled: true`) plus
       `max_query_series: <o.MaxQuerySeries or 500>`;
     - `query_range.align_queries_with_step: true`;
     - `pattern_ingester.enabled: false`.
  3. Start `loki -config.file=<file>` and poll `GET /ready` until the body is `ready` within 60 s.
- [ ] Create `e2e/querylog_conformance_loki_test.go`:
  - `TestQueryLogConformanceLoki` starts `StartLoki(LokiOptions{})` and
    `StartOtelcol(OtelcolConfig{LokiURL: loki.URL, DebugFile: ...})`, builds `querylog.NewLoki` with
    the default selector and lookback, and runs `querylogtest.Run` as in Task 5.
  - The subtest `t.Run("top-partitioned", ...)` starts a second env with `MaxQuerySeries: 3` and
    ingests the same dataset with a new run id. It first asserts that a raw GET to
    `/loki/api/v1/query` with
    `sum by (dns_question_name) (count_over_time({service_name="nexora-engine"}[10m]))` returns 400
    containing `maximum of series` (the positive precondition). It then asserts that `backend.Top`
    for `Field name, Limit 3` equals the builtin reference's `Top`.
- [ ] Run the step-2 command and expect PASS. Then run
      `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestQueryLogConformanceLoki -count=1 -v -timeout 25m'`
      and expect PASS for every subtest and `top-partitioned`. If Loki rejects `[<n>ns]` ranges,
      express the range in the smallest unit Loki accepts that is exact for millisecond-whole
      windows (`ms`). Keep `top-time-range-inclusive` passing, and record the change in the report.
- [ ] Add the comment
      `// debt: Loki drops entries with equal timestamp and line in one stream; the transform/loki body makes that need an identical query from one client in one microsecond; revisit if a conformance or kw count ever differs`
      above `recordFromLoki`.
- [ ] Report the paths. Commit message: `querylog: loki backend`.

## Task 7: OpenSearch conformance

Files:

- `e2e/querylog_conformance_opensearch_test.go`: created. `TestQueryLogConformanceOpenSearch`.
- `mgmt/internal/querylog/opensearch.go`: only if the suite finds a divergence.
- `mgmt/internal/querylog/opensearch_test.go`: a unit test for any fix.

Interfaces: consumes `querylogtest` (Task 1) and `OtelcolConfig.OpenSearchIndex` (Task 4). Produces
nothing new.

- [ ] Create `e2e/querylog_conformance_opensearch_test.go`:
  ```go
  func TestQueryLogConformanceOpenSearch(t *testing.T) {
  	env := harness.New(t)
  	run := strings.TrimSuffix(harness.UniqueName("conf"), ".")
  	index := "nexora-conformance-" + strings.ToLower(strings.ReplaceAll(run, ".", "-"))
  	osURL := harness.OpenSearchURL(t)
  	t.Cleanup(func() {
  		req, _ := http.NewRequest(http.MethodDelete, osURL+"/"+index+"*", nil)
  		if resp, err := http.DefaultClient.Do(req); err == nil {
  			_ = resp.Body.Close()
  		}
  	})
  	col := env.StartOtelcol(harness.OtelcolConfig{OpenSearchURL: osURL, OpenSearchIndex: index, DebugFile: env.Dir + "/otel.jsonl"})
  	backend, err := querylog.NewOpenSearch(config.OpenSearchConfig{URL: osURL, Index: index + "*"})
  	if err != nil {
  		t.Fatal(err)
  	}
  	base := time.Now().UTC().Truncate(time.Millisecond).Add(-5 * time.Minute)
  	querylogtest.Run(t, run, base, querylogtest.Harness{
  		Backend: backend,
  		Ingest: func(t *testing.T, _ string, req *collogspb.ExportLogsServiceRequest) {
  			querylogtest.OTLPExport(t, col.OTLPGRPC, []querylogtest.EngineBatch{{Req: req}})
  		},
  		Visible: querylogtest.PollVisible(backend, base),
  	})
  }
  ```
  OpenSearch indices are shared, so `top-client` and `top-category-skips-empty` compare only keys
  present in the dataset. Task 1's `Run` does that for every Top case when the index is not fresh:
  it filters both sides to the dataset's keys.
- [ ] Run
      `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run TestQueryLogConformanceOpenSearch -count=1 -v -timeout 20m'`.
      Expect PASS. If a subtest fails: 1. Write a unit test in `opensearch_test.go` that reproduces the request-body or decoding
      difference, and see it fail. 2. Fix `opensearch.go` without changing the M6 or M7 cursor format. 3. Rerun both tests and expect PASS.
- [ ] Report the paths and any divergence found. Commit message: `e2e: opensearch query-log conformance`.

## Task 8: Management plane wiring and product e2e

Files:

- `mgmt/cmd/nexora-mgmt/main.go`: `buildQueryLog` for all four backends.
- `mgmt/cmd/nexora-mgmt/querylog_test.go`: created. `TestServeBuildsQueryLogBackend`.
- `mgmt/api/openapi.yaml`: the `backend` description lists the four values.
- `mgmt/internal/api/gen.go` and `web/src/api/schema.d.ts`: regenerated.
- `e2e/querylog_backends_helper_test.go`: created. `queryLogMgmtOptions`.
- `e2e/gui_test.go`: `TestQueryLogBackends` runs four backends, plus the `dashboard-top` subtest.
- `e2e/filter_attribution_test.go`: `TestQueryLogCategoryAttribution` runs four backends.
- `e2e/harness/backends_test.go`: created. `TestHarnessStartsClickHouseAndLoki`.

Interfaces:

```go
// package main
func buildQueryLog(cfg config.Config) (backend querylog.Backend, builtin *querylog.Builtin, err error)

// package e2e
func queryLogMgmtOptions(t *testing.T, env *harness.Env, backend string) harness.MgmtOptions
```

- [ ] Create `mgmt/cmd/nexora-mgmt/querylog_test.go`:
  ```go
  package main

  import (
  	"testing"

  	"github.com/piwi3910/nexora/mgmt/internal/config"
  )

  func TestServeBuildsQueryLogBackend(t *testing.T) {
  	for _, tc := range []struct {
  		cfg     config.Config
  		name    string
  		builtin bool
  	}{
  		{config.Config{QueryLogBackend: "builtin", QueryLogBuiltinCapacity: 10}, "builtin", true},
  		{config.Config{QueryLogBackend: "opensearch", OpenSearch: config.OpenSearchConfig{URL: "http://os:9200"}}, "opensearch", false},
  		{config.Config{QueryLogBackend: "clickhouse", ClickHouse: config.ClickHouseConfig{URL: "http://ch:8123", Database: "nexora", Table: "querylog", Username: "r"}}, "clickhouse", false},
  		{config.Config{QueryLogBackend: "loki", Loki: config.LokiConfig{URL: "http://loki:3100", Selector: `{service_name="nexora-engine"}`, Lookback: 168 * 3600e9}}, "loki", false},
  	} {
  		b, bi, err := buildQueryLog(tc.cfg)
  		if err != nil || b.Name() != tc.name || (bi != nil) != tc.builtin {
  			t.Errorf("%s: %v %v %v", tc.name, b, bi, err)
  		}
  		if got := queryLogToManagement(tc.cfg); got != tc.builtin {
  			t.Errorf("%s: QueryLogToManagement = %v", tc.name, got)
  		}
  	}
  	if _, _, err := buildQueryLog(config.Config{QueryLogBackend: "clickhouse", ClickHouse: config.ClickHouseConfig{URL: "http://ch", PasswordFile: "/nonexistent"}}); err == nil {
  		t.Fatal("unreadable password file must fail")
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'make webui-placeholder && go test ./mgmt/cmd/nexora-mgmt -run TestServeBuildsQueryLogBackend -count=1'`
      and expect FAIL: `undefined: buildQueryLog`.
- [ ] In `main.go`:
  - Replace the `if cfg.QueryLogBackend == "opensearch"` block with `queryLog, builtinLog, err := buildQueryLog(cfg)`.
  - `buildQueryLog` switches over the four names and calls `NewOpenSearch`, `NewClickHouse`,
    `NewLoki` or `NewBuiltin`.
  - `func queryLogToManagement(cfg config.Config) bool { return cfg.QueryLogBackend == "builtin" }` is
    used in `snapshot.BuildConfig`.
- [ ] Run the step-2 command and expect PASS.
- [ ] In `mgmt/api/openapi.yaml`, set the query-log page `backend` property to
      `{ type: string, description: "builtin, opensearch, clickhouse or loki" }` (no enum, so older
      clients keep working). On the laptop, run `cd mgmt/api && oapi-codegen -config oapi-codegen.yaml openapi.yaml`
      and `cd web && pnpm run gen:api`, and expect only comment changes in the generated files.
- [ ] Create `e2e/harness/backends_test.go`:
  ```go
  func TestHarnessStartsClickHouseAndLoki(t *testing.T) {
  	env := New(t)
  	ch := env.StartClickHouse()
  	pw, _ := os.ReadFile(ch.ReaderPasswordFile)
  	req, _ := http.NewRequest(http.MethodPost, ch.HTTPURL+"/?query=SELECT%201", nil)
  	req.Header.Set("X-ClickHouse-User", ch.ReaderUser)
  	req.Header.Set("X-ClickHouse-Key", strings.TrimSpace(string(pw)))
  	resp, err := http.DefaultClient.Do(req)
  	if err != nil || resp.StatusCode != 200 {
  		t.Fatalf("SELECT 1 as reader: %v %v", resp, err)
  	}
  	lk := env.StartLoki(LokiOptions{})
  	r, err := http.Get(lk.URL + "/ready")
  	if err != nil || r.StatusCode != 200 {
  		t.Fatalf("loki ready: %v %v", r, err)
  	}
  	t.Setenv("PATH", t.TempDir())
  	msg := captureFatal(t, func(tb *Env) { tb.StartClickHouse() })
  	if !strings.Contains(msg, "clickhouse not found: rebuild the toolbox image") {
  		t.Fatalf("missing binary message: %q", msg)
  	}
  }
  ```
  `captureFatal` runs the function with an `Env` whose `T` is a recording `testing.TB` (a local test
  helper in this file) and returns the fatal message.
- [ ] Create `e2e/querylog_backends_helper_test.go`. `queryLogMgmtOptions(t, env, backend)` returns
      `MgmtOptions{QueryLogBackend: backend}` extended per backend:
  - **opensearch:** the existing collector and URL lines, moved here.
  - **clickhouse:** `ch := env.StartClickHouse()` and a collector with `ClickHouseNative`, the
    writer credentials and `DebugFile`. It sets `ClickHouseURL: ch.HTTPURL` and
    `ClickHousePasswordFile: ch.ReaderPasswordFile`, plus
    `OTLPEndpoint: "http://" + col.OTLPGRPC`.
  - **loki:** `lk := env.StartLoki(LokiOptions{})` and a collector with `LokiURL`. It sets
    `LokiURL: lk.URL` and the OTLP endpoint.
- [ ] In `e2e/gui_test.go` `TestQueryLogBackends`:
  - The loop becomes `[]string{"builtin", "opensearch", "clickhouse", "loki"}`.
  - The per-backend option block becomes `opts := queryLogMgmtOptions(t, env, backend)`.
  - Add, after the M6 `multi-value` subtest:
    ```go
    t.Run("dashboard-top", func(t *testing.T) {
    	n := strconv.FormatInt(time.Now().UnixNano()%1_000_000, 10)
    	for i := 0; i < 5; i++ {
    		harness.MustQuery(t, eng.DNS, "dt-"+n+".test.", dns.TypeA, harness.QueryOpts{})
    	}
    	harness.MustQuery(t, eng.DNS, "dt-other-"+n+".test.", dns.TypeA, harness.QueryOpts{})
    	harness.MustQuery(t, eng.DNS, "dt-other-"+n+".test.", dns.TypeA, harness.QueryOpts{})
    	var top struct {
    		Available bool `json:"available"`
    		Domains   []struct {
    			Key   string `json:"key"`
    			Count int64  `json:"count"`
    		} `json:"domains"`
    	}
    	harness.EventuallyTrue(t, 60*time.Second, func() bool {
    		api.Must("GET", "/dashboard/top?range=15m&limit=50", nil, &top, 200)
    		for _, d := range top.Domains {
    			if d.Key == "dt-"+n+".test." && d.Count == 5 {
    				return top.Available
    			}
    		}
    		return false
    	}, "dashboard top counts dt-<n> five times")
    })
    ```
    Use the JSON field names of the committed M6 `DashboardTop` schema (read them from
    `mgmt/api/openapi.yaml`) if they differ from `domains`, `key` and `count`.
- [ ] In `e2e/filter_attribution_test.go`, make the loop list the four backends and build options with
      `queryLogMgmtOptions`.
- [ ] Run
      `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e/harness -run "TestHarnessStartsClickHouseAndLoki|TestOtelcolConfigValidates" -count=1 && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e -run "TestQueryLogBackends|TestQueryLogCategoryAttribution" -count=1 -v -timeout 60m'`.
      Expect PASS for `builtin`, `opensearch`, `clickhouse` and `loki`, each with the Playwright run,
      `partial-name`, `multi-value` and `dashboard-top`, and the attribution subtests.
- [ ] Run `scripts/dev-exec.sh 'make webui-placeholder && go test ./mgmt/... -count=1'` and expect PASS.
- [ ] Report the paths. Commit message: `mgmt: clickhouse and loki query-log backends wired end to end`.

## Task 9: Helm chart

Files:

- `deploy/helm/nexora/values.yaml`: `mgmt.querylog.clickhouse` and `mgmt.querylog.loki`.
- `deploy/helm/nexora/values.schema.json`: backend enum and the new objects.
- `deploy/helm/nexora/templates/mgmt-deployment.yaml`: env, Secret volume and mount.
- `deploy/helm/nexora/ci/lint-values.yaml`: unchanged unless lint requires it.
- `deploy/deploytest/helm_querylog_test.go`: created. `TestHelmQueryLogBackends`.
- The M9 operator CRD file, only when `grep -rln 'opensearch' deploy/operator config 2>/dev/null`
  finds a CRD or type that enumerates query-log backends.

Interfaces: consumes the variable names from Task 2. Produces the values keys of spec [S-9].

- [ ] Create `deploy/deploytest/helm_querylog_test.go`:
  ```go
  func TestHelmQueryLogBackends(t *testing.T) {
  	common := []string{"--set", "image.tag=sha-0000000", "--set", "mgmt.ca.existingSecret=ca", "--set-json", `engine.groups=[{"name":"default","joinTokenSecret":"jt"}]`}
  	ch := render(t, append(common, "--set", "mgmt.querylog.backend=clickhouse",
  		"--set", "mgmt.querylog.clickhouse.url=http://clickhouse.nexora.svc:8123", "--set", "mgmt.querylog.clickhouse.username=nexora_reader",
  		"--set", "mgmt.querylog.clickhouse.passwordSecret.name=nexora-clickhouse", "--set", "mgmt.querylog.clickhouse.passwordSecret.key=reader-password")...)
  	mgmt := find(t, ch, "Deployment", "nexora-mgmt")
  	mc := container(t, mgmt, "mgmt")
  	for name, want := range map[string]string{
  		"NEXORA_QUERYLOG_BACKEND": "clickhouse", "NEXORA_CLICKHOUSE_URL": "http://clickhouse.nexora.svc:8123",
  		"NEXORA_CLICKHOUSE_DATABASE": "nexora", "NEXORA_CLICKHOUSE_TABLE": "querylog", "NEXORA_CLICKHOUSE_USERNAME": "nexora_reader",
  		"NEXORA_CLICKHOUSE_PASSWORD_FILE": "/etc/nexora/querylog/password",
  	} {
  		if e := env(mc, name); e == nil || e["value"] != want {
  			t.Errorf("clickhouse %s = %v, want %s", name, e, want)
  		}
  	}
  	if !strings.Contains(fmt.Sprint(mgmt.path("spec", "template", "spec", "volumes")), "nexora-clickhouse") {
  		t.Error("password Secret volume missing")
  	}
  	lk := render(t, append(common, "--set", "mgmt.querylog.backend=loki", "--set", "mgmt.querylog.loki.url=http://loki.monitoring.svc:3100", "--set", "mgmt.querylog.loki.tenant=t1")...)
  	lc := container(t, find(t, lk, "Deployment", "nexora-mgmt"), "mgmt")
  	for name, want := range map[string]string{
  		"NEXORA_LOKI_URL": "http://loki.monitoring.svc:3100", "NEXORA_LOKI_SELECTOR": `{service_name="nexora-engine"}`,
  		"NEXORA_LOKI_TENANT": "t1", "NEXORA_LOKI_LOOKBACK": "168h",
  	} {
  		if e := env(lc, name); e == nil || e["value"] != want {
  			t.Errorf("loki %s = %v, want %s", name, e, want)
  		}
  	}
  	if env(lc, "NEXORA_LOKI_PASSWORD_FILE") != nil {
  		t.Error("no passwordSecret -> no password file")
  	}
  	if out, err := helm(append([]string{"template", "nexora", chartDir, "--set", "mgmt.querylog.backend=loki"}, common...)...); err == nil || !strings.Contains(out, "mgmt.querylog.loki.url is required when mgmt.querylog.backend=loki") {
  		t.Errorf("missing loki url: %v %s", err, out)
  	}
  	if out, err := helm(append([]string{"template", "nexora", chartDir, "--set", "mgmt.querylog.backend=clickhouse"}, common...)...); err == nil || !strings.Contains(out, "mgmt.querylog.clickhouse.url is required when mgmt.querylog.backend=clickhouse") {
  		t.Errorf("missing clickhouse url: %v %s", err, out)
  	}
  	kw := render(t, "-f", "../kw/values-kw.yaml", "--set", "image.tag=sha-0000000", "--api-versions", "monitoring.coreos.com/v1")
  	if e := env(container(t, find(t, kw, "Deployment", "nexora-mgmt"), "mgmt"), "NEXORA_QUERYLOG_BACKEND"); e == nil || e["value"] != "opensearch" {
  		t.Errorf("kw backend = %v, want opensearch", e)
  	}
  }
  ```
  Reuse the `common` flags that `TestHelmTemplate` uses for a minimal render, if they differ.
- [ ] Run `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestHelmQueryLogBackends -count=1'` and
      expect FAIL: `clickhouse NEXORA_CLICKHOUSE_URL = map[] ...` (or a schema rejection of `clickhouse`).
- [ ] Implement:
  - **`values.yaml`:** `backend: builtin # builtin | opensearch | clickhouse | loki`, plus
    `clickhouse: { url: "", database: nexora, table: querylog, username: default, passwordSecret: { name: "", key: password } }`
    and
    `loki: { url: "", selector: '{service_name="nexora-engine"}', tenant: "", username: "", passwordSecret: { name: "", key: password }, lookback: 168h }`.
  - **`values.schema.json`:** the enum gains the two names, and add `clickhouse` and `loki` objects
    with string properties and `passwordSecret {name, key}`.
  - **`mgmt-deployment.yaml`:**
    - For `clickhouse`: `required "mgmt.querylog.clickhouse.url is required when mgmt.querylog.backend=clickhouse"`,
      the four value env entries, and `NEXORA_CLICKHOUSE_PASSWORD_FILE=/etc/nexora/querylog/password`
      when `passwordSecret.name` is set.
    - For `loki`: the same pattern with its variables; `NEXORA_LOKI_TENANT` and
      `NEXORA_LOKI_USERNAME` are rendered even when empty.
    - The volume `querylog-password` (secret `passwordSecret.name`, items `key → password`) is
      mounted read-only at `/etc/nexora/querylog`.
- [ ] If the M9 CRD grep matched, add `clickhouse` and `loki` with the same fields to that CRD or type
      and to its generated schema with the operator's generator command. Extend the operator's
      rendering test with one case per backend asserting the rendered `NEXORA_*_URL`. Otherwise
      write `no M9 query-log backend enum found (grep output empty)` in the report.
- [ ] Run
      `scripts/dev-exec.sh 'go test ./deploy/deploytest -count=1 && helm lint deploy/helm/nexora --strict -f deploy/helm/nexora/ci/lint-values.yaml'`
      and expect PASS.
- [ ] Run `scripts/pc-format.sh deploy/helm/nexora/values.yaml`.
- [ ] Report the paths. Commit message: `helm: clickhouse and loki query-log settings`.

## Task 10: kw ClickHouse, collector fan-out and kw acceptance

Files:

- `deploy/kw/clickhouse.yaml`: created. ConfigMap `clickhouse-config` (`config.d/nexora.xml`,
  `users.d/nexora.xml`), StatefulSet and Service `clickhouse`.
- `deploy/kw/otelcol.yaml`: pipelines `logs/clickhouse` and `logs/loki`, and the writer password env.
- `deploy/kw/README.md`: the new manifest row and the query-log fan-out paragraph.
- `scripts/kw-deploy.sh`: Secret, manifest, rollout and schema apply.
- `scripts/kw-acceptance.sh`: the ClickHouse reader password and M10 environment, plus
  `TestKwQueryLogBackends` in the default run pattern.
- `deploy/deploytest/kw_querylog_test.go`: created. `TestKwClickHouseManifest` and
  `TestKwCollectorFansOutQueryLogs`.
- `e2e/kw_querylog_backends_test.go`: created. `TestKwQueryLogBackends`.

Interfaces: consumes `querylog.NewClickHouse`, `NewLoki`, `NewOpenSearch` and
`deploy/clickhouse/querylog.sql`. Produces the kw environment `NEXORA_KW_OPENSEARCH_URL`,
`NEXORA_KW_CLICKHOUSE_URL`, `NEXORA_KW_CLICKHOUSE_PASSWORD_FILE` and `NEXORA_KW_LOKI_URL`.

- [ ] Create `deploy/deploytest/kw_querylog_test.go`:
  - **`TestKwClickHouseManifest`:**
    - Decode every document of `../kw/clickhouse.yaml` with `sigs.k8s.io/yaml`, or with the YAML
      decoder already used in this package.
    - Assert every document has namespace `nexora`.
    - The StatefulSet `clickhouse` has:
      - image `192.168.10.131/clickhouse/clickhouse-server:26.8.4.11`;
      - `volumeClaimTemplates[0]` with `storageClassName: longhorn-single` and `storage: 20Gi`;
      - container `resources.requests` and `resources.limits` set;
      - env `CLICKHOUSE_WRITER_PASSWORD` and `CLICKHOUSE_READER_PASSWORD` from Secret
        `nexora-clickhouse` keys `writer-password` and `reader-password`;
      - ports 8123 and 9000.
    - The ConfigMap `users.d/nexora.xml` contains `from_env="CLICKHOUSE_WRITER_PASSWORD"` and
      `from_env="CLICKHOUSE_READER_PASSWORD"`, `<ip>::1</ip>` and `<ip>127.0.0.1</ip>` under
      `default`, and no `<password>` element with text content.
    - The Service exposes 8123 and 9000.
    - The positive assertions come first. Then assert the file contains no base64 or plaintext
      password (regex `password>[^<]+<`).
  - **`TestKwCollectorFansOutQueryLogs`:**
    - Parse the ConfigMap `nexora-otelcol` `config.yaml` string as YAML.
    - Assert that:
      - `service.pipelines.logs.exporters == [opensearch]` with processors
        `[batch, transform/querylog]`;
      - `logs/clickhouse.exporters == [clickhouse]`;
      - `logs/loki` has exporters `[otlphttp/loki]` and processors `[batch, transform/loki]`;
      - `exporters.clickhouse.create_schema == false`, with `username == nexora_writer`,
        `password == "${env:CLICKHOUSE_WRITER_PASSWORD}"`,
        `endpoint == "tcp://clickhouse.nexora.svc.cluster.local:9000"`, `database == nexora` and
        `logs_table_name == querylog`;
      - `exporters["otlphttp/loki"].endpoint == "http://loki.monitoring.svc:3100/otlp"`;
      - the Deployment env `CLICKHOUSE_WRITER_PASSWORD` comes from Secret `nexora-clickhouse`.
    - When `exec.LookPath("otelcol-contrib")` succeeds (the dev pod), write the config to a temp file
      and require `otelcol-contrib validate --config` to exit 0. The test runs in the dev pod, where
      the binary always exists. Outside it, `t.Fatal` with `run in the dev pod`.
- [ ] Run
      `scripts/dev-exec.sh 'go test ./deploy/deploytest -run "TestKwClickHouseManifest|TestKwCollectorFansOutQueryLogs" -count=1'`
      and expect FAIL: `open ../kw/clickhouse.yaml: no such file or directory`.
- [ ] Create `deploy/kw/clickhouse.yaml`:
  - **ConfigMap `clickhouse-config`:**
    - `nexora.xml` for `config.d`: `listen_host 0.0.0.0`, logger to console at level `warning`,
      `max_server_memory_usage_to_ram_ratio 0.8`.
    - `nexora.xml` for `users.d`:
      - `default` with `<networks replace="replace"><ip>::1</ip><ip>127.0.0.1</ip></networks>` and
        `access_management 1`;
      - `nexora_writer` with `<password from_env="CLICKHOUSE_WRITER_PASSWORD"/>`, `networks ::/0`,
        profile `default` and a grant through `<grants><query>GRANT INSERT ON nexora.querylog</query></grants>`;
      - `nexora_reader` with its `from_env` password and
        `<grants><query>GRANT SELECT ON nexora.querylog</query></grants>`, plus
        `<readonly>2</readonly>` in its profile (typed query parameters need settings changes).
  - **StatefulSet `clickhouse`:**
    - 1 replica, `fsGroup: 101`, image as tested, ports `http 8123` and `native 9000`;
    - `readinessProbe httpGet /ping 8123`;
    - requests `500m`/`1Gi`, limits `2`/`3Gi`;
    - mounts `/etc/clickhouse-server/config.d/nexora.xml` and
      `/etc/clickhouse-server/users.d/nexora.xml` (`subPath`), and `/var/lib/clickhouse` from the
      claim `data`, 20 Gi `longhorn-single`;
    - env from Secret `nexora-clickhouse`.
  - **Service `clickhouse`:** 8123 and 9000.
  - Header comment matching `opensearch.yaml`'s style.
- [ ] Edit `deploy/kw/otelcol.yaml`:
  - Add the `clickhouse` exporter and the `otlphttp/loki` exporter
    (`endpoint: "http://loki.monitoring.svc:3100/otlp"`, `tls: { insecure: true }`), both with
    `sending_queue: { enabled: true, queue_size: 5000 }` and
    `retry_on_failure: { enabled: true, max_elapsed_time: 300s }`.
  - Add the `transform/loki` processor with the statement proven by `TestOtelcolConfigValidates`.
  - Add the two pipelines.
  - Add to the Deployment container the env `CLICKHOUSE_WRITER_PASSWORD` from `secretKeyRef`
    `nexora-clickhouse/writer-password`.
  - Update the header comment to name the three query-log destinations.
- [ ] Run the step-2 command and expect PASS.
- [ ] Edit `scripts/kw-deploy.sh`, before the existing `k apply -f "$kw/opensearch.yaml" ...` line:
  ```bash
  # ClickHouse query-log backend (M10): passwords generated once, schema applied idempotently.
  if ! k get secret nexora-clickhouse >/dev/null 2>&1; then
  	k create secret generic nexora-clickhouse \
  		--from-literal=writer-password="$(openssl rand -base64 32)" \
  		--from-literal=reader-password="$(openssl rand -base64 32)"
  fi
  k apply -f "$kw/clickhouse.yaml"
  ch_sha=$(shasum -a 256 "$kw/clickhouse.yaml" | cut -c1-16)
  k patch statefulset clickhouse -p "{\"spec\":{\"template\":{\"metadata\":{\"annotations\":{\"nexora.io/config-sha\":\"$ch_sha\"}}}}}"
  k rollout status statefulset/clickhouse --timeout=5m
  k exec -i clickhouse-0 -c clickhouse -- clickhouse client --multiquery <"$repo/deploy/clickhouse/querylog.sql"
  ```
  Use the script's existing repository-root variable in place of `$repo`. The collector apply and
  restart that follow already pick up the new `otelcol.yaml`.
- [ ] Create `e2e/kw_querylog_backends_test.go` with `TestKwQueryLogBackends`:
  1. Read `NEXORA_KW_DNS_ADDR`, `NEXORA_KW_DNS_ADDR_2`, `NEXORA_KW_OPENSEARCH_URL`,
     `NEXORA_KW_CLICKHOUSE_URL`, `NEXORA_KW_CLICKHOUSE_PASSWORD_FILE` and `NEXORA_KW_LOKI_URL`, using
     the `t.Fatalf` helper the other kw tests use for missing variables.
  2. Build `os := querylog.NewOpenSearch({URL, Index: "nexora-querylog-*"})`,
     `ch := querylog.NewClickHouse({URL, Database: "nexora", Table: "querylog", Username: "nexora_reader", PasswordFile})`
     and `lk := querylog.NewLoki({URL, Selector: "{service_name=\"nexora-engine\"}", Lookback: 168h})`.
  3. Positive path: send 20 names `kwql-<i>-<run>.test.`, alternating the two DNS addresses, and
     assert every answer is NOERROR or NXDOMAIN. Then, with `harness.EventuallyTrue` for 60 s, query
     each backend with `Name: "-<run>"`, `From = start−1m`, `To = now` and `Limit 100`, until each
     returns 20 records.
  4. Compare the three sets by the sorted `(Name, Client, QType, RCode, EngineID, Filter)` tuples, and
     `t.Fatalf` with a diff on a difference.
  5. Top: `from := time.Now().Add(-13*time.Minute).Truncate(time.Minute)` and
     `to := from.Add(10*time.Minute)`. For each of `name` and `client`, get
     `Top(TopQuery{From, To, Field, Limit: 10})` on the three backends. Require the OpenSearch list to
     be non-empty (positive path), and all three `reflect.DeepEqual`. On a mismatch, print the three
     lists.
- [ ] Edit `scripts/kw-acceptance.sh`:
  - Copy `k get secret nexora-clickhouse -o jsonpath='{.data.reader-password}' | base64 -d` to
    `/work/kw-clickhouse-password`.
  - Add the env
    `NEXORA_KW_OPENSEARCH_URL=http://opensearch.nexora.svc.cluster.local:9200`,
    `NEXORA_KW_CLICKHOUSE_URL=http://clickhouse.nexora.svc.cluster.local:8123`,
    `NEXORA_KW_CLICKHOUSE_PASSWORD_FILE=/work/kw-clickhouse-password` and
    `NEXORA_KW_LOKI_URL=http://loki.monitoring.svc:3100`.
  - The default run pattern gains `|TestKwQueryLogBackends`, and the header comment names it.
- [ ] Edit `deploy/kw/README.md`:
  - Add a `clickhouse.yaml` row to the manifest table.
  - Add a paragraph after the collector paragraph: logs fan out to OpenSearch (read by the console),
    ClickHouse (`nexora.querylog`, 7-day TTL, `nexora_writer`/`nexora_reader` from
    `nexora-clickhouse`) and the shared Loki in `monitoring` (168 h retention, nothing in `monitoring`
    changed). `TestKwQueryLogBackends` checks that all three agree.
- [ ] Run `scripts/pc-format.sh deploy/kw/README.md deploy/kw/clickhouse.yaml deploy/kw/otelcol.yaml` and
      `shellcheck scripts/kw-deploy.sh scripts/kw-acceptance.sh`. Expect no findings beyond those
      already present on `main`.
- [ ] Run `scripts/dev-exec.sh 'go test ./deploy/deploytest -count=1 && go vet ./e2e/...'` and expect PASS.
      `TestKwQueryLogBackends` runs only in Task 12, after the deploy.
- [ ] Report the paths. Commit message: `kw: clickhouse backend, collector fan-out to clickhouse and loki`.

## Task 11: Operations guide and help topic

Files:

- `docs/operations.md`: "Query logs, traces and OTLP" gains ClickHouse and Loki, and "Management plane
  environment" gains the variables.
- `web/src/help/topics/observability.md`: names the four backends.
- `deploy/deploytest/docs_querylog_test.go`: created. `TestOperationsDocNamesQueryLogSettings`.

Interfaces: consumes the variable names from Task 2 and the file names from Tasks 5 and 10. Produces
nothing.

- [ ] Create `deploy/deploytest/docs_querylog_test.go`:
  ```go
  func TestOperationsDocNamesQueryLogSettings(t *testing.T) {
  	cfg, err := os.ReadFile("../../mgmt/internal/config/config.go")
  	if err != nil {
  		t.Fatal(err)
  	}
  	names := regexp.MustCompile(`"(NEXORA_(?:CLICKHOUSE|LOKI)_[A-Z_]+)"`).FindAllStringSubmatch(string(cfg), -1)
  	if len(names) < 11 {
  		t.Fatalf("expected at least 11 clickhouse/loki variables in config.go, found %d", len(names))
  	}
  	ops, _ := os.ReadFile("../../docs/operations.md")
  	arch, _ := os.ReadFile("../../docs/architecture.md")
  	for _, m := range names {
  		for doc, body := range map[string][]byte{"docs/operations.md": ops, "docs/architecture.md": arch} {
  			if !bytes.Contains(body, []byte(m[1])) {
  				t.Errorf("%s does not name %s", doc, m[1])
  			}
  		}
  	}
  	for _, want := range []string{"deploy/clickhouse/querylog.sql", "create_schema: false", "/otlp", "allow_structured_metadata", "max_query_series"} {
  		if !bytes.Contains(ops, []byte(want)) {
  			t.Errorf("docs/operations.md lacks %q", want)
  		}
  	}
  	help, _ := os.ReadFile("../../web/src/help/topics/observability.md")
  	for _, want := range []string{"ClickHouse", "Loki", "OpenSearch"} {
  		if !bytes.Contains(help, []byte(want)) {
  			t.Errorf("observability help topic lacks %s", want)
  		}
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestOperationsDocNamesQueryLogSettings -count=1'`
      and expect FAIL: `docs/operations.md does not name NEXORA_CLICKHOUSE_URL`.
- [ ] Edit `docs/operations.md`.
  - **"Management plane environment" table or list:** one entry per [S-8] variable, with its default
    and meaning.
  - **"Query logs, traces and OTLP":** after the OpenSearch bullet, add a **ClickHouse
    (`clickhouse`)** bullet:
    - apply `deploy/clickhouse/querylog.sql` with `clickhouse client --multiquery` (idempotent) and
      create an insert-only user and a select-only user;
    - a collector exporter snippet (`clickhouse`, `create_schema: false`, `logs_table_name: querylog`);
    - `NEXORA_CLICKHOUSE_*` for the management plane;
    - retention is the table's 7-day TTL, edited in the SQL before the first apply;
    - schema upgrades ship as new SQL files.
  - Add a **Loki (`loki`)** bullet:
    - Loki 3.x with `allow_structured_metadata: true`;
    - the collector `otlphttp` exporter to `<loki>/otlp` with the `transform/loki` body statement;
    - selector, tenant and lookback;
    - Top uses two instant queries and falls back to 37 partition queries when `max_query_series` is
      reached, and a partition still over the limit shows top lists as unavailable (raise
      `max_query_series`);
    - identical queries from one client in one microsecond collapse (a Loki property).
  - Add a **Choosing a backend** bullet: builtin (single instance), OpenSearch (full-text, heavy),
    ClickHouse (large volume, exact aggregates), Loki (existing Grafana stack).
  - **"Known limitations":** the Loki dedupe and series-limit notes, and ClickHouse schema changes
    being manual.
- [ ] Edit `web/src/help/topics/observability.md`: one short paragraph per backend in operator
      language, keeping the existing `docs/operations.md` section reference.
- [ ] Run
      `scripts/dev-exec.sh 'go test ./deploy/deploytest -count=1 && make webui-placeholder && go test ./deploy/deploytest -run TestHelpTopicsReferenceOperationsDoc -count=1'`
      and expect PASS. Then run `scripts/pc-format.sh docs/operations.md web/src/help/topics/observability.md`
      and `cd web && pnpm run lint`, and expect no findings.
- [ ] Report the paths. Commit message: `docs: clickhouse and loki query-log backends`.

## Task 12: Full verification, kw deploy, acceptance and issue close

Files:

- None created or modified. The task runs the milestone gate and reports.
- Only if kw measurements change a documented figure: `docs/operations.md`, "kw deployment" section.

Interfaces: consumes every task.

- [ ] Run
      `scripts/dev-exec.sh 'make lint && make engine-test && make webui-placeholder && go test ./mgmt/... ./deploy/... -count=1'`
      and expect PASS.
- [ ] Run
      `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=/work/nexora/bin go test ./e2e/... -count=1 -timeout 180m'`
      and expect PASS, including `TestGUICoverage`, `TestQueryLogBackends` (four backends),
      `TestQueryLogCategoryAttribution` (four backends) and `TestQueryLogConformanceOpenSearch`,
      `...ClickHouse` and `...Loki`.
- [ ] Run `scripts/kw-deploy.sh` with the DNS probe (5 queries/s each to 192.168.10.136 and 192.168.10.139)
      and expect zero lost probe queries. Check with
      `kubectl --context kw -n nexora get statefulset clickhouse -o jsonpath='{.status.readyReplicas}'`
      (expect `1`) and
      `kubectl --context kw -n nexora exec clickhouse-0 -- clickhouse client -q "SELECT count() FROM nexora.querylog WHERE Timestamp > now() - INTERVAL 5 MINUTE"`
      (expect > 0 within 5 minutes). On any lost query, run `helm rollback nexora` and record why.
- [ ] Wait 3 minutes after the collector rollout. Run `scripts/kw-acceptance.sh` and expect PASS,
      including `TestKwQueryLogBackends`. On failure, run `helm rollback nexora`. Leave
      `deploy/kw/clickhouse.yaml` running (it does not serve DNS), and record the failing assertion.
- [ ] Close issues #35 and #36 in `azrtydxb/nexora` with
      `gh issue close <n> -R azrtydxb/nexora -c "<comment>"`. The comment names the commits and the
      proof: the conformance test names, `TestQueryLogBackends` subtests, `TestKwQueryLogBackends`,
      and the zero-loss probe counts.
- [ ] Report the proof. No commit.
