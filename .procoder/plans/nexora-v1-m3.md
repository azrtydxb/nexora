# nexora-v1-m3 — implementation plan

Status: draft
Spec: .procoder/specs/nexora-v1.md

## Goal

Ship milestone M3 "Recursion + validation": full iterative recursion from root hints (S-2), DNSSEC validation of recursive and forwarded answers (S-7), and RPZ policy zones loaded from file or AXFR/IXFR (S-9), wired into the engine query path, the management-plane API, the GUI (`/rpz`, `/dnssec`, resolution settings on `/upstreams`), metrics, the kw deployment, and the acceptance tests `TestRecursionRootHints`, `TestSpoofedReplyRejected`, `TestDNSSECValidation`, `TestRPZPolicy`.

## Architecture

All engine code for M3 lives under `engine/src/recursor/` (per docs/architecture.md): a transport (`transport.rs`: random ID, random source port, 0x20, strict reply matching, TCP on TC, EDNS0 1232 + DO), an infrastructure cache (`infra.rs`), an RRset cache (`rrcache.rs`), root hints (`roothints.rs`), a work budget (`budget.rs`), the iterative resolver (`iterate.rs`), mode/route selection and the miss-path entry point (`dispatch.rs`), DNSSEC (`dnssec/{verify,denial,validator,anchors,nsec_cache}.rs`) and RPZ (`rpz/{parse,index,apply,transfer,tsig}.rs`). hickory-proto 0.26.3 (`dnssec-ring`) is used only as the message codec, zone-text parser and signature primitive (`Verifier::verify_rrsig`, `DS::covers`, `Nsec3HashAlgorithm::hash`); all resolution, chain-of-trust, denial-proof, RFC 5011, RPZ and TSIG logic is Nexora code.

Process-wide state that must survive snapshot swaps (infrastructure cache, RRset cache, key cache, trust-anchor store, RPZ zone data) lives in one `recursor::RecursorState` created in `main.rs` and shared by `Arc` with every worker; per-snapshot settings live in `runtime::Runtime.resolution: recursor::dispatch::ResolutionRuntime`, swapped atomically with the rest of the runtime. The M1 miss task (the `spawn_local` task in `src/server/mod.rs`) calls `recursor::dispatch::resolve_miss`, which routes to a forward zone, to recursion, or to the M1 upstreams, validates, applies RPZ response-phase triggers, and returns response wire bytes for the M1 cache/serve code. RPZ QNAME and CLIENT-IP triggers run before the cache lookup on the worker (no allocation when nothing matches).

The management plane adds migrations `0300`–`0302`, OpenAPI operations for resolution settings, forward zones, DNSSEC settings/trust anchors/NTAs/status and RPZ zones, snapshot-builder code filling the new `ConfigSnapshot` fields, and ingestion of the new `Stats` fields. The e2e suite never touches the internet: `nexora-fixture authhier` serves a private hierarchy (fake root `.`, `test.`, leaf zones, signed with miekg/dns under a per-run test trust anchor, plus a spoofing server and a validating-forwarder endpoint) on loopback addresses `127.0.53.0/24` sharing one random port, which the engine reaches through the snapshot's root-hint override and `authority_port`; a BIND `named` child process serves an RPZ zone over AXFR/IXFR with TSIG.

Interfaces consumed from M1/M2 (names chosen by those plans; this plan uses exactly these and nothing else):

- Rust: `crate::pb` (prost/tonic module for `nexora.control.v1`); `runtime::Runtime` (built in `Runtime::build(snapshot: &pb::ConfigSnapshot, blobs: &control::BlobStore) -> Result<Runtime, String>`); `wire::NameKey::as_wire(&self) -> &[u8]` (lowercase uncompressed wire name); `cache::CachedResponse`; `upstream::Upstreams` with `async fn query(&self, query_wire: &[u8]) -> Result<Vec<u8>, UpstreamError>`; `control::BlobStore::get(&self, sha256_hex: &str) -> Result<Arc<[u8]>, String>`; `snapshot::validate(s: &pb::ConfigSnapshot, applied: u64) -> Result<(), String>`; `edns.rs` OPT writer `write_opt`; `telemetry::metrics::render(out: &mut String)`; `telemetry::querylog::QueryRecord`.
- Go: `gen/go/nexora/control/v1` package `controlv1`; `mgmt/internal/snapshot.Build(ctx, tx pgx.Tx) (*controlv1.ConfigSnapshot, error)`; `mgmt/internal/store` blob table `blobs(sha256 text primary key, data bytea)` with `store.PutBlob(ctx, tx pgx.Tx, compressed []byte) (string, error)`; `mgmt/internal/api.Server` strict handlers; `mgmt/internal/auth/permissions.go` map `Permissions map[string]Role`; `mgmt/internal/audit.Record(ctx, tx, actor, action string, before, after any) error`; `mgmt/internal/control.Hub.OnStats(func(engineID uuid.UUID, s *controlv1.Stats))`; e2e `harness.New(t) *harness.Env`, `(*Env).StartMgmt() *harness.Mgmt`, `(*Mgmt).Admin() *harness.APIClient`, `(*APIClient).JSON(t, method, path string, body, out any, wantStatus int)`, `(*Env).StartEngine(m *harness.Mgmt) *harness.Engine`, `(*Engine).DNSAddr() string`, `(*Engine).Metric(t, name string, labels map[string]string) float64`, `(*Mgmt).WaitApplied(t, e *harness.Engine, timeout time.Duration)`, `(*Env).BinPath(name string) string`, `(*Env).TempDir() string`, `harness.FreePort(t) int`; `nexora-fixture` dispatches subcommands through `var subcommands = map[string]func(args []string) error{}` in `e2e/fixtures/cmd/nexora-fixture/main.go`; Playwright annotation `test.info().annotations.push({ type: 'operation', description: '<operationId>' })` consumed by `TestGUICoverage`; Playwright fixture module `web/e2e/fixtures.ts` exporting `test` (fixtures `adminPage`, `viewerPage`, `api`) and `expect`; `github.com/klauspost/compress/zstd` already in `go.mod`; engine crates `quick_cache`, `arc-swap`, `rustc-hash`, `ipnet`, `parking_lot`, `rand`, `futures`, `zstd`, `serde`, `tempfile` (dev) — Task 1 adds to `engine/Cargo.toml` any of these M1 did not already declare.

Decisions made in this plan that docs/architecture.md does not settle:

1. Snapshot field numbers 100–104 (`ConfigSnapshot`) and 100–102 (`Stats`); enum value names prefixed per buf lint.
2. `RecursionConfig.authority_port` (default 53) lets the private e2e hierarchy run on loopback without binding port 53; root-hint override is `RecursionConfig.root_hints`.
3. Forward zones carry their own `ip:port` servers (UDP with TCP fallback via the recursor transport) and a per-zone `validate` flag (default off, so internal zones below signed public names do not go bogus).
4. Resolution deadline 4000 ms; unknown-server RTO 376 ms; RFC 6298 RTO clamp 50–3000 ms; backoff after 3 consecutive timeouts `5 s << (n-3)` capped at 300 s; lame marks 900 s; server choice random within 400 ms of the best RTO; glueless NS resolution capped at 3 names per cut.
5. 0x20 is strict: a case-mismatched reply is dropped, never retried without 0x20. Source ports are chosen randomly in 1024–65535 in user space before `connect`.
6. QNAME minimisation uses QTYPE A for hidden labels and falls back to the full name on NXDOMAIN for an intermediate name (relaxed RFC 9156 mode).
7. DNSSEC: signature skew = 10% of the RRSIG lifetime clamped to 300–3600 s; algorithms exactly {8, 13, 14, 15} (RSASHA512 and others → insecure); DS digests SHA-1/256/384 with SHA-1 ignored when a stronger digest exists; NSEC3 iterations > 50 → insecure (EDE 27), > 150 → bogus.
8. CD=1 skips validation entirely; AD is set only when the query had DO or AD; the cached wire keeps AD and the serve path clears it for clients without DO/AD.
9. Trust anchors from mgmt are DS records; IANA KSK-2017 (20326) and KSK-2024 (38696) are seeded by migration; RFC 5011 state lives in `state_dir/trust-anchors.json`; 5011-learned `Valid` keys survive removal of the mgmt DS; the GUI raises a rollover alarm after 72 h without a successful refresh or with no usable root key.
10. NTAs require an expiry at most 30 days ahead; expired NTAs are deleted by a mgmt job so a new config version is pushed.
11. RPZ runs after Nexora's blocklists/policies/rewrites; RPZ rewrites always apply (no "break-dnssec" exception) and clear AD; NSDNAME/NSIP triggers only fire in recursive mode; policy-rewritten answers are not cached; one `policy_generation` counter (bumped by RPZ publications and by resolution/DNSSEC snapshot changes) invalidates cached answers.
12. RPZ EDE codes: 15 (Blocked) for NXDOMAIN/NODATA, 4 (Forged Answer) for local data.
13. RPZ file zones are zstd-compressed zone text blobs validated by mgmt with miekg/dns (`$INCLUDE` refused on both sides); transfer zones persist the last good copy as `state_dir/rpz/<id>.zone`; an expired zone is marked stale but keeps serving.
14. TSIG (HMAC-SHA256/512) is implemented in Nexora code over `ring::hmac`; in M3 the TSIG secret is stored in `rpz_zones.tsig_secret` (write-only in the API, redacted in audit) and stripped from the engine's on-disk snapshot; M4's KEK storage (S-23) takes it over.
15. Resolution settings and forward zones are managed on the existing `/upstreams` screen; RPZ zone order is an explicit `position` with a reorder operation.
16. Migrations numbered `0300`–`0302`; per-engine DNSSEC/RPZ status is upserted into `engine_dnssec_status` / `engine_rpz_status` from every `Stats` message.
17. The e2e hierarchy runs on `127.0.53.0/24` from `nexora-fixture authhier` (miekg/dns signing, ECDSA P-256), with a spoofing server and a validating-forwarder endpoint; the RPZ primary is a real BIND `named` child process with TSIG and `ixfr-from-differences`.
18. kw: engine `state_dir` on a `hostPath` per node so RFC 5011 and RPZ state survive restarts; smoke subtests live in `e2e/kwsmoke` behind the `kwsmoke` build tag.

## Constraints

Copied verbatim from `.procoder/specs/nexora-v1.md` and `docs/architecture.md`:

