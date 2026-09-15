# nexora-m9-platform

Status: complete

## Problem

Nexora runs on Kubernetes only through the Helm chart `deploy/helm/nexora`, and the chart leaves every
day-2 step to hand work:

- Somebody writes and maintains the values file.
- The CA and the key-encryption key are created with `nexora-mgmt ca init` and `openssl`.
- A first install runs in two phases: the management plane first, then `deploy/kw/bootstrap.sh` creates
  the join token Secret through the API, then the engines.
- Engine groups and join tokens exist only in the database, and nothing renews a join token before
  it expires.

kw shows the cost: `scripts/kw-deploy.sh` and `bootstrap.sh` together are about 400 lines of shell that
encode this order. PostgreSQL is equally manual. The chart renders a two-instance CloudNativePG
`Cluster` without backups, anti-affinity or a restore path. `docs/operations.md` says only "configure
`spec.backup.barmanObjectStore`" (issue #41), so an operator who loses the database volume loses every
zone, key and user.

Issues #37 and #41 were decided on 2026-09-14 (`.procoder/ask/decisions.md`):

- #37: a Kubernetes operator with CRDs for installations (management plane and engines) and for
  engine groups, while the Helm chart stays.
- #41: an optional CNPG HA cluster with backups in the Helm chart, plus docs, and no HA logic in the
  management plane.

The roadmap puts this in M9 Platform, after M6–M8.

## Users

- **Platform operator (Kubernetes admin):**
  - Installs Nexora by applying one `NexoraInstallation` and a few `NexoraEngineGroup` objects, from Git
    (Argo CD, Flux) or `kubectl`.
  - Expects no Helm values to maintain and no bootstrap script.
  - Expects engines that roll without losing queries, and status conditions that say what is not ready.
- **Helm user:** keeps using `deploy/helm/nexora` unchanged. Can turn on CNPG HA, scheduled backups to
  S3-compatible storage and a restore from backup through values.
- **Nexora admin (GUI/API):** sees engine groups created by the operator like any other group. Can still
  edit settings the CR does not manage. Sees the operator's system account in Users and cannot delete
  it by accident.
- **Lead / CI:** needs envtest and unit proof in CI, and one kw e2e run in a disposable namespace that
  can never touch the production release in `nexora`.

## In scope

- [S-1] **Operator module and packaging.**
  - A new Go module `github.com/piwi3910/nexora/operator` in `operator/`, built on controller-runtime
    v0.25.0 and k8s.io v0.37.0, with the binary `nexora-operator`.
  - Image `nexora-operator` from `deploy/docker/operator.Dockerfile`. It is built by
    `scripts/build-image.sh` and `.github/workflows/images.yml`, stamps `VERSION`/`COMMIT`/`BUILD_DATE`,
    and contains the Nexora chart at `/charts/nexora`.
  - Installable two ways:
    - the new chart `deploy/helm/nexora-operator`, with CRDs in its `crds/` directory, scope `cluster`
      (ClusterRole) or `namespace` (a Role per watched namespace), and leader election;
    - the plain manifests `deploy/operator/crds/` and `deploy/operator/operator.yaml` (namespace
      `nexora-operator`, cluster scope), generated from that chart.
  - Design choice: a separate operator chart instead of an `operator.enabled` switch in
    `deploy/helm/nexora`. Helm installs a chart's `crds/` on every install, and existing releases (kw
    production) must not gain CRDs or cluster RBAC on upgrade.
- [S-2] **`NexoraInstallation` CRD** (`nexora.io/v1alpha1`, namespaced, short name `nxi`).
  - `spec` mirrors the chart values with identical JSON names: `image`, `imagePullSecrets`, `mgmt`,
    `database`, `engine`, `otelCollector`, `metrics`. A reader of `values.yaml` can write a CR without a
    translation table.
  - The operator owns and injects these values, so the CRD omits them: `mgmt.bootstrapToken`,
    `engine.groups[].joinTokenSecret`, `engine.groups[].joinTokenKey`, and
    `metrics.serviceMonitor.namespace` / `metrics.prometheusRule.namespace` (always the installation
    namespace).
  - `engine.groups[].engineGroupRef` (default: the group `name`) names the `NexoraEngineGroup` whose
    join token Secret the group's engines use.
  - CEL validation rules reject what the chart's `required`/`fail` would reject when the rule does not
    depend on other objects:
    - `database.mode=external` without `database.external.existingSecret`;
    - an instance without `node`;
    - duplicate group or instance names;
    - `engine.kind` other than `DaemonSet`/`Deployment`;
    - `mgmt.querylog.backend=opensearch` without a URL;
    - `database.cnpg.backup.enabled` without `destinationPath` or credentials.
- [S-3] **Installation reconcile.**
  - The operator renders the Nexora chart in-process with the Helm v3.21.4 SDK
    (`chart/loader`, `chartutil.ToRenderValues`, `engine.Render`):
    - release name = CR name, namespace = CR namespace, `Release.Service` = `nexora-operator`;
    - API versions taken from discovery;
    - values = the CR `spec` plus injected values.
  - Each rendered object gets the label `nexora.io/installation: <name>` and is server-side applied
    with field manager `nexora-operator` and `force: true`.
  - Every object gets a controller owner reference to the CR, except the CNPG `Cluster`.
  - Objects labelled for this installation that are no longer rendered are deleted (pruning), except
    CNPG `Cluster` objects.
  - Design choice: rendering the chart instead of hand-written Go builders.
    - The operator's Deployments, DaemonSets, Services, PDB, Ingress and CNPG objects are then the
      chart's by construction.
    - The zero-downtime rolling strategy, node pinning of instances, probes and security contexts
      cannot drift.
    - `TestHelmTemplate` stays the single place that proves object shapes.
  - A render error (a chart `required`/`fail`) sets `Rendered=False` with the chart message and changes
    no object.
  - An object that would land in another namespace is refused.
