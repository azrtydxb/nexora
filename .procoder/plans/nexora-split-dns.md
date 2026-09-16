# Nexora split-horizon DNS implementation plan

Status: draft — full first-release scope approved for planning, including DNSSEC and secondary views. Architecture review and implementation authorization are separate gates.

Spec: `.procoder/specs/nexora-split-dns.md`.

## Goal

Serve isolated authoritative zone views selected by observed client IPv4/IPv6 source, with usable GUI/API configuration and no change to existing default-only zones. The first release includes unsigned/signed primary and secondary views, independent signing lifecycles, unambiguous replication and validated end-to-end isolation. DNSSEC and secondary support are mandatory, not optional follow-ups.

## Architecture

- Preserve concrete `zones.id` for records, serials, images and journals. A default zone is the family anchor; named views are concrete zones referencing that anchor.
- Resolve logical origin first, then source prefix, then selected-view authorization and answering. Never retry against another view because the selected view lacks data or denies access.
- Replace the engine's origin-to-one-zone index with origin-to-family plus a concrete-ID index for lifecycle operations. Compile bounded IPv4/IPv6 matchers at snapshot load; benchmark before selecting a trie versus sorted-prefix representation.
- Carry source and concrete view identity through all authoritative paths, transfers, updates and telemetry. Do not reuse name-only indexes for mutable state.
- Stage compatibility support in both management and engines before activation. Normal proto unknown-field compatibility alone is insufficient for private records.
- Keep keys, denial chains, rollover acknowledgments, upstream bindings and refresh jobs concrete-view scoped. Proposed primary signing uses independent keys per view; signed secondaries preserve upstream signatures without local private keys.
- Distinguish client source selection from authenticated replication routing. Explicit per-family TSIG-to-view bindings let one secondary address replicate multiple views; ordinary queries and UPDATE do not inherit that exception.

## Constraints and release gates

- No production mutation during planning. Do not auto-classify current kw zones as internal/public.
- Build/test through `scripts/dev-sync.sh` and `scripts/dev-exec.sh`; retain raw Helm golden output if chart snapshots change.
- Reserve proto field numbers and migration sequence after checking main, not by copying another milestone's range.
- Existing auth, revision checks, DNSSEC and query ACL behavior remain enforced. No optional assertions to manufacture acceptance success.
- Treat spec review, plan review, task creation, implementation, review, tests, commit and deployment as separate gates. State-changing procoder workflow commands are invoked by the human.
- A rolling binary downgrade is not a safe rollback while view configurations exist. Remove named-view configuration with the new management binary, wait for application, then downgrade only after preflight permits it.

## Wave order and ownership

1. Contract/call-site audit and approval: Task 1.
2. Migration/model and protocol capability groundwork: Tasks 2–3, separate files; serialize any shared snapshot edits.
3. Management CRUD, snapshot assembly and engine routing: Tasks 4–6; merge protocol changes first.
4. Transfers/UPDATE and telemetry: Tasks 7–8 after concrete identity flows end to end.
5. Full DNSSEC and secondary lifecycles: Tasks 13–14 after Tasks 2–7; these new task IDs preserve earlier references and are not deferred until after deployment. Serialize shared API/snapshot changes.
6. GUI and conformance: Tasks 9–10 after Tasks 13–14 and their API contracts stabilize.
7. Documentation, staged deployment and acceptance: Tasks 11–12 after all feature tasks pass.

The API schema/generated files and snapshot builder have one writer at a time. Use isolated worktrees for parallel implementation; merge and review the integrated behavior before deployment.

## Task 1: Finalize contract and inventory identity-sensitive paths

Files: `docs/architecture.md`, the split-DNS spec/plan, `engine/src/authoritative/{set,state,loader,lookup,dispatch,xfr,update,notify_in,notify_out}.rs`, `engine/src/{runtime,cache}.rs`, `mgmt/internal/{zone,snapshot,dynupdate,xfrin,dnssec}/`, `proto/nexora/control/v1/control.proto`.

