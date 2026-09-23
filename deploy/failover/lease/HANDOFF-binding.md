# Lease binding candidate follow-up

Scope: only `deploy/failover/lease/` in the same isolated
`/tmp/nexora-wave3/lease` workspace. This is an unactivated binding candidate,
not production HA acceptance or closure of the parent's task. No cluster/node
execution, credentials, link/route changes, namespace entry, frontend activation,
actual fence-driver launch or kernel attachment was performed here. Adjacent
`fence/driver.c` and `gate.h` were read-only protocol references. Parent kernel
proof uses **classic TC**, with foreign TCX hook checks, not TCX attachment.

## Import deltas from the previous six-file snapshot

Add these files:

- `driver.go`: explicit launch options, private child-pipe protocol, exact bounded
  READY/TICKET/ARMED/DENIED parsing, immutable outstanding ticket, terminal poison,
  cancellation/deadlines, kill and bounded reap/Close. No public arbitrary-command
  or injected pipe/process constructor; the fake process entry exists only in tests.
- `clock.go`: sticky clock failure state and strict zero-offset parsing.
- `binding_linux.go`: trusted absolute-path/UID/assignment validation; inherited
  owned netns only; Linux absolute CLOCK_BOOTTIME; boot/time namespace/offset
  validation; paired gate/clock and caller/child identity checks before and after
  each command. No elapsed-time or wall-clock substitution.
- `binding_unsupported.go`: explicit refusal on other platforms.
- `driver_test.go`, `binding_linux_test.go`, `binding_unsupported_test.go`: real
  child pipes with test-only fixture, parser/state/timeout/cleanup adversaries,
  unsupported host checks and Linux validation tests for the parent to execute.
- `HANDOFF-binding.md` and `lease-binding.result`: this separate follow-up record.

Modify only these existing files:

- `controller.go`: Gate comment plus one constructor guard: a real DriverGate must
  use its exact nonnil paired BootClock and may be claimed by only one controller.
  No changes to Step, quarantine, CAS count, DENY handling or ticket authorization.
- `README.md`: concrete launch/clock contracts, ownership/UID/namespace scope,
  deadline and trust limitations, classic TC clarification and validation commands.

Do **not** import/overwrite `http_test.go` from this workspace. It remains exactly
as found at follow-up start, SHA-256
`bbe90e6a06d66b9a81957f0ecbba1d1066fb78e13704cd8e2b34d2ab5a39d6cd`.
This local copy still has the old 40 ms sleep / 10 ms client timeout, so the
parent's reported 2 s sleep / 1 s timeout / elapsed <= 2 s / calls == 1 fix is
**not present here**. Preserve that parent delta unchanged during import. The
reported parent Linux race pass (94.461 s) and vet are parent evidence, not a run
performed here. `http.go`, `controller_test.go`, previous `HANDOFF.md`, root
`go.mod` and adjacent fence files were not modified. No commits were made.

## Binding and shutdown contract

Call `LaunchDriver` only with `Enabled` explicitly set and fully supplied trusted
paths, interface/alias, Unix UID, boot ID, net/time namespace identities and
positive I/O/reap bounds. All files/ancestors must satisfy the path checks;
`/tmp` is intentionally unsuitable for production launch. An administrator must
provision the exclusively owned namespace and down veth externally. The driver
checks the frontend and creates its own fresh map/attachment; this package never
enters namespaces, runs host-network commands, creates a Lease, or activates a
frontend. The library does not establish that an asserted namespace is owned.
Unix UID is distinct from Kubernetes Lease UID, which remains first-GET pinned.
Inherited groups/capabilities and all bypass prevention remain external.

Pass the returned gate and clock to `New` exactly once. Keep the original clock;
never replace it to reset a terminal error. Parent code should stop its foreground
loop on any Step error and call `gate.Close` with a separate bounded shutdown
context. Any operation/parse/uncertainty or shutdown error must produce a nonzero
lab exit; there is no executable here to silently retry or hide it. Close makes
one bounded DENY attempt, closes descriptors, kills the child and waits up to
ReapTimeout. A poisoned connection cannot send a trustworthy later DENY. Failed
DENY/Close never establishes inactivity: rely on kernel expiry and explicit parent
teardown. No program detach, qdisc delete, map pin/reuse or namespace cleanup occurs.
A successful DENY is an operation acknowledgement under the trusted-driver
contract, not independent packet proof or node eligibility.

The fixed driver protocol has no sequence IDs or authenticated acknowledgements.
One outstanding operation/ticket, exact parsing and permanent poison after any
ambiguity prevent recovery via late responses or recapturing a fresh window for
an old CAS. They cannot prove that a malicious driver did not forge a syntactically
valid reply at the expected time. The driver/object and administrative scope must
remain trusted and immutable. Procfs checks are not atomic attestation against a
malicious administrator. Clocks still require the README's external rate, drain,
VM pause/migration and no externally-visible forwarding during frozen time
assumptions. Offset/namespace drift and regression are refused; oscillator-rate
drift is not independently measured. Local syscall/scheduler progress is assumed
for userspace bounds; the kernel expiry remains essential when userspace freezes.

## Four validation passes

Host: Go 1.27.1, darwin/arm64. Cache: `/tmp/nexora-lease-gocache`.

1. Expanded deterministic/controller plus real child-pipe tests: PASS, 1.690 s.
   Covers round trips, startup timeout, actual blocked read/write deadlines,
   cancellation without deadline, EOF, partial/oversized/malformed responses,
   canonical numbers/overflow, replayed tickets, mismatched/double ARM, ARM after
   DENY, double CAPTURE, lock timeout, identity rejection, single-controller
   pairing, concurrent poison and reap timeout reporting. Zero-offset parser and
   unsupported-platform refusal execute locally. The initial fixture helper name
   collided with an existing test helper; renamed only the new helper.
2. Existing TLS/HTTP tests, unchanged: PASS, 60.415 s. This uses the local old
   slow-test fixture; it does not supersede the parent's corrected test.
3. Full package race: PASS, 88.219 s. Local vet PASS. Linux/amd64 test binary
   cross-compilation PASS at `/tmp/lease-binding-linux.test` (not executed here).
4. Local adversarial source/test review (no sub-agents) checked stale authorization,
   overflow/parser bounds, callback cancellation races, poison/Wait ordering,
   pre-write ticket matching, exact clock pairing, namespace/path trust scope and
   unsupported fallback refusal. Tightened identity verification to run after
   responses too; added late-ARM/no-recapture and post-reply drift tests. Expanded
   targeted race tests PASS, 3.242 s. Vet and Linux cross-compilation PASS again.
   Final full race rerun: PASS, 88.398 s. All package Go files are gofmt-clean.
   Preserved-file SHA-256 values matched the follow-up starting snapshot.

Tests never launch the real privileged driver. Linux-specific executable tests,
real Linux pipe deadline/cancellation proof, actual driver startup/refusals,
namespace/clock replacement, pause/death/expiry/packet behavior, separate management
traffic and API server CAS remain **pending parent lab validation**. Linux tests
must not silently count a skipped clock-domain test as proof. Parent should run
all four README passes using its preserved HTTP fix, then perform kernel binding
and Kubernetes CAS integration in its owned environment. No production HA claim
or parent task closure follows from this handoff.
