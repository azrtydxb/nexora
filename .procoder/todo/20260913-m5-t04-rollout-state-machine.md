# M5 Task 4: Rollout state machine

Status: open
Created: 2026-09-13

## Description

Implement Task 4 ("Rollout state machine") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 4 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [ ] `TestAllAtOnceCompletesWhenConnectedEnginesApply` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestCanaryHappyPath` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestHaltsOnCanaryRejectAndAckTimeout` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestHaltsOnServfailRatio` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestHaltsWhenCanaryStopsReporting` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestLowTrafficPassesGate` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestPausedGroupHoldsChangesOnly` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestSelectCanaries` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestTarget` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

