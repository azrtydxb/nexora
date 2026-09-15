# M9 T5: Operator key material and the management API client

Status: open
Created: 2026-09-15

## Description

Implements Task 5 of `.procoder/plans/nexora-m9-platform.md` (spec `.procoder/specs/nexora-m9-platform.md`, milestone M9 Platform). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] Create `operator/internal/keys/keys_test.go`: failing first (no non-test Go files), then PASS with `keys.go`.
- [x] Create `operator/internal/mgmtapi/oapi-codegen.yaml`: generates `client.gen.go` with `ClientWithResponses` (generated low-level client renamed `rawClient`).
- [x] Create `operator/internal/mgmtapi/client_test.go`: failing first (`mgmtapi.New` undefined), then PASS with `client.go`.
- [x] Create `operator/internal/mgmtapi/fake/fake_test.go`: `TestFakeServesEngineGroupsAndTokens` (steps 1-8) and `TestFakeKnobs`; failing first, then PASS with `fake.go`.
- [x] Run `scripts/dev-exec.sh 'make operator-test'` and `cd operator && go vet ./...`, and expect PASS.
- [ ] Committed (lead commits).

## Evidence

- `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'cd operator && go test ./internal/keys -count=1'` before `keys.go`: `no non-test Go files ... [build failed]`; after: `ok github.com/piwi3910/nexora/operator/internal/keys`.
- `... 'cd operator && go test ./internal/mgmtapi -count=1'` before `client.go`: `undefined: mgmtapi.ErrNotFound ... [build failed]`; after: `ok github.com/piwi3910/nexora/operator/internal/mgmtapi`.
- `... 'cd operator && go test ./internal/mgmtapi/... -count=1'` before `fake.go`: `mgmtapi/fake: no non-test Go files [build failed]`; after (with `-race`): `ok .../mgmtapi/fake`.
- Generation (laptop): `cd operator/internal/mgmtapi && go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config oapi-codegen.yaml ../../../mgmt/api/openapi.yaml` (the oapi line of `make operator-generate`).
- `NEXORA_DEV_DEPLOY=toolbox-m9 scripts/dev-exec.sh 'make operator-test && cd operator && go vet ./...'`: `ok api/v1alpha1`, `ok internal/keys`, `ok internal/mgmtapi`, `ok internal/mgmtapi/fake`; `go vet` clean. `gofmt -l operator/internal` empty.
