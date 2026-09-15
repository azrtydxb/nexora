# M11 T24: Query log GUI: Ask box, anomaly banner, threat badge

Status: implemented, not committed (the lead commits)
Created: 2026-09-15

## Description

Implements Task 24 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `e2e/gui_seed_ai_querylog_test.go`. Its seed: one lookup of `threat.aiq-gui.test`, the
      cached malware verdict inserted with `harness.PGExec`, the `querylog_search` script (translation
      then summary, the pair twice because the spec asks once per viewport width) and
      `NEXORA_E2E_AI_THREAT_NAME`.
- [x] Create `web/e2e/screens/52-ai-querylog.spec.ts`. At 1280 and 400 px, as viewer: the anomaly
      banner, the Ask box, the succeeded task, the summary and explanation, `name=threat.aiq-gui` in
      the URL and the `querylog-threat` badge on the row.
- [x] Implement the components and the page mount, keeping every M6 query-log test id. Run the same
      commands: specs 10, 23, 24, 25 and 52 pass; 26 fails for a reason that predates this task
      (proved by a control run, see Evidence).
- [ ] Report the paths. Commit message: `M11 T24: query log AI search, anomaly banner and threat badge`.

## Evidence

Task 19 (`ai_domain_verdicts`, the `threat` fill in `querylog_resolve.go`) is not committed, so every
GUI-coverage run used a private copy of the tree in the dev pod (`/work/t24`, its own `bin/`, no
cargo build: the committed `nexora-engine` was reused) carrying a throwaway stand-in of those two
pieces. The copy was deleted afterwards (`rm -rf /work/t24`).

- `cd web && pnpm run typecheck && pnpm run lint` → clean:
  `permission parity: 138 operations match`, `check-help: 165 controls on 46 pages have help`.
- Red (private copy with the three components removed and `QueryLogPage.tsx`/`ai.ts` at HEAD):
  `go test ./e2e -run TestGUICoverage -count=1` → `6 failed, 44 passed (4.2m)`, with
  `✘ 52-ai-querylog … at 1280px` and `… at 400px` both failing on
  `expect(getByTestId('querylog-ai-anomalies')).toBeVisible() … element(s) not found`.
- Green: `go test ./e2e -run TestGUICoverage -count=1` → `5 failed, 45 passed (3.8m)`, with
  `✓ 10-query-log`, `✓ 23-querylog-category`, `✓ 24-querylog-refresh`, `✓ 25-querylog-filters`,
  `✓ 52-ai-querylog … at 1280px`, `✓ 52-ai-querylog … at 400px`.
  The Go test still exits 1: the coverage assertion lists the AI operations of Tasks 25-30 as
  uncovered.
- Control run (same tree, this task's seed and spec moved aside): `4 failed, 44 passed (3.6m)` —
  `26-querylog-reason` and `34-version` fail without this task's files too, so they are pre-existing.
  `26` fails with "No queries match these filters." under `source=allowlist`: the allowlisted seed
  record is missing from the query log, a seed race outside this task. `60-ai-viewer` (Task 22) and
  `51-ai-insights` (Task 23) were in flight in the shared tree during these runs.

Paths: `web/src/components/ai/QueryLogAsk.tsx`, `web/src/components/ai/AnomalyBanner.tsx`,
`web/src/components/ai/ThreatBadge.tsx` (created); `web/src/pages/QueryLogPage.tsx`,
`web/src/help/catalog/ai.ts`, `.procoder/plans/nexora-m11-ai.md` (modified);
`e2e/gui_seed_ai_querylog_test.go`, `web/e2e/screens/52-ai-querylog.spec.ts` (created).
