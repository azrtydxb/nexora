# M11 T20: Capacity forecast agent

Status: open
Created: 2026-09-15

## Description

Implements Task 20 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `project_test.go` with `TestCapacityProjection`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/capacity -run TestCapacityProjection -count=1'`,
- [x] Create `agent_test.go` with `TestCapacityForecastAgent`:
- [x] Add the registration file. Run `scripts/dev-exec.sh 'gofmt -l mgmt && go vet ./mgmt/... && go test ./mgmt/internal/ai/capacity -count=1'` and expect PASS.
- [ ] Report the paths. Commit message: `M11 T20: capacity forecast agent`.

## Evidence

- Red (projection): `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/capacity -run TestCapacityProjection -count=1'`
  → `no non-test Go files in /work/nexora/mgmt/internal/ai/capacity`, `FAIL [build failed]`.
  After `project.go`: `ok github.com/piwi3910/nexora/mgmt/internal/ai/capacity 0.003s`.
- Red (agent): `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/capacity -count=1'` →
  `undefined: capacity.SampleToday` (x2), `undefined: capacity.Agent` (x2), `FAIL [build failed]`.
  After `agent.go` and `01205_ai_capacity_samples.sql`:
  `ok github.com/piwi3910/nexora/mgmt/internal/ai/capacity 14.204s`.
- Green with the registration file:
  `scripts/dev-exec.sh 'gofmt -l mgmt; go vet ./mgmt/... && go test ./mgmt/internal/ai/capacity -count=1'`
  → no gofmt output, vet clean, `ok github.com/piwi3910/nexora/mgmt/internal/ai/capacity 6.191s`.
- Playwright spec 50 (`50-ai-status.spec.ts`, the "Run now" step Task 11 left blocked on this agent):
  run in a private pod copy (`/work/t20-e2e`, own `bin`, shared `bin` and `target/release` untouched)
  with `NEXORA_E2E_BIN_DIR=/work/t20-e2e/bin go test ./e2e -run '^TestGUICoverage$' -count=1`.
  Result: `✓ 42 50-ai-status.spec.ts › AI status page at 1280px`, `✓ 43 … at 400px` — the Run now click
  now gets its 202. `42 passed, 2 failed`; the two failures are other agents' work (26-querylog-reason,
  34-version), not this task. The private copy was deleted afterwards.
- Mutation check: replacing `f.Confidence > p.MaxConfidence` with `> 1` makes `TestCapacityForecastAgent`
  fail (`model calls = 1, want 2`); dropping the `max != nil` guard on the exhaustion date makes
  `TestCapacityProjection` fail (`no limit or confidence cap`). Both mutations reverted, suite green
  again (`ok … 4.613s`).
- Deviations recorded in the plan text of Task 20: `recursor_cache` is not sampled (no engine stat and
  no `resolution_settings.recursor_cache_max_bytes` before M7 Task 12; marked with a `debt:` comment),
  `blocklist_entries` sums enabled block lists only, `SampleToday` lives in `agent.go`, and the model
  answer must cover every projected resource exactly once.
