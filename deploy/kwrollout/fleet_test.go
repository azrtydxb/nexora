package kwrollout

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"
)

func TestFleetDiscoveryAndBinding(t *testing.T) {
	for _, setup := range []struct{ four, legacy bool }{{false, true}, {true, true}, {true, false}} {
		f := newFleetFixture(t, setup.four, setup.legacy)
		got, err := (&FleetReader{execute: f.execute}).Inspect(context.Background())
		if err != nil || got.LegacySelectors != setup.legacy || len(got.Pods) != len(f.members) {
			t.Fatalf("setup=%+v fleet=%+v err=%v", setup, got, err)
		}
		if err := CheckBoundFleet(got.Pods, got.Pods); err != nil {
			t.Fatal(err)
		}
		changed := append([]ServingPod(nil), got.Pods...)
		changed[0].EngineID = "replacement-id"
		if CheckBoundFleet(got.Pods, changed) == nil {
			t.Fatal("identity replacement accepted")
		}
		changed = append([]ServingPod(nil), got.Pods...)
		changed[0].UID = "new-pod-same-engine"
		if err := CheckBoundFleet(got.Pods, changed); err != nil {
			t.Fatalf("legitimate persistent-identity replacement rejected: %v", err)
		}
	}
}

func TestFleetRejectsAmbiguousAndSharedState(t *testing.T) {
	for name, mutate := range map[string]func(*fleetFixture){
		"partial":   func(f *fleetFixture) { delete(f.members, "d") },
		"extra":     func(f *fleetFixture) { f.members["x"] = workloadFixture(t) },
		"shared-id": func(f *fleetFixture) { f.members["d"].identity = f.members["c"].identity },
		"shared-state": func(f *fleetFixture) {
			f.members["d"].pods[0].Spec.Volumes[0].HostPath.Path = f.members["c"].pods[0].Spec.Volumes[0].HostPath.Path
		},
		"mixed-selectors": func(f *fleetFixture) {
			delete(f.services["nexora-dns"].service.Spec.Selector, "nexora.io/engine-instance")
			f.services["nexora-dns"].service.Spec.Selector["nexora.io/failover-pair"] = "dns136"
		},
		"shared-node-name": func(f *fleetFixture) { f.members["d"].pods[0].Spec.Containers[0].Env[1].Value = "c-$(K8S_NODE_NAME)" },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFleetFixture(t, true, true)
			mutate(f)
			if _, err := (&FleetReader{execute: f.execute}).Inspect(context.Background()); err == nil {
				t.Fatal("unsafe fleet accepted")
			}
		})
	}
}

func TestEnrolmentUsesCASAndOwnershipPerPod(t *testing.T) {
	for _, mode := range []string{"ok", "stale-rv", "replaced-uid", "ownership-loss", "foreign-label", "foreign-candidate"} {
		t.Run(mode, func(t *testing.T) {
			f := newFleetFixture(t, true, true)
			r := &FleetReader{execute: f.execute}
			snapshot, err := r.Inspect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "stale-rv":
				f.beforePatch = func(p *enginePod) { p.Metadata.ResourceVersion = "changed" }
			case "replaced-uid":
				f.beforePatch = func(p *enginePod) { p.Metadata.UID = "replaced" }
			case "foreign-label":
				f.members["a"].pods[0].Metadata.Labels["nexora.io/failover-pair"] = "other"
			case "foreign-candidate":
				f.members["d"].pods[0].Metadata.Labels["nexora.io/failover-pair"] = "dns136"
			}
			checks := 0
			err = r.enrollPairs(context.Background(), snapshot, []string{"nexora-engine-a", "nexora-engine-b"}, func(context.Context) error {
				checks++
				if mode == "ownership-loss" && checks == 2 {
					return fmt.Errorf("ownership lost")
				}
				return nil
			})
			if mode == "ok" {
				if err != nil || checks != 2 || f.patches != 2 {
					t.Fatalf("err=%v checks=%d patches=%d", err, checks, f.patches)
				}
			} else if err == nil {
				t.Fatal("unsafe enrolment accepted")
			}
			if mode == "ownership-loss" && f.patches != 1 {
				t.Fatal("patched second anchor after losing ownership")
			}
			if (mode == "stale-rv" || mode == "replaced-uid") && f.patches != 0 {
				t.Fatal("CAS failed to protect anchor")
			}
		})
	}
}

type fleetFixture struct {
	members     map[string]*workloadFake
	services    map[string]*endpointFake
	beforePatch func(*enginePod)
	patches     int
}