Steps:

- Record the user's full-scope decision; review S-10/S-13 key ownership, parent trust, transfer binding and secondary lifecycle design. Establish benchmark-limit ownership without reopening the answered scope question.
- Trace every `AuthSet.get/find/find_for_query`, name-keyed map, zone export/import, scheduler, transfer and UPDATE caller. Record which identity it requires: family, concrete view, or DNS origin.
- Trace transport source extraction for every DNS protocol, including DoH; identify any existing proxy support without changing trust semantics.
- Reserve protocol/migration identifiers and define exact API errors, revision fields, capability lifecycle and matcher representation.
- Review negative answers, glue, DS parent lookup and CNAME transitions as explicit leakage threats.

Done when: approved spec has no unresolved product boundary and architecture maps each name-only caller to a concrete identity strategy. No implementation starts with implicit DNSSEC/secondary deferrals.

## Task 2: Migrate concrete zones into families

Files: new migration in `mgmt/migrations/`, `mgmt/internal/zone/{model,service,validate,build}.go`, new `views.go`, corresponding tests, `mgmt/internal/store/` migration tests.

Steps:

- Add family reference, label and CIDRs; backfill existing rows as defaults without changing IDs, SOA or serials.
- Replace global origin uniqueness safely; enforce no nested views, matching origins and shared placement. Serialize family mutations on the default row.
- Implement named-view create/clone/update/delete and explicit family cascade. Clone records with new IDs; do not clone TSIG grants or signing state.
- Support per-view primary/secondary kind and signing state, including mixed families. Stage creation until bootstrap/signing/trust readiness is satisfied; make activation and explicit kind conversion atomic. Reject secondary record edits/local signing and family-placement bypasses. Reject unsupported downgrade while named views exist.
- Add revisioned, role-specific transfer/NOTIFY and upstream bindings with secret references and family-level uniqueness checks. Preserve existing default signing keys and secondary refresh state.
- Test concurrent duplicate CIDRs, normalized overlaps, 32-view/128-prefix boundaries, transaction rollback and default-only migration fixtures.

Done when: `TestZoneViewMigrationAndAPI` storage cases and existing zone/store tests pass; migration preserves old answers byte-for-byte where existing tests require it.

## Task 3: Add protocol identity and capability negotiation

Files: `proto/nexora/control/v1/control.proto`, generated Go/Rust bindings through existing commands, `mgmt/internal/control/`, `mgmt/internal/fleet/`, `engine/src/` control client discovered in Task 1, capability storage migration as needed.

Steps:

- Add family/view identity and match metadata without reusing numbers. Add a named split-DNS capability to Hello and persist what the current engine reports.
- Define handling of legacy absent identity, unknown capability, stale/disconnected engine state and a newly enrolled old engine.
- Add publication and delivery gates, not just a UI version check. An incompatible engine must never receive a snapshot containing private views.
- Gate activation until management rollout is homogeneous and all relevant engines support the feature; deployment tooling must establish this before allowing activation.

Done when: `TestSplitDNSCapabilityGate` includes old-engine fixtures and reconnect/new-enrollment cases. Default-only legacy protocol tests pass.

## Task 4: Expose view API and authorization

Files: `mgmt/api/openapi.yaml`, `mgmt/internal/api/{zones,zonefile}.go`, new `zone_views.go` and tests, `mgmt/internal/auth/permissions.go`, generated clients and server bindings.

Steps:

- Implement the spec's list/create/patch/delete/preview interfaces and concrete-ID operations. Root list endpoints remain default-only with view summaries.
- Apply existing operator/viewer role boundaries and revisions to new actions, including clone and cascade.
- Preview uses the same management-side matcher as validation; return only documented selection/ACL information.
- Record audit targets as concrete view plus family; test stale revisions, cross-family IDs, read-only access and root mutation bypasses.
- Extend import/export with explicit view context; importing ordinary zone text must not change matchers or another view.

