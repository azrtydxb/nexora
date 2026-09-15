//go:build kwe2e

package kw_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/piwi3910/nexora/operator/api/v1alpha1"
	"github.com/piwi3910/nexora/operator/internal/render"
)

func TestKwOperator(t *testing.T) {
	ns := os.Getenv("NEXORA_OPTEST_NAMESPACE")
	nodes := strings.Split(os.Getenv("NEXORA_OPTEST_NODES"), ",")
	ctx := context.Background()
	key := types.NamespacedName{Namespace: ns, Name: "nexora-optest"}
	var inst v1alpha1.NexoraInstallation

	// A failed guard stops everything: nothing may be applied that could touch production.
	if !t.Run("guards", func(t *testing.T) {
		if ns != "nexora-optest" {
			t.Fatalf("namespace %q: the operator e2e runs only in nexora-optest", ns)
		}
		if kubeContext() != "kw" {
			t.Fatalf("kube context %q: the operator e2e runs only on kw", kubeContext())
		}
		if len(nodes) != 3 {
			t.Fatalf("NEXORA_OPTEST_NODES %q must name three nodes", os.Getenv("NEXORA_OPTEST_NODES"))
		}
		for _, n := range nodes {
			if n == "master-12" || n == "master-13" {
				t.Fatalf("node %s carries the production DNS addresses", n)
			}
		}
		manifest := loadInstallation(t)
		chart, err := render.LoadChart("../../../deploy/helm/nexora")
		if err != nil {
			t.Fatal(err)
		}
		vals, err := render.BuildValues(manifest.Spec, render.Injected{Tag: os.Getenv("NEXORA_OPTEST_TAG"), CASecret: "x", KEKSecret: "x", BootstrapTokenSecret: "x",
			JoinTokenSecrets: map[string]string{"default": "x", "edge": "x"}})
		if err != nil {
			t.Fatal(err)
		}
		objs, err := chart.Render(render.Target{Name: key.Name, Namespace: ns, KubeVersion: "v1.34.4", APIVersions: []string{"postgresql.cnpg.io/v1"}}, vals.Map)
		if err != nil {
			t.Fatal(err)
		}
		services := 0
		for _, o := range objs {
			if o.GetNamespace() != ns {
				t.Fatalf("%s %s renders into namespace %q", o.GetKind(), o.GetName(), o.GetNamespace())
			}
			if o.GetKind() != "Service" {
				continue
			}
			services++
			typ, _, _ := unstructured.NestedString(o.Object, "spec", "type")
			ip, _, _ := unstructured.NestedString(o.Object, "spec", "loadBalancerIP")
			if typ == "LoadBalancer" || typ == "NodePort" || ip != "" {
				t.Fatalf("Service %s would take a node or LoadBalancer address (%s %s)", o.GetName(), typ, ip)
			}
		}
		if services == 0 {
			t.Fatal("no Service rendered: the guard checked nothing")
		}
		kubectl(t, "apply", "-f", writeTemp(t, manifest), "-f", "testdata/enginegroups.yaml")
	}) {
		t.FailNow()
	}
	c := newClient(t)

	if !t.Run("install", func(t *testing.T) {
		start := time.Now()
		waitFor(t, 15*time.Minute, "installation Ready", func() (bool, error) {
			if err := c.Get(ctx, key, &inst); err != nil {
				return false, err
			}
			return condTrue(&inst, v1alpha1.ConditionReady), nil
		})
		t.Logf("installation Ready after %s", time.Since(start).Round(time.Second))
		var zone map[string]any
		if code := apiCall(t, "POST", "/zones", map[string]any{"name": "optest.nexora.test.", "kind": "primary", "default_ttl": 60,
			"soa":         map[string]string{"mname": "ns1.optest.nexora.test.", "rname": "hostmaster.optest.nexora.test."},
			"nameservers": []string{"ns1.optest.nexora.test."}}, &zone); code != 201 {
			t.Fatalf("create zone -> %d %v", code, zone)
		}
		if code := apiCall(t, "POST", "/zones/"+zone["id"].(string)+"/records", map[string]any{"name": "www.optest.nexora.test.", "type": "A", "data": "192.0.2.10", "ttl": 60}, nil); code != 201 {
			t.Fatalf("create record -> %d", code)
		}
		for _, svc := range []string{"nexora-optest-dns-a", "nexora-optest-dns-b"} {
			ip := serviceIP(t, c, ns, svc)
			waitFor(t, 2*time.Minute, svc+" answers the zone", func() (bool, error) {
				out, _ := probeErr("", "dig +short +time=1 +tries=1 @"+ip+" www.optest.nexora.test. A")
				return strings.TrimSpace(out) == "192.0.2.10", nil
			})
		}
	}) {
		t.FailNow()
	}

	t.Run("engine-groups", func(t *testing.T) {
		var edge v1alpha1.NexoraEngineGroup
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "edge"}, &edge); err != nil || edge.Status.GroupID == "" {
			t.Fatalf("edge engine group: %v %+v", err, edge.Status)
		}
		waitFor(t, 5*time.Minute, "an engine enrolled into edge on "+nodes[2], func() (bool, error) {
			var engines []map[string]any
			apiCall(t, "GET", "/engines", nil, &engines)
			for _, e := range engines {
				if e["engine_group_id"] == edge.Status.GroupID && e["node_name"] == nodes[2] && e["connected"] == true {
					return true, nil
				}
			}
			return false, nil
		})
	})

	t.Run("rolling-update", func(t *testing.T) {
		before := enginePodUIDs(t, c, ns)
		ips := map[string]string{"a": serviceIP(t, c, ns, "nexora-optest-dns-a"), "b": serviceIP(t, c, ns, "nexora-optest-dns-b")}
		type result struct {
			lost    int
			noerror bool
		}
		results := map[string]result{}
		digLost := map[string]int{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for name, ip := range ips {
			wg.Add(2)
			go func() {
				defer wg.Done()
				lost, ok := dnsperf(t, ip, 240)
				mu.Lock()
				results[name] = result{lost, ok}
				mu.Unlock()
			}()
			// The same rate with a fresh socket per query: a client that is not a single connected socket.
			go func() {
				defer wg.Done()
				lost := digLoop(t, ip, 240)
				mu.Lock()
				digLost[name] = lost
				mu.Unlock()
			}()
		}
		time.Sleep(10 * time.Second)
		start := time.Now()
		updateRetry(t, c, key, &inst, func() {
			one := int32(1)
			inst.Spec.Engine.Workers = &one
		})
		waitFor(t, 200*time.Second, "every engine pod replaced and ready", func() (bool, error) {
			after := enginePodUIDs(t, c, ns)
			if len(after) != len(before) {
				return false, nil
			}
			for uid := range after {
				if before[uid] {
					return false, nil
				}
			}
			return enginesReady(t, c, ns), nil
		})
		t.Logf("every engine pod replaced after %s", time.Since(start).Round(time.Second))
		wg.Wait()
		for _, name := range []string{"a", "b"} {
			r := results[name]
			t.Logf("instance %s during the roll: %d lost, NOERROR only=%v", name, r.lost, r.noerror)
			if r.lost != 0 || !r.noerror {
				t.Errorf("instance %s during the roll: %d lost, NOERROR only=%v", name, r.lost, r.noerror)
			}
			if digLost[name] != 0 {
				t.Errorf("instance %s during the roll: dig loop lost or failed %d queries", name, digLost[name])
			}
		}
	})

	t.Run("join-token-rotation", func(t *testing.T) {
		egKey := types.NamespacedName{Namespace: ns, Name: "edge"}
		var edge v1alpha1.NexoraEngineGroup
		updateRetry(t, c, egKey, &edge, func() {
			edge.Spec.JoinToken.TTL = &metav1.Duration{Duration: 3 * time.Minute}
			edge.Spec.JoinToken.RenewBefore = &metav1.Duration{Duration: 2 * time.Minute}
			edge.Spec.JoinToken.RevokeGracePeriod = &metav1.Duration{Duration: 30 * time.Second}
		})
		tokenState := func(id string) string {
			var tokens []map[string]any
			apiCall(t, "GET", "/join-tokens", nil, &tokens)
			for _, tok := range tokens {
				if tok["id"] == id {
					s, _ := tok["state"].(string)
					return s
				}
			}
			return ""
		}
		// rotated waits until the join token differs from (id, token) in the status and the Secret, and the
		// controller has revoked id.
		rotated := func(what, id, token string) (string, string) {
			start := time.Now()
			var sec corev1.Secret
			waitFor(t, 4*time.Minute, what, func() (bool, error) {
				if err := c.Get(ctx, egKey, &edge); err != nil {
					return false, err
				}
				if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: edge.Status.JoinTokenSecret}, &sec); err != nil {
					return false, err
				}
				if edge.Status.JoinTokenID == id || string(sec.Data["join-token"]) == token || len(sec.Data["join-token"]) == 0 {
					return false, nil
				}
				return tokenState(id) == "revoked", nil
			})
			t.Logf("%s after %s", what, time.Since(start).Round(time.Second))
			return edge.Status.JoinTokenID, string(sec.Data["join-token"])
		}

		// The token recorded at install expires in 8760h, so a ttl change alone schedules nothing: remove its
		// Secret, one of the three renewal triggers. The old token, still active, is revoked after the grace.
		if err := c.Get(ctx, egKey, &edge); err != nil {
			t.Fatal(err)
		}
		firstID := edge.Status.JoinTokenID
		var sec corev1.Secret
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: edge.Status.JoinTokenSecret}, &sec); err != nil {
			t.Fatal(err)
		}
		firstToken := string(sec.Data["join-token"])
		if tokenState(firstID) != "active" {
			t.Fatalf("join token %s of edge is not active before the rotation", firstID)
		}
		if err := c.Delete(ctx, &sec); err != nil {
			t.Fatal(err)
		}
		secondID, secondToken := rotated("new join token for the missing Secret, predecessor revoked after the grace", firstID, firstToken)
		// The new token lives 3m and renews 2m before expiry: the controller rotates it after about 1m on its
		// own and revokes it 30s later.
		rotated("renewal before expiry, predecessor revoked after the grace", secondID, secondToken)
	})

	t.Run("prune", func(t *testing.T) {
		updateRetry(t, c, key, &inst, func() {
			var groups []v1alpha1.EngineGroupSpec
			for _, g := range inst.Spec.Engine.Groups {
				if g.Name != "edge" {
					groups = append(groups, g)
				}
			}
			inst.Spec.Engine.Groups = groups
		})
		waitFor(t, 3*time.Minute, "edge DaemonSet and Service pruned", func() (bool, error) {
			dsErr := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "nexora-optest-engine-edge"}, &appsv1.DaemonSet{})
			svcErr := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "nexora-optest-dns-edge"}, &corev1.Service{})
			gone := func(err error) bool { return err != nil && client.IgnoreNotFound(err) == nil }
			return gone(dsErr) && gone(svcErr), nil
		})
	})

	t.Run("cnpg-failover", func(t *testing.T) { cnpgFailover(t, c, ns) })
	t.Run("cnpg-backup-restore", func(t *testing.T) { cnpgBackupRestore(t, c, ns) })

	t.Run("delete-retains-state", func(t *testing.T) {
		if err := c.Get(ctx, key, &inst); err != nil {
			t.Fatal(err)
		}
		if err := c.Delete(ctx, &inst); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		waitFor(t, 2*time.Minute, "workloads garbage-collected", func() (bool, error) {
			err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "nexora-optest-mgmt"}, &appsv1.Deployment{})
			return err != nil && client.IgnoreNotFound(err) == nil && !enginesExist(t, c, ns), nil
		})
		t.Logf("workloads removed after %s", time.Since(start).Round(time.Second))
		cluster := cnpgObject("Cluster")
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "nexora-db"}, cluster); err != nil || cluster.GetDeletionTimestamp() != nil {
			t.Fatalf("CNPG cluster after deleting the installation: err %v, deletionTimestamp %v", err, cluster.GetDeletionTimestamp())
		}
		for _, s := range []string{"nexora-optest-ca", "nexora-optest-kek", "nexora-optest-operator-token"} {
			var sec corev1.Secret
			if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: s}, &sec); err != nil || sec.GetDeletionTimestamp() != nil {
				t.Errorf("secret %s: err %v, deletionTimestamp %v", s, err, sec.GetDeletionTimestamp())
			}
		}
	})
}

