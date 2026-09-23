# nexora-m8-dns-protocols

Status: complete

## Problem

Nexora v1 serves, forwards, recurses and filters DNS, but four DNS protocols that homelab and ISP
operators ask for were left out of scope and filed as #30–#33:

- **mDNS (#30):** clients that only speak unicast DNS (Windows clients on another VLAN, containers,
  resolvers behind a router) cannot resolve `printer.local` names announced with Multicast DNS
  (RFC 6762). Today a `.local` query is forwarded or recursed and fails. Devices on one VLAN are
  also invisible to mDNS clients on another VLAN.
- **ZONEMD (#31):** a secondary zone or an RPZ feed transferred over an unauthenticated or corrupting
  path is applied without any integrity check, and Nexora's own primary zones publish no digest
  that downstream secondaries could check (RFC 8976).
- **ODoH (#32):** a DoH client's resolver learns both who asks and what is asked. Oblivious DoH
  (RFC 9230) splits that knowledge between a proxy and a target; Nexora can play neither role.
- **Catalog zones (#33):** every hosted zone must be added by hand to every external secondary
  (BIND, Knot, NSD, PowerDNS), and every zone of a remote primary must be added by hand to Nexora.
  RFC 9432 catalog zones automate both directions.

The roadmap orders M8 after M6 (Operator UX) and M7 (Hardening). The owner decided all four on
2026-09-14 (`.procoder/ask/decisions.md`: mDNS gateway with optional cross-VLAN reflection; ODoH
target and proxy roles; ZONEMD generate and verify; catalog producer and consumer).

## Users

- **Homelab operator:** resolves `.local` devices from clients that cannot do mDNS, makes a printer on
  the IoT VLAN visible to laptops on the trusted VLAN, and runs a secondary BIND at a friend's house
  that follows every zone added in Nexora without editing BIND.
- **ISP / enterprise operator:** checks that RPZ feeds and secondary zones arrive intact (ZONEMD),
  publishes digests for its own zones, provisions hundreds of zones on external secondaries through
  one catalog, consumes a customer's catalog, and offers an Oblivious DoH target or proxy for
  privacy-sensitive clients.
- **External DNS software** (BIND 9 as catalog consumer and as primary, `ldns-verify-zone`, ODoH
  clients): must interoperate with what Nexora publishes, following the RFCs.
- **Nexora maintainers:** need each protocol proven by tests that fail when the RFC behaviour breaks,
  including a real multicast proof that does not depend on kw's pod network.

## In scope

- [S-1] (#30) **mDNS gateway, off by default, per engine group.**
  - An engine group gains mDNS settings: enabled (default false), the interface names to query
    (at least one when enabled), a collection timeout in milliseconds (100 to 5,000, default 500),
    reflection on or off, and the reflection interface names (at least two when reflection is on).
    The management plane publishes them in the group's snapshot.
  - With the gateway on, an engine answers a unicast DNS query for a name under `local.` by sending
    an RFC 6762 §5.1 one-shot multicast query from an ephemeral port to 224.0.0.251:5353 (and
    ff02::fb:5353 when the interface has an IPv6 link-local address) on each configured interface.
    It collects the RFC 6762 §6.7 legacy unicast responses sent back to that port.
  - Route order for a query that is not answered from the cache: a hosted zone for the name, then a
    forward zone matching the name (longest match), then the mDNS gateway for names under `local.`,
    then forwarding or recursion as today.
  - A reply is accepted only when it arrives on the query's socket, carries the query ID, QR=1, and
    the question. Answer records are those whose owner and type match the question.
  - For query types A, AAAA, SRV and TXT the first reply with matching answers ends collection. For
    every other type (PTR included) the engine waits for the whole timeout and merges answers,
    dropping duplicates.
  - The client gets NOERROR with the answers, AA=0, and every TTL capped at 10 seconds (RFC 6762
    §6.7). With no matching answer by the timeout it gets NXDOMAIN without SOA, which is not cached.
  - Recursion ACL, filtering, rewrites and RPZ apply to these queries as to any recursion query.
    Answers are cached under the existing cache rules, and in-flight coalescing applies.
  - At most 64 gateway queries run at once per engine. Above that the client gets SERVFAIL, counted
    as dropped.
  - Metrics: `nexora_mdns_queries_total{result="answered|unanswered|dropped"}` and
    `nexora_mdns_interface_missing{interface}` (1 while a configured interface does not exist on the
    engine).
  - Design choices:
    - Only `local.` is gatewayed. The reverse zones of RFC 6762 §4 (`254.169.in-addr.arpa.`,
      `8.e.f.ip6.arpa.` and so on) keep today's resolution.
    - Additional-section records from responders are not passed on.
    - There is no "all interfaces" default, so an engine never multicasts on a network nobody chose.
- [S-2] (#30) **mDNS reflection across interfaces (VLANs).**
  - With reflection on, the engine joins 224.0.0.251 (and ff02::fb where the interface has an IPv6
    link-local address) on port 5353 on every reflection interface (`SO_REUSEADDR`/`SO_REUSEPORT`,
    so an avahi-daemon on the host keeps working).
  - Every multicast mDNS packet received on one reflection interface is resent unchanged to the
    group on each other reflection interface, with IP TTL / hop limit 255 (RFC 6762 §11) and
    multicast loopback off.
  - Loop and storm prevention:
    - packets whose source address belongs to one of the engine's reflection interfaces are dropped;
    - a packet whose payload digest was reflected in the last second on any interface is dropped
      (a table of 1,024 entries).
  - Unicast legacy responses are not reflected. The reflector runs on the control runtime, never on
    a query worker.
  - Metric `nexora_mdns_reflected_packets_total{from,to}`.
  - Design choice: packets are reflected without filtering by service type. Service filtering is out
    of scope.
- [S-3] (#30) **Proof without kw multicast.** kw engines are not `hostNetwork` pods, so multicast from
  a pod never reaches the home LAN.
  - The multicast proof runs in the dev pod inside an unprivileged user and network namespace
    (`unshare -Urn`, verified available in the kw dev pod on 2026-09-14). Veth pairs connect a
    "gateway" namespace running a standalone engine to "LAN" namespaces running a fixture mDNS
    responder and querier.
  - Real multicast crosses real links there, and nothing reaches the cluster network.
  - On kw, only the settings round trip is proven, on a scratch engine group without engines.
  - `docs/operations.md` states that the gateway needs `engine.hostNetwork: true` or an interface on
    the LAN segment (for example macvlan).
- [S-4] (#31) **ZONEMD generation for primary zones.**
  - A primary zone gains `zonemd_generate` (default false).
  - When set, every zone rebuild (`zone.Rebuild`) publishes one apex ZONEMD record: scheme 1
    (SIMPLE), hash algorithm 1 (SHA-384), serial equal to the new SOA serial, TTL equal to the SOA
    TTL.
  - The digest follows RFC 8976 §3:
    - RRs in DNSSEC canonical form and order (RFC 4034 §6), RRsets of one owner ordered by type;
    - duplicates once;
    - occluded data and glue included, records outside the zone excluded;
    - apex ZONEMD RRs and the RRSIGs covering the apex ZONEMD RRset excluded.
  - For DNSSEC-signed zones:
    - a placeholder ZONEMD RR is added before signing, so NSEC/NSEC3 bitmaps list ZONEMD;
    - the digest is computed after the SOA serial is set and the SOA re-signed;
    - then the ZONEMD RRset is signed with the zone's ZSK, without changing the serial (RFC 8976 §3.4).
  - Every version therefore carries a new ZONEMD. Deltas and IXFR out replace it like any changed
    RRset.
  - Operators cannot write ZONEMD records through the record API (400 `invalid_request`).
  - Design choice: SHA-384 only, the RFC's mandatory algorithm. SHA-512 is accepted when verifying.
- [S-5] (#31) **ZONEMD verification for secondary zones.**
  - A secondary zone gains `zonemd_verify`: `off`, `if_present` (default for new zones) or
    `required`. Existing zones migrate to `off` (lead decision 2026-09-15: an upgrade must not stop a
    remote zone that already carries a wrong ZONEMD from updating).
  - After each AXFR or IXFR the management plane builds the resulting full RR set and verifies it
    before anything is written, following RFC 8976 §4 steps 4 and 5 (a match with any supported
    scheme/hash tuple is enough; duplicate tuples do not count; a serial mismatch or a short digest
    fails).
  - The zone stores `zonemd_status` (`not_checked`, `off`, `absent`, `verified`, `failed`) and
    `zonemd_error`.
  - A failed verification, or `required` with no apex ZONEMD:
    - the transfer is discarded;
    - the zone keeps serving its previous version;
    - `last_error` names the primary followed by `zonemd:` and the reason;
    - the zone retries after the SOA retry interval, as for any failed refresh.
  - Design choice (RFC 8976 §4 step 1): the management plane performs no DNSSEC validation of
    transferred zones, so verification continues at step 4 for signed and unsigned zones alike.
    TSIG already authenticates the transfer source. The RFC explicitly allows this.
- [S-6] (#31) **ZONEMD verification for RPZ transfer zones.**
  - An RPZ zone with a transfer source gains the same `zonemd_verify` setting (default `if_present` for new RPZ zones, `off` for existing ones).
  - The engine verifies the full zone after each AXFR or applied IXFR, with the same rules as [S-5],
    in the RPZ transfer path.
  - A failure keeps the last good copy (in memory and in the engine's state directory) and records
    the error.
  - Each engine reports status and error per RPZ zone in its RPZ status. The RPZ page shows them.
  - Design choice: RPZ file uploads are not verified (the operator supplies the file).
- [S-7] (#32) **ODoH target role.**
  - Fleet-wide ODoH settings gain `target_enabled` (default false).
  - When on, every engine DoH listener also:
    - answers `GET /.well-known/odohconfigs` with the RFC 9230 §6 `ObliviousDoHConfigs` of the
      published keys (HPKE DHKEM(X25519, HKDF-SHA256), HKDF-SHA256, AES-128-GCM, the §9 mandatory
      suite);
    - accepts `POST` of `application/oblivious-dns-message` on the DoH path, decrypts the query,
      answers it through the normal query pipeline and returns the encrypted response with that
      content type and `Cache-Control: no-store`.
  - Rejections (RFC 9230 §4.3 and §8):
    - an unknown key id gets 401;
    - a decryption failure, non-zero padding, wrong message type or malformed body gets 400;
    - another content type or method on an ODoH request gets 415 or 405.
  - DNS errors are still HTTP 200.
  - Design choices:
    - the ACL, policy group and query log use the connecting peer's address (the proxy), because the
      target never learns the client;
    - `/.well-known/odohconfigs` is the de-facto discovery path of existing ODoH clients (RFC 9230
      leaves discovery open).
- [S-8] (#32) **ODoH keys.**
  - The management plane generates a 32-byte random seed per key and seals it under the KEK
    (`secrets.Box`).
  - A new key replaces the newest one every `key_rotation_hours` (1 to 720, default 24, the RFC's
    daily rotation advice), on the instance holding the advisory lock `nexora:odoh-rotate`.
  - Each key is:
    - accepted by engines at once;
    - listed first in `/.well-known/odohconfigs` only 5 minutes after creation, so every engine holds
      it before clients learn it;
    - valid until two rotation intervals after creation.
  - Keys travel only on the control stream in a new `OdohKeys` message, like `RpzTsigKeys`: never in
    a snapshot, `config_versions` or the engine state directory. An engine derives the HPKE key pair
    from the seed (RFC 9180 DeriveKeyPair).
  - `rotateOdohKey` forces a rotation. Expired keys are deleted.
- [S-9] (#32) **ODoH proxy role.**
  - Fleet-wide `proxy_enabled` (default false), `proxy_targets` (host or host:port, each with an
    optional PEM CA; at least one when enabled) and `proxy_timeout_ms` (100 to 10,000, default 2,000).
  - An engine DoH listener accepts `POST` of `application/oblivious-dns-message` on the DoH path
    with `targethost` and `targetpath` query parameters (RFC 9230 §4.1, template
    `https://<engine>/dns-query{?targethost,targetpath}`).
  - It forwards the body unchanged over HTTPS to `https://<targethost><targetpath>` with only the
    `content-type` and `accept` headers, and returns the target's status and body unchanged, adding
    `Proxy-Status: nexora; received-status=<code>`.
  - Rejections:
    - a missing or malformed parameter gets 400 with `Proxy-Status: nexora; error=http_request_error`;
    - a target not in the list (host and port compared; no port means 443), or a client outside the
      recursion ACL, gets 403 with `error=http_request_denied`;
    - a timeout gets 502 with `error=connection_timeout`, a TLS failure 502 with
      `error=tls_protocol_error`, any other connection failure 502 with
      `error=destination_unavailable`.
  - No client address, cookie or forwarding header is sent to the target.
  - A request carrying `targethost` is always a proxy request, even when the engine is also a target.
  - Metric `nexora_odoh_requests_total{role="target|proxy",status}`.
  - Design choice: the allow list is mandatory, so no engine is an open relay.
- [S-10] (#33) **Catalog zone producer.**
  - An operator creates a producer catalog with a zone name, an optional engine group, transfer
    `allow_cidrs` (at least one), an optional transfer TSIG key and NOTIFY targets. This creates a
    primary zone plus a `catalog_zones` row.
  - The zone's records are generated, never edited by hand (record API: 409 `catalog_managed`):
    - `@ NS invalid.` (RFC 9432 §4);
    - `version TXT "2"` (§4.2.1);
    - one `<label>.zones PTR <member>.` per member zone (§4.1), with TTL 0.
  - The label is the member zone's UUID as 32 lowercase hex digits. It stays stable for the zone's
    life, and a deleted and re-created zone gets a new label, which resets its state on consumers
    (§5.6).
  - A zone joins at most one producer catalog through its `catalog_zone_id`, set on create or
    update. A catalog zone cannot be a member.
  - Creating, deleting or moving a member regenerates and rebuilds the catalog zone in the same
    transaction, so its serial increases and NOTIFY goes to its targets.
  - The catalog zone's allow-query list defaults to `127.0.0.1/32, ::1/128` (RFC 9432 §6: limit who
    can query it). Transfers use the zone's transfer policy.
  - Design choice: the producer writes no `coo`, `group` or `ext` properties.
- [S-11] (#33) **Catalog zone consumer.**
  - An operator creates a consumer catalog with the catalog zone name, primaries (address and
    optional TSIG key) and an optional engine group. This creates a secondary zone plus a
    `catalog_zones` row. The catalog zone is transferred by the existing refresh loop.
  - After each successful refresh of a consumer catalog, the management plane processes the catalog:
    - **Valid catalog** (§4.2.1 version `"2"` exactly once; each member node one PTR; no two labels
      with the same member name): each member not yet present is created as a secondary zone with
      the catalog's primaries, TSIG keys and engine group, `catalog_zone_id` and
      `catalog_member_label`. A refresh is requested for it.
    - **Member removed** from the catalog: the zone this catalog created is deleted (§5.3).
    - **Label changed** for a member: the zone is deleted and created again, which resets its state
      (§5.4).
    - **Name clash** with a zone this catalog did not create: the incoming member is ignored and
      recorded as a clash (§5.2).
    - **Broken catalog:** nothing changes and the reason is stored (§5.1).
    - **Expired catalog zone:** it is not processed (§5.1).
  - Records the implementation does not use (`coo`, `group`, `ext`, unknown) are ignored (§3).
  - Consumer-created zones reject updates and deletion through the zone API with 409
    `catalog_managed`.
  - Deleting a consumer catalog keeps its member zones as ordinary secondaries (`catalog_zone_id`
    becomes empty).
  - Design choices:
    - the management plane is the consumer, because secondary zones are already transferred by the
      management plane and shipped to engines as NZF images;
    - members use the catalog's primaries (RFC 9432 §6 leaves member primaries to local
      configuration);
    - `coo` migration and `group` mapping are not implemented, which §4.3 allows.
- [S-12] **API, permissions and GUI.**
  - New operations:

    | operationId          | Method and path                         | Role     |
    | -------------------- | --------------------------------------- | -------- |
    | `listCatalogZones`   | `GET /catalog-zones`                    | viewer   |
    | `createCatalogZone`  | `POST /catalog-zones`                   | operator |
    | `getCatalogZone`     | `GET /catalog-zones/{catalogZoneId}`    | viewer   |
    | `deleteCatalogZone`  | `DELETE /catalog-zones/{catalogZoneId}` | operator |
    | `getOdohSettings`    | `GET /odoh`                             | viewer   |
    | `updateOdohSettings` | `PUT /odoh`                             | operator |
    | `rotateOdohKey`      | `POST /odoh/rotate-key`                 | admin    |

  - Schema changes:
    - `Zone`, `ZoneCreate` and `ZoneUpdate` gain `zonemd_generate`, `zonemd_verify` and
      `catalog_zone_id`;
    - `Zone` also gains the read-only `zonemd_status`, `zonemd_error` and `catalog_member_label`;
    - RPZ zone schemas gain `zonemd_verify`, and RPZ engine status gains `zonemd` and `zonemd_error`;
    - engine group schemas gain an `mdns` object.
  - Each mutation is audited under its operationId and publishes a config version where engines are
    affected.
  - GUI:
    - the zone detail page gains a ZONEMD card (generate for primaries, verify mode and status for
      secondaries) and a catalog select;
    - a **Catalog zones** page (`/zones/catalogs`, in the Zones navigation) lists catalogs, creates
      both roles, and shows members, clashes and the broken reason;
    - Settings gains an **Oblivious DoH** section (roles, proxy targets, rotation, key list, rotate
      button);
    - the engine group page gains an **mDNS** section;
    - the RPZ page gains the verify mode and per-engine ZONEMD status.
  - Every control has help text (M6 `HelpTip` and catalogue), and every page works at 400 px width.
- [S-13] **Contract and data numbering.**
  - Proto fields added to existing messages use 900–999:
    - `ConfigSnapshot` `mdns` (900) and `odoh` (901);
    - `RpzTransferSource` `zonemd_verify` (900);
    - `RpzZoneStatus` `zonemd` (900) and `zonemd_error` (901);
    - `ServerMessage` `odoh_keys` (900).
  - New messages `MdnsConfig`, `OdohConfig`, `OdohProxyTarget`, `OdohKeys` and `OdohKey`, and enums
    `ZonemdVerify` and `ZonemdStatus`.
  - Migrations: `01302_zonemd.sql`, `01303_catalog_zones.sql`, `01304_odoh.sql`,
    `01305_engine_group_mdns.sql`.
  - Playwright screen specs: `40-zonemd`, `41-catalog-zones`, `42-odoh`, `43-mdns` and
    `44-rpz-zonemd`.
- [S-14] **Operations guide and kw proof.**
  - `docs/operations.md` gains the sections "mDNS gateway and reflection", "ZONEMD", "Oblivious DoH"
    and "Catalog zones": settings, defaults, the hostNetwork requirement, the proxy ACL advice, and
    the catalog deletion warning of RFC 9432 §6.
  - `scripts/kw-acceptance.sh` runs a new `TestKwSmokeM8` after the M8 `scripts/kw-deploy.sh`, with
    zero lost probe queries on 192.168.10.136 and 192.168.10.139.

## Out of scope

- An mDNS responder advertising Nexora's own services, mDNS service filtering in the reflector,
  DNS-SD browsing domains (RFC 6763 `b._dns-sd._udp`), and gatewaying the RFC 6762 reverse zones.
- mDNS on kw's home LAN: kw engines stay non-hostNetwork, and the gateway stays off on kw.
- DNSSEC validation of transferred zones as part of ZONEMD (RFC 8976 §4 steps 1–3), ZONEMD for RPZ
  file uploads, ZONEMD schemes other than SIMPLE, and generating SHA-512 digests.
- ODoH client role (forwarding upstream queries through ODoH), Oblivious HTTP (RFC 9458), ODoH in
  standalone engines (keys need the management plane), and per-engine-group ODoH settings.
- Catalog `coo` migration, `group` properties, custom `ext` properties, catalogs consumed by engines
  directly, and a mass-deletion guard for consumer catalogs.
- DHCP (#34, pending decision), and every M9–M11 feature.

## Constraints

- Hot path rules from `docs/architecture.md` stay binding. There is no logging, allocation or lock
  held across packets on the cache-hit path.
  - The mDNS route is chosen only on the cache-miss path.
  - ODoH and the proxy run on their own HTTP request paths.
  - ZONEMD verification of RPZ zones runs on the control runtime.
  - `cache_hit_path_does_not_allocate` and `authoritative_answer_path_does_not_allocate` keep passing,
    with the mDNS gateway and the ODoH target enabled in the tested snapshot.
- Rolling upgrades:
  - an engine without M8 ignores the new snapshot fields and the `odoh_keys` message;
  - an M8 engine treats `ZONEMD_VERIFY_UNSPECIFIED` (older management plane) as off and an absent
    `mdns` or `odoh` config as disabled;
  - a management plane without M8 ignores the new status fields;
  - the M8 management plane sets `mdns` only while the gateway or reflection is on and `odoh` only
    while a role is on, so an existing engine group's snapshot content (and its rollout content
    hash) does not change until an operator turns a feature on.
- Migrations keep existing behaviour:
  - no zone generates ZONEMD;
  - no engine group runs the mDNS gateway;
  - ODoH stays off;
  - existing secondary zones and RPZ transfer zones get `off`; only zones created after the upgrade
    default to `if_present`.
- Secrets:
  - ODoH seeds are sealed under the KEK and travel only on the mTLS control stream;
  - TSIG secrets for catalog member zones reuse the catalog's key ids and are never copied;
  - nothing secret reaches audit rows, logs or snapshots.
- kw rules from the roadmap:
  - deploy only after every test passes, through `scripts/kw-deploy.sh` with the DNS probe;
  - never change 192.168.10.136 or 192.168.10.139;
  - roll back with `helm rollback nexora` on any lost query or failed acceptance;
  - kw acceptance creates only scratch objects and deletes them.
- Tests that open multicast sockets run only inside the network namespace lab, never on the pod's
  cluster interface.
- Dependencies:
  - Rust `odoh-rs` 1.0.5 (hpke 0.14, sha2 0.11, matching the engine's sha2);
  - Go e2e client `github.com/cloudflare/circl` v1.6.5 (HPKE), an independent implementation, so
    the ODoH test proves interoperability;
  - no new management plane dependency: seeds use `crypto/rand`, ZONEMD uses `github.com/miekg/dns`
    (already v1.1.73, which has the ZONEMD type).
- `TestGUICoverage` requires every new operation to be requested by a Playwright screen spec, and
  every new operation has the same role in `mgmt/internal/auth/permissions.go` and
  `web/src/auth/permissions.ts`.
- M8 builds on M6 and M7 as committed. When they changed a file named here, the task adapts to the
  committed code and updates its plan text.
- Rust 1.97, edition 2024, `cargo clippy -D warnings`. Go module `github.com/piwi3910/nexora`.

## Interfaces

- **Contract (`proto/nexora/control/v1/control.proto`):**
  ```proto
  // ConfigSnapshot
  MdnsConfig mdns = 900;   // absent: gateway and reflector off
  OdohConfig odoh = 901;   // absent: target and proxy off
  // RpzTransferSource
  ZonemdVerify zonemd_verify = 900;
  // RpzZoneStatus
  ZonemdStatus zonemd = 900;
  string zonemd_error = 901;
  // ServerMessage oneof
  OdohKeys odoh_keys = 900; // never persisted, never part of ConfigSnapshot

  enum ZonemdVerify { ZONEMD_VERIFY_UNSPECIFIED = 0; ZONEMD_VERIFY_OFF = 1; ZONEMD_VERIFY_IF_PRESENT = 2; ZONEMD_VERIFY_REQUIRED = 3; }
  enum ZonemdStatus { ZONEMD_STATUS_UNSPECIFIED = 0; ZONEMD_STATUS_OFF = 1; ZONEMD_STATUS_ABSENT = 2; ZONEMD_STATUS_VERIFIED = 3; ZONEMD_STATUS_FAILED = 4; }
  message MdnsConfig { bool enabled = 1; repeated string interfaces = 2; uint32 timeout_ms = 3; bool reflect = 4; repeated string reflect_interfaces = 5; }
  message OdohConfig { bool target_enabled = 1; bool proxy_enabled = 2; repeated OdohProxyTarget proxy_targets = 3; uint32 proxy_timeout_ms = 4; }
  message OdohProxyTarget { string host = 1; string ca_pem = 2; }
  message OdohKeys { repeated OdohKey keys = 1; }
  message OdohKey { bytes seed = 1; int64 publish_after_unix = 2; int64 not_after_unix = 3; }
  ```
- **HTTP API (`mgmt/api/openapi.yaml`):** the seven operations and schema changes of [S-12]. Error
  codes:
  - `catalog_managed` (409);
  - `invalid_request` (400) for a ZONEMD setting on the wrong zone kind, a record of type ZONEMD, a
    proxy without targets, or mDNS without interfaces.
- **Engine HTTP surface (DoH listeners):**
  - `GET /.well-known/odohconfigs`;
  - `POST <doh_path>` with `application/oblivious-dns-message`, as target or, with
    `targethost`/`targetpath`, as proxy.
- **Engine metrics:** `nexora_mdns_queries_total{result}`, `nexora_mdns_interface_missing{interface}`,
  `nexora_mdns_reflected_packets_total{from,to}`, `nexora_odoh_requests_total{role,status}`.
- **Engine modules:**
  - `zonemd` (RFC 8976 digest and verify);
  - `mdns::{iface, gateway, reflector}`;
  - `server::odoh`;
  - the `Route` enum of the recursor dispatch gains `Mdns`.
- **Management plane packages:**
  - `mgmt/internal/zonemd` (Digest, Verify, Apply);
  - `mgmt/internal/catzone` (catalog codec, producer regeneration, consumer reconciliation);
  - `mgmt/internal/odoh` (settings, key rotation, key loading for the control hub).
  - The `zone.Signer` interface gains a method that signs the ZONEMD RRset, and `zone.Service` gains
    a catalog-change hook.
- **GUI:**
  - route `/zones/catalogs`;
  - components `ZoneZonemdCard`, `CatalogZonesPage`, `OdohSection`, `EngineGroupMdnsSection`;
  - test ids `zonemd-generate`, `zonemd-verify`, `zonemd-status`, `zone-catalog-select`,
    `catalog-create`, `catalog-members`, `odoh-target-enabled`, `odoh-proxy-enabled`,
    `odoh-targets`, `odoh-rotate`, `mdns-enabled`, `mdns-interfaces`, `mdns-reflect`,
    `rpz-zonemd-verify`.
- **Test fixtures:**
  - `nexora-fixture mdns-responder` and `nexora-fixture mdns-query`;
  - the harness `netlab` (user and network namespaces with veth pairs);
  - the harness ODoH client;
  - RFC 8976 Appendix A zones A.1–A.3 as test vectors under `e2e/testdata/rfc8976/`.

## Data

- **PostgreSQL** (management plane owns all):
  - `zones`:
    - `zonemd_generate boolean NOT NULL DEFAULT false` (primary only, checked);
    - `zonemd_verify text NOT NULL DEFAULT 'if_present'` (`off|if_present|required`); the migration sets
      existing rows to `off`;
    - `zonemd_status text NOT NULL DEFAULT 'not_checked'`;
    - `zonemd_error text NOT NULL DEFAULT ''`;
    - `catalog_zone_id uuid NULL REFERENCES catalog_zones ON DELETE SET NULL`;
    - `catalog_member_label text NOT NULL DEFAULT ''`.
  - `rpz_zones.zonemd_verify` (same values and default). `engine_rpz_status` gains
    `zonemd text NOT NULL DEFAULT 'off'` and `zonemd_error text NOT NULL DEFAULT ''`.
  - `catalog_zones`:
    - `id`;
    - `zone_id` (unique, cascade delete);
    - `role` (`producer|consumer`);
    - `broken_reason`;
    - `processed_serial`;
    - `processed_at`;
    - `created_at`.
  - `catalog_member_issues(catalog_zone_id, member_name, label, issue, seen_at)`: consumer clashes,
    replaced on each processing.
  - `odoh_settings`: one row with `target_enabled`, `proxy_enabled`, `proxy_targets jsonb`,
    `proxy_timeout_ms`, `key_rotation_hours`, `revision`, `updated_at`.
  - `odoh_keys`: `id`, `seed_envelope` (NXE1), `created_at`, `publish_after`, `not_after`.
  - `engine_groups`:
    - `mdns_enabled`;
    - `mdns_interfaces text[]`;
    - `mdns_timeout_ms`;
    - `mdns_reflect`;
    - `mdns_reflect_interfaces text[]`;
    - with checks matching [S-1].
- **Engine memory:**
  - ODoH key ring (at most the non-expired keys, derived key pairs);
  - mDNS gateway sockets only while a query runs;
  - reflector sockets per interface and family;
  - a 1,024-entry dedupe table;
  - the RPZ ZONEMD verdict per zone.
  - Nothing new in the engine state directory. The RPZ last-good file is unchanged.
- **Zone data:** ZONEMD and its RRSIG are ordinary records in NZF images and deltas. Engines serve them
  unchanged.

## Edge cases

- mDNS:
  - A query for `local.` itself, or `_services._dns-sd._udp.local.` PTR, goes to the gateway like any
    name under `local.`.
  - A name under `local.` covered by a hosted zone or a forward zone never reaches the gateway.
  - A responder answering with the cache-flush bit set: the bit is cleared in the answer (RFC 6762
    §10.2 class field).
  - A reply with a different ID, no question, or arriving after the timeout is ignored.
  - A configured interface absent on an engine is skipped and reported in
    `nexora_mdns_interface_missing`. An engine with none of its interfaces present answers NXDOMAIN
    after no wait.
  - Truncated legacy responses (TC=1) are used as received. The engine does not retry over TCP
    (mDNS has no TCP).
  - A client asking over TCP, DoT, DoH or DoQ gets the same gateway result.
  - The reflector never reflects a packet onto the interface it came from, and never re-reflects
    its own output (source address and digest checks).
  - Two reflectors on the same segments do not loop: the digest window drops the echo.
  - An interface without an IPv4 address is used for IPv6 only, and one without either is reported
    missing.
- ZONEMD:
  - A zone whose apex holds several ZONEMD RRs verifies when any supported one matches. Two RRs with
    the same scheme and hash both fail (RFC 8976 §4 step 4).
  - A ZONEMD below the apex is digested as an ordinary RR and not used for verification.
  - An apex ZONEMD RRset holding only unsupported schemes or hashes fails ("no supported ZONEMD") in
    both `if_present` and `required`. Status `absent` means no apex ZONEMD exists at all.
  - A digest shorter than 12 octets, or of the wrong length for its hash, fails.
  - An IXFR is verified on the result of applying it, not on the difference. IXFR falls back to AXFR
    as today.
  - Turning `zonemd_generate` off rebuilds the zone without ZONEMD, and the next delta deletes it.
  - A signed zone whose signer is unavailable (HSM down) fails the rebuild as signing does today.
  - Occluded names below a delegation and names below a DNAME are digested.
- ODoH:
  - A request with `targethost` but no `targetpath` gets 400.
  - A `targetpath` that is not an absolute path gets 400.
  - A `targethost` with userinfo, a fragment or a path gets 400.
  - Target disabled: an ODoH POST to the DoH path gets 415 as today, and `/.well-known/odohconfigs`
    gets 404.
  - Proxy disabled: a request with `targethost` gets 403 `http_request_denied`.
  - Target enabled but no key yet (fresh install): configs get 503 and queries get 401.
  - A key rotation while clients hold the previous config: the previous key is accepted until its
    `not_after`.
  - A proxy target that redirects: the 3xx is returned unchanged (RFC 9230 §4.3). The proxy does not
    follow it.
  - The proxy's body limit is 65,535 octets plus 1,024 octets of ODoH overhead; larger bodies get 413.
  - A query that the target's ACL refuses returns an encrypted REFUSED with HTTP 200.
- Catalog zones:
  - Member names are compared case-insensitively. A PTR target without a trailing dot cannot occur
    on the wire.
  - A catalog listing its own name as a member: the member is ignored and recorded as a clash.
  - A producer member zone deleted: its PTR disappears in the same transaction.
  - A member moved from one producer catalog to another: both catalogs regenerate in one
    transaction.
  - An empty producer catalog is valid (version and NS only).
  - A consumer catalog whose refresh brings an unchanged serial is not processed again.
  - A clash that disappears (the operator deletes their zone) is resolved on the next processing: the
    member is created then.
  - Two consumer catalogs listing the same member: the first catalog to create it owns it, and the
    second records a clash.
  - A consumer member whose primaries refuse the transfer shows the normal secondary refresh error.
    The catalog stays valid.

## Failure modes

- **No multicast route or interface on the engine** (non-hostNetwork pod): gateway queries get no
  replies and clients get NXDOMAIN after the timeout. `nexora_mdns_queries_total{result="unanswered"}`
  grows, and the operations guide explains why.
- **mDNS storm or a misbehaving responder:** the 64-query cap gives SERVFAIL. The reflector drops by
  source and digest window. Worker threads are never blocked, because the gateway runs as a miss
  task and the reflector on the control runtime.
- **Socket errors** (interface removed while running): the query treats that interface as missing,
  and the reflector reopens its sockets when the next snapshot applies. Errors are logged at most
  once per minute per interface on the control runtime.
- **ZONEMD mismatch on a secondary or RPZ zone:** the previous data keeps serving, the error is
  visible in the zone or RPZ status, and retries follow the SOA retry interval. A zone that never
  loaded stays unloaded.
- **ZONEMD computation error on a primary** (for example an RR miekg cannot pack canonically): the
  mutation fails with 500 and nothing is published, as with any rebuild error.
- **KEK unavailable:** ODoH key rotation fails and is retried on the next tick. Existing keys keep
  working until `not_after`. The settings page shows the newest key's age.
- **ODoH target unreachable, slow or failing TLS:** the proxy answers 502 with the Proxy-Status error
  within `proxy_timeout_ms`, and nothing is retried.
- **Management plane down:** engines keep their ODoH keys in memory until `not_after`, then answer 401. Catalog processing and ZONEMD verification of secondaries pause. Engines keep serving the
  last images.
- **Catalog primary unreachable:** normal secondary refresh failure. At SOA expire, catalog processing
  stops but member zones are not removed (RFC 9432 §5.1).
- **Broken catalog from the primary:** stored `broken_reason` (for example `version property missing`),
  no member change, and processing resumes when the catalog becomes valid.
- **An empty catalog published by mistake:** every member zone this catalog created is deleted, as
  RFC 9432 requires. The operations guide warns about it, and the audit log names every deleted zone
  (`system:catzone`).

## Acceptance criteria

- [ ] [S-13] `TestContractM8FieldsRoundTrip` round-trips a snapshot with `mdns` and `odoh`, an RPZ
      transfer source with `zonemd_verify`, an RPZ status with `zonemd`, and a `ServerMessage` with
      `odoh_keys`, and asserts field numbers 900/901. `TestSnapshotCannotReachOdohKeys` fails if any
      `ConfigSnapshot` field reaches `OdohKeys`. Fails if a field is renumbered or keys become
      snapshot-reachable.
- [ ] [S-13] `TestM8MigrationKeepsBehaviour` migrates a database holding a secondary zone, a primary
      zone, an RPZ transfer zone and an engine group from the M7 head to the M8 head. It asserts
      `zonemd_generate=false`, `zonemd_verify=off` on the existing zones (and `if_present` on a zone created
      after the migration), `mdns_enabled=false`, one `odoh_settings`
      row with both roles off, and a group snapshot built after the migration with no `mdns` and no
      `odoh` config and `zonemd_verify` OFF on the existing RPZ transfer zone. Fails if a migration
      changes the published behaviour of an existing install.
- [ ] [S-4] [S-5] Go `TestDigestMatchesRFC8976AppendixA` computes the digests of Appendix A.1, A.2
      and A.3 from `e2e/testdata/rfc8976/` and compares them to the published values.
      `TestVerifyRules` covers:
  - serial mismatch;
  - duplicate scheme/hash tuples;
  - unsupported hash only;
  - a short digest;
  - non-apex ZONEMD;
  - `if_present` without ZONEMD (absent);
  - `required` without ZONEMD (failed);
  - a one-bit change in any RR (failed).

  The Rust tests `zonemd::tests::digest_matches_rfc8976_appendix_a` and `zonemd::tests::verify_rules`
  (run by `make engine-test`) assert the same cases. Fails if either implementation digests one
  vector differently from the RFC.

- [ ] [S-4] `TestZonemdGeneratedForPrimaryZones` creates an unsigned and a DNSSEC-signed primary zone
      with `zonemd_generate`, adds a record to each, AXFRs both from a managed engine, and runs
      `ldns-verify-zone` on each transfer. It asserts:
  - the ZONEMD serial equals the SOA serial after every change;
  - `zonemd.Verify` says `verified`;
  - the signed zone's apex NSEC bitmap lists ZONEMD, and its ZONEMD RRSIG validates with the ZSK;
  - an IXFR out between two versions deletes the old ZONEMD and adds the new one;
  - `createZoneRecord` with type ZONEMD gets 400.

  Fails if a digest does not verify externally, the serial lags, or the signature is missing.

- [ ] [S-5] `TestZonemdSecondaryVerification` uses a BIND `named` primary serving Appendix A.2 as a
      Nexora secondary with `if_present` and expects `verified` and the zone answering. Then:
  - `named` serves a newer serial whose ZONEMD digest was not recomputed: the zone keeps the old
    serial on the engine, `zonemd_status=failed`, and `last_error` contains `zonemd:`;
  - `required` against a zone without ZONEMD: the zone never loads;
  - `off`: the tampered zone loads.

  The unit test `TestRefreshDiscardsTransferFailingZonemd` proves no `zone_records` row changes on
  failure for AXFR and IXFR. Fails if a failing transfer is applied or a passing one is refused.

- [ ] [S-6] `TestRPZZonemdVerification` serves an RPZ zone with a valid ZONEMD from `named`. Engines
      apply its policy and report `zonemd=verified` through `listRpzZones` status. Then:
  - a newer serial with a stale digest: the engine keeps enforcing the previous policy and reports
    `failed` with an error;
  - `required` without ZONEMD: nothing is applied.

  The Rust test `rpz_transfer_failing_zonemd_keeps_last_good` (run by `make engine-test`) proves the
  in-memory and persisted copy stay unchanged. Fails if a failing RPZ transfer is applied.

- [ ] [S-1] [S-3] `TestMdnsGatewayNetLab` runs in `unshare -Urn` with a standalone engine on veth
      `gw0` and `nexora-fixture mdns-responder` on the peer in another namespace. The responder
      announces `printer.local` A 10.254.0.9, AAAA fd00::9, and three `_ipp._tcp.local` PTR
      instances. The test asserts, over UDP and TCP:
  - `printer.local A` answers 10.254.0.9 with TTL at most 10 and AA=0;
  - `printer.local AAAA` answers fd00::9;
  - `_ipp._tcp.local PTR` returns all three instances after the timeout;
  - `nothere.local A` returns NXDOMAIN after the timeout;
  - `kw.local` covered by a forward zone is answered by the fixture upstream, not the responder;
  - `nexora_mdns_queries_total` counts answered and unanswered;
  - with a missing interface name, `nexora_mdns_interface_missing` is 1.

  Fails if any answer is wrong, a TTL exceeds 10, or the forward zone loses precedence.

- [ ] [S-1] `TestMdnsOffForwardsLocalNames` (standalone engine, no namespace) proves `printer.local`
      is forwarded to the fixture upstream when the snapshot has no `mdns` config. The Rust tests
      `mdns::gateway::tests::reply_checks_and_ttl_cap`, `mdns::gateway::tests::first_answer_ends_unique_types`
      and `mdns::gateway::tests::inflight_cap_gives_busy` (run by `make engine-test`) check ID,
      question and socket matching, the TTL cap, cache-flush clearing, collection rules and the
      64-query cap against a unicast fake responder. `cache_hit_path_does_not_allocate` passes with
      the gateway enabled. Fails if disabling mDNS changes routing, or a mismatched reply is
      accepted.
- [ ] [S-2] [S-3] `TestMdnsReflectorNetLab` builds namespaces `lanA`–`gw`–`lanB`, with the engine
      reflecting between `gwA` and `gwB` and the responder in `lanA`. `nexora-fixture mdns-query` in
      `lanB` sends a port-5353 multicast query for `printer.local` and asserts:
  - it receives the responder's multicast answer;
  - the packet counter `nexora_mdns_reflected_packets_total{from="gwA",to="gwB"}` is at least 1;
  - within 2 s the querier sees each reflected packet at most once;
  - the responder never receives its own answer back;
  - with reflection off, the querier gets no answer within 2 s (asserted after the positive run).

  The Rust test `mdns::reflector::tests::dedupe_and_source_filter` (run by `make engine-test`) covers
  the digest window and the source check. Fails if packets loop, duplicate, or do not cross.

- [ ] [S-7] [S-8] `TestODoHTargetAndProxy`, as target, runs with two managed engines with DoH and the
      harness ODoH client (circl HPKE). With `target_enabled`:
  - `GET /.well-known/odohconfigs` on engine B returns one X25519/HKDF-SHA256/AES-128-GCM config;
  - an encrypted `www.example.test A` query answers 192.0.2.10 with HTTP 200,
    `application/oblivious-dns-message` and `Cache-Control: no-store`;
  - a garbled ciphertext gets 400, an unknown key id 401, and `application/json` 415;
  - `rotateOdohKey` adds a key that is accepted at once, listed first only after `publish_after`
    (clock injected through the key's timestamps), while the old config still works;

  The Rust tests `server::odoh::tests::target_round_trip` and
  `server::odoh::tests::rejections_map_to_status` (run by `make engine-test`) cover the codec.
  `TestOdohKeyRotation` proves due rotation, `publish_after`, `not_after`, deletion of expired keys
  and that seeds are stored only as NXE1. Fails if an RFC 9230 status is wrong, a rotated key breaks
  old clients, or a seed is stored in clear.

- [ ] [S-9] `TestODoHTargetAndProxy`, as proxy (engine A with `proxy_enabled` and target B allowed),
      asserts:
  - a query through A to B decrypts at the client with the same answer;
  - B sees only A's address;
  - the request B receives carries no `x-forwarded-for`, `forwarded` or `cookie` header (captured
    through a fixture target);
  - a target not in the list gets 403 with `Proxy-Status` `http_request_denied`;
  - a missing `targetpath` gets 400 `http_request_error`;
  - an unreachable target gets 502 `destination_unavailable`, and a black-hole target 502
    `connection_timeout` within `proxy_timeout_ms` + 500 ms;
  - B's 401 is relayed unchanged with `received-status=401`;
  - a client outside the recursion ACL gets 403.

  The Rust test `server::odoh::tests::proxy_target_matching` covers host and port matching. Fails if
  the proxy relays to an unlisted host, leaks a client header, or alters the target's status.

- [ ] [S-10] `TestCatalogZoneProducer` creates a producer catalog `catalog.test.` and two member zones,
      and starts BIND `named` as a catalog consumer (`catalog-zones` with default-primaries of the
      engine). It asserts:
  - `named` serves both member zones' SOA;
  - AXFR of the catalog from the engine shows `version TXT "2"`, `NS invalid.` and two
    `<32-hex>.zones PTR` records with the zones' UUID labels;
  - removing one member (`catalog_zone_id: null`) makes `named` stop serving it;
  - deleting and re-creating a member zone gives a new label;
  - `createZoneRecord` on the catalog zone gets 409 `catalog_managed`.

  The unit test `TestCatalogBuildIsStable` proves record generation is deterministic. Fails if BIND
  cannot consume the catalog or membership changes do not regenerate it in the same version.

- [ ] [S-11] `TestCatalogZoneConsumer` uses a `named` primary serving a version 2 catalog with members
      `a.cat.test.` and `b.cat.test.` plus both zones, and an operator zone `c.cat.test.` created
      first in Nexora. It asserts:
  - Nexora creates both secondaries with the catalog's primaries and TSIG key, and engines answer
    their records;
  - adding `c.cat.test.` to the catalog records a clash and leaves the operator zone untouched;
  - removing `b` deletes that zone;
  - changing `a`'s label recreates it with a new id;
  - a catalog update to version `"1"` stores `broken_reason` and changes nothing, and version `"2"`
    resumes processing;
  - `updateZone` and `deleteZone` on a member get 409 `catalog_managed`;
  - `deleteCatalogZone` keeps the members as plain secondaries.

  The unit test `TestCatalogParseBrokenRules` covers:
  - a missing, duplicated or other version;
  - two PTRs at one member node;
  - one member name under two labels;
  - `coo`, `group` and `ext` records ignored.

  Fails if any RFC 9432 §5 rule is violated.

- [ ] [S-12] `TestCatalogZonesAPI`, `TestZonemdAPI`, `TestOdohSettingsAPI` and `TestEngineGroupMdnsAPI`
      prove request validation (400 for ZONEMD on the wrong kind, a proxy without targets, mDNS
      without interfaces, reflection with one interface), roles (viewer 403 on mutations, operator
      403 on `rotateOdohKey`), audit rows without secrets, and the snapshot fields each change
      publishes. `TestPermissionsCoverEveryOperation` passes. Fails if an operation lacks a role, a
      mutation is unaudited, or a setting does not reach the snapshot.
- [ ] [S-12] Playwright screen specs run by `TestGUICoverage`, each at desktop and 400 px width:
  - `web/e2e/screens/40-zonemd.spec.ts` toggles generation on a primary, puts it into a producer
    catalog with the catalog select, sets `required` on a secondary and sees the status badge;
  - `41-catalog-zones.spec.ts` creates producer and consumer catalogs and sees the seeded members and
    clash;
  - `42-odoh.spec.ts` enables both roles, adds a proxy target, rotates the key and sees two keys;
  - `43-mdns.spec.ts` enables the gateway with two interfaces and reflection and reloads;
  - `44-rpz-zonemd.spec.ts` sets the RPZ verify mode and sees engine status.

  `pnpm lint` (help check) passes with every new control. Fails if a setting does not persist across
  reload, an operation is uncovered, or a control lacks help.

- [ ] [S-1] [S-7] `cache_hit_path_does_not_allocate` and `authoritative_answer_path_does_not_allocate`
      in `engine/tests/hot_path_alloc.rs` (run by `.github/workflows/ci.yml`) pass with a snapshot that enables the mDNS gateway and the
      ODoH target. Fails if either feature allocates on the cache-hit path.
- [ ] [S-14] `TestKwSmokeM8`, run by `scripts/kw-acceptance.sh` after the M8 `scripts/kw-deploy.sh`,
      covers:
  - a scratch primary zone with `zonemd_generate`, AXFR from 192.168.10.136, passing
    `ldns-verify-zone`;
  - a scratch producer catalog listing that zone, whose AXFR from 192.168.10.139 shows the member
    PTR;
  - ODoH target enabled for the test, one encrypted query through `https://192.168.10.136/dns-query`
    answered once the rotated key is published after the normal 5-minute delay (the test never
    writes to the kw database directly), then the previous settings restored;
  - the mDNS settings of a scratch engine group without engines round-tripping;
  - everything scratch deleted.

  The deploy probe records zero lost queries on 192.168.10.136 and 192.168.10.139.
  `TestOperationsGuideCoversM8` in `deploy/deploytest` fails if `docs/operations.md` lacks any of the
  four M8 sections or the hostNetwork note. Fails if a probe query is lost, an acceptance step fails,
  or a scratch object remains.

## Open questions
