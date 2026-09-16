# Pi-hole versus Nexora: feature gap analysis

## Product decision

The user explicitly declined all identified Pi-hole gaps: they do not fit Nexora. This report is retained as research only. All recommendations and priority labels below are historical analysis, superseded by that decision; do not create backlog items or implementation work from them. See `.procoder/ask/decisions.md`.

## Scope and method

Compared Pi-hole's public v6 configuration documentation, domain/group documentation, CLI reference and online API schema with Nexora source at `0d9681b` and the current working tree. Pi-hole's online API reference tracks `master`; it is not a claim about an exact installed Pi-hole patch version. This is a documentation/source analysis, not a runtime interoperability test or performance benchmark.

“Missing” means no first-class implementation was found in Nexora's relevant engine, management API, GUI or CLI. Workarounds and external integrations are distinguished from native support. The split-DNS spec/plan is still draft planning and is not counted as implemented. No implementation, backlog seeding or production change is authorized by this report. The separately reported Account profile alignment issue remains pending.

## Summary

Nexora already covers the essential DNS-filtering role. Pi-hole's strongest remaining advantages are day-to-day troubleshooting, device-oriented administration, portable configuration and a lightweight LAN-appliance deployment. Nexora has broader authoritative DNS, encrypted transports, recursion and centrally managed fleet capabilities.

The most valuable additions are a timed filtering pause, deterministic policy diagnostics, richer explicit domain rules, privacy controls, client inventory and portable configuration import/export. DHCP and NTP are genuine differences but should be deliberate product choices rather than automatic parity work.

## Confirmed gaps and partial equivalents

### 1. Timed blocking pause — missing, high priority

Pi-hole supports `pihole disable 5m` with automatic re-enable [P3]. Nexora can enable/disable individual lists/categories and edit group selections, but its API has no temporary filtering-pause resource, expiry or visible countdown.

Evidence: `mgmt/api/openapi.yaml` filter-list/category/policy routes; `web/src/pages/FilteringPage.tsx`; `engine/src/filter.rs`.

Recommended: audited global/engine-group/client-policy pause with an absolute expiry enforced by engines, including during management disconnection and restart. Pause filtering only, never recursion ACLs, authoritative ACLs or DNSSEC validation. Define whether RPZ and safe-search are included instead of silently disabling security policies. Do not implement the timer solely in the browser.

### 2. Regex and query-type-aware domain rules — missing; simpler rules exist

Pi-hole supports exact and regex allow/deny entries, query-type restrictions, inverted matches and per-rule reply behavior [P2, P4]. Nexora's list filtering is domain-and-subdomain matching; there is no user-configurable regex filtering or equivalent query-type-conditioned rule model. Regexes used to validate API inputs are not filtering support.

Evidence: `docs/architecture.md` Filtering; `engine/src/filter/{index,lists}.rs`; `mgmt/internal/api/policyvalidate.go`; `mgmt/api/openapi.yaml`.

Nexora does have exact/wildcard rewrites and RPZ QNAME/actions, so “cannot express exact-name policy or custom answers” would be wrong. What is missing is Pi-hole's integrated manual-domain rule workflow and regex/query-type semantics, not every possible outcome.

Recommended: explicit exact/suffix/regex match types, allow/deny action, optional query types and explainable precedence. Keep ordinary lists on the current fast path. Prefer bounded linear-time regex execution; Pi-hole backreferences/approximate-match syntax must not be promised as compatible without an explicit implementation and migration policy.

### 3. Named-device inventory and non-IP client selectors — missing

Pi-hole exposes a network device table, MAC/vendor/hostname suggestions and client selectors for IP, CIDR, MAC, hostname or incoming interface [P1, P6]. Nexora policy selection is longest-prefix IP/CIDR matching; query logs show client addresses. Engine inventory is inventory of DNS servers, not of client devices.

Evidence: `engine/src/filter.rs` `PolicyTable::select`; `mgmt/internal/api/policyvalidate.go`; `engine/src/telemetry/querylog.rs`; API route inventory has no client-device resource.

