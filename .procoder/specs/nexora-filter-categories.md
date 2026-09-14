# nexora-filter-categories

Status: complete

## Problem

Nexora filters with blocklist subscriptions, but operators who know UniFi CyberSecure or similar
products expect to pick **categories** (malware, phishing, ads, gambling, adult, social, …) instead of
pasting list URLs, to see **which category** blocked a query, and to use large curated feeds without the
resolver getting slower or running out of memory.

Measured on kw (Cortex-A76/A55, `engine/examples/filter_bench.rs`, real HaGeZi, Blocklist Project and
OISD lists, single thread), the v1 filter (`FxHashSet<Box<[u8]>>` per filter set, one probe per name
suffix) degrades with list size because every probe is a cache miss plus a pointer chase:

| unique domains | build  | memory               | blocked name | clean name |
| -------------- | ------ | -------------------- | ------------ | ---------- |
| 0.2M           | 0.06 s | 12 MB                | 93 ns        | 188 ns     |
| 1.2M           | 0.41 s | 75 MB                | 295 ns       | 140 ns     |
| 2.5M           | 0.95 s | 150 MB               | 343 ns       | 221 ns     |
| 5.1M           | 2.8 s  | 309 MB (63 B/domain) | 400 ns       | 285 ns     |

Every policy group builds its own copy, multiplying memory and build time, and the engine cannot say
which list matched. Pi-hole keeps lists in SQLite (B-tree) and does not solve this either.

## Users

- **Homelab operator:** turns on "Malware", "Ads" and "Adult" for the whole network and "Gambling" and
  "Social" for the kids' devices without hunting for list URLs; sees in the query log why a site broke.
- **ISP / enterprise operator:** subscribes to multi-million-domain threat feeds on every engine
  without latency or memory blow-up; needs per-category statistics and memory visibility per engine.

## In scope

- [S-1] Shared filter index in the engine: every blocklist and allowlist of the snapshot (global,
  policy groups, categories) is built once into one deduplicated, pointer-free index; each entry carries
  a list-set id; global and per-group decisions are bitmask tests over that index.
- [S-2] Lookup optimisation: one-pass right-to-left hashing of all label suffixes, batched bucket
  prefetch, exact match confirmation against a compact name arena, and a per-worker decision cache for
  repeated names keyed by name hash, filter view and index generation; allocation-free on the query path.
- [S-3] Build off the query path without per-domain heap allocations, atomic swap, and reuse of an
  unchanged index when a snapshot does not change any list.
- [S-4] Category catalog in the management plane: a built-in, fixed catalog of categories, each mapping
  to one or more curated sources (with license and attribution metadata). Operators enable and disable
  each category globally and per policy group, and can enable or disable individual sources within a
  category; enabled sources become filter-list subscriptions managed by the catalog. Operators cannot
  add their own sources to a category (custom block lists remain the separate v1 filter-list feature).
- [S-10] OISD notice: enabling a category or source backed by OISD shows a notice that OISD lists are
  not free for commercial use and requires confirmation; the API rejects enabling an OISD source
  without an explicit `acknowledge_license: true`, and the audit log records the acknowledgement.
- [S-5] UT1 archive support: a source can point to a gzip-compressed tar archive and name the member path; the fetcher
  extracts only that member before normalising.
- [S-6] Attribution: the engine reports which list(s) matched a blocked query; query log records carry
  the list id and category, and the API/GUI show them and can filter by category.
- [S-7] GUI: category catalog screen with toggles, source/license/attribution info, per-policy-group
  category selection, category column and filter in the query log.
- [S-8] Observability: engine metrics for filter index entries, bytes, build duration and per-category
  block counts; the fleet/engine GUI shows filter index memory per engine.
- [S-9] Performance gate: a filter benchmark with fixed list corpora checked in CI (relative) that fails
  on regression, and recorded absolute numbers on kw.

## Out of scope

- Commercial categorisation feeds that require paid licensing (Webroot/Zvelo/Proofpoint); RPZ already
  covers commercial feeds delivered as zones.
