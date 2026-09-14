# M5 Task 14: Final kw deployment and TestKwFullProduct

Status: open
Created: 2026-09-13

## Description

Implement Task 14 ("Final kw deployment and TestKwFullProduct") of milestone M5 exactly as specified in
`.procoder/plans/nexora-v1-m5.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 14 in `.procoder/plans/nexora-v1-m5.md` is done as written (deviations recorded in the plan first)
- [x] `TestEncryptedTransports` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] `TestKwBodiesMatchOpenAPI` passes in the dev pod (`scripts/dev-exec.sh`) — no such test exists in the repository (template criterion); not applicable
- [x] `TestKwFullProduct` passes in the dev pod (`scripts/dev-exec.sh`)
- [ ] procoder gate clean over the changed files; work committed (gate clean; commit is the lead's)

## Evidence

- Images `sha-93dcd04` built from `git worktree add --detach /tmp/nexora-final HEAD` with `scripts/build-image.sh` (engine and mgmt exit 0); worktree removed.
- DB backup: `pg_dump -Fc` into the dev pod `/work/kw-backup/nexora-pre-m5-202609140030.dump`; restored into a scratch PostgreSQL 17 in the pod: zones 2, users 1, zone_records 11, dnssec_keys 2, goose 403 (same as live).
- `scripts/dev-exec.sh 'go vet ./e2e/ && go test ./e2e/ -run TestKwFullProduct -count=1 -v'` without kw env: `--- SKIP: TestKwFullProduct` (`NEXORA_KW_DNS_ADDR and NEXORA_KW_API_URL are not set`).
- `scripts/kw-deploy.sh --tag sha-93dcd04 --skip-build`: `STATUS: deployed` (revision 1 and 2), `daemon set "nexora-engine" successfully rolled out`, `daemon set "nexora-engine-edge-b" successfully rolled out`, `NEXORA_KW_ENGINES=8`, `NEXORA_KW_EDGE_B_DNS_ADDR=192.168.10.137:53`; LB IPs `nexora-mgmt-lb=192.168.10.135`, `nexora-dns=192.168.10.136`, `nexora-dns-edge-b=192.168.10.137`. After: goose 502, zones 2, zone_records 11, dnssec_keys 2, tsig 1, users 1; 8 engines named after nodes, 2 in edge-b.
- `scripts/kw-acceptance.sh` run 1: TestKwFullProduct/fleet-metrics-and-alerts FAIL (`current edge-b engines = 4`: the query summed both mgmt replicas); test query fixed to `max(...)`.
- `scripts/kw-acceptance.sh` run 2: `--- PASS: TestKwFullProduct (43.02s)` (all 6 subtests), `--- PASS: TestKwSmokeM4 (0.90s)`, `--- PASS: TestKwSmoke (9.45s)` (recursion SKIP as documented), `ok github.com/piwi3910/nexora/e2e 53.390s`.
- `scripts/dev-exec.sh 'go test ./e2e/ -run TestEncryptedTransports -count=1'`: `ok`; `go test ./deploy/deploytest/ -count=1 -v`: TestComposeExample, TestOperationsDoc, TestHelmTemplate, TestImagesWorkflow PASS.
