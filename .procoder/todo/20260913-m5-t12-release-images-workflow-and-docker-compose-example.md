# M5 Task 12: Release images workflow and docker-compose example

Status: open
Created: 2026-09-13

## Description

Implement Task 12 ("Release images workflow and docker-compose example") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 12 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [x] `TestComposeExample` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestImagesWorkflow` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./deploy/deploytest/ -count=1'` -> FAIL: `open ../compose/docker-compose.yml: no such file or directory`; `workflow does not contain "refs/heads/main"` (plus `:main"`, arm64/amd64 grep).
- Green: `scripts/dev-exec.sh 'go test ./deploy/deploytest/ -count=1 -v'` -> `--- PASS: TestComposeExample`, `--- PASS: TestImagesWorkflow`, `ok github.com/piwi3910/nexora/deploy/deploytest`.
- `actionlint .github/workflows/*.yml` (v1.7.12, with shellcheck) -> no output.
- `prettier --check` on compose YAML and images.yml -> clean; `golangci-lint run ./deploy/...` -> 0 issues.
- Not run: the compose stack itself (no Docker available; `docker compose config` not executed). `ca init --if-missing` depends on M5 Task 8's CLI. Not committed (lead commits).

