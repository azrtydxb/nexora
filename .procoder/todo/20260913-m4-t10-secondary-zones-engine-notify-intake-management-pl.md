# M4 Task 10: Secondary zones — engine NOTIFY intake, management-plane AXFR/IXFR pulls and SOA timers

Status: open
Created: 2026-09-13

## Description

Implement Task 10 ("Secondary zones — engine NOTIFY intake, management-plane AXFR/IXFR pulls and SOA timers") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [ ] Every step of Task 10 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [x] `TestInterpret` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestInterpretAXFRStyleAndUpToDate` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestInterpretIncremental` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestRefreshAXFRThenIXFRThenUpToDate` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestRefreshFailureRetriesAndExpires` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

Management-plane side done (2026-09-14); engine side (notify_in.rs, opcode 4 routing, AuthState sink) not yet built by the engine agent, so the task stays open.

- `scripts/dev-exec.sh 'go test ./mgmt/internal/xfrin/ -run TestInterpret -count=1'` before `ixfr.go`: FAIL `undefined: Interpret`; after: `ok`.
- `scripts/dev-exec.sh 'go test ./mgmt/internal/xfrin/ -count=1'` before `refresh.go`: FAIL `undefined: xfrin.Refresher`; after: `go test -race ./mgmt/internal/xfrin/ -count=1 -v` — PASS TestInterpretIncremental, TestInterpretAXFRStyleAndUpToDate, TestRefreshAXFRThenIXFRThenUpToDate, TestRefreshFailureRetriesAndExpires, TestRefreshWithTSIGRequiresSignedPrimary, TestSchedulerLoadsOnCreateAndRefreshesOnNotify; `ok github.com/piwi3910/nexora/mgmt/internal/xfrin 8.467s`.
- `scripts/dev-exec.sh 'go test -race -count=1 ./mgmt/...'` — every package ok (EXIT=0).
- `pnpm lint` (web) — `permission parity: 92 operations match`; `golangci-lint run` on changed packages — no new findings.
- `TestSecondaryAndDynamicUpdate` (e2e): secondary zone loads via the mgmt AXFR (a.upstream.test answered); fails at `b.upstream.test. ... NXDOMAIN` because the engine does not forward NOTIFY yet.