func newFleetFixture(t *testing.T, four, legacy bool) *fleetFixture {
	t.Helper()
	f := &fleetFixture{members: map[string]*workloadFake{}, services: map[string]*endpointFake{}}
	for i, pair := range KWPairTopology() {
		for j, member := range pair.Members {
			if j == 1 && !four {
				continue
			}
			instance := strings.TrimPrefix(member.Workload, "nexora-engine-")
			w := workloadFixture(t)
			w.ds.Metadata.Name, w.ds.Metadata.UID = member.Workload, "ds-"+instance
			w.ds.Spec.Selector.MatchLabels["nexora.io/engine-instance"] = instance
			p := &w.pods[0]
			p.Metadata.Name, p.Metadata.UID = "pod-"+instance, "uid-"+instance
			p.Metadata.Labels["nexora.io/engine-instance"] = instance
			p.Metadata.OwnerReferences[0].Name, p.Metadata.OwnerReferences[0].UID = member.Workload, "ds-"+instance
			p.Spec.NodeName, p.Status.PodIP = member.Node, fmt.Sprintf("10.42.0.%d", int(instance[0]-'a')+1)
			w.ds.Spec.Selector.MatchLabels = maps.Clone(p.Metadata.Labels)
			if j == 1 || !legacy {
				p.Metadata.Labels["nexora.io/failover-pair"] = pair.Name
				p.Metadata.Labels = map[string]string{"app.kubernetes.io/name": "nexora-engine", "app.kubernetes.io/instance": "nexora", "nexora.io/engine-group": "default", "nexora.io/engine-instance": instance, "nexora.io/failover-pair": pair.Name}
			}
			for _, spec := range []*enginePodSpec{&p.Spec, &w.ds.Spec.Template.Spec} {
				spec.Volumes[0].HostPath.Path = kwExpectation(member, "", false).StatePath
				if j == 1 {
					spec.Containers[0].Env[1].Value = instance + "-$(K8S_NODE_NAME)"
				}
			}
			w.identity = fmt.Sprintf("00000000-0000-0000-0000-%012d", int(instance[0]-'a')+1)
			f.members[instance] = w
		}
		e := endpointFixture(t, legacy)
		name := "nexora-dns"
		if i == 1 {
			name += "-2"
		}
		e.service.Metadata.Name = name
		e.service.Spec.LoadBalancerIP = pair.Address
		e.service.Status.LoadBalancer.Ingress[0].IP = pair.Address
		if legacy {
			e.service.Spec.Selector["nexora.io/engine-instance"] = strings.TrimPrefix(pair.Members[0].Workload, "nexora-engine-")
		} else {
			e.service.Spec.Selector["nexora.io/failover-pair"] = pair.Name
		}
		e.slice.Metadata.Labels["kubernetes.io/service-name"] = name
		e.slice.Metadata.OwnerReferences[0].Name = name
		for j := range e.slice.Endpoints {
			p := f.members[strings.TrimPrefix(pair.Members[j].Workload, "nexora-engine-")].pods[0]
			endpoint := &e.slice.Endpoints[j]
			endpoint.TargetRef.Name, endpoint.TargetRef.UID = p.Metadata.Name, p.Metadata.UID
			endpoint.NodeName, endpoint.Addresses = p.Spec.NodeName, []string{p.Status.PodIP}
		}
		f.services[name] = e
	}
	return f
}

func (f *fleetFixture) execute(_ context.Context, input []byte, args ...string) ([]byte, error) {
	if args[0] == "exec" {
		return []byte(f.members[strings.TrimPrefix(args[1], "pod-")].identity), nil
	}
	if args[0] == "patch" {
		p := &f.members[strings.TrimPrefix(args[2], "pod-")].pods[0]
		if f.beforePatch != nil {
			f.beforePatch(p)
		}
		var patch []map[string]string
		if err := json.Unmarshal(input, &patch); err != nil {
			return nil, err
		}
		if len(patch) != 3 || patch[0]["path"] != "/metadata/uid" || patch[0]["value"] != p.Metadata.UID || patch[1]["path"] != "/metadata/resourceVersion" || patch[1]["value"] != p.Metadata.ResourceVersion || patch[2]["path"] != "/metadata/labels/nexora.io~1failover-pair" {
			return nil, fmt.Errorf("CAS rejected")
		}
		p.Metadata.Labels["nexora.io/failover-pair"] = patch[2]["value"]
		p.Metadata.ResourceVersion = "patched"
		f.patches++
		return json.Marshal(p)
	}
	if args[0] != "get" {
		return nil, fmt.Errorf("unexpected mutation")
	}
	switch args[1] {
	case "daemonsets":
		var items []engineDaemonSet
		for _, m := range f.members {
			items = append(items, m.ds)
		}
		return json.Marshal(struct{ Items []engineDaemonSet }{items})
	case "daemonset":
		return json.Marshal(f.members[strings.TrimPrefix(args[2], "nexora-engine-")].ds)
	case "pod":
		return json.Marshal(f.members[strings.TrimPrefix(args[2], "pod-")].pods[0])
	case "pods":
		var items []enginePod
		for _, m := range f.members {
			p := m.pods[0]
			match := true
			for _, selector := range strings.Split(args[3], ",") {
				key, value, _ := strings.Cut(selector, "=")
				match = match && p.Metadata.Labels[key] == value
			}
			if match {
				items = append(items, p)
			}
		}
		return json.Marshal(struct{ Items []enginePod }{items})
	case "service":
		return json.Marshal(f.services[args[2]].service)
	case "endpointslices":
		name := strings.TrimPrefix(args[3], "kubernetes.io/service-name=")
		return json.Marshal(struct{ Items []dnsEndpointSlice }{[]dnsEndpointSlice{f.services[name].slice}})
	}
	return nil, fmt.Errorf("unexpected command %v", args)
}
