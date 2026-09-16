package kwrollout

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

// CheckNodes conservatively accounts for every bound pod's requests (including
// all init containers and overhead) and reserves missing partners plus one surge
// per node. It is a point-in-time scheduler budget, not a capacity reservation.
func (r *FleetReader) CheckNodes(ctx context.Context, fleet FleetSnapshot) error {
	var nodes struct {
		Items []struct {
			Metadata kubeMetadata
			Spec     struct {
				Unschedulable bool
				Taints        []struct{ Key, Effect string }
			}
			Status struct {
				Allocatable map[string]string
				Conditions  []struct{ Type, Status string }
			}
		}
	}
	data, err := r.execute(ctx, nil, "get", "nodes", "-o", "json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &nodes); err != nil {
		return err
	}
	type resourceContainer struct {
		Name      string
		Resources struct{ Requests map[string]string }
		Env       []struct{ Name, Value string }
	}
	var pods struct {
		Items []struct {
			Metadata kubeMetadata
			Spec     struct {
				NodeName                   string
				Containers, InitContainers []resourceContainer
				Overhead                   map[string]string
				Resources                  struct{ Requests map[string]string }
			}
			Status struct {
				Phase      string
				Conditions []struct{ Type, Status string }
			}
		}
	}
	data, err = r.execute(ctx, nil, "get", "pods", "--all-namespaces", "-o", "json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &pods); err != nil {
		return err
	}
	for _, name := range []string{"master-11", "master-12", "master-13"} {
		allocated := map[string]*big.Rat{"cpu": new(big.Rat), "memory": new(big.Rat), "pods": new(big.Rat)}
		vipReady := false
		for _, pod := range pods.Items {
			if pod.Spec.NodeName != name || pod.Status.Phase == "Succeeded" || pod.Status.Phase == "Failed" {
				continue
			}
			allocated["pods"].Add(allocated["pods"], big.NewRat(1, 1))
			if pod.Metadata.Namespace == "kube-system" && strings.HasPrefix(pod.Metadata.Name, "kube-vip-ds-") && pod.Metadata.DeletionTimestamp == nil {
				owner, election, ready := false, false, false
				for _, o := range pod.Metadata.OwnerReferences {
					owner = owner || o.Controller && o.Kind == "DaemonSet" && o.Name == "kube-vip-ds" && o.UID != ""
				}
				for _, c := range pod.Spec.Containers {
					for _, e := range c.Env {
						election = election || e.Name == "svc_election" && e.Value == "true"
					}
				}
				for _, c := range pod.Status.Conditions {
					ready = ready || c.Type == "Ready" && c.Status == "True"
				}
				vipReady = vipReady || owner && election && ready
			}
			// Summing init and pod-level requests may overestimate, but never
			// understates scheduler demand or needs a new Kubernetes dependency.
			requests := []map[string]string{pod.Spec.Overhead, pod.Spec.Resources.Requests}
			for _, c := range pod.Spec.Containers {
				requests = append(requests, c.Resources.Requests)
			}
			for _, c := range pod.Spec.InitContainers {
				requests = append(requests, c.Resources.Requests)
			}
			for _, request := range requests {
				for _, key := range []string{"cpu", "memory"} {
					if request[key] == "" {
						continue
					}
					q, err := resourceQuantity(request[key])
					if err != nil {
						return err
					}
					allocated[key].Add(allocated[key], q)
				}
			}
		}
		if !vipReady {
			return fmt.Errorf("%s lacks a Ready kube-vip announcer with per-Service election", name)
		}
		missing := 1 // one same-node surge
		for _, pair := range KWPairTopology() {
			for _, member := range pair.Members {
				if member.Node != name {
					continue
				}
				present := false
				for _, p := range fleet.Pods {
					present = present || p.Workload == member.Workload
				}
				if !present {
					missing++
				}
			}
		}
		allocated["cpu"].Add(allocated["cpu"], big.NewRat(int64(missing), 2))
		allocated["memory"].Add(allocated["memory"], big.NewRat(int64(missing)*384*1024*1024, 1))
		allocated["pods"].Add(allocated["pods"], big.NewRat(int64(missing), 1))
		found := false
		for _, node := range nodes.Items {
			if node.Metadata.Name != name {
				continue
			}
			found = true
			if node.Spec.Unschedulable || node.Metadata.DeletionTimestamp != nil || node.Metadata.Labels["kubernetes.io/hostname"] != name {
				return fmt.Errorf("%s is unschedulable, deleting or has an unexpected hostname label", name)
			}
			ready := false
			for _, condition := range node.Status.Conditions {
				ready = ready || condition.Type == "Ready" && condition.Status == "True"
				if (condition.Type == "MemoryPressure" || condition.Type == "DiskPressure" || condition.Type == "PIDPressure" || condition.Type == "NetworkUnavailable") && condition.Status != "False" {
					return fmt.Errorf("%s reports %s", name, condition.Type)
				}
			}
			if !ready {
				return fmt.Errorf("%s is not Ready", name)
			}
			for _, taint := range node.Spec.Taints {
				if taint.Effect == "NoExecute" || taint.Effect == "NoSchedule" && taint.Key != "node-role.kubernetes.io/control-plane" && taint.Key != "node-role.kubernetes.io/master" {
					return fmt.Errorf("%s has an untolerated scheduling taint", name)
				}
			}
			for key, demand := range allocated {
				capacity, err := resourceQuantity(node.Status.Allocatable[key])
				if err != nil {
					return err
				}
				if demand.Cmp(capacity) > 0 {
					return fmt.Errorf("%s lacks %s request capacity for partners and one surge", name, key)
				}
			}
		}
		if !found {
			return fmt.Errorf("required node %s is missing", name)
		}
	}
	return ctx.Err()
}

func resourceQuantity(value string) (*big.Rat, error) {
	parts := regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)([a-zA-Z]*)$`).FindStringSubmatch(value)
	if parts == nil {
		return nil, fmt.Errorf("unsupported Kubernetes resource quantity")
	}
	q, ok := new(big.Rat).SetString(parts[1])
	if !ok {
		return nil, fmt.Errorf("invalid Kubernetes resource quantity")
	}
	if parts[2] == "m" || parts[2] == "u" || parts[2] == "n" {
		denominator := map[string]int64{"m": 1000, "u": 1000000, "n": 1000000000}[parts[2]]
		return q.Quo(q, big.NewRat(denominator, 1)), nil
	}
	powers := map[string]int{"": 0, "k": 1, "K": 1, "M": 2, "G": 3, "T": 4, "P": 5, "E": 6, "Ki": 1, "Mi": 2, "Gi": 3, "Ti": 4, "Pi": 5, "Ei": 6}
	power, ok := powers[parts[2]]
	if !ok {
		return nil, fmt.Errorf("unsupported Kubernetes resource quantity suffix")
	}
	base := int64(1000)
	if strings.HasSuffix(parts[2], "i") {
		base = 1024
	}
	factor := new(big.Int).Exp(big.NewInt(base), big.NewInt(int64(power)), nil)
	return q.Mul(q, new(big.Rat).SetInt(factor)), nil
}
