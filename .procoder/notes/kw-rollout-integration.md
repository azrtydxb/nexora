# kw guarded rollout integration

## Status

Implemented and integrated, not yet committed or deployed. Production remains the two-engine
`sha-55dcf46` release; prior strict acceptance remains red. No production resources were changed
in this integration session. Four-engine live failover and full acceptance remain outstanding.

## Executable workflow

`scripts/kw-deploy.sh` invokes `deploy/kwrollout/cmd/kw-rollout`. The runtime assembles fleet
classification, persistent UUID binding, authenticated management/configuration checks, direct
and VIP UDP/TCP DNS, capacity/announcer checks, UID/resourceVersion label enrolment and serial
Helm stages. `deploy/kw/values-pairs.yaml` supplies the four-engine overlay; base kw rendering is
unchanged and the overlay has a separate raw golden.

The non-expiring ConfigMap lock spans supporting resources and bootstrap as well as Helm.
A private Unix socket lets shell phases check the same owner before production calls. Failures
retain the lock. Fresh installs, mixed selectors and partial controller sets are rejected;
manual recovery must establish quiescence of both the old process and remote mutations before
releasing ownership. This is cooperative exclusion, not a server-side Helm fence.

## Review findings addressed

- New partners cannot protect VIPs until label enrolment and selector cutover precede old-member replacement.
- Bootstrap must not prune prefixed c/d identities by Kubernetes node-name lookup.
- Failed remote DNS commands must retain their stdout sample evidence.
- Successful shutdown joins an in-flight monitoring round rather than cancelling away its evidence.
- Existing-install upgrades must not regenerate missing CA/KEK/TSIG material or delete the DNS
  serving certificate. Required secrets are checked before supporting-resource mutations, and
  SAN mismatches fail closed for separate certificate recovery/rotation.
- Shell tests now supply an explicit tag: the Linux synced source has no `.git`; missing-guard
  tests must reach the guard rather than pass accidentally on an earlier git failure.

## Evidence

- `/tmp/nexora-assembled-runtime-final.log`: Linux race suite 21.595s; chart suite 4.524s;
  e2e compile-only passed (not live acceptance).
- `/tmp/nexora-assembled-live-preflight.log`: read-only live preflight passed for two distinct
  identities, existing VIP endpoints, three eligible Ready nodes/capacity, current management
  configuration and configured direct/VIP UDP/TCP answers.
- `/tmp/nexora-rollout-reviewed-linux.log`: after secret-preservation review, Linux
  `go test -race ./deploy/kwrollout/... -count=1` passed 22.604s and
  `go test ./deploy/deploytest -count=1` passed 4.720s.
- Shellcheck passed for deployment, preflight, guard and bootstrap scripts; `git diff --check` passed.

Assembly tests use real Helm rendering with simulated subprocess/Kubernetes/HTTPS boundaries.
They cover migration success, supporting/bootstrap/Helm failures, ownership loss, failed DNS,
identity drift and cancellation. Missing-secret shell tests verify no mutation begins for each
required persistent secret. These do not establish production failover behavior.

## Remaining release work

Finish repository gates and resolve any blocking findings; commit before image builds. Run the
guarded migration, preserve per-sample evidence, inspect four identities and both complete pair
endpoint sets, then measure controlled individual-member failure/upgrades. Strict acceptance and
control-plane propagation diagnosis remain required, without weakened thresholds or retries that
hide DNS loss. Historical whole-laptop suite failures are distinct from Linux scoped results.