- [S-4] **Operator-generated key material.**
  - When `mgmt.ca.existingSecret` is empty, the operator creates Secret `<name>-ca`: ECDSA P-256,
    self-signed, CN `Nexora CA`, valid 10 years, keys `ca.crt` and `ca.key`, the format
    `pki.InitCA` writes.
  - When `mgmt.kek.existingSecret` is empty, it creates `<name>-kek`: key `kek`, 32 random bytes,
    base64.
  - It always creates `<name>-operator-token`: key `token`, `nxt_` plus unpadded base32 of 32 random
    bytes.
  - These Secrets carry the installation label but no owner reference. They are never overwritten or
    deleted by the operator, so deleting the CR keeps the keys.
  - A pre-existing Secret of that name that lacks a key is reported (`Rendered=False`, reason
    `SecretIncomplete`), never regenerated.
- [S-5] **Engines, join tokens and zero-downtime updates.**
  - A group whose referenced `NexoraEngineGroup` has no ready join token Secret renders no engine
    workload. Its condition is `EnginesReady=False` with reason `JoinTokenPending`, while the management
    plane and other groups render. A fresh install therefore needs no second phase.
  - Once the join token is ready, the group renders with `joinTokenSecret` = the engine group's
    `status.joinTokenSecret`.
  - Engine instances pinned to nodes, `extraServices`, `hostNetwork`, `stateDir`, the rolling
    strategies (`maxSurge: 1, maxUnavailable: 0`, or `0/1` with hostNetwork), `minReadySeconds`,
    `shutdownDrainSeconds` and config checksums come from the chart unchanged.
  - A spec change rolls exactly like `helm upgrade`, proven on kw with zero lost queries.
- [S-6] **Installation status.** `status.conditions`:
  - `Rendered`.
  - `DatabaseReady`: the CNPG `Cluster` condition `Ready=True`, or the external Secret exists.
  - `ManagementReady`: the mgmt Deployment has at least one available replica and
    `GET <managementURL>/api/v1/health` answers 200 with the operator token.
  - `SetupRequired`: `GET /api/v1/setup` reports `required`, which tells a human to finish first-run
    setup.
  - `EnginesReady`: every engine workload is fully updated and ready (reasons `JoinTokenPending`,
    `RollingUpdate`, `Unavailable`).
  - `Ready`: `Rendered`, `DatabaseReady`, `ManagementReady` and `EnginesReady` are all true.
  - Also `status.version` (the image tag), `status.managementURL`
    (`http://<prefix>-mgmt.<namespace>.svc:8080`), `status.secrets` (the CA, KEK and operator token
    Secret names), `status.workloads[]` (`kind`, `name`, `desired`, `ready`, `updated`) and
    `observedGeneration`.
  - The image tag defaults to the operator's stamped version. An unstamped operator (`dev`) with an
    empty `spec.image.tag` gives `Rendered=False` with reason `ImageTagRequired`.
  - Status refreshes on owned-object events and every 60 s.
- [S-7] **`NexoraEngineGroup` CRD** (`nexora.io/v1alpha1`, namespaced, short name `nxeg`) **and its
  reconcile through the management plane API.**
  - Spec:
    - `installationRef.name` (same namespace, immutable) and `groupName` (default `metadata.name`,
      immutable, the `EngineGroupInput.name` pattern of `mgmt/api/openapi.yaml`);
    - optional `description`, `upstreamMode`, `extraACLCIDRs`, `otlpEndpoint`, `filterIndexMaxBytes`;
    - `rollout` (`strategy`, `canaryCount`, `canaryPercent`, `ackTimeoutSeconds`,
      `healthWindowSeconds`, `maxServfailRatio` as a decimal string, `minHealthQueries`);
    - `joinToken`;
    - `deletionPolicy` (`Retain` default, or `Delete`).
  - The controller calls the API at the installation's `status.managementURL` with the operator token:
    `listEngineGroups`, `createEngineGroup`, `updateEngineGroup` with `revision`, `deleteEngineGroup`,
    `listJoinTokens`, `createJoinToken` and `revokeJoinToken`.
  - It creates the group when missing and adopts an existing group of that name (including `default`).
  - Design choice: only fields set in the CR are managed. An unset field keeps whatever the GUI or API
    set, so an admin's canary tuning is not reverted.
  - A 409 conflict re-reads and retries.
  - `status.groupID`, `revision`, `engineCount` and the conditions `Synced`, `JoinTokenReady` and
    `Ready` report the result.
  - Two CRs with the same `groupName` for one installation: the later one (by creation time) gets
    `Synced=False` with reason `DuplicateGroupName`.
  - The finalizer `nexora.io/engine-group` handles deletion:
    - `Retain` revokes the join tokens the CR created and keeps the group;
    - `Delete` also deletes the group. A non-empty group (409) keeps the finalizer with reason
      `DeletionBlocked` and retries;
    - the group `default` is never deleted;
    - when the installation no longer exists, the finalizer is removed without API calls.
- [S-8] **Join token lifecycle.**
  - `joinToken` settings: `secretName` (default `<cr name>-join-token`), `ttl` (default `8760h`, range
    1m–8760h), `renewBefore` (default `720h`, less than `ttl`), `revokeGracePeriod` (default `10m`),
    `maxUses` (optional), `labels` (optional, at most 32).
  - The controller creates a token named `op/<cr uid>/<unix seconds>/<namespace>/<cr name>`, truncated to the
    API's 64-character limit, and writes it to the Secret key `join-token`. That Secret is owned by the
    CR.
  - It creates a new token when the Secret is missing, when the recorded token is not `active`, or when
    it expires within `renewBefore`.
  - The previous token is revoked once `revokeGracePeriod` has passed. The kubelet refreshes Secret
    volumes within about a minute, so a pod enrolling during a rotation still holds a valid token.
  - Engines that already enrolled never read the token again.
- [S-9] **Management plane bootstrap token and system users** (the only management-plane change).
  - `NEXORA_BOOTSTRAP_TOKEN_FILE` (optional) names a file holding an `nxt_` token.
    `NEXORA_BOOTSTRAP_TOKEN_RELOAD_INTERVAL` defaults to `30s`.
  - At start and every interval, under `pg_advisory_xact_lock(hashtext('nexora:bootstrap_token'))`, the
    instance ensures that:
    - the user `nexora-operator` exists with source `system`, role `admin`, no password and not
      disabled;
    - one unrevoked API token named `bootstrap` with that token's hash and role `admin` exists;
    - any other unrevoked `bootstrap` token of that user is revoked.
  - An actual change writes audit action `ensureBootstrapToken` with actor `system`/`bootstrap`, without
    the token.
  - Migration `mgmt/migrations/01000_system_users.sql` allows `users.source = 'system'`.
  - Setup-required checks (`EnsureSetupToken`, `SetupRequired`, `CompleteSetup`) count only
    non-system users. First-run setup and the setup token therefore still appear for humans.
  - `updateUser` and `deleteUser` on a system user return 409 `system_user`. Login as a system user
    fails like a wrong password.
  - `User.source` gains `system` in `mgmt/api/openapi.yaml`. The Users page shows "System" and no edit
    or delete actions for such a row.
  - The chart gains `mgmt.bootstrapToken.existingSecret` (key `token`). It mounts the Secret at
    `/etc/nexora/bootstrap-token` (mode 0440) and sets the env. The operator sets it to
    `<name>-operator-token`.
  - Design choice: this replaces a human creating an API token before the operator can manage engine
    groups. The token is generated by the operator, lives only in a Secret, and is never logged.
