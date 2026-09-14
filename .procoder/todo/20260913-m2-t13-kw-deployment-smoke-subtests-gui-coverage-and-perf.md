# M2 Task 13: kw deployment, smoke subtests, GUI coverage and perf gate

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 13 ("kw deployment, smoke subtests, GUI coverage and perf gate") of milestone M2 exactly as specified in
`.procoder/plans/nexora-v1-m2.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed. Includes the kw follow-ups from `.procoder/notes/plan-review.md` "After M1 kw
deployment" (real client IPs, version stamping, HTTPS GUI with Secure cookies).

## Acceptance criteria

- [x] Every step of Task 13 in `.procoder/plans/nexora-v1-m2.md` is done as written (deviations recorded in the plan first)
- [x] `TestGUICoverage` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] `TestKWSmoke` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- Red: `TestKwSmoke` against the M1 deployment failed with `nexora-mgmt version is not stamped: "dev"`.
- Images: `scripts/build-image.sh -t sha-360b8cf` (engine, mgmt) from a clean `git archive HEAD` plus
  this task's changes; `scripts/kw-deploy.sh --skip-build --tag sha-360b8cf` ->
  `daemon set "nexora-engine" successfully rolled out`, `secret/nexora-dns-tls created`, 8 engines;
  `deploy/kw/bootstrap.sh` re-run -> exit 0.
- `TestKwSmoke` (dev pod, env from README) -> `--- PASS: TestKwSmoke (8.38s)` with all subtests
  (http-redirects-to-https, management-lb-has-no-cleartext-http, engine-version-stamped, dot,
  doh-get, doh-post, doq, query-log-records-client-address, per-client-policy-and-rewrites).
- Manual from the dev pod: `kdig @192.168.10.136 +tls/+https/+quic +tls-ca=/work/kw-ca.crt
+tls-hostname=dns.nexora.kw.local example.com` all answer; `curl http://nexora.kw.local/...` ->
  `308 https://nexora.kw.local/...`; `192.168.10.135:80` times out; health `"version":"sha-360b8cf"`;
  `nexora-engine --version` -> `nexora-engine sha-360b8cf`.
- External client address: `dig @192.168.10.136` from the laptop (192.168.10.220) -> query log
  `client: 192.168.10.220`.
- `TestGUICoverage` (clean copy, dev pod) -> `--- PASS: TestGUICoverage (49.25s)`.
- Perf gate (clean copy vs `git archive main`, 9 interleaved rounds, arm64 dev pod) ->
  `base median 164143 QPS, head median 167998 QPS, median per-round drop -6.08% (limit 5.00%): PASS`.
- `cargo test --locked -p nexora-engine --test hot_path_alloc cache_hit_path_does_not_allocate` ->
  `test result: ok. 1 passed`.
- `procoder check` over the changed files -> `10 clean, 0 unformatted`; `shellcheck` on the three
  scripts clean; `cargo fmt --check`, `cargo clippy -D warnings`, `go vet ./e2e/ ./mgmt/cmd/...` clean.

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in ca572be
