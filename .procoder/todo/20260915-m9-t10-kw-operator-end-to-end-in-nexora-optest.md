# M9 T10: kw operator end-to-end in nexora-optest

Status: open (every subtest green on kw with `dev-m9-8483b44`; the test and plan changes await the lead's commit)
Created: 2026-09-15

## Description

Implements Task 10 of `.procoder/plans/nexora-m9-platform.md` (spec `.procoder/specs/nexora-m9-platform.md`, milestone M9 Platform). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `scripts/kw-operator-e2e.sh`: built (the committed tree is copied with `git archive`, not a
      new git worktree; cleanup order lets the finalizers and node Jobs finish).
- [x] Create `operator/test/kw/helpers_test.go` (`//go:build kwe2e`) with: kubectl, probe, API, client,
      wait, workload and `dnsperf` helpers, plus `digLoop` and `updateRetry`.
- [x] Create `operator/test/kw/kw_operator_test.go`: `TestKwOperator` with the nine subtests.
- [x] Run `cd operator && go vet -tags kwe2e ./test/kw` and expect success. Run
      `scripts/kw-operator-e2e.sh`: all nine subtests PASS on kw (`dev-m9-8483b44`, re-run below).
- [x] Before and after the run, check that production was not touched.
- [x] Record in `.procoder/notes/plan-review.md` under `## M9 operator e2e (2026-09-15)`.

## Evidence

- `cd operator && gofmt -l test && go vet -tags kwe2e ./test/kw` — clean.
- Guard mutations (laptop, namespace `nexora-optest` absent so nothing could be applied):
  `NEXORA_OPTEST_NAMESPACE=nexora` → `namespace "nexora": the operator e2e runs only in nexora-optest`;
  node `master-12` → `node master-12 carries the production DNS addresses`; instance Service set to
  `LoadBalancer` → `Service nexora-optest-dns-b would take a node or LoadBalancer address`.
- `scripts/kw-operator-e2e.sh --skip-build --tag dev-m9-749166b-dirty` (run 2, exit 1, 756 s):
  `PASS guards (1.8s), install (117s), engine-groups (1.3s), join-token-rotation (94s), prune (2.7s),`
  `cnpg-backup-restore (86s), delete-retains-state (19s)`;
  `FAIL rolling-update (241s), cnpg-failover (192s)`.
  Cleanup completed: S3 prefix and bucket removed, three node Jobs completed, engine groups and operator
  removed, namespace `nexora-optest` deleted. The two CRDs stay installed, as the spec says.
- `rolling-update`: every engine pod was replaced in 27 s and the fresh-socket probe lost **0 of 1174**
  queries on each instance Service. `dnsperf` died on both with
  `Error: failed to receive packet: Software caused connection abort` when the first pod left the
  backends — Cilium 1.19.4 (`kube-proxy-replacement=true`) destroys UDP sockets connected to a removed
  backend, and `dnsperf` keeps one connected socket for the whole run. The spec's
  "dnsperf reports Queries lost: 0" cannot be measured through a ClusterIP on kw.
- `cnpg-failover`: `currentPrimary` changed 3m6s after `kubectl delete pod` (2m9s in run 1), over the
  spec's 120 s; the API answered 200 one second later and a group change reached every connected engine
  5 s after that. CNPG's `smartShutdownTimeout` (180 s default) waits for the management plane's pooled
  sessions before the failover starts.
- Fixed while proving this (Task 5's file, reported to the lead): the operator's bodiless `DELETE`s got
  `415 unsupported_media_type`, so no join token was ever revoked and no engine group deleted.
  `operator/internal/mgmtapi/fake` now enforces the management plane's `jsonOnly` rule (red:
  `TestFakeServesEngineGroupsAndTokens … 415`), and `client.go` sets the header on every non-GET request
  (green). `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'make operator-test'`: all packages `ok`.
- Open finding, not fixed (Task 7's files): with a failing revoke the controller created ~2500 join
  tokens in nine minutes (rotate writes the Secret, fails on the predecessor revoke, and the Secret event
  starts another rotation).
- Production untouched before and after: `kubectl --context kw -n nexora get nexorainstallations` →
  `No resources found`; `nexora-dns` 192.168.10.136 and `nexora-dns-2` 192.168.10.139 unchanged.
- Re-run 2026-09-15 on `dev-m9-8483b44` (see plan-review "M9 operator e2e re-run"): run A (images built
  by the script, test unchanged) failed only `rolling-update` on `dnsperf`'s ECONNABORTED;
  `cnpg-failover` 43 s. `rolling-update` now asserts zero loss with the fresh-socket probe plus its
  continuity (>= 960 sent, gap <= 1 s, window covers the roll) and asserts `dnsperf` only when it
  completes. Run B (`--skip-build`): `--- PASS: TestKwOperator (662.53s)`, exit 0; probe 0 of 1174 and
  0 of 1173 lost, both `dnsperf`s aborted 11 s after the change, failover 50 s, join tokens `default` 1
  and `edge` 8 in nine minutes. `nexora-optest` NotFound afterwards; production Services unchanged.
