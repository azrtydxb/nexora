// Package enginegroup reconciles NexoraEngineGroup objects into management-plane engine groups and the
// join token Secret engines enrol with.
package enginegroup

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/piwi3910/nexora/operator/api/v1alpha1"
	"github.com/piwi3910/nexora/operator/internal/mgmtapi"
)

// API is the part of the management API the controller uses; *mgmtapi.Client implements it.
type API interface {
	EngineGroups(ctx context.Context) ([]mgmtapi.EngineGroup, error)
	CreateEngineGroup(ctx context.Context, in mgmtapi.EngineGroupInput) (mgmtapi.EngineGroup, error)
	UpdateEngineGroup(ctx context.Context, id uuid.UUID, in mgmtapi.EngineGroupUpdate) (mgmtapi.EngineGroup, error)
	DeleteEngineGroup(ctx context.Context, id uuid.UUID, revision int64) error
	JoinTokens(ctx context.Context) ([]mgmtapi.JoinToken, error)
	CreateJoinToken(ctx context.Context, in mgmtapi.JoinTokenCreate) (mgmtapi.JoinTokenCreated, error)
	RevokeJoinToken(ctx context.Context, id uuid.UUID) error
}

// Reconciler reconciles NexoraEngineGroup objects.
type Reconciler struct {
	Client    client.Client
	Scheme    *runtime.Scheme
	ClientFor func(ctx context.Context, inst *v1alpha1.NexoraInstallation) (API, error)
	Now       func() time.Time
}

const (
	requeueManagement   = 10 * time.Second
	requeueConflict     = time.Second
	requeueUnauthorized = 30 * time.Second
	requeueBlocked      = 30 * time.Second
	requeueMax          = time.Hour
	requeueMin          = time.Second

	defaultGroupName = "default"
	policyDelete     = "Delete"
)

// errInvalidSpec marks a CR value the controller cannot turn into an API request.
var errInvalidSpec = errors.New("invalid NexoraEngineGroup spec")

// groupName is the effective management-plane group name of eg.
func groupName(eg *v1alpha1.NexoraEngineGroup) string {
	if eg.Spec.GroupName != "" {
		return eg.Spec.GroupName
	}
	return eg.Name
}

// Reconcile follows the order of the M9 plan: finalizer, installation gate, duplicates, group, join
// token, status.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var eg v1alpha1.NexoraEngineGroup
	if err := r.Client.Get(ctx, req.NamespacedName, &eg); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !eg.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &eg)
	}
	if controllerutil.AddFinalizer(&eg, v1alpha1.FinalizerEngineGroup) {
		if err := r.Client.Update(ctx, &eg); err != nil {
			return ctrl.Result{}, err
		}
	}
	base := eg.DeepCopy()

	inst, err := r.installation(ctx, &eg)
	if err != nil {
		return ctrl.Result{}, err
	}
	if inst == nil || !apimeta.IsStatusConditionTrue(inst.Status.Conditions, v1alpha1.ConditionManagementReady) {
		return r.fail(ctx, &eg, base, v1alpha1.ReasonManagementUnavailable,
			fmt.Sprintf("installation %q has no ready management plane", eg.Spec.InstallationRef.Name), requeueManagement)
	}
	if older, err := r.olderDuplicate(ctx, &eg); err != nil {
		return ctrl.Result{}, err
	} else if older != "" {
		return r.fail(ctx, &eg, base, v1alpha1.ReasonDuplicateGroupName,
			fmt.Sprintf("NexoraEngineGroup %q already manages engine group %q", older, groupName(&eg)), requeueBlocked)
	}
	api, err := r.ClientFor(ctx, inst)
	if err != nil {
		return r.fail(ctx, &eg, base, v1alpha1.ReasonManagementUnavailable, "management API client: "+err.Error(), requeueManagement)
	}

	group, err := syncGroup(ctx, api, &eg)
	if err != nil {
		return r.apiFailure(ctx, &eg, base, err)
	}
	eg.Status.GroupID = group.Id.String()
	eg.Status.Revision = group.Revision
	eg.Status.EngineCount = int32(group.EngineCount)

	reason, msg, err := r.syncJoinToken(ctx, api, &eg, group)
	if err != nil {
		return r.apiFailure(ctx, &eg, base, err)
	}
	setCondition(&eg, v1alpha1.ConditionSynced, true, v1alpha1.ReasonReconciled, "")
	if reason != "" {
		// The group is synced; the join token is not. The Secret keeps the token it holds, so engines
		// that already have it keep enrolling while an operator reads the reason off the object.
		setCondition(&eg, v1alpha1.ConditionJoinTokenReady, false, reason, msg)
		setCondition(&eg, v1alpha1.ConditionReady, false, reason, msg)
		return ctrl.Result{RequeueAfter: requeueBlocked}, r.patchStatus(ctx, &eg, base)
	}
	setCondition(&eg, v1alpha1.ConditionJoinTokenReady, true, v1alpha1.ReasonReconciled, "")
	setCondition(&eg, v1alpha1.ConditionReady, true, v1alpha1.ReasonReconciled, "")
	return ctrl.Result{RequeueAfter: r.nextWake(&eg)}, r.patchStatus(ctx, &eg, base)
}

