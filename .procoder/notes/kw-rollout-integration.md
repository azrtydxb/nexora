# kw guarded rollout integration

## Current outcome

Production is now `sha-809cf3a`, Helm revision 35: four Ready engines, two complete paired VIP
endpoint sets, all controllers OnDelete. Strict acceptance passed in 459.272s:
AI 42.92s, filtering 152.15s, full product 9.17s, M4 61.74s and smoke 193.27s.
Evidence: `/tmp/nexora-acceptance-809cf3a/acceptance.log` and `acceptance.exit` (0).
Post-acceptance read-only paired preflight passed in `/tmp/nexora-postacceptance-809cf3a.log`.
Forced individual-member failure testing remains outstanding; recurring propagation root cause
is not established as fixed by this acceptance pass.

Manual recovery used UID/resourceVersion/protocol/owner CAS after preflight, terminal Helm status
and absence of active deployment clients. No expiry or automatic takeover was introduced.
Commit `4503ec5` corrected Helm's waiter and allowed healthy previously-created partners to stay
frozen through resumed cutover; selected/final gates still require target images. Its rollout
activated both pairs but stopped after a because Helm returned with its old surge pod draining.
Commit `809cf3a` added a bounded old-target-pod deletion wait, not DNS/health retries, with an
assembled failure-retains-lock test. Linux race/chart suites passed (`/tmp/nexora-drain-linux.log`).

The `809cf3a` rollout verified a/b/c/d serially and the final frozen fleet. Bootstrap then briefly
left one engine at applied/target 1233 versus latest 1234, so the workflow correctly exited 1 and
retained its lock. Subsequent paired preflight and the entire strict acceptance suite passed;
the lock was manually CAS-released only after another successful preflight and quiescence check.
This was a recovered deployment, not a zero-exit unattended workflow.

Logs: `/tmp/nexora-deploy-4503ec5/deploy.log` (320 DNS samples, no recorded query errors),
`/tmp/nexora-deploy-809cf3a/deploy.log` (836 samples, no recorded query errors). Finite samples
are not proof of zero loss. The final bootstrap propagation wait remains a workflow issue.

## First attempt (historical)

Committed as `7ce4bed`; the first production attempt stopped at Helm revision 26 (failed).
Four engines are Ready, connected and current, but VIP selectors remain legacy: a/b still serve
on their original `sha-55dcf46` pods; c/d and management run `sha-7ce4bed`. All engine controllers
are OnDelete. The non-expiring deployment lock is retained. Pair cutover, live failover and full
acceptance remain outstanding. Do not treat four Ready engines as completed VIP protection.

Helm 4.1.1's default watcher timed out after 15 minutes, insisting on Updated 1/1 for the deliberately
frozen a/b DaemonSets. Its stored release description confirms Updated 0/1 for both. The candidate
fix uses `--wait=legacy`, preserving subsequent selected-image and fleet health gates. Assembly
regression failed before the fix (`/tmp/nexora-wait-mode-red.log`); Linux race/chart suites passed
22.496s/4.715s afterward (`/tmp/nexora-wait-mode-linux.log`). The fix is not yet deployed.

Attempt evidence: `/tmp/nexora-deploy-7ce4bed/deploy.log`, exit 1. All 3,680 recorded DNS samples
have empty query errors; cancellation also produced an incomplete remote-monitor evidence error,
so this is not a successful monitoring run or a zero-loss claim. Read-only preflight passed again
in `/tmp/nexora-after-frozen-timeout.log`. Process inspection found no remaining deployment/Helm
client, and Helm status is terminal failed; no lock release or recovery mutation has been made.
Recovery must recheck quiescence and exact state, then explicitly release ownership using CAS,
never automatic expiry/takeover.

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
