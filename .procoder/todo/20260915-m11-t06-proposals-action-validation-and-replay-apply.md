# M11 T6: Proposals, action validation and replay apply

Status: open
Created: 2026-09-15

## Description

Implements Task 6 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/ai/proposal/validate_test.go` with `TestProposalActionValidation`.
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/ai/proposal -run TestProposalActionValidation -count=1'`
- [x] Implement `validate.go`.
- [x] Create `mgmt/internal/ai/proposal/proposal_test.go` with `TestUpsertDedupeAndDismiss`:
- [x] Create `mgmt/internal/ai/proposal/rpz_test.go` with `TestRpzZoneContent`.
- [x] Create `mgmt/internal/api/replay_test.go` with `TestReplayUsesCallerCredentials`.
- [x] Create `mgmt/internal/api/ai_proposals_test.go` with `TestApplyAiProposalsHandler` over
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/api -run TestApplyAiProposalsHandler -count=1'` and
- [x] Run `scripts/dev-exec.sh 'gofmt -l mgmt && go vet ./mgmt/... && go test ./mgmt/internal/ai/... ./mgmt/internal/api/... -count=1'` and expect PASS.
- [ ] Report the paths. Commit message: `M11 T6: AI proposals, validation and audited replay apply`. (paths reported; lead commits)

## Evidence

- Red: `go test ./mgmt/internal/ai/proposal -run TestProposalActionValidation` -> `no non-test Go files ... [build failed]`;
  `go test ./mgmt/internal/api -run TestReplay` -> `undefined: replayCall`, `h.replay undefined`.
- Handler test first run exposed a real bug (replay reused the original chi route context -> 404 `no such API route`); fixed in `replay.go`.
- Mutation checks (each reverted): unknown-field walk always open + revision check off -> `colour` and `stale revision` cases FAIL;
  dismissed-fingerprint check off -> `TestUpsertDedupeAndDismiss` FAIL; comment sanitising off -> `TestRpzZoneContent` FAIL;
  stale mapping off -> `TestApplyAiProposalsHandler` `stale apply ... Status:failed`; license merge wrong key -> `TestLicenseAcknowledgementOnlyForCategoryOperations` FAIL.
- `scripts/dev-exec.sh 'gofmt -l mgmt; go vet ./mgmt/... && go test ./mgmt/internal/ai/... ./mgmt/internal/api/... -count=1'`:
  gofmt no output, vet clean, `ok .../ai`, `ok .../ai/finding`, `ok .../ai/forecast`, `ok .../ai/proposal 14.952s`, `ok .../api 109.642s`.
