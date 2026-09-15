package installation

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/piwi3910/nexora/operator/api/v1alpha1"
	"github.com/piwi3910/nexora/operator/internal/render"
)

// apply server-side applies every object in order with field manager nexora-operator and force.
func (r *Reconciler) apply(ctx context.Context, objs []*unstructured.Unstructured) error {
	for _, o := range objs {
		if err := r.Client.Apply(ctx, client.ApplyConfigurationFromUnstructured(o),
			client.FieldOwner(v1alpha1.FieldManager), client.ForceOwnership); err != nil {
			return fmt.Errorf("apply %s/%s: %w", o.GetKind(), o.GetName(), err)
		}
	}
	return nil
}

// prune deletes objects of every served managed kind labelled for inst that were not rendered, except
// retained kinds (the CNPG Cluster holding the database).
func (r *Reconciler) prune(ctx context.Context, inst *v1alpha1.NexoraInstallation, rendered []*unstructured.Unstructured, served map[string]bool) error {
	keep := map[string]bool{}
	for _, o := range rendered {
		keep[o.GroupVersionKind().String()+"|"+o.GetName()] = true
	}
	for _, gvk := range render.ManagedKinds {
		if render.Retained(gvk) || !served[gvk.GroupVersion().String()+"/"+gvk.Kind] {
			continue
		}
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk.GroupVersion().WithKind(gvk.Kind + "List"))
		if err := r.Client.List(ctx, list, client.InNamespace(inst.Namespace),
			client.MatchingLabels{v1alpha1.LabelInstallation: inst.Name}); err != nil {
			return fmt.Errorf("list %s: %w", gvk.Kind, err)
		}
		for i := range list.Items {
			o := &list.Items[i]
			if keep[gvk.String()+"|"+o.GetName()] || o.GetDeletionTimestamp() != nil {
				continue
			}
			if err := r.Client.Delete(ctx, o, client.PropagationPolicy("Background")); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("prune %s/%s: %w", gvk.Kind, o.GetName(), err)
			}
		}
	}
	return nil
}
