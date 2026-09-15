// Command nexora-operator runs the Nexora Kubernetes operator.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/piwi3910/nexora/operator/api/v1alpha1"
	"github.com/piwi3910/nexora/operator/internal/controller/enginegroup"
	"github.com/piwi3910/nexora/operator/internal/version"
)

const leaderElectionID = "nexora-operator.nexora.io"

type options struct {
	chartDir                string
	watchNamespaces         []string
	leaderElect             bool
	leaderElectionNamespace string
	metricsBindAddress      string
	healthProbeBindAddress  string
	resyncInterval          time.Duration
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func parseFlags(args []string, stderr io.Writer) (options, error) {
	var opts options
	fs := flag.NewFlagSet("nexora-operator", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var watch string
	fs.StringVar(&opts.chartDir, "chart-dir", "/charts/nexora", "directory of the Nexora Helm chart")
	fs.StringVar(&watch, "watch-namespaces", "", "comma-separated namespaces to watch (empty: all)")
	fs.BoolVar(&opts.leaderElect, "leader-elect", true, "enable leader election")
	fs.StringVar(&opts.leaderElectionNamespace, "leader-election-namespace", os.Getenv("POD_NAMESPACE"), "namespace of the leader election lease")
	fs.StringVar(&opts.metricsBindAddress, "metrics-bind-address", ":8080", "metrics listen address")
	fs.StringVar(&opts.healthProbeBindAddress, "health-probe-bind-address", ":8081", "health probe listen address")
	fs.DurationVar(&opts.resyncInterval, "resync-interval", 60*time.Second, "status refresh interval")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() > 0 {
		return opts, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	for _, ns := range strings.Split(watch, ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			opts.watchNamespaces = append(opts.watchNamespaces, ns)
		}
	}
	if opts.resyncInterval <= 0 {
		return opts, fmt.Errorf("--resync-interval must be positive")
	}
	return opts, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "version" {
		fmt.Fprintf(stdout, "nexora-operator %s %s %s\n", version.Version, version.Commit, version.BuildDate)
		return 0
	}
	opts, err := parseFlags(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctrl.SetLogger(zap.New(zap.WriteTo(stderr)))
	log := ctrl.Log.WithName("nexora-operator")

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		log.Error(err, "register client-go scheme")
		return 1
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		log.Error(err, "register nexora.io scheme")
		return 1
	}
	cacheOpts := cache.Options{}
	if len(opts.watchNamespaces) > 0 {
		cacheOpts.DefaultNamespaces = map[string]cache.Config{}
		for _, ns := range opts.watchNamespaces {
			cacheOpts.DefaultNamespaces[ns] = cache.Config{}
		}
	}
	cfg, err := ctrl.GetConfig()
	if err != nil {
		log.Error(err, "kubernetes client configuration")
		return 1
	}
	mgr, err := ctrl.NewManager(cfg, manager.Options{
		Scheme:                  scheme,
		Cache:                   cacheOpts,
		Metrics:                 metricsserver.Options{BindAddress: opts.metricsBindAddress},
		HealthProbeBindAddress:  opts.healthProbeBindAddress,
		LeaderElection:          opts.leaderElect,
		LeaderElectionID:        leaderElectionID,
		LeaderElectionNamespace: opts.leaderElectionNamespace,
	})
	if err != nil {
		log.Error(err, "create manager")
		return 1
	}
	if err := setupControllers(mgr, opts); err != nil {
		log.Error(err, "set up controllers")
		return 1
	}
	log.Info("starting", "version", version.Version, "commit", version.Commit)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "manager stopped")
		return 1
	}
	return 0
}

// setupControllers registers the controllers with the manager.
func setupControllers(mgr ctrl.Manager, opts options) error {
	if err := (&enginegroup.Reconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(),
		ClientFor: enginegroup.DefaultClientFor(mgr.GetClient()), Now: time.Now}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("engine group controller: %w", err)
	}
	return nil
}
