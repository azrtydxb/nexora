package installation_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/piwi3910/nexora/operator/api/v1alpha1"
	"github.com/piwi3910/nexora/operator/internal/controller/installation"
	"github.com/piwi3910/nexora/operator/internal/envtestutil"
	"github.com/piwi3910/nexora/operator/internal/render"
)

type fakeHealth struct {
	res installation.HealthResult
	err error
}

func (f *fakeHealth) Check(context.Context, string, string) (installation.HealthResult, error) {
	return f.res, f.err
}

type env struct {
	t      *testing.T
	c      client.Client
	r      *installation.Reconciler
	health *fakeHealth
	ns     string
}

func setup(t *testing.T, withCNPG bool) *env {
	var extra []string
	if withCNPG {
		extra = append(extra, "testdata/cnpg-crd.yaml")
	}
	cfg, c := envtestutil.Start(t, extra...)
	chart, err := render.LoadChart(filepath.Join(envtestutil.RepoRoot(t), "deploy/helm/nexora"))
	if err != nil {
		t.Fatal(err)
	}
	disc := discovery.NewDiscoveryClientForConfigOrDie(rest.CopyConfig(cfg))
	e := &env{t: t, c: c, health: &fakeHealth{}, ns: "inst-" + uuid.NewString()[:8]}
	e.r = &installation.Reconciler{Client: c, Scheme: c.Scheme(), Chart: chart, Discovery: disc, Health: e.health,
		OperatorVersion: "sha-abc1234", ResyncInterval: time.Minute}
	if err := c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: e.ns}}); err != nil {
		t.Fatal(err)
	}
	return e
}

func i32(v int32) *int32 { return &v }

func kwLike(ns string) *v1alpha1.NexoraInstallation {
	svc := func(name string) *v1alpha1.ServiceSpec { return &v1alpha1.ServiceSpec{Name: name, Type: "ClusterIP"} }
	return &v1alpha1.NexoraInstallation{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "nexora"},
		Spec: v1alpha1.NexoraInstallationSpec{
			Database: v1alpha1.DatabaseSpec{Mode: "external", External: v1alpha1.ExternalDatabaseSpec{ExistingSecret: "nexora-db-app", Key: "uri"}},
			Engine: v1alpha1.EngineSpec{Groups: []v1alpha1.EngineGroupSpec{{Name: "default", WorkloadName: "nexora-engine-default",
				Instances: []v1alpha1.EngineInstanceSpec{{Name: "a", Node: "node-1", Service: svc("nexora-dns-a")}, {Name: "b", Node: "node-2", Service: svc("nexora-dns-b")}}}}},
		}}
}

func (e *env) readyEngineGroup(name, secret string) {
	ctx := context.Background()
	g := &v1alpha1.NexoraEngineGroup{ObjectMeta: metav1.ObjectMeta{Namespace: e.ns, Name: name},
		Spec: v1alpha1.NexoraEngineGroupSpec{InstallationRef: v1alpha1.LocalRef{Name: "nexora"}}}
	if err := e.c.Create(ctx, g); err != nil {
		e.t.Fatal(err)
	}
	g.Status.JoinTokenSecret = secret
	apimeta.SetStatusCondition(&g.Status.Conditions, metav1.Condition{Type: v1alpha1.ConditionJoinTokenReady, Status: metav1.ConditionTrue, Reason: v1alpha1.ReasonReconciled})
	if err := e.c.Status().Update(ctx, g); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) reconcile() *v1alpha1.NexoraInstallation {
	e.t.Helper()
	ctx := context.Background()
	if _, err := e.r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: e.ns, Name: "nexora"}}); err != nil {
		e.t.Fatalf("reconcile: %v", err)
	}
	var inst v1alpha1.NexoraInstallation
	if err := e.c.Get(ctx, types.NamespacedName{Namespace: e.ns, Name: "nexora"}, &inst); err != nil {
		e.t.Fatal(err)
	}
	return &inst
}

func (e *env) get(obj client.Object, name string) error {
	return e.c.Get(context.Background(), types.NamespacedName{Namespace: e.ns, Name: name}, obj)
}

