package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NexoraInstallationSpec mirrors the values of deploy/helm/nexora with identical JSON names. The
// operator injects mgmt.bootstrapToken, engine.groups[].joinTokenSecret/joinTokenKey and
// metrics.*.namespace, so they are absent here.
type NexoraInstallationSpec struct {
	Image            ImageSpec                     `json:"image,omitempty"`
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
	Mgmt             MgmtSpec                      `json:"mgmt,omitempty"`
	Database         DatabaseSpec                  `json:"database,omitempty"`
	Engine           EngineSpec                    `json:"engine,omitempty"`
	OtelCollector    OtelCollectorSpec             `json:"otelCollector,omitempty"`
	Metrics          MetricsSpec                   `json:"metrics,omitempty"`
}

// InstallationSecrets names the Secrets holding the installation's key material.
type InstallationSecrets struct {
	CA            string `json:"ca,omitempty"`
	KEK           string `json:"kek,omitempty"`
	OperatorToken string `json:"operatorToken,omitempty"`
}

// WorkloadStatus is the rollout state of one rendered Deployment or DaemonSet.
type WorkloadStatus struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Desired int32  `json:"desired"`
	Ready   int32  `json:"ready"`
	Updated int32  `json:"updated"`
}

// NexoraInstallationStatus is the observed state of a NexoraInstallation.
type NexoraInstallationStatus struct {
	ObservedGeneration int64               `json:"observedGeneration,omitempty"`
	Version            string              `json:"version,omitempty"`
	ManagementURL      string              `json:"managementURL,omitempty"`
	Secrets            InstallationSecrets `json:"secrets,omitempty"`
	Workloads          []WorkloadStatus    `json:"workloads,omitempty"`
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// NexoraInstallation is a Nexora management plane, database and engines rendered from the Nexora chart.
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=nxi,categories=nexora
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.version`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NexoraInstallation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NexoraInstallationSpec   `json:"spec,omitempty"`
	Status NexoraInstallationStatus `json:"status,omitempty"`
}

// NexoraInstallationList is a list of NexoraInstallation.
// +kubebuilder:object:root=true
type NexoraInstallationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NexoraInstallation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NexoraInstallation{}, &NexoraInstallationList{})
}
