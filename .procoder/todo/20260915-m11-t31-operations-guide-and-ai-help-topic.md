# M11 T31: Operations guide and AI help topic

Status: open
Created: 2026-09-15

## Description

Implements Task 31 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create `deploy/deploytest/ai_docs_test.go` with `TestOperationsDocumentsAI`. It reads
- [ ] Run `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestOperationsDocumentsAI -count=1'` and
- [ ] Write `## AI` in `docs/operations.md` with the subsections:
- [ ] Report the paths. Commit message: `M11 T31: AI operations guide and help topic`.

## Evidence