- URL-path or TLS-SNI filtering (DNS names only).
- Block pages (HTTP interstitial) — block responses stay DNS-level (null IP / NXDOMAIN / REFUSED).
- Per-user (identity-based) policy; policy stays per client CIDR.
- Machine-learning or heuristic categorisation of unknown domains.

## Constraints

- Hot path rules from `docs/architecture.md` stay binding: no allocation, lock or log on the cache-hit
  path (`cache_hit_path_does_not_allocate` must keep passing with the new index).
- Targets on kw hardware (Cortex-A76 core, single thread) with the 5.1M-domain corpus (revised
  2026-09-14 after measurement: one uncached memory read on the RK3588 costs 110-140 ns, so the
  original < 150 ns for blocked names is below the hardware floor): clean names **< 150 ns**, cold
  blocked names **< 300 ns**, repeated names **< 50 ns** on a Zipf-distributed query workload thanks to a
  per-worker decision cache (fits in CPU cache, invalidated by index generation), index memory
  **< 120 MB**, build **< 2 s**; with 3 policy groups selecting different subsets, memory stays within
  10% of the single-group index.
- Exact matching: a hash collision must never block or allow a name that is not listed (confirmation
  against stored names).
- Semantics unchanged from v1: an entry blocks the domain and all subdomains; allowlist beats
  blocklist; per-client group lists replace global lists; CNAME cloaking checks CNAME targets.
- Catalog ships all sources the user approved, including OISD; the GUI shows each source's license and
  attribution, and marks OISD as not free for commercial use (with a confirmation notice when enabled).
- The catalog is read-only for operators: no API or GUI path adds, edits or removes catalog sources;
  catalog changes ship with Nexora releases.
- Categories are off by default on new installs.
- Filter index memory cap: default 50% of the engine's cgroup memory limit, or 512 MB when the engine
  has no limit; overridable per engine group. A snapshot whose index would exceed the cap is rejected
  with a reason and the previous index stays active.
- Existing M1–M5 acceptance tests keep passing unchanged.

## Interfaces

- **Contract:** `FilterConfig` and `PolicyGroup` gain list identity: each `BlobRef` in blocklists
  is accompanied by a `list_id` and `category` (new message `FilterListRef` at fields 600+); the engine
  maps list ids to bit positions. Query-log records gain `nexora.filter.list_id` and
  `nexora.filter.category`.
- **Engine:** `filter::FilterIndex` (shared) and `filter::FilterView` (bitmask per global/group)
  replace per-group `FilterSet` copies; `decide(name_wire) -> FilterDecision` keeps its signature and
  returns the matched list bit for attribution.
- **API (OpenAPI):** `GET /filter-categories` (catalog with sources, license, enabled state, entry
  counts), `PUT /filter-categories/{key}` (enable/disable the category globally; body carries per-source
  enabled flags and `acknowledge_license` for OISD sources; there is no create/delete operation), policy group input gains
  `category_keys`, query log gains `category` filter and field, engine stats gain filter index metrics.
- **Catalog data:** `mgmt/internal/catalog/catalog.yaml` embedded in the binary (category key, name,
  description, sources with URL, format, optional archive member, license, attribution).
- **GUI:** `/filtering/categories` screen with enable/disable toggles per category and per source and an
  OISD license notice dialog on enabling; policy group dialog category picker; query log category
  column and filter; engine detail shows filter index memory.

## Data

- **PostgreSQL:** `filter_categories` (key, enabled, revision), `filter_lists` gains `category_key` and
  `managed_by_catalog`; policy groups gain `category_keys text[]`; migration after 00502.
- **Engine memory:** one index per engine: hash table of `(u64 hash, u32 name offset, u32 list-set id)`
  slots plus a name arena and a list-set table (distinct combinations of list bits).
- **Blobs:** unchanged normalised list blobs; UT1 member extraction happens before normalisation.

## Edge cases

- The same domain in several lists and categories: stored once, list-set covers all; attribution
  reports every matching category (first by catalog order in the log field, all in metrics).
- A suffix listed in one list and a longer name in another list: the longest matching suffix decides
  attribution; allowlist at any suffix still wins.
- More than 64 distinct lists in one snapshot: list-set ids index a table of bitsets sized to the list
  count, not a fixed 64-bit mask.
