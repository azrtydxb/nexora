// Command nexora-mgmt is the Nexora management plane.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

const usage = "usage: nexora-mgmt serve | migrate | ca init --out <dir> | user create --admin"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var err error
	switch {
	case len(args) == 1 && args[0] == "serve":
		err = serve(ctx, stdout)
	case len(args) == 1 && args[0] == "migrate":
		err = migrate(ctx, stdout)
	case len(args) >= 2 && args[0] == "ca" && args[1] == "init":
		err = caInit(args[2:], stdout)
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "nexora-mgmt %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

// migrate needs only the database, so it reads NEXORA_DATABASE_URL directly instead of
// config.Load (which also requires the CA files).
func migrate(ctx context.Context, stdout io.Writer) error {
	url := os.Getenv("NEXORA_DATABASE_URL")
	if url == "" {
		return errors.New("NEXORA_DATABASE_URL is required")
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "migrations applied")
	return nil
}

func caInit(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("ca init", flag.ContinueOnError)
	out := fs.String("out", "", "directory for ca.crt and ca.key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" || fs.NArg() != 0 {
		return errors.New("usage: nexora-mgmt ca init --out <dir>")
	}
	if err := pki.InitCA(*out); err != nil {
		return err
	}
	ca, err := pki.LoadCA(*out+"/ca.crt", *out+"/ca.key")
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "ca fingerprint: %s\n", ca.Fingerprint())
	return nil
}

func serve(ctx context.Context, stdout io.Writer) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	ca, err := pki.LoadCA(cfg.CACertFile, cfg.CAKeyFile)
	if err != nil {
		return err
	}
	tlsCfg, err := control.TLSConfig(ca, cfg.GRPCServerNames)
	if err != nil {
		return fmt.Errorf("gRPC server certificate (NEXORA_GRPC_SERVER_NAMES): %w", err)
	}
	instanceID := control.NewInstanceID()
	go control.RunInstanceHeartbeat(ctx, st, instanceID)
	build := snapshot.BuildConfig{QueryLogToManagement: cfg.QueryLogBackend == "builtin", DefaultOTLPEndpoint: cfg.OTLPEndpoint}
	if _, err := snapshot.EnsureInitial(ctx, st, build); err != nil {
		return err
	}
	hub := control.NewHub(st, instanceID)
	go func() { _ = hub.Run(ctx) }()

	srv := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsCfg)),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 20 * time.Second, Timeout: 10 * time.Second}),
		// Engines ping every 10 s; the default policy (5 min) would close their connections.
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 5 * time.Second, PermitWithoutStream: true}),
	)
	controlv1.RegisterEngineControlServer(srv, control.NewServer(st, ca, hub, instanceID))
	lis, err := net.Listen("tcp", cfg.GRPCListen)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "grpc listening on %s\n", lis.Addr())
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(lis) }()
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	// Engine streams never finish on their own: give unary calls a moment, then close the rest.
	stopped := make(chan struct{})
	go func() { srv.GracefulStop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		srv.Stop()
	}
	return nil
}
