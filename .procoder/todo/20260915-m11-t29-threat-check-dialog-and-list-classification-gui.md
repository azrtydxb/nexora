# M11 T29: Threat check dialog and list classification GUI

Status: open
Created: 2026-09-15

## Description

Implements Task 29 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `e2e/gui_seed_ai_threat_test.go`:
- [x] Create `web/e2e/screens/57-ai-threat.spec.ts`. At 1280 and 400 px as viewer:
- [x] Run the Task 24 commands. Expect FAIL, implement, and expect specs 57 and 04 to PASS.
- [ ] Report the paths. Commit message: `M11 T29: threat check and list classification GUI`.

## Evidence

- Laptop: `cd web && pnpm run typecheck && pnpm run lint` — clean
  (`permission parity: 138 operations match`, `check-help: 171 controls on 49 pages have help`).
- All pod runs used a private tree `/work/t29` (HEAD via `git archive` plus this task's files, its
  own `bin/`), deleted afterwards: `make web-build e2e-build`, then
  `NEXORA_E2E_BIN_DIR=/work/t29/bin go test ./e2e -run '^TestGUICoverage$' -count=1`.
- RED (seed and spec only): `✘ 57-ai-threat … at 1280px` and `at 400px`, waiting for
  `getByTestId('ai-threat-open')`; spec 04 passed.
- First implementation run: 57 passed, 04 failed — the Details card crashed with
  `Cannot read properties of null (reading 'reduce')`: `getAiFilterListClassification` returns
  `breakdown: null` for a never-classified list (Task 19 handler). Fixed in `ListClassification`
  with a `debt:` fallback. (51-ai-insights at 1280px also failed once; it passed on the rerun.)
- GREEN: `✓ 04-filtering.spec.ts › filter lists and allowlist`,
  `✓ 57-ai-threat.spec.ts … at 1280px`, `✓ … at 400px`; `52 passed, 2 failed`. The two failures are
  `60-ai-viewer.spec.ts` at both widths (`/ai/assistant` does not redirect to `/ai`), which also fail
  in RED: that is Task 26's uncommitted work, not in HEAD. The coverage assertion is not reached while
  Playwright fails; it also needs Tasks 26-30's specs.
