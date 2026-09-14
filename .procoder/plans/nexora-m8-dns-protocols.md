# nexora-m8-dns-protocols — implementation plan

Status: draft
Spec: .procoder/specs/nexora-m8-dns-protocols.md

## Goal

Ship milestone M8 DNS protocols (GitHub issues #30–#33): an mDNS gateway with cross-interface
reflection, ZONEMD generation and verification, Oblivious DoH target and proxy roles, and catalog zone
producer and consumer. The work is cut into file-disjoint tasks that parallel implementer agents build
wave by wave, each proven by tests that fail when the RFC behaviour breaks.

## Architecture

Three contract tasks come first and freeze every shared name:

- proto fields 900–999, module declarations, RFC 8976 test vectors and the architecture text
  (Task 1);
- the OpenAPI operations, schema fields, permissions, generated clients and 501 stubs (Task 2);
- the migrations and the Go model fields that read them (Task 3).

Each protocol then splits into a pure core (wave 1), its integration (wave 2–3), end-to-end proof
(wave 3–4) and GUI (wave 4):

- **ZONEMD:** Go `mgmt/internal/zonemd` (4) and Rust `engine/src/zonemd.rs` (5) implement one RFC
  8976 digest, both pinned to the same RFC Appendix A vectors. The management plane generates on
  rebuild (12) and verifies secondary transfers (13); the engine verifies RPZ transfers (20).
- **Catalog zones:** a codec (8), a service that regenerates producer catalogs inside zone
  transactions and reconciles consumer catalogs after refreshes (15), the API and wiring (22).
- **ODoH:** the engine codec and proxy (7) are wired into the DoH listener (19); the management plane
  keeps settings and rotating sealed seeds (9) and pushes keys on the control stream (16).
- **mDNS:** the engine gateway (6) and reflector (18) are wired into the resolution route and
  snapshot (21); group settings come from the management plane (17). The multicast proof runs in a
  user and network namespace lab (10, 26).

### Decisions not settled by the spec (made here, binding for the tasks)

- **Proto:**
  - numbers as in the spec Interfaces block;
  - `ZONEMD_VERIFY_UNSPECIFIED` and `ZONEMD_VERIFY_OFF` both mean off in the engine;
  - the management plane always sends an explicit value for transfer RPZ zones;
  - it sets `ConfigSnapshot.mdns` only when `enabled || reflect`, and `ConfigSnapshot.odoh` only when
    `target_enabled || proxy_enabled`.
- **Module skeletons:** Task 1 declares `pub mod zonemd;` and `pub mod mdns;` in
  `engine/src/lib.rs` and `pub mod odoh;` in `engine/src/server/mod.rs`. It creates
  `engine/src/zonemd.rs`, `engine/src/server/odoh.rs`, `engine/src/mdns/mod.rs` (declaring
  `pub mod gateway; pub mod iface; pub mod reflector;`) and the three files, each holding only its
  module doc comment, so wave 1 tasks own disjoint files.
- **RFC 8976 vectors:**
  - `e2e/testdata/rfc8976/{a1,a2,a3}.zone` are Appendix A.1–A.3 verbatim, with a first line
    `$ORIGIN example.`;
  - `e2e/testdata/rfc8976/{a1,a2,a3}.hex` hold one RR per line, the lowercase hex of the
    uncompressed wire form as parsed (`dns.PackRR(rr, buf, 0, nil, false)`), in file order;
  - `e2e/testdata/rfc8976/genhex/main.go` regenerates them, so Rust tests need no zone-file parser
    for ZONEMD.
- **Canonical form (both implementations):**
  - owner lowercased;
  - RDATA names lowercased for NS, MD, MF, CNAME, SOA, MB, MG, MR, PTR, MINFO, MX, RP, AFSDB, RT, SIG,
    PX, NXT, NAPTR, KX, SRV, DNAME, A6 and RRSIG (RFC 4034 §6.2 as corrected by RFC 6840 §5.1: not
    NSEC and not HINFO);
  - TTL as in the zone;
  - sort by RFC 4034 §6.1 owner order, then type, then RDATA octets; equal (owner, class, type,
    RDATA) once;
  - excluded: owner outside the origin, apex ZONEMD, and apex RRSIG whose type covered is ZONEMD.
- **ZONEMD in rebuilds:** `zone.Rebuild` order for `zonemd_generate` zones:
  1. `desiredRRs` plus `zonemd.Placeholder(origin, 0, soaTTL)` (48 zero octets);
  2. `Signer.Sign` (signed zones);
  3. `setSOASerial` and `Signer.ResignSOA` as today;
  4. `zonemd.Apply(origin, desired)` sets the apex ZONEMD serial to the SOA serial and its digest;
  5. `Signer.SignZONEMD` (signed zones) replaces the RRSIGs covering the apex ZONEMD.

  `isSOAData` also matches apex ZONEMD and the RRSIGs covering it, so the diff (`diffIgnoringSOA`)
  ignores the placeholder and the digest, an unchanged zone publishes nothing, and every delta carries
  the old ZONEMD in its deleted SOA section and the new one in its added SOA section (`soaWithSigs`).
  A change of `zonemd_generate` forces a rebuild (`RebuildOptions.Force`). The zone status columns are written with
  `zone.SetZonemdStatus(ctx, q, zoneID, status, errText)`.

- **Secondary verification:** `xfrin.Refresher.succeed` computes the resulting RR set before calling
  `Zones.Mutate`:
  - AXFR: the transferred RRs;
  - IXFR: `zone.LoadServed` plus the diffs applied in memory by `applyDiffs(current, diffs)`.

  It calls `zonemd.Verify(z.Name, set, zonemd.Mode(z.ZonemdVerify))`. On `failed` it returns
  `zonemdError{reason}` (message `zonemd: <reason>`), which `Refresh` records through `fail` as for
  any primary error, and writes `zonemd_status=failed`. On success the status is written inside the
  `Mutate` transaction.

- **mDNS engine:**
  - `mdns::iface::lookup` reads `getifaddrs` (nix `ifaddrs`) and `if_nametoindex`.
  - The gateway opens, per query and per interface family, one socket2 UDP socket bound to the
    interface address with port 0, `IP_MULTICAST_IF` (v6: the interface index), multicast TTL 255,
    and multicast loop on (so a responder on the same host answers).
  - Replies are read with `tokio::net::UdpSocket` until the deadline or first unique answer.
  - The in-flight cap is a static `AtomicUsize` with a guard.
  - Counters are the static `mdns::gateway::COUNTERS`.
  - The reflector uses one socket per interface and family, bound to `0.0.0.0:5353` / `[::]:5353` with
    `SO_REUSEADDR`, `SO_REUSEPORT` and `SO_BINDTODEVICE`, joined to the group on that interface's
    index, loop off, TTL 255. It runs as tokio tasks on the control runtime handle and is restarted
    by `mdns::MdnsState::sync` only when the reflect interface list changes.
- **Route:** `recursor::dispatch::ResolutionRuntime` gains `mdns: Option<Arc<mdns::gateway::Gateway>>`.
  `route()` returns `Route::Mdns` after the forward-zone match fails and the lowercase wire name ends
  in `\x05local\x00`. `resolve_miss` answers `Route::Mdns` through `mdns::answer(q, outcome)`. A
  `Busy` outcome is SERVFAIL, not cached, and not served stale.
- **ODoH engine:**
  - `server::odoh::Keyring` derives key pairs with
    `ObliviousDoHKeyPair::from_parameters(0x0020, 0x0001, 0x0001, &seed)` and indexes them by
    `public().identifier()`.
  - `Shared.odoh: server::odoh::OdohState` holds `ArcSwap<Keyring>`.
  - `Runtime.odoh: Arc<server::odoh::OdohRuntime>` holds `target_enabled` and
    `proxy: Option<ProxyConfig>`, built from the snapshot.
  - `doh::handle` gains the `&OdohRuntime` and `&OdohState` parameters.
  - Response padding is 0.
  - The proxy uses one `reqwest::Client` per target (HTTP/2, rustls, the target CA or webpki roots,
    `NEXORA_DOH_RESOLVE` pins as in `upstream::doh`), built when the runtime is built.
- **ODoH management plane:**
  - seeds are 32 bytes from `crypto/rand`, sealed with `Box.Seal("odoh-seed", seed)`;
  - `publish_after = created_at + 5 min`, `not_after = created_at + 2 × key_rotation_hours`;
  - `Keys.Run` ticks every 60 s: when `target_enabled` and no key is newer than `key_rotation_hours`,
    it rotates under `pg_try_advisory_xact_lock(hashtext('nexora:odoh-rotate'))`, deletes keys past
    `not_after`, and runs `pg_notify('nexora_odoh_keys', '')`;
  - the hub LISTENs on `nexora_odoh_keys` and re-offers the key set to every subscriber, with digest
    suppression as for RPZ keys.
- **Catalog:**
  - member label `strings.ReplaceAll(zoneID.String(), "-", "")`;
  - generated record TTL 0;
  - `catalog_zones.processed_serial` holds the serial last processed;
  - consumer member zones copy the catalog zone's `primaries` JSON (TSIG key ids included) and
    `engine_group_id`;
  - consumer actions run as actor `system:catzone`, audit action `reconcileCatalogZone` (one row per
    processing, listing created, deleted and recreated names).
- **Catalog hook:** `zone.Service` gains
  `CatalogChanged func(ctx context.Context, tx pgx.Tx, catalogZoneIDs []uuid.UUID) error`, called
  from `CreateZone`, `UpdateZone` (when `catalog_zone_id` changes: old and new ids) and `DeleteZone`
  (before the delete) inside their transactions. `catzone.Service.Regenerate` implements it, and
  `mgmt/cmd/nexora-mgmt/main.go` wires it.
- **Consumer trigger:** `xfrin.Scheduler` gains `AfterRefresh func(ctx context.Context, zoneID uuid.UUID)`,
  called after `Refresher.Refresh` returns nil. `catzone.Service.Reconcile` ignores zones without a
  consumer row.
- **GUI:**
  - the ZONEMD card and catalog select live in `web/src/pages/ZoneZonemdCard.tsx`, rendered by
    `ZoneDetailPage.tsx` above the tabs;
  - the catalog page is a child of the Zones navigation item;
  - help entries go in `zones.ts` (card), a new `catalogzones.ts` area, the area whose `pages` lists
    `pages/SettingsPage.tsx` (ODoH), `fleet.ts` (mDNS) and `filtering.ts` (RPZ).

### Wave order and file ownership

A task may start when every task in earlier waves is committed. Tasks in one wave never edit the same
file; each task's `Files:` line is its exclusive ownership for its wave.

| Wave | Tasks (parallel)               | Files serialised by the wave                                                                                                                                                                                                                                                                                                                                                                                                                                                                   |
| ---- | ------------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 0    | 1, 2, 3                        | `control.proto`, `gen/go`, `docs/architecture.md`, `engine/src/lib.rs`, `engine/src/server/mod.rs`, `engine/src/control.rs` (1); `openapi.yaml`, `gen.go`, `schema.d.ts`, both permission maps, `api/server.go` (2); migrations, `zone/model.go`, `zone/service.go`, `fleet/groups.go`, `store/resolution.go` (3)                                                                                                                                                                              |
| 1    | 4, 5, 6, 7, 8, 9, 10, 11       | `mgmt/internal/zonemd` (4); `engine/src/zonemd.rs` (5); `engine/src/mdns/{gateway,iface}.rs` (6); `engine/src/server/odoh.rs`, `engine/Cargo.toml`, `Cargo.lock` (7); `mgmt/internal/catzone/codec*.go` (8); `mgmt/internal/odoh` (9); `e2e/harness/netlab*.go`, fixture `mdns.go`, fixture `main.go` (10); `e2e/harness/odoh*.go`, `go.mod`, `go.sum` (11)                                                                                                                                    |
| 2    | 12, 13, 14, 15, 16, 18, 19, 20 | `zone/build.go`, `dnssec/store.go` (12); `xfrin/refresh.go`, `zone/zonemd_status.go` (13); `zone/service.go`, `api/zones.go`, `api/rpz.go`, `store/resolution.go`, `snapshot/resolution.go`, `stats/resolution.go` (14); `catzone/service.go`, `xfrin/scheduler.go` (15); `api/odoh.go`, `control/hub.go`, `snapshot/odoh.go` (16); `engine/src/mdns/reflector.rs` (18); `server/doh.rs`, `server/mod.rs`, `control.rs`, `snapshot.rs`, `runtime.rs`, `metrics.rs` (19); `rpz/manager.rs` (20) |
| 3    | 17, 21, 22, 23                 | `api/fleet_groups.go`, `fleet/groups.go`, `snapshot/snapshot.go` (17); `mdns/mod.rs`, `recursor/dispatch.rs`, `snapshot.rs`, `runtime.rs`, `metrics.rs`, `server/mod.rs`, `hot_path_alloc.rs` (21); `api/catalog_zones.go`, `cmd/nexora-mgmt/main.go` (22); `e2e/zonemd_test.go`, `e2e/rpz_zonemd_test.go` (23)                                                                                                                                                                                |
| 4    | 24, 25, 26, 27, 28, 29, 30, 31 | `e2e/odoh_test.go`, fixture `dns.go` (24); `e2e/catalog_zones_test.go`, `harness/named.go` (25); `e2e/mdns_test.go` (26); `ZoneDetailPage.tsx`, `zones.ts` (27); `router.tsx`, `AppShell.tsx`, `help/catalog/index.ts` (28); `SettingsPage.tsx` (29); `EngineGroupPage.tsx`, `fleet.ts` (30); `RpzPage.tsx`, `filtering.ts` (31)                                                                                                                                                               |
| 5    | 32, 33                         | `docs/operations.md`, `web/src/help/topics/*.md` (32); none (33)                                                                                                                                                                                                                                                                                                                                                                                                                               |
| 6    | 34                             | `e2e/kw_smoke_m8_test.go`, `scripts/kw-acceptance.sh`                                                                                                                                                                                                                                                                                                                                                                                                                                          |

Dependencies beyond the wave rule (a task names a later interface only through its `Interfaces:`
line):

- 4 and 5 consume Task 1's vectors.
- 6 and 7 consume Task 1's module skeletons.
- 12 and 13 consume 3 and 4.
- 14 consumes 2 and 3.
- 15 consumes 3 and 8; its `Regenerate` matches the hook signature Task 14 declares, wired by 22.
- 16 consumes 1, 2 and 9.
- 17 consumes 2, 3 and 16's `snapshot.go` edit.
- 18 consumes 6.
- 19 consumes 1 and 7.
- 20 consumes 1 and 5.
- 21 consumes 6, 18 and 19.
- 22 consumes 2, 9, 14, 15 and 16.
- 23 consumes 12, 13, 14 and 20.
- 24 consumes 11, 16, 19 and 22.
- 25 consumes 22.
- 26 consumes 10, 17 and 21.
- 27–31 consume 2 and their backends.
- 32 and 33 consume everything before them.
- 34 consumes everything.

### Spec coverage

| Spec item                          | Tasks                        |
| ---------------------------------- | ---------------------------- |
| S-1 mDNS gateway                   | 1, 3, 6, 17, 21, 26, 30      |
| S-2 mDNS reflection                | 1, 18, 21, 26, 30            |
| S-3 multicast proof without kw     | 10, 26, 32                   |
| S-4 ZONEMD generation              | 1, 3, 4, 12, 14, 23, 27      |
| S-5 ZONEMD secondary verification  | 1, 3, 4, 13, 14, 23, 27      |
| S-6 ZONEMD RPZ verification        | 1, 3, 5, 14, 20, 23, 31      |
| S-7 ODoH target                    | 1, 7, 19, 24, 29             |
| S-8 ODoH keys                      | 1, 3, 9, 16, 19, 22, 24, 29  |
| S-9 ODoH proxy                     | 1, 7, 19, 24, 29             |
| S-10 catalog producer              | 3, 8, 14, 15, 22, 25, 27, 28 |
| S-11 catalog consumer              | 3, 8, 15, 22, 25, 28         |
| S-12 API, permissions, GUI         | 2, 14, 16, 17, 22, 27–31, 33 |
| S-13 contract and data numbering   | 1, 2, 3                      |
| S-14 operations guide and kw proof | 32, 34                       |

## Constraints

Copied from the spec (binding for every task):

- "Hot path rules from `docs/architecture.md` stay binding. There is no logging, allocation or lock
  held across packets on the cache-hit path."
  - "The mDNS route is chosen only on the cache-miss path."
  - "ODoH and the proxy run on their own HTTP request paths."
  - "ZONEMD verification of RPZ zones runs on the control runtime."
  - "`cache_hit_path_does_not_allocate` and `authoritative_answer_path_does_not_allocate` keep
    passing, with the mDNS gateway and the ODoH target enabled in the tested snapshot."
- Rolling upgrades:
  - "an engine without M8 ignores the new snapshot fields and the `odoh_keys` message";
  - "an M8 engine treats `ZONEMD_VERIFY_UNSPECIFIED` (older management plane) as off and an absent
    `mdns` or `odoh` config as disabled";
  - "the M8 management plane sets `mdns` only while the gateway or reflection is on and `odoh` only
    while a role is on".
- "Migrations keep existing behaviour": no ZONEMD generation, no mDNS, ODoH off, `if_present` for
  existing secondaries and RPZ transfer zones.
- Secrets: "ODoH seeds are sealed under the KEK and travel only on the mTLS control stream"; "nothing
  secret reaches audit rows, logs or snapshots".
- kw:
  - deploy only after every test passes, through `scripts/kw-deploy.sh` with the DNS probe;
  - never change 192.168.10.136 or 192.168.10.139;
  - `helm rollback nexora` on any lost query or failed acceptance;
  - kw acceptance creates only scratch objects and deletes them.
- "Tests that open multicast sockets run only inside the network namespace lab, never on the pod's
  cluster interface." Rust unit tests use unicast fakes on 127.0.0.1.
- Dependencies: Rust `odoh-rs = "=1.0.5"`; Go `github.com/cloudflare/circl v1.6.5` (e2e harness
  only); no new management plane dependency.
- Numbering: proto 900–999; migrations `00900_zonemd.sql`, `00901_catalog_zones.sql`,
  `00902_odoh.sql`, `00903_engine_group_mdns.sql`; Playwright `40-zonemd`, `41-catalog-zones`,
  `42-odoh`, `43-mdns`, `44-rpz-zonemd`. If M6 or M7 as built already took a number, use the next free
  one and record it in `docs/architecture.md`.
- "M8 builds on M6 and M7 as committed. When they changed a file named here, the task adapts to the
  committed code and updates its plan text." Every task's first step re-reads its files at the M7
  head.
- Out of scope:
  - an mDNS responder, service filtering and reverse zones;
  - DNSSEC validation inside ZONEMD verification, ZONEMD for RPZ file uploads, and SHA-512
    generation;
  - the ODoH client role, OHTTP, and standalone ODoH;
  - catalog `coo`, `group` and `ext`.

Project rules (from `.procoder/notes/implementer-brief.md` and `docs/architecture.md`):

- Edit on the laptop. Build and test in the dev pod with `scripts/dev-exec.sh '<cmd>'`. Parallel
  worktrees use `NEXORA_DEV_DEPLOY`.
- Generated sources:
  - `gen/go/nexora/control/v1/*.pb.go` are regenerated in the dev pod and copied back, as M6 Task 1
    did;
  - `mgmt/internal/api/gen.go` (`cd mgmt/api && oapi-codegen -config oapi-codegen.yaml openapi.yaml`)
    and `web/src/api/schema.d.ts` (`cd web && pnpm run gen:api`) are generated on the laptop.
- TDD: write the failing test, run it, see the stated failure, implement, see it pass. Never weaken or
  delete a test.
- Formatters and linters on changed files:
  - `cargo fmt`
  - `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings`
  - `gofmt -l`, `go vet ./...`
  - `cd web && pnpm run typecheck && pnpm run lint`
  - `scripts/pc-format.sh <md files>`
- Do NOT commit, `git add`, `reset` or `stash`. List every changed path in the final report; the lead
  commits with `scripts/commit-paths.sh`. A "report" step means exactly that.
- Every test that asserts "does not happen" first asserts the positive path in the same run.
- Test helpers: the literal tests name helpers (`hasA`, `exchange`, `newAPITestServer`,
  `auditHasNoSecret`, ...). Several M8 test files share one Go package, and tasks of one wave cannot
  see each other's files. Before writing a helper, run `rg -n 'func <name>\b' <package dir>` and reuse
  an existing one. A helper that does not exist yet is written in the task's own test file with the
  file's prefix (`zmd`, `odoh`, `cat`, `mdns`, `rpzZmd`), and the test's call sites use that name.
- Mark deliberate ceilings with `debt:` comments naming the ceiling and the revisit condition.
- Fixed identifiers:
  - Error code `catalog_managed`.
  - Audit actions: the seven operationIds plus `reconcileCatalogZone` and `rotateOdohKeyScheduled`.
  - Metrics: `nexora_mdns_queries_total{result}`, `nexora_mdns_interface_missing{interface}`,
    `nexora_mdns_reflected_packets_total{from,to}`, `nexora_odoh_requests_total{role,status}`.
  - Channel `nexora_odoh_keys`. Advisory locks `nexora:odoh-rotate` and `catalog:<catalog zone id>`.
  - Test ids as in the spec Interfaces.

## Task 1: Contract fields 900–999, module skeletons, RFC 8976 vectors, architecture text

Files:

- `proto/nexora/control/v1/control.proto`: the M8 fields, enums and messages.
- `gen/go/nexora/control/v1/control.pb.go`, `gen/go/nexora/control/v1/control_grpc.pb.go`:
  regenerated.
- `mgmt/internal/control/contract_m8_test.go`: created.
- `engine/src/lib.rs`: `pub mod mdns;` and `pub mod zonemd;`.
- `engine/src/server/mod.rs`: only `pub mod odoh;` in the module list.
- `engine/src/zonemd.rs`, `engine/src/server/odoh.rs`, `engine/src/mdns/mod.rs`,
  `engine/src/mdns/gateway.rs`, `engine/src/mdns/iface.rs`, `engine/src/mdns/reflector.rs`: created,
  each with its module doc comment only (`mod.rs` also declares the three submodules).
- `engine/src/control.rs`: only the `ServerMessage` match arm for `OdohKeys`.
- `e2e/testdata/rfc8976/a1.zone`, `a2.zone`, `a3.zone`, `a1.hex`, `a2.hex`, `a3.hex`,
  `e2e/testdata/rfc8976/genhex/main.go`: created.
- `docs/architecture.md`: the settled M8 design.

Interfaces: produces the proto names used by Tasks 7, 9, 14, 16, 17, 19, 20, 21 (Rust
`nexora_engine::proto::*`, Go `controlv1.*`):

```proto
// M8 DNS protocols: fields added to existing messages use 900-999.
// ConfigSnapshot
MdnsConfig mdns = 900; // M8: absent = mDNS gateway and reflector off
OdohConfig odoh = 901; // M8: absent = ODoH target and proxy off
// RpzTransferSource
ZonemdVerify zonemd_verify = 900; // M8: UNSPECIFIED = off
// RpzZoneStatus
ZonemdStatus zonemd = 900;  // M8
string zonemd_error = 901;  // M8
// ServerMessage oneof
OdohKeys odoh_keys = 900; // M8: never persisted, never part of ConfigSnapshot

enum ZonemdVerify {
  ZONEMD_VERIFY_UNSPECIFIED = 0;
  ZONEMD_VERIFY_OFF = 1;
  ZONEMD_VERIFY_IF_PRESENT = 2;
  ZONEMD_VERIFY_REQUIRED = 3;
}
enum ZonemdStatus {
  ZONEMD_STATUS_UNSPECIFIED = 0;
  ZONEMD_STATUS_OFF = 1;
  ZONEMD_STATUS_ABSENT = 2;
  ZONEMD_STATUS_VERIFIED = 3;
  ZONEMD_STATUS_FAILED = 4;
}
message MdnsConfig {
  bool enabled = 1;
  repeated string interfaces = 2;         // interface names, 1-15 chars
  uint32 timeout_ms = 3;                  // 0 -> 500; else 100..=5000
  bool reflect = 4;
  repeated string reflect_interfaces = 5; // at least 2 when reflect
}
message OdohConfig {
  bool target_enabled = 1;
  bool proxy_enabled = 2;
  repeated OdohProxyTarget proxy_targets = 3; // at least 1 when proxy_enabled
  uint32 proxy_timeout_ms = 4;                // 0 -> 2000; else 100..=10000
}
message OdohProxyTarget {
  string host = 1;   // "name" or "name:port" (IPv6 in brackets); no port = 443
  string ca_pem = 2; // empty = webpki roots
}
message OdohKeys {
  repeated OdohKey keys = 1; // the complete set, newest first
}
message OdohKey {
  bytes seed = 1;               // 32 octets; HPKE DeriveKeyPair input
  int64 publish_after_unix = 2; // listed in /.well-known/odohconfigs from then on
  int64 not_after_unix = 3;     // accepted until then
}
```

- [ ] Create `mgmt/internal/control/contract_m8_test.go`:
  ```go
  package control_test

  import (
  	"testing"

  	"google.golang.org/protobuf/proto"
  	"google.golang.org/protobuf/reflect/protoreflect"

  	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
  )

  // M8 fields use 900-999 and survive a round trip; a renumbering or a missing field fails here.
  func TestContractM8FieldsRoundTrip(t *testing.T) {
  	snap := &controlv1.ConfigSnapshot{
  		Mdns: &controlv1.MdnsConfig{Enabled: true, Interfaces: []string{"gw0"}, TimeoutMs: 500, Reflect: true, ReflectInterfaces: []string{"gwA", "gwB"}},
  		Odoh: &controlv1.OdohConfig{TargetEnabled: true, ProxyEnabled: true, ProxyTimeoutMs: 2000,
  			ProxyTargets: []*controlv1.OdohProxyTarget{{Host: "odoh.example:8443", CaPem: "pem"}}},
  		RpzZones: []*controlv1.RpzZone{{Id: "z", Name: "rpz.test.", Source: &controlv1.RpzZone_Transfer{
  			Transfer: &controlv1.RpzTransferSource{Primary: "127.0.0.1:53", ZonemdVerify: controlv1.ZonemdVerify_ZONEMD_VERIFY_REQUIRED}}}},
  	}
  	stats := &controlv1.Stats{RpzZones: []*controlv1.RpzZoneStatus{{Id: "z", Zonemd: controlv1.ZonemdStatus_ZONEMD_STATUS_FAILED, ZonemdError: "digest mismatch"}}}
  	keys := &controlv1.ServerMessage{Msg: &controlv1.ServerMessage_OdohKeys{OdohKeys: &controlv1.OdohKeys{
  		Keys: []*controlv1.OdohKey{{Seed: make([]byte, 32), PublishAfterUnix: 1, NotAfterUnix: 2}}}}}
  	for _, m := range []proto.Message{snap, stats, keys} {
  		b, err := proto.Marshal(m)
  		if err != nil {
  			t.Fatal(err)
  		}
  		back := m.ProtoReflect().New().Interface()
  		if err := proto.Unmarshal(b, back); err != nil || !proto.Equal(m, back) {
  			t.Fatalf("round trip of %T: %v", m, err)
  		}
  	}
  	for _, c := range []struct {
  		msg   proto.Message
  		field string
  		num   protoreflect.FieldNumber
  	}{
  		{snap, "mdns", 900}, {snap, "odoh", 901},
  		{&controlv1.RpzTransferSource{}, "zonemd_verify", 900},
  		{&controlv1.RpzZoneStatus{}, "zonemd", 900}, {&controlv1.RpzZoneStatus{}, "zonemd_error", 901},
  		{keys, "odoh_keys", 900},
  	} {
  		f := c.msg.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(c.field))
  		if f == nil || f.Number() != c.num {
  			t.Fatalf("%s.%s must be field %d", c.msg.ProtoReflect().Descriptor().Name(), c.field, c.num)
  		}
  	}
  }

  // ODoH seeds must never become part of a persisted snapshot.
  func TestSnapshotCannotReachOdohKeys(t *testing.T) {
  	seen := map[protoreflect.FullName]bool{}
  	var walk func(md protoreflect.MessageDescriptor)
  	walk = func(md protoreflect.MessageDescriptor) {
  		if seen[md.FullName()] {
  			return
  		}
  		seen[md.FullName()] = true
  		if md.FullName() == "nexora.control.v1.OdohKeys" || md.FullName() == "nexora.control.v1.OdohKey" {
  			t.Fatalf("ConfigSnapshot reaches %s", md.FullName())
  		}
  		fields := md.Fields()
  		for i := 0; i < fields.Len(); i++ {
  			if m := fields.Get(i).Message(); m != nil {
  				walk(m)
  			}
  		}
  	}
  	walk((&controlv1.ConfigSnapshot{}).ProtoReflect().Descriptor())
  	if !seen["nexora.control.v1.MdnsConfig"] || !seen["nexora.control.v1.OdohConfig"] {
  		t.Fatal("the walk did not reach the M8 snapshot messages")
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/control -run "TestContractM8|TestSnapshotCannotReachOdohKeys" -count=1'`.
      Expect FAIL: it does not compile, with `unknown field Mdns in struct literal`.
- [ ] Edit `control.proto`:
  - add every field, enum and message of the Interfaces block to the named messages, fields with a
    trailing `// M8` comment;
  - put the comment block `// M8 DNS protocols: fields added to existing messages use 900-999.`
    above the new messages.
- [ ] Regenerate in the dev pod and copy back:
      `scripts/dev-exec.sh 'protoc -I proto --go_out=gen/go --go_opt=paths=source_relative --go-grpc_out=gen/go --go-grpc_opt=paths=source_relative proto/nexora/control/v1/control.proto'`,
      then `kubectl --context kw -n nexora-dev exec deploy/toolbox -c toolbox -- tar -C /work/nexora -cf - gen/go | tar -xf -`.
- [ ] Create the engine skeletons:
  - `engine/src/zonemd.rs`: `//! RFC 8976 zone digests (ZONEMD): digest and verify.`
  - `engine/src/server/odoh.rs`: `//! RFC 9230 Oblivious DoH target and proxy.`
  - `engine/src/mdns/mod.rs`: `//! mDNS (RFC 6762) gateway and reflector.` followed by
    `pub mod gateway;`, `pub mod iface;`, `pub mod reflector;`.
  - `engine/src/mdns/gateway.rs`: `//! One-shot multicast queries answering unicast DNS clients (RFC 6762 §5.1, §6.7).`
  - `engine/src/mdns/iface.rs`: `//! Interface lookup by name for multicast sockets.`
  - `engine/src/mdns/reflector.rs`: `//! Multicast mDNS reflection between interfaces.`
  - Add `pub mod mdns;` and `pub mod zonemd;` to `engine/src/lib.rs` (alphabetical), and `pub mod odoh;`
    to the module list in `engine/src/server/mod.rs`.
- [ ] In `engine/src/control.rs`, extend the no-op arm to
      `Some(ServerMsg::LogRequest(_)) | Some(ServerMsg::OdohKeys(_)) | None => {}` (keep the arm M6 left
      for `LogRequest` if Task 13 of M6 already replaced it: then add a separate
      `Some(ServerMsg::OdohKeys(_)) => {}` arm). Add the comment
      `// debt: ODoH keys are ignored until M8 Task 19 wires server::odoh::OdohState.`
- [ ] Create `e2e/testdata/rfc8976/a1.zone`, `a2.zone` and `a3.zone`:
  - the first line of each is `$ORIGIN example.`;
  - then the zone text of RFC 8976 Appendix A.1, A.2 and A.3 verbatim, from
    `https://www.rfc-editor.org/rfc/rfc8976.txt` (the indented zone lines only, without the section
    prose).
- [ ] Create `e2e/testdata/rfc8976/genhex/main.go`:
  ```go
  // Command genhex writes <zone>.hex next to each RFC 8976 vector: one RR per line, the lowercase
  // hex of its uncompressed wire form, in file order. Rust tests read these files.
  package main

  import (
  	"encoding/hex"
  	"fmt"
  	"os"
  	"path/filepath"
  	"strings"

  	"github.com/miekg/dns"
  )

  func main() {
  	dir := os.Args[1]
  	for _, name := range []string{"a1", "a2", "a3"} {
  		src, err := os.ReadFile(filepath.Join(dir, name+".zone"))
  		if err != nil {
  			panic(err)
  		}
  		zp := dns.NewZoneParser(strings.NewReader(string(src)), "example.", name+".zone")
  		var lines []string
  		for rr, ok := zp.Next(); ok; rr, ok = zp.Next() {
  			buf := make([]byte, 65535)
  			n, err := dns.PackRR(rr, buf, 0, nil, false)
  			if err != nil {
  				panic(err)
  			}
  			lines = append(lines, hex.EncodeToString(buf[:n]))
  		}
  		if err := zp.Err(); err != nil {
  			panic(err)
  		}
  		if err := os.WriteFile(filepath.Join(dir, name+".hex"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
  			panic(err)
  		}
  		fmt.Println(name, len(lines), "records")
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'go run ./e2e/testdata/rfc8976/genhex e2e/testdata/rfc8976'` and
      expect `a1 6 records`, `a2 21 records` and `a3 10 records`. Copy the three `.hex` files back from
      the pod with the `kubectl ... tar` command above, using the path `e2e/testdata/rfc8976`.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/control -run "TestContractM8|TestSnapshotCannotReachOdohKeys" -count=1 && cargo build --locked -p nexora-engine --all-targets'`
      and expect PASS and a clean build.
- [ ] Edit `docs/architecture.md`:
  - In the repository layout, add:
    - `src/zonemd.rs  (M8) RFC 8976 digest and verify`
    - `src/mdns/{mod,iface,gateway,reflector}.rs  (M8) mDNS gateway and reflector`
    - `src/server/odoh.rs  (M8) Oblivious DoH target and proxy`
    - `internal/zonemd, internal/catzone, internal/odoh  (M8)`
  - In `## Contract`, after the M6 bullet, add:
    ```markdown
    - M8 DNS protocols: fields added to existing messages use 900-999: `ConfigSnapshot.mdns` (900)
      and `odoh` (901), `RpzTransferSource.zonemd_verify` (900), `RpzZoneStatus.zonemd` (900) and
      `zonemd_error` (901), `ServerMessage.odoh_keys` (900). `OdohKeys` travel only on `Connect`, are
      held in engine memory and are never part of `ConfigSnapshot`, `config_versions` or `state_dir`.
    ```
  - After `### ACL`, add a `### mDNS gateway and reflector (M8)` section with the route order, the
    collection rules, TTL cap 10, the 64-query cap, the NXDOMAIN rule and the reflector's socket,
    source and digest rules from this plan's Decisions.
  - In `### Upstreams (forwarding)`, add a bullet on route order: hosted zone, forward zone, mDNS for
    `local.`, then forward or recursive.
  - Add a `### Oblivious DoH (M8)` section: DoH listener paths, statuses 400/401/403/405/413/415/502,
    key ring and publish rule, and the proxy allow list and headers.
  - In `### Telemetry`, append the four M8 metric names.
  - In `## Management plane`, add:
    ```markdown
    - M8: primary zones with `zonemd_generate` get a SHA-384 SIMPLE ZONEMD on every rebuild (signed
      zones: placeholder before signing, ZONEMD RRset signed last); secondary refreshes verify
      ZONEMD (`zonemd_verify`) before writing anything. Catalog zones (`catalog_zones`): producer
      catalogs are regenerated inside the zone transaction that changes membership; consumer
      catalogs are reconciled after each refresh (`xfrin.Scheduler.AfterRefresh`) under
      `pg_advisory_xact_lock(hashtext('catalog:'||id))`. ODoH seeds (`odoh_keys`, sealed) rotate
      every `key_rotation_hours` under `nexora:odoh-rotate` and are pushed on `nexora_odoh_keys`.
    ```
  - In `## GUI`, add `/zones/catalogs` to the routes.
- [ ] Run `grep -c '900-999' docs/architecture.md; grep -c 'odoh_keys' docs/architecture.md; grep -c 'zones/catalogs' docs/architecture.md`
      and expect three non-zero counts. Run `scripts/pc-format.sh docs/architecture.md`.
- [ ] Report the paths under Files. The lead commits
      `M8 T1: contract fields 900-999, module skeletons, RFC 8976 vectors, architecture`.

## Task 2: OpenAPI contract, permissions, generated clients and stubs

Files:

- `mgmt/api/openapi.yaml`: every M8 schema and operation.
- `mgmt/internal/api/gen.go`, `web/src/api/schema.d.ts`: regenerated.
- `mgmt/internal/auth/permissions.go`, `web/src/auth/permissions.ts`: roles.
- `mgmt/internal/api/server.go`: `Deps.CatalogZones` and `Deps.ODoH` (interfaces).
- `mgmt/internal/api/catalog_zones.go`, `mgmt/internal/api/odoh.go`: created with 501 stubs (owned
  later by Tasks 22 and 16).
- `mgmt/internal/api/m8_contract_test.go`: created.

Interfaces: produces for Tasks 14, 16, 17, 22 and 27–31:

- operationIds and roles: `listCatalogZones` viewer, `createCatalogZone` operator, `getCatalogZone`
  viewer, `deleteCatalogZone` operator, `getOdohSettings` viewer, `updateOdohSettings` operator,
  `rotateOdohKey` admin;
- schemas `ZonemdVerify`, `ZonemdStatus`, `MdnsSettings`, `CatalogZone`, `CatalogZoneCreate`,
  `CatalogMember`, `OdohProxyTarget`, `OdohKeyInfo`, `OdohSettings`, `OdohSettingsUpdate`;
- Go generated names `api.CatalogZone`, `api.OdohSettings`, `api.MdnsSettings`, `api.ZonemdVerify`,
  `api.ListCatalogZonesRequestObject` and the like;
- in `server.go`, `Deps` gains two services typed on the generated schemas, so wave 0 does not
  depend on the wave 1 packages (Tasks 16 and 22 adapt their packages to these in adapter files
  they own):

  ```go
  // CatalogZones serves the catalog zone operations; nil answers 501.
  CatalogZones CatalogZoneService
  // ODoH serves the Oblivious DoH settings and keys; nil answers 501.
  ODoH ODoHService

  type CatalogZoneService interface {
  	List(ctx context.Context) ([]CatalogZone, error)
  	Get(ctx context.Context, id uuid.UUID) (CatalogZone, error)
  	Create(ctx context.Context, actor auth.Actor, in CatalogZoneCreate) (CatalogZone, error)
  	Delete(ctx context.Context, actor auth.Actor, id uuid.UUID) error
  }
  type ODoHService interface {
  	Get(ctx context.Context) (OdohSettings, error)
  	Update(ctx context.Context, actor auth.Actor, in OdohSettingsUpdate) (OdohSettings, error)
  	Rotate(ctx context.Context, actor auth.Actor) (OdohSettings, error)
  }
  ```

- [ ] Create `mgmt/internal/api/m8_contract_test.go`:
  ```go
  package api_test

  import (
  	"net/http"
  	"testing"

  	"github.com/piwi3910/nexora/mgmt/internal/auth"
  )

  // Every M8 operation exists with its role; without a service it answers 501, never 404.
  func TestM8OperationsDeclared(t *testing.T) {
  	want := map[string]auth.Role{
  		"listCatalogZones": auth.RoleViewer, "createCatalogZone": auth.RoleOperator,
  		"getCatalogZone": auth.RoleViewer, "deleteCatalogZone": auth.RoleOperator,
  		"getOdohSettings": auth.RoleViewer, "updateOdohSettings": auth.RoleOperator,
  		"rotateOdohKey": auth.RoleAdmin,
  	}
  	for op, role := range want {
  		if got, ok := auth.OperationRoles[op]; !ok || got != role {
  			t.Fatalf("%s: role %v, want %v", op, got, role)
  		}
  	}
  	srv := newM8TestServer(t) // admin session, Deps without CatalogZones and ODoH
  	for _, c := range []struct{ method, path, body string }{
  		{http.MethodGet, "/api/v1/catalog-zones", ""},
  		{http.MethodPost, "/api/v1/catalog-zones", `{"name":"catalog.test.","role":"producer","transfer":{"allow_cidrs":["127.0.0.1/32"]}}`},
  		{http.MethodGet, "/api/v1/catalog-zones/00000000-0000-0000-0000-0000000000aa", ""},
  		{http.MethodDelete, "/api/v1/catalog-zones/00000000-0000-0000-0000-0000000000aa", ""},
  		{http.MethodGet, "/api/v1/odoh", ""},
  		{http.MethodPut, "/api/v1/odoh", `{"target_enabled":false,"proxy_enabled":false,"proxy_targets":[],"proxy_timeout_ms":2000,"key_rotation_hours":24,"revision":1}`},
  		{http.MethodPost, "/api/v1/odoh/rotate-key", ""},
  	} {
  		if code := srv.do(t, c.method, c.path, c.body); code != http.StatusNotImplemented {
  			t.Fatalf("%s %s: %d, want 501", c.method, c.path, code)
  		}
  	}
  }
  ```
  Use the permissions map name that `mgmt/internal/auth/permissions.go` exports (read it first; M6's
  `m6_contract_test.go` shows the pattern). Build `newM8TestServer` and its `do` helper from the
  helpers `m6_contract_test.go` uses, in this file.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api -run TestM8OperationsDeclared -count=1'`
      and expect FAIL: `listCatalogZones: role 0, want viewer`.
- [ ] Add to `components/schemas` in `openapi.yaml`:
  ```yaml
  ZonemdVerify:
    type: string
    enum: [off, if_present, required]
    description: "Secondary and RPZ transfer zones: verify RFC 8976 ZONEMD after each transfer."
  ZonemdStatus:
    type: string
    enum: [not_checked, off, absent, verified, failed]
  MdnsSettings:
    type: object
    additionalProperties: false
    required: [enabled, interfaces, timeout_ms, reflect, reflect_interfaces]
    properties:
      enabled: { type: boolean }
      interfaces:
        {
          type: array,
          maxItems: 16,
          items: { type: string, pattern: "^[A-Za-z0-9_.:@-]{1,15}$" },
        }
      timeout_ms: { type: integer, minimum: 100, maximum: 5000 }
      reflect: { type: boolean }
      reflect_interfaces:
        {
          type: array,
          maxItems: 16,
          items: { type: string, pattern: "^[A-Za-z0-9_.:@-]{1,15}$" },
        }
  CatalogMember:
    type: object
    required: [zone_id, name, label, state, issue]
    properties:
      zone_id: { type: [string, "null"], format: uuid }
      name: { type: string }
      label: { type: string }
      state: { type: string, enum: [configured, clash] }
      issue: { type: string }
  CatalogZone:
    type: object
    required:
      [
        id,
        zone_id,
        name,
        role,
        engine_group_id,
        broken_reason,
        processed_serial,
        processed_at,
        members,
        created_at,
      ]
    properties:
      id: { type: string, format: uuid }
      zone_id: { type: string, format: uuid }
      name: { type: string }
      role: { type: string, enum: [producer, consumer] }
      engine_group_id: { type: [string, "null"], format: uuid }
      broken_reason: { type: string }
      processed_serial: { type: [integer, "null"], format: int64 }
      processed_at: { type: [string, "null"], format: date-time }
      members:
        { type: array, items: { $ref: "#/components/schemas/CatalogMember" } }
      created_at: { type: string, format: date-time }
  CatalogZoneCreate:
    type: object
    additionalProperties: false
    required: [name, role]
    properties:
      name: { type: string, maxLength: 255 }
      role: { type: string, enum: [producer, consumer] }
      engine_group_id: { type: [string, "null"], format: uuid }
      primaries:
        { type: array, items: { $ref: "#/components/schemas/ZoneEndpoint" } }
      transfer: { $ref: "#/components/schemas/ZoneTransfer" }
      notify:
        { type: array, items: { $ref: "#/components/schemas/ZoneEndpoint" } }
  OdohProxyTarget:
    type: object
    additionalProperties: false
    required: [host, ca_pem]
    properties:
      host: { type: string, maxLength: 261 }
      ca_pem: { type: string, maxLength: 65536 }
  OdohKeyInfo:
    type: object
    required: [id, created_at, publish_after, not_after]
    properties:
      id: { type: string, format: uuid }
      created_at: { type: string, format: date-time }
      publish_after: { type: string, format: date-time }
      not_after: { type: string, format: date-time }
  OdohSettingsUpdate:
    type: object
    additionalProperties: false
    required:
      [
        target_enabled,
        proxy_enabled,
        proxy_targets,
        proxy_timeout_ms,
        key_rotation_hours,
        revision,
      ]
    properties:
      target_enabled: { type: boolean }
      proxy_enabled: { type: boolean }
      proxy_targets:
        {
          type: array,
          maxItems: 64,
          items: { $ref: "#/components/schemas/OdohProxyTarget" },
        }
      proxy_timeout_ms: { type: integer, minimum: 100, maximum: 10000 }
      key_rotation_hours: { type: integer, minimum: 1, maximum: 720 }
      revision: { type: integer, format: int64 }
  OdohSettings:
    allOf:
      - { $ref: "#/components/schemas/OdohSettingsUpdate" }
      - type: object
        required: [keys, updated_at]
        properties:
          keys:
            { type: array, items: { $ref: "#/components/schemas/OdohKeyInfo" } }
          updated_at: { type: string, format: date-time }
  ```
- [ ] Extend existing schemas:
  - `ZoneCreate` and `ZoneUpdate` gain `zonemd_generate: { type: boolean }`,
    `zonemd_verify: { $ref: "#/components/schemas/ZonemdVerify" }` and
    `catalog_zone_id: { type: [string, "null"], format: uuid }`.
  - `Zone` gains the same three plus `zonemd_status: { $ref: "#/components/schemas/ZonemdStatus" }`,
    `zonemd_error: { type: string }` and `catalog_member_label: { type: string }`, all six in
    `required`.
  - `RpzZoneInput` and `RpzZoneUpdate` gain `zonemd_verify` (optional).
  - `RpzZone` gains `zonemd_verify` (required), and its `status` item gains `zonemd` (ZonemdStatus
    without `not_checked`) and `zonemd_error`, both required.
  - `EngineGroupInput` gains `mdns: { $ref: "#/components/schemas/MdnsSettings" }`, and
    `EngineGroup` gains `mdns` in `required`.
- [ ] Add the paths:
  ```yaml
  /catalog-zones:
    get:
      {
        operationId: listCatalogZones,
        tags: [zones],
        responses: { "200": list of CatalogZone },
      }
    post:
      {
        operationId: createCatalogZone,
        tags: [zones],
        requestBody: CatalogZoneCreate,
        responses: { "201": CatalogZone, "400", "409" },
      }
  /catalog-zones/{catalogZoneId}:
    get:
      { operationId: getCatalogZone, responses: { "200": CatalogZone, "404" } }
    delete: { operationId: deleteCatalogZone, responses: { "204", "404" } }
  /odoh:
    get: { operationId: getOdohSettings, responses: { "200": OdohSettings } }
    put:
      {
        operationId: updateOdohSettings,
        requestBody: OdohSettingsUpdate,
        responses: { "200": OdohSettings, "400", "409" },
      }
  /odoh/rotate-key:
    post:
      {
        operationId: rotateOdohKey,
        responses: { "200": OdohSettings, "409", "503" },
      }
  ```
  Write each in the file's full style (response `content`, `$ref: "#/components/responses/..."` for
  errors, `security` as the neighbouring zone operations). A list response is
  `{ type: object, required: [items], properties: { items: { type: array, items: CatalogZone } } }`,
  as `listZones` returns. Add a `501` response to each operation, as M6's stubs did.
