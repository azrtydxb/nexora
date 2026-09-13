package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"

	"github.com/piwi3910/nexora/mgmt/internal/config"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

const (
	oidcDiscoveryTimeout = 5 * time.Second
	oidcHTTPTimeout      = 10 * time.Second
	oidcStateTTL         = 10 * time.Minute
)

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

// OIDC performs the authorization-code + PKCE login against the configured identity provider.
type OIDC struct {
	cfg       config.OIDCConfig
	publicURL string
	st        *store.Store

	mu       sync.Mutex
	provider *oidc.Provider
	secret   string
}

// NewOIDC creates the OIDC login. Nothing is contacted until the first login.
func NewOIDC(cfg config.OIDCConfig, publicURL string, st *store.Store) *OIDC {
	return &OIDC{cfg: cfg, publicURL: strings.TrimSuffix(publicURL, "/"), st: st}
}

// DisabledOIDC is the configuration of an instance without OIDC login.
func DisabledOIDC() config.OIDCConfig { return config.OIDCConfig{} }

// Enabled reports whether an issuer is configured.
func (o *OIDC) Enabled() bool { return o.cfg.Enabled() }

// httpContext bounds every call to the identity provider.
func httpContext(ctx context.Context) context.Context {
	return oidc.ClientContext(ctx, &http.Client{Timeout: oidcHTTPTimeout})
}

// oauth returns the discovered provider and client configuration. Discovery is retried on every
// call until it succeeds once; failures yield ErrOIDCUnavailable.
func (o *OIDC) oauth(ctx context.Context) (*oidc.Provider, *oauth2.Config, error) {
	if !o.Enabled() {
		return nil, nil, fmt.Errorf("%w: OIDC is not configured", ErrOIDCUnavailable)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.provider == nil {
		secret, err := os.ReadFile(o.cfg.ClientSecretFile)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: client secret: %v", ErrOIDCUnavailable, err)
		}
		dctx, cancel := context.WithTimeout(httpContext(ctx), oidcDiscoveryTimeout)
		defer cancel()
		p, err := oidc.NewProvider(dctx, o.cfg.Issuer)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrOIDCUnavailable, err)
		}
		o.provider, o.secret = p, strings.TrimSpace(string(secret))
	}
	return o.provider, &oauth2.Config{
		ClientID:     o.cfg.ClientID,
		ClientSecret: o.secret,
		Endpoint:     o.provider.Endpoint(),
		RedirectURL:  o.publicURL + "/api/v1/auth/oidc/callback",
		Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
	}, nil
}

// probe checks that the provider answers its discovery document now. A provider discovered earlier
// may since have gone down; failing here lets the GUI report the outage instead of sending the
// browser to an unreachable identity provider. Logins are rare, so the extra request is cheap.
func (o *OIDC) probe(ctx context.Context) error {
	pctx, cancel := context.WithTimeout(ctx, oidcDiscoveryTimeout)
	defer cancel()
	wellKnown := strings.TrimSuffix(o.cfg.Issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOIDCUnavailable, err)
	}
	resp, err := (&http.Client{Timeout: oidcDiscoveryTimeout}).Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOIDCUnavailable, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: discovery returned %s", ErrOIDCUnavailable, resp.Status)
	}
	return nil
}

// SafeReturnTo returns p when it is a local absolute path, otherwise "/".
func SafeReturnTo(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n") {
		return "/"
	}
	return p
}

// Start stores a login state and returns the provider's authorization URL.
func (o *OIDC) Start(ctx context.Context, returnTo string) (string, error) {
	_, oc, err := o.oauth(ctx)
	if err != nil {
		return "", err
	}
	if err := o.probe(ctx); err != nil {
		return "", err
	}
	state, err := randomToken(32)
	if err != nil {
		return "", err
	}
	nonce, err := randomToken(32)
	if err != nil {
		return "", err
	}
	verifier := oauth2.GenerateVerifier()
	if _, err := o.st.Pool.Exec(ctx, `insert into oidc_login_states(state, nonce, code_verifier, return_to, expires_at)
		values ($1, $2, $3, $4, now() + $5 * interval '1 second')`,
		state, nonce, verifier, SafeReturnTo(returnTo), int64(oidcStateTTL.Seconds())); err != nil {
		return "", store.MapError(err)
	}
	// Expired states of abandoned logins are removed opportunistically.
	_, _ = o.st.Pool.Exec(ctx, "delete from oidc_login_states where expires_at < now()")
	return oc.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}

