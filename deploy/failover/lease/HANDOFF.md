# FG-05 Lease candidate handoff

Workspace: `/tmp/nexora-wave3/lease` (macOS resolves to
`/private/tmp/nexora-wave3/lease`), base `f9c1653`. No commits made. All candidate
source/docs are new files in `deploy/failover/lease/`; the four supplied untracked
fence reference files were read only. Root go.mod was read and not changed.

## Delivered files

- `controller.go`: serialized injected authority/gate/boottime controller;
  quarantine, exact UID/RV CAS, per-incarnation holder, changing nonce/epoch,
  pre-request absolute ticket binding, verified acknowledgements, cancellation,
  DENY and terminal gate uncertainty.
- `http.go`: stdlib strict HTTPS Kubernetes Lease GET/PUT, fixed target,
  certificate validation, no redirect/proxy/replay/retry, bounded I/O, preserved
  metadata, duplicate/depth/UTF-8-safe JSON and exact CAS response validation.
- `controller_test.go`: deterministic adversarial schedules and gate failures.
- `http_test.go`: real TLS and HTTP transport over in-memory byte streams,
  certificate trust/hostname checks, strict body/status handling and committed
  PUT followed by lost response.
- `README.md`: protocol/provisioning contract, safety argument, clock/drain
  assumptions, exact commands, integration gaps and scope.
- `HANDOFF.md`: this evidence and next-work record; separate from final result.

## Four passes, in order

1. Controller tests passed (`go test ... -count=1`, initially 0.546s). The initial
   fake authority reused a manually advanced RV in one test; fixed the fixture
   to issue a distinct opaque RV on every successful write. No production check
   was weakened. Expanded deterministic suite after review passed in 0.675s.
2. Real TLS/client tests passed (35.667s). First TCP listener attempt failed on
   sandbox `bind: operation not permitted`. Tests were changed to use net.Pipe
   only as the byte transport, retaining real Go TLS/certificate/HTTP behavior.
   This is not a TCP or cluster test. No certificate bypass was added.
3. Full `go test -race ./deploy/failover/lease -count=1 -timeout=120s` passed
   (96.848s); `go vet ./deploy/failover/lease` passed. This host is Go 1.27.1,
   darwin/arm64. A later default-cache invocation was sandbox denied; subsequent
   commands use `GOCACHE=/tmp/nexora-lease-gocache`.
4. Independent read-only adversarial review (`/root/adversarial_review`) found no
   blocking implementation defect under the stated contracts. Review identified
   missing hostname/depth/reappearance assertions and an HTTP disconnect fixture
   that had not actually persisted state; all were fixed. Local review added
   context-expiry acknowledgement checks, unchanged-RV record-consistency checks
   and UTF-8 validation. Independent re-review found no new implementation issue
   and requested a stronger DENY-count assertion, now fixed. Targeted race test
   of cancellation acknowledgements passed (1.447s); vet passed again.

Final expanded `GOCACHE=/tmp/nexora-lease-gocache go test -race
./deploy/failover/lease -count=1 -timeout=240s` passed (96.688s). The final
DENY-count assertion change was separately verified by the targeted race test
above. `gofmt -l deploy/failover/lease/*.go` returned no paths; tracked diff checks
were clean. The candidate remains untracked and uncommitted for parent review.

## Coverage and evidence limits

Tests cover two contenders; renewals resetting quarantine; stale cached reads
with conflict; partitions; a paused/late driver; delayed grants; ambiguous
committed PUTs; deletion/recreation; different incarnations and reused holder
startup; stale owners; unchanged nonce/RV; expired/invalid tickets; clock
regression; overflow; malformed/oversized/redirected/error HTTP; TLS trust and
hostname rejection; no ARM after failed CAS; and gate-error terminal uncertainty.
These are protocol/transport tests with injected gate/clock, not packet evidence.

The parent reports real kernel paused/dead-owner expiry at five seconds with
independent management traffic unaffected. That result was not rerun here.
Actual Gate adapter, zero-offset Linux CLOCK_BOOTTIME adapter, Kubernetes API
server semantics/integration, actual frontend activation and supported-Linux
cross-host tests remain absent. No fallback or shell-out adapter exists.

Parent next steps: run the README commands on supported Linux; bind exactly one
real fence-driver/boot/namespace/frontend instance to each controller; prove
clock-rate and post-gate drain bounds for an explicit margin; exercise actual
API CAS conflicts, response loss, process pause/death, object lifecycle and
separate management traffic with kernel enforcement. Provisioning, all-writer
serialization/authorization, capability/bypass prevention, engine attestation
and eligibility require external implementation and acceptance evidence.

No live/SSH/shared-toolbox/devsync operations, actual secrets, production
activation, or HA acceptance are part of this work.
