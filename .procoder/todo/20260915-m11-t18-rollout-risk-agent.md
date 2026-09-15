# M11 T18: Rollout risk agent

Status: done
Created: 2026-09-15

## Description

Implements Task 18 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `features_test.go` with `TestRolloutRiskFeatures`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/rolloutrisk -run TestRolloutRiskFeatures -count=1'`,
- [x] Create `agent_test.go` with `TestRolloutRiskAgent`. The fake model answers in order:
- [x] Implement `GetAiRolloutRisk` (404 for an unknown rollout), plus the registration file.
- [x] Create `e2e/ai_rollout_risk_test.go` with `TestRolloutNotDelayedByAI`:
- [x] Run the e2e test and expect PASS (run in the private tree, see Notes).
- [x] Report the paths. Commit message: `M11 T18: rollout risk assessment` (the lead commits).

## Evidence

Paths created or modified:

- `mgmt/migrations/01206_ai_rollout_risks.sql` (new)
- `mgmt/internal/ai/rolloutrisk/features.go`, `features_test.go`, `agent.go`, `agent_test.go` (new)
- `mgmt/internal/api/ai_rollout_risk.go` (stub replaced by the real handler)
- `mgmt/cmd/nexora-mgmt/ai_rolloutrisk.go` (new)
- `e2e/ai_rollout_risk_test.go` (new)
- `mgmt/api/openapi.yaml` (`EngineGroupInput` loses `additionalProperties: false`)
- `.procoder/plans/nexora-m11-ai.md` (Task 18 text made truthful)

TDD:

- `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/rolloutrisk -run TestRolloutRiskFeatures -count=1'`
  first failed with `no non-test Go files in /work/nexora/mgmt/internal/ai/rolloutrisk`, then after
  `features.go`: `ok  github.com/piwi3910/nexora/mgmt/internal/ai/rolloutrisk  3.306s`.
- `TestRolloutRiskAgent` first failed with
  `actions[0] updateEngineGroup: body: doesn't match schema due to: property "revision" is unsupported`
  — the `EngineGroupUpdate` `allOf` over a closed `EngineGroupInput` is unsatisfiable, so no
  `updateEngineGroup` proposal could ever validate. After removing `additionalProperties: false` from
  `EngineGroupInput` and implementing `agent.go`/`Get`:
  `ok  github.com/piwi3910/nexora/mgmt/internal/ai/rolloutrisk  5.252s` (with `mgmt/api` also ok).

E2E: `NEXORA_E2E_BIN_DIR=/work/t18-priv/bin go test ./e2e -run TestRolloutNotDelayedByAI -count=1 -v -timeout 15m`
→ `--- PASS: TestRolloutNotDelayedByAI (165.08s)` / `ok github.com/piwi3910/nexora/e2e 165.094s`.
It first failed (`Status:skipped`) because a description-only policy group update leaves the snapshot
content unchanged; widening the group's CIDRs makes it a real rollout.

Full suites in the private tree, twice, both green:
`go test ./mgmt/internal/ai/... ./mgmt/api ./mgmt/internal/api -count=1` — every package `ok`
(`mgmt/internal/api 202.387s` / `117.582s`), which also proves the openapi.yaml change breaks nothing.

Notes:

- The shared checkout does not build: another agent's in-flight `mgmt/internal/api/querylog_resolve.go`
  and `handlers_admin.go` (Task 19) are mid-edit. Verification therefore ran in a private copy of HEAD
  plus this task's files at `/work/t18-priv` in the dev pod, with private `bin/` binaries (the engine
  binary copied, never rebuilt, from `/work/target/release`).
- `rollout.Create` does not record `Immediate` in `rollouts.params`, so the skip rule compares the
  rollout snapshot's `content_sha256` with the group's previous snapshot's instead.
