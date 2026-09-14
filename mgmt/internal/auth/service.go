package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Authentication and authorization errors.
var (
	ErrUnauthenticated    = errors.New("unauthenticated")
	ErrForbidden          = errors.New("forbidden")
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrSetupDone          = errors.New("setup already completed or invalid setup token")
	ErrWeakPassword       = fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	ErrOIDCUnavailable    = errors.New("identity provider unavailable")
)

// Session parameters.
const (
	SessionCookieName = "nexora_session"
	SessionTTL        = 12 * time.Hour
	apiTokenPrefix    = "nxt_"
)

// Principal is the authenticated caller of one request.
type Principal struct {
	UserID, Username string
	Role             Role
	Kind             string // session | api_token
	TokenID          string
	TokenName        string
}

// Actor returns the audit identity of p.
func (p Principal) Actor() Actor {
	if p.Kind == "api_token" {
		return Actor{Type: "api_token", ID: p.TokenID, Name: p.Username + "/" + p.TokenName}
	}
	return Actor{Type: "user", ID: p.UserID, Name: p.Username}
}

// User is a stored user without its password hash.
type User struct {
	ID, Username, Email string
	DisplayName         string
	Role                Role
	Source              string
	Disabled            bool
	Revision            int64
	CreatedAt           time.Time
	LastLoginAt         *time.Time
	Preferences         Preferences
}

// APIToken is a stored API token without its secret.
type APIToken struct {
	ID, UserID, Name, Prefix         string
	Role                             Role
	CreatedAt                        time.Time
	ExpiresAt, LastUsedAt, RevokedAt *time.Time
}

// UserColumns is the select list matching ScanUser.
const UserColumns = "id::text, username, email, role, source, disabled, revision, created_at, display_name, last_login_at, preferences"

// ScanUser scans a row selected with UserColumns.
func ScanUser(row pgx.Row) (User, error) { return scanUser(row) }

// scanUser scans UserColumns followed by extra columns. Preference keys missing from the stored
// document keep their defaults.
func scanUser(row pgx.Row, extra ...any) (User, error) {
	var u User
	var role string
	var prefs []byte
	dest := append([]any{&u.ID, &u.Username, &u.Email, &role, &u.Source, &u.Disabled, &u.Revision, &u.CreatedAt,
		&u.DisplayName, &u.LastLoginAt, &prefs}, extra...)
	if err := row.Scan(dest...); err != nil {
		return u, err
	}
	u.Role = Role(role)
	u.Preferences = DefaultPreferences()
	if err := json.Unmarshal(prefs, &u.Preferences); err != nil {
		return u, fmt.Errorf("user %s preferences: %w", u.ID, err)
	}
	return u, nil
}

// Service authenticates requests and manages users, sessions, API tokens and first-run setup.
type Service struct {
	st            *store.Store
	secureCookies bool
}

// NewService creates the authentication service.
func NewService(st *store.Store, secureCookies bool) *Service {
	return &Service{st: st, secureCookies: secureCookies}
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// EnsureSetupToken creates the single setup token when no user exists. created is true only for
// the caller whose insert took effect; that caller must show the token to the operator.
func (s *Service) EnsureSetupToken(ctx context.Context, instanceID string) (string, bool, error) {
	var token string
	var created bool
	err := s.st.InTx(ctx, func(tx pgx.Tx) error {
		token, created = "", false
		var users int
		if err := tx.QueryRow(ctx, "select count(*) from users").Scan(&users); err != nil {
			return err
		}
		if users > 0 {
			return nil
		}
		tok, err := randomToken(32)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `insert into setup_tokens(token_hash, created_by_instance) values ($1, $2)
			on conflict (singleton) do nothing`, hashToken(tok), instanceID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			token, created = tok, true
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return token, created, nil
}

// SetupRequired reports whether no user exists yet.
func (s *Service) SetupRequired(ctx context.Context) (bool, error) {
	var users int
	if err := s.st.Pool.QueryRow(ctx, "select count(*) from users").Scan(&users); err != nil {
		return false, store.MapError(err)
	}
	return users == 0, nil
}

// CompleteSetup consumes the setup token and creates the first admin in one transaction.
func (s *Service) CompleteSetup(ctx context.Context, token, username, email, password string) (User, error) {
	var u User
	err := s.st.InTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "delete from setup_tokens where token_hash = $1", hashToken(token))
		if err != nil {
			return err
		}
		var users int
		if err := tx.QueryRow(ctx, "select count(*) from users").Scan(&users); err != nil {
			return err
		}
		if tag.RowsAffected() != 1 || users > 0 {
			return ErrSetupDone
		}
		u, err = CreateUser(ctx, tx, username, email, password, RoleAdmin)
		if err != nil {
			return err
		}
		return WriteAudit(ctx, tx, Actor{Type: "user", ID: u.ID, Name: u.Username},
			Change{Action: "completeSetup", TargetType: "user", TargetID: u.ID, After: u}, nil)
	})
	return u, err
}

