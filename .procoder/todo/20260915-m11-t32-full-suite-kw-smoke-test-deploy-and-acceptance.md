# M11 T32: Full suite, kw smoke test, deploy and acceptance

Status: open
Created: 2026-09-15

## Description

Implements Task 32 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Create `e2e/kw_ai_test.go`, skipping when `NEXORA_KW_API_URL` is empty, like `TestKwSmoke`:
- [ ] In `deploy/kw/values-kw.yaml` set:
- [ ] Verify the Secret exists without reading its values:
- [ ] After the lead has committed Tasks 1–31, run the full local suite in the dev pod:
- [ ] Run `scripts/kw-deploy.sh` (it builds both images from a clean worktree of HEAD with tag
- [ ] Run `scripts/kw-acceptance.sh` and expect PASS for `TestKwSmoke`, `TestKwFullProduct`,
- [ ] On `https://nexora.kw.local`, check by hand:
- [ ] Report the paths, the image tag, and the probe and acceptance results. The lead closes #42–#52 with

## Evidence