Done when: API conformance and permissions tests pass, generated artifacts match schema, and errors distinguish conflict, validation, permission and not-found without revealing unauthorized records.

## Task 5: Publish atomic view-aware snapshots

Files: `mgmt/internal/snapshot/authzones.go`, snapshot tests, `mgmt/internal/zone/build.go`, `engine/src/authoritative/{loader,state,nzf}.rs`, `engine/src/runtime.rs`.

Steps:

- Emit complete families in deterministic order, each concrete image/journal identified independently of origin/serial.
- Include view identity in image/delta state keys; reject cross-view chains and malformed family membership.
- Account for matcher and all view image memory in snapshot admission. Build an entire candidate runtime before swapping it into service.
- Preserve old single-view snapshot format defaults and restart behavior; family edits publish transactionally.
- Prove interrupted loading, invalid CIDRs, duplicate defaults and memory exhaustion preserve the previous runtime.

Done when: snapshot round-trip, corruption, restart, delta replay and atomic rejection tests pass, with no name-keyed collision between two equal-origin views.

## Task 6: Select views and isolate answers/caches

Files: `engine/src/authoritative/{set,lookup,answer,dispatch}.rs`, new matcher module/tests, `engine/src/{runtime,cache}.rs`, transport/dispatcher callers identified in Task 1.

Steps:

- Build family lookup and allocation-free source selection; normalize mapped source addresses and use most-specific prefixes.
- Preserve longest suffix and DS parent behavior. Apply selected-view query ACL before serving records and never fallback on denial/missing data.
- Carry view context through wildcard, CNAME, referrals, glue, SOA and negative answers.
- Audit positive/negative/stale cache and in-flight request keys; bypass authoritative caching where already appropriate rather than introducing redundant caches.
- Test UDP/TCP/DoT/DoH/DoQ source selection and make forged ECS/headers unable to choose internal records.
- Benchmark single-view and bounded worst-case configurations; retain reproducible results and investigate regressions instead of relaxing limits silently.

Done when: Rust selection/answer tests, `TestSplitDNSAnswers`, `TestSplitDNSAccess` and `TestSplitDNSCacheIsolation` pass, including concurrent cache priming.

## Task 7: Isolate transfers, notifications and updates

Files: `engine/src/authoritative/{xfr,update,notify_in,notify_out,state}.rs`, `mgmt/internal/dynupdate/apply.go`, `mgmt/internal/xfrin/`, `mgmt/internal/control/server.go`, zone transfer API/UI contract and tests.

Steps:

- Implement source-selected transfers plus explicit verified TSIG-to-view bindings for AXFR/IXFR and apex bootstrap SOA. Apply selected-view transfer ACLs even for bound keys, refuse ambiguous/invalid bindings, and test that ordinary record queries/UPDATE remain source-selected. Pin one immutable view image for the entire transfer.
- Key journal/delta lookups and outgoing notification scheduling by concrete view ID. Validate ambiguous target/key combinations and document source mapping for external secondaries.
- Forward selected view identity and source for UPDATE; revalidate current matcher and grants in the same management transaction as prerequisites/mutation.
- Reject absent view identity for split families and reject configuration changes racing UPDATE instead of redirecting the write.
- Forward authenticated NOTIFY identity, peer, view and binding revision; revalidate at management before refresh. Replace origin-only scheduling in Task 14. Include same-source/distinct-key interoperability and rotation tests.
- Integrate signed UPDATE publication with Task 13; refuse secondary UPDATE rather than updating a sibling or silently forwarding it.

Done when: `TestSplitDNSTransfers`, `TestSplitDNSUpdate` and existing TSIG/secondary suites pass; cross-view failures disclose no records and mutate no unrelated journal. Full secondary lifecycle completion is required in Task 14.

