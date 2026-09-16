package kwrollout

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"path"
	"slices"
	"strings"
)

const defaultGroupID = "00000000-0000-0000-0000-000000000001"

// KWPairTopology is the approved topology, not an inference from live endpoints.
func KWPairTopology() []Pair {
	return []Pair{
		{Name: "dns136", Address: "192.168.10.136", Members: [2]Member{{Workload: "nexora-engine-a", Node: "master-12"}, {Workload: "nexora-engine-c", Node: "master-11"}}},
		{Name: "dns139", Address: "192.168.10.139", Members: [2]Member{{Workload: "nexora-engine-b", Node: "master-13"}, {Workload: "nexora-engine-d", Node: "master-11"}}},
	}
}

// FleetSnapshot contains only public identities and serving topology. It is not
// permission to deploy: the caller must retain/revalidate bindings under lock.
type FleetSnapshot struct {
	LegacySelectors bool
	Pods            []ServingPod
}

// FleetReader assembles controller, pod, persisted identity and VIP observations.
// It is intentionally scoped to the approved kw release, not a general operator.
type FleetReader struct {
	execute func(context.Context, []byte, ...string) ([]byte, error)
}

func NewFleetReader(kubeContext string) *FleetReader {
	return &FleetReader{execute: kubectlCommand(kubeContext, "nexora")}
}

func kwExpectation(member Member, image string, desired bool) WorkloadExpectation {
	state := "/var/lib/nexora/nexora-engine"
	if member.Node == "master-11" {
		state = "/var/lib/nexora/" + member.Workload
	}
	return WorkloadExpectation{Member: member, StatePath: state, Image: image, RequireUpdated: desired}
}

// Inspect rejects incomplete/unknown topologies rather than guessing how to
// resume a partially completed migration. Diagnosis is safe; automatic takeover
// of a retained lock is not. Four members may still use both legacy selectors.
func (r *FleetReader) Inspect(ctx context.Context) (FleetSnapshot, error) {
	return r.inspect(ctx, "", nil)
}

func (r *FleetReader) inspect(ctx context.Context, image string, desired []string) (FleetSnapshot, error) {
	var snapshot FleetSnapshot
	data, err := r.execute(ctx, nil, "get", "daemonsets", "-l", "app.kubernetes.io/instance=nexora,app.kubernetes.io/name=nexora-engine", "-o", "json")
	if err != nil {
		return snapshot, err
	}
	var list struct{ Items []engineDaemonSet }
	if err := json.Unmarshal(data, &list); err != nil {
		return snapshot, fmt.Errorf("decode fleet controllers: %w", err)
	}
	seen := map[string]bool{}
	for _, ds := range list.Items {
		if seen[ds.Metadata.Name] {
			return snapshot, fmt.Errorf("duplicate fleet controller")
		}
		seen[ds.Metadata.Name] = true
	}
	if !seen["nexora-engine-a"] || !seen["nexora-engine-b"] || (len(seen) != 2 && (len(seen) != 4 || !seen["nexora-engine-c"] || !seen["nexora-engine-d"])) {
		return snapshot, fmt.Errorf("unknown or partial fleet: expected a/b or a/b/c/d; diagnose before resuming")
	}
	for _, pair := range KWPairTopology() {
		for _, member := range pair.Members {
			if !seen[member.Workload] {
				continue
			}
			pod, err := readServingPod(ctx, r.execute, "nexora", "nexora", kwExpectation(member, image, slices.Contains(desired, member.Workload)))
			if err != nil {
				return snapshot, fmt.Errorf("%s: %w", member.Workload, err)
			}
			snapshot.Pods = append(snapshot.Pods, pod)
		}
	}
	if err := validateFleetIsolation(snapshot.Pods); err != nil {
		return snapshot, err
	}
	for i, pair := range KWPairTopology() {
		name := "nexora-dns"
		if i == 1 {
			name += "-2"
		}
		body, err := r.execute(ctx, nil, "get", "service", name, "-o", "json")
		if err != nil {
			return snapshot, err
		}
		var service dnsService
		if err := json.Unmarshal(body, &service); err != nil {
			return snapshot, err
		}
		legacy := service.Spec.Selector["nexora.io/engine-instance"] != ""
		if i == 0 {
			snapshot.LegacySelectors = legacy
		} else if snapshot.LegacySelectors != legacy {
			return snapshot, fmt.Errorf("partial VIP selector cutover; diagnose before resuming")
		}
		if err := r.checkPair(ctx, snapshot.Pods, pair, name, legacy); err != nil {
			return snapshot, err
		}
	}
	return snapshot, ctx.Err()
}

