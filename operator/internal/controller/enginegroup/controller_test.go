package enginegroup_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/piwi3910/nexora/operator/api/v1alpha1"
	"github.com/piwi3910/nexora/operator/internal/controller/enginegroup"
	"github.com/piwi3910/nexora/operator/internal/envtestutil"
	"github.com/piwi3910/nexora/operator/internal/mgmtapi"
	"github.com/piwi3910/nexora/operator/internal/mgmtapi/fake"
)

type env struct {
	t   *testing.T
	c   client.Client
	r   *enginegroup.Reconciler
	f   *fake.Server
	now time.Time
	ns  string
}

func setup(t *testing.T) *env {
	_, c := envtestutil.Start(t)
	f := fake.New(t)
	e := &env{t: t, c: c, f: f, now: time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC), ns: "eg-" + uuid.NewString()[:8]}
	ctx := context.Background()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: e.ns}}); err != nil {
		t.Fatal(err)
	}
	e.r = &enginegroup.Reconciler{Client: c, Scheme: c.Scheme(), Now: func() time.Time { return e.now },
		ClientFor: func(ctx context.Context, inst *v1alpha1.NexoraInstallation) (enginegroup.API, error) {
			return mgmtapi.New(f.URL, f.Token, nil)
		}}
	inst := &v1alpha1.NexoraInstallation{ObjectMeta: metav1.ObjectMeta{Namespace: e.ns, Name: "nexora"}}
	if err := c.Create(ctx, inst); err != nil {
		t.Fatal(err)
	}
	e.setManagementReady(true)
	return e
}

func (e *env) setManagementReady(ok bool) {
	ctx := context.Background()
	var inst v1alpha1.NexoraInstallation
	if err := e.c.Get(ctx, types.NamespacedName{Namespace: e.ns, Name: "nexora"}, &inst); err != nil {
		e.t.Fatal(err)
	}
	st := metav1.ConditionFalse
	if ok {
		st = metav1.ConditionTrue
	}
	apimeta.SetStatusCondition(&inst.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionManagementReady, Status: st, Reason: v1alpha1.ReasonReconciled})
	inst.Status.ManagementURL = e.f.URL
	if err := e.c.Status().Update(ctx, &inst); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) create(g *v1alpha1.NexoraEngineGroup) *v1alpha1.NexoraEngineGroup {
	g.Namespace = e.ns
	g.Spec.InstallationRef.Name = "nexora"
	if err := e.c.Create(context.Background(), g); err != nil {
		e.t.Fatal(err)
	}
	return g
}

func (e *env) reconcile(name string) (ctrl.Result, *v1alpha1.NexoraEngineGroup) {
	e.t.Helper()
	ctx := context.Background()
	res, err := e.r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: e.ns, Name: name}})
	if err != nil {
		e.t.Fatalf("reconcile %s: %v", name, err)
	}
	var g v1alpha1.NexoraEngineGroup
	if err := e.c.Get(ctx, types.NamespacedName{Namespace: e.ns, Name: name}, &g); err != nil {
		return res, nil
	}
	return res, &g
}

func cond(g *v1alpha1.NexoraEngineGroup, typ string) *metav1.Condition {
	return apimeta.FindStatusCondition(g.Status.Conditions, typ)
}

func str(s string) *string { return &s }
func i32(v int32) *int32   { return &v }
func dur(s string) *metav1.Duration {
	d, _ := time.ParseDuration(s)
	return &metav1.Duration{Duration: d}
}

func TestEngineGroupCreatesGroupAndJoinToken(t *testing.T) {
	e := setup(t)
	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge"}, Spec: v1alpha1.NexoraEngineGroupSpec{
		Description: str("edge engines"), ExtraACLCIDRs: []string{"198.51.100.0/24"},
		JoinToken: v1alpha1.JoinTokenSpec{TTL: dur("24h"), RenewBefore: dur("1h"), MaxUses: i32(5), Labels: map[string]string{"site": "a"}}}})
	_, g := e.reconcile("edge")
	fg, ok := e.f.Group("edge")
	if !ok || fg.Description != "edge engines" || len(fg.ExtraAclCidrs) != 1 || g.Status.GroupID != fg.Id.String() {
		t.Fatalf("group %+v status %+v", fg, g.Status)
	}
	if c := cond(g, v1alpha1.ConditionReady); c == nil || c.Status != metav1.ConditionTrue {
		t.Fatalf("ready = %v", c)
	}
	var sec corev1.Secret
	if err := e.c.Get(context.Background(), types.NamespacedName{Namespace: e.ns, Name: "edge-join-token"}, &sec); err != nil {
		t.Fatal(err)
	}
	id := uuid.MustParse(g.Status.JoinTokenID)
	if string(sec.Data["join-token"]) != e.f.TokenSecret(id) || len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].Name != "edge" {
		t.Fatalf("secret %v owners %v", sec.Data, sec.OwnerReferences)
	}
	for _, tok := range e.f.Tokens() {
		if tok.Id == id {
			if tok.EngineGroupId != fg.Id || tok.MaxUses == nil || *tok.MaxUses != 5 || tok.Labels["site"] != "a" || len(tok.Name) > 64 {
				t.Fatalf("token %+v", tok)
			}
		}
	}
	if g.Status.JoinTokenSecret != "edge-join-token" {
		t.Fatalf("status.joinTokenSecret = %q", g.Status.JoinTokenSecret)
	}
}

