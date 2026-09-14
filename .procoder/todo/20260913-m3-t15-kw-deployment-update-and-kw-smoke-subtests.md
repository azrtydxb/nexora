# M3 Task 15: kw deployment update and kw smoke subtests

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 15 ("kw deployment update and kw smoke subtests") of milestone M3 exactly as specified in
`.procoder/plans/nexora-v1-m3.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 15 in `.procoder/plans/nexora-v1-m3.md` is done as written (deviations recorded in the plan first)
- [x] `TestKwSmokeM3` passes in the dev pod (`scripts/dev-exec.sh`) — as built, the M3 subtests are part of `TestKwSmoke` (plan updated)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Images `nexora-engine:sha-b643000`, `nexora-mgmt:sha-b643000` built from a clean `git worktree` of b643000 with `scripts/build-image.sh`; worktree removed.
- `scripts/kw-deploy.sh --skip-build --tag sha-b643000`: `deployment "nexora-mgmt" successfully rolled out`, `daemon set "nexora-engine" successfully rolled out`, `NEXORA_KW_DNS_ADDR=192.168.10.136:53`; `nexora-kek` created (32 bytes decoded); rerun `deploy/kw/bootstrap.sh` printed nothing (idempotent).
- Egress probe from the kw dev pod: `dig +norec @198.41.0.4 example.com` and `dig +norec @192.0.2.1 example.com` return recursive answers (flags `qr ra`/`qr aa ra`) — outbound 53 is redirected, so kw runs forward mode with `validate_forwarded`.
- `scripts/dev-exec.sh env NEXORA_KW_... go test -count=1 -v -run 'TestKwSmoke$' ./e2e/` (twice): `--- PASS: TestKwSmoke (9.40s)`; all M1/M2 subtests PASS, `dnssec-forwarded`, `trust-anchors`, `rpz`, `m3-metrics` PASS, `recursion` SKIP (redirect detected).
- `delv @192.168.10.136 dnssec-failed.org`: `;; resolution failed: failure` (dig: SERVFAIL, `EDE: 9 (DNSKEY Missing)`; with +cd NOERROR); `dig @192.168.10.136 +dnssec cloudflare.com`: `flags: qr rd ra ad`, NOERROR.

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in 3af7663
