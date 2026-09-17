# Operator kw render regression

Implemented in the existing worktree; no commits, cluster mutations, or devsync.

## Root cause and fix

The kw values added `mgmt.ai.existingSecret`, `mgmt.mcp.enabled`, and
`mgmt.mcp.readOnly`, but `MgmtSpec` lacked those fields. The existing fixture
change consequently failed strict decoding. The chart schema confirms AI has
only `existingSecret` and MCP has only the two boolean fields.

Added `AI SecretRef` and `MCP MCPSpec` to MgmtSpec. MCP booleans are optional
pointers, preserving explicit false and leaving omitted values to the chart
(disabled, read-only). BuildValues needs no change. Strict fixture decoding and
complete kw values equality remain intact. No raw golden was modified.

Generated deepcopies and both CRD copies using the installed controller-gen
v0.20.1. The pinned `go run` initially failed with GOPROXY=off due to module
lookup; the installed binary has the exact version required by Makefile.
Commands executed from repository root:

```sh
(cd operator && GOCACHE=/tmp/nexora-operator-render-gocache GOPROXY=off controller-gen object paths=./api/...)
(cd operator && GOCACHE=/tmp/nexora-operator-render-gocache GOPROXY=off controller-gen crd paths=./api/... output:crd:artifacts:config=../deploy/operator/crds)
cp deploy/operator/crds/*.yaml deploy/helm/nexora-operator/crds/
```

Added render tests covering omitted/empty settings, enabled with default
read-only, explicit false, enabled writable with an AI Secret, actual rendered
MCP environment and all three AI Secret keys, plus deepcopy independence.
Added API persistence roundtrip assertions for omitted values, the kw fixture,
and explicit-false MCP settings. Expected values are captured before Create so
API response pruning cannot mask field loss. Documented the operator fields.

## Validation

From `operator/`, with `GOCACHE=/tmp/nexora-operator-render-gocache GOPROXY=off`:

- PASS: `go test -count=1 -v ./internal/render -run 'TestValuesFromKwEquivalentInstallation|TestAIAndMCPValuesDefaultsAndOverrides|TestBuildValuesPendingGroupsAndTag'`.
- PASS: `go test ./api/v1alpha1 -run '^$'` (compile only).
- Full `go test -count=1 ./internal/render`: fails existing Helm parity assertions
  for engine/otel ConfigMaps. The identical failures reproduce in an isolated
  `git archive HEAD` baseline with `-run '^TestRenderMatchesHelmTemplate$'`.
- `go test -count=1 ./api/v1alpha1`: cannot run persistence tests because
  `KUBEBUILDER_ASSETS` is unset. Tests were not weakened or skipped.
- PASS: `git diff --check` and byte comparison of the generated installation CRD copies.

Parent Linux validation still required: `make operator-test` from repo root,
which provisions local envtest assets and runs the operator suite with race
checks. No deployed cluster is involved. Generated artifacts are already present;
no manual CRD edits or deferred generation are required.
