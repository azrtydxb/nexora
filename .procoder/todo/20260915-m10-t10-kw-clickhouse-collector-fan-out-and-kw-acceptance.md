# M10 T10: kw ClickHouse, collector fan-out and kw acceptance

Status: open
Created: 2026-09-15

## Description

Implements Task 10 of `.procoder/plans/nexora-m10-querylog-backends.md` (spec `.procoder/specs/nexora-m10-querylog-backends.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `deploy/deploytest/kw_querylog_test.go`:
- [x] Run
- [x] Create `deploy/kw/clickhouse.yaml`:
- [x] Edit `deploy/kw/otelcol.yaml`:
- [x] Run the step-2 command and expect PASS.
- [x] Edit `scripts/kw-deploy.sh`, before the existing `k apply -f "$kw/opensearch.yaml" ...` line:
- [x] Create `mgmt/internal/querylog/e2e/kw_querylog_backends_test.go` with `TestKwQueryLogBackends` (not `e2e/`: that import path cannot import the internal `querylog` package):
- [x] Edit `scripts/kw-acceptance.sh`:
- [x] Edit `deploy/kw/README.md`:
- [x] Run `scripts/pc-format.sh deploy/kw/README.md deploy/kw/clickhouse.yaml deploy/kw/otelcol.yaml` and
- [x] Run `scripts/dev-exec.sh 'go test ./deploy/deploytest -count=1 && go vet ./e2e/...'` and expect PASS.
- [ ] Report the paths. Commit message: `kw: clickhouse backend, collector fan-out to clickhouse and loki`.

## Evidence

- Red: `NEXORA_DEV_DEPLOY=toolbox-m10 scripts/dev-exec.sh 'go test ./deploy/deploytest -run "TestKwClickHouseManifest|TestKwCollectorFansOutQueryLogs" -count=1'` failed with `open ../kw/clickhouse.yaml: no such file or directory` and `pipeline logs/clickhouse missing`, `pipeline logs/loki missing`, `container otelcol has no env`.
- Green: the same command: `ok github.com/piwi3910/nexora/deploy/deploytest 0.210s` (both PASS).
- `otelcol-contrib validate` is a real check: the kw config with an extra `bogus_key` under `exporters.clickhouse` exits 1 (`'clickhouseexporter.Config' has invalid keys: bogus_key`).
- Full: `scripts/dev-exec.sh 'go test ./deploy/deploytest -count=1 && go vet ./e2e/... ./mgmt/internal/querylog/e2e/ ./deploy/deploytest/'`: `ok .../deploy/deploytest 2.219s`, vet clean. `go test -run TestKwQueryLogBackends ./mgmt/internal/querylog/e2e/` without the kw environment: SKIP (so `make e2e` is unaffected).
- `kubectl --context kw apply --dry-run=server` of `clickhouse.yaml` and `otelcol.yaml` renamed into `nexora-dev`: all six objects `created (server dry run)`.
- Throwaway copy in `nexora-dev` (StatefulSet `clickhouse-t10`, 2 Gi, Secret with random passwords, never printed): rollout complete; `deploy/clickhouse/querylog.sql` applied twice through `kubectl exec -i ... clickhouse client --multiquery <file` (the kw-deploy.sh command) without error; `nexora_writer` INSERT ok, SELECT denied; `nexora_reader` SELECT ok, INSERT and DROP denied; `default` from the toolbox pod refused (`Authentication failed`, codes 516 and 194); the real adapter `querylog.NewClickHouse` as `nexora_reader` (readonly 2) returned `search: 1 <nil>` and `top: [{t10.test. 1}] <nil>`. Everything deleted afterwards (StatefulSet, Service, ConfigMap, Secret, PVC; `kubectl get all,pvc,cm,secret | grep t10` empty).
- `scripts/pc-format.sh deploy/kw/README.md deploy/kw/clickhouse.yaml deploy/kw/otelcol.yaml`: rc 0. `shellcheck scripts/kw-deploy.sh scripts/kw-acceptance.sh`: no findings. `shfmt -d scripts/kw-acceptance.sh`: clean; `shfmt -d scripts/kw-deploy.sh` reports only the `[ -n "$dns_ips" ] || { ...; }` line, identical on HEAD.
- `golangci-lint run ./deploy/deploytest/ ./mgmt/internal/querylog/e2e/`: one finding, `ai_test.go:63` errcheck, present before this task.
- Loki service confirmed read-only: `kubectl --context kw get svc -n monitoring` lists `loki ClusterIP 3100/TCP`.
- Not run here: `TestKwQueryLogBackends` against kw (Task 12, after the deploy).