func TestEngineGroupUpdatesOnlySetFields(t *testing.T) {
	e := setup(t)
	eg := e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge"}, Spec: v1alpha1.NexoraEngineGroupSpec{Description: str("one")}})
	e.reconcile("edge")
	e.f.MutateGroup("edge", func(g *mgmtapi.EngineGroup) { g.CanaryCount = 3 })
	eg.Spec.Description = str("two")
	if err := e.c.Get(context.Background(), client.ObjectKeyFromObject(eg), eg); err != nil {
		t.Fatal(err)
	}
	eg.Spec.Description = str("two")
	if err := e.c.Update(context.Background(), eg); err != nil {
		t.Fatal(err)
	}
	e.reconcile("edge")
	fg, _ := e.f.Group("edge")
	if fg.Description != "two" || fg.CanaryCount != 3 || e.f.LastUpdate == nil || e.f.LastUpdate.Revision != 2 {
		t.Fatalf("group %+v last update %+v", fg, e.f.LastUpdate)
	}
	puts := 0
	for _, r := range e.f.Requests {
		if len(r) > 3 && r[:3] == "PUT" {
			puts++
		}
	}
	e.reconcile("edge")
	after := 0
	for _, r := range e.f.Requests {
		if len(r) > 3 && r[:3] == "PUT" {
			after++
		}
	}
	if after != puts {
		t.Fatal("an unchanged CR sent another PUT")
	}
}

func TestEngineGroupRetriesOnConflict(t *testing.T) {
	e := setup(t)
	eg := e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge"}, Spec: v1alpha1.NexoraEngineGroupSpec{Description: str("one")}})
	e.reconcile("edge")
	_ = e.c.Get(context.Background(), client.ObjectKeyFromObject(eg), eg)
	eg.Spec.Description = str("two")
	_ = e.c.Update(context.Background(), eg)
	e.f.ConflictOnce = true
	res, g := e.reconcile("edge")
	if c := cond(g, v1alpha1.ConditionSynced); c == nil || c.Reason != v1alpha1.ReasonConflict || res.RequeueAfter == 0 {
		t.Fatalf("after conflict: %v %v", c, res)
	}
	_, g = e.reconcile("edge")
	if fg, _ := e.f.Group("edge"); fg.Description != "two" || cond(g, v1alpha1.ConditionSynced).Status != metav1.ConditionTrue {
		t.Fatalf("retry did not apply: %+v", fg)
	}
}

func TestEngineGroupDuplicateNameRefused(t *testing.T) {
	e := setup(t)
	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge"}})
	time.Sleep(1100 * time.Millisecond) // creationTimestamp has second resolution
	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge-copy"}, Spec: v1alpha1.NexoraEngineGroupSpec{GroupName: "edge"}})
	e.reconcile("edge")
	before := len(e.f.Requests)
	_, g := e.reconcile("edge-copy")
	if c := cond(g, v1alpha1.ConditionSynced); c == nil || c.Reason != v1alpha1.ReasonDuplicateGroupName || len(e.f.Requests) != before {
		t.Fatalf("duplicate: %v, %d new requests", c, len(e.f.Requests)-before)
	}
}

