# nexora-v1-m5 — implementation plan

Status: draft
Spec: .procoder/specs/nexora-v1.md

## Goal

Ship S-5 as a product: one stateless management plane drives N engines on different hosts through engine groups, group-scoped configuration, staged (canary) rollouts with an automatic health halt and manual rollback, fleet health and engine lifecycle (join tokens, certificate renewal, rotation and revocation), a fleet GUI, release images, a docker-compose example, a finished Helm chart, and the final kw deployment verified end to end by `TestKwFullProduct`.

## Architecture

The M1 contract already streams versioned snapshots over mTLS and every mgmt instance LISTENs on Postgres; M5 inserts two layers between a config mutation and an engine push. First, every mutation publishes one global version with one snapshot per affected engine group (`group_snapshots`), built from global plus group-scoped config rows. Second, each group snapshot gets a `rollouts` row whose state machine (`mgmt/internal/rollout`, a pure `Step` function) is advanced by any mgmt instance under a Postgres advisory lock; the instance holding an engine's stream pushes whatever `rollout.Target` says that engine should run, and health samples written from engine `Stats` feed the canary gate. Engine lifecycle adds a certificate table checked on every `Connect` (CRL equivalent) and certificate renewal over the existing stream; distribution adds `images.yml`, `deploy/compose/`, `deploy/helm/nexora` and a kw deployment built from that chart.

## Constraints

Copied from the spec and docs/architecture.md (binding):

- "Management plane is stateless: all state in PostgreSQL, any number of instances behind a load balancer, engines may connect to any instance."
- "Deployment: multi-node from v1 — one management plane controls N engines across hosts; single-host deployment is the N=1 case of the same model."
- "Distribution: multi-arch (amd64, arm64) OCI container images for engine and management plane, a docker-compose example, and a Helm chart."
- "The query path never logs synchronously, never touches a database, and never performs per-packet heap allocation on the cache-hit path." Nothing in M5 touches the engine worker threads; health counters are read from the existing per-worker atomics on the control runtime.
- "Engines persist their last applied snapshot locally and keep serving on it when the management plane is unreachable." This holds for revoked engines too.
- "Stale engine: an engine reconnecting with an old snapshot version is brought to current; an engine reporting a newer version than the database (restored backup) is flagged, not silently downgraded."
- "Concurrent edits: two operators editing the same zone or policy get optimistic-concurrency conflicts, not lost writes." Engine groups and engines carry `revision`; stale revision -> 409 `conflict`.
- Out of scope: "Kubernetes operator / CRDs (Helm chart only)", "Management-plane-managed PostgreSQL HA (operator's responsibility)".
- "Every test that asserts 'does not happen' first asserts the positive path in the same run, so a harness failure cannot pass a negative check."
- Every build/test command runs in the kw dev pod through `scripts/dev-exec.sh <cmd>` (it syncs first). Images build with `scripts/build-image.sh` and are pulled as `192.168.10.131/azrtydxb/<name>:<tag>`. Commits run on the laptop with `git`.
- Go module `github.com/piwi3910/nexora`; uuid type `github.com/google/uuid`; DNS client in Go tests `github.com/miekg/dns`.
- M5 migrations are numbered `00500`..`00509` in `mgmt/migrations/`; M5 proto fields use numbers `>= 100` so they never collide with M1–M4 fields.
- Fixed identifiers: default group id `00000000-0000-0000-0000-000000000001`, name `default`; canary label key `nexora.io/canary`; NOTIFY channels `nexora_rollout` (payload group id; instances re-push), `nexora_rollout_tick` (payload group id; wakes controllers only), `nexora_engine_updated`, `nexora_engine_revoked`, `nexora_engine_rotate` (payload engine id); advisory locks `hashtext('rollout:' || id)`, `hashtext('publish')`, `hashtext('fleet:prune')`.
- New management-plane environment: `NEXORA_INSTANCE_ID` (default `os.Hostname()`), `NEXORA_ENGINE_CERT_TTL` (Go duration, default `2160h`, minimum `30s`), `NEXORA_ROLLOUT_TICK` (default `1s`). New engine environment: `NEXORA_ENGINE_NODE_NAME` (overrides `node_name`).
- New metrics: `nexora_mgmt_engines_disconnected` (gauge), `nexora_mgmt_engines{group,state}`, `nexora_mgmt_engine_drift{group,drift}`, `nexora_mgmt_rollouts{state}`; engine `nexora_control_revoked` (gauge 0/1).
- kw facts: arm64 k3s (nodes master-11..13, worker-21..25), storage classes `longhorn` (default) / `longhorn-single`, ingress class `nginx`, ClusterIssuer `cluster-ca`, CNPG operator already installed in `cnpg-system`, Jaeger at `jaeger.observability:4317` (query `jaeger.observability:16686`), Prometheus `kps-prometheus.monitoring:9090`, dev pod namespace `nexora-dev`. LoadBalancer IPs used by other workloads: 120–131, 133, 134. M5 takes: `192.168.10.135` mgmt gRPC 9443, `192.168.10.136` engine group `edge-a` DNS, `192.168.10.137` engine group `edge-b` DNS; `.138` and `.139` stay unassigned.
- Consumed M1–M4 identifiers (verified in Task 1, step 1): tables `engines`, `join_tokens`, `config_versions(version, snapshot)`, `upstreams`, `acl_entries`, `filter_lists`, `client_policies`, `rewrites`, `zones`, `rpz_zones`, `telemetry_settings`; proto messages `Hello`, `Stats`, `ConfigSnapshot` (field `uint64 version`), `EngineMessage` and `ServerMessage` each with `oneof msg`; Go `snapshot.Publish`, `storetest.NewDB(t) *pgxpool.Pool`, `auth.Audit`; harness `StartPostgres`, `StartMgmt`, `StartEngine`, `StartFixtureUpstream`, `RunPlaywright`; engine identity files `state_dir/identity/{cert.pem,key.pem,ca.pem}`. Where Task 1 finds a different M1–M4 spelling, that spelling replaces the one written here everywhere in this plan; behaviour does not change.

## Task 1: Settle the fleet design in docs/architecture.md

Files: `docs/architecture.md` (the binding how; changed before any code, per its own rule)
Interfaces: produces the names every later task uses — tables `engine_groups`, `group_snapshots`, `rollouts`, `engine_health_samples`, `engine_certificates`; package `mgmt/internal/rollout`; package `mgmt/internal/fleet`; routes `/engines`, `/engines/:engineId`, `/engines/groups/:groupId`, `/engines/rollouts/:rolloutId`.

- [ ] Verify the M1–M4 identifiers this plan consumes. Run:
  ```
  scripts/dev-exec.sh 'ls mgmt/migrations | sort | tail -1; grep -hoE "CREATE TABLE (engines|join_tokens|config_versions|upstreams|acl_entries|filter_lists|client_policies|rewrites|zones|rpz_zones|telemetry_settings) " mgmt/migrations/*.sql | sort -u | wc -l; grep -c "snapshot bytea" mgmt/migrations/*.sql | grep -v ":0" | wc -l; grep -nE "^message (Hello|Stats|ConfigSnapshot|EngineMessage|ServerMessage) " proto/nexora/control/v1/control.proto | wc -l; grep -c "oneof msg" proto/nexora/control/v1/control.proto; grep -rhoE "func (StartPostgres|StartMgmt|StartEngine|StartFixtureUpstream|RunPlaywright)\(" e2e/harness | sort -u | wc -l; grep -rhoE "func (NewDB|Publish|Audit)\(" mgmt/internal/store/storetest mgmt/internal/snapshot mgmt/internal/auth | sort -u | wc -l'
  ```
  expect, line by line: a file name whose numeric prefix is below `00500`; `11`; `1`; `5`; `2`; `5`; `3`. For every count that differs, find the M1–M4 spelling (`grep -rn` for the concept) and record it in the "M1–M4 names" table added in the next step; later tasks use the recorded spelling.
- [ ] Add a section `## Fleet (M5)` to `docs/architecture.md` directly before `## Deployment on kw`, with exactly this content (plus the "M1–M4 names" table from the previous step when it is non-empty):
  ```markdown
  ## Fleet (M5)

  ### Engine groups and scoping

  - `engine_groups` holds groups. The group `default`
    (`00000000-0000-0000-0000-000000000001`) always exists and cannot be renamed
    or deleted. Every engine belongs to exactly one group (`engines.group_id`);
    a join token names the group the enrolling engine lands in, and
    `PATCH /api/v1/engines/{engineId}` moves an engine.
  - Config resource tables `upstreams`, `acl_entries`, `filter_lists`,
    `client_policies`, `rewrites`, `zones`, `rpz_zones`, `telemetry_settings`
    carry `group_id uuid NULL`; NULL means global. Resource names stay unique
    across scopes, except zones: a zone name may exist once per group but never
    both globally and in a group, so one group can be a secondary for a zone
    another group serves as primary.
  - A group's effective configuration: ACL entries, filter lists, policies,
    rewrites, zones and RPZ zones are global rows followed by the group's rows.
    Upstreams follow `engine_groups.upstream_mode`: `inherit` = the group's
    upstreams first, then global ones; `override` = only the group's upstreams
    (none = full recursion from root hints). Telemetry settings: the group row,
    when present, replaces the global row as a whole.
  - Host concerns stay in `engine.toml` (listen addresses, `node_name`,
    `state_dir`). `NEXORA_ENGINE_NODE_NAME` overrides `node_name`. Per-engine
    state set through the API is limited to the group and labels
    (`engines.labels`, string -> string, key `^[a-z0-9]([a-z0-9./-]{0,61}[a-z0-9])?$`,
    value at most 63 characters, at most 32 labels). `nexora.io/canary=true`
    makes an engine preferred for canary selection.

  ### Versions and snapshots

  - `config_versions.version` stays one global monotonic sequence. A mutation
    publishes one version and one `group_snapshots(version, group_id)` row per
    affected group: a global resource affects every group, a group-scoped
    resource affects its group, a resource moved between scopes affects the
    union of old and new scope. `ConfigSnapshot.version` equals the version.
    Publishing takes `pg_advisory_xact_lock(hashtext('publish'))`.
  - `engine_groups.stable_version` is the newest version whose rollout
    completed for that group.
  - Rollback and republish copy an existing group snapshot into a new version
    (re-encoded with the new version number), because engines only apply a
    version greater than the one they run.

  ### Rollouts

  - Each group snapshot gets one `rollouts` row. Kinds: `change` (a config
    mutation; strategy from the group), `rollback` and `republish` (always
    `all_at_once`). Group rollout parameters: `rollout_strategy`
    (`all_at_once` | `canary`), `canary_count`, `canary_percent`,
    `ack_timeout_seconds` (default 60), `health_window_seconds` (default 30),
    `max_servfail_ratio` (default 0.05), `min_health_queries` (default 100).
    Parameters are copied into `rollouts.params` when the rollout is created.
  - States: `pending` -> `canary` -> `verifying` -> `rolling` -> `completed`;
    `canary`, `verifying`, `rolling` -> `halted`; `halted` -> `rolled_back`;
    every non-terminal state -> `superseded`. Terminal: `completed`,
    `rolled_back`, `superseded`. At most one rollout per group is in
    `canary`/`verifying`/`rolling`.
  - `pending`: waits while the group has `rollouts_paused` and the kind is
    `change`; otherwise `all_at_once` or non-change kinds go to `rolling`, and
    `canary` selects canaries and goes to `canary`.
  - Canary selection: connected engines, `nexora.io/canary=true` first, then by
    name; size = max(`canary_count`, ceil(`canary_percent`% of connected)),
    at least 1, at most connected-1 when two or more engines are connected.
  - `canary`: any canary rejecting the version -> `halted`; all canaries
    applied -> `verifying`; `ack_timeout_seconds` elapsed -> `halted`.
  - `verifying`: rejection -> `halted`; after `health_window_seconds`, for each
    canary: fewer than 2 health samples -> `halted` ("stopped reporting");
    at least `min_health_queries` queries and SERVFAIL/queries >
    `max_servfail_ratio` -> `halted`; otherwise -> `rolling`.
  - `rolling`: rejection by any engine -> `halted`; every connected engine
    applied -> `completed` (sets `stable_version`); `ack_timeout_seconds`
    elapsed -> `halted`. Disconnected engines get the version on reconnect.
  - A new `change` supersedes the group's `pending` rollouts when the group is
    paused, and its `pending`/`canary`/`verifying`/`rolling`/`halted` rollouts
    otherwise. A `rollback` marks the `halted` rollout `rolled_back`,
    supersedes the rest, and sets `rollouts_paused`. A `republish` (group move)
    supersedes every non-terminal rollout. `resume-rollouts` clears
    `rollouts_paused` and publishes a fresh `change` from the current rows.
  - Target version of an engine, given the group's newest rollout R:
    R `rolling`/`completed` -> R.version; R `canary`/`verifying` and the engine
    is a canary -> R.version; R `halted` and the engine applied R.version ->
    R.version; otherwise `stable_version`. An engine whose applied version is
    above its target is flagged `ahead` and never pushed.
  - Every instance runs the controller: tick `NEXORA_ROLLOUT_TICK` plus LISTEN
    `nexora_rollout`. Per non-terminal rollout: `BEGIN`,
    `pg_try_advisory_xact_lock(hashtext('rollout:' || id))` (skip when not
    acquired), `SELECT ... FOR UPDATE`, `rollout.Step` with `now()` read from
    Postgres, `UPDATE`, `pg_notify('nexora_rollout', group_id)`, `COMMIT`.
    Instances push on `nexora_rollout` to their connected engines of that group
    whose target is above the last version sent. The M1 `nexora_config`
    channel is retired.

  ### Fleet health

  - `Stats` carries `FleetHealth` (cumulative queries, SERVFAIL, cache hits,
    cache misses; p50/p99 latency of the last interval). The instance holding
    the stream writes `engine_health_samples` and `engines.last_seen_at` on
    every `Stats`, `Hello`, `Applied` and `Rejected`. Samples older than 24 h
    are deleted every 5 min under `pg_try_advisory_xact_lock(hashtext('fleet:prune'))`.
    Acks and Rejected notify `nexora_rollout_tick` so controllers step at once.
  - `connection_state`: `revoked` (engine revoked), `connected`
    (`last_seen_at` within 60 s), `never_connected` (no `last_seen_at`),
    `disconnected` (otherwise). `drift`: `rejected` (rejected its target),
    `in_sync`, `behind`, `ahead` (applied vs target), `unknown` (never
    connected).
  - Metrics on every instance, refreshed every 15 s from Postgres:
    `nexora_mgmt_engines_disconnected` (non-revoked engines unseen for more than
    60 s, including never-connected engines enrolled more than 60 s ago),
    `nexora_mgmt_engines{group,state}`, `nexora_mgmt_engine_drift{group,drift}`,
    `nexora_mgmt_rollouts{state}` (non-terminal plus halted).

  ### Engine lifecycle

  - Join tokens carry `group_id`, `labels`, `expires_at` (TTL 60 s .. 30 d,
    default 24 h), `max_uses` (default 1), `uses`, `revoked_at`. Enroll errors
    (gRPC `Unauthenticated`): `join token unknown`, `join token expired`,
    `join token exhausted`, `join token revoked`.
  - `engine_certificates(serial, engine_id, not_before, not_after, issued_at,
    revoked_at, revoke_reason)`; serial is lowercase hex. Engine certificate
    lifetime is `NEXORA_ENGINE_CERT_TTL`.
  - Renewal: from 2/3 of the certificate lifetime the engine sends
    `CertificateRequest{csr_der, reason: RENEWAL}` on the stream with a new
    P-256 key; the server issues and answers `CertificateIssued`; the engine
    swaps `state_dir/identity` atomically (`identity.new` -> rename) and
    reconnects. On the first `Connect` with a newer serial the server marks the
    engine's older serials `superseded`.
  - Rotation: `POST /api/v1/engines/{engineId}/rotate-certificate` sets
    `cert_rotate_requested_at` and notifies `nexora_engine_rotate`; the
    instance holding the stream sends `RenewCertificate{reason: ROTATE}` (also
    sent on `Hello` while a request is outstanding); issuing clears it.
  - Revocation: `POST /api/v1/engines/{engineId}/revoke` sets
    `engines.revoked_at`, revokes every certificate (`revoked`), notifies
    `nexora_engine_revoked`; instances close that engine's streams with
    `PermissionDenied` `certificate revoked`. `Connect` checks Postgres on
    every call; `GetBlob` and the OTLP `LogsService` use a 5 s per-instance
    cache invalidated by the notification. A certificate that chains to the CA
    but is missing from the table (issued before M5) is recorded on first use.
    A revoked engine keeps serving its last snapshot, sets
    `nexora_control_revoked 1` and retries every 300 s (±10% jitter); joining
    again needs `state_dir/identity` removed and a new join token (new engine
    id). Engines are never deleted automatically.

  ### Distribution

  - `.github/workflows/images.yml` builds `nexora-engine` and `nexora-mgmt`
    per architecture on `arc-azrtydxb-publish` (arm64) and
    `arc-azrtydxb-amd64-publish` (amd64), pushes by digest to
    `192.168.10.131:5000/azrtydxb`, and merges digests into `:sha-<7>` (and
    `:main` on main).
  - `deploy/compose/` runs Postgres, one mgmt, one engine and an optional
    OpenTelemetry Collector (profile `otel`).
  - `deploy/helm/nexora`: mgmt Deployment (N replicas, `migrate` init
    container), engine workloads per group (`Deployment` or `DaemonSet`,
    optional `hostNetwork`), CNPG `Cluster` or an external database secret,
    optional OpenTelemetry Collector, ServiceMonitor and PrometheusRule,
    `values.schema.json`. The CA is always an existing secret created with
    `nexora-mgmt ca init`. Static checks live in `deploy/deploytest`.
  - Operational CLI: `nexora-mgmt group create`, `nexora-mgmt join-token
    create`, `nexora-mgmt api-token create`, `nexora-mgmt ca init --if-missing`.
  ```
- [ ] Replace the body of `## Deployment on kw` with:
  ```markdown
  Namespace `nexora`, installed from `deploy/helm/nexora` with
  `deploy/kw/values-kw.yaml` by `scripts/kw-deploy.sh`: CNPG cluster
  `nexora-db` (2 instances, `longhorn-single`), `nexora-mgmt` Deployment
  (2 replicas) behind ingress `nexora.kw.local` (class `nginx`, ClusterIssuer
  `cluster-ca`) and a LoadBalancer `192.168.10.135:9443` for gRPC; engines in
  two groups on distinct worker nodes (required pod anti-affinity):
  `edge-a` (2 replicas, LoadBalancer `192.168.10.136`) and `edge-b`
  (1 replica, LoadBalancer `192.168.10.137`), state on hostPath
  `/var/lib/nexora/<release>-<group>`; OpenTelemetry Collector sending traces to
  `jaeger.observability:4317` and query logs to OpenSearch (`deploy/kw/opensearch.yaml`,
  single node); ServiceMonitor and PrometheusRule in namespace `monitoring`
  with label `release: kps`; `nexora-fixture` for blocklist and primary-zone
  fixtures. Acceptance: `scripts/kw-acceptance.sh` runs `TestKwFullProduct`.
  ```
- [ ] Add to the repository layout block: `mgmt/internal/rollout                    staged rollout state machine + controller`, `mgmt/internal/fleet                      groups, engines, health samples, fleet metrics, join tokens`, `deploy/deploytest/                       helm/compose/workflow static tests`, `deploy/docker/fixture.Dockerfile         fixture image for kw acceptance`, and in the Contract section the line `- M5 adds FleetHealth (Stats field 100), CertificateRequest (EngineMessage 100), CertificateIssued and RenewCertificate (ServerMessage 100, 101).`
- [ ] Run `grep -c "^## Fleet (M5)" docs/architecture.md` and expect `1`; run `grep -c "192.168.10.13[5-7]" docs/architecture.md` and expect `3`.
- [ ] Commit: `git add docs/architecture.md && git commit -m "docs(architecture): settle M5 fleet design"`.

## Task 2: Fleet schema migration

Files: `mgmt/migrations/00500_fleet.sql` (all M5 tables and columns), `mgmt/internal/store/fleet.go` (fleet schema constants), `mgmt/internal/store/fleet_migration_test.go` (schema test)
Interfaces: `store.DefaultGroupID uuid.UUID`; `store.ScopedConfigTables []string`; tables and columns exactly as in the migration below.

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
  	db := storetest.NewDB(t)

  	var name string
  	if err := db.QueryRow(ctx, `SELECT name FROM engine_groups WHERE id = $1`, store.DefaultGroupID).Scan(&name); err != nil || name != "default" {
  		t.Fatalf("default group: name=%q err=%v", name, err)
  	}

  	hasColumn := func(table, column string) bool {
  		var n int
  		err := db.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
  			WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2`, table, column).Scan(&n)
  		return err == nil && n == 1
  	}
  	for _, table := range store.ScopedConfigTables {
  		if !hasColumn(table, "group_id") {
  			t.Errorf("%s.group_id missing", table)
  		}
  	}
  	for _, c := range []string{"group_id", "labels", "last_seen_at", "connected_instance", "revoked_at",
  		"cert_rotate_requested_at", "applied_version", "rejected_version", "rejected_reason", "revision"} {
  		if !hasColumn("engines", c) {
  			t.Errorf("engines.%s missing", c)
  		}
  	}
  	for _, c := range []string{"group_id", "expires_at", "max_uses", "uses", "labels", "revoked_at"} {
  		if !hasColumn("join_tokens", c) {
  			t.Errorf("join_tokens.%s missing", c)
  		}
  	}
  	for _, tbl := range []string{"group_snapshots", "rollouts", "engine_health_samples", "engine_certificates"} {
  		var reg *string
  		if err := db.QueryRow(ctx, `SELECT to_regclass('public.' || $1)::text`, tbl).Scan(&reg); err != nil || reg == nil {
  			t.Errorf("table %s missing (err=%v)", tbl, err)
  		}
  	}

  	var def string
  	if err := db.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE indexname = 'rollouts_one_active_per_group'`).Scan(&def); err != nil ||
  		!strings.Contains(def, "UNIQUE") || !strings.Contains(def, "verifying") {
  		t.Errorf("rollouts_one_active_per_group: %q err=%v", def, err)
  	}

  	// Positive path first: a valid canary configuration is accepted.
  	if _, err := db.Exec(ctx, `UPDATE engine_groups SET rollout_strategy = 'canary', canary_count = 1 WHERE id = $1`, store.DefaultGroupID); err != nil {
  		t.Fatalf("valid canary rejected: %v", err)
  	}
  	if _, err := db.Exec(ctx, `UPDATE engine_groups SET canary_count = 0, canary_percent = 0 WHERE id = $1`, store.DefaultGroupID); err == nil {
  		t.Fatal("canary strategy without a canary size was accepted")
  	}
  	if _, err := db.Exec(ctx, `INSERT INTO engine_groups (name) VALUES ('Bad_Name')`); err == nil {
  		t.Fatal("group name outside [a-z0-9-] was accepted")
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/store/ -run TestFleetMigration -count=1` and expect FAIL with `undefined: store.DefaultGroupID`.
- [ ] Create `mgmt/internal/store/fleet.go`:
  ```go
  package store

  import "github.com/google/uuid"

  // DefaultGroupID is the engine group every engine and join token falls back to.
  var DefaultGroupID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

  // ScopedConfigTables are the config resource tables that can be global
  // (group_id IS NULL) or belong to one engine group.
  var ScopedConfigTables = []string{
  	"upstreams", "acl_entries", "filter_lists", "client_policies",
  	"rewrites", "zones", "rpz_zones", "telemetry_settings",
  }
  ```
- [ ] Create `mgmt/migrations/00500_fleet.sql`:
  ```sql
  -- +goose Up
  -- +goose StatementBegin
  CREATE TABLE engine_groups (
      id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
      name                  text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
      description           text NOT NULL DEFAULT '' CHECK (length(description) <= 1024),
      upstream_mode         text NOT NULL DEFAULT 'inherit' CHECK (upstream_mode IN ('inherit', 'override')),
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
  VALUES ('00000000-0000-0000-0000-000000000001', 'default', 'Engines not assigned to another group');

  ALTER TABLE engines
      ADD COLUMN IF NOT EXISTS group_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000001'
          REFERENCES engine_groups (id) ON DELETE RESTRICT,
      ADD COLUMN IF NOT EXISTS labels jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(labels) = 'object'),
      ADD COLUMN IF NOT EXISTS last_seen_at timestamptz,
      ADD COLUMN IF NOT EXISTS connected_instance text,
      ADD COLUMN IF NOT EXISTS revoked_at timestamptz,
      ADD COLUMN IF NOT EXISTS cert_rotate_requested_at timestamptz,
      ADD COLUMN IF NOT EXISTS applied_version bigint NOT NULL DEFAULT 0,
      ADD COLUMN IF NOT EXISTS rejected_version bigint,
      ADD COLUMN IF NOT EXISTS rejected_reason text,
      ADD COLUMN IF NOT EXISTS revision bigint NOT NULL DEFAULT 1,
      ADD COLUMN IF NOT EXISTS created_at timestamptz NOT NULL DEFAULT now();
  CREATE INDEX engines_group_id_idx ON engines (group_id);

  DO $$
  DECLARE t text;
  BEGIN
      FOREACH t IN ARRAY ARRAY['upstreams', 'acl_entries', 'filter_lists', 'client_policies',
                               'rewrites', 'zones', 'rpz_zones', 'telemetry_settings'] LOOP
          IF to_regclass('public.' || t) IS NULL THEN
              RAISE EXCEPTION 'fleet migration: config table % not found; align 00500_fleet.sql with mgmt/migrations', t;
          END IF;
          EXECUTE format('ALTER TABLE %I ADD COLUMN group_id uuid REFERENCES engine_groups (id) ON DELETE RESTRICT', t);
          EXECUTE format('CREATE INDEX %I ON %I (group_id)', t || '_group_id_idx', t);
      END LOOP;
  END $$;

  -- telemetry_settings was a singleton; it becomes one row per scope.
  ALTER TABLE telemetry_settings DROP CONSTRAINT IF EXISTS telemetry_settings_pkey;
  ALTER TABLE telemetry_settings
      ADD COLUMN scope_key uuid GENERATED ALWAYS AS (coalesce(group_id, '00000000-0000-0000-0000-000000000000'::uuid)) STORED;
  ALTER TABLE telemetry_settings ADD CONSTRAINT telemetry_settings_pkey PRIMARY KEY (scope_key);

  -- zones: one name per group, never both global and group-scoped (the latter enforced by the API).
  ALTER TABLE zones DROP CONSTRAINT IF EXISTS zones_name_key;
  CREATE UNIQUE INDEX zones_name_scope_uq ON zones (lower(name), coalesce(group_id, '00000000-0000-0000-0000-000000000000'::uuid));

  ALTER TABLE config_versions ALTER COLUMN snapshot DROP NOT NULL;

  CREATE TABLE group_snapshots (
      version    bigint NOT NULL REFERENCES config_versions (version) ON DELETE CASCADE,
      group_id   uuid NOT NULL REFERENCES engine_groups (id) ON DELETE CASCADE,
      snapshot   bytea NOT NULL,
      sha256     text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
      created_at timestamptz NOT NULL DEFAULT now(),
      PRIMARY KEY (version, group_id)
  );
  CREATE INDEX group_snapshots_group_version_idx ON group_snapshots (group_id, version DESC);
  INSERT INTO group_snapshots (version, group_id, snapshot, sha256)
  SELECT version, '00000000-0000-0000-0000-000000000001', snapshot, encode(sha256(snapshot), 'hex')
  FROM config_versions WHERE snapshot IS NOT NULL ORDER BY version DESC LIMIT 1;
  UPDATE engine_groups SET stable_version = (SELECT max(version) FROM group_snapshots)
  WHERE id = '00000000-0000-0000-0000-000000000001';

  CREATE TABLE rollouts (
      id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
      group_id          uuid NOT NULL REFERENCES engine_groups (id) ON DELETE CASCADE,
      version           bigint NOT NULL,
      from_version      bigint,
      kind              text NOT NULL CHECK (kind IN ('change', 'rollback', 'republish')),
      strategy          text NOT NULL CHECK (strategy IN ('all_at_once', 'canary')),
      state             text NOT NULL DEFAULT 'pending' CHECK (state IN
                          ('pending', 'canary', 'verifying', 'rolling', 'completed', 'halted', 'rolled_back', 'superseded')),
      params            jsonb NOT NULL,
      canary_engine_ids uuid[] NOT NULL DEFAULT '{}',
      phase_started_at  timestamptz,
      halt_reason       text NOT NULL DEFAULT '',
      created_by        text NOT NULL,
      created_at        timestamptz NOT NULL DEFAULT now(),
      updated_at        timestamptz NOT NULL DEFAULT now(),
      finished_at       timestamptz,
      FOREIGN KEY (version, group_id) REFERENCES group_snapshots (version, group_id) ON DELETE CASCADE
  );
  CREATE UNIQUE INDEX rollouts_one_active_per_group ON rollouts (group_id)
      WHERE state IN ('canary', 'verifying', 'rolling');
  CREATE INDEX rollouts_group_created_idx ON rollouts (group_id, created_at DESC);
  CREATE INDEX rollouts_open_idx ON rollouts (created_at) WHERE state IN ('pending', 'canary', 'verifying', 'rolling');

  CREATE TABLE engine_health_samples (
      engine_id          uuid NOT NULL REFERENCES engines (id) ON DELETE CASCADE,
      at                 timestamptz NOT NULL,
      applied_version    bigint NOT NULL,
      queries_total      bigint NOT NULL,
      servfail_total     bigint NOT NULL,
      cache_hits_total   bigint NOT NULL,
      cache_misses_total bigint NOT NULL,
      latency_p50_us     integer NOT NULL,
      latency_p99_us     integer NOT NULL,
      PRIMARY KEY (engine_id, at)
  );
  CREATE INDEX engine_health_samples_at_idx ON engine_health_samples (at);

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
  CREATE INDEX engine_certificates_engine_idx ON engine_certificates (engine_id, issued_at DESC);

  ALTER TABLE join_tokens
      ADD COLUMN IF NOT EXISTS group_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000001'
          REFERENCES engine_groups (id) ON DELETE CASCADE,
      ADD COLUMN IF NOT EXISTS expires_at timestamptz NOT NULL DEFAULT now() + interval '24 hours',
      ADD COLUMN IF NOT EXISTS max_uses integer NOT NULL DEFAULT 1 CHECK (max_uses >= 1),
      ADD COLUMN IF NOT EXISTS uses integer NOT NULL DEFAULT 0 CHECK (uses >= 0),
      ADD COLUMN IF NOT EXISTS labels jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(labels) = 'object'),
      ADD COLUMN IF NOT EXISTS revoked_at timestamptz;
  -- +goose StatementEnd

  -- +goose Down
  -- +goose StatementBegin
  ALTER TABLE join_tokens DROP COLUMN revoked_at, DROP COLUMN labels, DROP COLUMN uses,
      DROP COLUMN max_uses, DROP COLUMN expires_at, DROP COLUMN group_id;
  DROP TABLE engine_certificates;
  DROP TABLE engine_health_samples;
  DROP TABLE rollouts;
  DROP TABLE group_snapshots;
  ALTER TABLE telemetry_settings DROP CONSTRAINT telemetry_settings_pkey;
  ALTER TABLE telemetry_settings DROP COLUMN scope_key;
  DROP INDEX zones_name_scope_uq;
  ALTER TABLE zones ADD CONSTRAINT zones_name_key UNIQUE (name);
  DO $$
  DECLARE t text;
  BEGIN
      FOREACH t IN ARRAY ARRAY['upstreams', 'acl_entries', 'filter_lists', 'client_policies',
                               'rewrites', 'zones', 'rpz_zones', 'telemetry_settings'] LOOP
          EXECUTE format('ALTER TABLE %I DROP COLUMN group_id', t);
      END LOOP;
  END $$;
  ALTER TABLE engines DROP COLUMN group_id, DROP COLUMN labels, DROP COLUMN connected_instance,
      DROP COLUMN revoked_at, DROP COLUMN cert_rotate_requested_at;
  DROP TABLE engine_groups;
  -- +goose StatementEnd
  ```
  When Task 1 recorded that `telemetry_settings` had its singleton enforced by something other than its primary key (for example a `CHECK (id)` constraint), drop that constraint in the same migration by its name from `\d telemetry_settings`. The Down section keeps M1 columns that existed before (`last_seen_at`, `applied_version`, `rejected_*`, `revision`, `created_at` were added with `IF NOT EXISTS`) and does not drop them.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/store/ -run TestFleetMigration -count=1` and expect `ok`.
- [ ] Add the down/up round trip to the same test file and run it:
  ```go
  func TestFleetMigrationDownUp(t *testing.T) {
  	ctx := context.Background()
  	db := storetest.NewDB(t)
  	if err := store.MigrateDownTo(ctx, db, 499); err != nil {
  		t.Fatalf("down to 499: %v", err)
  	}
  	var reg *string
  	if err := db.QueryRow(ctx, `SELECT to_regclass('public.engine_groups')::text`).Scan(&reg); err != nil || reg != nil {
  		t.Fatalf("engine_groups still present after down: %v %v", reg, err)
  	}
  	if err := store.Migrate(ctx, db); err != nil {
  		t.Fatalf("up again: %v", err)
  	}
  }
  ```
  `store.MigrateDownTo(ctx, pool, version int64) error` wraps `goose.DownTo` with the embedded migrations FS the M1 `store.Migrate` already uses (add it to `mgmt/internal/store/fleet.go` if M1 has no such helper). Run `scripts/dev-exec.sh go test ./mgmt/internal/store/ -run 'TestFleetMigration' -count=1` and expect `ok`.
- [ ] Commit (all files of this task): `git add mgmt/migrations/00500_fleet.sql mgmt/internal/store/fleet.go mgmt/internal/store/fleet_migration_test.go && git commit -m "feat(store): fleet schema (groups, rollouts, health, certificates)"`.

## Task 3: Contract additions for fleet health and certificate renewal

Files: `proto/nexora/control/v1/control.proto` (contract), `gen/go/nexora/control/v1/*.pb.go` (regenerated, committed), `gen/go/nexora/control/v1/fleet_contract_test.go` (wire round-trip test)
Interfaces: Go `controlv1.FleetHealth{QueriesTotal, ServfailTotal, CacheHitsTotal, CacheMissesTotal uint64; LatencyP50Us, LatencyP99Us uint32}`, `controlv1.Stats.Health`, `controlv1.CertificateRequest{CsrDer []byte; Reason CertificateRequest_Reason}`, `controlv1.CertificateIssued{CertDer, CaDer []byte}`, `controlv1.RenewCertificate{Reason CertificateRequest_Reason}`, oneof wrappers `EngineMessage_CertRequest`, `ServerMessage_CertIssued`, `ServerMessage_RenewCertificate`; Rust `pb::FleetHealth`, `pb::CertificateRequest`, `pb::engine_message::Msg::CertRequest`, `pb::server_message::Msg::{CertIssued, RenewCertificate}` (tonic-prost codegen in `engine/build.rs`).

- [ ] Write the failing test `gen/go/nexora/control/v1/fleet_contract_test.go`:
  ```go
  package controlv1_test

  import (
  	"testing"

  	"google.golang.org/protobuf/proto"

  	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
  )

  func TestFleetContractRoundTrip(t *testing.T) {
  	in := &controlv1.EngineMessage{Msg: &controlv1.EngineMessage_CertRequest{CertRequest: &controlv1.CertificateRequest{
  		CsrDer: []byte{0x30, 0x01}, Reason: controlv1.CertificateRequest_REASON_ROTATE}}}
  	raw, err := proto.Marshal(in)
  	if err != nil {
  		t.Fatal(err)
  	}
  	var out controlv1.EngineMessage
  	if err := proto.Unmarshal(raw, &out); err != nil {
  		t.Fatal(err)
  	}
  	if got := out.GetCertRequest().GetReason(); got != controlv1.CertificateRequest_REASON_ROTATE {
  		t.Fatalf("reason = %v", got)
  	}

  	srv := &controlv1.ServerMessage{Msg: &controlv1.ServerMessage_RenewCertificate{RenewCertificate: &controlv1.RenewCertificate{
  		Reason: controlv1.CertificateRequest_REASON_RENEWAL}}}
  	if _, err := proto.Marshal(srv); err != nil {
  		t.Fatal(err)
  	}
  	issued := &controlv1.ServerMessage{Msg: &controlv1.ServerMessage_CertIssued{CertIssued: &controlv1.CertificateIssued{CertDer: []byte{1}, CaDer: []byte{2}}}}
  	if _, err := proto.Marshal(issued); err != nil {
  		t.Fatal(err)
  	}

  	stats := &controlv1.Stats{Health: &controlv1.FleetHealth{QueriesTotal: 1000, ServfailTotal: 7,
  		CacheHitsTotal: 900, CacheMissesTotal: 100, LatencyP50Us: 250, LatencyP99Us: 5000}}
  	raw, _ = proto.Marshal(stats)
  	var back controlv1.Stats
  	if err := proto.Unmarshal(raw, &back); err != nil || back.GetHealth().GetServfailTotal() != 7 {
  		t.Fatalf("stats round trip: %v %v", back.GetHealth(), err)
  	}
  	if n := stats.ProtoReflect().Descriptor().Fields().ByName("health").Number(); n != 100 {
  		t.Fatalf("Stats.health field number = %d, want 100", n)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./gen/go/nexora/control/v1/ -run TestFleetContractRoundTrip -count=1` and expect FAIL with `undefined: controlv1.EngineMessage_CertRequest`.
- [ ] Append to `proto/nexora/control/v1/control.proto`:
  ```proto
  // ---- M5 fleet additions. Field numbers >= 100 are reserved for M5. ----

  // Cumulative counters since engine start plus latency of the last Stats interval.
  message FleetHealth {
    uint64 queries_total = 1;
    uint64 servfail_total = 2;
    uint64 cache_hits_total = 3;
    uint64 cache_misses_total = 4;
    uint32 latency_p50_us = 5;
    uint32 latency_p99_us = 6;
  }

  // Engine -> server: a CSR for a new P-256 key. The CSR subject CN must be the engine id.
  message CertificateRequest {
    enum Reason {
      REASON_UNSPECIFIED = 0;
      REASON_RENEWAL = 1;
      REASON_ROTATE = 2;
    }
    bytes csr_der = 1;
    Reason reason = 2;
  }

  // Server -> engine: the issued certificate and the CA that signed it.
  message CertificateIssued {
    bytes cert_der = 1;
    bytes ca_der = 2;
  }

  // Server -> engine: send a CertificateRequest now.
  message RenewCertificate {
    CertificateRequest.Reason reason = 1;
  }
  ```
  and add the fields inside the existing messages: in `message Stats` the line `FleetHealth health = 100;`; inside `oneof msg` of `message EngineMessage` the line `CertificateRequest cert_request = 100;`; inside `oneof msg` of `message ServerMessage` the lines `CertificateIssued cert_issued = 100;` and `RenewCertificate renew_certificate = 101;`.
- [ ] Run `scripts/dev-exec.sh make proto` and expect exit 0 with regenerated files under `gen/go/nexora/control/v1/`; then `scripts/dev-exec.sh buf lint proto` and expect no output.
- [ ] Run `scripts/dev-exec.sh 'go test ./gen/go/nexora/control/v1/ -run TestFleetContractRoundTrip -count=1 && cargo check --manifest-path engine/Cargo.toml'` and expect `ok` and `Finished`.
- [ ] Commit: `git add proto/nexora/control/v1/control.proto gen/go/nexora/control/v1 && git commit -m "feat(proto): fleet health and certificate renewal messages"`.

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
type Rollout struct { ID, GroupID uuid.UUID; Version int64; Kind Kind; State State; CanaryEngineIDs []uuid.UUID; PhaseStartedAt time.Time; HaltReason string; Params Params }
type Health struct { Samples int; Queries, Servfail uint64 }
type Engine struct { ID uuid.UUID; Name string; Labels map[string]string; Connected bool; AppliedVersion, RejectedVersion int64; RejectedReason string; Health Health }
type Observation struct { Now time.Time; GroupPaused bool; Engines []Engine }
func Step(r Rollout, obs Observation) (Rollout, bool)
func SelectCanaries(p Params, engines []Engine) []uuid.UUID
func Target(e Engine, stable int64, latest *Rollout) int64
```

- [ ] Write the failing test `mgmt/internal/rollout/rollout_test.go`:
  ```go
  package rollout

  import (
  	"strings"
  	"testing"
  	"time"

  	"github.com/google/uuid"
  )

  var t0 = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

  func eng(name string, connected bool, applied int64) Engine {
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
  	r, es := verifyingWith(Health{Samples: 1, Queries: 5000, Servfail: 0})
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
  	if Target(b, 6, &Rollout{Version: 7, State: Rolling}) != 7 {
  		t.Fatal("rolling: everyone targets the new version")
  	}
  	if Target(b, 6, &Rollout{Version: 8, State: Pending}) != 6 || Target(b, 6, nil) != 6 {
  		t.Fatal("pending or no rollout: stable")
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/rollout/ -count=1` and expect FAIL with `undefined: Rollout`.
- [ ] Create `mgmt/internal/rollout/rollout.go`:
  ```go
  // Package rollout is the staged-rollout state machine. Step is pure: the
  // controller loads a rollout and an observation of its group under an
  // advisory lock, calls Step, and persists the result.
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
  	GroupID         uuid.UUID
  	Version         int64
  	Kind            Kind
  	State           State
  	CanaryEngineIDs []uuid.UUID
  	PhaseStartedAt  time.Time
  	HaltReason      string
  	Params          Params
  }

  // Health is measured from the newest sample taken at most 60 s before the
  // phase start (baseline) to the newest sample; Samples counts both ends.
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
  	AppliedVersion  int64
  	RejectedVersion int64
  	RejectedReason  string
  	Health          Health
  }

  type Observation struct {
  	Now         time.Time
  	GroupPaused bool
  	Engines     []Engine // non-revoked engines of the rollout's group
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

  // firstUnapplied returns the first engine that has not applied r.Version.
  // connectedOnly skips disconnected engines (rolling phase); canaries must
  // apply even when their stream dropped.
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
  	want := p.CanaryCount
  	if byPct := int(math.Ceil(float64(n) * float64(p.CanaryPercent) / 100)); byPct > want {
  		want = byPct
  	}
  	want = max(want, 1)
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

  // Target is the version engine e should run; latest is the group's newest rollout.
  func Target(e Engine, stable int64, latest *Rollout) int64 {
  	if latest == nil {
  		return stable
  	}
  	switch latest.State {
  	case Rolling, Completed:
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
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/rollout/ -count=1 -race` and expect `ok`.
- [ ] Mutation check: temporarily change `ratio > r.Params.MaxServfailRatio` to `ratio >= 1` and run the same command; expect FAIL with `--- FAIL: TestHaltsOnServfailRatio`. Restore the line and expect `ok` again.
- [ ] Commit: `git add mgmt/internal/rollout && git commit -m "feat(rollout): staged rollout state machine"`.

## Task 5: Per-group snapshots and publishing

Files: `mgmt/internal/snapshot/scope.go` (scope type and merge rules), `mgmt/internal/snapshot/builder.go` (M1 builder becomes per group), `mgmt/internal/snapshot/publish.go` (Publish, Republish), `mgmt/internal/rollout/store.go` (rollout creation and supersede rules), `mgmt/internal/snapshot/scope_test.go`, `mgmt/internal/snapshot/publish_test.go`, every M1–M4 mutation handler under `mgmt/internal/api/` that calls `snapshot.Publish` (pass scopes)
Interfaces:
```go
package snapshot
type Scope struct{ GroupID *uuid.UUID } // nil = global
func Global() Scope
func Group(id uuid.UUID) Scope
func ScopeOf(groupID *uuid.UUID) Scope
type Scoped interface{ ScopeGroup() *uuid.UUID }
func ForGroup[T Scoped](rows []T, group uuid.UUID) []T
func UpstreamsForGroup[T Scoped](rows []T, group uuid.UUID, mode string) []T
func SettingsForGroup[T Scoped](rows []T, group uuid.UUID) (T, bool)
func ScopeClause(alias string, param int) string
func BuildForGroup(ctx context.Context, tx pgx.Tx, groupID uuid.UUID, version int64) (*controlv1.ConfigSnapshot, error)
func Publish(ctx context.Context, tx pgx.Tx, actor string, scopes ...Scope) (int64, error)
func Republish(ctx context.Context, tx pgx.Tx, actor string, groupID uuid.UUID, fromVersion int64, kind rollout.Kind) (int64, uuid.UUID, error)
var ErrUnknownVersion = errors.New("snapshot: version not found for group")

package rollout
func Create(ctx context.Context, tx pgx.Tx, groupID uuid.UUID, version int64, kind Kind, fromVersion *int64, actor string) (uuid.UUID, error)
```

- [ ] Write the failing pure test `mgmt/internal/snapshot/scope_test.go`:
  ```go
  package snapshot

  import (
  	"slices"
  	"testing"

  	"github.com/google/uuid"
  )

  type row struct {
  	name string
  	g    *uuid.UUID
  }

  func (r row) ScopeGroup() *uuid.UUID { return r.g }

  func names(rs []row) []string {
  	out := []string{}
  	for _, r := range rs {
  		out = append(out, r.name)
  	}
  	return out
  }

  func TestScopeMerge(t *testing.T) {
  	a, b := uuid.New(), uuid.New()
  	rows := []row{{"g1", nil}, {"a1", &a}, {"b1", &b}, {"g2", nil}}

  	if got := names(ForGroup(rows, a)); !slices.Equal(got, []string{"g1", "g2", "a1"}) {
  		t.Errorf("ForGroup = %v", got)
  	}
  	if got := names(UpstreamsForGroup(rows, a, "inherit")); !slices.Equal(got, []string{"a1", "g1", "g2"}) {
  		t.Errorf("inherit = %v", got)
  	}
  	if got := names(UpstreamsForGroup(rows, a, "override")); !slices.Equal(got, []string{"a1"}) {
  		t.Errorf("override = %v", got)
  	}
  	if got := names(UpstreamsForGroup(rows, uuid.New(), "override")); len(got) != 0 {
  		t.Errorf("override without group upstreams = %v, want none (recursion)", got)
  	}
  	if s, ok := SettingsForGroup([]row{{"global", nil}, {"b", &b}}, b); !ok || s.name != "b" {
  		t.Errorf("group settings row must replace global, got %v %v", s, ok)
  	}
  	if s, ok := SettingsForGroup([]row{{"global", nil}, {"b", &b}}, a); !ok || s.name != "global" {
  		t.Errorf("group without settings falls back to global, got %v %v", s, ok)
  	}
  	if _, ok := SettingsForGroup([]row{}, a); ok {
  		t.Error("no rows must report !ok")
  	}
  	if got := ScopeClause("u", 1); got != "(u.group_id IS NULL OR u.group_id = $1)" {
  		t.Errorf("ScopeClause = %q", got)
  	}
  	all, ids := affected([]Scope{Group(a), Group(a), Group(b)})
  	if all || len(ids) != 2 {
  		t.Errorf("affected(groups) = %v %v", all, ids)
  	}
  	if all, _ := affected([]Scope{Group(a), Global()}); !all {
  		t.Error("a global scope must affect every group")
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/snapshot/ -run TestScopeMerge -count=1` and expect FAIL with `undefined: ForGroup`.
- [ ] Create `mgmt/internal/snapshot/scope.go`:
  ```go
  package snapshot

  import (
  	"fmt"

  	"github.com/google/uuid"
  )

  // Scope names where a changed resource lives: nil GroupID is global.
  type Scope struct{ GroupID *uuid.UUID }

  func Global() Scope                    { return Scope{} }
  func Group(id uuid.UUID) Scope         { return Scope{GroupID: &id} }
  func ScopeOf(groupID *uuid.UUID) Scope { return Scope{GroupID: groupID} }

  type Scoped interface{ ScopeGroup() *uuid.UUID }

  func inGroup[T Scoped](r T, g uuid.UUID) bool { return r.ScopeGroup() != nil && *r.ScopeGroup() == g }

  // ForGroup returns global rows, then the group's rows, each in input order.
  func ForGroup[T Scoped](rows []T, group uuid.UUID) []T {
  	out := make([]T, 0, len(rows))
  	for _, r := range rows {
  		if r.ScopeGroup() == nil {
  			out = append(out, r)
  		}
  	}
  	for _, r := range rows {
  		if inGroup(r, group) {
  			out = append(out, r)
  		}
  	}
  	return out
  }

  // UpstreamsForGroup: inherit = group rows then global rows; override = group rows only.
  func UpstreamsForGroup[T Scoped](rows []T, group uuid.UUID, mode string) []T {
  	out := make([]T, 0, len(rows))
  	for _, r := range rows {
  		if inGroup(r, group) {
  			out = append(out, r)
  		}
  	}
  	if mode == "override" {
  		return out
  	}
  	for _, r := range rows {
  		if r.ScopeGroup() == nil {
  			out = append(out, r)
  		}
  	}
  	return out
  }

  // SettingsForGroup returns the group's row when present, else the global row.
  func SettingsForGroup[T Scoped](rows []T, group uuid.UUID) (T, bool) {
  	var global T
  	found := false
  	for _, r := range rows {
  		if inGroup(r, group) {
  			return r, true
  		}
  		if r.ScopeGroup() == nil {
  			global, found = r, true
  		}
  	}
  	return global, found
  }

  // ScopeClause restricts a query on a scoped table to global rows and one group.
  func ScopeClause(alias string, param int) string {
  	return fmt.Sprintf("(%s.group_id IS NULL OR %s.group_id = $%d)", alias, alias, param)
  }

  func affected(scopes []Scope) (all bool, ids []uuid.UUID) {
  	seen := map[uuid.UUID]bool{}
  	for _, s := range scopes {
  		if s.GroupID == nil {
  			return true, nil
  		}
  		if !seen[*s.GroupID] {
  			seen[*s.GroupID] = true
  			ids = append(ids, *s.GroupID)
  		}
  	}
  	return false, ids
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/snapshot/ -run TestScopeMerge -count=1` and expect `ok`.
- [ ] Write the failing database test `mgmt/internal/snapshot/publish_test.go`:
  ```go
  package snapshot_test

  import (
  	"context"
  	"errors"
  	"slices"
  	"testing"

  	"github.com/google/uuid"
  	"github.com/jackc/pgx/v5/pgxpool"
  	"google.golang.org/protobuf/proto"

  	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
  	"github.com/piwi3910/nexora/mgmt/internal/rollout"
  	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
  	"github.com/piwi3910/nexora/mgmt/internal/store"
  	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
  )

  func publish(t *testing.T, db *pgxpool.Pool, scopes ...snapshot.Scope) int64 {
  	t.Helper()
  	ctx := context.Background()
  	tx, err := db.Begin(ctx)
  	if err != nil {
  		t.Fatal(err)
  	}
  	v, err := snapshot.Publish(ctx, tx, "test", scopes...)
  	if err != nil {
  		t.Fatalf("publish: %v", err)
  	}
  	if err := tx.Commit(ctx); err != nil {
  		t.Fatal(err)
  	}
  	return v
  }

  func groupsOf(t *testing.T, db *pgxpool.Pool, v int64) []uuid.UUID {
  	t.Helper()
  	rows, err := db.Query(context.Background(), `SELECT group_id FROM group_snapshots WHERE version = $1 ORDER BY group_id`, v)
  	if err != nil {
  		t.Fatal(err)
  	}
  	var out []uuid.UUID
  	for rows.Next() {
  		var id uuid.UUID
  		if err := rows.Scan(&id); err != nil {
  			t.Fatal(err)
  		}
  		out = append(out, id)
  	}
  	return out
  }

  func stateOf(t *testing.T, db *pgxpool.Pool, group uuid.UUID, v int64) string {
  	t.Helper()
  	var s string
  	if err := db.QueryRow(context.Background(), `SELECT state FROM rollouts WHERE group_id = $1 AND version = $2`, group, v).Scan(&s); err != nil {
  		t.Fatalf("rollout (%s, %d): %v", group, v, err)
  	}
  	return s
  }

  func TestPublishPerGroup(t *testing.T) {
  	ctx := context.Background()
  	db := storetest.NewDB(t)
  	var edge uuid.UUID
  	if err := db.QueryRow(ctx, `INSERT INTO engine_groups (name) VALUES ('edge') RETURNING id`).Scan(&edge); err != nil {
  		t.Fatal(err)
  	}

  	vGlobal := publish(t, db, snapshot.Global())
  	if got := groupsOf(t, db, vGlobal); len(got) != 2 || !slices.Contains(got, edge) || !slices.Contains(got, store.DefaultGroupID) {
  		t.Fatalf("global publish groups = %v", got)
  	}
  	var raw []byte
  	if err := db.QueryRow(ctx, `SELECT snapshot FROM group_snapshots WHERE version = $1 AND group_id = $2`, vGlobal, edge).Scan(&raw); err != nil {
  		t.Fatal(err)
  	}
  	var snap controlv1.ConfigSnapshot
  	if err := proto.Unmarshal(raw, &snap); err != nil || int64(snap.GetVersion()) != vGlobal {
  		t.Fatalf("snapshot version %d, want %d (err %v)", snap.GetVersion(), vGlobal, err)
  	}
  	if stateOf(t, db, edge, vGlobal) != "pending" || stateOf(t, db, store.DefaultGroupID, vGlobal) != "pending" {
  		t.Fatal("each group snapshot needs a pending rollout")
  	}

  	vEdge := publish(t, db, snapshot.Group(edge))
  	if got := groupsOf(t, db, vEdge); len(got) != 1 || got[0] != edge || vEdge <= vGlobal {
  		t.Fatalf("group publish: version %d groups %v", vEdge, got)
  	}
  	if stateOf(t, db, edge, vGlobal) != "superseded" || stateOf(t, db, store.DefaultGroupID, vGlobal) != "pending" {
  		t.Fatal("a group change supersedes only that group's open rollouts")
  	}

  	// Paused group: a new change leaves the halted rollout alone.
  	if _, err := db.Exec(ctx, `UPDATE rollouts SET state = 'halted' WHERE group_id = $1 AND version = $2`, edge, vEdge); err != nil {
  		t.Fatal(err)
  	}
  	if _, err := db.Exec(ctx, `UPDATE engine_groups SET rollouts_paused = true WHERE id = $1`, edge); err != nil {
  		t.Fatal(err)
  	}
  	vPaused := publish(t, db, snapshot.Group(edge))
  	if stateOf(t, db, edge, vEdge) != "halted" || stateOf(t, db, edge, vPaused) != "pending" {
  		t.Fatal("paused: halted must stay halted and the new change must wait")
  	}

  	tx, _ := db.Begin(ctx)
  	vRB, rid, err := snapshot.Republish(ctx, tx, "test", edge, vGlobal, rollout.KindRollback)
  	if err != nil {
  		t.Fatalf("rollback: %v", err)
  	}
  	if err := tx.Commit(ctx); err != nil {
  		t.Fatal(err)
  	}
  	if rid == uuid.Nil || vRB <= vPaused {
  		t.Fatalf("rollback rollout %s version %d", rid, vRB)
  	}
  	if stateOf(t, db, edge, vEdge) != "rolled_back" || stateOf(t, db, edge, vPaused) != "superseded" || stateOf(t, db, edge, vRB) != "pending" {
  		t.Fatal("rollback must mark halted rolled_back and supersede the pending change")
  	}
  	var rbRaw []byte
  	_ = db.QueryRow(ctx, `SELECT snapshot FROM group_snapshots WHERE version = $1 AND group_id = $2`, vRB, edge).Scan(&rbRaw)
  	var rb controlv1.ConfigSnapshot
  	if err := proto.Unmarshal(rbRaw, &rb); err != nil || int64(rb.GetVersion()) != vRB {
  		t.Fatalf("rollback snapshot version %d want %d", rb.GetVersion(), vRB)
  	}
  	rb.Version = snap.Version
  	if !proto.Equal(&rb, &snap) {
  		t.Fatal("rollback snapshot must equal the source snapshot apart from its version")
  	}

  	tx2, _ := db.Begin(ctx)
  	defer tx2.Rollback(ctx)
  	if _, _, err := snapshot.Republish(ctx, tx2, "test", edge, 999999, rollout.KindRollback); !errors.Is(err, snapshot.ErrUnknownVersion) {
  		t.Fatalf("unknown version: err = %v", err)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/snapshot/ -run TestPublishPerGroup -count=1` and expect FAIL with `undefined: snapshot.Republish`.
- [ ] Create `mgmt/internal/rollout/store.go`:
  ```go
  package rollout

  import (
  	"context"
  	"fmt"

  	"github.com/google/uuid"
  	"github.com/jackc/pgx/v5"
  )

  // Create supersedes the group's open rollouts per kind and inserts a pending rollout.
  func Create(ctx context.Context, tx pgx.Tx, groupID uuid.UUID, version int64, kind Kind, fromVersion *int64, actor string) (uuid.UUID, error) {
  	var paused bool
  	if err := tx.QueryRow(ctx, `SELECT rollouts_paused FROM engine_groups WHERE id = $1 FOR UPDATE`, groupID).Scan(&paused); err != nil {
  		return uuid.Nil, fmt.Errorf("rollout: load group %s: %w", groupID, err)
  	}
  	supersede := []string{"pending", "canary", "verifying", "rolling", "halted"}
  	switch {
  	case kind == KindChange && paused:
  		supersede = []string{"pending"}
  	case kind == KindRollback:
  		if _, err := tx.Exec(ctx, `UPDATE rollouts SET state = 'rolled_back', finished_at = now(), updated_at = now()
  			WHERE group_id = $1 AND state = 'halted'`, groupID); err != nil {
  			return uuid.Nil, err
  		}
  		supersede = []string{"pending", "canary", "verifying", "rolling"}
  		if _, err := tx.Exec(ctx, `UPDATE engine_groups SET rollouts_paused = true, updated_at = now() WHERE id = $1`, groupID); err != nil {
  			return uuid.Nil, err
  		}
  	}
  	if _, err := tx.Exec(ctx, `UPDATE rollouts SET state = 'superseded', finished_at = now(), updated_at = now()
  		WHERE group_id = $1 AND state = ANY($2)`, groupID, supersede); err != nil {
  		return uuid.Nil, err
  	}
  	var id uuid.UUID
  	err := tx.QueryRow(ctx, `
  		INSERT INTO rollouts (group_id, version, from_version, kind, strategy, params, created_by)
  		SELECT g.id, $2, $3, $4,
  		       CASE WHEN $4 = 'change' THEN g.rollout_strategy ELSE 'all_at_once' END,
  		       jsonb_build_object(
  		         'strategy', CASE WHEN $4 = 'change' THEN g.rollout_strategy ELSE 'all_at_once' END,
  		         'canary_count', g.canary_count, 'canary_percent', g.canary_percent,
  		         'ack_timeout_seconds', g.ack_timeout_seconds, 'health_window_seconds', g.health_window_seconds,
  		         'max_servfail_ratio', g.max_servfail_ratio, 'min_health_queries', g.min_health_queries),
  		       $5
  		FROM engine_groups g WHERE g.id = $1
  		RETURNING id`, groupID, version, fromVersion, string(kind), actor).Scan(&id)
  	if err != nil {
  		return uuid.Nil, fmt.Errorf("rollout: insert: %w", err)
  	}
  	if _, err := tx.Exec(ctx, `SELECT pg_notify('nexora_rollout', $1::text)`, groupID); err != nil {
  		return uuid.Nil, err
  	}
  	return id, nil
  }
  ```
- [ ] Create `mgmt/internal/snapshot/publish.go`:
  ```go
  package snapshot

  import (
  	"context"
  	"crypto/sha256"
  	"encoding/hex"
  	"errors"
  	"fmt"

  	"github.com/google/uuid"
  	"github.com/jackc/pgx/v5"
  	"google.golang.org/protobuf/proto"

  	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
  	"github.com/piwi3910/nexora/mgmt/internal/rollout"
  )

  var ErrUnknownVersion = errors.New("snapshot: version not found for group")

  func nextVersion(ctx context.Context, tx pgx.Tx, actor string) (int64, error) {
  	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('publish'))`); err != nil {
  		return 0, err
  	}
  	var v int64
  	err := tx.QueryRow(ctx, `INSERT INTO config_versions (created_by) VALUES ($1) RETURNING version`, actor).Scan(&v)
  	return v, err
  }

  func saveGroupSnapshot(ctx context.Context, tx pgx.Tx, group uuid.UUID, snap *controlv1.ConfigSnapshot) error {
  	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(snap)
  	if err != nil {
  		return err
  	}
  	sum := sha256.Sum256(raw)
  	_, err = tx.Exec(ctx, `INSERT INTO group_snapshots (version, group_id, snapshot, sha256) VALUES ($1, $2, $3, $4)`,
  		int64(snap.GetVersion()), group, raw, hex.EncodeToString(sum[:]))
  	return err
  }

  // Publish builds one snapshot per affected group under a new version and
  // creates a pending rollout for each. Call it inside the mutation's transaction.
  func Publish(ctx context.Context, tx pgx.Tx, actor string, scopes ...Scope) (int64, error) {
  	all, groups := affected(scopes)
  	if all || len(scopes) == 0 {
  		groups = nil
  		rows, err := tx.Query(ctx, `SELECT id FROM engine_groups ORDER BY name`)
  		if err != nil {
  			return 0, err
  		}
  		ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
  		if err != nil {
  			return 0, err
  		}
  		groups = ids
  	}
  	version, err := nextVersion(ctx, tx, actor)
  	if err != nil {
  		return 0, fmt.Errorf("publish: version: %w", err)
  	}
  	for _, g := range groups {
  		snap, err := BuildForGroup(ctx, tx, g, version)
  		if err != nil {
  			return 0, fmt.Errorf("publish: build group %s: %w", g, err)
  		}
  		if err := saveGroupSnapshot(ctx, tx, g, snap); err != nil {
  			return 0, fmt.Errorf("publish: store group %s: %w", g, err)
  		}
  		if _, err := rollout.Create(ctx, tx, g, version, rollout.KindChange, nil, actor); err != nil {
  			return 0, err
  		}
  	}
  	return version, nil
  }

  // Republish copies the group's snapshot at fromVersion into a new version.
  func Republish(ctx context.Context, tx pgx.Tx, actor string, groupID uuid.UUID, fromVersion int64, kind rollout.Kind) (int64, uuid.UUID, error) {
  	var raw []byte
  	err := tx.QueryRow(ctx, `SELECT snapshot FROM group_snapshots WHERE version = $1 AND group_id = $2`, fromVersion, groupID).Scan(&raw)
  	if errors.Is(err, pgx.ErrNoRows) {
  		return 0, uuid.Nil, ErrUnknownVersion
  	}
  	if err != nil {
  		return 0, uuid.Nil, err
  	}
  	var snap controlv1.ConfigSnapshot
  	if err := proto.Unmarshal(raw, &snap); err != nil {
  		return 0, uuid.Nil, fmt.Errorf("republish: decode %d: %w", fromVersion, err)
  	}
  	version, err := nextVersion(ctx, tx, actor)
  	if err != nil {
  		return 0, uuid.Nil, err
  	}
  	snap.Version = uint64(version)
  	if err := saveGroupSnapshot(ctx, tx, groupID, &snap); err != nil {
  		return 0, uuid.Nil, err
  	}
  	id, err := rollout.Create(ctx, tx, groupID, version, kind, &fromVersion, actor)
  	return version, id, err
  }
  ```
  Delete M1's former `Publish` body (including its `pg_notify('nexora_config', ...)` and its write of `config_versions.snapshot`). When Task 1 recorded additional NOT NULL columns on `config_versions`, supply them in `nextVersion`'s INSERT.
- [ ] Change M1's `Build(ctx, tx, version)` in `mgmt/internal/snapshot/builder.go` into `BuildForGroup(ctx, tx, groupID, version)`: load `SELECT upstream_mode FROM engine_groups WHERE id = $1`; add `WHERE ` + `ScopeClause(<alias>, 1)` (with `groupID` as `$1`, renumbering existing parameters) to the query of each table in `store.ScopedConfigTables`; add `GroupID *uuid.UUID` plus `func (r X) ScopeGroup() *uuid.UUID { return r.GroupID }` to each row struct; order rows with `ForGroup` (ACL entries, filter lists, client policies, rewrites, zones, RPZ zones), `UpstreamsForGroup(rows, groupID, upstreamMode)` for upstreams and `SettingsForGroup` for telemetry settings; blob references, DNSSEC key material and TSIG keys are collected only from the rows that survived the scope filter.
- [ ] Update call sites: run `scripts/dev-exec.sh 'grep -rn "snapshot.Publish(" mgmt/internal --include=*.go'`; for mutations of a table in `store.ScopedConfigTables` pass `snapshot.ScopeOf(before.GroupID), snapshot.ScopeOf(after.GroupID)` (create: only `after`; delete: only `before`); for every other mutation pass `snapshot.Global()`.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/internal/snapshot/ ./mgmt/internal/rollout/ -count=1 && go vet ./mgmt/...'` and expect `ok` for both packages and no vet output.
- [ ] Commit: `git add mgmt/internal/snapshot mgmt/internal/rollout/store.go mgmt/internal/api && git commit -m "feat(snapshot): per-group snapshots with pending rollouts"`.

## Task 6: Rollout controller, targeted pushes and fleet health

Files: `mgmt/internal/rollout/controller.go` (multi-instance driver), `mgmt/internal/rollout/controller_test.go`, `mgmt/internal/fleet/engines.go` (engine views: connection state, drift, target), `mgmt/internal/fleet/health.go` (heartbeat, acks, samples, health window, prune), `mgmt/internal/fleet/metrics.go` (fleet gauges), `mgmt/internal/fleet/fleet_test.go`, `mgmt/internal/store/storetest/fleet.go` (engine row fixture), `mgmt/internal/control/hub.go` (M1 hub: push by target), `mgmt/internal/control/hub_fleet_test.go`, `mgmt/internal/config/config.go` (new env), `mgmt/cmd/nexora-mgmt/main.go` (start controller and metrics refresher)
Interfaces:
```go
package rollout
type Controller struct { Pool *pgxpool.Pool; Tick time.Duration; Log *slog.Logger }
func (c *Controller) Run(ctx context.Context) error
func (c *Controller) Step(ctx context.Context) (driven int, err error)

package fleet
type Querier interface { Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error); QueryRow(ctx context.Context, sql string, args ...any) pgx.Row; Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) }
type EngineView struct { ID, GroupID uuid.UUID; Name, GroupName string; Labels map[string]string; Revision int64; ConnectionState string; LastSeenAt *time.Time; ConnectedInstance string; AppliedVersion, TargetVersion int64; RejectedVersion *int64; RejectedReason string; Drift string; CreatedAt time.Time }
type EngineFilter struct { GroupID *uuid.UUID; EngineID *uuid.UUID }
func ListEngines(ctx context.Context, q Querier, f EngineFilter) ([]EngineView, error)
func SnapshotFor(ctx context.Context, q Querier, groupID uuid.UUID, version int64) ([]byte, error)
func Heartbeat(ctx context.Context, q Querier, engineID uuid.UUID, instance string) error
func Disconnect(ctx context.Context, q Querier, engineID uuid.UUID, instance string) error
func RecordApplied(ctx context.Context, q Querier, engineID uuid.UUID, version int64, instance string) error
func RecordRejected(ctx context.Context, q Querier, engineID uuid.UUID, version int64, reason, instance string) error
func RecordStats(ctx context.Context, q Querier, engineID uuid.UUID, instance string, applied int64, h *controlv1.FleetHealth) error
func HealthSince(ctx context.Context, q Querier, engineID uuid.UUID, since time.Time) (rollout.Health, error)
func Prune(ctx context.Context, pool *pgxpool.Pool) (deleted int64, err error)
type Metrics struct { Disconnected prometheus.Gauge; Engines, Drift, Rollouts *prometheus.GaugeVec }
func NewMetrics(reg prometheus.Registerer) *Metrics
func (m *Metrics) Refresh(ctx context.Context, q Querier) error

package storetest
func InsertEngine(t *testing.T, db *pgxpool.Pool, name string, group uuid.UUID) uuid.UUID
```

- [ ] Create the fixture `mgmt/internal/store/storetest/fleet.go` (INSERT lists every NOT NULL column without default that Task 1 found on `engines`; the M1 columns assumed are `id` and `name`):
  ```go
  package storetest

  import (
  	"context"
  	"testing"

  	"github.com/google/uuid"
  	"github.com/jackc/pgx/v5/pgxpool"
  )

  // InsertEngine creates an enrolled, never-connected engine row.
  func InsertEngine(t *testing.T, db *pgxpool.Pool, name string, group uuid.UUID) uuid.UUID {
  	t.Helper()
  	id := uuid.New()
  	if _, err := db.Exec(context.Background(),
  		`INSERT INTO engines (id, name, group_id) VALUES ($1, $2, $3)`, id, name, group); err != nil {
  		t.Fatalf("insert engine %s: %v", name, err)
  	}
  	return id
  }
  ```
- [ ] Write the failing test `mgmt/internal/rollout/controller_test.go`:
  ```go
  package rollout_test

  import (
  	"context"
  	"testing"

  	"github.com/google/uuid"
  	"github.com/jackc/pgx/v5/pgxpool"

  	"github.com/piwi3910/nexora/mgmt/internal/rollout"
  	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
  	"github.com/piwi3910/nexora/mgmt/internal/store"
  	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
  )

  func publishDefault(t *testing.T, db *pgxpool.Pool) int64 {
  	t.Helper()
  	ctx := context.Background()
  	tx, err := db.Begin(ctx)
  	if err != nil {
  		t.Fatal(err)
  	}
  	v, err := snapshot.Publish(ctx, tx, "test", snapshot.Group(store.DefaultGroupID))
  	if err != nil {
  		t.Fatal(err)
  	}
  	if err := tx.Commit(ctx); err != nil {
  		t.Fatal(err)
  	}
  	return v
  }

  func TestControllerDrivesUnderAdvisoryLock(t *testing.T) {
  	ctx := context.Background()
  	db := storetest.NewDB(t)
  	v := publishDefault(t, db)
  	var id uuid.UUID
  	if err := db.QueryRow(ctx, `SELECT id FROM rollouts WHERE version = $1`, v).Scan(&id); err != nil {
  		t.Fatal(err)
  	}
  	c := &rollout.Controller{Pool: db}

  	conn, err := db.Acquire(ctx)
  	if err != nil {
  		t.Fatal(err)
  	}
  	holder, err := conn.Begin(ctx)
  	if err != nil {
  		t.Fatal(err)
  	}
  	if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('rollout:' || $1::text))`, id); err != nil {
  		t.Fatal(err)
  	}
  	if n, err := c.Step(ctx); err != nil || n != 0 {
  		t.Fatalf("locked rollout: driven=%d err=%v, want 0 nil", n, err)
  	}
  	_ = holder.Rollback(ctx)
  	conn.Release()

  	if n, err := c.Step(ctx); err != nil || n != 1 {
  		t.Fatalf("pending -> rolling: driven=%d err=%v", n, err)
  	}
  	if n, err := c.Step(ctx); err != nil || n != 1 {
  		t.Fatalf("rolling -> completed: driven=%d err=%v", n, err)
  	}
  	var state string
  	var stable *int64
  	if err := db.QueryRow(ctx, `SELECT r.state, g.stable_version FROM rollouts r JOIN engine_groups g ON g.id = r.group_id WHERE r.id = $1`, id).Scan(&state, &stable); err != nil {
  		t.Fatal(err)
  	}
  	if state != "completed" || stable == nil || *stable != v {
  		t.Fatalf("state %s stable %v, want completed %d", state, stable, v)
  	}
  	if n, _ := c.Step(ctx); n != 0 {
  		t.Fatalf("terminal rollout driven again (%d)", n)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/rollout/ -run TestControllerDrivesUnderAdvisoryLock -count=1` and expect FAIL with `undefined: rollout.Controller`.
- [ ] Create `mgmt/internal/rollout/controller.go`. The engine and health loading lives here as SQL (not via `fleet`, which imports `rollout`):
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
  	"github.com/jackc/pgx/v5/pgxpool"
  )

  type Controller struct {
  	Pool *pgxpool.Pool
  	Tick time.Duration // NEXORA_ROLLOUT_TICK, default 1s
  	Log  *slog.Logger
  }

  // Run steps open rollouts on every tick and on nexora_rollout / nexora_rollout_tick notifications.
  func (c *Controller) Run(ctx context.Context) error {
  	if c.Tick <= 0 {
  		c.Tick = time.Second
  	}
  	if c.Log == nil {
  		c.Log = slog.Default()
  	}
  	wake := make(chan struct{}, 1)
  	go c.listen(ctx, wake)
  	t := time.NewTicker(c.Tick)
  	defer t.Stop()
  	for {
  		if _, err := c.Step(ctx); err != nil && ctx.Err() == nil {
  			c.Log.Warn("rollout step failed", "err", err)
  		}
  		select {
  		case <-ctx.Done():
  			return nil
  		case <-t.C:
  		case <-wake:
  		}
  	}
  }

  func (c *Controller) listen(ctx context.Context, wake chan<- struct{}) {
  	for ctx.Err() == nil {
  		conn, err := c.Pool.Acquire(ctx)
  		if err == nil {
  			_, err = conn.Exec(ctx, `LISTEN nexora_rollout; LISTEN nexora_rollout_tick`)
  			for err == nil {
  				if _, err = conn.Conn().WaitForNotification(ctx); err == nil {
  					select {
  					case wake <- struct{}{}:
  					default:
  					}
  				}
  			}
  			conn.Release()
  		}
  		select {
  		case <-ctx.Done():
  		case <-time.After(time.Second):
  		}
  	}
  }

  // Step drives every open rollout once and returns how many changed state.
  func (c *Controller) Step(ctx context.Context) (int, error) {
  	rows, err := c.Pool.Query(ctx, `SELECT id FROM rollouts WHERE state IN ('pending','canary','verifying','rolling') ORDER BY created_at`)
  	if err != nil {
  		return 0, err
  	}
  	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
  	if err != nil {
  		return 0, err
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
  	tx, err := c.Pool.Begin(ctx)
  	if err != nil {
  		return false, err
  	}
  	defer func() { _ = tx.Rollback(ctx) }()
  	var locked bool
  	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtext('rollout:' || $1::text))`, id).Scan(&locked); err != nil || !locked {
  		return false, err
  	}
  	var (
  		r       Rollout
  		params  []byte
  		phase   *time.Time
  		paused  bool
  		now     time.Time
  	)
  	err = tx.QueryRow(ctx, `
  		SELECT r.id, r.group_id, r.version, r.kind, r.state, r.canary_engine_ids, r.phase_started_at, r.halt_reason, r.params,
  		       g.rollouts_paused, now()
  		FROM rollouts r JOIN engine_groups g ON g.id = r.group_id
  		WHERE r.id = $1 FOR UPDATE OF r`, id).
  		Scan(&r.ID, &r.GroupID, &r.Version, &r.Kind, &r.State, &r.CanaryEngineIDs, &phase, &r.HaltReason, &params, &paused, &now)
  	if err != nil {
  		return false, err
  	}
  	if r.State.Terminal() || r.State == Halted {
  		return false, nil
  	}
  	if phase != nil {
  		r.PhaseStartedAt = *phase
  	}
  	if err := json.Unmarshal(params, &r.Params); err != nil {
  		return false, err
  	}
  	engines, err := loadEngines(ctx, tx, r.GroupID)
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
  	_, err = tx.Exec(ctx, `
  		UPDATE rollouts SET state = $2, canary_engine_ids = $3, phase_started_at = $4, halt_reason = $5, updated_at = now(),
  		       finished_at = CASE WHEN $2 IN ('completed') THEN now() ELSE finished_at END
  		WHERE id = $1`, next.ID, string(next.State), next.CanaryEngineIDs, next.PhaseStartedAt, next.HaltReason)
  	if err != nil {
  		return false, err
  	}
  	if next.State == Completed {
  		if _, err := tx.Exec(ctx, `UPDATE engine_groups SET stable_version = GREATEST(coalesce(stable_version, 0), $2), updated_at = now() WHERE id = $1`,
  			next.GroupID, next.Version); err != nil {
  			return false, err
  		}
  	}
  	if _, err := tx.Exec(ctx, `SELECT pg_notify('nexora_rollout', $1::text)`, next.GroupID); err != nil {
  		return false, err
  	}
  	return true, tx.Commit(ctx)
  }

  func loadEngines(ctx context.Context, tx pgx.Tx, group uuid.UUID) ([]Engine, error) {
  	rows, err := tx.Query(ctx, `
  		SELECT id, name, labels, coalesce(last_seen_at >= now() - interval '60 seconds', false),
  		       applied_version, coalesce(rejected_version, 0), coalesce(rejected_reason, '')
  		FROM engines WHERE group_id = $1 AND revoked_at IS NULL ORDER BY name`, group)
  	if err != nil {
  		return nil, err
  	}
  	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Engine, error) {
  		var e Engine
  		err := row.Scan(&e.ID, &e.Name, &e.Labels, &e.Connected, &e.AppliedVersion, &e.RejectedVersion, &e.RejectedReason)
  		return e, err
  	})
  }

  // HealthSince: baseline = newest sample in (since-60s, since]; then every later sample.
  func HealthSince(ctx context.Context, q interface {
  	Query(context.Context, string, ...any) (pgx.Rows, error)
  }, engine uuid.UUID, since time.Time) (Health, error) {
  	rows, err := q.Query(ctx, `
  		(SELECT at, queries_total, servfail_total FROM engine_health_samples
  		  WHERE engine_id = $1 AND at <= $2 AND at > $2 - interval '60 seconds' ORDER BY at DESC LIMIT 1)
  		UNION ALL
  		(SELECT at, queries_total, servfail_total FROM engine_health_samples
  		  WHERE engine_id = $1 AND at > $2 ORDER BY at)
  		ORDER BY at`, engine, since)
  	if err != nil {
  		return Health{}, err
  	}
  	defer rows.Close()
  	var h Health
  	var firstQ, firstS, lastQ, lastS int64
  	for rows.Next() {
  		var at time.Time
  		var q, s int64
  		if err := rows.Scan(&at, &q, &s); err != nil {
  			return Health{}, err
  		}
  		if h.Samples == 0 {
  			firstQ, firstS = q, s
  		}
  		lastQ, lastS = q, s
  		h.Samples++
  	}
  	if h.Samples >= 2 {
  		if lastQ < firstQ || lastS < firstS { // engine restarted: counters reset
  			firstQ, firstS = 0, 0
  		}
  		h.Queries, h.Servfail = uint64(lastQ-firstQ), uint64(lastS-firstS)
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
  	"testing"
  	"time"

  	"github.com/prometheus/client_golang/prometheus"
  	"github.com/prometheus/client_golang/prometheus/testutil"

  	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
  	"github.com/piwi3910/nexora/mgmt/internal/fleet"
  	"github.com/piwi3910/nexora/mgmt/internal/rollout"
  	"github.com/piwi3910/nexora/mgmt/internal/store"
  	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
  )

  func TestHealthAndDisconnectedGauge(t *testing.T) {
  	ctx := context.Background()
  	db := storetest.NewDB(t)
  	a := storetest.InsertEngine(t, db, "a", store.DefaultGroupID)
  	b := storetest.InsertEngine(t, db, "b", store.DefaultGroupID)
  	c := storetest.InsertEngine(t, db, "c", store.DefaultGroupID)

  	if err := fleet.RecordStats(ctx, db, c, "mgmt-1", 3, &controlv1.FleetHealth{QueriesTotal: 10, ServfailTotal: 1, LatencyP99Us: 900}); err != nil {
  		t.Fatal(err)
  	}
  	views, err := fleet.ListEngines(ctx, db, fleet.EngineFilter{})
  	if err != nil || len(views) != 3 {
  		t.Fatalf("views %v err %v", views, err)
  	}
  	byName := map[string]fleet.EngineView{}
  	for _, v := range views {
  		byName[v.Name] = v
  	}
  	if byName["c"].ConnectionState != "connected" || byName["c"].ConnectedInstance != "mgmt-1" || byName["b"].ConnectionState != "never_connected" {
  		t.Fatalf("states c=%+v b=%+v", byName["c"], byName["b"])
  	}

  	if _, err := db.Exec(ctx, `UPDATE engines SET created_at = now() - interval '2 minutes' WHERE id = $1`, b); err != nil {
  		t.Fatal(err)
  	}
  	m := fleet.NewMetrics(prometheus.NewRegistry())
  	if err := m.Refresh(ctx, db); err != nil {
  		t.Fatal(err)
  	}
  	if got := testutil.ToFloat64(m.Disconnected); got != 1 {
  		t.Fatalf("nexora_mgmt_engines_disconnected = %v, want 1 (b unseen for 2 minutes)", got)
  	}
  	if _, err := db.Exec(ctx, `UPDATE engines SET revoked_at = now() WHERE id = $1`, b); err != nil {
  		t.Fatal(err)
  	}
  	_ = m.Refresh(ctx, db)
  	if got := testutil.ToFloat64(m.Disconnected); got != 0 {
  		t.Fatalf("revoked engines must not count as disconnected, got %v", got)
  	}

  	since := time.Now().Add(-time.Minute)
  	for _, s := range []struct {
  		off  time.Duration
  		q, f int64
  	}{{-15 * time.Second, 100, 0}, {5 * time.Second, 300, 50}, {15 * time.Second, 500, 100}} {
  		if _, err := db.Exec(ctx, `INSERT INTO engine_health_samples VALUES ($1, $2, 3, $3, $4, 0, 0, 0, 0)`, a, since.Add(s.off), s.q, s.f); err != nil {
  			t.Fatal(err)
  		}
  	}
  	h, err := rollout.HealthSince(ctx, db, a, since)
  	if err != nil || h.Samples != 3 || h.Queries != 400 || h.Servfail != 100 {
  		t.Fatalf("health = %+v err %v, want 3 samples, 400 queries, 100 servfail", h, err)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/fleet/ -count=1` and expect FAIL with `undefined: fleet.RecordStats`.
- [ ] Create `mgmt/internal/fleet/health.go` with these statements (each function is a single `Exec` unless noted; `instance` is `NEXORA_INSTANCE_ID`):
  ```sql
  -- Heartbeat
  UPDATE engines SET last_seen_at = now(), connected_instance = $2 WHERE id = $1;
  -- Disconnect (only clears when this instance still owns the stream)
  UPDATE engines SET connected_instance = NULL WHERE id = $1 AND connected_instance = $2;
  -- RecordApplied, then SELECT pg_notify('nexora_rollout_tick', group_id::text) FROM engines WHERE id = $1
  UPDATE engines SET applied_version = $2, last_seen_at = now(), connected_instance = $3,
         rejected_version = CASE WHEN rejected_version <= $2 THEN NULL ELSE rejected_version END,
         rejected_reason  = CASE WHEN rejected_version <= $2 THEN NULL ELSE rejected_reason END
  WHERE id = $1;
  -- RecordRejected, then the same pg_notify
  UPDATE engines SET rejected_version = $2, rejected_reason = left($3, 1024), last_seen_at = now(), connected_instance = $4 WHERE id = $1;
  -- RecordStats (both statements in one transaction)
  INSERT INTO engine_health_samples (engine_id, at, applied_version, queries_total, servfail_total, cache_hits_total,
         cache_misses_total, latency_p50_us, latency_p99_us)
  VALUES ($1, now(), $2, $3, $4, $5, $6, $7, $8) ON CONFLICT DO NOTHING;
  UPDATE engines SET last_seen_at = now(), connected_instance = $2 WHERE id = $1;
  -- Prune (inside a transaction; skip when the lock is not acquired)
  SELECT pg_try_advisory_xact_lock(hashtext('fleet:prune'));
  DELETE FROM engine_health_samples WHERE at < now() - interval '24 hours';
  ```
  `RecordStats` with a nil `FleetHealth` (an engine older than M5) only runs the heartbeat. Values above `math.MaxInt64` are clamped. `HealthSince` calls `rollout.HealthSince`.
- [ ] Create `mgmt/internal/fleet/engines.go`. `ListEngines` runs two queries and computes state in Go:
  ```sql
  SELECT e.id, e.name, e.group_id, g.name, e.labels, e.revision, e.last_seen_at, coalesce(e.connected_instance, ''),
         e.applied_version, e.rejected_version, coalesce(e.rejected_reason, ''), e.revoked_at, e.created_at,
         coalesce(e.last_seen_at >= now() - interval '60 seconds', false)
  FROM engines e JOIN engine_groups g ON g.id = e.group_id
  WHERE ($1::uuid IS NULL OR e.group_id = $1) AND ($2::uuid IS NULL OR e.id = $2)
  ORDER BY g.name, e.name;

  SELECT g.id, coalesce(g.stable_version, 0), r.id, r.version, r.state, r.canary_engine_ids
  FROM engine_groups g
  LEFT JOIN LATERAL (SELECT id, version, state, canary_engine_ids FROM rollouts
                     WHERE group_id = g.id ORDER BY version DESC LIMIT 1) r ON true;
  ```
  Per engine: `TargetVersion = rollout.Target(engine, stable, latest)`; `ConnectionState` = `revoked` if `revoked_at` set, else `connected` if the last column is true, else `never_connected` if `last_seen_at` is NULL, else `disconnected`; `Drift` = `unknown` if never connected, `rejected` if `rejected_version = TargetVersion`, `ahead` if applied > target, `behind` if applied < target, else `in_sync`. `SnapshotFor` returns `SELECT snapshot FROM group_snapshots WHERE group_id = $1 AND version = $2`.
- [ ] Create `mgmt/internal/fleet/metrics.go`: gauges `nexora_mgmt_engines_disconnected` (help "Non-revoked engines not seen for more than 60 seconds"), `nexora_mgmt_engines{group,state}`, `nexora_mgmt_engine_drift{group,drift}`, `nexora_mgmt_rollouts{state}`. `Refresh` computes all of them from `ListEngines` (disconnected = views with state `disconnected`, plus `never_connected` whose `CreatedAt` is more than 60 s before the database `now()` read via `SELECT now()`) and `SELECT state, count(*) FROM rollouts WHERE state IN ('pending','canary','verifying','rolling','halted') GROUP BY state`; it calls `Reset()` on each vector before setting values so vanished groups disappear.
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/fleet/ -count=1` and expect `ok`.
- [ ] Write the failing hub test `mgmt/internal/control/hub_fleet_test.go` (uses the M1 in-process hub test helpers; the test drives `Hub.TargetFor`, the one function the hub uses both on `Hello` and on `nexora_rollout`):
  ```go
  package control_test

  import (
  	"context"
  	"testing"

  	"github.com/piwi3910/nexora/mgmt/internal/control"
  	"github.com/piwi3910/nexora/mgmt/internal/rollout"
  	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
  	"github.com/piwi3910/nexora/mgmt/internal/store"
  	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
  )

  func TestHubTargetsCanariesOnly(t *testing.T) {
  	ctx := context.Background()
  	db := storetest.NewDB(t)
  	a := storetest.InsertEngine(t, db, "a", store.DefaultGroupID)
  	b := storetest.InsertEngine(t, db, "b", store.DefaultGroupID)
  	ctl := &rollout.Controller{Pool: db}

  	tx, _ := db.Begin(ctx)
  	v1, err := snapshot.Publish(ctx, tx, "test", snapshot.Global())
  	if err != nil {
  		t.Fatal(err)
  	}
  	_ = tx.Commit(ctx)
  	for i := 0; i < 3; i++ {
  		_, _ = ctl.Step(ctx)
  	}
  	if _, err := db.Exec(ctx, `UPDATE engines SET last_seen_at = now(), applied_version = $1`, v1); err != nil {
  		t.Fatal(err)
  	}
  	if _, err := db.Exec(ctx, `UPDATE engine_groups SET rollout_strategy = 'canary', canary_count = 1 WHERE id = $1`, store.DefaultGroupID); err != nil {
  		t.Fatal(err)
  	}
  	tx, _ = db.Begin(ctx)
  	v2, _ := snapshot.Publish(ctx, tx, "test", snapshot.Global())
  	_ = tx.Commit(ctx)
  	if _, err := ctl.Step(ctx); err != nil { // pending -> canary, selects "a"
  		t.Fatal(err)
  	}

  	hub := control.NewHubForTest(db, "mgmt-1")
  	ta, err := hub.TargetFor(ctx, a)
  	if err != nil || ta.Version != v2 || len(ta.Snapshot) == 0 {
  		t.Fatalf("canary target = %d (%d bytes) err %v, want %d", ta.Version, len(ta.Snapshot), err, v2)
  	}
  	tb, err := hub.TargetFor(ctx, b)
  	if err != nil || tb.Version != v1 || tb.Push {
  		t.Fatalf("non-canary target = %d push=%v err %v, want %d and no push", tb.Version, tb.Push, err, v1)
  	}
  	if _, err := db.Exec(ctx, `UPDATE engines SET applied_version = $1 WHERE id = $2`, v2+10, b); err != nil {
  		t.Fatal(err)
  	}
  	if tb, _ = hub.TargetFor(ctx, b); !tb.Ahead || tb.Push {
  		t.Fatalf("engine above its target must be flagged ahead and not pushed: %+v", tb)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/control/ -run TestHubTargetsCanariesOnly -count=1` and expect FAIL with `undefined: control.NewHubForTest`.
- [ ] Change the M1 hub in `mgmt/internal/control/hub.go`:
  - `type PushTarget struct { Version int64; Snapshot []byte; Push, Ahead bool }`; `func (h *Hub) TargetFor(ctx context.Context, engineID uuid.UUID) (PushTarget, error)` loads the engine's view with `fleet.ListEngines(ctx, h.pool, fleet.EngineFilter{EngineID: &engineID})`, sets `Ahead = applied > target`, `Push = target > applied && target > session.lastSent` (lastSent is 0 without a session), and loads `Snapshot` with `fleet.SnapshotFor` only when `Push`. `NewHubForTest(pool *pgxpool.Pool, instance string) *Hub` constructs a hub without a gRPC server.
  - Sessions are keyed by engine id and record `groupID` and `lastSent`.
  - On `Hello`: `fleet.Heartbeat`, then `TargetFor`; when `Push`, send the snapshot and set `lastSent`; when `Ahead`, log `engine ahead of management plane` with both versions once per stream (the spec's "flagged, not silently downgraded").
  - A dedicated LISTEN connection on `nexora_rollout` and `nexora_engine_updated`: for `nexora_rollout` (payload group id) run `TargetFor` for each local session in that group and push when `Push`; for `nexora_engine_updated` (payload engine id) reload the session's group with `SELECT group_id FROM engines WHERE id = $1` and then run `TargetFor`. Every 30 s every local session is re-synced the same way (covers notifications lost while the LISTEN connection reconnected). Remove the M1 `nexora_config` listener.
  - `Applied{version}` -> `fleet.RecordApplied`; `Rejected{version, reason}` -> `fleet.RecordRejected`; `Stats` -> `fleet.RecordStats(ctx, pool, id, instance, stats.AppliedVersion, stats.GetHealth())` (use the applied-version field M1 put in `Stats`, or the session's last applied version when M1 has none); stream end -> `fleet.Disconnect`.
- [ ] Wire `mgmt/internal/config/config.go`: `InstanceID string` from `NEXORA_INSTANCE_ID` (default `os.Hostname()`), `RolloutTick time.Duration` from `NEXORA_ROLLOUT_TICK` (default `1s`, must be between `100ms` and `1m`, otherwise startup fails with `NEXORA_ROLLOUT_TICK must be between 100ms and 1m`). In `mgmt/cmd/nexora-mgmt/main.go` `serve`: start `(&rollout.Controller{Pool: pool, Tick: cfg.RolloutTick, Log: log}).Run(ctx)` in a goroutine; create `fleet.NewMetrics(prometheus.DefaultRegisterer)` and refresh it every 15 s; run `fleet.Prune` every 5 min.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/... -count=1'` and expect every package `ok` (M1–M4 tests that published through `nexora_config` now pass through rollouts; `all_at_once` is the default strategy).
- [ ] Commit: `git add mgmt/internal/rollout mgmt/internal/fleet mgmt/internal/control mgmt/internal/store/storetest/fleet.go mgmt/internal/config mgmt/cmd/nexora-mgmt && git commit -m "feat(mgmt): rollout controller, targeted pushes, fleet health"`.

## Task 7: Fleet HTTP API and the fleet e2e harness

Files: `mgmt/api/openapi.yaml` (new operations and scoped `group_id`), `mgmt/internal/api/fleet_groups.go` (group and rollout handlers), `mgmt/internal/api/fleet_engines.go` (engine, stats, summary handlers), `mgmt/internal/api/fleet_join_tokens.go` (join token handlers), `mgmt/internal/fleet/groups.go` (group queries and validation), `mgmt/internal/fleet/join_tokens.go` (join token queries), `mgmt/internal/fleet/stats.go` (derived rates), `mgmt/internal/auth/permissions.go` (roles for new operations), `mgmt/internal/auth/permissions_fleet_test.go`, M1–M4 handlers of scoped resources (accept and return `group_id`), `web/src/api/schema.d.ts` (regenerated), `e2e/harness/fleet.go` (fleet harness and typed API client), `e2e/harness/fleet_openapi_test.go` (helper bodies validated against OpenAPI), `e2e/fleet_api_test.go` (`TestFleetAPI`)
Interfaces: operationIds `listEngineGroups`, `createEngineGroup`, `getEngineGroup`, `updateEngineGroup`, `deleteEngineGroup`, `rollbackEngineGroup`, `resumeEngineGroupRollouts`, `listRollouts`, `getRollout`, `getFleetSummary`, `listEngines`, `getEngine`, `updateEngine`, `deleteEngine`, `getEngineStats`, `listJoinTokens`, `createJoinToken`, `revokeJoinToken`; error codes `conflict`, `name_taken`, `group_protected`, `group_not_empty`, `invalid_rollout_params`, `invalid_labels`, `version_not_found`, `not_older`, `not_paused`, `engine_revoked`, `group_not_found`; harness API below.
```go
package harness
const DefaultGroupID = "00000000-0000-0000-0000-000000000001"
type FleetOptions struct { Engines, MgmtInstances int; MgmtEnv, EngineEnv map[string]string; EngineGroup string }
type Fleet struct { DB *Postgres; Mgmt []*Mgmt; Engines []*Engine; API *API; GRPCURLs []string; CADir string }
func StartFleet(t *testing.T, o FleetOptions) *Fleet
func (f *Fleet) AddEngine(t *testing.T, name, groupID string, env map[string]string) *Engine
func (f *Fleet) AddEngineWithToken(t *testing.T, name, token string, env map[string]string) *Engine
func (f *Fleet) Engine(name string) *Engine
func (f *Fleet) DNSAddr(name string) string
func (f *Fleet) MetricsURL(name string) string
func (f *Fleet) StateDir(name string) string
func (f *Fleet) WaitConnected(t *testing.T, n int, timeout time.Duration)
func (f *Fleet) WaitAllApplied(t *testing.T, names []string, version int64, timeout time.Duration)
type API struct { Base, Token string; HC *http.Client }
func NewAPI(base, token string) *API
func (a *API) Do(t *testing.T, method, path string, in, out any) int
func (a *API) Must(t *testing.T, method, path string, in, out any, want int)
func (a *API) CreateGroup(t *testing.T, g GroupSpec) EngineGroup
func (a *API) Group(t *testing.T, id string) EngineGroup
func (a *API) WaitGroupStable(t *testing.T, id string, after int64, timeout time.Duration) int64
func (a *API) Engines(t *testing.T) []EngineInfo
func (a *API) EngineByName(t *testing.T, name string) EngineInfo
func (a *API) PatchEngine(t *testing.T, name string, fields map[string]any) EngineInfo
func (a *API) WaitRollout(t *testing.T, groupID string, minVersion int64, timeout time.Duration, states ...string) Rollout
func (a *API) CreateJoinToken(t *testing.T, s JoinTokenSpec) JoinToken
func (a *API) CreateRewrite(t *testing.T, r Rewrite) string
func (a *API) CreateUpstream(t *testing.T, u Upstream) string
func (a *API) SetUpstreamAddress(t *testing.T, id, address string)
func Exchange(addr, name string, qtype uint16) (*dns.Msg, error)
func ExpectA(t *testing.T, addr, name, ip string)
func ExpectRcode(t *testing.T, addr, name string, rcode int)
func MetricValue(t *testing.T, metricsURL, name string) float64
```

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
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/auth/ -run TestFleetPermissions -count=1` and expect FAIL with `listEngineGroups has no permission entry`.
- [ ] Add the 18 entries of `want` to the `Permissions` map in `mgmt/internal/auth/permissions.go` (entries M1 already has for `listEngines`, `getEngine`, `deleteEngine`, `createJoinToken` are set to the roles above). Run the same command and expect `ok`.
- [ ] Add to `mgmt/api/openapi.yaml` (tag `fleet`; existing M1 operations with the same operationId are replaced by these definitions):
  ```yaml
  paths:
    /engine-groups:
      get:
        operationId: listEngineGroups
        tags: [fleet]
        responses:
          "200": { description: Engine groups, content: { application/json: { schema: { type: array, items: { $ref: "#/components/schemas/EngineGroup" } } } } }
      post:
        operationId: createEngineGroup
        tags: [fleet]
        requestBody: { required: true, content: { application/json: { schema: { $ref: "#/components/schemas/EngineGroupInput" } } } }
        responses:
          "201": { description: Created, content: { application/json: { schema: { $ref: "#/components/schemas/EngineGroup" } } } }
          "400": { $ref: "#/components/responses/Error" }
          "409": { $ref: "#/components/responses/Error" }
    /engine-groups/{groupId}:
      parameters: [{ name: groupId, in: path, required: true, schema: { type: string, format: uuid } }]
      get:
        operationId: getEngineGroup
        tags: [fleet]
        responses:
          "200": { description: Group, content: { application/json: { schema: { $ref: "#/components/schemas/EngineGroup" } } } }
          "404": { $ref: "#/components/responses/Error" }
      put:
        operationId: updateEngineGroup
        tags: [fleet]
        requestBody: { required: true, content: { application/json: { schema: { $ref: "#/components/schemas/EngineGroupUpdate" } } } }
        responses:
          "200": { description: Updated, content: { application/json: { schema: { $ref: "#/components/schemas/EngineGroup" } } } }
          "400": { $ref: "#/components/responses/Error" }
          "404": { $ref: "#/components/responses/Error" }
          "409": { $ref: "#/components/responses/Error" }
      delete:
        operationId: deleteEngineGroup
        tags: [fleet]
        responses:
          "204": { description: Deleted }
          "404": { $ref: "#/components/responses/Error" }
          "409": { $ref: "#/components/responses/Error" }
    /engine-groups/{groupId}/rollback:
      parameters: [{ name: groupId, in: path, required: true, schema: { type: string, format: uuid } }]
      post:
        operationId: rollbackEngineGroup
        tags: [fleet]
        requestBody:
          required: true
          content:
            application/json:
              schema: { type: object, additionalProperties: false, required: [to_version], properties: { to_version: { type: integer, format: int64, minimum: 1 } } }
        responses:
          "202": { description: Rollback rollout created, content: { application/json: { schema: { $ref: "#/components/schemas/Rollout" } } } }
          "400": { $ref: "#/components/responses/Error" }
          "404": { $ref: "#/components/responses/Error" }
    /engine-groups/{groupId}/resume-rollouts:
      parameters: [{ name: groupId, in: path, required: true, schema: { type: string, format: uuid } }]
      post:
        operationId: resumeEngineGroupRollouts
        tags: [fleet]
        responses:
          "202": { description: Fresh change rollout created, content: { application/json: { schema: { $ref: "#/components/schemas/Rollout" } } } }
          "404": { $ref: "#/components/responses/Error" }
          "409": { $ref: "#/components/responses/Error" }
    /rollouts:
      get:
        operationId: listRollouts
        tags: [fleet]
        parameters:
          - { name: group_id, in: query, schema: { type: string, format: uuid } }
          - { name: state, in: query, schema: { $ref: "#/components/schemas/RolloutState" } }
          - { name: limit, in: query, schema: { type: integer, minimum: 1, maximum: 200, default: 50 } }
        responses:
          "200": { description: Rollouts newest first, content: { application/json: { schema: { type: array, items: { $ref: "#/components/schemas/Rollout" } } } } }
    /rollouts/{rolloutId}:
      parameters: [{ name: rolloutId, in: path, required: true, schema: { type: string, format: uuid } }]
      get:
        operationId: getRollout
        tags: [fleet]
        responses:
          "200": { description: Rollout with per-engine progress, content: { application/json: { schema: { $ref: "#/components/schemas/RolloutDetail" } } } }
          "404": { $ref: "#/components/responses/Error" }
    /fleet/summary:
      get:
        operationId: getFleetSummary
        tags: [fleet]
        responses:
          "200": { description: Fleet totals, content: { application/json: { schema: { $ref: "#/components/schemas/FleetSummary" } } } }
    /engines:
      get:
        operationId: listEngines
        tags: [fleet]
        parameters:
          - { name: group_id, in: query, schema: { type: string, format: uuid } }
          - { name: state, in: query, schema: { $ref: "#/components/schemas/ConnectionState" } }
          - { name: drift, in: query, schema: { $ref: "#/components/schemas/Drift" } }
        responses:
          "200": { description: Engines, content: { application/json: { schema: { type: array, items: { $ref: "#/components/schemas/Engine" } } } } }
    /engines/{engineId}:
      parameters: [{ name: engineId, in: path, required: true, schema: { type: string, format: uuid } }]
      get:
        operationId: getEngine
        tags: [fleet]
        responses:
          "200": { description: Engine, content: { application/json: { schema: { $ref: "#/components/schemas/EngineDetail" } } } }
          "404": { $ref: "#/components/responses/Error" }
      patch:
        operationId: updateEngine
        tags: [fleet]
        requestBody: { required: true, content: { application/json: { schema: { $ref: "#/components/schemas/EngineUpdate" } } } }
        responses:
          "200": { description: Updated, content: { application/json: { schema: { $ref: "#/components/schemas/Engine" } } } }
          "400": { $ref: "#/components/responses/Error" }
          "404": { $ref: "#/components/responses/Error" }
          "409": { $ref: "#/components/responses/Error" }
      delete:
        operationId: deleteEngine
        tags: [fleet]
        responses:
          "204": { description: Deleted and its certificates revoked }
          "404": { $ref: "#/components/responses/Error" }
    /engines/{engineId}/stats:
      parameters: [{ name: engineId, in: path, required: true, schema: { type: string, format: uuid } }]
      get:
        operationId: getEngineStats
        tags: [fleet]
        parameters: [{ name: window, in: query, schema: { type: string, enum: [5m, 1h, 24h], default: 1h } }]
        responses:
          "200": { description: Samples, content: { application/json: { schema: { $ref: "#/components/schemas/EngineStats" } } } }
          "404": { $ref: "#/components/responses/Error" }
    /join-tokens:
      get:
        operationId: listJoinTokens
        tags: [fleet]
        responses:
          "200": { description: Join tokens (secrets never returned), content: { application/json: { schema: { type: array, items: { $ref: "#/components/schemas/JoinToken" } } } } }
      post:
        operationId: createJoinToken
        tags: [fleet]
        requestBody: { required: true, content: { application/json: { schema: { $ref: "#/components/schemas/JoinTokenInput" } } } }
        responses:
          "201": { description: Created; token shown once, content: { application/json: { schema: { $ref: "#/components/schemas/JoinTokenCreated" } } } }
          "400": { $ref: "#/components/responses/Error" }
    /join-tokens/{tokenId}:
      parameters: [{ name: tokenId, in: path, required: true, schema: { type: string, format: uuid } }]
      delete:
        operationId: revokeJoinToken
        tags: [fleet]
        responses:
          "204": { description: Revoked }
          "404": { $ref: "#/components/responses/Error" }
  components:
    schemas:
      ScopeGroupId:
        type: [string, "null"]
        format: uuid
        description: Engine group this resource applies to; null applies it to every group.
      RolloutState: { type: string, enum: [pending, canary, verifying, rolling, completed, halted, rolled_back, superseded] }
      ConnectionState: { type: string, enum: [connected, disconnected, never_connected, revoked] }
      Drift: { type: string, enum: [in_sync, behind, ahead, rejected, unknown] }
      EngineGroupInput:
        type: object
        additionalProperties: false
        required: [name]
        properties:
          name: { type: string, pattern: "^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$" }
          description: { type: string, maxLength: 1024 }
          upstream_mode: { type: string, enum: [inherit, override], default: inherit }
          rollout_strategy: { type: string, enum: [all_at_once, canary], default: all_at_once }
          canary_count: { type: integer, minimum: 0, default: 0 }
          canary_percent: { type: integer, minimum: 0, maximum: 100, default: 0 }
          ack_timeout_seconds: { type: integer, minimum: 5, maximum: 3600, default: 60 }
          health_window_seconds: { type: integer, minimum: 20, maximum: 3600, default: 30 }
          max_servfail_ratio: { type: number, minimum: 0, maximum: 1, default: 0.05 }
          min_health_queries: { type: integer, minimum: 0, default: 100 }
      EngineGroupUpdate:
        allOf:
          - $ref: "#/components/schemas/EngineGroupInput"
          - type: object
            required: [revision]
            properties: { revision: { type: integer, format: int64 } }
      EngineGroup:
        allOf:
          - $ref: "#/components/schemas/EngineGroupInput"
          - type: object
            required: [id, rollouts_paused, stable_version, revision, engine_count, active_rollout, created_at, updated_at]
            properties:
              id: { type: string, format: uuid }
              rollouts_paused: { type: boolean }
              stable_version: { type: [integer, "null"], format: int64 }
              revision: { type: integer, format: int64 }
              engine_count: { type: integer }
              active_rollout: { oneOf: [{ $ref: "#/components/schemas/Rollout" }, { type: "null" }] }
              created_at: { type: string, format: date-time }
              updated_at: { type: string, format: date-time }
      Rollout:
        type: object
        required: [id, group_id, group_name, version, from_version, kind, strategy, state, canary_engine_ids, phase_started_at, halt_reason, created_by, created_at, finished_at, progress]
        properties:
          id: { type: string, format: uuid }
          group_id: { type: string, format: uuid }
          group_name: { type: string }
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
            properties: { total: { type: integer }, applied: { type: integer }, rejected: { type: integer } }
      RolloutDetail:
        allOf:
          - $ref: "#/components/schemas/Rollout"
          - type: object
            required: [engines]
            properties:
              engines:
                type: array
                items:
                  type: object
                  required: [engine_id, name, canary, connection_state, applied_version, status, rejected_reason]
                  properties:
                    engine_id: { type: string, format: uuid }
                    name: { type: string }
                    canary: { type: boolean }
                    connection_state: { $ref: "#/components/schemas/ConnectionState" }
                    applied_version: { type: integer, format: int64 }
                    status: { type: string, enum: [waiting, applied, rejected, disconnected] }
                    rejected_reason: { type: string }
      EngineStatsLatest:
        type: object
        required: [sampled_at, qps, cache_hit_ratio, servfail_ratio, latency_p50_us, latency_p99_us]
        properties:
          sampled_at: { type: string, format: date-time }
          qps: { type: number }
          cache_hit_ratio: { type: number }
          servfail_ratio: { type: number }
          latency_p50_us: { type: integer }
          latency_p99_us: { type: integer }
      Engine:
        type: object
        required: [id, name, group_id, group_name, labels, revision, connection_state, last_seen_at, connected_instance, applied_version, target_version, rejected_version, rejected_reason, drift, created_at, revoked_at, certificate, stats]
        properties:
          id: { type: string, format: uuid }
          name: { type: string }
          group_id: { type: string, format: uuid }
          group_name: { type: string }
          labels: { type: object, additionalProperties: { type: string, maxLength: 63 }, maxProperties: 32 }
          revision: { type: integer, format: int64 }
          connection_state: { $ref: "#/components/schemas/ConnectionState" }
          last_seen_at: { type: [string, "null"], format: date-time }
          connected_instance: { type: string }
          applied_version: { type: integer, format: int64 }
          target_version: { type: integer, format: int64 }
          rejected_version: { type: [integer, "null"], format: int64 }
          rejected_reason: { type: string }
          drift: { $ref: "#/components/schemas/Drift" }
          created_at: { type: string, format: date-time }
          revoked_at: { type: [string, "null"], format: date-time }
          certificate:
            oneOf:
              - type: "null"
              - type: object
                required: [serial, not_before, not_after]
                properties: { serial: { type: string }, not_before: { type: string, format: date-time }, not_after: { type: string, format: date-time } }
          stats: { oneOf: [{ $ref: "#/components/schemas/EngineStatsLatest" }, { type: "null" }] }
      EngineDetail:
        allOf:
          - $ref: "#/components/schemas/Engine"
          - type: object
            required: [certificates, cert_rotate_requested_at]
            properties:
              cert_rotate_requested_at: { type: [string, "null"], format: date-time }
              certificates:
                type: array
                items:
                  type: object
                  required: [serial, not_before, not_after, issued_at, revoked_at, revoke_reason]
                  properties:
                    serial: { type: string }
                    not_before: { type: string, format: date-time }
                    not_after: { type: string, format: date-time }
                    issued_at: { type: string, format: date-time }
                    revoked_at: { type: [string, "null"], format: date-time }
                    revoke_reason: { type: [string, "null"], enum: [revoked, superseded, null] }
      EngineUpdate:
        type: object
        additionalProperties: false
        required: [revision]
        properties:
          revision: { type: integer, format: int64 }
          group_id: { type: string, format: uuid }
          labels: { type: object, additionalProperties: { type: string, maxLength: 63 }, maxProperties: 32 }
      EngineStats:
        type: object
        required: [window, samples]
        properties:
          window: { type: string, enum: [5m, 1h, 24h] }
          samples:
            type: array
            items:
              allOf:
                - $ref: "#/components/schemas/EngineStatsLatest"
                - type: object
                  required: [applied_version]
                  properties: { applied_version: { type: integer, format: int64 } }
      FleetSummary:
        type: object
        required: [engines, drift, qps, cache_hit_ratio, servfail_ratio, latency_p99_us_max, halted_rollouts, groups]
        properties:
          engines:
            type: object
            required: [total, connected, disconnected, never_connected, revoked]
            properties: { total: { type: integer }, connected: { type: integer }, disconnected: { type: integer }, never_connected: { type: integer }, revoked: { type: integer } }
          drift:
            type: object
            required: [in_sync, behind, ahead, rejected, unknown]
            properties: { in_sync: { type: integer }, behind: { type: integer }, ahead: { type: integer }, rejected: { type: integer }, unknown: { type: integer } }
          qps: { type: number }
          cache_hit_ratio: { type: number }
          servfail_ratio: { type: number }
          latency_p99_us_max: { type: integer }
          halted_rollouts: { type: integer }
          groups:
            type: array
            items:
              type: object
              required: [group_id, name, engines, connected, stable_version, qps, active_rollout]
              properties:
                group_id: { type: string, format: uuid }
                name: { type: string }
                engines: { type: integer }
                connected: { type: integer }
                stable_version: { type: [integer, "null"], format: int64 }
                qps: { type: number }
                active_rollout: { oneOf: [{ $ref: "#/components/schemas/Rollout" }, { type: "null" }] }
      JoinTokenInput:
        type: object
        additionalProperties: false
        required: [group_id]
        properties:
          group_id: { type: string, format: uuid }
          ttl_seconds: { type: integer, minimum: 60, maximum: 2592000, default: 86400 }
          max_uses: { type: integer, minimum: 1, maximum: 10000, default: 1 }
          labels: { type: object, additionalProperties: { type: string, maxLength: 63 }, maxProperties: 32 }
      JoinToken:
        type: object
        required: [id, group_id, group_name, labels, expires_at, max_uses, uses, revoked_at, created_at, state]
        properties:
          id: { type: string, format: uuid }
          group_id: { type: string, format: uuid }
          group_name: { type: string }
          labels: { type: object, additionalProperties: { type: string } }
          expires_at: { type: string, format: date-time }
          max_uses: { type: integer }
          uses: { type: integer }
          revoked_at: { type: [string, "null"], format: date-time }
          created_at: { type: string, format: date-time }
          state: { type: string, enum: [active, expired, exhausted, revoked] }
      JoinTokenCreated:
        allOf:
          - $ref: "#/components/schemas/JoinToken"
          - type: object
            required: [token]
            properties: { token: { type: string, pattern: "^nxj1\\.[A-Z2-7]+\\.[0-9a-f]{64}$" } }
  ```
  For every request and response schema of the resources in `store.ScopedConfigTables` add property `group_id: { $ref: "#/components/schemas/ScopeGroupId" }` and to their list operations the query parameter `{ name: group_id, in: query, schema: { type: string, format: uuid } }` (filter: rows of that group plus global rows when `include_global=true`, a second boolean query parameter defaulting to `false`).
- [ ] Run `scripts/dev-exec.sh 'make proto && go build ./mgmt/... && pnpm --dir web run gen:api'` and expect exit 0 (oapi-codegen strict server now has unimplemented methods, so `go build` fails with `does not implement` until the handlers below exist; fix that before moving on).
- [ ] Implement the handlers with this exact behaviour; every mutation runs in one transaction and writes `auth.Audit` with the operationId, before and after JSON:
  - `createEngineGroup`: validate (canary strategy needs `canary_count > 0` or `canary_percent > 0`, else 400 `invalid_rollout_params`); insert; `snapshot.Publish(ctx, tx, actor, snapshot.Group(id))`; unique violation on name -> 409 `name_taken`; 201.
  - `updateEngineGroup`: `UPDATE engine_groups SET ..., revision = revision + 1, updated_at = now() WHERE id = $1 AND revision = $2 RETURNING *`; no row and the group exists -> 409 `conflict`; renaming `default` -> 409 `group_protected`; when `upstream_mode` changed, `snapshot.Publish(..., snapshot.Group(id))`.
  - `deleteEngineGroup`: `default` -> 409 `group_protected`; when the group has engines, rows in any scoped table, or active join tokens -> 409 `group_not_empty` with message `group has N engines, M scoped resources and K active join tokens`; otherwise delete.
  - `rollbackEngineGroup`: `to_version` missing from `group_snapshots` for the group -> 404 `version_not_found`; `to_version` not below the group's newest version -> 400 `not_older`; else `snapshot.Republish(ctx, tx, actor, group, to_version, rollout.KindRollback)` and 202 with the rollout.
  - `resumeEngineGroupRollouts`: not paused -> 409 `not_paused`; else `UPDATE engine_groups SET rollouts_paused = false` then `snapshot.Publish(..., snapshot.Group(id))`, 202 with the newest rollout of the group.
  - `listRollouts`/`getRollout`: newest first; `progress.total` counts non-revoked engines of the group (canaries only while state is `canary`/`verifying`), `applied` those with `applied_version >= version`, `rejected` those with `rejected_version = version`; detail `status` per engine is `rejected`, `applied`, `disconnected` or `waiting` in that precedence.
  - `listEngines`/`getEngine`: from `fleet.ListEngines`; `certificate` is the newest non-revoked `engine_certificates` row; `stats` from `fleet.LatestStats` (below); `getEngine` adds all certificates newest first.
  - `updateEngine`: revision check as above (409 `conflict`); revoked engine -> 409 `engine_revoked`; labels checked against the label key pattern of docs/architecture.md (`fleet.LabelKeyPattern`, a `regexp.MustCompile` of that pattern declared in `mgmt/internal/fleet/groups.go` and also applied to join token labels), value length <= 63, at most 32 -> else 400 `invalid_labels`; unknown `group_id` -> 400 `group_not_found`; on group change: update the row, `snapshot.Republish(ctx, tx, actor, newGroup, v, rollout.KindRepublish)` where `v` is the new group's `stable_version`, or its newest `group_snapshots` version when `stable_version` is NULL, then `pg_notify('nexora_engine_updated', engine_id)`.
  - `deleteEngine`: `UPDATE engine_certificates SET revoked_at = now(), revoke_reason = 'revoked' WHERE engine_id = $1 AND revoked_at IS NULL`, `pg_notify('nexora_engine_revoked', id)`, `DELETE FROM engines WHERE id = $1`; 204.
  - `getEngineStats`: samples in the window; for consecutive samples s0, s1: `qps = (s1.queries - s0.queries) / seconds`, `cache_hit_ratio = dHits / (dHits + dMisses)` (0 when no lookups), `servfail_ratio = dServfail / dQueries` (0 when no queries), latency copied from s1; a negative delta (engine restart) uses s1's counters as the delta. `fleet.LatestStats(ctx, q, engineID) (*EngineStatsLatest, error)` returns the same numbers for the newest pair, nil when fewer than two samples exist in the last 60 s.
  - `getFleetSummary`: engine and drift counts from `fleet.ListEngines`; `qps` is the sum and `cache_hit_ratio`/`servfail_ratio` are query-weighted means of `LatestStats` over connected engines; `latency_p99_us_max` the maximum; `halted_rollouts` counts `state = 'halted'`.
  - `createJoinToken`: secret = 32 random bytes base32 (no padding); token `nxj1.<secret>.<CA sha256 hex>`; store the SHA-256 of the secret the way M1 stores it plus `group_id`, `labels`, `expires_at = now() + ttl`, `max_uses`; unknown group -> 400 `group_not_found`; 201 with `token`. `listJoinTokens` never selects the secret hash; `state` = `revoked` / `expired` / `exhausted` / `active` in that precedence. `revokeJoinToken` sets `revoked_at`.
  - Scoped resource handlers: accept `group_id` (unknown group -> 400 `group_not_found`), store it, return it, and pass scopes to `snapshot.Publish` as in Task 5. Zones: creating or re-scoping a zone whose name already exists globally, or creating a global zone whose name exists in any group, returns 409 `name_taken`; the same name in two different groups is allowed.
- [ ] Create `e2e/harness/fleet.go`:
  ```go
  package harness

  import (
  	"bytes"
  	"context"
  	"encoding/json"
  	"fmt"
  	"io"
  	"net"
  	"net/http"
  	"path/filepath"
  	"strconv"
  	"strings"
  	"testing"
  	"time"

  	"github.com/miekg/dns"
  )

  const DefaultGroupID = "00000000-0000-0000-0000-000000000001"

  type FleetOptions struct {
  	Engines       int               // engine processes, each with its own state dir and loopback address
  	MgmtInstances int               // default 1
  	MgmtEnv       map[string]string // extra environment for every mgmt instance
  	EngineEnv     map[string]string // extra environment for every engine
  	EngineGroup   string            // join-token group for the initial engines; default DefaultGroupID
  }

  type Fleet struct {
  	DB       *Postgres
  	Mgmt     []*Mgmt
  	Engines  []*Engine
  	API      *API
  	GRPCURLs []string
  	CADir    string // ca.crt and ca.key shared by every mgmt instance
  	next     int
  	byName   map[string]*fleetEngine
  }

  type fleetEngine struct {
  	proc                        *Engine
  	dnsAddr, metricsURL, stateDir string
  }

  func (f *Fleet) Engine(name string) *Engine     { return f.byName[name].proc }
  func (f *Fleet) DNSAddr(name string) string     { return f.byName[name].dnsAddr }
  func (f *Fleet) MetricsURL(name string) string  { return f.byName[name].metricsURL }
  func (f *Fleet) StateDir(name string) string    { return f.byName[name].stateDir }

  func StartFleet(t *testing.T, o FleetOptions) *Fleet {
  	t.Helper()
  	if o.MgmtInstances == 0 {
  		o.MgmtInstances = 1
  	}
  	if o.EngineGroup == "" {
  		o.EngineGroup = DefaultGroupID
  	}
  	f := &Fleet{DB: StartPostgres(t), CADir: t.TempDir(), byName: map[string]*fleetEngine{}}
  	for i := 0; i < o.MgmtInstances; i++ {
  		env := map[string]string{"NEXORA_INSTANCE_ID": fmt.Sprintf("mgmt-%d", i+1)}
  		for k, v := range o.MgmtEnv {
  			env[k] = v
  		}
  		m := StartMgmt(t, MgmtOptions{DatabaseURL: f.DB.URL, CADir: f.CADir, Env: env})
  		f.Mgmt = append(f.Mgmt, m)
  		f.GRPCURLs = append(f.GRPCURLs, "https://"+m.GRPCAddr)
  	}
  	f.API = NewAPI(f.Mgmt[0].HTTPURL, f.Mgmt[0].AdminToken)
  	for i := 0; i < o.Engines; i++ {
  		f.AddEngine(t, fmt.Sprintf("engine-%d", i+1), o.EngineGroup, o.EngineEnv)
  	}
  	return f
  }

  // AddEngine starts one engine on its own loopback address (127.0.1.N) and state dir,
  // the process-harness stand-in for a separate host.
  func (f *Fleet) AddEngine(t *testing.T, name, groupID string, env map[string]string) *Engine {
  	t.Helper()
  	tok := f.API.CreateJoinToken(t, JoinTokenSpec{GroupID: groupID, TTLSeconds: 3600, MaxUses: 1})
  	return f.AddEngineWithToken(t, name, tok.Token, env)
  }

  // AddEngineWithToken starts an engine with a caller-supplied join token and
  // returns as soon as the process runs (enrollment may be refused).
  func (f *Fleet) AddEngineWithToken(t *testing.T, name, token string, env map[string]string) *Engine {
  	t.Helper()
  	f.next++
  	ip := fmt.Sprintf("127.0.1.%d", f.next)
  	fe := &fleetEngine{dnsAddr: freeAddr(t, ip), stateDir: filepath.Join(t.TempDir(), name)}
  	metrics := freeAddr(t, ip)
  	fe.metricsURL = "http://" + metrics + "/metrics"
  	fe.proc = StartEngine(t, EngineOptions{
  		Name: name, StateDir: fe.stateDir, ManagementURLs: f.GRPCURLs,
  		JoinToken: token, ListenUDP: []string{fe.dnsAddr}, ListenTCP: []string{fe.dnsAddr},
  		MetricsListen: metrics, Env: env, SkipReadyWait: true,
  	})
  	f.byName[name] = fe
  	f.Engines = append(f.Engines, fe.proc)
  	return fe.proc
  }

  func freeAddr(t *testing.T, ip string) string {
  	t.Helper()
  	for i := 0; i < 50; i++ {
  		l, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
  		if err != nil {
  			t.Fatalf("listen %s: %v", ip, err)
  		}
  		addr := l.Addr().String()
  		_ = l.Close()
  		if u, err := net.ListenPacket("udp", addr); err == nil {
  			_ = u.Close()
  			return addr
  		}
  	}
  	t.Fatalf("no free tcp+udp port on %s", ip)
  	return ""
  }

  func (f *Fleet) WaitConnected(t *testing.T, n int, timeout time.Duration) {
  	t.Helper()
  	Eventually(t, timeout, func() error {
  		c := 0
  		for _, e := range f.API.Engines(t) {
  			if e.ConnectionState == "connected" {
  				c++
  			}
  		}
  		if c < n {
  			return fmt.Errorf("%d/%d engines connected", c, n)
  		}
  		return nil
  	})
  }

  func (f *Fleet) WaitAllApplied(t *testing.T, names []string, version int64, timeout time.Duration) {
  	t.Helper()
  	Eventually(t, timeout, func() error {
  		for _, name := range names {
  			if e := f.API.EngineByName(t, name); e.AppliedVersion != version {
  				return fmt.Errorf("%s applied %d, want %d", name, e.AppliedVersion, version)
  			}
  		}
  		return nil
  	})
  }

  // Eventually retries fn every 250ms until it returns nil or timeout passes.
  func Eventually(t *testing.T, timeout time.Duration, fn func() error) {
  	t.Helper()
  	deadline := time.Now().Add(timeout)
  	var err error
  	for time.Now().Before(deadline) {
  		if err = fn(); err == nil {
  			return
  		}
  		time.Sleep(250 * time.Millisecond)
  	}
  	t.Fatalf("not reached within %s: %v", timeout, err)
  }

  type API struct {
  	Base, Token string
  	HC          *http.Client
  }

  func NewAPI(base, token string) *API {
  	return &API{Base: strings.TrimSuffix(base, "/"), Token: token, HC: &http.Client{Timeout: 15 * time.Second}}
  }

  // Do sends JSON and decodes a 2xx JSON body into out; it returns the status code.
  func (a *API) Do(t *testing.T, method, path string, in, out any) int {
  	t.Helper()
  	var body io.Reader
  	if in != nil {
  		raw, err := json.Marshal(in)
  		if err != nil {
  			t.Fatal(err)
  		}
  		body = bytes.NewReader(raw)
  	}
  	req, err := http.NewRequestWithContext(context.Background(), method, a.Base+path, body)
  	if err != nil {
  		t.Fatal(err)
  	}
  	req.Header.Set("Authorization", "Bearer "+a.Token)
  	req.Header.Set("Content-Type", "application/json")
  	resp, err := a.HC.Do(req)
  	if err != nil {
  		t.Fatalf("%s %s: %v", method, path, err)
  	}
  	defer resp.Body.Close()
  	raw, _ := io.ReadAll(resp.Body)
  	if out != nil && len(raw) > 0 {
  		if err := json.Unmarshal(raw, out); err != nil {
  			t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
  		}
  	}
  	return resp.StatusCode
  }

  func (a *API) Must(t *testing.T, method, path string, in, out any, want int) {
  	t.Helper()
  	if got := a.Do(t, method, path, in, out); got != want {
  		t.Fatalf("%s %s = %d, want %d", method, path, got, want)
  	}
  }

  type APIError struct {
  	Code    string `json:"code"`
  	Message string `json:"message"`
  }

  type GroupSpec struct {
  	Name, UpstreamMode, RolloutStrategy                                          string
  	CanaryCount, CanaryPercent, AckTimeoutSeconds, HealthWindowSeconds, MinHealthQueries int
  	MaxServfailRatio                                                             float64
  }

  func groupBody(g GroupSpec) map[string]any {
  	b := map[string]any{"name": g.Name}
  	set := func(k string, v any, zero bool) {
  		if !zero {
  			b[k] = v
  		}
  	}
  	set("upstream_mode", g.UpstreamMode, g.UpstreamMode == "")
  	set("rollout_strategy", g.RolloutStrategy, g.RolloutStrategy == "")
  	set("canary_count", g.CanaryCount, g.CanaryCount == 0)
  	set("canary_percent", g.CanaryPercent, g.CanaryPercent == 0)
  	set("ack_timeout_seconds", g.AckTimeoutSeconds, g.AckTimeoutSeconds == 0)
  	set("health_window_seconds", g.HealthWindowSeconds, g.HealthWindowSeconds == 0)
  	set("min_health_queries", g.MinHealthQueries, g.MinHealthQueries == 0)
  	set("max_servfail_ratio", g.MaxServfailRatio, g.MaxServfailRatio == 0)
  	return b
  }

  type EngineGroup struct {
  	ID               string `json:"id"`
  	Name             string `json:"name"`
  	RolloutStrategy  string `json:"rollout_strategy"`
  	UpstreamMode     string `json:"upstream_mode"`
  	RolloutsPaused   bool   `json:"rollouts_paused"`
  	StableVersion    *int64 `json:"stable_version"`
  	Revision         int64  `json:"revision"`
  	EngineCount      int    `json:"engine_count"`
  }

  func (a *API) CreateGroup(t *testing.T, g GroupSpec) EngineGroup {
  	t.Helper()
  	var out EngineGroup
  	a.Must(t, "POST", "/api/v1/engine-groups", groupBody(g), &out, http.StatusCreated)
  	return out
  }

  func (a *API) Group(t *testing.T, id string) EngineGroup {
  	t.Helper()
  	var out EngineGroup
  	a.Must(t, "GET", "/api/v1/engine-groups/"+id, nil, &out, http.StatusOK)
  	return out
  }

  // WaitGroupStable waits until the group's stable version is above after and returns it.
  func (a *API) WaitGroupStable(t *testing.T, id string, after int64, timeout time.Duration) int64 {
  	t.Helper()
  	var v int64
  	Eventually(t, timeout, func() error {
  		g := a.Group(t, id)
  		if g.StableVersion == nil || *g.StableVersion <= after {
  			return fmt.Errorf("group %s stable %v, waiting for > %d", g.Name, g.StableVersion, after)
  		}
  		v = *g.StableVersion
  		return nil
  	})
  	return v
  }

  type EngineInfo struct {
  	ID              string            `json:"id"`
  	Name            string            `json:"name"`
  	GroupID         string            `json:"group_id"`
  	GroupName       string            `json:"group_name"`
  	Labels          map[string]string `json:"labels"`
  	Revision        int64             `json:"revision"`
  	ConnectionState string            `json:"connection_state"`
  	AppliedVersion  int64             `json:"applied_version"`
  	TargetVersion   int64             `json:"target_version"`
  	RejectedReason  string            `json:"rejected_reason"`
  	Drift           string            `json:"drift"`
  	Certificate     *struct {
  		Serial string `json:"serial"`
  	} `json:"certificate"`
  }

  func (a *API) Engines(t *testing.T) []EngineInfo {
  	t.Helper()
  	var out []EngineInfo
  	a.Must(t, "GET", "/api/v1/engines", nil, &out, http.StatusOK)
  	return out
  }

  func (a *API) EngineByName(t *testing.T, name string) EngineInfo {
  	t.Helper()
  	for _, e := range a.Engines(t) {
  		if e.Name == name {
  			return e
  		}
  	}
  	t.Fatalf("engine %s not listed", name)
  	return EngineInfo{}
  }

  func (a *API) PatchEngine(t *testing.T, name string, fields map[string]any) EngineInfo {
  	t.Helper()
  	e := a.EngineByName(t, name)
  	body := map[string]any{"revision": e.Revision}
  	for k, v := range fields {
  		body[k] = v
  	}
  	var out EngineInfo
  	a.Must(t, "PATCH", "/api/v1/engines/"+e.ID, body, &out, http.StatusOK)
  	return out
  }

  type Rollout struct {
  	ID              string   `json:"id"`
  	GroupID         string   `json:"group_id"`
  	Version         int64    `json:"version"`
  	Kind            string   `json:"kind"`
  	Strategy        string   `json:"strategy"`
  	State           string   `json:"state"`
  	HaltReason      string   `json:"halt_reason"`
  	CanaryEngineIDs []string `json:"canary_engine_ids"`
  }

  // WaitRollout waits for the group's newest rollout with version >= minVersion to reach one of states.
  func (a *API) WaitRollout(t *testing.T, groupID string, minVersion int64, timeout time.Duration, states ...string) Rollout {
  	t.Helper()
  	var got Rollout
  	Eventually(t, timeout, func() error {
  		var rs []Rollout
  		a.Must(t, "GET", "/api/v1/rollouts?group_id="+groupID+"&limit=1", nil, &rs, http.StatusOK)
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

  type JoinTokenSpec struct {
  	GroupID             string
  	TTLSeconds, MaxUses int
  	Labels              map[string]string
  }

  func joinTokenBody(s JoinTokenSpec) map[string]any {
  	b := map[string]any{"group_id": s.GroupID, "ttl_seconds": s.TTLSeconds, "max_uses": s.MaxUses}
  	if len(s.Labels) > 0 {
  		b["labels"] = s.Labels
  	}
  	return b
  }

  type JoinToken struct {
  	ID      string `json:"id"`
  	Token   string `json:"token"`
  	GroupID string `json:"group_id"`
  	State   string `json:"state"`
  	Uses    int    `json:"uses"`
  	MaxUses int    `json:"max_uses"`
  }

  func (a *API) CreateJoinToken(t *testing.T, s JoinTokenSpec) JoinToken {
  	t.Helper()
  	var out JoinToken
  	a.Must(t, "POST", "/api/v1/join-tokens", joinTokenBody(s), &out, http.StatusCreated)
  	return out
  }

  type Rewrite struct {
  	Domain, Type, Value string
  	GroupID             *string
  }

  func rewriteBody(r Rewrite) map[string]any {
  	return map[string]any{"domain": r.Domain, "type": r.Type, "value": r.Value, "group_id": r.GroupID}
  }

  func (a *API) CreateRewrite(t *testing.T, r Rewrite) string {
  	t.Helper()
  	var out struct {
  		ID string `json:"id"`
  	}
  	a.Must(t, "POST", "/api/v1/rewrites", rewriteBody(r), &out, http.StatusCreated)
  	return out.ID
  }

  type Upstream struct {
  	Name, Address, Protocol string
  	GroupID                 *string
  }

  func upstreamBody(u Upstream) map[string]any {
  	return map[string]any{"name": u.Name, "address": u.Address, "protocol": u.Protocol, "group_id": u.GroupID}
  }

  func (a *API) CreateUpstream(t *testing.T, u Upstream) string {
  	t.Helper()
  	var out struct {
  		ID string `json:"id"`
  	}
  	a.Must(t, "POST", "/api/v1/upstreams", upstreamBody(u), &out, http.StatusCreated)
  	return out.ID
  }

  func (a *API) SetUpstreamAddress(t *testing.T, id, address string) {
  	t.Helper()
  	var cur map[string]any
  	a.Must(t, "GET", "/api/v1/upstreams/"+id, nil, &cur, http.StatusOK)
  	cur["address"] = address
  	for _, readOnly := range []string{"id", "created_at", "updated_at", "health"} {
  		delete(cur, readOnly)
  	}
  	a.Must(t, "PUT", "/api/v1/upstreams/"+id, cur, nil, http.StatusOK)
  }

  func Exchange(addr, name string, qtype uint16) (*dns.Msg, error) {
  	m := new(dns.Msg)
  	m.SetQuestion(dns.Fqdn(name), qtype)
  	c := &dns.Client{Timeout: 3 * time.Second}
  	r, _, err := c.Exchange(m, addr)
  	return r, err
  }

  func ExpectA(t *testing.T, addr, name, ip string) {
  	t.Helper()
  	r, err := Exchange(addr, name, dns.TypeA)
  	if err != nil {
  		t.Fatalf("%s A @%s: %v", name, addr, err)
  	}
  	for _, rr := range r.Answer {
  		if a, ok := rr.(*dns.A); ok && a.A.String() == ip {
  			return
  		}
  	}
  	t.Fatalf("%s A @%s = %s %v, want %s", name, addr, dns.RcodeToString[r.Rcode], r.Answer, ip)
  }

  func ExpectRcode(t *testing.T, addr, name string, rcode int) {
  	t.Helper()
  	r, err := Exchange(addr, name, dns.TypeA)
  	if err != nil {
  		t.Fatalf("%s @%s: %v", name, addr, err)
  	}
  	if r.Rcode != rcode {
  		t.Fatalf("%s @%s rcode %s, want %s", name, addr, dns.RcodeToString[r.Rcode], dns.RcodeToString[rcode])
  	}
  }

  // MetricValue returns the first sample of an unlabelled metric from a Prometheus text endpoint.
  func MetricValue(t *testing.T, metricsURL, name string) float64 {
  	t.Helper()
  	resp, err := http.Get(metricsURL)
  	if err != nil {
  		t.Fatalf("scrape %s: %v", metricsURL, err)
  	}
  	defer resp.Body.Close()
  	raw, _ := io.ReadAll(resp.Body)
  	for _, line := range strings.Split(string(raw), "\n") {
  		if f := strings.Fields(line); len(f) == 2 && f[0] == name {
  			v, err := strconv.ParseFloat(f[1], 64)
  			if err == nil {
  				return v
  			}
  		}
  	}
  	t.Fatalf("metric %s not found at %s", name, metricsURL)
  	return 0
  }
  ```
- [ ] In M1's `e2e/harness` engine starter add `SkipReadyWait bool` to `EngineOptions` when it is missing: with it set, `StartEngine` returns once the process is spawned instead of waiting for the DNS listener (an engine whose enrollment is refused never listens). Run `scripts/dev-exec.sh go vet ./e2e/...` and expect no output.
- [ ] Write the failing helper contract test `e2e/harness/fleet_openapi_test.go`:
  ```go
  package harness

  import (
  	"bytes"
  	"encoding/json"
  	"net/http/httptest"
  	"os"
  	"testing"

  	"github.com/pb33f/libopenapi"
  	validator "github.com/pb33f/libopenapi-validator"
  )

  func TestFleetAPIHelpersMatchOpenAPI(t *testing.T) {
  	spec, err := os.ReadFile("../../mgmt/api/openapi.yaml")
  	if err != nil {
  		t.Fatal(err)
  	}
  	doc, err := libopenapi.NewDocument(spec)
  	if err != nil {
  		t.Fatal(err)
  	}
  	v, errs := validator.NewValidator(doc)
  	if len(errs) > 0 {
  		t.Fatalf("validator: %v", errs)
  	}
  	group := DefaultGroupID
  	cases := []struct {
  		method, path string
  		body         any
  	}{
  		{"POST", "/api/v1/engine-groups", groupBody(GroupSpec{Name: "edge-a", RolloutStrategy: "canary", CanaryCount: 1, HealthWindowSeconds: 20, MaxServfailRatio: 0.05})},
  		{"POST", "/api/v1/join-tokens", joinTokenBody(JoinTokenSpec{GroupID: group, TTLSeconds: 600, MaxUses: 2, Labels: map[string]string{"site": "lab"}})},
  		{"POST", "/api/v1/rewrites", rewriteBody(Rewrite{Domain: "a.fleet.test", Type: "A", Value: "192.0.2.1", GroupID: &group})},
  		{"POST", "/api/v1/upstreams", upstreamBody(Upstream{Name: "fixture", Address: "127.0.0.1:5300", Protocol: "udp", GroupID: &group})},
  		{"PATCH", "/api/v1/engines/" + group, map[string]any{"revision": 1, "labels": map[string]string{"nexora.io/canary": "true"}}},
  		{"POST", "/api/v1/engine-groups/" + group + "/rollback", map[string]any{"to_version": 3}},
  	}
  	for _, c := range cases {
  		raw, _ := json.Marshal(c.body)
  		req := httptest.NewRequest(c.method, "http://nexora.test"+c.path, bytes.NewReader(raw))
  		req.Header.Set("Content-Type", "application/json")
  		if ok, verrs := v.ValidateHttpRequest(req); !ok {
  			for _, e := range verrs {
  				t.Errorf("%s %s: %s", c.method, c.path, e.Message)
  			}
  		}
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./e2e/harness/ -run TestFleetAPIHelpersMatchOpenAPI -count=1` and expect `ok` when the OpenAPI edits above are in place (run it once with the `/join-tokens` path temporarily renamed in the YAML and expect FAIL with `POST /api/v1/join-tokens`, then restore).
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
  	f := harness.StartFleet(t, harness.FleetOptions{})
  	api := f.API

  	g := api.CreateGroup(t, harness.GroupSpec{Name: "edge-a", RolloutStrategy: "canary", CanaryPercent: 34,
  		AckTimeoutSeconds: 30, HealthWindowSeconds: 20, MaxServfailRatio: 0.05, MinHealthQueries: 10})
  	if g.Revision != 1 || g.RolloutsPaused || g.RolloutStrategy != "canary" {
  		t.Fatalf("created group %+v", g)
  	}
  	stable := api.WaitGroupStable(t, g.ID, 0, 15*time.Second)

  	var groups []harness.EngineGroup
  	api.Must(t, "GET", "/api/v1/engine-groups", nil, &groups, http.StatusOK)
  	if len(groups) != 2 {
  		t.Fatalf("groups = %+v, want default and edge-a", groups)
  	}

  	var e harness.APIError
  	if code := api.Do(t, "POST", "/api/v1/engine-groups", map[string]any{"name": "edge-a"}, &e); code != http.StatusConflict || e.Code != "name_taken" {
  		t.Fatalf("duplicate name: %d %+v", code, e)
  	}
  	if code := api.Do(t, "POST", "/api/v1/engine-groups", map[string]any{"name": "bad", "rollout_strategy": "canary"}, &e); code != http.StatusBadRequest {
  		t.Fatalf("canary without size: %d %+v", code, e)
  	}

  	upd := map[string]any{"name": "edge-a", "revision": g.Revision, "description": "first"}
  	api.Must(t, "PUT", "/api/v1/engine-groups/"+g.ID, upd, nil, http.StatusOK)
  	if code := api.Do(t, "PUT", "/api/v1/engine-groups/"+g.ID, upd, &e); code != http.StatusConflict || e.Code != "conflict" {
  		t.Fatalf("stale revision: %d %+v", code, e)
  	}
  	if code := api.Do(t, "DELETE", "/api/v1/engine-groups/"+harness.DefaultGroupID, nil, &e); code != http.StatusConflict || e.Code != "group_protected" {
  		t.Fatalf("delete default: %d %+v", code, e)
  	}

  	gid := g.ID
  	api.CreateRewrite(t, harness.Rewrite{Domain: "scoped.fleet.test", Type: "A", Value: "192.0.2.7", GroupID: &gid})
  	if code := api.Do(t, "DELETE", "/api/v1/engine-groups/"+g.ID, nil, &e); code != http.StatusConflict || e.Code != "group_not_empty" {
  		t.Fatalf("delete non-empty group: %d %+v", code, e)
  	}
  	after := api.WaitGroupStable(t, g.ID, stable, 15*time.Second)

  	tok := api.CreateJoinToken(t, harness.JoinTokenSpec{GroupID: g.ID, TTLSeconds: 600, MaxUses: 2, Labels: map[string]string{"site": "lab"}})
  	if !regexp.MustCompile(`^nxj1\.[A-Z2-7]+\.[0-9a-f]{64}$`).MatchString(tok.Token) || tok.State != "active" {
  		t.Fatalf("join token %+v", tok)
  	}
  	var listed []map[string]any
  	api.Must(t, "GET", "/api/v1/join-tokens", nil, &listed, http.StatusOK)
  	for _, row := range listed {
  		if _, has := row["token"]; has {
  			t.Fatal("listJoinTokens must never return the token")
  		}
  	}
  	api.Must(t, "DELETE", "/api/v1/join-tokens/"+tok.ID, nil, nil, http.StatusNoContent)
  	api.Must(t, "GET", "/api/v1/join-tokens", nil, &listed, http.StatusOK)
  	if len(listed) != 1 || listed[0]["state"] != "revoked" {
  		t.Fatalf("revoked token listing %+v", listed)
  	}

  	var rb harness.Rollout
  	if code := api.Do(t, "POST", "/api/v1/engine-groups/"+g.ID+"/rollback", map[string]any{"to_version": after + 1000}, &e); code != http.StatusNotFound || e.Code != "version_not_found" {
  		t.Fatalf("rollback to unknown version: %d %+v", code, e)
  	}
  	api.Must(t, "POST", "/api/v1/engine-groups/"+g.ID+"/rollback", map[string]any{"to_version": stable}, &rb, http.StatusAccepted)
  	if rb.Kind != "rollback" || rb.Version <= after {
  		t.Fatalf("rollback rollout %+v", rb)
  	}
  	api.WaitRollout(t, g.ID, rb.Version, 15*time.Second, "completed")
  	if !api.Group(t, g.ID).RolloutsPaused {
  		t.Fatal("rollback must pause change rollouts")
  	}
  	var resumed harness.Rollout
  	api.Must(t, "POST", "/api/v1/engine-groups/"+g.ID+"/resume-rollouts", nil, &resumed, http.StatusAccepted)
  	if resumed.Kind != "change" || resumed.Version <= rb.Version {
  		t.Fatalf("resume rollout %+v", resumed)
  	}
  	if code := api.Do(t, "POST", "/api/v1/engine-groups/"+g.ID+"/resume-rollouts", nil, &e); code != http.StatusConflict || e.Code != "not_paused" {
  		t.Fatalf("resume twice: %d %+v", code, e)
  	}

  	var sum struct {
  		Engines struct{ Total int } `json:"engines"`
  		Groups  []struct {
  			Name string `json:"name"`
  		} `json:"groups"`
  	}
  	api.Must(t, "GET", "/api/v1/fleet/summary", nil, &sum, http.StatusOK)
  	if sum.Engines.Total != 0 || len(sum.Groups) != 2 {
  		t.Fatalf("summary %+v", sum)
  	}
  	if code := api.Do(t, "PATCH", "/api/v1/engines/"+harness.DefaultGroupID, map[string]any{"revision": 1}, &e); code != http.StatusNotFound {
  		t.Fatalf("patch unknown engine: %d %+v", code, e)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e/ -run TestFleetAPI -count=1 -v'` before the handlers exist and expect FAIL with `POST /api/v1/engine-groups = 404, want 201` (or 501 from the strict server stub); after the handlers above, run it again and expect `--- PASS: TestFleetAPI`.
- [ ] Run `scripts/dev-exec.sh 'go test ./mgmt/... ./e2e/harness/ -count=1 && go test ./e2e/ -run "TestAuthRBACAuditOIDC|TestMgmtStatelessHA" -count=1'` and expect `ok` for every package and test.
- [ ] Run `scripts/dev-exec.sh 'go test ./e2e/ -run TestGUICoverage -count=1'` and expect FAIL whose uncovered-operation list contains only operationIds introduced in this task (Task 11 adds the covering Playwright tests); any other operation in the list is a regression to fix now.
- [ ] Commit: `git add mgmt/api/openapi.yaml mgmt/internal/api mgmt/internal/fleet mgmt/internal/auth web/src/api/schema.d.ts e2e/harness/fleet.go e2e/harness/fleet_openapi_test.go e2e/fleet_api_test.go && git commit -m "feat(api): engine groups, rollouts, fleet engines and join tokens"`.

## Task 8: Engine lifecycle in the management plane

Files: `mgmt/internal/pki/engine_cert.go` (issuance with TTL and CSR checks), `mgmt/internal/pki/engine_cert_test.go`, `mgmt/internal/fleet/join_tokens.go` (consumption rules), `mgmt/internal/fleet/join_tokens_test.go`, `mgmt/internal/control/revocation.go` (CRL-equivalent checks, revoke, rotate), `mgmt/internal/control/revocation_test.go`, `mgmt/internal/control/enroll.go` (M1 Enroll: token rules, group, labels, recorded certificate), `mgmt/internal/control/hub.go` (certificate messages on the stream), `mgmt/internal/control/server.go` (interceptors on Connect, GetBlob, LogsService), `mgmt/api/openapi.yaml` + `mgmt/internal/api/fleet_engines.go` (revoke, rotate), `mgmt/internal/auth/permissions.go` + `permissions_fleet_test.go`, `mgmt/internal/config/config.go` (`NEXORA_ENGINE_CERT_TTL`), `mgmt/cmd/nexora-mgmt/cli_fleet.go` (group, join-token, api-token, ca init --if-missing), `e2e/cli_fleet_test.go` (`TestMgmtCLIFleet`)
Interfaces:
```go
package pki
var ErrCSRSubject = errors.New("pki: CSR common name does not match engine id")
var ErrCertTTL = errors.New("pki: engine certificate TTL must be at least 30s")
func (ca *CA) IssueEngineCert(csrDER []byte, engineID uuid.UUID, ttl time.Duration, now time.Time) (certDER []byte, serialHex string, err error)

package fleet
var ErrJoinTokenUnknown, ErrJoinTokenExpired, ErrJoinTokenExhausted, ErrJoinTokenRevoked error // messages "join token unknown|expired|exhausted|revoked"
type JoinTokenGrant struct { ID, GroupID uuid.UUID; Labels map[string]string }
func CreateJoinToken(ctx context.Context, q Querier, caSHA256Hex string, groupID uuid.UUID, ttl time.Duration, maxUses int, labels map[string]string) (token string, id uuid.UUID, err error)
func ConsumeJoinToken(ctx context.Context, tx pgx.Tx, token string) (JoinTokenGrant, error)

package control
type RevocationChecker struct { /* pool, cache ttl, cache */ }
func NewRevocationChecker(pool *pgxpool.Pool, cacheTTL time.Duration) *RevocationChecker
func (rc *RevocationChecker) CheckConnect(ctx context.Context, cert *x509.Certificate) error // status PermissionDenied "certificate revoked" / Unauthenticated "unknown engine"
func (rc *RevocationChecker) Check(ctx context.Context, cert *x509.Certificate) error        // cached variant for GetBlob / LogsService
func (rc *RevocationChecker) Invalidate(engineID uuid.UUID)
func RecordCertificate(ctx context.Context, q fleet.Querier, engineID uuid.UUID, cert *x509.Certificate) error
func RevokeEngine(ctx context.Context, tx pgx.Tx, engineID uuid.UUID) error
func RequestRotation(ctx context.Context, tx pgx.Tx, engineID uuid.UUID) error
```
operationIds `revokeEngine` (admin), `rotateEngineCertificate` (admin).

- [ ] Record the M1 spellings this task touches: run `scripts/dev-exec.sh 'grep -n "func InitCA\|func LoadCA\|func (ca \*CA)" mgmt/internal/pki/*.go; grep -A12 "CREATE TABLE join_tokens" mgmt/migrations/*.sql; grep -rn "AdminUsername" e2e/harness | head -3'` and expect `InitCA(outDir string) error`, `LoadCA(certFile, keyFile string) (*CA, error)`, a `join_tokens` column holding the SHA-256 of the secret (this task writes it as `secret_sha256 bytea`), and a harness constant `AdminUsername`. Use the spellings found wherever this task writes those names.
- [ ] Write the failing test `mgmt/internal/pki/engine_cert_test.go`:
  ```go
  package pki_test

  import (
  	"crypto/ecdsa"
  	"crypto/elliptic"
  	"crypto/rand"
  	"crypto/x509"
  	"crypto/x509/pkix"
  	"errors"
  	"path/filepath"
  	"testing"
  	"time"

  	"github.com/google/uuid"

  	"github.com/piwi3910/nexora/mgmt/internal/pki"
  )

  func csrFor(t *testing.T, cn string) []byte {
  	t.Helper()
  	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
  	if err != nil {
  		t.Fatal(err)
  	}
  	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
  	if err != nil {
  		t.Fatal(err)
  	}
  	return der
  }

  func TestIssueEngineCert(t *testing.T) {
  	dir := t.TempDir()
  	if err := pki.InitCA(dir); err != nil {
  		t.Fatal(err)
  	}
  	ca, err := pki.LoadCA(filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key"))
  	if err != nil {
  		t.Fatal(err)
  	}
  	id := uuid.New()
  	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

  	der, serial, err := ca.IssueEngineCert(csrFor(t, id.String()), id, 45*time.Second, now)
  	if err != nil {
  		t.Fatalf("issue: %v", err)
  	}
  	cert, err := x509.ParseCertificate(der)
  	if err != nil {
  		t.Fatal(err)
  	}
  	if cert.Subject.CommonName != id.String() || serial != cert.SerialNumber.Text(16) {
  		t.Fatalf("cn %q serial %q/%q", cert.Subject.CommonName, serial, cert.SerialNumber.Text(16))
  	}
  	if !cert.NotBefore.Equal(now.Add(-5*time.Second)) || !cert.NotAfter.Equal(now.Add(45*time.Second)) {
  		t.Fatalf("validity %s .. %s", cert.NotBefore, cert.NotAfter)
  	}
  	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
  		t.Fatalf("ext key usage %v, want client auth only", cert.ExtKeyUsage)
  	}
  	_, serial2, _ := ca.IssueEngineCert(csrFor(t, id.String()), id, time.Hour, now)
  	if serial2 == serial {
  		t.Fatal("serials must be unique")
  	}

  	if _, _, err := ca.IssueEngineCert(csrFor(t, uuid.NewString()), id, time.Hour, now); !errors.Is(err, pki.ErrCSRSubject) {
  		t.Fatalf("foreign CN: err = %v", err)
  	}
  	bad := csrFor(t, id.String())
  	bad[len(bad)-3] ^= 0xff
  	if _, _, err := ca.IssueEngineCert(bad, id, time.Hour, now); err == nil {
  		t.Fatal("tampered CSR accepted")
  	}
  	if _, _, err := ca.IssueEngineCert(csrFor(t, id.String()), id, 29*time.Second, now); !errors.Is(err, pki.ErrCertTTL) {
  		t.Fatalf("short TTL: err = %v", err)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/pki/ -run TestIssueEngineCert -count=1` and expect FAIL with `ca.IssueEngineCert undefined`.
- [ ] Implement `IssueEngineCert` in `mgmt/internal/pki/engine_cert.go`: parse the CSR (`x509.ParseCertificateRequest`), `csr.CheckSignature()`, require `csr.PublicKey` to be ECDSA P-256 (else `pki: engine key must be ECDSA P-256`), require CN == `engineID.String()` (`ErrCSRSubject`), `ttl < 30*time.Second` -> `ErrCertTTL`; serial = 128 random bits (`rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))`, retried when zero); template `NotBefore: now.Add(-5 * time.Second)`, `NotAfter: now.Add(ttl)`, `KeyUsage: x509.KeyUsageDigitalSignature`, `ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}`, `Subject.CommonName: engineID.String()`; sign with the CA key. Make M1's Enroll issuance call this function. Run the test and expect `ok`.
- [ ] Write the failing test `mgmt/internal/fleet/join_tokens_test.go`:
  ```go
  package fleet_test

  import (
  	"context"
  	"errors"
  	"strings"
  	"testing"
  	"time"

  	"github.com/piwi3910/nexora/mgmt/internal/fleet"
  	"github.com/piwi3910/nexora/mgmt/internal/store"
  	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
  )

  func TestConsumeJoinToken(t *testing.T) {
  	ctx := context.Background()
  	db := storetest.NewDB(t)
  	ca := strings.Repeat("ab", 32)
  	consume := func(tok string) (fleet.JoinTokenGrant, error) {
  		tx, err := db.Begin(ctx)
  		if err != nil {
  			t.Fatal(err)
  		}
  		defer tx.Rollback(ctx)
  		g, err := fleet.ConsumeJoinToken(ctx, tx, tok)
  		if err == nil {
  			_ = tx.Commit(ctx)
  		}
  		return g, err
  	}

  	tok, id, err := fleet.CreateJoinToken(ctx, db, ca, store.DefaultGroupID, time.Hour, 2, map[string]string{"site": "lab"})
  	if err != nil {
  		t.Fatal(err)
  	}
  	if !strings.HasPrefix(tok, "nxj1.") || !strings.HasSuffix(tok, "."+ca) {
  		t.Fatalf("token format %q", tok)
  	}
  	for i := 0; i < 2; i++ {
  		g, err := consume(tok)
  		if err != nil || g.ID != id || g.GroupID != store.DefaultGroupID || g.Labels["site"] != "lab" {
  			t.Fatalf("use %d: %+v %v", i+1, g, err)
  		}
  	}
  	if _, err := consume(tok); !errors.Is(err, fleet.ErrJoinTokenExhausted) {
  		t.Fatalf("third use: %v", err)
  	}

  	exp, expID, _ := fleet.CreateJoinToken(ctx, db, ca, store.DefaultGroupID, time.Hour, 5, nil)
  	if _, err := db.Exec(ctx, `UPDATE join_tokens SET expires_at = now() - interval '1 second' WHERE id = $1`, expID); err != nil {
  		t.Fatal(err)
  	}
  	if _, err := consume(exp); !errors.Is(err, fleet.ErrJoinTokenExpired) || err.Error() != "join token expired" {
  		t.Fatalf("expired: %v", err)
  	}
  	rev, revID, _ := fleet.CreateJoinToken(ctx, db, ca, store.DefaultGroupID, time.Hour, 5, nil)
  	_, _ = db.Exec(ctx, `UPDATE join_tokens SET revoked_at = now() WHERE id = $1`, revID)
  	if _, err := consume(rev); !errors.Is(err, fleet.ErrJoinTokenRevoked) {
  		t.Fatalf("revoked: %v", err)
  	}
  	if _, err := consume("nxj1.AAAAAAAA." + ca); !errors.Is(err, fleet.ErrJoinTokenUnknown) {
  		t.Fatalf("unknown: %v", err)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/fleet/ -run TestConsumeJoinToken -count=1` and expect FAIL with `undefined: fleet.ConsumeJoinToken`.
- [ ] Implement `mgmt/internal/fleet/join_tokens.go`. `CreateJoinToken` validates labels, TTL (60 s .. 30 d) and max uses (1 .. 10000), generates the secret (32 random bytes, `base32.StdEncoding.WithPadding(base32.NoPadding)`), stores `sha256(secret)`, returns `nxj1.<secret>.<caSHA256Hex>`. `ConsumeJoinToken` parses the three dot-separated parts (malformed -> `ErrJoinTokenUnknown`) and runs:
  ```sql
  UPDATE join_tokens SET uses = uses + 1
  WHERE secret_sha256 = $1 AND revoked_at IS NULL AND expires_at > now() AND uses < max_uses
  RETURNING id, group_id, labels;
  ```
  When no row returns it classifies with `SELECT revoked_at IS NOT NULL, expires_at <= now(), uses >= max_uses FROM join_tokens WHERE secret_sha256 = $1` in the order revoked, expired, exhausted; no row -> unknown. The API handler `createJoinToken` from Task 7 calls `CreateJoinToken`. Run the test and expect `ok`.
- [ ] Write the failing test `mgmt/internal/control/revocation_test.go`:
  ```go
  package control_test

  import (
  	"context"
  	"crypto/ecdsa"
  	"crypto/elliptic"
  	"crypto/rand"
  	"crypto/x509"
  	"crypto/x509/pkix"
  	"math/big"
  	"testing"
  	"time"

  	"github.com/google/uuid"
  	"google.golang.org/grpc/codes"
  	"google.golang.org/grpc/status"

  	"github.com/piwi3910/nexora/mgmt/internal/control"
  	"github.com/piwi3910/nexora/mgmt/internal/store"
  	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
  )

  func leaf(t *testing.T, id uuid.UUID, serial int64) *x509.Certificate {
  	t.Helper()
  	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
  	tpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: id.String()},
  		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
  	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
  	if err != nil {
  		t.Fatal(err)
  	}
  	c, _ := x509.ParseCertificate(der)
  	return c
  }

  func wantCode(t *testing.T, err error, code codes.Code, msg string) {
  	t.Helper()
  	if s, _ := status.FromError(err); s.Code() != code || s.Message() != msg {
  		t.Fatalf("err = %v, want %s %q", err, code, msg)
  	}
  }

  func TestRevocationChecker(t *testing.T) {
  	ctx := context.Background()
  	db := storetest.NewDB(t)
  	id := storetest.InsertEngine(t, db, "a", store.DefaultGroupID)
  	old, cur := leaf(t, id, 0xa1), leaf(t, id, 0xb2)
  	rc := control.NewRevocationChecker(db, 5*time.Second)

  	if err := rc.CheckConnect(ctx, old); err != nil {
  		t.Fatalf("pre-M5 certificate must be accepted and recorded: %v", err)
  	}
  	var n int
  	_ = db.QueryRow(ctx, `SELECT count(*) FROM engine_certificates WHERE serial = 'a1' AND engine_id = $1`, id).Scan(&n)
  	if n != 1 {
  		t.Fatalf("recorded rows = %d", n)
  	}

  	time.Sleep(10 * time.Millisecond)
  	if err := control.RecordCertificate(ctx, db, id, cur); err != nil {
  		t.Fatal(err)
  	}
  	if err := rc.CheckConnect(ctx, cur); err != nil {
  		t.Fatalf("renewed certificate: %v", err)
  	}
  	wantCode(t, rc.CheckConnect(ctx, old), codes.PermissionDenied, "certificate revoked")
  	var reason string
  	_ = db.QueryRow(ctx, `SELECT revoke_reason FROM engine_certificates WHERE serial = 'a1'`).Scan(&reason)
  	if reason != "superseded" {
  		t.Fatalf("old serial reason %q, want superseded", reason)
  	}

  	if err := rc.Check(ctx, cur); err != nil { // warms the cache
  		t.Fatal(err)
  	}
  	tx, _ := db.Begin(ctx)
  	if err := control.RevokeEngine(ctx, tx, id); err != nil {
  		t.Fatal(err)
  	}
  	_ = tx.Commit(ctx)
  	wantCode(t, rc.CheckConnect(ctx, cur), codes.PermissionDenied, "certificate revoked")
  	rc.Invalidate(id)
  	wantCode(t, rc.Check(ctx, cur), codes.PermissionDenied, "certificate revoked")

  	wantCode(t, rc.CheckConnect(ctx, leaf(t, uuid.New(), 0xc3)), codes.Unauthenticated, "unknown engine")
  	other := storetest.InsertEngine(t, db, "b", store.DefaultGroupID)
  	stolen := leaf(t, other, 0xb2) // serial registered to engine a
  	wantCode(t, rc.CheckConnect(ctx, stolen), codes.PermissionDenied, "certificate revoked")
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./mgmt/internal/control/ -run TestRevocationChecker -count=1` and expect FAIL with `undefined: control.NewRevocationChecker`.
- [ ] Implement `mgmt/internal/control/revocation.go`:
  - Engine id = `uuid.Parse(cert.Subject.CommonName)`; parse failure or no `engines` row -> `status.Error(codes.Unauthenticated, "unknown engine")`.
  - `CheckConnect` runs, in one transaction:
    ```sql
    SELECT e.revoked_at IS NOT NULL FROM engines e WHERE e.id = $1;
    INSERT INTO engine_certificates (serial, engine_id, not_before, not_after)
    VALUES ($2, $1, $3, $4) ON CONFLICT (serial) DO NOTHING;
    SELECT engine_id, revoked_at IS NOT NULL, issued_at FROM engine_certificates WHERE serial = $2;
    UPDATE engine_certificates SET revoked_at = now(), revoke_reason = 'superseded'
    WHERE engine_id = $1 AND revoked_at IS NULL AND issued_at < $5;   -- $5 = issued_at of this serial
    ```
    denied (`PermissionDenied`, `certificate revoked`) when the engine is revoked, the serial is revoked, or the serial belongs to another engine; the superseding UPDATE runs only when allowed.
  - `Check` caches `(serial -> allowed, expiry)` for `cacheTTL`, calls `CheckConnect` on a miss, and `Invalidate(engineID)` drops every cached serial of that engine. A LISTEN on `nexora_engine_revoked` and `nexora_engine_rotate` calls `Invalidate`.
  - `RecordCertificate` inserts the row (`issued_at = now()`).
  - `RevokeEngine`: `UPDATE engines SET revoked_at = now(), revision = revision + 1 WHERE id = $1 AND revoked_at IS NULL`; `UPDATE engine_certificates SET revoked_at = now(), revoke_reason = 'revoked' WHERE engine_id = $1 AND revoked_at IS NULL`; `pg_notify('nexora_engine_revoked', $1)`.
  - `RequestRotation`: revoked engine -> `ErrEngineRevoked`; `UPDATE engines SET cert_rotate_requested_at = now() WHERE id = $1`; `pg_notify('nexora_engine_rotate', $1)`.
  Run the test and expect `ok`.
- [ ] Wire the checks and stream messages:
  - `mgmt/internal/control/server.go`: a stream interceptor calls `CheckConnect` for `/nexora.control.v1.EngineControl/Connect` and `Check` for `GetBlob` and the OTLP `LogsService/Export`, using the verified leaf from `peer.FromContext(ctx).AuthInfo.(credentials.TLSInfo).State.VerifiedChains[0][0]`.
  - `mgmt/internal/control/enroll.go`: `fleet.ConsumeJoinToken` in the enroll transaction; its error text becomes `status.Error(codes.Unauthenticated, err.Error())`; the new engine row gets `group_id` and `labels` from the grant; the certificate is issued with `cfg.EngineCertTTL` and `RecordCertificate` is called in the same transaction.
  - `mgmt/internal/control/hub.go`: on `CertificateRequest` from a session: at most one issuance per engine per 10 s (extra requests are ignored with a warning); `ca.IssueEngineCert(csr_der, sessionEngineID, cfg.EngineCertTTL, time.Now())`; `RecordCertificate`; `UPDATE engines SET cert_rotate_requested_at = NULL WHERE id = $1`; reply `CertificateIssued{cert_der, ca_der}`; issuance errors are logged and answered with nothing (the engine retries at its next check). On `Hello`, when `cert_rotate_requested_at IS NOT NULL`, send `RenewCertificate{reason: REASON_ROTATE}`; a LISTEN on `nexora_engine_rotate` sends it to the local session; a LISTEN on `nexora_engine_revoked` ends the local session's stream with `status.Error(codes.PermissionDenied, "certificate revoked")`.
  - `mgmt/internal/config/config.go`: `EngineCertTTL` from `NEXORA_ENGINE_CERT_TTL` (default `2160h`; below `30s` startup fails with `NEXORA_ENGINE_CERT_TTL must be at least 30s`).
- [ ] Add to `mgmt/api/openapi.yaml`:
  ```yaml
  paths:
    /engines/{engineId}/revoke:
      parameters: [{ name: engineId, in: path, required: true, schema: { type: string, format: uuid } }]
      post:
        operationId: revokeEngine
        tags: [fleet]
        responses:
          "200": { description: Engine revoked; its streams are closed, content: { application/json: { schema: { $ref: "#/components/schemas/Engine" } } } }
          "404": { $ref: "#/components/responses/Error" }
          "409": { $ref: "#/components/responses/Error" }
    /engines/{engineId}/rotate-certificate:
      parameters: [{ name: engineId, in: path, required: true, schema: { type: string, format: uuid } }]
      post:
        operationId: rotateEngineCertificate
        tags: [fleet]
        responses:
          "202": { description: Rotation requested, content: { application/json: { schema: { $ref: "#/components/schemas/Engine" } } } }
          "404": { $ref: "#/components/responses/Error" }
          "409": { $ref: "#/components/responses/Error" }
  ```
  Handlers: `revokeEngine` runs `RevokeEngine` + audit (already revoked -> 409 `engine_revoked`); `rotateEngineCertificate` runs `RequestRotation` + audit (revoked -> 409 `engine_revoked`). Add `"revokeEngine": RoleAdmin, "rotateEngineCertificate": RoleAdmin` to both `Permissions` and the `want` map in `permissions_fleet_test.go`. Run `scripts/dev-exec.sh 'make proto && pnpm --dir web run gen:api && go test ./mgmt/... -count=1'` and expect `ok`.
- [ ] Write the failing black-box CLI test `e2e/cli_fleet_test.go`:
  ```go
  package e2e

  import (
  	"bytes"
  	"crypto/sha256"
  	"net/http"
  	"os"
  	"os/exec"
  	"path/filepath"
  	"regexp"
  	"strings"
  	"testing"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  func mgmtCLI(t *testing.T, f *harness.Fleet, args ...string) (string, string, error) {
  	t.Helper()
  	dir := os.Getenv("NEXORA_E2E_BIN_DIR")
  	if dir == "" {
  		dir = "../bin"
  	}
  	cmd := exec.Command(filepath.Join(dir, "nexora-mgmt"), args...)
  	cmd.Env = append(os.Environ(),
  		"NEXORA_DATABASE_URL="+f.DB.URL,
  		"NEXORA_CA_CERT_FILE="+filepath.Join(f.CADir, "ca.crt"),
  		"NEXORA_CA_KEY_FILE="+filepath.Join(f.CADir, "ca.key"))
  	var out, errb bytes.Buffer
  	cmd.Stdout, cmd.Stderr = &out, &errb
  	err := cmd.Run()
  	return strings.TrimSpace(out.String()), errb.String(), err
  }

  func TestMgmtCLIFleet(t *testing.T) {
  	f := harness.StartFleet(t, harness.FleetOptions{})

  	gid, stderr, err := mgmtCLI(t, f, "group", "create", "--name", "edge-cli", "--description", "from cli")
  	if err != nil || !regexp.MustCompile(`^[0-9a-f-]{36}$`).MatchString(gid) {
  		t.Fatalf("group create: %q %q %v", gid, stderr, err)
  	}
  	if g := f.API.Group(t, gid); g.Name != "edge-cli" {
  		t.Fatalf("group via API: %+v", g)
  	}
  	if again, _, err := mgmtCLI(t, f, "group", "create", "--name", "edge-cli", "--if-missing"); err != nil || again != gid {
  		t.Fatalf("group create --if-missing: %q %v, want %s", again, err, gid)
  	}
  	if _, stderr, err := mgmtCLI(t, f, "group", "create", "--name", "edge-cli"); err == nil || !strings.Contains(stderr, `group "edge-cli" already exists`) {
  		t.Fatalf("duplicate group create: %q %v", stderr, err)
  	}

  	tok, stderr, err := mgmtCLI(t, f, "join-token", "create", "--group", "edge-cli", "--ttl", "10m", "--max-uses", "3", "--label", "site=lab")
  	if err != nil || !regexp.MustCompile(`^nxj1\.[A-Z2-7]+\.[0-9a-f]{64}$`).MatchString(tok) {
  		t.Fatalf("join-token create: %q %q %v", tok, stderr, err)
  	}
  	var listed []harness.JoinToken
  	f.API.Must(t, "GET", "/api/v1/join-tokens", nil, &listed, http.StatusOK)
  	if len(listed) != 1 || listed[0].MaxUses != 3 || listed[0].GroupID != gid {
  		t.Fatalf("listed tokens %+v", listed)
  	}
  	if _, stderr, err := mgmtCLI(t, f, "join-token", "create", "--group", "missing"); err == nil || !strings.Contains(stderr, `group "missing" not found`) {
  		t.Fatalf("unknown group: %q %v", stderr, err)
  	}

  	before, _ := os.ReadFile(filepath.Join(f.CADir, "ca.crt"))
  	if _, stderr, err := mgmtCLI(t, f, "ca", "init", "--out", f.CADir, "--if-missing"); err != nil {
  		t.Fatalf("ca init --if-missing on existing CA: %q %v", stderr, err)
  	}
  	after, _ := os.ReadFile(filepath.Join(f.CADir, "ca.crt"))
  	if sha256.Sum256(before) != sha256.Sum256(after) {
  		t.Fatal("ca init --if-missing replaced an existing CA")
  	}
  	if _, stderr, err := mgmtCLI(t, f, "ca", "init", "--out", f.CADir); err == nil || !strings.Contains(stderr, "already exists") {
  		t.Fatalf("ca init over existing CA: %q %v", stderr, err)
  	}

  	api, stderr, err := mgmtCLI(t, f, "api-token", "create", "--user", harness.AdminUsername, "--name", "cli", "--ttl", "1h")
  	if err != nil || !strings.HasPrefix(api, "nxt_") {
  		t.Fatalf("api-token create: %q %q %v", api, stderr, err)
  	}
  	harness.NewAPI(f.Mgmt[0].HTTPURL, api).Must(t, "GET", "/api/v1/fleet/summary", nil, nil, http.StatusOK)
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e/ -run TestMgmtCLIFleet -count=1 -v'` and expect FAIL with `group create:`.
- [ ] Implement `mgmt/cmd/nexora-mgmt/cli_fleet.go` with the M1 command framework: `group create --name <n> [--description <d>] [--if-missing]` (existing name: with `--if-missing` prints the existing id and exits 0, otherwise stderr `group "<n>" already exists`, exit 1; a new group is inserted in a transaction with `snapshot.Publish(..., snapshot.Group(id))` and an audit row with actor `cli`; prints the id); `join-token create --group <name> [--ttl 24h] [--max-uses 1] [--label k=v]...` (reads the CA certificate from `NEXORA_CA_CERT_FILE` for the fingerprint; prints only the token; unknown group -> stderr `group "<name>" not found`, exit 1); `api-token create --user <username> --name <name> [--ttl 24h]` (M1 token creation function; prints only the token; unknown user -> exit 1); `ca init --out <dir> [--if-missing]` (when `ca.crt` exists: exit 0 without writing when `--if-missing`, otherwise stderr `ca.crt already exists in <dir>`, exit 1). Run the test and expect `--- PASS: TestMgmtCLIFleet`.
- [ ] Commit: `git add mgmt/internal/pki mgmt/internal/fleet mgmt/internal/control mgmt/api/openapi.yaml mgmt/internal/api mgmt/internal/auth mgmt/internal/config mgmt/cmd/nexora-mgmt web/src/api/schema.d.ts e2e/cli_fleet_test.go && git commit -m "feat(mgmt): join token rules, certificate renewal, rotation and revocation"`.

## Task 9: Engine side of renewal, rotation, revocation and fleet health

Files: `engine/src/cert_renewal.rs` (renewal timing, CSR, atomic identity swap), `engine/src/lib.rs` (module declaration), `engine/src/control.rs` (stream handling of the new messages, revoked backoff, FleetHealth in Stats), `engine/src/telemetry/metrics.rs` (latency percentile from histogram deltas, SERVFAIL total, `nexora_control_revoked`, `nexora_control_cert_renewals_total`), the bootstrap loader that parses `engine.toml` (`NEXORA_ENGINE_NODE_NAME`; locate it with `grep -rln standalone_snapshot engine/src`)
Interfaces:
```rust
// engine/src/cert_renewal.rs
pub fn renewal_due(not_before: SystemTime, not_after: SystemTime, now: SystemTime) -> bool;
pub fn new_csr(engine_id: &str) -> Result<(Vec<u8> /* csr der */, String /* key pem */), RenewalError>;
pub fn swap_identity(state_dir: &Path, cert_pem: &[u8], key_pem: &[u8]) -> std::io::Result<()>;
pub fn recover_identity(state_dir: &Path) -> std::io::Result<()>;
// engine/src/telemetry/metrics.rs
pub fn percentile_us(bounds_us: &[u64], deltas: &[u64], q: f64) -> u32;
pub struct HealthSampler { /* previous cumulative bucket counts */ }
impl HealthSampler { pub fn new() -> Self; pub fn sample(&mut self, m: &Metrics) -> pb::FleetHealth; }
// engine/src/control.rs
pub fn reconnect_delay(status: Option<&tonic::Status>, attempt: u32, jitter: f64 /* 0.0..1.0 */) -> Duration;
// bootstrap
pub fn apply_env_overrides(b: &mut Bootstrap, get: impl Fn(&str) -> Option<String>) -> Result<(), BootstrapError>;
```

- [ ] Write the failing unit tests at the bottom of a new `engine/src/cert_renewal.rs` (declare `pub mod cert_renewal;` in `engine/src/lib.rs`):
  ```rust
  #[cfg(test)]
  mod tests {
      use super::*;
      use std::fs;
      use std::time::{Duration, UNIX_EPOCH};

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
          let (der, key_pem) = new_csr(id).unwrap();
          let (_, csr) = x509_parser::certification_request::X509CertificationRequest::from_der(&der).unwrap();
          let cn = csr.certification_request_info.subject.iter_common_name().next().unwrap();
          assert_eq!(cn.as_str().unwrap(), id);
          csr.verify_signature().unwrap();
          assert!(key_pem.starts_with("-----BEGIN PRIVATE KEY-----"));
      }

      fn identity(dir: &std::path::Path, cert: &[u8], key: &[u8]) {
          let id = dir.join("identity");
          fs::create_dir_all(&id).unwrap();
          fs::write(id.join("cert.pem"), cert).unwrap();
          fs::write(id.join("key.pem"), key).unwrap();
          fs::write(id.join("ca.pem"), b"ca").unwrap();
      }

      #[test]
      fn swap_identity_replaces_pair_and_keeps_ca() {
          let dir = tempfile::tempdir().unwrap();
          identity(dir.path(), b"old-cert", b"old-key");
          swap_identity(dir.path(), b"new-cert", b"new-key").unwrap();
          let id = dir.path().join("identity");
          assert_eq!(fs::read(id.join("cert.pem")).unwrap(), b"new-cert");
          assert_eq!(fs::read(id.join("key.pem")).unwrap(), b"new-key");
          assert_eq!(fs::read(id.join("ca.pem")).unwrap(), b"ca");
          assert!(!dir.path().join("identity.new").exists());
          assert!(!dir.path().join("identity.old").exists());
          #[cfg(unix)]
          {
              use std::os::unix::fs::PermissionsExt;
              assert_eq!(fs::metadata(id.join("key.pem")).unwrap().permissions().mode() & 0o777, 0o600);
          }
      }

      #[test]
      fn recover_identity_after_crash_between_renames() {
          let dir = tempfile::tempdir().unwrap();
          identity(dir.path(), b"old-cert", b"old-key");
          // crash after `identity` -> `identity.old`, before `identity.new` -> `identity`
          fs::rename(dir.path().join("identity"), dir.path().join("identity.old")).unwrap();
          let new = dir.path().join("identity.new");
          fs::create_dir_all(&new).unwrap();
          fs::write(new.join("cert.pem"), b"new-cert").unwrap();
          fs::write(new.join("key.pem"), b"new-key").unwrap();
          fs::write(new.join("ca.pem"), b"ca").unwrap();
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
- [ ] Run `scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml cert_renewal` and expect FAIL with `cannot find function `renewal_due` in this scope`.
- [ ] Implement `engine/src/cert_renewal.rs` above the tests (add `x509-parser` and `tempfile` (dev) to `engine/Cargo.toml` if M1 lacks them; `rcgen` generates the key and CSR):
  ```rust
  //! Engine certificate renewal: timing, CSR generation and the atomic swap of
  //! `state_dir/identity`. Runs on the control runtime only.
  use std::fs::{self, File, OpenOptions};
  use std::io::{self, Write};
  use std::path::Path;
  use std::time::SystemTime;

  #[derive(Debug, thiserror::Error)]
  pub enum RenewalError {
      #[error("generate key or csr: {0}")]
      Rcgen(#[from] rcgen::Error),
  }

  /// True from 2/3 of the certificate lifetime onwards (and for inverted validity).
  pub fn renewal_due(not_before: SystemTime, not_after: SystemTime, now: SystemTime) -> bool {
      let Ok(lifetime) = not_after.duration_since(not_before) else { return true };
      now >= not_before + lifetime * 2 / 3
  }

  pub fn new_csr(engine_id: &str) -> Result<(Vec<u8>, String), RenewalError> {
      let key = rcgen::KeyPair::generate_for(&rcgen::PKCS_ECDSA_P256_SHA256)?;
      let mut params = rcgen::CertificateParams::default();
      params.distinguished_name = rcgen::DistinguishedName::new();
      params.distinguished_name.push(rcgen::DnType::CommonName, engine_id);
      let csr = params.serialize_request(&key)?;
      Ok((csr.der().to_vec(), key.serialize_pem()))
  }

  fn write_synced(path: &Path, bytes: &[u8], mode: u32) -> io::Result<()> {
      let mut opts = OpenOptions::new();
      opts.write(true).create_new(true);
      #[cfg(unix)]
      {
          use std::os::unix::fs::OpenOptionsExt;
          opts.mode(mode);
      }
      let mut f = opts.open(path)?;
      f.write_all(bytes)?;
      f.sync_all()
  }

  fn sync_dir(path: &Path) -> io::Result<()> {
      File::open(path)?.sync_all()
  }

  /// Stage the new pair next to the other identity files in `identity.new`,
  /// then rename `identity` -> `identity.old`, `identity.new` -> `identity`.
  pub fn swap_identity(state_dir: &Path, cert_pem: &[u8], key_pem: &[u8]) -> io::Result<()> {
      let cur = state_dir.join("identity");
      let new = state_dir.join("identity.new");
      let old = state_dir.join("identity.old");
      if new.exists() {
          fs::remove_dir_all(&new)?;
      }
      fs::create_dir(&new)?;
      for entry in fs::read_dir(&cur)? {
          let entry = entry?;
          let name = entry.file_name();
          if name != "cert.pem" && name != "key.pem" {
              fs::copy(entry.path(), new.join(&name))?;
          }
      }
      write_synced(&new.join("cert.pem"), cert_pem, 0o644)?;
      write_synced(&new.join("key.pem"), key_pem, 0o600)?;
      sync_dir(&new)?;
      if old.exists() {
          fs::remove_dir_all(&old)?;
      }
      fs::rename(&cur, &old)?;
      fs::rename(&new, &cur)?;
      sync_dir(state_dir)?;
      fs::remove_dir_all(&old)
  }

  /// Finish or discard an interrupted swap. Call before loading the identity.
  pub fn recover_identity(state_dir: &Path) -> io::Result<()> {
      let cur = state_dir.join("identity");
      let new = state_dir.join("identity.new");
      let old = state_dir.join("identity.old");
      if !cur.exists() {
          if new.join("cert.pem").exists() && new.join("key.pem").exists() {
              fs::rename(&new, &cur)?;
          } else if old.exists() {
              fs::rename(&old, &cur)?;
          }
          sync_dir(state_dir)?;
      }
      for leftover in [new, old] {
          if leftover.exists() {
              fs::remove_dir_all(leftover)?;
          }
      }
      Ok(())
  }
  ```
  Run `scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml cert_renewal` and expect `test result: ok. 4 passed`.
- [ ] Write the failing tests in `engine/src/telemetry/metrics.rs` (inside its `#[cfg(test)] mod tests`):
  ```rust
  #[test]
  fn percentile_from_bucket_deltas_reports_bucket_upper_bound() {
      let bounds = [50, 100, 250, 500, 1_000];
      let deltas = [10, 70, 15, 4, 1, 0]; // last entry is the +Inf bucket
      assert_eq!(percentile_us(&bounds, &deltas, 0.50), 100);
      assert_eq!(percentile_us(&bounds, &deltas, 0.99), 500);
      assert_eq!(percentile_us(&bounds, &[0, 0, 0, 0, 0, 0], 0.99), 0);
      assert_eq!(percentile_us(&bounds, &[0, 0, 0, 0, 0, 3], 0.5), 1_000, "+Inf reports the largest finite bound");
  }

  #[test]
  fn health_sampler_uses_interval_latency_and_cumulative_counters() {
      let m = Metrics::new_for_test(1);
      let mut s = HealthSampler::new();
      m.record_query_for_test(0, Rcode::NoError, 80);
      m.record_query_for_test(0, Rcode::ServFail, 900);
      let first = s.sample(&m);
      assert_eq!((first.queries_total, first.servfail_total), (2, 1));
      m.record_query_for_test(0, Rcode::NoError, 40);
      let second = s.sample(&m);
      assert_eq!((second.queries_total, second.servfail_total), (3, 1));
      assert_eq!(second.latency_p99_us, 50, "latency covers only the last interval");
  }
  ```
  `Metrics::new_for_test(workers)` and `record_query_for_test(worker, rcode, duration_us)` are test-only constructors over the existing per-worker counters and histogram; add them under `#[cfg(test)]` if M1 has no equivalents.
- [ ] Run `scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml telemetry::metrics` and expect FAIL with `cannot find function `percentile_us``.
- [ ] Implement in `engine/src/telemetry/metrics.rs`:
  ```rust
  /// Upper bound (µs) of the bucket holding quantile `q` of the interval's observations.
  pub fn percentile_us(bounds_us: &[u64], deltas: &[u64], q: f64) -> u32 {
      let total: u64 = deltas.iter().sum();
      if total == 0 {
          return 0;
      }
      let rank = ((total as f64) * q).ceil().max(1.0) as u64;
      let mut seen = 0;
      for (i, count) in deltas.iter().enumerate() {
          seen += count;
          if seen >= rank {
              let bound = bounds_us.get(i).or(bounds_us.last()).copied().unwrap_or(0);
              return bound.min(u32::MAX as u64) as u32;
          }
      }
      bounds_us.last().copied().unwrap_or(0).min(u32::MAX as u64) as u32
  }

  pub struct HealthSampler {
      prev_buckets: Vec<u64>,
  }

  impl HealthSampler {
      pub fn new() -> Self {
          Self { prev_buckets: Vec::new() }
      }

      /// Reads the summed per-worker atomics; never touches worker threads.
      pub fn sample(&mut self, m: &Metrics) -> pb::FleetHealth {
          let buckets = m.duration_bucket_counts(); // non-cumulative, one per bound plus +Inf
          let deltas: Vec<u64> = buckets
              .iter()
              .enumerate()
              .map(|(i, c)| c.saturating_sub(self.prev_buckets.get(i).copied().unwrap_or(0)))
              .collect();
          self.prev_buckets = buckets;
          let bounds = m.duration_bounds_us();
          pb::FleetHealth {
              queries_total: m.queries_total(),
              servfail_total: m.queries_with_rcode(Rcode::ServFail),
              cache_hits_total: m.cache_hits_total(),
              cache_misses_total: m.cache_misses_total(),
              latency_p50_us: percentile_us(&bounds, &deltas, 0.50),
              latency_p99_us: percentile_us(&bounds, &deltas, 0.99),
          }
      }
  }
  ```
  plus gauges `nexora_control_revoked` and counter `nexora_control_cert_renewals_total` registered with the existing registry, and accessors `duration_bucket_counts`, `duration_bounds_us`, `queries_total`, `queries_with_rcode`, `cache_hits_total`, `cache_misses_total` summing the per-worker arrays. Run the tests and expect `ok`.
- [ ] Write the failing tests in `engine/src/control.rs` and in the bootstrap module:
  ```rust
  // engine/src/control.rs, #[cfg(test)] mod tests
  #[test]
  fn revoked_or_unknown_engine_backs_off_five_minutes() {
      let revoked = tonic::Status::permission_denied("certificate revoked");
      let unknown = tonic::Status::unauthenticated("unknown engine");
      for s in [&revoked, &unknown] {
          assert_eq!(reconnect_delay(Some(s), 0, 0.0), Duration::from_secs(270));
          assert_eq!(reconnect_delay(Some(s), 7, 1.0), Duration::from_secs(330));
      }
      let flaky = tonic::Status::unavailable("connection refused");
      assert!(reconnect_delay(Some(&flaky), 0, 0.5) < Duration::from_secs(5));
  }

  // bootstrap module, #[cfg(test)] mod tests
  #[test]
  fn node_name_env_override_is_validated() {
      let mut b = Bootstrap::parse_for_test("node_name = \"engine-1\"\nstate_dir = \"/tmp/x\"\nstandalone_snapshot = \"/tmp/s\"\n");
      apply_env_overrides(&mut b, |k| (k == "NEXORA_ENGINE_NODE_NAME").then(|| "edge-a-worker-21".to_string())).unwrap();
      assert_eq!(b.node_name, "edge-a-worker-21");
      let err = apply_env_overrides(&mut b, |k| (k == "NEXORA_ENGINE_NODE_NAME").then(|| "Bad_Name".to_string()));
      assert!(err.is_err());
      apply_env_overrides(&mut b, |_| None).unwrap();
      assert_eq!(b.node_name, "edge-a-worker-21");
  }
  ```
- [ ] Run `scripts/dev-exec.sh cargo test --manifest-path engine/Cargo.toml 'control::tests::revoked_or_unknown_engine_backs_off_five_minutes|node_name_env_override_is_validated'` and expect FAIL with `cannot find function `reconnect_delay``.
- [ ] Implement:
  - `reconnect_delay`: for `PermissionDenied` with message `certificate revoked` or `Unauthenticated` with message `unknown engine`, `Duration::from_secs_f64(300.0 * (0.9 + 0.2 * jitter))`; otherwise M1's jittered exponential backoff for `attempt`.
  - `apply_env_overrides`: when `NEXORA_ENGINE_NODE_NAME` is set, validate `^[a-z0-9-]{1,63}$` (error `NEXORA_ENGINE_NODE_NAME must match [a-z0-9-]{1,63}`) and replace `node_name`; called by the bootstrap loader right after parsing `engine.toml`.
  - `engine/src/control.rs` stream handling on the control runtime:
    - before loading the identity call `cert_renewal::recover_identity(state_dir)`;
    - on stream open and every 10 s: if `renewal_due(cert.not_before, cert.not_after, now)` and no renewal is pending, `new_csr(engine_id)` and send `EngineMessage{msg: CertRequest{csr_der, reason: REASON_RENEWAL}}`; keep the pending key in memory (never on disk until issued); a pending renewal older than 30 s is dropped so the next check retries;
    - on `ServerMessage::RenewCertificate{reason}`: the same, with that reason, unless one is pending;
    - on `ServerMessage::CertIssued{cert_der, ca_der}`: ignore without a pending renewal; require `sha256(ca_der)` to equal the SHA-256 of the DER in `identity/ca.pem`, the certificate's SubjectPublicKeyInfo to equal the pending key's public key, and the CN to equal the engine id; then `swap_identity(state_dir, cert_pem, key_pem)`, increment `nexora_control_cert_renewals_total`, close the stream and reconnect immediately with the new identity (backoff attempt reset); any check failure logs `rejected issued certificate: <reason>` and drops the pending renewal;
    - each `Stats` message sets `health = Some(sampler.sample(&metrics))`;
    - connect/stream errors go through `reconnect_delay`; a revoked/unknown status sets `nexora_control_revoked` to 1 and a successful `Hello` exchange sets it to 0; serving from the last applied snapshot continues in every case.
- [ ] Run `scripts/dev-exec.sh 'cargo test --manifest-path engine/Cargo.toml && cargo clippy --manifest-path engine/Cargo.toml --all-targets -- -D warnings'` and expect `test result: ok` for every suite, including `cache_hit_path_does_not_allocate`, and no clippy findings.
- [ ] Commit: `git add engine && git commit -m "feat(engine): certificate renewal, revocation backoff and fleet health stats"`.

## Task 10: Fleet acceptance tests

Files: `e2e/fleet_test.go` (`TestFleetRolloutAndPartition`, `TestGroupScopedConfig`, `TestJoinTokenGroupAndExpiry`), `e2e/fleet_canary_test.go` (`TestCanaryRolloutHaltsOnFailure`), `e2e/fleet_cert_test.go` (`TestEngineCertRevocation`)
Interfaces: consumes the Task 7 harness, M1 `harness.StartFixtureUpstream(t) *FixtureUpstream{Addr string}` (answers every `A` query for names under `fixture.test.` with `192.0.2.1`), M1 `(*Engine).Stop()` / `Start()`, M1 `(*Mgmt).Stop()`.

- [ ] Write `e2e/fleet_test.go`:
  ```go
  package e2e

  import (
  	"context"
  	"fmt"
  	"net"
  	"testing"
  	"time"

  	"github.com/jackc/pgx/v5"
  	"github.com/miekg/dns"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  func stableOf(g harness.EngineGroup) int64 {
  	if g.StableVersion == nil {
  		return 0
  	}
  	return *g.StableVersion
  }

  func TestFleetRolloutAndPartition(t *testing.T) {
  	f := harness.StartFleet(t, harness.FleetOptions{Engines: 3})
  	names := []string{"engine-1", "engine-2", "engine-3"}
  	f.WaitConnected(t, 3, 60*time.Second)

  	hosts, dirs := map[string]bool{}, map[string]bool{}
  	for _, n := range names {
  		host, _, _ := net.SplitHostPort(f.DNSAddr(n))
  		hosts[host], dirs[f.StateDir(n)] = true, true
  	}
  	if len(hosts) != 3 || len(dirs) != 3 {
  		t.Fatalf("engines must not share an address or state dir: %v %v", hosts, dirs)
  	}

  	base := stableOf(f.API.Group(t, harness.DefaultGroupID))
  	f.API.CreateRewrite(t, harness.Rewrite{Domain: "before.fleet.test", Type: "A", Value: "192.0.2.10"})
  	v1 := f.API.WaitGroupStable(t, harness.DefaultGroupID, base, 20*time.Second)
  	f.WaitAllApplied(t, names, v1, 20*time.Second)
  	for _, n := range names {
  		harness.ExpectA(t, f.DNSAddr(n), "before.fleet.test", "192.0.2.10")
  	}

  	f.API.CreateRewrite(t, harness.Rewrite{Domain: "after.fleet.test", Type: "A", Value: "192.0.2.20"})
  	v2 := f.API.WaitGroupStable(t, harness.DefaultGroupID, v1, 20*time.Second)
  	f.WaitAllApplied(t, names, v2, 20*time.Second)
  	acked := map[int64][]string{}
  	for _, e := range f.API.Engines(t) {
  		acked[e.AppliedVersion] = append(acked[e.AppliedVersion], e.Name)
  		if e.Drift != "in_sync" || e.TargetVersion != v2 {
  			t.Fatalf("%s drift %s target %d, want in_sync %d", e.Name, e.Drift, e.TargetVersion, v2)
  		}
  	}
  	if len(acked) != 1 || len(acked[v2]) != 3 {
  		t.Fatalf("applied versions %v, want all three engines on %d", acked, v2)
  	}

  	for _, m := range f.Mgmt {
  		m.Stop()
  	}
  	harness.Eventually(t, 60*time.Second, func() error {
  		for _, n := range names {
  			if v := harness.MetricValue(t, f.MetricsURL(n), "nexora_control_connected"); v != 0 {
  				return fmt.Errorf("%s still reports connected", n)
  			}
  		}
  		return nil
  	})
  	deadline := time.Now().Add(30 * time.Second)
  	for time.Now().Before(deadline) {
  		for _, n := range names {
  			harness.ExpectA(t, f.DNSAddr(n), "before.fleet.test", "192.0.2.10")
  			harness.ExpectA(t, f.DNSAddr(n), "after.fleet.test", "192.0.2.20")
  		}
  		time.Sleep(time.Second)
  	}

  	f.Engine("engine-2").Stop()
  	f.Engine("engine-2").Start()
  	harness.Eventually(t, 30*time.Second, func() error {
  		r, err := harness.Exchange(f.DNSAddr("engine-2"), "after.fleet.test", dns.TypeA)
  		if err != nil {
  			return err
  		}
  		if len(r.Answer) == 0 {
  			return fmt.Errorf("restarted engine answered %s without data", dns.RcodeToString[r.Rcode])
  		}
  		return nil
  	})
  }

  func TestGroupScopedConfig(t *testing.T) {
  	f := harness.StartFleet(t, harness.FleetOptions{})
  	a := f.API.CreateGroup(t, harness.GroupSpec{Name: "site-a"})
  	b := f.API.CreateGroup(t, harness.GroupSpec{Name: "site-b"})
  	f.AddEngine(t, "engine-a", a.ID, nil)
  	f.AddEngine(t, "engine-b", b.ID, nil)
  	f.WaitConnected(t, 2, 60*time.Second)

  	aID := a.ID
  	f.API.CreateRewrite(t, harness.Rewrite{Domain: "global.scope.test", Type: "A", Value: "192.0.2.1"})
  	f.API.CreateRewrite(t, harness.Rewrite{Domain: "only-a.scope.test", Type: "A", Value: "192.0.2.2", GroupID: &aID})
  	harness.Eventually(t, 30*time.Second, func() error {
  		for _, q := range []struct{ addr, name string }{{f.DNSAddr("engine-a"), "only-a.scope.test"}, {f.DNSAddr("engine-b"), "global.scope.test"}} {
  			r, err := harness.Exchange(q.addr, q.name, dns.TypeA)
  			if err != nil || len(r.Answer) == 0 {
  				return fmt.Errorf("%s not served yet at %s (%v)", q.name, q.addr, err)
  			}
  		}
  		return nil
  	})
  	harness.ExpectA(t, f.DNSAddr("engine-a"), "global.scope.test", "192.0.2.1")
  	harness.ExpectA(t, f.DNSAddr("engine-a"), "only-a.scope.test", "192.0.2.2")
  	harness.ExpectA(t, f.DNSAddr("engine-b"), "global.scope.test", "192.0.2.1")
  	if r, err := harness.Exchange(f.DNSAddr("engine-b"), "only-a.scope.test", dns.TypeA); err == nil {
  		for _, rr := range r.Answer {
  			if rec, ok := rr.(*dns.A); ok && rec.A.String() == "192.0.2.2" {
  				t.Fatal("engine in site-b served a rewrite scoped to site-a")
  			}
  		}
  	}

  	before := f.API.EngineByName(t, "engine-b").AppliedVersion
  	f.API.PatchEngine(t, "engine-b", map[string]any{"group_id": a.ID})
  	harness.Eventually(t, 30*time.Second, func() error {
  		e := f.API.EngineByName(t, "engine-b")
  		if e.GroupName != "site-a" || e.AppliedVersion <= before || e.Drift != "in_sync" {
  			return fmt.Errorf("engine-b group %s applied %d drift %s", e.GroupName, e.AppliedVersion, e.Drift)
  		}
  		return nil
  	})
  	harness.ExpectA(t, f.DNSAddr("engine-b"), "only-a.scope.test", "192.0.2.2")
  }

  func TestJoinTokenGroupAndExpiry(t *testing.T) {
  	ctx := context.Background()
  	f := harness.StartFleet(t, harness.FleetOptions{})
  	g := f.API.CreateGroup(t, harness.GroupSpec{Name: "site-x"})

  	tok := f.API.CreateJoinToken(t, harness.JoinTokenSpec{GroupID: g.ID, TTLSeconds: 600, MaxUses: 1, Labels: map[string]string{"rack": "r1"}})
  	f.AddEngineWithToken(t, "joined", tok.Token, nil)
  	harness.Eventually(t, 60*time.Second, func() error {
  		for _, e := range f.API.Engines(t) {
  			if e.Name == "joined" && e.ConnectionState == "connected" && e.GroupName == "site-x" && e.Labels["rack"] == "r1" {
  				return nil
  			}
  		}
  		return fmt.Errorf("engine joined not connected in site-x with rack=r1")
  	})

  	f.AddEngineWithToken(t, "second", tok.Token, nil)
  	expired := f.API.CreateJoinToken(t, harness.JoinTokenSpec{GroupID: g.ID, TTLSeconds: 600, MaxUses: 5})
  	conn, err := pgx.Connect(ctx, f.DB.URL)
  	if err != nil {
  		t.Fatal(err)
  	}
  	defer conn.Close(ctx)
  	if _, err := conn.Exec(ctx, `UPDATE join_tokens SET expires_at = now() - interval '1 second' WHERE id = $1`, expired.ID); err != nil {
  		t.Fatal(err)
  	}
  	f.AddEngineWithToken(t, "late", expired.Token, nil)

  	time.Sleep(20 * time.Second)
  	for _, e := range f.API.Engines(t) {
  		if e.Name == "second" || e.Name == "late" {
  			t.Fatalf("engine %s enrolled with an exhausted or expired token", e.Name)
  		}
  	}
  	var listed []harness.JoinToken
  	f.API.Must(t, "GET", "/api/v1/join-tokens", nil, &listed, 200)
  	states := map[string]string{}
  	for _, jt := range listed {
  		states[jt.ID] = jt.State
  	}
  	if states[tok.ID] != "exhausted" || states[expired.ID] != "expired" {
  		t.Fatalf("token states %v", states)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e/ -run "TestFleetRolloutAndPartition|TestGroupScopedConfig|TestJoinTokenGroupAndExpiry" -count=1 -v -timeout 20m'`. Tasks 2–9 are in place, so expect `--- PASS` for all three. To prove the partition assertion bites, temporarily make the harness `(*Mgmt).Stop` a no-op and rerun `TestFleetRolloutAndPartition`; expect FAIL with `still reports connected`; restore.
- [ ] Write `e2e/fleet_canary_test.go`:
  ```go
  package e2e

  import (
  	"fmt"
  	"net"
  	"net/http"
  	"slices"
  	"strings"
  	"sync"
  	"testing"
  	"time"

  	"github.com/miekg/dns"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  func closedUDPAddr(t *testing.T) string {
  	t.Helper()
  	c, err := net.ListenPacket("udp", "127.0.0.1:0")
  	if err != nil {
  		t.Fatal(err)
  	}
  	addr := c.LocalAddr().String()
  	_ = c.Close()
  	return addr
  }

  func TestCanaryRolloutHaltsOnFailure(t *testing.T) {
  	up := harness.StartFixtureUpstream(t)
  	f := harness.StartFleet(t, harness.FleetOptions{})
  	g := f.API.CreateGroup(t, harness.GroupSpec{Name: "canary", UpstreamMode: "override", RolloutStrategy: "canary",
  		CanaryCount: 1, AckTimeoutSeconds: 30, HealthWindowSeconds: 30, MaxServfailRatio: 0.05, MinHealthQueries: 20})
  	names := []string{"engine-1", "engine-2", "engine-3"}
  	for _, n := range names {
  		f.AddEngine(t, n, g.ID, nil)
  	}
  	f.WaitConnected(t, 3, 60*time.Second)
  	f.API.PatchEngine(t, "engine-3", map[string]any{"labels": map[string]string{"nexora.io/canary": "true"}})
  	canaryID := f.API.EngineByName(t, "engine-3").ID

  	stop := make(chan struct{})
  	var wg sync.WaitGroup
  	for _, n := range names {
  		addr := f.DNSAddr(n)
  		wg.Add(1)
  		go func() {
  			defer wg.Done()
  			for i := 0; ; i++ {
  				select {
  				case <-stop:
  					return
  				case <-time.After(50 * time.Millisecond):
  				}
  				_, _ = harness.Exchange(addr, fmt.Sprintf("load-%d-%d.fixture.test", i, time.Now().UnixNano()), dns.TypeA)
  			}
  		}()
  	}
  	defer func() { close(stop); wg.Wait() }()

  	gid := g.ID
  	upID := f.API.CreateUpstream(t, harness.Upstream{Name: "fixture", Address: up.Addr, Protocol: "udp", GroupID: &gid})
  	var rs []harness.Rollout
  	f.API.Must(t, "GET", "/api/v1/rollouts?limit=1&group_id="+g.ID, nil, &rs, http.StatusOK)

  	// Positive path: a healthy change passes the canary gate and completes everywhere.
  	good := f.API.WaitRollout(t, g.ID, rs[0].Version, 3*time.Minute, "completed")
  	if good.Strategy != "canary" || !slices.Equal(good.CanaryEngineIDs, []string{canaryID}) {
  		t.Fatalf("healthy rollout %+v, want canary strategy with engine-3 as canary", good)
  	}
  	f.WaitAllApplied(t, names, good.Version, 30*time.Second)
  	for _, n := range names {
  		harness.ExpectA(t, f.DNSAddr(n), "healthy.fixture.test", "192.0.2.1")
  	}

  	f.API.SetUpstreamAddress(t, upID, closedUDPAddr(t))
  	halted := f.API.WaitRollout(t, g.ID, good.Version+1, 3*time.Minute, "halted")
  	if !strings.Contains(halted.HaltReason, "engine-3 servfail ratio") || !slices.Equal(halted.CanaryEngineIDs, []string{canaryID}) {
  		t.Fatalf("halted rollout %+v", halted)
  	}
  	if e := f.API.EngineByName(t, "engine-3"); e.AppliedVersion != halted.Version {
  		t.Fatalf("canary applied %d, want %d", e.AppliedVersion, halted.Version)
  	}
  	for _, n := range []string{"engine-1", "engine-2"} {
  		if e := f.API.EngineByName(t, n); e.AppliedVersion != good.Version {
  			t.Fatalf("%s applied %d during a halted canary, want %d", n, e.AppliedVersion, good.Version)
  		}
  		harness.ExpectA(t, f.DNSAddr(n), "still-good.fixture.test", "192.0.2.1")
  	}
  	time.Sleep(15 * time.Second)
  	f.API.Must(t, "GET", "/api/v1/rollouts?limit=1&group_id="+g.ID, nil, &rs, http.StatusOK)
  	if rs[0].State != "halted" || f.API.EngineByName(t, "engine-1").AppliedVersion != good.Version {
  		t.Fatalf("halted rollout progressed on its own: %+v", rs[0])
  	}

  	var rb harness.Rollout
  	f.API.Must(t, "POST", "/api/v1/engine-groups/"+g.ID+"/rollback", map[string]any{"to_version": good.Version}, &rb, http.StatusAccepted)
  	done := f.API.WaitRollout(t, g.ID, rb.Version, time.Minute, "completed")
  	f.WaitAllApplied(t, names, done.Version, 30*time.Second)
  	harness.ExpectA(t, f.DNSAddr("engine-3"), "recovered.fixture.test", "192.0.2.1")

  	f.API.CreateRewrite(t, harness.Rewrite{Domain: "held.fleet.test", Type: "A", Value: "192.0.2.99", GroupID: &gid})
  	time.Sleep(5 * time.Second)
  	f.API.Must(t, "GET", "/api/v1/rollouts?limit=1&group_id="+g.ID, nil, &rs, http.StatusOK)
  	if rs[0].State != "pending" || f.API.EngineByName(t, "engine-1").AppliedVersion != done.Version {
  		t.Fatalf("change after rollback must wait while paused: %+v", rs[0])
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'go test ./e2e/ -run TestCanaryRolloutHaltsOnFailure -count=1 -v -timeout 20m'` and expect `--- PASS`. Mutation check: change `MaxServfailRatio: 0.05` in the test to `1` (the gate can never trip), rerun, and expect FAIL with `waiting for [halted]`; restore `0.05`.
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

  	"github.com/miekg/dns"
  	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
  	"google.golang.org/grpc"
  	"google.golang.org/grpc/codes"
  	"google.golang.org/grpc/credentials"
  	"google.golang.org/grpc/status"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  // exportAs calls the builtin OTLP logs service with an engine's own identity files.
  func exportAs(t *testing.T, f *harness.Fleet, engine string) error {
  	t.Helper()
  	id := filepath.Join(f.StateDir(engine), "identity")
  	pair, err := tls.LoadX509KeyPair(filepath.Join(id, "cert.pem"), filepath.Join(id, "key.pem"))
  	if err != nil {
  		t.Fatal(err)
  	}
  	// Server identity is not under test here; the client certificate is.
  	creds := credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{pair}, InsecureSkipVerify: true}) //nolint:gosec
  	conn, err := grpc.NewClient(f.Mgmt[0].GRPCAddr, grpc.WithTransportCredentials(creds))
  	if err != nil {
  		t.Fatal(err)
  	}
  	defer conn.Close()
  	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
  	defer cancel()
  	_, err = collogspb.NewLogsServiceClient(conn).Export(ctx, &collogspb.ExportLogsServiceRequest{})
  	return err
  }

  type engineDetail struct {
  	ConnectionState string `json:"connection_state"`
  	Certificates    []struct {
  		Serial       string  `json:"serial"`
  		RevokeReason *string `json:"revoke_reason"`
  	} `json:"certificates"`
  }

  func TestEngineCertRevocation(t *testing.T) {
  	f := harness.StartFleet(t, harness.FleetOptions{Engines: 3, MgmtEnv: map[string]string{"NEXORA_ENGINE_CERT_TTL": "60s"}})
  	f.WaitConnected(t, 3, 60*time.Second)
  	base := stableOf(f.API.Group(t, harness.DefaultGroupID))
  	f.API.CreateRewrite(t, harness.Rewrite{Domain: "rev.fleet.test", Type: "A", Value: "192.0.2.30"})
  	v1 := f.API.WaitGroupStable(t, harness.DefaultGroupID, base, 20*time.Second)
  	f.WaitAllApplied(t, []string{"engine-1", "engine-2", "engine-3"}, v1, 20*time.Second)

  	if err := exportAs(t, f, "engine-1"); err != nil {
  		t.Fatalf("engine-1 identity rejected before revocation: %v", err)
  	}
  	e1 := f.API.EngineByName(t, "engine-1")
  	f.API.Must(t, "POST", "/api/v1/engines/"+e1.ID+"/revoke", nil, nil, http.StatusOK)
  	harness.Eventually(t, 15*time.Second, func() error {
  		if s := f.API.EngineByName(t, "engine-1").ConnectionState; s != "revoked" {
  			return fmt.Errorf("state %s", s)
  		}
  		if harness.MetricValue(t, f.MetricsURL("engine-1"), "nexora_control_revoked") != 1 ||
  			harness.MetricValue(t, f.MetricsURL("engine-1"), "nexora_control_connected") != 0 {
  			return fmt.Errorf("engine-1 has not observed its revocation")
  		}
  		if s, _ := status.FromError(exportAs(t, f, "engine-1")); s.Code() != codes.PermissionDenied || s.Message() != "certificate revoked" {
  			return fmt.Errorf("export as engine-1: %v", s)
  		}
  		return nil
  	})
  	harness.ExpectA(t, f.DNSAddr("engine-1"), "rev.fleet.test", "192.0.2.30")

  	f.API.CreateRewrite(t, harness.Rewrite{Domain: "post-revoke.fleet.test", Type: "A", Value: "192.0.2.31"})
  	v2 := f.API.WaitGroupStable(t, harness.DefaultGroupID, v1, 20*time.Second)
  	f.WaitAllApplied(t, []string{"engine-2", "engine-3"}, v2, 20*time.Second)
  	harness.ExpectA(t, f.DNSAddr("engine-2"), "post-revoke.fleet.test", "192.0.2.31")
  	if got := f.API.EngineByName(t, "engine-1").AppliedVersion; got != v1 {
  		t.Fatalf("revoked engine applied %d, want %d", got, v1)
  	}
  	if r, err := harness.Exchange(f.DNSAddr("engine-1"), "post-revoke.fleet.test", dns.TypeA); err == nil {
  		for _, rr := range r.Answer {
  			if a, ok := rr.(*dns.A); ok && a.A.String() == "192.0.2.31" {
  				t.Fatal("revoked engine received config after revocation")
  			}
  		}
  	}

  	e2 := f.API.EngineByName(t, "engine-2")
  	oldSerial := e2.Certificate.Serial
  	f.API.Must(t, "POST", "/api/v1/engines/"+e2.ID+"/rotate-certificate", nil, nil, http.StatusAccepted)
  	harness.Eventually(t, 30*time.Second, func() error {
  		var d engineDetail
  		f.API.Must(t, "GET", "/api/v1/engines/"+e2.ID, nil, &d, http.StatusOK)
  		if d.ConnectionState != "connected" || len(d.Certificates) < 2 || d.Certificates[0].Serial == oldSerial {
  			return fmt.Errorf("engine-2 not rotated yet: %+v", d)
  		}
  		for _, c := range d.Certificates {
  			if c.Serial == oldSerial && (c.RevokeReason == nil || *c.RevokeReason != "superseded") {
  				return fmt.Errorf("old serial %s not superseded yet", oldSerial)
  			}
  		}
  		return nil
  	})

  	s3 := f.API.EngineByName(t, "engine-3").Certificate.Serial
  	harness.Eventually(t, 75*time.Second, func() error {
  		e := f.API.EngineByName(t, "engine-3")
  		if e.Certificate == nil || e.Certificate.Serial == s3 || e.ConnectionState != "connected" {
  			return fmt.Errorf("engine-3 has not renewed on its own")
  		}
  		return nil
  	})

  	if v := harness.MetricValue(t, f.Mgmt[0].HTTPURL+"/metrics", "nexora_mgmt_engines_disconnected"); v != 0 {
  		t.Fatalf("disconnected gauge %v with every non-revoked engine connected", v)
  	}
  	f.Engine("engine-3").Stop()
  	harness.Eventually(t, 110*time.Second, func() error {
  		if v := harness.MetricValue(t, f.Mgmt[0].HTTPURL+"/metrics", "nexora_mgmt_engines_disconnected"); v != 1 {
  			return fmt.Errorf("nexora_mgmt_engines_disconnected = %v, want 1", v)
  		}
  		return nil
  	})
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'go test ./e2e/ -run TestEngineCertRevocation -count=1 -v -timeout 20m'` and expect `--- PASS`. Mutation check: comment out the `CheckConnect` call in the stream interceptor and rerun; expect FAIL with `export as engine-1`; restore.
- [ ] Run the regression set `scripts/dev-exec.sh 'go test ./e2e/ -run "TestMgmtStatelessHA|TestInvalidSnapshotRejected|TestAuthoritativeZonePropagation|TestFleetAPI|TestMgmtCLIFleet" -count=1 -v -timeout 30m'` and expect `--- PASS` for each (`TestMgmtStatelessHA` keeps the engine's control stream failing over within 10 s because targets are computed from Postgres on whichever instance receives `Hello`).
- [ ] Commit: `git add e2e/fleet_test.go e2e/fleet_canary_test.go e2e/fleet_cert_test.go && git commit -m "test(e2e): fleet rollout, canary halt, scoping, join tokens, revocation"`.

## Task 11: Fleet GUI

Files: `web/src/api/fleet.ts` (TanStack Query hooks over the generated client), `web/src/routes/engines/FleetPage.tsx` (`/engines`: summary, groups, rollouts, engines), `web/src/routes/engines/GroupDetailPage.tsx` (`/engines/groups/:groupId`: settings, join tokens, rollback/resume, delete), `web/src/routes/engines/EngineDetailPage.tsx` (`/engines/:engineId`: identity, certificate, group/labels, charts, revoke/rotate/delete), `web/src/routes/engines/RolloutDetailPage.tsx` (`/engines/rollouts/:rolloutId`), `web/src/components/fleet/RolloutProgress.tsx`, `web/src/components/fleet/StatusBadges.tsx` (connection state, drift, rollout state), `web/src/components/fleet/ScopeSelect.tsx` (Global / group picker for scoped resources), `web/src/components/fleet/LabelsEditor.tsx`, `web/src/components/fleet/fleet.test.tsx` (vitest), `web/src/router.tsx` (routes), the scoped resource pages (`/upstreams`, `/access-control`, `/filtering`, `/policies`, `/rewrites`, `/zones`, `/rpz`, `/settings` telemetry section: scope field, Scope column, scope filter), `web/e2e/fleet.spec.ts` (Playwright), `e2e/gui_fleet_test.go` (`TestGUIFleet` wrapper)
Interfaces: routes above; `ScopeSelect` props `{ value: string | null; onChange(v: string | null): void; label?: string }` where `null` means global; visible copy used by tests: buttons `New group`, `Create`, `Save`, `New join token`, `Revoke`, `Roll back`, `Resume rollouts`, `Rotate certificate`, `Revoke engine`, `Delete engine`, `Delete group`, `Confirm`; headings `Fleet`, `Groups`, `Rollouts`, `Engines`; banner `Change rollouts are paused for this group`; badges `Connected`, `Disconnected`, `Never connected`, `Revoked`, `In sync`, `Behind`, `Ahead`, `Rejected`, `Unknown`, and rollout states in Title Case (`Pending`, `Canary`, `Verifying`, `Rolling`, `Completed`, `Halted`, `Rolled back`, `Superseded`); scope column text `Global` or the group name.

- [ ] Write the failing unit test `web/src/components/fleet/fleet.test.tsx`:
  ```tsx
  import { render, screen } from "@testing-library/react";
  import { describe, expect, it } from "vitest";
  import { RolloutProgress } from "./RolloutProgress";
  import { ConnectionBadge, DriftBadge, RolloutStateBadge } from "./StatusBadges";

  describe("fleet components", () => {
    it("shows applied over total and the halt reason", () => {
      render(
        <RolloutProgress
          rollout={{ state: "halted", progress: { total: 3, applied: 1, rejected: 0 }, halt_reason: "engine engine-3 servfail ratio 0.950 > 0.050 over 400 queries" }}
        />,
      );
      expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "33");
      expect(screen.getByText("1 / 3 applied")).toBeInTheDocument();
      expect(screen.getByText(/servfail ratio 0.950/)).toBeInTheDocument();
    });

    it("labels states for humans", () => {
      render(
        <>
          <ConnectionBadge state="never_connected" />
          <DriftBadge drift="ahead" />
          <RolloutStateBadge state="rolled_back" />
        </>,
      );
      expect(screen.getByText("Never connected")).toBeInTheDocument();
      expect(screen.getByText("Ahead")).toBeInTheDocument();
      expect(screen.getByText("Rolled back")).toBeInTheDocument();
    });
  });
  ```
- [ ] Run `scripts/dev-exec.sh 'pnpm --dir web vitest run src/components/fleet'` and expect FAIL with `Failed to resolve import "./RolloutProgress"`.
- [ ] Implement the components: `RolloutProgress` renders a Radix `Progress` with `aria-valuenow = round(applied / total * 100)` (0 when total is 0), the text `<applied> / <total> applied`, `<rejected> rejected` when non-zero, and `halt_reason` in a destructive-coloured line when state is `halted`; badges map the enum values to the copy listed under Interfaces (connected/in_sync/completed green, behind/pending/canary/verifying/rolling blue, disconnected/ahead amber, revoked/rejected/halted red, others grey). Run the vitest command and expect `2 passed`.
- [ ] Implement `web/src/api/fleet.ts` with one hook per operation (`useFleetSummary`, `useEngineGroups`, `useEngineGroup(id)`, `useRollouts({groupId,state})`, `useRollout(id)`, `useEngines(filters)`, `useEngine(id)`, `useEngineStats(id, window)`, `useJoinTokens`, and mutations `useCreateEngineGroup`, `useUpdateEngineGroup`, `useDeleteEngineGroup`, `useRollbackEngineGroup`, `useResumeRollouts`, `useUpdateEngine`, `useDeleteEngine`, `useRevokeEngine`, `useRotateEngineCertificate`, `useCreateJoinToken`, `useRevokeJoinToken`); list queries refetch every 5 s while the page is visible; mutations invalidate `["fleet"]`; a 409 `conflict` shows the toast `Someone else changed this; reload to see their change`.
- [ ] Implement the pages:
  - `/engines` (`FleetPage`, heading `Fleet`): summary cards (connected/total, disconnected, drift not in sync, fleet QPS, cache hit ratio as percent, max p99 in ms, halted rollouts); `Groups` table (name linking to the group page, strategy, engines connected/total, stable version, active rollout `RolloutProgress`) with `New group` dialog (name, description, upstream mode, strategy, canary count, canary percent, ack timeout, health window, max SERVFAIL ratio, min health queries; submit `Create`); `Rollouts` table (newest 20: group, version, kind, strategy, `RolloutStateBadge`, progress, created) linking to the rollout page; `Engines` table (name linking to engine page, group, `ConnectionBadge`, `DriftBadge`, applied/target version, QPS, p99, last seen, labels as chips) with filters group, state, drift.
  - `/engines/groups/:groupId`: settings form (`Save`, sends `revision`); paused banner `Change rollouts are paused for this group` with `Resume rollouts`; `Roll back` dialog with a select of the group's earlier versions taken from its rollouts (`to_version`) and `Confirm`; join token table (state, uses/max, expires, labels, `Revoke` per active row) and `New join token` dialog (TTL select 1h/24h/7d, max uses, labels) that shows the created token once in a read-only field with a copy button; scoped resource counts; `Delete group` (disabled for `default`; `Confirm` dialog; 409 message shown inline).
  - `/engines/:engineId`: identity (id, node name, created), `ConnectionBadge`, connected instance, last seen; versions (applied, target, `DriftBadge`, rejection reason); certificate (serial, not before/after, history table with revoke reason); group select and `LabelsEditor` with `Save`; Recharts line charts for QPS, cache hit ratio, p99 over the selected window (5m/1h/24h); buttons `Rotate certificate`, `Revoke engine`, `Delete engine`, each behind a `Confirm` dialog; admin-only buttons are hidden for other roles using the M1 role hook.
  - `/engines/rollouts/:rolloutId`: rollout header (`RolloutStateBadge`, kind, strategy, version, from version, created by, halt reason), `RolloutProgress`, per-engine table (name, canary marker, connection, applied version, status).
  - Scoped resource pages: a `ScopeSelect` field (options `Global` and every group name) in each create/edit form, a `Scope` column, and a scope filter above each list.
- [ ] Write the failing Playwright spec `web/e2e/fleet.spec.ts` (uses M1's `loginAsAdmin` and `covers` helpers from `web/e2e/helpers`, the mechanism `TestGUICoverage` reads):
  ```ts
  import { expect, test } from "@playwright/test";
  import { covers, loginAsAdmin } from "./helpers";

  test.describe("fleet", () => {
    test.beforeEach(async ({ page }) => {
      await loginAsAdmin(page);
    });

    test("fleet view, groups, join tokens, engines, rollouts", async ({ page }) => {
      covers("getFleetSummary", "listEngineGroups", "listEngines", "listRollouts", "createEngineGroup", "getEngineGroup",
        "updateEngineGroup", "createJoinToken", "listJoinTokens", "revokeJoinToken", "getEngine", "updateEngine",
        "getEngineStats", "rotateEngineCertificate", "getRollout", "rollbackEngineGroup", "resumeEngineGroupRollouts",
        "revokeEngine", "deleteEngine", "deleteEngineGroup");

      await page.goto("/engines");
      await expect(page.getByRole("heading", { name: "Fleet" })).toBeVisible();
      const engines = page.getByRole("region", { name: "Engines" });
      await expect(engines.getByRole("row", { name: /engine-1/ })).toContainText("Connected");
      await expect(engines.getByRole("row", { name: /engine-2/ })).toContainText("In sync");

      await page.getByRole("button", { name: "New group" }).click();
      await page.getByLabel("Name").fill("edge-gui");
      await page.getByLabel("Strategy").selectOption("canary");
      await page.getByLabel("Canary count").fill("1");
      await page.getByRole("button", { name: "Create" }).click();
      await page.getByRole("region", { name: "Groups" }).getByRole("link", { name: "edge-gui" }).click();

      await page.getByLabel("Health window (seconds)").fill("45");
      await page.getByRole("button", { name: "Save" }).click();
      await expect(page.getByText("Saved")).toBeVisible();

      await page.getByRole("button", { name: "New join token" }).click();
      await page.getByLabel("Max uses").fill("2");
      await page.getByRole("button", { name: "Create" }).click();
      await expect(page.getByLabel("Join token")).toHaveValue(/^nxj1\.[A-Z2-7]+\.[0-9a-f]{64}$/);
      await page.keyboard.press("Escape");
      const tokens = page.getByRole("region", { name: "Join tokens" });
      await tokens.getByRole("button", { name: "Revoke" }).first().click();
      await page.getByRole("button", { name: "Confirm" }).click();
      await expect(tokens.getByRole("row").nth(1)).toContainText("revoked");

      await page.goto("/engines");
      await engines.getByRole("link", { name: "engine-2" }).click();
      await page.getByLabel("Group").selectOption({ label: "edge-gui" });
      await page.getByRole("button", { name: "Add label" }).click();
      await page.getByLabel("Label key").last().fill("nexora.io/canary");
      await page.getByLabel("Label value").last().fill("true");
      await page.getByRole("button", { name: "Save" }).click();
      await expect(page.getByText("edge-gui")).toBeVisible();
      await expect(page.getByRole("img", { name: "QPS" })).toBeVisible();
      await page.getByRole("button", { name: "Rotate certificate" }).click();
      await page.getByRole("button", { name: "Confirm" }).click();
      await expect(page.getByText("Rotation requested")).toBeVisible();

      await page.goto("/engines");
      await page.getByRole("region", { name: "Rollouts" }).getByRole("link").first().click();
      await expect(page.getByRole("progressbar")).toBeVisible();
      await expect(page.getByRole("table", { name: "Engines in this rollout" })).toBeVisible();

      await page.goto("/engines");
      await page.getByRole("region", { name: "Groups" }).getByRole("link", { name: "edge-gui" }).click();
      await page.getByRole("button", { name: "Roll back" }).click();
      await page.getByLabel("Version").selectOption({ index: 1 });
      await page.getByRole("button", { name: "Confirm" }).click();
      await expect(page.getByText("Change rollouts are paused for this group")).toBeVisible();
      await page.getByRole("button", { name: "Resume rollouts" }).click();
      await expect(page.getByText("Change rollouts are paused for this group")).toBeHidden();

      await page.goto("/engines");
      await engines.getByRole("link", { name: "engine-1" }).click();
      await page.getByRole("button", { name: "Revoke engine" }).click();
      await page.getByRole("button", { name: "Confirm" }).click();
      await expect(page.getByText("Revoked").first()).toBeVisible();
      await page.getByRole("button", { name: "Delete engine" }).click();
      await page.getByRole("button", { name: "Confirm" }).click();
      await expect(page).toHaveURL(/\/engines$/);
      await expect(engines.getByRole("link", { name: "engine-1" })).toHaveCount(0);

      await page.getByRole("button", { name: "New group" }).click();
      await page.getByLabel("Name").fill("tmp-gui");
      await page.getByRole("button", { name: "Create" }).click();
      await page.getByRole("region", { name: "Groups" }).getByRole("link", { name: "tmp-gui" }).click();
      await page.getByRole("button", { name: "Delete group" }).click();
      await page.getByRole("button", { name: "Confirm" }).click();
      await expect(page).toHaveURL(/\/engines$/);
      await expect(page.getByRole("region", { name: "Groups" }).getByRole("link", { name: "tmp-gui" })).toHaveCount(0);
    });

    test("scoped rewrite shows its group", async ({ page }) => {
      await page.goto("/rewrites");
      await page.getByRole("button", { name: "New rewrite" }).click();
      await page.getByLabel("Domain").fill("scoped-gui.fleet.test");
      await page.getByLabel("Type").selectOption("A");
      await page.getByLabel("Value").fill("192.0.2.77");
      await page.getByLabel("Scope").selectOption({ label: "default" });
      await page.getByRole("button", { name: "Create" }).click();
      await expect(page.getByRole("row", { name: /scoped-gui\.fleet\.test/ })).toContainText("default");
      await page.getByLabel("Scope filter").selectOption({ label: "Global" });
      await expect(page.getByRole("row", { name: /scoped-gui\.fleet\.test/ })).toHaveCount(0);
    });
  });
  ```
  Tables in the pages are wrapped in `<section aria-labelledby>` so `getByRole("region", { name })` resolves; the per-engine rollout table carries `aria-label="Engines in this rollout"`; charts carry `role="img"` with `aria-label` `QPS`, `Cache hit ratio`, `p99 latency`.
- [ ] Write the Go wrapper `e2e/gui_fleet_test.go`:
  ```go
  package e2e

  import (
  	"testing"
  	"time"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  func TestGUIFleet(t *testing.T) {
  	f := harness.StartFleet(t, harness.FleetOptions{Engines: 2})
  	f.WaitConnected(t, 2, 60*time.Second)
  	harness.RunPlaywright(t, harness.PlaywrightOptions{Spec: "fleet.spec.ts", Mgmt: f.Mgmt[0]})
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'make e2e-build && go test ./e2e/ -run TestGUIFleet -count=1 -v'` before the pages exist and expect FAIL with `getByRole('heading', { name: 'Fleet' })` in the Playwright output; after the pages, expect `--- PASS: TestGUIFleet`.
- [ ] Run `scripts/dev-exec.sh 'pnpm --dir web run lint && pnpm --dir web run typecheck && pnpm --dir web vitest run && go test ./e2e/ -run "TestGUICoverage|TestGUIFleet" -count=1'` and expect lint and typecheck clean, vitest passing, and `ok` (every OpenAPI operation, including the fleet ones, is covered).
- [ ] Commit: `git add web e2e/gui_fleet_test.go && git commit -m "feat(web): fleet view, group, engine and rollout pages"`.

## Task 12: Release images workflow and docker-compose example

Files: `.github/workflows/images.yml` (per-arch builds, digest merge), `.github/actionlint.yaml` (self-hosted runner labels), `deploy/compose/docker-compose.yml` (example stack), `deploy/compose/engine.toml` (engine bootstrap for compose), `deploy/compose/otel-collector.yaml` (optional collector config), `deploy/compose/.env.example` (required variables), `deploy/compose/secrets/.gitignore` (keeps the token directory, never its contents), `deploy/deploytest/workflow_test.go` (`TestImagesWorkflow`), `deploy/deploytest/compose_test.go` (`TestComposeExample`)
Interfaces: images `192.168.10.131:5000/azrtydxb/nexora-engine` and `.../nexora-mgmt` tagged `sha-<7>` (and `main` on main); compose services `postgres`, `ca-init`, `migrate`, `mgmt`, `engine` (profile `engine`), `otel-collector` (profile `otel`).

- [ ] Write the failing test `deploy/deploytest/workflow_test.go`:
  ```go
  package deploytest

  import (
  	"os"
  	"regexp"
  	"strings"
  	"testing"

  	"gopkg.in/yaml.v3"
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
  		Matrix struct {
  			Include []map[string]string `yaml:"include"`
  			Image   []string            `yaml:"image"`
  		} `yaml:"matrix"`
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
  	combos := map[string]bool{}
  	for _, inc := range build.Strategy.Matrix.Include {
  		combos[inc["image"]+"|"+inc["runner"]+"|"+inc["platform"]] = true
  	}
  	for _, img := range []string{"nexora-engine", "nexora-mgmt"} {
  		for _, rp := range []string{"arc-azrtydxb-publish|linux/arm64", "arc-azrtydxb-amd64-publish|linux/amd64"} {
  			if !combos[img+"|"+rp] {
  				t.Errorf("build matrix lacks %s on %s", img, rp)
  			}
  		}
  	}
  	if build.RunsOn != "${{ matrix.runner }}" || merge.RunsOn != "arc-azrtydxb-publish" || merge.Needs != "build" {
  		t.Errorf("runs-on/needs: build %q merge %q needs %v", build.RunsOn, merge.RunsOn, merge.Needs)
  	}
  	pinned := regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)
  	all := strings.Builder{}
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
  	for _, want := range []string{"push-by-digest=true", "imagetools create", "sha-${GITHUB_SHA::7}", `grep -q '"arm64"'`, `grep -q '"amd64"'`} {
  		if !strings.Contains(all.String(), want) {
  			t.Errorf("workflow does not contain %q", want)
  		}
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./deploy/deploytest/ -run TestImagesWorkflow -count=1` and expect FAIL with `no such file or directory`.
- [ ] Verify the M1 Dockerfiles build for either architecture: `scripts/dev-exec.sh 'grep -nE "arm64|aarch64|amd64|x86_64" deploy/docker/engine.Dockerfile deploy/docker/mgmt.Dockerfile'` must print nothing; where it prints a line, replace the hard-coded architecture with `ARG TARGETARCH` / `$TARGETARCH` (Go: `GOARCH=$TARGETARCH`; Rust builds natively on each runner so no cross target is needed).
- [ ] Create `.github/workflows/images.yml`:
  ```yaml
  # Release images for engine and management plane. Each architecture builds on
  # hardware of that architecture (kw is arm64 without emulation); the merge job
  # stitches the per-arch digests into one multi-arch tag.
  name: images

  on:
    push:
      branches: [main]
      paths:
        - "engine/**"
        - "mgmt/**"
        - "web/**"
        - "proto/**"
        - "gen/**"
        - "go.mod"
        - "go.sum"
        - "deploy/docker/**"
        - ".github/workflows/images.yml"
    workflow_dispatch:

  concurrency:
    group: ${{ github.workflow }}-${{ github.ref }}
    cancel-in-progress: false

  permissions:
    contents: read

  env:
    PUSH_REGISTRY: 192.168.10.131:5000

  jobs:
    build:
      strategy:
        fail-fast: false
        matrix:
          include:
            - { image: nexora-engine, dockerfile: deploy/docker/engine.Dockerfile, runner: arc-azrtydxb-publish, platform: linux/arm64, arch: arm64 }
            - { image: nexora-engine, dockerfile: deploy/docker/engine.Dockerfile, runner: arc-azrtydxb-amd64-publish, platform: linux/amd64, arch: amd64 }
            - { image: nexora-mgmt, dockerfile: deploy/docker/mgmt.Dockerfile, runner: arc-azrtydxb-publish, platform: linux/arm64, arch: arm64 }
            - { image: nexora-mgmt, dockerfile: deploy/docker/mgmt.Dockerfile, runner: arc-azrtydxb-amd64-publish, platform: linux/amd64, arch: amd64 }
      runs-on: ${{ matrix.runner }}
      timeout-minutes: 120
      steps:
        - uses: actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4

        - name: Set up buildx
          run: |
            cat > /tmp/buildkitd.toml <<'TOML'
            [registry."192.168.10.131"]
              insecure = true
            [registry."192.168.10.131:5000"]
              insecure = true
            [registry."docker.io"]
              mirrors = ["192.168.10.131"]
            TOML
            docker buildx create --name builder --driver docker-container \
              --platform ${{ matrix.platform }} --buildkitd-config /tmp/buildkitd.toml --use
            docker buildx inspect --bootstrap

        - uses: docker/login-action@c94ce9fb468520275223c153574b00df6fe4bcc9 # v3
          with:
            registry: ${{ env.PUSH_REGISTRY }}
            username: ${{ secrets.NEXUS_USER }}
            password: ${{ secrets.NEXUS_PASSWORD }}

        - name: Build and push by digest
          id: build
          uses: docker/build-push-action@53b7df96c91f9c12dcc8a07bcb9ccacbed38856a # v7.3.0
          with:
            context: .
            file: ${{ matrix.dockerfile }}
            builder: builder
            platforms: ${{ matrix.platform }}
            outputs: type=image,name=${{ env.PUSH_REGISTRY }}/azrtydxb/${{ matrix.image }},push-by-digest=true,name-canonical=true,push=true
            cache-from: type=registry,ref=${{ env.PUSH_REGISTRY }}/azrtydxb/${{ matrix.image }}-buildcache:${{ matrix.arch }}
            cache-to: type=registry,ref=${{ env.PUSH_REGISTRY }}/azrtydxb/${{ matrix.image }}-buildcache:${{ matrix.arch }},mode=max,image-manifest=true,ignore-error=true
            provenance: false
            sbom: false

        - name: Record digest
          env:
            DIGEST: ${{ steps.build.outputs.digest }}
          run: |
            mkdir -p /tmp/digests
            touch "/tmp/digests/${DIGEST#sha256:}"

        - uses: actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4.6.2
          with:
            name: digests-${{ matrix.image }}-${{ matrix.arch }}
            path: /tmp/digests/*
            if-no-files-found: error
            retention-days: 1

    merge:
      needs: build
      runs-on: arc-azrtydxb-publish
      timeout-minutes: 20
      strategy:
        fail-fast: false
        matrix:
          image: [nexora-engine, nexora-mgmt]
      steps:
        - uses: actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093 # v4
          with:
            pattern: digests-${{ matrix.image }}-*
            path: /tmp/digests
            merge-multiple: true

        - uses: docker/login-action@c94ce9fb468520275223c153574b00df6fe4bcc9 # v3
          with:
            registry: ${{ env.PUSH_REGISTRY }}
            username: ${{ secrets.NEXUS_USER }}
            password: ${{ secrets.NEXUS_PASSWORD }}

        - name: Merge digests into sha-<7> (and main)
          working-directory: /tmp/digests
          env:
            IMAGE: ${{ env.PUSH_REGISTRY }}/azrtydxb/${{ matrix.image }}
          run: |
            set -euo pipefail
            test "$(ls | wc -l)" -eq 2 || { echo "expected 2 digests, got: $(ls)"; exit 1; }
            tags=(-t "${IMAGE}:sha-${GITHUB_SHA::7}")
            if [ "${GITHUB_REF}" = "refs/heads/main" ]; then tags+=(-t "${IMAGE}:main"); fi
            docker buildx imagetools create "${tags[@]}" $(printf "${IMAGE}@sha256:%s " *)
            manifest=$(docker buildx imagetools inspect --raw "${IMAGE}:sha-${GITHUB_SHA::7}")
            echo "$manifest"
            echo "$manifest" | grep -q '"arm64"'
            echo "$manifest" | grep -q '"amd64"'
  ```
- [ ] Create `.github/actionlint.yaml`:
  ```yaml
  # Self-hosted ARC scale sets registered to the azrtydxb org; each carries one label.
  self-hosted-runner:
    labels:
      - arc-azrtydxb
      - arc-azrtydxb-publish
      - arc-azrtydxb-amd64
      - arc-azrtydxb-amd64-publish
  ```
- [ ] Run `scripts/dev-exec.sh 'go test ./deploy/deploytest/ -run TestImagesWorkflow -count=1 && go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/images.yml'` and expect `ok` and no actionlint output.
- [ ] Write the failing test `deploy/deploytest/compose_test.go`:
  ```go
  package deploytest

  import (
  	"os"
  	"strings"
  	"testing"

  	"gopkg.in/yaml.v3"
  )

  type composeService struct {
  	Image       string            `yaml:"image"`
  	Command     []string          `yaml:"command"`
  	Profiles    []string          `yaml:"profiles"`
  	Environment map[string]string `yaml:"environment"`
  	Ports       []string          `yaml:"ports"`
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
  		Volumes  map[string]any            `yaml:"volumes"`
  	}
  	if err := yaml.Unmarshal(raw, &c); err != nil {
  		t.Fatal(err)
  	}
  	for _, s := range []string{"postgres", "ca-init", "migrate", "mgmt", "engine", "otel-collector"} {
  		if _, ok := c.Services[s]; !ok {
  			t.Fatalf("service %s missing", s)
  		}
  	}
  	pg, mgmt, engine, otel := c.Services["postgres"], c.Services["mgmt"], c.Services["engine"], c.Services["otel-collector"]
  	if pg.Healthcheck == nil || !strings.Contains(pg.Environment["POSTGRES_PASSWORD"], ":?") {
  		t.Error("postgres needs a healthcheck and a required (no default) password")
  	}
  	if mgmt.DependsOn["migrate"].Condition != "service_completed_successfully" || mgmt.DependsOn["ca-init"].Condition != "service_completed_successfully" {
  		t.Errorf("mgmt depends_on = %+v", mgmt.DependsOn)
  	}
  	if !strings.Contains(mgmt.Image, "nexora-mgmt:") || !strings.Contains(engine.Image, "nexora-engine:") {
  		t.Errorf("images mgmt=%q engine=%q", mgmt.Image, engine.Image)
  	}
  	for _, k := range []string{"NEXORA_DATABASE_URL", "NEXORA_CA_CERT_FILE", "NEXORA_CA_KEY_FILE", "NEXORA_GRPC_SERVER_NAMES", "NEXORA_PUBLIC_URL"} {
  		if mgmt.Environment[k] == "" {
  			t.Errorf("mgmt environment lacks %s", k)
  		}
  	}
  	if len(engine.Profiles) != 1 || engine.Profiles[0] != "engine" || len(otel.Profiles) != 1 || otel.Profiles[0] != "otel" {
  		t.Errorf("profiles engine=%v otel=%v", engine.Profiles, otel.Profiles)
  	}
  	udp := false
  	for _, p := range engine.Ports {
  		udp = udp || strings.HasSuffix(p, ":53/udp")
  	}
  	if !udp {
  		t.Errorf("engine ports %v lack DNS over UDP", engine.Ports)
  	}
  	env, err := os.ReadFile("../compose/.env.example")
  	if err != nil || !strings.Contains(string(env), "POSTGRES_PASSWORD=") || !strings.Contains(string(env), "NEXORA_TAG=") {
  		t.Errorf(".env.example: %v %q", err, env)
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./deploy/deploytest/ -run TestComposeExample -count=1` and expect FAIL with `no such file or directory`.
- [ ] Create `deploy/compose/docker-compose.yml`:
  ```yaml
  # Nexora example: one management plane, one engine, PostgreSQL, optional
  # OpenTelemetry Collector. See docs/operations.md "Docker Compose".
  #   cp .env.example .env && $EDITOR .env
  #   docker compose up -d
  #   docker compose exec mgmt nexora-mgmt join-token create --group default --ttl 1h > secrets/join-token
  #   docker compose --profile engine up -d
  name: nexora

  services:
    postgres:
      image: postgres:17
      restart: unless-stopped
      environment:
        POSTGRES_USER: nexora
        POSTGRES_DB: nexora
        POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}
      volumes:
        - pgdata:/var/lib/postgresql/data
      healthcheck:
        test: ["CMD", "pg_isready", "-U", "nexora", "-d", "nexora"]
        interval: 5s
        timeout: 3s
        retries: 20

    ca-init:
      image: ${NEXORA_REGISTRY:-192.168.10.131/azrtydxb}/nexora-mgmt:${NEXORA_TAG:?set NEXORA_TAG in .env}
      command: ["ca", "init", "--out", "/var/lib/nexora-ca", "--if-missing"]
      volumes:
        - ca:/var/lib/nexora-ca
      restart: "no"

    migrate:
      image: ${NEXORA_REGISTRY:-192.168.10.131/azrtydxb}/nexora-mgmt:${NEXORA_TAG:?set NEXORA_TAG in .env}
      command: ["migrate"]
      environment:
        NEXORA_DATABASE_URL: postgres://nexora:${POSTGRES_PASSWORD}@postgres:5432/nexora?sslmode=disable
      depends_on:
        postgres: { condition: service_healthy }
      restart: "no"

    mgmt:
      image: ${NEXORA_REGISTRY:-192.168.10.131/azrtydxb}/nexora-mgmt:${NEXORA_TAG:?set NEXORA_TAG in .env}
      command: ["serve"]
      restart: unless-stopped
      environment:
        NEXORA_DATABASE_URL: postgres://nexora:${POSTGRES_PASSWORD}@postgres:5432/nexora?sslmode=disable
        NEXORA_CA_CERT_FILE: /var/lib/nexora-ca/ca.crt
        NEXORA_CA_KEY_FILE: /var/lib/nexora-ca/ca.key
        NEXORA_GRPC_SERVER_NAMES: mgmt,localhost,${NEXORA_PUBLIC_HOST:-localhost}
        NEXORA_PUBLIC_URL: ${NEXORA_PUBLIC_URL:-http://localhost:8080}
        NEXORA_SECURE_COOKIES: ${NEXORA_SECURE_COOKIES:-false}
        NEXORA_QUERYLOG_BACKEND: builtin
        NEXORA_OTLP_ENDPOINT: ${NEXORA_OTLP_ENDPOINT:-}
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
      cap_drop: [ALL]
      cap_add: [NET_BIND_SERVICE]
      read_only: true
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
      logs: { receivers: [otlp], processors: [memory_limiter, batch], exporters: [debug] }
      traces: { receivers: [otlp], processors: [memory_limiter, batch], exporters: [debug] }
      metrics: { receivers: [otlp], processors: [memory_limiter, batch], exporters: [debug] }
  ```
- [ ] Create `deploy/compose/.env.example`:
  ```dotenv
  # Copy to .env. No defaults for secrets.
  POSTGRES_PASSWORD=
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
  and `deploy/compose/secrets/.gitignore` containing `*` and `!.gitignore` so the directory exists without committing tokens.
- [ ] Run `scripts/dev-exec.sh go test ./deploy/deploytest/ -count=1` and expect `ok`.
- [ ] Commit: `git add .github/workflows/images.yml .github/actionlint.yaml deploy/compose deploy/deploytest deploy/docker && git commit -m "build: multi-arch release images and docker-compose example"`.

## Task 13: Helm chart finalisation

Files: `deploy/helm/nexora/Chart.yaml`, `deploy/helm/nexora/values.yaml`, `deploy/helm/nexora/values.schema.json`, `deploy/helm/nexora/templates/_helpers.tpl`, `templates/mgmt-deployment.yaml`, `templates/mgmt-services.yaml` (http ClusterIP + gRPC LoadBalancer), `templates/mgmt-ingress.yaml`, `templates/mgmt-pdb.yaml`, `templates/engine-configmap.yaml`, `templates/engine-workload.yaml` (per group Deployment or DaemonSet), `templates/engine-services.yaml` (per group DNS service + metrics headless service), `templates/database-cnpg.yaml`, `templates/otel-collector.yaml`, `templates/servicemonitor.yaml`, `templates/prometheusrule.yaml`, `templates/NOTES.txt`, `deploy/helm/nexora/ci/lint-values.yaml`, `deploy/kw/values-kw.yaml` (kw release values), `deploy/deploytest/helm_test.go` (`TestHelmTemplate`)
Interfaces: resource names for release `R`: `F` = `R` when `R` contains `nexora`, else `R-nexora`; `F-mgmt` (Deployment, http Service), `F-mgmt-grpc` (Service), `F-engine-<group>` (ConfigMap, workload, DNS Service), `F-engine-metrics` (headless Service), `F-otel-collector`, `F` (ServiceMonitor, PrometheusRule); CNPG `Cluster` named `database.cnpg.clusterName`; pod labels `app.kubernetes.io/component` (`mgmt`, `engine`, `otel-collector`) and `nexora.io/group`.

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

  	"gopkg.in/yaml.v3"
  )

  const chartDir = "../helm/nexora"

  func helm(args ...string) (string, error) {
  	out, err := exec.Command("helm", args...).CombinedOutput()
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
  		var o obj
  		if err := dec.Decode(&o); errors.Is(err, io.EOF) {
  			break
  		} else if err != nil {
  			t.Fatalf("decode: %v", err)
  		}
  		if o != nil {
  			docs = append(docs, o)
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

  func env(c obj, name string) map[string]any {
  	for _, e := range c["env"].([]any) {
  		if m := e.(map[string]any); m["name"] == name {
  			return m
  		}
  	}
  	return nil
  }

  func TestHelmTemplate(t *testing.T) {
  	if out, err := helm("lint", chartDir, "--strict", "-f", chartDir+"/ci/lint-values.yaml"); err != nil {
  		t.Fatalf("helm lint: %v\n%s", err, out)
  	}

  	kw := render(t, "-f", "../kw/values-kw.yaml", "--api-versions", "postgresql.cnpg.io/v1", "--api-versions", "monitoring.coreos.com/v1")
  	mgmt := find(t, kw, "Deployment", "nexora-mgmt")
  	if mgmt.path("spec", "replicas") != 2 {
  		t.Errorf("mgmt replicas = %v", mgmt.path("spec", "replicas"))
  	}
  	db := env(container(t, mgmt, "mgmt"), "NEXORA_DATABASE_URL")
  	if ref := obj(db).path("valueFrom", "secretKeyRef"); ref == nil || ref.(map[string]any)["name"] != "nexora-db-app" || ref.(map[string]any)["key"] != "uri" {
  		t.Errorf("database env = %v", db)
  	}
  	init := mgmt.path("spec", "template", "spec", "initContainers").([]any)[0].(map[string]any)
  	if args := init["args"].([]any); len(args) != 1 || args[0] != "migrate" {
  		t.Errorf("migrate init container args = %v", init["args"])
  	}
  	grpc := find(t, kw, "Service", "nexora-mgmt-grpc")
  	if grpc.path("spec", "type") != "LoadBalancer" || grpc.path("spec", "loadBalancerIP") != "192.168.10.135" {
  		t.Errorf("grpc service spec = %v", grpc["spec"])
  	}
  	for group, want := range map[string]struct {
  		ip       string
  		replicas int
  	}{"edge-a": {"192.168.10.136", 2}, "edge-b": {"192.168.10.137", 1}} {
  		w := find(t, kw, "Deployment", "nexora-engine-"+group)
  		if w.path("spec", "replicas") != want.replicas {
  			t.Errorf("%s replicas = %v", group, w.path("spec", "replicas"))
  		}
  		anti := w.path("spec", "template", "spec", "affinity", "podAntiAffinity", "requiredDuringSchedulingIgnoredDuringExecution")
  		if anti == nil || anti.([]any)[0].(map[string]any)["topologyKey"] != "kubernetes.io/hostname" {
  			t.Errorf("%s lacks required host anti-affinity: %v", group, anti)
  		}
  		if nn := env(container(t, w, "engine"), "NEXORA_ENGINE_NODE_NAME"); nn == nil || nn["value"] != group+"-$(K8S_NODE_NAME)" {
  			t.Errorf("%s node name env = %v", group, nn)
  		}
  		svc := find(t, kw, "Service", "nexora-engine-"+group)
  		if svc.path("spec", "loadBalancerIP") != want.ip {
  			t.Errorf("%s service IP = %v", group, svc.path("spec", "loadBalancerIP"))
  		}
  	}
  	cluster := find(t, kw, "Cluster", "nexora-db")
  	if cluster.path("spec", "instances") != 2 || cluster.path("spec", "storage", "storageClass") != "longhorn-single" {
  		t.Errorf("cnpg cluster spec = %v", cluster["spec"])
  	}
  	sm := find(t, kw, "ServiceMonitor", "nexora")
  	if sm.path("metadata", "namespace") != "monitoring" || sm.path("metadata", "labels", "release") != "kps" {
  		t.Errorf("service monitor metadata = %v", sm["metadata"])
  	}
  	rule, _ := yaml.Marshal(find(t, kw, "PrometheusRule", "nexora"))
  	if !strings.Contains(string(rule), "max(nexora_mgmt_engines_disconnected") {
  		t.Errorf("prometheus rule lacks the disconnected alert:\n%s", rule)
  	}
  	otel, _ := yaml.Marshal(find(t, kw, "ConfigMap", "nexora-otel-collector"))
  	if !strings.Contains(string(otel), "jaeger.observability:4317") {
  		t.Errorf("collector config lacks jaeger exporter:\n%s", otel)
  	}
  	ing := find(t, kw, "Ingress", "nexora-mgmt")
  	if ing.path("spec", "ingressClassName") != "nginx" || ing.path("metadata", "annotations", "cert-manager.io/cluster-issuer") != "cluster-ca" {
  		t.Errorf("ingress = %v", ing)
  	}

  	ds := render(t, "--set", "database.mode=external", "--set", "database.external.existingSecret=pg",
  		"--set", "mgmt.ca.existingSecret=ca", "--set", "engine.kind=DaemonSet", "--set", "engine.hostNetwork=true",
  		"--set-json", `engine.groups=[{"name":"default","joinTokenSecret":"jt"}]`)
  	d := find(t, ds, "DaemonSet", "nexora-engine-default")
  	if d.path("spec", "template", "spec", "hostNetwork") != true || d.path("spec", "template", "spec", "dnsPolicy") != "ClusterFirstWithHostNet" {
  		t.Errorf("daemonset host network = %v", d.path("spec", "template", "spec"))
  	}
  	if has(ds, "Cluster") || has(ds, "ServiceMonitor") {
  		t.Error("external database or disabled monitoring must not render CNPG or ServiceMonitor objects")
  	}
  	ref := obj(env(container(t, find(t, ds, "Deployment", "nexora-mgmt"), "mgmt"), "NEXORA_DATABASE_URL")).path("valueFrom", "secretKeyRef").(map[string]any)
  	if ref["name"] != "pg" || ref["key"] != "url" {
  		t.Errorf("external database secret ref = %v", ref)
  	}

  	for _, bad := range []struct {
  		args []string
  		want string
  	}{
  		{[]string{"--set", "mgmt.ca.existingSecret=ca", "--set", "database.mode=external", "--set", "database.external.existingSecret=pg", "--set", "mgmt.replicas=0"}, "replicas"},
  		{[]string{"--set", "database.mode=external", "--set", "database.external.existingSecret=pg"}, "existingSecret"},
  		{[]string{"--set", "mgmt.ca.existingSecret=ca", "--set", "database.mode=external", "--set", "database.external.existingSecret=pg", "--set", "engine.kind=StatefulSet"}, "kind"},
  		{[]string{"--set", "mgmt.ca.existingSecret=ca", "--set-json", `engine.groups=[{"name":"default","joinTokenSecret":"jt"}]`}, "postgresql.cnpg.io/v1"},
  	} {
  		out, err := helm(append([]string{"template", "nexora", chartDir}, bad.args...)...)
  		if err == nil || !strings.Contains(out, bad.want) {
  			t.Errorf("helm template %v: err=%v, want failure mentioning %q; output:\n%s", bad.args, err, bad.want, out)
  		}
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./deploy/deploytest/ -run TestHelmTemplate -count=1` and expect FAIL with `helm lint:` (the chart does not exist yet or is the M1 skeleton).
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
    nexora.io/requires: "CloudNativePG operator (postgresql.cnpg.io/v1) when database.mode=cnpg; Prometheus Operator CRDs when metrics.* are enabled"
  ```
- [ ] Create `deploy/helm/nexora/values.yaml`:
  ```yaml
  image:
    registry: 192.168.10.131
    repository: azrtydxb
    tag: ""            # defaults to .Chart.AppVersion
    pullPolicy: IfNotPresent
  imagePullSecrets: []

  mgmt:
    replicas: 2
    publicURL: ""
    secureCookies: true
    engineCertTTL: 2160h
    grpcServerNames: []   # extra SANs; the gRPC service DNS names and loadBalancerIP are always included
    ca:
      existingSecret: ""  # required: secret with ca.crt and ca.key (nexora-mgmt ca init)
    querylog:
      backend: builtin    # builtin | opensearch
      builtinCapacity: 200000
      opensearch:
        url: ""
        index: nexora-querylog-*
        username: ""
        passwordSecret: ""  # secret with key "password"
    extraEnv: []
    extraVolumes: []
    extraVolumeMounts: []
    resources:
      requests: { cpu: 100m, memory: 128Mi }
      limits: { memory: 512Mi }
    service:
      httpPort: 8080
    grpcService:
      type: LoadBalancer
      port: 9443
      loadBalancerIP: ""
      annotations: {}
    ingress:
      enabled: false
      className: nginx
      host: ""
      clusterIssuer: ""
      tlsSecretName: ""
    pdb:
      minAvailable: 1

  database:
    mode: cnpg            # cnpg | external
    external:
      existingSecret: ""  # secret holding the PostgreSQL URL
      key: url
    cnpg:
      clusterName: nexora-db
      instances: 2
      imageName: ghcr.io/cloudnative-pg/postgresql:17.6
      storageClass: ""
      size: 10Gi
      podMonitor: false

  engine:
    enabled: true
    kind: Deployment      # Deployment | DaemonSet
    hostNetwork: false
    managementURL: ""     # default https://<fullname>-mgmt-grpc.<namespace>.svc:9443
    workers: 0
    listenAddresses: ["0.0.0.0"]
    ports:
      dns: 53
      metrics: 9153
      dot: 0              # 0 disables the port
      doh: 0
      doq: 0
    extraToml: ""         # appended to engine.toml verbatim
    stateDir:
      type: hostPath      # hostPath | emptyDir (emptyDir re-enrolls after every pod restart)
      hostPathPrefix: /var/lib/nexora
    spreadAcrossNodes: true
    nodeAffinity: {}
    tolerations: []
    resources:
      requests: { cpu: 250m, memory: 256Mi }
      limits: { memory: 2Gi }
    groups:
      - name: default
        replicas: 1
        joinTokenSecret: ""   # secret with key "token" (nexora-mgmt join-token create)
        nodeSelector: {}
        service:
          type: LoadBalancer
          loadBalancerIP: ""
          externalTrafficPolicy: Local
          annotations: {}

  otelCollector:
    enabled: false
    image: otel/opentelemetry-collector-contrib:0.160.0
    traces:
      otlpEndpoint: ""    # e.g. jaeger.observability:4317
      insecure: true
    logs:
      opensearch:
        url: ""
    resources:
      requests: { cpu: 50m, memory: 128Mi }
      limits: { memory: 512Mi }

  metrics:
    serviceMonitor:
      enabled: false
      namespace: ""
      labels: {}
      interval: 30s
    prometheusRule:
      enabled: false
      namespace: ""
      labels: {}
  ```
- [ ] Create `deploy/helm/nexora/values.schema.json`:
  ```json
  {
    "$schema": "https://json-schema.org/draft-07/schema#",
    "type": "object",
    "required": ["image", "mgmt", "database", "engine"],
    "properties": {
      "image": {
        "type": "object",
        "required": ["registry", "repository"],
        "properties": {
          "registry": { "type": "string", "minLength": 1 },
          "repository": { "type": "string", "minLength": 1 },
          "tag": { "type": "string" },
          "pullPolicy": { "enum": ["Always", "IfNotPresent", "Never"] }
        }
      },
      "imagePullSecrets": { "type": "array" },
      "mgmt": {
        "type": "object",
        "required": ["replicas", "ca"],
        "properties": {
          "replicas": { "type": "integer", "minimum": 1 },
          "publicURL": { "type": "string" },
          "secureCookies": { "type": "boolean" },
          "engineCertTTL": { "type": "string", "pattern": "^([0-9]+(h|m|s))+$" },
          "grpcServerNames": { "type": "array", "items": { "type": "string" } },
          "ca": {
            "type": "object",
            "required": ["existingSecret"],
            "properties": { "existingSecret": { "type": "string", "minLength": 1 } }
          },
          "querylog": {
            "type": "object",
            "properties": {
              "backend": { "enum": ["builtin", "opensearch"] },
              "builtinCapacity": { "type": "integer", "minimum": 1000 },
              "opensearch": { "type": "object" }
            }
          },
          "grpcService": {
            "type": "object",
            "properties": {
              "type": { "enum": ["ClusterIP", "NodePort", "LoadBalancer"] },
              "port": { "type": "integer", "minimum": 1, "maximum": 65535 },
              "loadBalancerIP": { "type": "string" }
            }
          },
          "ingress": { "type": "object" },
          "pdb": { "type": "object" }
        }
      },
      "database": {
        "type": "object",
        "required": ["mode"],
        "properties": {
          "mode": { "enum": ["cnpg", "external"] },
          "external": { "type": "object", "properties": { "existingSecret": { "type": "string" }, "key": { "type": "string", "minLength": 1 } } },
          "cnpg": {
            "type": "object",
            "properties": {
              "clusterName": { "type": "string", "pattern": "^[a-z0-9]([a-z0-9-]*[a-z0-9])?$" },
              "instances": { "type": "integer", "minimum": 1 },
              "size": { "type": "string" },
              "storageClass": { "type": "string" }
            }
          }
        },
        "if": { "properties": { "mode": { "const": "external" } } },
        "then": { "properties": { "external": { "required": ["existingSecret"], "properties": { "existingSecret": { "minLength": 1 } } } } }
      },
      "engine": {
        "type": "object",
        "required": ["kind", "groups"],
        "properties": {
          "enabled": { "type": "boolean" },
          "kind": { "enum": ["Deployment", "DaemonSet"] },
          "hostNetwork": { "type": "boolean" },
          "workers": { "type": "integer", "minimum": 0 },
          "listenAddresses": { "type": "array", "minItems": 1, "items": { "type": "string" } },
          "ports": {
            "type": "object",
            "properties": {
              "dns": { "type": "integer", "minimum": 1, "maximum": 65535 },
              "metrics": { "type": "integer", "minimum": 1, "maximum": 65535 },
              "dot": { "type": "integer", "minimum": 0, "maximum": 65535 },
              "doh": { "type": "integer", "minimum": 0, "maximum": 65535 },
              "doq": { "type": "integer", "minimum": 0, "maximum": 65535 }
            }
          },
          "stateDir": { "type": "object", "properties": { "type": { "enum": ["hostPath", "emptyDir"] }, "hostPathPrefix": { "type": "string", "pattern": "^/" } } },
          "spreadAcrossNodes": { "type": "boolean" },
          "groups": {
            "type": "array",
            "minItems": 1,
            "items": {
              "type": "object",
              "required": ["name"],
              "properties": {
                "name": { "type": "string", "pattern": "^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$" },
                "replicas": { "type": "integer", "minimum": 0 },
                "joinTokenSecret": { "type": "string" },
                "nodeSelector": { "type": "object" },
                "service": { "type": "object" }
              }
            }
          }
        }
      },
      "otelCollector": { "type": "object" },
      "metrics": { "type": "object" }
    }
  }
  ```
- [ ] Create `deploy/helm/nexora/templates/_helpers.tpl`:
  ```yaml
  {{- define "nexora.fullname" -}}
  {{- if contains "nexora" .Release.Name -}}{{ .Release.Name | trunc 50 | trimSuffix "-" }}{{- else -}}{{ printf "%s-nexora" .Release.Name | trunc 50 | trimSuffix "-" }}{{- end -}}
  {{- end -}}

  {{- define "nexora.selectorLabels" -}}
  app.kubernetes.io/name: nexora
  app.kubernetes.io/instance: {{ .root.Release.Name }}
  app.kubernetes.io/component: {{ .component }}
  {{- end -}}

  {{- define "nexora.labels" -}}
  {{ include "nexora.selectorLabels" . }}
  app.kubernetes.io/managed-by: {{ .root.Release.Service }}
  app.kubernetes.io/version: {{ default .root.Chart.AppVersion .root.Values.image.tag | quote }}
  helm.sh/chart: {{ printf "%s-%s" .root.Chart.Name .root.Chart.Version }}
  {{- end -}}

  {{- define "nexora.image" -}}
  {{ printf "%s/%s/%s:%s" .root.Values.image.registry .root.Values.image.repository .name (default .root.Chart.AppVersion .root.Values.image.tag) }}
  {{- end -}}

  {{- define "nexora.containerSecurity" -}}
  allowPrivilegeEscalation: false
  readOnlyRootFilesystem: true
  capabilities:
    drop: [ALL]
  {{- end -}}

  {{- define "nexora.databaseEnv" -}}
  - name: NEXORA_DATABASE_URL
    valueFrom:
      secretKeyRef:
        {{- if eq .Values.database.mode "cnpg" }}
        name: {{ .Values.database.cnpg.clusterName }}-app
        key: uri
        {{- else }}
        name: {{ required "database.external.existingSecret is required when database.mode=external" .Values.database.external.existingSecret }}
        key: {{ .Values.database.external.key }}
        {{- end }}
  {{- end -}}

  {{- define "nexora.managementURL" -}}
  {{- default (printf "https://%s-mgmt-grpc.%s.svc:%d" (include "nexora.fullname" .) .Release.Namespace (int .Values.mgmt.grpcService.port)) .Values.engine.managementURL -}}
  {{- end -}}

  {{- define "nexora.grpcServerNames" -}}
  {{- $f := include "nexora.fullname" . -}}
  {{- $names := list (printf "%s-mgmt-grpc" $f) (printf "%s-mgmt-grpc.%s.svc" $f .Release.Namespace) (printf "%s-mgmt-grpc.%s.svc.cluster.local" $f .Release.Namespace) -}}
  {{- with .Values.mgmt.grpcService.loadBalancerIP }}{{ $names = append $names . }}{{ end -}}
  {{- join "," (concat $names .Values.mgmt.grpcServerNames) -}}
  {{- end -}}
  ```
- [ ] Create `deploy/helm/nexora/templates/mgmt-deployment.yaml`:
  ```yaml
  {{- if eq .Values.database.mode "cnpg" }}
  {{- if not (.Capabilities.APIVersions.Has "postgresql.cnpg.io/v1") }}
  {{- fail "database.mode=cnpg needs the CloudNativePG operator (API postgresql.cnpg.io/v1); install it or set database.mode=external" }}
  {{- end }}
  {{- end }}
  {{- $f := include "nexora.fullname" . }}
  apiVersion: apps/v1
  kind: Deployment
  metadata:
    name: {{ $f }}-mgmt
    labels: {{- include "nexora.labels" (dict "root" . "component" "mgmt") | nindent 4 }}
  spec:
    replicas: {{ .Values.mgmt.replicas }}
    selector:
      matchLabels: {{- include "nexora.selectorLabels" (dict "root" . "component" "mgmt") | nindent 6 }}
    template:
      metadata:
        labels: {{- include "nexora.selectorLabels" (dict "root" . "component" "mgmt") | nindent 8 }}
      spec:
        {{- with .Values.imagePullSecrets }}
        imagePullSecrets: {{- toYaml . | nindent 8 }}
        {{- end }}
        securityContext:
          runAsNonRoot: true
          runAsUser: 65532
          runAsGroup: 65532
          seccompProfile: { type: RuntimeDefault }
        affinity:
          podAntiAffinity:
            preferredDuringSchedulingIgnoredDuringExecution:
              - weight: 100
                podAffinityTerm:
                  topologyKey: kubernetes.io/hostname
                  labelSelector:
                    matchLabels: {{- include "nexora.selectorLabels" (dict "root" . "component" "mgmt") | nindent 20 }}
        initContainers:
          - name: migrate
            image: {{ include "nexora.image" (dict "root" . "name" "nexora-mgmt") }}
            imagePullPolicy: {{ .Values.image.pullPolicy }}
            args: ["migrate"]
            env: {{- include "nexora.databaseEnv" . | nindent 12 }}
            securityContext: {{- include "nexora.containerSecurity" . | nindent 12 }}
        containers:
          - name: mgmt
            image: {{ include "nexora.image" (dict "root" . "name" "nexora-mgmt") }}
            imagePullPolicy: {{ .Values.image.pullPolicy }}
            args: ["serve"]
            ports:
              - { name: http, containerPort: 8080, protocol: TCP }
              - { name: grpc, containerPort: 9443, protocol: TCP }
            env:
              {{- include "nexora.databaseEnv" . | nindent 14 }}
              - name: NEXORA_INSTANCE_ID
                valueFrom: { fieldRef: { fieldPath: metadata.name } }
              - { name: NEXORA_CA_CERT_FILE, value: /etc/nexora/ca/ca.crt }
              - { name: NEXORA_CA_KEY_FILE, value: /etc/nexora/ca/ca.key }
              - { name: NEXORA_GRPC_SERVER_NAMES, value: {{ include "nexora.grpcServerNames" . | quote }} }
              - { name: NEXORA_PUBLIC_URL, value: {{ .Values.mgmt.publicURL | quote }} }
              - { name: NEXORA_SECURE_COOKIES, value: {{ .Values.mgmt.secureCookies | quote }} }
              - { name: NEXORA_ENGINE_CERT_TTL, value: {{ .Values.mgmt.engineCertTTL | quote }} }
              - { name: NEXORA_QUERYLOG_BACKEND, value: {{ .Values.mgmt.querylog.backend | quote }} }
              - { name: NEXORA_QUERYLOG_BUILTIN_CAPACITY, value: {{ .Values.mgmt.querylog.builtinCapacity | quote }} }
              {{- if eq .Values.mgmt.querylog.backend "opensearch" }}
              - { name: NEXORA_OPENSEARCH_URL, value: {{ required "mgmt.querylog.opensearch.url is required" .Values.mgmt.querylog.opensearch.url | quote }} }
              - { name: NEXORA_OPENSEARCH_INDEX, value: {{ .Values.mgmt.querylog.opensearch.index | quote }} }
              {{- with .Values.mgmt.querylog.opensearch.username }}
              - { name: NEXORA_OPENSEARCH_USERNAME, value: {{ . | quote }} }
              {{- end }}
              {{- if .Values.mgmt.querylog.opensearch.passwordSecret }}
              - { name: NEXORA_OPENSEARCH_PASSWORD_FILE, value: /etc/nexora/opensearch/password }
              {{- end }}
              {{- end }}
              {{- if .Values.otelCollector.enabled }}
              - { name: NEXORA_OTLP_ENDPOINT, value: {{ printf "http://%s-otel-collector:4317" $f | quote }} }
              {{- end }}
              {{- with .Values.mgmt.extraEnv }}
              {{- toYaml . | nindent 14 }}
              {{- end }}
            readinessProbe:
              httpGet: { path: /healthz, port: http }
              periodSeconds: 5
            livenessProbe:
              httpGet: { path: /healthz, port: http }
              periodSeconds: 20
              failureThreshold: 6
            resources: {{- toYaml .Values.mgmt.resources | nindent 14 }}
            securityContext: {{- include "nexora.containerSecurity" . | nindent 14 }}
            volumeMounts:
              - { name: ca, mountPath: /etc/nexora/ca, readOnly: true }
              - { name: tmp, mountPath: /tmp }
              {{- if and (eq .Values.mgmt.querylog.backend "opensearch") .Values.mgmt.querylog.opensearch.passwordSecret }}
              - { name: opensearch, mountPath: /etc/nexora/opensearch, readOnly: true }
              {{- end }}
              {{- with .Values.mgmt.extraVolumeMounts }}
              {{- toYaml . | nindent 14 }}
              {{- end }}
        volumes:
          - name: ca
            secret:
              secretName: {{ required "mgmt.ca.existingSecret is required (create it with nexora-mgmt ca init)" .Values.mgmt.ca.existingSecret }}
              defaultMode: 0440
          - name: tmp
            emptyDir: {}
          {{- if and (eq .Values.mgmt.querylog.backend "opensearch") .Values.mgmt.querylog.opensearch.passwordSecret }}
          - name: opensearch
            secret: { secretName: {{ .Values.mgmt.querylog.opensearch.passwordSecret }}, defaultMode: 0440 }
          {{- end }}
          {{- with .Values.mgmt.extraVolumes }}
          {{- toYaml . | nindent 10 }}
          {{- end }}
  ```
  The M1 management plane serves its readiness endpoint at `/healthz`; use M1's path if it differs.
- [ ] Create `deploy/helm/nexora/templates/mgmt-services.yaml`, `mgmt-ingress.yaml`, `mgmt-pdb.yaml`:
  ```yaml
  {{- $f := include "nexora.fullname" . }}
  apiVersion: v1
  kind: Service
  metadata:
    name: {{ $f }}-mgmt
    labels:
      {{- include "nexora.labels" (dict "root" . "component" "mgmt") | nindent 4 }}
      nexora.io/metrics: "true"
  spec:
    type: ClusterIP
    selector: {{- include "nexora.selectorLabels" (dict "root" . "component" "mgmt") | nindent 4 }}
    ports:
      - { name: http, port: {{ .Values.mgmt.service.httpPort }}, targetPort: http, protocol: TCP }
  ---
  apiVersion: v1
  kind: Service
  metadata:
    name: {{ $f }}-mgmt-grpc
    labels: {{- include "nexora.labels" (dict "root" . "component" "mgmt") | nindent 4 }}
    {{- with .Values.mgmt.grpcService.annotations }}
    annotations: {{- toYaml . | nindent 4 }}
    {{- end }}
  spec:
    type: {{ .Values.mgmt.grpcService.type }}
    {{- with .Values.mgmt.grpcService.loadBalancerIP }}
    loadBalancerIP: {{ . }}
    {{- end }}
    selector: {{- include "nexora.selectorLabels" (dict "root" . "component" "mgmt") | nindent 4 }}
    ports:
      - { name: grpc, port: {{ .Values.mgmt.grpcService.port }}, targetPort: grpc, protocol: TCP }
  ```
  ```yaml
  {{- if .Values.mgmt.ingress.enabled }}
  apiVersion: networking.k8s.io/v1
  kind: Ingress
  metadata:
    name: {{ include "nexora.fullname" . }}-mgmt
    labels: {{- include "nexora.labels" (dict "root" . "component" "mgmt") | nindent 4 }}
    {{- with .Values.mgmt.ingress.clusterIssuer }}
    annotations:
      cert-manager.io/cluster-issuer: {{ . }}
    {{- end }}
  spec:
    ingressClassName: {{ .Values.mgmt.ingress.className }}
    tls:
      - hosts: [{{ required "mgmt.ingress.host is required" .Values.mgmt.ingress.host | quote }}]
        secretName: {{ default (printf "%s-mgmt-tls" (include "nexora.fullname" .)) .Values.mgmt.ingress.tlsSecretName }}
    rules:
      - host: {{ .Values.mgmt.ingress.host | quote }}
        http:
          paths:
            - path: /
              pathType: Prefix
              backend:
                service:
                  name: {{ include "nexora.fullname" . }}-mgmt
                  port: { name: http }
  {{- end }}
  ```
  ```yaml
  {{- if gt (int .Values.mgmt.replicas) 1 }}
  apiVersion: policy/v1
  kind: PodDisruptionBudget
  metadata:
    name: {{ include "nexora.fullname" . }}-mgmt
    labels: {{- include "nexora.labels" (dict "root" . "component" "mgmt") | nindent 4 }}
  spec:
    minAvailable: {{ .Values.mgmt.pdb.minAvailable }}
    selector:
      matchLabels: {{- include "nexora.selectorLabels" (dict "root" . "component" "mgmt") | nindent 6 }}
  {{- end }}
  ```
- [ ] Create `deploy/helm/nexora/templates/engine-configmap.yaml` and `engine-workload.yaml`:
  ```yaml
  {{- if .Values.engine.enabled }}
  {{- range $g := .Values.engine.groups }}
  ---
  apiVersion: v1
  kind: ConfigMap
  metadata:
    name: {{ include "nexora.fullname" $ }}-engine-{{ $g.name }}
    labels: {{- include "nexora.labels" (dict "root" $ "component" "engine") | nindent 4 }}
  data:
    engine.toml: |
      node_name = "{{ $g.name }}"
      state_dir = "/var/lib/nexora"
      management_urls = [{{ include "nexora.managementURL" $ | quote }}]
      join_token_file = "/etc/nexora/join/token"
      listen_udp = [{{ range $i, $a := $.Values.engine.listenAddresses }}{{ if $i }}, {{ end }}"{{ $a }}:{{ $.Values.engine.ports.dns }}"{{ end }}]
      listen_tcp = [{{ range $i, $a := $.Values.engine.listenAddresses }}{{ if $i }}, {{ end }}"{{ $a }}:{{ $.Values.engine.ports.dns }}"{{ end }}]
      metrics_listen = "0.0.0.0:{{ $.Values.engine.ports.metrics }}"
      workers = {{ $.Values.engine.workers }}
      {{- with $.Values.engine.extraToml }}
      {{- . | nindent 4 }}
      {{- end }}
  {{- end }}
  {{- end }}
  ```
  ```yaml
  {{- if .Values.engine.enabled }}
  {{- range $g := .Values.engine.groups }}
  {{- $name := printf "%s-engine-%s" (include "nexora.fullname" $) $g.name }}
  ---
  apiVersion: apps/v1
  kind: {{ $.Values.engine.kind }}
  metadata:
    name: {{ $name }}
    labels:
      {{- include "nexora.labels" (dict "root" $ "component" "engine") | nindent 4 }}
      nexora.io/group: {{ $g.name }}
  spec:
    {{- if eq $.Values.engine.kind "Deployment" }}
    replicas: {{ $g.replicas | default 1 }}
    strategy:
      type: RollingUpdate
      rollingUpdate: { maxUnavailable: 1, maxSurge: 0 }
    {{- else }}
    updateStrategy:
      type: RollingUpdate
      rollingUpdate: { maxUnavailable: 1 }
    {{- end }}
    selector:
      matchLabels:
        {{- include "nexora.selectorLabels" (dict "root" $ "component" "engine") | nindent 6 }}
        nexora.io/group: {{ $g.name }}
    template:
      metadata:
        labels:
          {{- include "nexora.selectorLabels" (dict "root" $ "component" "engine") | nindent 8 }}
          nexora.io/group: {{ $g.name }}
        annotations:
          checksum/config: {{ include (print $.Template.BasePath "/engine-configmap.yaml") $ | sha256sum }}
      spec:
        {{- with $.Values.imagePullSecrets }}
        imagePullSecrets: {{- toYaml . | nindent 8 }}
        {{- end }}
        {{- if $.Values.engine.hostNetwork }}
        hostNetwork: true
        dnsPolicy: ClusterFirstWithHostNet
        {{- end }}
        {{- with $g.nodeSelector }}
        nodeSelector: {{- toYaml . | nindent 8 }}
        {{- end }}
        {{- with $.Values.engine.tolerations }}
        tolerations: {{- toYaml . | nindent 8 }}
        {{- end }}
        affinity:
          {{- with $.Values.engine.nodeAffinity }}
          nodeAffinity: {{- toYaml . | nindent 10 }}
          {{- end }}
          {{- if $.Values.engine.spreadAcrossNodes }}
          podAntiAffinity:
            requiredDuringSchedulingIgnoredDuringExecution:
              - topologyKey: kubernetes.io/hostname
                labelSelector:
                  matchLabels:
                    app.kubernetes.io/instance: {{ $.Release.Name }}
                    app.kubernetes.io/component: engine
          {{- end }}
        securityContext:
          {{- if eq $.Values.engine.stateDir.type "hostPath" }}
          runAsUser: 0
          {{- else }}
          runAsNonRoot: true
          runAsUser: 65532
          runAsGroup: 65532
          fsGroup: 65532
          {{- end }}
          seccompProfile: { type: RuntimeDefault }
        containers:
          - name: engine
            image: {{ include "nexora.image" (dict "root" $ "name" "nexora-engine") }}
            imagePullPolicy: {{ $.Values.image.pullPolicy }}
            args: ["--config", "/etc/nexora/engine.toml"]
            env:
              - name: K8S_NODE_NAME
                valueFrom: { fieldRef: { fieldPath: spec.nodeName } }
              - name: NEXORA_ENGINE_NODE_NAME
                value: "{{ $g.name }}-$(K8S_NODE_NAME)"
            ports:
              - { name: dns-udp, containerPort: {{ $.Values.engine.ports.dns }}, protocol: UDP }
              - { name: dns-tcp, containerPort: {{ $.Values.engine.ports.dns }}, protocol: TCP }
              - { name: metrics, containerPort: {{ $.Values.engine.ports.metrics }}, protocol: TCP }
              {{- with $.Values.engine.ports.dot }}
              - { name: dot, containerPort: {{ . }}, protocol: TCP }
              {{- end }}
              {{- with $.Values.engine.ports.doh }}
              - { name: doh, containerPort: {{ . }}, protocol: TCP }
              {{- end }}
              {{- with $.Values.engine.ports.doq }}
              - { name: doq, containerPort: {{ . }}, protocol: UDP }
              {{- end }}
            readinessProbe:
              tcpSocket: { port: dns-tcp }
              periodSeconds: 5
            resources: {{- toYaml $.Values.engine.resources | nindent 14 }}
            securityContext:
              allowPrivilegeEscalation: false
              readOnlyRootFilesystem: true
              capabilities:
                drop: [ALL]
                add: [NET_BIND_SERVICE]
            volumeMounts:
              - { name: config, mountPath: /etc/nexora/engine.toml, subPath: engine.toml, readOnly: true }
              - { name: join, mountPath: /etc/nexora/join, readOnly: true }
              - { name: state, mountPath: /var/lib/nexora }
        volumes:
          - name: config
            configMap: { name: {{ $name }} }
          - name: join
            secret:
              secretName: {{ required (printf "engine.groups[%s].joinTokenSecret is required" $g.name) $g.joinTokenSecret }}
              defaultMode: 0400
          - name: state
            {{- if eq $.Values.engine.stateDir.type "hostPath" }}
            hostPath:
              path: {{ printf "%s/%s-%s" $.Values.engine.stateDir.hostPathPrefix $.Release.Name $g.name }}
              type: DirectoryOrCreate
            {{- else }}
            emptyDir: {}
            {{- end }}
  {{- end }}
  {{- end }}
  ```
- [ ] Create `deploy/helm/nexora/templates/engine-services.yaml`:
  ```yaml
  {{- if .Values.engine.enabled }}
  apiVersion: v1
  kind: Service
  metadata:
    name: {{ include "nexora.fullname" . }}-engine-metrics
    labels:
      {{- include "nexora.labels" (dict "root" . "component" "engine") | nindent 4 }}
      nexora.io/metrics: "true"
  spec:
    clusterIP: None
    selector: {{- include "nexora.selectorLabels" (dict "root" . "component" "engine") | nindent 4 }}
    ports:
      - { name: metrics, port: {{ .Values.engine.ports.metrics }}, targetPort: metrics, protocol: TCP }
  {{- range $g := .Values.engine.groups }}
  {{- $svc := $g.service | default dict }}
  ---
  apiVersion: v1
  kind: Service
  metadata:
    name: {{ include "nexora.fullname" $ }}-engine-{{ $g.name }}
    labels:
      {{- include "nexora.labels" (dict "root" $ "component" "engine") | nindent 4 }}
      nexora.io/group: {{ $g.name }}
    {{- with $svc.annotations }}
    annotations: {{- toYaml . | nindent 4 }}
    {{- end }}
  spec:
    type: {{ $svc.type | default "LoadBalancer" }}
    {{- with $svc.loadBalancerIP }}
    loadBalancerIP: {{ . }}
    {{- end }}
    {{- if ne ($svc.type | default "LoadBalancer") "ClusterIP" }}
    externalTrafficPolicy: {{ $svc.externalTrafficPolicy | default "Local" }}
    {{- end }}
    selector:
      {{- include "nexora.selectorLabels" (dict "root" $ "component" "engine") | nindent 4 }}
      nexora.io/group: {{ $g.name }}
    ports:
      - { name: dns-udp, port: 53, targetPort: dns-udp, protocol: UDP }
      - { name: dns-tcp, port: 53, targetPort: dns-tcp, protocol: TCP }
      {{- if $.Values.engine.ports.dot }}
      - { name: dot, port: 853, targetPort: dot, protocol: TCP }
      {{- end }}
      {{- if $.Values.engine.ports.doh }}
      - { name: doh, port: 443, targetPort: doh, protocol: TCP }
      {{- end }}
      {{- if $.Values.engine.ports.doq }}
      - { name: doq, port: 853, targetPort: doq, protocol: UDP }
      {{- end }}
  {{- end }}
  {{- end }}
  ```
- [ ] Create `deploy/helm/nexora/templates/database-cnpg.yaml`:
  ```yaml
  {{- if eq .Values.database.mode "cnpg" }}
  apiVersion: postgresql.cnpg.io/v1
  kind: Cluster
  metadata:
    name: {{ .Values.database.cnpg.clusterName }}
    labels: {{- include "nexora.labels" (dict "root" . "component" "database") | nindent 4 }}
  spec:
    instances: {{ .Values.database.cnpg.instances }}
    imageName: {{ .Values.database.cnpg.imageName }}
    bootstrap:
      initdb:
        database: nexora
        owner: nexora
    storage:
      size: {{ .Values.database.cnpg.size }}
      {{- with .Values.database.cnpg.storageClass }}
      storageClass: {{ . }}
      {{- end }}
    monitoring:
      enablePodMonitor: {{ .Values.database.cnpg.podMonitor }}
  {{- end }}
  ```
- [ ] Create `deploy/helm/nexora/templates/otel-collector.yaml` (the OpenSearch exporter settings are the ones M1 used for the kw collector; keep M1's keys when they differ):
  ```yaml
  {{- if .Values.otelCollector.enabled }}
  {{- $f := include "nexora.fullname" . }}
  {{- $c := .Values.otelCollector }}
  apiVersion: v1
  kind: ConfigMap
  metadata:
    name: {{ $f }}-otel-collector
    labels: {{- include "nexora.labels" (dict "root" . "component" "otel-collector") | nindent 4 }}
  data:
    config.yaml: |
      receivers:
        otlp:
          protocols:
            grpc: { endpoint: 0.0.0.0:4317 }
            http: { endpoint: 0.0.0.0:4318 }
      processors:
        memory_limiter: { check_interval: 1s, limit_percentage: 80, spike_limit_percentage: 20 }
        batch: { send_batch_size: 1000, timeout: 1s }
      exporters:
        debug: { verbosity: basic }
        {{- with $c.traces.otlpEndpoint }}
        otlp/traces:
          endpoint: {{ . }}
          tls: { insecure: {{ $c.traces.insecure }} }
        {{- end }}
        {{- with $c.logs.opensearch.url }}
        opensearch/logs:
          http: { endpoint: {{ . }} }
          logs_index: nexora-querylog
          logs_index_time_format: "yyyy.MM.dd"
        {{- end }}
      service:
        telemetry:
          metrics:
            readers:
              - pull: { exporter: { prometheus: { host: 0.0.0.0, port: 8888 } } }
        pipelines:
          traces: { receivers: [otlp], processors: [memory_limiter, batch], exporters: [{{ if $c.traces.otlpEndpoint }}otlp/traces{{ else }}debug{{ end }}] }
          logs: { receivers: [otlp], processors: [memory_limiter, batch], exporters: [{{ if $c.logs.opensearch.url }}opensearch/logs{{ else }}debug{{ end }}] }
          metrics: { receivers: [otlp], processors: [memory_limiter, batch], exporters: [debug] }
  ---
  apiVersion: apps/v1
  kind: Deployment
  metadata:
    name: {{ $f }}-otel-collector
    labels: {{- include "nexora.labels" (dict "root" . "component" "otel-collector") | nindent 4 }}
  spec:
    replicas: 1
    selector:
      matchLabels: {{- include "nexora.selectorLabels" (dict "root" . "component" "otel-collector") | nindent 6 }}
    template:
      metadata:
        labels: {{- include "nexora.selectorLabels" (dict "root" . "component" "otel-collector") | nindent 8 }}
        annotations:
          checksum/config: {{ $c | toJson | sha256sum }}
      spec:
        securityContext: { runAsNonRoot: true, runAsUser: 10001, seccompProfile: { type: RuntimeDefault } }
        containers:
          - name: otel-collector
            image: {{ $c.image }}
            args: ["--config", "/etc/otelcol/config.yaml"]
            ports:
              - { name: otlp-grpc, containerPort: 4317 }
              - { name: otlp-http, containerPort: 4318 }
              - { name: metrics, containerPort: 8888 }
            resources: {{- toYaml $c.resources | nindent 14 }}
            securityContext: {{- include "nexora.containerSecurity" . | nindent 14 }}
            volumeMounts:
              - { name: config, mountPath: /etc/otelcol, readOnly: true }
        volumes:
          - name: config
            configMap: { name: {{ $f }}-otel-collector }
  ---
  apiVersion: v1
  kind: Service
  metadata:
    name: {{ $f }}-otel-collector
    labels:
      {{- include "nexora.labels" (dict "root" . "component" "otel-collector") | nindent 4 }}
      nexora.io/metrics: "true"
  spec:
    selector: {{- include "nexora.selectorLabels" (dict "root" . "component" "otel-collector") | nindent 4 }}
    ports:
      - { name: otlp-grpc, port: 4317, targetPort: otlp-grpc }
      - { name: otlp-http, port: 4318, targetPort: otlp-http }
      - { name: otel-metrics, port: 8888, targetPort: metrics }
  {{- end }}
  ```
- [ ] Create `deploy/helm/nexora/templates/servicemonitor.yaml` and `prometheusrule.yaml`:
  ```yaml
  {{- if .Values.metrics.serviceMonitor.enabled }}
  apiVersion: monitoring.coreos.com/v1
  kind: ServiceMonitor
  metadata:
    name: {{ include "nexora.fullname" . }}
    namespace: {{ default .Release.Namespace .Values.metrics.serviceMonitor.namespace }}
    labels:
      {{- include "nexora.labels" (dict "root" . "component" "metrics") | nindent 4 }}
      {{- with .Values.metrics.serviceMonitor.labels }}
      {{- toYaml . | nindent 4 }}
      {{- end }}
  spec:
    namespaceSelector:
      matchNames: [{{ .Release.Namespace }}]
    selector:
      matchLabels:
        app.kubernetes.io/instance: {{ .Release.Name }}
        nexora.io/metrics: "true"
    endpoints:
      - { port: http, path: /metrics, interval: {{ .Values.metrics.serviceMonitor.interval }} }
      - { port: metrics, path: /metrics, interval: {{ .Values.metrics.serviceMonitor.interval }} }
      - { port: otel-metrics, path: /metrics, interval: {{ .Values.metrics.serviceMonitor.interval }} }
  {{- end }}
  ```
  ```yaml
  {{- if .Values.metrics.prometheusRule.enabled }}
  apiVersion: monitoring.coreos.com/v1
  kind: PrometheusRule
  metadata:
    name: {{ include "nexora.fullname" . }}
    namespace: {{ default .Release.Namespace .Values.metrics.prometheusRule.namespace }}
    labels:
      {{- include "nexora.labels" (dict "root" . "component" "metrics") | nindent 4 }}
      {{- with .Values.metrics.prometheusRule.labels }}
      {{- toYaml . | nindent 4 }}
      {{- end }}
  spec:
    groups:
      - name: nexora-fleet
        rules:
          - alert: NexoraEngineDisconnected
            expr: max(nexora_mgmt_engines_disconnected{namespace="{{ .Release.Namespace }}"}) > 0
            for: 0m
            labels: { severity: warning }
            annotations:
              summary: "{{ `{{ $value }}` }} Nexora engine(s) not seen by the management plane for more than 60 seconds"
          - alert: NexoraRolloutHalted
            expr: max(nexora_mgmt_rollouts{namespace="{{ .Release.Namespace }}",state="halted"}) > 0
            for: 0m
            labels: { severity: warning }
            annotations:
              summary: A Nexora config rollout halted on its health gate; roll back or fix forward
          - alert: NexoraManagementPlaneDown
            expr: absent(up{namespace="{{ .Release.Namespace }}",service="{{ include "nexora.fullname" . }}-mgmt"} == 1)
            for: 2m
            labels: { severity: critical }
            annotations:
              summary: No Nexora management plane instance is being scraped
  {{- end }}
  ```
- [ ] Create `deploy/helm/nexora/templates/NOTES.txt`:
  ```text
  Nexora {{ .Chart.AppVersion }} installed as {{ include "nexora.fullname" . }} in {{ .Release.Namespace }}.

  First admin (no default password exists):
    kubectl -n {{ .Release.Namespace }} exec deploy/{{ include "nexora.fullname" . }}-mgmt -c mgmt -- nexora-mgmt user create --admin --username admin --password-file /dev/stdin

  Engines join with a per-group token stored in the secret named by engine.groups[].joinTokenSecret:
    kubectl -n {{ .Release.Namespace }} exec deploy/{{ include "nexora.fullname" . }}-mgmt -c mgmt -- nexora-mgmt join-token create --group <group> --ttl 1h --max-uses <replicas>
  ```
- [ ] Create `deploy/helm/nexora/ci/lint-values.yaml`:
  ```yaml
  mgmt:
    ca: { existingSecret: nexora-ca }
  database:
    mode: external
    external: { existingSecret: nexora-db, key: url }
  engine:
    groups:
      - name: default
        replicas: 1
        joinTokenSecret: nexora-join-default
  otelCollector:
    enabled: true
    traces: { otlpEndpoint: collector.example:4317 }
  ```
- [ ] Create `deploy/kw/values-kw.yaml`:
  ```yaml
  # kw release: helm upgrade --install nexora deploy/helm/nexora -n nexora -f deploy/kw/values-kw.yaml --set image.tag=<tag>
  image:
    registry: 192.168.10.131
    repository: azrtydxb
  mgmt:
    replicas: 2
    publicURL: https://nexora.kw.local
    ca: { existingSecret: nexora-ca }
    querylog:
      backend: opensearch
      opensearch:
        url: http://opensearch.nexora.svc:9200
        index: nexora-querylog-*
    grpcService:
      type: LoadBalancer
      loadBalancerIP: 192.168.10.135
    ingress:
      enabled: true
      className: nginx
      host: nexora.kw.local
      clusterIssuer: cluster-ca
  database:
    mode: cnpg
    cnpg:
      clusterName: nexora-db
      instances: 2
      storageClass: longhorn-single
      size: 10Gi
  engine:
    kind: Deployment
    hostNetwork: false
    ports: { dns: 53, metrics: 9153, dot: 853, doh: 443, doq: 853 }
    extraToml: |
      listen_dot = ["0.0.0.0:853"]
      listen_doh = ["0.0.0.0:443"]
      listen_doq = ["0.0.0.0:853"]
    stateDir: { type: hostPath, hostPathPrefix: /var/lib/nexora }
    spreadAcrossNodes: true
    nodeAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        nodeSelectorTerms:
          - matchExpressions:
              - { key: node-role.kubernetes.io/control-plane, operator: DoesNotExist }
    groups:
      - name: edge-a
        replicas: 2
        joinTokenSecret: nexora-join-edge-a
        service: { type: LoadBalancer, loadBalancerIP: 192.168.10.136, externalTrafficPolicy: Local }
      - name: edge-b
        replicas: 1
        joinTokenSecret: nexora-join-edge-b
        service: { type: LoadBalancer, loadBalancerIP: 192.168.10.137, externalTrafficPolicy: Local }
  otelCollector:
    enabled: true
    traces: { otlpEndpoint: jaeger.observability:4317, insecure: true }
    logs:
      opensearch: { url: http://opensearch.nexora.svc:9200 }
  metrics:
    serviceMonitor:
      enabled: true
      namespace: monitoring
      labels: { release: kps }
    prometheusRule:
      enabled: true
      namespace: monitoring
      labels: { release: kps }
  ```
  `listen_dot`, `listen_doh`, `listen_doq` are the M2 bootstrap keys for encrypted listeners; use M2's spelling if it differs.
- [ ] Run `scripts/dev-exec.sh 'go test ./deploy/deploytest/ -count=1 -v'` and expect `--- PASS: TestHelmTemplate`, `--- PASS: TestImagesWorkflow`, `--- PASS: TestComposeExample`.
- [ ] Commit: `git add deploy/helm/nexora deploy/kw/values-kw.yaml deploy/deploytest/helm_test.go && git commit -m "feat(helm): finalise chart (groups, CNPG, collector, monitoring, schema)"`.

## Task 14: Final kw deployment and TestKwFullProduct

Files: `deploy/kw/opensearch.yaml` (single-node query-log store), `deploy/kw/fixture-http.yaml` (static blocklist fixture), `scripts/kw-deploy.sh` (install/upgrade), `scripts/kw-acceptance.sh` (runs the acceptance test with live addresses), `e2e/kw_bodies.go` (request bodies for M1–M4 resources used on kw), `e2e/kw_bodies_test.go` (`TestKwBodiesMatchOpenAPI`), `e2e/kw_full_test.go` (`TestKwFullProduct`), the M1 manifests under `deploy/kw/` that the chart replaces (removed)
Interfaces: environment read by `TestKwFullProduct`: `NEXORA_KW_API_URL`, `NEXORA_KW_API_TOKEN`, `NEXORA_KW_MGMT_ADDRS` (`ip:8080,...`), `NEXORA_KW_ENGINES` (`group=podIP@node,...`), `NEXORA_KW_GROUP_DNS` (`edge-a=192.168.10.136:53,edge-b=192.168.10.137:53`), `NEXORA_KW_INGRESS_IP`, `NEXORA_KW_FIXTURE_URL`, `NEXORA_KW_JAEGER_QUERY_URL`, `NEXORA_KW_PROMETHEUS_URL`; M2 harness `harness.ExchangeDoQ(addr, name string, qtype uint16) (*dns.Msg, error)`.

- [ ] Write the failing body contract test `e2e/kw_bodies_test.go`:
  ```go
  package e2e

  import (
  	"bytes"
  	"encoding/json"
  	"net/http/httptest"
  	"os"
  	"testing"

  	"github.com/pb33f/libopenapi"
  	validator "github.com/pb33f/libopenapi-validator"
  )

  func TestKwBodiesMatchOpenAPI(t *testing.T) {
  	spec, err := os.ReadFile("../mgmt/api/openapi.yaml")
  	if err != nil {
  		t.Fatal(err)
  	}
  	doc, err := libopenapi.NewDocument(spec)
  	if err != nil {
  		t.Fatal(err)
  	}
  	v, errs := validator.NewValidator(doc)
  	if len(errs) > 0 {
  		t.Fatal(errs)
  	}
  	id := "00000000-0000-0000-0000-000000000001"
  	cases := []struct {
  		method, path string
  		body         any
  	}{
  		{"POST", "/api/v1/upstreams", upstreamReq("kw-cloudflare", "1.1.1.1:53", nil)},
  		{"POST", "/api/v1/filter-lists", filterListReq("kw-hosts", "http://fixture/hosts.txt", "block", nil)},
  		{"POST", "/api/v1/client-policies", clientPolicyReq("kw-policy", "10.42.0.9/32", []string{id}, true, &id)},
  		{"POST", "/api/v1/zones", zoneReq("kw.nexora.test", "primary", nil, false, []string{"10.42.0.0/16"}, nil, []string{"kw-tsig"})},
  		{"POST", "/api/v1/zones", zoneReq("xfr.nexora.test", "secondary", &id, false, nil, []string{"192.168.10.136:53"}, nil)},
  		{"POST", "/api/v1/zones/" + id + "/records", recordReq("www", "A", 60, "192.0.2.80")},
  		{"POST", "/api/v1/tsig-keys", tsigKeyReq("kw-tsig")},
  		{"POST", "/api/v1/rpz-zones", rpzReq("kw-rpz", "$TTL 60\n@ SOA . . 1 1 1 1 1\nblocked-by-rpz.kw.test CNAME .\n")},
  		{"POST", "/api/v1/api-tokens", apiTokenReq("kw-viewer", "viewer")},
  		{"PUT", "/api/v1/settings/telemetry", telemetryReq("http://nexora-otel-collector.nexora.svc:4317", 100, 1)},
  	}
  	for _, c := range cases {
  		raw, _ := json.Marshal(c.body)
  		req := httptest.NewRequest(c.method, "http://nexora.test"+c.path, bytes.NewReader(raw))
  		req.Header.Set("Content-Type", "application/json")
  		if ok, verrs := v.ValidateHttpRequest(req); !ok {
  			for _, e := range verrs {
  				t.Errorf("%s %s: %s", c.method, c.path, e.Message)
  			}
  		}
  	}
  }
  ```
- [ ] Run `scripts/dev-exec.sh go test ./e2e/ -run TestKwBodiesMatchOpenAPI -count=1` and expect FAIL with `undefined: upstreamReq`.
- [ ] Create `e2e/kw_bodies.go`; each builder mirrors one M1–M4 request schema (when the validator reports a mismatch, change the builder's field names to the schema's, not the schema):
  ```go
  package e2e

  func upstreamReq(name, address string, groupID *string) map[string]any {
  	return map[string]any{"name": name, "address": address, "protocol": "udp", "group_id": groupID}
  }

  func filterListReq(name, url, kind string, groupID *string) map[string]any {
  	return map[string]any{"name": name, "url": url, "format": "hosts", "kind": kind, "enabled": true, "refresh_interval_seconds": 3600, "group_id": groupID}
  }

  func clientPolicyReq(name, cidr string, listIDs []string, safeSearch bool, groupID *string) map[string]any {
  	return map[string]any{"name": name, "client_cidrs": []string{cidr}, "filter_list_ids": listIDs, "safe_search": safeSearch, "group_id": groupID}
  }

  func zoneReq(name, kind string, groupID *string, signing bool, allowTransfer, primaries, updateKeys []string) map[string]any {
  	b := map[string]any{"name": name, "kind": kind, "group_id": groupID, "dnssec_signing": signing}
  	if allowTransfer != nil {
  		b["allow_transfer_cidrs"] = allowTransfer
  	}
  	if primaries != nil {
  		b["primaries"] = primaries
  	}
  	if updateKeys != nil {
  		b["update_tsig_keys"] = updateKeys
  	}
  	return b
  }

  func recordReq(name, typ string, ttl int, data string) map[string]any {
  	return map[string]any{"name": name, "type": typ, "ttl": ttl, "data": data}
  }

  func tsigKeyReq(name string) map[string]any {
  	return map[string]any{"name": name, "algorithm": "hmac-sha256"}
  }

  func rpzReq(name, content string) map[string]any {
  	return map[string]any{"name": name, "source": "inline", "content": content, "policy": "given"}
  }

  func apiTokenReq(name, role string) map[string]any {
  	return map[string]any{"name": name, "role": role, "ttl_seconds": 7200}
  }

  func telemetryReq(otlp string, sampleOneIn int, revision int64) map[string]any {
  	return map[string]any{"otlp_endpoint": otlp, "trace_sample_one_in": sampleOneIn, "trace_slow_threshold_us": 50000, "revision": revision}
  }
  ```
  Run `scripts/dev-exec.sh go test ./e2e/ -run TestKwBodiesMatchOpenAPI -count=1` and expect `ok` (after aligning any reported field).
- [ ] Write `e2e/kw_full_test.go`:
  ```go
  package e2e

  import (
  	"bytes"
  	"crypto/tls"
  	"encoding/base64"
  	"encoding/json"
  	"fmt"
  	"io"
  	"net"
  	"net/http"
  	"net/url"
  	"os"
  	"slices"
  	"sort"
  	"strings"
  	"sync"
  	"testing"
  	"time"

  	"github.com/miekg/dns"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  type kwEngine struct{ Group, IP, Node string }

  func (e kwEngine) Addr() string { return net.JoinHostPort(e.IP, "53") }

  type kwEnv struct {
  	api                                          *harness.API
  	mgmtAddrs                                    []string
  	engines                                      []kwEngine
  	groupDNS                                     map[string]string
  	ingressIP, fixtureURL, jaegerURL, promURL, token string
  }

  func splitList(v string) []string {
  	var out []string
  	for _, p := range strings.Split(v, ",") {
  		if p = strings.TrimSpace(p); p != "" {
  			out = append(out, p)
  		}
  	}
  	return out
  }

  func loadKwEnv(t *testing.T) *kwEnv {
  	t.Helper()
  	base := os.Getenv("NEXORA_KW_API_URL")
  	if base == "" {
  		t.Skip("NEXORA_KW_* not set; run scripts/kw-acceptance.sh")
  	}
  	k := &kwEnv{
  		api: harness.NewAPI(base, os.Getenv("NEXORA_KW_API_TOKEN")), token: os.Getenv("NEXORA_KW_API_TOKEN"),
  		mgmtAddrs: splitList(os.Getenv("NEXORA_KW_MGMT_ADDRS")), groupDNS: map[string]string{},
  		ingressIP: os.Getenv("NEXORA_KW_INGRESS_IP"), fixtureURL: os.Getenv("NEXORA_KW_FIXTURE_URL"),
  		jaegerURL: os.Getenv("NEXORA_KW_JAEGER_QUERY_URL"), promURL: os.Getenv("NEXORA_KW_PROMETHEUS_URL"),
  	}
  	for _, e := range splitList(os.Getenv("NEXORA_KW_ENGINES")) {
  		group, rest, _ := strings.Cut(e, "=")
  		ip, node, _ := strings.Cut(rest, "@")
  		k.engines = append(k.engines, kwEngine{group, ip, node})
  	}
  	for _, g := range splitList(os.Getenv("NEXORA_KW_GROUP_DNS")) {
  		name, addr, _ := strings.Cut(g, "=")
  		k.groupDNS[name] = addr
  	}
  	return k
  }

  func (k *kwEnv) group(g string) []kwEngine {
  	var out []kwEngine
  	for _, e := range k.engines {
  		if e.Group == g {
  			out = append(out, e)
  		}
  	}
  	return out
  }

  // ensure returns the id of the item at listPath whose field equals value, creating it from body when absent.
  func (k *kwEnv) ensure(t *testing.T, listPath, field, value string, body map[string]any) string {
  	t.Helper()
  	var items []map[string]any
  	k.api.Must(t, "GET", listPath, nil, &items, http.StatusOK)
  	for _, it := range items {
  		if it[field] == value {
  			return it["id"].(string)
  		}
  	}
  	var created map[string]any
  	k.api.Must(t, "POST", listPath, body, &created, http.StatusCreated)
  	return created["id"].(string)
  }

  func (k *kwEnv) groupByName(t *testing.T, name string) harness.EngineGroup {
  	t.Helper()
  	var gs []harness.EngineGroup
  	k.api.Must(t, "GET", "/api/v1/engine-groups", nil, &gs, http.StatusOK)
  	for _, g := range gs {
  		if g.Name == name {
  			return g
  		}
  	}
  	t.Fatalf("group %s missing", name)
  	return harness.EngineGroup{}
  }

  // settle waits until every engine is connected, in sync, and no rollout is open.
  func (k *kwEnv) settle(t *testing.T, timeout time.Duration) {
  	t.Helper()
  	harness.Eventually(t, timeout, func() error {
  		for _, e := range k.api.Engines(t) {
  			if e.ConnectionState != "connected" || e.Drift != "in_sync" {
  				return fmt.Errorf("%s is %s/%s", e.Name, e.ConnectionState, e.Drift)
  			}
  		}
  		var open []harness.Rollout
  		for _, s := range []string{"pending", "canary", "verifying", "rolling"} {
  			var rs []harness.Rollout
  			k.api.Must(t, "GET", "/api/v1/rollouts?state="+s, nil, &rs, http.StatusOK)
  			open = append(open, rs...)
  		}
  		if len(open) > 0 {
  			return fmt.Errorf("%d rollouts still open", len(open))
  		}
  		return nil
  	})
  }

  func query(addr, name string, qtype uint16, network string, do bool, bufsize uint16) (*dns.Msg, error) {
  	m := new(dns.Msg)
  	m.SetQuestion(dns.Fqdn(name), qtype)
  	if bufsize > 0 || do {
  		m.SetEdns0(max(bufsize, 512), do)
  	}
  	c := &dns.Client{Net: network, Timeout: 5 * time.Second}
  	if network == "tcp-tls" {
  		c.TLSConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // engine certificate trust is covered by TestEncryptedTransports
  	}
  	r, _, err := c.Exchange(m, addr)
  	return r, err
  }

  func aValues(r *dns.Msg) []string {
  	var out []string
  	for _, rr := range r.Answer {
  		if a, ok := rr.(*dns.A); ok {
  			out = append(out, a.A.String())
  		}
  	}
  	return out
  }

  func getJSON(t *testing.T, rawURL string, out any) {
  	t.Helper()
  	resp, err := http.Get(rawURL)
  	if err != nil {
  		t.Fatalf("GET %s: %v", rawURL, err)
  	}
  	defer resp.Body.Close()
  	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
  		t.Fatalf("decode %s: %v", rawURL, err)
  	}
  }

  func TestKwFullProduct(t *testing.T) {
  	k := loadKwEnv(t)
  	edgeA, edgeB := k.groupDNS["edge-a"], k.groupDNS["edge-b"]
  	ga, gb := k.groupByName(t, "edge-a"), k.groupByName(t, "edge-b")

  	// Baseline configuration, idempotent across runs.
  	k.ensure(t, "/api/v1/upstreams", "name", "kw-cloudflare", upstreamReq("kw-cloudflare", "1.1.1.1:53", nil))
  	k.ensure(t, "/api/v1/upstreams", "name", "kw-quad9", upstreamReq("kw-quad9", "9.9.9.9:53", nil))
  	if gb.UpstreamMode != "override" {
  		k.api.Must(t, "PUT", "/api/v1/engine-groups/"+gb.ID, map[string]any{"name": "edge-b", "revision": gb.Revision, "upstream_mode": "override"}, nil, http.StatusOK)
  	}
  	var tel map[string]any
  	k.api.Must(t, "GET", "/api/v1/settings/telemetry", nil, &tel, http.StatusOK)
  	k.api.Must(t, "PUT", "/api/v1/settings/telemetry", telemetryReq("http://nexora-otel-collector.nexora.svc:4317", 100, int64(tel["revision"].(float64))), nil, http.StatusOK)
  	k.settle(t, 3*time.Minute)

  	t.Run("M5/topology", func(t *testing.T) {
  		nodes := map[string]bool{}
  		for _, e := range k.engines {
  			nodes[e.Node] = true
  		}
  		if len(k.engines) != 3 || len(nodes) != 3 || len(k.group("edge-a")) != 2 || len(k.group("edge-b")) != 1 {
  			t.Fatalf("engines %+v, want 3 on distinct nodes: 2 in edge-a, 1 in edge-b", k.engines)
  		}
  		names := map[string]string{}
  		for _, e := range k.api.Engines(t) {
  			names[e.Name] = e.GroupName
  		}
  		for _, e := range k.engines {
  			if names[e.Group+"-"+e.Node] != e.Group {
  				t.Errorf("engine %s-%s not registered in %s: %v", e.Group, e.Node, e.Group, names)
  			}
  		}
  	})

  	t.Run("M1/mgmt_ha_and_ingress", func(t *testing.T) {
  		if len(k.mgmtAddrs) != 2 {
  			t.Fatalf("mgmt instances %v, want 2", k.mgmtAddrs)
  		}
  		for _, a := range k.mgmtAddrs {
  			harness.NewAPI("http://"+a, k.token).Must(t, "GET", "/api/v1/fleet/summary", nil, nil, http.StatusOK)
  		}
  		hc := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{ServerName: "nexora.kw.local", InsecureSkipVerify: true}}} //nolint:gosec
  		req, _ := http.NewRequest("GET", "https://"+k.ingressIP+"/api/v1/fleet/summary", nil)
  		req.Host = "nexora.kw.local"
  		req.Header.Set("Authorization", "Bearer "+k.token)
  		resp, err := hc.Do(req)
  		if err != nil || resp.StatusCode != http.StatusOK {
  			t.Fatalf("ingress: %v %v", resp, err)
  		}
  		resp.Body.Close()
  	})

  	t.Run("M1/forward_cache_ttl", func(t *testing.T) {
  		first, err := query(edgeA, "example.org", dns.TypeA, "udp", false, 0)
  		if err != nil || len(first.Answer) == 0 {
  			t.Fatalf("first: %v %v", first, err)
  		}
  		time.Sleep(2 * time.Second)
  		for _, e := range k.group("edge-a") {
  			_, _ = query(e.Addr(), "example.org", dns.TypeA, "udp", false, 0)
  		}
  		second, err := query(edgeA, "example.org", dns.TypeA, "udp", false, 0)
  		if err != nil || len(second.Answer) == 0 || second.Answer[0].Header().Ttl >= first.Answer[0].Header().Ttl {
  			t.Fatalf("TTL %d then %v (%v), want a decremented cached TTL", first.Answer[0].Header().Ttl, second, err)
  		}
  	})

  	t.Run("M1/blocklist_allowlist", func(t *testing.T) {
  		k.ensure(t, "/api/v1/filter-lists", "name", "kw-hosts", filterListReq("kw-hosts", k.fixtureURL+"/hosts.txt", "block", nil))
  		k.ensure(t, "/api/v1/filter-lists", "name", "kw-allow", filterListReq("kw-allow", k.fixtureURL+"/allow.txt", "allow", nil))
  		harness.Eventually(t, 2*time.Minute, func() error {
  			for _, e := range k.engines {
  				r, err := query(e.Addr(), "blocked.kw-acceptance.test", dns.TypeA, "udp", false, 0)
  				if err != nil || !slices.Equal(aValues(r), []string{"0.0.0.0"}) {
  					return fmt.Errorf("%s not blocked on %s: %v %v", "blocked.kw-acceptance.test", e.IP, r, err)
  				}
  			}
  			return nil
  		})
  		for _, e := range k.engines {
  			if r, err := query(e.Addr(), "allowed.kw-acceptance.test", dns.TypeA, "udp", false, 0); err != nil || slices.Contains(aValues(r), "0.0.0.0") {
  				t.Fatalf("allowlisted name blocked on %s: %v %v", e.IP, r, err)
  			}
  		}
  	})

  	var zoneID string
  	t.Run("M4/authoritative_propagation", func(t *testing.T) {
  		zoneID = k.ensure(t, "/api/v1/zones", "name", "kw.nexora.test", zoneReq("kw.nexora.test", "primary", nil, false, []string{"10.42.0.0/16"}, nil, nil))
  		stamp := fmt.Sprintf("192.0.2.%d", time.Now().Second()%200+10)
  		k.api.Must(t, "POST", "/api/v1/zones/"+zoneID+"/records", recordReq(fmt.Sprintf("p%d", time.Now().UnixNano()), "A", 60, stamp), nil, http.StatusCreated)
  		k.api.Do(t, "POST", "/api/v1/zones/"+zoneID+"/records", recordReq("big", "TXT", 60, strings.Repeat("\"nexora-large-answer-padding-0123456789\" ", 40)), nil) // 201, or 409 on a rerun
  		start := time.Now()
  		harness.Eventually(t, 5*time.Second, func() error {
  			for _, e := range k.engines {
  				r, err := query(e.Addr(), "kw.nexora.test", dns.TypeSOA, "udp", false, 0)
  				if err != nil || !r.Authoritative {
  					return fmt.Errorf("%s not authoritative: %v", e.IP, err)
  				}
  			}
  			return nil
  		})
  		t.Logf("zone authoritative on all engines after %s", time.Since(start))
  	})

  	t.Run("M1/edns_truncation_tcp", func(t *testing.T) {
  		udp, err := query(edgeA, "big.kw.nexora.test", dns.TypeTXT, "udp", false, 512)
  		if err != nil || !udp.Truncated {
  			t.Fatalf("UDP with 512 buffer: TC=%v err %v", udp != nil && udp.Truncated, err)
  		}
  		tcp, err := query(edgeA, "big.kw.nexora.test", dns.TypeTXT, "tcp", false, 0)
  		if err != nil || tcp.Truncated || len(tcp.Answer) == 0 {
  			t.Fatalf("TCP: %v %v", tcp, err)
  		}
  	})

  	t.Run("M2/encrypted_transports", func(t *testing.T) {
  		host, _, _ := net.SplitHostPort(edgeA)
  		want := "192.0.2.53"
  		k.ensure(t, "/api/v1/rewrites", "domain", "kw-rewrite.kw.test", map[string]any{"domain": "kw-rewrite.kw.test", "type": "A", "value": want, "group_id": nil})
  		k.settle(t, time.Minute)
  		dot, err := query(net.JoinHostPort(host, "853"), "kw-rewrite.kw.test", dns.TypeA, "tcp-tls", false, 0)
  		if err != nil || !slices.Equal(aValues(dot), []string{want}) {
  			t.Fatalf("DoT: %v %v", dot, err)
  		}
  		m := new(dns.Msg)
  		m.SetQuestion("kw-rewrite.kw.test.", dns.TypeA)
  		wire, _ := m.Pack()
  		hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ForceAttemptHTTP2: true}} //nolint:gosec
  		for _, method := range []string{"GET", "POST"} {
  			var req *http.Request
  			if method == "GET" {
  				req, _ = http.NewRequest("GET", "https://"+host+"/dns-query?dns="+base64.RawURLEncoding.EncodeToString(wire), nil)
  			} else {
  				req, _ = http.NewRequest("POST", "https://"+host+"/dns-query", bytes.NewReader(wire))
  				req.Header.Set("Content-Type", "application/dns-message")
  			}
  			req.Header.Set("Accept", "application/dns-message")
  			resp, err := hc.Do(req)
  			if err != nil {
  				t.Fatalf("DoH %s: %v", method, err)
  			}
  			body, _ := io.ReadAll(resp.Body)
  			resp.Body.Close()
  			var r dns.Msg
  			if err := r.Unpack(body); err != nil || !slices.Equal(aValues(&r), []string{want}) {
  				t.Fatalf("DoH %s: status %d %v %v", method, resp.StatusCode, r.Answer, err)
  			}
  		}
  		doq, err := harness.ExchangeDoQ(net.JoinHostPort(host, "853"), "kw-rewrite.kw.test", dns.TypeA)
  		if err != nil || !slices.Equal(aValues(doq), []string{want}) {
  			t.Fatalf("DoQ: %v %v", doq, err)
  		}
  	})

  	t.Run("M2/per_client_policy_safe_search_scoped_to_group", func(t *testing.T) {
  		conn, err := net.Dial("udp", edgeA)
  		if err != nil {
  			t.Fatal(err)
  		}
  		myIP := conn.LocalAddr().(*net.UDPAddr).IP.String()
  		conn.Close()
  		listID := k.ensure(t, "/api/v1/filter-lists", "name", "kw-policy-list", filterListReq("kw-policy-list", k.fixtureURL+"/policy.txt", "block", &ga.ID))
  		k.ensure(t, "/api/v1/client-policies", "name", "kw-dev-pod", clientPolicyReq("kw-dev-pod", myIP+"/32", []string{listID}, true, &ga.ID))
  		harness.Eventually(t, 2*time.Minute, func() error {
  			r, err := query(edgeA, "policy-block.kw-acceptance.test", dns.TypeA, "udp", false, 0)
  			if err != nil || !slices.Equal(aValues(r), []string{"0.0.0.0"}) {
  				return fmt.Errorf("edge-a policy not applied: %v %v", r, err)
  			}
  			return nil
  		})
  		if r, err := query(edgeB, "policy-block.kw-acceptance.test", dns.TypeA, "udp", false, 0); err != nil || slices.Contains(aValues(r), "0.0.0.0") {
  			t.Fatalf("edge-b applied a policy scoped to edge-a: %v %v", r, err)
  		}
  		safe, err := query(edgeA, "www.google.com", dns.TypeA, "udp", false, 0)
  		if err != nil || !slices.Contains(aValues(safe), "216.239.38.120") {
  			t.Fatalf("safe search on edge-a: %v %v", safe, err)
  		}
  		plain, err := query(edgeB, "www.google.com", dns.TypeA, "udp", false, 0)
  		if err != nil || slices.Contains(aValues(plain), "216.239.38.120") {
  			t.Fatalf("safe search leaked to edge-b: %v %v", plain, err)
  		}
  	})

  	t.Run("M3/recursion_and_dnssec_validation", func(t *testing.T) {
  		secure, err := query(edgeB, "isc.org", dns.TypeA, "udp", true, 1232)
  		if err != nil || secure.Rcode != dns.RcodeSuccess || !secure.AuthenticatedData {
  			t.Fatalf("isc.org via recursion: %v %v", secure, err)
  		}
  		bogus, err := query(edgeB, "dnssec-failed.org", dns.TypeA, "udp", true, 1232)
  		if err != nil || bogus.Rcode != dns.RcodeServerFailure {
  			t.Fatalf("dnssec-failed.org: %v %v", bogus, err)
  		}
  	})

  	t.Run("M3/rpz", func(t *testing.T) {
  		k.ensure(t, "/api/v1/rpz-zones", "name", "kw-rpz", rpzReq("kw-rpz", "$TTL 60\n@ SOA . . 1 3600 600 86400 60\nblocked-by-rpz.kw.test CNAME .\n"))
  		harness.Eventually(t, time.Minute, func() error {
  			for _, g := range []string{edgeA, edgeB} {
  				r, err := query(g, "blocked-by-rpz.kw.test", dns.TypeA, "udp", false, 0)
  				if err != nil || r.Rcode != dns.RcodeNameError {
  					return fmt.Errorf("rpz not applied at %s: %v %v", g, r, err)
  				}
  			}
  			return nil
  		})
  	})

  	t.Run("M4/axfr_out_and_secondary_via_notify", func(t *testing.T) {
  		tr := new(dns.Transfer)
  		m := new(dns.Msg)
  		m.SetAxfr("kw.nexora.test.")
  		env, err := tr.In(m, k.group("edge-a")[0].Addr())
  		if err != nil {
  			t.Fatal(err)
  		}
  		n := 0
  		for e := range env {
  			if e.Error != nil {
  				t.Fatal(e.Error)
  			}
  			n += len(e.RR)
  		}
  		if n < 3 {
  			t.Fatalf("AXFR returned %d records", n)
  		}

  		primary := k.ensure(t, "/api/v1/zones?group_id="+ga.ID, "name", "xfr.nexora.test", zoneReq("xfr.nexora.test", "primary", &ga.ID, false, []string{"10.42.0.0/16", "192.168.10.0/24"}, nil, nil))
  		k.ensure(t, "/api/v1/zones?group_id="+gb.ID, "name", "xfr.nexora.test", zoneReq("xfr.nexora.test", "secondary", &gb.ID, false, nil, []string{edgeA}, nil))
  		label := fmt.Sprintf("n%d", time.Now().UnixNano())
  		k.api.Must(t, "POST", "/api/v1/zones/"+primary+"/records", recordReq(label, "A", 60, "192.0.2.44"), nil, http.StatusCreated)
  		harness.Eventually(t, time.Minute, func() error {
  			r, err := query(edgeB, label+".xfr.nexora.test", dns.TypeA, "udp", false, 0)
  			if err != nil || !slices.Equal(aValues(r), []string{"192.0.2.44"}) {
  				return fmt.Errorf("secondary on edge-b has not picked up %s: %v %v", label, r, err)
  			}
  			return nil
  		})
  	})

  	t.Run("M4/dynamic_update_tsig", func(t *testing.T) {
  		run := time.Now().Unix()
  		keyName := fmt.Sprintf("kw-tsig-%d", run)
  		zone := fmt.Sprintf("u%d.nexora.test", run)
  		var key struct {
  			ID     string `json:"id"`
  			Secret string `json:"secret"`
  		}
  		k.api.Must(t, "POST", "/api/v1/tsig-keys", tsigKeyReq(keyName), &key, http.StatusCreated)
  		var z struct {
  			ID string `json:"id"`
  		}
  		k.api.Must(t, "POST", "/api/v1/zones", zoneReq(zone, "primary", nil, false, nil, nil, []string{keyName}), &z, http.StatusCreated)
  		defer func() {
  			k.api.Do(t, "DELETE", "/api/v1/zones/"+z.ID, nil, nil)
  			k.api.Do(t, "DELETE", "/api/v1/tsig-keys/"+key.ID, nil, nil)
  		}()
  		k.settle(t, time.Minute)
  		target := k.group("edge-a")[0].Addr()
  		name := "dyn." + zone + "."
  		rr, _ := dns.NewRR(name + " 60 IN A 192.0.2.66")

  		signed := new(dns.Msg)
  		signed.SetUpdate(zone + ".")
  		signed.Insert([]dns.RR{rr})
  		signed.SetTsig(keyName+".", dns.HmacSHA256, 300, time.Now().Unix())
  		c := &dns.Client{TsigSecret: map[string]string{keyName + ".": key.Secret}}
  		if r, _, err := c.Exchange(signed, target); err != nil || r.Rcode != dns.RcodeSuccess {
  			t.Fatalf("signed update: %v %v", r, err)
  		}
  		harness.Eventually(t, 10*time.Second, func() error {
  			for _, e := range k.engines {
  				r, err := query(e.Addr(), name, dns.TypeA, "udp", false, 0)
  				if err != nil || !slices.Equal(aValues(r), []string{"192.0.2.66"}) {
  					return fmt.Errorf("%s lacks updated record: %v %v", e.IP, r, err)
  				}
  			}
  			return nil
  		})
  		rr2, _ := dns.NewRR("unsigned." + zone + ". 60 IN A 192.0.2.67")
  		unsigned := new(dns.Msg)
  		unsigned.SetUpdate(zone + ".")
  		unsigned.Insert([]dns.RR{rr2})
  		if r, _, err := new(dns.Client).Exchange(unsigned, target); err != nil || r.Rcode != dns.RcodeRefused {
  			t.Fatalf("unsigned update: %v %v, want REFUSED", r, err)
  		}
  	})

  	t.Run("M4/dnssec_signing", func(t *testing.T) {
  		signedZone := k.ensure(t, "/api/v1/zones", "name", "signed.nexora.test", zoneReq("signed.nexora.test", "primary", nil, true, nil, nil, nil))
  		k.api.Do(t, "POST", "/api/v1/zones/"+signedZone+"/records", recordReq("www", "A", 60, "192.0.2.90"), nil)
  		var keys *dns.Msg
  		harness.Eventually(t, time.Minute, func() error {
  			var err error
  			keys, err = query(edgeA, "signed.nexora.test", dns.TypeDNSKEY, "tcp", true, 4096)
  			if err != nil || len(keys.Answer) == 0 {
  				return fmt.Errorf("no DNSKEY yet: %v", err)
  			}
  			return nil
  		})
  		ans, err := query(edgeA, "www.signed.nexora.test", dns.TypeA, "tcp", true, 4096)
  		if err != nil {
  			t.Fatal(err)
  		}
  		var rrset []dns.RR
  		var sig *dns.RRSIG
  		for _, rr := range ans.Answer {
  			switch v := rr.(type) {
  			case *dns.RRSIG:
  				sig = v
  			default:
  				rrset = append(rrset, rr)
  			}
  		}
  		if sig == nil || len(rrset) == 0 {
  			t.Fatalf("answer lacks RRSIG: %v", ans)
  		}
  		for _, rr := range keys.Answer {
  			if key, ok := rr.(*dns.DNSKEY); ok && key.KeyTag() == sig.KeyTag {
  				if err := sig.Verify(key, rrset); err != nil {
  					t.Fatalf("RRSIG does not verify: %v", err)
  				}
  				return
  			}
  		}
  		t.Fatalf("no DNSKEY with tag %d", sig.KeyTag)
  	})

  	t.Run("M4/zone_file_round_trip", func(t *testing.T) {
  		zone := fmt.Sprintf("rt%d.nexora.test.", time.Now().Unix())
  		src := "$ORIGIN " + zone + "\n$TTL 300\n@ IN SOA ns1 hostmaster 1 3600 600 86400 300\n@ IN NS ns1\nns1 IN A 192.0.2.1\nwww IN A 192.0.2.2\nmail IN MX 10 www\ntxt IN TXT \"a b\"\n"
  		id := k.ensure(t, "/api/v1/zones", "name", strings.TrimSuffix(zone, "."), zoneReq(strings.TrimSuffix(zone, "."), "primary", nil, false, nil, nil, nil))
  		req, _ := http.NewRequest("POST", k.api.Base+"/api/v1/zones/"+id+"/import", strings.NewReader(src))
  		req.Header.Set("Authorization", "Bearer "+k.token)
  		req.Header.Set("Content-Type", "text/dns")
  		if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode >= 300 {
  			t.Fatalf("import: %v %v", resp, err)
  		}
  		req, _ = http.NewRequest("GET", k.api.Base+"/api/v1/zones/"+id+"/export", nil)
  		req.Header.Set("Authorization", "Bearer "+k.token)
  		resp, err := http.DefaultClient.Do(req)
  		if err != nil {
  			t.Fatal(err)
  		}
  		out, _ := io.ReadAll(resp.Body)
  		resp.Body.Close()
  		norm := func(text string) []string {
  			var rrs []string
  			zp := dns.NewZoneParser(strings.NewReader(text), zone, "")
  			for rr, ok := zp.Next(); ok; rr, ok = zp.Next() {
  				if rr.Header().Rrtype == dns.TypeSOA {
  					continue // serial changes on import
  				}
  				rrs = append(rrs, strings.ToLower(rr.String()))
  			}
  			sort.Strings(rrs)
  			return rrs
  		}
  		if a, b := norm(src), norm(string(out)); !slices.Equal(a, b) {
  			t.Fatalf("round trip differs:\nimported %v\nexported %v", a, b)
  		}
  	})

  	t.Run("M1/query_log_opensearch", func(t *testing.T) {
  		name := fmt.Sprintf("ql%d.kw-rewrite.kw.test", time.Now().UnixNano())
  		_, _ = query(edgeA, name, dns.TypeA, "udp", false, 0)
  		harness.Eventually(t, 10*time.Second, func() error {
  			var page struct {
  				Items []map[string]any `json:"items"`
  			}
  			k.api.Must(t, "GET", "/api/v1/query-log?name="+url.QueryEscape(name)+"&limit=5", nil, &page, http.StatusOK)
  			if len(page.Items) == 0 {
  				return fmt.Errorf("query %s not in the query log yet", name)
  			}
  			return nil
  		})
  	})

  	t.Run("M1/auth_viewer_forbidden_and_audit", func(t *testing.T) {
  		var tok struct {
  			Token string `json:"token"`
  		}
  		k.api.Must(t, "POST", "/api/v1/api-tokens", apiTokenReq(fmt.Sprintf("kw-viewer-%d", time.Now().Unix()), "viewer"), &tok, http.StatusCreated)
  		viewer := harness.NewAPI(k.api.Base, tok.Token)
  		viewer.Must(t, "GET", "/api/v1/engines", nil, nil, http.StatusOK)
  		if code := viewer.Do(t, "POST", "/api/v1/engine-groups", map[string]any{"name": "viewer-attempt"}, nil); code != http.StatusForbidden {
  			t.Fatalf("viewer createEngineGroup = %d, want 403", code)
  		}
  		var audit []map[string]any
  		k.api.Must(t, "GET", "/api/v1/audit?limit=200", nil, &audit, http.StatusOK)
  		found := false
  		for _, a := range audit {
  			found = found || a["operation_id"] == "createApiToken"
  		}
  		if !found {
  			t.Fatal("token creation has no audit entry")
  		}
  	})

  	t.Run("M5/canary_rollout_and_rollback", func(t *testing.T) {
  		g := k.groupByName(t, "edge-a")
  		k.api.Must(t, "PUT", "/api/v1/engine-groups/"+g.ID, map[string]any{"name": "edge-a", "revision": g.Revision,
  			"rollout_strategy": "canary", "canary_count": 1, "health_window_seconds": 30, "ack_timeout_seconds": 60,
  			"max_servfail_ratio": 0.05, "min_health_queries": 20}, nil, http.StatusOK)
  		stop := make(chan struct{})
  		var wg sync.WaitGroup
  		for _, e := range k.group("edge-a") {
  			wg.Add(1)
  			go func(addr string) {
  				defer wg.Done()
  				for {
  					select {
  					case <-stop:
  						return
  					case <-time.After(50 * time.Millisecond):
  						_, _ = query(addr, "kw-rewrite.kw.test", dns.TypeA, "udp", false, 0)
  					}
  				}
  			}(e.Addr())
  		}
  		defer func() { close(stop); wg.Wait() }()

  		before := stableOf(k.groupByName(t, "edge-b"))
  		value := fmt.Sprintf("192.0.2.%d", time.Now().Unix()%200+20)
  		k.api.Must(t, "POST", "/api/v1/rewrites", map[string]any{"domain": fmt.Sprintf("canary%d.kw.test", time.Now().Unix()), "type": "A", "value": value, "group_id": g.ID}, nil, http.StatusCreated)
  		var rs []harness.Rollout
  		k.api.Must(t, "GET", "/api/v1/rollouts?limit=1&group_id="+g.ID, nil, &rs, http.StatusOK)
  		done := k.api.WaitRollout(t, g.ID, rs[0].Version, 4*time.Minute, "completed")
  		if done.Strategy != "canary" || len(done.CanaryEngineIDs) != 1 {
  			t.Fatalf("rollout %+v", done)
  		}
  		if after := stableOf(k.groupByName(t, "edge-b")); after != before {
  			t.Fatalf("edge-a change moved edge-b from %d to %d", before, after)
  		}
  		prev := done.Version
  		var list []harness.Rollout
  		k.api.Must(t, "GET", "/api/v1/rollouts?limit=10&group_id="+g.ID+"&state=completed", nil, &list, http.StatusOK)
  		for _, r := range list {
  			if r.Version < done.Version {
  				prev = r.Version
  				break
  			}
  		}
  		var rb harness.Rollout
  		k.api.Must(t, "POST", "/api/v1/engine-groups/"+g.ID+"/rollback", map[string]any{"to_version": prev}, &rb, http.StatusAccepted)
  		k.api.WaitRollout(t, g.ID, rb.Version, 2*time.Minute, "completed")
  		var resumed harness.Rollout
  		k.api.Must(t, "POST", "/api/v1/engine-groups/"+g.ID+"/resume-rollouts", nil, &resumed, http.StatusAccepted)
  		k.api.WaitRollout(t, g.ID, resumed.Version, 4*time.Minute, "completed")
  	})

  	t.Run("M5/certificate_rotation", func(t *testing.T) {
  		var target harness.EngineInfo
  		for _, e := range k.api.Engines(t) {
  			if e.GroupName == "edge-b" {
  				target = e
  			}
  		}
  		old := target.Certificate.Serial
  		k.api.Must(t, "POST", "/api/v1/engines/"+target.ID+"/rotate-certificate", nil, nil, http.StatusAccepted)
  		harness.Eventually(t, time.Minute, func() error {
  			e := k.api.EngineByName(t, target.Name)
  			if e.Certificate == nil || e.Certificate.Serial == old || e.ConnectionState != "connected" {
  				return fmt.Errorf("%s not rotated yet", target.Name)
  			}
  			return nil
  		})
  	})

  	t.Run("observability/prometheus_and_jaeger", func(t *testing.T) {
  		promQuery := func(q string) float64 {
  			var out struct {
  				Data struct {
  					Result []struct {
  						Value []any `json:"value"`
  					} `json:"result"`
  				} `json:"data"`
  			}
  			getJSON(t, k.promURL+"/api/v1/query?query="+url.QueryEscape(q), &out)
  			if len(out.Data.Result) == 0 {
  				return -1
  			}
  			var v float64
  			fmt.Sscan(out.Data.Result[0].Value[1].(string), &v)
  			return v
  		}
  		harness.Eventually(t, 2*time.Minute, func() error {
  			if v := promQuery(`count(up{namespace="nexora",service="nexora-engine-metrics"} == 1)`); v != 3 {
  				return fmt.Errorf("engine targets up = %v, want 3", v)
  			}
  			if v := promQuery(`max(nexora_mgmt_engines_disconnected{namespace="nexora"})`); v != 0 {
  				return fmt.Errorf("nexora_mgmt_engines_disconnected = %v, want 0", v)
  			}
  			if v := promQuery(`sum(nexora_queries_total{namespace="nexora"})`); v <= 0 {
  				return fmt.Errorf("nexora_queries_total = %v", v)
  			}
  			return nil
  		})
  		_, _ = query(edgeB, "dnssec-failed.org", dns.TypeA, "udp", true, 1232)
  		harness.Eventually(t, 90*time.Second, func() error {
  			var traces struct {
  				Data []any `json:"data"`
  			}
  			tags := url.QueryEscape(`{"dns.response.code":"SERVFAIL"}`)
  			getJSON(t, k.jaegerURL+"/api/traces?service=nexora-engine&lookback=15m&limit=5&tags="+tags, &traces)
  			if len(traces.Data) == 0 {
  				return fmt.Errorf("no SERVFAIL trace in Jaeger yet")
  			}
  			return nil
  		})
  	})
  }
  ```
- [ ] Run `scripts/dev-exec.sh 'go vet ./e2e/ && go test ./e2e/ -run TestKwFullProduct -count=1 -v'` without the `NEXORA_KW_*` variables and expect `--- SKIP: TestKwFullProduct` with `NEXORA_KW_* not set; run scripts/kw-acceptance.sh`.
- [ ] Create `deploy/kw/opensearch.yaml`:
  ```yaml
  apiVersion: apps/v1
  kind: StatefulSet
  metadata:
    name: opensearch
    namespace: nexora
    labels: { app: opensearch }
  spec:
    serviceName: opensearch
    replicas: 1
    selector: { matchLabels: { app: opensearch } }
    template:
      metadata: { labels: { app: opensearch } }
      spec:
        securityContext: { fsGroup: 1000, runAsUser: 1000, runAsNonRoot: true }
        containers:
          - name: opensearch
            image: opensearchproject/opensearch:3.2.0
            env:
              - { name: discovery.type, value: single-node }
              - { name: DISABLE_SECURITY_PLUGIN, value: "true" }
              - { name: DISABLE_INSTALL_DEMO_CONFIG, value: "true" }
              - { name: OPENSEARCH_JAVA_OPTS, value: "-Xms1g -Xmx1g" }
              - { name: node.store.allow_mmap, value: "false" }
              - { name: bootstrap.memory_lock, value: "false" }
            ports: [{ name: http, containerPort: 9200 }]
            readinessProbe:
              httpGet: { path: /_cluster/health?local=true, port: http }
              periodSeconds: 10
              failureThreshold: 30
            resources:
              requests: { cpu: 500m, memory: 2Gi }
              limits: { memory: 2Gi }
            volumeMounts: [{ name: data, mountPath: /usr/share/opensearch/data }]
    volumeClaimTemplates:
      - metadata: { name: data }
        spec:
          accessModes: [ReadWriteOnce]
          storageClassName: longhorn-single
          resources: { requests: { storage: 20Gi } }
  ---
  apiVersion: v1
  kind: Service
  metadata:
    name: opensearch
    namespace: nexora
  spec:
    selector: { app: opensearch }
    ports: [{ name: http, port: 9200, targetPort: http }]
  ```
- [ ] Create `deploy/kw/fixture-http.yaml`:
  ```yaml
  apiVersion: v1
  kind: ConfigMap
  metadata:
    name: kw-fixture-http
    namespace: nexora
  data:
    hosts.txt: |
      # kw acceptance blocklist
      0.0.0.0 blocked.kw-acceptance.test
      0.0.0.0 allowed.kw-acceptance.test
    allow.txt: |
      allowed.kw-acceptance.test
    policy.txt: |
      policy-block.kw-acceptance.test
  ---
  apiVersion: apps/v1
  kind: Deployment
  metadata:
    name: kw-fixture-http
    namespace: nexora
    labels: { app: kw-fixture-http }
  spec:
    replicas: 1
    selector: { matchLabels: { app: kw-fixture-http } }
    template:
      metadata: { labels: { app: kw-fixture-http } }
      spec:
        securityContext: { runAsNonRoot: true, runAsUser: 101 }
        containers:
          - name: nginx
            image: nginxinc/nginx-unprivileged:1.29-alpine
            ports: [{ name: http, containerPort: 8080 }]
            securityContext: { allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: { drop: [ALL] } }
            resources: { requests: { cpu: 10m, memory: 16Mi }, limits: { memory: 64Mi } }
            volumeMounts:
              - { name: content, mountPath: /usr/share/nginx/html, readOnly: true }
              - { name: tmp, mountPath: /tmp }
        volumes:
          - name: content
            configMap: { name: kw-fixture-http }
          - name: tmp
            emptyDir: {}
  ---
  apiVersion: v1
  kind: Service
  metadata:
    name: kw-fixture-http
    namespace: nexora
  spec:
    selector: { app: kw-fixture-http }
    ports: [{ name: http, port: 8080, targetPort: http }]
  ```
- [ ] Create `scripts/kw-deploy.sh` (mode 0755):
  ```bash
  #!/usr/bin/env bash
  # Install or upgrade the Nexora kw deployment from the Helm chart.
  #   scripts/kw-deploy.sh <image-tag>        e.g. sha-1a2b3c4
  set -euo pipefail
  tag=${1:?usage: scripts/kw-deploy.sh <image-tag>}
  root=$(git -C "$(dirname "$0")/.." rev-parse --show-toplevel)
  k() { kubectl --context kw -n nexora "$@"; }
  mgmt() { k exec -i deploy/nexora-mgmt -c mgmt -- nexora-mgmt "$@"; }
  release() {
  	helm --kube-context kw -n nexora upgrade --install nexora "$root/deploy/helm/nexora" \
  		-f "$root/deploy/kw/values-kw.yaml" --set image.tag="$tag" --wait --timeout 15m "$@"
  }

  kubectl --context kw create namespace nexora --dry-run=client -o yaml | kubectl --context kw apply -f -
  k apply -f "$root/deploy/kw/opensearch.yaml" -f "$root/deploy/kw/fixture-http.yaml"
  k rollout status statefulset/opensearch --timeout 10m
  k rollout status deploy/kw-fixture-http --timeout 5m

  if ! k get secret nexora-ca >/dev/null 2>&1; then
  	work=$(mktemp -d)
  	trap 'rm -rf "$work"' EXIT
  	"$root/scripts/dev-exec.sh" 'rm -rf /tmp/nexora-ca && go run ./mgmt/cmd/nexora-mgmt ca init --out /tmp/nexora-ca >&2 && tar -C /tmp/nexora-ca -cf - ca.crt ca.key' >"$work/ca.tar"
  	tar -C "$work" -xf "$work/ca.tar"
  	k create secret generic nexora-ca --from-file=ca.crt="$work/ca.crt" --from-file=ca.key="$work/ca.key"
  fi

  # Phase 1: database and management plane; engines need join tokens from it.
  release --set engine.enabled=false
  k rollout status deploy/nexora-mgmt --timeout 10m

  if ! k get secret nexora-admin >/dev/null 2>&1; then
  	password=$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 32)
  	printf '%s' "$password" | mgmt user create --admin --username admin --password-file /dev/stdin
  	k create secret generic nexora-admin --from-literal=username=admin --from-literal=password="$password"
  fi

  for group in edge-a edge-b; do
  	mgmt group create --name "$group" --if-missing >/dev/null
  	if ! k get secret "nexora-join-$group" >/dev/null 2>&1; then
  		token=$(mgmt join-token create --group "$group" --ttl 24h --max-uses 4)
  		k create secret generic "nexora-join-$group" --from-literal=token="$token"
  	fi
  done

  # Phase 2: engines.
  release
  k rollout status deploy/nexora-engine-edge-a --timeout 10m
  k rollout status deploy/nexora-engine-edge-b --timeout 10m
  k get pods -o wide -l app.kubernetes.io/component=engine
  ```
- [ ] Create `scripts/kw-acceptance.sh` (mode 0755):
  ```bash
  #!/usr/bin/env bash
  # Run TestKwFullProduct in the dev pod against the live kw deployment.
  set -euo pipefail
  k() { kubectl --context kw -n nexora "$@"; }
  token=$(k exec deploy/nexora-mgmt -c mgmt -- nexora-mgmt api-token create --user admin --name "kw-acceptance-$(date +%s)" --ttl 2h)
  engines=$(k get pods -l app.kubernetes.io/component=engine --field-selector=status.phase=Running \
  	-o jsonpath='{range .items[*]}{.metadata.labels.nexora\.io/group}={.status.podIP}@{.spec.nodeName},{end}')
  mgmts=$(k get pods -l app.kubernetes.io/component=mgmt --field-selector=status.phase=Running \
  	-o jsonpath='{range .items[*]}{.status.podIP}:8080,{end}')
  exec "$(dirname "$0")/dev-exec.sh" \
  	NEXORA_KW_API_URL=http://nexora-mgmt.nexora.svc:8080 \
  	NEXORA_KW_API_TOKEN="$token" \
  	NEXORA_KW_MGMT_ADDRS="${mgmts%,}" \
  	NEXORA_KW_ENGINES="${engines%,}" \
  	NEXORA_KW_GROUP_DNS=edge-a=192.168.10.136:53,edge-b=192.168.10.137:53 \
  	NEXORA_KW_INGRESS_IP=192.168.10.120 \
  	NEXORA_KW_FIXTURE_URL=http://kw-fixture-http.nexora.svc:8080 \
  	NEXORA_KW_JAEGER_QUERY_URL=http://jaeger.observability.svc:16686 \
  	NEXORA_KW_PROMETHEUS_URL=http://kps-prometheus.monitoring.svc:9090 \
  	go test ./e2e/ -run TestKwFullProduct -count=1 -v -timeout 45m
  ```
- [ ] Retire the M1 kw manifests: run `git ls-files deploy/kw`; for every file other than `values-kw.yaml`, `opensearch.yaml` and `fixture-http.yaml`, run `kubectl --context kw -n nexora delete -f <file> --ignore-not-found` except for the CNPG `Cluster` (keep its data and let Helm adopt it with `kubectl --context kw -n nexora annotate cluster.postgresql.cnpg.io/nexora-db meta.helm.sh/release-name=nexora meta.helm.sh/release-namespace=nexora --overwrite && kubectl --context kw -n nexora label cluster.postgresql.cnpg.io/nexora-db app.kubernetes.io/managed-by=Helm --overwrite`), then `git rm <file>`. Expect `git ls-files deploy/kw` to list exactly the three files.
- [ ] Build and push arm64 images for the current commit: `tag=sha-$(git rev-parse --short=7 HEAD); scripts/build-image.sh -f deploy/docker/engine.Dockerfile -n nexora-engine -t "$tag" && scripts/build-image.sh -f deploy/docker/mgmt.Dockerfile -n nexora-mgmt -t "$tag"` and expect two `pull as 192.168.10.131/azrtydxb/...:sha-...` lines.
- [ ] Deploy: `scripts/kw-deploy.sh "$tag"` and expect the final pod listing to show three `nexora-engine-*` pods `Running` on three different worker nodes; then `kubectl --context kw -n nexora get svc nexora-mgmt-grpc nexora-engine-edge-a nexora-engine-edge-b -o jsonpath='{range .items[*]}{.metadata.name}={.status.loadBalancer.ingress[0].ip}{"\n"}{end}'` and expect `nexora-mgmt-grpc=192.168.10.135`, `nexora-engine-edge-a=192.168.10.136`, `nexora-engine-edge-b=192.168.10.137`; then `kubectl --context kw -n nexora exec statefulset/opensearch -- curl -s localhost:9200/_cat/indices/nexora-querylog-*` after a few queries and expect at least one `nexora-querylog-YYYY.MM.DD` index.
- [ ] Run the acceptance: `scripts/kw-acceptance.sh` and expect `--- PASS: TestKwFullProduct` with every subtest `--- PASS` (engines need outbound DNS to the internet for the forwarding, recursion and DNSSEC subtests).
- [ ] Commit: `git add deploy/kw scripts/kw-deploy.sh scripts/kw-acceptance.sh e2e/kw_bodies.go e2e/kw_bodies_test.go e2e/kw_full_test.go && git commit -m "feat(kw): final fleet deployment from the chart and TestKwFullProduct"`.

## Task 15: Operations documentation and README

Files: `docs/operations.md` (install, upgrade, backup/restore, rollouts, lifecycle, monitoring, kw), `README.md` (product overview and quick start), `deploy/deploytest/docs_test.go` (`TestOperationsDoc`)
Interfaces: headings listed in the test; every repository path the docs mention must exist.

- [ ] Write the failing test `deploy/deploytest/docs_test.go`:
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
- [ ] Run `scripts/dev-exec.sh go test ./deploy/deploytest/ -run TestOperationsDoc -count=1` and expect FAIL with `no such file or directory`.
- [ ] Create `docs/operations.md`:
  ````markdown
  # Operating Nexora

  Nexora is one stateless management plane (`nexora-mgmt`, any number of
  replicas, all state in PostgreSQL) and any number of engines
  (`nexora-engine`) that dial out to it over mTLS. The design is in
  `docs/architecture.md`.

  ## Install with Helm

  Prerequisites: Kubernetes 1.28+, Helm 3.14+ or 4, and either the
  CloudNativePG operator (`database.mode=cnpg`) or a PostgreSQL 15+ URL in a
  secret (`database.mode=external`). Prometheus Operator CRDs are needed only
  when `metrics.serviceMonitor` or `metrics.prometheusRule` is enabled.

  1. Create the engine CA once and keep a copy offline; losing it means
     re-enrolling every engine.
     ```sh
     nexora-mgmt ca init --out ./nexora-ca
     kubectl -n nexora create secret generic nexora-ca --from-file=./nexora-ca/ca.crt --from-file=./nexora-ca/ca.key
     ```
  2. Install the management plane without engines:
     ```sh
     helm upgrade --install nexora deploy/helm/nexora -n nexora \
       --set mgmt.ca.existingSecret=nexora-ca --set engine.enabled=false --wait
     ```
  3. Create the first admin (there is no default password):
     ```sh
     kubectl -n nexora exec -i deploy/nexora-mgmt -c mgmt -- \
       nexora-mgmt user create --admin --username admin --password-file /dev/stdin
     ```
  4. Create a group and a join token per engine workload, stored as a secret:
     ```sh
     kubectl -n nexora exec deploy/nexora-mgmt -c mgmt -- nexora-mgmt group create --name edge --if-missing
     token=$(kubectl -n nexora exec deploy/nexora-mgmt -c mgmt -- nexora-mgmt join-token create --group edge --ttl 24h --max-uses 4)
     kubectl -n nexora create secret generic nexora-join-edge --from-literal=token="$token"
     ```
     `--max-uses` must cover every pod that may enroll before the token
     expires (replicas plus rescheduling); an enrolled engine never needs the
     token again because its identity lives under the state directory.
  5. Enable engines with `engine.groups[].joinTokenSecret` set and run the same
     `helm upgrade` without `engine.enabled=false`.

  Engine state: `engine.stateDir.type=hostPath` (default) keeps identity and
  last snapshot across pod restarts; `emptyDir` enrolls a new engine on every
  restart. `engine.kind=DaemonSet` with `engine.hostNetwork=true` serves DNS
  on each node's addresses. All values are validated by
  `deploy/helm/nexora/values.schema.json`.

  ## Install with Docker Compose

  ```sh
  cd deploy/compose
  cp .env.example .env            # set POSTGRES_PASSWORD and NEXORA_TAG
  docker compose up -d            # postgres, CA, migrations, mgmt
  docker compose exec -T mgmt nexora-mgmt user create --admin --username admin --password-file /dev/stdin <<<"choose-a-password"
  docker compose exec -T mgmt nexora-mgmt join-token create --group default --ttl 1h > secrets/join-token
  docker compose --profile engine up -d
  docker compose --profile otel up -d   # optional collector (config: otel-collector.yaml)
  ```

  ## Upgrade

  1. Read the release notes for migration notes.
  2. `helm upgrade` with the new `image.tag`. Every mgmt pod runs
     `nexora-mgmt migrate` as an init container; migrations take a Postgres
     session lock, so replicas never migrate concurrently. Management-plane
     pods roll one at a time (PodDisruptionBudget `minAvailable: 1`); engines
     keep serving and reconnect to a surviving instance.
  3. Engines roll with `maxUnavailable: 1` per group. During an engine upgrade
     the group's DNS Service keeps the other replicas in rotation.
  4. Check `/engines` (drift column) or
     `kubectl -n nexora exec deploy/nexora-mgmt -c mgmt -- wget -qO- localhost:8080/metrics | grep nexora_mgmt_engines_disconnected`.

  Rolling back the chart: `helm rollback nexora <revision>`. Migrations are
  forward-only in production; restore the pre-upgrade backup to go back past a
  migration.

  ## Backup and restore PostgreSQL

  Everything except query logs lives in PostgreSQL. Back up before every
  upgrade and on a schedule.

  Backup (CNPG primary, custom format):
  ```sh
  primary=$(kubectl -n nexora get cluster nexora-db -o jsonpath='{.status.currentPrimary}')
  kubectl -n nexora exec "$primary" -c postgres -- pg_dump -Fc -d nexora > nexora-$(date +%F).dump
  ```
  For continuous backups configure CNPG `spec.backup.barmanObjectStore` and a
  `ScheduledBackup` against your object store.

  Restore:
  ```sh
  kubectl -n nexora scale deploy/nexora-mgmt --replicas=0
  kubectl -n nexora exec -i "$primary" -c postgres -- pg_restore --clean --if-exists -d nexora < nexora-2026-09-13.dump
  ```
  Engines may now run config versions newer than the restored database.
  Before starting the management plane, move the version sequence past every
  version an engine reports (`max(nexora_config_version)` in Prometheus):
  ```sh
  kubectl -n nexora exec -i "$primary" -c postgres -- psql -d nexora <<'SQL'
  SELECT setval(pg_get_serial_sequence('config_versions', 'version'),
                GREATEST((SELECT max(version) FROM config_versions), 12345) + 1);
  UPDATE engine_groups SET rollouts_paused = true;
  SQL
  kubectl -n nexora scale deploy/nexora-mgmt --replicas=2
  ```
  (replace `12345` with the reported maximum), then call
  `POST /api/v1/engine-groups/{id}/resume-rollouts` for each group: it
  publishes the restored configuration as a new, higher version and the
  engines flagged `ahead` return to `in_sync`.

  ## Engine groups and staged rollouts

  - Every engine is in one group; config resources are global or belong to one
    group (Scope field in the GUI, `group_id` in the API). Upstreams follow the
    group's upstream mode (`inherit` or `override`).
  - A config change creates a pending version per affected group. Strategy
    `all_at_once` pushes to every engine; `canary` pushes to
    max(`canary_count`, `canary_percent`) engines (label
    `nexora.io/canary=true` first), waits until they apply it within
    `ack_timeout_seconds`, watches their SERVFAIL ratio for
    `health_window_seconds` (ignored below `min_health_queries` queries), then
    rolls to the rest.
  - A rejection, an ack timeout, a canary that stops reporting, or a SERVFAIL
    ratio above `max_servfail_ratio` halts the rollout; the canaries keep the
    new version and everyone else stays on the stable one. Alert
    `NexoraRolloutHalted` fires.
  - Fix forward by making another change (it supersedes the halted rollout), or
    roll back: `/engines/groups/<group>` -> `Roll back`, or
    `POST /api/v1/engine-groups/{id}/rollback {"to_version": N}`. Rollback
    republishes version N's snapshot as a new version to every engine at once
    and pauses change rollouts for the group, because the configuration rows
    still contain the rolled-back change. Correct the configuration, then
    `Resume rollouts` (`POST .../resume-rollouts`) to publish it.
  - Moving an engine to another group republishes that group's stable snapshot.

  ## Engine lifecycle

  - Join tokens (`/engines/groups/<group>` -> `New join token`, or
    `nexora-mgmt join-token create`) carry the group, labels, expiry (default
    24 h) and a use count (default 1). Revoke unused tokens.
  - Engine certificates live for `NEXORA_ENGINE_CERT_TTL` (default 90 days) and
    renew automatically from 2/3 of their lifetime over the control stream.
  - `Rotate certificate` forces a renewal now; the old serial is marked
    superseded once the engine reconnects with the new one.
  - `Revoke engine` rejects the engine's certificates on every management plane
    instance immediately; the engine keeps serving its last configuration and
    retries every 5 minutes. To re-admit the host, delete
    `/var/lib/nexora/<release>-<group>/identity` (hostPath) and give it a new
    join token; it enrolls as a new engine. `Delete engine` removes the record
    after revoking it.

  ## Monitoring and alerts

  - Scrape `/metrics` on mgmt (port `http`), engines (port `metrics`, headless
    Service `nexora-engine-metrics`) and the collector (`otel-metrics`); the
    chart's ServiceMonitor does this when `metrics.serviceMonitor.enabled`.
  - Fleet metrics: `nexora_mgmt_engines_disconnected`,
    `nexora_mgmt_engines{group,state}`, `nexora_mgmt_engine_drift{group,drift}`,
    `nexora_mgmt_rollouts{state}`; engine metrics are listed in
    `docs/architecture.md`.
  - Alerts (`metrics.prometheusRule.enabled`): `NexoraEngineDisconnected`
    (an engine unseen for more than 60 s), `NexoraRolloutHalted`,
    `NexoraManagementPlaneDown`.
  - Traces and query logs go to the OpenTelemetry Collector
    (`otelCollector.traces.otlpEndpoint`, `otelCollector.logs.opensearch.url`).

  ## kw deployment

  The lab deployment is `deploy/kw/values-kw.yaml` plus
  `deploy/kw/opensearch.yaml` and `deploy/kw/fixture-http.yaml`:

  | Component | Address |
  | --- | --- |
  | GUI and API | `https://nexora.kw.local` (ingress-nginx, ClusterIssuer `cluster-ca`) |
  | Engine gRPC | `192.168.10.135:9443` |
  | DNS group `edge-a` (2 engines) | `192.168.10.136` |
  | DNS group `edge-b` (1 engine, recursive) | `192.168.10.137` |
  | Traces | Jaeger `jaeger.observability:4317` |
  | Metrics | kube-prometheus-stack (`release: kps`) |

  ```sh
  tag=sha-$(git rev-parse --short=7 HEAD)
  scripts/build-image.sh -f deploy/docker/engine.Dockerfile -n nexora-engine -t "$tag"
  scripts/build-image.sh -f deploy/docker/mgmt.Dockerfile -n nexora-mgmt -t "$tag"
  scripts/kw-deploy.sh "$tag"
  scripts/kw-acceptance.sh     # TestKwFullProduct against the live deployment
  ```
  The admin password is in secret `nexora-admin`.
  ````
- [ ] Rewrite `README.md` with these sections, keeping any M1–M4 badge lines at the top:
  ```markdown
  # Nexora

  A fast, feature-complete DNS server: a Rust engine (forwarding, full
  recursion, DNSSEC validation and signing, authoritative zones with
  transfers and dynamic updates, blocklists, per-client policy, RPZ, DoT/DoH/DoQ)
  managed by a stateless Go management plane with a React GUI, from one box to
  a fleet of engines with staged, health-gated config rollouts.

  ## Quick start

  Docker Compose (single host): see `deploy/compose` and
  "Install with Docker Compose" in `docs/operations.md`.

  Kubernetes: the Helm chart in `deploy/helm/nexora`; see "Install with Helm" in
  `docs/operations.md`.

  ## Documentation

  - `docs/operations.md` — install, upgrade, backup and restore, rollouts, engine lifecycle, monitoring
  - `docs/architecture.md` — how Nexora is built
  - `.procoder/specs/nexora-v1.md` — what v1 delivers and why

  ## Development

  The engine is Linux-only; builds and tests run in the kw dev pod:
  `scripts/dev-exec.sh make build`, `scripts/dev-exec.sh make e2e`.
  Release images are built by `.github/workflows/images.yml`.
  ```
- [ ] Run `scripts/dev-exec.sh go test ./deploy/deploytest/ -count=1 -v` and expect `--- PASS` for `TestOperationsDoc`, `TestHelmTemplate`, `TestImagesWorkflow`, `TestComposeExample`.
- [ ] Run the whole milestone gate once more: `scripts/dev-exec.sh 'make lint && make engine-test && make mgmt-test && make web-test && go test ./e2e/ -run "TestFleetRolloutAndPartition|TestMgmtStatelessHA|TestCanaryRolloutHaltsOnFailure|TestEngineCertRevocation|TestGroupScopedConfig|TestJoinTokenGroupAndExpiry|TestFleetAPI|TestMgmtCLIFleet|TestGUIFleet|TestGUICoverage" -count=1 -timeout 60m'` and expect every target and test to pass; then `scripts/kw-acceptance.sh` and expect `--- PASS: TestKwFullProduct`.
- [ ] Commit: `git add docs/operations.md README.md deploy/deploytest/docs_test.go && git commit -m "docs: operations guide and README for the fleet release"`.

