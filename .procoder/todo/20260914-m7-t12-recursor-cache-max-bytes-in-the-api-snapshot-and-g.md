# M7 T12: recursor_cache_max_bytes in the API, snapshot and GUI (#18)

Status: open
Created: 2026-09-14

## Description

Implements Task 12 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [ ] Add to `resolution_test.go`:
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/api -run TestResolutionSettingsRecursorCacheMaxBytes'`. Expect FAIL `default recursor_cache_max_bytes = 200 <nil>`.
- [ ] Create `mgmt/migrations/00800_recursor_cache_max_bytes.sql`:
- [ ] `store/resolution.go`: add `RecursorCacheMaxBytes int64` to `ResolutionSettings` and to the select and update statements of `GetResolutionSettings`/`UpdateResolutionSettings`.
- [ ] `openapi.yaml`: in `ResolutionSettings`, add `recursor_cache_max_bytes` to `required` and the property `recursor_cache_max_bytes: { type: integer, format: int64, minimum: 4194304, maximum: 17179869184, description: "Memory for the RRset, aggressive NSEC and server caches of recursive resolution, in 
- [ ] `api/resolution.go`: map the field in `resolutionOut` and in the update input.
- [ ] `snapshot/resolution.go`: add `CacheMaxBytes: uint64(res.RecursorCacheMaxBytes),` to the `RecursionConfig` literal.
- [ ] `ResolutionSection.tsx`:
- [ ] In `17-resolution.spec.ts`, after the "Maximum upstream queries" fill:
- [ ] Run `scripts/dev-exec.sh 'go test -count=1 ./mgmt/internal/api/... ./mgmt/internal/snapshot/... ./mgmt/internal/store/... && make web-test && cd web && pnpm lint'` and expect all to pass. Run `scripts/dev-exec.sh 'make e2e-build && go test -count=1 ./e2e -run "TestGUICoverage" -timeout 60m'` and exp

## Evidence

