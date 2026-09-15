# M9 T9: NexoraInstallation controller

Status: open
Created: 2026-09-15

## Description

Implements Task 9 of `.procoder/plans/nexora-m9-platform.md` (spec `.procoder/specs/nexora-m9-platform.md`, milestone M9 Platform). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `operator/internal/controller/installation/controller_test.go`:
- [x] Create `operator/internal/controller/installation/rbac_test.go`:
- [x] Register in `setupControllers`:

## Evidence

- Red 1: `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'cd operator && go vet ./internal/controller/installation/'`
  -> `no non-test Go files` (package missing).
- Red 2 (after implementing): `go test ./internal/controller/installation/` in toolbox-m9 ->
  `--- FAIL: TestInstallationWaitsForJoinTokens ... deployments.apps "nexora-mgmt" not found`. Cause:
  `deploy/helm/nexora/values.schema.json` had `engine.groups.minItems: 1`, refusing the operator's
  `engine.groups: []` while join tokens are pending. Removed `minItems` (plan Task 9 Files updated);
  `helm lint --strict` passes and `helm template ... --set-json engine.groups=[]` renders mgmt only.
- Mutation check: `prune` returning early -> `TestInstallationPrunesRemovedObjects` fails with
  `removed instance b still exists`; restored.
- Green: `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'make operator-test'` -> every package `ok`
  (`internal/controller/installation 88.663s`, `enginegroup 82.874s`, `render 2.817s`, `api/v1alpha1`, `keys`,
  `mgmtapi`, `mgmtapi/fake`).
- `go test -count=1 ./deploy/...` (toolbox-m9) -> `ok github.com/piwi3910/nexora/deploy/deploytest`.
- Laptop: `cd operator && gofmt -l internal cmd` (empty), `go vet ./...` ok,
  `go build ./cmd/nexora-operator` ok.
- Not committed yet (the lead commits).
