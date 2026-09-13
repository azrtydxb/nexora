package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
)

type oidcUser struct {
	Username string   `json:"username"`
	Email    string   `json:"email"`
	Groups   []string `json:"groups"`
}

type authCode struct {
	user                                    oidcUser
	clientID, redirectURI, nonce, challenge string
}

type oidcProvider struct {
	issuer, clientID, clientSecret string
	users                          map[string]oidcUser
	signer                         jose.Signer
	jwks                           jose.JSONWebKeySet

	mu     sync.Mutex
	codes  map[string]authCode
	access map[string]oidcUser
}

var authorizePage = template.Must(template.New("authorize").Parse(`<!doctype html>
<title>nexora-fixture sign in</title>
<form method="post" action="/authorize">
<input type="hidden" name="client_id" value="{{.ClientID}}">
<input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
<input type="hidden" name="state" value="{{.State}}">
<input type="hidden" name="nonce" value="{{.Nonce}}">
<input type="hidden" name="code_challenge" value="{{.Challenge}}">
{{range .Users}}<button name="user" value="{{.Username}}">Sign in as {{.Username}}</button>
{{end}}</form>
`))

func runOIDC(args []string) (func(), string, error) {
	fs := flag.NewFlagSet("oidc", flag.ContinueOnError)
	listen := fs.String("listen", "", "listen address")
	clientID := fs.String("client-id", "", "client id")
	secretFile := fs.String("client-secret-file", "", "client secret file")
	usersFile := fs.String("users-file", "", "users JSON file")
	if err := fs.Parse(args); err != nil {
		return nil, "", err
	}
	if *listen == "" || *clientID == "" || *secretFile == "" || *usersFile == "" {
		return nil, "", errors.New("--listen, --client-id, --client-secret-file and --users-file are required")
	}
	secret, err := os.ReadFile(*secretFile)
	if err != nil {
		return nil, "", err
	}
	raw, err := os.ReadFile(*usersFile)
	if err != nil {
		return nil, "", err
	}
	var users []oidcUser
	if err := json.Unmarshal(raw, &users); err != nil {
		return nil, "", err
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, "", err
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jose.JSONWebKey{Key: key, KeyID: "fixture"}},
		(&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return nil, "", err
	}
	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		return nil, "", err
	}
	p := &oidcProvider{
		issuer: "http://" + lis.Addr().String(), clientID: *clientID, clientSecret: strings.TrimSpace(string(secret)),
		users: map[string]oidcUser{}, signer: signer,
		jwks:  jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "fixture", Algorithm: "RS256", Use: "sig"}}},
		codes: map[string]authCode{}, access: map[string]oidcUser{},
	}
	for _, u := range users {
		p.users[u.Username] = u
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, p.jwks) })
	mux.HandleFunc("GET /authorize", p.authorizeForm)
	mux.HandleFunc("POST /authorize", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	mux.HandleFunc("GET /userinfo", p.userinfo)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(lis) }()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}, "http=" + lis.Addr().String(), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func randomString() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func (p *oidcProvider) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                p.issuer,
		"authorization_endpoint":                p.issuer + "/authorize",
		"token_endpoint":                        p.issuer + "/token",
		"jwks_uri":                              p.issuer + "/jwks",
		"userinfo_endpoint":                     p.issuer + "/userinfo",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
	})
}

func (p *oidcProvider) authorizeForm(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != p.clientID || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "unknown client or missing S256 PKCE", http.StatusBadRequest)
		return
	}
	users := make([]oidcUser, 0, len(p.users))
	for _, u := range p.users {
		users = append(users, u)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = authorizePage.Execute(w, map[string]any{
		"ClientID": q.Get("client_id"), "RedirectURI": q.Get("redirect_uri"), "State": q.Get("state"),
		"Nonce": q.Get("nonce"), "Challenge": q.Get("code_challenge"), "Users": users,
	})
}

func (p *oidcProvider) authorize(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	user, ok := p.users[r.PostForm.Get("user")]
	redirect, err := url.Parse(r.PostForm.Get("redirect_uri"))
	if !ok || err != nil || r.PostForm.Get("client_id") != p.clientID || r.PostForm.Get("code_challenge") == "" {
		http.Error(w, "unknown user or client, bad redirect_uri or missing code_challenge", http.StatusBadRequest)
		return
	}
	code := randomString()
	p.mu.Lock()
	p.codes[code] = authCode{user: user, clientID: p.clientID, redirectURI: redirect.String(),
		nonce: r.PostForm.Get("nonce"), challenge: r.PostForm.Get("code_challenge")}
	p.mu.Unlock()
	q := redirect.Query()
	q.Set("code", code)
	q.Set("state", r.PostForm.Get("state"))
	redirect.RawQuery = q.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (p *oidcProvider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	id, secret, basic := r.BasicAuth()
	if basic {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != p.clientID || subtle.ConstantTimeCompare([]byte(secret), []byte(p.clientSecret)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	p.mu.Lock()
	c, ok := p.codes[r.PostForm.Get("code")]
	delete(p.codes, r.PostForm.Get("code"))
	p.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("redirect_uri") != c.redirectURI ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	now := time.Now()
	claims := map[string]any{
		"iss": p.issuer, "aud": p.clientID, "sub": "sub-" + c.user.Username, "email": c.user.Email,
		"preferred_username": c.user.Username, "groups": c.user.Groups, "nonce": c.nonce,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	}
	payload, _ := json.Marshal(claims)
	jws, err := p.signer.Sign(payload)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	idToken, _ := jws.CompactSerialize()
	access := randomString()
	p.mu.Lock()
	p.access[access] = c.user
	p.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": 300, "id_token": idToken})
}

func (p *oidcProvider) userinfo(w http.ResponseWriter, r *http.Request) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	p.mu.Lock()
	u, ok := p.access[token]
	p.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sub": "sub-" + u.Username, "email": u.Email,
		"preferred_username": u.Username, "groups": u.Groups})
}
