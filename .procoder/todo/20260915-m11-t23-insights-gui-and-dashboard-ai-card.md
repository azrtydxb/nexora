# M11 T23: Insights GUI and dashboard AI card

Status: done (not committed; the lead commits)
Created: 2026-09-15

## Description

Implements Task 23 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `e2e/gui_seed_ai_insights_test.go`: two open anomalies and two open insights inserted
      with `harness.PGExec`, `NEXORA_E2E_AI_ANOMALY` and three sibling vars exported.
- [x] Create `web/e2e/screens/51-ai-insights.spec.ts`. At 1280 and 400 px, as operator: the anomaly
      card, acknowledge + PATCH 200 + state badge, the insight tab with a cause, a dismissal that
      leaves the open list, the dashboard card's `ai-score` and top insight, and the link back.
- [x] Fill `web/src/pages/ai/AiInsightsPage.tsx` (kind tabs and status filter from `?kind=`/`?status=`,
      causes with confidence bars, anomaly clients/domains and the query-log link), create
      `web/src/components/ai/DashboardAiCard.tsx`, mount it on `DashboardPage` behind `useAiEnabled()`.
- [x] Run `scripts/dev-exec.sh 'make web-build e2e-build && go test ./e2e -run TestGUICoverage -count=1'`
      and expect spec 51 to PASS.
- [x] Report the paths. Commit message: `M11 T23: AI insights GUI and dashboard card`.

## Evidence

- `cd web && pnpm run typecheck` — clean.
- `cd web && pnpm run lint` — `permission parity: 138 operations match`,
  `check-help: 165 controls on 46 pages have help`.
- `gofmt -l e2e/gui_seed_ai_insights_test.go` — no output; `go vet ./e2e/` — clean.
- `scripts/dev-exec.sh 'make web-build e2e-build && go test ./e2e -run TestGUICoverage -count=1 -timeout 40m'`:
  - `✓ 44 e2e/screens/51-ai-insights.spec.ts:6:3 › AI insights at 1280px (4.6s)`
  - `✓ 45 e2e/screens/51-ai-insights.spec.ts:6:3 › AI insights at 400px (4.0s)`
  - `✓ 48 e2e/screens/59-ai-disabled.spec.ts:3:1 › AI off hides every AI surface (4.4s)` (asserts the
    dashboard card is absent while AI is off)
  - `TestGUICoverage` itself still FAILs on work of other wave-4 tasks: `34-version`,
    `52-ai-querylog` at 400px (Task 24) and `60-ai-viewer` on `/ai/recommendations`, whose page is
    Task 25. No failure is in this task's surfaces.
