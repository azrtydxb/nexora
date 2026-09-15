# M11 T2: OpenAPI contract, permissions, generated clients, stubs, embedded spec

Status: done (verified; awaiting lead commit)
Created: 2026-09-15

## Description

Implements Task 2 of `.procoder/plans/nexora-m11-ai.md` (spec `.procoder/specs/nexora-m11-ai.md`, milestone M11 AI). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/api/embed_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/api/ -run TestOperationsIncludeM11 -count=1'` and expect
- [x] Create `mgmt/api/embed.go`. It embeds `openapi.yaml` with `//go:embed openapi.yaml`, loads it with
- [x] Add the schemas and operations to `mgmt/api/openapi.yaml`, then run
- [x] Add the roles to both permission maps, and the 501 stub handlers (each returns
- [x] In `server.go`:
- [x] Create `mgmt/internal/api/m11_contract_test.go`:
- [x] Run
- [x] Report the paths. Commit message: `M11 T2: OpenAPI contract, permissions, generated clients and AI stubs`.

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./mgmt/api/ -run TestOperationsIncludeM11 -count=1'` →
  `no non-test Go files in /work/nexora/mgmt/api` / `FAIL [build failed]`; after `embed.go` but before
  the spec change → `applyAiProposals = " ", want "POST /ai/proposals/apply"` (3 errors).
- Red: `TestM11OperationsHaveRoles` and `TestPermissionsCoverEveryOperation` failed with 18
  `has no permission entry` / `role ""` errors before the permission maps.
- Generation: `go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config
oapi-codegen.yaml openapi.yaml` (clean) and `pnpm run gen:api` (openapi-typescript 7.13.0, clean).
- Green: `scripts/dev-exec.sh 'go vet ./mgmt/... && go test ./mgmt/api/ ./mgmt/internal/api/
./mgmt/internal/auth/ -count=1'` → `ok mgmt/api 0.341s`, `ok mgmt/internal/api 77.430s`,
  `ok mgmt/internal/auth 19.382s`; vet silent. `go vet ./e2e/...` clean.
- Mutation: mapping `ai.ErrBudgetExhausted` to `ai_busy` makes `TestM11AIGateAndErrorCodes` FAIL; restored.
- Web: `pnpm run typecheck` clean; `pnpm run lint` → `permission parity: 138 operations match`.
- `gofmt -l` on changed Go dirs: no output. `TestGUICoverage` will list the 18 AI operations as
  uncovered until the GUI tasks land (expected).