## Task 8: Attribute queries and expose operational evidence

Files: `engine/src/telemetry/{querylog,otlp}.rs`, `mgmt/internal/querylog/`, `mgmt/internal/api/querylog_resolve.go`, `mgmt/api/openapi.yaml`, `deploy/kw/otelcol.yaml`, any additional collector/backend schemas present at implementation time.

Steps:

- Add zone/view ID and label fields to authoritative logs/traces, including NXDOMAIN/NODATA/REFUSED and transfer audit context where appropriate.
- Preserve/query/filter fields in builtin and OpenSearch and any other backends merged by then; add mapping/index migration if required.
- Treat historical rows without fields as unattributed, not as internal or default evidence.
- Keep client IPs out of metrics labels; test bounded label sets and old-client decoding.
- Attribute signing/rollover, trust-readiness, transfer/refresh and expiry failures to concrete views in operational status/audit records; distinguish SOA expiry from signature expiry.

Done when: `TestSplitDNSQueryLogBackends` proves attribution and filtering for every supported backend through real ingestion where the harness supports it.

## Task 9: Build the zone-view GUI

Files: `web/src/pages/{ZoneDetailPage,ZoneRecordsTab,ZoneImportExportTab,ZoneTransfersTab,ZoneDnssecTab,ZonesPage}.tsx`, new `ZoneViewsTab.tsx`, `web/src/api/zones.ts`, `web/src/pages/QueryLogPage.tsx`, `web/e2e/screens/split-dns-views.spec.ts`.

Steps:

- Show view selector and persistent identity context in all zone editing tabs; preserve deep links and default-only navigation.
- Add CIDR editor, clone/create/edit/delete flows, source preview and clear next-view/default fallback warnings.
- Expose per-view DNSSEC enable/DS export/rollover and parent-readiness status, family DS summary, secondary upstream/source/key bindings, refresh/expiry and validation status. Keep secondary records read-only; explicitly confirm kind conversion.
- Test signed cloning with fresh keys, staged bootstrap/activation failures and view-scoped rollover/refresh actions. Never show one view's keys or status as another view's.
- Display view attribution and filtering in query logs; do not confuse zone views with filtering policy groups.
- Exercise keyboard navigation, accessible labels, loading/errors, read-only users, stale revisions and destructive confirmations.

Done when: Playwright and component tests pass, including switching views without applying unsaved edits to the wrong view and default-only regression screens.

## Task 10: End-to-end security and compatibility suite

Files: new `e2e/split_dns_test.go`, `e2e/split_dns_cache_test.go`, `e2e/split_dns_transfer_test.go`, `e2e/split_dns_dnssec_test.go`, `e2e/split_dns_secondary_test.go`, `e2e/harness/`, appropriate engine/management test fixtures.

Steps:

- Provide two genuinely distinct observed sources, plus IPv6 fixtures, not spoofed headers. Use explicit test CIDRs and documentation-space returned addresses.
- Implement every spec acceptance test with precise record/flag/identity assertions; failure of either view must fail the test.
- Exercise concurrent queries, matcher edits, engine restart, snapshot rollback and old-client/capability failures.
- Include parent/child origins, DS unions and split-parent mappings, CNAME-to-other-family cases, NSEC/NSEC3 and KSK/ZSK rollovers. Validate through independent resolver instances with explicit test trust anchors, not just RRSIG presence.
- Test unsigned/signed primary and secondary views plus mixed families. Use an independent authoritative implementation for inbound signed transfers and outbound multi-view replication; include same-source/distinct-key and source-bound upstream selection.
- Inject bad signatures, mismatched DS, wrong keys, stale transfer completions, expired SOA/RRSIG and concurrent rebind/rollover events. Preserve all single-view DNSSEC/secondary regression tests.
- Add malicious cross-family view IDs, forged source metadata, unauthorized transfers/UPDATE and cache-poisoning attempts to adversarial tests.

