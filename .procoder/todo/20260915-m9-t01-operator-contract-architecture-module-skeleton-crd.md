# M9 T1: Operator contract: architecture, module skeleton, CRD types, generation, CI

Status: open (implemented, awaiting lead commit)
Created: 2026-09-15

## Description

Implements Task 1 of `.procoder/plans/nexora-m9-platform.md` (spec `.procoder/specs/nexora-m9-platform.md`, milestone M9 Platform). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `deploy/deploytest/platform_docs_test.go`:
- [x] Write `## Platform (M9)` in `docs/architecture.md` after `## Fleet (M5)`: the module layout, both
- [x] Create `operator/go.mod` (`module github.com/piwi3910/nexora/operator`, `go 1.27`) and
- [x] Write the types, `groupversion_info.go`, `version.go`, `envtestutil.go`, `main.go` and the two
- [x] Add to `Makefile`:
- [x] Create `operator/api/v1alpha1/validation_test.go`:
- [x] Write `operator/internal/render/testdata/kw-installation.yaml`: `deploy/kw/values-kw.yaml` as a
- [x] Add the CI job to `.github/workflows/ci.yml` after `mgmt`, with the same container, timeout 30 and
- [x] Run `scripts/dev-exec.sh 'make operator-test'` and `scripts/dev-exec.sh 'cd operator && go vet ./... && go run ./cmd/nexora-operator version'`.

## Evidence

- `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestArchitectureDocNamesPlatform -count=1'`
  before the doc section: FAIL `docs/architecture.md lacks ## Platform (M9)`; after: the whole package
  `go test ./deploy/deploytest -count=1` → `ok github.com/piwi3910/nexora/deploy/deploytest 1.113s`
  (includes TestImagesWorkflow).
- `cd operator && go get ...@pinned versions && go mod tidy` (laptop): controller-runtime v0.25.0,
  k8s.io v0.37.0, helm v3.21.4, oapi-codegen/runtime v1.7.0; `go build -tags tools ./...` OK (Helm
  compiles against k8s.io v0.37.0).
- `make operator-generate` (laptop) wrote `zz_generated.deepcopy.go` and both CRDs into
  `deploy/operator/crds/` and `deploy/helm/nexora-operator/crds/`; re-running it in the pod gives
  identical sha1 sums.
- TDD: with the CEL markers removed, `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'make operator-test'`
  FAILED: external-no-secret, opensearch-no-url, backup-no-path, short-ttl, renew-too-long admitted and
  groupName/installationRef changes accepted. With the markers: `ok github.com/piwi3910/nexora/operator/api/v1alpha1 17.738s`.
- `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'cd operator && go vet ./... && go run ./cmd/nexora-operator version'`
  → `nexora-operator dev` (vet clean, `gofmt -l operator` empty).
- Deviation: CEL rules use `size(x) > 0` instead of `x != ''` (gofmt rewrites `''` in doc comments to
  `”`); plan table updated.

