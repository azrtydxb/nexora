# M1 Task 1: Dev pod, sync/exec scripts and Makefile

Status: closed 2026-09-13
Created: 2026-09-13

## Description

Implement Task 1 ("Dev pod, sync/exec scripts and Makefile") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 1 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- `scripts/dev-exec.sh bash scripts/dev-selftest.sh` -> every line `ok:` (rustc 1.97.1, go1.27.1, node v26.7.0, pnpm 10.34.5, protoc-gen-go v1.36.12, protoc-gen-go-grpc 1.6.2, initdb 17.11, otelcol-contrib 0.160.0, cargo fuzz, rsync 3.4.1; /work 100Gi; nproc 8), exit 0.
- `scripts/dev-exec.sh 'make webui-placeholder && cat mgmt/internal/webui/dist/index.html && make -n e2e-build lint proto >/dev/null && echo parse-ok'` -> placeholder page printed, `parse-ok`.
- `scripts/dev-exec.sh bash -c 'ls mgmt/internal/webui/dist && echo "host=$HOSTNAME"'` -> `index.html` survives the sync (excluded), `host=toolbox-84f9c66cb5-rchql` (multi-arg quoting works).
- `scripts/dev-exec.sh false` -> `command terminated with exit code 1`, exit 1 (status propagates).
- Pod not recreated: Deployment `toolbox` kept; plan Task 1 rewritten to match; later `deploy/nexora-dev` exec/rollout references in the M1 plan changed to `deploy/toolbox`.
- procoder check: `4 clean, 0 unformatted, 0 unchecked ... (0 blocking)`; committed fbdd5a8.