Done when: all S-1 through S-13 criteria have runnable tests; full Linux engine/management/web/e2e suites pass without skips concealing required coverage.

## Task 11: Operator documentation and upgrade/rollback preflight

Files: `docs/architecture.md`, `docs/operations.md`, `deploy/kw/README.md`, `web/src/help/topics/zones.md`, `scripts/kw-deploy.sh`, deployment/operator compatibility validation if applicable.

Steps:

- Document complete-view semantics, CIDRs, default exposure, resolver/NAT limitations, IPv6 and private PTR examples.
- Explain transfer source/key mapping, NOTIFY routing, UPDATE authorization, independent DNSSEC keys, DS unions versus split-parent delegations, signed-secondary pass-through and upstream validation policy. Include resolver-cache isolation and NAT limitations.
- Give staged activation, kind-conversion and per-view recovery examples. External parent trust readiness cannot be reported as validated without observation or explicit operator acknowledgment.
- Implement and test downgrade/activation preflight: software rollout first, capability checks second, configuration activation last.
- Provide rollback steps preserving new binaries until all named views are removed and default-only configuration is acknowledged.
- Document that source-based routing does not authenticate clients and does not itself publish a zone to the Internet.

Done when: documentation examples match API/GUI, preflight tests block incompatible actions, and an operator can recover without exporting private records into default.

## Task 12: kw staged deployment and acceptance

Files: new `e2e/kw_split_dns_test.go`, `scripts/kw-acceptance.sh`, deployment evidence in `.procoder/notes/`, milestone tasks after approval.

Steps:

- Build from committed HEAD only after code review and Linux suite evidence. Run the commit gate; report environment failures separately rather than claiming green.
- Probe both production DNS addresses throughout rolling deployment and keep request counts, errors and raw logs. Require zero sampled query failures.
- Verify engine capabilities and connection state before creating a temporary test-only zone with default/internal views.
- Run `TestKwSplitDNS` from two observed source networks, documenting NAT/peer evidence; if the environment cannot supply two sources, stop acceptance rather than skip it.
- Exercise unsigned/signed primary and secondary views using temporary upstream fixtures and validating resolvers with private trust anchors. Prove independent refresh/expiry and signed positive/negative answers without changing public delegation. Missing signed-secondary evidence blocks acceptance.
- Run the entire strict kw acceptance suite, check no private answer appears from the external test source, and remove test objects with revision-safe cleanup.
- Record deployed commit/revision and evidence. Close governed tasks only through the human-invoked workflow after all criteria are met. Real production split-zone configuration requires the user's actual CIDRs and record values.

Done when: post-deploy acceptance and DNS probes pass, cleanup is verified, and all release gates have evidence rather than assumed compatibility.

## Task 13: Implement view-isolated DNSSEC lifecycle and trust readiness

Files: `mgmt/internal/dnssec/{enable,service,store,sign,maintainer,lifecycle,rollover}.go` and tests, `mgmt/internal/api/dnssec_zone.go`, `mgmt/internal/zone/{views,build}.go`, DNSSEC migrations as needed, `engine/src/authoritative/` signed answer/denial paths, `e2e/split_dns_dnssec_test.go`.

Steps:

- Audit and scope signing keys, signature caches, denial parameters, maintenance jobs, locks and DS acknowledgments by concrete ID. Existing default keys survive migration. Prevent key-tag/origin/serial collisions from merging state.
- Add independently signed primary views, safe signed cloning/import, staged activation and view-scoped UPDATE resigning. Never copy private keys or stored signing artifacts across views.
- Implement per-view DS export and family summary, hosted-parent consistency checks across CIDR intersections, and external-parent readiness acknowledgment/diagnostics. Cover both common-parent DS unions and split-parent delegation; do not silently disable validation for unsigned siblings.
- Exercise NSEC/NSEC3 positive, wildcard and negative responses and independent signature refresh and KSK/ZSK rollovers. Test overlapping rollovers with distinct DS acknowledgments and retained-key TTL windows.
- Fail closed on invalid signed-response state without stripping signatures or substituting another view; scope operational errors to the affected view. Verify cache context includes DNSSEC/denial data.
- Provide independent-validator fixtures, tampered/mismatched-proof cases and secure/insecure mixed-delegation examples. Coordinate signed-secondary wire preservation and trust policy with Task 14.

