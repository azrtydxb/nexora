# FG-02 trusted observation reader handoff

Baseline: `f9c1653ac1096ea17be62c587d86d8a2536d4872`. Only the three new
`observation_reader` files in this directory belong to this change. No existing
validator, API, main, migrations, deployment, locks or trust material changed.
This is an implemented read-only adapter with transport-fixture tests, **not
production integration or FG-02 closure**.

## Authoritative identity findings

- `mgmt/internal/control/server.go`: `Enroll` allocates `uuid.New()`, signs the
  engine certificate and persists the engine row. It stores the requested node
  name without Kubernetes Pod/Node UIDs. `Connect` authenticates the certificate
  identity but can update `node_name` from Hello, so that column is not placement
  authority. Enrollment/session wiring needs the provisioning correlation below.
- `engine/src/control.rs`: enrollment returns the engine UUID; `save_identity`
  persists it in `state_dir/identity/engine_id` alongside certificate material.
  The identity file is written last. Identity state is protected by the state lock.
- `deploy/helm/nexora/templates/engine-workloads.yaml`: `/var/lib/nexora` is
  mounted from the preserved state volume; the Secret mounted at `/etc/nexora/join`
  is enrollment input, not an authoritative persisted UUID-to-Pod UID record.
  `NEXORA_ENGINE_NODE_NAME` includes the configurable prefix and downward-API
  node name; it is not identity binding evidence.
- `deploy/kwrollout/workload.go`: `ReadServingPod` verifies controller, pod,
  state mount and readiness, then executes `head -c 128` against the public
  persisted UUID file and rereads the pod to detect replacement. That identity
  acquisition requires pod exec. It is not used by this read-only reader.

No read-only UUID-to-Pod binding publisher was found in the deployed chart path.
No cluster, Secret, host state, or live identity was read to reach this conclusion.
The fallback is a new explicit trusted-input contract, not an assertion that an
existing ConfigMap contains these observations.

## Reader and input contract

Construct `NewObservationReader` with an authenticated `http.Client`, trusted
HTTPS API-server origin, namespace and dedicated ConfigMap name. The client
transport must validate the cluster CA and authenticate with narrowly scoped
read-only credentials. The constructor copies the client, disables redirects,
and bounds requests and the complete observation to at most ten seconds (or a
smaller caller age/deadline). It neither discovers nor reads credentials.

`Observe(ctx, group, maxAge)` GETs the ConfigMap's `observations.json` key as a
`TrustedObservations` version 1 document. Marshal the exported Go type to produce
the schema: exactly two `members`, using the `TrustedObservation` JSON tags;
nested check/snapshot objects use `EligibilityCheck` (`OK`, `ObservedAt`) and
`EligibilitySnapshot` (`Version`, `Digest`). Times are RFC3339 UTC; explicit
`revoked` and `deleted` booleans are required. Unknown document fields, trailing
JSON, missing members and malformed bindings fail closed. Never store credentials
or private keys in this document.

Each record pins the persisted engine UUID, namespace, Pod name/UID, Node UID,
engine container ID and direct backend IP. The reader resolves `spec.nodeName`
from Kubernetes, GETs that actual Node, and checks its UID. It does not accept
engine-reported node names, prefixes, labels or a name-only node substitute.
It checks Pod creation/container start times, running container identity, phase,
readiness and backend address. A VIP or shared backend address cannot count as a
direct probe endpoint. A restarted container needs fresh measurements even if the
Pod UID survives. Missing/replaced/deleting objects and ambiguous bindings deny
the entire pool conservatively; ordinary fresh bound health/revocation failures
can leave the proven partner eligible via `EvaluateEligibility`.

Every input/Pod/Node object is reread in reverse order, with the ConfigMap last.
Changed UIDs, resource versions, targets, revocation state or decoded observations
discard the result. Reads use the core/v1 API with no stale resourceVersion hint;
there is no local cache, retry or previous-good fallback. Requests and response
sizes are bounded; IO errors return an explicit empty pool and an error. Never
ignore that error or retain the previous pool. Successful reads still return
eligibility denial codes for invalid freshness/configuration/health evidence.

These reads do not form an atomic Kubernetes/database transaction, nor a durable
lease. The caller must reevaluate desired state and observations on changes and
before the earliest evidence expiry, using a measured freshness budget. A mutation
after the last read remains possible. Frontend ownership/fencing is separate.

## Required provisioning and integration hooks

A trusted observer, with engines denied write access to its dedicated ConfigMap,
must publish each pair atomically. Its responsibilities remain unimplemented:

1. Acquire the public persisted engine UUID through a trusted provisioning/state
   inspection boundary. Verify the current Pod/container incarnation and actual
   Node UID before and after acquisition; replace the record on restart/migration.
   Never derive UUID ownership from a reported node name, workload prefix or label.
   Reuse the rollout verification principles, but provide an explicitly authorized
   acquisition mechanism; this reader deliberately cannot exec into pods.
2. Read authoritative management revocation/deletion, policy group and desired
   target state coherently. Correlate authenticated management session health and
   applied-version acknowledgements with the same persisted identity AND current
   container incarnation; UUID-only sessions cannot prove that correlation.
