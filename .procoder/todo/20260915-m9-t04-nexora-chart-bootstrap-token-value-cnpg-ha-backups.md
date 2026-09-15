# M9 T4: Nexora chart: bootstrap token value, CNPG HA, backups and recovery

Status: open
Created: 2026-09-15

## Description

Implements Task 4 of `.procoder/plans/nexora-m9-platform.md` (spec `.procoder/specs/nexora-m9-platform.md`, milestone M9 Platform). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Before editing any chart file, capture the golden render in the dev pod:
- [x] Create `deploy/deploytest/helm_cnpg_test.go`:
- [x] Add the values from Interfaces to `values.yaml` and their schema to `values.schema.json`:
- [x] Edit `templates/database-cnpg.yaml`:
- [x] Edit `templates/mgmt-deployment.yaml`: with `mgmt.bootstrapToken.existingSecret`, add env
- [x] Add to `ci/lint-values.yaml`:

## Evidence

- Golden captured before any chart edit, in the pod, stdout redirected on the laptop:
  `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'helm template nexora deploy/helm/nexora --namespace nexora -f deploy/kw/values-kw.yaml --api-versions monitoring.coreos.com/v1 --set image.tag=golden' > deploy/deploytest/testdata/kw-render.golden.yaml`
  (707 lines, pod Helm v4.3.0). The laptop's Helm v4.1.1 renders extra blank lines between documents,
  so the golden is pod-only; plan step text updated.
- Red: `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'go test ./deploy/deploytest -run "TestHelmKwRenderUnchanged|TestHelmCNPG|TestHelmBootstrapToken" -count=1'`
  → `antiAffinity = <nil>, want true` (and the other HA fields), `TestHelmCNPGBackups` panics on the
  missing `spec.backup`, `FAIL`.
- First green run exposed `--set ...max_connections=200` failing the schema (`got number, want string`);
  `postgresql.parameters` now accepts string/number/boolean scalars and renders them quoted (plan updated).
- Green: `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'go test ./deploy/deploytest -count=1 -v'` →
  every test PASS including `TestHelmKwRenderUnchanged`, `TestHelmCNPGHighAvailability`,
  `TestHelmCNPGBackups`, `TestHelmCNPGRecovery`, `TestHelmBootstrapToken`, `TestHelmTemplate`;
  `ok github.com/piwi3910/nexora/deploy/deploytest 2.088s`.
- `helm lint --strict deploy/helm/nexora -f deploy/helm/nexora/ci/lint-values.yaml` → `1 chart(s) linted, 0 chart(s) failed`.
- `go vet ./deploy/deploytest` and `gofmt -l deploy/deploytest` → clean; `scripts/pc-format.sh` on `values.schema.json`.
- kw render byte-identical (golden test). Not committed (lead commits).

