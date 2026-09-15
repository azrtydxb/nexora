# M11 T4: Scripted fake OpenAI-compatible fixture

Status: open
Created: 2026-09-15

## Description

Implements Task 4 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `e2e/fixtures/cmd/nexora-fixture/openai_test.go`. It starts the handler with
- [x] Run `scripts/dev-exec.sh 'go test ./e2e/fixtures/cmd/nexora-fixture -run TestOpenAIFixture -count=1'`
- [x] Implement `openai.go` (`runOpenAI(args) (stop func(), addrs string, err error)` with
- [x] Create `e2e/harness/openai_test.go` with `TestOpenAIFixtureHarness`:
- [ ] Report the paths. Commit message: `M11 T4: scripted fake OpenAI-compatible fixture`.

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./e2e/fixtures/cmd/nexora-fixture -run TestOpenAIFixture -count=1'`
  → `openai_test.go:14:28: undefined: newOpenAIHandler` (build failed).
- Green: same command with `-v` → `--- PASS: TestOpenAIFixture (0.02s)`, `ok .../nexora-fixture 0.032s`.
- Red: `scripts/dev-exec.sh 'go vet ./e2e/harness'` → `harness.New(t).StartOpenAIFixture undefined`.
- Green: fixture built into a private temp bin dir instead of `make e2e-build` (so the shared
  `/work/nexora/bin` binaries used by concurrent M6 agents are not overwritten, and the in-progress web
  tree is not rebuilt): `go build -o $D/nexora-fixture ./e2e/fixtures/cmd/nexora-fixture &&
NEXORA_E2E_BIN_DIR=$D go test ./e2e/harness -run TestOpenAIFixtureHarness -count=1 -v` →
  `--- PASS: TestOpenAIFixtureHarness (0.06s)`; temp dir removed.
- `scripts/dev-exec.sh 'gofmt -l e2e/harness e2e/fixtures; go vet ./e2e/harness ./e2e/fixtures/cmd/nexora-fixture && go test ./e2e/fixtures/cmd/nexora-fixture -count=1'`
  → no gofmt output, vet clean, `ok .../nexora-fixture 0.354s`.
- Commit pending (lead commits).
