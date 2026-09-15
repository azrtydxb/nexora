# M6 T19: Query log GUI: partial names, multi-select, URL state, Reason column

Status: open
Created: 2026-09-14

## Description

Implements Task 19 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `e2e/gui_seed_querylog_test.go`:
- [x] Update `web/e2e/screens/10-query-log.spec.ts`. After the existing search, add:
- [x] Run
- [x] Create `web/src/components/MultiSelect.tsx` from Radix `Dialog`-free primitives. Use a `Popover`
- [x] In `web/src/pages/QueryLogPage.tsx`:
- [x] Run the same `TestGUICoverage` command and expect specs 10, 23, 24, 25 and 26 to pass. Run
- [ ] Report the paths. Commit message: `gui: query log multi-select, URL state and decision reason`. (paths reported; commit is the lead's)

## Evidence

All pod runs used a private copy (`/work/t19`: HEAD + this task's files, own `CARGO_TARGET_DIR` and
`bin`), deleted afterwards, because the shared tree carried other tasks' in-progress work.

- Red (HEAD page, new specs and seed): `make e2e-build && NEXORA_E2E_BIN_DIR=/work/t19/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m`:
  `5 failed` — 10 (`Expected pattern: /part of a name/i`, received `"example.com"`), 23 and 25
  (`waiting for getByTestId('querylog-category-option-malware')` / `querylog-qtype-option-A`, timeout),
  26 (`querylog-reason` not found), 30 (`missing environment variable NEXORA_E2E_ACL_ZONE`, Task 16's
  seed, not in that HEAD).
- Seed fixes found on the way (plan text updated): every forwarded answer was SERVFAIL until the seed
  turned off forwarded DNSSEC validation; an allowlist entry with no covering block list is decided
  "none" by the engine; the engine's miss path (`resolve_miss` in `engine/src/server/mod.rs`) drops the
  allowlist attribution, so only the cached second answer carries it (`debt:` in the seed).
- Green (HEAD at e478d44+ with Task 12 engine, plus this task): same command: `38 passed`, `1 failed`
  — `✓ 10-query-log`, `✓ 23-querylog-category`, `✓ 24-querylog-refresh`, `✓ 25-querylog-filters`,
  `✓ 26-querylog-reason`; the failure is 30-access-control-split (`NEXORA_E2E_ACL_ZONE`, seeded by
  `e2e/gui_seed_access_test.go`, committed by Task 16 after the copy was taken).
- `cd web && pnpm run typecheck && pnpm run lint` (pod copy and shared tree): typecheck clean,
  `permission parity: 138 operations match`, `check-help: 0 controls on 0 pages have help`.
- `gofmt -l e2e` empty, `go vet ./e2e` ok; prettier `--check` clean on the changed web files.

## Follow-up (2026-09-15): 26-querylog-reason flake in the full run

Root cause: the engine exports query log batches best-effort (`engine/src/telemetry/otlp.rs`
`export_batch`) — a batch whose management channel is unavailable, or whose export times out, is
counted in `export_dropped` and never retried. The seed queried the allowlisted name exactly once,
right after the allowlist config apply, so a dropped batch left the spec with no row to filter by
`source=allowlist` ("No queries match these filters."). `e2e/gui_seed_querylog_test.go` now re-queries
until the record is in the query log with its allowlist attribution (GET /query-log?source=allowlist).

Evidence (private pod copy `/work/qlfix`, own `CARGO_TARGET_DIR`/`bin`, deleted afterwards):
`NEXORA_E2E_BIN_DIR=/work/qlfix/bin go test ./e2e -run TestGUICoverage -count=1 -timeout 45m` ->
`✓ 10-query-log`, `✓ 23-querylog-category`, `✓ 25-querylog-filters`, `✓ 26-querylog-reason`,
`✓ 52-ai-querylog`, `✓ 34-version`; `48 passed`, the 2 failures are 60-ai-viewer (another agent's
in-progress M11 work).
