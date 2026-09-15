// Package installation reconciles NexoraInstallation objects: key material Secrets, the rendered Nexora
// chart (applied and pruned) and the installation status.
package installation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/piwi3910/nexora/operator/api/v1alpha1"
	"github.com/piwi3910/nexora/operator/internal/keys"
	"github.com/piwi3910/nexora/operator/internal/render"
)

// Reconciler reconciles NexoraInstallation objects.
type Reconciler struct {
	Client          client.Client
	Scheme          *runtime.Scheme
	Chart           *render.Chart
	Discovery       discovery.DiscoveryInterface
	Health          Health
	OperatorVersion string
	ResyncInterval  time.Duration
}

// errBlocked marks a failure that sets Rendered=False and waits for a spec or Secret change (or the
// resync) instead of controller-runtime's backoff; with retry the error is also returned for backoff.
type errBlocked struct {
	reason, msg string
	retry       bool
}

func (e *errBlocked) Error() string { return e.reason + ": " + e.msg }

func blocked(reason, msg string) error { return &errBlocked{reason: reason, msg: truncate(msg)} }

var installationGVK = v1alpha1.GroupVersion.WithKind("NexoraInstallation")

// Reconcile follows the M9 plan's order: keys, join tokens, values, discovery, render, apply, prune,
// status. The operator never deletes the key Secrets or the CNPG Cluster; deleting the CR removes
// only the owned objects (garbage collection).
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var inst v1alpha1.NexoraInstallation
	if err := r.Client.Get(ctx, req.NamespacedName, &inst); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !inst.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	base := inst.DeepCopy()
	err := r.reconcile(ctx, &inst)
	var b *errBlocked
	switch {
	case errors.As(err, &b):
		setCondition(&inst, v1alpha1.ConditionRendered, metav1.ConditionFalse, b.reason, b.msg)
		setCondition(&inst, v1alpha1.ConditionReady, metav1.ConditionFalse, b.reason, b.msg)
	case err != nil:
		// Kubernetes API failures: record them and let controller-runtime back off.
		setCondition(&inst, v1alpha1.ConditionReady, metav1.ConditionFalse, v1alpha1.ReasonRenderFailed, truncate(err.Error()))
	}
	inst.Status.ObservedGeneration = inst.Generation
	if perr := r.Client.Status().Patch(ctx, &inst, client.MergeFrom(base)); perr != nil {
		return ctrl.Result{}, errors.Join(err, perr)
	}
	if err != nil && (b == nil || b.retry) {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
}

// reconcile does the work on inst and fills its status; the caller writes the status.
func (r *Reconciler) reconcile(ctx context.Context, inst *v1alpha1.NexoraInstallation) error {
	secrets, err := r.ensureKeys(ctx, inst)
	if err != nil {
		return err
	}
	inst.Status.Secrets = secrets

	joinTokens, err := r.joinTokenSecrets(ctx, inst)
	if err != nil {
		return err
	}
	values, err := render.BuildValues(inst.Spec, render.Injected{Tag: r.OperatorVersion, CASecret: secrets.CA,
		KEKSecret: secrets.KEK, BootstrapTokenSecret: secrets.OperatorToken, JoinTokenSecrets: joinTokens})
	if errors.Is(err, render.ErrImageTagRequired) {
		return blocked(v1alpha1.ReasonImageTagRequired, err.Error())
	} else if err != nil {
		return blocked(v1alpha1.ReasonRenderFailed, err.Error())
	}

	target, served, err := r.discover(inst)
	if err != nil {
		return err
	}
	objs, err := r.Chart.Render(target, values.Map)
	if err != nil {
		return blocked(v1alpha1.ReasonRenderFailed, err.Error())
	}
	if err := render.Own(objs, inst, installationGVK); errors.Is(err, render.ErrForeignNamespace) {
		return blocked(v1alpha1.ReasonForeignNamespace, err.Error())
	} else if err != nil {
		return blocked(v1alpha1.ReasonRenderFailed, err.Error())
	}
	if err := r.apply(ctx, objs); err != nil {
		// Stop without pruning. A refused object (invalid) waits for a spec change; anything else
		// also backs off and retries.
		return &errBlocked{reason: v1alpha1.ReasonRenderFailed, msg: truncate(err.Error()),
			retry: !apierrors.IsInvalid(err) && !apierrors.IsBadRequest(err)}
	}
	if err := r.prune(ctx, inst, objs, served); err != nil {
		return err
	}
	setCondition(inst, v1alpha1.ConditionRendered, metav1.ConditionTrue, v1alpha1.ReasonReconciled, "")
	return r.updateStatus(ctx, inst, objs, values)
}

