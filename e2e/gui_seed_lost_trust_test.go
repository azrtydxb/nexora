package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
)

const lostTrustZone = "lost-trust.gui.test."
const lostTrustEngine = "gui-lost-trust-telemetry"

func init() { registerGUISeed(seedLostTrust) }

// seedLostTrust only changes a disconnected fixture engine's telemetry. It never
// changes configured trust anchors, validation settings, or a real engine's state.
// No heartbeat can overwrite these reports between the browser's assertions.
func seedLostTrust(s guiSeedEnv) {
	group := s.Admin.CreateEngineGroup(map[string]any{"name": lostTrustEngine})
	engineID := uuid.NewString()
	harness.PGExec(s.T, s.PGURL, `insert into engines(id, node_name, certificate_serial, engine_group_id)
  values ($1, $2, $2, $3)`, engineID, lostTrustEngine, group.ID)
	raw, err := lostTrustReport("valid")
	if err != nil {
		s.T.Fatal(err)
	}
	harness.PGExec(s.T, s.PGURL, `insert into engine_dnssec_status(engine_id, stats, reported_at) values ($1, $2, now())`, engineID, raw)
	token := uuid.NewString()
	controller := httptest.NewServer(lostTrustController(token, func(ctx context.Context, raw []byte) error {
		conn, err := pgx.Connect(ctx, s.PGURL)
		if err != nil {
			return err
		}
		defer conn.Close(context.Background())
		tag, err := conn.Exec(ctx, `update engine_dnssec_status set stats = $2, reported_at = now() where engine_id = $1`, engineID, raw)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("fixture status row missing")
		}
		return nil
	}))
	s.T.Cleanup(controller.Close)
	s.Vars["NEXORA_E2E_LOST_TRUST_CONTROL"] = controller.URL
	s.Vars["NEXORA_E2E_LOST_TRUST_TOKEN"] = token
	s.Vars["NEXORA_E2E_LOST_TRUST_ENGINE_ID"] = engineID
}

// Report a revoked last key, then an untrusted replacement in hold-down, then
// a trusted replacement. Keep the revoked key present during recovery so the
// test cannot pass merely because the status row or revoked key disappeared.
func lostTrustReport(state string) ([]byte, error) {
	key := &controlv1.TrustAnchorStatus{Zone: lostTrustZone, KeyTag: 12345, Algorithm: 13,
		State: controlv1.TrustAnchorState_TRUST_ANCHOR_STATE_VALID}
	report := &controlv1.DnssecStats{TrustAnchors: []*controlv1.TrustAnchorStatus{key}}
	switch state {
	case "valid":
	case "lost", "pending", "recovered":
		key.State = controlv1.TrustAnchorState_TRUST_ANCHOR_STATE_REVOKED
		if state != "lost" {
			replacement := &controlv1.TrustAnchorStatus{Zone: lostTrustZone, KeyTag: 23456, Algorithm: 13,
				State: controlv1.TrustAnchorState_TRUST_ANCHOR_STATE_ADD_PEND}
			if state == "recovered" {
				replacement.State = controlv1.TrustAnchorState_TRUST_ANCHOR_STATE_VALID
			}
			report.TrustAnchors = append(report.TrustAnchors, replacement)
		}
	default:
		return nil, fmt.Errorf("unknown fixture state")
	}
	return protojson.Marshal(report)
}