// nextWake is min(renewal time, previous token revocation, 1h) from now, at least 1s.
func (r *Reconciler) nextWake(eg *v1alpha1.NexoraEngineGroup) time.Duration {
	now := r.Now()
	wait := requeueMax
	if eg.Status.JoinTokenExpiresAt != nil {
		wait = min(wait, eg.Status.JoinTokenExpiresAt.Sub(now)-renewBefore(eg))
	}
	if eg.Status.PreviousJoinTokenRevokeAt != nil {
		wait = min(wait, eg.Status.PreviousJoinTokenRevokeAt.Sub(now))
	}
	return max(wait, requeueMin)
}

// finalize revokes the CR's join tokens (recorded or carrying its marker), deletes the group under deletionPolicy Delete (never
// "default", and only a group this CR synced) and removes the finalizer.
func (r *Reconciler) finalize(ctx context.Context, eg *v1alpha1.NexoraEngineGroup) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(eg, v1alpha1.FinalizerEngineGroup) {
		return ctrl.Result{}, nil
	}
	base := eg.DeepCopy()
	inst, err := r.installation(ctx, eg)
	if err != nil {
		return ctrl.Result{}, err
	}
	if inst != nil {
		api, err := r.ClientFor(ctx, inst)
		if err != nil {
			return r.fail(ctx, eg, base, v1alpha1.ReasonManagementUnavailable, "management API client: "+err.Error(), requeueManagement)
		}
		if err := revokeAll(ctx, api, eg); err != nil {
			return r.apiFailure(ctx, eg, base, err)
		}
		if eg.Spec.DeletionPolicy == policyDelete && groupName(eg) != defaultGroupName && eg.Status.GroupID != "" {
			if err := deleteGroup(ctx, api, eg.Status.GroupID); errors.Is(err, mgmtapi.ErrConflict) {
				return r.fail(ctx, eg, base, v1alpha1.ReasonDeletionBlocked, err.Error(), requeueBlocked)
			} else if err != nil {
				return r.apiFailure(ctx, eg, base, err)
			}
		}
	}
	controllerutil.RemoveFinalizer(eg, v1alpha1.FinalizerEngineGroup)
	return ctrl.Result{}, r.Client.Update(ctx, eg)
}

// deleteGroup deletes the group with id at its current revision; a group already gone is done.
func deleteGroup(ctx context.Context, api API, id string) error {
	groups, err := api.EngineGroups(ctx)
	if err != nil {
		return err
	}
	for _, g := range groups {
		if g.Id.String() == id {
			if err := api.DeleteEngineGroup(ctx, g.Id, g.Revision); err != nil && !errors.Is(err, mgmtapi.ErrNotFound) {
				return err
			}
			return nil
		}
	}
	return nil
}

