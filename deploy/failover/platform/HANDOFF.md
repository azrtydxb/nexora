# FG-04 handoff

Base: `f9c1653ac1096ea17be62c587d86d8a2536d4872` in the assigned platform worktree.
All source/document changes are under `deploy/failover/platform/`. No commit,
push, SSH, cluster, shared toolbox, dev-sync, secrets, live change, trust
regeneration, production manifest modification or task closure was performed.

## Read inputs and evidence distinction

Read the approved failover specification, failover implementation/sprint plans,
Procoder skill and the parent main
`/Users/pascal/Development/nexora/deploy/failoverlab/crosshost/REAL_ENGINE.md`
read-only. The parent file still described execution as pending. The user's newer
report states real Nexora cross-host run `run57f032b7` passed its verifier with 80
exact flows across two groups, two clients, two backends per group and five
transports, real source policy/OTLP, signed large payload/truncation and independent
management attachment; echo/negative controls passed separately. That is
parent-supplied evidence, not rerun or independently verified by this lane, and
does not establish managed-control continuity, HA or acceptance of this adapter.

## Four passes

1. Implemented explicit manifest validation, injected runtime execution, actual
   fenced reconciliation and owned cleanup, read-only CLI and opt-in Linux runner.
2. Reviewer reread plus independent read-only reviewer identified unused-table
   errors, missing main/local route validation and return-route conflicts. Fixed
   all-table inspection, explicit kernel-route allowlists and conflict rejection.
   Frontend host routes use main, avoiding an unselected staging table.
3. Adversarial review added ambiguous command-success recovery and found unsafe
   direct child-runner entry and iproute2's named `bgp` representation for protocol 186. Fixed inherited network/mount descriptor and PID isolation checks before
   mutation, private propagation validation and protocol normalization. Added
   targeted regression tests. No Linux execution is implied by these tests.
4. Polish: formatted/linted Python, documented provisioning/fence boundaries,
   moved runner PASS after cleanup, checked no-mutation opt-in rejection and
   recorded explicit missing evidence. No changes to the other runtime lane.

## Original verification in this worktree (superseded by follow-up below)

Environment: Darwin, Python 3.14.7, Procoder 1.0.2.

- `python3 -B -m unittest discover -s deploy/failover/platform -v`: 13 tests pass,
  including each before/after mutation boundary for 28 apply and 26 cleanup
  operations, plus interrupted-apply direct cleanup. The independent final
  read-only reviewer reran the suite and confirmed the identified blockers fixed.
- `ruff check deploy/failover/platform`: pass.
- `ruff format --check deploy/failover/platform`: pass after formatting.
- `git diff --check`: pass (new files also inspected with Ruff/Procoder).
- Linux runner without opt-in: exits 1 before mutation with the required opt-in
  explanation. Actual privileged Linux execution: **NOT RUN**.
- `procoder review`: exits 2; installed binary has no such command. Manual and
  independent reviewer/adversarial passes were performed; no tool PASS claimed.
- `procoder test .` from this directory: exits 1; selects Go `.` (no Go package)
  and repository Rust, which fails on Darwin `libc::mmsghdr`. Scoped unittest
  success does not make this a passing Procoder/full supported-suite run.
- Procoder check initially caught formatting defects; fixed and rerun. The final
  seven-file gate exits 0: 7 clean, 0 unformatted, 0 unchecked, 0 out of scope,
  7 pre-existing repository hygiene advisories, 0 blocking findings. This covers
  the four Python files, README, handoff and example JSON; it is not a full-suite
  or Linux acceptance result.

## Original missing evidence and next owner actions

1. Parent must run the isolated Linux command in README and preserve its full
   result. In particular, confirm IPVS output/schema, down-dummy preferred-source
   route acceptance, kernel support, namespace mount isolation and cleanup.
2. The kernel-fence lane must supply/review the real `Fence.run_closed` binding,
   kernel denial lifetime, all-writer serialization and activation/withdrawal
   integration. No lease election, stale-owner or partition proof exists here.