func condition(inst *v1alpha1.NexoraInstallation, typ string) metav1.Condition {
	if c := apimeta.FindStatusCondition(inst.Status.Conditions, typ); c != nil {
		return *c
	}
	return metav1.Condition{}
}

func TestInstallationCreatesChartObjects(t *testing.T) {
	e := setup(t, false)
	if err := e.c.Create(context.Background(), kwLike(e.ns)); err != nil {
		t.Fatal(err)
	}
	e.readyEngineGroup("default", "default-join-token")
	inst := e.reconcile()

	var mgmt appsv1.Deployment
	if err := e.get(&mgmt, "nexora-mgmt"); err != nil {
		t.Fatal(err)
	}
	var tokenEnv bool
	for _, ev := range mgmt.Spec.Template.Spec.Containers[0].Env {
		tokenEnv = tokenEnv || (ev.Name == "NEXORA_BOOTSTRAP_TOKEN_FILE" && ev.Value == "/etc/nexora/bootstrap-token/token")
	}
	if !tokenEnv || mgmt.Labels[v1alpha1.LabelInstallation] != "nexora" || len(mgmt.OwnerReferences) != 1 {
		t.Fatalf("mgmt deployment env/labels/owners: %v %v %v", tokenEnv, mgmt.Labels, mgmt.OwnerReferences)
	}
	for inst, node := range map[string]string{"a": "node-1", "b": "node-2"} {
		var ds appsv1.DaemonSet
		if err := e.get(&ds, "nexora-engine-default-"+inst); err != nil {
			t.Fatalf("daemonset %s: %v", inst, err)
		}
		terms := ds.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
		last := terms[0].MatchExpressions[len(terms[0].MatchExpressions)-1]
		ru := ds.Spec.UpdateStrategy.RollingUpdate
		if last.Key != "kubernetes.io/hostname" || last.Values[0] != node || ru.MaxSurge.IntValue() != 1 || ru.MaxUnavailable.IntValue() != 0 {
			t.Errorf("daemonset %s: pin %v rolling %v", inst, last, ru)
		}
		var svc corev1.Service
		if err := e.get(&svc, "nexora-dns-"+inst); err != nil || svc.Spec.Type != corev1.ServiceTypeClusterIP {
			t.Errorf("service %s: %v %v", inst, err, svc.Spec.Type)
		}
	}
	for _, name := range []string{"nexora-ca", "nexora-kek", "nexora-operator-token"} {
		var s corev1.Secret
		if err := e.get(&s, name); err != nil || len(s.OwnerReferences) != 0 {
			t.Errorf("secret %s: err=%v owners=%v", name, err, s.OwnerReferences)
		}
	}
	if inst.Status.Secrets.CA != "nexora-ca" || inst.Status.Secrets.OperatorToken != "nexora-operator-token" ||
		inst.Status.ManagementURL != "http://nexora-mgmt."+e.ns+".svc:8080" || condition(inst, v1alpha1.ConditionRendered).Status != metav1.ConditionTrue {
		t.Fatalf("status = %+v", inst.Status)
	}
}

func TestInstallationWaitsForJoinTokens(t *testing.T) {
	e := setup(t, false)
	_ = e.c.Create(context.Background(), kwLike(e.ns))
	inst := e.reconcile()
	var ds appsv1.DaemonSet
	if err := e.get(&ds, "nexora-engine-default-a"); err == nil {
		t.Fatal("an engine workload rendered without a join token")
	}
	var mgmt appsv1.Deployment
	if err := e.get(&mgmt, "nexora-mgmt"); err != nil {
		t.Fatalf("mgmt must render while engines wait: %v", err)
	}
	if c := condition(inst, v1alpha1.ConditionEnginesReady); c.Reason != v1alpha1.ReasonJoinTokenPending {
		t.Fatalf("engines condition = %+v", c)
	}
	e.readyEngineGroup("default", "default-join-token")
	e.reconcile()
	for _, n := range []string{"nexora-engine-default-a", "nexora-engine-default-b"} {
		if err := e.get(&ds, n); err != nil {
			t.Errorf("%s after the join token: %v", n, err)
		}
	}
}

