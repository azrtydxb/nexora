# M2 Task 8: OpenAPI operations and handlers for policies, rewrites and safe search

Status: open
Created: 2026-09-13

## Description

Implement Task 8 ("OpenAPI operations and handlers for policies, rewrites and safe search") of milestone M2 exactly as specified in
`.procoder/plans/nexora-v1-m2.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 8 in `.procoder/plans/nexora-v1-m2.md` is done as written (deviations recorded in the plan first)
- [x] `TestPolicyGroupAPI` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestRewriteAPIValidation` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- Generation: `scripts/dev-exec.sh 'oapi-codegen --version; cd mgmt/api && oapi-codegen -config oapi-codegen.yaml openapi.yaml && cd ../.. && go build ./mgmt/...'` -> `v2.8.0`, then FAIL `*handlers does not implement StrictServerInterface (missing method CreatePolicyGroup)`; gen.go copied back with `kubectl ... tar`; `cd web && pnpm run gen:api` -> `openapi-typescript 7.13.0 ... src/api/schema.d.ts`. After prettier-formatting openapi.yaml (`procoder format`), regenerating gave an identical gen.go (md5 7cde45dd3dc10164b60d7e2f3a557760 in pod and laptop).
- Pass: `scripts/dev-exec.sh 'go vet ./mgmt/... && go test -count=1 ./mgmt/internal/api/ -run "TestPolicyGroupAPI|TestRewriteAPIValidation|TestPermissions" -v'` -> `--- PASS: TestPermissionsCoverEveryOperation`, `--- PASS: TestPolicyGroupAPI (2.99s)`, `--- PASS: TestRewriteAPIValidation (3.19s)`.
- `scripts/dev-exec.sh 'make web-test'` -> exit=0, `permission parity: 53 operations match`.
- `scripts/dev-exec.sh 'make mgmt-test'` -> all packages ok (api 43.1s, auth, blocklist, config, control, pki, querylog, snapshot, stats, store, gen, bench).
- `procoder check` over changed files -> 0 unformatted, 0 blocking.
- Known: `TestGUICoverage` (e2e) will report the eleven new operations uncovered until M2 Task 10; not run here.
- Not committed (the lead commits serially per the implementer brief).