// CreateUser inserts a local user. Passwords shorter than MinPasswordLength yield ErrWeakPassword;
// a taken username yields store.ErrConflict.
func CreateUser(ctx context.Context, tx pgx.Tx, username, email, password string, role Role) (User, error) {
	if len(password) < MinPasswordLength {
		return User{}, ErrWeakPassword
	}
	if _, err := ParseRole(string(role)); err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	u, err := ScanUser(tx.QueryRow(ctx, `insert into users(username, email, password_hash, role, source)
		values ($1, $2, $3, $4, 'local') returning `+UserColumns, username, email, hash, string(role)))
	return u, store.MapError(err)
}

var (
	dummyHashOnce sync.Once
	dummyHash     string
)

// Login verifies a local user's password and creates a session. Unknown users, users without a
// password (OIDC) and disabled users all yield ErrInvalidCredentials after the same argon2 work,
// and each such failure counts against username and client (the caller's address). More than
// maxAuthFailures within authFailureWindow yield ErrTooManyAttempts before any password check.
func (s *Service) Login(ctx context.Context, username, password, client string) (string, User, error) {
	if err := s.throttled(ctx, username, client); err != nil {
		return "", User{}, err
	}
	var hash *string
	u, err := scanUser(s.st.Pool.QueryRow(ctx, "select "+UserColumns+", password_hash from users where username = $1", username), &hash)
	if err = store.MapError(err); err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", User{}, err
	}
	if err != nil || hash == nil {
		dummyHashOnce.Do(func() { dummyHash, _ = HashPassword("nexora-dummy-password") })
		_, _ = VerifyPassword(dummyHash, password)
		return "", User{}, s.failed(ctx, username, client, ErrInvalidCredentials)
	}
	ok, err := VerifyPassword(*hash, password)
	if err != nil || !ok || u.Disabled {
		return "", User{}, s.failed(ctx, username, client, ErrInvalidCredentials)
	}
	if err := s.st.Pool.QueryRow(ctx, "update users set last_login_at = now() where id = $1 returning last_login_at", u.ID).
		Scan(&u.LastLoginAt); err != nil {
		return "", User{}, store.MapError(err)
	}
	sess, err := s.CreateSession(ctx, u.ID)
	if err != nil {
		return "", User{}, err
	}
	return sess, u, nil
}

// CreateSession starts a session for userID and returns its token.
func (s *Service) CreateSession(ctx context.Context, userID string) (string, error) {
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	_, err = s.st.Pool.Exec(ctx, `insert into sessions(token_hash, user_id, expires_at)
		values ($1, $2, now() + $3 * interval '1 second')`, hashToken(token), userID, int64(SessionTTL.Seconds()))
	if err != nil {
		return "", store.MapError(err)
	}
	return token, nil
}

// Logout deletes the session.
func (s *Service) Logout(ctx context.Context, sessionToken string) error {
	_, err := s.st.Pool.Exec(ctx, "delete from sessions where token_hash = $1", hashToken(sessionToken))
	return store.MapError(err)
}

// SessionCookie returns the cookie carrying a session token.
func (s *Service) SessionCookie(token string) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(SessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteLaxMode,
	}
}

