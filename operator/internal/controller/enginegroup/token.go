package enginegroup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/piwi3910/nexora/operator/api/v1alpha1"
	"github.com/piwi3910/nexora/operator/internal/mgmtapi"
)

const (
	// joinTokenKey is the Secret key holding the join token.
	joinTokenKey = "join-token"
	// maxTokenName is the API's join token name limit.
	maxTokenName = 64

	// The CRD defaults, applied again for objects created before the defaults existed.
	defaultTTL               = 8760 * time.Hour
	defaultRenewBefore       = 720 * time.Hour
	defaultRevokeGracePeriod = 10 * time.Minute

	tokenActive = "active"
)

func durationOr(d *metav1.Duration, def time.Duration) time.Duration {
	if d == nil {
		return def
	}
	return d.Duration
}

func renewBefore(eg *v1alpha1.NexoraEngineGroup) time.Duration {
	return durationOr(eg.Spec.JoinToken.RenewBefore, defaultRenewBefore)
}

// joinTokenSecretName is spec.joinToken.secretName, default <cr name>-join-token.
func joinTokenSecretName(eg *v1alpha1.NexoraEngineGroup) string {
	if eg.Spec.JoinToken.SecretName != "" {
		return eg.Spec.JoinToken.SecretName
	}
	return eg.Name + "-join-token"
}

// tokenPrefix marks the join tokens created for eg: op/<cr uid>/. The uid keeps the marker unique
// across CRs and across a deleted and recreated CR of the same name.
func tokenPrefix(eg *v1alpha1.NexoraEngineGroup) string {
	return "op/" + string(eg.UID) + "/"
}

// tokenName is op/<cr uid>/<unix seconds>/<namespace>/<cr name>, cut to the API's 64 characters (the
// 51-character marker and timestamp always survive the cut).
func tokenName(eg *v1alpha1.NexoraEngineGroup, now time.Time) string {
	name := fmt.Sprintf("%s%d/%s/%s", tokenPrefix(eg), now.Unix(), eg.Namespace, eg.Name)
	if len(name) > maxTokenName {
		name = name[:maxTokenName]
	}
	return name
}

// revoke revokes the join token id; an empty id or a token the API no longer knows is done.
func revoke(ctx context.Context, api API, id string) error {
	if id == "" {
		return nil
	}
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil // never created by this controller; nothing to revoke
	}
	if err := api.RevokeJoinToken(ctx, uid); err != nil && !errors.Is(err, mgmtapi.ErrNotFound) {
		return err
	}
	return nil
}

