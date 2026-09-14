<!-- operations: Engine groups and staged rollouts; Engine lifecycle; Enrolling engines -->

# Fleet

Engines serve DNS; the management plane configures them over a mutually authenticated connection.

## Engine groups

Every engine is in exactly one engine group; the group `default` always exists. Upstreams, filter
lists, policy groups, global rewrites, forward zones, zones and RPZ zones apply to every group or to
one. Per group you can also choose whether its upstreams add to the global ones (`inherit`) or replace
them (`override`), extra allowed client networks, an OTLP endpoint and the filter index memory cap.
Resolution, DNSSEC, cache, block mode, allowlist and safe-search settings are fleet-wide.

Engines join a group with a join token that carries the group, labels, an expiry and optionally a use
limit. A token without a use limit can enroll every pod of a DaemonSet. An enrolled engine never needs
the token again. Enrolling always creates a new engine record, so delete stale records of re-enrolled
hosts.

## Rollouts

Every change publishes one version. With `all_at_once` the whole group receives it at once. With
`canary`, canaries are picked first (engines labelled `nexora.io/canary=true` first, then by node
name), must apply the version within the acknowledgement timeout, and are watched for the health
window: a canary that stops reporting, or a SERVFAIL ratio above the maximum once enough queries were
seen, halts the rollout. Otherwise the rest of the group receives the version.

When a rollout halts, the canaries keep the new version and everyone else stays on the stable one.
Fix forward with another change, or roll back, which republishes an earlier version to the whole
group and pauses change rollouts until you resume them.

## Certificates

Engine certificates live for 90 days by default and are renewed automatically from two thirds of
their lifetime. "Rotate certificate" renews one now. An engine that stays disconnected past its
certificate's expiry must be enrolled again. "Revoke" revokes all of an engine's certificates and ends
its connections; the engine keeps serving its last configuration. To admit a revoked host again,
remove its state directory and enroll it with a new join token.

## Engine logs

Each engine keeps its most recent log lines in memory: 2,000 lines, each cut to 512 bytes, with
secrets such as tokens and certificates masked. The Logs tab of the engine modal reads them over the
engine's control connection and follows new lines while open. Lines are lost when the engine restarts.
