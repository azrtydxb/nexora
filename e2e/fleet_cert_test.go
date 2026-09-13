package e2e

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/piwi3910/nexora/e2e/harness"
)

// exportAs calls the builtin OTLP logs service with an engine's own identity files.
func exportAs(t *testing.T, mg *harness.Mgmt, en *harness.Engine) error {
	t.Helper()
	id := filepath.Join(en.StateDir, "identity")
	pair, err := tls.LoadX509KeyPair(filepath.Join(id, "cert.pem"), filepath.Join(id, "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	// The server's identity is not under test here; the client certificate is.
	creds := credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{pair}, InsecureSkipVerify: true}) //nolint:gosec
	conn, err := grpc.NewClient(mg.GRPCAddr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = collogspb.NewLogsServiceClient(conn).Export(ctx, &collogspb.ExportLogsServiceRequest{})
	return err
}

func TestEngineCertRevocation(t *testing.T) {
	ctx := context.Background()
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	mg := env.StartMgmt(pg, ca, harness.MgmtOptions{ExtraEnv: []string{"NEXORA_ENGINE_CERT_TTL=60s"}})
	api := harness.Bootstrap(t, env, mg.SetupToken(t), mg.BaseURL)
	api.DisableForwardedValidation()
	fx := env.StartDNSFixture()
	createUDPUpstream(t, api, "fixture", fx.UDP)
	names := []string{"rev-1", "rev-2", "rev-3"}
	engines := map[string]*harness.Engine{}
	token := api.CreateJoinToken()
	for _, n := range names {
		engines[n] = env.StartManagedEngine(n, []string{mg.GRPCURL}, token)
	}
	rewrite(t, api, "rev.fleet.test", "192.0.2.30", nil)
	waitLatestApplied(t, api, names...)

	if err := exportAs(t, mg, engines["rev-1"]); err != nil {
		t.Fatalf("rev-1 identity rejected before revocation: %v", err)
	}
	api.Must(http.MethodPost, "/engines/"+api.EngineByNode("rev-1").ID+"/revoke", nil, nil, http.StatusOK)
	harness.Eventually(t, 15*time.Second, func() error {
		if s := api.EngineByNode("rev-1").Status; s != "revoked" {
			return fmt.Errorf("status %s", s)
		}
		if engines["rev-1"].Metric(t, "nexora_control_revoked", nil) != 1 || engines["rev-1"].Metric(t, "nexora_control_connected", nil) != 0 {
			return fmt.Errorf("rev-1 has not observed its revocation")
		}
		if s, _ := status.FromError(exportAs(t, mg, engines["rev-1"])); s.Code() != codes.PermissionDenied || s.Message() != "certificate revoked" {
			return fmt.Errorf("export as rev-1: %v", s)
		}
		return nil
	})
	wantA(t, harness.MustQuery(t, engines["rev-1"].DNS, "rev.fleet.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.30")

	applied := api.EngineByNode("rev-1").AppliedVersion
	rewrite(t, api, "post-revoke.fleet.test", "192.0.2.31", nil)
	waitLatestApplied(t, api, "rev-2", "rev-3")
	wantA(t, harness.MustQuery(t, engines["rev-2"].DNS, "post-revoke.fleet.test.", dns.TypeA, harness.QueryOpts{}), "192.0.2.31")
	if got := api.EngineByNode("rev-1").AppliedVersion; got != applied {
		t.Fatalf("revoked engine applied %d, want %d", got, applied)
	}
	if got := aValues(harness.MustQuery(t, engines["rev-1"].DNS, "post-revoke.fleet.test.", dns.TypeA, harness.QueryOpts{})); len(got) > 0 && got[0] == "192.0.2.31" {
		t.Fatal("revoked engine received configuration after revocation")
	}

	conn, err := pgx.Connect(ctx, pg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	reason := func(serial string) string {
		var r *string
		_ = conn.QueryRow(ctx, `select revoke_reason from engine_certificates where serial = $1`, serial).Scan(&r)
		if r == nil {
			return ""
		}
		return *r
	}
	rev2 := api.EngineByNode("rev-2")
	api.Must(http.MethodPost, "/engines/"+rev2.ID+"/rotate-certificate", nil, nil, http.StatusAccepted)
	harness.Eventually(t, 30*time.Second, func() error {
		e := api.EngineByNode("rev-2")
		if !e.Connected || e.CertificateSerial == rev2.CertificateSerial || reason(rev2.CertificateSerial) != "superseded" {
			return fmt.Errorf("rev-2 not rotated yet: %+v", e)
		}
		return nil
	})

	s3 := api.EngineByNode("rev-3").CertificateSerial
	harness.Eventually(t, 75*time.Second, func() error {
		if e := api.EngineByNode("rev-3"); e.CertificateSerial == s3 || !e.Connected {
			return fmt.Errorf("rev-3 has not renewed on its own")
		}
		return nil
	})

	if v := mg.Metric(t, "nexora_mgmt_engines_disconnected", nil); v != 0 {
		t.Fatalf("disconnected gauge %v with every non-revoked engine connected", v)
	}
	engines["rev-3"].Proc.Stop()
	harness.Eventually(t, 110*time.Second, func() error {
		if v := mg.Metric(t, "nexora_mgmt_engines_disconnected", nil); v != 1 {
			return fmt.Errorf("nexora_mgmt_engines_disconnected = %v, want 1", v)
		}
		return nil
	})
}
