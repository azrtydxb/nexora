# nexora-v1-m5 — implementation plan

Status: draft (reconciled with the M1–M4 code at HEAD 91cf9a4 plus the uncommitted M4 Tasks 10–11 work)
Spec: .procoder/specs/nexora-v1.md

## Goal

Ship S-5 as a product: one stateless management plane drives N engines on different hosts through engine groups, engine-group-scoped configuration, staged (canary) rollouts with an automatic health halt and manual rollback, fleet health and engine lifecycle (join tokens bound to groups, certificate renewal, rotation and revocation, engine state that survives restarts), a fleet GUI, release images, a docker-compose example, a Helm chart, and the final kw deployment built from that chart and verified end to end by `TestKwFullProduct`.

## Architecture

M1 already streams versioned snapshots over mTLS, and every mgmt instance LISTENs on PostgreSQL and pushes to the engines connected to it. M5 puts two layers between a config mutation and an engine push. First, `snapshot.Mutate` publishes one global version with one snapshot per engine group (`group_snapshots`), each built from the global rows plus that group's rows (`engine_group_id` on the scoped tables). Second, each group snapshot gets a `rollouts` row whose state machine (`mgmt/internal/rollout`, a pure `Step` function) is advanced by any mgmt instance under a PostgreSQL advisory lock; the hub pushes whatever `fleet.TargetFor` says an engine should run, and the canary health gate reads the `engine_stats` samples M1 already stores. Engine lifecycle adds an `engine_certificates` table checked on every authenticated call and certificate renewal over the existing `Connect` stream; distribution extends `images.yml` and adds `deploy/compose/`, `deploy/helm/nexora` and a kw deployment installed from that chart.

### Reconciled with M1–M4 code

The first draft of this plan was written before any code existed. These design-level changes replace it (each is binding for the tasks below and is written into `docs/architecture.md` by Task 1):

1. **No `FleetHealth` message and no `engine_health_samples` table.** M1's `Stats` already carries `queries_total`, `servfail_total`, cache hit/miss totals and the duration histogram, and `stats.Record` stores every sample as protobuf in `engine_stats` (24 h retention). The canary gate and the per-engine charts decode those samples. The only contract additions are the certificate renewal messages, numbered 500 and up as `.procoder/notes/plan-review.md` requires (`EngineMessage.cert_request = 500`, `ServerMessage.cert_issued = 500`, `ServerMessage.renew_certificate = 501`); M5 adds no `ConfigSnapshot` or `Stats` field.
2. **"Engine groups" are the server-side fleet and never share a name with M2 policy groups.** Table `engine_groups`, Go type `fleet.EngineGroup`, column `engine_group_id`, API paths `/engine-groups`, JSON field `engine_group_id`, GUI label "Engine group". M2's client-side `policy_groups` and the existing `rewrites.group_id` (a policy group) keep their names. A rewrite inside a policy group follows its policy group's engine group; only global rewrites (`group_id IS NULL`) carry their own `engine_group_id`.
3. **Scoped tables are the real ones:** `upstreams`, `filter_lists`, `policy_groups`, `rewrites`, `forward_zones`, `zones` (M4) and `rpz_zones` (M3). The singleton settings tables (`resolver_settings`, `access_control`, `allowlist`, `resolution_settings`, `dnssec_settings`, `global_safe_search`) stay fleet-wide; an engine group adds only `extra_acl_cidrs` (appended to `access_control.allow_cidrs`) and `otlp_endpoint` (replaces the global endpoint when non-empty). Resolution mode and DNSSEC settings stay snapshot-wide, as accepted in the M3 review.
4. **Zones and RPZ zones reuse the M3/M4 structures unchanged.** Zone names stay globally unique (M4's `zones.name UNIQUE`, `zone.Service.GetZoneByName`, NOTIFY intake and `dynupdate` look zones up by name); a zone is served by every group (`engine_group_id IS NULL`) or by exactly one group. RPZ zones keep one global `position` order and a group sees the global zones plus its own in that order. The first draft's "same zone name as primary in one group and secondary in another" is dropped: M4 secondaries are pulled by the management plane (`xfrin`), not by engines.
5. **Every publish writes a snapshot for every engine group**, not only for "affected" groups. M1–M4 tests and `kwWaitApplied` rely on every engine applying the newest version. A group snapshot whose content digest (snapshot with `version` and `created_unix_ms` zeroed) equals the group's stable content rolls out immediately (`all_at_once`, even when paused); only a real content change goes through the group's strategy. `all_at_once` rollouts are inserted directly in `rolling`, so the hub pushes on the same notification without waiting for a controller tick.
6. **Connection state is M1's**: an engine is connected while `engines.connected_instance` names an instance whose `instances.heartbeat_at` is younger than 15 s. There is no `NEXORA_INSTANCE_ID`; `control.NewInstanceID()` stays. `config_versions.snapshot` becomes nullable; `snapshot.Latest` reads the default group's newest group snapshot. `pg_notify('nexora_config', version)` stays as an informational event; the hub pushes on `nexora_rollout`.
7. **Engine records keep M1's shape**: `node_name`, soft delete `deleted_at`, `version_ahead`, `status` (`current` now means "applied version equals the engine's target version"; `revoked` is added). API paths keep `{id}`. Deleting an engine revokes its certificates and sets `deleted_at`.
8. **Join tokens keep M1's reusable-until-expiry semantics** (accepted in the M1 review for autoscaled engines and used by the kw DaemonSet). M5 adds `engine_group_id` (default group), `labels` and an optional `max_uses` (NULL = unlimited). Enrollment errors stay `PermissionDenied`, now with the messages `join token unknown`, `join token expired`, `join token exhausted`, `join token revoked`.
9. **Certificates**: M1 issues 365-day engine certificates and stores the serial in `engines.certificate_serial`. The migration backfills `engine_certificates` from that column with M1's exact validity (`enrolled_at - 1 hour` .. `enrolled_at + 365 days`), so no "record on first use" path is needed. New certificates use `NEXORA_ENGINE_CERT_TTL` (default `2160h`). Revocation is checked on `Connect`, `GetBlob` and the builtin OTLP `LogsService` through the per-call database lookup M1's `Server.engine` already does (no cache).
10. **Engine state on kw moves from `emptyDir` to `hostPath`** (as the M1 and M3 reviews required) and the engine name comes from `NEXORA_ENGINE_NODE_NAME` (Kubernetes node name plus group), so a restarted pod keeps its engine id.
11. **kw engines are DaemonSets (one engine per node per group), not "3 engines on distinct nodes".** Two groups are assigned by node label `nexora.io/engine-group`: `scripts/kw-deploy.sh` labels `worker-24` and `worker-25` with `edge-b`; the `default` DaemonSet runs on every other node (`master-11..13`, `worker-21..23`, 6 engines) and the `edge-b` DaemonSet on the two labelled workers. LoadBalancer layout: `192.168.10.135` mgmt gRPC (unchanged); `192.168.10.136` stays the `default` group's DNS/DoT/DoH/DoQ address with `externalTrafficPolicy: Local` (kube-vip holds VIPs on a control-plane node, which runs a `default` engine); `192.168.10.137` is new for `edge-b` with `externalTrafficPolicy: Cluster`, because the VIP node runs no `edge-b` engine (so `edge-b` sees node addresses, not client addresses, on `.137`; per-client policy is exercised on `.136`). `NEXORA_KW_ENGINES` stays the total (8). `.138` and `.139` stay unassigned.
12. **kw from the chart, not a rewrite of kw**: the chart installs `nexora-mgmt`, the per-group engine DaemonSets, their Services, the ServiceMonitor and the PrometheusRule. The existing CNPG cluster `nexora-db` (external database mode, secret `nexora-db-app` key `uri`), `opensearch.yaml`, `otelcol.yaml`, `blocklist.yaml`, `namespace.yaml` and `bootstrap.sh` stay, as do the secrets `nexora-ca`, `nexora-kek`, `nexora-dns-tls`, `nexora-admin` and `nexora-join-token`. Resource names stay those `TestKwSmoke` and `bootstrap.sh` use (`nexora-mgmt`, `nexora-mgmt-grpc`, `nexora-mgmt-lb`, `nexora-dns`, `nexora-engine`, `nexora-engine-metrics`, ingress TLS secret `nexora-ingress-tls`).
13. **`images.yml` already exists** (per-arch runners, push by digest, merge): M5 extends it with the `:main` tag and a multi-arch manifest check, and `deploy/deploytest` guards it. It is not recreated.
14. **The e2e harness is extended, not replaced**: fleet helpers are methods on M1's `*harness.API` (`a.Must(method, path, body, out, want)`), engines start with `StartManagedEngineWith` (new `EngineOptions.ExtraEnv`), and `(*Env).RestartEngine` restarts an engine on its state directory. `harness.PublishRawSnapshot` writes group snapshots and rollouts.
15. **GUI**: pages live in `web/src/pages/` with `data-testid` hooks like M1–M4; there is no vitest, so component behaviour is covered by Playwright screens `web/e2e/screens/20-fleet.spec.ts` and `21-engine-group-scope.spec.ts` (M4 Task 15 takes 18–19) and `TestGUICoverage`'s glob widens from `[01][0-9]` to `[012][0-9]`.
16. **CLI**: `nexora-mgmt engine-group create`, `nexora-mgmt join-token create` and `nexora-mgmt ca init --if-missing` are added for Helm and compose installs; the kw deployment keeps bootstrapping through the HTTPS API (`bootstrap.sh`). There is no `api-token create` CLI; the kw acceptance test logs in with the admin password like `TestKwSmoke` (`kwLogin`).
17. **Health and readiness path is `/api/v1/health`**, advisory locks follow M1's `hashtext('nexora:...')` naming (`nexora:config_version` stays the version lock), and M5's migration is `00500_fleet.sql`, after every M4 migration (`00400`, `00401`, and M4 Tasks 12–14's `004xx` files).

## Constraints

Copied from the spec and docs/architecture.md (binding):

- "Management plane is stateless: all state in PostgreSQL, any number of instances behind a load balancer, engines may connect to any instance."
- "Deployment: multi-node from v1 — one management plane controls N engines across hosts; single-host deployment is the N=1 case of the same model."
- "Distribution: multi-arch (amd64, arm64) OCI container images for engine and management plane, a docker-compose example, and a Helm chart."
- "The query path never logs synchronously, never touches a database, and never performs per-packet heap allocation on the cache-hit path." Nothing in M5 touches engine worker threads; certificate renewal runs on the `nexora-control` runtime.
- "Engines persist their last applied snapshot locally and keep serving on it when the management plane is unreachable." This holds for revoked engines too.
- "Stale engine: an engine reconnecting with an old snapshot version is brought to current; an engine reporting a newer version than the database (restored backup) is flagged, not silently downgraded." M1's `version_ahead` flag and `VersionAhead` message stay, compared against the engine's target version.
- "Concurrent edits: two operators editing the same zone or policy get optimistic-concurrency conflicts, not lost writes." Engine groups and engines carry `revision`; a stale revision returns 409 `conflict`.
- Out of scope: "Kubernetes operator / CRDs (Helm chart only)", "Management-plane-managed PostgreSQL HA (operator's responsibility)".
- "Every test that asserts 'does not happen' first asserts the positive path in the same run, so a harness failure cannot pass a negative check."
- Builds and tests run in the kw dev pod through `scripts/dev-exec.sh <cmd>` (it syncs first). Generated sources (`gen/go/`, `mgmt/internal/api/gen.go`, `web/src/api/schema.d.ts`) are generated on the laptop with `make proto`, never in the pod. Images build with `scripts/build-image.sh` and are pulled as `192.168.10.131/azrtydxb/<name>:<tag>`. Commits run on the laptop with `git`.
- Go module `github.com/piwi3910/nexora`; uuid `github.com/google/uuid`; Go DNS client `github.com/miekg/dns`; YAML in Go tests `go.yaml.in/yaml/v3` (already in `go.mod`).
- Proto field numbers: M5 fields in existing messages use 500 and up (M2 300–399, M3 100–199, M4 200–299).
- Fixed identifiers: default engine group id `00000000-0000-0000-0000-000000000001`, name `default`; canary label key `nexora.io/canary`; NOTIFY channels `nexora_rollout` (payload engine group id; instances re-push, controllers step), `nexora_engine_updated`, `nexora_engine_revoked`, `nexora_engine_rotate` (payload engine id); advisory locks `hashtext('nexora:rollout:' || id)`.
- New management-plane environment: `NEXORA_ENGINE_CERT_TTL` (Go duration, default `2160h`, minimum `30s`), `NEXORA_ROLLOUT_TICK` (default `1s`, `100ms`..`1m`). New engine environment: `NEXORA_ENGINE_NODE_NAME` (overrides `node_name`).
- New metrics: management `nexora_mgmt_engines{engine_group,status}`, `nexora_mgmt_engines_disconnected`, `nexora_mgmt_rollouts{engine_group,state}`; engine `nexora_control_revoked` (gauge 0/1), `nexora_control_cert_renewals_total`.
- kw facts: arm64 k3s, nodes `master-11..13` (control plane, tainted, kube-vip ARP holds LoadBalancer VIPs there) and `worker-21..25`; storage classes `longhorn` (default) and `longhorn-single`; ingress class `nginx`; ClusterIssuer `cluster-ca`; CNPG operator in `cnpg-system`; Jaeger `jaeger.observability:4317` (query `jaeger.observability:16686`); Prometheus `kps-prometheus.monitoring:9090` (kube-prometheus-stack, label `release: kps`); dev pod namespace `nexora-dev`. kw's network redirects every outbound DNS query, so kw runs forward mode with forwarded DNSSEC validation (recursion is tested only in the private hierarchy). LoadBalancer IPs used by other workloads: 120–131, 133, 134; Nexora: `.135` mgmt gRPC, `.136` DNS group `default`, `.137` DNS group `edge-b`.
- Consumed M1–M4 identifiers (verified by Task 1): tables `engines(id, node_name, join_token_id, certificate_serial, engine_version, enrolled_at, last_seen_at, connected_instance, applied_version, rejected_version, rejected_reason, persist_error, version_ahead, deleted_at)`, `join_tokens(id, name, secret_hash, created_by, created_at, expires_at, revoked_at, uses)`, `instances`, `engine_stats(engine_id, at, stats)`, `config_versions(version, created_at, created_by, summary, snapshot)`, `upstreams`, `filter_lists`, `policy_groups`, `rewrites(group_id)`, `forward_zones`, `zones`, `rpz_zones`, `access_control`, `resolver_settings`; Go `snapshot.Mutate`, `snapshot.EnsureInitial`, `snapshot.PublishRaw`, `snapshot.Latest`, `snapshot.Build`, `snapshot.AddAuthZones`, `store.LoadResolution`, `store.PolicyQuerier`, `storetest.New(t) *store.Store`, `(*store.Store).Migrate`, `auth.WriteAudit`, `(*pki.CA).SignEngineCSR`, `control.CreateJoinToken`, `control.NewInstanceID`, `control.EngineID`; harness `(*Env).StartMgmt`, `(*Env).StartManagedEngineWith`, `EngineOptions`, `Bootstrap`, `(*API).Must`, `(*API).WaitEngine`, `(*API).CreateJoinToken`, `(*API).LatestVersion`, `PublishRawSnapshot`, `RunPlaywright`, `EventuallyTrue`, `Eventually`; e2e helpers `waitLatestApplied`, `loadKwEnv`, `kwLogin`, `kwEngine`, `kwWaitApplied`, `aValues`; engine identity files `state_dir/identity/{cert.pem,key.pem,ca.pem,engine_id}`; web `web/src/pages/EnginesPage.tsx`, `web/src/app/router.tsx`, `web/e2e/fixtures.ts` (`login`, `env`), `web/src/auth/permissions.ts` mirrored by `web/scripts/check-permissions.mjs`.

## Task 1: Settle the fleet design in docs/architecture.md

Files: `docs/architecture.md` (the binding how; changed before any code, per its own rule)
Interfaces: produces the names every later task uses — tables `engine_groups`, `group_snapshots`, `rollouts`, `engine_certificates`; columns `engine_group_id`, `engines.labels`, `engines.revoked_at`, `engines.cert_rotate_requested_at`, `engines.revision`; packages `mgmt/internal/rollout`, `mgmt/internal/fleet`; API paths `/engine-groups`, `/rollouts`, `/fleet/summary`, `/engines/{id}/stats|revoke|rotate-certificate`; GUI routes `/engines`, `/engines/groups/:id`, `/engines/nodes/:id`, `/engines/rollouts/:id`.

- [ ] Verify the M1–M4 identifiers this plan consumes. Run:
  ```
  scripts/dev-exec.sh 'ls mgmt/migrations/*.sql | sort | tail -1; grep -hoiE "create table (engines|join_tokens|instances|engine_stats|config_versions|upstreams|filter_lists|policy_groups|rewrites|forward_zones|zones|rpz_zones|access_control|resolver_settings) " mgmt/migrations/*.sql | tr A-Z a-z | sort -u | wc -l; grep -cE "^message (Hello|Stats|ConfigSnapshot|EngineMessage|ServerMessage) " proto/nexora/control/v1/control.proto; grep -cE "= 5[0-9][0-9];" proto/nexora/control/v1/control.proto; grep -rhoE "func (Mutate|EnsureInitial|PublishRaw|Latest|Build|AddAuthZones|LoadResolution|New|WriteAudit|CreateJoinToken|NewInstanceID|EngineID)\(" mgmt/internal/snapshot mgmt/internal/store mgmt/internal/auth mgmt/internal/control | sort -u | wc -l; grep -rhoE "func (\(e \*Env\) StartMgmt|\(e \*Env\) StartManagedEngineWith|Bootstrap|\(a \*API\) WaitEngine|\(a \*API\) CreateJoinToken|PublishRawSnapshot|RunPlaywright|EventuallyTrue)\(" e2e/harness | sort -u | wc -l; grep -c "\"cert.pem\", \"key.pem\", \"ca.pem\", \"engine_id\"" engine/src/control.rs; grep -hoE "func (waitLatestApplied|loadKwEnv|kwLogin|kwWaitApplied|aValues)\(" e2e/*_test.go | sort -u | wc -l'
  ```
  expect, line by line: a migration whose numeric prefix is below `00500`; `14`; `5`; `0`; `12`; `8`; `1`; `5`. For every count that differs, find the current spelling with `grep -rn` for the concept, add it to the "M1–M4 names" table the next step appends, and use that spelling wherever this plan writes the name; behaviour does not change.
- [ ] Add a section `## Fleet (M5)` to `docs/architecture.md` directly before `## Deployment on kw`, with exactly this content (plus a table "M1–M4 names" when the previous step recorded any difference):
  ```markdown
  ## Fleet (M5)

  ### Engine groups and scoping

  - `engine_groups` holds the server-side fleet partition. The group `default`
    (`00000000-0000-0000-0000-000000000001`) always exists and cannot be renamed
    or deleted. Every engine belongs to one group (`engines.engine_group_id`); a
    join token names the group an enrolling engine lands in, and
    `PATCH /api/v1/engines/{id}` moves an engine. Engine groups are unrelated to
    M2 policy groups (client-side, selected by source CIDR).
  - Scoped tables carry `engine_group_id uuid NULL` (NULL = every group):
    `upstreams`, `filter_lists`, `policy_groups`, `rewrites` (only global
    rewrites; a rewrite inside a policy group follows that policy group),
    `forward_zones`, `zones`, `rpz_zones`. Names stay unique across the fleet.
  - A group's snapshot contains the global rows plus the group's rows. Upstreams
    follow `engine_groups.upstream_mode`: `inherit` = the group's upstreams
    first, then the global ones; `override` = only the group's upstreams.
    `access_control.allow_cidrs` is followed by `engine_groups.extra_acl_cidrs`;
    a non-empty `engine_groups.otlp_endpoint` replaces the global OTLP endpoint.
    Resolution, DNSSEC, resolver/cache, block mode, allowlist and global safe
    search settings are fleet-wide. A policy group may select only filter lists
    that are global or in its own engine group. RPZ TSIG keys and hosted-zone
    TSIG keys (`RpzTsigKeys`, `KeyMaterial`) are filtered per engine to the
    zones in its target snapshot.
  - Host concerns stay in `engine.toml`. `NEXORA_ENGINE_NODE_NAME` overrides
    `node_name`. Per-engine state set through the API is the group and
    `engines.labels` (string -> string; key of 1-63 characters from `a-z`,
    `0-9`, `.`, `/`, `-` that starts and ends with `a-z` or `0-9`; value at most
    63 characters; at most 32 labels). `nexora.io/canary=true` makes an engine preferred for canary
    selection.

  ### Versions and snapshots

  - `config_versions.version` stays one global sequence
    (`pg_advisory_xact_lock(hashtext('nexora:config_version'))`). Every publish
    writes one
    `group_snapshots(version, engine_group_id, snapshot, content_sha256)` row per
    engine group; `content_sha256` is the SHA-256 of the
    deterministic encoding with `version` and `created_unix_ms` zeroed.
    `config_versions.snapshot` is NULL from M5 on; `snapshot.Latest` returns the
    default group's newest group snapshot.
  - `engine_groups.stable_version` is the newest version whose rollout completed
    for that group. Rollback and republish copy an existing group snapshot into
    a new version (re-encoded with the new number), because engines apply only a
    version greater than the one they run.

  ### Rollouts

  - Each group snapshot gets one `rollouts` row. Kinds: `change` (a config
    mutation), `rollback`, `republish` (engine moved into the group). Group
    parameters: `rollout_strategy` (`all_at_once` | `canary`), `canary_count`,
    `canary_percent`, `ack_timeout_seconds` (60), `health_window_seconds` (30),
    `max_servfail_ratio` (0.05), `min_health_queries` (100), copied into
    `rollouts.params` at creation.
  - A `change` whose `content_sha256` equals the content of the group's stable
    version, `rollback`, `republish` and test-only raw publishes are immediate:
    strategy `all_at_once`, not held by `rollouts_paused`. An `all_at_once`
    rollout is inserted in `rolling`; a `canary` change is inserted in `pending`.
  - States: `pending` -> `canary` -> `verifying` -> `rolling` -> `completed`;
    `canary`/`verifying`/`rolling` -> `halted`; `halted` -> `rolled_back`; every
    non-terminal state -> `superseded`. At most one rollout per group is in
    `canary`/`verifying`/`rolling`.
  - `pending` waits while the group has `rollouts_paused`, else selects
    canaries: connected engines, `nexora.io/canary=true` first, then by node
    name; size max(`canary_count`, ceil(`canary_percent`% of connected)), at
    least 1, at most connected-1 when two or more are connected.
  - `canary`: a canary rejecting the version -> `halted`; all canaries applied
    -> `verifying`; `ack_timeout_seconds` elapsed -> `halted`. `verifying`:
    after `health_window_seconds`, each canary needs at least 2 `engine_stats`
    samples from the newest sample at most 60 s before the phase start onwards
    (else `halted`, "stopped reporting"); with at least `min_health_queries`
    queries a SERVFAIL/queries ratio above `max_servfail_ratio` -> `halted`;
    otherwise `rolling`. `rolling`: a rejection -> `halted`; every connected
    engine applied -> `completed` (sets `stable_version`); `ack_timeout_seconds`
    elapsed -> `halted`. Disconnected engines get the version on reconnect.
  - Creation supersedes the group's open rollouts: a paused, non-immediate
    `change` supersedes only `pending`; a `rollback` marks `halted` rollouts
    `rolled_back`, supersedes the rest and sets `rollouts_paused`; anything else
    supersedes `pending`/`canary`/`verifying`/`rolling`/`halted`.
    `resume-rollouts` clears `rollouts_paused` and publishes a fresh version.
  - Target version of an engine, given the group's newest non-superseded
    rollout R: R `rolling`/`completed`/`rolled_back` -> R.version; R
    `canary`/`verifying` and the engine is a canary -> R.version; R `halted` and
    the engine applied R.version -> R.version; otherwise `stable_version`. An
    engine whose applied version is above its target is flagged `version_ahead`
    and never pushed.
  - Every instance runs `rollout.Controller`: tick `NEXORA_ROLLOUT_TICK` plus
    LISTEN `nexora_rollout`. Per open rollout: `BEGIN`,
    `pg_try_advisory_xact_lock(hashtext('nexora:rollout:' || id))` (skip when not
    acquired), `SELECT ... FOR UPDATE`, `rollout.Step` with `now()` from
    PostgreSQL, `UPDATE`, `pg_notify('nexora_rollout', engine_group_id)`,
    `COMMIT`. The hub LISTENs on `nexora_rollout` and pushes to its connected
    engines of that group whose target is above the version last sent; acks and
    rejections notify `nexora_rollout` so controllers step at once.

  ### Fleet health

  - Engine `status`: `revoked` (engine revoked), `ahead` (`version_ahead` or
    applied above target), `disconnected` (no live stream: `connected_instance`
    NULL or its instance heartbeat older than 15 s), `rejected`
    (`rejected_version` above applied), `current` (applied equals target),
    `behind`.
  - Metrics on every instance, read from PostgreSQL at scrape time:
    `nexora_mgmt_engines{engine_group,status}`,
    `nexora_mgmt_engines_disconnected` (non-revoked, non-deleted engines without
    a live stream whose `last_seen_at`, or `enrolled_at` when never seen, is
    older than 60 s), `nexora_mgmt_rollouts{engine_group,state}` (non-terminal
    and halted).

  ### Engine lifecycle

  - Join tokens: `name`, `engine_group_id`, `labels`, `expires_at` (TTL 60 s ..
    1 year), `max_uses` (NULL = unlimited), `uses`, `revoked_at`. Enroll errors
    (`PermissionDenied`): `join token unknown`, `join token expired`,
    `join token exhausted`, `join token revoked`.
  - `engine_certificates` columns: `serial`, `engine_id`, `not_before`,
    `not_after`, `issued_at`, `revoked_at`, `revoke_reason`; serial lowercase hex. Lifetime
    `NEXORA_ENGINE_CERT_TTL`. `engines.certificate_serial` holds the newest
    issued serial.
  - Every `Connect`, `GetBlob` and builtin `LogsService/Export` call looks up
    the caller's certificate serial: unknown engine or deleted engine ->
    `PermissionDenied` `unknown or deleted engine`; revoked engine, revoked or
    unknown serial, or a serial of another engine -> `PermissionDenied`
    `certificate revoked`. A `Connect` with a serial marks the engine's older
    unrevoked serials `superseded`.
  - Renewal: from 2/3 of the lifetime the engine sends
    `CertificateRequest{csr_der, reason: RENEWAL}` (CSR CN = engine id, new
    P-256 key); the instance issues (at most once per engine per 10 s) and
    answers `CertificateIssued{cert_der, ca_der}`; the engine swaps
    `state_dir/identity` atomically (`identity.new` -> `identity`) and
    reconnects.
  - Rotation: `POST /api/v1/engines/{id}/rotate-certificate` sets
    `cert_rotate_requested_at` and notifies `nexora_engine_rotate`; the instance
    holding the stream (and every `Connect` while the request is outstanding)
    sends `RenewCertificate{reason: ROTATE}`; issuing clears it.
  - Revocation: `POST /api/v1/engines/{id}/revoke` sets `engines.revoked_at`,
    revokes every certificate (`revoked`) and notifies `nexora_engine_revoked`;
    instances end that engine's streams with `PermissionDenied`
    `certificate revoked`. A revoked engine keeps serving its last snapshot,
    sets `nexora_control_revoked 1` and retries every 300 s (±10%); joining
    again needs `state_dir/identity` removed and a new join token.
    `DELETE /api/v1/engines/{id}` revokes and sets `deleted_at`.

  ### Distribution

  - `.github/workflows/images.yml` builds `nexora-engine` and `nexora-mgmt`
    natively on `arc-azrtydxb-publish` (arm64) and `arc-azrtydxb-amd64-publish`
    (amd64), pushes by digest to `192.168.10.131:5000/azrtydxb`, and merges
    digests into `:sha-<7>` (`:v*` on tags) plus `:main` on main.
  - `deploy/compose/` runs PostgreSQL, one mgmt, one engine (profile `engine`)
    and an optional OpenTelemetry Collector (profile `otel`).
  - `deploy/helm/nexora`: mgmt Deployment (`migrate` init container), one
    engine workload per engine group (`DaemonSet` or `Deployment`, node
    selector, hostPath state), per-group DNS Service, CNPG `Cluster` or an
    external database secret, optional collector, ServiceMonitor,
    PrometheusRule, `values.schema.json`. Static checks live in
    `deploy/deploytest`.
  - CLI: `nexora-mgmt engine-group create`, `nexora-mgmt join-token create`,
    `nexora-mgmt ca init --if-missing`.
  ```
- [ ] Replace the body of `## Deployment on kw` with:
  ```markdown
  Namespace `nexora`. `scripts/kw-deploy.sh` applies `deploy/kw/namespace.yaml`,
  `opensearch.yaml`, `cnpg-cluster.yaml` (CNPG `nexora-db`, 2 instances),
  `otelcol.yaml` (traces to `jaeger.observability.svc:4317`, query logs to
  OpenSearch) and `blocklist.yaml`, then installs `deploy/helm/nexora` with
  `deploy/kw/values-kw.yaml`: `nexora-mgmt` Deployment (2 replicas) behind ingress
  `nexora.kw.local` (class `nginx`, ClusterIssuer `cluster-ca`, HTTPS only) and a
  gRPC LoadBalancer `192.168.10.135:9443`; engine DaemonSets per engine group,
  selected by node label `nexora.io/engine-group`: `nexora-engine` (group
  `default`, every node without the label, DNS/DoT/DoH/DoQ LoadBalancer
  `nexora-dns` `192.168.10.136`, `externalTrafficPolicy: Local`) and
  `nexora-engine-edge-b` (group `edge-b`, nodes labelled `edge-b`: `worker-24`,
  `worker-25`; LoadBalancer `nexora-dns-edge-b` `192.168.10.137`,
  `externalTrafficPolicy: Cluster`); engine state on hostPath
  `/var/lib/nexora/<workload>`; ServiceMonitor and PrometheusRule in `monitoring`
  with label `release: kps`. `deploy/kw/bootstrap.sh` configures the API (admin,
  upstreams, block list, RPZ, engine group `edge-b`, join token secrets
  `nexora-join-token` and `nexora-join-token-edge-b`). Acceptance:
  `scripts/kw-acceptance.sh` runs `TestKwSmoke` (which includes `TestKwSmokeM4`)
  and `TestKwFullProduct`.
  ```
- [ ] Add to the repository layout block: `mgmt/internal/rollout                    staged rollout state machine, creation, controller`, `mgmt/internal/fleet                      engine groups, engine views and targets, join tokens, certificates, fleet metrics`, `deploy/deploytest/                       helm/compose/workflow/docs static tests`, `engine/src/cert_renewal.rs               certificate renewal timing, CSR, atomic identity swap`; and in the Contract section the line `- M5: EngineMessage.cert_request (500), ServerMessage.cert_issued (500) and ServerMessage.renew_certificate (501) carry certificate renewal and rotation; M5 adds no ConfigSnapshot or Stats field (fleet health is derived from the M1 Stats samples).`
- [ ] Run `grep -c "^## Fleet (M5)" docs/architecture.md` and expect `1`; run `grep -c "192.168.10.13[5-7]" docs/architecture.md` and expect `3`; run `grep -c "3 replicas, one per" docs/architecture.md` and expect `0`.
- [ ] Commit: `git add docs/architecture.md && git commit -m "docs(architecture): settle M5 fleet design"`.

## Task 2: Fleet schema migration

Files: `mgmt/migrations/00500_fleet.sql` (all M5 tables, columns and backfills), `mgmt/internal/store/fleet.go` (fleet constants, down-migration helper), `mgmt/internal/store/fleet_migration_test.go` (schema test)
Interfaces: `store.DefaultEngineGroupID uuid.UUID`; `store.EngineScopedTables []string`; `func (s *Store) MigrateDownTo(ctx context.Context, version int64) error`; tables and columns exactly as in the migration below.

- [ ] Write the failing test `mgmt/internal/store/fleet_migration_test.go`:
  ```go
  package store_test

  import (
  	"context"
  	"strings"
  	"testing"

  	"github.com/piwi3910/nexora/mgmt/internal/store"
  	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
  )

  func TestFleetMigration(t *testing.T) {
  	ctx := context.Background()
  	st := storetest.New(t)
  	db := st.Pool

  	var name string
  	if err := db.QueryRow(ctx, `select name from engine_groups where id = $1`, store.DefaultEngineGroupID).Scan(&name); err != nil || name != "default" {
  		t.Fatalf("default engine group: name=%q err=%v", name, err)
  	}
  	hasColumn := func(table, column string) bool {
  		var n int
  		err := db.QueryRow(ctx, `select count(*) from information_schema.columns
  			where table_schema = 'public' and table_name = $1 and column_name = $2`, table, column).Scan(&n)
  		return err == nil && n == 1
  	}
  	for _, table := range store.EngineScopedTables {
  		if !hasColumn(table, "engine_group_id") {
  			t.Errorf("%s.engine_group_id missing", table)
  		}
  	}
  	if !hasColumn("rewrites", "group_id") {
  		t.Error("rewrites.group_id (the M2 policy group) must stay")
  	}
  	for _, c := range []string{"engine_group_id", "labels", "revoked_at", "cert_rotate_requested_at", "revision"} {
  		if !hasColumn("engines", c) {
  			t.Errorf("engines.%s missing", c)
  		}
  	}
  	for _, c := range []string{"engine_group_id", "labels", "max_uses"} {
  		if !hasColumn("join_tokens", c) {
  			t.Errorf("join_tokens.%s missing", c)
  		}
  	}
  	for _, tbl := range []string{"group_snapshots", "rollouts", "engine_certificates"} {
  		var reg *string
  		if err := db.QueryRow(ctx, `select to_regclass('public.' || $1)::text`, tbl).Scan(&reg); err != nil || reg == nil {
  			t.Errorf("table %s missing (err=%v)", tbl, err)
  		}
  	}
  	var def string
  	if err := db.QueryRow(ctx, `select indexdef from pg_indexes where indexname = 'rollouts_one_active_per_group'`).Scan(&def); err != nil ||
  		!strings.Contains(def, "UNIQUE") || !strings.Contains(def, "verifying") {
  		t.Errorf("rollouts_one_active_per_group: %q err=%v", def, err)
  	}
  	var nullable string
  	if err := db.QueryRow(ctx, `select is_nullable from information_schema.columns
  		where table_name = 'config_versions' and column_name = 'snapshot'`).Scan(&nullable); err != nil || nullable != "YES" {
  		t.Errorf("config_versions.snapshot nullable = %q err=%v", nullable, err)
  	}

  	// Positive path first: a valid canary configuration is accepted.
  	if _, err := db.Exec(ctx, `update engine_groups set rollout_strategy = 'canary', canary_count = 1 where id = $1`, store.DefaultEngineGroupID); err != nil {
  		t.Fatalf("valid canary configuration rejected: %v", err)
  	}
  	if _, err := db.Exec(ctx, `update engine_groups set canary_count = 0, canary_percent = 0 where id = $1`, store.DefaultEngineGroupID); err == nil {
  		t.Fatal("canary strategy without a canary size was accepted")
  	}
  	if _, err := db.Exec(ctx, `insert into engine_groups (name) values ('Bad_Name')`); err == nil {
  		t.Fatal("engine group name outside [a-z0-9-] was accepted")
  	}
  	if _, err := db.Exec(ctx, `insert into policy_groups (name) values ('p1')`); err != nil {
  		t.Fatal(err)
  	}
  	if _, err := db.Exec(ctx, `insert into rewrites (group_id, engine_group_id, name, type, value)
  		select id, $1, 'a.test', 'A', '192.0.2.1' from policy_groups where name = 'p1'`, store.DefaultEngineGroupID); err == nil {
  		t.Fatal("a policy group rewrite with its own engine_group_id was accepted")
  	}
  }

  func TestFleetMigrationBackfillsCertificatesAndDownUp(t *testing.T) {
  	ctx := context.Background()
  	st := storetest.New(t)
  	if err := st.MigrateDownTo(ctx, 499); err != nil {
  		t.Fatalf("down to 499: %v", err)
  	}
  	if _, err := st.Pool.Exec(ctx, `insert into engines (node_name, certificate_serial, enrolled_at)
  		values ('old-engine', 'a1b2', '2026-01-01T00:00:00Z')`); err != nil {
  		t.Fatal(err)
  	}
  	if err := st.Migrate(ctx); err != nil {
  		t.Fatalf("up again: %v", err)
  	}
  	var notAfter, group string
  	if err := st.Pool.QueryRow(ctx, `select c.not_after::date::text, e.engine_group_id::text from engine_certificates c
  		join engines e on e.id = c.engine_id where c.serial = 'a1b2'`).Scan(&notAfter, &group); err != nil {
  		t.Fatalf("backfilled certificate: %v", err)
  	}
  	if notAfter != "2027-01-01" || group != store.DefaultEngineGroupID.String() {
  		t.Fatalf("backfill not_after %s group %s", notAfter, group)
  	}
  }
  ```
- [ ] Add `TestFleetMigrationPopulatedM4Database` to the same file: migrate down to `403`, insert M1–M4 rows (a join token, a live and a soft-deleted engine with an upper-case serial, an upstream, a policy group with a rewrite, a global rewrite, config versions 1 and 2 with snapshots), migrate up, and assert: both engines and the join token are in the default group with default labels/revision and `max_uses` NULL; no scoped row has an engine group; exactly one group snapshot (version 2) with a completed `change`/`all_at_once` rollout and `stable_version` 2; certificates backfilled with lower-case serials, M1 validity (UTC dates) and `revoked` only for the deleted engine; an M4-style upstream insert and a snapshot-less config version still succeed; migrating down to `403` again keeps both engines.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/store/ -run TestFleetMigration -count=1` and expect FAIL with `undefined: store.DefaultEngineGroupID`.
- [ ] Create `mgmt/internal/store/fleet.go`:
  ```go
  package store

  import (
  	"context"
  	"fmt"

  	"github.com/google/uuid"
  	"github.com/jackc/pgx/v5/stdlib"
  	"github.com/pressly/goose/v3"

  	"github.com/piwi3910/nexora/mgmt/migrations"
  )

  // DefaultEngineGroupID is the engine group every engine and join token falls back to.
  var DefaultEngineGroupID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

  // EngineScopedTables are the configuration tables whose rows apply to every engine group
  // (engine_group_id IS NULL) or to one engine group.
  var EngineScopedTables = []string{
  	"upstreams", "filter_lists", "policy_groups", "rewrites", "forward_zones", "zones", "rpz_zones",
  }

  // MigrateDownTo rolls the schema back to version (tests and emergency operations only).
  func (s *Store) MigrateDownTo(ctx context.Context, version int64) error {
  	db := stdlib.OpenDBFromPool(s.Pool)
  	defer db.Close()
  	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
  	if err != nil {
  		return err
  	}
  	if _, err := provider.DownTo(ctx, version); err != nil {
  		return fmt.Errorf("migrate down to %d: %w", version, MapError(err))
  	}
  	return nil
  }
  ```
- [ ] Create `mgmt/migrations/00500_fleet.sql`:
  ```sql
  -- +goose Up
  CREATE TABLE engine_groups (
      id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
      name                  text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
      description           text NOT NULL DEFAULT '' CHECK (length(description) <= 1024),
      upstream_mode         text NOT NULL DEFAULT 'inherit' CHECK (upstream_mode IN ('inherit', 'override')),
      extra_acl_cidrs       cidr[] NOT NULL DEFAULT '{}',
      otlp_endpoint         text NOT NULL DEFAULT '',
      rollout_strategy      text NOT NULL DEFAULT 'all_at_once' CHECK (rollout_strategy IN ('all_at_once', 'canary')),
      canary_count          integer NOT NULL DEFAULT 0 CHECK (canary_count >= 0),
      canary_percent        integer NOT NULL DEFAULT 0 CHECK (canary_percent BETWEEN 0 AND 100),
      ack_timeout_seconds   integer NOT NULL DEFAULT 60 CHECK (ack_timeout_seconds BETWEEN 5 AND 3600),
      health_window_seconds integer NOT NULL DEFAULT 30 CHECK (health_window_seconds BETWEEN 20 AND 3600),
      max_servfail_ratio    double precision NOT NULL DEFAULT 0.05 CHECK (max_servfail_ratio >= 0 AND max_servfail_ratio <= 1),
      min_health_queries    integer NOT NULL DEFAULT 100 CHECK (min_health_queries >= 0),
      rollouts_paused       boolean NOT NULL DEFAULT false,
      stable_version        bigint,
      revision              bigint NOT NULL DEFAULT 1,
      created_at            timestamptz NOT NULL DEFAULT now(),
      updated_at            timestamptz NOT NULL DEFAULT now(),
      CONSTRAINT engine_groups_canary_size CHECK (rollout_strategy = 'all_at_once' OR canary_count > 0 OR canary_percent > 0)
  );
  INSERT INTO engine_groups (id, name, description)
  VALUES ('00000000-0000-0000-0000-000000000001', 'default', 'Engines not assigned to another engine group');

  ALTER TABLE engines
      ADD COLUMN engine_group_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000001'
          REFERENCES engine_groups (id) ON DELETE RESTRICT,
      ADD COLUMN labels jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(labels) = 'object'),
      ADD COLUMN revoked_at timestamptz,
      ADD COLUMN cert_rotate_requested_at timestamptz,
      ADD COLUMN revision bigint NOT NULL DEFAULT 1;
  CREATE INDEX engines_engine_group ON engines (engine_group_id);

  ALTER TABLE join_tokens
      ADD COLUMN engine_group_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000001'
          REFERENCES engine_groups (id) ON DELETE CASCADE,
      ADD COLUMN labels jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(labels) = 'object'),
      ADD COLUMN max_uses integer CHECK (max_uses IS NULL OR max_uses >= 1);

  ALTER TABLE upstreams     ADD COLUMN engine_group_id uuid REFERENCES engine_groups (id) ON DELETE RESTRICT;
  ALTER TABLE filter_lists  ADD COLUMN engine_group_id uuid REFERENCES engine_groups (id) ON DELETE RESTRICT;
  ALTER TABLE policy_groups ADD COLUMN engine_group_id uuid REFERENCES engine_groups (id) ON DELETE RESTRICT;
  ALTER TABLE forward_zones ADD COLUMN engine_group_id uuid REFERENCES engine_groups (id) ON DELETE RESTRICT;
  ALTER TABLE zones         ADD COLUMN engine_group_id uuid REFERENCES engine_groups (id) ON DELETE RESTRICT;
  ALTER TABLE rpz_zones     ADD COLUMN engine_group_id uuid REFERENCES engine_groups (id) ON DELETE RESTRICT;
  -- Only global rewrites have their own engine group; policy group rewrites follow their policy group.
  ALTER TABLE rewrites
      ADD COLUMN engine_group_id uuid REFERENCES engine_groups (id) ON DELETE RESTRICT,
      ADD CONSTRAINT rewrites_engine_group_global_only CHECK (group_id IS NULL OR engine_group_id IS NULL);
  CREATE INDEX upstreams_engine_group ON upstreams (engine_group_id);
  CREATE INDEX filter_lists_engine_group ON filter_lists (engine_group_id);
  CREATE INDEX policy_groups_engine_group ON policy_groups (engine_group_id);
  CREATE INDEX rewrites_engine_group ON rewrites (engine_group_id);
  CREATE INDEX forward_zones_engine_group ON forward_zones (engine_group_id);
  CREATE INDEX zones_engine_group ON zones (engine_group_id);
  CREATE INDEX rpz_zones_engine_group ON rpz_zones (engine_group_id);

  ALTER TABLE config_versions ALTER COLUMN snapshot DROP NOT NULL;

  CREATE TABLE group_snapshots (
      version         bigint NOT NULL REFERENCES config_versions (version) ON DELETE CASCADE,
      engine_group_id uuid NOT NULL REFERENCES engine_groups (id) ON DELETE CASCADE,
      snapshot        bytea NOT NULL,
      content_sha256  text NOT NULL CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
      created_at      timestamptz NOT NULL DEFAULT now(),
      PRIMARY KEY (version, engine_group_id)
  );
  CREATE INDEX group_snapshots_group_version ON group_snapshots (engine_group_id, version DESC);

  CREATE TABLE rollouts (
      id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
      engine_group_id   uuid NOT NULL REFERENCES engine_groups (id) ON DELETE CASCADE,
      version           bigint NOT NULL,
      from_version      bigint,
      kind              text NOT NULL CHECK (kind IN ('change', 'rollback', 'republish')),
      strategy          text NOT NULL CHECK (strategy IN ('all_at_once', 'canary')),
      state             text NOT NULL CHECK (state IN
                          ('pending', 'canary', 'verifying', 'rolling', 'completed', 'halted', 'rolled_back', 'superseded')),
      params            jsonb NOT NULL,
      canary_engine_ids uuid[] NOT NULL DEFAULT '{}',
      phase_started_at  timestamptz,
      halt_reason       text NOT NULL DEFAULT '',
      created_by        text NOT NULL,
      created_at        timestamptz NOT NULL DEFAULT now(),
      updated_at        timestamptz NOT NULL DEFAULT now(),
      finished_at       timestamptz,
      FOREIGN KEY (version, engine_group_id) REFERENCES group_snapshots (version, engine_group_id) ON DELETE CASCADE
  );
  CREATE UNIQUE INDEX rollouts_one_active_per_group ON rollouts (engine_group_id)
      WHERE state IN ('canary', 'verifying', 'rolling');
  CREATE INDEX rollouts_group_version ON rollouts (engine_group_id, version DESC);
  CREATE INDEX rollouts_open ON rollouts (created_at) WHERE state IN ('pending', 'canary', 'verifying', 'rolling');

  -- The newest pre-M5 version becomes the default group's completed, stable snapshot.
  INSERT INTO group_snapshots (version, engine_group_id, snapshot, content_sha256)
  SELECT version, '00000000-0000-0000-0000-000000000001', snapshot, encode(sha256(snapshot), 'hex')
  FROM config_versions WHERE snapshot IS NOT NULL ORDER BY version DESC LIMIT 1;
  INSERT INTO rollouts (engine_group_id, version, kind, strategy, state, params, created_by, phase_started_at, finished_at)
  SELECT engine_group_id, version, 'change', 'all_at_once', 'completed', '{"strategy":"all_at_once"}'::jsonb, 'migration', now(), now()
  FROM group_snapshots;
  UPDATE engine_groups SET stable_version = (SELECT max(version) FROM group_snapshots)
  WHERE id = '00000000-0000-0000-0000-000000000001';

  CREATE TABLE engine_certificates (
      serial        text PRIMARY KEY CHECK (serial ~ '^[0-9a-f]+$'),
      engine_id     uuid NOT NULL REFERENCES engines (id) ON DELETE CASCADE,
      not_before    timestamptz NOT NULL,
      not_after     timestamptz NOT NULL,
      issued_at     timestamptz NOT NULL DEFAULT now(),
      revoked_at    timestamptz,
      revoke_reason text CHECK (revoke_reason IN ('revoked', 'superseded')),
      CHECK ((revoked_at IS NULL) = (revoke_reason IS NULL))
  );
  CREATE INDEX engine_certificates_engine ON engine_certificates (engine_id, issued_at DESC);
  -- M1 issued every engine certificate with NotBefore now-1h and NotAfter now+365d at enrollment.
  INSERT INTO engine_certificates (serial, engine_id, not_before, not_after, issued_at, revoked_at, revoke_reason)
  SELECT lower(certificate_serial), id, enrolled_at - interval '1 hour', enrolled_at + interval '365 days', enrolled_at,
         deleted_at, CASE WHEN deleted_at IS NOT NULL THEN 'revoked' END
  FROM engines WHERE certificate_serial ~ '^[0-9a-fA-F]+$'
  ON CONFLICT (serial) DO NOTHING;

  -- +goose Down
  DROP TABLE engine_certificates;
  DROP TABLE rollouts;
  DROP TABLE group_snapshots;
  DELETE FROM config_versions WHERE snapshot IS NULL;
  ALTER TABLE config_versions ALTER COLUMN snapshot SET NOT NULL;
  ALTER TABLE rewrites DROP CONSTRAINT rewrites_engine_group_global_only, DROP COLUMN engine_group_id;
  ALTER TABLE rpz_zones DROP COLUMN engine_group_id;
  ALTER TABLE zones DROP COLUMN engine_group_id;
  ALTER TABLE forward_zones DROP COLUMN engine_group_id;
  ALTER TABLE policy_groups DROP COLUMN engine_group_id;
  ALTER TABLE filter_lists DROP COLUMN engine_group_id;
  ALTER TABLE upstreams DROP COLUMN engine_group_id;
  ALTER TABLE join_tokens DROP COLUMN max_uses, DROP COLUMN labels, DROP COLUMN engine_group_id;
  ALTER TABLE engines DROP COLUMN revision, DROP COLUMN cert_rotate_requested_at, DROP COLUMN revoked_at,
      DROP COLUMN labels, DROP COLUMN engine_group_id;
  DROP TABLE engine_groups;
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/store/ -run 'TestFleetMigration' -count=1` and expect `ok` (both tests).
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/... -count=1'` and expect every package `ok` (the new columns are nullable or defaulted, so M1–M4 inserts are unchanged).
- [ ] Commit: `git add mgmt/migrations/00500_fleet.sql mgmt/internal/store/fleet.go mgmt/internal/store/fleet_migration_test.go && git commit -m "feat(store): fleet schema (engine groups, group snapshots, rollouts, certificates)"`.

## Task 3: Contract additions for certificate renewal

Files: `proto/nexora/control/v1/control.proto` (contract), `gen/go/nexora/control/v1/control.pb.go` and `control_grpc.pb.go` (regenerated on the laptop), `mgmt/internal/control/contract_m5_test.go` (field numbers and wire round trip), `engine/src/control.rs` (exhaustive match over the new server messages)
Interfaces: Go `controlv1.CertificateRequest{CsrDer []byte; Reason controlv1.CertificateRequest_Reason}`, `controlv1.CertificateIssued{CertDer, CaDer []byte}`, `controlv1.RenewCertificate{Reason controlv1.CertificateRequest_Reason}`, oneof wrappers `controlv1.EngineMessage_CertRequest`, `controlv1.ServerMessage_CertIssued`, `controlv1.ServerMessage_RenewCertificate`; Rust `crate::proto::{CertificateRequest, CertificateIssued, RenewCertificate}`, `crate::proto::certificate_request::Reason`, `engine_message::Msg::CertRequest`, `server_message::Msg::{CertIssued, RenewCertificate}`.

- [ ] Write the failing test `mgmt/internal/control/contract_m5_test.go`:
  ```go
  package control_test

  import (
  	"testing"

  	"google.golang.org/protobuf/proto"
  	"google.golang.org/protobuf/reflect/protoreflect"

  	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
  )

  func TestM5ContractFieldNumbers(t *testing.T) {
  	srv := (&controlv1.ServerMessage{}).ProtoReflect().Descriptor()
  	eng := (&controlv1.EngineMessage{}).ProtoReflect().Descriptor()
  	for _, c := range []struct {
  		msg   protoreflect.MessageDescriptor
  		field protoreflect.Name
  		num   protoreflect.FieldNumber
  	}{
  		{eng, "cert_request", 500},
  		{srv, "cert_issued", 500},
  		{srv, "renew_certificate", 501},
  	} {
  		f := c.msg.Fields().ByName(c.field)
  		if f == nil {
  			t.Fatalf("%s.%s missing", c.msg.FullName(), c.field)
  		}
  		if f.Number() != c.num || f.ContainingOneof() == nil || f.ContainingOneof().Name() != "msg" {
  			t.Errorf("%s.%s = %d (oneof %v), want %d in oneof msg", c.msg.FullName(), c.field, f.Number(), f.ContainingOneof(), c.num)
  		}
  	}
  	for _, m := range []protoreflect.MessageDescriptor{
  		(&controlv1.ConfigSnapshot{}).ProtoReflect().Descriptor(), (&controlv1.Stats{}).ProtoReflect().Descriptor(),
  	} {
  		for i := 0; i < m.Fields().Len(); i++ {
  			if n := m.Fields().Get(i).Number(); n >= 500 {
  				t.Errorf("%s has an M5 field %d; M5 adds none there", m.FullName(), n)
  			}
  		}
  	}

  	in := &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_CertRequest{CertRequest: &controlv1.CertificateRequest{
  		CsrDer: []byte{0x30, 0x01}, Reason: controlv1.CertificateRequest_REASON_ROTATE}}}
  	raw, err := proto.Marshal(in)
  	if err != nil {
  		t.Fatal(err)
  	}
  	var out controlv1.EngineMessage
  	if err := proto.Unmarshal(raw, &out); err != nil || out.GetCertRequest().GetReason() != controlv1.CertificateRequest_REASON_ROTATE {
  		t.Fatalf("round trip: %v %v", out.GetCertRequest(), err)
  	}
  	issued := &controlv1.ServerMessage{Msg: &controlv1.ServerMessage_CertIssued{CertIssued: &controlv1.CertificateIssued{CertDer: []byte{1}, CaDer: []byte{2}}}}
  	renew := &controlv1.ServerMessage{Msg: &controlv1.ServerMessage_RenewCertificate{RenewCertificate: &controlv1.RenewCertificate{
  		Reason: controlv1.CertificateRequest_REASON_RENEWAL}}}
  	for _, m := range []proto.Message{issued, renew} {
  		if _, err := proto.Marshal(m); err != nil {
  			t.Fatal(err)
  		}
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/control/ -run TestM5ContractFieldNumbers -count=1` and expect FAIL with `undefined: controlv1.EngineMessage_CertRequest`.
- [ ] Edit `proto/nexora/control/v1/control.proto`: inside `oneof msg` of `message EngineMessage` add `CertificateRequest cert_request = 500; // M5`; inside `oneof msg` of `message ServerMessage` add `CertificateIssued cert_issued = 500;       // M5: reply to EngineMessage.cert_request` and `RenewCertificate renew_certificate = 501; // M5: send a CertificateRequest now`; append:
  ```proto
  // ---- M5: engine certificate renewal and rotation ----

  // Engine -> server: a PKCS#10 CSR for a new ECDSA P-256 key; the subject CN must be the engine id.
  message CertificateRequest {
    enum Reason {
      REASON_UNSPECIFIED = 0;
      REASON_RENEWAL = 1; // 2/3 of the certificate lifetime passed
      REASON_ROTATE = 2;  // answering RenewCertificate
    }
    bytes csr_der = 1;
    Reason reason = 2;
  }

  // Server -> engine: the issued client certificate and the CA that signed it (both DER).
  message CertificateIssued {
    bytes cert_der = 1;
    bytes ca_der = 2;
  }

  // Server -> engine: an operator requested rotation; send a CertificateRequest.
  message RenewCertificate {
    CertificateRequest.Reason reason = 1;
  }
  ```
- [ ] Regenerate the Go code in the dev pod, whose plugins are the pinned ones the committed headers name (protoc 3.21.12, protoc-gen-go v1.36.12, protoc-gen-go-grpc 1.6.2; the laptop has newer ones): run the `protoc` line of `make proto` into a pod temp directory and copy `control.pb.go` and `control_grpc.pb.go` back into `gen/go/nexora/control/v1/` (only `control.pb.go` changes; the OpenAPI steps are untouched because the HTTP API does not change).
- [ ] In `engine/src/control.rs` `session`, extend the `match msg.msg` with `Some(ServerMsg::CertIssued(_)) | Some(ServerMsg::RenewCertificate(_)) => {}` directly above `None => {}` (Task 9 gives both arms their behaviour), so the match stays exhaustive.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/control/ -run TestM5ContractFieldNumbers -count=1 && cargo check --locked -p nexora-engine'` and expect `ok` and `Finished`.
- [ ] Commit: `git add proto/nexora/control/v1/control.proto gen/go/nexora/control/v1 mgmt/internal/control/contract_m5_test.go engine/src/control.rs && git commit -m "feat(proto): certificate renewal and rotation messages (fields 500+)"`.

## Task 4: Rollout state machine

Files: `mgmt/internal/rollout/rollout.go` (pure state machine, canary selection, target version), `mgmt/internal/rollout/rollout_test.go` (table of transitions)
Interfaces:

```go
package rollout
type State string // Pending, Canary, Verifying, Rolling, Completed, Halted, RolledBack, Superseded
func (s State) Terminal() bool
type Strategy string // AllAtOnce "all_at_once", CanaryStrategy "canary"
type Kind string     // KindChange "change", KindRollback "rollback", KindRepublish "republish"
const CanaryLabel = "nexora.io/canary"
type Params struct { Strategy Strategy; CanaryCount, CanaryPercent, AckTimeoutSeconds, HealthWindowSeconds int; MaxServfailRatio float64; MinHealthQueries uint64 } // JSON tags snake_case
type Rollout struct { ID, EngineGroupID uuid.UUID; Version uint64; Kind Kind; State State; CanaryEngineIDs []uuid.UUID; PhaseStartedAt time.Time; HaltReason string; Params Params }
type Health struct { Samples int; Queries, Servfail uint64 }
type Engine struct { ID uuid.UUID; Name string; Labels map[string]string; Connected bool; AppliedVersion, RejectedVersion uint64; RejectedReason string; Health Health }
type Observation struct { Now time.Time; GroupPaused bool; Engines []Engine }
func Step(r Rollout, obs Observation) (Rollout, bool)
func SelectCanaries(p Params, engines []Engine) []uuid.UUID
func Target(e Engine, stable uint64, latest *Rollout) uint64
```

`Engine.Name` is `engines.node_name`; `Engine.RejectedVersion` is 0 when `engines.rejected_version` is NULL.

- [ ] Write the failing test `mgmt/internal/rollout/rollout_test.go`:
  ```go
  package rollout

  import (
  	"strings"
  	"testing"
  	"time"

  	"github.com/google/uuid"
  )

  var t0 = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

  func eng(name string, connected bool, applied uint64) Engine {
  	return Engine{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)), Name: name, Connected: connected,
  		AppliedVersion: applied, Labels: map[string]string{}}
  }

  func canaryParams() Params {
  	return Params{Strategy: CanaryStrategy, CanaryCount: 1, AckTimeoutSeconds: 60, HealthWindowSeconds: 30,
  		MaxServfailRatio: 0.05, MinHealthQueries: 100}
  }

  func TestAllAtOnceCompletesWhenConnectedEnginesApply(t *testing.T) {
  	r := Rollout{Version: 7, Kind: KindChange, State: Pending, Params: Params{Strategy: AllAtOnce, AckTimeoutSeconds: 60}}
  	es := []Engine{eng("a", true, 6), eng("b", true, 6), eng("c", false, 3)}
  	r, ch := Step(r, Observation{Now: t0, Engines: es})
  	if !ch || r.State != Rolling || !r.PhaseStartedAt.Equal(t0) {
  		t.Fatalf("pending -> %s (changed=%v)", r.State, ch)
  	}
  	if r, ch = Step(r, Observation{Now: t0.Add(5 * time.Second), Engines: es}); ch {
  		t.Fatalf("advanced to %s before any ack", r.State)
  	}
  	es[0].AppliedVersion, es[1].AppliedVersion = 7, 7
  	if r, ch = Step(r, Observation{Now: t0.Add(6 * time.Second), Engines: es}); !ch || r.State != Completed {
  		t.Fatalf("got %s, want completed; a disconnected engine must not block", r.State)
  	}
  }

  func TestCanaryHappyPath(t *testing.T) {
  	es := []Engine{eng("a", true, 6), eng("b", true, 6), eng("c", true, 6)}
  	es[2].Labels[CanaryLabel] = "true"
  	r := Rollout{Version: 7, Kind: KindChange, State: Pending, Params: canaryParams()}
  	r, _ = Step(r, Observation{Now: t0, Engines: es})
  	if r.State != Canary || len(r.CanaryEngineIDs) != 1 || r.CanaryEngineIDs[0] != es[2].ID {
  		t.Fatalf("state %s canaries %v, want canary [c]", r.State, r.CanaryEngineIDs)
  	}
  	es[2].AppliedVersion = 7
  	r, _ = Step(r, Observation{Now: t0.Add(3 * time.Second), Engines: es})
  	if r.State != Verifying {
  		t.Fatalf("state %s, want verifying", r.State)
  	}
  	es[2].Health = Health{Samples: 3, Queries: 1000, Servfail: 10}
  	if r, _ = Step(r, Observation{Now: t0.Add(20 * time.Second), Engines: es}); r.State != Verifying {
  		t.Fatalf("left verifying before the health window: %s", r.State)
  	}
  	if r, _ = Step(r, Observation{Now: t0.Add(34 * time.Second), Engines: es}); r.State != Rolling {
  		t.Fatalf("state %s, want rolling (1%% servfail is under 5%%)", r.State)
  	}
  	es[0].AppliedVersion, es[1].AppliedVersion = 7, 7
  	if r, _ = Step(r, Observation{Now: t0.Add(40 * time.Second), Engines: es}); r.State != Completed {
  		t.Fatalf("state %s, want completed", r.State)
  	}
  }

  func verifyingWith(h Health) (Rollout, []Engine) {
  	es := []Engine{eng("a", true, 7), eng("b", true, 6)}
  	es[0].Health = h
  	return Rollout{Version: 7, Kind: KindChange, State: Verifying, PhaseStartedAt: t0,
  		CanaryEngineIDs: []uuid.UUID{es[0].ID}, Params: canaryParams()}, es
  }

  func TestHaltsOnServfailRatio(t *testing.T) {
  	r, es := verifyingWith(Health{Samples: 3, Queries: 400, Servfail: 380})
  	r, ch := Step(r, Observation{Now: t0.Add(31 * time.Second), Engines: es})
  	if !ch || r.State != Halted || !strings.Contains(r.HaltReason, "engine a servfail ratio 0.950 > 0.050") {
  		t.Fatalf("state %s reason %q", r.State, r.HaltReason)
  	}
  	if r, ch = Step(r, Observation{Now: t0.Add(60 * time.Second), Engines: es}); ch || r.State != Halted {
  		t.Fatalf("halted rollout moved on its own to %s", r.State)
  	}
  }

  func TestLowTrafficPassesGate(t *testing.T) {
  	r, es := verifyingWith(Health{Samples: 3, Queries: 20, Servfail: 20})
  	if r, _ = Step(r, Observation{Now: t0.Add(31 * time.Second), Engines: es}); r.State != Rolling {
  		t.Fatalf("state %s, want rolling below min_health_queries", r.State)
  	}
  }

  func TestHaltsWhenCanaryStopsReporting(t *testing.T) {
  	r, es := verifyingWith(Health{Samples: 1, Queries: 5000})
  	if r, _ = Step(r, Observation{Now: t0.Add(31 * time.Second), Engines: es}); r.State != Halted ||
  		!strings.Contains(r.HaltReason, "stopped reporting") {
  		t.Fatalf("state %s reason %q", r.State, r.HaltReason)
  	}
  }

  func TestHaltsOnCanaryRejectAndAckTimeout(t *testing.T) {
  	es := []Engine{eng("a", true, 6), eng("b", true, 6)}
  	base := Rollout{Version: 7, Kind: KindChange, State: Canary, PhaseStartedAt: t0,
  		CanaryEngineIDs: []uuid.UUID{es[0].ID}, Params: canaryParams()}

  	rej := append([]Engine(nil), es...)
  	rej[0].RejectedVersion, rej[0].RejectedReason = 7, "cache max_bytes below 1048576"
  	if r, _ := Step(base, Observation{Now: t0.Add(time.Second), Engines: rej}); r.State != Halted ||
  		r.HaltReason != "engine a rejected version 7: cache max_bytes below 1048576" {
  		t.Fatalf("reject: state %s reason %q", r.State, r.HaltReason)
  	}
  	if r, ch := Step(base, Observation{Now: t0.Add(59 * time.Second), Engines: es}); ch {
  		t.Fatalf("halted before the ack timeout: %s", r.State)
  	}
  	if r, _ := Step(base, Observation{Now: t0.Add(61 * time.Second), Engines: es}); r.State != Halted ||
  		r.HaltReason != "engine a did not apply version 7 within 60s" {
  		t.Fatalf("timeout: state %s reason %q", r.State, r.HaltReason)
  	}
  }

  func TestPausedGroupHoldsChangesOnly(t *testing.T) {
  	es := []Engine{eng("a", true, 6)}
  	change := Rollout{Version: 8, Kind: KindChange, State: Pending, Params: canaryParams()}
  	if r, ch := Step(change, Observation{Now: t0, GroupPaused: true, Engines: es}); ch || r.State != Pending {
  		t.Fatalf("paused change moved to %s", r.State)
  	}
  	rb := Rollout{Version: 9, Kind: KindRollback, State: Pending, Params: canaryParams()}
  	if r, _ := Step(rb, Observation{Now: t0, GroupPaused: true, Engines: es}); r.State != Rolling {
  		t.Fatalf("rollback under pause went to %s, want rolling (all at once)", r.State)
  	}
  }

  func TestSelectCanaries(t *testing.T) {
  	es := []Engine{eng("d", true, 0), eng("b", true, 0), eng("a", false, 0), eng("c", true, 0), eng("e", true, 0)}
  	es[3].Labels[CanaryLabel] = "true" // c
  	got := SelectCanaries(Params{Strategy: CanaryStrategy, CanaryCount: 1, CanaryPercent: 50}, es)
  	// 4 connected, 50% -> 2; c first by label, then b by name
  	if len(got) != 2 || got[0] != es[3].ID || got[1] != es[1].ID {
  		t.Fatalf("got %v", got)
  	}
  	if got := SelectCanaries(Params{Strategy: CanaryStrategy, CanaryCount: 10}, es); len(got) != 3 {
  		t.Fatalf("canary set must leave one connected engine out, got %d", len(got))
  	}
  	if got := SelectCanaries(Params{Strategy: CanaryStrategy, CanaryPercent: 1}, es[:1]); len(got) != 1 {
  		t.Fatalf("single engine group: got %d canaries, want 1", len(got))
  	}
  	if got := SelectCanaries(Params{Strategy: CanaryStrategy, CanaryCount: 1}, []Engine{eng("x", false, 0)}); len(got) != 0 {
  		t.Fatalf("no connected engines must yield no canaries, got %v", got)
  	}
  }

  func TestTarget(t *testing.T) {
  	a, b := eng("a", true, 7), eng("b", true, 6)
  	canary := &Rollout{Version: 7, State: Verifying, CanaryEngineIDs: []uuid.UUID{a.ID}}
  	if Target(a, 6, canary) != 7 || Target(b, 6, canary) != 6 {
  		t.Fatal("canary phase: only canaries target the new version")
  	}
  	halted := &Rollout{Version: 7, State: Halted, CanaryEngineIDs: []uuid.UUID{a.ID}}
  	if Target(a, 6, halted) != 7 || Target(b, 6, halted) != 6 {
  		t.Fatal("halted: engines already on the version keep it, others stay stable")
  	}
  	for _, s := range []State{Rolling, Completed, RolledBack} {
  		if Target(b, 6, &Rollout{Version: 7, State: s}) != 7 {
  			t.Fatalf("%s: everyone targets the version", s)
  		}
  	}
  	if Target(b, 6, &Rollout{Version: 8, State: Pending}) != 6 || Target(b, 6, nil) != 6 {
  		t.Fatal("pending or no rollout: stable")
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/rollout/ -count=1` and expect FAIL with `undefined: Rollout`.
- [ ] Create `mgmt/internal/rollout/rollout.go`:
  ```go
  // Package rollout is the staged-rollout state machine. Step is pure: the controller loads a
  // rollout and an observation of its engine group under an advisory lock, calls Step, and
  // persists the result.
  package rollout

  import (
  	"fmt"
  	"math"
  	"slices"
  	"sort"
  	"time"

  	"github.com/google/uuid"
  )

  type State string

  const (
  	Pending    State = "pending"
  	Canary     State = "canary"
  	Verifying  State = "verifying"
  	Rolling    State = "rolling"
  	Completed  State = "completed"
  	Halted     State = "halted"
  	RolledBack State = "rolled_back"
  	Superseded State = "superseded"
  )

  // Terminal states are never stepped again.
  func (s State) Terminal() bool { return s == Completed || s == RolledBack || s == Superseded }

  type Strategy string

  const (
  	AllAtOnce      Strategy = "all_at_once"
  	CanaryStrategy Strategy = "canary"
  )

  type Kind string

  const (
  	KindChange    Kind = "change"
  	KindRollback  Kind = "rollback"
  	KindRepublish Kind = "republish"
  )

  // CanaryLabel on an engine makes it preferred for canary selection.
  const CanaryLabel = "nexora.io/canary"

  type Params struct {
  	Strategy            Strategy `json:"strategy"`
  	CanaryCount         int      `json:"canary_count"`
  	CanaryPercent       int      `json:"canary_percent"`
  	AckTimeoutSeconds   int      `json:"ack_timeout_seconds"`
  	HealthWindowSeconds int      `json:"health_window_seconds"`
  	MaxServfailRatio    float64  `json:"max_servfail_ratio"`
  	MinHealthQueries    uint64   `json:"min_health_queries"`
  }

  type Rollout struct {
  	ID              uuid.UUID
  	EngineGroupID   uuid.UUID
  	Version         uint64
  	Kind            Kind
  	State           State
  	CanaryEngineIDs []uuid.UUID
  	PhaseStartedAt  time.Time
  	HaltReason      string
  	Params          Params
  }

  // Health is measured from the newest engine_stats sample taken at most 60 s before the phase
  // start (the baseline) to the newest sample; Samples counts both ends.
  type Health struct {
  	Samples  int
  	Queries  uint64
  	Servfail uint64
  }

  type Engine struct {
  	ID              uuid.UUID
  	Name            string
  	Labels          map[string]string
  	Connected       bool
  	AppliedVersion  uint64
  	RejectedVersion uint64
  	RejectedReason  string
  	Health          Health
  }

  type Observation struct {
  	Now         time.Time
  	GroupPaused bool
  	Engines     []Engine // non-revoked, non-deleted engines of the rollout's engine group
  }

  func enter(r Rollout, s State, now time.Time) Rollout {
  	r.State, r.PhaseStartedAt = s, now
  	return r
  }

  func halt(r Rollout, now time.Time, reason string) Rollout {
  	r = enter(r, Halted, now)
  	r.HaltReason = reason
  	return r
  }

  func pick(es []Engine, ids []uuid.UUID) []Engine {
  	var out []Engine
  	for _, e := range es {
  		if slices.Contains(ids, e.ID) {
  			out = append(out, e)
  		}
  	}
  	return out
  }

  func rejection(r Rollout, es []Engine) (string, bool) {
  	for _, e := range es {
  		if e.RejectedVersion == r.Version {
  			return fmt.Sprintf("engine %s rejected version %d: %s", e.Name, r.Version, e.RejectedReason), true
  		}
  	}
  	return "", false
  }

  // firstUnapplied returns the first engine that has not applied r.Version. connectedOnly skips
  // disconnected engines (rolling phase); canaries must apply even when their stream dropped.
  func firstUnapplied(r Rollout, es []Engine, connectedOnly bool) (Engine, bool) {
  	for _, e := range es {
  		if connectedOnly && !e.Connected {
  			continue
  		}
  		if e.AppliedVersion < r.Version {
  			return e, true
  		}
  	}
  	return Engine{}, false
  }

  // Step advances r by at most one transition and reports whether it changed.
  func Step(r Rollout, obs Observation) (Rollout, bool) {
  	if r.State.Terminal() || r.State == Halted {
  		return r, false
  	}
  	ackTimeout := time.Duration(r.Params.AckTimeoutSeconds) * time.Second
  	switch r.State {
  	case Pending:
  		if obs.GroupPaused && r.Kind == KindChange {
  			return r, false
  		}
  		if r.Kind != KindChange || r.Params.Strategy == AllAtOnce {
  			return enter(r, Rolling, obs.Now), true
  		}
  		r.CanaryEngineIDs = SelectCanaries(r.Params, obs.Engines)
  		return enter(r, Canary, obs.Now), true

  	case Canary:
  		canaries := pick(obs.Engines, r.CanaryEngineIDs)
  		if reason, bad := rejection(r, canaries); bad {
  			return halt(r, obs.Now, reason), true
  		}
  		e, waiting := firstUnapplied(r, canaries, false)
  		if !waiting {
  			return enter(r, Verifying, obs.Now), true
  		}
  		if obs.Now.Sub(r.PhaseStartedAt) > ackTimeout {
  			return halt(r, obs.Now, fmt.Sprintf("engine %s did not apply version %d within %ds", e.Name, r.Version, r.Params.AckTimeoutSeconds)), true
  		}
  		return r, false

  	case Verifying:
  		canaries := pick(obs.Engines, r.CanaryEngineIDs)
  		if reason, bad := rejection(r, canaries); bad {
  			return halt(r, obs.Now, reason), true
  		}
  		if obs.Now.Sub(r.PhaseStartedAt) < time.Duration(r.Params.HealthWindowSeconds)*time.Second {
  			return r, false
  		}
  		for _, e := range canaries {
  			if e.Health.Samples < 2 {
  				return halt(r, obs.Now, fmt.Sprintf("engine %s stopped reporting health during verification", e.Name)), true
  			}
  			if e.Health.Queries < r.Params.MinHealthQueries || e.Health.Queries == 0 {
  				continue
  			}
  			ratio := float64(e.Health.Servfail) / float64(e.Health.Queries)
  			if ratio > r.Params.MaxServfailRatio {
  				return halt(r, obs.Now, fmt.Sprintf("engine %s servfail ratio %.3f > %.3f over %d queries",
  					e.Name, ratio, r.Params.MaxServfailRatio, e.Health.Queries)), true
  			}
  		}
  		return enter(r, Rolling, obs.Now), true

  	case Rolling:
  		if reason, bad := rejection(r, obs.Engines); bad {
  			return halt(r, obs.Now, reason), true
  		}
  		e, waiting := firstUnapplied(r, obs.Engines, true)
  		if !waiting {
  			r.State = Completed
  			return r, true
  		}
  		if obs.Now.Sub(r.PhaseStartedAt) > ackTimeout {
  			return halt(r, obs.Now, fmt.Sprintf("engine %s did not apply version %d within %ds", e.Name, r.Version, r.Params.AckTimeoutSeconds)), true
  		}
  	}
  	return r, false
  }

  // SelectCanaries picks connected engines, labelled canaries first, then by name.
  func SelectCanaries(p Params, engines []Engine) []uuid.UUID {
  	var connected []Engine
  	for _, e := range engines {
  		if e.Connected {
  			connected = append(connected, e)
  		}
  	}
  	n := len(connected)
  	if n == 0 {
  		return nil
  	}
  	want := max(p.CanaryCount, int(math.Ceil(float64(n)*float64(p.CanaryPercent)/100)), 1)
  	if n >= 2 {
  		want = min(want, n-1)
  	}
  	want = min(want, n)
  	sort.SliceStable(connected, func(i, j int) bool {
  		ci, cj := connected[i].Labels[CanaryLabel] == "true", connected[j].Labels[CanaryLabel] == "true"
  		if ci != cj {
  			return ci
  		}
  		return connected[i].Name < connected[j].Name
  	})
  	ids := make([]uuid.UUID, want)
  	for i := range ids {
  		ids[i] = connected[i].ID
  	}
  	return ids
  }

  // Target is the version engine e should run; latest is its engine group's newest
  // non-superseded rollout.
  func Target(e Engine, stable uint64, latest *Rollout) uint64 {
  	if latest == nil {
  		return stable
  	}
  	switch latest.State {
  	case Rolling, Completed, RolledBack:
  		return latest.Version
  	case Canary, Verifying:
  		if slices.Contains(latest.CanaryEngineIDs, e.ID) {
  			return latest.Version
  		}
  	case Halted:
  		if e.AppliedVersion == latest.Version {
  			return latest.Version
  		}
  	}
  	return stable
  }
  ```
  `fleet.TargetFor` (Task 6) passes the newest non-superseded rollout; a `rolled_back` rollout is always older than the rollback rollout that marked it, so the `RolledBack` case only keeps `Target` total.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/rollout/ -count=1 -race` and expect `ok`.
- [ ] Mutation check: temporarily change `ratio > r.Params.MaxServfailRatio` to `ratio >= 1` and rerun; expect FAIL with `--- FAIL: TestHaltsOnServfailRatio`. Restore the line and expect `ok` again.
- [ ] Add `mgmt/internal/rollout/rollout_property_test.go`: `TestStepTransitionTable` enumerates every state x kind x strategy x pause x phase age (0, 29, 30, 60, 61 s) x observation (every one- and two-engine combination of connected/applied/rejected/health, a sample of three-engine ones, with and without canary membership) and checks the invariants of each `Step` (only the allowed successor states, at most one transition, unchanged rollouts are returned untouched, identity/version/params never change, the phase starts at `Now`, halt reasons exactly on halting, canaries are distinct connected engines leaving one out, no unhealthy canary passes, `completed` only when every connected engine applied); `TestStepRandomWalk` walks random observation sequences and asserts no state is entered twice; `TestTargetTable` covers `Target` over every state.
- [ ] Commit: `git add mgmt/internal/rollout && git commit -m "feat(rollout): staged rollout state machine"`.

## Task 5: Per-group snapshots, rollout creation and publishing

Files: `mgmt/internal/rollout/create.go` (rollout creation and supersede rules), `mgmt/internal/snapshot/snapshot.go` (publish per engine group, `BuildForGroup`, `Latest`, `PublishRaw`, `Republish`), `mgmt/internal/snapshot/policy.go` (policy groups and rewrites filtered by engine group), `mgmt/internal/snapshot/authzones.go` (`AddAuthZones` takes the engine group), `mgmt/internal/store/resolution.go` (`LoadResolution` takes the engine group), `mgmt/internal/snapshot/authzones_test.go` and `mgmt/internal/snapshot/snapshot_test.go` (call sites), `mgmt/internal/snapshot/publish_m5_test.go` (per-group publish test), `e2e/harness/mgmt.go` (`PublishRawSnapshot` writes group snapshots and rollouts)
Interfaces:

```go
package rollout
type CreateParams struct { EngineGroupID uuid.UUID; Version uint64; FromVersion *uint64; Kind Kind; Immediate bool; Actor string }
func Create(ctx context.Context, tx pgx.Tx, p CreateParams) (uuid.UUID, State, error)

package snapshot
const NotifyChannel = "nexora_config"   // unchanged: informational, payload version
func BuildForGroup(ctx context.Context, tx pgx.Tx, version uint64, cfg BuildConfig, engineGroupID uuid.UUID) (*controlv1.ConfigSnapshot, error)
func Build(ctx context.Context, tx pgx.Tx, version uint64, cfg BuildConfig) (*controlv1.ConfigSnapshot, error) // BuildForGroup(default)
func ContentDigest(snap *controlv1.ConfigSnapshot) (string, error)
func Latest(ctx context.Context, q Querier) (uint64, *controlv1.ConfigSnapshot, error) // default group's newest group snapshot
func Republish(ctx context.Context, tx pgx.Tx, a auth.Actor, engineGroupID uuid.UUID, fromVersion uint64, kind rollout.Kind) (uint64, uuid.UUID, error)
func AddAuthZones(ctx context.Context, tx pgx.Tx, snap *controlv1.ConfigSnapshot, engineGroupID uuid.UUID) error
var ErrUnknownVersion = errors.New("version not found for this engine group")

package store
func LoadResolution(ctx context.Context, q PolicyQuerier, engineGroupID uuid.UUID) (ResolutionRows, error)
```

- [ ] Write the failing database test `mgmt/internal/snapshot/publish_m5_test.go`:
  ```go
  package snapshot_test

  import (
  	"context"
  	"errors"
  	"slices"
  	"testing"

  	"github.com/google/uuid"
  	"github.com/jackc/pgx/v5"
  	"google.golang.org/protobuf/proto"

  	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
  	"github.com/piwi3910/nexora/mgmt/internal/auth"
  	"github.com/piwi3910/nexora/mgmt/internal/rollout"
  	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
  	"github.com/piwi3910/nexora/mgmt/internal/store"
  	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
  )

  var testActor = auth.Actor{Type: "system", ID: "test", Name: "test"}

  func mutateSQL(t *testing.T, st *store.Store, sql string, args ...any) uint64 {
  	t.Helper()
  	v, err := snapshot.Mutate(context.Background(), st, snapshot.BuildConfig{}, testActor, func(tx pgx.Tx) (auth.Change, error) {
  		_, err := tx.Exec(context.Background(), sql, args...)
  		return auth.Change{Action: "test", TargetType: "test", TargetID: "1"}, err
  	})
  	if err != nil {
  		t.Fatalf("mutate %q: %v", sql, err)
  	}
  	return v
  }

  func groupSnapshot(t *testing.T, st *store.Store, version uint64, group uuid.UUID) *controlv1.ConfigSnapshot {
  	t.Helper()
  	var raw []byte
  	if err := st.Pool.QueryRow(context.Background(), `select snapshot from group_snapshots where version = $1 and engine_group_id = $2`,
  		int64(version), group).Scan(&raw); err != nil {
  		t.Fatalf("group snapshot (%d, %s): %v", version, group, err)
  	}
  	s := &controlv1.ConfigSnapshot{}
  	if err := proto.Unmarshal(raw, s); err != nil {
  		t.Fatal(err)
  	}
  	return s
  }

  func rolloutState(t *testing.T, st *store.Store, group uuid.UUID, version uint64) string {
  	t.Helper()
  	var s string
  	if err := st.Pool.QueryRow(context.Background(), `select state from rollouts where engine_group_id = $1 and version = $2`,
  		group, int64(version)).Scan(&s); err != nil {
  		t.Fatalf("rollout (%s, %d): %v", group, version, err)
  	}
  	return s
  }

  func upstreamNames(s *controlv1.ConfigSnapshot) []string {
  	out := []string{}
  	for _, u := range s.Upstreams {
  		out = append(out, u.Name)
  	}
  	return out
  }

  func TestPublishPerEngineGroup(t *testing.T) {
  	ctx := context.Background()
  	st := storetest.New(t)
  	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
  		t.Fatal(err)
  	}
  	var edge uuid.UUID
  	if err := st.Pool.QueryRow(ctx, `insert into engine_groups (name, rollout_strategy, canary_count, upstream_mode)
  		values ('edge', 'canary', 1, 'inherit') returning id`).Scan(&edge); err != nil {
  		t.Fatal(err)
  	}

  	v1 := mutateSQL(t, st, `insert into upstreams (name, protocol, address, position) values ('global', 'udp', '192.0.2.1:53', 0)`)
  	v2 := mutateSQL(t, st, `insert into upstreams (name, protocol, address, position, engine_group_id)
  		values ('edge-only', 'udp', '192.0.2.2:53', 1, $1)`, edge)

  	if got := upstreamNames(groupSnapshot(t, st, v2, store.DefaultEngineGroupID)); !slices.Equal(got, []string{"global"}) {
  		t.Fatalf("default group upstreams = %v, want [global]", got)
  	}
  	edgeSnap := groupSnapshot(t, st, v2, edge)
  	if got := upstreamNames(edgeSnap); !slices.Equal(got, []string{"edge-only", "global"}) || edgeSnap.Version != v2 {
  		t.Fatalf("edge upstreams = %v version %d, want [edge-only global] at %d", got, edgeSnap.Version, v2)
  	}
  	if _, err := st.Pool.Exec(ctx, `update engine_groups set upstream_mode = 'override' where id = $1`, edge); err != nil {
  		t.Fatal(err)
  	}
  	vOverride := mutateSQL(t, st, `update resolver_settings set block_ttl = 61`)
  	if got := upstreamNames(groupSnapshot(t, st, vOverride, edge)); !slices.Equal(got, []string{"edge-only"}) {
  		t.Fatalf("override upstreams = %v, want [edge-only]", got)
  	}

  	// all_at_once starts in rolling; a canary change waits in pending and supersedes the older one.
  	if s := rolloutState(t, st, store.DefaultEngineGroupID, v2); s != "rolling" && s != "superseded" {
  		t.Fatalf("default rollout v2 = %s", s)
  	}
  	if s := rolloutState(t, st, store.DefaultEngineGroupID, vOverride); s != "rolling" {
  		t.Fatalf("default rollout %d = %s, want rolling", vOverride, s)
  	}
  	if s := rolloutState(t, st, edge, v1); s != "superseded" {
  		t.Fatalf("edge rollout v1 = %s, want superseded", s)
  	}
  	if s := rolloutState(t, st, edge, vOverride); s != "pending" {
  		t.Fatalf("edge rollout %d = %s, want pending (canary change)", vOverride, s)
  	}

  	// Content equal to the stable version rolls out at once, even in a canary group.
  	if _, err := st.Pool.Exec(ctx, `update rollouts set state = 'completed' where engine_group_id = $1 and version = $2`, edge, int64(vOverride)); err != nil {
  		t.Fatal(err)
  	}
  	if _, err := st.Pool.Exec(ctx, `update engine_groups set stable_version = $1 where id = $2`, int64(vOverride), edge); err != nil {
  		t.Fatal(err)
  	}
  	vSame := mutateSQL(t, st, `update resolver_settings set block_ttl = 61`)
  	if s := rolloutState(t, st, edge, vSame); s != "rolling" {
  		t.Fatalf("unchanged content rollout = %s, want rolling", s)
  	}

  	// Rollback: the halted rollout becomes rolled_back, the group pauses, the copy rolls out at once.
  	vBad := mutateSQL(t, st, `update upstreams set timeout_ms = 400 where name = 'edge-only'`)
  	if _, err := st.Pool.Exec(ctx, `update rollouts set state = 'halted' where engine_group_id = $1 and version = $2`, edge, int64(vBad)); err != nil {
  		t.Fatal(err)
  	}
  	var vRB uint64
  	var rid uuid.UUID
  	err := st.InTx(ctx, func(tx pgx.Tx) error {
  		var err error
  		vRB, rid, err = snapshot.Republish(ctx, tx, testActor, edge, vOverride, rollout.KindRollback)
  		return err
  	})
  	if err != nil || rid == uuid.Nil || vRB <= vBad {
  		t.Fatalf("rollback: version %d rollout %s err %v", vRB, rid, err)
  	}
  	if rolloutState(t, st, edge, vBad) != "rolled_back" || rolloutState(t, st, edge, vRB) != "rolling" {
  		t.Fatal("rollback must mark the halted rollout rolled_back and start rolling at once")
  	}
  	var paused bool
  	_ = st.Pool.QueryRow(ctx, `select rollouts_paused from engine_groups where id = $1`, edge).Scan(&paused)
  	if !paused {
  		t.Fatal("rollback must pause change rollouts")
  	}
  	rb, src := groupSnapshot(t, st, vRB, edge), groupSnapshot(t, st, vOverride, edge)
  	if rb.Version != vRB {
  		t.Fatalf("rollback snapshot version %d, want %d", rb.Version, vRB)
  	}
  	rb.Version, rb.CreatedUnixMs, src.CreatedUnixMs = src.Version, 0, 0
  	if !proto.Equal(rb, src) {
  		t.Fatal("rollback snapshot must equal the source snapshot apart from its version")
  	}
  	vHeld := mutateSQL(t, st, `update upstreams set timeout_ms = 450 where name = 'edge-only'`)
  	if s := rolloutState(t, st, edge, vHeld); s != "pending" {
  		t.Fatalf("change while paused = %s, want pending", s)
  	}

  	if _, latest, err := snapshot.Latest(ctx, st.Pool); err != nil || latest.Version != vHeld {
  		t.Fatalf("Latest = %v err %v, want the default group's version %d", latest.GetVersion(), err, vHeld)
  	}
  	err = st.InTx(ctx, func(tx pgx.Tx) error {
  		_, _, err := snapshot.Republish(ctx, tx, testActor, edge, 999999, rollout.KindRollback)
  		return err
  	})
  	if !errors.Is(err, snapshot.ErrUnknownVersion) {
  		t.Fatalf("unknown version: err = %v", err)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/snapshot/ -run TestPublishPerEngineGroup -count=1` and expect FAIL with `undefined: snapshot.Republish`.
- [ ] Create `mgmt/internal/rollout/create.go`:
  ```go
  package rollout

  import (
  	"context"
  	"fmt"

  	"github.com/google/uuid"
  	"github.com/jackc/pgx/v5"
  )

  // CreateParams describes the rollout of one group snapshot.
  type CreateParams struct {
  	EngineGroupID uuid.UUID
  	Version       uint64
  	FromVersion   *uint64
  	Kind          Kind
  	// Immediate rolls out to every engine at once, ignoring the group's strategy and pause: the
  	// content equals the group's stable content, or a test published the snapshot raw.
  	Immediate bool
  	Actor     string
  }

  // Create supersedes the engine group's open rollouts, inserts the new rollout (rolling for
  // all_at_once, pending otherwise) and notifies nexora_rollout inside tx.
  func Create(ctx context.Context, tx pgx.Tx, p CreateParams) (uuid.UUID, State, error) {
  	var paused bool
  	var groupStrategy string
  	if err := tx.QueryRow(ctx, `select rollouts_paused, rollout_strategy from engine_groups where id = $1 for update`,
  		p.EngineGroupID).Scan(&paused, &groupStrategy); err != nil {
  		return uuid.Nil, "", fmt.Errorf("rollout: engine group %s: %w", p.EngineGroupID, err)
  	}
  	held := p.Kind == KindChange && !p.Immediate && paused
  	supersede := []string{"pending", "canary", "verifying", "rolling", "halted"}
  	switch {
  	case held:
  		supersede = []string{"pending"}
  	case p.Kind == KindRollback:
  		if _, err := tx.Exec(ctx, `update rollouts set state = 'rolled_back', finished_at = now(), updated_at = now()
  			where engine_group_id = $1 and state = 'halted'`, p.EngineGroupID); err != nil {
  			return uuid.Nil, "", err
  		}
  		supersede = []string{"pending", "canary", "verifying", "rolling"}
  		if _, err := tx.Exec(ctx, `update engine_groups set rollouts_paused = true, updated_at = now() where id = $1`, p.EngineGroupID); err != nil {
  			return uuid.Nil, "", err
  		}
  	}
  	if _, err := tx.Exec(ctx, `update rollouts set state = 'superseded', finished_at = now(), updated_at = now()
  		where engine_group_id = $1 and state = any($2)`, p.EngineGroupID, supersede); err != nil {
  		return uuid.Nil, "", err
  	}
  	strategy := Strategy(groupStrategy)
  	if p.Kind != KindChange || p.Immediate {
  		strategy = AllAtOnce
  	}
  	state := Pending
  	if strategy == AllAtOnce && !held {
  		state = Rolling
  	}
  	var from *int64
  	if p.FromVersion != nil {
  		f := int64(*p.FromVersion)
  		from = &f
  	}
  	var id uuid.UUID
  	err := tx.QueryRow(ctx, `
  		insert into rollouts (engine_group_id, version, from_version, kind, strategy, state, params, created_by, phase_started_at)
  		select g.id, $2, $3, $4, $5, $6,
  		       jsonb_build_object('strategy', $5::text, 'canary_count', g.canary_count, 'canary_percent', g.canary_percent,
  		         'ack_timeout_seconds', g.ack_timeout_seconds, 'health_window_seconds', g.health_window_seconds,
  		         'max_servfail_ratio', g.max_servfail_ratio, 'min_health_queries', g.min_health_queries),
  		       $7, case when $6 = 'rolling' then now() end
  		from engine_groups g where g.id = $1
  		returning id`, p.EngineGroupID, int64(p.Version), from, string(p.Kind), string(strategy), string(state), p.Actor).Scan(&id)
  	if err != nil {
  		return uuid.Nil, "", fmt.Errorf("rollout: insert: %w", err)
  	}
  	if _, err := tx.Exec(ctx, `select pg_notify('nexora_rollout', $1::text)`, p.EngineGroupID); err != nil {
  		return uuid.Nil, "", err
  	}
  	return id, state, nil
  }
  ```
- [ ] Change `mgmt/internal/snapshot/snapshot.go`:
  - `publish(ctx, tx, cfg, a, change)`: after `nextVersion` and `auth.WriteAudit`, insert the `config_versions` row with a NULL snapshot (`insertVersion` takes `raw []byte` that may be nil; it still sends `pg_notify(NotifyChannel, version)`), then for every `select id from engine_groups order by (id <> '00000000-0000-0000-0000-000000000001'), name`: `snap := BuildForGroup(ctx, tx, version, cfg, id)`, `digest := ContentDigest(snap)`, `raw := proto.MarshalOptions{Deterministic: true}.Marshal(snap)`, `insert into group_snapshots (version, engine_group_id, snapshot, content_sha256) values ($1, $2, $3, $4)`, `immediate := digest == (select gs.content_sha256 from engine_groups g join group_snapshots gs on gs.engine_group_id = g.id and gs.version = g.stable_version where g.id = $1)`, then `rollout.Create(ctx, tx, rollout.CreateParams{EngineGroupID: id, Version: version, Kind: rollout.KindChange, Immediate: immediate, Actor: a.Name})`.
  - `ContentDigest(snap)`: clone, zero `Version` and `CreatedUnixMs`, deterministic marshal, lowercase hex SHA-256.
  - `Build(ctx, tx, version, cfg)` becomes `BuildForGroup(ctx, tx, version, cfg, store.DefaultEngineGroupID)`. `BuildForGroup` first loads `select upstream_mode, host(c) || '/' || masklen(c) array, otlp_endpoint` from `engine_groups` (`array(select host(c) || '/' || masklen(c) from unnest(extra_acl_cidrs) with ordinality as u(c, n) order by n)`); the ACL is `access_control` CIDRs followed by the group's extra CIDRs; a non-empty group `otlp_endpoint` replaces `resolver_settings.otlp_endpoint` (the `cfg.DefaultOTLPEndpoint` fallback applies only when both are empty).
  - `buildUpstreams(ctx, tx, snap, groupID, mode)`: `where enabled and (engine_group_id = $1 or ($2 = 'inherit' and engine_group_id is null)) order by (engine_group_id is null), position, name`.
  - `buildFilterLists(ctx, tx, snap, groupID)`: add `and (f.engine_group_id is null or f.engine_group_id = $1)`.
  - `PublishRaw(ctx, st, snap, createdBy)`: insert `config_versions` with a NULL snapshot, then for every engine group insert the same raw bytes (with `snap.Version` set) into `group_snapshots` (content digest from `ContentDigest`) and `rollout.Create` with `Immediate: true`.
  - `Latest(ctx, q)`: `select version, snapshot from group_snapshots where engine_group_id = '00000000-0000-0000-0000-000000000001' order by version desc limit 1`.
  - `Republish(ctx, tx, a, groupID, fromVersion, kind)`: load `select snapshot from group_snapshots where version = $1 and engine_group_id = $2` (no row -> `ErrUnknownVersion`), `nextVersion`, `auth.WriteAudit(ctx, tx, a, auth.Change{Action: string(kind) + "EngineGroup", TargetType: "engine_group", TargetID: groupID.String(), After: map[string]uint64{"from_version": fromVersion}}, &version)`, insert the `config_versions` row with summary `"<kind> engine group <id> to <fromVersion>"`, re-encode with the new `Version` and `CreatedUnixMs = time.Now().UnixMilli()`, insert the group snapshot and `rollout.Create(ctx, tx, rollout.CreateParams{EngineGroupID: groupID, Version: version, FromVersion: &fromVersion, Kind: kind, Immediate: true, Actor: a.Name})`. Other groups get no row for this version (their target stays unchanged).
  - `EnsureInitial` is unchanged apart from calling the new `publish`.
- [ ] Change `mgmt/internal/snapshot/policy.go` `buildPolicy(ctx, tx, snap, groupID)`: policy groups `store.ListPolicyGroups` rows are kept when `EngineGroupID == nil || *EngineGroupID == groupID` (add `EngineGroupID *uuid.UUID` to `store.PolicyGroup` and select `engine_group_id` in `scanPolicyGroup`); rewrites keep those whose policy group was kept, plus global rewrites with `EngineGroupID == nil || *EngineGroupID == groupID` (add `EngineGroupID *uuid.UUID` to `store.Rewrite` and to its select list); the block list map keeps every fetched block list (a policy group may only reference global lists or lists of its engine group, enforced by the API in Task 7).
- [ ] Change `store.LoadResolution(ctx, q, groupID)` in `mgmt/internal/store/resolution.go`: forward zones and RPZ zones add `where engine_group_id is null or engine_group_id = $1` (RPZ zones keep `order by position`); add `EngineGroupID *uuid.UUID` to `store.ForwardZone` and `store.RPZZone` and their scans. `ListForwardZones`/`ListRPZZones` keep returning every row.
- [ ] Change `AddAuthZones(ctx, tx, snap, groupID)` in `mgmt/internal/snapshot/authzones.go`: add `and (z.engine_group_id is null or z.engine_group_id = $1)` to the zones query. Update the call sites `mgmt/internal/snapshot/authzones_test.go` (pass `store.DefaultEngineGroupID`) and `mgmt/internal/snapshot/snapshot_test.go` (unchanged `snapshot.Build`).
- [ ] Change `harness.PublishRawSnapshot` in `e2e/harness/mgmt.go` (the harness cannot import `mgmt/internal`): inside the same transaction after the version lock, `insert into config_versions(version, created_by, summary) values ($1, 'e2e', 'raw snapshot')`, then for each `select id from engine_groups`: `insert into group_snapshots(version, engine_group_id, snapshot, content_sha256) values ($1, $2, $3, encode(sha256($3), 'hex'))`, `update rollouts set state = 'superseded', finished_at = now() where engine_group_id = $2 and state in ('pending','canary','verifying','rolling','halted')`, `insert into rollouts(engine_group_id, version, kind, strategy, state, params, created_by, phase_started_at) values ($2, $1, 'change', 'all_at_once', 'rolling', '{"strategy":"all_at_once","ack_timeout_seconds":60}', 'e2e', now())`, `select pg_notify('nexora_rollout', $2::text)`; keep `pg_notify('nexora_config', version)`.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/snapshot/ ./mgmt/internal/rollout/ ./mgmt/internal/store/ -count=1 && go vet ./mgmt/... ./e2e/...'` and expect `ok` for each package and no vet output.
- [ ] Reconciled with the code while building (binding):
  - `buildPolicy` lives in `snapshot.go`, not `policy.go` (unchanged file); `store.LoadResolution` reads the scoped forward and RPZ zones through `collect(..., args...)`.
  - `rollout.Create` locks the engine group row `FOR NO KEY UPDATE` (not `FOR UPDATE`, which would block every foreign-key insert naming the group), and the controller (Task 6) takes the same lock before the rollout row, so publish and step never deadlock. `harness.PublishRawSnapshot` locks the engine group rows the same way first.
  - `Republish` takes the version lock (`nextVersion`) before loading the source snapshot.
  - Three readers of `config_versions.snapshot` the first draft missed now read `group_snapshots`: blob garbage collection in `mgmt/internal/blocklist/fetcher.go` keeps the blobs of the group snapshots of the newest 20 versions plus every group's stable version and open or halted rollouts (new `TestCollectBlobsKeepsStableGroupSnapshots` in `gc_test.go`); `mgmt/internal/tsigkey/service_test.go`; and `e2e/rpz_test.go`'s snapshot byte search.
- [ ] Commit: `git add mgmt/internal/snapshot mgmt/internal/rollout/create.go mgmt/internal/store mgmt/internal/blocklist mgmt/internal/tsigkey/service_test.go e2e/harness/mgmt.go e2e/rpz_test.go && git commit -m "feat(snapshot): one snapshot and rollout per engine group"`.

## Task 6: Rollout controller, targeted pushes and fleet status

Files: `mgmt/internal/rollout/controller.go` (multi-instance driver, health from `engine_stats`), `mgmt/internal/rollout/controller_test.go`, `mgmt/internal/fleet/engines.go` (engine views, status, targets), `mgmt/internal/fleet/collector.go` (fleet gauges), `mgmt/internal/fleet/fleet_test.go`, `mgmt/internal/store/storetest/fleet.go` (engine row fixtures), `mgmt/internal/control/hub.go` (push by target, per-group notifications), `mgmt/internal/control/server.go` (`Connect` uses the target), `mgmt/internal/control/keys.go` (per-engine key filtering), `mgmt/internal/control/keys_test.go`, `mgmt/internal/control/control_test.go` (fixture runs a controller per instance), `mgmt/internal/control/fleet_push_test.go`, `mgmt/internal/api/handlers_fleet.go` (engine list and status from `fleet.ListEngines`), `mgmt/internal/stats/stats.go` (`Record` notifies nothing; unchanged signature), `mgmt/internal/config/config.go` (`NEXORA_ROLLOUT_TICK`), `mgmt/internal/config/config_test.go`, `mgmt/cmd/nexora-mgmt/main.go` (start the controller, register the collector)
Interfaces:

```go
package rollout
type Controller struct { Store *store.Store; Tick time.Duration }
func (c *Controller) Run(ctx context.Context)
func (c *Controller) Step(ctx context.Context) (driven int, err error)
func HealthSince(ctx context.Context, q store.PolicyQuerier, engineID uuid.UUID, since time.Time) (Health, error)

package fleet
type EngineView struct {
	ID, EngineGroupID uuid.UUID
	NodeName, EngineVersion, EngineGroupName, RejectedReason, PersistError, CertificateSerial, Status string
	EnrolledAt time.Time
	LastSeenAt, RevokedAt, CertRotateRequestedAt, CertificateNotAfter *time.Time
	Connected, VersionAhead bool
	AppliedVersion, TargetVersion uint64
	RejectedVersion *uint64
	Labels map[string]string
	Revision int64
}
type EngineFilter struct { EngineGroupID, EngineID *uuid.UUID }
func ListEngines(ctx context.Context, q store.PolicyQuerier, f EngineFilter) ([]EngineView, error)
type Target struct { EngineGroupID uuid.UUID; Version uint64; Snapshot *controlv1.ConfigSnapshot; RotateRequested bool }
func TargetFor(ctx context.Context, q store.PolicyQuerier, engineID uuid.UUID) (Target, error)
func NewCollector(st *store.Store) prometheus.Collector

package storetest
func InsertEngine(t *testing.T, st *store.Store, nodeName string, engineGroupID uuid.UUID) uuid.UUID
func ConnectEngine(t *testing.T, st *store.Store, engineID uuid.UUID)

package control
func FilterRPZKeys(snap *controlv1.ConfigSnapshot, keys *controlv1.RpzTsigKeys) *controlv1.RpzTsigKeys
func FilterKeyMaterial(snap *controlv1.ConfigSnapshot, km *controlv1.KeyMaterial, zoned map[string]bool) *controlv1.KeyMaterial
```

- [ ] Create the fixture `mgmt/internal/store/storetest/fleet.go`:
  ```go
  package storetest

  import (
  	"context"
  	"testing"

  	"github.com/google/uuid"

  	"github.com/piwi3910/nexora/mgmt/internal/store"
  )

  // InsertEngine creates an enrolled, never-connected engine row in engineGroupID.
  func InsertEngine(t *testing.T, st *store.Store, nodeName string, engineGroupID uuid.UUID) uuid.UUID {
  	t.Helper()
  	var id uuid.UUID
  	if err := st.Pool.QueryRow(context.Background(), `insert into engines (node_name, certificate_serial, engine_group_id)
  		values ($1, md5(random()::text), $2) returning id`, nodeName, engineGroupID).Scan(&id); err != nil {
  		t.Fatalf("insert engine %s: %v", nodeName, err)
  	}
  	return id
  }

  // ConnectEngine marks the engine as holding a live stream on the fixture instance "storetest".
  func ConnectEngine(t *testing.T, st *store.Store, engineID uuid.UUID) {
  	t.Helper()
  	ctx := context.Background()
  	if _, err := st.Pool.Exec(ctx, `insert into instances (id) values ('storetest')
  		on conflict (id) do update set heartbeat_at = now()`); err != nil {
  		t.Fatal(err)
  	}
  	if _, err := st.Pool.Exec(ctx, `update engines set connected_instance = 'storetest', last_seen_at = now() where id = $1`, engineID); err != nil {
  		t.Fatal(err)
  	}
  }
  ```
  `md5(random()::text)` is a lowercase hex serial without the `pgcrypto` extension.
- [ ] Write the failing test `mgmt/internal/rollout/controller_test.go`:
  ```go
  package rollout_test

  import (
  	"context"
  	"testing"
  	"time"

  	"github.com/google/uuid"
  	"github.com/jackc/pgx/v5"
  	"google.golang.org/protobuf/proto"

  	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
  	"github.com/piwi3910/nexora/mgmt/internal/auth"
  	"github.com/piwi3910/nexora/mgmt/internal/rollout"
  	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
  	"github.com/piwi3910/nexora/mgmt/internal/store"
  	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
  )

  func stateOf(t *testing.T, st *store.Store, version uint64) (string, []uuid.UUID) {
  	t.Helper()
  	var s string
  	var canaries []uuid.UUID
  	if err := st.Pool.QueryRow(context.Background(), `select state, canary_engine_ids from rollouts
  		where engine_group_id = $1 and version = $2`, store.DefaultEngineGroupID, int64(version)).Scan(&s, &canaries); err != nil {
  		t.Fatal(err)
  	}
  	return s, canaries
  }

  func sample(t *testing.T, st *store.Store, engine uuid.UUID, at string, queries, servfail uint64) {
  	t.Helper()
  	raw, _ := proto.Marshal(&controlv1.Stats{QueriesTotal: queries, ServfailTotal: servfail})
  	if _, err := st.Pool.Exec(context.Background(), `insert into engine_stats (engine_id, at, stats)
  		select $1, phase_started_at + $2::interval, $3 from rollouts where state = 'verifying'`, engine, at, raw); err != nil {
  		t.Fatal(err)
  	}
  }

  func TestControllerDrivesCanaryUnderAdvisoryLock(t *testing.T) {
  	ctx := context.Background()
  	st := storetest.New(t)
  	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
  		t.Fatal(err)
  	}
  	a := storetest.InsertEngine(t, st, "a", store.DefaultEngineGroupID)
  	b := storetest.InsertEngine(t, st, "b", store.DefaultEngineGroupID)
  	storetest.ConnectEngine(t, st, a)
  	storetest.ConnectEngine(t, st, b)
  	c := &rollout.Controller{Store: st}

  	if n, err := c.Step(ctx); err != nil || n != 0 {
  		t.Fatalf("version 1 completed before any ack: driven=%d err=%v", n, err)
  	}
  	if _, err := st.Pool.Exec(ctx, `update engines set applied_version = 1`); err != nil {
  		t.Fatal(err)
  	}
  	if n, err := c.Step(ctx); err != nil || n != 1 {
  		t.Fatalf("rolling -> completed: driven=%d err=%v", n, err)
  	}

  	if _, err := st.Pool.Exec(ctx, `update engine_groups set rollout_strategy = 'canary', canary_count = 1, min_health_queries = 10`); err != nil {
  		t.Fatal(err)
  	}
  	v2, err := snapshot.Mutate(ctx, st, snapshot.BuildConfig{}, auth.Actor{Type: "system", ID: "t", Name: "t"}, func(tx pgx.Tx) (auth.Change, error) {
  		_, err := tx.Exec(ctx, `update resolver_settings set block_ttl = 7`)
  		return auth.Change{Action: "updateResolverSettings", TargetType: "resolver_settings", TargetID: "singleton"}, err
  	})
  	if err != nil {
  		t.Fatal(err)
  	}
  	var id uuid.UUID
  	if err := st.Pool.QueryRow(ctx, `select id from rollouts where version = $1`, int64(v2)).Scan(&id); err != nil {
  		t.Fatal(err)
  	}
  	holder, err := st.Pool.Begin(ctx)
  	if err != nil {
  		t.Fatal(err)
  	}
  	if _, err := holder.Exec(ctx, `select pg_advisory_xact_lock(hashtext('nexora:rollout:' || $1::text))`, id); err != nil {
  		t.Fatal(err)
  	}
  	if n, err := c.Step(ctx); err != nil || n != 0 {
  		t.Fatalf("locked rollout: driven=%d err=%v, want 0 nil", n, err)
  	}
  	_ = holder.Rollback(ctx)

  	if n, err := c.Step(ctx); err != nil || n != 1 {
  		t.Fatalf("pending -> canary: driven=%d err=%v", n, err)
  	}
  	if s, canaries := stateOf(t, st, v2); s != "canary" || len(canaries) != 1 || canaries[0] != a {
  		t.Fatalf("state %s canaries %v, want canary [a]", s, canaries)
  	}
  	if _, err := st.Pool.Exec(ctx, `update engines set applied_version = $1 where id = $2`, int64(v2), a); err != nil {
  		t.Fatal(err)
  	}
  	if n, err := c.Step(ctx); err != nil || n != 1 {
  		t.Fatalf("canary -> verifying: driven=%d err=%v", n, err)
  	}
  	if s, _ := stateOf(t, st, v2); s != "verifying" {
  		t.Fatalf("state %s, want verifying", s)
  	}
  	if _, err := st.Pool.Exec(ctx, `update rollouts set phase_started_at = now() - interval '31 seconds' where id = $1`, id); err != nil {
  		t.Fatal(err)
  	}
  	sample(t, st, a, "-5 seconds", 1000, 3)
  	sample(t, st, a, "10 seconds", 1400, 5)
  	sample(t, st, a, "20 seconds", 1900, 8)
  	var since time.Time
  	_ = st.Pool.QueryRow(ctx, `select phase_started_at from rollouts where id = $1`, id).Scan(&since)
  	if h, err := rollout.HealthSince(ctx, st.Pool, a, since); err != nil || h.Samples != 3 || h.Queries != 900 || h.Servfail != 5 {
  		t.Fatalf("health = %+v err %v, want 3 samples, 900 queries, 5 servfail", h, err)
  	}
  	if n, err := c.Step(ctx); err != nil || n != 1 {
  		t.Fatalf("verifying -> rolling: driven=%d err=%v", n, err)
  	}
  	if _, err := st.Pool.Exec(ctx, `update engines set applied_version = $1 where id = $2`, int64(v2), b); err != nil {
  		t.Fatal(err)
  	}
  	if n, err := c.Step(ctx); err != nil || n != 1 {
  		t.Fatalf("rolling -> completed: driven=%d err=%v", n, err)
  	}
  	var stable int64
  	if err := st.Pool.QueryRow(ctx, `select stable_version from engine_groups where id = $1`, store.DefaultEngineGroupID).Scan(&stable); err != nil || uint64(stable) != v2 {
  		t.Fatalf("stable_version = %d err %v, want %d", stable, err, v2)
  	}
  	if n, _ := c.Step(ctx); n != 0 {
  		t.Fatalf("terminal rollout driven again (%d)", n)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/rollout/ -run TestControllerDrivesCanaryUnderAdvisoryLock -count=1` and expect FAIL with `undefined: rollout.Controller`.
- [ ] Create `mgmt/internal/rollout/controller.go`:
  ```go
  package rollout

  import (
  	"context"
  	"encoding/json"
  	"errors"
  	"log/slog"
  	"time"

  	"github.com/google/uuid"
  	"github.com/jackc/pgx/v5"
  	"google.golang.org/protobuf/proto"

  	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
  	"github.com/piwi3910/nexora/mgmt/internal/store"
  )

  // Controller steps every open rollout; any number of instances run one.
  type Controller struct {
  	Store *store.Store
  	Tick  time.Duration // NEXORA_ROLLOUT_TICK; default 1 s
  }

  // Run steps on every tick and on nexora_rollout notifications until ctx ends.
  func (c *Controller) Run(ctx context.Context) {
  	tick := c.Tick
  	if tick <= 0 {
  		tick = time.Second
  	}
  	wake := make(chan struct{}, 1)
  	go c.listen(ctx, wake)
  	t := time.NewTicker(tick)
  	defer t.Stop()
  	for {
  		if _, err := c.Step(ctx); err != nil && ctx.Err() == nil {
  			slog.Warn("rollout step", "err", err)
  		}
  		select {
  		case <-ctx.Done():
  			return
  		case <-t.C:
  		case <-wake:
  		}
  	}
  }

  // listen holds a dedicated connection outside the pool, like the hub's LISTEN.
  func (c *Controller) listen(ctx context.Context, wake chan<- struct{}) {
  	for ctx.Err() == nil {
  		conn, err := pgx.ConnectConfig(ctx, c.Store.Pool.Config().ConnConfig)
  		if err == nil {
  			_, err = conn.Exec(ctx, "listen nexora_rollout")
  			for err == nil {
  				if _, err = conn.WaitForNotification(ctx); err == nil {
  					select {
  					case wake <- struct{}{}:
  					default:
  					}
  				}
  			}
  			_ = conn.Close(context.WithoutCancel(ctx))
  		}
  		select {
  		case <-ctx.Done():
  		case <-time.After(time.Second):
  		}
  	}
  }

  // Step drives every open rollout once and returns how many changed state.
  func (c *Controller) Step(ctx context.Context) (int, error) {
  	rows, err := c.Store.Pool.Query(ctx, `select id from rollouts where state in ('pending','canary','verifying','rolling') order by created_at`)
  	if err != nil {
  		return 0, store.MapError(err)
  	}
  	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
  	if err != nil {
  		return 0, store.MapError(err)
  	}
  	driven := 0
  	var errs []error
  	for _, id := range ids {
  		ok, err := c.driveOne(ctx, id)
  		if err != nil {
  			errs = append(errs, err)
  		}
  		if ok {
  			driven++
  		}
  	}
  	return driven, errors.Join(errs...)
  }

  func (c *Controller) driveOne(ctx context.Context, id uuid.UUID) (bool, error) {
  	tx, err := c.Store.Pool.Begin(ctx)
  	if err != nil {
  		return false, err
  	}
  	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
  	var locked bool
  	if err := tx.QueryRow(ctx, `select pg_try_advisory_xact_lock(hashtext('nexora:rollout:' || $1::text))`, id).Scan(&locked); err != nil || !locked {
  		return false, err
  	}
  	var (
  		r       Rollout
  		version int64
  		params  []byte
  		phase   *time.Time
  		paused  bool
  		now     time.Time
  	)
  	err = tx.QueryRow(ctx, `
  		select r.id, r.engine_group_id, r.version, r.kind, r.state, r.canary_engine_ids, r.phase_started_at, r.halt_reason, r.params,
  		       g.rollouts_paused, now()
  		from rollouts r join engine_groups g on g.id = r.engine_group_id
  		where r.id = $1 for update of r`, id).
  		Scan(&r.ID, &r.EngineGroupID, &version, &r.Kind, &r.State, &r.CanaryEngineIDs, &phase, &r.HaltReason, &params, &paused, &now)
  	if err != nil {
  		return false, err
  	}
  	r.Version = uint64(version)
  	if r.State.Terminal() || r.State == Halted {
  		return false, nil
  	}
  	if phase != nil {
  		r.PhaseStartedAt = *phase
  	}
  	if err := json.Unmarshal(params, &r.Params); err != nil {
  		return false, err
  	}
  	engines, err := loadEngines(ctx, tx, r.EngineGroupID)
  	if err != nil {
  		return false, err
  	}
  	if r.State == Verifying {
  		for i := range engines {
  			if engines[i].Health, err = HealthSince(ctx, tx, engines[i].ID, r.PhaseStartedAt); err != nil {
  				return false, err
  			}
  		}
  	}
  	next, changed := Step(r, Observation{Now: now, GroupPaused: paused, Engines: engines})
  	if !changed {
  		return false, nil
  	}
  	if _, err := tx.Exec(ctx, `
  		update rollouts set state = $2, canary_engine_ids = $3, phase_started_at = $4, halt_reason = $5, updated_at = now(),
  		       finished_at = case when $2 = 'completed' then now() else finished_at end
  		where id = $1`, next.ID, string(next.State), next.CanaryEngineIDs, next.PhaseStartedAt, next.HaltReason); err != nil {
  		return false, err
  	}
  	if next.State == Completed {
  		if _, err := tx.Exec(ctx, `update engine_groups set stable_version = greatest(coalesce(stable_version, 0), $2), updated_at = now()
  			where id = $1`, next.EngineGroupID, int64(next.Version)); err != nil {
  			return false, err
  		}
  	}
  	if _, err := tx.Exec(ctx, `select pg_notify('nexora_rollout', $1::text)`, next.EngineGroupID); err != nil {
  		return false, err
  	}
  	return true, tx.Commit(ctx)
  }

  func loadEngines(ctx context.Context, tx pgx.Tx, group uuid.UUID) ([]Engine, error) {
  	rows, err := tx.Query(ctx, `
  		select e.id, e.node_name, e.labels,
  		       (e.connected_instance is not null and coalesce(i.heartbeat_at > now() - interval '15 seconds', false)),
  		       e.applied_version, coalesce(e.rejected_version, 0), e.rejected_reason
  		from engines e left join instances i on i.id = e.connected_instance
  		where e.engine_group_id = $1 and e.revoked_at is null and e.deleted_at is null
  		order by e.node_name, e.enrolled_at`, group)
  	if err != nil {
  		return nil, err
  	}
  	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Engine, error) {
  		var e Engine
  		var applied, rejected int64
  		err := row.Scan(&e.ID, &e.Name, &e.Labels, &e.Connected, &applied, &rejected, &e.RejectedReason)
  		e.AppliedVersion, e.RejectedVersion = uint64(applied), uint64(rejected)
  		return e, err
  	})
  }

  // HealthSince: the baseline is the newest engine_stats sample in (since-60s, since]; then every
  // later sample. Counters that went down (engine restart) count from zero.
  func HealthSince(ctx context.Context, q store.PolicyQuerier, engineID uuid.UUID, since time.Time) (Health, error) {
  	rows, err := q.Query(ctx, `
  		(select at, stats from engine_stats where engine_id = $1 and at <= $2 and at > $2 - interval '60 seconds' order by at desc limit 1)
  		union all
  		(select at, stats from engine_stats where engine_id = $1 and at > $2 order by at)
  		order by at`, engineID, since)
  	if err != nil {
  		return Health{}, err
  	}
  	defer rows.Close()
  	var h Health
  	var first, last *controlv1.Stats
  	for rows.Next() {
  		var at time.Time
  		var raw []byte
  		if err := rows.Scan(&at, &raw); err != nil {
  			return Health{}, err
  		}
  		s := &controlv1.Stats{}
  		if proto.Unmarshal(raw, s) != nil {
  			continue
  		}
  		if first == nil {
  			first = s
  		}
  		last = s
  		h.Samples++
  	}
  	if h.Samples >= 2 {
  		q0, f0 := first.QueriesTotal, first.ServfailTotal
  		if last.QueriesTotal < q0 || last.ServfailTotal < f0 {
  			q0, f0 = 0, 0
  		}
  		h.Queries, h.Servfail = last.QueriesTotal-q0, last.ServfailTotal-f0
  	}
  	return h, rows.Err()
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/rollout/ -count=1` and expect `ok`.
- [ ] Write the failing test `mgmt/internal/fleet/fleet_test.go`:
  ```go
  package fleet_test

  import (
  	"context"
  	"strings"
  	"testing"

  	"github.com/prometheus/client_golang/prometheus/testutil"

  	"github.com/piwi3910/nexora/mgmt/internal/fleet"
  	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
  	"github.com/piwi3910/nexora/mgmt/internal/store"
  	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
  )

  func TestEngineStatusTargetsAndMetrics(t *testing.T) {
  	ctx := context.Background()
  	st := storetest.New(t)
  	if _, err := snapshot.EnsureInitial(ctx, st, snapshot.BuildConfig{}); err != nil {
  		t.Fatal(err)
  	}
  	for _, n := range []string{"current", "behind", "rejected", "revoked", "gone"} {
  		storetest.ConnectEngine(t, st, storetest.InsertEngine(t, st, n, store.DefaultEngineGroupID))
  	}
  	quiet := storetest.InsertEngine(t, st, "quiet", store.DefaultEngineGroupID)
  	for _, stmt := range []string{
  		`update engines set applied_version = 1 where node_name in ('current', 'revoked')`,
  		`update engines set rejected_version = 1, rejected_reason = 'bad' where node_name = 'rejected'`,
  		`update engines set revoked_at = now() where node_name = 'revoked'`,
  		`update engines set connected_instance = null, last_seen_at = now() - interval '2 minutes' where node_name = 'gone'`,
  		`update engines set enrolled_at = now() - interval '5 seconds' where node_name = 'quiet'`,
  	} {
  		if _, err := st.Pool.Exec(ctx, stmt); err != nil {
  			t.Fatal(err)
  		}
  	}
  	views, err := fleet.ListEngines(ctx, st.Pool, fleet.EngineFilter{})
  	if err != nil || len(views) != 6 {
  		t.Fatalf("views %d err %v", len(views), err)
  	}
  	want := map[string]string{"current": "current", "behind": "behind", "rejected": "rejected", "revoked": "revoked", "gone": "disconnected", "quiet": "disconnected"}
  	for _, v := range views {
  		if v.Status != want[v.NodeName] || v.TargetVersion != 1 || v.EngineGroupName != "default" {
  			t.Errorf("%s: status %s target %d group %s, want %s 1 default", v.NodeName, v.Status, v.TargetVersion, v.EngineGroupName, want[v.NodeName])
  		}
  	}
  	target, err := fleet.TargetFor(ctx, st.Pool, quiet)
  	if err != nil || target.Version != 1 || target.Snapshot.GetVersion() != 1 || target.EngineGroupID != store.DefaultEngineGroupID {
  		t.Fatalf("target = %+v err %v", target, err)
  	}

  	// Positive first: the gauge counts "gone" (unseen for 2 minutes); "quiet" enrolled 5 s ago and
  	// "revoked" are not counted.
  	expected := `
  # HELP nexora_mgmt_engines_disconnected Non-revoked engines without a live control stream for more than 60 seconds
  # TYPE nexora_mgmt_engines_disconnected gauge
  nexora_mgmt_engines_disconnected 1
  `
  	if err := testutil.CollectAndCompare(fleet.NewCollector(st), strings.NewReader(expected), "nexora_mgmt_engines_disconnected"); err != nil {
  		t.Fatal(err)
  	}
  	if _, err := st.Pool.Exec(ctx, `update engines set revoked_at = now() where node_name = 'gone'`); err != nil {
  		t.Fatal(err)
  	}
  	if err := testutil.CollectAndCompare(fleet.NewCollector(st), strings.NewReader(strings.Replace(expected, "disconnected 1", "disconnected 0", 1)),
  		"nexora_mgmt_engines_disconnected"); err != nil {
  		t.Fatalf("revoked engines must not count as disconnected: %v", err)
  	}
  	if n := testutil.CollectAndCount(fleet.NewCollector(st), "nexora_mgmt_engines"); n == 0 {
  		t.Fatal("nexora_mgmt_engines not exported")
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/fleet/ -count=1` and expect FAIL with `undefined: fleet.ListEngines`.
- [ ] Create `mgmt/internal/fleet/engines.go`:
  - `ListEngines` runs, then computes target and status in Go:
    ```sql
    select e.id, e.node_name, e.engine_version, e.enrolled_at, e.last_seen_at,
           (e.connected_instance is not null and coalesce(i.heartbeat_at > now() - interval '15 seconds', false)),
           e.applied_version, e.rejected_version, e.rejected_reason, e.persist_error, e.version_ahead,
           e.engine_group_id, g.name, e.labels, e.revision, e.revoked_at, e.cert_rotate_requested_at,
           e.certificate_serial, c.not_after, coalesce(g.stable_version, 0)
    from engines e
    join engine_groups g on g.id = e.engine_group_id
    left join instances i on i.id = e.connected_instance
    left join engine_certificates c on c.serial = e.certificate_serial
    where e.deleted_at is null and ($1::uuid is null or e.engine_group_id = $1) and ($2::uuid is null or e.id = $2)
    order by e.node_name, e.enrolled_at;

    select distinct on (engine_group_id) engine_group_id, version, state, canary_engine_ids
    from rollouts where state <> 'superseded' order by engine_group_id, version desc;
    ```
    `TargetVersion = rollout.Target(rollout.Engine{ID, AppliedVersion}, stable, newest)`; `Status`, first match wins: `revoked` (`RevokedAt != nil`), `ahead` (`VersionAhead` or `TargetVersion > 0 && AppliedVersion > TargetVersion`), `disconnected` (`!Connected`), `rejected` (`RejectedVersion != nil && *RejectedVersion > AppliedVersion`), `current` (`AppliedVersion == TargetVersion`), `behind`.
  - `TargetFor(ctx, q, engineID)`: `ListEngines` with `EngineID`; no row -> `store.ErrNotFound`; when `TargetVersion > 0` load `select snapshot from group_snapshots where engine_group_id = $1 and version = $2` and unmarshal into `Snapshot`; `RotateRequested = CertRotateRequestedAt != nil`.
- [ ] Create `mgmt/internal/fleet/collector.go` in the style of `stats.NewCollector` (read at scrape time, 5 s timeout, a database error leaves the families out): `nexora_mgmt_engines{engine_group,status}` (gauge, help `Engines by engine group and status`, one sample per group and status that has engines), `nexora_mgmt_engines_disconnected` (gauge, help `Non-revoked engines without a live control stream for more than 60 seconds`, from `select count(*) from engines e left join instances i on i.id = e.connected_instance where e.deleted_at is null and e.revoked_at is null and not (e.connected_instance is not null and coalesce(i.heartbeat_at > now() - interval '15 seconds', false)) and coalesce(e.last_seen_at, e.enrolled_at) < now() - interval '60 seconds'`), `nexora_mgmt_rollouts{engine_group,state}` (gauge, help `Open and halted rollouts by engine group and state`, from `select g.name, r.state, count(*) from rollouts r join engine_groups g on g.id = r.engine_group_id where r.state in ('pending','canary','verifying','rolling','halted') group by 1, 2`).
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/fleet/ -count=1` and expect `ok`.
- [ ] Write the failing test `mgmt/internal/control/keys_test.go`:
  ```go
  package control_test

  import (
  	"testing"

  	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
  	"github.com/piwi3910/nexora/mgmt/internal/control"
  )

  func TestKeysFilteredToTargetSnapshot(t *testing.T) {
  	snap := &controlv1.ConfigSnapshot{
  		RpzZones: []*controlv1.RpzZone{{Id: "zone-a"}},
  		AuthZones: []*controlv1.AuthZone{{
  			Name:           "served.test.",
  			Transfer:       &controlv1.TransferPolicy{TsigKey: "xfr.key."},
  			Notify:         []*controlv1.NotifyTarget{{Address: "192.0.2.9:53", TsigKey: "notify.key."}},
  			UpdateTsigKeys: []string{"update.key."},
  		}},
  	}
  	rpz := control.FilterRPZKeys(snap, &controlv1.RpzTsigKeys{Keys: []*controlv1.RpzTsigKey{{ZoneId: "zone-a"}, {ZoneId: "zone-b"}}})
  	if len(rpz.Keys) != 1 || rpz.Keys[0].ZoneId != "zone-a" {
  		t.Fatalf("rpz keys %v, want only zone-a", rpz.Keys)
  	}
  	km := control.FilterKeyMaterial(snap, &controlv1.KeyMaterial{TsigKeys: []*controlv1.TsigSecret{
  		{Name: "xfr.key."}, {Name: "notify.key."}, {Name: "update.key."}, {Name: "other-group.key."},
  	}})
  	names := map[string]bool{}
  	for _, k := range km.TsigKeys {
  		names[k.Name] = true
  	}
  	if len(names) != 3 || names["other-group.key."] {
  		t.Fatalf("key material %v, want the three keys the snapshot names", names)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/control/ -run TestKeysFilteredToTargetSnapshot -count=1` and expect FAIL with `undefined: control.FilterRPZKeys`; create `mgmt/internal/control/keys.go` with both functions (they return new messages holding the matching element pointers; `FilterKeyMaterial` keeps keys named by any auth zone's `transfer.tsig_key`, `notify[].tsig_key` or `update_tsig_keys`) and expect `ok`.
- [ ] Change the hub and server in `mgmt/internal/control`:
  - `subscriber` gains `engineGroupID uuid.UUID`, `control chan *controlv1.ServerMessage` (capacity 4, never replaced: certificate messages) and `revoked chan struct{}` (closed once).
  - `Hub.listen` LISTENs on `nexora_rollout`, `nexora_engine_updated`, `nexora_engine_revoked` and `nexora_engine_rotate` (keep the 30 s safety resync and the initial resync after connecting). `nexora_rollout` payload = engine group id: `h.push(ctx, s)` for every subscriber of that group. `nexora_engine_updated` (engine id): `h.push` for that engine's subscriber (TargetFor reloads its group). `nexora_engine_revoked` (engine id): close that subscriber's `revoked`. `nexora_engine_rotate` (engine id): queue `RenewCertificate{Reason: REASON_ROTATE}` on its `control` channel. The safety resync calls `h.push` for every subscriber.
  - `h.push(ctx, s)`: `t, err := fleet.TargetFor(ctx, h.st.Pool, s.engineID)`; set `s.engineGroupID = t.EngineGroupID`; when `t.Snapshot != nil`, offer `FilterRPZKeys(t.Snapshot, keys)` and `FilterKeyMaterial(t.Snapshot, km)` (digest = lowercase hex SHA-256 of the deterministic encoding of the filtered set; the unfiltered `KeyMaterial` is cleared after offering, as today) and then `s.offer(t.Version, t.Snapshot)`.
  - `Server.Connect`: after registering the subscriber, `t, err := fleet.TargetFor(ctx, s.st.Pool, id)` replaces `snapshot.Latest`; keys are offered filtered as in `push`; `hello.AppliedVersion < t.Version` -> `sub.offer`; `t.Version > 0 && hello.AppliedVersion > t.Version` -> set `version_ahead` and send `VersionAhead{ServerVersion: t.Version}` (M1 behaviour); `t.RotateRequested` -> queue `RenewCertificate{REASON_ROTATE}`. The send loop also drains `sub.control`. `receive` runs in its own goroutine and `Connect` returns `status.Error(codes.PermissionDenied, "certificate revoked")` when `sub.revoked` closes first.
  - `Applied` and `Rejected` handling additionally run `select pg_notify('nexora_rollout', engine_group_id::text) from engines where id = $1` after their update, so controllers step at once.
- [ ] Change `setupServers` in `mgmt/internal/control/control_test.go` to start `go (&rollout.Controller{Store: st, Tick: 100 * time.Millisecond}).Run(ctx)` next to each hub.
- [ ] Write the failing test `mgmt/internal/control/fleet_push_test.go`:
  ```go
  package control_test

  import (
  	"testing"
  	"time"

  	"github.com/jackc/pgx/v5"
  	"google.golang.org/protobuf/proto"

  	"github.com/piwi3910/nexora/e2e/harness"
  	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
  	"github.com/piwi3910/nexora/mgmt/internal/auth"
  	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
  )

  func connectAs(t *testing.T, f *fixture, node string) (controlv1.EngineControl_ConnectClient, string) {
  	t.Helper()
  	client, id := f.enroll(t, f.addr[0])
  	stream, err := client.Connect(f.ctx)
  	if err != nil {
  		t.Fatal(err)
  	}
  	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, NodeName: node}}})
  	s := recvSnapshot(t, stream)
  	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Applied{Applied: &controlv1.Applied{Version: s.Version}}})
  	return stream, id
  }

  func TestCanaryVersionReachesOnlyCanariesUntilHealthy(t *testing.T) {
  	f := setup(t, 1)
  	a, aID := connectAs(t, f, "canary-a")
  	b, _ := connectAs(t, f, "canary-b")
  	harness.Eventually(t, 5*time.Second, func() error {
  		return expectRow(f, "select count(*) from rollouts where version = 1 and state = 'completed'", int64(1))
  	})
  	if _, err := f.st.Pool.Exec(f.ctx, `update engine_groups set rollout_strategy = 'canary', canary_count = 1, min_health_queries = 10`); err != nil {
  		t.Fatal(err)
  	}
  	v, err := snapshot.Mutate(f.ctx, f.st, snapshot.BuildConfig{}, auth.Actor{Type: "system", ID: "t", Name: "t"}, func(tx pgx.Tx) (auth.Change, error) {
  		_, err := tx.Exec(f.ctx, "update resolver_settings set block_ttl = 11")
  		return auth.Change{Action: "updateResolverSettings", TargetType: "resolver_settings", TargetID: "singleton"}, err
  	})
  	if err != nil {
  		t.Fatal(err)
  	}
  	// Positive path first: the canary receives the new version.
  	if s := recvSnapshot(t, a); s.Version != v {
  		t.Fatalf("canary got version %d, want %d", s.Version, v)
  	}
  	got := make(chan *controlv1.ServerMessage, 1)
  	go func() { m, _ := b.Recv(); got <- m }()
  	select {
  	case m := <-got:
  		t.Fatalf("non-canary received %v during the canary phase", m)
  	case <-time.After(1500 * time.Millisecond):
  	}
  	_ = a.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Applied{Applied: &controlv1.Applied{Version: v}}})
  	harness.Eventually(t, 5*time.Second, func() error {
  		return expectRow(f, "select count(*) from rollouts where state = 'verifying'", int64(1))
  	})
  	raw := func(q uint64) []byte { r, _ := proto.Marshal(&controlv1.Stats{QueriesTotal: q}); return r }
  	for i, q := range []uint64{100, 300, 500} {
  		if _, err := f.st.Pool.Exec(f.ctx, `insert into engine_stats (engine_id, at, stats)
  			select $1, now() - interval '40 seconds' + $2 * interval '10 seconds', $3`, aID, i, raw(q)); err != nil {
  			t.Fatal(err)
  		}
  	}
  	if _, err := f.st.Pool.Exec(f.ctx, `update rollouts set phase_started_at = now() - interval '35 seconds' where state = 'verifying'`); err != nil {
  		t.Fatal(err)
  	}
  	select {
  	case m := <-got:
  		if m.GetSnapshot().GetVersion() != v {
  			t.Fatalf("non-canary got %v, want version %d after the health gate", m, v)
  		}
  	case <-time.After(5 * time.Second):
  		t.Fatal("non-canary never received the version after the canary passed")
  	}
  }

  func expectRow(f *fixture, sql string, want any) error {
  	var got any
  	if err := f.st.Pool.QueryRow(f.ctx, sql).Scan(&got); err != nil {
  		return err
  	}
  	if got != want {
  		return fmt.Errorf("%s = %v, want %v", sql, got, want)
  	}
  	return nil
  }
  ```
  (import `fmt` alongside the packages listed).
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/control/ -run 'TestCanaryVersionReachesOnlyCanariesUntilHealthy|TestEnrollConnectPushAckReject|TestVersionAheadIsFlaggedNotDowngraded|TestNotifyFansOutToEngineOnOtherInstance' -count=1` before the hub change and expect FAIL with `non-canary received`; after it, expect `ok`.
- [ ] Change `mgmt/internal/api/handlers_fleet.go`: `ListEngines`, `GetEngine` and `DeleteEngine`'s `before` map `fleet.EngineView` to the existing `Engine` schema fields (`Status` from the view; the M1 fields keep their meaning) instead of `engineSelect`; remove `engineSelect` and `scanEngine`.
- [ ] Add `RolloutTick time.Duration` to `mgmt/internal/config/config.go` from `NEXORA_ROLLOUT_TICK` (default `1s`; outside `100ms`..`1m` -> `NEXORA_ROLLOUT_TICK must be between 100ms and 1m`) with a case in `config_test.go` for `50ms` expecting that error; in `serve` of `mgmt/cmd/nexora-mgmt/main.go` start `go (&rollout.Controller{Store: st, Tick: cfg.RolloutTick}).Run(ctx)` after `snapshot.EnsureInitial` and register `fleet.NewCollector(st)` next to `stats.NewCollector(st)`.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/... -count=1 && make e2e-build && NEXORA_E2E_BIN_DIR=$PWD/bin go test ./e2e/ -run "TestInvalidSnapshotRejected|TestMgmtStatelessHA|TestPerClientPolicy|TestAuthoritativeZonePropagation" -count=1 -timeout 20m'` and expect every package and test `ok` (all_at_once is the default, so M1–M4 flows reach every engine without a controller tick).
- [ ] Reconciled with the code while building (binding):
  - `driveOne` locks, after the advisory lock, the engine group row `FOR NO KEY UPDATE`, then the rollout `FOR UPDATE` (same order as `rollout.Create`); a rollout deleted with its group since listing is skipped; health is loaded for canaries only.
  - `fleet.Targets(ctx, q, EngineFilter) (map[uuid.UUID]Target, error)` loads many targets decoding each group snapshot once; `TargetFor` uses it. The hub pushes through it and coalesces notifications arriving within 10 ms (at most 256) into one push per engine group or engine. The hub no longer LISTENs on `nexora_config`.
  - `FilterKeyMaterial(snap, km, zoned map[string]bool)` keeps the keys `snap`'s auth zones name (also `AuthZone.primary_tsig_keys`, M4 per-primary NOTIFY keys) plus every key no zone of any group names (`zoned` from the new `(*TSIGKeys).ZoneKeyNames`); only keys used exclusively by other groups' zones are left out. TSIG keys are fleet-wide objects and M4's `TestSecondaryAndDynamicUpdate` expects a NOTIFY signed with a valid key no zone names to be REFUSED, not BADKEY; `keys_test.go` adds such a key.
  - Extra tests in `mgmt/internal/rollout/controller_ha_test.go`: `TestTwoControllersNeverDoubleAdvance` (two instances, each a running controller plus four goroutines calling `Step`; a trigger logs every controller write, and the committed log must be exactly `pending->canary, canary->verifying, verifying->rolling, rolling->completed`) and `TestControllerRestartResumesFromDatabase` (a controller exits mid-rollout, another dies holding the lock with an uncommitted transition and is terminated; a controller on a second instance resumes from the committed state).
  - `fleet_test.go` needs `github.com/kylelemons/godebug` (via `prometheus/testutil`) in `go.mod`.
- [ ] Commit: `git add go.mod docs/architecture.md mgmt/internal/rollout mgmt/internal/fleet mgmt/internal/control mgmt/internal/store/storetest/fleet.go mgmt/internal/api/handlers_fleet.go mgmt/internal/config mgmt/cmd/nexora-mgmt && git commit -m "feat(mgmt): rollout controller, targeted pushes, fleet status and metrics"`.

## Task 7: Fleet HTTP API, engine-group scoping in the API, and the fleet harness

Files: `mgmt/api/openapi.yaml` (new operations, extended `Engine`/`JoinToken` schemas, `engine_group_id` on scoped schemas), `mgmt/internal/api/gen.go` and `web/src/api/schema.d.ts` (regenerated on the laptop), `mgmt/internal/api/fleet_errors.go` (coded API errors), `mgmt/internal/api/server.go` (`mapError` case), `mgmt/internal/api/fleet_groups.go` (engine group and rollback/resume handlers), `mgmt/internal/api/fleet_rollouts.go` (rollout list/detail, fleet summary), `mgmt/internal/api/fleet_engines.go` (update engine, engine stats), `mgmt/internal/api/handlers_fleet.go` (engine schema mapping, join tokens with group, labels, max uses), `mgmt/internal/fleet/groups.go` (engine group queries, label validation), `mgmt/internal/fleet/stats.go` (derived per-engine series), `mgmt/internal/control/jointokens.go` (`CreateJoinTokenFor`), scoped resource handlers and stores (`mgmt/internal/api/handlers_dns.go` upstreams and filter lists, `policies.go`, `rewrites.go`, `resolution.go` forward zones, `rpz.go`, `zones.go`; `mgmt/internal/store/policies.go`, `rewrites.go`, `resolution.go`; `mgmt/internal/zone/service.go`), `mgmt/internal/auth/permissions.go`, `mgmt/internal/auth/permissions_fleet_test.go`, `web/src/auth/permissions.ts`, `e2e/harness/mgmt.go` (`EngineView` fields, `EngineOptions.ExtraEnv`, `Engine` restart data), `e2e/harness/engine.go` (metric scraping shared with mgmt), `e2e/harness/fleet.go` (fleet helpers), `e2e/fleet_api_test.go` (`TestFleetAPI`)
Interfaces: operationIds `listEngineGroups`, `createEngineGroup`, `getEngineGroup`, `updateEngineGroup`, `deleteEngineGroup`, `rollbackEngineGroup`, `resumeEngineGroupRollouts`, `listRollouts`, `getRollout`, `getFleetSummary`, `updateEngine`, `getEngineStats`; existing `listEngines`, `getEngine`, `deleteEngine`, `listJoinTokens`, `createJoinToken`, `revokeJoinToken` extended; error codes `conflict` (M1), `name_taken`, `engine_group_protected`, `engine_group_not_empty`, `engine_group_not_found`, `engine_group_scope`, `invalid_rollout_params`, `invalid_labels`, `version_not_found`, `not_older`, `not_paused`, `engine_revoked`; harness API below.

```go
package harness
const DefaultEngineGroupID = "00000000-0000-0000-0000-000000000001"
type EngineGroupView struct { ID, Name, Description, UpstreamMode, RolloutStrategy string; CanaryCount, CanaryPercent, EngineCount int; RolloutsPaused bool; StableVersion *uint64; Revision int64 }
type RolloutView struct { ID, EngineGroupID, Kind, Strategy, State, HaltReason string; Version uint64; FromVersion *uint64; CanaryEngineIDs []string }
func (a *API) CreateEngineGroup(body map[string]any) EngineGroupView
func (a *API) EngineGroup(id string) EngineGroupView
func (a *API) WaitEngineGroupStable(id string, after uint64, timeout time.Duration) uint64
func (a *API) WaitRollout(engineGroupID string, minVersion uint64, timeout time.Duration, states ...string) RolloutView
func (a *API) CreateJoinTokenFor(engineGroupID string, labels map[string]string) string
func (a *API) EngineByNode(nodeName string) EngineView
func (a *API) PatchEngine(nodeName string, fields map[string]any) EngineView
func (a *API) ErrorCode(method, path string, body any) (int, string)
func (e *Env) RestartEngine(en *Engine)
func (m *Mgmt) Metric(t *testing.T, name string, labels map[string]string) float64
// EngineView gains: EngineGroupID, EngineGroupName, CertificateSerial string; Labels map[string]string; Revision int64; TargetVersion uint64; RevokedAt *time.Time
// EngineOptions gains: ExtraEnv []string
```

> As built (deviations, 2026-09-14): `revokeEngine` and `rotateEngineCertificate` (Task 8) were added to `openapi.yaml` in the same regeneration. `gen.go` is regenerated with oapi-codegen v2.8.0 in the dev pod (the laptop has v2.6.0); the new `revoked` enum value made oapi-codegen prefix the `DnssecStatusEnginesTrustAnchorsState` constants, so `mgmt/internal/api/dnssec.go` uses the prefixed names, and `web/src/pages/EnginesPage.tsx` gained the `revoked` status tone and help text so the web build type-checks. `422` responses are declared on `createUpstream`, `updateUpstream`, `createFilterList`, `updateFilterList`, `createForwardZone`, `updateForwardZone`, `createRpzZone`, `createJoinToken` and `updateEngine`. `updateEngineGroup` keeps the stored value of every optional field the body omits. Zones and RPZ zones are scoped at creation only (`ZoneCreate`/`RpzZoneInput`; the partial `ZoneUpdate`/`RpzZoneUpdate` bodies carry no `engine_group_id`, as the schema list above already implied); `zone.Service.UpdateZone` is unchanged and `zone.ErrUnknownEngineGroup` maps to 422 `engine_group_not_found`. The global-rewrite CNAME conflict check only compares rewrites whose engine groups can meet in one snapshot (same group, or either global). `fleet.DeleteEngineGroup` moves soft-deleted engines of the group to `default` before deleting it (their rows would otherwise hold the foreign key). `openapi.yaml` is prettier-formatted. Extra tests: `mgmt/internal/fleet/stats_test.go` `TestSeriesDerivesRatesAndSkipsCounterResets`.

- [ ] Write the failing permission test `mgmt/internal/auth/permissions_fleet_test.go`:
  ```go
  package auth

  import "testing"

  func TestFleetPermissions(t *testing.T) {
  	want := map[string]Role{
  		"listEngineGroups": RoleViewer, "getEngineGroup": RoleViewer, "listRollouts": RoleViewer, "getRollout": RoleViewer,
  		"getFleetSummary": RoleViewer, "listEngines": RoleViewer, "getEngine": RoleViewer, "getEngineStats": RoleViewer,
  		"createEngineGroup": RoleOperator, "updateEngineGroup": RoleOperator, "rollbackEngineGroup": RoleOperator,
  		"resumeEngineGroupRollouts": RoleOperator, "updateEngine": RoleOperator,
  		"deleteEngineGroup": RoleAdmin, "deleteEngine": RoleAdmin, "listJoinTokens": RoleAdmin,
  		"createJoinToken": RoleAdmin, "revokeJoinToken": RoleAdmin,
  	}
  	for op, role := range want {
  		got, ok := Permissions[op]
  		if !ok {
  			t.Errorf("%s has no permission entry", op)
  			continue
  		}
  		if got != role {
  			t.Errorf("%s requires %v, want %v", op, got, role)
  		}
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/auth/ -run TestFleetPermissions -count=1` and expect FAIL with `listEngineGroups has no permission entry`; add the twelve new entries to `Permissions` in `mgmt/internal/auth/permissions.go` (the six existing entries already match) and the same entries to `web/src/auth/permissions.ts` (lower-case role strings); rerun and expect `ok`.
- [ ] Add to `mgmt/api/openapi.yaml` under `paths`:
  ```yaml
  /engine-groups:
    get:
      operationId: listEngineGroups
      responses:
        "200":
          description: ok
          content:
            application/json:
              schema:
                {
                  type: array,
                  items: { $ref: "#/components/schemas/EngineGroup" },
                }
    post:
      operationId: createEngineGroup
      requestBody:
        required: true
        content:
          {
            application/json:
              { schema: { $ref: "#/components/schemas/EngineGroupInput" } },
          }
      responses:
        "201":
          {
            description: created,
            content:
              {
                application/json:
                  { schema: { $ref: "#/components/schemas/EngineGroup" } },
              },
          }
        "400": { $ref: "#/components/responses/Error" }
        "409": { $ref: "#/components/responses/Error" }
  /engine-groups/{id}:
    get:
      operationId: getEngineGroup
      parameters: [{ $ref: "#/components/parameters/Id" }]
      responses:
        "200":
          {
            description: ok,
            content:
              {
                application/json:
                  { schema: { $ref: "#/components/schemas/EngineGroup" } },
              },
          }
        "404": { $ref: "#/components/responses/Error" }
    put:
      operationId: updateEngineGroup
      parameters: [{ $ref: "#/components/parameters/Id" }]
      requestBody:
        required: true
        content:
          {
            application/json:
              { schema: { $ref: "#/components/schemas/EngineGroupUpdate" } },
          }
      responses:
        "200":
          {
            description: ok,
            content:
              {
                application/json:
                  { schema: { $ref: "#/components/schemas/EngineGroup" } },
              },
          }
        "400": { $ref: "#/components/responses/Error" }
        "404": { $ref: "#/components/responses/Error" }
        "409": { $ref: "#/components/responses/Error" }
    delete:
      operationId: deleteEngineGroup
      parameters:
        [
          { $ref: "#/components/parameters/Id" },
          { $ref: "#/components/parameters/Revision" },
        ]
      responses:
        "204": { description: deleted }
        "404": { $ref: "#/components/responses/Error" }
        "409": { $ref: "#/components/responses/Error" }
  /engine-groups/{id}/rollback:
    post:
      operationId: rollbackEngineGroup
      parameters: [{ $ref: "#/components/parameters/Id" }]
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              additionalProperties: false
              required: [to_version]
              properties:
                { to_version: { type: integer, format: int64, minimum: 1 } }
      responses:
        "202":
          {
            description: rollback rollout created,
            content:
              {
                application/json:
                  { schema: { $ref: "#/components/schemas/Rollout" } },
              },
          }
        "400": { $ref: "#/components/responses/Error" }
        "404": { $ref: "#/components/responses/Error" }
  /engine-groups/{id}/resume-rollouts:
    post:
      operationId: resumeEngineGroupRollouts
      parameters: [{ $ref: "#/components/parameters/Id" }]
      responses:
        "202":
          {
            description: fresh version published,
            content:
              {
                application/json:
                  { schema: { $ref: "#/components/schemas/Rollout" } },
              },
          }
        "404": { $ref: "#/components/responses/Error" }
        "409": { $ref: "#/components/responses/Error" }
  /rollouts:
    get:
      operationId: listRollouts
      parameters:
        - {
            name: engine_group_id,
            in: query,
            schema: { type: string, format: uuid },
          }
        - {
            name: state,
            in: query,
            schema: { $ref: "#/components/schemas/RolloutState" },
          }
        - {
            name: limit,
            in: query,
            schema: { type: integer, minimum: 1, maximum: 200 },
          }
      responses:
        "200":
          description: newest version first
          content:
            application/json:
              schema:
                { type: array, items: { $ref: "#/components/schemas/Rollout" } }
        "400": { $ref: "#/components/responses/Error" }
  /rollouts/{id}:
    get:
      operationId: getRollout
      parameters: [{ $ref: "#/components/parameters/Id" }]
      responses:
        "200":
          {
            description: ok,
            content:
              {
                application/json:
                  { schema: { $ref: "#/components/schemas/RolloutDetail" } },
              },
          }
        "404": { $ref: "#/components/responses/Error" }
  /fleet/summary:
    get:
      operationId: getFleetSummary
      responses:
        "200":
          {
            description: ok,
            content:
              {
                application/json:
                  { schema: { $ref: "#/components/schemas/FleetSummary" } },
              },
          }
  /engines/{id}/stats:
    get:
      operationId: getEngineStats
      parameters:
        - { $ref: "#/components/parameters/Id" }
        - {
            name: window,
            in: query,
            schema: { type: string, enum: [5m, 1h, 24h] },
          }
      responses:
        "200":
          {
            description: ok,
            content:
              {
                application/json:
                  { schema: { $ref: "#/components/schemas/EngineStats" } },
              },
          }
        "400": { $ref: "#/components/responses/Error" }
        "404": { $ref: "#/components/responses/Error" }
  ```
  add `patch` with `operationId: updateEngine`, `parameters: [{ $ref: "#/components/parameters/Id" }]`, request body `EngineUpdate`, responses `200` (`Engine`), `400`, `404`, `409` to the existing `/engines/{id}` path; and add under `components.schemas`:
  ```yaml
  RolloutState:
    {
      type: string,
      enum:
        [
          pending,
          canary,
          verifying,
          rolling,
          completed,
          halted,
          rolled_back,
          superseded,
        ],
    }
  EngineGroupInput:
    type: object
    additionalProperties: false
    required: [name]
    properties:
      name: { type: string, pattern: "^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$" }
      description: { type: string, maxLength: 1024 }
      upstream_mode: { type: string, enum: [inherit, override] }
      extra_acl_cidrs: { type: array, items: { type: string }, maxItems: 256 }
      otlp_endpoint: { type: string, maxLength: 512 }
      rollout_strategy: { type: string, enum: [all_at_once, canary] }
      canary_count: { type: integer, minimum: 0 }
      canary_percent: { type: integer, minimum: 0, maximum: 100 }
      ack_timeout_seconds: { type: integer, minimum: 5, maximum: 3600 }
      health_window_seconds: { type: integer, minimum: 20, maximum: 3600 }
      max_servfail_ratio: { type: number, minimum: 0, maximum: 1 }
      min_health_queries: { type: integer, minimum: 0 }
  EngineGroupUpdate:
    allOf:
      - { $ref: "#/components/schemas/EngineGroupInput" }
      - {
          type: object,
          required: [revision],
          properties: { revision: { type: integer, format: int64 } },
        }
  EngineGroup:
    type: object
    required:
      [
        id,
        name,
        description,
        upstream_mode,
        extra_acl_cidrs,
        otlp_endpoint,
        rollout_strategy,
        canary_count,
        canary_percent,
        ack_timeout_seconds,
        health_window_seconds,
        max_servfail_ratio,
        min_health_queries,
        rollouts_paused,
        engine_count,
        revision,
        created_at,
        updated_at,
      ]
    properties:
      id: { type: string, format: uuid }
      name: { type: string }
      description: { type: string }
      upstream_mode: { type: string, enum: [inherit, override] }
      extra_acl_cidrs: { type: array, items: { type: string } }
      otlp_endpoint: { type: string }
      rollout_strategy: { type: string, enum: [all_at_once, canary] }
      canary_count: { type: integer }
      canary_percent: { type: integer }
      ack_timeout_seconds: { type: integer }
      health_window_seconds: { type: integer }
      max_servfail_ratio: { type: number }
      min_health_queries: { type: integer }
      rollouts_paused: { type: boolean }
      stable_version: { type: [integer, "null"], format: int64 }
      engine_count: { type: integer }
      active_rollout:
        { oneOf: [{ $ref: "#/components/schemas/Rollout" }, { type: "null" }] }
      revision: { type: integer, format: int64 }
      created_at: { type: string, format: date-time }
      updated_at: { type: string, format: date-time }
  Rollout:
    type: object
    required:
      [
        id,
        engine_group_id,
        engine_group_name,
        version,
        kind,
        strategy,
        state,
        canary_engine_ids,
        halt_reason,
        created_by,
        created_at,
        progress,
      ]
    properties:
      id: { type: string, format: uuid }
      engine_group_id: { type: string, format: uuid }
      engine_group_name: { type: string }
      version: { type: integer, format: int64 }
      from_version: { type: [integer, "null"], format: int64 }
      kind: { type: string, enum: [change, rollback, republish] }
      strategy: { type: string, enum: [all_at_once, canary] }
      state: { $ref: "#/components/schemas/RolloutState" }
      canary_engine_ids: { type: array, items: { type: string, format: uuid } }
      phase_started_at: { type: [string, "null"], format: date-time }
      halt_reason: { type: string }
      created_by: { type: string }
      created_at: { type: string, format: date-time }
      finished_at: { type: [string, "null"], format: date-time }
      progress:
        type: object
        required: [total, applied, rejected]
        properties:
          {
            total: { type: integer },
            applied: { type: integer },
            rejected: { type: integer },
          }
  RolloutDetail:
    allOf:
      - { $ref: "#/components/schemas/Rollout" }
      - type: object
        required: [engines]
        properties:
          engines:
            type: array
            items:
              type: object
              required:
                [
                  engine_id,
                  node_name,
                  canary,
                  connected,
                  applied_version,
                  progress,
                  rejected_reason,
                ]
              properties:
                engine_id: { type: string, format: uuid }
                node_name: { type: string }
                canary: { type: boolean }
                connected: { type: boolean }
                applied_version: { type: integer, format: int64 }
                progress:
                  {
                    type: string,
                    enum: [waiting, applied, rejected, disconnected],
                  }
                rejected_reason: { type: string }
  EngineUpdate:
    type: object
    additionalProperties: false
    required: [revision]
    properties:
      revision: { type: integer, format: int64 }
      engine_group_id: { type: string, format: uuid }
      labels:
        {
          type: object,
          additionalProperties: { type: string, maxLength: 63 },
          maxProperties: 32,
        }
  EngineStats:
    type: object
    required: [window, samples]
    properties:
      window: { type: string, enum: [5m, 1h, 24h] }
      samples:
        type: array
        items:
          type: object
          required: [at, qps, cache_hit_ratio, servfail_ratio, p99_ms]
          properties:
            at: { type: string, format: date-time }
            qps: { type: number }
            cache_hit_ratio: { type: number }
            servfail_ratio: { type: number }
            p99_ms: { type: number }
  FleetSummary:
    type: object
    required: [engines_total, engines_by_status, halted_rollouts, engine_groups]
    properties:
      engines_total: { type: integer }
      engines_by_status:
        { type: object, additionalProperties: { type: integer } }
      halted_rollouts: { type: integer }
      engine_groups:
        type: array
        items:
          type: object
          required:
            [id, name, engines, connected, rollout_strategy, rollouts_paused]
          properties:
            id: { type: string, format: uuid }
            name: { type: string }
            engines: { type: integer }
            connected: { type: integer }
            rollout_strategy: { type: string, enum: [all_at_once, canary] }
            rollouts_paused: { type: boolean }
            stable_version: { type: [integer, "null"], format: int64 }
            active_rollout:
              {
                oneOf:
                  [{ $ref: "#/components/schemas/Rollout" }, { type: "null" }],
              }
  ```
  Extend existing schemas: `Engine` adds required `engine_group_id` (uuid), `engine_group_name`, `labels` (object of strings), `revision` (int64), `target_version` (int64), `certificate_serial`, optional `revoked_at`, `cert_rotate_requested_at`, `certificate_not_after` (nullable date-time), and `status` gains `revoked`; `JoinToken` adds required `engine_group_id`, `engine_group_name`, `labels`, `state` (`active|expired|exhausted|revoked`) and optional nullable `max_uses`; `JoinTokenCreate` adds optional `engine_group_id` (uuid), `max_uses` (integer 1..100000) and `labels` (object of strings, at most 32). Add `engine_group_id: { type: [string, "null"], format: uuid, description: "engine group; null applies to every group" }` to `UpstreamInput`, `Upstream`, `FilterListInput`, `FilterList`, `PolicyGroupInput`, `PolicyGroup`, `RewriteInput`, `Rewrite`, `ForwardZoneInput`, `ForwardZone`, `RpzZoneInput`, `RpzZone`, `ZoneCreate`, `Zone` (required in the response schemas).
- [ ] On the laptop run `make proto` and expect exit 0; then `scripts/dev-exec.sh go build ./mgmt/...` fails with `does not implement StrictServerInterface` until the handlers below exist.
- [ ] Create `mgmt/internal/api/fleet_errors.go`:
  ```go
  package api

  import "fmt"

  // apiError is a request refused with a specific status and error code.
  type apiError struct {
  	status    int
  	code, msg string
  }

  func (e apiError) Error() string { return e.msg }

  func coded(status int, code, format string, args ...any) error {
  	return apiError{status: status, code: code, msg: fmt.Sprintf(format, args...)}
  }
  ```
  and in `mapError` (`mgmt/internal/api/server.go`) add as the first case `case errors.As(err, &aerr): writeError(w, aerr.status, aerr.code, aerr.msg)` with `var aerr apiError`.
- [ ] Create `mgmt/internal/fleet/groups.go`: `type EngineGroup struct` mirroring the table (with `EngineCount int`, `StableVersion *uint64`, `ExtraACLCIDRs []string`); `ListEngineGroups(ctx, q)`, `GetEngineGroup(ctx, q, id)`, `CreateEngineGroup(ctx, tx, g)`, `UpdateEngineGroup(ctx, tx, g, revision)` (`update ... revision = revision + 1, updated_at = now() where id = $1 and revision = $2 returning ...`; no row and the group exists -> `store.ErrConflict`), `DeleteEngineGroup(ctx, tx, id, revision)` (returns counts of engines, scoped rows over `store.EngineScopedTables` and unrevoked unexpired join tokens when any is non-zero, wrapped in `ErrEngineGroupNotEmpty`); `LabelKeyRE`, a `regexp.MustCompile` of the label key pattern from the "Engine groups and scoping" section of `docs/architecture.md`; `ValidateLabels(map[string]string) error` (key pattern, value at most 63 characters, at most 32 labels; the error message names the offending key); `ErrEngineGroupNotEmpty`, `ErrEngineGroupProtected`.
- [ ] Create `mgmt/internal/fleet/stats.go`: `Series(ctx, q, engineID uuid.UUID, window time.Duration) ([]Point, error)` reads `select at, stats from engine_stats where engine_id = $1 and at > now() - $2 * interval '1 second' order by at` and, for each consecutive pair (skipping pairs whose `queries_total` went down), returns `Point{At, QPS, CacheHitRatio, ServfailRatio, P99Ms}` where `QPS = dQueries / seconds`, `CacheHitRatio = dHits / (dHits + dMisses)` (0 without lookups), `ServfailRatio = dServfail / dQueries` (0 without queries), and `P99Ms` is the upper bound (ms) of the first `duration_bucket_bounds_us` entry whose cumulative count delta reaches 99% of the pair's total count delta (0 when the delta is 0).
- [ ] Implement the handlers; every mutation uses `h.mutate` (publishes a version) or, where no configuration changes, `h.audited` (audit only):
  - `createEngineGroup` (`fleet_groups.go`): canary strategy with `canary_count == 0 && canary_percent == 0` -> `coded(400, "invalid_rollout_params", "canary strategy needs canary_count or canary_percent")`; unique violation on `name` -> `coded(409, "name_taken", "engine group %q already exists")`; `extra_acl_cidrs` parse with `netip.ParsePrefix` (400 `invalid_request`); inside `h.mutate` (a new group gets its first snapshot with the publish); 201.
  - `updateEngineGroup`: renaming `default` -> `coded(409, "engine_group_protected", ...)`; stale revision -> `store.ErrConflict` (409 `conflict`); inside `h.mutate`; 200.
  - `deleteEngineGroup`: `default` -> 409 `engine_group_protected`; `ErrEngineGroupNotEmpty` -> `coded(409, "engine_group_not_empty", "engine group has %d engines, %d scoped resources and %d active join tokens")`; inside `h.audited`; 204.
  - `rollbackEngineGroup`: runs in `h.d.Store.InTx`: `to_version` without a `group_snapshots` row for the group -> `coded(404, "version_not_found", ...)`; `to_version` not below the group's newest group snapshot version -> `coded(400, "not_older", ...)`; else `snapshot.Republish(ctx, tx, actor, id, to_version, rollout.KindRollback)`; 202 with the created rollout.
  - `resumeEngineGroupRollouts`: not paused -> `coded(409, "not_paused", ...)`; else inside `h.mutate`: `update engine_groups set rollouts_paused = false` (the publish creates the fresh version); 202 with the group's newest rollout.
  - `listRollouts` / `getRollout` (`fleet_rollouts.go`): newest version first, `limit` default 50; `progress.total` counts the group's non-revoked, non-deleted engines (only canaries while `canary`/`verifying`), `applied` those with `applied_version >= version`, `rejected` those with `rejected_version = version`; detail `progress` per engine: `rejected`, `applied`, `disconnected`, `waiting` in that precedence.
  - `getFleetSummary`: from `fleet.ListEngines` and `fleet.ListEngineGroups`; `active_rollout` is the group's newest rollout in `pending`/`canary`/`verifying`/`rolling`/`halted`.
  - `updateEngine` (`fleet_engines.go`): stale revision -> 409 `conflict`; revoked engine -> `coded(409, "engine_revoked", ...)`; `fleet.ValidateLabels` error -> `coded(400, "invalid_labels", ...)`; unknown `engine_group_id` -> `coded(422, "engine_group_not_found", ...)`; update `engine_group_id`, `labels`, `revision = revision + 1`; on a group change inside the same transaction: `snapshot.Republish(ctx, tx, actor, newGroup, from, rollout.KindRepublish)` where `from` is the new group's `stable_version` or, when NULL, its newest group snapshot version, then `pg_notify('nexora_engine_updated', id)`; audit `updateEngine`; 200 with the engine.
  - `getEngineStats`: window `5m` (default), `1h`, `24h` -> `fleet.Series`; unknown engine -> 404.
  - `listEngines` / `getEngine` / `deleteEngine` (`handlers_fleet.go`): map every `fleet.EngineView` field to the extended schema; `deleteEngine` stays as M1 until Task 8 adds revocation.
  - `createJoinToken`: add `control.CreateJoinTokenFor(ctx, tx, ca, control.JoinTokenSpec{Name, CreatedBy string; TTL time.Duration; EngineGroupID uuid.UUID; MaxUses *int; Labels map[string]string})` in `mgmt/internal/control/jointokens.go` (inserts `engine_group_id`, `labels`, `max_uses`); `control.CreateJoinToken(ctx, tx, ca, name, createdBy, ttl)` calls it with the default group, no labels and unlimited uses; unknown group -> 422 `engine_group_not_found`; invalid labels -> 400 `invalid_labels`. `listJoinTokens` adds the group name, labels, max uses and `state` (`revoked` if `revoked_at`, `expired` if `expires_at <= now()`, `exhausted` if `max_uses is not null and uses >= max_uses`, else `active`).
  - Scoped resources: every create/update body accepts `engine_group_id` (store structs gain `EngineGroupID *uuid.UUID`; inserts, updates and selects carry the column); an unknown id -> 422 `engine_group_not_found`. A rewrite with both `group_id` and `engine_group_id` -> 422 `engine_group_scope` (`a rewrite inside a policy group follows the policy group's engine group`). A policy group whose `filter_list_ids` include a list scoped to another engine group -> 422 `engine_group_scope`; a filter list re-scoped while a policy group of another engine group selects it -> 422 `engine_group_scope`. Zones: `zone.Service` create/update carry `EngineGroupID`; names stay unique fleet-wide (M4 behaviour unchanged).
- [ ] Extend `e2e/harness/mgmt.go`: `EngineView` gains `EngineGroupID string json:"engine_group_id"`, `EngineGroupName string json:"engine_group_name"`, `Labels map[string]string json:"labels"`, `Revision int64 json:"revision"`, `TargetVersion uint64 json:"target_version"`, `CertificateSerial string json:"certificate_serial"`, `RevokedAt *time.Time json:"revoked_at"`; `EngineOptions` gains `ExtraEnv []string` (passed to `e.Start` for the engine process); `Engine` gains an unexported `env []string` set by `StartManagedEngineWith`. In `e2e/harness/engine.go` move the body of `(*Engine).Metric` into `scrapeMetric(t *testing.T, url, name string, labels map[string]string) float64` and call it with `"http://" + en.Metrics + "/metrics"`.
- [ ] Create `e2e/harness/fleet.go`:
  ```go
  package harness

  import (
  	"encoding/json"
  	"fmt"
  	"net/http"
  	"strings"
  	"testing"
  	"time"
  )

  // DefaultEngineGroupID is the engine group that always exists.
  const DefaultEngineGroupID = "00000000-0000-0000-0000-000000000001"

  // EngineGroupView is the part of the API's EngineGroup the tests inspect.
  type EngineGroupView struct {
  	ID              string  `json:"id"`
  	Name            string  `json:"name"`
  	Description     string  `json:"description"`
  	UpstreamMode    string  `json:"upstream_mode"`
  	RolloutStrategy string  `json:"rollout_strategy"`
  	CanaryCount     int     `json:"canary_count"`
  	CanaryPercent   int     `json:"canary_percent"`
  	EngineCount     int     `json:"engine_count"`
  	RolloutsPaused  bool    `json:"rollouts_paused"`
  	StableVersion   *uint64 `json:"stable_version"`
  	Revision        int64   `json:"revision"`
  }

  // RolloutView is the part of the API's Rollout the tests inspect.
  type RolloutView struct {
  	ID              string   `json:"id"`
  	EngineGroupID   string   `json:"engine_group_id"`
  	Kind            string   `json:"kind"`
  	Strategy        string   `json:"strategy"`
  	State           string   `json:"state"`
  	HaltReason      string   `json:"halt_reason"`
  	Version         uint64   `json:"version"`
  	FromVersion     *uint64  `json:"from_version"`
  	CanaryEngineIDs []string `json:"canary_engine_ids"`
  }

  // CreateEngineGroup posts body to /engine-groups and expects 201.
  func (a *API) CreateEngineGroup(body map[string]any) EngineGroupView {
  	a.T.Helper()
  	var g EngineGroupView
  	a.Must(http.MethodPost, "/engine-groups", body, &g, http.StatusCreated)
  	return g
  }

  // EngineGroup returns one engine group.
  func (a *API) EngineGroup(id string) EngineGroupView {
  	a.T.Helper()
  	var g EngineGroupView
  	a.Must(http.MethodGet, "/engine-groups/"+id, nil, &g, http.StatusOK)
  	return g
  }

  // WaitEngineGroupStable waits until the group's stable version is above after and returns it.
  func (a *API) WaitEngineGroupStable(id string, after uint64, timeout time.Duration) uint64 {
  	a.T.Helper()
  	var v uint64
  	Eventually(a.T, timeout, func() error {
  		g := a.EngineGroup(id)
  		if g.StableVersion == nil || *g.StableVersion <= after {
  			return fmt.Errorf("engine group %s stable %v, waiting for > %d", g.Name, g.StableVersion, after)
  		}
  		v = *g.StableVersion
  		return nil
  	})
  	return v
  }

  // WaitRollout waits for the group's newest rollout with version >= minVersion to reach one of states.
  func (a *API) WaitRollout(engineGroupID string, minVersion uint64, timeout time.Duration, states ...string) RolloutView {
  	a.T.Helper()
  	var got RolloutView
  	Eventually(a.T, timeout, func() error {
  		var rs []RolloutView
  		if _, err := a.Do(http.MethodGet, "/rollouts?limit=1&engine_group_id="+engineGroupID, nil, &rs); err != nil {
  			return err
  		}
  		if len(rs) == 0 || rs[0].Version < minVersion {
  			return fmt.Errorf("no rollout >= %d yet", minVersion)
  		}
  		for _, s := range states {
  			if rs[0].State == s {
  				got = rs[0]
  				return nil
  			}
  		}
  		return fmt.Errorf("rollout %d is %s (%s), waiting for %v", rs[0].Version, rs[0].State, rs[0].HaltReason, states)
  	})
  	return got
  }

  // CreateJoinTokenFor creates a one-hour, unlimited join token for an engine group.
  func (a *API) CreateJoinTokenFor(engineGroupID string, labels map[string]string) string {
  	a.T.Helper()
  	var created struct {
  		Token string `json:"token"`
  	}
  	body := map[string]any{"name": UniqueName("e2e"), "ttl_seconds": 3600, "engine_group_id": engineGroupID}
  	if len(labels) > 0 {
  		body["labels"] = labels
  	}
  	a.Must(http.MethodPost, "/join-tokens", body, &created, http.StatusCreated)
  	return created.Token
  }

  // EngineByNode returns the listed engine with nodeName.
  func (a *API) EngineByNode(nodeName string) EngineView {
  	a.T.Helper()
  	var engines []EngineView
  	a.Must(http.MethodGet, "/engines", nil, &engines, http.StatusOK)
  	for _, e := range engines {
  		if e.NodeName == nodeName {
  			return e
  		}
  	}
  	a.T.Fatalf("engine %s not listed", nodeName)
  	return EngineView{}
  }

  // PatchEngine sends fields with the engine's current revision and expects 200.
  func (a *API) PatchEngine(nodeName string, fields map[string]any) EngineView {
  	a.T.Helper()
  	e := a.EngineByNode(nodeName)
  	body := map[string]any{"revision": e.Revision}
  	for k, v := range fields {
  		body[k] = v
  	}
  	var out EngineView
  	a.Must(http.MethodPatch, "/engines/"+e.ID, body, &out, http.StatusOK)
  	return out
  }

  // ErrorCode sends a request that must fail and returns its status and the error body's code.
  func (a *API) ErrorCode(method, path string, body any) (int, string) {
  	a.T.Helper()
  	status, err := a.Do(method, path, body, nil)
  	if err == nil {
  		a.T.Fatalf("%s %s succeeded with %d, want an error", method, path, status)
  	}
  	msg := err.Error()
  	var e struct {
  		Code string `json:"code"`
  	}
  	if i := strings.Index(msg, "{"); i >= 0 {
  		_ = json.Unmarshal([]byte(msg[i:]), &e)
  	}
  	return status, e.Code
  }

  // RestartEngine stops en and starts it again on the same engine.toml and state directory, then
  // waits for its control stream (it must not enroll again).
  func (e *Env) RestartEngine(en *Engine) {
  	e.T.Helper()
  	en.Proc.Stop()
  	en.Proc = e.Start("nexora-engine", []string{"--config", en.ConfigPath}, en.env)
  	en.readAddrs()
  	en.Proc.WaitLog(controlConnected, 30*time.Second)
  }

  // Metric scrapes the management instance's /metrics like (*Engine).Metric.
  func (m *Mgmt) Metric(t *testing.T, name string, labels map[string]string) float64 {
  	t.Helper()
  	return scrapeMetric(t, m.BaseURL+"/metrics", name, labels)
  }
  ```
- [ ] Write the failing black-box test `e2e/fleet_api_test.go`:
  ```go
  package e2e

  import (
  	"net/http"
  	"regexp"
  	"testing"
  	"time"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  func TestFleetAPI(t *testing.T) {
  	env := harness.New(t)
  	pg := env.StartPostgres()
  	ca := env.InitCA()
  	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{})
  	api := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)

  	g := api.CreateEngineGroup(map[string]any{"name": "edge-a", "description": "first", "extra_acl_cidrs": []string{"198.51.100.0/24"}})
  	if g.Revision != 1 || g.RolloutsPaused || g.RolloutStrategy != "all_at_once" || g.UpstreamMode != "inherit" {
  		t.Fatalf("created engine group %+v", g)
  	}
  	stable := api.WaitEngineGroupStable(g.ID, 0, 15*time.Second)
  	var groups []harness.EngineGroupView
  	api.Must(http.MethodGet, "/engine-groups", nil, &groups, http.StatusOK)
  	if len(groups) != 2 {
  		t.Fatalf("engine groups = %+v, want default and edge-a", groups)
  	}
  	if code, c := api.ErrorCode(http.MethodPost, "/engine-groups", map[string]any{"name": "edge-a"}); code != http.StatusConflict || c != "name_taken" {
  		t.Fatalf("duplicate name: %d %s", code, c)
  	}
  	if code, c := api.ErrorCode(http.MethodPost, "/engine-groups", map[string]any{"name": "bad", "rollout_strategy": "canary"}); code != http.StatusBadRequest || c != "invalid_rollout_params" {
  		t.Fatalf("canary without size: %d %s", code, c)
  	}
  	upd := map[string]any{"name": "edge-a", "revision": g.Revision, "rollout_strategy": "canary", "canary_percent": 34, "health_window_seconds": 20}
  	api.Must(http.MethodPut, "/engine-groups/"+g.ID, upd, nil, http.StatusOK)
  	if code, c := api.ErrorCode(http.MethodPut, "/engine-groups/"+g.ID, upd); code != http.StatusConflict || c != "conflict" {
  		t.Fatalf("stale revision: %d %s", code, c)
  	}
  	if code, c := api.ErrorCode(http.MethodDelete, "/engine-groups/"+harness.DefaultEngineGroupID+"?revision=1", nil); code != http.StatusConflict || c != "engine_group_protected" {
  		t.Fatalf("delete default: %d %s", code, c)
  	}
  	g = api.EngineGroup(g.ID)
  	api.Must(http.MethodPut, "/engine-groups/"+g.ID, map[string]any{"name": "edge-a", "revision": g.Revision, "rollout_strategy": "all_at_once"}, nil, http.StatusOK)

  	api.Must(http.MethodPost, "/rewrites", map[string]any{"name": "scoped.fleet.test", "type": "A", "value": "192.0.2.7", "engine_group_id": g.ID}, nil, http.StatusCreated)
  	g = api.EngineGroup(g.ID)
  	if code, c := api.ErrorCode(http.MethodDelete, "/engine-groups/"+g.ID+"?revision="+itoa(g.Revision), nil); code != http.StatusConflict || c != "engine_group_not_empty" {
  		t.Fatalf("delete non-empty engine group: %d %s", code, c)
  	}
  	if code, c := api.ErrorCode(http.MethodPost, "/upstreams", map[string]any{"name": "nowhere", "protocol": "udp", "address": "192.0.2.53:53",
  		"timeout_ms": 250, "enabled": true, "position": 0, "engine_group_id": "11111111-1111-1111-1111-111111111111"}); code != http.StatusUnprocessableEntity || c != "engine_group_not_found" {
  		t.Fatalf("unknown engine group: %d %s", code, c)
  	}
  	var pg1 struct {
  		ID string `json:"id"`
  	}
  	api.Must(http.MethodPost, "/policy-groups", map[string]any{"name": "fleet-policy", "cidrs": []string{"10.9.0.0/16"}}, &pg1, http.StatusCreated)
  	if code, c := api.ErrorCode(http.MethodPost, "/rewrites", map[string]any{"group_id": pg1.ID, "engine_group_id": g.ID, "name": "p.fleet.test", "type": "A", "value": "192.0.2.8"}); code != http.StatusUnprocessableEntity || c != "engine_group_scope" {
  		t.Fatalf("policy group rewrite with its own engine group: %d %s", code, c)
  	}
  	after := api.WaitEngineGroupStable(g.ID, stable, 15*time.Second)

  	var created struct {
  		Token     string `json:"token"`
  		JoinToken struct {
  			ID string `json:"id"`
  		} `json:"join_token"`
  	}
  	api.Must(http.MethodPost, "/join-tokens", map[string]any{"name": "edge", "ttl_seconds": 600, "engine_group_id": g.ID, "max_uses": 2,
  		"labels": map[string]string{"site": "lab"}}, &created, http.StatusCreated)
  	if !regexp.MustCompile(`^nxj1\.[A-Z2-7]+\.[0-9a-f]{64}$`).MatchString(created.Token) {
  		t.Fatalf("join token format %q", created.Token)
  	}
  	var listed []map[string]any
  	api.Must(http.MethodGet, "/join-tokens", nil, &listed, http.StatusOK)
  	if len(listed) != 1 || listed[0]["state"] != "active" || listed[0]["engine_group_name"] != "edge-a" || listed[0]["max_uses"] != float64(2) {
  		t.Fatalf("listed join tokens %+v", listed)
  	}
  	if _, has := listed[0]["token"]; has {
  		t.Fatal("listJoinTokens must never return the token")
  	}
  	api.Must(http.MethodDelete, "/join-tokens/"+created.JoinToken.ID, nil, nil, http.StatusNoContent)
  	api.Must(http.MethodGet, "/join-tokens", nil, &listed, http.StatusOK)
  	if listed[0]["state"] != "revoked" {
  		t.Fatalf("revoked token listing %+v", listed)
  	}

  	if code, c := api.ErrorCode(http.MethodPost, "/engine-groups/"+g.ID+"/rollback", map[string]any{"to_version": after + 1000}); code != http.StatusNotFound || c != "version_not_found" {
  		t.Fatalf("rollback to an unknown version: %d %s", code, c)
  	}
  	if code, c := api.ErrorCode(http.MethodPost, "/engine-groups/"+g.ID+"/rollback", map[string]any{"to_version": api.LatestVersion()}); code != http.StatusBadRequest || c != "not_older" {
  		t.Fatalf("rollback to the newest version: %d %s", code, c)
  	}
  	var rb harness.RolloutView
  	api.Must(http.MethodPost, "/engine-groups/"+g.ID+"/rollback", map[string]any{"to_version": stable}, &rb, http.StatusAccepted)
  	if rb.Kind != "rollback" || rb.Version <= after || rb.FromVersion == nil || *rb.FromVersion != stable {
  		t.Fatalf("rollback rollout %+v", rb)
  	}
  	api.WaitRollout(g.ID, rb.Version, 15*time.Second, "completed")
  	if !api.EngineGroup(g.ID).RolloutsPaused {
  		t.Fatal("rollback must pause change rollouts")
  	}
  	var resumed harness.RolloutView
  	api.Must(http.MethodPost, "/engine-groups/"+g.ID+"/resume-rollouts", nil, &resumed, http.StatusAccepted)
  	if resumed.Kind != "change" || resumed.Version <= rb.Version {
  		t.Fatalf("resume rollout %+v", resumed)
  	}
  	if code, c := api.ErrorCode(http.MethodPost, "/engine-groups/"+g.ID+"/resume-rollouts", nil); code != http.StatusConflict || c != "not_paused" {
  		t.Fatalf("resume twice: %d %s", code, c)
  	}

  	var sum struct {
  		EnginesTotal int `json:"engines_total"`
  		EngineGroups []struct {
  			Name string `json:"name"`
  		} `json:"engine_groups"`
  	}
  	api.Must(http.MethodGet, "/fleet/summary", nil, &sum, http.StatusOK)
  	if sum.EnginesTotal != 0 || len(sum.EngineGroups) != 2 {
  		t.Fatalf("summary %+v", sum)
  	}
  	var detail map[string]any
  	api.Must(http.MethodGet, "/rollouts/"+rb.ID, nil, &detail, http.StatusOK)
  	if detail["state"] != "completed" || detail["engines"] == nil {
  		t.Fatalf("rollout detail %+v", detail)
  	}
  	if code, _ := api.ErrorCode(http.MethodPatch, "/engines/"+harness.DefaultEngineGroupID, map[string]any{"revision": 1}); code != http.StatusNotFound {
  		t.Fatalf("patch unknown engine: %d", code)
  	}
  }

  func itoa(v int64) string { return strconv.FormatInt(v, 10) }
  ```
  (import `strconv` alongside the packages listed).
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=$PWD/bin go test ./e2e/ -run TestFleetAPI -count=1 -v'` before the handlers exist and expect FAIL with `POST /engine-groups: status 404` (or `no such API route`); after the handlers, expect `--- PASS: TestFleetAPI`.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/... ./e2e/harness/ -count=1 && pnpm --dir web run lint && NEXORA_E2E_BIN_DIR=$PWD/bin go test ./e2e/ -run "TestAuthRBACAuditOIDC|TestMgmtStatelessHA|TestSafeSearchRewrites|TestRPZPolicy" -count=1 -timeout 20m'` and expect `ok` for every package, lint clean (permissions mirrored), and each test passing.
- [ ] Commit: `git add mgmt/api/openapi.yaml mgmt/internal/api mgmt/internal/fleet mgmt/internal/control/jointokens.go mgmt/internal/store mgmt/internal/zone mgmt/internal/auth web/src/api/schema.d.ts web/src/auth/permissions.ts e2e/harness e2e/fleet_api_test.go && git commit -m "feat(api): engine groups, rollouts, fleet summary and engine-group scoping"`.

## Task 8: Engine lifecycle in the management plane

Files: `mgmt/internal/fleet/jointokens.go` (join token consumption rules), `mgmt/internal/control/jointokens_test.go`, `mgmt/internal/fleet/certs.go` (certificate records, checks, revocation, rotation), `mgmt/internal/control/tls.go` (verified leaf certificate), `mgmt/internal/control/server.go` (`Enroll` with group and labels, `Authenticate`, renewal on the stream, `EngineCertTTL`), `mgmt/internal/control/lifecycle_test.go`, `mgmt/internal/querylog/builtin.go` (`Authenticate` hook), `mgmt/api/openapi.yaml` and regenerated `mgmt/internal/api/gen.go` / `web/src/api/schema.d.ts` (`revokeEngine`, `rotateEngineCertificate`), `mgmt/internal/api/fleet_engines.go` (revoke, rotate, delete revokes), `mgmt/internal/auth/permissions.go`, `mgmt/internal/auth/permissions_fleet_test.go`, `web/src/auth/permissions.ts`, `mgmt/internal/config/config.go` and `config_test.go` (`NEXORA_ENGINE_CERT_TTL`), `mgmt/internal/pki/pki.go` (`FingerprintFile`), `mgmt/cmd/nexora-mgmt/fleet_cli.go` (`engine-group create`, `join-token create`), `mgmt/cmd/nexora-mgmt/main.go` (usage, `ca init --if-missing`, wiring), `e2e/cli_fleet_test.go` (`TestMgmtCLIFleet`)
Interfaces:

```go
package fleet
var ErrJoinTokenUnknown, ErrJoinTokenExpired, ErrJoinTokenExhausted, ErrJoinTokenRevoked error // "join token unknown|expired|exhausted|revoked"
type JoinTokenGrant struct { ID, EngineGroupID uuid.UUID; Labels map[string]string }
func ConsumeJoinToken(ctx context.Context, tx pgx.Tx, secret string) (JoinTokenGrant, error)
var ErrUnknownEngine = errors.New("unknown or deleted engine")
var ErrCertificateRevoked = errors.New("certificate revoked")
var ErrEngineRevoked = errors.New("engine is revoked")
func RecordCertificate(ctx context.Context, q store.PolicyQuerier, engineID uuid.UUID, certDER []byte) error
func CheckCertificate(ctx context.Context, q store.PolicyQuerier, engineID uuid.UUID, serialHex string) error
func SupersedeOlderCertificates(ctx context.Context, q store.PolicyQuerier, engineID uuid.UUID, serialHex string) error
func RevokeEngine(ctx context.Context, tx pgx.Tx, engineID uuid.UUID) error
func RequestRotation(ctx context.Context, tx pgx.Tx, engineID uuid.UUID) error

package control
func PeerCertificate(ctx context.Context) (*x509.Certificate, error)
func (s *Server) Authenticate(ctx context.Context) (string, error)
// Server gains: EngineCertTTL time.Duration (0 = pki.EngineCertValidity)

package pki
func FingerprintFile(certFile string) (string, error)
```

operationIds `revokeEngine` (admin), `rotateEngineCertificate` (admin).

> As built (deviations, 2026-09-14): `NEXORA_ROLLOUT_TICK` was already implemented by Task 6. `Connect` supersedes older serials only after the Hello is validated, and checks the certificate again right after registering its subscriber (a revocation notified between the first check and the registration would otherwise reach no stream); the hub's full resync (on LISTEN reconnect and every 30 s) also ends the streams of engines that are revoked or deleted, in case an instance missed `nexora_engine_revoked`. Renewal issuance records the certificate in a transaction that locks the engine row and refuses (ending the stream with `PermissionDenied certificate revoked`) a revoked or deleted engine; the 10 s limit is per stream and counts only issued certificates. `fleet.Target` gains `Revoked`. `control.InsertJoinToken(ctx, tx, caFingerprint, spec)` is the fingerprint-only form of `CreateJoinTokenFor` used by the CLI. The CLI `join-token create` is an audited transaction without a config version (a join token is not configuration), and `engine-group create` publishes with a `snapshot.BuildConfig` read from `NEXORA_QUERYLOG_BACKEND` and `NEXORA_OTLP_ENDPOINT` like `serve`. `deleteEngine` and the join token handlers keep M1's transaction kinds (`deleteEngine` now audited without a version). Extra test: `mgmt/internal/api/fleet_lifecycle_test.go` `TestEngineLifecycleAuditAndHashedJoinTokens` (secret stored only as its SHA-256, revocation revokes every certificate and notifies, 409 `engine_revoked` on revoked engines, every lifecycle action audited).

- [ ] Write the failing test `mgmt/internal/control/jointokens_test.go`:
  ```go
  package control_test

  import (
  	"context"
  	"errors"
  	"path/filepath"
  	"testing"
  	"time"

  	"github.com/google/uuid"
  	"github.com/jackc/pgx/v5"

  	"github.com/piwi3910/nexora/mgmt/internal/control"
  	"github.com/piwi3910/nexora/mgmt/internal/fleet"
  	"github.com/piwi3910/nexora/mgmt/internal/pki"
  	"github.com/piwi3910/nexora/mgmt/internal/store"
  	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
  )

  func TestConsumeJoinToken(t *testing.T) {
  	ctx := context.Background()
  	st := storetest.New(t)
  	dir := t.TempDir()
  	if err := pki.InitCA(dir); err != nil {
  		t.Fatal(err)
  	}
  	ca, err := pki.LoadCA(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key"))
  	if err != nil {
  		t.Fatal(err)
  	}
  	var edge uuid.UUID
  	if err := st.Pool.QueryRow(ctx, `insert into engine_groups (name) values ('edge') returning id`).Scan(&edge); err != nil {
  		t.Fatal(err)
  	}
  	two := 2
  	create := func(spec control.JoinTokenSpec) (string, string) {
  		var id, secret string
  		err := st.InTx(ctx, func(tx pgx.Tx) error {
  			jt, token, err := control.CreateJoinTokenFor(ctx, tx, ca, spec)
  			id = jt.ID
  			secret, _, _ = pki.ParseJoinToken(token)
  			return err
  		})
  		if err != nil {
  			t.Fatal(err)
  		}
  		return id, secret
  	}
  	consume := func(secret string) (fleet.JoinTokenGrant, error) {
  		var g fleet.JoinTokenGrant
  		err := st.InTx(ctx, func(tx pgx.Tx) error {
  			var err error
  			g, err = fleet.ConsumeJoinToken(ctx, tx, secret)
  			return err
  		})
  		return g, err
  	}

  	_, secret := create(control.JoinTokenSpec{Name: "edge", CreatedBy: "t", TTL: time.Hour, EngineGroupID: edge, MaxUses: &two, Labels: map[string]string{"site": "lab"}})
  	for i := 0; i < 2; i++ {
  		g, err := consume(secret)
  		if err != nil || g.EngineGroupID != edge || g.Labels["site"] != "lab" {
  			t.Fatalf("use %d: %+v %v", i+1, g, err)
  		}
  	}
  	if _, err := consume(secret); !errors.Is(err, fleet.ErrJoinTokenExhausted) || err.Error() != "join token exhausted" {
  		t.Fatalf("third use: %v", err)
  	}

  	_, unlimited := create(control.JoinTokenSpec{Name: "fleet", CreatedBy: "t", TTL: time.Hour, EngineGroupID: store.DefaultEngineGroupID})
  	for i := 0; i < 5; i++ {
  		if _, err := consume(unlimited); err != nil {
  			t.Fatalf("unlimited token use %d: %v", i+1, err)
  		}
  	}
  	expID, expired := create(control.JoinTokenSpec{Name: "old", CreatedBy: "t", TTL: time.Hour, EngineGroupID: edge})
  	if _, err := st.Pool.Exec(ctx, `update join_tokens set expires_at = now() - interval '1 second' where id = $1`, expID); err != nil {
  		t.Fatal(err)
  	}
  	if _, err := consume(expired); !errors.Is(err, fleet.ErrJoinTokenExpired) || err.Error() != "join token expired" {
  		t.Fatalf("expired: %v", err)
  	}
  	revID, revoked := create(control.JoinTokenSpec{Name: "revoked", CreatedBy: "t", TTL: time.Hour, EngineGroupID: edge})
  	if _, err := st.Pool.Exec(ctx, `update join_tokens set revoked_at = now() where id = $1`, revID); err != nil {
  		t.Fatal(err)
  	}
  	if _, err := consume(revoked); !errors.Is(err, fleet.ErrJoinTokenRevoked) {
  		t.Fatalf("revoked: %v", err)
  	}
  	if _, err := consume("AAAAAAAA"); !errors.Is(err, fleet.ErrJoinTokenUnknown) || err.Error() != "join token unknown" {
  		t.Fatalf("unknown: %v", err)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/control/ -run TestConsumeJoinToken -count=1` and expect FAIL with `undefined: fleet.ConsumeJoinToken`.
- [ ] Create `mgmt/internal/fleet/jointokens.go`: the four errors are `errors.New` with exactly the messages above; `ConsumeJoinToken` runs
  ```sql
  update join_tokens set uses = uses + 1
  where secret_hash = $1 and revoked_at is null and expires_at > now() and (max_uses is null or uses < max_uses)
  returning id, engine_group_id, labels;
  ```
  with `pki.HashSecret(secret)`; when no row returns it classifies with `select revoked_at is not null, expires_at <= now(), max_uses is not null and uses >= max_uses from join_tokens where secret_hash = $1` in the order revoked, expired, exhausted; no row -> `ErrJoinTokenUnknown`. Remove `lookupJoinToken` from `mgmt/internal/control/jointokens.go`. Run the test and expect `ok`.
- [ ] Write the failing test `mgmt/internal/control/lifecycle_test.go`:
  ```go
  package control_test

  import (
  	"crypto/ecdsa"
  	"crypto/elliptic"
  	"crypto/rand"
  	"crypto/tls"
  	"crypto/x509"
  	"crypto/x509/pkix"
  	"testing"
  	"time"

  	"github.com/google/uuid"
  	"github.com/jackc/pgx/v5"
  	"google.golang.org/grpc"
  	"google.golang.org/grpc/codes"
  	"google.golang.org/grpc/credentials"
  	"google.golang.org/grpc/status"

  	"github.com/piwi3910/nexora/e2e/harness"
  	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
  	"github.com/piwi3910/nexora/mgmt/internal/control"
  	"github.com/piwi3910/nexora/mgmt/internal/fleet"
  )

  func recvMsg(t *testing.T, s controlv1.EngineControl_ConnectClient, within time.Duration) (*controlv1.ServerMessage, error) {
  	t.Helper()
  	type res struct {
  		m   *controlv1.ServerMessage
  		err error
  	}
  	ch := make(chan res, 1)
  	go func() { m, err := s.Recv(); ch <- res{m, err} }()
  	select {
  	case r := <-ch:
  		return r.m, r.err
  	case <-time.After(within):
  		t.Fatalf("no server message within %s", within)
  	}
  	return nil, nil
  }

  func csrFor(t *testing.T, cn string) ([]byte, *ecdsa.PrivateKey) {
  	t.Helper()
  	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
  	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
  	if err != nil {
  		t.Fatal(err)
  	}
  	return der, key
  }

  func blobCode(c controlv1.EngineControlClient, f *fixture) codes.Code {
  	_, err := mustRecvErr(c, f.ctx)
  	return status.Code(err)
  }

  func TestCertificateRenewalRotationAndRevocation(t *testing.T) {
  	f := setupServers(t, 1, nil, func(s *control.Server) { s.EngineCertTTL = time.Hour })
  	oldClient, id := f.enroll(t, f.addr[0])
  	var oldSerial string
  	if err := f.st.Pool.QueryRow(f.ctx, `select certificate_serial from engines where id = $1`, id).Scan(&oldSerial); err != nil {
  		t.Fatal(err)
  	}
  	if c := blobCode(oldClient, f); c != codes.NotFound {
  		t.Fatalf("enrolled engine GetBlob -> %v, want NotFound (authenticated)", c)
  	}
  	stream, _ := oldClient.Connect(f.ctx)
  	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, NodeName: "life-1"}}})
  	if m, err := recvMsg(t, stream, 3*time.Second); err != nil || m.GetSnapshot() == nil {
  		t.Fatalf("initial snapshot: %v %v", m, err)
  	}

  	if err := f.st.InTx(f.ctx, func(tx pgx.Tx) error { return fleet.RequestRotation(f.ctx, tx, uuid.MustParse(id)) }); err != nil {
  		t.Fatal(err)
  	}
  	if m, err := recvMsg(t, stream, 3*time.Second); err != nil || m.GetRenewCertificate().GetReason() != controlv1.CertificateRequest_REASON_ROTATE {
  		t.Fatalf("rotation request: %v %v", m, err)
  	}
  	foreign, _ := csrFor(t, uuid.NewString())
  	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_CertRequest{CertRequest: &controlv1.CertificateRequest{CsrDer: foreign, Reason: controlv1.CertificateRequest_REASON_ROTATE}}})
  	csr, key := csrFor(t, id)
  	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_CertRequest{CertRequest: &controlv1.CertificateRequest{CsrDer: csr, Reason: controlv1.CertificateRequest_REASON_ROTATE}}})
  	m, err := recvMsg(t, stream, 3*time.Second)
  	if err != nil || m.GetCertIssued() == nil {
  		t.Fatalf("certificate issued: %v %v (a CSR with a foreign CN must get no answer)", m, err)
  	}
  	cert, err := x509.ParseCertificate(m.GetCertIssued().CertDer)
  	if err != nil || cert.Subject.CommonName != id || cert.SerialNumber.Text(16) == oldSerial || time.Until(cert.NotAfter) > time.Hour+time.Minute {
  		t.Fatalf("issued certificate %v err %v", cert, err)
  	}
  	harness.Eventually(t, 3*time.Second, func() error { return expectEngine(f, id, "cert_rotate_requested_at is null", true) })

  	conn, _ := grpc.NewClient(f.addr[0], grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: f.ca.Pool(), ServerName: "127.0.0.1",
  		Certificates: []tls.Certificate{{Certificate: [][]byte{m.GetCertIssued().CertDer}, PrivateKey: key}}})))
  	t.Cleanup(func() { conn.Close() })
  	newClient := controlv1.NewEngineControlClient(conn)
  	renewed, _ := newClient.Connect(f.ctx)
  	_ = renewed.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, NodeName: "life-1", AppliedVersion: 1}}})
  	harness.Eventually(t, 3*time.Second, func() error {
  		return expectRow(f, "select revoke_reason from engine_certificates where serial = '"+oldSerial+"'", "superseded")
  	})
  	if c := blobCode(oldClient, f); c != codes.PermissionDenied {
  		t.Fatalf("superseded certificate GetBlob -> %v, want PermissionDenied", c)
  	}
  	if c := blobCode(newClient, f); c != codes.NotFound {
  		t.Fatalf("renewed certificate GetBlob -> %v, want NotFound", c)
  	}

  	if err := f.st.InTx(f.ctx, func(tx pgx.Tx) error { return fleet.RevokeEngine(f.ctx, tx, uuid.MustParse(id)) }); err != nil {
  		t.Fatal(err)
  	}
  	for {
  		_, err := recvMsg(t, renewed, 3*time.Second)
  		if err == nil {
  			continue
  		}
  		if s, _ := status.FromError(err); s.Code() != codes.PermissionDenied || s.Message() != "certificate revoked" {
  			t.Fatalf("stream end after revocation: %v", err)
  		}
  		break
  	}
  	if c := blobCode(newClient, f); c != codes.PermissionDenied {
  		t.Fatalf("revoked engine GetBlob -> %v, want PermissionDenied", c)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/control/ -run TestCertificateRenewalRotationAndRevocation -count=1` and expect FAIL with `s.EngineCertTTL undefined`.
- [ ] Create `mgmt/internal/fleet/certs.go`:
  - `RecordCertificate`: parse `certDER`; `insert into engine_certificates (serial, engine_id, not_before, not_after) values (lower($1), $2, $3, $4) on conflict (serial) do nothing`; `update engines set certificate_serial = lower($1) where id = $2`.
  - `CheckCertificate`: `select e.deleted_at is not null, e.revoked_at is not null, c.engine_id, c.revoked_at is not null from engines e left join engine_certificates c on c.serial = lower($2) where e.id = $1`; no row or deleted -> `ErrUnknownEngine`; engine revoked, no certificate row, certificate revoked, or `c.engine_id <> e.id` -> `ErrCertificateRevoked`.
  - `SupersedeOlderCertificates`: `update engine_certificates set revoked_at = now(), revoke_reason = 'superseded' where engine_id = $1 and revoked_at is null and issued_at < (select issued_at from engine_certificates where serial = lower($2))`.
  - `RevokeEngine`: `update engines set revoked_at = coalesce(revoked_at, now()), revision = revision + 1 where id = $1 and deleted_at is null` (no row -> `store.ErrNotFound`); `update engine_certificates set revoked_at = now(), revoke_reason = 'revoked' where engine_id = $1 and revoked_at is null`; `select pg_notify('nexora_engine_revoked', $1::text)`.
  - `RequestRotation`: revoked engine -> `ErrEngineRevoked`; `update engines set cert_rotate_requested_at = now() where id = $1`; `select pg_notify('nexora_engine_rotate', $1::text)`.
- [ ] Change `mgmt/internal/control`:
  - `tls.go`: `PeerCertificate(ctx)` returns `VerifiedChains[0][0]` (or `Unauthenticated` `client certificate required`); `EngineID` uses it.
  - `server.go`: rename `engine(ctx)` to exported `Authenticate(ctx)`: `PeerCertificate`, UUID check as today, then `fleet.CheckCertificate(ctx, s.st.Pool, id, cert.SerialNumber.Text(16))` mapped to `status.Error(codes.PermissionDenied, "unknown or deleted engine")` / `status.Error(codes.PermissionDenied, "certificate revoked")`; `Connect` and `GetBlob` call it; `Connect` then runs `fleet.SupersedeOlderCertificates` for the presented serial.
  - `Enroll`: replace `lookupJoinToken` with `fleet.ConsumeJoinToken` (errors -> `status.Error(codes.PermissionDenied, err.Error())`); insert the engine with `engine_group_id` and `labels` from the grant (drop the separate `uses + 1` update); sign with `s.certTTL()` (`EngineCertTTL`, or `pki.EngineCertValidity` when zero); `fleet.RecordCertificate` in the same transaction.
  - `receive`: `case *controlv1.EngineMessage_CertRequest: s.renew(ctx, sub, m.CertRequest)`: at most one issuance per engine per 10 s (`subscriber.lastIssued`; extra requests logged `certificate request ignored: rate limited`); `x509.ParseCertificateRequest` and subject CN equal to the engine id, else log `certificate request refused: <reason>` and answer nothing; `s.ca.SignEngineCSR(csr, sub.engineID, s.certTTL())`; `fleet.RecordCertificate`; `update engines set cert_rotate_requested_at = null where id = $1`; queue `CertificateIssued{CertDer: der, CaDer: s.ca.Cert.Raw}` on `sub.control`.
- [ ] Change `mgmt/internal/querylog/builtin.go`: `Builtin` gains `Authenticate func(context.Context) (string, error)`; `Export` uses it when set, else `control.EngineID`. In `serve` of `mgmt/cmd/nexora-mgmt/main.go` set `builtinLog.Authenticate = controlServer.Authenticate` and `controlServer.EngineCertTTL = cfg.EngineCertTTL`.
- [ ] Add `EngineCertTTL time.Duration` to `mgmt/internal/config/config.go` from `NEXORA_ENGINE_CERT_TTL` (default `2160h`; below `30s` or unparsable -> `NEXORA_ENGINE_CERT_TTL must be a duration of at least 30s`) with a `config_test.go` case for `10s`.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/control/ ./mgmt/internal/querylog/ ./mgmt/internal/config/ -count=1` and expect `ok` (including every M1–M4 control test: M1 certificates were backfilled into `engine_certificates`, and new enrollments record theirs).
- [ ] Add to `mgmt/api/openapi.yaml`:
  ```yaml
  /engines/{id}/revoke:
    post:
      operationId: revokeEngine
      parameters: [{ $ref: "#/components/parameters/Id" }]
      responses:
        "200":
          {
            description: revoked; its streams are closed,
            content:
              {
                application/json:
                  { schema: { $ref: "#/components/schemas/Engine" } },
              },
          }
        "404": { $ref: "#/components/responses/Error" }
        "409": { $ref: "#/components/responses/Error" }
  /engines/{id}/rotate-certificate:
    post:
      operationId: rotateEngineCertificate
      parameters: [{ $ref: "#/components/parameters/Id" }]
      responses:
        "202":
          {
            description: rotation requested,
            content:
              {
                application/json:
                  { schema: { $ref: "#/components/schemas/Engine" } },
              },
          }
        "404": { $ref: "#/components/responses/Error" }
        "409": { $ref: "#/components/responses/Error" }
  ```
  Handlers in `mgmt/internal/api/fleet_engines.go` (both through `h.audited`): `revokeEngine` -> already revoked `coded(409, "engine_revoked", ...)`, else `fleet.RevokeEngine`, 200; `rotateEngineCertificate` -> `fleet.ErrEngineRevoked` maps to 409 `engine_revoked`, else 202. `deleteEngine` now runs `fleet.RevokeEngine` before setting `deleted_at`. Add `"revokeEngine": RoleAdmin, "rotateEngineCertificate": RoleAdmin` to `Permissions`, to `web/src/auth/permissions.ts` and to the `want` map of `permissions_fleet_test.go`. On the laptop run `make proto`.
- [ ] Add `func FingerprintFile(certFile string) (string, error)` to `mgmt/internal/pki/pki.go` (PEM certificate -> lowercase hex SHA-256 of its DER, like `(*CA).Fingerprint`).
- [ ] Write the failing black-box CLI test `e2e/cli_fleet_test.go`:
  ```go
  package e2e

  import (
  	"bytes"
  	"crypto/sha256"
  	"net/http"
  	"os"
  	"os/exec"
  	"regexp"
  	"strings"
  	"testing"
  	"time"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  func TestMgmtCLIFleet(t *testing.T) {
  	env := harness.New(t)
  	pg := env.StartPostgres()
  	ca := env.InitCA()
  	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{})
  	api := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
  	run := func(args ...string) (string, string, error) {
  		cmd := exec.Command(env.Bin("nexora-mgmt"), args...) // nosemgrep: dangerous-exec-command
  		cmd.Env = append(os.Environ(), "NEXORA_DATABASE_URL="+pg.URL, "NEXORA_CA_CERT_FILE="+ca.CertFile)
  		var out, errb bytes.Buffer
  		cmd.Stdout, cmd.Stderr = &out, &errb
  		err := cmd.Run()
  		return strings.TrimSpace(out.String()), errb.String(), err
  	}

  	gid, stderr, err := run("engine-group", "create", "--name", "edge-cli", "--description", "from cli")
  	if err != nil || !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(gid) {
  		t.Fatalf("engine-group create: %q %q %v", gid, stderr, err)
  	}
  	if g := api.EngineGroup(gid); g.Name != "edge-cli" || g.Description != "from cli" {
  		t.Fatalf("engine group via API: %+v", g)
  	}
  	if again, _, err := run("engine-group", "create", "--name", "edge-cli", "--if-missing"); err != nil || again != gid {
  		t.Fatalf("engine-group create --if-missing: %q %v, want %s", again, err, gid)
  	}
  	if _, stderr, err := run("engine-group", "create", "--name", "edge-cli"); err == nil || !strings.Contains(stderr, `engine group "edge-cli" already exists`) {
  		t.Fatalf("duplicate engine-group create: %q %v", stderr, err)
  	}

  	token, stderr, err := run("join-token", "create", "--engine-group", "edge-cli", "--ttl", "10m", "--max-uses", "3", "--label", "site=lab")
  	if err != nil || !regexp.MustCompile(`^nxj1\.[A-Z2-7]+\.[0-9a-f]{64}$`).MatchString(token) {
  		t.Fatalf("join-token create: %q %q %v", token, stderr, err)
  	}
  	if _, stderr, err := run("join-token", "create", "--engine-group", "missing"); err == nil || !strings.Contains(stderr, `engine group "missing" not found`) {
  		t.Fatalf("unknown engine group: %q %v", stderr, err)
  	}
  	env.StartManagedEngine("cli-1", []string{mg.GRPCURL}, token)
  	e := api.WaitEngine("cli-1", 15*time.Second, func(v harness.EngineView) bool { return v.Connected })
  	if e.EngineGroupID != gid || e.Labels["site"] != "lab" {
  		t.Fatalf("engine enrolled with the CLI token: %+v", e)
  	}
  	var listed []map[string]any
  	api.Must(http.MethodGet, "/join-tokens", nil, &listed, http.StatusOK)
  	if len(listed) != 1 || listed[0]["max_uses"] != float64(3) || listed[0]["uses"] != float64(1) {
  		t.Fatalf("listed join tokens %+v", listed)
  	}

  	before, _ := os.ReadFile(ca.CertFile)
  	if out, stderr, err := run("ca", "init", "--out", ca.Dir, "--if-missing"); err != nil || !strings.HasPrefix(out, "ca fingerprint: ") {
  		t.Fatalf("ca init --if-missing on an existing CA: %q %q %v", out, stderr, err)
  	}
  	after, _ := os.ReadFile(ca.CertFile)
  	if sha256.Sum256(before) != sha256.Sum256(after) {
  		t.Fatal("ca init --if-missing replaced an existing CA")
  	}
  	if _, stderr, err := run("ca", "init", "--out", ca.Dir); err == nil || !strings.Contains(stderr, "refusing to overwrite") {
  		t.Fatalf("ca init over an existing CA: %q %v", stderr, err)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=$PWD/bin go test ./e2e/ -run TestMgmtCLIFleet -count=1 -v'` and expect FAIL with `engine-group create:` and the usage line.
- [ ] Implement `mgmt/cmd/nexora-mgmt/fleet_cli.go` with the `flag.NewFlagSet` style of `userCreate`, both on `openMigrated`:
  - `engine-group create --name N [--description D] [--if-missing]`: an existing name prints its id and exits 0 with `--if-missing`, otherwise `engine group "N" already exists` (exit 1); a new group is created inside `snapshot.Mutate` with actor `auth.Actor{Type: "system", ID: "cli", Name: "cli"}` and audit action `createEngineGroup`; prints the id.
  - `join-token create --engine-group NAME [--name LABEL] [--ttl 24h] [--max-uses N] [--label k=v]...`: the CA fingerprint comes from `pki.FingerprintFile(os.Getenv("NEXORA_CA_CERT_FILE"))`; unknown group -> `engine group "NAME" not found` (exit 1); invalid labels -> the `fleet.ValidateLabels` message; creates the token with `pki.NewJoinToken` plus the same insert as `control.CreateJoinTokenFor` inside `snapshot.Mutate` (audit `createJoinToken`); `--max-uses 0` (default) means unlimited; prints only the token.
  - `ca init --out DIR [--if-missing]` in `main.go`: with `--if-missing`, when both `ca.crt` and `ca.key` exist, load them and print `ca fingerprint: <hex>` without writing; exactly one present -> error `ca.crt and ca.key must both exist or both be absent`.
  - Extend `usage` with `engine-group create --name N [--description D] [--if-missing] | join-token create --engine-group G [--ttl 24h] [--max-uses N] [--label k=v]` and dispatch `engine-group create` and `join-token create` in `run`.
    Run the test and expect `--- PASS: TestMgmtCLIFleet`.
- [ ] Commit: `git add mgmt/internal/fleet mgmt/internal/control mgmt/internal/querylog mgmt/api/openapi.yaml mgmt/internal/api mgmt/internal/auth mgmt/internal/config mgmt/internal/pki mgmt/cmd/nexora-mgmt web/src/api/schema.d.ts web/src/auth/permissions.ts e2e/cli_fleet_test.go && git commit -m "feat(mgmt): join token rules, certificate renewal, rotation and revocation"`.

## Task 9: Engine side of renewal, rotation, revocation and node naming

Files: `engine/src/cert_renewal.rs` (renewal timing, CSR, verification of the issued certificate, staged identity, atomic swap, recovery), `engine/src/lib.rs` (module declaration), `engine/src/control.rs` (stream handling of the new messages, identity reload per session, staged-identity confirmation, revoked backoff), `engine/src/telemetry/metrics.rs` (`nexora_control_revoked`, `nexora_control_cert_renewals_total`), `engine/tests/telemetry_export.rs` (both names in the scrape), `engine/src/bootstrap.rs` (`NEXORA_ENGINE_NODE_NAME`), `engine/tests/control_renewal.rs` (the control loop against a fake management plane)
Interfaces:

```rust
// engine/src/cert_renewal.rs
pub fn renewal_due(not_before: SystemTime, not_after: SystemTime, now: SystemTime) -> bool;
pub fn cert_validity(cert_pem: &str) -> Option<(SystemTime, SystemTime)>;
pub fn new_csr(engine_id: &str) -> Result<(Vec<u8> /* csr der */, rcgen::KeyPair), rcgen::Error>;
pub fn cert_serial(cert_pem: &str) -> String; // lowercase hex, mgmt notation
pub fn verify_issued(engine_id: &str, ca_pem: &str, key: &rcgen::KeyPair, issued: &CertificateIssued, now: SystemTime) -> Result<String /* cert pem */, String>;
pub fn staged_dir(state_dir: &Path) -> PathBuf; // state_dir/identity.new
pub fn stage_identity(state_dir: &Path, cert_pem: &[u8], key_pem: &[u8]) -> std::io::Result<()>;
pub fn promote_identity(state_dir: &Path) -> std::io::Result<()>;
pub fn discard_staged(state_dir: &Path) -> std::io::Result<()>;
pub fn swap_identity(state_dir: &Path, cert_pem: &[u8], key_pem: &[u8]) -> std::io::Result<()>; // stage + promote
pub fn recover_identity(state_dir: &Path) -> std::io::Result<()>;
// engine/src/control.rs (ControlError gains `Renewed`)
pub fn reconnect_delay(status: Option<&tonic::Status>, attempt: u32, jitter: f64 /* 0.0..=1.0 */) -> Duration;
// engine/src/bootstrap.rs
pub fn apply_env_overrides(b: &mut Bootstrap, get: impl Fn(&str) -> Option<String>) -> anyhow::Result<()>;
// engine/src/telemetry/metrics.rs: Metrics gains `pub control_revoked: AtomicBool`, `pub cert_renewals: AtomicU64`
```

> As built (deviation, 2026-09-14): rcgen 0.14 has no `KeyPair::public_key_der`; the SPKI comes from `rcgen::PublicKeyData::subject_public_key_info`. To keep the old identity until the new certificate is confirmed working, `CertIssued` stages the identity (`identity.new.tmp` -> `identity.new`, so a present `identity.new` is always complete) and `run` tries the staged identity first; it is promoted (`identity` -> `identity.old`, `identity.new` -> `identity`, old removed) right after the management plane accepts `Connect` with it, which is also when the plane supersedes the old serial. A staged identity refused with `PermissionDenied`/`Unauthenticated` is discarded and the current identity retried at once. `recover_identity` keeps a complete `identity.new` next to an intact `identity` (unconfirmed renewal). `verify_issued` additionally verifies the certificate as a client certificate under the pinned CA with webpki. `cert_renewals` counts promotions. Extra tests: `issued_certificate_must_match_pinned_ca_key_and_engine_id`, `staged_identity_survives_recovery_until_promoted_or_discarded`, and `engine/tests/control_renewal.rs` `renews_rotates_and_backs_off_when_revoked` (renewal, rotation, refused staged fallback, revoked backoff against a fake mTLS control server).

- [x] Write the failing unit tests at the bottom of a new `engine/src/cert_renewal.rs` (declare `pub mod cert_renewal;` in `engine/src/lib.rs`):
  ```rust
  #[cfg(test)]
  mod tests {
      use super::*;
      use std::fs;
      use std::time::{Duration, UNIX_EPOCH};
      use x509_parser::prelude::FromDer;

      #[test]
      fn renewal_due_from_two_thirds_of_lifetime() {
          let nb = UNIX_EPOCH + Duration::from_secs(1_000);
          let na = nb + Duration::from_secs(90);
          assert!(!renewal_due(nb, na, nb + Duration::from_secs(59)));
          assert!(renewal_due(nb, na, nb + Duration::from_secs(60)));
          assert!(renewal_due(nb, na, na + Duration::from_secs(1)));
          assert!(renewal_due(na, nb, nb), "inverted validity must renew");
      }

      #[test]
      fn csr_carries_engine_id_and_p256_key() {
          let id = "0b7c1f5e-8f4f-4d47-9a55-3f4f0f6d2c11";
          let (der, key) = new_csr(id).unwrap();
          let (_, csr) = x509_parser::certification_request::X509CertificationRequest::from_der(&der).unwrap();
          let cn = csr.certification_request_info.subject.iter_common_name().next().unwrap();
          assert_eq!(cn.as_str().unwrap(), id);
          csr.verify_signature().unwrap();
          assert_eq!(csr.certification_request_info.subject_pki.raw, key.subject_public_key_info().as_slice());
      }

      fn identity(dir: &std::path::Path, cert: &[u8], key: &[u8]) {
          let id = dir.join("identity");
          fs::create_dir_all(&id).unwrap();
          fs::write(id.join("cert.pem"), cert).unwrap();
          fs::write(id.join("key.pem"), key).unwrap();
          fs::write(id.join("ca.pem"), b"ca").unwrap();
          fs::write(id.join("engine_id"), b"engine-uuid").unwrap();
      }

      #[test]
      fn swap_identity_replaces_pair_and_keeps_the_rest() {
          let dir = tempfile::tempdir().unwrap();
          identity(dir.path(), b"old-cert", b"old-key");
          swap_identity(dir.path(), b"new-cert", b"new-key").unwrap();
          let id = dir.path().join("identity");
          assert_eq!(fs::read(id.join("cert.pem")).unwrap(), b"new-cert");
          assert_eq!(fs::read(id.join("key.pem")).unwrap(), b"new-key");
          assert_eq!(fs::read(id.join("ca.pem")).unwrap(), b"ca");
          assert_eq!(fs::read(id.join("engine_id")).unwrap(), b"engine-uuid");
          assert!(!dir.path().join("identity.new").exists());
          assert!(!dir.path().join("identity.old").exists());
          use std::os::unix::fs::PermissionsExt;
          assert_eq!(fs::metadata(id.join("key.pem")).unwrap().permissions().mode() & 0o777, 0o600);
      }

      #[test]
      fn recover_identity_after_crash_between_renames() {
          let dir = tempfile::tempdir().unwrap();
          identity(dir.path(), b"old-cert", b"old-key");
          // crash after `identity` -> `identity.old`, before `identity.new` -> `identity`
          fs::rename(dir.path().join("identity"), dir.path().join("identity.old")).unwrap();
          let new = dir.path().join("identity.new");
          fs::create_dir_all(&new).unwrap();
          for (name, data) in [("cert.pem", &b"new-cert"[..]), ("key.pem", b"new-key"), ("ca.pem", b"ca"), ("engine_id", b"engine-uuid")] {
              fs::write(new.join(name), data).unwrap();
          }
          recover_identity(dir.path()).unwrap();
          assert_eq!(fs::read(dir.path().join("identity/cert.pem")).unwrap(), b"new-cert");
          assert!(!dir.path().join("identity.old").exists());

          // crash while staging: `identity` intact, half-written `identity.new`
          fs::create_dir_all(dir.path().join("identity.new")).unwrap();
          recover_identity(dir.path()).unwrap();
          assert_eq!(fs::read(dir.path().join("identity/cert.pem")).unwrap(), b"new-cert");
          assert!(!dir.path().join("identity.new").exists());
      }
  }
  ```
- [x] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine cert_renewal` and expect FAIL with ``cannot find function `renewal_due` in this scope``.
- [x] Implement `engine/src/cert_renewal.rs` above the tests (`rcgen` with its `x509-parser` feature and `x509-parser` are already dependencies; `tempfile` is a dev-dependency):
  ```rust
  //! Engine certificate renewal: timing, CSR generation and the atomic swap of
  //! `state_dir/identity`. Runs on the control runtime only.
  use std::fs::{self, File, OpenOptions};
  use std::io::{self, Write};
  use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
  use std::path::Path;
  use std::time::{Duration, SystemTime, UNIX_EPOCH};

  /// True from 2/3 of the certificate lifetime onwards (and for inverted validity).
  pub fn renewal_due(not_before: SystemTime, not_after: SystemTime, now: SystemTime) -> bool {
      let Ok(lifetime) = not_after.duration_since(not_before) else {
          return true;
      };
      now >= not_before + lifetime * 2 / 3
  }

  /// NotBefore and NotAfter of the first certificate in `cert_pem`.
  pub fn cert_validity(cert_pem: &str) -> Option<(SystemTime, SystemTime)> {
      let (_, pem) = x509_parser::pem::parse_x509_pem(cert_pem.as_bytes()).ok()?;
      let cert = pem.parse_x509().ok()?;
      let at = |t: i64| UNIX_EPOCH + Duration::from_secs(t.max(0) as u64);
      Some((at(cert.validity().not_before.timestamp()), at(cert.validity().not_after.timestamp())))
  }

  /// A fresh ECDSA P-256 key and a CSR whose subject CN is the engine id.
  pub fn new_csr(engine_id: &str) -> Result<(Vec<u8>, rcgen::KeyPair), rcgen::Error> {
      let key = rcgen::KeyPair::generate_for(&rcgen::PKCS_ECDSA_P256_SHA256)?;
      let mut params = rcgen::CertificateParams::default();
      params.distinguished_name = rcgen::DistinguishedName::new();
      params.distinguished_name.push(rcgen::DnType::CommonName, engine_id);
      let csr = params.serialize_request(&key)?;
      Ok((csr.der().to_vec(), key))
  }

  fn write_synced(path: &Path, bytes: &[u8]) -> io::Result<()> {
      let mut f = OpenOptions::new().write(true).create_new(true).mode(0o600).open(path)?;
      f.write_all(bytes)?;
      f.sync_all()
  }

  /// Stage the new pair with copies of the other identity files in `identity.new`, then rename
  /// `identity` -> `identity.old` and `identity.new` -> `identity`.
  pub fn swap_identity(state_dir: &Path, cert_pem: &[u8], key_pem: &[u8]) -> io::Result<()> {
      let (cur, new, old) = (state_dir.join("identity"), state_dir.join("identity.new"), state_dir.join("identity.old"));
      if new.exists() {
          fs::remove_dir_all(&new)?;
      }
      fs::DirBuilder::new().mode(0o700).create(&new)?;
      for entry in fs::read_dir(&cur)? {
          let entry = entry?;
          let name = entry.file_name();
          if name != "cert.pem" && name != "key.pem" && !name.to_string_lossy().ends_with(".tmp") {
              write_synced(&new.join(&name), &fs::read(entry.path())?)?;
          }
      }
      write_synced(&new.join("cert.pem"), cert_pem)?;
      write_synced(&new.join("key.pem"), key_pem)?;
      File::open(&new)?.sync_all()?;
      if old.exists() {
          fs::remove_dir_all(&old)?;
      }
      fs::rename(&cur, &old)?;
      fs::rename(&new, &cur)?;
      File::open(state_dir)?.sync_all()?;
      fs::remove_dir_all(&old)
  }

  /// Finish or discard an interrupted swap. Call before loading the identity.
  pub fn recover_identity(state_dir: &Path) -> io::Result<()> {
      let (cur, new, old) = (state_dir.join("identity"), state_dir.join("identity.new"), state_dir.join("identity.old"));
      if !cur.exists() {
          if ["cert.pem", "key.pem", "ca.pem", "engine_id"].iter().all(|f| new.join(f).exists()) {
              fs::rename(&new, &cur)?;
          } else if old.exists() {
              fs::rename(&old, &cur)?;
          }
          if state_dir.exists() {
              File::open(state_dir)?.sync_all()?;
          }
      }
      for leftover in [new, old] {
          if leftover.exists() {
              fs::remove_dir_all(leftover)?;
          }
      }
      Ok(())
  }
  ```
  Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine cert_renewal` and expect `test result: ok. 4 passed`.
- [x] Write the failing tests: in `engine/src/control.rs` (add `#[cfg(test)] mod tests` when absent)
  ```rust
  #[test]
  fn revoked_or_unknown_engine_backs_off_five_minutes() {
      let revoked = tonic::Status::permission_denied("certificate revoked");
      let unknown = tonic::Status::permission_denied("unknown or deleted engine");
      for s in [&revoked, &unknown] {
          assert_eq!(reconnect_delay(Some(s), 0, 0.0), Duration::from_secs(270));
          assert_eq!(reconnect_delay(Some(s), 7, 1.0), Duration::from_secs(330));
      }
      let flaky = tonic::Status::unavailable("connection refused");
      assert!(reconnect_delay(Some(&flaky), 0, 0.5) <= Duration::from_millis(600));
      assert!(reconnect_delay(None, 20, 1.0) <= Duration::from_secs(36));
  }
  ```
  and in `engine/src/bootstrap.rs` `mod tests`
  ```rust
  #[test]
  fn node_name_env_override_is_validated() {
      let dir = tempfile::tempdir().unwrap();
      let path = dir.path().join("engine.toml");
      std::fs::write(&path, "node_name = \"engine-1\"\nstate_dir = \"/tmp/x\"\nstandalone_snapshot = \"/tmp/s\"\n").unwrap();
      let mut b = load(&path).unwrap();
      apply_env_overrides(&mut b, |k| (k == "NEXORA_ENGINE_NODE_NAME").then(|| "edge-b-worker-24".to_string())).unwrap();
      assert_eq!(b.node_name, "edge-b-worker-24");
      let err = apply_env_overrides(&mut b, |k| (k == "NEXORA_ENGINE_NODE_NAME").then(|| "Bad_Name".to_string())).unwrap_err();
      assert!(format!("{err:#}").contains("NEXORA_ENGINE_NODE_NAME must match [a-z0-9-]{1,63}"), "{err:#}");
      apply_env_overrides(&mut b, |_| None).unwrap();
      assert_eq!(b.node_name, "edge-b-worker-24");
  }
  ```
- [x] Run `scripts/dev-exec.sh cargo test --locked -p nexora-engine -- revoked_or_unknown_engine_backs_off_five_minutes node_name_env_override_is_validated` and expect FAIL with ``cannot find function `reconnect_delay` ``.
- [x] Implement:
  - `reconnect_delay` in `engine/src/control.rs`: a `PermissionDenied` status whose message is `certificate revoked` or `unknown or deleted engine` -> `Duration::from_secs_f64(300.0 * (0.9 + 0.2 * jitter))`; otherwise M1's `backoff(attempt)` computed with the given jitter (`500 ms × 2^attempt` capped at 30 s, times `0.8 + 0.4 * jitter`). `backoff(attempt)` keeps its signature and calls `reconnect_delay(None, attempt, rand::rng().random_range(0.0..=1.0))`.
  - `apply_env_overrides` in `engine/src/bootstrap.rs`: when `NEXORA_ENGINE_NODE_NAME` is set, validate it with the same rule as `node_name` (error `NEXORA_ENGINE_NODE_NAME must match [a-z0-9-]{1,63}`) and replace `node_name`; `load` calls `apply_env_overrides(&mut b, |k| std::env::var(k).ok())` right after parsing and before its own `node_name` check.
  - `engine/src/telemetry/metrics.rs`: `Metrics` gains `control_revoked: AtomicBool` and `cert_renewals: AtomicU64`; `render` registers `nexora_control_revoked` (`1 while the management plane refuses this engine's certificate`) and `nexora_control_cert_renewals_total` (`Engine certificates renewed over the control stream`).
  - `engine/src/control.rs`: `obtain_identity` calls `cert_renewal::recover_identity(&boot.state_dir)` before `load_identity`; `run` reloads the identity with `load_identity` before every `session` (so a swapped identity is used on reconnect) and sleeps `reconnect_delay(status, attempt, jitter)` where `status` is the `tonic::Status` inside `ControlError::Grpc`; a revoked/unknown status sets `control_revoked` true, a successful `connect` sets it false. Inside `session` (control runtime only): keep `pending: Option<(rcgen::KeyPair, Instant)>`; on stream open and on every stats tick, when `cert_validity(&id.cert_pem)` says `renewal_due` and no renewal younger than 30 s is pending, `new_csr(&id.engine_id)` and send `Msg::CertRequest(CertificateRequest { csr_der, reason: Reason::Renewal as i32 })`; on `ServerMsg::RenewCertificate` do the same with `Reason::Rotate`; on `ServerMsg::CertIssued(ci)` with a pending key: require `sha256(ci.ca_der)` to equal the SHA-256 of the DER in `id.ca_pem`, the issued certificate's SubjectPublicKeyInfo to equal `key.public_key_der()`, and its CN to equal the engine id; and verify it under the pinned CA (`verify_issued`); then `stage_identity(state_dir, cert_pem, key.serialize_pem())` and return `ControlError::Renewed` so `run` reconnects at once with the staged identity (attempt reset to 0); when that stream is accepted, `promote_identity`, increment `cert_renewals` and log `nexora-engine: certificate renewed (serial <hex>)`; a staged identity refused with `PermissionDenied`/`Unauthenticated` is discarded and the current identity retried at once; any failed check logs `nexora-engine: rejected issued certificate: <reason>` and drops the pending key. The pending private key is never written to disk before it is issued and never logged. Serving from the last applied snapshot continues in every case.
- [x] Run `scripts/dev-exec.sh 'cargo test --locked -p nexora-engine --all-targets && cargo clippy --locked -p nexora-engine --all-targets -- -D warnings && cargo fmt --all -- --check'` and expect `test result: ok` for every suite (including `cache_hit_path_does_not_allocate`) and no clippy or fmt findings.
- [ ] Commit: `git add engine && git commit -m "feat(engine): certificate renewal and rotation, revoked backoff, node name override"`.

## Task 10: Fleet acceptance tests

Files: `engine/src/runtime.rs`, `engine/tests/snapshot_apply.rs`, `mgmt/internal/pki/pki.go`, `mgmt/internal/pki/pki_test.go`, `docs/architecture.md` (cache clearing rule), `e2e/harness/lb.go` (backends that can change while the balancer runs), `e2e/harness/mgmt.go` (`EngineOptions.SkipControlWait`), `e2e/fleet_test.go` (`TestFleetRolloutAndPartition`, `TestEngineGroupScopedConfig`, `TestJoinTokenGroupAndExpiry`), `e2e/fleet_canary_test.go` (`TestCanaryRolloutHaltsOnFailure`), `e2e/fleet_cert_test.go` (`TestEngineCertRevocation`)
Interfaces: `func (e *Env) StartSwitchableBalancer(backends ...string) *SwitchableBalancer`, `type SwitchableBalancer struct { Addr string }` with `func (b *SwitchableBalancer) SetBackends(backends ...string)`; `EngineOptions.SkipControlWait bool` (return once the READY line is read); consumes the Task 7 harness, M1 `(*Env).StartDNSFixture` (`SetRecords`, `SetMode(t, "servfail")`), `harness.MustQuery`, `waitLatestApplied`, `wantA`, `createUDPUpstream`.

- [ ] Change `e2e/harness/lb.go`: `forward(l, backends func() []string)` reads the list for every accepted connection; `StartTCPBalancer` passes a function returning its fixed slice; add
  ```go
  // SwitchableBalancer is a TCP balancer whose backends can be replaced while it runs.
  type SwitchableBalancer struct {
  	Addr string

  	mu       sync.Mutex
  	backends []string
  }

  // StartSwitchableBalancer is StartTCPBalancer with SetBackends.
  func (e *Env) StartSwitchableBalancer(backends ...string) *SwitchableBalancer {
  	e.T.Helper()
  	b := &SwitchableBalancer{backends: backends}
  	l := e.listenLoopback()
  	b.Addr = l.Addr().String()
  	e.forward(l, func() []string {
  		b.mu.Lock()
  		defer b.mu.Unlock()
  		return append([]string(nil), b.backends...)
  	})
  	return b
  }

  // SetBackends replaces the backends for connections accepted from now on.
  func (b *SwitchableBalancer) SetBackends(backends ...string) {
  	b.mu.Lock()
  	defer b.mu.Unlock()
  	b.backends = backends
  }
  ```
  (`StartMgmt`'s public HTTP forwarder passes `func() []string { return []string{m.HTTPAddr} }`) and in `e2e/harness/mgmt.go` make `StartManagedEngineWith` skip `en.Proc.WaitLog(controlConnected, ...)` when `o.SkipControlWait` is set. Run `scripts/dev-exec.sh 'go vet ./e2e/... && go test ./e2e/harness/ -count=1'` and expect no vet output and `ok`.
- [ ] Write `e2e/fleet_test.go`:
  ```go
  package e2e

  import (
  	"context"
  	"fmt"
  	"net/http"
  	"regexp"
  	"testing"
  	"time"

  	"github.com/jackc/pgx/v5"
  	"github.com/miekg/dns"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  func rewrite(t *testing.T, api *harness.API, name, value string, engineGroupID any) {
  	t.Helper()
  	api.Must(http.MethodPost, "/rewrites", map[string]any{"name": name, "type": "A", "value": value, "engine_group_id": engineGroupID}, nil, http.StatusCreated)
  }

  func TestFleetRolloutAndPartition(t *testing.T) {
  	env := harness.New(t)
  	pg := env.StartPostgres()
  	ca := env.InitCA()
  	a := env.StartMgmt(pg, ca, harness.MgmtOptions{})
  	api := harness.Bootstrap(t, env, a.SetupToken(t), a.BaseURL)
  	api.DisableForwardedValidation() // fixture upstreams serve unsigned data under the real root anchor
  	fx := env.StartDNSFixture()
  	createUDPUpstream(t, api, "fixture", fx.UDP)
  	lb := env.StartSwitchableBalancer(a.GRPCAddr)
  	edge := api.CreateEngineGroup(map[string]any{"name": "fleet-edge"})
  	names := []string{"fleet-1", "fleet-2", "fleet-3"}
  	groups := map[string]string{"fleet-1": harness.DefaultEngineGroupID, "fleet-2": harness.DefaultEngineGroupID, "fleet-3": edge.ID}
  	tokens := map[string]string{harness.DefaultEngineGroupID: api.CreateJoinToken(), edge.ID: api.CreateJoinTokenFor(edge.ID, nil)}
  	engines := map[string]*harness.Engine{}
  	dirs := map[string]bool{}
  	for _, n := range names {
  		engines[n] = env.StartManagedEngine(n, []string{"https://" + lb.Addr}, tokens[groups[n]])
  		dirs[engines[n].StateDir] = true
  	}
  	if len(dirs) != 3 {
  		t.Fatalf("engines share a state directory: %v", dirs)
  	}

  	rewrite(t, api, "before.fleet.test", "192.0.2.10", nil)
  	waitLatestApplied(t, api, names...)
  	v := api.LatestVersion()
  	ids := map[string]string{}
  	for _, n := range names {
  		wantA(t, harness.MustQuery(t, engines[n].DNS, "before.fleet.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.10")
  		// Engine groups share one version sequence: a fleet-wide change is the same version in every group.
  		e := api.EngineByNode(n)
  		if e.Status != "current" || e.AppliedVersion != v || e.TargetVersion != v || e.EngineGroupID != groups[n] {
  			t.Fatalf("%s: %+v, want current, applied and targeted at %d in engine group %s", n, e, v, groups[n])
  		}
  		ids[n] = e.ID
  	}
  	rewrite(t, api, "after.fleet.test", "192.0.2.20", nil)
  	waitLatestApplied(t, api, names...)

  	// Partition: the only management instance dies; engines keep serving their last snapshot.
  	a.Proc.Kill()
  	harness.Eventually(t, 30*time.Second, func() error {
  		for _, n := range names {
  			if v := engines[n].Metric(t, "nexora_control_connected", nil); v != 0 {
  				return fmt.Errorf("%s still reports a control stream", n)
  			}
  		}
  		return nil
  	})
  	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(time.Second) {
  		for _, n := range names {
  			wantA(t, harness.MustQuery(t, engines[n].DNS, "before.fleet.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.10")
  			wantA(t, harness.MustQuery(t, engines[n].DNS, "after.fleet.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.20")
  		}
  	}

  	// A new instance behind the same address: engines return, a restarted engine keeps its identity.
  	b := env.StartMgmt(pg, ca, harness.MgmtOptions{})
  	lb.SetBackends(b.GRPCAddr)
  	api2 := env.NewAPI(b.BaseURL)
  	api2.Bearer = api.Bearer
  	for _, n := range names {
  		api2.WaitEngine(n, 45*time.Second, func(e harness.EngineView) bool { return e.Connected })
  	}
  	env.RestartEngine(engines["fleet-2"])
  	var listed []harness.EngineView
  	api2.Must(http.MethodGet, "/engines", nil, &listed, http.StatusOK)
  	if len(listed) != 3 || api2.EngineByNode("fleet-2").ID != ids["fleet-2"] {
  		t.Fatalf("restarted engine enrolled again: %+v", listed)
  	}
  	rewrite(t, api2, "later.fleet.test", "192.0.2.30", nil)
  	waitLatestApplied(t, api2, names...)
  	wantA(t, harness.MustQuery(t, engines["fleet-2"].DNS, "later.fleet.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.30")
  }

  func TestEngineGroupScopedConfig(t *testing.T) {
  	env := harness.New(t)
  	pg := env.StartPostgres()
  	ca := env.InitCA()
  	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{})
  	api := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
  	api.DisableForwardedValidation()
  	fxA, fxB := env.StartDNSFixture(), env.StartDNSFixture()
  	fxA.SetRecords(t, "site.fleet.test. 60 IN A 192.0.2.101")
  	fxB.SetRecords(t, "site.fleet.test. 60 IN A 192.0.2.102")
  	edge := api.CreateEngineGroup(map[string]any{"name": "edge", "upstream_mode": "override"})
  	createUDPUpstream(t, api, "fixture-a", fxA.UDP)
  	api.Must(http.MethodPost, "/upstreams", map[string]any{"name": "fixture-b", "protocol": "udp", "address": fxB.UDP, "timeout_ms": 250,
  		"enabled": true, "position": 1, "engine_group_id": edge.ID}, nil, http.StatusCreated)
  	rewrite(t, api, "global.scope.test", "192.0.2.1", nil)
  	rewrite(t, api, "only-edge.scope.test", "192.0.2.2", edge.ID)
  	zone := "edge-only.zone.test."
  	api.Must(http.MethodPost, "/zones", map[string]any{"name": zone, "kind": "primary", "default_ttl": 60, "engine_group_id": edge.ID,
  		"soa": map[string]any{"mname": "ns1." + zone, "rname": "hostmaster." + zone}, "nameservers": []string{"ns1." + zone}}, nil, http.StatusCreated)

  	engDefault := env.StartManagedEngine("scope-default", []string{mg.GRPCURL}, api.CreateJoinToken())
  	engEdge := env.StartManagedEngine("scope-edge", []string{mg.GRPCURL}, api.CreateJoinTokenFor(edge.ID, nil))
  	waitLatestApplied(t, api, "scope-default", "scope-edge")
  	if e := api.EngineByNode("scope-edge"); e.EngineGroupName != "edge" {
  		t.Fatalf("scope-edge enrolled into %q", e.EngineGroupName)
  	}

  	// Positive path first: each engine resolves through its own upstreams and serves the global rewrite.
  	wantA(t, harness.MustQuery(t, engDefault.DNS, "site.fleet.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.101")
  	wantA(t, harness.MustQuery(t, engEdge.DNS, "site.fleet.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.102")
  	for _, en := range []*harness.Engine{engDefault, engEdge} {
  		wantA(t, harness.MustQuery(t, en.DNS, "global.scope.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.1")
  	}
  	wantA(t, harness.MustQuery(t, engEdge.DNS, "only-edge.scope.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.2")
  	if soa := harness.MustQuery(t, engEdge.DNS, zone, dns.TypeSOA, harness.QueryOpts{}); !soa.Authoritative {
  		t.Fatalf("scope-edge is not authoritative for %s: %v", zone, soa)
  	}
  	if got := aValues(harness.MustQuery(t, engDefault.DNS, "only-edge.scope.test.", dns.TypeA, harness.QueryOpts{})); len(got) > 0 && got[0] == "192.0.2.2" {
  		t.Fatal("an engine of the default group served a rewrite scoped to edge")
  	}
  	if soa := harness.MustQuery(t, engDefault.DNS, zone, dns.TypeSOA, harness.QueryOpts{}); soa.Authoritative {
  		t.Fatalf("an engine of the default group serves %s: %v", zone, soa)
  	}

  	api.PatchEngine("scope-default", map[string]any{"engine_group_id": edge.ID})
  	api.WaitEngine("scope-default", 20*time.Second, func(e harness.EngineView) bool {
  		return e.EngineGroupID == edge.ID && e.Status == "current"
  	})
  	wantA(t, harness.MustQuery(t, engDefault.DNS, "only-edge.scope.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.2")
  	wantA(t, harness.MustQuery(t, engDefault.DNS, "site.fleet.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.102")
  }

  func TestJoinTokenGroupAndExpiry(t *testing.T) {
  	ctx := context.Background()
  	env := harness.New(t)
  	pg := env.StartPostgres()
  	ca := env.InitCA()
  	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{})
  	api := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
  	g := api.CreateEngineGroup(map[string]any{"name": "site-x"})

  	var once, stale struct {
  		Token     string `json:"token"`
  		JoinToken struct {
  			ID string `json:"id"`
  		} `json:"join_token"`
  	}
  	api.Must(http.MethodPost, "/join-tokens", map[string]any{"name": "once", "ttl_seconds": 600, "engine_group_id": g.ID, "max_uses": 1,
  		"labels": map[string]string{"rack": "r1"}}, &once, http.StatusCreated)
  	env.StartManagedEngine("joined", []string{mg.GRPCURL}, once.Token)
  	e := api.WaitEngine("joined", 20*time.Second, func(v harness.EngineView) bool { return v.Connected })
  	if e.EngineGroupID != g.ID || e.Labels["rack"] != "r1" {
  		t.Fatalf("joined engine %+v, want engine group site-x with rack=r1", e)
  	}

  	second := env.StartManagedEngineWith("second", []string{mg.GRPCURL}, once.Token, harness.EngineOptions{SkipControlWait: true})
  	second.Proc.WaitLog(regexp.MustCompile(`join token exhausted`), 30*time.Second)
  	api.Must(http.MethodPost, "/join-tokens", map[string]any{"name": "stale", "ttl_seconds": 600, "engine_group_id": g.ID}, &stale, http.StatusCreated)
  	conn, err := pgx.Connect(ctx, pg.URL)
  	if err != nil {
  		t.Fatal(err)
  	}
  	defer conn.Close(ctx)
  	if _, err := conn.Exec(ctx, `update join_tokens set expires_at = now() - interval '1 second' where id = $1`, stale.JoinToken.ID); err != nil {
  		t.Fatal(err)
  	}
  	late := env.StartManagedEngineWith("late", []string{mg.GRPCURL}, stale.Token, harness.EngineOptions{SkipControlWait: true})
  	late.Proc.WaitLog(regexp.MustCompile(`join token expired`), 30*time.Second)

  	var engines []harness.EngineView
  	api.Must(http.MethodGet, "/engines", nil, &engines, http.StatusOK)
  	for _, v := range engines {
  		if v.NodeName == "second" || v.NodeName == "late" {
  			t.Fatalf("engine %s enrolled with an exhausted or expired token", v.NodeName)
  		}
  	}
  	var listed []map[string]any
  	api.Must(http.MethodGet, "/join-tokens", nil, &listed, http.StatusOK)
  	states := map[string]any{}
  	for _, jt := range listed {
  		states[jt["id"].(string)] = jt["state"]
  	}
  	if states[once.JoinToken.ID] != "exhausted" || states[stale.JoinToken.ID] != "expired" {
  		t.Fatalf("token states %v", states)
  	}
  }
  ```
  `env.RestartEngine` already waits for the restarted engine's control stream; the engine count and id prove it did not enroll again.
- [ ] Product fixes these tests exposed (each with a unit test): `engine/src/runtime.rs` adds `u:<sha256 of strategy and upstreams>` to the cache-invalidation key, so an engine moved into a group with other upstreams stops serving answers cached through the old ones (`upstream_changes_clear_cached_answers` in `engine/tests/snapshot_apply.rs`); `mgmt/internal/pki/pki.go` `SignEngineCSR` backdates `NotBefore` by `min(1h, validity/10)` instead of 1 h, which put the 2/3 renewal point of short-lived certificates in the past and made engines renew in a loop (`TestEngineCertificateRenewalPointIsInTheFuture`).
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=$PWD/bin go test ./e2e/ -run "TestFleetRolloutAndPartition|TestEngineGroupScopedConfig|TestJoinTokenGroupAndExpiry" -count=1 -v -timeout 20m'` and expect `--- PASS` for all three. To prove the partition assertion bites, temporarily replace `a.Proc.Kill()` with `time.Sleep(time.Second)` and rerun `TestFleetRolloutAndPartition`; expect FAIL with `still reports a control stream`; restore.
- [ ] Write `e2e/fleet_canary_test.go`:
  ```go
  package e2e

  import (
  	"fmt"
  	"net/http"
  	"slices"
  	"strings"
  	"sync"
  	"testing"
  	"time"

  	"github.com/miekg/dns"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  func TestCanaryRolloutHaltsOnFailure(t *testing.T) {
  	env := harness.New(t)
  	pg := env.StartPostgres()
  	ca := env.InitCA()
  	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{})
  	api := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
  	api.DisableForwardedValidation()
  	good, bad := env.StartDNSFixture(), env.StartDNSFixture()
  	good.SetRecords(t, "healthy.canary.test. 60 IN A 192.0.2.1")
  	bad.SetMode(t, "servfail")
  	g := api.CreateEngineGroup(map[string]any{"name": "canary", "upstream_mode": "override", "rollout_strategy": "canary", "canary_count": 1,
  		"ack_timeout_seconds": 30, "health_window_seconds": 20, "max_servfail_ratio": 0.05, "min_health_queries": 20})
  	var up struct {
  		ID string `json:"id"`
  	}
  	api.Must(http.MethodPost, "/upstreams", map[string]any{"name": "canary-up", "protocol": "udp", "address": good.UDP, "timeout_ms": 250,
  		"enabled": true, "position": 0, "engine_group_id": g.ID}, &up, http.StatusCreated)
  	names := []string{"canary-1", "canary-2", "canary-3"}
  	engines := map[string]*harness.Engine{}
  	token := api.CreateJoinTokenFor(g.ID, nil)
  	for _, n := range names {
  		engines[n] = env.StartManagedEngine(n, []string{mg.GRPCURL}, token)
  	}
  	api.PatchEngine("canary-3", map[string]any{"labels": map[string]string{"nexora.io/canary": "true"}})
  	canaryID := api.EngineByNode("canary-3").ID
  	base := api.WaitEngineGroupStable(g.ID, 0, 90*time.Second)
  	for _, n := range names {
  		api.WaitEngine(n, 30*time.Second, func(e harness.EngineView) bool { return e.Connected && e.AppliedVersion >= base })
  	}

  	stop := make(chan struct{})
  	var wg sync.WaitGroup
  	for _, n := range names {
  		addr := engines[n].DNS
  		wg.Add(1)
  		go func() {
  			defer wg.Done()
  			for i := 0; ; i++ {
  				select {
  				case <-stop:
  					return
  				case <-time.After(20 * time.Millisecond):
  				}
  				_, _, _ = harness.Query(t, addr, fmt.Sprintf("load-%d.canary.test.", i), dns.TypeA, harness.QueryOpts{})
  			}
  		}()
  	}
  	defer func() { close(stop); wg.Wait() }()

  	// Positive path: a healthy change passes the canary gate and completes everywhere.
  	rewrite(t, api, "gate.canary.test", "192.0.2.50", g.ID)
  	healthy := api.WaitRollout(g.ID, base+1, 3*time.Minute, "completed")
  	if healthy.Strategy != "canary" || !slices.Equal(healthy.CanaryEngineIDs, []string{canaryID}) {
  		t.Fatalf("healthy rollout %+v, want canary strategy with canary-3 as canary", healthy)
  	}
  	for _, n := range names {
  		api.WaitEngine(n, 20*time.Second, func(e harness.EngineView) bool { return e.AppliedVersion == healthy.Version })
  		wantA(t, harness.MustQuery(t, engines[n].DNS, "healthy.canary.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.1")
  	}

  	var ups []map[string]any
  	api.Must(http.MethodGet, "/upstreams", nil, &ups, http.StatusOK)
  	for _, u := range ups {
  		if u["id"] == up.ID {
  			u["address"] = bad.UDP
  			delete(u, "id")
  			api.Must(http.MethodPut, "/upstreams/"+up.ID, u, nil, http.StatusOK)
  		}
  	}
  	halted := api.WaitRollout(g.ID, healthy.Version+1, 3*time.Minute, "halted")
  	if !strings.Contains(halted.HaltReason, "engine canary-3 servfail ratio") || !slices.Equal(halted.CanaryEngineIDs, []string{canaryID}) {
  		t.Fatalf("halted rollout %+v", halted)
  	}
  	if e := api.EngineByNode("canary-3"); e.AppliedVersion != halted.Version {
  		t.Fatalf("canary applied %d, want %d", e.AppliedVersion, halted.Version)
  	}
  	for _, n := range []string{"canary-1", "canary-2"} {
  		if e := api.EngineByNode(n); e.AppliedVersion != healthy.Version {
  			t.Fatalf("%s applied %d during a halted canary, want %d", n, e.AppliedVersion, healthy.Version)
  		}
  		wantA(t, harness.MustQuery(t, engines[n].DNS, "healthy.canary.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.1")
  	}
  	time.Sleep(10 * time.Second)
  	if r := api.WaitRollout(g.ID, halted.Version, time.Second, "halted", "pending", "canary", "verifying", "rolling", "completed"); r.State != "halted" || api.EngineByNode("canary-1").AppliedVersion != healthy.Version {
  		t.Fatalf("halted rollout progressed on its own: %+v", r)
  	}

  	var rb harness.RolloutView
  	api.Must(http.MethodPost, "/engine-groups/"+g.ID+"/rollback", map[string]any{"to_version": healthy.Version}, &rb, http.StatusAccepted)
  	api.WaitRollout(g.ID, rb.Version, time.Minute, "completed")
  	waitLatestApplied(t, api, names...)
  	wantA(t, harness.MustQuery(t, engines["canary-3"].DNS, "healthy.canary.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.1")

  	rewrite(t, api, "held.canary.test", "192.0.2.99", g.ID)
  	time.Sleep(5 * time.Second)
  	held := api.WaitRollout(g.ID, rb.Version+1, time.Second, "pending", "canary", "verifying", "rolling", "completed", "halted")
  	if held.State != "pending" || api.EngineByNode("canary-1").AppliedVersion != rb.Version {
  		t.Fatalf("a change after rollback must wait while paused: %+v", held)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'NEXORA_E2E_BIN_DIR=$PWD/bin go test ./e2e/ -run TestCanaryRolloutHaltsOnFailure -count=1 -v -timeout 20m'` and expect `--- PASS`. Mutation check: change `"max_servfail_ratio": 0.05` in the test to `1` (the gate can never trip), rerun, and expect FAIL with `waiting for [halted]`; restore `0.05`.
- [ ] Write `e2e/fleet_cert_test.go`:
  ```go
  package e2e

  import (
  	"context"
  	"crypto/tls"
  	"fmt"
  	"net/http"
  	"path/filepath"
  	"testing"
  	"time"

  	"github.com/jackc/pgx/v5"
  	"github.com/miekg/dns"
  	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
  	"google.golang.org/grpc"
  	"google.golang.org/grpc/codes"
  	"google.golang.org/grpc/credentials"
  	"google.golang.org/grpc/status"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  // exportAs calls the builtin OTLP logs service with an engine's own identity files.
  func exportAs(t *testing.T, mg *harness.Mgmt, en *harness.Engine) error {
  	t.Helper()
  	id := filepath.Join(en.StateDir, "identity")
  	pair, err := tls.LoadX509KeyPair(filepath.Join(id, "cert.pem"), filepath.Join(id, "key.pem"))
  	if err != nil {
  		t.Fatal(err)
  	}
  	// The server's identity is not under test here; the client certificate is.
  	creds := credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{pair}, InsecureSkipVerify: true}) //nolint:gosec
  	conn, err := grpc.NewClient(mg.GRPCAddr, grpc.WithTransportCredentials(creds))
  	if err != nil {
  		t.Fatal(err)
  	}
  	defer conn.Close()
  	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
  	defer cancel()
  	_, err = collogspb.NewLogsServiceClient(conn).Export(ctx, &collogspb.ExportLogsServiceRequest{})
  	return err
  }

  func TestEngineCertRevocation(t *testing.T) {
  	ctx := context.Background()
  	env := harness.New(t)
  	pg := env.StartPostgres()
  	ca := env.InitCA()
  	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{ExtraEnv: []string{"NEXORA_ENGINE_CERT_TTL=60s"}})
  	api := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
  	api.DisableForwardedValidation()
  	fx := env.StartDNSFixture()
  	createUDPUpstream(t, api, "fixture", fx.UDP)
  	names := []string{"rev-1", "rev-2", "rev-3"}
  	engines := map[string]*harness.Engine{}
  	token := api.CreateJoinToken()
  	for _, n := range names {
  		engines[n] = env.StartManagedEngine(n, []string{mg.GRPCURL}, token)
  	}
  	rewrite(t, api, "rev.fleet.test", "192.0.2.30", nil)
  	waitLatestApplied(t, api, names...)

  	if err := exportAs(t, mg, engines["rev-1"]); err != nil {
  		t.Fatalf("rev-1 identity rejected before revocation: %v", err)
  	}
  	api.Must(http.MethodPost, "/engines/"+api.EngineByNode("rev-1").ID+"/revoke", nil, nil, http.StatusOK)
  	harness.Eventually(t, 15*time.Second, func() error {
  		if s := api.EngineByNode("rev-1").Status; s != "revoked" {
  			return fmt.Errorf("status %s", s)
  		}
  		if engines["rev-1"].Metric(t, "nexora_control_revoked", nil) != 1 || engines["rev-1"].Metric(t, "nexora_control_connected", nil) != 0 {
  			return fmt.Errorf("rev-1 has not observed its revocation")
  		}
  		if s, _ := status.FromError(exportAs(t, mg, engines["rev-1"])); s.Code() != codes.PermissionDenied || s.Message() != "certificate revoked" {
  			return fmt.Errorf("export as rev-1: %v", s)
  		}
  		return nil
  	})
  	wantA(t, harness.MustQuery(t, engines["rev-1"].DNS, "rev.fleet.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.30")

  	applied := api.EngineByNode("rev-1").AppliedVersion
  	rewrite(t, api, "post-revoke.fleet.test", "192.0.2.31", nil)
  	waitLatestApplied(t, api, "rev-2", "rev-3")
  	wantA(t, harness.MustQuery(t, engines["rev-2"].DNS, "post-revoke.fleet.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.31")
  	if got := api.EngineByNode("rev-1").AppliedVersion; got != applied {
  		t.Fatalf("revoked engine applied %d, want %d", got, applied)
  	}
  	if got := aValues(harness.MustQuery(t, engines["rev-1"].DNS, "post-revoke.fleet.test.", dns.TypeA, harness.QueryOpts{})); len(got) > 0 && got[0] == "192.0.2.31" {
  		t.Fatal("revoked engine received configuration after revocation")
  	}

  	conn, err := pgx.Connect(ctx, pg.URL)
  	if err != nil {
  		t.Fatal(err)
  	}
  	defer conn.Close(ctx)
  	reason := func(serial string) string {
  		var r *string
  		_ = conn.QueryRow(ctx, `select revoke_reason from engine_certificates where serial = $1`, serial).Scan(&r)
  		if r == nil {
  			return ""
  		}
  		return *r
  	}
  	rev2 := api.EngineByNode("rev-2")
  	api.Must(http.MethodPost, "/engines/"+rev2.ID+"/rotate-certificate", nil, nil, http.StatusAccepted)
  	harness.Eventually(t, 30*time.Second, func() error {
  		e := api.EngineByNode("rev-2")
  		if !e.Connected || e.CertificateSerial == rev2.CertificateSerial || reason(rev2.CertificateSerial) != "superseded" {
  			return fmt.Errorf("rev-2 not rotated yet: %+v", e)
  		}
  		return nil
  	})

  	s3 := api.EngineByNode("rev-3").CertificateSerial
  	harness.Eventually(t, 75*time.Second, func() error {
  		if e := api.EngineByNode("rev-3"); e.CertificateSerial == s3 || !e.Connected {
  			return fmt.Errorf("rev-3 has not renewed on its own")
  		}
  		return nil
  	})

  	if v := mg.Metric(t, "nexora_mgmt_engines_disconnected", nil); v != 0 {
  		t.Fatalf("disconnected gauge %v with every non-revoked engine connected", v)
  	}
  	engines["rev-3"].Proc.Stop()
  	harness.Eventually(t, 110*time.Second, func() error {
  		if v := mg.Metric(t, "nexora_mgmt_engines_disconnected", nil); v != 1 {
  			return fmt.Errorf("nexora_mgmt_engines_disconnected = %v, want 1", v)
  		}
  		return nil
  	})
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'NEXORA_E2E_BIN_DIR=$PWD/bin go test ./e2e/ -run TestEngineCertRevocation -count=1 -v -timeout 20m'` and expect `--- PASS`. Mutation check: make `fleet.CheckCertificate` return nil for revoked engines, rebuild with `make e2e-build`, rerun, and expect FAIL with `export as rev-1`; restore and rebuild.
- [ ] Run the regression set `scripts/dev-exec.sh 'NEXORA_E2E_BIN_DIR=$PWD/bin go test ./e2e/ -run "TestMgmtStatelessHA|TestInvalidSnapshotRejected|TestAuthoritativeZonePropagation|TestSecondaryAndDynamicUpdate|TestRPZPolicy|TestFleetAPI|TestMgmtCLIFleet" -count=1 -v -timeout 40m'` and expect `--- PASS` for each.
- [ ] Commit: `git add e2e/harness/lb.go e2e/harness/mgmt.go e2e/fleet_test.go e2e/fleet_canary_test.go e2e/fleet_cert_test.go && git commit -m "test(e2e): fleet partition, engine-group scoping, join tokens, canary halt, revocation"`.

## Task 11: Fleet GUI

The screens follow the M1–M4 GUI layout (pages in `web/src/pages/`, TanStack Query hooks over `api`/`unwrap`, Radix components from `components/ui`, `data-testid` hooks, `useCan` for role gating) and are covered by the request-based `TestGUICoverage`: every M5 operation must be issued by the browser in `web/e2e/screens/20-fleet.spec.ts` or `21-engine-group-scope.spec.ts`. The web package has no unit-test runner, so badge and progress rendering is asserted in Playwright. This task runs after M4 Task 15 (it edits `web/src/pages/ZonesPage.tsx`).

Files: `web/src/api/fleet.ts` (hooks), `web/src/components/fleet.tsx` (`EngineStatusBadge`, `RolloutStateBadge`, `RolloutProgress`, `EngineGroupSelect`, `LabelsEditor`), `web/src/pages/EnginesPage.tsx` (`/engines`: fleet summary, engine groups, engines, join tokens), `web/src/pages/EngineGroupPage.tsx` (`/engines/groups/:id`), `web/src/pages/EngineDetailPage.tsx` (`/engines/nodes/:id`), `web/src/pages/RolloutPage.tsx` (`/engines/rollouts/:id`), `web/src/app/router.tsx` (routes), engine-group scope fields in `web/src/pages/UpstreamsPage.tsx`, `FilteringPage.tsx`, `PoliciesPage.tsx`, `RewritesPage.tsx`, `ForwardZonesSection.tsx`, `RpzPage.tsx`, `ZonesPage.tsx`, `web/e2e/screens/20-fleet.spec.ts`, `web/e2e/screens/21-engine-group-scope.spec.ts`, `e2e/gui_test.go` (coverage glob)
Interfaces: routes above; `EngineGroupSelect` props `{ value: string | null; onChange(v: string | null): void; id?: string; testId: string; allowAll?: boolean; disabled?: boolean; className?: string }` where `null` renders `All engine groups` (`allowAll={false}` drops that option); `ConfirmDialog` gains `testId` (default `confirm-delete`) and `destructive` (default true); test ids listed in the specs below (the M1 ids `engine-row-<node>`, `engine-open-<node>`, `engine-detail`, `engine-delete`, `confirm-delete`, `jointoken-add`, `jointoken-name`, `jointoken-save`, `jointoken-value`, `jointoken-row-<name>`, `jointoken-revoke-<name>` keep working for `05-engines.spec.ts`).

- [x] Widen the coverage glob in `e2e/gui_test.go` from `web/e2e/screens/[01][0-9]-*.spec.ts` to `web/e2e/screens/[012][0-9]-*.spec.ts`, and run `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=$PWD/bin go test ./e2e/ -run TestGUICoverage -count=1'`; expect FAIL whose uncovered list is exactly the M5 operations (`createEngineGroup`, `deleteEngineGroup`, `getEngineGroup`, `getEngineStats`, `getFleetSummary`, `getRollout`, `listEngineGroups`, `listRollouts`, `resumeEngineGroupRollouts`, `revokeEngine`, `rollbackEngineGroup`, `rotateEngineCertificate`, `updateEngine`, `updateEngineGroup`).
- [x] Write the failing Playwright spec `web/e2e/screens/20-fleet.spec.ts`:
  ```ts
  import { test, expect, env, login } from "../fixtures";

  test("admin manages engine groups, engines, rollouts and certificates", async ({
    page,
  }) => {
    // covers getFleetSummary, listEngineGroups, createEngineGroup, getEngineGroup, updateEngineGroup,
    // updateEngine, getEngineStats, rotateEngineCertificate, listRollouts, getRollout,
    // rollbackEngineGroup, resumeEngineGroupRollouts, revokeEngine, deleteEngineGroup
    await login(
      page,
      env("NEXORA_E2E_ADMIN_USER"),
      env("NEXORA_E2E_ADMIN_PASSWORD"),
    );
    await page.getByTestId("nav-engines").click();
    await expect(page.getByTestId("fleet-summary")).toContainText("default");

    await page.getByTestId("enginegroup-add").click();
    await page.getByTestId("enginegroup-name").fill("gui-edge");
    await page.getByTestId("enginegroup-save").click();
    await expect(page.getByTestId("enginegroup-row-gui-edge")).toContainText(
      "all_at_once",
    );

    await page.getByTestId("engine-open-gui-engine").click();
    await expect(page.getByTestId("engine-detail")).toContainText("gui-engine");
    await expect(page.getByTestId("engine-stats-chart")).toBeVisible();
    await page.getByTestId("engine-group-select").click();
    await page.getByRole("option", { name: "gui-edge", exact: true }).click();
    await page.getByTestId("engine-label-add").click();
    await page.getByTestId("engine-label-key-0").fill("nexora.io/canary");
    await page.getByTestId("engine-label-value-0").fill("true");
    await page.getByTestId("engine-save").click();
    await expect(page.getByTestId("engine-group-name")).toHaveText("gui-edge");
    await expect(page.getByTestId("engine-status")).toContainText("current", {
      timeout: 30_000,
    });
    await page.getByTestId("engine-rotate").click();
    await page.getByTestId("confirm-rotate").click();
    await expect(page.getByText("Rotation requested")).toBeVisible();

    await page.getByTestId("nav-engines").click();
    await page.getByTestId("enginegroup-open-gui-edge").click();
    await expect(page.getByTestId("enginegroup-detail")).toContainText(
      "gui-edge",
    );
    await page.getByTestId("enginegroup-description").fill("from the gui");
    await page.getByTestId("enginegroup-save-settings").click();
    await expect(page.getByText("Saved")).toBeVisible();

    await page.getByTestId("rollout-open").first().click();
    await expect(page.getByTestId("rollout-detail")).toBeVisible();
    await expect(page.getByTestId("rollout-progress")).toHaveAttribute(
      "role",
      "progressbar",
    );
    await expect(page.getByTestId("rollout-engines")).toContainText(
      "gui-engine",
    );
    await page.goBack();

    await page.getByTestId("enginegroup-rollback").click();
    await page.getByTestId("rollback-version").click();
    await page.getByRole("option").last().click();
    await page.getByTestId("rollback-confirm").click();
    await expect(page.getByTestId("enginegroup-paused")).toBeVisible();
    await page.getByTestId("enginegroup-resume").click();
    await expect(page.getByTestId("enginegroup-paused")).toHaveCount(0);

    await page.getByTestId("nav-engines").click();
    await page.getByTestId("engine-open-gui-engine").click();
    await page.getByTestId("engine-revoke").click();
    await page.getByTestId("confirm-revoke").click();
    await expect(page.getByTestId("engine-status")).toContainText("revoked");

    await page.getByTestId("nav-engines").click();
    await page.getByTestId("enginegroup-add").click();
    await page.getByTestId("enginegroup-name").fill("gui-tmp");
    await page.getByTestId("enginegroup-save").click();
    await page.getByTestId("enginegroup-open-gui-tmp").click();
    await page.getByTestId("enginegroup-delete").click();
    await page.getByTestId("confirm-delete").click();
    await expect(page.getByTestId("enginegroup-row-gui-tmp")).toHaveCount(0);
  });
  ```
- [x] Write the failing Playwright spec `web/e2e/screens/21-engine-group-scope.spec.ts` (the column-header check is scoped to the `Upstreams` region because the forward zones section on the same page also has an "Engine group" column):
  ```ts
  import { test, expect, env, login } from "../fixtures";

  test("operator scopes an upstream and a rewrite to an engine group", async ({
    page,
  }) => {
    await login(
      page,
      env("NEXORA_E2E_OPERATOR_USER"),
      env("NEXORA_E2E_OPERATOR_PASSWORD"),
    );
    const host = `scoped-${Date.now()}.home.test`;
    await page.getByTestId("nav-rewrites").click();
    await page.getByRole("button", { name: "New rewrite" }).click();
    const dialog = page.getByRole("dialog", { name: "New rewrite" });
    await dialog.getByLabel("Name").fill(host);
    await dialog.getByLabel("Type").click();
    await page.getByRole("option", { name: "A", exact: true }).click();
    await dialog.getByLabel("Value").fill("192.168.1.77");
    await dialog.getByTestId("rewrite-engine-group").click();
    await page.getByRole("option", { name: "gui-edge", exact: true }).click();
    await dialog.getByRole("button", { name: "Save" }).click();
    await expect(
      page.getByRole("row", { name: new RegExp(host) }),
    ).toContainText("gui-edge");

    await page.getByTestId("nav-upstreams").click();
    await expect(
      page
        .getByRole("region", { name: "Upstreams", exact: true })
        .getByRole("columnheader", { name: "Engine group" }),
    ).toBeVisible();
  });
  ```
- [x] Run `scripts/dev-exec.sh 'NEXORA_E2E_BIN_DIR=$PWD/bin go test ./e2e/ -run TestGUICoverage -count=1'` and expect FAIL with `getByTestId('fleet-summary')` in the Playwright output.
- [x] Implement `web/src/api/fleet.ts`: one hook per operation (`useFleetSummary`, `useEngineGroups({ live })` (polls only on the fleet screens; scope selects load once), `useEngines`, `useDeleteEngine`, `useEngineGroup(id)`, `useRollouts({ engineGroupId, limit })`, `useRollout(id)`, `useEngine(id)`, `useEngineStats(id, window)`, and mutations `useCreateEngineGroup`, `useUpdateEngineGroup`, `useDeleteEngineGroup`, `useRollbackEngineGroup`, `useResumeRollouts`, `useUpdateEngine`, `useRevokeEngine`, `useRotateEngineCertificate`); list queries refetch every 5 s; mutations invalidate the `["fleet"]` and `["engines"]` query keys; a 409 `conflict` shows the existing conflict message pattern of the M2 pages (`reload to see the other change`).
- [x] Implement `web/src/components/fleet.tsx` (also `RolloutStages` — the strategy's phases with the current one marked and halted/rolled back/superseded appended — `EngineGroupName`, `LinkButton`, `BackLink`, `canarySize`): `EngineStatusBadge` (current green, behind/ahead amber, rejected/revoked red, disconnected muted; text is the status value), `RolloutStateBadge` (completed green, pending/canary/verifying/rolling in the primary accent (the theme has no blue token), halted red, rolled_back/superseded muted), `RolloutProgress` (`role="progressbar"`, `aria-valuenow` = round(applied / total × 100), text `<applied> / <total> applied`, `<rejected> rejected` when non-zero, halt reason under it when halted; `data-testid="rollout-progress"`), `EngineGroupSelect` (Radix `Select` over `useEngineGroups`, first option `All engine groups` mapping to `null`), `LabelsEditor` (rows of key/value inputs `engine-label-key-<i>` / `engine-label-value-<i>`, add button `engine-label-add`, remove buttons).
- [x] Implement the pages (the engines table sorts by column, filters by text/label, engine group and status, pages 50 rows at a time, keeps its filters in the URL, and flags config lag and engine software drift from the fleet's most common version; the upstreams table sits in a `section` labelled `Upstreams`):
  - `EnginesPage` (`/engines`): `fleet-summary` card (engines by status, halted rollouts, one line per engine group with name, engines connected/total, stable version and `RolloutStateBadge` of its active rollout); "Engine groups" table (row `enginegroup-row-<name>` with name, strategy, upstream mode, engines, stable version; link `enginegroup-open-<name>`) with `enginegroup-add` dialog (`enginegroup-name`, description, upstream mode, strategy, canary count/percent, save `enginegroup-save`; 400/409 messages inline); the M1 engines table extended with engine group, target version and `EngineStatusBadge` (`engine-open-<node>` navigates to the engine page); the M1 join token section extended with an engine group select and max uses in the create dialog and engine group, state and uses columns.
  - `EngineGroupPage` (`/engines/groups/:id`, `enginegroup-detail`): settings form (description `enginegroup-description`, upstream mode, extra ACL CIDRs, OTLP endpoint, strategy and gate parameters; save `enginegroup-save-settings` sends `revision`, success text `Saved`); paused banner `enginegroup-paused` with `enginegroup-resume`; `enginegroup-rollback` dialog whose `rollback-version` select lists the group's rollout versions older than the newest one and `rollback-confirm`; rollouts table (newest 20, `rollout-open` links, `RolloutStateBadge`, `RolloutProgress`); engines of the group; `enginegroup-delete` (hidden for `default` and without `deleteEngineGroup` permission) with `confirm-delete`, returning to `/engines`.
  - `EngineDetailPage` (`/engines/nodes/:id`, `engine-detail`): node name, engine id, version, connected, last seen, `engine-status` (`EngineStatusBadge`), applied/target version and rejection reason; `engine-group-name`; `engine-group-select` (the `EngineGroupSelect` without the `All engine groups` option) and `LabelsEditor` with `engine-save` (sends `revision`); certificate serial and not-after; Recharts line chart `engine-stats-chart` of QPS and p99 over `useEngineStats(id, "1h")`; `engine-rotate` (+ `confirm-rotate`, toast `Rotation requested`), `engine-revoke` (+ `confirm-revoke`), the M1 `engine-delete` (+ `confirm-delete`); admin-only actions hidden via `useCan`.
  - `RolloutPage` (`/engines/rollouts/:id`, `rollout-detail`): state, kind, strategy, version, from version, creator, halt reason, `RolloutProgress`, and table `rollout-engines` (node name, canary marker, connected, applied version, progress).
  - `web/src/app/router.tsx`: routes `engines/groups/:id`, `engines/nodes/:id`, `engines/rollouts/:id` under the authenticated shell.
  - Scoped pages: each create/edit dialog gets an "Engine group" `EngineGroupSelect` (test id `<resource>-engine-group`, e.g. `rewrite-engine-group`, `upstream-engine-group`) and each table an "Engine group" column showing the group name or `All engine groups`; the rewrite dialog hides the field when a policy group is selected.
- [x] Run `scripts/dev-exec.sh 'make web-test && make e2e-build && NEXORA_E2E_BIN_DIR=$PWD/bin go test ./e2e/ -run TestGUICoverage -count=1 -v'` and expect typecheck, lint and build clean and `--- PASS: TestGUICoverage` (every OpenAPI operation, including the fleet ones, is covered; `05-engines.spec.ts` still passes).
- [ ] Commit: `git add web e2e/gui_test.go && git commit -m "feat(web): fleet overview, engine group, engine and rollout pages; engine-group scope fields"`.

## Task 12: Release images workflow (extend) and docker-compose example

Files: `.github/workflows/images.yml` (existing: add `:main` and the multi-arch manifest check), `deploy/deploytest/workflow_test.go` (`TestImagesWorkflow`), `deploy/compose/docker-compose.yml`, `deploy/compose/engine.toml`, `deploy/compose/otel-collector.yaml`, `deploy/compose/.env.example`, `deploy/compose/secrets/.gitignore`, `deploy/deploytest/compose_test.go` (`TestComposeExample`)
Interfaces: images `192.168.10.131:5000/azrtydxb/nexora-engine` and `.../nexora-mgmt` tagged `sha-<7>` (`v*` on tags) and `main` on main, pulled as `192.168.10.131/azrtydxb/<name>:<tag>`; compose services `postgres`, `volume-init`, `ca-init`, `migrate`, `mgmt`, `engine` (profile `engine`), `otel-collector` (profile `otel`).

- [ ] Write the failing test `deploy/deploytest/workflow_test.go`:
  ```go
  // Package deploytest checks the deployment artifacts (workflow, compose, Helm chart, docs) statically.
  package deploytest

  import (
  	"fmt"
  	"os"
  	"regexp"
  	"strings"
  	"testing"

  	"go.yaml.in/yaml/v3"
  )

  type step struct {
  	Name string            `yaml:"name"`
  	Uses string            `yaml:"uses"`
  	Run  string            `yaml:"run"`
  	With map[string]string `yaml:"with"`
  }

  type job struct {
  	RunsOn         string `yaml:"runs-on"`
  	Needs          any    `yaml:"needs"`
  	TimeoutMinutes int    `yaml:"timeout-minutes"`
  	Strategy       struct {
  		Matrix map[string]any `yaml:"matrix"`
  	} `yaml:"strategy"`
  	Steps []step `yaml:"steps"`
  }

  func TestImagesWorkflow(t *testing.T) {
  	raw, err := os.ReadFile("../../.github/workflows/images.yml")
  	if err != nil {
  		t.Fatal(err)
  	}
  	var wf struct {
  		Jobs map[string]job `yaml:"jobs"`
  	}
  	if err := yaml.Unmarshal(raw, &wf); err != nil {
  		t.Fatal(err)
  	}
  	build, merge := wf.Jobs["build"], wf.Jobs["merge"]
  	images, archs := map[string]bool{}, map[string]bool{}
  	for _, im := range build.Strategy.Matrix["image"].([]any) {
  		images[im.(map[string]any)["name"].(string)] = true
  	}
  	for _, a := range build.Strategy.Matrix["arch"].([]any) {
  		m := a.(map[string]any)
  		archs[fmt.Sprint(m["runner"], "|", m["platform"])] = true
  	}
  	for _, want := range []string{"nexora-engine", "nexora-mgmt"} {
  		if !images[want] {
  			t.Errorf("build matrix lacks image %s", want)
  		}
  	}
  	for _, want := range []string{"arc-azrtydxb-publish|linux/arm64", "arc-azrtydxb-amd64-publish|linux/amd64"} {
  		if !archs[want] {
  			t.Errorf("build matrix lacks %s", want)
  		}
  	}
  	if build.RunsOn != "${{ matrix.arch.runner }}" || merge.RunsOn != "arc-azrtydxb-publish" || merge.Needs != "build" {
  		t.Errorf("runs-on/needs: build %q merge %q needs %v", build.RunsOn, merge.RunsOn, merge.Needs)
  	}
  	pinned := regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)
  	var all strings.Builder
  	for name, j := range wf.Jobs {
  		if j.TimeoutMinutes == 0 {
  			t.Errorf("job %s has no timeout-minutes", name)
  		}
  		for _, s := range j.Steps {
  			if s.Uses != "" && !pinned.MatchString(s.Uses) {
  				t.Errorf("job %s step %q uses unpinned action %s", name, s.Name, s.Uses)
  			}
  			all.WriteString(s.Run)
  			for _, v := range s.With {
  				all.WriteString(v)
  			}
  		}
  	}
  	for _, want := range []string{"push-by-digest=true", "192.168.10.131:5000/azrtydxb", "imagetools create", "sha-${GITHUB_SHA::7}",
  		`refs/heads/main`, `:main"`, `grep -q '"arm64"'`, `grep -q '"amd64"'`} {
  		if !strings.Contains(all.String()+string(raw), want) {
  			t.Errorf("workflow does not contain %q", want)
  		}
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./deploy/deploytest/ -run TestImagesWorkflow -count=1` and expect FAIL with `workflow does not contain "refs/heads/main"`.
- [ ] Change the `Create the multi-arch manifest list` step of the `merge` job in `.github/workflows/images.yml` to:
  ```yaml
  - name: Create the multi-arch manifest list
    working-directory: /tmp/digests
    env:
      IMAGE: ${{ env.REGISTRY }}/${{ matrix.image }}
      VERSION: ${{ steps.meta.outputs.version }}
    run: |
      refs=()
      for d in *; do refs+=("$IMAGE@sha256:$d"); done
      test "${#refs[@]}" -eq 2 || { echo "expected 2 digests, got ${#refs[@]}"; exit 1; }
      tags=(-t "$IMAGE:$VERSION")
      if [ "$GITHUB_REF" = "refs/heads/main" ]; then tags+=(-t "$IMAGE:main"); fi
      docker buildx imagetools create "${tags[@]}" "${refs[@]}"
      manifest=$(docker buildx imagetools inspect --raw "$IMAGE:$VERSION")
      echo "$manifest"
      grep -q '"arm64"' <<<"$manifest"
      grep -q '"amd64"' <<<"$manifest"
  ```
  Run `scripts/dev-exec.sh 'go test ./deploy/deploytest/ -run TestImagesWorkflow -count=1 && go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/images.yml'` and expect `ok` and no actionlint output (`.github/actionlint.yaml` already lists the self-hosted runner labels).
- [ ] Write the failing test `deploy/deploytest/compose_test.go`:
  ```go
  package deploytest

  import (
  	"os"
  	"strings"
  	"testing"

  	"go.yaml.in/yaml/v3"
  )

  type composeService struct {
  	Image       string            `yaml:"image"`
  	Command     []string          `yaml:"command"`
  	User        string            `yaml:"user"`
  	Profiles    []string          `yaml:"profiles"`
  	Environment map[string]string `yaml:"environment"`
  	Ports       []string          `yaml:"ports"`
  	Sysctls     map[string]string `yaml:"sysctls"`
  	DependsOn   map[string]struct {
  		Condition string `yaml:"condition"`
  	} `yaml:"depends_on"`
  	Healthcheck *struct {
  		Test []string `yaml:"test"`
  	} `yaml:"healthcheck"`
  }

  func TestComposeExample(t *testing.T) {
  	raw, err := os.ReadFile("../compose/docker-compose.yml")
  	if err != nil {
  		t.Fatal(err)
  	}
  	var c struct {
  		Services map[string]composeService `yaml:"services"`
  	}
  	if err := yaml.Unmarshal(raw, &c); err != nil {
  		t.Fatal(err)
  	}
  	for _, s := range []string{"postgres", "volume-init", "ca-init", "migrate", "mgmt", "engine", "otel-collector"} {
  		if _, ok := c.Services[s]; !ok {
  			t.Fatalf("service %s missing", s)
  		}
  	}
  	pg, ca, mgmt, engine, otel := c.Services["postgres"], c.Services["ca-init"], c.Services["mgmt"], c.Services["engine"], c.Services["otel-collector"]
  	if pg.Healthcheck == nil || pg.Environment["POSTGRES_PASSWORD_FILE"] != "/run/secrets/postgres-password" {
  		t.Error("postgres needs a healthcheck and its password from the secret file")
  	}
  	if !strings.Contains(c.Services["migrate"].Environment["NEXORA_DATABASE_URL"], "passfile=/run/secrets/pgpass") {
  		t.Error("migrate must read the database password from the pgpass secret")
  	}
  	if strings.Join(ca.Command, " ") != "ca init --out /var/lib/nexora-ca --if-missing" || ca.DependsOn["volume-init"].Condition != "service_completed_successfully" {
  		t.Errorf("ca-init = %+v", ca)
  	}
  	if mgmt.DependsOn["migrate"].Condition != "service_completed_successfully" || mgmt.DependsOn["ca-init"].Condition != "service_completed_successfully" {
  		t.Errorf("mgmt depends_on = %+v", mgmt.DependsOn)
  	}
  	if !strings.Contains(mgmt.Image, "/nexora-mgmt:") || !strings.Contains(engine.Image, "/nexora-engine:") {
  		t.Errorf("images mgmt=%q engine=%q", mgmt.Image, engine.Image)
  	}
  	for _, k := range []string{"NEXORA_DATABASE_URL", "NEXORA_CA_CERT_FILE", "NEXORA_CA_KEY_FILE", "NEXORA_GRPC_SERVER_NAMES", "NEXORA_PUBLIC_URL"} {
  		if mgmt.Environment[k] == "" {
  			t.Errorf("mgmt environment lacks %s", k)
  		}
  	}
  	if !strings.Contains(mgmt.Environment["NEXORA_GRPC_SERVER_NAMES"], "mgmt") {
  		t.Error("the gRPC server certificate must name the compose service mgmt")
  	}
  	if len(engine.Profiles) != 1 || engine.Profiles[0] != "engine" || len(otel.Profiles) != 1 || otel.Profiles[0] != "otel" {
  		t.Errorf("profiles engine=%v otel=%v", engine.Profiles, otel.Profiles)
  	}
  	if engine.Sysctls["net.ipv4.ip_unprivileged_port_start"] != "0" {
  		t.Error("the engine runs as uid 10001 and needs net.ipv4.ip_unprivileged_port_start=0 for port 53")
  	}
  	udp := false
  	for _, p := range engine.Ports {
  		udp = udp || strings.HasSuffix(p, ":53/udp")
  	}
  	if !udp {
  		t.Errorf("engine ports %v lack DNS over UDP", engine.Ports)
  	}
  	if !strings.Contains(mgmt.Environment["NEXORA_DATABASE_URL"], "passfile=/run/secrets/pgpass") {
  		t.Error("mgmt must read the database password from the pgpass secret")
  	}
  	env, err := os.ReadFile("../compose/.env.example")
  	if err != nil || !strings.Contains(string(env), "NEXORA_TAG=") || strings.Contains(strings.ToUpper(string(env)), "PASSWORD") {
  		t.Errorf(".env.example must set NEXORA_TAG and hold no password: %v %q", err, env)
  	}
  	toml, err := os.ReadFile("../compose/engine.toml")
  	if err != nil || !strings.Contains(string(toml), `management_urls = ["https://mgmt:9443"]`) {
  		t.Errorf("engine.toml: %v %q", err, toml)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./deploy/deploytest/ -run TestComposeExample -count=1` and expect FAIL with `no such file or directory`.
- [ ] Create `deploy/compose/docker-compose.yml`:
  ```yaml
  # Nexora example: PostgreSQL, one management plane, one engine and an optional OpenTelemetry
  # Collector. See "Install with Docker Compose" in docs/operations.md. The database password lives
  # only in secrets/postgres-password and secrets/pgpass, which the operator generates.
  name: nexora

  secrets:
    postgres-password: { file: ./secrets/postgres-password }
    pgpass: { file: ./secrets/pgpass }

  services:
    postgres:
      image: postgres:17
      restart: unless-stopped
      environment:
        POSTGRES_USER: nexora
        POSTGRES_DB: nexora
        POSTGRES_PASSWORD_FILE: /run/secrets/postgres-password
      secrets: [postgres-password]
      volumes:
        - pgdata:/var/lib/postgresql/data
      healthcheck:
        test: ["CMD", "pg_isready", "-U", "nexora", "-d", "nexora"]
        interval: 5s
        timeout: 3s
        retries: 20

    # The CA volume must belong to the management plane's non-root user before ca-init writes to it.
    volume-init:
      image: busybox:1.37
      command:
        [
          "sh",
          "-c",
          "chown 65532:65532 /var/lib/nexora-ca && chmod 0700 /var/lib/nexora-ca",
        ]
      volumes:
        - ca:/var/lib/nexora-ca
      restart: "no"

    ca-init:
      image: ${NEXORA_REGISTRY:-192.168.10.131/azrtydxb}/nexora-mgmt:${NEXORA_TAG:?set NEXORA_TAG in .env}
      command: ["ca", "init", "--out", "/var/lib/nexora-ca", "--if-missing"]
      user: "65532:65532"
      volumes:
        - ca:/var/lib/nexora-ca
      depends_on:
        volume-init: { condition: service_completed_successfully }
      restart: "no"

    migrate:
      image: ${NEXORA_REGISTRY:-192.168.10.131/azrtydxb}/nexora-mgmt:${NEXORA_TAG:?set NEXORA_TAG in .env}
      command: ["migrate"]
      environment:
        NEXORA_DATABASE_URL: postgres://nexora@postgres:5432/nexora?sslmode=disable&passfile=/run/secrets/pgpass
      secrets: [pgpass]
      depends_on:
        postgres: { condition: service_healthy }
      restart: "no"

    mgmt:
      image: ${NEXORA_REGISTRY:-192.168.10.131/azrtydxb}/nexora-mgmt:${NEXORA_TAG:?set NEXORA_TAG in .env}
      command: ["serve"]
      user: "65532:65532"
      restart: unless-stopped
      environment:
        NEXORA_DATABASE_URL: postgres://nexora@postgres:5432/nexora?sslmode=disable&passfile=/run/secrets/pgpass
        NEXORA_CA_CERT_FILE: /var/lib/nexora-ca/ca.crt
        NEXORA_CA_KEY_FILE: /var/lib/nexora-ca/ca.key
        NEXORA_GRPC_SERVER_NAMES: mgmt,localhost,${NEXORA_PUBLIC_HOST:-localhost}
        NEXORA_PUBLIC_URL: ${NEXORA_PUBLIC_URL:-http://localhost:8080}
        NEXORA_SECURE_COOKIES: ${NEXORA_SECURE_COOKIES:-false}
        NEXORA_QUERYLOG_BACKEND: builtin
        NEXORA_OTLP_ENDPOINT: ${NEXORA_OTLP_ENDPOINT:-}
      secrets: [pgpass]
      ports:
        - "${NEXORA_HTTP_PORT:-8080}:8080"
        - "${NEXORA_GRPC_PORT:-9443}:9443"
      volumes:
        - ca:/var/lib/nexora-ca:ro
      depends_on:
        ca-init: { condition: service_completed_successfully }
        migrate: { condition: service_completed_successfully }

    engine:
      image: ${NEXORA_REGISTRY:-192.168.10.131/azrtydxb}/nexora-engine:${NEXORA_TAG:?set NEXORA_TAG in .env}
      profiles: ["engine"]
      restart: unless-stopped
      command: ["--config", "/etc/nexora/engine.toml"]
      environment:
        NEXORA_ENGINE_NODE_NAME: ${NEXORA_ENGINE_NODE_NAME:-compose-engine-1}
      read_only: true
      cap_drop: [ALL]
      sysctls:
        net.ipv4.ip_unprivileged_port_start: "0"
      ports:
        - "${NEXORA_DNS_PORT:-53}:53/udp"
        - "${NEXORA_DNS_PORT:-53}:53/tcp"
        - "${NEXORA_METRICS_PORT:-9153}:9153"
      volumes:
        - ./engine.toml:/etc/nexora/engine.toml:ro
        - ./secrets/join-token:/etc/nexora/join-token:ro
        - enginestate:/var/lib/nexora
      depends_on:
        mgmt: { condition: service_started }

    otel-collector:
      image: otel/opentelemetry-collector-contrib:0.160.0
      profiles: ["otel"]
      restart: unless-stopped
      command: ["--config", "/etc/otelcol/config.yaml"]
      volumes:
        - ./otel-collector.yaml:/etc/otelcol/config.yaml:ro
      ports:
        - "4317:4317"

  volumes:
    pgdata: {}
    ca: {}
    enginestate: {}
  ```
- [ ] Create `deploy/compose/engine.toml`:
  ```toml
  # Engine bootstrap for the compose example; configuration arrives from mgmt.
  node_name = "compose-engine-1"            # overridden by NEXORA_ENGINE_NODE_NAME
  state_dir = "/var/lib/nexora"
  management_urls = ["https://mgmt:9443"]
  join_token_file = "/etc/nexora/join-token"
  listen_udp = ["0.0.0.0:53"]
  listen_tcp = ["0.0.0.0:53"]
  metrics_listen = "0.0.0.0:9153"
  workers = 0
  ```
- [ ] Create `deploy/compose/otel-collector.yaml`:
  ```yaml
  receivers:
    otlp:
      protocols:
        grpc: { endpoint: 0.0.0.0:4317 }
  processors:
    batch: {}
    memory_limiter: { check_interval: 1s, limit_mib: 256 }
  exporters:
    debug: { verbosity: basic }
  service:
    pipelines:
      logs:
        {
          receivers: [otlp],
          processors: [memory_limiter, batch],
          exporters: [debug],
        }
      traces:
        {
          receivers: [otlp],
          processors: [memory_limiter, batch],
          exporters: [debug],
        }
      metrics:
        {
          receivers: [otlp],
          processors: [memory_limiter, batch],
          exporters: [debug],
        }
  ```
- [ ] Create `deploy/compose/.env.example` (no credentials; the database secret files are generated as described in `docs/operations.md`):
  ```dotenv
  # Copy to .env.
  NEXORA_TAG=main
  NEXORA_REGISTRY=192.168.10.131/azrtydxb
  NEXORA_PUBLIC_URL=http://localhost:8080
  NEXORA_PUBLIC_HOST=localhost
  NEXORA_SECURE_COOKIES=false
  NEXORA_DNS_PORT=53
  NEXORA_HTTP_PORT=8080
  NEXORA_GRPC_PORT=9443
  NEXORA_METRICS_PORT=9153
  NEXORA_ENGINE_NODE_NAME=compose-engine-1
  NEXORA_OTLP_ENDPOINT=
  ```
  and `deploy/compose/secrets/.gitignore` with the two lines `*` and `!.gitignore`, so the directory exists without committing the join token or the database secret files. pgx reads `passfile` (libpq `.pgpass` format `host:port:database:user:secret`) and does not require mode 0600, so the bind-mounted file stays readable by uid 65532.
- [ ] Run `scripts/dev-exec.sh go test ./deploy/deploytest/ -count=1` and expect `ok`.
- [ ] Commit: `git add .github/workflows/images.yml deploy/compose deploy/deploytest && git commit -m "build: main tag and manifest check for release images; docker-compose example"`.

## Task 13: Helm chart

Files: `deploy/helm/nexora/Chart.yaml`, `deploy/helm/nexora/values.yaml`, `deploy/helm/nexora/values.schema.json`, `deploy/helm/nexora/templates/_helpers.tpl`, `templates/mgmt-deployment.yaml`, `templates/mgmt-services.yaml` (http and gRPC ClusterIP, optional gRPC LoadBalancer), `templates/mgmt-ingress.yaml`, `templates/mgmt-pdb.yaml`, `templates/engine-configmap.yaml`, `templates/engine-workloads.yaml` (one DaemonSet or Deployment per entry of `engine.groups`), `templates/engine-services.yaml` (per-group DNS Service, metrics Service), `templates/database-cnpg.yaml`, `templates/servicemonitor.yaml`, `templates/prometheusrule.yaml`, `templates/otel-collector.yaml` (optional collector), `templates/NOTES.txt`, `deploy/helm/nexora/ci/lint-values.yaml`, `deploy/kw/values-kw.yaml` (kw release values), `deploy/deploytest/helm_test.go` (`TestHelmTemplate`), `Makefile` (`GO_PKGS` includes `deploy`), `.prettierignore` (Helm templates are not YAML)
Interfaces: for release `R` the name prefix `F` is `R` when `R` contains `nexora`, else `R-nexora`; objects `F-mgmt` (Deployment, http Service, Ingress unless `mgmt.ingress.name` is set, PodDisruptionBudget), `F-mgmt-grpc` (ClusterIP Service), `F-mgmt-lb` (LoadBalancer Service when `mgmt.grpcLoadBalancer.enabled`), per engine group entry `workloadName` (default `F-engine-<name>`) for the ConfigMap and workload and `service.name` (default `F-dns-<name>`) for the DNS Service, `F-engine-metrics` (Service), CNPG `Cluster` `database.cnpg.clusterName`, `F` (ServiceMonitor, PrometheusRule); pod labels `app.kubernetes.io/name` (`nexora-mgmt` or `nexora-engine`), `app.kubernetes.io/instance`, `nexora.io/engine-group`.

- [ ] Write the failing test `deploy/deploytest/helm_test.go`:
  ```go
  package deploytest

  import (
  	"bytes"
  	"errors"
  	"io"
  	"os/exec"
  	"strings"
  	"testing"

  	"go.yaml.in/yaml/v3"
  )

  const chartDir = "../helm/nexora"

  func helm(args ...string) (string, error) {
  	out, err := exec.Command("helm", args...).CombinedOutput() // nosemgrep: dangerous-exec-command
  	return string(out), err
  }

  type obj map[string]any

  func (o obj) path(keys ...string) any {
  	var cur any = map[string]any(o)
  	for _, k := range keys {
  		m, ok := cur.(map[string]any)
  		if !ok {
  			return nil
  		}
  		cur = m[k]
  	}
  	return cur
  }

  func render(t *testing.T, args ...string) []obj {
  	t.Helper()
  	out, err := helm(append([]string{"template", "nexora", chartDir, "--namespace", "nexora"}, args...)...)
  	if err != nil {
  		t.Fatalf("helm template: %v\n%s", err, out)
  	}
  	var docs []obj
  	dec := yaml.NewDecoder(bytes.NewBufferString(out))
  	for {
  		var o map[string]any // unnamed: yaml.v3 gives nested mappings the target map type
  		if err := dec.Decode(&o); errors.Is(err, io.EOF) {
  			break
  		} else if err != nil {
  			t.Fatalf("decode: %v", err)
  		}
  		if o != nil {
  			docs = append(docs, obj(o))
  		}
  	}
  	return docs
  }

  func find(t *testing.T, docs []obj, kind, name string) obj {
  	t.Helper()
  	for _, d := range docs {
  		if d["kind"] == kind && d.path("metadata", "name") == name {
  			return d
  		}
  	}
  	t.Fatalf("%s/%s not rendered", kind, name)
  	return nil
  }

  func has(docs []obj, kind string) bool {
  	for _, d := range docs {
  		if d["kind"] == kind {
  			return true
  		}
  	}
  	return false
  }

  func container(t *testing.T, workload obj, name string) obj {
  	t.Helper()
  	for _, c := range workload.path("spec", "template", "spec", "containers").([]any) {
  		if c.(map[string]any)["name"] == name {
  			return obj(c.(map[string]any))
  		}
  	}
  	t.Fatalf("container %s missing", name)
  	return nil
  }

  func env(c obj, name string) obj {
  	for _, e := range c["env"].([]any) {
  		if m := e.(map[string]any); m["name"] == name {
  			return obj(m)
  		}
  	}
  	return nil
  }

  func TestHelmTemplate(t *testing.T) {
  	if out, err := helm("lint", chartDir, "--strict", "-f", chartDir+"/ci/lint-values.yaml"); err != nil {
  		t.Fatalf("helm lint: %v\n%s", err, out)
  	}

  	kw := render(t, "-f", "../kw/values-kw.yaml", "--set", "image.tag=sha-0000000", "--api-versions", "monitoring.coreos.com/v1")
  	mgmt := find(t, kw, "Deployment", "nexora-mgmt")
  	if mgmt.path("spec", "replicas") != 2 {
  		t.Errorf("mgmt replicas = %v", mgmt.path("spec", "replicas"))
  	}
  	mc := container(t, mgmt, "mgmt")
  	if ref := env(mc, "NEXORA_DATABASE_URL").path("valueFrom", "secretKeyRef"); ref == nil || ref.(map[string]any)["name"] != "nexora-db-app" || ref.(map[string]any)["key"] != "uri" {
  		t.Errorf("database env = %v", env(mc, "NEXORA_DATABASE_URL"))
  	}
  	for name, want := range map[string]string{
  		"NEXORA_KEK_FILE": "/etc/nexora/kek/kek", "NEXORA_DNS_TLS_CERT_FILE": "/etc/nexora/dns-tls/tls.crt",
  		"NEXORA_QUERYLOG_BACKEND": "opensearch", "NEXORA_SECURE_COOKIES": "true", "NEXORA_PUBLIC_URL": "https://nexora.kw.local",
  	} {
  		if e := env(mc, name); e == nil || e["value"] != want {
  			t.Errorf("mgmt %s = %v, want %s", name, e, want)
  		}
  	}
  	if names := env(mc, "NEXORA_GRPC_SERVER_NAMES"); names == nil || !strings.Contains(names["value"].(string), "192.168.10.135") ||
  		!strings.Contains(names["value"].(string), "nexora-mgmt-grpc.nexora.svc.cluster.local") {
  		t.Errorf("gRPC server names = %v", names)
  	}
  	if probe := mc.path("readinessProbe", "httpGet", "path"); probe != "/api/v1/health" {
  		t.Errorf("mgmt readiness path = %v", probe)
  	}
  	if init := mgmt.path("spec", "template", "spec", "initContainers").([]any)[0].(map[string]any); init["args"].([]any)[0] != "migrate" {
  		t.Errorf("migrate init container = %v", init)
  	}
  	if lb := find(t, kw, "Service", "nexora-mgmt-lb"); lb.path("spec", "loadBalancerIP") != "192.168.10.135" || len(lb.path("spec", "ports").([]any)) != 1 {
  		t.Errorf("gRPC load balancer = %v", lb["spec"])
  	}
  	find(t, kw, "Service", "nexora-mgmt-grpc")
  	ing := find(t, kw, "Ingress", "nexora")
  	if ing.path("spec", "ingressClassName") != "nginx" || ing.path("metadata", "annotations", "cert-manager.io/cluster-issuer") != "cluster-ca" ||
  		ing.path("spec", "tls").([]any)[0].(map[string]any)["secretName"] != "nexora-ingress-tls" {
  		t.Errorf("ingress = %v", ing)
  	}

  	for _, g := range []struct{ workload, service, ip, policy, prefix, token, op string }{
  		{"nexora-engine", "nexora-dns", "192.168.10.136", "Local", "", "nexora-join-token", "NotIn"},
  		{"nexora-engine-edge-b", "nexora-dns-edge-b", "192.168.10.137", "Cluster", "edge-b-", "nexora-join-token-edge-b", "In"},
  	} {
  		ds := find(t, kw, "DaemonSet", g.workload)
  		spec := obj(ds.path("spec", "template", "spec").(map[string]any))
  		term := spec.path("affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms").([]any)[0].(map[string]any)
  		expr := term["matchExpressions"].([]any)[0].(map[string]any)
  		if expr["key"] != "nexora.io/engine-group" || expr["operator"] != g.op {
  			t.Errorf("%s node affinity = %v", g.workload, expr)
  		}
  		ec := container(t, ds, "engine")
  		if nn := env(ec, "NEXORA_ENGINE_NODE_NAME"); nn == nil || nn["value"] != g.prefix+"$(K8S_NODE_NAME)" {
  			t.Errorf("%s node name env = %v", g.workload, nn)
  		}
  		var hostPath, tokenSecret any
  		for _, v := range spec["volumes"].([]any) {
  			m := obj(v.(map[string]any))
  			switch m["name"] {
  			case "state":
  				hostPath = m.path("hostPath", "path")
  			case "join":
  				tokenSecret = m.path("secret", "secretName")
  			}
  		}
  		if hostPath != "/var/lib/nexora/"+g.workload || tokenSecret != g.token {
  			t.Errorf("%s state %v join secret %v", g.workload, hostPath, tokenSecret)
  		}
  		svc := find(t, kw, "Service", g.service)
  		if svc.path("spec", "loadBalancerIP") != g.ip || svc.path("spec", "externalTrafficPolicy") != g.policy {
  			t.Errorf("%s = %v", g.service, svc["spec"])
  		}
  		if len(svc.path("spec", "ports").([]any)) != 5 {
  			t.Errorf("%s ports = %v, want dns-udp, dns-tcp, dot, doq, doh", g.service, svc.path("spec", "ports"))
  		}
  	}
  	find(t, kw, "Service", "nexora-engine-metrics")
  	if has(kw, "Cluster") {
  		t.Error("kw uses its existing CNPG cluster (external database mode)")
  	}
  	sm := find(t, kw, "ServiceMonitor", "nexora")
  	if sm.path("metadata", "namespace") != "monitoring" || sm.path("metadata", "labels", "release") != "kps" {
  		t.Errorf("service monitor metadata = %v", sm["metadata"])
  	}
  	rule, _ := yaml.Marshal(find(t, kw, "PrometheusRule", "nexora"))
  	for _, want := range []string{"max(nexora_mgmt_engines_disconnected", `nexora_mgmt_rollouts{state="halted",namespace="nexora"}`, "NexoraManagementPlaneDown"} {
  		if !strings.Contains(string(rule), want) {
  			t.Errorf("prometheus rule lacks %q:\n%s", want, rule)
  		}
  	}

  	cnpg := render(t, "--set", "mgmt.ca.existingSecret=ca", "--api-versions", "postgresql.cnpg.io/v1",
  		"--set", "engine.kind=Deployment", "--set-json", `engine.groups=[{"name":"default","replicas":2,"joinTokenSecret":"jt"}]`)
  	if c := find(t, cnpg, "Cluster", "nexora-db"); c.path("spec", "instances") != 2 {
  		t.Errorf("cnpg cluster = %v", c["spec"])
  	}
  	if d := find(t, cnpg, "Deployment", "nexora-engine-default"); d.path("spec", "replicas") != 2 {
  		t.Errorf("engine deployment replicas = %v", d.path("spec", "replicas"))
  	}
  	if has(cnpg, "ServiceMonitor") || has(cnpg, "Ingress") {
  		t.Error("disabled monitoring and ingress must not render")
  	}

  	for _, bad := range []struct {
  		args []string
  		want string
  	}{
  		{[]string{"--set", "mgmt.ca.existingSecret=ca", "--set", "database.mode=external", "--set", "database.external.existingSecret=pg", "--set", "mgmt.replicas=0"}, "replicas"},
  		{[]string{"--set", "database.mode=external", "--set", "database.external.existingSecret=pg", "--set-json", `engine.groups=[{"name":"default","joinTokenSecret":"jt"}]`}, "mgmt.ca.existingSecret"},
  		{[]string{"--set", "mgmt.ca.existingSecret=ca", "--set", "database.mode=external", "--set", "database.external.existingSecret=pg", "--set", "engine.kind=StatefulSet"}, "kind"},
  		{[]string{"--set", "mgmt.ca.existingSecret=ca", "--set-json", `engine.groups=[{"name":"default","joinTokenSecret":"jt"}]`}, "postgresql.cnpg.io/v1"},
  		{[]string{"--set", "mgmt.ca.existingSecret=ca", "--set", "database.mode=external", "--set", "database.external.existingSecret=pg"}, "joinTokenSecret is required"},
  	} {
  		out, err := helm(append([]string{"template", "nexora", chartDir}, bad.args...)...)
  		if err == nil || !strings.Contains(out, bad.want) {
  			t.Errorf("helm template %v: err=%v, want failure mentioning %q; output:\n%s", bad.args, err, bad.want, out)
  		}
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./deploy/deploytest/ -run TestHelmTemplate -count=1` and expect FAIL with `helm lint:` (`deploy/helm/nexora` does not exist).
- [ ] Create `deploy/helm/nexora/Chart.yaml`:
  ```yaml
  apiVersion: v2
  name: nexora
  description: Nexora DNS management plane and engine fleet
  type: application
  version: 1.0.0
  appVersion: "main"
  kubeVersion: ">=1.28.0-0"
  annotations:
    nexora.io/requires: "CloudNativePG operator (postgresql.cnpg.io/v1) when database.mode=cnpg; Prometheus Operator CRDs when metrics are enabled"
  ```
- [ ] Create `deploy/helm/nexora/values.yaml`:
  ```yaml
  image:
    registry: 192.168.10.131/azrtydxb
    tag: "" # defaults to .Chart.AppVersion
    pullPolicy: IfNotPresent
  imagePullSecrets: []

  mgmt:
    replicas: 2
    publicURL: ""
    secureCookies: true
    engineCertTTL: 2160h
    rolloutTick: 1s
    grpcServerNames: [] # extra SANs; the gRPC Service names and the load balancer IP are always included
    otlpEndpoint: ""
    ca:
      existingSecret: "" # required: secret with ca.crt and ca.key (nexora-mgmt ca init)
    kek:
      existingSecret: "" # optional: secret with key "kek" (32 random bytes, base64)
    dnsTLS:
      existingSecret: "" # optional: kubernetes.io/tls secret for DoT/DoH/DoQ, pushed to engines
      reloadInterval: 30s
    querylog:
      backend: builtin # builtin | opensearch
      builtinCapacity: 200000
      opensearch:
        url: ""
        index: nexora-querylog-*
    extraEnv: []
    extraVolumes: []
    extraVolumeMounts: []
    resources:
      requests: { cpu: 100m, memory: 128Mi }
      limits: { cpu: "1", memory: 512Mi }
    grpcLoadBalancer:
      enabled: false
      loadBalancerIP: ""
    ingress:
      enabled: false
      name: "" # default <prefix>-mgmt
      className: nginx
      host: ""
      clusterIssuer: ""
      tlsSecretName: ""
    pdb:
      minAvailable: 1

  database:
    mode: cnpg # cnpg | external
    external:
      existingSecret: ""
      key: uri
    cnpg:
      clusterName: nexora-db
      instances: 2
      imageName: ghcr.io/cloudnative-pg/postgresql:17.6
      storageClass: ""
      size: 10Gi

  engine:
    enabled: true
    kind: DaemonSet # DaemonSet | Deployment
    managementURL: "" # default https://<prefix>-mgmt-grpc.<namespace>.svc.cluster.local:9443
    workers: 2
    initImage: busybox:1.37
    ports: { dns: 53, metrics: 9153, dot: 0, doh: 0, doq: 0 } # 0 disables an encrypted listener
    dohPath: /dns-query
    stateDir:
      type: hostPath # hostPath | emptyDir (emptyDir enrolls again after every pod restart)
      hostPathPrefix: /var/lib/nexora
    tolerations: []
    resources:
      requests: { cpu: 250m, memory: 256Mi }
      limits: { cpu: "2", memory: 1Gi }
    groups:
      - name: default
        workloadName: "" # default <prefix>-engine-<name>
        nodeNamePrefix: "" # NEXORA_ENGINE_NODE_NAME = <nodeNamePrefix><Kubernetes node name>
        replicas: 1 # Deployment only
        nodeAffinity: {}
        joinTokenSecret: ""
        joinTokenKey: join-token
        service:
          name: "" # default <prefix>-dns-<name>
          type: LoadBalancer
          loadBalancerIP: ""
          externalTrafficPolicy: Local

  metrics:
    serviceMonitor: { enabled: false, namespace: "", labels: {}, interval: 30s }
    prometheusRule: { enabled: false, namespace: "", labels: {} }
  ```
- [ ] Create `deploy/helm/nexora/values.schema.json` (draft-07): `required: ["image", "mgmt", "database", "engine"]`; `mgmt.replicas` integer `minimum: 1`; `mgmt.ca.existingSecret` string; `mgmt.engineCertTTL` and `mgmt.rolloutTick` strings matching `^([0-9]+(ms|h|m|s))+$`; `mgmt.querylog.backend` enum `builtin|opensearch`; `database.mode` enum `cnpg|external` with `if mode = external then external.existingSecret minLength 1`; `engine.kind` enum `DaemonSet|Deployment`; `engine.stateDir.type` enum `hostPath|emptyDir`, `hostPathPrefix` pattern `^/`; `engine.ports.*` integers 0..65535 (`dns`, `metrics` minimum 1); `engine.groups` array `minItems: 1` of objects with required `name` (the same pattern as `EngineGroupInput.name` in `mgmt/api/openapi.yaml`), `replicas` integer `minimum: 0`, `joinTokenSecret` string, `service.type` enum `ClusterIP|NodePort|LoadBalancer`, `service.externalTrafficPolicy` enum `Local|Cluster`.
- [ ] Create `deploy/helm/nexora/templates/_helpers.tpl` with: `nexora.prefix` (`.Release.Name` when it contains `nexora`, else `<release>-nexora`, truncated to 50); `nexora.selectorLabels` (dict `root`, `name`: `app.kubernetes.io/name: <name>`, `app.kubernetes.io/instance: <release>`); `nexora.labels` (selector labels plus `app.kubernetes.io/managed-by`, `app.kubernetes.io/version`, `helm.sh/chart`); `nexora.image` (dict `root`, `name` -> `<image.registry>/<name>:<image.tag or AppVersion>`); `nexora.containerSecurity` (`allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, `capabilities.drop: [ALL]`); `nexora.databaseEnv` (`NEXORA_DATABASE_URL` from `<cnpg.clusterName>-app`/`uri` in cnpg mode, else `required "database.external.existingSecret is required when database.mode=external"` with `database.external.key`); `nexora.managementURL` (default `https://<prefix>-mgmt-grpc.<namespace>.svc.cluster.local:9443`); `nexora.grpcServerNames` (`<prefix>-mgmt-grpc`, `<prefix>-mgmt-grpc.<namespace>.svc`, `<prefix>-mgmt-grpc.<namespace>.svc.cluster.local`, the load balancer IP when set, then `mgmt.grpcServerNames`, comma-joined).
- [ ] Create `deploy/helm/nexora/templates/mgmt-deployment.yaml`: fail with `database.mode=cnpg needs the CloudNativePG operator (API postgresql.cnpg.io/v1); install it or set database.mode=external` when `database.mode` is `cnpg` and `.Capabilities.APIVersions.Has "postgresql.cnpg.io/v1"` is false; Deployment `<prefix>-mgmt` with `replicas`, pod security context `runAsNonRoot: true, runAsUser: 65532, fsGroup: 65532, seccompProfile RuntimeDefault`, preferred host anti-affinity; init container `migrate` (`args: ["migrate"]`, database env); container `mgmt` with `args: ["serve"]`, ports `http` 8080 and `grpc` 9443, env `NEXORA_HTTP_LISTEN=:8080`, `NEXORA_GRPC_LISTEN=:9443`, database env, `NEXORA_CA_CERT_FILE=/etc/nexora/ca/ca.crt`, `NEXORA_CA_KEY_FILE=/etc/nexora/ca/ca.key` (volume from `required "mgmt.ca.existingSecret is required (create it with nexora-mgmt ca init)"`), `NEXORA_GRPC_SERVER_NAMES`, `NEXORA_PUBLIC_URL`, `NEXORA_SECURE_COOKIES`, `NEXORA_ENGINE_CERT_TTL`, `NEXORA_ROLLOUT_TICK`, `NEXORA_QUERYLOG_BACKEND`, `NEXORA_QUERYLOG_BUILTIN_CAPACITY`, with opensearch `NEXORA_OPENSEARCH_URL` (required) and `NEXORA_OPENSEARCH_INDEX`, `NEXORA_OTLP_ENDPOINT` when set, with `mgmt.kek.existingSecret` a `kek` volume at `/etc/nexora/kek` and `NEXORA_KEK_FILE=/etc/nexora/kek/kek`, with `mgmt.dnsTLS.existingSecret` a `dns-tls` volume at `/etc/nexora/dns-tls`, `NEXORA_DNS_TLS_CERT_FILE=/etc/nexora/dns-tls/tls.crt`, `NEXORA_DNS_TLS_KEY_FILE=/etc/nexora/dns-tls/tls.key` and `NEXORA_DNS_TLS_RELOAD_INTERVAL`, then `mgmt.extraEnv`; readiness `httpGet /api/v1/health` on `http` every 5 s, liveness `tcpSocket grpc` every 10 s; secret volumes `defaultMode: 0440`; `mgmt.extraVolumes`/`extraVolumeMounts`.
- [ ] Create `templates/mgmt-services.yaml` (`<prefix>-mgmt` ClusterIP port `http` 8080; `<prefix>-mgmt-grpc` ClusterIP port `grpc` 9443; when `mgmt.grpcLoadBalancer.enabled`, `<prefix>-mgmt-lb` `type: LoadBalancer` with `loadBalancerIP` and only the `grpc` port, so the GUI is never reachable in cleartext there), `templates/mgmt-ingress.yaml` (when enabled: name `mgmt.ingress.name` or `<prefix>-mgmt`, annotations `cert-manager.io/cluster-issuer` when set plus `nginx.ingress.kubernetes.io/ssl-redirect: "true"` and `force-ssl-redirect: "true"`, `ingressClassName`, TLS host `required "mgmt.ingress.host is required"` with `tlsSecretName` or `<prefix>-mgmt-tls`, backend `<prefix>-mgmt` port `http`) and `templates/mgmt-pdb.yaml` (when `replicas > 1`, `minAvailable`).
- [ ] Create `templates/engine-configmap.yaml` (like `engine-workloads.yaml` and `engine-services.yaml`, the whole file is wrapped in `{{- if .Values.engine.enabled }}`): for each group, ConfigMap `<workload>` with `engine.toml`:
  ```yaml
  {{- range $g := .Values.engine.groups }}
  {{- $workload := default (printf "%s-engine-%s" (include "nexora.prefix" $) $g.name) $g.workloadName }}
  ---
  apiVersion: v1
  kind: ConfigMap
  metadata:
    name: {{ $workload }}
    labels: {{- include "nexora.labels" (dict "root" $ "name" "nexora-engine") | nindent 4 }}
  data:
    engine.toml: |
      node_name = "engine"
      state_dir = "/var/lib/nexora"
      management_urls = [{{ include "nexora.managementURL" $ | quote }}]
      join_token_file = "/etc/nexora/join/{{ $g.joinTokenKey | default "join-token" }}"
      listen_udp = ["0.0.0.0:{{ $.Values.engine.ports.dns }}"]
      listen_tcp = ["0.0.0.0:{{ $.Values.engine.ports.dns }}"]
      {{- with $.Values.engine.ports.dot }}
      listen_dot = ["0.0.0.0:{{ . }}"]
      {{- end }}
      {{- with $.Values.engine.ports.doh }}
      listen_doh = ["0.0.0.0:{{ . }}"]
      doh_path = {{ $.Values.engine.dohPath | quote }}
      {{- end }}
      {{- with $.Values.engine.ports.doq }}
      listen_doq = ["0.0.0.0:{{ . }}"]
      {{- end }}
      metrics_listen = "0.0.0.0:{{ $.Values.engine.ports.metrics }}"
      workers = {{ $.Values.engine.workers }}
  {{- end }}
  ```
  (`node_name` is always replaced by `NEXORA_ENGINE_NODE_NAME`.)
- [ ] Create `templates/engine-workloads.yaml`: for each group a `{{ .Values.engine.kind }}` named `<workload>` (Deployment: `replicas`, `maxSurge: 0`, `maxUnavailable: 1`; DaemonSet: `RollingUpdate maxUnavailable: 1`), selector labels `app.kubernetes.io/name: nexora-engine`, `app.kubernetes.io/instance`, `nexora.io/engine-group: <name>`, annotation `checksum/config` of the rendered ConfigMap; pod `securityContext` `runAsNonRoot: true, runAsUser: 10001, fsGroup: 10001, seccompProfile RuntimeDefault, sysctls: [{ name: net.ipv4.ip_unprivileged_port_start, value: "0" }]`; `affinity.nodeAffinity` from the group's `nodeAffinity`; `tolerations` from `engine.tolerations`; with `stateDir.type: hostPath` an init container `state-owner` (`engine.initImage`, `command: ["sh", "-c", "chown 10001:10001 /var/lib/nexora && chmod 0700 /var/lib/nexora"]`, `securityContext: { runAsNonRoot: false, runAsUser: 0, allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: { drop: [ALL], add: [CHOWN, FOWNER] } }`); container `engine` (`args: ["--config", "/etc/nexora/engine.toml"]`, env `K8S_NODE_NAME` from `spec.nodeName` and `NEXORA_ENGINE_NODE_NAME` = `<nodeNamePrefix>$(K8S_NODE_NAME)`, ports `dns-udp`/`dns-tcp`/`metrics` plus `dot` (TCP), `doh` (TCP), `doq` (UDP) when non-zero, readiness `tcpSocket dns-tcp` 5 s, liveness `httpGet /metrics` on `metrics` 10 s, `nexora.containerSecurity`, mounts `config` at `/etc/nexora/engine.toml` (`subPath: engine.toml`), `join` at `/etc/nexora/join` read-only, `state` at `/var/lib/nexora`); volumes `config` (ConfigMap `<workload>`), `join` (secret `required (printf "engine.groups[%s].joinTokenSecret is required" .name)`, `defaultMode: 0440`), `state` (`hostPath: { path: <hostPathPrefix>/<workload>, type: DirectoryOrCreate }` or `emptyDir: {}`).
- [ ] Create `templates/engine-services.yaml`: `<prefix>-engine-metrics` (ClusterIP, selector `app.kubernetes.io/name: nexora-engine` and instance, port `metrics`, label `nexora.io/metrics: "true"`) and, per group, Service `service.name` or `<prefix>-dns-<name>` (`type`, `loadBalancerIP` when set, `externalTrafficPolicy` unless `ClusterIP`, selector including `nexora.io/engine-group`, ports `dns-udp` 53/UDP, `dns-tcp` 53/TCP, and when enabled `dot` 853/TCP, `doq` 853/UDP, `doh` 443/TCP targeting the named container ports).
- [ ] Create `templates/database-cnpg.yaml` (cnpg mode: `postgresql.cnpg.io/v1` `Cluster` `clusterName` with `instances`, `imageName`, `bootstrap.initdb` database and owner `nexora`, `storage.size` and `storageClass` when set), `templates/servicemonitor.yaml` (when enabled: `monitoring.coreos.com/v1` `ServiceMonitor` `<prefix>` in `metrics.serviceMonitor.namespace` or the release namespace, labels from values, `namespaceSelector.matchNames: [<release namespace>]`, selector `app.kubernetes.io/instance` plus `nexora.io/metrics: "true"` (added to the `<prefix>-mgmt` Service and the metrics Service), endpoints `{ port: http, path: /metrics }` and `{ port: metrics, path: /metrics }` at `interval`), `templates/prometheusrule.yaml`:
  ```yaml
  {{- if .Values.metrics.prometheusRule.enabled }}
  apiVersion: monitoring.coreos.com/v1
  kind: PrometheusRule
  metadata:
    name: {{ include "nexora.prefix" . }}
    namespace: {{ default .Release.Namespace .Values.metrics.prometheusRule.namespace }}
    labels:
      {{- include "nexora.labels" (dict "root" . "name" "nexora") | nindent 4 }}
      {{- with .Values.metrics.prometheusRule.labels }}
      {{- toYaml . | nindent 4 }}
      {{- end }}
  spec:
    groups:
      - name: nexora-fleet
        rules:
          - alert: NexoraEngineDisconnected
            expr: max(nexora_mgmt_engines_disconnected{namespace="{{ .Release.Namespace }}"}) > 0
            for: 2m
            labels: { severity: warning }
            annotations:
              summary: "{{ `{{ $value }}` }} Nexora engine(s) without a control stream for more than 60 seconds"
          - alert: NexoraRolloutHalted
            expr: max(nexora_mgmt_rollouts{state="halted",namespace="{{ .Release.Namespace }}"}) > 0
            labels: { severity: warning }
            annotations:
              summary: A Nexora configuration rollout halted on its health gate; roll back or fix forward
          - alert: NexoraManagementPlaneDown
            expr: absent(up{namespace="{{ .Release.Namespace }}",service="{{ include "nexora.prefix" . }}-mgmt"} == 1)
            for: 2m
            labels: { severity: critical }
            annotations:
              summary: No Nexora management plane instance is being scraped
  {{- end }}
  ```
  and `templates/NOTES.txt` (the release prefix, that the first admin is created through `/setup` with the setup token logged by `nexora-mgmt`, and that each engine group needs a join token secret: `kubectl exec deploy/<prefix>-mgmt -c mgmt -- /nexora-mgmt join-token create --engine-group <group> --ttl 24h` with `NEXORA_DATABASE_URL` and `NEXORA_CA_CERT_FILE` already set in that container).
- [ ] Create `deploy/helm/nexora/ci/lint-values.yaml`:
  ```yaml
  mgmt:
    ca: { existingSecret: nexora-ca }
  database:
    mode: external
    external: { existingSecret: nexora-db, key: uri }
  engine:
    groups:
      - name: default
        joinTokenSecret: nexora-join-default
  ```
- [ ] Create `deploy/kw/values-kw.yaml`:
  ```yaml
  # kw release (scripts/kw-deploy.sh): helm upgrade --install nexora deploy/helm/nexora -n nexora -f deploy/kw/values-kw.yaml --set image.tag=<tag>
  image:
    registry: 192.168.10.131/azrtydxb
    pullPolicy: Always # kw tags are rebuilt in place
  mgmt:
    replicas: 2
    publicURL: https://nexora.kw.local
    secureCookies: true
    grpcServerNames: [nexora-mgmt-grpc.nexora.svc]
    otlpEndpoint: http://nexora-otelcol.nexora.svc.cluster.local:4317
    ca: { existingSecret: nexora-ca }
    kek: { existingSecret: nexora-kek }
    dnsTLS: { existingSecret: nexora-dns-tls, reloadInterval: 30s }
    querylog:
      backend: opensearch
      opensearch:
        {
          url: "http://opensearch.nexora.svc.cluster.local:9200",
          index: "nexora-querylog-*",
        }
    grpcLoadBalancer: { enabled: true, loadBalancerIP: 192.168.10.135 }
    ingress:
      enabled: true
      name: nexora
      className: nginx
      host: nexora.kw.local
      clusterIssuer: cluster-ca
      tlsSecretName: nexora-ingress-tls
  database:
    mode: external
    external: { existingSecret: nexora-db-app, key: uri }
  engine:
    kind: DaemonSet
    workers: 2
    initImage: 192.168.10.131/library/busybox:1.37
    ports: { dns: 53, metrics: 9153, dot: 853, doh: 443, doq: 853 }
    stateDir: { type: hostPath, hostPathPrefix: /var/lib/nexora }
    # kube-vip announces the VIPs from a control-plane node; with externalTrafficPolicy Local that
    # node must run a default-group engine.
    tolerations:
      - {
          key: node-role.kubernetes.io/control-plane,
          operator: Exists,
          effect: NoSchedule,
        }
      - {
          key: node-role.kubernetes.io/master,
          operator: Exists,
          effect: NoSchedule,
        }
    groups:
      - name: default
        workloadName: nexora-engine
        nodeNamePrefix: ""
        joinTokenSecret: nexora-join-token
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
              - matchExpressions:
                  - {
                      key: nexora.io/engine-group,
                      operator: NotIn,
                      values: [edge-b],
                    }
        service:
          {
            name: nexora-dns,
            type: LoadBalancer,
            loadBalancerIP: 192.168.10.136,
            externalTrafficPolicy: Local,
          }
      - name: edge-b
        workloadName: nexora-engine-edge-b
        nodeNamePrefix: edge-b-
        joinTokenSecret: nexora-join-token-edge-b
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
              - matchExpressions:
                  - {
                      key: nexora.io/engine-group,
                      operator: In,
                      values: [edge-b],
                    }
        # The kube-vip node runs no edge-b engine, so Local would drop external traffic to .137.
        service:
          {
            name: nexora-dns-edge-b,
            type: LoadBalancer,
            loadBalancerIP: 192.168.10.137,
            externalTrafficPolicy: Cluster,
          }
  metrics:
    serviceMonitor:
      {
        enabled: true,
        namespace: monitoring,
        labels: { release: kps },
        interval: 30s,
      }
    prometheusRule:
      { enabled: true, namespace: monitoring, labels: { release: kps } }
  ```
- [ ] As built (additions to the steps above, all covered by extra assertions in `TestHelmTemplate` after the `cnpg` render): `engine.hostNetwork` (default false; true sets `hostNetwork: true`, `dnsPolicy: ClusterFirstWithHostNet` and drops the pod sysctl, which is namespaced and rejected with host networking); `otelCollector` (`enabled: false`, `image`, `resources`, `config` string) rendering `templates/otel-collector.yaml` (`<prefix>-otelcol` ConfigMap, Deployment, Service), and when enabled with an empty `mgmt.otlpEndpoint` mgmt gets `NEXORA_OTLP_ENDPOINT=http://<prefix>-otelcol.<namespace>.svc.cluster.local:4317` (helper `nexora.otlpEndpoint`); mgmt pod `runAsGroup: 65532` and engine pod `runAsGroup: 10001` (the 0440 secret volumes, as on kw); helpers `nexora.engineWorkload` and `nexora.engineToml` so the ConfigMap and the per-group `checksum/config` share one rendering; `servicemonitor.yaml` and `prometheusrule.yaml` fail with a message when `monitoring.coreos.com/v1` is absent; group `service` and `replicas` may be omitted (`--set-json` groups replace the defaults wholesale); `values.schema.json` also forbids unknown keys in a group and its `service`. `.prettierignore` excludes `deploy/helm/*/templates/`.
- [ ] Change `Makefile`: `GO_PKGS := $(foreach d,mgmt gen bench deploy,$(if $(wildcard $(d)),./$(d)/...))` so `make mgmt-test` (and the CI `mgmt` job) runs `deploy/deploytest`.
- [ ] Run `scripts/dev-exec.sh 'go test ./deploy/deploytest/ -count=1 -v'` and expect `--- PASS: TestHelmTemplate`, `--- PASS: TestImagesWorkflow`, `--- PASS: TestComposeExample`.
- [ ] Commit: `git add deploy/helm/nexora deploy/kw/values-kw.yaml deploy/deploytest/helm_test.go Makefile && git commit -m "feat(helm): chart with engine-group workloads, CNPG or external database, monitoring"`.

## Task 14: Final kw deployment and TestKwFullProduct

Files: `scripts/kw-deploy.sh` (node labels, one-time removal of the kubectl-applied mgmt/engine objects, two-phase Helm install, printed environment), `deploy/kw/bootstrap.sh` (engine group `edge-b`, its join token secret, pruning of pre-M5 engine rows), `deploy/kw/mgmt.yaml` and `deploy/kw/engine.yaml` (removed; the chart replaces them), `deploy/kw/README.md` (chart-based deployment, addresses, engine groups, known limits), `scripts/kw-acceptance.sh` (copies the CA files and password into the dev pod, restarts the `edge-b` engines, runs the kw tests), `e2e/kw_full_product_test.go` (`TestKwFullProduct`)
Interfaces: environment read by `TestKwFullProduct` in addition to `loadKwEnv`'s: `NEXORA_KW_EDGE_B_DNS_ADDR` (`192.168.10.137:53`), `NEXORA_KW_EDGE_B_ENGINE_IPS` (comma-separated pod IPs of `nexora-engine-edge-b`), `NEXORA_KW_PROMETHEUS_URL` (default `http://kps-prometheus.monitoring.svc:9090`); reuses `loadKwEnv`, `kwLogin`, `kwUniqueName`, `kwWaitApplied`, `aValues` and the Task 7 harness helpers.

- [ ] Write `e2e/kw_full_product_test.go`:
  ```go
  package e2e

  import (
  	"encoding/json"
  	"fmt"
  	"net"
  	"net/http"
  	"net/url"
  	"os"
  	"regexp"
  	"strings"
  	"sync"
  	"testing"
  	"time"

  	"github.com/miekg/dns"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  func kwQueryA(addr, name string) ([]string, bool, error) {
  	m := new(dns.Msg)
  	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
  	r, _, err := (&dns.Client{Timeout: 3 * time.Second}).Exchange(m, addr)
  	if err != nil {
  		return nil, false, err
  	}
  	return aValues(r), r.Authoritative, nil
  }

  func kwEngineGroupByName(t *testing.T, api *harness.API, name string) harness.EngineGroupView {
  	t.Helper()
  	var groups []harness.EngineGroupView
  	api.Must(http.MethodGet, "/engine-groups", nil, &groups, http.StatusOK)
  	for _, g := range groups {
  		if g.Name == name {
  			return g
  		}
  	}
  	t.Fatalf("engine group %s missing (created by deploy/kw/bootstrap.sh)", name)
  	return harness.EngineGroupView{}
  }

  // TestKwFullProduct checks the M5 fleet on kw: the per-node engine topology in two engine groups,
  // engine-group-scoped rewrites and zones, identities that survive pod restarts, a canary rollout
  // with rollback and resume on edge-b, certificate rotation, and the fleet metrics and alerts.
  func TestKwFullProduct(t *testing.T) {
  	env := loadKwEnv(t)
  	edgeAddr := os.Getenv("NEXORA_KW_EDGE_B_DNS_ADDR")
  	var edgeIPs []string
  	for _, ip := range strings.Split(os.Getenv("NEXORA_KW_EDGE_B_ENGINE_IPS"), ",") {
  		if ip = strings.TrimSpace(ip); ip != "" {
  			edgeIPs = append(edgeIPs, ip)
  		}
  	}
  	if edgeAddr == "" || len(edgeIPs) != 2 {
  		t.Fatalf("NEXORA_KW_EDGE_B_DNS_ADDR and two NEXORA_KW_EDGE_B_ENGINE_IPS are required (printed by scripts/kw-deploy.sh), got %q %v", edgeAddr, edgeIPs)
  	}
  	promURL := os.Getenv("NEXORA_KW_PROMETHEUS_URL")
  	if promURL == "" {
  		promURL = "http://kps-prometheus.monitoring.svc:9090"
  	}
  	api := kwLogin(t, env)
  	edge := kwEngineGroupByName(t, api, "edge-b")
  	t.Cleanup(func() {
  		g := api.EngineGroup(edge.ID)
  		if g.RolloutsPaused {
  			api.Must(http.MethodPost, "/engine-groups/"+edge.ID+"/resume-rollouts", nil, nil, http.StatusAccepted)
  			g = api.EngineGroup(edge.ID)
  		}
  		api.Must(http.MethodPut, "/engine-groups/"+edge.ID, map[string]any{"name": "edge-b", "revision": g.Revision,
  			"description": g.Description, "rollout_strategy": "all_at_once"}, nil, http.StatusOK)
  	})

  	var engines []harness.EngineView
  	api.Must(http.MethodGet, "/engines", nil, &engines, http.StatusOK)

  	t.Run("topology", func(t *testing.T) {
  		nodeName := regexp.MustCompile(`^(edge-b-)?(master|worker)-[0-9]+$`)
  		perNode := map[string]int{}
  		edgeCount, connected := 0, 0
  		for _, e := range engines {
  			if !nodeName.MatchString(e.NodeName) {
  				t.Errorf("engine %s is not named after its Kubernetes node", e.NodeName)
  			}
  			perNode[e.NodeName]++
  			if e.Connected {
  				connected++
  			}
  			if strings.HasPrefix(e.NodeName, "edge-b-") != (e.EngineGroupID == edge.ID) {
  				t.Errorf("engine %s is in engine group %s", e.NodeName, e.EngineGroupName)
  			}
  			if e.EngineGroupID == edge.ID {
  				edgeCount++
  			}
  		}
  		for node, n := range perNode {
  			if n != 1 {
  				t.Errorf("node %s has %d engine records; a restarted pod enrolled again", node, n)
  			}
  		}
  		if connected != env.engines || edgeCount != 2 {
  			t.Fatalf("connected engines %d (want %d), edge-b engines %d (want 2)", connected, env.engines, edgeCount)
  		}
  	})

  	t.Run("engine-group-scoped-rewrite", func(t *testing.T) {
  		name := kwUniqueName("kw-fleet")
  		var rw struct {
  			ID       string `json:"id"`
  			Revision int64  `json:"revision"`
  		}
  		api.Must(http.MethodPost, "/rewrites", map[string]any{"name": name, "type": "A", "value": "192.0.2.77", "engine_group_id": edge.ID}, &rw, http.StatusCreated)
  		t.Cleanup(func() { api.Must(http.MethodDelete, fmt.Sprintf("/rewrites/%s?revision=%d", rw.ID, rw.Revision), nil, nil, http.StatusNoContent) })
  		kwWaitApplied(t, api, env.engines)
  		for _, ip := range edgeIPs {
  			if got, _, err := kwQueryA(net.JoinHostPort(ip, "53"), name); err != nil || len(got) != 1 || got[0] != "192.0.2.77" {
  				t.Fatalf("edge-b engine %s: %v %v", ip, got, err)
  			}
  		}
  		if got, _, err := kwQueryA(edgeAddr, name); err != nil || len(got) != 1 || got[0] != "192.0.2.77" {
  			t.Fatalf("edge-b load balancer: %v %v", got, err)
  		}
  		if got, _, err := kwQueryA(env.dnsAddr, name); err != nil || (len(got) > 0 && got[0] == "192.0.2.77") {
  			t.Fatalf("the default group served an edge-b rewrite: %v %v", got, err)
  		}
  	})

  	t.Run("zone-scoped-to-edge-b", func(t *testing.T) {
  		zone := kwUniqueName("zone")
  		var z struct {
  			ID string `json:"id"`
  		}
  		api.Must(http.MethodPost, "/zones", map[string]any{"name": zone, "kind": "primary", "default_ttl": 60, "engine_group_id": edge.ID,
  			"soa": map[string]any{"mname": "ns1." + zone, "rname": "hostmaster." + zone}, "nameservers": []string{"ns1." + zone}}, &z, http.StatusCreated)
  		t.Cleanup(func() {
  			var cur struct {
  				Revision int64 `json:"revision"`
  			}
  			api.Must(http.MethodGet, "/zones/"+z.ID, nil, &cur, http.StatusOK)
  			api.Must(http.MethodDelete, fmt.Sprintf("/zones/%s?revision=%d", z.ID, cur.Revision), nil, nil, http.StatusNoContent)
  		})
  		kwWaitApplied(t, api, env.engines)
  		for _, ip := range edgeIPs {
  			m := new(dns.Msg)
  			m.SetQuestion(zone, dns.TypeSOA)
  			r, _, err := (&dns.Client{Timeout: 3 * time.Second}).Exchange(m, net.JoinHostPort(ip, "53"))
  			if err != nil || !r.Authoritative {
  				t.Fatalf("edge-b engine %s is not authoritative for %s: %v %v", ip, zone, r, err)
  			}
  		}
  		m := new(dns.Msg)
  		m.SetQuestion(zone, dns.TypeSOA)
  		if r, _, err := (&dns.Client{Timeout: 3 * time.Second}).Exchange(m, env.dnsAddr); err == nil && r.Authoritative {
  			t.Fatalf("the default group serves the edge-b zone %s", zone)
  		}
  	})

  	t.Run("canary-rollout-rollback-resume", func(t *testing.T) {
  		g := api.EngineGroup(edge.ID)
  		api.Must(http.MethodPut, "/engine-groups/"+edge.ID, map[string]any{"name": "edge-b", "revision": g.Revision, "description": g.Description,
  			"rollout_strategy": "canary", "canary_count": 1, "health_window_seconds": 20, "ack_timeout_seconds": 60,
  			"max_servfail_ratio": 0.05, "min_health_queries": 20}, nil, http.StatusOK)
  		first := api.EngineByNode("edge-b-worker-24")
  		api.PatchEngine(first.NodeName, map[string]any{"labels": map[string]string{"nexora.io/canary": "true"}})
  		stop := make(chan struct{})
  		var wg sync.WaitGroup
  		for _, ip := range edgeIPs {
  			wg.Add(1)
  			go func(addr string) {
  				defer wg.Done()
  				for {
  					select {
  					case <-stop:
  						return
  					case <-time.After(20 * time.Millisecond):
  						_, _, _ = kwQueryA(addr, "example.com")
  					}
  				}
  			}(net.JoinHostPort(ip, "53"))
  		}
  		defer func() { close(stop); wg.Wait() }()

  		name := kwUniqueName("kw-canary")
  		var rw struct {
  			ID       string `json:"id"`
  			Revision int64  `json:"revision"`
  		}
  		api.Must(http.MethodPost, "/rewrites", map[string]any{"name": name, "type": "A", "value": "192.0.2.78", "engine_group_id": edge.ID}, &rw, http.StatusCreated)
  		var newest []harness.RolloutView
  		api.Must(http.MethodGet, "/rollouts?limit=1&engine_group_id="+edge.ID, nil, &newest, http.StatusOK)
  		done := api.WaitRollout(edge.ID, newest[0].Version, 4*time.Minute, "completed")
  		if done.Strategy != "canary" || len(done.CanaryEngineIDs) != 1 || done.CanaryEngineIDs[0] != first.ID {
  			t.Fatalf("rollout %+v, want canary strategy with %s as canary", done, first.NodeName)
  		}
  		var completed []harness.RolloutView
  		api.Must(http.MethodGet, "/rollouts?limit=20&state=completed&engine_group_id="+edge.ID, nil, &completed, http.StatusOK)
  		var prev uint64
  		for _, r := range completed {
  			if r.Version < done.Version {
  				prev = r.Version
  				break
  			}
  		}
  		if prev == 0 {
  			t.Fatalf("no completed edge-b rollout older than %d", done.Version)
  		}
  		var rb harness.RolloutView
  		api.Must(http.MethodPost, "/engine-groups/"+edge.ID+"/rollback", map[string]any{"to_version": prev}, &rb, http.StatusAccepted)
  		api.WaitRollout(edge.ID, rb.Version, 2*time.Minute, "completed")
  		for _, ip := range edgeIPs {
  			if got, _, _ := kwQueryA(net.JoinHostPort(ip, "53"), name); len(got) > 0 && got[0] == "192.0.2.78" {
  				t.Fatalf("edge-b engine %s still serves the rolled-back rewrite", ip)
  			}
  		}
  		api.Must(http.MethodDelete, fmt.Sprintf("/rewrites/%s?revision=%d", rw.ID, rw.Revision), nil, nil, http.StatusNoContent)
  		var resumed harness.RolloutView
  		api.Must(http.MethodPost, "/engine-groups/"+edge.ID+"/resume-rollouts", nil, &resumed, http.StatusAccepted)
  		api.WaitRollout(edge.ID, resumed.Version, 4*time.Minute, "completed")
  	})

  	t.Run("certificate-rotation", func(t *testing.T) {
  		target := api.EngineByNode("edge-b-worker-25")
  		api.Must(http.MethodPost, "/engines/"+target.ID+"/rotate-certificate", nil, nil, http.StatusAccepted)
  		harness.Eventually(t, 90*time.Second, func() error {
  			if e := api.EngineByNode(target.NodeName); e.CertificateSerial == target.CertificateSerial || !e.Connected {
  				return fmt.Errorf("%s not rotated yet", target.NodeName)
  			}
  			return nil
  		})
  	})

  	t.Run("fleet-metrics-and-alerts", func(t *testing.T) {
  		promQuery := func(q string) (float64, bool) {
  			resp, err := http.Get(promURL + "/api/v1/query?query=" + url.QueryEscape(q))
  			if err != nil {
  				t.Fatal(err)
  			}
  			defer resp.Body.Close()
  			var out struct {
  				Data struct {
  					Result []struct {
  						Value []any `json:"value"`
  					} `json:"result"`
  				} `json:"data"`
  			}
  			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Data.Result) == 0 {
  				return 0, false
  			}
  			var v float64
  			_, _ = fmt.Sscan(out.Data.Result[0].Value[1].(string), &v)
  			return v, true
  		}
  		harness.Eventually(t, 3*time.Minute, func() error {
  			if v, ok := promQuery(`max(nexora_mgmt_engines_disconnected{namespace="nexora"})`); !ok || v != 0 {
  				return fmt.Errorf("nexora_mgmt_engines_disconnected = %v (scraped %v), want 0", v, ok)
  			}
  			if v, ok := promQuery(`max(sum by (engine_group) (nexora_mgmt_engines{namespace="nexora",engine_group="edge-b",status="current"}))`); !ok || v != 2 {
  				return fmt.Errorf("current edge-b engines = %v (scraped %v), want 2", v, ok)
  			}
  			if _, firing := promQuery(`ALERTS{alertname="NexoraRolloutHalted",alertstate="firing"}`); firing {
  				return fmt.Errorf("NexoraRolloutHalted is firing")
  			}
  			return nil
  		})
  		resp, err := http.Get(promURL + "/api/v1/rules")
  		if err != nil {
  			t.Fatal(err)
  		}
  		defer resp.Body.Close()
  		var rules struct {
  			Data json.RawMessage `json:"data"`
  		}
  		if err := json.NewDecoder(resp.Body).Decode(&rules); err != nil || !strings.Contains(string(rules.Data), "NexoraEngineDisconnected") {
  			t.Fatalf("PrometheusRule not loaded: %v", err)
  		}
  	})
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'go vet ./e2e/ && go test ./e2e/ -run TestKwFullProduct -count=1 -v'` without the kw variables and expect `--- SKIP: TestKwFullProduct` with `NEXORA_KW_DNS_ADDR and NEXORA_KW_API_URL are not set`.
- [ ] Change `deploy/kw/bootstrap.sh`: after the RPZ block and before the join token block, add
  ```bash
  edge=$(call "$api/api/v1/engine-groups" | jq -r '.[] | select(.name=="edge-b") | .id')
  if [ -z "$edge" ]; then
  	edge=$(call -d '{"name":"edge-b","description":"engines on nodes labelled nexora.io/engine-group=edge-b"}' "$api/api/v1/engine-groups" | jq -r .id)
  	echo "engine group edge-b created"
  fi
  ```
  after the existing `nexora-join-token` block add
  ```bash
  if ! k get secret nexora-join-token-edge-b >/dev/null 2>&1; then
  	jq -n --arg g "$edge" '{name:"kw-engines-edge-b", ttl_seconds:31536000, engine_group_id:$g}' |
  		call -d @- "$api/api/v1/join-tokens" | jq -r .token | tr -d '\n' >"$tmp/join-token-edge-b"
  	k create secret generic nexora-join-token-edge-b --from-file=join-token="$tmp/join-token-edge-b"
  fi

  # Engines enrolled before M5 were named after their (emptyDir) pods and never reconnect.
  call "$api/api/v1/engines" |
  	jq -r '.[] | select(.connected | not) | select(.node_name | test("^(edge-b-)?(master|worker)-[0-9]+$") | not) | .id' |
  	while read -r id; do
  		call -X DELETE "$api/api/v1/engines/$id" >/dev/null && echo "removed pre-M5 engine $id"
  	done
  ```
- [ ] Replace the mgmt and engine part of `scripts/kw-deploy.sh` (from `sed "s/NEXORA_TAG/$tag/" "$kw/mgmt.yaml"` to the end) with:
  ```bash
  kubectl --context "$ctx" label node worker-24 worker-25 nexora.io/engine-group=edge-b --overwrite

  # One-time switch from the kubectl-applied M1-M4 manifests to the chart: remove objects Helm does not own.
  if k get deployment nexora-mgmt >/dev/null 2>&1 &&
  	[ "$(k get deployment nexora-mgmt -o jsonpath='{.metadata.labels.app\.kubernetes\.io/managed-by}')" != Helm ]; then
  	k delete deployment/nexora-mgmt daemonset/nexora-engine service/nexora-mgmt service/nexora-mgmt-grpc \
  		service/nexora-mgmt-lb service/nexora-dns service/nexora-engine-metrics ingress/nexora \
  		configmap/nexora-engine-config --ignore-not-found
  fi

  release() {
  	helm --kube-context "$ctx" -n "$ns" upgrade --install nexora "$root/deploy/helm/nexora" \
  		-f "$kw/values-kw.yaml" --set image.tag="$tag" --wait --timeout 15m "$@"
  }

  # Phase 1: the management plane; the engine join tokens come from its API.
  release --set engine.enabled=false
  k rollout status deployment/nexora-mgmt --timeout=10m
  k rollout status deployment/nexora-otelcol --timeout=5m
  k rollout status deployment/nexora-blocklist --timeout=5m
  "$kw/bootstrap.sh"

  # Phase 2: engines of both engine groups.
  release
  k rollout status daemonset/nexora-engine --timeout=15m
  k rollout status daemonset/nexora-engine-edge-b --timeout=15m
  "$kw/bootstrap.sh"

  dns_ip=$(k get service nexora-dns -o jsonpath='{.spec.loadBalancerIP}')
  edge_ip=$(k get service nexora-dns-edge-b -o jsonpath='{.spec.loadBalancerIP}')
  mgmt_ip=$(k get service nexora-mgmt-lb -o jsonpath='{.spec.loadBalancerIP}')
  engines=$(($(k get daemonset nexora-engine -o jsonpath='{.status.desiredNumberScheduled}') + \
  	$(k get daemonset nexora-engine-edge-b -o jsonpath='{.status.desiredNumberScheduled}')))
  edge_ips=$(k get pods -l nexora.io/engine-group=edge-b -o jsonpath='{range .items[*]}{.status.podIP}{","}{end}')
  echo "NEXORA_KW_DNS_ADDR=${dns_ip}:53"
  echo "NEXORA_KW_EDGE_B_DNS_ADDR=${edge_ip}:53"
  echo "NEXORA_KW_EDGE_B_ENGINE_IPS=${edge_ips%,}"
  echo "NEXORA_KW_API_URL=${NEXORA_KW_API_URL:-https://nexora.kw.local}"
  echo "NEXORA_KW_ENCRYPTED_ADDR=${dns_ip}"
  echo "NEXORA_KW_DNS_TLS_NAME=dns.nexora.kw.local"
  echo "NEXORA_KW_ENGINES=${engines}"
  echo "NEXORA_KW_MGMT_LB_IP=${mgmt_ip}"
  ```
  and add `192.168.10.137` to the `--names` of the `nexora-dns-tls` issuance (`dns.nexora.kw.local,192.168.10.136,192.168.10.137`; an existing secret is replaced by deleting it once: `kubectl --context kw -n nexora delete secret nexora-dns-tls` before the deploy). Then `git rm deploy/kw/mgmt.yaml deploy/kw/engine.yaml`.
- [ ] Create `scripts/kw-acceptance.sh` (mode 0755):
  ```bash
  #!/usr/bin/env bash
  # Run the kw acceptance tests (TestKwSmoke, TestKwSmokeM4, TestKwFullProduct) in the dev pod against
  # the live deployment. The edge-b engines are restarted first, so TestKwFullProduct proves that
  # engine identities survive pod restarts (hostPath state).
  set -euo pipefail
  ctx="${NEXORA_KW_CONTEXT:-kw}"
  k() { kubectl --context "$ctx" -n nexora "$@"; }
  pod() { kubectl --context "$ctx" -n nexora-dev exec -i deploy/toolbox -c toolbox -- sh -c "$1"; }
  k get secret nexora-ca -o jsonpath='{.data.ca\.crt}' | base64 -d | pod 'cat > /work/kw-ca.crt'
  k get secret nexora-ingress-tls -o jsonpath='{.data.ca\.crt}' | base64 -d | pod 'cat > /work/kw-cluster-ca.crt'
  k get secret nexora-admin -o jsonpath='{.data.password}' | base64 -d | pod 'umask 077; cat > /work/kw-admin-password'

  k rollout restart daemonset/nexora-engine-edge-b
  k rollout status daemonset/nexora-engine-edge-b --timeout=10m
  engines=$(($(k get daemonset nexora-engine -o jsonpath='{.status.desiredNumberScheduled}') + \
  	$(k get daemonset nexora-engine-edge-b -o jsonpath='{.status.desiredNumberScheduled}')))
  edge_ips=$(k get pods -l nexora.io/engine-group=edge-b --field-selector=status.phase=Running \
  	-o jsonpath='{range .items[*]}{.status.podIP}{","}{end}')

  exec "$(dirname "$0")/dev-exec.sh" env \
  	NEXORA_KW_DNS_ADDR=192.168.10.136:53 NEXORA_KW_EDGE_B_DNS_ADDR=192.168.10.137:53 \
  	NEXORA_KW_EDGE_B_ENGINE_IPS="${edge_ips%,}" NEXORA_KW_API_URL=https://nexora.kw.local \
  	NEXORA_KW_API_CA_FILE=/work/kw-cluster-ca.crt NEXORA_KW_ENCRYPTED_ADDR=192.168.10.136 \
  	NEXORA_KW_CA_FILE=/work/kw-ca.crt NEXORA_KW_DNS_TLS_NAME=dns.nexora.kw.local NEXORA_KW_ENGINES="$engines" \
  	NEXORA_KW_MGMT_LB_IP=192.168.10.135 NEXORA_KW_ADMIN_PASSWORD_FILE=/work/kw-admin-password \
  	NEXORA_KW_PROMETHEUS_URL=http://kps-prometheus.monitoring.svc:9090 \
  	go test -count=1 -v -timeout 45m -run 'TestKwSmoke|TestKwFullProduct' ./e2e/
  ```
- [ ] Update `deploy/kw/README.md`: the deploy command stays `scripts/kw-deploy.sh [--tag sha-<7>] [--skip-build]`, now followed by `scripts/kw-acceptance.sh`; the file table replaces `mgmt.yaml` and `engine.yaml` with `values-kw.yaml` (Helm release `nexora` from `deploy/helm/nexora`) and adds `nexora-join-token-edge-b` under "Secrets"; "Addresses" adds `192.168.10.137` (engine group `edge-b`, `externalTrafficPolicy: Cluster`, engines see node addresses there) and the node label `nexora.io/engine-group=edge-b` on `worker-24` and `worker-25`; "Known limits" replaces the `emptyDir` entry with "engine state is hostPath `/var/lib/nexora/<workload>`; a restarted pod keeps its engine id; removing that directory and the pod re-enrolls it as a new engine".
- [ ] Build and deploy the current commit: run `scripts/kw-deploy.sh` and expect `helm` to report `STATUS: deployed` twice, `daemon set "nexora-engine" successfully rolled out`, `daemon set "nexora-engine-edge-b" successfully rolled out`, and the printed `NEXORA_KW_ENGINES=8` and `NEXORA_KW_EDGE_B_DNS_ADDR=192.168.10.137:53`; then `kubectl --context kw -n nexora get svc nexora-mgmt-lb nexora-dns nexora-dns-edge-b -o jsonpath='{range .items[*]}{.metadata.name}={.status.loadBalancer.ingress[0].ip}{"\n"}{end}'` and expect `nexora-mgmt-lb=192.168.10.135`, `nexora-dns=192.168.10.136`, `nexora-dns-edge-b=192.168.10.137`.
- [ ] Run `scripts/kw-acceptance.sh` and expect `--- PASS: TestKwSmoke`, `--- PASS: TestKwSmokeM4` and `--- PASS: TestKwFullProduct` with every subtest passing (`TestKwSmoke/recursion` skips itself on kw as documented).
- [ ] Commit: `git add deploy/kw scripts/kw-deploy.sh scripts/kw-acceptance.sh e2e/kw_full_product_test.go && git commit -m "feat(kw): fleet deployment from the Helm chart with two engine groups; TestKwFullProduct"`.

## Task 15: Operations documentation and README

Files: `docs/operations.md` (install, upgrade, backup/restore, rollouts, lifecycle, monitoring, kw), `README.md` (product overview and quick start), `deploy/deploytest/docs_test.go` (`TestOperationsDoc`)
Interfaces: headings listed in the test; every repository path the documents mention in backticks must exist.

As built: `docs/operations.md` keeps the eight required headings and the content below, verified against the code, and adds overview (components, features per milestone, ports, mgmt environment and CLI, `engine.toml` essentials), first-run setup and access (lost setup token: `user create --admin --password-file /dev/stdin`; a new install forwards with no upstreams), enrolling engines, encrypted DNS, key storage, a state-location table, Compose backup/restore, performance tuning and the perf gate, known limitations and troubleshooting. Corrections to the draft below: the setup-token `kubectl logs` uses the label selector (only one replica logs it), `docker compose run` needs `-T` when redirecting the token, and the restore loop pauses/resumes one group repeatedly because each publish adds exactly one global version. The kw section describes the chart release from `deploy/kw/values-kw.yaml` and points to `deploy/kw/README.md`; it does not name `scripts/kw-acceptance.sh`, which Task 14 creates (re-check that section when Task 14 lands). README has no badges (the repository has no CI remote yet).

- [x] Write the failing test `deploy/deploytest/docs_test.go`:
  ```go
  package deploytest

  import (
  	"os"
  	"regexp"
  	"strings"
  	"testing"
  )

  func TestOperationsDoc(t *testing.T) {
  	ops, err := os.ReadFile("../../docs/operations.md")
  	if err != nil {
  		t.Fatal(err)
  	}
  	for _, h := range []string{
  		"## Install with Helm", "## Install with Docker Compose", "## Upgrade", "## Backup and restore PostgreSQL",
  		"## Engine groups and staged rollouts", "## Engine lifecycle", "## Monitoring and alerts", "## kw deployment",
  	} {
  		if !strings.Contains(string(ops), "\n"+h+"\n") {
  			t.Errorf("docs/operations.md lacks heading %q", h)
  		}
  	}
  	readme, err := os.ReadFile("../../README.md")
  	if err != nil {
  		t.Fatal(err)
  	}
  	for _, want := range []string{"## Quick start", "docs/operations.md", "docs/architecture.md", "deploy/helm/nexora", "deploy/compose"} {
  		if !strings.Contains(string(readme), want) {
  			t.Errorf("README.md lacks %q", want)
  		}
  	}
  	paths := regexp.MustCompile("`((?:scripts|deploy|docs|mgmt|engine|e2e)/[A-Za-z0-9_./-]+)`")
  	for _, doc := range [][]byte{ops, readme} {
  		for _, m := range paths.FindAllSubmatch(doc, -1) {
  			if _, err := os.Stat("../../" + string(m[1])); err != nil {
  				t.Errorf("documented path %s does not exist", m[1])
  			}
  		}
  	}
  }
  ```
- [x] Run `scripts/dev-exec.sh go test ./deploy/deploytest/ -run TestOperationsDoc -count=1` and expect FAIL with `no such file or directory`.
- [x] Create `docs/operations.md`:
  ````markdown
  # Operating Nexora

  Nexora is one stateless management plane (`nexora-mgmt`, any number of
  replicas, all state in PostgreSQL) and any number of engines
  (`nexora-engine`) that dial out to it over mTLS. The design is in
  `docs/architecture.md`.

  ## Install with Helm

  Prerequisites: Kubernetes 1.28+, Helm 3.14+, and either the CloudNativePG
  operator (`database.mode=cnpg`) or a PostgreSQL 15+ connection URL in a
  secret (`database.mode=external`, key `uri` by default). Prometheus Operator
  CRDs are needed only when `metrics.serviceMonitor` or `metrics.prometheusRule`
  is enabled. Every value is validated by `deploy/helm/nexora/values.schema.json`.

  1. Create the engine CA once and keep an offline copy; losing it means
     re-enrolling every engine:
     ```sh
     nexora-mgmt ca init --out ./nexora-ca
     kubectl -n nexora create secret generic nexora-ca --from-file=./nexora-ca/ca.crt --from-file=./nexora-ca/ca.key
     ```
     Optionally create a key-encryption key secret (`kek`: `openssl rand -base64 32`)
     for TSIG, RPZ TSIG and DNSSEC keys (`mgmt.kek.existingSecret`) and a
     `kubernetes.io/tls` secret for DoT/DoH/DoQ (`mgmt.dnsTLS.existingSecret`,
     issued with `nexora-mgmt ca issue-dns`).
  2. Install the management plane without engines:
     ```sh
     helm upgrade --install nexora deploy/helm/nexora -n nexora \
       --set mgmt.ca.existingSecret=nexora-ca --set engine.enabled=false --wait
     ```
  3. Complete first-run setup in the GUI at `/setup` with the setup token the
     first instance logs (`kubectl -n nexora logs deploy/nexora-mgmt | grep "setup token"`).
  4. Create an engine group (GUI `/engines` or the API) and a join token per
     engine workload, stored as a secret with key `join-token`:
     ```sh
     kubectl -n nexora exec deploy/nexora-mgmt -c mgmt -- /nexora-mgmt engine-group create --name edge --if-missing
     kubectl -n nexora exec deploy/nexora-mgmt -c mgmt -- /nexora-mgmt join-token create --engine-group edge --ttl 8760h \
       | kubectl -n nexora create secret generic nexora-join-edge --from-file=join-token=/dev/stdin
     ```
     A token without `--max-uses` can enroll every pod of a DaemonSet; an
     enrolled engine never needs it again because its identity lives in the
     state directory.
  5. Add the group to `engine.groups` (name, `joinTokenSecret`, node affinity,
     DNS Service) and run the same `helm upgrade` without `engine.enabled=false`.

  Engine state: `engine.stateDir.type=hostPath` (default) keeps the identity and
  the last snapshot across pod restarts under `<hostPathPrefix>/<workload>`;
  `emptyDir` enrolls a new engine on every restart. The engine name is
  `<nodeNamePrefix><Kubernetes node name>`.

  ## Install with Docker Compose

  ```sh
  cd deploy/compose
  cp .env.example .env                                   # set NEXORA_TAG
  openssl rand -hex 24 > secrets/postgres-password
  printf 'postgres:5432:nexora:nexora:%s\n' "$(cat secrets/postgres-password)" > secrets/pgpass
  chmod 0644 secrets/postgres-password secrets/pgpass    # read by the postgres and nexora-mgmt users
  docker compose up -d                                   # postgres, CA, migrations, mgmt
  docker compose logs mgmt | grep "setup token"          # complete /setup at NEXORA_PUBLIC_URL
  docker compose run --rm mgmt join-token create --engine-group default --ttl 1h > secrets/join-token
  docker compose --profile engine up -d
  docker compose --profile otel up -d                    # optional collector (otel-collector.yaml)
  ```

  ## Upgrade

  1. Back up PostgreSQL (next section).
  2. `helm upgrade` with the new `image.tag`. Every mgmt pod runs
     `nexora-mgmt migrate` as an init container; migrations take a PostgreSQL
     advisory lock, so replicas never migrate concurrently. Management pods roll
     one at a time (PodDisruptionBudget `minAvailable: 1`); engines keep serving
     and reconnect to a surviving instance.
  3. Engine workloads roll with `maxUnavailable: 1`; with hostPath state a
     restarted engine keeps its identity and last snapshot.
  4. Check `/engines` (status column) or the metric
     `nexora_mgmt_engines_disconnected`.

  Migrations are forward-only in production; to go back past one, restore the
  pre-upgrade backup.

  ## Backup and restore PostgreSQL

  Everything except query logs lives in PostgreSQL. Back up before every
  upgrade and on a schedule (CNPG `ScheduledBackup` with
  `spec.backup.barmanObjectStore` for continuous backups).

  ```sh
  primary=$(kubectl -n nexora get cluster nexora-db -o jsonpath='{.status.currentPrimary}')
  kubectl -n nexora exec "$primary" -c postgres -- pg_dump -Fc -d nexora > nexora-backup.dump
  ```

  Restore with the management plane stopped:

  ```sh
  kubectl -n nexora scale deploy/nexora-mgmt --replicas=0
  kubectl -n nexora exec -i "$primary" -c postgres -- pg_restore --clean --if-exists -d nexora < nexora-backup.dump
  kubectl -n nexora scale deploy/nexora-mgmt --replicas=2
  ```

  Engines that ran newer configuration versions than the restored database are
  flagged `ahead` and are not downgraded. To bring them back:

  1. In `psql`, pause every group: `update engine_groups set rollouts_paused = true;`
  2. Call `POST /api/v1/engine-groups/{id}/resume-rollouts` for each group; each
     call publishes the restored configuration as a new version. Repeat steps 1
     and 2 until the newest version (`GET /api/v1/config-versions?limit=1`) is above the
     highest `nexora_config_version` the engines report. `ahead` engines then
     return to `current`.

  ## Engine groups and staged rollouts

  - Every engine is in one engine group; upstreams, filter lists, policy
    groups, global rewrites, forward zones, zones and RPZ zones apply to every
    group or to one ("Engine group" field in the GUI, `engine_group_id` in the
    API). Engine groups are server-side; policy groups still select clients.
  - Every change publishes one version with a snapshot per engine group. A
    group whose content did not change applies it at once. Strategy
    `all_at_once` pushes to every engine; `canary` pushes to
    max(`canary_count`, `canary_percent`) engines (label
    `nexora.io/canary=true` first), waits for them to apply within
    `ack_timeout_seconds`, watches their SERVFAIL ratio for
    `health_window_seconds` (ignored below `min_health_queries` queries), then
    rolls to the rest.
  - A rejection, an ack timeout, a canary that stops reporting, or a SERVFAIL
    ratio above `max_servfail_ratio` halts the rollout: the canaries keep the new
    version, everyone else stays on the stable one, and `NexoraRolloutHalted`
    fires.
  - Fix forward with another change (it supersedes the halted rollout), or roll
    back (`/engines/groups/<id>` "Roll back", or
    `POST /api/v1/engine-groups/{id}/rollback` with `{"to_version": N}`). A
    rollback republishes version N to the group at once and pauses change
    rollouts, because the configuration rows still contain the rolled-back
    change; correct them, then "Resume rollouts".
  - Moving an engine to another group republishes that group's stable snapshot
    as a new version.

  ## Engine lifecycle

  - Join tokens carry the engine group, labels, an expiry and optionally a use
    limit; revoke unused tokens under `/engines`.
  - Engine certificates live for `NEXORA_ENGINE_CERT_TTL` (default 90 days) and
    renew automatically from 2/3 of their lifetime over the control stream.
  - "Rotate certificate" forces a renewal now; the old serial is marked
    superseded when the engine reconnects with the new one.
  - "Revoke engine" rejects the engine's certificates on every management
    instance at once; the engine keeps serving its last configuration and
    retries every 5 minutes. To re-admit the host, remove its state directory
    (`/var/lib/nexora/<workload>` with hostPath) and give it a join token; it
    enrolls as a new engine. "Delete engine" revokes and removes the record.

  ## Monitoring and alerts

  - Scrape `/metrics` on the mgmt `http` port and on the engines' `metrics`
    port; the chart's ServiceMonitor does both when enabled.
  - Fleet metrics: `nexora_mgmt_engines{engine_group,status}`,
    `nexora_mgmt_engines_disconnected`, `nexora_mgmt_rollouts{engine_group,state}`;
    engine `nexora_control_connected`, `nexora_control_revoked`,
    `nexora_control_cert_renewals_total`; the rest are listed in
    `docs/architecture.md`.
  - Alerts (`metrics.prometheusRule.enabled`): `NexoraEngineDisconnected`,
    `NexoraRolloutHalted`, `NexoraManagementPlaneDown`.

  ## kw deployment

  The lab deployment is described in `deploy/kw/README.md`:
  `scripts/kw-deploy.sh` installs `deploy/helm/nexora` with
  `deploy/kw/values-kw.yaml`, and `scripts/kw-acceptance.sh` runs
  `TestKwSmoke`, `TestKwSmokeM4` and `TestKwFullProduct`.

  | Component                                             | Address                                                         |
  | ----------------------------------------------------- | --------------------------------------------------------------- |
  | GUI and API                                           | `https://nexora.kw.local` (ingress, ClusterIssuer `cluster-ca`) |
  | Engine gRPC                                           | `192.168.10.135:9443`                                           |
  | DNS, engine group `default` (6 engines)               | `192.168.10.136` (53, DoT 853, DoH 443, DoQ 853)                |
  | DNS, engine group `edge-b` (`worker-24`, `worker-25`) | `192.168.10.137`                                                |
  | Traces                                                | Jaeger `jaeger.observability:4317`                              |
  | Metrics                                               | kube-prometheus-stack (`release: kps`)                          |
  ````
- [x] Rewrite `README.md` with these sections, keeping any existing badge lines at the top:
  ```markdown
  # Nexora

  A fast DNS server: a Rust engine (forwarding, recursion, DNSSEC validation and
  signing, authoritative zones with transfers and dynamic updates, blocklists,
  per-client policy, RPZ, DoT/DoH/DoQ) managed by a stateless Go management plane
  with a React GUI, from one host to a fleet of engines in engine groups with
  staged, health-gated configuration rollouts.

  ## Quick start

  Docker Compose (single host): `deploy/compose` and "Install with Docker
  Compose" in `docs/operations.md`.

  Kubernetes: the Helm chart in `deploy/helm/nexora`; see "Install with Helm" in
  `docs/operations.md`.

  ## Documentation

  - `docs/operations.md`: install, upgrade, backup and restore, rollouts, engine lifecycle, monitoring
  - `docs/architecture.md`: how Nexora is built

  ## Development

  The engine is Linux-only; builds and tests run in the kw dev pod:
  `scripts/dev-exec.sh make build`, `scripts/dev-exec.sh make e2e`.
  Release images are built by `.github/workflows/images.yml`.
  ```
- [x] Run `scripts/dev-exec.sh go test ./deploy/deploytest/ -count=1 -v` and expect `--- PASS` for `TestOperationsDoc`, `TestHelmTemplate`, `TestImagesWorkflow` and `TestComposeExample`.
- [ ] Run the milestone gate: `scripts/dev-exec.sh 'make lint && make engine-test && make mgmt-test && make web-test && make e2e-build && NEXORA_E2E_BIN_DIR=$PWD/bin go test ./e2e/ -run "TestFleetRolloutAndPartition|TestEngineGroupScopedConfig|TestJoinTokenGroupAndExpiry|TestCanaryRolloutHaltsOnFailure|TestEngineCertRevocation|TestFleetAPI|TestMgmtCLIFleet|TestGUICoverage|TestMgmtStatelessHA|TestInvalidSnapshotRejected" -count=1 -timeout 90m'` and expect every target and test to pass; then `scripts/kw-acceptance.sh` and expect `--- PASS: TestKwFullProduct`.
- [ ] Commit: `git add docs/operations.md README.md deploy/deploytest/docs_test.go && git commit -m "docs: operations guide and README for the fleet release"`.
