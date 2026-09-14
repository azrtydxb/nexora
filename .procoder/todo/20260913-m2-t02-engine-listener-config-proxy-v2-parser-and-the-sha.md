# M2 Task 2: Engine listener config, PROXY v2 parser and the shared stream server

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 2 ("Engine listener config, PROXY v2 parser and the shared stream server") of milestone M2 exactly as specified in
`.procoder/plans/nexora-v1-m2.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 2 in `.procoder/plans/nexora-v1-m2.md` is done as written (deviations recorded in the plan first)
- [x] `TestEDNSTruncationTCP` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed (fmt/clippy clean; commit left to the lead)

## Evidence

- Red: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib server::proxy'` -> compile FAIL, `cannot find function read_proxy_v2` / `serve_dns_stream` / `Transport::Dot`.
- Green: `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine'` -> all suites `test result: ok` (lib 31 passed incl. 4 `server::proxy`, 2 `server::stream`, `bootstrap_rejects_proxy_without_trusted_cidrs`; `cache_hit_path_does_not_allocate ... ok`).
- `cargo clippy --locked -p nexora-engine --all-targets -- -D warnings` -> exit 0; `cargo fmt -- --check` clean.
- `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=bin go test -count=1 -v ./e2e/ -run "TestForward|TestUpstream|TestDedup|TestEDNS|TestInvalidSnapshot"'` -> PASS TestInvalidSnapshotRejected, TestEDNSTruncationTCP, TestForwardCacheTTL, TestUpstreamFailover, TestDedupAllWaitersAnswered; `ok github.com/piwi3910/nexora/e2e 11.329s`. Also PASS TestBlocklistSubscription, TestMgmtStatelessHA.
- Deviations recorded in the plan: tokio `test-util` dev feature; `Arc<Semaphore>` and idle-bounded writer/permit wait in `serve_dns_stream`; one `Rc<WorkerAnswerer>` per TCP listener.

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in 33609a6
