# M11 T30: RPZ suggestions GUI

Status: open
Created: 2026-09-15

## Description

Implements Task 30 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `e2e/gui_seed_ai_rpz_test.go`, inserting three open `rpz_suggestions` proposals for
- [x] Create `web/e2e/screens/58-ai-rpz-suggestions.spec.ts`. As operator at 1280 px:
- [x] Run the Task 24 commands. Expect FAIL, implement, and expect specs 58 and 15 to PASS.
- [ ] Report the paths. Commit message: `M11 T30: RPZ suggestions GUI`.

## Evidence

Built and tested in a private copy of the tree in the `toolbox` dev pod (`/work/t30`, its own
`bin/`, the engine binary copied from `/work/nexora/bin`), deleted afterwards.

- Laptop: `cd web && pnpm run typecheck && pnpm run lint` passed:
  `permission parity: 138 operations match`, `check-help: 171 controls on 49 pages have help`.
  `gofmt -l e2e/gui_seed_ai_rpz_test.go` printed nothing; `go vet ./e2e` was clean.
- RED, with `RpzSuggestionsTab` stubbed to `return null`: running
  `make web-build && go build -o bin/nexora-mgmt ./mgmt/cmd/nexora-mgmt && NEXORA_E2E_BIN_DIR=/work/t30/bin go test ./e2e -run TestGUICoverage -count=1`
  failed 58 at 1280px with `getByTestId('ai-rpz-row-c2.gui-rpz.test') … element(s) not found` and at 400px
  with `getByTestId('ai-rpz-zone-notice') … element(s) not found`.
- The first implementation run failed 58 at 1280px with `ai-apply-result` count 0. The apply
  refetched the open list, which emptied the selection and closed the dialog. The fix makes the
  dialogs keep the ids they opened with.
- GREEN, same command: `✓ 16 15-rpz … operator manages RPZ zones`, `✓ 17 15-rpz … viewer`,
  `✓ 58 58-ai-rpz-suggestions … at 1280px`, `✓ 59 … at 400px`, with 60 passed and 2 failed. Both
  failures are in `57-ai-threat.spec.ts`, which is Task 29's work and still in progress in parallel.
  Because of them, the `TestGUICoverage` run as a whole reports FAIL.
