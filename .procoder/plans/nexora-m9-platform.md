# nexora-m9-platform — implementation plan

Status: draft
Spec: .procoder/specs/nexora-m9-platform.md

## Goal

Ship milestone M9 Platform (issues #37 and #41): a Kubernetes operator with `NexoraInstallation` and
`NexoraEngineGroup` CRDs that renders the existing Nexora chart and manages engine groups and join tokens
through the API, plus optional CNPG HA, backups and restore in the chart. The proof is envtest, unit
tests and one kw e2e run in the disposable namespace `nexora-optest`.

## Architecture

Two contract tasks come first and freeze every shared name:

- Task 1: the architecture text, the `operator/` module skeleton, the CRD Go types, the generated
  CRDs, the Make targets and the CI job.
- Task 2: the management API contract (`User.source` `system`, error `system_user`), migration `01000`
  and the regenerated clients.

The work then splits into three layers:

- **Management plane:** Task 3, the bootstrap token and system users.
- **Chart:** Task 4 (bootstrap token value, CNPG HA, backups, recovery) and Task 8 (the operator's own
  chart, manifests and image).
- **Operator:**
  - Task 5: key generation and the management API client with its fake.
  - Task 6: rendering the chart.
  - Task 7: the engine group controller.
  - Task 9: the installation controller.

Task 10 proves everything on kw in `nexora-optest`, Task 11 writes the operations guide, and Task 12 runs
the regular production deploy.

### Decisions not settled by the spec (made here, binding for the tasks)

- **Module layout** (`operator/`, module `github.com/piwi3910/nexora/operator`, go 1.27):
  - `api/v1alpha1/`: `groupversion_info.go`, `nexorainstallation_types.go`, `nexoraenginegroup_types.go`,
    `values_types.go`, `conditions.go`, `zz_generated.deepcopy.go`.
  - `cmd/nexora-operator/main.go`.
  - `internal/version/`: `Version`, `Commit`, `BuildDate`, set with `-X`.
  - `internal/envtestutil/`: starts envtest with `deploy/operator/crds`.
  - `internal/keys/`, `internal/mgmtapi/` (with `fake/`), `internal/render/`.
  - `internal/controller/enginegroup/` and `internal/controller/installation/`.
  - `config/samples/` and `test/kw/`.
- **Dependencies:** Task 1 writes `operator/go.mod` with every dependency. `operator/tools.go`
  (`//go:build tools`) blank-imports `helm.sh/helm/v3/pkg/engine`, `helm.sh/helm/v3/pkg/chart/loader`,
  `github.com/oapi-codegen/runtime` and `sigs.k8s.io/controller-runtime/pkg/envtest`, so `go mod tidy`
  keeps them. Later tasks do not edit `go.mod`/`go.sum`; a missing module is reported to the lead, who
  adds it serially.
- **CRD fields:** every boolean and integer field of the values mirror is a pointer, and strings use
  `omitempty`. An unset field is then absent from the values and the chart default applies. The CRD
  has no `+kubebuilder:default` on values fields, so `values.yaml` stays the single source of defaults.
  - Kubernetes-typed fields: `corev1.ResourceRequirements`, `corev1.NodeAffinity`,
    `[]corev1.Toleration`, `[]corev1.EnvVar`, `[]corev1.Volume`, `[]corev1.VolumeMount` and
    `[]corev1.LocalObjectReference`.
  - `engine.groups` and `instances` are `+listType=map +listMapKey=name` with `MaxItems=64`, so the API
    server rejects duplicates.
- **Values mapping** (`render.BuildValues`):
  - JSON-marshal the spec into a map and remove every `engineGroupRef`.
  - Always set `engine.groups` to the resolved list (possibly empty), so the chart's default group never
    applies.
  - Set `mgmt.ca.existingSecret`, `mgmt.kek.existingSecret`, `mgmt.bootstrapToken.existingSecret`,
    `image.tag`, and `metrics.serviceMonitor.namespace = ""` and `metrics.prometheusRule.namespace = ""`.
- **Rendering:**
  - `chartutil.ToRenderValues` with `ReleaseOptions{Name, Namespace, Revision: 1, IsInstall: true}`,
    then overwrite `Release.Service` with `nexora-operator` in the returned map.
  - Capabilities: `chartutil.DefaultCapabilities.Copy()` with the discovered API versions and server
    version.
  - Templates whose base name starts with `_` or ends with `NOTES.txt` are skipped. Documents are split
    with `releaseutil.SplitManifests`.
  - Objects are ordered by kind rank: ConfigMap, Service, Cluster, ScheduledBackup,
    PodDisruptionBudget, Deployment, DaemonSet, Ingress, ServiceMonitor, PrometheusRule; then by name.
- **Ownership:**
  - Label `nexora.io/installation` goes on top-level metadata only, never on pod templates or
    selectors.
  - Every object gets a controller owner reference, except `postgresql.cnpg.io/v1 Cluster`
    (`render.Retained`).
- **Managed kinds** (`render.ManagedKinds`): apps/v1 Deployment and DaemonSet; v1 Service and
  ConfigMap; policy/v1 PodDisruptionBudget; networking.k8s.io/v1 Ingress; postgresql.cnpg.io/v1
  Cluster and ScheduledBackup; monitoring.coreos.com/v1 ServiceMonitor and PrometheusRule.
- **Pruning:** only after every rendered object applied without error. For every managed kind the API
  server serves, list objects labelled `nexora.io/installation=<name>` in the namespace and delete those
  not rendered, except retained kinds.
- **Apply:** `Client.Apply(ctx, client.ApplyConfigurationFromUnstructured(obj),
client.FieldOwner("nexora-operator"), client.ForceOwnership)`.
- **Management URL:** `http://<name of the rendered Service labelled app.kubernetes.io/name=nexora-mgmt
and nexora.io/metrics=true>.<namespace>.svc:8080`. The chart's prefix helper is not duplicated in Go.
- **Engine group gating:** a group's Secret is used only when the referenced `NexoraEngineGroup` has
  `spec.installationRef.name` equal to the installation and the condition `JoinTokenReady=True`.
- **Engine group controller → API:** the controller calls the API only when the installation has
  `ManagementReady=True`. The token is read from the Secret named in `status.secrets.operatorToken`.
  - Updates always send the full current group overlaid with the fields set in the CR, and only when
    the overlay differs.
  - Join token name: `op/<namespace>/<cr name>/<unix seconds>`, cut to 64 characters.
- **Clock:** both controllers have `Now func() time.Time`. Tests call `Reconcile` directly against
  envtest, with no manager, so fake clocks are deterministic.
- **Bootstrap token in the management plane:** `auth.Service.EnsureBootstrapToken(ctx, token)` runs in
  one transaction under `pg_advisory_xact_lock(hashtext('nexora:bootstrap_token'))`.
  `auth.Service.RunBootstrapToken` reads the file at start and every interval. The counter
  `auth.BootstrapTokenErrors` is registered by `serve` like `pki.DNSTLSReloadErrors`.
  `ensureOtherAdmin` and the setup-required counts exclude `source = 'system'`.
- **Envtest:** Kubernetes assets `1.34.x` from `setup-envtest@v0.25.0` into `bin/envtest`.
  `envtestutil.Start` fails (not skips) without `KUBEBUILDER_ASSETS`.
- **kw e2e:** runs on the laptop (`go test -tags kwe2e` in `operator/`) with kube context `kw`.
  - Commands inside the cluster run through `kubectl exec` into the probe pod `probe` (image
    `192.168.10.131/azrtydxb/nexora-dev:toolbox-1`).
  - The API token goes to curl on stdin, never in argv.

### Wave order and file ownership

A task may start when every task in earlier waves is committed. Tasks in one wave never edit the same
file. Each task's `Files:` line is its exclusive ownership for its wave.

| Wave | Tasks (parallel)                                                               | Shared files it serialises                                                                                                                                    |
| ---- | ------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 0    | 1 operator contract, 2 management API contract                                 | `docs/architecture.md`, `Makefile`, `.github/workflows/ci.yml`, `operator/go.mod` (1); `openapi.yaml`, `gen.go`, `schema.d.ts` (2)                            |
| 1    | 3 bootstrap token in mgmt, 4 chart CNPG and token value, 5 keys and API client | `mgmt/cmd/nexora-mgmt/main.go`, `handlers_admin.go`, `auth/service.go`, `docs/architecture.md`, `web/src/pages/UsersPage.tsx` (3); `deploy/helm/nexora/*` (4) |
| 2    | 6 render, 7 engine group controller, 8 operator packaging                      | `operator/cmd/nexora-operator/main.go` (7); `workflow_test.go`, `buildinfo_test.go`, `images.yml` (8)                                                         |
| 3    | 9 installation controller                                                      | `operator/cmd/nexora-operator/main.go`                                                                                                                        |
| 4    | 10 kw operator e2e                                                             | `.procoder/notes/plan-review.md`                                                                                                                              |
| 5    | 11 operations guide                                                            | `docs/operations.md`, `deploy/deploytest/docs_test.go`, `deploy/kw/README.md`                                                                                 |
| 6    | 12 kw production deploy                                                        | `.procoder/notes/plan-review.md`                                                                                                                              |

Dependencies beyond the wave rule:

- 3 consumes 2.
- 5 consumes 1 and 2 (the generated client reads `openapi.yaml`).
- 6 consumes 1 and 4.
- 7 consumes 1 and 5.
- 8 consumes 1.
- 9 consumes 5, 6, 7 and 8.
- 10 consumes everything.
- 11 consumes 10 (the documented script and results).
- 12 consumes everything.

### Spec coverage

| Spec item                               | Tasks      |
| --------------------------------------- | ---------- |
| S-1 operator module and packaging       | 1, 8, 9    |
| S-2 NexoraInstallation CRD              | 1, 6       |
| S-3 installation reconcile              | 6, 9       |
| S-4 generated key material              | 5, 9, 10   |
| S-5 engines, join tokens, rolling       | 6, 9, 10   |
| S-6 installation status                 | 1, 9       |
| S-7 NexoraEngineGroup and API reconcile | 1, 5, 7    |
| S-8 join token lifecycle                | 7, 10      |
| S-9 bootstrap token and system users    | 1, 2, 3, 4 |
| S-10 CNPG HA                            | 4, 10      |
| S-11 CNPG backups and restore           | 4, 6, 10   |
| S-12 documentation                      | 1, 11      |
| S-13 kw e2e                             | 10         |
| S-14 production continuity              | 4, 12      |

## Constraints

Copied from the spec (binding for every task):

- "Never change the production namespace `nexora`, the Helm release `nexora`, or the addresses
  192.168.10.136 and 192.168.10.139." The kw e2e runs only in `nexora-optest`, uses only ClusterIP
  Services and never schedules engines on `master-12` or `master-13`.
- "Migrations added by M9 use `01000`–`01009`; M9 adds only `01000_system_users.sql`. Proto fields
  added by M9 use `1000`–`1099`; M9 adds none."
- "The engine is unchanged."
- "The management plane stays stateless and gains no HA logic."
- "Every existing Go, Rust and Playwright test keeps passing."
- "The kw values render stays byte-identical."
- Operator module versions: Go 1.27, controller-runtime v0.25.0, k8s.io/\* v0.37.0,
  helm.sh/helm/v3 v3.21.4, github.com/oapi-codegen/runtime v1.7.0, controller-gen v0.20.1,
  setup-envtest v0.25.0, envtest Kubernetes 1.34.x.
- "The root module's `go.mod` gains no Kubernetes dependency."
- "Secrets never appear in logs, events, conditions or audit rows."
- Containers run non-root with read-only root filesystems and drop all capabilities.
- "Generated files are committed and checked for drift in CI."
- Out of scope:
  - configuration CRDs;
  - moving kw production to the operator;
  - backups for the kw production database;
  - the Barman Cloud plugin;
  - HA logic in the management plane;
  - objects outside the installation namespace;
  - webhooks;
  - engine groups for non-operator installs;
  - operator-issued DNS TLS certificates.

Project rules (from `.procoder/notes/implementer-brief.md` and `docs/architecture.md`):

- Builds and tests run in the kw dev pod: `scripts/dev-exec.sh '<command>'`.
  - Generated sources are produced on the laptop: `make operator-generate` and
    `cd mgmt/api && oapi-codegen -config oapi-codegen.yaml openapi.yaml`, plus
    `cd web && pnpm run gen:api`.
  - The kw e2e (Task 10) runs on the laptop with kube context `kw`.
- Implementers never commit. They list changed paths in the final report, and the lead commits with
  `scripts/commit-paths.sh`.
- TDD: write the failing test, run it, see the stated failure, implement, see it pass. Never weaken or
  delete a test.
- Formatters and linters on changed files: `gofmt -l`, `go vet ./...` (root) and
  `cd operator && go vet ./...`; `cd web && pnpm run typecheck && pnpm run lint`;
  `scripts/pc-format.sh <md files>`; `helm lint --strict` for charts.
- Mark deliberate ceilings with `debt:` comments naming the ceiling and the revisit condition.
- Fixed identifiers:
  - API group `nexora.io`, version `v1alpha1`, kinds `NexoraInstallation` (`nxi`) and
    `NexoraEngineGroup` (`nxeg`).
  - Finalizer `nexora.io/engine-group`, label `nexora.io/installation`, field manager
    `nexora-operator`.
  - Condition types `Rendered`, `DatabaseReady`, `ManagementReady`, `SetupRequired`, `EnginesReady`,
    `Ready`, `Synced`, `JoinTokenReady`.
  - Reasons `RenderFailed`, `ImageTagRequired`, `SecretIncomplete`, `ForeignNamespace`,
    `JoinTokenPending`, `RollingUpdate`, `Unavailable`, `ManagementUnavailable`, `Unauthorized`,
    `Conflict`, `DuplicateGroupName`, `DeletionBlocked`, `Reconciled`.
  - Management env `NEXORA_BOOTSTRAP_TOKEN_FILE` and `NEXORA_BOOTSTRAP_TOKEN_RELOAD_INTERVAL`; system
    user `nexora-operator`; token name `bootstrap`; audit action `ensureBootstrapToken`; error code
    `system_user`; metric `nexora_mgmt_bootstrap_token_errors_total`.
  - Secrets `<name>-ca`, `<name>-kek`, `<name>-operator-token` and `<cr name>-join-token`.

## Task 1: Operator contract: architecture, module skeleton, CRD types, generation, CI

Files:

- `docs/architecture.md`: new section `## Platform (M9)`; repository layout lines for `operator/`,
  `deploy/operator/` and `deploy/helm/nexora-operator/`.
- `operator/go.mod`, `operator/go.sum`, `operator/tools.go`: the module and every dependency.
- `operator/api/v1alpha1/groupversion_info.go`, `nexorainstallation_types.go`,
  `nexoraenginegroup_types.go`, `values_types.go`, `conditions.go`: the CRD types.
- `operator/api/v1alpha1/zz_generated.deepcopy.go`: generated.
- `operator/api/v1alpha1/validation_test.go`: created; the envtest validation tests.
- `deploy/operator/crds/nexora.io_nexorainstallations.yaml`, `deploy/operator/crds/nexora.io_nexoraenginegroups.yaml`:
  generated.
- `deploy/helm/nexora-operator/crds/nexora.io_nexorainstallations.yaml`,
  `deploy/helm/nexora-operator/crds/nexora.io_nexoraenginegroups.yaml`: copies.
- `operator/internal/version/version.go`, `operator/internal/envtestutil/envtestutil.go`: created.
- `operator/cmd/nexora-operator/main.go`: the manager with flags and no controllers.
- `operator/config/samples/nexora_v1alpha1_nexorainstallation.yaml`,
  `operator/config/samples/nexora_v1alpha1_nexoraenginegroup.yaml`: created.
- `operator/internal/render/testdata/kw-installation.yaml`: created; the kw-equivalent CR that Task 6
  consumes.
- `Makefile`: targets `operator-generate` and `operator-test`.
- `.github/workflows/ci.yml`: job `operator`.
- `deploy/deploytest/platform_docs_test.go`: created.

Interfaces (produced; Tasks 5–10 consume them):

```go
package v1alpha1 // github.com/piwi3910/nexora/operator/api/v1alpha1
var GroupVersion = schema.GroupVersion{Group: "nexora.io", Version: "v1alpha1"}
var AddToScheme func(*runtime.Scheme) error

// +kubebuilder:resource:shortName=nxi,categories=nexora
// +kubebuilder:subresource:status
type NexoraInstallation struct { metav1.TypeMeta; metav1.ObjectMeta; Spec NexoraInstallationSpec; Status NexoraInstallationStatus }
type NexoraInstallationSpec struct {
	Image            ImageSpec                     `json:"image,omitempty"`
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
	Mgmt             MgmtSpec                      `json:"mgmt,omitempty"`
	Database         DatabaseSpec                  `json:"database,omitempty"`
	Engine           EngineSpec                    `json:"engine,omitempty"`
	OtelCollector    OtelCollectorSpec             `json:"otelCollector,omitempty"`
	Metrics          MetricsSpec                   `json:"metrics,omitempty"`
}
type NexoraInstallationStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Version            string             `json:"version,omitempty"`
	ManagementURL      string             `json:"managementURL,omitempty"`
	Secrets            InstallationSecrets `json:"secrets,omitempty"` // CA, KEK, OperatorToken string
	Workloads          []WorkloadStatus   `json:"workloads,omitempty"` // Kind, Name string; Desired, Ready, Updated int32
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// values_types.go (JSON names equal deploy/helm/nexora/values.yaml keys; bools and ints are pointers)
type ImageSpec struct { Registry, Tag string; PullPolicy corev1.PullPolicy }
type MgmtSpec struct {
	Replicas *int32; PublicURL string; SecureCookies *bool; EngineCertTTL, RolloutTick string
	GrpcServerNames []string; OtlpEndpoint string
	CA, KEK SecretRef                                  // json "ca", "kek"; SecretRef{ExistingSecret string}
	DNSTLS DNSTLSSpec                                  // json "dnsTLS": ExistingSecret, ReloadInterval string
	Querylog QuerylogSpec                              // Backend string (+Enum builtin;opensearch); BuiltinCapacity *int32; Opensearch{URL "url", Index string}
	ExtraEnv []corev1.EnvVar; ExtraVolumes []corev1.Volume; ExtraVolumeMounts []corev1.VolumeMount
	Resources *corev1.ResourceRequirements
	GrpcLoadBalancer LoadBalancerSpec                  // Enabled *bool; LoadBalancerIP string
	Ingress IngressSpec                                // Enabled *bool; Name, ClassName, Host, ClusterIssuer, TLSSecretName ("tlsSecretName") string
	PDB PDBSpec                                        // json "pdb": MinAvailable *int32
}
type DatabaseSpec struct { Mode string /* +Enum cnpg;external */; External ExternalDatabaseSpec /* ExistingSecret, Key */; CNPG CNPGSpec /* json "cnpg" */ }
type CNPGSpec struct {
	ClusterName string; Instances *int32; ImageName, StorageClass, Size string
	Resources *corev1.ResourceRequirements
	AntiAffinity string        // +Enum preferred;required
	PrimaryUpdateMethod string // +Enum switchover;restart
	PostgreSQL PostgreSQLSpec  // json "postgresql": Parameters map[string]string
	Backup CNPGBackupSpec; Recovery CNPGRecoverySpec
}
type S3Credentials struct { ExistingSecret, AccessKeyIDKey /* "accessKeyIdKey" */, SecretAccessKeyKey string }
type SecretKeyRef struct { ExistingSecret, Key string }
type CNPGBackupSpec struct {
	Enabled *bool; DestinationPath, EndpointURL /* "endpointURL" */, ServerName string
	S3Credentials S3Credentials; EndpointCA SecretKeyRef /* "endpointCA" */
	RetentionPolicy, WalCompression, DataCompression, Schedule string; Immediate *bool
}
type CNPGRecoverySpec struct {
	Enabled *bool; SourceServerName, DestinationPath, EndpointURL string
	S3Credentials S3Credentials; EndpointCA SecretKeyRef; TargetTime string
}
type EngineSpec struct {
	Enabled *bool; Kind string /* +Enum DaemonSet;Deployment */; ManagementURL string; Workers *int32; InitImage string
	HostNetwork *bool; Ports EnginePorts /* DNS "dns", Metrics, DoT "dot", DoH "doh", DoQ "doq" *int32 */; DohPath string
	StateDir StateDirSpec /* Type +Enum hostPath;emptyDir, HostPathPrefix */; Tolerations []corev1.Toleration
	ShutdownDrainSeconds, MinReadySeconds *int32; Resources *corev1.ResourceRequirements
	Groups []EngineGroupSpec // +listType=map +listMapKey=name +kubebuilder:validation:MaxItems=64
}
type EngineGroupSpec struct {
	Name string // required, pattern ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$
	EngineGroupRef, WorkloadName string; NodeNamePrefix *string /* pointer: "" differs from unset */; Replicas *int32
	NodeAffinity *corev1.NodeAffinity; Service *ServiceSpec; ExtraServices []ServiceSpec
	Instances []EngineInstanceSpec // +listType=map +listMapKey=name +MaxItems=64
}
type ServiceSpec struct { Name, Type, LoadBalancerIP /* "loadBalancerIP" */, ExternalTrafficPolicy string }
type EngineInstanceSpec struct { Name string /* required */; Node string /* required, MinLength=1 */; Service *ServiceSpec }
type OtelCollectorSpec struct { Enabled *bool; Image string; Resources *corev1.ResourceRequirements; Config string }
type MetricsSpec struct { ServiceMonitor ServiceMonitorSpec /* Enabled *bool; Labels map[string]string; Interval string */; PrometheusRule PrometheusRuleSpec /* Enabled *bool; Labels */ }

// +kubebuilder:resource:shortName=nxeg,categories=nexora
// +kubebuilder:subresource:status
type NexoraEngineGroup struct { metav1.TypeMeta; metav1.ObjectMeta; Spec NexoraEngineGroupSpec; Status NexoraEngineGroupStatus }
type NexoraEngineGroupSpec struct {
	InstallationRef     LocalRef          `json:"installationRef"` // Name string
	GroupName           string            `json:"groupName,omitempty"`
	Description         *string           `json:"description,omitempty"`
	UpstreamMode        *string           `json:"upstreamMode,omitempty"` // +Enum inherit;override
	ExtraACLCIDRs       []string          `json:"extraACLCIDRs,omitempty"`
	OtlpEndpoint        *string           `json:"otlpEndpoint,omitempty"`
	FilterIndexMaxBytes *int64            `json:"filterIndexMaxBytes,omitempty"`
	Rollout             RolloutSpec       `json:"rollout,omitempty"` // Strategy *string; CanaryCount, CanaryPercent, AckTimeoutSeconds, HealthWindowSeconds, MinHealthQueries *int32; MaxServfailRatio *string
	JoinToken           JoinTokenSpec     `json:"joinToken,omitempty"`
	DeletionPolicy      string            `json:"deletionPolicy,omitempty"` // +Enum Retain;Delete +default Retain
}
type JoinTokenSpec struct {
	SecretName        string            `json:"secretName,omitempty"`
	TTL               *metav1.Duration  `json:"ttl,omitempty"`               // +default "8760h"
	RenewBefore       *metav1.Duration  `json:"renewBefore,omitempty"`       // +default "720h"
	RevokeGracePeriod *metav1.Duration  `json:"revokeGracePeriod,omitempty"` // +default "10m"
	MaxUses           *int32            `json:"maxUses,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"` // MaxProperties=32
}
type NexoraEngineGroupStatus struct {
	ObservedGeneration int64; GroupID string; Revision int64; EngineCount int32
	JoinTokenSecret, JoinTokenID string; JoinTokenExpiresAt *metav1.Time
	PreviousJoinTokenID string; PreviousJoinTokenRevokeAt *metav1.Time
	Conditions []metav1.Condition
}

// conditions.go
const (
	ConditionRendered = "Rendered"; ConditionDatabaseReady = "DatabaseReady"; ConditionManagementReady = "ManagementReady"
	ConditionSetupRequired = "SetupRequired"; ConditionEnginesReady = "EnginesReady"; ConditionReady = "Ready"
	ConditionSynced = "Synced"; ConditionJoinTokenReady = "JoinTokenReady"
	ReasonRenderFailed = "RenderFailed"; ReasonImageTagRequired = "ImageTagRequired"; ReasonSecretIncomplete = "SecretIncomplete"
	ReasonForeignNamespace = "ForeignNamespace"; ReasonJoinTokenPending = "JoinTokenPending"; ReasonRollingUpdate = "RollingUpdate"
	ReasonUnavailable = "Unavailable"; ReasonManagementUnavailable = "ManagementUnavailable"; ReasonUnauthorized = "Unauthorized"
	ReasonConflict = "Conflict"; ReasonDuplicateGroupName = "DuplicateGroupName"; ReasonDeletionBlocked = "DeletionBlocked"
	ReasonReconciled = "Reconciled"
	FinalizerEngineGroup = "nexora.io/engine-group"; LabelInstallation = "nexora.io/installation"; FieldManager = "nexora-operator"
)
```

The CEL rules (markers on the named types; the messages are the literal strings the tests match):

| Type                    | Rule                                                                                                                                                 | Message                                                                          |
| ----------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| `DatabaseSpec`          | `!has(self.mode) \|\| self.mode != 'external' \|\| (has(self.external.existingSecret) && size(self.external.existingSecret) > 0)`                    | `database.external.existingSecret is required when database.mode=external`       |
| `QuerylogSpec`          | `!has(self.backend) \|\| self.backend != 'opensearch' \|\| (has(self.opensearch.url) && size(self.opensearch.url) > 0)`                              | `mgmt.querylog.opensearch.url is required when mgmt.querylog.backend=opensearch` |
| `CNPGBackupSpec`        | `!has(self.enabled) \|\| !self.enabled \|\| (has(self.destinationPath) && size(self.destinationPath) > 0 && has(self.s3Credentials.existingSecret))` | `database.cnpg.backup needs destinationPath and s3Credentials.existingSecret`    |
| `JoinTokenSpec`         | `!has(self.ttl) \|\| (duration(self.ttl) >= duration('1m') && duration(self.ttl) <= duration('8760h'))`                                              | `joinToken.ttl must be between 1m and 8760h`                                     |
| `JoinTokenSpec`         | `!has(self.ttl) \|\| !has(self.renewBefore) \|\| duration(self.renewBefore) < duration(self.ttl)`                                                    | `joinToken.renewBefore must be less than joinToken.ttl`                          |
| `NexoraEngineGroupSpec` | `has(self.groupName) == has(oldSelf.groupName) && (!has(self.groupName) \|\| self.groupName == oldSelf.groupName)`                                   | `groupName is immutable`                                                         |
| `NexoraEngineGroupSpec` | `self.installationRef.name == oldSelf.installationRef.name`                                                                                          | `installationRef is immutable`                                                   |

`CNPGBackupSpec.S3Credentials` and `QuerylogSpec.Opensearch` and `DatabaseSpec.External` are
non-pointer structs with `+kubebuilder:default={}` so `self.external` always exists in CEL; so is
`NexoraEngineGroupSpec.JoinToken`, so the join token duration defaults apply when `joinToken` is omitted.
The rules use `size(x) > 0` instead of `x != ''`: gofmt rewrites `''` in doc comments to a typographic
quote, which breaks the marker.
`NexoraEngineGroupSpec.GroupName` has the pattern `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`.

```go
package envtestutil // github.com/piwi3910/nexora/operator/internal/envtestutil
// Start runs kube-apiserver and etcd (KUBEBUILDER_ASSETS) with deploy/operator/crds plus extraCRDs,
// registers clientgoscheme and v1alpha1, and stops at test cleanup.
func Start(t *testing.T, extraCRDs ...string) (*rest.Config, client.Client)
// RepoRoot is the directory holding operator/go.mod's parent.
func RepoRoot(t *testing.T) string

package version // github.com/piwi3910/nexora/operator/internal/version
var Version = "dev"; var Commit = ""; var BuildDate = ""
```

`operator/cmd/nexora-operator/main.go` exposes `run(args []string, stdout, stderr io.Writer) int` with
the flags `--chart-dir` (`/charts/nexora`), `--watch-namespaces` (comma list → `cache.Options.DefaultNamespaces`),
`--leader-elect` (true), `--leader-election-namespace` (the pod namespace from `POD_NAMESPACE`),
`--metrics-bind-address` (`:8080`), `--health-probe-bind-address` (`:8081`), `--resync-interval` (`60s`),
leader election id `nexora-operator.nexora.io`, and the subcommand `version` printing
`nexora-operator <Version> <Commit> <BuildDate>`. It builds `opts` (a struct `options` with those
fields) and a manager, then calls `setupControllers(mgr, opts)`, a function in the same file that
Task 7 and Task 9 extend; Task 1 leaves its body `return nil`.

- [x] Create `deploy/deploytest/platform_docs_test.go`:
  ```go
  package deploytest

  import (
  	"os"
  	"strings"
  	"testing"
  )

  func TestArchitectureDocNamesPlatform(t *testing.T) {
  	b, err := os.ReadFile("../../docs/architecture.md")
  	if err != nil {
  		t.Fatal(err)
  	}
  	doc := string(b)
  	i := strings.Index(doc, "\n## Platform (M9)\n")
  	if i < 0 {
  		t.Fatal("docs/architecture.md lacks ## Platform (M9)")
  	}
  	section := doc[i:]
  	if j := strings.Index(section[1:], "\n## "); j >= 0 {
  		section = section[:j+1]
  	}
  	for _, want := range []string{"NexoraInstallation", "NexoraEngineGroup", "NEXORA_BOOTSTRAP_TOKEN_FILE", "01000_system_users.sql", "nexora.io/installation", "barmanObjectStore"} {
  		if !strings.Contains(section, want) {
  			t.Errorf("## Platform (M9) does not mention %s", want)
  		}
  	}
  }
  ```
  Run `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestArchitectureDocNamesPlatform -count=1'` and
  expect FAIL `docs/architecture.md lacks ## Platform (M9)`.
- [x] Write `## Platform (M9)` in `docs/architecture.md` after `## Fleet (M5)`: the module layout, both
      CRDs and their JSON-mirrors-values rule, the render → own → apply → prune → status loop, the
      retained kinds and Secrets, engine group gating, the engine group controller's managed-fields rule
      and join token rotation, the bootstrap token (`NEXORA_BOOTSTRAP_TOKEN_FILE`, system user,
      `01000_system_users.sql`), and the CNPG values (`barmanObjectStore`, ScheduledBackup, recovery).
      Add the three directories to the repository layout block. Run the test again and expect PASS.
- [x] Create `operator/go.mod` (`module github.com/piwi3910/nexora/operator`, `go 1.27`) and
      `operator/tools.go`:
  ```go
  //go:build tools

  // Package tools pins modules the operator's packages import in later tasks.
  package tools

  import (
  	_ "github.com/oapi-codegen/runtime"
  	_ "helm.sh/helm/v3/pkg/chart/loader"
  	_ "helm.sh/helm/v3/pkg/chartutil"
  	_ "helm.sh/helm/v3/pkg/engine"
  	_ "helm.sh/helm/v3/pkg/releaseutil"
  	_ "sigs.k8s.io/controller-runtime/pkg/envtest"
  )
  ```
  Run `cd operator && go get sigs.k8s.io/controller-runtime@v0.25.0 k8s.io/api@v0.37.0 k8s.io/apimachinery@v0.37.0 k8s.io/client-go@v0.37.0 k8s.io/apiextensions-apiserver@v0.37.0 helm.sh/helm/v3@v3.21.4 github.com/oapi-codegen/runtime@v1.7.0 github.com/google/uuid@v1.6.0 && go mod tidy`
  and expect a `go.sum` and no error. If Helm v3.21.4 does not compile against k8s.io v0.37.0
  (`go build ./...` error in `helm.sh/helm/v3/pkg/engine`), stop and report the error to the lead
  instead of changing versions.
- [x] Write the types, `groupversion_info.go`, `version.go`, `envtestutil.go`, `main.go` and the two
      samples exactly as in Interfaces. The installation sample has group `default` with instances `a`
      and `b` on `node-1` and `node-2`, mode `cnpg` with backup enabled, and `image.tag: sha-0000000`.
- [x] Add to `Makefile`:
  ```make
  CONTROLLER_GEN := go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.20.1
  ENVTEST_K8S ?= 1.34.x

  operator-generate:
  	cd operator && $(CONTROLLER_GEN) object paths=./api/...
  	cd operator && $(CONTROLLER_GEN) crd paths=./api/... output:crd:artifacts:config=../deploy/operator/crds
  	mkdir -p deploy/helm/nexora-operator/crds && cp deploy/operator/crds/*.yaml deploy/helm/nexora-operator/crds/
  	if [ -f operator/internal/mgmtapi/oapi-codegen.yaml ]; then cd operator/internal/mgmtapi && go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config oapi-codegen.yaml ../../../mgmt/api/openapi.yaml; fi
  	if [ -f deploy/helm/nexora-operator/Chart.yaml ]; then helm template nexora-operator deploy/helm/nexora-operator --namespace nexora-operator --set rbac.scope=cluster --set createNamespace=true > deploy/operator/operator.yaml; fi

  operator-test:
  	cd operator && KUBEBUILDER_ASSETS="$$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.25.0 use $(ENVTEST_K8S) --bin-dir $(CURDIR)/bin/envtest -p path)" go test -race -count=1 ./...
  ```
  and add both names to `.PHONY`. Run `make operator-generate` on the laptop and expect the deepcopy
  file and both CRD files in `deploy/operator/crds/` and `deploy/helm/nexora-operator/crds/`.
- [x] Create `operator/api/v1alpha1/validation_test.go`:
  ```go
  package v1alpha1_test

  import (
  	"context"
  	"os"
  	"path/filepath"
  	"strings"
  	"testing"
  	"time"

  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  	"sigs.k8s.io/controller-runtime/pkg/client"
  	"sigs.k8s.io/yaml"

  	"github.com/piwi3910/nexora/operator/api/v1alpha1"
  	"github.com/piwi3910/nexora/operator/internal/envtestutil"
  )

  func loadInstallation(t *testing.T, path string) *v1alpha1.NexoraInstallation {
  	t.Helper()
  	b, err := os.ReadFile(path)
  	if err != nil {
  		t.Fatal(err)
  	}
  	var in v1alpha1.NexoraInstallation
  	if err := yaml.UnmarshalStrict(b, &in); err != nil {
  		t.Fatalf("%s: %v", path, err)
  	}
  	return &in
  }

  func i32(v int32) *int32 { return &v }
  func yes() *bool         { b := true; return &b }

  func TestInstallationCRDValidation(t *testing.T) {
  	_, c := envtestutil.Start(t)
  	ctx := context.Background()
  	root := envtestutil.RepoRoot(t)
  	for i, p := range []string{
  		filepath.Join(root, "operator/config/samples/nexora_v1alpha1_nexorainstallation.yaml"),
  		filepath.Join(root, "operator/internal/render/testdata/kw-installation.yaml"),
  	} {
  		in := loadInstallation(t, p)
  		in.Namespace, in.Name = "default", "valid-"+string(rune('a'+i))
  		if err := c.Create(ctx, in); err != nil {
  			t.Fatalf("valid %s refused: %v", p, err)
  		}
  	}
  	base := func(name string) *v1alpha1.NexoraInstallation {
  		return &v1alpha1.NexoraInstallation{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name}}
  	}
  	cases := []struct {
  		name, want string
  		mutate     func(*v1alpha1.NexoraInstallation)
  	}{
  		{"external-no-secret", "database.external.existingSecret is required", func(in *v1alpha1.NexoraInstallation) { in.Spec.Database.Mode = "external" }},
  		{"instance-no-node", "node", func(in *v1alpha1.NexoraInstallation) {
  			in.Spec.Engine.Groups = []v1alpha1.EngineGroupSpec{{Name: "default", Instances: []v1alpha1.EngineInstanceSpec{{Name: "a"}}}}
  		}},
  		{"duplicate-instance", "Duplicate value", func(in *v1alpha1.NexoraInstallation) {
  			in.Spec.Engine.Groups = []v1alpha1.EngineGroupSpec{{Name: "default", Instances: []v1alpha1.EngineInstanceSpec{{Name: "a", Node: "n1"}, {Name: "a", Node: "n2"}}}}
  		}},
  		{"statefulset", "Unsupported value", func(in *v1alpha1.NexoraInstallation) { in.Spec.Engine.Kind = "StatefulSet" }},
  		{"opensearch-no-url", "mgmt.querylog.opensearch.url is required", func(in *v1alpha1.NexoraInstallation) { in.Spec.Mgmt.Querylog.Backend = "opensearch" }},
  		{"backup-no-path", "database.cnpg.backup needs destinationPath", func(in *v1alpha1.NexoraInstallation) { in.Spec.Database.CNPG.Backup.Enabled = yes() }},
  	}
  	for _, tc := range cases {
  		in := base(tc.name)
  		tc.mutate(in)
  		err := c.Create(ctx, in)
  		if err == nil || !strings.Contains(err.Error(), tc.want) {
  			t.Errorf("%s: err = %v, want it to contain %q", tc.name, err, tc.want)
  		}
  	}
  }

  func TestEngineGroupCRDValidation(t *testing.T) {
  	_, c := envtestutil.Start(t)
  	ctx := context.Background()
  	d := func(s string) *metav1.Duration { v, _ := time.ParseDuration(s); return &metav1.Duration{Duration: v} }
  	eg := func(name string) *v1alpha1.NexoraEngineGroup {
  		return &v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name},
  			Spec: v1alpha1.NexoraEngineGroupSpec{InstallationRef: v1alpha1.LocalRef{Name: "nexora"}, GroupName: name}}
  	}
  	good := eg("edge")
  	if err := c.Create(ctx, good); err != nil {
  		t.Fatalf("valid engine group refused: %v", err)
  	}
  	for _, tc := range []struct {
  		name, want string
  		mutate     func(*v1alpha1.NexoraEngineGroup)
  	}{
  		{"short-ttl", "joinToken.ttl must be between 1m and 8760h", func(g *v1alpha1.NexoraEngineGroup) { g.Spec.JoinToken.TTL = d("30s") }},
  		{"renew-too-long", "joinToken.renewBefore must be less than joinToken.ttl", func(g *v1alpha1.NexoraEngineGroup) {
  			g.Spec.JoinToken.TTL, g.Spec.JoinToken.RenewBefore = d("1h"), d("1h")
  		}},
  		{"bad-name", "spec.groupName", func(g *v1alpha1.NexoraEngineGroup) { g.Spec.GroupName = "Edge_B" }},
  	} {
  		g := eg(tc.name)
  		tc.mutate(g)
  		if err := c.Create(ctx, g); err == nil || !strings.Contains(err.Error(), tc.want) {
  			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
  		}
  	}
  	moved := good.DeepCopy()
  	moved.Spec.GroupName = "edge2"
  	if err := c.Update(ctx, moved); err == nil || !strings.Contains(err.Error(), "groupName is immutable") {
  		t.Errorf("groupName change: %v", err)
  	}
  	if err := c.Get(ctx, client.ObjectKeyFromObject(good), good); err != nil {
  		t.Fatal(err)
  	}
  	good.Spec.InstallationRef.Name = "other"
  	if err := c.Update(ctx, good); err == nil || !strings.Contains(err.Error(), "installationRef is immutable") {
  		t.Errorf("installationRef change: %v", err)
  	}
  }
  ```
  Run it before writing the CEL markers (`make operator-test` in the dev pod, filtered with
  `go test ./api/... -run CRDValidation` inside the target's environment) and expect FAIL on
  `external-no-secret` (admitted). Add the markers from the table, run `make operator-generate`, and
  expect PASS.
- [x] Write `operator/internal/render/testdata/kw-installation.yaml`: `deploy/kw/values-kw.yaml` as a
      `NexoraInstallation` named `nexora` in namespace `nexora`. Leave out `mgmt.ca`, `mgmt.kek` and
      `engine.groups[].joinTokenSecret`, give group `default` `engineGroupRef: default`, and drop
      `metrics.*.namespace`. Every other key and value is the same.
- [x] Add the CI job to `.github/workflows/ci.yml` after `mgmt`, with the same container, timeout 30 and
      the same "Trust the cluster CA", checkout and `safe.directory` steps, then:
  ```yaml
  - name: gofmt
    run: test -z "$(gofmt -l operator | tee /dev/stderr)"
  - run: cd operator && go vet ./...
  - run: make operator-test
  - name: Generated operator files are up to date
    run: make operator-generate && git diff --exit-code -- operator deploy/operator deploy/helm/nexora-operator
  ```
  Run `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestImagesWorkflow -count=1'` and expect
  PASS (the images workflow is unchanged).
- [x] Run `scripts/dev-exec.sh 'make operator-test'` and `scripts/dev-exec.sh 'cd operator && go vet ./... && go run ./cmd/nexora-operator version'`.
      Expect PASS and `nexora-operator dev`. Report the paths.

## Task 2: Management API contract: system users, error code, migration 01000

Files:

- `mgmt/api/openapi.yaml`: `User.source` enum `[local, oidc, system]`; the `updateUser` and `deleteUser`
  409 descriptions name `system_user`.
- `mgmt/internal/api/gen.go`, `web/src/api/schema.d.ts`: regenerated.
- `mgmt/migrations/01000_system_users.sql`: created.
- `mgmt/internal/store/system_users_migration_test.go`: created.
- `web/src/pages/UsersPage.tsx`: only the source label switch, so the page type-checks against the new
  enum (`system` → "System").

Interfaces:

- SQL: `users_source_check` allows `'local', 'oidc', 'system'`.
- Go: `api.UserSourceSystem` (the generated enum constant).
- TS: `Schemas["User"]["source"]` is `"local" | "oidc" | "system"`.
- Error code string `system_user` (HTTP 409), which Task 3 returns.

- [ ] Create `mgmt/internal/store/system_users_migration_test.go`:
  ```go
  package store_test

  import (
  	"context"
  	"testing"
  	"time"

  	"github.com/jackc/pgx/v5/stdlib"
  	"github.com/pressly/goose/v3"

  	"github.com/piwi3910/nexora/e2e/harness"
  	"github.com/piwi3910/nexora/mgmt/internal/store"
  	"github.com/piwi3910/nexora/mgmt/migrations"
  )

  // An install at the last pre-M9 migration keeps its users and then accepts system users.
  func TestSystemUsersMigration(t *testing.T) {
  	pg := harness.New(t).StartPostgres()
  	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
  	defer cancel()
  	st, err := store.Open(ctx, pg.URL)
  	if err != nil {
  		t.Fatal(err)
  	}
  	defer st.Close()
  	db := stdlib.OpenDBFromPool(st.Pool)
  	defer db.Close()
  	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
  	if err != nil {
  		t.Fatal(err)
  	}
  	sources := provider.ListSources()
  	var before int64
  	for _, s := range sources {
  		if s.Version < 1000 && s.Version > before {
  			before = s.Version
  		}
  	}
  	if _, err := provider.UpTo(ctx, before); err != nil {
  		t.Fatal(err)
  	}
  	if _, err := st.Pool.Exec(ctx, `insert into users(username, role, source, password_hash) values ('alice', 'admin', 'local', 'x')`); err != nil {
  		t.Fatal(err)
  	}
  	if _, err := st.Pool.Exec(ctx, `insert into users(username, role, source) values ('early', 'admin', 'system')`); err == nil {
  		t.Fatal("source system accepted before 01000")
  	}
  	if err := st.Migrate(ctx); err != nil {
  		t.Fatal(err)
  	}
  	var n int
  	if err := st.Pool.QueryRow(ctx, `select count(*) from users where username = 'alice' and source = 'local' and role = 'admin'`).Scan(&n); err != nil || n != 1 {
  		t.Fatalf("alice after migration: n=%d err=%v", n, err)
  	}
  	if _, err := st.Pool.Exec(ctx, `insert into users(username, role, source) values ('nexora-operator', 'admin', 'system')`); err != nil {
  		t.Fatalf("system user refused after 01000: %v", err)
  	}
  	if _, err := st.Pool.Exec(ctx, `insert into users(username, role, source) values ('bad', 'admin', 'robot')`); err == nil {
  		t.Fatal("unknown source accepted")
  	}
  }
  ```
  Run `scripts/dev-exec.sh 'go test ./mgmt/internal/store -run TestSystemUsersMigration -count=1'` and expect
  FAIL `system user refused after 01000` (the migration does not exist yet).
- [ ] Create `mgmt/migrations/01000_system_users.sql`:
  ```sql
  -- +goose Up
  -- System users (source system) own the API tokens of automation such as the Kubernetes operator's
  -- bootstrap token; they have no password and never count as a human user or admin.
  alter table users drop constraint users_source_check;
  alter table users add constraint users_source_check check (source in ('local', 'oidc', 'system'));

  -- +goose Down
  delete from users where source = 'system';
  alter table users drop constraint users_source_check;
  alter table users add constraint users_source_check check (source in ('local', 'oidc'));
  ```
  Run the test again and expect PASS.
- [ ] In `mgmt/api/openapi.yaml`, change the `User` schema's `source` to
      `{ type: string, enum: [local, oidc, system] }`. Set the 409 response of `updateUser` and
      `deleteUser` to
      `{ $ref: "#/components/responses/Error" }` with the description line
      `# 409: stale revision, last admin, or system_user (system users are managed by automation)` as a
      YAML comment above it. Regenerate:
      `cd mgmt/api && go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 -config oapi-codegen.yaml openapi.yaml`
      (v2.6.0 fails on this spec) and `cd web && pnpm run gen:api` (openapi-typescript 7.13.0). With a
      third enum value the generator prefixes the constants: `api.UserSourceLocal`, `api.UserSourceOidc`,
      `api.UserSourceSystem` (the old `api.Local`/`api.Oidc` had no users outside `gen.go`).
- [ ] In `web/src/pages/UsersPage.tsx` replace
      `{u.source === "oidc" ? "Identity provider" : "Password"}` with
      `{u.source === "oidc" ? "Identity provider" : u.source === "system" ? "System" : "Password"}`.
      Run `cd web && pnpm run typecheck && pnpm run lint` and expect PASS.
- [ ] Run `scripts/dev-exec.sh 'go vet ./... && go test -count=1 ./mgmt/internal/api/... ./mgmt/internal/store/...'`
      and expect PASS. Report the paths.

## Task 3: Bootstrap token and system users in the management plane

Files:

- `mgmt/internal/auth/bootstrap.go`: created; `EnsureBootstrapToken`, `RunBootstrapToken`, errors, metric.
- `mgmt/internal/auth/bootstrap_test.go`: created.
- `mgmt/internal/auth/service.go`: the setup-required counts exclude system users; login refuses system
  users.
- `mgmt/internal/api/handlers_admin.go`: `UpdateUser`/`DeleteUser` return 409 `system_user`;
  `ensureOtherAdmin` excludes system users.
- `mgmt/internal/api/system_user_test.go`: created.
- `mgmt/internal/config/config.go`, `mgmt/internal/config/config_test.go`: `BootstrapTokenFile`,
  `BootstrapTokenReloadInterval`, and `TestConfigBootstrapToken`.
- `mgmt/cmd/nexora-mgmt/main.go`: starts the loop and registers the counter.
- `e2e/bootstrap_token_test.go`: created.
- `web/src/pages/UsersPage.tsx`: hides edit and delete for system users.
- `web/e2e/screens/71-system-user.spec.ts`: created.
- `docs/architecture.md`: only the two env lines in the management-plane environment list.

Interfaces:

```go
package auth
const (
	BootstrapUsername  = "nexora-operator"
	BootstrapTokenName = "bootstrap"
)
var (
	ErrBootstrapTokenInvalid = errors.New("bootstrap token: the file does not hold an nxt_ token")
	ErrBootstrapUserConflict = errors.New("bootstrap token: user nexora-operator exists and is not a system user")
	BootstrapTokenErrors     = prometheus.NewCounter(prometheus.CounterOpts{Name: "nexora_mgmt_bootstrap_token_errors_total", Help: "Failed applications of the bootstrap token file."})
)
// EnsureBootstrapToken makes token the only unrevoked bootstrap token of the system user (created when
// missing) and reports whether it wrote anything; a write also writes the audit action ensureBootstrapToken.
func (s *Service) EnsureBootstrapToken(ctx context.Context, token string) (changed bool, err error)
// RunBootstrapToken applies file at start and every interval until ctx ends. Errors are logged through
// logf without the file content and counted in BootstrapTokenErrors.
func (s *Service) RunBootstrapToken(ctx context.Context, file string, interval time.Duration, logf func(format string, args ...any))

package config
// Config gains:
BootstrapTokenFile           string        // NEXORA_BOOTSTRAP_TOKEN_FILE
BootstrapTokenReloadInterval time.Duration // NEXORA_BOOTSTRAP_TOKEN_RELOAD_INTERVAL, default 30s, at least 1s
```

- [ ] Create `mgmt/internal/auth/bootstrap_test.go`:
  ```go
  package auth_test

  import (
  	"context"
  	"errors"
  	"net/http"
  	"strings"
  	"testing"

  	"github.com/piwi3910/nexora/mgmt/internal/auth"
  	"github.com/piwi3910/nexora/mgmt/internal/store/storetest"
  )

  const tokA = "nxt_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
  const tokB = "nxt_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

  func bearer(tok string) *http.Request {
  	r, _ := http.NewRequest(http.MethodGet, "/", nil)
  	r.Header.Set("Authorization", "Bearer "+tok)
  	return r
  }

  func TestBootstrapTokenEnsuresSystemUser(t *testing.T) {
  	st := storetest.New(t)
  	svc := auth.NewService(st, false)
  	ctx := context.Background()
  	if _, err := svc.EnsureBootstrapToken(ctx, "not-a-token"); !errors.Is(err, auth.ErrBootstrapTokenInvalid) {
  		t.Fatalf("invalid token: %v", err)
  	}
  	var users int
  	_ = st.Pool.QueryRow(ctx, "select count(*) from users").Scan(&users)
  	if users != 0 {
  		t.Fatalf("an invalid token created %d users", users)
  	}
  	changed, err := svc.EnsureBootstrapToken(ctx, tokA)
  	if err != nil || !changed {
  		t.Fatalf("first ensure: changed=%v err=%v", changed, err)
  	}
  	var source, role string
  	var hash *string
  	if err := st.Pool.QueryRow(ctx, "select source, role, password_hash from users where username = $1", auth.BootstrapUsername).Scan(&source, &role, &hash); err != nil {
  		t.Fatal(err)
  	}
  	if source != "system" || role != "admin" || hash != nil {
  		t.Fatalf("system user: source=%s role=%s hash=%v", source, role, hash)
  	}
  	p, err := svc.Authenticate(ctx, bearer(tokA))
  	if err != nil || p.Role != auth.RoleAdmin || p.Username != auth.BootstrapUsername {
  		t.Fatalf("token principal %+v err=%v", p, err)
  	}
  	var audits int
  	_ = st.Pool.QueryRow(ctx, "select count(*) from audit_log where action = 'ensureBootstrapToken'").Scan(&audits)
  	changed, err = svc.EnsureBootstrapToken(ctx, tokA)
  	if err != nil || changed {
  		t.Fatalf("second ensure: changed=%v err=%v", changed, err)
  	}
  	var audits2, tokens int
  	_ = st.Pool.QueryRow(ctx, "select count(*) from audit_log where action = 'ensureBootstrapToken'").Scan(&audits2)
  	_ = st.Pool.QueryRow(ctx, "select count(*) from api_tokens").Scan(&tokens)
  	if audits != 1 || audits2 != 1 || tokens != 1 {
  		t.Fatalf("idempotence: audits %d->%d tokens %d", audits, audits2, tokens)
  	}
  	var diff string
  	_ = st.Pool.QueryRow(ctx, "select diff::text from audit_log where action = 'ensureBootstrapToken'").Scan(&diff)
  	if strings.Contains(diff, tokA) || strings.Contains(diff, tokA[4:]) {
  		t.Fatal("audit diff contains the token")
  	}
  }

  func TestBootstrapTokenRotation(t *testing.T) {
  	st := storetest.New(t)
  	svc := auth.NewService(st, false)
  	ctx := context.Background()
  	if _, err := svc.EnsureBootstrapToken(ctx, tokA); err != nil {
  		t.Fatal(err)
  	}
  	if _, err := svc.EnsureBootstrapToken(ctx, tokB); err != nil {
  		t.Fatal(err)
  	}
  	if _, err := svc.Authenticate(ctx, bearer(tokB)); err != nil {
  		t.Fatalf("new token: %v", err)
  	}
  	if _, err := svc.Authenticate(ctx, bearer(tokA)); !errors.Is(err, auth.ErrUnauthenticated) {
  		t.Fatalf("old token after rotation: %v", err)
  	}
  }

  func TestBootstrapTokenRefusesHumanUser(t *testing.T) {
  	st := storetest.New(t)
  	svc := auth.NewService(st, false)
  	ctx := context.Background()
  	if _, err := st.Pool.Exec(ctx, "insert into users(username, role, source, password_hash) values ($1, 'viewer', 'local', 'x')", auth.BootstrapUsername); err != nil {
  		t.Fatal(err)
  	}
  	if _, err := svc.EnsureBootstrapToken(ctx, tokA); !errors.Is(err, auth.ErrBootstrapUserConflict) {
  		t.Fatalf("human user: %v", err)
  	}
  	var tokens int
  	_ = st.Pool.QueryRow(ctx, "select count(*) from api_tokens").Scan(&tokens)
  	if tokens != 0 {
  		t.Fatal("a token was created for a human user")
  	}
  }

  func TestSetupRequiredIgnoresSystemUsers(t *testing.T) {
  	st := storetest.New(t)
  	svc := auth.NewService(st, false)
  	ctx := context.Background()
  	if _, err := svc.EnsureBootstrapToken(ctx, tokA); err != nil {
  		t.Fatal(err)
  	}
  	if req, err := svc.SetupRequired(ctx); err != nil || !req {
  		t.Fatalf("setup required with only a system user: %v %v", req, err)
  	}
  	tok, created, err := svc.EnsureSetupToken(ctx, "test")
  	if err != nil || !created {
  		t.Fatalf("setup token: created=%v err=%v", created, err)
  	}
  	if _, err := svc.CompleteSetup(ctx, tok, "admin", "a@x", "admin-password-1"); err != nil {
  		t.Fatalf("complete setup: %v", err)
  	}
  	if _, _, err := svc.Login(ctx, auth.BootstrapUsername, "", "127.0.0.1"); err == nil {
  		t.Fatal("system user logged in")
  	}
  }
  ```
  Run `scripts/dev-exec.sh 'go test ./mgmt/internal/auth -run "Bootstrap|SystemUsers" -count=1'` and expect
  a build failure `undefined: auth.EnsureBootstrapToken` (method) and `auth.BootstrapUsername`.
- [ ] Implement `mgmt/internal/auth/bootstrap.go`. Validate `strings.HasPrefix(token, apiTokenPrefix)` and
      `len(token) > len(apiTokenPrefix)+8` before any database work, else `ErrBootstrapTokenInvalid`. Then,
      in one `s.st.InTx`:
  1. `select pg_advisory_xact_lock(hashtext('nexora:bootstrap_token'))`.
  2. (validation, done above).
  3. `select id, source from users where username = $1 for update`; a non-`system` row →
     `ErrBootstrapUserConflict`; no row → insert
     `(username, role, source, disabled) values ('nexora-operator', 'admin', 'system', false)`; an existing
     system row with `role <> 'admin' or disabled` → update to admin, not disabled.
  4. Select the user's unrevoked, unexpired `bootstrap` token hashes and compare each with
     `hashToken(token)` using `subtle.ConstantTimeCompare`.
  5. When none matches, delete any stale (revoked or expired) row of that user with the same hash
     (`token_hash` is unique, so a file rolled back to an old token would otherwise conflict), then insert
     `api_tokens(user_id, name, prefix, token_hash, role)` with name `bootstrap`, prefix `token[:12]`,
     role `admin`. In every case run `update api_tokens set revoked_at = now() where user_id = $1
and name = 'bootstrap' and revoked_at is null and token_hash <> $2`. `changed` is true when step 3,
     the insert or a revocation wrote; otherwise return without an audit row.
  6. When `changed`, write `WriteAudit(ctx, tx, Actor{Type: "system", ID: "bootstrap", Name: "bootstrap"},
Change{Action: "ensureBootstrapToken", TargetType: "user", TargetID: userID,
After: map[string]any{"token_prefix": token[:12]}}, nil)`.

  `RunBootstrapToken` reads the file, applies `strings.TrimSpace`, calls `EnsureBootstrapToken`, and on
  error calls `logf("bootstrap token: %v", err)` and `BootstrapTokenErrors.Inc()`. It then waits
  `interval` or `ctx.Done()`.

- [ ] In `mgmt/internal/auth/service.go` change the three `select count(*) from users` statements to
      `select count(*) from users where source <> 'system'`, and make `Login` fail with the wrong-password
      error for a `system` user before the hash check. Run the auth tests and expect PASS.
- [ ] Create `mgmt/internal/api/system_user_test.go`:
  ```go
  package api_test

  import (
  	"net/http"
  	"testing"
  )

  func TestSystemUserIsReadOnlyInAPI(t *testing.T) {
  	e := newAPI(t)
  	admin := e.client(t)
  	if code := admin.do("POST", "/setup", map[string]string{"token": e.setup, "username": "admin", "email": "a@x", "password": "admin-password-1"}, nil); code != 201 {
  		t.Fatalf("setup -> %d", code)
  	}
  	if _, err := e.svc.EnsureBootstrapToken(e.ctx, "nxt_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"); err != nil {
  		t.Fatal(err)
  	}
  	var users []map[string]any
  	if code := admin.do("GET", "/users", nil, &users); code != 200 {
  		t.Fatalf("list users -> %d", code)
  	}
  	var sysID, adminID string
  	var sysRev, adminRev float64
  	for _, u := range users {
  		switch u["username"] {
  		case "nexora-operator":
  			sysID, sysRev = u["id"].(string), u["revision"].(float64)
  			if u["source"] != "system" {
  				t.Fatalf("system user source = %v", u["source"])
  			}
  		case "admin":
  			adminID, adminRev = u["id"].(string), u["revision"].(float64)
  		}
  	}
  	if sysID == "" || adminID == "" {
  		t.Fatalf("users = %v", users)
  	}
  	var apiErr map[string]string
  	if code := admin.do("PUT", "/users/"+sysID, map[string]any{"role": "viewer", "email": "", "disabled": true, "revision": sysRev}, &apiErr); code != http.StatusConflict || apiErr["code"] != "system_user" {
  		t.Fatalf("update system user -> %d %v", code, apiErr)
  	}
  	if code := admin.do("DELETE", "/users/"+sysID+"?revision=1", nil, &apiErr); code != http.StatusConflict || apiErr["code"] != "system_user" {
  		t.Fatalf("delete system user -> %d %v", code, apiErr)
  	}
  	anon := e.client(t)
  	if code := anon.do("POST", "/auth/login", map[string]string{"username": "nexora-operator", "password": ""}, nil); code != http.StatusUnauthorized {
  		t.Fatalf("system login -> %d", code)
  	}
  	if code := admin.do("DELETE", "/users/"+adminID+"?revision="+formatRev(adminRev), nil, &apiErr); code != http.StatusConflict {
  		t.Fatalf("deleting the last human admin -> %d %v", code, apiErr)
  	}
  }

  func formatRev(f float64) string { return strconv.FormatInt(int64(f), 10) }
  ```
  (add `"strconv"` to the imports). Run
  `scripts/dev-exec.sh 'go test ./mgmt/internal/api -run TestSystemUserIsReadOnlyInAPI -count=1'` and expect
  FAIL `update system user -> 200`.
- [ ] In `handlers_admin.go`, after `lockUser` in `UpdateUser` and `DeleteUser`, return
      `errSystemUser = coded(http.StatusConflict, "system_user", "system users are managed by automation")`
      when `before.Source == "system"`. Add `and source <> 'system'` to the query in `ensureOtherAdmin`. Run
      and expect PASS. If the delete of the self-admin answers 409 with a different code because it is
      the caller's own account, keep the assertion on 409 only, as written.
- [ ] Add `TestConfigBootstrapToken` to `mgmt/internal/config/config_test.go`: with only the required
      env set, `BootstrapTokenFile == ""` and `BootstrapTokenReloadInterval == 30*time.Second`;
      both set give those values; `NEXORA_BOOTSTRAP_TOKEN_RELOAD_INTERVAL=500ms` gives an error mentioning
      `NEXORA_BOOTSTRAP_TOKEN_RELOAD_INTERVAL`. Implement, and expect PASS.
- [ ] In `main.go` `serve`, register `auth.BootstrapTokenErrors` next to `pki.DNSTLSReloadErrors`, and
      after `EnsureSetupToken` add
      `if cfg.BootstrapTokenFile != "" { go authSvc.RunBootstrapToken(ctx, cfg.BootstrapTokenFile, cfg.BootstrapTokenReloadInterval, logger.Printf) }`,
      using the logger `serve` already uses (`log.Printf`).
- [ ] Create `e2e/bootstrap_token_test.go`:
  ```go
  package e2e

  import (
  	"net/http"
  	"os"
  	"path/filepath"
  	"testing"
  	"time"

  	"github.com/piwi3910/nexora/e2e/harness"
  )

  func TestBootstrapTokenFleetBootstrap(t *testing.T) {
  	env := harness.New(t)
  	pg := env.StartPostgres()
  	ca := env.InitCA()
  	tokFile := filepath.Join(t.TempDir(), "token")
  	const first = "nxt_DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD"
  	const second = "nxt_EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE"
  	if err := os.WriteFile(tokFile, []byte(first+"\n"), 0o600); err != nil {
  		t.Fatal(err)
  	}
  	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{ExtraEnv: []string{
  		"NEXORA_BOOTSTRAP_TOKEN_FILE=" + tokFile, "NEXORA_BOOTSTRAP_TOKEN_RELOAD_INTERVAL=1s"}})
  	api := env.NewAPI(mg.BaseURL)
  	api.Bearer = first
  	harness.EventuallyTrue(t, 10*time.Second, func() bool {
  		code, _ := api.Do(http.MethodGet, "/engine-groups", nil, nil)
  		return code == http.StatusOK
  	}, "the bootstrap token authenticates")
  	g := api.CreateEngineGroup(map[string]any{"name": "boot"})
  	join := api.CreateJoinTokenFor(g.ID, nil)
  	en := env.StartManagedEngine("boot-1", []string{mg.GRPCURL}, join)
  	_ = en
  	v := api.WaitEngine("boot-1", 30*time.Second, func(e harness.EngineView) bool { return e.Connected })
  	if v.EngineGroupID != g.ID {
  		t.Fatalf("engine enrolled into %s, want %s", v.EngineGroupID, g.ID)
  	}
  	var setup map[string]bool
  	api.Must(http.MethodGet, "/setup", nil, &setup, http.StatusOK)
  	if !setup["required"] {
  		t.Fatal("setup is no longer required after the bootstrap token created a system user")
  	}
  	if err := os.WriteFile(tokFile, []byte(second), 0o600); err != nil {
  		t.Fatal(err)
  	}
  	next := env.NewAPI(mg.BaseURL)
  	next.Bearer = second
  	harness.EventuallyTrue(t, 5*time.Second, func() bool {
  		oldCode, _ := api.Do(http.MethodGet, "/engine-groups", nil, nil)
  		newCode, _ := next.Do(http.MethodGet, "/engine-groups", nil, nil)
  		return oldCode == http.StatusUnauthorized && newCode == http.StatusOK
  	}, "the rotated token replaces the old one")
  	next.CreateEngineGroup(map[string]any{"name": "boot2"})
  }
  ```
  Run `scripts/dev-exec.sh 'make e2e-build && NEXORA_E2E_BIN_DIR=bin go test ./e2e -run TestBootstrapTokenFleetBootstrap -count=1'`.
  Expect PASS once the steps above are in; with the `main.go` step removed it fails in the first
  `EventuallyTrue`. `harness.EventuallyTrue` takes a message argument. The rotation check polls with GET and
  creates `boot2` once afterwards: a POST inside the poll would answer 409 on every retry after its first
  success.
- [ ] In `web/src/pages/UsersPage.tsx`, render the row actions only when `u.source !== "system"`. Create
      `web/e2e/screens/71-system-user.spec.ts`:
  ```ts
  import { test, expect, env, login } from "../fixtures";

  test("system users show as System without edit or delete", async ({
    page,
  }) => {
    await login(
      page,
      env("NEXORA_E2E_ADMIN_USER"),
      env("NEXORA_E2E_ADMIN_PASSWORD"),
    );
    await page.route("**/api/v1/users", async (route) => {
      const res = await route.fetch();
      const users = await res.json();
      users.push({
        id: "00000000-0000-0000-0000-00000000cafe",
        username: "nexora-operator",
        email: "",
        role: "admin",
        source: "system",
        disabled: false,
        revision: 1,
        created_at: new Date().toISOString(),
        display_name: "",
        last_login_at: null,
        preferences: {
          theme: "system",
          time_zone: "",
          clock_24h: true,
          querylog_live: false,
        },
      });
      await route.fulfill({ response: res, json: users });
    });
    await page.goto("/users");
    const row = page.getByRole("row", { name: /nexora-operator/ });
    await expect(row).toContainText("System");
    await expect(row.getByRole("button")).toHaveCount(0);
    const admin = page.getByRole("row", { name: /admin@|\badmin\b/ }).first();
    await expect(admin.getByRole("button").first()).toBeVisible();
  });
  ```
  `TestGUICoverage` has no per-spec subtests; run
  `scripts/dev-exec.sh 'go test ./e2e -run "^TestGUICoverage$" -count=1 -timeout 40m'` (for a quick
  red/green, a throwaway e2e test running `00-setup` then `71-system-user` through
  `harness.RunPlaywright`, deleted afterwards). Expect FAIL on `toHaveCount(0)` (`unexpected value "2"`)
  before the UsersPage change and PASS after.
- [ ] Add `NEXORA_BOOTSTRAP_TOKEN_FILE` and `NEXORA_BOOTSTRAP_TOKEN_RELOAD_INTERVAL` (`30s`) to the
      management environment list in `docs/architecture.md`. Run
      `scripts/dev-exec.sh 'go vet ./... && go test -race -count=1 ./mgmt/...'` and
      `cd web && pnpm run typecheck && pnpm run lint`, and expect PASS. Report the paths.

## Task 4: Nexora chart: bootstrap token value, CNPG HA, backups and recovery

Files:

- `deploy/deploytest/testdata/kw-render.golden.yaml`: created first, from the chart before any edit.
- `deploy/deploytest/helm_cnpg_test.go`: created; `TestHelmKwRenderUnchanged`, `TestHelmCNPGHighAvailability`,
  `TestHelmCNPGBackups`, `TestHelmCNPGRecovery`, `TestHelmBootstrapToken`.
- `deploy/helm/nexora/values.yaml`, `deploy/helm/nexora/values.schema.json`: the new values.
- `deploy/helm/nexora/templates/database-cnpg.yaml`: HA fields, backup, ScheduledBackup, recovery.
- `deploy/helm/nexora/templates/mgmt-deployment.yaml`: the bootstrap token env, volume and mount.
- `deploy/helm/nexora/ci/lint-values.yaml`: exercises backup values.

Interfaces (values consumed by Task 1's CRD mirror and Task 6):

```yaml
mgmt:
  bootstrapToken:
    existingSecret: "" # Secret with key "token" (an nxt_ API token); set by the operator
database:
  cnpg:
    resources: {}
    antiAffinity: preferred # preferred | required
    primaryUpdateMethod: switchover # switchover | restart
    postgresql:
      parameters: {}
    backup:
      enabled: false
      destinationPath: "" # s3://bucket/path
      endpointURL: ""
      serverName: "" # default: clusterName
      s3Credentials:
        {
          existingSecret: "",
          accessKeyIdKey: ACCESS_KEY_ID,
          secretAccessKeyKey: ACCESS_SECRET_KEY,
        }
      endpointCA: { existingSecret: "", key: ca.crt }
      retentionPolicy: 30d
      walCompression: gzip
      dataCompression: gzip
      schedule: "0 0 3 * * *" # six fields, seconds first
      immediate: false
    recovery:
      enabled: false
      sourceServerName: ""
      destinationPath: "" # default: backup.destinationPath
      endpointURL: "" # default: backup.endpointURL
      s3Credentials: {
          existingSecret: "",
          accessKeyIdKey: "",
          secretAccessKeyKey: "",
        } # each default: backup.s3Credentials.*
      endpointCA: { existingSecret: "", key: "" } # each default: backup.endpointCA.*
      targetTime: "" # RFC 3339; empty recovers to the end of the WAL archive
```

Rendered objects: `Cluster.spec.affinity`, `spec.primaryUpdateStrategy: unsupervised`,
`spec.primaryUpdateMethod`, `spec.resources`, `spec.postgresql.parameters`, `spec.backup`,
`bootstrap.recovery` and `externalClusters`. The ScheduledBackup is named `<clusterName>-scheduled`.
Mgmt env `NEXORA_BOOTSTRAP_TOKEN_FILE=/etc/nexora/bootstrap-token/token`, volume `bootstrap-token`.

- [x] Before editing any chart file, capture the golden render in the dev pod:
      `scripts/dev-exec.sh 'helm template nexora deploy/helm/nexora --namespace nexora -f deploy/kw/values-kw.yaml --api-versions monitoring.coreos.com/v1 --set image.tag=golden' > deploy/deploytest/testdata/kw-render.golden.yaml`,
      writing the file on the laptop (redirect the command's stdout there; a file written only in the pod is
      removed by the next `dev-sync.sh --delete`). Do not regenerate it with the laptop's `helm`: Helm 4.1
      emits different blank lines between documents than the pod's Helm 4.3, and the test runs in the pod.
- [x] Create `deploy/deploytest/helm_cnpg_test.go`:
  ```go
  package deploytest

  import (
  	"os"
  	"strings"
  	"testing"
  )

  var base = []string{"--set", "mgmt.ca.existingSecret=ca", "--api-versions", "postgresql.cnpg.io/v1",
  	"--set-json", `engine.groups=[{"name":"default","joinTokenSecret":"jt"}]`}

  func with(extra ...string) []string { return append(append([]string{}, base...), extra...) }

  func renderErr(t *testing.T, args ...string) string {
  	t.Helper()
  	out, err := helm(append([]string{"template", "nexora", chartDir, "--namespace", "nexora"}, args...)...)
  	if err == nil {
  		t.Fatalf("helm template %v succeeded, want a failure", args)
  	}
  	return out
  }

  func TestHelmKwRenderUnchanged(t *testing.T) {
  	want, err := os.ReadFile("testdata/kw-render.golden.yaml")
  	if err != nil {
  		t.Fatal(err)
  	}
  	got, err := helm("template", "nexora", chartDir, "--namespace", "nexora", "-f", "../kw/values-kw.yaml",
  		"--api-versions", "monitoring.coreos.com/v1", "--set", "image.tag=golden")
  	if err != nil {
  		t.Fatalf("helm template: %v\n%s", err, got)
  	}
  	if got != string(want) {
  		t.Fatal("the kw render changed; production output must stay byte-identical (compare with testdata/kw-render.golden.yaml)")
  	}
  }

  func TestHelmCNPGHighAvailability(t *testing.T) {
  	docs := render(t, with("--set", "database.cnpg.instances=3", "--set", "database.cnpg.antiAffinity=required",
  		"--set", "database.cnpg.primaryUpdateMethod=switchover", "--set", "database.cnpg.resources.requests.memory=512Mi",
  		"--set", "database.cnpg.postgresql.parameters.max_connections=200")...)
  	c := find(t, docs, "Cluster", "nexora-db")
  	checks := map[string][2]any{
  		"instances":        {c.path("spec", "instances"), 3},
  		"antiAffinity":     {c.path("spec", "affinity", "enablePodAntiAffinity"), true},
  		"topologyKey":      {c.path("spec", "affinity", "topologyKey"), "kubernetes.io/hostname"},
  		"type":             {c.path("spec", "affinity", "podAntiAffinityType"), "required"},
  		"updateStrategy":   {c.path("spec", "primaryUpdateStrategy"), "unsupervised"},
  		"updateMethod":     {c.path("spec", "primaryUpdateMethod"), "switchover"},
  		"memory":           {c.path("spec", "resources", "requests", "memory"), "512Mi"},
  		"max_connections":  {c.path("spec", "postgresql", "parameters", "max_connections"), "200"},
  	}
  	for name, v := range checks {
  		if v[0] != v[1] {
  			t.Errorf("%s = %v, want %v", name, v[0], v[1])
  		}
  	}
  	ref := env(container(t, find(t, docs, "Deployment", "nexora-mgmt"), "mgmt"), "NEXORA_DATABASE_URL").path("valueFrom", "secretKeyRef").(map[string]any)
  	if ref["name"] != "nexora-db-app" || ref["key"] != "uri" {
  		t.Errorf("mgmt database secret = %v", ref)
  	}
  	if has(docs, "ScheduledBackup") || c.path("spec", "backup") != nil {
  		t.Error("backup renders while disabled")
  	}
  }

  func TestHelmCNPGBackups(t *testing.T) {
  	b := []string{"--set", "database.cnpg.backup.enabled=true", "--set", "database.cnpg.backup.destinationPath=s3://bkt/nexora",
  		"--set", "database.cnpg.backup.endpointURL=http://minio:9000", "--set", "database.cnpg.backup.s3Credentials.existingSecret=s3",
  		"--set", "database.cnpg.backup.schedule=0 30 2 * * *", "--set", "database.cnpg.backup.immediate=true"}
  	docs := render(t, with(b...)...)
  	c := find(t, docs, "Cluster", "nexora-db")
  	bos := obj(c.path("spec", "backup", "barmanObjectStore").(map[string]any))
  	for path, want := range map[string]any{
  		"destinationPath": "s3://bkt/nexora", "endpointURL": "http://minio:9000", "serverName": "nexora-db",
  	} {
  		if bos[path] != want {
  			t.Errorf("barmanObjectStore.%s = %v, want %v", path, bos[path], want)
  		}
  	}
  	if bos.path("s3Credentials", "accessKeyId", "name") != "s3" || bos.path("s3Credentials", "accessKeyId", "key") != "ACCESS_KEY_ID" ||
  		bos.path("s3Credentials", "secretAccessKey", "key") != "ACCESS_SECRET_KEY" {
  		t.Errorf("s3Credentials = %v", bos["s3Credentials"])
  	}
  	if bos.path("wal", "compression") != "gzip" || bos.path("data", "compression") != "gzip" || c.path("spec", "backup", "retentionPolicy") != "30d" {
  		t.Errorf("compression/retention = %v", c.path("spec", "backup"))
  	}
  	if bos["endpointCA"] != nil {
  		t.Error("endpointCA renders without a secret")
  	}
  	sb := find(t, docs, "ScheduledBackup", "nexora-db-scheduled")
  	if sb.path("spec", "schedule") != "0 30 2 * * *" || sb.path("spec", "cluster", "name") != "nexora-db" ||
  		sb.path("spec", "method") != "barmanObjectStore" || sb.path("spec", "backupOwnerReference") != "self" || sb.path("spec", "immediate") != true {
  		t.Errorf("scheduled backup = %v", sb["spec"])
  	}
  	if out := renderErr(t, with("--set", "database.cnpg.backup.enabled=true", "--set", "database.cnpg.backup.s3Credentials.existingSecret=s3")...); !strings.Contains(out, "database.cnpg.backup.destinationPath is required") {
  		t.Errorf("missing path error: %s", out)
  	}
  	if out := renderErr(t, with("--set", "database.cnpg.backup.enabled=true", "--set", "database.cnpg.backup.destinationPath=s3://b/p")...); !strings.Contains(out, "database.cnpg.backup.s3Credentials.existingSecret is required") {
  		t.Errorf("missing credentials error: %s", out)
  	}
  }

  func TestHelmCNPGRecovery(t *testing.T) {
  	b := []string{"--set", "database.cnpg.backup.destinationPath=s3://bkt/nexora", "--set", "database.cnpg.backup.endpointURL=http://minio:9000",
  		"--set", "database.cnpg.backup.s3Credentials.existingSecret=s3", "--set", "database.cnpg.recovery.enabled=true",
  		"--set", "database.cnpg.recovery.sourceServerName=nexora-db", "--set", "database.cnpg.clusterName=nexora-db-restore"}
  	docs := render(t, with(b...)...)
  	c := find(t, docs, "Cluster", "nexora-db-restore")
  	if c.path("spec", "bootstrap", "initdb") != nil {
  		t.Error("initdb renders next to recovery")
  	}
  	if c.path("spec", "bootstrap", "recovery", "source") != "backup-source" || c.path("spec", "bootstrap", "recovery", "database") != "nexora" || c.path("spec", "bootstrap", "recovery", "owner") != "nexora" {
  		t.Errorf("recovery = %v", c.path("spec", "bootstrap"))
  	}
  	ext := c.path("spec", "externalClusters").([]any)
  	e0 := obj(ext[0].(map[string]any))
  	if e0["name"] != "backup-source" || e0.path("barmanObjectStore", "serverName") != "nexora-db" ||
  		e0.path("barmanObjectStore", "destinationPath") != "s3://bkt/nexora" || e0.path("barmanObjectStore", "endpointURL") != "http://minio:9000" ||
  		e0.path("barmanObjectStore", "s3Credentials", "accessKeyId", "name") != "s3" {
  		t.Errorf("external cluster = %v", e0)
  	}
  	withTarget := render(t, with(append(b, "--set", "database.cnpg.recovery.targetTime=2026-09-15T03:00:00Z")...)...)
  	if find(t, withTarget, "Cluster", "nexora-db-restore").path("spec", "bootstrap", "recovery", "recoveryTarget", "targetTime") != "2026-09-15T03:00:00Z" {
  		t.Error("targetTime not rendered")
  	}
  	collide := with("--set", "database.cnpg.backup.enabled=true", "--set", "database.cnpg.backup.destinationPath=s3://bkt/nexora",
  		"--set", "database.cnpg.backup.s3Credentials.existingSecret=s3", "--set", "database.cnpg.recovery.enabled=true",
  		"--set", "database.cnpg.recovery.sourceServerName=nexora-db")
  	if out := renderErr(t, collide...); !strings.Contains(out, "would archive into the backup it restores from") {
  		t.Errorf("collision error: %s", out)
  	}
  	if out := renderErr(t, with("--set", "database.cnpg.recovery.enabled=true")...); !strings.Contains(out, "database.cnpg.recovery.sourceServerName is required") {
  		t.Errorf("missing source error: %s", out)
  	}
  }

  func TestHelmBootstrapToken(t *testing.T) {
  	off := container(t, find(t, render(t, base...), "Deployment", "nexora-mgmt"), "mgmt")
  	if env(off, "NEXORA_BOOTSTRAP_TOKEN_FILE") != nil {
  		t.Error("bootstrap token env renders without the value")
  	}
  	docs := render(t, with("--set", "mgmt.bootstrapToken.existingSecret=tok")...)
  	d := find(t, docs, "Deployment", "nexora-mgmt")
  	c := container(t, d, "mgmt")
  	if e := env(c, "NEXORA_BOOTSTRAP_TOKEN_FILE"); e == nil || e["value"] != "/etc/nexora/bootstrap-token/token" {
  		t.Errorf("env = %v", e)
  	}
  	var mounted, volume bool
  	for _, m := range c["volumeMounts"].([]any) {
  		mm := m.(map[string]any)
  		mounted = mounted || (mm["name"] == "bootstrap-token" && mm["mountPath"] == "/etc/nexora/bootstrap-token" && mm["readOnly"] == true)
  	}
  	for _, v := range d.path("spec", "template", "spec", "volumes").([]any) {
  		vv := obj(v.(map[string]any))
  		volume = volume || (vv["name"] == "bootstrap-token" && vv.path("secret", "secretName") == "tok" && vv.path("secret", "defaultMode") == 288)
  	}
  	if !mounted || !volume {
  		t.Errorf("mount %v volume %v", mounted, volume)
  	}
  }
  ```
  Run `scripts/dev-exec.sh 'go test ./deploy/deploytest -run "TestHelmKwRenderUnchanged|TestHelmCNPG|TestHelmBootstrapToken" -count=1'`.
  Expect PASS for `TestHelmKwRenderUnchanged` and FAIL for the other four (for example
  `antiAffinity = <nil>, want true`).
- [x] Add the values from Interfaces to `values.yaml` and their schema to `values.schema.json`:
      enums for `antiAffinity` and `primaryUpdateMethod`; `schedule` pattern
      `^\\S+( \\S+){5}$`; `postgresql.parameters` as an object of scalars
      (`string`, `number` or `boolean`, because `--set ...max_connections=200` parses as a number; the
      template always renders them as quoted strings).
- [x] Edit `templates/database-cnpg.yaml`:
  - Always render `affinity: { enablePodAntiAffinity: true, topologyKey: kubernetes.io/hostname,
podAntiAffinityType: <antiAffinity> }`, `primaryUpdateStrategy: unsupervised`,
    `primaryUpdateMethod`, `resources` (when non-empty) and `postgresql.parameters` (when non-empty,
    values quoted).
  - With `backup.enabled`, render `backup.barmanObjectStore`:
    - `required "database.cnpg.backup.destinationPath is required when database.cnpg.backup.enabled"`
      on the path;
    - `endpointURL` when set;
    - `serverName | default clusterName`;
    - `s3Credentials.accessKeyId/secretAccessKey {name, key}` with
      `required "database.cnpg.backup.s3Credentials.existingSecret is required when database.cnpg.backup.enabled"`;
    - `endpointCA {name, key}` only with a Secret;
    - `wal.compression` and `data.compression`.

    Also render `backup.retentionPolicy`, and a second document `ScheduledBackup`
    `<clusterName>-scheduled` with `schedule`, `immediate`, `backupOwnerReference: self`,
    `cluster.name` and `method: barmanObjectStore`.

  - With `recovery.enabled`, render `bootstrap.recovery` (`source: backup-source`, `database: nexora`,
    `owner: nexora`, `recoveryTarget.targetTime` when set) instead of `initdb`. Render
    `externalClusters: [{ name: backup-source, barmanObjectStore: { ... } }]` from the recovery values,
    each empty field falling back to `backup.*`, with
    `required "database.cnpg.recovery.sourceServerName is required when database.cnpg.recovery.enabled"`
    (checked first), then `required` on the effective `destinationPath` and
    `s3Credentials.existingSecret`. The object-store stanza shared by `backup` and `externalClusters`
    is the `define "nexora.cnpgObjectStore"` at the top of the template.
    Fail with
    `database.cnpg.recovery.sourceServerName equals the backup serverName on the same destinationPath: the restored cluster would archive into the backup it restores from; set database.cnpg.backup.serverName`
    when backup is enabled, the effective paths are equal and the server names are equal.
  - Add the template comment (`{{- /* ... */}}`, so it stays out of the rendered output)
    `debt: in-tree barmanObjectStore (deprecated since CNPG 1.26, present in kw's 1.29.1); move to the Barman Cloud plugin when a CNPG release removes the field or the plugin is installed on kw.`
- [x] Edit `templates/mgmt-deployment.yaml`: with `mgmt.bootstrapToken.existingSecret`, add env
      `NEXORA_BOOTSTRAP_TOKEN_FILE=/etc/nexora/bootstrap-token/token`, the mount
      `{ name: bootstrap-token, mountPath: /etc/nexora/bootstrap-token, readOnly: true }` and the volume
      `secret: { secretName: <value>, defaultMode: 0440 }`.
- [x] Add to `ci/lint-values.yaml`:
  ```yaml
  cnpg:
    backup:
      enabled: true
      destinationPath: s3://nexora-lint/backups
      s3Credentials: { existingSecret: nexora-s3 }
  ```
  under the existing `database:` key (lint renders with `mode: external`, so this only checks the
  schema). Run
  `scripts/dev-exec.sh 'go test ./deploy/deploytest -count=1'` and expect PASS for every test, including
  the unchanged `TestHelmTemplate` and `TestHelmKwRenderUnchanged`. Report the paths.

## Task 5: Operator key material and the management API client

Files:

- `operator/internal/keys/keys.go`, `operator/internal/keys/keys_test.go`: created.
- `operator/internal/mgmtapi/oapi-codegen.yaml`: created.
- `operator/internal/mgmtapi/client.gen.go`: generated by `make operator-generate`.
- `operator/internal/mgmtapi/client.go`, `operator/internal/mgmtapi/client_test.go`: created.
- `operator/internal/mgmtapi/fake/fake.go`, `operator/internal/mgmtapi/fake/fake_test.go`: created.

Interfaces:

```go
package keys // github.com/piwi3910/nexora/operator/internal/keys
var ErrSecretIncomplete = errors.New("secret incomplete")
// GenerateCA returns a PEM "CERTIFICATE" and "EC PRIVATE KEY" (ECDSA P-256, CN "Nexora CA", IsCA,
// KeyUsageCertSign|CRLSign|DigitalSignature, NotBefore now-1h, NotAfter now+10y), as pki.InitCA writes.
func GenerateCA(now time.Time) (certPEM, keyPEM []byte, err error)
// GenerateKEK returns base64 (std, padded) of 32 random bytes, no newline.
func GenerateKEK() ([]byte, error)
// GenerateBootstrapToken returns "nxt_" + unpadded std base32 of 32 random bytes.
func GenerateBootstrapToken() (string, error)
// EnsureSecret creates key (type Opaque, labels, no owner reference) from gen when it does not exist.
// An existing Secret is never modified; it must hold every name in required, else ErrSecretIncomplete
// wrapped with "secret <name> lacks key <k>".
func EnsureSecret(ctx context.Context, c client.Client, key types.NamespacedName, labels map[string]string, required []string, gen func() (map[string][]byte, error)) error
// CheckSecret verifies a user-provided Secret exists and holds required (same errors; not found is returned as is).
func CheckSecret(ctx context.Context, c client.Client, key types.NamespacedName, required []string) error

package mgmtapi // github.com/piwi3910/nexora/operator/internal/mgmtapi
var (ErrUnauthorized, ErrNotFound, ErrConflict, ErrUnavailable error) // sentinel errors
type APIError struct { Status int; Code, Message string } // Error(); Is maps 401/404/409/5xx to the sentinels
type Client struct { /* generated ClientWithResponses */ }
func New(baseURL, token string, hc *http.Client) (*Client, error) // baseURL without /api/v1
func (c *Client) Health(ctx context.Context) error
func (c *Client) SetupRequired(ctx context.Context) (bool, error)
func (c *Client) EngineGroups(ctx context.Context) ([]EngineGroup, error)
func (c *Client) CreateEngineGroup(ctx context.Context, in EngineGroupInput) (EngineGroup, error)
func (c *Client) UpdateEngineGroup(ctx context.Context, id uuid.UUID, in EngineGroupUpdate) (EngineGroup, error)
func (c *Client) DeleteEngineGroup(ctx context.Context, id uuid.UUID, revision int64) error
func (c *Client) JoinTokens(ctx context.Context) ([]JoinToken, error)
func (c *Client) CreateJoinToken(ctx context.Context, in JoinTokenCreate) (JoinTokenCreated, error)
func (c *Client) RevokeJoinToken(ctx context.Context, id uuid.UUID) error
// EngineGroup, EngineGroupInput, EngineGroupUpdate, JoinToken, JoinTokenCreate, JoinTokenCreated are
// the generated models of mgmt/api/openapi.yaml.

package fake // github.com/piwi3910/nexora/operator/internal/mgmtapi/fake
const DefaultGroupID = "00000000-0000-0000-0000-000000000001"
type Server struct {
	URL, Token string
	// Knobs, read under the lock on every request:
	ConflictOnce bool            // the next PUT /engine-groups/{id} answers 409 conflict
	Status       int             // non-zero: every request answers this status
	NonEmpty     map[string]bool // group name -> DELETE answers 409 engine_group_not_empty
	Requests     []string        // "METHOD /path" of every request, in order
	LastUpdate   *mgmtapi.EngineGroupUpdate
}
// New starts an httptest server holding the default group, requiring "Bearer "+Token (random nxt_ token).
func New(t *testing.T) *Server
func (s *Server) Group(name string) (mgmtapi.EngineGroup, bool)
func (s *Server) MutateGroup(name string, f func(*mgmtapi.EngineGroup)) // bumps revision
func (s *Server) Tokens() []mgmtapi.JoinToken
func (s *Server) TokenSecret(id uuid.UUID) string // the token value handed out at creation
func (s *Server) SetTokenState(id uuid.UUID, state string, expiresAt time.Time)
func (s *Server) SetHealth(ok bool, setupRequired bool)
```

- [x] Create `operator/internal/keys/keys_test.go`:
  ```go
  package keys_test

  import (
  	"context"
  	"crypto/ecdsa"
  	"crypto/elliptic"
  	"crypto/x509"
  	"encoding/base64"
  	"encoding/pem"
  	"errors"
  	"regexp"
  	"testing"
  	"time"

  	corev1 "k8s.io/api/core/v1"
  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  	"k8s.io/apimachinery/pkg/types"
  	"sigs.k8s.io/controller-runtime/pkg/client/fake"

  	"github.com/piwi3910/nexora/operator/internal/keys"
  )

  func TestGenerateCA(t *testing.T) {
  	now := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
  	certPEM, keyPEM, err := keys.GenerateCA(now)
  	if err != nil {
  		t.Fatal(err)
  	}
  	cb, _ := pem.Decode(certPEM)
  	kb, _ := pem.Decode(keyPEM)
  	if cb == nil || cb.Type != "CERTIFICATE" || kb == nil || kb.Type != "EC PRIVATE KEY" {
  		t.Fatalf("PEM types: %v %v", cb, kb)
  	}
  	cert, err := x509.ParseCertificate(cb.Bytes)
  	if err != nil {
  		t.Fatal(err)
  	}
  	key, err := x509.ParseECPrivateKey(kb.Bytes)
  	if err != nil {
  		t.Fatal(err)
  	}
  	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
  	if !ok || pub.Curve != elliptic.P256() || !pub.Equal(&key.PublicKey) {
  		t.Fatal("certificate key is not the P-256 private key")
  	}
  	if !cert.IsCA || cert.Subject.CommonName != "Nexora CA" || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
  		t.Fatalf("CA fields: IsCA=%v CN=%q usage=%v", cert.IsCA, cert.Subject.CommonName, cert.KeyUsage)
  	}
  	if got := cert.NotAfter.Sub(now); got < 10*365*24*time.Hour-2*time.Hour || got > 10*366*24*time.Hour {
  		t.Fatalf("validity %v", got)
  	}
  }

  func TestGenerateKEK(t *testing.T) {
  	kek, err := keys.GenerateKEK()
  	if err != nil {
  		t.Fatal(err)
  	}
  	raw, err := base64.StdEncoding.DecodeString(string(kek))
  	if err != nil || len(raw) != 32 {
  		t.Fatalf("kek decodes to %d bytes: %v", len(raw), err)
  	}
  }

  func TestGenerateBootstrapToken(t *testing.T) {
  	a, err := keys.GenerateBootstrapToken()
  	if err != nil {
  		t.Fatal(err)
  	}
  	b, _ := keys.GenerateBootstrapToken()
  	if !regexp.MustCompile(`^nxt_[A-Z2-7]{52}$`).MatchString(a) || a == b {
  		t.Fatalf("tokens %q %q", a, b)
  	}
  }

  func TestEnsureSecretNeverOverwrites(t *testing.T) {
  	ctx := context.Background()
  	existing := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "nx-ca"}, Data: map[string][]byte{"ca.crt": []byte("old")}}
  	c := fake.NewClientBuilder().WithObjects(existing).Build()
  	gen := func() (map[string][]byte, error) { return map[string][]byte{"ca.crt": []byte("new"), "ca.key": []byte("new")}, nil }
  	err := keys.EnsureSecret(ctx, c, types.NamespacedName{Namespace: "ns", Name: "nx-ca"}, nil, []string{"ca.crt", "ca.key"}, gen)
  	if !errors.Is(err, keys.ErrSecretIncomplete) {
  		t.Fatalf("incomplete secret: %v", err)
  	}
  	var got corev1.Secret
  	_ = c.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "nx-ca"}, &got)
  	if string(got.Data["ca.crt"]) != "old" || got.Data["ca.key"] != nil {
  		t.Fatalf("existing secret modified: %v", got.Data)
  	}
  	labels := map[string]string{"nexora.io/installation": "nx"}
  	if err := keys.EnsureSecret(ctx, c, types.NamespacedName{Namespace: "ns", Name: "nx-kek"}, labels, []string{"kek"},
  		func() (map[string][]byte, error) { return map[string][]byte{"kek": []byte("k1")}, nil }); err != nil {
  		t.Fatal(err)
  	}
  	if err := keys.EnsureSecret(ctx, c, types.NamespacedName{Namespace: "ns", Name: "nx-kek"}, labels, []string{"kek"},
  		func() (map[string][]byte, error) { return map[string][]byte{"kek": []byte("k2")}, nil }); err != nil {
  		t.Fatal(err)
  	}
  	_ = c.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "nx-kek"}, &got)
  	if string(got.Data["kek"]) != "k1" || got.Labels["nexora.io/installation"] != "nx" || len(got.OwnerReferences) != 0 {
  		t.Fatalf("created secret: data=%s labels=%v owners=%v", got.Data["kek"], got.Labels, got.OwnerReferences)
  	}
  }
  ```
  Run `scripts/dev-exec.sh 'cd operator && go test ./internal/keys -count=1'` and expect a build failure
  (package `keys` does not exist). Implement `keys.go` and expect PASS.
- [x] Create `operator/internal/mgmtapi/oapi-codegen.yaml`:
  ```yaml
  package: mgmtapi
  output: client.gen.go
  generate:
    client: true
    models: true
  output-options:
    # The hand-written Client in client.go wraps the generated one.
    client-type-name: rawClient
    include-operation-ids:
      [
        getHealth,
        getSetupStatus,
        listEngineGroups,
        createEngineGroup,
        getEngineGroup,
        updateEngineGroup,
        deleteEngineGroup,
        listJoinTokens,
        createJoinToken,
        revokeJoinToken,
      ]
  ```
  Run `make operator-generate` on the laptop and expect `client.gen.go` with `ClientWithResponses`.
  `client-type-name: rawClient` renames the generated low-level `Client` type, which otherwise collides
  with the hand-written `mgmtapi.Client`.
- [x] Create `operator/internal/mgmtapi/client_test.go`:
  ```go
  package mgmtapi_test

  import (
  	"context"
  	"errors"
  	"net/http"
  	"net/http/httptest"
  	"testing"

  	"github.com/piwi3910/nexora/operator/internal/mgmtapi"
  )

  func TestClientSendsBearerAndMapsErrors(t *testing.T) {
  	status := http.StatusOK
  	var auth, path string
  	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  		auth, path = r.Header.Get("Authorization"), r.URL.Path
  		w.Header().Set("Content-Type", "application/json")
  		w.WriteHeader(status)
  		if status == http.StatusOK {
  			_, _ = w.Write([]byte(`[]`))
  			return
  		}
  		_, _ = w.Write([]byte(`{"code":"conflict","message":"stale revision"}`))
  	}))
  	defer srv.Close()
  	c, err := mgmtapi.New(srv.URL, "nxt_TEST", srv.Client())
  	if err != nil {
  		t.Fatal(err)
  	}
  	ctx := context.Background()
  	if _, err := c.EngineGroups(ctx); err != nil || auth != "Bearer nxt_TEST" || path != "/api/v1/engine-groups" {
  		t.Fatalf("list: err=%v auth=%q path=%q", err, auth, path)
  	}
  	for code, want := range map[int]error{401: mgmtapi.ErrUnauthorized, 404: mgmtapi.ErrNotFound, 409: mgmtapi.ErrConflict, 503: mgmtapi.ErrUnavailable} {
  		status = code
  		_, err := c.EngineGroups(ctx)
  		var apiErr *mgmtapi.APIError
  		if !errors.Is(err, want) || !errors.As(err, &apiErr) || apiErr.Code != "conflict" || apiErr.Status != code {
  			t.Errorf("status %d: err=%v", code, err)
  		}
  	}
  	srv.Close()
  	if _, err := c.EngineGroups(ctx); !errors.Is(err, mgmtapi.ErrUnavailable) {
  		t.Errorf("transport failure: %v", err)
  	}
  }
  ```
  Run it and expect a build failure (`mgmtapi.New` undefined). Implement `client.go`: every method calls
  the generated `…WithResponse` method and maps non-2xx through `APIError` (decoding the JSON
  `{code,message}`). A transport error, an undecodable response and a 2xx without its JSON body are
  wrapped with `ErrUnavailable`. `SetupRequired` returns
  `SetupStatus.Required`. Expect PASS.
- [x] Create `operator/internal/mgmtapi/fake/fake_test.go`: `TestFakeServesEngineGroupsAndTokens` uses
      `mgmtapi.New(s.URL, s.Token, nil)`:
  1. lists `default`;
  2. creates `edge` (201, revision 1);
  3. updates it with revision 1 (revision 2), then a stale revision 1 gives `ErrConflict`;
  4. creates a join token for edge with `ttl_seconds: 3600` and asserts `TokenSecret(id)` equals the
     returned token and state `active`;
  5. revokes it (state `revoked`);
  6. a wrong bearer gives `ErrUnauthorized`;
  7. `NonEmpty["edge"]` makes the delete fail with `ErrConflict`;
  8. deleting `default` gives 409 `engine_group_protected`.

  `TestFakeKnobs` covers `SetHealth`, `ConflictOnce`, `MutateGroup`, `SetTokenState` and `Status`.

  Run and expect a build failure; implement `fake.go` (in-memory maps under a mutex, `httptest.NewServer`,
  closed by `t.Cleanup`) and expect PASS.

- [x] Run `scripts/dev-exec.sh 'make operator-test'` and `cd operator && go vet ./...`, and expect PASS.
      Report the paths.

## Task 6: Rendering the Nexora chart from a NexoraInstallation

Files:

- `operator/internal/render/values.go`: `BuildValues`.
- `operator/internal/render/render.go`: `LoadChart`, `Chart.Render`.
- `operator/internal/render/objects.go`: `Own`, `ManagedKinds`, `Retained`.
- `operator/internal/render/values_test.go`, `operator/internal/render/render_test.go`: created.
- `operator/internal/render/testdata/cnpg-backup-values.yaml`,
  `operator/internal/render/testdata/hostnetwork-values.yaml`: created.

Interfaces:

```go
package render // github.com/piwi3910/nexora/operator/internal/render
var (
	ErrImageTagRequired = errors.New("image tag required: set spec.image.tag or run a stamped operator")
	ErrForeignNamespace = errors.New("object outside the installation namespace")
)
type Injected struct {
	Tag                                       string            // operator version; "" or "dev" means none
	CASecret, KEKSecret, BootstrapTokenSecret string
	JoinTokenSecrets                          map[string]string // NexoraEngineGroup name -> join token Secret
}
type Values struct {
	Map           map[string]any
	PendingGroups []string // spec group names left out because their engine group has no Secret yet
}
func BuildValues(spec v1alpha1.NexoraInstallationSpec, in Injected) (Values, error)

type Chart struct{ /* *chart.Chart */ }
type Target struct {
	Name, Namespace string
	APIVersions     []string // "group/version" and "group/version/Kind" from discovery
	KubeVersion     string   // e.g. "v1.34.4"
}
func LoadChart(dir string) (*Chart, error)
// Render returns the chart's objects in apply order (see Architecture), namespaced to t.Namespace where unset.
func (c *Chart) Render(t Target, values map[string]any) ([]*unstructured.Unstructured, error)

var ManagedKinds []schema.GroupVersionKind // the ten kinds in Architecture
func Retained(gvk schema.GroupVersionKind) bool // true only for postgresql.cnpg.io/v1 Cluster
// Own sets metadata.labels["nexora.io/installation"]=owner.GetName() and, except for retained kinds, a
// controller owner reference (BlockOwnerDeletion true). Any object whose namespace differs from
// owner's returns ErrForeignNamespace wrapped with "<Kind>/<name> in <namespace>".
func Own(objs []*unstructured.Unstructured, owner metav1.Object, ownerGVK schema.GroupVersionKind) error
```

- [ ] Create `operator/internal/render/values_test.go`:
  ```go
  package render_test

  import (
  	"encoding/json"
  	"errors"
  	"os"
  	"reflect"
  	"testing"

  	"sigs.k8s.io/yaml"

  	"github.com/piwi3910/nexora/operator/api/v1alpha1"
  	"github.com/piwi3910/nexora/operator/internal/render"
  )

  func jsonNormal(t *testing.T, v any) map[string]any {
  	t.Helper()
  	b, err := json.Marshal(v)
  	if err != nil {
  		t.Fatal(err)
  	}
  	var out map[string]any
  	if err := json.Unmarshal(b, &out); err != nil {
  		t.Fatal(err)
  	}
  	return out
  }

  func dropMetricsNamespaces(m map[string]any) {
  	metrics, _ := m["metrics"].(map[string]any)
  	for _, k := range []string{"serviceMonitor", "prometheusRule"} {
  		if sub, ok := metrics[k].(map[string]any); ok {
  			delete(sub, "namespace")
  		}
  	}
  }

  func TestValuesFromKwEquivalentInstallation(t *testing.T) {
  	raw, err := os.ReadFile("testdata/kw-installation.yaml")
  	if err != nil {
  		t.Fatal(err)
  	}
  	var inst v1alpha1.NexoraInstallation
  	if err := yaml.UnmarshalStrict(raw, &inst); err != nil {
  		t.Fatal(err)
  	}
  	v, err := render.BuildValues(inst.Spec, render.Injected{Tag: "sha-0000000", CASecret: "nexora-ca", KEKSecret: "nexora-kek",
  		JoinTokenSecrets: map[string]string{"default": "nexora-join-token"}})
  	if err != nil || len(v.PendingGroups) != 0 {
  		t.Fatalf("values: %v pending %v", err, v.PendingGroups)
  	}
  	kwRaw, err := os.ReadFile("../../../deploy/kw/values-kw.yaml")
  	if err != nil {
  		t.Fatal(err)
  	}
  	var kw map[string]any
  	if err := yaml.Unmarshal(kwRaw, &kw); err != nil {
  		t.Fatal(err)
  	}
  	got, want := jsonNormal(t, v.Map), jsonNormal(t, kw)
  	// Injected by the operator, absent from values-kw.yaml.
  	delete(got["image"].(map[string]any), "tag")
  	delete(got["mgmt"].(map[string]any), "bootstrapToken")
  	dropMetricsNamespaces(got)
  	dropMetricsNamespaces(want)
  	if !reflect.DeepEqual(got, want) {
  		gb, _ := yaml.Marshal(got)
  		wb, _ := yaml.Marshal(want)
  		t.Fatalf("values differ\n--- operator\n%s\n--- values-kw.yaml\n%s", gb, wb)
  	}
  }

  func TestBuildValuesPendingGroupsAndTag(t *testing.T) {
  	spec := v1alpha1.NexoraInstallationSpec{Engine: v1alpha1.EngineSpec{Groups: []v1alpha1.EngineGroupSpec{
  		{Name: "default"}, {Name: "edge", EngineGroupRef: "edge-eg"}}}}
  	v, err := render.BuildValues(spec, render.Injected{Tag: "sha-1", JoinTokenSecrets: map[string]string{"edge-eg": "edge-join-token"}})
  	if err != nil {
  		t.Fatal(err)
  	}
  	groups := v.Map["engine"].(map[string]any)["groups"].([]any)
  	if len(groups) != 1 || groups[0].(map[string]any)["joinTokenSecret"] != "edge-join-token" || groups[0].(map[string]any)["engineGroupRef"] != nil {
  		t.Fatalf("groups = %v", groups)
  	}
  	if !reflect.DeepEqual(v.PendingGroups, []string{"default"}) {
  		t.Fatalf("pending = %v", v.PendingGroups)
  	}
  	none, _ := render.BuildValues(v1alpha1.NexoraInstallationSpec{}, render.Injected{Tag: "sha-1"})
  	if g := none.Map["engine"].(map[string]any)["groups"].([]any); len(g) != 0 {
  		t.Fatalf("no spec groups must render no chart default group: %v", g)
  	}
  	if _, err := render.BuildValues(spec, render.Injected{Tag: "dev"}); !errors.Is(err, render.ErrImageTagRequired) {
  		t.Fatalf("dev operator without tag: %v", err)
  	}
  }
  ```
  Run `scripts/dev-exec.sh 'cd operator && go test ./internal/render -count=1'` and expect a build
  failure. Implement `values.go` as described in Architecture, and expect PASS.
  `TestValuesFromKwEquivalentInstallation` fails with the diff whenever a CRD field is missing or
  misnamed; fix the Task 1 types only by reporting to the lead, since Task 1 is committed.
- [ ] Create `operator/internal/render/render_test.go`:

  ```go
  package render_test

  import (
  	"errors"
  	"os"
  	"os/exec"
  	"path/filepath"
  	"reflect"
  	"strings"
  	"testing"

  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
  	"k8s.io/apimachinery/pkg/runtime/schema"
  	"sigs.k8s.io/yaml"

  	"github.com/piwi3910/nexora/operator/api/v1alpha1"
  	"github.com/piwi3910/nexora/operator/internal/render"
  )

  const chartDir = "../../../deploy/helm/nexora"

  var target = render.Target{Name: "nexora", Namespace: "nexora", KubeVersion: "v1.34.4",
  	APIVersions: []string{"postgresql.cnpg.io/v1", "monitoring.coreos.com/v1"}}

  func load(t *testing.T) *render.Chart {
  	t.Helper()
  	c, err := render.LoadChart(chartDir)
  	if err != nil {
  		t.Fatal(err)
  	}
  	return c
  }

  func valuesFile(t *testing.T, name string) map[string]any {
  	t.Helper()
  	b, err := os.ReadFile(name)
  	if err != nil {
  		t.Fatal(err)
  	}
  	var m map[string]any
  	if err := yaml.Unmarshal(b, &m); err != nil {
  		t.Fatal(err)
  	}
  	return m
  }

  func normalize(v any) any {
  	switch x := v.(type) {
  	case map[string]any:
  		out := map[string]any{}
  		for k, val := range x {
  			switch k {
  			case "app.kubernetes.io/managed-by":
  				out[k] = "X"
  			case "nexora.io/installation", "ownerReferences":
  			default:
  				out[k] = normalize(val)
  			}
  		}
  		return out
  	case []any:
  		out := make([]any, len(x))
  		for i := range x {
  			out[i] = normalize(x[i])
  		}
  		return out
  	}
  	return v
  }

  func byKey(t *testing.T, objs []map[string]any) map[string]any {
  	out := map[string]any{}
  	for _, o := range objs {
  		md := o["metadata"].(map[string]any)
  		md["namespace"] = "nexora" // helm template omits it for most templates
  		out[o["kind"].(string)+"/"+md["name"].(string)] = normalize(o)
  	}
  	return out
  }

  func TestRenderMatchesHelmTemplate(t *testing.T) {
  	c := load(t)
  	for _, vf := range []string{"testdata/cnpg-backup-values.yaml", "testdata/hostnetwork-values.yaml", "../../../deploy/kw/values-kw.yaml"} {
  		t.Run(filepath.Base(vf), func(t *testing.T) {
  			vals := valuesFile(t, vf)
  			objs, err := c.Render(target, vals)
  			if err != nil {
  				t.Fatal(err)
  			}
  			var mine []map[string]any
  			for _, o := range objs {
  				mine = append(mine, o.Object)
  			}
  			out, err := exec.Command("helm", "template", "nexora", chartDir, "--namespace", "nexora", "-f", vf,
  				"--api-versions", "postgresql.cnpg.io/v1", "--api-versions", "monitoring.coreos.com/v1").CombinedOutput()
  			if err != nil {
  				t.Fatalf("helm template: %v\n%s", err, out)
  			}
  			var theirs []map[string]any
  			for _, doc := range strings.Split(string(out), "\n---\n") {
  				var m map[string]any
  				if err := yaml.Unmarshal([]byte(doc), &m); err == nil && m != nil {
  					theirs = append(theirs, m)
  				}
  			}
  			a, b := byKey(t, mine), byKey(t, theirs)
  			if !reflect.DeepEqual(a, b) {
  				for k := range b {
  					if !reflect.DeepEqual(a[k], b[k]) {
  						t.Errorf("%s differs from helm template", k)
  					}
  				}
  				t.Fatalf("operator render has %d objects, helm template %d", len(a), len(b))
  			}
  		})
  	}
  }

  func TestRenderAddsOwnershipExceptRetainedKinds(t *testing.T) {
  	objs, err := load(t).Render(target, valuesFile(t, "testdata/cnpg-backup-values.yaml"))
  	if err != nil {
  		t.Fatal(err)
  	}
  	owner := &v1alpha1.NexoraInstallation{ObjectMeta: metav1.ObjectMeta{Name: "nexora", Namespace: "nexora", UID: "u-1"}}
  	if err := render.Own(objs, owner, v1alpha1.GroupVersion.WithKind("NexoraInstallation")); err != nil {
  		t.Fatal(err)
  	}
  	var sawCluster bool
  	for _, o := range objs {
  		if o.GetLabels()["nexora.io/installation"] != "nexora" {
  			t.Errorf("%s/%s lacks the installation label", o.GetKind(), o.GetName())
  		}
  		refs := o.GetOwnerReferences()
  		if render.Retained(o.GroupVersionKind()) {
  			sawCluster = true
  			if len(refs) != 0 {
  				t.Errorf("retained %s has owners %v", o.GetName(), refs)
  			}
  			continue
  		}
  		if len(refs) != 1 || refs[0].UID != "u-1" || refs[0].Controller == nil || !*refs[0].Controller {
  			t.Errorf("%s/%s owners = %v", o.GetKind(), o.GetName(), refs)
  		}
  	}
  	if !sawCluster || !render.Retained(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}) {
  		t.Fatal("the CNPG Cluster must be rendered and retained")
  	}
  }

  func TestRenderRejectsForeignNamespace(t *testing.T) {
  	o := &unstructured.Unstructured{}
  	o.SetAPIVersion("monitoring.coreos.com/v1")
  	o.SetKind("ServiceMonitor")
  	o.SetName("nexora")
  	o.SetNamespace("monitoring")
  	owner := &v1alpha1.NexoraInstallation{ObjectMeta: metav1.ObjectMeta{Name: "nexora", Namespace: "nexora"}}
  	err := render.Own([]*unstructured.Unstructured{o}, owner, v1alpha1.GroupVersion.WithKind("NexoraInstallation"))
  	if !errors.Is(err, render.ErrForeignNamespace) || !strings.Contains(err.Error(), "ServiceMonitor/nexora in monitoring") {
  		t.Fatalf("err = %v", err)
  	}
  }

  func TestRenderChartFailureIsReported(t *testing.T) {
  	vals := valuesFile(t, "testdata/hostnetwork-values.yaml")
  	vals["engine"].(map[string]any)["groups"] = []any{map[string]any{"name": "default"}}
  	if _, err := load(t).Render(target, vals); err == nil || !strings.Contains(err.Error(), "joinTokenSecret is required") {
  		t.Fatalf("err = %v", err)
  	}
  }

  func TestRenderCapabilities(t *testing.T) {
  	noCNPG := target
  	noCNPG.APIVersions = nil
  	if _, err := load(t).Render(noCNPG, valuesFile(t, "testdata/cnpg-backup-values.yaml")); err == nil ||
  		!strings.Contains(err.Error(), "database.mode=cnpg needs the CloudNativePG operator") {
  		t.Fatalf("err = %v", err)
  	}
  }
  ```

  Write `testdata/cnpg-backup-values.yaml`:
  - `mgmt.ca.existingSecret: nexora-ca` and `mgmt.bootstrapToken.existingSecret: nexora-operator-token`;
  - `database.mode: cnpg` with `backup.enabled: true`, `destinationPath: s3://b/p` and
    `s3Credentials.existingSecret: s3`;
  - group `default` with `joinTokenSecret: jt` and instances `a`/`b` on `node-1`/`node-2`, each with
    a ClusterIP service.

  Write `testdata/hostnetwork-values.yaml`: external mode (`external.existingSecret: nexora-db`),
  `mgmt.ca.existingSecret: nexora-ca`, `engine.hostNetwork: true`, `engine.kind: Deployment`, group
  `default` with `replicas: 2` and `joinTokenSecret: jt`, `otelCollector.enabled: true`.

  As built: `BuildValues` removes empty maps from the marshalled spec (unset struct fields marshal to
  `{}` despite `omitempty`; an empty map merges into the chart default unchanged). `Render` parses each
  `SplitManifests` document with a trailing newline appended, as Helm writes manifests, so a trailing
  block scalar (ConfigMap data) keeps its final newline.

  Task 1 type change made here (lead decision): `TestValuesFromKwEquivalentInstallation` failed on
  `engine.groups[0].nodeNamePrefix: ""`, which `values-kw.yaml` sets and `NodeNamePrefix string` with
  `omitempty` dropped. `EngineGroupSpec.NodeNamePrefix` is now `*string`; `make operator-generate`
  changed only `zz_generated.deepcopy.go` (the CRD schema is identical).

  Run the tests in the dev pod (which has `helm`) and expect a build failure. Implement
  `render.go` and `objects.go`, and expect PASS. `TestRenderMatchesHelmTemplate` fails when a
  template is skipped, `Release.Service` leaks into anything other than the normalized label, or a
  document is lost.

- [ ] Run `scripts/dev-exec.sh 'make operator-test'` and `cd operator && go vet ./...`, and expect PASS.
      Report the paths.

## Task 7: NexoraEngineGroup controller

Files:

- `operator/internal/controller/enginegroup/controller.go`: `Reconciler`, `SetupWithManager`,
  `DefaultClientFor`.
- `operator/internal/controller/enginegroup/token.go`: join token creation, rotation and revocation.
- `operator/internal/controller/enginegroup/controller_test.go`: created.
- `operator/cmd/nexora-operator/main.go`: registers the controller in `setupControllers`.

Interfaces:

```go
package enginegroup // github.com/piwi3910/nexora/operator/internal/controller/enginegroup
type API interface {
	EngineGroups(ctx context.Context) ([]mgmtapi.EngineGroup, error)
	CreateEngineGroup(ctx context.Context, in mgmtapi.EngineGroupInput) (mgmtapi.EngineGroup, error)
	UpdateEngineGroup(ctx context.Context, id uuid.UUID, in mgmtapi.EngineGroupUpdate) (mgmtapi.EngineGroup, error)
	DeleteEngineGroup(ctx context.Context, id uuid.UUID, revision int64) error
	JoinTokens(ctx context.Context) ([]mgmtapi.JoinToken, error)
	CreateJoinToken(ctx context.Context, in mgmtapi.JoinTokenCreate) (mgmtapi.JoinTokenCreated, error)
	RevokeJoinToken(ctx context.Context, id uuid.UUID) error
}
type Reconciler struct {
	Client    client.Client
	Scheme    *runtime.Scheme
	ClientFor func(ctx context.Context, inst *v1alpha1.NexoraInstallation) (API, error)
	Now       func() time.Time
}
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error)
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error // For NexoraEngineGroup, Owns Secret, Watches NexoraInstallation
// DefaultClientFor builds a mgmtapi.Client from inst.Status.ManagementURL and the "token" key of the
// Secret inst.Status.Secrets.OperatorToken.
func DefaultClientFor(c client.Client) func(ctx context.Context, inst *v1alpha1.NexoraInstallation) (API, error)
```

Reconcile order:

1. Get the CR. When it is being deleted, run deletion (below).
2. Otherwise ensure the finalizer, then get the installation.
   - Missing, or `ManagementReady` not `True`: `Synced=False` with reason `ManagementUnavailable`,
     requeue after 10 s.
3. Duplicates: list `NexoraEngineGroup` in the namespace. Another CR with the same effective group name
   and installation and an older `creationTimestamp` (name as tiebreak) → `Synced=False` with reason
   `DuplicateGroupName`, no API call.
4. Find the group by name. Create it when missing; otherwise PUT the overlay when it differs.
   - `ErrConflict` → `Synced=False` with reason `Conflict`, requeue after 1 s.
   - `ErrUnauthorized` → `Unauthorized`, requeue after 30 s.
   - `ErrUnavailable` → `ManagementUnavailable`, requeue after 10 s.
5. Join token:
   - The Secret `spec.joinToken.secretName` (default `<name>-join-token`) exists without this CR as
     controller owner → `JoinTokenReady=False` with reason `Conflict`.
   - The status token is not listed as `active`, expires within `renewBefore`, or the Secret is
     missing → create a token, write the Secret (owner reference to the CR, key `join-token`), move the
     old id to `previousJoinTokenID`, and set `previousJoinTokenRevokeAt = now + revokeGracePeriod`.
   - `previousJoinTokenRevokeAt <= now` → revoke the previous token (404 counts as done) and clear both
     fields.
6. Status: `groupID`, `revision`, `engineCount`, `joinTokenSecret`, `joinTokenID`, `joinTokenExpiresAt`,
   `Synced=True`, `JoinTokenReady=True`, `Ready=True` (reason `Reconciled`).
   `RequeueAfter = min(expiresAt - renewBefore - now, previousJoinTokenRevokeAt - now, 1h)`, at least
   1 s.

Deletion:

- Installation missing → remove the finalizer.
- Otherwise revoke `joinTokenID` and `previousJoinTokenID`.
- `deletionPolicy: Delete` and a group name other than `default` → delete with the current revision.
  - `ErrConflict` → `Synced=False` with reason `DeletionBlocked`, keep the finalizer, requeue after
    30 s.
- Then remove the finalizer.

As built (details the order above leaves open):

- Status is written with a merge patch (`client.MergeFrom`), so a concurrent spec edit does not fail the
  write. A failed `Synced` sets `Ready=False` with the same reason and leaves `JoinTokenReady` unchanged:
  the Secret stays valid while the management plane is briefly unreachable.
- `DuplicateGroupName` requeues after 30 s. A 400/422 refusal by the API, or a `rollout.maxServfailRatio`
  that is not a decimal, sets `Synced=False` with reason `Conflict` and requeues after 30 s (the
  reason list has no "invalid" reason). Kubernetes API errors are returned for controller-runtime's
  backoff.
- A token counts as current only when it is listed `active` for this group's id. `joinTokenExpiresAt`
  is `now + ttl` on the controller clock at creation, so renewal uses one clock.
- A rotation while a previous token is still in its grace period revokes that previous token at once
  (one previous token is tracked); a token whose Secret write fails is revoked immediately.
- Deletion deletes only the group whose id is `status.groupID`, looked up for its current revision, so
  a `DuplicateGroupName` CR (no `groupID`) never deletes the group another CR manages.
- The watch on `NexoraInstallation` enqueues every `NexoraEngineGroup` in its namespace referencing it.

- [x] Create `operator/internal/controller/enginegroup/controller_test.go`:

  ```go
  package enginegroup_test

  import (
  	"context"
  	"testing"
  	"time"

  	"github.com/google/uuid"
  	corev1 "k8s.io/api/core/v1"
  	apimeta "k8s.io/apimachinery/pkg/api/meta"
  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  	"k8s.io/apimachinery/pkg/types"
  	ctrl "sigs.k8s.io/controller-runtime"
  	"sigs.k8s.io/controller-runtime/pkg/client"

  	"github.com/piwi3910/nexora/operator/api/v1alpha1"
  	"github.com/piwi3910/nexora/operator/internal/controller/enginegroup"
  	"github.com/piwi3910/nexora/operator/internal/envtestutil"
  	"github.com/piwi3910/nexora/operator/internal/mgmtapi"
  	"github.com/piwi3910/nexora/operator/internal/mgmtapi/fake"
  )

  type env struct {
  	t   *testing.T
  	c   client.Client
  	r   *enginegroup.Reconciler
  	f   *fake.Server
  	now time.Time
  	ns  string
  }

  func setup(t *testing.T) *env {
  	_, c := envtestutil.Start(t)
  	f := fake.New(t)
  	e := &env{t: t, c: c, f: f, now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC), ns: "eg-" + uuid.NewString()[:8]}
  	ctx := context.Background()
  	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: e.ns}}); err != nil {
  		t.Fatal(err)
  	}
  	e.r = &enginegroup.Reconciler{Client: c, Scheme: c.Scheme(), Now: func() time.Time { return e.now },
  		ClientFor: func(ctx context.Context, inst *v1alpha1.NexoraInstallation) (enginegroup.API, error) {
  			return mgmtapi.New(f.URL, f.Token, nil)
  		}}
  	inst := &v1alpha1.NexoraInstallation{ObjectMeta: metav1.ObjectMeta{Namespace: e.ns, Name: "nexora"}}
  	if err := c.Create(ctx, inst); err != nil {
  		t.Fatal(err)
  	}
  	e.setManagementReady(true)
  	return e
  }

  func (e *env) setManagementReady(ok bool) {
  	ctx := context.Background()
  	var inst v1alpha1.NexoraInstallation
  	if err := e.c.Get(ctx, types.NamespacedName{Namespace: e.ns, Name: "nexora"}, &inst); err != nil {
  		e.t.Fatal(err)
  	}
  	st := metav1.ConditionFalse
  	if ok {
  		st = metav1.ConditionTrue
  	}
  	apimeta.SetStatusCondition(&inst.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionManagementReady, Status: st, Reason: v1alpha1.ReasonReconciled})
  	inst.Status.ManagementURL = e.f.URL
  	if err := e.c.Status().Update(ctx, &inst); err != nil {
  		e.t.Fatal(err)
  	}
  }

  func (e *env) create(g *v1alpha1.NexoraEngineGroup) *v1alpha1.NexoraEngineGroup {
  	g.Namespace = e.ns
  	g.Spec.InstallationRef.Name = "nexora"
  	if err := e.c.Create(context.Background(), g); err != nil {
  		e.t.Fatal(err)
  	}
  	return g
  }

  func (e *env) reconcile(name string) (ctrl.Result, *v1alpha1.NexoraEngineGroup) {
  	e.t.Helper()
  	ctx := context.Background()
  	res, err := e.r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: e.ns, Name: name}})
  	if err != nil {
  		e.t.Fatalf("reconcile %s: %v", name, err)
  	}
  	var g v1alpha1.NexoraEngineGroup
  	if err := e.c.Get(ctx, types.NamespacedName{Namespace: e.ns, Name: name}, &g); err != nil {
  		return res, nil
  	}
  	return res, &g
  }

  func cond(g *v1alpha1.NexoraEngineGroup, typ string) *metav1.Condition {
  	return apimeta.FindStatusCondition(g.Status.Conditions, typ)
  }

  func str(s string) *string { return &s }
  func i32(v int32) *int32  { return &v }
  func dur(s string) *metav1.Duration {
  	d, _ := time.ParseDuration(s)
  	return &metav1.Duration{Duration: d}
  }

  func TestEngineGroupCreatesGroupAndJoinToken(t *testing.T) {
  	e := setup(t)
  	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge"}, Spec: v1alpha1.NexoraEngineGroupSpec{
  		Description: str("edge engines"), ExtraACLCIDRs: []string{"198.51.100.0/24"},
  		JoinToken: v1alpha1.JoinTokenSpec{TTL: dur("24h"), RenewBefore: dur("1h"), MaxUses: i32(5), Labels: map[string]string{"site": "a"}}}})
  	_, g := e.reconcile("edge")
  	fg, ok := e.f.Group("edge")
  	if !ok || fg.Description != "edge engines" || len(fg.ExtraAclCidrs) != 1 || g.Status.GroupID != fg.Id.String() {
  		t.Fatalf("group %+v status %+v", fg, g.Status)
  	}
  	if c := cond(g, v1alpha1.ConditionReady); c == nil || c.Status != metav1.ConditionTrue {
  		t.Fatalf("ready = %v", c)
  	}
  	var sec corev1.Secret
  	if err := e.c.Get(context.Background(), types.NamespacedName{Namespace: e.ns, Name: "edge-join-token"}, &sec); err != nil {
  		t.Fatal(err)
  	}
  	id := uuid.MustParse(g.Status.JoinTokenID)
  	if string(sec.Data["join-token"]) != e.f.TokenSecret(id) || len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].Name != "edge" {
  		t.Fatalf("secret %v owners %v", sec.Data, sec.OwnerReferences)
  	}
  	for _, tok := range e.f.Tokens() {
  		if tok.Id == id {
  			if tok.EngineGroupId != fg.Id || tok.MaxUses == nil || *tok.MaxUses != 5 || tok.Labels["site"] != "a" || len(tok.Name) > 64 {
  				t.Fatalf("token %+v", tok)
  			}
  		}
  	}
  	if g.Status.JoinTokenSecret != "edge-join-token" {
  		t.Fatalf("status.joinTokenSecret = %q", g.Status.JoinTokenSecret)
  	}
  }

  func TestEngineGroupUpdatesOnlySetFields(t *testing.T) {
  	e := setup(t)
  	eg := e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge"}, Spec: v1alpha1.NexoraEngineGroupSpec{Description: str("one")}})
  	e.reconcile("edge")
  	e.f.MutateGroup("edge", func(g *mgmtapi.EngineGroup) { g.CanaryCount = 3 })
  	eg.Spec.Description = str("two")
  	if err := e.c.Get(context.Background(), client.ObjectKeyFromObject(eg), eg); err != nil {
  		t.Fatal(err)
  	}
  	eg.Spec.Description = str("two")
  	if err := e.c.Update(context.Background(), eg); err != nil {
  		t.Fatal(err)
  	}
  	e.reconcile("edge")
  	fg, _ := e.f.Group("edge")
  	if fg.Description != "two" || fg.CanaryCount != 3 || e.f.LastUpdate == nil || e.f.LastUpdate.Revision != 2 {
  		t.Fatalf("group %+v last update %+v", fg, e.f.LastUpdate)
  	}
  	puts := 0
  	for _, r := range e.f.Requests {
  		if len(r) > 3 && r[:3] == "PUT" {
  			puts++
  		}
  	}
  	e.reconcile("edge")
  	after := 0
  	for _, r := range e.f.Requests {
  		if len(r) > 3 && r[:3] == "PUT" {
  			after++
  		}
  	}
  	if after != puts {
  		t.Fatal("an unchanged CR sent another PUT")
  	}
  }

  func TestEngineGroupRetriesOnConflict(t *testing.T) {
  	e := setup(t)
  	eg := e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge"}, Spec: v1alpha1.NexoraEngineGroupSpec{Description: str("one")}})
  	e.reconcile("edge")
  	_ = e.c.Get(context.Background(), client.ObjectKeyFromObject(eg), eg)
  	eg.Spec.Description = str("two")
  	_ = e.c.Update(context.Background(), eg)
  	e.f.ConflictOnce = true
  	res, g := e.reconcile("edge")
  	if c := cond(g, v1alpha1.ConditionSynced); c == nil || c.Reason != v1alpha1.ReasonConflict || res.RequeueAfter == 0 {
  		t.Fatalf("after conflict: %v %v", c, res)
  	}
  	_, g = e.reconcile("edge")
  	if fg, _ := e.f.Group("edge"); fg.Description != "two" || cond(g, v1alpha1.ConditionSynced).Status != metav1.ConditionTrue {
  		t.Fatalf("retry did not apply: %+v", fg)
  	}
  }

  func TestEngineGroupDuplicateNameRefused(t *testing.T) {
  	e := setup(t)
  	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge"}})
  	time.Sleep(1100 * time.Millisecond) // creationTimestamp has second resolution
  	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge-copy"}, Spec: v1alpha1.NexoraEngineGroupSpec{GroupName: "edge"}})
  	e.reconcile("edge")
  	before := len(e.f.Requests)
  	_, g := e.reconcile("edge-copy")
  	if c := cond(g, v1alpha1.ConditionSynced); c == nil || c.Reason != v1alpha1.ReasonDuplicateGroupName || len(e.f.Requests) != before {
  		t.Fatalf("duplicate: %v, %d new requests", c, len(e.f.Requests)-before)
  	}
  }

  func TestEngineGroupRotatesJoinToken(t *testing.T) {
  	e := setup(t)
  	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge"}, Spec: v1alpha1.NexoraEngineGroupSpec{
  		JoinToken: v1alpha1.JoinTokenSpec{TTL: dur("3h"), RenewBefore: dur("1h"), RevokeGracePeriod: dur("10m")}}})
  	_, g := e.reconcile("edge")
  	first := g.Status.JoinTokenID
  	e.now = e.now.Add(90 * time.Minute)
  	_, g = e.reconcile("edge")
  	if g.Status.JoinTokenID != first {
  		t.Fatal("rotated before renewBefore")
  	}
  	e.now = e.now.Add(31 * time.Minute) // 2h01m: expiry within 1h
  	_, g = e.reconcile("edge")
  	if g.Status.JoinTokenID == first || g.Status.PreviousJoinTokenID != first {
  		t.Fatalf("no rotation: %+v", g.Status)
  	}
  	var sec corev1.Secret
  	_ = e.c.Get(context.Background(), types.NamespacedName{Namespace: e.ns, Name: "edge-join-token"}, &sec)
  	if string(sec.Data["join-token"]) != e.f.TokenSecret(uuid.MustParse(g.Status.JoinTokenID)) {
  		t.Fatal("secret not updated to the new token")
  	}
  	state := func(id string) string {
  		for _, tok := range e.f.Tokens() {
  			if tok.Id.String() == id {
  				return string(tok.State)
  			}
  		}
  		return ""
  	}
  	e.now = e.now.Add(5 * time.Minute)
  	e.reconcile("edge")
  	if state(first) != "active" {
  		t.Fatal("previous token revoked inside the grace period")
  	}
  	e.now = e.now.Add(6 * time.Minute)
  	_, g = e.reconcile("edge")
  	if state(first) != "revoked" || g.Status.PreviousJoinTokenID != "" {
  		t.Fatalf("previous token after grace: %s %+v", state(first), g.Status)
  	}
  }

  func TestEngineGroupRecreatesRevokedToken(t *testing.T) {
  	e := setup(t)
  	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge"}})
  	_, g := e.reconcile("edge")
  	first := uuid.MustParse(g.Status.JoinTokenID)
  	e.f.SetTokenState(first, "revoked", e.now.Add(time.Hour))
  	_, g = e.reconcile("edge")
  	if g.Status.JoinTokenID == first.String() {
  		t.Fatal("a revoked token was kept")
  	}
  }

  func TestEngineGroupDeletion(t *testing.T) {
  	e := setup(t)
  	ctx := context.Background()
  	del := func(name string) {
  		var g v1alpha1.NexoraEngineGroup
  		_ = e.c.Get(ctx, types.NamespacedName{Namespace: e.ns, Name: name}, &g)
  		if err := e.c.Delete(ctx, &g); err != nil {
  			t.Fatal(err)
  		}
  	}
  	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "keep"}})
  	_, g := e.reconcile("keep")
  	tok := g.Status.JoinTokenID
  	del("keep")
  	if _, g := e.reconcile("keep"); g != nil {
  		t.Fatalf("Retain: finalizer kept: %v", g.Finalizers)
  	}
  	if _, ok := e.f.Group("keep"); !ok {
  		t.Fatal("Retain deleted the group")
  	}
  	for _, jt := range e.f.Tokens() {
  		if jt.Id.String() == tok && jt.State != "revoked" {
  			t.Fatal("Retain left the join token active")
  		}
  	}

  	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "busy"}, Spec: v1alpha1.NexoraEngineGroupSpec{DeletionPolicy: "Delete"}})
  	e.reconcile("busy")
  	e.f.NonEmpty = map[string]bool{"busy": true}
  	del("busy")
  	_, g = e.reconcile("busy")
  	if g == nil || cond(g, v1alpha1.ConditionSynced).Reason != v1alpha1.ReasonDeletionBlocked {
  		t.Fatalf("non-empty delete: %v", g)
  	}
  	e.f.NonEmpty = nil
  	if _, g := e.reconcile("busy"); g != nil {
  		t.Fatal("finalizer kept after the group emptied")
  	}
  	if _, ok := e.f.Group("busy"); ok {
  		t.Fatal("Delete kept the group")
  	}

  	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "default"}, Spec: v1alpha1.NexoraEngineGroupSpec{DeletionPolicy: "Delete"}})
  	e.reconcile("default")
  	del("default")
  	e.reconcile("default")
  	for _, r := range e.f.Requests {
  		if r == "DELETE /api/v1/engine-groups/"+fake.DefaultGroupID {
  			t.Fatal("the default group was deleted")
  		}
  	}

  	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "orphan"}})
  	e.reconcile("orphan")
  	if err := e.c.Delete(ctx, &v1alpha1.NexoraInstallation{ObjectMeta: metav1.ObjectMeta{Namespace: e.ns, Name: "nexora"}}); err != nil {
  		t.Fatal(err)
  	}
  	before := len(e.f.Requests)
  	del("orphan")
  	if _, g := e.reconcile("orphan"); g != nil || len(e.f.Requests) != before {
  		t.Fatalf("orphan deletion: %v, %d calls", g, len(e.f.Requests)-before)
  	}
  }

  func TestEngineGroupWaitsForManagement(t *testing.T) {
  	e := setup(t)
  	e.setManagementReady(false)
  	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge"}})
  	res, g := e.reconcile("edge")
  	var sec corev1.Secret
  	err := e.c.Get(context.Background(), types.NamespacedName{Namespace: e.ns, Name: "edge-join-token"}, &sec)
  	if cond(g, v1alpha1.ConditionSynced).Reason != v1alpha1.ReasonManagementUnavailable || res.RequeueAfter == 0 || err == nil || len(e.f.Requests) != 0 {
  		t.Fatalf("waiting: %v %v secret err=%v calls=%d", cond(g, v1alpha1.ConditionSynced), res, err, len(e.f.Requests))
  	}
  }

  func TestEngineGroupUnauthorized(t *testing.T) {
  	e := setup(t)
  	e.f.Status = 401
  	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge"}})
  	_, g := e.reconcile("edge")
  	var sec corev1.Secret
  	err := e.c.Get(context.Background(), types.NamespacedName{Namespace: e.ns, Name: "edge-join-token"}, &sec)
  	if cond(g, v1alpha1.ConditionSynced).Reason != v1alpha1.ReasonUnauthorized || err == nil {
  		t.Fatalf("unauthorized: %v secret err=%v", cond(g, v1alpha1.ConditionSynced), err)
  	}
  }
  ```

  Field names of the generated models (`Id`, `ExtraAclCidrs`, `CanaryCount`, `EngineGroupId`, `MaxUses`,
  `Labels`, `State`) follow oapi-codegen's output in `client.gen.go`. Adapt the test to the generated
  spelling, never the other way round. The fake's request log records full paths (`/api/v1/...`).

  Run `scripts/dev-exec.sh 'make operator-test'` and expect a build failure (package `enginegroup` does
  not exist). Implement `controller.go` and `token.go` per the reconcile order above, and expect PASS.

- [x] Register the controller in `setupControllers` in `main.go`:
  ```go
  if err := (&enginegroup.Reconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(),
  	ClientFor: enginegroup.DefaultClientFor(mgr.GetClient()), Now: time.Now}).SetupWithManager(mgr); err != nil {
  	return fmt.Errorf("engine group controller: %w", err)
  }
  ```
  Run `cd operator && go vet ./... && go build ./cmd/nexora-operator` and expect success. Report the paths.

## Task 8: Operator packaging: image, workflow, operator chart and plain manifests

Files:

- `deploy/docker/operator.Dockerfile`: created.
- `.github/workflows/images.yml`: matrix entries `nexora-operator` (build and merge).
- `deploy/helm/nexora-operator/Chart.yaml`, `values.yaml`, `values.schema.json`, `.helmignore`: created.
- `deploy/helm/nexora-operator/templates/_helpers.tpl`, `deployment.yaml`, `serviceaccount.yaml`,
  `rbac.yaml`, `namespace.yaml`, `NOTES.txt`: created. (`crds/` belongs to Task 1.)
- `deploy/operator/operator.yaml`: generated by `make operator-generate`.
- `deploy/deploytest/operator_chart_test.go`: created.
- `deploy/deploytest/workflow_test.go`, `deploy/deploytest/buildinfo_test.go`: operator expectations.

Interfaces:

- Image `<registry>/nexora-operator:<tag>`, entrypoint `/nexora-operator`, chart at `/charts/nexora`,
  user 65532. The build arguments `VERSION`, `COMMIT` and `BUILD_DATE` become
  `-X github.com/piwi3910/nexora/operator/internal/version.{Version,Commit,BuildDate}`.
- Chart values:
  ```yaml
  image: {
      registry: 192.168.10.131/azrtydxb,
      tag: "",
      pullPolicy: IfNotPresent,
    } # tag default: appVersion
  imagePullSecrets: []
  replicas: 1
  rbac:
    scope: cluster # cluster | namespace
  watchNamespaces: [] # required (non-empty) with rbac.scope=namespace
  leaderElection: true
  createNamespace: false # renders the release Namespace (used for deploy/operator/operator.yaml)
  resources:
    requests: { cpu: 50m, memory: 128Mi }
    limits: { cpu: "1", memory: 512Mi }
  ```
- Objects:
  - ServiceAccount `nexora-operator`.
  - Deployment `nexora-operator`:
    - args `--chart-dir=/charts/nexora`, `--leader-elect=<leaderElection>`, and
      `--watch-namespaces=<a,b>` only in namespace scope;
    - env `POD_NAMESPACE` from the downward API;
    - probes `/healthz` and `/readyz` on 8081, a metrics port on 8080;
    - `runAsNonRoot`, `readOnlyRootFilesystem`, capabilities dropped, seccomp `RuntimeDefault`.
  - RBAC rules (the rule set, identical for ClusterRole and Role):
    - `apps`: deployments, daemonsets;
    - `""`: services, configmaps;
    - `policy`: poddisruptionbudgets;
    - `networking.k8s.io`: ingresses;
    - `postgresql.cnpg.io`: clusters, scheduledbackups;
    - `monitoring.coreos.com`: servicemonitors, prometheusrules.

    Each gets `get, list, watch, create, update, patch, delete`. On top of that:
    - `""` secrets: `get, list, watch, create, update, patch, delete`;
    - `""` events and `events.k8s.io` events: `create, patch`;
    - `nexora.io` nexorainstallations and nexoraenginegroups: `get, list, watch, update, patch`; their
      `/status` subresources `get, update, patch`; their `/finalizers` subresources `update`;

    CNPG `Backup` objects are not in the rule set: the operator never creates them.

  - Cluster scope: ClusterRole plus ClusterRoleBinding `nexora-operator`.
  - Namespace scope: Role plus RoleBinding `nexora-operator` in each watched namespace.
  - Leader election: Role `nexora-operator-leader-election` (`coordination.k8s.io` leases:
    `get, list, watch, create, update, patch, delete`) in the release namespace, always.
    bound by RoleBinding `nexora-operator-leader-election` to the ServiceAccount.

- [ ] Create `deploy/deploytest/operator_chart_test.go`:
  ```go
  package deploytest

  import (
  	"os"
  	"path/filepath"
  	"strings"
  	"testing"
  )

  const operatorChartDir = "../helm/nexora-operator"

  func renderOperator(t *testing.T, args ...string) []obj {
  	t.Helper()
  	return renderChartDocs(t, append([]string{"template", "nexora-operator", operatorChartDir, "--namespace", "nexora-operator"}, args...)...)
  }

  func renderChartDocs(t *testing.T, args ...string) []obj {
  	t.Helper()
  	out, err := helm(args...)
  	if err != nil {
  		t.Fatalf("helm %v: %v\n%s", args, err, out)
  	}
  	return decodeDocs(t, out)
  }

  func findNS(t *testing.T, docs []obj, kind, ns, name string) obj {
  	t.Helper()
  	for _, d := range docs {
  		if d["kind"] == kind && d.path("metadata", "name") == name && d.path("metadata", "namespace") == ns {
  			return d
  		}
  	}
  	t.Fatalf("%s %s/%s not rendered", kind, ns, name)
  	return nil
  }

  func args(t *testing.T, d obj) string {
  	c := container(t, d, "operator")
  	var s []string
  	for _, a := range c["args"].([]any) {
  		s = append(s, a.(string))
  	}
  	return strings.Join(s, " ")
  }

  func TestOperatorChart(t *testing.T) {
  	if out, err := helm("lint", operatorChartDir, "--strict"); err != nil {
  		t.Fatalf("helm lint: %v\n%s", err, out)
  	}
  	cluster := renderOperator(t, "--set", "image.tag=sha-1234567")
  	find(t, cluster, "ClusterRole", "nexora-operator")
  	find(t, cluster, "ClusterRoleBinding", "nexora-operator")
  	d := find(t, cluster, "Deployment", "nexora-operator")
  	c := container(t, d, "operator")
  	if c["image"] != "192.168.10.131/azrtydxb/nexora-operator:sha-1234567" {
  		t.Errorf("image = %v", c["image"])
  	}
  	if a := args(t, d); !strings.Contains(a, "--chart-dir=/charts/nexora") || strings.Contains(a, "--watch-namespaces") {
  		t.Errorf("cluster args = %s", a)
  	}
  	sc := obj(c["securityContext"].(map[string]any))
  	if sc["readOnlyRootFilesystem"] != true || sc["allowPrivilegeEscalation"] != false || sc.path("capabilities", "drop").([]any)[0] != "ALL" {
  		t.Errorf("container security = %v", sc)
  	}
  	if d.path("spec", "template", "spec", "securityContext", "runAsNonRoot") != true {
  		t.Error("pod must run as non-root")
  	}
  	findNS(t, cluster, "Role", "nexora-operator", "nexora-operator-leader-election")
  	if has(cluster, "Namespace") {
  		t.Error("the Namespace renders without createNamespace")
  	}

  	scoped := renderOperator(t, "--set", "rbac.scope=namespace", "--set-json", `watchNamespaces=["a","b"]`)
  	if has(scoped, "ClusterRole") || has(scoped, "ClusterRoleBinding") {
  		t.Error("namespace scope renders cluster RBAC")
  	}
  	for _, ns := range []string{"a", "b"} {
  		findNS(t, scoped, "Role", ns, "nexora-operator")
  		rb := findNS(t, scoped, "RoleBinding", ns, "nexora-operator")
  		if s := rb.path("subjects").([]any)[0].(map[string]any); s["namespace"] != "nexora-operator" || s["name"] != "nexora-operator" {
  			t.Errorf("role binding subject in %s = %v", ns, s)
  		}
  	}
  	if a := args(t, find(t, scoped, "Deployment", "nexora-operator")); !strings.Contains(a, "--watch-namespaces=a,b") {
  		t.Errorf("namespace args = %s", a)
  	}
  	if out, err := helm("template", "x", operatorChartDir, "--set", "rbac.scope=namespace"); err == nil || !strings.Contains(out, "watchNamespaces") {
  		t.Errorf("namespace scope without namespaces must fail: %v %s", err, out)
  	}
  }

  func TestOperatorManifestsMatchChart(t *testing.T) {
  	want, err := helm("template", "nexora-operator", operatorChartDir, "--namespace", "nexora-operator", "--set", "rbac.scope=cluster", "--set", "createNamespace=true")
  	if err != nil {
  		t.Fatalf("helm template: %v\n%s", err, want)
  	}
  	got, err := os.ReadFile("../operator/operator.yaml")
  	if err != nil {
  		t.Fatal(err)
  	}
  	if string(got) != want {
  		t.Error("deploy/operator/operator.yaml is stale; run make operator-generate")
  	}
  	crds, _ := filepath.Glob("../operator/crds/*.yaml")
  	if len(crds) != 2 {
  		t.Fatalf("crds = %v", crds)
  	}
  	for _, p := range crds {
  		a, _ := os.ReadFile(p)
  		b, err := os.ReadFile(filepath.Join(operatorChartDir, "crds", filepath.Base(p)))
  		if err != nil || string(a) != string(b) {
  			t.Errorf("chart CRD %s differs from deploy/operator/crds: %v", filepath.Base(p), err)
  		}
  	}
  }
  ```
  If `deploy/deploytest` has no `decodeDocs` helper, extract the YAML document loop of `render` in
  `helm_test.go` into `decodeDocs(t *testing.T, out string) []obj` inside this new file, and leave
  `helm_test.go` untouched (the loop code is duplicated, not moved). Run
  `scripts/dev-exec.sh 'go test ./deploy/deploytest -run "TestOperatorChart|TestOperatorManifestsMatchChart" -count=1'`
  and expect FAIL `helm lint: ... no such file` (chart missing).
- [ ] Write the operator chart per Interfaces. `watchNamespaces` must be non-empty with namespace scope:
      `{{- if and (eq .Values.rbac.scope "namespace") (not .Values.watchNamespaces) }}{{ fail "watchNamespaces is required when rbac.scope=namespace" }}{{ end }}`.
      `Chart.yaml` has `name: nexora-operator`, `version: 0.1.0`, `appVersion: "main"`, `kubeVersion: ">=1.28.0-0"`.
      Generate `deploy/operator/operator.yaml` with the helm of the dev pod (the `helm template` line of
      `make operator-generate`, run through `scripts/dev-exec.sh` with stdout redirected into the laptop file):
      the laptop's helm 4.1 separates documents differently from the pod's and CI's helm 4.3, and CI
      regenerates the file in the toolbox image. Run the tests and expect PASS.
- [ ] Create `deploy/docker/operator.Dockerfile`:
  ```dockerfile
  # syntax=docker/dockerfile:1.10
  FROM golang:1.27-trixie AS build
  ARG VERSION=dev
  ARG COMMIT=
  ARG BUILD_DATE=
  WORKDIR /src/operator
  COPY operator/go.mod operator/go.sum ./
  RUN --mount=type=cache,target=/go/pkg/mod go mod download
  COPY operator ./
  RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
      CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/piwi3910/nexora/operator/internal/version.Version=${VERSION} -X github.com/piwi3910/nexora/operator/internal/version.Commit=${COMMIT} -X github.com/piwi3910/nexora/operator/internal/version.BuildDate=${BUILD_DATE}" -o /nexora-operator ./cmd/nexora-operator \
   && /nexora-operator version

  FROM debian:trixie-slim
  RUN groupadd --gid 65532 nonroot \
   && useradd --uid 65532 --gid 65532 --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin nonroot
  # The Nexora chart the operator renders; it ships with the operator of the same commit.
  COPY deploy/helm/nexora /charts/nexora
  COPY --from=build /nexora-operator /nexora-operator
  USER 65532:65532
  ENTRYPOINT ["/nexora-operator"]
  ```
  Add `- { name: nexora-operator, file: deploy/docker/operator.Dockerfile }` to the `images.yml` build
  matrix, and `nexora-operator` to the `merge` job's image matrix (else no multi-arch tag is created).
- [ ] In `workflow_test.go` add `"nexora-operator"` to the wanted images. In `buildinfo_test.go` add
      `"deploy/docker/operator.Dockerfile": {"ARG VERSION=dev", "ARG COMMIT=", "ARG BUILD_DATE=", "internal/version.Commit=${COMMIT}", "internal/version.BuildDate=${BUILD_DATE}", "COPY deploy/helm/nexora /charts/nexora"}`.
      Run those tests before editing the Dockerfile and workflow to see them FAIL
      (`build matrix lacks image nexora-operator`), then after, and expect PASS.
- [ ] Build the image once to prove the Dockerfile:
      `scripts/build-image.sh -f deploy/docker/operator.Dockerfile -n nexora-operator -t dev-m9-t8`. Expect
      `pull as 192.168.10.131/azrtydxb/nexora-operator:dev-m9-t8`. Run
      `scripts/dev-exec.sh 'go test ./deploy/deploytest -count=1'` and expect PASS. Report the paths and
      the image tag.

## Task 9: NexoraInstallation controller

Files:

- `operator/internal/controller/installation/controller.go`: `Reconciler`, `SetupWithManager`.
- `operator/internal/controller/installation/apply.go`: apply and prune.
- `operator/internal/controller/installation/status.go`: workloads and conditions.
- `operator/internal/controller/installation/health.go`: `Health`, `HTTPHealth`.
- `operator/internal/controller/installation/controller_test.go`, `rbac_test.go`: created.
- `operator/internal/controller/installation/testdata/cnpg-crd.yaml`: a minimal CRD
  `clusters.postgresql.cnpg.io` (v1, namespaced, `x-kubernetes-preserve-unknown-fields: true` on spec
  and status).
- `operator/cmd/nexora-operator/main.go`: registers the controller and the healthz and readyz checks.

Interfaces:

```go
package installation // github.com/piwi3910/nexora/operator/internal/controller/installation
type HealthResult struct{ Healthy, SetupRequired bool }
type Health interface {
	Check(ctx context.Context, managementURL, token string) (HealthResult, error) // error: unreachable or 401
}
type HTTPHealth struct{ HC *http.Client } // mgmtapi.New + Health + SetupRequired
type Reconciler struct {
	Client          client.Client
	Scheme          *runtime.Scheme
	Chart           *render.Chart
	Discovery       discovery.DiscoveryInterface
	Health          Health
	OperatorVersion string
	ResyncInterval  time.Duration
}
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error)
// SetupWithManager: For NexoraInstallation; Owns Deployment, DaemonSet, Service, ConfigMap; Watches
// NexoraEngineGroup mapped to spec.installationRef.name.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error
```

Reconcile order:

1. Get the CR.
2. Keys:
   - `mgmt.ca.existingSecret` set → `keys.CheckSecret` with `ca.crt`, `ca.key`; otherwise
     `keys.EnsureSecret("<name>-ca", …, keys.GenerateCA)`.
   - Likewise the KEK (`kek`).
   - Always `EnsureSecret("<name>-operator-token", ["token"])`.
   - `ErrSecretIncomplete` → `Rendered=False` with reason `SecretIncomplete` and the Secret and key in
     the message.
3. Join tokens: list `NexoraEngineGroup` in the namespace. A CR maps to its `status.joinTokenSecret`
   when its `installationRef.name` matches and `JoinTokenReady=True`.
4. `render.BuildValues` with `Injected{Tag: OperatorVersion, CASecret, KEKSecret,
BootstrapTokenSecret, JoinTokenSecrets}`. `ErrImageTagRequired` → reason `ImageTagRequired`.
5. Discovery: `ServerGroupsAndResources` gives group/version and group/version/Kind strings;
   `ServerVersion().GitVersion` gives the version.
6. `Chart.Render`: error → reason `RenderFailed` with the error text (at most 1024 characters).
   `render.Own`: `ErrForeignNamespace` → reason `ForeignNamespace`.
7. Apply each object in order. An error → reason `RenderFailed` with `apply <Kind>/<name>: <error>`;
   stop without pruning.
8. Prune (Architecture).
9. Status and requeue:
   - `version`, `managementURL` and `secrets`.
   - `workloads` from the rendered engine DaemonSets and Deployments (label
     `app.kubernetes.io/name=nexora-engine`), read back from the API.
   - `DatabaseReady`: CNPG `Cluster` `status.conditions[type=Ready].status == "True"`, or the external
     Secret exists.
   - `ManagementReady`: the rendered mgmt Deployment has `availableReplicas >= 1` and `Health.Check`
     reports healthy. A 401 gives reason `Unauthorized`.
   - `SetupRequired` from the health result.
   - `EnginesReady`:
     - `JoinTokenPending` when `PendingGroups` is non-empty;
     - `RollingUpdate` when any workload has `observedGeneration < generation` or updated < desired;
     - `Unavailable` when ready < desired or desired == 0;
     - otherwise `True`.
   - `Rendered=True` and `Ready`.
   - `RequeueAfter: ResyncInterval`.

- [ ] Create `operator/internal/controller/installation/controller_test.go`:
  ```go
  package installation_test

  import (
  	"context"
  	"errors"
  	"path/filepath"
  	"testing"
  	"time"

  	"github.com/google/uuid"
  	appsv1 "k8s.io/api/apps/v1"
  	corev1 "k8s.io/api/core/v1"
  	apimeta "k8s.io/apimachinery/pkg/api/meta"
  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
  	"k8s.io/apimachinery/pkg/types"
  	"k8s.io/client-go/discovery"
  	"k8s.io/client-go/rest"
  	ctrl "sigs.k8s.io/controller-runtime"
  	"sigs.k8s.io/controller-runtime/pkg/client"

  	"github.com/piwi3910/nexora/operator/api/v1alpha1"
  	"github.com/piwi3910/nexora/operator/internal/controller/installation"
  	"github.com/piwi3910/nexora/operator/internal/envtestutil"
  	"github.com/piwi3910/nexora/operator/internal/render"
  )

  type fakeHealth struct {
  	res installation.HealthResult
  	err error
  }

  func (f *fakeHealth) Check(context.Context, string, string) (installation.HealthResult, error) { return f.res, f.err }

  type env struct {
  	t      *testing.T
  	c      client.Client
  	r      *installation.Reconciler
  	health *fakeHealth
  	ns     string
  }

  func setup(t *testing.T, withCNPG bool) *env {
  	var extra []string
  	if withCNPG {
  		extra = append(extra, "testdata/cnpg-crd.yaml")
  	}
  	cfg, c := envtestutil.Start(t, extra...)
  	chart, err := render.LoadChart(filepath.Join(envtestutil.RepoRoot(t), "deploy/helm/nexora"))
  	if err != nil {
  		t.Fatal(err)
  	}
  	disc := discovery.NewDiscoveryClientForConfigOrDie(rest.CopyConfig(cfg))
  	e := &env{t: t, c: c, health: &fakeHealth{}, ns: "inst-" + uuid.NewString()[:8]}
  	e.r = &installation.Reconciler{Client: c, Scheme: c.Scheme(), Chart: chart, Discovery: disc, Health: e.health,
  		OperatorVersion: "sha-abc1234", ResyncInterval: time.Minute}
  	if err := c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: e.ns}}); err != nil {
  		t.Fatal(err)
  	}
  	return e
  }

  func i32(v int32) *int32 { return &v }

  func kwLike(ns string) *v1alpha1.NexoraInstallation {
  	svc := func(name string) *v1alpha1.ServiceSpec { return &v1alpha1.ServiceSpec{Name: name, Type: "ClusterIP"} }
  	return &v1alpha1.NexoraInstallation{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "nexora"},
  		Spec: v1alpha1.NexoraInstallationSpec{
  			Database: v1alpha1.DatabaseSpec{Mode: "external", External: v1alpha1.ExternalDatabaseSpec{ExistingSecret: "nexora-db-app", Key: "uri"}},
  			Engine: v1alpha1.EngineSpec{Groups: []v1alpha1.EngineGroupSpec{{Name: "default", WorkloadName: "nexora-engine-default",
  				Instances: []v1alpha1.EngineInstanceSpec{{Name: "a", Node: "node-1", Service: svc("nexora-dns-a")}, {Name: "b", Node: "node-2", Service: svc("nexora-dns-b")}}}}},
  		}}
  }

  func (e *env) readyEngineGroup(name, secret string) {
  	ctx := context.Background()
  	g := &v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Namespace: e.ns, Name: name},
  		Spec: v1alpha1.NexoraEngineGroupSpec{InstallationRef: v1alpha1.LocalRef{Name: "nexora"}}}
  	if err := e.c.Create(ctx, g); err != nil {
  		e.t.Fatal(err)
  	}
  	g.Status.JoinTokenSecret = secret
  	apimeta.SetStatusCondition(&g.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionJoinTokenReady, Status: metav1.ConditionTrue, Reason: v1alpha1.ReasonReconciled})
  	if err := e.c.Status().Update(ctx, g); err != nil {
  		e.t.Fatal(err)
  	}
  }

  func (e *env) reconcile() *v1alpha1.NexoraInstallation {
  	e.t.Helper()
  	ctx := context.Background()
  	if _, err := e.r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: e.ns, Name: "nexora"}}); err != nil {
  		e.t.Fatalf("reconcile: %v", err)
  	}
  	var inst v1alpha1.NexoraInstallation
  	if err := e.c.Get(ctx, types.NamespacedName{Namespace: e.ns, Name: "nexora"}, &inst); err != nil {
  		e.t.Fatal(err)
  	}
  	return &inst
  }

  func (e *env) get(obj client.Object, name string) error {
  	return e.c.Get(context.Background(), types.NamespacedName{Namespace: e.ns, Name: name}, obj)
  }

  func condition(inst *v1alpha1.NexoraInstallation, typ string) metav1.Condition {
  	if c := apimeta.FindStatusCondition(inst.Status.Conditions, typ); c != nil {
  		return *c
  	}
  	return metav1.Condition{}
  }

  func TestInstallationCreatesChartObjects(t *testing.T) {
  	e := setup(t, false)
  	if err := e.c.Create(context.Background(), kwLike(e.ns)); err != nil {
  		t.Fatal(err)
  	}
  	e.readyEngineGroup("default", "default-join-token")
  	inst := e.reconcile()

  	var mgmt appsv1.Deployment
  	if err := e.get(&mgmt, "nexora-mgmt"); err != nil {
  		t.Fatal(err)
  	}
  	var tokenEnv bool
  	for _, ev := range mgmt.Spec.Template.Spec.Containers[0].Env {
  		tokenEnv = tokenEnv || (ev.Name == "NEXORA_BOOTSTRAP_TOKEN_FILE" && ev.Value == "/etc/nexora/bootstrap-token/token")
  	}
  	if !tokenEnv || mgmt.Labels[v1alpha1.LabelInstallation] != "nexora" || len(mgmt.OwnerReferences) != 1 {
  		t.Fatalf("mgmt deployment env/labels/owners: %v %v %v", tokenEnv, mgmt.Labels, mgmt.OwnerReferences)
  	}
  	for inst, node := range map[string]string{"a": "node-1", "b": "node-2"} {
  		var ds appsv1.DaemonSet
  		if err := e.get(&ds, "nexora-engine-default-"+inst); err != nil {
  			t.Fatalf("daemonset %s: %v", inst, err)
  		}
  		terms := ds.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
  		last := terms[0].MatchExpressions[len(terms[0].MatchExpressions)-1]
  		ru := ds.Spec.UpdateStrategy.RollingUpdate
  		if last.Key != "kubernetes.io/hostname" || last.Values[0] != node || ru.MaxSurge.IntValue() != 1 || ru.MaxUnavailable.IntValue() != 0 {
  			t.Errorf("daemonset %s: pin %v rolling %v", inst, last, ru)
  		}
  		var svc corev1.Service
  		if err := e.get(&svc, "nexora-dns-"+inst); err != nil || svc.Spec.Type != corev1.ServiceTypeClusterIP {
  			t.Errorf("service %s: %v %v", inst, err, svc.Spec.Type)
  		}
  	}
  	for _, name := range []string{"nexora-ca", "nexora-kek", "nexora-operator-token"} {
  		var s corev1.Secret
  		if err := e.get(&s, name); err != nil || len(s.OwnerReferences) != 0 {
  			t.Errorf("secret %s: err=%v owners=%v", name, err, s.OwnerReferences)
  		}
  	}
  	if inst.Status.Secrets.CA != "nexora-ca" || inst.Status.Secrets.OperatorToken != "nexora-operator-token" ||
  		inst.Status.ManagementURL != "http://nexora-mgmt."+e.ns+".svc:8080" || condition(inst, v1alpha1.ConditionRendered).Status != metav1.ConditionTrue {
  		t.Fatalf("status = %+v", inst.Status)
  	}
  }

  func TestInstallationWaitsForJoinTokens(t *testing.T) {
  	e := setup(t, false)
  	_ = e.c.Create(context.Background(), kwLike(e.ns))
  	inst := e.reconcile()
  	var ds appsv1.DaemonSet
  	if err := e.get(&ds, "nexora-engine-default-a"); err == nil {
  		t.Fatal("an engine workload rendered without a join token")
  	}
  	var mgmt appsv1.Deployment
  	if err := e.get(&mgmt, "nexora-mgmt"); err != nil {
  		t.Fatalf("mgmt must render while engines wait: %v", err)
  	}
  	if c := condition(inst, v1alpha1.ConditionEnginesReady); c.Reason != v1alpha1.ReasonJoinTokenPending {
  		t.Fatalf("engines condition = %+v", c)
  	}
  	e.readyEngineGroup("default", "default-join-token")
  	e.reconcile()
  	for _, n := range []string{"nexora-engine-default-a", "nexora-engine-default-b"} {
  		if err := e.get(&ds, n); err != nil {
  			t.Errorf("%s after the join token: %v", n, err)
  		}
  	}
  }

  func TestInstallationPrunesRemovedObjects(t *testing.T) {
  	e := setup(t, false)
  	ctx := context.Background()
  	inst := kwLike(e.ns)
  	_ = e.c.Create(ctx, inst)
  	e.readyEngineGroup("default", "default-join-token")
  	e.reconcile()
  	var a appsv1.DaemonSet
  	_ = e.get(&a, "nexora-engine-default-a")
  	_ = e.get(inst, "nexora")
  	inst.Spec.Engine.Groups[0].Instances = inst.Spec.Engine.Groups[0].Instances[:1]
  	if err := e.c.Update(ctx, inst); err != nil {
  		t.Fatal(err)
  	}
  	e.reconcile()
  	var b appsv1.DaemonSet
  	if err := e.get(&b, "nexora-engine-default-b"); err == nil && b.DeletionTimestamp == nil {
  		t.Error("removed instance b still exists")
  	}
  	var svc corev1.Service
  	if err := e.get(&svc, "nexora-dns-b"); err == nil && svc.DeletionTimestamp == nil {
  		t.Error("removed instance service still exists")
  	}
  	var a2 appsv1.DaemonSet
  	if err := e.get(&a2, "nexora-engine-default-a"); err != nil || a2.UID != a.UID {
  		t.Errorf("instance a replaced: %v", err)
  	}
  }

  func TestInstallationKeepsDatabaseAndKeys(t *testing.T) {
  	e := setup(t, true)
  	ctx := context.Background()
  	inst := kwLike(e.ns)
  	inst.Spec.Database = v1alpha1.DatabaseSpec{Mode: "cnpg", CNPG: v1alpha1.CNPGSpec{Instances: i32(2)}}
  	_ = e.c.Create(ctx, inst)
  	e.readyEngineGroup("default", "default-join-token")
  	e.reconcile()
  	cluster := &unstructured.Unstructured{}
  	cluster.SetAPIVersion("postgresql.cnpg.io/v1")
  	cluster.SetKind("Cluster")
  	if err := e.get(cluster, "nexora-db"); err != nil || len(cluster.GetOwnerReferences()) != 0 {
  		t.Fatalf("cluster: err=%v owners=%v", err, cluster.GetOwnerReferences())
  	}
  	_ = e.get(inst, "nexora")
  	inst.Spec.Database.CNPG.Instances = i32(3)
  	_ = e.c.Update(ctx, inst)
  	e.reconcile()
  	_ = e.get(cluster, "nexora-db")
  	if n, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances"); n != 3 {
  		t.Fatalf("instances = %d", n)
  	}
  	_ = e.get(inst, "nexora")
  	inst.Spec.Database = v1alpha1.DatabaseSpec{Mode: "external", External: v1alpha1.ExternalDatabaseSpec{ExistingSecret: "pg"}}
  	_ = e.c.Update(ctx, inst)
  	e.reconcile()
  	if err := e.get(cluster, "nexora-db"); err != nil || cluster.GetDeletionTimestamp() != nil {
  		t.Fatalf("switching to external deleted the cluster: %v", err)
  	}

  	e2 := setup(t, false)
  	broken := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: e2.ns, Name: "nexora-ca"}, Data: map[string][]byte{"ca.crt": []byte("x")}}
  	_ = e2.c.Create(ctx, broken)
  	_ = e2.c.Create(ctx, kwLike(e2.ns))
  	got := e2.reconcile()
  	var after corev1.Secret
  	_ = e2.get(&after, "nexora-ca")
  	if c := condition(got, v1alpha1.ConditionRendered); c.Reason != v1alpha1.ReasonSecretIncomplete || after.ResourceVersion != broken.ResourceVersion {
  		t.Fatalf("incomplete CA: %+v rv %s->%s", c, broken.ResourceVersion, after.ResourceVersion)
  	}
  }

  func TestInstallationRenderFailureKeepsObjects(t *testing.T) {
  	e := setup(t, false)
  	ctx := context.Background()
  	inst := kwLike(e.ns)
  	_ = e.c.Create(ctx, inst)
  	e.readyEngineGroup("default", "default-join-token")
  	e.reconcile()
  	var before appsv1.Deployment
  	_ = e.get(&before, "nexora-mgmt")
  	_ = e.get(inst, "nexora")
  	inst.Spec.Mgmt.Ingress = v1alpha1.IngressSpec{Enabled: func() *bool { b := true; return &b }()} // host required by the chart
  	if err := e.c.Update(ctx, inst); err != nil {
  		t.Fatal(err)
  	}
  	got := e.reconcile()
  	var after appsv1.Deployment
  	_ = e.get(&after, "nexora-mgmt")
  	if c := condition(got, v1alpha1.ConditionRendered); c.Status != metav1.ConditionFalse || c.Reason != v1alpha1.ReasonRenderFailed || after.ResourceVersion != before.ResourceVersion {
  		t.Fatalf("render failure: %+v rv %s->%s", c, before.ResourceVersion, after.ResourceVersion)
  	}
  }

  func TestInstallationStatusConditions(t *testing.T) {
  	e := setup(t, false)
  	ctx := context.Background()
  	_ = e.c.Create(ctx, kwLike(e.ns))
  	e.readyEngineGroup("default", "default-join-token")
  	_ = e.c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: e.ns, Name: "nexora-db-app"}, Data: map[string][]byte{"uri": []byte("postgres://x")}})
  	e.reconcile()
  	e.health.res = installation.HealthResult{Healthy: true, SetupRequired: true}
  	var mgmt appsv1.Deployment
  	_ = e.get(&mgmt, "nexora-mgmt")
  	mgmt.Status.Replicas, mgmt.Status.AvailableReplicas, mgmt.Status.ReadyReplicas = 2, 1, 1
  	_ = e.c.Status().Update(ctx, &mgmt)
  	setDS := func(name string, desired, updated, ready int32) {
  		var ds appsv1.DaemonSet
  		_ = e.get(&ds, name)
  		ds.Status = appsv1.DaemonSetStatus{DesiredNumberScheduled: desired, UpdatedNumberScheduled: updated, NumberReady: ready,
  			CurrentNumberScheduled: desired, NumberAvailable: ready, ObservedGeneration: ds.Generation}
  		if err := e.c.Status().Update(ctx, &ds); err != nil {
  			t.Fatal(err)
  		}
  	}
  	setDS("nexora-engine-default-a", 1, 0, 1)
  	setDS("nexora-engine-default-b", 1, 1, 1)
  	inst := e.reconcile()
  	for typ, want := range map[string]metav1.ConditionStatus{v1alpha1.ConditionManagementReady: "True", v1alpha1.ConditionSetupRequired: "True", v1alpha1.ConditionDatabaseReady: "True", v1alpha1.ConditionEnginesReady: "False", v1alpha1.ConditionReady: "False"} {
  		if c := condition(inst, typ); c.Status != want {
  			t.Errorf("%s = %+v, want %s", typ, c, want)
  		}
  	}
  	if c := condition(inst, v1alpha1.ConditionEnginesReady); c.Reason != v1alpha1.ReasonRollingUpdate {
  		t.Errorf("engines reason = %s", c.Reason)
  	}
  	setDS("nexora-engine-default-a", 1, 1, 1)
  	inst = e.reconcile()
  	if condition(inst, v1alpha1.ConditionReady).Status != metav1.ConditionTrue || len(inst.Status.Workloads) != 2 {
  		t.Fatalf("ready: %+v workloads %v", condition(inst, v1alpha1.ConditionReady), inst.Status.Workloads)
  	}
  	e.health.err = errors.New("401 unauthorized")
  	inst = e.reconcile()
  	if c := condition(inst, v1alpha1.ConditionManagementReady); c.Status != metav1.ConditionFalse {
  		t.Fatalf("health error: %+v", c)
  	}
  }

  func TestInstallationDefaultsImageTagToOperatorVersion(t *testing.T) {
  	e := setup(t, false)
  	_ = e.c.Create(context.Background(), kwLike(e.ns))
  	e.readyEngineGroup("default", "default-join-token")
  	inst := e.reconcile()
  	var mgmt appsv1.Deployment
  	_ = e.get(&mgmt, "nexora-mgmt")
  	if img := mgmt.Spec.Template.Spec.Containers[0].Image; img != "192.168.10.131/azrtydxb/nexora-mgmt:sha-abc1234" || inst.Status.Version != "sha-abc1234" {
  		t.Fatalf("image %s version %s", img, inst.Status.Version)
  	}
  	e.r.OperatorVersion = "dev"
  	inst = e.reconcile()
  	if c := condition(inst, v1alpha1.ConditionRendered); c.Reason != v1alpha1.ReasonImageTagRequired {
  		t.Fatalf("dev operator: %+v", c)
  	}
  }
  ```
  `TestInstallationRenderFailureKeepsObjects` uses the chart's `mgmt.ingress.host is required` failure. Run
  `scripts/dev-exec.sh 'make operator-test'` and expect a build failure (package missing). Implement
  per the reconcile order, and expect PASS. The `401` case maps to `ManagementReady=False`; the reason
  is `Unauthorized` when `errors.Is(err, mgmtapi.ErrUnauthorized)`, else `ManagementUnavailable`.
- [ ] Create `operator/internal/controller/installation/rbac_test.go`:
  ```go
  package installation_test

  import (
  	"bufio"
  	"bytes"
  	"context"
  	"io"
  	"os"
  	"path/filepath"
  	"strings"
  	"testing"

  	rbacv1 "k8s.io/api/rbac/v1"
  	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
  	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
  	"sigs.k8s.io/controller-runtime/pkg/client"
  	"sigs.k8s.io/yaml"

  	"github.com/piwi3910/nexora/operator/internal/envtestutil"
  	"github.com/piwi3910/nexora/operator/internal/render"
  )

  func manifestDocs(t *testing.T) []*unstructured.Unstructured {
  	t.Helper()
  	b, err := os.ReadFile(filepath.Join(envtestutil.RepoRoot(t), "deploy/operator/operator.yaml"))
  	if err != nil {
  		t.Fatal(err)
  	}
  	r := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(b)))
  	var out []*unstructured.Unstructured
  	for {
  		doc, err := r.Read()
  		if err == io.EOF {
  			break
  		} else if err != nil {
  			t.Fatal(err)
  		}
  		u := &unstructured.Unstructured{}
  		if err := yaml.Unmarshal(doc, &u.Object); err != nil || len(u.Object) == 0 {
  			continue
  		}
  		out = append(out, u)
  	}
  	return out
  }

  func TestOperatorRBACCoversManagedKinds(t *testing.T) {
  	var role rbacv1.ClusterRole
  	for _, u := range manifestDocs(t) {
  		if u.GetKind() == "ClusterRole" && u.GetName() == "nexora-operator" {
  			b, _ := yaml.Marshal(u.Object)
  			if err := yaml.Unmarshal(b, &role); err != nil {
  				t.Fatal(err)
  			}
  		}
  	}
  	allows := func(group, resource string, verbs ...string) bool {
  		have := map[string]bool{}
  		for _, rule := range role.Rules {
  			for _, g := range rule.APIGroups {
  				for _, res := range rule.Resources {
  					if g == group && res == resource {
  						for _, v := range rule.Verbs {
  							have[v] = true
  						}
  					}
  				}
  			}
  		}
  		for _, v := range verbs {
  			if !have[v] {
  				return false
  			}
  		}
  		return true
  	}
  	for _, gvk := range render.ManagedKinds {
  		res := strings.ToLower(gvk.Kind) + "s"
  		if gvk.Kind == "Ingress" {
  			res = "ingresses"
  		}
  		if !allows(gvk.Group, res, "get", "list", "watch", "create", "patch", "delete") {
  			t.Errorf("RBAC lacks %s/%s", gvk.Group, res)
  		}
  	}
  	for _, want := range [][2]string{{"", "secrets"}, {"nexora.io", "nexorainstallations/status"}, {"nexora.io", "nexoraenginegroups/finalizers"}} {
  		if !allows(want[0], want[1], "update") {
  			t.Errorf("RBAC lacks update on %s/%s", want[0], want[1])
  		}
  	}
  	if !allows("", "events", "create", "patch") {
  		t.Error("RBAC lacks events")
  	}
  }

  func TestOperatorManifestsApplyToAPIServer(t *testing.T) {
  	_, c := envtestutil.Start(t)
  	ctx := context.Background()
  	for _, u := range manifestDocs(t) {
  		if err := c.Apply(ctx, client.ApplyConfigurationFromUnstructured(u), client.FieldOwner("test"), client.ForceOwnership); err != nil {
  			t.Errorf("apply %s/%s: %v", u.GetKind(), u.GetName(), err)
  		}
  	}
  }
  ```
  (Task 1's `envtestutil.Start` already installs `deploy/operator/crds/`.) Run and expect FAIL if any
  rendered kind is missing from Task 8's rules; fix the chart RBAC in a follow-up reported to the lead
  (Task 8 is committed), otherwise PASS.
- [ ] Register in `setupControllers`:
  ```go
  chart, err := render.LoadChart(opts.chartDir)
  if err != nil {
  	return fmt.Errorf("load chart %s: %w", opts.chartDir, err)
  }
  disc, err := discovery.NewDiscoveryClientForConfig(mgr.GetConfig())
  if err != nil {
  	return err
  }
  if err := (&installation.Reconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Chart: chart, Discovery: disc,
  	Health: installation.HTTPHealth{HC: &http.Client{Timeout: 5 * time.Second}}, OperatorVersion: version.Version,
  	ResyncInterval: opts.resyncInterval}).SetupWithManager(mgr); err != nil {
  	return fmt.Errorf("installation controller: %w", err)
  }
  ```
  and `mgr.AddHealthzCheck("healthz", healthz.Ping)` plus `mgr.AddReadyzCheck("readyz", healthz.Ping)`.
  Run `cd operator && go vet ./... && go build ./cmd/nexora-operator` and
  `scripts/dev-exec.sh 'make operator-test'`, and expect PASS. Report the paths.

## Task 10: kw operator end-to-end in nexora-optest

Files:

- `scripts/kw-operator-e2e.sh`: created.
- `operator/test/kw/kw_operator_test.go`: created (build tag `kwe2e`); `TestKwOperator` and its subtests.
- `operator/test/kw/helpers_test.go`: created (build tag `kwe2e`); kubectl, probe and API helpers.
- `operator/test/kw/testdata/installation.yaml`, `operator/test/kw/testdata/enginegroups.yaml`,
  `operator/test/kw/testdata/probe.yaml`, `operator/test/kw/testdata/cleanup-job.yaml`: created.
- `.procoder/notes/plan-review.md`: the `## M9 operator e2e (<date>)` record.

Interfaces:

- Script: `scripts/kw-operator-e2e.sh [--tag TAG] [--skip-build] [--keep]`.
- Env for the test (set by the script):
  - `NEXORA_KW_CONTEXT` (default `kw`), `NEXORA_OPTEST_NAMESPACE` (`nexora-optest`),
    `NEXORA_OPTEST_TAG` (`sha-<7>`);
  - `NEXORA_OPTEST_NODES` (`worker-21,worker-22,worker-23`), `NEXORA_OPTEST_S3_PATH`
    (`s3://nexora-optest/<run id>`).
- Objects:
  - `NexoraInstallation` `nexora-optest`:
    - `image.tag` from env, `mgmt.replicas: 2`;
    - `database.mode: cnpg` with `instances: 2`, `storageClass: longhorn`, `size: 1Gi`, and backup to
      `NEXORA_OPTEST_S3_PATH` at `http://minio.minio.svc.cluster.local:9000` with credentials Secret
      `optest-s3` (keys `ACCESS_KEY_ID`, `ACCESS_SECRET_KEY`);
    - `engine.stateDir.hostPathPrefix: /var/lib/nexora-optest`, `engine.workers: 2`, small resources;
    - group `default` with instances `a` on node 1 (Service `nexora-optest-dns-a`, ClusterIP) and `b` on
      node 2 (`nexora-optest-dns-b`);
    - group `edge` with `nodeAffinity` hostname In [node 3] and Service `nexora-optest-dns-edge`
      (ClusterIP).
  - `NexoraEngineGroup` `default` (`groupName: default`) and `edge` (`description: optest edge`,
    `deletionPolicy: Delete`).
  - Pod `probe` (image `192.168.10.131/azrtydxb/nexora-dev:toolbox-1`, `sleep infinity`).

- [ ] Create `scripts/kw-operator-e2e.sh`:
  ```bash
  #!/usr/bin/env bash
  # Operator e2e on kw in the disposable namespace nexora-optest: build the three images, install the CRDs and
  # the operator (namespace scope), run TestKwOperator from operator/test/kw, and clean up. Never touches the
  # production namespace nexora or its addresses.
  #   scripts/kw-operator-e2e.sh [--tag TAG] [--skip-build] [--keep]
  set -euo pipefail
  root="$(cd "$(dirname "$0")/.." && pwd)"
  ctx="${NEXORA_KW_CONTEXT:-kw}"
  ns=nexora-optest
  tag="sha-$(git -C "$root" rev-parse --short=7 HEAD)"
  build=1 keep=0
  while [ $# -gt 0 ]; do
  	case "$1" in
  	--tag) tag="$2"; shift 2 ;;
  	--skip-build) build=0; shift ;;
  	--keep) keep=1; shift ;;
  	*) echo "usage: $0 [--tag TAG] [--skip-build] [--keep]" >&2; exit 2 ;;
  	esac
  done
  [ "$ns" != nexora ] || { echo "refusing the production namespace" >&2; exit 2; }
  k() { kubectl --context "$ctx" -n "$ns" "$@"; }
  run="$(date +%Y%m%d%H%M%S)"
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT

  if [ "$build" = 1 ]; then
  	git -C "$root" worktree add --detach "$tmp/src" HEAD
  	for img in engine mgmt operator; do
  		"$root/scripts/build-image.sh" -f "deploy/docker/$img.Dockerfile" -n "nexora-$img" -t "$tag" "$tmp/src"
  	done
  	git -C "$root" worktree remove --force "$tmp/src"
  fi

  if kubectl --context "$ctx" get namespace "$ns" >/dev/null 2>&1; then
  	[ "$(kubectl --context "$ctx" get namespace "$ns" -o jsonpath='{.metadata.labels.nexora\.io/e2e}')" = operator ] ||
  		{ echo "namespace $ns exists without label nexora.io/e2e=operator; refusing to reuse it" >&2; exit 1; }
  else
  	kubectl --context "$ctx" create namespace "$ns"
  	kubectl --context "$ctx" label namespace "$ns" nexora.io/e2e=operator
  fi
  kubectl --context "$ctx" apply --server-side -f "$root/deploy/operator/crds/"
  helm --kube-context "$ctx" upgrade --install nexora-operator "$root/deploy/helm/nexora-operator" -n "$ns" \
  	--set image.tag="$tag" --set image.pullPolicy=Always --set rbac.scope=namespace --set-json "watchNamespaces=[\"$ns\"]" --wait

  # MinIO credentials for the CNPG backup; never printed.
  kubectl --context "$ctx" -n minio get secret minio-root -o json |
  	jq '{apiVersion:"v1",kind:"Secret",metadata:{name:"optest-s3"},data:{ACCESS_KEY_ID:(.data["MINIO_ROOT_USER"] // .data["rootUser"]),ACCESS_SECRET_KEY:(.data["MINIO_ROOT_PASSWORD"] // .data["rootPassword"])}}' |
  	k apply -f -
  k apply -f "$root/operator/test/kw/testdata/probe.yaml"
  k wait --for=condition=Ready pod/probe --timeout=5m
  k exec probe -- sh -c 'curl -fsS --aws-sigv4 "aws:amz:us-east-1:s3" --user "$(cat /s3/ACCESS_KEY_ID):$(cat /s3/ACCESS_SECRET_KEY)" -X PUT http://minio.minio.svc.cluster.local:9000/nexora-optest -o /dev/null -w "%{http_code}\n" | grep -Eq "^(200|409)$"'

  status=0
  (cd "$root/operator" && NEXORA_KW_CONTEXT="$ctx" NEXORA_OPTEST_NAMESPACE="$ns" NEXORA_OPTEST_TAG="$tag" \
  	NEXORA_OPTEST_NODES="${NEXORA_OPTEST_NODES:-worker-21,worker-22,worker-23}" NEXORA_OPTEST_S3_PATH="s3://nexora-optest/$run" \
  	go test -tags kwe2e -count=1 -timeout 90m -v ./test/kw -run TestKwOperator) || status=$?

  if [ "$keep" = 0 ]; then
  	k exec probe -- sh -c "for key in \$(curl -fsS --aws-sigv4 aws:amz:us-east-1:s3 --user \"\$(cat /s3/ACCESS_KEY_ID):\$(cat /s3/ACCESS_SECRET_KEY)\" 'http://minio.minio.svc.cluster.local:9000/nexora-optest?list-type=2&prefix=$run/' | grep -o '<Key>[^<]*' | cut -c6-); do curl -fsS --aws-sigv4 aws:amz:us-east-1:s3 --user \"\$(cat /s3/ACCESS_KEY_ID):\$(cat /s3/ACCESS_SECRET_KEY)\" -X DELETE \"http://minio.minio.svc.cluster.local:9000/nexora-optest/\$key\"; done" || { echo "S3 prefix $run left behind" >&2; status=1; }
  	for node in ${NEXORA_OPTEST_NODES:-worker-21 worker-22 worker-23}; do
  		node=${node//,/ }
  		sed "s/NODE/$node/g" "$root/operator/test/kw/testdata/cleanup-job.yaml" | k apply -f -
  	done
  	k wait --for=condition=complete job -l nexora.io/e2e-cleanup=true --timeout=5m || { echo "node state cleanup incomplete" >&2; status=1; }
  	helm --kube-context "$ctx" uninstall nexora-operator -n "$ns" || true
  	kubectl --context "$ctx" delete namespace "$ns" --wait=true --timeout=10m
  fi
  exit "$status"
  ```
  `probe.yaml` mounts Secret `optest-s3` at `/s3` (mode 0400). `cleanup-job.yaml` is a Job
  `cleanup-NODE` pinned with `nodeName: NODE`, image `192.168.10.131/library/busybox:1.37`, running
  `rm -rf /host/var/lib/nexora-optest` with hostPath `/var/lib/nexora-optest` mounted at
  `/host/var/lib/nexora-optest`, and label `nexora.io/e2e-cleanup: "true"`. Split
  `NEXORA_OPTEST_NODES` on commas once, before the loop. Adjust the MinIO Secret key names to the real
  keys of `minio/minio-root`, found with
  `kubectl --context kw -n minio get secret minio-root -o jsonpath='{.data}' | jq 'keys'`, which prints
  names only.
- [ ] Create `operator/test/kw/helpers_test.go` (`//go:build kwe2e`) with:
  - `kubectl(t, args...) string` (runs `kubectl --context $NEXORA_KW_CONTEXT -n $ns`, fails the test on
    error);
  - `probe(t, stdin string, script string) string` (`kubectl exec -i probe -- sh -c script`);
  - `apiCall(t, method, path string, body any, out any) int`: reads the token from Secret
    `nexora-optest-operator-token` with the controller-runtime client and runs, in the probe,
    `read -r T; curl -sS -o /tmp/out -w '%{http_code}' -H "Authorization: Bearer $T" -H 'Content-Type: application/json' -X METHOD --data @- http://nexora-optest-mgmt.nexora-optest.svc:8080/api/v1PATH; cat /tmp/out`,
    with the token on the first stdin line and the JSON body after it;
  - `newClient(t) client.Client` (`config.GetConfigWithContext(ctx)` plus the v1alpha1 scheme);
  - `waitFor(t, timeout, what string, cond func() (bool, error))`;
  - `condTrue(inst, typ) bool`;
  - `dnsperf(t, serviceIP string, seconds int) (lost int, noerror bool)` running
    `printf 'www.optest.nexora.test. A\n' > /tmp/q-$$; dnsperf -s IP -d /tmp/q-$$ -Q 5 -l SECONDS -t 2`
    in the probe and parsing `Queries lost:` and `NOERROR`.
- [ ] Create `operator/test/kw/kw_operator_test.go`:

  ```go
  //go:build kwe2e

  package kw_test

  import (
  	"context"
  	"os"
  	"strings"
  	"sync"
  	"testing"
  	"time"

  	appsv1 "k8s.io/api/apps/v1"
  	corev1 "k8s.io/api/core/v1"
  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
  	"k8s.io/apimachinery/pkg/types"
  	"sigs.k8s.io/controller-runtime/pkg/client"

  	"github.com/piwi3910/nexora/operator/api/v1alpha1"
  	"github.com/piwi3910/nexora/operator/internal/render"
  )

  func TestKwOperator(t *testing.T) {
  	ns := os.Getenv("NEXORA_OPTEST_NAMESPACE")
  	nodes := strings.Split(os.Getenv("NEXORA_OPTEST_NODES"), ",")
  	c := newClient(t)
  	ctx := context.Background()
  	key := types.NamespacedName{Namespace: ns, Name: "nexora-optest"}
  	var inst v1alpha1.NexoraInstallation

  	t.Run("guards", func(t *testing.T) {
  		if ns != "nexora-optest" {
  			t.Fatalf("namespace %q: the operator e2e runs only in nexora-optest", ns)
  		}
  		for _, n := range nodes {
  			if n == "master-12" || n == "master-13" {
  				t.Fatalf("node %s carries the production DNS addresses", n)
  			}
  		}
  		manifest := loadInstallation(t) // testdata/installation.yaml with the tag, nodes and S3 path substituted
  		chart, err := render.LoadChart("../../../deploy/helm/nexora")
  		if err != nil {
  			t.Fatal(err)
  		}
  		vals, err := render.BuildValues(manifest.Spec, render.Injected{Tag: os.Getenv("NEXORA_OPTEST_TAG"), BootstrapTokenSecret: "x",
  			JoinTokenSecrets: map[string]string{"default": "x", "edge": "x"}})
  		if err != nil {
  			t.Fatal(err)
  		}
  		objs, err := chart.Render(render.Target{Name: key.Name, Namespace: ns, KubeVersion: "v1.34.4", APIVersions: []string{"postgresql.cnpg.io/v1"}}, vals.Map)
  		if err != nil {
  			t.Fatal(err)
  		}
  		for _, o := range objs {
  			if o.GetKind() != "Service" {
  				continue
  			}
  			typ, _, _ := unstructured.NestedString(o.Object, "spec", "type")
  			ip, _, _ := unstructured.NestedString(o.Object, "spec", "loadBalancerIP")
  			if typ == "LoadBalancer" || ip != "" {
  				t.Fatalf("Service %s would take a LoadBalancer address (%s %s)", o.GetName(), typ, ip)
  			}
  		}
  		kubectl(t, "apply", "-f", writeTemp(t, manifest), "-f", "testdata/enginegroups.yaml")
  	})

  	t.Run("install", func(t *testing.T) {
  		waitFor(t, 15*time.Minute, "installation Ready", func() (bool, error) {
  			if err := c.Get(ctx, key, &inst); err != nil {
  				return false, err
  			}
  			return condTrue(&inst, v1alpha1.ConditionReady), nil
  		})
  		var zone map[string]any
  		if code := apiCall(t, "POST", "/zones", map[string]any{"name": "optest.nexora.test.", "kind": "primary", "default_ttl": 60,
  			"soa": map[string]string{"mname": "ns1.optest.nexora.test.", "rname": "hostmaster.optest.nexora.test."},
  			"nameservers": []string{"ns1.optest.nexora.test."}}, &zone); code != 201 {
  			t.Fatalf("create zone -> %d %v", code, zone)
  		}
  		if code := apiCall(t, "POST", "/zones/"+zone["id"].(string)+"/records", map[string]any{"name": "www.optest.nexora.test.", "type": "A", "data": "192.0.2.10", "ttl": 60}, nil); code != 201 {
  			t.Fatalf("create record -> %d", code)
  		}
  		for _, svc := range []string{"nexora-optest-dns-a", "nexora-optest-dns-b"} {
  			ip := serviceIP(t, c, ns, svc)
  			waitFor(t, 2*time.Minute, svc+" answers the zone", func() (bool, error) {
  				out := probe(t, "", "dig +short +time=1 +tries=1 @"+ip+" www.optest.nexora.test. A")
  				return strings.TrimSpace(out) == "192.0.2.10", nil
  			})
  		}
  	})

  	t.Run("engine-groups", func(t *testing.T) {
  		var edge v1alpha1.NexoraEngineGroup
  		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "edge"}, &edge); err != nil || edge.Status.GroupID == "" {
  			t.Fatalf("edge engine group: %v %+v", err, edge.Status)
  		}
  		waitFor(t, 5*time.Minute, "an engine enrolled into edge on "+nodes[2], func() (bool, error) {
  			var engines []map[string]any
  			apiCall(t, "GET", "/engines", nil, &engines)
  			for _, e := range engines {
  				if e["engine_group_id"] == edge.Status.GroupID && e["node_name"] == nodes[2] && e["connected"] == true {
  					return true, nil
  				}
  			}
  			return false, nil
  		})
  	})

  	t.Run("rolling-update", func(t *testing.T) {
  		before := enginePodUIDs(t, c, ns)
  		ips := map[string]string{"a": serviceIP(t, c, ns, "nexora-optest-dns-a"), "b": serviceIP(t, c, ns, "nexora-optest-dns-b")}
  		type result struct {
  			lost    int
  			noerror bool
  		}
  		results := map[string]result{}
  		var mu sync.Mutex
  		var wg sync.WaitGroup
  		for name, ip := range ips {
  			wg.Add(1)
  			go func() {
  				defer wg.Done()
  				lost, ok := dnsperf(t, ip, 240)
  				mu.Lock()
  				results[name] = result{lost, ok}
  				mu.Unlock()
  			}()
  		}
  		time.Sleep(10 * time.Second)
  		if err := c.Get(ctx, key, &inst); err != nil {
  			t.Fatal(err)
  		}
  		one := int32(1)
  		inst.Spec.Engine.Workers = &one
  		if err := c.Update(ctx, &inst); err != nil {
  			t.Fatal(err)
  		}
  		waitFor(t, 200*time.Second, "every engine pod replaced and ready", func() (bool, error) {
  			after := enginePodUIDs(t, c, ns)
  			if len(after) != len(before) {
  				return false, nil
  			}
  			for uid := range after {
  				if before[uid] {
  					return false, nil
  				}
  			}
  			return enginesReady(t, c, ns), nil
  		})
  		wg.Wait()
  		for name, r := range results {
  			if r.lost != 0 || !r.noerror {
  				t.Errorf("instance %s during the roll: %d lost, NOERROR=%v", name, r.lost, r.noerror)
  			}
  		}
  	})

  	t.Run("join-token-rotation", func(t *testing.T) {
  		var edge v1alpha1.NexoraEngineGroup
  		_ = c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "edge"}, &edge)
  		oldID := edge.Status.JoinTokenID
  		var sec corev1.Secret
  		_ = c.Get(ctx, types.NamespacedName{Namespace: ns, Name: edge.Status.JoinTokenSecret}, &sec)
  		oldToken := string(sec.Data["join-token"])
  		edge.Spec.JoinToken.TTL = &metav1.Duration{Duration: 3 * time.Minute}
  		edge.Spec.JoinToken.RenewBefore = &metav1.Duration{Duration: 2 * time.Minute}
  		edge.Spec.JoinToken.RevokeGracePeriod = &metav1.Duration{Duration: 30 * time.Second}
  		if err := c.Update(ctx, &edge); err != nil {
  			t.Fatal(err)
  		}
  		waitFor(t, 5*time.Minute, "rotated join token and revoked predecessor", func() (bool, error) {
  			_ = c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "edge"}, &edge)
  			_ = c.Get(ctx, types.NamespacedName{Namespace: ns, Name: edge.Status.JoinTokenSecret}, &sec)
  			if edge.Status.JoinTokenID == oldID || string(sec.Data["join-token"]) == oldToken {
  				return false, nil
  			}
  			var tokens []map[string]any
  			apiCall(t, "GET", "/join-tokens", nil, &tokens)
  			for _, tok := range tokens {
  				if tok["id"] == oldID {
  					return tok["state"] == "revoked", nil
  				}
  			}
  			return false, nil
  		})
  	})

  	t.Run("prune", func(t *testing.T) {
  		_ = c.Get(ctx, key, &inst)
  		var groups []v1alpha1.EngineGroupSpec
  		for _, g := range inst.Spec.Engine.Groups {
  			if g.Name != "edge" {
  				groups = append(groups, g)
  			}
  		}
  		inst.Spec.Engine.Groups = groups
  		if err := c.Update(ctx, &inst); err != nil {
  			t.Fatal(err)
  		}
  		waitFor(t, 3*time.Minute, "edge DaemonSet pruned", func() (bool, error) {
  			var ds appsv1.DaemonSet
  			err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "nexora-optest-engine-edge"}, &ds)
  			return client.IgnoreNotFound(err) == nil && err != nil, nil
  		})
  	})

  	t.Run("cnpg-failover", func(t *testing.T) { cnpgFailover(t, c, ns) })
  	t.Run("cnpg-backup-restore", func(t *testing.T) { cnpgBackupRestore(t, c, ns) })

  	t.Run("delete-retains-state", func(t *testing.T) {
  		_ = c.Get(ctx, key, &inst)
  		if err := c.Delete(ctx, &inst); err != nil {
  			t.Fatal(err)
  		}
  		waitFor(t, 2*time.Minute, "workloads garbage-collected", func() (bool, error) {
  			var d appsv1.Deployment
  			err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "nexora-optest-mgmt"}, &d)
  			return err != nil && client.IgnoreNotFound(err) == nil && !enginesExist(t, c, ns), nil
  		})
  		cluster := &unstructured.Unstructured{}
  		cluster.SetAPIVersion("postgresql.cnpg.io/v1")
  		cluster.SetKind("Cluster")
  		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "nexora-db"}, cluster); err != nil || cluster.GetDeletionTimestamp() != nil {
  			t.Fatalf("CNPG cluster after deleting the installation: %v", err)
  		}
  		for _, s := range []string{"nexora-optest-ca", "nexora-optest-kek", "nexora-optest-operator-token"} {
  			var sec corev1.Secret
  			if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: s}, &sec); err != nil {
  				t.Errorf("secret %s: %v", s, err)
  			}
  		}
  	})
  }
  ```

  Add to `helpers_test.go`:
  - `loadInstallation`, `writeTemp`, `serviceIP`;
  - `enginePodUIDs` (pods labelled `app.kubernetes.io/name=nexora-engine`, UID set);
  - `enginesReady` (every engine DaemonSet has updated == desired == ready and observedGeneration ==
    generation);
  - `enginesExist`.

  And two subtest bodies:
  - `cnpgFailover`:
    1. Read `status.currentPrimary` of Cluster `nexora-db` and the engines' max `applied_version` from
       `/engines`.
    2. Delete the primary pod.
    3. Require `currentPrimary` to change within 120 s, then `apiCall GET /health == 200` within 60 s.
    4. Patch NexoraEngineGroup `default` `description` to `failover-<unix>`.
    5. Require `/engine-groups` to show it and every connected engine's `applied_version` to rise within
       120 s.
  - `cnpgBackupRestore`:
    1. Create `Backup` `optest-<unix>` (`spec.cluster.name: nexora-db`, `method: barmanObjectStore`) and
       require `status.phase == completed` within 10 minutes.
    2. Build values with `render.BuildValues` from the installation manifest, with
       `database.cnpg.clusterName: nexora-db-restore`, `backup.enabled: false`, and recovery enabled with
       `sourceServerName: nexora-db`.
    3. `Chart.Render` it and take the single `Cluster` object; `Own` is not called, so the object has no
       owner. Apply it with the client.
    4. Require its `Ready` condition within 15 minutes.
    5. Require
       `kubectl exec nexora-db-restore-1 -c postgres -- psql -d nexora -tAc "select count(*) from engine_groups where name = 'edge'"`
       to print `1`.
    6. Delete the restore Cluster.

  The `edge` group is deleted in the API only when its CR is deleted, which never happens before this
  subtest, so the row exists.

- [ ] Run `cd operator && go vet -tags kwe2e ./test/kw` and expect success. Run
      `scripts/kw-operator-e2e.sh` from the laptop after the lead has committed Tasks 1–9. Expect
      `--- PASS: TestKwOperator` with every subtest passing, and exit 0. On a failure, rerun with
      `--keep`, diagnose in `nexora-optest` (never in `nexora`), fix in the owning task's files through
      the lead, and run again.
- [ ] Before and after the run, check that production was not touched:
      `kubectl --context kw -n nexora get nexorainstallations` prints `No resources found`, and the
      Services `nexora-dns` and `nexora-dns-2` still hold `192.168.10.136` and `192.168.10.139`
      (`kubectl --context kw -n nexora get svc nexora-dns nexora-dns-2 -o wide`).
- [ ] Record in `.procoder/notes/plan-review.md` under `## M9 operator e2e (<date>)`: the image tag, each
      subtest's result and duration, the dnsperf lost counts per instance, the failover time
      (primary change to healthy API) and the backup/restore duration. Report the paths.

## Task 11: Operations guide, kw README and docs test

Files:

- `docs/operations.md`: sections `## Install with the Kubernetes operator` and
  `## PostgreSQL high availability and backups`; the `### CloudNativePG` subsection of
  `## Backup and restore PostgreSQL` points to the new section.
- `deploy/deploytest/docs_test.go`: the two headings.
- `deploy/kw/README.md`: section `## Operator e2e (namespace nexora-optest)`.

Interfaces: none.

- [ ] In `deploy/deploytest/docs_test.go` add `"## Install with the Kubernetes operator"` and
      `"## PostgreSQL high availability and backups"` to the required headings. Run
      `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestOperationsDoc -count=1'` and expect FAIL
      `docs/operations.md lacks heading "## Install with the Kubernetes operator"`.
- [ ] Write `## Install with the Kubernetes operator` after `## Install with Docker Compose`:
  1. Prerequisites: Kubernetes 1.28+, the CNPG operator for `database.mode: cnpg`.
  2. Install with `helm upgrade --install nexora-operator deploy/helm/nexora-operator -n nexora-operator --create-namespace`
     (cluster scope), or `kubectl apply --server-side -f deploy/operator/crds/ && kubectl apply -f deploy/operator/operator.yaml`.
     Namespace scope uses `--set rbac.scope=namespace --set-json 'watchNamespaces=["nexora"]'`.
  3. A complete `NexoraInstallation` and two `NexoraEngineGroup` examples, taken from
     `operator/test/kw/testdata/` with placeholders for addresses and nodes.
  4. The field mapping rule (same names as `deploy/helm/nexora/values.yaml`), what the operator injects,
     and the generated Secrets and their retention.
  5. Conditions and reasons, with `kubectl get nxi,nxeg` and `kubectl describe` examples.
  6. First-run setup (`SetupRequired`), the system user `nexora-operator`, and rotating its token by
     deleting the `token` key's Secret (it is recreated with a new token, and the management plane picks
     it up within `NEXORA_BOOTSTRAP_TOKEN_RELOAD_INTERVAL`).
  7. Upgrades: upgrade the operator (its image carries the matching chart); `spec.image.tag` pins a
     version; engines roll as with Helm.
  8. Deleting: workloads go, the database and key Secrets stay; `deletionPolicy` of engine groups.
  9. Limits: no adoption of Helm releases, no objects outside the namespace.
- [ ] Write `## PostgreSQL high availability and backups` after `## Backup and restore PostgreSQL`:
  - the HA values (`instances`, `antiAffinity`, `primaryUpdateMethod`, `resources`,
    `postgresql.parameters`) and what a failover looks like (503s for the switchover, no restart);
  - backups to S3-compatible storage with a MinIO example (credentials Secret, `destinationPath`,
    `endpointURL`, schedule, retention, `kubectl get backups,scheduledbackups`, the cluster's
    `ContinuousArchiving` condition);
  - the image requirement (`barman-cloud` in the PostgreSQL image);
  - restoring into a new cluster with `database.cnpg.recovery` (a different `clusterName`, pointing
    `database.external` or the new cluster name at it, then switching mgmt);
  - what stays manual (the CA and KEK backups, cross-region copies);
  - the in-tree `barmanObjectStore` deprecation note.

  Replace the first sentence of `### CloudNativePG` with a link to the new section. Run the test
  again and expect PASS (the path check covers every backticked path).

- [ ] Add `## Operator e2e (namespace nexora-optest)` to `deploy/kw/README.md`: what
      `scripts/kw-operator-e2e.sh` builds, installs and deletes; the guards; the MinIO bucket
      `nexora-optest`; the nodes; `--keep` for debugging; and the last recorded result from
      `.procoder/notes/plan-review.md`. Run `scripts/pc-format.sh docs/operations.md deploy/kw/README.md`
      and `scripts/dev-exec.sh 'go test ./deploy/deploytest -count=1'`, and expect PASS. Report the paths.

## Task 12: Deploy M9 to kw production and close the issues

Files:

- `.procoder/notes/plan-review.md`: the M9 production deployment record.

Interfaces: none.

- [ ] Confirm the production Helm render is unchanged: run
      `scripts/dev-exec.sh 'go test ./deploy/deploytest -run TestHelmKwRenderUnchanged -count=1'` and expect
      PASS.
- [ ] After the lead has committed Tasks 1–11, run `make e2e` and `make operator-test` in the dev pod
      (`scripts/dev-exec.sh`), including `TestGUICoverage`, and expect PASS.
- [ ] Start the DNS probe, then run `scripts/kw-deploy.sh`, which runs 5 queries/s to 192.168.10.136 and
      192.168.10.139. Expect `0 lost` on both. On any lost query, run `helm rollback nexora`, record the
      cause in `.procoder/notes/plan-review.md` under `## M9 (<date>)`, and stop.
- [ ] Run `scripts/kw-acceptance.sh` and expect PASS for `TestKwSmoke`, `TestKwFullProduct` and
      `TestKwFilterCategories`. On failure, run `helm rollback nexora` and record why.
- [ ] Verify that `kubectl --context kw -n nexora get nexorainstallations` prints `No resources found` and
      `kubectl --context kw -n nexora exec deploy/nexora-mgmt -c mgmt -- /nexora-mgmt version` shows the
      new tag. Record the tag, the probe summary and the acceptance result in
      `.procoder/notes/plan-review.md`.
- [ ] Report the paths and results. The lead closes #37 and #41 with the commit and the test names
      `TestKwOperator`, `TestInstallationCreatesChartObjects`, `TestEngineGroupRotatesJoinToken`,
      `TestHelmCNPGBackups` and `TestHelmCNPGRecovery`.
