# M8 T15: Catalog service: producer regeneration and consumer reconciliation

Status: implemented (awaiting lead commit)
Created: 2026-09-15

## Description

Implements Task 15 of `.procoder/plans/nexora-m8-dns-protocols.md` (spec `.procoder/specs/nexora-m8-dns-protocols.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `mgmt/internal/catzone/service_test.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/catzone -run "TestProducer|TestConsumer" -count=1'`
- [x] Implement `service.go`:
- [x] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/catzone ./mgmt/internal/xfrin -count=1 && go vet ./mgmt/...'`
- [ ] Report the paths. The lead commits `M8 T15: catalog producer regeneration and consumer reconciliation`.

## Evidence

- Pod toolbox-m7 (synced): `go test ./mgmt/internal/catzone ./mgmt/internal/xfrin -count=1` ->
  `ok mgmt/internal/catzone 7.501s`, `ok mgmt/internal/xfrin 21.146s`; `go vet ./mgmt/...` exit 0;
  `gofmt -l` on the changed files: empty.
- Red (private copy /work/m8-t15, removed afterwards): `Reconcile` renamed away ->
  `service_test.go:63:20: env.svc.Reconcile undefined` (build failed).
- Mutation: label comparison dropped -> `service_test.go:85: label change did not recreate a.remote.`
- Mutation: `Regenerate` writes an empty member list -> `service_test.go:43: catalog after create: [] <nil>`.
- Mutation: `Expired` skip removed -> `service_test.go:96: an expired catalog was processed`.
- Mutation: clash checks disabled -> `service_test.go:64: conflict: duplicate key value violates unique constraint "zones_name_key"`.
- `Scheduler.AfterRefresh` has no dedicated test (the plan names none and `scheduler_test.go` is not in
  this task's files); Task 25's end-to-end test covers it.
- Remaining: the lead commits `M8 T15: catalog producer regeneration and consumer reconciliation`.
