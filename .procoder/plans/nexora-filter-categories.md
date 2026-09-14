# nexora-filter-categories — implementation plan

Status: draft
Spec: .procoder/specs/nexora-filter-categories.md

## Goal

Replace the per-group `FxHashSet` filter with one shared, pointer-free, prefetched filter index that meets the kw budgets (< 150 ns per decision, < 120 MB and < 1.5 s build for the 5.1M-name corpus), and build the category catalog on top of it: read-only curated sources with license metadata and an OISD/commercial-use guard, UT1 archive members, per-list attribution in the query log, per-category metrics, GUI screens, a CI performance gate and a kw acceptance test.

## Architecture

The engine builds one `filter::index::FilterIndex` per snapshot from every block and allow list (global, policy groups, categories, inline group allowlists): names are deduplicated, each carries a list-set id, and the table is an array of 128-byte cache-line blocks with two candidate blocks per name, holding 6-bit packed names inline so a lookup confirms exactly without a second memory access. Every global or group policy holds a cheap `FilterView` (per list-set verdict, first matching list and category bits), so groups share the index. The management plane embeds `mgmt/internal/catalog/catalog.yaml`, mirrors each catalog source into a `filter_lists` row managed by the catalog, sends list identity (`FilterListRef`, proto fields 600+) with every blob, and serves `/filter-categories`; attribution flows back through new query-log attributes.

### Filter index design (binding for Tasks 2–7)

