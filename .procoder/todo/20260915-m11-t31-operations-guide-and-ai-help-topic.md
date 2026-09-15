# M11 T31: Operations guide and AI help topic

Status: done (not committed; the lead commits)
Created: 2026-09-15

## Description

Implements Task 31 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `deploy/deploytest/ai_docs_test.go` with `TestOperationsDocumentsAI`. It reads
      `docs/operations.md` and asserts the `## AI` heading plus every M11 environment variable,
      metric, error code (`ai_disabled`, `ai_busy`, `ai_budget_exhausted`, `feature_disabled`,
      `proposal_not_open`, `unknown_agent`, `endpoint_not_private`), `nexora-mgmt mcp-stdio` and
      `ai-suggested.rpz`.
- [x] Run `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestOperationsDocumentsAI -count=1'` and
      see it fail first (`docs/operations.md lacks heading "## AI"`), then pass.
- [x] Write `## AI` in `docs/operations.md` with the subsections: Enabling AI (Secret `nexora-ai`
      created from a prompt, never from a file), Privacy guard, Structured output, Limits and budget,
      Agents (table with interval variable, default, inputs, outputs), Proposals and apply, Interactive
      features, Metrics and alerts, MCP (Claude Desktop JSON with `nexora-mgmt mcp-stdio` and a
      Streamable HTTP example with a viewer token), Troubleshooting (every error code) and Ceilings and
      retention.
- [x] Mirror the short version in `web/src/help/topics/ai.md` under the Task 11 headings, keeping the
      `<!-- operations: AI -->` reference and the anchors the help catalogue uses (`insights`,
      `recommendations`, `assistant`, `forecasts`, `threat-checks`, `rpz-suggestions`).
- [ ] Report the paths. Commit message: `M11 T31: AI operations guide and help topic`. (Reported; the
      lead commits.)

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestOperationsDocumentsAI -count=1'`
  → `--- FAIL: TestOperationsDocumentsAI (0.00s) ai_docs_test.go:51: docs/operations.md lacks heading "## AI"`.
- Green: `scripts/dev-exec.sh 'go test ./deploy/deploytest -run "TestOperationsDocumentsAI|TestOperationsDoc|TestHelpTopicsReferenceOperationsDoc" -count=1'`
  → `ok github.com/piwi3910/nexora/deploy/deploytest 0.019s` (this also clears the previously failing
  `TestHelpTopicsReferenceOperationsDoc`).
- Whole package: `scripts/dev-exec.sh 'go vet ./deploy/deploytest && go test ./deploy/deploytest -count=1'`
  → `ok github.com/piwi3910/nexora/deploy/deploytest 1.608s`.
- `cd web && pnpm run typecheck` → clean (`tsc -b --noEmit`, no output).
- `scripts/pc-format.sh docs/operations.md web/src/help/topics/ai.md deploy/deploytest/ai_docs_test.go`
  → all three report `already formatted`, no diff.
- Content checked against the code, not the spec alone: `mgmt/internal/config/ai.go` (defaults and
  agent intervals), `mgmt/internal/ai/privacy.go` (private-address ranges), `mgmt/internal/ai/prune.go`
  (retention), `mgmt/internal/ai/anomaly/detect.go` (5,000), `mgmt/internal/ai/filterrec/aggregate.go`
  and `mgmt/internal/ai/rpzsuggest/inputs.go` (50,000), `mgmt/internal/ai/threat/classify.go` (200),
  `mgmt/cmd/nexora-mgmt/main.go` (`mcp-stdio` flags), `mgmt/internal/mcpserver/server.go` (bearer auth,
  Origin check, no batches), `deploy/helm/nexora/templates/prometheusrule.yaml` (the two alerts).
