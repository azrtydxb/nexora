package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
)

// The types in this file mirror deploy/helm/nexora/values.yaml: JSON names equal the values keys,
// booleans and integers are pointers and strings omitempty, so an unset field is absent from the
// values and the chart default applies. No values field carries a CRD default.

// ImageSpec is values `image`.
type ImageSpec struct {
	Registry   string            `json:"registry,omitempty"`
	Tag        string            `json:"tag,omitempty"`
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}

// SecretRef names an existing Secret.
type SecretRef struct {
	ExistingSecret string `json:"existingSecret,omitempty"`
}

// DNSTLSSpec is values `mgmt.dnsTLS`.
type DNSTLSSpec struct {
	ExistingSecret string `json:"existingSecret,omitempty"`
	ReloadInterval string `json:"reloadInterval,omitempty"`
}

// OpensearchSpec is values `mgmt.querylog.opensearch`.
type OpensearchSpec struct {
	URL   string `json:"url,omitempty"`
	Index string `json:"index,omitempty"`
}

// QuerylogSpec is values `mgmt.querylog`.
// +kubebuilder:validation:XValidation:rule="!has(self.backend) || self.backend != 'opensearch' || (has(self.opensearch.url) && size(self.opensearch.url) > 0)",message="mgmt.querylog.opensearch.url is required when mgmt.querylog.backend=opensearch"
type QuerylogSpec struct {
	// +kubebuilder:validation:Enum=builtin;opensearch
	Backend         string `json:"backend,omitempty"`
	BuiltinCapacity *int32 `json:"builtinCapacity,omitempty"`
	// +kubebuilder:default={}
	Opensearch OpensearchSpec `json:"opensearch,omitempty"`
}

// LoadBalancerSpec is values `mgmt.grpcLoadBalancer`.
type LoadBalancerSpec struct {
	Enabled        *bool  `json:"enabled,omitempty"`
	LoadBalancerIP string `json:"loadBalancerIP,omitempty"`
}

// IngressSpec is values `mgmt.ingress`.
type IngressSpec struct {
	Enabled       *bool  `json:"enabled,omitempty"`
	Name          string `json:"name,omitempty"`
	ClassName     string `json:"className,omitempty"`
	Host          string `json:"host,omitempty"`
	ClusterIssuer string `json:"clusterIssuer,omitempty"`
	TLSSecretName string `json:"tlsSecretName,omitempty"`
}

// PDBSpec is values `mgmt.pdb`.
type PDBSpec struct {
	MinAvailable *int32 `json:"minAvailable,omitempty"`
}

// MCPSpec is values `mgmt.mcp`. Unset fields use chart defaults: disabled and read-only.
type MCPSpec struct {
	Enabled  *bool `json:"enabled,omitempty"`
	ReadOnly *bool `json:"readOnly,omitempty"`
}

// MgmtSpec is values `mgmt` without `bootstrapToken`, which the operator injects.
type MgmtSpec struct {
	Replicas          *int32                       `json:"replicas,omitempty"`
	PublicURL         string                       `json:"publicURL,omitempty"`
	SecureCookies     *bool                        `json:"secureCookies,omitempty"`
	EngineCertTTL     string                       `json:"engineCertTTL,omitempty"`
	RolloutTick       string                       `json:"rolloutTick,omitempty"`
	GrpcServerNames   []string                     `json:"grpcServerNames,omitempty"`
	OtlpEndpoint      string                       `json:"otlpEndpoint,omitempty"`
	CA                SecretRef                    `json:"ca,omitempty"`
	KEK               SecretRef                    `json:"kek,omitempty"`
	AI                SecretRef                    `json:"ai,omitempty"`
	MCP               MCPSpec                      `json:"mcp,omitempty"`
	DNSTLS            DNSTLSSpec                   `json:"dnsTLS,omitempty"`
	Querylog          QuerylogSpec                 `json:"querylog,omitempty"`
	ExtraEnv          []corev1.EnvVar              `json:"extraEnv,omitempty"`
	ExtraVolumes      []corev1.Volume              `json:"extraVolumes,omitempty"`
	ExtraVolumeMounts []corev1.VolumeMount         `json:"extraVolumeMounts,omitempty"`
	Resources         *corev1.ResourceRequirements `json:"resources,omitempty"`
	GrpcLoadBalancer  LoadBalancerSpec             `json:"grpcLoadBalancer,omitempty"`
	Ingress           IngressSpec                  `json:"ingress,omitempty"`
	PDB               PDBSpec                      `json:"pdb,omitempty"`
}

