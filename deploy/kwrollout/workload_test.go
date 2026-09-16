package kwrollout

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
)

func TestReadServingPod(t *testing.T) {
	f := workloadFixture(t)
	got, err := readServingPod(context.Background(), f.execute, "nexora", "nexora", f.expected)
	if err != nil {
		t.Fatal(err)
	}
	if got.EngineID != f.identity || got.UID != "pod-uid" || got.EngineNodeName != "master-12" || got.StatePath != f.expected.StatePath {
		t.Fatalf("incorrect identity binding: %+v", got)
	}
}

func TestReadServingPodIgnoresJSONIndentation(t *testing.T) {
	f := workloadFixture(t)
	execute := func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
		out, err := f.execute(ctx, input, args...)
		if err == nil && args[0] == "get" && args[1] == "pod" {
			var indented bytes.Buffer
			if err := json.Indent(&indented, out, "", "    "); err != nil {
				return nil, err
			}
			return indented.Bytes(), nil
		}
		return out, err
	}
	if _, err := readServingPod(context.Background(), execute, "nexora", "nexora", f.expected); err != nil {
		t.Fatal(err)
	}
}

func TestReadServingPodRejectsUnsafeObservations(t *testing.T) {
	for name, mutate := range map[string]func(*workloadFake){
		"wrong-node":      func(f *workloadFake) { f.pods[0].Spec.NodeName = "master-11" },
		"wrong-owner":     func(f *workloadFake) { f.pods[0].Metadata.OwnerReferences[0].UID = "other" },
		"wrong-image":     func(f *workloadFake) { f.pods[0].Spec.Containers[0].Image = "old" },
		"missing-id":      func(f *workloadFake) { f.identity = "" },
		"invalid-id":      func(f *workloadFake) { f.identity = "not-a-uuid" },
		"missing-pod":     func(f *workloadFake) { f.pods = nil },
		"surge-active":    func(f *workloadFake) { f.pods = append(f.pods, f.pods[0]) },
		"not-ready":       func(f *workloadFake) { f.pods[0].Status.Conditions[0].Status = "False" },
		"container-down":  func(f *workloadFake) { f.pods[0].Status.ContainerStatuses[0].Ready = false },
		"old-generation":  func(f *workloadFake) { f.ds.Status.ObservedGeneration-- },
		"not-available":   func(f *workloadFake) { f.ds.Status.NumberAvailable = 0 },
		"not-updated":     func(f *workloadFake) { f.ds.Status.UpdatedNumberScheduled = 0 },
		"wrong-state":     func(f *workloadFake) { f.pods[0].Spec.Volumes[0].HostPath.Path = "/other" },
		"wrong-expansion": func(f *workloadFake) { f.pods[0].Spec.Containers[0].Env[1].Value = "fixed-name" },
		"replaced-during-read": func(f *workloadFake) {
			f.after = func(p *enginePod) { p.Metadata.UID = "replacement" }
		},
		"restarted-during-read": func(f *workloadFake) {
			f.after = func(p *enginePod) { p.Status.ContainerStatuses[0].ContainerID = "restarted-container" }
		},
		"terminating": func(f *workloadFake) {
			stamp := "2026-09-16T00:00:00Z"
			f.pods[0].Metadata.DeletionTimestamp = &stamp
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := workloadFixture(t)
			mutate(f)
			if _, err := readServingPod(context.Background(), f.execute, "nexora", "nexora", f.expected); err == nil {
				t.Fatal("unsafe observation passed")
			}
		})
	}
}

func TestReadServingPodAllowsHealthyFrozenPreviousImage(t *testing.T) {
	f := workloadFixture(t)
	f.expected.RequireUpdated = false
	f.expected.Image = "next-image"
	f.ds.Status.UpdatedNumberScheduled = 0
	f.ds.Spec.Template.Spec.Containers[0].Image = "next-image"
	if _, err := readServingPod(context.Background(), f.execute, "nexora", "nexora", f.expected); err != nil {
		t.Fatal(err)
	}
}

