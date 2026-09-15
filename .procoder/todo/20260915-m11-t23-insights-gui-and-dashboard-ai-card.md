# M11 T23: Insights GUI and dashboard AI card

Status: open
Created: 2026-09-15

## Description

Implements Task 23 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create `e2e/gui_seed_ai_insights_test.go`:
- [ ] Create `web/e2e/screens/51-ai-insights.spec.ts`. At 1280 and 400 px, as operator:
- [ ] Run `scripts/dev-exec.sh 'make web-build e2e-build && go test ./e2e -run TestGUICoverage -count=1'` and
- [ ] Report the paths. Commit message: `M11 T23: AI insights GUI and dashboard card`.

## Evidence

