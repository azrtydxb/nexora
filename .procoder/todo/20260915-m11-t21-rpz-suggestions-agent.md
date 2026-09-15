# M11 T21: RPZ suggestions agent

Status: done (not committed — the lead commits)
Created: 2026-09-15

## Description

Implements Task 21 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `inputs_test.go` with `TestCollectRpzInputs`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/rpzsuggest -run TestCollectRpzInputs -count=1'`,
- [x] Create `agent_test.go` with `TestRpzSuggestionAgent`. The fake answers rules for `bad.ars.test`
- [x] Add the registration file. Create `e2e/ai_rpz_test.go` with `TestAIRpzSuggestionsApply`, following
- [x] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e -run TestAIRpzSuggestionsApply -count=1 -v'` and expect PASS.
- [x] Report the paths. Commit message: `M11 T21: RPZ rule suggestions`.

## Evidence

Paths created: `mgmt/internal/ai/rpzsuggest/inputs.go`, `inputs_test.go`, `agent.go`, `agent_test.go`,
`mgmt/cmd/nexora-mgmt/ai_rpzsuggest.go`, `e2e/ai_rpz_test.go`. Nothing else was modified or deleted.

Red/green (TDD):

- `go test ./mgmt/internal/ai/rpzsuggest -run TestCollectRpzInputs -count=1` failed on the missing family
  input (`family input = {Kind: Name: ...}`) before the detector was right, then `ok`.
- `go test ./mgmt/internal/ai/rpzsuggest -run TestRpzSuggestionAgent -count=1` failed with
  `undefined: rpzsuggest.Agent` before `agent.go` existed, then `ok`.

Green, shared pod (`scripts/dev-exec.sh`):

```
go test ./mgmt/internal/ai/rpzsuggest -run TestCollectRpzInputs -count=1
ok  	github.com/piwi3910/nexora/mgmt/internal/ai/rpzsuggest	4.706s
go test ./mgmt/internal/ai/rpzsuggest -run TestRpzSuggestionAgent -count=1
ok  	github.com/piwi3910/nexora/mgmt/internal/ai/rpzsuggest	8.718s
```

Green, private tree in the pod (`/work/t21-priv` = committed HEAD + this task's files; the shared
checkout carries other agents' work in progress and did not compile, and `make e2e-build` would
overwrite the shared `bin/` and `target/release`, so the engine binary was copied from the shared
build and only `nexora-mgmt` and `nexora-fixture` were built into the private `bin/`):

```
NEXORA_E2E_BIN_DIR=/work/t21-priv/bin go test ./e2e -run TestAIRpzSuggestionsApply -count=1 -v -timeout 20m
--- PASS: TestAIRpzSuggestionsApply (14.58s)
ok  	github.com/piwi3910/nexora/e2e	14.598s

go test -count=1 ./mgmt/internal/ai/...   # every AI package, all ok
gofmt -l mgmt e2e  -> clean; go vet ./mgmt/... ./e2e/...  -> clean
```

Dependency: `Collect` reads `ai_domain_verdicts`, created by Task 19's migration
`mgmt/migrations/01207_ai_threat.sql`. While Task 19 was still running the private tree carried a copy
of that migration; it has since landed in the checkout byte-identical, and the shared-pod runs above
used it.
