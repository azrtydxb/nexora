# M11 T25: Recommendations GUI

Status: open
Created: 2026-09-15

## Description

Implements Task 25 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `e2e/gui_seed_ai_recommendations_test.go`. With `harness.PGExec` it inserts two open
      proposals (a `filter_recommendations` `updateFilterCategory` on `key=malware` at the live
      revision, an `upstream_prediction` `updateResolverSettings` built from `GET /resolver-settings`
      with `strategy` `fastest`) and sets `NEXORA_E2E_AI_PROPOSAL_APPLY` /
      `NEXORA_E2E_AI_PROPOSAL_DISMISS`. A third `capacity_forecast` proposal stays open so
      `60-ai-viewer.spec.ts`, which runs after 53, still finds an open proposal.
- [x] Create `web/e2e/screens/53-ai-recommendations.spec.ts`. The writes run once at 1280 px (list,
      details with `json-diff`, apply with `ai-apply-action` + license checkbox, `ai-apply-result`
      "applied", the card leaves the open list, dismiss with reason "not now", the dismissed list
      keeps the reason). A second test at 400 px reads `?status=all`, expands the details and checks
      the source tabs and the RPZ link.
- [x] Run the typecheck, lint and `TestGUICoverage` commands of Task 24. Expect FAIL, implement the
      page, and expect spec 53 to PASS.
- [ ] Report the paths. Commit message: `M11 T25: AI recommendations GUI`.

## Evidence

Laptop (`cd web`):

- `pnpm run typecheck` — clean (`tsc -b --noEmit`).
- `pnpm run lint` — `permission parity: 138 operations match`,
  `check-help: 167 controls on 46 pages have help`.
- `pnpm exec prettier --check` on the page and the spec, `gofmt -l e2e` and `go vet ./e2e` — clean.

Dev pod (`toolbox`), in a private copy `/work/t25` of the tree with its own `bin/`, deleted
afterwards; `go test ./e2e -run TestGUICoverage -count=1`:

- RED (page still the Task 11 stub): both 53 tests fail —
  `Error: expect(locator).toBeVisible() failed / Locator: getByTestId('ai-proposal-6f4b25d8-…') /
element(s) not found` at `53-ai-recommendations.spec.ts:22`. `60-ai-viewer` also failed (no
  proposal rendered), which is why the seed adds the third open proposal.
- GREEN (page implemented): `✓ 48 53-ai-recommendations.spec.ts:5:1 › AI recommendations at 1280px`,
  `✓ 49 … :69:1 › AI recommendations at 400px`, and `60-ai-viewer.spec.ts` passes at both widths
  (`52 passed`, 4 failed).

`TestGUICoverage` as a whole still FAILS in this shared snapshot for work that is not this task:
`34-version`, `54-ai-assistant` and `55-ai-forecasts` (Tasks 26 and 27, in flight in parallel), and
the OpenAPI coverage check cannot pass until those tasks land. The private tree and its logs were
deleted from the pod afterwards.
