# M11 T10: MCP server and stdio bridge

Status: done
Created: 2026-09-15

## Description

Implements Task 10 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/mcpserver/tools_test.go` with `TestToolsMapToOperations`:
- [x] Create `mgmt/internal/mcpserver/server_test.go` with `TestServerProtocol`, using a fake `Replayer`:
- [x] Implement `mcp_replay.go`, the `mcp-stdio` subcommand (flags `--url`, `--token-file`, `--ca-file`),
- [x] Create `e2e/mcp_test.go` with `TestMCPServerWithGoAISDKClient` and `TestMCPStdioBridge` exactly as
- [x] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e -run "TestMCP" -count=1 -v'` and expect PASS.
- [x] Report the paths. Commit message: `M11 T10: MCP server with API RBAC and stdio bridge`.

## Evidence

Implemented on 2026-09-15 (not committed; the lead commits).

Paths created: `mgmt/internal/mcpserver/{server.go,tools.go,resources.go,prompts.go,stdio.go,server_test.go,tools_test.go}`,
`mgmt/internal/api/mcp_replay.go`, `e2e/mcp_test.go`.
Paths modified: `mgmt/internal/api/server.go` (NewHandler delegates to NewHandlerWithReplayer),
`mgmt/cmd/nexora-mgmt/main.go` (the `mcp-stdio` subcommand and the `/mcp` mount),
`.procoder/plans/nexora-m11-ai.md` (Task 10 text).

Evidence (all in the kw dev pod through `scripts/dev-exec.sh`):

- `go test ./mgmt/internal/mcpserver -count=1` before `tools.go`: FAIL `undefined: Tools`,
  `undefined: inputSchema`; after: `ok`.
- `go test ./mgmt/internal/mcpserver -count=1` before `server.go`: FAIL `undefined: New`,
  `undefined: Options`, `undefined: maxResultBytes`; after: `ok  ... 6.981s`.
- Mutation checks on `TestServerProtocol`: dropping the Origin check → `foreign Origin = 200, want 403`;
  dropping the read-only filter in `callable` → `read-only lists nexora_zone_records_create (POST)`;
  dropping the read-only refusal in `tools/call` → `read-only create = ... want isError read_only`;
  dropping `auth.Authorize` → `viewer lists nexora_policy_groups_create (role operator)`;
  disabling truncation → `2 MiB result: ... 2097154 bytes`.
- `go build ./mgmt/... && go vet ./mgmt/... ./e2e/`: success.
- `go build -o $B/nexora-mgmt ./mgmt/cmd/nexora-mgmt && NEXORA_E2E_BIN_DIR=$B go test ./e2e -run "TestMCP" -count=1 -v`
  (private bin dir, deleted afterwards): `--- PASS: TestMCPServerWithGoAISDKClient (7.35s)`,
  `--- PASS: TestMCPStdioBridge (6.24s)`, `ok ... 13.599s`.
- `go test ./mgmt/internal/api ./mgmt/internal/mcpserver ./mgmt/cmd/... -count=1`:
  `ok ... api 101.138s`, `ok ... mcpserver 3.260s`.
- `gofmt -l mgmt e2e`: empty.

