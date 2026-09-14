# M4 Task 8: Zone transfers out (AXFR/IXFR, ACL + TSIG) and NOTIFY to secondaries

Status: closed 2026-09-14
Created: 2026-09-13

## Description

Implement Task 8 ("Zone transfers out (AXFR/IXFR, ACL + TSIG) and NOTIFY to secondaries") of milestone M4 exactly as specified in
`.procoder/plans/nexora-v1-m4.md`, following `docs/architecture.md`. Done means
the task's tests exist, fail before the change, pass after it, and the change
is committed.

## Acceptance criteria

- [x] Every step of Task 8 in `.procoder/plans/nexora-v1-m4.md` is done as written (deviations recorded in the plan first)
- [x] `TestAXFRIXFROut` passes in the dev pod (`scripts/dev-exec.sh`)
- [x] procoder gate clean over the changed files; work committed

## Evidence

- `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --lib xfr_tests'` → `test result: ok. 7 passed`; `loader_tests` → `4 passed`; `notify_out_tests` and the metrics family test pass in `make engine-test` (lib 176 passed, 1 ignored, all targets ok incl. hot_path_alloc).
- Tests were written together with the implementation; the red run was not observed separately.
- `scripts/dev-exec.sh 'go test ./e2e/harness/ -run TestNamed -count=1 -v'` → `PASS` (StartNamed via StartNamedConfig).
- `scripts/dev-exec.sh 'make e2e-build && go test ./e2e/ -run TestAXFRIXFROut -count=1 -v'` → `--- PASS: TestAXFRIXFROut (3.40s)` against the in-progress mgmt tree (zones + tsig-keys API present).
- `scripts/dev-exec.sh 'go test ./e2e/ -run "TestAXFRIXFROut|TestRPZ|TestAuthoritative" -count=1'` → `ok github.com/piwi3910/nexora/e2e 29.466s`.
- clippy `-D warnings` clean, `gofmt`/`go vet ./e2e/ ./e2e/harness/` clean. Not committed (lead commits).

Closing evidence (lead, 2026-09-14):

- gate/commit: commit gate passed on every commit for this task; todo last committed in 899ec87
