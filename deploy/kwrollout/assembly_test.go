package kwrollout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Only process execution and the HTTPS API are simulated. These tests exercise
// the production assembly, lock protocol, actual Helm renderer, pod/identity and
// EndpointSlice readers, enrolment CAS, management gate and remote DNS evidence.
func TestAssembledRuntimeMigration(t *testing.T) {
	for _, mode := range []string{"success", "drain-failure", "support-failure", "bootstrap-failure", "helm-failure", "ownership-loss", "dns-failure", "identity-drift", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var mu sync.Mutex
			fleet := newFleetFixture(t, false, true)
			store := &lockStore{}
			var mutations []string
			image := "192.168.10.131/azrtydxb/nexora-engine:sha-1234567"
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.URL.Path {
				case "/api/v1/config-versions":
					fmt.Fprint(w, `[{"version":1}]`)
				case "/api/v1/engines":
					var engines []managedEngine
					now := time.Now()
					for _, m := range fleet.members {
						p := m.pods[0]
						name := strings.ReplaceAll(p.Spec.Containers[0].Env[1].Value, "$(K8S_NODE_NAME)", p.Spec.NodeName)
						engines = append(engines, managedEngine{ID: m.identity, NodeName: name, GroupID: defaultGroupID, Connected: true, Status: "current", AppliedVersion: 1, TargetVersion: 1, LastSeenAt: &now})
					}
					json.NewEncoder(w).Encode(engines)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			nodes, pods := nodeFixtureJSON()
			executor := func(ctx context.Context, command commandSpec) ([]byte, error) {
				if command.Program == "helm" && command.Args[0] == "template" {
					return executeCommand(ctx, command)
				}
				mu.Lock()
				defer mu.Unlock()
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				switch command.Program {
				case "git":
					if slices.Contains(command.Args, "status") {
						return nil, nil
					}
					return []byte("1234567\n"), nil
				case "bash":
					phase := ""
					for _, entry := range command.Env {
						if strings.HasPrefix(entry, "NEXORA_KW_DEPLOY_PHASE=") {
							phase = strings.TrimPrefix(entry, "NEXORA_KW_DEPLOY_PHASE=")
						}
					}
					if store.obj == nil || store.obj.Data["owner"] == "" {
						return nil, fmt.Errorf("phase without lock")
					}
					mutations = append(mutations, phase)
					if mode == phase+"-failure" {
						return nil, errors.New("phase failure")
					}
					return nil, nil
				case "helm":
					if command.Args[0] != "upgrade" {
						return nil, fmt.Errorf("unexpected Helm command")
					}
					if !slices.Contains(command.Args, "--wait=legacy") {
						return nil, fmt.Errorf("Helm watcher would wait for intentionally frozen OnDelete pods")
					}
					if store.obj == nil || store.obj.Data["owner"] == "" {
						return nil, fmt.Errorf("Helm without lock")
					}
					target, legacy := "", false
					for _, arg := range command.Args {
						if strings.HasPrefix(arg, "engine.pairedRollout.workload=") {
							target = strings.TrimPrefix(arg, "engine.pairedRollout.workload=")
						}
						legacy = legacy || arg == "engine.pairedRollout.legacySelectors=true"
					}
					mutations = append(mutations, "helm:"+target)
					if mode == "helm-failure" && target == "nexora-engine-a" {
						return nil, errors.New("Helm failed")
					}
					if mode == "cancel" && target == "nexora-engine-a" {
						cancel()
						return nil, ctx.Err()
					}
					next := newFleetFixture(t, true, legacy)
					for instance, w := range next.members {
						if old, ok := fleet.members[instance]; ok {
							w = old
							next.members[instance] = old
						}
						w.ds.Spec.Template.Spec.Containers[0].Image = image
						if instance == "c" || instance == "d" || target == w.ds.Metadata.Name {
							w.pods[0].Spec.Containers[0].Image = image
						}
					}
					fleet = next
					if mode == "ownership-loss" && target == "nexora-engine-a" {
						store.obj.Data["owner"] = "successor"
					}
					if mode == "identity-drift" && target == "nexora-engine-a" {
						fleet.members["a"].identity = "10000000-0000-0000-0000-000000000099"
					}
					return nil, nil
				case "kubectl":
					if len(command.Args) < 6 || command.Args[0] != "--context" || command.Args[1] != "kw-fixture" {
						return nil, fmt.Errorf("missing explicit context")
					}
					args := command.Args[5:]
					if args[0] == "wait" {
						if !slices.Contains(args, "--for=delete") || !slices.Contains(args, "--timeout=120s") {
							return nil, fmt.Errorf("unbounded or incorrect drain wait")
						}
						if mode == "drain-failure" {
							return nil, errors.New("old pod still draining")
						}
						return nil, nil
					}
					if command.Args[3] == "nexora-dev" {
						var probes []DNSProbe
						if err := json.Unmarshal(command.Input, &probes); err != nil {
							return nil, err
						}
						result := ProbeResult{}
						for _, p := range probes {
							for _, transport := range []string{"udp", "tcp"} {
								sample := ProbeSample{Address: p.Address, Transport: transport, Started: time.Now()}
								result.Attempts++
								if mode == "dns-failure" && slices.Contains(mutations, "helm:nexora-engine-a") && transport == "tcp" {
									sample.Error = "injected sampled TCP failure"
									result.Error = sample.Error
									result.Samples = append(result.Samples, sample)
									body, _ := json.Marshal(result)
									return body, errors.New("remote probe exited 1")
								}
								result.Samples = append(result.Samples, sample)
							}
						}
						return json.Marshal(result)
					}
					if args[0] == "create" || args[1] == "configmap" {
						return store.execute(ctx, command.Input, args...)
					}
					if args[1] == "nodes" {
						return nodes, nil
					}
					if args[1] == "pods" && slices.Contains(args, "--all-namespaces") {
						return pods, nil
					}
					if args[0] == "get" && args[1] == "services" {
						return []byte(l2ServicesFixture), nil
					}
					if args[0] == "get" && args[1] == "ciliuml2announcementpolicies" {
						return []byte(l2PoliciesFixture), nil
					}
					if args[0] == "get" && args[1] == "lease" {
						return []byte(`{"spec":{"holderIdentity":"master-11"}}`), nil
					}
					if args[0] == "patch" {
						mutations = append(mutations, "enroll:"+args[2])
					}
					return fleet.execute(ctx, command.Input, args...)
				}
				return nil, fmt.Errorf("unexpected command %s", command.Program)
			}
			root, _ := filepath.Abs("../..")
			err := rolloutWith(ctx, RuntimeConfig{Context: "kw-fixture", Root: root, Tag: "sha-1234567", Origin: server.URL, ProbeHelper: "/tmp/nexora-kw-probe-fixture", Report: func(string) {}}, server.Client(), executor)
			if mode == "success" {
				want := []string{"support", "helm:", "enroll:pod-a", "enroll:pod-b", "helm:", "helm:nexora-engine-a", "helm:nexora-engine-b", "helm:nexora-engine-c", "helm:nexora-engine-d", "helm:", "bootstrap"}
				if err != nil || !slices.Equal(mutations, want) || store.obj.Data["owner"] != "" {
					t.Fatalf("err=%v mutations=%v", err, mutations)
				}
			} else {
				if err == nil || store.obj == nil || store.obj.Data["owner"] == "" {
					t.Fatalf("failure released lock: %v", err)
				}
				if mode != "bootstrap-failure" && slices.Contains(mutations, "helm:nexora-engine-b") {
					t.Fatalf("advanced to next member after failure: %v", mutations)
				}
				if mode == "dns-failure" && !strings.Contains(err.Error(), "sampled TCP failure") {
					t.Fatalf("lost remote failure evidence: %v", err)
				}
			}
		})
	}
}