Measured inputs (laptop analysis of the real lists on 2026-09-14, with the Python/Rust scripts kept only in the author's scratchpad): the default catalog selection (every source with `default_enabled: true` in Task 9) has **5,112,325 unique names**, average text length 18.0 characters, 77% two-label, 19% three-label, 4% four-or-more-label names, 1,187 TLDs, and 776 distinct list combinations across 21 lists. A first prototype with linear probing over cache-line blocks spilled into the next block for 74% of the blocks at 80% fill and was rejected; greedy two-choice placement over 64-byte blocks overflowed 5–10% of the names and was rejected too; two-choice placement over 128-byte blocks overflowed 0.16–0.41% at the fill below, and is the design.

1. **Symbols.** List names only contain `[a-z0-9_-]` and dots (v1 `domain_to_wire` rules). Each character maps to a 6-bit symbol: `a..z` → 1..26 (upper case maps to the same symbol), `0..9` → 27..36, `-` → 37, `_` → 38; the label separator is symbol 0; every other octet is `INVALID` (0xFF). A name is packed **right to left by label** (TLD label first, labels left to right inside), little-endian bit order: symbol `k` occupies bits `6k..6k+6` of the stream. Consequently the packed form of a suffix is a bit-prefix of the packed form of every longer name that ends in it, and one packed buffer of the query holds every suffix. A name of `n` text characters has exactly `n` symbols (each dot becomes one separator), at most 253, stored as `u8`.
2. **Hash.** `fold(a, b) = lo64(a·b) ^ hi64(a·b)` (64×64→128-bit multiply, one `MUL`+`UMULH` on aarch64, one `MUL` on x86_64). A suffix hash is computed incrementally right to left: `h(root) = seed`, `h(label.suffix) = mix_label(h(suffix), label)` with `mix_label(p, l) = fold over the 8-byte little-endian chunks of l (zero-padded) starting from fold(p ^ K_LEN, len(l) ^ K_CHUNK)`. **Prefix independence:** `h(s)` is defined only from `seed` and the labels of `s`; by induction on the label count, the value computed for suffix `s` while walking any longer name `x.s` is the value computed for the listed text `s` at build time, because the walk visits the labels of `s` first and in the same order, and `mix_label` absorbs the label length before its octets, so label boundaries are unambiguous. The seed is random per build (`rand::rng()`), so neither list content nor query names can be tuned to collide blocks; correctness never depends on the hash because every candidate is confirmed against its packed name. Chosen over SipHash (keyed but ~3× slower per label) and FxHash (unkeyed, weak low bits for block selection).
3. **Blocks.** One `AlignedBytes` allocation, 2 MiB aligned, `madvise(MADV_HUGEPAGE)` (kw kernels run THP in `madvise` mode), of `block_count × 128` bytes. Block layout: byte 0 = entry count (bits 0–5, at most 42) | `OVERFLOWED` (bit 7: a name whose candidates include this block lives in the stash); bytes `1..1+count` = 8-bit fingerprints `(h·K_FP) >> 56`; bytes `1+count..1+2·count` = entry offsets from the block start; then the entries. Entry = `symbols: u8` + list-set id as LEB128 varint (set ids are renumbered by frequency, so 99% take one byte) + `ceil(6·symbols/8)` packed bytes; names above 64 symbols store a stub `0xFF, varint set, symbols: u8, u32 LE offset` into a separate `long` arena. Candidates: `b1 = ((h >> 32) · N) >> 32`, `b2 = ((h & 0xFFFF_FFFF) · N) >> 32`, `b2 = b1 + 1 (mod N)` when equal. Average entry 15.65 bytes + 2 directory bytes; `N = ceil(Σ(entry + 2) / (127 · 0.84))` gives **21.2 bytes per name → 108 MB at 5.1M names** (budget 120 MB). The stash is a second table in the same format sized for 50% fill; if a stash placement fails the stash doubles, at most four times, then the build fails with `IndexError::Placement`.
4. **Placement (build).** No per-domain heap allocation: (a) decode each blob once; (b) scoped threads (`min(available_parallelism, 4)`, 2 on kw engines) scan lists into one pre-sized `Vec<Record>` per list, `Record { hash: u64, offset: u32, list: u16, len: u8, _pad: u8 }` = 16 bytes; (c) scatter by the top hash byte into 256 buckets and sort buckets in parallel by `(hash, list)`; (d) walk runs of equal hash, compare names (ASCII case-insensitive) and OR list bits into a reused scratch bitset of `ceil(lists/64)` words; intern bitsets into `SetTable` (one flat `Vec<u64>` arena plus an `FxHashMap<u64, u32>` keyed by a fold hash of the words with collision chains), which supports any number of lists up to 65,534; (e) renumber sets by frequency; (f) compute entry sizes, `N`, and the exact memory; reject with `IndexError::OverCap` **before** allocating blocks; (g) place names in hash order (so `b1` is monotonic) by looking only at a one-byte `used` array per block (850 KB at 5.1M names): pick the less-used candidate that fits, else queue for the stash; (h) counting-sort names by chosen block and write blocks in parallel over disjoint block ranges. Reuse: the runtime keys the index by every list's id, kind and content hash; a snapshot with the same key reuses the `Arc<FilterIndex>`; views are rebuilt (microseconds).
5. **Lookup.** `FilterView::decide(name_wire) -> FilterDecision` is allocation-free: one forward pass records label offsets; one right-to-left pass computes each suffix hash, appends its symbols to a 200-byte stack buffer, and immediately prefetches both cache lines of both candidate blocks (4 prefetches per level), so the memory fetches run in parallel with the remaining hashing; labels with an `INVALID` octet end the walk (such a suffix and every longer one cannot be listed). Then levels are examined from the longest suffix to the shortest: compare the fingerprint bytes of `b1`, then `b2` (and the stash only when either block is `OVERFLOWED`), check `symbols`, compare `floor(6·symbols/8)` bytes and the masked tail bits. A match yields a set id; `class[set]` bit 1 (allow list of the view) returns `Allowed`; bit 0 (block list of the view) records the first (longest) hit and returns it at once when the view has no allow list — exactly v1's order. Prefetch on stable Rust 1.97.1 (verified against the 1.97.1 sources): `core::arch::x86_64::_mm_prefetch` is stable on x86_64 but, carrying `#[target_feature(enable = "sse")]`, still needs an `unsafe` call (E0133) although SSE is baseline; `core::arch::aarch64::_prefetch` (`stdarch_aarch64_prefetch`), `core::hint::prefetch_read` (`hint_prefetch`) and `core::intrinsics::prefetch_read_data` are unstable, so aarch64 uses stable inline assembly `asm!("prfm pldl1keep, [{p}]", options(nostack, readonly, preserves_flags))`, which a test build on the laptop showed is inlined as a single `prfm` instruction.
6. **List sets and views.** `FilterView` holds, per set id, `class: u8`, `first_list: u16` (lowest list index of the view's block lists in the set; lists are ordered by `FilterListRef.position`, which management sets to catalog order, then custom lists by name) and `categories: u64` (category slot bits). A view costs 11 bytes per set (8.5 KB for 776 sets), so three extra policy groups add well under 1% memory.
7. **Memory cap.** `ConfigSnapshot.filter_index_max_bytes` (from `engine_groups.filter_index_max_bytes`) when non-zero, else 50% of `/sys/fs/cgroup/memory.max` (cgroup v2), else 512 MiB when the file is absent or reads `max`. Index plus views above the cap rejects the snapshot with `filter index needs N bytes, above the cap of M bytes`; the previous runtime stays.

### Filter index design as built and measured in Task 4 (supersedes items 1, 3, 4 and 5 above)

Measured on kw (`worker-23`, Cortex-A76 cores 4–5, 2 build threads) with the 5.1M-name dev-pod corpus, the design above gave 342 ns blocked / 475 ns clean and a 2.8 s build: every query suffix probed two cold 128-byte blocks (12–20 cache lines per decision) and hashing plus 6-bit packing cost ~200 ns of compute; hash-order placement overflowed 2.9% of the names (the placement simulation had placed largest entries first). Region instruction counters (`perf_event_open`, as `perf` is not installed) and several iterations produced the design now in `engine/src/filter/{names,index}.rs`, with the Task 3 interfaces unchanged except `BLOCK_FILL = 0.88`:

1. **Grouped placement.** A name's placement key is the seeded suffix hash of its last two labels (single-label names: their own hash), so one probe of two blocks answers every level of a query name. A two-label suffix with more than `GROUP_MAX = 4` names is heavy: its deeper names are keyed by their own hash, the suffix gets a marker entry (header bit), and heavy keys sit in a small in-cache open-addressing table so lookups under them prefetch per-level blocks in the same wave.
2. **Entries.** Block: count, 8 overflow tag bits (`fingerprint & 7` of stashed names), one fingerprint octet per entry (key fingerprint xor symbol count, so only the one- and two-label hashes are computed per query), then entries: symbols (wire length without root) | marker bit, LEB128 set id, the wire form (length octets included) packed 7 bits per octet. A query confirms by comparing its own octets (high bit rejected) word by word — no packing, no validation pass; 6-bit symbols needed a positional validity check that cost ~270 instructions per hit.
3. **Hash.** `absorb(parent, len, w0, w1) = fold(parent ^ w0, K ^ len << 8) ^ fold(rotl(parent, 32) ^ w1, K)`, one constant (aarch64 rematerialised four 64-bit constants per label), both multiplies parallel; further 8-octet chunks `fold(h ^ w, K)`. Fingerprint `(h ^ h >> 32) as u8`.
4. **Placement.** Largest entries first into the less-used candidate, then one cuckoo relocation step for names that do not fit (113K overflow -> ~14K stashed at fill 0.88), then the stash. Build: parallel scan, parallel scatter by the top key octet, per-bucket radix-then-sort and dedupe on 2 threads with one-list set ids cached, slot arrays with prefetch, entries written in unique order at precomputed octet positions on 2 threads.
5. **API changes in `names`:** `walk_hashes(seed, name, len, &mut Levels, FnMut(u64) -> bool)` (stops when the closure returns false), `Levels::{labels, start, root, symbols}` (symbols = text length + 1), `suffix_matches`, `label_listable`, `pack_wire_text`, `text_hashes_in` / `TextHashes { hash, key, labels }`, `find_byte`, `PACKED_LEN = 232`; `packed_eq`, `Levels::pack` and `Packer` on the query side are gone. The Task 2/3 code blocks below are the first iteration; the committed code is authoritative.

**Gate result (2026-09-14, same run):** index 5,136,759 names, 118.2 MB (23.0 B/name), build 1.87 s, blocked 284 ns, clean 107 ns; v1 `FilterSet` 324 MB RSS, 2.65 s, 388 ns, 264 ns. Memory and clean decisions meet the targets; **blocked decisions (< 150 ns) and build (< 1.5 s) do not**, so Task 6 does not start. The same binary decides blocked names in 137 ns with the index in cache: each blocked sample needs one uncached block, ~140 ns on this node (a dependent DRAM read measures ~110 ns; an idealised loop with no hashing or confirmation reaches 46 ns only because tiny iterations overlap in the out-of-order window, which ~850-instruction decisions cannot). Remaining build ideas, not yet done: bucket records during the scan (drop the 0.12 s scatter), compact dedupe output in place (0.09 s merge), cheaper scan lines (0.29 s), parallel placement rounds.

**Revised targets and the decision cache (2026-09-14, spec revision `92a8ece`):** the user accepted clean < 150 ns, cold blocked < 300 ns, repeated names < 50 ns on a Zipf workload through a per-worker decision cache, < 120 MB and < 2 s build. As built:

6. **Decision cache** (`engine/src/filter/decisions.rs`, `DecisionCache`): a direct-mapped table of `DEFAULT_SLOTS = 32,768` 64-octet slots plus one tag octet per slot (2.03 MiB per worker), owned by `server::WorkerCtx::filter_decisions` (`Cell`s, `!Sync`, allocated once with the worker). The slot is chosen by a keyed 64-bit hash of the name's zero-padded words (three independent multiplies); the tag array (hash bits 57..64) rejects most misses without reading the cold slot, which cut the Zipf mean from 161 to 135 ns at 16K slots. A hit requires the slot's owner tag, length and all name words to equal the query's, so a hash or slot collision can never return another name's decision; the hash itself is a function of the compared words and is not stored. Names above `CACHED_NAME_MAX = 48` wire octets bypass the cache. The owner tag is `FilterView::cache_owner() = index generation << 16 | view id`; view ids are assigned per index to each distinct (block mask, allow mask) pair, so equal views on a reused index keep their decisions and any new index build (new generation) invalidates everything; generation 0 (the empty index), generations past 48 bits and views past 65,535 masks are never cached. Measured alternatives: 2-way buckets added 1% hit rate and cost 5 ns per hit; 8K/16K/32K/65K slots gave 53/58/64/70% hits and 139/138/122/119 ns Zipf means (144 ns uncached), repeated names 31–33 ns at every size. `filter_index_matches_filterset_semantics` also checks every decision through a 16-slot cache.
7. **Build:** the merge interns thread set tables, renumbers, and writes uniques with final set ids in parallel into one allocation; `write_all` gives each thread a contiguous block range (no sequential position pass); large build buffers come from `storage::huge_vec` (THP-advised before first touch); the scatter writes into uninitialised capacity. 1.87 s -> 1.72 s on 2 threads, 1.24 s on 4.
8. **Benchmark:** `filter_bench` gained `--pin CPU`, `--v1` (the oracle `FilterSet` on the same samples, RSS growth as memory), and a Zipf workload (`--zipf 1000000 --zipf-queries 4000000 --zipf-s 1.0 --zipf-blocked 0.2 --cache-slots N`) reporting `zipf_ns` (through the cache), `zipf_hit_rate`, `zipf_uncached_ns`, `zipf_repeated_ns` (queries of names ranked within the first 1/16 of the slots) and `zipf_repeated_hit_rate`; queries are laid out contiguously in query order. `filter::oracle` is `#[doc(hidden)] pub` for it.

**Part A gate result (2026-09-14 12:45 UTC, one run, `worker-23`, decisions pinned to Cortex-A76 core 4, build threads floating on A76 cores 4–7, load average 1.7 from other agents):** `taskset -c 4-7 filter_bench --threads 2 --pin 4 --rounds 9 --v1 /work/lists/clean-*.txt`: 5,136,759 names, index 118,195,732 bytes (23.0 B/name), build 1.72 s on 2 threads (1.24 s on 4 in a second run), clean 103 ns, cold blocked 274 ns, Zipf 124 ns at 63.5% hits (147 ns uncached), repeated names 32.9 ns at 98.8% hits; v1 `FilterSet` in the same run: 260 MB RSS growth, 2.99 s, blocked 425 ns, clean 303 ns, Zipf 200 ns. Every revised target passes, so Tasks 6–8 proceed; `TestFilterIndexBudget` now budgets 2 s per 5.1M names.

### Decisions not settled by the spec

- **Index layout deviates from the spec's Data line** (`(u64 hash, u32 name offset, u32 list-set id)` slots plus a name arena): 16-byte slots alone take 96 MB at 5.1M names, leaving 4.7 bytes per name for names, so the 120 MB and 150 ns targets cannot both hold; blocks with inline packed names meet both on paper and are measured in Task 4 before integration.
- **OpenSearch mapping conflict:** kw's OpenSearch 3.8 maps `attributes.nexora.filter` as a text leaf, so a record carrying `nexora.filter.list_id` and `nexora.filter.category` would be rejected (`attributes.nexora.filter` cannot be both a value and an object). The engine sends the spec's attribute names unchanged; the collector's OpenSearch pipeline (`transform/querylog`) copies `nexora.filter` to `nexora.filter.result`, deletes `nexora.filter`, and writes to a new index prefix `nexora-querylog-v2`; the adapter's default pattern `nexora-querylog-*` reads both generations and matches `filter` on either field.
- **`commercial_use: false` covers OISD and URLhaus.** OISD's FAQ and repository state GPL-3.0; the spec requires the OISD non-commercial notice, so the catalog records `license: GPL-3.0` and `commercial_use: false` with the spec's notice. abuse.ch's Terms of Use (section 4) say commercial use may require a paid subscription, so URLhaus carries the same flag. The API guard applies to every `commercial_use: false` source (the spec's OISD rule is the case the tests pin).
- **Rolling upgrades:** management keeps filling the M1 `blocklists`/`allowlists` fields next to the new `*_refs`, so engines older than this change keep filtering; engines use the refs when present and synthesize `blob:<sha256>` ids (no category) otherwise.
- **Air-gapped mirror:** `NEXORA_CATALOG_MIRROR` (base URL) makes the fetcher download every catalog source from `<mirror>/<source key>`; it cannot add or change sources, and it is how e2e tests serve fixture lists.
- **Per-engine decision time on kw:** after each index build the engine measures 4,096 listed and 4,096 unlisted names on a thread pinned to its fastest core (Cortex-A76 on RK3588: CPU part `0xd0b`, cores 4–7) and exports `nexora_filter_index_decision_seconds{kind,cpu}` and `Stats.filter_index`, so `TestKwFilterCategories` records decision time and memory per engine.
- **`nexora_filter_blocked_total` gains the `category` label** (`custom` for lists without a category); a name listed in several categories counts once in each, so the sum over labels can exceed blocked queries; `Stats.filter_blocked_total` and OTLP `nexora.filter.blocked` keep the unlabeled count.
- **Catalog defaults:** categories start disabled; sources start enabled except the six listed in Task 9 (`blp-malware`, `ut1-adult`, `blp-facebook`, `blp-tiktok`, `blp-twitter`, `blp-fortnite`: large duplicates of other sources or single-brand lists), which keeps "every category on" at the measured 5.1M names.
- **Relative perf gate on the first PR:** the base commit has no benchmark CLI, so the filter gate step passes with a notice when `base/engine/examples/filter_bench.rs` lacks `--synthetic`.

### Spec coverage

| Spec item                                                                                   | Tasks             |
| ------------------------------------------------------------------------------------------- | ----------------- |
| S-1 shared index, list sets, views                                                          | 2, 3, 7           |
| S-2 one-pass hashing, batched prefetch, exact confirmation, no allocation                   | 2, 3, 7           |
| S-3 build off the query path, atomic swap, reuse                                            | 3, 7              |
| S-4 catalog, toggles, managed subscriptions, read-only                                      | 9, 10, 11, 13, 17 |
| S-5 UT1 archive members                                                                     | 12, 18            |
| S-6 attribution, query log list id and category                                             | 8, 14, 18         |
| S-7 GUI                                                                                     | 15, 16            |
| S-8 metrics, engine memory in the GUI                                                       | 8, 14, 16, 18     |
| S-9 performance gate and kw numbers                                                         | 4, 19, 20         |
| S-10 OISD notice, `acknowledge_license`, audit                                              | 9, 13, 15, 17     |
| `filter_index_matches_filterset_semantics`                                                  | 3                 |
| `filter_bench` on kw, `TestFilterIndexBudget`                                               | 4                 |
| `TestFilterIndexSharedAcrossGroups`                                                         | 7                 |
| `cache_hit_path_does_not_allocate`                                                          | 7                 |
| `unchanged_lists_reuse_index`                                                               | 7                 |
| `TestFilterCategories`, `TestFilterCategoryToggles`                                         | 17                |
| `TestUT1ArchiveMember`, `TestQueryLogCategoryAttribution`, `TestObservabilityMetricsTraces` | 18                |
| `TestGUICoverage`, `22-filter-categories.spec.ts`                                           | 16                |
| perf gate, `TestKwFilterCategories`                                                         | 19, 20            |

## Constraints

Copied from the spec (binding for every task):

- "Hot path rules from `docs/architecture.md` stay binding: no allocation, lock or log on the cache-hit path (`cache_hit_path_does_not_allocate` must keep passing with the new index)."
- "Targets on kw hardware (Cortex-A76 core, single thread) with the 5.1M-domain corpus: **< 150 ns per decision** for both blocked and clean names, **< 120 MB** index memory, build **< 1.5 s**; with 3 policy groups selecting different subsets, memory stays within 10% of the single-group index."
- "Exact matching: a hash collision must never block or allow a name that is not listed (confirmation against stored names)."
- "Semantics unchanged from v1: an entry blocks the domain and all subdomains; allowlist beats blocklist; per-client group lists replace global lists; CNAME cloaking checks CNAME targets."
- "Catalog ships all sources the user approved, including OISD; the GUI shows each source's license and attribution, and marks OISD as not free for commercial use (with a confirmation notice when enabled)."
- "The catalog is read-only for operators: no API or GUI path adds, edits or removes catalog sources; catalog changes ship with Nexora releases."
- "Categories are off by default on new installs."
- "Filter index memory cap: default 50% of the engine's cgroup memory limit, or 512 MB when the engine has no limit; overridable per engine group. A snapshot whose index would exceed the cap is rejected with a reason and the previous index stays active."
- "Existing M1–M5 acceptance tests keep passing unchanged."
- Out of scope: paid categorisation feeds, URL-path or TLS-SNI filtering, block pages, per-user policy, heuristic categorisation.

Project rules (from `docs/architecture.md` and earlier plans):

- Builds and tests run in the kw dev pod through `scripts/dev-exec.sh <cmd>` (it syncs first). `mgmt/internal/api/gen.go` and `web/src/api/schema.d.ts` are generated on the laptop with the `oapi-codegen` and `pnpm run gen:api` lines of `make proto`; `gen/go/nexora/control/v1/*.pb.go` are regenerated in the dev pod with its pinned plugins (protoc 3.21.12, protoc-gen-go v1.36.12, protoc-gen-go-grpc 1.6.2) and copied back. Commits run on the laptop.
- Rust toolchain 1.97.1, edition 2024, `cargo clippy -- -D warnings`; Go module `github.com/piwi3910/nexora`; YAML via `go.yaml.in/yaml/v3`; uuid `github.com/google/uuid`; DNS client `github.com/miekg/dns`.
- Proto fields added by this feature to existing messages use **600–699**; new messages number from 1.
- Migration file `mgmt/migrations/00503_filter_categories.sql` (next after `00502_installation.sql`).
- Playwright screen spec `web/e2e/screens/22-filter-categories.spec.ts` (next after 21); every new OpenAPI operation needs a covering browser request (`TestGUICoverage`), a permission in `mgmt/internal/auth/permissions.go` and the same role in `web/src/auth/permissions.ts`.
- E2E tests use fixture HTTP sources through `NEXORA_CATALOG_MIRROR`, never the internet; only `TestKwFilterCategories` uses the real catalog URLs.
- Every test that asserts "does not happen" first asserts the positive path in the same run.
- Metric names: `nexora_filter_index_entries`, `nexora_filter_index_bytes`, `nexora_filter_index_max_bytes`, `nexora_filter_index_build_seconds`, `nexora_filter_index_decision_seconds{kind="blocked|clean",cpu}`, `nexora_filter_blocked_total{category}`, management `nexora_mgmt_filter_category_stale{category}`. Query-log attributes `nexora.filter.list_id`, `nexora.filter.category`.
- Fixed identifiers: list id of the global allowlist blob `allowlist`; inline group allowlist list id `group-allow:<sha256 hex of the text>`; list id synthesized for refs-less blobs `blob:<sha256>`; catalog list names `catalog:<category>:<source>`; license error code `license_acknowledgement_required`; read-only error code `catalog_managed`; unknown source code `unknown_source`.

## Task 1: Settle the filter index and catalog design in docs/architecture.md

Files: `docs/architecture.md` (the binding how; its own rule says it changes before the code)
Interfaces: produces the names every later task uses: modules `engine/src/filter/{names,prefetch,storage,index,lists,synth,calibrate}.rs` and `engine/src/filter/oracle.rs` (test only), `mgmt/internal/catalog`, proto range 600–699, the metric and attribute names in Constraints, collector processor `transform/querylog` and index prefix `nexora-querylog-v2`.

- [ ] Run `grep -c "FilterIndex" docs/architecture.md` and expect FAIL with `0` (the document still describes the v1 `FxHashSet` matcher).
- [ ] In the repository layout block of `docs/architecture.md`, replace the line `  src/filter.rs                         blocklist/allowlist matcher, per-client policy (M2)` with:
  ```
    src/filter.rs                         block replies, rewrites, per-client policy (M2) on filter views
    src/filter/{names,prefetch,storage}.rs suffix hashing and 6-bit packing, cache prefetch, huge-page bytes
    src/filter/{index,lists,calibrate}.rs shared filter index and views, snapshot list collection, decision timing
    src/filter/synth.rs                   deterministic synthetic lists for tests and filter_bench
  ```
  and after `  internal/blocklist                    list fetcher/parser` add `  internal/catalog                      embedded filter category catalog (catalog.yaml) and its sync into filter_lists`.
- [ ] Replace the whole `### Filtering` section with:
  ```markdown
  ### Filtering

  - Blocklist content is fetched and normalised by the management plane and delivered as blobs:
    zstd-compressed UTF-8, one lowercase ASCII (punycode) domain per line, no trailing dot, sorted,
    unique. Identified by SHA-256 hex of the compressed bytes. `FilterListRef` carries each blob with
    its `list_id`, `category` (catalog key, empty for custom lists) and `position` (catalog order, then
    custom lists by name).
  - An entry blocks the domain and all subdomains. Allowlist beats blocklist. A client in a policy
    group gets only the group's lists.
  - One `filter::index::FilterIndex` per runtime holds every block and allow list of the snapshot
    (global, groups, categories, inline group allowlists as `group-allow:<sha256>` lists), names
    deduplicated, each with a list-set id (a deduplicated bitset over the snapshot's lists). Layout:
    128-byte blocks (count and overflow flag, 8-bit fingerprints, entry offsets, entries of
    `symbols`, LEB128 set id, 6-bit packed name written right to left by label), two candidate blocks
    per name from a seeded 64-bit fold-multiply suffix hash, a stash table for the few names both
    candidates cannot hold, a long-name arena for names above 64 characters. Fill 0.84; about
    21 bytes per name.
  - Lookup walks the query name once right to left, hashing and packing every suffix and prefetching
    both candidate blocks per suffix, then confirms candidates against the packed names from the
    longest suffix down. It never allocates. `FilterView` (per global or group policy) maps a set id
    to allow/block, the first matching list in position order (attribution) and category slot bits.
  - The index is built on the control runtime with up to four threads, swapped in with the runtime,
    and reused when every list id, kind and content hash is unchanged. Cap:
    `ConfigSnapshot.filter_index_max_bytes` when non-zero, else 50% of `/sys/fs/cgroup/memory.max`,
    else 512 MiB; a snapshot whose index and views exceed it is rejected and the previous runtime stays.
  - Answers whose CNAME chain reaches a blocked name are blocked.
  - Block response: `null_ip` (A 0.0.0.0 / AAAA :: , other types NODATA), `nxdomain`, or `refused`;
    TTL `block_ttl`.
  ```
- [ ] In `### Telemetry`, replace `` `nexora_cache_entries`, `nexora_cache_bytes`, `nexora_filter_blocked_total`, `` with `` `nexora_cache_entries`, `nexora_cache_bytes`, `nexora_filter_blocked_total{category}` (`custom` for lists without a category; a name in several categories counts in each), `nexora_filter_index_entries`, `nexora_filter_index_bytes`, `nexora_filter_index_max_bytes`, `nexora_filter_index_build_seconds`, `nexora_filter_index_decision_seconds{kind="blocked|clean",cpu}`, `` and in the query-log attribute list after `` `nexora.policy.group` (policy group id, empty for global clients), `` add `` `nexora.filter.list_id` and `nexora.filter.category` (blocked queries only: the first list, in position order, of the longest blocked suffix), ``.
- [ ] In `## Contract`, after the M5 bullet add:
  ```markdown
  - Filter categories: fields added to existing messages use 600-699: `FilterConfig.blocklist_refs`
    (600), `FilterConfig.allowlist_refs` (601), `PolicyGroup.blocklist_refs` (600),
    `ConfigSnapshot.filter_index_max_bytes` (600), `Stats.filter_index` (600); new messages
    `FilterListRef` and `FilterIndexStats`. Management keeps filling the M1 `blocklists`/`allowlists`
    blob fields for older engines; engines prefer the refs.
  ```
- [ ] In `## Management plane`, after the blocklist advisory-lock bullet add:
  ```markdown
  - The filter category catalog is `mgmt/internal/catalog/catalog.yaml`, embedded and read-only. At
    start every instance syncs it under `pg_advisory_xact_lock(hashtext('nexora:catalog'))` into
    `filter_categories` (enabled flag, default false) and one `filter_lists` row per source
    (`managed_by_catalog`, `category_key`, `source_key`, `archive_member`, `catalog_position`,
    `license_acknowledged_at`). Sources with `commercial_use: false` need `acknowledge_license: true`
    in the request that enables them. `NEXORA_CATALOG_MIRROR` (base URL) fetches every catalog source
    from `<mirror>/<source key>`. UT1 sources name a member of a `.tar.gz` archive; only that member
    is parsed.
  ```
- [ ] In the `## Management plane` bullet describing the OpenSearch backend, append these sentences:
  ```markdown
  The collector's OpenSearch pipeline runs `transform/querylog` (copies `nexora.filter` to
  `nexora.filter.result` and deletes `nexora.filter`, because OpenSearch cannot map `nexora.filter` as
  both a value and the parent of `nexora.filter.category`) and writes `nexora-querylog-v2-YYYY.MM.DD`;
  the adapter matches the `filter` parameter on either field.
  ```
- [ ] Run `grep -c "FilterIndex" docs/architecture.md; grep -c "nexora-querylog-v2" docs/architecture.md; grep -c "600-699" docs/architecture.md` and expect three non-zero counts.
- [ ] Commit: `git add docs/architecture.md && git commit -m "docs(architecture): shared filter index, category catalog and attribution design"`.

## Task 2: Suffix hashing, symbol packing, prefetch and huge-page storage

Files: `engine/src/filter/names.rs` (suffix hash, 6-bit packing, varints, candidates), `engine/src/filter/prefetch.rs` (stable cache prefetch per architecture), `engine/src/filter/storage.rs` (2 MiB-aligned, huge-page-advised zeroed bytes), `engine/src/filter.rs` (module declarations; `domain_to_wire` validates through `names::valid_label`)
Interfaces:

```rust
// crate::filter::names
pub const MAX_LEVELS: usize = 127;
pub const MAX_TEXT_LEN: usize = 253;
pub const PACKED_LEN: usize = 200;
pub const SEP: u8 = 0;
pub const INVALID: u8 = 0xFF;
pub static SYMBOL: [u8; 256];
pub fn fold(a: u64, b: u64) -> u64;
pub fn mix_label(parent: u64, label: &[u8]) -> u64;
pub fn fingerprint(hash: u64) -> u8;
pub fn candidates(hash: u64, blocks: u32) -> (u32, u32);
pub fn valid_label(label: &[u8]) -> bool;
pub fn text_hash(seed: u64, domain: &[u8]) -> Option<u64>;
pub fn text_name(seed: u64, domain: &[u8], packer: &mut Packer) -> Option<u64>;
pub fn pack_text(domain: &[u8], packer: &mut Packer);
pub fn packed_eq(entry: &[u8], query: &[u8], symbols: u8) -> bool;
pub fn write_varint(out: &mut [u8], v: u32) -> usize;
pub fn read_varint(bytes: &[u8]) -> (u32, usize);
pub fn varint_len(v: u32) -> usize;
pub struct Packer { /* buf: [u8; PACKED_LEN], bits: usize */ }
impl Packer { pub fn new() -> Packer; pub fn clear(&mut self); pub fn push(&mut self, symbol: u8); pub fn symbols(&self) -> usize; pub fn bytes(&self) -> &[u8]; pub fn buf(&self) -> &[u8; PACKED_LEN] }
pub struct Levels { pub packer: Packer, /* hashes, symbols, count */ }
impl Levels { pub fn new() -> Levels; pub fn count(&self) -> usize; pub fn hash(&self, level: usize) -> u64; pub fn symbols(&self, level: usize) -> u8 }
pub fn walk(seed: u64, name_wire: &[u8], out: &mut Levels, on_level: impl FnMut(u64));
#[cfg(test)] thread_local! { pub(crate) static FORCED_HASH: Cell<Option<u64>> }
// crate::filter::prefetch
pub fn prefetch_read(p: *const u8);
// crate::filter::storage
pub const BLOCK: usize = 128;
pub struct AlignedBytes;
impl AlignedBytes { pub fn zeroed(len: usize) -> AlignedBytes; pub fn len(&self) -> usize; pub fn as_ptr(&self) -> *const u8; pub fn as_slice(&self) -> &[u8]; pub fn as_mut_slice(&mut self) -> &mut [u8]; pub fn block(&self, i: u32) -> &[u8; BLOCK] }
```

- [ ] Add `pub mod names;`, `pub mod prefetch;` and `pub mod storage;` at the top of `engine/src/filter.rs`, and create `engine/src/filter/names.rs` containing only its test module:
  ```rust
  //! Suffix hashing and 6-bit symbol packing of DNS names for the filter index.

  #[cfg(test)]
  mod tests {
      use super::*;
      use crate::filter::domain_to_wire;

      const SEED: u64 = 0x5EED_0000_0000_0001;

      fn levels_of(wire: &[u8]) -> Levels {
          let mut l = Levels::new();
          walk(SEED, wire, &mut l, |_| {});
          l
      }

      #[test]
      fn suffix_hash_is_independent_of_the_prefix() {
          let full = domain_to_wire(b"www.ads.example.test").unwrap();
          let l = levels_of(&full);
          assert_eq!(l.count(), 4);
          for (level, suffix) in ["test", "example.test", "ads.example.test", "www.ads.example.test"]
              .iter()
              .enumerate()
          {
              let mut p = Packer::new();
              let h = text_name(SEED, suffix.as_bytes(), &mut p).unwrap();
              assert_eq!(l.hash(level), h, "hash of {suffix}");
              assert_eq!(text_hash(SEED, suffix.as_bytes()), Some(h));
              assert_eq!(usize::from(l.symbols(level)), suffix.len());
              assert!(packed_eq(p.bytes(), l.packer.buf(), l.symbols(level)), "packing of {suffix}");
          }
          let other = levels_of(&domain_to_wire(b"cdn.tracker.ads.example.test").unwrap());
          assert_eq!(other.hash(2), l.hash(2), "ads.example.test under another prefix");
          assert_ne!(other.hash(3), l.hash(3), "tracker.ads.example.test differs from www.ads.example.test");
      }

      #[test]
      fn text_names_follow_v1_validation_and_lowercase() {
          let lower = text_name(SEED, b"ads.example.test", &mut Packer::new());
          assert!(lower.is_some());
          assert_eq!(text_name(SEED, b"Ads.Example.TEST", &mut Packer::new()), lower);
          for bad in [&b""[..], b"-bad-.example", b"a..b", b"a b.test", b"a.test.", b"\xc3\xa9.test", b".test"] {
              assert_eq!(text_name(SEED, bad, &mut Packer::new()), None, "{bad:?}");
              assert!(domain_to_wire(bad).is_none(), "v1 agrees on {bad:?}");
          }
          let long_label = "a".repeat(64);
          assert_eq!(text_hash(SEED, long_label.as_bytes()), None);
          assert!(domain_to_wire(long_label.as_bytes()).is_none());
          assert!(text_hash(SEED, b"x_1.a-b.0").is_some() && domain_to_wire(b"x_1.a-b.0").is_some());
      }

      #[test]
      fn labels_with_octets_outside_the_list_alphabet_end_the_walk() {
          // A label "a.b" (a dot inside a label) and a label with a non-ASCII octet, left of a listable suffix.
          let mut wire = vec![3, b'a', b'.', b'b', 2, 0xC3, 0xA9];
          wire.extend_from_slice(&domain_to_wire(b"ads.example.test").unwrap());
          let l = levels_of(&wire);
          assert_eq!(l.count(), 3, "only the listable suffixes right of the odd labels");
          assert_eq!(Some(l.hash(2)), text_hash(SEED, b"ads.example.test"));
          let truncated = [3u8, b'w', b'w', b'w', 4, b't', b'e'];
          assert_eq!(levels_of(&truncated).count(), 0, "a name without its root octet yields nothing");
      }

      #[test]
      fn names_at_the_255_octet_limit_hash_and_pack_exactly() {
          let text = format!("{a}.{a}.{a}.{b}", a = "a".repeat(63), b = "b".repeat(61));
          assert_eq!(text.len(), 253);
          let wire = domain_to_wire(text.as_bytes()).unwrap();
          assert_eq!(wire.len(), 255);
          let l = levels_of(&wire);
          assert_eq!((l.count(), l.symbols(3)), (4, 253));
          let mut p = Packer::new();
          assert_eq!(Some(l.hash(3)), text_name(SEED, text.as_bytes(), &mut p));
          assert!(packed_eq(p.bytes(), l.packer.buf(), 253));
          let mut near = text.clone().into_bytes();
          near[0] = b'c';
          let mut q = Packer::new();
          text_name(SEED, &near, &mut q).unwrap();
          assert!(!packed_eq(q.bytes(), l.packer.buf(), 253), "the last packed symbol differs");
      }

      #[test]
      fn candidates_varints_and_forced_hashes() {
          for blocks in [1u32, 2, 3, 1000, 845_000] {
              for i in 0..10_000u64 {
                  let h = fold(i ^ 0xDEAD_BEEF, 0x9E37_79B9_7F4A_7C15);
                  let (b1, b2) = candidates(h, blocks);
                  assert!(b1 < blocks && b2 < blocks);
                  assert!(blocks == 1 || b1 != b2);
              }
          }
          let mut v = [0u8; 8];
          for x in [0u32, 1, 127, 128, 300, 16_383, 16_384, u32::MAX] {
              let n = write_varint(&mut v, x);
              assert_eq!((read_varint(&v), varint_len(x)), ((x, n), n));
          }
          FORCED_HASH.with(|c| c.set(Some(42)));
          let forced = text_hash(SEED, b"one.test");
          FORCED_HASH.with(|c| c.set(None));
          assert_eq!(forced, Some(42));
      }
  }
  ```
- [ ] Add to `engine/src/filter/storage.rs` (created with this test module only) and `engine/src/filter/prefetch.rs` (created empty but for its doc comment):
  ```rust
  #[cfg(test)]
  mod tests {
      use super::*;

      #[test]
      fn aligned_bytes_are_zeroed_writable_and_block_addressable() {
          let mut b = AlignedBytes::zeroed(3 * BLOCK + 5);
          assert_eq!(b.len(), 4 * BLOCK, "rounded up to whole blocks");
          assert!(b.as_slice().iter().all(|&x| x == 0));
          b.as_mut_slice()[2 * BLOCK + 7] = 9;
          assert_eq!(b.block(2)[7], 9);
          crate::filter::prefetch::prefetch_read(b.as_ptr());
          crate::filter::prefetch::prefetch_read(std::ptr::null());
          assert_eq!(AlignedBytes::zeroed(0).len(), BLOCK);
      }
  }
  ```
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib filter::` and expect FAIL with ``cannot find function `walk` in this scope``.
- [ ] Implement `engine/src/filter/names.rs` above the test module:
  ```rust
  use std::mem::MaybeUninit;

  pub const MAX_LEVELS: usize = 127;
  pub const MAX_TEXT_LEN: usize = 253;
  pub const PACKED_LEN: usize = 200;
  pub const SEP: u8 = 0;
  pub const INVALID: u8 = 0xFF;
  const K_LEN: u64 = 0xA076_1D64_78BD_642F;
  const K_CHUNK: u64 = 0xE703_7ED1_A0B4_28DB;
  const K_FP: u64 = 0x8EBC_6AF0_9C88_C6E3;

  /// The 6-bit symbol of an octet: a-z (and A-Z) 1..26, 0-9 27..36, '-' 37, '_' 38, else INVALID.
  pub static SYMBOL: [u8; 256] = {
      let mut t = [INVALID; 256];
      let mut i = 0;
      while i < 26 {
          t[b'a' as usize + i] = 1 + i as u8;
          t[b'A' as usize + i] = 1 + i as u8;
          i += 1;
      }
      let mut d = 0;
      while d < 10 {
          t[b'0' as usize + d] = 27 + d as u8;
          d += 1;
      }
      t[b'-' as usize] = 37;
      t[b'_' as usize] = 38;
      t
  };

  #[cfg(test)]
  thread_local! {
      /// Test hook: every suffix hash becomes this value (collision tests).
      pub(crate) static FORCED_HASH: std::cell::Cell<Option<u64>> = const { std::cell::Cell::new(None) };
  }

  #[inline(always)]
  fn finish(h: u64) -> u64 {
      #[cfg(test)]
      if let Some(forced) = FORCED_HASH.with(|c| c.get()) {
          return forced;
      }
      h
  }

  /// The xor of the low and high halves of the 128-bit product.
  #[inline(always)]
  pub fn fold(a: u64, b: u64) -> u64 {
      let r = u128::from(a) * u128::from(b);
      (r as u64) ^ ((r >> 64) as u64)
  }

  /// Absorbs one lowercase label into the hash of its parent suffix: the length first, then the
  /// zero-padded 8-byte little-endian chunks.
  #[inline(always)]
  pub fn mix_label(parent: u64, label: &[u8]) -> u64 {
      let mut h = fold(parent ^ K_LEN, label.len() as u64 ^ K_CHUNK);
      let mut chunks = label.chunks_exact(8);
      for c in &mut chunks {
          h = fold(h ^ u64::from_le_bytes([c[0], c[1], c[2], c[3], c[4], c[5], c[6], c[7]]), K_CHUNK);
      }
      let rest = chunks.remainder();
      if !rest.is_empty() {
          let mut tail = [0u8; 8];
          tail[..rest.len()].copy_from_slice(rest);
          h = fold(h ^ u64::from_le_bytes(tail), K_CHUNK);
      }
      h
  }

  /// The block directory fingerprint: the top byte of a multiply that mixes every hash bit.
  #[inline(always)]
  pub fn fingerprint(hash: u64) -> u8 {
      (hash.wrapping_mul(K_FP) >> 56) as u8
  }

  /// The two candidate blocks of a hash among `blocks` (distinct unless there is one block).
  #[inline(always)]
  pub fn candidates(hash: u64, blocks: u32) -> (u32, u32) {
      let n = u64::from(blocks);
      let b1 = (((hash >> 32) * n) >> 32) as u32;
      let b2 = (((hash & 0xFFFF_FFFF) * n) >> 32) as u32;
      if b2 != b1 {
          (b1, b2)
      } else if b1 + 1 < blocks {
          (b1, b1 + 1)
      } else {
          (b1, 0)
      }
  }

  /// v1's label rule: 1..=63 octets of [A-Za-z0-9-_], not starting or ending with '-'.
  pub fn valid_label(label: &[u8]) -> bool {
      !label.is_empty()
          && label.len() <= 63
          && label[0] != b'-'
          && label[label.len() - 1] != b'-'
          && label.iter().all(|b| b.is_ascii_alphanumeric() || *b == b'-' || *b == b'_')
  }

  pub struct Packer {
      buf: [u8; PACKED_LEN],
      bits: usize,
  }

  impl Default for Packer {
      fn default() -> Packer {
          Packer::new()
      }
  }

  impl Packer {
      pub fn new() -> Packer {
          Packer { buf: [0; PACKED_LEN], bits: 0 }
      }
      pub fn clear(&mut self) {
          self.buf[..self.bits.div_ceil(8)].fill(0);
          self.bits = 0;
      }
      #[inline(always)]
      pub fn push(&mut self, symbol: u8) {
          let byte = self.bits >> 3;
          let v = u16::from(symbol) << (self.bits & 7);
          self.buf[byte] |= v as u8;
          self.buf[byte + 1] |= (v >> 8) as u8;
          self.bits += 6;
      }
      pub fn symbols(&self) -> usize {
          self.bits / 6
      }
      pub fn bytes(&self) -> &[u8] {
          &self.buf[..self.bits.div_ceil(8)]
      }
      pub fn buf(&self) -> &[u8; PACKED_LEN] {
          &self.buf
      }
  }

  /// Hashes a text domain right to left with v1's validation, lowercasing each label.
  fn hash_text(seed: u64, domain: &[u8], mut packer: Option<&mut Packer>) -> Option<u64> {
      if domain.is_empty() || domain.len() > MAX_TEXT_LEN {
          return None;
      }
      if let Some(p) = packer.as_deref_mut() {
          p.clear();
      }
      let mut h = seed;
      let mut lower = [0u8; 63];
      for (i, label) in domain.rsplit(|&b| b == b'.').enumerate() {
          if !valid_label(label) {
              return None;
          }
          let l = &mut lower[..label.len()];
          for (d, s) in l.iter_mut().zip(label) {
              *d = s.to_ascii_lowercase();
          }
          if let Some(p) = packer.as_deref_mut() {
              if i > 0 {
                  p.push(SEP);
              }
              for &c in l.iter() {
                  p.push(SYMBOL[usize::from(c)]);
              }
          }
          h = finish(mix_label(h, l));
      }
      Some(h)
  }

  pub fn text_hash(seed: u64, domain: &[u8]) -> Option<u64> {
      hash_text(seed, domain, None)
  }

  pub fn text_name(seed: u64, domain: &[u8], packer: &mut Packer) -> Option<u64> {
      hash_text(seed, domain, Some(packer))
  }

  /// Packs an already validated text domain (build side).
  pub fn pack_text(domain: &[u8], packer: &mut Packer) {
      packer.clear();
      for (i, label) in domain.rsplit(|&b| b == b'.').enumerate() {
          if i > 0 {
              packer.push(SEP);
          }
          for &c in label {
              packer.push(SYMBOL[usize::from(c)]);
          }
      }
  }

  /// True when the first `symbols` packed symbols of `entry` and `query` are equal.
  #[inline(always)]
  pub fn packed_eq(entry: &[u8], query: &[u8], symbols: u8) -> bool {
      let bits = usize::from(symbols) * 6;
      let (full, rest) = (bits / 8, bits % 8);
      entry[..full] == query[..full] && (rest == 0 || (entry[full] ^ query[full]) & ((1u8 << rest) - 1) == 0)
  }

  pub fn write_varint(out: &mut [u8], mut v: u32) -> usize {
      let mut i = 0;
      loop {
          let b = (v & 0x7F) as u8;
          v >>= 7;
          if v == 0 {
              out[i] = b;
              return i + 1;
          }
          out[i] = b | 0x80;
          i += 1;
      }
  }

  #[inline(always)]
  pub fn read_varint(bytes: &[u8]) -> (u32, usize) {
      let mut v = 0u32;
      for (i, &b) in bytes.iter().take(5).enumerate() {
          v |= u32::from(b & 0x7F) << (7 * i);
          if b & 0x80 == 0 {
              return (v, i + 1);
          }
      }
      (v, 5)
  }

  pub fn varint_len(v: u32) -> usize {
      match v {
          0..=0x7F => 1,
          0x80..=0x3FFF => 2,
          0x4000..=0x1F_FFFF => 3,
          0x20_0000..=0xFFF_FFFF => 4,
          _ => 5,
      }
  }

  /// Per-suffix results of one walk; level 0 is the shortest listable suffix.
  pub struct Levels {
      hashes: [MaybeUninit<u64>; MAX_LEVELS],
      symbols: [u8; MAX_LEVELS],
      count: usize,
      pub packer: Packer,
  }

  impl Default for Levels {
      fn default() -> Levels {
          Levels::new()
      }
  }

  impl Levels {
      pub fn new() -> Levels {
          Levels { hashes: [MaybeUninit::uninit(); MAX_LEVELS], symbols: [0; MAX_LEVELS], count: 0, packer: Packer::new() }
      }
      pub fn count(&self) -> usize {
          self.count
      }
      #[inline(always)]
      pub fn hash(&self, level: usize) -> u64 {
          assert!(level < self.count, "level {level} of {}", self.count);
          // SAFETY: `walk` initialises hashes[0..count] before raising `count`.
          unsafe { self.hashes[level].assume_init() }
      }
      #[inline(always)]
      pub fn symbols(&self, level: usize) -> u8 {
          self.symbols[level]
      }
  }

  /// Walks a lowercase uncompressed wire name right to left, hashing and packing each suffix whose
  /// labels are all listable, and calls `on_level` with each suffix hash as soon as it is known.
  #[inline(always)]
  pub fn walk(seed: u64, name_wire: &[u8], out: &mut Levels, mut on_level: impl FnMut(u64)) {
      let mut starts = [0u8; MAX_LEVELS];
      let mut labels = 0;
      let mut p = 0usize;
      loop {
          let Some(&len) = name_wire.get(p) else { return };
          if len == 0 {
              break;
          }
          let end = p + 1 + usize::from(len);
          if end >= name_wire.len() || labels == MAX_LEVELS {
              return;
          }
          starts[labels] = p as u8;
          labels += 1;
          p = end;
      }
      let mut h = seed;
      for i in (0..labels).rev() {
          let s = usize::from(starts[i]);
          let label = &name_wire[s + 1..s + 1 + usize::from(name_wire[s])];
          if label.iter().any(|&c| SYMBOL[usize::from(c)] == INVALID) {
              break;
          }
          if out.count > 0 {
              out.packer.push(SEP);
          }
          for &c in label {
              out.packer.push(SYMBOL[usize::from(c)]);
          }
          h = finish(mix_label(h, label));
          out.hashes[out.count] = MaybeUninit::new(h);
          out.symbols[out.count] = out.packer.symbols() as u8;
          out.count += 1;
          on_level(h);
      }
  }
  ```
- [ ] Implement `engine/src/filter/prefetch.rs`:
  ```rust
  //! A cache prefetch hint that compiles on stable Rust: `_mm_prefetch` on x86_64 (stable; its
  //! `#[target_feature(enable = "sse")]` still needs an `unsafe` call although SSE is baseline),
  //! `PRFM` through `asm!` on aarch64 (`_prefetch` and `core::hint::prefetch_read` are unstable in
  //! 1.97), nothing elsewhere.

  #[inline(always)]
  pub fn prefetch_read(p: *const u8) {
      #[cfg(target_arch = "x86_64")]
      // SAFETY: SSE is a baseline x86_64 feature, and a prefetch hint never faults, even for an
      // unmapped address.
      unsafe {
          core::arch::x86_64::_mm_prefetch::<{ core::arch::x86_64::_MM_HINT_T0 }>(p.cast());
      }
      #[cfg(target_arch = "aarch64")]
      // SAFETY: PRFM only hints the data cache: it never faults, even for an unmapped address, and
      // changes no register, flag or memory.
      unsafe {
          core::arch::asm!("prfm pldl1keep, [{p}]", p = in(reg) p, options(nostack, readonly, preserves_flags));
      }
      #[cfg(not(any(target_arch = "x86_64", target_arch = "aarch64")))]
      let _ = p;
  }
  ```
- [ ] Implement `engine/src/filter/storage.rs` above its tests:
  ```rust
  //! Zeroed byte storage for filter index blocks: anonymous `mmap` advised for transparent huge pages
  //! before first touch on Linux (kw runs THP in `madvise` mode), a 2 MiB-aligned allocation elsewhere.

  use std::ptr::NonNull;

  pub const BLOCK: usize = 128;

  pub struct AlignedBytes {
      ptr: NonNull<u8>,
      len: usize,
  }

  // SAFETY: the bytes are owned plain data with no interior pointers.
  unsafe impl Send for AlignedBytes {}
  // SAFETY: shared access is read-only.
  unsafe impl Sync for AlignedBytes {}

  impl AlignedBytes {
      pub fn zeroed(len: usize) -> AlignedBytes {
          let len = len.max(BLOCK).next_multiple_of(BLOCK);
          #[cfg(target_os = "linux")]
          {
              // SAFETY: a fresh private anonymous mapping; the kernel zero-fills it on first touch.
              let ptr = unsafe {
                  libc::mmap(std::ptr::null_mut(), len, libc::PROT_READ | libc::PROT_WRITE, libc::MAP_PRIVATE | libc::MAP_ANONYMOUS, -1, 0)
              };
              assert!(ptr != libc::MAP_FAILED, "filter index mmap of {len} bytes failed");
              // SAFETY: advice on the mapping created above; failure only loses huge pages.
              unsafe { libc::madvise(ptr, len, libc::MADV_HUGEPAGE) };
              AlignedBytes { ptr: NonNull::new(ptr.cast()).expect("mmap returned null"), len }
          }
          #[cfg(not(target_os = "linux"))]
          {
              let layout = std::alloc::Layout::from_size_align(len, 2 << 20).expect("layout");
              // SAFETY: the layout has a non-zero size.
              let ptr = unsafe { std::alloc::alloc_zeroed(layout) };
              let Some(ptr) = NonNull::new(ptr) else { std::alloc::handle_alloc_error(layout) };
              AlignedBytes { ptr, len }
          }
      }
      pub fn len(&self) -> usize {
          self.len
      }
      pub fn is_empty(&self) -> bool {
          false
      }
      pub fn as_ptr(&self) -> *const u8 {
          self.ptr.as_ptr()
      }
      pub fn as_slice(&self) -> &[u8] {
          // SAFETY: ptr..ptr+len is one live zero-initialised allocation owned by self.
          unsafe { std::slice::from_raw_parts(self.ptr.as_ptr(), self.len) }
      }
      pub fn as_mut_slice(&mut self) -> &mut [u8] {
          // SAFETY: as above, and &mut self guarantees exclusive access.
          unsafe { std::slice::from_raw_parts_mut(self.ptr.as_ptr(), self.len) }
      }
      #[inline(always)]
      pub fn block(&self, i: u32) -> &[u8; BLOCK] {
          self.as_slice()[i as usize * BLOCK..][..BLOCK].try_into().expect("block size")
      }
  }

  impl Drop for AlignedBytes {
      fn drop(&mut self) {
          #[cfg(target_os = "linux")]
          // SAFETY: unmaps exactly the mapping created in `zeroed`.
          unsafe {
              libc::munmap(self.ptr.as_ptr().cast(), self.len);
          }
          #[cfg(not(target_os = "linux"))]
          // SAFETY: deallocates with the layout used in `zeroed`.
          unsafe {
              std::alloc::dealloc(self.ptr.as_ptr(), std::alloc::Layout::from_size_align(self.len, 2 << 20).expect("layout"));
          }
      }
  }
  ```
- [ ] In `engine/src/filter.rs`, replace the label check inside `domain_to_wire` with `names::valid_label(label)` (the loop body keeps pushing the lowercase label) and keep `MAX_TEXT_LEN` as `names::MAX_TEXT_LEN`.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib filter:: && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'` and expect `test result: ok` for the six new tests plus the existing `filter::policy_tests`, and no clippy warnings.
- [ ] Verify the x86_64 prefetch path compiles on stable (measured: without the `unsafe` block rustc 1.97 rejects the call with E0133, because `_mm_prefetch` carries `#[target_feature(enable = "sse")]`): `scripts/dev-exec.sh 'rustup target add x86_64-unknown-linux-gnu && rustc --edition 2024 --crate-type lib --target x86_64-unknown-linux-gnu -o /tmp/prefetch.rlib engine/src/filter/prefetch.rs'` and expect exit status 0 with no output.
- [ ] Commit: `git add engine/src/filter.rs engine/src/filter/names.rs engine/src/filter/prefetch.rs engine/src/filter/storage.rs && git commit -m "feat(engine): suffix hashing, 6-bit name packing, stable prefetch and huge-page storage"`.

## Task 3: FilterIndex, FilterView and the v1 oracle property test

Files: `engine/src/filter/index.rs` (build, blocks, stash, list sets, views, lookup), `engine/src/filter/oracle.rs` (v1 `FilterSet` verbatim, `#[cfg(test)]`, test oracle only), `engine/src/filter.rs` (declares `pub mod index;` and `#[cfg(test)] mod oracle;`)
Interfaces:

```rust
// crate::filter::index
pub const BLOCK_FILL: f64 = 0.84;
pub const MAX_LISTS: usize = 65_534;
pub static BUILDS: AtomicU64;
#[derive(Clone, Copy, Debug, PartialEq, Eq)] pub enum ListKind { Block, Allow }
pub struct ListInput<'a> { pub id: &'a str, pub category: &'a str, pub category_slot: u8, pub kind: ListKind, pub text: &'a [u8] }
#[derive(Clone, Debug)] pub struct ListMeta { pub id: Box<str>, pub category: Box<str>, pub category_slot: u8, pub kind: ListKind, pub invalid_lines: u64 }
#[derive(Clone, Copy, Debug)] pub struct IndexOptions { pub max_bytes: u64, pub threads: usize, pub seed: u64, pub fill: f64 }
impl IndexOptions { pub fn new(max_bytes: u64) -> IndexOptions }
#[derive(Debug, PartialEq, Eq, thiserror::Error)] pub enum IndexError { OverCap { needed: u64, cap: u64 }, TooManyLists(usize), Placement }
#[derive(Clone, Copy, Debug, PartialEq, Eq)] pub struct ListHit { pub list: u16, pub set: u32 }
#[derive(Clone, Copy, Debug, PartialEq, Eq)] pub enum FilterDecision { None, Blocked(ListHit), Allowed }
pub struct FilterIndex;
impl FilterIndex {
    pub fn build(lists: &[ListInput<'_>], opts: &IndexOptions) -> Result<FilterIndex, IndexError>;
    pub fn empty() -> FilterIndex;
    pub fn entries(&self) -> u64;
    pub fn invalid_lines(&self) -> u64;
    pub fn stash_entries(&self) -> u64;
    pub fn memory_bytes(&self) -> u64;
    pub fn build_seconds(&self) -> f64;
    pub fn generation(&self) -> u64;
    pub fn lists(&self) -> &[ListMeta];
    pub fn list_index(&self, id: &str) -> Option<u16>;
    pub fn view(self: &Arc<Self>, block: &[u16], allow: &[u16]) -> FilterView;
}
pub struct FilterView;
impl FilterView {
    pub fn decide(&self, name_wire: &[u8]) -> FilterDecision;
    pub fn cloaked(&self, cname_targets: &[crate::wire::NameKey]) -> Option<ListHit>;
    pub fn categories(&self, hit: ListHit) -> u64;
    pub fn memory_bytes(&self) -> u64;
    pub fn index(&self) -> &Arc<FilterIndex>;
    pub fn is_empty(&self) -> bool;
}
// crate::filter::oracle (cfg(test))
pub enum Decision { None, Blocked, Allowed }
pub struct FilterSet;
pub struct ListStats { pub entries: usize, pub invalid_lines: usize }
impl FilterSet { pub fn build(blocklists: &[Vec<u8>], allowlists: &[Vec<u8>], mode: BlockMode, ttl: u32) -> (FilterSet, ListStats); pub fn decide(&self, name_wire: &[u8]) -> Decision; pub fn blocks_exactly(&self, name_wire: &[u8]) -> bool }
```

- [ ] Create `engine/src/filter/oracle.rs` as a copy of today's `ListStats` and `FilterSet` (struct, `empty`, `build`, `decide`; without `write_block_reply`, `cloaked` and the `mode`/`ttl` fields, which the oracle never reads and `clippy -D warnings` rejects as dead code; `build` keeps its `mode, ttl` parameters) whose `decide` returns `pub enum Decision { None, Blocked, Allowed }` (derive `Clone, Copy, Debug, PartialEq, Eq`), plus `pub fn blocks_exactly(&self, name_wire: &[u8]) -> bool { self.blocked.contains(name_wire) }`; head the file with `//! The v1 matcher, kept only as the oracle for filter_index_matches_filterset_semantics.` and declare it in `engine/src/filter.rs` as `#[cfg(test)] mod oracle;`.
- [ ] Declare `pub mod index;` in `engine/src/filter.rs` and create `engine/src/filter/index.rs` with only this test module:
  ```rust
  #[cfg(test)]
  mod tests {
      use super::*;
      use crate::filter::BlockMode;
      use crate::filter::domain_to_wire;
      use crate::filter::oracle::{Decision, FilterSet};

      struct XorShift(u64);
      impl XorShift {
          fn next(&mut self) -> u64 {
              self.0 ^= self.0 << 13;
              self.0 ^= self.0 >> 7;
              self.0 ^= self.0 << 17;
              self.0
          }
          fn below(&mut self, n: usize) -> usize {
              (self.next() % n as u64) as usize
          }
      }

      fn opts() -> IndexOptions {
          IndexOptions { max_bytes: 64 << 20, threads: 2, seed: 7, fill: BLOCK_FILL }
      }

      /// Names over a tiny label alphabet, so exact names and suffixes repeat across lists.
      fn name(r: &mut XorShift) -> String {
          const LABELS: [&str; 10] = ["a", "b", "ab", "a-b", "x_1", "0", "test", "example", "cdn", "www"];
          let labels = 1 + r.below(4);
          (0..labels).map(|_| LABELS[r.below(LABELS.len())]).collect::<Vec<_>>().join(".")
      }

      fn list_text(r: &mut XorShift, lines: usize) -> Vec<u8> {
          let mut t = String::new();
          for _ in 0..lines {
              match r.below(20) {
                  0 => t.push_str("-bad-.example"),
                  1 => t.push_str("UPPER.Example"),
                  2 => {}
                  _ => t.push_str(&name(r)),
              }
              t.push('\n');
          }
          t.into_bytes()
      }

      fn subset(r: &mut XorShift, of: &[u16]) -> Vec<u16> {
          of.iter().copied().filter(|_| r.below(3) == 0).collect()
      }

      fn inputs<'a>(ids: &'a [String], texts: &'a [Vec<u8>], kinds: &[ListKind]) -> Vec<ListInput<'a>> {
          (0..texts.len())
              .map(|i| ListInput {
                  id: &ids[i],
                  category: if i % 2 == 0 { "ads" } else { "" },
                  category_slot: (i % 2) as u8,
                  kind: kinds[i],
                  text: &texts[i],
              })
              .collect()
      }

      /// The first list (by index) of the longest suffix any of `per_list` lists exactly.
      fn expected_list(per_list: &[(u16, FilterSet)], wire: &[u8]) -> u16 {
          let mut pos = 0;
          while wire[pos] != 0 {
              let suffix = &wire[pos..];
              if let Some((i, _)) = per_list.iter().filter(|(_, s)| s.blocks_exactly(suffix)).min_by_key(|(i, _)| *i) {
                  return *i;
              }
              pos += 1 + usize::from(wire[pos]);
          }
          panic!("blocked without a listed suffix");
      }

      #[test]
      fn filter_index_matches_filterset_semantics() {
          let mut r = XorShift(0x9E37_79B9_7F4A_7C15);
          for round in 0..40 {
              let list_count = if round % 4 == 3 { 70 } else { 1 + r.below(12) };
              let texts: Vec<Vec<u8>> = (0..list_count).map(|_| { let n = r.below(40); list_text(&mut r, n) }).collect();
              let kinds: Vec<ListKind> = (0..list_count).map(|_| if r.below(4) == 0 { ListKind::Allow } else { ListKind::Block }).collect();
              let ids: Vec<String> = (0..list_count).map(|i| format!("l{i}")).collect();
              let index = Arc::new(FilterIndex::build(&inputs(&ids, &texts, &kinds), &opts()).unwrap());
              let (_, all) = FilterSet::build(&texts, &[], BlockMode::NullIp, 60);
              assert_eq!(index.invalid_lines(), all.invalid_lines as u64, "invalid lines (round {round})");
              let block: Vec<u16> = (0..list_count as u16).filter(|&i| kinds[usize::from(i)] == ListKind::Block).collect();
              let allow: Vec<u16> = (0..list_count as u16).filter(|&i| kinds[usize::from(i)] == ListKind::Allow).collect();
              let mut views = vec![(block.clone(), allow.clone())];
              for _ in 0..3 {
                  views.push((subset(&mut r, &block), subset(&mut r, &allow)));
              }
              for (vb, va) in &views {
                  let view = index.view(vb, va);
                  let pick = |ls: &[u16]| ls.iter().map(|&i| texts[usize::from(i)].clone()).collect::<Vec<_>>();
                  let (oracle, _) = FilterSet::build(&pick(vb), &pick(va), BlockMode::NullIp, 60);
                  let per_list: Vec<(u16, FilterSet)> =
                      vb.iter().map(|&i| (i, FilterSet::build(&[texts[usize::from(i)].clone()], &[], BlockMode::NullIp, 60).0)).collect();
                  for _ in 0..400 {
                      let q = format!("{}{}", if r.below(2) == 0 { "www." } else { "" }, name(&mut r));
                      let wire = domain_to_wire(q.as_bytes()).unwrap();
                      match (oracle.decide(&wire), view.decide(&wire)) {
                          (Decision::None, FilterDecision::None) | (Decision::Allowed, FilterDecision::Allowed) => {}
                          (Decision::Blocked, FilterDecision::Blocked(hit)) => {
                              assert_eq!(hit.list, expected_list(&per_list, &wire), "attribution of {q} (round {round})");
                          }
                          (want, got) => panic!("{q} (round {round}, view {vb:?}/{va:?}): oracle {want:?}, index {got:?}"),
                      }
                  }
              }
          }
      }

      fn one_block_list(text: &[u8], o: IndexOptions) -> (Arc<FilterIndex>, FilterView) {
          let input = [ListInput { id: "l", category: "", category_slot: 0, kind: ListKind::Block, text }];
          let index = Arc::new(FilterIndex::build(&input, &o).unwrap());
          let view = index.view(&[0], &[]);
          (index, view)
      }

      fn w(s: &str) -> Box<[u8]> {
          domain_to_wire(s.as_bytes()).unwrap()
      }

      #[test]
      fn hash_collisions_are_confirmed_against_stored_names() {
          crate::filter::names::FORCED_HASH.with(|c| c.set(Some(0x0123_4567_89AB_CDEF)));
          let (index, view) = one_block_list(b"one.test\ntwo.test\nthree.test\n", IndexOptions { threads: 1, ..opts() });
          let blocked = [view.decide(&w("one.test")), view.decide(&w("x.two.test")), view.decide(&w("three.test"))];
          let unlisted = [view.decide(&w("four.test")), view.decide(&w("test")), view.decide(&w("onetest"))];
          crate::filter::names::FORCED_HASH.with(|c| c.set(None));
          assert_eq!(index.entries(), 3, "colliding names are all stored");
          assert!(blocked.iter().all(|d| matches!(d, FilterDecision::Blocked(_))), "{blocked:?}");
          assert!(unlisted.iter().all(|d| *d == FilterDecision::None), "{unlisted:?}");
      }

      #[test]
      fn more_than_64_lists_attribute_and_split_views() {
          let texts: Vec<Vec<u8>> = (0..100)
              .map(|i| {
                  let mut t = format!("n{i}.test\n");
                  if i == 70 || i == 99 {
                      t.push_str("shared.test\n");
                  }
                  t.into_bytes()
              })
              .collect();
          let ids: Vec<String> = (0..100).map(|i| format!("l{i}")).collect();
          let kinds = vec![ListKind::Block; 100];
          let index = Arc::new(FilterIndex::build(&inputs(&ids, &texts, &kinds), &opts()).unwrap());
          let high: Vec<u16> = (65..100).collect();
          let v = index.view(&high, &[]);
          assert!(matches!(v.decide(&w("a.shared.test")), FilterDecision::Blocked(ListHit { list: 70, .. })));
          assert!(matches!(index.view(&[99], &[]).decide(&w("shared.test")), FilterDecision::Blocked(ListHit { list: 99, .. })));
          assert!(matches!(v.decide(&w("n80.test")), FilterDecision::Blocked(ListHit { list: 80, .. })));
          assert_eq!(index.view(&[0, 1, 2], &[]).decide(&w("n80.test")), FilterDecision::None);
          assert_eq!(index.list_index("l70"), Some(70));
      }

      #[test]
      fn long_names_empty_lists_and_empty_views() {
          let long = format!("{a}.{a}.{a}.{b}", a = "a".repeat(63), b = "b".repeat(61));
          let (index, view) = one_block_list(format!("{long}\nshort.test\n").as_bytes(), opts());
          assert!(matches!(view.decide(&w(&long)), FilterDecision::Blocked(_)));
          let mut near = long.clone().into_bytes();
          near[0] = b'c';
          assert_eq!(view.decide(&domain_to_wire(&near).unwrap()), FilterDecision::None);
          assert!(matches!(view.decide(&w("short.test")), FilterDecision::Blocked(_)));
          assert_eq!(index.view(&[], &[]).decide(&w("short.test")), FilterDecision::None);
          let (empty, ev) = one_block_list(b"", opts());
          assert_eq!((empty.entries(), ev.decide(&w("short.test"))), (0, FilterDecision::None));
          assert_eq!(Arc::new(FilterIndex::empty()).view(&[], &[]).decide(&w("short.test")), FilterDecision::None);
      }

      #[test]
      fn the_stash_holds_overflow_and_lookups_find_it() {
          let mut r = XorShift(3);
          let mut text = String::new();
          let mut listed = Vec::new();
          for i in 0..20_000 {
              let n = format!("s{}-{i}.example{}.test", r.next() % 1_000_000, r.below(50));
              text.push_str(&n);
              text.push('\n');
              listed.push(n);
          }
          let (index, view) = one_block_list(text.as_bytes(), IndexOptions { fill: 0.99, ..opts() });
          assert!(index.stash_entries() > 0, "fill 0.99 overflows some blocks");
          for n in &listed {
              assert!(matches!(view.decide(&w(n)), FilterDecision::Blocked(_)), "{n}");
          }
          for i in 0..20_000 {
              assert_eq!(view.decide(&w(&format!("u{i}.example1.test"))), FilterDecision::None);
          }
      }

      #[test]
      fn over_cap_is_rejected_before_blocks_are_allocated() {
          let text: String = (0..10_000).map(|i| format!("name{i}.example.test\n")).collect();
          let input = [ListInput { id: "l", category: "", category_slot: 0, kind: ListKind::Block, text: text.as_bytes() }];
          match FilterIndex::build(&input, &IndexOptions { max_bytes: 4096, ..opts() }) {
              Err(IndexError::OverCap { needed, cap }) => assert!(needed > 4096 && cap == 4096, "{needed} {cap}"),
              other => panic!("expected OverCap, got {:?}", other.map(|i| i.entries())),
          }
          let before = BUILDS.load(Ordering::Relaxed);
          let ok = FilterIndex::build(&input, &opts()).unwrap();
          assert!(ok.generation() > before && ok.memory_bytes() < 64 << 20);
      }
  }
  ```
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib filter::index` and expect FAIL with ``cannot find type `IndexOptions` in this scope``.
- [ ] Implement the types and constants of the Interfaces block at the top of `engine/src/filter/index.rs` (`use super::names::{self, Levels, Packer}; use super::prefetch::prefetch_read; use super::storage::{AlignedBytes, BLOCK};`), with these private items: `const BLOCK_CAPACITY: usize = BLOCK - 1; const STASH_FILL: f64 = 0.5; const OVERFLOWED: u8 = 0x80; const COUNT_MASK: u8 = 0x3F; const LONG: u8 = 0xFF; const LONG_SYMBOLS: usize = 64; const STASHED: u32 = u32::MAX; const SCAN_CHUNK: usize = 16 << 20;`, `#[derive(Clone, Copy, Default)] struct Record { hash: u64, offset: u32, list: u16, len: u8, _pad: u8 }` (16 bytes; `fn text<'a>(&self, lists: &[ListInput<'a>]) -> &'a [u8]` returns `&lists[list].text[offset..offset + len]`), `#[derive(Clone, Copy)] struct Unique { hash: u64, set: u32, offset: u32, list: u16, len: u8 }` with the same `text`, and `IndexError` messages `filter index needs {needed} bytes, above the cap of {cap} bytes`, `{0} filter lists exceed 65534`, `filter index placement failed after 4 stash resizes`. `IndexOptions::new` uses `threads = available_parallelism().min(4)`, `fill = BLOCK_FILL` and a seed from `rand::rng().fill(&mut [u8; 8])` (`use rand::RngExt;`, as `server::Shared::with_recursor` does).
- [ ] Implement `SetTable` exactly:
  ```rust
  struct SetTable {
      words: usize,
      bits: Vec<u64>,
      counts: Vec<u32>,
      heads: rustc_hash::FxHashMap<u64, u32>,
      next: Vec<u32>,
  }

  impl SetTable {
      fn new(lists: usize) -> SetTable {
          let words = lists.div_ceil(64).max(1);
          // Set 0 is the empty set, never interned.
          SetTable { words, bits: vec![0; words], counts: vec![0], heads: Default::default(), next: vec![u32::MAX] }
      }
      fn len(&self) -> usize {
          self.counts.len()
      }
      fn get(&self, id: u32) -> &[u64] {
          &self.bits[id as usize * self.words..][..self.words]
      }
      fn intern(&mut self, set: &[u64]) -> u32 {
          let key = set.iter().fold(0x243F_6A88_85A3_08D3u64, |h, &w| names::fold(h ^ w, 0x9E37_79B9_7F4A_7C15));
          let mut id = self.heads.get(&key).copied().unwrap_or(u32::MAX);
          while id != u32::MAX {
              if self.get(id) == set {
                  self.counts[id as usize] += 1;
                  return id;
              }
              id = self.next[id as usize];
          }
          let new = self.counts.len() as u32;
          self.bits.extend_from_slice(set);
          self.counts.push(1);
          self.next.push(self.heads.insert(key, new).unwrap_or(u32::MAX));
          new
      }
      /// Renumbers sets by descending use so frequent sets get one-byte varints; returns old -> new.
      fn renumber(&mut self) -> Vec<u32> {
          let mut order: Vec<u32> = (1..self.len() as u32).collect();
          order.sort_by_key(|&id| std::cmp::Reverse(self.counts[id as usize]));
          let mut remap = vec![0u32; self.len()];
          let mut bits = vec![0u64; self.words];
          let mut counts = vec![0u32];
          for (new, &old) in order.iter().enumerate() {
              remap[old as usize] = new as u32 + 1;
              bits.extend_from_slice(self.get(old));
              counts.push(self.counts[old as usize]);
          }
          self.bits = bits;
          self.counts = counts;
          self.heads = Default::default();
          self.next = Vec::new();
          remap
      }
  }
  ```
- [ ] Implement `FilterIndex::build` as the eight steps of the Architecture's placement item, with these functions:
  - `fn scan_lists(lists, opts) -> (Vec<Vec<Record>>, Vec<u64>)`: split every list text into work items `(list, start, end)` of at most `SCAN_CHUNK` bytes ending after a `\n`; each item allocates one `Vec<Record>` with capacity equal to its newline count plus one and, for every non-empty line, pushes `Record { hash, offset, list, len }` when `names::text_hash(opts.seed, line)` is `Some`, else counts an invalid line for its list. With `opts.threads <= 1` items run inline on the calling thread (the `FORCED_HASH` test relies on it); otherwise `std::thread::scope` runs `opts.threads` workers that take items through an `AtomicUsize` cursor and store results in a `Vec<parking_lot::Mutex<Option<(Vec<Record>, u64)>>>`.
  - `fn sort_records(parts: Vec<Vec<Record>>, total: usize, threads: usize) -> Vec<Record>`: count records per top hash byte, allocate `vec![Record::default(); total]` once, scatter (dropping each part after it is copied), split the output into its 256 bucket slices with `split_at_mut`, and sort each slice by `(hash, list)` with `sort_unstable_by_key`, the buckets divided over `threads` scoped threads in chunks of `256.div_ceil(threads)`.
  - `fn dedupe(lists, records, sets, out: &mut Vec<Unique>)`: for each run of equal `hash`, for each record whose text is not ASCII-case-insensitively equal to an earlier record of the run, clear a reused `scratch: Vec<u64>` of `sets.words`, set bit `list` of every record in the rest of the run with the same text, and push `Unique { hash, set: sets.intern(&scratch), offset, list, len }` (`out` has capacity `records.len()`).
  - after `let remap = sets.renumber();` rewrite every `Unique.set` through `remap`.
  - `fn entry_len(symbols: usize, set: u32) -> usize` = `1 + varint_len(set) + 1 + 4` above `LONG_SYMBOLS`, else `1 + varint_len(set) + (6 * symbols).div_ceil(8)`; `sizes: Vec<u8>` holds `entry_len + 2` (directory bytes) per unique name; `long_bytes` is the packed size of every long name; `block_count = max(1, ceil(Σ sizes / (BLOCK_CAPACITY · opts.fill)))`; `needed = block_count·BLOCK + long_bytes + 8·sets.bits.len() + BLOCK`; return `OverCap { needed, cap: opts.max_bytes }` when `needed > opts.max_bytes` or `block_count > u32::MAX`, before any block allocation.
  - `fn pick(used: &[u8], b1: u32, b2: u32, size: u8) -> Option<u32>`: the less-used of the two that still has `used + size <= BLOCK_CAPACITY`, else the other if it fits, else `None`.
  - placement in `uniques` order (hash order): `chosen[i] = pick(..)` and `used[b] += size`; on `None`, `chosen[i] = STASHED`, push `i` to `overflow`, and set bits `b1` and `b2` in an `overflowed: Vec<u64>` bitset.
  - `fn place_stash(uniques, sizes, overflow) -> Result<(u32, Vec<u32>), IndexError>`: `count = max(1, ceil(Σ overflow sizes / (BLOCK_CAPACITY · STASH_FILL)))`; place every overflow name with `names::candidates(hash, count)` and `pick`; on the first failure double `count` and retry; after the fifth failed attempt return `IndexError::Placement`. Re-check the cap with `needed + stash_count·BLOCK`.
  - `fn group(chosen: impl Iterator<Item = u32>, blocks: usize) -> (Vec<u32>, Vec<u32>)`: counting sort into `starts` (length `blocks + 1`) and `members`.
  - write the long arena first, serially, into `long: Vec<u8>` (plus 8 zero bytes of slack) and keep `long_at: Vec<(u32, u32)>` of `(unique index, offset)` sorted by index (looked up with `binary_search_by_key`).
  - `fn write_block(dst: &mut [u8], members: &[u32], overflowed: bool, uniques, lists, long_at)` writes: `dst[0] = members.len() as u8 | (OVERFLOWED if overflowed)`; for member `k`, `dst[1 + k] = names::fingerprint(hash)` and `dst[1 + count + k] = pos as u8`; entry at `pos`: long names `[LONG, varint set, len, offset u32 LE]`, others `[len, varint set, packed]` where `packed` comes from `names::pack_text(text, &mut packer)`; `debug_assert!(pos <= BLOCK)`.
  - primary blocks: `AlignedBytes::zeroed(block_count * BLOCK)`, written by `opts.threads` scoped threads over `chunks_mut(block_count.div_ceil(threads) * BLOCK)`, each chunk handling its own block range; the stash the same way without overflow flags.
  - finish with `entries = uniques.len()`, `stash_entries = overflow.len()`, `lists` metadata (`ListMeta` from the inputs plus per-list `invalid_lines`), `by_id: FxHashMap<Box<str>, u16>`, `build_seconds` from an `Instant` taken at entry, `generation = BUILDS.fetch_add(1, Relaxed) + 1`, and `memory_bytes = blocks.len() + stash.len() + long.len() + 8·bits.len() + 4·counts.len() + 64·lists.len()`.
- [ ] Implement lookup and views:
  ```rust
  impl FilterIndex {
      #[inline(always)]
      fn scan_block(&self, block: &[u8; BLOCK], fp: u8, symbols: u8, query: &[u8]) -> Option<u32> {
          let count = usize::from(block[0] & COUNT_MASK);
          for k in 0..count {
              if block[1 + k] != fp {
                  continue;
              }
              let e = &block[usize::from(block[1 + count + k])..];
              if e[0] == symbols {
                  let (set, n) = names::read_varint(&e[1..]);
                  if names::packed_eq(&e[1 + n..], query, symbols) {
                      return Some(set);
                  }
              } else if e[0] == LONG {
                  let (set, n) = names::read_varint(&e[1..]);
                  if e[1 + n] == symbols {
                      let off = u32::from_le_bytes([e[2 + n], e[3 + n], e[4 + n], e[5 + n]]) as usize;
                      if names::packed_eq(&self.long[off..], query, symbols) {
                          return Some(set);
                      }
                  }
              }
          }
          None
      }

      /// Calls `visit` with the set id of every listed suffix of a lowercase wire name, longest
      /// first, until it returns true. Allocation-free.
      #[inline]
      fn for_each_match(&self, name_wire: &[u8], mut visit: impl FnMut(u32) -> bool) {
          if self.entries == 0 {
              return;
          }
          let mut levels = Levels::new();
          let (base, n) = (self.blocks.as_ptr(), self.block_count);
          names::walk(self.seed, name_wire, &mut levels, |h| {
              let (b1, b2) = names::candidates(h, n);
              let p1 = base.wrapping_add(b1 as usize * BLOCK);
              let p2 = base.wrapping_add(b2 as usize * BLOCK);
              prefetch_read(p1);
              prefetch_read(p1.wrapping_add(64));
              prefetch_read(p2);
              prefetch_read(p2.wrapping_add(64));
          });
          let query = levels.packer.buf();
          for level in (0..levels.count()).rev() {
              let h = levels.hash(level);
              let (symbols, fp) = (levels.symbols(level), names::fingerprint(h));
              let (b1, b2) = names::candidates(h, n);
              let (x1, x2) = (self.blocks.block(b1), self.blocks.block(b2));
              let mut hit = self.scan_block(x1, fp, symbols, query).or_else(|| self.scan_block(x2, fp, symbols, query));
              if hit.is_none() && (x1[0] | x2[0]) & OVERFLOWED != 0 {
                  let (s1, s2) = names::candidates(h, self.stash_count);
                  hit = self
                      .scan_block(self.stash.block(s1), fp, symbols, query)
                      .or_else(|| self.scan_block(self.stash.block(s2), fp, symbols, query));
              }
              if let Some(set) = hit
                  && visit(set)
              {
                  return;
              }
          }
      }
  }

  impl FilterView {
      #[inline]
      pub fn decide(&self, name_wire: &[u8]) -> FilterDecision {
          if self.empty {
              return FilterDecision::None;
          }
          let mut decision = FilterDecision::None;
          self.index.for_each_match(name_wire, |set| {
              let class = self.class[set as usize];
              if class & 2 != 0 {
                  decision = FilterDecision::Allowed;
                  return true;
              }
              if class & 1 != 0 && decision == FilterDecision::None {
                  decision = FilterDecision::Blocked(ListHit { list: self.first_list[set as usize], set });
                  return !self.has_allow;
              }
              false
          });
          decision
      }

      pub fn cloaked(&self, cname_targets: &[crate::wire::NameKey]) -> Option<ListHit> {
          cname_targets.iter().find_map(|t| match self.decide(t.as_wire()) {
              FilterDecision::Blocked(hit) => Some(hit),
              _ => None,
          })
      }

      #[inline]
      pub fn categories(&self, hit: ListHit) -> u64 {
          self.categories[hit.set as usize]
      }
  }
  ```
  `FilterIndex::view` builds `block_mask`/`allow_mask` bitsets of `sets.words` words and, for every set id from 1, `class` (bit 0: set ∩ block ≠ ∅, bit 1: set ∩ allow ≠ ∅), `first_list` (lowest list index of set ∩ block, `u16::MAX` when none) and `categories` (OR of `1 << lists[l].category_slot` over set ∩ block); `has_allow = !allow.is_empty()`, `empty = block.is_empty() && allow.is_empty()`, `memory_bytes = 11 · sets`. `FilterIndex::empty()` is a one-block index with `entries = 0` and generation 0.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib filter:: && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'` and expect `test result: ok` including `filter_index_matches_filterset_semantics`, `hash_collisions_are_confirmed_against_stored_names`, `more_than_64_lists_attribute_and_split_views`, `long_names_empty_lists_and_empty_views`, `the_stash_holds_overflow_and_lookups_find_it` and `over_cap_is_rejected_before_blocks_are_allocated`.
- [ ] Mutation check: temporarily change `class & 2 != 0` to `class & 2 != 0 && decision == FilterDecision::None` in `FilterView::decide`, run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib filter::index::tests::filter_index_matches_filterset_semantics` and expect FAIL with `oracle Allowed, index Blocked`; restore the line.
- [ ] Commit: `git add engine/src/filter.rs engine/src/filter/index.rs engine/src/filter/oracle.rs && git commit -m "feat(engine): shared filter index with list sets, views and v1 oracle property test"`.

## Task 4: Permanent filter benchmark, synthetic corpus, CI budget test and the kw measurement gate

Files: `engine/src/filter/synth.rs` (deterministic corpus), `engine/src/filter.rs` (`pub mod synth;`), `engine/examples/filter_bench.rs` (replaces the throwaway spike), `engine/tests/filter_index_budget.rs` (`TestFilterIndexBudget`), `bench/filter/corpus-5m.tsv` (the default catalog selection as download list), `scripts/filter-corpus.sh` (downloads and normalises it), `.github/workflows/ci.yml` (release run of the budget test), `docs/operations.md` (recorded kw numbers)
Interfaces: `pub fn nexora_engine::filter::synth::synthetic_lists(names: usize, lists: usize, seed: u64) -> Vec<Vec<u8>>`; `filter_bench [--synthetic N] [--lists N] [--seed S] [--threads N] [--fill F] [--rounds N] [--json PATH] [FILES...]` printing and writing `{"unique_names","invalid_lines","index_bytes","bytes_per_name","stash_entries","build_seconds","blocked_ns","clean_ns"}` (consumed by Task 19's `perfgate filter-compare`); `scripts/filter-corpus.sh <dir>` writes `<dir>/<source key>.txt`.

- [ ] Write the failing test `engine/tests/filter_index_budget.rs`:
  ```rust
  //! Filter index budgets on a synthetic corpus shaped like the default catalog selection.
  use nexora_engine::filter::index::{FilterIndex, IndexOptions, ListInput, ListKind};
  use nexora_engine::filter::synth::synthetic_lists;
  use std::time::Instant;

  /// 120 MB for the 5.1M-name corpus.
  const BYTES_PER_NAME: f64 = 120e6 / 5.1e6;
  /// 1.5 s for the 5.1M-name corpus.
  const SECONDS_PER_NAME: f64 = 1.5 / 5.1e6;

  #[allow(non_snake_case)]
  #[test]
  fn TestFilterIndexBudget() {
      let names = 1_000_000;
      let texts = synthetic_lists(names, 21, 1);
      let chars: usize = texts.iter().map(|t| t.len()).sum();
      let lines: usize = texts.iter().map(|t| t.iter().filter(|&&b| b == b'\n').count()).sum();
      let mean = (chars - lines) as f64 / lines as f64;
      assert!((17.0..19.5).contains(&mean), "synthetic names average {mean:.1} characters, the corpus 18.0");
      let ids: Vec<String> = (0..texts.len()).map(|i| format!("synthetic-{i}")).collect();
      let inputs: Vec<ListInput<'_>> = texts
          .iter()
          .zip(&ids)
          .map(|(text, id)| ListInput { id, category: "", category_slot: 0, kind: ListKind::Block, text })
          .collect();
      let opts = IndexOptions { threads: 2, ..IndexOptions::new(512 << 20) };
      let mut best = f64::MAX;
      let mut last = None;
      for _ in 0..3 {
          let t = Instant::now();
          let built = FilterIndex::build(&inputs, &opts).expect("build");
          best = best.min(t.elapsed().as_secs_f64());
          last = Some(built);
      }
      let index = last.expect("built");
      let unique = index.entries() as f64;
      assert!(unique > 0.99 * names as f64, "synthetic names are unique: {unique}");
      let per_name = index.memory_bytes() as f64 / unique;
      assert!(per_name < BYTES_PER_NAME, "{per_name:.2} bytes per name, budget {BYTES_PER_NAME:.2}");
      if cfg!(debug_assertions) {
          eprintln!("debug build: build time {best:.3} s is checked by the --release CI step");
          return;
      }
      let budget = SECONDS_PER_NAME * unique;
      assert!(best < budget, "build {best:.3} s, budget {budget:.3} s");
  }
  ```
- [ ] Run `scripts/dev-exec.sh cargo test --locked --release -p nexora-engine --test filter_index_budget` and expect FAIL with `` could not find `synth` in `filter` ``.
- [ ] Create `engine/src/filter/synth.rs` and declare `pub mod synth;` in `engine/src/filter.rs`:
  ```rust
  //! Deterministic synthetic block lists shaped like the default catalog selection: 77% two-label,
  //! 19% three-label and 4% four-label names, about 18 characters on average; 10% of the names are
  //! also in a second list.

  const TLDS: [&str; 16] = ["com", "net", "org", "de", "ru", "info", "xyz", "top", "io", "nl", "fr", "uk", "cn", "br", "online", "site"];
  const EDGE: &[u8] = b"abcdefghijklmnopqrstuvwxyz0123456789";
  const INNER: &[u8] = b"abcdefghijklmnopqrstuvwxyz0123456789-";

  struct Rng(u64);

  impl Rng {
      fn next(&mut self) -> u64 {
          self.0 ^= self.0 << 13;
          self.0 ^= self.0 >> 7;
          self.0 ^= self.0 << 17;
          self.0
      }
      fn below(&mut self, n: usize) -> usize {
          (self.next() % n as u64) as usize
      }
      fn label(&mut self, len: usize, out: &mut Vec<u8>) {
          for i in 0..len {
              let set = if i == 0 || i + 1 == len { EDGE } else { INNER };
              out.push(set[self.below(set.len())]);
          }
      }
  }

  pub fn synthetic_lists(names: usize, lists: usize, seed: u64) -> Vec<Vec<u8>> {
      let mut rng = Rng(seed.wrapping_mul(0x9E37_79B9_7F4A_7C15) | 1);
      let mut out = vec![Vec::with_capacity(names * 20 / lists.max(1)); lists.max(1)];
      let mut name = Vec::with_capacity(64);
      for _ in 0..names {
          name.clear();
          let labels = match rng.below(100) {
              0..=76 => 2,
              77..=95 => 3,
              _ => 4,
          };
          for _ in 2..labels {
              let len = 2 + rng.below(7);
              rng.label(len, &mut name);
              name.push(b'.');
          }
          let len = 6 + rng.below(14);
          rng.label(len, &mut name);
          name.push(b'.');
          name.extend_from_slice(TLDS[rng.below(TLDS.len())].as_bytes());
          name.push(b'\n');
          let first = rng.below(out.len());
          out[first].extend_from_slice(&name);
          if rng.below(10) == 0 {
              let second = rng.below(out.len());
              out[second].extend_from_slice(&name);
          }
      }
      out
  }
  ```
- [ ] Replace `engine/examples/filter_bench.rs` with:
  ```rust
  //! Filter index benchmark: build time, index memory and single-thread decision time.
  //!   filter_bench --synthetic 1000000 --json bench.json
  //!   taskset -c 4,5 filter_bench --threads 2 --json kw.json /work/filter-corpus/*.txt
  //! Decision time is the median over --rounds passes of up to 20,000 blocked names ("www." plus
  //! every 97th listed name) and 20,000 clean names (cdnN.imgN.siteN.exampleN.com, unlisted ones only).
  use clap::Parser;
  use nexora_engine::filter::domain_to_wire;
  use nexora_engine::filter::index::{FilterDecision, FilterIndex, IndexOptions, ListInput, ListKind};
  use nexora_engine::filter::synth::synthetic_lists;
  use std::path::PathBuf;
  use std::sync::Arc;
  use std::time::Instant;

  #[derive(Parser)]
  struct Args {
      /// Generate this many names instead of reading list files.
      #[arg(long)]
      synthetic: Option<usize>,
      #[arg(long, default_value_t = 21)]
      lists: usize,
      #[arg(long, default_value_t = 1)]
      seed: u64,
      #[arg(long, default_value_t = 4)]
      threads: usize,
      #[arg(long, default_value_t = nexora_engine::filter::index::BLOCK_FILL)]
      fill: f64,
      #[arg(long, default_value_t = 5)]
      rounds: usize,
      #[arg(long)]
      json: Option<PathBuf>,
      files: Vec<PathBuf>,
  }

  #[derive(serde::Serialize)]
  struct Report {
      unique_names: u64,
      invalid_lines: u64,
      index_bytes: u64,
      bytes_per_name: f64,
      stash_entries: u64,
      build_seconds: f64,
      blocked_ns: f64,
      clean_ns: f64,
  }

  fn median(mut v: Vec<f64>) -> f64 {
      v.sort_by(f64::total_cmp);
      v[v.len() / 2]
  }

  fn main() {
      let args = Args::parse();
      let texts: Vec<Vec<u8>> = match args.synthetic {
          Some(n) => synthetic_lists(n, args.lists, args.seed),
          None => args.files.iter().map(|f| std::fs::read(f).unwrap_or_else(|e| panic!("{}: {e}", f.display()))).collect(),
      };
      assert!(!texts.is_empty(), "no lists: pass --synthetic N or list files");
      let ids: Vec<String> = (0..texts.len()).map(|i| format!("list-{i}")).collect();
      let inputs: Vec<ListInput<'_>> = texts
          .iter()
          .zip(&ids)
          .map(|(text, id)| ListInput { id, category: "", category_slot: 0, kind: ListKind::Block, text })
          .collect();
      let opts = IndexOptions { threads: args.threads, fill: args.fill, ..IndexOptions::new(4 << 30) };
      let mut builds = Vec::new();
      let mut last = None;
      for _ in 0..3 {
          let t = Instant::now();
          let built = FilterIndex::build(&inputs, &opts).expect("build");
          builds.push(t.elapsed().as_secs_f64());
          last = Some(built);
      }
      let index = Arc::new(last.expect("built"));
      let all: Vec<u16> = (0..texts.len() as u16).collect();
      let view = index.view(&all, &[]);
      let blocked: Vec<Box<[u8]>> = texts
          .iter()
          .flat_map(|t| t.split(|&b| b == b'\n'))
          .filter(|l| !l.is_empty())
          .step_by(97)
          .filter_map(|l| {
              let mut n = b"www.".to_vec();
              n.extend_from_slice(l);
              domain_to_wire(&n)
          })
          .filter(|q| matches!(view.decide(q), FilterDecision::Blocked(_)))
          .take(20_000)
          .collect();
      let mut state = 0x9E37_79B9_7F4A_7C15u64;
      let mut rnd = move || {
          state ^= state << 13;
          state ^= state >> 7;
          state ^= state << 17;
          state
      };
      let clean: Vec<Box<[u8]>> = (0..20_000)
          .map(|_| format!("cdn{}.img{}.site{}.example{}.com", rnd() % 1000, rnd() % 1000, rnd() % 100_000, rnd() % 1000))
          .filter_map(|n| domain_to_wire(n.as_bytes()))
          .filter(|q| view.decide(q) == FilterDecision::None)
          .collect();
      assert!(blocked.len() >= 1_000 && clean.len() >= 19_000, "samples: {} blocked, {} clean", blocked.len(), clean.len());
      let time = |names: &[Box<[u8]>]| {
          let per_round = (0..args.rounds.max(1))
              .map(|_| {
                  let t = Instant::now();
                  for q in names {
                      std::hint::black_box(view.decide(std::hint::black_box(q)));
                  }
                  t.elapsed().as_nanos() as f64 / names.len() as f64
              })
              .collect();
          median(per_round)
      };
      let report = Report {
          unique_names: index.entries(),
          invalid_lines: index.invalid_lines(),
          index_bytes: index.memory_bytes() + view.memory_bytes(),
          bytes_per_name: (index.memory_bytes() + view.memory_bytes()) as f64 / index.entries().max(1) as f64,
          stash_entries: index.stash_entries(),
          build_seconds: median(builds),
          blocked_ns: time(&blocked),
          clean_ns: time(&clean),
      };
      let json = serde_json::to_string_pretty(&report).expect("json");
      println!("{json}");
      if let Some(p) = &args.json {
          std::fs::write(p, json).unwrap_or_else(|e| panic!("{}: {e}", p.display()));
      }
  }
  ```
- [ ] Create `bench/filter/corpus-5m.tsv` (tab-separated `key`, `url`, `archive member or -`; the default catalog selection of Task 9, 5,112,325 unique names on 2026-09-14):
  ```
  # key	url	member
  hagezi-tif	https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/tif-onlydomains.txt	-
  urlhaus	https://urlhaus.abuse.ch/downloads/hostfile/	-
  blp-ransomware	https://blocklistproject.github.io/Lists/alt-version/ransomware-nl.txt	-
  ut1-malware	https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz	blacklists/malware/domains
  blp-phishing	https://blocklistproject.github.io/Lists/alt-version/phishing-nl.txt	-
  blp-fraud	https://blocklistproject.github.io/Lists/alt-version/fraud-nl.txt	-
  blp-scam	https://blocklistproject.github.io/Lists/alt-version/scam-nl.txt	-
  hagezi-fake	https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/fake-onlydomains.txt	-
  ut1-phishing	https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz	blacklists/phishing/domains
  hagezi-pro	https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/pro-onlydomains.txt	-
  oisd-big	https://big.oisd.nl/domainswild	-
  blp-ads	https://blocklistproject.github.io/Lists/alt-version/ads-nl.txt	-
  blp-tracking	https://blocklistproject.github.io/Lists/alt-version/tracking-nl.txt	-
  hagezi-nsfw	https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/nsfw-onlydomains.txt	-
  oisd-nsfw	https://nsfw.oisd.nl/domainswild	-
  blp-porn	https://blocklistproject.github.io/Lists/alt-version/porn-nl.txt	-
  stevenblack-porn	https://raw.githubusercontent.com/StevenBlack/hosts/master/alternates/porn-only/hosts	-
  hagezi-gambling	https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/gambling-onlydomains.txt	-
  blp-gambling	https://blocklistproject.github.io/Lists/alt-version/gambling-nl.txt	-
  stevenblack-gambling	https://raw.githubusercontent.com/StevenBlack/hosts/master/alternates/gambling-only/hosts	-
  ut1-gambling	https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz	blacklists/gambling/domains
  hagezi-social	https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/social-onlydomains.txt	-
  stevenblack-social	https://raw.githubusercontent.com/StevenBlack/hosts/master/alternates/social-only/hosts	-
  ut1-social-networks	https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz	blacklists/social_networks/domains
  blp-crypto	https://blocklistproject.github.io/Lists/alt-version/crypto-nl.txt	-
  ut1-cryptojacking	https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz	blacklists/cryptojacking/domains
  hagezi-anti-piracy	https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/anti.piracy-onlydomains.txt	-
  blp-piracy	https://blocklistproject.github.io/Lists/alt-version/piracy-nl.txt	-
  ut1-warez	https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz	blacklists/warez/domains
  blp-drugs	https://blocklistproject.github.io/Lists/alt-version/drugs-nl.txt	-
  ut1-drogue	https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz	blacklists/drogue/domains
  stevenblack-fakenews	https://raw.githubusercontent.com/StevenBlack/hosts/master/alternates/fakenews-only/hosts	-
  ut1-fakenews	https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz	blacklists/fakenews/domains
  ut1-dating	https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz	blacklists/dating/domains
  ut1-games	https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz	blacklists/games/domains
  ```
- [ ] Create `scripts/filter-corpus.sh` (mode 0755):
  ```bash
  #!/usr/bin/env bash
  # Download the default catalog selection (bench/filter/corpus-5m.tsv) as one normalised domain list
  # per source for filter_bench: comments, hosts sink addresses, "*." prefixes and CRs removed,
  # lower-cased, unique. The UT1 archive is downloaded once.
  #   scripts/filter-corpus.sh /work/filter-corpus
  set -euo pipefail
  out="${1:?usage: $0 <dir>}"
  tsv="$(cd "$(dirname "$0")/.." && pwd)/bench/filter/corpus-5m.tsv"
  mkdir -p "$out"
  ut1="$out/.ut1.tar.gz"
  while IFS=$'\t' read -r key url member; do
  	case "$key" in '' | '#'*) continue ;; esac
  	if [ "$member" != "-" ]; then
  		[ -s "$ut1" ] || curl -fsSL --retry 3 -o "$ut1" "$url"
  		tar -xzOf "$ut1" "$member"
  	else
  		curl -fsSL --retry 3 "$url"
  	fi | tr -d '\r' |
  		sed -E -e 's/[[:space:]]*#.*$//' -e 's/^(0\.0\.0\.0|127\.0\.0\.1)[[:space:]]+//' -e 's/^\*\.//' |
  		tr 'A-Z' 'a-z' | awk 'NF == 1' | sort -u >"$out/$key.txt"
  	echo "$key $(wc -l <"$out/$key.txt")"
  done <"$tsv"
  ```
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked --release -p nexora-engine --test filter_index_budget && cargo run --locked --release -p nexora-engine --example filter_bench -- --synthetic 1000000 --rounds 3'` and expect `test TestFilterIndexBudget ... ok` and a JSON report with `"bytes_per_name"` below `23.53`.
- [ ] Add to the `engine` job of `.github/workflows/ci.yml`, directly after the `cargo test --locked -p nexora-engine --all-targets` step, a step `- name: Filter index budget (release)` running `cargo test --locked --release -p nexora-engine --test filter_index_budget`.
- [ ] Measure on kw (RK3588: cores 4–7 are Cortex-A76, CPU part `0xd0b`; the engine pods have a 2-CPU limit, hence two build threads): run `scripts/dev-exec.sh 'command -v taskset && grep -c 0xd0b /proc/cpuinfo && scripts/filter-corpus.sh /work/filter-corpus && cargo build --locked --release -p nexora-engine --example filter_bench && taskset -c 4,5 "${CARGO_TARGET_DIR:-target}/release/examples/filter_bench" --threads 2 --rounds 9 --json /work/filter-bench-kw.json /work/filter-corpus/*.txt'` and expect `4` A76 cores, `unique_names` about 5.1M, and `blocked_ns` < 150, `clean_ns` < 150, `index_bytes` < 120000000 and `build_seconds` < 1.5. This is the integration gate: Task 6 does not start while any of the four numbers misses; the plan is then revised with the measured numbers before building on the design. Measured on 2026-09-14 against the existing dev-pod corpus `/work/lists/clean-*.txt` (5,136,759 names) instead of a fresh download: see "Filter index design as built and measured in Task 4" — memory and clean met, blocked and build missed.
- [ ] Record the kw numbers in `docs/operations.md` under a new section `## Filter index performance` as a table with the columns `date`, `commit`, `unique names`, `index bytes`, `bytes per name`, `build (2 threads)`, `blocked ns`, `clean ns`, `stash entries`, followed by the v1 baseline row from the spec (5.1M: 2.8 s, 309 MB, 400 ns, 285 ns) and the command above.
- [ ] Commit: `git add engine/src/filter.rs engine/src/filter/synth.rs engine/examples/filter_bench.rs engine/tests/filter_index_budget.rs bench/filter/corpus-5m.tsv scripts/filter-corpus.sh .github/workflows/ci.yml docs/operations.md && git commit -m "perf(engine): filter benchmark, budget test and kw measurements of the shared index"`.

