# M6 T8: Version endpoint and build stamping

Status: open
Created: 2026-09-14

## Description

Implements Task 8 of `.procoder/plans/nexora-m6-operator-ux.md` (spec `.procoder/specs/nexora-m6-operator-ux.md`,
milestone M6 Operator UX, GitHub issues #54-#67). Done when every step of that plan task is done, its
tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `deploy/deploytest/buildinfo_test.go`:
- [x] Run
- [x] Implement `GetVersion`: `select engine_version, count(*) from engines where deleted_at is null and engine_version <> '' group by 1 order by 2 desc, 1`.
- [x] Dockerfiles:
- [x] In `scripts/build-image.sh`, compute `commit=$(git -C "$context" rev-parse HEAD 2>/dev/null || echo "")`
- [x] In `web/vite.config.ts`, add
- [x] Run
- [ ] Report the paths. Commit message: `build: stamp version, commit and build date; GET /version`.

## Evidence

- Red (main tree, dev pod `toolbox`): `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestDockerfilesStampBuildInfo -count=1; make webui-placeholder && go test ./mgmt/internal/api -run TestVersionEndpoint -count=1'`
  -> FAIL: `deploy/docker/mgmt.Dockerfile lacks "ARG COMMIT="` (and the other 13 stamps); `undefined: api.Commit`.
- Green: the main tree's `mgmt/internal/api` did not compile at the time (Task 5 in progress: `handlers_admin.go:267 unknown field QType in struct literal of type querylog.Query`),
  so the plan command ran in an isolated copy in the pod (`/tmp/t8-q7x2` = `git archive HEAD` + only Task 8's files, `web/node_modules` symlinked):
  `go test ./deploy/deploytest -count=1` -> `ok` (TestDockerfilesStampBuildInfo, TestImagesWorkflow, TestHelmTemplate, TestComposeExample, TestOperationsDoc PASS);
  `go test ./mgmt/internal/config -count=1` -> `ok`;
  `make webui-placeholder && go test ./mgmt/internal/api -run "TestVersionEndpoint|TestCSRFAndDatabaseDown" -count=1` -> `--- PASS: TestCSRFAndDatabaseDown (7.14s)`, `--- PASS: TestVersionEndpoint (4.64s)`, `ok`;
  `cd web && pnpm run typecheck && pnpm run build` -> pass (dist/index.html written).
- `go vet` (api, config, cmd, deploytest) clean; `gofmt -l` clean; `shellcheck scripts/build-image.sh` clean; prettier check on vite.config.ts, build-info.d.ts, images.yml clean.
- `go build -ldflags "-X main.version=v6 -X main.commit=abc -X main.buildDate=d"` then `nexora-mgmt version` -> `nexora-mgmt v6 abc`.
- Not committed (lead commits); the last criterion stays open until then.
