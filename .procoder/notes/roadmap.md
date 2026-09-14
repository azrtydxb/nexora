# Nexora roadmap after v1 (agreed 2026-09-14)

Every open GitHub issue is either in a milestone below, labelled `pending-decision`, or closed.
Decisions and their answers are in `.procoder/ask/decisions.md`.

## Operating rules while the owner is away

- Order: M6 → M7 → M8 → M9 → M10 → M11. Within a milestone, parallel implementer agents, and the lead
  commits serially (`scripts/commit-paths.sh`).
- Every milestone: spec (`.procoder/specs/nexora-<milestone>.md`, checker COMPLETE), plan
  (`.procoder/plans/`), todo, implementation, full local e2e including `TestGUICoverage`, then deploy.
- **kw deploys** after each milestone once all tests pass:
  - `scripts/kw-deploy.sh`, with the DNS probe (5 queries/s each to 192.168.10.136 and 192.168.10.139).
  - Then `scripts/kw-acceptance.sh`.
  - **Roll back** (`helm rollback nexora`) on any lost query or failed acceptance test, and record why.
- **Never change** 192.168.10.136 or 192.168.10.139: the home LAN uses them via DHCP.
- Do not implement `pending-decision` issues.
- AI features are **suggest-only**. Nothing changes configuration until an operator reviews and applies
  it, and every apply is audited.
- **Secrets** stay in Kubernetes Secrets only, never in the repo:
  - `nexora-ai` in `nexora` and `nexora-dev`: fastllm base URL, model `qwen3-6-35b-a3b`, API key.
- Close each issue with a comment naming the commit and the proof (test names, kw measurements).

## Milestones

### M6 Operator UX (#54–#67)

- **Query log:**
  - #54: partial name match on both backends.
  - #56: multi-select filters.
  - #61: decision reason, covering list name, allowlist entry, policy group, RPZ and rewrite.
- **Dashboard (#55):** more metrics and graphs.
- **Navigation and text:**
  - #57: rename Upstreams to **Forwarding & recursion**.
  - #59: clearer page descriptions.
  - #60: collapsible Filtering menu with Blocklist / allowlist, Categories, Policies and **RPZ**.
  - #62: collapsible categories.
- **Help (#58):** tooltips on every setting, plus in-app help pages.
- **Access control (#63):**
  - Separate resolver access from authoritative access.
  - Per-zone allow-query.
  - Existing installs migrate without behaviour change.
- **Upstreams (#64):** `parallel` selection strategy with `parallel_max`.
- **Engine modal (#65):**
  - Metrics tab.
  - Logs tab, from a bounded in-memory log buffer on each engine streamed over the control connection.
  - Queries tab.
- **Account (#66):** self-service profile and password change.
- **Version (#67):** version and short hash in the sidebar, with a reload hint when the GUI is stale.

### M7 Hardening (#3, #5, #9–#29)

- **#3:** CI, images and fuzz already run on the ARC runners. Verify, fix what is red, and close.
  The perf A/A part moves to #6 (pending).
- **#5:** run the compose example on novanas (`ssh piwi@192.168.10.211`, Docker 29, Compose v5), and fix
  the compose files and docs.
- **#9–#29:** engine and management-plane tech debt as filed, including #21 BADVERS.

### M8 DNS protocols (#30–#33)

- **#30 mDNS gateway:**
  - Answer unicast DNS queries for `.local` from mDNS on the engine's segment.
  - Optional reflection across VLANs.
- **#31 ZONEMD (RFC 8976):** generate for primary zones, verify for secondary zones.
- **#32 ODoH (RFC 9230):** target and proxy roles.
- **#33 Catalog zones (RFC 9432):** producer and consumer.

### M9 Platform (#37, #41)

- **#37:** Kubernetes operator with CRDs for installations (mgmt + engines) and engine groups. The Helm
  chart stays.
- **#41:** optional CNPG HA cluster and backups in the Helm chart, plus docs. No HA logic in the
  management plane.

### M10 Query-log backends (#35, #36)

- **#35 ClickHouse:** a backend deployed on kw (namespace `nexora`), plus local e2e.
- **#36 Loki:** a backend using the existing Loki in `monitoring`, plus local e2e.

### M11 AI (#42–#52)

- **Foundation (#42):** `go-ai-sdk` against fastllm, OpenAI-compatible and provider-agnostic, with a
  fake provider in tests. With no provider configured, AI is off and nothing else is affected.
- **Features (#43–#51):** suggest-only.
- **#52:** MCP server that uses the same RBAC as the API.

## Pending decisions (not implemented)

| Issue          | Blocker                                        |
| -------------- | ---------------------------------------------- |
| #2, #6, #7, #8 | Performance proof needs x86 reference hardware |
| #34            | DHCP server                                    |
| #38            | deb/rpm packages                               |
| #39            | Release tarballs                               |

## Closed as decided

- #4: obsolete, edge-b was removed.
- #40: Windows/macOS engine is out of scope.