Done when: `TestSplitDNSSECViews` and all existing DNSSEC suites pass, independently validating both views throughout key rollover. No missing scope is replaced by a rejection of split-view signing.

## Task 14: Implement view-isolated secondary replication and signed transfers

Files: `mgmt/internal/xfrin/{scheduler,refresh,ixfr}.go` and tests, `mgmt/internal/zone/{model,service,views}.go`, `mgmt/internal/control/server.go`, `mgmt/internal/snapshot/authzones.go`, `engine/src/authoritative/{notify_in,state,loader}.rs`, `e2e/split_dns_secondary_test.go`, upstream/validator fixtures in `e2e/harness/`.

Steps:

- Replace the origin-only query in `Scheduler.Notify` with verified concrete-view routing and binding revalidation. No broadcast-to-siblings fallback; stale/forged bindings cannot enqueue another view.
- Implement per-view bootstrap/refresh/retry/failover, configured outgoing source and TSIG selection for SOA/AXFR/IXFR, and uniqueness validation for endpoint/key/source bindings. Reject unbindable source addresses before activation; account explicitly for NAT seen by the upstream.
- Preserve separate serials, journals, SOA timers, expiry and locks. Pin configuration revision in work items so rebind/deletion/kind conversion rejects stale completion. IXFR fallback never switches to a sibling's upstream.
- Stage new replicas until a complete image is ready. Support mixed primary/secondary families and explicit kind conversion while preserving healthy sibling service. Do not clone a secondary as if copied records proved upstream synchronization.
- Preserve transferred DNSKEY/RRSIG/NSEC/NSEC3 without local signing keys. Add explicit per-view DS/trust-anchor validation policy, validate before publishing when enabled, and retain last good data only within its SOA/signature validity window on failure.
- Test against an independent primary (for example BIND) and independent validating resolvers; verify an external secondary can replicate multiple Nexora views using distinct TSIG keys from the same IP. Cover refresh, NOTIFY, expiry, failover, key rotation, equal serials and invalid transfers.

Done when: `TestSplitDNSSecondaryViews` and `TestSplitDNSSignedSecondary` pass alongside existing secondary tests; a broken or expired view neither poisons nor refreshes a sibling. Signed-secondary interoperability and validation are required before Tasks 10–12 can complete.

## Spec coverage

- S-1: Tasks 2, 4, 5, 9, 10.
- S-2/S-3/S-4/S-5: Tasks 1, 5, 6, 10.
- S-6: Tasks 4, 9, 11.
- S-7: Tasks 8, 9, 10.
- S-8/S-9: Tasks 2, 3, 4, 7, 10, 11, 13, 14.
- S-10: Tasks 2, 4, 5, 6, 9, 10, 11, 12, 13, 14.
- S-13: Tasks 2, 3, 4, 5, 7, 9, 10, 11, 12, 14.
- S-11: Tasks 3, 5, 10, 11, 12.
- S-12: Tasks 2, 5, 6, 10, 12.

## Review blockers

The scope decision is resolved: the user requires DNSSEC and secondary views in the first release. Architecture review must still verify parent trust consistency, authenticated transfer selection, NOTIFY disambiguation and atomic secondary lifecycle before task seeding/implementation. The newly specified technical designs are proposals, not claims of existing behavior or separately approved choices. Resource/performance limits remain engineering targets to validate in Task 1, not measured claims. No production configuration change is authorized by this planning update.
