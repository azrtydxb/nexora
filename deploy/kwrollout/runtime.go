package kwrollout

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
)

// RuntimeConfig selects the existing kw installation. This path does not create
// a cluster or bootstrap policy: it requires a healthy installed fleet first.
type RuntimeConfig struct {
	Context, Root, Tag, Origin, ProbeHelper string
	Report                                  func(string)
}

// Rollout assembles the real lock, readers, authenticated health checks, DNS
// monitor, UID-safe enrolment and serial Helm stages. It refuses dirty sources.
// Supporting-resource and bootstrap subprocesses check the same lock owner via
// a private Unix socket before every Kubernetes/API call.
func Rollout(ctx context.Context, config RuntimeConfig, client *http.Client) error {
	return rolloutWith(ctx, config, client, executeCommand)
}

func rolloutWith(ctx context.Context, config RuntimeConfig, client *http.Client, execute commandRunner) error {
	if config.Context == "" || !filepath.IsAbs(config.Root) || !regexp.MustCompile(`^sha-[0-9a-f]{7}$`).MatchString(config.Tag) || config.Report == nil || client == nil {
		return fmt.Errorf("explicit rollout context, source root, immutable tag, reporter and authenticated client required")
	}
	var reportMu sync.Mutex
	reporter := config.Report
	config.Report = func(message string) {
		reportMu.Lock()
		defer reportMu.Unlock()
		reporter(message)
	}
	git := func(args ...string) ([]byte, error) {
		return execute(ctx, commandSpec{Program: "git", Args: append([]string{"-C", config.Root}, args...)})
	}
	status, err := git("status", "--porcelain", "--untracked-files=normal")
	if err != nil || len(bytes.TrimSpace(status)) != 0 {
		return fmt.Errorf("rollout requires a clean committed source tree")
	}
	head, err := git("rev-parse", "--short=7", "HEAD")
	if err != nil || config.Tag != "sha-"+strings.TrimSpace(string(head)) {
		return fmt.Errorf("rollout image tag must name the committed source HEAD")
	}
	lock, err := NewDeploymentLock(config.Context, "nexora", "nexora")
	if err != nil {
		return err
	}
	lock.execute = kubectlWith(config.Context, "nexora", execute)
	var lockMu sync.Mutex
	serialized := func(operation func(context.Context) error) func(context.Context) error {
		return func(ctx context.Context) error {
			lockMu.Lock()
			defer lockMu.Unlock()
			if err := ctx.Err(); err != nil {
				return err
			}
			return operation(ctx)
		}
	}
	acquire, release, ownership := serialized(lock.Acquire), serialized(lock.Release), serialized(lock.Check)
	guard, err := newMutationGuard(ctx, ownership)
	if err != nil {
		return err
	}
	defer guard.close()
	phase := func(name string) func(context.Context) error {
		return func(ctx context.Context) error {
			guard.setContext(ctx)
			config.Report("running guarded deployment phase: " + name)
			command := commandSpec{Program: "bash", Args: []string{filepath.Join(config.Root, "scripts/kw-deploy.sh"), "--skip-build", "--tag", config.Tag}}
			command.Env = guardEnvironment(os.Environ(), map[string]string{
				"NEXORA_KW_CONTEXT": config.Context, "NEXORA_KW_API_URL": config.Origin,
				"NEXORA_KW_DEPLOY_PHASE": name, "NEXORA_KW_GUARD_SOCKET": guard.path,
			})
			if _, err := execute(ctx, command); err != nil {
				return fmt.Errorf("guarded %s phase failed; lock retained", name)
			}
			return ctx.Err()
		}
	}
	reader := &FleetReader{execute: kubectlWith(config.Context, "nexora", execute)}
	image := "192.168.10.131/azrtydxb/nexora-engine:" + config.Tag
	probe := func(ctx context.Context, probes []DNSProbe) error {
		return remoteDNSWith(ctx, kubectlWith(config.Context, "nexora-dev", execute), config.ProbeHelper, probes, func(sample ProbeSample) {
			body, _ := json.Marshal(sample)
			config.Report("DNS sample: " + string(body))
		})
	}
	var bound, observed FleetSnapshot
	helmArgs := func(stage Stage) []string {
		return []string{"--kube-context", config.Context, "-n", "nexora", "-f", filepath.Join(config.Root, "deploy/kw/values-kw.yaml"), "-f", filepath.Join(config.Root, "deploy/kw/values-pairs.yaml"), "--set-string", "image.tag=" + config.Tag, "--set-string", "engine.pairedRollout.workload=" + stage.RollingWorkload, "--set", fmt.Sprintf("engine.pairedRollout.legacySelectors=%t", stage.LegacySelectors)}
	}
	chart := filepath.Join(config.Root, "deploy/helm/nexora")
	render := func(ctx context.Context, stage Stage) error {
		args := append([]string{"template", "nexora", chart}, helmArgs(stage)...)
		args = append(args, "--api-versions", "monitoring.coreos.com/v1", "--api-versions", "postgresql.cnpg.io/v1")
		body, err := execute(ctx, commandSpec{Program: "helm", Args: args})
		if err != nil {
			return fmt.Errorf("render stage %s failed", stage.Name)
		}
		return validateRenderedEngines(body, stage, image)
	}
	// Validate every permitted stage before acquiring a production lock.
	stages := []Stage{{Name: "frozen", LegacySelectors: true}, {Name: "paired"}}
	for _, pair := range KWPairTopology() {
		for _, m := range pair.Members {
			stages = append(stages, Stage{Name: m.Workload, RollingWorkload: m.Workload})
		}
	}
	for _, stage := range stages {
		if err := render(ctx, stage); err != nil {
			return err
		}
	}
	s := steps{
		lock: acquire, unlock: release, ownership: ownership,
		prepare: phase("support"), finalize: phase("bootstrap"),
		monitorInterval: time.Second,
		checkDNS:        func(ctx context.Context) error { return probe(ctx, VIPDNSProbes()) },
		inspect: func(ctx context.Context) (bool, error) {
			var err error
			bound, err = Preflight(ctx, reader, client, config.Origin, probe, config.Report)
			if err == nil {
				err = reader.CheckNodes(ctx, bound)
			}
			return bound.LegacySelectors, err
		},
		health: func(ctx context.Context, gate Gate) error {
			current, err := reader.inspect(ctx, image, gate.Desired)
			if err != nil {
				return err
			}
			if gate.PairSelectors && current.LegacySelectors {
				return fmt.Errorf("paired selectors required at %s", gate.Name)
			}
			for _, required := range gate.Required {
				if !slices.ContainsFunc(current.Pods, func(p ServingPod) bool { return p.Workload == required }) {
					return fmt.Errorf("required workload %s missing at %s", required, gate.Name)
				}
			}
			if err := CheckBoundFleet(bound.Pods, current.Pods); err != nil {
				return err
			}
			if err := CheckFleetManagement(ctx, client, config.Origin, current); err != nil {
				return err
			}
			if err := probe(ctx, FleetDNSProbes(current)); err != nil {
				return err
			}
			bound, observed = current, current
			config.Report("health passed: " + gate.Name)
			return nil
		},
		enroll: func(ctx context.Context, anchors []string) error {
			return reader.enrollPairs(ctx, observed, anchors, ownership)
		},
		apply: func(ctx context.Context, stage Stage) error {
			if err := reader.CheckNodes(ctx, observed); err != nil {
				return err
			}
			if err := render(ctx, stage); err != nil {
				return err
			}
			// Rendering and capacity inspection take time: check ownership again
			// immediately before the only controller mutation in this stage.
			if err := ownership(ctx); err != nil {
				return err
			}
			stageCtx, cancel := context.WithTimeout(ctx, 16*time.Minute)
			defer cancel()
			args := append([]string{"upgrade", "nexora", chart}, helmArgs(stage)...)
			// Helm 4's watcher insists that even OnDelete DaemonSets have
			// updated pods. Legacy readiness respects intentionally frozen
			// partners; our subsequent gates still require the selected image,
			// exact endpoints, persisted identity, management/config and DNS.
			args = append(args, "--force-conflicts", "--wait=legacy", "--timeout", "15m")
			config.Report("applying guarded Helm stage: " + stage.Name)
			if _, err := execute(stageCtx, commandSpec{Program: "helm", Args: args}); err != nil {
				// Helm diagnostics can include Secret manifests. Never forward
				// them; a failed release is diagnosed separately under its lock.
				return fmt.Errorf("Helm stage %s failed; lock retained, diagnose Helm status before recovery", stage.Name)
			}
			// Helm can report readiness while the old surge pod is still
			// draining. Wait only for the known replaced target pod to vanish;
			// do not retry or suppress any DNS/management health failure.
			for _, previous := range observed.Pods {
				if previous.Workload != stage.RollingWorkload || previous.Image == image {
					continue
				}
				args := []string{"--context", config.Context, "-n", "nexora", "--request-timeout=130s", "wait", "--for=delete", "pod/" + previous.Name, "--timeout=120s"}
				if _, err := execute(stageCtx, commandSpec{Program: "kubectl", Args: args}); err != nil {
					return fmt.Errorf("old target pod did not finish draining; lock retained")
				}
			}
			return stageCtx.Err()
		},
	}
	return run(ctx, KWPairTopology(), s)
}