## Task 5: Contract: list identity, memory cap and index statistics (fields 600+)

Files: `proto/nexora/control/v1/control.proto` (contract), `gen/go/nexora/control/v1/control.pb.go` (regenerated in the dev pod), `mgmt/internal/control/contract_categories_test.go` (field numbers and round trip)
Interfaces: Rust `crate::proto::{FilterListRef, FilterIndexStats}`, `FilterConfig.blocklist_refs: Vec<FilterListRef>`, `FilterConfig.allowlist_refs`, `PolicyGroup.blocklist_refs`, `ConfigSnapshot.filter_index_max_bytes: u64`, `Stats.filter_index: Option<FilterIndexStats>`; Go `controlv1.FilterListRef{ListId, Category string; Position uint32; Blob *BlobRef}`, `controlv1.FilterIndexStats{Entries, Bytes, MaxBytes uint64; BuildSeconds, DecisionNsBlocked, DecisionNsClean float64; Cpu string; BlockedByCategory map[string]uint64}`, `FilterConfig.BlocklistRefs`, `FilterConfig.AllowlistRefs`, `PolicyGroup.BlocklistRefs`, `ConfigSnapshot.FilterIndexMaxBytes`, `Stats.FilterIndex`.

- [ ] Write the failing test `mgmt/internal/control/contract_categories_test.go`:
  ```go
  package control_test

  import (
  	"testing"

  	"google.golang.org/protobuf/proto"
  	"google.golang.org/protobuf/reflect/protoreflect"

  	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
  )

  func TestFilterCategoryContractFieldNumbers(t *testing.T) {
  	desc := func(m proto.Message) protoreflect.MessageDescriptor { return m.ProtoReflect().Descriptor() }
  	for _, c := range []struct {
  		msg   protoreflect.MessageDescriptor
  		field protoreflect.Name
  		num   protoreflect.FieldNumber
  	}{
  		{desc(&controlv1.FilterConfig{}), "blocklist_refs", 600},
  		{desc(&controlv1.FilterConfig{}), "allowlist_refs", 601},
  		{desc(&controlv1.PolicyGroup{}), "blocklist_refs", 600},
  		{desc(&controlv1.ConfigSnapshot{}), "filter_index_max_bytes", 600},
  		{desc(&controlv1.Stats{}), "filter_index", 600},
  		{desc(&controlv1.FilterListRef{}), "list_id", 1},
  		{desc(&controlv1.FilterListRef{}), "category", 2},
  		{desc(&controlv1.FilterListRef{}), "position", 3},
  		{desc(&controlv1.FilterListRef{}), "blob", 4},
  		{desc(&controlv1.FilterIndexStats{}), "blocked_by_category", 8},
  	} {
  		f := c.msg.Fields().ByName(c.field)
  		if f == nil || f.Number() != c.num {
  			t.Errorf("%s.%s = %v, want field %d", c.msg.FullName(), c.field, f, c.num)
  		}
  	}
  	in := &controlv1.ConfigSnapshot{
  		FilterIndexMaxBytes: 64 << 20,
  		Filter: &controlv1.FilterConfig{BlocklistRefs: []*controlv1.FilterListRef{{
  			ListId: "0b6c3e2a-2d57-4a43-9a52-8f0e8bb3c1d1", Category: "gambling", Position: 7,
  			Blob: &controlv1.BlobRef{Sha256: "ab", Size: 2, Name: "catalog:gambling:hagezi-gambling"},
  		}}},
  	}
  	raw, err := proto.Marshal(in)
  	if err != nil {
  		t.Fatal(err)
  	}
  	var out controlv1.ConfigSnapshot
  	if err := proto.Unmarshal(raw, &out); err != nil || !proto.Equal(in, &out) {
  		t.Fatalf("round trip: %v %v", &out, err)
  	}
  	st := &controlv1.Stats{FilterIndex: &controlv1.FilterIndexStats{Entries: 3, Bytes: 4096, Cpu: "cortex-a76", BlockedByCategory: map[string]uint64{"gambling": 2}}}
  	if _, err := proto.Marshal(st); err != nil {
  		t.Fatal(err)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/control/ -run TestFilterCategoryContractFieldNumbers -count=1` and expect FAIL with `undefined: controlv1.FilterListRef`.
- [ ] Edit `proto/nexora/control/v1/control.proto`: add `repeated FilterListRef blocklist_refs = 600; // blocklists with identity; engines prefer these over blocklists` and `repeated FilterListRef allowlist_refs = 601;` to `FilterConfig`; `repeated FilterListRef blocklist_refs = 600;` to `PolicyGroup`; `uint64 filter_index_max_bytes = 600; // 0 = engine default (50% of the cgroup memory limit, else 512 MiB)` to `ConfigSnapshot`; `FilterIndexStats filter_index = 600;` to `Stats`; and append:
  ```proto
  // ---------------------------------------------------------------------------
  // Filter categories: fields added to existing messages use 600-699.
  // ---------------------------------------------------------------------------

  // A filter list blob with its identity. Management also fills the M1 BlobRef fields with the same
  // blobs so engines without this message keep filtering.
  message FilterListRef {
    string list_id = 1;   // filter_lists.id; "allowlist" for the global allowlist blob
    string category = 2;  // catalog category key ([a-z0-9-]{1,32}); empty for custom lists
    uint32 position = 3;  // attribution order: catalog position from 1, custom lists from 1000000 by name
    BlobRef blob = 4;
  }

  message FilterIndexStats {
    uint64 entries = 1;
    uint64 bytes = 2;                 // index plus views
    uint64 max_bytes = 3;             // cap in force
    double build_seconds = 4;         // last build (0 when the index was reused from the first build)
    double decision_ns_blocked = 5;   // median per decision, measured after the last build
    double decision_ns_clean = 6;
    string cpu = 7;                   // core of that measurement: cortex-a76, cortex-a55, x86_64, ...
    map<string, uint64> blocked_by_category = 8; // "custom" for lists without a category
  }
  ```
- [ ] Regenerate the Go code with the dev pod's pinned plugins: `scripts/dev-exec.sh 'rm -rf /tmp/gen && mkdir -p /tmp/gen && protoc -I proto --go_out=/tmp/gen --go_opt=paths=source_relative --go-grpc_out=/tmp/gen --go-grpc_opt=paths=source_relative proto/nexora/control/v1/control.proto'` then `kubectl --context kw -n nexora-dev exec deploy/toolbox -c toolbox -- cat /tmp/gen/nexora/control/v1/control.pb.go > gen/go/nexora/control/v1/control.pb.go` (only `control.pb.go` changes).
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/control/ -run TestFilterCategoryContractFieldNumbers -count=1 && cargo check --locked -p nexora-engine --all-targets'` and expect `ok` and `Finished` (prost adds the fields; existing struct literals use `..Default::default()`).
- [ ] `mgmt/internal/control/contract_m5_test.go` asserted that `ConfigSnapshot` and `Stats` have no field number `>= 500`; as built it checks the M5 range `500..599` only, since 600+ belong to this plan.
- [ ] Commit: `git add proto/nexora/control/v1/control.proto gen/go/nexora/control/v1/control.pb.go mgmt/internal/control/contract_categories_test.go mgmt/internal/control/contract_m5_test.go && git commit -m "feat(proto): filter list identity, index memory cap and index stats (fields 600+)"`.

## Task 6: Snapshot list collection, category slots and the memory cap default