- [S-10] **CNPG high availability in the Helm chart.** New values under `database.cnpg`:
  - `instances` (default stays 2);
  - `antiAffinity` (`preferred` default or `required`), rendered as `spec.affinity`
    `{enablePodAntiAffinity: true, topologyKey: kubernetes.io/hostname, podAntiAffinityType}`;
  - `primaryUpdateMethod` (`switchover` default or `restart`), with
    `primaryUpdateStrategy: unsupervised`;
  - `resources`;
  - `postgresql.parameters` (a string map rendered as `spec.postgresql.parameters`).
  - The management plane keeps connecting through the `<clusterName>-app` Secret URI (the `-rw`
    Service), so failover needs no management-plane logic.
  - CNPG creates its own PodDisruptionBudgets.
- [S-11] **CNPG backups and restore in the Helm chart.**
  - `database.cnpg.backup` renders `spec.backup.barmanObjectStore` and `spec.backup.retentionPolicy`, plus
    a `ScheduledBackup` named `<clusterName>-scheduled` (`method: barmanObjectStore`,
    `backupOwnerReference: self`, `schedule`, `immediate`). Its values:
    - `enabled`;
    - `destinationPath` (`s3://…`);
    - `endpointURL`;
    - `serverName` (default `clusterName`);
    - `s3Credentials.existingSecret`, `accessKeyIdKey` (default `ACCESS_KEY_ID`) and
      `secretAccessKeyKey` (default `ACCESS_SECRET_KEY`);
    - `endpointCA.existingSecret` and `key`;
    - `retentionPolicy` (default `30d`);
    - `walCompression` and `dataCompression` (default `gzip`);
    - `schedule` (six-field cron, default `0 0 3 * * *`);
    - `immediate` (default `false`).
  - `database.cnpg.recovery` renders `bootstrap.recovery` (`source: backup-source`, `database: nexora`,
    `owner: nexora`, optional `recoveryTarget.targetTime`) and `externalClusters[0]`
    (`name: backup-source`, a `barmanObjectStore` with `serverName: sourceServerName`) instead of
    `bootstrap.initdb`. Its values:
    - `enabled`;
    - `sourceServerName` (required);
    - `destinationPath`, `endpointURL`, `s3Credentials` and `endpointCA`, each defaulting to the backup
      values;
    - `targetTime`.
  - Rendering fails when recovery and backup would archive into the same `destinationPath` and
    `serverName`.
  - Design choice: the in-tree `barmanObjectStore`. kw runs CNPG 1.29.1, where it is present (deprecated),
    and the Barman Cloud plugin is not installed. A `debt:` comment in `database-cnpg.yaml` names the
    plugin migration and its trigger (a CNPG release that removes the field, or the plugin installed on
    kw).
  - The `NexoraInstallation` CRD exposes the same `database.cnpg` fields ([S-2]). Documented CNPG
    requirements: operator 1.25 or newer, and an image that ships `barman-cloud`.
- [S-12] **Documentation.**
  - `docs/operations.md` gains two sections:
    - `## Install with the Kubernetes operator`: install paths, a complete CR example, engine groups
      and join tokens, conditions, upgrades, deletion and retained state;
    - `## PostgreSQL high availability and backups`: HA values, backup to S3/MinIO, restore into a new
      cluster, failover behaviour, and what stays manual.
  - `docs/architecture.md` gains `## Platform (M9)`: operator layout, CRDs, the render/apply/prune loop,
    the bootstrap token, and CNPG values.
  - `deploy/kw/README.md` gains `## Operator e2e (namespace nexora-optest)`.
  - `TestOperationsDoc` requires both new headings.
- [S-13] **kw end-to-end proof in a disposable namespace.**
  - `scripts/kw-operator-e2e.sh [--tag TAG] [--skip-build] [--keep]`:
    1. builds and pushes `nexora-engine`, `nexora-mgmt` and `nexora-operator` at `sha-<7>`;
    2. creates namespace `nexora-optest` with the label `nexora.io/e2e=operator`, and refuses to reuse an
       unlabelled namespace;
    3. server-side applies `deploy/operator/crds/`;
    4. installs `deploy/helm/nexora-operator` as release `nexora-operator` in `nexora-optest` with
       `rbac.scope=namespace` and `watchNamespaces={nexora-optest}`;
    5. copies the MinIO credentials into Secret `optest-s3` and creates bucket `nexora-optest`
       (`curl --aws-sigv4` from the probe pod);
    6. runs `go test -tags kwe2e ./test/kw -run TestKwOperator` from `operator/`;
    7. unless `--keep`, removes the S3 prefix, the node state directories (a Job per node) and the
       namespace.
  - The CRDs stay installed, because they are cluster-scoped and no production object uses them.
  - The test uses engine nodes `worker-21`, `worker-22` and `worker-23`, ClusterIP Services only,
    hostPath prefix `/var/lib/nexora-optest`, and a probe pod running
    `192.168.10.131/azrtydxb/nexora-dev:toolbox-1` for dnsperf, curl and psql.
- [S-14] **Production continuity.**
  - `helm template` of `deploy/helm/nexora` with `deploy/kw/values-kw.yaml` renders byte-identical
    output before and after M9.
  - After the milestone, kw production gets the regular roadmap deploy: `scripts/kw-deploy.sh` with the
    DNS probe, then `scripts/kw-acceptance.sh`. It stays a Helm release; the operator e2e never runs
    there.

## Out of scope

