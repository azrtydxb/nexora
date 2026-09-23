# Wave 3 reviewed implementation and Linux evidence

Work in progress on baseline `f9c1653`; no milestone, sprint, deployment or release
closure. Raw logs and isolated worktrees are under `/tmp/nexora-wave3/`. Parent
alone operated the shared toolbox and serial host experiments. Original dirty M8
and unfinished historical merge worktrees remain preserved.

## Review defects and corrections

1. Fresh ODoH seeds could reach silent superseded streams. The follow-up found the
   same pattern in DNS TLS private material and both TSIG producers. All four now
   authorize bounded in-memory enqueue against persisted connection session and
   engine/certificate revocation under database row locks. Load/decrypt occurs
   before locking; transport Send occurs independently afterward. No nested pool
   acquisition or automatic retry of the enqueue side effect. Tests cover reused
   instance IDs, delayed registration/load, blocked old sends, revocation and a
   single-slot pool. Already queued/in-flight material is **not** retracted; this
   is not a universal transport-send fence. A second review found database-lock
   loss during process suspension could precede late enqueue, and a transient TLS
   offer failure had no same-stream recovery. The corrected queues carry prepared
   envelopes that cannot reach Send until commit is positively acknowledged;
   failed/ambiguous commits discard. TLS reoffers latest unacknowledged material
   through a session-specific 30-second retry worker with fresh authorization.
   Full Linux control race passed (147.283s), including 16 backend-termination
   cases and same-stream TLS recovery (32.61s). A separate deliberate mutant that
   opened the barrier on failed commit failed all 16 cases with actual unwanted
   delivery (`commitbarrier-negative-linux.log`). The original retry fixture used
   an invalid revocation reason; its SQL-constraint failure is preserved in
   `commitbarrier-linux.log`, corrected to the real `revoked` enum without
   weakening the test. Positive evidence: `commitbarrier-product-linux.log`.
   A third independent review found two further P2 defects: replacing pending
   TSIG A with failed-commit B leaves digest A and can suppress recovery to A;
   pre-existing handler shutdown waits on transport Send before returning to
   cancel that RPC. Both have concrete follow-up work in the isolated
   `control-final` tree. Passing ordinary suites does not close these findings.
2. The real-engine verifier compared captures and query logs independently. New
   probes retain actual local/remote socket tuples for every transport; the
   verifier joins question/client/group/transport to captured backend and that
   backend's OTLP. Swapped backend and altered-port regressions reject the old
   implementation. Local socket race tests passed (2.319s); 24 optimized-Python
   crosshost tests passed. Historical `57f032b7` is insufficient attribution proof.
3. Multicast acceptance initially rejected valid DNS owner-case randomization and
   iproute2's double-space IPv4 membership output. Compare DNS case-insensitively
   while retaining exact RCODE, answer count, type, TTL and address; parse exact
   membership tokens with whitespace/near-match regressions. Gateway and reflector
   Linux product tests passed, including IPv4/IPv6, reload, disable, cache and loop
   behavior: `mdns-product-reviewed-linux.log`, 25.885s. Original failures retained.
4. Initial real GUI suite had 11 failures. Reviewed fixes preserve strict labels,
   authorization/CAS/status assertions and mobile overflow checks: exact accessible
   label lookup, valid JSON rotation request, independent stale-edit setup, safe
   response handling across navigation and narrow-screen layout. Capacity browser
   seed now runs actual sampling/model arithmetic with dedicated telemetry and
   history instead of a forecast overwritten by new real sampling. Seven local
   M8 unit tests passed. The next full run improved to 82/83 browser cases but
   still found narrow-screen overflow in the active-rollout status row. That row
   now wraps; the complete real GUI rerun passed all 83 screen cases plus setup
   (335.169s). Overflow checks remain strict and now include non-sensitive layout
   diagnostics. Playwright invocation output directories are independent, so later
   successful tests cannot erase earlier failure evidence. A private ancestor
   protects retained traces even when Playwright recreates its output directory;
   artifact-isolation tests passed locally and on Linux.

## Corrected crosshost engine proof

Fresh run **`fb374f92` PASS**: both supervisors exited zero and the corrected
optimized-Python verifier accepted 80 exact bidirectional flows, 40/group, both
clients and both backends across UDP/TCP DNS, DoT, HTTP/2 DoH and DoQ. Original
client source selected `/32` rewrite policy and was joined to its actual backend
and persisted query record. Signed large TXT/RRSIG stream replies and bounded UDP
truncation passed, without retry/fallback. Independent management attachments
remained healthy. Evidence: `crosshost-joined-*.log`,
`evidence/nexora-crosshost-fb374f92-{left,right}`, `verified-joined/`.

The engine byte hash is recorded in each manifest; this used the existing Linux
baseline engine, not a combined M8 release. This proves standalone attachment,
not enrolled management-stream continuity, chain-of-trust DNSSEC validation,
general PMTU/fragmentation, reused-session continuity or frontend HA. Temporary
lab TLS keys were removed from both hosts and laptop, and owned namespaces were
removed. Prior strengthened controls `fb04bedb` (exact SNAT source mismatches) and
`1119d895` (exact three assigned advertiser MACs) remain separate evidence.

## Kernel expiry candidate

`deploy/failover/fence/` built with Linux clang 19 and privately extracted libbpf
1.5 development files. Policy and six contract tests passed; actual classic-TC
smoke (with TCX checks rejecting foreign attachments) on
worker21 kernel `6.12.58-current-rockchip64` passed:

- Missing/unsupported gate objects and foreign ownership refused.
- Empty authorization denied ARP, gratuitous ARP and UDP data at the peer.
- A live absolute CLOCK_BOOTTIME window admitted those frames.
- SIGSTOP and owner exit did not defeat kernel expiry; old/delayed tickets were
  rejected, explicit denial worked and the separate management link stayed live.
