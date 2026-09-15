package installation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/piwi3910/nexora/operator/api/v1alpha1"
	"github.com/piwi3910/nexora/operator/internal/mgmtapi"
)

const (
	labelName      = "app.kubernetes.io/name"
	nameMgmt       = "nexora-mgmt"
	nameEngine     = "nexora-engine"
	labelMetrics   = "nexora.io/metrics"
	managementPort = 8080
)

// setCondition sets a condition at the CR's generation.
func setCondition(inst *v1alpha1.NexoraInstallation, typ string, status metav1.ConditionStatus, reason, msg string) {
	apimeta.SetStatusCondition(&inst.Status.Conditions, metav1.Condition{Type: typ, Status: status, Reason: reason,
		Message: msg, ObservedGeneration: inst.Generation})
}

func conditionStatus(ok bool) metav1.ConditionStatus {
	if ok {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

// managementURL is http://<mgmt Service>.<namespace>.svc:8080 of the rendered Service labelled
// app.kubernetes.io/name=nexora-mgmt and nexora.io/metrics=true, or "".
func managementURL(objs []*unstructured.Unstructured) string {
	for _, o := range objs {
		l := o.GetLabels()
		if o.GetKind() == "Service" && l[labelName] == nameMgmt && l[labelMetrics] == "true" {
			return fmt.Sprintf("http://%s.%s.svc:%d", o.GetName(), o.GetNamespace(), managementPort)
		}
	}
	return ""
}

// rendered returns the rendered objects of kind whose app.kubernetes.io/name label is name.
func rendered(objs []*unstructured.Unstructured, kind, name string) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for _, o := range objs {
		if o.GetKind() == kind && (name == "" || o.GetLabels()[labelName] == name) {
			out = append(out, o)
		}
	}
	return out
}

// workloadState is a workload's status plus whether its rollout is still in progress.
type workloadState struct {
	v1alpha1.WorkloadStatus
	rolling bool
}

// engineWorkloads reads back the rendered engine DaemonSets and Deployments.
func (r *Reconciler) engineWorkloads(ctx context.Context, objs []*unstructured.Unstructured) ([]workloadState, error) {
	var out []workloadState
	for _, o := range objs {
		if o.GetLabels()[labelName] != nameEngine {
			continue
		}
		key := types.NamespacedName{Namespace: o.GetNamespace(), Name: o.GetName()}
		switch o.GetKind() {
		case "DaemonSet":
			var ds appsv1.DaemonSet
			if err := r.Client.Get(ctx, key, &ds); err != nil {
				return nil, fmt.Errorf("read DaemonSet/%s: %w", key.Name, err)
			}
			s := ds.Status
			out = append(out, workloadState{
				WorkloadStatus: v1alpha1.WorkloadStatus{Kind: "DaemonSet", Name: ds.Name, Desired: s.DesiredNumberScheduled,
					Ready: s.NumberReady, Updated: s.UpdatedNumberScheduled},
				rolling: s.ObservedGeneration < ds.Generation || s.UpdatedNumberScheduled < s.DesiredNumberScheduled,
			})
		case "Deployment":
			var d appsv1.Deployment
			if err := r.Client.Get(ctx, key, &d); err != nil {
				return nil, fmt.Errorf("read Deployment/%s: %w", key.Name, err)
			}
			desired := int32(1)
			if d.Spec.Replicas != nil {
				desired = *d.Spec.Replicas
			}
			s := d.Status
			out = append(out, workloadState{
				WorkloadStatus: v1alpha1.WorkloadStatus{Kind: "Deployment", Name: d.Name, Desired: desired,
					Ready: s.ReadyReplicas, Updated: s.UpdatedReplicas},
				rolling: s.ObservedGeneration < d.Generation || s.UpdatedReplicas < desired,
			})
		}
	}
	return out, nil
}

// enginesCondition derives EnginesReady from the pending groups and the workloads.
func enginesCondition(pending []string, workloads []workloadState) (bool, string, string) {
	if len(pending) > 0 {
		return false, v1alpha1.ReasonJoinTokenPending,
			"engine groups waiting for a ready NexoraEngineGroup join token: " + strings.Join(pending, ", ")
	}
	for _, w := range workloads {
		if w.rolling {
			return false, v1alpha1.ReasonRollingUpdate, fmt.Sprintf("%s/%s: %d of %d updated", w.Kind, w.Name, w.Updated, w.Desired)
		}
	}
	for _, w := range workloads {
		if w.Desired == 0 || w.Ready < w.Desired {
			return false, v1alpha1.ReasonUnavailable, fmt.Sprintf("%s/%s: %d of %d ready", w.Kind, w.Name, w.Ready, w.Desired)
		}
	}
	return true, v1alpha1.ReasonReconciled, ""
}

// databaseReady is the rendered CNPG Cluster's Ready condition, or, without one, whether the external
// database Secret exists.
func (r *Reconciler) databaseReady(ctx context.Context, inst *v1alpha1.NexoraInstallation, objs []*unstructured.Unstructured) (bool, string, error) {
	for _, o := range rendered(objs, "Cluster", "") {
		if o.GroupVersionKind().Group != "postgresql.cnpg.io" {
			continue
		}
		live := &unstructured.Unstructured{}
		live.SetGroupVersionKind(o.GroupVersionKind())
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: o.GetNamespace(), Name: o.GetName()}, live); err != nil {
			return false, "", fmt.Errorf("read Cluster/%s: %w", o.GetName(), err)
		}
		conds, _, _ := unstructured.NestedSlice(live.Object, "status", "conditions")
		for _, c := range conds {
			if m, ok := c.(map[string]any); ok && m["type"] == "Ready" && m["status"] == "True" {
				return true, "", nil
			}
		}
		return false, fmt.Sprintf("CNPG Cluster %s is not Ready", o.GetName()), nil
	}
	name := inst.Spec.Database.External.ExistingSecret
	if name == "" {
		return false, "no database rendered and no external database Secret", nil
	}
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: inst.Namespace, Name: name}, &corev1.Secret{})
	if apierrors.IsNotFound(err) {
		return false, fmt.Sprintf("external database Secret %s does not exist", name), nil
	}
	return err == nil, "", err
}