- Declarative configuration CRDs (zones, filter lists, policies, upstreams synced into the management
  plane; the second #37 option, not chosen).
- Moving kw production (namespace `nexora`) or any existing Helm release to a `NexoraInstallation`, or
  adopting Helm-owned objects.
- Enabling backups or changing the CNPG cluster of kw production (`deploy/kw/cnpg-cluster.yaml`).
- The Barman Cloud plugin, volume-snapshot backups and point-in-time restore beyond `targetTime`.
- Any HA, failover or replica-routing logic in the management plane (#41 decision).
- Objects outside the installation namespace, such as a ServiceMonitor in `monitoring`. Prometheus must
  select ServiceMonitors in the installation namespace.
- Admission or conversion webhooks, OLM bundles or OperatorHub, and CRD versions other than
  `v1alpha1`.
- `NexoraEngineGroup` targeting a Helm-installed or external management plane (it references a
  `NexoraInstallation` only).
- Operator-issued DNS TLS certificates (`mgmt.dnsTLS.existingSecret` stays user-provided) and creating
  the first human admin (first-run setup stays manual and is surfaced by `SetupRequired`).
- Engine autoscaling, multi-cluster installations, and a NetworkPolicy around the operator-to-mgmt
  HTTP call.

## Constraints

- Never change the production namespace `nexora`, the Helm release `nexora`, or the addresses
  192.168.10.136 and 192.168.10.139.
  - The kw e2e runs only in `nexora-optest`. The script and the test refuse any other namespace.
  - The test fails before creating objects when a rendered Service has `type: LoadBalancer` or a
    `loadBalancerIP`.
  - It never schedules engines on `master-12` or `master-13`.
- Migrations added by M9 use `01000`–`01009`; M9 adds only `01000_system_users.sql`. Proto fields
  added by M9 use `1000`–`1099`; M9 adds none.
- The engine is unchanged. Hot-path rules are unaffected.
- The management plane stays stateless and gains no HA logic. The bootstrap token loop is idempotent
  across instances through the advisory lock.
- Every existing Go, Rust and Playwright test keeps passing. `TestHelmTemplate` is changed only to
  add cases. `TestGUICoverage` needs no new screen because no operation is added.
- Chart compatibility: new values default to the current behaviour. The kw values render stays
  byte-identical ([S-14]). Default-values renders gain only the explicit CNPG affinity and update-method
  fields, equal to CNPG's defaults.
- Operator module:
  - Go 1.27, controller-runtime v0.25.0, k8s.io/* v0.37.0, helm.sh/helm/v3 v3.21.4,
    github.com/oapi-codegen/runtime v1.7.0;
  - controller-gen v0.20.1 and envtest assets for Kubernetes 1.34 (kw runs 1.34.4), fetched by
    setup-envtest v0.25.0;
  - Kubernetes 1.28 or newer (CEL validation), the chart's floor.
  - The root module's `go.mod` gains no Kubernetes dependency.
- Security:
  - Secrets never appear in logs, events, conditions or audit rows.
  - The operator's RBAC covers only the kinds the chart renders plus Secrets, Leases, Events and its
    own CRDs.
  - Containers run non-root with read-only root filesystems and drop all capabilities.
  - The operator calls the management plane over in-cluster HTTP on port 8080, the same cleartext
    in-cluster path the ingress uses. The bearer token stays inside the pod network.
- Generated files are committed and checked for drift in CI:
  - CRDs, deepcopy code and `deploy/operator/operator.yaml`;
  - the operator API client `operator/internal/mgmtapi/client.gen.go`, generated from
    `mgmt/api/openapi.yaml`.
- The roadmap's deploy rule applies to the final production deploy: roll back on any lost probe query
  or failed acceptance test.

## Interfaces

- **CRD `nexorainstallations.nexora.io`** (kind `NexoraInstallation`, v1alpha1, short name `nxi`,
  category `nexora`; printer columns Ready, Version, Age):
  - `spec`: `image{registry,tag,pullPolicy}`, `imagePullSecrets[]` and `mgmt`. `mgmt` covers
    `replicas`, `publicURL`, `secureCookies`, `engineCertTTL`, `rolloutTick`, `grpcServerNames`,
    `otlpEndpoint`, `ca.existingSecret`, `kek.existingSecret`, `dnsTLS{existingSecret,reloadInterval}`,
    `querylog{backend,builtinCapacity,opensearch{url,index}}`, `extraEnv`, `extraVolumes`,
    `extraVolumeMounts`, `resources`, `grpcLoadBalancer{enabled,loadBalancerIP}`,
    `ingress{enabled,name,className,host,clusterIssuer,tlsSecretName}` and `pdb.minAvailable`.
  - `spec.database`: `mode`, `external{existingSecret,key}`, and `cnpg`. `cnpg` covers `clusterName`,
    `instances`, `imageName`, `storageClass`, `size`, `resources`, `antiAffinity`,
    `primaryUpdateMethod`, `postgresql.parameters`, `backup{…}` and `recovery{…}`.
  - `spec.engine`: `enabled`, `kind`, `managementURL`, `workers`, `initImage`, `hostNetwork`,
    `ports{dns,metrics,dot,doh,doq}`, `dohPath`, `stateDir{type,hostPathPrefix}`, `tolerations`,
    `shutdownDrainSeconds`, `minReadySeconds`, `resources`, and `groups[]`. Each group has `name`,
    `engineGroupRef`, `workloadName`, `nodeNamePrefix`, `replicas`, `nodeAffinity`,
    `service{name,type,loadBalancerIP,externalTrafficPolicy}`, `extraServices[]` and
    `instances[]{name,node,service}`.
  - `spec.otelCollector{enabled,image,resources,config}` and
    `spec.metrics{serviceMonitor{enabled,labels,interval},prometheusRule{enabled,labels}}`.
  - `status`: `observedGeneration`, `version`, `managementURL`,
    `secrets{ca,kek,operatorToken}`, `workloads[]{kind,name,desired,ready,updated}` and `conditions[]`.
- **CRD `nexoraenginegroups.nexora.io`** (kind `NexoraEngineGroup`, v1alpha1, short name `nxeg`,
  category `nexora`; printer columns Group, Ready, Engines, Age):
  - `spec`: `installationRef{name}`, `groupName`, `description`, `upstreamMode`, `extraACLCIDRs`,
    `otlpEndpoint`, `filterIndexMaxBytes`, `rollout{…}`, `deletionPolicy`, and
    `joinToken{secretName,ttl,renewBefore,revokeGracePeriod,maxUses,labels}`. The `rollout` fields are
    `strategy`, `canaryCount`, `canaryPercent`, `ackTimeoutSeconds`, `healthWindowSeconds`,
    `maxServfailRatio` and `minHealthQueries`.
  - `status`: `observedGeneration`, `groupID`, `revision`, `engineCount`, `joinTokenSecret`,
    `joinTokenID`, `joinTokenExpiresAt`, `previousJoinTokenID`, `previousJoinTokenRevokeAt` and
    `conditions[]`.
- **Labels, annotations and finalizers:** `nexora.io/installation: <name>` on every managed object,
  and the finalizer `nexora.io/engine-group`. The chart's `app.kubernetes.io/managed-by` becomes
  `nexora-operator`.
- **Condition types and reasons:**
  - Types: `Rendered`, `DatabaseReady`, `ManagementReady`, `SetupRequired`, `EnginesReady`, `Ready`,
    `Synced`, `JoinTokenReady`.
  - Reasons: `RenderFailed`, `ImageTagRequired`, `SecretIncomplete`, `ForeignNamespace`,
    `JoinTokenPending`, `RollingUpdate`, `Unavailable`, `ManagementUnavailable`, `Unauthorized`,
    `Conflict`, `DuplicateGroupName`, `DeletionBlocked`, `Reconciled`.
- **Operator binary** `nexora-operator` flags:
  - `--chart-dir` (default `/charts/nexora`);
  - `--watch-namespaces` (comma-separated; empty = all);
  - `--leader-elect` (default `true`) and `--leader-election-namespace`;
  - `--metrics-bind-address` (default `:8080`) and `--health-probe-bind-address` (default `:8081`);
  - `--resync-interval` (default `60s`);
  - `version` subcommand.
- **Charts:**
  - `deploy/helm/nexora` values: `mgmt.bootstrapToken.existingSecret`,
    `database.cnpg.{resources,antiAffinity,primaryUpdateMethod,postgresql.parameters,backup.*,recovery.*}`,
    added to `values.schema.json`.
  - `deploy/helm/nexora-operator` values: `image{registry,tag,pullPolicy}`, `imagePullSecrets`,
    `replicas`, `rbac.scope` (`cluster`|`namespace`), `watchNamespaces`, `resources`, `leaderElection`.
- **Management plane:**
  - Env `NEXORA_BOOTSTRAP_TOKEN_FILE` and `NEXORA_BOOTSTRAP_TOKEN_RELOAD_INTERVAL`.
  - Audit action `ensureBootstrapToken`. Error code `system_user` (409).
  - `User.source` enum `[local, oidc, system]`.
  - Metric `nexora_mgmt_bootstrap_token_errors_total`.
- **Images and scripts:**
  - Image `nexora-operator` (`deploy/docker/operator.Dockerfile`) and the `images.yml` matrix entry.
  - `scripts/kw-operator-e2e.sh`.
  - Make targets `operator-generate` and `operator-test`, and the CI job `operator`.
- **GUI:** the Users page shows the source label "System" and hides edit/delete for system users.

## Data

- **PostgreSQL** (owned by the management plane):
  - migration `01000_system_users.sql` widens `users_source_check` to `('local','oidc','system')`;
  - one `users` row `nexora-operator` (source `system`, role `admin`, `password_hash` NULL);
  - `api_tokens` rows named `bootstrap` for that user, with at most one unrevoked;
  - existing rows are unchanged.
- **Kubernetes Secrets** in the installation namespace:
  - owned by the operator, retained (no owner reference): `<name>-ca` (`ca.crt`, `ca.key`),
    `<name>-kek` (`kek`), `<name>-operator-token` (`token`);
  - owned by a `NexoraEngineGroup` (owner reference, deleted with it): `<joinToken.secretName>`
    (`join-token`);
  - generated by CNPG: `<clusterName>-app`;
  - user-provided: S3 credentials and the optional endpoint CA.
- **Custom resources:** desired state in `spec`, observed state in `status`. The operator stores nothing
  else. Management-plane ids (`groupID`, `joinTokenID`) are cached in status and re-resolved by name
  when missing.
- **Object storage:** CNPG writes base backups and WAL under
  `<destinationPath>/<serverName>/{base,wals}`. The kw e2e uses `s3://nexora-optest/<run id>` on the
  kw MinIO (`http://minio.minio.svc.cluster.local:9000`), deleted after the run.
- **Engine nodes:** in the e2e, hostPath `/var/lib/nexora-optest/<group workload>` on `worker-21`,
  `worker-22` and `worker-23`, removed after the run.

## Edge cases

- **CR names:**
  - A CR name without `nexora` gets prefix `<name>-nexora`, matching the chart. A 50-character
    truncation is inherited.
  - Two installations in one namespace have distinct prefixes and labels. Pruning filters by the
    installation label, so they never delete each other's objects.
- **Moving a group to `instances`:** the rendered workload and Service names change, so pruning deletes
  the old workload after applying the new ones. The address is not served between old removal and new
  readiness (documented, as for the chart).
- **Changing a selector** or other immutable field on an existing name fails the apply. The installation
  reports `Rendered=False` with reason `RenderFailed` and the API message, and keeps the object as it was.
- **Mode changes:** `database.mode` from `cnpg` to `external` stops rendering the `Cluster` but never
  deletes it. Switching back re-applies the same name.
- **Missing engine group:** `engineGroupRef` pointing to a missing `NexoraEngineGroup` gives
  `JoinTokenPending` until the CR appears. Deleting the engine group CR while its group is rendered
  stops rendering that group's workloads (`JoinTokenPending`).
- **Adopting `default`:** a `groupName: default` CR adopts the built-in group, never renames or deletes
  it, and applies only set fields.
- **Existing join token Secret:** a `joinToken.secretName` Secret that exists but is not owned by this CR
  is not overwritten; the CR reports `JoinTokenReady=False` with reason `Conflict`.
- **Rotation during enrollment:** the old token stays valid for `revokeGracePeriod`. `ttl` shorter than
  `renewBefore` is refused by CEL.
- **Operator token deleted** from the database or revoked in the GUI: the management plane recreates it
  within one reload interval, and the operator sees `Unauthorized` at most that long.
- **A human user named `nexora-operator`** (local or OIDC): the bootstrap loop leaves it alone, logs
  `bootstrap token: user nexora-operator exists and is not a system user` and counts an error. The
  operator reports `ManagementReady=False` with reason `Unauthorized`.
- **Token file content:** an empty file or a file without the `nxt_` prefix is an error (logged without
  the content, counted); existing tokens stay.
- **Only the system user exists:** setup is still required and the setup token is still logged.
- **Last human admin:** the system user never counts as "another enabled admin", so the last human admin
  still cannot be demoted, disabled or deleted.
- **Recovery collision:** `recovery.sourceServerName` equal to the effective backup `serverName` with
  the same `destinationPath` fails rendering (WAL archive collision).
- **Replica count:** `instances: 1` with backups is allowed, with no HA; the docs say so. `required`
  anti-affinity with fewer schedulable nodes than instances leaves pods Pending, and
  `DatabaseReady=False` shows it.
- **Metrics namespace:** `metrics.serviceMonitor.enabled` renders in the installation namespace only.
- **Unstamped operator:** a `dev` operator build without `spec.image.tag` does not guess a tag.
- **Watch scope:** with `--watch-namespaces`, CRs in other namespaces are ignored, never half-reconciled.

## Failure modes

- **Kubernetes API unavailable or conflicting:** controller-runtime requeues with backoff, and nothing
  is deleted on a failed list (pruning runs only after a successful apply of every rendered object).
- **Chart render error:** `Rendered=False` with reason `RenderFailed` and the chart message; objects
  are untouched.
- **CNPG operator missing** with `database.mode=cnpg`: the chart's own failure message appears in
  `Rendered` (discovery lacks `postgresql.cnpg.io/v1`).
- **Database not ready:** `DatabaseReady=False`. Mgmt pods wait in their `migrate` init container as
  with the chart, and engine groups report `ManagementUnavailable`.
- **Primary failure (CNPG):** CNPG promotes a replica, and the `-rw` Service follows. The management
  plane's pool and its LISTEN connections reconnect (existing reconnect loops); requests fail with 503
  during the switchover. The kw e2e bounds the recovery.
- **Management plane unreachable or 5xx:** engine group reconcile requeues after 10 s with reason
  `ManagementUnavailable`. Existing groups, tokens and Secrets are untouched.
- **401 from the management plane:** `Unauthorized` on both CRs, retried every 30 s (the token loop
  converges within its interval).
- **Backup destination unreachable or bad credentials:** the CNPG `Cluster` reports
  `ContinuousArchiving=False` and the `Backup` object fails. Nexora keeps serving, and the docs name
  the checks (`kubectl get backup`, cluster conditions).
- **Postgres image without `barman-cloud`:** archiving fails as above, and the docs name the image
  requirement.
- **Operator down:** rendered workloads keep running and nothing is pruned. Join tokens are not rotated
  (engines already enrolled are unaffected). Rotation catches up on restart, since `renewBefore`
  defaults to 30 days.
- **Leader election lost:** the replica stops reconciling, and another replica takes over.
- **S3 bucket or node cleanup failure** in the e2e script: the script exits non-zero after printing the
  leftovers. The namespace is still deleted.

## Acceptance criteria

- [ ] [S-1] Go test `TestOperatorChart` in `deploy/deploytest` renders `deploy/helm/nexora-operator`:
  - With scope `cluster`: ClusterRole plus ClusterRoleBinding, no `--watch-namespaces`.
  - With scope `namespace` and `watchNamespaces=[a,b]`: a Role and RoleBinding in `a` and `b`, the
    flag `--watch-namespaces=a,b`, and a leader-election Role on `leases` in the release namespace.
  - A non-root, read-only-root container running `<registry>/nexora-operator:<tag>` with
    `--chart-dir=/charts/nexora`.

  `TestOperatorManifestsMatchChart` asserts that `deploy/operator/operator.yaml` equals the cluster-scope
  render and that `deploy/operator/crds/` equals the chart's `crds/` byte for byte.
  `TestImagesWorkflow` and `TestDockerfilesStampBuildInfo` assert the operator image matrix entry, the
  build arguments and the chart copy to `/charts/nexora`. Fails if a scope renders the wrong RBAC, the
  manifests drift from the chart, or the image lacks the chart or build stamp.

- [ ] [S-1] [S-3] Envtest `TestOperatorRBACCoversManagedKinds` (`operator/internal/controller/installation`)
      requires every group/resource in `render.ManagedKinds` to have `get, list, watch, create, patch,
delete` in the ClusterRole of `deploy/operator/operator.yaml`, and Secrets and Events to have their
      verbs. `TestOperatorManifestsApplyToAPIServer` server-side applies `deploy/operator/crds/` and
      `deploy/operator/operator.yaml` to envtest. The CI job `operator` runs `make operator-generate`
      and fails on a diff. Fails if a rendered kind is missing from RBAC, a manifest is rejected, or
      generated files are stale.
- [ ] [S-2] Envtest `TestInstallationCRDValidation` (`operator/api/v1alpha1`) accepts
      `operator/config/samples/nexora_v1alpha1_nexorainstallation.yaml` and
      `operator/internal/render/testdata/kw-installation.yaml`. It rejects, each with its CEL message:
  - external mode without a Secret;
  - an instance without `node`;
  - duplicate instance names;
  - `engine.kind: StatefulSet`;
  - opensearch without a URL;
  - backup enabled without `destinationPath`.

  `TestEngineGroupCRDValidation` rejects `ttl: 30s`, `renewBefore >= ttl`, a changed `groupName` or
  `installationRef`, and an invalid group name. Fails if an invalid object is admitted or a valid
  sample is refused.

- [ ] [S-2] [S-3] [S-14] Go test `TestValuesFromKwEquivalentInstallation` (`operator/internal/render`)
      converts `testdata/kw-installation.yaml`, with injected `nexora-ca`, `nexora-kek` and
      `nexora-join-token`, into values. It deep-equals `deploy/kw/values-kw.yaml` with
      `metrics.*.namespace` removed from both. Fails if the CRD cannot express a production value or
      renames one.
- [ ] [S-3] Go test `TestRenderMatchesHelmTemplate` (`operator/internal/render`, run in the dev pod with
      `helm`) renders three value sets through `render.Render` and through `helm template`: kw-equivalent,
      CNPG with backup, and instances with hostNetwork. After removing `nexora.io/installation`, owner
      references and the `managed-by` label value, both yield the same objects.

  `TestRenderAddsOwnershipExceptRetainedKinds` asserts the installation label on every object and a
  controller owner reference on every object except `postgresql.cnpg.io/Cluster`.
  `TestRenderRejectsForeignNamespace` asserts `ErrForeignNamespace` naming the object.
  `TestRenderChartFailureIsReported` asserts the error carries `joinTokenSecret is required`.
  `TestRenderCapabilities` asserts the chart's CNPG message without that API. Fails if the operator's
  objects differ from the chart's or a retained kind gets an owner.

- [ ] [S-3] [S-5] [S-6] Envtest `TestInstallationCreatesChartObjects`
      (`operator/internal/controller/installation`) reconciles a CR with group `default` (instances `a` on
      `node-1` and `b` on `node-2`, ClusterIP instance Services) whose `NexoraEngineGroup` status names
      `default-join-token`. It asserts:
  - Deployment `nexora-mgmt` with `NEXORA_BOOTSTRAP_TOKEN_FILE`;
  - DaemonSets `nexora-engine-default-a` and `-b` with `kubernetes.io/hostname In [node-1]` and
    `[node-2]`, `maxSurge: 1` and `maxUnavailable: 0`;
  - Services `nexora-dns-a` and `-b`;
  - Secrets `nexora-ca`, `nexora-kek` and `nexora-operator-token` without owner references;
  - `status.secrets` and `status.managementURL`.

  `TestInstallationWaitsForJoinTokens` sees no engine workload and `EnginesReady` reason
  `JoinTokenPending` until the engine group status is set, then both DaemonSets.
  `TestInstallationPrunesRemovedObjects` removes instance `b`: `-b` and its Service are deleted and `-a`
  keeps its UID. Fails if a chart object is missing, a workload renders without a token, or a removed
  object survives.

- [ ] [S-3] [S-4] [S-11] Envtest `TestInstallationKeepsDatabaseAndKeys` installs a minimal CNPG CRD.
  - It asserts the `Cluster` has no owner reference.
  - It asserts an `instances` change 2→3 is applied.
  - After switching to external mode, the `Cluster` still exists.
  - Keys: a pre-existing `nexora-ca` with only `ca.crt` gives `Rendered=False` with reason
    `SecretIncomplete` and is not modified.

  `TestInstallationRenderFailureKeepsObjects` sets opensearch without a URL after a good reconcile. It
  sees `Rendered=False` with reason `RenderFailed` and an unchanged mgmt Deployment `resourceVersion`.
  Fails if the database or keys can be deleted or regenerated, or a render error touches live objects.

- [ ] [S-4] Go tests `TestGenerateCA` (P-256, `IsCA`, CN `Nexora CA`, 10-year validity, PEM types
      `CERTIFICATE` and `EC PRIVATE KEY`, key matches certificate), `TestGenerateKEK` (base64 of 32 bytes),
      `TestGenerateBootstrapToken` (matches `^nxt_[A-Z2-7]{52}$`) and `TestEnsureSecretNeverOverwrites`
      (`operator/internal/keys`). The kw e2e subtest `install` proves the management plane loads the
      generated CA and KEK. Fails if a generated key has another format or an existing Secret changes.
- [ ] [S-6] Envtest `TestInstallationStatusConditions` uses an injected health checker. It asserts:
  - `ManagementReady` and `SetupRequired` are true once the mgmt Deployment reports an available
    replica, health answers 200 and setup is required;
  - `EnginesReady` is false with reason `RollingUpdate` while a DaemonSet has
    `updatedNumberScheduled < desiredNumberScheduled`;
  - `Ready` is true when every condition holds.

  `TestInstallationDefaultsImageTagToOperatorVersion` asserts the tag comes from version
  `sha-abc1234`, and that version `dev` without a tag gives reason `ImageTagRequired`. Fails if a
  condition is wrong or an image tag is guessed.

- [ ] [S-7] Envtest `TestEngineGroupCreatesGroupAndJoinToken` (`operator/internal/controller/enginegroup`,
      against `mgmtapi/fake`) asserts the group was created with the CR fields, `status.groupID`, and a
      Secret `edge-join-token` owned by the CR holding the fake token. The token carries
      `engine_group_id`, `ttl_seconds`, `max_uses` and `labels`, and the CR is `Ready`.
      `TestEngineGroupUpdatesOnlySetFields` changes `canary_count` in the fake and the CR's description,
      and sees a PUT carrying the new description, the fake's `canary_count` and the current revision.
      `TestEngineGroupRetriesOnConflict` answers 409 once and asserts eventual success.
      `TestEngineGroupDuplicateNameRefused` asserts `DuplicateGroupName` on the younger CR. Fails if
      unset fields are overwritten, a conflict is fatal, or two CRs fight over one group.
- [ ] [S-7] [S-8] Envtest `TestEngineGroupRotatesJoinToken` uses a fake clock and asserts:
  - a new token appears when expiry is within `renewBefore`;
  - the Secret is updated;
  - the previous token is revoked only after `revokeGracePeriod`.

  `TestEngineGroupRecreatesRevokedToken` asserts a replacement when the fake reports `revoked`.
  `TestEngineGroupDeletion` asserts:
  - `Retain`: tokens revoked, group kept, finalizer removed;
  - `Delete` on a non-empty group: `DeletionBlocked` and finalizer kept, then deletion after the fake
    empties;
  - `groupName: default` with `Delete`: no DELETE call;
  - a missing installation: finalizer removed without calls.

  `TestEngineGroupWaitsForManagement` and `TestEngineGroupUnauthorized` assert
  `ManagementUnavailable` and `Unauthorized` with no Secret written. Fails if a token outlives its grace,
  a non-empty or default group is deleted, or deletion hangs without an installation.

- [ ] [S-9] Go tests in `mgmt/internal/auth`:
  - `TestBootstrapTokenEnsuresSystemUser`: user `nexora-operator` has source `system`, role `admin` and
    no password; the token authenticates as admin; a second run adds no row and no audit event; an
    invalid file creates nothing.
  - `TestBootstrapTokenRotation`: the new token works and the old gets 401.
  - `TestBootstrapTokenRefusesHumanUser`.
  - `TestSetupRequiredIgnoresSystemUsers`.

  `TestSystemUsersMigration` (`mgmt/internal/store`) migrates a database at the previous head holding a
  local admin to `01000`, keeps the admin, and accepts `source='system'`. `TestSystemUserIsReadOnlyInAPI`
  (`mgmt/internal/api`) gets 409 `system_user` for `updateUser` and `deleteUser`, 401 for login, and
  still refuses to delete the last human admin while the system user exists.
  `TestConfigBootstrapToken` checks the env defaults and rejects an invalid interval. Fails if a system
  user suppresses setup, can be edited, deleted or logged in as, or a rotated token keeps working.

- [ ] [S-9] E2E `TestBootstrapTokenFleetBootstrap` (`e2e/bootstrap_token_test.go`) starts mgmt with
      `NEXORA_BOOTSTRAP_TOKEN_FILE` and a 1 s interval. With the bearer token it creates engine group `boot`
      and a join token. An engine enrolls with that token into `boot`. `GET /setup` still says required.
      After the file is rewritten, the old token gets 401 and the new one 201 within 5 s.

  `TestHelmBootstrapToken` (`deploy/deploytest`) asserts the env, the 0440 Secret volume and the mount
  only when the value is set. The Playwright spec `web/e2e/screens/71-system-user.spec.ts` mocks
  `/api/v1/users` with a system user and sees "System" and no edit or delete buttons on that row. Fails
  if the token does not reach the API path engines depend on, or the GUI offers destructive actions on
  the system user.

- [ ] [S-10] Go test `TestHelmCNPGHighAvailability` (`deploy/deploytest`) asserts that
      `database.cnpg.instances=3`, `antiAffinity=required`, `primaryUpdateMethod=switchover`, `resources`
      and `postgresql.parameters.max_connections=200` render into the `Cluster` fields named in [S-10].
      It also asserts the mgmt `NEXORA_DATABASE_URL` still reads `<clusterName>-app`/`uri`. The kw e2e
      subtest `TestKwOperator/cnpg-failover` deletes the current primary pod of the two-instance
      cluster. It requires `status.currentPrimary` to change within 120 s, `GET /api/v1/health` to
      answer 200 within 60 s after that, and a `NexoraEngineGroup` description change to reach the API
      and raise the engines' applied config version. Fails if a value is not rendered or the management
      plane needs a restart to survive failover.
- [ ] [S-11] Go test `TestHelmCNPGBackups` asserts `spec.backup.barmanObjectStore` (path, endpoint,
      credential refs, compression), `retentionPolicy`, and the `ScheduledBackup` fields in [S-11]. Nothing
      renders when disabled, and the chart fails with messages when `destinationPath` or credentials are
      missing.

  `TestHelmCNPGRecovery` asserts `bootstrap.recovery` and `externalClusters[0]` with defaults inherited
  from `backup`, the absence of `initdb`, and a failure on a serverName and path collision.
  `TestHelmKwRenderUnchanged` compares the kw render with `deploy/deploytest/testdata/kw-render.golden.yaml`,
  captured before M9's chart edits.

  The kw e2e subtest `TestKwOperator/cnpg-backup-restore` runs an on-demand `Backup` to MinIO, which
  must reach `completed`. It then restores a cluster rendered by `render.Render` from recovery values,
  requires `Ready`, and finds engine group `edge` in the restored `nexora` database. Fails if a backup
  cannot be taken and restored, or kw production output changes.

- [ ] [S-12] `TestOperationsDoc` requires `## Install with the Kubernetes operator` and
      `## PostgreSQL high availability and backups` in `docs/operations.md`, and every documented path to
      exist. `TestArchitectureDocNamesPlatform` (`deploy/deploytest`) requires `## Platform (M9)` in
      `docs/architecture.md` naming both CRD kinds, `NEXORA_BOOTSTRAP_TOKEN_FILE` and `01000_system_users.sql`.
      Fails if a section or path is missing.
- [ ] [S-5] [S-13] `scripts/kw-operator-e2e.sh` runs `TestKwOperator` on kw. Its subtests are:
  - `guards`: namespace is `nexora-optest`; no rendered LoadBalancer or `loadBalancerIP`; engine nodes
    exclude `master-12` and `master-13`.
  - `install`: `Ready` within 15 minutes; a zone `optest.nexora.test.` created through the API answers
    NOERROR from `nexora-optest-dns-a` and `-b`.
  - `engine-groups`: an engine of group `edge` enrolls into `edge`.
  - `rolling-update`: `spec.engine.workers` goes 2→1 while dnsperf sends 5 queries/s to each instance
    Service. Every engine pod is replaced, and a continuous fresh-socket probe on both loses 0 queries (at least 960 sent, no gap over 1 s, from before the change until every pod is ready). dnsperf also runs and must report 0 lost when it finishes; on kw, Cilium's socket load balancer aborts dnsperf's connected UDP socket when its backend leaves, which is a client-socket effect, not an engine loss (lead decision 2026-09-15).
  - `join-token-rotation`: `ttl: 3m`, `renewBefore: 2m`, `revokeGracePeriod: 30s`. The Secret changes
    and the old token shows `revoked`.
  - `prune`: removing group `edge` deletes its DaemonSet.
  - `cnpg-failover` and `cnpg-backup-restore`, as above.
  - `delete-retains-state`: deleting the CR removes the workloads within 2 minutes and keeps the
    `Cluster` and the three Secrets.

  The script exits 0, deletes `nexora-optest`, and records the run in `.procoder/notes/plan-review.md`.
  Fails if any subtest fails, a query is lost during the roll, or a guard would allow touching
  production.

- [ ] [S-14] After the M9 `scripts/kw-deploy.sh` run, `scripts/kw-acceptance.sh` passes (`TestKwSmoke`,
      `TestKwFullProduct`, `TestKwFilterCategories`) with zero lost probe queries on 192.168.10.136 and
      192.168.10.139. `kubectl --context kw -n nexora get nexorainstallations` returns no resources.
      Issues #37 and #41 are closed with the commit and these test names. Fails if production loses a
      query, an acceptance test fails, or production was switched to the operator.

## Open questions