func TestInstallationPrunesRemovedObjects(t *testing.T) {
	e := setup(t, false)
	ctx := context.Background()
	inst := kwLike(e.ns)
	_ = e.c.Create(ctx, inst)
	e.readyEngineGroup("default", "default-join-token")
	e.reconcile()
	var a appsv1.DaemonSet
	_ = e.get(&a, "nexora-engine-default-a")
	_ = e.get(inst, "nexora")
	inst.Spec.Engine.Groups[0].Instances = inst.Spec.Engine.Groups[0].Instances[:1]
	if err := e.c.Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	e.reconcile()
	var b appsv1.DaemonSet
	if err := e.get(&b, "nexora-engine-default-b"); err == nil && b.DeletionTimestamp == nil {
		t.Error("removed instance b still exists")
	}
	var svc corev1.Service
	if err := e.get(&svc, "nexora-dns-b"); err == nil && svc.DeletionTimestamp == nil {
		t.Error("removed instance service still exists")
	}
	var a2 appsv1.DaemonSet
	if err := e.get(&a2, "nexora-engine-default-a"); err != nil || a2.UID != a.UID {
		t.Errorf("instance a replaced: %v", err)
	}
}

func TestInstallationKeepsDatabaseAndKeys(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	inst := kwLike(e.ns)
	inst.Spec.Database = v1alpha1.DatabaseSpec{Mode: "cnpg", CNPG: v1alpha1.CNPGSpec{Instances: i32(2)}}
	_ = e.c.Create(ctx, inst)
	e.readyEngineGroup("default", "default-join-token")
	e.reconcile()
	cluster := &unstructured.Unstructured{}
	cluster.SetAPIVersion("postgresql.cnpg.io/v1")
	cluster.SetKind("Cluster")
	if err := e.get(cluster, "nexora-db"); err != nil || len(cluster.GetOwnerReferences()) != 0 {
		t.Fatalf("cluster: err=%v owners=%v", err, cluster.GetOwnerReferences())
	}
	_ = e.get(inst, "nexora")
	inst.Spec.Database.CNPG.Instances = i32(3)
	_ = e.c.Update(ctx, inst)
	e.reconcile()
	_ = e.get(cluster, "nexora-db")
	if n, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances"); n != 3 {
		t.Fatalf("instances = %d", n)
	}
	_ = e.get(inst, "nexora")
	inst.Spec.Database = v1alpha1.DatabaseSpec{Mode: "external", External: v1alpha1.ExternalDatabaseSpec{ExistingSecret: "pg"}}
	_ = e.c.Update(ctx, inst)
	e.reconcile()
	if err := e.get(cluster, "nexora-db"); err != nil || cluster.GetDeletionTimestamp() != nil {
		t.Fatalf("switching to external deleted the cluster: %v", err)
	}

	e2 := setup(t, false)
	broken := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: e2.ns, Name: "nexora-ca"}, Data: map[string][]byte{"ca.crt": []byte("x")}}
	_ = e2.c.Create(ctx, broken)
	_ = e2.c.Create(ctx, kwLike(e2.ns))
	got := e2.reconcile()
	var after corev1.Secret
	_ = e2.get(&after, "nexora-ca")
	if c := condition(got, v1alpha1.ConditionRendered); c.Reason != v1alpha1.ReasonSecretIncomplete || after.ResourceVersion != broken.ResourceVersion {
		t.Fatalf("incomplete CA: %+v rv %s->%s", c, broken.ResourceVersion, after.ResourceVersion)
	}
}

func TestInstallationRenderFailureKeepsObjects(t *testing.T) {
	e := setup(t, false)
	ctx := context.Background()
	inst := kwLike(e.ns)
	_ = e.c.Create(ctx, inst)
	e.readyEngineGroup("default", "default-join-token")
	e.reconcile()
	var before appsv1.Deployment
	_ = e.get(&before, "nexora-mgmt")
	_ = e.get(inst, "nexora")
	inst.Spec.Mgmt.Ingress = v1alpha1.IngressSpec{Enabled: func() *bool { b := true; return &b }()} // host required by the chart
	if err := e.c.Update(ctx, inst); err != nil {
		t.Fatal(err)
	}
	got := e.reconcile()
	var after appsv1.Deployment
	_ = e.get(&after, "nexora-mgmt")
	if c := condition(got, v1alpha1.ConditionRendered); c.Status != metav1.ConditionFalse || c.Reason != v1alpha1.ReasonRenderFailed || after.ResourceVersion != before.ResourceVersion {
		t.Fatalf("render failure: %+v rv %s->%s", c, before.ResourceVersion, after.ResourceVersion)
	}
}