func (r *FleetReader) checkPair(ctx context.Context, pods []ServingPod, pair Pair, service string, legacy bool) error {
	expected := ServiceExpectation{Name: service, Address: pair.Address, Pair: pair.Name}
	members := pair.Members[:]
	if legacy {
		members = members[:1]
		expected.LegacyInstance = strings.TrimPrefix(members[0].Workload, "nexora-engine-")
	}
	for _, member := range members {
		for _, p := range pods {
			if p.Workload == member.Workload {
				expected.Pods = append(expected.Pods, p)
			}
		}
	}
	return checkServiceEndpoints(ctx, r.execute, "nexora", "nexora", expected)
}

func validateFleetIsolation(pods []ServingPod) error {
	ids, names, ips, uids, workloads := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, p := range pods {
		if p.EngineID == "" || p.EngineNodeName == "" || p.UID == "" || p.IP == "" || p.Workload == "" || p.Node == "" || !path.IsAbs(p.StatePath) || path.Clean(p.StatePath) != p.StatePath || p.StatePath == "/" || ids[p.EngineID] || names[p.EngineNodeName] || ips[p.IP] || uids[p.UID] || workloads[p.Workload] {
			return fmt.Errorf("fleet has missing or shared workload, pod, address or engine identity")
		}
		ids[p.EngineID], names[p.EngineNodeName], ips[p.IP], uids[p.UID], workloads[p.Workload] = true, true, true, true, true
		for _, prior := range pods[:i] {
			if prior.Node == p.Node && (prior.StatePath == p.StatePath || strings.HasPrefix(prior.StatePath, p.StatePath+"/") || strings.HasPrefix(p.StatePath, prior.StatePath+"/")) {
				return fmt.Errorf("colocated workloads have overlapping persistent state")
			}
		}
	}
	return nil
}

// CheckBoundFleet revalidates persistent IDs across replacements. Pod UIDs may
// change; engine UUIDs and logical node names must not. Newly created partners
// are bound only after their state isolation and full health have been checked.
func CheckBoundFleet(before, after []ServingPod) error {
	if err := validateFleetIsolation(after); err != nil {
		return err
	}
	for _, old := range before {
		index := slices.IndexFunc(after, func(p ServingPod) bool { return p.Workload == old.Workload })
		if index < 0 {
			return fmt.Errorf("bound workload %s disappeared", old.Workload)
		}
		p := after[index]
		if old.EngineID != p.EngineID || old.EngineNodeName != p.EngineNodeName || old.Node != p.Node || old.StatePath != p.StatePath {
			return fmt.Errorf("persistent identity or placement changed for %s", old.Workload)
		}
	}
	return nil
}

// CheckFleetManagement uses the UUIDs read from persistent pod state, not an API
// lookup by mutable node name, and preserves the single default policy group.
func CheckFleetManagement(ctx context.Context, client *http.Client, origin string, snapshot FleetSnapshot) error {
	var bindings []EngineBinding
	for _, pod := range snapshot.Pods {
		bindings = append(bindings, EngineBinding{ID: pod.EngineID, NodeName: pod.EngineNodeName, GroupID: defaultGroupID})
	}
	return CheckManagement(ctx, client, origin, bindings)
}

// FleetDNSProbes uses the existing bootstrap contract: www.nexora-demo.kw. A
// 192.0.2.80. It never learns an expectation from the first DNS response.
func FleetDNSProbes(snapshot FleetSnapshot) []DNSProbe {
	probes := VIPDNSProbes()
	for _, pod := range snapshot.Pods {
		probes = append(probes, DNSProbe{Address: net.JoinHostPort(pod.IP, "53"), Name: "www.nexora-demo.kw.", Expected: netip.MustParseAddr("192.0.2.80")})
	}
	return probes
}

func VIPDNSProbes() []DNSProbe {
	var probes []DNSProbe
	for _, pair := range KWPairTopology() {
		probes = append(probes, DNSProbe{Address: net.JoinHostPort(pair.Address, "53"), Name: "www.nexora-demo.kw.", Expected: netip.MustParseAddr("192.0.2.80")})
	}
	return probes
}