// ensureKeys verifies the Secrets named in the spec and creates the operator's own ones when missing.
func (r *Reconciler) ensureKeys(ctx context.Context, inst *v1alpha1.NexoraInstallation) (v1alpha1.InstallationSecrets, error) {
	labels := map[string]string{v1alpha1.LabelInstallation: inst.Name}
	secret := func(existing, suffix string, required []string, gen func() (map[string][]byte, error)) (string, error) {
		var err error
		name := existing
		if existing != "" {
			err = keys.CheckSecret(ctx, r.Client, types.NamespacedName{Namespace: inst.Namespace, Name: name}, required)
			if apierrors.IsNotFound(err) {
				return "", blocked(v1alpha1.ReasonSecretIncomplete, fmt.Sprintf("secret %s does not exist", name))
			}
		} else {
			name = inst.Name + suffix
			err = keys.EnsureSecret(ctx, r.Client, types.NamespacedName{Namespace: inst.Namespace, Name: name}, labels, required, gen)
		}
		if errors.Is(err, keys.ErrSecretIncomplete) {
			return "", blocked(v1alpha1.ReasonSecretIncomplete, err.Error())
		}
		return name, err
	}
	var out v1alpha1.InstallationSecrets
	var err error
	if out.CA, err = secret(inst.Spec.Mgmt.CA.ExistingSecret, "-ca", []string{"ca.crt", "ca.key"}, func() (map[string][]byte, error) {
		cert, key, err := keys.GenerateCA(time.Now())
		return map[string][]byte{"ca.crt": cert, "ca.key": key}, err
	}); err != nil {
		return out, err
	}
	if out.KEK, err = secret(inst.Spec.Mgmt.KEK.ExistingSecret, "-kek", []string{"kek"}, func() (map[string][]byte, error) {
		kek, err := keys.GenerateKEK()
		return map[string][]byte{"kek": kek}, err
	}); err != nil {
		return out, err
	}
	if out.OperatorToken, err = secret("", "-operator-token", []string{"token"}, func() (map[string][]byte, error) {
		tok, err := keys.GenerateBootstrapToken()
		return map[string][]byte{"token": []byte(tok)}, err
	}); err != nil {
		return out, err
	}
	return out, nil
}

// joinTokenSecrets maps each NexoraEngineGroup of inst with JoinTokenReady=True to its join token Secret.
func (r *Reconciler) joinTokenSecrets(ctx context.Context, inst *v1alpha1.NexoraInstallation) (map[string]string, error) {
	var list v1alpha1.NexoraEngineGroupList
	if err := r.Client.List(ctx, &list, client.InNamespace(inst.Namespace)); err != nil {
		return nil, fmt.Errorf("list NexoraEngineGroups: %w", err)
	}
	out := map[string]string{}
	for _, eg := range list.Items {
		if eg.Spec.InstallationRef.Name == inst.Name && eg.Status.JoinTokenSecret != "" &&
			apimeta.IsStatusConditionTrue(eg.Status.Conditions, v1alpha1.ConditionJoinTokenReady) {
			out[eg.Name] = eg.Status.JoinTokenSecret
		}
	}
	return out, nil
}