- [ ] Add the seven operationIds with their roles to `mgmt/internal/auth/permissions.go` and
      `web/src/auth/permissions.ts`.
- [ ] Regenerate: `cd mgmt/api && oapi-codegen -config oapi-codegen.yaml openapi.yaml` and
      `cd web && pnpm run gen:api`.
- [ ] Add `CatalogZones CatalogZoneService`, `ODoH ODoHService` and both interfaces to `server.go`. Create `catalog_zones.go` and `odoh.go`, each implementing its
      operations as
      `return nil, apiError(http.StatusNotImplemented, "not_implemented", "catalog zones are not available")`
      when the service is nil, and delegating to the service otherwise. Use the error helper the M6
      stubs used (read `engine_logs.go` at the M7 head).
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api -run "TestM8OperationsDeclared|TestPermissionsCoverEveryOperation" -count=1 && go vet ./mgmt/...'`
      and `cd web && pnpm run typecheck`. Expect PASS.
- [ ] Report the paths. The lead commits `M8 T2: OpenAPI contract, permissions, generated clients, stubs`.

## Task 3: Migrations and model fields

Files:

- `mgmt/migrations/00900_zonemd.sql`, `00901_catalog_zones.sql`, `00902_odoh.sql`,
  `00903_engine_group_mdns.sql`: created.
- `mgmt/internal/zone/model.go`: new `Zone` and input fields, and the `Signer.SignZONEMD` method.
- `mgmt/internal/zone/service.go`: the zone column list, `scanZone`, the new input columns in the
  insert and update statements, and the `CreateZoneInTx`/`DeleteZoneInTx` extraction (no behaviour
  change).
- `mgmt/internal/fleet/groups.go`: `EngineGroup` mDNS fields, scan, insert and update columns.
- `mgmt/internal/store/resolution.go`: `RPZZone.ZonemdVerify`, scan, insert and update columns.
- `mgmt/internal/dnssec/store.go`: only a `SignZONEMD` method returning the input unchanged with a
  `debt:` comment naming Task 12, so `*dnssec.Store` keeps satisfying `zone.Signer`.
- `mgmt/internal/store/m8_migration_test.go`: created.

Interfaces: produces for Tasks 12–17 and 22:

```go
// zone.Zone gains:
ZonemdGenerate      bool
ZonemdVerify        string     // "off" | "if_present" | "required"
ZonemdStatus        string     // "not_checked" | "off" | "absent" | "verified" | "failed"
ZonemdError         string
CatalogZoneID       *uuid.UUID // producer membership or consumer ownership
CatalogMemberLabel  string     // consumer-created zones only
// zone.CreateZoneInput gains ZonemdGenerate bool, ZonemdVerify string, CatalogZoneID *uuid.UUID,
//   CatalogMemberLabel string; zone.UpdateZoneInput gains ZonemdGenerate *bool, ZonemdVerify *string,
//   CatalogZoneID **uuid.UUID (nil: unchanged; pointer to nil: leave the catalog).
// zone.Signer gains:
SignZONEMD(ctx context.Context, tx pgx.Tx, z *Zone, served []dns.RR, now time.Time) ([]dns.RR, error)
// fleet.EngineGroup gains:
MdnsEnabled bool; MdnsInterfaces []string; MdnsTimeoutMS int32; MdnsReflect bool; MdnsReflectInterfaces []string
// store.RPZZone gains:
ZonemdVerify string
// zone.Service gains (the transaction-free bodies of CreateZone and DeleteZone; the public methods call them):
func (s *Service) CreateZoneInTx(ctx context.Context, tx pgx.Tx, actor auth.Actor, in CreateZoneInput) (*Zone, error)
func (s *Service) DeleteZoneInTx(ctx context.Context, tx pgx.Tx, actor auth.Actor, id uuid.UUID) error
```

- [ ] Create `mgmt/internal/store/m8_migration_test.go`:
  ```go
  package store_test

  import (
  	"context"
  	"testing"
  	"time"

  	"github.com/jackc/pgx/v5/stdlib"
  	"github.com/pressly/goose/v3"

  	"github.com/piwi3910/nexora/e2e/harness"
  	"github.com/piwi3910/nexora/mgmt/internal/store"
  	"github.com/piwi3910/nexora/mgmt/migrations"
  )

  // An install at the M7 head keeps its published behaviour after the M8 migrations.
  func TestM8MigrationKeepsBehaviour(t *testing.T) {
  	pg := harness.New(t).StartPostgres()
  	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
  	defer cancel()
  	st, err := store.Open(ctx, pg.URL)
  	if err != nil {
  		t.Fatal(err)
  	}
  	defer st.Close()
  	db := stdlib.OpenDBFromPool(st.Pool)
  	defer db.Close()
  	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
  	if err != nil {
  		t.Fatal(err)
  	}
  	if _, err := provider.UpTo(ctx, 899); err != nil {
  		t.Fatal(err)
  	}
  	for _, q := range []string{
  		`insert into zones (name, kind, soa_mname, soa_rname) values ('p.test.', 'primary', 'ns.p.test.', 'h.p.test.')`,
  		`insert into zones (name, kind, soa_mname, soa_rname, primaries) values ('s.test.', 'secondary', '', '', '[{"address":"127.0.0.1:53","tsig_key_id":null}]')`,
  		`insert into rpz_zones (name, position, source_type, primary_address) values ('rpz.test.', 1, 'transfer', '127.0.0.1:53')`,
  		`insert into engine_groups (name) values ('edge')`,
  	} {
  		if _, err := st.Pool.Exec(ctx, q); err != nil {
  			t.Fatalf("%s: %v", q, err)
  		}
  	}
  	if err := st.Migrate(ctx); err != nil {
  		t.Fatal(err)
  	}
  	var gen int
  	var verify []string
  	if err := st.Pool.QueryRow(ctx, `select count(*) filter (where zonemd_generate), array_agg(distinct zonemd_verify) from zones`).Scan(&gen, &verify); err != nil {
  		t.Fatal(err)
  	}
  	if gen != 0 || len(verify) != 1 || verify[0] != "if_present" {
  		t.Fatalf("zones: generate=%d verify=%v", gen, verify)
  	}
  	var rpz string
  	if err := st.Pool.QueryRow(ctx, `select zonemd_verify from rpz_zones`).Scan(&rpz); err != nil || rpz != "if_present" {
  		t.Fatalf("rpz zonemd_verify %q: %v", rpz, err)
  	}
  	var mdns int
  	if err := st.Pool.QueryRow(ctx, `select count(*) from engine_groups where mdns_enabled or mdns_reflect`).Scan(&mdns); err != nil || mdns != 0 {
  		t.Fatalf("mdns groups %d: %v", mdns, err)
  	}
  	var rows int
  	var target, proxy bool
  	if err := st.Pool.QueryRow(ctx, `select count(*), bool_or(target_enabled), bool_or(proxy_enabled) from odoh_settings`).Scan(&rows, &target, &proxy); err != nil {
  		t.Fatal(err)
  	}
  	if rows != 1 || target || proxy {
  		t.Fatalf("odoh_settings rows=%d target=%v proxy=%v", rows, target, proxy)
  	}
  	if _, err := st.Pool.Exec(ctx, `update zones set zonemd_generate = true where name = 's.test.'`); err == nil {
  		t.Fatal("a secondary zone accepted zonemd_generate")
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/store -run TestM8MigrationKeepsBehaviour -count=1'`.
      Expect FAIL: `column "zonemd_generate" does not exist`. If the M7 head has migrations above
      00899, stop and renumber as the Constraints say.
- [ ] Create `mgmt/migrations/00900_zonemd.sql`:
  ```sql
  -- +goose Up
  ALTER TABLE zones
      ADD COLUMN zonemd_generate boolean NOT NULL DEFAULT false,
      ADD COLUMN zonemd_verify   text NOT NULL DEFAULT 'if_present' CHECK (zonemd_verify IN ('off', 'if_present', 'required')),
      ADD COLUMN zonemd_status   text NOT NULL DEFAULT 'not_checked' CHECK (zonemd_status IN ('not_checked', 'off', 'absent', 'verified', 'failed')),
      ADD COLUMN zonemd_error    text NOT NULL DEFAULT '',
      ADD CONSTRAINT zones_zonemd_generate_primary CHECK (kind = 'primary' OR NOT zonemd_generate);
  ALTER TABLE rpz_zones
      ADD COLUMN zonemd_verify text NOT NULL DEFAULT 'if_present' CHECK (zonemd_verify IN ('off', 'if_present', 'required'));
  -- Existing rows keep today's behaviour (no verification); new rows default to if_present
  -- (lead decision 2026-09-15).
  UPDATE zones SET zonemd_verify = 'off';
  UPDATE rpz_zones SET zonemd_verify = 'off';
  ALTER TABLE engine_rpz_status
      ADD COLUMN zonemd       text NOT NULL DEFAULT 'off' CHECK (zonemd IN ('off', 'absent', 'verified', 'failed')),
      ADD COLUMN zonemd_error text NOT NULL DEFAULT '';

  -- +goose Down
  ALTER TABLE engine_rpz_status DROP COLUMN zonemd_error, DROP COLUMN zonemd;
  ALTER TABLE rpz_zones DROP COLUMN zonemd_verify;
  ALTER TABLE zones DROP CONSTRAINT zones_zonemd_generate_primary,
      DROP COLUMN zonemd_error, DROP COLUMN zonemd_status, DROP COLUMN zonemd_verify, DROP COLUMN zonemd_generate;
  ```
- [ ] Create `mgmt/migrations/00901_catalog_zones.sql`:
  ```sql
  -- +goose Up
  CREATE TABLE catalog_zones (
      id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
      zone_id          uuid NOT NULL UNIQUE REFERENCES zones (id) ON DELETE CASCADE,
      role             text NOT NULL CHECK (role IN ('producer', 'consumer')),
      broken_reason    text NOT NULL DEFAULT '',
      processed_serial bigint,
      processed_at     timestamptz,
      created_at       timestamptz NOT NULL DEFAULT now()
  );
  ALTER TABLE zones
      ADD COLUMN catalog_zone_id      uuid REFERENCES catalog_zones (id) ON DELETE SET NULL,
      ADD COLUMN catalog_member_label text NOT NULL DEFAULT '';
  CREATE INDEX zones_catalog_zone ON zones (catalog_zone_id) WHERE catalog_zone_id IS NOT NULL;
  CREATE TABLE catalog_member_issues (
      catalog_zone_id uuid NOT NULL REFERENCES catalog_zones (id) ON DELETE CASCADE,
      member_name     text NOT NULL,
      label           text NOT NULL,
      issue           text NOT NULL,
      seen_at         timestamptz NOT NULL DEFAULT now(),
      PRIMARY KEY (catalog_zone_id, member_name)
  );

  -- +goose Down
  DROP TABLE catalog_member_issues;
  DROP INDEX zones_catalog_zone;
  ALTER TABLE zones DROP COLUMN catalog_member_label, DROP COLUMN catalog_zone_id;
  DROP TABLE catalog_zones;
  ```
- [ ] Create `mgmt/migrations/00902_odoh.sql`:
  ```sql
  -- +goose Up
  CREATE TABLE odoh_settings (
      id                 boolean PRIMARY KEY DEFAULT true CHECK (id),
      target_enabled     boolean NOT NULL DEFAULT false,
      proxy_enabled      boolean NOT NULL DEFAULT false,
      proxy_targets      jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(proxy_targets) = 'array'),
      proxy_timeout_ms   integer NOT NULL DEFAULT 2000 CHECK (proxy_timeout_ms BETWEEN 100 AND 10000),
      key_rotation_hours integer NOT NULL DEFAULT 24 CHECK (key_rotation_hours BETWEEN 1 AND 720),
      revision           bigint NOT NULL DEFAULT 1,
      updated_at         timestamptz NOT NULL DEFAULT now(),
      CONSTRAINT odoh_proxy_targets CHECK (NOT proxy_enabled OR jsonb_array_length(proxy_targets) >= 1)
  );
  INSERT INTO odoh_settings DEFAULT VALUES;
  CREATE TABLE odoh_keys (
      id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
      -- NXE1 envelope from internal/secrets; never plaintext.
      seed_envelope bytea NOT NULL CHECK (substring(seed_envelope from 1 for 4) = 'NXE1'::bytea),
      created_at    timestamptz NOT NULL DEFAULT now(),
      publish_after timestamptz NOT NULL,
      not_after     timestamptz NOT NULL CHECK (not_after > publish_after)
  );

  -- +goose Down
  DROP TABLE odoh_keys;
  DROP TABLE odoh_settings;
  ```
- [ ] Create `mgmt/migrations/00903_engine_group_mdns.sql`:
  ```sql
  -- +goose Up
  ALTER TABLE engine_groups
      ADD COLUMN mdns_enabled            boolean NOT NULL DEFAULT false,
      ADD COLUMN mdns_interfaces         text[] NOT NULL DEFAULT '{}',
      ADD COLUMN mdns_timeout_ms         integer NOT NULL DEFAULT 500 CHECK (mdns_timeout_ms BETWEEN 100 AND 5000),
      ADD COLUMN mdns_reflect            boolean NOT NULL DEFAULT false,
      ADD COLUMN mdns_reflect_interfaces text[] NOT NULL DEFAULT '{}',
      ADD CONSTRAINT engine_groups_mdns_interfaces CHECK (NOT mdns_enabled OR cardinality(mdns_interfaces) >= 1),
      ADD CONSTRAINT engine_groups_mdns_reflect CHECK (NOT mdns_reflect OR cardinality(mdns_reflect_interfaces) >= 2);

  -- +goose Down
  ALTER TABLE engine_groups DROP CONSTRAINT engine_groups_mdns_reflect, DROP CONSTRAINT engine_groups_mdns_interfaces,
      DROP COLUMN mdns_reflect_interfaces, DROP COLUMN mdns_reflect, DROP COLUMN mdns_timeout_ms,
      DROP COLUMN mdns_interfaces, DROP COLUMN mdns_enabled;
  ```
- [ ] Add the Interfaces fields to `zone.Zone`, `CreateZoneInput`, `UpdateZoneInput` and `Signer` in
      `model.go`. Extend the zone column list and `scanZone` in `service.go` with the six columns (in
      Interfaces order), and write the new input fields in the insert and update statements. Move the
      bodies of `CreateZone` and `DeleteZone` into `CreateZoneInTx` and `DeleteZoneInTx` (the public
      methods open the transaction, call them and publish as before). Add the `SignZONEMD`
      pass-through to `dnssec/store.go`.
- [ ] Extend `fleet.EngineGroup`, `scanEngineGroup`, `CreateEngineGroup` and `UpdateEngineGroup` with
      the five mDNS columns. Extend `store.RPZZone`, `scanRPZZone`, `CreateRPZZone` and
      `UpdateRPZZone` with `zonemd_verify` (empty input means `if_present`).
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/store -run TestM8MigrationKeepsBehaviour -count=1 && go test ./mgmt/internal/zone ./mgmt/internal/fleet ./mgmt/internal/dnssec -count=1 && go vet ./mgmt/...'`
      and expect PASS.
- [ ] Report the paths. The lead commits `M8 T3: migrations 00900-00903 and model fields`.

## Task 4: ZONEMD digest and verification in the management plane

Files: `mgmt/internal/zonemd/zonemd.go` (created), `mgmt/internal/zonemd/zonemd_test.go` (created)
Interfaces: produces for Tasks 12, 13, 15, 23:

```go
package zonemd
type Mode string
const (ModeOff Mode = "off"; ModeIfPresent Mode = "if_present"; ModeRequired Mode = "required")
type Status string
const (StatusOff Status = "off"; StatusAbsent Status = "absent"; StatusVerified Status = "verified"; StatusFailed Status = "failed")
type Result struct { Status Status; Err string }
const (SchemeSimple uint8 = 1; HashSHA384 uint8 = 1; HashSHA512 uint8 = 2)
func Digest(origin string, rrs []dns.RR, hash uint8) ([]byte, error)
func Verify(origin string, rrs []dns.RR, mode Mode) Result
func Placeholder(origin string, serial, ttl uint32) *dns.ZONEMD
func Apply(origin string, rrs []dns.RR) error // sets serial and SHA-384 digest of the apex ZONEMD (scheme 1, hash 1)
```

