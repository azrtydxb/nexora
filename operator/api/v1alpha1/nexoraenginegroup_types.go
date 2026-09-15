package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LocalRef names an object in the same namespace.
type LocalRef struct {
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// RolloutSpec is the engine group's rollout settings; unset fields keep the management plane's values.
type RolloutSpec struct {
	Strategy            *string `json:"strategy,omitempty"`
	CanaryCount         *int32  `json:"canaryCount,omitempty"`
	CanaryPercent       *int32  `json:"canaryPercent,omitempty"`
	AckTimeoutSeconds   *int32  `json:"ackTimeoutSeconds,omitempty"`
	HealthWindowSeconds *int32  `json:"healthWindowSeconds,omitempty"`
	// MaxServfailRatio is a decimal string such as "0.05".
	MaxServfailRatio *string `json:"maxServfailRatio,omitempty"`
	MinHealthQueries *int32  `json:"minHealthQueries,omitempty"`
}

// JoinTokenSpec controls the join token the controller keeps in a Secret.
// +kubebuilder:validation:XValidation:rule="!has(self.ttl) || (duration(self.ttl) >= duration('1m') && duration(self.ttl) <= duration('8760h'))",message="joinToken.ttl must be between 1m and 8760h"
// +kubebuilder:validation:XValidation:rule="!has(self.ttl) || !has(self.renewBefore) || duration(self.renewBefore) < duration(self.ttl)",message="joinToken.renewBefore must be less than joinToken.ttl"
type JoinTokenSpec struct {
	// SecretName defaults to <cr name>-join-token.
	SecretName string `json:"secretName,omitempty"`
	// +kubebuilder:default="8760h"
	TTL *metav1.Duration `json:"ttl,omitempty"`
	// +kubebuilder:default="720h"
	RenewBefore *metav1.Duration `json:"renewBefore,omitempty"`
	// +kubebuilder:default="10m"
	RevokeGracePeriod *metav1.Duration `json:"revokeGracePeriod,omitempty"`
	MaxUses           *int32           `json:"maxUses,omitempty"`
	// +kubebuilder:validation:MaxProperties=32
	Labels map[string]string `json:"labels,omitempty"`
}

// NexoraEngineGroupSpec is a management-plane engine group of a NexoraInstallation. Only fields set here
// are managed; unset fields keep whatever the GUI or API set.
// +kubebuilder:validation:XValidation:rule="has(self.groupName) == has(oldSelf.groupName) && (!has(self.groupName) || self.groupName == oldSelf.groupName)",message="groupName is immutable"
// +kubebuilder:validation:XValidation:rule="self.installationRef.name == oldSelf.installationRef.name",message="installationRef is immutable"
type NexoraEngineGroupSpec struct {
	// +required
	InstallationRef LocalRef `json:"installationRef"`
	// GroupName defaults to metadata.name.
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`
	GroupName   string  `json:"groupName,omitempty"`
	Description *string `json:"description,omitempty"`
	// +kubebuilder:validation:Enum=inherit;override
	UpstreamMode        *string     `json:"upstreamMode,omitempty"`
	ExtraACLCIDRs       []string    `json:"extraACLCIDRs,omitempty"`
	OtlpEndpoint        *string     `json:"otlpEndpoint,omitempty"`
	FilterIndexMaxBytes *int64      `json:"filterIndexMaxBytes,omitempty"`
	Rollout             RolloutSpec `json:"rollout,omitempty"`
	// +kubebuilder:default={}
	JoinToken JoinTokenSpec `json:"joinToken,omitempty"`
	// +kubebuilder:validation:Enum=Retain;Delete
	// +kubebuilder:default=Retain
	DeletionPolicy string `json:"deletionPolicy,omitempty"`
}

// NexoraEngineGroupStatus is the observed state of a NexoraEngineGroup.
type NexoraEngineGroupStatus struct {
	ObservedGeneration        int64        `json:"observedGeneration,omitempty"`
	GroupID                   string       `json:"groupID,omitempty"`
	Revision                  int64        `json:"revision,omitempty"`
	EngineCount               int32        `json:"engineCount,omitempty"`
	JoinTokenSecret           string       `json:"joinTokenSecret,omitempty"`
	JoinTokenID               string       `json:"joinTokenID,omitempty"`
	JoinTokenExpiresAt        *metav1.Time `json:"joinTokenExpiresAt,omitempty"`
	PreviousJoinTokenID       string       `json:"previousJoinTokenID,omitempty"`
	PreviousJoinTokenRevokeAt *metav1.Time `json:"previousJoinTokenRevokeAt,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// NexoraEngineGroup manages an engine group and its join token through the management API.
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=nxeg,categories=nexora
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Group",type=string,JSONPath=`.spec.groupName`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Engines",type=integer,JSONPath=`.status.engineCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NexoraEngineGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NexoraEngineGroupSpec   `json:"spec,omitempty"`
	Status NexoraEngineGroupStatus `json:"status,omitempty"`
}

// NexoraEngineGroupList is a list of NexoraEngineGroup.
// +kubebuilder:object:root=true
type NexoraEngineGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NexoraEngineGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NexoraEngineGroup{}, &NexoraEngineGroupList{})
}
