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

	// maxOwnedTokens caps the active join tokens one NexoraEngineGroup may own. Two is the working
	// maximum (the current token and the previous one inside its grace period); the third is slack for a
	// token a failed status write left unrecorded. Past it the controller stops creating tokens and says
	// so on JoinTokenReady.
	maxOwnedTokens = 3
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

// revokeAll revokes the recorded tokens of eg and every active token carrying its marker, in any group:
// status may not record the group either when the first status write failed, and the uid is unique to eg.
func revokeAll(ctx context.Context, api API, eg *v1alpha1.NexoraEngineGroup) error {
	ids := []string{eg.Status.JoinTokenID, eg.Status.PreviousJoinTokenID}
	tokens, err := api.JoinTokens(ctx)
	if err != nil {
		return err
	}
	for _, t := range tokens {
		if t.State == tokenActive && strings.HasPrefix(t.Name, tokenPrefix(eg)) {
			ids = append(ids, t.Id.String())
		}
	}
	_, err = revokeEach(ctx, api, ids)
	return err
}

// revokeEach revokes every id once, in order, and stops at the first failure, returning the id it could
// not revoke. Duplicates and empty ids cost nothing.
func revokeEach(ctx context.Context, api API, ids []string) (string, error) {
	done := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || done[id] {
			continue
		}
		done[id] = true
		if err := revoke(ctx, api, id); err != nil {
			return id, err
		}
	}
	return "", nil
}

// ownedActive lists the active join tokens of group carrying eg's marker: the tokens this CR is
// responsible for, as the management plane sees them (status may be stale or unwritten).
func ownedActive(tokens []mgmtapi.JoinToken, eg *v1alpha1.NexoraEngineGroup, group mgmtapi.EngineGroup) []mgmtapi.JoinToken {
	var out []mgmtapi.JoinToken
	for _, t := range tokens {
		if t.State == tokenActive && t.EngineGroupId == group.Id && strings.HasPrefix(t.Name, tokenPrefix(eg)) {
			out = append(out, t)
		}
	}
	return out
}

// superseded is the ids among owned that the recorded token replaces: the recorded token itself survives,
// and so does a token created after it — that is what a reconcile reading a stale status sees as
// unrecorded. With nothing recorded (a status write that never landed) every owned token is superseded.
// The previous token is not included: inside its grace period it is still handed out to enrolling engines.
func superseded(owned []mgmtapi.JoinToken, st *v1alpha1.NexoraEngineGroupStatus, currentCreated time.Time) []string {
	var ids []string
	for _, t := range owned {
		id := t.Id.String()
		if id == st.JoinTokenID || id == st.PreviousJoinTokenID {
			continue
		}
		if currentCreated.IsZero() || t.CreatedAt.Before(currentCreated) {
			ids = append(ids, id)
		}
	}
	return ids
}