func validateRenderedEngines(body []byte, stage Stage, image string) error {
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	seen := map[string]bool{}
	for {
		var doc map[string]any
		if err := decoder.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("invalid Helm rendering")
		}
		if doc["kind"] != "DaemonSet" {
			continue
		}
		data, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		var ds engineDaemonSet
		if err := json.Unmarshal(data, &ds); err != nil {
			return err
		}
		var member Member
		for _, pair := range KWPairTopology() {
			for _, candidate := range pair.Members {
				if candidate.Workload == ds.Metadata.Name {
					member = candidate
				}
			}
		}
		if member.Workload == "" || seen[member.Workload] {
			return fmt.Errorf("unexpected or duplicate rendered DaemonSet")
		}
		seen[member.Workload] = true
		if _, _, err := verifyEngineSpec(ds.Spec.Template.Spec, kwExpectation(member, image, true)); err != nil {
			return fmt.Errorf("rendered %s: %w", member.Workload, err)
		}
		// Validate the fixed scheduling and resource assumptions used by the
		// node capacity gate independently from Helm helper validation.
		var scheduling struct {
			Spec struct {
				UpdateStrategy struct{ Type string }
				Template       struct {
					Spec struct {
						Affinity struct {
							NodeAffinity struct {
								Required struct {
									Terms []struct {
										Expressions []struct {
											Key, Operator string
											Values        []string
										} `json:"matchExpressions"`
									} `json:"nodeSelectorTerms"`
								} `json:"requiredDuringSchedulingIgnoredDuringExecution"`
							}
						}
						Containers []struct {
							Name      string
							Resources struct{ Requests map[string]string }
						}
					}
				}
			}
		}
		if err := json.Unmarshal(data, &scheduling); err != nil {
			return err
		}
		strategy := "OnDelete"
		if member.Workload == stage.RollingWorkload {
			strategy = "RollingUpdate"
		}
		if scheduling.Spec.UpdateStrategy.Type != strategy {
			return fmt.Errorf("rendered stage permits the wrong rolling workloads")
		}
		terms := scheduling.Spec.Template.Spec.Affinity.NodeAffinity.Required.Terms
		if len(terms) == 0 {
			return fmt.Errorf("rendered workload is not node pinned")
		}
		for _, term := range terms {
			pinned := false
			for _, expression := range term.Expressions {
				pinned = pinned || expression.Key == "kubernetes.io/hostname" && expression.Operator == "In" && len(expression.Values) == 1 && expression.Values[0] == member.Node
			}
			if !pinned {
				return fmt.Errorf("rendered workload may schedule outside the approved node")
			}
		}
		containers := scheduling.Spec.Template.Spec.Containers
		if len(containers) != 1 || containers[0].Name != "engine" || containers[0].Resources.Requests["cpu"] != "500m" || containers[0].Resources.Requests["memory"] != "384Mi" {
			return fmt.Errorf("rendered engine requests differ from the capacity budget")
		}
	}
	if len(seen) != 4 {
		return fmt.Errorf("rendering must contain exactly four approved engine workloads")
	}
	return nil
}