func TestEngineGroupRotatesJoinToken(t *testing.T) {
	e := setup(t)
	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge"}, Spec: v1alpha1.NexoraEngineGroupSpec{
		JoinToken: v1alpha1.JoinTokenSpec{TTL: dur("3h"), RenewBefore: dur("1h"), RevokeGracePeriod: dur("10m")}}})
	_, g := e.reconcile("edge")
	first := g.Status.JoinTokenID
	e.now = e.now.Add(90 * time.Minute)
	_, g = e.reconcile("edge")
	if g.Status.JoinTokenID != first {
		t.Fatal("rotated before renewBefore")
	}
	e.now = e.now.Add(31 * time.Minute) // 2h01m: expiry within 1h
	_, g = e.reconcile("edge")
	if g.Status.JoinTokenID == first || g.Status.PreviousJoinTokenID != first {
		t.Fatalf("no rotation: %+v", g.Status)
	}
	var sec corev1.Secret
	_ = e.c.Get(context.Background(), types.NamespacedName{Namespace: e.ns, Name: "edge-join-token"}, &sec)
	if string(sec.Data["join-token"]) != e.f.TokenSecret(uuid.MustParse(g.Status.JoinTokenID)) {
		t.Fatal("secret not updated to the new token")
	}
	state := func(id string) string {
		for _, tok := range e.f.Tokens() {
			if tok.Id.String() == id {
				return string(tok.State)
			}
		}
		return ""
	}
	e.now = e.now.Add(5 * time.Minute)
	e.reconcile("edge")
	if state(first) != "active" {
		t.Fatal("previous token revoked inside the grace period")
	}
	e.now = e.now.Add(6 * time.Minute)
	_, g = e.reconcile("edge")
	if state(first) != "revoked" || g.Status.PreviousJoinTokenID != "" {
		t.Fatalf("previous token after grace: %s %+v", state(first), g.Status)
	}
}

func TestEngineGroupRecreatesRevokedToken(t *testing.T) {
	e := setup(t)
	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge"}})
	_, g := e.reconcile("edge")
	first := uuid.MustParse(g.Status.JoinTokenID)
	e.f.SetTokenState(first, "revoked", e.now.Add(time.Hour))
	_, g = e.reconcile("edge")
	if g.Status.JoinTokenID == first.String() {
		t.Fatal("a revoked token was kept")
	}
}

func TestEngineGroupDeletion(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	del := func(name string) {
		var g v1alpha1.NexoraEngineGroup
		_ = e.c.Get(ctx, types.NamespacedName{Namespace: e.ns, Name: name}, &g)
		if err := e.c.Delete(ctx, &g); err != nil {
			t.Fatal(err)
		}
	}
	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "keep"}})
	_, g := e.reconcile("keep")
	tok := g.Status.JoinTokenID
	del("keep")
	if _, g := e.reconcile("keep"); g != nil {
		t.Fatalf("Retain: finalizer kept: %v", g.Finalizers)
	}
	if _, ok := e.f.Group("keep"); !ok {
		t.Fatal("Retain deleted the group")
	}
	for _, jt := range e.f.Tokens() {
		if jt.Id.String() == tok && jt.State != "revoked" {
			t.Fatal("Retain left the join token active")
		}
	}

	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "busy"}, Spec: v1alpha1.NexoraEngineGroupSpec{DeletionPolicy: "Delete"}})
	e.reconcile("busy")
	e.f.NonEmpty = map[string]bool{"busy": true}
	del("busy")
	_, g = e.reconcile("busy")
	if g == nil || cond(g, v1alpha1.ConditionSynced).Reason != v1alpha1.ReasonDeletionBlocked {
		t.Fatalf("non-empty delete: %v", g)
	}
	e.f.NonEmpty = nil
	if _, g := e.reconcile("busy"); g != nil {
		t.Fatal("finalizer kept after the group emptied")
	}
	if _, ok := e.f.Group("busy"); ok {
		t.Fatal("Delete kept the group")
	}

	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "default"}, Spec: v1alpha1.NexoraEngineGroupSpec{DeletionPolicy: "Delete"}})
	e.reconcile("default")
	del("default")
	e.reconcile("default")
	for _, r := range e.f.Requests {
		if r == "DELETE /api/v1/engine-groups/"+fake.DefaultGroupID {
			t.Fatal("the default group was deleted")
		}
	}

	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "orphan"}})
	e.reconcile("orphan")
	if err := e.c.Delete(ctx, &v1alpha1.NexoraInstallation{ObjectMeta: metav1.ObjectMeta{Namespace: e.ns, Name: "nexora"}}); err != nil {
		t.Fatal(err)
	}
	before := len(e.f.Requests)
	del("orphan")
	if _, g := e.reconcile("orphan"); g != nil || len(e.f.Requests) != before {
		t.Fatalf("orphan deletion: %v, %d calls", g, len(e.f.Requests)-before)
	}
}

func TestEngineGroupWaitsForManagement(t *testing.T) {
	e := setup(t)
	e.setManagementReady(false)
	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge"}})
	res, g := e.reconcile("edge")
	var sec corev1.Secret
	err := e.c.Get(context.Background(), types.NamespacedName{Namespace: e.ns, Name: "edge-join-token"}, &sec)
	if cond(g, v1alpha1.ConditionSynced).Reason != v1alpha1.ReasonManagementUnavailable || res.RequeueAfter == 0 || err == nil || len(e.f.Requests) != 0 {
		t.Fatalf("waiting: %v %v secret err=%v calls=%d", cond(g, v1alpha1.ConditionSynced), res, err, len(e.f.Requests))
	}
}

