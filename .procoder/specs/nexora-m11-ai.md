# nexora-m11-ai

Status: complete

## Problem

Nexora already collects everything needed to spot trouble: query logs (builtin or OpenSearch, with
M6 decision reasons), engine `Stats` samples with the M6 5-minute rollup, rollout history, filter
list and category state, RPZ zones and audit rows. An operator still has to read all of it by hand.

- DNS tunnelling, a compromised device, an upstream that degrades slowly or a filter index that fills
  up is found only after it hurts.
- Filtering, policy and RPZ decisions need expertise that most homelab and small-ISP operators lack.
- The console forms are precise but slow when the operator only knows the intent ("block adult
  content for guests").
- External AI agents (Claude Desktop, Cursor) cannot use Nexora at all.

Issues #42–#52 describe an AI layer for this. The owner decided (2026-09-14, `.procoder/ask/decisions.md`):

- The language model is **fastllm on kw**, OpenAI-compatible, model `qwen3-6-35b-a3b`, reached on the
  LAN, so query data never leaves the network.
- The code stays provider-agnostic.
- Every AI feature is **suggest-only**: nothing changes configuration until an operator reviews and
  applies it, and every apply is audited.

M11 is the last milestone on the roadmap (M6 → M11). It builds on the M6 query-log reasons, dashboard
series and rollup, top lists, engine metrics and version endpoint.

## Users

- **Homelab operator** (operator role): wants to be told when a device on the LAN tunnels data, when
  an upstream is getting slower, or when the filter index is about to hit its cap. Wants filtering
  recommendations they can accept with one click after reading what will change. Asks the query log
  and the configuration assistant in plain language.
- **ISP / enterprise operator:** wants fleet-wide anomaly correlation, a risk score for a rollout
  before its canaries start, capacity forecasts, and RPZ rule suggestions for threats their clients
  actually query.
- **Viewer:** reads insights, forecasts, recommendations and rollout risk, runs natural-language
  query-log searches and threat checks, and never applies anything.
- **Admin:** configures the provider through a Kubernetes Secret, watches the AI status page (agents,
  token budget, errors), and creates API tokens for MCP clients.
- **External AI agent** (through MCP with an API token): reads fleet, query log, filtering, zone and
  upstream state, and writes only when an admin turned read-only MCP off and the token's role allows
  the operation.
- **Installations without an LLM:** see no AI navigation, no AI widgets and no outbound calls. Every
  other feature behaves exactly as before.

## In scope

- [S-1] (#42) AI foundation in the management plane.
  - Package `mgmt/internal/ai` uses `github.com/azrtydxb/go-ai-sdk` `v0.4.1` (root module only). The
    model comes from its OpenAI provider with `WithBaseURL` and `WithAPIKey`. No provider-specific code
    exists outside `mgmt/internal/ai/provider.go`. The engine gains no dependency and no code.
  - Configuration comes from `NEXORA_AI_BASE_URL`, `NEXORA_AI_MODEL` and `NEXORA_AI_API_KEY`. On kw these
    come from Kubernetes Secret `nexora-ai`, keys `base-url`, `model` and `api-key`. The tuning
    variables are listed under Interfaces.
  - AI is **on only when base URL and model are both set**. Otherwise:
    - `getAiStatus` reports `enabled: false` with a reason, one of `not_configured`,
      `incomplete_configuration` or `endpoint_not_private`.
    - No agent starts, no outbound AI connection is made, and every other AI operation answers 503
      `ai_disabled`.
    - Nothing else in Nexora changes.
  - An empty API key is allowed, because local OpenAI-compatible servers may need none.
  - **Privacy guard:** the base URL host must resolve only to loopback, RFC 1918, RFC 6598
    (100.64.0.0/10), IPv6 ULA or link-local addresses. Otherwise AI stays off with
    `endpoint_not_private`, unless `NEXORA_AI_ALLOW_PUBLIC_ENDPOINT=true`.
    - Design choice: fail closed, so a mistyped cloud URL cannot ship query logs off the LAN.
    - The host is resolved at start and again on each call. A call to a host that now resolves
      publicly fails with `endpoint_not_private` and increments the provider error counter.
  - **Structured output:** every model answer is decoded into a typed Go value and then checked by a
    feature validator (ids exist, enums, bounds, allowlisted operations).
    - Invalid JSON, a schema mismatch or a validator error sends the model the previous answer plus
      the error text and asks again. There are at most `NEXORA_AI_VALIDATION_ATTEMPTS` (3) attempts.
      After that the call fails with `invalid_output` and nothing is stored.
    - Two modes:
      - `json_schema` (default): `response_format` with the JSON schema, through GenerateObject.
      - `prompt`: the schema goes in the system prompt, the text is decoded, and code fences and
        `<think>` blocks are stripped.
  - **Reasoning model:** `NEXORA_AI_MAX_TOKENS` defaults to 16384, because qwen3 spends tokens thinking
    before it answers.
    - A response that finishes with `length` and has no decodable answer is retried once with double
      the max tokens, capped at 32768.
    - Reasoning text is never stored or shown. Reasoning tokens are counted.
  - **Bounds**, shared by every feature on one instance:
    - At most `NEXORA_AI_MAX_CONCURRENCY` (2) model calls in flight.
    - At most `NEXORA_AI_REQUESTS_PER_MINUTE` (20) started per minute, as a token bucket.
    - A per-call timeout `NEXORA_AI_TIMEOUT` (180 s).
    - A fleet-wide daily token budget `NEXORA_AI_DAILY_TOKEN_BUDGET` (2,000,000 input+output tokens per
      UTC day, summed in PostgreSQL across instances). Background agents stop at
      `NEXORA_AI_BACKGROUND_BUDGET_PERCENT` (80) of it, so interactive requests keep 20%.
    - Interactive requests wait at most 5 s for a slot and then answer 429 `ai_busy`. With the budget
      spent they answer 429 `ai_budget_exhausted`.
    - Transport retries: the SDK's 2 retries with backoff for retryable provider errors (429, 5xx).
  - **Observability:** the Prometheus metrics listed under Interfaces, one structured log line per
    failed call or agent run (feature, outcome, duration, tokens — never prompt text or the key), and
    `getAiStatus` with per-agent last run, next run, last outcome and error, plus today's budget use.
  - **Background agents** run under a scheduler.
    - Each run takes `pg_try_advisory_lock(hashtext('nexora:ai:agent:' || name))`, so only one
      instance runs an agent at a time.
    - A run starts when `now - last started run >= interval`, read from `ai_agent_runs`, so a restart
      does not re-run a 24 h agent at once. An agent that never ran starts `NEXORA_AI_AGENT_START_DELAY`
      (2 min) after the instance starts, so stats samples exist first.
    - Every run records agent, instance, start, end, outcome, tokens and error.
    - `runAiAgent` requests an immediate run.
    - An interval of `0` disables an agent.
  - **Interactive AI work runs as asynchronous tasks** stored in PostgreSQL (`ai_tasks`). The request
    answers 202 with a task id, and the GUI polls `getAiTask` every 2 s.
    - Design choice: polling like M6 engine logs. Reasoning calls take 10–120 s, longer than the kw
      ingress read timeout, and any instance can serve the poll.
    - A `running` task whose instance heartbeat is older than 15 s is marked `failed` with
      `instance_stopped`.
  - **Untrusted data:** query names, list entries and client strings reach the model only inside a
    JSON data block. The system prompt marks that block as untrusted. No model call has tools with side
    effects.
  - **Retention,** pruned hourly under the lock `nexora:ai:prune`: tasks 24 h; agent runs 30 days;
    resolved or dismissed findings 30 days; terminal proposals 90 days; expired forecasts 7 days;
    domain verdicts past `expires_at`; idle assistant sessions 30 days; usage rows 400 days.
- [S-2] Suggest-only proposals, applied through the normal API.
  - Every actionable AI output is a row in `ai_proposals`. Sources: `filter_recommendations`,
    `config_assistant`, `upstream_prediction`, `rollout_risk`, `capacity_forecast` and
    `rpz_suggestions`.
  - A proposal holds a title, description, priority (`low|medium|high`), impact and evidence JSON,
    and an ordered list of at most 8 **actions**.
  - An action is `{operation_id, path_params, body}` for an OpenAPI operation from a fixed allowlist:
    `updateFilterCategory`, `createPolicyGroup`, `updatePolicyGroup`, `updateGlobalSafeSearch`,
    `updateAllowlist`, `updateResolverSettings`, `updateUpstream`, `updateEngineGroup`, plus the RPZ
    action `appendAiRpzRules` (S-12).
  - Actions are validated before storing:
    - the body against the operation's request schema in the embedded `mgmt/api/openapi.yaml`
      (kin-openapi, unknown fields rejected);
    - the path parameters against existing rows;
    - `revision` equal to the current revision.
  - A validation failure retries the model (S-1) and never stores an invalid proposal.
  - `getAiProposal` adds `current` per action: the live resource JSON read through the matching GET
    operation (for example `getPolicyGroup` for `updatePolicyGroup`) with the caller's credentials. The
    GUI shows current against proposed side by side. Design choice: a live read instead of a stored
    snapshot, so the diff never shows a stale "before".
  - **Apply:** `applyAiProposals` (operator) replays each action in order as an in-process request
    through the same HTTP handler the API uses.
    - The replay carries the caller's own `Cookie` or `Authorization` header and
      `Content-Type: application/json`, so authentication, RBAC, validation, revision checks, snapshot
      publishing and audit rows are exactly those of a direct API call by that user.
    - The first failing action stops that proposal. Status becomes `applied`, `failed` (with the HTTP
      status, error code and message of the failing action) or `stale` (a 409 `conflict` on
      `revision`, meaning the configuration changed after the proposal).
    - One audit row `applyAiProposals` (target `ai_proposal`) records the proposal id, each action's
      operation id and HTTP status.
    - Design choice: actions are not atomic across operations; each is its own config version, as if
      the operator had clicked through the forms.
  - **Dismiss:** `dismissAiProposals` (operator) records who dismissed it, the time and an optional
    reason, and writes an audit row `dismissAiProposals`.
  - **Dedupe:** each proposal has a `fingerprint` (source + target + action digest). An agent run that
    produces an existing open fingerprint refreshes that row, and a dismissed fingerprint is not
    re-proposed for 7 days.
  - **No agent, task or model output ever calls a mutating operation.** Only `applyAiProposals` does,
    with a human caller.
- [S-3] (#43) Natural-language query-log search.
  - `startAiQueryLogSearch` (viewer) takes `{query (1..500 chars), from?, to?}` and runs a task.
  - The model translates the text into the M6 query-log filters: time range, client, name fragment,
    qtypes, rcodes, cache, filter, category, source, list ids, policy groups, engine ids. Names such as
    "finance group" and "OISD" are resolved against the current policy groups, lists, categories and
    engines, which are sent as context.
  - The filters are validated: enums, known ids, a time range of at most 7 days that ends no later
    than now. A missing range defaults to the last hour.
  - The management plane then runs the search (limit 200) and the M6 top aggregations for that range
    and filters. A second model call writes a summary of at most 600 characters and at most 3
    suggestions from those aggregates and the first 50 records.
  - The task result holds `filters` (the exact `searchQueryLog` parameters), `explanation`, `summary`,
    `suggestions` and `total_shown`.
  - The GUI applies the filters to the query log URL state (M6), so the records come from the existing
    search and stay live.
  - Design choice: no embedding index. fastllm serves only the chat model, and filter translation
    covers the issue's examples.
- [S-4] (#43) Query-log anomaly agent (`querylog_anomalies`, interval `NEXORA_AI_QUERYLOG_INTERVAL`, 30 s).
  - Deterministic detectors run on every interval over the window since the previous run, capped at
    the newest 5,000 records per run (a deliberate ceiling for
    high-QPS fleets, documented in the operations guide), plus top aggregations. They
    produce candidates with the deterministic id `<type>:<subject>`:
    - `dns_tunneling`: at least 50 queries from one client under one registrable parent, with mean
      leftmost-label Shannon entropy ≥ 3.5 bits/char and mean label length ≥ 20; or TXT/NULL at least
      40% of that client's queries, with at least 30 of them.
    - `nxdomain_burst`: one client with at least 50 queries and an NXDOMAIN ratio ≥ 0.5.
    - `query_flood`: one client's QPS ≥ 10× its 24 h median and ≥ 20 QPS.
    - `periodic_beacon`: at least 20 queries for one name from one client whose inter-arrival
      coefficient of variation is ≤ 0.1.
    - `category_escalation`: blocked queries in `malware`, `phishing` or `cryptomining` categories
      from a client or policy group that had none in the previous 24 h.
  - The model is called only when the candidate set changed (a new id or a severity change), and at
    most once per `NEXORA_AI_LLM_MIN_INTERVAL` (5 min).
  - It returns one anomaly per candidate id it confirms: severity, confidence 0..1, description,
    affected clients, sample domains and recommended actions. Ids not in the candidate set are
    rejected by validation.
  - Findings persist in `ai_findings` (kind `anomaly`). A candidate that is no longer detected for
    30 min is marked `resolved`.
  - Without a model answer (budget, error), candidates are still stored with a generated description
    from the detector and `explained: false`. Design choice: stale or unexplained anomalies are better
    than none.
  - `listAiFindings` and `updateAiFinding` (`acknowledged` | `dismissed`, operator, audited) serve
    them. A dismissed candidate id is not raised again for 24 h unless its severity rises.
- [S-5] (#44) Dashboard insight agent (`dashboard_insights`, interval `NEXORA_AI_DASHBOARD_INTERVAL`, 30 s).
  - Detectors over the M6 dashboard series (last 15 min against the previous 24 h, from raw samples
    and the rollup) and fleet health:
    - `servfail_spike`: engine SERVFAIL ratio ≥ 3× baseline and ≥ 2%.
    - `latency_spike`: p99 ≥ 2× baseline and ≥ 50 ms.
    - `qps_spike`: fleet or engine QPS ≥ 3× baseline.
    - `block_spike`: blocked rate ≥ 3× baseline.
    - `upstream_degraded`: RTT ≥ 2× baseline, or down.
    - `engine_disconnected`.
    - `export_dropped`.
    - `certificate_expiring`.
  - The model correlates the current candidates (for example a SERVFAIL spike plus upstream RTT),
    under the same change and 5-minute rules as S-4. It returns insights with title, description,
    engines, possible causes with confidence and supporting candidate ids, and recommended actions.
  - `getAiInsights` returns open insights, a one-line summary and a **health score 0–10** computed
    deterministically: `min(10, 3 × critical + 1 × warning)`. The score is correct without the model.
  - Insights are findings of kind `insight`, with the same acknowledge and dismiss.
- [S-6] (#45) Filter and policy recommendations (`filter_recommendations`, `NEXORA_AI_FILTER_RECOMMENDATIONS_INTERVAL`, 6 h).
  - Aggregates over the last 24 h per policy group (global included) and engine group:
    - blocked and allowed counts by category;
    - top names that are not blocked and not on any allowlist, with query and client counts;
    - queries to Google, Bing, DuckDuckGo and YouTube hosts per group, against its safe-search
      setting;
    - disabled catalog categories whose source lists contain the queried names (checked against the
      current list blobs, at most 2,000 names).
  - The model proposes at most 10 recommendations, each a proposal (S-2):
    - `enable_category`: `updateFilterCategory`, or `updatePolicyGroup` adding a `category_keys` entry
      for a group. `acknowledge_license` is never set by the model; a non-commercial source makes the
      proposal show the license notice, and the operator ticks it in the apply dialog.
    - `block_domains`: `appendAiRpzRules` with policy `nxdomain`. Design choice: Nexora custom lists
      are URL-fetched, so AI blocks live in the dedicated RPZ file zone.
    - `allow_domains`: `updateAllowlist` or the group allowlist through `updatePolicyGroup`.
    - `safe_search`: `updateGlobalSafeSearch` or `updatePolicyGroup`.
  - Impact numbers (additional blocked queries, analysed total, coverage %) are computed by code, not
    by the model. The model only chooses, orders and explains.
- [S-7] (#46) Configuration assistant (operator, `NEXORA_AI_CONFIG_ASSISTANT_ENABLED`).
  - `createAiAssistantSession` starts a session owned by the caller. `postAiAssistantMessage` adds a
    user message (1..2,000 chars) and runs a task.
  - The model receives the conversation (the last 20 messages) and a compact JSON context:
    - policy groups (name, id, CIDRs, category keys, list ids, safe search, revision);
    - filter categories (key, name, enabled, revision);
    - custom filter lists (id, name, kind, engine group);
    - engine groups (id, name, rollout params, revision);
    - access control CIDRs, global safe search and allowlist revision, resolver settings;
    - RPZ zone names.
  - The model returns `ConfigChangePlan {reply, summary, actions[] {operation_id, path_params, body,
explanation}}`, validated as in S-2. A plan with actions becomes a proposal (source
    `config_assistant`, linked to the session) and supersedes that session's previous open plan.
  - A reply without actions (a clarifying question) is allowed.
  - `getAiAssistantSession` returns the messages, the current plan proposal and the task state. The
    caller must own the session; others get 404.
  - Design choice: the context is preloaded instead of given as model tools. It is deterministic,
    bounded and testable with a scripted fake, and it avoids tool-call fragility on a local model.
    Tool access for external agents is MCP (S-13).
  - Design choice: no token streaming. The GUI shows "Thinking…" with elapsed time, consistent with
    S-1 tasks.
- [S-8] (#47) Upstream health prediction (`upstream_prediction`, `NEXORA_AI_UPSTREAM_PREDICTION_INTERVAL`, 6 h).
  - Per upstream, code builds per-hour p50/p99 RTT and failure ratio from `engine_stats` (24 h) and
    the M6 rollup (up to 8 days). It computes:
    - `slope_ms_per_hour`: least squares over 24 h;
    - `step_change`: last 3 h mean against the prior 21 h, ≥ 2× and ≥ 20 ms;
    - `periodic`: the same hour-of-day p99 ≥ 2× the daily median on at least 3 of the last 7 days;
    - `failure_trend`;
    - `projected_time_to_threshold`: when the p99 trend line crosses the upstream `timeout_ms`, or
      `null`.
  - The model classifies `trend` (`stable|degrading|improving|periodic|failing`), sets confidence and
    writes reasoning. It may recommend one of:
    - `switch_strategy`: `updateResolverSettings` to `fastest` or `parallel`;
    - `reorder`: `updateUpstream` `position`;
    - `disable`: `updateUpstream` `enabled: false`, only when at least one other enabled upstream
      stays;
    - `none`.
  - Predictions are stored as forecasts (kind `upstream`) with `generated_at` and
    `valid_until = generated_at + interval`. A recommendation other than `none` also becomes a
    proposal.
  - Fewer than 12 hourly points gives trend `insufficient_data` with no model call.
  - Design choice: no auto-approve, in line with suggest-only (the issue's optional auto-approve is
    dropped).
- [S-9] (#48) Rollout risk (`rollout_risk`, polls every 15 s, `NEXORA_AI_ROLLOUT_RISK_ENABLED`).
  - Every rollout of kind `change` created while AI is on, and not yet assessed, gets a risk
    assessment. Assessment is asynchronous and **never delays or blocks** a mutation, snapshot
    publish or rollout step.
  - Features computed by code from the audit rows of that config version:
    - changed target types and actions, and the number of changed resources;
    - high-risk types: `resolution_settings`, `dnssec_settings`, `rpz_zone`, `policy_group`,
      `resolver_settings`, `access_control`, `zone`;
    - engines in the group;
    - current fleet health (disconnected engines, SERVFAIL ratio over the last 15 min);
    - the 5 most similar past rollouts in the group by Jaccard similarity of `(target_type, action)`
      sets, with their outcomes (`completed`, `halted` with reason, `rolled_back`) and the maximum
      SERVFAIL ratio seen during `verifying`.
  - The model returns:
    - `risk_score` 1–10, whose `risk_level` must match: 1–3 low, 4–6 medium, 7–10 high;
    - `analysis` (≤ 1,200 chars);
    - `historical_patterns`, which may cite only the versions provided;
    - a recommendation `{strategy, canary_count, min_health_queries, max_servfail_ratio, reasoning}`.
  - The result is stored in `ai_rollout_risks` and served by `getAiRolloutRisk`.
  - When the recommendation differs from the group's rollout parameters and the level is medium or
    high, it becomes a proposal with `updateEngineGroup`.
  - A config-assistant proposal also carries a pre-apply risk estimate computed from its actions with
    the same features (`risk` on the proposal).
- [S-10] (#49) Threat intelligence assistance.
  - `startAiThreatCheck` (viewer) takes 1..100 domain names and runs a task. Per domain, code collects:
    - query count, client count and first/last seen over 7 days from the query log;
    - whether and why it is blocked or allowed now (M6 source, list name, category from the newest
      record);
    - label entropy, label count, length and TLD.
  - The model returns `is_threat`, categories (`malware|phishing|spam|c2|botnet|adult|tracking`),
    confidence and reasoning.
  - Verdicts are cached in `ai_domain_verdicts` for 7 days. A cached domain is answered without a
    model call.
  - `QueryLogRecord` gains `threat` (`null` or `{is_threat, categories, confidence, checked_at}`), read
    from cached verdicts at search time. No model call happens on the query-log read path.
  - **List classification** (`threat_classification`, `NEXORA_AI_THREAT_CLASSIFICATION_INTERVAL`,
    6 h): for each enabled block list whose `current_blob_sha256` changed since its last
    classification (at most 20 lists per run), code samples 200 names uniformly from the blob. The
    model classifies them in batches of 50. The stored breakdown (category → sampled count and
    estimated count = share × `entry_count`) is served by `getAiFilterListClassification`.
  - Design choice: sampling instead of classifying every entry. A local model cannot classify
    millions of names, and an estimate with its sample size is honest.
  - Design choice: threat feeds stay ordinary URL filter lists. No `threat_intelligence` list kind,
    no feed API fetchers, no auto-blocking of high-confidence names. Blocking goes through S-6 or S-12
    proposals.
- [S-11] (#50) Capacity forecasts (`capacity_forecast`, `NEXORA_AI_CAPACITY_FORECAST_INTERVAL`, 24 h).
  - The agent appends one daily sample per resource to `ai_capacity_samples`, kept 400 days, and
    reads the M6 rollup (8 days). Resources:
    - `filter_index`: filter index bytes against its cap from the engine stats filter index sample, per engine, max over the
      fleet;
    - `cache`: `nexora_cache_bytes` against `cache_max_bytes`;
    - `recursor_cache`: the M7 budget;
    - `engine_memory`: resident against the cgroup limit;
    - `blocklist_entries`: the sum of enabled lists' `entry_count`, no limit;
    - `query_volume`: daily queries, no limit.
  - Code fits a least-squares line over the available daily points (at least 3). It computes the
    growth per day and week, the projected exhaustion date and days remaining (only with a limit and
    positive growth), and a trend (`stable|growing|shrinking`).
  - The model adds confidence (0..1, at most 0.5 with fewer than 7 points), a recommendation text, and
    optionally a proposal action `updateResolverSettings` `cache_max_bytes` for `cache`. The other
    limits have no API setting, so their advice is text only.
  - Forecasts are stored with kind `capacity` and `valid_until = generated_at + interval`. Fewer than
    3 points gives `insufficient_data` with no model call.
- [S-12] (#51) RPZ rule suggestions (`rpz_suggestions`, `NEXORA_AI_RPZ_SUGGESTIONS_INTERVAL`, 24 h).
  - Inputs from code over 24 h:
    - not-blocked names that are S-4 candidates or have a cached threat verdict with confidence
      ≥ 0.7;
    - domain families: at least 5 distinct queried names under one parent, each with leftmost-label
      entropy ≥ 3.5;
    - look-alikes of hosted zone names (Damerau-Levenshtein distance 1–2 on the registrable label).
  - The model returns rules `{record, policy (nxdomain|nodata|drop|passthru), category, reason,
confidence}` for at most 50 records.
  - Validation rejects:
    - names that are not valid DNS names;
    - wildcards with fewer than 2 labels under the `*` (`*.com` is rejected);
    - names at or under a hosted zone;
    - names of configured upstream hosts (DoT server names, DoH URL hosts);
    - the `NEXORA_PUBLIC_URL` host;
    - names on an allowlist.
  - Each rule is one proposal with action `appendAiRpzRules`.
  - **Apply selected** (`applyAiProposals` with several ids) compiles the chosen rules into two
    replayed requests:
    1. `createRpzZone` for the file zone `ai-suggested.rpz` (policy override `given`, min refresh 300) when it does not exist.
    2. One `uploadRpzZoneFile` whose content is every rule ever applied (`ai_rpz_rules`) plus the
       selection, rendered as BIND RPZ with one comment line per rule (proposal id, category, reason
       truncated to 200 characters).
  - The zone is written only by apply. The GUI says that manual uploads to it are replaced by the
    next apply.
  - Rules appear in `ai_rpz_rules` only after a successful upload.
- [S-13] (#52) MCP server.
  - `NEXORA_MCP_ENABLED` (default `false`) mounts a Model Context Protocol endpoint at `/mcp` on the
    HTTP listener, using the Streamable HTTP transport.
    - `POST` carries JSON-RPC and is answered with one JSON body. `GET` answers 405.
    - Protocol versions `2025-06-18` and `2025-03-26`. No session id.
    - Implemented in `mgmt/internal/mcpserver` without a new dependency. go-ai-sdk ships only an MCP
      client, which the tests use.
  - It works whether or not AI is configured.
  - **Authentication and RBAC are those of the API:** a bearer `nxt_` API token or the session
    cookie, checked by the same authentication service.
    - An `Origin` header that differs from the `NEXORA_PUBLIC_URL` origin gets 403, as DNS-rebinding
      protection.
    - A missing or invalid credential gets 401.
    - Every tool call is replayed in-process through the API handler with the caller's credentials,
      exactly as in S-2. Mutations therefore write the normal audit rows, and reads are not audited
      (the Nexora rule).
  - **Tools**: the #52 list, each mapped to one operation. Input schemas come from the OpenAPI
    parameters and request body (refs inlined).
    - `tools/list` shows only the tools whose operation the caller's role may call.
    - With `NEXORA_MCP_READ_ONLY=true` (default), only `GET` operations are listed and callable.
      Design choice: a safe default in the spirit of suggest-only; an admin turns it off explicitly.
    - A forbidden or unknown tool returns a tool result with `isError: true` and the API error code.
  - **Resources** (template):
    - `nexora://fleet/status`: `getFleetSummary`;
    - `nexora://policy-groups`: `listPolicyGroups`;
    - `nexora://engine-groups`: `listEngineGroups`;
    - `nexora://query-log/recent`: `searchQueryLog`, limit 100;
    - `nexora://filter-lists/{id}/content`: the list's current blob, the first 10,000 names, after
      a `getFilterList` authorization.
  - **Prompts:** `nexora_status`, `nexora_query_log` (argument `minutes`, default 15) and
    `nexora_filter_health`.
  - `nexora-mgmt mcp-stdio --url <base URL> --token-file <file> [--ca-file <file>]` bridges stdio
    JSON-RPC to the HTTP endpoint, for clients that only speak stdio. Same auth, no store access.
  - Design choice: no separate MCP port (the embedded endpoint already speaks Streamable HTTP), no
    build tag, no argument completions.
- [S-14] GUI.
  - A sidebar group **AI**, shown only when the AI status reports enabled, with:
    - **Insights** (`/ai/insights`): anomalies and insights with acknowledge and dismiss;
    - **Recommendations** (`/ai/recommendations`): proposals, tabs by source, a diff view, apply with
      a confirmation dialog listing every action, dismiss;
    - **Assistant** (`/ai/assistant`, operator);
    - **Forecasts** (`/ai/forecasts`): upstream and capacity, with freshness;
    - **AI status** (`/ai`): provider host and model, never the key; budget; agents with Run now;
      MCP endpoint state.
  - Inline:
    - a Dashboard AI card with score and top 3 insights;
    - a Query log "Ask" box (S-3), an anomaly banner and a threat badge in the Reason column;
    - a risk card on the rollout page;
    - a classification breakdown and a "Check domains" dialog on Blocklist / allowlist;
    - an "AI suggestions" tab on RPZ;
    - an upstream predictions panel on Forwarding & recursion.
  - When AI is off, none of these render, and `/ai*` routes show "AI is off" with the reason; admins
    also see the configuration hint.
  - Apply and dismiss controls render only for operator and admin.
  - Every control has an M6 help catalogue entry. Every page works at 400 px and in dark and light
    themes.
- [S-15] Deployment and documentation.
  - The Helm chart gains `mgmt.ai.existingSecret` (empty by default). When set, the mgmt Deployment
    reads `NEXORA_AI_BASE_URL`, `NEXORA_AI_MODEL` and `NEXORA_AI_API_KEY` from that Secret's keys
    `base-url`, `model` and `api-key` with `secretKeyRef` `optional: true`. A missing Secret therefore
    leaves AI off instead of failing the pod.
  - The chart also gains `mgmt.mcp.enabled` and `mgmt.mcp.readOnly`.
  - `deploy/kw/values-kw.yaml` sets `existingSecret: nexora-ai`, MCP enabled and read-only.
  - The PrometheusRule gains:
    - `NexoraAIAgentFailing`: no successful run for 3 intervals and at least one failed run;
    - `NexoraAIBudgetExhausted`: the used ratio ≥ 1 for 10 min.
  - `docs/operations.md` gains an AI section (configuration, privacy guard, bounds, metrics, MCP
    client setup) and the help topic `ai`.
  - `docs/architecture.md` gains the M11 design.

## Out of scope

- Any AI action that changes configuration without an operator applying it, auto-apply switches, and
  the auto-approve for upstream recommendations mentioned in #47.
- Engine changes, proto changes, and any AI on the DNS query path.
- Embeddings, a semantic query-log index and `CosineSimilarity` pattern matching: fastllm serves no
  embedding model.
- OpenTelemetry spans for AI calls. The management plane has no tracing today, so metrics and logs
  cover observability.
- Token streaming, SSE and WebSocket for the assistant or search.
- Model tool calling inside Nexora's own features. Tools are exposed only to external agents through
  MCP.
- A `threat_intelligence` filter-list kind, commercial threat-feed API integrations (VirusTotal, OTX,
  URLhaus API JSON), WHOIS lookups, and auto-blocking from feeds.
- AI classification of every entry of a list (sampled only).
- A separate MCP listener, an MCP build tag, MCP argument completions, sampling, elicitation and MCP
  session state.
- Storing or displaying model reasoning text.
- Per-user AI quotas (the budget is fleet-wide).
- Cloud LLM providers in the kw deployment. The code stays provider-agnostic, but only fastllm is
  deployed and smoke-tested.

## Constraints

- **Suggest-only (owner decision, binding):**
  - No agent, task, model output or MCP read-only tool mutates configuration.
  - The only writer of AI suggestions into configuration is `applyAiProposals`, called by a human with
    operator role or above, replaying the normal API with that human's credentials, and audited.
  - `TestNoAgentWritesConfiguration` enforces this.
- **Privacy:**
  - Query data is sent only to the configured endpoint, which must be private unless explicitly
    allowed.
  - The API key never enters the repository, the database, logs, metrics, `getAiStatus`, the GUI or
    audit rows.
  - On kw it lives only in Kubernetes Secret `nexora-ai` in namespaces `nexora` and `nexora-dev`.
  - `TestHelmAISecretWiring` asserts the chart takes the key only through `secretKeyRef`.
- **Off means off:** without base URL and model, the management plane starts no AI goroutine and
  opens no AI connection. Every pre-M11 test passes unchanged, and the startup time and memory of an
  unconfigured instance do not measurably change.
- **Provider-agnostic:** only `mgmt/internal/ai/provider.go` constructs a provider. Unit tests use the
  go-ai-sdk `aitest` MockModel as the fake provider. End-to-end tests use the scripted fake
  OpenAI-compatible server `nexora-fixture openai`. A smoke test runs against the real fastllm.
- **Bounded:** the S-1 limits apply to every model call, background or interactive, and every bound
  has a metric.
- **No engine changes:** M11 touches no file under `engine/` or `proto/`. The DNS hot path rules in
  `docs/architecture.md` are unaffected.
- **Asynchronous AI:** rollout creation, config mutations, snapshot publishing, the query-log search
  and the dashboard never wait for a model call.
- **Contract rules:**
  - Every new operation has a role in `mgmt/internal/auth/permissions.go` and the same role in
    `web/src/auth/permissions.ts` (`TestPermissionsCoverEveryOperation`, `pnpm lint`).
  - `TestGUICoverage` requires every new operation to be requested by a Playwright screen spec.
  - M11 specs are numbered 50–69 (`web/e2e/screens/5[0-9]-ai-*.spec.ts`, `6[0-9]-*`).
- **Migrations:** `mgmt/migrations/01200_ai_usage.sql` through `01219_*`. M11 uses 01200–01207 (listed
  under Data).
- **Dependencies:**
  - Go `github.com/azrtydxb/go-ai-sdk v0.4.1`. It needs Go 1.26+, and Nexora is on Go 1.27.
  - No other new Go module.
  - No new npm dependency.
- **Every prompt starts with the line `nexora-feature: <feature>`,** so the fake server and logs can
  attribute calls. Prompt templates are Go constants in the feature packages.
- **Existing tests:** every Go, Rust and Playwright test keeps passing. Tests change only where M11
  adds a nav group or a widget with a new test id.
- **Deployment:** kw deploys follow the roadmap: all tests green, `scripts/kw-deploy.sh` with zero lost
  probe queries on 192.168.10.136 and 192.168.10.139, `scripts/kw-acceptance.sh`, and rollback on
  failure. 192.168.10.136 and 192.168.10.139 never change.

## Interfaces

- **Management environment** (defaults in parentheses):
  - Connection: `NEXORA_AI_BASE_URL`, `NEXORA_AI_MODEL`, `NEXORA_AI_API_KEY`,
    `NEXORA_AI_ALLOW_PUBLIC_ENDPOINT` (`false`).
  - Model calls:
    - `NEXORA_AI_STRUCTURED_OUTPUT` (`json_schema` | `prompt`)
    - `NEXORA_AI_MAX_TOKENS` (`16384`)
    - `NEXORA_AI_TEMPERATURE` (`0.2`)
    - `NEXORA_AI_TIMEOUT` (`180s`)
    - `NEXORA_AI_VALIDATION_ATTEMPTS` (`3`)
  - Limits:
    - `NEXORA_AI_MAX_CONCURRENCY` (`2`)
    - `NEXORA_AI_REQUESTS_PER_MINUTE` (`20`)
    - `NEXORA_AI_DAILY_TOKEN_BUDGET` (`2000000`)
    - `NEXORA_AI_BACKGROUND_BUDGET_PERCENT` (`80`)
    - `NEXORA_AI_LLM_MIN_INTERVAL` (`5m`)
    - `NEXORA_AI_AGENT_START_DELAY` (`2m`)
  - Agents, each `_ENABLED` (`true`) plus `_INTERVAL` where noted; an interval of `0` disables:
    - `NEXORA_AI_QUERYLOG_ENABLED` / `_INTERVAL` (`30s`)
    - `NEXORA_AI_DASHBOARD_ENABLED` / `_INTERVAL` (`30s`)
    - `NEXORA_AI_FILTER_RECOMMENDATIONS_ENABLED` / `_INTERVAL` (`6h`)
    - `NEXORA_AI_UPSTREAM_PREDICTION_ENABLED` / `_INTERVAL` (`6h`)
    - `NEXORA_AI_ROLLOUT_RISK_ENABLED` (no interval)
    - `NEXORA_AI_THREAT_CLASSIFICATION_ENABLED` / `_INTERVAL` (`6h`)
    - `NEXORA_AI_CAPACITY_FORECAST_ENABLED` / `_INTERVAL` (`24h`)
    - `NEXORA_AI_RPZ_SUGGESTIONS_ENABLED` / `_INTERVAL` (`24h`)
    - `NEXORA_AI_CONFIG_ASSISTANT_ENABLED` (no interval)
  - MCP: `NEXORA_MCP_ENABLED` (`false`), `NEXORA_MCP_READ_ONLY` (`true`).
- **Agent names** (`runAiAgent` path enum): `querylog_anomalies`, `dashboard_insights`,
  `filter_recommendations`, `upstream_prediction`, `rollout_risk`, `threat_classification`,
  `capacity_forecast`, `rpz_suggestions`.
- **Feature label values:** the agent names plus `querylog_search`, `config_assistant` and
  `threat_check`.
- **HTTP API (`mgmt/api/openapi.yaml`), new operations:**

  | operationId                     | Method and path                             | Role     |
  | ------------------------------- | ------------------------------------------- | -------- |
  | `getAiStatus`                   | `GET /ai/status`                            | viewer   |
  | `runAiAgent`                    | `POST /ai/agents/{agent}/run`               | operator |
  | `getAiTask`                     | `GET /ai/tasks/{id}`                        | viewer   |
  | `startAiQueryLogSearch`         | `POST /ai/query-log/search`                 | viewer   |
  | `listAiFindings`                | `GET /ai/findings?kind=&status=&limit=`     | viewer   |
  | `updateAiFinding`               | `PATCH /ai/findings/{id}`                   | operator |
  | `getAiInsights`                 | `GET /ai/insights`                          | viewer   |
  | `listAiProposals`               | `GET /ai/proposals?source=&status=&limit=`  | viewer   |
  | `getAiProposal`                 | `GET /ai/proposals/{id}`                    | viewer   |
  | `applyAiProposals`              | `POST /ai/proposals/apply`                  | operator |
  | `dismissAiProposals`            | `POST /ai/proposals/dismiss`                | operator |
  | `createAiAssistantSession`      | `POST /ai/assistant/sessions`               | operator |
  | `getAiAssistantSession`         | `GET /ai/assistant/sessions/{id}`           | operator |
  | `postAiAssistantMessage`        | `POST /ai/assistant/sessions/{id}/messages` | operator |
  | `listAiForecasts`               | `GET /ai/forecasts?kind=`                   | viewer   |
  | `getAiRolloutRisk`              | `GET /rollouts/{id}/ai-risk`                | viewer   |
  | `startAiThreatCheck`            | `POST /ai/threat-check`                     | viewer   |
  | `getAiFilterListClassification` | `GET /filter-lists/{id}/ai-classification`  | viewer   |
  - `getAiTask` returns 404 to anyone but the requester or an admin.
  - `applyAiProposals` body `{ids (1..100), acknowledge_license?}`, response
    `{results[] {id, status, actions[] {operation_id, http_status, code, message}}}`.
  - `dismissAiProposals` body `{ids (1..100), reason? (≤ 500)}`.
  - `QueryLogRecord` gains `threat`.
  - Design choice: every new path lives under `/ai`, except the two that extend an existing resource
    (`/rollouts/{id}/ai-risk`, `/filter-lists/{id}/ai-classification`). One prefix gives one AI-off
    gate and a clear MCP mapping. This replaces the scattered paths proposed in the issues.
  - Error codes:
    - `ai_disabled` (503);
    - `ai_busy` (429);
    - `ai_budget_exhausted` (429);
    - `feature_disabled` (503, the feature's `_ENABLED` is false);
    - `proposal_not_open` (409, apply or dismiss of a non-open proposal; per id in results);
    - `unknown_agent` (404).
  - Task status: `queued|running|succeeded|failed`; task error codes: `invalid_output`, `timeout`,
    `provider_error`, `endpoint_not_private`, `budget_exhausted`, `instance_stopped`.

- **MCP:**
  - `POST /mcp` (not an OpenAPI operation, not `/api/v1`), JSON-RPC methods `initialize`,
    `notifications/initialized`, `ping`, `tools/list`, `tools/call`, `resources/list`,
    `resources/templates/list`, `resources/read`, `prompts/list`, `prompts/get`.
  - CLI `nexora-mgmt mcp-stdio`.
- **Prometheus (management plane):**
  - Model calls:
    - `nexora_mgmt_ai_enabled`
    - `nexora_mgmt_ai_requests_total{feature,outcome}`, outcome
      `ok|invalid_output|timeout|provider_error|rate_limited|budget_exhausted|endpoint_not_private`
    - `nexora_mgmt_ai_request_duration_seconds{feature}`, buckets 1, 2, 5, 10, 20, 40, 80, 160, 320
    - `nexora_mgmt_ai_tokens_total{feature,kind}`, kind `input|output|reasoning`
    - `nexora_mgmt_ai_validation_retries_total{feature}`
    - `nexora_mgmt_ai_inflight_requests`
    - `nexora_mgmt_ai_queue_wait_seconds`
    - `nexora_mgmt_ai_budget_used_ratio`
  - Agents:
    - `nexora_mgmt_ai_agent_runs_total{agent,outcome}`, outcome
      `ok|no_change|skipped_locked|skipped_budget|failed`
    - `nexora_mgmt_ai_agent_last_success_timestamp_seconds{agent}`
  - State: `nexora_mgmt_ai_open_findings{kind,severity}`, `nexora_mgmt_ai_open_proposals{source}`.
  - MCP: `nexora_mgmt_mcp_requests_total{method,outcome}`, `nexora_mgmt_mcp_tool_calls_total{tool,outcome}`.
- **GUI:**
  - Routes `/ai`, `/ai/insights`, `/ai/recommendations`, `/ai/assistant` and `/ai/forecasts`, and the
    sidebar group `nav-ai`.
  - Components `ProposalCard`, `ProposalApplyDialog`, `JsonDiff`, `FindingCard`, `AiTaskStatus`,
    `AiOff`.
  - Hooks in `web/src/api/ai.ts`.
  - Help catalogue area `web/src/help/catalog/ai.ts` and topic `web/src/help/topics/ai.md`.
- **Test fixture:**
  - `nexora-fixture openai --listen ADDR` serves `POST /v1/chat/completions` (OpenAI wire format,
    with `reasoning_content` and `usage.completion_tokens_details.reasoning_tokens`).
  - Control: `PUT /control/script` sets the per-feature response queues, `GET /control/requests`
    lists recorded calls (feature, model, whether a bearer key was present, the response format type,
    `max_tokens`), and `POST /control/reset` clears both.

## Data

PostgreSQL, owned by the management plane:

- `01200_ai_usage.sql`: `ai_usage(day date, feature text, requests bigint, input_tokens bigint,
output_tokens bigint, reasoning_tokens bigint, primary key (day, feature))`, upserted after every
  call.
- `01201_ai_runs_tasks.sql`:
  - `ai_agent_runs(id bigserial, agent text, instance_id text, started_at, finished_at, outcome text,
input_tokens, output_tokens, error text)`, indexed `(agent, started_at desc)`.
  - `ai_agent_requests(agent text primary key, requested_at, requested_by)` for Run now.
  - `ai_tasks(id uuid, kind text, status text, requested_by text, requester_kind text, instance_id text,
input jsonb, result jsonb, error_code text, error_message text, created_at, started_at,
finished_at)`.
- `01202_ai_proposals.sql`: `ai_proposals(id uuid, source text, fingerprint text, status text check in
(open, applied, failed, stale, dismissed, superseded), title, description, priority, impact jsonb,
evidence jsonb, actions jsonb, risk jsonb, session_id uuid null, created_at, updated_at,
reviewed_by text, reviewed_at, result jsonb, dismiss_reason text)`, with a unique partial index on
  `fingerprint` where `status = 'open'`; and `ai_rpz_rules(record text primary key, policy text,
category text, reason text, proposal_id uuid, applied_by text, applied_at)`.
- `01203_ai_findings_forecasts.sql`:
  - `ai_findings(id uuid, kind text check in (anomaly, insight), candidate_id text, status text check in
(open, acknowledged, dismissed, resolved), severity text check in (info, warning, critical),
confidence real, title, description, detail jsonb, explained bool, first_seen, last_seen,
updated_by text, updated_at)`, unique `(kind, candidate_id)` where status in (open, acknowledged).
  - `ai_forecasts(id uuid, kind text check in (upstream, capacity), subject text, detail jsonb,
proposal_id uuid null, generated_at, valid_until)`, unique `(kind, subject, generated_at)`.
- `01204_ai_assistant.sql`: `ai_assistant_sessions(id uuid, owner_id text, owner_kind text, title text,
created_at, updated_at)` and `ai_assistant_messages(id bigserial, session_id uuid, role text check in
(user, assistant), content text, proposal_id uuid null, task_id uuid null, created_at)`.
- `01205_ai_capacity_samples.sql`: `ai_capacity_samples(day date, resource text, value double precision,
limit_value double precision null, primary key (day, resource))`.
- `01206_ai_rollout_risks.sql`: `ai_rollout_risks(rollout_id uuid primary key references rollouts on
delete cascade, status text check in (pending, assessed, failed, skipped), risk_score int, risk_level
text, analysis text, detail jsonb, proposal_id uuid null, assessed_at, error text)`.
- `01207_ai_threat.sql`: `ai_domain_verdicts(name text primary key, is_threat bool, categories text[],
confidence real, reasoning text, checked_at, expires_at)` and `ai_list_classifications(list_id uuid
primary key references filter_lists on delete cascade, blob_sha256 text, sample_size int, breakdown
jsonb, classified_at)`.

Other data:

- Nothing is stored in engines, `state_dir` or snapshots. Model reasoning text is discarded.
- Prompts contain query names, client addresses, list names and config JSON as returned by the API
  (which never returns secrets). They go only to the configured endpoint and are never persisted.
  Task results store only the validated outputs.
- Browser localStorage: `nexora-ai-assistant-session` (the last session id), wrapped in try/catch.
- Kubernetes: Secret `nexora-ai` (keys `base-url`, `model`, `api-key`) in `nexora` and `nexora-dev`,
  created by the owner and never templated by the chart.

## Edge cases

- **Configuration:**
  - The base URL is set but the model is not, or the reverse: AI off with `incomplete_configuration`.
  - The base URL has a trailing slash or `/v1/`: it is normalised.
  - The base URL host does not resolve at start: AI stays on (fastllm may start later), calls fail
    with `provider_error`, and the status shows the last error.
- **Privacy:**
  - A host resolving to both a private and a public address counts as public.
  - An IP literal is checked directly.
  - `localhost` is allowed.
- **Model output:**
  - Empty content with `finish_reason=length` is retried with double max tokens.
  - JSON wrapped in fences, or content after `</think>`, is decoded after stripping.
  - Extra fields are ignored in the model object but rejected in action bodies.
  - A confidence outside 0..1, or a `risk_level` that does not match the score, fails validation and
    is retried.
- **Prompt injection:** a queried name such as `ignore-previous-instructions.example` or list content
  aimed at the model can only produce text or proposals.
  - Validation still restricts ids, operations and RPZ names.
  - Proposals require review.
  - GUI text is rendered as text, never HTML or markdown links.
  - `TestPromptInjectionCannotEscalate` feeds such a name and a scripted malicious answer (an unknown
    operation and a `deleteZone` action) and asserts both are rejected.
- **Proposals:**
  - Apply of a proposal whose resource was deleted: action 404 → status `failed`.
  - Revision changed: 409 → `stale`.
  - A viewer calling `applyAiProposals` gets 403 before any replay.
  - An operator applying a proposal containing an admin-only operation: impossible by the allowlist
    (none is admin-only). A replayed 403 would be recorded as `failed`.
  - Two operators applying the same proposal at once: a row lock (`FOR UPDATE SKIP LOCKED`) lets one
    apply, and the other gets `proposal_not_open`.
  - Mixed ids in one apply: each is processed independently in id order, and RPZ rule proposals are
    grouped into one upload.
  - A non-commercial category source without `acknowledge_license: true`: the replay answers 400 and
    the proposal stays open with the message.
- **Agents:**
  - Two instances: only the lock holder runs.
  - An instance dies mid-run: the lock is released with the connection, the run row stays without
    `finished_at` and is closed as `failed` (`instance_stopped`) by the next prune.
  - An interval shorter than a run: the next run waits for the lock, and runs never overlap.
  - Run now while the agent is running: the request is kept and runs once afterwards.
  - The budget is spent mid-run: remaining model calls are skipped, detector findings are still
    stored with `explained: false`, and the outcome is `skipped_budget`.
- **Query log:**
  - No backend (`Noop`): query-log agents store no candidates and report outcome `no_change`.
  - OpenSearch unavailable: the run fails with `querylog_unavailable` and the next run retries.
  - A window with more than 5,000 records: the newest 5,000 are analysed, and the run detail records
    `sampled: true`.
- **NL search:**
  - The model returns a time range in the future or longer than 7 days: validation fails and retries.
  - It names an unknown policy group: validation fails with the known names in the error text.
  - Searching "everything" yields no filter, which is valid.
- **Assistant:**
  - A plan for a policy group that another operator deleted meanwhile fails validation (unknown path
    id) and the model is re-asked.
  - A session owner deleted: sessions of deleted users are pruned.
  - An API token owner can use its own sessions through the API.
- **Rollouts:**
  - Rollback, republish and immediate (content equal to stable) rollouts are skipped with status
    `skipped`.
  - A rollout already `completed` before assessment is still assessed (history).
  - A group with no history: similarity list empty; the model must not cite versions.
- **Upstreams:**
  - An upstream renamed or deleted since the samples: matched by id; deleted upstreams are skipped.
  - A group with a single enabled upstream: `disable` is not allowed by validation.
- **Capacity:**
  - Engines restarted (a counter reset) are handled by the M6 series code.
  - A missing limit gives no exhaustion date.
  - Negative growth gives no exhaustion date and trend `shrinking`.
- **RPZ:**
  - A suggested name already in `ai_rpz_rules` gets no new proposal.
  - The operator deleted the `ai-suggested.rpz` zone: the next apply recreates it with all recorded
    rules.
  - Upload content over 3 MiB: apply fails with the API's 400 and nothing is recorded (roughly 40,000
    rules).
- **MCP:**
  - A JSON-RPC batch array is rejected with error -32600 (batches were removed in `2025-06-18`).
  - Unknown method: -32601.
  - `tools/call` for a GET tool with a body argument: the body is ignored.
  - A tool result larger than 1 MiB is truncated with `truncated: true` in the text.
  - `initialize` with an unsupported protocol version answers with `2025-06-18`.
- **GUI:**
  - AI status fails to load: the AI nav and widgets stay hidden and nothing else breaks.
  - A task poll returns 404 (pruned): "This request expired".
  - A viewer opening `/ai/assistant` is redirected to `/ai`.

## Failure modes

- **fastllm down or unreachable:**
  - Calls fail after the SDK retries with `provider_error`.
  - Background runs record `failed` and retry at the next interval; detector-only findings still
    appear.
  - Interactive tasks end `failed` with the message.
  - `nexora_mgmt_ai_requests_total{outcome="provider_error"}` rises, and `NexoraAIAgentFailing` fires
    after 3 intervals.
  - DNS, the API and the GUI are unaffected.
- **fastllm slow:** the per-call timeout (180 s) gives `timeout`. At most 2 calls block slots, so
  interactive requests get `ai_busy` after 5 s instead of queueing without bound.
- **fastllm returns garbage or non-schema JSON:** validation retries up to 3 attempts, then
  `invalid_output`, with nothing stored and `nexora_mgmt_ai_validation_retries_total` counting.
- **fastllm rejects `response_format`** (400): the call fails with `provider_error` and the message.
  The operator sets `NEXORA_AI_STRUCTURED_OUTPUT=prompt`. `TestKwSmokeAI` proves the mode configured on
  kw works.
- **Wrong API key** (401): `provider_error`, and the status shows "provider rejected the credentials"
  without the key.
- **Budget exhausted:** background agents skip model calls at 80%, and interactive requests get 429 at
  100% until 00:00 UTC. `NexoraAIBudgetExhausted` fires.
- **PostgreSQL down:** AI operations answer 503 like the rest of the API. Agents fail their runs and
  hold no locks.
- **Query-log backend down:** query-log agents and NL search fail with `querylog_unavailable`. Other
  agents run.
- **Secret `nexora-ai` missing on kw:** the pod starts with AI off (`not_configured`), and
  `TestKwSmokeAI` fails loudly with that reason.
- **The replay target handler fails** (500): that action records 500, the proposal becomes `failed`,
  and the operator can retry by regenerating.
- **MCP client floods:** MCP requests pass through the same HTTP metrics and body limit (4 MiB). Tool
  calls hit the same handlers and database pool as the API. No AI budget is involved, because MCP
  calls no model.
- **Management instance killed during apply:** actions already replayed stay applied, each with its
  audit row. The proposal stays `open` with a partial `result`, and a second apply replays from the
  first action. Stale revisions make the already-applied actions return 409, which is recorded as
  `stale`, so nothing is applied twice.

## Acceptance criteria

- [ ] [S-1] `TestLoadAIConfig` (`mgmt/internal/config`) proves:
  - AI is off with `not_configured` when base URL and model are empty, and with
    `incomplete_configuration` when only one is set;
  - every default listed under Interfaces;
  - interval `0` disables an agent;
  - invalid durations and numbers fail `Load` with the variable name.

  Fails if a partial configuration turns AI on or a default differs.

- [ ] [S-1] `TestEndpointPrivacyGuard` accepts `http://192.168.10.125:4000/v1`, `http://localhost:1234/v1`
      and `http://[fd00::1]/v1`. It rejects `https://api.openai.com/v1`, and a host resolving to a
      private and a public address, with `endpoint_not_private`, and accepts them with
      `NEXORA_AI_ALLOW_PUBLIC_ENDPOINT=true`. Fails if a public endpoint is accepted by default.
- [ ] [S-1] `TestGenerateValidatesAndRetries` uses go-ai-sdk `aitest.MockModel`:
  - fenced JSON decodes;
  - invalid JSON then valid JSON succeeds on attempt 2, and the second call carries the decode error
    text;
  - a validator error is fed back the same way;
  - three invalid answers return `invalid_output`, with
    `nexora_mgmt_ai_validation_retries_total{feature}` = 2 and nothing returned;
  - `finish_reason=length` with empty text retries with `MaxTokens` 32768;
  - usage input, output and reasoning tokens reach `ai_usage` and `nexora_mgmt_ai_tokens_total`.

  Fails if an invalid object is returned or tokens are not counted.

- [ ] [S-1] `TestServiceBounds` proves:
  - 6 concurrent calls with concurrency 2 never exceed 2 in flight (`nexora_mgmt_ai_inflight_requests`);
  - a rate of 3/min rejects the 4th interactive call in the same minute with `ai_busy` after the 5 s
    wait;
  - with a budget of 1,000 tokens and 850 used, a background call gets `ErrBudgetExhausted` and an
    interactive call succeeds; at 1,000 used the interactive call gets `ai_budget_exhausted`;
  - the budget sums over two service instances sharing one database.

  Fails if any bound is exceeded.

- [ ] [S-1] `TestSchedulerRunsOnceAcrossInstances` starts two schedulers on one database with a test
      agent (interval 2 s) for 7 s. It asserts:
  - 3 or 4 runs in total with no overlap;
  - `skipped_locked` counted on the loser;
  - a restart does not run again before the interval;
  - `runAiAgent` triggers an immediate run;
  - interval 0 never runs.

  `TestTasksInstanceLoss` asserts that a `running` task of a stale instance becomes `failed`
  `instance_stopped`. Fails if agents overlap or run on both instances.

- [ ] [S-1] `TestAIOpenAICompatibleWire` (e2e) runs mgmt against `nexora-fixture openai`. The fixture
      records a request with `Authorization: Bearer <test key>`, the configured model,
      `response_format.type` `json_schema` and `max_tokens` 16384. A scripted answer with
      `reasoning_content` and 900 reasoning tokens raises `nexora_mgmt_ai_tokens_total{kind="reasoning"}`
      by 900. `getAiStatus` never contains the key. With `NEXORA_AI_STRUCTURED_OUTPUT=prompt` the
      request has no `response_format` and a fenced answer still succeeds. Fails if the wire format,
      token accounting or key hiding is wrong.
- [ ] [S-1] `TestAIDisabledChangesNothing` (e2e):
  - first proves the positive path: a configured mgmt calls the fixture within 60 s of query traffic;
  - then starts an unconfigured mgmt next to a running fixture, sends query traffic for 60 s and
    asserts zero fixture requests;
  - `getAiStatus` `{enabled:false, reason:"not_configured"}`, `listAiProposals` 503 `ai_disabled`,
    and no `nexora_mgmt_ai_agent_runs_total` samples;
  - `TestGUICoverage` specs `01`–`35` pass unchanged.

  Fails if an unconfigured instance contacts any AI endpoint or an existing test changes.

- [ ] [S-2] `TestProposalActionValidation` rejects:
  - an operation outside the allowlist (`deleteZone`);
  - an unknown body field;
  - a wrong type;
  - an unknown path id;
  - a stale revision.

  It accepts a valid `updatePolicyGroup`. `TestPromptInjectionCannotEscalate` asserts the injected
  name and scripted malicious answer produce no stored proposal and a `invalid_output` task. Fails if
  an invalid action is stored.

- [ ] [S-2] `TestApplyAiProposalsReplaysThroughAPI` (e2e, real mgmt and engine):
  - a viewer gets 403;
  - an operator applies a `updatePolicyGroup` proposal: the group changes, a new config version
    reaches the engine, `audit_log` has `updatePolicyGroup` with the operator as actor and
    `applyAiProposals` with the proposal id and HTTP 200;
  - a second apply gets `proposal_not_open`;
  - a proposal whose group revision changed ends `stale` with 409 recorded;
  - dismiss records the reason and audit row.

  Fails if apply bypasses RBAC, audit or revision checks.

- [ ] [S-2] `TestNoAgentWritesConfiguration` (e2e) scripts every agent's fixture answers so that each
      produces at least one proposal (positive path asserted). It runs every agent with `runAiAgent`,
      then asserts `config_versions`, every configuration table checksum and the non-AI `audit_log`
      rows are unchanged. Fails if any AI path mutates configuration.
- [ ] [S-3] `TestQueryLogSearchTranslation` (unit, fake provider):
  - "blocked TXT queries from the guest group in the last 2 hours" becomes `filter=blocked`,
    `qtype=TXT`, the guest group id and a 2 h range;
  - an unknown group name or an 8-day range is re-asked with the error text;
  - no range defaults to 1 h.

  `TestAIQueryLogSearch` (e2e, builtin backend, real engine) runs a task to `succeeded` with filters
  that return the seeded blocked record through `searchQueryLog`, and a summary from the scripted
  answer. Fails if filters are invalid or the task never finishes.

- [ ] [S-4] Detector unit tests `TestTunnelingDetector`, `TestNXDomainBurstDetector`,
      `TestQueryFloodDetector`, `TestPeriodicBeaconDetector` and `TestCategoryEscalationDetector` each
      use a synthetic record set just above and just below the thresholds. `TestAnomalyAgentLLMOnlyOnChange`
      asserts:
  - one model call for a new candidate set;
  - none on an unchanged set within 5 minutes;
  - a call after a severity change;
  - findings stored with `explained: false` when the model fails;
  - `resolved` after 30 minutes without detection.

  `TestAIQueryLogAnomalies` (e2e) sends 80 TXT queries with 32-character random labels under one
  parent through a real engine, runs `querylog_anomalies`, and sees a `dns_tunneling` finding for
  `127.0.0.1` with the scripted description. Fails if a threshold is off by one, the model is called
  on every tick, or real traffic is not detected.

- [ ] [S-5] `TestInsightDetectors` seeds `engine_stats` for two engines with a SERVFAIL spike and an
      upstream RTT rise and asserts both candidates. `TestInsightScore` asserts the score is 4 for one
      critical and one warning without any model call. `TestDashboardInsightCorrelation` (fake
      provider) stores one insight citing both candidate ids and rejects an answer citing an unknown id.
      Fails if the score depends on the model or correlations cite unknown signals.
- [ ] [S-6] `TestFilterRecommendationAgent` (storetest, builtin backend, fake provider) seeds 24 h of
      records. It asserts:
  - an `enable_category` proposal for the guest group with the computed impact numbers (not the
    model's);
  - a `block_domains` proposal with an `appendAiRpzRules` action;
  - a `safe_search` proposal;
  - a second run refreshes the same fingerprints instead of duplicating;
  - a dismissed fingerprint is not re-proposed within 7 days.

  Fails if impact comes from the model or duplicates appear.

- [ ] [S-7] `TestAssistantPlanValidation` (unit) accepts a plan creating a policy group with existing
      category keys. It re-asks for an unknown category, a CIDR outside the syntax and an operation
      outside the allowlist, and accepts a reply-only answer. `TestAIConfigAssistant` (e2e) asserts:
  - an operator session message "Block adult content for 10.9.0.0/24 as guest-wifi" yields a plan
    proposal with `createPolicyGroup`;
  - a second message supersedes it;
  - apply creates the group with an audit row by the operator;
  - another operator gets 404 on the session;
  - a viewer gets 403.

  Fails if a plan applies without apply or leaks across users.

- [ ] [S-8] `TestUpstreamTrendFeatures` asserts the slope of a series climbing 50 ms/h, a step change
      from 10 to 200 ms, a daily periodic spike, the crossing time of `timeout_ms`, and
      `insufficient_data` below 12 points. `TestUpstreamPredictionAgent` (storetest, fake provider)
      stores a `degrading` forecast with `valid_until = generated_at + 6h` and a `switch_strategy`
      proposal. It rejects `disable` for the only enabled upstream. Fails if trends are computed by the
      model or an unsafe recommendation is stored.
- [ ] [S-9] `TestRolloutRiskFeatures` asserts the changed types, the high-risk flag, the engine count
      and the Jaccard ranking of history. `TestRolloutRiskAgent` (storetest, fake provider) assesses a
      new canary rollout, rejects a score 8 labelled `low` and a cited version not in history, skips
      rollback rollouts, and creates an `updateEngineGroup` proposal for a medium risk.
      `TestRolloutNotDelayedByAI` (e2e) sets a 120 s fixture delay, updates a policy group, and asserts
      the API answers within 2 s and the rollout completes before the assessment. Fails if AI delays a
      mutation or rollout, or an invalid assessment is stored.
- [ ] [S-10] `TestThreatCheck` (unit) asserts:
  - cached verdicts skip the model;
  - 100 names are accepted and 101 rejected;
  - query counts and block reason come from the query log.

  `TestListClassificationSampling` asserts 200 uniformly sampled names from a 10,000-name blob,
  batches of 50, estimated counts equal to share × entry_count, and no re-run for an unchanged blob.
  `TestAIThreatLabelsInQueryLog` (e2e) runs a threat check for a queried name, then `searchQueryLog`
  returns that record with `threat.is_threat=true` and the query-log request makes no fixture call.
  Fails if the read path calls the model or sampling is biased.

- [ ] [S-11] `TestCapacityProjection` asserts:
  - growth of 1,200 entries/day against a 22.7M cap from 18.5M gives an exhaustion date within
    ±1 day of 3,500 days;
  - negative growth gives no date;
  - fewer than 3 points gives `insufficient_data`;
  - confidence is capped at 0.5 below 7 points.

  `TestCapacityForecastAgent` (storetest, fake provider) writes daily samples idempotently per day,
  stores forecasts for every resource with data, and a `cache_max_bytes` proposal only for `cache`.
  Fails if a limitless resource gets an exhaustion date or samples duplicate.

- [ ] [S-12] `TestRpzSuggestionValidation` rejects `*.com`, a name under a hosted zone, a DoH upstream
      host, the public URL host, an allowlisted name and an invalid name. It accepts
      `c2.evil.example`. `TestRpzZoneContent` renders applied plus selected rules, and the result
      passes Nexora's RPZ file validation with one comment per rule. `TestAIRpzSuggestionsApply` (e2e,
      real engine):
  - applying two rule proposals creates `ai-suggested.rpz`, and the engine answers NXDOMAIN for both
    names;
  - `ai_rpz_rules` holds both;
  - a later apply keeps them and adds a third;
  - deleting the zone and applying again recreates all three.

  Fails if generated content is invalid or earlier rules are lost.

- [ ] [S-13] `TestMCPServerWithGoAISDKClient` (e2e) uses the go-ai-sdk `mcp` client over Streamable
      HTTP against mgmt with `NEXORA_MCP_ENABLED=true`. It asserts:
  - `initialize` negotiates `2025-06-18`;
  - with read-only on, a viewer token lists `nexora_policy_groups_list` but no `_create` tool, and
    calling `nexora_policy_groups_list` returns the groups;
  - with read-only off, an operator token's `nexora_policy_groups_create` creates a group, with an
    `audit_log` row whose actor is the token, while a viewer token's call returns `isError` with
    `forbidden`;
  - `resources/read nexora://fleet/status` works, and `prompts/get nexora_status` returns a message;
  - no token gives 401 and a foreign `Origin` gives 403;
  - AI unconfigured does not disable MCP.

  `TestMCPStdioBridge` runs `nexora-mgmt mcp-stdio` through `mcp.NewStdioTransport` and lists tools.
  Fails if MCP bypasses RBAC or audit, or read-only lists write tools.

- [ ] [S-14] Playwright screen specs (run by `TestGUICoverage`, AI configured against the scripted
      fixture):
  - `50-ai-status.spec.ts`: status, budget, agents, Run now;
  - `51-ai-insights.spec.ts`: findings and insights, acknowledge and dismiss, Dashboard card;
  - `52-ai-querylog.spec.ts`: Ask box to filtered URL and summary, anomaly banner, threat badge;
  - `53-ai-recommendations.spec.ts`: diff view, apply dialog listing actions, apply, dismiss;
  - `54-ai-assistant.spec.ts`: message, thinking state, plan diff, apply;
  - `55-ai-forecasts.spec.ts`: upstream and capacity cards, Forwarding & recursion panel;
  - `56-ai-rollout-risk.spec.ts`: risk card on a canary rollout;
  - `57-ai-threat.spec.ts`: Check domains dialog, list classification breakdown;
  - `58-ai-rpz-suggestions.spec.ts`: select two, apply, zone listed;
  - `59-ai-disabled.spec.ts`: status mocked off, no AI nav, no widgets, `/ai` shows "AI is off";
  - `60-ai-viewer.spec.ts`: a viewer sees insights and recommendations without apply or dismiss, and
    no Assistant.

  Each spec runs at desktop and 400 px width. `TestGUICoverage` passes with all 18 new operations
  covered. Fails if a page is missing, an operation is uncovered, or AI UI shows when off.

- [ ] [S-15] `TestHelmAISecretWiring` (`deploy/deploytest`) renders the chart:
  - with `mgmt.ai.existingSecret=nexora-ai`, three `secretKeyRef` env entries with `optional: true`
    and keys `base-url`, `model`, `api-key`;
  - without it, no `NEXORA_AI_` variable;
  - the MCP env from values;
  - no `NEXORA_AI_API_KEY` literal value anywhere under `deploy/`;
  - `NexoraAIAgentFailing` and `NexoraAIBudgetExhausted` in the PrometheusRule.

  Fails if the key could come from values or a file in the repo.

- [ ] [S-1] [S-3] [S-13] [S-15] `TestKwSmokeAI`, run by `scripts/kw-acceptance.sh` against the kw
      deployment with fastllm, asserts:
  - `getAiStatus` `enabled:true`, model `qwen3-6-35b-a3b` and a private base URL host;
  - `startAiQueryLogSearch` for "blocked queries in the last hour" ends `succeeded` within 300 s with
    valid filters;
  - `runAiAgent capacity_forecast` finishes `ok` or `no_change` within 600 s with input and
    reasoning tokens above 0 in `nexora_mgmt_ai_tokens_total`;
  - `nexora_mgmt_ai_requests_total{outcome="invalid_output"}` did not grow;
  - MCP `initialize` and `tools/list` succeed with a viewer API token.

  Fails if fastllm is not reachable with the Secret's settings or the structured output mode fails on
  the real model.

- [ ] [S-1] [S-2] [S-3] [S-4] [S-5] [S-6] [S-7] [S-8] [S-9] [S-10] [S-11] [S-12] [S-13] [S-14] [S-15]
      All of the following hold:
  - the full local suite passes (`make mgmt-test`, `make e2e` including `TestGUICoverage`, `make web-test`);
  - `scripts/kw-deploy.sh` finishes with zero lost probe queries on 192.168.10.136 and 192.168.10.139;
  - `scripts/kw-acceptance.sh` passes including `TestKwSmokeAI`;
  - `docs/operations.md` documents every M11 variable, metric, error code and the MCP client setup;
  - `docs/architecture.md` has the M11 section.

  Fails if a probe query is lost, a test fails, or a variable is undocumented.

## Open questions