// discover returns the render target and the set of served "group/version/Kind" strings.
func (r *Reconciler) discover(inst *v1alpha1.NexoraInstallation) (render.Target, map[string]bool, error) {
	groups, resources, err := r.Discovery.ServerGroupsAndResources()
	if err != nil && !discovery.IsGroupDiscoveryFailedError(err) {
		return render.Target{}, nil, fmt.Errorf("discovery: %w", err)
	}
	// A failed group (an aggregated API that is down) is left out: its kinds are neither rendered
	// against nor pruned.
	t := render.Target{Name: inst.Name, Namespace: inst.Namespace}
	served := map[string]bool{}
	for _, g := range groups {
		for _, v := range g.Versions {
			t.APIVersions = append(t.APIVersions, v.GroupVersion)
		}
	}
	for _, list := range resources {
		for _, res := range list.APIResources {
			if strings.Contains(res.Name, "/") {
				continue
			}
			gvk := list.GroupVersion + "/" + res.Kind
			if !served[gvk] {
				served[gvk] = true
				t.APIVersions = append(t.APIVersions, gvk)
			}
		}
	}
	v, err := r.Discovery.ServerVersion()
	if err != nil {
		return render.Target{}, nil, fmt.Errorf("server version: %w", err)
	}
	t.KubeVersion = v.GitVersion
	return t, served, nil
}

// updateStatus fills version, management URL, workloads and the conditions after a successful apply.
func (r *Reconciler) updateStatus(ctx context.Context, inst *v1alpha1.NexoraInstallation, objs []*unstructured.Unstructured, values render.Values) error {
	if img, ok := values.Map["image"].(map[string]any); ok {
		inst.Status.Version, _ = img["tag"].(string)
	}
	inst.Status.ManagementURL = managementURL(objs)

	workloads, err := r.engineWorkloads(ctx, objs)
	if err != nil {
		return err
	}
	inst.Status.Workloads = nil
	for _, w := range workloads {
		inst.Status.Workloads = append(inst.Status.Workloads, w.WorkloadStatus)
	}

	dbOK, dbMsg, err := r.databaseReady(ctx, inst, objs)
	if err != nil {
		return err
	}
	dbReason := v1alpha1.ReasonReconciled
	if !dbOK {
		dbReason = v1alpha1.ReasonUnavailable
	}
	setCondition(inst, v1alpha1.ConditionDatabaseReady, conditionStatus(dbOK), dbReason, dbMsg)

	mgmtOK, mgmtReason, mgmtMsg, health, err := r.management(ctx, inst, objs)
	if err != nil {
		return err
	}
	setCondition(inst, v1alpha1.ConditionManagementReady, conditionStatus(mgmtOK), mgmtReason, mgmtMsg)
	if mgmtOK {
		setCondition(inst, v1alpha1.ConditionSetupRequired, conditionStatus(health.SetupRequired), v1alpha1.ReasonReconciled, "")
	} else {
		setCondition(inst, v1alpha1.ConditionSetupRequired, metav1.ConditionUnknown, mgmtReason, "management plane not ready")
	}

	engOK, engReason, engMsg := enginesCondition(values.PendingGroups, workloads)
	setCondition(inst, v1alpha1.ConditionEnginesReady, conditionStatus(engOK), engReason, engMsg)

	for _, typ := range []string{v1alpha1.ConditionRendered, v1alpha1.ConditionDatabaseReady, v1alpha1.ConditionManagementReady, v1alpha1.ConditionEnginesReady} {
		if c := apimeta.FindStatusCondition(inst.Status.Conditions, typ); c.Status != metav1.ConditionTrue {
			setCondition(inst, v1alpha1.ConditionReady, metav1.ConditionFalse, c.Reason, typ+" is not true")
			return nil
		}
	}
	setCondition(inst, v1alpha1.ConditionReady, metav1.ConditionTrue, v1alpha1.ReasonReconciled, "")
	return nil
}

// SetupWithManager watches NexoraInstallation (spec changes), the workloads, Services and ConfigMaps it
// owns, and NexoraEngineGroup (whose join token readiness gates engine rendering).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.NexoraInstallation{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.DaemonSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Watches(&v1alpha1.NexoraEngineGroup{}, handler.EnqueueRequestsFromMapFunc(installationOf)).
		Named("nexorainstallation").
		Complete(r)
}

// installationOf enqueues the installation a NexoraEngineGroup references.
func installationOf(_ context.Context, obj client.Object) []reconcile.Request {
	eg, ok := obj.(*v1alpha1.NexoraEngineGroup)
	if !ok {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: eg.Namespace, Name: eg.Spec.InstallationRef.Name}}}
}
