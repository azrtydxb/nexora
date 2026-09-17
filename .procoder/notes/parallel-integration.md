# Parallel integration checkpoint — 2026-09-17

Scope: five isolated agent worktrees based on `f4544a0`; parent reviewed and
integrated patches. This is a tested implementation checkpoint, **not completion
of all milestones, a deployment, or operational failover acceptance**.

## Landed work and boundaries

- FG-02: pure, fail-closed placement/configuration eligibility contract and tests.
  Trusted adapter wiring and real network/engine evidence remain outstanding.
- CP-01: bootstrap pins existing connected identities before writes and waits at
  its final acknowledgement boundary for one fixed published version. The wait
  is bounded to 1–120 seconds, refuses changed/unhealthy/stale fleets or a moving
  target, and does not retry failed HTTP/DNS, republish, or soften parent gates.
  Unattended live deployment convergence remains unverified.
- CP-02: migration `01301` adds a unique-per-stream UUID token. Database ownership
  fences disconnects, acknowledgements, rejections, stats/heartbeats, TLS status,
  certificate renewal and hello version-ahead writes across management instances,
  including reused instance IDs. Production stats use the same transaction as
  ownership checking; single-slot pools no longer require a nested connection.
  Callback errors roll back stats and heartbeat together. This is **partial**
  CP-02: stale NOTIFY/UPDATE callbacks, pending logs and some in-memory effects
  remain unfenced. Mixed old/new binaries cannot enforce the invariant; all
  management replicas must be upgraded before acceptance. No dataplane fencing
  is provided by this session token.
- Operator: supported Linux baseline exposed missing AI/MCP API fields, not merely
  a stale golden. Added optional AI Secret reference and pointer-valued MCP flags,
  generated CRDs/deepcopy, strict kw fixture and default/override/persistence tests.
  Omitted MCP values retain disabled/read-only defaults; explicit false survives.
- `.procoder/notes/completion-audit.md` inventories all task/milestone records.
  M8/M10 implementation on unmerged branches must be integrated, not rebuilt;
  preserve the dirty M8 worktree. No formal task/sprint closure was performed.

## Review findings and verification

Parent review caught pool starvation in the first ownership patch: holding one
transaction while the stats callback borrowed another pool connection deadlocks
at saturation. The revised callback accepts `pgx.Tx`; a bounded single-connection
regression covers samples, rollups, DNSSEC/RPZ, rollback and stale-owner rejection.

First Linux integration was red because TLS test fingerprints violated the real
64-hex schema constraint, and full-row migration comparison included the newly
added nullable token key. Corrected fixtures use valid fingerprints and compare
all pre-existing engine fields, excluding only that additive key. Neither database
constraints nor existing data-preservation assertions were removed.

Verified commands/evidence:

- Baseline Linux `go test ./mgmt/... ./deploy/kwrollout ./deploy/deploytest -count=1`
  and `cargo test --locked -p nexora-engine --all-targets` passed before patches:
  `/tmp/nexora-parallel/{linux,engine}-baseline.log`.
- Initial operator baseline was red at strict kw values equivalence:
  `/tmp/nexora-parallel/operator-baseline.log`.
- Integrated Linux race tests: control **69.411s**, stats **15.814s**, failover
  **7.849s**; management command compiled:
  `/tmp/nexora-parallel/control-reviewed-linux.log`.
- Integrated bootstrap/rollout race suite **72.483s**, eligibility/model **6.305s**:
  `/tmp/nexora-parallel/first-integrated-linux.log`.
- Deliberately removing the applied-ack session CAS caused both different-instance
  and reused-instance regressions to fail (stale streams were not terminated).
  Mutation was restored in `finally`; this is deliberate negative evidence:
  `/tmp/nexora-parallel/control-negative-linux.log`.
- Final full management, rollout and deploy/chart packages passed on Linux:
  `/tmp/nexora-parallel/final-linux.log`. The enclosing command timed out during
  the subsequent operator run; that operator invocation is not counted as a pass.
- Separate `make operator-test` completed successfully on Linux, with real envtest,
  race detection and unchanged raw Helm goldens:
  `/tmp/nexora-parallel/operator-final-linux.log`.
- Laptop `procoder test` remains red: **235 Go failures** (including missing local
  PostgreSQL and platform-sensitive goldens); Rust cannot resolve `libc::mmsghdr`.
  Linux results do not turn the laptop-wide report green.

No kw deployment, live DNS disruption, retained-lock transfer, Cilium change,
identity/trust replacement or claim of transparent HA occurred. Remaining release
work includes cross-host real-engine attachment, trusted adapter, independent HA
and dataplane fencing, lifecycle/deletion, API/GUI, branch integration, guarded
migration, strict live acceptance and formal evidence-based milestone closure.
