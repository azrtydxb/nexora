package kwrollout

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// ServiceExpectation binds a client VIP to already verified serving pod UIDs.
// LegacyInstance is nonempty only during the initial single-member topology.
type ServiceExpectation struct {
	Name, Address, Pair, LegacyInstance string
	Pods                                []ServingPod
}

type dnsService struct {
	Kind     string
	Metadata kubeMetadata
	Spec     struct {
		Type, LoadBalancerIP, ExternalTrafficPolicy string
		PublishNotReadyAddresses                    bool
		Selector                                    map[string]string
		Ports                                       []struct {
			Name, Protocol string
			Port           int
			TargetPort     string
		}
	}
	Status struct {
		LoadBalancer struct{ Ingress []struct{ IP string } }
	}
}

type dnsEndpointSlice struct {
	Metadata    kubeMetadata
	AddressType string
	Ports       []struct {
		Name, Protocol string
		Port           int
	}
	Endpoints []struct {
		Addresses  []string
		NodeName   string
		TargetRef  struct{ Kind, Name, Namespace, UID string }
		Conditions struct{ Ready, Serving, Terminating *bool }
	}
}

// CheckServiceEndpoints verifies selectors AND actual endpoint ownership, pod
// identity, address, node, readiness and all five kw DNS transport port mappings.
func CheckServiceEndpoints(ctx context.Context, kubeContext, namespace, release string, expected ServiceExpectation) error {
	return checkServiceEndpoints(ctx, kubectlCommand(kubeContext, namespace), namespace, release, expected)
}

func checkServiceEndpoints(ctx context.Context, execute func(context.Context, []byte, ...string) ([]byte, error), namespace, release string, expected ServiceExpectation) error {
	if namespace == "" || release == "" || expected.Name == "" || expected.Pair == "" || (expected.Address != "192.168.10.136" && expected.Address != "192.168.10.139") {
		return fmt.Errorf("invalid kw Service expectation")
	}
	wantCount := 2
	selector := map[string]string{"app.kubernetes.io/name": "nexora-engine", "app.kubernetes.io/instance": release, "nexora.io/engine-group": "default"}
	if expected.LegacyInstance != "" {
		wantCount = 1
		selector["nexora.io/engine-instance"] = expected.LegacyInstance
	} else {
		selector["nexora.io/failover-pair"] = expected.Pair
	}
	if len(expected.Pods) != wantCount {
		return fmt.Errorf("unexpected number of serving members for VIP")
	}
	pods, nodes, identities := map[string]ServingPod{}, map[string]bool{}, map[string]bool{}
	for _, p := range expected.Pods {
		if p.UID == "" || p.Name == "" || p.Node == "" || p.IP == "" || p.EngineID == "" || pods[p.UID].UID != "" || nodes[p.Node] || identities[p.EngineID] {
			return fmt.Errorf("VIP members must have distinct verified pod identities and nodes")
		}
		pods[p.UID], nodes[p.Node], identities[p.EngineID] = p, true, true
	}
	get := func(out any, args ...string) error {
		data, err := execute(ctx, nil, args...)
		if err != nil {
			return err
		}
		return json.Unmarshal(data, out)
	}
	var service dnsService
	if err := get(&service, "get", "service", expected.Name, "-o", "json"); err != nil {
		return err
	}
	if service.Kind != "Service" || service.Metadata.Namespace != namespace || service.Metadata.Name != expected.Name || service.Metadata.UID == "" || service.Metadata.DeletionTimestamp != nil || service.Spec.Type != "LoadBalancer" || service.Spec.LoadBalancerIP != expected.Address || service.Spec.ExternalTrafficPolicy != "Local" || service.Spec.PublishNotReadyAddresses || !reflect.DeepEqual(service.Spec.Selector, selector) {
		return fmt.Errorf("VIP Service identity, selector or source-IP policy differs from expectation")
	}
	if len(service.Status.LoadBalancer.Ingress) != 1 || service.Status.LoadBalancer.Ingress[0].IP != expected.Address {
		return fmt.Errorf("VIP has not been assigned exactly its configured address")
	}
	ports := map[string]struct {
		protocol string
		port     int
	}{"dns-udp": {"UDP", 53}, "dns-tcp": {"TCP", 53}, "dot": {"TCP", 853}, "doq": {"UDP", 853}, "doh": {"TCP", 443}}
	servicePorts := map[string]bool{}
	for _, p := range service.Spec.Ports {
		want, ok := ports[p.Name]
		if !ok || servicePorts[p.Name] || p.Protocol != want.protocol || p.Port != want.port || p.TargetPort != p.Name {
			return fmt.Errorf("VIP Service port mapping differs from kw DNS contract")
		}
		servicePorts[p.Name] = true
	}
	if len(servicePorts) != len(ports) {
		return fmt.Errorf("VIP Service is missing a DNS transport")
	}
	var slices struct{ Items []dnsEndpointSlice }
	if err := get(&slices, "get", "endpointslices", "-l", "kubernetes.io/service-name="+expected.Name, "-o", "json"); err != nil {
		return err
	}
	seen := map[string]map[string]bool{}
	for _, slice := range slices.Items {
		owned := false
		for _, owner := range slice.Metadata.OwnerReferences {
			if owner.Controller && owner.Kind == "Service" && owner.Name == expected.Name && owner.UID == service.Metadata.UID {
				owned = true
			}
		}
		if !owned || slice.Metadata.Namespace != namespace || slice.Metadata.DeletionTimestamp != nil || slice.Metadata.Labels["kubernetes.io/service-name"] != expected.Name || slice.AddressType != "IPv4" || len(slice.Ports) == 0 || len(slice.Endpoints) == 0 {
			return fmt.Errorf("EndpointSlice is not owned by the expected IPv4 VIP Service")
		}
		for _, port := range slice.Ports {
			want, ok := ports[port.Name]
			if !ok || port.Protocol != want.protocol || port.Port != want.port {
				return fmt.Errorf("EndpointSlice contains an unexpected DNS port")
			}
			if seen[port.Name] == nil {
				seen[port.Name] = map[string]bool{}
			}
			for _, endpoint := range slice.Endpoints {
				p, ok := pods[endpoint.TargetRef.UID]
				if !ok || endpoint.TargetRef.Kind != "Pod" || endpoint.TargetRef.Namespace != namespace || endpoint.TargetRef.Name != p.Name || endpoint.NodeName != p.Node || len(endpoint.Addresses) != 1 || endpoint.Addresses[0] != p.IP {
					return fmt.Errorf("VIP endpoint is missing, stale or belongs to another pair")
				}
				if endpoint.Conditions.Ready == nil || !*endpoint.Conditions.Ready || (endpoint.Conditions.Serving != nil && !*endpoint.Conditions.Serving) || (endpoint.Conditions.Terminating != nil && *endpoint.Conditions.Terminating) || seen[port.Name][p.UID] {
					return fmt.Errorf("VIP endpoint is unready, terminating or duplicated")
				}
				seen[port.Name][p.UID] = true
			}
		}
	}
	for name := range ports {
		if len(seen[name]) != len(pods) {
			return fmt.Errorf("VIP transport %s does not expose every verified member", name)
		}
	}
	// Reject replacement/selector changes between Service and EndpointSlice reads.
	var after dnsService
	if err := get(&after, "get", "service", expected.Name, "-o", "json"); err != nil {
		return err
	}
	if !reflect.DeepEqual(service, after) {
		return fmt.Errorf("VIP Service changed during endpoint observation")
	}
	return ctx.Err()
}
