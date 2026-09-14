# M4 Task 10: Secondary zones — engine NOTIFY intake, management-plane AXFR/IXFR pulls and SOA timers

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 10 ("Secondary zones — engine NOTIFY intake, management-plane AXFR/IXFR pulls and SOA timers") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 10 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [x] `TestInterpret` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestInterpretAXFRStyleAndUpToDate` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestInterpretIncremental` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestRefreshAXFRThenIXFRThenUpToDate` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestRefreshFailureRetriesAndExpires` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

Management-plane side done (2026-09-14); engine side (notify_in.rs, opcode 4 routing, AuthState sink) not yet built by the engine agent, so the task stays open.

- `scripts/dev-exec.sh 'go test ./mgmt/internal/xfrin/ -run TestInterpret -count=1'` before `ixfr.go`: FAIL `undefined: Interpret`; after: `ok`.
- `scripts/dev-exec.sh 'go test ./mgmt/internal/xfrin/ -count=1'` before `refresh.go`: FAIL `undefined: xfrin.Refresher`; after: `go test -race ./mgmt/internal/xfrin/ -count=1 -v` — PASS TestInterpretIncremental, TestInterpretAXFRStyleAndUpToDate, TestRefreshAXFRThenIXFRThenUpToDate, TestRefreshFailureRetriesAndExpires, TestRefreshWithTSIGRequiresSignedPrimary, TestSchedulerLoadsOnCreateAndRefreshesOnNotify; `ok github.com/piwi3910/nexora/mgmt/internal/xfrin 8.467s`.
- `scripts/dev-exec.sh 'go test -race -count=1 ./mgmt/...'` — every package ok (EXIT=0).
- `pnpm lint` (web) — `permission parity: 92 operations match`; `golangci-lint run` on changed packages — no new findings.
- `TestSecondaryAndDynamicUpdate` (e2e): secondary zone loads via the mgmt AXFR (a.upstream.test answered); fails at `b.upstream.test. ... NXDOMAIN` because the engine does not forward NOTIFY yet.

Engine side done (2026-09-14): `engine/src/authoritative/notify_in.rs` (+ `notify_in_tests.rs`), opcode 4 routing in `dispatch::unparsed`, `Zone.secondary/primaries/update_keys` set by the loader, `AuthState::attach/detach` + `NotifySink`, `control::session` channel 1024 and attach/detach, `nexora_auth_notify_received_total{result=forwarded|dropped|refused}`.

- `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib -- notify_in_tests update_tests auth_families'` — `test result: ok. 6 passed` (both notify_in tests, auth_families_render_every_label_at_zero). The tests were written together with the implementation in one pass; the red run (unresolved import) was not captured separately.
- `scripts/dev-exec.sh make e2e-build` then `NEXORA_E2E_BIN_DIR=/work/nexora/bin go test -count=3 ./e2e/ -run "TestSecondaryAndDynamicUpdate|TestAXFRIXFROut|TestAuthoritativeZonePropagation" -v` — 3x PASS each; `ok github.com/piwi3910/nexora/e2e 44.782s`.
- `scripts/dev-exec.sh make engine-test` — all suites ok (lib `194 passed; 0 failed; 1 ignored`, hot_path_alloc `2 passed`); `cargo fmt --all -- --check` clean; `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` clean.

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in 5e7280b
