# M6 T3: GUI e2e seed registry and wider spec glob

Status: open
Created: 2026-09-14

## Description

Implements Task 3 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `e2e/gui_seed_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./e2e -run TestGUISeedRegistryRunsInOrder -count=1'` and expect
- [x] Add to `e2e/gui_seed_test.go`:
- [x] In `e2e/gui_test.go` `TestGUICoverage`:
- [x] Run
- [x] Report the paths. Commit message: `e2e: GUI seed registry and screen spec glob 00-99`.

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./e2e -run TestGUISeedRegistryRunsInOrder -count=1'` ->
  `e2e/gui_seed_test.go:33:2: undefined: runGUISeeds` / `FAIL github.com/piwi3910/nexora/e2e [build failed]`.
- Green: `scripts/dev-exec.sh 'go vet ./e2e && go test ./e2e -run TestGUISeedRegistryRunsInOrder -count=1 -v'` ->
  `--- PASS: TestGUISeedRegistryRunsInOrder (0.00s)` / `ok github.com/piwi3910/nexora/e2e 0.014s`.
- `gofmt -l e2e/gui_test.go e2e/gui_seed_test.go` -> no output.
- `e2e/gui_test.go`: `runGUISeeds(...)` inserted before `time.Sleep(12 * time.Second)`; glob is now
  `web/e2e/screens/[0-9][0-9]-*.spec.ts` (no spec numbered 30+ exists yet, so the current run set is unchanged).
- Commit step: paths reported to the lead (implementers do not commit).