func cnpgObject(kind string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("postgresql.cnpg.io/v1")
	u.SetKind(kind)
	return u
}

// connectedEngines returns the applied_version of every connected engine by id.
func connectedEngines(t *testing.T) map[string]int64 {
	t.Helper()
	var engines []map[string]any
	if code := apiCall(t, "GET", "/engines", nil, &engines); code != 200 {
		t.Fatalf("GET /engines -> %d", code)
	}
	out := map[string]int64{}
	for _, e := range engines {
		if e["connected"] == true {
			v, _ := e["applied_version"].(float64)
			out[e["id"].(string)] = int64(v)
		}
	}
	return out
}

// cnpgFailover deletes the primary of nexora-db and proves the management plane and configuration
// distribution survive: a new primary, a healthy API and a group change that reaches every engine.
func cnpgFailover(t *testing.T, c client.Client, ns string) {
	ctx := context.Background()
	clusterKey := types.NamespacedName{Namespace: ns, Name: "nexora-db"}
	cluster := cnpgObject("Cluster")
	if err := c.Get(ctx, clusterKey, cluster); err != nil {
		t.Fatal(err)
	}
	primary, _, _ := unstructured.NestedString(cluster.Object, "status", "currentPrimary")
	if primary == "" {
		t.Fatal("nexora-db has no current primary")
	}
	before := connectedEngines(t)
	var maxApplied int64
	for _, v := range before {
		maxApplied = max(maxApplied, v)
	}
	if len(before) == 0 {
		t.Fatal("no connected engine before the failover")
	}

	start := time.Now()
	if err := c.Delete(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: primary}}); err != nil {
		t.Fatalf("delete primary pod %s: %v", primary, err)
	}
	// Measured up to 5 minutes so an overrun reports its real duration; the requirement is 120 s.
	var newPrimary string
	waitFor(t, 5*time.Minute, "a new CNPG primary", func() (bool, error) {
		if err := c.Get(ctx, clusterKey, cluster); err != nil {
			return false, err
		}
		newPrimary, _, _ = unstructured.NestedString(cluster.Object, "status", "currentPrimary")
		return newPrimary != "" && newPrimary != primary, nil
	})
	promoted := time.Now()
	targetAt, _, _ := unstructured.NestedString(cluster.Object, "status", "targetPrimaryTimestamp")
	t.Logf("new primary %s after %s (CNPG targetPrimaryTimestamp %s, pod deleted at %s)", newPrimary,
		promoted.Sub(start).Round(time.Second), targetAt, start.UTC().Format(time.RFC3339))
	if promoted.Sub(start) > 120*time.Second {
		t.Errorf("currentPrimary changed after %s, want within 120s", promoted.Sub(start).Round(time.Second))
	}
	waitFor(t, 60*time.Second, "GET /health 200", func() (bool, error) {
		return apiCall(t, "GET", "/health", nil, nil) == 200, nil
	})
	healthy := time.Now()
	t.Logf("failover: primary %s -> %s after %s; API healthy %s after the primary change (%s after the pod deletion)",
		primary, newPrimary, promoted.Sub(start).Round(time.Second), healthy.Sub(promoted).Round(time.Second), healthy.Sub(start).Round(time.Second))

	desc := fmt.Sprintf("failover-%d", time.Now().Unix())
	var eg v1alpha1.NexoraEngineGroup
	updateRetry(t, c, types.NamespacedName{Namespace: ns, Name: "default"}, &eg, func() { eg.Spec.Description = &desc })
	waitFor(t, 120*time.Second, "group description and a higher applied_version on every connected engine", func() (bool, error) {
		var groups []map[string]any
		apiCall(t, "GET", "/engine-groups", nil, &groups)
		found := false
		for _, g := range groups {
			if g["name"] == "default" && g["description"] == desc {
				found = true
			}
		}
		if !found {
			return false, nil
		}
		now := connectedEngines(t)
		if len(now) == 0 {
			return false, nil
		}
		for _, v := range now {
			if v <= maxApplied {
				return false, nil
			}
		}
		return true, nil
	})
	t.Logf("group change applied by every connected engine %s after the pod deletion", time.Since(start).Round(time.Second))
}

