// Command nexora-mgmt is the Nexora management plane.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/api"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/stats"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// version is set at build time with -ldflags "-X main.version=<tag>".
var version = "dev"

const usage = "usage: nexora-mgmt serve | version | migrate | ca init --out <dir> | user create --admin --username U --email E --password-file F"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var err error
	switch {
	case len(args) == 1 && args[0] == "version":
		fmt.Fprintf(stdout, "nexora-mgmt %s\n", version)
	case len(args) == 1 && args[0] == "serve":
		err = serve(ctx, stdout)
	case len(args) == 1 && args[0] == "migrate":
		err = migrate(ctx, stdout)
	case len(args) >= 2 && args[0] == "ca" && args[1] == "init":
		err = caInit(args[2:], stdout)
	case len(args) >= 2 && args[0] == "user" && args[1] == "create":
		err = userCreate(ctx, args[2:], stdout)
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

// openMigrated opens the database named by NEXORA_DATABASE_URL and applies migrations. The
// database-only commands read it directly instead of config.Load (which also requires the CA files).
func openMigrated(ctx context.Context) (*store.Store, error) {
	url := os.Getenv("NEXORA_DATABASE_URL")
	if url == "" {
		return nil, errors.New("NEXORA_DATABASE_URL is required")
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(ctx); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

func migrate(ctx context.Context, stdout io.Writer) error {
	st, err := openMigrated(ctx)
	if err != nil {
		return err
	}
	st.Close()
	fmt.Fprintln(stdout, "migrations applied")
	return nil
}

func userCreate(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("user create", flag.ContinueOnError)
	admin := fs.Bool("admin", false, "create an admin (the only role the CLI creates)")
	username := fs.String("username", "", "username")
	email := fs.String("email", "", "email")
	passwordFile := fs.String("password-file", "", "file holding the password")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*admin || *username == "" || *passwordFile == "" || fs.NArg() != 0 {
		return errors.New("usage: nexora-mgmt user create --admin --username U --email E --password-file F")
	}
	raw, err := os.ReadFile(*passwordFile)
	if err != nil {
		return err
	}
	password := strings.TrimRight(string(raw), "\r\n")
	st, err := openMigrated(ctx)
	if err != nil {
		return err
	}
	defer st.Close()
	var u auth.User
	err = st.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		if u, err = auth.CreateUser(ctx, tx, *username, *email, password, auth.RoleAdmin); err != nil {
			return err
		}
		return auth.WriteAudit(ctx, tx, auth.Actor{Type: "system", ID: "cli", Name: "cli"},
			auth.Change{Action: "createUser", TargetType: "user", TargetID: u.ID, After: u}, nil)
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "user created: %s\n", u.ID)
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

	authSvc := auth.NewService(st, cfg.SecureCookies)
	if token, created, err := authSvc.EnsureSetupToken(ctx, instanceID); err != nil {
		return fmt.Errorf("setup token: %w", err)
	} else if created {
		log.Printf("setup token: %s", token)
	}
	httpSrv := &http.Server{
		Handler: api.NewHandler(api.Deps{
			Store: st, Auth: authSvc, OIDC: auth.NewOIDC(cfg.OIDC, cfg.PublicURL, st), CA: ca, Build: build,
			QueryLog: querylog.Noop{}, InstanceID: instanceID, PublicURL: cfg.PublicURL,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	httpLis, err := net.Listen("tcp", cfg.HTTPListen)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "http listening on %s\n", httpLis.Addr())

	srv := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(tlsCfg)),
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 20 * time.Second, Timeout: 10 * time.Second}),
		// Engines ping every 10 s; the default policy (5 min) would close their connections.
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 5 * time.Second, PermitWithoutStream: true}),
	)
	controlServer := control.NewServer(st, ca, hub, instanceID)
	controlServer.OnStats = func(ctx context.Context, engineID string, s *controlv1.Stats) {
		_ = stats.Record(ctx, st, engineID, s)
	}
	controlv1.RegisterEngineControlServer(srv, controlServer)
	lis, err := net.Listen("tcp", cfg.GRPCListen)
	if err != nil {
		_ = httpLis.Close()
		return err
	}
	fmt.Fprintf(stdout, "grpc listening on %s\n", lis.Addr())
	serveErr := make(chan error, 2)
	go func() { serveErr <- srv.Serve(lis) }()
	go func() { serveErr <- httpSrv.Serve(httpLis) }()
	select {
	case err := <-serveErr:
		srv.Stop()
		_ = httpSrv.Close()
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
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