// Explicit opt-in, read-only production check: reads only the public engine UUID.
func TestReadServingPodLive(t *testing.T) {
	if os.Getenv("NEXORA_KW_WORKLOAD_TEST") != "1" {
		t.Skip("set NEXORA_KW_WORKLOAD_TEST=1 and NEXORA_KW_EXPECT_IMAGE for read-only kw verification")
	}
	image := os.Getenv("NEXORA_KW_EXPECT_IMAGE")
	if image == "" {
		t.Fatal("expected image must be supplied independently of observed pods")
	}
	ids := map[string]bool{}
	for _, m := range []Member{{Workload: "nexora-engine-a", Node: "master-12"}, {Workload: "nexora-engine-b", Node: "master-13"}} {
		p, err := ReadServingPod(context.Background(), "kw", "nexora", "nexora", WorkloadExpectation{Member: m, StatePath: "/var/lib/nexora/nexora-engine", Image: image, RequireUpdated: true})
		if err != nil {
			t.Fatal(err)
		}
		if ids[p.EngineID] {
			t.Fatal("live workloads share an engine identity")
		}
		ids[p.EngineID] = true
		service := ServiceExpectation{Name: "nexora-dns", Address: "192.168.10.136", Pair: "dns136", LegacyInstance: "a", Pods: []ServingPod{p}}
		if m.Workload == "nexora-engine-b" {
			service.Name, service.Address, service.Pair, service.LegacyInstance = "nexora-dns-2", "192.168.10.139", "dns139", "b"
		}
		if err := CheckServiceEndpoints(context.Background(), "kw", "nexora", "nexora", service); err != nil {
			t.Fatal(err)
		}
		t.Logf("verified %s on %s, stable pod/public UUID and VIP endpoints", p.Workload, p.Node)
	}
}

type workloadFake struct {
	ds       engineDaemonSet
	pods     []enginePod
	identity string
	expected WorkloadExpectation
	after    func(*enginePod)
}

func (f *workloadFake) execute(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	if args[0] == "exec" {
		want := []string{"exec", "pod-a", "-c", "engine", "--", "head", "-c", "128", "/var/lib/nexora/identity/engine_id"}
		if !reflect.DeepEqual(args, want) {
			return nil, fmt.Errorf("unexpected container command: %v", args)
		}
		return []byte(f.identity), nil
	}
	switch args[1] {
	case "daemonset":
		return json.Marshal(f.ds)
	case "pods":
		return json.Marshal(struct{ Items []enginePod }{f.pods})
	case "pod":
		p := f.pods[0]
		if f.after != nil {
			f.after(&p)
		}
		return json.Marshal(p)
	}
	return nil, fmt.Errorf("unexpected read %v", args)
}

func workloadFixture(t *testing.T) *workloadFake {
	t.Helper()
	const podJSON = `{
		"kind":"Pod","metadata":{"name":"pod-a","namespace":"nexora","uid":"pod-uid","resourceVersion":"1",
		"labels":{"app.kubernetes.io/name":"nexora-engine","app.kubernetes.io/instance":"nexora","nexora.io/engine-group":"default","nexora.io/engine-instance":"a"},
		"ownerReferences":[{"kind":"DaemonSet","name":"nexora-engine-a","uid":"ds-uid","controller":true}]},
		"spec":{"nodeName":"master-12","containers":[{"name":"engine","image":"image:expected",
		"env":[{"name":"K8S_NODE_NAME","valueFrom":{"fieldRef":{"fieldPath":"spec.nodeName"}}},{"name":"NEXORA_ENGINE_NODE_NAME","value":"$(K8S_NODE_NAME)"}],
		"volumeMounts":[{"name":"state","mountPath":"/var/lib/nexora"}]}],
		"volumes":[{"name":"state","hostPath":{"path":"/var/lib/nexora/nexora-engine"}}]},
		"status":{"phase":"Running","podIP":"10.42.0.10","conditions":[{"type":"Ready","status":"True"}],
		"containerStatuses":[{"name":"engine","ready":true,"imageID":"sha256:expected","containerID":"container-a","state":{"running":{"startedAt":"2026-09-16T00:00:00Z"}}}]}}
	`
	var p enginePod
	if err := json.Unmarshal([]byte(podJSON), &p); err != nil {
		t.Fatal(err)
	}
	f := &workloadFake{pods: []enginePod{p}, identity: "bbd7b9cc-0b07-4f6f-9355-04737c4fd8eb", expected: WorkloadExpectation{Member: Member{Workload: "nexora-engine-a", Node: "master-12"}, StatePath: "/var/lib/nexora/nexora-engine", Image: "image:expected", RequireUpdated: true}}
	f.ds.Kind = "DaemonSet"
	f.ds.Metadata = kubeMetadata{Name: "nexora-engine-a", Namespace: "nexora", UID: "ds-uid", Generation: 1}
	f.ds.Spec.Selector.MatchLabels = p.Metadata.Labels
	// Decode again to avoid aliasing mutable pod/template slices in failure tests.
	var template enginePod
	if err := json.Unmarshal([]byte(podJSON), &template); err != nil {
		t.Fatal(err)
	}
	f.ds.Spec.Template.Spec = template.Spec
	f.ds.Status.ObservedGeneration = 1
	f.ds.Status.DesiredNumberScheduled = 1
	f.ds.Status.CurrentNumberScheduled = 1
	f.ds.Status.NumberReady = 1
	f.ds.Status.NumberAvailable = 1
	f.ds.Status.UpdatedNumberScheduled = 1
	return f
}