// ExternalDatabaseSpec is values `database.external`.
type ExternalDatabaseSpec struct {
	ExistingSecret string `json:"existingSecret,omitempty"`
	Key            string `json:"key,omitempty"`
}

// DatabaseSpec is values `database`.
// +kubebuilder:validation:XValidation:rule="!has(self.mode) || self.mode != 'external' || (has(self.external.existingSecret) && size(self.external.existingSecret) > 0)",message="database.external.existingSecret is required when database.mode=external"
type DatabaseSpec struct {
	// +kubebuilder:validation:Enum=cnpg;external
	Mode string `json:"mode,omitempty"`
	// +kubebuilder:default={}
	External ExternalDatabaseSpec `json:"external,omitempty"`
	CNPG     CNPGSpec             `json:"cnpg,omitempty"`
}

// PostgreSQLSpec is values `database.cnpg.postgresql`.
type PostgreSQLSpec struct {
	Parameters map[string]string `json:"parameters,omitempty"`
}

// CNPGSpec is values `database.cnpg`.
type CNPGSpec struct {
	ClusterName  string                       `json:"clusterName,omitempty"`
	Instances    *int32                       `json:"instances,omitempty"`
	ImageName    string                       `json:"imageName,omitempty"`
	StorageClass string                       `json:"storageClass,omitempty"`
	Size         string                       `json:"size,omitempty"`
	Resources    *corev1.ResourceRequirements `json:"resources,omitempty"`
	// +kubebuilder:validation:Enum=preferred;required
	AntiAffinity string `json:"antiAffinity,omitempty"`
	// +kubebuilder:validation:Enum=switchover;restart
	PrimaryUpdateMethod string           `json:"primaryUpdateMethod,omitempty"`
	PostgreSQL          PostgreSQLSpec   `json:"postgresql,omitempty"`
	Backup              CNPGBackupSpec   `json:"backup,omitempty"`
	Recovery            CNPGRecoverySpec `json:"recovery,omitempty"`
}

// S3Credentials names the Secret and keys holding S3 credentials.
type S3Credentials struct {
	ExistingSecret     string `json:"existingSecret,omitempty"`
	AccessKeyIDKey     string `json:"accessKeyIdKey,omitempty"`
	SecretAccessKeyKey string `json:"secretAccessKeyKey,omitempty"`
}

// SecretKeyRef names a key of an existing Secret.
type SecretKeyRef struct {
	ExistingSecret string `json:"existingSecret,omitempty"`
	Key            string `json:"key,omitempty"`
}

// CNPGBackupSpec is values `database.cnpg.backup`.
// +kubebuilder:validation:XValidation:rule="!has(self.enabled) || !self.enabled || (has(self.destinationPath) && size(self.destinationPath) > 0 && has(self.s3Credentials.existingSecret))",message="database.cnpg.backup needs destinationPath and s3Credentials.existingSecret"
type CNPGBackupSpec struct {
	Enabled         *bool  `json:"enabled,omitempty"`
	DestinationPath string `json:"destinationPath,omitempty"`
	EndpointURL     string `json:"endpointURL,omitempty"`
	ServerName      string `json:"serverName,omitempty"`
	// +kubebuilder:default={}
	S3Credentials   S3Credentials `json:"s3Credentials,omitempty"`
	EndpointCA      SecretKeyRef  `json:"endpointCA,omitempty"`
	RetentionPolicy string        `json:"retentionPolicy,omitempty"`
	WalCompression  string        `json:"walCompression,omitempty"`
	DataCompression string        `json:"dataCompression,omitempty"`
	Schedule        string        `json:"schedule,omitempty"`
	Immediate       *bool         `json:"immediate,omitempty"`
}

// CNPGRecoverySpec is values `database.cnpg.recovery`.
type CNPGRecoverySpec struct {
	Enabled          *bool         `json:"enabled,omitempty"`
	SourceServerName string        `json:"sourceServerName,omitempty"`
	DestinationPath  string        `json:"destinationPath,omitempty"`
	EndpointURL      string        `json:"endpointURL,omitempty"`
	S3Credentials    S3Credentials `json:"s3Credentials,omitempty"`
	EndpointCA       SecretKeyRef  `json:"endpointCA,omitempty"`
	TargetTime       string        `json:"targetTime,omitempty"`
}

