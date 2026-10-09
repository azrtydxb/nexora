package kwrollout

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const ciliumL2Class = "io.cilium/l2-announcer"

type labelSelector struct {
	MatchLabels      map[string]string
	MatchExpressions []struct {
		Key, Operator string
		Values        []string
	}
}

// matches implements Kubernetes label-selector semantics. An empty selector
// matches everything; an unknown operator matches nothing (fail closed).
func (s labelSelector) matches(labels map[string]string) bool {
	for k, v := range s.MatchLabels {
		if got, ok := labels[k]; !ok || got != v {
			return false
		}
	}
	for _, e := range s.MatchExpressions {
		got, ok := labels[e.Key]
		in := false
		for _, v := range e.Values {
			in = in || ok && got == v
		}
		switch e.Operator {
		case "In":
			if !in {
				return false
			}
		case "NotIn":
			if in {
				return false
			}
		case "Exists":
			if !ok {
				return false
			}
		case "DoesNotExist":
			if ok {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// checkL2Announcers verifies, for each VIP in the kw topology, that a Cilium L2
// announcer can actually serve it from every node that hosts one of its engines
// (externalTrafficPolicy Local only delivers on nodes with a local engine):
//   - the LoadBalancer Service pinned to the VIP uses the Cilium L2 class and
//     externalTrafficPolicy Local;
//   - the union of CiliumL2AnnouncementPolicies selecting that Service (with
//     loadBalancerIPs enabled) selects every engine node;
//   - the Service's L2 announcement lease exists and is held by a node that such
//     a policy selects.
func (r *FleetReader) checkL2Announcers(ctx context.Context, nodeLabels map[string]map[string]string) error {
	var services struct {
		Items []struct {
			Metadata kubeMetadata
			Spec     struct {
				Type, LoadBalancerClass, ExternalTrafficPolicy string
			}
		}
	}
	var annotated struct {
		Items []struct {
			Metadata struct {
				Name        string
				Annotations map[string]string
			}
		}
	}
	data, err := r.execute(ctx, nil, "get", "services", "-o", "json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &services); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &annotated); err != nil {
		return err
	}
	var policies struct {
		Items []struct {
			Metadata kubeMetadata
			Spec     struct {
				NodeSelector    *labelSelector
				ServiceSelector *labelSelector
				LoadBalancerIPs bool
			}
		}
	}
	if data, err = r.execute(ctx, nil, "get", "ciliuml2announcementpolicies", "-o", "json"); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &policies); err != nil {
		return err
	}
	for _, pair := range KWPairTopology() {
		found := -1
		for i, s := range annotated.Items {
			if s.Metadata.Annotations["lbipam.cilium.io/ips"] == pair.Address {
				if found >= 0 {
					return fmt.Errorf("VIP %s is pinned by more than one Service", pair.Address)
				}
				found = i
			}
		}
		if found < 0 {
			return fmt.Errorf("no Service pins VIP %s", pair.Address)
		}
		svc := services.Items[found]
		if svc.Spec.Type != "LoadBalancer" || svc.Spec.LoadBalancerClass != ciliumL2Class || svc.Spec.ExternalTrafficPolicy != "Local" {
			return fmt.Errorf("Service %s for VIP %s must be a Local LoadBalancer of class %s", svc.Metadata.Name, pair.Address, ciliumL2Class)
		}
		selected := map[string]bool{}
		for _, p := range policies.Items {
			if !p.Spec.LoadBalancerIPs || p.Spec.ServiceSelector == nil || !p.Spec.ServiceSelector.matches(svc.Metadata.Labels) {
				continue
			}
			for node, labels := range nodeLabels {
				if p.Spec.NodeSelector == nil || p.Spec.NodeSelector.matches(labels) {
					selected[node] = true
				}
			}
		}
		for _, member := range pair.Members {
			if !selected[member.Node] {
				return fmt.Errorf("no CiliumL2AnnouncementPolicy lets %s announce %s for Service %s", member.Node, pair.Address, svc.Metadata.Name)
			}
		}
		lease := "cilium-l2announce-" + svc.Metadata.Namespace + "-" + svc.Metadata.Name
		if svc.Metadata.Namespace == "" {
			lease = "cilium-l2announce-nexora-" + svc.Metadata.Name
		}
		data, err = r.execute(ctx, nil, "get", "lease", lease, "-n", "kube-system", "-o", "json")
		if err != nil {
			return fmt.Errorf("L2 announcement lease for %s unreadable: %w", pair.Address, err)
		}
		var l struct {
			Spec struct{ HolderIdentity string }
		}
		if err := json.Unmarshal(data, &l); err != nil {
			return err
		}
		if !selected[strings.TrimSpace(l.Spec.HolderIdentity)] {
			return fmt.Errorf("L2 lease for %s is not held by a policy-selected node", pair.Address)
		}
	}
	return nil
}
