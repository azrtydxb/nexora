# M4 Task 16: kw deployment, Helm/compose key storage, smoke subtests

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 16 ("kw deployment, Helm/compose key storage, smoke subtests") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 16 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [x] `TestKWSmokeM4` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Red: `scripts/dev-exec.sh 'env NEXORA_KW_*… go test -count=1 -v -run TestKwSmokeM4 ./e2e/'` against M3 (sha-b643000): `POST /zones: status 404 ... no such API route` FAIL.
- Images from a clean worktree of HEAD c08a936 (plus this task's mgmt.Dockerfile): `scripts/build-image.sh -f deploy/docker/{engine,mgmt}.Dockerfile -t sha-c08a936` EXIT 0; build log `nexora-mgmt sha-c08a936`.
- First rollout crashed: `NEXORA_KEK_FILE ... has mode 0440` (image gid 999 from `useradd --system`); fixed with gid 65532 in the image and `runAsGroup: 65532`.
- `scripts/kw-deploy.sh --skip-build --tag sha-c08a936`: EXIT 0, mgmt/bind/engine rollouts complete, bootstrap created TSIG key, `nexora-demo.kw.` (6 records, DNSSEC KEK), `bind-demo.kw.`; second `deploy/kw/bootstrap.sh` run prints nothing, EXIT 0.
- In the mgmt pod: `id` uid/gid 65532; `ldd /nexora-mgmt` → `libc.so.6 => /lib/aarch64-linux-gnu/libc.so.6`.
- `go test -count=1 -v -run "TestKwSmokeM4|TestKwSmoke$" ./e2e/`: `--- PASS: TestKwSmokeM4` (zones, axfr, dnssec PASS), `--- PASS: TestKwSmoke` (recursion SKIP as before), `ok github.com/piwi3910/nexora/e2e 14.491s`.
- Image binary with SoftHSM2: `NEXORA_E2E_BIN_DIR=<copied image binary> go test -run TestTSIGKeysWithPKCS11OnlyKeyStorage ./e2e/`: `--- PASS` (4.87s).
- Manual (dev pod): SOA +dnssec `flags: qr aa ra`, RRSIG SOA 13; `delv -a anchor +root=nexora-demo.kw.` SOA and www A `; fully validated`; AXFR with TSIG 34 records TSIG-signed, without key `; Transfer failed.`; signed nsupdate exit 0 and `smoke-upd` A 192.0.2.99 in DNS (validated) and API, unsigned `update failed: REFUSED` (test record removed afterwards); `bind-demo.kw.` serial 2026091401, `last_error ""`, `www.bind-demo.kw.` `aa` 192.0.2.53, TSIG AXFR 7 lines.
- `gofmt -l e2e/` clean, `go vet ./e2e/` clean, `shellcheck deploy/kw/bootstrap.sh scripts/kw-deploy.sh` clean, `kubectl apply --dry-run=server -f deploy/kw/bind-primary.yaml` ok.

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in ed232b2