Recommended: start with explicit client names and router/DHCP integration, then IPv4/IPv6 address association. Do not assume engines in Kubernetes can discover remote MAC addresses. Pi-hole itself documents locality limits for MAC recognition; NAT/shared resolvers hide clients for both products.

### 4. Additive membership in several filtering groups — different model

Pi-hole clients can belong to several groups and receive associated rules/lists [P5]. Nexora chooses one most-specific CIDR group, with that group's policy rather than a union of matching groups.

Evidence: `engine/src/filter.rs` `PolicyTable`; `web/src/help/topics/filtering.md`.

This is a semantic difference, not a broken feature. Consider reusable policy bundles/composition if needed. Do not silently change longest-prefix behavior or confuse filtering groups with the planned authoritative zone views.

### 5. Native privacy levels and selective log suppression — missing

Pi-hole offers levels that hide domains, hide domains and clients, or disable identifying query analysis/long-term logging; it also exposes API response exclusions for selected domains/clients [P1, P8]. API hiding and preventing collection are different promises.

Nexora exposes trace sampling and external telemetry/storage integration, but its query records carry the raw client address and queried name. No equivalent end-to-end privacy-mode policy was found. The AI endpoint privacy guard is not query anonymization.

Evidence: `engine/src/telemetry/{querylog,otlp}.rs`; `mgmt/internal/querylog/`; `web/src/pages/SettingsPage.tsx`.

Recommended: define collection-time suppression/redaction, storage retention, UI visibility and AI access separately. Redact before export, cover traces as well as query logs, and explain that a policy change does not erase historical external data. Preserve aggregate counters where possible.

### 6. Teleporter-style configuration export/import — missing; infrastructure backup exists

Pi-hole exports a ZIP of its configuration and supports selective import of configuration, lists, domains, clients, group associations and DHCP leases [P7]. Nexora has PostgreSQL/CNPG backups and zone-file import/export, but no portable whole-configuration GUI/API archive or Pi-hole migration importer.

Evidence: `docs/operations.md` Backup and restore PostgreSQL; `mgmt/api/openapi.yaml` zone import/export routes; `web/src/pages/ZoneImportExportTab.tsx`.

Recommended: versioned logical export, validation/dry run, conflict preview and selective restore. Exclude private keys, passwords, TSIG material and bearer tokens by default; any secret-inclusive archive needs explicit protection. A Pi-hole importer must report unsupported regex syntax and different allow/group precedence instead of silently changing policy.

### 7. Deterministic “which list/rule matches this domain?” search — partial

Pi-hole has `pihole query` and `/search/{domain}` for list membership [P3, P6]. Nexora query logs attribute the rule/list/category responsible for observed traffic. Its “Check domains” action is AI threat advice with historical evidence, not a deterministic search across every configured list for an arbitrary client/query.

Evidence: `web/src/pages/FilteringPage.tsx`; `mgmt/internal/ai/threat/check.go`; `web/src/pages/QueryLogPage.tsx`; `mgmt/api/openapi.yaml`.

Recommended: a model-independent diagnostic taking name, qtype, client address and engine group, reporting selected policy, matching lists/rules, winning precedence and configuration version. Offer both all-list search and effective-policy evaluation; they answer different questions. This is particularly valuable before a query appears in logs.

### 8. Lightweight durable query history — partial

Pi-hole keeps a local SQLite query database [P1, P2]. Nexora's builtin backend is bounded, per-management-instance memory; durable/shared history requires OpenSearch. Durable query history therefore exists, but not with Pi-hole's small standalone operational footprint.

Evidence: `mgmt/internal/querylog/{builtin,opensearch}.go`; `mgmt/internal/config/config.go`; `docs/operations.md` Known limitations.

Recommended: evaluate a supported lightweight persistent backend only if standalone installations matter. A local SQLite file is not automatically suitable for multi-replica management; define HA, retention and query/index budgets first.

### 9. Native local-account TOTP — missing; OIDC alternative exists

Pi-hole supports native TOTP for web/API authentication [P1]. Nexora supports local accounts, roles, API tokens and OIDC, but no local TOTP enrollment/challenge/recovery flow was found. An OIDC provider can supply MFA; that is not native local-account MFA.