// EnginePorts is values `engine.ports`; 0 disables an encrypted listener.
type EnginePorts struct {
	DNS     *int32 `json:"dns,omitempty"`
	Metrics *int32 `json:"metrics,omitempty"`
	DoT     *int32 `json:"dot,omitempty"`
	DoH     *int32 `json:"doh,omitempty"`
	DoQ     *int32 `json:"doq,omitempty"`
}

// StateDirSpec is values `engine.stateDir`.
type StateDirSpec struct {
	// +kubebuilder:validation:Enum=hostPath;emptyDir
	Type           string `json:"type,omitempty"`
	HostPathPrefix string `json:"hostPathPrefix,omitempty"`
}

// EngineSpec is values `engine`.
type EngineSpec struct {
	Enabled *bool `json:"enabled,omitempty"`
	// +kubebuilder:validation:Enum=DaemonSet;Deployment
	Kind                 string                       `json:"kind,omitempty"`
	ManagementURL        string                       `json:"managementURL,omitempty"`
	Workers              *int32                       `json:"workers,omitempty"`
	InitImage            string                       `json:"initImage,omitempty"`
	HostNetwork          *bool                        `json:"hostNetwork,omitempty"`
	Ports                EnginePorts                  `json:"ports,omitempty"`
	DohPath              string                       `json:"dohPath,omitempty"`
	StateDir             StateDirSpec                 `json:"stateDir,omitempty"`
	Tolerations          []corev1.Toleration          `json:"tolerations,omitempty"`
	ShutdownDrainSeconds *int32                       `json:"shutdownDrainSeconds,omitempty"`
	MinReadySeconds      *int32                       `json:"minReadySeconds,omitempty"`
	Resources            *corev1.ResourceRequirements `json:"resources,omitempty"`
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	Groups []EngineGroupSpec `json:"groups,omitempty"`
}

// EngineGroupSpec is one entry of values `engine.groups`, without joinTokenSecret/joinTokenKey.
type EngineGroupSpec struct {
	// +required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`
	Name string `json:"name"`
	// EngineGroupRef names the NexoraEngineGroup whose join token Secret the group's engines use
	// (default: name).
	EngineGroupRef string               `json:"engineGroupRef,omitempty"`
	WorkloadName   string               `json:"workloadName,omitempty"`
	NodeNamePrefix *string              `json:"nodeNamePrefix,omitempty"`
	Replicas       *int32               `json:"replicas,omitempty"`
	NodeAffinity   *corev1.NodeAffinity `json:"nodeAffinity,omitempty"`
	Service        *ServiceSpec         `json:"service,omitempty"`
	ExtraServices  []ServiceSpec        `json:"extraServices,omitempty"`
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	Instances []EngineInstanceSpec `json:"instances,omitempty"`
}

// ServiceSpec is a DNS Service of an engine group or instance.
type ServiceSpec struct {
	Name                  string `json:"name,omitempty"`
	Type                  string `json:"type,omitempty"`
	LoadBalancerIP        string `json:"loadBalancerIP,omitempty"`
	ExternalTrafficPolicy string `json:"externalTrafficPolicy,omitempty"`
}

// EngineInstanceSpec is one engine pinned to one node.
type EngineInstanceSpec struct {
	// +required
	Name string `json:"name"`
	// +required
	// +kubebuilder:validation:MinLength=1
	Node    string       `json:"node"`
	Service *ServiceSpec `json:"service,omitempty"`
}

// OtelCollectorSpec is values `otelCollector`.
type OtelCollectorSpec struct {
	Enabled   *bool                        `json:"enabled,omitempty"`
	Image     string                       `json:"image,omitempty"`
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
	Config    string                       `json:"config,omitempty"`
}

// ServiceMonitorSpec is values `metrics.serviceMonitor` without `namespace`.
type ServiceMonitorSpec struct {
	Enabled  *bool             `json:"enabled,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Interval string            `json:"interval,omitempty"`
}

// PrometheusRuleSpec is values `metrics.prometheusRule` without `namespace`.
type PrometheusRuleSpec struct {
	Enabled *bool             `json:"enabled,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
}

// MetricsSpec is values `metrics`.
type MetricsSpec struct {
	ServiceMonitor ServiceMonitorSpec `json:"serviceMonitor,omitempty"`
	PrometheusRule PrometheusRuleSpec `json:"prometheusRule,omitempty"`
}
