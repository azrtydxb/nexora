# M11 T3: AI service foundation

Status: done (verified, awaiting lead commit)
Created: 2026-09-15

## Description

Implements Task 3 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/config/ai_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/config -run TestLoadAIConfig -count=1'` and expect
- [x] Implement `mgmt/internal/config/ai.go` (`loadAI(getenv) (AIConfig, error)`), called from `Load`.
- [x] Add the dependency on the laptop (`go get github.com/azrtydxb/go-ai-sdk@v0.4.1 && go mod tidy`) and
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/ai -run TestEndpointPrivacyGuard -count=1'` and
- [x] Implement `privacy.go`:
- [x] Create `mgmt/internal/ai/generate_test.go` using `storetest.New`. It includes this test:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/... -run TestGenerateValidatesAndRetries -count=1'`
- [x] Implement `generate.go`, `service.go`, `metrics.go`, `provider.go` and `aifake`.
- [x] Create `mgmt/internal/ai/limits_test.go` with `TestServiceBounds`, following the spec criterion
- [x] Run `scripts/dev-exec.sh 'gofmt -l mgmt && go vet ./mgmt/... && go test ./mgmt/internal/ai/... ./mgmt/internal/config/... -count=1'`
- [x] Report the paths. Commit message: `M11 T3: AI service foundation on go-ai-sdk`.

## Evidence

- `scripts/dev-exec.sh 'go test ./mgmt/internal/config -run TestLoadAIConfig -count=1'` before implementing:
  FAIL `c.AI undefined (type Config has no field or method AI)`; after `ai.go`: `ok .../mgmt/internal/config`.
- `go get github.com/azrtydxb/go-ai-sdk@v0.4.1 && go mod tidy` on the laptop: go.mod/go.sum gain only go-ai-sdk
  (it has no dependencies of its own).
- `scripts/dev-exec.sh 'go test ./mgmt/internal/ai -run TestEndpointPrivacyGuard -count=1'`: FAIL `undefined:
  ErrProvider`/`CheckEndpoint`, then `ok .../mgmt/internal/ai`.
- `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/... -run TestGenerateValidatesAndRetries -count=1'`: FAIL
  `no required module provides package .../mgmt/internal/ai/aifake` (Generate/aifake missing), then
  `ok .../mgmt/internal/ai 2.916s`.
- `TestServiceBounds` passed on first run because `limits.go` was written with `service.go`; mutation check
  instead: semaphore capacity 100 → `limits_test.go:72: in flight max 6, gauge max 6, want 2`; background
  budget check removed → `limits_test.go:112: background at 85%: <nil>`; restored → `ok`.
- `scripts/dev-exec.sh 'gofmt -l mgmt && go vet ./mgmt/... && go test ./mgmt/internal/ai/... ./mgmt/internal/config/... -count=1'`:
  no gofmt output, vet clean, `ok .../mgmt/internal/ai 7.384s`, `ok .../mgmt/internal/config 0.006s`.
- `scripts/dev-exec.sh 'go test ./mgmt/internal/store/ -count=1'` (migrations incl. 01200): `ok 24.310s`.
- Plan text of Task 3 updated to what was built (`Options.SlotWait`, `aifake.Config`, capture model, test
  additions, config test CA variables).

