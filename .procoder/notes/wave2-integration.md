# Wave 2 integration evidence — 2026-09-17

Parent baseline `504a006`. Five isolated agents worked under `/tmp/nexora-wave2`;
the operator agent exhausted its Codex quota after writing its implementation.
Parent preserved and completed integration/testing directly. This is a code and
laboratory checkpoint, **not completion of all milestones or live deployment**.

## Integrated scope

- Imported M10 from `d0009ce`: ClickHouse/Loki adapters, conformance suite, real
  collector fixtures, product/browser tests, settings/contracts, chart and guarded
  support-resource/acceptance wiring. Preserved current paired deployment guards,
  bootstrap wait, TLS/identity state, AI checks and raw Linux Helm goldens.
  Redirects cannot forward backend credentials. Missing ClickHouse credentials
  fail closed on lookup errors, an existing StatefulSet **or retained data PVC**;
  parent added orphaned-PVC and PVC-lookup-error regressions.
- Extended operator query-log fields, validation, generated CRDs/deepcopy and
  render/persistence tests to support ClickHouse/Loki without inline credentials.
- Extended stream ownership fencing to transactional NOTIFY scheduling, UPDATE
  mutations and log replies; TLS fanout results apply after successful persistence
  and only to the matching registration. Tests exercise actual adapters across
  different/reused management instance IDs and single-connection pools. Already
  committed jobs and queued outbound messages are not retroactively revoked;
  mixed old/new binaries still cannot enforce the ownership invariant.
- Added opt-in cross-host userspace-carried VXLAN/IPVS DR fixture, two isolated
  groups, packet verification, ownership cleanup and negative-control modes.
  This uses the echo probe, **not a real Nexora engine/policy attachment**.
- Reviewed/hardened the historical opt-in member failure harness and preserved its
  original RED evidence. No new live member deletion was executed.
- Refreshed gap inventory in `completion-wave2.md` and sprint baseline. M8's dirty
  worktree and the older unfinished `nexora-merge` worktree remain untouched.
  M10 code was transported as a reviewed patch; its branch ancestry is not merged.

## Supported verification

All logs below are under `/tmp/nexora-wave2/`.

| Run                                                                     | Result / evidence                                                                                                                                                                                       |
| ----------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Baseline `504a006` full Linux `go test ./e2e/... -count=1 -timeout=30m` | PASS, product package 1389.569s, exit 0; `product-baseline.log` and `.exit`. This is pre-wave2 evidence, not a combined full-suite claim.                                                               |
| Integrated Linux race packages                                          | PASS: control 83.845s, dynupdate 15.690s, xfrin 17.816s, zone 44.858s, querylog 26.628s, config 1.039s, kwrollout 73.337s, deploytest 9.848s; `integrated-linux.log`.                                   |
| Real backend conformance                                                | PASS 49.743s: ClickHouse 26.8.4.11, Loki 3.6.7 and OpenSearch via collector ingestion; `conformance-linux.log`, exit 0. Tools staged in `/work/tools/nexora-m10`, no toolbox/cluster release installed. |
| Integrated operator                                                     | PASS full `make operator-test`, real envtest/race; API 29.890s, enginegroup 119.726s, installation 91.255s, render 4.407s; `operator-linux.log`, exit 0.                                                |
| Integrated query-log product/browser                                    | PASS 71.025s across builtin/OpenSearch/ClickHouse/Loki, including real managed engines, API, Playwright, partial-name, multi-value and dashboard top; `querylog-product-reviewed-linux.log`.            |
| Integrated management and control product                               | Full `go test ./mgmt/... -count=1` and real `TestMgmtStatelessHA`, `TestSecondaryAndDynamicUpdate`, `TestEngineLogsFromRealEngine` passed; `full-mgmt-control-product-linux.log`.                       |
| Laptop Procoder suite                                                   | RED: 241 Go failures; Rust `libc::mmsghdr` compile error. Do not turn this into a pass based on supported scoped runs.                                                                                  |

Gate review also made the toolbox image default to `USER dev`, with the existing
root requirement explicitly confined to the development pod's `runAsUser: 0`.
A manifest regression preserves that distinction; no image was rebuilt/deployed.
The scheduler LISTEN statement is now a compile-time constant. Final Linux xfrin,
deploy/chart tests and eight cross-host unit tests passed after these changes:
`final-polish-linux.log`.