// installation returns the referenced installation, or nil when it does not exist.
func (r *Reconciler) installation(ctx context.Context, eg *v1alpha1.NexoraEngineGroup) (*v1alpha1.NexoraInstallation, error) {
	var inst v1alpha1.NexoraInstallation
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: eg.Namespace, Name: eg.Spec.InstallationRef.Name}, &inst)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &inst, nil
}

// olderDuplicate names an older CR for the same installation and group name (name breaks ties), or "".
func (r *Reconciler) olderDuplicate(ctx context.Context, eg *v1alpha1.NexoraEngineGroup) (string, error) {
	var list v1alpha1.NexoraEngineGroupList
	if err := r.Client.List(ctx, &list, client.InNamespace(eg.Namespace)); err != nil {
		return "", err
	}
	for i := range list.Items {
		o := &list.Items[i]
		if o.Name == eg.Name || o.Spec.InstallationRef.Name != eg.Spec.InstallationRef.Name || groupName(o) != groupName(eg) {
			continue
		}
		if o.CreationTimestamp.Before(&eg.CreationTimestamp) ||
			(o.CreationTimestamp.Equal(&eg.CreationTimestamp) && o.Name < eg.Name) {
			return o.Name, nil
		}
	}
	return "", nil
}

// syncGroup creates the group when missing, otherwise PUTs the current group overlaid with the CR's set
// fields when that differs from the current group.
func syncGroup(ctx context.Context, api API, eg *v1alpha1.NexoraEngineGroup) (mgmtapi.EngineGroup, error) {
	groups, err := api.EngineGroups(ctx)
	if err != nil {
		return mgmtapi.EngineGroup{}, err
	}
	name := groupName(eg)
	for _, g := range groups {
		if g.Name != name {
			continue
		}
		cur := inputOf(g)
		want := inputOf(g)
		if err := overlaySpec(&want, eg.Spec); err != nil {
			return mgmtapi.EngineGroup{}, err
		}
		if reflect.DeepEqual(cur, want) {
			return g, nil
		}
		return api.UpdateEngineGroup(ctx, g.Id, updateOf(want, g.Revision))
	}
	in := mgmtapi.EngineGroupInput{Name: name}
	if err := overlaySpec(&in, eg.Spec); err != nil {
		return mgmtapi.EngineGroup{}, err
	}
	return api.CreateEngineGroup(ctx, in)
}

func ptr[T any](v T) *T { return &v }

// setInt sets the API's optional int from an optional int32 CR field when that is set.
func setInt(dst **int, src *int32) {
	if src != nil {
		*dst = ptr(int(*src))
	}
}

// inputOf is every setting of g as an input, so an update never resets a field the CR does not set.
func inputOf(g mgmtapi.EngineGroup) mgmtapi.EngineGroupInput {
	return mgmtapi.EngineGroupInput{
		Name:                g.Name,
		Description:         ptr(g.Description),
		UpstreamMode:        ptr(mgmtapi.EngineGroupInputUpstreamMode(g.UpstreamMode)),
		ExtraAclCidrs:       ptr(append([]string{}, g.ExtraAclCidrs...)),
		OtlpEndpoint:        ptr(g.OtlpEndpoint),
		FilterIndexMaxBytes: ptr(g.FilterIndexMaxBytes),
		RolloutStrategy:     ptr(mgmtapi.EngineGroupInputRolloutStrategy(g.RolloutStrategy)),
		CanaryCount:         ptr(g.CanaryCount),
		CanaryPercent:       ptr(g.CanaryPercent),
		AckTimeoutSeconds:   ptr(g.AckTimeoutSeconds),
		HealthWindowSeconds: ptr(g.HealthWindowSeconds),
		MaxServfailRatio:    ptr(g.MaxServfailRatio),
		MinHealthQueries:    ptr(g.MinHealthQueries),
	}
}