- DNS engine: Rust, hickory-proto used as wire codec only; server loop, cache and resolver are Nexora code.
- Platform: engine targets Linux only (free to use SO_REUSEPORT, recvmmsg, io_uring and other Linux-specific APIs). Management plane and GUI ship as Linux builds/containers.
- Management plane: Go.
- GUI: React on Vite (not Next.js).
- Management plane is stateless: all state in PostgreSQL, any number of instances behind a load balancer, engines may connect to any instance.
- Security: DNS upstream queries use random transaction IDs and random source ports and match responses on address + ID + question (fixes dns-c's poisoning hole); resolvers refuse recursion to clients outside configured ACLs by default; no default passwords (first admin set at install).
- The query path never logs synchronously, never touches a database, and never performs per-packet heap allocation on the cache-hit path.
- **Engine local state:** last applied config snapshot on disk (without key material); in-memory cache; DNSSEC trust anchors.
- **Name case:** cache and policy keys are case-insensitive; 0x20-randomised queries get the client's casing back.
- **CNAME/DNAME chains:** followed to a bounded depth; loops → SERVFAIL.
- **DNSSEC:** unsupported algorithms → insecure, not bogus; clock skew near signature validity edges; NSEC3 with excessive iterations treated per RFC 9276.
- **Root/authoritative servers unreachable or lame:** marked in an infrastructure cache with backoff; recursion tries remaining servers.
- **Blocklist or RPZ source fetch fails:** keep last good version, surface staleness in GUI and metrics.
- **Trust anchor rollover:** RFC 5011 automated updates; failure alerts before the old anchor expires.
- Every test that asserts "does not happen" first asserts the positive path in the same run, so a harness failure cannot pass a negative check.
- No logging, no allocation, no locks held across packets on the cache-hit path. Enforced by `cache_hit_path_does_not_allocate` (counting allocator).
- Config is read through `ArcSwap<Runtime>::load()` once per packet.
- The engine is Linux-only. All builds and tests run in the dev pod on kw (`deploy/dev/`), image `192.168.10.131/azrtydxb/nexora-dev:<tag>`. Source is synced with `scripts/dev-sync.sh` (rsync over `kubectl exec`), commands run with `scripts/dev-exec.sh <cmd>`.
- HTTP API: `/api/v1`, OpenAPI 3.1 at `mgmt/api/openapi.yaml`, JSON errors `{"code": "...", "message": "..."}`. Editable resources carry `revision`; a stale revision returns 409 `conflict`.
- Every config mutation runs in one transaction: change rows -> write audit row -> build snapshot -> insert `config_versions` -> `pg_notify('nexora_config', version)`.
- Roles: `viewer` (read everything except users, tokens, audit), `operator` (+ DNS configuration mutations), `admin` (everything). Permissions keyed by OpenAPI operationId in `mgmt/internal/auth/permissions.go`.
- Pinned versions for this milestone: Rust 1.97, hickory-proto 0.26.3 with features `dnssec-ring`, ring 0.17.14, Go 1.27, miekg/dns v1.1.73.

Plan-wide conventions (decided here):

- Every command step is run as `scripts/dev-sync.sh && scripts/dev-exec.sh <cmd>` from the repository root on the laptop.
- New `ConfigSnapshot` fields use numbers 100–104 and new `Stats` fields use numbers 100–102, so they can never collide with M1/M2 numbering (which stays below 100).
- Resolution deadline for one client query in recursive mode: 4000 ms (`recursor::RESOLUTION_DEADLINE`). Outgoing EDNS buffer: 1232 (`recursor::EDNS_BUFFER`). CNAME/DNAME depth: 16 (`recursor::MAX_CNAME_DEPTH`).
- Migrations for M3 are numbered `0300_…`–`0302_…`.

## Task 1: Proto contract and engine snapshot validation for M3 fields

Files:

- `proto/nexora/control/v1/control.proto` — new messages/enums, `ConfigSnapshot` fields 100–104, `Stats` fields 100–102.
- `gen/go/nexora/control/v1/control.pb.go`, `gen/go/nexora/control/v1/control_grpc.pb.go` — regenerated by `make proto`.
- `engine/src/snapshot_m3.rs` — `validate_m3` (validation of the new fields only).
- `engine/src/snapshot.rs` — calls `validate_m3` from `validate`.
- `engine/src/lib.rs` — `pub mod snapshot_m3;` and `pub mod recursor;`.
- `engine/src/recursor/mod.rs` — module declarations and M3 constants.
- `engine/Cargo.toml` — `hickory-proto = { version = "=0.26.3", features = ["dnssec-ring"] }`, `ring = "=0.17.14"`, `data-encoding = "2"`, `serde_json = "1"`.

Interfaces:

- `pub fn snapshot_m3::validate_m3(s: &pb::ConfigSnapshot) -> Result<(), String>`
- `pub fn snapshot_m3::parse_ds(text: &str) -> Result<(u16, u8, u8, Vec<u8>), String>` — `"<key tag> <algorithm> <digest type> <hex digest>"`.
- `pub fn snapshot_m3::strip_key_material(s: &mut pb::ConfigSnapshot)` — clears every `RpzTransferSource.tsig_secret` before `snapshot.binpb` is written.
- `recursor::{RESOLUTION_DEADLINE: Duration = 4000 ms, EDNS_BUFFER: u16 = 1232, MAX_CNAME_DEPTH: u8 = 16, DEFAULT_MAX_UPSTREAM_QUERIES: u32 = 100, DEFAULT_MAX_DELEGATION_DEPTH: u32 = 32}`
- Proto (literal, appended to `control.proto`):

```proto
// ---- M3: recursion, DNSSEC validation, RPZ. Field numbers >= 100. ----

enum ResolutionMode {
  RESOLUTION_MODE_UNSPECIFIED = 0; // engine treats as FORWARD
  RESOLUTION_MODE_FORWARD = 1;
  RESOLUTION_MODE_RECURSIVE = 2;
}

message RootHint {
  string name = 1;               // e.g. "a.root-servers.net."
  repeated string addresses = 2; // IPv4 or IPv6 literals, no port
}

message RecursionConfig {
  repeated RootHint root_hints = 1; // empty: compiled-in IANA hints
  bool qname_minimisation = 2;      // RFC 9156
  bool aggressive_nsec = 3;         // RFC 8198, off by default
  uint32 max_upstream_queries = 4;  // 0 -> 100; valid 1..=1000
  uint32 max_delegation_depth = 5;  // 0 -> 32; valid 1..=64
  uint32 authority_port = 6;        // 0 -> 53; port used for every authoritative server
}

message ForwardZone {
  string domain = 1;             // "corp.example."
  repeated string addresses = 2; // "ip:port", UDP with TCP fallback on TC
  bool validate = 3;             // false: answers under this zone are treated as insecure
}

message TrustAnchor {
  string zone = 1; // "."
  string ds = 2;   // "20326 8 2 E06D44B8..."
}

message NegativeTrustAnchor {
  string domain = 1;
  int64 expires_unix = 2;
}

message DnssecConfig {
  bool validation = 1;
  repeated TrustAnchor trust_anchors = 2;
  repeated NegativeTrustAnchor negative_trust_anchors = 3;
  bool rfc5011 = 4;
}

enum RpzPolicyOverride {
  RPZ_POLICY_OVERRIDE_GIVEN = 0;
  RPZ_POLICY_OVERRIDE_DISABLED = 1; // match counted, no action
  RPZ_POLICY_OVERRIDE_NXDOMAIN = 2;
  RPZ_POLICY_OVERRIDE_NODATA = 3;
  RPZ_POLICY_OVERRIDE_PASSTHRU = 4;
  RPZ_POLICY_OVERRIDE_DROP = 5;
  RPZ_POLICY_OVERRIDE_TCP_ONLY = 6;
}

enum TsigAlgorithm {
  TSIG_ALGORITHM_NONE = 0;
  TSIG_ALGORITHM_HMAC_SHA256 = 1;
  TSIG_ALGORITHM_HMAC_SHA512 = 2;
}

message RpzFileSource {
  string blob_sha256 = 1; // zstd-compressed zone text
}

message RpzTransferSource {
  string primary = 1; // "ip:port"
  string tsig_key_name = 2;
  TsigAlgorithm tsig_algorithm = 3;
  bytes tsig_secret = 4;           // key material: never persisted by the engine
  uint32 min_refresh_seconds = 5;  // 0 -> 60
}

message RpzZone {
  string id = 1;   // UUID
  string name = 2; // zone origin, "rpz.example."
  oneof source {
    RpzFileSource file = 3;
    RpzTransferSource transfer = 4;
  }
  RpzPolicyOverride policy_override = 5;
  uint64 refresh_nonce = 6; // incremented by POST /rpz-zones/{id}/refresh
}

message RecursionStats {
  uint64 upstream_queries = 1;
  uint64 mismatched_replies = 2;
  uint64 tcp_fallbacks = 3;
  uint64 work_limit_exceeded = 4;
  uint32 infra_entries = 5;
  uint64 lame_marked = 6;
}

enum TrustAnchorState {
  TRUST_ANCHOR_STATE_UNSPECIFIED = 0;
  TRUST_ANCHOR_STATE_CONFIGURED = 1;
  TRUST_ANCHOR_STATE_ADD_PEND = 2;
  TRUST_ANCHOR_STATE_VALID = 3;
  TRUST_ANCHOR_STATE_MISSING = 4;
  TRUST_ANCHOR_STATE_REVOKED = 5;
}

message TrustAnchorStatus {
  string zone = 1;
  uint32 key_tag = 2;
  uint32 algorithm = 3;
  TrustAnchorState state = 4;
  int64 last_refresh_success_unix = 5;
  int64 hold_down_until_unix = 6;
  string last_error = 7;
}

message DnssecStats {
  uint64 secure = 1;
  uint64 insecure = 2;
  uint64 bogus = 3;
  uint64 indeterminate = 4;
  repeated TrustAnchorStatus trust_anchors = 5;
  uint32 active_negative_trust_anchors = 6;
}

message RpzZoneStatus {
  string id = 1;
  uint32 serial = 2;
  uint64 records = 3;
  uint64 skipped = 4;
  uint64 hits = 5;
  int64 last_success_unix = 6;
  string last_error = 7;
  bool stale = 8;
}
```

and, inside the existing messages:

```proto
message ConfigSnapshot {
  // ... M1/M2 fields (< 100) unchanged ...
  ResolutionMode resolution_mode = 100;
  RecursionConfig recursion = 101;
  repeated ForwardZone forward_zones = 102;
  DnssecConfig dnssec = 103;
  repeated RpzZone rpz_zones = 104; // ordered: index 0 has the highest precedence
}

message Stats {
  // ... M1/M2 fields (< 100) unchanged ...
  RecursionStats recursion = 100;
  DnssecStats dnssec = 101;
  repeated RpzZoneStatus rpz_zones = 102;
}
```

- [ ] Create `engine/src/snapshot_m3.rs` containing only the test module below (and add `pub mod snapshot_m3;` to `engine/src/lib.rs`):

```rust
#[cfg(test)]
mod tests {
    use super::*;
    use crate::pb::{
        rpz_zone::Source, ConfigSnapshot, DnssecConfig, ForwardZone, NegativeTrustAnchor,
        RecursionConfig, RootHint, RpzFileSource, RpzTransferSource, RpzZone, TrustAnchor,
        TsigAlgorithm,
    };

    fn ok_snapshot() -> ConfigSnapshot {
        ConfigSnapshot {
            resolution_mode: crate::pb::ResolutionMode::Recursive as i32,
            recursion: Some(RecursionConfig {
                root_hints: vec![RootHint { name: "a.root.test.".into(), addresses: vec!["127.0.53.1".into(), "::1".into()] }],
                qname_minimisation: true,
                aggressive_nsec: false,
                max_upstream_queries: 100,
                max_delegation_depth: 32,
                authority_port: 5353,
            }),
            forward_zones: vec![ForwardZone { domain: "corp.example.".into(), addresses: vec!["10.0.0.1:53".into()], validate: false }],
            dnssec: Some(DnssecConfig {
                validation: true,
                trust_anchors: vec![TrustAnchor { zone: ".".into(), ds: "20326 8 2 E06D44B80B8F1D39A95C0B0D7C65D08458E880409BBC683457104237C7F8EC8D".into() }],
                negative_trust_anchors: vec![NegativeTrustAnchor { domain: "broken.example.".into(), expires_unix: 4_000_000_000 }],
                rfc5011: true,
            }),
            rpz_zones: vec![
                RpzZone { id: "11111111-1111-1111-1111-111111111111".into(), name: "rpz.file.".into(), source: Some(Source::File(RpzFileSource { blob_sha256: "a".repeat(64) })), policy_override: 0, refresh_nonce: 0 },
                RpzZone { id: "22222222-2222-2222-2222-222222222222".into(), name: "rpz.axfr.".into(), source: Some(Source::Transfer(RpzTransferSource { primary: "127.0.0.1:5300".into(), tsig_key_name: "rpz-key.".into(), tsig_algorithm: TsigAlgorithm::HmacSha256 as i32, tsig_secret: vec![7u8; 32], min_refresh_seconds: 0 })), policy_override: 0, refresh_nonce: 0 },
            ],
            ..Default::default()
        }
    }

    #[test]
    fn accepts_valid_m3_fields() {
        assert_eq!(validate_m3(&ok_snapshot()), Ok(()));
    }

    #[test]
    fn rejects_bad_root_hint_address() {
        let mut s = ok_snapshot();
        s.recursion.as_mut().unwrap().root_hints[0].addresses[0] = "127.0.53.1:53".into();
        assert_eq!(validate_m3(&s).unwrap_err(), "recursion.root_hints[0].addresses[0]: not an IP address: 127.0.53.1:53");
    }

    #[test]
    fn rejects_limits_out_of_range() {
        let mut s = ok_snapshot();
        s.recursion.as_mut().unwrap().max_upstream_queries = 1001;
        assert_eq!(validate_m3(&s).unwrap_err(), "recursion.max_upstream_queries: 1001 not in 1..=1000");
        let mut s = ok_snapshot();
        s.recursion.as_mut().unwrap().max_delegation_depth = 65;
        assert_eq!(validate_m3(&s).unwrap_err(), "recursion.max_delegation_depth: 65 not in 1..=64");
    }

    #[test]
    fn rejects_duplicate_forward_zone() {
        let mut s = ok_snapshot();
        s.forward_zones.push(ForwardZone { domain: "CORP.example.".into(), addresses: vec!["10.0.0.2:53".into()], validate: false });
        assert_eq!(validate_m3(&s).unwrap_err(), "forward_zones[1].domain: duplicate corp.example.");
    }

    #[test]
    fn rejects_forward_zone_without_port() {
        let mut s = ok_snapshot();
        s.forward_zones[0].addresses[0] = "10.0.0.1".into();
        assert_eq!(validate_m3(&s).unwrap_err(), "forward_zones[0].addresses[0]: not ip:port: 10.0.0.1");
    }

    #[test]
    fn rejects_malformed_trust_anchor() {
        let mut s = ok_snapshot();
        s.dnssec.as_mut().unwrap().trust_anchors[0].ds = "20326 8 2 ZZ".into();
        assert_eq!(validate_m3(&s).unwrap_err(), "dnssec.trust_anchors[0].ds: digest is not hex");
    }

    #[test]
    fn rejects_rpz_without_source_and_short_tsig_secret() {
        let mut s = ok_snapshot();
        s.rpz_zones[0].source = None;
        assert_eq!(validate_m3(&s).unwrap_err(), "rpz_zones[0]: no source");
        let mut s = ok_snapshot();
        if let Some(Source::Transfer(t)) = s.rpz_zones[1].source.as_mut() { t.tsig_secret = vec![1u8; 8]; }
        assert_eq!(validate_m3(&s).unwrap_err(), "rpz_zones[1].transfer.tsig_secret: must be 16..=64 bytes");
    }

    #[test]
    fn rejects_duplicate_rpz_id() {
        let mut s = ok_snapshot();
        s.rpz_zones[1].id = s.rpz_zones[0].id.clone();
        assert_eq!(validate_m3(&s).unwrap_err(), "rpz_zones[1].id: duplicate 11111111-1111-1111-1111-111111111111");
    }

    #[test]
    fn strip_key_material_clears_tsig_secrets_only() {
        let mut s = ok_snapshot();
        strip_key_material(&mut s);
        match s.rpz_zones[1].source.as_ref().unwrap() {
            Source::Transfer(t) => { assert!(t.tsig_secret.is_empty()); assert_eq!(t.tsig_key_name, "rpz-key."); }
            Source::File(_) => panic!("wrong source"),
        }
    }
}
```

- [ ] Add the proto definitions above to `proto/nexora/control/v1/control.proto` and regenerate: `scripts/dev-sync.sh && scripts/dev-exec.sh make proto`; expect exit 0 and a diff in `gen/go/nexora/control/v1/control.pb.go` containing `ResolutionMode_RESOLUTION_MODE_RECURSIVE`.
- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib snapshot_m3::tests`; expect FAIL with "cannot find function `validate_m3` in this scope".
- [ ] Implement `validate_m3` in `engine/src/snapshot_m3.rs` above the test module. Rules, checked in this order, each returning the exact error strings asserted above (field path, colon, message):
  - `recursion` (when present): each `root_hints[i].name` parses with `hickory_proto::rr::Name::from_ascii` and is FQDN; each `addresses[j]` parses as `std::net::IpAddr` (`"recursion.root_hints[{i}].addresses[{j}]: not an IP address: {v}"`); `max_upstream_queries` is 0 or 1..=1000; `max_delegation_depth` is 0 or 1..=64; `authority_port` is 0..=65535.
  - `forward_zones[i]`: `domain` parses as FQDN; lowercased duplicates rejected (`"forward_zones[{i}].domain: duplicate {lower}"`); `addresses` non-empty, each parses as `std::net::SocketAddr` (`"forward_zones[{i}].addresses[{j}]: not ip:port: {v}"`).
  - `dnssec.trust_anchors[i]`: `zone` FQDN; `ds` parses with `parse_ds` (four whitespace-separated fields; key tag `u16`; algorithm `u8`; digest type `u8`; digest hex via `data_encoding::HEXUPPER_PERMISSIVE`, error `"digest is not hex"`; digest length 20 for type 1, 32 for type 2, 48 for type 4, error `"digest length {n} does not match digest type {t}"`).
  - `dnssec.negative_trust_anchors[i]`: `domain` FQDN, `expires_unix > 0`.
  - `rpz_zones[i]`: `id` non-empty and unique; `name` FQDN; `source` present (`"rpz_zones[{i}]: no source"`); file: `blob_sha256` is 64 lowercase hex; transfer: `primary` parses as `SocketAddr`; when `tsig_algorithm != NONE`, `tsig_key_name` is FQDN and `tsig_secret` is empty (stripped snapshot loaded from disk) or 16..=64 bytes (`"rpz_zones[{i}].transfer.tsig_secret: must be 16..=64 bytes"`).
- [ ] Implement `strip_key_material` (iterate `rpz_zones`, clear `tsig_secret` of transfer sources) and change `snapshot.rs`: `validate` calls `crate::snapshot_m3::validate_m3(s)?` after the M1 checks, and the persistence function clones the snapshot, calls `strip_key_material` on the clone, and writes the clone.
- [ ] Create `engine/src/recursor/mod.rs` with the constants listed under Interfaces and `pub mod` lines for the submodules added by later tasks, starting with none.
- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib snapshot_m3::tests`; expect PASS (9 tests).
- [ ] Commit: `git add proto gen engine/Cargo.toml engine/src/lib.rs engine/src/snapshot.rs engine/src/snapshot_m3.rs engine/src/recursor/mod.rs && git commit -m "feat(proto): M3 recursion, DNSSEC and RPZ snapshot fields"`.

## Task 2: Outbound transport with spoofing defences and recursor metrics

Files:

- `engine/src/recursor/metrics.rs` — `RecursorMetrics` atomics for every M3 counter.
- `engine/src/recursor/transport.rs` — one query/response exchange with an authoritative server or forward-zone server.
- `engine/src/recursor/transport/tests.rs` — unit tests with in-test fake servers.
- `engine/src/recursor/mod.rs` — `pub mod metrics; pub mod transport;`.

Interfaces:

```rust
// metrics.rs
#[derive(Default)]
pub struct RecursorMetrics {
    pub upstream_queries: AtomicU64,
    pub upstream_timeouts: AtomicU64,
    pub mismatched_id: AtomicU64,
    pub mismatched_question: AtomicU64,
    pub mismatched_case: AtomicU64,
    pub malformed_replies: AtomicU64,
    pub tcp_fallbacks: AtomicU64,
    pub edns_fallbacks: AtomicU64,
    pub lame_marked: AtomicU64,
    pub limit_queries: AtomicU64,
    pub limit_delegation_depth: AtomicU64,
    pub limit_cname_depth: AtomicU64,
    pub cname_loops: AtomicU64,
    pub resolutions_recursive: AtomicU64,
    pub resolutions_forward_zone: AtomicU64,
    pub resolution_failures: AtomicU64,
    pub dnssec_secure: AtomicU64,
    pub dnssec_insecure: AtomicU64,
    pub dnssec_bogus: AtomicU64,
    pub dnssec_indeterminate: AtomicU64,
    pub dnssec_bogus_by_ede: [AtomicU64; 32],
    pub dnssec_aggressive_synthesized: AtomicU64,
    pub trust_anchor_refresh_failures: AtomicU64,
}
impl RecursorMetrics { pub fn inc(c: &AtomicU64) { c.fetch_add(1, Ordering::Relaxed); } }

// transport.rs
pub struct OutboundQuery<'a> {
    pub server: SocketAddr,
    pub qname: &'a Name,
    pub qtype: RecordType,
    pub recursion_desired: bool,  // true only for forward-zone servers
    pub edns: bool,               // OPT with payload EDNS_BUFFER
    pub dnssec_ok: bool,
    pub checking_disabled: bool,
    pub use_0x20: bool,
    pub timeout: Duration,
}
pub struct Exchange { pub message: Message, pub rtt: Duration, pub via_tcp: bool }
#[derive(Debug)]
pub enum ExchangeError { Timeout, Network(std::io::ErrorKind), TcpFailed(String) }
pub struct Transport { metrics: Arc<RecursorMetrics> }
impl Transport {
    pub fn new(metrics: Arc<RecursorMetrics>) -> Self;
    pub async fn exchange(&self, q: &OutboundQuery<'_>) -> Result<Exchange, ExchangeError>;
}
pub fn randomise_case(name: &Name, rng: &mut impl rand::RngCore) -> Name;
pub fn reply_matches(sent_id: u16, sent_name: &Name, sent_type: RecordType, strict_case: bool, reply: &Message) -> Result<(), Mismatch>;
#[derive(Debug, PartialEq)] pub enum Mismatch { Id, Question, Case, NotResponse }
```

- [ ] Create `engine/src/recursor/transport/tests.rs` and declare it from `transport.rs` with `#[cfg(test)] mod tests;` (transport.rs otherwise empty):

```rust
use super::*;
use hickory_proto::op::{Message, MessageType, OpCode, Query};
use hickory_proto::rr::{rdata::A, DNSClass, Name, RData, Record, RecordType};
use std::net::{Ipv4Addr, SocketAddr};
use std::sync::atomic::Ordering;
use std::sync::Arc;
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::{TcpListener, UdpSocket};

fn answer(id: u16, name: &Name, ip: Ipv4Addr, tc: bool) -> Vec<u8> {
    let mut m = Message::response(id, OpCode::Query);
    m.metadata.authoritative = true;
    m.metadata.truncation = tc;
    m.queries.push(Query::query(name.clone(), RecordType::A));
    if !tc {
        m.answers.push(Record::from_rdata(name.clone(), 300, RData::A(A(ip))));
    }
    m.to_vec().unwrap()
}

fn outbound<'a>(server: SocketAddr, name: &'a Name) -> OutboundQuery<'a> {
    OutboundQuery { server, qname: name, qtype: RecordType::A, recursion_desired: false, edns: true, dnssec_ok: true, checking_disabled: false, use_0x20: true, timeout: Duration::from_millis(800) }
}

#[tokio::test(flavor = "current_thread")]
async fn spoofed_replies_are_dropped_and_real_reply_accepted() {
    let server = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let addr = server.local_addr().unwrap();
    tokio::spawn(async move {
        let mut buf = [0u8; 1500];
        let (n, peer) = server.recv_from(&mut buf).await.unwrap();
        let q = Message::from_vec(&buf[..n]).unwrap();
        let id = q.metadata.id;
        let sent = q.queries[0].name().clone();
        // wrong ID
        server.send_to(&answer(id.wrapping_add(1), &sent, Ipv4Addr::new(6, 6, 6, 6), false), peer).await.unwrap();
        // right ID, wrong question
        server.send_to(&answer(id, &Name::from_ascii("evil.example.").unwrap(), Ipv4Addr::new(6, 6, 6, 6), false), peer).await.unwrap();
        // right ID, 0x20 casing not preserved
        server.send_to(&answer(id, &sent.to_lowercase(), Ipv4Addr::new(6, 6, 6, 6), false), peer).await.unwrap();
        // genuine reply
        server.send_to(&answer(id, &sent, Ipv4Addr::new(192, 0, 2, 1), false), peer).await.unwrap();
    });
    let metrics = Arc::new(RecursorMetrics::default());
    let t = Transport::new(metrics.clone());
    let name = Name::from_ascii("www.abcdefghijklmnop.example.").unwrap();
    let ex = t.exchange(&outbound(addr, &name)).await.unwrap();
    assert_eq!(ex.message.answers[0].data, RData::A(A(Ipv4Addr::new(192, 0, 2, 1))));
    assert!(!ex.via_tcp);
    assert_eq!(metrics.mismatched_id.load(Ordering::Relaxed), 1);
    assert_eq!(metrics.mismatched_question.load(Ordering::Relaxed), 1);
    // the all-lowercase spoof differs from the randomised case unless the RNG produced all-lowercase,
    // which for 20 letters happens with probability 2^-20; the test name has 20 letters.
    assert_eq!(metrics.mismatched_case.load(Ordering::Relaxed), 1);
}

#[tokio::test(flavor = "current_thread")]
async fn truncated_udp_reply_retries_over_tcp() {
    let udp = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let addr = udp.local_addr().unwrap();
    let tcp = TcpListener::bind(addr).await.unwrap();
    tokio::spawn(async move {
        let mut buf = [0u8; 1500];
        let (n, peer) = udp.recv_from(&mut buf).await.unwrap();
        let q = Message::from_vec(&buf[..n]).unwrap();
        udp.send_to(&answer(q.metadata.id, q.queries[0].name(), Ipv4Addr::UNSPECIFIED, true), peer).await.unwrap();
    });
    tokio::spawn(async move {
        let (mut s, _) = tcp.accept().await.unwrap();
        let len = s.read_u16().await.unwrap() as usize;
        let mut buf = vec![0u8; len];
        s.read_exact(&mut buf).await.unwrap();
        let q = Message::from_vec(&buf).unwrap();
        let out = answer(q.metadata.id, q.queries[0].name(), Ipv4Addr::new(192, 0, 2, 2), false);
        s.write_u16(out.len() as u16).await.unwrap();
        s.write_all(&out).await.unwrap();
    });
    let metrics = Arc::new(RecursorMetrics::default());
    let name = Name::from_ascii("big.example.").unwrap();
    let ex = Transport::new(metrics.clone()).exchange(&outbound(addr, &name)).await.unwrap();
    assert!(ex.via_tcp);
    assert_eq!(ex.message.answers[0].data, RData::A(A(Ipv4Addr::new(192, 0, 2, 2))));
    assert_eq!(metrics.tcp_fallbacks.load(Ordering::Relaxed), 1);
}

#[tokio::test(flavor = "current_thread")]
async fn query_carries_random_id_edns_1232_do_and_rd_clear() {
    let server = UdpSocket::bind("127.0.0.1:0").await.unwrap();
    let addr = server.local_addr().unwrap();
    let (tx, rx) = tokio::sync::oneshot::channel::<(Message, SocketAddr)>();
    tokio::spawn(async move {
        let mut buf = [0u8; 1500];
        let (n, peer) = server.recv_from(&mut buf).await.unwrap();
        tx.send((Message::from_vec(&buf[..n]).unwrap(), peer)).unwrap();
    });
    let name = Name::from_ascii("example.").unwrap();
    let t = Transport::new(Arc::new(RecursorMetrics::default()));
    let mut q = outbound(addr, &name);
    q.timeout = Duration::from_millis(100);
    let _ = t.exchange(&q).await;
    let (m, peer) = rx.await.unwrap();
    assert_eq!(m.metadata.message_type, MessageType::Query);
    assert!(!m.metadata.recursion_desired);
    let edns = m.edns.expect("OPT present");
    assert_eq!(edns.max_payload(), 1232);
    assert!(edns.flags().dnssec_ok);
    assert_ne!(peer.port(), 0);
    assert_eq!(m.queries[0].query_class(), DNSClass::IN);
}

#[test]
fn reply_matching_rules() {
    let sent = Name::from_ascii("wWw.ExAmple.").unwrap();
    let ok = Message::from_vec(&answer(7, &sent, Ipv4Addr::LOCALHOST, false)).unwrap();
    assert_eq!(reply_matches(7, &sent, RecordType::A, true, &ok), Ok(()));
    assert_eq!(reply_matches(8, &sent, RecordType::A, true, &ok), Err(Mismatch::Id));
    let lower = Message::from_vec(&answer(7, &sent.to_lowercase(), Ipv4Addr::LOCALHOST, false)).unwrap();
    assert_eq!(reply_matches(7, &sent, RecordType::A, true, &lower), Err(Mismatch::Case));
    assert_eq!(reply_matches(7, &sent, RecordType::A, false, &lower), Ok(()));
    assert_eq!(reply_matches(7, &sent, RecordType::AAAA, true, &ok), Err(Mismatch::Question));
}
```

- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib recursor::transport::tests`; expect FAIL with "cannot find type `RecursorMetrics` in this scope".
- [ ] Create `engine/src/recursor/metrics.rs` with the struct above (all fields `AtomicU64`, `Default` derived; `dnssec_bogus_by_ede` default via `std::array::from_fn(|_| AtomicU64::new(0))` in a manual `Default` impl because arrays of 32 atomics do not derive).
- [ ] Implement `transport.rs`:
  - `randomise_case`: for every label byte in `b'a'..=b'z' | b'A'..=b'Z'` draw one bit from `rng.next_u32()` (refilled every 32 bits) and set upper case when 1; rebuild with `Name::from_labels`, keep FQDN.
  - `reply_matches` order: `message_type != Response` → `NotResponse`; `metadata.id != sent_id` → `Id`; `queries.len() != 1`, type, class IN, or `!name.eq_ignore_ascii_case` (compare `to_lowercase()` values) → `Question`; `strict_case && !queries[0].name().eq_case(sent_name)` → `Case`.
  - `exchange`: `let mut rng = rand::rngs::OsRng;` (`rand` CSPRNG per architecture); `id = rng.next_u32() as u16`; `wire_name = if use_0x20 { randomise_case } else { qname.clone() }`; build `Message::new(id, MessageType::Query, OpCode::Query)` with `recursion_desired`, `checking_disabled`, one `Query::query(wire_name, qtype)`, and when `edns` an `Edns::new()` with `set_max_payload(EDNS_BUFFER)` and `set_dnssec_ok(dnssec_ok)`.
  - UDP socket: bind to the unspecified address of the server's family on a random port — up to 10 attempts at `1024 + rng.next_u32() % 64512`, then port 0 — then `connect(server)` so the kernel drops datagrams from any other address/port. Send once; loop `recv` under `tokio::time::timeout_at(start + timeout)`. For each datagram: `< 12` bytes → `malformed_replies += 1`, continue; `Message::from_vec` error → `malformed_replies += 1`, continue; `reply_matches(.., strict_case = use_0x20, ..)` error → increment `mismatched_id` / `mismatched_question` (also for `NotResponse`) / `mismatched_case`, continue; otherwise accept. Deadline expiry → `upstream_timeouts += 1`, `Err(Timeout)`. `upstream_queries += 1` per datagram sent.
  - If the accepted reply has `truncation`: `tcp_fallbacks += 1`; open `TcpStream::connect(server)` under the remaining deadline (minimum 500 ms), send the same query with a fresh random ID prefixed by a big-endian `u16` length, read one length-prefixed reply, apply `reply_matches` (mismatch → `Err(TcpFailed("reply mismatch"))`), return `via_tcp: true`.
  - `rtt` is measured from send to accepted reply.
- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib recursor::transport::tests`; expect PASS (4 tests).
- [ ] Commit: `git add engine/src/recursor && git commit -m "feat(recursor): outbound transport with 0x20, strict matching and TCP fallback"`.

## Task 3: Infrastructure cache, RRset cache, root hints, work budget

Files:

- `engine/src/recursor/infra.rs` — per-server RTT/RTO, timeouts, backoff, EDNS support, lame (server, zone) marks.
- `engine/src/recursor/rrcache.rs` — RRset cache with RFC 2181 credibility ranking and DNSSEC status.
- `engine/src/recursor/roothints.rs` — compiled-in IANA root hints (IPv4 + IPv6) and snapshot override.
- `engine/src/recursor/budget.rs` — per-client-query work limits.
- `engine/src/recursor/infra_tests.rs` — unit tests for all four (declared `#[cfg(test)] mod infra_tests;` in `mod.rs`).
- `engine/src/recursor/mod.rs` — module declarations.

Interfaces:

```rust
// infra.rs
pub struct InfraCache { /* quick_cache::sync::Cache<IpAddr, InfraEntry>, quick_cache::sync::Cache<(IpAddr, Name), u64 /*lame until*/> */ }
#[derive(Clone, Copy, Debug)]
pub struct InfraEntry { pub srtt_ms: f32, pub rttvar_ms: f32, pub rto_ms: u32, pub consecutive_timeouts: u8, pub backoff_until: u64, pub no_edns: bool }
impl InfraCache {
    pub fn new(capacity: usize) -> Self;
    pub fn rto(&self, ip: IpAddr) -> Duration;                       // 376 ms for unknown servers
    pub fn record_rtt(&self, ip: IpAddr, rtt: Duration);
    pub fn record_timeout(&self, ip: IpAddr, now: u64);
    pub fn is_backed_off(&self, ip: IpAddr, now: u64) -> bool;
    pub fn mark_lame(&self, ip: IpAddr, zone: &Name, now: u64);      // 900 s
    pub fn is_lame(&self, ip: IpAddr, zone: &Name, now: u64) -> bool;
    pub fn set_no_edns(&self, ip: IpAddr);
    pub fn no_edns(&self, ip: IpAddr) -> bool;
    pub fn select(&self, candidates: &[IpAddr], zone: &Name, now: u64, rng: &mut impl rand::RngCore) -> Option<IpAddr>;
    pub fn len(&self) -> usize;
}
// rrcache.rs
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub enum Credibility { Additional = 1, Glue = 2, AuthorityNonAa = 3, AnswerNonAa = 4, AuthorityAa = 5, AnswerAa = 6 }
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum DnssecStatus { Unchecked, Secure, Insecure, Bogus }
pub struct CachedRrset { pub records: Vec<Record>, pub rrsigs: Vec<Record>, pub expires_at: u64, pub credibility: Credibility, pub dnssec: DnssecStatus }
pub struct RrCache { /* quick_cache::sync::Cache<(Name, RecordType), Arc<CachedRrset>> keyed by lowercase name */ }
impl RrCache {
    pub fn new(max_entries: usize) -> Self;
    pub fn insert(&self, records: Vec<Record>, rrsigs: Vec<Record>, credibility: Credibility, dnssec: DnssecStatus, now: u64) -> bool; // false when an unexpired higher-credibility entry exists
    pub fn get(&self, name: &Name, rtype: RecordType, now: u64) -> Option<Arc<CachedRrset>>;
    pub fn set_dnssec(&self, name: &Name, rtype: RecordType, status: DnssecStatus);
}
// roothints.rs
pub struct RootHints { pub servers: Vec<(Name, Vec<IpAddr>)> }
impl RootHints { pub fn iana() -> Self; pub fn from_config(hints: &[pb::RootHint]) -> Self; pub fn addresses(&self, ipv6: bool) -> Vec<IpAddr>; }
// budget.rs
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Limit { UpstreamQueries, DelegationDepth, CnameDepth }
pub struct WorkBudget { queries: Cell<u32>, max_queries: u32, max_depth: u32, in_progress: RefCell<Vec<(Name, RecordType)>> }
impl WorkBudget {
    pub fn new(max_queries: u32, max_depth: u32) -> Self;
    pub fn spend_query(&self) -> Result<(), Limit>;
    pub fn check_depth(&self, depth: u32) -> Result<(), Limit>;
    pub fn enter(&self, name: &Name, rtype: RecordType) -> bool; // false = cycle
    pub fn leave(&self, name: &Name, rtype: RecordType);
    pub fn queries_used(&self) -> u32;
}
```

- [ ] Create `engine/src/recursor/infra_tests.rs`:

```rust
use super::budget::{Limit, WorkBudget};
use super::infra::InfraCache;
use super::roothints::RootHints;
use super::rrcache::{Credibility, DnssecStatus, RrCache};
use hickory_proto::rr::{rdata::{A, NS}, Name, RData, Record, RecordType};
use rand::SeedableRng;
use std::net::{IpAddr, Ipv4Addr};
use std::time::Duration;

fn ip(last: u8) -> IpAddr { IpAddr::V4(Ipv4Addr::new(192, 0, 2, last)) }

#[test]
fn rto_follows_rfc6298_smoothing() {
    let c = InfraCache::new(1000);
    assert_eq!(c.rto(ip(1)), Duration::from_millis(376));
    c.record_rtt(ip(1), Duration::from_millis(100)); // srtt 100, rttvar 50 -> rto 300
    assert_eq!(c.rto(ip(1)), Duration::from_millis(300));
    c.record_rtt(ip(1), Duration::from_millis(100)); // rttvar 37.5 -> rto 250
    assert_eq!(c.rto(ip(1)), Duration::from_millis(250));
}

#[test]
fn three_timeouts_back_off_exponentially_and_rto_doubles() {
    let c = InfraCache::new(1000);
    c.record_rtt(ip(2), Duration::from_millis(100));
    c.record_timeout(ip(2), 1000);
    assert_eq!(c.rto(ip(2)), Duration::from_millis(600));
    assert!(!c.is_backed_off(ip(2), 1000));
    c.record_timeout(ip(2), 1000);
    c.record_timeout(ip(2), 1000);
    assert!(c.is_backed_off(ip(2), 1004));
    assert!(!c.is_backed_off(ip(2), 1005));
    c.record_timeout(ip(2), 1005); // 4th consecutive -> 10 s
    assert!(c.is_backed_off(ip(2), 1014));
    assert!(!c.is_backed_off(ip(2), 1015));
    c.record_rtt(ip(2), Duration::from_millis(80));
    assert!(!c.is_backed_off(ip(2), 1005));
}

#[test]
fn lame_marks_are_per_zone_and_expire() {
    let c = InfraCache::new(1000);
    let zone = Name::from_ascii("example.").unwrap();
    c.mark_lame(ip(3), &zone, 100);
    assert!(c.is_lame(ip(3), &zone, 999));
    assert!(!c.is_lame(ip(3), &Name::from_ascii("other.").unwrap(), 101));
    assert!(!c.is_lame(ip(3), &zone, 1000));
}

#[test]
fn select_skips_lame_and_backed_off_but_probes_when_all_are_down() {
    let c = InfraCache::new(1000);
    let zone = Name::from_ascii("example.").unwrap();
    let mut rng = rand::rngs::StdRng::seed_from_u64(1);
    c.mark_lame(ip(4), &zone, 0);
    for _ in 0..3 { c.record_timeout(ip(5), 0); }
    c.record_rtt(ip(6), Duration::from_millis(30));
    for _ in 0..20 { assert_eq!(c.select(&[ip(4), ip(5), ip(6)], &zone, 1, &mut rng), Some(ip(6))); }
    for _ in 0..3 { c.record_timeout(ip(6), 1); }
    assert_eq!(c.select(&[ip(4), ip(5), ip(6)], &zone, 2, &mut rng), Some(ip(5)));
    assert_eq!(c.select(&[ip(4)], &zone, 2, &mut rng), None);
}

#[test]
fn select_picks_randomly_within_400ms_band() {
    let c = InfraCache::new(1000);
    let zone = Name::root();
    c.record_rtt(ip(7), Duration::from_millis(20));
    c.record_rtt(ip(8), Duration::from_millis(60));
    for _ in 0..10 { c.record_rtt(ip(9), Duration::from_millis(900)); }
    let mut rng = rand::rngs::StdRng::seed_from_u64(7);
    let mut seen = std::collections::HashSet::new();
    for _ in 0..200 { seen.insert(c.select(&[ip(7), ip(8), ip(9)], &zone, 0, &mut rng).unwrap()); }
    assert!(seen.contains(&ip(7)) && seen.contains(&ip(8)));
    assert!(!seen.contains(&ip(9)));
}

#[test]
fn rrcache_credibility_ranking_and_expiry() {
    let c = RrCache::new(1000);
    let n = Name::from_ascii("ns.example.").unwrap();
    let glue = vec![Record::from_rdata(n.clone(), 3600, RData::A(A(Ipv4Addr::new(6, 6, 6, 6))))];
    let auth = vec![Record::from_rdata(n.clone(), 60, RData::A(A(Ipv4Addr::new(192, 0, 2, 53))))];
    assert!(c.insert(auth.clone(), vec![], Credibility::AnswerAa, DnssecStatus::Unchecked, 100));
    assert!(!c.insert(glue, vec![], Credibility::Glue, DnssecStatus::Unchecked, 100));
    assert_eq!(c.get(&Name::from_ascii("NS.Example.").unwrap(), RecordType::A, 159).unwrap().records, auth);
    assert!(c.get(&n, RecordType::A, 160).is_none());
    let zone = Name::from_ascii("example.").unwrap();
    let ns = vec![Record::from_rdata(zone.clone(), 0, RData::NS(NS(n.clone())))];
    assert!(!c.insert(ns, vec![], Credibility::AuthorityAa, DnssecStatus::Unchecked, 100), "TTL 0 is never cached");
}

#[test]
fn iana_root_hints_have_13_servers_with_v4_and_v6() {
    let h = RootHints::iana();
    assert_eq!(h.servers.len(), 13);
    assert_eq!(h.addresses(false).len(), 13);
    assert_eq!(h.addresses(true).len(), 26);
    assert!(h.addresses(false).contains(&"198.41.0.4".parse().unwrap()));
    assert!(h.addresses(true).contains(&"2001:7fd::1".parse().unwrap()));
}

#[test]
fn work_budget_limits_queries_depth_and_detects_cycles() {
    let b = WorkBudget::new(3, 2);
    assert_eq!(b.spend_query(), Ok(()));
    assert_eq!(b.spend_query(), Ok(()));
    assert_eq!(b.spend_query(), Ok(()));
    assert_eq!(b.spend_query(), Err(Limit::UpstreamQueries));
    assert_eq!(b.check_depth(2), Ok(()));
    assert_eq!(b.check_depth(3), Err(Limit::DelegationDepth));
    let n = Name::from_ascii("ns.loop.example.").unwrap();
    assert!(b.enter(&n, RecordType::A));
    assert!(!b.enter(&Name::from_ascii("NS.loop.example.").unwrap(), RecordType::A));
    b.leave(&n, RecordType::A);
    assert!(b.enter(&n, RecordType::A));
}
```

- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib recursor::infra_tests`; expect FAIL with "unresolved import `super::budget`".
- [ ] Implement `infra.rs`. RTT update (RFC 6298, α=1/8, β=1/4): first sample `srtt=r, rttvar=r/2`; later `rttvar = 0.75*rttvar + 0.25*|srtt-r|; srtt = 0.875*srtt + 0.125*r`; `rto_ms = clamp(round(srtt + 4*rttvar), 50, 3000)`; a sample resets `consecutive_timeouts=0, backoff_until=0`. Timeout: `rto_ms = min(rto_ms*2, 3000)` (unknown server starts at 376), `consecutive_timeouts += 1`; when `>= 3`, `backoff_until = now + min(5 << (consecutive_timeouts - 3), 300)`. `is_backed_off` is `now < backoff_until`. `select`:

```rust
pub fn select(&self, candidates: &[IpAddr], zone: &Name, now: u64, rng: &mut impl rand::RngCore) -> Option<IpAddr> {
    let usable: Vec<IpAddr> = candidates.iter().copied().filter(|ip| !self.is_lame(*ip, zone, now)).collect();
    let up: Vec<(IpAddr, u32)> = usable.iter().copied().filter(|ip| !self.is_backed_off(*ip, now))
        .map(|ip| (ip, self.rto(ip).as_millis() as u32)).collect();
    if up.is_empty() {
        // every non-lame server is backed off: probe the one whose backoff ends first
        return usable.into_iter().min_by_key(|ip| self.entry(*ip).map(|e| e.backoff_until).unwrap_or(0));
    }
    let best = up.iter().map(|(_, r)| *r).min().unwrap();
    let band: Vec<IpAddr> = up.into_iter().filter(|(_, r)| *r <= best + 400).map(|(ip, _)| ip).collect();
    Some(band[(rng.next_u32() as usize) % band.len()])
}
```

Lame keys store `zone.to_lowercase()`; lame expiry `now + 900`.

- [ ] Implement `rrcache.rs`: key `(records[0].name.to_lowercase(), records[0].record_type())`; `expires_at = now + min(min TTL over records, 86400)`; TTL 0 → not inserted, returns false; insert refuses when the existing entry is unexpired and `existing.credibility > credibility`; `Bogus` entries are stored with `expires_at = now + min(ttl, 60)`; `get` returns `None` after `now >= expires_at` and never returns entries whose credibility is `Additional` or `Glue` when called through `get` (glue is read by `get_glue(name, rtype, now)` which returns any credibility — add that method too).
- [ ] Implement `roothints.rs` with this table (IANA `named.root`, 2024):

```rust
const IANA: [(&str, &str, &str); 13] = [
    ("a.root-servers.net.", "198.41.0.4", "2001:503:ba3e::2:30"),
    ("b.root-servers.net.", "170.247.170.2", "2801:1b8:10::b"),
    ("c.root-servers.net.", "192.33.4.12", "2001:500:2::c"),
    ("d.root-servers.net.", "199.7.91.13", "2001:500:2d::d"),
    ("e.root-servers.net.", "192.203.230.10", "2001:500:a8::e"),
    ("f.root-servers.net.", "192.5.5.241", "2001:500:2f::f"),
    ("g.root-servers.net.", "192.112.36.4", "2001:500:12::d0d"),
    ("h.root-servers.net.", "198.97.190.53", "2001:500:1::53"),
    ("i.root-servers.net.", "192.36.148.17", "2001:7fe::53"),
    ("j.root-servers.net.", "192.58.128.30", "2001:503:c27::2:30"),
    ("k.root-servers.net.", "193.0.14.129", "2001:7fd::1"),
    ("l.root-servers.net.", "199.7.83.42", "2001:500:9f::42"),
    ("m.root-servers.net.", "202.12.27.33", "2001:dc3::35"),
];
```

`addresses(ipv6)` returns all IPv4 addresses plus IPv6 addresses only when `ipv6` is true; `from_config` with an empty slice returns `iana()`.

- [ ] Implement `budget.rs`: `spend_query` increments then errors when the count exceeds `max_queries`; `check_depth(d)` errors when `d > max_depth`; `enter` compares lowercase names and pushes on success.
- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib recursor::infra_tests`; expect PASS (8 tests).
- [ ] Commit: `git add engine/src/recursor && git commit -m "feat(recursor): infrastructure cache, rrset cache, root hints and work budget"`.

## Task 4: Iterative resolver (delegations, bailiwick, glueless NS, CNAME/DNAME, QNAME minimisation, limits)

Files:

- `engine/src/recursor/testnet.rs` — `#[cfg(test)]` fake authoritative servers built from zone text, bound to `127.0.54.x` on one shared port.
- `engine/src/recursor/iterate.rs` — `Recursor` and the iterative algorithm.
- `engine/src/recursor/iterate_tests.rs` — unit tests.
- `engine/src/recursor/mod.rs` — declarations; `RecursorState` skeleton.

Interfaces:

```rust
// iterate.rs
pub struct RecursionParams {
    pub root_hints: Arc<RootHints>,
    pub qname_minimisation: bool,
    pub max_upstream_queries: u32,
    pub max_delegation_depth: u32,
    pub authority_port: u16,
}
impl RecursionParams { pub fn from_config(c: Option<&pb::RecursionConfig>) -> Self; }
#[derive(Debug, Clone)]
pub struct Resolution {
    pub rcode: ResponseCode,
    pub answers: Vec<Record>,      // CNAME/DNAME chain + final RRset, RRSIGs included
    pub authorities: Vec<Record>,  // SOA + NSEC/NSEC3 (+RRSIGs) for negative answers
    pub zone: Name,                // zone cut that produced the final answer
    pub ns_names: Vec<Name>,       // NS set of that zone (RPZ NSDNAME)
    pub ns_addrs: Vec<IpAddr>,     // addresses of that NS set (RPZ NSIP)
}
#[derive(Debug, Clone, PartialEq)]
pub enum RecursionError { Limit(Limit), CnameLoop, NoReachableAuthority, Deadline }
pub struct Recursor { pub transport: Transport, pub infra: InfraCache, pub rrcache: RrCache, pub metrics: Arc<RecursorMetrics>, pub ipv6: AtomicBool }
impl Recursor {
    pub fn new(metrics: Arc<RecursorMetrics>) -> Self;
    pub async fn resolve(&self, qname: &Name, qtype: RecordType, p: &RecursionParams, budget: &WorkBudget) -> Result<Resolution, RecursionError>;
    pub async fn fetch(&self, qname: &Name, qtype: RecordType, p: &RecursionParams, budget: &WorkBudget) -> Result<Resolution, RecursionError>; // no CNAME chasing, used by the validator for DS/DNSKEY
    pub fn detect_ipv6(&self);
}
pub fn in_bailiwick(zone: &Name, owner: &Name) -> bool; // zone.zone_of(owner), case-insensitive
// testnet.rs (cfg(test))
pub struct FakeZone { pub origin: &'static str, pub ip: Ipv4Addr, pub text: String, pub behaviour: Behaviour }
#[derive(Clone, Copy, PartialEq)] pub enum Behaviour { Normal, Silent, Refused }
pub struct FakeNet { pub port: u16, pub queries: Arc<Mutex<Vec<(Ipv4Addr, Name, RecordType)>>> }
impl FakeNet { pub async fn start(zones: Vec<FakeZone>) -> FakeNet; pub fn params(&self, root_ip: Ipv4Addr) -> RecursionParams; pub fn queries_to(&self, ip: Ipv4Addr) -> usize; }
```

- [ ] Create `engine/src/recursor/testnet.rs`. Each zone is parsed with `hickory_proto::serialize::txt::Parser::new(text, None, Some(origin))`. The port is chosen by binding `127.0.54.1:0`, then binding every other zone IP (UDP and TCP) on that port, retrying from scratch up to 20 times on `AddrInUse`. Answer logic per received query (`q`, zone `z`):

```rust
fn answer(z: &ParsedZone, q: &Message) -> Message {
    let query = &q.queries[0];
    let qname = query.name().to_lowercase();
    let mut r = Message::response(q.metadata.id, OpCode::Query);
    r.queries.push(query.clone()); // echo the exact casing (0x20)
    if z.behaviour == Behaviour::Refused || !z.origin.zone_of(&qname) {
        r.metadata.response_code = ResponseCode::Refused;
        return r;
    }
    // delegation: deepest NS owner strictly below the origin that is an ancestor-or-self of qname
    if let Some(cut) = z.cuts.iter().filter(|c| c.zone_of(&qname)).max_by_key(|c| c.num_labels()) {
        if !(query.query_type() == RecordType::DS && &qname == cut) {
            r.authorities.extend(z.rrset(cut, RecordType::NS));
            for ns in z.rrset(cut, RecordType::NS) {
                if let RData::NS(NS(target)) = &ns.data {
                    r.additionals.extend(z.rrset(target, RecordType::A)); // glue as written, even out of bailiwick
                }
            }
            return r;
        }
    }
    r.metadata.authoritative = true;
    // DNAME at an ancestor
    for anc in ancestors(&qname) {
        if let Some(d) = z.rrset(&anc, RecordType::DNAME).first() {
            if anc != qname {
                if let RData::DNAME(DNAME(target)) = &d.data {
                    r.answers.push(d.clone());
                    let keep = (qname.num_labels() - anc.num_labels()) as usize;
                    let synth = Name::from_labels(qname.iter().take(keep)).unwrap().append_domain(target).unwrap();
                    r.answers.push(Record::from_rdata(qname.clone(), 0, RData::CNAME(CNAME(synth))));
                    return r;
                }
            }
        }
    }
    let exact = z.rrset(&qname, query.query_type());
    if !exact.is_empty() { r.answers.extend(exact); return r; }
    let cname = z.rrset(&qname, RecordType::CNAME);
    if !cname.is_empty() { r.answers.extend(cname); return r; }
    r.authorities.extend(z.rrset(&z.origin, RecordType::SOA));
    if !z.names.contains(&qname) && !z.names.iter().any(|n| qname.zone_of(n)) {
        r.metadata.response_code = ResponseCode::NXDomain;
    }
    r
}
```

`Behaviour::Silent` never replies. TCP accepts one length-prefixed query per connection and uses the same `answer`. Every received query is appended to `queries`. `params(root_ip)` returns hints `[("root.fake.", [root_ip])]`, `qname_minimisation: true`, limits 100/32, `authority_port: port`.

- [ ] Create `engine/src/recursor/iterate_tests.rs`:

```rust
use super::budget::{Limit, WorkBudget};
use super::iterate::{RecursionError, Recursor};
use super::metrics::RecursorMetrics;
use super::testnet::{Behaviour, FakeNet, FakeZone};
use hickory_proto::op::ResponseCode;
use hickory_proto::rr::{rdata::A, Name, RData, RecordType};
use std::net::Ipv4Addr;
use std::sync::Arc;

const SOA: &str = "@ 300 IN SOA ns hostmaster 1 3600 600 86400 300\n";

fn hierarchy(extra_example: &str, zones_extra: Vec<FakeZone>) -> Vec<FakeZone> {
    let mut v = vec![
        FakeZone { origin: ".", ip: Ipv4Addr::new(127, 0, 54, 1), behaviour: Behaviour::Normal, text: format!(
            "{SOA}@ 300 IN NS root.fake.\nroot.fake. 300 IN A 127.0.54.1\n\
             test. 300 IN NS ns1.test.\ntest. 300 IN NS ns2.test.\nns1.test. 300 IN A 127.0.54.9\nns2.test. 300 IN A 127.0.54.2\n") },
        FakeZone { origin: "test.", ip: Ipv4Addr::new(127, 0, 54, 2), behaviour: Behaviour::Normal, text: format!(
            "{SOA}@ 300 IN NS ns2\nns2 300 IN A 127.0.54.2\n\
             example 300 IN NS ns.example\nns.example 300 IN A 127.0.54.3\n\
             glueless 300 IN NS ns.provider.example.test.\n\
             poison 300 IN NS ns.poison\nns.poison 300 IN A 127.0.54.5\n\
             deep.a.b.c.d.e 300 IN A 192.0.2.77\n") },
        FakeZone { origin: "example.test.", ip: Ipv4Addr::new(127, 0, 54, 3), behaviour: Behaviour::Normal, text: format!(
            "{SOA}@ 300 IN NS ns\nns 300 IN A 127.0.54.3\nwww 300 IN A 192.0.2.10\nalias 300 IN CNAME www\n\
             ext 300 IN CNAME www.glueless.test.\nloop1 300 IN CNAME loop2\nloop2 300 IN CNAME loop1\n\
             old 300 IN DNAME example.test.\nns.provider 300 IN A 127.0.54.4\n{extra_example}") },
        FakeZone { origin: "glueless.test.", ip: Ipv4Addr::new(127, 0, 54, 4), behaviour: Behaviour::Normal, text: format!(
            "{SOA}@ 300 IN NS ns.provider.example.test.\nwww 300 IN A 192.0.2.20\n") },
        FakeZone { origin: "poison.test.", ip: Ipv4Addr::new(127, 0, 54, 5), behaviour: Behaviour::Normal, text: format!(
            "{SOA}@ 300 IN NS ns\nns 300 IN A 127.0.54.5\n\
             sub 300 IN NS ns.example.test.\nns.example.test. 300 IN A 127.0.54.66\n\
             www 300 IN A 192.0.2.30\n") },
        FakeZone { origin: "test.", ip: Ipv4Addr::new(127, 0, 54, 9), behaviour: Behaviour::Silent, text: String::new() },
    ];
    v.extend(zones_extra);
    v
}

async fn resolve(net: &FakeNet, r: &Recursor, name: &str, t: RecordType) -> Result<super::iterate::Resolution, RecursionError> {
    let p = net.params(Ipv4Addr::new(127, 0, 54, 1));
    let b = WorkBudget::new(p.max_upstream_queries, p.max_delegation_depth);
    r.resolve(&Name::from_ascii(name).unwrap(), t, &p, &b).await
}

fn a(ip: [u8; 4]) -> RData { RData::A(A(Ipv4Addr::from(ip))) }

#[tokio::test(flavor = "current_thread")]
async fn follows_delegations_from_root_hints() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let res = resolve(&net, &r, "www.example.test.", RecordType::A).await.unwrap();
    assert_eq!(res.rcode, ResponseCode::NoError);
    assert_eq!(res.answers.last().unwrap().data, a([192, 0, 2, 10]));
    assert_eq!(res.zone, Name::from_ascii("example.test.").unwrap());
    assert_eq!(res.ns_addrs, vec!["127.0.54.3".parse::<std::net::IpAddr>().unwrap()]);
}

#[tokio::test(flavor = "current_thread")]
async fn silent_server_is_skipped_and_backed_off() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    for i in 0..8 {
        let name = format!("nx{i}.test.");
        let res = resolve(&net, &r, &name, RecordType::A).await.unwrap();
        assert_eq!(res.rcode, ResponseCode::NXDomain, "{name}");
    }
    assert!(net.queries_to(Ipv4Addr::new(127, 0, 54, 9)) <= 3, "silent server backed off after 3 timeouts");
}

#[tokio::test(flavor = "current_thread")]
async fn glueless_delegation_resolves_ns_address() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let res = resolve(&net, &r, "www.glueless.test.", RecordType::A).await.unwrap();
    assert_eq!(res.answers.last().unwrap().data, a([192, 0, 2, 20]));
}

#[tokio::test(flavor = "current_thread")]
async fn out_of_bailiwick_glue_is_ignored() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    // poison.test's referral for sub.poison.test carries glue ns.example.test A 127.0.54.66 (outside poison.test)
    let _ = resolve(&net, &r, "www.sub.poison.test.", RecordType::A).await;
    let res = resolve(&net, &r, "ns.example.test.", RecordType::A).await.unwrap();
    assert_eq!(res.answers.last().unwrap().data, a([127, 0, 54, 3]));
    assert!(r.rrcache.get_glue(&Name::from_ascii("ns.example.test.").unwrap(), RecordType::A, 0)
        .map(|s| s.records.iter().all(|rec| rec.data != a([127, 0, 54, 66]))).unwrap_or(true));
}

#[tokio::test(flavor = "current_thread")]
async fn chases_cname_across_zones_and_dname() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let res = resolve(&net, &r, "ext.example.test.", RecordType::A).await.unwrap();
    assert_eq!(res.answers.len(), 2);
    assert_eq!(res.answers[1].data, a([192, 0, 2, 20]));
    let res = resolve(&net, &r, "www.old.example.test.", RecordType::A).await.unwrap();
    assert_eq!(res.answers.last().unwrap().data, a([192, 0, 2, 10]));
}

#[tokio::test(flavor = "current_thread")]
async fn cname_loop_and_depth_limit_fail() {
    let mut chain = String::new();
    for i in 0..17 { chain.push_str(&format!("c{i} 300 IN CNAME c{}.example.test.\n", i + 1)); }
    chain.push_str("c17 300 IN A 192.0.2.99\n");
    let net = FakeNet::start(hierarchy(&chain, vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    assert_eq!(resolve(&net, &r, "loop1.example.test.", RecordType::A).await.unwrap_err(), RecursionError::CnameLoop);
    assert_eq!(resolve(&net, &r, "c0.example.test.", RecordType::A).await.unwrap_err(), RecursionError::Limit(Limit::CnameDepth));
    assert!(resolve(&net, &r, "c2.example.test.", RecordType::A).await.is_ok(), "15 hops is within the limit");
}

#[tokio::test(flavor = "current_thread")]
async fn qname_minimisation_hides_full_name_from_root_and_tld() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let res = resolve(&net, &r, "deep.a.b.c.d.e.test.", RecordType::A).await.unwrap();
    assert_eq!(res.answers.last().unwrap().data, a([192, 0, 2, 77]));
    let q = net.queries.lock().unwrap().clone();
    let root_names: Vec<_> = q.iter().filter(|(ip, _, _)| *ip == Ipv4Addr::new(127, 0, 54, 1)).map(|(_, n, _)| n.to_lowercase()).collect();
    assert!(root_names.iter().all(|n| n == &Name::from_ascii("test.").unwrap()), "root saw {root_names:?}");
}

#[tokio::test(flavor = "current_thread")]
async fn query_budget_stops_amplification() {
    let net = FakeNet::start(hierarchy("", vec![])).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    let mut p = net.params(Ipv4Addr::new(127, 0, 54, 1));
    p.max_upstream_queries = 2;
    let b = WorkBudget::new(p.max_upstream_queries, p.max_delegation_depth);
    let err = r.resolve(&Name::from_ascii("www.glueless.test.").unwrap(), RecordType::A, &p, &b).await.unwrap_err();
    assert_eq!(err, RecursionError::Limit(Limit::UpstreamQueries));
    assert!(b.queries_used() <= 3);
}

#[tokio::test(flavor = "current_thread")]
async fn all_servers_refused_is_no_reachable_authority() {
    let net = FakeNet::start(vec![FakeZone { origin: ".", ip: Ipv4Addr::new(127, 0, 54, 1), behaviour: Behaviour::Refused, text: format!("{SOA}") }]).await;
    let r = Recursor::new(Arc::new(RecursorMetrics::default()));
    assert_eq!(resolve(&net, &r, "x.test.", RecordType::A).await.unwrap_err(), RecursionError::NoReachableAuthority);
}
```

- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib recursor::iterate_tests`; expect FAIL with "unresolved import `super::iterate`".
- [ ] Implement `iterate.rs`. `resolve` wraps `resolve_chain` in `tokio::time::timeout(RESOLUTION_DEADLINE)` (`Deadline` on expiry). All outgoing queries go through one helper that calls `budget.spend_query()?` first and always sets `dnssec_ok: true`, `use_0x20: true`, `recursion_desired: false`, `edns: !infra.no_edns(ip)`, `timeout: infra.rto(ip)`, `server: SocketAddr::new(ip, p.authority_port)`. Recursive calls are `Box::pin`ned. The algorithm:
  1. `resolve_chain(qname, qtype, depth)`: loop over the CNAME/DNAME chain with `seen: Vec<Name>` (lowercase). For each step call `resolve_one(sname, qtype)`. If the result's answers contain a CNAME owned by `sname` (and `qtype != CNAME`) or a DNAME owned by a proper ancestor of `sname`: target = CNAME target, or synthesised name `sname` minus the DNAME owner plus the DNAME target (a synthesised name longer than 255 octets → `rcode = YXDomain`, return). `hops += 1`; `hops > MAX_CNAME_DEPTH` → `metrics.limit_cname_depth += 1`, `Err(Limit(CnameDepth))`; target already in `seen` → `metrics.cname_loops += 1`, `Err(CnameLoop)`. If the response already contains in-bailiwick records for the target (same zone), take them without a new query; otherwise continue the loop with `sname = target`. Append every step's answers to the accumulated `answers`.
  2. `resolve_one(sname, stype)`: exact unexpired `rrcache.get` hit of credibility `>= AnswerNonAa` → return it. Otherwise `cut = closest_cut(sname)`: walk `sname` and its ancestors, the first cached NS RRset with at least one cached address (glue or authoritative) is the cut; none → root cut from `p.root_hints.addresses(ipv6)`.
  3. Loop with `delegations = 0`, `labels = if p.qname_minimisation { cut.zone.num_labels() + 1 } else { sname.num_labels() }`:
     - `query_name = if labels < sname.num_labels() { sname.trim_to(labels) } else { sname }`, `query_type = if query_name == sname { stype } else { RecordType::A }` (RFC 9156 §2.3 recommends A for hidden labels).
     - `ip = infra.select(&cut.addrs, &cut.zone, now, rng)`; `None` → resolve missing NS addresses (step 4); still `None` → `Err(NoReachableAuthority)`.
     - On `ExchangeError::Timeout`, `ExchangeError::Network(_)` (e.g. ICMP port unreachable, network unreachable for IPv6) or `ExchangeError::TcpFailed(_)` → `infra.record_timeout`, retry with the next selection (at most `cut.addrs.len() + 2` attempts per step before `NoReachableAuthority`). On success `infra.record_rtt`. `FORMERR`/`NOTIMP` to an EDNS query → `infra.set_no_edns(ip)`, `metrics.edns_fallbacks += 1`, retry the same server once.
     - Classify with `classify(&msg, &cut.zone, &query_name, query_type)`:

```rust
enum Class { Answer, Referral(Name), NoData, NxDomain, Lame }
fn classify(m: &Message, zone: &Name, qn: &Name, qt: RecordType) -> Class {
    let rc = m.metadata.response_code;
    if rc == ResponseCode::NXDomain { return Class::NxDomain; }
    if rc != ResponseCode::NoError { return Class::Lame; }
    let has_answer = m.answers.iter().any(|r| r.name.to_lowercase() == qn.to_lowercase()
        && (r.record_type() == qt || r.record_type() == RecordType::CNAME))
        || m.answers.iter().any(|r| r.record_type() == RecordType::DNAME && r.name.zone_of(qn));
    if has_answer { return Class::Answer; }
    let ns_owner = m.authorities.iter().filter(|r| r.record_type() == RecordType::NS).map(|r| r.name.to_lowercase()).next();
    if let Some(owner) = ns_owner {
        // a referral must go strictly down, stay inside the queried zone and lead towards qname
        if !m.metadata.authoritative && in_bailiwick(zone, &owner) && owner != zone.to_lowercase() && owner.zone_of(&qn.to_lowercase()) {
            return Class::Referral(owner);
        }
        if !m.metadata.authoritative { return Class::Lame; } // upward or sideways referral
    }
    if m.metadata.authoritative { Class::NoData } else { Class::Lame }
}
```

     - `Answer`: keep only answer records with `in_bailiwick(&cut.zone, &r.name)` (RRSIGs included); cache them `AnswerAa`/`AnswerNonAa`; if `query_name != sname` (minimised step) → `labels += 1`, continue; else return `Resolution { rcode: NoError, answers, authorities: in-bailiwick SOA/NSEC/NSEC3/RRSIG, zone: cut.zone, ns_names: cut.names, ns_addrs: cut.addrs }`.
     - `Referral(owner)`: `delegations += 1`; `budget.check_depth(delegations)` → `metrics.limit_delegation_depth += 1`, `Err(Limit(DelegationDepth))`. New cut: NS names from the authority NS RRset; glue = additional A/AAAA whose owner is one of those NS names **and** `in_bailiwick(&cut.zone, owner)` (records failing the check are discarded, never cached); cache NS as `AuthorityNonAa`, glue as `Glue`; DS/NSEC/NSEC3/RRSIG from the authority section cached as `AuthorityNonAa` with `DnssecStatus::Unchecked`; `labels = owner.num_labels() + 1`, continue.
     - `NoData`: if `query_name != sname` → empty non-terminal, `labels += 1`, continue; else return `NoError` with the SOA/NSEC authority section.
     - `NxDomain`: if `query_name != sname` → disable minimisation for this lookup (`labels = sname.num_labels()`), continue (relaxed RFC 9156 §2.3 fallback for broken servers); else return `NXDomain` with authority section.
     - `Lame`: `infra.mark_lame(ip, &cut.zone, now)`, `metrics.lame_marked += 1`, select another server.

4. Missing NS addresses: for up to 3 NS names without cached addresses, skip names where `!budget.enter(name, A)` (cycle), otherwise `Box::pin(self.resolve_chain(ns_name, A, depth + 1))` (and `AAAA` when `ipv6`), `budget.leave`, add addresses to `cut.addrs`. Errors other than `Limit` are ignored for that NS name; `Limit` propagates.

- `fetch` is `resolve_one` without the CNAME loop. `detect_ipv6` sets `ipv6` when `std::net::UdpSocket::bind("[::]:0")` followed by `connect("[2001:500:2::c]:53")` succeeds (no packet is sent).
- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib recursor::iterate_tests`; expect PASS (9 tests).
- [ ] Commit: `git add engine/src/recursor && git commit -m "feat(recursor): iterative resolution with bailiwick checks, QNAME minimisation and limits"`.

## Task 5: Mode selection, forward zones and query-path integration

Files:

- `engine/src/recursor/dispatch.rs` — `ResolutionRuntime`, `ForwardZones`, `Route`, `MissQuery`, `MissAnswer`, `resolve_miss`, response building.
- `engine/src/recursor/dispatch_tests.rs` — unit tests.
- `engine/src/recursor/mod.rs` — `RecursorState`.
- `engine/src/runtime.rs` — `Runtime.resolution: ResolutionRuntime` built in `Runtime::build`.
- `engine/src/upstream/mod.rs` — `impl ForwardUpstream for Upstreams`.
- `engine/src/server/mod.rs` — miss task calls `resolve_miss`; RPZ/AD hooks on the hit path.
- `engine/src/cache.rs` — `CachedResponse.policy_generation: u64`; AD-bit clearing on serve.
- `engine/src/edns.rs` — EDE option writing.
- `engine/src/main.rs` — creates `Arc<RecursorState>`, calls `detect_ipv6`, passes it to workers and the control runtime.
- `engine/src/telemetry/metrics.rs` — exposition of every `RecursorMetrics` counter.
- `engine/src/telemetry/querylog.rs` — `QueryRecord.route`, `QueryRecord.dnssec`, `QueryRecord.rpz` + OTLP attributes.

Interfaces:

```rust
// dispatch.rs
#[derive(Clone, Copy, Debug, PartialEq, Eq)] pub enum Mode { Forward, Recursive }
pub struct ForwardZone { pub domain: Name, pub servers: Vec<SocketAddr>, pub validate: bool }
pub struct ForwardZones { by_wire: FxHashMap<Box<[u8]>, usize>, pub zones: Vec<ForwardZone> }
impl ForwardZones { pub fn build(z: &[pb::ForwardZone]) -> Result<Self, String>; pub fn longest_match(&self, qname_wire_lower: &[u8]) -> Option<&ForwardZone>; }
pub enum Route<'a> { Forward, Recursive, ForwardZone(&'a ForwardZone) }
pub struct ResolutionRuntime { pub mode: Mode, pub params: Arc<RecursionParams>, pub forward_zones: ForwardZones, pub dnssec: Arc<crate::recursor::dnssec::DnssecRuntime> }
impl ResolutionRuntime {
    pub fn build(s: &pb::ConfigSnapshot) -> Result<Self, String>;
    pub fn route(&self, qname_wire_lower: &[u8]) -> Route<'_>;
}
pub trait ForwardUpstream { fn forward<'a>(&'a self, query_wire: Vec<u8>) -> futures::future::LocalBoxFuture<'a, Result<Vec<u8>, String>>; }
pub struct MissQuery { pub qname: Name, pub qtype: RecordType, pub client_ip: IpAddr, pub dnssec_ok: bool, pub checking_disabled: bool, pub authentic_data: bool, pub over_tcp: bool }
#[derive(Clone, Debug, PartialEq, Eq)] pub struct Ede { pub code: u16, pub text: String }
#[derive(Clone, Copy, Debug, PartialEq, Eq)] pub enum RouteTaken { Forward = 0, Recursive = 1, ForwardZone = 2 }
#[derive(Clone, Copy, Debug, PartialEq, Eq)] pub enum SecurityTag { None = 0, Secure = 1, Insecure = 2, Bogus = 3, Indeterminate = 4 }
pub struct MissAnswer {
    pub wire: Vec<u8>,            // full response, ID 0, lowercase question, no OPT
    pub cacheable: bool,
    pub ede: Option<Ede>,
    pub drop: bool,               // RPZ DROP
    pub route: RouteTaken,
    pub security: SecurityTag,
    pub rpz_action: u8,           // 0 none, then RpzAction discriminant + 1
    pub policy_generation: u64,
}
pub async fn resolve_miss(rt: &ResolutionRuntime, state: &RecursorState, upstream: &dyn ForwardUpstream, q: &MissQuery) -> MissAnswer;
pub fn build_response(q: &MissQuery, rcode: ResponseCode, answers: &[Record], authorities: &[Record], secure: bool) -> Vec<u8>;
// mod.rs
pub struct RecursorState {
    pub metrics: Arc<RecursorMetrics>,
    pub recursor: Recursor,
    pub validator: dnssec::validator::Validator,          // Task 7
    pub anchors: Arc<dnssec::anchors::TrustAnchorStore>,  // Task 8
    pub rpz: arc_swap::ArcSwap<rpz::index::RpzSet>,       // Task 9
    pub policy_generation: AtomicU64,
}
impl RecursorState { pub fn new(state_dir: &Path) -> Arc<Self>; }
// edns.rs
pub fn append_ede_option(opt_rdata: &mut Vec<u8>, ede: &Ede); // option code 15, info-code u16, UTF-8 text
```

In this task `validator`, `anchors` and `rpz` are declared but `resolve_miss` does not call them yet (Tasks 7–9 add those calls); `DnssecRuntime` is created here as `pub struct DnssecRuntime { pub validation: bool, pub ntas: Vec<(Name, i64)>, pub anchors: Vec<pb::TrustAnchor>, pub rfc5011: bool }` in `engine/src/recursor/dnssec/mod.rs`.

- [ ] Create `engine/src/recursor/dispatch_tests.rs`:

```rust
use super::dispatch::*;
use super::testnet::{Behaviour, FakeNet, FakeZone};
use super::RecursorState;
use crate::pb;
use hickory_proto::op::{Message, ResponseCode};
use hickory_proto::rr::{rdata::A, Name, RData, RecordType};
use std::cell::Cell;
use std::net::Ipv4Addr;

const SOA: &str = "@ 300 IN SOA ns hostmaster 1 3600 600 86400 300\n";

struct NoUpstream(Cell<u32>);
impl ForwardUpstream for NoUpstream {
    fn forward<'a>(&'a self, _q: Vec<u8>) -> futures::future::LocalBoxFuture<'a, Result<Vec<u8>, String>> {
        self.0.set(self.0.get() + 1);
        Box::pin(async { Err("no upstream in this test".to_string()) })
    }
}

async fn net() -> FakeNet {
    FakeNet::start(vec![
        FakeZone { origin: ".", ip: Ipv4Addr::new(127, 0, 54, 1), behaviour: Behaviour::Normal, text: format!("{SOA}@ 300 IN NS root.fake.\nroot.fake. 300 IN A 127.0.54.1\ncorp 300 IN NS ns.corp\nns.corp 300 IN A 127.0.54.3\npublic 300 IN NS ns.public\nns.public 300 IN A 127.0.54.3\n") },
        FakeZone { origin: "public.", ip: Ipv4Addr::new(127, 0, 54, 3), behaviour: Behaviour::Normal, text: format!("{SOA}@ 300 IN NS ns\nns 300 IN A 127.0.54.3\nwww 300 IN A 192.0.2.10\n") },
        FakeZone { origin: "corp.", ip: Ipv4Addr::new(127, 0, 54, 7), behaviour: Behaviour::Normal, text: format!("{SOA}@ 300 IN NS ns\nwww 300 IN A 10.0.0.10\n") },
    ]).await
}

fn snapshot(mode: pb::ResolutionMode, port: u16) -> pb::ConfigSnapshot {
    pb::ConfigSnapshot {
        resolution_mode: mode as i32,
        recursion: Some(pb::RecursionConfig { root_hints: vec![pb::RootHint { name: "root.fake.".into(), addresses: vec!["127.0.54.1".into()] }], qname_minimisation: true, aggressive_nsec: false, max_upstream_queries: 100, max_delegation_depth: 32, authority_port: port as u32 }),
        forward_zones: vec![pb::ForwardZone { domain: "corp.".into(), addresses: vec![format!("127.0.54.7:{port}")], validate: false }],
        ..Default::default()
    }
}

fn q(name: &str, dnssec_ok: bool) -> MissQuery {
    MissQuery { qname: Name::from_ascii(name).unwrap(), qtype: RecordType::A, client_ip: "127.0.0.1".parse().unwrap(), dnssec_ok, checking_disabled: false, authentic_data: false, over_tcp: false }
}

fn wire(n: &str) -> Vec<u8> { Name::from_ascii(n).unwrap().to_lowercase().to_bytes().unwrap() }

#[test]
fn route_prefers_longest_forward_zone_then_mode() {
    let mut s = snapshot(pb::ResolutionMode::Recursive, 53);
    s.forward_zones.push(pb::ForwardZone { domain: "lab.corp.".into(), addresses: vec!["127.0.0.9:53".into()], validate: true });
    let rt = ResolutionRuntime::build(&s).unwrap();
    match rt.route(&wire("a.lab.corp.")) { Route::ForwardZone(z) => assert_eq!(z.domain, Name::from_ascii("lab.corp.").unwrap()), _ => panic!("expected lab.corp.") }
    match rt.route(&wire("corp.")) { Route::ForwardZone(z) => assert_eq!(z.domain, Name::from_ascii("corp.").unwrap()), _ => panic!("expected corp.") }
    assert!(matches!(rt.route(&wire("xcorp.")), Route::Recursive));
    let rt = ResolutionRuntime::build(&snapshot(pb::ResolutionMode::Unspecified, 53)).unwrap();
    assert!(matches!(rt.route(&wire("www.public.")), Route::Forward));
}

#[tokio::test(flavor = "current_thread")]
async fn recursive_mode_resolves_without_upstreams_and_forward_zone_overrides() {
    let net = net().await;
    let dir = tempfile::tempdir().unwrap();
    let state = RecursorState::new(dir.path());
    let rt = ResolutionRuntime::build(&snapshot(pb::ResolutionMode::Recursive, net.port)).unwrap();
    let up = NoUpstream(Cell::new(0));
    let a = resolve_miss(&rt, &state, &up, &q("www.public.", false)).await;
    let m = Message::from_vec(&a.wire).unwrap();
    assert_eq!(m.metadata.response_code, ResponseCode::NoError);
    assert!(m.metadata.recursion_available);
    assert_eq!(m.answers[0].data, RData::A(A(Ipv4Addr::new(192, 0, 2, 10))));
    assert_eq!(a.route, RouteTaken::Recursive);
    assert!(a.cacheable);
    let b = resolve_miss(&rt, &state, &up, &q("www.corp.", false)).await;
    let m = Message::from_vec(&b.wire).unwrap();
    assert_eq!(m.answers[0].data, RData::A(A(Ipv4Addr::new(10, 0, 0, 10))), "forward zone beats recursion (root delegates corp. elsewhere)");
    assert_eq!(b.route, RouteTaken::ForwardZone);
    assert_eq!(up.0.get(), 0, "global upstreams never used in recursive mode");
}

#[tokio::test(flavor = "current_thread")]
async fn forward_mode_uses_upstreams_and_failure_is_servfail_with_ede() {
    let dir = tempfile::tempdir().unwrap();
    let state = RecursorState::new(dir.path());
    let rt = ResolutionRuntime::build(&snapshot(pb::ResolutionMode::Forward, 53)).unwrap();
    let up = NoUpstream(Cell::new(0));
    let a = resolve_miss(&rt, &state, &up, &q("www.public.", false)).await;
    assert_eq!(up.0.get(), 1);
    let m = Message::from_vec(&a.wire).unwrap();
    assert_eq!(m.metadata.response_code, ResponseCode::ServFail);
    assert_eq!(a.ede, Some(Ede { code: 22, text: "no reachable authority".into() }));
    assert!(!a.cacheable);
}

#[tokio::test(flavor = "current_thread")]
async fn root_unreachable_is_servfail_ede_22() {
    let dir = tempfile::tempdir().unwrap();
    let state = RecursorState::new(dir.path());
    let mut s = snapshot(pb::ResolutionMode::Recursive, 1);
    s.recursion.as_mut().unwrap().root_hints[0].addresses = vec!["127.0.54.250".into()];
    let rt = ResolutionRuntime::build(&s).unwrap();
    let a = resolve_miss(&rt, &state, &NoUpstream(Cell::new(0)), &q("www.public.", false)).await;
    assert_eq!(Message::from_vec(&a.wire).unwrap().metadata.response_code, ResponseCode::ServFail);
    assert_eq!(a.ede.unwrap().code, 22);
}
```

- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib recursor::dispatch_tests`; expect FAIL with "unresolved import `super::dispatch`".
- [ ] Implement `dispatch.rs`:
  - `ForwardZones::build` stores `domain.to_lowercase().to_bytes()` (uncompressed wire) → index. `longest_match` walks label offsets from the start of the name (offset 0, then `offset += 1 + wire[offset]` until the root label) and returns the first hit, which is the longest suffix; no allocation.
  - `route`: `longest_match` first; else `Mode::Recursive → Route::Recursive`, `Mode::Forward → Route::Forward`. `ResolutionMode::Unspecified` maps to `Mode::Forward`.
  - `resolve_miss`:
    - `Route::Recursive`: `WorkBudget::new(params.max_upstream_queries, params.max_delegation_depth)`; `state.recursor.resolve`; `metrics.resolutions_recursive += 1`.
    - `Route::ForwardZone(z)`: `metrics.resolutions_forward_zone += 1`; `Transport::exchange` with `recursion_desired: true`, `use_0x20: true`, `dnssec_ok: true`, `checking_disabled: true`, server chosen with `infra.select` over `z.servers` IPs (port taken from the matching `SocketAddr`), up to `z.servers.len()` attempts; answer/authority sections copied as received.
    - `Route::Forward`: build a query with `hickory_proto::op::Message` (random ID, RD=1, EDNS 1232, DO=1 and CD=1 when `rt.dnssec.validation`, else DO = client DO), call `upstream.forward`, decode; `Err` → failure.
    - Errors map to `SERVFAIL`, `cacheable: false`, `metrics.resolution_failures += 1` and EDE: `NoReachableAuthority`/forward error → `22 "no reachable authority"`; `Deadline` → `22 "resolution deadline exceeded"`; `Limit(_)` → `metrics.limit_queries += 1` when `UpstreamQueries`, EDE `0 "work limit exceeded"`; `CnameLoop` → `0 "CNAME loop"`.
    - Success: `build_response` then `cacheable = rcode ∈ {NoError, NXDomain}`.
  - `build_response`: `Message::response(0, Query)`, RA=1, RD=1, AA=0, `authentic_data = secure`, CD copied from query, question with lowercase `q.qname`; when `!q.dnssec_ok`, drop RRSIG/NSEC/NSEC3 records unless `q.qtype` is that type (RFC 4035 §3.2.1); `to_vec()`.
- [ ] Implement `RecursorState::new` (metrics, `Recursor::new`, empty `RpzSet`, generation 0; validator/anchors placeholders are the real structs once Tasks 7–8 land — in this task create `Validator::new(metrics.clone())` and `TrustAnchorStore::open(state_dir.join("trust-anchors.json"))` as empty types with those constructors in `dnssec/validator.rs` and `dnssec/anchors.rs`, whose bodies Tasks 7–8 fill).
- [ ] Integrate into the engine:
  - `runtime.rs`: `Runtime::build` sets `resolution: ResolutionRuntime::build(snapshot)?`.
  - `upstream/mod.rs`: `impl ForwardUpstream for Upstreams { fn forward(..) { Box::pin(async move { self.query(&query_wire).await.map_err(|e| e.to_string()) }) } }`.
  - `server/mod.rs` miss task: replace the direct upstream call with `let ans = recursor::dispatch::resolve_miss(&rt.resolution, &state, &*rt.upstreams, &miss_query).await;` When `ans.drop` send nothing. Otherwise feed `ans.wire` to the existing M1 insert-and-serve code: insert into the cache only when `ans.cacheable`, set `CachedResponse.policy_generation = ans.policy_generation`, and pass `ans.ede.as_ref()` to the reply writer, which calls `edns::append_ede_option` when the client sent OPT.
  - `cache.rs` serve step: when the client query has neither DO nor AD set, clear the AD bit (byte 3, mask `0x20`) in the output buffer after copying — a single byte operation, no allocation.
  - `main.rs`: `let recursor_state = RecursorState::new(&cfg.state_dir); recursor_state.recursor.detect_ipv6();` and clone the `Arc` into every worker and the control runtime.
  - `telemetry/metrics.rs` renders (counter unless noted): `nexora_recursor_upstream_queries_total`, `nexora_recursor_upstream_timeouts_total`, `nexora_recursor_mismatched_replies_total{reason="id|question|case|malformed"}`, `nexora_recursor_tcp_fallback_total`, `nexora_recursor_edns_fallback_total`, `nexora_recursor_lame_servers_total`, `nexora_recursor_work_limit_exceeded_total{limit="upstream_queries|delegation_depth|cname_depth"}`, `nexora_recursor_cname_loops_total`, `nexora_resolutions_total{route="recursive|forward_zone"}`, `nexora_resolution_failures_total`, gauge `nexora_recursor_infra_entries`, `nexora_dnssec_validations_total{result="secure|insecure|bogus|indeterminate"}`, `nexora_dnssec_bogus_total{ede="<code>"}` (only non-zero codes), `nexora_dnssec_aggressive_synthesized_total`, `nexora_dnssec_trust_anchor_refresh_failures_total`.
  - `telemetry/querylog.rs`: `QueryRecord` gains `route: u8`, `dnssec: u8`, `rpz_action: u8`; OTLP attributes `nexora.route` (`forward|recursive|forward_zone`), `nexora.dnssec` (`none|secure|insecure|bogus|indeterminate`), `nexora.rpz` (`none|nxdomain|nodata|passthru|drop|tcp_only|local_data|disabled`).
- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib recursor::dispatch_tests`; expect PASS (4 tests).
- [ ] Run the whole engine suite to prove the hot path is untouched: `scripts/dev-sync.sh && scripts/dev-exec.sh make engine-test`; expect PASS including `cache_hit_path_does_not_allocate`.
- [ ] Commit: `git add engine && git commit -m "feat(engine): resolution mode selection, forward zones and recursive miss path"`.

## Task 6: DNSSEC primitives — signature verification, validity windows, DS matching, NSEC/NSEC3 denial

Files:

- `engine/src/recursor/dnssec/mod.rs` — module declarations (`verify`, `denial`, `validator`, `anchors`, `nsec_cache`, `#[cfg(test)] testsign`), `DnssecRuntime`.
- `engine/src/recursor/dnssec/verify.rs` — RRSIG verification over hickory's `Verifier`, RFC 4034 §3.1.5 time arithmetic with skew, algorithm/digest support, DS matching.
- `engine/src/recursor/dnssec/denial.rs` — canonical ordering, NSEC (RFC 4035 §5.4) and NSEC3 (RFC 5155 §8, RFC 9276) proofs.
- `engine/src/recursor/dnssec/testsign.rs` — `#[cfg(test)]` signing helpers (ECDSA P-256 via `EcdsaSigningKey`).
- `engine/src/recursor/dnssec/primitives_tests.rs` — unit tests.

Interfaces:

```rust
// verify.rs
pub const SUPPORTED_ALGORITHMS: [u8; 4] = [8, 13, 14, 15];
#[derive(Debug, PartialEq, Eq, Clone, Copy)] pub enum Validity { Valid, NotYetValid, Expired, Malformed }
pub fn within_validity(inception: u32, expiration: u32, now_unix: u64) -> Validity;
pub fn algorithm_supported(a: Algorithm) -> bool;          // u8::from(a) in SUPPORTED_ALGORITHMS
pub fn digest_supported(d: DigestType) -> bool;             // SHA1, SHA256, SHA384
#[derive(Debug, PartialEq, Eq, Clone, Copy)]
pub enum VerifyError { NoRrsig, NoMatchingKey, UnsupportedAlgorithm, Expired, NotYetValid, BadSignature, SignerMismatch, LabelCount }
#[derive(Debug, Clone, Copy)] pub struct VerifiedSig { pub key_tag: u16, pub expiration: u32, pub original_ttl: u32, pub wildcard_expanded: bool }
pub fn verify_rrset(rrset: &[Record], rrsigs: &[Record], keys: &[DNSKEY], zone: &Name, now_unix: u64) -> Result<VerifiedSig, VerifyError>;
pub fn capped_ttl(rrset_ttl: u32, sig: &VerifiedSig, now_unix: u64) -> u32; // min(ttl, original_ttl, expiration - now)
#[derive(Debug)] pub enum DsMatch { Matched(Vec<DNSKEY>), NoSupported, NoMatch }
pub fn match_ds(zone: &Name, dnskeys: &[DNSKEY], ds: &[DS]) -> DsMatch;
// denial.rs
pub const NSEC3_INSECURE_ITERATIONS: u16 = 50;
pub const NSEC3_BOGUS_ITERATIONS: u16 = 150;
#[derive(Debug, PartialEq, Eq, Clone, Copy)]
pub enum Denial { Proven, ProvenOptOut, NotProven(&'static str), InsecureIterations(u16), BogusIterations(u16) }
pub fn canonical_cmp(a: &Name, b: &Name) -> Ordering;
pub fn nsec_covers(owner: &Name, next: &Name, name: &Name) -> bool;
pub fn nsec_proves_nxdomain(qname: &Name, nsecs: &[(Name, NSEC)]) -> Denial;
pub fn nsec_proves_nodata(qname: &Name, qtype: RecordType, nsecs: &[(Name, NSEC)]) -> Denial;
pub fn nsec_proves_no_closer_match(qname: &Name, rrsig_labels: u8, nsecs: &[(Name, NSEC)]) -> Denial;
pub fn nsec3_proves_nxdomain(qname: &Name, zone: &Name, nsec3s: &[(Name, NSEC3)]) -> Denial;
pub fn nsec3_proves_nodata(qname: &Name, qtype: RecordType, zone: &Name, nsec3s: &[(Name, NSEC3)]) -> Denial;
// testsign.rs (cfg(test))
pub struct TestKey { pub zone: Name, pub dnskey: DNSKEY, key: EcdsaSigningKey }
impl TestKey {
    pub fn generate(zone: &str, ksk: bool) -> Self;
    pub fn ds(&self) -> DS;
    pub fn dnskey_record(&self) -> Record;
    pub fn sign(&self, rrset: &[Record], inception: u32, expiration: u32) -> Record;
}
pub fn nsec_chain(zone: &str, names: &[(&str, &[RecordType])]) -> Vec<(Name, NSEC)>;
pub fn nsec3_chain(zone: &str, names: &[(&str, &[RecordType])], iterations: u16, opt_out: bool) -> Vec<(Name, NSEC3)>;
```

- [ ] Create `engine/src/recursor/dnssec/testsign.rs`:

```rust
use hickory_proto::dnssec::crypto::EcdsaSigningKey;
use hickory_proto::dnssec::rdata::{sig::SigInput, DNSSECRData, DNSKEY, DS, NSEC, NSEC3, RRSIG};
use hickory_proto::dnssec::{Algorithm, DigestType, Nsec3HashAlgorithm, SigningKey, TBS};
use hickory_proto::rr::{DNSClass, Name, RData, Record, RecordType, SerialNumber};

pub struct TestKey { pub zone: Name, pub dnskey: DNSKEY, key: EcdsaSigningKey }

impl TestKey {
    pub fn generate(zone: &str, ksk: bool) -> Self {
        let pkcs8 = EcdsaSigningKey::generate_pkcs8(Algorithm::ECDSAP256SHA256).unwrap();
        let key = EcdsaSigningKey::from_pkcs8(&pkcs8, Algorithm::ECDSAP256SHA256).unwrap();
        let dnskey = DNSKEY::new(true, ksk, false, key.to_public_key().unwrap());
        TestKey { zone: Name::from_ascii(zone).unwrap(), dnskey, key }
    }
    pub fn ds(&self) -> DS {
        let digest = self.dnskey.to_digest(&self.zone, DigestType::SHA256).unwrap();
        DS::new(self.dnskey.calculate_key_tag().unwrap(), Algorithm::ECDSAP256SHA256, DigestType::SHA256, digest.as_ref().to_vec())
    }
    pub fn dnskey_record(&self) -> Record {
        Record::from_rdata(self.zone.clone(), 3600, RData::DNSSEC(DNSSECRData::DNSKEY(self.dnskey.clone())))
    }
    pub fn sign(&self, rrset: &[Record], inception: u32, expiration: u32) -> Record {
        let first = &rrset[0];
        let input = SigInput {
            type_covered: first.record_type(),
            algorithm: Algorithm::ECDSAP256SHA256,
            num_labels: first.name.num_labels(),
            original_ttl: first.ttl,
            sig_expiration: SerialNumber::new(expiration),
            sig_inception: SerialNumber::new(inception),
            key_tag: self.dnskey.calculate_key_tag().unwrap(),
            signer_name: self.zone.clone(),
        };
        let tbs = TBS::from_input(&first.name, DNSClass::IN, &input, rrset.iter()).unwrap();
        let sig = self.key.sign(&tbs).unwrap();
        Record::from_rdata(first.name.clone(), first.ttl, RData::DNSSEC(DNSSECRData::RRSIG(RRSIG::from_sig(input, sig))))
    }
}

pub fn nsec_chain(zone: &str, names: &[(&str, &[RecordType])]) -> Vec<(Name, NSEC)> {
    let mut v: Vec<(Name, Vec<RecordType>)> = names.iter().map(|(n, t)| (Name::from_ascii(n).unwrap(), t.to_vec())).collect();
    v.sort_by(|a, b| super::denial::canonical_cmp(&a.0, &b.0));
    let _ = zone;
    (0..v.len()).map(|i| {
        let next = v[(i + 1) % v.len()].0.clone();
        let mut types = v[i].1.clone();
        types.extend([RecordType::RRSIG, RecordType::NSEC]);
        (v[i].0.clone(), NSEC::new(next, types))
    }).collect()
}

pub fn nsec3_chain(zone: &str, names: &[(&str, &[RecordType])], iterations: u16, opt_out: bool) -> Vec<(Name, NSEC3)> {
    let z = Name::from_ascii(zone).unwrap();
    let mut hashed: Vec<(Vec<u8>, Vec<RecordType>)> = names.iter().map(|(n, t)| {
        let h = Nsec3HashAlgorithm::SHA1.hash(&[], &Name::from_ascii(n).unwrap(), iterations).unwrap();
        (h.as_ref().to_vec(), t.to_vec())
    }).collect();
    hashed.sort();
    (0..hashed.len()).map(|i| {
        let next = hashed[(i + 1) % hashed.len()].0.clone();
        let label = data_encoding::BASE32HEX_NOPAD.encode(&hashed[i].0).to_ascii_lowercase();
        let owner = Name::from_ascii(&label).unwrap().append_domain(&z).unwrap();
        (owner, NSEC3::new(Nsec3HashAlgorithm::SHA1, opt_out, iterations, vec![], next, hashed[i].1.clone()))
    }).collect()
}
```

- [ ] Create `engine/src/recursor/dnssec/primitives_tests.rs`:

```rust
use super::denial::*;
use super::testsign::{nsec3_chain, nsec_chain, TestKey};
use super::verify::*;
use hickory_proto::dnssec::{Algorithm, DigestType};
use hickory_proto::rr::{rdata::A, Name, RData, Record, RecordType};
use std::cmp::Ordering;
use std::net::Ipv4Addr;

const NOW: u32 = 1_800_000_000;

fn a_rrset(name: &str) -> Vec<Record> {
    vec![Record::from_rdata(Name::from_ascii(name).unwrap(), 300, RData::A(A(Ipv4Addr::new(192, 0, 2, 1))))]
}

#[test]
fn validity_window_with_skew_and_wraparound() {
    let inc = NOW - 86_400; let exp = NOW + 86_400; // span 2 days -> skew clamps to 3600
    assert_eq!(within_validity(inc, exp, NOW as u64), Validity::Valid);
    assert_eq!(within_validity(inc, exp, (exp + 3600) as u64), Validity::Valid);
    assert_eq!(within_validity(inc, exp, (exp + 3601) as u64), Validity::Expired);
    assert_eq!(within_validity(inc, exp, (inc - 3600) as u64), Validity::Valid);
    assert_eq!(within_validity(inc, exp, (inc - 3601) as u64), Validity::NotYetValid);
    // span 1 hour -> skew clamps up to 300
    assert_eq!(within_validity(NOW, NOW + 3600, (NOW + 3900) as u64), Validity::Valid);
    assert_eq!(within_validity(NOW, NOW + 3600, (NOW + 3901) as u64), Validity::Expired);
    // RFC 1982 wraparound: inception just before 2^32, expiration after
    assert_eq!(within_validity(0xFFFF_FF00, 0x0001_0000, 0x1_0000_0010), Validity::Valid);
    assert_eq!(within_validity(NOW + 10, NOW, NOW as u64), Validity::Malformed);
}

#[test]
fn only_algorithms_8_13_14_15_are_supported() {
    for (a, ok) in [(Algorithm::RSASHA256, true), (Algorithm::ECDSAP256SHA256, true), (Algorithm::ECDSAP384SHA384, true), (Algorithm::ED25519, true), (Algorithm::RSASHA1, false), (Algorithm::RSASHA512, false), (Algorithm::Unknown(16), false)] {
        assert_eq!(algorithm_supported(a), ok, "{a:?}");
    }
    assert!(digest_supported(DigestType::SHA256));
    assert!(!digest_supported(DigestType::Unknown(3)));
}

#[test]
fn verifies_good_signature_and_rejects_tampering_expiry_and_wrong_key() {
    let zsk = TestKey::generate("example.", false);
    let other = TestKey::generate("example.", false);
    let rrset = a_rrset("www.example.");
    let sig = zsk.sign(&rrset, NOW - 3600, NOW + 86_400);
    let zone = Name::from_ascii("example.").unwrap();
    let v = verify_rrset(&rrset, &[sig.clone()], &[zsk.dnskey.clone()], &zone, NOW as u64).unwrap();
    assert!(!v.wildcard_expanded);
    let mut tampered = rrset.clone();
    tampered[0].data = RData::A(A(Ipv4Addr::new(6, 6, 6, 6)));
    assert_eq!(verify_rrset(&tampered, &[sig.clone()], &[zsk.dnskey.clone()], &zone, NOW as u64).unwrap_err(), VerifyError::BadSignature);
    assert_eq!(verify_rrset(&rrset, &[sig.clone()], &[other.dnskey.clone()], &zone, NOW as u64).unwrap_err(), VerifyError::NoMatchingKey);
    assert_eq!(verify_rrset(&rrset, &[sig.clone()], &[zsk.dnskey.clone()], &zone, (NOW + 86_400 + 7200) as u64).unwrap_err(), VerifyError::Expired);
    assert_eq!(verify_rrset(&rrset, &[], &[zsk.dnskey.clone()], &zone, NOW as u64).unwrap_err(), VerifyError::NoRrsig);
    assert_eq!(verify_rrset(&rrset, &[sig], &[zsk.dnskey.clone()], &Name::from_ascii("other.").unwrap(), NOW as u64).unwrap_err(), VerifyError::SignerMismatch);
}

#[test]
fn ttl_is_capped_by_original_ttl_and_expiration() {
    let zsk = TestKey::generate("example.", false);
    let rrset = a_rrset("www.example.");
    let sig = zsk.sign(&rrset, NOW - 3600, NOW + 100);
    let v = verify_rrset(&rrset, &[sig], &[zsk.dnskey.clone()], &Name::from_ascii("example.").unwrap(), NOW as u64).unwrap();
    assert_eq!(capped_ttl(3600, &v, NOW as u64), 100);
    assert_eq!(capped_ttl(50, &v, NOW as u64), 50);
}

#[test]
fn ds_matching() {
    let ksk = TestKey::generate("example.", true);
    let other = TestKey::generate("example.", true);
    let zone = Name::from_ascii("example.").unwrap();
    match match_ds(&zone, &[ksk.dnskey.clone(), other.dnskey.clone()], &[ksk.ds()]) { DsMatch::Matched(k) => assert_eq!(k, vec![ksk.dnskey.clone()]), m => panic!("{m:?}") }
    assert!(matches!(match_ds(&zone, &[other.dnskey.clone()], &[ksk.ds()]), DsMatch::NoMatch));
    let unsupported = hickory_proto::dnssec::rdata::DS::new(1, Algorithm::Unknown(200), DigestType::SHA256, vec![0; 32]);
    assert!(matches!(match_ds(&zone, &[ksk.dnskey.clone()], &[unsupported]), DsMatch::NoSupported));
}

#[test]
fn canonical_order_rfc4034_section_6_1() {
    let names = ["example.", "a.example.", "yljkjljk.a.example.", "Z.a.example.", "zABC.a.EXAMPLE.", "z.example.", "\\001.z.example.", "*.z.example.", "\\200.z.example."];
    let parsed: Vec<Name> = names.iter().map(|n| Name::from_ascii(n).unwrap()).collect();
    for w in parsed.windows(2) { assert_eq!(canonical_cmp(&w[0], &w[1]), Ordering::Less, "{} < {}", w[0], w[1]); }
}

#[test]
fn nsec_nxdomain_and_nodata_proofs() {
    let chain = nsec_chain("example.", &[("example.", &[RecordType::SOA, RecordType::NS]), ("a.example.", &[RecordType::A]), ("d.example.", &[RecordType::A, RecordType::TXT])]);
    let q = Name::from_ascii("b.example.").unwrap();
    assert_eq!(nsec_proves_nxdomain(&q, &chain), Denial::Proven);
    assert_eq!(nsec_proves_nxdomain(&q, &chain[1..2]), Denial::NotProven("no NSEC covers the wildcard"));
    assert_eq!(nsec_proves_nxdomain(&Name::from_ascii("a.example.").unwrap(), &chain), Denial::NotProven("name exists"));
    assert_eq!(nsec_proves_nodata(&Name::from_ascii("d.example.").unwrap(), RecordType::AAAA, &chain), Denial::Proven);
    assert_eq!(nsec_proves_nodata(&Name::from_ascii("d.example.").unwrap(), RecordType::TXT, &chain), Denial::NotProven("type present in bitmap"));
}

#[test]
fn nsec3_nxdomain_nodata_opt_out_and_rfc9276_iterations() {
    let names: [(&str, &[RecordType]); 3] = [("example.", &[RecordType::SOA, RecordType::NS]), ("a.example.", &[RecordType::A]), ("c.example.", &[RecordType::A])];
    let zone = Name::from_ascii("example.").unwrap();
    let q = Name::from_ascii("b.example.").unwrap();
    let chain = nsec3_chain("example.", &names, 0, false);
    assert_eq!(nsec3_proves_nxdomain(&q, &zone, &chain), Denial::Proven);
    assert_eq!(nsec3_proves_nodata(&Name::from_ascii("a.example.").unwrap(), RecordType::TXT, &zone, &chain), Denial::Proven);
    assert_eq!(nsec3_proves_nodata(&Name::from_ascii("a.example.").unwrap(), RecordType::A, &zone, &chain), Denial::NotProven("type present in bitmap"));
    let opt_out = nsec3_chain("example.", &names, 0, true);
    assert_eq!(nsec3_proves_nodata(&Name::from_ascii("sub.b.example.").unwrap(), RecordType::DS, &zone, &opt_out), Denial::ProvenOptOut);
    assert_eq!(nsec3_proves_nxdomain(&q, &zone, &nsec3_chain("example.", &names, 51, false)), Denial::InsecureIterations(51));
    assert_eq!(nsec3_proves_nxdomain(&q, &zone, &nsec3_chain("example.", &names, 151, false)), Denial::BogusIterations(151));
}
```

- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib recursor::dnssec::primitives_tests`; expect FAIL with "unresolved import `super::denial`".
- [ ] Implement `within_validity` (RFC 4034 §3.1.5 arithmetic modulo 2^32; skew is 10% of the signature lifetime clamped to 300..=3600 s so signatures on hosts with modest clock skew still validate near both edges):

```rust
pub fn within_validity(inception: u32, expiration: u32, now_unix: u64) -> Validity {
    let now = now_unix as u32;
    let span = expiration.wrapping_sub(inception);
    if span == 0 || span > i32::MAX as u32 { return Validity::Malformed; }
    let skew = (span / 10).clamp(300, 3600);
    if (now.wrapping_sub(inception.wrapping_sub(skew)) as i32) < 0 { return Validity::NotYetValid; }
    if (expiration.wrapping_add(skew).wrapping_sub(now) as i32) < 0 { return Validity::Expired; }
    Validity::Valid
}
```

- [ ] Implement `verify_rrset`: filter `rrsigs` to RRSIG records whose `type_covered == rrset[0].record_type()` and owner equals the RRset owner (case-insensitive); none → `NoRrsig`. For each candidate: signer name must equal `zone` → else remember `SignerMismatch`; `!algorithm_supported` → remember `UnsupportedAlgorithm`; `num_labels > owner.num_labels()` (owner labels excluding a leading `*`) → `LabelCount`; `within_validity` not `Valid` → remember `Expired`/`NotYetValid`; keys = `keys` with `zone_key()`, `!revoke()`, same algorithm, `calculate_key_tag() == key_tag`; none → remember `NoMatchingKey`; for each key `key.verify_rrsig(&owner, DNSClass::IN, rrsig, rrset.iter())` → first `Ok` returns `VerifiedSig { wildcard_expanded: num_labels < owner labels, .. }`, all `Err` → remember `BadSignature`. When no candidate verifies, return the remembered error with precedence `BadSignature > Expired > NotYetValid > NoMatchingKey > UnsupportedAlgorithm > SignerMismatch > LabelCount` (the most specific failure). `capped_ttl` returns `min(rrset_ttl, original_ttl, expiration.wrapping_sub(now) as u32)`.
- [ ] Implement `match_ds`: `supported` = DS with `algorithm_supported` and `digest_supported`; empty → `NoSupported`; when a SHA-256 or SHA-384 DS exists, ignore SHA-1 DS for the same key tag (RFC 4509 §3); `Matched` keys are those with `ds.covers(zone, key) == Ok(true)` and `secure_entry_point` ignored (any zone key may be referenced); none → `NoMatch`.
- [ ] Implement `denial.rs`:

```rust
pub fn canonical_cmp(a: &Name, b: &Name) -> Ordering {
    let al: Vec<Vec<u8>> = a.iter().map(|l| l.to_ascii_lowercase()).collect();
    let bl: Vec<Vec<u8>> = b.iter().map(|l| l.to_ascii_lowercase()).collect();
    for (x, y) in al.iter().rev().zip(bl.iter().rev()) {
        match x.cmp(y) { Ordering::Equal => continue, o => return o }
    }
    al.len().cmp(&bl.len())
}

pub fn nsec_covers(owner: &Name, next: &Name, name: &Name) -> bool {
    if canonical_cmp(owner, next) == Ordering::Less {
        canonical_cmp(owner, name) == Ordering::Less && canonical_cmp(name, next) == Ordering::Less
    } else {
        // last NSEC in the zone wraps to the apex
        canonical_cmp(owner, name) == Ordering::Less || canonical_cmp(name, next) == Ordering::Less
    }
}

fn nsec3_hash_label(name: &Name, n: &NSEC3) -> Option<Vec<u8>> {
    n.hash_algorithm().hash(n.salt(), name, n.iterations()).ok().map(|d| d.as_ref().to_vec())
}

fn nsec3_owner_hash(owner: &Name) -> Option<Vec<u8>> {
    let first = owner.iter().next()?;
    data_encoding::BASE32HEX_NOPAD.decode(&first.to_ascii_uppercase()).ok()
}

fn nsec3_covers_hash(owner_hash: &[u8], next: &[u8], h: &[u8]) -> bool {
    if owner_hash < next { owner_hash < h && h < next } else { owner_hash < h || h < next }
}
```

- `nsec_proves_nxdomain`: any NSEC owner equal to `qname` → `NotProven("name exists")`; need one NSEC with `nsec_covers(owner, next, qname)` (else `NotProven("no NSEC covers qname")`); closest encloser = the longer of the common ancestors of `qname` with that NSEC's owner and with its next name; need an NSEC covering or matching-without-the-type `*.<closest encloser>` (else `NotProven("no NSEC covers the wildcard")`). Ignore NSECs whose bitmap has `NS` without `SOA` when proving names below their owner (ancestor delegation, `is_ancestor_delegation`).
- `nsec_proves_nodata`: an NSEC whose owner equals `qname` with neither `qtype` nor `CNAME` in `type_set()` → `Proven` (`qtype` present → `NotProven("type present in bitmap")`); for `qtype == DS` the NSEC must not contain `SOA` (must be the parent side); wildcard NODATA: a covering NSEC for `qname` plus an NSEC matching `*.<closest encloser>` without `qtype` → `Proven`.
- `nsec_proves_no_closer_match(qname, rrsig_labels, nsecs)`: next closer name = `qname.trim_to(rrsig_labels + 1)` must be covered by an NSEC.
- NSEC3 functions first check iterations of every supplied record: any `> NSEC3_BOGUS_ITERATIONS` → `BogusIterations(n)`; any `> NSEC3_INSECURE_ITERATIONS` → `InsecureIterations(n)` (RFC 9276 §3.2); unknown hash algorithm or non-empty flags other than opt-out → ignore that record.
- `nsec3_proves_nxdomain`: find closest encloser `ce` by walking `qname`'s ancestors down to `zone` and taking the first whose hash matches an NSEC3 owner hash; `nc` = the ancestor of `qname` one label longer than `ce`; require an NSEC3 covering `H(nc)` (`NotProven("next closer not covered")`); if that NSEC3 has opt-out → `ProvenOptOut`; require an NSEC3 covering `H(*.ce)` (`NotProven("wildcard not covered")`); else `Proven`.
- `nsec3_proves_nodata`: an NSEC3 matching `H(qname)` whose bitmap lacks `qtype` and `CNAME` → `Proven` (present → `NotProven("type present in bitmap")`); if no match and `qtype == DS`: closest encloser proof where the NSEC3 covering the next closer has opt-out → `ProvenOptOut` (RFC 5155 §8.6); otherwise `NotProven("no matching NSEC3")`.
- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib recursor::dnssec::primitives_tests`; expect PASS (8 tests).
- [ ] Commit: `git add engine/src/recursor/dnssec && git commit -m "feat(dnssec): signature verification, DS matching and NSEC/NSEC3 denial proofs"`.

## Task 7: Validator — chain of trust, EDE, NTAs, CD/AD, forward-mode validation, aggressive NSEC

Files:

- `engine/src/recursor/dnssec/validator.rs` — `Validator`, `Fetcher`, chain-of-trust walk, per-response validation, EDE mapping.
- `engine/src/recursor/dnssec/nsec_cache.rs` — RFC 8198 aggressive use of validated NSEC/NSEC3.
- `engine/src/recursor/dnssec/validator_tests.rs` — unit tests with an in-memory signed hierarchy.
- `engine/src/recursor/dispatch.rs` — `RoutedFetcher`; validation, CD and NTA handling in `resolve_miss`; aggressive-NSEC lookup before recursion.

Interfaces:

```rust
// validator.rs
pub struct FetchedSet { pub rcode: ResponseCode, pub answers: Vec<Record>, pub authorities: Vec<Record> }
#[derive(Debug, Clone)] pub enum FetchError { Unreachable, Limit }
pub trait Fetcher { fn fetch<'a>(&'a self, name: &'a Name, rtype: RecordType) -> LocalBoxFuture<'a, Result<FetchedSet, FetchError>>; }
#[derive(Debug, Clone)] pub enum TrustPoint { Ds(Vec<DS>), Keys(Vec<DNSKEY>) }
#[derive(Debug, Clone, Default)] pub struct TrustPoints { pub zones: Vec<(Name, TrustPoint)> }
impl TrustPoints { pub fn from_config(anchors: &[pb::TrustAnchor]) -> Self; pub fn closest(&self, name: &Name) -> Option<&(Name, TrustPoint)>; }
#[derive(Debug, Clone, PartialEq, Eq)] pub enum Security { Secure, Insecure(Option<Ede>), Bogus(Ede), Indeterminate(Ede) }
pub struct ValidationInput<'a> { pub qname: &'a Name, pub qtype: RecordType, pub rcode: ResponseCode, pub answers: &'a [Record], pub authorities: &'a [Record] }
pub struct ValidationResult { pub security: Security, pub ttl_cap: Option<u32> }
pub struct Validator { metrics: Arc<RecursorMetrics>, zones: quick_cache::sync::Cache<Name, Arc<(ZoneState, u64)>>, pub nsec: AggressiveNsecCache }
#[derive(Debug, Clone)] pub enum ZoneState { Secure(Vec<DNSKEY>), Insecure(Option<Ede>), Bogus(Ede) }
impl Validator {
    pub fn new(metrics: Arc<RecursorMetrics>) -> Self;
    pub async fn validate(&self, input: &ValidationInput<'_>, trust: &TrustPoints, ntas: &[(Name, i64)], fetcher: &dyn Fetcher, now_unix: u64) -> ValidationResult;
    pub async fn zone_state(&self, zone: &Name, trust: &TrustPoints, fetcher: &dyn Fetcher, now_unix: u64) -> ZoneState;
}
pub fn under_nta(name: &Name, ntas: &[(Name, i64)], now_unix: u64) -> bool;
// nsec_cache.rs
pub struct AggressiveNsecCache { /* per zone: BTreeMap of canonical key -> (owner, NSEC, rrsig, expires); SOA record per zone */ }
impl AggressiveNsecCache {
    pub fn new(max_zones: usize) -> Self;
    pub fn insert_secure(&self, zone: &Name, soa: &[Record], denial_records: &[Record], now_unix: u64);
    pub fn synthesize(&self, qname: &Name, qtype: RecordType, now_unix: u64) -> Option<(ResponseCode, Vec<Record>)>; // authorities incl. SOA, NSEC(3), RRSIGs
}
// dispatch.rs
pub struct RoutedFetcher<'a> { pub rt: &'a ResolutionRuntime, pub state: &'a RecursorState, pub upstream: &'a dyn ForwardUpstream, pub budget: &'a WorkBudget }
impl Fetcher for RoutedFetcher<'_> { /* route(name) -> Recursor::fetch | forward-zone exchange | upstream.forward with DO=1 CD=1 */ }
```

EDE codes produced (RFC 8914): 1 Unsupported DNSKEY Algorithm (insecure), 2 Unsupported DS Digest Type (insecure), 6 DNSSEC Bogus (bad signature), 7 Signature Expired, 8 Signature Not Yet Valid, 9 DNSKEY Missing, 10 RRSIGs Missing, 12 NSEC Missing, 22 No Reachable Authority, 23 Network Error (indeterminate), 27 Unsupported NSEC3 Iterations Value (insecure above 50, bogus above 150).

- [ ] Create `engine/src/recursor/dnssec/validator_tests.rs`:

```rust
use super::testsign::{nsec_chain, TestKey};
use super::validator::*;
use crate::recursor::dispatch::Ede;
use crate::recursor::metrics::RecursorMetrics;
use futures::future::LocalBoxFuture;
use hickory_proto::dnssec::rdata::{DNSSECRData, DS};
use hickory_proto::dnssec::{Algorithm, DigestType};
use hickory_proto::op::ResponseCode;
use hickory_proto::rr::{rdata::{A, NS, SOA}, Name, RData, Record, RecordType};
use std::cell::RefCell;
use std::collections::HashMap;
use std::net::Ipv4Addr;
use std::sync::Arc;

const NOW: u64 = 1_800_000_000;
const INC: u32 = (NOW - 3600) as u32;
const EXP: u32 = (NOW + 86_400) as u32;

fn n(s: &str) -> Name { Name::from_ascii(s).unwrap() }
fn rec(name: &str, data: RData) -> Record { Record::from_rdata(n(name), 300, data) }

#[derive(Default)]
struct Mock { sets: HashMap<(Name, RecordType), FetchedSet>, calls: RefCell<u32> }
impl Fetcher for Mock {
    fn fetch<'a>(&'a self, name: &'a Name, rtype: RecordType) -> LocalBoxFuture<'a, Result<FetchedSet, FetchError>> {
        *self.calls.borrow_mut() += 1;
        let r = self.sets.get(&(name.to_lowercase(), rtype)).map(|s| FetchedSet { rcode: s.rcode, answers: s.answers.clone(), authorities: s.authorities.clone() }).ok_or(FetchError::Unreachable);
        Box::pin(async move { r })
    }
}

struct World { root: TestKey, example: TestKey, mock: Mock, trust: TrustPoints }

fn signed(key: &TestKey, set: Vec<Record>) -> Vec<Record> { let s = key.sign(&set, INC, EXP); let mut v = set; v.push(s); v }

fn world() -> World {
    let root = TestKey::generate(".", true);
    let example = TestKey::generate("example.", true);
    let mut mock = Mock::default();
    let ok = |answers: Vec<Record>| FetchedSet { rcode: ResponseCode::NoError, answers, authorities: vec![] };
    mock.sets.insert((n("."), RecordType::DNSKEY), ok(signed(&root, vec![root.dnskey_record()])));
    mock.sets.insert((n("example."), RecordType::DS), ok(signed(&root, vec![rec("example.", RData::DNSSEC(DNSSECRData::DS(example.ds())))])));
    mock.sets.insert((n("example."), RecordType::DNSKEY), ok(signed(&example, vec![example.dnskey_record()])));
    // plain.example. is an unsigned delegation: NODATA for DS with a signed NSEC proving NS without DS
    let chain = nsec_chain("example.", &[("example.", &[RecordType::SOA, RecordType::NS, RecordType::DNSKEY]), ("plain.example.", &[RecordType::NS]), ("www.example.", &[RecordType::A])]);
    let soa = rec("example.", RData::SOA(SOA::new(n("ns.example."), n("h.example."), 1, 3600, 600, 86400, 300)));
    let mut auth = signed(&example, vec![soa.clone()]);
    let plain_nsec = rec("plain.example.", RData::DNSSEC(DNSSECRData::NSEC(chain.iter().find(|(o, _)| *o == n("plain.example.")).unwrap().1.clone())));
    auth.extend(signed(&example, vec![plain_nsec]));
    mock.sets.insert((n("plain.example."), RecordType::DS), FetchedSet { rcode: ResponseCode::NoError, answers: vec![], authorities: auth });
    // www.example. is not a zone cut: NODATA for DS with an NSEC whose bitmap has no NS
    let mut www_auth = signed(&example, vec![soa.clone()]);
    let www_nsec = rec("www.example.", RData::DNSSEC(DNSSECRData::NSEC(chain.iter().find(|(o, _)| *o == n("www.example.")).unwrap().1.clone())));
    www_auth.extend(signed(&example, vec![www_nsec]));
    mock.sets.insert((n("www.example."), RecordType::DS), FetchedSet { rcode: ResponseCode::NoError, answers: vec![], authorities: www_auth });
    // nodenial.example.: parent returns NODATA for DS without any NSEC
    mock.sets.insert((n("nodenial.example."), RecordType::DS), FetchedSet { rcode: ResponseCode::NoError, answers: vec![], authorities: signed(&example, vec![soa]) });
    let trust = TrustPoints { zones: vec![(Name::root(), TrustPoint::Ds(vec![root.ds()]))] };
    World { root, example, mock, trust }
}

fn input<'a>(qname: &'a Name, rcode: ResponseCode, answers: &'a [Record], authorities: &'a [Record]) -> ValidationInput<'a> {
    ValidationInput { qname, qtype: RecordType::A, rcode, answers, authorities }
}

#[tokio::test(flavor = "current_thread")]
async fn secure_answer_validates() {
    let w = world();
    let v = Validator::new(Arc::new(RecursorMetrics::default()));
    let q = n("www.example.");
    let answers = signed(&w.example, vec![rec("www.example.", RData::A(A(Ipv4Addr::new(192, 0, 2, 10))))]);
    let r = v.validate(&input(&q, ResponseCode::NoError, &answers, &[]), &w.trust, &[], &w.mock, NOW).await;
    assert_eq!(r.security, Security::Secure);
    let _ = &w.root;
}

#[tokio::test(flavor = "current_thread")]
async fn broken_signature_is_bogus_ede_6() {
    let w = world();
    let v = Validator::new(Arc::new(RecursorMetrics::default()));
    let q = n("www.example.");
    let mut answers = signed(&w.example, vec![rec("www.example.", RData::A(A(Ipv4Addr::new(192, 0, 2, 10))))]);
    answers[0].data = RData::A(A(Ipv4Addr::new(6, 6, 6, 6)));
    let r = v.validate(&input(&q, ResponseCode::NoError, &answers, &[]), &w.trust, &[], &w.mock, NOW).await;
    assert_eq!(r.security, Security::Bogus(Ede { code: 6, text: "bad signature for www.example. A".into() }));
}

#[tokio::test(flavor = "current_thread")]
async fn expired_signature_is_bogus_ede_7() {
    let w = world();
    let v = Validator::new(Arc::new(RecursorMetrics::default()));
    let q = n("www.example.");
    let set = vec![rec("www.example.", RData::A(A(Ipv4Addr::new(192, 0, 2, 10))))];
    let mut answers = set.clone();
    answers.push(w.example.sign(&set, INC - 90_000, INC - 86_400));
    let r = v.validate(&input(&q, ResponseCode::NoError, &answers, &[]), &w.trust, &[], &w.mock, NOW).await;
    assert!(matches!(r.security, Security::Bogus(Ede { code: 7, .. })), "{:?}", r.security);
}

#[tokio::test(flavor = "current_thread")]
async fn missing_rrsig_in_signed_zone_is_bogus_ede_10() {
    let w = world();
    let v = Validator::new(Arc::new(RecursorMetrics::default()));
    let q = n("www.example.");
    let answers = vec![rec("www.example.", RData::A(A(Ipv4Addr::new(192, 0, 2, 10))))];
    let r = v.validate(&input(&q, ResponseCode::NoError, &answers, &[]), &w.trust, &[], &w.mock, NOW).await;
    assert!(matches!(r.security, Security::Bogus(Ede { code: 10, .. })), "{:?}", r.security);
}

#[tokio::test(flavor = "current_thread")]
async fn proven_unsigned_delegation_is_insecure_and_unproven_is_bogus_ede_12() {
    let w = world();
    let v = Validator::new(Arc::new(RecursorMetrics::default()));
    let q = n("www.plain.example.");
    let answers = vec![rec("www.plain.example.", RData::A(A(Ipv4Addr::new(192, 0, 2, 20))))];
    let r = v.validate(&input(&q, ResponseCode::NoError, &answers, &[]), &w.trust, &[], &w.mock, NOW).await;
    assert_eq!(r.security, Security::Insecure(None));
    let q = n("www.nodenial.example.");
    let answers = vec![rec("www.nodenial.example.", RData::A(A(Ipv4Addr::new(192, 0, 2, 21))))];
    let r = v.validate(&input(&q, ResponseCode::NoError, &answers, &[]), &w.trust, &[], &w.mock, NOW).await;
    assert!(matches!(r.security, Security::Bogus(Ede { code: 12, .. })), "{:?}", r.security);
}

#[tokio::test(flavor = "current_thread")]
async fn unsupported_ds_algorithm_is_insecure_ede_1() {
    let mut w = world();
    let bad = DS::new(4242, Algorithm::Unknown(200), DigestType::SHA256, vec![0; 32]);
    w.mock.sets.insert((n("example."), RecordType::DS), FetchedSet { rcode: ResponseCode::NoError, answers: signed(&w.root, vec![rec("example.", RData::DNSSEC(DNSSECRData::DS(bad)))]), authorities: vec![] });
    let v = Validator::new(Arc::new(RecursorMetrics::default()));
    let q = n("www.example.");
    let answers = signed(&w.example, vec![rec("www.example.", RData::A(A(Ipv4Addr::new(192, 0, 2, 10))))]);
    let r = v.validate(&input(&q, ResponseCode::NoError, &answers, &[]), &w.trust, &[], &w.mock, NOW).await;
    assert!(matches!(r.security, Security::Insecure(Some(Ede { code: 1, .. }))), "{:?}", r.security);
}

#[tokio::test(flavor = "current_thread")]
async fn negative_trust_anchor_makes_bogus_zone_insecure_without_fetching() {
    let w = world();
    let v = Validator::new(Arc::new(RecursorMetrics::default()));
    let q = n("www.example.");
    let answers = vec![rec("www.example.", RData::A(A(Ipv4Addr::new(6, 6, 6, 6))))];
    let ntas = vec![(n("example."), (NOW + 60) as i64)];
    let r = v.validate(&input(&q, ResponseCode::NoError, &answers, &[]), &w.trust, &ntas, &w.mock, NOW).await;
    assert_eq!(r.security, Security::Insecure(None));
    assert_eq!(*w.mock.calls.borrow(), 0);
    assert!(!under_nta(&q, &ntas, NOW + 61), "expired NTA no longer applies");
}

#[tokio::test(flavor = "current_thread")]
async fn signed_nxdomain_validates_and_feeds_aggressive_cache() {
    let w = world();
    let v = Validator::new(Arc::new(RecursorMetrics::default()));
    let chain = nsec_chain("example.", &[("example.", &[RecordType::SOA, RecordType::NS, RecordType::DNSKEY]), ("plain.example.", &[RecordType::NS]), ("www.example.", &[RecordType::A])]);
    let soa = rec("example.", RData::SOA(SOA::new(n("ns.example."), n("h.example."), 1, 3600, 600, 86400, 300)));
    let mut auth = signed(&w.example, vec![soa]);
    for (owner, nsec) in &chain { auth.extend(signed(&w.example, vec![Record::from_rdata(owner.clone(), 300, RData::DNSSEC(DNSSECRData::NSEC(nsec.clone())))])); }
    let q = n("abc.example.");
    let r = v.validate(&input(&q, ResponseCode::NXDomain, &[], &auth), &w.trust, &[], &w.mock, NOW).await;
    assert_eq!(r.security, Security::Secure);
    let (rcode, records) = v.nsec.synthesize(&n("abd.example."), RecordType::A, NOW).expect("synthesised");
    assert_eq!(rcode, ResponseCode::NXDomain);
    assert!(records.iter().any(|r| r.record_type() == RecordType::SOA));
    assert!(v.nsec.synthesize(&n("www.example."), RecordType::A, NOW).is_none(), "existing name is never denied");
    let stripped: Vec<Record> = auth.iter().filter(|r| r.record_type() != RecordType::NSEC).cloned().collect();
    let r = v.validate(&input(&n("abe.example."), ResponseCode::NXDomain, &[], &stripped), &w.trust, &[], &w.mock, NOW).await;
    assert!(matches!(r.security, Security::Bogus(Ede { code: 12, .. })), "{:?}", r.security);
    let _ = NS(n("x."));
}
```

- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib recursor::dnssec::validator_tests`; expect FAIL with "cannot find type `FetchedSet` in this scope".
- [ ] Implement `Validator::zone_state(zone)` (results cached in `zones` until `now + min(DNSKEY TTL, 3600)`; `Bogus` for 60 s):
  1. `(tp_name, tp) = trust.closest(zone)`; none → `Insecure(None)`.
  2. Start at the trust point: fetch `DNSKEY tp_name`; keys authorised by the trust point (`TrustPoint::Keys` equal keys, or `match_ds` against `TrustPoint::Ds`); `NoMatch`/empty → `Bogus(9 "no DNSKEY matches trust anchor for <zone>")`; `NoSupported` → `Insecure(Some(1))`; the DNSKEY RRset must verify with one of the authorised keys (`verify_rrset`) → otherwise `Bogus` per the `VerifyError` mapping; state = `Secure(all zone keys in the RRset)`.
  3. Walk labels from `tp_name` towards `zone`, one label at a time (`child = zone.trim_to(cur.num_labels() + 1)`): fetch `DS child`.
     - DS RRset present: verify with the current keys (failure → `Bogus`); `match_ds(child, DNSKEY child)` — `Matched(keys)` and the child DNSKEY RRset verifies with one of them → `Secure(child keys)`; `NoSupported` → `Insecure(Some(Ede{code: 2 or 1}))` (2 when every DS digest is unsupported, 1 otherwise); `NoMatch` → `Bogus(9)`.
     - NODATA / NXDOMAIN: verify the authority NSEC/NSEC3 records with the current keys; `nsec_proves_nodata(child, DS)` / `nsec3_proves_nodata` → `Proven` or `ProvenOptOut`: if the matching NSEC bitmap has `NS` → `Insecure(None)` (unsigned delegation) and stop; if no `NS` bit (empty non-terminal or name inside the same zone) → keep the current keys and continue; `InsecureIterations(n)` → `Insecure(Some(27))`; `BogusIterations(n)` → `Bogus(27)`; `NotProven` → `Bogus(12 "no proof of missing DS for <child>")`. NXDOMAIN with a valid proof → stop, keep current keys (names below do not exist).
     - `FetchError` → return `Bogus(Ede{23, "network error fetching DS <child>"})` wrapped by the caller as `Indeterminate`.
- [ ] Implement `Validator::validate`:
  1. `under_nta(qname, ntas, now)` (NTA domain is an ancestor-or-self and `expires_unix > now`) → `Insecure(None)` without fetching.
  2. Group `answers` and `authorities` into RRsets by `(owner lowercase, type)`, RRSIGs attached by `type_covered`.
  3. For every answer RRset (the CNAME/DNAME chain and the final set): signer = RRSIG `signer_name` if any RRSIG exists, else the zone of the owner is computed by `zone_state(owner)` walking to the deepest secure zone reached. `zone_state(signer)` must be `Secure(keys)` → `verify_rrset`; RRSIG missing while the zone is `Secure` → `Bogus(10 "RRSIGs missing for <owner> <type>")`; `Insecure` → that RRset is insecure; wildcard-expanded signatures require `nsec_proves_no_closer_match` / NSEC3 next-closer coverage from the authority section, else `Bogus(12)`. Synthesised CNAMEs from a validated DNAME are accepted without RRSIG.
  4. Negative answers (`NXDomain`, or `NoError` with no RRset for `qname`/`qtype` at the end of the chain): the SOA and every NSEC/NSEC3 RRset in `authorities` are verified as in step 3; then `nsec_proves_nxdomain` / `nsec_proves_nodata` (or the NSEC3 variants when NSEC3 records are present) for the last name in the chain → `Proven` → secure; `ProvenOptOut` → `Insecure(None)`; iteration outcomes as in `zone_state`; `NotProven` → `Bogus(12 "NSEC missing: <reason>")`. When the proof is `Proven` and the records verified, call `self.nsec.insert_secure(zone, soa, denial_records, now)`.
  5. Combine: any `Bogus` → `Bogus` (first error), any `Indeterminate` → `Indeterminate`, any `Insecure` → `Insecure` (first EDE), else `Secure`; `ttl_cap = min capped_ttl` over verified RRsets. Update `metrics.dnssec_secure/insecure/bogus/indeterminate` and `dnssec_bogus_by_ede[code]` (codes ≥ 32 counted in slot 0).
- [ ] Implement `AggressiveNsecCache` (`parking_lot::Mutex<HashMap<Name, ZoneDenials>>`, max 10 000 zones, oldest zone evicted): `insert_secure` stores each NSEC (owner, next, bitmap, record + RRSIG, `expires = now + min(TTL, SOA minimum)`) and each NSEC3 by owner hash, plus the SOA RRset with RRSIG. `synthesize(qname, qtype)`: find the deepest cached zone containing `qname`; with cached NSECs run `nsec_proves_nxdomain` (→ `NXDomain`) then `nsec_proves_nodata` (→ `NoError`) over the unexpired records; for NSEC3 zones the same with NSEC3 functions, never using opt-out records; return the SOA + the NSEC/NSEC3 records used + RRSIGs; `None` when no proof or no SOA; increment `dnssec_aggressive_synthesized`.
- [ ] Wire into `dispatch::resolve_miss`:
  - `validation_enabled = rt.dnssec.validation && !(route is ForwardZone with validate == false)`.
  - Before resolving, when `rt.params.aggressive_nsec && validation_enabled && !q.checking_disabled`, `state.validator.nsec.synthesize(..)` → `Some` returns that answer (`security = Secure`, cacheable).
  - After a successful resolution: when `q.checking_disabled` → no validation, `SecurityTag::None`, AD=0 (RFC 4035 §3.2.2 CD honoured; the M1 cache key already separates CD). Otherwise run `validator.validate` with `TrustPoints` from `state.anchors.trust_points()` (Task 8; until then `TrustPoints::from_config(&rt.dnssec.anchors)`), `rt.dnssec.ntas`, and `RoutedFetcher`. `Secure` → `build_response(.., secure: true)`; `Insecure(ede)` → AD=0 and EDE attached; `Bogus(ede)`/`Indeterminate(ede)` → SERVFAIL, `cacheable: false`, EDE attached.
  - `RoutedFetcher::fetch` builds `FetchedSet` from: `Route::Recursive` → `recursor.fetch(name, rtype, params, budget)`; `Route::ForwardZone` → the forward-zone exchange with DO=1, CD=1; `Route::Forward` → `upstream.forward` of a query with RD=1, DO=1, CD=1 (validation in forward mode requires DO upstream; an upstream that strips RRSIGs produces `Bogus(10)`).
- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib recursor::dnssec::validator_tests`; expect PASS (8 tests).
- [ ] Commit: `git add engine/src/recursor && git commit -m "feat(dnssec): chain-of-trust validation, EDE, NTAs and aggressive NSEC"`.

## Task 8: Trust anchor store with RFC 5011 automated rollover

Files:

- `engine/src/recursor/dnssec/anchors.rs` — persisted anchor state in `state_dir/trust-anchors.json`, merge with mgmt-delivered anchors, RFC 5011 state machine, refresh scheduling, status for `Stats`.
- `engine/src/recursor/dnssec/anchors_tests.rs` — unit tests.
- `engine/src/control.rs` — after each applied snapshot call `anchors.merge_config`; every 10 s `Stats` includes `DnssecStats` and `RecursionStats`.
- `engine/src/main.rs` — spawns `anchors::refresh_loop` on the control runtime.
- `engine/src/telemetry/metrics.rs` — `nexora_dnssec_trust_anchor_keys{zone,state}`, `nexora_dnssec_trust_anchor_last_refresh_success_timestamp_seconds{zone}`, `nexora_dnssec_negative_trust_anchors`.

Interfaces:

```rust
#[derive(Serialize, Deserialize, Clone, Copy, Debug, PartialEq, Eq)]
pub enum KeyState { Configured, AddPend, Valid, Missing, Revoked }
#[derive(Serialize, Deserialize, Clone, Debug)]
pub struct AnchorKey { pub key_tag: u16, pub algorithm: u8, pub ds: Option<String>, pub dnskey_b64: Option<String>, pub state: KeyState, pub hold_down_until: i64, pub from_config: bool }
#[derive(Serialize, Deserialize, Clone, Debug, Default)]
pub struct ZoneAnchors { pub keys: Vec<AnchorKey>, pub last_success: i64, pub next_refresh: i64, pub last_error: String }
#[derive(Serialize, Deserialize, Clone, Debug, Default)]
pub struct AnchorFile { pub zones: BTreeMap<String, ZoneAnchors> }
pub const ADD_HOLD_DOWN: i64 = 30 * 86_400;
pub const REMOVE_HOLD_DOWN: i64 = 30 * 86_400;
pub struct Observation<'a> { pub dnskeys: &'a [DNSKEY], pub validated_by_trusted: bool, pub revoked_self_signed: &'a [u16], pub orig_ttl: u32, pub sig_expiration: i64 }
pub fn apply_rfc5011(z: &mut ZoneAnchors, zone: &Name, obs: &Observation<'_>, now: i64);
pub fn refresh_interval(orig_ttl: u32, sig_expiration: i64, now: i64) -> i64;   // RFC 5011 §2.3 active refresh
pub fn retry_interval(orig_ttl: u32, sig_expiration: i64, now: i64) -> i64;
pub struct TrustAnchorStore { path: PathBuf, file: parking_lot::Mutex<AnchorFile>, points: arc_swap::ArcSwap<TrustPoints>, rfc5011: AtomicBool }
impl TrustAnchorStore {
    pub fn open(path: PathBuf) -> Arc<Self>;                         // missing/corrupt file -> empty state, error kept in last_error
    pub fn merge_config(&self, anchors: &[pb::TrustAnchor], rfc5011: bool, now: i64) -> std::io::Result<()>;
    pub fn trust_points(&self) -> Arc<TrustPoints>;                  // Configured(DS) + Valid(DNSKEY) + Missing keys; never AddPend/Revoked
    pub fn record_observation(&self, zone: &Name, obs: &Observation<'_>, now: i64) -> std::io::Result<()>;
    pub fn record_failure(&self, zone: &Name, error: &str, now: i64);
    pub fn status(&self) -> Vec<pb::TrustAnchorStatus>;
}
pub async fn refresh_loop(store: Arc<TrustAnchorStore>, state: Arc<RecursorState>, runtime: Arc<arc_swap::ArcSwap<crate::runtime::Runtime>>);
```

- [ ] Create `engine/src/recursor/dnssec/anchors_tests.rs`:

```rust
use super::anchors::*;
use super::testsign::TestKey;
use crate::pb;
use hickory_proto::dnssec::rdata::DNSKEY;
use hickory_proto::rr::Name;

const DAY: i64 = 86_400;
const T0: i64 = 1_800_000_000;

fn ds_text(k: &TestKey) -> String {
    let ds = k.ds();
    format!("{} {} {} {}", ds.key_tag(), u8::from(ds.algorithm()), u8::from(ds.digest_type()), data_encoding::HEXUPPER.encode(ds.digest()))
}

fn obs<'a>(keys: &'a [DNSKEY], revoked: &'a [u16]) -> Observation<'a> {
    Observation { dnskeys: keys, validated_by_trusted: true, revoked_self_signed: revoked, orig_ttl: 172_800, sig_expiration: T0 + 10 * DAY }
}

fn store_with(k: &TestKey) -> (tempfile::TempDir, std::sync::Arc<TrustAnchorStore>) {
    let dir = tempfile::tempdir().unwrap();
    let s = TrustAnchorStore::open(dir.path().join("trust-anchors.json"));
    s.merge_config(&[pb::TrustAnchor { zone: ".".into(), ds: ds_text(k) }], true, T0).unwrap();
    (dir, s)
}

fn state_of(s: &TrustAnchorStore, tag: u16) -> Option<KeyState> {
    s.status().into_iter().find(|a| a.key_tag == tag as u32).map(|a| match a.state {
        x if x == pb::TrustAnchorState::Configured as i32 => KeyState::Configured,
        x if x == pb::TrustAnchorState::AddPend as i32 => KeyState::AddPend,
        x if x == pb::TrustAnchorState::Valid as i32 => KeyState::Valid,
        x if x == pb::TrustAnchorState::Missing as i32 => KeyState::Missing,
        _ => KeyState::Revoked,
    })
}

#[test]
fn configured_ds_becomes_valid_when_observed() {
    let old = TestKey::generate(".", true);
    let (_d, s) = store_with(&old);
    let tag = old.dnskey.calculate_key_tag().unwrap();
    assert_eq!(state_of(&s, tag), Some(KeyState::Configured));
    s.record_observation(&Name::root(), &obs(&[old.dnskey.clone()], &[]), T0).unwrap();
    assert_eq!(state_of(&s, tag), Some(KeyState::Valid));
}

#[test]
fn new_key_waits_add_hold_down_then_becomes_trusted() {
    let old = TestKey::generate(".", true);
    let new = TestKey::generate(".", true);
    let (_d, s) = store_with(&old);
    let keys = [old.dnskey.clone(), new.dnskey.clone()];
    let new_tag = new.dnskey.calculate_key_tag().unwrap();
    s.record_observation(&Name::root(), &obs(&keys, &[]), T0).unwrap();
    assert_eq!(state_of(&s, new_tag), Some(KeyState::AddPend));
    assert_eq!(s.trust_points().zones[0].1.key_count(), 1, "AddPend is not trusted");
    s.record_observation(&Name::root(), &obs(&keys, &[]), T0 + 29 * DAY).unwrap();
    assert_eq!(state_of(&s, new_tag), Some(KeyState::AddPend));
    s.record_observation(&Name::root(), &obs(&keys, &[]), T0 + 30 * DAY).unwrap();
    assert_eq!(state_of(&s, new_tag), Some(KeyState::Valid));
    assert_eq!(s.trust_points().zones[0].1.key_count(), 2);
}

#[test]
fn pending_key_that_disappears_is_forgotten_and_unvalidated_observations_change_nothing() {
    let old = TestKey::generate(".", true);
    let new = TestKey::generate(".", true);
    let (_d, s) = store_with(&old);
    let new_tag = new.dnskey.calculate_key_tag().unwrap();
    let mut o = obs(&[], &[]);
    let both = [old.dnskey.clone(), new.dnskey.clone()];
    o.dnskeys = &both;
    o.validated_by_trusted = false;
    s.record_observation(&Name::root(), &o, T0).unwrap();
    assert_eq!(state_of(&s, new_tag), None, "an RRset not signed by a trusted key is ignored");
    s.record_observation(&Name::root(), &obs(&both, &[]), T0).unwrap();
    s.record_observation(&Name::root(), &obs(&[old.dnskey.clone()], &[]), T0 + DAY).unwrap();
    assert_eq!(state_of(&s, new_tag), None);
}

#[test]
fn revoked_self_signed_key_is_revoked_and_removed_after_hold_down() {
    let old = TestKey::generate(".", true);
    let (_d, s) = store_with(&old);
    let tag = old.dnskey.calculate_key_tag().unwrap();
    s.record_observation(&Name::root(), &obs(&[old.dnskey.clone()], &[]), T0).unwrap();
    s.record_observation(&Name::root(), &obs(&[old.dnskey.clone()], &[tag]), T0 + DAY).unwrap();
    assert_eq!(state_of(&s, tag), Some(KeyState::Revoked));
    assert_eq!(s.trust_points().zones.len(), 0, "a zone whose only key is revoked has no trust point");
    s.record_observation(&Name::root(), &obs(&[], &[]), T0 + DAY + 30 * DAY).unwrap();
    assert_eq!(state_of(&s, tag), None);
}

#[test]
fn state_survives_restart_and_mgmt_removal_drops_zone() {
    let old = TestKey::generate(".", true);
    let (d, s) = store_with(&old);
    let tag = old.dnskey.calculate_key_tag().unwrap();
    s.record_observation(&Name::root(), &obs(&[old.dnskey.clone()], &[]), T0).unwrap();
    drop(s);
    let s = TrustAnchorStore::open(d.path().join("trust-anchors.json"));
    assert_eq!(state_of(&s, tag), Some(KeyState::Valid));
    s.merge_config(&[], true, T0 + 1).unwrap();
    assert!(s.status().is_empty());
}

#[test]
fn refresh_and_retry_intervals_follow_rfc5011_section_2_3() {
    assert_eq!(refresh_interval(172_800, T0 + 10 * DAY, T0), DAY);          // min(15d, ttl/2=1d, exp/2=5d)
    assert_eq!(refresh_interval(600, T0 + 10 * DAY, T0), 3600);             // floor 1h
    assert_eq!(retry_interval(172_800, T0 + 10 * DAY, T0), 17_280);         // min(1d, ttl/10, exp/10)
    assert_eq!(retry_interval(600, T0 + 10 * DAY, T0), 3600);
}
```

- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib recursor::dnssec::anchors_tests`; expect FAIL with "cannot find struct, variant or union type `Observation` in this scope".
- [ ] Implement `apply_rfc5011` (no-op except `last_error = "DNSKEY RRset not validated by a trusted key"` when `!obs.validated_by_trusted`):

```rust
pub fn apply_rfc5011(z: &mut ZoneAnchors, zone: &Name, obs: &Observation<'_>, now: i64) {
    if !obs.validated_by_trusted { z.last_error = "DNSKEY RRset not validated by a trusted key".into(); return; }
    let seen: Vec<(u16, u8, &DNSKEY)> = obs.dnskeys.iter().filter(|k| k.secure_entry_point() && k.zone_key())
        .filter_map(|k| k.calculate_key_tag().ok().map(|t| (t, u8::from(k.algorithm()), k))).collect();
    for key in z.keys.iter_mut() {
        let hit = seen.iter().find(|(t, a, k)| *t == key.key_tag && *a == key.algorithm
            && (key.dnskey_b64.is_none() || key.dnskey_b64.as_deref() == Some(&b64(k))));
        match (key.state, hit) {
            (_, Some(_)) if obs.revoked_self_signed.contains(&key.key_tag) && key.state != KeyState::Revoked => {
                key.state = KeyState::Revoked; key.hold_down_until = now + REMOVE_HOLD_DOWN;
            }
            (KeyState::Configured, Some((_, _, k))) => {
                if key.ds.as_deref().map(|d| ds_matches(d, zone, k)).unwrap_or(true) { key.state = KeyState::Valid; key.dnskey_b64 = Some(b64(k)); }
            }
            (KeyState::AddPend, Some(_)) if now >= key.hold_down_until => key.state = KeyState::Valid,
            (KeyState::Missing, Some(_)) => key.state = KeyState::Valid,
            (KeyState::Valid, None) => key.state = KeyState::Missing,
            _ => {}
        }
    }
    // AddPend keys that vanished return to Start (forgotten); Revoked keys past the remove hold-down are removed
    z.keys.retain(|k| !(k.state == KeyState::AddPend && !seen.iter().any(|(t, a, _)| *t == k.key_tag && *a == k.algorithm))
        && !(k.state == KeyState::Revoked && now >= k.hold_down_until));
    for (t, a, k) in &seen {
        let known = z.keys.iter().any(|x| x.key_tag == *t && x.algorithm == *a);
        if !known && !k.revoke() {
            z.keys.push(AnchorKey { key_tag: *t, algorithm: *a, ds: None, dnskey_b64: Some(b64(k)), state: KeyState::AddPend, hold_down_until: now + ADD_HOLD_DOWN, from_config: false });
        }
    }
    z.last_success = now;
    z.last_error.clear();
    z.next_refresh = now + refresh_interval(obs.orig_ttl, obs.sig_expiration, now);
}
```

`b64(k)` is `data_encoding::BASE64.encode(&k.to_bytes().unwrap())`; `ds_matches` parses the DS text with `snapshot_m3::parse_ds` and compares key tag/algorithm and `k.to_digest(zone, digest_type)` bytes. The caller (`refresh_loop`) verifies that a REVOKE-flagged key signs the DNSKEY RRset (RFC 5011 §2.1), then passes that key in `dnskeys` with the REVOKE bit cleared (`DNSKEY::with_flags(flags & !0x0080, public_key)`) and lists the key tag of that cleared form in `revoked_self_signed`, so it matches the stored anchor.

- [ ] Implement intervals: `refresh_interval = max(3600, min(15*DAY, orig_ttl/2, (sig_expiration-now)/2))`; `retry_interval = max(3600, min(DAY, orig_ttl/10, (sig_expiration-now)/10))`.
- [ ] Implement `TrustAnchorStore`: `merge_config` adds a `Configured` key (`from_config: true`, key tag/algorithm from the DS) for every configured DS not already present by key tag+algorithm; removes zones not present in `anchors`; removes `from_config` keys whose DS is no longer configured unless their state is `Valid` (an authenticated rollover key stays); persists with write-temp + `fsync` + rename (same pattern as `snapshot.binpb`) and republishes `points`. `trust_points()` builds `TrustPoint::Ds` from `Configured` keys and `TrustPoint::Keys` from `Valid`/`Missing` keys (both kinds are merged per zone; add `TrustPoint::key_count(&self) -> usize` counting DS entries plus keys); zones with nothing trusted are omitted. `status()` maps to `pb::TrustAnchorStatus`.
- [ ] Implement `refresh_loop`: every 60 s, for each zone with `rfc5011` enabled and `now >= next_refresh`: fetch `DNSKEY <zone>` through a `RoutedFetcher` built from the current runtime (budget 100/32); verify the RRset with current trust points (`validated_by_trusted`), find `REVOKE`-flagged SEP keys whose own signature over the RRset verifies (`revoked_self_signed`), take `orig_ttl`/`sig_expiration` from the verifying RRSIG, and call `record_observation`. On any failure call `record_failure` (sets `last_error`, `next_refresh = now + retry_interval`, increments `metrics.trust_anchor_refresh_failures`). `dispatch::resolve_miss` now uses `state.anchors.trust_points()`.
- [ ] `control.rs`: after a snapshot applies, `state.anchors.merge_config(&dnssec.trust_anchors, dnssec.rfc5011, now)`; failures to persist are reported in `DnssecStats.trust_anchors[].last_error` and never reject the snapshot. `Stats` gains `recursion: RecursionStats` (from `RecursorMetrics` and `infra.len()`), `dnssec: DnssecStats` (counters, `anchors.status()`, `active_negative_trust_anchors` = unexpired NTAs).
- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib recursor::dnssec::anchors_tests`; expect PASS (6 tests).
- [ ] Commit: `git add engine && git commit -m "feat(dnssec): RFC 5011 trust anchor store persisted in state_dir"`.

## Task 9: RPZ policy engine — parsing, triggers, precedence, actions and query-path hooks

Files:

- `engine/src/recursor/rpz/mod.rs` — declarations.
- `engine/src/recursor/rpz/parse.rs` — RPZ zone text/records → rules.
- `engine/src/recursor/rpz/index.rs` — `RpzZoneIndex`, `RpzSet`, trigger lookup and precedence.
- `engine/src/recursor/rpz/apply.rs` — action → response synthesis.
- `engine/src/recursor/rpz/rpz_tests.rs` — unit tests.
- `engine/src/server/mod.rs` — query-phase check before the cache lookup.
- `engine/src/recursor/dispatch.rs` — response-phase check after validation.
- `engine/src/cache.rs` — hit path rejects entries whose `policy_generation` differs from `RecursorState.policy_generation`.
- `engine/src/control.rs` — increments `policy_generation` when resolution/DNSSEC snapshot sections change.

Interfaces:

```rust
// parse.rs
#[derive(Clone, Debug, PartialEq)] pub enum CnameTarget { Name(Name), WildcardSuffix(Name) }
#[derive(Clone, Debug, PartialEq)] pub struct LocalData { pub records: Vec<(RecordType, u32, RData)>, pub cname: Option<(u32, CnameTarget)> }
#[derive(Clone, Debug, PartialEq)] pub enum RpzAction { Nxdomain, Nodata, Passthru, Drop, TcpOnly, LocalData(Arc<LocalData>) }
#[derive(Clone, Debug, PartialEq)] pub enum Trigger { Qname { name: Name, wildcard: bool }, ClientIp(ipnet::IpNet), ResponseIp(ipnet::IpNet), Nsdname { name: Name, wildcard: bool }, Nsip(ipnet::IpNet) }
#[derive(Clone, Debug)] pub struct ParsedRpz { pub origin: Name, pub serial: u32, pub soa: Record, pub rules: Vec<(Trigger, RpzAction)>, pub skipped: u64, pub records: u64 }
pub fn parse_rpz_text(origin: &Name, text: &str) -> Result<ParsedRpz, String>;
pub fn parse_rpz_records(origin: &Name, records: &[Record]) -> Result<ParsedRpz, String>;
pub fn decode_ip_trigger(labels: &[String]) -> Option<ipnet::IpNet>; // labels in zone order, e.g. ["32","66","2","0","192"]
// index.rs
pub struct RpzZoneIndex { pub id: String, pub origin: Name, pub soa: Record, pub serial: u32, pub records: u64, pub skipped: u64, pub policy_override: i32, pub hits: AtomicU64, /* lookup tables */ }
impl RpzZoneIndex { pub fn build(id: &str, parsed: &ParsedRpz, policy_override: i32) -> Self; pub fn has_query_triggers(&self) -> bool; pub fn has_response_triggers(&self) -> bool; }
pub struct RpzSet { pub zones: Vec<Arc<RpzZoneIndex>>, pub has_query_triggers: bool, pub has_response_triggers: bool }
#[derive(Debug)] pub enum QueryPhase<'a> { NoMatch, Hit { zone: usize, action: &'a RpzAction }, Deferred { zone: usize, action: &'a RpzAction } }
impl RpzSet {
    pub fn new(zones: Vec<Arc<RpzZoneIndex>>) -> Self;
    pub fn check_query(&self, qname_wire_lower: &[u8], client: IpAddr) -> QueryPhase<'_>;
    pub fn check_response(&self, upto_zone: usize, chain: &[Name], answers: &[Record], ns_names: &[Name], ns_addrs: &[IpAddr]) -> Option<(usize, &RpzAction)>;
    pub fn effective_action<'a>(&'a self, zone: usize, action: &'a RpzAction) -> Option<RpzAction>; // applies policy_override; None = DISABLED
}
// apply.rs
#[derive(Debug)] pub enum PolicyOutcome { Respond { wire: Vec<u8>, ede: Ede }, Drop, Truncate { wire: Vec<u8> }, Passthru, ChaseCname { cname: Record, target: Name } }
pub fn apply_action(qname: &Name, qtype: RecordType, over_tcp: bool, zone: &RpzZoneIndex, action: &RpzAction) -> PolicyOutcome;
```

Precedence (draft-vixie-dnsop-dns-rpz): zones in snapshot order, the first zone with a matching trigger wins; within one zone CLIENT-IP > QNAME > response IP > NSDNAME > NSIP; exact QNAME/NSDNAME beats wildcard, deeper wildcard beats shallower; longest IP prefix wins. QNAME triggers are also checked against every CNAME target in the resolved chain. RPZ runs after Nexora's own blocklist/allowlist, per-client policy and rewrites (M1/M2), so a Nexora block decision is final.

- [ ] Create `engine/src/recursor/rpz/rpz_tests.rs`:

```rust
use super::apply::*;
use super::index::*;
use super::parse::*;
use crate::pb::RpzPolicyOverride;
use hickory_proto::op::{Message, ResponseCode};
use hickory_proto::rr::{rdata::{A, CNAME}, Name, RData, Record, RecordType};
use std::net::{IpAddr, Ipv4Addr};
use std::sync::Arc;

const ZONE: &str = "$TTL 60
@ SOA ns.rpz. hostmaster.rpz. 7 60 60 86400 60
@ NS ns.rpz.
bad.example CNAME .
*.bad.example CNAME *.
pass.bad.example CNAME rpz-passthru.
nodata.example CNAME *.
drop.example CNAME rpz-drop.
tcp.example CNAME rpz-tcp-only.
local.example A 10.9.9.9
local.example TXT \"blocked\"
alias.example CNAME walled.garden.
wild.example CNAME *.garden.
32.66.2.0.192.rpz-ip CNAME .
48.zz.db8.2001.rpz-ip CNAME .
32.2.0.0.127.rpz-client-ip CNAME rpz-tcp-only.
ns.evil.rpz-nsdname CNAME .
24.0.113.203.rpz-nsip CNAME .
garbage.rpz-ip CNAME .
";

fn n(s: &str) -> Name { Name::from_ascii(s).unwrap() }
fn w(s: &str) -> Vec<u8> { n(s).to_lowercase().to_bytes().unwrap() }
fn client(last: u8) -> IpAddr { IpAddr::V4(Ipv4Addr::new(127, 0, 0, last)) }

fn zone(id: &str, origin: &str, text: &str, ov: RpzPolicyOverride) -> Arc<RpzZoneIndex> {
    let o = n(origin);
    Arc::new(RpzZoneIndex::build(id, &parse_rpz_text(&o, text).unwrap(), ov as i32))
}

#[test]
fn parses_triggers_actions_and_counts_skipped() {
    let p = parse_rpz_text(&n("rpz.local."), ZONE).unwrap();
    assert_eq!(p.serial, 7);
    assert_eq!(p.skipped, 1);
    assert!(p.rules.contains(&(Trigger::ResponseIp("2001:db8::/48".parse().unwrap()), RpzAction::Nxdomain)));
    assert!(p.rules.contains(&(Trigger::Qname { name: n("bad.example."), wildcard: true }, RpzAction::Nodata)));
    assert!(p.rules.contains(&(Trigger::Nsdname { name: n("ns.evil."), wildcard: false }, RpzAction::Nxdomain)));
    assert!(p.rules.contains(&(Trigger::Nsip("203.0.113.0/24".parse().unwrap()), RpzAction::Nxdomain)));
    assert!(p.rules.contains(&(Trigger::ClientIp("127.0.0.2/32".parse().unwrap()), RpzAction::TcpOnly)));
}

#[test]
fn include_directive_is_rejected() {
    let err = parse_rpz_text(&n("rpz.local."), "$INCLUDE /etc/passwd\n@ SOA a. b. 1 1 1 1 1\n").unwrap_err();
    assert_eq!(err, "$INCLUDE is not allowed in RPZ zones");
}

#[test]
fn ip_trigger_decoding() {
    let l = |s: &str| s.split('.').map(String::from).collect::<Vec<_>>();
    assert_eq!(decode_ip_trigger(&l("24.0.2.0.192")), Some("192.0.2.0/24".parse().unwrap()));
    assert_eq!(decode_ip_trigger(&l("128.1.zz.2001")), Some("2001::1/128".parse().unwrap()));
    assert_eq!(decode_ip_trigger(&l("33.1.2.0.192")), None);
    assert_eq!(decode_ip_trigger(&l("24.0.2.0.300")), None);
}

#[test]
fn qname_exact_wildcard_and_client_ip_precedence() {
    let set = RpzSet::new(vec![zone("z", "rpz.local.", ZONE, RpzPolicyOverride::Given)]);
    assert!(matches!(set.check_query(&w("bad.example."), client(1)), QueryPhase::Hit { zone: 0, action: RpzAction::Nxdomain }));
    assert!(matches!(set.check_query(&w("x.y.bad.example."), client(1)), QueryPhase::Hit { action: RpzAction::Nodata, .. }));
    assert!(matches!(set.check_query(&w("pass.bad.example."), client(1)), QueryPhase::Hit { action: RpzAction::Passthru, .. }));
    assert!(matches!(set.check_query(&w("notbad.example."), client(1)), QueryPhase::NoMatch));
    assert!(matches!(set.check_query(&w("bad.example."), client(2)), QueryPhase::Hit { action: RpzAction::TcpOnly, .. }), "CLIENT-IP beats QNAME");
}

#[test]
fn response_triggers_ip_before_nsdname_before_nsip() {
    let set = RpzSet::new(vec![zone("z", "rpz.local.", ZONE, RpzPolicyOverride::Given)]);
    let ans = vec![Record::from_rdata(n("www.example."), 60, RData::A(A(Ipv4Addr::new(192, 0, 2, 66))))];
    let chain = vec![n("www.example.")];
    assert!(matches!(set.check_response(1, &chain, &ans, &[], &[]), Some((0, RpzAction::Nxdomain))));
    let ok = vec![Record::from_rdata(n("www.example."), 60, RData::A(A(Ipv4Addr::new(192, 0, 2, 67))))];
    assert!(set.check_response(1, &chain, &ok, &[], &[]).is_none());
    assert!(set.check_response(1, &chain, &ok, &[n("NS.evil.")], &[]).is_some());
    assert!(set.check_response(1, &chain, &ok, &[], &["203.0.113.9".parse().unwrap()]).is_some());
    let cname_chain = vec![n("www.example."), n("bad.example.")];
    assert!(matches!(set.check_response(1, &cname_chain, &ok, &[], &[]), Some((0, RpzAction::Nxdomain))), "QNAME trigger on a CNAME target");
}

#[test]
fn zone_order_decides_and_earlier_response_triggers_defer() {
    let allow = "@ SOA a. b. 1 60 60 60 60\nallow.example CNAME rpz-passthru.\n";
    let deny = "@ SOA a. b. 1 60 60 60 60\nallow.example CNAME .\nx.example CNAME .\n";
    let set = RpzSet::new(vec![zone("a", "allow.rpz.", allow, RpzPolicyOverride::Given), zone("d", "deny.rpz.", deny, RpzPolicyOverride::Given)]);
    assert!(matches!(set.check_query(&w("allow.example."), client(1)), QueryPhase::Hit { zone: 0, action: RpzAction::Passthru }));
    let set = RpzSet::new(vec![zone("d", "deny.rpz.", deny, RpzPolicyOverride::Given), zone("a", "allow.rpz.", allow, RpzPolicyOverride::Given)]);
    assert!(matches!(set.check_query(&w("allow.example."), client(1)), QueryPhase::Hit { zone: 0, action: RpzAction::Nxdomain }));
    let ipzone = "@ SOA a. b. 1 60 60 60 60\n32.66.2.0.192.rpz-ip CNAME *.\n";
    let set = RpzSet::new(vec![zone("i", "ip.rpz.", ipzone, RpzPolicyOverride::Given), zone("d", "deny.rpz.", deny, RpzPolicyOverride::Given)]);
    assert!(matches!(set.check_query(&w("x.example."), client(1)), QueryPhase::Deferred { zone: 1, .. }));
    let ans = vec![Record::from_rdata(n("x.example."), 60, RData::A(A(Ipv4Addr::new(192, 0, 2, 66))))];
    assert!(matches!(set.check_response(1, &[n("x.example.")], &ans, &[], &[]), Some((0, RpzAction::Nodata))));
}

#[test]
fn actions_synthesise_responses() {
    let z = zone("z", "rpz.local.", ZONE, RpzPolicyOverride::Given);
    let msg = |o: PolicyOutcome| match o { PolicyOutcome::Respond { wire, ede } => (Message::from_vec(&wire).unwrap(), ede), other => panic!("{other:?}") };
    let (m, ede) = msg(apply_action(&n("bad.example."), RecordType::A, false, &z, &RpzAction::Nxdomain));
    assert_eq!(m.metadata.response_code, ResponseCode::NXDomain);
    assert_eq!(m.authorities[0].record_type(), RecordType::SOA);
    assert_eq!(ede.code, 15);
    let local = match parse_rpz_text(&n("rpz.local."), ZONE).unwrap().rules.into_iter().find(|(t, _)| *t == Trigger::Qname { name: n("local.example."), wildcard: false }).unwrap().1 { a @ RpzAction::LocalData(_) => a, other => panic!("{other:?}") };
    let (m, ede) = msg(apply_action(&n("LOCAL.example."), RecordType::A, false, &z, &local));
    assert_eq!(m.answers[0].data, RData::A(A(Ipv4Addr::new(10, 9, 9, 9))));
    assert_eq!(m.answers[0].ttl, 60);
    assert_eq!(ede.code, 4);
    let (m, _) = msg(apply_action(&n("local.example."), RecordType::AAAA, false, &z, &local));
    assert_eq!((m.metadata.response_code, m.answers.len()), (ResponseCode::NoError, 0));
    assert!(matches!(apply_action(&n("tcp.example."), RecordType::A, false, &z, &RpzAction::TcpOnly), PolicyOutcome::Truncate { .. }));
    assert!(matches!(apply_action(&n("tcp.example."), RecordType::A, true, &z, &RpzAction::TcpOnly), PolicyOutcome::Passthru));
    assert!(matches!(apply_action(&n("drop.example."), RecordType::A, false, &z, &RpzAction::Drop), PolicyOutcome::Drop));
    let wild = parse_rpz_text(&n("rpz.local."), ZONE).unwrap().rules.into_iter().find(|(t, _)| *t == Trigger::Qname { name: n("wild.example."), wildcard: false }).unwrap().1;
    match apply_action(&n("wild.example."), RecordType::A, false, &z, &wild) {
        PolicyOutcome::ChaseCname { cname, target } => { assert_eq!(target, n("wild.example.garden.")); assert_eq!(cname.data, RData::CNAME(CNAME(n("wild.example.garden.")))); }
        other => panic!("{other:?}"),
    }
}

#[test]
fn policy_override_replaces_or_disables_action() {
    let set = RpzSet::new(vec![zone("z", "rpz.local.", ZONE, RpzPolicyOverride::Nodata)]);
    assert_eq!(set.effective_action(0, &RpzAction::Nxdomain), Some(RpzAction::Nodata));
    let set = RpzSet::new(vec![zone("z", "rpz.local.", ZONE, RpzPolicyOverride::Disabled)]);
    assert_eq!(set.effective_action(0, &RpzAction::Nxdomain), None);
}
```

- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib recursor::rpz::rpz_tests`; expect FAIL with "unresolved import `super::apply`".
- [ ] Implement `parse.rs`:
  - `parse_rpz_text`: if any line, after trimming leading whitespace, starts with `$INCLUDE` (ASCII case-insensitive) → `Err("$INCLUDE is not allowed in RPZ zones")`; parse with `Parser::new(text, None, Some(origin.clone())).parse()`; flatten the `RecordSet`s into records and call `parse_rpz_records`.
  - `parse_rpz_records`: the apex SOA is required (`Err("RPZ zone has no SOA")`); apex NS/SOA are not rules. Every other owner must be under `origin` (else `skipped += 1`); relative labels = owner minus origin. Suffix dispatch on the last relative label: `rpz-client-ip` → `ClientIp`, `rpz-ip` → `ResponseIp`, `rpz-nsip` → `Nsip` (labels before the suffix through `decode_ip_trigger`, `None` → skipped), `rpz-nsdname` → `Nsdname` over the preceding labels (leading `*` → wildcard), anything else → `Qname` (leading `*` → wildcard). Records of one owner are grouped into one action: a single CNAME whose target (with `origin` stripped when it ends in `origin`) is the root → `Nxdomain`; `*.` → `Nodata`; `rpz-passthru.` or equal to the trigger name → `Passthru`; `rpz-drop.` → `Drop`; `rpz-tcp-only.` → `TcpOnly`; target whose first label is `*` → `LocalData { cname: WildcardSuffix(target minus the "*" label) }`; other CNAME target → `LocalData { cname: Name(target) }`; non-CNAME records → `LocalData { records }`; CNAME mixed with other types → skipped. `records` counts every non-apex record read.
  - `decode_ip_trigger`: first label = prefix length; IPv4 when exactly 5 labels, all octets decimal ≤ 255, prefix 1..=32, octets given least-significant first; IPv6 otherwise: remaining labels are hex words least-significant first, at most one `zz` meaning the run of zero words needed to make 8, prefix 1..=128; the network is truncated to the prefix (`IpNet::trunc`).
- [ ] Implement `index.rs` lookup tables per zone: `qname_exact`, `qname_wild`, `nsdname_exact`, `nsdname_wild` as `FxHashMap<Box<[u8]>, RpzAction>` keyed by lowercase wire names; `client_ip`, `resp_ip`, `nsip` as `IpTable { v4: FxHashMap<(u8, u32), RpzAction>, v6: FxHashMap<(u8, u128), RpzAction>, v4_lens: Vec<u8>, v6_lens: Vec<u8> }` with prefix lengths sorted descending; lookup masks the address for each present length (longest first). Name lookup over wire bytes without allocation:

```rust
fn lookup_name<'a>(exact: &'a FxHashMap<Box<[u8]>, RpzAction>, wild: &'a FxHashMap<Box<[u8]>, RpzAction>, wire: &[u8]) -> Option<&'a RpzAction> {
    if let Some(a) = exact.get(wire) { return Some(a); }
    // proper suffixes, longest (deepest) first: skip the first label, then the next ...
    let mut off = 0usize;
    while wire[off] != 0 {
        off += 1 + wire[off] as usize;
        if let Some(a) = wild.get(&wire[off..]) { return Some(a); }
    }
    None
}
```

`wild` keys are the wire form of the name after the `*` label, so `*.bad.example` matches `x.bad.example` and `x.y.bad.example` but not `bad.example`. `check_query`: iterate zones in order; for zone `i` check `client_ip` then QNAME; on a hit return `Deferred { zone: i, .. }` if any zone `< i` `has_response_triggers()`, else `Hit`. `check_response(upto_zone, ..)`: for zones `0..upto_zone` (when called for `NoMatch`, `upto_zone = zones.len()`), per zone in precedence order: QNAME over `chain[1..]`, response IP over A/AAAA answers, NSDNAME over `ns_names` (lowercased wire, computed once per call), NSIP over `ns_addrs`; first hit returns. `effective_action`: `policy_override` `GIVEN` → clone; `DISABLED` → `None`; others → the corresponding fixed action. Each hit increments `hits`.

- [ ] Implement `apply.rs`: responses are `Message::response(0, Query)`, RA=1, AA=0, AD=0, question `qname` lowercased. `Nxdomain`/`Nodata` → rcode NXDOMAIN/NOERROR with the zone SOA in the authority section (TTL = min(SOA TTL, SOA minimum)), EDE `{15, "rpz <origin>"}`. `LocalData`: records of `qtype` (or all when `qtype == ANY`) with owner = `qname` and their own TTL → EDE `{4, "rpz <origin>"}`; with `cname` → `ChaseCname { cname: CNAME record qname → target (WildcardSuffix: qname labels + suffix), target }`; no records of `qtype` and no CNAME → NOERROR/NODATA with SOA and EDE 4. `TcpOnly` over UDP → `Truncate` (NOERROR, TC=1, no records); over TCP → `Passthru`. `Drop` → `Drop`. `Passthru` → `Passthru`.
- [ ] Wire into the query path:
  - `server/mod.rs` (worker, per packet, after ACL/filter/rewrites and before the cache lookup): `let rpz = state.rpz.load();` only when `rpz.has_query_triggers`: `check_query(name_key.as_wire(), client_ip)`. `Hit` → `effective_action`; `None` or `Passthru` → continue normally; otherwise `apply_action` synchronously: `Respond`/`Truncate` → send; `Drop` → no reply; `ChaseCname` → spawn the miss task with the CNAME target and prepend the CNAME record to its answer. `Deferred` → skip the cache and spawn the miss task carrying the deferred `(zone, action)`. `NoMatch` → continue.
  - `dispatch::resolve_miss`, after validation and only when `rpz.has_response_triggers || deferred.is_some()`: `chain` = `qname` plus CNAME targets in `answers`; `check_response(deferred.zone or zones.len(), ..)` with `Resolution.ns_names/ns_addrs` (empty for forward routes, so NSDNAME/NSIP triggers only fire in recursive mode); a hit (or else the deferred action) goes through `effective_action` + `apply_action`; policy results set `cacheable: false`, `rpz_action`, `ede`, `drop`. Unrewritten answers set `policy_generation = state.policy_generation.load()`.
  - `cache.rs` hit path: `if entry.policy_generation != state.policy_generation.load(Ordering::Relaxed) { treat as miss }` — one atomic load and compare, no allocation. `policy_generation` is incremented on every RPZ publication (Task 10) and by `control.rs` whenever an applied snapshot changes `resolution_mode`, `recursion`, `forward_zones` or `dnssec` compared with the previous snapshot (prost `PartialEq` on those fields), so answers cached under an old NTA, trust anchor, route or policy are never served.
- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib recursor::rpz::rpz_tests`; expect PASS (8 tests).
- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh make engine-test`; expect PASS including `cache_hit_path_does_not_allocate`.
- [ ] Commit: `git add engine/src && git commit -m "feat(rpz): RPZ triggers, precedence and actions on the query path"`.

## Task 10: RPZ sources — blob files, AXFR/IXFR with TSIG, SOA refresh, last-good persistence

Files:

- `engine/src/recursor/rpz/tsig.rs` — RFC 8945 TSIG signing of requests and verification of (multi-message) responses with `ring::hmac`.
- `engine/src/recursor/rpz/transfer.rs` — SOA check, IXFR (RFC 1995) with AXFR fallback, RFC 1982 serial comparison, timers.
- `engine/src/recursor/rpz/manager.rs` — per-zone lifecycle on the control runtime, `RpzSet` publication, persistence under `state_dir/rpz/`, status.
- `engine/src/recursor/rpz/transfer_tests.rs` — unit tests with an in-test primary.
- `engine/src/control.rs` — calls `RpzManager::apply_config` during snapshot application; `Stats.rpz_zones`.
- `engine/src/telemetry/metrics.rs` — `nexora_rpz_hits_total{zone}`, `nexora_rpz_zone_serial{zone}`, `nexora_rpz_zone_records{zone}`, `nexora_rpz_zone_stale{zone}`, `nexora_rpz_zone_last_success_timestamp_seconds{zone}`, `nexora_rpz_refresh_failures_total{zone}` (`zone` label = zone origin).

Interfaces:

```rust
// tsig.rs
#[derive(Clone, Copy, Debug, PartialEq, Eq)] pub enum TsigAlg { HmacSha256, HmacSha512 }
#[derive(Clone, Debug)] pub struct TsigKey { pub name: Name, pub alg: TsigAlg, pub secret: Vec<u8> }
#[derive(Debug, PartialEq, Eq)] pub enum TsigError { Missing, BadKey, BadSig, BadTime, Malformed, TooManyUnsigned }
pub const FUDGE: u16 = 300;
pub fn skip_name(b: &[u8], i: usize) -> Option<usize>;
pub fn extract_mac(wire: &[u8]) -> Option<Vec<u8>>; // MAC field of a trailing TSIG RR
pub fn sign_request(wire: &mut Vec<u8>, key: &TsigKey, time_signed: u64) -> Vec<u8>;                         // appends TSIG RR, returns MAC
pub fn sign_response(wire: &mut Vec<u8>, key: &TsigKey, time_signed: u64, prior_mac: &[u8], first: bool, unsigned_before: &[u8]) -> Vec<u8>; // test primary + completeness
pub struct TsigVerifier { key: TsigKey, prior_mac: Vec<u8>, first: bool, unsigned: Vec<u8>, unsigned_count: u32 }
impl TsigVerifier {
    pub fn new(key: TsigKey, request_mac: Vec<u8>) -> Self;
    pub fn verify(&mut self, wire: &[u8], now: u64) -> Result<(), TsigError>;
    pub fn finish(&self) -> Result<(), TsigError>; // the last message must have been signed
}
// transfer.rs
#[derive(Clone, Debug, PartialEq)] pub struct ZoneData { pub serial: u32, pub records: Vec<Record> } // records exclude the trailing SOA duplicate
pub fn serial_gt(a: u32, b: u32) -> bool;
#[derive(Debug, PartialEq, Eq)] pub struct Timers { pub refresh: u64, pub retry: u64, pub expire: u64 }
pub fn timers_from_soa(refresh: u32, retry: u32, expire: u32, min_refresh: u32) -> Timers;
pub fn apply_ixfr(current: &ZoneData, answers: &[Record]) -> Result<ZoneData, String>;
pub async fn soa_serial(primary: SocketAddr, zone: &Name, key: Option<&TsigKey>) -> Result<(u32, SOA), String>;
pub async fn transfer(primary: SocketAddr, zone: &Name, current: Option<&ZoneData>, key: Option<&TsigKey>) -> Result<ZoneData, String>;
// manager.rs
pub struct RpzManager { /* dir, Arc<RecursorState>, Mutex<HashMap<String, ZoneTask>> */ }
impl RpzManager {
    pub fn new(state_dir: &Path, state: Arc<RecursorState>) -> Arc<Self>;
    pub fn apply_config(self: &Arc<Self>, zones: &[pb::RpzZone], blobs: &control::BlobStore) -> Result<(), String>;
    pub async fn refresh_now(&self, id: &str) -> Result<(), String>;
    pub fn status(&self) -> Vec<pb::RpzZoneStatus>;
}
```

- [ ] Create `engine/src/recursor/rpz/transfer_tests.rs`:

```rust
use super::manager::RpzManager;
use super::transfer::*;
use super::tsig::*;
use crate::pb;
use crate::recursor::RecursorState;
use hickory_proto::op::{Message, OpCode, Query};
use hickory_proto::rr::{rdata::{CNAME, SOA}, Name, RData, Record, RecordType};
use std::sync::{Arc, Mutex};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpListener;

fn n(s: &str) -> Name { Name::from_ascii(s).unwrap() }
fn key() -> TsigKey { TsigKey { name: n("rpz-key."), alg: TsigAlg::HmacSha256, secret: vec![0x42; 32] } }
fn soa(serial: u32) -> Record { Record::from_rdata(n("rpz.test."), 60, RData::SOA(SOA::new(n("ns.rpz.test."), n("h.rpz.test."), serial, 2, 1, 30, 60))) }
fn block(name: &str) -> Record { Record::from_rdata(n(&format!("{name}.rpz.test.")), 60, RData::CNAME(CNAME(Name::root()))) }

/// Serves AXFR/IXFR/SOA over TCP from `zone` (serial, records); each AXFR is split into one message per record and TSIG-signed
/// on the first and last message only, exercising the unsigned-intermediate rule.
async fn primary(zone: Arc<Mutex<(u32, Vec<Record>)>>, signing: Option<TsigKey>) -> std::net::SocketAddr {
    let l = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = l.local_addr().unwrap();
    tokio::spawn(async move {
        loop {
            let (mut s, _) = l.accept().await.unwrap();
            let zone = zone.clone();
            let signing = signing.clone();
            tokio::spawn(async move {
                let len = s.read_u16().await.unwrap() as usize;
                let mut buf = vec![0u8; len];
                s.read_exact(&mut buf).await.unwrap();
                let req = Message::from_vec(&buf).unwrap();
                let request_mac = req_mac(&buf);
                let (serial, records) = zone.lock().unwrap().clone();
                let qt = req.queries[0].query_type();
                let mut bodies: Vec<Vec<Record>> = vec![];
                if qt == RecordType::SOA { bodies.push(vec![soa(serial)]); }
                else { bodies.push(vec![soa(serial)]); for r in records { bodies.push(vec![r]); } bodies.push(vec![soa(serial)]); }
                let mut prior = request_mac;
                let mut unsigned = Vec::new();
                let last = bodies.len() - 1;
                for (i, body) in bodies.into_iter().enumerate() {
                    let mut m = Message::response(req.metadata.id, OpCode::Query);
                    m.metadata.authoritative = true;
                    m.queries.push(Query::query(n("rpz.test."), qt));
                    m.answers = body;
                    let mut wire = m.to_vec().unwrap();
                    if let Some(k) = &signing {
                        if i == 0 || i == last {
                            prior = sign_response(&mut wire, k, now(), &prior, i == 0, &unsigned);
                            unsigned.clear();
                        } else {
                            unsigned.extend_from_slice(&wire);
                        }
                    }
                    s.write_u16(wire.len() as u16).await.unwrap();
                    s.write_all(&wire).await.unwrap();
                }
            });
        }
    });
    addr
}

fn now() -> u64 { std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_secs() }

/// MAC of the TSIG RR at the end of a signed request, empty when unsigned.
fn req_mac(wire: &[u8]) -> Vec<u8> { extract_mac(wire).unwrap_or_default() }

#[test]
fn serial_arithmetic_rfc1982() {
    assert!(serial_gt(2, 1));
    assert!(serial_gt(0, 0xFFFF_FFFF));
    assert!(!serial_gt(1, 1));
    assert!(serial_gt(0x7FFF_FFFF, 0));
    assert!(!serial_gt(0x8000_0000, 0), "distance 2^31 is undefined and treated as not greater");
    assert!(!serial_gt(1, 2));
}

#[test]
fn timers_respect_min_refresh() {
    assert_eq!(timers_from_soa(2, 1, 30, 60), Timers { refresh: 60, retry: 60, expire: 60 });
    assert_eq!(timers_from_soa(3600, 600, 86400, 60), Timers { refresh: 3600, retry: 600, expire: 86400 });
}

#[test]
fn tsig_sign_and_verify_detects_tampering_wrong_key_and_time() {
    let mut req = Message::query().to_vec().unwrap();
    let mac = sign_request(&mut req, &key(), 1_800_000_000);
    let mut resp = Message::response(0, OpCode::Query).to_vec().unwrap();
    sign_response(&mut resp, &key(), 1_800_000_000, &mac, true, &[]);
    let mut v = TsigVerifier::new(key(), mac.clone());
    assert_eq!(v.verify(&resp, 1_800_000_100), Ok(()));
    let mut tampered = resp.clone();
    tampered[3] ^= 0x01;
    assert_eq!(TsigVerifier::new(key(), mac.clone()).verify(&tampered, 1_800_000_100), Err(TsigError::BadSig));
    let wrong = TsigKey { secret: vec![0x43; 32], ..key() };
    assert_eq!(TsigVerifier::new(wrong, mac.clone()).verify(&resp, 1_800_000_100), Err(TsigError::BadSig));
    assert_eq!(TsigVerifier::new(key(), mac.clone()).verify(&resp, 1_800_000_301), Err(TsigError::BadTime));
    let unsigned = Message::response(0, OpCode::Query).to_vec().unwrap();
    let mut v = TsigVerifier::new(key(), mac);
    assert_eq!(v.verify(&unsigned, 1_800_000_000), Err(TsigError::Missing), "first message must be signed");
}

#[test]
fn ixfr_applies_deletions_and_additions() {
    let cur = ZoneData { serial: 1, records: vec![soa(1), block("a"), block("b")] };
    // IXFR: new SOA, old SOA, deleted..., new SOA, added..., new SOA
    let answers = vec![soa(2), soa(1), block("a"), soa(2), block("c"), soa(2)];
    let next = apply_ixfr(&cur, &answers).unwrap();
    assert_eq!(next.serial, 2);
    assert!(next.records.contains(&block("b")) && next.records.contains(&block("c")) && !next.records.contains(&block("a")));
    // AXFR-style reply to an IXFR request replaces the zone
    let full = apply_ixfr(&cur, &[soa(3), block("z"), soa(3)]).unwrap();
    assert_eq!(full.records, vec![soa(3), block("z")]);
    assert!(apply_ixfr(&cur, &[soa(1)]).unwrap() == cur, "single SOA with current serial = up to date");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn axfr_with_tsig_then_incremental_update() {
    let zone = Arc::new(Mutex::new((5u32, vec![block("a"), block("b")])));
    let addr = primary(zone.clone(), Some(key())).await;
    let z = transfer(addr, &n("rpz.test."), None, Some(&key())).await.unwrap();
    assert_eq!(z.serial, 5);
    assert_eq!(z.records.len(), 3);
    let unsigned_primary = primary(zone.clone(), None).await;
    assert!(transfer(unsigned_primary, &n("rpz.test."), None, Some(&key())).await.unwrap_err().contains("TSIG"));
    assert_eq!(soa_serial(addr, &n("rpz.test."), Some(&key())).await.unwrap().0, 5);
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn manager_keeps_last_good_zone_when_primary_fails_and_reloads_from_disk() {
    let zone = Arc::new(Mutex::new((5u32, vec![block("a")])));
    let addr = primary(zone.clone(), None).await;
    let dir = tempfile::tempdir().unwrap();
    let state = RecursorState::new(dir.path());
    let mgr = RpzManager::new(dir.path(), state.clone());
    let cfg = vec![pb::RpzZone { id: "z1".into(), name: "rpz.test.".into(), source: Some(pb::rpz_zone::Source::Transfer(pb::RpzTransferSource { primary: addr.to_string(), tsig_key_name: String::new(), tsig_algorithm: 0, tsig_secret: vec![], min_refresh_seconds: 1 })), policy_override: 0, refresh_nonce: 0 }];
    mgr.apply_config(&cfg, &crate::control::BlobStore::empty()).unwrap();
    mgr.refresh_now("z1").await.unwrap();
    assert_eq!(state.rpz.load().zones.len(), 1);
    assert!(dir.path().join("rpz/z1.zone").exists());
    let gen = state.policy_generation.load(std::sync::atomic::Ordering::Relaxed);
    let dead: std::net::SocketAddr = "127.0.0.1:1".parse().unwrap();
    let mut cfg2 = cfg.clone();
    if let Some(pb::rpz_zone::Source::Transfer(t)) = cfg2[0].source.as_mut() { t.primary = dead.to_string(); }
    mgr.apply_config(&cfg2, &crate::control::BlobStore::empty()).unwrap();
    assert!(mgr.refresh_now("z1").await.is_err());
    assert_eq!(state.rpz.load().zones.len(), 1, "last good zone kept");
    let st = &mgr.status()[0];
    assert!(!st.last_error.is_empty());
    assert_eq!(st.serial, 5);
    let state2 = RecursorState::new(dir.path());
    let mgr2 = RpzManager::new(dir.path(), state2.clone());
    mgr2.apply_config(&cfg2, &crate::control::BlobStore::empty()).unwrap();
    assert_eq!(state2.rpz.load().zones.len(), 1, "persisted zone served after restart before any transfer");
    assert!(gen >= 1);
}
```

- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib recursor::rpz::transfer_tests`; expect FAIL with "unresolved import `super::manager`".
- [ ] Implement `tsig.rs`. Wire helpers:

```rust
pub fn skip_name(b: &[u8], mut i: usize) -> Option<usize> {
    loop {
        let len = *b.get(i)? as usize;
        if len & 0xC0 == 0xC0 { b.get(i + 1)?; return Some(i + 2); }
        if len & 0xC0 != 0 { return None; }
        i += 1;
        if len == 0 { return Some(i); }
        i += len;
        if i > b.len() { return None; }
    }
}

/// Offset of the last RR in the additional section when it is a TSIG RR.
fn tsig_offset(b: &[u8]) -> Option<usize> {
    if b.len() < 12 { return None; }
    let count = |o: usize| u16::from_be_bytes([b[o], b[o + 1]]) as usize;
    let (qd, an, ns, ar) = (count(4), count(6), count(8), count(10));
    if ar == 0 { return None; }
    let mut i = 12;
    for _ in 0..qd { i = skip_name(b, i)? + 4; }
    let mut last = i;
    for _ in 0..(an + ns + ar) {
        last = i;
        i = skip_name(b, i)?;
        let rdlen = u16::from_be_bytes([*b.get(i + 8)?, *b.get(i + 9)?]) as usize;
        i += 10 + rdlen;
        if i > b.len() { return None; }
    }
    let t = skip_name(b, last)?;
    (u16::from_be_bytes([b[t], b[t + 1]]) == 250).then_some(last)
}

fn variables(key: &TsigKey, time_signed: u64, error: u16, other: &[u8], timers_only: bool) -> Vec<u8> {
    let mut v = Vec::new();
    if !timers_only {
        v.extend(key.name.to_lowercase().to_bytes().unwrap());
        v.extend(255u16.to_be_bytes()); // CLASS ANY
        v.extend(0u32.to_be_bytes());   // TTL
        v.extend(Name::from_ascii(key.alg.name()).unwrap().to_bytes().unwrap());
    }
    v.extend(&time_signed.to_be_bytes()[2..]); // 48-bit time
    v.extend(FUDGE.to_be_bytes());
    if !timers_only {
        v.extend(error.to_be_bytes());
        v.extend((other.len() as u16).to_be_bytes());
        v.extend(other);
    }
    v
}
```

`TsigAlg::name()` → `"hmac-sha256."` / `"hmac-sha512."`; ring algorithm `hmac::HMAC_SHA256` / `HMAC_SHA512`. Request MAC input = message (as sent, ARCOUNT not yet incremented) ‖ `variables(.., timers_only=false)`. Response MAC input (RFC 8945 §5.3): `u16 len(prior_mac) ‖ prior_mac ‖ unsigned_before ‖ message_without_tsig ‖ variables(.., timers_only = !first)` where `message_without_tsig` has ARCOUNT decremented and the ID replaced by the TSIG original ID. The appended RR: owner = key name wire, TYPE 250, CLASS 255, TTL 0, RDATA = algorithm name wire ‖ 48-bit time ‖ fudge ‖ `u16` MAC size ‖ MAC ‖ original ID ‖ error 0 ‖ other len 0; ARCOUNT incremented. `TsigVerifier::verify`: `tsig_offset` absent → if `first` → `Missing`; else append the whole message to `unsigned`, `unsigned_count += 1`, `> 99` → `TooManyUnsigned`, return `Ok`. Present → parse the RR (algorithm name must equal the key's, owner must equal key name, case-insensitive, else `BadKey`), rebuild the MAC input as above, `ring::hmac::verify` (constant time) → `BadSig`; `|now - time_signed| > fudge` → `BadTime`; then `prior_mac = mac`, `first = false`, clear `unsigned`, `unsigned_count = 0`. `finish` returns `Missing` when `unsigned_count > 0`.

- [ ] Implement `transfer.rs`: `serial_gt(a, b) = a != b && (a.wrapping_sub(b) as i32) > 0` (the undefined 2^31 distance counts as not greater); `timers_from_soa` floors each value at `min_refresh` (0 → 60). `soa_serial`: UDP query for `SOA` (Transport-style random ID, `OsRng`), TCP retry on TC; with a key the query is TSIG-signed and the reply verified. `transfer`: open TCP; with `current` send IXFR (question `IXFR`, authority section = `current` SOA) otherwise AXFR; sign when `key`; read length-prefixed messages until the second occurrence of the SOA whose serial equals the first answer's serial (for IXFR: a single SOA equal to the current serial → up to date); every message rcode must be NOERROR (`NOTIMP`/`REFUSED`/`FORMERR` to IXFR → retry once with AXFR); verify each message with `TsigVerifier` and call `finish`; TSIG failures produce `Err(format!("TSIG verification failed: {e:?}"))`; overall deadline 60 s; limit 10 000 000 records. `apply_ixfr` implements RFC 1995 §4: `[SOA n]` only with serial == current → `current.clone()`; `[SOA n, SOA old, …]` → sequences of (old SOA, deletions, new SOA, additions) applied in order, verifying each old SOA serial equals the running serial; `[SOA n, non-SOA…, SOA n]` → full replacement (records = everything before the trailing SOA).
- [ ] Implement `manager.rs`:
  - `apply_config` (runs on the control runtime during snapshot application): zones not present in the config are dropped (their files deleted). File sources: `blobs.get(sha)` → `zstd::decode_all` → `parse_rpz_text(origin, text)` → an error rejects the snapshot with `"rpz_zones[{i}]: {error}"`. Transfer sources: create or update a `ZoneTask` (key from `tsig_*`; an empty stripped secret with an algorithm set → key `None` and `last_error = "tsig secret not available (snapshot loaded from disk)"`, the persisted zone keeps serving); if `state_dir/rpz/<id>.zone` exists and no data is loaded yet, parse it (`parse_rpz_text` with `$ORIGIN` taken from the zone name) and serve it immediately. A changed `refresh_nonce` schedules an immediate refresh.
  - Each transfer zone runs a tokio task: at `next_refresh` call `soa_serial`; `serial_gt(remote, local)` or no local data → `transfer`; success → build `RpzZoneIndex`, publish, persist records one per line with `format!("{record}\n")` via write-temp + fsync + rename, `last_success = now`, `stale = false`, `next_refresh = now + refresh`; failure → `last_error`, `nexora_rpz_refresh_failures_total += 1`, `next_refresh = now + retry`, and `stale = true` once `now - last_success > expire` — the last good data keeps serving (never dropped).
  - Publication: rebuild `RpzSet::new(zones in config order)`; `state.rpz.store(Arc::new(set))`; `state.policy_generation.fetch_add(1)`.
  - `refresh_now` performs one refresh cycle synchronously and returns its result. `status()` produces `pb::RpzZoneStatus` per zone in config order (hits from the index).
  - `control::BlobStore::empty()` is added for tests if M1 did not provide it (a store with no blobs whose `get` returns `Err("blob not found")`).
- [ ] `control.rs`: snapshot application calls `rpz_manager.apply_config(&snapshot.rpz_zones, &blobs)?` before storing the runtime; `Stats.rpz_zones = rpz_manager.status()`.
- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml --lib recursor::rpz::transfer_tests`; expect PASS (6 tests).
- [ ] Commit: `git add engine/src && git commit -m "feat(rpz): file and AXFR/IXFR sources with TSIG and last-good persistence"`.

## Task 11: Management plane — migrations, OpenAPI operations, handlers, snapshot builder, stats ingestion

Files:

- `mgmt/migrations/0300_resolution.sql`, `mgmt/migrations/0301_dnssec.sql`, `mgmt/migrations/0302_rpz.sql` — tables below.
- `mgmt/api/openapi.yaml` — operations and schemas below.
- `mgmt/internal/api/gen.go` — regenerated by `oapi-codegen` (`make proto` regenerates Go API code too; otherwise `go generate ./mgmt/internal/api`).
- `mgmt/internal/rpz/validate.go`, `mgmt/internal/rpz/validate_test.go` — RPZ zone text validation and zstd packing.
- `mgmt/internal/dnssecconf/ds.go`, `mgmt/internal/dnssecconf/ds_test.go` — DS/NTA/root-hint/forward-zone input validation.
- `mgmt/internal/snapshot/m3.go`, `mgmt/internal/snapshot/m3_test.go` — row → proto conversion; `Build` calls it.
- `mgmt/internal/store/m3.go` — pgx queries for the new tables.
- `mgmt/internal/api/m3.go` — strict-server handlers.
- `mgmt/internal/auth/permissions.go` — permission entries.
- `mgmt/internal/stats/m3.go` — `Stats` fields 100–102 → `engine_dnssec_status`, `engine_rpz_status`.
- `web/src/api/schema.d.ts` — regenerated by `pnpm --dir web gen:api`.

Interfaces:

```go
// mgmt/internal/rpz
type Summary struct{ Serial uint32; Records int; Skipped int }
func ValidateZone(origin, content string) (Summary, error)
func Pack(content string) (compressed []byte, err error) // zstd, level 3
// mgmt/internal/dnssecconf
func ValidateDS(ds string) error
func ValidateDomain(name string) (fqdnLower string, err error)
func ValidateForwardAddresses(addrs []string) error
func ValidateRootHints(h []RootHint) error
type RootHint struct{ Name string `json:"name"`; Addresses []string `json:"addresses"` }
var IANARootAnchors = []string{
    "20326 8 2 E06D44B80B8F1D39A95C0B0D7C65D08458E880409BBC683457104237C7F8EC8D",
    "38696 8 2 683D2D0ACB8C9B712A1948B27F741219298D0A450D612C483AF444A4C0FB2B16",
}
// mgmt/internal/store
type M3Rows struct {
    Resolution   ResolutionSettings
    ForwardZones []ForwardZone
    Dnssec       DnssecSettings
    Anchors      []TrustAnchor
    NTAs         []NegativeTrustAnchor
    RPZ          []RPZZone
}
func LoadM3(ctx context.Context, tx pgx.Tx) (M3Rows, error)
// mgmt/internal/snapshot
type M3Rows = store.M3Rows
func ApplyM3(s *controlv1.ConfigSnapshot, rows M3Rows, now time.Time)
// mgmt/internal/stats
func RecordM3(ctx context.Context, pool *pgxpool.Pool, engineID uuid.UUID, s *controlv1.Stats) error
```

OpenAPI operations (operationId → method path, role for mutations = operator):

| operationId               | method + path                              |
| ------------------------- | ------------------------------------------ |
| getResolutionSettings     | GET /resolution                            |
| updateResolutionSettings  | PUT /resolution                            |
| listForwardZones          | GET /forward-zones                         |
| createForwardZone         | POST /forward-zones                        |
| updateForwardZone         | PUT /forward-zones/{id}                    |
| deleteForwardZone         | DELETE /forward-zones/{id}                 |
| getDnssecSettings         | GET /dnssec/settings                       |
| updateDnssecSettings      | PUT /dnssec/settings                       |
| getDnssecStatus           | GET /dnssec/status                         |
| listTrustAnchors          | GET /dnssec/trust-anchors                  |
| createTrustAnchor         | POST /dnssec/trust-anchors                 |
| deleteTrustAnchor         | DELETE /dnssec/trust-anchors/{id}          |
| listNegativeTrustAnchors  | GET /dnssec/negative-trust-anchors         |
| createNegativeTrustAnchor | POST /dnssec/negative-trust-anchors        |
| deleteNegativeTrustAnchor | DELETE /dnssec/negative-trust-anchors/{id} |
| listRpzZones              | GET /rpz-zones                             |
| createRpzZone             | POST /rpz-zones                            |
| getRpzZone                | GET /rpz-zones/{id}                        |
| updateRpzZone             | PUT /rpz-zones/{id}                        |
| deleteRpzZone             | DELETE /rpz-zones/{id}                     |
| uploadRpzZoneFile         | PUT /rpz-zones/{id}/file                   |
| refreshRpzZone            | POST /rpz-zones/{id}/refresh               |
| reorderRpzZones           | PUT /rpz-zones/order                       |

Schemas (literal, under `components.schemas`):

```yaml
ResolutionSettings:
  type: object
  required:
    [
      mode,
      qname_minimisation,
      aggressive_nsec,
      max_upstream_queries,
      max_delegation_depth,
      authority_port,
      root_hints,
      revision,
    ]
  properties:
    mode: { type: string, enum: [forward, recursive] }
    qname_minimisation: { type: boolean }
    aggressive_nsec: { type: boolean }
    max_upstream_queries: { type: integer, minimum: 1, maximum: 1000 }
    max_delegation_depth: { type: integer, minimum: 1, maximum: 64 }
    authority_port: { type: integer, minimum: 1, maximum: 65535 }
    root_hints:
      { type: array, items: { $ref: "#/components/schemas/RootHint" } }
    revision: { type: integer, format: int64 }
RootHint:
  type: object
  required: [name, addresses]
  properties:
    name: { type: string }
    addresses: { type: array, minItems: 1, items: { type: string } }
ForwardZone:
  type: object
  required: [id, domain, addresses, validate, revision]
  properties:
    id: { type: string, format: uuid, readOnly: true }
    domain: { type: string }
    addresses:
      {
        type: array,
        minItems: 1,
        items: { type: string, description: "ip:port" },
      }
    validate: { type: boolean }
    revision: { type: integer, format: int64 }
DnssecSettings:
  type: object
  required: [validation, rfc5011, revision]
  properties:
    validation: { type: boolean }
    rfc5011: { type: boolean }
    revision: { type: integer, format: int64 }
TrustAnchor:
  type: object
  required: [id, zone, ds, source, created_at]
  properties:
    id: { type: string, format: uuid, readOnly: true }
    zone: { type: string }
    ds: { type: string }
    source: { type: string, enum: [iana, operator], readOnly: true }
    created_at: { type: string, format: date-time, readOnly: true }
NegativeTrustAnchor:
  type: object
  required: [id, domain, reason, expires_at, created_by]
  properties:
    id: { type: string, format: uuid, readOnly: true }
    domain: { type: string }
    reason: { type: string, maxLength: 500 }
    expires_at:
      {
        type: string,
        format: date-time,
        description: "at most 30 days in the future",
      }
    created_by: { type: string, readOnly: true }
DnssecStatus:
  type: object
  required: [engines]
  properties:
    engines:
      type: array
      items:
        type: object
        required:
          [
            engine_id,
            engine_name,
            reported_at,
            secure,
            insecure,
            bogus,
            indeterminate,
            active_negative_trust_anchors,
            trust_anchors,
          ]
        properties:
          engine_id: { type: string, format: uuid }
          engine_name: { type: string }
          reported_at: { type: string, format: date-time }
          secure: { type: integer, format: int64 }
          insecure: { type: integer, format: int64 }
          bogus: { type: integer, format: int64 }
          indeterminate: { type: integer, format: int64 }
          active_negative_trust_anchors: { type: integer }
          trust_anchors:
            type: array
            items:
              type: object
              required:
                [
                  zone,
                  key_tag,
                  algorithm,
                  state,
                  last_refresh_success,
                  hold_down_until,
                  last_error,
                ]
              properties:
                zone: { type: string }
                key_tag: { type: integer }
                algorithm: { type: integer }
                state:
                  {
                    type: string,
                    enum: [configured, add_pend, valid, missing, revoked],
                  }
                last_refresh_success:
                  { type: [string, "null"], format: date-time }
                hold_down_until: { type: [string, "null"], format: date-time }
                last_error: { type: string }
RpzZone:
  type: object
  required: [id, name, position, source_type, policy_override, revision, status]
  properties:
    id: { type: string, format: uuid, readOnly: true }
    name: { type: string }
    position: { type: integer, readOnly: true }
    source_type: { type: string, enum: [file, transfer] }
    primary: { type: [string, "null"], description: "ip:port, transfer only" }
    tsig_key_name: { type: [string, "null"] }
    tsig_algorithm:
      { type: [string, "null"], enum: [hmac-sha256, hmac-sha512, null] }
    tsig_secret:
      {
        type: string,
        writeOnly: true,
        description: "base64; omit to keep the stored secret",
      }
    tsig_secret_set: { type: boolean, readOnly: true }
    min_refresh_seconds: { type: integer, minimum: 1, maximum: 86400 }
    policy_override:
      {
        type: string,
        enum: [given, disabled, nxdomain, nodata, passthru, drop, tcp_only],
      }
    file_records: { type: [integer, "null"], readOnly: true }
    revision: { type: integer, format: int64 }
    status:
      type: array
      readOnly: true
      items:
        type: object
        required:
          [
            engine_id,
            engine_name,
            serial,
            records,
            skipped,
            hits,
            last_success,
            last_error,
            stale,
          ]
        properties:
          engine_id: { type: string, format: uuid }
          engine_name: { type: string }
          serial: { type: integer, format: int64 }
          records: { type: integer, format: int64 }
          skipped: { type: integer, format: int64 }
          hits: { type: integer, format: int64 }
          last_success: { type: [string, "null"], format: date-time }
          last_error: { type: string }
          stale: { type: boolean }
RpzZoneFile:
  type: object
  required: [content, revision]
  properties:
    content: { type: string, maxLength: 52428800 }
    revision: { type: integer, format: int64 }
RpzZoneOrder:
  type: object
  required: [ids]
  properties:
    ids: { type: array, items: { type: string, format: uuid } }
```

Migrations (literal):

```sql
-- 0300_resolution.sql
-- +goose Up
CREATE TABLE resolution_settings (
    id smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    mode text NOT NULL DEFAULT 'forward' CHECK (mode IN ('forward', 'recursive')),
    qname_minimisation boolean NOT NULL DEFAULT true,
    aggressive_nsec boolean NOT NULL DEFAULT false,
    max_upstream_queries integer NOT NULL DEFAULT 100 CHECK (max_upstream_queries BETWEEN 1 AND 1000),
    max_delegation_depth integer NOT NULL DEFAULT 32 CHECK (max_delegation_depth BETWEEN 1 AND 64),
    authority_port integer NOT NULL DEFAULT 53 CHECK (authority_port BETWEEN 1 AND 65535),
    root_hints jsonb NOT NULL DEFAULT '[]'::jsonb,
    revision bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO resolution_settings (id) VALUES (1);
CREATE TABLE forward_zones (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    domain text NOT NULL UNIQUE,
    addresses text[] NOT NULL CHECK (cardinality(addresses) > 0),
    validate boolean NOT NULL DEFAULT false,
    revision bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
-- +goose Down
DROP TABLE forward_zones;
DROP TABLE resolution_settings;

-- 0301_dnssec.sql
-- +goose Up
CREATE TABLE dnssec_settings (
    id smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    validation boolean NOT NULL DEFAULT true,
    rfc5011 boolean NOT NULL DEFAULT true,
    revision bigint NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO dnssec_settings (id) VALUES (1);
CREATE TABLE trust_anchors (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    zone text NOT NULL,
    ds text NOT NULL,
    source text NOT NULL DEFAULT 'operator' CHECK (source IN ('iana', 'operator')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (zone, ds)
);
INSERT INTO trust_anchors (zone, ds, source) VALUES
    ('.', '20326 8 2 E06D44B80B8F1D39A95C0B0D7C65D08458E880409BBC683457104237C7F8EC8D', 'iana'),
    ('.', '38696 8 2 683D2D0ACB8C9B712A1948B27F741219298D0A450D612C483AF444A4C0FB2B16', 'iana');
CREATE TABLE negative_trust_anchors (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    domain text NOT NULL UNIQUE,
    reason text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL,
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE engine_dnssec_status (
    engine_id uuid PRIMARY KEY REFERENCES engines(id) ON DELETE CASCADE,
    stats jsonb NOT NULL,
    reported_at timestamptz NOT NULL
);
-- +goose Down
DROP TABLE engine_dnssec_status;
DROP TABLE negative_trust_anchors;
DROP TABLE trust_anchors;
DROP TABLE dnssec_settings;

-- 0302_rpz.sql
-- +goose Up
CREATE TABLE rpz_zones (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE,
    position integer NOT NULL,
    source_type text NOT NULL CHECK (source_type IN ('file', 'transfer')),
    blob_sha256 text REFERENCES blobs(sha256),
    file_records integer,
    primary_address text,
    tsig_key_name text,
    tsig_algorithm text CHECK (tsig_algorithm IN ('hmac-sha256', 'hmac-sha512')),
    tsig_secret bytea,
    min_refresh_seconds integer NOT NULL DEFAULT 60 CHECK (min_refresh_seconds BETWEEN 1 AND 86400),
    policy_override text NOT NULL DEFAULT 'given' CHECK (policy_override IN ('given', 'disabled', 'nxdomain', 'nodata', 'passthru', 'drop', 'tcp_only')),
    refresh_nonce bigint NOT NULL DEFAULT 0,
    revision bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((source_type = 'transfer' AND primary_address IS NOT NULL) OR (source_type = 'file' AND primary_address IS NULL)),
    CHECK ((tsig_algorithm IS NULL) = (tsig_key_name IS NULL))
);
CREATE UNIQUE INDEX rpz_zones_position ON rpz_zones (position);
CREATE TABLE engine_rpz_status (
    engine_id uuid NOT NULL REFERENCES engines(id) ON DELETE CASCADE,
    rpz_zone_id uuid NOT NULL REFERENCES rpz_zones(id) ON DELETE CASCADE,
    serial bigint NOT NULL,
    records bigint NOT NULL,
    skipped bigint NOT NULL,
    hits bigint NOT NULL,
    last_success_at timestamptz,
    last_error text NOT NULL DEFAULT '',
    stale boolean NOT NULL DEFAULT false,
    reported_at timestamptz NOT NULL,
    PRIMARY KEY (engine_id, rpz_zone_id)
);
-- +goose Down
DROP TABLE engine_rpz_status;
DROP TABLE rpz_zones;
```

- [ ] Create `mgmt/internal/rpz/validate_test.go`:

```go
package rpz

import (
	"bytes"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

const zone = `$TTL 60
@ SOA ns.rpz. hostmaster.rpz. 7 60 60 86400 60
@ NS ns.rpz.
bad.example CNAME .
32.66.2.0.192.rpz-ip CNAME .
local.example A 10.9.9.9
`

func TestValidateZoneCountsRecordsAndSerial(t *testing.T) {
	s, err := ValidateZone("rpz.file.test.", zone)
	if err != nil {
		t.Fatalf("ValidateZone: %v", err)
	}
	if s.Serial != 7 || s.Records != 3 {
		t.Fatalf("summary = %+v, want serial 7 records 3", s)
	}
}

func TestValidateZoneRejectsIncludeMissingSOAAndSyntax(t *testing.T) {
	for name, text := range map[string]string{
		"include": "$INCLUDE /etc/passwd\n" + zone,
		"no soa":  "$TTL 60\nbad.example CNAME .\n",
		"syntax":  zone + "broken.example IN A not-an-ip\n",
	} {
		if _, err := ValidateZone("rpz.file.test.", text); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	_, err := ValidateZone("rpz.file.test.", "$INCLUDE x\n")
	if err == nil || !strings.Contains(err.Error(), "$INCLUDE is not allowed") {
		t.Fatalf("include error = %v", err)
	}
}

func TestPackIsZstdOfContent(t *testing.T) {
	b, err := Pack(zone)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := zstd.NewReader(nil)
	out, err := d.DecodeAll(b, nil)
	if err != nil || !bytes.Equal(out, []byte(zone)) {
		t.Fatalf("round trip failed: %v", err)
	}
}
```

- [ ] Create `mgmt/internal/dnssecconf/ds_test.go`:

```go
package dnssecconf

import "testing"

func TestValidateDS(t *testing.T) {
	for _, ok := range IANARootAnchors {
		if err := ValidateDS(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "20326 8 2", "20326 8 2 ZZ", "70000 8 2 E06D", "20326 8 2 E06D44"} {
		if err := ValidateDS(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestValidateDomainAndForwardAddresses(t *testing.T) {
	if d, err := ValidateDomain("Corp.Example"); err != nil || d != "corp.example." {
		t.Fatalf("ValidateDomain = %q, %v", d, err)
	}
	if _, err := ValidateDomain("bad..name"); err == nil {
		t.Fatal("expected error for empty label")
	}
	if err := ValidateForwardAddresses([]string{"10.0.0.1:53", "[fd00::1]:5353"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateForwardAddresses([]string{"10.0.0.1"}); err == nil {
		t.Fatal("expected error for missing port")
	}
	if err := ValidateRootHints([]RootHint{{Name: "a.root.test.", Addresses: []string{"127.0.53.1:53"}}}); err == nil {
		t.Fatal("expected error for root hint with port")
	}
}
```

- [ ] Create `mgmt/internal/snapshot/m3_test.go`:

```go
package snapshot

import (
	"testing"
	"time"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func TestApplyM3(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	sha := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	primary := "127.0.0.1:5300"
	alg := "hmac-sha256"
	keyName := "rpz-key."
	rows := M3Rows{
		Resolution:   store.ResolutionSettings{Mode: "recursive", QnameMinimisation: true, MaxUpstreamQueries: 100, MaxDelegationDepth: 32, AuthorityPort: 5353, RootHints: []store.RootHint{{Name: "a.root.test.", Addresses: []string{"127.0.53.1"}}}},
		ForwardZones: []store.ForwardZone{{Domain: "corp.example.", Addresses: []string{"10.0.0.1:53"}, Validate: true}},
		Dnssec:       store.DnssecSettings{Validation: true, RFC5011: true},
		Anchors:      []store.TrustAnchor{{Zone: ".", DS: "20326 8 2 E06D44B80B8F1D39A95C0B0D7C65D08458E880409BBC683457104237C7F8EC8D"}},
		NTAs: []store.NegativeTrustAnchor{
			{Domain: "broken.example.", ExpiresAt: now.Add(time.Hour)},
			{Domain: "expired.example.", ExpiresAt: now.Add(-time.Second)},
		},
		RPZ: []store.RPZZone{
			{ID: "22222222-2222-2222-2222-222222222222", Name: "rpz.axfr.test.", Position: 2, SourceType: "transfer", PrimaryAddress: &primary, TSIGAlgorithm: &alg, TSIGKeyName: &keyName, TSIGSecret: []byte("0123456789abcdef"), MinRefreshSeconds: 5, PolicyOverride: "given"},
			{ID: "11111111-1111-1111-1111-111111111111", Name: "rpz.file.test.", Position: 1, SourceType: "file", BlobSHA256: &sha, MinRefreshSeconds: 60, PolicyOverride: "nxdomain", RefreshNonce: 3},
		},
	}
	s := &controlv1.ConfigSnapshot{}
	ApplyM3(s, rows, now)
	if s.ResolutionMode != controlv1.ResolutionMode_RESOLUTION_MODE_RECURSIVE || s.Recursion.AuthorityPort != 5353 || len(s.Recursion.RootHints) != 1 {
		t.Fatalf("recursion = %v %v", s.ResolutionMode, s.Recursion)
	}
	if len(s.ForwardZones) != 1 || !s.ForwardZones[0].Validate {
		t.Fatalf("forward zones = %v", s.ForwardZones)
	}
	if len(s.Dnssec.NegativeTrustAnchors) != 1 || s.Dnssec.NegativeTrustAnchors[0].ExpiresUnix != now.Add(time.Hour).Unix() {
		t.Fatalf("expired NTAs must be omitted: %v", s.Dnssec.NegativeTrustAnchors)
	}
	if s.RpzZones[0].Name != "rpz.file.test." || s.RpzZones[0].GetFile().GetBlobSha256() != sha || s.RpzZones[0].PolicyOverride != controlv1.RpzPolicyOverride_RPZ_POLICY_OVERRIDE_NXDOMAIN || s.RpzZones[0].RefreshNonce != 3 {
		t.Fatalf("rpz[0] = %v", s.RpzZones[0])
	}
	tr := s.RpzZones[1].GetTransfer()
	if tr.GetPrimary() != primary || tr.GetTsigAlgorithm() != controlv1.TsigAlgorithm_TSIG_ALGORITHM_HMAC_SHA256 || string(tr.GetTsigSecret()) != "0123456789abcdef" {
		t.Fatalf("rpz[1] = %v", s.RpzZones[1])
	}
}
```

- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh go test ./mgmt/internal/rpz/... ./mgmt/internal/dnssecconf/... ./mgmt/internal/snapshot/...`; expect FAIL with "undefined: ValidateZone".
- [ ] Write the three migrations literally as above.
- [ ] Implement `rpz.ValidateZone`: reject any line whose trimmed prefix is `$INCLUDE` (case-insensitive) with `errors.New("$INCLUDE is not allowed in RPZ zones")`; parse with `dns.NewZoneParser(strings.NewReader(content), dns.Fqdn(origin), "")` and `zp.SetIncludeAllowed(false)`; iterate `zp.Next()`; first parse error → `fmt.Errorf("line %d: %v", ...)`; the apex SOA is required (`"zone has no SOA at the apex"`) and supplies `Serial`; `Records` counts non-apex records; content larger than 50 MiB → error. `Pack` uses `zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))` and `EncodeAll`.
- [ ] Implement `dnssecconf`: `ValidateDS` = four fields, key tag 0..65535, algorithm and digest type 0..255, hex digest with length 40/64/96 hex characters for types 1/2/4; `ValidateDomain` uses `dns.IsDomainName` and returns lowercase FQDN; `ValidateForwardAddresses` uses `net.SplitHostPort` + `netip.ParseAddr`; `ValidateRootHints` requires FQDN names and bare IP addresses.
- [ ] Implement `store/m3.go` (row structs with the fields used in the test, `LoadM3` selecting everything with `ORDER BY domain`, `ORDER BY position`) and `snapshot.ApplyM3`: mode `recursive` → `RESOLUTION_MODE_RECURSIVE`, otherwise `RESOLUTION_MODE_FORWARD`; NTAs with `ExpiresAt <= now` omitted; RPZ zones sorted by `Position`; policy override strings map to the proto enum; transfer sources carry `TsigSecret` bytes (TLS-protected control stream only). In `snapshot.Build`, call `store.LoadM3` and `ApplyM3(snap, rows, time.Now())`. A scheduled job in `mgmt/internal/snapshot` (every 60 s, under `pg_try_advisory_lock(hashtext('nta_expiry'))`) deletes expired NTAs through the normal mutation transaction so engines receive a new version when one expires.
- [ ] Add the OpenAPI paths and schemas (JSON errors per architecture; every `PUT` carries `revision` and returns 409 `conflict` when stale; `createNegativeTrustAnchor` returns 400 `invalid_argument` when `expires_at` is in the past or more than 30 days ahead; `uploadRpzZoneFile` returns 400 with the `ValidateZone` message and 409 when `source_type` is `transfer`; `deleteTrustAnchor` on the last anchor for `.` returns 409 `last_root_anchor`). Regenerate: `scripts/dev-sync.sh && scripts/dev-exec.sh sh -c 'go generate ./mgmt/internal/api && pnpm --dir web gen:api'`; expect exit 0.
- [ ] Implement `api/m3.go` handlers. Each mutation uses the architecture transaction (change rows → `audit.Record(ctx, tx, actor, "<operationId>", before, after)` with `tsig_secret` redacted to `"***"` → build snapshot → insert `config_versions` → `pg_notify`). `uploadRpzZoneFile`: `ValidateZone(zone.Name, body.Content)`, `Pack`, `store.PutBlob`, update `blob_sha256`, `file_records`, `revision+1`. `refreshRpzZone`: `refresh_nonce = refresh_nonce + 1`, responds `202 Accepted` with an empty body. `reorderRpzZones`: the `ids` must be exactly the set of zone IDs (else 400); positions rewritten `1..n` inside the transaction (temporarily negated to satisfy the unique index). `getRpzZone`/`listRpzZones` join `engine_rpz_status` + `engines.name` into `status`, set `tsig_secret_set`. `getDnssecStatus` reads `engine_dnssec_status`. Create handlers return 201 with the resource; DELETE handlers return 204.
- [ ] Add every operationId to `permissions.go`: `get*`/`list*` → `viewer`; all others → `operator`.
- [ ] Implement `stats.RecordM3`, registered with `control.Hub.OnStats`: when `s.Dnssec != nil`, upsert `engine_dnssec_status(engine_id, stats = protojson of s.Dnssec, reported_at = now())`; for each `s.RpzZones` whose `id` parses as a UUID of an existing zone, upsert `engine_rpz_status`.
- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh go test ./mgmt/internal/rpz/... ./mgmt/internal/dnssecconf/... ./mgmt/internal/snapshot/...`; expect PASS.
- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh make mgmt-test`; expect PASS (migrations apply on a fresh database in the existing store tests).
- [ ] Commit: `git add mgmt web/src/api/schema.d.ts && git commit -m "feat(mgmt): resolution, DNSSEC and RPZ API, migrations and snapshot fields"`.

## Task 12: GUI — `/rpz`, `/dnssec`, resolution settings and forward zones on `/upstreams`

Files:

- `web/src/pages/rpz/RpzPage.tsx` — ordered zone table (drag handle + up/down buttons), status per engine, create/edit dialog, file upload, refresh, delete.
- `web/src/pages/rpz/RpzZoneDialog.tsx` — create/edit form (file vs transfer, primary, TSIG, override, min refresh).
- `web/src/pages/dnssec/DnssecPage.tsx` — validation toggles, trust anchors, NTAs, per-engine validation stats and anchor states with rollover warning.
- `web/src/pages/upstreams/ResolutionCard.tsx` — mode, QNAME minimisation, aggressive NSEC, limits, authority port, root-hint override.
- `web/src/pages/upstreams/ForwardZonesTable.tsx` — forward zone CRUD.
- `web/src/pages/upstreams/UpstreamsPage.tsx` — renders the two new components above the M1 upstream list.
- `web/src/api/m3.ts` — TanStack Query hooks over the generated `openapi-fetch` client.
- `web/src/router.tsx`, `web/src/components/nav/Sidebar.tsx` — routes `/rpz`, `/dnssec` and nav entries (icons `ShieldBan`, `ShieldCheck` from lucide-react).
- `web/e2e/rpz.spec.ts`, `web/e2e/dnssec.spec.ts`, `web/e2e/resolution.spec.ts` — Playwright tests.

Interfaces:

```ts
// web/src/api/m3.ts
export function useResolutionSettings(): UseQueryResult<
  components["schemas"]["ResolutionSettings"]
>;
export function useUpdateResolutionSettings(): UseMutationResult<
  components["schemas"]["ResolutionSettings"],
  ApiError,
  components["schemas"]["ResolutionSettings"]
>;
export function useForwardZones(): UseQueryResult<
  components["schemas"]["ForwardZone"][]
>;
export function useSaveForwardZone(): UseMutationResult<
  components["schemas"]["ForwardZone"],
  ApiError,
  Partial<components["schemas"]["ForwardZone"]>
>;
export function useDeleteForwardZone(): UseMutationResult<
  void,
  ApiError,
  string
>;
export function useDnssecSettings(): UseQueryResult<
  components["schemas"]["DnssecSettings"]
>;
export function useUpdateDnssecSettings(): UseMutationResult<
  components["schemas"]["DnssecSettings"],
  ApiError,
  components["schemas"]["DnssecSettings"]
>;
export function useDnssecStatus(): UseQueryResult<
  components["schemas"]["DnssecStatus"]
>; // refetchInterval 10_000
export function useTrustAnchors(): UseQueryResult<
  components["schemas"]["TrustAnchor"][]
>;
export function useCreateTrustAnchor(): UseMutationResult<
  components["schemas"]["TrustAnchor"],
  ApiError,
  { zone: string; ds: string }
>;
export function useDeleteTrustAnchor(): UseMutationResult<
  void,
  ApiError,
  string
>;
export function useNegativeTrustAnchors(): UseQueryResult<
  components["schemas"]["NegativeTrustAnchor"][]
>;
export function useCreateNegativeTrustAnchor(): UseMutationResult<
  components["schemas"]["NegativeTrustAnchor"],
  ApiError,
  { domain: string; reason: string; expires_at: string }
>;
export function useDeleteNegativeTrustAnchor(): UseMutationResult<
  void,
  ApiError,
  string
>;
export function useRpzZones(): UseQueryResult<
  components["schemas"]["RpzZone"][]
>; // refetchInterval 10_000
export function useRpzZone(
  id: string,
): UseQueryResult<components["schemas"]["RpzZone"]>;
export function useSaveRpzZone(): UseMutationResult<
  components["schemas"]["RpzZone"],
  ApiError,
  Partial<components["schemas"]["RpzZone"]>
>;
export function useDeleteRpzZone(): UseMutationResult<void, ApiError, string>;
export function useUploadRpzZoneFile(): UseMutationResult<
  components["schemas"]["RpzZone"],
  ApiError,
  { id: string; content: string; revision: number }
>;
export function useRefreshRpzZone(): UseMutationResult<void, ApiError, string>;
export function useReorderRpzZones(): UseMutationResult<
  void,
  ApiError,
  string[]
>;
```

The Playwright fixture consumed from M1 is `web/e2e/fixtures.ts` exporting `test` (with fixtures `adminPage: Page`, `viewerPage: Page`, `api: APIRequestContext` authenticated as admin against a freshly started management plane) and `expect`.

- [ ] Create `web/e2e/rpz.spec.ts`:

```ts
import { expect, test } from "./fixtures";

const ZONE = `$TTL 60
@ SOA ns.rpz. hostmaster.rpz. 7 60 60 86400 60
@ NS ns.rpz.
bad.example CNAME .
local.example A 10.9.9.9
`;

test("rpz zones: create file zone, upload, create transfer zone, reorder, refresh, delete", async ({
  adminPage: page,
}) => {
  for (const op of [
    "listRpzZones",
    "createRpzZone",
    "getRpzZone",
    "updateRpzZone",
    "deleteRpzZone",
    "uploadRpzZoneFile",
    "refreshRpzZone",
    "reorderRpzZones",
  ]) {
    test.info().annotations.push({ type: "operation", description: op });
  }
  await page.goto("/rpz");
  await expect(
    page.getByRole("heading", { name: "Response Policy Zones" }),
  ).toBeVisible();

  await page.getByRole("button", { name: "Add zone" }).click();
  await page.getByLabel("Zone name").fill("rpz.file.test.");
  await page.getByLabel("Source").selectOption("file");
  await page.getByRole("button", { name: "Save" }).click();
  const fileRow = page.getByRole("row", { name: /rpz\.file\.test\./ });
  await expect(fileRow).toBeVisible();

  await fileRow.getByRole("button", { name: "Upload file" }).click();
  await page
    .getByLabel("Zone file")
    .setInputFiles({
      name: "rpz.zone",
      mimeType: "text/plain",
      buffer: Buffer.from(ZONE),
    });
  await page.getByRole("button", { name: "Upload" }).click();
  await expect(fileRow.getByText("2 records")).toBeVisible();

  await page.getByRole("button", { name: "Add zone" }).click();
  await page.getByLabel("Zone name").fill("rpz.axfr.test.");
  await page.getByLabel("Source").selectOption("transfer");
  await page.getByLabel("Primary").fill("127.0.0.1:5300");
  await page.getByLabel("TSIG key name").fill("rpz-key.");
  await page.getByLabel("TSIG algorithm").selectOption("hmac-sha256");
  await page
    .getByLabel("TSIG secret (base64)")
    .fill("MDEyMzQ1Njc4OWFiY2RlZg==");
  await page.getByLabel("Policy override").selectOption("nxdomain");
  await page.getByRole("button", { name: "Save" }).click();
  const axfrRow = page.getByRole("row", { name: /rpz\.axfr\.test\./ });
  await expect(axfrRow.getByText("secret set")).toBeVisible();

  await axfrRow.getByRole("button", { name: "Move up" }).click();
  await expect(page.getByRole("row").nth(1)).toContainText("rpz.axfr.test.");

  await axfrRow.getByRole("button", { name: "Edit" }).click();
  await page.getByLabel("Minimum refresh (seconds)").fill("120");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(axfrRow.getByText("120 s")).toBeVisible();

  await axfrRow.getByRole("button", { name: "Refresh now" }).click();
  await expect(page.getByText("Refresh requested")).toBeVisible();

  await fileRow.getByRole("button", { name: "Delete" }).click();
  await page.getByRole("button", { name: "Confirm" }).click();
  await expect(fileRow).toHaveCount(0);
});

test("rpz upload rejects $INCLUDE with the server message", async ({
  adminPage: page,
}) => {
  await page.goto("/rpz");
  await page.getByRole("button", { name: "Add zone" }).click();
  await page.getByLabel("Zone name").fill("rpz.bad.test.");
  await page.getByLabel("Source").selectOption("file");
  await page.getByRole("button", { name: "Save" }).click();
  const row = page.getByRole("row", { name: /rpz\.bad\.test\./ });
  await row.getByRole("button", { name: "Upload file" }).click();
  await page
    .getByLabel("Zone file")
    .setInputFiles({
      name: "bad.zone",
      mimeType: "text/plain",
      buffer: Buffer.from("$INCLUDE /etc/passwd\n"),
    });
  await page.getByRole("button", { name: "Upload" }).click();
  await expect(page.getByRole("alert")).toContainText(
    "$INCLUDE is not allowed",
  );
});

test("viewer sees rpz zones read-only", async ({ viewerPage: page }) => {
  await page.goto("/rpz");
  await expect(
    page.getByRole("heading", { name: "Response Policy Zones" }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "Add zone" })).toHaveCount(0);
});
```

- [ ] Create `web/e2e/dnssec.spec.ts`:

```ts
import { expect, test } from "./fixtures";

test("dnssec: settings, trust anchors, negative trust anchors and status", async ({
  adminPage: page,
}) => {
  for (const op of [
    "getDnssecSettings",
    "updateDnssecSettings",
    "getDnssecStatus",
    "listTrustAnchors",
    "createTrustAnchor",
    "deleteTrustAnchor",
    "listNegativeTrustAnchors",
    "createNegativeTrustAnchor",
    "deleteNegativeTrustAnchor",
  ]) {
    test.info().annotations.push({ type: "operation", description: op });
  }
  await page.goto("/dnssec");
  await expect(page.getByRole("heading", { name: "DNSSEC" })).toBeVisible();
  await expect(page.getByRole("row", { name: /20326 8 2/ })).toContainText(
    "IANA",
  );
  await expect(page.getByRole("row", { name: /38696 8 2/ })).toContainText(
    "IANA",
  );

  await page
    .getByRole("switch", { name: "Automated trust anchor updates (RFC 5011)" })
    .click();
  await page.getByRole("button", { name: "Save settings" }).click();
  await expect(page.getByText("Settings saved")).toBeVisible();

  await page.getByRole("button", { name: "Add trust anchor" }).click();
  await page.getByLabel("Zone").fill("example.");
  await page
    .getByLabel("DS record")
    .fill(
      "12345 13 2 0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
    );
  await page.getByRole("button", { name: "Save" }).click();
  const anchor = page.getByRole("row", { name: /12345 13 2/ });
  await expect(anchor).toBeVisible();
  await anchor.getByRole("button", { name: "Delete" }).click();
  await page.getByRole("button", { name: "Confirm" }).click();
  await expect(anchor).toHaveCount(0);

  await page.getByRole("button", { name: "Add negative trust anchor" }).click();
  await page.getByLabel("Domain").fill("broken.example.");
  await page.getByLabel("Reason").fill("expired signatures at provider");
  await page.getByLabel("Expires in").selectOption("1d");
  await page.getByRole("button", { name: "Save" }).click();
  const nta = page.getByRole("row", { name: /broken\.example\./ });
  await expect(nta).toContainText("expired signatures at provider");
  await nta.getByRole("button", { name: "Delete" }).click();
  await page.getByRole("button", { name: "Confirm" }).click();
  await expect(nta).toHaveCount(0);

  await expect(
    page.getByRole("region", { name: "Validation by engine" }),
  ).toBeVisible();
});

test("dnssec rejects malformed DS", async ({ adminPage: page }) => {
  await page.goto("/dnssec");
  await page.getByRole("button", { name: "Add trust anchor" }).click();
  await page.getByLabel("Zone").fill("example.");
  await page.getByLabel("DS record").fill("1 13 2 ZZ");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("alert")).toContainText("digest");
});
```

- [ ] Create `web/e2e/resolution.spec.ts`:

```ts
import { expect, test } from "./fixtures";

test("resolution settings and forward zones on /upstreams", async ({
  adminPage: page,
}) => {
  for (const op of [
    "getResolutionSettings",
    "updateResolutionSettings",
    "listForwardZones",
    "createForwardZone",
    "updateForwardZone",
    "deleteForwardZone",
  ]) {
    test.info().annotations.push({ type: "operation", description: op });
  }
  await page.goto("/upstreams");
  const card = page.getByRole("region", { name: "Resolution" });
  await card.getByLabel("Mode").selectOption("recursive");
  await card
    .getByLabel("Maximum upstream queries per client query")
    .fill("150");
  await card.getByRole("button", { name: "Add root hint" }).click();
  await card.getByLabel("Root hint name").fill("a.root.test.");
  await card.getByLabel("Root hint addresses").fill("127.0.53.1");
  await card.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText("Resolution settings saved")).toBeVisible();
  await page.reload();
  await expect(
    page.getByRole("region", { name: "Resolution" }).getByLabel("Mode"),
  ).toHaveValue("recursive");

  const fz = page.getByRole("region", { name: "Forward zones" });
  await fz.getByRole("button", { name: "Add forward zone" }).click();
  await page.getByLabel("Domain").fill("corp.example");
  await page.getByLabel("Servers").fill("10.0.0.1:53, 10.0.0.2:53");
  await page.getByRole("button", { name: "Save" }).click();
  const row = fz.getByRole("row", { name: /corp\.example\./ });
  await expect(row).toContainText("10.0.0.2:53");
  await row.getByRole("button", { name: "Edit" }).click();
  await page.getByLabel("Validate DNSSEC").check();
  await page.getByRole("button", { name: "Save" }).click();
  await expect(row).toContainText("validated");
  await row.getByRole("button", { name: "Delete" }).click();
  await page.getByRole("button", { name: "Confirm" }).click();
  await expect(row).toHaveCount(0);
});
```

- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh pnpm --dir web exec playwright test e2e/rpz.spec.ts e2e/dnssec.spec.ts e2e/resolution.spec.ts`; expect FAIL with "getByRole('heading', { name: 'Response Policy Zones' })" not found (route `/rpz` does not exist).
- [ ] Implement `web/src/api/m3.ts` hooks (query keys `['resolution']`, `['forward-zones']`, `['dnssec','settings']`, `['dnssec','status']`, `['dnssec','anchors']`, `['dnssec','ntas']`, `['rpz-zones']`, `['rpz-zones', id]`; every mutation invalidates its list key; `ApiError` shows the server `message` in a Radix `Alert` with `role="alert"`).
- [ ] Implement the pages with the exact accessible names used in the tests:
  - `RpzPage`: heading "Response Policy Zones"; button "Add zone" (hidden for `viewer`); table rows show name, source ("file"/"transfer"), `N records` (file) or primary, override, "secret set" badge when `tsig_secret_set`, `min_refresh_seconds` as `N s`, and per-engine serial/last success/stale badge (red "stale" when any engine reports `stale`, amber when `last_error` non-empty); row buttons "Move up", "Move down" (call `reorderRpzZones` with the full new ID order), "Edit", "Upload file" (file zones), "Refresh now" (transfer zones; toast "Refresh requested"), "Delete" (confirm dialog button "Confirm").
  - `RpzZoneDialog`: labels "Zone name", "Source", "Primary", "TSIG key name", "TSIG algorithm", "TSIG secret (base64)", "Policy override", "Minimum refresh (seconds)"; upload dialog label "Zone file" reads the file with `File.text()` and button "Upload".
  - `DnssecPage`: heading "DNSSEC"; switches "Validate answers" and "Automated trust anchor updates (RFC 5011)", button "Save settings" (toast "Settings saved"); anchors table with source badge "IANA"/"Operator", button "Add trust anchor" (labels "Zone", "DS record"); NTA table with "Add negative trust anchor" (labels "Domain", "Reason", "Expires in" options `1h`, `1d`, `7d`, `30d` converted to `expires_at`); region "Validation by engine" (`<section aria-label>`) showing secure/insecure/bogus/indeterminate counts and trust anchor key states; a red banner "Trust anchor refresh failing" when any anchor for `.` has a `last_error` and `last_refresh_success` older than 72 h, or when no key for `.` is in state `valid`/`configured`.
  - `ResolutionCard` (`<section aria-label="Resolution">`): labels "Mode", "QNAME minimisation", "Aggressive NSEC caching", "Maximum upstream queries per client query", "Maximum delegation depth", "Authority port", root hint rows ("Add root hint", "Root hint name", "Root hint addresses" comma-separated), button "Save" (toast "Resolution settings saved"); mode help text states that forward zones always override.
  - `ForwardZonesTable` (`<section aria-label="Forward zones">`): "Add forward zone", dialog labels "Domain", "Servers" (comma-separated `ip:port`), "Validate DNSSEC"; row badge "validated" when `validate`.
- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh pnpm --dir web exec playwright test e2e/rpz.spec.ts e2e/dnssec.spec.ts e2e/resolution.spec.ts`; expect PASS (6 tests).
- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh go test ./e2e/... -run TestGUICoverage`; expect PASS (all 23 new operationIds covered).
- [ ] Commit: `git add web && git commit -m "feat(web): RPZ, DNSSEC and resolution screens"`.

## Task 13: Private DNS hierarchy fixture and harness helpers (fake root/TLD/leaf, signed zones, spoofer, BIND primary)

Files:

- `e2e/fixtures/authhier/spec.go` — `Spec`, `ZoneSpec`, `DefaultSpec`.
- `e2e/fixtures/authhier/sign.go` — key generation, DS, RRSIG, NSEC and NSEC3 chains with miekg/dns.
- `e2e/fixtures/authhier/server.go` — per-zone authoritative UDP/TCP servers, spoofing behaviour, validating-forwarder endpoint, stats HTTP.
- `e2e/fixtures/authhier/authhier_test.go` — in-process tests.
- `e2e/fixtures/cmd/nexora-fixture/authhier.go` — `authhier` subcommand registration.
- `e2e/harness/hierarchy.go` — `StartHierarchy`, `ConfigureRecursion`, stats accessors.
- `e2e/harness/named.go` — `StartNamed` (BIND 9 primary with TSIG, IXFR from differences).
- `e2e/harness/named_test.go` — harness self-test for BIND.
- `go.mod`, `go.sum` — `github.com/miekg/dns v1.1.73`.

Interfaces:

```go
// package authhier
type ZoneSpec struct {
	Origin          string   `json:"origin"`
	ServerIP        string   `json:"server_ip"`
	Signed          bool     `json:"signed"`
	NSEC3Iterations int      `json:"nsec3_iterations"` // -1: NSEC; >= 0: NSEC3 with this iteration count
	BreakSignatures bool     `json:"break_signatures"` // flips the last byte of every RRSIG over A/AAAA/TXT
	Spoof           bool     `json:"spoof"`
	Records         []string `json:"records"`          // presentation format, owner names absolute
}
type Spec struct {
	Port          int        `json:"port"`
	ForwarderIP   string     `json:"forwarder_ip"`
	Zones         []ZoneSpec `json:"zones"`
}
type Ready struct {
	Port      int                `json:"port"`
	RootDS    string             `json:"root_ds"`
	RootHints []RootHint         `json:"root_hints"`
	Forwarder string             `json:"forwarder"`
	StatsURL  string             `json:"stats_url"`
}
type RootHint struct{ Name string `json:"name"`; Addresses []string `json:"addresses"` }
type Stats struct{ Queries map[string]int `json:"queries"`; SpoofsSent int `json:"spoofs_sent"` }
func DefaultSpec(port int) Spec
func Start(ctx context.Context, spec Spec, now time.Time) (*Hierarchy, Ready, error)
func (h *Hierarchy) Close()
func (h *Hierarchy) Stats() Stats
// package harness
type Hierarchy struct{ Ready authhier.Ready; env *Env }
func (e *Env) StartHierarchy(t *testing.T) *Hierarchy
func (h *Hierarchy) Stats(t *testing.T) authhier.Stats
func (h *Hierarchy) ConfigureRecursion(t *testing.T, m *Mgmt, e *Engine) // PUT /resolution recursive + hints + port, POST root DS trust anchor, waits applied
type Named struct{ Addr string; KeyName string; KeySecretB64 string; dir string; cmd *exec.Cmd; zoneName string }
func (e *Env) StartNamed(t *testing.T, zoneName, zoneText string) *Named
func (n *Named) UpdateZone(t *testing.T, zoneText string) // rewrites the zone file and sends SIGHUP
func (n *Named) Stop(t *testing.T)
```

`DefaultSpec` (all servers on `127.0.53.0/24`, one shared port):

| origin           | server     | signed              | notes                                                                                                                                                                                                                            |
| ---------------- | ---------- | ------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `.`              | 127.0.53.1 | NSEC                | delegates `test.` (NS `ns.test.` glue 127.0.53.2) with DS                                                                                                                                                                        |
| `test.`          | 127.0.53.2 | NSEC                | delegates `good` (glue 127.0.53.3, DS), `bad` (127.0.53.4, DS), `n3` (127.0.53.8, DS), `plain` (127.0.53.5, no DS), `glueless` (NS `ns2.plain.test.`, no glue, no DS), `spoof` (127.0.53.7, no DS), `poison` (127.0.53.9, no DS) |
| `good.test.`     | 127.0.53.3 | NSEC                | `www A 192.0.2.10`, `alias CNAME www.good.test.`, `big TXT` ×40 strings of 60 octets (response > 1232 octets), `ns A 127.0.53.3`                                                                                                 |
| `bad.test.`      | 127.0.53.4 | NSEC, broken        | `www A 192.0.2.11`                                                                                                                                                                                                               |
| `n3.test.`       | 127.0.53.8 | NSEC3, 0 iterations | `www A 192.0.2.40`                                                                                                                                                                                                               |
| `plain.test.`    | 127.0.53.5 | no                  | `www A 192.0.2.12`, `mail A 192.0.2.13`, `ip A 192.0.2.66`, `pass A 192.0.2.14`, `later A 192.0.2.15`, `ns2 A 127.0.53.6`                                                                                                        |
| `glueless.test.` | 127.0.53.6 | no                  | `www A 192.0.2.20`                                                                                                                                                                                                               |
| `spoof.test.`    | 127.0.53.7 | no, spoofing        | `www A 192.0.2.77`                                                                                                                                                                                                               |
| `poison.test.`   | 127.0.53.9 | no                  | `www A 192.0.2.30`, `sub NS ns.good.test.` with out-of-bailiwick glue `ns.good.test. A 127.0.53.66`                                                                                                                              |

Forwarder endpoint `127.0.53.100` answers RD=1 queries with RA=1 from the deepest zone in the hierarchy containing the name (DS queries from the parent zone), including RRSIG/NSEC when DO=1.

- [ ] Create `e2e/fixtures/authhier/authhier_test.go`:

```go
package authhier

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.ListenPacket("udp", "127.0.53.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	return l.LocalAddr().(*net.UDPAddr).Port
}

func start(t *testing.T) (*Hierarchy, Ready) {
	t.Helper()
	h, r, err := Start(context.Background(), DefaultSpec(freePort(t)), time.Now())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(h.Close)
	return h, r
}

func ask(t *testing.T, server string, port int, name string, qtype uint16, do bool, network string) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(name, qtype)
	m.RecursionDesired = false
	m.SetEdns0(1232, do)
	c := &dns.Client{Net: network, Timeout: 2 * time.Second}
	r, _, err := c.Exchange(m, net.JoinHostPort(server, strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("%s %s @%s: %v", name, dns.TypeToString[qtype], server, err)
	}
	return r
}

func keysOf(r *dns.Msg) []*dns.DNSKEY {
	var out []*dns.DNSKEY
	for _, rr := range r.Answer {
		if k, ok := rr.(*dns.DNSKEY); ok {
			out = append(out, k)
		}
	}
	return out
}

func verifyA(t *testing.T, r *dns.Msg, keys []*dns.DNSKEY) error {
	t.Helper()
	var set []dns.RR
	var sig *dns.RRSIG
	for _, rr := range r.Answer {
		switch v := rr.(type) {
		case *dns.A:
			set = append(set, v)
		case *dns.RRSIG:
			sig = v
		}
	}
	if sig == nil {
		t.Fatal("no RRSIG")
	}
	for _, k := range keys {
		if k.KeyTag() == sig.KeyTag {
			return sig.Verify(k, set)
		}
	}
	t.Fatal("no key with matching tag")
	return nil
}

func TestRootReferralCarriesSignedDSAndRootDSMatches(t *testing.T) {
	_, ready := start(t)
	r := ask(t, "127.0.53.1", ready.Port, "www.good.test.", dns.TypeA, true, "udp")
	if r.Authoritative || len(r.Ns) == 0 {
		t.Fatalf("expected referral, got %v", r)
	}
	var hasDS, hasSig bool
	for _, rr := range r.Ns {
		hasDS = hasDS || rr.Header().Rrtype == dns.TypeDS
		hasSig = hasSig || rr.Header().Rrtype == dns.TypeRRSIG
	}
	if !hasDS || !hasSig {
		t.Fatalf("referral lacks DS/RRSIG: %v", r.Ns)
	}
	keys := ask(t, "127.0.53.1", ready.Port, ".", dns.TypeDNSKEY, true, "udp")
	var matched bool
	for _, k := range keysOf(keys) {
		ds := k.ToDS(dns.SHA256)
		text := fmt.Sprintf("%d %d %d %s", ds.KeyTag, ds.Algorithm, ds.DigestType, strings.ToUpper(ds.Digest))
		if k.Flags == 257 && text == ready.RootDS {
			matched = true
		}
	}
	if !matched {
		t.Fatalf("root DS %q matches no root KSK", ready.RootDS)
	}
}

func TestSignedGoodVerifiesAndBadDoesNot(t *testing.T) {
	_, ready := start(t)
	good := ask(t, "127.0.53.3", ready.Port, "www.good.test.", dns.TypeA, true, "udp")
	if err := verifyA(t, good, keysOf(ask(t, "127.0.53.3", ready.Port, "good.test.", dns.TypeDNSKEY, true, "udp"))); err != nil {
		t.Fatalf("good.test signature: %v", err)
	}
	bad := ask(t, "127.0.53.4", ready.Port, "www.bad.test.", dns.TypeA, true, "udp")
	if err := verifyA(t, bad, keysOf(ask(t, "127.0.53.4", ready.Port, "bad.test.", dns.TypeDNSKEY, true, "udp"))); err == nil {
		t.Fatal("bad.test signature unexpectedly verifies")
	}
}

func TestNegativeAnswersCarryDenialProofs(t *testing.T) {
	_, ready := start(t)
	nx := ask(t, "127.0.53.3", ready.Port, "nope.good.test.", dns.TypeA, true, "udp")
	if nx.Rcode != dns.RcodeNameError || countType(nx.Ns, dns.TypeNSEC) < 2 {
		t.Fatalf("NSEC NXDOMAIN proof missing: %v", nx)
	}
	n3 := ask(t, "127.0.53.8", ready.Port, "nope.n3.test.", dns.TypeA, true, "udp")
	if n3.Rcode != dns.RcodeNameError || countType(n3.Ns, dns.TypeNSEC3) < 2 {
		t.Fatalf("NSEC3 NXDOMAIN proof missing: %v", n3)
	}
	ds := ask(t, "127.0.53.2", ready.Port, "plain.test.", dns.TypeDS, true, "udp")
	if len(ds.Answer) != 0 || countType(ds.Ns, dns.TypeNSEC) != 1 {
		t.Fatalf("insecure delegation proof missing: %v", ds)
	}
}

func countType(rrs []dns.RR, t uint16) int {
	n := 0
	for _, rr := range rrs {
		if rr.Header().Rrtype == t {
			n++
		}
	}
	return n
}

func TestBigAnswerTruncatesOverUDPAndCompletesOverTCP(t *testing.T) {
	_, ready := start(t)
	udp := ask(t, "127.0.53.3", ready.Port, "big.good.test.", dns.TypeTXT, false, "udp")
	if !udp.Truncated {
		t.Fatal("expected TC=1 over UDP")
	}
	tcp := ask(t, "127.0.53.3", ready.Port, "big.good.test.", dns.TypeTXT, false, "tcp")
	if tcp.Truncated || len(tcp.Answer) != 40 {
		t.Fatalf("tcp answer: tc=%v n=%d", tcp.Truncated, len(tcp.Answer))
	}
}

func TestSpoofServerSendsForgeriesBeforeRealAnswer(t *testing.T) {
	h, ready := start(t)
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.53.7"), Port: ready.Port})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	q := new(dns.Msg)
	q.SetQuestion("wWw.SpOof.test.", dns.TypeA)
	q.Id = 4242
	b, _ := q.Pack()
	conn.Write(b)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	var got []*dns.Msg
	buf := make([]byte, 1500)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			break
		}
		m := new(dns.Msg)
		if m.Unpack(buf[:n]) == nil {
			got = append(got, m)
		}
	}
	if len(got) != 4 {
		t.Fatalf("received %d replies on the query socket, want 4 (wrong id, wrong question, wrong case, real)", len(got))
	}
	last := got[len(got)-1]
	if last.Id != 4242 || last.Question[0].Name != "wWw.SpOof.test." || last.Answer[0].(*dns.A).A.String() != "192.0.2.77" {
		t.Fatalf("real answer wrong: %v", last)
	}
	if h.Stats().SpoofsSent != 4 {
		t.Fatalf("spoofs_sent = %d, want 4 (3 on socket + 1 from another port)", h.Stats().SpoofsSent)
	}
}

func TestForwarderEndpointAnswersRecursivelyWithSignatures(t *testing.T) {
	_, ready := start(t)
	host, portStr, _ := net.SplitHostPort(ready.Forwarder)
	port, _ := strconv.Atoi(portStr)
	m := new(dns.Msg)
	m.SetQuestion("www.good.test.", dns.TypeA)
	m.SetEdns0(1232, true)
	r, _, err := (&dns.Client{Timeout: 2 * time.Second}).Exchange(m, net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil || !r.RecursionAvailable || countType(r.Answer, dns.TypeRRSIG) != 1 {
		t.Fatalf("forwarder answer: %v %v", r, err)
	}
}
```

- [ ] Create `e2e/harness/named_test.go`:

```go
package harness

import (
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestNamedHarnessServesTSIGAXFRAndReloads(t *testing.T) {
	env := New(t)
	zone := "$TTL 60\n@ SOA ns.rpz.axfr.test. h.rpz.axfr.test. 1 2 1 30 60\n@ NS ns.rpz.axfr.test.\nns A 127.0.0.1\nblocked-axfr.plain.test CNAME .\n"
	n := env.StartNamed(t, "rpz.axfr.test.", zone)
	axfr := func() []dns.RR {
		tr := &dns.Transfer{TsigSecret: map[string]string{n.KeyName: n.KeySecretB64}}
		m := new(dns.Msg)
		m.SetAxfr("rpz.axfr.test.")
		m.SetTsig(n.KeyName, dns.HmacSHA256, 300, time.Now().Unix())
		ch, err := tr.In(m, n.Addr)
		if err != nil {
			t.Fatalf("axfr: %v", err)
		}
		var rrs []dns.RR
		for env := range ch {
			if env.Error != nil {
				t.Fatalf("axfr envelope: %v", env.Error)
			}
			rrs = append(rrs, env.RR...)
		}
		return rrs
	}
	if got := len(axfr()); got != 5 {
		t.Fatalf("axfr records = %d, want 5 (SOA NS A CNAME SOA)", got)
	}
	n.UpdateZone(t, "$TTL 60\n@ SOA ns.rpz.axfr.test. h.rpz.axfr.test. 2 2 1 30 60\n@ NS ns.rpz.axfr.test.\nns A 127.0.0.1\nblocked-axfr.plain.test CNAME .\nnew.plain.test CNAME .\n")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(axfr()) != 6 {
		time.Sleep(100 * time.Millisecond)
	}
	if got := len(axfr()); got != 6 {
		t.Fatalf("after reload axfr records = %d, want 6", got)
	}
	unsigned := new(dns.Msg)
	unsigned.SetAxfr("rpz.axfr.test.")
	ch, err := (&dns.Transfer{}).In(unsigned, n.Addr)
	if err == nil {
		for e := range ch {
			if e.Error == nil && len(e.RR) > 0 {
				t.Fatal("unsigned AXFR must be refused")
			}
		}
	}
}
```

- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh go test ./e2e/fixtures/authhier/... ./e2e/harness/ -run 'Test(Root|Signed|Negative|Big|Spoof|Forwarder|NamedHarness)'`; expect FAIL with "undefined: Start".
- [ ] Implement `sign.go` (miekg/dns). Per signed zone: KSK `DNSKEY{Flags: 257, Protocol: 3, Algorithm: dns.ECDSAP256SHA256}` and ZSK `Flags: 256`, both `Generate(256)`; group records into RRsets by `(lowercase owner, type)`; sign every authoritative RRset except delegation NS and glue with the ZSK, the DNSKEY RRset with the KSK; `Inception = now - 1h`, `Expiration = now + 24h`, `SignerName = origin`, `OrigTtl = rrset TTL`, `Labels = dns.CountLabel(owner)` (minus one for a leading `*`). `BreakSignatures` flips the last byte of the base64-decoded signature of RRSIGs covering A/AAAA/TXT. DS for a child = `childKSK.ToDS(dns.SHA256)` inserted into the parent zone before the parent is signed (children are built first: `Start` signs zones sorted by label count descending). `Ready.RootDS` is the root KSK DS rdata `"<tag> 13 2 <HEX>"`. Canonical order for NSEC:

```go
func canonicalLess(a, b string) bool {
	la, lb := dns.SplitDomainName(strings.ToLower(a)), dns.SplitDomainName(strings.ToLower(b))
	for i, j := len(la)-1, len(lb)-1; i >= 0 && j >= 0; i, j = i-1, j-1 {
		x, y := unescape(la[i]), unescape(lb[j])
		if c := bytes.Compare(x, y); c != 0 {
			return c < 0
		}
	}
	return len(la) < len(lb)
}
```

(`unescape` turns `\DDD` and `\X` escapes into raw octets.) NSEC chain: owner names = every authoritative owner plus delegation points (bitmap `NS DS RRSIG NSEC` for signed children, `NS RRSIG NSEC` for unsigned ones); apex bitmap includes `SOA NS DNSKEY RRSIG NSEC`. NSEC3 (`NSEC3Iterations >= 0`): `dns.HashName(owner, dns.SHA1, iterations, "")` owners `<hash>.<origin>`, empty-non-terminal hashes included, NSEC3PARAM at apex, flags 0.

- [ ] Implement `server.go`: for every zone a `dns.Server` on UDP and TCP at `ServerIP:Port` with a handler implementing: REFUSED outside the origin; referral (AA=0, NS in authority, DS + RRSIG or the delegation's NSEC + RRSIG when the zone is signed and DO=1, glue from the zone's records for NS targets); DS queries at a cut answered authoritatively from the parent; exact answers with RRSIGs when DO=1; CNAME answers; NODATA with SOA (+NSEC/NSEC3 proof); NXDOMAIN with SOA + covering NSEC + wildcard-covering NSEC (NSEC) or closest-encloser match + next-closer cover + wildcard cover (NSEC3); UDP responses larger than the client EDNS size (or 512) are returned with TC=1 and no records; the reply echoes the query name's exact casing (`r.Question = q.Question`). `Spoof` zones: the UDP handler writes, in order and through the same `dns.ResponseWriter`: (1) the real answer with `Id ^ 0xFFFF` and `A 6.6.6.6`; (2) correct ID with question name `evil.spoof.test.` and `A 6.6.6.6`; (3) correct ID with the query name in all lower case and `A 6.6.6.6` (a 0x20 case mismatch whenever the query had upper-case letters); (4) from a second UDP socket bound to `ServerIP:0` (different source port) to the query's source address: correct ID and question with `A 6.6.6.6`; then after 30 ms (5) the real answer — `SpoofsSent` increments for (1)–(4). Every query increments `Queries[ServerIP]`. Forwarder at `ForwarderIP:Port`: resolves by selecting the deepest zone whose origin contains the name (for DS, the parent of the deepest), answering as that zone would authoritatively but with AA=0, RA=1 and following in-hierarchy CNAMEs. Stats HTTP server on `127.0.0.1:0` serving `GET /stats` as JSON.
- [ ] Implement `nexora-fixture authhier --spec <file|default> --port <n> --ready-file <path>`: registers `subcommands["authhier"]`, builds `DefaultSpec(port)` for `--spec default`, calls `Start`, writes `Ready` JSON to the ready file, blocks until SIGTERM.
- [ ] Implement `harness.StartHierarchy`: port = `FreePort(t)` probed on 127.0.53.1 UDP+TCP; runs `env.BinPath("nexora-fixture") authhier --spec default --port P --ready-file <tmp>`, waits up to 10 s for the ready file, registers cleanup. `ConfigureRecursion`: `PUT /api/v1/resolution` `{mode: "recursive", qname_minimisation: true, aggressive_nsec: false, max_upstream_queries: 100, max_delegation_depth: 32, authority_port: P, root_hints: Ready.RootHints, revision: <current>}`, `POST /api/v1/dnssec/trust-anchors {zone: ".", ds: Ready.RootDS}`, then `m.WaitApplied(t, e, 10*time.Second)`.
- [ ] Implement `harness.StartNamed`: temp dir with `named.conf`:

```go
const namedConf = `options {
	directory "{{.Dir}}";
	listen-on port {{.Port}} { 127.0.0.1; };
	listen-on-v6 { none; };
	pid-file "{{.Dir}}/named.pid";
	recursion no;
	notify no;
	allow-transfer { key "{{.KeyName}}"; };
	ixfr-from-differences yes;
	dnssec-validation no;
};
key "{{.KeyName}}" {
	algorithm hmac-sha256;
	secret "{{.Secret}}";
};
zone "{{.Zone}}" {
	type primary;
	file "{{.Dir}}/zone.db";
};
`
```

with `KeyName = "rpz-key."`, a random 32-byte base64 secret, the port from `FreePort(t)`; start `named -g -c <dir>/named.conf -u $(id -un)` (omit `-u` when not root), wait until a TCP SOA query succeeds (10 s). `UpdateZone` rewrites `zone.db` and sends `SIGHUP` to the process; `Stop` sends `SIGTERM` and waits.

- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh go test ./e2e/fixtures/authhier/... ./e2e/harness/ -run 'Test(Root|Signed|Negative|Big|Spoof|Forwarder|NamedHarness)'`; expect PASS (7 tests).
- [ ] Commit: `git add go.mod go.sum e2e/fixtures e2e/harness && git commit -m "test(e2e): private signed DNS hierarchy fixture and BIND primary harness"`.

## Task 14: Acceptance tests — TestRecursionRootHints, TestSpoofedReplyRejected, TestDNSSECValidation, TestRPZPolicy

Files:

- `e2e/m3_helpers_test.go` — DNS query helper with DO/CD/TCP/source-address options and EDE extraction.
- `e2e/recursion_test.go` — `TestRecursionRootHints`, `TestSpoofedReplyRejected`.
- `e2e/dnssec_test.go` — `TestDNSSECValidation`.
- `e2e/rpz_test.go` — `TestRPZPolicy`.
- `Makefile` — `e2e-build` also builds `nexora-fixture` with the `authhier` subcommand (already part of the fixture binary) and checks `named` is on `PATH`.

Interfaces:

```go
type qopt struct {
	DO, CD, TCP bool
	Source      string // local IP to bind, "" = default
	Timeout     time.Duration
}
func query(t *testing.T, server, name string, qtype uint16, o qopt) *dns.Msg
func queryErr(server, name string, qtype uint16, o qopt) (*dns.Msg, error)
func aValues(m *dns.Msg) []string
func edeCode(m *dns.Msg) (uint16, bool)
func setupRecursion(t *testing.T) (*harness.Env, *harness.Mgmt, *harness.Engine, *harness.Hierarchy)
func eventually(t *testing.T, timeout time.Duration, what string, f func() error)
```

- [ ] Create `e2e/recursion_test.go`:

```go
package e2e

import (
	"testing"

	"github.com/miekg/dns"
)

func TestRecursionRootHints(t *testing.T) {
	_, mg, eng, h := setupRecursion(t)
	addr := eng.DNSAddr()

	var upstreams []map[string]any
	mg.Admin().JSON(t, "GET", "/api/v1/upstreams", nil, &upstreams, 200)
	if len(upstreams) != 0 {
		t.Fatalf("test requires no forwarders configured, found %d", len(upstreams))
	}

	t.Run("resolves from root hints", func(t *testing.T) {
		before := h.Stats(t).Queries["127.0.53.1"]
		wantA(t, query(t, addr, "www.good.test", dns.TypeA, qopt{}), "192.0.2.10")
		if h.Stats(t).Queries["127.0.53.1"] <= before {
			t.Fatal("fake root was never queried")
		}
		if eng.Metric(t, "nexora_resolutions_total", map[string]string{"route": "recursive"}) < 1 {
			t.Fatal("nexora_resolutions_total{route=recursive} did not increase")
		}
	})
	t.Run("cname chain", func(t *testing.T) {
		m := query(t, addr, "alias.good.test", dns.TypeA, qopt{})
		wantA(t, m, "192.0.2.10")
		if _, ok := m.Answer[0].(*dns.CNAME); !ok {
			t.Fatalf("first answer is not the CNAME: %v", m.Answer)
		}
	})
	t.Run("glueless delegation", func(t *testing.T) {
		wantA(t, query(t, addr, "www.glueless.test", dns.TypeA, qopt{}), "192.0.2.20")
	})
	t.Run("tcp fallback on truncated authoritative reply", func(t *testing.T) {
		m := query(t, addr, "big.good.test", dns.TypeTXT, qopt{TCP: true})
		if m.Rcode != dns.RcodeSuccess || len(m.Answer) != 40 {
			t.Fatalf("big TXT over TCP: rcode=%s n=%d", dns.RcodeToString[m.Rcode], len(m.Answer))
		}
		if eng.Metric(t, "nexora_recursor_tcp_fallback_total", nil) < 1 {
			t.Fatal("engine did not retry the truncated authoritative reply over TCP")
		}
	})
	t.Run("nxdomain", func(t *testing.T) {
		if m := query(t, addr, "nope.plain.test", dns.TypeA, qopt{}); m.Rcode != dns.RcodeNameError {
			t.Fatalf("rcode = %s", dns.RcodeToString[m.Rcode])
		}
	})
	t.Run("out-of-bailiwick glue is not used", func(t *testing.T) {
		wantA(t, query(t, addr, "www.poison.test", dns.TypeA, qopt{}), "192.0.2.30")
		_ = query(t, addr, "www.sub.poison.test", dns.TypeA, qopt{})
		wantA(t, query(t, addr, "ns.good.test", dns.TypeA, qopt{}), "127.0.53.3")
		if n := h.Stats(t).Queries["127.0.53.66"]; n != 0 {
			t.Fatalf("engine sent %d queries to the poisoned glue address", n)
		}
	})
	t.Run("fixture servers preserve 0x20 case so no case mismatches occur", func(t *testing.T) {
		if n := eng.Metric(t, "nexora_recursor_mismatched_replies_total", map[string]string{"reason": "case"}); n != 0 {
			t.Fatalf("case mismatches = %v, want 0 against honest servers", n)
		}
	})
}

func TestSpoofedReplyRejected(t *testing.T) {
	_, _, eng, h := setupRecursion(t)
	addr := eng.DNSAddr()

	// positive path first: the hierarchy and recursion work in this run
	wantA(t, query(t, addr, "www.good.test", dns.TypeA, qopt{}), "192.0.2.10")

	m := query(t, addr, "www.spoof.test", dns.TypeA, qopt{})
	wantA(t, m, "192.0.2.77")
	for _, v := range aValues(m) {
		if v == "6.6.6.6" {
			t.Fatal("spoofed address served")
		}
	}
	st := h.Stats(t)
	if st.SpoofsSent < 4 {
		t.Fatalf("fixture sent %d spoofs, want >= 4", st.SpoofsSent)
	}
	for _, reason := range []string{"id", "question", "case"} {
		if eng.Metric(t, "nexora_recursor_mismatched_replies_total", map[string]string{"reason": reason}) < 1 {
			t.Fatalf("mismatched reply with wrong %s was not counted", reason)
		}
	}

	spoofQueries := st.Queries["127.0.53.7"]
	again := query(t, addr, "www.spoof.test", dns.TypeA, qopt{})
	wantA(t, again, "192.0.2.77")
	if got := h.Stats(t).Queries["127.0.53.7"]; got != spoofQueries {
		t.Fatalf("second query reached the authoritative server (%d -> %d); the cached answer must be the real one", spoofQueries, got)
	}
	if again.Answer[len(again.Answer)-1].Header().Ttl >= m.Answer[len(m.Answer)-1].Header().Ttl+1 {
		t.Fatal("cached TTL not decremented")
	}
}
```

- [ ] Create `e2e/dnssec_test.go`:

```go
package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestDNSSECValidation(t *testing.T) {
	_, mg, eng, h := setupRecursion(t)
	addr := eng.DNSAddr()
	api := mg.Admin()

	t.Run("signed zone returns AD", func(t *testing.T) {
		m := query(t, addr, "www.good.test", dns.TypeA, qopt{DO: true})
		wantA(t, m, "192.0.2.10")
		if !m.AuthenticatedData {
			t.Fatal("AD=0 for a secure answer")
		}
		if n := query(t, addr, "www.n3.test", dns.TypeA, qopt{DO: true}); !n.AuthenticatedData {
			t.Fatal("AD=0 for NSEC3-signed zone")
		}
		nx := query(t, addr, "nope.good.test", dns.TypeA, qopt{DO: true})
		if nx.Rcode != dns.RcodeNameError || !nx.AuthenticatedData {
			t.Fatalf("NXDOMAIN proof: rcode=%s ad=%v", dns.RcodeToString[nx.Rcode], nx.AuthenticatedData)
		}
	})
	t.Run("broken signature returns SERVFAIL with EDE", func(t *testing.T) {
		m := query(t, addr, "www.bad.test", dns.TypeA, qopt{DO: true})
		if m.Rcode != dns.RcodeServerFailure || len(aValues(m)) != 0 {
			t.Fatalf("bogus data served: rcode=%s answers=%v", dns.RcodeToString[m.Rcode], aValues(m))
		}
		if code, ok := edeCode(m); !ok || code != 6 {
			t.Fatalf("EDE = %d (present %v), want 6", code, ok)
		}
		if eng.Metric(t, "nexora_dnssec_validations_total", map[string]string{"result": "bogus"}) < 1 {
			t.Fatal("bogus validation not counted")
		}
	})
	t.Run("CD bit returns unvalidated data without AD", func(t *testing.T) {
		m := query(t, addr, "www.bad.test", dns.TypeA, qopt{DO: true, CD: true})
		wantA(t, m, "192.0.2.11")
		if m.AuthenticatedData {
			t.Fatal("AD=1 with CD=1 on bogus data")
		}
	})
	t.Run("unsigned zone returns AD=0", func(t *testing.T) {
		m := query(t, addr, "www.plain.test", dns.TypeA, qopt{DO: true})
		wantA(t, m, "192.0.2.12")
		if m.AuthenticatedData {
			t.Fatal("AD=1 for an insecure answer")
		}
	})
	t.Run("negative trust anchor", func(t *testing.T) {
		var nta struct{ ID string `json:"id"` }
		api.JSON(t, "POST", "/api/v1/dnssec/negative-trust-anchors", map[string]any{"domain": "bad.test.", "reason": "e2e", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, &nta, 201)
		mg.WaitApplied(t, eng, 10*time.Second)
		m := query(t, addr, "www.bad.test", dns.TypeA, qopt{DO: true})
		wantA(t, m, "192.0.2.11")
		if m.AuthenticatedData {
			t.Fatal("AD=1 under an NTA")
		}
		api.JSON(t, "DELETE", "/api/v1/dnssec/negative-trust-anchors/"+nta.ID, nil, nil, 204)
		mg.WaitApplied(t, eng, 10*time.Second)
		if m := query(t, addr, "www.bad.test", dns.TypeA, qopt{DO: true}); m.Rcode != dns.RcodeServerFailure {
			t.Fatalf("after NTA removal rcode = %s, want SERVFAIL (cached insecure answer must not be reused)", dns.RcodeToString[m.Rcode])
		}
	})
	t.Run("forwarded answers are validated", func(t *testing.T) {
		var fz struct{ ID string `json:"id"` }
		api.JSON(t, "POST", "/api/v1/forward-zones", map[string]any{"domain": "test.", "addresses": []string{h.Ready.Forwarder}, "validate": true}, &fz, 201)
		mg.WaitApplied(t, eng, 10*time.Second)
		before := eng.Metric(t, "nexora_resolutions_total", map[string]string{"route": "forward_zone"})
		good := query(t, addr, "www.good.test", dns.TypeA, qopt{DO: true})
		wantA(t, good, "192.0.2.10")
		if !good.AuthenticatedData {
			t.Fatal("forwarded secure answer lacks AD")
		}
		if bad := query(t, addr, "www.bad.test", dns.TypeA, qopt{DO: true}); bad.Rcode != dns.RcodeServerFailure {
			t.Fatalf("forwarded bogus answer rcode = %s", dns.RcodeToString[bad.Rcode])
		}
		if after := eng.Metric(t, "nexora_resolutions_total", map[string]string{"route": "forward_zone"}); after <= before {
			t.Fatalf("forward zone route not used (%v -> %v)", before, after)
		}
		api.JSON(t, "DELETE", "/api/v1/forward-zones/"+fz.ID, nil, nil, 204)
	})
	t.Run("status reports trust anchors", func(t *testing.T) {
		eventually(t, 30*time.Second, "engine dnssec status", func() error {
			var st struct {
				Engines []struct {
					Secure       int64 `json:"secure"`
					TrustAnchors []struct{ Zone string `json:"zone"` } `json:"trust_anchors"`
				} `json:"engines"`
			}
			api.JSON(t, "GET", "/api/v1/dnssec/status", nil, &st, 200)
			if len(st.Engines) != 1 || st.Engines[0].Secure < 1 || len(st.Engines[0].TrustAnchors) < 1 {
				return fmt.Errorf("status = %+v", st)
			}
			return nil
		})
	})
}
```

- [ ] Create `e2e/rpz_test.go`:

```go
package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/miekg/dns"
)

const rpzFileZone = `$TTL 60
@ SOA ns.rpz.file.test. h.rpz.file.test. 1 60 60 86400 60
@ NS ns.rpz.file.test.
www.plain.test CNAME .
mail.plain.test A 10.9.9.9
pass.plain.test CNAME rpz-passthru.
32.66.2.0.192.rpz-ip CNAME *.
32.2.0.0.127.rpz-client-ip CNAME rpz-tcp-only.
`

func rpzAxfrZone(serial int, extra string) string {
	return fmt.Sprintf("$TTL 60\n@ SOA ns.rpz.axfr.test. h.rpz.axfr.test. %d 2 1 30 60\n@ NS ns.rpz.axfr.test.\nns A 127.0.0.1\npass.plain.test CNAME .\nblocked-axfr.plain.test A 10.8.8.8\nns2.plain.test.rpz-nsdname CNAME .\n%s", serial, extra)
}

type rpzZone struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Status   []struct {
		Serial    int64  `json:"serial"`
		LastError string `json:"last_error"`
	} `json:"status"`
}

func TestRPZPolicy(t *testing.T) {
	env, mg, eng, _ := setupRecursion(t)
	addr := eng.DNSAddr()
	api := mg.Admin()

	// positive path before any policy exists
	wantA(t, query(t, addr, "www.plain.test", dns.TypeA, qopt{}), "192.0.2.12")
	wantA(t, query(t, addr, "www.glueless.test", dns.TypeA, qopt{}), "192.0.2.20")
	wantA(t, query(t, addr, "later.plain.test", dns.TypeA, qopt{}), "192.0.2.15")
	wantA(t, query(t, addr, "www.good.test", dns.TypeA, qopt{Source: "127.0.0.2"}), "192.0.2.10")

	named := env.StartNamed(t, "rpz.axfr.test.", rpzAxfrZone(1, ""))

	var file rpzZone
	api.JSON(t, "POST", "/api/v1/rpz-zones", map[string]any{"name": "rpz.file.test.", "source_type": "file", "policy_override": "given", "min_refresh_seconds": 60}, &file, 201)
	api.JSON(t, "PUT", "/api/v1/rpz-zones/"+file.ID+"/file", map[string]any{"content": rpzFileZone, "revision": file.Revision}, &file, 200)
	var axfr rpzZone
	api.JSON(t, "POST", "/api/v1/rpz-zones", map[string]any{"name": "rpz.axfr.test.", "source_type": "transfer", "primary": named.Addr, "tsig_key_name": named.KeyName, "tsig_algorithm": "hmac-sha256", "tsig_secret": named.KeySecretB64, "policy_override": "given", "min_refresh_seconds": 1}, &axfr, 201)
	mg.WaitApplied(t, eng, 10*time.Second)
	eventually(t, 20*time.Second, "axfr zone loaded", func() error {
		var z rpzZone
		api.JSON(t, "GET", "/api/v1/rpz-zones/"+axfr.ID, nil, &z, 200)
		if len(z.Status) != 1 || z.Status[0].Serial != 1 {
			return fmt.Errorf("status %+v", z.Status)
		}
		return nil
	})

	t.Run("file zone qname NXDOMAIN with EDE", func(t *testing.T) {
		m := query(t, addr, "www.plain.test", dns.TypeA, qopt{})
		if m.Rcode != dns.RcodeNameError {
			t.Fatalf("rcode = %s, want NXDOMAIN", dns.RcodeToString[m.Rcode])
		}
		if code, ok := edeCode(m); !ok || code != 15 {
			t.Fatalf("EDE = %d %v, want 15", code, ok)
		}
	})
	t.Run("file zone local data", func(t *testing.T) {
		wantA(t, query(t, addr, "mail.plain.test", dns.TypeA, qopt{}), "10.9.9.9")
	})
	t.Run("zone order: passthru in first zone beats NXDOMAIN in second", func(t *testing.T) {
		wantA(t, query(t, addr, "pass.plain.test", dns.TypeA, qopt{}), "192.0.2.14")
	})
	t.Run("response IP trigger NODATA", func(t *testing.T) {
		m := query(t, addr, "ip.plain.test", dns.TypeA, qopt{})
		if m.Rcode != dns.RcodeSuccess || len(aValues(m)) != 0 {
			t.Fatalf("rcode=%s answers=%v, want NODATA", dns.RcodeToString[m.Rcode], aValues(m))
		}
	})
	t.Run("axfr zone local data", func(t *testing.T) {
		wantA(t, query(t, addr, "blocked-axfr.plain.test", dns.TypeA, qopt{}), "10.8.8.8")
	})
	t.Run("axfr zone NSDNAME trigger overrides a previously cached answer", func(t *testing.T) {
		if m := query(t, addr, "www.glueless.test", dns.TypeA, qopt{}); m.Rcode != dns.RcodeNameError {
			t.Fatalf("rcode = %s, want NXDOMAIN", dns.RcodeToString[m.Rcode])
		}
	})
	t.Run("client-ip trigger forces TCP", func(t *testing.T) {
		udp := query(t, addr, "www.good.test", dns.TypeA, qopt{Source: "127.0.0.2"})
		if !udp.Truncated || len(udp.Answer) != 0 {
			t.Fatalf("UDP from 127.0.0.2: tc=%v answers=%d", udp.Truncated, len(udp.Answer))
		}
		wantA(t, query(t, addr, "www.good.test", dns.TypeA, qopt{Source: "127.0.0.2", TCP: true}), "192.0.2.10")
	})
	t.Run("incremental transfer picks up a change", func(t *testing.T) {
		named.UpdateZone(t, rpzAxfrZone(2, "later.plain.test A 10.7.7.7\n"))
		eventually(t, 20*time.Second, "later.plain.test rewritten", func() error {
			m, err := queryErr(addr, "later.plain.test", dns.TypeA, qopt{})
			if err != nil {
				return err
			}
			if v := aValues(m); len(v) != 1 || v[0] != "10.7.7.7" {
				return fmt.Errorf("answers %v", v)
			}
			return nil
		})
	})
	t.Run("failed refresh keeps last good zone and reports error", func(t *testing.T) {
		named.Stop(t)
		api.JSON(t, "POST", "/api/v1/rpz-zones/"+axfr.ID+"/refresh", map[string]any{}, nil, 202)
		eventually(t, 30*time.Second, "refresh error reported", func() error {
			var z rpzZone
			api.JSON(t, "GET", "/api/v1/rpz-zones/"+axfr.ID, nil, &z, 200)
			if len(z.Status) != 1 || z.Status[0].LastError == "" {
				return fmt.Errorf("status %+v", z.Status)
			}
			return nil
		})
		wantA(t, query(t, addr, "later.plain.test", dns.TypeA, qopt{}), "10.7.7.7")
		wantA(t, query(t, addr, "mail.plain.test", dns.TypeA, qopt{}), "10.9.9.9")
	})
}
```

- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh go vet ./e2e/`; expect FAIL with "undefined: setupRecursion".
- [ ] Create `e2e/m3_helpers_test.go`:

```go
package e2e

import (
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/piwi3910/nexora/e2e/harness"
)

type qopt struct {
	DO, CD, TCP bool
	Source      string
	Timeout     time.Duration
}

func queryErr(server, name string, qtype uint16, o qopt) (*dns.Msg, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), qtype)
	m.RecursionDesired = true
	m.CheckingDisabled = o.CD
	m.SetEdns0(1232, o.DO)
	c := &dns.Client{Timeout: o.Timeout}
	if c.Timeout == 0 {
		c.Timeout = 5 * time.Second
	}
	if o.TCP {
		c.Net = "tcp"
	}
	if o.Source != "" {
		if o.TCP {
			c.Dialer = &net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(o.Source)}, Timeout: c.Timeout}
		} else {
			c.Dialer = &net.Dialer{LocalAddr: &net.UDPAddr{IP: net.ParseIP(o.Source)}, Timeout: c.Timeout}
		}
	}
	r, _, err := c.Exchange(m, server)
	return r, err
}

func query(t *testing.T, server, name string, qtype uint16, o qopt) *dns.Msg {
	t.Helper()
	r, err := queryErr(server, name, qtype, o)
	if err != nil {
		t.Fatalf("query %s %s: %v", name, dns.TypeToString[qtype], err)
	}
	return r
}

func aValues(m *dns.Msg) []string {
	var out []string
	for _, rr := range m.Answer {
		if a, ok := rr.(*dns.A); ok {
			out = append(out, a.A.String())
		}
	}
	return out
}

func edeCode(m *dns.Msg) (uint16, bool) {
	if opt := m.IsEdns0(); opt != nil {
		for _, o := range opt.Option {
			if e, ok := o.(*dns.EDNS0_EDE); ok {
				return e.InfoCode, true
			}
		}
	}
	return 0, false
}

func eventually(t *testing.T, timeout time.Duration, what string, f func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var err error
	for time.Now().Before(deadline) {
		if err = f(); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s: %v", what, err)
}

func setupRecursion(t *testing.T) (*harness.Env, *harness.Mgmt, *harness.Engine, *harness.Hierarchy) {
	t.Helper()
	env := harness.New(t)
	mg := env.StartMgmt()
	eng := env.StartEngine(mg)
	h := env.StartHierarchy(t)
	h.ConfigureRecursion(t, mg, eng)
	return env, mg, eng, h
}

func wantA(t *testing.T, m *dns.Msg, want string) {
	t.Helper()
	got := aValues(m)
	if m.Rcode != dns.RcodeSuccess || len(got) == 0 || got[len(got)-1] != want {
		t.Fatalf("want A %s, got rcode=%s answers=%v", want, dns.RcodeToString[m.Rcode], got)
	}
}
```

- [ ] Run `scripts/dev-sync.sh && scripts/dev-exec.sh sh -c 'make e2e-build && go test ./e2e/ -run "TestRecursionRootHints|TestSpoofedReplyRejected|TestDNSSECValidation|TestRPZPolicy" -count=3'`; expect PASS three times in a row (no flakes).
- [ ] Run the full suite: `scripts/dev-sync.sh && scripts/dev-exec.sh make e2e`; expect PASS, including the M1/M2 tests (forward mode is still the default).
- [ ] Commit: `git add e2e Makefile mgmt/api/openapi.yaml && git commit -m "test(e2e): recursion, spoofing, DNSSEC validation and RPZ acceptance tests"`.

## Task 15: kw deployment update and kw smoke subtests

Files:

- `deploy/kw/engine.yaml` — engine Deployment: `state` volume becomes `hostPath` `/var/lib/nexora-engine` (`DirectoryOrCreate`) so `trust-anchors.json` and `rpz/` survive pod restarts on each node; image tag bumped to the M3 build.
- `deploy/kw/engine-networkpolicy.yaml` — egress to UDP/TCP 53 on `0.0.0.0/0` and `::/0` for recursion (plus the existing management-plane, OTLP and upstream rules).
- `deploy/kw/mgmt.yaml` — image tag bumped (migrations `0300`–`0302` run through `nexora-mgmt migrate` in the existing init container).
- `e2e/kwsmoke/m3_test.go` — `TestKwSmokeM3` with subtests `recursion`, `dnssec`, `rpz`, `metrics` (build tag `kwsmoke`).

Interfaces:

```go
//go:build kwsmoke
// env: NEXORA_KW_API_URL (https://nexora.kw.local), NEXORA_KW_API_TOKEN (nxt_… operator token),
//      NEXORA_KW_DNS_ADDRS (comma-separated host:port of the engine LoadBalancer services),
//      NEXORA_KW_METRICS_URLS (comma-separated engine /metrics URLs)
func TestKwSmokeM3(t *testing.T)
type kwClient struct{ base, token string; http *http.Client }
func (c *kwClient) do(t *testing.T, method, path string, body, out any, want int)
```

- [ ] Create `e2e/kwsmoke/m3_test.go`:

```go
//go:build kwsmoke

package kwsmoke

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

type kwClient struct {
	base, token string
	http        *http.Client
}

func (c *kwClient) do(t *testing.T, method, path string, body, out any, want int) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status %d want %d: %s", method, path, resp.StatusCode, want, data)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
}

func env(t *testing.T, k string) string {
	v := os.Getenv(k)
	if v == "" {
		t.Skipf("%s not set", k)
	}
	return v
}

func ask(t *testing.T, server, name string, qtype uint16, do bool) *dns.Msg {
	t.Helper()
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), qtype)
	m.SetEdns0(1232, do)
	var last error
	for i := 0; i < 3; i++ {
		r, _, err := (&dns.Client{Timeout: 5 * time.Second}).Exchange(m, server)
		if err == nil {
			return r
		}
		last = err
	}
	t.Fatalf("%s @%s: %v", name, server, last)
	return nil
}

func waitFor(t *testing.T, what string, f func() error) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		if err = f(); err == nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("%s: %v", what, err)
}

func TestKwSmokeM3(t *testing.T) {
	c := &kwClient{base: strings.TrimRight(env(t, "NEXORA_KW_API_URL"), "/"), token: env(t, "NEXORA_KW_API_TOKEN"),
		http: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}}
	servers := strings.Split(env(t, "NEXORA_KW_DNS_ADDRS"), ",")
	metricsURLs := strings.Split(env(t, "NEXORA_KW_METRICS_URLS"), ",")

	var original map[string]any
	c.do(t, "GET", "/api/v1/resolution", nil, &original, 200)
	t.Cleanup(func() {
		var cur map[string]any
		c.do(t, "GET", "/api/v1/resolution", nil, &cur, 200)
		original["revision"] = cur["revision"]
		c.do(t, "PUT", "/api/v1/resolution", original, nil, 200)
	})
	recursive := map[string]any{}
	for k, v := range original {
		recursive[k] = v
	}
	recursive["mode"] = "recursive"
	recursive["root_hints"] = []any{}
	recursive["authority_port"] = 53
	c.do(t, "PUT", "/api/v1/resolution", recursive, nil, 200)

	t.Run("recursion", func(t *testing.T) {
		for _, s := range servers {
			waitFor(t, "recursive answer from "+s, func() error {
				r := ask(t, s, "www.iana.org", dns.TypeA, false)
				if r.Rcode != dns.RcodeSuccess || len(r.Answer) == 0 {
					return fmt.Errorf("rcode %s answers %d", dns.RcodeToString[r.Rcode], len(r.Answer))
				}
				return nil
			})
		}
	})

	t.Run("dnssec", func(t *testing.T) {
		for _, s := range servers {
			if r := ask(t, s, "www.iana.org", dns.TypeA, true); !r.AuthenticatedData {
				t.Fatalf("%s: www.iana.org not AD", s)
			}
			if r := ask(t, s, "dnssec-failed.org", dns.TypeA, true); r.Rcode != dns.RcodeServerFailure {
				t.Fatalf("%s: dnssec-failed.org rcode %s, want SERVFAIL", s, dns.RcodeToString[r.Rcode])
			}
		}
		var st struct {
			Engines []struct {
				EngineName   string `json:"engine_name"`
				TrustAnchors []struct {
					Zone  string `json:"zone"`
					State string `json:"state"`
				} `json:"trust_anchors"`
			} `json:"engines"`
		}
		c.do(t, "GET", "/api/v1/dnssec/status", nil, &st, 200)
		for _, e := range st.Engines {
			valid := false
			for _, a := range e.TrustAnchors {
				valid = valid || (a.Zone == "." && (a.State == "valid" || a.State == "configured"))
			}
			if !valid {
				t.Fatalf("engine %s has no usable root trust anchor: %+v", e.EngineName, e.TrustAnchors)
			}
		}
	})

	t.Run("rpz", func(t *testing.T) {
		for _, s := range servers {
			if r := ask(t, s, "example.org", dns.TypeA, false); r.Rcode != dns.RcodeSuccess {
				t.Fatalf("positive path: example.org rcode %s", dns.RcodeToString[r.Rcode])
			}
		}
		var z struct {
			ID       string `json:"id"`
			Revision int64  `json:"revision"`
		}
		c.do(t, "POST", "/api/v1/rpz-zones", map[string]any{"name": "rpz.kwsmoke.nexora.", "source_type": "file", "policy_override": "given", "min_refresh_seconds": 60}, &z, 201)
		t.Cleanup(func() { c.do(t, "DELETE", "/api/v1/rpz-zones/"+z.ID, nil, nil, 204) })
		zone := "$TTL 60\n@ SOA ns.rpz.kwsmoke.nexora. h.rpz.kwsmoke.nexora. 1 60 60 86400 60\n@ NS ns.rpz.kwsmoke.nexora.\nexample.org CNAME .\n"
		c.do(t, "PUT", "/api/v1/rpz-zones/"+z.ID+"/file", map[string]any{"content": zone, "revision": z.Revision}, nil, 200)
		for _, s := range servers {
			waitFor(t, "rpz applied on "+s, func() error {
				if r := ask(t, s, "example.org", dns.TypeA, false); r.Rcode != dns.RcodeNameError {
					return fmt.Errorf("rcode %s", dns.RcodeToString[r.Rcode])
				}
				return nil
			})
		}
	})

	t.Run("metrics", func(t *testing.T) {
		for _, u := range metricsURLs {
			resp, err := http.Get(u)
			if err != nil {
				t.Fatalf("%s: %v", u, err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			for _, name := range []string{"nexora_resolutions_total", "nexora_recursor_upstream_queries_total", "nexora_dnssec_validations_total", "nexora_rpz_zone_serial", "nexora_dnssec_trust_anchor_keys"} {
				if !bytes.Contains(body, []byte(name)) {
					t.Fatalf("%s lacks %s", u, name)
				}
			}
		}
	})
}
```

- [ ] Run against the currently deployed (M2) build to prove the smoke test detects the missing feature: `NEXORA_KW_API_URL=https://nexora.kw.local NEXORA_KW_API_TOKEN=$(cat ~/.config/nexora/kw-operator-token) NEXORA_KW_DNS_ADDRS=$(kubectl --context kw -n nexora get svc -l app=nexora-engine -o jsonpath='{range .items[*]}{.status.loadBalancer.ingress[0].ip}:53,{end}' | sed 's/,$//') NEXORA_KW_METRICS_URLS=$(kubectl --context kw -n nexora get svc -l app=nexora-engine -o jsonpath='{range .items[*]}http://{.status.loadBalancer.ingress[0].ip}:9153/metrics,{end}' | sed 's/,$//') go test -tags kwsmoke ./e2e/kwsmoke/ -run TestKwSmokeM3 -v`; expect FAIL with "GET /api/v1/resolution: status 404".
- [ ] Build and push images: `scripts/build-image.sh -f deploy/docker/engine.Dockerfile -n nexora-engine -t m3-$(git rev-parse --short HEAD)` and `scripts/build-image.sh -f deploy/docker/mgmt.Dockerfile -n nexora-mgmt -t m3-$(git rev-parse --short HEAD)`; expect `pull as 192.168.10.131/azrtydxb/nexora-engine:m3-<sha>` and the same for `nexora-mgmt`.
- [ ] Edit `deploy/kw/engine.yaml`: image `192.168.10.131/azrtydxb/nexora-engine:m3-<sha>`; replace the `state` volume with

```yaml
volumes:
  - name: state
    hostPath:
      path: /var/lib/nexora-engine
      type: DirectoryOrCreate
```

and edit `deploy/kw/mgmt.yaml` image to `nexora-mgmt:m3-<sha>`.

- [ ] Create or extend `deploy/kw/engine-networkpolicy.yaml` with the recursion egress rule:

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: nexora-engine-recursion-egress
  namespace: nexora
spec:
  podSelector:
    matchLabels:
      app: nexora-engine
  policyTypes: [Egress]
  egress:
    - to:
        - ipBlock: { cidr: 0.0.0.0/0 }
        - ipBlock: { cidr: ::/0 }
      ports:
        - { protocol: UDP, port: 53 }
        - { protocol: TCP, port: 53 }
```

- [ ] Apply: `kubectl --context kw -n nexora apply -f deploy/kw/engine-networkpolicy.yaml -f deploy/kw/mgmt.yaml -f deploy/kw/engine.yaml && kubectl --context kw -n nexora rollout status deploy/nexora-mgmt --timeout=300s && kubectl --context kw -n nexora rollout status deploy/nexora-engine --timeout=300s`; expect both rollouts "successfully rolled out".
- [ ] Re-run the smoke command from the first step; expect PASS for `TestKwSmokeM3/recursion`, `/dnssec`, `/rpz`, `/metrics`, and the resolution settings restored by cleanup.
- [ ] Commit: `git add deploy/kw e2e/kwsmoke && git commit -m "deploy(kw): M3 images, recursion egress, persistent engine state and smoke subtests"`.