- Hash collisions: two different names with the same 64-bit hash both stored and both confirmed.
- Names at the 255-octet limit and labels with escaped bytes hash and compare exactly.
- Empty lists, lists that fail to fetch (last good version kept), categories whose every source fails.
- Snapshot that only changes an allowlist entry: index rebuilt; snapshot that changes nothing list
  related: index reused without rebuild.
- Memory pressure: an index above the cap (50% of the cgroup limit, or 512 MB without a limit, per
  engine group override) refuses the snapshot with a clear reason instead of OOM-killing the engine.
- UT1 archive where the named member is missing or the archive is corrupt: fetch error recorded, last
  good version kept.

## Failure modes

- **Source download fails or is slow:** last good blob kept, category marked stale in the GUI and metric.
- **Index build fails or exceeds the memory cap:** snapshot rejected with the reason; previous index
  stays active.
- **Catalog source URL disappears upstream:** fetch errors surface per source; other sources of the
  category still apply.
- **Management plane unreachable:** engines keep the last applied index.
- **Query log backend down:** attribution is dropped with the record as today; blocking is unaffected.

## Acceptance criteria

- [ ] [S-1] [S-2] [S-3] Rust test `filter_index_matches_filterset_semantics`, run by `.github/workflows/ci.yml` (property test over random names and lists) proves identical decisions to the v1 `FilterSet` for global and per-group views, including allowlist precedence and subdomain matching; fails if any decision differs.
- [ ] [S-1] [S-2] `engine/examples/filter_bench.rs` on kw with the 5.1M corpus reports < 150 ns for clean names, < 300 ns for cold blocked names, < 50 ns for repeated names on a Zipf workload, < 120 MB index memory and < 2 s build; `TestFilterIndexBudget` asserts the memory and build limits on a 1M synthetic corpus in CI; fails if a limit is exceeded.
- [ ] [S-1] `TestFilterIndexSharedAcrossGroups` shows three policy groups over the same lists add < 10% index memory versus one; fails if groups duplicate the index.
- [ ] [S-2] Rust test `cache_hit_path_does_not_allocate`, run by `.github/workflows/ci.yml`, still passes with the shared index and a policy group client; fails if the decision path allocates.
- [ ] [S-3] Rust test `unchanged_lists_reuse_index`, run by `.github/workflows/ci.yml`, proves a snapshot without list changes does not rebuild the index; fails if it rebuilds.
- [ ] [S-4] `TestFilterCategories` enables categories globally and per policy group through the API and proves blocked names per category over UDP, TCP and DoH against a real managed stack with fixture sources; fails if a category does not block or blocks the wrong group.
- [ ] [S-4] [S-10] `TestFilterCategoryToggles` disables a category and a single source and proves their names stop being blocked while other sources keep blocking; enabling an OISD source without `acknowledge_license` returns 422, with it succeeds and writes an audit entry; no endpoint accepts a custom source for a category; fails if any toggle does not take effect or the OISD guard or read-only catalog can be bypassed.
- [ ] [S-5] `TestUT1ArchiveMember` fetches a fixture `tar.gz`, extracts one category member and blocks its names; fails if other members leak in or a missing member is not reported.
- [ ] [S-6] `TestQueryLogCategoryAttribution` proves blocked queries carry list id and category in both builtin and OpenSearch backends and the API filters by category; fails if attribution is missing or wrong.
- [ ] [S-7] `TestGUICoverage` stays green with the new operations and Playwright spec `22-filter-categories.spec.ts` toggles a category on and off, disables one source, sees the OISD notice before enabling an OISD-backed category, assigns a category to a policy group, and finds the category in the query log; fails if any step does not render or persist.
- [ ] [S-8] `TestObservabilityMetricsTraces` asserts `nexora_filter_index_entries`, `nexora_filter_index_bytes`, `nexora_filter_index_build_seconds` and `nexora_filter_blocked_total{category}`; fails if a metric is missing.
- [ ] [S-9] `.github/workflows/perf-gate.yml` runs the filter benchmark relative gate and fails on > 5% decision-time regression; `TestKwFilterCategories` on kw enables real catalog categories and records decision time and memory per engine; fails if kw blocking or the budgets fail.

## Open questions

