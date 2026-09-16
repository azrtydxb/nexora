package kwrollout

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManagementClientAuthenticatesWithoutLeakingSecrets(t *testing.T) {
	for _, mode := range []string{"ok", "wrong-ca", "redirect", "denied", "no-cookie", "missing-password", "malformed-secret"} {
		t.Run(mode, func(t *testing.T) {
			const password = "private-test-password-not-for-errors"
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/auth/login" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				var login map[string]string
				if json.NewDecoder(r.Body).Decode(&login) != nil || login["username"] != "admin" || login["password"] != password {
					t.Error("incorrect login payload")
				}
				switch mode {
				case "redirect":
					http.Redirect(w, r, "/leaked", http.StatusTemporaryRedirect)
				case "denied":
					http.Error(w, password, http.StatusUnauthorized)
				case "no-cookie":
					w.WriteHeader(http.StatusOK)
				default:
					http.SetCookie(w, &http.Cookie{Name: "session", Value: "test-session", Path: "/", Secure: true, HttpOnly: true})
				}
			}))
			defer server.Close()
			ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
			client, err := managementClient(context.Background(), func(_ context.Context, _ []byte, args ...string) ([]byte, error) {
				if args[0] != "get" || args[1] != "secret" {
					return nil, fmt.Errorf("unexpected command")
				}
				data := map[string][]byte{"ca.crt": ca}
				if args[2] == "nexora-admin" {
					data = map[string][]byte{"username": []byte("admin"), "password": []byte(password)}
					if mode == "missing-password" {
						delete(data, "password")
					}
				}
				if mode == "wrong-ca" {
					data["ca.crt"] = []byte("invalid")
				}
				if mode == "malformed-secret" {
					return []byte(password), nil
				}
				return json.Marshal(map[string]any{"data": data})
			}, server.URL)
			if mode == "ok" {
				if err != nil || client == nil {
					t.Fatalf("authentication failed: %v", err)
				}
				client.CloseIdleConnections()
			} else if err == nil || strings.Contains(err.Error(), password) {
				t.Fatalf("bad auth handling: %v", err)
			}
		})
	}
}

func TestManagementClientRejectsUnsafeOriginBeforeReadingSecrets(t *testing.T) {
	for _, origin := range []string{"http://host", "https://user:pass@host", "https://host/path", "https://host?token=x", "https://host/#fragment"} {
		_, err := managementClient(context.Background(), func(context.Context, []byte, ...string) ([]byte, error) {
			t.Fatal("read secrets for invalid origin")
			return nil, nil
		}, origin)
		if err == nil {
			t.Fatal("invalid origin accepted")
		}
	}
}
