# Wave3 engine-lab handoff

Baseline `f9c1653`; changes confined to `/tmp/nexora-wave3/engine-lab/deploy/failoverlab`.
No commits, pushes, git-state mutations, task/sprint closure, SSH, cluster, shared
toolbox, dev-sync/dev-exec, external-worktree writes or existing trust access.
Only test-temp lab trust was generated. Existing rollout/CAS/OnDelete/wait=legacy
contracts are untouched. No CNI migration or split DNS.

## Implemented

The [runnable real-engine target](REAL_ENGINE.md) extends the cross-host namespace
fixture with a separately opted-in compiled standalone Nexora engine, disposable
protobuf configuration and certificate-bound lab trust. Actual source-selected
rewrite policy and OTLP query attribution are checked against original client
results. Five-transport packet evidence covers forty exact bidirectional flows
per group, both clients and both backends. The additional owned management veth
carries health, telemetry and a signed upstream. Large signed stream replies and
explicit UDP truncation are independently accepted without query retries/fallback.

Echo remains usable and is never reported as product proof. SNAT controls now
require an exact structured translated source; duplicate ARP controls require all
three known MACs. Upfront iputils/iptables checks, explicit source binding,
startup/finish health, exclusive evidence, telemetry-count checks and cleanup
error accumulation fail closed. Forced SIGKILL cleanup is red.

## Exact files

All paths below are relative to `deploy/failoverlab/`:

- `cmd/probe/main.go`: real probe commands, exact source binding on all transports,
  structured SNAT mismatch, signed response acceptance.
- `cmd/probe/engine.go` (new): lab-only trust generator/marker, protobuf/TOML
  preparation, isolated OTLP receiver and policy validation.
- `cmd/probe/signed.go` (new): disposable signed TXT upstream and pinned-signature,
  size/truncation validation.
- `cmd/probe/engine_test.go` (new): material isolation, policy mutations, corrupted
  and untrusted signatures, truncation, OTLP persistence/write failure, trust refusal.
- `crosshost/lab.py`: real engine opt-in, owned management attachment, lifecycle,
  collector supervision, controls and cleanup.
- `crosshost/verify_pair.py`: separate real engine evidence contract and provenance.
- `crosshost/test_engine.py` (new): synthetic evidence validator mutation tests;
  explicitly not live evidence.
- `crosshost/test_lab.py`: opt-in, control and cleanup regressions.
- `verify.py`: strict forty-flow engine capture coverage alongside unchanged
  twenty-flow echo requirements.
- `test_verify.py`: engine packet-count/source-port mutation regression.
- `crosshost/README.md`: links real mode and updates control/remaining-gate wording.
- `crosshost/REAL_ENGINE.md` (new): complete build/run/verify integration contract.
- `crosshost/HANDOFF.md` (new): this handoff.

No e2e file or product source was modified.

## Commands and supported results

Run from the assigned worktree; logs are under `/tmp/`:

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -O -m unittest discover -s deploy/failoverlab/crosshost -v
PYTHONDONTWRITEBYTECODE=1 python3 -O -m unittest discover -s deploy/failoverlab -v
GOCACHE=/tmp/nexora-wave3-engine-go-cache GOPROXY=off go test -race ./deploy/failoverlab/cmd/probe -count=1 -v
GOCACHE=/tmp/nexora-wave3-engine-go-cache GOPROXY=off go build -o /tmp/nexora-wave3-failover-probe ./deploy/failoverlab/cmd/probe
GOCACHE=/tmp/nexora-wave3-engine-go-cache GOPROXY=off procoder test deploy/failoverlab/cmd/probe
git diff --check
```

- Python: **17 cross-host + 8 tuple tests PASS**, including `python -O` so runtime
  guards cannot disappear with assertions. Logs `nexora-wave3-engine-python-crosshost.log`
  and `nexora-wave3-engine-python-tuples.log`.
- Go race: **7 tests PASS**, 1.692s; `nexora-wave3-engine-go-race.log`.
- Local probe build: exit zero. Go emitted a nonfatal module stat-cache write
  warning because `/Users/pascal/go/pkg/mod/cache/download/...` is sandbox read-only.
- Procoder test: Go passes; **Rust RED** on this macOS host with
  `error[E0425]: cannot find type mmsghdr in crate libc`. Logs
  `nexora-wave3-engine-procoder-test.log` and the final rerun
  `nexora-wave3-engine-procoder-test-final.log`. Never a full-suite green claim.
- Procoder check is run with the explicit changed/new file list, `GOCACHE` above
  and `GOPROXY=off`; see `nexora-wave3-engine-check-final.log`. Earlier invocation
  with the directory argument checked zero files (not useful evidence); an
  intermediate explicit-file run had a lint-cache access warning with default
  GOCACHE. Final explicit-file gate output is authoritative.
- `procoder review` is unsupported by the installed binary (usage output in
  `nexora-wave3-engine-review.log`); direct review and adversarial mutation tests
  were performed instead. No automated review pass is claimed.
- Formatting used `procoder format` and gofmt. A wrapper initially misread
  `already formatted` output and emptied three files; diff inspection caught it,
  files were restored, wrapper corrected, and the full expected test count rerun.
- A diagnostic `ps` command was denied by the sandbox; no process-list result was
  used as evidence. `git diff --check` passes.

## Parent execution and remaining work

Follow `REAL_ENGINE.md` to build a Linux engine and the probe, generate **new lab
trust only**, run both supervisors, probe hosts serially, finish both, and verify
stopped evidence. Both nodes need identical compiled engine bytes, root-owned
namespaces/IPVS/VXLAN capabilities and the documented carrier PMTU. Fresh echo
SNAT and duplicate runs are separate controls; preserve failures without retry.

**No Linux engine, namespace, cross-host or live negative-control execution was
performed here.** Actual protobuf acceptance by the compiled engine, real packet
paths and real OTLP integration remain to be verified by parent. Unit tests of
material/signatures/verifiers do not substitute for that run. Engine logs contain
client IP, not source port; ports come from paired captures. Signed payload
verification is not engine DNSSEC chain validation. Management attachment is
standalone telemetry/health, not enrolled control-stream continuity.

FG-01 remains open. Managed control attachment, ACL/rate-limit variants, multi-route
return, host attestation, sustained isolation, session reuse/failure continuity,
arbitrary PMTU/fragmentation, frontend HA/fencing and performance remain gates.
No adapter, rollout, task or sprint is closed by this work.
