# M10 T3: ClickHouse and Loki binaries in the dev toolbox

Status: open
Created: 2026-09-15

## Description

Implements Task 3 of `.procoder/plans/nexora-m10-querylog-backends.md` (spec `.procoder/specs/nexora-m10-querylog-backends.md`). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Add to `scripts/dev-selftest.sh`, before the `cargo fuzz` check:
- [x] Run `scripts/dev-exec.sh 'bash scripts/dev-selftest.sh'` in the current toolbox and expect exit 1
- [x] Edit `deploy/dev/Dockerfile`:
- [x] Find the next unused tag:
- [x] Set `image: 192.168.10.131/azrtydxb/nexora-dev:toolbox-<N>` in `deploy/dev/dev-pod.yaml`.
- [x] Run `scripts/dev-exec.sh 'bash scripts/dev-selftest.sh'` and expect exit 0 with
- [x] Run `scripts/pc-format.sh deploy/dev/dev-pod.yaml`.
- [x] Report the paths and the image tag. Commit message: `dev: clickhouse and loki in the toolbox image`.

## Evidence

Not committed yet (lead commits).

- Red: `NEXORA_DEV_DEPLOY=toolbox-m10 scripts/dev-exec.sh 'bash scripts/dev-selftest.sh; echo exit=$?'` on image toolbox-1 -> `MISSING: clickhouse --version`, `WRONG or MISSING clickhouse (want 26.8.4.11)`, `MISSING: loki --version`, `WRONG or MISSING loki (want 3.6.7)`, `exit=1`.
- Tag: `curl -sk https://192.168.10.131/v2/azrtydxb/nexora-dev/tags/list` -> `["toolbox-1"]`; new tag `toolbox-2`.
- Build: `scripts/build-image.sh -f Dockerfile -n nexora-dev -t toolbox-2 deploy/dev` -> exit 0, `pull as 192.168.10.131/azrtydxb/nexora-dev:toolbox-2`; registry tags now `["toolbox-1","toolbox-2"]`.
- ClickHouse URL used: `https://github.com/ClickHouse/ClickHouse/releases/download/v26.8.4.11-lts/clickhouse-common-static-26.8.4.11-arm64.tgz`.
- Rollout (M10 pod only, per lead instruction): `kubectl --context kw -n nexora-dev set image deploy/toolbox-m10 toolbox=192.168.10.131/azrtydxb/nexora-dev:toolbox-2` and `rollout status` -> `successfully rolled out`. `deploy/dev/dev-pod.yaml` now names toolbox-2 but was NOT applied; `toolbox`, `toolbox-m7`, `toolbox-m9` still run toolbox-1 (lead rolls out).
- Green: `NEXORA_DEV_DEPLOY=toolbox-m10 scripts/dev-exec.sh 'bash scripts/dev-selftest.sh; echo exit=$?'` -> `ok: clickhouse --version -> ClickHouse local version 26.8.4.11 (official build).`, `ok: loki --version -> loki, version 3.6.7 (branch: release-3.6.x, revision: 7e1daf3a)`, `exit=0`.
- Negative branch: `env PATH=/usr/bin:/bin bash scripts/dev-selftest.sh; echo exit=$?` -> `WRONG or MISSING clickhouse (want 26.8.4.11)`, `WRONG or MISSING loki (want 3.6.7)`, `exit=1`.
- `scripts/pc-format.sh deploy/dev/dev-pod.yaml` run.
