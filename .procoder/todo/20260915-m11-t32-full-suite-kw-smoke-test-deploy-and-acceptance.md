# M11 T32: Full suite, kw smoke test, deploy and acceptance

Status: open
Created: 2026-09-15

## Description

Implements Task 32 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create `e2e/kw_ai_test.go`, skipping when `NEXORA_KW_API_URL` is empty, like `TestKwSmoke`:
- [ ] In `deploy/kw/values-kw.yaml` set:
- [ ] Verify the Secret exists without reading its values:
- [x] After the lead has committed Tasks 1–31, run the full local suite in the dev pod:
- [ ] Run `scripts/kw-deploy.sh` (it builds both images from a clean worktree of HEAD with tag
- [ ] Run `scripts/kw-acceptance.sh` and expect PASS for `TestKwSmoke`, `TestKwFullProduct`,
- [ ] On `https://nexora.kw.local`, check by hand:
- [ ] Report the paths, the image tag, and the probe and acceptance results. The lead closes #42–#52 with

## Evidence

2026-09-15 verification sweep (local suite only; kw_ai_test.go, values-kw, Secret check, deploy and acceptance are the lead's kw step and not done here):
- 54-ai-assistant flake fixed at its root (commit db6c761): fake model answer held until the spec has seen "Thinking" (`OpenAIResponse.Hold`, `POST /control/release/{feature}`, `TestOpenAIFixtureHoldsUntilReleased` red then green).
- Also fixed: 51-ai-insights load race (0ea1ef9), TestQueryLogBackends partial-name ordering in the shared index (0ea1ef9), TestQueryLogCategoryAttribution/opensearch stale records and TestNoAgentWritesConfiguration insight window (uncommitted at the time of writing).
- dev pod: make lint rc=0; cargo test -p nexora-engine rc=0; go test -race ./mgmt/... ./deploy/... rc=0; web typecheck+build rc=0; full `go test ./e2e/...`: TestGUICoverage PASS; first full run failed TestNoAgentWritesConfiguration and TestQueryLogCategoryAttribution/opensearch (both fixed at the root, see plan-review.md; `-count=3` of both ok). Second full run `NEXORA_E2E_BIN_DIR=/work/nexora/bin go test -v -count=1 -timeout 120m ./e2e/...`: `ok github.com/piwi3910/nexora/e2e 1361.894s`, 78 PASS, 0 FAIL, only the four TestKw* skip without kw env; TestGUICoverage PASS in both full runs (258.16s, 256.63s) and the targeted run before them.

