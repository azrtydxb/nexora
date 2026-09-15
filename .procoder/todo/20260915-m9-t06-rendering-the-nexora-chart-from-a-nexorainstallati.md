# M9 T6: Rendering the Nexora chart from a NexoraInstallation

Status: open
Created: 2026-09-15

## Description

Implements Task 6 of `.procoder/plans/nexora-m9-platform.md` (spec `.procoder/specs/nexora-m9-platform.md`, milestone M9 Platform). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `operator/internal/render/values_test.go`: created; both values tests pass (after the lead-approved Task 1 change `EngineGroupSpec.NodeNamePrefix` to `*string`, deepcopy regenerated).
- [x] Create `operator/internal/render/render_test.go`: created; all five render tests pass.
- [x] Run `scripts/dev-exec.sh 'make operator-test'` and `cd operator && go vet ./...`, and expect PASS.

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'cd operator && go test ./internal/render -count=1'` before implementation: `no non-test Go files ... [build failed]`.
- Then `TestValuesFromKwEquivalentInstallation` failed only on `engine.groups[0].nodeNamePrefix: ""`; fixed with `NodeNamePrefix *string` and `make operator-generate` in toolbox-m9 (only `zz_generated.deepcopy.go` changed; both CRDs and `client.gen.go` byte-identical).
- Green: `go test ./internal/render -count=1 -v`: PASS for TestRenderMatchesHelmTemplate, TestRenderAddsOwnershipExceptRetainedKinds, TestRenderRejectsForeignNamespace, TestRenderChartFailureIsReported, TestRenderCapabilities, TestValuesFromKwEquivalentInstallation, TestBuildValuesPendingGroupsAndTag; `ok .../operator/internal/render 0.862s`.
- `make operator-test` (toolbox-m9): ok for api/v1alpha1, controller/enginegroup, keys, mgmtapi, mgmtapi/fake, render.
- `gofmt -l operator/internal/render operator/api`: empty; `go vet ./api/... ./internal/render ./internal/keys ./internal/mgmtapi/...`: OK.
