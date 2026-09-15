# M11 T8: Helm chart AI and MCP wiring, alerts

Status: open
Created: 2026-09-15

## Description

Implements Task 8 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `deploy/deploytest/ai_test.go` with `TestHelmAISecretWiring`, reusing the chart render helper
- [x] Run `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestHelmAISecretWiring -count=1'` and
- [x] Implement the values, schema, template (inside `{{- with $m.ai }}{{- if .existingSecret }}`) and
- [ ] Report the paths. Commit message: `M11 T8: Helm AI secret and MCP wiring, AI alerts`.

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestHelmAISecretWiring -count=1'` →
  `FAIL` with `mgmt env NEXORA_AI_BASE_URL = map[]` (all five env entries) and `alert NexoraAIAgentFailing missing`,
  `alert NexoraAIBudgetExhausted missing`.
- Green: `scripts/dev-exec.sh 'go test ./deploy/deploytest -count=1 -v'` → TestHelmAISecretWiring, TestHelmTemplate
  (includes `helm lint --strict`), TestDockerfilesStampBuildInfo, TestComposeExample, TestOperationsDoc,
  TestHelpTopicsReferenceOperationsDoc, TestImagesWorkflow all PASS; `ok github.com/piwi3910/nexora/deploy/deploytest 1.419s`.
- `go vet ./deploy/deploytest` and `gofmt -l deploy/deploytest` clean.
- Paths reported to the lead; commit pending (lead commits serially).