The first isolated web-build attempt refused pnpm module-directory removal without
TTY. It made no product-pass claim. Parent then used the already installed shared
dependencies without purging them, built the new web assets/management binary in
`/work/nexora-wave2`, and ran the successful product test. Baseline work in
`/work/nexora` was kept separate throughout.

## Cross-host results and preserved failures

Hosts: kw workers 21/22, `192.168.10.104` / `.105`. No host interfaces/routes,
Cilium settings, serving VIPs, persistent engine state or retained lock changed.
Tools were extracted/copied beneath `/tmp/nexora-wave2-lab`, not installed as host
services. New links, addresses, IPVS and ethtool changes exist only in owned lab
namespaces; host exposure is two explicit UDP sockets.

1. **Setup RED, run `1149b19e`:** Linux VXLAN's wildcard UDP listener collided with
   the proposed same-namespace loopback relay. Worker22's tcpdump decoding also
   assumed a missing `tcpdump` user. All owned namespaces were removed. Preserve
   `crosshost-setup-{failure,left,right}.log` and remote evidence directories.
   Fix: separate relay namespace plus isolated `198.19.0.0/30` veth; private
   captures/decoding explicitly use `-Z root`.
2. **UDP RED, run `3c1ab31a`:** first left-client DNS probe timed out. Remote backend
   capture shows the request arrived with bad UDP checksum (`0x8cce -> 0x6596`).
   Userspace forwarding lost CHECKSUM_PARTIAL metadata. Stopped without retry.
   Fix: disable checksum/segmentation offloads only on owned endpoint veths using
   ethtool. Evidence: `crosshost-checksum-{run,left,right}.log`, remote PCAPs.
3. **Corrected DR PASS, run `5b307575`:** both supervisors exited zero. Verifier
   under Python `-O` accepted **20 exact bidirectional tuples per group**, two
   clients and both backends over UDP/TCP DNS, DoT, HTTP/2 DoH and DoQ; **40 total**.
   Evidence: local `evidence-left/`, `evidence-right/`, `crosshost-verified/`,
   `crosshost-verification.log`; remote `/tmp/nexora-crosshost-5b307575-{left,right}`.
   Cleanup files report no errors; no `fx-*` namespace remained.
4. **SNAT control setup RED, run `2a179c8b`:** worker22 lacked `iptables`; cancelled
   peer and removed all owned namespaces. No negative-control pass claimed;
   duplicate-advertisement control was not run. Added prerequisite checks before
   resource creation and a regression for missing SNAT tooling. Preserve
   `crosshost-snat-run.log` and remote evidence.

Cancellation now terminates the probe process group, not merely its Python parent,
so an in-flight `ip netns exec` probe cannot outlive namespace cleanup. A unit
regression verifies group signalling. Current unit tests include upfront tool
checks; those last guard-only changes are not a second claim of live execution.

Both before/after paired preflights passed (`preflight-before.log`,
`preflight-after.log`); a final post-control preflight is recorded separately.
The DR pass proves this bounded fixture's tuples, not lossless sessions, real
engine source-policy attribution, large signed replies, production attachment,
negative-control acceptance, HA, partitions or fencing. FG-01 remains open.

## Remaining work, in dependency order

1. Finish cross-host controls and actual Nexora engine attachment/policy/log/MTU
   evidence; wire trusted inventory to the eligibility contract.
2. Implement scoped production adapter, independent redundant frontend ownership
   and **dataplane fencing**, lifecycle/drain/withdrawal/reservation semantics.
3. Complete audited failover API/GUI and real integration tests.
4. Review M8 committed/dirty work separately, resolve migration-version collisions
   with deployment history, and implement missing protocol/browser acceptance.
5. Run combined full product/release suites; deploy committed immutable images
   through existing serial guards, prove unattended bootstrap and all-replica
   ownership enforcement, then measure failover and strict live acceptance.
6. Close milestones/sprints only through human Procoder workflow with matching
   evidence. No formal closure, split-DNS implementation or CNI migration occurred.
