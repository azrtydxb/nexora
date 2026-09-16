package kwrollout

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"
)

// Preflight runs the assembled read-only health path. DNS must run from a host
// with routes to the engine pod network, not through a Service in place of direct
// probes. Reported progress contains no credentials.
func Preflight(ctx context.Context, reader *FleetReader, client *http.Client, origin string, probe func(context.Context, []DNSProbe) error, report func(string)) (FleetSnapshot, error) {
	snapshot, err := reader.Inspect(ctx)
	if err != nil {
		return snapshot, fmt.Errorf("fleet topology: %w", err)
	}
	if report != nil {
		report(fmt.Sprintf("verified %d distinct engine identities and both VIP endpoint sets (legacy selectors=%t)", len(snapshot.Pods), snapshot.LegacySelectors))
	}
	if err := reader.CheckNodes(ctx, snapshot); err != nil {
		return snapshot, fmt.Errorf("node capacity/announcers: %w", err)
	}
	if report != nil {
		report("all three nodes have Ready per-Service VIP announcers and partner/surge request capacity")
	}
	if err := CheckFleetManagement(ctx, client, origin, snapshot); err != nil {
		return snapshot, fmt.Errorf("fleet management: %w", err)
	}
	if report != nil {
		report("all discovered engines connected and current at the latest configuration")
	}
	if probe == nil {
		return snapshot, fmt.Errorf("direct and VIP DNS probe runner required")
	}
	if err := probe(ctx, FleetDNSProbes(snapshot)); err != nil {
		return snapshot, fmt.Errorf("fleet DNS: %w", err)
	}
	// Management/DNS observation can take seconds. Re-read the topology and
	// reject replacement or selector drift rather than blessing stale pod IPs.
	after, err := reader.Inspect(ctx)
	if err != nil {
		return snapshot, err
	}
	if snapshot.LegacySelectors != after.LegacySelectors || len(snapshot.Pods) != len(after.Pods) {
		return snapshot, fmt.Errorf("fleet changed during preflight")
	}
	if err := CheckBoundFleet(snapshot.Pods, after.Pods); err != nil {
		return snapshot, err
	}
	for _, p := range snapshot.Pods {
		for _, next := range after.Pods {
			if p.Workload == next.Workload && (p.UID != next.UID || p.IP != next.IP || p.ResourceVersion != next.ResourceVersion) {
				return snapshot, fmt.Errorf("pod changed during preflight")
			}
		}
	}
	if report != nil {
		report("configured UDP/TCP answers passed on both VIPs and every direct pod address")
	}
	return snapshot, ctx.Err()
}

// RemoteDNS executes the same compiled probe implementation in the existing
// Linux toolbox. Only public probe specifications travel over stdin. A unique
// executable is installed by kw-preflight.sh; no production pod is modified.
func RemoteDNS(ctx context.Context, kubeContext, helperPath string, probes []DNSProbe) error {
	return remoteDNS(ctx, kubeContext, helperPath, probes, nil)
}

func remoteDNS(ctx context.Context, kubeContext, helperPath string, probes []DNSProbe, record func(ProbeSample)) error {
	return remoteDNSWith(ctx, kubectlCommand(kubeContext, "nexora-dev"), helperPath, probes, record)
}

func remoteDNSWith(ctx context.Context, execute func(context.Context, []byte, ...string) ([]byte, error), helperPath string, probes []DNSProbe, record func(ProbeSample)) error {
	if !regexp.MustCompile(`^/tmp/nexora-kw-probe-[a-zA-Z0-9.-]+$`).MatchString(helperPath) {
		return fmt.Errorf("invalid toolbox probe executable path")
	}
	for _, p := range probes {
		if err := p.validate(); err != nil {
			return err
		}
	}
	body, err := json.Marshal(probes)
	if err != nil {
		return err
	}
	out, execErr := execute(ctx, body, "exec", "-i", "deploy/toolbox", "-c", "toolbox", "--", helperPath, "dns")
	var result ProbeResult
	if err := json.Unmarshal(out, &result); err != nil {
		return fmt.Errorf("toolbox DNS probe did not return valid evidence")
	}
	for i, sample := range result.Samples {
		if i >= len(probes)*2 || sample.Address != probes[i/2].Address || sample.Transport != []string{"udp", "tcp"}[i%2] {
			return fmt.Errorf("toolbox DNS evidence differs from the requested probe set")
		}
		if record != nil {
			record(sample)
		}
		if sample.Error != "" && result.Error == "" {
			return fmt.Errorf("toolbox DNS evidence contains an unreported failure")
		}
	}
	if result.Attempts != len(result.Samples) {
		return fmt.Errorf("toolbox DNS evidence is incomplete")
	}
	// The helper encodes a result even when a DNS transport fails; kubectl
	// failure is independently fatal. Do not treat an empty result as success.
	if execErr != nil || result.Error != "" || result.Attempts != 2*len(probes) || result.Attempts == 0 {
		return fmt.Errorf("toolbox DNS failed after %d transport attempts: %s", result.Attempts, result.Error)
	}
	return ctx.Err()
}

// ProbeResult is a bounded evidence summary, not credentials or learned answers.
type ProbeResult struct {
	Attempts int
	Error    string
	Samples  []ProbeSample
}

// ProbeSample serializes errors explicitly rather than losing them as empty
// JSON objects through the Go error interface.
type ProbeSample struct {
	Address, Transport, Error string
	Started                   time.Time
	Duration                  time.Duration
}