- Owned namespace cleanup passed. Raw `kernel-fence-smoke-linux.log` and
  `fence-linux-build.log` preserve the commands/results.

This is a bounded egress-hook primitive, not an election/lease implementation or
proof covering every possible Linux bypass/transmit path. The production binding,
privilege separation, all-writer serialization and frontend HA remain open.

## Owned platform staging candidate

`deploy/failover/platform/` now passes actual isolated Linux stage, repeat,
process-restart recovery and cleanup: **25 mutations**, `active: false`, recorded
in `platform-stage-fixed-linux.log`. Fifteen local fault-boundary and ownership
unit tests passed. No frontend was activated.

Preserved failures uncovered missing host prerequisites and a real kernel
assumption: `ip link add ... alias ... type dummy` silently produced an unmarked
link. The reconciler must not claim that link. Its corrected contract requires a
preprovisioned, owned DOWN dummy; the namespace creator verifies alias before
handover. Reconciliation never creates, marks or deletes that baseline link and
only changes exact owned address/routes/rules/IPVS state. Missing/foreign baseline
links fail closed, including cleanup. The old unit fake's alias-on-create behavior
was corrected rather than weakening kernel ownership checks.

No host package install was performed. A private mount-namespace `/usr/sbin`
overlay supplied the previously staged ipvsadm. Parent explicitly loaded the
`dummy` module with `numdummies=0`; host dummy-link inventory remained empty. This
kernel prerequisite is the only host-level setup change in these two candidate
experiments. No serving VIP, route, CNI, trust, data or deployment-lock change.
Paired preflights after engine/kernel work and after corrected staging passed in
`preflight-after-fence-reviewed.log` and `preflight-after-stage.log`; an earlier
helper-path validation failure is preserved separately. Production fence binding, provisioning/anti-spoofing,
independent engine management attachment and active lifecycle are not established
by this detached stage-only test.

## Lease ownership and driver-binding candidates

`deploy/failover/lease/` implements strict HTTPS Kubernetes Lease UID/resourceVersion
CAS, first-acquisition quarantine, per-incarnation holder and changing nonce,
pre-request absolute kernel tickets, and acknowledged-CAS-only ARM. Wall-clock
lease expiry is not authority. Real TLS runs over in-memory streams in unit tests;
this is not API-server or packet evidence. Corrected Linux race passed (94.461s)
and vet passed. The original slow-response test allowed only 10 ms for its real
TLS handshake and failed before reaching the handler; its corrected one-second
request still requires exactly one handler call and a bounded failure.

Concrete private-pipe driver and Linux CLOCK_BOOTTIME bindings are now integrated,
with explicit opt-in, trusted absolute paths, caller/child namespace/boot checks,
exact protocol parsing, operation timeouts, terminal poisoning and bounded reap.
Parent review added typed-nil driver rejection. Initial Linux binding race passed
(101.462s) but skipped its positive clock test: Linux exposes `timens_offsets`
under `/proc/self`, not `/proc/thread-self`. The corrected reader verifies that
leader/current-thread current and future time namespaces all match before reading
the process offset file. The strengthened test compares real CLOCK_BOOTTIME and
fails unexpected constructor errors rather than skipping them. Full Linux race
(101.368s) and vet passed with no skipped clock test
(`lease-binding-fixed-linux.log`); restoring the invalid path in an isolated
negative-control tree makes that test fail (`clock-negative-linux.log`).
Actual driver/Kubernetes API/frontend integration, proven clock/drain
margins, privilege separation and cross-host owner-failure acceptance remain open.

## Acceptance still open

Supported combined management/race passed all packages except two blocked on a
missing backend-tool PATH, including API (473.248s), control (185.604s), deployment
and probe packages. With the existing private tools explicitly on PATH, real
query-log conformance (51.458s) and harness race (22.243s) passed. Preserve the first
invocation's prerequisite failures in `final-product-linux.log`; the corrected
pipeline is `final-product-reviewed-linux.log`; that complete E2E attempt was RED
(1628.471s) solely at the remaining mobile GUI assertion. The corrected barrier/UI
pipeline is `commitbarrier-product-linux.log`: control and harness race passed,
GUI type/lint/build passed, and the complete GUI shard passed. The second shard
ran every remaining E2E test with only that already-passed GUI test excluded and
passed (1298.388s, pipeline exit 0). Together the shards cover the full enabled
package on that commit-barrier/UI candidate; live opt-in skips remain unaccepted.
The product tree is no longer running that pipeline. The subsequent control review
findings above still block completion and require new verification after fixes.
Seven
final local M8 web unit tests passed (286ms). The complete operator race/envtest
suite also passed again using Kubernetes 1.34.1 assets (`operator-final-linux.log`).
Earlier
protocol passes include real ODoH/BIND catalogs (32.055s), strict management
recovery (~681ms) and ZONEMD/RPZ cases. Earlier full combined Rust all-targets passed
with isolated `/work/target-wave3`. These do not substitute for the pending full
combined product outcome. Laptop Procoder test diagnostics remain red (latest
278 Go failures and Darwin Rust `libc::mmsghdr` compilation failure).

Remaining approved work includes authoritative eligibility publication/runtime
wiring; real platform/fence/election integration; health, drain, withdrawal and
safe tombstones; audited failover API/UI; managed crosshost attachment and full
loss/partition/session matrix; guarded frontend migration; unattended bootstrap;
and committed release deployment/full live acceptance. Historical abrupt member
failover remains RED. Neither the kernel smoke nor the staged adapter closes it.
