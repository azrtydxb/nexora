# M9 T8: Operator packaging: image, workflow, operator chart and plain manifests

Status: open
Created: 2026-09-15

## Description

Implements Task 8 of `.procoder/plans/nexora-m9-platform.md` (spec `.procoder/specs/nexora-m9-platform.md`, milestone M9 Platform). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `deploy/deploytest/operator_chart_test.go`:
- [x] Write the operator chart per Interfaces. `watchNamespaces` must be non-empty with namespace scope:
- [x] Create `deploy/docker/operator.Dockerfile`:
- [x] In `workflow_test.go` add `"nexora-operator"` to the wanted images. In `buildinfo_test.go` add
- [x] Build the image once to prove the Dockerfile:

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'go test ./deploy/deploytest -run "TestOperatorChart|TestOperatorManifestsMatchChart" -count=1'`
  -> FAIL `helm lint: ... stat ../helm/nexora-operator/Chart.yaml: no such file or directory`.
- Green (same command, after the chart and `deploy/operator/operator.yaml`): `ok github.com/piwi3910/nexora/deploy/deploytest 0.491s`.
- `deploy/operator/operator.yaml` generated with the pod's helm 4.3.0 (laptop helm 4.1.1 omits the blank
  line before `---`, which failed TestOperatorManifestsMatchChart; the kw golden and CI use 4.3).
- Red: `... -run "TestImagesWorkflow|TestDockerfilesStampBuildInfo"` -> FAIL `build matrix lacks image nexora-operator`,
  `open ../../deploy/docker/operator.Dockerfile: no such file or directory`. Green after: `ok ... 0.009s`.
- `scripts/build-image.sh -f deploy/docker/operator.Dockerfile -n nexora-operator -t dev-m9-t8` ->
  `nexora-operator dev-m9-t8 e1a0ecc... 2026-09-15T02:53:19Z` (version stamp) and
  `pull as 192.168.10.131/azrtydxb/nexora-operator:dev-m9-t8`.
- `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'go test ./deploy/deploytest -count=1; gofmt -l deploy/deploytest; go vet ./deploy/deploytest'`
  -> `ok github.com/piwi3910/nexora/deploy/deploytest 2.564s`, gofmt and vet clean.
- `procoder check` on the changed chart, workflow, Dockerfile and tests: 0 unformatted, 0 blocking.