3. Resolve the acknowledged applied version from trusted snapshot storage and
   compute/verify `snapshot.ContentDigest` over effective policy AND authoritative
   configuration. Resolve the target separately. Do not infer applied content from
   readiness, the desired target, equal policy IDs, or engine-supplied digests.
4. Probe the bound backend directly, never the frontend VIP, and associate the
   probe time/result with that same Pod UID, container ID and backend IP. Publish
   independent binding, inventory, management, DNS and snapshot observation times;
   republishing cannot refresh an old measurement. Stop refreshing invalid data.
5. Provision narrowly scoped GET RBAC for the named ConfigMap, member Pods and
   Nodes; separate publisher write authority from engines and the reader. Supply
   the authenticated client, schedule reevaluation/expiry, and wire empty-pool
   handling into the eventual reconciler. None of this wiring is in API/main here.

This contract trusts its publisher's provenance and Kubernetes authority. It
cannot authenticate arbitrary operator-written observations, independently prove
an acknowledgement against database storage, or identify virtual Node objects
that secretly share physical hardware. Those are explicit upstream obligations,
not manufactured production evidence.

## Review and verification

Implementation, manual review, adversarial review and polish were performed.
The adversarial pass added rejection of a frontend/shared/link-local backend;
it also checked replacement between reads, same-Pod container restarts, stale
measurements, absent revocation state and redirect credential boundaries.
Tests exercise actual HTTP request construction, response parsing, rereads and
eligibility decisions through an injected `http.RoundTripper`. They use synthetic
fixtures; no live Kubernetes, TLS/RBAC deployment, Linux or database pass is claimed.

Verification commands/results are recorded below after the final checks.

All Go commands below used
`GOCACHE=/tmp/nexora-wave3-eligibility-go-cache GOPROXY=off`.

- `go test ./mgmt/internal/failover -run 'TestObservationReader|TestEligibility|TestValidation|TestStatus' -count=1`:
  PASS, 0.769s (initial implementation).
- `go test -race ./mgmt/internal/failover -run 'TestObservationReader|TestEligibility|TestValidation|TestStatus' -count=1 -coverprofile=/tmp/nexora-wave3-eligibility-coverage.out`:
  PASS after final behavior/test changes, 2.116s; package statement coverage 71.4%.
  `go tool cover -func=/tmp/nexora-wave3-eligibility-coverage.out` reports constructor
  100%, HTTP reader 95%, Observe 97.7%. This is focused coverage, not a full database
  package-suite claim. Log: `/tmp/nexora-wave3-eligibility-race.log`.
- `go vet ./mgmt/internal/failover`: PASS, including after the deadline test.
- `procoder test --name 'TestObservationReader|TestEligibility|TestValidation|TestStatus' mgmt/internal/failover`:
  overall FAIL (exit 1). Selected Go package PASS; procoder also selected Rust,
  which failed compiling `libc::mmsghdr` on macOS. Log:
  `/tmp/nexora-wave3-eligibility-procoder-test.log`.
- `procoder review`: unsupported by installed binary, exit 2; manual review and
  adversarial review were performed instead. An earlier `procoder review --help`
  likewise returned usage. `procoder test --help` was interpreted as a test path:
  Go failed on `./--help`, and Rust failed on `libc::mmsghdr`. Neither is a pass.
- `procoder check mgmt/internal/failover/observation_reader.go mgmt/internal/failover/observation_reader_test.go mgmt/internal/failover/observation_reader.md`:
  initial gate exit 0, 3 clean, 0 blocking. It reported existing repository hygiene
  advisories and lint NOT checked because the default Go cache was inaccessible.
  Its new exported-function doc-comment advisory was fixed. Initial log:
  `/tmp/nexora-wave3-eligibility-procoder-check.log`.
- Repeating that gate with the writable Go cache: PASS, exit 0, 3 clean,
  0 unchecked, 0 blocking; seven existing repository hygiene advisories remain.
  Log: `/tmp/nexora-wave3-eligibility-procoder-check-final.log`.
- `gofmt -w` on the new Go files; `git diff --no-index --check /dev/null <file>`
  on each new file produced no whitespace diagnostics (exit 1 denotes differing
  files). Final gate is rerun with the writable Go cache and recorded separately.

Parent handoff: integrate/review these three files, implement the publisher and
RBAC/client/reconciler wiring above, choose measured freshness/expiry behavior,
and run supported Linux/database/live acceptance. Preserve existing CAS retention,
serial paired rollout, OnDelete and wait=legacy. No task/sprint closure, commit,
push, deployment, CNI migration or split-DNS work was performed here.

## Wave 4 package extension

The original handoff above describes the wave 3 baseline. Wave 4 adds Collect and
an optional `connectionSession` UUID to the input schema, then wires this reader
into the source-authorized database publisher. Publication requires that session;
the legacy Observe projection remains compatible. See [lifecycle.md](lifecycle.md)
for the new mandatory trust boundary, current-session ACK provenance, lifecycle,
reservation and API consumer contracts. The external trusted input provisioner and
runtime withdrawal verifier are still not deployed or accepted by this package.
