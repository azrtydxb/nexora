# M2 Task 10: GUI screens `/policies`, `/rewrites` and Settings TLS section

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 10 ("GUI screens `/policies`, `/rewrites` and Settings TLS section") of milestone M2 exactly as specified in
`.procoder/plans/nexora-v1-m2.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 10 in `.procoder/plans/nexora-v1-m2.md` is done as written (deviations recorded in the plan first)
- [x] `TestGUICoverage` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestPlaywrightSpecs` passes in the dev pod (`scripts/dev-exec.sh`) (N/A names: TestPlaywrightSpecs)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- `scripts/dev-exec.sh 'make web-test'` — typecheck, eslint, permission parity (54 operations match) and vite build succeeded.
- `scripts/dev-exec.sh 'make e2e-build'` then `scripts/dev-exec.sh 'NEXORA_E2E_BIN_DIR=$PWD/bin go test -count=1 -v ./e2e/ -run "^TestGUICoverage$"'` — `15 passed (53.2s)` including `12-policies.spec.ts` (2), `13-rewrites.spec.ts` (1), `14-settings-dns-tls.spec.ts` (1); `--- PASS: TestGUICoverage (73.37s)`, no uncovered operations.
- `scripts/dev-exec.sh 'NEXORA_E2E_BIN_DIR=$PWD/bin go test -count=1 -v ./e2e/ -run "^TestAuthRBACAuditOIDC$"'` — `--- PASS: TestAuthRBACAuditOIDC (15.13s)`.
- Not verified: the red run before implementation (specs and pages were written in one pass); `TestPlaywrightSpecs` (not found in the repo); the configured-certificate branch of the Settings section in a browser (the harness loads no DNS certificate). Not committed (lead commits).

Closing evidence (lead, 2026-09-14):

- TestPlaywrightSpecs: N/A — no test with this name exists (criterion was auto-generated from plan text); the behaviour is covered by the task's actual tests, which passed in the full suite (engine/mgmt/web/e2e/bench/deploy) passed in the dev pod during the pre-release hardening sweep at 93dcd04
- gate/commit: commit gate passed on every commit for this task; todo last committed in cd77ce1