// Authenticate identifies the caller by a bearer API token or, without an Authorization header,
// by the session cookie. Database outages are returned as store errors, not ErrUnauthenticated.
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (Principal, error) {
	if h := r.Header.Get("Authorization"); h != "" {
		token, ok := strings.CutPrefix(h, "Bearer ")
		if !ok || !strings.HasPrefix(token, apiTokenPrefix) {
			return Principal{}, ErrUnauthenticated
		}
		return s.authenticateToken(ctx, token)
	}
	c, err := r.Cookie(SessionCookieName)
	if err != nil || c.Value == "" {
		return Principal{}, ErrUnauthenticated
	}
	return s.authenticateSession(ctx, c.Value)
}

func (s *Service) authenticateToken(ctx context.Context, token string) (Principal, error) {
	p := Principal{Kind: "api_token"}
	var tokenRole, userRole string
	err := s.st.Pool.QueryRow(ctx, `select t.id::text, t.name, t.role, u.id::text, u.username, u.role
		from api_tokens t join users u on u.id = t.user_id
		where t.token_hash = $1 and t.revoked_at is null and (t.expires_at is null or t.expires_at > now())
		and not u.disabled`, hashToken(token)).Scan(&p.TokenID, &p.TokenName, &tokenRole, &p.UserID, &p.Username, &userRole)
	if err := unauthenticated(err); err != nil {
		return Principal{}, err
	}
	// A token never carries more than its owner currently has (the owner may have been demoted).
	p.Role = Role(tokenRole)
	if !Role(userRole).AtLeast(p.Role) {
		p.Role = Role(userRole)
	}
	_, err = s.st.Pool.Exec(ctx, `update api_tokens set last_used_at = now()
		where id = $1 and (last_used_at is null or last_used_at < now() - interval '1 minute')`, p.TokenID)
	return p, store.MapError(err)
}

func (s *Service) authenticateSession(ctx context.Context, token string) (Principal, error) {
	p := Principal{Kind: "session"}
	var role string
	hash := hashToken(token)
	err := s.st.Pool.QueryRow(ctx, `select u.id::text, u.username, u.role
		from sessions s join users u on u.id = s.user_id
		where s.token_hash = $1 and s.expires_at > now() and not u.disabled`, hash).Scan(&p.UserID, &p.Username, &role)
	if err := unauthenticated(err); err != nil {
		return Principal{}, err
	}
	p.Role = Role(role)
	_, err = s.st.Pool.Exec(ctx, `update sessions set last_seen_at = now()
		where token_hash = $1 and last_seen_at < now() - interval '1 minute'`, hash)
	return p, store.MapError(err)
}

func unauthenticated(err error) error {
	err = store.MapError(err)
	if errors.Is(err, store.ErrNotFound) {
		return ErrUnauthenticated
	}
	return err
}

// CreateAPIToken mints a token for owner. The token's role must not exceed the owner's.
func (s *Service) CreateAPIToken(ctx context.Context, tx pgx.Tx, owner Principal, name string, role Role, expiresAt *time.Time) (APIToken, string, error) {
	if _, err := ParseRole(string(role)); err != nil {
		return APIToken{}, "", err
	}
	if !owner.Role.AtLeast(role) {
		return APIToken{}, "", fmt.Errorf("%w: a %s cannot create a %s token", ErrForbidden, owner.Role, role)
	}
	if name == "" {
		return APIToken{}, "", errors.New("api token name is required")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return APIToken{}, "", err
	}
	token := apiTokenPrefix + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	t := APIToken{UserID: owner.UserID, Name: name, Prefix: token[:len(apiTokenPrefix)+8], Role: role}
	err := tx.QueryRow(ctx, `insert into api_tokens(user_id, name, prefix, token_hash, role, expires_at)
		values ($1, $2, $3, $4, $5, $6) returning id::text, created_at, expires_at`,
		owner.UserID, name, t.Prefix, hashToken(token), string(role), expiresAt).Scan(&t.ID, &t.CreatedAt, &t.ExpiresAt)
	if err != nil {
		return APIToken{}, "", store.MapError(err)
	}
	return t, token, nil
}
