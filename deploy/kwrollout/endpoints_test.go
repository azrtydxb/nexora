package kwrollout

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestServiceEndpointsLegacyAndPaired(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		f := endpointFixture(t, legacy)
		if err := checkServiceEndpoints(context.Background(), f.execute, "nexora", "nexora", f.expected); err != nil {
			t.Fatalf("legacy=%v: %v", legacy, err)
		}
	}
}

func TestServiceEndpointsRejectsUnsafeTopology(t *testing.T) {
	for name, mutate := range map[string]func(*endpointFake){
		"wrong-vip":      func(f *endpointFake) { f.service.Spec.LoadBalancerIP = "192.168.10.140" },
		"wrong-selector": func(f *endpointFake) { f.service.Spec.Selector["nexora.io/failover-pair"] = "dns139" },
		"source-ip":      func(f *endpointFake) { f.service.Spec.ExternalTrafficPolicy = "Cluster" },
		"missing-port":   func(f *endpointFake) { f.service.Spec.Ports = f.service.Spec.Ports[1:] },
		"cross-pair":     func(f *endpointFake) { f.slice.Endpoints[0].TargetRef.UID = "foreign-pod" },
		"stale-owner":    func(f *endpointFake) { f.slice.Metadata.OwnerReferences[0].UID = "previous-service" },
		"wrong-address":  func(f *endpointFake) { f.slice.Endpoints[0].Addresses[0] = "10.42.99.1" },
		"wrong-node":     func(f *endpointFake) { f.slice.Endpoints[0].NodeName = "master-13" },
		"missing-ready":  func(f *endpointFake) { f.slice.Endpoints[0].Conditions.Ready = nil },
		"not-ready":      func(f *endpointFake) { value := false; f.slice.Endpoints[0].Conditions.Ready = &value },
		"terminating":    func(f *endpointFake) { value := true; f.slice.Endpoints[0].Conditions.Terminating = &value },
		"missing-member": func(f *endpointFake) { f.slice.Endpoints = f.slice.Endpoints[1:] },
		"duplicate":      func(f *endpointFake) { f.slice.Endpoints = append(f.slice.Endpoints, f.slice.Endpoints[0]) },
		"empty-ports":    func(f *endpointFake) { f.slice.Ports = nil },
		"colocated":      func(f *endpointFake) { f.expected.Pods[1].Node = f.expected.Pods[0].Node },
		"shared-id":      func(f *endpointFake) { f.expected.Pods[1].EngineID = f.expected.Pods[0].EngineID },
		"service-changed": func(f *endpointFake) {
			f.changeAfter = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := endpointFixture(t, false)
			mutate(f)
			if err := checkServiceEndpoints(context.Background(), f.execute, "nexora", "nexora", f.expected); err == nil {
				t.Fatal("unsafe endpoint topology accepted")
			}
		})
	}
}

type endpointFake struct {
	service      dnsService
	slice        dnsEndpointSlice
	expected     ServiceExpectation
	serviceReads int
	changeAfter  bool
}

func (f *endpointFake) execute(_ context.Context, _ []byte, args ...string) ([]byte, error) {
	if args[0] != "get" {
		return nil, fmt.Errorf("unexpected mutation: %v", args)
	}
	switch args[1] {
	case "service":
		f.serviceReads++
		if f.changeAfter && f.serviceReads == 2 {
			f.service.Metadata.UID = "new-service"
		}
		return json.Marshal(f.service)
	case "endpointslices":
		return json.Marshal(struct{ Items []dnsEndpointSlice }{[]dnsEndpointSlice{f.slice}})
	}
	return nil, fmt.Errorf("unexpected read: %v", args)
}

func endpointFixture(t *testing.T, legacy bool) *endpointFake {
	t.Helper()
	f := &endpointFake{expected: ServiceExpectation{Name: "nexora-dns", Address: "192.168.10.136", Pair: "dns136", Pods: []ServingPod{
		{Name: "pod-a", UID: "uid-a", Node: "master-12", IP: "10.42.0.10", EngineID: "engine-a"},
		{Name: "pod-c", UID: "uid-c", Node: "master-11", IP: "10.42.2.10", EngineID: "engine-c"},
	}}}
	const serviceJSON = `{"kind":"Service","metadata":{"name":"nexora-dns","namespace":"nexora","uid":"service-uid"},
	"spec":{"type":"LoadBalancer","loadBalancerIP":"192.168.10.136","externalTrafficPolicy":"Local",
	"selector":{"app.kubernetes.io/name":"nexora-engine","app.kubernetes.io/instance":"nexora","nexora.io/engine-group":"default","nexora.io/failover-pair":"dns136"},
	"ports":[{"name":"dns-udp","port":53,"targetPort":"dns-udp","protocol":"UDP"},{"name":"dns-tcp","port":53,"targetPort":"dns-tcp","protocol":"TCP"},
	{"name":"dot","port":853,"targetPort":"dot","protocol":"TCP"},{"name":"doq","port":853,"targetPort":"doq","protocol":"UDP"},{"name":"doh","port":443,"targetPort":"doh","protocol":"TCP"}]},
	"status":{"loadBalancer":{"ingress":[{"ip":"192.168.10.136"}]}}}`
	if err := json.Unmarshal([]byte(serviceJSON), &f.service); err != nil {
		t.Fatal(err)
	}
	const sliceJSON = `{"metadata":{"name":"slice","namespace":"nexora","labels":{"kubernetes.io/service-name":"nexora-dns"},
	"ownerReferences":[{"kind":"Service","name":"nexora-dns","uid":"service-uid","controller":true}]},"addressType":"IPv4",
	"ports":[{"name":"dns-udp","port":53,"protocol":"UDP"},{"name":"dns-tcp","port":53,"protocol":"TCP"},{"name":"dot","port":853,"protocol":"TCP"},{"name":"doq","port":853,"protocol":"UDP"},{"name":"doh","port":443,"protocol":"TCP"}],
	"endpoints":[{"addresses":["10.42.0.10"],"nodeName":"master-12","targetRef":{"kind":"Pod","name":"pod-a","namespace":"nexora","uid":"uid-a"},"conditions":{"ready":true,"serving":true,"terminating":false}},
	{"addresses":["10.42.2.10"],"nodeName":"master-11","targetRef":{"kind":"Pod","name":"pod-c","namespace":"nexora","uid":"uid-c"},"conditions":{"ready":true,"serving":true,"terminating":false}}]}`
	if err := json.Unmarshal([]byte(sliceJSON), &f.slice); err != nil {
		t.Fatal(err)
	}
	if legacy {
		f.expected.LegacyInstance = "a"
		f.expected.Pods = f.expected.Pods[:1]
		f.slice.Endpoints = f.slice.Endpoints[:1]
		delete(f.service.Spec.Selector, "nexora.io/failover-pair")
		f.service.Spec.Selector["nexora.io/engine-instance"] = "a"
	}
	return f
}