// cnpgBackupRestore takes an on-demand backup of nexora-db and restores it into a separate cluster
// rendered by the chart's recovery values, then checks the restored data.
func cnpgBackupRestore(t *testing.T, c client.Client, ns string) {
	ctx := context.Background()
	start := time.Now()
	backup := cnpgObject("Backup")
	backup.SetNamespace(ns)
	backup.SetName(fmt.Sprintf("optest-%d", time.Now().Unix()))
	backup.Object["spec"] = map[string]any{"cluster": map[string]any{"name": "nexora-db"}, "method": "barmanObjectStore"}
	if err := c.Create(ctx, backup); err != nil {
		t.Fatalf("create Backup: %v", err)
	}
	waitFor(t, 10*time.Minute, "Backup "+backup.GetName()+" completed", func() (bool, error) {
		if err := c.Get(ctx, client.ObjectKeyFromObject(backup), backup); err != nil {
			return false, err
		}
		phase, _, _ := unstructured.NestedString(backup.Object, "status", "phase")
		if phase == "failed" {
			msg, _, _ := unstructured.NestedString(backup.Object, "status", "error")
			t.Fatalf("Backup %s failed: %s", backup.GetName(), msg)
		}
		return phase == "completed", nil
	})
	backedUp := time.Now()
	t.Logf("backup completed after %s", backedUp.Sub(start).Round(time.Second))

	manifest := loadInstallation(t)
	cnpg := &manifest.Spec.Database.CNPG
	cnpg.ClusterName = "nexora-db-restore"
	// One instance: the restore proves the data, not replication.
	cnpg.Instances = ptr(int32(1))
	cnpg.Backup.Enabled = ptr(false)
	cnpg.Recovery.Enabled = ptr(true)
	cnpg.Recovery.SourceServerName = "nexora-db"
	chart, err := render.LoadChart("../../../deploy/helm/nexora")
	if err != nil {
		t.Fatal(err)
	}
	vals, err := render.BuildValues(manifest.Spec, render.Injected{Tag: os.Getenv("NEXORA_OPTEST_TAG"), CASecret: "x", KEKSecret: "x", BootstrapTokenSecret: "x",
		JoinTokenSecrets: map[string]string{"default": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	objs, err := chart.Render(render.Target{Name: "nexora-optest", Namespace: ns, KubeVersion: "v1.34.4", APIVersions: []string{"postgresql.cnpg.io/v1"}}, vals.Map)
	if err != nil {
		t.Fatal(err)
	}
	var restore *unstructured.Unstructured
	for _, o := range objs {
		if o.GetKind() == "Cluster" {
			if restore != nil {
				t.Fatal("more than one Cluster rendered")
			}
			restore = o
		}
	}
	if restore == nil || restore.GetName() != "nexora-db-restore" || len(restore.GetOwnerReferences()) != 0 {
		t.Fatalf("restore Cluster not rendered as expected: %v", restore)
	}
	if err := c.Create(ctx, restore); err != nil {
		t.Fatalf("create restore Cluster: %v", err)
	}
	defer func() {
		if err := c.Delete(ctx, restore); client.IgnoreNotFound(err) != nil {
			t.Errorf("delete restore Cluster: %v", err)
		}
	}()
	waitFor(t, 15*time.Minute, "restore Cluster Ready", func() (bool, error) {
		live := cnpgObject("Cluster")
		if err := c.Get(ctx, client.ObjectKeyFromObject(restore), live); err != nil {
			return false, err
		}
		conds, _, _ := unstructured.NestedSlice(live.Object, "status", "conditions")
		for _, cond := range conds {
			if m, ok := cond.(map[string]any); ok && m["type"] == "Ready" && m["status"] == "True" {
				return true, nil
			}
		}
		phase, _, _ := unstructured.NestedString(live.Object, "status", "phase")
		return false, fmt.Errorf("phase %q", phase)
	})
	out := kubectl(t, "exec", "nexora-db-restore-1", "-c", "postgres", "--", "psql", "-d", "nexora", "-tAc",
		"select count(*) from engine_groups where name = 'edge'")
	if strings.TrimSpace(out) != "1" {
		t.Fatalf("restored engine_groups rows named edge: %q, want 1", out)
	}
	t.Logf("restore Ready and verified %s after the backup (%s in total)", time.Since(backedUp).Round(time.Second), time.Since(start).Round(time.Second))
}

func ptr[T any](v T) *T { return &v }