// management checks the rendered mgmt Deployment's availability and, when available, the health
// endpoint with the operator token. It returns ManagementReady's status, reason and message, and the
// health result.
func (r *Reconciler) management(ctx context.Context, inst *v1alpha1.NexoraInstallation, objs []*unstructured.Unstructured) (bool, string, string, HealthResult, error) {
	deps := rendered(objs, "Deployment", nameMgmt)
	if len(deps) == 0 || inst.Status.ManagementURL == "" {
		return false, v1alpha1.ReasonManagementUnavailable, "no management plane rendered", HealthResult{}, nil
	}
	var d appsv1.Deployment
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: inst.Namespace, Name: deps[0].GetName()}, &d); err != nil {
		return false, "", "", HealthResult{}, fmt.Errorf("read Deployment/%s: %w", deps[0].GetName(), err)
	}
	if d.Status.AvailableReplicas < 1 {
		return false, v1alpha1.ReasonManagementUnavailable, fmt.Sprintf("Deployment/%s has no available replica", d.Name), HealthResult{}, nil
	}
	var tok corev1.Secret
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: inst.Namespace, Name: inst.Status.Secrets.OperatorToken}, &tok); err != nil {
		return false, "", "", HealthResult{}, fmt.Errorf("read operator token Secret: %w", err)
	}
	res, err := r.Health.Check(ctx, inst.Status.ManagementURL, string(tok.Data["token"]))
	if err != nil {
		reason := v1alpha1.ReasonManagementUnavailable
		if errors.Is(err, mgmtapi.ErrUnauthorized) {
			reason = v1alpha1.ReasonUnauthorized
		}
		return false, reason, truncate(err.Error()), HealthResult{}, nil
	}
	if !res.Healthy {
		return false, v1alpha1.ReasonManagementUnavailable, "management plane reports unhealthy", res, nil
	}
	return true, v1alpha1.ReasonReconciled, "", res, nil
}

// maxMessage bounds condition messages taken from errors.
const maxMessage = 1024

func truncate(s string) string {
	if len(s) > maxMessage {
		return strings.ToValidUTF8(s[:maxMessage], "")
	}
	return s
}
