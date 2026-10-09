// kw-rollout exposes preflight, guarded deployment and the toolbox DNS probe runner.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/piwi3910/nexora/deploy/kwrollout"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kw-rollout check|deploy|release-lock --context kw --probe-helper PATH [--root PATH --tag sha-COMMIT] | dns")
	}
	if args[0] == "dns" {
		if len(args) != 1 {
			return fmt.Errorf("dns takes its probe set on stdin")
		}
		var probes []kwrollout.DNSProbe
		decoder := json.NewDecoder(io.LimitReader(input, 16<<10))
		if err := decoder.Decode(&probes); err != nil || len(probes) == 0 || len(probes) > 6 {
			return fmt.Errorf("invalid DNS probe set")
		}
		ctx, cancel := context.WithTimeout(ctx, 13*time.Second)
		defer cancel()
		result := kwrollout.ProbeResult{}
		err := kwrollout.CheckDNS(ctx, probes, func(s kwrollout.DNSSample) {
			result.Attempts++
			sample := kwrollout.ProbeSample{Address: s.Address, Transport: s.Transport, Started: s.Started, Duration: s.Duration}
			if s.Err != nil {
				sample.Error = s.Err.Error()
			}
			result.Samples = append(result.Samples, sample)
		})
		if err != nil {
			result.Error = err.Error()
		}
		if encodeErr := json.NewEncoder(output).Encode(result); encodeErr != nil {
			return encodeErr
		}
		return err
	}
	if args[0] == "release-lock" {
		return releaseLock(ctx, args[1:], output, func(kubeContext string) (lockReleaser, error) {
			return kwrollout.NewDeploymentLock(kubeContext, "nexora", "nexora")
		})
	}
	if args[0] != "check" && args[0] != "deploy" {
		return fmt.Errorf("unknown command %q", args[0])
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	kubeContext := flags.String("context", "kw", "explicit Kubernetes context")
	origin := flags.String("api", "https://nexora.kw.watteel.lab", "management HTTPS origin")
	helper := flags.String("probe-helper", "", "unique compiled toolbox probe executable")
	root := flags.String("root", "", "clean committed source root (deployment only)")
	tag := flags.String("tag", "", "image tag naming source HEAD (deployment only)")
	requirePaired := flags.Bool("require-paired", false, "require the completed four-engine topology")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *kubeContext == "" || *helper == "" {
		return fmt.Errorf("context and toolbox probe helper required")
	}
	timeout := 2 * time.Minute
	if args[0] == "deploy" {
		timeout = 2 * time.Hour
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := kwrollout.ManagementClient(ctx, *kubeContext, "nexora", *origin)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	if args[0] == "deploy" {
		return kwrollout.Rollout(ctx, kwrollout.RuntimeConfig{Context: *kubeContext, Root: *root, Tag: *tag, Origin: *origin, ProbeHelper: *helper,
			Report: func(message string) { fmt.Fprintln(output, message) }}, client)
	}
	snapshot, err := kwrollout.Preflight(ctx, kwrollout.NewFleetReader(*kubeContext), client, *origin, func(ctx context.Context, probes []kwrollout.DNSProbe) error {
		return kwrollout.RemoteDNS(ctx, *kubeContext, *helper, probes)
	}, func(message string) { fmt.Fprintln(output, message) })
	if err == nil && *requirePaired && (snapshot.LegacySelectors || len(snapshot.Pods) != 4) {
		return fmt.Errorf("acceptance requires four engines and paired VIP selectors")
	}
	if err == nil {
		fmt.Fprintln(output, "preflight passed; no production resources modified")
	}
	return err
}

type lockReleaser interface {
	ReleaseHeld(ctx context.Context, owner string, confirm bool) (*kwrollout.HeldLock, error)
}

// releaseLock is the guarded operator recovery for a lock whose run died. Read
// docs/operations.md before using it: it must never follow a mere client timeout.
func releaseLock(ctx context.Context, args []string, output io.Writer, newLock func(string) (lockReleaser, error)) error {
	flags := flag.NewFlagSet("release-lock", flag.ContinueOnError)
	kubeContext := flags.String("context", "kw", "explicit Kubernetes context")
	owner := flags.String("owner", "", "current lock owner token, read from the ConfigMap (required)")
	confirm := flags.Bool("confirm", false, "confirm that no rollout process or remote mutation is still running")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *kubeContext == "" || *owner == "" {
		return fmt.Errorf("usage: kw-rollout release-lock --context kw --owner TOKEN --confirm (read the owner from nexora/nexora-deploy-lock first)")
	}
	lock, err := newLock(*kubeContext)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	held, err := lock.ReleaseHeld(ctx, *owner, *confirm)
	if held != nil {
		verb := "found"
		if err == nil {
			verb = "released"
		}
		fmt.Fprintf(output, "%s lock %s: owner=%s acquiredAt=%s stage=%s resourceVersion=%s\n", verb, held.Name,
			held.Owner, orUnknown(held.AcquiredAt), orUnknown(held.Stage), held.ResourceVersion)
	}
	if errors.Is(err, kwrollout.ErrReleaseNotConfirmed) {
		return fmt.Errorf("%w: confirm no kw-rollout process is running and the failed stage is understood, then re-run with --confirm", err)
	}
	return err
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
