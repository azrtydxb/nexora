# nexora-m6-operator-ux

Status: complete

## Problem

Nexora v1 (M1–M5 and filter categories) is deployed on kw and resolves the home LAN, but the operator
console lags behind the engine. Issues #54–#67 were filed on 2026-09-14 while using it:

- The query log cannot search part of a name on OpenSearch, cannot combine values in one filter, and
  does not say why a query was blocked, allowed, rewritten or refused.
- The dashboard shows four tiles and one chart although engines export much richer telemetry.
- The navigation hides where resolution is configured ("Upstreams") and spreads filtering over
  unrelated items.
- Settings have no explanation, and page descriptions use architecture jargon.
- One implicit access list mixes resolver access and authoritative access, and no hosted zone can be
  restricted to internal networks.
- Forwarding pays a full timeout for a slow upstream.
- Engines have no in-console metrics or logs: logs only exist in `kubectl logs`.
- Users cannot change their own password.
- Nobody can see which build is running.

The owner wants these fixed first (roadmap M6, before hardening, protocols, platform, backends and
AI), because the console is now the daily tool for the kw deployment.

## Users

- **Homelab operator** (operator or admin role): checks why a site broke, reads engine logs without
  `kubectl`, restricts an internal zone to the LAN, and sees at a glance whether engines, upstreams and
  certificates are healthy.
- **ISP / enterprise operator:** needs fleet-wide traffic, latency, filtering and DNSSEC graphs, top
  lists, per-engine drill-down, and parallel upstream racing to cut tail latency.
- **Viewer:** reads dashboards, query log and help. Changes only their own password and profile.
- **Every signed-in user:** needs in-app help for every setting, and needs to see the running version.

## In scope

