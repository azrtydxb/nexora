# M7 T12: recursor_cache_max_bytes in the API, snapshot and GUI (#18)

Status: open
Created: 2026-09-14

## Description

Implements Task 12 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Add to `resolution_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/api -run TestResolutionSettingsRecursorCacheMaxBytes'`. Expect FAIL `default recursor_cache_max_bytes = 200 <nil>`.
- [x] Create `mgmt/migrations/00800_recursor_cache_max_bytes.sql`:
- [x] `store/resolution.go`: add `RecursorCacheMaxBytes int64` to `ResolutionSettings` and to the select and update statements of `GetResolutionSettings`/`UpdateResolutionSettings`.
- [x] `openapi.yaml`: in `ResolutionSettings`, add `recursor_cache_max_bytes` to `required` and the property `recursor_cache_max_bytes: { type: integer, format: int64, minimum: 4194304, maximum: 17179869184, description: "Memory for the RRset, aggressive NSEC and server caches of recursive resolution, in 
- [x] `api/resolution.go`: map the field in `resolutionOut` and in the update input.
- [x] `snapshot/resolution.go`: add `CacheMaxBytes: uint64(res.RecursorCacheMaxBytes),` to the `RecursionConfig` literal.
- [x] `ResolutionSection.tsx`:
- [x] In `17-resolution.spec.ts`, after the "Maximum upstream queries" fill:
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/api/... ./mgmt/internal/snapshot/... ./mgmt/internal/store/... && make web-test && cd web && pnpm lint'` and expect all to pass. Run `scripts/dev-exec.sh 'make e2e-build && go test -count=1 ./e2e -run "TestGUICoverage" -timeout 60m'` and exp

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/api -run TestResolutionSettingsRecursorCacheMaxBytes'`
  -> `resolution_test.go:196: default recursor_cache_max_bytes = 200 <nil>` FAIL (the stated reason).
- Migration 00800, store select/update, OpenAPI property (required, int64, 4194304..17179869184),
  `validateResolution` range check (400 invalid_request), `resolutionOut`/input mapping,
  snapshot `CacheMaxBytes`, GUI field `Recursor cache memory (MiB)`, e2e fill + reload assertion.
- Generated on the laptop: `gen.go` with `go run .../oapi-codegen@v2.8.0` (laptop binary is v2.6.0 and
  fails on the 3.1 spec; the committed header says v2.8.0); `schema.d.ts` with openapi-typescript 7.13.0.
  Only the new field changed in both (plus the embedded spec blob).
- Deviation: "M6 help tooltip" is not on the m7 branch; used the page's existing `Field` `hint` text.
  Plan text updated.
- Green: `go test -count=1 ./mgmt/internal/api/... ./mgmt/internal/snapshot/... ./mgmt/internal/store/...`
  -> api ok 81.7s, snapshot ok 20.0s, store ok 32.4s. `make web-test` (typecheck, lint, build) ok;
  `pnpm lint` ok ("permission parity: 120 operations match"). `go vet` on the three packages ok; gofmt clean.
- e2e: `make e2e-build` ok; `go test -count=1 ./e2e -run TestGUICoverage -timeout 60m` -> Playwright
  `29 passed (1.6m)` including `17-resolution.spec.ts` (3.5s), but the test FAILS on the coverage check:
  `8 OpenAPI operations have no covering Playwright test` (changeOwnPassword, getDashboardHealth,
  getDashboardSeries, getDashboardTop, getEngineLogs, getEngineMetrics, getVersion, updateCurrentUser).
  These are M6 T2 contract operations already in m7's base (cb517d5) whose M6 GUI specs are not on this
  branch; unrelated to Task 12. Last criterion left open for that reason. Not committed.

