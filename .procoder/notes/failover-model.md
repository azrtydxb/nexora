# Failover desired-state implementation checkpoint

Implemented, not deployed: `mgmt/internal/failover/` and migration
`01300_failover_groups.sql`. No live VIP, Cilium, engine, trust, policy or retained
lock changes were made. Tests used disposable PostgreSQL in the Linux toolbox.

## Implemented boundary

- UUID/name/unique canonical IPv4 frontend and two persistent engine identities.
- Relational membership reservations with deferred reverse foreign keys enforce
  exactly two members at commit, including writes bypassing Go. Global engine-ID
  uniqueness prevents concurrent cross-slot/cross-group reuse.
- Transactional create/list/get/update, immutable frontend IP and generation CAS.
- Admission rejects unknown, revoked/deleted, unplaced and same-reported-node
  identities. Same policy engine group is required conservatively; this does not
  establish current snapshot equivalence or live backend eligibility.
- Separate observation storage/read and conservative status projection. Missing,
  stale, future-dated or invalid reports cannot produce healthy; generation
  mismatch is pending. No adapter status writer/election authority exists yet.

## Review findings and remaining work

Adversarial review: engine `node_name` is self-reported and may be prefixed. Two
unequal names do NOT establish distinct physical failure domains. The platform
adapter must resolve actual placement before admitting backends. The current
model deliberately stores desired state only, never admits traffic.

Deleting a row immediately would release its IP while an old LB might still
advertise it. No delete operation is exposed yet; tombstone/withdrawal acknowledgement
and fenced ownership must be designed with T4 before safe API deletion/reuse.
CRUD audit/authorization belong in the same caller transaction during T3.

The first schema accepts only IPv4; accepting IPv6 without a verified adapter would
misrepresent support. Address collision with existing external infrastructure
still requires the migration/preflight inventory, not only the DB unique index.

Test review tightened direct-SQL assertions to require the intended FK/CHECK
SQLSTATE, rather than accepting an unrelated SQL error. Membership slot reversal
and old-generation status are covered. Down/up migration compares every existing
engine row before/after; no identities or engine metadata are rewritten.

T1 cross-host datapath, T2 placement/live-eligibility integration, T3 API/UI,
T4 HA/fencing, T5 guarded migration and T6 acceptance remain open. This is not a
completed failover-group feature or a deployed transparent frontend.

## Verification

- `scripts/dev-exec.sh 'go test -race ./mgmt/internal/failover ./mgmt/internal/store ./mgmt/internal/fleet -count=1'`
  passed after review: failover 7.932s, store 28.971s, fleet 13.042s.
  Log: `/tmp/nexora-failover-model-reviewed-linux.log`.
- Tests include invalid addresses/IDs, collision, concurrent membership race,
  stale edits, same reported node, revoked member, slot swap, immutable IP,
  deferred exact-cardinality constraints, migration preservation and status aging.
- Laptop Procoder suite remains RED: 233 Go failures and Rust `libc::mmsghdr`
  compilation failure. The two new DB tests fail locally because `initdb` is not
  on PATH, confirmed by `/tmp/nexora-failover-model-laptop.log`; they pass on Linux.
  No golden rewriting, skipped DB assertions or relaxed thresholds were used.
- Procoder adversarial review and commit gate ran; gate reported zero blocking
  findings. Full-suite failure is not replaced by the scoped Linux result.