3. Pre-provisioner/FG-02 must deliver independently attested engine identities,
   allocated L2 endpoints, pinned resources, anti-spoofing and explicit return
   routes. This strict single-attachment namespace contract does not yet support
   the real-engine fixture's independent management link or production Cilium pods.
4. Parent integration must measure actual tuple/policy/log and management behavior
   using this adapter and fence. The earlier engine proof exercises another
   fixture, not these commands. Active updates/deletion, empty-pool eligibility,
   two-frontend HA, loss/partition timing, session behavior and guarded migration
   remain outside this stage-only component.
5. Run supported Linux suites and the required Procoder workflow on the integrated
   result. No platform/release acceptance or formal FG-04 closure is asserted.

## VIP provisioning follow-up — 2026-09-17

Parent Linux execution is **RED**, not accepted. The preserved diagnostic at
`/tmp/nexora-wave3/platform-stage-diagnostic-linux.log` shows successful dummy
creation without `ifalias`; strict inspection correctly rejected it. The original
fake kernel incorrectly assumed alias-on-create was effective. The original
13-test results above did not validate that kernel behavior. Parent prerequisite
work (private ipvsadm overlay and dummy module availability) is separate from this
fix and was neither changed nor exercised here.

The contract now requires an existing, correctly owned DOWN `nxvip` dummy for
every local role, including frontend. The reconciler never creates/deletes/marks
links. Cleanup deletes only the exact backend VIP `/32` address after dependent
routes/rules, along with exact owned IPVS objects, and retains the dummy baseline.
Absent, unmarked, foreign, wrong-kind and UP VIP links reject stage and cleanup
before any writes. Existing resource fences and full-manifest ownership remain.
The parent's `{vip_link!r}` diagnostic is retained in the more explicit error.

The isolated fixture creator creates each dummy in its fresh owned namespace,
then assigns aliases after all manifest inventory is finalized. It reads back all
baselines before staging; failed creation, marking or validation tears down the
creator-owned namespaces. A partial manifest is never handed to reconciliation.
The non-atomic create/set interval belongs solely to the unpublished provisioner;
there is no claim of atomic rtnetlink ownership. Namespace teardown on process
termination relies on the existing isolated PID/network/private-mount lifecycle.

Local follow-up gates:

- `python3 -B -m unittest discover -s deploy/failover/platform -v`: **15 PASS**.
  The revised fixture has 25 stage and 25 cleanup commands. All before/after
  mutation failure and restart/partial-cleanup tests pass against the retained
  dummy baseline. Added absent/foreign/unmarked/UP/wrong-kind VIP regressions and
  fixture failure injection before/after all six VIP create/alias commands plus
  baseline readback failure; namespace cleanup occurs without staging handover.
- `ruff check deploy/failover/platform`: **PASS**.
- `ruff format --check deploy/failover/platform`: **PASS**.
- No privileged Linux execution, live/SSH/toolbox access or commits in this
  follow-up. No new Procoder result claimed. Stage remains **RED pending parent
  rerun**; unit mocks cannot establish the kernel result.

Exact modified paths relative to the original handoff (copy only these from this
isolated worktree, preserving unrelated parent/M8 changes):

1. `deploy/failover/platform/adapter.py`
2. `deploy/failover/platform/test_adapter.py`
3. `deploy/failover/platform/cmd/linux_namespace_test.py`
4. `deploy/failover/platform/README.md`
5. `deploy/failover/platform/HANDOFF.md`

`cmd/plan.py` and `example.manifest.json` are unchanged. Parent should rerun the
README's opt-in isolated Linux command against these files and save fresh logs
and exit status without overwriting the earlier RED evidence. Confirm alias
readback, real stage/repeat/new-instance cleanup, retained DOWN owned dummies,
exact baseline restoration and final namespace teardown before claiming PASS.
Production fencing, active lifecycle, engine traffic and HA gaps above remain.