// syncJoinToken keeps an active join token of group in the CR-owned Secret, rotating it before expiry
// and revoking the previous token after the grace period. It records the result in eg.Status. A non-empty
// reason means the token could not be brought to the wanted state and nothing was created: the Secret and
// the token in it are left as they are, and the caller reports reason on JoinTokenReady.
func (r *Reconciler) syncJoinToken(ctx context.Context, api API, eg *v1alpha1.NexoraEngineGroup, group mgmtapi.EngineGroup) (reason, msg string, err error) {
	name := joinTokenSecretName(eg)
	var sec corev1.Secret
	err = r.Client.Get(ctx, types.NamespacedName{Namespace: eg.Namespace, Name: name}, &sec)
	secretExists := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return "", "", err
	}
	if secretExists && !metav1.IsControlledBy(&sec, eg) {
		return v1alpha1.ReasonConflict, fmt.Sprintf("Secret %q exists and is not controlled by this NexoraEngineGroup", name), nil
	}

	tokens, err := api.JoinTokens(ctx)
	if err != nil {
		return "", "", err
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
	owned := ownedActive(tokens, eg, group)
	// A token carrying this CR's marker that status does not record, created before the recorded token,
	// was created by a reconcile whose status write failed: revoke it instead of leaving it valid until its
	// TTL. A newer unrecorded token is kept: it is the recorded one when this reconcile read a stale status
	// from the cache.
	stale := superseded(owned, st, currentCreated)
	if !current {
		// Every token this CR still owns must be gone before another is created, the previous one
		// included: a revoke that keeps failing is never answered with a new token. On kw a 415 on every
		// DELETE made this controller create ~2500 join tokens in nine minutes.
		if id, err := revokeEach(ctx, api, append(stale, st.PreviousJoinTokenID)); err != nil {
			return v1alpha1.ReasonJoinTokenRevokeFailed,
				fmt.Sprintf("join token %s could not be revoked (%v); no new join token is created until it is gone", id, err), nil
		}
		revoked := len(stale)
		if st.PreviousJoinTokenID != "" {
			revoked++
		}
		st.PreviousJoinTokenID, st.PreviousJoinTokenRevokeAt = "", nil
		stale = nil
		// Hard ceiling on the join tokens one CR may own, counted from the management plane rather than
		// from status, so neither a reconcile storm nor a stale status can pass it.
		if kept := len(owned) - revoked; kept+1 > maxOwnedTokens {
			return v1alpha1.ReasonJoinTokenLimit,
				fmt.Sprintf("engine group %q still has %d active join tokens of this NexoraEngineGroup (ceiling %d); no new join token is created",
					group.Name, kept, maxOwnedTokens), nil
		}
		if err := r.rotate(ctx, api, eg, group, name, active(st.JoinTokenID), now); err != nil {
			return "", "", err
		}
	}
	graceOver := st.PreviousJoinTokenID != "" && st.PreviousJoinTokenRevokeAt != nil && !now.Before(st.PreviousJoinTokenRevokeAt.Time)
	if graceOver {
		stale = append(stale, st.PreviousJoinTokenID)
	}
	if id, err := revokeEach(ctx, api, stale); err != nil {
		return v1alpha1.ReasonJoinTokenRevokeFailed, fmt.Sprintf("join token %s could not be revoked: %v", id, err), nil
	}
	if graceOver {
		st.PreviousJoinTokenID, st.PreviousJoinTokenRevokeAt = "", nil
	}
	st.JoinTokenSecret = name
	return "", "", nil
}

// rotate creates a join token, writes it to the Secret and records it; an old token that is still active
// becomes the previous token, revoked after revokeGracePeriod. The new token is recorded in status before
// anything else can fail, so a later error can never leave it unrecorded and have the next reconcile
// create another.
func (r *Reconciler) rotate(ctx context.Context, api API, eg *v1alpha1.NexoraEngineGroup, group mgmtapi.EngineGroup,
	secretName string, oldActive bool, now time.Time) error {
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
		return err
	}

	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: eg.Namespace, Name: secretName}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, sec, func() error {
		sec.Type = corev1.SecretTypeOpaque
		sec.Data = map[string][]byte{joinTokenKey: []byte(created.Token)}
		return controllerutil.SetControllerReference(eg, sec, r.Scheme)
	})
	if err != nil {
		// The token never reached a Secret: revoke it so it cannot outlive this attempt. A revoke that
		// fails too leaves an unrecorded token of this CR, which the ceiling in syncJoinToken bounds.
		_ = api.RevokeJoinToken(ctx, created.JoinToken.Id)
		return fmt.Errorf("write join token Secret %q: %w", secretName, err)
	}

	if oldActive {
		// The replaced token stays valid for revokeGracePeriod, so engines that read the Secret just
		// before the rotation can still enrol. Only one previous token is tracked; the caller revoked any
		// earlier one before this rotation.
		st.PreviousJoinTokenID = st.JoinTokenID
		st.PreviousJoinTokenRevokeAt = &metav1.Time{Time: now.Add(durationOr(spec.RevokeGracePeriod, defaultRevokeGracePeriod))}
	}
	st.JoinTokenID = created.JoinToken.Id.String()
	st.JoinTokenExpiresAt = &metav1.Time{Time: now.Add(ttl)}
	return nil
}
