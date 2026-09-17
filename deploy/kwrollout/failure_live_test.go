package kwrollout

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// TestMemberFailureLive is deliberately opt-in: it abruptly deletes one engine
// pod, never its state directory or node. A failed sample prevents further
// disruptions and retains the same deployment lock used by production upgrades.
func TestMemberFailureLive(t *testing.T) {
	target := os.Getenv("NEXORA_KW_FAILURE_MEMBER")
	if target == "" {
		t.Skip("set NEXORA_KW_FAILURE_MEMBER to one approved full workload name")
	}
	valid := false
	for _, pair := range KWPairTopology() {
		for _, member := range pair.Members {
			valid = valid || member.Workload == target
		}
	}
	if !valid {
		t.Fatal("unknown member")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	lock, err := NewDeploymentLock("kw", "nexora", "nexora")
	if err != nil {
		t.Fatal(err)
	}
	if err = lock.Acquire(ctx); err != nil {
		t.Fatal(err)
	}
	// No deferred unlock: every failure intentionally retains ownership.
	reader := NewFleetReader("kw")
	client, err := ManagementClient(ctx, "kw", "nexora", "https://nexora.kw.watteel.lab")
	if err != nil {
		t.Fatal(err)
	}
	before, err := reader.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.LegacySelectors || len(before.Pods) != 4 {
		t.Fatal("complete paired fleet required")
	}
	if err = reader.CheckNodes(ctx, before); err != nil {
		t.Fatal(err)
	}
	if err = CheckFleetManagement(ctx, client, "https://nexora.kw.watteel.lab", before); err != nil {
		t.Fatal(err)
	}
	checkControllers := func(snapshot FleetSnapshot) error {
		controllers, err := kubectlCommand("kw", "nexora")(ctx, nil, "get", "daemonsets", "-l", "app.kubernetes.io/instance=nexora,app.kubernetes.io/name=nexora-engine", "-o", "json")
		if err != nil {
			return err
		}
		return checkFailureControllers(controllers, snapshot)
	}
	if err = checkControllers(before); err != nil {
		t.Fatal(err)
	}
	var old ServingPod
	for _, p := range before.Pods {
		if p.Workload == target {
			old = p
		}
	}
	if old.UID == "" {
		t.Fatal("missing target")
	}
	var count atomic.Int64
	probe := func() error {
		if err := lock.Check(ctx); err != nil {
			return err
		}
		return CheckDNS(ctx, VIPDNSProbes(), func(s DNSSample) {
			count.Add(1)
			t.Logf("DNS %s %s started=%s duration=%s error=%v", s.Address, s.Transport, s.Started.Format(time.RFC3339Nano), s.Duration, s.Err)
		})
	}
	if err = probe(); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		for {
			if err := probe(); err != nil {
				done <- err
				cancel()
				return
			}
			select {
			case <-stop:
				done <- nil
				return
			case <-ctx.Done():
				done <- ctx.Err()
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	joined := false
	defer func() {
		if !joined {
			cancel()
			close(stop)
			<-done
		}
	}()
	if err = checkControllers(before); err != nil {
		t.Fatal(err)
	}
	if err = lock.Check(ctx); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "gracePeriodSeconds": 0, "preconditions": map[string]string{"uid": old.UID, "resourceVersion": old.ResourceVersion}})
	// UID/RV preconditions prevent deleting a replacement discovered after preflight.
	started := time.Now()
	t.Logf("abrupt pod deletion workload=%s pod=%s uid=%s identity=%s", target, old.Name, old.UID, old.EngineID)
	if _, err = kubectlCommand("kw", "nexora")(ctx, body, "delete", "--raw", "/api/v1/namespaces/nexora/pods/"+old.Name, "-f", "-"); err != nil {
		t.Fatal(err)
	}
	// Recovery observation may see an absent/not-ready replacement. It never
	// retries DNS; monitoring remains independent and cancels at its first error.
	recovered := false
	for ctx.Err() == nil {
		current, inspectErr := reader.Inspect(ctx)
		if inspectErr == nil {
			if err = checkFailureFleet(before, current, target); err != nil {
				t.Fatal(err)
			}
			replacement := false
			for _, p := range current.Pods {
				if p.Workload == target {
					replacement = p.UID != old.UID && p.Image == old.Image
				}
			}
			if replacement {
				if err = CheckFleetManagement(ctx, client, "https://nexora.kw.watteel.lab", current); err != nil {
					t.Fatal(err)
				}
				recovered = true
				break
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
	}
	if !recovered {
		t.Fatalf("replacement not recovered: %v", ctx.Err())
	}
	t.Logf("replacement identity preserved; recovery=%s", time.Since(started))
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-time.After(10 * time.Second):
	}
	// VIP success alone cannot certify the replacement or the other members.
	// Revalidate after the observation window while monitoring is still active.
	final, err := reader.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = checkFailureFleet(before, final, target); err != nil {
		t.Fatal(err)
	}
	for _, p := range final.Pods {
		if p.Workload == target && p.UID == old.UID {
			t.Fatal("original target returned instead of a replacement")
		}
	}
	if err = checkControllers(final); err != nil {
		t.Fatal(err)
	}
	if err = reader.CheckNodes(ctx, final); err != nil {
		t.Fatal(err)
	}
	if err = CheckFleetManagement(ctx, client, "https://nexora.kw.watteel.lab", final); err != nil {
		t.Fatal(err)
	}
	close(stop)
	err = <-done
	joined = true
	if err != nil {
		t.Fatal(err)
	}
	if err = probe(); err != nil {
		t.Fatal(err)
	}
	if err = lock.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err = lock.Release(ctx); err != nil {
		t.Fatal(err)
	}
	t.Log(fmt.Sprintf("member=%s successful samples=%d; pod-failure test only, not node isolation or zero-loss proof", target, count.Load()))
}

// Refuse a staged image change: OnDelete alone would make this failure test
// perform an upgrade as soon as it deletes the old pod.
func checkFailureControllers(data []byte, fleet FleetSnapshot) error {
	var list struct {
		Items []struct {
			Metadata kubeMetadata
			Spec     struct {
				UpdateStrategy struct{ Type string }
				Template       struct{ Spec enginePodSpec }
			}
		}
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	if len(list.Items) != 4 || len(fleet.Pods) != 4 {
		return fmt.Errorf("four frozen controllers and members required")
	}
	seen := map[string]bool{}
	for _, ds := range list.Items {
		if seen[ds.Metadata.Name] || ds.Metadata.Namespace != "nexora" || ds.Metadata.DeletionTimestamp != nil || ds.Spec.UpdateStrategy.Type != "OnDelete" {
			return fmt.Errorf("duplicate, deleting or unfrozen controller")
		}
		seen[ds.Metadata.Name] = true
		matched := false
		for _, p := range fleet.Pods {
			if p.Workload != ds.Metadata.Name {
				continue
			}
			matched = true
			engines := 0
			for _, c := range ds.Spec.Template.Spec.Containers {
				if c.Name == "engine" {
					engines++
					if c.Image == "" || c.Image != p.Image {
						return fmt.Errorf("failure test would change image for %s", p.Workload)
					}
				}
			}
			if engines != 1 {
				return fmt.Errorf("ambiguous engine template")
			}
		}
		if !matched {
			return fmt.Errorf("unknown controller %s", ds.Metadata.Name)
		}
	}
	return nil
}

func checkFailureFleet(before, after FleetSnapshot, target string) error {
	if after.LegacySelectors || len(after.Pods) != 4 {
		return fmt.Errorf("complete paired fleet required")
	}
	if err := CheckBoundFleet(before.Pods, after.Pods); err != nil {
		return err
	}
	for _, old := range before.Pods {
		for _, p := range after.Pods {
			if p.Workload == old.Workload && (p.Image != old.Image || (p.Workload != target && p.UID != old.UID)) {
				return fmt.Errorf("unexpected image change or partner replacement: %s", p.Workload)
			}
		}
	}
	return nil
}

func TestMemberFailureSafetyGuards(t *testing.T) {
	fixture := newFleetFixture(t, true, false)
	before, err := (&FleetReader{execute: fixture.execute}).Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	target := before.Pods[0].Workload
	for _, mode := range []string{"replacement", "legacy", "missing", "partner-replaced", "image-changed", "identity-changed"} {
		t.Run(mode, func(t *testing.T) {
			after := FleetSnapshot{Pods: append([]ServingPod(nil), before.Pods...)}
			after.Pods[0].UID = "replacement-uid"
			switch mode {
			case "legacy":
				after.LegacySelectors = true
			case "missing":
				after.Pods = after.Pods[:3]
			case "partner-replaced":
				after.Pods[1].UID = "unexpected-replacement"
			case "image-changed":
				after.Pods[0].Image = "unexpected-image"
			case "identity-changed":
				after.Pods[0].EngineID = "unexpected-identity"
			}
			err := checkFailureFleet(before, after, target)
			if (err == nil) != (mode == "replacement") {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
		})
	}
	for _, mode := range []string{"frozen", "rolling", "duplicate", "unknown", "staged-image", "missing-engine", "namespace", "deleting", "missing"} {
		t.Run(mode, func(t *testing.T) {
			var items []map[string]any
			for i, p := range before.Pods {
				name, namespace, strategy, image, container := p.Workload, "nexora", "OnDelete", p.Image, "engine"
				if i == 0 {
					switch mode {
					case "rolling":
						strategy = "RollingUpdate"
					case "duplicate":
						name = before.Pods[1].Workload
					case "unknown":
						name = "other-engine"
					case "staged-image":
						image = "pending-upgrade"
					case "missing-engine":
						container = "sidecar"
					case "namespace":
						namespace = "other-release"
					}
				}
				metadata := map[string]any{"name": name, "namespace": namespace}
				if i == 0 && mode == "deleting" {
					metadata["deletionTimestamp"] = "2026-09-17T00:00:00Z"
				}
				items = append(items, map[string]any{"metadata": metadata, "spec": map[string]any{
					"updateStrategy": map[string]string{"type": strategy},
					"template":       map[string]any{"spec": map[string]any{"containers": []map[string]string{{"name": container, "image": image}}}},
				}})
			}
			if mode == "missing" {
				items = items[:3]
			}
			data, err := json.Marshal(map[string]any{"items": items})
			if err != nil {
				t.Fatal(err)
			}
			err = checkFailureControllers(data, before)
			if (err == nil) != (mode == "frozen") {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
		})
	}
	if checkFailureControllers([]byte("invalid JSON"), before) == nil {
		t.Fatal("invalid controller response accepted")
	}
}
