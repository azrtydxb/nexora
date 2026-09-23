<!-- operations: Engine groups and staged rollouts; Engine lifecycle; Enrolling engines -->

# Fleet

Engines serve DNS; the management plane configures them over a mutually authenticated connection.

## Engine groups

Every engine is in exactly one engine group; the group `default` always exists. Upstreams, filter
lists, policy groups, global rewrites, forward zones, zones and RPZ zones apply to every group or to
one. Per group you can also choose whether its upstreams add to the global ones (`inherit`) or replace
them (`override`), extra allowed client networks, an OTLP endpoint and the filter index memory cap.
Resolution, DNSSEC, cache, block mode, allowlist and safe-search settings are fleet-wide.
Moving an engine to another group republishes that group's stable configuration as a new version.

Engines join a group with a join token that carries the group, labels, an expiry (1 minute to 1
year) and optionally a use limit. The token is shown once, when you create it. A token without a use limit can enroll every pod of a DaemonSet. An enrolled engine never needs
the token again. Enrolling always creates a new engine record, so delete stale records of re-enrolled
hosts.

## Rollouts

Every change publishes one version. With `all_at_once` the whole group receives it at once. With
`canary`, canaries are picked first: engines labelled `nexora.io/canary=true` first, then by node
name, as many as the larger of the canary count and the canary percentage of connected engines, at
least one and, with two or more connected, at most all but one. The canaries must apply the version
within the apply timeout (default 60 seconds) and are then watched for the health window (default 30
seconds). A canary that stops reporting halts the rollout, and so does a SERVFAIL ratio above the
maximum (default 5 %) once the canaries answered at least the minimum number of queries (default
100). Otherwise the rest of the group receives the version and must apply it within the same
timeout; disconnected engines get it when they reconnect.

When a rollout halts, the canaries keep the new version and everyone else stays on the stable one.
Fix forward with another change, or roll back, which republishes an earlier version to the whole
group and pauses change rollouts until you resume them. While paused, new changes wait as `pending`;
"Resume rollouts" publishes a fresh version.

## Certificates

Engine certificates live for 90 days by default and are renewed automatically from two thirds of
their lifetime. "Rotate certificate" renews one now. An engine that stays disconnected past its
certificate's expiry must be enrolled again. "Revoke" revokes all of an engine's certificates and ends
its connections; the engine keeps serving its last configuration. To admit a revoked host again,
remove its state directory and enroll it with a new join token.

## Engine logs

Each engine keeps its most recent log lines in memory: 2,000 lines, each cut to 512 bytes, with
secrets such as tokens and certificates masked. The Logs tab of the engine modal reads them over the
engine's control connection and follows new lines while open. Older lines are dropped as new ones
arrive, and all lines are lost when the engine restarts.

The level filter shows lines at the chosen level and above: `error`, then `warn`, `info` and
`debug`, so `debug` shows every line. The search matches part of a line's message, ignoring case. An
engine that is not connected cannot send logs.

## mDNS gateway and reflection

Each engine group's mDNS section controls gateway lookups and multicast reflection independently. Both start off. The gateway needs at least one named LAN interface; reflection needs at least two. Names are comma-separated, unique within each list, and must exist on the engine hosts. The query timeout defaults to 500 ms (100–5000).

The mDNS gateway needs hostNetwork or an interface on the LAN segment; normal pod networking alone does not provide LAN multicast. Reflection exposes discovery across the selected VLANs, so select interfaces deliberately. Turning off gateway resolution does not turn off reflection. Saving changes the group configuration using its revision and existing rollout policy.