func lostTrustController(token string, write func(context.Context, []byte) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("Origin") != "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		raw, err := lostTrustReport(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err := write(ctx, raw); err != nil {
			http.Error(w, "fixture status write failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func TestLostTrustFixtureTransitions(t *testing.T) {
	for _, state := range []string{"valid", "lost", "pending", "recovered"} {
		t.Run(state, func(t *testing.T) {
			var report controlv1.DnssecStats
			writes := 0
			h := lostTrustController("fixture-token", func(_ context.Context, raw []byte) error {
				writes++
				return protojson.Unmarshal(raw, &report)
			})
			r := httptest.NewRequest(http.MethodPost, "/"+state, nil)
			r.Header.Set("Authorization", "Bearer fixture-token")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 204 || writes != 1 {
				t.Fatalf("status=%d writes=%d", w.Code, writes)
			}
			wantCount := 1
			if state == "pending" || state == "recovered" {
				wantCount = 2
			}
			if len(report.TrustAnchors) != wantCount {
				t.Fatalf("anchors=%v", report.TrustAnchors)
			}
			for _, key := range report.TrustAnchors {
				if key.Zone != lostTrustZone {
					t.Fatalf("fixture escaped test zone: %q", key.Zone)
				}
			}
			wantFirst := controlv1.TrustAnchorState_TRUST_ANCHOR_STATE_REVOKED
			if state == "valid" {
				wantFirst = controlv1.TrustAnchorState_TRUST_ANCHOR_STATE_VALID
			}
			if report.TrustAnchors[0].State != wantFirst {
				t.Fatalf("first key=%v", report.TrustAnchors[0])
			}
			if wantCount == 2 {
				want := controlv1.TrustAnchorState_TRUST_ANCHOR_STATE_ADD_PEND
				if state == "recovered" {
					want = controlv1.TrustAnchorState_TRUST_ANCHOR_STATE_VALID
				}
				if report.TrustAnchors[1].State != want {
					t.Fatalf("replacement=%v", report.TrustAnchors[1])
				}
			}
		})
	}
}

func TestLostTrustFixtureRejectsInvalidControl(t *testing.T) {
	for _, tc := range []struct {
		method, path, token, origin string
		want                        int
	}{
		{"GET", "/lost", "fixture-token", "", 405},
		{"POST", "/lost", "wrong", "", 403},
		{"POST", "/lost", "fixture-token", "https://example.test", 403},
		{"POST", "/arbitrary", "fixture-token", "", 400},
	} {
		h := lostTrustController("fixture-token", func(context.Context, []byte) error { t.Error("unexpected write"); return nil })
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%+v: got %d", tc, w.Code)
		}
	}
	h := lostTrustController("fixture-token", func(context.Context, []byte) error { return fmt.Errorf("write failed") })
	r := httptest.NewRequest("POST", "/lost", nil)
	r.Header.Set("Authorization", "Bearer fixture-token")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 500 {
		t.Fatalf("failed write returned %d", w.Code)
	}
}

// TestGUILostTrustRecovery runs the one screen with a fresh management database,
// without starting a resolver or borrowing the state of the wider GUI suite.
func TestGUILostTrustRecovery(t *testing.T) {
	env := harness.New(t)
	pg := env.StartPostgres()
	ca := env.InitCA()
	mgmt := env.StartMgmt(pg, ca, harness.MgmtOptions{})
	admin := harness.Bootstrap(t, env, mgmt.SetupToken(t), mgmt.BaseURL)
	admin.Must("POST", "/users", map[string]any{"username": "trust-operator", "email": "trust@example.test", "password": "trust-operator-password", "role": "operator"}, nil, 201)
	vars := map[string]string{
		"NEXORA_E2E_BASE_URL":          mgmt.BaseURL,
		"NEXORA_E2E_OPERATOR_USER":     "trust-operator",
		"NEXORA_E2E_OPERATOR_PASSWORD": "trust-operator-password",
	}
	// These are configuration objects, not fixture telemetry. They must survive
	// loss and recovery unchanged, including the real root anchors and DNSSEC mode.
	paths := []string{"/dnssec/settings", "/dnssec/trust-anchors", "/dnssec/negative-trust-anchors"}
	before := make([]any, len(paths))
	for i, path := range paths {
		admin.Must("GET", path, nil, &before[i], 200)
	}
	seedLostTrust(guiSeedEnv{T: t, Env: env, Mgmt: mgmt, Admin: admin, PGURL: pg.URL, Vars: vars})
	harness.RunPlaywright(t, []string{"e2e/screens/45-lost-trust.spec.ts"}, vars)
	for i, path := range paths {
		var after any
		admin.Must("GET", path, nil, &after, 200)
		if !reflect.DeepEqual(before[i], after) {
			t.Errorf("%s changed during telemetry fixture", path)
		}
	}
}
