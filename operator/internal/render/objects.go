package render

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// InstallationLabel marks every top-level object an installation renders.
const InstallationLabel = "nexora.io/installation"

// ManagedKinds are the kinds the chart renders; pruning lists only these.
var ManagedKinds = []schema.GroupVersionKind{
	{Group: "apps", Version: "v1", Kind: "Deployment"},
	{Group: "apps", Version: "v1", Kind: "DaemonSet"},
	{Group: "", Version: "v1", Kind: "Service"},
	{Group: "", Version: "v1", Kind: "ConfigMap"},
	{Group: "policy", Version: "v1", Kind: "PodDisruptionBudget"},
	{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress"},
	{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"},
	{Group: "postgresql.cnpg.io", Version: "v1", Kind: "ScheduledBackup"},
	{Group: "monitoring.coreos.com", Version: "v1", Kind: "ServiceMonitor"},
	{Group: "monitoring.coreos.com", Version: "v1", Kind: "PrometheusRule"},
}

// Retained reports whether objects of gvk outlive the installation (no owner reference, never pruned):
// true only for postgresql.cnpg.io/v1 Cluster, which holds the database.
func Retained(gvk schema.GroupVersionKind) bool {
	return gvk == schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}
}

// Own sets metadata.labels["nexora.io/installation"]=owner.GetName() and, except for retained kinds, a
// controller owner reference (BlockOwnerDeletion true). Any object whose namespace differs from
// owner's returns ErrForeignNamespace wrapped with "<Kind>/<name> in <namespace>".
func Own(objs []*unstructured.Unstructured, owner metav1.Object, ownerGVK schema.GroupVersionKind) error {
	for _, o := range objs {
		if o.GetNamespace() != owner.GetNamespace() {
			return fmt.Errorf("%w: %s/%s in %s", ErrForeignNamespace, o.GetKind(), o.GetName(), o.GetNamespace())
		}
	}
	yes := true
	ref := metav1.OwnerReference{
		APIVersion:         ownerGVK.GroupVersion().String(),
		Kind:               ownerGVK.Kind,
		Name:               owner.GetName(),
		UID:                owner.GetUID(),
		Controller:         &yes,
		BlockOwnerDeletion: &yes,
	}
	for _, o := range objs {
		labels := o.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		labels[InstallationLabel] = owner.GetName()
		o.SetLabels(labels)
		if !Retained(o.GroupVersionKind()) {
			o.SetOwnerReferences([]metav1.OwnerReference{ref})
		}
	}
	return nil
}