func nodeFixtureJSON() ([]byte, []byte) {
	var nodes []json.RawMessage
	for _, node := range []string{"master-11", "master-12", "master-13"} {
		nodes = append(nodes, json.RawMessage(fmt.Sprintf(`{"metadata":{"name":%q,"labels":{"kubernetes.io/hostname":%q}},"status":{"allocatable":{"cpu":"8","memory":"32Gi","pods":"110"},"conditions":[{"type":"Ready","status":"True"}]}}`, node, node)))
	}
	n, _ := json.Marshal(map[string]any{"items": nodes})
	return n, []byte(`{"items":[]}`)
}

const l2ServicesFixture = `{"items":[
{"metadata":{"name":"nexora-dns","namespace":"nexora","labels":{"lb.kw.watteel.lab/pinned":"dns136"},"annotations":{"lbipam.cilium.io/ips":"192.168.10.136"}},"spec":{"type":"LoadBalancer","loadBalancerClass":"io.cilium/l2-announcer","externalTrafficPolicy":"Local"}},
{"metadata":{"name":"nexora-dns-2","namespace":"nexora","labels":{"lb.kw.watteel.lab/pinned":"dns139"},"annotations":{"lbipam.cilium.io/ips":"192.168.10.139"}},"spec":{"type":"LoadBalancer","loadBalancerClass":"io.cilium/l2-announcer","externalTrafficPolicy":"Local"}}]}`

