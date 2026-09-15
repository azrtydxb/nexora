# M11 T1: M11 architecture text

Status: open
Created: 2026-09-15

## Description

Implements Task 1 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Run `grep -n "## AI (M11)" docs/architecture.md` and expect no match.
- [x] Add to the repository layout block, after `internal/fleet`:
- [x] Append the section `## AI (M11)`, one paragraph each, restating the spec decisions:
- [x] Run `scripts/pc-format.sh docs/architecture.md` and expect no diff output.
- [ ] Report the paths. Commit message: `M11 T1: AI architecture`.

## Evidence

- `grep -n "## AI (M11)" docs/architecture.md` before editing: no output, exit 1.
- Layout block (`docs/architecture.md`): `internal/ai`, `internal/ai/{proposal,finding,forecast}`,
  `internal/ai/{qlsearch,…,rpzsuggest}` and `internal/mcpserver` added after `internal/fleet`;
  `api/embed.go` (package apispec) added under `mgmt/` next to `api/openapi.yaml` (plan text updated).
- Management plane bullet: every M11 AI and MCP variable with its default.
- `## AI (M11)` appended (grep now matches line 736): intro (suggest-only, no proto field, no engine
  code), Enablement, Provider, Generation, Bounds, Scheduler, Tasks, Proposals, Findings and forecasts,
  MCP, Metrics and retention, Configuration.
- `scripts/pc-format.sh docs/architecture.md`, then `diff` against a pre-format copy: no output, exit 0
  (launcher reports "already formatted").
- Commit pending: paths reported to the lead (implementers do not commit).
