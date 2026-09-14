# nexora-m7-hardening — implementation plan

Status: draft
Spec: .procoder/specs/nexora-m7-hardening.md

## Goal

Prove every workflow and the Compose example on real infrastructure (#3, #5) and lift the 21 v1 debt limits (#9–#29), each with a test that fails before the change.

## Architecture

Every issue is a small, independent change in the file that carries its `debt:` marker. Three shared pieces are settled first in `docs/architecture.md` (Task 1):

- the proto field `RecursionConfig.cache_max_bytes = 800`, used by the engine (Task 10) and the management plane (Task 12);
- the per-worker stream buffer pool (Task 11);
- the migration numbers 00800/00801 (Tasks 12, 13).

Everything else only touches its own module. Tasks own their files exclusively, so tasks in the same wave can run as parallel implementer agents. The lead commits serially with `scripts/commit-paths.sh`.

### Design choices (from the spec, binding)

- **ACL (#11):** sorted, merged `(start, end)` ranges per family with `partition_point`, not a prefix trie.
- **Transfers (#12):** `tokio::task::spawn_blocking` around authorisation, planning and encoding.
- **Renewal (#13):** a staged certificate that fails for a reason other than PermissionDenied/Unauthenticated makes the next attempt use the current identity. If that attempt reaches the stream, the staged certificate is discarded.
- **Enrollment (#14):** an unstorable identity stays in memory; saving it is retried and it is never enrolled again.
- **RFC 5011 (#15):** revocation by the only trusted key is accepted, as §2.1 allows; the emptied trust point disappears, as §5 requires (the store already does this).
- **Recursor memory (#16, #18):**
  - one budget, `cache_max_bytes` (0 → 64 MiB), split 12/16 RRset, 3/16 aggressive NSEC, 1/16 infra;
  - byte-weighted quick_cache (`with_weighter`, `set_capacity`);
  - the NSEC cache evicts whole zones oldest first.
- **Infra cache (#17):** `quick_cache::sync::Cache::entry`.
- **RPZ IXFR (#19):** prefilter by deleted owner names (hickory `Name` hashes and compares case-insensitively).
- **Buffers (#20, #22):** a thread-local pool in `engine/src/server/buffers.rs`. Workers are single-threaded tokio runtimes, so thread-local means per worker.
- **BADVERS (#21):** header RCODE 0, OPT extended RCODE octet 1, version 0, no cookie. It is checked right after the question parse, before everything else.
- **Labels (#23):** `Runtime.labels` holds the upstream-name and policy-group-id tables of up to 8 versions, newest first, built in `Runtime::build_with` from the previous runtime.
- **Exports (#24):** a `JoinSet` of at most 4 exports, with an immediate re-drain while the ring holds a full batch.
- **Blobs (#25):** per-chunk `substring(data from $2 for $3)` queries without a transaction, because blobs are content-addressed and immutable. A row that disappears mid-stream means NotFound. Migration 00801 sets `STORAGE EXTERNAL`.
- **OpenSearch (#26):** sort `@timestamp` desc, then `_id` asc; a one-element cursor gains `""`. `indices.id_field_data.enabled` was checked on kw's OpenSearch 3.8.0 (default `true`, and a trial search sorted by `_id` succeeded).
- **PKCS#11 (#27):** replace an invalidated session and retry once; re-read the PIN file for login.
- **Zone export (#28):** `zone.Service.ExportTo` streams rows. Order: SOA from the zone row, then `ORDER BY lower(owner) <> lower(zone name), lower(owner) COLLATE "C", rtype, rdata_wire`. Import keeps its limits.
- **Zone edits (#29):**
  - owner-scoped checks;
  - an incremental journal delta for unsigned primary zones, built from the edited RRsets before and after the edit;
  - the full set is loaded only when an image is due;
  - signed zones keep the full rebuild behind a rewritten `debt:` marker.
- **Compose (#5):** `scripts/compose-verify.sh` runs the documented commands over ssh.
  - Images come from `192.168.10.131:5000/azrtydxb`, which the laptop reads anonymously with `crane`, then `docker load` on the host with `NEXORA_REGISTRY=192.168.10.131:5000/azrtydxb`.
  - `COMPOSE_PROJECT_NAME=nexora-verify` and non-default ports.
- **CI (#3):** the dispatched perf-gate verdict belongs to #6. #3 needs every step executed and the artifacts uploaded.

### Spec coverage

| Spec item                  | Tasks     |
| -------------------------- | --------- |
| S-1                        | 22        |
| S-2                        | 20, 21    |
| S-3                        | 18        |
| S-4                        | 19        |
| S-5                        | 4         |
| S-6                        | 5         |
| S-7, S-8                   | 6         |
| S-9                        | 7         |
| S-10–S-12                  | 2, 10, 12 |
| S-13                       | 8         |
| S-14                       | 11        |
| S-15                       | 3         |
| S-16, S-17                 | 9         |
| S-18                       | 13        |
| S-19                       | 14        |
| S-20                       | 15        |
| S-21                       | 16        |
| S-22                       | 17        |
| marker test, issue closing | 21, 23    |

### File ownership and waves

No two tasks in one wave touch the same file.

| Wave | Tasks                | Exclusive files                                                                                                                                                                                                                                                                                                                      |
| ---- | -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 0    | 1                    | `docs/architecture.md`                                                                                                                                                                                                                                                                                                               |
| 1    | 2                    | `proto/nexora/control/v1/control.proto`, `gen/go/nexora/control/v1/*`                                                                                                                                                                                                                                                                |
| 1    | 3                    | `engine/src/server/mod.rs` (handle_packet/Scope only), `engine/tests/server_pipeline.rs`                                                                                                                                                                                                                                             |
| 1    | 4                    | `engine/src/acl.rs`                                                                                                                                                                                                                                                                                                                  |
| 1    | 5                    | `engine/src/authoritative/dispatch.rs`, `engine/src/authoritative/xfr_tests.rs`                                                                                                                                                                                                                                                      |
| 1    | 6                    | `engine/src/control.rs`, `engine/tests/control_renewal.rs`, `engine/tests/control_enroll.rs` (new)                                                                                                                                                                                                                                   |
| 1    | 7                    | `engine/src/recursor/dnssec/anchors.rs`, `.../anchors_tests.rs`, `.../verify.rs`                                                                                                                                                                                                                                                     |
| 1    | 8                    | `engine/src/recursor/rpz/transfer.rs`, `.../transfer_tests.rs`                                                                                                                                                                                                                                                                       |
| 1    | 9                    | `engine/src/runtime.rs`, `engine/src/telemetry/otlp.rs`, `engine/tests/telemetry_export.rs`                                                                                                                                                                                                                                          |
| 1    | 13                   | `mgmt/internal/control/server.go`, `mgmt/internal/control/control_test.go`, `mgmt/migrations/00801_blobs_storage_external.sql`                                                                                                                                                                                                       |
| 1    | 14                   | `mgmt/internal/querylog/opensearch.go`, `.../opensearch_test.go`, `e2e/querylog_paging_test.go` (new)                                                                                                                                                                                                                                |
| 1    | 15                   | `mgmt/internal/secrets/pkcs11.go`, `.../pkcs11_test.go`, `.../export_test.go` (new), `.../secrets.go` (Unseal only)                                                                                                                                                                                                                  |
| 1    | 16                   | `mgmt/internal/zone/import.go`, `mgmt/internal/zonefile/export.go`, `mgmt/internal/api/zonefile.go`, `mgmt/internal/zone/export_test.go` (new), `mgmt/internal/api/zonefile_test.go` (new)                                                                                                                                           |
| 1    | 17                   | `mgmt/internal/zone/service.go`, `.../build.go`, `.../validate.go`, `.../model.go` (RebuildOptions), `mgmt/internal/zone/edit_test.go` (new), `mgmt/internal/zone/export_internal_test.go` (new)                                                                                                                                                                      |
| 1    | 18                   | `e2e/fixtures/authhier/{spec,server,authhier_test}.go`, `e2e/dnssec_test.go`                                                                                                                                                                                                                                                         |
| 1    | 19                   | `e2e/harness/otelcol.go`, `e2e/harness/harness_test.go`                                                                                                                                                                                                                                                                              |
| 1    | 20                   | `scripts/compose-verify.sh` (new), `deploy/compose/*`, `docs/operations.md` (sections "Install with Docker Compose", "Plain PostgreSQL and Compose", "Engines ahead of a restored database"), `deploy/deploytest/compose_test.go`                                                                                                    |
| 2    | 10 (after 2)         | `engine/src/recursor/{iterate,infra,rrcache,mod,memory(new)}.rs`, `engine/src/recursor/dnssec/{nsec_cache,validator}.rs`, `engine/src/snapshot_m3.rs`                                                                                                                                                                                |
| 2    | 11 (after 3)         | `engine/src/server/buffers.rs` (new), `engine/src/server/mod.rs` (Answerer impls, module list), `engine/src/server/stream.rs`, `engine/src/server/doq.rs`, `engine/tests/hot_path_alloc.rs`                                                                                                                                          |
| 2    | 12 (after 2)         | `mgmt/migrations/00800_recursor_cache_max_bytes.sql`, `mgmt/internal/store/resolution.go`, `mgmt/internal/api/{resolution.go,gen.go,resolution_test.go}`, `mgmt/api/openapi.yaml`, `mgmt/internal/snapshot/resolution.go`, `web/src/api/schema.d.ts`, `web/src/pages/ResolutionSection.tsx`, `web/e2e/screens/17-resolution.spec.ts` |
| 3    | 21 (after 1–20)      | `deploy/deploytest/debt_test.go` (new), `docs/operations.md` (Known limitations)                                                                                                                                                                                                                                                     |
| 3    | 22 (after M7 pushed) | `.github/workflows/*` only if a run is red                                                                                                                                                                                                                                                                                           |
| 4    | 23                   | none (verification, kw deploy, issue closing)                                                                                                                                                                                                                                                                                        |

## Constraints

Copied from the spec (binding for every task):

- "Hot path rules from `docs/architecture.md` stay binding: no logging, allocation or locks held across packets on the cache-hit path. `cache_hit_path_does_not_allocate`, `authoritative_answer_path_does_not_allocate` and the blocked-reply measurement in `engine/tests/hot_path_alloc.rs` keep passing."
- "Every existing test keeps passing unchanged, including `TestGUICoverage`, the full local e2e suite and `scripts/kw-acceptance.sh`."
- "M7 builds on M6 as committed. When M6 changed a function or file named here, the task adapts to the committed code and updates its plan text."
  - M6 touches `engine/src/acl.rs` (#63 resolver vs authoritative access), `engine/src/control.rs` (#65 engine logs), `mgmt/internal/querylog/opensearch.go` (#54, #56, #61), `mgmt/api/openapi.yaml` and the GUI.
  - Every task's first step re-reads its files at the M6 head.
- "Proto fields added to existing messages use 800–899; migrations `00800_recursor_cache_max_bytes.sql` and `00801_blobs_storage_external.sql`." If M6 took these numbers, use the next free ones and record them in `docs/architecture.md`.
- kw:
  - deploy only after all tests pass, with `scripts/kw-deploy.sh` and its DNS probe;
  - never change 192.168.10.136/139;
  - `helm rollback nexora` on any lost query or failed acceptance.
- novanas only through `scripts/compose-verify.sh`:
  - ports: DNS 15353, HTTP 18080, gRPC 19443, metrics 19153, OTLP 14317;
  - directory `~/nexora-compose-verify`;
  - clean-up with `docker compose down -v` plus removal of the images the run added.
- Secrets are generated at run time, never committed.

Project rules (from `.procoder/notes/implementer-brief.md`):

- Edit on the laptop.
- Build and test in the dev pod with `scripts/dev-exec.sh '<cmd>'`.
- Generated sources (`gen/go`, `mgmt/internal/api/gen.go`, `web/src/api/schema.d.ts`) are generated on the laptop with `make generate`.
- TDD: write the test, run it, see the stated failure, implement, see it pass.
- Rust 1.97.1 with `cargo fmt` and `cargo clippy --all-targets -D warnings`; Go with `gofmt` and `go vet`; the web app with `pnpm lint`.
- Do NOT commit, `git add`, `reset` or `stash`. List every changed path in the final report; the lead commits.
- Update the matching `.procoder/todo/` file with the evidence.

## Task 1: Settle the M7 design in docs/architecture.md

Files: `docs/architecture.md`
Interfaces: produces the names every later task uses:

- `RecursionConfig.cache_max_bytes = 800`
- `server::buffers`
- `recursor::memory`
- `Runtime.labels`
- `telemetry::otlp::MAX_INFLIGHT`
- migration numbers 00800/00801
- `scripts/compose-verify.sh`

- [ ] Run `grep -c "BADVERS\|cache_max_bytes = 800\|server::buffers" docs/architecture.md` and expect `0`.
- [ ] In `### Wire handling`, after the FORMERR bullet, add: `- An OPT record with an EDNS version other than 0 -> extended RCODE BADVERS (header RCODE 0, OPT extended RCODE 1, version 0, no answer), before the authoritative, ACL, filter, cache and resolution stages.`
- [ ] In `### ACL`, append: `The allow list is kept as sorted, merged address ranges per family and looked up by binary search.`
- [ ] In `### Process and threads`, append the bullet: ``- TCP, DoT and DoQ queries take their query, 64 KiB answer and frame buffers from a per-worker pool (`server::buffers`, at most 256 buffers of 65,537 octets). Zone transfers are built on tokio's blocking pool.``
- [ ] In `### Telemetry`, replace `The telemetry thread batches records (max 1000 or 1 s)` with ``The telemetry thread batches records (max 1000 or 1 s) and runs up to 4 exports at once (`MAX_INFLIGHT`)``, and append the bullet: ``- The upstream and policy-group labels of a record come from `Runtime.labels`, the label tables of the 8 newest runtime versions, matched by the record's `config_version`; an older record gets empty labels.``
- [ ] After the `### Snapshot application` section add a `### Recursor memory` section: ``One budget, `RecursionConfig.cache_max_bytes` (0 = 64 MiB, else 4 MiB..16 GiB), split 12/16 RRset cache, 3/16 aggressive NSEC cache, 1/16 infrastructure caches, each weighted by estimated bytes (`recursor::memory`). A snapshot resizes the caches in place without clearing them.``
- [ ] In `## Contract`, append: ``- M7: fields added to existing messages use 800-899: `RecursionConfig.cache_max_bytes` (800).``
- [ ] In `## Management plane`, append these bullets:
  - ``M7: `GetBlob` streams 1 MiB `substring` reads; `blobs.data` storage is `EXTERNAL` (migration 00801). `resolution_settings.recursor_cache_max_bytes` (migration 00800).``
  - ``The OpenSearch adapter sorts by `@timestamp` then `_id`; its cursor is `[timestamp, _id]`.``
  - `Zone export streams rows (apex first, then owners byte-wise); record edits validate only the edited owners and, for unsigned primary zones, write the journal delta from the edited RRsets.`
- [ ] In `### Distribution`, after the compose bullet, add: ``- `scripts/compose-verify.sh <user@host>` runs the documented Compose install, backup and restore on a Docker host.``
- [ ] Run `grep -c "BADVERS" docs/architecture.md; grep -c "cache_max_bytes. (800)" docs/architecture.md; grep -c "server::buffers" docs/architecture.md` and expect three non-zero counts.

## Task 2: Contract field RecursionConfig.cache_max_bytes

Files: `proto/nexora/control/v1/control.proto`, `gen/go/nexora/control/v1/control.pb.go` (regenerated), `engine/src/snapshot_m3.rs` and `engine/src/recursor/dispatch_tests.rs` (test struct literals gain `cache_max_bytes: 0`)
Interfaces: `RecursionConfig.cache_max_bytes` (uint64, field 800). Rust: `proto::RecursionConfig { cache_max_bytes: u64, .. }`. Go: `controlv1.RecursionConfig.CacheMaxBytes uint64`.

- [ ] Run `rg -n "cache_max_bytes" proto/nexora/control/v1/control.proto` and expect no match.
- [ ] In `message RecursionConfig`, after `authority_port = 6;`, add:
  ```proto
    // M7: recursor cache memory in bytes; 0 -> 67108864 (64 MiB); otherwise 4194304..=17179869184.
    uint64 cache_max_bytes = 800;
  ```
- [ ] Regenerate the Go code with the `protoc` command of `make proto` (the repository has no `make generate`; the other two `make proto` steps regenerate the OpenAPI clients and are Task 12's). Run it in the dev pod, whose protoc 3.21.12 and protoc-gen-go v1.36.12 produced the committed files (the laptop's newer protoc would rewrite the version headers), and copy `control.pb.go`/`control_grpc.pb.go` back to the laptop. Expect `gen/go/nexora/control/v1/control.pb.go` to contain `CacheMaxBytes`.
- [ ] Run `scripts/dev-exec.sh 'cargo build --locked -p nexora-engine && go build ./... && cargo test --locked -p nexora-engine --test proto_roundtrip'` and expect success. The Rust struct gains the field through `build.rs`. Existing struct literals use `..Default::default()`; fix any that do not by adding `cache_max_bytes: 0`.

## Task 3: BADVERS for EDNS versions other than 0 (#21)

Files: `engine/src/server/mod.rs` (the `handle_packet` OPT handling and `Scope::finish`), `engine/tests/server_pipeline.rs`
Interfaces: `pub const RCODE_BADVERS: u8 = 16;` in `engine/src/server/mod.rs`; `Scope::finish_rcode(&self, r: QueryRecord, reply: &[u8], rcode: u8)`.

- [ ] Add to `engine/tests/server_pipeline.rs` (after `malformed_and_notimp`):
  ```rust
  #[test]
  fn edns_version_above_zero_gets_badvers() {
      let count = Arc::new(AtomicUsize::new(0));
      let (srv, shared) = start_engine(fake_upstream(count.clone(), 1), "127.0.0.0/8", None);
      assert_eq!(
          ask(srv, "v0.example.", Some(1232)).metadata.response_code,
          ResponseCode::NoError,
          "EDNS version 0 is answered normally"
      );
      let upstream_before = count.load(Ordering::SeqCst);
      for version in [1u8, 255] {
          let c = UdpSocket::bind("127.0.0.1:0").unwrap();
          c.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
          let mut m = Message::new(rand_id(), MessageType::Query, OpCode::Query);
          m.metadata.recursion_desired = true;
          m.add_query(Query::query(Name::from_ascii("v1.example.").unwrap(), RecordType::A));
          let mut e = Edns::new();
          e.set_max_payload(1232);
          e.set_version(version);
          m.set_edns(e);
          c.send_to(&m.to_bytes().unwrap(), srv).unwrap();
          let mut buf = [0u8; 4096];
          let (n, _) = c.recv_from(&mut buf).unwrap();
          assert_eq!(buf[3] & 0x0f, 0, "version {version}: header RCODE carries the low bits of 16");
          let r = Message::from_bytes(&buf[..n]).unwrap();
          // hickory decodes 16 as BADSIG; the numeric value is what matters.
          assert_eq!(u16::from(r.metadata.response_code), 16, "version {version}: BADVERS");
          assert_eq!(r.edns.as_ref().expect("OPT in a BADVERS reply").version(), 0);
          assert!(r.answers.is_empty());
      }
      std::thread::sleep(Duration::from_millis(100));
      assert_eq!(count.load(Ordering::SeqCst), upstream_before, "BADVERS never reaches the upstream");
      let body = shared.metrics.render(&shared.runtime.load(), &shared.recursor);
      assert!(body.contains("rcode=\"other\""), "rcode 16 is counted as other: {body}");
  }
  ```
  Use the `Edns` setter names of hickory-proto 0.26.3 (`set_version` is at `src/op/edns.rs:111`). If `Message::set_edns` is named differently, use what `ask` uses.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test server_pipeline edns_version_above_zero_gets_badvers'`. Expect FAIL at `assert_eq!(u16::from(..), 16)` with left `0`: today the query is forwarded and answered with version 0.
- [ ] In `engine/src/server/mod.rs`, split `Scope::finish` so the rcode can be given:
  ```rust
  fn finish(&self, r: QueryRecord, reply: &[u8]) {
      let rcode = reply.get(3).map_or(wire::RCODE_SERVFAIL, |b| b & 0x0f);
      self.finish_rcode(r, reply, rcode);
  }

  fn finish_rcode(&self, mut r: QueryRecord, _reply: &[u8], rcode: u8) {
      r.rcode = rcode;
      // the remaining body of the former `finish` (duration, unix_micros, counters, querylog push)
  }
  ```
- [ ] In `handle_packet`, directly after `let mut opt = q.opt.map(|o| ReplyOpt { .. });` and before the `bad_cookie_len` check, replace the `// debt: EDNS versions other than 0 ...` comment with:
  ```rust
  // RFC 6891 §6.1.3: only EDNS version 0 is implemented.
  if let Some(o) = q.opt.filter(|o| o.version != 0) {
      let badvers = ReplyOpt {
          udp_size: ADVERTISED_UDP_SIZE,
          do_bit: o.do_bit,
          ext_rcode: RCODE_BADVERS >> 4,
          cookie: None,
          ede: None,
      };
      let n = wire::write_rcode_reply(&q, RCODE_BADVERS & 0x0f, &mut out[..limit], Some(&badvers));
      if n < HEADER_LEN {
          return FastOutcome::Drop;
      }
      scope.finish_rcode(rec, &out[..n], RCODE_BADVERS);
      return FastOutcome::Reply(n);
  }
  ```
  Add `pub const RCODE_BADVERS: u8 = 16;` near the other server constants. `RCODE_BADVERS & 0x0f` is 0 and `>> 4` is 1 (already `u8`, so no cast: clippy::unnecessary_cast). The query-log exporter already names rcode 16 `RCODE16`, and the metric slot is `other`.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test server_pipeline && cargo test --locked -p nexora-engine --test hot_path_alloc && cargo test --locked -p nexora-engine --lib edns'` and expect all to pass.
- [ ] Run `scripts/dev-exec.sh 'cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'` and expect no warnings. Run `rg -n "EDNS versions other than 0" engine/src` and expect no match.

## Task 4: ACL as merged ranges with binary search (#11)

Files: `engine/src/acl.rs`
Interfaces: `Acl::parse(&[String]) -> Result<Acl, String>` and `Acl::allows(IpAddr) -> bool` unchanged; new `Acl::v4_ranges(&self) -> usize`, `Acl::v6_ranges(&self) -> usize`.

- [x] Re-read `engine/src/acl.rs` at the M6 head. If M6 #63 split the ACL into resolver and authoritative lists, apply this task to the type that holds CIDRs and keep M6's API. (Built on the m7 branch, where `acl.rs` is still the single `Acl` with `parse`/`allows`; M6 had not changed `acl.rs` when this was built, so only the internals changed and the public API stays as M6 uses it.)
- [x] Add the counters with the current representation, `pub fn v4_ranges(&self) -> usize { self.v4.len() }` and the same for v6, then add this test module at the end of `acl.rs`:
  ```rust
  #[cfg(test)]
  mod tests {
      use super::*;

      fn next(seed: &mut u64) -> u64 {
          *seed = seed.wrapping_mul(6364136223846793005).wrapping_add(1442695040888963407);
          *seed >> 11
      }

      #[test]
      fn overlapping_cidrs_merge_into_ranges() {
          let acl = Acl::parse(&[
              "10.0.0.0/8".into(), "10.1.0.0/16".into(), "11.0.0.0/8".into(),
              "192.168.1.7/24".into(), "fc00::/7".into(), "fd00::/8".into(),
          ])
          .unwrap();
          assert_eq!(acl.v4_ranges(), 2, "10/8, 10.1/16 and the adjacent 11/8 merge");
          assert_eq!(acl.v6_ranges(), 1);
          assert!(acl.allows("11.255.255.255".parse().unwrap()));
          assert!(acl.allows("192.168.1.200".parse().unwrap()), "host bits are truncated");
          assert!(!acl.allows("12.0.0.0".parse().unwrap()));
          assert!(acl.allows("::ffff:10.9.9.9".parse().unwrap()), "v4-mapped uses the v4 table");
          let all = Acl::parse(&["0.0.0.0/0".into(), "::/0".into()]).unwrap();
          assert!(all.allows("255.255.255.255".parse().unwrap()) && all.allows("ffff::1".parse().unwrap()));
          assert!(!Acl::parse(&[]).unwrap().allows("127.0.0.1".parse().unwrap()));
      }

      #[test]
      fn range_search_matches_linear_scan() {
          let mut seed = 7u64;
          let mut cidrs = Vec::new();
          for _ in 0..1000 {
              let a = std::net::Ipv4Addr::from(next(&mut seed) as u32);
              cidrs.push(format!("{a}/{}", 8 + next(&mut seed) % 25));
              let b = std::net::Ipv6Addr::from(((next(&mut seed) as u128) << 64) | next(&mut seed) as u128);
              cidrs.push(format!("{b}/{}", 16 + next(&mut seed) % 97));
          }
          let acl = Acl::parse(&cidrs).unwrap();
          let nets: Vec<IpNet> = cidrs.iter().map(|c| c.parse::<IpNet>().unwrap().trunc()).collect();
          for i in 0..10_000u64 {
              let ip: IpAddr = if i % 2 == 0 {
                  std::net::Ipv4Addr::from(next(&mut seed) as u32).into()
              } else {
                  std::net::Ipv6Addr::from(((next(&mut seed) as u128) << 64) | next(&mut seed) as u128).into()
              };
              // Pick addresses inside a listed network half the time, so both answers are exercised.
              let ip = if i % 4 < 2 { nets[(i as usize) % nets.len()].network() } else { ip };
              let linear = nets.iter().any(|n| n.contains(&ip));
              assert_eq!(acl.allows(ip), linear, "{ip}");
          }
          assert!(acl.v4_ranges() < 1000, "overlapping random v4 CIDRs merge");
      }
  }
  ```
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib acl::tests'`. Expect FAIL in `overlapping_cidrs_merge_into_ranges` with `left: 4, right: 2` (the linear representation keeps every CIDR).
- [x] Replace the struct and `allows` with ranges and remove the `debt:` doc lines:
  ```rust
  pub struct Acl {
      /// Sorted, non-overlapping, non-adjacent inclusive ranges.
      v4: Vec<(u32, u32)>,
      v6: Vec<(u128, u128)>,
  }

  fn merged<T: Copy + Ord>(mut r: Vec<(T, T)>, succ: impl Fn(T) -> Option<T>) -> Vec<(T, T)> {
      r.sort_unstable();
      let mut out: Vec<(T, T)> = Vec::with_capacity(r.len());
      for (s, e) in r {
          match out.last_mut() {
              Some(last) if succ(last.1).is_none_or(|n| s <= n) => last.1 = last.1.max(e),
              _ => out.push((s, e)),
          }
      }
      out
  }

  fn covered<T: Copy + Ord>(r: &[(T, T)], a: T) -> bool {
      let i = r.partition_point(|&(s, _)| s <= a);
      i > 0 && a <= r[i - 1].1
  }
  ```
  In `parse`, collect `(u32::from(n.network()), u32::from(n.broadcast()))` per `Ipv4Net` (after `trunc()`), and `u128` pairs per `Ipv6Net`. Then `v4: merged(v4, |x| x.checked_add(1))` and the same for v6. `allows` becomes `covered(&self.v4, u32::from(a))`, and `u128::from(a)` for v6, keeping the v4-mapped branch. `v4_ranges`/`v6_ranges` return the merged lengths. Document on `allows`: `Binary search over merged ranges: O(log n), allocation-free.`
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib acl::tests && cargo test --locked -p nexora-engine --test snapshot_apply && cargo test --locked -p nexora-engine --test hot_path_alloc'` and expect every test to pass.

## Task 5: Zone transfers off the worker thread (#12)

Files: `engine/src/authoritative/dispatch.rs`, `engine/src/authoritative/xfr_tests.rs`
Interfaces: `run_slow(ctx: Rc<WorkerCtx>, rt: Arc<Runtime>, job: SlowJob) -> Vec<Vec<u8>>` unchanged; new private `fn build_transfer(rt: &Runtime, shared: &Shared, job: &SlowJob) -> Vec<Vec<u8>>`.

- [x] Add inside `mod authorization` in `xfr_tests.rs`:
  ```rust
  /// The blocking pool has one thread, taken by a job only the worker's other task releases: the
  /// transfer completes only if the worker ran that task while the transfer was pending.
  #[test]
  fn transfer_is_built_off_the_worker() {
      use crate::edns::Transport;
      use crate::server::{FastOutcome, Shared, WorkerCtx, handle_packet};
      use std::cell::Cell;
      use std::rc::Rc;

      let shared = Shared::new(1);
      let mut z = Zone::from_image(&nzf::parse(BIG).unwrap()).unwrap();
      z.transfer_allow = vec!["127.0.0.1/32".parse().unwrap()];
      let mut rt = Runtime::initial();
      rt.auth = Arc::new(AuthSet::from_zones(vec![Arc::new(z)]).unwrap());
      shared.runtime.store(Arc::new(rt));
      let ctx = Rc::new(WorkerCtx::new(0, shared.clone()));
      let rt = shared.runtime.load_full();
      let client = "127.0.0.1:5353".parse().unwrap();
      let axfr = query("big.test.", RecordType::AXFR, None);
      let worker = tokio::runtime::Builder::new_current_thread()
          .max_blocking_threads(1)
          .build()
          .unwrap();
      let local = tokio::task::LocalSet::new();
      worker.block_on(local.run_until(async {
          for round in 0..20 {
              let mut out = vec![0u8; 65535];
              let FastOutcome::Slow(job) =
                  handle_packet(&ctx, &rt, &axfr, client, Transport::Tcp, &mut out)
              else {
                  panic!("AXFR over TCP not handed to the slow path")
              };
              let (release, released) = std::sync::mpsc::channel::<()>();
              let blocker = tokio::task::spawn_blocking(move || {
                  let _ = released.recv();
              });
              let ran = Rc::new(Cell::new(false));
              let flag = ran.clone();
              let other = tokio::task::spawn_local(async move {
                  flag.set(true);
                  let _ = release.send(());
              });
              let msgs =
                  crate::authoritative::dispatch::run_slow(ctx.clone(), rt.clone(), job).await;
              assert!(
                  msgs.len() > 1,
                  "the 2,002-record zone spans several messages"
              );
              assert!(
                  ran.get(),
                  "round {round}: nothing else ran on the worker while the transfer was built"
              );
              other.await.unwrap();
              blocker.await.unwrap();
          }
      }));
  }
  ```
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib transfer_is_built_off_the_worker'`. Expect FAIL with `round 0: nothing else ran on the worker`: today `run_slow` never yields for transfers. (The test caps the blocking pool at one thread and fills it with a job only the worker's other task releases; a first version without that blocker was flaky, because the pool could finish the build before the worker first polled the join handle.)
- [x] In `dispatch.rs`, move the body of the `SlowKind::Transfer` arm (the `Question::parse` FORMERR reply, `authorize_and_plan`, `xfr::count`, `xfr::messages`) into `fn build_transfer(rt: &Runtime, shared: &Shared, job: &SlowJob) -> Vec<Vec<u8>>`. Use `shared.auth.keyring` and `shared.metrics.auth`, and delete the `debt:` comment. The arm becomes:
  ```rust
  SlowKind::Transfer => {
      // Large zones take long to encode; the blocking pool keeps the worker answering queries.
      let shared = ctx.shared.clone();
      tokio::task::spawn_blocking(move || build_transfer(&rt, &shared, &job))
          .await
          .unwrap_or_default()
  }
  ```
  If `WorkerCtx::shared` is not an `Arc<Shared>`, clone the `Arc`s the function needs (`auth`, `metrics`) instead.
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib authoritative:: && cargo test --locked -p nexora-engine --test authoritative_pipeline'` and expect every test to pass, including `signed_queries_and_transfers_through_the_pipeline`.

## Task 6: Renewal fallback and unstorable identities (#13, #14)

Files: `engine/src/control.rs`, `engine/tests/control_renewal.rs`, `engine/tests/control_enroll.rs` (new)
Interfaces:

- `control::run(shared, boot, cert_store)` is unchanged.
- The private `session(...)` gains a `discard_staged: Option<&Identity>` parameter (and `#[allow(clippy::too_many_arguments)]`, now 8 arguments). After the stream connects, it discards `identity.new` when that directory still holds exactly this certificate.
- `obtain_identity` keeps an enrolled but unsaved `Identity` in memory.

- [x] Re-read `engine/src/control.rs` at the M6 head (M6 #65 adds engine log streaming to `session`) and keep M6's additions.
- [x] Add to `engine/tests/control_renewal.rs` a second fake and a test (reuse `Ca`, `p256`, `set_validity`, `serial_of`, `next`, `Event`):
  ```rust
  /// Refuses the staged certificate with a non-authentication status and accepts every other one.
  struct StagedUnavailableMgmt {
      staged_serial: String,
      conns: AtomicUsize,
      events: mpsc::UnboundedSender<Event>,
  }

  #[tonic::async_trait]
  impl EngineControl for StagedUnavailableMgmt {
      async fn enroll(&self, _: Request<EnrollRequest>) -> Result<Response<EnrollResponse>, Status> {
          Err(Status::unimplemented("enroll"))
      }
      type ConnectStream = ReceiverStream<Result<ServerMessage, Status>>;
      async fn connect(&self, req: Request<Streaming<EngineMessage>>) -> Result<Response<Self::ConnectStream>, Status> {
          let certs = req.peer_certs().ok_or_else(|| Status::unauthenticated("client certificate required"))?;
          let (_, leaf) = x509_parser::parse_x509_certificate(&certs[0]).unwrap();
          let serial = leaf.serial.to_str_radix(16);
          let n = self.conns.fetch_add(1, Ordering::SeqCst);
          let _ = self.events.send(Event::Connect { n, serial: serial.clone() });
          if serial == self.staged_serial {
              return Err(Status::unavailable("certificate not usable yet"));
          }
          let (tx, rx) = mpsc::channel(8);
          let mut inbound = req.into_inner();
          tokio::spawn(async move {
              while let Ok(Some(_)) = inbound.message().await {}
              drop(tx);
          });
          Ok(Response::new(ReceiverStream::new(rx)))
      }
      type GetBlobStream = ReceiverStream<Result<BlobChunk, Status>>;
      async fn get_blob(&self, _: Request<GetBlobRequest>) -> Result<Response<Self::GetBlobStream>, Status> {
          Err(Status::not_found("no blobs"))
      }
  }

  #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
  async fn staged_certificate_failing_otherwise_falls_back_and_is_discarded() {
      let _ = rustls::crypto::ring::default_provider().install_default();
      let ca = Ca::new();
      let now = SystemTime::now();
      let server_key = p256();
      let mut sp = rcgen::CertificateParams::new(vec!["127.0.0.1".to_string()]).unwrap();
      sp.extended_key_usages = vec![rcgen::ExtendedKeyUsagePurpose::ServerAuth];
      let server_cert = ca.issue(sp, &server_key);
      // Both certificates are young (renewal not due), so no certificate request interferes.
      let engine_cert = |key: &rcgen::KeyPair| {
          let mut p = rcgen::CertificateParams::default();
          p.distinguished_name.push(rcgen::DnType::CommonName, ENGINE_ID);
          p.extended_key_usages = vec![rcgen::ExtendedKeyUsagePurpose::ClientAuth];
          set_validity(&mut p, now - Duration::from_secs(60), now + Duration::from_secs(3600));
          ca.issue(p, key)
      };
      let (current_key, staged_key) = (p256(), p256());
      let (current, staged) = (engine_cert(&current_key), engine_cert(&staged_key));
      let state = tempfile::tempdir().unwrap();
      save_identity(state.path(), &Identity {
          engine_id: ENGINE_ID.into(),
          cert_pem: current.pem(),
          key_pem: current_key.serialize_pem(),
          ca_pem: ca.cert.pem(),
      })
      .unwrap();
      nexora_engine::cert_renewal::stage_identity(state.path(), staged.pem().as_bytes(), staged_key.serialize_pem().as_bytes()).unwrap();

      let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
      let addr = listener.local_addr().unwrap();
      let (events_tx, mut events) = mpsc::unbounded_channel();
      let fake = StagedUnavailableMgmt { staged_serial: serial_of(&staged.pem()), conns: AtomicUsize::new(0), events: events_tx };
      let tls = ServerTlsConfig::new()
          .identity(tonic::transport::Identity::from_pem(server_cert.pem(), server_key.serialize_pem()))
          .client_ca_root(Certificate::from_pem(ca.cert.pem()));
      tokio::spawn(Server::builder().tls_config(tls).unwrap()
          .add_service(EngineControlServer::new(fake))
          .serve_with_incoming(TcpListenerStream::new(listener)));
      let boot: Bootstrap = toml::from_str(&format!(
          "node_name = \"fallback-1\"\nstate_dir = {:?}\nmanagement_urls = [\"https://{addr}\"]\n",
          state.path()
      ))
      .unwrap();
      let shared = Shared::new(1);
      tokio::spawn(control::run(shared.clone(), boot, Arc::new(CertStore::new())));

      assert_eq!(next(&mut events).await, Event::Connect { n: 0, serial: serial_of(&staged.pem()) });
      assert_eq!(
          next(&mut events).await,
          Event::Connect { n: 1, serial: serial_of(&current.pem()) },
          "after a non-authentication failure the current identity is tried"
      );
      let deadline = tokio::time::Instant::now() + Duration::from_secs(5);
      while nexora_engine::cert_renewal::staged_dir(state.path()).exists() {
          assert!(tokio::time::Instant::now() < deadline, "the staged certificate was not discarded");
          tokio::time::sleep(Duration::from_millis(20)).await;
      }
      assert_eq!(serial_of(&load_identity(state.path()).unwrap().unwrap().cert_pem), serial_of(&current.pem()));
  }
  ```
  `stage_identity(state_dir, cert_pem, key_pem)` is `engine/src/cert_renewal.rs:148`. If it expects the CA or the engine id from `identity/`, it reads them from the saved identity.
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test control_renewal staged_certificate_failing'`. Expect FAIL at the second `assert_eq!`: connection 1 presents the staged serial again.
- [x] Create `engine/tests/control_enroll.rs`. It uses the `Ca` helpers copied from `control_renewal.rs` (a test file cannot import another). An `EnrollingMgmt` counts `enroll` calls and answers each with engine id `11111111-2222-3333-4444-555555555555`, `ca.sign_csr(&csr_der)` and `ca.cert.der()`. Its `connect` returns `Status::unavailable("down")`. The server identity PEM is the server certificate followed by the CA certificate, so `fetch_pinned_ca` finds the pinned CA in the chain. The test:
  ```rust
  #[tokio::test(flavor = "multi_thread", worker_threads = 2)]
  async fn unstorable_identity_is_not_enrolled_again() {
      let _ = rustls::crypto::ring::default_provider().install_default();
      // server, CA, fake as described above; `enrolls: Arc<AtomicUsize>`
      let state = tempfile::tempdir().unwrap();
      // A regular file where the identity directory must be created: saving fails.
      std::fs::write(state.path().join("identity"), b"blocked").unwrap();
      let token = state.path().join("join-token");
      let fp = hex::encode(<sha2::Sha256 as sha2::Digest>::digest(ca.cert.der()));
      std::fs::write(&token, format!("nxj1.MFRGGZDFMZTWQ2LKNNWG23TPOBYXE43U.{fp}\n")).unwrap();
      let boot: Bootstrap = toml::from_str(&format!(
          "node_name = \"enroll-1\"\nstate_dir = {:?}\nmanagement_urls = [\"https://{addr}\"]\njoin_token_file = {:?}\n",
          state.path(), token
      ))
      .unwrap();
      tokio::spawn(control::run(Shared::new(1), boot, Arc::new(CertStore::new())));
      tokio::time::sleep(Duration::from_secs(4)).await;
      assert_eq!(enrolls.load(Ordering::SeqCst), 1, "an unstorable identity is kept, not enrolled again");
      std::fs::remove_file(state.path().join("identity")).unwrap();
      let deadline = tokio::time::Instant::now() + Duration::from_secs(40);
      let stored = loop {
          if let Ok(Some(id)) = load_identity(state.path()) { break id; }
          assert!(tokio::time::Instant::now() < deadline, "identity never stored");
          tokio::time::sleep(Duration::from_millis(100)).await;
      };
      assert_eq!(stored.engine_id, "11111111-2222-3333-4444-555555555555");
      assert_eq!(enrolls.load(Ordering::SeqCst), 1);
  }
  ```
  The 40 s deadline covers the backoff cap of 30 s. If `parse_join_token` rejects the secret, use a base32 string it accepts (read the function).
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test control_enroll'`. Expect FAIL with `left: 3, right: 1` (enrolls at 0 s, about 0.5 s and about 1.5 s).
- [x] In `obtain_identity`, hold `let mut unsaved: Option<Identity> = None;` outside the loop. At the top of each iteration, after `recover_identity`/`load_identity`:
  ```rust
  if let Some(id) = unsaved.take() {
      match save_identity(&boot.state_dir, &id) {
          Ok(()) => {
              eprintln!("nexora-engine: stored identity {}", id.engine_id);
              return id;
          }
          Err(e) => {
              eprintln!("nexora-engine: store identity (retrying, not re-enrolling): {e}");
              unsaved = Some(id);
              drop(lock);
              tokio::time::sleep(backoff(attempt)).await;
              attempt = attempt.saturating_add(1);
              continue;
          }
      }
  }
  ```
  In the enroll branch, `Err(e)` from `save_identity` sets `unsaved = Some(id)` and breaks out of the URL loop. Delete the `debt:` comment.
- [x] In `run`, add `let mut fallback_from: Option<Identity> = None;` before the loop.
  - Load `staged` only when `fallback_from.is_none()`.
  - Pass `fallback_from.as_ref()` as `discard_staged` to `session`.
  - After the existing `staged_refused` block (unchanged), update the fallback:
  ```rust
  // A staged certificate failing for another reason: the next attempt uses the current identity,
  // which discards the staged one if it reaches the stream. With both failing, they alternate.
  fallback_from = match (&staged, staged_refused) {
      (Some(s), false) => Some(s.clone()),
      _ => None,
  };
  ```
  The `Renewed` branch `continue`s before this point, so it sets `fallback_from = None` itself: a newly staged identity is tried next instead of being skipped for the older failed one (otherwise a fallback session that renews would loop on the current identity).
  In `session`, right after `eprintln!("nexora-engine: control connected to {url}")`, add:
  ```rust
  if let Some(failed) = discard_staged {
      let lock = identity_lock(boot).await;
      if let Ok(Some(on_disk)) = load_identity_dir(&cert_renewal::staged_dir(&boot.state_dir))
          && on_disk.cert_pem == failed.cert_pem
          && let Err(e) = cert_renewal::discard_staged(&boot.state_dir)
      {
          eprintln!("nexora-engine: discard renewed identity: {e}");
      }
      drop(lock);
  }
  ```
  Delete the `debt:` comment on the renewal branch.
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test control_renewal && cargo test --locked -p nexora-engine --test control_enroll && cargo test --locked -p nexora-engine --test control_unit'`. Expect all to pass, `renews_rotates_and_backs_off_when_revoked` included.

## Task 7: RFC 5011 revocation of the only trusted key (#15)

Files: `engine/src/recursor/dnssec/anchors.rs`, `engine/src/recursor/dnssec/anchors_tests.rs`, `engine/src/recursor/dnssec/verify.rs`; for the alert step also `engine/src/telemetry/metrics.rs` (one gauge and its test) and `web/src/pages/DnssecPage.tsx` (one alert component)
Interfaces: `pub fn revoked_key_verifies(rrset: &[Record], rrsigs: &[Record], key: &DNSKEY, zone: &Name, now_unix: u64) -> Option<VerifiedSig>` in `verify.rs`; `revoked_key_signs` is re-expressed through it. `pub fn lost_trust_points(&self) -> Vec<Name>` on `TrustAnchorStore`.

- [x] Add to `anchors_tests.rs`:
  ```rust
  #[tokio::test(flavor = "current_thread")]
  async fn refresh_accepts_revocation_of_the_only_trusted_key() {
      let mut only = TestKey::generate(".", true);
      let newcomer = TestKey::generate(".", true);
      let (_d, s) = store_with(&only);
      let metrics = RecursorMetrics::default();
      let root = Name::root();
      let tag = only.dnskey.calculate_key_tag().unwrap();
      let f = Dnskeys(RefCell::new(Some(signed_rrset(&[&only], &[&only], T0))));
      refresh_zone(&s, &root, &f, &metrics, T0).await;
      assert_eq!(state_of(&s, tag), Some(KeyState::Valid));

      // The only trusted key revokes itself; a new key appears, signed by nothing trusted.
      only.set_revoked();
      let now = T0 + DAY;
      *f.0.borrow_mut() = Some(signed_rrset(&[&only, &newcomer], &[&only], now));
      refresh_zone(&s, &root, &f, &metrics, now).await;
      assert_eq!(state_of(&s, tag), Some(KeyState::Revoked), "{:?}", s.status());
      assert_eq!(s.trust_points().zones.len(), 0, "no trusted key is left for the root");
      assert_eq!(
          state_of(&s, newcomer.dnskey.calculate_key_tag().unwrap()),
          None,
          "a key vouched for only by a revoked key is not added"
      );
      assert_eq!(metrics.trust_anchor_refresh_failures.load(std::sync::atomic::Ordering::Relaxed), 0);
  }
  ```
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib refresh_accepts_revocation_of_the_only_trusted_key'`. Expect FAIL `left: Some(Valid), right: Some(Revoked)`: the RRset is refused as "not validated by a trusted key".
- [x] In `verify.rs`, add `revoked_key_verifies`, which returns `key.revoke().then(|| verify_inner(rrset, rrsigs, std::slice::from_ref(key), zone, now_unix, true).ok()).flatten()`. Make `revoked_key_signs` return `revoked_key_verifies(..).is_some()`.
- [x] In `refresh_zone`, replace the `let Ok(v) = verify_rrset(...) else { return fail(...) };` statement with a match whose `Err` arm handles a self-revocation by a trusted key:
  ```rust
  let v = match verify_rrset(&records, &sigs, &authorised, zone, now_unix) {
      Ok(v) => v,
      Err(_) => {
          // RFC 5011 §2.1: a trusted key may revoke itself even when no other trusted key signs
          // the set. Only the revocation is recorded; nothing else in the RRset is trusted.
          let revocations: Vec<(DNSKEY, VerifiedSig)> = keys
              .iter()
              .filter(|k| k.revoke() && k.secure_entry_point())
              .filter_map(|k| {
                  let cleared = DNSKEY::with_flags(k.flags() & !0x0080, k.public_key().clone());
                  let trusted = tp.keys.contains(&cleared)
                      || matches!(match_ds(zone, std::slice::from_ref(&cleared), &tp.ds), DsMatch::Matched(_));
                  if !trusted {
                      return None;
                  }
                  revoked_key_verifies(&records, &sigs, k, zone, now_unix).map(|v| (cleared, v))
              })
              .collect();
          let Some(&(_, v)) = revocations.first() else {
              return fail("DNSKEY RRset not validated by a trusted key");
          };
          let observed: Vec<DNSKEY> = revocations.iter().map(|(k, _)| k.clone()).collect();
          let revoked: Vec<u16> = observed.iter().filter_map(|k| k.calculate_key_tag().ok()).collect();
          let obs = Observation {
              dnskeys: &observed,
              validated_by_trusted: true,
              revoked_self_signed: &revoked,
              orig_ttl: v.original_ttl,
              sig_expiration: i64::from(v.expiration),
          };
          if let Err(e) = store.record_observation(zone, &obs, now) {
              fail(&format!("persisting trust anchor state: {e}"));
          }
          return;
      }
  };
  ```
  Delete the `debt:` paragraph from the doc comment and add: `A trusted key's self-signed revocation is accepted even when it is the zone's only trusted key; the zone then has no trust point (RFC 5011 §5).`
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib recursor::dnssec'` and expect all to pass, including `refresh_authenticates_rollover_and_self_signed_revocation`.
- [x] Lost trust point alert (spec [S-9] edge case, lead decision 2026-09-14). Extend the test above with `assert_eq!(s.lost_trust_points(), vec![root.clone()]);`, and at its end re-merge the configuration with the original DS (captured as `only_ds` before `set_revoked`) plus the newcomer's DS and assert the old key stays `Revoked` and `s.lost_trust_points().is_empty()`. Expect FAIL (no such method). Then:
  - Add `pub fn lost_trust_points(&self) -> Vec<Name>` to the trust anchor store in `anchors.rs`. It returns the zones that hold a `Revoked` key and have no trust point now, derived from the persisted state (no extra state). Adding a trust anchor for that zone (or removing the zone) clears the entry.
  - Export it in `engine/src/telemetry/metrics.rs` as the gauge `nexora_dnssec_trust_point_lost{zone}` = 1 (no series when no zone is lost), with the test `lost_trust_point_raises_its_gauge`. No proto field is needed: `DnssecStats.trust_anchors` already carries every key's state per zone, and the same condition (a `revoked` key and no `configured`/`valid`/`missing` key) is derived from it in the GUI.
  - `web/src/pages/DnssecPage.tsx` shows a destructive `Alert` with `data-testid="dnssec-trust-point-lost"` naming each lost zone and the engines reporting it.
  - Rerun `cargo test --locked -p nexora-engine --lib anchors_tests` and `... --lib telemetry::metrics` and expect PASS.
## Task 8: RPZ IXFR keys only for deleted owners (#19)

Files: `engine/src/recursor/rpz/transfer.rs`, `engine/src/recursor/rpz/transfer_tests.rs`
Interfaces: `apply_ixfr(&ZoneData, &[Record]) -> Result<ZoneData, String>` unchanged; `#[cfg(test)] thread_local! { pub(super) static KEYS_BUILT: Cell<usize> }` in `transfer.rs`.

- [x] In `transfer.rs`, add the test counter and count every `record_key` call:
  ```rust
  #[cfg(test)]
  thread_local! {
      pub(super) static KEYS_BUILT: std::cell::Cell<usize> = const { std::cell::Cell::new(0) };
  }
  ```
  and `#[cfg(test)] KEYS_BUILT.with(|c| c.set(c.get() + 1));` as the first statement of `record_key`.
- [x] Add to `transfer_tests.rs`:
  ```rust
  #[test]
  fn ixfr_builds_keys_only_for_records_at_deleted_owners() {
      let mut records = vec![soa(1)];
      records.extend((0..100_000).map(|i| block(&format!("n{i}"))));
      let cur = ZoneData { serial: 1, records };
      let answers = vec![soa(2), soa(1), block("N500"), soa(2), block("added"), soa(2)];
      KEYS_BUILT.with(|c| c.set(0));
      let next = apply_ixfr(&cur, &answers).unwrap();
      assert!(!next.records.contains(&block("n500")), "the deletion matches case-insensitively");
      assert!(next.records.contains(&block("added")));
      assert_eq!(next.records.len(), 100_001);
      let built = KEYS_BUILT.with(|c| c.get());
      assert!(built <= 2, "built {built} record keys for a one-record deletion");
  }
  ```
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib ixfr_builds_keys_only'`. Expect FAIL `built 100001 record keys for a one-record deletion`.
- [x] In `apply_ixfr`, build the owner set next to `deletions`, then filter by it before building a key:
  ```rust
  // hickory's Name hashes and compares case-insensitively.
  let deleted_owners: FxHashSet<&Name> = body[del_start..i].iter().map(|r| &r.name).collect();
  ```
  ```rust
  records.retain(|r| {
      r.record_type() != RecordType::SOA
          && !(deleted_owners.contains(&r.name) && deletions.contains(&record_key(r)))
  });
  ```
  Delete the `debt:` comment.
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib recursor::rpz && cargo test --locked -p nexora-engine --test rpz_pipeline'` and expect all to pass.

## Task 9: Query-log labels by config version and concurrent exports (#23, #24)

Files: `engine/src/runtime.rs`, `engine/src/telemetry/otlp.rs`, `engine/tests/telemetry_export.rs`
Interfaces:

- `runtime::LABEL_HISTORY: usize = 8`
- `pub struct RecordLabels { pub version: u64, pub upstreams: Vec<String>, pub groups: Vec<String> }`
- `Runtime.labels: Arc<[Arc<RecordLabels>]>`, newest first
- `telemetry::otlp::MAX_INFLIGHT: usize = 4`

- [x] Extend `Sink` in `telemetry_export.rs` with `upstreams: Arc<parking_lot::Mutex<Vec<String>>>`, `delay: Duration`, `active: Arc<AtomicUsize>` and `peak: Arc<AtomicUsize>`. In `LogsService::export`:
  - increment `active` and raise `peak` to it;
  - `tokio::time::sleep(self.delay).await`;
  - push every `nexora.upstream` attribute string into `upstreams`;
  - decrement `active`.
  - Existing tests keep `Sink::default()` (delay zero).
- [x] Add the two tests:
  ```rust
  fn serve(sink: Sink) -> (tokio::runtime::Runtime, std::net::SocketAddr) {
      let rt = tokio::runtime::Runtime::new().unwrap();
      let listener = rt.block_on(tokio::net::TcpListener::bind("127.0.0.1:0")).unwrap();
      let addr = listener.local_addr().unwrap();
      rt.spawn(tonic::transport::Server::builder()
          .add_service(LogsServiceServer::new(sink.clone()))
          .add_service(TraceServiceServer::new(sink))
          .serve_with_incoming(tokio_stream::wrappers::TcpListenerStream::new(listener)));
      (rt, addr)
  }

  #[test]
  fn upstream_label_uses_the_runtime_that_answered() {
      let sink = Sink::default();
      let (_rt, addr) = serve(sink.clone());
      let shared = shared_with_endpoint(&format!("http://{addr}"));
      // Version 2 reorders the upstreams before the version-1 record is drained.
      push(&shared.querylog, &shared.metrics, record(0)); // upstream 0, config_version 1
      apply_reordered(&shared, 2, &["other", "fixture"]);
      let _t = spawn_telemetry_thread(shared.clone());
      let deadline = Instant::now() + Duration::from_secs(5);
      while sink.upstreams.lock().is_empty() && Instant::now() < deadline {
          std::thread::sleep(Duration::from_millis(50));
      }
      assert_eq!(*sink.upstreams.lock(), vec!["fixture".to_string()]);
  }
  ```
  Implement `apply_reordered(shared, version, names)` as a helper next to `shared_with_endpoint`: it applies a `ConfigSnapshot` equal to the one in `shared_with_endpoint` with `version` and one `Upstream` per name (ids `u-<name>`, addresses `127.0.0.1:9`).
  ```rust
  #[test]
  fn slow_collector_does_not_drop_with_concurrent_exports() {
      let sink = Sink { delay: Duration::from_millis(400), ..Sink::default() };
      let (_rt, addr) = serve(sink.clone());
      let shared = shared_with_endpoint(&format!("http://{addr}"));
      let _t = spawn_telemetry_thread(shared.clone());
      for _ in 0..20 {
          for _ in 0..1000 {
              push(&shared.querylog, &shared.metrics, record(0));
          }
          std::thread::sleep(Duration::from_millis(100));
      }
      let deadline = Instant::now() + Duration::from_secs(15);
      while sink.logs.load(Ordering::SeqCst) + shared.metrics.dropped(Signal::Logs) as usize < 20_000
          && Instant::now() < deadline
      {
          std::thread::sleep(Duration::from_millis(50));
      }
      assert_eq!(shared.metrics.dropped(Signal::Logs), 0, "logs dropped behind a 400 ms collector");
      assert_eq!(sink.logs.load(Ordering::SeqCst), 20_000);
      assert!(sink.peak.load(Ordering::SeqCst) > 1, "exports never overlapped");
  }
  ```
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test telemetry_export'`. Expect both new tests to FAIL:
  - `upstream_label_uses_the_runtime_that_answered` with `left: ["other"]`;
  - `slow_collector_does_not_drop_with_concurrent_exports` with a non-zero drop count (one export at a time clears about 2.5 batches/s against 10/s arriving).
- [x] In `runtime.rs`:
  - add `LABEL_HISTORY`, `RecordLabels` and the field `labels`;
  - `Runtime::initial()` gets `labels: Arc::from([])`;
  - in `build_with`, after the upstreams and policy are built:
  ```rust
  let current = Arc::new(RecordLabels {
      version: s.version,
      upstreams: upstreams.specs.iter().map(|u| u.name.clone()).collect(),
      groups: (0..=u16::MAX).map_while(|i| policy.group(i)).map(|g| g.group_id().to_owned()).collect(),
  });
  let labels: Arc<[Arc<RecordLabels>]> = std::iter::once(current)
      .chain(previous.into_iter().flat_map(|p| p.labels.iter().filter(|l| l.version != s.version).cloned()))
      .take(LABEL_HISTORY)
      .collect();
  ```
  The committed `PolicyTable` has no group count, so the groups are enumerated with `group(i)` until it returns `None` (`engine/src/filter.rs` is not this task's file).
- [x] In `otlp.rs`, replace `upstream_name` (and the policy-group lookup in `drain`) with:
  ```rust
  /// The labels of the runtime that answered `r`: empty when that version is no longer kept.
  fn labels<'a>(rt: &'a Runtime, r: &QueryRecord) -> (&'a str, &'a str) {
      rt.labels.iter().find(|l| l.version == r.config_version).map_or(("", ""), |l| {
          (
              l.upstreams.get(usize::from(r.upstream)).map_or("", String::as_str),
              l.groups.get(usize::from(r.policy_group)).map_or("", String::as_str),
          )
      })
  }
  ```
- [x] Still in `otlp.rs`, add `pub const MAX_INFLIGHT: usize = 4;`.
  - Change `export_batch` to return the export future (`impl Future<Output = ()> + 'static`) instead of spawning.
  - In `run`, replace `inflight: Option<JoinHandle<()>>` with `let mut inflight = tokio::task::JoinSet::new();` and the select arm `Some(_) = inflight.join_next(), if !inflight.is_empty() => {}`.
  - After `ex.drain()`:
  ```rust
  // Drain again at once while a full batch waits, and keep up to MAX_INFLIGHT exports running.
  // Bounded, so records that never form a batch (logs off) cannot starve the loop.
  for _ in 0..MAX_QUEUED_BATCHES {
      if ex.shared.querylog.len() < BATCH_MAX || ex.queue.len() >= MAX_QUEUED_BATCHES {
          break;
      }
      ex.drain();
  }
  while inflight.len() < MAX_INFLIGHT && let Some(batch) = ex.queue.pop_front() {
      inflight.spawn(ex.export_batch(batch));
  }
  ```
  Delete both `debt:` comments.
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test telemetry_export && cargo test --locked -p nexora-engine --test snapshot_apply && cargo test --locked -p nexora-engine --test hot_path_alloc'` and expect all to pass.

## Task 10: Recursor memory budget, atomic infra updates, uncapped NSEC cache (#16, #17, #18)

Depends on Task 2.

Files:

- `engine/src/recursor/memory.rs` (new)
- `engine/src/recursor/mod.rs` (module list, `RecursorState::sync`)
- `engine/src/recursor/iterate.rs`
- `engine/src/recursor/infra.rs`
- `engine/src/recursor/rrcache.rs`
- `engine/src/recursor/dnssec/nsec_cache.rs`
- `engine/src/recursor/dnssec/validator.rs` (construction only)
- `engine/src/recursor/testnet.rs` (the `RecursionParams` literal gains `cache_max_bytes: 0`)
- `engine/src/snapshot_m3.rs`

Interfaces:

```rust
// crate::recursor::memory
pub const DEFAULT_CACHE_MAX_BYTES: u64 = 64 << 20;
pub const MIN_CACHE_MAX_BYTES: u64 = 4 << 20;
pub const MAX_CACHE_MAX_BYTES: u64 = 16 << 30;
pub struct Shares { pub rrset: u64, pub nsec: u64, pub infra: u64 }
pub fn shares(cache_max_bytes: u64) -> Shares; // 0 -> default; 12/16, 3/16, 1/16
pub fn record_bytes(r: &hickory_proto::rr::Record) -> u64;
// RrCache::new(capacity_bytes: u64), RrCache::set_capacity(u64), RrCache::weight() -> u64, RrCache::len() -> usize
// InfraCache::new(capacity_bytes: u64), InfraCache::set_capacity(u64)
// AggressiveNsecCache::new(capacity_bytes: u64, metrics), AggressiveNsecCache::set_capacity(u64)
```

- [ ] Add accessors on the current `RrCache` (`pub fn weight(&self) -> u64 { self.entries.weight() }`, `pub fn len(&self) -> usize { self.entries.len() }`, `pub fn set_capacity(&self, c: u64) { self.entries.set_capacity(c) }`) and the test module at the end of `rrcache.rs`:
  ```rust
  #[cfg(test)]
  mod tests {
      use super::*;
      use hickory_proto::rr::{RData, rdata::A};

      fn a(owner: &str, i: u32) -> Record {
          Record::from_rdata(Name::from_ascii(owner).unwrap(), 300, RData::A(A::from(std::net::Ipv4Addr::from(i))))
      }

      #[test]
      fn rrset_cache_is_bounded_by_bytes() {
          let cache = RrCache::new(256 << 10);
          for i in 0..20_000u32 {
              cache.insert(vec![a(&format!("n{i}.example."), i)], vec![], Credibility::AnswerAa, DnssecStatus::Unchecked, 1_000);
          }
          assert!(cache.len() < 20_000, "entries are weighed in bytes, not counted: {}", cache.len());
          assert!(cache.weight() <= 256 << 10, "weight {} above 256 KiB", cache.weight());
          cache.set_capacity(64 << 10);
          assert!(cache.weight() <= 64 << 10, "set_capacity evicts at once");
      }
  }
  ```
- [ ] Add at the end of `infra.rs`:
  ```rust
  #[cfg(test)]
  mod tests {
      use super::*;

      #[test]
      fn concurrent_updates_are_not_lost() {
          let cache = std::sync::Arc::new(InfraCache::new(1 << 20));
          let ip: IpAddr = "192.0.2.53".parse().unwrap();
          let threads: Vec<_> = (0..8)
              .map(|_| {
                  let c = cache.clone();
                  std::thread::spawn(move || {
                      for _ in 0..100_000 {
                          c.update(ip, |e| e.rto_ms += 1);
                      }
                  })
              })
              .collect();
          for t in threads {
              t.join().unwrap();
          }
          assert_eq!(cache.entry(ip).unwrap().rto_ms, UNKNOWN_RTO_MS + 800_000);
      }
  }
  ```
- [ ] Add at the end of `nsec_cache.rs`:
  ```rust
  #[cfg(test)]
  mod tests {
      use super::*;
      use hickory_proto::rr::rdata::SOA;

      #[test]
      fn aggressive_nsec_caches_more_than_1024_denials_of_one_zone() {
          let zone = Name::from_ascii("big.test.").unwrap();
          let cache = AggressiveNsecCache::new(64 << 20, Arc::new(RecursorMetrics::default()));
          let soa = vec![Record::from_rdata(zone.clone(), 300, RData::SOA(SOA::new(
              Name::from_ascii("ns.big.test.").unwrap(), Name::from_ascii("h.big.test.").unwrap(), 1, 3600, 600, 86400, 300)))];
          let owner = |i: usize| Name::from_ascii(format!("n{i:05}.big.test.")).unwrap();
          let nsec = |from: Name, to: Name| Record::from_rdata(from, 300,
              RData::DNSSEC(DNSSECRData::NSEC(NSEC::new(to, [RecordType::A, RecordType::RRSIG, RecordType::NSEC]))));
          let mut denials = vec![nsec(zone.clone(), owner(0))];
          denials.extend((0..5000).map(|i| nsec(owner(i), if i == 4999 { zone.clone() } else { owner(i + 1) })));
          cache.insert_secure(&zone, &soa, &denials, 1_000);
          // Between n03999 and n04000: covered by the 4,000th NSEC; *.big.test. by the apex NSEC.
          let q = Name::from_ascii("n03999a.big.test.").unwrap();
          let (rcode, _) = cache.synthesize(&q, RecordType::A, 1_001).expect("NXDOMAIN synthesised");
          assert_eq!(rcode, ResponseCode::NXDomain);
      }
  }
  ```
  Use the `NSEC::new` signature of hickory-proto 0.26.3. `insert_secure` takes records without signature checks, because the validator verifies them before calling it.
- [ ] Add to the existing tests in `snapshot_m3.rs`, starting from the snapshot the module's tests already build (the one with `recursion: Some(RecursionConfig { .. })`):
  ```rust
  #[test]
  fn cache_max_bytes_outside_range_is_rejected() {
      for (bytes, ok) in [(0u64, true), (4 << 20, true), (64 << 20, true), (16 << 30, true), ((4 << 20) - 1, false), ((16 << 30) + 1, false)] {
          let mut s = ok_snapshot(); // the module's existing builder
          s.recursion.as_mut().unwrap().cache_max_bytes = bytes;
          assert_eq!(validate_m3(&s).is_ok(), ok, "cache_max_bytes {bytes}");
      }
  }
  ```
  Use the builder and validation function names of `snapshot_m3.rs`'s own tests.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib -- rrset_cache_is_bounded_by_bytes concurrent_updates_are_not_lost aggressive_nsec_caches cache_max_bytes_outside_range'`. Expect four FAILs:
  - `entries are weighed in bytes, not counted: 20000`;
  - `left: <less than 800376>, right: 800376` (lost updates);
  - `NXDOMAIN synthesised` panics on `None` (only 1,024 NSECs stored);
  - `cache_max_bytes 4194303` accepted.
- [ ] Create `engine/src/recursor/memory.rs`:
  ```rust
  //! The recursor's memory budget (`RecursionConfig.cache_max_bytes`) and its split over the caches.

  use hickory_proto::rr::Record;

  pub const DEFAULT_CACHE_MAX_BYTES: u64 = 64 << 20;
  pub const MIN_CACHE_MAX_BYTES: u64 = 4 << 20;
  pub const MAX_CACHE_MAX_BYTES: u64 = 16 << 30;

  pub struct Shares {
      pub rrset: u64,
      pub nsec: u64,
      pub infra: u64,
  }

  pub fn shares(cache_max_bytes: u64) -> Shares {
      let total = if cache_max_bytes == 0 { DEFAULT_CACHE_MAX_BYTES } else { cache_max_bytes };
      Shares { rrset: total / 16 * 12, nsec: total / 16 * 3, infra: total / 16 }
  }

  /// Estimated heap and inline bytes of one cached record: an estimate for eviction, not an exact count.
  pub fn record_bytes(r: &Record) -> u64 {
      (std::mem::size_of::<Record>() + r.name.len() + 32) as u64
  }
  ```
  Register `pub mod memory;` in `recursor/mod.rs`.
- [ ] `rrcache.rs`:
  - add `#[derive(Clone)] struct RrsetWeight;` implementing `quick_cache::Weighter<(Name, RecordType), Arc<CachedRrset>>`, with `weight = 64 + key.0.len() + Σ record_bytes(records ∪ rrsigs)` (at least 1);
  - `RrCache::new(capacity_bytes: u64)` builds `Cache::with_weighter((capacity_bytes / 512).max(64) as usize, capacity_bytes, RrsetWeight)`.
- [ ] `infra.rs`:
  - `servers` is weighted by a fixed `INFRA_ENTRY_BYTES = 128`;
  - `lame` is weighted by `64 + name.len()`;
  - `new(capacity_bytes)` gives each half;
  - `set_capacity(bytes)` sets both halves.
  - Replace `update` (and delete its `debt:` comment) with an atomic in-place update:
  ```rust
  /// Atomic across workers: the shard lock covers read, change and write; a new server is filled
  /// through its placeholder guard, so concurrent first updates wait for each other.
  fn update(&self, ip: IpAddr, f: impl FnOnce(&mut InfraEntry)) {
      let mut f = Some(f);
      match self.servers.entry(&ip, None, |_, e| {
          (f.take().expect("runs once"))(e);
          quick_cache::sync::EntryAction::Retain(())
      }) {
          quick_cache::sync::EntryResult::Vacant(guard) => {
              let mut e = InfraEntry::unknown();
              if let Some(f) = f.take() {
                  f(&mut e);
              }
              let _ = guard.insert(e);
          }
          _ => {}
      }
  }
  ```
  Adjust the enum paths to where quick_cache 0.7 exports `EntryAction`/`EntryResult` (`quick_cache::sync`).
- [ ] `nsec_cache.rs`:
  - remove `MAX_DENIALS_PER_ZONE` and its `debt:` comment, and `max_zones`;
  - add `capacity: AtomicU64`, `ZoneDenials.bytes: u64` and a cache-wide `bytes: u64` kept inside the mutex (`struct Zones { map: HashMap<Name, ZoneDenials>, bytes: u64 }`);
  - an entry's bytes = `64 + Σ record_bytes(records)`; add them on insert (replacing an entry subtracts the old one), subtract on `retain` of expired entries; a zone also counts `64 + Σ record_bytes(soa)` for its SOA;
  - a zone stops taking new entries once its own bytes would exceed the capacity;
  - after inserting, while `bytes > capacity`, remove the zone with the smallest `inserted` other than the one just written;
  - `set_capacity(bytes)` stores the capacity and evicts the same way.
  - `validator.rs` constructs it with the `nsec` share, replacing `NSEC_CACHE_ZONES`.
- [ ] `iterate.rs`: remove `INFRA_CAPACITY`, `RRSET_CAPACITY` and their `debt:` comment; `Recursor::new` builds `InfraCache::new(memory::shares(0).infra)` and `RrCache::new(memory::shares(0).rrset)`. Add `pub cache_max_bytes: u64` to `RecursionParams`, set from `c.cache_max_bytes` (0 when `c` is `None`).
- [ ] `RecursorState::sync(&runtime)` in `recursor/mod.rs`: read the runtime's `RecursionParams::cache_max_bytes` (the params `recursor/dispatch.rs` builds from the snapshot), then call `set_capacity` with `memory::shares(..)` on `recursor.rrcache`, `recursor.infra` and the validator's aggressive NSEC cache.
- [ ] `snapshot_m3.rs`: in the `recursion` block, add
  ```rust
  if r.cache_max_bytes != 0 && !(MIN_CACHE_MAX_BYTES..=MAX_CACHE_MAX_BYTES).contains(&r.cache_max_bytes) {
      return Err(format!(
          "recursion.cache_max_bytes: {} not 0 or in {MIN_CACHE_MAX_BYTES}..={MAX_CACHE_MAX_BYTES}",
          r.cache_max_bytes
      ));
  }
  ```
  following the file's existing error style.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib recursor:: && cargo test --locked -p nexora-engine --lib snapshot_m3 && cargo test --locked -p nexora-engine --test hot_path_alloc && cargo test --locked -p nexora-engine --test rpz_pipeline'` and expect all to pass. Run `scripts/dev-exec.sh 'cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`.

## Task 11: Per-worker buffer pool for TCP, DoT and DoQ (#20, #22)

Depends on Task 3 (same `engine/src/server/mod.rs`).

Files: `engine/src/server/buffers.rs` (new), `engine/src/server/mod.rs` (module declaration and the two `WorkerAnswerer` methods), `engine/src/server/stream.rs`, `engine/src/server/doq.rs`, `engine/tests/hot_path_alloc.rs`
Interfaces: `server::buffers::{POOL_MAX = 256, BUFFER_CAPACITY = 65_537, take() -> Vec<u8>, give(Vec<u8>)}`.

- [x] In `hot_path_alloc.rs`, add a big-allocation counter next to `ALLOCS`: `static BIG: Cell<usize>`, incremented by the armed allocator when the requested size is at least 60,000 octets (`layout.size()` in `alloc`, the new size in `realloc`).
- [x] Add a helper `stream_setup() -> (Arc<Shared>, Vec<u8> /* query */)`. It applies a snapshot with `acl_allow_cidrs: ["127.0.0.0/8"]`, an 8 MiB cache and an empty filter (no lists), and inserts a cached answer for `hot.example. A` for client `127.0.0.1` exactly as `measure` does (`CacheKey::in_partition(&v, policy.cache_partition())`).
- [x] Add the stream test:
  ```rust
  #[test]
  fn stream_answers_reuse_pooled_buffers() {
      use nexora_engine::edns::Transport;
      use nexora_engine::server::stream::serve_dns_stream;
      use nexora_engine::server::{ClientInfo, WorkerAnswerer, WorkerCtx};
      use tokio::io::{AsyncReadExt, AsyncWriteExt};
      let (shared, query) = stream_setup();
      let rt = tokio::runtime::Builder::new_current_thread().enable_all().build().unwrap();
      tokio::task::LocalSet::new().block_on(&rt, async {
          let (client_io, server_io) = tokio::io::duplex(1 << 20);
          let ctx = std::rc::Rc::new(WorkerCtx::new(0, shared.clone()));
          let client = ClientInfo { addr: "127.0.0.1:5555".parse().unwrap(), transport: Transport::Tcp };
          tokio::task::spawn_local(serve_dns_stream(
              std::rc::Rc::new(WorkerAnswerer(ctx)), server_io, client, std::time::Duration::from_secs(30)));
          let (mut rd, mut wr) = tokio::io::split(client_io);
          let mut frame = (query.len() as u16).to_be_bytes().to_vec();
          frame.extend_from_slice(&query);
          let mut reply = vec![0u8; 4096];
          let mut roundtrip = async |wr: &mut _, rd: &mut _| {
              let (wr, rd): (&mut tokio::io::WriteHalf<_>, &mut tokio::io::ReadHalf<_>) = (wr, rd);
              wr.write_all(&frame).await.unwrap();
              let n = rd.read_u16().await.unwrap() as usize;
              rd.read_exact(&mut reply[..n]).await.unwrap();
          };
          for _ in 0..64 {
              roundtrip(&mut wr, &mut rd).await;
          }
          BIG.with(|c| c.set(0));
          ARMED.with(|a| a.set(true));
          for _ in 0..1000 {
              roundtrip(&mut wr, &mut rd).await;
          }
          ARMED.with(|a| a.set(false));
      });
      assert_eq!(BIG.with(Cell::get), 0, "a stream query allocated a 64 KiB buffer");
  }
  ```
  As built, the round trip is a helper `async fn stream_roundtrip(wr, rd, frame, reply)` (it also asserts one answer) instead of the async closure. The shape that matters: 64 warm-up round trips, then 1,000 measured ones.
- [x] Add `doq_answers_reuse_pooled_buffers` the same way:
  - self-signed `rcgen` certificate installed in a `CertStore`;
  - `nexora_engine::server::doq::bind_doq("127.0.0.1:0", tls::quic_server_config(store), doq::endpoint_config(&[7; 64]))`;
  - `run_doq(server, Rc::new(WorkerAnswerer(ctx)), store)` spawned locally;
  - a quinn client built as in `doq.rs`'s `client_endpoint` test helper;
  - each query opens a bidirectional stream, writes the length-prefixed query with message ID 0 (DoQ requires it; rebuild `query` with ID 0), finishes, and reads to the end into a preallocated reply buffer (`recv.read` loop, helper `doq_roundtrip`);
  - 32 warm-up queries, then 200 measured with `ARMED` set;
  - assert `BIG == 0` with the message `a DoQ query allocated a 64 KiB buffer`.
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test hot_path_alloc -- stream_answers_reuse_pooled_buffers doq_answers_reuse_pooled_buffers'`. Expect FAIL on both assertions, with `left` 1,000 (the answer buffer; the old frame is sized to the answer) and 200 (observed).
- [x] Create `engine/src/server/buffers.rs`:
  ```rust
  //! Per-worker pool of stream query, answer and frame buffers. Every worker thread runs its own
  //! single-threaded runtime, so a thread-local pool is a per-worker pool without locks.

  use std::cell::RefCell;

  pub const POOL_MAX: usize = 256;
  /// A 65,535-octet message plus its 2-octet length prefix.
  pub const BUFFER_CAPACITY: usize = 65_537;

  thread_local! {
      static POOL: RefCell<Vec<Vec<u8>>> = const { RefCell::new(Vec::new()) };
  }

  /// An empty buffer with `BUFFER_CAPACITY` octets of capacity.
  pub fn take() -> Vec<u8> {
      POOL.with(|p| p.borrow_mut().pop())
          .unwrap_or_else(|| Vec::with_capacity(BUFFER_CAPACITY))
  }

  /// Returns a buffer; kept only when it still has the pool's capacity and the pool has room.
  pub fn give(mut b: Vec<u8>) {
      if !(BUFFER_CAPACITY - 2..=BUFFER_CAPACITY).contains(&b.capacity()) {
          return;
      }
      b.clear();
      POOL.with(|p| {
          let mut p = p.borrow_mut();
          if p.len() < POOL_MAX {
              p.push(b);
          }
      });
  }
  ```
  Declare `pub mod buffers;` in `server/mod.rs`.
- [x] In `WorkerAnswerer::answer_frames`, replace `let mut out = vec![0u8; 65535];` with `let mut out = buffers::take(); out.resize(65535, 0);`. `answer` already resizes the `out` it is given. On a `FastOutcome::Slow` outcome (zone transfer), `buffers::give(out)` before running the slow job. `buffers.rs` has a unit test `pool_reuses_bounded_and_rejects_grown_buffers` (reuse, capacity check, `POOL_MAX` bound).
- [x] In `stream.rs`:
  - replace `let mut msg = vec![0u8; n];` with `let mut msg = buffers::take(); msg.resize(n, 0);`;
  - in the per-query task, build each frame from `buffers::take()` and `give` the answer message after copying it;
  - `give(msg)` after `answer_frames` returns;
  - in the writer task, `buffers::give(frame)` after `write_all` (whether or not it succeeded);
  - delete the `debt:` comment.
- [x] In `doq.rs`:
  - read the stream into a pooled buffer, `let mut buf = buffers::take();` with a `recv.read_chunk(MAX_STREAM + 1 - buf.len(), true)` loop up to `MAX_STREAM` (exceeding it keeps the existing `message too long` protocol error), instead of `read_to_end`; new test `oversized_stream_closes_connection_with_protocol_error` in `doq.rs` (a shared `connect_echo_server` helper also serves the existing test);
  - `let mut out = buffers::take();` for the answer;
  - the frame comes from `buffers::take()`;
  - `give` all three after `send.write_all` (error paths drop them);
  - delete the `debt:` comment.
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test hot_path_alloc && cargo test --locked -p nexora-engine --lib server:: && cargo test --locked -p nexora-engine --test upstream_encrypted && cargo test --locked -p nexora-engine --test authoritative_pipeline'` and expect all to pass. That covers `pipelined_queries_all_answered_on_one_stream`, `one_stream_per_query_and_nonzero_id_closes_connection` and zone transfers over TCP.

## Task 12: recursor_cache_max_bytes in the API, snapshot and GUI (#18)

Depends on Task 2.

Files:

- `mgmt/migrations/00800_recursor_cache_max_bytes.sql` (new)
- `mgmt/internal/store/resolution.go`
- `mgmt/api/openapi.yaml`
- `mgmt/internal/api/gen.go` (generated)
- `mgmt/internal/api/resolution.go`
- `mgmt/internal/api/resolution_test.go`
- `mgmt/internal/snapshot/resolution.go`
- `web/src/api/schema.d.ts` (generated)
- `web/src/pages/ResolutionSection.tsx`
- `web/e2e/screens/17-resolution.spec.ts`

Interfaces: store field `ResolutionSettings.RecursorCacheMaxBytes int64`; API field `recursor_cache_max_bytes` (int64, 4194304..17179869184, required); snapshot `RecursionConfig.CacheMaxBytes`.

- [ ] Add to `resolution_test.go`:
  ```go
  func TestResolutionSettingsRecursorCacheMaxBytes(t *testing.T) {
  	op, _, env := roleClientsWith(t, nil)
  	var res map[string]any
  	if code := op.do(http.MethodGet, "/resolution", nil, &res); code != 200 || res["recursor_cache_max_bytes"] != float64(64<<20) {
  		t.Fatalf("default recursor_cache_max_bytes = %d %v", code, res["recursor_cache_max_bytes"])
  	}
  	var e apiErr
  	res["recursor_cache_max_bytes"] = (4 << 20) - 1
  	if code := op.do(http.MethodPut, "/resolution", res, &e); code != 400 || e.Code != "invalid_request" {
  		t.Fatalf("below 4 MiB = %d %+v", code, e)
  	}
  	res["recursor_cache_max_bytes"] = 128 << 20
  	if code := op.do(http.MethodPut, "/resolution", res, &res); code != 200 || res["recursor_cache_max_bytes"] != float64(128<<20) {
  		t.Fatalf("update = %d %v", code, res)
  	}
  	_, snap, err := snapshot.Latest(env.ctx, env.st.Pool)
  	if err != nil || snap.GetRecursion().GetCacheMaxBytes() != 128<<20 {
  		t.Fatalf("snapshot recursion.cache_max_bytes = %d (%v)", snap.GetRecursion().GetCacheMaxBytes(), err)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/api -run TestResolutionSettingsRecursorCacheMaxBytes'`. Expect FAIL `default recursor_cache_max_bytes = 200 <nil>`.
- [ ] Create `mgmt/migrations/00800_recursor_cache_max_bytes.sql`:
  ```sql
  -- +goose Up
  -- M7: the recursor cache memory budget (RecursionConfig.cache_max_bytes).
  ALTER TABLE resolution_settings
      ADD COLUMN recursor_cache_max_bytes bigint NOT NULL DEFAULT 67108864
      CHECK (recursor_cache_max_bytes BETWEEN 4194304 AND 17179869184);

  -- +goose Down
  ALTER TABLE resolution_settings DROP COLUMN recursor_cache_max_bytes;
  ```
- [ ] `store/resolution.go`: add `RecursorCacheMaxBytes int64` to `ResolutionSettings` and to the select and update statements of `GetResolutionSettings`/`UpdateResolutionSettings`.
- [ ] `openapi.yaml`: in `ResolutionSettings`, add `recursor_cache_max_bytes` to `required` and the property `recursor_cache_max_bytes: { type: integer, format: int64, minimum: 4194304, maximum: 17179869184, description: "Memory for the RRset, aggressive NSEC and server caches of recursive resolution, in bytes." }`. Regenerate on the laptop with `cd mgmt/api && go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config oapi-codegen.yaml openapi.yaml` (the version `gen.go` was generated with) and `cd web && pnpm run gen:api`; expect `gen.go` and `schema.d.ts` to change.
- [ ] `api/resolution.go`: map the field in `resolutionOut` and in the update input; `validateResolution` rejects values outside 4194304..17179869184 with `invalid_request`.
- [ ] `snapshot/resolution.go`: add `CacheMaxBytes: uint64(res.RecursorCacheMaxBytes),` to the `RecursionConfig` literal.
- [ ] `ResolutionSection.tsx`:
  - form field `recursor_cache_mib: string`, initialised with `String(s.recursor_cache_max_bytes / 1048576)`;
  - an input labelled `Recursor cache memory (MiB)` (type number, min 4, max 16384), with the hint "Memory for the RRset, aggressive NSEC and server caches of recursive resolution" (the `Field` `hint` this page already uses; the M6 help tooltip is not on this branch);
  - saved as `recursor_cache_max_bytes: Number(form.recursor_cache_mib) * 1048576`.
- [ ] In `17-resolution.spec.ts`, after the "Maximum upstream queries" fill:
  - `await card.getByLabel("Recursor cache memory (MiB)").fill("128");`;
  - after the reload, `await expect(page.getByRole("region", { name: "Resolution" }).getByLabel("Recursor cache memory (MiB)")).toHaveValue("128");`.
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/api/... ./mgmt/internal/snapshot/... ./mgmt/internal/store/... && make web-test && cd web && pnpm lint'` and expect all to pass. Run `scripts/dev-exec.sh 'make e2e-build && go test -count=1 ./e2e -run "TestGUICoverage" -timeout 60m'` and expect `17-resolution.spec.ts` and the coverage check to pass.

## Task 13: GetBlob streams from PostgreSQL (#25)

Files: `mgmt/internal/control/server.go`, `mgmt/internal/control/control_test.go`, `mgmt/migrations/00801_blobs_storage_external.sql` (new)
Interfaces: `(*control.Server).GetBlob` unchanged on the wire (chunks of at most `BlobChunkSize`, `codes.NotFound` for unknown blobs).

- [ ] Add to `control_test.go` (imports `runtime`, `time`, `crypto/sha256`):
  ```go
  func TestGetBlobDoesNotHoldWholeBlob(t *testing.T) {
  	f := setup(t, 1)
  	client, _ := f.enroll(t, f.addr[0])
  	data := make([]byte, 64<<20)
  	_, _ = rand.Read(data)
  	sha := sha256Hex(data)
  	if _, err := f.st.Pool.Exec(f.ctx, "insert into blobs(sha256,size,data) values ($1,$2,$3)", sha, len(data), data); err != nil {
  		t.Fatal(err)
  	}
  	data = nil
  	s, err := client.GetBlob(f.ctx, &controlv1.GetBlobRequest{Sha256: sha})
  	if err != nil {
  		t.Fatal(err)
  	}
  	h := sha256.New()
  	first, err := s.Recv()
  	if err != nil {
  		t.Fatal(err)
  	}
  	h.Write(first.Data)
  	time.Sleep(300 * time.Millisecond) // the server fills the flow-control window and waits in Send
  	runtime.GC()
  	var ms runtime.MemStats
  	runtime.ReadMemStats(&ms)
  	if ms.HeapAlloc > 40<<20 {
  		t.Fatalf("heap %d MiB while a 64 MiB blob is mid-stream", ms.HeapAlloc>>20)
  	}
  	chunks := 1
  	for {
  		c, err := s.Recv()
  		if err == io.EOF {
  			break
  		}
  		if err != nil {
  			t.Fatal(err)
  		}
  		chunks++
  		h.Write(c.Data)
  	}
  	if chunks != 64 || hex.EncodeToString(h.Sum(nil)) != sha {
  		t.Fatalf("chunks=%d, hash match=%v", chunks, hex.EncodeToString(h.Sum(nil)) == sha)
  	}
  }

  func TestGetBlobChunkBoundaries(t *testing.T) {
  	f := setup(t, 1)
  	client, _ := f.enroll(t, f.addr[0])
  	for _, c := range []struct{ size, chunks int }{{0, 0}, {control.BlobChunkSize, 1}, {2 * control.BlobChunkSize, 2}} {
  		data := make([]byte, c.size)
  		_, _ = rand.Read(data)
  		sha := sha256Hex(data)
  		if _, err := f.st.Pool.Exec(f.ctx, "insert into blobs(sha256,size,data) values ($1,$2,$3) on conflict do nothing", sha, len(data), data); err != nil {
  			t.Fatal(err)
  		}
  		s, err := client.GetBlob(f.ctx, &controlv1.GetBlobRequest{Sha256: sha})
  		if err != nil {
  			t.Fatal(err)
  		}
  		var got []byte
  		n := 0
  		for {
  			ch, err := s.Recv()
  			if err == io.EOF {
  				break
  			}
  			if err != nil {
  				t.Fatal(err)
  			}
  			n++
  			got = append(got, ch.Data...)
  		}
  		if n != c.chunks || sha256Hex(got) != sha {
  			t.Fatalf("size %d: %d chunks (want %d), match=%v", c.size, n, c.chunks, sha256Hex(got) == sha)
  		}
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/control -run "TestGetBlob"'`. Expect `TestGetBlobDoesNotHoldWholeBlob` to FAIL with `heap NNN MiB while a 64 MiB blob is mid-stream` (199 MiB measured before the rewrite); `TestGetBlobChunkBoundaries` passes today and guards the rewrite.
- [ ] Rewrite the body of `GetBlob` after authentication and the `sha256RE` check (delete the `debt:` comment):
  ```go
  // Blobs are content-addressed and never change, so chunks read by separate statements belong to
  // one value; a blob collected mid-stream ends the stream with NotFound (the engine checks size
  // and SHA-256 and retries).
  var size int64
  if err := s.st.Pool.QueryRow(ctx, "select octet_length(data) from blobs where sha256 = $1", req.Sha256).Scan(&size); err != nil {
  	if errors.Is(store.MapError(err), store.ErrNotFound) {
  		return status.Error(codes.NotFound, "blob not found")
  	}
  	return grpcError(err)
  }
  for off := int64(0); off < size; off += BlobChunkSize {
  	var chunk []byte
  	if err := s.st.Pool.QueryRow(ctx, "select substring(data from $2 for $3) from blobs where sha256 = $1",
  		req.Sha256, off+1, BlobChunkSize).Scan(&chunk); err != nil {
  		if errors.Is(store.MapError(err), store.ErrNotFound) {
  			return status.Error(codes.NotFound, "blob removed while streaming")
  		}
  		return grpcError(err)
  	}
  	if err := stream.Send(&controlv1.BlobChunk{Data: chunk}); err != nil {
  		return err
  	}
  }
  return nil
  ```
  Errors keep the existing mapping (`store.MapError` for NotFound, `grpcError` for Unavailable/Canceled/Internal) and the existing `blob not found` message.
- [ ] Create `mgmt/migrations/00801_blobs_storage_external.sql`:
  ```sql
  -- +goose Up
  -- M7: blob data is zstd (incompressible); stored uncompressed out of line, a substring read
  -- fetches only its chunk. Applies to rows written from now on.
  ALTER TABLE blobs ALTER COLUMN data SET STORAGE EXTERNAL;

  -- +goose Down
  ALTER TABLE blobs ALTER COLUMN data SET STORAGE EXTENDED;
  ```
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 -race ./mgmt/internal/control/... ./mgmt/internal/store/...'` and expect all to pass, including `TestGetBlobStreamsMiBChunks` and the lifecycle GetBlob tests.

## Task 14: OpenSearch paging with a unique tiebreaker (#26)

Files: `mgmt/internal/querylog/opensearch.go`, `mgmt/internal/querylog/opensearch_test.go`, `e2e/querylog_paging_test.go` (new)
Interfaces: the cursor is base64url JSON `[timestamp, _id]`; a one-element cursor is accepted as `[timestamp, ""]`.

- [x] Re-read `opensearch.go` at the M6 head (M6 #54, #56, #61 change the filters) and keep M6's filter code. (Built on m7 at 3ac5883, where M6 T5's filter changes are not yet merged; the change touches only the `sort` entry and the cursor decoding, so the merge with M6's filter code is textual only.)
- [x] Add to `opensearch_test.go` (imports `encoding/base64`, `encoding/json`):
  ```go
  func TestOpenSearchSortHasUniqueTiebreaker(t *testing.T) {
  	var bodies []string
  	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  		b, _ := io.ReadAll(r.Body)
  		bodies = append(bodies, string(b))
  		w.Header().Set("Content-Type", "application/json")
  		hit := func(id string) string {
  			return `{"_id":"` + id + `","_source":{"@timestamp":"2026-09-14T10:00:00.123Z","attributes":{"dns.question.name":"` + id + `.example."}},"sort":[1789380000123,"` + id + `"]}`
  		}
  		fmt.Fprintf(w, `{"hits":{"hits":[%s,%s]}}`, hit("a"), hit("b"))
  	}))
  	defer srv.Close()
  	os, err := querylog.NewOpenSearch(config.OpenSearchConfig{URL: srv.URL, Index: "nexora-querylog-*"})
  	if err != nil {
  		t.Fatal(err)
  	}
  	ctx := context.Background()
  	page, err := os.Search(ctx, querylog.Query{Limit: 1})
  	if err != nil || page.NextCursor == "" {
  		t.Fatalf("first page: %+v %v", page, err)
  	}
  	if !strings.Contains(bodies[0], `"sort":[{"@timestamp":{"order":"desc"}},{"_id":{"order":"asc"}}]`) {
  		t.Fatalf("sort lacks the _id tiebreaker: %s", bodies[0])
  	}
  	raw, _ := base64.RawURLEncoding.DecodeString(page.NextCursor)
  	var after []any
  	if err := json.Unmarshal(raw, &after); err != nil || len(after) != 2 || after[1] != "a" {
  		t.Fatalf("cursor = %s", raw)
  	}
  	if _, err := os.Search(ctx, querylog.Query{Limit: 1, Cursor: page.NextCursor}); err != nil {
  		t.Fatal(err)
  	}
  	if !strings.Contains(bodies[1], `"search_after":[1789380000123,"a"]`) {
  		t.Fatalf("second request: %s", bodies[1])
  	}
  	old := base64.RawURLEncoding.EncodeToString([]byte(`[1789380000123]`))
  	if _, err := os.Search(ctx, querylog.Query{Limit: 1, Cursor: old}); err != nil {
  		t.Fatalf("a cursor from an older instance: %v", err)
  	}
  	if !strings.Contains(bodies[2], `"search_after":[1789380000123,""]`) {
  		t.Fatalf("upgraded cursor: %s", bodies[2])
  	}
  }
  ```
- [x] Create `e2e/querylog_paging_test.go`:
  ```go
  package e2e

  // TestOpenSearchPagesRecordsSharingAMillisecond indexes three query-log documents with one
  // @timestamp into a private index and pages through them one record at a time via the API.
  func TestOpenSearchPagesRecordsSharingAMillisecond(t *testing.T) {
  	osURL := harness.OpenSearchURL(t)
  	index := "nexora-querylog-paging-" + strings.ToLower(strings.TrimSuffix(harness.UniqueName("t"), "."))
  	for i := range 3 {
  		doc := fmt.Sprintf(`{"@timestamp":"2026-09-14T10:00:00.123Z","attributes":{"client.address":"10.0.0.9","dns.question.name":"r%d.paging.test.","dns.question.type":"A","dns.response.code":"NOERROR","nexora.cache":"miss","nexora.filter":"none","nexora.transport":"udp","nexora.engine.id":"e","nexora.duration_us":1}}`, i)
  		resp, err := http.Post(osURL+"/"+index+"/_doc?refresh=true", "application/json", strings.NewReader(doc))
  		if err != nil || resp.StatusCode != http.StatusCreated {
  			t.Fatalf("index document: %v %v", resp, err)
  		}
  		resp.Body.Close()
  	}
  	t.Cleanup(func() {
  		req, _ := http.NewRequest(http.MethodDelete, osURL+"/"+index, nil)
  		if resp, err := http.DefaultClient.Do(req); err == nil {
  			resp.Body.Close()
  		}
  	})
  	env := harness.New(t)
  	pg := env.StartPostgres()
  	ca := env.InitCA()
  	mgmt := env.StartMgmt(pg, ca, harness.MgmtOptions{QueryLogBackend: "opensearch", OpenSearchURL: osURL,
  		ExtraEnv: []string{"NEXORA_OPENSEARCH_INDEX=" + index}})
  	api := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
  	seen := map[string]bool{}
  	cursor := ""
  	for range 5 {
  		var page struct {
  			Records []struct {
  				Name string `json:"name"`
  			} `json:"records"`
  			NextCursor string `json:"next_cursor"`
  		}
  		api.Must("GET", "/query-log?limit=1&name=paging.test&cursor="+url.QueryEscape(cursor), nil, &page, 200)
  		for _, r := range page.Records {
  			seen[r.Name] = true
  		}
  		if page.NextCursor == "" {
  			break
  		}
  		cursor = page.NextCursor
  	}
  	if len(seen) != 3 {
  		t.Fatalf("paged records %v, want all 3 sharing one millisecond", seen)
  	}
  }
  ```
  Use the query-log response field names and name filter of `mgmt/api/openapi.yaml` (`/query-log`) at the M6 head. The `name` filter keeps other tests' documents out, if the index pattern overlaps.
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/querylog -run TestOpenSearchSortHasUniqueTiebreaker'`. Expect FAIL `sort lacks the _id tiebreaker`. Run `scripts/dev-exec.sh 'make e2e-build && go test -count=1 ./e2e -run TestOpenSearchPagesRecordsSharingAMillisecond'`. Expect FAIL `paged records map[r0.paging.test.:true], want all 3`: page 2's `search_after [ts]` skips the other two.
- [x] In `opensearch.go`, set `"sort": []map[string]any{{"@timestamp": map[string]any{"order": "desc"}}, {"_id": map[string]any{"order": "asc"}}},` and replace the `debt:` comment with a one-line note on the tiebreaker. In cursor decoding:
  ```go
  if err != nil || json.Unmarshal(raw, &after) != nil || len(after) == 0 || len(after) > 2 {
  	return Page{}, ErrInvalidCursor
  }
  if len(after) == 1 { // a cursor from an instance without the _id tiebreaker
  	after = append(after, "")
  }
  ```
- [x] Run both commands again and expect PASS. Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/querylog/... && go test -count=1 ./e2e -run "TestQueryLogBackends|TestQueryLogCategoryAttribution"'` and expect PASS.

## Task 15: PKCS#11 session recovery (#27)

Files: `mgmt/internal/secrets/pkcs11.go`, `mgmt/internal/secrets/pkcs11_test.go`, `mgmt/internal/secrets/export_test.go` (new), `mgmt/internal/secrets/secrets.go` (`Unseal` passes `ErrBackendUnavailable` through)
Interfaces: `(*HSM).with(fn)` unchanged for callers. New unexported fields `label, pinFile string`, `mu sync.Mutex`, `gen uint64`. Test-only `(*Box).KillHSMSessionsForTest() error` and `(*Box).HSMPoolLenForTest() int`.

- [x] Create `mgmt/internal/secrets/export_test.go`:
  ```go
  //go:build cgo

  package secrets

  // KillHSMSessionsForTest closes every session of the token underneath the pool, as a token reset does.
  func (b *Box) KillHSMSessionsForTest() error { return b.hsm.ctx.CloseAllSessions(b.hsm.slot) }

  // HSMPoolLenForTest reports the sessions currently idle in the pool.
  func (b *Box) HSMPoolLenForTest() int { return len(b.hsm.pool) }
  ```
- [x] Add to `pkcs11_test.go`:
  ```go
  func TestPKCS11RecoversFromInvalidatedSessions(t *testing.T) {
  	cfg := softhsmConfig(t)
  	box, err := secrets.Open(cfg)
  	if err != nil {
  		t.Fatal(err)
  	}
  	defer box.Close()
  	if err := box.EnsureHSMWrapKey(t.Context()); err != nil {
  		t.Fatal(err)
  	}
  	env, err := box.Seal("test-purpose", []byte("secret"))
  	if err != nil {
  		t.Fatal(err)
  	}
  	if err := box.KillHSMSessionsForTest(); err != nil {
  		t.Fatal(err)
  	}
  	for i := range 8 {
  		if got, err := box.Unseal("test-purpose", env); err != nil || string(got) != "secret" {
  			t.Fatalf("unseal %d after the reset: %q %v", i, got, err)
  		}
  		k, err := box.GenerateSigningKey(t.Context(), secrets.BackendPKCS11, 13)
  		if err != nil {
  			t.Fatalf("generate %d after the reset: %v", i, err)
  		}
  		verifySignerMatchesDNSKEY(t, box, k)
  	}
  	// A reset the pool cannot recover from: the PIN is gone. The tests run as root in the dev pod,
  	// where chmod 0 does not stop reading, so the file gets a wrong PIN instead.
  	if err := os.WriteFile(cfg.PKCS11PinFile, []byte("000000\n"), 0o600); err != nil {
  		t.Fatal(err)
  	}
  	if err := box.KillHSMSessionsForTest(); err != nil {
  		t.Fatal(err)
  	}
  	done := make(chan error, 1)
  	go func() { _, err := box.Unseal("test-purpose", env); done <- err }()
  	select {
  	case err := <-done:
  		if !errors.Is(err, secrets.ErrBackendUnavailable) {
  			t.Fatalf("unrecoverable token -> %v, want ErrBackendUnavailable", err)
  		}
  	case <-time.After(10 * time.Second):
  		t.Fatal("call deadlocked on a dead token")
  	}
  	if n := box.HSMPoolLenForTest(); n != 4 {
  		t.Fatalf("pool holds %d sessions, want 4", n)
  	}
  }
  ```
  The dev pod runs tests as root, so `chmod 0` does not stop reading; the test writes a wrong PIN instead.
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/secrets -run TestPKCS11RecoversFromInvalidatedSessions'`. Expect FAIL `unseal 0 after the reset: "" envelope authentication failed` (`Unseal` hides the `CKR_SESSION_HANDLE_INVALID` underneath).
- [x] In `pkcs11.go`:
  - store `label`, `pinFile` in `openHSM`; the slot lookup and the login move into `findSlot()` and `login(sh, pin)`, shared by `openHSM` and `reopen`;
  - add `mu sync.Mutex` and `gen uint64`;
  - replace `with` (delete the `debt:` comment):
  ```go
  // invalidated reports PKCS#11 errors after which a session can never succeed again.
  func invalidated(err error) bool {
  	for _, code := range []uint{pkcs11.CKR_SESSION_HANDLE_INVALID, pkcs11.CKR_SESSION_CLOSED, pkcs11.CKR_DEVICE_REMOVED,
  		pkcs11.CKR_TOKEN_NOT_PRESENT, pkcs11.CKR_USER_NOT_LOGGED_IN} {
  		if errors.Is(err, pkcs11.Error(code)) {
  			return true
  		}
  	}
  	return false
  }

  // with runs fn on a pooled session. A session the token invalidated (removal, reset) is replaced
  // and fn retried once; a failed call changed nothing on the token, so the retry is safe.
  func (h *HSM) with(fn func(sh pkcs11.SessionHandle) error) error {
  	sh := <-h.pool
  	gen := h.generation()
  	err := fn(sh)
  	if !invalidated(err) {
  		h.pool <- sh
  		return err
  	}
  	fresh, rerr := h.reopen(sh, gen)
  	if rerr != nil {
  		h.pool <- sh // the pool never shrinks; later calls try to recover again
  		return fmt.Errorf("%w: pkcs11 session lost and not recovered: %v", ErrBackendUnavailable, rerr)
  	}
  	err = fn(fresh)
  	h.pool <- fresh
  	return err
  }
  ```
  - `generation()` reads `gen` under `mu`.
  - `reopen(old, seenGen)` takes `mu`:
    1. closes `old` and ignores the error;
    2. finds the slot by `label` again (the slot id can change after re-insertion) and stores it;
    3. opens a RW session;
    4. if `seenGen == h.gen` (the first recovery for this event), reads the PIN with `readSecretFile(pinFile)`, logs in with `CKU_USER`, tolerates `CKR_USER_ALREADY_LOGGED_IN`, clears the PIN bytes and increments `gen`;
    5. returns the new handle; on a failed login it closes the new session first.
- [x] In `secrets.go` `Unseal`, return an unwrap error that `errors.Is(err, ErrBackendUnavailable)` as is, instead of `envelope authentication failed`: a lost token is not a forged envelope.
- [x] Run `scripts/dev-exec.sh 'go test -count=1 -race ./mgmt/internal/secrets/... ./mgmt/internal/dnssec/...'` and expect all to pass, including `TestPKCS11SigningKeysStayInToken` and `TestSweepLeavesOtherInstallationsTokenKeys`.

## Task 16: Streaming zone export (#28)

Files: `mgmt/internal/zone/import.go`, `mgmt/internal/zonefile/export.go`, `mgmt/internal/api/zonefile.go`, `mgmt/internal/zone/export_test.go` (new), `mgmt/internal/api/zonefile_test.go` (new)
Interfaces:

- `func (s *Service) ExportTo(ctx context.Context, zoneID uuid.UUID, w io.Writer) error`
- `zonefile.NewWriter(w io.Writer, origin string, defaultTTL uint32) *zonefile.Writer`
- `(*Writer).SOA(dns.RR) error`, `(*Writer).Record(dns.RR) error`, `(*Writer).Flush() error`
- `zonefile.Export` keeps its signature and output, built on `Writer`.

- [x] Create `mgmt/internal/zone/export_test.go`:
  ```go
  package zone_test

  func TestExportToIsStableAndReparses(t *testing.T) {
  	ctx := context.Background()
  	export := func(order []int) string {
  		s := newService(t)
  		z := createZone(t, s, "stable.test.")
  		recs := []zone.RecordInput{
  			{Name: "www.stable.test.", Type: "A", TTL: 300, Data: "192.0.2.1"},
  			{Name: "a.b.stable.test.", Type: "TXT", TTL: 300, Data: `"x"`},
  			{Name: "stable.test.", Type: "MX", TTL: 300, Data: "10 mail.stable.test."},
  			{Name: "mail.stable.test.", Type: "AAAA", TTL: 300, Data: "2001:db8::1"},
  		}
  		for _, i := range order {
  			if _, err := s.CreateRecord(ctx, actor, z.ID, recs[i]); err != nil {
  				t.Fatal(err)
  			}
  		}
  		var b strings.Builder
  		if err := s.ExportTo(ctx, z.ID, &b); err != nil {
  			t.Fatal(err)
  		}
  		return b.String()
  	}
  	one, two := export([]int{0, 1, 2, 3}), export([]int{3, 2, 1, 0})
  	if one != two {
  		t.Fatalf("export depends on insertion order:\n%s\n---\n%s", one, two)
  	}
  	res, err := zonefile.Parse(strings.NewReader(one), "stable.test.", zonefile.Options{})
  	if err != nil || len(res.Records) < 5 {
  		t.Fatalf("export does not reparse: %v (%d records)", err, len(res.Records))
  	}
  }
  ```
  `zonefile.Parse` is called with `zonefile.Options{AllowedTypes: zone.ManagedTypes, MaxRecords: zone.MaxImportRecords}` (an empty `Options` rejects every type).
- [x] Create `mgmt/internal/api/zonefile_test.go`: `TestExportZoneFileStreams`.
  - It creates a zone (through the API, as `tsig_keys_test.go` does) and one record: a `www` TXT of 12 × 255-octet strings, because net/http announces a `Content-Length` by itself when the whole body fits its 2 KiB buffer before the handler returns.
  - It performs a raw `GET /api/v1/zones/<id>/export` with the operator client's `http.Client` and cookie jar.
  - It asserts status 200, the `Content-Disposition` header present, `resp.ContentLength == -1` (no `Content-Length`: the body streams), and a body containing `www`.
  - Add a `raw(method, path string) *http.Response` helper next to `client.do` in this file.
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/zone -run TestExportToIsStableAndReparses; go test -count=1 ./mgmt/internal/api -run TestExportZoneFileStreams'`. Expect a compile FAIL `s.ExportTo undefined` for the first. For the second, expect FAIL `ContentLength = <n>, want -1`.
- [x] In `zonefile/export.go`, extract the per-record line writing of `Export` into `Writer` (header `$ORIGIN`/`$TTL`, `SOA`, `Record` with the relative owner names and presentation `rdataText` Export already uses, `Flush`). `Export` sorts as before and writes through a `Writer`; `TestExportIsStableAndReparses` must keep passing unchanged.
- [x] In `zone/import.go`, replace `Export` (delete the `debt:` comment) with `ExportTo`. `Export` had no caller besides the handler, so no wrapper is kept.
  ```go
  // ExportTo writes the zone file of zoneID to w, streaming records from one read-only snapshot:
  // the SOA from the zone row, then the apex, then owners byte-wise, then type and RDATA.
  func (s *Service) ExportTo(ctx context.Context, zoneID uuid.UUID, w io.Writer) error {
  	tx, err := s.Store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
  	if err != nil {
  		return err
  	}
  	defer tx.Rollback(ctx) //nolint:errcheck // read-only
  	z, err := getZone(ctx, tx, zoneID)
  	if err != nil {
  		return err
  	}
  	zw := zonefile.NewWriter(w, z.Name, uint32(z.DefaultTTL))
  	if err := zw.SOA(soaRR(z)); err != nil {
  		return err
  	}
  	rows, err := tx.Query(ctx, `SELECT owner, rtype, ttl, rdata_wire FROM zone_records WHERE zone_id = $1
  		ORDER BY lower(owner) <> lower($2), lower(owner) COLLATE "C", rtype, rdata_wire`, zoneID, z.Name)
  	if err != nil {
  		return err
  	}
  	defer rows.Close()
  	for rows.Next() {
  		rr, err := scanRR(rows) // the decoding loadRecordRRs does per row
  		if err != nil {
  			return err
  		}
  		if err := zw.Record(rr); err != nil {
  			return err
  		}
  	}
  	if err := rows.Err(); err != nil {
  		return err
  	}
  	return zw.Flush()
  }
  ```
  The zone row is read with `loadZone(ctx, tx, zoneID, false)` and the SOA built with `soaRR`. The per-row decoding (`PackDomainName` + `nzf.ToRR`, as `loadRecordRRs` does) is written inline in `ExportTo`, because `service.go` is Task 17's file.
- [x] In `api/zonefile.go`, keep the `GetZone` 404 check. Then stream:
  ```go
  pr, pw := io.Pipe()
  go func() { pw.CloseWithError(h.d.Zones.ExportTo(ctx, id, pw)) }()
  return ExportZoneFile200TextplainCharsetUtf8Response{Body: pr, Headers: ExportZoneFile200ResponseHeaders{ContentDisposition: disposition}}, nil
  ```
  `ContentLength` stays zero, so the generated visitor sets no `Content-Length`. A failure after the status line truncates the body, and the generated error handler logs it.
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/zone/... ./mgmt/internal/zonefile/... ./mgmt/internal/api/... && make e2e-build && go test -count=1 ./e2e -run TestZoneFileRoundTrip'` and expect all to pass.

## Task 17: Record edits without loading the zone (#29)

Files: `mgmt/internal/zone/service.go`, `mgmt/internal/zone/build.go`, `mgmt/internal/zone/validate.go`, `mgmt/internal/zone/model.go` (`RebuildOptions` lives there; one field and one type added), `mgmt/internal/zone/edit_test.go` (new), `mgmt/internal/zone/export_internal_test.go` (new)
Interfaces:

- `RebuildOptions` gains `Edit *EditDelta`, with `type EditDelta struct { Before, After []dns.RR }`: the RRsets at the edited owner/type pairs before and after the change.
- `checkOwners(ctx context.Context, tx pgx.Tx, z *Zone, owners ...string) error` replaces `checkZoneRecords` for record edits.
- `recordEdit(ctx, tx, z, owners []string, sets []rrset, change func() error) (*EditDelta, error)` reads the RRsets, runs the change, checks the owners and reads the RRsets again; `rtypeOf(*Record) uint16`.
- `build.go` helpers shared by both paths: `nextSerial(z, opts)`, `writeVersion(ctx, tx, z, origin, newSerial, d *nzf.Delta, forceImage, image func() ([]nzf.Record, error))`, `writeImage(ctx, tx, z, origin, seq, serial, recs)`.
- `CheckSet` stays for import and dynamic updates.

- [x] Create `mgmt/internal/zone/edit_test.go` (package `zone_test`, reusing `newService`, `createZone` and `actor` from `service_test.go`):
  ```go
  // rowTracer counts the rows each zone_records SELECT returned.
  type rowTracer struct {
  	mu   sync.Mutex
  	max  int64
  	full int
  }

  func (r *rowTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
  	return context.WithValue(ctx, rowTracerKey{}, d.SQL)
  }

  func (r *rowTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryEndData) {
  	sql, _ := ctx.Value(rowTracerKey{}).(string)
  	if !strings.Contains(sql, "FROM zone_records") || !strings.HasPrefix(strings.TrimSpace(strings.ToUpper(sql)), "SELECT") {
  		return
  	}
  	r.mu.Lock()
  	defer r.mu.Unlock()
  	r.max = max(r.max, d.CommandTag.RowsAffected())
  }

  type rowTracerKey struct{}

  func tracedService(t *testing.T) (*zone.Service, *rowTracer) {
  	base := storetest.New(t)
  	cfg, err := pgxpool.ParseConfig(base.Pool.Config().ConnString())
  	if err != nil {
  		t.Fatal(err)
  	}
  	tr := &rowTracer{}
  	cfg.ConnConfig.Tracer = tr
  	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
  	if err != nil {
  		t.Fatal(err)
  	}
  	t.Cleanup(pool.Close)
  	return &zone.Service{Store: &store.Store{Pool: pool}, Now: time.Now}, tr
  }

  func TestRecordEditDoesNotLoadWholeZone(t *testing.T) {
  	ctx := context.Background()
  	s, tr := tracedService(t)
  	z := createZone(t, s, "big.test.")
  	for i := range 2000 {
  		if _, err := s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: fmt.Sprintf("h%d.big.test.", i), Type: "A", TTL: 300, Data: "192.0.2.1"}); err != nil {
  			t.Fatal(err)
  		}
  	}
  	tr.mu.Lock()
  	tr.max = 0
  	tr.mu.Unlock()
  	r, err := s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "new.big.test.", Type: "A", TTL: 300, Data: "192.0.2.2"})
  	if err != nil {
  		t.Fatal(err)
  	}
  	r, err = s.UpdateRecord(ctx, actor, z.ID, r.ID, r.Revision, zone.RecordInput{Name: "moved.big.test.", Type: "A", TTL: 300, Data: "192.0.2.3"})
  	if err != nil {
  		t.Fatal(err)
  	}
  	if err := s.DeleteRecord(ctx, actor, z.ID, r.ID, r.Revision); err != nil {
  		t.Fatal(err)
  	}
  	if tr.max > 16 {
  		t.Fatalf("a record edit read %d zone_records rows", tr.max)
  	}
  }
  ```
  As built, the 2,000 records are bulk-inserted with one `INSERT ... SELECT generate_series` and published by `zone.RebuildWithImageForTest(t, pool, zoneID)` (in `export_internal_test.go`): a forced `Rebuild` plus a full image at the new version (`Force` alone writes no image), so no image falls due during the three measured edits.
  ```go
  func TestIncrementalEditsMatchFullRebuild(t *testing.T) {
  	ctx := context.Background()
  	s := newService(t)
  	z := createZone(t, s, "inc.test.")
  	rng := rand.New(rand.NewPCG(1, 2))
  	var live []*zone.Record
  	for i := range 200 {
  		switch {
  		case len(live) == 0 || rng.IntN(3) == 0:
  			r, err := s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: fmt.Sprintf("n%d.inc.test.", rng.IntN(40)), Type: "A", TTL: uint32(60 + rng.IntN(3)*60), Data: fmt.Sprintf("192.0.2.%d", i%250)})
  			if err == nil {
  				live = append(live, r)
  			}
  		case rng.IntN(2) == 0:
  			j := rng.IntN(len(live))
  			r, err := s.UpdateRecord(ctx, actor, z.ID, live[j].ID, live[j].Revision, zone.RecordInput{Name: fmt.Sprintf("n%d.inc.test.", rng.IntN(40)), Type: "A", TTL: 300, Data: fmt.Sprintf("198.51.100.%d", i%250)})
  			if err == nil {
  				live[j] = r
  			}
  		default:
  			j := rng.IntN(len(live))
  			if err := s.DeleteRecord(ctx, actor, z.ID, live[j].ID, live[j].Revision); err == nil {
  				live = append(live[:j], live[j+1:]...)
  			}
  		}
  	}
  	served, err := zone.LoadServed(ctx, s.Store.Pool, z.ID)
  	if err != nil {
  		t.Fatal(err)
  	}
  	desired, err := zone.LoadRecords(ctx, s.Store.Pool, z.ID)
  	if err != nil {
  		t.Fatal(err)
  	}
  	if diff := zone.DiffForTest(served, desired); diff != "" {
  		t.Fatalf("served zone differs from its records after incremental edits:\n%s", diff)
  	}
  }
  ```
  As built: `LoadServed(ctx, tx, z)` and `LoadRecords(ctx, tx, zoneID)` take a transaction, so the test begins one and re-reads the zone with `GetZone`; after every edit the test refreshes the revisions of the live records (a sibling's TTL change bumps them) and fails if fewer than 100 of the 200 edits succeeded, so the delta is exercised. Add `DiffForTest` in `mgmt/internal/zone/export_internal_test.go`, a package-internal test file this task owns: it compares ignoring SOA serial and RRSIGs and returns the differing lines. A failed create or update (for example an RRset TTL conflict) is fine and skipped.
  ```go
  func TestOwnerScopedChecksKeepRules(t *testing.T) {
  	ctx := context.Background()
  	s := newService(t)
  	z := createZone(t, s, "own.test.")
  	must := func(in zone.RecordInput) *zone.Record {
  		t.Helper()
  		r, err := s.CreateRecord(ctx, actor, z.ID, in)
  		if err != nil {
  			t.Fatalf("%+v: %v", in, err)
  		}
  		return r
  	}
  	code := func(err error, want string) {
  		t.Helper()
  		var ve *zone.ValidationError
  		if !errors.As(err, &ve) || ve.Code != want {
  			t.Fatalf("got %v, want %s", err, want)
  		}
  	}
  	must(zone.RecordInput{Name: "x.sub.own.test.", Type: "A", TTL: 300, Data: "192.0.2.1"})
  	_, err := s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "sub.own.test.", Type: "DNAME", TTL: 300, Data: "other.test."})
  	code(err, "dname_occludes")
  	must(zone.RecordInput{Name: "d.own.test.", Type: "DNAME", TTL: 300, Data: "other.test."})
  	_, err = s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "y.d.own.test.", Type: "A", TTL: 300, Data: "192.0.2.1"})
  	code(err, "dname_occludes")
  	_, err = s.CreateRecord(ctx, actor, z.ID, zone.RecordInput{Name: "ds.own.test.", Type: "DS", TTL: 300, Data: "12345 13 2 " + strings.Repeat("ab", 32)})
  	code(err, "ds_not_at_delegation")
  	a := must(zone.RecordInput{Name: "host.own.test.", Type: "A", TTL: 300, Data: "192.0.2.9"})
  	c := must(zone.RecordInput{Name: "alias.own.test.", Type: "CNAME", TTL: 300, Data: "www.example."})
  	_, err = s.UpdateRecord(ctx, actor, z.ID, c.ID, c.Revision, zone.RecordInput{Name: "host.own.test.", Type: "CNAME", TTL: 300, Data: "www.example."})
  	code(err, "cname_conflict")
  	_ = a
  	ns := apexNS(t, s, z) // the zone's only apex NS record
  	_, err = s.UpdateRecord(ctx, actor, z.ID, ns.ID, ns.Revision, zone.RecordInput{Name: "www.own.test.", Type: "NS", TTL: 300, Data: "ns1.own.test."})
  	code(err, "last_apex_ns")
  }
  ```
  `apexNS` lists the zone's records with the service's list function and returns the apex NS.
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/zone -run "TestRecordEditDoesNotLoadWholeZone|TestIncrementalEditsMatchFullRebuild|TestOwnerScopedChecksKeepRules"'`. Expect `TestRecordEditDoesNotLoadWholeZone` to FAIL with `a record edit read 20xx zone_records rows`. The other two pass today and guard the rewrite.
- [x] In `validate.go`, factor `CheckSet`'s rules into per-owner functions. `CheckSet` keeps calling them over the whole set.
- [x] In `service.go`, add `checkOwners`. It reads only these rows and applies the same rules and error codes:
  - `SELECT lower(owner), rtype FROM zone_records WHERE zone_id = $1 AND lower(owner) = ANY($2)` for the apex, the edited owners and their ancestors up to the apex (DNAME above, DS/NS at the owner, CNAME exclusivity); the apex is always among the names, so this also gives the apex NS count without a separate query;
  - for an owner holding a DNAME, `SELECT lower(owner) FROM zone_records WHERE zone_id = $1 AND lower(owner) LIKE '%.' || $2 ESCAPE '\' LIMIT 1` with `%`, `_` and `\` escaped in `$2` (the name found goes into the `dname_occludes` message, as `CheckSet` words it);
  - owners with no records left are skipped, as `CheckSet` only visits owners that hold records.
  - `CreateRecord` checks the new owner; `UpdateRecord` checks the old and new owners; `DeleteRecord` checks the deleted owner.
  - Replace the `checkZoneRecords` calls, and delete `checkZoneRecords` if nothing else uses it.
- [x] In `CreateRecord`/`UpdateRecord`/`DeleteRecord`:
  - read the RRsets at each affected (owner, type) before the change (`SELECT ... WHERE zone_id = $1 AND lower(owner) = lower($2) AND rtype = $3`) and after it;
  - pass them as `RebuildOptions{Edit: &EditDelta{Before, After}}` through `Mutate`.
- [x] In `build.go`'s `Rebuild`, add the incremental path at the top:
  ```go
  if opts.Edit != nil && !z.DNSSECEnabled && z.Kind == "primary" && z.CurrentSeq > 0 && !opts.Force {
  	return rebuildEdit(ctx, tx, z, opts, now)
  }
  ```
  `rebuildEdit`:
  1. converts `Before`/`After` with `toRecords`;
  2. diffs them with `diffIgnoringSOA(before, after)` (no change and no serial → return false);
  3. computes the new serial as `Rebuild` does;
  4. builds the old SOA from the zone row with `oldSerial` and the new SOA with `newSerial` (`soaRR(z)` with the serial replaced); it reads the served serial at `current_seq` (`zone_journal.to_serial`, else `zone_images.serial`) and forces an image when it differs from the row's serial, as `Rebuild` does;
  5. writes the `nzf.Delta{Origin, FromSerial, ToSerial, Deleted: [oldSOA]+deleted, Added: [newSOA]+added}` journal row exactly as `Rebuild` does;
  6. when `needImage` is due, loads `desiredRRs` once and writes the image;
  7. prunes the journal and updates the zone row as `Rebuild` does.
  - Share the journal, image and zone-row code with `Rebuild` by extracting it into a helper rather than copying it.
  - Replace the old `debt:` marker in `service.go` with this marker at the incremental-path condition in `build.go`:
    `// debt: DNSSEC-signed zones still rebuild from the whole record set per edit (NSEC/NSEC3 chain and RRSIG maintenance need the ordered owner set; zone_signatures limits re-signing); revisit when signed zones with hundreds of thousands of records are edited record by record.`
- [x] Run `scripts/dev-exec.sh 'go test -count=1 -race ./mgmt/internal/zone/... ./mgmt/internal/dnssec/... ./mgmt/internal/dynupdate/... ./mgmt/internal/xfrin/...'` and expect all to pass. Run `scripts/dev-exec.sh 'make e2e-build && go test -count=1 ./e2e -run "TestAuthoritativeZonePropagation|TestZoneFileRoundTrip|TestSecondaryAndDynamicUpdate"'` and expect PASS.

## Task 18: Wildcard synthesis in the private DNS hierarchy (#9)

Files: `e2e/fixtures/authhier/spec.go`, `e2e/fixtures/authhier/server.go`, `e2e/fixtures/authhier/authhier_test.go`, `e2e/dnssec_test.go`
Interfaces: `ZoneSpec.OmitWildcardProof bool` (json `omit_wildcard_proof`); `DefaultSpec` gains `*.w.good.test.`, `*.w.n3.test.` and the zone `wild.test.` on 127.0.53.12.

- [x] In `spec.go`:
  - add the field `OmitWildcardProof bool \`json:"omit_wildcard_proof"\``with the comment`serves synthesised wildcard answers without the next-closer denial`;
  - add to `good.test.` the records `*.w.good.test. 300 IN A 192.0.2.60` and `*.w.good.test. 300 IN TXT "wild"`;
  - add to `n3.test.` the record `*.w.n3.test. 300 IN A 192.0.2.61`;
  - add to `test.` the records `wild.test. 300 IN NS ns.wild.test.` and `ns.wild.test. 300 IN A 127.0.53.12`;
  - add the zone `{Origin: "wild.test.", ServerIP: "127.0.53.12", Signed: true, NSEC3Iterations: nsec, OmitWildcardProof: true, Records: ["wild.test. 300 IN SOA ns.wild.test. hostmaster.wild.test. 1 3600 600 86400 300", "wild.test. 300 IN NS ns.wild.test.", "ns.wild.test. 300 IN A 127.0.53.12", "*.w.wild.test. 300 IN A 192.0.2.62"]}`;
  - extend the `DefaultSpec` doc table.
- [x] Add to `authhier_test.go` `TestWildcardAnswerCarriesExpandedSignatureAndNextCloserProof`:
  - start the default hierarchy as the other tests there do;
  - query `x.w.good.test. A` with DO at 127.0.53.3;
  - assert one A `192.0.2.60` owned by `x.w.good.test.`;
  - assert an RRSIG over it with `Labels == 3` (fewer than the owner's 4) that verifies against the zone's ZSK with the owners of copies of the A and the RRSIG rewritten back to `*.w.good.test.` (`wildSig.Verify(zsk, []dns.RR{wildcardCopy})`; miekg's `Verify` rejects an RRset whose owner differs from the RRSIG's);
  - assert an NSEC in the authority section whose owner sorts before `x.w.good.test.` and whose next name sorts after it;
  - query `x.w.good.test. AAAA` and assert NOERROR, no answer, SOA and NSEC in the authority section;
  - query `x.w.wild.test. A` at 127.0.53.12 and assert the answer carries no NSEC in the authority section.
- [x] Add three subtests to `TestDNSSECValidation` in `e2e/dnssec_test.go`:
  ```go
  t.Run("wildcard answer validates with AD", func(t *testing.T) {
  	m := query(t, addr, "x.w.good.test", dns.TypeA, qopt{DO: true})
  	wantA(t, m, "192.0.2.60")
  	if !m.AuthenticatedData {
  		t.Fatal("AD=0 for a validated NSEC wildcard answer")
  	}
  	n := query(t, addr, "x.w.n3.test", dns.TypeA, qopt{DO: true})
  	wantA(t, n, "192.0.2.61")
  	if !n.AuthenticatedData {
  		t.Fatal("AD=0 for a validated NSEC3 wildcard answer")
  	}
  })
  t.Run("wildcard NODATA is proven", func(t *testing.T) {
  	m := query(t, addr, "x.w.good.test", dns.TypeAAAA, qopt{DO: true})
  	if m.Rcode != dns.RcodeSuccess || len(m.Answer) != 0 || !m.AuthenticatedData {
  		t.Fatalf("wildcard NODATA: rcode=%s answers=%d ad=%v", dns.RcodeToString[m.Rcode], len(m.Answer), m.AuthenticatedData)
  	}
  })
  t.Run("wildcard answer without next-closer proof is bogus", func(t *testing.T) {
  	// Positive path first: the zone itself validates.
  	if ns := query(t, addr, "ns.wild.test", dns.TypeA, qopt{DO: true}); !ns.AuthenticatedData {
  		t.Fatal("wild.test is not a secure zone; the negative check below would prove nothing")
  	}
  	m := query(t, addr, "x.w.wild.test", dns.TypeA, qopt{DO: true})
  	if m.Rcode != dns.RcodeServerFailure || len(aValues(m)) != 0 {
  		t.Fatalf("unproven wildcard served: rcode=%s answers=%v", dns.RcodeToString[m.Rcode], aValues(m))
  	}
  	if code, ok := edeCode(m); !ok || code != 12 {
  		t.Fatalf("EDE = %d (present %v), want 12", code, ok)
  	}
  })
  ```
  If the engine reports another EDE for a missing wildcard proof, check `validator_tests.rs` `wildcard_expansion_needs_a_next_closer_proof` (EDE 12) and use the code it asserts.
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./e2e/fixtures/authhier -run TestWildcardAnswer'`. Expect FAIL: the query for `x.w.good.test.` returns NXDOMAIN.
- [x] In `server.go` `authoritative`, before the NXDOMAIN branch, add RFC 1034 §4.3.3 synthesis:
  1. Factor the closest-encloser walk (`ce`) and the `nextCloser` computation into `func (z *zone) closestEncloser(name string) (ce, nextCloser string)`.
  2. If `!z.exists[name]` and `z.exists["*."+ce]`:
     - With a set `{"*."+ce, qtype}` (or a CNAME): copy each RR and its RRSIGs with `Hdr.Name` set to `name`, leaving `RRSIG.Labels` unchanged, into `m.Answer`.
     - Otherwise: SOA plus the NODATA proof (NSEC `z.cover(name)` and `z.match("*."+ce)`; NSEC3 `z.match(ce)`, `z.cover(nextCloser)`, `z.match("*."+ce)`).
     - When `secure && !z.spec.OmitWildcardProof`, add the next-closer proof for answers: NSEC `z.addDenial(m, z.cover(name))`; NSEC3 `z.addDenial(m, z.cover(nextCloser))`.
     - Return.
  3. Delete the `debt:` comment.
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./e2e/fixtures/authhier/... && make e2e-build && go test -count=1 ./e2e -run "TestDNSSECValidation|TestRecursionRootHints|TestSpoofedReplyRejected"'` and expect every test to pass. `TestDelvValidatesTheHierarchyIndependently` also validates the new records.

## Task 19: Collector restart waits for its port (#10)

Files: `e2e/harness/otelcol.go`, `e2e/harness/harness_test.go`
Interfaces: `(*Otelcol).Restart(e *Env)` unchanged in signature; it retries the same port for up to 10 s.

- [x] Add to `harness_test.go`:
  ```go
  func TestHarnessOtelcolRestartWaitsForPortHolder(t *testing.T) {
  	env := harness.New(t)
  	col := env.StartOtelcol(harness.OtelcolConfig{DebugFile: env.Dir + "/otel.jsonl"})
  	col.Stop()
  	l, err := net.Listen("tcp", col.OTLPGRPC)
  	if err != nil {
  		t.Fatalf("take the stopped collector's port: %v", err)
  	}
  	go func() { time.Sleep(1500 * time.Millisecond); l.Close() }()
  	started := time.Now()
  	col.Restart(env)
  	if time.Since(started) < time.Second {
  		t.Fatal("restart returned while another process still held the port")
  	}
  	c, err := net.DialTimeout("tcp", col.OTLPGRPC, time.Second)
  	if err != nil {
  		t.Fatalf("collector not listening after restart: %v", err)
  	}
  	c.Close()
  }
  ```
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./e2e/harness -run TestHarnessOtelcolRestartWaitsForPortHolder'`. Expect FAIL `otelcol-contrib exited: ... address already in use`.
- [x] Replace `Restart` (delete the `debt:` comment):
  ```go
  // Restart stops the collector if running and starts it again on the same port and config: the
  // exporters point at that port. A port another process holds is retried for 10 s.
  func (o *Otelcol) Restart(e *Env) {
  	e.T.Helper()
  	o.Proc.Stop()
  	deadline := time.Now().Add(10 * time.Second)
  	for !o.start(e, time.Now().Before(deadline)) {
  		time.Sleep(200 * time.Millisecond)
  	}
  }
  ```
  `start(e, false)` still fails the test with the collector's log, which names the address in use. Prefix that `Fatalf` message with `otelcol %s:` and `o.OTLPGRPC`.
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./e2e/harness -run Otelcol && make e2e-build && go test -count=1 ./e2e -run TestOTelSinkDownNoBackpressure'` and expect PASS.

## Task 20: The Compose example on novanas (#5)

Files: `scripts/compose-verify.sh` (new), `deploy/compose/docker-compose.yml`, `deploy/compose/.env.example`, `deploy/compose/engine.toml`, `deploy/compose/otel-collector.yaml`, `docs/operations.md` (sections "Install with Docker Compose", "Plain PostgreSQL and Compose", "Engines ahead of a restored database" only), `deploy/deploytest/compose_test.go`
Interfaces: `scripts/compose-verify.sh <user@host>` prints `ok <step>` per step and exits 0 only when all pass. Environment:

- `NEXORA_TAG` (default `sha-$(git rev-parse --short=7 HEAD)`; the image must exist on Nexus)
- `NEXORA_SOURCE_REGISTRY` (default `192.168.10.131:5000/azrtydxb`)

- [ ] In `TestComposeExample`, append:
  ```go
  ops, err := os.ReadFile("../../docs/operations.md")
  if err != nil || strings.Contains(string(ops), "it is not started") || !strings.Contains(string(ops), "scripts/compose-verify.sh") {
  	t.Errorf("docs/operations.md must describe the real Compose run (scripts/compose-verify.sh): %v", err)
  }
  if _, err := os.Stat("../../scripts/compose-verify.sh"); err != nil {
  	t.Errorf("scripts/compose-verify.sh: %v", err)
  }
  ```
  Also replace the test's doc comment with `// TestComposeExample checks deploy/compose statically; scripts/compose-verify.sh starts it on a Docker host.`
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./deploy/deploytest -run TestComposeExample'` and expect FAIL on the docs assertion.
- [ ] Create `scripts/compose-verify.sh` (mode 0755). It runs the documented commands verbatim on the host, with the documented `.env` settings changed:
  ```bash
  #!/usr/bin/env bash
  # Runs the "Install with Docker Compose" and "Plain PostgreSQL and Compose" steps of docs/operations.md
  # on a Docker host over ssh, checks enrollment, DNS, the collector, backup and restore, and removes
  # everything it created. Usage: scripts/compose-verify.sh user@host
  # shellcheck disable=SC2016 # single-quoted commands expand on the host, not here
  set -euo pipefail
  HOST=${1:?usage: scripts/compose-verify.sh user@host}
  ADDR=${HOST#*@}
  TAG=${NEXORA_TAG:-sha-$(git rev-parse --short=7 HEAD)}
  SRC=${NEXORA_SOURCE_REGISTRY:-192.168.10.131:5000/azrtydxb}
  DIR=nexora-compose-verify
  API=http://$ADDR:18080/api/v1
  WORK=$(mktemp -d)
  JAR=$WORK/cookies
  ok() { echo "ok $1"; }
  remote() { ssh -o BatchMode=yes "$HOST" "cd ~/$DIR && export COMPOSE_PROJECT_NAME=nexora-verify && $*"; }
  api() { curl -fsS -b "$JAR" -c "$JAR" -H 'Content-Type: application/json' "$@"; }

  cleanup() {
  	remote 'docker compose --profile engine --profile otel down -v --remove-orphans' >/dev/null 2>&1 || true
  	ssh -o BatchMode=yes "$HOST" "comm -13 ~/$DIR.images-before <(docker image ls -q | sort -u) | xargs -r docker image rm -f; rm -rf ~/$DIR ~/$DIR.images-before" >/dev/null 2>&1 || true
  	rm -rf "$WORK"
  }
  trap cleanup EXIT
  trap 'echo "failed at line $LINENO: $BASH_COMMAND" >&2' ERR

  arch=$(ssh -o BatchMode=yes "$HOST" 'uname -m')
  case $arch in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac
  ssh -o BatchMode=yes "$HOST" "test ! -e ~/$DIR && docker image ls -q | sort -u > ~/$DIR.images-before"
  for image in nexora-mgmt nexora-engine; do
  	# --insecure: the kw Nexus certificate is not in the laptop's trust store either.
  	crane pull --insecure --platform "linux/$arch" "$SRC/$image:$TAG" "$WORK/$image.tar"
  	ssh -o BatchMode=yes "$HOST" 'docker load' <"$WORK/$image.tar" >/dev/null
  done
  ok pull

  tar --no-xattrs -C deploy/compose -cf - . | ssh -o BatchMode=yes "$HOST" "mkdir -p ~/$DIR && tar -C ~/$DIR -xf -"
  # The documented steps, with this run's registry, tag, address and ports.
  remote "cp .env.example .env && sed -i \
    -e 's|^NEXORA_TAG=.*|NEXORA_TAG=$TAG|' -e 's|^NEXORA_REGISTRY=.*|NEXORA_REGISTRY=$SRC|' \
    -e 's|^NEXORA_PUBLIC_URL=.*|NEXORA_PUBLIC_URL=http://$ADDR:18080|' -e 's|^NEXORA_PUBLIC_HOST=.*|NEXORA_PUBLIC_HOST=$ADDR|' \
    -e 's|^NEXORA_DNS_PORT=.*|NEXORA_DNS_PORT=15353|' -e 's|^NEXORA_HTTP_PORT=.*|NEXORA_HTTP_PORT=18080|' \
    -e 's|^NEXORA_GRPC_PORT=.*|NEXORA_GRPC_PORT=19443|' -e 's|^NEXORA_METRICS_PORT=.*|NEXORA_METRICS_PORT=19153|' \
    -e 's|^NEXORA_OTLP_ENDPOINT=.*|NEXORA_OTLP_ENDPOINT=http://otel-collector:4317|' .env"
  remote 'openssl rand -hex 24 > secrets/postgres-password'
  remote 'printf "postgres:5432:nexora:nexora:%s\n" "$(cat secrets/postgres-password)" > secrets/pgpass'
  remote 'chmod 0644 secrets/postgres-password secrets/pgpass'
  remote 'docker compose up -d'
  ok up

  token=""
  for _ in $(seq 60); do
  	token=$(remote 'docker compose logs mgmt' 2>/dev/null | sed -n 's/.*setup token: //p' | tail -1)
  	[ -n "$token" ] && break
  	sleep 2
  done
  [ -n "$token" ] || {
  	echo "no setup token in the mgmt log"
  	exit 1
  }
  ok setup-token

  password=$(openssl rand -hex 16)
  api -X POST "$API/setup" -d "{\"token\":\"$token\",\"username\":\"admin\",\"email\":\"admin@example.net\",\"password\":\"$password\"}" >/dev/null
  api -X POST "$API/auth/login" -d "{\"username\":\"admin\",\"password\":\"$password\"}" >/dev/null
  zone=$(api -X POST "$API/zones" -d '{"name":"compose.test.","kind":"primary","default_ttl":300,"soa":{"mname":"ns1.compose.test.","rname":"hostmaster.compose.test."},"nameservers":["ns1.compose.test."]}' | jq -r .id)
  api -X POST "$API/zones/$zone/records" -d '{"name":"www.compose.test.","type":"A","ttl":300,"data":"192.0.2.10"}' >/dev/null
  ok setup

  remote 'docker compose run --rm -T mgmt join-token create --engine-group default --ttl 1h > secrets/join-token'
  remote 'docker compose --profile engine up -d'
  remote 'docker compose --profile otel up -d'
  ok join-token

  for _ in $(seq 60); do
  	api "$API/engines" | jq -e '.[] | select(.connected and .status == "current")' >/dev/null && break
  	sleep 2
  done
  api "$API/engines" | jq -e '.[] | select(.connected and .status == "current")' >/dev/null
  ok engine-enrolled

  dns() { dig +short +time=2 +tries=3 @"$ADDR" -p 15353 www.compose.test A; }
  for _ in $(seq 30); do
  	[ "$(dns)" = 192.0.2.10 ] && break
  	sleep 2
  done
  [ "$(dns)" = 192.0.2.10 ]
  ok dns-answers

  # With the built-in query log the engine sends query logs to mgmt, not to the collector; the collector's
  # debug exporter logs the engine's OTLP metrics (pushed every 15 s) as "data points".
  for _ in $(seq 30); do
  	remote 'docker compose logs otel-collector' | grep -q 'data points' && break
  	sleep 2
  done
  remote 'docker compose logs otel-collector' | grep -q 'data points'
  ok otel-logs

  remote 'docker compose exec -T postgres pg_dump -U nexora -Fc nexora > nexora-backup.dump'
  record=$(api "$API/zones/$zone/records?name=www.compose.test." | jq -r '.items[] | select(.name=="www.compose.test.") | "\(.id) \(.revision)"')
  api -X DELETE "$API/zones/$zone/records/${record% *}?revision=${record#* }" >/dev/null
  for _ in $(seq 30); do
  	[ -z "$(dns)" ] && break
  	sleep 2
  done
  [ -z "$(dns)" ]
  ok backup

  # The version the engine runs now; the restored database's newest version is below it.
  engine_version=$(api "$API/engines" | jq '[.[].applied_version] | max')
  remote 'docker compose stop mgmt'
  remote 'docker compose exec -T postgres pg_restore -U nexora --clean --if-exists -d nexora < nexora-backup.dump'
  remote 'docker compose start mgmt'
  for _ in $(seq 60); do
  	api -X POST "$API/auth/login" -d "{\"username\":\"admin\",\"password\":\"$password\"}" >/dev/null 2>&1 && break
  	sleep 2
  done
  api "$API/zones/$zone/records?name=www.compose.test." | jq -e '.items[] | select(.name=="www.compose.test.")' >/dev/null
  ok restore

  # docs/operations.md "Engines ahead of a restored database": publish until the newest version is above
  # the engine's. The restored engine row reads "current" until the engine reconnects, so it is not the test.
  for _ in $(seq 5); do
  	[ "$(api "$API/config-versions?limit=1" | jq '.[0].version')" -gt "$engine_version" ] && break
  	remote 'docker compose exec -T postgres psql -U nexora -d nexora -c "update engine_groups set rollouts_paused = true where name = '"'"'default'"'"';"' >/dev/null
  	api -X POST "$API/engine-groups/00000000-0000-0000-0000-000000000001/resume-rollouts" >/dev/null
  done
  [ "$(api "$API/config-versions?limit=1" | jq '.[0].version')" -gt "$engine_version" ]
  for _ in $(seq 60); do
  	api "$API/engines" | jq -e ".[] | select(.connected and .status == \"current\" and .applied_version > $engine_version)" >/dev/null && break
  	sleep 2
  done
  for _ in $(seq 30); do
  	[ "$(dns)" = 192.0.2.10 ] && break
  	sleep 2
  done
  [ "$(dns)" = 192.0.2.10 ]
  ok restored-dns

  cleanup
  trap - EXIT
  ok cleanup
  ```
  As built, the script differs from the first draft where the first runs proved the draft wrong: `crane pull --insecure` (the laptop does not trust the Nexus certificate either); record lists read `.items[]` (`RecordPage`); `otel-logs` greps the collector's `data points` lines, because with `NEXORA_QUERYLOG_BACKEND=builtin` engines send query logs to mgmt and only OTLP metrics and traces reach the collector (now said in `docs/operations.md`); the restore recovery publishes until `GET /config-versions?limit=1` is above the engine's `applied_version` recorded before the restore, then waits for a connected `current` engine above it (the restored engine row reads `current` until the engine reconnects, which made the first draft's check racy); `tar --no-xattrs` and an `ERR` trap that names the failing line.
  Take the exact paths and fields from `mgmt/api/openapi.yaml`: the zone create body (`ZoneCreate`), record list and delete (revision as query or body), the engine list fields, and resume-rollouts. Where the documented command sequence itself is wrong, fix `docs/operations.md` and the script in the same way, and note it in the todo evidence. Never change only the script.
- [ ] Run `scripts/compose-verify.sh piwi@192.168.10.211` from the laptop, after `images.yml` has pushed `sha-<7>` for the commit being verified (while the branch is unpushed, `NEXORA_TAG=sha-<origin/main>` when `deploy/compose`, the Compose sections of `docs/operations.md` and `mgmt/cmd` are unchanged since that commit).
  - Expect to find real failures on the first run.
  - For each failure: fix the compose file or the doc, re-run from the start (the trap leaves the host clean), and record the failure and fix in `.procoder/todo/`.
  - Expected when done: 12 lines `ok pull` … `ok cleanup` and exit 0.
- [ ] Update `docs/operations.md`:
  - replace "The example is checked statically (`TestComposeExample`); it is not started in CI." with "The example is checked statically (`TestComposeExample`) and run for real on a Docker host with `scripts/compose-verify.sh user@host` (last verified on novanas, x86_64, Docker 29.4.1, Compose v5.1.3).";
  - add a Compose variant of the `psql` command in "Engines ahead of a restored database": `docker compose exec -T postgres psql -U nexora -d nexora -c "..."`;
  - add a bullet for hosts that cannot pull from the registry: `crane pull --platform linux/<arch> <registry>/nexora-mgmt:<tag> mgmt.tar` and `docker load < mgmt.tar` (same for the engine), with `NEXORA_REGISTRY` set to the name the images were pulled as.
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./deploy/deploytest/...'` and expect PASS. Run `ssh piwi@192.168.10.211 'docker ps -a --format "{{.Names}}"; ls -d ~/nexora-compose-verify 2>&1'` and expect no `nexora-verify` containers and `No such file or directory`.

## Task 21: Debt marker test and operations limits

Depends on Tasks 3–20.

Files: `deploy/deploytest/debt_test.go` (new), `docs/operations.md` (section "Known limitations")
Interfaces: `TestM7DebtMarkersResolved`.

- [ ] Create `deploy/deploytest/debt_test.go`:
  ```go
  package deploytest

  import (
  	"os"
  	"strings"
  	"testing"
  )

  // TestM7DebtMarkersResolved fails while any v1 debt marker lifted by milestone M7 (#9-#29) is still in the tree.
  func TestM7DebtMarkersResolved(t *testing.T) {
  	for file, marker := range map[string]string{
  		"../../e2e/fixtures/authhier/server.go":              "no wildcard synthesis",
  		"../../e2e/harness/otelcol.go":                       "the port stays free while the collector is down",
  		"../../engine/src/acl.rs":                            "switch to a prefix trie",
  		"../../engine/src/authoritative/dispatch.rs":         "the whole transfer is built on the worker thread",
  		"../../engine/src/control.rs":                        "is retried with backoff instead of discarded",
  		"../../engine/src/recursor/dnssec/anchors.rs":        "a revocation is only accepted when the RRset also verifies",
  		"../../engine/src/recursor/dnssec/nsec_cache.rs":     "denial records kept per zone",
  		"../../engine/src/recursor/infra.rs":                 "read-modify-write is not atomic",
  		"../../engine/src/recursor/iterate.rs":               "fixed capacities (entries)",
  		"../../engine/src/recursor/rpz/transfer.rs":          "one string key per zone record per difference",
  		"../../engine/src/server/doq.rs":                     "one stream buffer, answer buffer and frame per DoQ query",
  		"../../engine/src/server/mod.rs":                     "answered as version 0 instead of",
  		"../../engine/src/server/stream.rs":                  "one query buffer, one 64 KiB answer buffer",
  		"../../engine/src/telemetry/otlp.rs":                 "one batch export at a time",
  		"../../mgmt/internal/control/server.go":              "the whole blob is read into memory",
  		"../../mgmt/internal/querylog/opensearch.go":         "@timestamp has millisecond resolution",
  		"../../mgmt/internal/secrets/pkcs11.go":              "a session invalidated by the token",
  		"../../mgmt/internal/zone/import.go":                 "loads every record into memory",
  		"../../mgmt/internal/zone/service.go":                "loads the whole zone per record edit",
  	} {
  		raw, err := os.ReadFile(file)
  		if err != nil {
  			t.Errorf("%s: %v", file, err)
  			continue
  		}
  		if strings.Contains(string(raw), marker) {
  			t.Errorf("%s still carries the debt marker %q", file, marker)
  		}
  	}
  	otlp, _ := os.ReadFile("../../engine/src/telemetry/otlp.rs")
  	control, _ := os.ReadFile("../../engine/src/control.rs")
  	for _, gone := range []struct{ text, marker string }{
  		{string(otlp), "the index is resolved against the runtime current at drain time"},
  		{string(control), "an identity that cannot be stored is re-enrolled"},
  	} {
  		if strings.Contains(gone.text, gone.marker) {
  			t.Errorf("debt marker %q remains", gone.marker)
  		}
  	}
  	build, _ := os.ReadFile("../../mgmt/internal/zone/build.go")
  	if !strings.Contains(string(build), "debt: DNSSEC-signed zones still rebuild") {
  		t.Error("the signed-zone remainder of #29 must be recorded as a debt marker in mgmt/internal/zone/build.go")
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./deploy/deploytest -run TestM7DebtMarkersResolved'`. Expect PASS once Tasks 3–20 are in. If a marker remains, the task that owns the file is not done: send it back rather than editing that file here.
- [ ] In `docs/operations.md` "Known limitations":
  - remove entries these tasks lifted, if listed (zone export memory, per-edit zone loads for unsigned zones, collector drop behaviour);
  - add: "Record edits of DNSSEC-signed zones still rebuild from the whole zone per edit.";
  - add: "The OpenSearch query log sorts by `_id`, which needs `indices.id_field_data.enabled` (the default).";
  - add: "The recursor cache budget is `recursor_cache_max_bytes` (default 64 MiB) under Forwarding & recursion.".
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./deploy/deploytest/...'` and expect PASS.
- [ ] (Lead note from Task 14) In `docs/operations.md` Known limitations / OpenSearch, state that query-log paging sorts by `_id`, which requires `indices.id_field_data.enabled` (true by default in OpenSearch 3.x); with it disabled, searches fail with HTTP 400.

## Task 22: Workflows proven on the ARC runners (#3)

Depends on Tasks 1–21 committed and pushed to `main`.

Files: `.github/workflows/ci.yml`, `.github/workflows/images.yml`, `.github/workflows/fuzz.yml`, `.github/workflows/perf-gate.yml`, edited only when a run is red because of the workflow.
Interfaces: none.

- [ ] Run `gh run list -R azrtydxb/nexora --workflow ci.yml --commit "$(git rev-parse HEAD)"` and `gh run list -R azrtydxb/nexora --workflow images.yml --commit "$(git rev-parse HEAD)"`; wait for completion with `gh run watch <id> -R azrtydxb/nexora --exit-status`. Expect `success` for both. A red job caused by M7 code goes back to the owning task. A red job caused by the workflow (runner image, Nexus certificate, cache path) is fixed in the workflow, pushed, and proven with a new green run.
- [ ] Run `gh run list -R azrtydxb/nexora --workflow fuzz.yml --event schedule --limit 1 --json conclusion,url,createdAt`. The 02:17 UTC nightly run must be a completed `success` after the M7 head was pushed. If none exists yet, wait for the next schedule rather than dispatching, because the criterion is the nightly run. If the run found a crash:
  1. download the `fuzz-artifacts` artifact;
  2. add the input to `engine/fuzz/corpus/parse_query/`;
  3. add a unit test in `engine/src/wire.rs` that fails on it;
  4. fix the parser;
  5. wait for the next nightly `success`.
- [ ] Run `gh workflow run perf-gate.yml -R azrtydxb/nexora --ref main`, then `gh run watch <id> -R azrtydxb/nexora`.
  - Expect the `relative` job to run on `arc-azrtydxb-amd64` through "Build base and head engines", both 9-round steps and the artifact upload. `gh run view <id> -R azrtydxb/nexora --json jobs` shows each step `completed`, and `gh run download <id> -n perf-relative` holds 18 QPS and 18 filter JSON files.
  - Post the two gate outputs (median ratios) as a comment on #6 with the run URL.
  - A non-zero compare step on this A/A run is #6's finding, not a #3 failure. Any other failed step is fixed here.
- [ ] Run `gh run list -R azrtydxb/nexora --workflow perf-gate.yml --event schedule --limit 1 --json conclusion,url`. Expect `success` (the relative job is skipped on schedule, and the absolute job is skipped without `NEXORA_REFERENCE_HOST`).
- [ ] Record every run URL in `.procoder/todo/` for the closing comment of #3.

## Task 23: Milestone verification, kw deployment and issue closing

Depends on Tasks 1–22.

Files: none; this task touches only `.procoder/todo/` evidence.
Interfaces: none.

- [ ] Run the full local suites in the dev pod and expect every one to pass. `TestGUICoverage` and `TestKeyStorageBackends` are included in the e2e run.
  ```sh
  scripts/dev-exec.sh 'cargo fmt --all -- --check && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings && cargo test --locked -p nexora-engine --all-targets && cargo test --locked --release -p nexora-engine --test filter_index_budget'
  scripts/dev-exec.sh 'gofmt -l mgmt e2e bench deploy && go vet ./... && go test -race -count=1 ./mgmt/... ./gen/... ./bench/... ./deploy/...'
  scripts/dev-exec.sh 'make web-test && cd web && pnpm lint'
  scripts/dev-exec.sh 'make e2e-build && go test -count=1 -timeout 180m ./e2e/...'
  ```
- [ ] Deploy to kw per the roadmap:
  1. `scripts/kw-deploy.sh` with the DNS probe (5 queries/s each to 192.168.10.136 and 192.168.10.139);
  2. expect zero lost queries;
  3. `scripts/kw-acceptance.sh` and expect `TestKwSmoke`, `TestKwFullProduct` and `TestKwFilterCategories` to pass.
  - On any lost query or failed test, run `helm rollback nexora`, record why in `.procoder/notes/plan-review.md`, and fix before retrying.
- [ ] Re-run `scripts/compose-verify.sh piwi@192.168.10.211` with `NEXORA_TAG` set to the deployed image tag and expect exit 0.
- [ ] Close the issues with a comment naming the commit and proof:
  - #3: run URLs from Task 22;
  - #5: the `compose-verify.sh` output and the doc fixes;
  - #9–#29: the test names from the spec's acceptance criteria;
  - #29's comment also states the signed-zone remainder and its reason.
  - Use `gh issue close N -R azrtydxb/nexora -c "<comment>"`.
- [ ] Run `gh issue list -R azrtydxb/nexora --state open --limit 200 --json number -q '.[].number'` and expect none of 3, 5, 9–29 listed. Set `Status: implemented` in this plan.
