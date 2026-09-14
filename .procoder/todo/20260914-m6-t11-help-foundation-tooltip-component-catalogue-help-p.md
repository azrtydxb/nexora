# M6 T11: Help foundation: tooltip component, catalogue, help pages, lint check

Status: open
Created: 2026-09-14

## Description

Implements Task 11 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `deploy/deploytest/help_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestHelpTopicsReferenceOperationsDoc -count=1'`
- [x] Create the eight topic files. Each has the first line naming headings that exist in
- [x] Run `cd web && pnpm add react-markdown@10` and confirm the version resolved in `pnpm-lock.yaml`.
- [x] Create `web/src/pages/HelpPage.tsx`. It renders `PageHeader title="Help"` with a topic list at
- [x] Create `web/src/components/HelpTip.tsx` per Interfaces, using `Tooltip`, `TooltipTrigger` and
- [x] Create the empty area files, for example `web/src/help/catalog/resolver.ts`:
- [x] Create `web/scripts/check-help.mjs`:
- [x] Add a self-test by running
- [x] In `web/src/components/ListEditor.tsx`, add the optional `help?: string` prop and render
- [x] Run
- [x] Report the paths (lead commits). Commit message: `gui: help tooltip, catalogue, help pages and help lint`.

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestHelpTopicsReferenceOperationsDoc -count=1'`
  before the topics existed: `help_test.go:29: want 8 help topics, have 0`, FAIL.
- `cd web && pnpm add react-markdown@10`: resolved `react-markdown@10.1.0` in `pnpm-lock.yaml`.
- Self-test of `node scripts/check-help.mjs` with a temporary `src/pages/ProbeHelp.tsx` listed in
  `resolverHelp.pages`: no entry, exit 1 `check-help: pages/ProbeHelp.tsx: control "probe-field" has no help entry`;
  entry without HelpTip, exit 1 `... control "probe-field" has no HelpTip`; entry and
  `<HelpTip id="probe-field" />`, exit 0 `check-help: 1 controls on 1 pages have help`. Probe removed
  and `pages: [], entries: {}` restored.
- Heading slugs of the eight topics match every anchor the plan names (checked with the same
  lower-case, non-alphanumeric-to-hyphen rule as `HelpPage`).
- Green: `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestHelpTopicsReferenceOperationsDoc -count=1 && cd web && pnpm install --frozen-lockfile && pnpm run typecheck && pnpm run lint && pnpm run build'`:
  `ok github.com/piwi3910/nexora/deploy/deploytest`, typecheck clean, `permission parity: 120 operations match`,
  `check-help: 0 controls on 0 pages have help`, `✓ built in 1.10s`.
- Not committed: the lead commits the reported paths.