func TestInstallationStatusConditions(t *testing.T) {
	e := setup(t, false)
	ctx := context.Background()
	_ = e.c.Create(ctx, kwLike(e.ns))
	e.readyEngineGroup("default", "default-join-token")
	_ = e.c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: e.ns, Name: "nexora-db-app"}, Data: map[string][]byte{"uri": []byte("postgres://x")}})
	e.reconcile()
	e.health.res = installation.HealthResult{Healthy: true, SetupRequired: true}
	var mgmt appsv1.Deployment
	_ = e.get(&mgmt, "nexora-mgmt")
	mgmt.Status.Replicas, mgmt.Status.AvailableReplicas, mgmt.Status.ReadyReplicas = 2, 1, 1
	_ = e.c.Status().Update(ctx, &mgmt)
	setDS := func(name string, desired, updated, ready int32) {
		var ds appsv1.DaemonSet
		_ = e.get(&ds, name)
		ds.Status = appsv1.DaemonSetStatus{DesiredNumberScheduled: desired, UpdatedNumberScheduled: updated, NumberReady: ready,
			CurrentNumberScheduled: desired, NumberAvailable: ready, ObservedGeneration: ds.Generation}
		if err := e.c.Status().Update(ctx, &ds); err != nil {
			t.Fatal(err)
		}
	}
	setDS("nexora-engine-default-a", 1, 0, 1)
	setDS("nexora-engine-default-b", 1, 1, 1)
	inst := e.reconcile()
	for typ, want := range map[string]metav1.ConditionStatus{v1alpha1.ConditionManagementReady: "True", v1alpha1.ConditionSetupRequired: "True", v1alpha1.ConditionDatabaseReady: "True", v1alpha1.ConditionEnginesReady: "False", v1alpha1.ConditionReady: "False"} {
		if c := condition(inst, typ); c.Status != want {
			t.Errorf("%s = %+v, want %s", typ, c, want)
		}
	}
	if c := condition(inst, v1alpha1.ConditionEnginesReady); c.Reason != v1alpha1.ReasonRollingUpdate {
		t.Errorf("engines reason = %s", c.Reason)
	}
	setDS("nexora-engine-default-a", 1, 1, 1)
	inst = e.reconcile()
	if condition(inst, v1alpha1.ConditionReady).Status != metav1.ConditionTrue || len(inst.Status.Workloads) != 2 {
		t.Fatalf("ready: %+v workloads %v", condition(inst, v1alpha1.ConditionReady), inst.Status.Workloads)
	}
	e.health.err = errors.New("401 unauthorized")
	inst = e.reconcile()
	if c := condition(inst, v1alpha1.ConditionManagementReady); c.Status != metav1.ConditionFalse {
		t.Fatalf("health error: %+v", c)
	}
}

func TestInstallationDefaultsImageTagToOperatorVersion(t *testing.T) {
	e := setup(t, false)
	_ = e.c.Create(context.Background(), kwLike(e.ns))
	e.readyEngineGroup("default", "default-join-token")
	inst := e.reconcile()
	var mgmt appsv1.Deployment
	_ = e.get(&mgmt, "nexora-mgmt")
	if img := mgmt.Spec.Template.Spec.Containers[0].Image; img != "192.168.10.131/azrtydxb/nexora-mgmt:sha-abc1234" || inst.Status.Version != "sha-abc1234" {
		t.Fatalf("image %s version %s", img, inst.Status.Version)
	}
	e.r.OperatorVersion = "dev"
	inst = e.reconcile()
	if c := condition(inst, v1alpha1.ConditionRendered); c.Reason != v1alpha1.ReasonImageTagRequired {
		t.Fatalf("dev operator: %+v", c)
	}
}
