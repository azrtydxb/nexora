# M1 Task 22: Container images and CI workflows

Status: done
Created: 2026-09-13

## Description

Implement Task 22 ("Container images and CI workflows") of milestone M1 exactly as specified in
`.procoder/plans/nexora-v1-m1.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 22 in `.procoder/plans/nexora-v1-m1.md` is done as written (deviations recorded in the plan first)
- [x] `TestOIDCLoginAndProviderDown` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Red: `scripts/build-image.sh -f deploy/docker/engine.Dockerfile -n nexora-engine -t m1-check .` before the Dockerfile -> `error: invalid local: resolve : lstat deploy/docker: no such file or directory`.
- `scripts/build-image.sh -f deploy/docker/engine.Dockerfile -n nexora-engine -t dev-bd64c06 .` (working tree incl. Tasks 8/9) -> `pull as 192.168.10.131/azrtydxb/nexora-engine:dev-bd64c06` (cargo release 3m57s).
- `scripts/build-image.sh -f deploy/docker/mgmt.Dockerfile -n nexora-mgmt -t dev-bd64c06 .` -> `pull as 192.168.10.131/azrtydxb/nexora-mgmt:dev-bd64c06`.
- `kubectl --context kw -n nexora-dev run t22-engine-help --rm -i --restart=Never --image=192.168.10.131/azrtydxb/nexora-engine:dev-bd64c06 -- --help` -> clap usage `--config <CONFIG>  [default: /etc/nexora/engine.toml]`.
- `kubectl ... run t22-mgmt-version ... --image=192.168.10.131/azrtydxb/nexora-mgmt:dev-bd64c06 -- version` -> `nexora-mgmt dev`.
- `scripts/dev-exec.sh 'NEXORA_E2E_BIN_DIR=/tmp/t21bin go test -race -count=1 -run TestOIDCLoginAndProviderDown ./mgmt/internal/auth/'` -> `ok github.com/piwi3910/nexora/mgmt/internal/auth 7.313s`.
- actionlint 1.7.12 over all workflows (pod and laptop with shellcheck) -> no output.
- Deviation: engine `#[command(version)]` not added (engine/** owned by the concurrent engine task); recorded in the plan.
- mgmt.Dockerfile states `USER nonroot:nonroot` (security scan); rebuilt `nexora-mgmt:dev-t22-user` and `kubectl run ... -- version` -> `nexora-mgmt dev`.
- procoder check -> 0 blocking; images.yml prettier-formatted.