Evidence: `mgmt/internal/auth/`; `mgmt/api/openapi.yaml` auth routes; `web/src/pages/{LoginPage,AccountPage}.tsx`.

Recommended: local MFA if local accounts remain a supported administrative entry point, with protected enrollment, recovery codes, rate limits and session/token semantics. Do not substitute MFA for the existing authorization model.

### 10. Bundled DHCP service — missing, strategic choice

Pi-hole has an optional DHCP service, lease ranges, static reservations, lease times and IPv6 support [P1]. Nexora has DNS dynamic UPDATE, but UPDATE is not DHCP. No DHCP server, lease database or DHCP management UI was found.

Evidence: `engine/src/server/`; `mgmt/api/openapi.yaml`; `docs/operations.md` ports/features; `mgmt/internal/dynupdate/`.

Recommendation: for the present fleet/Kubernetes architecture, prefer DHCP/router integration before building DHCP. A native service would need explicit interface/privilege, relay, lease persistence and failover design. Never deploy a competing DHCP server as part of a DNS feature rollout.

### 11. Bundled NTP server/client — missing, low priority

Pi-hole FTL can serve NTP on IPv4/IPv6 and synchronize system time [P1]. Nexora does not implement an NTP service. Dependence on accurate host time for DNSSEC/TLS does not make it an NTP client/server.

Recommendation: keep host time synchronization external unless Nexora is deliberately becoming a LAN appliance. Time-health diagnostics are more immediately useful than taking over host clock management.

### 12. Dedicated anti-bypass domain switches — missing convenience controls

Pi-hole has explicit settings for Firefox's automatic-DoH canary, Apple Private Relay discovery domains and Discovery of Designated Resolvers [P1]. No named equivalents were found in Nexora's engine/API/UI.

Existing lists, rewrites and RPZ can express related policies; this is chiefly a curated preset and response-semantics gap. Pi-hole uses specific NXDOMAIN/NODATA behavior, not merely any block response. None of these switches universally prevents encrypted DNS bypass: hard-coded resolvers and VPNs need separate network policy.

### 13. ECS-derived client identity — unsupported difference, not a default recommendation

Pi-hole can use EDNS Client Subnet information to identify clients behind a forwarding router [P1]. Nexora selects policies by the observed client address; no corresponding ECS identity override was found.

Evidence: `engine/src/edns.rs`; `engine/src/filter.rs`; source-visibility limitation in `docs/operations.md`.

The split-DNS draft intentionally excludes arbitrary ECS/header trust. Only consider a separately designed trusted-proxy feature with an explicit peer allowlist and abuse tests. Never trust arbitrary ECS to grant an internal zone view.

### 14. Appliance CLI and guided support diagnostics — partial

Pi-hole offers commands for list updates/search, timed disable, log tail, diagnosis, repair and update [P3]. Nexora has a management CLI for bootstrap/fleet enrollment, API/MCP interfaces, engine logs/metrics and deployment scripts, but not an equivalent consolidated filtering CLI or sanitized one-command diagnostic bundle.

Evidence: `mgmt/cmd/nexora-mgmt/{main,fleet_cli}.go`; `mgmt/api/openapi.yaml`; `scripts/`; `docs/operations.md` Troubleshooting.

Recommended: a thin API-backed operator CLI plus opt-in redacted diagnostic export. Do not copy host-level self-updating/repair commands into a Helm/operator-managed deployment or auto-upload sensitive diagnostics.

## Already covered: do not file these as missing

