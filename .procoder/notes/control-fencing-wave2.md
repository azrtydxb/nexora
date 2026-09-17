# CP-02 control mutation fencing, wave 2

Base: `504a006`. Isolated implementation in `/tmp/nexora-wave2/control`.
No commits, deployment, cluster/toolbox access, live secrets, trust regeneration,
or task closure changes. No AGENTS.md was present in the worktree. Read
`.procoder/notes/parallel-integration.md` before implementation.

## Implemented slice

- `OnNotify` now receives `pgx.Tx`. The receive loop owns a bounded ownership
  transaction, and main calls `Scheduler.NotifyTx`. Primary-source validation,
  secondary row locking, refresh request increment and `pg_notify` use that same
  transaction. Scheduler transfer IO happens separately after commit. Existing
  `Scheduler.Notify` remains available and opens its own transaction for ordinary
  non-stream callers. Invalid notifications still log and continue; superseded
  ownership ends the stream.
- `OnUpdate` receives a transaction fence. Main binds `Applier.ApplyFenced`, which
  does its existing read-only parsing/TSIG preparation first, then invokes the
  fence inside `zone.Service.MutateFenced`, before locking the zone. Engine
  ownership, record changes, zone rebuild, audit and snapshot publication share
  one transaction. Transaction retries rerun the fence. There is no outer pool
  connection held during preparation and no check-then-mutate gap. Existing
  `Apply`/`Mutate` APIs preserve non-stream behavior. Stale mutating UPDATEs return
  SERVFAIL; asynchronous UPDATE handling and its rate/concurrency bounds remain.
- Pending log replies must both match the subscriber's request and pass the
  ownership fence. `DeliverTx` persists reply and notification atomically, even
  for local waiters. Local wakeup happens after commit and reads committed data
  from the table. This removes nested pool acquisition and pre-commit delivery.
  Log persistence is bounded to five seconds.
- TLS registration checks ownership under the engine row lock, so a delayed
  Hello cannot overwrite a newer local registration. Failed registration
  transactions remove their channel. TLS result persistence precedes in-memory
  updates; `ResultFor` matches the registration channel, preventing a delayed
  committed result from changing a replacement registration. Failed writes no
  longer alter the fingerprint cache.
- Existing stats callbacks continue to use their supplied transaction; no stats
  caller required changing.

## Caller inventory

Production: `mgmt/cmd/nexora-mgmt/main.go` is the sole OnNotify/OnUpdate wiring.
Its stats adapter remains unchanged. The forwarding transport test updates both
callback signatures; its UPDATE stub only returns responses and does not mutate
state. New ownership tests use the production dynupdate and xfrin adapters.
`LogBroker.Deliver` had only the receive-loop caller and is replaced by
`DeliverTx`. DNS TLS receive handling uses `ResultFor`; legacy `Result` remains
for the existing fanout API/tests and must not be used for stream callbacks.

## Tests and local results

The cross-server ownership test now uses a pool with MaxConns=1, covers both
separate instance IDs and reused instance IDs, and adds real NOTIFY scheduling,
pending log replies/local waiters, and an UPDATE callback paused before takeover
and resumed afterward. It checks unchanged zone/audit/config/log state for stale
work and successful replacement operations. Existing stats/TLS/ack/renewal and
Hello/disconnect assertions remain. Additional regressions check ownership lock
retention against a competing claim, log rollback without waking a local waiter,
TLS persistence failure without cache mutation, and delayed TLS result identity.

Local commands:

```sh
GOCACHE=/tmp/nexora-control-gocache go test ./mgmt/internal/control ./mgmt/internal/dynupdate ./mgmt/internal/zone ./mgmt/internal/xfrin -run '^$'
GOCACHE=/tmp/nexora-control-gocache go test ./mgmt/internal/control -run '^TestDNSTLS' -count=1
GOCACHE=/tmp/nexora-control-gocache go test ./mgmt/internal/control -run 'Test(ConnectionOwnershipAcrossServers|ConnectionFenceSerializesClaimWithMutation|TLSResultRollbackKeepsFingerprint|StatsOwnershipSingleConnection)' -count=1
```

The first command passes compilation; the second passes the database-free TLS
suite. The third fails at fixture startup: `initdb` is not in PATH. No database
regression is claimed as passed. The log rollback regression compiles but also
requires the parent's PostgreSQL environment. Default Go cache access was denied
by the sandbox; the temporary GOCACHE resolves that issue. Including
`./mgmt/cmd/nexora-mgmt -run '^$'` fails because this worktree lacks
`mgmt/internal/webui/dist` (`pattern all:dist: no matching files found`); no dummy
web asset or production code workaround was introduced.

Parent Linux handoff, after the normal GUI build prerequisite:

```sh
go test -race ./mgmt/internal/control ./mgmt/internal/dynupdate ./mgmt/internal/zone ./mgmt/internal/xfrin ./mgmt/internal/stats -count=1
go test ./mgmt/cmd/nexora-mgmt -run '^$'
```

Parent alone runs these supported Linux tests and broader integration. No Rust
changes or Rust test claims.

## Exact changed paths

- `mgmt/cmd/nexora-mgmt/main.go` (only the control callback wiring block)
- `mgmt/internal/control/dnstls.go`
- `mgmt/internal/control/dnstls_test.go`
- `mgmt/internal/control/forward_test.go`
- `mgmt/internal/control/hub.go`
- `mgmt/internal/control/logs.go`
- `mgmt/internal/control/ownership.go`
- `mgmt/internal/control/ownership_test.go`
- `mgmt/internal/control/server.go`
- `mgmt/internal/dynupdate/apply.go` (required production UPDATE adapter)
- `mgmt/internal/xfrin/scheduler.go`
- `mgmt/internal/zone/service.go`
- `.procoder/notes/control-fencing-wave2.md`

## Remaining boundaries, not full CP-02 acceptance

1. The invariant linearizes database mutations against ownership claims. An
   operation that commits before takeover is valid. Its DNS response, log wakeup,
   queued NOTIFY refresh or other outbound delivery may occur afterward. This
   patch does not fence all network sends or provide dataplane fencing.
2. A NOTIFY accepted before takeover becomes zone-owned durable scheduler work.
   The eventual transfer is not bound to the originating stream. Making transfers
   cancel/revalidate per stream would require a durable work-identity design;
   holding an engine row lock across transfer IO is deliberately not used.
3. Cross-process TLS fanout caches and queues are not invalidated on takeover;
   the old process can retain its own registration until disconnect. Channel
   identity protects the replacement registration within a fanout, and stale
   result writes are rejected by the database. It is not distributed revocation
   of previously queued certificate material. Registration memory is ephemeral,
   with cleanup on transaction failure, not a transactional external resource.
4. UPDATE adapter authors must honor the explicit fence contract. The production
   caller does; Go's callback type cannot prevent a future callback from ignoring
   its fence or writing through another connection. Read-only rejection paths
   can return DNS errors without ownership locking because they make no changes.
5. The scheduler's existing background `refreshLocked` holds a session advisory
   lock connection while Refresher uses other connections. General one-slot
   background transfer support is outside this slice; the NOTIFY callback itself
   works with one slot and performs no transfer IO.
6. All replicas must run fencing-aware binaries. Mixed old/new versions remain
   unsafe. Linux DB/race verification and integration are outstanding; this note
   does not close CP-02 or any milestone.