func TestEngineGroupUnauthorized(t *testing.T) {
	e := setup(t)
	e.f.Status = 401
	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge"}})
	_, g := e.reconcile("edge")
	var sec corev1.Secret
	err := e.c.Get(context.Background(), types.NamespacedName{Namespace: e.ns, Name: "edge-join-token"}, &sec)
	if cond(g, v1alpha1.ConditionSynced).Reason != v1alpha1.ReasonUnauthorized || err == nil {
		t.Fatalf("unauthorized: %v secret err=%v", cond(g, v1alpha1.ConditionSynced), err)
	}
}

// failStatus fails the next status write once *fail is set, as a lost API server connection would.
type failStatus struct {
	client.Client
	fail *bool
}

func (c failStatus) Status() client.SubResourceWriter { return failWriter{c.Client.Status(), c.fail} }

type failWriter struct {
	client.SubResourceWriter
	fail *bool
}

func (w failWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if *w.fail {
		*w.fail = false
		return errors.New("injected status write failure")
	}
	return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

// Catches: a join token created by a reconcile whose status write failed staying active (unrecorded)
// until its TTL, and the sweep revoking tokens this CR did not create.
func TestEngineGroupRevokesUnrecordedToken(t *testing.T) {
	e := setup(t)
	fail := true
	e.r.Client = failStatus{e.c, &fail}
	e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge"}})
	if _, err := e.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: e.ns, Name: "edge"}}); err == nil {
		t.Fatal("the injected status write failure was not returned")
	}
	tokens := e.f.Tokens()
	if len(tokens) != 1 {
		t.Fatalf("tokens after the failed reconcile: %+v", tokens)
	}
	orphan := tokens[0].Id
	fg, _ := e.f.Group("edge")
	api, err := mgmtapi.New(e.f.URL, e.f.Token, nil)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := api.CreateJoinToken(context.Background(), mgmtapi.JoinTokenCreate{Name: "manual", TtlSeconds: 3600, EngineGroupId: &fg.Id})
	if err != nil {
		t.Fatal(err)
	}

	_, g := e.reconcile("edge")
	state := map[uuid.UUID]string{}
	for _, tok := range e.f.Tokens() {
		state[tok.Id] = string(tok.State)
	}
	current := uuid.MustParse(g.Status.JoinTokenID)
	if current == orphan || state[current] != "active" {
		t.Fatalf("current token %s state %q", current, state[current])
	}
	if state[orphan] != "revoked" {
		t.Fatalf("unrecorded token %s is %q, want revoked", orphan, state[orphan])
	}
	if state[foreign.JoinToken.Id] != "active" {
		t.Fatal("a token this CR did not create was revoked")
	}

	// A marked token newer than the recorded one is what a reconcile reading a stale status would see
	// as unrecorded: it must survive.
	newer, err := api.CreateJoinToken(context.Background(), mgmtapi.JoinTokenCreate{Name: "op/" + string(g.UID) + "/1/x", TtlSeconds: 3600, EngineGroupId: &fg.Id})
	if err != nil {
		t.Fatal(err)
	}
	e.reconcile("edge")
	for _, tok := range e.f.Tokens() {
		if tok.Id == newer.JoinToken.Id && tok.State != "active" {
			t.Fatal("a marked token newer than the recorded token was revoked")
		}
	}
}

// Catches: an unrecorded join token of the CR (status write failed right after creation) staying valid
// after the CR is deleted.
func TestEngineGroupDeletionRevokesUnrecordedToken(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	fail := true
	e.r.Client = failStatus{e.c, &fail}
	eg := e.create(&v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Name: "edge"}})
	if _, err := e.r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: e.ns, Name: "edge"}}); err == nil {
		t.Fatal("the injected status write failure was not returned")
	}
	tokens := e.f.Tokens()
	if len(tokens) != 1 {
		t.Fatalf("tokens after the failed reconcile: %+v", tokens)
	}
	if err := e.c.Delete(ctx, eg); err != nil {
		t.Fatal(err)
	}
	if _, g := e.reconcile("edge"); g != nil {
		t.Fatalf("finalizer kept: %v", g.Finalizers)
	}
	for _, tok := range e.f.Tokens() {
		if tok.Id == tokens[0].Id && tok.State != "revoked" {
			t.Fatalf("unrecorded token after deletion is %q, want revoked", tok.State)
		}
	}
}