// syncJoinToken keeps an active join token of group in the CR-owned Secret, rotating it before expiry
// and revoking the previous token after the grace period. It records the result in eg.Status. A non-empty
// conflict means the Secret name is taken by an object this CR does not control; nothing was changed.
func (r *Reconciler) syncJoinToken(ctx context.Context, api API, eg *v1alpha1.NexoraEngineGroup, group mgmtapi.EngineGroup) (conflict string, err error) {
	name := joinTokenSecretName(eg)
	var sec corev1.Secret
	err = r.Client.Get(ctx, types.NamespacedName{Namespace: eg.Namespace, Name: name}, &sec)
	secretExists := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return "", err
	}
	if secretExists && !metav1.IsControlledBy(&sec, eg) {
		return fmt.Sprintf("Secret %q exists and is not controlled by this NexoraEngineGroup", name), nil
	}

	tokens, err := api.JoinTokens(ctx)
	if err != nil {
		return "", err
	}
	var currentCreated time.Time // API creation time of the recorded token; zero when it is not listed
	for _, t := range tokens {
		if t.Id.String() == eg.Status.JoinTokenID {
			currentCreated = t.CreatedAt
		}
	}
	active := func(id string) bool {
		for _, t := range tokens {
			if t.Id.String() == id {
				return t.State == tokenActive && t.EngineGroupId == group.Id
			}
		}
		return false
	}

	now := r.Now()
	st := &eg.Status
	// The expiry is computed with the controller's clock at creation (now + ttl), so renewal decisions
	// and tests use one clock; the API's expires_at is the same instant up to request latency.
	current := st.JoinTokenID != "" && active(st.JoinTokenID) && secretExists && len(sec.Data[joinTokenKey]) > 0 &&
		st.JoinTokenExpiresAt != nil && now.Before(st.JoinTokenExpiresAt.Add(-renewBefore(eg)))
	if !current {
		if currentCreated, err = r.rotate(ctx, api, eg, group, name, active(st.JoinTokenID), now); err != nil {
			return "", err
		}
	}
	if st.PreviousJoinTokenID != "" && st.PreviousJoinTokenRevokeAt != nil && !now.Before(st.PreviousJoinTokenRevokeAt.Time) {
		if err := revoke(ctx, api, st.PreviousJoinTokenID); err != nil {
			return "", err
		}
		st.PreviousJoinTokenID, st.PreviousJoinTokenRevokeAt = "", nil
	}
	// A token carrying this CR's marker that status does not record, created before the recorded token,
	// was created by a reconcile whose status write failed: revoke it instead of leaving it valid until its
	// TTL. A newer unrecorded token is kept: it is the recorded one when this reconcile read a stale status
	// from the cache.
	for _, t := range tokens {
		id := t.Id.String()
		if t.State == tokenActive && t.EngineGroupId == group.Id && strings.HasPrefix(t.Name, tokenPrefix(eg)) &&
			t.CreatedAt.Before(currentCreated) && id != st.JoinTokenID && id != st.PreviousJoinTokenID {
			if err := revoke(ctx, api, id); err != nil {
				return "", err
			}
		}
	}
	st.JoinTokenSecret = name
	return "", nil
}

// rotate creates a join token, writes it to the Secret and records it; an old token that is still active
// becomes the previous token, revoked after revokeGracePeriod. It returns the new token's API creation time.
func (r *Reconciler) rotate(ctx context.Context, api API, eg *v1alpha1.NexoraEngineGroup, group mgmtapi.EngineGroup,
	secretName string, oldActive bool, now time.Time) (time.Time, error) {
	st := &eg.Status
	spec := eg.Spec.JoinToken
	ttl := durationOr(spec.TTL, defaultTTL)
	in := mgmtapi.JoinTokenCreate{EngineGroupId: ptr(group.Id), Name: tokenName(eg, now), TtlSeconds: int(ttl / time.Second)}
	if spec.MaxUses != nil {
		in.MaxUses = ptr(int(*spec.MaxUses))
	}
	if len(spec.Labels) > 0 {
		in.Labels = ptr(spec.Labels)
	}
	created, err := api.CreateJoinToken(ctx, in)
	if err != nil {
		return time.Time{}, err
	}

	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: eg.Namespace, Name: secretName}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, sec, func() error {
		sec.Type = corev1.SecretTypeOpaque
		sec.Data = map[string][]byte{joinTokenKey: []byte(created.Token)}
		return controllerutil.SetControllerReference(eg, sec, r.Scheme)
	})
	if err != nil {
		// The token never reached a Secret: revoke it so it cannot outlive this attempt.
		_ = api.RevokeJoinToken(ctx, created.JoinToken.Id)
		return time.Time{}, fmt.Errorf("write join token Secret %q: %w", secretName, err)
	}

	if oldActive {
		// A still-pending previous token loses its grace period: only one previous token is tracked.
		if err := revoke(ctx, api, st.PreviousJoinTokenID); err != nil {
			return time.Time{}, err
		}
		st.PreviousJoinTokenID = st.JoinTokenID
		st.PreviousJoinTokenRevokeAt = &metav1.Time{Time: now.Add(durationOr(spec.RevokeGracePeriod, defaultRevokeGracePeriod))}
	}
	st.JoinTokenID = created.JoinToken.Id.String()
	st.JoinTokenExpiresAt = &metav1.Time{Time: now.Add(ttl)}
	return created.JoinToken.CreatedAt, nil
}
