# FG-05 candidate handoff — f9c1653

Worktree baseline: `f9c1653ac1096ea17be62c587d86d8a2536d4872`.
Only new `deploy/failover/fence/` files belong to this lane. No commit, push,
SSH, cluster, shared toolbox, dev-sync, secret, live configuration or task closure
was performed. The parent main REAL_ENGINE document was read only.

## Four passes

1. Implementation: kernel TC all-frame predicate, atomic absolute-window map,
   narrow libbpf loader/ticket driver, isolated Linux smoke, adapter and allocator
   contracts. Fresh map, no pins, no relative TTL renewal, no cleanup detach.
2. Reviewer reread: checked deadline overflow/boundaries, namespace assignment,
   DOWN-before-attach, failure retention, map lifetime, dedicated management path,
   packet/verifier controls and precise scope of evidence. Corrected netlink dump
   request payload sizes and added a second ownership check before attachment.
3. Adversarial review: independent read-only reviewer identified time namespace
   clock mismatch, BPF include ordering, raw-socket ENOBUFS on TC drop, cleanup
   failure handling and literal broadcast GARP coverage. Addressed these; also
   reject foreign pinned/map ABI state, XDP and TCX, and unknown TCX query support.
   Privileged bypass and unmeasured suspend/drift remain explicit platform gates.
4. Polish: canonical ARM syntax, readable failure messages, explicit build
   prerequisites, bounded subprocess waits, optimized-Python refusal, fail-red
   cleanup escalation, six verifier negative-control unit tests and exact handoff.

## Commands and results on this macOS worktree

- `make -C deploy/failover/fence test`: PASS. The C test executes the shared
  deadline predicate, including missing state, expiry equality, stale replay,
  future capture, horizon excess and wraparound. Six Python tests validate the
  packet verifier's failure behavior using simulated sockets. Neither is kernel
  or HA evidence.
- `ruff check --no-cache deploy/failover/fence/*.py`: PASS.
- `clang-format` applied to all C/header sources; BPF Linux type headers retained
  before libbpf helper declarations in a separate include group.
- `make -C deploy/failover/fence smoke-build`: FAILED here because Linux/libbpf
  `bpf/bpf_helpers.h` is absent. The real driver/BPF build is **not verified**.
- `procoder test .` from this directory: FAILED. Installed Procoder 1.0.2 selects
  repository Go/Rust ecosystems rather than the independent Make target; reports
  Go `# .` failure and Rust missing `libc::mmsghdr` on Darwin. The earlier
  `procoder test --help` invocation also selected tests and was interrupted;
  this version does not provide per-command help for that invocation.
- `procoder review deploy/failover/fence`: unsupported command, exit 2 on installed
  Procoder 1.0.2. Manual four-pass and independent read-only review were performed;
  no Procoder review PASS is claimed.
- A directory-only `procoder check deploy/failover/fence` exited zero but checked
  zero files, so it is not used as formatting evidence. The explicit-file gate
  subsequently PASSED: 3 clean, 0 unformatted, 0 unchecked, 7 out of scope and no
  blocking findings. C/headers are outside this Procoder version's coverage;
  `clang-format --dry-run --Werror` passed separately. A final explicit-file
  gate also includes this handoff. Existing out-of-lane hygiene was not changed.

## Parent-only remaining evidence

Run the exact prerequisites/build/smoke commands in [README.md](README.md), on a
new disposable Linux environment, and retain complete stdout/stderr, exit status,
source/binary hashes and kernel/tool versions. Unsupported capabilities must fail;
do not replace libbpf or the kernel program with a fake driver. The namespace smoke
has **not been executed** in this lane. The negative object fixtures have likewise
not been loaded on a kernel here.

Still missing: supported Linux compile/verifier/load, real paused-owner expired
ARP/GARP/data capture and management preservation, kernel/version matrix,
time-namespace/XDP/TCX refusal fixtures, interrupted load/attachment and cleanup
fault injection, measured physical clock drift/suspend/VM-pause assumptions,
bounded post-gate drain, durable allocator serialization/uncertain-grant bounds,
platform integration, two-owner successor and partition tests, managed control
continuity, lifecycle/cutover and full HA acceptance. Backend reply/session behavior
is outside this gate. No DB lease, fake unit driver or cross-host forwarding result
satisfies these missing gates.