- **Blocklist subscriptions, allowlists and refresh:** Nexora already downloads/normalizes lists, supports allow sources, categories and per-policy selections. Matching/precedence still differs from Pi-hole.
- **IPv4/IPv6 per-client filtering:** present through CIDRs, though MAC/hostname selectors and additive group membership differ.
- **CNAME-cloaking inspection:** implemented; `docs/architecture.md` and engine filter/dispatch paths describe blocking a chain reaching a blocked name.
- **Local DNS records and aliases:** authoritative zones and exact/wildcard rewrites already cover these. Nexora has source-group-specific rewrites today; the new full authoritative view model is still only planned.
- **Conditional forwarding:** `/forward-zones` and `ForwardZonesSection.tsx` exist. A router-friendly reverse-DNS/client-name wizard could improve UX but is not missing conditional resolution.
- **DNSSEC validation, caching and rate limiting:** existing engine capabilities, not Pi-hole-only features.
- **Dashboard, live query log, filtering attribution and REST API:** present; history footprint, diagnostics and privacy controls are the gaps.
- **Backup capability:** present at infrastructure/database level; portable selective configuration restore is the gap.
- **Client-localized answers:** Pi-hole's `dns.localise` is dnsmasq local-record localization, not evidence of full authoritative zone-view, transfer and signing isolation. Do not treat it as identical to the planned split-DNS feature.

## Nexora capabilities beyond the Pi-hole core role

Nexora already provides built-in iterative recursion, encrypted DNS client transports (DoT/DoH/DoQ), encrypted upstreams, authoritative primary/secondary zones, zone signing and rollovers, TSIG transfers/UPDATE, RPZ, safe-search policies, RBAC/OIDC, mTLS fleet enrollment, scoped snapshots/staged rollouts, Helm/operator/CNPG integration, telemetry and AI/MCP operations.

This is not a claim that a Pi-hole installation cannot integrate other tools such as Unbound, encrypted-DNS proxies, an identity gateway or external monitoring. Compare native capability with native capability, not a bare Pi-hole against an entire external stack. No throughput or memory superiority is claimed without a benchmark.

## Suggested order, not an approved backlog

1. **Troubleshooting:** timed pause and deterministic policy/list diagnostics.
2. **Policy control:** explicit domain-rule editor, exact/suffix distinctions and bounded regex/query-type rules.
3. **Privacy and security:** collection-time privacy controls and local-account MFA.
4. **Everyday administration:** named clients/device integrations and selective configuration export/import, including Pi-hole migration analysis.
5. **Standalone ergonomics:** lightweight query history and a consolidated operator CLI/diagnostic bundle.
6. **Optional integrations/presets:** anti-bypass controls; decide separately whether DHCP/NTP belong in this product. Treat trusted ECS and additive group composition as explicit architecture choices.

Before implementing migration, test the semantic differences: Nexora suffix matching can affect more names than Pi-hole exact rules; Pi-hole allow/deny precedence differs; multi-group membership cannot be silently reduced to one longest-prefix group.

## Primary sources

- [P1 — Pi-hole configuration](https://docs.pi-hole.net/ftldns/configfile/): DHCP, NTP, client naming, ECS, localise, reverse servers, privacy, TOTP and special domains.
- [P2 — Pi-hole domain database](https://docs.pi-hole.net/database/domain-database/): exact/regex rules, subscribed allow/deny lists and precedence. Query retention settings are documented in P1.
- [P3 — Pi-hole CLI](https://docs.pi-hole.net/main/pihole-command/): timed disable, domain search, debugging, repair and maintenance.
- [P4 — Pi-hole regex extensions](https://docs.pi-hole.net/regex/pi-hole/): query-type restrictions, inverted matching, replies and regex syntax.
- [P5 — Pi-hole group examples](https://docs.pi-hole.net/group_management/example/): additive membership and list/domain assignments.
- [P6 — Pi-hole API](https://ftl.pi-hole.net/master/docs/) and [client schema](https://ftl.pi-hole.net/master/docs/specs/clients.yaml): network inventory, domain search and client identity types/limitations.
- [P7 — Teleporter API schema](https://ftl.pi-hole.net/master/docs/specs/teleporter.yaml): archived export and selective import.
- [P8 — Privacy configuration](https://docs.pi-hole.net/ftldns/configfile/#privacylevel): privacy levels; API exclusions documented on the same page.

Repository evidence is cited inline; route inventory was cross-checked against `mgmt/api/openapi.yaml`. A negative source search alone was not treated as proof of equivalent capability elsewhere. Tests were not run: no application code was changed, and this report makes no new runtime verification claim.