- [ ] Create `mgmt/internal/zonemd/zonemd_test.go`:
  ```go
  package zonemd

  import (
  	"encoding/hex"
  	"os"
  	"path/filepath"
  	"runtime"
  	"strings"
  	"testing"

  	"github.com/miekg/dns"
  )

  func vector(t *testing.T, name string) []dns.RR {
  	t.Helper()
  	_, file, _, _ := runtime.Caller(0)
  	src, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../../e2e/testdata/rfc8976", name+".zone"))
  	if err != nil {
  		t.Fatal(err)
  	}
  	zp := dns.NewZoneParser(strings.NewReader(string(src)), "example.", name)
  	var rrs []dns.RR
  	for rr, ok := zp.Next(); ok; rr, ok = zp.Next() {
  		rrs = append(rrs, rr)
  	}
  	if err := zp.Err(); err != nil {
  		t.Fatal(err)
  	}
  	return rrs
  }

  func TestDigestMatchesRFC8976AppendixA(t *testing.T) {
  	checked := 0
  	for _, name := range []string{"a1", "a2", "a3"} {
  		rrs := vector(t, name)
  		for _, rr := range rrs {
  			z, ok := rr.(*dns.ZONEMD)
  			if !ok || !strings.EqualFold(z.Hdr.Name, "example.") || z.Scheme != SchemeSimple || (z.Hash != HashSHA384 && z.Hash != HashSHA512) {
  				continue
  			}
  			got, err := Digest("example.", rrs, z.Hash)
  			if err != nil {
  				t.Fatal(err)
  			}
  			if hex.EncodeToString(got) != strings.ToLower(z.Digest) {
  				t.Fatalf("%s hash %d: digest %x, RFC %s", name, z.Hash, got, z.Digest)
  			}
  			checked++
  		}
  		if r := Verify("example.", rrs, ModeRequired); r.Status != StatusVerified {
  			t.Fatalf("%s: %+v", name, r)
  		}
  	}
  	if checked != 4 { // A.1 SHA-384, A.2 SHA-384, A.3 SHA-384 and SHA-512
  		t.Fatalf("checked %d digests, want 4", checked)
  	}
  }

  func TestVerifyRules(t *testing.T) {
  	base := vector(t, "a1")
  	apex := func(rrs []dns.RR) *dns.ZONEMD {
  		for _, rr := range rrs {
  			if z, ok := rr.(*dns.ZONEMD); ok && z.Hdr.Name == "example." {
  				return z
  			}
  		}
  		return nil
  	}
  	clone := func() []dns.RR {
  		out := make([]dns.RR, len(base))
  		for i, rr := range base {
  			out[i] = dns.Copy(rr)
  		}
  		return out
  	}
  	without := func() []dns.RR {
  		var out []dns.RR
  		for _, rr := range clone() {
  			if _, ok := rr.(*dns.ZONEMD); !ok {
  				out = append(out, rr)
  			}
  		}
  		return out
  	}
  	cases := []struct {
  		name   string
  		rrs    func() []dns.RR
  		mode   Mode
  		status Status
  	}{
  		{"valid", clone, ModeIfPresent, StatusVerified},
  		{"off ignores a broken digest", func() []dns.RR { r := clone(); apex(r).Digest = strings.Repeat("00", 48); return r }, ModeOff, StatusOff},
  		{"serial mismatch", func() []dns.RR { r := clone(); apex(r).Serial++; return r }, ModeIfPresent, StatusFailed},
  		{"one bit changed", func() []dns.RR { r := clone(); r[len(r)-2].(*dns.A).A[3] ^= 1; return r }, ModeIfPresent, StatusFailed},
  		{"unsupported hash only", func() []dns.RR { r := clone(); apex(r).Hash = 200; return r }, ModeIfPresent, StatusFailed},
  		{"short digest", func() []dns.RR { r := clone(); apex(r).Digest = "c68090d90a7aed716bc459f9"; return r }, ModeIfPresent, StatusFailed},
  		{"duplicate tuple", func() []dns.RR {
  			r := clone()
  			d := dns.Copy(apex(r)).(*dns.ZONEMD)
  			d.Digest = strings.Repeat("ab", 48)
  			return append(r, d)
  		}, ModeIfPresent, StatusFailed},
  		{"absent if_present", without, ModeIfPresent, StatusAbsent},
  		{"absent required", without, ModeRequired, StatusFailed},
  		{"non-apex only", func() []dns.RR {
  			r := without()
  			z := dns.Copy(apex(clone())).(*dns.ZONEMD)
  			z.Hdr.Name = "ns1.example."
  			return append(r, z)
  		}, ModeRequired, StatusFailed},
  	}
  	for _, c := range cases {
  		if got := Verify("example.", c.rrs(), c.mode); got.Status != c.status {
  			t.Fatalf("%s: %+v, want %s", c.name, got, c.status)
  		}
  	}
  	// Apply on a zone with a placeholder produces a digest Verify accepts.
  	rrs := append(without(), Placeholder("example.", 0, 86400))
  	if err := Apply("example.", rrs); err != nil {
  		t.Fatal(err)
  	}
  	if got := Verify("example.", rrs, ModeRequired); got.Status != StatusVerified || apex(rrs).Serial != 2018031900 {
  		t.Fatalf("applied: %+v serial %d", got, apex(rrs).Serial)
  	}
  }
  ```
  In A.1 the second-to-last record is `ns1 A 203.0.113.63`; if the parser order differs, pick the `*dns.A` by type.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/zonemd -count=1'` and expect FAIL: the
      package does not compile (`undefined: Digest`).
- [ ] Implement `zonemd.go`:
  - `canonical(origin string, rrs []dns.RR) ([][]byte, error)`:
    - keep RRs at or below origin (`dns.IsSubDomain`);
    - drop apex ZONEMD and apex RRSIG with `TypeCovered == dns.TypeZONEMD`;
    - for each, `dns.Copy`, lowercase the owner, lowercase the RDATA names of the Decisions' type
      list (switch on the concrete miekg types: `*dns.NS`, `*dns.CNAME`, `*dns.SOA` (Ns, Mbox),
      `*dns.PTR`, `*dns.MX`, `*dns.SRV`, `*dns.DNAME`, `*dns.NAPTR` (Replacement), `*dns.RRSIG`
      (SignerName), `*dns.MINFO`, `*dns.RP`, `*dns.AFSDB`, `*dns.RT`, `*dns.PX`, `*dns.KX`, `*dns.MB`,
      `*dns.MG`, `*dns.MR`, `*dns.MD`, `*dns.MF`);
    - pack with `dns.PackRR(c, buf, 0, nil, false)`;
    - sort by the RFC 4034 §6.1 owner key (labels reversed, each label's octets compared unsigned),
      then type, then RDATA octets (the packed bytes after the 10-octet fixed header following the
      owner);
    - drop entries whose owner, type, class and RDATA equal the previous entry's.
  - `Digest`: SHA-384 or SHA-512 over the concatenation. Any other hash is an error.
  - `Verify`:
    1. mode off gives `StatusOff`;
    2. collect apex ZONEMD RRs; none gives `StatusAbsent` for `if_present`, and `StatusFailed`
       ("no apex ZONEMD") for `required`;
    3. tuples seen twice are unusable;
    4. for each usable RR:
       - serial must equal the apex SOA serial;
       - scheme must be 1 and hash 1 or 2;
       - the digest length must be 48 or 64 and at least 12;
       - compute and compare with `hmac.Equal` on the decoded bytes;
    5. the first match gives `StatusVerified`;
    6. otherwise `StatusFailed` with the last reason (`serial mismatch`, `no supported ZONEMD`,
       `digest mismatch`, `duplicate scheme and hash`).
  - `Placeholder` gives scheme 1, hash 1, 96 zero hex digits, class IN, and the given TTL.
  - `Apply` finds the apex ZONEMD (error when none), sets `Serial` from the apex SOA, and sets
    `Digest = hex(Digest(origin, rrs, HashSHA384))`.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/zonemd -count=1 -v'` and expect PASS with
      `checked` 4.
- [ ] Report the paths. The lead commits `M8 T4: ZONEMD digest and verification (Go)`.

## Task 5: ZONEMD digest and verification in the engine

Files: `engine/src/zonemd.rs`
Interfaces: produces for Task 20:

```rust
pub const TYPE_ZONEMD: u16 = 63;
pub const SCHEME_SIMPLE: u8 = 1;
pub const HASH_SHA384: u8 = 1;
pub const HASH_SHA512: u8 = 2;
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum VerifyMode { Off, IfPresent, Required }
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Verdict { Off, Absent, Verified, Failed(String) }
impl VerifyMode { pub fn from_proto(v: i32) -> VerifyMode } // 2 IfPresent, 3 Required, anything else Off
impl Verdict { pub fn to_proto(&self) -> (i32, String) }   // ZonemdStatus value and error text
pub fn digest(origin: &Name, records: &[Record], hash: u8) -> Result<Vec<u8>, String>;
pub fn verify(origin: &Name, records: &[Record], mode: VerifyMode) -> Verdict;
```

- [ ] Replace the skeleton of `engine/src/zonemd.rs` with its doc comment plus this test module:
  ```rust
  #[cfg(test)]
  mod tests {
      use super::*;
      use hickory_proto::rr::{Name, RData, Record, RecordType};
      use hickory_proto::serialize::binary::BinDecodable;

      fn vector(name: &str) -> Vec<Record> {
          let path = format!("{}/../e2e/testdata/rfc8976/{name}.hex", env!("CARGO_MANIFEST_DIR"));
          std::fs::read_to_string(&path)
              .unwrap()
              .lines()
              .map(|l| Record::from_bytes(&hex_decode(l)).unwrap())
              .collect()
      }

      fn hex_decode(s: &str) -> Vec<u8> {
          (0..s.len()).step_by(2).map(|i| u8::from_str_radix(&s[i..i + 2], 16).unwrap()).collect()
      }

      fn zonemd_rdata(r: &Record) -> Option<&[u8]> {
          match r.data() {
              RData::Unknown { rdata, .. } if u16::from(r.record_type()) == TYPE_ZONEMD => rdata.anything(),
              _ => None,
          }
      }

      #[test]
      fn digest_matches_rfc8976_appendix_a() {
          let origin = Name::from_ascii("example.").unwrap();
          let mut checked = 0;
          for name in ["a1", "a2", "a3"] {
              let rrs = vector(name);
              for r in &rrs {
                  let Some(rd) = zonemd_rdata(r) else { continue };
                  if r.name() != &origin || rd[4] != SCHEME_SIMPLE || !(rd[5] == HASH_SHA384 || rd[5] == HASH_SHA512) {
                      continue;
                  }
                  assert_eq!(digest(&origin, &rrs, rd[5]).unwrap(), rd[6..].to_vec(), "{name} hash {}", rd[5]);
                  checked += 1;
              }
              assert_eq!(verify(&origin, &rrs, VerifyMode::Required), Verdict::Verified, "{name}");
          }
          assert_eq!(checked, 4);
      }

      #[test]
      fn verify_rules() {
          let origin = Name::from_ascii("example.").unwrap();
          let base = vector("a1");
          let is_apex_zonemd = |r: &Record| u16::from(r.record_type()) == TYPE_ZONEMD && r.name() == &origin;
          let with_apex = |f: &dyn Fn(&mut Vec<u8>)| -> Vec<Record> {
              base.iter()
                  .map(|r| {
                      if !is_apex_zonemd(r) {
                          return r.clone();
                      }
                      let mut rd = zonemd_rdata(r).unwrap().to_vec();
                      f(&mut rd);
                      Record::from_rdata(r.name().clone(), r.ttl(), RData::Unknown {
                          code: RecordType::Unknown(TYPE_ZONEMD),
                          rdata: hickory_proto::rr::rdata::NULL::with(rd),
                      })
                  })
                  .collect()
          };
          let without: Vec<Record> = base.iter().filter(|r| !is_apex_zonemd(r)).cloned().collect();
          assert_eq!(verify(&origin, &base, VerifyMode::IfPresent), Verdict::Verified);
          assert_eq!(verify(&origin, &with_apex(&|rd| rd[6] ^= 1), VerifyMode::Off), Verdict::Off);
          assert!(matches!(verify(&origin, &with_apex(&|rd| rd[3] ^= 1), VerifyMode::IfPresent), Verdict::Failed(e) if e.contains("serial")));
          assert!(matches!(verify(&origin, &with_apex(&|rd| rd[6] ^= 1), VerifyMode::IfPresent), Verdict::Failed(e) if e.contains("digest")));
          assert!(matches!(verify(&origin, &with_apex(&|rd| rd[5] = 200), VerifyMode::IfPresent), Verdict::Failed(_)));
          assert!(matches!(verify(&origin, &with_apex(&|rd| rd.truncate(6 + 11)), VerifyMode::IfPresent), Verdict::Failed(_)));
          let mut dup = base.clone();
          dup.extend(with_apex(&|rd| rd[7] ^= 1).into_iter().filter(|r| is_apex_zonemd(r)));
          assert!(matches!(verify(&origin, &dup, VerifyMode::IfPresent), Verdict::Failed(e) if e.contains("duplicate")));
          assert_eq!(verify(&origin, &without, VerifyMode::IfPresent), Verdict::Absent);
          assert!(matches!(verify(&origin, &without, VerifyMode::Required), Verdict::Failed(_)));
          assert_eq!(VerifyMode::from_proto(0), VerifyMode::Off);
          assert_eq!(Verdict::Verified.to_proto().0, 3);
      }
  }
  ```
  Use hickory-proto 0.26.3's names for `NULL::with` and `anything()` (read
  `~/.cargo/registry/src/*/hickory-proto-0.26.3/src/rr/rdata/null.rs`). If hickory decodes type 63
  into a dedicated variant, match that instead; the test data stays the same.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib zonemd::tests'` and expect
      FAIL: it does not compile (`cannot find function digest`).
- [ ] Implement:
  - `canonical(origin, records) -> Result<Vec<Vec<u8>>, String>`:
    - owners at or below origin (`origin.zone_of(name)`);
    - exclude apex type 63 and apex RRSIG covering 63;
    - encode each record into a `Vec<u8>` with `BinEncoder::with_mode(&mut buf, EncodeMode::Signing)`,
      owner `name.to_lowercase()` emitted uncompressed, then type, class, TTL, RDLENGTH and RDATA via
      `record.data().emit`;
    - sort by `(recursor::dnssec::denial::canonical_cmp(owner), type, rdata octets)`, keeping the
      owner `Name` beside the bytes for the comparison;
    - dedupe equal (owner, type, class, rdata).
  - `digest`: `sha2::Sha384` or `sha2::Sha512` over the concatenation.
  - `verify` follows the same steps as Task 4's `Verify`, with the same reason words (`serial`,
    `digest`, `duplicate`, `no supported`).
  - `to_proto` maps Off 1, Absent 2, Verified 3, Failed 4.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib zonemd::tests && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`
      and expect PASS.
- [ ] Report the paths. The lead commits `M8 T5: ZONEMD digest and verification (engine)`.

## Task 6: Engine mDNS interface lookup and gateway core

Files: `engine/src/mdns/iface.rs`, `engine/src/mdns/gateway.rs`
Interfaces: produces for Tasks 18 and 21:

```rust
// iface.rs
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct IfaceTarget { pub name: String, pub index: u32, pub v4: Option<Ipv4Addr>, pub v6_link_local: Option<Ipv6Addr> }
pub fn lookup(name: &str) -> Option<IfaceTarget>; // None when the interface does not exist or has no address
// gateway.rs
pub const MAX_INFLIGHT: usize = 64;
pub const TTL_CAP: u32 = 10;
pub const GROUP_V4: SocketAddr; // 224.0.0.251:5353
pub const GROUP_V6: Ipv6Addr;   // ff02::fb (port 5353, scope = interface index)
pub struct Target { pub bind: SocketAddr, pub group: SocketAddr, pub if_index: u32 }
impl Target { pub fn for_iface(i: &IfaceTarget) -> Vec<Target> } // one per family with an address
pub struct Gateway { /* targets, timeout */ }
impl Gateway {
    pub fn new(targets: Vec<Target>, timeout: Duration) -> Gateway;
    pub async fn query(&self, qname: &Name, qtype: RecordType) -> Outcome;
}
pub enum Outcome { Answers(Vec<Record>), NoAnswer, Busy }
pub fn unique_type(t: RecordType) -> bool; // A, AAAA, SRV, TXT
pub struct Counters { pub answered: AtomicU64, pub unanswered: AtomicU64, pub dropped: AtomicU64 }
pub static COUNTERS: Counters;
```

- [ ] Add to `engine/src/mdns/gateway.rs` (below the doc comment) this test module:
  ```rust
  #[cfg(test)]
  mod tests {
      use super::*;
      use hickory_proto::rr::{Name, RData, RecordType};
      use std::net::SocketAddr;
      use std::time::{Duration, Instant};
      use tokio::net::UdpSocket;

      /// Wire reply: header with `id`, the question of `query`, and one answer per `(name wire, type, class, ttl, rdata)`.
      fn reply(id: u16, query: &[u8], answers: &[(Vec<u8>, u16, u16, u32, Vec<u8>)]) -> Vec<u8> {
          let qend = 12 + query[12..].iter().position(|&b| b == 0).unwrap() + 1 + 4;
          let mut m = vec![(id >> 8) as u8, id as u8, 0x84, 0x00, 0, 1, 0, answers.len() as u8, 0, 0, 0, 0];
          m.extend_from_slice(&query[12..qend]);
          for (name, t, class, ttl, rd) in answers {
              m.extend_from_slice(name);
              m.extend_from_slice(&t.to_be_bytes());
              m.extend_from_slice(&class.to_be_bytes());
              m.extend_from_slice(&ttl.to_be_bytes());
              m.extend_from_slice(&(rd.len() as u16).to_be_bytes());
              m.extend_from_slice(rd);
          }
          m
      }

      fn wire(name: &str) -> Vec<u8> {
          let mut out = Vec::new();
          for l in name.trim_end_matches('.').split('.') {
              out.push(l.len() as u8);
              out.extend_from_slice(l.as_bytes());
          }
          out.push(0);
          out
      }

      /// A unicast stand-in for the multicast group: `script(query)` gives the datagrams to send back, with delays.
      async fn responder<F>(script: F) -> SocketAddr
      where
          F: Fn(&[u8]) -> Vec<(u64, Vec<u8>)> + Send + 'static,
      {
          let sock = UdpSocket::bind("127.0.0.1:0").await.unwrap();
          let addr = sock.local_addr().unwrap();
          tokio::spawn(async move {
              let mut buf = [0u8; 1500];
              loop {
                  let (n, from) = sock.recv_from(&mut buf).await.unwrap();
                  for (delay_ms, d) in script(&buf[..n]) {
                      tokio::time::sleep(Duration::from_millis(delay_ms)).await;
                      sock.send_to(&d, from).await.unwrap();
                  }
              }
          });
          addr
      }

      fn gateway(group: SocketAddr, timeout_ms: u64) -> Gateway {
          Gateway::new(
              vec![Target { bind: "127.0.0.1:0".parse().unwrap(), group, if_index: 0 }],
              Duration::from_millis(timeout_ms),
          )
      }

      #[tokio::test(flavor = "current_thread")]
      async fn reply_checks_and_ttl_cap() {
          let group = responder(|q| {
              let id = u16::from_be_bytes([q[0], q[1]]);
              let name = wire("printer.local.");
              let other = wire("scanner.local.");
              vec![
                  (0, reply(id ^ 1, q, &[(name.clone(), 1, 1, 120, vec![10, 254, 0, 66])])), // wrong ID
                  (0, reply(id, q, &[(other, 1, 1, 120, vec![10, 254, 0, 77])])), // other owner
                  (0, reply(id, q, &[(name, 1, 0x8001, 120, vec![10, 254, 0, 9])])), // cache-flush class
              ]
          })
          .await;
          let out = gateway(group, 500).query(&Name::from_ascii("printer.local.").unwrap(), RecordType::A).await;
          let Outcome::Answers(rrs) = out else { panic!("no answers") };
          assert_eq!(rrs.len(), 1, "only the matching owner and ID count: {rrs:?}");
          assert_eq!(rrs[0].ttl(), TTL_CAP, "TTL capped at 10");
          assert_eq!(u16::from(rrs[0].dns_class()), 1, "cache-flush bit cleared");
          assert!(matches!(rrs[0].data(), RData::A(a) if a.0 == std::net::Ipv4Addr::new(10, 254, 0, 9)));
      }

      #[tokio::test(flavor = "current_thread")]
      async fn first_answer_ends_unique_types() {
          let group = responder(|q| {
              let id = u16::from_be_bytes([q[0], q[1]]);
              let qtype = u16::from_be_bytes([q[q.len() - 4], q[q.len() - 3]]);
              if qtype == 1 {
                  return vec![(0, reply(id, q, &[(wire("printer.local."), 1, 1, 120, vec![10, 254, 0, 9])]))];
              }
              if qtype != 12 {
                  return vec![];
              }
              let owner = wire("_ipp._tcp.local.");
              let inst = |n: &str| wire(&format!("{n}._ipp._tcp.local."));
              vec![
                  (20, reply(id, q, &[(owner.clone(), 12, 1, 4500, inst("one"))])),
                  (50, reply(id, q, &[(owner.clone(), 12, 1, 4500, inst("two")), (owner.clone(), 12, 1, 4500, inst("one"))])),
                  (50, reply(id, q, &[(owner, 12, 1, 4500, inst("three"))])),
              ]
          })
          .await;
          let gw = gateway(group, 400);
          let started = Instant::now();
          assert!(matches!(gw.query(&Name::from_ascii("printer.local.").unwrap(), RecordType::A).await, Outcome::Answers(_)));
          assert!(started.elapsed() < Duration::from_millis(200), "a unique type returns on the first answer");
          let started = Instant::now();
          let Outcome::Answers(ptrs) = gw.query(&Name::from_ascii("_ipp._tcp.local.").unwrap(), RecordType::PTR).await else {
              panic!("no PTR answers")
          };
          assert!(started.elapsed() >= Duration::from_millis(400), "PTR waits for the whole window");
          assert_eq!(ptrs.len(), 3, "three instances, duplicates dropped: {ptrs:?}");
          let started = Instant::now();
          assert!(matches!(gw.query(&Name::from_ascii("nothere.local.").unwrap(), RecordType::TXT).await, Outcome::NoAnswer));
          assert!(started.elapsed() >= Duration::from_millis(400));
      }

      #[tokio::test(flavor = "current_thread")]
      async fn inflight_cap_gives_busy() {
          let group = responder(|_| vec![]).await;
          let gw = gateway(group, 200);
          let held: Vec<_> = (0..MAX_INFLIGHT).map(|_| InflightGuard::try_acquire().unwrap()).collect();
          let before = COUNTERS.dropped.load(std::sync::atomic::Ordering::Relaxed);
          assert!(matches!(gw.query(&Name::from_ascii("printer.local.").unwrap(), RecordType::A).await, Outcome::Busy));
          assert_eq!(COUNTERS.dropped.load(std::sync::atomic::Ordering::Relaxed), before + 1);
          drop(held);
          assert!(matches!(gw.query(&Name::from_ascii("printer.local.").unwrap(), RecordType::A).await, Outcome::NoAnswer));
      }
  }
  ```
  and to `engine/src/mdns/iface.rs`:
  ```rust
  #[cfg(test)]
  mod tests {
      #[test]
      fn loopback_is_found_and_unknown_is_not() {
          let lo = super::lookup("lo").expect("lo exists");
          assert!(lo.index > 0);
          assert_eq!(lo.v4, Some(std::net::Ipv4Addr::LOCALHOST));
          assert!(super::lookup("nxnope0").is_none());
      }
  }
  ```
  `InflightGuard` is the RAII guard of the static in-flight counter (`try_acquire() -> Option<InflightGuard>`),
  public within the crate.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib mdns::'` and expect FAIL:
      it does not compile (`cannot find struct Gateway`).
- [ ] Implement `iface.rs` with `nix::ifaddrs::getifaddrs()`:
  - first IPv4 address;
  - first IPv6 address in `fe80::/10`;
  - `nix::net::if_::if_nametoindex`;
  - `None` when the index lookup fails or neither address exists.
