# M1 Task 23: First deployment to kw and `TestKwSmoke`

Status: open
Created: 2026-09-13

## Description

Implement Task 23 ("First deployment to kw and `TestKwSmoke`") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 23 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] `TestKwSmoke` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestMain` passes in the dev pod (`scripts/dev-exec.sh`) (TestMain removed per the plan; the e2e package builds and vets)
- [ ] procoder gate clean over the changed files; work committed

## Evidence

- Red: `scripts/dev-exec.sh 'env NEXORA_KW_DNS_ADDR=192.0.2.1:53 NEXORA_KW_API_URL=http://nexora.kw.local go test -count=1 -run TestKwSmoke ./e2e/'` -> `--- FAIL: TestKwSmoke ... health: 404 map[]`.
- Images: `scripts/build-image.sh -f deploy/docker/{engine,mgmt}.Dockerfile -n nexora-{engine,mgmt} -t sha-cf25008 .` -> `pull as 192.168.10.131/azrtydxb/nexora-engine:sha-cf25008`, `.../nexora-mgmt:sha-cf25008`.
- Deploy: `scripts/kw-deploy.sh --tag sha-cf25008 --skip-build` -> `NEXORA_KW_DNS_ADDR=192.168.10.136:53`, `NEXORA_KW_API_URL=http://nexora.kw.local`; rerun is a no-op. Pods: 2 nexora-mgmt, 3 nexora-engine (master-12, worker-22, master-13), nexora-otelcol, nexora-blocklist, opensearch-0, nexora-db-1/2 Running.
- Green: `scripts/dev-exec.sh 'env NEXORA_KW_DNS_ADDR=192.168.10.136:53 NEXORA_KW_API_URL=http://nexora.kw.local go test -count=1 -v -run TestKwSmoke ./e2e/'` -> `--- PASS: TestKwSmoke (2.22s)`; `go vet ./e2e/` ok.
- Manual: `dig @192.168.10.136 example.com +short` -> 172.66.147.243, 104.20.23.154 (UDP and TCP, laptop and dev pod); `dig @192.168.10.136 ads.nexora-smoke.test +short` -> 0.0.0.0; Playwright login as admin over http://192.168.10.135 and http://nexora.kw.local -> engines page lists 3 engines `current 6`; `/api/v1/query-log` returns records from backend `opensearch`.
- Gate: `procoder check` over the changed files -> `11 clean, 0 unformatted ... (0 blocking)`; `shellcheck` clean.
