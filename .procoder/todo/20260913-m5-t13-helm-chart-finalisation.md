# M5 Task 13: Helm chart finalisation

Status: open
Created: 2026-09-13

## Description

Implement Task 13 ("Helm chart finalisation") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 13 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [x] `TestComposeExample` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestHelmTemplate` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestImagesWorkflow` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- Red: `scripts/dev-exec.sh 'go test ./deploy/deploytest/ -run TestHelmTemplate -count=1'` -> FAIL `helm lint: ... open ../helm/nexora/ci/lint-values.yaml: no such file or directory`.
- Green (dev pod, helm v4.3.0): `scripts/dev-exec.sh 'go vet ./deploy/... && go test ./deploy/deploytest/ -count=1 -v'` -> `--- PASS: TestComposeExample`, `--- PASS: TestHelmTemplate`, `--- PASS: TestImagesWorkflow`, `ok`. Also passes on the laptop (helm v4.1.1).
- `helm lint deploy/helm/nexora --strict -f deploy/helm/nexora/ci/lint-values.yaml` -> `1 chart(s) linted, 0 chart(s) failed` (INFO: icon recommended).
- `kubeconform v0.8.0 -strict -kubernetes-version 1.33.0` (default + datreeio CRDs-catalog) on `helm template` output for kw values, ci/lint-values, and a cnpg+Deployment+hostNetwork+otelCollector+ingress+DoT render -> `36 resources found in 3 files - Valid: 36, Invalid: 0` (first found the collector ConfigMap rendering config as a map; fixed and covered by a test assertion).
- Mutation checks: removing mgmt `runAsGroup`, keeping the sysctl under hostNetwork, and un-blocking the collector config each fail TestHelmTemplate.
- Plan test fixes: decode into `map[string]any` (yaml.v3 propagates the named `obj` type to nested maps, so `path` saw nothing); PrometheusRule assertion includes the namespace matcher the plan's own template emits. Plan updated.
- Not committed (lead commits).

