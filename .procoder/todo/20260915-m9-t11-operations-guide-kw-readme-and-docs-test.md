# M9 T11: Operations guide, kw README and docs test

Status: open
Created: 2026-09-15

## Description

Implements Task 11 of `.procoder/plans/nexora-m9-platform.md` (spec `.procoder/specs/nexora-m9-platform.md`, milestone M9 Platform). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] In `deploy/deploytest/docs_test.go` add `"## Install with the Kubernetes operator"` and
- [x] Write `## Install with the Kubernetes operator` after `## Install with Docker Compose`:
- [x] Write `## PostgreSQL high availability and backups` after `## Backup and restore PostgreSQL`:
- [x] Add `## Operator e2e (namespace nexora-optest)` to `deploy/kw/README.md`: what

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestOperationsDoc -count=1'`
  after adding the two headings -> `--- FAIL: TestOperationsDoc` with `docs/operations.md lacks heading "## Install with the Kubernetes operator"`
  and `... "## PostgreSQL high availability and backups"`.
- Wrote both sections in `docs/operations.md` (TOC entries, `### CloudNativePG` now links to the new section,
  Known limitations updated) and `## Operator e2e (namespace nexora-optest)` in `deploy/kw/README.md`.
- Facts checked against the code: `operator/internal/controller/{installation,enginegroup}`,
  `operator/api/v1alpha1`, `deploy/helm/nexora/templates/database-cnpg.yaml`, `deploy/helm/nexora-operator`,
  `scripts/kw-operator-e2e.sh`, `operator/test/kw`, `mgmt/internal/fleet/groups.go`; results from
  `.procoder/notes/plan-review.md` (re-run `dev-m9-8483b44`). The Helm restore render was checked with
  `helm template ... --show-only templates/database-cnpg.yaml` (laptop); CNPG pod label `cnpg.io/instanceRole`
  checked read-only on kw.
- `scripts/pc-format.sh docs/operations.md deploy/kw/README.md` -> exit 0.
- Green: `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'go test ./deploy/deploytest -count=1'` ->
  `ok  github.com/piwi3910/nexora/deploy/deploytest 2.900s`.
- Not committed (implementers never commit; the lead commits).

