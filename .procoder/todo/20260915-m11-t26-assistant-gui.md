# M11 T26: Assistant GUI

Status: implemented, not committed (the lead commits)
Created: 2026-09-15

## Description

Implements Task 26 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `e2e/gui_seed_ai_assistant_test.go`, which scripts `config_assistant` on `s.AI` with the
      `createPolicyGroup` plan for `guest-wifi-gui` (one entry: the last scripted response repeats).
- [x] Create `web/e2e/screens/54-ai-assistant.spec.ts`. As operator at 1280 px: a new session, the
      message, `ai-task-status` "Thinking", the assistant reply, the plan panel with
      `createPolicyGroup`, the apply (with the license acknowledgement), `guest-wifi-gui` on
      `/policies`, and the session URL reopened with the conversation intact. At 400 px: the textarea
      and a new session.
- [x] Run the Task 24 commands. Expect FAIL, implement, and expect spec 54 and `12-policies` to PASS.
- [ ] Report the paths. Commit message: `M11 T26: AI assistant GUI`.

## Evidence

Tasks 25 and 27-31 are in flight in the shared tree, so every `TestGUICoverage` run used a private
copy of the tree in the dev pod (`/work/t26`, its own `bin/`, the committed `nexora-engine` reused
instead of a cargo build). The copy was deleted afterwards (`rm -rf /work/t26`).

- `cd web && pnpm run typecheck && pnpm run lint` → clean: `permission parity: 138 operations match`,
  `check-help: 168 controls on 46 pages have help`.
- Red (this task's spec and seed in, `AiAssistantPage.tsx` still the Task 11 stub):
  `go test ./e2e -run TestGUICoverage -count=1` → `11 failed, 45 passed (7.2m)` with
  `✘ 54-ai-assistant … applies it` (`waiting for getByTestId('ai-assistant-new')`) and
  `✘ 54-ai-assistant … at 400px` (`getByTestId('ai-assistant-message')` not found).
- Second run (page implemented): `9 failed, 47 passed (5.5m)`. Spec 54's 400 px test passed; the
  1280 px test reached the apply and failed there with a 422
  `license_acknowledgement_required` on `createPolicyGroup` (urlhaus is not free for commercial
  use) — the apply dialog showed no license checkbox because `licensedOperations` listed
  only `updateFilterCategory` and `updatePolicyGroup`. Fixed by adding `createPolicyGroup` (a create
  enables a licensed category exactly as an update does); the spec ticks the checkbox.
- Green: `7 failed, 49 passed (5.2m)` with
  `✓ 54-ai-assistant … applies it`, `✓ 54-ai-assistant … at 400px`,
  `✓ 12-policies … manages a policy group`, `✓ 12-policies … viewer sees policies read-only`,
  and `✓ 50-ai-status`, `✓ 51-ai-insights`, `✓ 52-ai-querylog`, `✓ 59-ai-disabled`.
  The Go test still exits 1: the coverage assertion lists the AI operations of Tasks 27-30 as
  uncovered, and `53-ai-recommendations`, `55-ai-forecasts`, `60-ai-viewer` (blocked by Task 25's
  page) and `34-version` fail for reasons outside this task.
- Viewer redirect: `60-ai-viewer.spec.ts` asserts it only after a Task 25 step that fails today, so a
  throwaway spec (`58-t26-viewer.spec.ts`, added to the pod copy only and deleted with it) checked
  `/ai/assistant` → `/ai` for a viewer with no `ai-assistant-message`: `✓ 1 passed`.

Paths: `web/src/pages/ai/AiAssistantPage.tsx`, `web/src/components/ai/ProposalApplyDialog.tsx`,
`.procoder/plans/nexora-m11-ai.md` (modified); `e2e/gui_seed_ai_assistant_test.go`,
`web/e2e/screens/54-ai-assistant.spec.ts` (created).