// overlaySpec sets in's fields from the fields set in spec.
func overlaySpec(in *mgmtapi.EngineGroupInput, spec v1alpha1.NexoraEngineGroupSpec) error {
	if spec.Description != nil {
		in.Description = ptr(*spec.Description)
	}
	if spec.UpstreamMode != nil {
		in.UpstreamMode = ptr(mgmtapi.EngineGroupInputUpstreamMode(*spec.UpstreamMode))
	}
	if spec.ExtraACLCIDRs != nil {
		in.ExtraAclCidrs = ptr(append([]string{}, spec.ExtraACLCIDRs...))
	}
	if spec.OtlpEndpoint != nil {
		in.OtlpEndpoint = ptr(*spec.OtlpEndpoint)
	}
	if spec.FilterIndexMaxBytes != nil {
		in.FilterIndexMaxBytes = ptr(*spec.FilterIndexMaxBytes)
	}
	ro := spec.Rollout
	if ro.Strategy != nil {
		in.RolloutStrategy = ptr(mgmtapi.EngineGroupInputRolloutStrategy(*ro.Strategy))
	}
	setInt(&in.CanaryCount, ro.CanaryCount)
	setInt(&in.CanaryPercent, ro.CanaryPercent)
	setInt(&in.AckTimeoutSeconds, ro.AckTimeoutSeconds)
	setInt(&in.HealthWindowSeconds, ro.HealthWindowSeconds)
	setInt(&in.MinHealthQueries, ro.MinHealthQueries)
	if ro.MaxServfailRatio != nil {
		v, err := strconv.ParseFloat(*ro.MaxServfailRatio, 32)
		if err != nil {
			return fmt.Errorf("%w: rollout.maxServfailRatio %q is not a decimal number", errInvalidSpec, *ro.MaxServfailRatio)
		}
		in.MaxServfailRatio = ptr(float32(v))
	}
	return nil
}

// updateOf turns a full input into the update body at revision.
func updateOf(in mgmtapi.EngineGroupInput, revision int64) mgmtapi.EngineGroupUpdate {
	return mgmtapi.EngineGroupUpdate{
		Name:                in.Name,
		Revision:            revision,
		Description:         in.Description,
		UpstreamMode:        (*mgmtapi.EngineGroupUpdateUpstreamMode)(in.UpstreamMode),
		ExtraAclCidrs:       in.ExtraAclCidrs,
		OtlpEndpoint:        in.OtlpEndpoint,
		FilterIndexMaxBytes: in.FilterIndexMaxBytes,
		RolloutStrategy:     (*mgmtapi.EngineGroupUpdateRolloutStrategy)(in.RolloutStrategy),
		CanaryCount:         in.CanaryCount,
		CanaryPercent:       in.CanaryPercent,
		AckTimeoutSeconds:   in.AckTimeoutSeconds,
		HealthWindowSeconds: in.HealthWindowSeconds,
		MaxServfailRatio:    in.MaxServfailRatio,
		MinHealthQueries:    in.MinHealthQueries,
	}
}

func setCondition(eg *v1alpha1.NexoraEngineGroup, typ string, ok bool, reason, msg string) {
	st := metav1.ConditionFalse
	if ok {
		st = metav1.ConditionTrue
	}
	apimeta.SetStatusCondition(&eg.Status.Conditions, metav1.Condition{Type: typ, Status: st, Reason: reason,
		Message: msg, ObservedGeneration: eg.Generation})
}

// patchStatus merge-patches the status against base, so a concurrent spec edit does not fail the write.
func (r *Reconciler) patchStatus(ctx context.Context, eg, base *v1alpha1.NexoraEngineGroup) error {
	eg.Status.ObservedGeneration = eg.Generation
	return r.Client.Status().Patch(ctx, eg, client.MergeFrom(base))
}

