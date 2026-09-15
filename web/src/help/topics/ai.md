<!-- operations: AI -->

# AI

The AI features suggest; they never change configuration on their own. Every suggestion waits for an
operator to apply it, and the apply replays the normal API with that operator's credentials and is
audited like any other change. Query data goes only to the endpoint your operator configured, which
must be on a private network unless that guard was deliberately lifted. Setup, privacy, bounds,
metrics and MCP clients are described in the `AI` section of `docs/operations.md`.

## Enabling AI

AI is off until an operator configures an endpoint and a model on the management plane. While it is
off the AI pages explain that and every AI action is unavailable; nothing else about Nexora changes.
The status page shows whether AI is on, what each background agent last did, when it runs next and how
much of today's token budget is spent. An operator can ask an agent to run now instead of waiting for
its interval.

Work that needs the model runs in the background: the page asks, then polls for the answer, which can
take from a few seconds to two minutes. A request can come back busy when too many run at once, or out
of budget when the day's tokens are spent; both are temporary.

## Insights

Two agents watch the fleet. The query-log agent reads the newest records and reports anomalies:
sudden blocked-query spikes, new domains a client never asked for before, failure bursts. The
dashboard agent correlates the dashboard, the fleet summary and the health alerts into insights that
span engines. Each finding names what it saw, the likely cause and what to check.

Findings stay open until you acknowledge or dismiss them; dismissed findings leave the dashboard.
Closed findings are deleted after 30 days.

## Recommendations

Proposals are concrete configuration changes: the filter, upstream, capacity, rollout-risk and RPZ
agents write them, and so does the assistant. Each proposal shows its rationale, its expected impact
and the exact API actions it would perform, with a diff of the resulting configuration.

Select proposals and apply them together (up to 100), or dismiss them with a reason so other operators
see why. An apply reports each action's result separately, so a partly failing proposal tells you
which action failed and why. A proposal whose target changed since it was written turns stale and must
be regenerated, and a newer proposal for the same target supersedes the older one. Enabling a category
whose source list is licensed for non-commercial use only requires the licence acknowledgement.

## Assistant

The assistant answers questions about this installation's configuration in plain language and can turn
a request into proposals you review on the Recommendations page. It reads; it never writes. It is open
to operators, keeps each conversation for 30 days of inactivity, and cites the settings it looked at.

## Forecasts

Two agents project ahead. The capacity forecast uses daily samples of query volume, cache behaviour
and engine load to say when the fleet outgrows its current size. The upstream prediction uses the
health history of each upstream resolver to flag one that is degrading before it fails, and shows its
outlook on the Upstreams page. A forecast is kept until it expires, then for 7 more days.

## Rollout risk

Before a staged rollout goes out, the rollout-risk agent reviews its configuration diff and the state
of the fleet and rates the risk, naming what could break and which wave would show it first. The
rating is advice on a rollout you still start, pause or halt yourself.

## Threat checks

Ask about any domain and the model classifies it — malware, phishing, advertising, tracking, benign —
with its reasoning and a confidence. Verdicts are cached until they expire, so repeating a check is
free. Query-log rows carry the badge for domains already classified. The same agent samples names from
each filter list to describe what the list actually blocks.

## RPZ suggestions

The RPZ agent proposes response-policy rules from query-log patterns and threat verdicts: domains
worth blocking, redirecting or passing through. Applied rules are written to the zone
`ai-suggested.rpz`, which the apply creates when it is missing. Keep hand-written rules in another
zone, because an apply rewrites that zone from the recorded rules.

## MCP

When your operator enables it, Nexora serves the Model Context Protocol, so an AI client such as
Claude Desktop can read the query log, lists, policy groups, engines, zones, upstreams, rollouts and
the AI findings and proposals through your own API token. Tools act with the role of that token and
are audited exactly like the API, and the endpoint is read-only unless an operator opened it. Use a
viewer token. The client setup, both over HTTP and through the `nexora-mgmt mcp-stdio` bridge, is in
the `AI` section of `docs/operations.md`.
