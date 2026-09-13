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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/api"
	"github.com/piwi3910/nexora/mgmt/internal/auth"
	"github.com/piwi3910/nexora/mgmt/internal/blocklist"
	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/dnssec"
	"github.com/piwi3910/nexora/mgmt/internal/dynupdate"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/querylog"
	"github.com/piwi3910/nexora/mgmt/internal/secrets"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/stats"
	"github.com/piwi3910/nexora/mgmt/internal/store"
	"github.com/piwi3910/nexora/mgmt/internal/tsigkey"
	"github.com/piwi3910/nexora/mgmt/internal/xfrin"
	"github.com/piwi3910/nexora/mgmt/internal/zone"
)

// version is set at build time with -ldflags "-X main.version=<tag>"; main hands it to the API's
// health report.
var version = "dev"

const usage = "usage: nexora-mgmt serve | version | migrate | ca init --out <dir> | ca issue-dns --ca-cert F --ca-key F --names N[,N...] [--days 90] --out <dir> | user create --admin --username U --email E --password-file F"

func main() {
	api.Version = version
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
	case len(args) >= 2 && args[0] == "ca" && args[1] == "issue-dns":
		err = caIssueDNS(args[2:], stdout)
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

func caIssueDNS(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("ca issue-dns", flag.ContinueOnError)
	caCert := fs.String("ca-cert", "", "CA certificate file")
	caKey := fs.String("ca-key", "", "CA private key file")
	namesFlag := fs.String("names", "", "comma-separated DNS names and IP addresses")
	days := fs.Int("days", 90, "validity in days")
	out := fs.String("out", "", "directory for tls.crt and tls.key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var names []string
	for _, n := range strings.Split(*namesFlag, ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	if *caCert == "" || *caKey == "" || len(names) == 0 || *days <= 0 || *out == "" || fs.NArg() != 0 {
		return errors.New("usage: nexora-mgmt ca issue-dns --ca-cert F --ca-key F --names N[,N...] [--days 90] --out <dir>")
	}
	ca, err := pki.LoadCA(*caCert, *caKey)
	if err != nil {
		return err
	}
	now := time.Now()
	chain, key, err := ca.IssueDNSServerCert(names, time.Duration(*days)*24*time.Hour, now)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o700); err != nil {
		return err
	}
	certPath, keyPath := filepath.Join(*out, "tls.crt"), filepath.Join(*out, "tls.key")
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(certPath, chain, 0o644); err != nil {
		return err
	}
	m, err := pki.LoadDNSTLS(certPath, keyPath, now)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s and %s (fingerprint %s)\n", certPath, keyPath, m.FingerprintSHA256)
	return nil
}

// ensureHSMWrapKey creates the PKCS#11 envelope wrap key while holding a cluster-wide advisory
// lock, so instances starting together never create two (no-op without a token).
func ensureHSMWrapKey(ctx context.Context, st *store.Store, box *secrets.Box) error {
	if !box.HasBackend(secrets.BackendPKCS11) {
		return nil
	}
	conn, err := st.Pool.Acquire(ctx)
	if err != nil {
		return store.MapError(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "select pg_advisory_lock(hashtext('pkcs11_wrap_key'))"); err != nil {
		return store.MapError(err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "select pg_advisory_unlock(hashtext('pkcs11_wrap_key'))")
	}()
	if err := box.EnsureHSMWrapKey(ctx); err != nil {
		return fmt.Errorf("PKCS#11 wrap key: %w", err)
	}
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
	box, err := secrets.Open(secrets.Config{KEKFile: cfg.KEKFile, PKCS11Module: cfg.PKCS11Module, PKCS11TokenLabel: cfg.PKCS11TokenLabel, PKCS11PinFile: cfg.PKCS11PinFile})
	if err != nil {
		return err
	}
	defer func() { _ = box.Close() }()
	if !box.Configured() {
		log.Printf("key storage: none configured; RPZ TSIG secrets, TSIG keys and DNSSEC signing are refused")
	}
	if err := ensureHSMWrapKey(ctx, st, box); err != nil {
		return err
	}
	hub := control.NewHub(st, instanceID)
	hub.RPZTsig = control.NewRPZTsig(st, box)
	hub.TSIGKeys = control.NewTSIGKeys(st, box)
	go func() { _ = hub.Run(ctx) }()
	go snapshot.RunNTAExpiry(ctx, st, build)

	fetcher := blocklist.NewFetcher(st, build, &http.Client{})
	go fetcher.Run(ctx)

	zones := &zone.Service{Store: st, Build: build, Signer: &dnssec.Store{Box: box}, Now: time.Now}
	zoneDNSSEC := &dnssec.Service{Store: st, Box: box, Zones: zones}
	go func() { _ = (&dnssec.Maintainer{Store: st, Service: zoneDNSSEC, Tick: 5 * time.Second}).Run(ctx) }()
	tsigKeys := &tsigkey.Service{Store: st, Build: build, Box: box}
	refresher := &xfrin.Refresher{Store: st, Zones: zones, TSIG: tsigKeys, Now: time.Now, Dial: 5 * time.Second}
	scheduler := &xfrin.Scheduler{Store: st, Refresher: refresher, Tick: 5 * time.Second}
	go func() { _ = scheduler.Run(ctx) }()

	authSvc := auth.NewService(st, cfg.SecureCookies)
	if token, created, err := authSvc.EnsureSetupToken(ctx, instanceID); err != nil {
		return fmt.Errorf("setup token: %w", err)
	} else if created {
		log.Printf("setup token: %s", token)
	}
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		stats.NewCollector(st), pki.DNSTLSReloadErrors, pki.DNSTLSNotAfter, xfrin.NotifyIgnored)
	dnsTLS := control.NewDNSTLSFanout()
	if cfg.DNSTLSCertFile != "" {
		go pki.NewDNSTLSWatcher(cfg.DNSTLSCertFile, cfg.DNSTLSKeyFile, cfg.DNSTLSReloadInterval).Run(ctx, dnsTLS.Set)
	}
	var queryLog querylog.Backend
	var builtinLog *querylog.Builtin
	if cfg.QueryLogBackend == "opensearch" {
		if queryLog, err = querylog.NewOpenSearch(cfg.OpenSearch); err != nil {
			return err
		}
	} else {
		builtinLog = querylog.NewBuiltin(cfg.QueryLogBuiltinCapacity)
		queryLog = builtinLog
	}
	httpSrv := &http.Server{
		Handler: api.NewHandler(api.Deps{
			Store: st, Auth: authSvc, OIDC: auth.NewOIDC(cfg.OIDC, cfg.PublicURL, st), CA: ca, Build: build,
			QueryLog: queryLog, InstanceID: instanceID, PublicURL: cfg.PublicURL,
			Metrics: promhttp.HandlerFor(reg, promhttp.HandlerOpts{}), HTTPMetrics: api.NewMetrics(reg),
			RefreshFilterList: fetcher.RefreshNow, DNSTLS: dnsTLS, Secrets: box,
			Zones: zones, TSIGKeys: tsigKeys, ZoneDNSSEC: zoneDNSSEC,
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
	controlServer := control.NewServer(st, ca, hub, instanceID, dnsTLS)
	controlServer.OnStats = func(ctx context.Context, engineID string, s *controlv1.Stats) {
		_ = stats.Record(ctx, st, engineID, s)
		_ = stats.RecordM3(ctx, st, engineID, s)
	}
	controlServer.OnNotify = func(ctx context.Context, _ string, ev *controlv1.NotifyReceived) error {
		return scheduler.Notify(ctx, ev.Zone, ev.Source)
	}
	controlServer.OnUpdate = (&dynupdate.Applier{Zones: zones, TSIG: tsigKeys, Now: time.Now, TSIGCheck: true}).Apply
	controlv1.RegisterEngineControlServer(srv, controlServer)
	if builtinLog != nil {
		collogspb.RegisterLogsServiceServer(srv, builtinLog)
	}
	lis, err := net.Listen("tcp", cfg.GRPCListen)
	if err != nil {
		_ = httpLis.Close()
		return err
	}
	fmt.Fprintf(stdout, "grpc listening on %s\n", lis.Addr())
	// One machine-readable line with both bound addresses, so callers can listen on port 0.
	fmt.Fprintf(stdout, "READY http=%s grpc=%s\n", httpLis.Addr(), lis.Addr())
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