type oidcClaims struct {
	Email             string   `json:"email"`
	PreferredUsername string   `json:"preferred_username"`
	Groups            []string `json:"groups"`
}

// Callback consumes the login state, exchanges the code, verifies the ID token and upserts the
// user. It returns the user and the path to return to.
func (o *OIDC) Callback(ctx context.Context, state, code string) (User, string, error) {
	var nonce, verifier, returnTo string
	err := o.st.Pool.QueryRow(ctx, `delete from oidc_login_states where state = $1 and expires_at > now()
		returning nonce, code_verifier, return_to`, state).Scan(&nonce, &verifier, &returnTo)
	if err := unauthenticated(err); err != nil {
		return User{}, "", err
	}
	provider, oc, err := o.oauth(ctx)
	if err != nil {
		return User{}, "", err
	}
	hctx := httpContext(ctx)
	tok, err := oc.Exchange(hctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return User{}, "", fmt.Errorf("%w: code exchange: %v", ErrUnauthenticated, err)
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return User{}, "", fmt.Errorf("%w: token response has no id_token", ErrUnauthenticated)
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: o.cfg.ClientID}).Verify(hctx, raw)
	if err != nil {
		return User{}, "", fmt.Errorf("%w: id token: %v", ErrUnauthenticated, err)
	}
	if idToken.Nonce != nonce {
		return User{}, "", fmt.Errorf("%w: id token nonce mismatch", ErrUnauthenticated)
	}
	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		return User{}, "", fmt.Errorf("%w: id token claims: %v", ErrUnauthenticated, err)
	}
	u, err := o.upsertUser(ctx, idToken.Subject, claims)
	if err != nil {
		return User{}, "", err
	}
	if u.Disabled {
		return User{}, "", fmt.Errorf("%w: user is disabled", ErrUnauthenticated)
	}
	return u, returnTo, nil
}

func (o *OIDC) role(groups []string) Role {
	switch {
	case o.cfg.AdminGroup != "" && slices.Contains(groups, o.cfg.AdminGroup):
		return RoleAdmin
	case o.cfg.OperatorGroup != "" && slices.Contains(groups, o.cfg.OperatorGroup):
		return RoleOperator
	}
	return RoleViewer
}

// upsertUser finds the user by subject (refreshing email and role) or creates it, naming it after
// preferred_username with an "-oidc" suffix when that name is taken.
func (o *OIDC) upsertUser(ctx context.Context, subject string, c oidcClaims) (User, error) {
	if subject == "" {
		return User{}, fmt.Errorf("%w: id token has no subject", ErrUnauthenticated)
	}
	role := o.role(c.Groups)
	var u User
	err := o.st.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		u, err = ScanUser(tx.QueryRow(ctx, `update users set email = $2, role = $3, updated_at = now(),
			revision = revision + case when email <> $2 or role <> $3 then 1 else 0 end
			where oidc_subject = $1 returning `+UserColumns, subject, c.Email, string(role)))
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		name := c.PreferredUsername
		if !usernameRE.MatchString(name) {
			name = c.Email
		}
		if !usernameRE.MatchString(name) {
			return fmt.Errorf("%w: no usable preferred_username or email claim", ErrUnauthenticated)
		}
		var taken bool
		if err := tx.QueryRow(ctx, "select exists(select 1 from users where username = $1)", name).Scan(&taken); err != nil {
			return err
		}
		if taken {
			name = name[:min(len(name), 59)] + "-oidc"
		}
		u, err = ScanUser(tx.QueryRow(ctx, `insert into users(username, email, role, source, oidc_subject)
			values ($1, $2, $3, 'oidc', $4) returning `+UserColumns, name, c.Email, string(role), subject))
		return err
	})
	return u, store.MapError(err)
}
