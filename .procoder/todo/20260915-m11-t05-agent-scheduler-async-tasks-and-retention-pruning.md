# M11 T5: Agent scheduler, async tasks and retention pruning

Status: open
Created: 2026-09-15

## Description

Implements Task 5 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/ai/scheduler_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/ai -run TestScheduler -count=1'` and expect FAIL:
- [x] Implement `scheduler.go`. Per tick, per enabled agent:
- [x] Create `mgmt/internal/ai/tasks_test.go` with `TestTasksLifecycle` and `TestTasksInstanceLoss`:
- [x] Create `mgmt/internal/ai/prune_test.go` with `TestPrune`. Seed one row older and one newer than
- [x] Run `scripts/dev-exec.sh 'gofmt -l mgmt && go vet ./mgmt/... && go test ./mgmt/internal/ai/... -count=1'` and expect PASS.
- [ ] Report the paths. Commit message: `M11 T5: AI agent scheduler, async tasks and retention`.

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./mgmt/internal/ai -run TestScheduler -count=1'` → build failed,
  `undefined: ai.Run`, `undefined: ai.Scheduler`, `undefined: ai.NewTasks`/`ai.Task`, `undefined: ai.Prune`.
- Green: `go test ./mgmt/internal/ai -run "TestScheduler|TestTasks|TestPrune" -count=1 -v` → PASS
  (TestPrune, TestSchedulerRunsOnceAcrossInstances, TestSchedulerRespectsLastRunAndRequests,
  TestTasksLifecycle, TestTasksInstanceLoss); same with `-race` → `ok`.
- `go test ./mgmt/internal/ai/... -count=1` → ok (ai, finding, forecast, proposal);
  `go test ./mgmt/internal/store/... -count=1` → ok (migrations incl. 01201).
- `gofmt -l` on Task 5 files: clean. `go vet ./mgmt/internal/ai ./mgmt/internal/config ./mgmt/cmd/...`: clean.
  `go vet ./mgmt/...` fails only in `mgmt/internal/api/ai_findings_test.go` (`d.AI` undefined) — Task 7's
  test awaiting Task 6's `Deps.AI`; `gofmt -l mgmt` lists Task 6/7 files (`finding.go`, `validate.go`).
- Commit pending (lead commits).