const l2PoliciesFixture = `{"items":[
{"metadata":{"name":"kw-dns136"},"spec":{"loadBalancerIPs":true,"nodeSelector":{"matchExpressions":[{"key":"kubernetes.io/hostname","operator":"In","values":["master-12","master-11"]}]},"serviceSelector":{"matchLabels":{"lb.kw.watteel.lab/pinned":"dns136"}}}},
{"metadata":{"name":"kw-dns139"},"spec":{"loadBalancerIPs":true,"nodeSelector":{"matchExpressions":[{"key":"kubernetes.io/hostname","operator":"In","values":["master-13","master-11"]}]},"serviceSelector":{"matchLabels":{"lb.kw.watteel.lab/pinned":"dns139"}}}},
{"metadata":{"name":"kw-lan"},"spec":{"loadBalancerIPs":true,"nodeSelector":{"matchLabels":{"node-role.kubernetes.io/control-plane":"true"}},"serviceSelector":{"matchExpressions":[{"key":"lb.kw.watteel.lab/pinned","operator":"DoesNotExist"}]}}}]}`

func TestNodeCapacityAndVIPFailures(t *testing.T) {
	holders := map[string]string{"cilium-l2announce-nexora-nexora-dns": "master-12", "cilium-l2announce-nexora-nexora-dns-2": "master-11"}
	for _, mode := range []string{"ok", "cpu", "memory", "pods", "ready",
		"policy-missing", "policy-node-mismatch", "policy-selector-mismatch", "policy-lbips-off",
		"service-class", "service-policy", "service-missing", "lease-missing", "lease-holder"} {
		nodes, pods := nodeFixtureJSON()
		services, policies := l2ServicesFixture, l2PoliciesFixture
		leaseHolders := map[string]string{}
		for k, v := range holders {
			leaseHolders[k] = v
		}
		switch mode {
		case "cpu":
			nodes = []byte(strings.ReplaceAll(string(nodes), `"cpu":"8"`, `"cpu":"1"`))
		case "memory":
			nodes = []byte(strings.ReplaceAll(string(nodes), `"memory":"32Gi"`, `"memory":"512Mi"`))
		case "pods":
			nodes = []byte(strings.ReplaceAll(string(nodes), `"pods":"110"`, `"pods":"1"`))
		case "ready":
			nodes = []byte(strings.ReplaceAll(string(nodes), `"status":"True"`, `"status":"False"`))
		case "policy-missing":
			policies = `{"items":[]}`
		case "policy-node-mismatch": // dns139 policy no longer selects master-13
			policies = strings.Replace(policies, `"master-13","master-11"`, `"master-12","master-11"`, 1)
		case "policy-selector-mismatch": // dns136 policy selects a different Service
			policies = strings.Replace(policies, `/pinned":"dns136"}}}}`, `/pinned":"other"}}}}`, 1)
		case "policy-lbips-off":
			policies = strings.Replace(policies, `"loadBalancerIPs":true`, `"loadBalancerIPs":false`, 1)
		case "service-class":
			services = strings.Replace(services, `"loadBalancerClass":"io.cilium/l2-announcer"`, `"loadBalancerClass":"kube-vip.io/kube-vip-class"`, 1)
		case "service-policy":
			services = strings.Replace(services, `"externalTrafficPolicy":"Local"`, `"externalTrafficPolicy":"Cluster"`, 1)
		case "service-missing":
			services = strings.Replace(services, `192.168.10.139`, `192.168.10.200`, 1)
		case "lease-missing":
			delete(leaseHolders, "cilium-l2announce-nexora-nexora-dns")
		case "lease-holder": // held by a node the dns136 policy does not select
			leaseHolders["cilium-l2announce-nexora-nexora-dns"] = "master-13"
		}
		r := &FleetReader{execute: func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
			switch args[1] {
			case "nodes":
				return nodes, nil
			case "pods":
				return pods, nil
			case "services":
				return []byte(services), nil
			case "ciliuml2announcementpolicies":
				return []byte(policies), nil
			case "lease":
				h, ok := leaseHolders[args[2]]
				if !ok || args[3] != "-n" || args[4] != "kube-system" {
					return nil, fmt.Errorf("not found")
				}
				return []byte(fmt.Sprintf(`{"spec":{"holderIdentity":%q}}`, h)), nil
			}
			return nil, fmt.Errorf("unexpected %v", args)
		}}
		err := r.CheckNodes(context.Background(), FleetSnapshot{})
		if (mode == "ok") != (err == nil) {
			t.Fatalf("mode=%s err=%v", mode, err)
		}
	}
}
