# M5 Task 4: Rollout state machine

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 4 ("Rollout state machine") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 4 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [x] `TestAllAtOnceCompletesWhenConnectedEnginesApply` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestCanaryHappyPath` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestHaltsOnCanaryRejectAndAckTimeout` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestHaltsOnServfailRatio` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestHaltsWhenCanaryStopsReporting` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestLowTrafficPassesGate` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestPausedGroupHoldsChangesOnly` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestSelectCanaries` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestTarget` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

Implemented 2026-09-14, not committed (lead commits).

- Red: `scripts/dev-exec.sh 'go test ./mgmt/internal/rollout/ -count=1'` -> build failed, `undefined: Rollout`.
- Green: `scripts/dev-exec.sh 'go test ./mgmt/internal/rollout/ -count=1 -race -v'` -> all 12 tests PASS, `ok`.
- Added property-style tests (plan updated): `TestStepTransitionTable` (~2.5M enumerated steps without -race), `TestStepRandomWalk`, `TestTargetTable`.
- Mutation: `ratio > r.Params.MaxServfailRatio` -> `ratio >= 1` -> FAIL `TestHaltsOnServfailRatio`, `TestStepTransitionTable`, `TestStepRandomWalk`; restored -> ok.
- Gate: `procoder check` over changed files -> 0 unformatted, 0 blocking.

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in c44e988
