// Package v1alpha1 holds the nexora.io/v1alpha1 API: NexoraInstallation and NexoraEngineGroup.
// +kubebuilder:object:generate=true
// +groupName=nexora.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the API group and version of the Nexora CRDs.
	GroupVersion = schema.GroupVersion{Group: "nexora.io", Version: "v1alpha1"}

	// SchemeBuilder registers the Nexora types with a scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the Nexora types to a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