Files: `engine/src/filter/lists.rs` (collects every list of a snapshot with identity, keys, category slots, cgroup cap), `engine/src/filter.rs` (`pub mod lists;`), `engine/src/snapshot.rs` (validates refs), `engine/src/control.rs` (`fetch_blobs` also fetches refs' blobs)
Interfaces:

```rust
// crate::filter::lists
pub const CGROUP_MEMORY_MAX: &str = "/sys/fs/cgroup/memory.max";
pub const DEFAULT_MAX_BYTES: u64 = 512 << 20;
pub const CATEGORY_SLOTS: usize = 64;
pub const CUSTOM_POSITION: u32 = 1_000_000;
pub enum ListSource { Blob(BlobRef), Inline(Vec<u8>) }
pub struct CollectedList { pub id: String, pub category: String, pub position: u32, pub kind: ListKind, pub sha256: String, pub source: ListSource }
pub struct GroupLists { pub block: Vec<String>, pub allow: Option<String> }
pub struct SnapshotLists { pub lists: Vec<CollectedList>, pub global_block: Vec<String>, pub global_allow: Vec<String>, pub groups: Vec<GroupLists>, pub key: String }
impl SnapshotLists {
    pub fn collect(snap: &ConfigSnapshot) -> Result<SnapshotLists, String>;
    pub fn build_index(&self, blobs: &dyn BlobSource, max_bytes: u64, threads: usize) -> Result<FilterIndex, SnapshotError>;
    pub fn indexes(&self, index: &FilterIndex, ids: &[String]) -> Vec<u16>;
    pub fn group_key(&self, group: usize) -> String;
}
pub fn default_max_bytes(memory_max: &Path) -> u64;
pub fn build_threads() -> usize;
pub fn category_slot(category: &str) -> u8;
pub fn category_names() -> Vec<(u8, String)>;
```

- [ ] Declare `pub mod lists;` in `engine/src/filter.rs` and create `engine/src/filter/lists.rs` with this test module:
  ```rust
  #[cfg(test)]
  mod tests {
      use super::*;
      use crate::proto::{BlobRef, ConfigSnapshot, FilterConfig, FilterListRef, PolicyGroup};

      fn blob(sha: char) -> BlobRef {
          BlobRef { sha256: sha.to_string().repeat(64), size: 10, name: "n".into() }
      }
      fn list_ref(id: &str, category: &str, position: u32, sha: char) -> FilterListRef {
          FilterListRef { list_id: id.into(), category: category.into(), position, blob: Some(blob(sha)) }
      }

      #[test]
      fn collect_prefers_refs_synthesizes_ids_and_orders_by_position() {
          let snap = ConfigSnapshot {
              filter: Some(FilterConfig {
                  blocklists: vec![blob('a'), blob('b')],
                  blocklist_refs: vec![list_ref("custom-1", "", CUSTOM_POSITION, 'b'), list_ref("hagezi-pro", "ads-tracking", 3, 'a')],
                  allowlists: vec![blob('c')],
                  ..Default::default()
              }),
              policy_groups: vec![PolicyGroup {
                  id: "g1".into(),
                  blocklists: vec![blob('d')],
                  allowlist: vec!["ok.example".into()],
                  ..Default::default()
              }],
              ..Default::default()
          };
          let l = SnapshotLists::collect(&snap).unwrap();
          let ids: Vec<&str> = l.lists.iter().map(|x| x.id.as_str()).collect();
          let (c, d) = (format!("blob:{}", "c".repeat(64)), format!("blob:{}", "d".repeat(64)));
          assert_eq!(&ids[..4], &["hagezi-pro", c.as_str(), d.as_str(), "custom-1"], "by position, then id");
          assert_eq!(l.global_block, vec!["custom-1".to_string(), "hagezi-pro".to_string()]);
          assert_eq!(l.global_allow, vec![format!("blob:{}", "c".repeat(64))]);
          assert_eq!(l.groups[0].block, vec![format!("blob:{}", "d".repeat(64))]);
          let allow = l.groups[0].allow.clone().unwrap();
          assert!(allow.starts_with("group-allow:") && allow.len() == "group-allow:".len() + 64);
          assert!(l.lists.iter().any(|x| x.id == allow && matches!(&x.source, ListSource::Inline(t) if t == b"ok.example\n")));
          assert_eq!(l.lists.iter().find(|x| x.id == "hagezi-pro").unwrap().category, "ads-tracking");
      }

      #[test]
      fn identical_lists_are_shared_and_conflicts_are_rejected() {
          let g = |id: &str, refs: Vec<FilterListRef>, allow: &[&str]| PolicyGroup {
              id: id.into(),
              blocklist_refs: refs,
              allowlist: allow.iter().map(|s| s.to_string()).collect(),
              ..Default::default()
          };
          let mut snap = ConfigSnapshot {
              filter: Some(FilterConfig { blocklist_refs: vec![list_ref("l1", "gambling", 1, 'a')], ..Default::default() }),
              policy_groups: vec![g("g1", vec![list_ref("l1", "gambling", 1, 'a')], &["x.test"]), g("g2", vec![], &["x.test"])],
              ..Default::default()
          };
          let l = SnapshotLists::collect(&snap).unwrap();
          assert_eq!(l.lists.len(), 2, "one shared block list and one shared inline allowlist");
          assert_eq!(l.groups[0].allow, l.groups[1].allow);
          let before = l.key.clone();
          snap.policy_groups[1].allowlist = vec!["y.test".into()];
          assert_ne!(SnapshotLists::collect(&snap).unwrap().key, before, "an allowlist edit changes the key");
          snap.policy_groups[0].blocklist_refs = vec![list_ref("l1", "gambling", 1, 'b')];
          assert_eq!(SnapshotLists::collect(&snap).err().unwrap(), "filter list l1: two different blobs in one snapshot");
          snap.policy_groups[0].blocklist_refs = vec![list_ref("l2", "Bad Category", 1, 'a')];
          assert_eq!(SnapshotLists::collect(&snap).err().unwrap(), "filter list l2: category Bad Category must match [a-z0-9-]{0,32}");
          snap.policy_groups[0].blocklist_refs = vec![FilterListRef { list_id: "l3".into(), blob: None, ..Default::default() }];
          assert_eq!(SnapshotLists::collect(&snap).err().unwrap(), "filter list l3: blob missing");
          snap.policy_groups[0].blocklist_refs = vec![list_ref("", "", 1, 'a')];
          assert_eq!(SnapshotLists::collect(&snap).err().unwrap(), "filter list id must be 1..=128 bytes");
      }

      #[test]
      fn default_max_bytes_reads_the_cgroup_v2_limit() {
          let dir = tempfile::tempdir().unwrap();
          let f = dir.path().join("memory.max");
          std::fs::write(&f, "1073741824\n").unwrap();
          assert_eq!(default_max_bytes(&f), 536_870_912);
          std::fs::write(&f, "max\n").unwrap();
          assert_eq!(default_max_bytes(&f), DEFAULT_MAX_BYTES);
          std::fs::write(&f, "garbage").unwrap();
          assert_eq!(default_max_bytes(&f), DEFAULT_MAX_BYTES);
          assert_eq!(default_max_bytes(&dir.path().join("absent")), DEFAULT_MAX_BYTES);
          assert!((1..=4).contains(&build_threads()));
      }

      #[test]
      fn category_slots_are_stable_and_bounded() {
          assert_eq!(category_slot(""), 0);
          let a = category_slot("slot-test-a");
          assert!(a >= 1 && a < 63);
          assert_eq!(category_slot("slot-test-a"), a, "stable across calls");
          for i in 0..80 {
              assert!(category_slot(&format!("slot-overflow-{i}")) <= 63);
          }
          let names = category_names();
          assert!(names.contains(&(0, "custom".to_string())));
          assert!(names.contains(&(a, "slot-test-a".to_string())));
          assert!(names.contains(&(63, "other".to_string())));
      }
  }
  ```
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --lib filter::lists` and expect FAIL with ``cannot find type `SnapshotLists` in this scope``.
- [ ] Implement `engine/src/filter/lists.rs` (`//! Every filter list of a snapshot with its identity, the index key, category counter slots and the index memory cap.`):
  - `collect`: global block lists from `filter.blocklist_refs` when non-empty, else `filter.blocklists` as `CollectedList { id: "blob:<sha256>", category: "", position: CUSTOM_POSITION + i, kind: Block }`; global allow lists from `allowlist_refs`, else `allowlists` the same way; for each policy group (in snapshot order) block lists from `blocklist_refs`, else `blocklists`; a non-empty group `allowlist` becomes `Inline(text)` with `text = allowlist.join("\n") + "\n"`, `sha256 = hex(Sha256(text))`, `id = "group-allow:" + sha256`, kind `Allow`, category `""`, position `u32::MAX`. Validation order and messages: `filter list id must be 1..=128 bytes`; `filter list {id}: blob missing`; `filter list {id}: category {c} must match [a-z0-9-]{0,32}`; the same id seen again with another `sha256` → `filter list {id}: two different blobs in one snapshot`, with another kind → `filter list {id}: used as block and allow list`; more than `index::MAX_LISTS` distinct lists → `{n} filter lists exceed 65534`. Distinct lists are kept once, then sorted by `(position, id)`. `global_block`/`global_allow` keep snapshot order; `key` = the sorted lists joined as `{b|a}:{id}@{sha256}` with `;`.
  - `build_index`: decode every blob list with `filter::decode_blob(&blobs.read(r)?)` (zstd errors as `SnapshotError::Blob { sha256, reason: "zstd: {e}" }`), build `ListInput` in list order with `category_slot(&category)`, and call `FilterIndex::build(&inputs, &IndexOptions { max_bytes, threads, ..IndexOptions::new(max_bytes) })`, mapping `IndexError` to `SnapshotError::Invalid(e.to_string())`.
  - `indexes`: `ids.iter().filter_map(|id| index.list_index(id))`, `group_key(i)`: the group's block ids with `@sha256`, sorted and joined with `,`, then `|` and the allow id (empty when none).
  - `default_max_bytes`: read the file; a trimmed decimal `n` gives `n / 2`; `max`, unreadable or unparsable gives `DEFAULT_MAX_BYTES`. `build_threads`: `available_parallelism().map_or(1, NonZeroUsize::get).min(4)` (std reads the cgroup CPU quota, so kw engines get 2).
  - Category slots: `static CATEGORIES: std::sync::Mutex<Vec<String>> = Mutex::new(Vec::new());` holds names for slots 1..=62 in first-use order; `category_slot("") = 0`; a known name returns its slot; a new name gets the next slot while fewer than 62 are taken, else 63. `category_names()` returns `(0, "custom")`, every taken slot, and `(63, "other")` once any name mapped to 63. Only snapshot application calls these (never the query path).
- [ ] In `engine/src/snapshot.rs` `validate`, check the `sha256` of every blob in `filter.blocklist_refs`, `filter.allowlist_refs` and each policy group's `blocklists` and `blocklist_refs` with the existing `is_sha256_hex` rule, then run `crate::filter::lists::SnapshotLists::collect(s).map_err(SnapshotError::Invalid)?;`; add to `engine/tests/snapshot_apply.rs`:
  ```rust
  #[test]
  fn filter_list_refs_are_validated() {
      let dir = tempfile::tempdir().unwrap();
      let cur = ArcSwap::from_pointee(Runtime::initial());
      let blobs = DirBlobs { dir: dir.path().to_path_buf() };
      let good = blob(dir.path(), "ads.example\n");
      let mut s = base(1);
      s.filter.as_mut().unwrap().blocklist_refs =
          vec![FilterListRef { list_id: "ads".into(), category: "ads-tracking".into(), position: 1, blob: Some(good.clone()) }];
      assert!(matches!(snapshot::apply(&cur, s, &blobs, None), ApplyOutcome::Applied { version: 1, .. }));
      let mut bad = base(2);
      bad.filter.as_mut().unwrap().blocklist_refs =
          vec![FilterListRef { list_id: "ads".into(), category: "ads-tracking".into(), position: 1, blob: Some(BlobRef { sha256: "XYZ".into(), ..good }) }];
      assert_eq!(outcome_reason(snapshot::apply(&cur, bad, &blobs, None)), "invalid snapshot: sha256 XYZ must be 64 lowercase hex");
      assert_eq!(cur.load().version, 1);
  }
  ```
- [ ] In `engine/src/control.rs` `fetch_blobs`, chain `f.blocklist_refs.iter().chain(&f.allowlist_refs).filter_map(|r| r.blob.as_ref())` and `snap.policy_groups.iter().flat_map(|g| g.blocklist_refs.iter().filter_map(|r| r.blob.as_ref()))` into `refs` before `.collect::<Vec<_>>()`, and skip a `sha256` already fetched in this call (a `HashSet<&str>`), since the refs repeat the M1 blobs.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib filter::lists && cargo test --locked -p nexora-engine --test snapshot_apply filter_list_refs_are_validated && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'` and expect `test result: ok` for the five tests.
- As built (2026-09-14): as specified, except `key` entries are `{b|a}:{id}@{sha256}/{category}` (a category change must rebuild the index, since `ListMeta.category_slot` lives in it), the `other` slot is listed by `category_names()` only once a name mapped to it, and `snapshot::validate` also checks the sha256 of every ref blob before `collect`. Tests: `cargo test --locked -p nexora-engine --lib filter::lists` (4 passed) and `--test snapshot_apply filter_list_refs_are_validated` (passed).
- [ ] Commit: `git add engine/src/filter.rs engine/src/filter/lists.rs engine/src/snapshot.rs engine/src/control.rs engine/tests/snapshot_apply.rs && git commit -m "feat(engine): collect snapshot filter lists with identity, category slots and memory cap default"`.

## Task 7: Runtime and query path on the shared index (views, reuse, cap, allocation-free)

Files: `engine/src/filter.rs` (v1 `FilterSet`, `ListStats` and the old `FilterDecision` removed; `BlockReply`; `EffectivePolicy` and `PolicyTable` on `Arc<FilterView>`; `pub use index::{FilterDecision, FilterIndex, FilterView, ListHit}` (the spec's `filter::FilterIndex` and `filter::FilterView`); `policy_tests` rebuilt on the index), `engine/src/runtime.rs` (index build or reuse, views, cap check), `engine/src/server/mod.rs` (`Verdict::Blocked(hit)`, block replies through `BlockReply`, cloaking through the view), `engine/src/server/rewrite.rs` (`block_records(BlockReply, ...)` and its test), `engine/tests/snapshot_apply.rs` (`unchanged_lists_reuse_index`, cap rejection, v1 filter test ported), `engine/tests/hot_path_alloc.rs` (index-backed group and blocked replies), `engine/tests/filter_index_budget.rs` (`TestFilterIndexSharedAcrossGroups`)
Interfaces:

```rust
// crate::filter
pub use index::{FilterDecision, FilterIndex, FilterView, ListHit};
#[derive(Clone, Copy, Debug, PartialEq, Eq)] pub struct BlockReply { pub mode: BlockMode, pub ttl: u32 }
impl BlockReply { pub fn write(&self, q: &QueryView<'_>, out: &mut [u8], opt: Option<&ReplyOpt>) -> usize }
pub enum Verdict<'a> { Pass, Allowed, Blocked(ListHit), Rewrite(&'a RewriteAnswer) }
impl EffectivePolicy { pub fn group_id(&self) -> &str; pub fn filter(&self) -> &FilterView; pub fn block_reply(&self) -> BlockReply; pub fn cache_partition(&self) -> u16; pub fn check(&self, wire_name: &[u8]) -> Verdict<'_> }
impl PolicyTable {
    pub fn global_only(global: Arc<FilterView>, block: BlockReply) -> PolicyTable;
    pub fn build(snap: &ConfigSnapshot, lists: &SnapshotLists, index: &Arc<FilterIndex>, global: Arc<FilterView>, block: BlockReply) -> Result<PolicyTable, String>;
    pub fn views_memory_bytes(&self) -> u64;
}
// crate::runtime::Runtime (fields replacing `filter` and `filter_stats`)
pub filter_index: Arc<FilterIndex>, pub filter_key: String, pub filter_max_bytes: u64, pub filter_memory_bytes: u64
```

- [ ] Add to `engine/tests/snapshot_apply.rs` (and change its `use nexora_engine::filter::...` line to `use nexora_engine::filter::{FilterDecision, domain_to_wire};`):
  ```rust
  fn with_lists(version: u64, dir: &std::path::Path, group_allow: &[&str]) -> ConfigSnapshot {
      let mut s = base(version);
      let ads = blob(dir, "ads.example\ntracker.example.net\n");
      let f = s.filter.as_mut().unwrap();
      f.blocklists = vec![ads.clone()];
      f.blocklist_refs = vec![FilterListRef { list_id: "ads".into(), category: "ads-tracking".into(), position: 1, blob: Some(ads.clone()) }];
      s.policy_groups = vec![PolicyGroup {
          id: "kids".into(),
          name: "kids".into(),
          cidrs: vec!["127.0.0.2/32".into()],
          blocklists: vec![ads.clone()],
          blocklist_refs: vec![FilterListRef { list_id: "ads".into(), category: "ads-tracking".into(), position: 1, blob: Some(ads) }],
          allowlist: group_allow.iter().map(|x| x.to_string()).collect(),
          ..Default::default()
      }];
      s
  }

  #[test]
  fn unchanged_lists_reuse_index() {
      let dir = tempfile::tempdir().unwrap();
      let cur = ArcSwap::from_pointee(Runtime::initial());
      let blobs = DirBlobs { dir: dir.path().to_path_buf() };
      assert!(matches!(snapshot::apply(&cur, with_lists(1, dir.path(), &["ok.ads.example"]), &blobs, None), ApplyOutcome::Applied { .. }));
      let first = cur.load_full();
      let blocked = |rt: &Runtime| rt.policy.select("127.0.0.1".parse().unwrap()).0.filter().decide(&domain_to_wire(b"x.ads.example").unwrap());
      assert!(matches!(blocked(&first), FilterDecision::Blocked(_)), "positive path: the list blocks");
      assert!(first.filter_index.entries() >= 3);

      let mut same_lists = with_lists(2, dir.path(), &["ok.ads.example"]);
      same_lists.cache.as_mut().unwrap().max_bytes = 8 << 20;
      same_lists.upstreams[0].timeout_ms = 300;
      assert!(matches!(snapshot::apply(&cur, same_lists, &blobs, None), ApplyOutcome::Applied { .. }));
      let second = cur.load_full();
      assert!(Arc::ptr_eq(&first.filter_index, &second.filter_index), "no list changed: the index is reused");
      assert_eq!(first.filter_index.generation(), second.filter_index.generation());

      assert!(matches!(snapshot::apply(&cur, with_lists(3, dir.path(), &["other.ads.example"]), &blobs, None), ApplyOutcome::Applied { .. }));
      let third = cur.load_full();
      assert!(!Arc::ptr_eq(&second.filter_index, &third.filter_index), "an allowlist edit rebuilds the index");
      assert!(third.filter_index.generation() > second.filter_index.generation());
  }

  #[test]
  fn filter_index_over_cap_rejects_snapshot_and_keeps_previous() {
      let dir = tempfile::tempdir().unwrap();
      let cur = ArcSwap::from_pointee(Runtime::initial());
      let blobs = DirBlobs { dir: dir.path().to_path_buf() };
      assert!(matches!(snapshot::apply(&cur, with_lists(1, dir.path(), &[]), &blobs, None), ApplyOutcome::Applied { .. }));
      let mut capped = with_lists(2, dir.path(), &["new.example"]);
      capped.filter_index_max_bytes = 4096;
      let reason = outcome_reason(snapshot::apply(&cur, capped, &blobs, None));
      assert!(reason.contains("above the cap of 4096 bytes"), "{reason}");
      let rt = cur.load();
      assert_eq!(rt.version, 1);
      let (p, _) = rt.policy.select("127.0.0.2".parse().unwrap());
      assert!(matches!(p.filter().decide(&domain_to_wire(b"ads.example").unwrap()), FilterDecision::Blocked(_)), "previous index still active");
  }
  ```
  and replace `filter_subdomains_allowlist_invalid_lines_and_cloaking` with:
  ```rust
  #[test]
  fn filter_subdomains_allowlist_invalid_lines_and_cloaking() {
      use nexora_engine::filter::index::{FilterIndex, IndexOptions, ListInput, ListKind};
      let block = b"ads.example\ntracker.example.net\nnot a domain\n-bad-.example\n".to_vec();
      let allow = b"good.ads.example\n".to_vec();
      let inputs = [
          ListInput { id: "b", category: "", category_slot: 0, kind: ListKind::Block, text: &block },
          ListInput { id: "a", category: "", category_slot: 0, kind: ListKind::Allow, text: &allow },
      ];
      let index = Arc::new(FilterIndex::build(&inputs, &IndexOptions::new(16 << 20)).unwrap());
      assert_eq!((index.entries(), index.invalid_lines()), (3, 2));
      let f = index.view(&[0], &[1]);
      let w = |s: &str| domain_to_wire(s.as_bytes()).unwrap();
      assert!(matches!(f.decide(&w("ads.example")), FilterDecision::Blocked(_)));
      assert!(matches!(f.decide(&w("x.y.ads.example")), FilterDecision::Blocked(_)));
      assert_eq!(f.decide(&w("good.ads.example")), FilterDecision::Allowed);
      assert_eq!(f.decide(&w("sub.good.ads.example")), FilterDecision::Allowed);
      assert_eq!(f.decide(&w("example")), FilterDecision::None);
      assert_eq!(f.decide(&w("notads.example")), FilterDecision::None);
      let target = NameKey::from_wire_lowercase(&w("cdn.tracker.example.net")).unwrap();
      assert!(f.cloaked(&[target]).is_some());
      assert!(Arc::new(FilterIndex::empty()).view(&[], &[]).cloaked(&[target]).is_none());
  }
  ```
- [ ] Add to `engine/tests/filter_index_budget.rs`:
  ```rust
  #[allow(non_snake_case)]
  #[test]
  fn TestFilterIndexSharedAcrossGroups() {
      use nexora_engine::proto::*;
      use nexora_engine::runtime::Runtime;
      use nexora_engine::snapshot::DirBlobs;
      use sha2::{Digest, Sha256};

      let dir = tempfile::tempdir().unwrap();
      let refs: Vec<FilterListRef> = synthetic_lists(200_000, 6, 9)
          .iter()
          .enumerate()
          .map(|(i, text)| {
              let z = zstd::encode_all(&text[..], 3).unwrap();
              let sha = hex::encode(Sha256::digest(&z));
              std::fs::write(dir.path().join(&sha), &z).unwrap();
              FilterListRef {
                  list_id: format!("list-{i}"),
                  category: "ads-tracking".into(),
                  position: i as u32 + 1,
                  blob: Some(BlobRef { sha256: sha, size: z.len() as u64, name: format!("list-{i}") }),
              }
          })
          .collect();
      let snapshot = |groups: Vec<PolicyGroup>| ConfigSnapshot {
          version: 1,
          cache: Some(CacheConfig { max_bytes: 1 << 20, ..Default::default() }),
          filter: Some(FilterConfig { block_mode: BlockMode::NullIp as i32, block_ttl: 60, blocklist_refs: refs.clone(), ..Default::default() }),
          policy_groups: groups,
          ..Default::default()
      };
      let group = |id: &str, cidr: &str, lists: &[usize]| PolicyGroup {
          id: id.into(),
          name: id.into(),
          cidrs: vec![cidr.into()],
          blocklist_refs: lists.iter().map(|&i| refs[i].clone()).collect(),
          ..Default::default()
      };
      let blobs = DirBlobs { dir: dir.path().into() };
      let single = Runtime::build(&snapshot(vec![]), &blobs, None).unwrap();
      let three = Runtime::build(
          &snapshot(vec![group("g1", "10.1.0.0/16", &[0, 1]), group("g2", "10.2.0.0/16", &[2, 3, 4]), group("g3", "10.3.0.0/16", &[1, 5])]),
          &blobs,
          None,
      )
      .unwrap();
      assert!(single.filter_memory_bytes > 3_000_000, "the single-group index holds the lists: {}", single.filter_memory_bytes);
      assert_eq!(three.filter_index.entries(), single.filter_index.entries(), "groups add no names");
      let ratio = three.filter_memory_bytes as f64 / single.filter_memory_bytes as f64;
      assert!(ratio < 1.10, "three policy groups use {ratio:.3}x the memory of one");
  }
  ```
  and add `tempfile`, `zstd`, `hex` and `sha2` imports as used above (all already dependencies of the crate).
- [ ] In `engine/tests/hot_path_alloc.rs` `cache_hit_path_does_not_allocate`, create `tmp` before the snapshot, write a list blob and select it globally and in group `g1` with an allowlist, so the measured cache-hit client walks a non-empty index with allow lists; then measure blocked replies:
  ```rust
  let tmp = tempfile::tempdir().unwrap();
  let z = zstd::encode_all(&b"ads.hot.test\n"[..], 3).unwrap();
  let sha = hex::encode(<sha2::Sha256 as sha2::Digest>::digest(&z));
  std::fs::write(tmp.path().join(&sha), &z).unwrap();
  let ads = BlobRef { sha256: sha, size: z.len() as u64, name: "ads".into() };
  let ads_ref = FilterListRef { list_id: "ads".into(), category: "ads-tracking".into(), position: 1, blob: Some(ads.clone()) };
  ```
  In the snapshot literal set `blocklists: vec![ads.clone()], blocklist_refs: vec![ads_ref.clone()], allowlist: vec!["ok.ads.hot.test".into()]` on `PolicyGroup g1` and `blocklists: vec![ads.clone()], blocklist_refs: vec![ads_ref.clone()]` in `FilterConfig`, remove the later `let tmp = tempfile::tempdir().unwrap();`, and after each `measure(&shared)` call `measure_blocked(&shared)`:
  ```rust
  fn measure_blocked(shared: &std::sync::Arc<nexora_engine::server::Shared>) {
      use hickory_proto::op::{Message, MessageType, OpCode, Query};
      use hickory_proto::rr::{Name, RecordType};
      use hickory_proto::serialize::binary::BinEncodable;
      use nexora_engine::edns::Transport;
      use nexora_engine::server::{FastOutcome, WorkerCtx, handle_packet};

      let mut m = Message::new(2, MessageType::Query, OpCode::Query);
      m.add_query(Query::query(Name::from_ascii("x.ads.hot.test.").unwrap(), RecordType::A));
      let query = m.to_bytes().unwrap();
      let ctx = WorkerCtx::new(0, shared.clone());
      let mut out = [0u8; 1232];
      for client in ["10.1.2.3:5353", "127.0.0.1:5353"] {
          let client: std::net::SocketAddr = client.parse().unwrap();
          let before = shared.metrics.totals().filter_blocked;
          for _ in 0..64 {
              let rt = shared.runtime.load();
              assert!(matches!(handle_packet(&ctx, &rt, &query, client, Transport::Udp, &mut out), FastOutcome::Reply(_)));
          }
          ALLOCS.with(|c| c.set(0));
          ARMED.with(|a| a.set(true));
          for _ in 0..50_000 {
              let rt = shared.runtime.load();
              match handle_packet(&ctx, &rt, &query, client, Transport::Udp, &mut out) {
                  FastOutcome::Reply(n) => assert!(n > 12),
                  _ => panic!("expected a block reply"),
              }
          }
          ARMED.with(|a| a.set(false));
          assert_eq!(ALLOCS.with(Cell::get), 0, "blocked reply path allocated for {client}");
          assert!(shared.metrics.totals().filter_blocked >= before + 50_000, "the name was blocked for {client}");
      }
  }
  ```
- [ ] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine --test snapshot_apply unchanged_lists_reuse_index` and expect FAIL with ``no field `filter_index` on type``.
- [ ] Rework `engine/src/filter.rs`: delete `FilterSet`, `ListStats` and the old `FilterDecision`; add `pub use index::{FilterDecision, FilterIndex, FilterView, ListHit};` and `BlockReply` whose `write` is the body of the old `FilterSet::write_block_reply` on `self.mode`/`self.ttl`; `Verdict::Blocked(ListHit)`; `EffectivePolicy { group_id, filter: Arc<FilterView>, block: BlockReply, rewrites, cache_partition }` whose `check` maps `FilterDecision::Blocked(hit)` to `Verdict::Blocked(hit)`; `PolicyTable::global_only(global, block)`; and `PolicyTable::build(snap, lists, index, global, block)` which keeps the rewrite, CIDR and duplicate checks unchanged but, for group `i`, looks up `lists.group_key(i)` in a `HashMap<String, (Arc<FilterView>, u16)>` and otherwise creates `Arc::new(index.view(&lists.indexes(index, &lists.groups[i].block), &lists.indexes(index, lists.groups[i].allow.as_slice())))` with the next cache partition, pushing the key to `partition_keys`; `views_memory_bytes` sums the global view and each distinct group view.
- [ ] Port `filter::policy_tests`: replace `global()` and the `PolicyTable::build(&s, global(), &blobs())` calls with
  ```rust
  fn table(s: &ConfigSnapshot) -> Result<PolicyTable, String> {
      let mut s = s.clone();
      let (r, _) = ads_blob();
      s.filter = Some(crate::proto::FilterConfig { blocklists: vec![BlobRef { name: "global".into(), ..r }], block_mode: 1, block_ttl: 60, ..Default::default() });
      let lists = crate::filter::lists::SnapshotLists::collect(&s)?;
      let index = Arc::new(lists.build_index(&blobs(), 64 << 20, 1).map_err(|e| e.to_string())?);
      let global = Arc::new(index.view(&lists.indexes(&index, &lists.global_block), &lists.indexes(&index, &lists.global_allow)));
      PolicyTable::build(&s, &lists, &index, global, BlockReply { mode: BlockMode::NullIp, ttl: 60 })
  }
  ```
  (`most_specific_cidr_wins_and_group_replaces_global`, `rewrite_precedence_exact_wildcard_and_set_order`, `groups_sharing_a_filter_share_a_cache_partition` call `table(&snapshot())`/`table(&s)` and match `Verdict::Blocked(_)`); in `invalid_snapshots_are_rejected_with_reason` the missing-blob case now expects `blob nope: missing` (the blob is read while building the index, before group policy), all other messages unchanged.
- [ ] Rework `engine/src/runtime.rs`: remove `filter` and `filter_stats`; add the four fields of the Interfaces block; `initial()` uses `let index = Arc::new(FilterIndex::empty());`, `PolicyTable::global_only(Arc::new(index.view(&[], &[])), BlockReply { mode: BlockMode::NullIp, ttl: 0 })`, `filter_key: String::new()`, `filter_max_bytes: lists::DEFAULT_MAX_BYTES`, `filter_memory_bytes: 0`. In `build`, replace the `read_all`/`FilterSet::build`/`PolicyTable::build` block with:
  ```rust
  let lists = SnapshotLists::collect(s).map_err(SnapshotError::Invalid)?;
  let filter_max_bytes = match s.filter_index_max_bytes {
      0 => lists::default_max_bytes(std::path::Path::new(lists::CGROUP_MEMORY_MAX)),
      n => n,
  };
  let filter_index = match previous.filter(|p| p.filter_key == lists.key) {
      Some(p) => p.filter_index.clone(),
      None => Arc::new(lists.build_index(blobs, filter_max_bytes, lists::build_threads())?),
  };
  let block = BlockReply { mode, ttl: f.block_ttl };
  let global = Arc::new(filter_index.view(
      &lists.indexes(&filter_index, &lists.global_block),
      &lists.indexes(&filter_index, &lists.global_allow),
  ));
  let policy = PolicyTable::build(s, &lists, &filter_index, global, block).map_err(SnapshotError::Invalid)?;
  let filter_memory_bytes = filter_index.memory_bytes() + policy.views_memory_bytes();
  if filter_memory_bytes > filter_max_bytes {
      return Err(SnapshotError::Invalid(
          IndexError::OverCap { needed: filter_memory_bytes, cap: filter_max_bytes }.to_string(),
      ));
  }
  ```
  and build `filter_hashes` as `format!("l:{}", lists.key)`, then the `g:` partition keys, `r:` and `u:` entries as before.
- [ ] In `engine/src/server/mod.rs`, the `Verdict::Blocked(hit)` arm writes with `policy.block_reply().write(&q, &mut out[..limit], opt.as_ref())`; `leader_answer` uses `if let Some(hit) = policy.filter().cloaked(&info.cname_targets)` and `policy.block_reply().write(q, &mut buf, None)` (`hit` is recorded in Task 8; bind it as `_hit` here). In `engine/src/server/rewrite.rs` change `block_records(filter: &FilterSet, ...)` to `block_records(block: BlockReply, ...)` using `block.mode`/`block.ttl`, its callers to `policy.block_reply()`, and the test's `FilterSet::build(...)` global filter to an index built from the same text through `SnapshotLists` exactly as `policy_tests::table` does.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --all-targets && cargo test --locked --release -p nexora-engine --test filter_index_budget && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'` and expect every test to pass, including `unchanged_lists_reuse_index`, `filter_index_over_cap_rejects_snapshot_and_keeps_previous`, `cache_hit_path_does_not_allocate`, `TestFilterIndexSharedAcrossGroups` and the ported `policy_tests`.
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=bin go test -count=1 -timeout 30m -run "TestBlocklistSubscription|TestPerClientPolicy|TestSafeSearchRewrites" ./e2e/'` and expect `ok` (M1/M2 filtering behaviour unchanged on the new index).
- As built (2026-09-14), deviations: the fast path decides through the worker's decision cache: `EffectivePolicy::check_cached(&self, cache: &DecisionCache, wire_name) -> Verdict<'_>` next to the uncached `check` (CNAME rewrite chases and cloaking stay uncached), `WorkerCtx.filter_decisions: DecisionCache`; `PolicyTable` stores `views_memory_bytes`; `filter_index_over_cap_rejects_snapshot_and_keeps_previous` uses a 256-byte cap (the smallest index is two 128-octet blocks plus its stash, below the planned 4096); `measure_blocked` in `hot_path_alloc.rs` decides 256 distinct blocked names per client with the allocation guard armed, so it covers the index walk (first sighting) and cache hits, and asserts `filter_decisions.hits() > 40_000`; the rewrite test builds its index from a `OneBlob` source. Tests: `make engine-test` all green, `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` clean.
- [ ] Commit: `git add engine/src/filter.rs engine/src/runtime.rs engine/src/server/mod.rs engine/src/server/rewrite.rs engine/tests/snapshot_apply.rs engine/tests/hot_path_alloc.rs engine/tests/filter_index_budget.rs && git commit -m "feat(engine): serve filtering from the shared index with per-group views, reuse and memory cap"`.

## Task 8: Attribution, per-category counters, index metrics and per-engine decision timing

Files: `engine/src/telemetry/querylog.rs` (`filter_list`, `filter_generation` in `QueryRecord`), `engine/src/server/mod.rs` (records the hit and counts categories on both block paths), `engine/src/server/rewrite.rs` (record literal gains the new fields when it builds one), `engine/src/telemetry/metrics.rs` (category counters, index gauges, `Stats.filter_index`), `engine/src/telemetry/otlp.rs` (`nexora.filter.list_id`, `nexora.filter.category`), `engine/src/filter/calibrate.rs` (pinned decision timing), `engine/src/filter/index.rs` (`sample_names`), `engine/src/filter/names.rs` (`unpack_wire`), `engine/src/runtime.rs` (`filter_calibration`), `engine/tests/telemetry_export.rs` (attribution and metric assertions)
Interfaces:

```rust
// crate::telemetry::querylog
pub const NO_FILTER_LIST: u16 = u16::MAX;
pub struct QueryRecord { /* existing fields */ pub filter_list: u16, pub filter_generation: u64 }
// crate::telemetry::metrics
impl WorkerCounters { pub fn count_categories(&self, slots: u64) }   // field: pub filter_blocked_category: [Counter; 64]
// crate::telemetry::otlp
pub fn log_record(r: &QueryRecord, upstream_name: &str, engine_id: &str, policy_group: &str, filter_list: Option<&ListMeta>) -> LogRecord;
// crate::filter::calibrate
#[derive(Clone, Debug, Default, PartialEq)] pub struct Calibration { pub blocked_ns: f64, pub clean_ns: f64, pub cpu: String }
pub const ARCH_LABEL: &str = std::env::consts::ARCH;
pub fn measure(index: &Arc<FilterIndex>) -> Calibration;
pub fn cpu_label(part: &str) -> &'static str;
pub fn fastest_cores(cpuinfo: &str, allowed: &[usize]) -> (Vec<usize>, &'static str);
// crate::filter::index
impl FilterIndex { pub fn sample_names(&self, n: usize) -> Vec<Box<[u8]>> }
// crate::filter::names
pub fn unpack_wire(packed: &[u8], symbols: u8) -> Box<[u8]>;
// crate::runtime::Runtime
pub filter_calibration: Calibration
```

- [ ] Add to `engine/tests/telemetry_export.rs` (and add `filter_list: NO_FILTER_LIST, filter_generation: 0` to `record()`, `None` as the fifth `log_record` argument in `log_record_attributes_and_trace_rules`, and in the metrics test replace `"nexora_filter_blocked_total",` with `"nexora_filter_blocked_total{category=\"custom\"} 0", "nexora_filter_index_entries 0", "nexora_filter_index_bytes", "nexora_filter_index_max_bytes", "nexora_filter_index_build_seconds",`):
  ```rust
  #[test]
  fn blocked_records_carry_list_id_and_category() {
      use nexora_engine::filter::index::{ListKind, ListMeta};
      let meta = ListMeta {
          id: "0b6c3e2a-2d57-4a43-9a52-8f0e8bb3c1d1".into(),
          category: "gambling".into(),
          category_slot: 1,
          kind: ListKind::Block,
          invalid_lines: 0,
      };
      let mut r = record(0);
      r.filter = FilterOutcome::Blocked;
      r.filter_list = 0;
      let attrs = |lr: &opentelemetry_proto::tonic::logs::v1::LogRecord| {
          lr.attributes
              .iter()
              .filter(|kv| kv.key.starts_with("nexora.filter."))
              .map(|kv| (kv.key.clone(), format!("{:?}", kv.value)))
              .collect::<Vec<_>>()
      };
      let with = attrs(&log_record(&r, "fixture", "engine-uuid", "", Some(&meta)));
      assert_eq!(with.len(), 2, "{with:?}");
      assert!(with[0].0 == "nexora.filter.list_id" && with[0].1.contains("0b6c3e2a-2d57-4a43-9a52-8f0e8bb3c1d1"), "{with:?}");
      assert!(with[1].0 == "nexora.filter.category" && with[1].1.contains("gambling"), "{with:?}");
      assert!(attrs(&log_record(&record(0), "fixture", "engine-uuid", "", None)).is_empty(), "unblocked records carry no attribution");
  }
  ```
  and to `engine/src/filter/calibrate.rs` (created with only this module):
  ```rust
  #[cfg(test)]
  mod tests {
      use super::*;
      use crate::filter::index::{FilterIndex, IndexOptions, ListInput, ListKind};

      const RK3588: &str = "processor\t: 0\nCPU part\t: 0xd05\n\nprocessor\t: 1\nCPU part\t: 0xd05\n\nprocessor\t: 4\nCPU part\t: 0xd0b\n\nprocessor\t: 5\nCPU part\t: 0xd0b\n";

      #[test]
      fn picks_the_fastest_allowed_cores() {
          assert_eq!(fastest_cores(RK3588, &[0, 1, 4, 5]), (vec![4, 5], "cortex-a76"));
          assert_eq!(fastest_cores(RK3588, &[0, 1]), (vec![0, 1], "cortex-a55"));
          assert_eq!(fastest_cores("", &[0, 1]), (vec![], ARCH_LABEL));
          assert_eq!(cpu_label("0xd08"), "cortex-a72");
      }

      #[test]
      fn measures_listed_and_clean_names() {
          let text: String = (0..5_000).map(|i| format!("n{i}.calibrate.test\n")).collect();
          let input = [ListInput { id: "l", category: "", category_slot: 0, kind: ListKind::Block, text: text.as_bytes() }];
          let index = Arc::new(FilterIndex::build(&input, &IndexOptions::new(16 << 20)).unwrap());
          let samples = index.sample_names(100);
          assert!(samples.len() >= 50);
          let view = index.view(&[0], &[]);
          assert!(samples.iter().all(|n| matches!(view.decide(n), crate::filter::FilterDecision::Blocked(_))), "samples decode to listed names");
          let c = measure(&index);
          assert!(c.blocked_ns > 0.0 && c.clean_ns > 0.0 && !c.cpu.is_empty(), "{c:?}");
          assert_eq!(measure(&Arc::new(FilterIndex::empty())), Calibration::default());
      }
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test telemetry_export && cargo test --locked -p nexora-engine --lib filter::calibrate'` and expect FAIL with `` struct `QueryRecord` has no field named `filter_list` ``.
- [ ] Implement:
  - `QueryRecord` gains `filter_list: u16` (`NO_FILTER_LIST` unless blocked) and `filter_generation: u64`; `Scope::record` sets them to `NO_FILTER_LIST` and 0.
  - `server/mod.rs` fast path: `Verdict::Blocked(hit) => { let c = ctx.counters(); c.filter_blocked.fetch_add(1, Ordering::Relaxed); c.count_categories(policy.filter().categories(hit)); rec.filter = FilterOutcome::Blocked; rec.filter_list = hit.list; rec.filter_generation = policy.filter().index().generation(); ... }`; `leader_answer` does the same for a cloaked hit.
  - `WorkerCounters.filter_blocked_category: [Counter; CATEGORY_SLOTS]` initialised with `array::from_fn(|_| counter())`, and `count_categories` increments `filter_blocked_category[slots.trailing_zeros()]` for each set bit (no allocation).
  - `metrics.rs` `render`: remove the unlabeled `nexora_filter_blocked` registration; register a `Family<Labels, PromCounter>` named `nexora_filter_blocked` ("Blocked queries by category of the matching lists; a name in several categories counts in each") with one child per `filter::lists::category_names()` entry, value `self.sum(|w| load(&w.filter_blocked_category[slot]))`; gauges `nexora_filter_index_entries` (`rt.filter_index.entries()`), `nexora_filter_index_bytes` (`rt.filter_memory_bytes`), `nexora_filter_index_max_bytes` (`rt.filter_max_bytes`), `nexora_filter_index_build_seconds` (`ConstGauge::<f64>` of `rt.filter_index.build_seconds()`); and, when `rt.filter_calibration.cpu` is non-empty, a `Family<Labels, Gauge<f64, AtomicU64>>` `nexora_filter_index_decision_seconds` with children `{kind="blocked",cpu}` and `{kind="clean",cpu}` set to the nanoseconds divided by 1e9. `stats()` fills `filter_index: Some(FilterIndexStats { entries, bytes: rt.filter_memory_bytes, max_bytes: rt.filter_max_bytes, build_seconds, decision_ns_blocked, decision_ns_clean, cpu, blocked_by_category })` from the same sums.
  - `otlp.rs` `log_record(..., filter_list: Option<&ListMeta>)` appends `kv("nexora.filter.list_id", &*meta.id)` and `kv("nexora.filter.category", &*meta.category)` when `Some`; `drain` passes `(r.filter_list != NO_FILTER_LIST && rt.filter_index.generation() == r.filter_generation).then(|| rt.filter_index.lists().get(usize::from(r.filter_list))).flatten()`, so a record never names a list of another index build.
  - `names::unpack_wire`: map symbols back through `const CHARS: &[u8; 39] = b"\0abcdefghijklmnopqrstuvwxyz0123456789-_";`, split on `SEP` (labels come TLD first) and write the labels in reverse order as an uncompressed wire name with the root octet.
  - `FilterIndex::sample_names(n)`: step through primary blocks by `max(1, block_count / n)`, decode the first non-`LONG` entry of each non-empty block with `unpack_wire`, stop at `n`.
  - `calibrate::cpu_label`: `0xd0b` → `cortex-a76`, `0xd05` → `cortex-a55`, `0xd08` → `cortex-a72`, `0xd03` → `cortex-a53`, `0xd0c` → `neoverse-n1`, anything else → `ARCH_LABEL` (`pub const ARCH_LABEL: &str = std::env::consts::ARCH;`); `fastest_cores` parses `processor`/`CPU part` pairs, ranks `cortex-a76`/`neoverse-n1` 4, `cortex-a72` 3, `cortex-a55` 2, `cortex-a53` 1, keeps the allowed cores of the best rank; no parts gives `(vec![], ARCH_LABEL)`.
  - `calibrate::measure`: return `Calibration::default()` for an empty index; otherwise build the all-block-lists view, take `sample_names(4096)` as blocked names and `cdn{i}.img{i % 97}.nexora-calibrate.com` for `i in 0..4096` (those decided `None`) as clean names, and run on a thread named `nexora-filter-calibrate` that on Linux reads `/proc/cpuinfo` and its `sched_getaffinity` mask, pins itself to the cores from `fastest_cores` with `libc::sched_setaffinity` (skipped when the list is empty), and records the median of five passes per kind; the thread is joined before returning.
  - `runtime.rs`: `filter_calibration` is copied from `previous` when the index was reused, else `calibrate::measure(&filter_index)`.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --all-targets && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'` and expect every test to pass, including `blocked_records_carry_list_id_and_category`, `picks_the_fastest_allowed_cores`, `measures_listed_and_clean_names`, `cache_hit_path_does_not_allocate` and the metrics test with the new names.
- As built (2026-09-14), deviations: both block paths call `server::record_block(ctx, policy, hit, rec)`; `names::unpack_wire` unpacks the as-built 7-bit wire octets (not 6-bit symbols) and `sample_names` skips long entries and set-0 markers; `FilterIndexStats.build_seconds` reports the build that produced the current index (also when reused); `nexora_filter_index_decision_seconds` labels are `kind` then `cpu`; `calibrate::measure` runs only when the index is rebuilt (reuse copies the previous calibration). Tests: `cargo test --locked -p nexora-engine --test telemetry_export` (6 passed), `--lib filter::calibrate` (2 passed).
- [ ] Commit: `git add engine/src && git add engine/tests/telemetry_export.rs && git commit -m "feat(engine): filter attribution in query logs, per-category block counters and index metrics"`.

## Task 9: The embedded, read-only category catalog

Files: `mgmt/internal/catalog/catalog.yaml` (categories and sources with license metadata), `mgmt/internal/catalog/catalog.go` (embed, parse, validate, lookups), `mgmt/internal/catalog/catalog_test.go` (catalog content and validation rules)
Interfaces:

```go
package catalog
type Catalog struct { Version int `yaml:"version"`; Categories []Category `yaml:"categories"` }
type Category struct { Key string `yaml:"key"`; Name string `yaml:"name"`; Description string `yaml:"description"`; Sources []Source `yaml:"sources"` }
type Source struct {
	Key string `yaml:"key"`; Name string `yaml:"name"`; URL string `yaml:"url"`; Format string `yaml:"format"`
	ArchiveMember string `yaml:"archive_member"`; License string `yaml:"license"`; LicenseURL string `yaml:"license_url"`
	Attribution string `yaml:"attribution"`; CommercialUse bool `yaml:"commercial_use"`; Notice string `yaml:"notice"`
	DefaultEnabled bool `yaml:"default_enabled"`; RefreshIntervalSeconds int `yaml:"refresh_interval_seconds"`
}
func Load() (*Catalog, error)                 // the embedded catalog.yaml, validated
func Parse(data []byte) (*Catalog, error)
func Digest(data []byte) string               // sha256 hex, recorded by Sync
func Raw() []byte                             // the embedded bytes
func (c *Catalog) Category(key string) (Category, bool)
func (c *Catalog) Source(categoryKey, sourceKey string) (Source, bool)
func (c *Catalog) Position(categoryKey, sourceKey string) int   // 1-based catalog order, 0 when unknown
func ListName(categoryKey, sourceKey string) string             // "catalog:<category>:<source>"
```

- [ ] Write the failing test `mgmt/internal/catalog/catalog_test.go`:
  ```go
  package catalog_test

  import (
  	"bufio"
  	"os"
  	"strings"
  	"testing"

  	"github.com/piwi3910/nexora/mgmt/internal/catalog"
  )

  func TestEmbeddedCatalogIsValid(t *testing.T) {
  	c, err := catalog.Load()
  	if err != nil {
  		t.Fatal(err)
  	}
  	var keys []string
  	for _, cat := range c.Categories {
  		keys = append(keys, cat.Key)
  	}
  	want := "malware,phishing,ads-tracking,adult,gambling,social,crypto-mining,piracy,drugs,fake-news,dating,games"
  	if strings.Join(keys, ",") != want {
  		t.Fatalf("categories %v, want %s", keys, want)
  	}
  	defaults := map[string]catalog.Source{}
  	for _, cat := range c.Categories {
  		for _, s := range cat.Sources {
  			if strings.HasPrefix(s.Key, "oisd-") && (s.CommercialUse || !strings.Contains(s.Notice, "not free for commercial use")) {
  				t.Errorf("%s must be flagged commercial_use: false with the OISD notice", s.Key)
  			}
  			if strings.HasPrefix(s.Key, "ut1-") && !strings.HasPrefix(s.ArchiveMember, "blacklists/") {
  				t.Errorf("%s needs a blacklists/<category>/domains archive member", s.Key)
  			}
  			if s.DefaultEnabled {
  				defaults[s.Key] = s
  			}
  		}
  	}
  	if _, ok := c.Source("ads-tracking", "oisd-big"); !ok || c.Position("malware", "hagezi-tif") != 1 {
  		t.Fatal("lookups by category and source")
  	}
  	// bench/filter/corpus-5m.tsv is the default selection the filter index budgets were measured on.
  	f, err := os.Open("../../../bench/filter/corpus-5m.tsv")
  	if err != nil {
  		t.Fatal(err)
  	}
  	defer f.Close()
  	seen := 0
  	sc := bufio.NewScanner(f)
  	for sc.Scan() {
  		line := sc.Text()
  		if line == "" || strings.HasPrefix(line, "#") {
  			continue
  		}
  		cols := strings.Split(line, "\t")
  		s, ok := defaults[cols[0]]
  		member := cols[2]
  		if member == "-" {
  			member = ""
  		}
  		if !ok || s.URL != cols[1] || s.ArchiveMember != member {
  			t.Errorf("corpus row %q does not match a default-enabled catalog source", line)
  		}
  		seen++
  	}
  	if seen != len(defaults) {
  		t.Errorf("corpus lists %d sources, catalog enables %d by default", seen, len(defaults))
  	}
  }

  func TestParseRejectsInvalidCatalogs(t *testing.T) {
  	valid := `version: 1
  categories:
    - key: gambling
      name: Gambling
      description: Betting sites.
      sources:
        - key: hagezi-gambling
          name: HaGeZi Gambling
          url: https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/gambling-onlydomains.txt
          format: domains
          license: GPL-3.0
          license_url: https://github.com/hagezi/dns-blocklists/blob/main/LICENSE
          attribution: HaGeZi
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 43200
  `
  	if _, err := catalog.Parse([]byte(valid)); err != nil {
  		t.Fatalf("valid catalog rejected: %v", err)
  	}
  	for _, c := range []struct{ from, to, want string }{
  		{"version: 1", "version: 2", "catalog version 2"},
  		{"key: gambling", "key: Gambling", "category key Gambling"},
  		{"url: https://raw", "url: http://raw", "source hagezi-gambling: url must be https"},
  		{"format: domains", "format: adblock", "source hagezi-gambling: format adblock"},
  		{"license: GPL-3.0", "license: \"\"", "source hagezi-gambling: license, license_url and attribution are required"},
  		{"commercial_use: true", "commercial_use: false", "source hagezi-gambling: commercial_use false needs a notice"},
  		{"refresh_interval_seconds: 43200", "refresh_interval_seconds: 60", "source hagezi-gambling: refresh_interval_seconds must be at least 3600"},
  		{"format: domains", "format: domains\n        archive_member: blacklists/gambling/domains", "source hagezi-gambling: archive_member needs a .tar.gz url"},
  	} {
  		_, err := catalog.Parse([]byte(strings.Replace(valid, c.from, c.to, 1)))
  		if err == nil || !strings.Contains(err.Error(), c.want) {
  			t.Errorf("%s -> %v, want %q", c.to, err, c.want)
  		}
  	}
  	dup := strings.Replace(valid, "sources:", "sources:\n      - key: hagezi-gambling\n        name: again\n        url: https://x.test/a.txt\n        format: domains\n        license: MIT\n        license_url: https://x.test/l\n        attribution: x\n        commercial_use: true\n        refresh_interval_seconds: 3600", 1)
  	if _, err := catalog.Parse([]byte(dup)); err == nil || !strings.Contains(err.Error(), "duplicate source key hagezi-gambling") {
  		t.Errorf("duplicate source -> %v", err)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/catalog/ -count=1` and expect FAIL with `no required module provides package github.com/piwi3910/nexora/mgmt/internal/catalog` (or `undefined: catalog.Load` once the directory exists).
- [ ] Create `mgmt/internal/catalog/catalog.yaml` (every URL answered HTTP 200 to `curl -sIL` on 2026-09-14; UT1 members were listed from the archive that day):
  ```yaml
  # Nexora filter category catalog. Read-only for operators: changes ship with Nexora releases.
  # format: domains (one name per line), hosts (sink address and name), wildcard ("*." prefix).
  version: 1
  categories:
    - key: malware
      name: Malware and threat intelligence
      description: Domains that spread malware, run command-and-control servers, ransomware or cryptojacking.
      sources:
        - key: hagezi-tif
          name: HaGeZi Threat Intelligence Feeds
          url: https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/tif-onlydomains.txt
          format: domains
          license: GPL-3.0
          license_url: https://github.com/hagezi/dns-blocklists/blob/main/LICENSE
          attribution: HaGeZi DNS Blocklists (github.com/hagezi/dns-blocklists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 43200
        - key: urlhaus
          name: abuse.ch URLhaus host file
          url: https://urlhaus.abuse.ch/downloads/hostfile/
          format: hosts
          license: abuse.ch Terms of Use
          license_url: https://abuse.ch/terms-of-use/
          attribution: URLhaus by abuse.ch
          commercial_use: false
          notice: abuse.ch data is free for non-commercial use; commercial or for-profit use may require a paid subscription (abuse.ch Terms of Use, section 4).
          default_enabled: true
          refresh_interval_seconds: 21600
        - key: blp-ransomware
          name: Block List Project Ransomware
          url: https://blocklistproject.github.io/Lists/alt-version/ransomware-nl.txt
          format: domains
          license: Unlicense
          license_url: https://github.com/blocklistproject/Lists/blob/master/LICENSE
          attribution: The Block List Project (github.com/blocklistproject/Lists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
        - key: blp-malware
          name: Block List Project Malware
          url: https://blocklistproject.github.io/Lists/alt-version/malware-nl.txt
          format: domains
          license: Unlicense
          license_url: https://github.com/blocklistproject/Lists/blob/master/LICENSE
          attribution: The Block List Project (github.com/blocklistproject/Lists)
          commercial_use: true
          default_enabled: false
          refresh_interval_seconds: 86400
        - key: ut1-malware
          name: UT1 malware
          url: https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz
          format: domains
          archive_member: blacklists/malware/domains
          license: CC-BY-SA-4.0
          license_url: https://creativecommons.org/licenses/by-sa/4.0/
          attribution: Blacklists UT1, Université Toulouse Capitole (dsi.ut-capitole.fr/blacklists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
    - key: phishing
      name: Phishing and fraud
      description: Credential phishing, scams, fraud and fake shops.
      sources:
        - key: blp-phishing
          name: Block List Project Phishing
          url: https://blocklistproject.github.io/Lists/alt-version/phishing-nl.txt
          format: domains
          license: Unlicense
          license_url: https://github.com/blocklistproject/Lists/blob/master/LICENSE
          attribution: The Block List Project (github.com/blocklistproject/Lists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
        - key: blp-fraud
          name: Block List Project Fraud
          url: https://blocklistproject.github.io/Lists/alt-version/fraud-nl.txt
          format: domains
          license: Unlicense
          license_url: https://github.com/blocklistproject/Lists/blob/master/LICENSE
          attribution: The Block List Project (github.com/blocklistproject/Lists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
        - key: blp-scam
          name: Block List Project Scam
          url: https://blocklistproject.github.io/Lists/alt-version/scam-nl.txt
          format: domains
          license: Unlicense
          license_url: https://github.com/blocklistproject/Lists/blob/master/LICENSE
          attribution: The Block List Project (github.com/blocklistproject/Lists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
        - key: hagezi-fake
          name: HaGeZi Fake
          url: https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/fake-onlydomains.txt
          format: domains
          license: GPL-3.0
          license_url: https://github.com/hagezi/dns-blocklists/blob/main/LICENSE
          attribution: HaGeZi DNS Blocklists (github.com/hagezi/dns-blocklists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 43200
        - key: ut1-phishing
          name: UT1 phishing
          url: https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz
          format: domains
          archive_member: blacklists/phishing/domains
          license: CC-BY-SA-4.0
          license_url: https://creativecommons.org/licenses/by-sa/4.0/
          attribution: Blacklists UT1, Université Toulouse Capitole (dsi.ut-capitole.fr/blacklists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
    - key: ads-tracking
      name: Ads and tracking
      description: Advertising, tracking, telemetry and analytics domains.
      sources:
        - key: hagezi-pro
          name: HaGeZi Multi PRO
          url: https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/pro-onlydomains.txt
          format: domains
          license: GPL-3.0
          license_url: https://github.com/hagezi/dns-blocklists/blob/main/LICENSE
          attribution: HaGeZi DNS Blocklists (github.com/hagezi/dns-blocklists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 43200
        - key: oisd-big
          name: OISD big
          url: https://big.oisd.nl/domainswild
          format: wildcard
          license: GPL-3.0
          license_url: https://github.com/sjhgvr/oisd/blob/main/LICENSE
          attribution: oisd by Stephan van Ruth (oisd.nl)
          commercial_use: false
          notice: OISD lists are not free for commercial use. Enable them only on non-commercial networks, or after getting permission from the OISD maintainer (oisd.nl).
          default_enabled: true
          refresh_interval_seconds: 86400
        - key: blp-ads
          name: Block List Project Ads
          url: https://blocklistproject.github.io/Lists/alt-version/ads-nl.txt
          format: domains
          license: Unlicense
          license_url: https://github.com/blocklistproject/Lists/blob/master/LICENSE
          attribution: The Block List Project (github.com/blocklistproject/Lists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
        - key: blp-tracking
          name: Block List Project Tracking
          url: https://blocklistproject.github.io/Lists/alt-version/tracking-nl.txt
          format: domains
          license: Unlicense
          license_url: https://github.com/blocklistproject/Lists/blob/master/LICENSE
          attribution: The Block List Project (github.com/blocklistproject/Lists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
    - key: adult
      name: Adult content
      description: Pornography and other adult content.
      sources:
        - key: hagezi-nsfw
          name: HaGeZi NSFW
          url: https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/nsfw-onlydomains.txt
          format: domains
          license: GPL-3.0
          license_url: https://github.com/hagezi/dns-blocklists/blob/main/LICENSE
          attribution: HaGeZi DNS Blocklists (github.com/hagezi/dns-blocklists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 43200
        - key: oisd-nsfw
          name: OISD NSFW
          url: https://nsfw.oisd.nl/domainswild
          format: wildcard
          license: GPL-3.0
          license_url: https://github.com/sjhgvr/oisd/blob/main/LICENSE
          attribution: oisd by Stephan van Ruth (oisd.nl)
          commercial_use: false
          notice: OISD lists are not free for commercial use. Enable them only on non-commercial networks, or after getting permission from the OISD maintainer (oisd.nl).
          default_enabled: true
          refresh_interval_seconds: 86400
        - key: blp-porn
          name: Block List Project Porn
          url: https://blocklistproject.github.io/Lists/alt-version/porn-nl.txt
          format: domains
          license: Unlicense
          license_url: https://github.com/blocklistproject/Lists/blob/master/LICENSE
          attribution: The Block List Project (github.com/blocklistproject/Lists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
        - key: stevenblack-porn
          name: StevenBlack porn extension
          url: https://raw.githubusercontent.com/StevenBlack/hosts/master/alternates/porn-only/hosts
          format: hosts
          license: MIT
          license_url: https://github.com/StevenBlack/hosts/blob/master/license.txt
          attribution: StevenBlack/hosts extensions (github.com/StevenBlack/hosts)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
        - key: ut1-adult
          name: UT1 adult
          url: https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz
          format: domains
          archive_member: blacklists/adult/domains
          license: CC-BY-SA-4.0
          license_url: https://creativecommons.org/licenses/by-sa/4.0/
          attribution: Blacklists UT1, Université Toulouse Capitole (dsi.ut-capitole.fr/blacklists)
          commercial_use: true
          default_enabled: false
          refresh_interval_seconds: 86400
    - key: gambling
      name: Gambling
      description: Online casinos, betting and lotteries.
      sources:
        - key: hagezi-gambling
          name: HaGeZi Gambling
          url: https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/gambling-onlydomains.txt
          format: domains
          license: GPL-3.0
          license_url: https://github.com/hagezi/dns-blocklists/blob/main/LICENSE
          attribution: HaGeZi DNS Blocklists (github.com/hagezi/dns-blocklists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 43200
        - key: blp-gambling
          name: Block List Project Gambling
          url: https://blocklistproject.github.io/Lists/alt-version/gambling-nl.txt
          format: domains
          license: Unlicense
          license_url: https://github.com/blocklistproject/Lists/blob/master/LICENSE
          attribution: The Block List Project (github.com/blocklistproject/Lists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
        - key: stevenblack-gambling
          name: StevenBlack gambling extension
          url: https://raw.githubusercontent.com/StevenBlack/hosts/master/alternates/gambling-only/hosts
          format: hosts
          license: MIT
          license_url: https://github.com/StevenBlack/hosts/blob/master/license.txt
          attribution: StevenBlack/hosts extensions (github.com/StevenBlack/hosts)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
        - key: ut1-gambling
          name: UT1 gambling
          url: https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz
          format: domains
          archive_member: blacklists/gambling/domains
          license: CC-BY-SA-4.0
          license_url: https://creativecommons.org/licenses/by-sa/4.0/
          attribution: Blacklists UT1, Université Toulouse Capitole (dsi.ut-capitole.fr/blacklists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
    - key: social
      name: Social networks
      description: Social networks and their content delivery domains.
      sources:
        - key: hagezi-social
          name: HaGeZi Social Networks
          url: https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/social-onlydomains.txt
          format: domains
          license: GPL-3.0
          license_url: https://github.com/hagezi/dns-blocklists/blob/main/LICENSE
          attribution: HaGeZi DNS Blocklists (github.com/hagezi/dns-blocklists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 43200
        - key: stevenblack-social
          name: StevenBlack social extension
          url: https://raw.githubusercontent.com/StevenBlack/hosts/master/alternates/social-only/hosts
          format: hosts
          license: MIT
          license_url: https://github.com/StevenBlack/hosts/blob/master/license.txt
          attribution: StevenBlack/hosts extensions (github.com/StevenBlack/hosts)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
        - key: ut1-social-networks
          name: UT1 social networks
          url: https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz
          format: domains
          archive_member: blacklists/social_networks/domains
          license: CC-BY-SA-4.0
          license_url: https://creativecommons.org/licenses/by-sa/4.0/
          attribution: Blacklists UT1, Université Toulouse Capitole (dsi.ut-capitole.fr/blacklists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
        - key: blp-facebook
          name: Block List Project Facebook
          url: https://blocklistproject.github.io/Lists/alt-version/facebook-nl.txt
          format: domains
          license: Unlicense
          license_url: https://github.com/blocklistproject/Lists/blob/master/LICENSE
          attribution: The Block List Project (github.com/blocklistproject/Lists)
          commercial_use: true
          default_enabled: false
          refresh_interval_seconds: 86400
        - key: blp-tiktok
          name: Block List Project TikTok
          url: https://blocklistproject.github.io/Lists/alt-version/tiktok-nl.txt
          format: domains
          license: Unlicense
          license_url: https://github.com/blocklistproject/Lists/blob/master/LICENSE
          attribution: The Block List Project (github.com/blocklistproject/Lists)
          commercial_use: true
          default_enabled: false
          refresh_interval_seconds: 86400
        - key: blp-twitter
          name: Block List Project Twitter
          url: https://blocklistproject.github.io/Lists/alt-version/twitter-nl.txt
          format: domains
          license: Unlicense
          license_url: https://github.com/blocklistproject/Lists/blob/master/LICENSE
          attribution: The Block List Project (github.com/blocklistproject/Lists)
          commercial_use: true
          default_enabled: false
          refresh_interval_seconds: 86400
    - key: crypto-mining
      name: Crypto mining
      description: Browser crypto miners and cryptojacking.
      sources:
        - key: blp-crypto
          name: Block List Project Crypto
          url: https://blocklistproject.github.io/Lists/alt-version/crypto-nl.txt
          format: domains
          license: Unlicense
          license_url: https://github.com/blocklistproject/Lists/blob/master/LICENSE
          attribution: The Block List Project (github.com/blocklistproject/Lists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
        - key: ut1-cryptojacking # gitleaks:allow (catalog source key, not a credential)
          name: UT1 cryptojacking
          url: https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz
          format: domains
          archive_member: blacklists/cryptojacking/domains
          license: CC-BY-SA-4.0
          license_url: https://creativecommons.org/licenses/by-sa/4.0/
          attribution: Blacklists UT1, Université Toulouse Capitole (dsi.ut-capitole.fr/blacklists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
    - key: piracy
      name: Piracy
      description: Warez, illegal streaming and file sharing sites.
      sources:
        - key: hagezi-anti-piracy
          name: HaGeZi Anti-Piracy
          url: https://raw.githubusercontent.com/hagezi/dns-blocklists/main/wildcard/anti.piracy-onlydomains.txt
          format: domains
          license: GPL-3.0
          license_url: https://github.com/hagezi/dns-blocklists/blob/main/LICENSE
          attribution: HaGeZi DNS Blocklists (github.com/hagezi/dns-blocklists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 43200
        - key: blp-piracy
          name: Block List Project Piracy
          url: https://blocklistproject.github.io/Lists/alt-version/piracy-nl.txt
          format: domains
          license: Unlicense
          license_url: https://github.com/blocklistproject/Lists/blob/master/LICENSE
          attribution: The Block List Project (github.com/blocklistproject/Lists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
        - key: ut1-warez
          name: UT1 warez
          url: https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz
          format: domains
          archive_member: blacklists/warez/domains
          license: CC-BY-SA-4.0
          license_url: https://creativecommons.org/licenses/by-sa/4.0/
          attribution: Blacklists UT1, Université Toulouse Capitole (dsi.ut-capitole.fr/blacklists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
    - key: drugs
      name: Drugs
      description: Sites selling or promoting illegal drugs.
      sources:
        - key: blp-drugs
          name: Block List Project Drugs
          url: https://blocklistproject.github.io/Lists/alt-version/drugs-nl.txt
          format: domains
          license: Unlicense
          license_url: https://github.com/blocklistproject/Lists/blob/master/LICENSE
          attribution: The Block List Project (github.com/blocklistproject/Lists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
        - key: ut1-drogue
          name: UT1 drugs
          url: https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz
          format: domains
          archive_member: blacklists/drogue/domains
          license: CC-BY-SA-4.0
          license_url: https://creativecommons.org/licenses/by-sa/4.0/
          attribution: Blacklists UT1, Université Toulouse Capitole (dsi.ut-capitole.fr/blacklists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
    - key: fake-news
      name: Fake news
      description: Sites known for publishing fabricated news.
      sources:
        - key: stevenblack-fakenews
          name: StevenBlack fake news extension
          url: https://raw.githubusercontent.com/StevenBlack/hosts/master/alternates/fakenews-only/hosts
          format: hosts
          license: MIT
          license_url: https://github.com/StevenBlack/hosts/blob/master/license.txt
          attribution: StevenBlack/hosts extensions (github.com/StevenBlack/hosts)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
        - key: ut1-fakenews
          name: UT1 fake news
          url: https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz
          format: domains
          archive_member: blacklists/fakenews/domains
          license: CC-BY-SA-4.0
          license_url: https://creativecommons.org/licenses/by-sa/4.0/
          attribution: Blacklists UT1, Université Toulouse Capitole (dsi.ut-capitole.fr/blacklists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
    - key: dating
      name: Dating
      description: Online dating services.
      sources:
        - key: ut1-dating
          name: UT1 dating
          url: https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz
          format: domains
          archive_member: blacklists/dating/domains
          license: CC-BY-SA-4.0
          license_url: https://creativecommons.org/licenses/by-sa/4.0/
          attribution: Blacklists UT1, Université Toulouse Capitole (dsi.ut-capitole.fr/blacklists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
    - key: games
      name: Online games
      description: Online gaming platforms and browser games.
      sources:
        - key: ut1-games
          name: UT1 games
          url: https://dsi.ut-capitole.fr/blacklists/download/blacklists.tar.gz
          format: domains
          archive_member: blacklists/games/domains
          license: CC-BY-SA-4.0
          license_url: https://creativecommons.org/licenses/by-sa/4.0/
          attribution: Blacklists UT1, Université Toulouse Capitole (dsi.ut-capitole.fr/blacklists)
          commercial_use: true
          default_enabled: true
          refresh_interval_seconds: 86400
        - key: blp-fortnite
          name: Block List Project Fortnite
          url: https://blocklistproject.github.io/Lists/alt-version/fortnite-nl.txt
          format: domains
          license: Unlicense
          license_url: https://github.com/blocklistproject/Lists/blob/master/LICENSE
          attribution: The Block List Project (github.com/blocklistproject/Lists)
          commercial_use: true
          default_enabled: false
          refresh_interval_seconds: 86400
  ```
- [ ] Create `mgmt/internal/catalog/catalog.go`: `//go:embed catalog.yaml` into `var raw []byte`; `Parse` decodes with `yaml.NewDecoder(bytes.NewReader(data))` and `KnownFields(true)`, then validates and returns the first violation as an error with exactly these messages: `catalog version %d, want 1`; `category key %s must match ^[a-z][a-z0-9-]{0,31}$`; `duplicate category key %s`; `category %s: name, description and at least one source are required`; `source key %s must match ^[a-z0-9][a-z0-9-]{0,47}$`; `duplicate source key %s` (source keys are unique across the catalog, because the mirror path is the source key); `source %s: url must be https`; `source %s: format %s must be domains, hosts or wildcard`; `source %s: archive_member needs a .tar.gz url`; `source %s: license, license_url and attribution are required`; `source %s: commercial_use false needs a notice`; `source %s: refresh_interval_seconds must be at least 3600`; `source %s: list name %s exceeds 64 characters`. `Load()` is `Parse(raw)`; `Digest` is `hex(sha256(data))`; `Position` counts sources across categories in file order from 1; `ListName` returns `"catalog:" + category + ":" + source`.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/catalog/ -count=1` and expect `ok`.
- [ ] Commit: `git add mgmt/internal/catalog && git commit -m "feat(mgmt): embedded filter category catalog with license and attribution metadata"`.

## Task 10: Schema and catalog sync into managed filter lists

Files: `mgmt/migrations/00503_filter_categories.sql` (tables and columns), `mgmt/internal/store/filtercategories.go` (category and catalog-list queries), `mgmt/internal/store/policies.go` (`CategoryKeys`, managed lists excluded from `filter_list_ids`), `mgmt/internal/catalog/sync.go` (`Sync`), `mgmt/internal/catalog/sync_test.go` (database test), `mgmt/cmd/nexora-mgmt/main.go` (sync at start)
Interfaces:

```go
// store
type FilterCategory struct { Key string; Enabled bool; Revision int64; UpdatedAt time.Time }
type CatalogList struct { ID uuid.UUID; CategoryKey, SourceKey string; Enabled bool; EntryCount int; LastSuccessAt *time.Time; LastError string; Stale bool; LicenseAcknowledgedAt *time.Time; CurrentBlobSHA256 *string }
func ListFilterCategories(ctx context.Context, q PolicyQuerier) ([]FilterCategory, error)
func LockFilterCategory(ctx context.Context, tx pgx.Tx, key string) (FilterCategory, error)            // ErrNotFound when absent
func SetFilterCategoryEnabled(ctx context.Context, tx pgx.Tx, key string, enabled bool) (FilterCategory, error) // revision + 1
func ListCatalogLists(ctx context.Context, q PolicyQuerier) ([]CatalogList, error)
func SetCatalogListEnabled(ctx context.Context, tx pgx.Tx, id uuid.UUID, enabled, acknowledged bool) error // acknowledged sets license_acknowledged_at = now()
// PolicyGroup gains: CategoryKeys []string
// catalog
func Sync(ctx context.Context, st *store.Store, build snapshot.BuildConfig, c *Catalog, data []byte) (changed bool, err error)
```

- [ ] Write the failing test `mgmt/internal/catalog/sync_test.go`:
  ```go
  package catalog_test

  import (
  	"context"
  	"strings"
  	"testing"

  	"github.com/piwi3910/nexora/mgmt/internal/catalog"
  	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
  	"github.com/piwi3910/nexora/mgmt/internal/store"
  	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
  )

  func TestSyncCreatesManagedListsAndIsIdempotent(t *testing.T) {
  	ctx := context.Background()
  	st := storetest.New(t)
  	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
  		t.Fatal(err)
  	}
  	c, err := catalog.Load()
  	if err != nil {
  		t.Fatal(err)
  	}
  	changed, err := catalog.Sync(ctx, st, snapshot.BuildConfig{}, c, catalog.Raw())
  	if err != nil || !changed {
  		t.Fatalf("first sync: changed=%v err=%v", changed, err)
  	}
  	cats, err := store.ListFilterCategories(ctx, st.Pool)
  	if err != nil || len(cats) != len(c.Categories) {
  		t.Fatalf("categories %d err %v", len(cats), err)
  	}
  	for _, cat := range cats {
  		if cat.Enabled {
  			t.Errorf("category %s enabled on a new install", cat.Key)
  		}
  	}
  	lists, err := store.ListCatalogLists(ctx, st.Pool)
  	if err != nil {
  		t.Fatal(err)
  	}
  	total := 0
  	for _, cat := range c.Categories {
  		total += len(cat.Sources)
  	}
  	if len(lists) != total {
  		t.Fatalf("managed lists %d, catalog sources %d", len(lists), total)
  	}
  	for _, l := range lists {
  		s, _ := c.Source(l.CategoryKey, l.SourceKey)
  		if l.Enabled != s.DefaultEnabled {
  			t.Errorf("%s enabled=%v, default %v", l.SourceKey, l.Enabled, s.DefaultEnabled)
  		}
  	}
  	var version int64
  	_ = st.Pool.QueryRow(ctx, "select max(version) from config_versions").Scan(&version)
  	if changed, err := catalog.Sync(ctx, st, snapshot.BuildConfig{}, c, catalog.Raw()); err != nil || changed {
  		t.Fatalf("second sync with the same catalog: changed=%v err=%v", changed, err)
  	}
  	var after int64
  	_ = st.Pool.QueryRow(ctx, "select max(version) from config_versions").Scan(&after)
  	if after != version {
  		t.Fatalf("an unchanged catalog published version %d (was %d)", after, version)
  	}
  	// An operator's source toggle survives a release that drops another source.
  	if _, err := st.Pool.Exec(ctx, "update filter_lists set enabled = false where source_key = 'hagezi-gambling'"); err != nil {
  		t.Fatal(err)
  	}
  	trimmed := strings.Replace(string(catalog.Raw()), "key: blp-fortnite", "key: blp-fortnite-removed-in-test", 1)
  	next, err := catalog.Parse([]byte(trimmed))
  	if err != nil {
  		t.Fatal(err)
  	}
  	if changed, err := catalog.Sync(ctx, st, snapshot.BuildConfig{}, next, []byte(trimmed)); err != nil || !changed {
  		t.Fatalf("changed catalog: changed=%v err=%v", changed, err)
  	}
  	var fortnite, renamed int
  	var gambling bool
  	_ = st.Pool.QueryRow(ctx, `select count(*) filter (where source_key = 'blp-fortnite'), count(*) filter (where source_key = 'blp-fortnite-removed-in-test'),
  		bool_or(enabled) filter (where source_key = 'hagezi-gambling') from filter_lists where managed_by_catalog`).Scan(&fortnite, &renamed, &gambling)
  	if fortnite != 0 || renamed != 1 || gambling {
  		t.Fatalf("after sync: blp-fortnite=%d renamed=%d hagezi-gambling enabled=%v", fortnite, renamed, gambling)
  	}
  }
  ```
  and add to `mgmt/internal/store/policies_test.go`:
  ```go
  func TestPolicyGroupCategoryKeysAndManagedListsExcluded(t *testing.T) {
  	s := openStore(t)
  	ctx := context.Background()
  	var managed uuid.UUID
  	if _, err := s.Pool.Exec(ctx, "insert into filter_categories(key) values ('gambling')"); err != nil {
  		t.Fatal(err)
  	}
  	if err := s.Pool.QueryRow(ctx, `insert into filter_lists(name, kind, url, category_key, source_key, managed_by_catalog, catalog_position)
  		values ('catalog:gambling:x', 'block', 'https://x.test/l.txt', 'gambling', 'x', true, 1) returning id`).Scan(&managed); err != nil {
  		t.Fatal(err)
  	}
  	inTx(t, s, func(tx pgx.Tx) {
  		g, err := store.CreatePolicyGroup(ctx, tx, store.PolicyGroup{Name: "kids", CIDRs: []netip.Prefix{netip.MustParsePrefix("10.9.0.0/16")}, CategoryKeys: []string{"gambling"}})
  		if err != nil || len(g.CategoryKeys) != 1 || g.CategoryKeys[0] != "gambling" {
  			t.Fatalf("create with category keys: %+v %v", g, err)
  		}
  		_, err = store.CreatePolicyGroup(ctx, tx, store.PolicyGroup{Name: "kids2", CIDRs: []netip.Prefix{netip.MustParsePrefix("10.8.0.0/16")}, FilterListIDs: []uuid.UUID{managed}})
  		if !errors.Is(err, store.ErrUnknownFilterList) {
  			t.Fatalf("a catalog-managed list selected by id -> %v", err)
  		}
  	})
  }
  ```
  (add the `github.com/google/uuid` import the file lacks; the file's `inTx` helper takes a `func(pgx.Tx) error`, so the built test returns `nil` from the closure and checks the result).
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/catalog/ ./mgmt/internal/store/ -run "TestSync|TestPolicyGroupCategoryKeys" -count=1'` and expect FAIL with `undefined: catalog.Sync`.
- [ ] Create `mgmt/migrations/00503_filter_categories.sql`:
  ```sql
  -- +goose Up
  -- Filter categories: the embedded catalog (mgmt/internal/catalog/catalog.yaml) synced into rows.
  CREATE TABLE filter_categories (
      key        text PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9-]{0,31}$'),
      enabled    boolean NOT NULL DEFAULT false,
      revision   bigint NOT NULL DEFAULT 1,
      updated_at timestamptz NOT NULL DEFAULT now()
  );

  -- One row: the digest of the catalog last synced, so an unchanged catalog publishes nothing.
  CREATE TABLE filter_catalog_state (
      singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
      digest    text NOT NULL DEFAULT ''
  );
  INSERT INTO filter_catalog_state DEFAULT VALUES;

  ALTER TABLE filter_lists
      ADD COLUMN category_key text REFERENCES filter_categories (key) ON DELETE CASCADE,
      ADD COLUMN source_key text,
      ADD COLUMN managed_by_catalog boolean NOT NULL DEFAULT false,
      ADD COLUMN archive_member text NOT NULL DEFAULT '',
      ADD COLUMN catalog_position integer,
      ADD COLUMN license_acknowledged_at timestamptz,
      ADD CONSTRAINT filter_lists_catalog_columns CHECK (
          managed_by_catalog = (category_key IS NOT NULL AND source_key IS NOT NULL AND catalog_position IS NOT NULL)
          AND (managed_by_catalog OR archive_member = '')
          AND (NOT managed_by_catalog OR (kind = 'block' AND engine_group_id IS NULL))
      );
  CREATE UNIQUE INDEX filter_lists_catalog_source ON filter_lists (category_key, source_key) WHERE managed_by_catalog;

  ALTER TABLE policy_groups ADD COLUMN category_keys text[] NOT NULL DEFAULT '{}';

  ALTER TABLE engine_groups ADD COLUMN filter_index_max_bytes bigint NOT NULL DEFAULT 0
      CHECK (filter_index_max_bytes = 0 OR filter_index_max_bytes >= 16777216);

  -- +goose Down
  ALTER TABLE engine_groups DROP COLUMN filter_index_max_bytes;
  ALTER TABLE policy_groups DROP COLUMN category_keys;
  DELETE FROM filter_lists WHERE managed_by_catalog;
  DROP INDEX filter_lists_catalog_source;
  ALTER TABLE filter_lists
      DROP CONSTRAINT filter_lists_catalog_columns,
      DROP COLUMN license_acknowledged_at,
      DROP COLUMN catalog_position,
      DROP COLUMN archive_member,
      DROP COLUMN managed_by_catalog,
      DROP COLUMN source_key,
      DROP COLUMN category_key;
  DROP TABLE filter_catalog_state;
  DROP TABLE filter_categories;
  ```
- [ ] Create `mgmt/internal/store/filtercategories.go` with the Interfaces' functions: `ListFilterCategories` = `select key, enabled, revision, updated_at from filter_categories order by key`; `LockFilterCategory` the same with `where key = $1 for update` (`MapError` turns no rows into `ErrNotFound`); `SetFilterCategoryEnabled` = `update filter_categories set enabled = $2, revision = revision + 1, updated_at = now() where key = $1 returning key, enabled, revision, updated_at`; `ListCatalogLists` = `select id, category_key, source_key, enabled, entry_count, last_success_at, last_error, (last_error <> '' or last_success_at is null or last_success_at < now() - 2 * refresh_interval_seconds * interval '1 second'), license_acknowledged_at, current_blob_sha256 from filter_lists where managed_by_catalog order by catalog_position`; `SetCatalogListEnabled` = `update filter_lists set enabled = $2, license_acknowledged_at = case when $3 then now() else license_acknowledged_at end, revision = revision + 1, updated_at = now() where id = $1 and managed_by_catalog`.
- [ ] In `mgmt/internal/store/policies.go`: add `CategoryKeys []string` to `PolicyGroup`; add `g.category_keys` as the last column of `selectPolicyGroups` and scan it; `CreatePolicyGroup` inserts `category_keys` (`$8`, `nonNil(g.CategoryKeys)` as `[]string{}` when nil) and `UpdatePolicyGroup` sets `category_keys = $10`; `insertGroupChildren` selects `from filter_lists where id = $2 and kind = 'block' and not managed_by_catalog`.
- [ ] Create `mgmt/internal/catalog/sync.go`: `Sync` returns `(false, nil)` without writing when `select digest from filter_catalog_state` equals `Digest(data)`; otherwise it runs `snapshot.Mutate(ctx, st, build, auth.Actor{Type: "system", ID: "filter-catalog", Name: "system"}, fn)` where `fn` executes, in order: `select pg_advisory_xact_lock(hashtext('nexora:catalog'))`; the digest check again (another instance may have synced; then return the sentinel `errUnchanged`, which `Sync` maps to `(false, nil)`); `insert into filter_categories(key) values ($1) on conflict (key) do nothing` per category; per source `insert into filter_lists(name, kind, url, refresh_interval_seconds, enabled, category_key, source_key, managed_by_catalog, archive_member, catalog_position) values ($1, 'block', $2, $3, $4, $5, $6, true, $7, $8) on conflict (category_key, source_key) where managed_by_catalog do update set name = excluded.name, url = excluded.url, refresh_interval_seconds = excluded.refresh_interval_seconds, archive_member = excluded.archive_member, catalog_position = excluded.catalog_position` (the insert uses `DefaultEnabled`; an update never touches `enabled`); `delete from filter_lists where managed_by_catalog and not (category_key || ':' || source_key = any($1))` with the current pairs; `update policy_groups set category_keys = array(select k from unnest(category_keys) k where k = any($1))` and `delete from filter_categories where not (key = any($1))` with the current category keys; `update filter_catalog_state set digest = $1`; and returns `auth.Change{Action: "syncFilterCatalog", TargetType: "filter_catalog", TargetID: digest, After: map[string]any{"categories": n, "sources": m}}`.
- [ ] In `mgmt/cmd/nexora-mgmt/main.go`, before `fetcher := blocklist.NewFetcher(...)`: `cat, err := catalog.Load(); if err != nil { return fmt.Errorf("filter catalog: %w", err) }; if _, err := catalog.Sync(ctx, st, build, cat, catalog.Raw()); err != nil { return fmt.Errorf("filter catalog sync: %w", err) }`.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/catalog/ ./mgmt/internal/store/ -count=1 && go test ./mgmt/... -count=1 -run TestFleetMigration'` and expect `ok` for all packages.
- [ ] Commit: `git add mgmt/migrations/00503_filter_categories.sql mgmt/internal/store/filtercategories.go mgmt/internal/store/policies.go mgmt/internal/store/policies_test.go mgmt/internal/catalog/sync.go mgmt/internal/catalog/sync_test.go mgmt/cmd/nexora-mgmt/main.go && git commit -m "feat(mgmt): filter category schema and catalog sync into managed filter lists"`.

## Task 11: Snapshots carry list identity, categories and the engine group memory cap

Files: `mgmt/internal/snapshot/snapshot.go` (`buildFilterLists`, allowlist ref, `buildPolicy`, engine group cap), `mgmt/internal/snapshot/policy.go` (`PolicyList`, group refs in catalog order), `mgmt/internal/snapshot/policy_test.go` (updated call), `mgmt/internal/snapshot/categories_test.go` (database test)
Interfaces:

```go
// snapshot
type PolicyList struct { ID uuid.UUID; Ref *controlv1.BlobRef; CategoryKey string; Position uint32; Enabled, Managed bool }
func BuildPolicySection(groups []store.PolicyGroup, lists []PolicyList, rewrites []store.Rewrite, global store.SafeSearch) PolicySection
const CustomListPosition = 1_000_000
```

- [ ] Write the failing test `mgmt/internal/snapshot/categories_test.go`:
  ```go
  package snapshot_test

  import (
  	"context"
  	"net/netip"
  	"testing"

  	"github.com/jackc/pgx/v5"

  	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
  	"github.com/piwi3910/nexora/mgmt/internal/catalog"
  	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
  	"github.com/piwi3910/nexora/mgmt/internal/store"
  	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
  )

  func TestCategoryListsInSnapshots(t *testing.T) {
  	ctx := context.Background()
  	st := storetest.New(t)
  	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
  		t.Fatal(err)
  	}
  	cat, err := catalog.Load()
  	if err != nil {
  		t.Fatal(err)
  	}
  	if _, err := catalog.Sync(ctx, st, snapshot.BuildConfig{}, cat, catalog.Raw()); err != nil {
  		t.Fatal(err)
  	}
  	var custom string
  	err = st.InTx(ctx, func(tx pgx.Tx) error {
  		for i, src := range []string{"hagezi-gambling", "blp-gambling", "stevenblack-gambling", "hagezi-nsfw", "ut1-adult"} {
  			sha, _, err := store.PutBlob(ctx, tx, []byte{byte(i), 1, 2})
  			if err != nil {
  				return err
  			}
  			if _, err := tx.Exec(ctx, "update filter_lists set current_blob_sha256 = $1 where source_key = $2", sha, src); err != nil {
  				return err
  			}
  		}
  		sha, _, err := store.PutBlob(ctx, tx, []byte("custom"))
  		if err != nil {
  			return err
  		}
  		if err := tx.QueryRow(ctx, `insert into filter_lists(name, kind, url, current_blob_sha256) values ('my-list', 'block', 'https://x.test/a', $1) returning id::text`, sha).Scan(&custom); err != nil {
  			return err
  		}
  		if _, err := tx.Exec(ctx, "update filter_categories set enabled = true where key = 'gambling'"); err != nil {
  			return err
  		}
  		if _, err := tx.Exec(ctx, "update filter_lists set enabled = false where source_key = 'blp-gambling'"); err != nil {
  			return err
  		}
  		if _, err := tx.Exec(ctx, "update engine_groups set filter_index_max_bytes = 67108864 where id = $1", store.DefaultEngineGroupID); err != nil {
  			return err
  		}
  		_, err = store.CreatePolicyGroup(ctx, tx, store.PolicyGroup{Name: "kids", CIDRs: []netip.Prefix{netip.MustParsePrefix("10.7.0.0/16")}, CategoryKeys: []string{"adult"}})
  		return err
  	})
  	if err != nil {
  		t.Fatal(err)
  	}
  	var snap *controlv1.ConfigSnapshot
  	if err := st.InTx(ctx, func(tx pgx.Tx) error {
  		var err error
  		snap, err = snapshot.BuildForGroup(ctx, tx, 99, snapshot.BuildConfig{}, store.DefaultEngineGroupID)
  		return err
  	}); err != nil {
  		t.Fatal(err)
  	}
  	refs := snap.Filter.BlocklistRefs
  	got := map[string]*controlv1.FilterListRef{}
  	for _, r := range refs {
  		got[r.Blob.Name] = r
  	}
  	if len(refs) != 3 || got["catalog:gambling:hagezi-gambling"] == nil || got["catalog:gambling:stevenblack-gambling"] == nil || got["my-list"] == nil {
  		t.Fatalf("global refs %v: enabled gambling sources plus the custom list", refs)
  	}
  	if got["catalog:gambling:hagezi-gambling"].Category != "gambling" || got["my-list"].Category != "" || got["my-list"].ListId != custom {
  		t.Fatalf("identity: %v", refs)
  	}
  	if refs[0].Position >= refs[1].Position || refs[2].Position < snapshot.CustomListPosition {
  		t.Fatalf("catalog order first, custom lists after: %v", refs)
  	}
  	if len(snap.Filter.Blocklists) != len(refs) {
  		t.Fatalf("M1 blocklists %d must mirror %d refs for older engines", len(snap.Filter.Blocklists), len(refs))
  	}
  	if len(snap.PolicyGroups) != 1 || len(snap.PolicyGroups[0].BlocklistRefs) != 1 || snap.PolicyGroups[0].BlocklistRefs[0].Blob.Name != "catalog:adult:hagezi-nsfw" {
  		t.Fatalf("group refs %v: only the enabled adult source with a blob", snap.PolicyGroups)
  	}
  	if snap.FilterIndexMaxBytes != 67108864 {
  		t.Fatalf("filter_index_max_bytes = %d", snap.FilterIndexMaxBytes)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/snapshot/ -run TestCategoryListsInSnapshots -count=1` and expect FAIL with `undefined: snapshot.CustomListPosition`.
- [ ] In `snapshot.go` `BuildForGroup`, read `filter_index_max_bytes` together with `upstream_mode` from `engine_groups` into `snap.FilterIndexMaxBytes`. Replace the `buildFilterLists` query with:
  ```sql
  select f.id::text, f.name, f.kind, f.current_blob_sha256, b.size, coalesce(f.category_key, ''),
      coalesce(f.catalog_position, 0), f.managed_by_catalog
  from filter_lists f
  join blobs b on b.sha256 = f.current_blob_sha256
  left join filter_categories c on c.key = f.category_key
  where f.enabled and f.current_blob_sha256 is not null
    and (f.engine_group_id is null or f.engine_group_id = $1)
    and (not f.managed_by_catalog or c.enabled)
  order by f.managed_by_catalog desc, f.catalog_position, f.name
  ```
  and for each row append the `BlobRef` to `Blocklists`/`Allowlists` as today and a `FilterListRef{ListId: id, Category: category, Position: pos, Blob: ref}` to `BlocklistRefs`/`AllowlistRefs`, where `pos` is the catalog position for managed lists and `CustomListPosition + rank` (rank = 0-based order among custom lists of this snapshot) otherwise; the stored allowlist blob gets the ref `ListId: "allowlist"`, `Position: CustomListPosition + 999_999`.
- [ ] In `buildPolicy`, replace the `listBlobs` query with `select f.id, f.name, b.sha256, b.size, coalesce(f.category_key, ''), coalesce(f.catalog_position, 0), f.enabled, f.managed_by_catalog from filter_lists f join blobs b on b.sha256 = f.current_blob_sha256 where f.kind = 'block' order by f.managed_by_catalog desc, f.catalog_position, f.name` into `[]PolicyList` (custom positions as above), and pass it to `BuildPolicySection`. In `policy.go`, for each group append first every `PolicyList` with `Managed && Enabled` whose `CategoryKey` is in `g.CategoryKeys` (in `lists` order, i.e. catalog order), then the lists named by `g.FilterListIDs` (in the group's order); each one goes to both `pg.Blocklists` and `pg.BlocklistRefs` (`ListId: id.String()`, `Category`, `Position`). Update `policy_test.go` to build `[]snapshot.PolicyList{{ID: id, Ref: ref, Position: snapshot.CustomListPosition}}` instead of the map.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/snapshot/ ./mgmt/internal/blocklist/ -count=1` and expect `ok` (including `TestBuildPolicySection` and the blob garbage-collection tests, which still read the M1 `Blocklists`).
- [ ] Commit: `git add mgmt/internal/snapshot && git commit -m "feat(mgmt): snapshots carry filter list identity, categories and the engine group index cap"`.

## Task 12: Fetcher: wildcard entries, UT1 archive members and the catalog mirror

Files: `mgmt/internal/blocklist/parse.go` (`*.` prefix), `mgmt/internal/blocklist/archive.go` (tar.gz member extraction), `mgmt/internal/blocklist/fetcher.go` (members, mirror, due list includes group categories), `mgmt/internal/blocklist/archive_test.go`, `mgmt/internal/blocklist/parse_test.go`, `mgmt/internal/blocklist/catalog_fetch_test.go`, `mgmt/internal/config/config.go` (`CatalogMirror`), `mgmt/cmd/nexora-mgmt/main.go` (wires the mirror), `docs/operations.md` (`NEXORA_CATALOG_MIRROR`)
Interfaces: `func blocklist.ReadArchiveMember(r io.Reader, member string) ([]string, ParseStats, error)`; `func (f *Fetcher) WithCatalogMirror(base string) *Fetcher`; `config.Config.CatalogMirror string` from `NEXORA_CATALOG_MIRROR` (must be `http://` or `https://` when set); fetch error texts `archive member %s not found` and `read archive: %v`.

- [ ] Write the failing tests. `mgmt/internal/blocklist/archive_test.go`:
  ```go
  package blocklist_test

  import (
  	"archive/tar"
  	"bytes"
  	"compress/gzip"
  	"slices"
  	"strings"
  	"testing"

  	"github.com/piwi3910/nexora/mgmt/internal/blocklist"
  )

  // UT1Archive builds a blacklists.tar.gz with the given member contents.
  func UT1Archive(t *testing.T, members map[string]string) []byte {
  	t.Helper()
  	var buf bytes.Buffer
  	gz := gzip.NewWriter(&buf)
  	tw := tar.NewWriter(gz)
  	for name, body := range members {
  		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
  			t.Fatal(err)
  		}
  		if _, err := tw.Write([]byte(body)); err != nil {
  			t.Fatal(err)
  		}
  	}
  	if err := tw.Close(); err != nil {
  		t.Fatal(err)
  	}
  	if err := gz.Close(); err != nil {
  		t.Fatal(err)
  	}
  	return buf.Bytes()
  }

  func TestReadArchiveMember(t *testing.T) {
  	archive := UT1Archive(t, map[string]string{
  		"blacklists/README":            "UT1",
  		"blacklists/gambling/domains":  "casino.ut1.test\nbet.ut1.test\n",
  		"blacklists/adult/domains":     "adult.ut1.test\n",
  		"blacklists/gambling/urls":     "casino.ut1.test/path\n",
  	})
  	domains, stats, err := blocklist.ReadArchiveMember(bytes.NewReader(archive), "blacklists/gambling/domains")
  	if err != nil || stats.Entries != 2 || !slices.Contains(domains, "casino.ut1.test") || slices.Contains(domains, "adult.ut1.test") {
  		t.Fatalf("member: %v %+v %v", domains, stats, err)
  	}
  	if _, _, err := blocklist.ReadArchiveMember(bytes.NewReader(archive), "blacklists/dating/domains"); err == nil || err.Error() != "archive member blacklists/dating/domains not found" {
  		t.Fatalf("missing member -> %v", err)
  	}
  	if _, _, err := blocklist.ReadArchiveMember(strings.NewReader("not a gzip stream"), "blacklists/gambling/domains"); err == nil || !strings.HasPrefix(err.Error(), "read archive: ") {
  		t.Fatalf("corrupt archive -> %v", err)
  	}
  }
  ```
  In `mgmt/internal/blocklist/parse_test.go` add:
  ```go
  func TestParseStripsWildcardPrefix(t *testing.T) {
  	domains, stats, err := blocklist.Parse(strings.NewReader("# oisd\n*.ads.oisd.test\n*.Tracker.OISD.test\n*.\n"))
  	if err != nil || stats.Entries != 2 || stats.Invalid != 1 || !slices.Equal(domains, []string{"ads.oisd.test", "tracker.oisd.test"}) {
  		t.Fatalf("wildcard: %v %+v %v", domains, stats, err)
  	}
  }
  ```
  `mgmt/internal/blocklist/catalog_fetch_test.go` (internal package, like `gc_test.go`):
  ```go
  package blocklist

  import (
  	"archive/tar"
  	"bytes"
  	"compress/gzip"
  	"context"
  	"net/http"
  	"net/http/httptest"
  	"strings"
  	"sync"
  	"testing"

  	"github.com/piwi3910/nexora/mgmt/internal/catalog"
  	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
  	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
  )

  func TestFetcherUsesCatalogMirrorAndArchiveMembers(t *testing.T) {
  	ctx := context.Background()
  	st := storetest.New(t)
  	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
  		t.Fatal(err)
  	}
  	cat, _ := catalog.Load()
  	if _, err := catalog.Sync(ctx, st, snapshot.BuildConfig{}, cat, catalog.Raw()); err != nil {
  		t.Fatal(err)
  	}
  	var mu sync.Mutex
  	served := map[string][]byte{"hagezi-gambling": []byte("casino.mirror.test\n")}
  	var paths []string
  	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  		mu.Lock()
  		defer mu.Unlock()
  		paths = append(paths, r.URL.Path)
  		body, ok := served[strings.TrimPrefix(r.URL.Path, "/mirror/")]
  		if !ok {
  			http.NotFound(w, r)
  			return
  		}
  		_, _ = w.Write(body)
  	}))
  	defer srv.Close()
  	f := NewFetcher(st, snapshot.BuildConfig{}, srv.Client()).WithCatalogMirror(srv.URL + "/mirror")
  	id := func(source string) string {
  		var v string
  		if err := st.Pool.QueryRow(ctx, "select id::text from filter_lists where source_key = $1", source).Scan(&v); err != nil {
  			t.Fatal(err)
  		}
  		return v
  	}
  	state := func(source string) (entries int, lastError string) {
  		_ = st.Pool.QueryRow(ctx, "select entry_count, last_error from filter_lists where source_key = $1", source).Scan(&entries, &lastError)
  		return
  	}
  	if err := f.refresh(ctx, id("hagezi-gambling"), systemActor, true); err != nil {
  		t.Fatal(err)
  	}
  	if n, e := state("hagezi-gambling"); n != 1 || e != "" || !strings.HasSuffix(paths[0], "/mirror/hagezi-gambling") {
  		t.Fatalf("mirror fetch: entries=%d err=%q paths=%v", n, e, paths)
  	}
  	var buf bytes.Buffer
  	gz := gzip.NewWriter(&buf)
  	tw := tar.NewWriter(gz)
  	body := "casino.ut1.test\n"
  	_ = tw.WriteHeader(&tar.Header{Name: "blacklists/gambling/domains", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
  	_, _ = tw.Write([]byte(body))
  	_ = tw.Close()
  	_ = gz.Close()
  	mu.Lock()
  	served["ut1-gambling"], served["ut1-dating"] = buf.Bytes(), buf.Bytes()
  	mu.Unlock()
  	if err := f.refresh(ctx, id("ut1-gambling"), systemActor, true); err != nil {
  		t.Fatal(err)
  	}
  	if n, e := state("ut1-gambling"); n != 1 || e != "" {
  		t.Fatalf("ut1 gambling member: entries=%d err=%q", n, e)
  	}
  	if err := f.refresh(ctx, id("ut1-dating"), systemActor, true); err != nil {
  		t.Fatal(err)
  	}
  	if n, e := state("ut1-dating"); n != 0 || e != "archive member blacklists/dating/domains not found" {
  		t.Fatalf("missing member: entries=%d err=%q", n, e)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/blocklist/ -count=1` and expect FAIL with `undefined: blocklist.ReadArchiveMember`.
- [ ] In `parse.go`, in the plain-domain branch, strip one leading `*.` before `normalizeDomain` (a bare `*.` stays invalid). Create `archive.go`:
  ```go
  package blocklist

  import (
  	"archive/tar"
  	"compress/gzip"
  	"errors"
  	"fmt"
  	"io"
  	"strings"
  )

  // ReadArchiveMember parses only `member` of a gzip-compressed tar archive (a UT1 blacklists.tar.gz),
  // reading at most MaxListBytes of it.
  func ReadArchiveMember(r io.Reader, member string) ([]string, ParseStats, error) {
  	gz, err := gzip.NewReader(r)
  	if err != nil {
  		return nil, ParseStats{}, fmt.Errorf("read archive: %v", err)
  	}
  	defer func() { _ = gz.Close() }()
  	tr := tar.NewReader(gz)
  	for {
  		h, err := tr.Next()
  		if errors.Is(err, io.EOF) {
  			return nil, ParseStats{}, fmt.Errorf("archive member %s not found", member)
  		}
  		if err != nil {
  			return nil, ParseStats{}, fmt.Errorf("read archive: %v", err)
  		}
  		if h.Typeflag != tar.TypeReg || strings.TrimPrefix(h.Name, "./") != member {
  			continue
  		}
  		body := &io.LimitedReader{R: tr, N: MaxListBytes + 1}
  		domains, stats, err := Parse(body)
  		if body.N == 0 {
  			return nil, ParseStats{}, errors.New("archive member exceeds 256 MiB")
  		}
  		if err != nil {
  			return nil, ParseStats{}, fmt.Errorf("read archive: %v", err)
  		}
  		return domains, stats, nil
  	}
  }
  ```
- [ ] In `fetcher.go`: add the field `mirror string` and `func (f *Fetcher) WithCatalogMirror(base string) *Fetcher { f.mirror = strings.TrimSuffix(base, "/"); return f }`; `refresh` selects `url, current_blob_sha256, managed_by_catalog, coalesce(source_key, ''), archive_member`, uses `f.mirror + "/" + sourceKey` when `managed_by_catalog && f.mirror != ""`, and calls `f.download(ctx, url, member)`; `download` passes a non-empty member's body (limited to `256 << 20` compressed bytes) to `ReadArchiveMember` and otherwise keeps `Parse`; the due-list query becomes `select id::text from filter_lists where (enabled and (not managed_by_catalog or category_key in (select key from filter_categories where enabled) or category_key in (select unnest(category_keys) from policy_groups))) or id in (select filter_list_id from policy_group_filter_lists)) and (last_attempt_at is null or last_attempt_at < now() - refresh_interval_seconds * interval '1 second') order by last_attempt_at nulls first`, so a disabled category's sources are not downloaded.
- [ ] In `config.go` add `CatalogMirror string` read from `NEXORA_CATALOG_MIRROR`, rejecting a value that does not start with `http://` or `https://` with `NEXORA_CATALOG_MIRROR must be an http(s) URL`; in `main.go` use `blocklist.NewFetcher(st, build, &http.Client{}).WithCatalogMirror(cfg.CatalogMirror)`; add the variable to the environment table of `docs/operations.md` (`NEXORA_CATALOG_MIRROR` | empty | base URL serving every catalog source at `<base>/<source key>`, for air-gapped installs and tests; it cannot add sources).
- [ ] In `config_test.go` add the `catalog mirror` case (`ftp://...`) to `TestLoadValidation` and `TestLoadCatalogMirror` (an http URL is kept; `mirror.test` fails with the exact message).
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/blocklist/ ./mgmt/internal/config/ -count=1'` and expect `ok`.
- [ ] Commit: `git add mgmt/internal/blocklist mgmt/internal/config/config.go mgmt/cmd/nexora-mgmt/main.go docs/operations.md && git commit -m "feat(mgmt): UT1 archive members, wildcard entries and the catalog mirror in the list fetcher"`.

## Task 13: Filter category API, OISD license guard, read-only catalog and engine index settings

Files: `mgmt/api/openapi.yaml` (operations and schemas), `mgmt/internal/api/gen.go` (regenerated on the laptop), `web/src/api/schema.d.ts` (regenerated on the laptop), `mgmt/internal/api/filtercategories.go` (handlers and guard), `mgmt/internal/api/filtercategories_test.go`, `mgmt/internal/api/policies.go` and `policyvalidate.go` (`category_keys`, `acknowledge_license`), `mgmt/internal/api/handlers_dns.go` (catalog-managed lists read-only, reserved names, new list fields), `mgmt/internal/api/fleet_groups.go` and `mgmt/internal/fleet/groups.go` (`filter_index_max_bytes`), `mgmt/internal/api/fleet_engines.go` and `mgmt/internal/fleet/stats.go` (`filter_index` of the newest sample), `mgmt/internal/api/server.go` (`Deps.Catalog`), `mgmt/internal/auth/permissions.go`, `web/src/auth/permissions.ts`, `mgmt/cmd/nexora-mgmt/main.go` (passes the catalog)
Interfaces (`api.Deps.Catalog` nil serves an empty catalog): operations `listFilterCategories` (`GET /filter-categories`, viewer) and `updateFilterCategory` (`PUT /filter-categories/{key}`, operator); schemas `FilterCategory{key, name, description, enabled, stale, revision, sources[]}`, `FilterCategorySource{key, name, url, format, archive_member, license, license_url, attribution, commercial_use, notice, enabled, list_id, entry_count, last_success_at, last_error, stale}`, `FilterCategoryUpdate{enabled, revision, acknowledge_license, sources[]{key, enabled}}`; `PolicyGroupInput.category_keys`, `PolicyGroupInput.acknowledge_license`, `PolicyGroup.category_keys`; `FilterList.category_key`, `FilterList.managed_by_catalog`; `EngineGroupInput.filter_index_max_bytes`, `EngineGroup.filter_index_max_bytes`; `EngineStats.filter_index{at, entries, bytes, max_bytes, build_seconds, decision_ns_blocked, decision_ns_clean, cpu}`; `api.Deps.Catalog *catalog.Catalog`; `func fleet.LatestFilterIndex(ctx context.Context, q store.PolicyQuerier, engineID uuid.UUID) (*controlv1.FilterIndexStats, time.Time, error)`; error codes `license_acknowledgement_required` (422, `details` names the sources), `unknown_source` (422), `unknown_category` (422), `catalog_managed` (422).

- [ ] Write the failing test `mgmt/internal/api/filtercategories_test.go`:
  ```go
  package api_test

  import (
  	"context"
  	"encoding/json"
  	"net/http"
  	"testing"

  	"github.com/piwi3910/nexora/mgmt/internal/api"
  	"github.com/piwi3910/nexora/mgmt/internal/catalog"
  	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
  )

  type categoryOut struct {
  	Key      string `json:"key"`
  	Enabled  bool   `json:"enabled"`
  	Revision int64  `json:"revision"`
  	Sources  []struct {
  		Key           string `json:"key"`
  		URL           string `json:"url"`
  		License       string `json:"license"`
  		Attribution   string `json:"attribution"`
  		CommercialUse bool   `json:"commercial_use"`
  		Notice        string `json:"notice"`
  		Enabled       bool   `json:"enabled"`
  		ListID        string `json:"list_id"`
  	} `json:"sources"`
  }

  func categoryClients(t *testing.T) (*client, *client, *apiEnv) {
  	t.Helper()
  	cat, err := catalog.Load()
  	if err != nil {
  		t.Fatal(err)
  	}
  	op, viewer, e := roleClientsWith(t, func(d *api.Deps) { d.Catalog = cat })
  	if _, err := catalog.Sync(context.Background(), e.st, snapshot.BuildConfig{}, cat, catalog.Raw()); err != nil {
  		t.Fatal(err)
  	}
  	return op, viewer, e
  }

  func findCategory(t *testing.T, c *client, key string) categoryOut {
  	t.Helper()
  	var all []categoryOut
  	if code := c.do(http.MethodGet, "/filter-categories", nil, &all); code != http.StatusOK {
  		t.Fatalf("list categories -> %d", code)
  	}
  	for _, x := range all {
  		if x.Key == key {
  			return x
  		}
  	}
  	t.Fatalf("category %s missing", key)
  	return categoryOut{}
  }

  func TestFilterCategoriesAPI(t *testing.T) {
  	op, viewer, e := categoryClients(t)
  	gambling := findCategory(t, viewer, "gambling")
  	if gambling.Enabled || len(gambling.Sources) != 4 || gambling.Sources[0].License == "" || gambling.Sources[0].ListID == "" {
  		t.Fatalf("viewer sees the catalog: %+v", gambling)
  	}
  	if code := viewer.do(http.MethodPut, "/filter-categories/gambling", map[string]any{"enabled": true, "revision": gambling.Revision}, nil); code != http.StatusForbidden {
  		t.Fatalf("viewer update -> %d", code)
  	}
  	var updated categoryOut
  	if code := op.do(http.MethodPut, "/filter-categories/gambling", map[string]any{"enabled": true, "revision": gambling.Revision,
  		"sources": []map[string]any{{"key": "blp-gambling", "enabled": false}}}, &updated); code != http.StatusOK || !updated.Enabled || updated.Revision != gambling.Revision+1 {
  		t.Fatalf("enable gambling -> %d %+v", code, updated)
  	}
  	for _, s := range updated.Sources {
  		if (s.Key == "blp-gambling") == s.Enabled {
  			t.Fatalf("source toggles: %+v", updated.Sources)
  		}
  	}
  	if code := op.do(http.MethodPut, "/filter-categories/gambling", map[string]any{"enabled": false, "revision": gambling.Revision}, nil); code != http.StatusConflict {
  		t.Fatalf("stale revision -> %d", code)
  	}

  	ads := findCategory(t, op, "ads-tracking")
  	var e422 apiErr
  	if code := op.do(http.MethodPut, "/filter-categories/ads-tracking", map[string]any{"enabled": true, "revision": ads.Revision}, &e422); code != http.StatusUnprocessableEntity || e422.Code != "license_acknowledgement_required" {
  		t.Fatalf("OISD without acknowledgement -> %d %+v", code, e422)
  	}
  	if findCategory(t, op, "ads-tracking").Enabled {
  		t.Fatal("a rejected update enabled the category")
  	}
  	if code := op.do(http.MethodPut, "/filter-categories/ads-tracking", map[string]any{"enabled": true, "revision": ads.Revision, "acknowledge_license": true}, nil); code != http.StatusOK {
  		t.Fatalf("OISD with acknowledgement -> %d", code)
  	}
  	var diff []byte
  	if err := e.st.Pool.QueryRow(context.Background(), `select diff from audit_log where action = 'updateFilterCategory' and target_id = 'ads-tracking' order by id desc limit 1`).Scan(&diff); err != nil {
  		t.Fatal(err)
  	}
  	var d struct {
  		After struct {
  			Acknowledged []string `json:"acknowledged_licenses"`
  		} `json:"after"`
  	}
  	if json.Unmarshal(diff, &d) != nil || len(d.After.Acknowledged) != 1 || d.After.Acknowledged[0] != "oisd-big" {
  		t.Fatalf("audit diff %s", diff)
  	}

  	// The catalog is read-only.
  	ads = findCategory(t, op, "ads-tracking")
  	if code := op.do(http.MethodPut, "/filter-categories/ads-tracking", map[string]any{"enabled": true, "revision": ads.Revision,
  		"sources": []map[string]any{{"key": "my-own-source", "enabled": true}}}, &e422); code != http.StatusUnprocessableEntity || e422.Code != "unknown_source" {
  		t.Fatalf("custom source -> %d %+v", code, e422)
  	}
  	for _, m := range []string{http.MethodPost, http.MethodDelete} {
  		if code := op.do(m, "/filter-categories", map[string]any{"key": "mine"}, nil); code != http.StatusMethodNotAllowed && code != http.StatusNotFound {
  			t.Fatalf("%s /filter-categories -> %d", m, code)
  		}
  	}
  	if code := op.do(http.MethodPost, "/filter-categories/gambling/sources", map[string]any{"key": "mine", "url": "https://x.test/l.txt"}, nil); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
  		t.Fatalf("POST sources -> %d", code)
  	}
  	managed := gambling.Sources[0].ListID
  	if code := op.do(http.MethodPut, "/filter-lists/"+managed, map[string]any{"name": "x", "kind": "block", "url": "https://evil.test/l.txt", "refresh_interval_seconds": 3600, "enabled": true, "revision": 1}, &e422); code != http.StatusUnprocessableEntity || e422.Code != "catalog_managed" {
  		t.Fatalf("edit a catalog list -> %d %+v", code, e422)
  	}
  	if code := op.do(http.MethodDelete, "/filter-lists/"+managed+"?revision=1", nil, &e422); code != http.StatusUnprocessableEntity || e422.Code != "catalog_managed" {
  		t.Fatalf("delete a catalog list -> %d %+v", code, e422)
  	}
  	if code := op.do(http.MethodPost, "/filter-lists", map[string]any{"name": "catalog:gambling:mine", "kind": "block", "url": "https://x.test/l.txt", "refresh_interval_seconds": 3600, "enabled": true}, nil); code != http.StatusBadRequest {
  		t.Fatalf("reserved list name -> %d", code)
  	}
  	if src := findCategory(t, op, "gambling").Sources; len(src) != 4 || src[0].URL != gambling.Sources[0].URL {
  		t.Fatalf("sources changed: %+v", src)
  	}

  	// Policy groups: category keys, the license guard and no catalog lists by id.
  	group := map[string]any{"name": "kids", "cidrs": []string{"10.50.0.0/16"}, "category_keys": []string{"adult"}}
  	if code := op.do(http.MethodPost, "/policy-groups", group, &e422); code != http.StatusUnprocessableEntity || e422.Code != "license_acknowledgement_required" {
  		t.Fatalf("group with OISD-backed adult without acknowledgement -> %d %+v", code, e422)
  	}
  	group["acknowledge_license"] = true
  	var created struct {
  		CategoryKeys []string `json:"category_keys"`
  	}
  	if code := op.do(http.MethodPost, "/policy-groups", group, &created); code != http.StatusCreated || len(created.CategoryKeys) != 1 {
  		t.Fatalf("group with acknowledgement -> %d %+v", code, created)
  	}
  	if code := op.do(http.MethodPost, "/policy-groups", map[string]any{"name": "k2", "cidrs": []string{"10.51.0.0/16"}, "category_keys": []string{"no-such"}}, &e422); code != http.StatusUnprocessableEntity || e422.Code != "unknown_category" {
  		t.Fatalf("unknown category -> %d %+v", code, e422)
  	}
  	if code := op.do(http.MethodPost, "/policy-groups", map[string]any{"name": "k3", "cidrs": []string{"10.52.0.0/16"}, "filter_list_ids": []string{managed}}, &e422); code != http.StatusUnprocessableEntity {
  		t.Fatalf("catalog list by id -> %d", code)
  	}
  }

  func TestEngineGroupFilterIndexMaxBytes(t *testing.T) {
  	op, _, _ := categoryClients(t)
  	var g struct {
  		ID       string `json:"id"`
  		Revision int64  `json:"revision"`
  		Max      int64  `json:"filter_index_max_bytes"`
  	}
  	if code := op.do(http.MethodPost, "/engine-groups", map[string]any{"name": "edge", "filter_index_max_bytes": 1 << 20}, nil); code != http.StatusBadRequest {
  		t.Fatalf("1 MiB cap -> %d", code)
  	}
  	if code := op.do(http.MethodPost, "/engine-groups", map[string]any{"name": "edge", "filter_index_max_bytes": 64 << 20}, &g); code != http.StatusCreated || g.Max != 64<<20 {
  		t.Fatalf("64 MiB cap -> %d %+v", code, g)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/api/ -run "TestFilterCategoriesAPI|TestEngineGroupFilterIndexMaxBytes" -count=1` and expect FAIL with `d.Catalog undefined (type *api.Deps has no field or method Catalog)`.
- [ ] Add to `mgmt/api/openapi.yaml` under `paths` (tag `filtering`, following the file's flow style):
  ```yaml
  /filter-categories:
    get:
      operationId: listFilterCategories
      tags: [filtering]
      responses:
        "200":
          description: the catalog in catalog order, with enabled state and fetch status per source
          content:
            application/json:
              schema:
                {
                  type: array,
                  items: { $ref: "#/components/schemas/FilterCategory" },
                }
  /filter-categories/{key}:
    put:
      operationId: updateFilterCategory
      tags: [filtering]
      parameters:
        - {
            name: key,
            in: path,
            required: true,
            schema: { type: string, pattern: "^[a-z][a-z0-9-]{0,31}$" },
          }
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: "#/components/schemas/FilterCategoryUpdate" }
      responses:
        "200":
          description: updated
          content:
            application/json:
              schema: { $ref: "#/components/schemas/FilterCategory" }
        "400": { $ref: "#/components/responses/Error" }
        "404": { $ref: "#/components/responses/Error" }
        "409": { $ref: "#/components/responses/Error" }
        "422": { $ref: "#/components/responses/Error" }
  ```
  and under `components.schemas`:
  ```yaml
  FilterCategorySource:
    type: object
    required:
      [
        key,
        name,
        url,
        format,
        archive_member,
        license,
        license_url,
        attribution,
        commercial_use,
        notice,
        enabled,
        list_id,
        entry_count,
        last_success_at,
        last_error,
        stale,
      ]
    properties:
      key: { type: string }
      name: { type: string }
      url: { type: string }
      format: { type: string, enum: [domains, hosts, wildcard] }
      archive_member:
        {
          type: string,
          description: "member of a .tar.gz archive; empty for plain lists",
        }
      license: { type: string }
      license_url: { type: string }
      attribution: { type: string }
      commercial_use:
        {
          type: boolean,
          description: "false: enabling needs acknowledge_license",
        }
      notice: { type: string }
      enabled: { type: boolean }
      list_id: { type: string, format: uuid }
      entry_count: { type: integer }
      last_success_at: { type: [string, "null"], format: date-time }
      last_error: { type: string }
      stale: { type: boolean }
  FilterCategory:
    type: object
    required: [key, name, description, enabled, stale, revision, sources]
    properties:
      key: { type: string }
      name: { type: string }
      description: { type: string }
      enabled: { type: boolean }
      stale:
        {
          type: boolean,
          description: "an enabled source failed its last refresh or is older than two intervals",
        }
      revision: { type: integer, format: int64 }
      sources:
        {
          type: array,
          items: { $ref: "#/components/schemas/FilterCategorySource" },
        }
  FilterCategoryUpdate:
    type: object
    additionalProperties: false
    required: [enabled, revision]
    properties:
      enabled: { type: boolean }
      revision: { type: integer, format: int64 }
      acknowledge_license: { type: boolean, default: false }
      sources:
        type: array
        maxItems: 64
        items:
          type: object
          additionalProperties: false
          required: [key, enabled]
          properties:
            key: { type: string }
            enabled: { type: boolean }
  ```
  Also add `category_keys: { type: array, maxItems: 64, items: { type: string }, default: [] }` and `acknowledge_license: { type: boolean, default: false }` to `PolicyGroupInput`, `category_keys` (required) to `PolicyGroup`; `category_key: { type: [string, "null"] }` and `managed_by_catalog: { type: boolean }` (both required) to `FilterList`; `filter_index_max_bytes: { type: integer, format: int64, minimum: 0 }` to `EngineGroupInput` and (required) `EngineGroup`; and to `EngineStats` the optional `filter_index: { type: [object, "null"], required: [at, entries, bytes, max_bytes, build_seconds, decision_ns_blocked, decision_ns_clean, cpu], properties: { at: { type: string, format: date-time }, entries: { type: integer, format: int64 }, bytes: { type: integer, format: int64 }, max_bytes: { type: integer, format: int64 }, build_seconds: { type: number }, decision_ns_blocked: { type: number }, decision_ns_clean: { type: number }, cpu: { type: string } } }`.
- [ ] Regenerate: `gen.go` with the dev pod's pinned oapi-codegen v2.8.0 (the laptop has v2.6.0) from a temporary copy of `openapi.yaml` and `oapi-codegen.yaml`, copied back with `kubectl ... cat`; `schema.d.ts` on the laptop with `cd web && pnpm run gen:api`. Also declare `"422"` on `deleteFilterList` (catalog_managed), and describe `Error.details` for `license_acknowledgement_required` (one entry per source, `line` 0, message `<source key>: <notice>`); `apiError` gains an optional `details []string` that `mapError` writes. openapi-typescript makes `category_keys` and `acknowledge_license` (they carry defaults) required in request bodies, so `web/src/pages/PoliciesPage.tsx` sends the stored group's `category_keys` and `acknowledge_license: false` and offers only lists without `managed_by_catalog` until Task 16's picker.
- [ ] Add `"listFilterCategories": RoleViewer,` and `"updateFilterCategory": RoleOperator,` to `mgmt/internal/auth/permissions.go` and `listFilterCategories: "viewer",` / `updateFilterCategory: "operator",` to `web/src/auth/permissions.ts`; add `Catalog *catalog.Catalog` to `api.Deps` and `Catalog: cat` to the `api.Deps` literal in `main.go`.
- [ ] Implement `mgmt/internal/api/filtercategories.go`:
  - `ListFilterCategories`: load `store.ListFilterCategories` and `store.ListCatalogLists` from `h.d.Store.Pool`, and emit the catalog's categories in catalog order with each source's catalog fields plus the row's `enabled`, `list_id`, `entry_count`, `last_success_at`, `last_error`, `stale`; a category is `stale` when it is enabled and any enabled source is stale.
  - `UpdateFilterCategory`: unknown key → `store.ErrNotFound`; every `sources[].key` must be a source of that category, else `coded(422, "unknown_source", "category %s has no source %s; catalog sources cannot be added", key, s)`; inside `h.mutate`: `store.LockFilterCategory`, `checkRevision(row.Revision, body.Revision)`, load the category's catalog lists; for each source compute `before := list.Enabled && (row.Enabled || selectedByAnyGroup)` and `after := toggled(list) && (body.Enabled || selectedByAnyGroup)` (a source enabled inside a category a policy group already selects comes into effect too, so it needs the acknowledgement as well); the sources with `after && !before && !source.CommercialUse` must be acknowledged: without `acknowledge_license` return `licenseRequired(keys)` = `apiError` 422 `license_acknowledgement_required`, message `sources %s are not free for commercial use; resend with acknowledge_license: true`, one `details` entry per source with its notice; then `store.SetCatalogListEnabled(ctx, tx, list.ID, toggled, acknowledgedNow)` for every changed or acknowledged source and `store.SetFilterCategoryEnabled`; return `auth.Change{Action: "updateFilterCategory", TargetType: "filter_category", TargetID: key, Before: {"enabled", "sources"}, After: {"enabled", "sources", "acknowledged_licenses": keys}}` (`keys` is `[]string{}` when none), and answer with the category rebuilt as in the list.
  - `func (h *handlers) checkGroupCategories(ctx context.Context, tx pgx.Tx, before []string, g store.PolicyGroup, acknowledged bool) error`: an unknown key → `coded(422, "unknown_category", "category_keys: unknown category %s", k)`; for keys not in `before`, the enabled catalog lists of that category whose source has `CommercialUse == false` need `acknowledged`, else `licenseRequired`; when acknowledged, set `license_acknowledged_at` on those lists and put `acknowledged_licenses` into the group change's `After`.
- [ ] Wire it: `validatePolicyGroup` copies `CategoryKeys` (deduplicated, at most 64) and `AcknowledgeLicense`; `CreatePolicyGroup` calls `checkGroupCategories(ctx, tx, nil, g, ack)` and `UpdatePolicyGroup` passes the stored group's keys as `before`, both before writing; `policyGroupOut` sets `CategoryKeys` (never nil). In `handlers_dns.go`: `filterListColumns` adds `category_key, managed_by_catalog`; `validateFilterList` rejects names starting with `catalog:` with `invalid("name must not start with catalog:")`; `UpdateFilterList` and `DeleteFilterList` read `managed_by_catalog` in their `for update` query and return `coded(422, "catalog_managed", "filter list %s is managed by the filter category catalog", id)`.
- [ ] Engine settings: `fleet.EngineGroup.FilterIndexMaxBytes int64` read by `engineGroupColumns` and written by `CreateEngineGroup`/`UpdateEngineGroup`; `engineGroupInput.filterIndexMaxBytes *int64` applied in `applyEngineGroupInput` with `invalid("filter_index_max_bytes must be 0 or at least 16777216")` for `1..16777215`; `engineGroupOut` sets it. Built after Task 5 (tests `TestLatestFilterIndexReadsNewestSample` in `mgmt/internal/fleet/stats_test.go` and `TestEngineStatsFilterIndex` in `filtercategories_test.go`): the `EngineStats.filter_index` schema and regeneration, and `fleet.LatestFilterIndex`, which runs `select at, stats from engine_stats where engine_id = $1 order by at desc limit 1`, unmarshals `controlv1.Stats` and returns its `FilterIndex` (nil when absent); `GetEngineStats` fills `EngineStats.FilterIndex` from it.
- [ ] Run `scripts/dev-exec.sh 'go vet ./mgmt/... && go test ./mgmt/internal/api/ ./mgmt/internal/fleet/ ./mgmt/internal/auth/ -count=1'` and expect `ok` (including `TestPermissionsCoverEveryOperation`), then `scripts/dev-exec.sh 'cd web && pnpm run typecheck && pnpm run lint'` and expect success (`check-permissions.mjs` agrees).
- [ ] Commit: `git add mgmt/api/openapi.yaml mgmt/internal/api mgmt/internal/fleet mgmt/internal/auth/permissions.go mgmt/cmd/nexora-mgmt/main.go web/src/api/schema.d.ts web/src/auth/permissions.ts && git commit -m "feat(mgmt): filter category API with commercial-use license guard and read-only catalog"`.

## Task 14: Query log category attribution in both backends and category staleness metric

Files: `mgmt/internal/querylog/backend.go` (`Query.Category`, `Record.ListID`, `Record.Category`), `mgmt/internal/querylog/builtin.go` (attribute mapping and filter), `mgmt/internal/querylog/opensearch.go` (category term, both filter field generations), `mgmt/internal/querylog/builtin_test.go`, `mgmt/internal/querylog/opensearch_test.go`, `mgmt/api/openapi.yaml` (`category` parameter, `list_id`/`category` fields), `mgmt/internal/api/handlers_admin.go` (`SearchQueryLog` mapping), `mgmt/internal/api/gen.go` and `web/src/api/schema.d.ts` (regenerated), `mgmt/internal/stats/collector.go` and `collector_test.go` (`nexora_mgmt_filter_category_stale{category}`)
As built so far: the `openapi.yaml` part (`category` parameter, required `list_id`/`category` on `QueryLogRecord`), the regenerated `gen.go`/`schema.d.ts` and the collector's `nexora_mgmt_filter_category_stale` with `TestCollectorExportsCategoryStaleness` landed together with Task 13's engine stats fields; the querylog backends and `SearchQueryLog` mapping remain.

Interfaces: `querylog.Query.Category string`; `querylog.Record.ListID, Category string`; attribute keys `nexora.filter.list_id`, `nexora.filter.category`, OpenSearch fields `attributes.nexora.filter.category.keyword`, `attributes.nexora.filter.result.keyword` (v2 index) and `attributes.nexora.filter.keyword` (older indices); API query parameter `category`, record fields `list_id` and `category` (both required, empty when not blocked).

As built (querylog part): `mgmt/internal/querylog` as below. `deploy/kw/otelcol.yaml` already gets Task 20's `transform/querylog` processor, `logs_index: nexora-querylog-v2` and `processors: [batch, transform/querylog]` (validated with `otelcol-contrib validate`). The Helm chart's default collector config only exports to `debug`, so it needs no rename.

- [ ] Add to `mgmt/internal/querylog/builtin_test.go`:
  ```go
  func TestBuiltinCategoryAttribution(t *testing.T) {
  	b := querylog.NewBuiltin(10)
  	req := request("casino.example.", "clean.example.")
  	attrs := &req.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Attributes
  	*attrs = append(*attrs, str("nexora.filter.list_id", "0b6c3e2a-2d57-4a43-9a52-8f0e8bb3c1d1"), str("nexora.filter.category", "gambling"))
  	b.Ingest("e1", req)
  	page, err := b.Search(context.Background(), querylog.Query{Category: "gambling", Limit: 10})
  	if err != nil || len(page.Records) != 1 || page.Records[0].Name != "casino.example." ||
  		page.Records[0].ListID != "0b6c3e2a-2d57-4a43-9a52-8f0e8bb3c1d1" || page.Records[0].Category != "gambling" {
  		t.Fatalf("category filter: %+v %v", page, err)
  	}
  	all, _ := b.Search(context.Background(), querylog.Query{Limit: 10})
  	if len(all.Records) != 2 {
  		t.Fatalf("positive path: %d records", len(all.Records))
  	}
  	none, _ := b.Search(context.Background(), querylog.Query{Category: "adult", Limit: 10})
  	if len(none.Records) != 0 {
  		t.Fatalf("other category matched: %+v", none)
  	}
  }
  ```
  and to `mgmt/internal/querylog/opensearch_test.go`:
  ```go
  func TestOpenSearchCategoryAndFilterGenerations(t *testing.T) {
  	var body string
  	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  		b, _ := io.ReadAll(r.Body)
  		body = string(b)
  		w.Header().Set("Content-Type", "application/json")
  		fmt.Fprint(w, `{"hits":{"hits":[{"_source":{"@timestamp":"2026-09-14T10:00:00Z","attributes":{"client.address":"10.0.0.9","dns.question.name":"casino.example.","dns.question.type":"A","dns.response.code":"NOERROR","nexora.cache":"none","nexora.filter.result":"blocked","nexora.filter.list_id":"l1","nexora.filter.category":"gambling","nexora.transport":"udp","nexora.engine.id":"e1","nexora.duration_us":12}},"sort":[1]}]}}`)
  	}))
  	defer srv.Close()
  	os, err := querylog.NewOpenSearch(config.OpenSearchConfig{URL: srv.URL, Index: "nexora-querylog-*"})
  	if err != nil {
  		t.Fatal(err)
  	}
  	page, err := os.Search(context.Background(), querylog.Query{Category: "gambling", Filter: "blocked", Limit: 5})
  	if err != nil || len(page.Records) != 1 || page.Records[0].Filter != "blocked" || page.Records[0].Category != "gambling" || page.Records[0].ListID != "l1" {
  		t.Fatalf("v2 document: %+v %v", page, err)
  	}
  	for _, want := range []string{`"attributes.nexora.filter.category.keyword":"gambling"`, `"attributes.nexora.filter.result.keyword":"blocked"`, `"attributes.nexora.filter.keyword":"blocked"`} {
  		if !strings.Contains(body, want) {
  			t.Errorf("query lacks %s: %s", want, body)
  		}
  	}
  }
  ```
  and to `mgmt/internal/stats/collector_test.go` a `TestCollectorExportsCategoryStaleness` that uses `storetest.New`, `catalog.Sync` with the embedded catalog, enables `gambling` (`update filter_categories set enabled = true where key = 'gambling'`), sets `last_error = 'boom'` on `hagezi-gambling`, gathers `stats.NewCollector(st)` through a `prometheus.NewRegistry()` and asserts a `nexora_mgmt_filter_category_stale` sample with label `category="gambling"` and value 1, and a sample `category="adult"` with value 0.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/querylog/ ./mgmt/internal/stats/ -count=1` and expect FAIL with `unknown field Category in struct literal of type querylog.Query`.
- [ ] Implement: `Query.Category`; `Record.ListID`, `Record.Category`; `recordFromAttributes` maps `nexora.filter.list_id` and `nexora.filter.category`, and also `nexora.filter.result` into `Filter`; `matches` requires `r.Category == q.Category` when set. OpenSearch: `osSource.Attributes` gains `` FilterResult string `json:"nexora.filter.result"` ``, `` ListID string `json:"nexora.filter.list_id"` ``, `` Category string `json:"nexora.filter.category"` ``; `Record.Filter` is `a.Filter` or, when empty, `a.FilterResult`; the `filter` parameter becomes `{"bool": {"should": [{"term": {"attributes.nexora.filter.keyword": v}}, {"term": {"attributes.nexora.filter.result.keyword": v}}], "minimum_should_match": 1}}` and `category` a `term` on `attributes.nexora.filter.category.keyword`. `SearchQueryLog` passes `Category: deref(p.Category)` and maps `ListId`/`Category`; in `openapi.yaml` add `- { name: category, in: query, schema: { type: string } }` to `/query-log` and `list_id: { type: string }`, `category: { type: string }` (required) to `QueryLogRecord`; regenerate `gen.go` and `schema.d.ts` on the laptop as in Task 13. The collector adds `descCategoryStale = prometheus.NewDesc("nexora_mgmt_filter_category_stale", "Whether an enabled filter category has an enabled source that failed its last refresh or is older than two intervals", []string{"category"}, nil)` emitted per `filter_categories` row from `select c.key, c.enabled and coalesce(bool_or(f.enabled and (f.last_error <> '' or f.last_success_at is null or f.last_success_at < now() - 2 * f.refresh_interval_seconds * interval '1 second')), false) from filter_categories c left join filter_lists f on f.category_key = c.key group by c.key, c.enabled`.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/querylog/ ./mgmt/internal/stats/ ./mgmt/internal/api/ -count=1'` and expect `ok`.
- [ ] Commit: `git add mgmt/internal/querylog mgmt/internal/stats mgmt/api/openapi.yaml mgmt/internal/api web/src/api/schema.d.ts && git commit -m "feat(mgmt): query log list and category attribution in builtin and OpenSearch backends"`.

## Task 15: GUI category catalog screen with toggles, licenses and the license notice

Files: `web/src/api/filterCategories.ts` (query and mutation hooks), `web/src/pages/FilterCategoriesPage.tsx` (the screen), `web/src/app/router.tsx` (`filtering/categories` route), `web/src/components/layout/AppShell.tsx` (nav entry), `web/e2e/screens/22-filter-categories.spec.ts` (first test), `e2e/harness/catalog.go` (catalog mirror and category helpers), `e2e/gui_test.go` (mirror, fixture sources, a blocked query with a category)
Interfaces: `useFilterCategories()`, `useUpdateFilterCategory()` (`mutate({ key, body })`), `needsAcknowledgement(category, body): Schemas["FilterCategorySource"][]`, component `FilterCategoriesPage`; test ids `nav-filter-categories`, `category-<key>`, `category-stale-<key>`, `source-row-<key>`, `source-toggle-<key>`, `source-noncommercial-<key>`; switch accessible name `Enable <category name>`; dialog title `License notice` with buttons `Cancel` and `Acknowledge and enable`. Go harness: `func CatalogMirrorEnv(f *HTTPFixture) string`, `type CategoryView`, `type CategorySource`, `type ListState`, `func (a *API) FilterCategory(key string) CategoryView`, `func (a *API) SetFilterCategory(key string, enabled bool, sources map[string]bool, acknowledge bool) (int, string)`, `func (a *API) RefreshSource(category, source string) ListState`, `func (a *API) RefreshCategory(category string)`, `func UT1Archive(t *testing.T, members map[string]string) []byte`; Playwright env `NEXORA_E2E_CATEGORY_QUERY_NAME`.

As built (deviations from the code below, which is kept for reference): `SetFilterCategory` calls `a.Do` and returns `(status, "")` on success, because `API.ErrorCode` fails the test on a 2xx; `FilterCategoryUpdate` bodies always carry `acknowledge_license` (the generated type makes the defaulted field required); `needsAcknowledgement` and `licenseNoticesFromError(err, categories)` live in `web/src/api/filterCategories.ts` and return `LicenseNotice` (`{ key, name, notice }`); the notice dialog is the shared `LicenseNoticeDialog` in `web/src/components/categories.tsx`, and a 422 `license_acknowledgement_required` from the server opens it too (notices parsed from `details`); the Filtering nav item gets `end: true` so it is not active on `/filtering/categories`. `e2e/gui_test.go` enables `malware` and refreshes `hagezi-tif` from the mirror but does not yet assert the blocked DNS answer or set `NEXORA_E2E_CATEGORY_QUERY_NAME`: that needs Task 11 snapshots and moves to the query-log part of Task 16.

- [ ] Write the failing Playwright test `web/e2e/screens/22-filter-categories.spec.ts`:
  ```ts
  import { test, expect, env, login } from "../fixtures";

  test("operator enables categories, toggles a source and acknowledges the OISD notice", async ({
    page,
  }) => {
    // TestGUICoverage counts the browser requests below: listFilterCategories, updateFilterCategory.
    await login(
      page,
      env("NEXORA_E2E_OPERATOR_USER"),
      env("NEXORA_E2E_OPERATOR_PASSWORD"),
    );
    await page.getByTestId("nav-filter-categories").click();
    await expect(
      page.getByRole("heading", { name: "Filter categories" }),
    ).toBeVisible();

    const gambling = () => page.getByTestId("category-gambling");
    const gamblingSwitch = () =>
      gambling().getByRole("switch", { name: "Enable Gambling", exact: true });
    await expect(gambling()).toContainText("GPL-3.0");
    await expect(gambling()).toContainText("HaGeZi DNS Blocklists");
    await gamblingSwitch().click();
    await expect(gamblingSwitch()).toBeChecked();
    await page.reload();
    await expect(gamblingSwitch()).toBeChecked();

    await page.getByTestId("source-toggle-blp-gambling").click();
    await expect(
      page.getByTestId("source-toggle-blp-gambling"),
    ).not.toBeChecked();
    await page.reload();
    await expect(
      page.getByTestId("source-toggle-blp-gambling"),
    ).not.toBeChecked();
    await expect(
      page.getByTestId("source-toggle-hagezi-gambling"),
    ).toBeChecked();

    await gamblingSwitch().click();
    await expect(gamblingSwitch()).not.toBeChecked();
    await page.reload();
    await expect(gamblingSwitch()).not.toBeChecked();

    const ads = () => page.getByTestId("category-ads-tracking");
    const adsSwitch = () =>
      ads().getByRole("switch", {
        name: "Enable Ads and tracking",
        exact: true,
      });
    await expect(
      ads().getByTestId("source-noncommercial-oisd-big"),
    ).toBeVisible();
    await adsSwitch().click();
    const notice = page.getByRole("dialog", { name: "License notice" });
    await expect(notice).toContainText(
      "OISD lists are not free for commercial use",
    );
    await notice.getByRole("button", { name: "Cancel" }).click();
    await expect(adsSwitch()).not.toBeChecked();
    await adsSwitch().click();
    await page
      .getByRole("dialog", { name: "License notice" })
      .getByRole("button", { name: "Acknowledge and enable" })
      .click();
    await expect(adsSwitch()).toBeChecked();
    await page.reload();
    await expect(adsSwitch()).toBeChecked();
  });

  test("viewer sees the catalog read-only", async ({ page }) => {
    await login(
      page,
      env("NEXORA_E2E_VIEWER_USER"),
      env("NEXORA_E2E_VIEWER_PASSWORD"),
    );
    await page.getByTestId("nav-filter-categories").click();
    await expect(page.getByTestId("category-malware")).toContainText(
      "abuse.ch",
    );
    await expect(
      page.getByTestId("category-malware").getByRole("switch", {
        name: "Enable Malware and threat intelligence",
        exact: true,
      }),
    ).toBeDisabled();
  });
  ```
- [ ] Create `e2e/harness/catalog.go`:
  ```go
  package harness

  import (
  	"archive/tar"
  	"bytes"
  	"compress/gzip"
  	"net/http"
  	"testing"
  )

  // CatalogMirrorEnv is the management environment entry that fetches every catalog source from
  // the fixture at <base>/lists/<source key> (never the internet).
  func CatalogMirrorEnv(f *HTTPFixture) string { return "NEXORA_CATALOG_MIRROR=" + f.Base + "/lists" }

  type CategorySource struct {
  	Key           string `json:"key"`
  	URL           string `json:"url"`
  	License       string `json:"license"`
  	Attribution   string `json:"attribution"`
  	CommercialUse bool   `json:"commercial_use"`
  	Notice        string `json:"notice"`
  	Enabled       bool   `json:"enabled"`
  	ListID        string `json:"list_id"`
  	EntryCount    int    `json:"entry_count"`
  	LastError     string `json:"last_error"`
  	Stale         bool   `json:"stale"`
  }

  type CategoryView struct {
  	Key      string           `json:"key"`
  	Enabled  bool             `json:"enabled"`
  	Stale    bool             `json:"stale"`
  	Revision int64            `json:"revision"`
  	Sources  []CategorySource `json:"sources"`
  }

  // ListState is the refresh answer of a filter list.
  type ListState struct {
  	EntryCount int    `json:"entry_count"`
  	LastError  string `json:"last_error"`
  	Stale      bool   `json:"stale"`
  }

  func (a *API) FilterCategory(key string) CategoryView {
  	a.T.Helper()
  	var all []CategoryView
  	a.Must(http.MethodGet, "/filter-categories", nil, &all, http.StatusOK)
  	for _, c := range all {
  		if c.Key == key {
  			return c
  		}
  	}
  	a.T.Fatalf("filter category %s missing", key)
  	return CategoryView{}
  }

  // SetFilterCategory enables or disables a category (and sources) at its current revision and
  // returns the status and the error code of a refusal.
  func (a *API) SetFilterCategory(key string, enabled bool, sources map[string]bool, acknowledge bool) (int, string) {
  	a.T.Helper()
  	body := map[string]any{"enabled": enabled, "revision": a.FilterCategory(key).Revision, "acknowledge_license": acknowledge}
  	if len(sources) > 0 {
  		var toggles []map[string]any
  		for k, v := range sources {
  			toggles = append(toggles, map[string]any{"key": k, "enabled": v})
  		}
  		body["sources"] = toggles
  	}
  	return a.ErrorCode(http.MethodPut, "/filter-categories/"+key, body)
  }

  // RefreshSource fetches one catalog source now and returns the list state.
  func (a *API) RefreshSource(category, source string) ListState {
  	a.T.Helper()
  	for _, s := range a.FilterCategory(category).Sources {
  		if s.Key == source {
  			var st ListState
  			a.Must(http.MethodPost, "/filter-lists/"+s.ListID+"/refresh", nil, &st, http.StatusOK)
  			return st
  		}
  	}
  	a.T.Fatalf("source %s missing in category %s", source, category)
  	return ListState{}
  }

  // RefreshCategory fetches every source of the category whose own enabled flag is set.
  func (a *API) RefreshCategory(category string) {
  	a.T.Helper()
  	for _, s := range a.FilterCategory(category).Sources {
  		if s.Enabled {
  			a.Must(http.MethodPost, "/filter-lists/"+s.ListID+"/refresh", nil, nil, http.StatusOK)
  		}
  	}
  }

  // UT1Archive builds a blacklists.tar.gz with the given members.
  func UT1Archive(t *testing.T, members map[string]string) []byte {
  	t.Helper()
  	var buf bytes.Buffer
  	gz := gzip.NewWriter(&buf)
  	tw := tar.NewWriter(gz)
  	for name, body := range members {
  		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
  			t.Fatal(err)
  		}
  		if _, err := tw.Write([]byte(body)); err != nil {
  			t.Fatal(err)
  		}
  	}
  	if err := tw.Close(); err != nil {
  		t.Fatal(err)
  	}
  	if err := gz.Close(); err != nil {
  		t.Fatal(err)
  	}
  	return buf.Bytes()
  }
  ```
- [ ] In `e2e/gui_test.go` `TestGUICoverage`: start `web := env.StartHTTPFixture()` before `StartMgmt` and add `harness.CatalogMirrorEnv(web)` to `MgmtOptions.ExtraEnv`; set fixture sources `web.SetList(t, "hagezi-gambling", "casino.gui.test\n")`, `web.SetList(t, "blp-gambling", "bets.gui.test\n")`, `web.SetList(t, "hagezi-pro", "ads.gui.test\n")`, `web.SetList(t, "oisd-big", "*.oisd.gui.test\n")`, `web.SetList(t, "hagezi-tif", "malware.gui.test\n")`; after both engines applied and before `time.Sleep(12 * time.Second)`, add:
  ```go
  if code, reason := admin.SetFilterCategory("malware", true, nil, true); code != http.StatusOK {
  	t.Fatalf("enable malware -> %d %s", code, reason)
  }
  if st := admin.RefreshSource("malware", "hagezi-tif"); st.EntryCount != 1 || st.LastError != "" {
  	t.Fatalf("hagezi-tif from the fixture mirror: %+v", st)
  }
  v = admin.LatestVersion()
  admin.WaitEngine("gui-engine", 15*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion >= v })
  blocked := "x.malware.gui.test."
  if ip := aRecord(t, harness.MustQuery(t, eng.DNS, blocked, dns.TypeA, harness.QueryOpts{})); !ip.Equal(net.IPv4zero) {
  	t.Fatalf("%s = %v, want 0.0.0.0 from the malware category", blocked, ip)
  }
  vars["NEXORA_E2E_CATEGORY_QUERY_NAME"] = strings.TrimSuffix(blocked, ".")
  ```
  (import `net` and `net/http` if missing; `aRecord` is in `blocklist_test.go`).
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=bin go test -count=1 -timeout 30m -run TestGUICoverage ./e2e/'` and expect FAIL with `22-filter-categories.spec.ts` failing on `getByTestId('nav-filter-categories')` (and `listFilterCategories`, `updateFilterCategory` listed as uncovered).
- [ ] Create `web/src/api/filterCategories.ts`:
  ```ts
  import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
  import { api, unwrap, type Schemas } from "@/api/client";

  export function useFilterCategories() {
    return useQuery({
      queryKey: ["filter-categories"],
      queryFn: async () => unwrap(await api.GET("/filter-categories")),
    });
  }

  export function useUpdateFilterCategory() {
    const qc = useQueryClient();
    return useMutation({
      mutationFn: async ({
        key,
        body,
      }: {
        key: string;
        body: Schemas["FilterCategoryUpdate"];
      }) =>
        unwrap(
          await api.PUT("/filter-categories/{key}", {
            params: { path: { key } },
            body,
          }),
        ),
      onSuccess: async () => {
        await qc.invalidateQueries({ queryKey: ["filter-categories"] });
        await qc.invalidateQueries({ queryKey: ["filter-lists"] });
      },
    });
  }
  ```
- [ ] Create `web/src/pages/FilterCategoriesPage.tsx`:
  ```tsx
  import { useState } from "react";
  import { type Schemas } from "@/api/client";
  import {
    useFilterCategories,
    useUpdateFilterCategory,
  } from "@/api/filterCategories";
  import { useCan } from "@/auth/AuthProvider";
  import { ErrorAlert, formatAgo } from "@/components/common";
  import { PageHeader } from "@/components/layout/AppShell";
  import { Badge } from "@/components/ui/badge";
  import { Button } from "@/components/ui/button";
  import { Card } from "@/components/ui/card";
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
  } from "@/components/ui/dialog";
  import { Switch } from "@/components/ui/switch";
  import {
    Table,
    TableBody,
    TableCell,
    TableHead,
    TableHeader,
    TableRow,
  } from "@/components/ui/table";

  type Category = Schemas["FilterCategory"];
  type Source = Schemas["FilterCategorySource"];
  type Update = Schemas["FilterCategoryUpdate"];

  /** The sources that `body` would newly enable and whose license is not free for commercial use. */
  export function needsAcknowledgement(
    category: Category,
    body: Update,
  ): Source[] {
    const toggled = new Map(
      (body.sources ?? []).map((s) => [s.key, s.enabled]),
    );
    return category.sources.filter((s) => {
      const before = category.enabled && s.enabled;
      const after = body.enabled && (toggled.get(s.key) ?? s.enabled);
      return after && !before && !s.commercial_use;
    });
  }

  export function FilterCategoriesPage() {
    const canUpdate = useCan("updateFilterCategory");
    const categories = useFilterCategories();
    const update = useUpdateFilterCategory();
    const [pending, setPending] = useState<{
      category: Category;
      body: Update;
      notices: Source[];
    } | null>(null);

    function apply(category: Category, body: Update) {
      const notices = needsAcknowledgement(category, body);
      if (notices.length > 0) {
        setPending({ category, body, notices });
        return;
      }
      update.mutate({ key: category.key, body });
    }

    return (
      <>
        <PageHeader
          title="Filter categories"
          description="Curated block lists by category. Sources, licenses and attribution ship with Nexora and cannot be edited."
        />
        <ErrorAlert
          error={categories.error}
          prefix="Could not load categories"
        />
        <ErrorAlert
          error={update.error}
          prefix="Could not update the category"
        />
        <div className="grid gap-4">
          {(categories.data ?? []).map((c) => (
            <Card
              key={c.key}
              data-testid={`category-${c.key}`}
              className="gap-3 p-4"
            >
              <div className="flex items-start justify-between gap-4">
                <div className="min-w-0">
                  <h2 className="font-medium">{c.name}</h2>
                  <p className="text-muted-foreground text-sm">
                    {c.description}
                  </p>
                  {c.stale && (
                    <Badge
                      variant="destructive"
                      data-testid={`category-stale-${c.key}`}
                    >
                      stale
                    </Badge>
                  )}
                </div>
                <Switch
                  aria-label={`Enable ${c.name}`}
                  checked={c.enabled}
                  disabled={!canUpdate || update.isPending}
                  onCheckedChange={(v) =>
                    apply(c, { enabled: v, revision: c.revision })
                  }
                />
              </div>
              <Table>
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    <TableHead>Source</TableHead>
                    <TableHead>License</TableHead>
                    <TableHead>Attribution</TableHead>
                    <TableHead className="text-right">Entries</TableHead>
                    <TableHead>Last fetched</TableHead>
                    <TableHead className="w-20">Enabled</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {c.sources.map((s) => (
                    <TableRow key={s.key} data-testid={`source-row-${s.key}`}>
                      <TableCell className="align-top">
                        {s.name}
                        <div className="text-muted-foreground font-mono text-xs break-all">
                          {s.archive_member
                            ? `${s.url} (${s.archive_member})`
                            : s.url}
                        </div>
                      </TableCell>
                      <TableCell className="align-top">
                        <a
                          href={s.license_url}
                          target="_blank"
                          rel="noreferrer"
                          className="underline"
                        >
                          {s.license}
                        </a>
                        {!s.commercial_use && (
                          <div>
                            <Badge
                              variant="outline"
                              data-testid={`source-noncommercial-${s.key}`}
                              title={s.notice}
                            >
                              Not free for commercial use
                            </Badge>
                          </div>
                        )}
                      </TableCell>
                      <TableCell className="align-top text-sm">
                        {s.attribution}
                      </TableCell>
                      <TableCell className="text-right align-top tabular-nums">
                        {s.entry_count}
                      </TableCell>
                      <TableCell className="align-top">
                        {s.last_error ? (
                          <span
                            className="text-destructive"
                            title={s.last_error}
                          >
                            failed
                          </span>
                        ) : (
                          formatAgo(s.last_success_at)
                        )}
                      </TableCell>
                      <TableCell className="align-top">
                        <Switch
                          data-testid={`source-toggle-${s.key}`}
                          aria-label={`Enable source ${s.name}`}
                          checked={s.enabled}
                          disabled={!canUpdate || update.isPending}
                          onCheckedChange={(v) =>
                            apply(c, {
                              enabled: c.enabled,
                              revision: c.revision,
                              sources: [{ key: s.key, enabled: v }],
                            })
                          }
                        />
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </Card>
          ))}
        </div>
        <Dialog
          open={pending !== null}
          onOpenChange={(open) => !open && setPending(null)}
        >
          <DialogContent>
            <DialogHeader>
              <DialogTitle>License notice</DialogTitle>
              <DialogDescription>
                Enabling this selection turns on sources that are not free for
                commercial use.
              </DialogDescription>
            </DialogHeader>
            <ul className="grid gap-2 text-sm">
              {pending?.notices.map((s) => (
                <li key={s.key}>
                  <strong>{s.name}</strong>: {s.notice}
                </li>
              ))}
            </ul>
            <DialogFooter>
              <Button variant="outline" onClick={() => setPending(null)}>
                Cancel
              </Button>
              <Button
                onClick={() => {
                  if (pending) {
                    update.mutate({
                      key: pending.category.key,
                      body: { ...pending.body, acknowledge_license: true },
                    });
                  }
                  setPending(null);
                }}
              >
                Acknowledge and enable
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </>
    );
  }
  ```
- [ ] In `web/src/app/router.tsx` import `FilterCategoriesPage` and add `{ path: "filtering/categories", element: <FilterCategoriesPage /> },` after the `filtering` route; in `AppShell.tsx` import `Tags` from `lucide-react` and add `{ route: "filter-categories", path: "/filtering/categories", label: "Categories", icon: Tags, op: "listFilterCategories" },` directly after the Filtering item.
- [ ] Run `scripts/dev-exec.sh 'make web-test && make e2e-build && NEXORA_E2E_BIN_DIR=bin go test -count=1 -timeout 40m -run TestGUICoverage ./e2e/'` and expect `ok` with both tests of `22-filter-categories.spec.ts` passing and no uncovered operation.
- [ ] Commit: `git add web/src/api/filterCategories.ts web/src/pages/FilterCategoriesPage.tsx web/src/app/router.tsx web/src/components/layout/AppShell.tsx web/e2e/screens/22-filter-categories.spec.ts e2e/harness/catalog.go e2e/gui_test.go && git commit -m "feat(web): filter category screen with source licenses and the OISD notice"`.

## Task 16: GUI category picker for policy groups, query log category, engine filter index memory

Files: `web/src/pages/PoliciesPage.tsx` (categories fieldset, license notice, categories column), `web/src/pages/QueryLogPage.tsx` (category filter and column), `web/src/pages/EngineDetailPage.tsx` (filter index facts), `web/e2e/screens/22-filter-categories.spec.ts` (second operator test)
Interfaces: consumes `useFilterCategories` and `needsAcknowledgement`'s rule (Task 15), `Schemas["PolicyGroupInput"].category_keys`/`acknowledge_license`, `QueryLogRecord.category`/`list_id`, `EngineStats.filter_index` (Task 13/14); test ids `querylog-category`, `engine-filter-index`; fieldset legend `Categories`.

As built so far: the policy-group part is its own test `categories in policy groups` (the first half of the test below, through `await expect(row).toContainText("Adult content")`), with `groupLicenseNotices` in `PoliciesPage.tsx` and the shared `LicenseNoticeDialog`; a server 422 `license_acknowledgement_required` also opens the notice. Still to do when Task 11/14 fields land: the query-log category filter and column, the engine filter index facts, the blocked malware query and `NEXORA_E2E_CATEGORY_QUERY_NAME` in `e2e/gui_test.go`, and a second test `category in the query log and filter index in engine detail` with the remaining steps.

As built (query log and engine detail): the second test lives in its own spec `web/e2e/screens/23-querylog-category.spec.ts` (from `await page.getByTestId("nav-query-log").click()` on, after an operator login) so 22 stays untouched. `QueryLogPage.tsx` keeps its `form`/`set` state names (the plan's `draft` never existed); the filter index facts are a `FilterIndexFacts` component in `EngineDetailPage.tsx`. `e2e/gui_test.go` sets `NEXORA_E2E_CATEGORY_QUERY_NAME` for spec 23: after the malware refresh it waits for both engines (`waitLatestApplied`) and eventually expects `0.0.0.0` for a unique subdomain `cat-<hex>.malware.gui.test.` (unique so the query-log name search finds exactly this run's record).

- [ ] Append to `web/e2e/screens/22-filter-categories.spec.ts`:
  ```ts
  test("categories in policy groups, the query log and engine detail", async ({
    page,
  }) => {
    await login(
      page,
      env("NEXORA_E2E_OPERATOR_USER"),
      env("NEXORA_E2E_OPERATOR_PASSWORD"),
    );
    await page.getByTestId("nav-policies").click();
    const name = `cat-${Date.now()}`;
    await page.getByRole("button", { name: "New group" }).click();
    const dialog = page.getByRole("dialog", { name: "New policy group" });
    await dialog.getByLabel("Name").fill(name);
    await dialog.getByLabel("Client CIDRs").fill("192.168.252.0/24");
    await dialog
      .getByRole("group", { name: "Categories" })
      .getByLabel("Gambling", { exact: true })
      .check();
    await dialog.getByRole("button", { name: "Save" }).click();
    const row = page.getByRole("row", { name: new RegExp(name) });
    await expect(row).toContainText("Gambling");
    await row.getByRole("button", { name: `Edit ${name}` }).click();
    const edit = page.getByRole("dialog", { name: "Edit policy group" });
    await edit
      .getByRole("group", { name: "Categories" })
      .getByLabel("Adult content", { exact: true })
      .check();
    await edit.getByRole("button", { name: "Save" }).click();
    const notice = page.getByRole("dialog", { name: "License notice" });
    await expect(notice).toContainText(
      "OISD lists are not free for commercial use",
    );
    await notice.getByRole("button", { name: "Acknowledge and save" }).click();
    await expect(edit).toBeHidden();
    await expect(row).toContainText("Adult content");

    await page.getByTestId("nav-query-log").click();
    const blocked = env("NEXORA_E2E_CATEGORY_QUERY_NAME");
    await page.getByTestId("querylog-name").fill(blocked);
    await page.getByTestId("querylog-category").click();
    await page.getByRole("option", { name: "malware", exact: true }).click();
    await page.getByTestId("querylog-search").click();
    const record = page
      .getByTestId("querylog-row")
      .filter({ hasText: blocked })
      .first();
    await expect(record).toContainText("malware");
    await expect(record).toContainText("blocked");

    await page.getByTestId("nav-engines").click();
    await page.getByTestId("engine-open-gui-engine").click();
    const index = page.getByTestId("engine-filter-index");
    await expect(index).toContainText("Filter index memory");
    await expect(index).toContainText(/\d+(\.\d+)? (KiB|MiB|GiB)/);
  });
  ```
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=bin go test -count=1 -timeout 40m -run TestGUICoverage ./e2e/'` and expect FAIL with the new test failing on `getByRole('group', { name: 'Categories' })`.
- [ ] `PoliciesPage.tsx`: `GroupForm` gains `categoryKeys: string[]` (from `g?.category_keys ?? []`); `PoliciesPage` loads `useFilterCategories()`, passes `categories.data` to `PolicyGroupDialog`, adds a `Categories` column after `Filter lists` rendering one `<Badge variant="secondary">` per key with the category name (key when unknown), and raises `cols` by one. In the dialog, below the `Filter lists` fieldset add `<fieldset className="grid gap-2"><legend className="mb-1.5 text-sm font-medium">Categories</legend>` with one `<label className="flex items-center gap-2 text-sm"><input type="checkbox" className="accent-primary h-4 w-4" .../>{c.name}</label>` per category toggling `categoryKeys`. `submit` computes `const added = form.categoryKeys.filter((k) => !(group?.category_keys ?? []).includes(k))` and `const notices = (categories ?? []).filter((c) => added.includes(c.key)).flatMap((c) => c.sources.filter((s) => s.enabled && !s.commercial_use))`; when `notices` is non-empty and the operator has not acknowledged yet, it opens a nested `Dialog` titled `License notice` listing `s.name: s.notice` with `Cancel` and `Acknowledge and save` (which sets `acknowledged` and submits again); the body sends `category_keys: form.categoryKeys` and `acknowledge_license: acknowledged`.
- [ ] `QueryLogPage.tsx`: `Filters` gains `category` (initial `any`); the request passes `category: param(applied.category)`; add `<FilterSelect label="Category" id="querylog-category" value={draft.category} options={(categories.data ?? []).map((c) => c.key)} onChange={(v) => setDraft({ ...draft, category: v })} />` next to the Filter select (`categories = useFilterCategories()`); the table gets a `Category` header after `Filter` and `RecordRow` a cell `{r.category || <span className="text-muted-foreground">—</span>}` with `title={r.list_id}`; every `colSpan={9}` becomes `colSpan={10}`.
- [ ] `EngineDetailPage.tsx`: in `EngineStatsCard`, below the chart, render when `stats.data?.filter_index` is set:
  ```tsx
  <dl
    data-testid="engine-filter-index"
    className="mt-4 grid grid-cols-2 gap-4 sm:grid-cols-4"
  >
    <Fact label="Filter index memory">
      {formatBytes(fi.bytes)} of {formatBytes(fi.max_bytes)}
    </Fact>
    <Fact label="Filter index names">{fi.entries.toLocaleString()}</Fact>
    <Fact label="Decision time">
      {fi.decision_ns_blocked.toFixed(0)} ns blocked,{" "}
      {fi.decision_ns_clean.toFixed(0)} ns clean ({fi.cpu})
    </Fact>
    <Fact label="Last index build">{fi.build_seconds.toFixed(2)} s</Fact>
  </dl>
  ```
  with `const fi = stats.data.filter_index;` and a module-level ``function formatBytes(n: number): string { const units = ["B", "KiB", "MiB", "GiB"]; let v = n; let i = 0; while (v >= 1024 && i < units.length - 1) { v /= 1024; i += 1; } return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`; }``.
- [ ] Run `scripts/dev-exec.sh 'make web-test && make e2e-build && NEXORA_E2E_BIN_DIR=bin go test -count=1 -timeout 40m -run TestGUICoverage ./e2e/'` and expect `ok` (all three tests of `22-filter-categories.spec.ts`, and `12-policies.spec.ts` and `10-query-log.spec.ts` unchanged and green).
- [ ] Commit: `git add web/src/pages/PoliciesPage.tsx web/src/pages/QueryLogPage.tsx web/src/pages/EngineDetailPage.tsx web/e2e/screens/22-filter-categories.spec.ts && git commit -m "feat(web): policy group categories, query log category and engine filter index memory"`.

## Task 17: E2E acceptance: categories per group over UDP, TCP and DoH, toggles, OISD guard, read-only catalog

Files: `e2e/filter_categories_test.go` (`TestFilterCategories`, `TestFilterCategoryToggles` and the stack helpers)
Interfaces: consumes `harness.CatalogMirrorEnv`, `(*harness.API).SetFilterCategory`, `FilterCategory`, `RefreshCategory`, `RefreshSource`, `ErrorCode` (Tasks 15 and M5), `udpFrom`, `question`, `waitLatestApplied` (existing e2e helpers); produces `type categoryStack`, `func startCategoryStack(t *testing.T, node string, opts func(env *harness.Env) harness.MgmtOptions) categoryStack`, `func (s categoryStack) answer(t *testing.T, transport, ip, name string) string`, `func (s categoryStack) want(t *testing.T, ip, name, ip4 string)`.

- [ ] Write `e2e/filter_categories_test.go`:
  ```go
  package e2e

  import (
  	"context"
  	"net"
  	"net/http"
  	"strings"
  	"testing"
  	"time"

  	"github.com/miekg/dns"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  // The DNS fixture answers every unlisted A query with 192.0.2.1; blocked names get 0.0.0.0 (null_ip).
  const unblocked = "192.0.2.1"

  type categoryStack struct {
  	env   *harness.Env
  	mg    *harness.Mgmt
  	api   *harness.API
  	lists *harness.HTTPFixture
  	eng   *harness.Engine
  	node  string
  }

  func startCategoryStack(t *testing.T, node string, opts func(env *harness.Env) harness.MgmtOptions) categoryStack {
  	t.Helper()
  	env := harness.New(t)
  	pg := env.StartPostgres()
  	ca := env.InitCA()
  	lists := env.StartHTTPFixture()
  	o := harness.MgmtOptions{}
  	if opts != nil {
  		o = opts(env)
  	}
  	o.DNSTLS = true
  	o.ExtraEnv = append(o.ExtraEnv, harness.CatalogMirrorEnv(lists))
  	mg := env.StartMgmt(pg, ca, o)
  	api := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
  	api.DisableForwardedValidation() // fixture upstreams serve unsigned data under the real root anchor
  	fx := env.StartDNSFixture()
  	api.Must(http.MethodPost, "/upstreams", map[string]any{"name": "fixture", "protocol": "udp", "address": fx.UDP, "timeout_ms": 250, "enabled": true, "position": 0}, nil, http.StatusCreated)
  	eng := env.StartManagedEngineWith(node, []string{mg.GRPCURL}, api.CreateJoinToken(), harness.EngineOptions{DoH: true})
  	waitLatestApplied(t, api, node)
  	return categoryStack{env: env, mg: mg, api: api, lists: lists, eng: eng, node: node}
  }

  // answer queries name (A) from ip over udp, tcp or doh and returns the first address.
  func (s categoryStack) answer(t *testing.T, transport, ip, name string) string {
  	t.Helper()
  	switch transport {
  	case "udp":
  		return firstA(udpFrom(t, ip, s.eng.DNS, name, dns.TypeA))
  	case "tcp":
  		c := &dns.Client{Net: "tcp", Timeout: 3 * time.Second, Dialer: &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(ip)}}}
  		m, _, err := c.Exchange(question(name, dns.TypeA), s.eng.DNS)
  		if err != nil {
  			t.Fatalf("tcp %s from %s: %v", name, ip, err)
  		}
  		return firstA(m)
  	default:
  		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
  		defer cancel()
  		c := harness.EncryptedClient{RootCAs: s.mg.DNSTLSRoots(), ServerName: "dns.nexora.test", LocalIP: net.ParseIP(ip)}
  		m, _, err := harness.DoH(ctx, c.HTTPClient(), s.eng.DoHURL(), http.MethodPost, question(name, dns.TypeA))
  		if err != nil {
  			t.Fatalf("doh %s from %s: %v", name, ip, err)
  		}
  		return firstA(m)
  	}
  }

  // want waits until UDP answers name from ip with addr, then requires TCP and DoH to agree.
  func (s categoryStack) want(t *testing.T, ip, name, addr string) {
  	t.Helper()
  	harness.EventuallyTrue(t, 20*time.Second, func() bool { return s.answer(t, "udp", ip, name) == addr }, name+" from "+ip+" over udp = "+addr)
  	for _, tr := range []string{"tcp", "doh"} {
  		if got := s.answer(t, tr, ip, name); got != addr {
  			t.Fatalf("%s from %s over %s = %q, want %s", name, ip, tr, got, addr)
  		}
  	}
  }

  func TestFilterCategories(t *testing.T) {
  	s := startCategoryStack(t, "cat-1", nil)
  	s.lists.SetList(t, "hagezi-pro", "ads.cat.test\n")
  	s.lists.SetList(t, "oisd-big", "*.oisd.cat.test\n")
  	s.lists.SetList(t, "hagezi-gambling", "casino.cat.test\n")

  	// Positive path first: with every category off nothing is blocked, for either client.
  	for _, ip := range []string{"127.0.0.1", "127.0.0.2"} {
  		for _, name := range []string{"ads.cat.test.", "x.oisd.cat.test.", "casino.cat.test."} {
  			s.want(t, ip, name, unblocked)
  		}
  	}

  	if code, reason := s.api.SetFilterCategory("ads-tracking", true, nil, true); code != http.StatusOK {
  		t.Fatalf("enable ads-tracking globally -> %d %s", code, reason)
  	}
  	s.api.RefreshCategory("ads-tracking")
  	s.api.Must(http.MethodPost, "/policy-groups", map[string]any{"name": "kids", "cidrs": []string{"127.0.0.2/32"}, "category_keys": []string{"gambling"}}, nil, http.StatusCreated)
  	s.api.RefreshCategory("gambling")
  	waitLatestApplied(t, s.api, s.node)

  	// Global clients: the global category blocks, including the OISD wildcard entry; gambling is only the group's.
  	s.want(t, "127.0.0.1", "ads.cat.test.", "0.0.0.0")
  	s.want(t, "127.0.0.1", "deep.x.oisd.cat.test.", "0.0.0.0")
  	s.want(t, "127.0.0.1", "casino.cat.test.", unblocked)
  	// The group client: its category blocks and its lists replace the global ones.
  	s.want(t, "127.0.0.2", "casino.cat.test.", "0.0.0.0")
  	s.want(t, "127.0.0.2", "www.casino.cat.test.", "0.0.0.0")
  	s.want(t, "127.0.0.2", "ads.cat.test.", unblocked)
  }

  func TestFilterCategoryToggles(t *testing.T) {
  	s := startCategoryStack(t, "cat-2", nil)
  	s.lists.SetList(t, "hagezi-gambling", "casino.toggle.test\n")
  	s.lists.SetList(t, "blp-gambling", "bets.toggle.test\n")
  	s.lists.SetList(t, "oisd-big", "*.oisd.toggle.test\n")
  	s.want(t, "127.0.0.1", "casino.toggle.test.", unblocked)
  	s.want(t, "127.0.0.1", "bets.toggle.test.", unblocked)

  	if code, reason := s.api.SetFilterCategory("gambling", true, nil, false); code != http.StatusOK {
  		t.Fatalf("enable gambling -> %d %s", code, reason)
  	}
  	s.api.RefreshCategory("gambling")
  	waitLatestApplied(t, s.api, s.node)
  	s.want(t, "127.0.0.1", "casino.toggle.test.", "0.0.0.0")
  	s.want(t, "127.0.0.1", "bets.toggle.test.", "0.0.0.0")

  	// One source off: its names resolve again, the other source keeps blocking.
  	if code, reason := s.api.SetFilterCategory("gambling", true, map[string]bool{"blp-gambling": false}, false); code != http.StatusOK {
  		t.Fatalf("disable blp-gambling -> %d %s", code, reason)
  	}
  	waitLatestApplied(t, s.api, s.node)
  	s.want(t, "127.0.0.1", "bets.toggle.test.", unblocked)
  	s.want(t, "127.0.0.1", "casino.toggle.test.", "0.0.0.0")

  	// The category off: nothing of it blocks.
  	if code, reason := s.api.SetFilterCategory("gambling", false, nil, false); code != http.StatusOK {
  		t.Fatalf("disable gambling -> %d %s", code, reason)
  	}
  	waitLatestApplied(t, s.api, s.node)
  	s.want(t, "127.0.0.1", "casino.toggle.test.", unblocked)

  	t.Run("oisd-license-guard", func(t *testing.T) {
  		code, reason := s.api.SetFilterCategory("ads-tracking", true, nil, false)
  		if code != http.StatusUnprocessableEntity || reason != "license_acknowledgement_required" {
  			t.Fatalf("OISD without acknowledge_license -> %d %s", code, reason)
  		}
  		if s.api.FilterCategory("ads-tracking").Enabled {
  			t.Fatal("the refused request enabled ads-tracking")
  		}
  		if code, reason := s.api.SetFilterCategory("ads-tracking", true, nil, true); code != http.StatusOK {
  			t.Fatalf("OISD with acknowledge_license -> %d %s", code, reason)
  		}
  		s.api.RefreshSource("ads-tracking", "oisd-big")
  		waitLatestApplied(t, s.api, s.node)
  		s.want(t, "127.0.0.1", "x.oisd.toggle.test.", "0.0.0.0")
  		var events []struct {
  			Action   string         `json:"action"`
  			TargetID string         `json:"target_id"`
  			Diff     map[string]any `json:"diff"`
  		}
  		s.api.Must(http.MethodGet, "/audit?limit=50", nil, &events, http.StatusOK)
  		found := false
  		for _, e := range events {
  			after, _ := e.Diff["after"].(map[string]any)
  			acks, _ := after["acknowledged_licenses"].([]any)
  			if e.Action == "updateFilterCategory" && e.TargetID == "ads-tracking" && len(acks) == 1 && acks[0] == "oisd-big" {
  				found = true
  			}
  		}
  		if !found {
  			t.Fatalf("no audit entry records the OISD acknowledgement: %+v", events)
  		}
  	})

  	t.Run("catalog-is-read-only", func(t *testing.T) {
  		before := s.api.FilterCategory("ads-tracking")
  		code, reason := s.api.ErrorCode(http.MethodPut, "/filter-categories/ads-tracking", map[string]any{"enabled": true, "revision": before.Revision,
  			"sources": []map[string]any{{"key": "my-own-source", "enabled": true}}})
  		if code != http.StatusUnprocessableEntity || reason != "unknown_source" {
  			t.Fatalf("custom source -> %d %s", code, reason)
  		}
  		for _, c := range []struct{ method, path string }{
  			{http.MethodPost, "/filter-categories"},
  			{http.MethodDelete, "/filter-categories/ads-tracking"},
  			{http.MethodPost, "/filter-categories/ads-tracking/sources"},
  			{http.MethodPatch, "/filter-categories/ads-tracking"},
  		} {
  			if code, _ := s.api.Do(c.method, c.path, map[string]any{"key": "mine", "url": "https://evil.test/list.txt"}, nil); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
  				t.Errorf("%s %s -> %d, want 404 or 405", c.method, c.path, code)
  			}
  		}
  		managed := before.Sources[0].ListID
  		code, reason = s.api.ErrorCode(http.MethodPut, "/filter-lists/"+managed, map[string]any{"name": "hijack", "kind": "block", "url": "https://evil.test/list.txt",
  			"refresh_interval_seconds": 3600, "enabled": true, "revision": 1})
  		if code != http.StatusUnprocessableEntity || reason != "catalog_managed" {
  			t.Fatalf("editing a catalog list -> %d %s", code, reason)
  		}
  		after := s.api.FilterCategory("ads-tracking")
  		if len(after.Sources) != len(before.Sources) {
  			t.Fatalf("sources %d -> %d", len(before.Sources), len(after.Sources))
  		}
  		for i := range after.Sources {
  			if after.Sources[i].URL != before.Sources[i].URL || strings.Contains(after.Sources[i].URL, "evil.test") {
  				t.Fatalf("source %s url changed to %s", after.Sources[i].Key, after.Sources[i].URL)
  			}
  		}
  	})
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=bin go test -count=1 -timeout 30m -run "TestFilterCategories|TestFilterCategoryToggles" ./e2e/'` and expect `ok` (every behaviour exists after Tasks 1–16). Mutation check: temporarily make `UpdateFilterCategory` skip the acknowledgement check, rerun `-run TestFilterCategoryToggles` and expect FAIL with `OISD without acknowledge_license -> 200`; restore it.
- [ ] Commit: `git add e2e/filter_categories_test.go && git commit -m "test(e2e): filter categories per group over UDP, TCP and DoH, toggles, license guard and read-only catalog"`.

## Task 18: E2E acceptance: UT1 archive members, query-log attribution in both backends, filter metrics

Files: `e2e/filter_attribution_test.go` (`TestUT1ArchiveMember`, `TestQueryLogCategoryAttribution`), `e2e/harness/otelcol.go` (OpenSearch pipeline `transform/querylog`, index `nexora-querylog-v2`), `e2e/observability_test.go` (filter metrics in `TestObservabilityMetricsTraces`)
Interfaces: consumes `startCategoryStack` and `categoryStack.want` (Task 17), `harness.UT1Archive`, `(*harness.API).RefreshSource` (Task 15); query-log API fields `list_id`, `category` and parameter `category` (Task 14).

- [ ] Write `e2e/filter_attribution_test.go`:
  ```go
  package e2e

  import (
  	"fmt"
  	"net/http"
  	"net/url"
  	"strings"
  	"testing"
  	"time"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  func TestUT1ArchiveMember(t *testing.T) {
  	s := startCategoryStack(t, "ut1-1", nil)
  	s.lists.SetList(t, "ut1-gambling", string(harness.UT1Archive(t, map[string]string{
  		"blacklists/gambling/domains": "casino.ut1.test\n",
  		"blacklists/adult/domains":    "adult.ut1.test\n",
  		"blacklists/gambling/urls":    "urls.ut1.test/path\n",
  	})))
  	s.want(t, "127.0.0.1", "casino.ut1.test.", unblocked)
  	s.want(t, "127.0.0.1", "adult.ut1.test.", unblocked)

  	if code, reason := s.api.SetFilterCategory("gambling", true, map[string]bool{"hagezi-gambling": false, "blp-gambling": false, "stevenblack-gambling": false}, false); code != http.StatusOK {
  		t.Fatalf("enable gambling with only ut1-gambling -> %d %s", code, reason)
  	}
  	if st := s.api.RefreshSource("gambling", "ut1-gambling"); st.EntryCount != 1 || st.LastError != "" {
  		t.Fatalf("ut1-gambling member: %+v", st)
  	}
  	waitLatestApplied(t, s.api, s.node)
  	s.want(t, "127.0.0.1", "casino.ut1.test.", "0.0.0.0")
  	s.want(t, "127.0.0.1", "adult.ut1.test.", unblocked) // other members of the archive do not leak in

  	// The member disappears from the archive: the error is reported, the last good list stays.
  	s.lists.SetList(t, "ut1-gambling", string(harness.UT1Archive(t, map[string]string{"blacklists/adult/domains": "adult.ut1.test\n"})))
  	st := s.api.RefreshSource("gambling", "ut1-gambling")
  	if st.LastError != "archive member blacklists/gambling/domains not found" || !st.Stale || st.EntryCount != 1 {
  		t.Fatalf("missing member: %+v", st)
  	}
  	s.want(t, "127.0.0.1", "casino.ut1.test.", "0.0.0.0")
  	if !s.api.FilterCategory("gambling").Stale {
  		t.Fatal("a category with a failing enabled source is not marked stale")
  	}

  	// A corrupt archive is an error too.
  	s.lists.SetList(t, "ut1-gambling", "this is not a gzip archive")
  	if st := s.api.RefreshSource("gambling", "ut1-gambling"); !strings.HasPrefix(st.LastError, "read archive: ") || st.EntryCount != 1 {
  		t.Fatalf("corrupt archive: %+v", st)
  	}
  	s.want(t, "127.0.0.1", "casino.ut1.test.", "0.0.0.0")
  }

  type attributedRecord struct {
  	Name     string `json:"name"`
  	Filter   string `json:"filter"`
  	ListID   string `json:"list_id"`
  	Category string `json:"category"`
  }

  func TestQueryLogCategoryAttribution(t *testing.T) {
  	for _, backend := range []string{"builtin", "opensearch"} {
  		t.Run(backend, func(t *testing.T) {
  			s := startCategoryStack(t, "attr-"+backend, func(env *harness.Env) harness.MgmtOptions {
  				o := harness.MgmtOptions{QueryLogBackend: backend}
  				if backend == "opensearch" {
  					col := env.StartOtelcol(harness.OtelcolConfig{OpenSearchURL: harness.OpenSearchURL(t), DebugFile: env.Dir + "/otel.jsonl"})
  					o.OpenSearchURL = harness.OpenSearchURL(t)
  					o.OTLPEndpoint = "http://" + col.OTLPGRPC
  				}
  				return o
  			})
  			casino := strings.TrimSuffix(harness.UniqueName("casino"), ".")
  			both := strings.TrimSuffix(harness.UniqueName("both"), ".")
  			clean := harness.UniqueName("clean")
  			s.lists.SetList(t, "hagezi-gambling", casino+"\n"+both+"\n")
  			s.lists.SetList(t, "hagezi-pro", both+"\n")
  			if code, reason := s.api.SetFilterCategory("gambling", true, nil, false); code != http.StatusOK {
  				t.Fatalf("enable gambling -> %d %s", code, reason)
  			}
  			if code, reason := s.api.SetFilterCategory("ads-tracking", true, nil, true); code != http.StatusOK {
  				t.Fatalf("enable ads-tracking -> %d %s", code, reason)
  			}
  			s.api.RefreshSource("gambling", "hagezi-gambling")
  			s.api.RefreshSource("ads-tracking", "hagezi-pro")
  			waitLatestApplied(t, s.api, s.node)
  			s.want(t, "127.0.0.1", casino+".", "0.0.0.0")
  			s.want(t, "127.0.0.1", both+".", "0.0.0.0")
  			s.want(t, "127.0.0.1", clean, unblocked)

  			listID := func(category, source string) string {
  				for _, src := range s.api.FilterCategory(category).Sources {
  					if src.Key == source {
  						return src.ListID
  					}
  				}
  				t.Fatalf("source %s missing", source)
  				return ""
  			}
  			search := func(query string) []attributedRecord {
  				var page struct {
  					Records []attributedRecord `json:"records"`
  				}
  				s.api.Must(http.MethodGet, "/query-log?"+query, nil, &page, http.StatusOK)
  				return page.Records
  			}
  			find := func(query, name string) (attributedRecord, error) {
  				for _, r := range search(query) {
  					if strings.TrimSuffix(r.Name, ".") == strings.TrimSuffix(name, ".") {
  						return r, nil
  					}
  				}
  				return attributedRecord{}, fmt.Errorf("no record for %s in %s", name, query)
  			}
  			var r attributedRecord
  			harness.Eventually(t, 60*time.Second, func() error {
  				var err error
  				r, err = find("category=gambling&name="+url.QueryEscape(casino), casino)
  				return err
  			})
  			if r.Filter != "blocked" || r.Category != "gambling" || r.ListID != listID("gambling", "hagezi-gambling") {
  				t.Fatalf("casino record: %+v", r)
  			}
  			// Listed in gambling and ads-tracking: the log names the first category in catalog order.
  			harness.Eventually(t, 60*time.Second, func() error {
  				var err error
  				r, err = find("name="+url.QueryEscape(both), both)
  				return err
  			})
  			if r.Category != "ads-tracking" || r.ListID != listID("ads-tracking", "hagezi-pro") {
  				t.Fatalf("record listed in two categories: %+v", r)
  			}
  			harness.Eventually(t, 60*time.Second, func() error {
  				var err error
  				r, err = find("name="+url.QueryEscape(strings.TrimSuffix(clean, ".")), clean)
  				return err
  			})
  			if r.Category != "" || r.ListID != "" || r.Filter == "blocked" {
  				t.Fatalf("clean record carries attribution: %+v", r)
  			}
  			if _, err := find("category=ads-tracking&name="+url.QueryEscape(casino), casino); err == nil {
  				t.Fatal("the category filter returned a record of another category")
  			}
  		})
  	}
  }
  ```
- [ ] In `e2e/observability_test.go` `TestObservabilityMetricsTraces`, after the engine applied the first version, add a custom list and a blocked query, and extend the engine metric list:
  ```go
  	lists := env.StartHTTPFixture()
  	lists.SetList(t, "obs-block", "blocked.obs.test\n")
  	var fl map[string]any
  	api.Must("POST", "/filter-lists", map[string]any{"name": "obs-block", "kind": "block", "url": lists.URL("obs-block"), "refresh_interval_seconds": 3600, "enabled": true}, &fl, 201)
  	api.Must("POST", "/filter-lists/"+fl["id"].(string)+"/refresh", nil, nil, 200)
  	waitLatestApplied(t, api, "engine-obs")
  	harness.MustQuery(t, eng.DNS, "x.blocked.obs.test.", dns.TypeA, harness.QueryOpts{})
  ```
  and append to the `for _, m := range []string{...}` list: `` `nexora_filter_blocked_total{category="custom"} 1` ``, `"nexora_filter_index_entries 1"`, `"nexora_filter_index_bytes "`, `"nexora_filter_index_max_bytes "`, `"nexora_filter_index_build_seconds "`, `` `nexora_filter_index_decision_seconds{kind="blocked",cpu="` ``.
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=bin go test -count=1 -timeout 30m -run "TestUT1ArchiveMember|TestQueryLogCategoryAttribution|TestObservabilityMetricsTraces" ./e2e/'` and expect FAIL in `TestQueryLogCategoryAttribution/opensearch` with `no record for` (OpenSearch rejects documents carrying both `nexora.filter` and `nexora.filter.category`), while `TestUT1ArchiveMember`, `TestQueryLogCategoryAttribution/builtin` and `TestObservabilityMetricsTraces` pass.
- [ ] In `e2e/harness/otelcol.go` `otelcolTemplate`, add under `processors:` when `OpenSearchURL` is set:
  ```yaml
  {{- if .OpenSearchURL}}
    transform/querylog:
      log_statements:
        - context: log
          statements:
            - set(attributes["nexora.filter.result"], attributes["nexora.filter"]) where attributes["nexora.filter"] != nil
            - delete_key(attributes, "nexora.filter")
  {{- end}}
  ```
  change the exporter's `logs_index` to `"nexora-querylog-v2"`, and split the logs pipeline so the OpenSearch exporter gets the transformed records: `logs: { receivers: [otlp], processors: [batch], exporters: [debug{{if .DebugFile}}, file{{end}}] }` plus, when `OpenSearchURL` is set, `logs/opensearch: { receivers: [otlp], processors: [batch, transform/querylog], exporters: [opensearch] }`.
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=bin go test -count=1 -timeout 40m -run "TestUT1ArchiveMember|TestQueryLogCategoryAttribution|TestObservabilityMetricsTraces|TestQueryLogBackends" ./e2e/'` and expect `ok` for all four (`TestQueryLogBackends` proves the M1 query-log screen still reads the renamed filter field).
- [ ] Commit: `git add e2e/filter_attribution_test.go e2e/harness/otelcol.go e2e/observability_test.go && git commit -m "test(e2e): UT1 archive members, query-log category attribution in both backends, filter metrics"`.

## Task 19: CI performance gate for the filter benchmark

Files: `bench/cmd/perfgate/filter.go` (`filter-compare`), `bench/cmd/perfgate/filter_test.go`, `bench/cmd/perfgate/main.go` (subcommand dispatch and usage), `.github/workflows/perf-gate.yml` (interleaved base/head filter rounds in the relative job)
Interfaces: ``type FilterResult struct { UniqueNames uint64 `json:"unique_names"`; IndexBytes uint64 `json:"index_bytes"`; BytesPerName float64 `json:"bytes_per_name"`; BuildSeconds float64 `json:"build_seconds"`; BlockedNS float64 `json:"blocked_ns"`; CleanNS float64 `json:"clean_ns"` }``; `type FilterVerdict struct { BlockedRatio, CleanRatio float64; Pass bool; Reasons []string }`; `func CompareFilter(base, head []FilterResult, maxRegress float64) (FilterVerdict, error)`; `perfgate filter-compare --base A1.json,... --head B1.json,... --max-regress 0.05`.

As built (deviations from the code below): `filter_bench` gained the Zipf decision-cache workload (`zipf_ns`), so `FilterResult` also reads `ZipfNS float64 \`json:"zipf_ns"\``and`FilterVerdict`has`ZipfRatio float64`and`ZipfGated bool`: the median Zipf ratio is gated (reason `Zipf (decision cache) decisions %.2f%% slower ...`) only when every base and head round reports a non-zero `zipf_ns`, so the PR adding the workload passes on blocked/clean alone. `CompareFilter`also errors on a round with a zero blocked or clean time;`TestCompareFilterZipf`covers the Zipf rules. The workflow step greps the base for`synthetic`(as committed at 09b816d), sends the JSON printed on stdout to`/dev/null`(the files are uploaded) and carries a`debt:`note that 5% is not yet shown above this runner's A/A noise. Dev pod (arm64, shared load) with two separate builds of one tree, 9 interleaved rounds as in the workflow (about 13 s per run, 3m50s for 18 runs): A/A median ratios blocked 1.0028, clean 0.9959, Zipf 1.0067, PASS; with 16`black_box`iterations injected at the top of`FilterView::decide` in the head copy: blocked 1.0189, clean 1.0579, Zipf 1.0148, FAIL (`clean decisions 5.79% slower`).

- [ ] Write the failing test `bench/cmd/perfgate/filter_test.go`:
  ```go
  package main

  import (
  	"strings"
  	"testing"
  )

  func TestCompareFilter(t *testing.T) {
  	round := func(blocked, clean float64) FilterResult {
  		return FilterResult{UniqueNames: 2_000_000, BlockedNS: blocked, CleanNS: clean}
  	}
  	base := []FilterResult{round(100, 90), round(102, 91), round(98, 89)}
  	v, err := CompareFilter(base, []FilterResult{round(103, 92), round(104, 93), round(101, 90)}, 0.05)
  	if err != nil || !v.Pass {
  		t.Fatalf("3%% slower must pass: %+v %v", v, err)
  	}
  	v, _ = CompareFilter(base, []FilterResult{round(107, 90), round(108, 91), round(104, 89)}, 0.05)
  	if v.Pass || len(v.Reasons) != 1 || !strings.Contains(v.Reasons[0], "blocked") {
  		t.Fatalf("6%% slower blocked decisions must fail: %+v", v)
  	}
  	v, _ = CompareFilter(base, []FilterResult{round(100, 99), round(102, 99), round(98, 99)}, 0.05)
  	if v.Pass || !strings.Contains(strings.Join(v.Reasons, ";"), "clean") {
  		t.Fatalf("10%% slower clean decisions must fail: %+v", v)
  	}
  	// One noisy round does not decide: the median ratio does.
  	v, _ = CompareFilter(base, []FilterResult{round(150, 91), round(101, 91), round(99, 89)}, 0.05)
  	if !v.Pass {
  		t.Fatalf("a single outlier round failed the gate: %+v", v)
  	}
  	if _, err := CompareFilter(base, base[:2], 0.05); err == nil {
  		t.Fatal("unequal round counts must be an error")
  	}
  	if _, err := CompareFilter(base, []FilterResult{round(100, 90), {UniqueNames: 1}, round(98, 89)}, 0.05); err == nil {
  		t.Fatal("rounds over different corpora must be an error")
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./bench/cmd/perfgate/ -run TestCompareFilter -count=1` and expect FAIL with `undefined: FilterResult`.
- [ ] Implement `filter.go`: `CompareFilter` errors on an empty base, unequal lengths (`%d base rounds but %d head rounds`) or a round pair whose `UniqueNames` differ (`round %d compares %d and %d names`); it takes the median of `head[i].BlockedNS / base[i].BlockedNS` and of the clean ratios; each above `1 + maxRegress + 1e-9` adds the reason `blocked decisions %.2f%% slower (median of %d rounds)` / `clean decisions ...`; `Pass` is no reasons. `cmdFilterCompare(args []string) error` parses `--base`, `--head` (comma-separated JSON files of `filter_bench --json`) and `--max-regress`, prints both ratios, the median `bytes_per_name` of each side and the verdict, and returns an error when the gate fails. Register `filter-compare` in `main.go`'s dispatch and usage text.
- [ ] Add to the `relative` job of `.github/workflows/perf-gate.yml`, after `Fail on more than 5% QPS drop`:
  ```yaml
  # The filter index decision time, interleaved like the QPS rounds. The base commit of the PR that
  # introduces filter_bench has no benchmark CLI yet; that one PR passes with a notice.
  - name: Interleaved filter benchmark (9 rounds) and 5% decision-time gate
    run: |
      if ! grep -q -- "--synthetic" base/engine/examples/filter_bench.rs 2>/dev/null; then
        echo "::notice::base has no filter_bench CLI; the filter gate applies from the next PR"
        exit 0
      fi
      (cd base && CARGO_TARGET_DIR=/tmp/target-base cargo build --locked --release -p nexora-engine --example filter_bench)
      (cd head && CARGO_TARGET_DIR=/tmp/target-head cargo build --locked --release -p nexora-engine --example filter_bench)
      fb() { "/tmp/target-$1/release/examples/filter_bench" --synthetic 2000000 --seed 7 --threads 2 --rounds 5 --json "filter-$1-$2.json"; }
      for i in $(seq 1 9); do
        if [ $((i % 2)) -eq 1 ]; then fb base "$i"; fb head "$i"; else fb head "$i"; fb base "$i"; fi
      done
      files() { seq 1 9 | sed "s/.*/filter-$1-&.json/" | paste -sd, -; }
      ./perfgate filter-compare --base "$(files base)" --head "$(files head)" --max-regress 0.05
  ```
  and change the job's artifact upload `path` to `"*.json"` (already covering `filter-*.json`).
- [ ] Run `scripts/dev-exec.sh 'go test ./bench/... -count=1 && go build -o bin/perfgate ./bench/cmd/perfgate && cargo build --locked --release -p nexora-engine --example filter_bench && T="${CARGO_TARGET_DIR:-target}/release/examples/filter_bench"; for s in a b; do "$T" --synthetic 200000 --seed 7 --threads 2 --rounds 3 --json /tmp/filter-$s.json >/dev/null; done && bin/perfgate filter-compare --base /tmp/filter-a.json --head /tmp/filter-b.json --max-regress 0.5'` and expect `ok` and a passing verdict line.
- [ ] Commit: `git add bench/cmd/perfgate .github/workflows/perf-gate.yml && git commit -m "ci(perf-gate): relative filter decision-time gate from interleaved filter_bench rounds"`.

## Task 20: kw deployment and TestKwFilterCategories with the real catalog

Files: `deploy/kw/otelcol.yaml` (OpenSearch pipeline transform and `nexora-querylog-v2`), `deploy/kw/values-kw.yaml` (explicit engine memory limit behind the 512 MiB cap), `deploy/kw/README.md` (Filter categories section), `scripts/kw-acceptance.sh` (runs the test, report path), `e2e/kw_filter_categories_test.go` (`TestKwFilterCategories`), `docs/architecture.md` (kw acceptance line), `docs/operations.md` (catalog, OISD notice, memory cap, recorded kw numbers)
Interfaces: consumes `loadKwEnv`, `kwLogin`, `kwWaitApplied`, `kwEnv.dnsAddr`, `kwEnv.engines` (kw tests), `harness.CategoryView`, `(*harness.API).SetFilterCategory`, `RefreshCategory`, `FilterCategory` (Task 15), `GET /engines` and `GET /engines/{id}/stats` `filter_index` (Task 13); environment `NEXORA_KW_FILTER_REPORT` (JSON report path).

- [ ] Write `e2e/kw_filter_categories_test.go`:
  ```go
  package e2e

  import (
  	"archive/tar"
  	"bufio"
  	"compress/gzip"
  	"encoding/json"
  	"errors"
  	"fmt"
  	"io"
  	"net/http"
  	"os"
  	"regexp"
  	"strings"
  	"testing"
  	"time"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  type kwFilterIndex struct {
  	Entries           int64   `json:"entries"`
  	Bytes             int64   `json:"bytes"`
  	MaxBytes          int64   `json:"max_bytes"`
  	BuildSeconds      float64 `json:"build_seconds"`
  	DecisionNSBlocked float64 `json:"decision_ns_blocked"`
  	DecisionNSClean   float64 `json:"decision_ns_clean"`
  	CPU               string  `json:"cpu"`
  }

  var kwListName = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?)+$`)

  // kwSourceNames downloads a catalog source (e2e cannot import mgmt/internal, so this repeats the
  // domain, hosts and wildcard formats) and returns up to n names from the middle of the list.
  func kwSourceNames(t *testing.T, src harness.CategorySource, member string, n int) []string {
  	t.Helper()
  	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Get(src.URL)
  	if err != nil {
  		t.Fatalf("download %s: %v", src.Key, err)
  	}
  	defer resp.Body.Close()
  	var body io.Reader = resp.Body
  	if member != "" {
  		gz, err := gzip.NewReader(resp.Body)
  		if err != nil {
  			t.Fatalf("%s archive: %v", src.Key, err)
  		}
  		tr := tar.NewReader(gz)
  		for {
  			h, err := tr.Next()
  			if errors.Is(err, io.EOF) {
  				t.Fatalf("%s: archive member %s not found", src.Key, member)
  			}
  			if err != nil {
  				t.Fatalf("%s archive: %v", src.Key, err)
  			}
  			if strings.TrimPrefix(h.Name, "./") == member {
  				body = tr
  				break
  			}
  		}
  	}
  	var names []string
  	sc := bufio.NewScanner(body)
  	sc.Buffer(make([]byte, 64*1024), 1024*1024)
  	for sc.Scan() {
  		f := strings.Fields(strings.ToLower(sc.Text()))
  		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
  			continue
  		}
  		name := f[0]
  		if (name == "0.0.0.0" || name == "127.0.0.1") && len(f) > 1 {
  			name = f[1]
  		}
  		name = strings.TrimPrefix(name, "*.")
  		if kwListName.MatchString(name) && name != "0.0.0.0" {
  			names = append(names, name)
  		}
  	}
  	if len(names) == 0 {
  		t.Fatalf("%s: no names (%v)", src.Key, sc.Err())
  	}
  	start := len(names) / 2
  	return names[start:min(start+n, len(names))]
  }

  func TestKwFilterCategories(t *testing.T) {
  	env := loadKwEnv(t)
  	api := kwLogin(t, env)
  	var cats []harness.CategoryView
  	api.Must(http.MethodGet, "/filter-categories", nil, &cats, http.StatusOK)
  	t.Cleanup(func() {
  		for _, c := range cats {
  			if code, reason := api.SetFilterCategory(c.Key, c.Enabled, nil, false); code != http.StatusOK {
  				t.Errorf("restore %s -> %d %s", c.Key, code, reason)
  			}
  		}
  		kwWaitApplied(t, api, env.engines)
  	})

  	for _, c := range cats {
  		if code, reason := api.SetFilterCategory(c.Key, true, nil, true); code != http.StatusOK {
  			t.Fatalf("enable %s -> %d %s", c.Key, code, reason)
  		}
  		api.RefreshCategory(c.Key)
  	}
  	kwWaitApplied(t, api, env.engines)

  	t.Run("every-category-blocks-real-names", func(t *testing.T) {
  		for _, cat := range cats {
  			view := api.FilterCategory(cat.Key)
  			var source *harness.CategorySource
  			for i, s := range view.Sources {
  				if s.Enabled && s.EntryCount > 0 && s.LastError == "" {
  					source = &view.Sources[i]
  					break
  				}
  			}
  			if source == nil {
  				t.Errorf("category %s has no source with entries: %+v", cat.Key, view.Sources)
  				continue
  			}
  			for _, s := range view.Sources {
  				if s.Enabled && s.LastError != "" {
  					t.Logf("source %s of %s failed its refresh (other sources still apply): %s", s.Key, cat.Key, s.LastError)
  				}
  			}
  			member := kwArchiveMember(source.Key)
  			blocked := 0
  			names := kwSourceNames(t, *source, member, 3)
  			for _, name := range names {
  				addrs, _, err := kwQueryA(env.dnsAddr, name)
  				if err == nil && len(addrs) == 1 && addrs[0] == "0.0.0.0" {
  					blocked++
  				}
  			}
  			if blocked < 2 {
  				t.Errorf("category %s (source %s): %d of %v blocked on %s", cat.Key, source.Key, blocked, names, env.dnsAddr)
  			}
  		}
  	})

  	t.Run("per-engine-memory-and-decision-time", func(t *testing.T) {
  		var engines []harness.EngineView
  		api.Must(http.MethodGet, "/engines", nil, &engines, http.StatusOK)
  		report := map[string]kwFilterIndex{}
  		harness.Eventually(t, 3*time.Minute, func() error {
  			for _, e := range engines {
  				if !e.Connected {
  					continue
  				}
  				var stats struct {
  					FilterIndex *kwFilterIndex `json:"filter_index"`
  				}
  				api.Must(http.MethodGet, "/engines/"+e.ID+"/stats?window=5m", nil, &stats, http.StatusOK)
  				if stats.FilterIndex == nil || stats.FilterIndex.Entries < 1_000_000 {
  					return fmt.Errorf("engine %s has not reported the category index yet: %+v", e.NodeName, stats.FilterIndex)
  				}
  				report[e.NodeName] = *stats.FilterIndex
  			}
  			return nil
  		})
  		for node, fi := range report {
  			scale := max(1, float64(fi.Entries)/5.1e6)
  			t.Logf("%s: %d names, %.1f MiB (cap %.0f MiB), build %.2f s, %.0f ns blocked, %.0f ns clean on %s",
  				node, fi.Entries, float64(fi.Bytes)/(1<<20), float64(fi.MaxBytes)/(1<<20), fi.BuildSeconds, fi.DecisionNSBlocked, fi.DecisionNSClean, fi.CPU)
  			if float64(fi.Bytes) >= 120e6*scale {
  				t.Errorf("%s: filter index %d bytes for %d names, budget %.0f", node, fi.Bytes, fi.Entries, 120e6*scale)
  			}
  			if fi.BuildSeconds >= 1.5*scale {
  				t.Errorf("%s: build %.2f s, budget %.2f s", node, fi.BuildSeconds, 1.5*scale)
  			}
  			if fi.CPU == "cortex-a76" && (fi.DecisionNSBlocked >= 150 || fi.DecisionNSClean >= 150) {
  				t.Errorf("%s: %.0f ns blocked / %.0f ns clean on a Cortex-A76, budget 150 ns", node, fi.DecisionNSBlocked, fi.DecisionNSClean)
  			}
  		}
  		if path := os.Getenv("NEXORA_KW_FILTER_REPORT"); path != "" {
  			raw, _ := json.MarshalIndent(report, "", "  ")
  			if err := os.WriteFile(path, raw, 0o644); err != nil {
  				t.Fatal(err)
  			}
  		}
  	})
  }

  // kwArchiveMember is the UT1 member of a catalog source key, empty for plain lists.
  func kwArchiveMember(sourceKey string) string {
  	members := map[string]string{
  		"ut1-malware": "blacklists/malware/domains", "ut1-phishing": "blacklists/phishing/domains",
  		"ut1-adult": "blacklists/adult/domains", "ut1-gambling": "blacklists/gambling/domains",
  		"ut1-social-networks": "blacklists/social_networks/domains", "ut1-cryptojacking": "blacklists/cryptojacking/domains",
  		"ut1-warez": "blacklists/warez/domains", "ut1-drogue": "blacklists/drogue/domains",
  		"ut1-fakenews": "blacklists/fakenews/domains", "ut1-dating": "blacklists/dating/domains", "ut1-games": "blacklists/games/domains",
  	}
  	return members[sourceKey]
  }
  ```
- [ ] `deploy/kw/bootstrap.sh` (added to this task): idempotently enables the catalog categories in `NEXORA_KW_CATEGORIES` (default `malware phishing ads-tracking crypto-mining`; an unknown key fails the bootstrap) with `acknowledge_license: true` (OISD, URLhaus: non-commercial lab), refreshing the enabled sources of a newly enabled category and reporting a failing source without failing. `TestKwFilterCategories` restores this state after enabling every category.
- [ ] Run `scripts/dev-exec.sh 'go vet ./e2e/ && go test -count=1 -run TestKwFilterCategories ./e2e/'` and expect `ok` with `--- SKIP: TestKwFilterCategories` (no `NEXORA_KW_*` variables in the plain dev-pod run), proving the test compiles and skips outside kw.
- [ ] `deploy/kw/otelcol.yaml`: add the `transform/querylog` processor exactly as in Task 18, set `logs_index: nexora-querylog-v2`, and route logs through `processors: [batch, transform/querylog]` to the `opensearch` exporter. `deploy/kw/values-kw.yaml`: under `engine:` add `resources: { requests: { cpu: 250m, memory: 384Mi }, limits: { cpu: "2", memory: 1Gi } }` (a 1 GiB limit gives the engines a 512 MiB filter index cap; the default catalog selection needs about 110 MB). `scripts/kw-acceptance.sh`: default run pattern `TestKwSmoke|TestKwFullProduct|TestKwFilterCategories`, add `NEXORA_KW_FILTER_REPORT=/work/kw-filter-categories.json` to the environment, `-timeout 75m`, and update its header comment. `deploy/kw/README.md`: new section `## Filter categories` stating that categories are off after install, `TestKwFilterCategories` enables all of them with the real catalog sources (acknowledging the OISD and URLhaus notices for this non-commercial lab), restores the previous state afterwards, and writes per-engine memory and decision time to `/work/kw-filter-categories.json`; and that the collector writes `nexora-querylog-v2-*`. `docs/architecture.md` `## Deployment on kw`: the acceptance sentence lists `TestKwFilterCategories`. `docs/operations.md`: a `## Filter categories` section (catalog is read-only and ships with releases; license and attribution per source; `commercial_use: false` sources need acknowledgement, recorded in the audit log; `NEXORA_CATALOG_MIRROR`; memory cap default and `filter_index_max_bytes` per engine group; stale categories and `nexora_mgmt_filter_category_stale`).
- [ ] Deploy and verify on kw, collector first so no query-log document meets the old mapping: `kubectl --context kw -n nexora apply -f deploy/kw/otelcol.yaml && kubectl --context kw -n nexora rollout status deploy/nexora-otelcol --timeout=5m && scripts/kw-deploy.sh` and expect the rollout to finish and the script to print the kw test environment.
- [ ] Run `scripts/kw-acceptance.sh 'TestKwSmoke|TestKwFullProduct|TestKwFilterCategories'` and expect `ok` for all three, then copy the report with `kubectl --context kw -n nexora-dev exec deploy/toolbox -c toolbox -- cat /work/kw-filter-categories.json` and append one row per engine (node, CPU, names, bytes, build seconds, blocked ns, clean ns, date, commit) to the `## Filter index performance` table in `docs/operations.md` next to the Task 4 benchmark row.
- [ ] Commit: `git add deploy/kw/otelcol.yaml deploy/kw/values-kw.yaml deploy/kw/README.md scripts/kw-acceptance.sh e2e/kw_filter_categories_test.go docs/architecture.md docs/operations.md && git commit -m "feat(kw): filter categories on kw with per-engine index memory and decision-time acceptance"`.