- [S-1] (#54) Query log partial name match: `name` matches a case-insensitive substring of the query
  name, ignoring a trailing dot, identically on the builtin and OpenSearch backends. OpenSearch uses a
  `wildcard` query with `case_insensitive: true` on `attributes.dns.question.name.keyword`, with `*`,
  `?` and `\` in the input escaped. The OpenSearch request timeout stays 5 s. The GUI Name field says
  it matches part of a name.
- [S-2] (#56) Query log multi-value filters. `qtype`, `rcode`, `cache`, `filter`, `category`, plus the
  new `source`, `list_id`, `policy_group` and `engine_id`, each accept repeated query parameters, and
  a single value keeps working. Values within one parameter are OR-ed; different parameters are
  AND-ed. OpenSearch uses `terms`; builtin uses set membership. The GUI uses a multi-select with search
  that shows "A, AAAA" or "3 selected" and has a clear option. The selection lives in the page URL, so
  it survives reload, live refresh and paging.
- [S-3] (#61) Decision reason in the query log.
  - The engine attributes allowed hits the same way as blocked hits, through the decision cache too.
  - For blocklist, category and allowlist hits the engine records the matched rule (the listed
    suffix). For rewrites it records the matching rewrite rule, for RPZ hits the RPZ zone, and for ACL
    refusals which ACL refused.
  - The management plane adds these fields to `QueryLogRecord`: `source` (blocklist, category,
    allowlist, rpz, rewrite, acl), `list_name` (resolved from `list_id` at read time), `rule`, policy
    group id and name, RPZ zone id, name and action, and the rewrite answer.
  - The GUI replaces the Category column with a Reason column, for example
    `Blocklist · OISD Big · ads`. A row detail shows the rule, list id and policy group. New filters
    cover source, list and policy group.
- [S-4] (#55) Dashboard.
  - A time range selector (15 min, 1 h, 6 h, 24 h, 7 d) with auto refresh.
  - Traffic by transport and by response code.
  - Latency p50/p95/p99, overall and uncached.
  - Cache hit/miss/stale ratios, plus cache entries and memory per engine.
  - Answers by route. Recursion upstream queries, timeouts and lame servers. Resolution failures.
  - Filtering: blocked and rewritten rate, blocked by category, filter index memory per engine.
  - DNSSEC secure/insecure/bogus over time.
  - Top domains, top blocked domains, top clients and top categories from the query log backend.
  - A per-engine fleet table and engine groups at a glance.
  - A health panel: disconnected engines, stale categories, upstreams down, DNS certificate expiry
    within 14 days, trust anchor refresh failures, export drops.
  - The data comes from the extended engine `Stats` samples, a 5-minute rollup kept 8 days, and
    backend top-N aggregations, with no per-query database writes. Three new viewer operations serve
    it.
- [S-5] (#57) The Upstreams navigation item and page are renamed **Forwarding & recursion**. Design
  choice: the route becomes `/resolution`, the path the issue proposed, which matches the existing
  `/api/v1/resolution` API. `/upstreams` redirects there. The subtitle explains that the page covers
  resolution mode, forward zones and upstream forwarders, under the section headings "Resolution
  mode", "Forward zones" and "Upstream forwarders". API paths do not change.
- [S-6] (#58) Help.
  - Every form control and non-obvious column header has an info icon. It opens a tooltip on hover,
    keyboard focus and tap, is linked with `aria-describedby`, and does not cover validation errors.
  - The tooltip shows what the setting does, its default, its range and units, and the effect of
    changing it. A "Learn more" link opens `/help/<topic>#<anchor>`.
  - Help text lives in one typed catalogue keyed by field id. Help pages are markdown shipped with
    the GUI, covering resolution, DNSSEC, filtering, authoritative zones, fleet, access control, users
    and API tokens, and query log and observability.
  - `pnpm lint` fails when a form control has no catalogue entry and help affordance.
- [S-7] (#59) Operator-facing page descriptions.
  - The Filtering page description uses the issue's proposed wording: allowlist precedence, and no
    "management plane" or "ships to engines".
  - It links to Filter categories and Policies.
  - The Access control, Query log, Settings and Dashboard headers lose engine and management-plane
    jargon. Engines, engine group, engine detail and rollout pages keep "engine".
- [S-8] (#60) Collapsible navigation.
  - A **Filtering** parent in the Resolver group holds **Blocklist / allowlist** (`/filtering`),
    **Categories**, **Policies** and **RPZ**. Paths do not change, so no redirects are needed.
  - Clicking the parent toggles it. It auto-expands and shows as active on a child route, remembers
    its state in localStorage with a safe fallback, and is hidden when the user may open no child.
  - Design choice: it is expanded by default. It works at phone width.
  - The `/filtering` page title and the document title read "Blocklist / allowlist". Every page sets
    the document title "<page title> · Nexora".
- [S-9] (#62) Filter categories load collapsed, one compact row per category.
  - The row shows a chevron, name, description, "N of M sources on", total domains, a stale or error
    indicator, a non-commercial marker, and the category toggle. The OISD notice still triggers from
    the row toggle.
  - Expanding a row shows the source table with per-source toggles.
  - There is an Expand all / Collapse all control, and the expanded state persists in localStorage.
  - A `#category-<key>` deep link auto-expands its category.
  - Design choice: the optional search box is included and filters by category or source name.
  - The row is a button with `aria-expanded`, and the toggle is reachable separately.
- [S-10] (#63) Access control split.
  - **Recursion / resolver access** is the existing access_control allow_cidrs column, followed by the
    engine group's `extra_acl_cidrs`. It covers cache, forwarding, recursion, rewrites, filtering and
    RPZ.
  - **Authoritative query access** has a global default list, the new access_control authoritative_allow_cidrs column,
    which is fleet-wide.
  - Each zone has an optional `allow_query_cidrs` override; an empty list inherits the global default.
  - The check order is: hosted zone match → zone override or authoritative default → REFUSED. A name
    that is not hosted → recursion ACL → REFUSED. RA is set only for clients the recursion ACL allows.
  - Transfers keep their per-zone `allow_cidrs` and TSIG. Updates keep TSIG and gain an optional
    per-zone update allow_cidrs source list, where empty means any source. NOTIFY stays accepted
    only from a secondary zone's configured primaries.
  - Refusals are recorded in the query log with source `acl` and counted in
    `nexora_acl_refused_total{acl}`.
  - The GUI shows both ACL sections and a read-only per-zone transfer and update summary. The zone
    editor has the allow-query list.
- [S-11] (#63) Migration without behaviour change: existing installs keep the recursion ACL equal to
  the current ACL, and the authoritative default is `0.0.0.0/0, ::/0` (any). An engine applying a
  snapshot from an older management plane, which lacks the new fields, answers hosted zones to every
  client as before.
- [S-12] (#64) Upstream strategy `parallel`, with `parallel_max` (0 = every candidate, at most 8).
  - On a forwarded miss the query goes to the admitted candidates at once, lowest RTT first, capped
    at `parallel_max`.
  - The first NOERROR or NXDOMAIN reply that passes the existing ID/question checks wins. SERVFAIL,
    REFUSED, other rcodes, malformed replies and errors never win while another attempt is pending.
    When every attempt fails, the best failure is returned: SERVFAIL or REFUSED before transport
    errors.
  - Losing attempts keep running off the reply path until they finish or time out. Their replies are
    dropped, and their health/EWMA is updated from every completion.
  - DNSSEC validation of the winning answer happens as today. Inflight coalescing still makes one race
    per unique question.
  - New metrics: `nexora_upstream_race_wins_total{upstream}` and
    `nexora_upstream_race_duration_seconds`. `nexora_upstream_queries_total` counts every attempt. The
    query log shows the winning upstream and how many upstreams were raced.
  - The Settings GUI offers `parallel` with help text that warns about N× upstream load and about
    privacy.
- [S-13] (#65) Engine modal.
  - Clicking an engine on the Engines page or in an engine group's list opens a modal. `?engine=<id>`
    makes it deep-linkable, and Esc or back closes it. Next and previous buttons step through the
    current list.
  - The header shows node name, status, group, engine version, applied/target version and last seen,
    with a link to the full page, which stays.
  - The **Metrics tab** has 5m, 1h and 24h windows with auto refresh. It charts QPS, p50/p99, cache
    hit ratio, SERVFAIL/NXDOMAIN/REFUSED ratios, blocked rate, per-upstream RTT and failures, filter
    index memory, entries and build time, process CPU and resident memory against the limit, open
    connections per transport, uptime and restarts. A new viewer operation `getEngineMetrics` serves
    it from the extended `Stats` samples.
  - The **Queries tab** shows the query log filtered by `engine_id` and links to the full query log.
- [S-14] (#65) Engine logs.
  - Each engine keeps a bounded in-memory ring buffer of its own log lines: 2,000 lines, each line
    truncated to 512 bytes. Admission is capped at 100 lines/s with a burst of 200. Levels are error,
    warn, info and debug. Join tokens, API tokens and PEM blocks are masked before storing.
  - The management plane requests lines over the existing mTLS control stream with
    ServerMessage log_request and EngineMessage log_batch. Requests are routed through PostgreSQL
    to the instance holding the engine's stream.
  - The new operator operation `getEngineLogs` takes `after` (sequence cursor), `level`, `q` (text) and
    `limit`.
  - The **Logs tab** shows the last 1,000 lines on open. It then live-tails by polling with the cursor
    every 2 s, with a level filter, search, pause/resume and follow.
  - Design choices: polling with a sequence cursor instead of SSE (one plain operation, works through
    the kw ingress), and no audit row for reads (reads are never audited in Nexora). Nothing is logged
    on the query path.
- [S-15] (#66) Account self-service.
  - The user menu gains **Profile** (`/account`) and **Change password**. Change password is hidden for
    OIDC users, who see "Managed by your identity provider" in the profile instead.
  - The profile shows username, role, source, created and last sign-in. It edits email and display
    name for local users only, and preferences for everyone: theme (system/light/dark), time zone,
    24-hour clock, and default query log live mode.
  - `PUT /auth/me` updates only email, display name and preferences, with `revision`. Role, disabled
    and username in the body are ignored.
  - `POST /auth/me/password` takes `{current_password, new_password, revoke_other_sessions}` and
    requires a session. It returns 204. It returns 400 when the new password is under 12 characters or
    equals the current one, 403 `invalid_current_password`, 403 for API tokens, 409 for OIDC users,
    and 429 `too_many_attempts`.
  - Other sessions are revoked when chosen (the default); the current session stays valid.
  - Audit actions `updateCurrentUser` and `changeOwnPassword` carry no secret.
  - Failed attempts are counted per username and client address, shared by login and password
    change: more than 10 in 15 minutes gives 429. Design choice: login gains the same counter, which it
    lacked. Keying on the client address too means a remote attacker cannot lock the admin out
    (lead decision 2026-09-14).
- [S-16] (#67) Version.
  - A new viewer operation `GET /version` returns `version`, `commit`, `build_date`, `repository_url`
    (from `NEXORA_REPOSITORY_URL`, empty by default) and the engine versions across the fleet with
    counts.
  - The sidebar footer shows `Nexora v1.2.3 · f0ee3a1` for release builds and `Nexora sha-f0ee3a1` for
    dev builds. The short hash links to the commit when a repository URL is set.
  - A details popover shows version, full commit, build date, management plane version, GUI build and
    engine versions, and warns when engines run mixed versions. In the collapsed or mobile layout it
    is reached through an info icon.
  - A "New version available – reload" hint appears when the GUI's compiled commit differs from the
    management plane's.
  - Build scripts, Dockerfiles and `.github/workflows/images.yml` pass `VERSION`, `COMMIT` and
    `BUILD_DATE` to mgmt (`-X`), the engine (`NEXORA_VERSION`, `NEXORA_COMMIT`) and Vite
    (`__NEXORA_VERSION__`, `__NEXORA_COMMIT__`, `__NEXORA_BUILD_DATE__`).

## Out of scope

- AI features (M11), and any change to engine query semantics other than the ACL split and the
  `parallel` strategy.
- Response rate limiting (RRL) for public authoritative answers. A "drop instead of REFUSED" option
  for authoritative ACLs.
- Allow-query per engine group or per listener IP (#63 item 5, "for later").
- A per-forward-zone strategy: forward zones have no strategy field (#64 considerations).
- Breached or common password checks against external lists (#66 optional). Listing or deleting your
  own sessions and your own API tokens (#66 optional).
- An OTLP log backend for engine logs (decision #65), log retention beyond the engine ring buffer, and
  SSE or WebSocket streaming.
- Audit entries for reading engine logs.
- New query log backends (M10). An OpenSearch index template or n-gram sub-field: wildcard on
  `.keyword` is accepted with the 5 s timeout, and the revisit condition is recorded as a `debt:`
  marker.
- Editing the filter category catalog (unchanged read-only rule).

## Constraints

- Hot path rules from `docs/architecture.md` stay binding: no logging, allocation or lock on the
  cache-hit path, and `cache_hit_path_does_not_allocate` keeps passing.
  - Attribution adds only fixed-size `QueryRecord` fields and reuses spare bits of the 64-octet
    decision cache slot.
  - The authoritative ACL check runs only when a hosted zone matches, and skips the scan when the list
    is "any".
  - The `parallel` race allocates only on the forwarded-miss path.
- `TestGUICoverage` requires every OpenAPI operation, new ones included, to be requested by a
  Playwright screen spec. Its spec glob widens from `[012][0-9]-*.spec.ts` to `[0-9][0-9]-*.spec.ts`,
  because new specs are numbered from 25 up.
- Every new operation has a role in `mgmt/internal/auth/permissions.go` and the same role in
  `web/src/auth/permissions.ts` (`TestPermissionsCoverEveryOperation`, `pnpm lint`).
- Every page, the modal and the help pages work at 400 px width and in dark and light themes.
- Every existing Go, Rust and Playwright test keeps passing; tests are updated only where M6 changes
  a label, a test id or a heading.
- Proto fields added by M6 to existing messages use **700–799**; new messages number from 1. New
  migrations are `mgmt/migrations/00600_access_split.sql`, `00601_parallel_strategy.sql`,
  `00602_user_profile.sql`, `00603_engine_log_replies.sql` and `00604_engine_stats_rollup.sql`.
- Rolling upgrades:
  - An engine without M6 ignores the new snapshot fields.
  - An M6 engine given a snapshot without `authoritative_acl_set` answers hosted zones to everyone.
  - The management plane treats a missing M6 `Stats` field as zero and a missing log reply as
    `engine_unsupported`.
- Engine logs: the redaction masks join tokens (`nxj1.`), API tokens (`nxt_`), PEM blocks and
  `secret=`/`password=` values. The `getEngineLogs` role is operator.
- Password rules: minimum 12 characters (the `UserCreate` rule), argon2id as today, no secret in audit
  rows or logs.
- AI is not part of M6.

## Interfaces

- **Contract (`proto/nexora/control/v1/control.proto`):**
  - `ConfigSnapshot`: `authoritative_allow_cidrs` (700), `authoritative_acl_set` (701).
  - `AuthZone`: `allow_query_cidrs` (700), `update_allow_cidrs` (701).
  - `UpstreamStrategy`: `UPSTREAM_STRATEGY_PARALLEL = 3`. `ResolverConfig`: `parallel_max` (700).
  - ServerMessage `log_request` (700), EngineMessage `log_batch` (700). New messages `LogRequest`,
    `LogBatch`, `LogLine`, and enum `LogLevel`.
  - `Stats` (700–714): `queries_by_rcode`, `queries_by_transport`, `miss_duration_bucket_counts`,
    `filter_rewritten_total`, `answers_by_route`, `resolution_failures_total`,
    `process_cpu_seconds_total`, `process_resident_bytes`, `memory_limit_bytes`, `open_connections`,
    `started_unix_ms`, `acl_refused`, `tls_certificate_not_after_unix`, `log_lines_dropped_total`,
    `race_duration_bucket_counts`.
  - RecursionStats `upstream_timeouts` (700), UpstreamStatus `race_wins_total` (700).
- **HTTP API (`mgmt/api/openapi.yaml`):**
  - `searchQueryLog` gains repeated parameters and the new fields.
  - `AccessControl` gains `authoritative_allow_cidrs`.
  - `Zone`, `ZoneCreate` and `ZoneUpdate` gain `allow_query_cidrs`. `ZoneUpdatePolicy` gains
    `allow_cidrs`.
  - ResolverSettings `strategy` gains `parallel`, plus `parallel_max`.
  - `User` gains `display_name`, `last_login_at` and `preferences`.
  - New operations:

    | operationId          | Method and path                                  | Role     |
    | -------------------- | ------------------------------------------------ | -------- |
    | `getDashboardSeries` | `GET /dashboard/series?range=`                   | viewer   |
    | `getDashboardTop`    | `GET /dashboard/top?range=&limit=`               | viewer   |
    | `getDashboardHealth` | `GET /dashboard/health`                          | viewer   |
    | `getEngineMetrics`   | `GET /engines/{id}/metrics?window=`              | viewer   |
    | `getEngineLogs`      | `GET /engines/{id}/logs?after=&level=&q=&limit=` | operator |
    | `updateCurrentUser`  | `PUT /auth/me`                                   | viewer   |
    | `changeOwnPassword`  | `POST /auth/me/password`                         | viewer   |
    | `getVersion`         | `GET /version`                                   | viewer   |

  - Error codes: `engine_disconnected` (409), `engine_timeout` (504), `engine_unsupported` (501),
    `invalid_current_password` (403), `too_many_attempts` (429), `managed_by_identity_provider` (409).
- **Management environment:** `NEXORA_REPOSITORY_URL` (optional).
- **Engine:**
  - `acl::Acl::allows_all`.
  - Runtime field `authoritative_acl`.
  - `Strategy::Parallel { max }`.
  - The `telemetry::logbuf` module, with the crate-level `eprintln!` shadow macro that also records
    into the ring buffer.
  - `filter::index::FilterDecision::Allowed(ListHit)`, with `ListHit` gaining `offset`.
- **GUI:**
  - Routes `/resolution` (plus the `/upstreams` redirect), `/help`, `/help/:topic` and `/account`.
  - The sidebar Filtering group and version footer.
  - The user menu items `menu-profile` and `menu-change-password`.
  - The `?engine=` modal.
  - Components: `HelpTip`, `MultiSelect`, `EngineModal`.
  - Libraries: `web/src/lib/preferences.ts` and `web/src/help/catalog/`.
- **Build:** Docker build args `COMMIT` and `BUILD_DATE`, passed by `scripts/build-image.sh`. Engine
  constant `nexora_engine::COMMIT`.

## Data

- **PostgreSQL:**
  - `access_control.authoritative_allow_cidrs cidr[] NOT NULL DEFAULT '{0.0.0.0/0,::/0}'`.
  - `zones.allow_query_cidrs cidr[] NOT NULL DEFAULT '{}'`.
  - `zones.update_allow_cidrs cidr[] NOT NULL DEFAULT '{}'`.
  - `resolver_settings.strategy` check adds `parallel`.
  - `resolver_settings.parallel_max integer NOT NULL DEFAULT 0 CHECK (parallel_max BETWEEN 0 AND 8)`.
  - `users.display_name text NOT NULL DEFAULT ''`, `users.last_login_at timestamptz`,
    `users.preferences jsonb NOT NULL DEFAULT '{}'`.
  - `auth_failures(username text, at timestamptz)`, pruned after 15 minutes.
  - `UNLOGGED engine_log_replies(request_id uuid primary key, batch bytea, created_at)`, deleted after
    read and pruned after 60 s.
  - `engine_stats_rollup(engine_id, bucket timestamptz, stats bytea, primary key (engine_id, bucket))`:
    the newest sample per 5-minute bucket, kept 8 days.
  - The management plane owns all of these.
- **Engine memory:** a ring of 2,000 `LogLine`s, each up to 512 message bytes (about 1.1 MiB). No
  engine state is added to `state_dir`.
- **Query-log attributes:**
  - New: `nexora.filter.source`, `nexora.filter.rule`, `nexora.rpz_zone` (the RPZ zone id),
    `nexora.acl.refused` (`recursion` or `authoritative`), `nexora.upstream_raced` (integer).
  - `nexora.filter.list_id` is now emitted for allowed hits too.
  - These names avoid the OpenSearch mapping conflict of a value that is also an object parent
    (`nexora.rpz` and `nexora.upstream` are values).
- **Browser localStorage:**
  - `nexora-theme` (existing), `nexora-nav-filtering` (`open`/`closed`), and
    `nexora-categories-expanded` (a JSON array of keys).
  - All access is wrapped in try/catch. Profile preferences win over localStorage for the theme once
    loaded.
- **GUI bundle:**
  - The help catalogue is in `web/src/help/catalog/*.ts`. Help topics are in
    `web/src/help/topics/*.md`, each naming its `docs/operations.md` section.
  - Build constants: `__NEXORA_VERSION__`, `__NEXORA_COMMIT__`, `__NEXORA_BUILD_DATE__`.

## Edge cases

- The name filter: an empty fragment does not filter. `*`, `?` and `\` match literally. A fragment
  with a trailing dot matches the same names as without it. Mixed case matches.
- Repeated parameters: an empty value (`qtype=`) is ignored. Duplicates are OR-ed once. Unknown enum
  values give 400. More than 32 values for one parameter gives 400.
- A name blocked by one list and allowed by an allowlist in the same view shows `allowlist` with the
  allowlist's list id and rule.
- A decision served from the decision cache carries the same list and rule offset as the first
  decision.
- A list deleted after a query was logged has an empty `list_name` while its `list_id` is kept.
  Renamed lists show the new name.
- A client in no policy group shows scope "global". The group allowlist id `group-allow:<sha>` shows
  as "<group> allowlist".
- A query that is hosted and refused by the zone override shows source `acl` and rule `authoritative`.
  With RA it is still REFUSED.
- A zone override that is narrower or wider than the default: the override replaces the default and
  is not merged with it.
- A hosted query over DoH or DoQ from an outside client: the same authoritative ACL applies. Transfers
  over DoH and DoQ stay REFUSED as today.
- `parallel` edge cases:
  - A single upstream behaves as `ordered`.
  - Every upstream down: the probe-admission rules apply per candidate.
  - `parallel_max` above the candidate count uses all candidates.
  - A fast SERVFAIL followed by a slow NOERROR returns the NOERROR.
  - Every attempt timing out gives `Deadline`, and the client gets SERVFAIL as today.
- Engine logs:
  - The `after` cursor is older than the oldest buffered line: the reply starts at the oldest line and
    carries `oldest_seq`, and the GUI shows "older lines were dropped".
  - The engine disconnects while a request is pending: 409 after the 5 s wait ends early.
  - Two management instances: only the stream holder answers.
- Engine metrics: a counter going backwards (engine restart) is skipped and counted as one restart. A
  window with fewer than two samples returns empty series.
- Dashboard: the 7 d range reads rollups, and ranges up to 24 h read raw samples. The query log backend
  being unavailable gives `available: false` for top lists while the other sections render. A fleet
  with no engines renders empty states.
- Account:
  - An API token principal calling `changeOwnPassword` gets 403.
  - An OIDC user changing email gets 409 `managed_by_identity_provider`.
  - A stale `revision` gives 409.
  - The last enabled admin can change their own password.
- Version: dev builds without a commit show `Nexora dev` with no link and no mismatch hint.
- Navigation: a user who may open no Filtering child (none today, since viewers can list all) sees no
  parent. With a collapsed parent on a child route, the parent is forced open for that visit.
- Filter categories: a deep link to an unknown key expands nothing. Corrupt localStorage JSON is
  ignored.

## Failure modes

- **OpenSearch slow or down:** the 5 s timeout gives 503 `querylog_unavailable` for the query log and
  `available: false` for dashboard top lists.
- **PostgreSQL down:** existing 503 behaviour. Log requests fail with 503.
- **Engine not connected to any instance:** `getEngineLogs` gives 409 `engine_disconnected`.
- **Engine connected but silent for 5 s:** 504 `engine_timeout`.
- **Engine without M6 log support:** 501 `engine_unsupported`, when its `Hello.engine_version` is below
  the first M6 build or no reply arrives but the stream stays up. The GUI shows "This engine version
  cannot send logs".
- **Log flood** (for example a debug loop): lines above the rate cap are dropped and counted in
  `nexora_log_lines_dropped_total`. The buffer never grows and the query path is unaffected.
- **Upstreams in a race all failing:** health marks each down as today; the client gets SERVFAIL, or
  serve-stale when available.
- **Snapshot with invalid authoritative or allow-query CIDRs:** rejected with the reason, and the
  previous runtime stays.
- **Too many password or login failures:** 429 for 15 minutes per username and client address, while the correct
  password still works for existing sessions.
- **Version endpoint unavailable:** the footer shows the GUI's own build and no mismatch hint.
- **Help catalogue entry missing:** `pnpm lint` fails in CI, so nothing ships without help.

## Acceptance criteria

- [ ] [S-1] `TestQueryLogBackends` gains the subtest `partial-name` for builtin and OpenSearch.
      `you-<n>` finds `www.you-<n>.tube.test.` and `you-<n>.test.`, `TUBE.TEST` finds the first, `x*y`
      matches only the literal `x*y-<n>.test.`, and a non-matching fragment returns none. Unit test
      `TestOpenSearchNameWildcardEscaped` asserts the request body. The spec
      `web/e2e/screens/10-query-log.spec.ts` searches a fragment. Fails if either backend matches whole
      names only or treats `*`/`?` as wildcards.
- [ ] [S-2] `TestQueryLogBackends` subtest `multi-value` proves OR within a field and AND across fields
      on both backends. `TestSearchQueryLogRepeatedParameters` proves that the repeated and single forms
      reach `querylog.Query` and that more than 32 values give 400. The spec
      `web/e2e/screens/25-querylog-filters.spec.ts` (run by `TestGUICoverage`) selects two response codes
      and two types, sees only matching rows, round-trips the URL and clears the selection. Fails if a
      second value replaces the first or the URL loses the selection.
- [ ] [S-3] Rust tests `allowed_hits_carry_list_and_rule_through_the_decision_cache` and
      `rewrite_and_rpz_attribution_in_query_records` (run by `.github/workflows/ci.yml`) prove attribution
      for blocked, allowed, rewritten and RPZ queries, including cache hits. `TestQueryLogCategoryAttribution`
      is extended on both backends: a custom list block shows `source=blocklist` with the list name and
      rule; a category block shows the category and source list name; global and group allowlists show
      `allowlist`, the entry and the scope; RPZ shows the zone; a rewrite shows the rule and answer; and
      `policy_group` is set for group clients. The spec `web/e2e/screens/26-querylog-reason.spec.ts`
      asserts the Reason column and row detail. Fails if any attribute is missing, wrong, or lost on a
      decision-cache hit.
- [ ] [S-3] [S-10] [S-12] Rust test `cache_hit_path_does_not_allocate` (run by
      `.github/workflows/ci.yml`) still passes with a group client, an allowlist hit, a hosted-zone client
      under an "any" authoritative ACL, and the `parallel` strategy configured. Fails if attribution, the
      ACL split or the strategy allocates on the cache-hit path.
- [ ] [S-4] `TestDashboardAggregations` seeds `engine_stats` and rollup samples for two engines and
      asserts the series values for 15m and 7d, the top lists from the builtin backend, and the health
      alerts for a disconnected engine, a stale category, an upstream down and a certificate expiring in
      3 days. `TestDashboardTopBackendUnavailable` asserts `available: false`. The spec
      `web/e2e/screens/27-dashboard.spec.ts` switches every range and sees every section render with data
      at desktop and 400 px width. Fails if a section is missing, a range is ignored, or an unavailable
      backend breaks the page.
- [ ] [S-5] [S-8] The spec `web/e2e/screens/28-navigation.spec.ts` (run by `TestGUICoverage`) sees
      "Forwarding & recursion" at `/resolution`, `/upstreams` redirecting there, the Filtering parent
      collapsing and expanding with the state kept across reload, auto-expand on `/rpz`, and the
      `/filtering` heading and document title "Blocklist / allowlist" at 400 px width. The specs
      `02-upstreams`, `17-resolution`, `21-engine-group-scope`, `04-filtering`, `12-policies`, `15-rpz`,
      `22-filter-categories` and `web/e2e/auth.spec.ts` pass with the new labels. Fails if a route,
      redirect, label or remembered state is wrong.
- [ ] [S-6] `web/scripts/check-help.mjs`, run by `pnpm lint` in `.github/workflows/ci.yml`, fails when
      any `id`/`htmlFor` form control under `web/src/pages` or `web/src/components` lacks a catalogue entry
      or a `HelpTip`. The spec `web/e2e/screens/29-help.spec.ts` hovers and keyboard-focuses the info icon
      on a sample field of every page, sees the tooltip, follows "Learn more" to the right help anchor, and
      renders help pages in dark and light themes at 400 px width. `TestHelpTopicsReferenceOperationsDoc`
      proves every help topic names an existing `docs/operations.md` heading. Fails if a control has no
      help, a tooltip is not keyboard reachable, or a topic points to a missing section.
- [ ] [S-7] The spec `web/e2e/screens/35-page-headers.spec.ts` (run by `TestGUICoverage`) asserts the
      new Filtering description, its links to Filter categories and Policies, and that no page header
      outside Engines, engine groups, engine detail and rollouts contains "management plane" or "engines".
      Fails if jargon remains or the links are missing.
- [ ] [S-9] The spec `web/e2e/screens/22-filter-categories.spec.ts` (run by `TestGUICoverage`) loads
      collapsed categories, toggles a category from its row (OISD notice included), expands it, toggles a
      source (OISD notice included), uses Expand all / Collapse all, reloads with the state kept, and opens
      a `#category-malware` deep link expanded. Fails if sources show before expanding, the row toggle
      does nothing, or state is not kept.
- [ ] [S-10] `TestAuthoritativeAccessSplit` runs over UDP, TCP, DoT and DoH against a managed stack.
      An outside client gets hosted answers under the "any" default, REFUSED when the zone's
      `allow_query_cidrs` excludes it, and REFUSED for recursion and cache names. An inside client gets
      both, with RA only when allowed. Refusals appear in the query log with source `acl` and in
      `nexora_acl_refused_total`. Rust test `authoritative_acl_order_and_ra` covers the pipeline order.
      Fails if hosted zones ignore the override, recursion leaks to outside clients, or RA is set for them.
- [ ] [S-10] `TestAccessControlSplitAPI` proves the API, audit rows and snapshot fields for the
      authoritative default, zone `allow_query_cidrs` and `update.allow_cidrs`, and rejects invalid CIDRs
      with 400. Rust test `update_source_cidrs_refuse_outside_senders` proves the update source list. The
      spec `web/e2e/screens/30-access-control-split.spec.ts` edits both ACL sections, sees the per-zone
      transfer/update summary and sets a zone override. Fails if either section does not persist or the
      override is not published.
- [ ] [S-11] `TestAccessSplitMigrationKeepsBehaviour` migrates a database at `00503` holding a custom
      ACL and a zone, and asserts the recursion CIDRs are unchanged and the authoritative default is
      `0.0.0.0/0, ::/0`. Rust test `snapshot_without_authoritative_acl_answers_every_client` proves an old
      snapshot keeps answering hosted zones to every client. Fails if an upgraded install refuses hosted
      queries it answered before.
- [ ] [S-12] Rust test `parallel_returns_first_valid_answer_and_updates_every_upstream` uses fast, slow
      and SERVFAIL fakes. It asserts the fast answer; with fast down, the slow NOERROR and not the SERVFAIL;
      health and RTT recorded on all three; and no pending waiters afterwards. Rust test
      `parallel_max_limits_to_lowest_rtt` proves only the two lowest-RTT upstreams receive the query.
      `TestUpstreamParallelStrategy` asserts the API accepts `parallel` and `parallel_max`, engines apply
      the new version, `nexora_upstream_race_wins_total` grows, and the query log shows the winner and
      `upstreams_raced`. Fails if a SERVFAIL wins while a valid answer is pending, waiters leak, or losers
      get no health update.
- [ ] [S-12] The spec `web/e2e/screens/31-upstream-parallel.spec.ts` (run by `TestGUICoverage`) selects
      `parallel`, sets `parallel_max`, sees the load and privacy warning, saves and reloads.
      `TestUpstreamParallelLatency`, run in the kw dev pod on kw hardware, sends 2,000 uncached queries
      through an engine with one fixture upstream delayed 150 ms and one undelayed, first with `ordered`
      (the delayed upstream first) and then with `parallel`, and requires parallel p95 and p99 to be lower
      than ordered. Fails if the setting does not persist or parallel is not faster with a slow upstream.
- [ ] [S-13] `TestEngineMetricsSeries` seeds samples, including a restart, and asserts every series
      for the three windows. The spec `web/e2e/screens/32-engine-modal.spec.ts` (run by `TestGUICoverage`)
      opens the modal from the Engines list and from a group list, deep-links `?engine=`, closes with Esc
      and back, steps next/previous, switches Metrics/Logs/Queries tabs, sees chart data and log lines,
      and works at 400 px width. Fails if the modal does not open, the URL does not round-trip, or a tab is
      empty with data present.
- [ ] [S-14] Rust tests `log_buffer_is_bounded_and_rate_capped`, `log_lines_are_redacted` and
      `log_request_returns_filtered_lines_after_cursor` (run by `.github/workflows/ci.yml`).
      `TestEngineLogsRoutedAcrossInstances` connects a fake engine to instance B, requests logs through
      instance A, and gets the batch; a disconnected engine gives 409 and a silent one gives 504.
      `TestEngineLogsFromRealEngine` reads the "applied version" line from a managed engine through
      `getEngineLogs`. Fails if the buffer grows past 2,000 lines, a secret survives redaction, or routing
      misses the stream holder.
- [ ] [S-15] `TestAccountSelfService`:
  - a viewer changes their own email and preferences; a body carrying `role` and `disabled` leaves
    them unchanged;
  - a wrong current password gives 403;
  - a short or unchanged password gives 400;
  - an OIDC user gives 409;
  - an API token gives 403;
  - eleven failures give 429;
  - a successful change revokes other sessions and keeps the current one;
  - audit rows hold no password.

  The spec `web/e2e/screens/33-account.spec.ts` (run by `TestGUICoverage`) edits the profile email and
  time zone, changes the password, signs out, signs in with the new password, and sees Change password
  hidden for the OIDC user. Fails if self-service can change role or disabled, or a secret reaches the
  audit log.

- [ ] [S-16] `TestVersionEndpoint` asserts `version`, `commit`, `build_date`, `repository_url` and the
      engine version counts. `TestDockerfilesStampBuildInfo` asserts both Dockerfiles, `scripts/build-image.sh`
      and `.github/workflows/images.yml` pass `VERSION`, `COMMIT` and `BUILD_DATE`. The spec
      `web/e2e/screens/34-version.spec.ts` (run by `TestGUICoverage`) sees the footer and popover in desktop
      and 400 px layouts, and the reload hint with a mocked `/api/v1/version` commit. `TestKwSmoke` subtest
      `version-footer-matches-health` compares `/version` with `/health` and the image tag on kw. Fails if a
      build argument is missing or the hint does not appear on a mismatch.
- [ ] [S-1] [S-2] [S-3] [S-4] [S-5] [S-6] [S-7] [S-8] [S-9] [S-10] [S-11] [S-12] [S-13] [S-14] [S-15]
      [S-16] `TestGUICoverage` passes with every OpenAPI operation covered, `scripts/kw-acceptance.sh` passes
      after the M6 `scripts/kw-deploy.sh` run with zero lost probe queries on 192.168.10.136 and
      192.168.10.139, and `docs/operations.md` describes every M6 setting. Fails if an operation is
      uncovered, a probe query is lost, or a setting is undocumented.

## Open questions