- [ ] Implement `gateway.rs`:
  - `Target::for_iface`:
    - v4 `{bind: (v4, 0), group: GROUP_V4, if_index}`;
    - v6 `{bind: ([v6 with scope], 0), group: [ff02::fb%index]:5353, if_index}`.
  - `query`:
    1. `InflightGuard::try_acquire()`, or count `dropped` and return `Busy`;
    2. build the query with a random ID from `rand::rng()`, RD=0, one question, QU bit clear;
    3. per target, a socket2 socket bound to `bind`, `set_multicast_if_v4` or `set_multicast_if_v6`,
       `set_multicast_ttl_v4(255)` or `set_multicast_hops_v6(255)`, `set_multicast_loop_v4(true)`,
       non-blocking, turned into `tokio::net::UdpSocket`;
    4. send to `group`;
    5. read from every socket until the deadline (`tokio::time::timeout_at`);
    6. accept a datagram only when it has at least 12 octets, QR=1, the ID, QDCOUNT 1 and the
       question equal case-insensitively;
    7. decode with `hickory_proto::op::Message::from_vec` and keep answers whose owner equals `qname`
       (case-insensitive) and whose type equals `qtype`;
    8. clear the cache-flush bit (class `& 0x7fff`) and set TTL `min(ttl, TTL_CAP)`;
    9. dedupe by (type, rdata) and return on the first answer when `unique_type`;
    10. count `answered` or `unanswered`.

    A target socket that fails to open is skipped (counted in no metric here; Task 21 reports missing
    interfaces).
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib mdns:: && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`
      and expect PASS.
- [ ] Report the paths. The lead commits `M8 T6: engine mDNS gateway core`.

## Task 7: Engine ODoH codec and proxy core

Files: `engine/src/server/odoh.rs`, `engine/Cargo.toml` (`odoh-rs = "=1.0.5"`), `Cargo.lock`
Interfaces: produces for Task 19:

```rust
pub const CONTENT_TYPE: &str = "application/oblivious-dns-message";
pub const CONFIGS_PATH: &str = "/.well-known/odohconfigs";
pub const MAX_BODY: usize = 65_535 + 1_024;
pub struct Keyring { /* key pairs by key id, publish_after, not_after */ }
impl Keyring {
    pub fn empty() -> Keyring;
    pub fn from_proto(k: &proto::OdohKeys) -> Result<Keyring, String>;
    pub fn configs(&self, now_unix: i64) -> Option<Bytes>; // None: no published key
}
#[derive(Default)]
pub struct OdohState { keys: ArcSwap<Keyring> }
impl OdohState { pub fn set_keys(&self, k: &proto::OdohKeys); pub fn keyring(&self) -> Arc<Keyring>; }
#[derive(Debug, PartialEq, Eq)]
pub enum Reject { UnknownKey, Malformed }
impl Reject { pub fn status(&self) -> http::StatusCode } // 401, 400
pub struct Opened { pub query: Vec<u8>, /* plaintext, secret */ }
pub fn open(keys: &Keyring, body: &[u8], now_unix: i64) -> Result<Opened, Reject>;
impl Opened { pub fn seal(self, response: &[u8]) -> Result<Vec<u8>, Reject>; }
pub struct ProxyTarget { pub host: String, pub port: u16, pub client: reqwest::Client }
pub struct ProxyConfig { pub targets: Vec<ProxyTarget>, pub timeout: Duration }
impl ProxyConfig {
    pub fn build(c: &proto::OdohConfig) -> Result<Option<ProxyConfig>, String>; // None when proxy off
    pub fn allowed(&self, targethost: &str) -> Option<&ProxyTarget>;
}
pub struct OdohRuntime { pub target_enabled: bool, pub proxy: Option<ProxyConfig> }
impl OdohRuntime { pub fn off() -> OdohRuntime; pub fn build(c: Option<&proto::OdohConfig>) -> Result<OdohRuntime, String>; }
pub fn parse_proxy_params(query: Option<&str>) -> Result<Option<(String, String)>, ()>; // Ok(None): no targethost
pub fn reject_response(r: Reject) -> Response<Full<Bytes>>;
pub fn proxy_error(status: StatusCode, error: &str) -> Response<Full<Bytes>>; // adds Proxy-Status: nexora; error=<error>
pub async fn forward(target: &ProxyTarget, targetpath: &str, body: Bytes, timeout: Duration) -> Response<Full<Bytes>>;
pub struct Counters { /* by role and status */ }
pub static COUNTERS: Counters; // rendered as nexora_odoh_requests_total{role,status}
impl Counters { pub fn count(&self, role: &'static str, status: StatusCode); pub fn render(&self, out: &mut String); }
```

- [ ] Add `odoh-rs = "=1.0.5"` to `[dependencies]` in `engine/Cargo.toml` and run
      `scripts/dev-exec.sh 'cargo update -p odoh-rs --precise 1.0.5 && cargo build --locked -p nexora-engine'`;
      copy `Cargo.lock` back with the `kubectl ... tar` command of Task 1. Expect a clean build. If the
      lock pulls a second `sha2` or `rand_core` major, record it in the report.
- [ ] Add to `engine/src/server/odoh.rs` this test module:
  ```rust
  #[cfg(test)]
  mod tests {
      use super::*;
      use crate::proto;
      use odoh_rs::{
          ObliviousDoHConfigs, ObliviousDoHMessage, ObliviousDoHMessagePlaintext, compose, decrypt_response, encrypt_query, parse,
      };

      fn keys(seeds: &[(u8, i64, i64)]) -> Keyring {
          Keyring::from_proto(&proto::OdohKeys {
              keys: seeds
                  .iter()
                  .map(|&(b, publish_after_unix, not_after_unix)| proto::OdohKey { seed: vec![b; 32], publish_after_unix, not_after_unix })
                  .collect(),
          })
          .unwrap()
      }

      fn client_query(configs: &Bytes, query: &[u8]) -> (Vec<u8>, ObliviousDoHMessagePlaintext, odoh_rs::OdohSecret) {
          let cfgs: ObliviousDoHConfigs = parse(&mut configs.clone()).unwrap();
          let cfg = cfgs.supported().into_iter().next().expect("a supported config");
          let plain = ObliviousDoHMessagePlaintext::new(query, 0);
          let (msg, secret) = encrypt_query(&plain, &cfg.into(), &mut rand::rng()).unwrap();
          (compose(&msg).unwrap().to_vec(), plain, secret)
      }

      #[test]
      fn target_round_trip() {
          let ring = keys(&[(7, 0, i64::MAX)]);
          let configs = ring.configs(1_000).expect("published");
          let query = b"\x12\x34\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x03www\x07example\x04test\x00\x00\x01\x00\x01";
          let (body, plain, secret) = client_query(&configs, query);
          let opened = open(&ring, &body, 1_000).unwrap();
          assert_eq!(opened.query, query.to_vec());
          let response = b"\x12\x34\x81\x80\x00\x01\x00\x00\x00\x00\x00\x00";
          let sealed = opened.seal(response).unwrap();
          let msg: ObliviousDoHMessage = parse(&mut Bytes::from(sealed)).unwrap();
          assert_eq!(decrypt_response(&plain, &msg, secret).unwrap().into_msg().to_vec(), response.to_vec());
      }

      #[test]
      fn rejections_map_to_status() {
          let ring = keys(&[(7, 0, 2_000), (8, 5_000, 9_000)]);
          let configs = ring.configs(1_000).unwrap();
          let cfgs: ObliviousDoHConfigs = parse(&mut configs.clone()).unwrap();
          assert_eq!(cfgs.supported().len(), 1, "a key before publish_after is not listed");
          let (body, _, _) = client_query(&configs, b"\x00\x01\x01\x00\x00\x01\x00\x00\x00\x00\x00\x00\x00\x00\x01\x00\x01");
          assert!(open(&ring, &body, 1_000).is_ok());
          assert_eq!(open(&ring, &body, 2_001).err(), Some(Reject::UnknownKey), "an expired key is unknown");
          let mut garbled = body.clone();
          let last = garbled.len() - 1;
          garbled[last] ^= 1;
          assert_eq!(open(&ring, &garbled, 1_000).err(), Some(Reject::Malformed));
          let mut other_key = body.clone();
          other_key[3] ^= 1; // inside the key id
          assert_eq!(open(&ring, &other_key, 1_000).err(), Some(Reject::UnknownKey));
          assert_eq!(open(&ring, b"\x01", 1_000).err(), Some(Reject::Malformed));
          assert_eq!(Reject::UnknownKey.status(), http::StatusCode::UNAUTHORIZED);
          assert_eq!(Reject::Malformed.status(), http::StatusCode::BAD_REQUEST);
          assert!(Keyring::empty().configs(1_000).is_none());
      }

      #[test]
      fn proxy_target_matching() {
          let cfg = ProxyConfig::build(&proto::OdohConfig {
              proxy_enabled: true,
              proxy_targets: vec![
                  proto::OdohProxyTarget { host: "odoh.example".into(), ca_pem: String::new() },
                  proto::OdohProxyTarget { host: "[2001:db8::1]:8443".into(), ca_pem: String::new() },
              ],
              ..Default::default()
          })
          .unwrap()
          .expect("proxy on");
          assert!(cfg.allowed("odoh.example").is_some());
          assert!(cfg.allowed("ODOH.example:443").is_some());
          assert!(cfg.allowed("odoh.example:8443").is_none());
          assert!(cfg.allowed("[2001:db8::1]:8443").is_some());
          assert!(cfg.allowed("evil.example").is_none());
          assert_eq!(
              parse_proxy_params(Some("targethost=odoh.example&targetpath=%2Fdns-query")),
              Ok(Some(("odoh.example".to_string(), "/dns-query".to_string())))
          );
          assert_eq!(parse_proxy_params(Some("dns=AAAB")), Ok(None));
          assert_eq!(parse_proxy_params(None), Ok(None));
          assert!(parse_proxy_params(Some("targethost=odoh.example")).is_err());
          assert!(parse_proxy_params(Some("targethost=odoh.example&targetpath=dns-query")).is_err());
          assert!(parse_proxy_params(Some("targethost=user%40odoh.example&targetpath=%2Fq")).is_err());
          assert!(parse_proxy_params(Some("targethost=odoh.example%2Fx&targetpath=%2Fq")).is_err());
          assert!(ProxyConfig::build(&proto::OdohConfig { proxy_enabled: true, ..Default::default() }).is_err());
          assert!(ProxyConfig::build(&proto::OdohConfig::default()).unwrap().is_none());
          let err = proxy_error(http::StatusCode::FORBIDDEN, "http_request_denied");
          assert_eq!(err.headers()["proxy-status"], "nexora; error=http_request_denied");
      }
  }
  ```
  Adapt the `odoh_rs` import paths to the crate's re-exports (`src/lib.rs` of odoh-rs 1.0.5) and the
  `OdohSecret` type path.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib server::odoh::tests'` and
      expect FAIL: it does not compile (`cannot find struct Keyring`).
- [ ] Implement:
  - `Keyring::from_proto`:
    - reject seeds that are not 32 octets;
    - `ObliviousDoHKeyPair::from_parameters(0x0020, 0x0001, 0x0001, &seed)`;
    - key id `pair.public().identifier()`;
    - keep the proto order (newest first).
  - `configs` composes an `ObliviousDoHConfigs` of the keys with
    `publish_after_unix <= now < not_after_unix`, newest first; `None` when empty.
  - `open`:
    1. parse `ObliviousDoHMessage`, else `Malformed`;
    2. find a key by `msg.key_id()` among keys with `now < not_after_unix`, else `UnknownKey`;
    3. `decrypt_query`, else `Malformed`;
    4. keep the plaintext and secret.
  - `seal`: a random `ResponseNonce` from `rand::rng()`, `encrypt_response` with padding 0, `compose`.
  - `ProxyConfig::build`:
    - `None` when `!proxy_enabled`; error when enabled without targets;
    - parse each host as `name`, `name:port`, or `[v6]:port` (lowercase, port 1..=65535, default 443);
    - reject userinfo, `/`, `?` and `#`;
    - build a `reqwest::Client` with `use_rustls_tls`, `http2_prior_knowledge` off,
      `redirect(Policy::none())`, the target CA via `upstream::doh`'s `client_tls_config(ca_pem)`
      (empty: webpki roots) and the `NEXORA_DOH_RESOLVE` pins (reuse the parsing in
      `upstream/doh.rs`; move it into a `pub(crate) fn resolve_pins()` there only if it is not
      reachable, and list that file in the report);
    - timeout `proxy_timeout_ms` (0 → 2,000).
  - `allowed` compares lowercase host and port.
  - `parse_proxy_params` percent-decodes both values. `targetpath` must start with `/`. `targethost`
    must parse as above.
  - `forward`:
    - POST `https://{host}:{port}{targetpath}` with `content-type` and `accept` set to
      `CONTENT_TYPE` and nothing else;
    - relay status and body with `content-type` from the target and
      `Proxy-Status: nexora; received-status=<code>`;
    - on error, `proxy_error(502, e)` with `connection_timeout` when `is_timeout()`,
      `tls_protocol_error` when the error chain holds a rustls error, otherwise
      `destination_unavailable`.
  - `COUNTERS` is a fixed array of `AtomicU64` keyed by role (`target`, `proxy`) and status class
    (200, 400, 401, 403, 404, 405, 413, 415, 502, 503, other).
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib server::odoh::tests && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`
      and expect PASS.
- [ ] Report the paths. The lead commits `M8 T7: engine ODoH codec and proxy core`.

## Task 8: Catalog zone codec

Files: `mgmt/internal/catzone/codec.go`, `mgmt/internal/catzone/codec_test.go` (both created)
Interfaces: produces for Task 15:

```go
package catzone
const Version = "2"
type Member struct { Label, Zone string } // Zone absolute, lowercase
type BrokenError struct { Reason string }
func (e *BrokenError) Error() string // "broken catalog: " + Reason
func Label(zoneID uuid.UUID) string     // 32 lowercase hex digits
func Build(catalog string, members []Member) []dns.RR // NS invalid., version TXT "2", PTRs; TTL 0; sorted by label
func Parse(catalog string, rrs []dns.RR) ([]Member, error) // members sorted by label, or *BrokenError
```

- [ ] Create `mgmt/internal/catzone/codec_test.go`:
  ```go
  package catzone

  import (
  	"errors"
  	"strings"
  	"testing"

  	"github.com/google/uuid"
  	"github.com/miekg/dns"
  )

  func rrs(t *testing.T, text string) []dns.RR {
  	t.Helper()
  	zp := dns.NewZoneParser(strings.NewReader(text), "cat.test.", "")
  	var out []dns.RR
  	for rr, ok := zp.Next(); ok; rr, ok = zp.Next() {
  		out = append(out, rr)
  	}
  	if err := zp.Err(); err != nil {
  		t.Fatal(err)
  	}
  	return out
  }

  func TestCatalogBuildIsStable(t *testing.T) {
  	id := uuid.MustParse("7f3d2c1b-0a09-4876-9543-210fedcba987")
  	if Label(id) != "7f3d2c1b0a0948769543210fedcba987" {
  		t.Fatalf("label %q", Label(id))
  	}
  	a := Build("cat.test.", []Member{{Label: "bb", Zone: "b.example."}, {Label: "aa", Zone: "a.example."}})
  	b := Build("cat.test.", []Member{{Label: "aa", Zone: "a.example."}, {Label: "bb", Zone: "b.example."}})
  	var sa, sb []string
  	for i := range a {
  		sa, sb = append(sa, a[i].String()), append(sb, b[i].String())
  	}
  	want := []string{
  		"cat.test.\t0\tIN\tNS\tinvalid.",
  		"version.cat.test.\t0\tIN\tTXT\t\"2\"",
  		"aa.zones.cat.test.\t0\tIN\tPTR\ta.example.",
  		"bb.zones.cat.test.\t0\tIN\tPTR\tb.example.",
  	}
  	if strings.Join(sa, "\n") != strings.Join(want, "\n") || strings.Join(sb, "\n") != strings.Join(want, "\n") {
  		t.Fatalf("build:\n%s\n%s", strings.Join(sa, "\n"), strings.Join(sb, "\n"))
  	}
  	got, err := Parse("cat.test.", a)
  	if err != nil || len(got) != 2 || got[0] != (Member{"aa", "a.example."}) {
  		t.Fatalf("round trip %v %v", got, err)
  	}
  }

  func TestCatalogParseBrokenRules(t *testing.T) {
  	valid := `@ 0 IN SOA invalid. invalid. 1 3600 600 86400 0
  @ 0 IN NS invalid.
  version 0 IN TXT "2"
  m1.zones 0 IN PTR A.Example.
  coo.m1.zones 0 IN PTR other.cat.
  group.m1.zones 0 IN TXT "blue"
  m2.zones 0 IN PTR b.example.
  foo.bar.ext 0 IN TXT "custom"
  unknown 0 IN A 192.0.2.1
  `
  	got, err := Parse("cat.test.", rrs(t, valid))
  	if err != nil || len(got) != 2 || got[0] != (Member{"m1", "a.example."}) || got[1] != (Member{"m2", "b.example."}) {
  		t.Fatalf("valid catalog: %v %v", got, err)
  	}
  	for _, c := range []struct{ name, text, reason string }{
  		{"no version", strings.Replace(valid, `version 0 IN TXT "2"`, "", 1), "version property missing"},
  		{"two versions", valid + "version 0 IN TXT \"1\"\n", "version property has 2 records"},
  		{"version 1", strings.Replace(valid, `TXT "2"`, `TXT "1"`, 1), `unsupported catalog version "1"`},
  		{"version two strings", strings.Replace(valid, `TXT "2"`, `TXT "2" "x"`, 1), `unsupported catalog version "2x"`},
  		{"two PTR at a member", valid + "m2.zones 0 IN PTR c.example.\n", "member node m2 has 2 PTR records"},
  		{"member under two labels", valid + "m3.zones 0 IN PTR a.example.\n", "member a.example. listed under 2 labels"},
  		{"two coo", valid + "coo.m1.zones 0 IN PTR third.cat.\n", "coo property of m1 has 2 records"},
  	} {
  		_, err := Parse("cat.test.", rrs(t, c.text))
  		var broken *BrokenError
  		if !errors.As(err, &broken) || broken.Reason != c.reason {
  			t.Fatalf("%s: %v, want %q", c.name, err, c.reason)
  		}
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/catzone -count=1'` and expect FAIL: the
      package does not compile (`undefined: Label`).
- [ ] Implement `codec.go`:
  - `Build`: sorted copy by label; records as in the test with TTL 0 and class IN.
  - `Parse`:
    - group records by lowercase owner;
    - `version.<catalog>` TXT: exactly one record whose strings joined equal `"2"`;
    - owners exactly one label below `zones.<catalog>` with PTR: exactly one PTR each, target
      lowercased;
    - `coo.<label>.zones.<catalog>` PTR: at most one (RFC 9432 §4.3.1; the value is ignored);
    - the same target under two labels is broken;
    - everything else is ignored;
    - members sorted by label.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/catzone -count=1'` and expect PASS.
- [ ] Report the paths. The lead commits `M8 T8: catalog zone codec`.

## Task 9: ODoH settings and keys in the management plane

Files: `mgmt/internal/odoh/settings.go`, `mgmt/internal/odoh/keys.go`, `mgmt/internal/odoh/odoh_test.go`
(all created)
Interfaces: produces for Tasks 16 and 22:

```go
package odoh
type ProxyTarget struct { Host string `json:"host"`; CAPEM string `json:"ca_pem"` }
type Settings struct {
	TargetEnabled, ProxyEnabled bool
	ProxyTargets                []ProxyTarget
	ProxyTimeoutMS, KeyRotationHours int32
	Revision                    int64
	UpdatedAt                   time.Time
}
type KeyInfo struct { ID uuid.UUID; CreatedAt, PublishAfter, NotAfter time.Time }
const (PublishDelay = 5 * time.Minute; SealPurpose = "odoh-seed"; ChannelKeys = "nexora_odoh_keys")
func Validate(s Settings) error // *ValidationError{Field, Message}
func GetSettings(ctx context.Context, q store.PolicyQuerier) (Settings, error)
func UpdateSettings(ctx context.Context, tx pgx.Tx, in Settings, revision int64) (Settings, error) // store.ErrConflict on a stale revision
func Config(s Settings) *controlv1.OdohConfig // nil when both roles are off
type Keys struct { Pool *pgxpool.Pool; Box *secrets.Box; Now func() time.Time }
func (k *Keys) List(ctx context.Context) ([]KeyInfo, error) // newest first, not expired
func (k *Keys) Rotate(ctx context.Context, force bool) (bool, error)
func (k *Keys) Load(ctx context.Context) (*controlv1.OdohKeys, string, error) // seeds unsealed; digest hex
func (k *Keys) Run(ctx context.Context, tick time.Duration) error
```

- [ ] Create `mgmt/internal/odoh/odoh_test.go`:
  ```go
  package odoh

  import (
  	"bytes"
  	"context"
  	"crypto/rand"
  	"encoding/base64"
  	"os"
  	"path/filepath"
  	"testing"
  	"time"

  	"github.com/piwi3910/nexora/mgmt/internal/secrets"
  	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
  )

  func testBox(t *testing.T) *secrets.Box {
  	t.Helper()
  	key := make([]byte, 32)
  	if _, err := rand.Read(key); err != nil {
  		t.Fatal(err)
  	}
  	p := filepath.Join(t.TempDir(), "kek")
  	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(key)), 0o600); err != nil {
  		t.Fatal(err)
  	}
  	box, err := secrets.Open(secrets.Config{KEKFile: p})
  	if err != nil {
  		t.Fatal(err)
  	}
  	return box
  }

  func TestOdohKeyRotation(t *testing.T) {
  	ctx := context.Background()
  	st := storetest.New(t)
  	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
  	k := &Keys{Pool: st.Pool, Box: testBox(t), Now: func() time.Time { return now }}
  	if rotated, err := k.Rotate(ctx, false); err != nil || rotated {
  		t.Fatalf("target off must not create keys: %v %v", rotated, err)
  	}
  	if _, err := st.Pool.Exec(ctx, `update odoh_settings set target_enabled = true, key_rotation_hours = 24`); err != nil {
  		t.Fatal(err)
  	}
  	if rotated, err := k.Rotate(ctx, false); err != nil || !rotated {
  		t.Fatalf("first key: %v %v", rotated, err)
  	}
  	if rotated, _ := k.Rotate(ctx, false); rotated {
  		t.Fatal("rotated again before the interval")
  	}
  	keys, digest1, err := k.Load(ctx)
  	if err != nil || len(keys.Keys) != 1 || len(keys.Keys[0].Seed) != 32 || digest1 == "" {
  		t.Fatalf("load: %v %v", keys, err)
  	}
  	if keys.Keys[0].PublishAfterUnix != now.Add(PublishDelay).Unix() || keys.Keys[0].NotAfterUnix != now.Add(48*time.Hour).Unix() {
  		t.Fatalf("timestamps %+v", keys.Keys[0])
  	}
  	var env []byte
  	if err := st.Pool.QueryRow(ctx, `select seed_envelope from odoh_keys`).Scan(&env); err != nil {
  		t.Fatal(err)
  	}
  	if !bytes.HasPrefix(env, []byte("NXE1")) || bytes.Contains(env, keys.Keys[0].Seed) {
  		t.Fatal("seed not sealed")
  	}
  	now = now.Add(25 * time.Hour)
  	if rotated, err := k.Rotate(ctx, false); err != nil || !rotated {
  		t.Fatalf("due rotation: %v %v", rotated, err)
  	}
  	keys, digest2, _ := k.Load(ctx)
  	if len(keys.Keys) != 2 || keys.Keys[0].PublishAfterUnix <= keys.Keys[1].PublishAfterUnix || digest2 == digest1 {
  		t.Fatalf("two keys newest first, new digest: %+v", keys.Keys)
  	}
  	now = now.Add(24 * time.Hour) // 49 h after the first key: expired
  	if rotated, _ := k.Rotate(ctx, false); !rotated {
  		t.Fatal("third rotation")
  	}
  	var n int
  	if err := st.Pool.QueryRow(ctx, `select count(*) from odoh_keys`).Scan(&n); err != nil || n != 2 {
  		t.Fatalf("expired key not deleted: %d %v", n, err)
  	}
  	if rotated, _ := k.Rotate(ctx, true); !rotated {
  		t.Fatal("forced rotation")
  	}
  	if infos, _ := k.List(ctx); len(infos) != 3 {
  		t.Fatalf("list: %v", infos)
  	}
  }

  func TestValidateOdohSettings(t *testing.T) {
  	ok := Settings{ProxyEnabled: true, ProxyTargets: []ProxyTarget{{Host: "odoh.example:8443"}}, ProxyTimeoutMS: 2000, KeyRotationHours: 24}
  	if err := Validate(ok); err != nil {
  		t.Fatal(err)
  	}
  	for name, s := range map[string]Settings{
  		"no targets": {ProxyEnabled: true, ProxyTimeoutMS: 2000, KeyRotationHours: 24},
  		"scheme":     {ProxyTargets: []ProxyTarget{{Host: "https://odoh.example"}}, ProxyTimeoutMS: 2000, KeyRotationHours: 24},
  		"port 0":     {ProxyTargets: []ProxyTarget{{Host: "odoh.example:0"}}, ProxyTimeoutMS: 2000, KeyRotationHours: 24},
  		"bad pem":    {ProxyTargets: []ProxyTarget{{Host: "odoh.example", CAPEM: "not pem"}}, ProxyTimeoutMS: 2000, KeyRotationHours: 24},
  		"timeout":    {ProxyTimeoutMS: 50, KeyRotationHours: 24},
  		"rotation":   {ProxyTimeoutMS: 2000, KeyRotationHours: 721},
  	} {
  		if Validate(s) == nil {
  			t.Fatalf("%s accepted", name)
  		}
  	}
  	if Config(Settings{}) != nil || Config(Settings{TargetEnabled: true}).GetTargetEnabled() != true {
  		t.Fatal("Config must be nil only when both roles are off")
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/odoh -count=1'` and expect FAIL: the package
      does not compile (`undefined: Keys`).
- [ ] Implement `settings.go`:
  - `Validate` enforces the migration ranges;
  - host syntax: `name`, `name:port` or `[v6]:port`; `url.Parse("https://"+host)` must give that
    host with no path, user or query; port 1..65535;
  - `ca_pem` empty or one or more PEM `CERTIFICATE` blocks that parse with `x509.ParseCertificate`;
  - `UpdateSettings` runs `update odoh_settings set ... revision = revision + 1, updated_at = now() where revision = $n returning ...`,
    with no row meaning `store.ErrConflict`;
  - `Config` copies the fields.
- [ ] Implement `keys.go`:
  - `Rotate` runs in one transaction:
    1. `select pg_try_advisory_xact_lock(hashtext('nexora:odoh-rotate'))`; false returns `(false, nil)`;
    2. read `target_enabled` and `key_rotation_hours`;
    3. `delete from odoh_keys where not_after <= $now`;
    4. unless forced, return false when target is off or a key has `created_at > now - rotation`;
    5. 32 random octets, `Box.Seal(SealPurpose, seed)`, `clear(seed)`;
    6. insert with `created_at = now`, `publish_after = now + PublishDelay`,
       `not_after = now + 2 × rotation`;
    7. `select pg_notify('nexora_odoh_keys', '')`, then commit.

    Deletion alone also notifies.

  - `Load` selects non-expired keys ordered `created_at desc`, unseals each, and computes the digest
    as the hub's `keySetDigest` does (SHA-256 of the deterministic encoding).
  - `Run` calls `Rotate(ctx, false)` every tick and logs errors without seeds.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/odoh -count=1 && go vet ./mgmt/internal/odoh'`
      and expect PASS.
- [ ] Report the paths. The lead commits `M8 T9: ODoH settings and rotating sealed keys`.

## Task 10: Network namespace lab and mDNS fixture

Files:

- `e2e/harness/netlab.go`, `e2e/harness/netlab_test.go`: created.
- `e2e/fixtures/cmd/nexora-fixture/mdns.go`: created.
- `e2e/fixtures/cmd/nexora-fixture/main.go`: two subcommands and usage lines.

Interfaces: produces for Task 26:

```go
package harness
// InNetLab reports whether this process runs inside the lab (NEXORA_NETLAB=1).
func InNetLab() bool
// RunInNetLab re-executes the current test (and only it) inside `unshare -Urn --kill-child`
// and fails t when the inner run fails; the caller returns right after.
func RunInNetLab(t *testing.T)
type NetLab struct { /* namespaces, links */ }
// NewNetLab brings up lo in the lab's root namespace (the "gateway" namespace).
func (e *Env) NewNetLab() *NetLab
// Namespace creates (once) a child network namespace named name, held by a sleeping process.
func (l *NetLab) Namespace(name string) int
// Link creates veth local<->peer, moves peer into namespace ns, sets 10.254.<subnet>.1/24 on local
// and .2/24 on peer, multicast on, both up; it returns the two IPv4 addresses.
func (l *NetLab) Link(local, peer, ns string, subnet int) (localIP, peerIP netip.Addr)
// StartIn runs a harness binary inside namespace ns (nsenter -t <pid> -n) and waits for its READY line.
func (l *NetLab) StartIn(ns, bin string, args ...string) *Proc
// RunIn runs a harness binary to completion inside namespace ns and returns its stdout.
func (l *NetLab) RunIn(ns, bin string, args ...string) string
// RunBin runs a harness binary to completion in the current namespace and returns its stdout.
func (e *Env) RunBin(t *testing.T, bin string, args ...string) string
```

The fixture subcommands:

- `nexora-fixture mdns-responder --interface IF --record "<zone file RR>"...`:
  - joins 224.0.0.251 and ff02::fb on IF;
  - answers each query whose question matches its records;
  - a query from a source port other than 5353 gets a unicast reply to the source, echoing ID and
    question, with the record TTLs as given and the cache-flush bit set for A, AAAA, SRV and TXT;
  - a query from port 5353 gets a multicast response with ID 0 on IF;
  - it prints `READY <IF address>`, logs `GOT <src> <qname> <qtype>` per query on stderr, and logs
    `ECHO` when it receives a response identical to one it sent.
- `nexora-fixture mdns-query --interface IF --name N --type T --wait D [--legacy]`:
  - sends one query (from port 5353 to the group, or with `--legacy` from an ephemeral port);
  - prints one `ANSWER <rr>` line per answer record and `PACKETS <n>` with the number of responses
    received within D;
  - exits 0.

- [ ] Create `e2e/harness/netlab_test.go`:
  ```go
  package harness

  import (
  	"strings"
  	"testing"
  )

  // Multicast crosses a veth link between two namespaces of the lab; nothing touches the pod network.
  func TestNetLabVethMulticast(t *testing.T) {
  	if !InNetLab() {
  		RunInNetLab(t)
  		return
  	}
  	e := New(t)
  	lab := e.NewNetLab()
  	_, peerIP := lab.Link("gw0", "lan0", "lanA", 0)
  	lab.StartIn("lanA", "nexora-fixture", "mdns-responder", "--interface", "lan0",
  		"--record", "printer.local. 120 IN A 10.254.0.9")
  	out := e.RunBin(t, "nexora-fixture", "mdns-query", "--interface", "gw0", "--name", "printer.local.",
  		"--type", "A", "--wait", "1s", "--legacy")
  	if !strings.Contains(out, "ANSWER printer.local.\t120\tIN\tA\t10.254.0.9") {
  		t.Fatalf("legacy query across the veth got:\n%s", out)
  	}
  	if peerIP.String() != "10.254.0.2" {
  		t.Fatalf("peer address %s", peerIP)
  	}
  	quiet := e.RunBin(t, "nexora-fixture", "mdns-query", "--interface", "gw0", "--name", "nothere.local.",
  		"--type", "A", "--wait", "500ms", "--legacy")
  	if !strings.Contains(quiet, "PACKETS 0") {
  		t.Fatalf("an unknown name was answered:\n%s", quiet)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e/harness -run TestNetLabVethMulticast -count=1 -v'`
      and expect FAIL: it does not compile (`undefined: InNetLab`).
- [ ] Implement `netlab.go`:
  - `RunInNetLab` runs
    `unshare -Urn --kill-child -- <os.Args[0]> -test.run '^<t.Name()>$' -test.v -test.count=1`
    with `NEXORA_NETLAB=1` and the parent environment, streams output through `t.Log`, and calls
    `t.Fatal` on a non-zero exit. Skip with `t.Skip` only when `unshare` is not in `PATH` (not in the
    dev pod).
  - `NewNetLab` runs `ip link set lo up`.
  - `Namespace` starts `unshare -n -- sleep infinity`, records the pid, brings up `lo` inside with
    `nsenter -t <pid> -n ip link set lo up`, and kills it at cleanup.
  - `Link` runs:
    - `ip link add <local> type veth peer name <peer>`;
    - `ip link set <peer> netns <pid>`;
    - `ip addr add 10.254.<subnet>.1/24 dev <local>`;
    - `ip link set <local> multicast on up`;
    - the same through nsenter for the peer with `.2/24`;
    - `ip route add 224.0.0.0/4 dev <peer>` inside the namespace, so a multicast sender there has a
      route.
  - `StartIn` wraps `Env.Start` with `nsenter -t <pid> -n --` in front of the binary path from
    `Env.Bin`.
- [ ] Implement `mdns.go` with `golang.org/x/net/ipv4` and `ipv6` packet conns:
  - `JoinGroup(iface, &net.UDPAddr{IP: 224.0.0.251})` and `ControlMessage` for the interface;
  - responses are built with miekg `dns.Msg`, and the cache-flush bit is set by OR-ing `0x8000` into
    `Hdr.Class` of each unique record;
  - `mdns-query` binds `0.0.0.0:5353` with `SO_REUSEADDR` (through `net.ListenConfig.Control`) unless
    `--legacy`, sets `SetMulticastInterface`, and counts responses whose question matches.

  Register both in `main.go` and extend `usage`.

- [ ] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e/harness -run TestNetLabVethMulticast -count=1 -v'`
      and expect PASS.
- [ ] Report the paths. The lead commits `M8 T10: network namespace lab and mDNS fixture`.

## Task 11: Harness ODoH client

Files: `e2e/harness/odoh.go`, `e2e/harness/odoh_test.go` (created), `go.mod`, `go.sum`
Interfaces: produces for Tasks 24 and 34:

```go
package harness
type ODoHConfig struct { KemID, KdfID, AeadID uint16; PublicKey []byte }
func FetchODoHConfigs(ctx context.Context, hc *http.Client, baseURL string) ([]ODoHConfig, *http.Response, error)
func ParseODoHConfigs(b []byte) ([]ODoHConfig, error)
func (c ODoHConfig) KeyID() []byte // RFC 9230 §6: Expand(Extract("", config), "odoh key id", Nh)
// ODoHQuery encrypts q for c, POSTs it to url (a target, or a proxy URL with targethost/targetpath),
// and decrypts a 200 response. On a non-200 status it returns the response and a nil message.
func ODoHQuery(ctx context.Context, hc *http.Client, url string, c ODoHConfig, q *dns.Msg) (*dns.Msg, *http.Response, error)
// ODoHRaw POSTs body with contentType and returns the response (for 400/401/415 checks).
func ODoHRaw(ctx context.Context, hc *http.Client, url, contentType string, body []byte) (*http.Response, error)
```

- [ ] Create `e2e/harness/odoh_test.go`:
  ```go
  package harness

  import (
  	"bytes"
  	"encoding/hex"
  	"testing"
  )

  // The client's config parsing and key id follow RFC 9230 §6, checked against a hand-built config.
  func TestODoHConfigParsingAndKeyID(t *testing.T) {
  	pub := bytes.Repeat([]byte{0x11}, 32)
  	contents := append([]byte{0x00, 0x20, 0x00, 0x01, 0x00, 0x01, 0x00, 0x20}, pub...)
  	config := append([]byte{0x00, 0x01, 0x00, byte(len(contents))}, contents...)
  	unknown := []byte{0x00, 0x02, 0x00, 0x02, 0xff, 0xff}
  	all := append(unknown, config...)
  	body := append([]byte{0x00, byte(len(all))}, all...)
  	cfgs, err := ParseODoHConfigs(body)
  	if err != nil || len(cfgs) != 1 || cfgs[0].KemID != 0x0020 || !bytes.Equal(cfgs[0].PublicKey, pub) {
  		t.Fatalf("parse: %+v %v", cfgs, err)
  	}
  	id := cfgs[0].KeyID()
  	if len(id) != 32 {
  		t.Fatalf("key id length %d", len(id))
  	}
  	// Expected value computed once with HKDF-SHA256 over the 40-octet contents (RFC 5869).
  	if hex.EncodeToString(id) != keyIDOf(contents) {
  		t.Fatalf("key id %x", id)
  	}
  	if _, err := ParseODoHConfigs([]byte{0x00, 0x09, 0x00}); err == nil {
  		t.Fatal("a truncated config list parsed")
  	}
  }

  func keyIDOf(contents []byte) string { return hex.EncodeToString(hkdfKeyID(contents)) }
  ```
  `hkdfKeyID(contents []byte) []byte` is an unexported helper in `odoh.go` built on the standard
  library `crypto/hkdf` (Go 1.24+): `Extract(sha256.New, contents, nil)`, then
  `Expand(sha256.New, prk, "odoh key id", 32)`. `KeyID` calls it with the serialised contents. The
  test pins that `KeyID` serialises exactly the `ObliviousDoHConfigContents` octets; the
  interoperability proof is Task 24 against the engine.
- [ ] Run `scripts/dev-exec.sh 'go test ./e2e/harness -run TestODoHConfigParsingAndKeyID -count=1'`
      and expect FAIL: it does not compile (`undefined: ParseODoHConfigs`).
- [ ] Run `go get github.com/cloudflare/circl@v1.6.5` on the laptop. Implement `odoh.go` with
      `github.com/cloudflare/circl/hpke`:
  - suite `hpke.NewSuite(hpke.KEM_X25519_HKDF_SHA256, hpke.KDF_HKDF_SHA256, hpke.AEAD_AES128GCM)`;
  - query: plaintext `u16 len | dns | u16 padlen (0)`;
    `aad = 0x01 | u16(len key_id) | key_id`;
    `sender.Setup(rand.Reader)` with info `"odoh query"`;
    `sealer.Seal(plaintext, aad)`;
    message `0x01 | u16 len key_id | key_id | u16 len(enc||ct) | enc||ct`;
  - the secret is `sealer.Export("odoh response", 16)`;
  - response:
    - parse `0x02 | u16 len nonce | nonce | u16 len ct | ct`;
    - `salt = Q_plain_serialised || u16(len nonce) || nonce`;
    - `prk = hkdf.Extract(sha256.New, secret, salt)`;
    - `key = Expand(prk, "odoh key", 16)`, `nonce = Expand(prk, "odoh nonce", 12)`;
    - AES-128-GCM open with `aad = 0x02 | u16(len nonce) | nonce`;
    - parse the plaintext and check the padding is all zero (RFC 9230 §6.2, §7).
  - Set `Content-Type` and `Accept` to `application/oblivious-dns-message`. Require that response
    content type before decrypting.
- [ ] Run `scripts/dev-exec.sh 'go test ./e2e/harness -run TestODoHConfigParsingAndKeyID -count=1 && go vet ./e2e/harness'`
      and expect PASS. Run `go mod tidy` on the laptop and confirm only circl was added.
- [ ] Report the paths. The lead commits `M8 T11: harness ODoH client (circl HPKE)`.

## Task 12: ZONEMD generation in zone rebuilds

Files: `mgmt/internal/zone/build.go`, `mgmt/internal/dnssec/store.go` (`SignZONEMD`),
`mgmt/internal/zone/zonemd_build_test.go` (created)
Interfaces: consumes `zonemd.Placeholder`, `zonemd.Apply` (Task 4) and `zone.Zone.ZonemdGenerate`
(Task 3); produces `(*dnssec.Store).SignZONEMD(ctx, tx, z, served, now) ([]dns.RR, error)`, which
re-signs only the apex ZONEMD RRset with the active ZSK and without touching the serial.

- [ ] Create `mgmt/internal/zone/zonemd_build_test.go` (package `zone_test`, using `storetest.New`, a
      `zone.Service` with a nil signer for the unsigned case and the `dnssec.Store` the existing
      `mgmt/internal/dnssec/store_test.go` builds for the signed case):
  ```go
  func TestRebuildAddsVerifiableZonemd(t *testing.T) {
  	for _, signed := range []bool{false, true} {
  		t.Run(fmt.Sprintf("signed=%v", signed), func(t *testing.T) {
  			ctx := context.Background()
  			svc, st := newZonemdService(t, signed) // zone.Service with Signer set when signed
  			z := createPrimary(t, svc, "zmd.test.")  // ns.zmd.test., one A record
  			if _, err := st.Pool.Exec(ctx, `update zones set zonemd_generate = true where id = $1`, z.ID); err != nil {
  				t.Fatal(err)
  			}
  			if signed {
  				enableSigning(t, svc, z.ID) // the helper dnssec tests use
  			}
  			for i := 0; i < 2; i++ {
  				if _, err := svc.CreateRecord(ctx, testActor, z.ID, zone.RecordInput{Name: fmt.Sprintf("r%d.zmd.test.", i), Type: "A", TTL: 60, Data: "192.0.2.1"}); err != nil {
  					t.Fatal(err)
  				}
  				served := loadServed(t, st, z.ID) // zone.LoadServed in a read-only tx
  				if r := zonemd.Verify("zmd.test.", served, zonemd.ModeRequired); r.Status != zonemd.StatusVerified {
  					t.Fatalf("version %d: %+v", i, r)
  				}
  				soa, md := apexSOA(served), apexZONEMD(served)
  				if md == nil || md.Serial != soa.Serial || md.Hash != zonemd.HashSHA384 || md.Hdr.Ttl != soa.Hdr.Ttl {
  					t.Fatalf("version %d: ZONEMD %v SOA %v", i, md, soa)
  				}
  				if signed {
  					if !nsecBitmapHas(served, "zmd.test.", dns.TypeZONEMD) {
  						t.Fatal("apex NSEC bitmap lacks ZONEMD")
  					}
  					if !rrsigValidates(t, served, "zmd.test.", dns.TypeZONEMD) {
  						t.Fatal("ZONEMD RRSIG missing or invalid")
  					}
  				}
  			}
  			deltaDeletesOldZonemd(t, st, z.ID) // newest zone_journal delta: old ZONEMD deleted, new added
  			if _, err := st.Pool.Exec(ctx, `update zones set zonemd_generate = false where id = $1`, z.ID); err != nil {
  				t.Fatal(err)
  			}
  			if _, err := svc.CreateRecord(ctx, testActor, z.ID, zone.RecordInput{Name: "off.zmd.test.", Type: "A", TTL: 60, Data: "192.0.2.2"}); err != nil {
  				t.Fatal(err)
  			}
  			if apexZONEMD(loadServed(t, st, z.ID)) != nil {
  				t.Fatal("ZONEMD kept after zonemd_generate was turned off")
  			}
  		})
  	}
  }
  ```
  Write the helpers (`newZonemdService`, `createPrimary`, `enableSigning`, `loadServed`, `apexSOA`,
  `apexZONEMD`, `nsecBitmapHas`, `rrsigValidates` using `(*dns.RRSIG).Verify` with the apex ZSK
  DNSKEY, `deltaDeletesOldZonemd` decoding the newest journal blob with `nzf.Decode`) in the same file.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/zone -run TestRebuildAddsVerifiableZonemd -count=1'`
      and expect FAIL: `version 0: {Status:failed Err:no apex ZONEMD}`.
- [ ] In `zone.Rebuild`, for `z.ZonemdGenerate`:
  - append `zonemd.Placeholder(z.Name, 0, z.SOA.TTL)` to `desired` before `Signer.Sign`;
  - after `setSOASerial` and `ResignSOA`, call `zonemd.Apply(z.Name, desired)`, then, for signed
    zones, `Signer.SignZONEMD`;
  - then `toRecords(desired)` as today.

  Extend `isSOAData` to apex ZONEMD records and RRSIGs whose type covered is ZONEMD, so the placeholder
  never counts as a change and deltas carry old and new ZONEMD with the SOA (`soaWithSigs` returns SOA,
  then the other SOA data). A zone without the flag is unchanged, and an unchanged zone still
  publishes nothing.

- [ ] Implement `SignZONEMD` in `dnssec/store.go`:
  - drop RRSIGs at the apex covering ZONEMD;
  - sign the apex ZONEMD RRset with the active ZSK through the same `signSet` path `Sign` uses, with
    the `zone_signatures` cache bypassed for this RRset, because the digest changes every version;
  - append the result.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/zone ./mgmt/internal/dnssec -count=1'` and
      expect PASS, including every existing test.
- [ ] Report the paths. The lead commits `M8 T12: ZONEMD generation for primary zones`.

## Task 13: ZONEMD verification of secondary transfers

Files: `mgmt/internal/xfrin/refresh.go`, `mgmt/internal/xfrin/refresh_test.go`,
`mgmt/internal/zone/zonemd_status.go` (created)
Interfaces: consumes `zonemd.Verify` (Task 4) and the zone fields (Task 3); produces
`zone.SetZonemdStatus(ctx context.Context, q Execer, zoneID uuid.UUID, status, errText string) error`.

- [ ] Add to `refresh_test.go` (reuse its fake primary and store setup):
  ```go
  func TestRefreshDiscardsTransferFailingZonemd(t *testing.T) {
  	for _, ixfr := range []bool{false, true} {
  		t.Run(fmt.Sprintf("ixfr=%v", ixfr), func(t *testing.T) {
  			env := newRefreshEnv(t) // fake primary serving zmd.test. with a valid ZONEMD at serial 1
  			env.load(t)             // first AXFR
  			if got := env.zone(t); got.ZonemdStatus != "verified" || got.Serial != 1 {
  				t.Fatalf("first load: %s serial %d", got.ZonemdStatus, got.Serial)
  			}
  			before := env.recordRows(t)
  			env.primary.setVersion(2, tamperedZonemd) // serial 2, a record added, ZONEMD digest of serial 1 with serial 2
  			env.primary.ixfr = ixfr
  			err := env.refresher.Refresh(context.Background(), env.zoneID, "manual")
  			if err == nil || !strings.Contains(err.Error(), "zonemd: ") {
  				t.Fatalf("refresh error %v", err)
  			}
  			got := env.zone(t)
  			if got.Serial != 1 || got.ZonemdStatus != "failed" || !strings.HasPrefix(got.LastError, "primary ") || !strings.Contains(got.LastError, "zonemd: digest mismatch") {
  				t.Fatalf("after failure: serial %d status %s error %q", got.Serial, got.ZonemdStatus, got.LastError)
  			}
  			if after := env.recordRows(t); after != before {
  				t.Fatalf("zone_records changed on a failed verification: %s -> %s", before, after)
  			}
  			env.primary.setVersion(3, validZonemd)
  			if err := env.refresher.Refresh(context.Background(), env.zoneID, "manual"); err != nil {
  				t.Fatal(err)
  			}
  			if got := env.zone(t); got.Serial != 3 || got.ZonemdStatus != "verified" {
  				t.Fatalf("recovery: serial %d status %s", got.Serial, got.ZonemdStatus)
  			}
  			env.setVerify(t, "required")
  			env.primary.setVersion(4, noZonemd)
  			if err := env.refresher.Refresh(context.Background(), env.zoneID, "manual"); err == nil {
  				t.Fatal("required accepted a zone without ZONEMD")
  			}
  			env.setVerify(t, "off")
  			if err := env.refresher.Refresh(context.Background(), env.zoneID, "manual"); err != nil {
  				t.Fatal(err)
  			}
  			if got := env.zone(t); got.Serial != 4 || got.ZonemdStatus != "off" {
  				t.Fatalf("off: serial %d status %s", got.Serial, got.ZonemdStatus)
  			}
  		})
  	}
  }
  ```
  Build `newRefreshEnv` on the fake primary the existing refresh tests use. The versions are
  generated with `zonemd.Apply`; `tamperedZonemd` keeps serial 1's digest but sets the ZONEMD serial
  to 2. `recordRows` is `string_agg(owner||rtype||rdata ORDER BY 1)` of `zone_records`.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/xfrin -run TestRefreshDiscardsTransferFailingZonemd -count=1'`
      and expect FAIL: `refresh error <nil>`.
- [ ] Implement:
  - in `succeed`, before `Zones.Mutate` and only when `ans != nil`, compute the resulting set:
    - `ans.Full` plus the primary's SOA;
    - or, for IXFR, `zone.LoadServed` (read-only transaction) with each diff applied through a new
      `applyDiffs(current []dns.RR, diffs []Diff) []dns.RR` keyed on `dns.RR.String()` of the
      lowercased copy, with SOA replaced by the diff's closing SOA;
  - `res := zonemd.Verify(z.Name, set, zonemd.Mode(z.ZonemdVerify))`;
  - on `StatusFailed`, `SetZonemdStatus(ctx, r.Store.Pool, z.ID, "failed", res.Err)` and return
    `fmt.Errorf("zonemd: %s", res.Err)`. `Refresh` records it through `fail` like any primary error,
    so the SOA retry and expiry stay;
  - otherwise set the status inside the `Mutate` callback (`SetZonemdStatus(ctx, tx, ...)`) with the
    result status;
  - an up-to-date refresh (`ans == nil`) leaves the status alone.

  `zonemd_status.go` holds the `UPDATE zones SET zonemd_status = $2, zonemd_error = $3 WHERE id = $1`
  helper.

- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/xfrin ./mgmt/internal/zone -count=1'` and
      expect PASS.
- [ ] Report the paths. The lead commits `M8 T13: ZONEMD verification of secondary transfers`.

## Task 14: Zone and RPZ API fields, catalog membership rules and hook points

Files:

- `mgmt/internal/zone/service.go`: input validation, the catalog hook, and the M7 incremental-edit
  condition.
- `mgmt/internal/api/zones.go`, `mgmt/internal/api/rpz.go`: request and response mapping.
- `mgmt/internal/store/resolution.go`, `mgmt/internal/snapshot/resolution.go`: the RPZ
  `zonemd_verify` into `RpzTransferSource`.
- `mgmt/internal/stats/resolution.go`: `engine_rpz_status.zonemd` and `zonemd_error` from
  `RpzZoneStatus`.
- `mgmt/internal/api/zonemd_api_test.go`: created.

Interfaces: produces for Tasks 15 and 22:

```go
// zone.Service gains:
CatalogChanged func(ctx context.Context, tx pgx.Tx, catalogZoneIDs []uuid.UUID) error
// zone errors gain:
var ErrCatalogManaged = errors.New("catalog_managed") // mapped to 409 catalog_managed by the API
```

`CreateZoneInTx` and `DeleteZoneInTx` (Task 3) stay free of the hook and the consumer check; only the
public `CreateZone`, `UpdateZone` and `DeleteZone` apply them.

- [ ] Create `mgmt/internal/api/zonemd_api_test.go` with `TestZonemdAPI` on the API test server the
      existing zone API tests use:
  ```go
  func TestZonemdAPI(t *testing.T) {
  	s := newAPITestServer(t) // admin client, operator client, viewer client, store
  	p := s.admin.createZone(t, `{"name":"p.test.","kind":"primary","soa":{"mname":"ns.p.test.","rname":"h.p.test."},"nameservers":["ns.p.test."],"zonemd_generate":true}`)
  	if p["zonemd_generate"] != true || p["zonemd_status"] != "not_checked" {
  		t.Fatalf("create: %v", p)
  	}
  	if code := s.admin.status(t, "POST", "/api/v1/zones", `{"name":"s.test.","kind":"secondary","primaries":[{"address":"127.0.0.1:53"}],"zonemd_generate":true}`); code != 400 {
  		t.Fatalf("zonemd_generate on a secondary: %d", code)
  	}
  	sec := s.admin.createZone(t, `{"name":"s.test.","kind":"secondary","primaries":[{"address":"127.0.0.1:53"}],"zonemd_verify":"required"}`)
  	if sec["zonemd_verify"] != "required" {
  		t.Fatalf("secondary verify: %v", sec)
  	}
  	if code := s.admin.status(t, "POST", "/api/v1/zones/"+p["id"].(string)+"/records", `{"name":"p.test.","type":"ZONEMD","ttl":60,"data":"1 1 1 00"}`); code != 400 {
  		t.Fatalf("ZONEMD record: %d", code)
  	}
  	if code := s.viewer.status(t, "PUT", "/api/v1/zones/"+p["id"].(string), `{"revision":1,"zonemd_generate":false}`); code != 403 {
  		t.Fatalf("viewer update: %d", code)
  	}
  	rpz := s.admin.createRPZTransfer(t, "rpz.test.", "127.0.0.1:53", `"zonemd_verify":"required"`)
  	snap := s.latestSnapshot(t)
  	if src := findRPZ(snap, rpz["id"].(string)).GetTransfer(); src.GetZonemdVerify() != controlv1.ZonemdVerify_ZONEMD_VERIFY_REQUIRED {
  		t.Fatalf("snapshot zonemd_verify %v", src.GetZonemdVerify())
  	}
  	s.recordRPZStats(t, rpz["id"].(string), controlv1.ZonemdStatus_ZONEMD_STATUS_FAILED, "digest mismatch")
  	got := s.admin.get(t, "/api/v1/rpz-zones")
  	if st := rpzStatus(got, rpz["id"].(string)); st["zonemd"] != "failed" || st["zonemd_error"] != "digest mismatch" {
  		t.Fatalf("rpz status %v", st)
  	}
  	auditHasNoSecret(t, s, "createZone", "updateRpzZone")
  }

  func TestCatalogMembershipRules(t *testing.T) {
  	s := newAPITestServer(t)
  	var calls [][]uuid.UUID
  	s.zones.CatalogChanged = func(_ context.Context, _ pgx.Tx, ids []uuid.UUID) error { calls = append(calls, ids); return nil }
  	prodA, prodB := s.insertCatalog(t, "a.cat.", "producer"), s.insertCatalog(t, "b.cat.", "producer")
  	z := s.admin.createZone(t, `{"name":"m.test.","kind":"primary","soa":{"mname":"ns.m.test.","rname":"h.m.test."},"nameservers":["ns.m.test."],"catalog_zone_id":"`+prodA.String()+`"}`)
  	s.admin.updateZone(t, z["id"].(string), `{"revision":1,"catalog_zone_id":"`+prodB.String()+`"}`)
  	s.admin.deleteZone(t, z["id"].(string))
  	want := [][]uuid.UUID{{prodA}, {prodA, prodB}, {prodB}}
  	if fmt.Sprint(calls) != fmt.Sprint(want) {
  		t.Fatalf("hook calls %v, want %v", calls, want)
  	}
  	consumer := s.insertCatalog(t, "c.cat.", "consumer")
  	member := s.insertMemberZone(t, "x.test.", consumer, "lbl") // secondary created by the consumer
  	if code := s.admin.status(t, "PUT", "/api/v1/zones/"+member.String(), `{"revision":1,"zonemd_verify":"off"}`); code != 409 {
  		t.Fatalf("update of a consumer member: %d", code)
  	}
  	if code := s.admin.status(t, "DELETE", "/api/v1/zones/"+member.String()+"?revision=1", ""); code != 409 {
  		t.Fatalf("delete of a consumer member: %d", code)
  	}
  	catZone := s.catalogZoneID(t, prodA)
  	if code := s.admin.status(t, "POST", "/api/v1/zones/"+catZone.String()+"/records", `{"name":"x.a.cat.","type":"TXT","ttl":0,"data":"\"x\""}`); code != 409 {
  		t.Fatalf("record edit of a producer catalog: %d", code)
  	}
  	if code := s.admin.status(t, "PUT", "/api/v1/zones/"+catZone.String(), `{"revision":1,"catalog_zone_id":"`+prodB.String()+`"}`); code != 400 {
  		t.Fatalf("catalog zone as a member: %d", code)
  	}
  }
  ```
  Write the missing helpers in the test file on top of the helpers the existing
  `mgmt/internal/api/api_test.go` provides.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api -run "TestZonemdAPI|TestCatalogMembershipRules" -count=1'`
      and expect FAIL: `create: map[...]` without `zonemd_generate`.
- [ ] Implement in `zone/service.go`:
  - **Create:**
    - `zonemd_generate` only for primary (invalid `zonemd_generate` "only primary zones generate
      ZONEMD");
    - `zonemd_verify` in the three values (secondary default `if_present`, primary stored
      `if_present` and ignored);
    - `catalog_zone_id` must name a producer catalog whose zone is not this zone (invalid
      `catalog_zone_id`);
    - the hook runs with `[id]` after insert and rebuild.
  - **Update:**
    - load the zone `FOR UPDATE`;
    - a zone with `catalog_member_label != ''` and a non-nil `CatalogZoneID` pointing at a consumer →
      `ErrCatalogManaged`;
    - a zone that is itself a catalog zone (a `catalog_zones` row) may not set `catalog_zone_id`
      (invalid);
    - a membership change calls the hook with the old and new ids (old first, nils dropped).
  - **Delete:** `ErrCatalogManaged` for consumer members; the hook runs with the old id before the
    delete.
  - **ZONEMD toggle:** an update that changes `zonemd_generate` rebuilds with
    `RebuildOptions{Force: true}`, so the digest appears or disappears at once.
  - **Records:** `ManagedTypes` stays without ZONEMD (already 400). A record mutation on a zone with a
    producer `catalog_zones` row gives `ErrCatalogManaged`.
  - **M7 incremental edits:** where M7 Task 17 chose the incremental delta path for unsigned primary
    zones, add `&& !z.ZonemdGenerate`. ZONEMD zones take the full rebuild, like signed zones.
- [ ] Map the fields in `api/zones.go` and `api/rpz.go` (create, update, get, list), and
      `ErrCatalogManaged` to 409 `catalog_managed` in the error mapper. Add `zonemd_verify` to
      `store.RPZZone` writes. In `snapshot/resolution.go`, set `RpzTransferSource.ZonemdVerify` from
      the row (`off` 1, `if_present` 2, `required` 3). In `stats/resolution.go`, write `zonemd`
      (enum to `off|absent|verified|failed`, unspecified → `off`) and `zonemd_error`. Return both in the
      RPZ status list.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api ./mgmt/internal/zone ./mgmt/internal/snapshot ./mgmt/internal/stats -count=1 && go vet ./mgmt/...'`
      and expect PASS.
- [ ] Report the paths. The lead commits `M8 T14: ZONEMD and catalog fields in the zone and RPZ API`.

## Task 15: Catalog service: producer regeneration and consumer reconciliation

Files: `mgmt/internal/catzone/service.go`, `mgmt/internal/catzone/service_test.go` (created),
`mgmt/internal/xfrin/scheduler.go` (`AfterRefresh`)
Interfaces: consumes Task 8's codec and Task 3's columns, `CreateZoneInTx` and `DeleteZoneInTx`;
produces for Task 22:

```go
package catzone
type Service struct { Store *store.Store; Zones *zone.Service; Build snapshot.BuildConfig; Now func() time.Time }
type MemberView struct { ZoneID *uuid.UUID; Name, Label, State, Issue string } // State "configured" | "clash"
type View struct {
	ID, ZoneID uuid.UUID; Name, Role string; EngineGroupID *uuid.UUID; BrokenReason string
	ProcessedSerial *int64; ProcessedAt *time.Time; Members []MemberView; CreatedAt time.Time
}
type CreateInput struct {
	Name, Role string; EngineGroupID *uuid.UUID
	Primaries []zone.Endpoint; Transfer zone.TransferInput; Notify []zone.Endpoint
}
func (s *Service) Create(ctx context.Context, actor auth.Actor, in CreateInput) (View, error)
func (s *Service) List(ctx context.Context) ([]View, error)
func (s *Service) Get(ctx context.Context, id uuid.UUID) (View, error)
func (s *Service) Delete(ctx context.Context, actor auth.Actor, id uuid.UUID) error
// Regenerate implements zone.Service.CatalogChanged: rewrites and rebuilds each producer catalog zone.
func (s *Service) Regenerate(ctx context.Context, tx pgx.Tx, catalogZoneIDs []uuid.UUID) error
// Reconcile processes a consumer catalog after its zone refreshed; other zone ids are ignored.
func (s *Service) Reconcile(ctx context.Context, zoneID uuid.UUID) error
// xfrin.Scheduler gains:
AfterRefresh func(ctx context.Context, zoneID uuid.UUID)
```

- [ ] Create `mgmt/internal/catzone/service_test.go`:
  ```go
  func TestProducerRegeneratesInTheSameVersion(t *testing.T) {
  	env := newCatEnv(t) // storetest store, zone.Service with CatalogChanged = svc.Regenerate
  	cat := env.createCatalog(t, catzone.CreateInput{Name: "catalog.test.", Role: "producer",
  		Transfer: zone.TransferInput{AllowCIDRs: []string{"127.0.0.1/32"}}})
  	if cat.Role != "producer" || len(cat.Members) != 0 {
  		t.Fatalf("create: %+v", cat)
  	}
  	v0 := env.latestVersion(t)
  	a := env.createMember(t, "a.test.", cat.ID)
  	if env.latestVersion(t) != v0+1 {
  		t.Fatal("member creation and catalog regeneration must publish one version")
  	}
  	rrs := env.served(t, cat.ZoneID)
  	members, err := catzone.Parse("catalog.test.", rrs)
  	if err != nil || len(members) != 1 || members[0] != (catzone.Member{Label: catzone.Label(a), Zone: "a.test."}) {
  		t.Fatalf("catalog after create: %v %v", members, err)
  	}
  	if q := env.allowQuery(t, cat.ZoneID); strings.Join(q, ",") != "127.0.0.1/32,::1/128" {
  		t.Fatalf("catalog allow-query %v", q)
  	}
  	env.deleteMember(t, a)
  	if members, _ := catzone.Parse("catalog.test.", env.served(t, cat.ZoneID)); len(members) != 0 {
  		t.Fatalf("member not removed: %v", members)
  	}
  }

  func TestConsumerReconcile(t *testing.T) {
  	env := newCatEnv(t)
  	cons := env.createCatalog(t, catzone.CreateInput{Name: "cat.remote.", Role: "consumer",
  		Primaries: []zone.Endpoint{{Address: "127.0.0.1:5399", TSIGKeyID: env.tsigKey(t)}}})
  	env.operatorZone(t, "c.remote.")
  	env.loadCatalog(t, cons, 1, `version 0 IN TXT "2"
  la.zones 0 IN PTR a.remote.
  lb.zones 0 IN PTR b.remote.
  lc.zones 0 IN PTR c.remote.`) // writes zone_records and serial as a refresh would
  	if err := env.svc.Reconcile(context.Background(), cons.ZoneID); err != nil {
  		t.Fatal(err)
  	}
  	v := env.get(t, cons.ID)
  	a, b := env.zoneByName(t, "a.remote."), env.zoneByName(t, "b.remote.")
  	if a.Kind != "secondary" || a.CatalogMemberLabel != "la" || *a.CatalogZoneID != cons.ID || a.Primaries[0].TSIGKeyID == nil || b == nil {
  		t.Fatalf("members not created: %+v %+v", a, b)
  	}
  	if !hasMember(v, "c.remote.", "clash") || env.zoneByName(t, "c.remote.").CatalogZoneID != nil {
  		t.Fatalf("clash not recorded or operator zone touched: %+v", v.Members)
  	}
  	if !env.refreshRequested(t, a.ID) {
  		t.Fatal("no refresh requested for a new member")
  	}
  	oldA := a.ID
  	env.loadCatalog(t, cons, 2, `version 0 IN TXT "2"
  la2.zones 0 IN PTR a.remote.`)
  	must(t, env.svc.Reconcile(context.Background(), cons.ZoneID))
  	if env.zoneByName(t, "b.remote.") != nil {
  		t.Fatal("removed member b.remote. still exists")
  	}
  	if na := env.zoneByName(t, "a.remote."); na == nil || na.ID == oldA || na.CatalogMemberLabel != "la2" {
  		t.Fatalf("label change did not recreate a.remote.: %+v", na)
  	}
  	env.loadCatalog(t, cons, 3, `version 0 IN TXT "1"`)
  	must(t, env.svc.Reconcile(context.Background(), cons.ZoneID))
  	if v := env.get(t, cons.ID); v.BrokenReason != `unsupported catalog version "1"` || env.zoneByName(t, "a.remote.") == nil {
  		t.Fatalf("broken catalog changed members or kept no reason: %+v", v)
  	}
  	env.expire(t, cons.ZoneID)
  	env.loadCatalog(t, cons, 4, `version 0 IN TXT "2"`)
  	must(t, env.svc.Reconcile(context.Background(), cons.ZoneID))
  	if env.zoneByName(t, "a.remote.") == nil {
  		t.Fatal("an expired catalog was processed")
  	}
  	env.unexpire(t, cons.ZoneID)
  	must(t, env.svc.Reconcile(context.Background(), cons.ZoneID))
  	if env.zoneByName(t, "a.remote.") != nil || env.get(t, cons.ID).BrokenReason != "" {
  		t.Fatal("processing did not resume")
  	}
  	must(t, env.svc.Delete(context.Background(), testActor, cons.ID))
  	if env.auditCount(t, "reconcileCatalogZone") < 3 {
  		t.Fatal("reconciliations not audited")
  	}
  }
  ```
  Write the helpers in the same file on `storetest.New`. `loadCatalog` uses `zone.SetRecords` and
  `zone.Rebuild` with `RebuildOptions{Serial: &serial}` as `xfrin` does.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/catzone -run "TestProducer|TestConsumer" -count=1'`
      and expect FAIL: it does not compile (`svc.Reconcile undefined`).
- [ ] Implement `service.go`:
  - **Create** (one `snapshot.Mutate` transaction):
    - producer: `CreateZoneInTx` with kind primary, SOA `invalid.`/`hostmaster.invalid.`,
      nameservers `["invalid."]`, `allow_query_cidrs` `["127.0.0.1/32","::1/128"]`, the transfer and
      notify input (at least one transfer CIDR, else invalid `transfer.allow_cidrs`), default TTL 0;
      then insert `catalog_zones`, `Regenerate(tx, [id])`;
    - consumer: `CreateZoneInTx` with kind secondary and the primaries (at least one), then insert the
      row and `zone.RequestRefresh(tx, zoneID, "create")`.
    - Audit `createCatalogZone`.
  - **Regenerate**, per id, ordered, skipping non-producers:
    - `pg_advisory_xact_lock(hashtext('catalog:'||id))`;
    - select members `zones where catalog_zone_id = id order by id`;
    - `zone.SetRecords(tx, catalogZoneID, Build(name, members))`;
    - load the zone `FOR UPDATE` and `zone.Rebuild(ctx, tx, s.Zones.Signer, z, RebuildOptions{}, now)`.
  - **Reconcile:**
    1. return when the zone has no consumer row;
    2. `BEGIN`, advisory lock, load catalog row and zone;
    3. skip when `expired` or `!loaded`, or when `processed_serial = serial` and `broken_reason = ''`;
    4. `LoadServed` and `Parse`: on `*BrokenError` set `broken_reason`, `processed_serial` and
       `processed_at`, then commit;
    5. otherwise diff the members against `zones where catalog_zone_id = id`:
       - a new name that is free → `CreateZoneInTx` (secondary, catalog's primaries JSON, group,
         `catalog_zone_id`, label) plus `RequestRefresh(…, "catalog")`;
       - a name taken by a zone not owned by this catalog, or equal to the catalog name → clash row;
       - a label differing for an owned name → `DeleteZoneInTx` then create;
       - an owned name no longer listed → `DeleteZoneInTx`;
    6. replace `catalog_member_issues`;
    7. clear `broken_reason` and set `processed_serial` and `processed_at`;
    8. one `snapshot.Mutate`-equivalent publish when any zone changed, audited
       `reconcileCatalogZone` by `system:catzone` with `{created, deleted, recreated, clashes}`.
  - **Delete:** `update zones set catalog_zone_id = null, catalog_member_label = '' where catalog_zone_id = id`,
    delete the catalog's own zone (`DeleteZoneInTx`, which cascades the row), audit
    `deleteCatalogZone`.
  - In `xfrin/scheduler.go` `refreshLocked`, after `Refresher.Refresh` returns nil, call
    `s.AfterRefresh(ctx, id)` when set.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/catzone ./mgmt/internal/xfrin -count=1 && go vet ./mgmt/...'`
      and expect PASS.
- [ ] Report the paths. The lead commits `M8 T15: catalog producer regeneration and consumer reconciliation`.

## Task 16: ODoH in the management plane: API, key push, snapshot

Files:

- `mgmt/internal/api/odoh.go`: replaces the stub.
- `mgmt/internal/api/odoh_service.go`: created, the adapter from `odoh` to `api.ODoHService`.
- `mgmt/internal/api/odoh_test.go`: created.
- `mgmt/internal/control/hub.go`: `Hub.ODoH`, LISTEN, offer.
- `mgmt/internal/control/keys_odoh.go`, `mgmt/internal/control/keys_odoh_test.go`: created.
- `mgmt/internal/snapshot/odoh.go`: created.
- `mgmt/internal/snapshot/snapshot.go`: the `AddOdoh` call.

Interfaces: consumes Task 9; produces:

```go
// api
func NewODoHService(st *store.Store, keys *odoh.Keys, build snapshot.BuildConfig) ODoHService
// control
type ODoHKeyLoader interface { Load(ctx context.Context) (*controlv1.OdohKeys, string, error) }
// Hub gains: ODoH ODoHKeyLoader
func (s *subscriber) offerOdohKeys(k *controlv1.OdohKeys, digest string)
// snapshot
func AddOdoh(ctx context.Context, tx pgx.Tx, snap *controlv1.ConfigSnapshot) error // sets snap.Odoh = odoh.Config(settings)
```

- [ ] Create `mgmt/internal/api/odoh_test.go`:
  ```go
  func TestOdohSettingsAPI(t *testing.T) {
  	s := newAPITestServer(t) // with Deps.ODoH = NewODoHService(...) and a KEK box
  	got := s.viewer.get(t, "/api/v1/odoh")
  	if got["target_enabled"] != false || len(got["keys"].([]any)) != 0 {
  		t.Fatalf("defaults: %v", got)
  	}
  	v0 := s.latestVersion(t)
  	if code := s.viewer.status(t, "PUT", "/api/v1/odoh", `{"target_enabled":true,"proxy_enabled":false,"proxy_targets":[],"proxy_timeout_ms":2000,"key_rotation_hours":24,"revision":1}`); code != 403 {
  		t.Fatalf("viewer update: %d", code)
  	}
  	if code := s.operator.status(t, "PUT", "/api/v1/odoh", `{"target_enabled":false,"proxy_enabled":true,"proxy_targets":[],"proxy_timeout_ms":2000,"key_rotation_hours":24,"revision":1}`); code != 400 {
  		t.Fatalf("proxy without targets: %d", code)
  	}
  	up := s.operator.put(t, "/api/v1/odoh", `{"target_enabled":true,"proxy_enabled":true,"proxy_targets":[{"host":"odoh.example:8443","ca_pem":""}],"proxy_timeout_ms":1500,"key_rotation_hours":12,"revision":1}`)
  	if up["revision"].(float64) != 2 {
  		t.Fatalf("update: %v", up)
  	}
  	snap := s.latestSnapshot(t)
  	if s.latestVersion(t) != v0+1 || !snap.GetOdoh().GetTargetEnabled() || snap.GetOdoh().GetProxyTargets()[0].GetHost() != "odoh.example:8443" || snap.GetOdoh().GetProxyTimeoutMs() != 1500 {
  		t.Fatalf("snapshot odoh %v", snap.GetOdoh())
  	}
  	if code := s.operator.status(t, "PUT", "/api/v1/odoh", `{"target_enabled":true,"proxy_enabled":false,"proxy_targets":[],"proxy_timeout_ms":2000,"key_rotation_hours":24,"revision":1}`); code != 409 {
  		t.Fatalf("stale revision: %d", code)
  	}
  	if code := s.operator.status(t, "POST", "/api/v1/odoh/rotate-key", ""); code != 403 {
  		t.Fatalf("operator rotate: %d", code)
  	}
  	rot := s.admin.post(t, "/api/v1/odoh/rotate-key", "")
  	if len(rot["keys"].([]any)) != 1 {
  		t.Fatalf("rotate: %v", rot)
  	}
  	s.admin.put(t, "/api/v1/odoh", `{"target_enabled":false,"proxy_enabled":false,"proxy_targets":[],"proxy_timeout_ms":2000,"key_rotation_hours":24,"revision":2}`)
  	if s.latestSnapshot(t).Odoh != nil {
  		t.Fatal("odoh config kept with both roles off")
  	}
  	auditHasNoSecret(t, s, "updateOdohSettings", "rotateOdohKey") // no seed, no envelope in audit rows
  }
  ```
  and `mgmt/internal/control/keys_odoh_test.go`:
  ```go
  // A key rotation reaches every connected engine without a new config version; an unchanged set is not resent.
  func TestHubOffersOdohKeysOnNotify(t *testing.T) {
  	env := newHubEnv(t)                // the fake engine stream helpers the hub tests use
  	loader := &fakeOdohLoader{}        // returns keys and a digest from its fields
  	env.hub.ODoH = loader
  	eng := env.connectEngine(t, "e1")
  	loader.set([]byte("seed-1"), "d1")
  	env.notify(t, "nexora_odoh_keys")
  	if k := eng.nextOdohKeys(t, 5*time.Second); string(k.Keys[0].Seed) != "seed-1" {
  		t.Fatalf("keys %v", k)
  	}
  	env.notify(t, "nexora_odoh_keys")
  	if eng.hasOdohKeys(500 * time.Millisecond) {
  		t.Fatal("an unchanged key set was resent")
  	}
  	loader.set([]byte("seed-2"), "d2")
  	env.notify(t, "nexora_odoh_keys")
  	if k := eng.nextOdohKeys(t, 5*time.Second); string(k.Keys[0].Seed) != "seed-2" {
  		t.Fatalf("rotated keys %v", k)
  	}
  }
  ```
  Build `newHubEnv` and the stream helpers from the existing hub tests (the RPZ key offer tests).
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api -run TestOdohSettingsAPI -count=1; go test ./mgmt/internal/control -run TestHubOffersOdohKeysOnNotify -count=1'`
      and expect FAIL: 501 on `GET /api/v1/odoh`, and `hub.ODoH undefined`.
- [ ] Implement:
  - `snapshot/odoh.go` reads `odoh.GetSettings` and sets `snap.Odoh = odoh.Config(s)`; call
    `AddOdoh` from `BuildForGroup` in `snapshot.go` next to the access-control fields.
  - `api/odoh_service.go` adapts:
    - `Get` = settings + `keys.List`;
    - `Update` = `odoh.Validate` (400 `invalid_request` with the field), then
      `snapshot.Mutate(ctx, st, build, actor, func(tx) { odoh.UpdateSettings(...) })` with audit
      `updateOdohSettings` holding the settings (no keys);
    - `Rotate` = `keys.Rotate(ctx, true)`, audit `rotateOdohKey`, 503
      `key_storage_unavailable` when the box is not configured.
  - In `hub.go`:
    - add `ChannelOdohKeys = odoh.ChannelKeys` to the LISTEN list;
    - on that channel load once and `offerOdohKeys` to every subscriber;
    - in `offerTarget`, also offer the current set;
    - `offerOdohKeys` keeps a digest per subscriber and sends `ServerMessage{OdohKeys}` only on a
      change, like `offerKeys`;
    - an engine with no keys and an empty set gets nothing.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api ./mgmt/internal/control ./mgmt/internal/snapshot -count=1 && go vet ./mgmt/...'`
      and expect PASS.
- [ ] Report the paths. The lead commits `M8 T16: ODoH settings API, key push and snapshot config`.

## Task 17: mDNS settings in the management plane

Files: `mgmt/internal/api/fleet_groups.go`, `mgmt/internal/fleet/groups.go` (`ValidateMdns`),
`mgmt/internal/snapshot/mdns.go` (created), `mgmt/internal/snapshot/snapshot.go` (the `AddMdns` call),
`mgmt/internal/api/engine_group_mdns_test.go` (created)
Interfaces: consumes Task 3's `fleet.EngineGroup` mDNS fields and Task 2's `MdnsSettings`; produces:

```go
func ValidateMdns(g EngineGroup) error // fleet: interfaces required when enabled, 2+ reflect interfaces, names 1-15 of [A-Za-z0-9_.:@-], no duplicates, timeout 100..5000
func AddMdns(snap *controlv1.ConfigSnapshot, g fleet.EngineGroup) // snapshot: sets snap.Mdns only when enabled || reflect
```

- [ ] Create `mgmt/internal/api/engine_group_mdns_test.go`:
  ```go
  func TestEngineGroupMdnsAPI(t *testing.T) {
  	s := newAPITestServer(t)
  	body := func(mdns string) string { return `{"name":"lan","mdns":` + mdns + `}` }
  	for name, m := range map[string]string{
  		"no interfaces":       `{"enabled":true,"interfaces":[],"timeout_ms":500,"reflect":false,"reflect_interfaces":[]}`,
  		"one reflect iface":   `{"enabled":false,"interfaces":[],"timeout_ms":500,"reflect":true,"reflect_interfaces":["eth0"]}`,
  		"bad interface name":  `{"enabled":true,"interfaces":["eth0/1"],"timeout_ms":500,"reflect":false,"reflect_interfaces":[]}`,
  		"timeout too small":   `{"enabled":true,"interfaces":["eth0"],"timeout_ms":50,"reflect":false,"reflect_interfaces":[]}`,
  		"duplicate interface": `{"enabled":false,"interfaces":[],"timeout_ms":500,"reflect":true,"reflect_interfaces":["eth0","eth0"]}`,
  	} {
  		if code := s.operator.status(t, "POST", "/api/v1/engine-groups", body(m)); code != 400 {
  			t.Fatalf("%s: %d", name, code)
  		}
  	}
  	if code := s.viewer.status(t, "POST", "/api/v1/engine-groups", body(`{"enabled":false,"interfaces":[],"timeout_ms":500,"reflect":false,"reflect_interfaces":[]}`)); code != 403 {
  		t.Fatalf("viewer create: %d", code)
  	}
  	g := s.operator.post(t, "/api/v1/engine-groups", body(`{"enabled":true,"interfaces":["vlan10"],"timeout_ms":700,"reflect":true,"reflect_interfaces":["vlan10","vlan20"]}`))
  	if m := g["mdns"].(map[string]any); m["enabled"] != true || m["timeout_ms"].(float64) != 700 {
  		t.Fatalf("create echo %v", g["mdns"])
  	}
  	snap := s.groupSnapshot(t, g["id"].(string))
  	if mc := snap.GetMdns(); !mc.GetEnabled() || mc.GetInterfaces()[0] != "vlan10" || mc.GetTimeoutMs() != 700 || len(mc.GetReflectInterfaces()) != 2 {
  		t.Fatalf("group snapshot mdns %v", mc)
  	}
  	if s.groupSnapshot(t, "00000000-0000-0000-0000-000000000001").Mdns != nil {
  		t.Fatal("the default group got an mdns config")
  	}
  	s.operator.put(t, "/api/v1/engine-groups/"+g["id"].(string), `{"name":"lan","description":"renamed","revision":1}`)
  	if !s.groupSnapshot(t, g["id"].(string)).GetMdns().GetEnabled() {
  		t.Fatal("an update without mdns reset the settings")
  	}
  	s.operator.put(t, "/api/v1/engine-groups/"+g["id"].(string), `{"name":"lan","revision":2,"mdns":{"enabled":false,"interfaces":[],"timeout_ms":500,"reflect":false,"reflect_interfaces":[]}}`)
  	if s.groupSnapshot(t, g["id"].(string)).Mdns != nil {
  		t.Fatal("mdns config kept after turning both features off")
  	}
  	auditHas(t, s, "updateEngineGroup", "mdns")
  }
  ```
  Use the API test server and snapshot helpers the fleet API tests use (`fleet_lifecycle_test.go`);
  add `groupSnapshot` there only if missing, in this test file.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api -run TestEngineGroupMdnsAPI -count=1'` and
      expect FAIL: `no interfaces: 201`.
- [ ] Implement:
  - `fleet.ValidateMdns`, called from `CreateEngineGroup` and `UpdateEngineGroup` before writing
    (400 `invalid_request` with the field `mdns.interfaces` and so on);
  - `fleet_groups.go` maps `mdns` in and out; an update without `mdns` keeps the stored values;
    create defaults to everything off, timeout 500;
  - `snapshot/mdns.go` `AddMdns`;
  - `BuildForGroup` calls `AddMdns(snap, group)` where it already reads the group row.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api ./mgmt/internal/fleet ./mgmt/internal/snapshot -count=1 && go vet ./mgmt/...'`
      and expect PASS.
- [ ] Report the paths. The lead commits `M8 T17: engine group mDNS settings`.

## Task 18: Engine mDNS reflector

Files: `engine/src/mdns/reflector.rs`
Interfaces: consumes `mdns::iface::IfaceTarget` (Task 6); produces for Task 21:

```rust
pub const DEDUPE_WINDOW: Duration = Duration::from_secs(1);
pub const DEDUPE_ENTRIES: usize = 1024;
pub struct Dedupe { /* ring of (digest, Instant) */ }
impl Dedupe { pub fn new() -> Dedupe; pub fn admit(&mut self, payload: &[u8], now: Instant) -> bool; }
pub fn from_local(src: IpAddr, ifaces: &[IfaceTarget]) -> bool;
pub struct Reflector { /* task handles */ }
impl Reflector {
    pub fn start(rt: &tokio::runtime::Handle, ifaces: Vec<IfaceTarget>) -> std::io::Result<Reflector>;
    pub fn stop(self);
}
pub struct ReflectCounters; pub static REFLECTED: ReflectCounters;
impl ReflectCounters { pub fn render(&self, out: &mut String); } // nexora_mdns_reflected_packets_total{from,to}
```

- [ ] Add this test module to `reflector.rs`:
  ```rust
  #[cfg(test)]
  mod tests {
      use super::*;
      use crate::mdns::iface::IfaceTarget;
      use std::time::{Duration, Instant};

      #[test]
      fn dedupe_and_source_filter() {
          let mut d = Dedupe::new();
          let t0 = Instant::now();
          assert!(d.admit(b"packet-a", t0));
          assert!(!d.admit(b"packet-a", t0 + Duration::from_millis(900)), "an echo inside the window is dropped");
          assert!(d.admit(b"packet-b", t0 + Duration::from_millis(900)));
          assert!(d.admit(b"packet-a", t0 + Duration::from_millis(2100)), "after the window the payload passes again");
          for i in 0..(DEDUPE_ENTRIES * 2) {
              assert!(d.admit(format!("flood-{i}").as_bytes(), t0 + Duration::from_secs(3)));
          }
          assert!(d.len() <= DEDUPE_ENTRIES, "the table stays bounded");
          let ifaces = vec![
              IfaceTarget { name: "gwA".into(), index: 5, v4: Some("10.254.1.1".parse().unwrap()), v6_link_local: Some("fe80::1".parse().unwrap()) },
              IfaceTarget { name: "gwB".into(), index: 6, v4: Some("10.254.2.1".parse().unwrap()), v6_link_local: None },
          ];
          assert!(from_local("10.254.2.1".parse().unwrap(), &ifaces));
          assert!(from_local("fe80::1".parse().unwrap(), &ifaces));
          assert!(!from_local("10.254.1.2".parse().unwrap(), &ifaces));
      }
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib mdns::reflector'` and expect
      FAIL: it does not compile (`cannot find struct Dedupe`).
- [ ] Implement:
  - `Dedupe`:
    - a `VecDeque<(u64, Instant)>` bounded to `DEDUPE_ENTRIES` (pop the oldest when full);
    - the digest is `std::hash::BuildHasher::hash_one` of a `RandomState` created once per reflector;
    - `admit` prunes entries older than the window, then rejects a present digest or records it;
    - `len()` for the test.
  - `Reflector::start`, per interface and family with an address:
    - a socket2 socket with `set_reuse_address`, `set_reuse_port` and `bind_device(Some(name))`, bound
      to `0.0.0.0:5353` or `[::]:5353`;
    - `join_multicast_v4(&224.0.0.251, &iface_v4)` or `join_multicast_v6(&ff02::fb, index)`;
    - `set_multicast_loop_v4(false)` or `set_multicast_loop_v6(false)`; TTL and hops 255;
    - `set_multicast_if_v4(&iface_v4)` or `set_multicast_if_v6(index)`.

    One tokio task per socket receives datagrams. It drops those whose source is `from_local`, whose
    destination port is not 5353, or which the shared `Mutex<Dedupe>` (control runtime only, never a
    query worker) rejects. It sends the payload unchanged to the group from each other interface's
    socket of the same family, and increments `REFLECTED{from,to}`. Socket errors are logged at most
    once per minute per interface.

  - `stop` aborts the tasks.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib mdns:: && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`
      and expect PASS.
- [ ] Report the paths. The lead commits `M8 T18: engine mDNS reflector`.

## Task 19: Engine ODoH integration

Files:

- `engine/src/server/doh.rs`: ODoH target, configs and proxy paths.
- `engine/src/server/mod.rs`: `Shared.odoh` and the `run_doh` arguments.
- `engine/src/control.rs`: apply `OdohKeys`.
- `engine/src/snapshot.rs`: validate the `odoh` config.
- `engine/src/runtime.rs`: `Runtime.odoh`.
- `engine/src/telemetry/metrics.rs`: render `nexora_odoh_requests_total`.
- `engine/tests/odoh_pipeline.rs`, `engine/tests/common/mod.rs`: created (the helpers below; when
  a shared test helper module already exists at the M7 head, add the helpers there instead and name
  it in the report).

Interfaces: consumes Task 7; produces
`doh::handle(answerer, client, doh_path, odoh: &OdohRuntime, keys: &OdohState, req)`,
`Shared.odoh: server::odoh::OdohState`, and `Runtime.odoh: Arc<server::odoh::OdohRuntime>`.

- [ ] Create `engine/tests/odoh_pipeline.rs`:
  ```rust
  //! ODoH through the DoH handler: configs, target, rejections, proxy statuses.
  use bytes::Bytes;
  use http::{Method, Request, StatusCode};
  use nexora_engine::proto;
  use nexora_engine::server::doh;
  use nexora_engine::server::odoh::{CONFIGS_PATH, CONTENT_TYPE, OdohRuntime, OdohState};

  mod common; // the fake Answerer used by the existing DoH tests (engine/tests/upstream_encrypted.rs or server testutil)

  fn post(uri: &str, ct: &str, body: Vec<u8>) -> Request<http_body_util::Full<Bytes>> {
      Request::builder().method(Method::POST).uri(uri).header("content-type", ct).body(http_body_util::Full::new(Bytes::from(body))).unwrap()
  }

  #[tokio::test(flavor = "current_thread")]
  async fn odoh_paths_through_the_doh_handler() {
      let answerer = common::StaticAnswerer::a("www.example.test.", "192.0.2.10");
      let keys = OdohState::default();
      keys.set_keys(&proto::OdohKeys { keys: vec![proto::OdohKey { seed: vec![9; 32], publish_after_unix: 0, not_after_unix: i64::MAX }] });
      let off = OdohRuntime::off();
      let target = OdohRuntime::build(Some(&proto::OdohConfig { target_enabled: true, ..Default::default() })).unwrap();
      let client = common::client_info("192.0.2.50:5000");

      // Target off: configs 404, an ODoH POST 415 as before M8.
      let r = doh::handle(&answerer, client, "/dns-query", &off, &keys, common::get(CONFIGS_PATH)).await;
      assert_eq!(r.status(), StatusCode::NOT_FOUND);
      let r = doh::handle(&answerer, client, "/dns-query", &off, &keys, post("/dns-query", CONTENT_TYPE, vec![1, 2, 3])).await;
      assert_eq!(r.status(), StatusCode::UNSUPPORTED_MEDIA_TYPE);

      // Target on: configs, a round trip, rejections.
      let r = doh::handle(&answerer, client, "/dns-query", &target, &keys, common::get(CONFIGS_PATH)).await;
      assert_eq!(r.status(), StatusCode::OK);
      let configs = common::body(r).await;
      let (body, plain, secret) = common::odoh_client_query(&configs, &common::query("www.example.test.", 1));
      let r = doh::handle(&answerer, client, "/dns-query", &target, &keys, post("/dns-query", CONTENT_TYPE, body.clone())).await;
      assert_eq!(r.status(), StatusCode::OK);
      assert_eq!(r.headers()["content-type"], CONTENT_TYPE);
      assert_eq!(r.headers()["cache-control"], "no-store");
      let answer = common::odoh_client_open(&plain, secret, &common::body(r).await);
      assert!(common::has_a(&answer, "192.0.2.10"));
      let mut garbled = body.clone();
      let n = garbled.len() - 1;
      garbled[n] ^= 1;
      let r = doh::handle(&answerer, client, "/dns-query", &target, &keys, post("/dns-query", CONTENT_TYPE, garbled)).await;
      assert_eq!(r.status(), StatusCode::BAD_REQUEST);
      keys.set_keys(&proto::OdohKeys { keys: vec![proto::OdohKey { seed: vec![10; 32], publish_after_unix: 0, not_after_unix: i64::MAX }] });
      let r = doh::handle(&answerer, client, "/dns-query", &target, &keys, post("/dns-query", CONTENT_TYPE, body)).await;
      assert_eq!(r.status(), StatusCode::UNAUTHORIZED, "a rotated-away key is unknown");
      let r = doh::handle(&answerer, client, "/dns-query", &target, &keys, post("/dns-query", "application/json", vec![0; 20])).await;
      assert_eq!(r.status(), StatusCode::UNSUPPORTED_MEDIA_TYPE);

      // Proxy off: a targethost request is denied; proxy on: unlisted 403, missing targetpath 400, unreachable 502.
      let r = doh::handle(&answerer, client, "/dns-query", &target, &keys, post("/dns-query?targethost=a.test&targetpath=%2Fq", CONTENT_TYPE, vec![1])).await;
      assert_eq!(r.status(), StatusCode::FORBIDDEN);
      let proxy = OdohRuntime::build(Some(&proto::OdohConfig {
          proxy_enabled: true,
          proxy_targets: vec![proto::OdohProxyTarget { host: format!("127.0.0.1:{}", common::closed_port()), ca_pem: String::new() }],
          proxy_timeout_ms: 500,
          ..Default::default()
      }))
      .unwrap();
      let listed = format!("/dns-query?targethost=127.0.0.1:{}&targetpath=%2Fdns-query", common::closed_port());
      let r = doh::handle(&answerer, client, "/dns-query", &proxy, &keys, post("/dns-query?targethost=evil.test&targetpath=%2Fq", CONTENT_TYPE, vec![1])).await;
      assert_eq!((r.status(), r.headers()["proxy-status"].to_str().unwrap()), (StatusCode::FORBIDDEN, "nexora; error=http_request_denied"));
      let r = doh::handle(&answerer, client, "/dns-query", &proxy, &keys, post("/dns-query?targethost=evil.test", CONTENT_TYPE, vec![1])).await;
      assert_eq!((r.status(), r.headers()["proxy-status"].to_str().unwrap()), (StatusCode::BAD_REQUEST, "nexora; error=http_request_error"));
      let r = doh::handle(&answerer, client, "/dns-query", &proxy, &keys, post(&listed, CONTENT_TYPE, vec![1])).await;
      assert_eq!((r.status(), r.headers()["proxy-status"].to_str().unwrap()), (StatusCode::BAD_GATEWAY, "nexora; error=destination_unavailable"));
  }
  ```
  `closed_port()` binds and drops a TCP listener and returns its port; `odoh_client_query` and
  `odoh_client_open` use `odoh_rs` as the Task 7 unit tests do.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test odoh_pipeline'` and expect
      FAIL: it does not compile (`doh::handle` takes 4 arguments).
- [ ] Implement:
  - `Runtime.odoh: Arc<OdohRuntime>` built with `OdohRuntime::build(s.odoh.as_ref())` in
    `Runtime::build_with`, with `OdohRuntime::off()` in `Runtime::initial`.
  - `snapshot::validate` maps `OdohRuntime::build` errors to `SnapshotError` with the prefix `odoh: `.
  - `Shared.odoh: OdohState` defaults to empty.
  - In `control.rs`, replace the M8 Task 1 no-op arm with `Some(ServerMsg::OdohKeys(k)) => shared.odoh.set_keys(&k)`
    and remove the `debt:` comment.
  - In `doh::handle_inner`, before the path check:
    1. `GET CONFIGS_PATH` → 404 when `!odoh.target_enabled`, 503 when `keys.keyring().configs(now)` is
       `None`, else 200 with `content-type: application/octet-stream`, `cache-control: max-age=300`;
    2. on the DoH path, a request whose query has `targethost` →
       - 403 `http_request_denied` when `odoh.proxy` is `None`;
       - 405 unless POST;
       - 415 unless `CONTENT_TYPE`;
       - `parse_proxy_params` error → 400 `http_request_error`;
       - `allowed` `None` or client outside `rt.acl` (recursion ACL of the loaded runtime) → 403;
       - 413 above `MAX_BODY`;
       - otherwise `odoh::forward`;
    3. a POST with `CONTENT_TYPE` → 415 when `!odoh.target_enabled`; else read at most `MAX_BODY`
       (413), `open` (reject → status), answer the query through `answerer.answer`, `seal`, and
       return 200 with `CONTENT_TYPE` and `cache-control: no-store`;
    4. everything else is unchanged.

    Count each outcome in `odoh::COUNTERS`.

  - Pass `&rt.odoh` and `&shared.odoh` from the listener loop in `server/mod.rs`, loading the runtime
    per request as the answerer does.
  - Render `odoh::COUNTERS` in `metrics.rs`.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test odoh_pipeline && cargo test --locked -p nexora-engine --test upstream_encrypted && cargo test --locked -p nexora-engine --test snapshot_apply && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`
      and expect PASS.
- [ ] Report the paths. The lead commits `M8 T19: engine ODoH target and proxy on DoH listeners`.

## Task 20: Engine RPZ ZONEMD verification

Files: `engine/src/recursor/rpz/manager.rs`, `engine/src/recursor/rpz/rpz_tests.rs` (or the manager
test module the M7 head uses), `engine/src/snapshot_m3.rs` (accept the enum)
Interfaces: consumes `zonemd::{verify, VerifyMode, Verdict}` (Task 5); `RpzZoneConfig` gains
`zonemd_verify: VerifyMode`; `RpzZoneStatus` carries `zonemd` and `zonemd_error`.

- [ ] Add to the RPZ manager tests:
  ```rust
  #[tokio::test(flavor = "current_thread")]
  async fn rpz_transfer_failing_zonemd_keeps_last_good() {
      let dir = tempfile::tempdir().unwrap();
      let primary = FakeRpzPrimary::start(zone_with_zonemd(1, &["bad.example CNAME ."], true)).await; // valid digest
      let mgr = manager_with(dir.path(), transfer_config(primary.addr(), VerifyMode::IfPresent));
      mgr.refresh_now("z").await;
      let st = mgr.status("z");
      assert_eq!((st.serial, st.zonemd), (1, proto::ZonemdStatus::Verified as i32));
      assert!(mgr.blocks("bad.example."));
      let persisted = std::fs::read(dir.path().join("rpz/z.zone")).unwrap();

      primary.set(zone_with_zonemd(2, &["bad.example CNAME .", "worse.example CNAME ."], false)).await; // stale digest
      mgr.refresh_now("z").await;
      let st = mgr.status("z");
      assert_eq!(st.serial, 1, "the failing version is not applied");
      assert_eq!(st.zonemd, proto::ZonemdStatus::Failed as i32);
      assert!(st.zonemd_error.contains("digest") || st.zonemd_error.contains("serial"), "{}", st.zonemd_error);
      assert!(!mgr.blocks("worse.example."));
      assert_eq!(std::fs::read(dir.path().join("rpz/z.zone")).unwrap(), persisted, "the last good copy is kept on disk");

      let mgr = manager_with(dir.path(), transfer_config(primary.addr(), VerifyMode::Required));
      primary.set(zone_without_zonemd(3, &["worse.example CNAME ."])).await;
      mgr.refresh_now("z").await;
      assert!(!mgr.blocks("worse.example."), "required refuses a zone without ZONEMD");
      let mgr = manager_with(dir.path(), transfer_config(primary.addr(), VerifyMode::Off));
      mgr.refresh_now("z").await;
      assert!(mgr.blocks("worse.example."), "off applies it");
      assert_eq!(mgr.status("z").zonemd, proto::ZonemdStatus::Off as i32);
  }
  ```
  Build `FakeRpzPrimary`, `manager_with`, `transfer_config`, `zone_with_zonemd` (computing the digest
  with `crate::zonemd::digest`; `false` keeps the previous version's digest bytes) and
  `zone_without_zonemd` from the existing RPZ transfer test fakes. If the manager exposes different
  names for `refresh_now`, `status` or `blocks`, use those and update this step.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib rpz_transfer_failing_zonemd_keeps_last_good'`
      and expect FAIL: `the failing version is not applied` (left 2, right 1).
- [ ] Implement:
  - `zone_configs` reads `RpzTransferSource.zonemd_verify` via `VerifyMode::from_proto`;
  - the transfer task, after `transfer::transfer` returns `ZoneData`, calls
    `zonemd::verify(&cfg.origin, &data.records, cfg.zonemd_verify)`;
  - `Verdict::Failed(e)` records `zonemd` failed and the error, keeps the current data, schedules
    the SOA retry and does not persist;
  - any other verdict applies as today and records the verdict;
  - `RpzZoneStatus` fills `zonemd` and `zonemd_error` from `Verdict::to_proto`;
  - an up-to-date check keeps the last verdict.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib rpz && cargo test --locked -p nexora-engine --test rpz_pipeline && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`
      and expect PASS.
- [ ] Report the paths. The lead commits `M8 T20: RPZ transfer ZONEMD verification`.

## Task 21: Engine mDNS integration and hot-path proof

Files:

- `engine/src/mdns/mod.rs`: runtime config, validation, reflector state and answer synthesis.
- `engine/src/recursor/dispatch.rs`: `Route::Mdns`, `ResolutionRuntime.mdns`, the resolve branch.
- `engine/src/snapshot.rs`: `mdns` validation and reflector sync on apply.
- `engine/src/runtime.rs`: carries the built mDNS config.
- `engine/src/server/mod.rs`: `Shared.mdns: mdns::MdnsState`.
- `engine/src/telemetry/metrics.rs`: the three mDNS metric families.
- `engine/tests/hot_path_alloc.rs`: the M8 snapshot in the existing allocation tests.
- `engine/tests/mdns_pipeline.rs`: created.
- `engine/tests/common/mod.rs`: the mDNS helpers, added next to Task 19's.

Interfaces: consumes Tasks 6, 18 and 19 (`snapshot.rs`, `runtime.rs` and `metrics.rs` as Task 19 left
them); produces:

```rust
// mdns/mod.rs
pub struct MdnsRuntime { pub gateway: Option<Arc<gateway::Gateway>>, pub reflect: Vec<iface::IfaceTarget>, pub reflect_key: String, pub missing: Vec<String> }
impl MdnsRuntime {
    pub fn off() -> MdnsRuntime;
    pub fn build(c: Option<&proto::MdnsConfig>) -> Result<MdnsRuntime, String>; // validates, resolves interfaces now
}
pub fn validate(c: &proto::MdnsConfig) -> Result<(), String>;
#[derive(Default)]
pub struct MdnsState { /* Mutex<Option<(String, reflector::Reflector)>>, missing interfaces */ }
impl MdnsState {
    pub fn sync(&self, rt: &MdnsRuntime, handle: &tokio::runtime::Handle);
    pub fn render(&self, out: &mut String);
}
pub fn is_local_name(qname_wire_lower: &[u8]) -> bool;
pub fn answer(q: &MissQuery<'_>, outcome: gateway::Outcome) -> MissAnswer; // NOERROR answers / NXDOMAIN / SERVFAIL
// recursor::dispatch
pub enum Route<'a> { Forward, Recursive, ForwardZone(&'a ForwardZone), Mdns }
// ResolutionRuntime gains: pub mdns: Option<Arc<crate::mdns::gateway::Gateway>>
```

- [ ] Create `engine/tests/mdns_pipeline.rs`:
  ```rust
  //! `.local` names through the full query pipeline: forward zones first, then the gateway.
  use std::sync::Arc;
  use std::sync::atomic::{AtomicUsize, Ordering};
  use std::time::Duration;

  use hickory_proto::op::ResponseCode;
  use hickory_proto::rr::{RData, RecordType};
  use nexora_engine::mdns::gateway::{Gateway, Target};

  mod common;

  #[test]
  fn local_names_route_to_the_gateway_after_forward_zones() {
      let upstream_hits = Arc::new(AtomicUsize::new(0));
      let upstream = common::fake_upstream_a(upstream_hits.clone(), "192.0.2.53");
      let responder_hits = Arc::new(AtomicUsize::new(0));
      let responder = common::unicast_mdns_responder(responder_hits.clone(), &[("printer.local.", [10, 254, 0, 9])]);
      let gw = Arc::new(Gateway::new(
          vec![Target { bind: "127.0.0.1:0".parse().unwrap(), group: responder, if_index: 0 }],
          Duration::from_millis(300),
      ));
      // Engine with global upstream `upstream`, a forward zone kw.local. -> `upstream`, and the gateway injected.
      let (srv, shared) = common::start_engine_with(upstream, "127.0.0.0/8", |snap| {
          snap.forward_zones.push(common::forward_zone("kw.local.", upstream));
      });
      common::inject_gateway(&shared, Some(gw.clone()));

      let r = common::ask(srv, "printer.local.", RecordType::A);
      assert_eq!(r.metadata.response_code, ResponseCode::NoError);
      assert!(matches!(r.answers[0].data(), RData::A(a) if a.0 == std::net::Ipv4Addr::new(10, 254, 0, 9)));
      assert!(r.answers[0].ttl() <= 10);
      assert!(!r.metadata.authoritative);
      assert_eq!(upstream_hits.load(Ordering::SeqCst), 0, "a .local name never reaches the upstream");

      let r = common::ask(srv, "host.kw.local.", RecordType::A);
      assert!(matches!(r.answers[0].data(), RData::A(a) if a.0 == std::net::Ipv4Addr::new(192, 0, 2, 53)), "forward zone wins");
      assert_eq!(responder_hits.load(Ordering::SeqCst), 1, "the forward zone name did not reach the gateway");

      for round in 0..2 {
          let r = common::ask(srv, "nothere.local.", RecordType::A);
          assert_eq!(r.metadata.response_code, ResponseCode::NXDomain, "round {round}");
      }
      assert_eq!(responder_hits.load(Ordering::SeqCst), 3, "NXDOMAIN without SOA is not cached");

      common::inject_gateway(&shared, None);
      let r = common::ask(srv, "other.local.", RecordType::A);
      assert!(matches!(r.answers[0].data(), RData::A(a) if a.0 == std::net::Ipv4Addr::new(192, 0, 2, 53)), "without mDNS .local is forwarded");
  }
  ```
  Put the helpers in `engine/tests/common/mod.rs`, next to Task 19's:
  - `start_engine_with` is the `server_pipeline.rs` engine starter with a snapshot hook;
  - `inject_gateway` stores a runtime clone whose `resolution.mdns` is replaced;
  - `unicast_mdns_responder` answers legacy queries as Task 6's test responder does.
- [ ] Add to `engine/src/mdns/mod.rs`:
  ```rust
  #[cfg(test)]
  mod tests {
      use super::*;
      use crate::proto::MdnsConfig;

      #[test]
      fn validate_rules() {
          let ok = MdnsConfig { enabled: true, interfaces: vec!["lo".into()], timeout_ms: 0, reflect: false, reflect_interfaces: vec![] };
          assert!(validate(&ok).is_ok());
          for bad in [
              MdnsConfig { interfaces: vec![], ..ok.clone() },
              MdnsConfig { interfaces: vec!["eth0/1".into()], ..ok.clone() },
              MdnsConfig { interfaces: vec!["a-very-long-interface".into()], ..ok.clone() },
              MdnsConfig { timeout_ms: 99, ..ok.clone() },
              MdnsConfig { timeout_ms: 5001, ..ok.clone() },
              MdnsConfig { reflect: true, reflect_interfaces: vec!["lo".into()], ..ok.clone() },
          ] {
              assert!(validate(&bad).is_err(), "{bad:?}");
          }
          let rt = MdnsRuntime::build(Some(&MdnsConfig { interfaces: vec!["lo".into(), "nxnope0".into()], ..ok })).unwrap();
          assert!(rt.gateway.is_some());
          assert_eq!(rt.missing, vec!["nxnope0".to_string()]);
          assert!(MdnsRuntime::build(None).unwrap().gateway.is_none());
          assert!(is_local_name(b"\x07printer\x05local\x00") && is_local_name(b"\x05local\x00"));
          assert!(!is_local_name(b"\x05local\x04test\x00") && !is_local_name(b"\x08notlocal\x00"));
      }
  }
  ```
- [ ] In `engine/tests/hot_path_alloc.rs`, set in the snapshot `cache_hit_path_does_not_allocate` and
      `authoritative_answer_path_does_not_allocate` apply:
      `mdns: Some(proto::MdnsConfig { enabled: true, interfaces: vec!["lo".into()], timeout_ms: 100, ..Default::default() })`
      and `odoh: Some(proto::OdohConfig { target_enabled: true, ..Default::default() })`.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test mdns_pipeline; cargo test --locked -p nexora-engine --lib mdns::tests'`
      and expect FAIL: it does not compile (`no field mdns on ResolutionRuntime`).
- [ ] Implement:
  - `validate` and `MdnsRuntime::build`:
    - the gateway targets from `iface::lookup` for each interface; missing names are recorded and
      skipped;
    - a gateway with zero targets still exists and answers NXDOMAIN at once;
    - the timeout is `timeout_ms` or 500;
    - the reflect list is resolved the same way, with `reflect_key` the joined names.
  - `snapshot::validate` calls `mdns::validate` when `mdns` is set (prefix `mdns: `).
    `ResolutionRuntime::build` takes `mdns: MdnsRuntime::build(s.mdns.as_ref())?.gateway`, and
    `Runtime` keeps the whole `MdnsRuntime` for the reflector.
  - `route()` returns `Route::Mdns` when no forward zone matches, `self.mdns.is_some()` and
    `is_local_name`. `validates(&Route::Mdns)` is false. `route_taken` maps it to the forwarded route
    label used for answers by route (`forwarded`).
  - In `resolve_name`, `Route::Mdns` →
    `mdns::answer(q, gateway.query(&q.qname, q.qtype).await)`:
    - answers: NOERROR, AA=0, RA as usual, the gateway records, cacheable;
    - `NoAnswer`: NXDOMAIN with no SOA, not cacheable;
    - `Busy`: SERVFAIL, `failed = true` without serve-stale.
  - `snapshot::apply_with` calls `shared.mdns.sync(&rt.mdns, &control_handle)` after the runtime swap.
    `MdnsState::sync` stops and starts the reflector only when `reflect_key` changed, and records
    `missing` for `nexora_mdns_interface_missing`.
  - Render `gateway::COUNTERS` as `nexora_mdns_queries_total{result}`, `reflector::REFLECTED`, and
    the missing gauge in `metrics.rs`.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test mdns_pipeline && cargo test --locked -p nexora-engine --lib mdns:: && cargo test --locked -p nexora-engine --test hot_path_alloc && cargo test --locked -p nexora-engine --test server_pipeline && cargo test --locked -p nexora-engine --test snapshot_apply && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings'`
      and expect PASS.
- [ ] Report the paths. The lead commits `M8 T21: mDNS route, reflector lifecycle and hot-path proof`.

## Task 22: Catalog zone API and management plane wiring

Files: `mgmt/internal/api/catalog_zones.go` (replaces the stub), `mgmt/internal/api/catalog_zone_service.go`
(created adapter), `mgmt/internal/api/catalog_zones_test.go` (created), `mgmt/cmd/nexora-mgmt/main.go`
Interfaces: consumes Tasks 9, 14, 15 and 16; produces
`func NewCatalogZoneService(s *catzone.Service) CatalogZoneService` and the running wiring:

- `zones.CatalogChanged = cat.Regenerate`;
- `scheduler.AfterRefresh = cat.Reconcile` (errors logged);
- `hub.ODoH = odohKeys`;
- `go odohKeys.Run(ctx, time.Minute)`;
- `Deps.CatalogZones` and `Deps.ODoH`.

- [ ] Create `mgmt/internal/api/catalog_zones_test.go`:
  ```go
  func TestCatalogZonesAPI(t *testing.T) {
  	s := newAPITestServer(t) // with Deps.CatalogZones and zone.Service.CatalogChanged wired as main.go does
  	if code := s.operator.status(t, "POST", "/api/v1/catalog-zones", `{"name":"catalog.test.","role":"producer"}`); code != 400 {
  		t.Fatalf("producer without transfer CIDRs: %d", code)
  	}
  	if code := s.operator.status(t, "POST", "/api/v1/catalog-zones", `{"name":"cat.remote.","role":"consumer"}`); code != 400 {
  		t.Fatalf("consumer without primaries: %d", code)
  	}
  	if code := s.viewer.status(t, "POST", "/api/v1/catalog-zones", `{"name":"catalog.test.","role":"producer","transfer":{"allow_cidrs":["127.0.0.1/32"]}}`); code != 403 {
  		t.Fatalf("viewer create: %d", code)
  	}
  	prod := s.operator.post(t, "/api/v1/catalog-zones", `{"name":"catalog.test.","role":"producer","transfer":{"allow_cidrs":["127.0.0.1/32"]}}`)
  	cons := s.operator.post(t, "/api/v1/catalog-zones", `{"name":"cat.remote.","role":"consumer","primaries":[{"address":"127.0.0.1:5399"}]}`)
  	if code := s.operator.status(t, "POST", "/api/v1/catalog-zones", `{"name":"catalog.test.","role":"producer","transfer":{"allow_cidrs":["127.0.0.1/32"]}}`); code != 409 {
  		t.Fatalf("duplicate catalog name: %d", code)
  	}
  	z := s.admin.createZone(t, `{"name":"m.test.","kind":"primary","soa":{"mname":"ns.m.test.","rname":"h.m.test."},"nameservers":["ns.m.test."],"catalog_zone_id":"`+prod["id"].(string)+`"}`)
  	got := s.viewer.get(t, "/api/v1/catalog-zones/"+prod["id"].(string))
  	members := got["members"].([]any)
  	if len(members) != 1 || members[0].(map[string]any)["name"] != "m.test." || members[0].(map[string]any)["zone_id"] != z["id"] {
  		t.Fatalf("members %v", members)
  	}
  	if list := s.viewer.get(t, "/api/v1/catalog-zones")["items"].([]any); len(list) != 2 {
  		t.Fatalf("list %v", list)
  	}
  	if code := s.viewer.status(t, "DELETE", "/api/v1/catalog-zones/"+cons["id"].(string), ""); code != 403 {
  		t.Fatalf("viewer delete: %d", code)
  	}
  	s.operator.delete(t, "/api/v1/catalog-zones/"+cons["id"].(string), 204)
  	if code := s.viewer.status(t, "GET", "/api/v1/catalog-zones/"+cons["id"].(string), ""); code != 404 {
  		t.Fatalf("deleted catalog: %d", code)
  	}
  	auditHasNoSecret(t, s, "createCatalogZone", "deleteCatalogZone")
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api -run TestCatalogZonesAPI -count=1'` and
      expect FAIL: `producer without transfer CIDRs: 501`.
- [ ] Implement the adapter:
  - map `CatalogZoneCreate` to `catzone.CreateInput`, and `catzone.View` to `CatalogZone` (members
    from zones plus issues);
  - map `catzone` validation errors to 400 and a duplicate zone name to 409 `conflict`, as zones do.

  The handlers call the service with the request's actor.

- [ ] In `main.go`, construct:
  - `cat := &catzone.Service{Store: st, Zones: zones, Build: build}`;
  - `odohKeys := &odoh.Keys{Pool: st.Pool, Box: box}`.

  Wire them as the Interfaces line says, and start `odohKeys.Run` with the serve context.

- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api -count=1 && go build ./mgmt/cmd/nexora-mgmt && go vet ./mgmt/...'`
      and expect PASS.
- [ ] Report the paths. The lead commits `M8 T22: catalog zone API and M8 service wiring`.

## Task 23: End-to-end ZONEMD for primaries, secondaries and RPZ

Files: `e2e/zonemd_test.go`, `e2e/rpz_zonemd_test.go` (both created)
Interfaces: consumes the managed stack harness (`StartPostgres`, `StartMgmt`, `Bootstrap`,
`StartManagedEngine`), `Env.StartNamedConfig` and `Named.UpdateZone`, and `zonemd.Apply` (Task 4).

- [ ] Create `e2e/zonemd_test.go` with `TestZonemdGeneratedForPrimaryZones` and
      `TestZonemdSecondaryVerification`:
  ```go
  func TestZonemdGeneratedForPrimaryZones(t *testing.T) {
  	st := startManagedStack(t) // postgres, mgmt with KEK, admin API, one managed engine; helper from xfr_test.go
  	for _, signed := range []bool{false, true} {
  		name := fmt.Sprintf("zmd%v.test.", signed)
  		id := st.api.createPrimaryZone(t, name, map[string]any{"zonemd_generate": true, "transfer": map[string]any{"allow_cidrs": []string{"127.0.0.1/32"}}})
  		if signed {
  			st.api.Must("PUT", "/api/v1/zones/"+id+"/dnssec", map[string]any{"enabled": true}, nil, 200)
  		}
  		st.api.createRecord(t, id, "www."+name, "A", "192.0.2.10")
  		st.waitEngineSerial(t, name)
  		first := axfr(t, st.engine.DNSAddr(), name)
  		ldnsVerify(t, first, signed) // ldns-verify-zone -Z (unsigned) or -ZZ (signed) on the transfer written to a file
  		st.api.createRecord(t, id, "mail."+name, "A", "192.0.2.11")
  		st.waitEngineSerial(t, name)
  		second := axfr(t, st.engine.DNSAddr(), name)
  		ldnsVerify(t, second, signed)
  		if soaSerial(second) == soaSerial(first) || zonemdSerial(second) != soaSerial(second) {
  			t.Fatalf("%s: ZONEMD serial %d SOA %d", name, zonemdSerial(second), soaSerial(second))
  		}
  		diff := ixfr(t, st.engine.DNSAddr(), name, soaSerial(first))
  		if !deletes(diff, dns.TypeZONEMD) || !adds(diff, dns.TypeZONEMD) {
  			t.Fatalf("%s: IXFR does not replace ZONEMD: %v", name, diff)
  		}
  		if signed && !apexNSECHas(second, name, dns.TypeZONEMD) {
  			t.Fatalf("%s: NSEC bitmap without ZONEMD", name)
  		}
  	}
  	code, _ := st.api.Do("POST", "/api/v1/zones/"+st.api.zoneID(t, "zmdfalse.test.")+"/records", map[string]any{"name": "zmdfalse.test.", "type": "ZONEMD", "ttl": 60, "data": "1 1 1 " + strings.Repeat("00", 48)}, nil)
  	if code != 400 {
  		t.Fatalf("ZONEMD record accepted: %d", code)
  	}
  }

  func TestZonemdSecondaryVerification(t *testing.T) {
  	st := startManagedStack(t)
  	a2 := readVector(t, "a2") // e2e/testdata/rfc8976/a2.zone without the out-of-zone foo.test. record
  	named := st.env.StartNamedConfig(harness.NamedConfig{Zones: []harness.NamedZone{{Name: "example.", Type: "primary", Text: a2}}})
  	id := st.api.createSecondaryZone(t, "example.", named.Addr, map[string]any{"zonemd_verify": "if_present"})
  	st.waitZone(t, id, func(z map[string]any) bool { return z["zonemd_status"] == "verified" })
  	if got := st.engine.Query(t, "ns1.example.", dns.TypeA); !hasA(got, "203.0.113.63") {
  		t.Fatalf("secondary does not answer: %v", got)
  	}
  	named.UpdateZone(t, bumpSerialKeepZonemd(a2, 2018031901, "added 3600 IN A 192.0.2.99"))
  	st.api.Must("POST", "/api/v1/zones/"+id+"/refresh", nil, nil, 202)
  	st.waitZone(t, id, func(z map[string]any) bool {
  		return z["zonemd_status"] == "failed" && strings.Contains(z["last_error"].(string), "zonemd:")
  	})
  	if got := st.engine.Query(t, "added.example.", dns.TypeA); len(got.Answer) != 0 || st.zoneSerial(t, id) != 2018031900 {
  		t.Fatal("a transfer failing ZONEMD was applied")
  	}
  	st.api.Must("PUT", "/api/v1/zones/"+id, map[string]any{"revision": st.revision(t, id), "zonemd_verify": "off"}, nil, 200)
  	st.api.Must("POST", "/api/v1/zones/"+id+"/refresh", nil, nil, 202)
  	st.waitZone(t, id, func(z map[string]any) bool { return z["serial"].(float64) == 2018031901 && z["zonemd_status"] == "off" })

  	plain := st.env.StartNamedConfig(harness.NamedConfig{Zones: []harness.NamedZone{{Name: "plain.test.", Type: "primary", Text: plainZone}}})
  	req := st.api.createSecondaryZone(t, "plain.test.", plain.Addr, map[string]any{"zonemd_verify": "required"})
  	st.waitZone(t, req, func(z map[string]any) bool { return z["zonemd_status"] == "failed" && z["loaded"] == false })
  }
  ```
  Put every helper (`startManagedStack`, `axfr`, `ixfr`, `ldnsVerify`, `readVector`,
  `bumpSerialKeepZonemd`, `waitZone`, `zoneSerial`) in `zonemd_test.go`, reusing existing e2e helpers
  where they exist (`xfr_test.go`, `secondary_update_test.go`).
- [ ] Create `e2e/rpz_zonemd_test.go` with `TestRPZZonemdVerification`:
  - `named` serves `rpz.test.` whose ZONEMD is computed with `zonemd.Apply` (serial 1, `bad.example CNAME .`);
  - an RPZ transfer zone with `zonemd_verify: if_present` is created, and `listRpzZones` status shows
    `zonemd: verified` for the engine, which blocks `bad.example.`;
  - `named.UpdateZone` with serial 2 adding `worse.example CNAME .` and serial 1's ZONEMD rdata, then
    `refreshRpzZone`: status `failed` with a non-empty `zonemd_error`, and `worse.example.` resolves
    (not blocked) while `bad.example.` stays blocked;
  - `updateRpzZone` to `required` against a zone without ZONEMD (serial 3): still not blocking
    `worse.example.`;
  - `off`: blocking.
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e -run "TestZonemdGeneratedForPrimaryZones|TestZonemdSecondaryVerification|TestRPZZonemdVerification" -count=1 -v'`
      and expect PASS. Before Tasks 12–14 and 20 are committed these fail with the stub symptoms (no
      ZONEMD in the AXFR, status `not_checked`); the lead runs this task after them.
- [ ] Report the paths. The lead commits `M8 T23: e2e ZONEMD for primaries, secondaries and RPZ`.

## Task 24: End-to-end ODoH target and proxy

Files: `e2e/odoh_test.go` (created), `e2e/fixtures/cmd/nexora-fixture/dns.go` (a header-capturing
`/odoh-capture` path on the fixture DoH server and a `/captured` control path)
Interfaces: consumes Tasks 11, 16, 19 and 22.

- [ ] Create `e2e/odoh_test.go` with `TestODoHTargetAndProxy`:
  ```go
  func TestODoHTargetAndProxy(t *testing.T) {
  	st := startODoHStack(t) // postgres, mgmt (KEK, DNS TLS), admin API, fixture upstream answering www.example.test A 192.0.2.10,
  	//                         engines A and B with DoH; A runs with NEXORA_DOH_RESOLVE=<first dnsTLSNames entry>=127.0.0.1
  	hc := st.mgmt.EncryptedClient().HTTPClient()
  	ctx := context.Background()

  	// Target role.
  	resp, err := hc.Get(st.b.DoHBase() + "/.well-known/odohconfigs")
  	if err != nil || resp.StatusCode != 404 {
  		t.Fatalf("target off: %v %v", resp, err)
  	}
  	st.api.Must("PUT", "/api/v1/odoh", odohBody(true, false, nil, 1), nil, 200)
  	st.api.Must("POST", "/api/v1/odoh/rotate-key", nil, nil, 200)
  	st.publishNow(t) // UPDATE odoh_keys SET publish_after = now() - interval '1 second'; NOTIFY nexora_odoh_keys
  	cfgs := waitConfigs(t, hc, st.b.DoHBase(), 1)
  	if cfgs[0].KemID != 0x0020 || cfgs[0].KdfID != 0x0001 || cfgs[0].AeadID != 0x0001 {
  		t.Fatalf("suite %+v", cfgs[0])
  	}
  	msg, r, err := harness.ODoHQuery(ctx, hc, st.b.DoHURL(), cfgs[0], query("www.example.test.", dns.TypeA))
  	if err != nil || r.StatusCode != 200 || r.Header.Get("Cache-Control") != "no-store" || !hasA(msg, "192.0.2.10") {
  		t.Fatalf("target query: %v %v %v", msg, r, err)
  	}
  	for name, c := range map[string]struct {
  		ct   string
  		body []byte
  		want int
  	}{
  		"garbled":      {"application/oblivious-dns-message", garble(encryptedQuery(t, cfgs[0])), 400},
  		"unknown key":  {"application/oblivious-dns-message", withKeyID(encryptedQuery(t, cfgs[0]), bytes.Repeat([]byte{7}, 32)), 401},
  		"content type": {"application/json", []byte(`{}`), 415},
  	} {
  		r, err := harness.ODoHRaw(ctx, hc, st.b.DoHURL(), c.ct, c.body)
  		if err != nil || r.StatusCode != c.want {
  			t.Fatalf("%s: %v %v, want %d", name, r, err, c.want)
  		}
  	}
  	old := cfgs[0]
  	st.api.Must("POST", "/api/v1/odoh/rotate-key", nil, nil, 200)
  	if first := waitConfigs(t, hc, st.b.DoHBase(), 1)[0]; !bytes.Equal(first.PublicKey, old.PublicKey) {
  		t.Fatal("a new key was listed before publish_after")
  	}
  	st.publishNow(t)
  	if first := waitConfigs(t, hc, st.b.DoHBase(), 2)[0]; bytes.Equal(first.PublicKey, old.PublicKey) {
  		t.Fatal("the new key is not listed first after publish_after")
  	}
  	if msg, r, err := harness.ODoHQuery(ctx, hc, st.b.DoHURL(), old, query("www.example.test.", dns.TypeA)); err != nil || r.StatusCode != 200 || !hasA(msg, "192.0.2.10") {
  		t.Fatalf("the previous key stopped working: %v %v", r, err)
  	}

  	// Proxy role on engine A towards engine B.
  	target := st.tlsName + ":" + st.b.DoHPort()
  	st.api.Must("PUT", "/api/v1/odoh", odohBody(true, true, []map[string]string{{"host": target, "ca_pem": st.caPEM}}, 3), nil, 200)
  	st.waitApplied(t)
  	proxyURL := func(host, path string) string {
  		return st.a.DoHURL() + "?targethost=" + url.QueryEscape(host) + "&targetpath=" + url.QueryEscape(path)
  	}
  	msg, r, err = harness.ODoHQuery(ctx, hc, proxyURL(target, "/dns-query"), cfgs[0], query("www.example.test.", dns.TypeA))
  	if err != nil || r.StatusCode != 200 || !hasA(msg, "192.0.2.10") || !strings.Contains(r.Header.Get("Proxy-Status"), "received-status=200") {
  		t.Fatalf("proxied query: %v %v", r, err)
  	}
  	if clients := st.queryLogClients(t, st.b.ID, "www.example.test."); !onlyAddress(clients, "127.0.0.1") {
  		t.Fatalf("target B logged clients %v", clients)
  	}
  	// denied, malformed, unreachable, black hole, relayed 401
  	expectProxy(t, hc, proxyURL("evil.test", "/dns-query"), 403, "http_request_denied")
  	expectProxy(t, hc, st.a.DoHURL()+"?targethost="+url.QueryEscape(target), 400, "http_request_error")
  	st.allowTargets(t, target, "127.0.0.1:"+st.closedPort, "127.0.0.1:"+st.blackHolePort, st.fixtureTarget)
  	expectProxy(t, hc, proxyURL("127.0.0.1:"+st.closedPort, "/dns-query"), 502, "destination_unavailable")
  	started := time.Now()
  	expectProxy(t, hc, proxyURL("127.0.0.1:"+st.blackHolePort, "/dns-query"), 502, "connection_timeout")
  	if time.Since(started) > 2500*time.Millisecond { // proxy_timeout_ms 2000 + 500
  		t.Fatal("the proxy timeout was not applied")
  	}
  	r, _ = harness.ODoHRaw(ctx, hc, proxyURL(target, "/dns-query"), "application/oblivious-dns-message", withKeyID(encryptedQuery(t, cfgs[0]), bytes.Repeat([]byte{7}, 32)))
  	if r.StatusCode != 401 || !strings.Contains(r.Header.Get("Proxy-Status"), "received-status=401") {
  		t.Fatalf("target 401 not relayed: %v", r)
  	}
  	r, _ = harness.ODoHRaw(ctx, hc, proxyURL(st.fixtureTarget, "/odoh-capture"), "application/oblivious-dns-message", []byte("x"))
  	if h := st.capturedHeaders(t); h.Get("X-Forwarded-For") != "" || h.Get("Forwarded") != "" || h.Get("Cookie") != "" {
  		t.Fatalf("client headers leaked to the target: %v", h)
  	}
  	st.setRecursionACL(t, "192.0.2.0/24")
  	expectProxy(t, hc, proxyURL(target, "/dns-query"), 403, "http_request_denied")
  }
  ```
  The request to `/odoh-capture` carries `Cookie: a=b` and `X-Forwarded-For: 198.51.100.1`, so the
  capture check cannot pass by accident. `/captured` on the fixture control listener returns the
  headers of the last `/odoh-capture` request as JSON. The black-hole port is a Go listener that
  accepts and never writes. Write the helpers in `odoh_test.go`.
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e -run TestODoHTargetAndProxy -count=1 -v'`
      and expect PASS.
- [ ] Report the paths. The lead commits `M8 T24: e2e ODoH target and proxy`.

## Task 25: End-to-end catalog zones with BIND

Files: `e2e/catalog_zones_test.go` (created), `e2e/harness/named.go` (catalog consumer
configuration)
Interfaces: consumes Task 22; `harness.NamedConfig` gains
`Catalogs []NamedCatalog`, with
`type NamedCatalog struct{ Zone, Primary, KeyName string }`. It renders
`options { allow-new-zones yes; };` and, in `options`,
`catalog-zones { zone "<Zone>" default-primaries { <ip> port <port> key "<KeyName>"; } in-memory yes; };`
plus a `secondary` zone for `<Zone>` from `<Primary>`.

- [ ] Create `e2e/catalog_zones_test.go`:
  ```go
  func TestCatalogZoneProducer(t *testing.T) {
  	st := startManagedStack(t) // from zonemd_test.go
  	key := st.createTSIGKey(t, "cat-key.")
  	cat := st.api.post(t, "/api/v1/catalog-zones", map[string]any{"name": "catalog.test.", "role": "producer",
  		"transfer": map[string]any{"allow_cidrs": []string{"127.0.0.1/32"}, "tsig_key_id": key.ID}})
  	a := st.api.createPrimaryZone(t, "a.prod.test.", map[string]any{"catalog_zone_id": cat["id"], "transfer": map[string]any{"allow_cidrs": []string{"127.0.0.1/32"}, "tsig_key_id": key.ID}})
  	b := st.api.createPrimaryZone(t, "b.prod.test.", map[string]any{"catalog_zone_id": cat["id"], "transfer": map[string]any{"allow_cidrs": []string{"127.0.0.1/32"}, "tsig_key_id": key.ID}})
  	st.waitEngineZones(t, "catalog.test.", "a.prod.test.", "b.prod.test.")
  	named := st.env.StartNamedConfig(harness.NamedConfig{
  		Keys:     []harness.NamedKey{{Name: "cat-key.", Algorithm: "hmac-sha256", SecretB64: key.SecretB64}},
  		Catalogs: []harness.NamedCatalog{{Zone: "catalog.test.", Primary: st.engine.DNSAddr(), KeyName: "cat-key."}},
  	})
  	harness.Eventually(t, 30*time.Second, func() error { return soaServed(named.Addr, "a.prod.test.", "b.prod.test.") })
  	rrs := axfrWithKey(t, st.engine.DNSAddr(), "catalog.test.", key)
  	if !hasTXT(rrs, "version.catalog.test.", "2") || !hasNS(rrs, "catalog.test.", "invalid.") ||
  		!hasPTR(rrs, catzone.Label(uuid.MustParse(a))+".zones.catalog.test.", "a.prod.test.") ||
  		!hasPTR(rrs, catzone.Label(uuid.MustParse(b))+".zones.catalog.test.", "b.prod.test.") {
  		t.Fatalf("catalog AXFR: %v", rrs)
  	}
  	st.api.Must("PUT", "/api/v1/zones/"+b, map[string]any{"revision": st.revision(t, b), "catalog_zone_id": nil}, nil, 200)
  	harness.Eventually(t, 30*time.Second, func() error { return notServed(named.Addr, "b.prod.test.") })
  	st.api.Must("DELETE", "/api/v1/zones/"+a+"?revision="+strconv.FormatInt(st.revision(t, a), 10), nil, nil, 204)
  	a2 := st.api.createPrimaryZone(t, "a.prod.test.", map[string]any{"catalog_zone_id": cat["id"]})
  	rrs = axfrWithKey(t, st.engine.DNSAddr(), "catalog.test.", key)
  	if hasPTR(rrs, catzone.Label(uuid.MustParse(a))+".zones.catalog.test.", "a.prod.test.") || !hasPTR(rrs, catzone.Label(uuid.MustParse(a2))+".zones.catalog.test.", "a.prod.test.") {
  		t.Fatal("a re-created member kept its old label")
  	}
  	code, _ := st.api.Do("POST", "/api/v1/zones/"+cat["zone_id"].(string)+"/records", map[string]any{"name": "x.catalog.test.", "type": "TXT", "ttl": 0, "data": `"x"`}, nil)
  	if code != 409 {
  		t.Fatalf("record edit on the catalog: %d", code)
  	}
  }

  func TestCatalogZoneConsumer(t *testing.T) {
  	st := startManagedStack(t)
  	key := st.createTSIGKey(t, "remote-key.")
  	st.api.createPrimaryZone(t, "c.cat.test.", nil) // operator zone, created first
  	catalogV := func(serial int, body string) string {
  		return fmt.Sprintf("$ORIGIN cat.remote.\n@ 0 IN SOA invalid. invalid. %d 3600 600 86400 0\n@ 0 IN NS invalid.\n%s\n", serial, body)
  	}
  	named := st.env.StartNamedConfig(harness.NamedConfig{
  		Keys: []harness.NamedKey{{Name: "remote-key.", Algorithm: "hmac-sha256", SecretB64: key.SecretB64}},
  		Zones: []harness.NamedZone{
  			{Name: "cat.remote.", Type: "primary", Text: catalogV(1, "version 0 IN TXT \"2\"\nla.zones 0 IN PTR a.cat.test.\nlb.zones 0 IN PTR b.cat.test."), AllowTransferKey: "remote-key."},
  			{Name: "a.cat.test.", Type: "primary", Text: memberZone("a.cat.test.", "192.0.2.21"), AllowTransferKey: "remote-key."},
  			{Name: "b.cat.test.", Type: "primary", Text: memberZone("b.cat.test.", "192.0.2.22"), AllowTransferKey: "remote-key."},
  			{Name: "c.cat.test.", Type: "primary", Text: memberZone("c.cat.test.", "192.0.2.23"), AllowTransferKey: "remote-key."},
  		},
  	})
  	cons := st.api.post(t, "/api/v1/catalog-zones", map[string]any{"name": "cat.remote.", "role": "consumer",
  		"primaries": []map[string]any{{"address": named.Addr, "tsig_key_id": key.ID}}})
  	harness.Eventually(t, 60*time.Second, func() error {
  		return answers(st.engine.DNSAddr(), map[string]string{"www.a.cat.test.": "192.0.2.21", "www.b.cat.test.": "192.0.2.22"})
  	})
  	named.UpdateZone(t, catalogV(2, "version 0 IN TXT \"2\"\nla.zones 0 IN PTR a.cat.test.\nlb.zones 0 IN PTR b.cat.test.\nlc.zones 0 IN PTR c.cat.test."))
  	st.refreshCatalog(t, cons)
  	st.waitCatalog(t, cons["id"].(string), func(v map[string]any) bool { return memberState(v, "c.cat.test.") == "clash" })
  	if z := st.zoneByName(t, "c.cat.test."); z["kind"] != "primary" || z["catalog_zone_id"] != nil {
  		t.Fatalf("operator zone touched: %v", z)
  	}
  	aID := st.zoneByName(t, "a.cat.test.")["id"]
  	named.UpdateZone(t, catalogV(3, "version 0 IN TXT \"2\"\nla2.zones 0 IN PTR a.cat.test."))
  	st.refreshCatalog(t, cons)
  	harness.Eventually(t, 60*time.Second, func() error {
  		if st.zoneByNameOrNil(t, "b.cat.test.") != nil {
  			return errors.New("b.cat.test. still exists")
  		}
  		if z := st.zoneByNameOrNil(t, "a.cat.test."); z == nil || z["id"] == aID || z["catalog_member_label"] != "la2" {
  			return fmt.Errorf("a.cat.test. not recreated: %v", z)
  		}
  		return nil
  	})
  	a := st.zoneByName(t, "a.cat.test.")
  	for _, c := range []struct{ method, path string; body any }{
  		{"PUT", "/api/v1/zones/" + a["id"].(string), map[string]any{"revision": a["revision"], "zonemd_verify": "off"}},
  		{"DELETE", "/api/v1/zones/" + a["id"].(string) + "?revision=" + fmt.Sprint(a["revision"]), nil},
  	} {
  		if code, _ := st.api.Do(c.method, c.path, c.body, nil); code != 409 {
  			t.Fatalf("%s on a member: %d", c.method, code)
  		}
  	}
  	named.UpdateZone(t, catalogV(4, "version 0 IN TXT \"1\"\nla2.zones 0 IN PTR a.cat.test."))
  	st.refreshCatalog(t, cons)
  	st.waitCatalog(t, cons["id"].(string), func(v map[string]any) bool { return v["broken_reason"] == `unsupported catalog version "1"` })
  	if st.zoneByNameOrNil(t, "a.cat.test.") == nil {
  		t.Fatal("a broken catalog removed a member")
  	}
  	named.UpdateZone(t, catalogV(5, "version 0 IN TXT \"2\""))
  	st.refreshCatalog(t, cons)
  	harness.Eventually(t, 60*time.Second, func() error {
  		if st.zoneByNameOrNil(t, "a.cat.test.") != nil {
  			return errors.New("processing did not resume")
  		}
  		return nil
  	})
  	named.UpdateZone(t, catalogV(6, "version 0 IN TXT \"2\"\nlz.zones 0 IN PTR a.cat.test."))
  	st.refreshCatalog(t, cons)
  	harness.Eventually(t, 60*time.Second, func() error {
  		if st.zoneByNameOrNil(t, "a.cat.test.") == nil {
  			return errors.New("member not created")
  		}
  		return nil
  	})
  	st.api.Must("DELETE", "/api/v1/catalog-zones/"+cons["id"].(string), nil, nil, 204)
  	if z := st.zoneByName(t, "a.cat.test."); z["catalog_zone_id"] != nil || z["kind"] != "secondary" {
  		t.Fatalf("member not kept as a plain secondary: %v", z)
  	}
  }
  ```
  Write the helpers in the test file.
- [ ] Extend `named.go` rendering for `Catalogs` as the Interfaces line says, and add a unit case to
      `named_test.go`'s config rendering test if one exists (else check with `named-checkconf` in the
      e2e run).
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e -run "TestCatalogZoneProducer|TestCatalogZoneConsumer" -count=1 -v'`
      and expect PASS.
- [ ] Report the paths. The lead commits `M8 T25: e2e catalog zones with BIND`.

## Task 26: End-to-end mDNS in the namespace lab

Files: `e2e/mdns_test.go` (created), `e2e/harness/harness.go` (only `Proc.Stderr`, when the harness
has no way to read a process's stderr)
Interfaces: consumes Task 10 (`RunInNetLab`, `NetLab`), Task 17 and Task 21, and
`Env.StartStandaloneEngine` and `Env.StartFixture`.

- [ ] Create `e2e/mdns_test.go`:
  ```go
  func TestMdnsGatewayNetLab(t *testing.T) {
  	if !harness.InNetLab() {
  		harness.RunInNetLab(t)
  		return
  	}
  	e := harness.New(t)
  	lab := e.NewNetLab()
  	lab.Link("gw0", "lan0", "lanA", 0)
  	lab.StartIn("lanA", "nexora-fixture", "mdns-responder", "--interface", "lan0",
  		"--record", "printer.local. 120 IN A 10.254.0.9",
  		"--record", "printer.local. 120 IN AAAA fd00::9",
  		"--record", "_ipp._tcp.local. 4500 IN PTR one._ipp._tcp.local.",
  		"--record", "_ipp._tcp.local. 4500 IN PTR two._ipp._tcp.local.",
  		"--record", "_ipp._tcp.local. 4500 IN PTR three._ipp._tcp.local.")
  	up := e.StartFixture() // answers every A query with 192.0.2.53
  	snap := standaloneSnapshot(up.UDPAddr(), func(s *controlv1.ConfigSnapshot) {
  		s.Mdns = &controlv1.MdnsConfig{Enabled: true, Interfaces: []string{"gw0", "nxmissing0"}, TimeoutMs: 400}
  		s.ForwardZones = []*controlv1.ForwardZone{{Domain: "kw.local.", Addresses: []string{up.UDPAddr()}}}
  	})
  	en := e.StartStandaloneEngine(snap, nil)
  	for _, net := range []string{"udp", "tcp"} {
  		r := exchange(t, net, en.DNSAddr(), "printer.local.", dns.TypeA)
  		if !hasA(r, "10.254.0.9") || r.Answer[0].Header().Ttl > 10 || r.Authoritative {
  			t.Fatalf("%s A: %v", net, r)
  		}
  		if r := exchange(t, net, en.DNSAddr(), "printer.local.", dns.TypeAAAA); !hasAAAA(r, "fd00::9") {
  			t.Fatalf("%s AAAA: %v", net, r)
  		}
  	}
  	started := time.Now()
  	if r := exchange(t, "udp", en.DNSAddr(), "_ipp._tcp.local.", dns.TypePTR); len(r.Answer) != 3 || time.Since(started) < 400*time.Millisecond {
  		t.Fatalf("PTR: %v after %s", r, time.Since(started))
  	}
  	if r := exchange(t, "udp", en.DNSAddr(), "nothere.local.", dns.TypeA); r.Rcode != dns.RcodeNameError {
  		t.Fatalf("unknown .local: %v", r)
  	}
  	if r := exchange(t, "udp", en.DNSAddr(), "host.kw.local.", dns.TypeA); !hasA(r, "192.0.2.53") {
  		t.Fatalf("forward zone under local.: %v", r)
  	}
  	if en.Metric(t, "nexora_mdns_queries_total", map[string]string{"result": "answered"}) < 1 ||
  		en.Metric(t, "nexora_mdns_queries_total", map[string]string{"result": "unanswered"}) < 1 ||
  		en.Metric(t, "nexora_mdns_interface_missing", map[string]string{"interface": "nxmissing0"}) != 1 {
  		t.Fatal("mDNS metrics missing")
  	}
  }

  func TestMdnsReflectorNetLab(t *testing.T) {
  	if !harness.InNetLab() {
  		harness.RunInNetLab(t)
  		return
  	}
  	e := harness.New(t)
  	lab := e.NewNetLab()
  	lab.Link("gwA", "lanA0", "lanA", 1)
  	lab.Link("gwB", "lanB0", "lanB", 2)
  	responder := lab.StartIn("lanA", "nexora-fixture", "mdns-responder", "--interface", "lanA0", "--record", "printer.local. 120 IN A 10.254.1.9")
  	up := e.StartFixture()
  	reflecting := standaloneSnapshot(up.UDPAddr(), func(s *controlv1.ConfigSnapshot) {
  		s.Mdns = &controlv1.MdnsConfig{Reflect: true, ReflectInterfaces: []string{"gwA", "gwB"}}
  	})
  	en := e.StartStandaloneEngine(reflecting, nil)
  	out := lab.RunIn("lanB", "nexora-fixture", "mdns-query", "--interface", "lanB0", "--name", "printer.local.", "--type", "A", "--wait", "2s")
  	if !strings.Contains(out, "10.254.1.9") || !strings.Contains(out, "PACKETS 1") {
  		t.Fatalf("reflected answer:\n%s", out)
  	}
  	if en.Metric(t, "nexora_mdns_reflected_packets_total", map[string]string{"from": "gwA", "to": "gwB"}) < 1 {
  		t.Fatal("no reflected packet counted")
  	}
  	if strings.Contains(responder.Stderr(), "ECHO") {
  		t.Fatal("the responder received its own answer back")
  	}
  	off := proto.Clone(reflecting).(*controlv1.ConfigSnapshot)
  	off.Version++
  	off.Mdns = nil
  	en.Reload(t, off, nil)
  	if out := lab.RunIn("lanB", "nexora-fixture", "mdns-query", "--interface", "lanB0", "--name", "printer.local.", "--type", "A", "--wait", "2s"); !strings.Contains(out, "PACKETS 0") {
  		t.Fatalf("answer crossed with reflection off:\n%s", out)
  	}
  }

  func TestMdnsOffForwardsLocalNames(t *testing.T) {
  	e := harness.New(t)
  	up := e.StartFixture()
  	en := e.StartStandaloneEngine(standaloneSnapshot(up.UDPAddr(), nil), nil)
  	if r := exchange(t, "udp", en.DNSAddr(), "printer.local.", dns.TypeA); !hasA(r, "192.0.2.53") {
  		t.Fatalf("without mdns config .local must be forwarded: %v", r)
  	}
  	if en.Metric(t, "nexora_mdns_queries_total", map[string]string{"result": "answered"}) != 0 {
  		t.Fatal("the gateway ran without an mdns config")
  	}
  }
  ```
  `standaloneSnapshot(upstream, mutate)` builds the forward-mode snapshot the standalone tests use
  (`forward_test.go`). `Proc.Stderr()` returns captured stderr. The fixture upstream's A answer is configured through
  its existing flags.
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e -run "TestMdnsGatewayNetLab|TestMdnsReflectorNetLab|TestMdnsOffForwardsLocalNames" -count=1 -v'`
      and expect PASS.
- [ ] Report the paths. The lead commits `M8 T26: e2e mDNS gateway and reflector in the namespace lab`.

## Task 27: GUI ZONEMD card and catalog select

Files: `web/src/pages/ZoneZonemdCard.tsx` (created), `web/src/pages/ZoneDetailPage.tsx`,
`web/src/help/catalog/zones.ts`, `web/e2e/screens/40-zonemd.spec.ts` (created),
`e2e/gui_seed_m8_zonemd_test.go` (created)
Interfaces: consumes Task 2's schema and Tasks 14 and 22; test ids `zonemd-generate`,
`zonemd-verify`, `zonemd-status`, `zone-catalog-select`. Seed vars: `NEXORA_E2E_ZONEMD_PRIMARY_ID`,
`NEXORA_E2E_ZONEMD_SECONDARY_ID`, `NEXORA_E2E_PRODUCER_CATALOG_NAME`.

- [ ] Create `e2e/gui_seed_m8_zonemd_test.go`. `init() { registerGUISeed(seedZonemd) }`, where
      `seedZonemd` creates:
  - a primary zone `gui-zmd.test.`;
  - a secondary zone `gui-zmd-sec.test.` whose primary is `127.0.0.1:9` (never loads, status
    `not_checked`);
  - a producer catalog `gui-catalog.test.`.

  It sets the three vars.

- [ ] Create `web/e2e/screens/40-zonemd.spec.ts`:
  ```ts
  import { test, expect, env, login } from "../fixtures";

  for (const width of [1280, 400]) {
    test(`ZONEMD settings and catalog membership at ${width}px`, async ({
      page,
    }) => {
      // TestGUICoverage counts: getZone, updateZone, listCatalogZones.
      await page.setViewportSize({ width, height: 900 });
      await login(
        page,
        env("NEXORA_E2E_OPERATOR_USER"),
        env("NEXORA_E2E_OPERATOR_PASSWORD"),
      );
      await page.goto(`/zones/${env("NEXORA_E2E_ZONEMD_PRIMARY_ID")}`);
      const generate = page.getByTestId("zonemd-generate");
      await expect(generate).toBeVisible();
      await generate.click();
      await page.getByTestId("zone-catalog-select").click();
      await page
        .getByRole("option", { name: env("NEXORA_E2E_PRODUCER_CATALOG_NAME") })
        .click();
      await page
        .getByRole("button", { name: "Save ZONEMD and catalog" })
        .click();
      await page.reload();
      await expect(page.getByTestId("zonemd-generate")).toBeChecked();
      await expect(page.getByTestId("zone-catalog-select")).toContainText(
        env("NEXORA_E2E_PRODUCER_CATALOG_NAME"),
      );
      await expect(page.getByTestId("zonemd-verify")).toHaveCount(0); // primaries have no verify mode

      await page.goto(`/zones/${env("NEXORA_E2E_ZONEMD_SECONDARY_ID")}`);
      await page.getByTestId("zonemd-verify").click();
      await page.getByRole("option", { name: "Required" }).click();
      await page
        .getByRole("button", { name: "Save ZONEMD and catalog" })
        .click();
      await page.reload();
      await expect(page.getByTestId("zonemd-verify")).toContainText("Required");
      await expect(page.getByTestId("zonemd-status")).toContainText(
        "Not checked",
      );
      await expect(page.getByTestId("zonemd-generate")).toHaveCount(0);

      // Restore for the other width.
      await page.getByTestId("zonemd-verify").click();
      await page.getByRole("option", { name: "If present" }).click();
      await page
        .getByRole("button", { name: "Save ZONEMD and catalog" })
        .click();
      await page.goto(`/zones/${env("NEXORA_E2E_ZONEMD_PRIMARY_ID")}`);
      await page.getByTestId("zonemd-generate").click();
      await page.getByTestId("zone-catalog-select").click();
      await page.getByRole("option", { name: "No catalog" }).click();
      await page
        .getByRole("button", { name: "Save ZONEMD and catalog" })
        .click();
      await expect(page.getByTestId("zonemd-generate")).not.toBeChecked();
    });
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e -run TestGUICoverage -count=1'` with
      `NEXORA_E2E_SPECS=40-zonemd` if the harness supports a spec filter (else the whole run), and
      expect FAIL on `zonemd-generate` not visible.
- [ ] Implement `ZoneZonemdCard.tsx`:
  - primaries show a "Generate ZONEMD (SHA-384)" switch (`zonemd-generate`) and the catalog select
    (`zone-catalog-select`, options "No catalog" plus `listCatalogZones` producers other than the zone
    itself, hidden for catalog zones and consumer members);
  - secondaries show the verify select (`zonemd-verify`: "Off", "If present", "Required") and the
    status badge (`zonemd-status`: Not checked, Off, Absent, Verified, Failed, with the error in a
    tooltip);
  - consumer members show "Managed by catalog <name>" read-only;
  - one "Save ZONEMD and catalog" button sends `updateZone` with `revision`;
  - each control has a `HelpTip`, with entries in `zones.ts` (`zonemd-generate`, `zonemd-verify`,
    `zone-catalog-select`) and the file listed in its `pages`.

  Render the card from `ZoneDetailPage.tsx` above the tabs. It stacks at 400 px.

- [ ] Run `cd web && pnpm run typecheck && pnpm run lint`, then the GUI run above, and expect PASS.
- [ ] Report the paths. The lead commits `M8 T27: GUI ZONEMD card and catalog select`.

## Task 28: GUI Catalog zones page

Files: `web/src/pages/CatalogZonesPage.tsx` (created), `web/src/app/router.tsx`,
`web/src/components/layout/AppShell.tsx`, `web/src/help/catalog/catalogzones.ts` (created),
`web/src/help/catalog/index.ts`, `web/e2e/screens/41-catalog-zones.spec.ts` (created),
`e2e/gui_seed_m8_catalog_test.go` (created)
Interfaces: route `/zones/catalogs`; nav test id `nav-catalog-zones`; test ids `catalog-create`,
`catalog-members`. Seed vars: `NEXORA_E2E_SEEDED_CATALOG_ID` (a producer with one member zone) and
`NEXORA_E2E_CLASH_CATALOG_ID` (a consumer with an inserted `catalog_member_issues` clash row).

- [ ] Create the seed: a producer catalog `gui-seeded.test.` with member zone `gui-member.test.`, and a
      consumer catalog `gui-remote.test.` (primary `127.0.0.1:9`) with a clash row for `gui-member.test.`
      inserted by SQL through the seed's store access.
- [ ] Create `web/e2e/screens/41-catalog-zones.spec.ts`:
  ```ts
  import { test, expect, env, login } from "../fixtures";

  for (const width of [1280, 400]) {
    test(`catalog zones page at ${width}px`, async ({ page }) => {
      // TestGUICoverage counts: listCatalogZones, createCatalogZone, getCatalogZone, deleteCatalogZone.
      await page.setViewportSize({ width, height: 900 });
      await login(
        page,
        env("NEXORA_E2E_OPERATOR_USER"),
        env("NEXORA_E2E_OPERATOR_PASSWORD"),
      );
      await page.goto("/zones");
      await page.getByTestId("nav-catalog-zones").click();
      await expect(page).toHaveURL(/\/zones\/catalogs$/);
      await expect(
        page.getByRole("heading", { name: "Catalog zones" }),
      ).toBeVisible();

      const suffix = `${width}-${Date.now()}`;
      await page.getByTestId("catalog-create").click();
      let dialog = page.getByRole("dialog", { name: "New catalog zone" });
      await dialog.getByLabel("Catalog zone name").fill(`prod-${suffix}.test.`);
      await dialog.getByLabel("Role").click();
      await page.getByRole("option", { name: "Producer" }).click();
      await dialog.getByLabel("Transfer allowed from").fill("127.0.0.1/32");
      await dialog.getByRole("button", { name: "Create" }).click();
      await expect(
        page.getByRole("row", { name: new RegExp(`prod-${suffix}`) }),
      ).toBeVisible();

      await page.getByTestId("catalog-create").click();
      dialog = page.getByRole("dialog", { name: "New catalog zone" });
      await dialog.getByLabel("Catalog zone name").fill(`cons-${suffix}.test.`);
      await dialog.getByLabel("Role").click();
      await page.getByRole("option", { name: "Consumer" }).click();
      await dialog.getByLabel("Primary address").fill("127.0.0.1:9");
      await dialog.getByRole("button", { name: "Create" }).click();
      await expect(
        page.getByRole("row", { name: new RegExp(`cons-${suffix}`) }),
      ).toBeVisible();

      await page.goto(
        `/zones/catalogs?catalog=${env("NEXORA_E2E_SEEDED_CATALOG_ID")}`,
      );
      await expect(page.getByTestId("catalog-members")).toContainText(
        "gui-member.test.",
      );
      await page.goto(
        `/zones/catalogs?catalog=${env("NEXORA_E2E_CLASH_CATALOG_ID")}`,
      );
      await expect(page.getByTestId("catalog-members")).toContainText("Clash");

      for (const name of [`prod-${suffix}`, `cons-${suffix}`]) {
        await page.goto("/zones/catalogs");
        await page
          .getByRole("row", { name: new RegExp(name) })
          .getByRole("button", { name: "Delete" })
          .click();
        await page
          .getByRole("alertdialog")
          .getByRole("button", { name: "Delete" })
          .click();
        await expect(
          page.getByRole("row", { name: new RegExp(name) }),
        ).toHaveCount(0);
      }
    });
  }
  ```
- [ ] Run the GUI coverage run for this spec and expect FAIL: `nav-catalog-zones` not found.
- [ ] Implement:
  - the page:
    - a table of catalogs (name, role, members count, broken reason);
    - the create dialog, whose fields switch by role;
    - `?catalog=<id>` opens a detail panel with the members table (`catalog-members`: name, label,
      state Configured or Clash, issue) and the broken reason as a destructive alert;
    - delete with confirmation, where the consumer text warns that member zones stay as secondaries;
  - the route in `router.tsx`;
  - a "Catalog zones" child under Zones in `AppShell.tsx` (`nav-catalog-zones`), following M6's
    collapsible pattern;
  - help entries in `catalogzones.ts` (with the page in `pages`) registered in `index.ts`;
  - the document title "Catalog zones · Nexora".
- [ ] Run `cd web && pnpm run typecheck && pnpm run lint` and the GUI run, and expect PASS.
- [ ] Report the paths. The lead commits `M8 T28: GUI catalog zones page`.

## Task 29: GUI Oblivious DoH settings

Files: `web/src/pages/OdohSection.tsx` (created), `web/src/pages/SettingsPage.tsx`, the help area
file whose `pages` lists `pages/SettingsPage.tsx`, `web/e2e/screens/42-odoh.spec.ts` (created)
Interfaces: consumes Task 16; test ids `odoh-target-enabled`, `odoh-proxy-enabled`, `odoh-targets`,
`odoh-rotate`, `odoh-keys`.

- [ ] Create `web/e2e/screens/42-odoh.spec.ts`:
  ```ts
  import { test, expect, env, login } from "../fixtures";

  for (const width of [1280, 400]) {
    test(`Oblivious DoH settings at ${width}px`, async ({ page }) => {
      // TestGUICoverage counts: getOdohSettings, updateOdohSettings, rotateOdohKey.
      await page.setViewportSize({ width, height: 900 });
      await login(
        page,
        env("NEXORA_E2E_ADMIN_USER"),
        env("NEXORA_E2E_ADMIN_PASSWORD"),
      );
      await page.goto("/settings");
      const section = page.getByRole("region", { name: "Oblivious DoH" });
      await section.getByTestId("odoh-target-enabled").click();
      await section.getByTestId("odoh-proxy-enabled").click();
      await section.getByRole("button", { name: "Save Oblivious DoH" }).click();
      await expect(
        section.getByText("Add at least one proxy target"),
      ).toBeVisible();
      await section
        .getByTestId("odoh-targets")
        .getByLabel("Target host")
        .fill("odoh.example:8443");
      await section.getByRole("button", { name: "Save Oblivious DoH" }).click();
      await page.reload();
      await expect(section.getByTestId("odoh-target-enabled")).toBeChecked();
      await expect(
        section.getByTestId("odoh-targets").getByLabel("Target host"),
      ).toHaveValue("odoh.example:8443");
      await section.getByTestId("odoh-rotate").click();
      await page
        .getByRole("alertdialog")
        .getByRole("button", { name: "Rotate" })
        .click();
      await section.getByTestId("odoh-rotate").click();
      await page
        .getByRole("alertdialog")
        .getByRole("button", { name: "Rotate" })
        .click();
      const rows = section.getByTestId("odoh-keys").getByRole("row");
      await expect(rows.nth(2)).toBeVisible(); // header plus at least two keys
      expect(await rows.count()).toBeGreaterThanOrEqual(3);
      await section.getByTestId("odoh-proxy-enabled").click();
      await section.getByTestId("odoh-target-enabled").click();
      await section.getByRole("button", { name: "Save Oblivious DoH" }).click();
      await page.reload();
      await expect(
        section.getByTestId("odoh-target-enabled"),
      ).not.toBeChecked();
    });
  }
  ```
  The 400 px run starts with the keys of the 1280 px run, hence the lower bound on rows.
- [ ] Run the GUI coverage run for this spec and expect FAIL: region "Oblivious DoH" not found.
- [ ] Implement `OdohSection.tsx` (a `section` with `aria-label="Oblivious DoH"`) in `SettingsPage.tsx`:
  - target and proxy switches;
  - the proxy target list (host plus optional CA PEM textarea, add and remove) with client-side
    "Add at least one proxy target";
  - proxy timeout and rotation hours inputs;
  - the key table (created, listed from, valid until);
  - the Rotate button, visible for admins only (`permissions.ts`), with a confirmation;
  - the warning text: proxy targets see only the engine address; the recursion ACL must allow the
    proxies that use this engine as a target.

  Every control has a `HelpTip` and a catalogue entry.

- [ ] Run `cd web && pnpm run typecheck && pnpm run lint` and the GUI run, and expect PASS.
- [ ] Report the paths. The lead commits `M8 T29: GUI Oblivious DoH settings`.

## Task 30: GUI engine group mDNS section

Files: `web/src/pages/EngineGroupMdnsSection.tsx` (created), `web/src/pages/EngineGroupPage.tsx`,
`web/src/help/catalog/fleet.ts`, `web/e2e/screens/43-mdns.spec.ts` (created),
`e2e/gui_seed_m8_mdns_test.go` (created)
Interfaces: consumes Task 17; test ids `mdns-enabled`, `mdns-interfaces`, `mdns-reflect`,
`mdns-reflect-interfaces`, `mdns-timeout`; seed var `NEXORA_E2E_MDNS_GROUP_ID` (a group without
engines).

- [ ] Create the seed (engine group `gui-mdns`, no engines) and `web/e2e/screens/43-mdns.spec.ts`:
  ```ts
  import { test, expect, env, login } from "../fixtures";

  for (const width of [1280, 400]) {
    test(`engine group mDNS settings at ${width}px`, async ({ page }) => {
      // TestGUICoverage counts: getEngineGroup, updateEngineGroup.
      await page.setViewportSize({ width, height: 900 });
      await login(
        page,
        env("NEXORA_E2E_OPERATOR_USER"),
        env("NEXORA_E2E_OPERATOR_PASSWORD"),
      );
      await page.goto(`/engines/groups/${env("NEXORA_E2E_MDNS_GROUP_ID")}`);
      const section = page.getByRole("region", { name: "mDNS" });
      await section.getByTestId("mdns-enabled").click();
      await section.getByRole("button", { name: "Save mDNS" }).click();
      await expect(
        section.getByText("Name at least one interface"),
      ).toBeVisible();
      await section.getByTestId("mdns-interfaces").fill("vlan10, vlan20");
      await section.getByTestId("mdns-reflect").click();
      await section
        .getByTestId("mdns-reflect-interfaces")
        .fill("vlan10, vlan20");
      await section.getByTestId("mdns-timeout").fill("700");
      await section.getByRole("button", { name: "Save mDNS" }).click();
      await page.reload();
      await expect(section.getByTestId("mdns-enabled")).toBeChecked();
      await expect(section.getByTestId("mdns-interfaces")).toHaveValue(
        "vlan10, vlan20",
      );
      await expect(section.getByTestId("mdns-reflect")).toBeChecked();
      await expect(section.getByTestId("mdns-timeout")).toHaveValue("700");
      await expect(section.getByText("needs hostNetwork")).toBeVisible();
      await section.getByTestId("mdns-enabled").click();
      await section.getByTestId("mdns-reflect").click();
      await section.getByRole("button", { name: "Save mDNS" }).click();
      await page.reload();
      await expect(section.getByTestId("mdns-enabled")).not.toBeChecked();
    });
  }
  ```
- [ ] Run the GUI coverage run for this spec and expect FAIL: region "mDNS" not found.
- [ ] Implement the section (`aria-label="mDNS"`):
  - gateway switch, interfaces (comma-separated), timeout (ms), reflection switch, reflection
    interfaces;
  - client validation mirroring `ValidateMdns`;
  - the info alert "The mDNS gateway needs hostNetwork or an interface on the LAN segment; see the
    operations guide";
  - save with `updateEngineGroup` and `revision`;
  - a `HelpTip` on each control, with entries in `fleet.ts`.
- [ ] Run `cd web && pnpm run typecheck && pnpm run lint` and the GUI run, and expect PASS.
- [ ] Report the paths. The lead commits `M8 T30: GUI engine group mDNS section`.

## Task 31: GUI RPZ ZONEMD verification

Files: `web/src/pages/RpzPage.tsx`, `web/src/help/catalog/filtering.ts`,
`web/e2e/screens/44-rpz-zonemd.spec.ts` (created), `e2e/gui_seed_m8_rpz_test.go` (created)
Interfaces: consumes Task 14; test ids `rpz-zonemd-verify`, `rpz-zonemd-status`; seed var
`NEXORA_E2E_RPZ_ZONEMD_NAME` (a transfer RPZ zone with an inserted `engine_rpz_status` row for the GUI
engine: `zonemd = 'failed'`, `zonemd_error = 'digest mismatch'`).

- [ ] Create the seed and `web/e2e/screens/44-rpz-zonemd.spec.ts`:
  ```ts
  import { test, expect, env, login } from "../fixtures";

  for (const width of [1280, 400]) {
    test(`RPZ ZONEMD verification at ${width}px`, async ({ page }) => {
      // TestGUICoverage counts: listRpzZones, updateRpzZone.
      await page.setViewportSize({ width, height: 900 });
      await login(
        page,
        env("NEXORA_E2E_OPERATOR_USER"),
        env("NEXORA_E2E_OPERATOR_PASSWORD"),
      );
      await page.goto("/rpz");
      const row = page.getByRole("row", {
        name: new RegExp(
          env("NEXORA_E2E_RPZ_ZONEMD_NAME").replaceAll(".", "\\."),
        ),
      });
      await row.getByRole("button", { name: "Edit" }).click();
      const dialog = page.getByRole("dialog");
      await dialog.getByTestId("rpz-zonemd-verify").click();
      await page.getByRole("option", { name: "Required" }).click();
      await dialog.getByRole("button", { name: "Save" }).click();
      await page.reload();
      await row.getByRole("button", { name: "Edit" }).click();
      await expect(
        page.getByRole("dialog").getByTestId("rpz-zonemd-verify"),
      ).toContainText("Required");
      await page.getByRole("dialog").getByTestId("rpz-zonemd-verify").click();
      await page.getByRole("option", { name: "If present" }).click();
      await page
        .getByRole("dialog")
        .getByRole("button", { name: "Save" })
        .click();
      await row.click(); // expands per-engine status
      await expect(page.getByTestId("rpz-zonemd-status").first()).toContainText(
        "Failed",
      );
      await expect(page.getByTestId("rpz-zonemd-status").first()).toContainText(
        "digest mismatch",
      );
    });
  }
  ```
- [ ] Run the GUI coverage run for this spec and expect FAIL: `rpz-zonemd-verify` not found.
- [ ] Implement:
  - the transfer RPZ edit and create dialogs get the verify select (Off, If present, Required,
    default If present; hidden for file sources);
  - the per-engine status table gets a ZONEMD column (`rpz-zonemd-status`: badge plus error text);
  - help entries in `filtering.ts`.
- [ ] Run `cd web && pnpm run typecheck && pnpm run lint` and the GUI run, and expect PASS.
- [ ] Report the paths. The lead commits `M8 T31: GUI RPZ ZONEMD verification`.

## Task 32: Operations guide for M8

Files: `docs/operations.md`, `web/src/help/topics/zones.md`, `web/src/help/topics/fleet.md`,
`web/src/help/topics/filtering.md`, the help topic file covering Settings (read the topics directory
at the M7 head), `deploy/deploytest/m8_docs_test.go` (created)
Interfaces: produces the headings the GUI help links to: "mDNS gateway and reflection", "ZONEMD",
"Oblivious DoH", "Catalog zones".

- [ ] Create `deploy/deploytest/m8_docs_test.go`:
  ```go
  package deploytest

  import (
  	"os"
  	"strings"
  	"testing"
  )

  func TestOperationsGuideCoversM8(t *testing.T) {
  	b, err := os.ReadFile("../../docs/operations.md")
  	if err != nil {
  		t.Fatal(err)
  	}
  	doc := string(b)
  	for _, want := range []string{
  		"\n## mDNS gateway and reflection\n",
  		"\n## ZONEMD\n",
  		"\n## Oblivious DoH\n",
  		"\n## Catalog zones\n",
  		"hostNetwork: true",
  		"/.well-known/odohconfigs",
  		"key_rotation_hours",
  		"ldns-verify-zone -Z",
  		"an empty catalog deletes every member zone",
  		"catalog_managed",
  	} {
  		if !strings.Contains(doc, want) {
  			t.Fatalf("docs/operations.md lacks %q", want)
  		}
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestOperationsGuideCoversM8 -count=1'`
      and expect FAIL: `lacks "\n## mDNS gateway and reflection\n"`.
- [ ] Write the four sections after "Encrypted DNS: DoT, DoH and DoQ":
  - **mDNS:** what it answers, route order, settings and defaults, the TTL cap, NXDOMAIN, the
    64-query cap, reflection rules, `engine.hostNetwork: true` or a macvlan interface, the kw
    limitation, metrics.
  - **ZONEMD:**
    - generation (SHA-384 SIMPLE; signed zones);
    - verification modes and statuses for secondaries and RPZ;
    - what a failure does;
    - how to check a zone externally with `ldns-verify-zone -Z` on an AXFR;
    - RFC 8976 §4 step 1 (no DNSSEC validation).
  - **Oblivious DoH:**
    - target and proxy, the DoH listener requirement, `/.well-known/odohconfigs`;
    - keys (`key_rotation_hours`, the 5-minute publish delay, validity);
    - statuses;
    - the proxy allow list and recursion ACL;
    - that targets see proxies, not clients;
    - KEK requirement.
  - **Catalog zones:**
    - producer (labels, allow-query default, transfer and TSIG, a BIND consumer example);
    - consumer (primaries, member lifecycle, clashes, broken catalogs, `catalog_managed`);
    - the warning sentence "an empty catalog deletes every member zone this catalog created".

  Add the M8 metrics to "Monitoring and alerts". Add the limitations (no `coo`/`group`, SHA-384
  only, no ODoH in standalone mode) to "Known limitations". Add one "Learn more" section per topic
  file naming the new `docs/operations.md` heading.

- [ ] Run `scripts/dev-exec.sh 'go test ./deploy/deploytest -count=1'`, then
      `scripts/pc-format.sh docs/operations.md web/src/help/topics/*.md`, and expect PASS, including
      M6's `TestHelpTopicsReferenceOperationsDoc`.
- [ ] Report the paths. The lead commits `M8 T32: operations guide for mDNS, ZONEMD, ODoH and catalog zones`.

## Task 33: Full verification

Files: none (verification only; failures are fixed by reopening the owning task)
Interfaces: consumes every earlier task.

- [ ] Run `scripts/dev-exec.sh 'make engine-test && make mgmt-test && make web-test && make lint'` and
      expect every suite green.
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e/... -count=1 -timeout 120m'` and expect
      PASS, including `TestGUICoverage` with the five M8 specs and every M8 operation counted, the
      three namespace lab tests, and all pre-M8 e2e tests.
- [ ] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --test hot_path_alloc -- --nocapture'`
      and expect `cache_hit_path_does_not_allocate` and `authoritative_answer_path_does_not_allocate`
      to pass with the M8 snapshot.
- [ ] Run `rg -n "debt: ODoH keys are ignored" engine/src` and expect no match (Task 19 removed it).
      Run `rg -n 'unimplemented!|todo!\(' engine/src/zonemd.rs engine/src/mdns engine/src/server/odoh.rs`
      and expect no match.
- [ ] Record each command's summary line in `.procoder/todo/` evidence for the M8 tasks.

## Task 34: Deploy M8 to kw and run acceptance

Files: `e2e/kw_smoke_m8_test.go` (created), `scripts/kw-acceptance.sh`
Interfaces: consumes everything; kw names from `docs/architecture.md` ("Deployment on kw").

- [ ] Create `e2e/kw_smoke_m8_test.go` with `TestKwSmokeM8`. It is gated like `TestKwSmokeM4` (the same
      environment variables for the kw API URL, admin credentials and the DNS addresses), and every
      scratch object is named `kw-m8-<unix time>` and deleted in `t.Cleanup`:
  1. Create a primary zone `kw-m8-<ts>.test.` with `zonemd_generate` and transfer allowed from the
     dev pod's address. AXFR it from `192.168.10.136:53`, write the transfer to a file, and require
     `ldns-verify-zone -Z <file>` to exit 0.
  2. Create a producer catalog `kw-m8-<ts>.catalog.test.` (transfer allowed from the dev pod) and put
     the zone into it. AXFR the catalog from `192.168.10.139:53` and require a PTR to the zone under
     `<32 hex>.zones.`.
  3. `getOdohSettings`; `updateOdohSettings` with `target_enabled: true`; `rotateOdohKey`; poll
     `https://192.168.10.136/.well-known/odohconfigs` every 10 s for up to 7 minutes until the new key
     is listed (normal 5-minute publish delay; never write to the kw database directly — lead decision
     2026-09-15). Then fetch `https://192.168.10.136/.well-known/odohconfigs` with the kw DNS TLS CA,
     and run one `harness.ODoHQuery` for `example.com. A` expecting HTTP 200 and a NOERROR answer. Restore
     the previous ODoH settings in cleanup.
  4. Create engine group `kw-m8-<ts>` without engines, and set and read back mDNS
     `{enabled: true, interfaces: ["eth0"], timeout_ms: 500, reflect: false}`.
  5. In cleanup, delete everything and assert with `listZones`, `listCatalogZones` and
     `listEngineGroups` that no `kw-m8-` object remains.
- [ ] Add `TestKwSmokeM8` to the `go test -run` list in `scripts/kw-acceptance.sh`.
- [ ] Run `scripts/dev-exec.sh 'go vet ./e2e/...'` and expect a clean result.
- [ ] Build and push the images for the M8 head with `scripts/build-image.sh`. Run
      `scripts/kw-deploy.sh` with the DNS probe (5 queries/s to 192.168.10.136 and 192.168.10.139).
      Expect zero lost probe queries. On any lost query, run `helm rollback nexora` and record why in
      the M8 issue comments.
- [ ] Run `scripts/kw-acceptance.sh` and expect `TestKwSmoke`, `TestKwFullProduct`,
      `TestKwFilterCategories` and `TestKwSmokeM8` to pass. On failure run `helm rollback nexora` and
      record why.
- [ ] Close #30, #31, #32 and #33, each with a comment naming the commit, the proving tests (the spec's
      acceptance criteria for that issue) and the kw result, noting for #30 that kw proves only the
      settings round trip because kw engines are not hostNetwork.
- [ ] Report the deploy output summary, the probe counts and the issue URLs.
