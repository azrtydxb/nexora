# M7 T20: The Compose example on novanas (#5)

Status: open
Created: 2026-09-14

## Description

Implements Task 20 of `.procoder/plans/nexora-m7-hardening.md` (spec `.procoder/specs/nexora-m7-hardening.md`, milestone M7 Hardening). Done when every step is done, its tests pass in the dev pod and the change is committed.

## Acceptance criteria

- [x] In `TestComposeExample`, append:
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./deploy/deploytest -run TestComposeExample'` and expect FAIL on the docs assertion.
- [x] Create `scripts/compose-verify.sh` (mode 0755). It runs the documented commands verbatim on the host, with the documented `.env` settings changed:
- [x] Run `scripts/compose-verify.sh piwi@192.168.10.211` from the laptop, after `images.yml` has pushed `sha-<7>` for the commit being verified.
- [x] Update `docs/operations.md`:
- [x] Run `scripts/dev-exec.sh 'go test -count=1 ./deploy/deploytest/...'` and expect PASS. Run `ssh piwi@192.168.10.211 'docker ps -a --format "{{.Names}}"; ls -d ~/nexora-compose-verify 2>&1'` and expect no `nexora-verify` containers and `No such file or directory`.

## Evidence

Not committed yet (the lead commits).

Images (amd64 present on Nexus). The m7 HEAD `3ac5883` has no image; `sha-1cc283d` (= origin/main, last green
`images.yml` run 34886744578) was used. `git diff --stat 1cc283d HEAD -- deploy docs mgmt/cmd` touches only
`docs/architecture.md`, so the compose files, the Compose doc sections and the mgmt CLI are the ones verified.

```
$ crane manifest --insecure 192.168.10.131:5000/azrtydxb/nexora-{engine,mgmt}:main | jq -c '[.manifests[].platform]'
[{"architecture":"arm64","os":"linux"},{"architecture":"amd64","os":"linux"}]   (both images)
```

Red (TDD), dev pod toolbox-m7:

```
$ NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test -count=1 ./deploy/deploytest -run TestComposeExample'
compose_test.go:94: docs/operations.md must describe the real Compose run (scripts/compose-verify.sh): <nil>
compose_test.go:97: scripts/compose-verify.sh: stat ../../scripts/compose-verify.sh: no such file or directory
FAIL	github.com/piwi3910/nexora/deploy/deploytest	0.014s
```

Runs on novanas (`NEXORA_TAG=sha-1cc283d scripts/compose-verify.sh piwi@192.168.10.211`), failures and fixes:

1. Run 1: `ok pull` … `ok dns-answers`, then exit 1 at `otel-logs`. Cause: with `NEXORA_QUERYLOG_BACKEND=builtin`
   (the compose default) engines send query logs to mgmt over mTLS (`engine/src/telemetry/otlp.rs`
   `querylog_to_management`), so the collector never logs `log records`; it receives OTLP metrics every 15 s and
   traces. Fix: the step greps the collector's `data points` lines; `docs/operations.md` "Install with Docker
   Compose" now says the bundled collector receives metrics and traces only with the built-in query log. Draft
   corrections found before running (from `mgmt/api/openapi.yaml` and the laptop): `crane pull --insecure`;
   record lists are `RecordPage` (`.items[]`).
2. Run 2: all 12 `ok`, exit 0; the trace showed two resume-rollouts publishes before `current`.
3. Run 3: all 12 `ok`, exit 0 (macOS tar printed `LIBARCHIVE.xattr.com.apple.provenance` warnings on the host; fix:
   `tar --no-xattrs`).
4. Run 4: `ok pull` … `ok restore`, then exit 1 silently at `restored-dns`. Cause: the script's recovery loop tested
   `status == "current"`, which the restored engine row shows until the engine reconnects, so it published nothing
   and the engine stayed ahead. The documented procedure ("publish until GET /api/v1/config-versions?limit=1 is
   above the engines' version") was right; the script now follows it literally: records the engine's
   `applied_version` before the restore, publishes until the newest config version exceeds it, then waits for a
   connected `current` engine above it. Added an `ERR` trap naming the failing line.
5. Runs 5, 6: all 12 `ok`, exit 0. Run 7 (with `bash -x`), exit 0:

```
ok pull ok up ok setup-token ok setup ok join-token ok engine-enrolled ok dns-answers ok otel-logs ok backup ok restore ok restored-dns ok cleanup
+ engine_version=5
+ '[' 4 -gt 5 ']'      -> resume-rollouts
+ '[' 5 -gt 5 ']'      -> resume-rollouts
+ '[' 6 -gt 5 ']'      -> engine current, dig @192.168.10.211 -p 15353 www.compose.test A = 192.0.2.10
```

No compose file needed changing; every documented command in "Install with Docker Compose" and "Plain PostgreSQL
and Compose" ran as written (only `.env` values changed, as documented).

`docs/operations.md`: "checked statically … and run for real … `scripts/compose-verify.sh user@host` (last verified on
novanas, x86_64, Docker 29.4.1, Compose v5.1.3)"; Compose `psql` variant in "Engines ahead of a restored database";
bullet for hosts that cannot pull (`crane pull --platform linux/<arch>` + `docker load`, `NEXORA_REGISTRY` = pulled
name); collector sentence from failure 1. `shellcheck scripts/compose-verify.sh` clean; formatter run on changed files.

Green, dev pod toolbox-m7:

```
$ NEXORA_DEV_DEPLOY=toolbox-m7 scripts/dev-exec.sh 'go test -count=1 ./deploy/deploytest/...'
ok  	github.com/piwi3910/nexora/deploy/deploytest	2.326s
--- PASS: TestComposeExample (0.00s)
```

Host left clean (after run 7):

```
$ ssh piwi@192.168.10.211 'docker ps -a --format "{{.Names}}"; ls -d ~/nexora-compose-verify 2>&1; docker volume ls -q | wc -l; docker network ls --format "{{.Name}}"; docker image ls -q | wc -l'
ls: cannot access '/home/piwi/nexora-compose-verify': No such file or directory
2                     (the two pre-existing anonymous volumes)
bridge host none
8                     (the eight images present before the first run)
```