// fail sets Synced=False and Ready=False with reason and requeues after the given delay. JoinTokenReady is
// left as it is: the Secret stays valid while the management plane is briefly unreachable.
func (r *Reconciler) fail(ctx context.Context, eg, base *v1alpha1.NexoraEngineGroup, reason, msg string, after time.Duration) (ctrl.Result, error) {
	setCondition(eg, v1alpha1.ConditionSynced, false, reason, msg)
	setCondition(eg, v1alpha1.ConditionReady, false, reason, msg)
	if err := r.patchStatus(ctx, eg, base); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: after}, nil
}

// apiFailure maps a management API error onto a reason and requeue delay. Kubernetes API errors are
// returned for controller-runtime's backoff.
func (r *Reconciler) apiFailure(ctx context.Context, eg, base *v1alpha1.NexoraEngineGroup, err error) (ctrl.Result, error) {
	var apiErr *mgmtapi.APIError
	switch {
	case errors.Is(err, mgmtapi.ErrConflict):
		return r.fail(ctx, eg, base, v1alpha1.ReasonConflict, err.Error(), requeueConflict)
	case errors.Is(err, mgmtapi.ErrUnauthorized):
		return r.fail(ctx, eg, base, v1alpha1.ReasonUnauthorized, err.Error(), requeueUnauthorized)
	case errors.Is(err, mgmtapi.ErrUnavailable):
		return r.fail(ctx, eg, base, v1alpha1.ReasonManagementUnavailable, err.Error(), requeueManagement)
	case errors.As(err, &apiErr), errors.Is(err, errInvalidSpec):
		// The API refused what the CR asks for (400/422) or the CR holds an unparsable value: the CR
		// conflicts with what the management plane accepts until someone edits it.
		return r.fail(ctx, eg, base, v1alpha1.ReasonConflict, err.Error(), requeueBlocked)
	}
	return ctrl.Result{}, err
}

// SetupWithManager watches NexoraEngineGroup, the join token Secrets it owns, and NexoraInstallation
// (whose ManagementReady gates the API calls).
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.NexoraEngineGroup{}).
		Owns(&corev1.Secret{}).
		Watches(&v1alpha1.NexoraInstallation{}, handler.EnqueueRequestsFromMapFunc(r.groupsOf)).
		Named("nexoraenginegroup").
		Complete(r)
}

// groupsOf enqueues every NexoraEngineGroup referencing the installation.
func (r *Reconciler) groupsOf(ctx context.Context, obj client.Object) []reconcile.Request {
	var list v1alpha1.NexoraEngineGroupList
	if err := r.Client.List(ctx, &list, client.InNamespace(obj.GetNamespace())); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "list NexoraEngineGroups for installation", "installation", obj.GetName())
		return nil
	}
	var out []reconcile.Request
	for _, eg := range list.Items {
		if eg.Spec.InstallationRef.Name == obj.GetName() {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&eg)})
		}
	}
	return out
}

// DefaultClientFor builds a mgmtapi.Client from inst.Status.ManagementURL and the "token" key of the
// Secret inst.Status.Secrets.OperatorToken.
func DefaultClientFor(c client.Client) func(ctx context.Context, inst *v1alpha1.NexoraInstallation) (API, error) {
	return func(ctx context.Context, inst *v1alpha1.NexoraInstallation) (API, error) {
		name := inst.Status.Secrets.OperatorToken
		if inst.Status.ManagementURL == "" || name == "" {
			return nil, errors.New("installation status has no management URL or operator token Secret")
		}
		var sec corev1.Secret
		if err := c.Get(ctx, types.NamespacedName{Namespace: inst.Namespace, Name: name}, &sec); err != nil {
			return nil, fmt.Errorf("operator token Secret %q: %w", name, err)
		}
		token := string(sec.Data["token"])
		if token == "" {
			return nil, fmt.Errorf("operator token Secret %q has no key token", name)
		}
		cl, err := mgmtapi.New(inst.Status.ManagementURL, token, nil)
		if err != nil {
			return nil, err
		}
		return cl, nil
	}
}
