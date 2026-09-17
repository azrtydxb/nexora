# CP-01 bounded bootstrap convergence slice

The existing bootstrap returns immediately after its configuration writes. The
parent then runs the strict `bootstrapped` fleet/management/DNS gate. A fresh,
connected engine can still report `current`, applied 1233 and target 1233 while
`config-versions?limit=1` already reports 1234. The strict gate correctly rejects
this observation; the premature handoff is at the bootstrap boundary. This does
not establish a root cause for any longer-lived control-plane propagation bug.

`bootstrap.sh` now pins connected UUID/node/group tuples before configuration
writes, then invokes a dedicated read-only wait after the existing writes. The
wait pins the latest published version once and requires every pinned identity
to acknowledge that exact version with a fresh heartbeat (30 seconds old maximum,
5 seconds future maximum). Only healthy current/behind version lag is polled.
Published target movement in either direction fails instead of resetting the
clock or chasing a newer version. Rejection, persistence errors, stale/invalid
heartbeats, disconnection, identity changes, ahead versions, invalid responses,
HTTP failures, redirects, DNS resolution failures and guard denial fail closed.
No publish endpoint, DNS probe retry, lock release or recovery mutation is added.

The default polling budget is 120 seconds; `NEXORA_KW_BOOTSTRAP_WAIT_SECONDS` can
only select 1..120. Each API read is limited to the remaining budget or 5 seconds,
whichever is smaller. Budget is checked before and after ownership checks. The
existing guard has its own 20-second request timeout, so one in-flight guard can
extend wall time beyond the polling budget by at most that timeout (plus local
process scheduling). The final sample is checked for freshness again after the
last version and ownership reads. SIGINT/SIGTERM exit rather than proceeding to
another bootstrap action. The parent remains responsible for persisted workload
bindings, distinct physical nodes, exact endpoints, source IP/port invariants,
DNS monitoring/cancellation and the unchanged final strict health gate.

Validation in this worktree:

- `go test ./deploy/kwrollout -run '^TestBootstrap(BoundaryCommands|LagStillFailsStrictManagementProbe)$' -count=1`
  passes. The command fixture executes the real Bash/JQ pin and wait against a
  fake curl executable, including 1233/1234 target lag, applied-only lag, timeout,
  rejection, persistence error, stale/future/missing/malformed timestamps,
  RFC3339 offsets and whole-second UTC, identity/disconnection changes, target
  movement, redirects, DNS failure and guard refusal. It asserts no repeat read
  for permanent failures. The strict management probe regression uses an in-memory
  HTTP transport and still rejects exactly applied=1233 target=1233 expected=1234.
- `bash -n deploy/kw/bootstrap.sh deploy/kwrollout/bootstrap-convergence.sh` passes.
- `shellcheck deploy/kw/bootstrap.sh deploy/kwrollout/bootstrap-convergence.sh` passes.
- `git diff --check` passes.
- `go test ./deploy/kwrollout -run 'TestBootstrap' -count=1` was attempted and is
  NOT green: the sandbox refuses Unix-socket and TCP binds. The full-bootstrap
  fixture retains real TLS curl requests, the real Unix ownership guard, and a
  fake kubectl command, including context cancellation and mutation counts.
  Parent must run `go test ./deploy/kwrollout -run 'TestBootstrap' -count=1` on
  Linux, followed by its normal race/integration suites. No skips were added.
- Procoder report tools are unavailable; procoder was not run via shell as
  instructed. Its check/test/review gates are therefore unverified here.

Review and adversarial testing caught an initial timestamp parser that accepted
only UTC and then the malformed-timestamp empty-output case; both are covered by
passing fixtures. A synthetic three-second success-fixture budget also exposed
process-startup sensitivity under concurrent test runs; success fixtures now use
ten seconds while still requiring exactly one poll for lag, and the timeout
fixture retains its explicit short budget. The production maximum is unchanged.

No commits, task/sprint closures, cluster execution, secret regeneration, new
dependencies, core management probe/control-server changes or golden changes.
This slice adds an executable bootstrap waiting boundary; it is not a deployment,
a control-server propagation fix, or HA/failover acceptance evidence.
