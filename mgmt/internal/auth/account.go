package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // time_zone validation must not depend on the image shipping zoneinfo
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Account self-service errors.
var (
	ErrTooManyAttempts           = errors.New("too many failed attempts; try again later")
	ErrInvalidCurrentPassword    = errors.New("current password is wrong")
	ErrManagedByIdentityProvider = errors.New("managed by your identity provider")
)

// ValidationError is self-service input the service rejects; its text is safe to show.
type ValidationError string

func (e ValidationError) Error() string { return string(e) }

// Failed-attempt throttle: more than maxAuthFailures failed logins or password changes for one
// username from one client address within authFailureWindow give ErrTooManyAttempts. The client
// address is part of the key so a remote attacker cannot lock a user out everywhere.
// debt: the address is the TCP peer; behind a reverse proxy every client shares the proxy's
// address, so the throttle degrades to per username. Revisit when a trusted-proxy setting exists.
const (
	maxAuthFailures      = 10
	authFailureWindowSQL = "interval '15 minutes'"
	maxDisplayNameRunes  = 64
)

// Preferences are a user's GUI settings.
type Preferences struct {
	Theme        string `json:"theme"`
	TimeZone     string `json:"time_zone"`
	Clock24h     bool   `json:"clock_24h"`
	QuerylogLive bool   `json:"querylog_live"`
}

// DefaultPreferences are the settings of a user who never saved any.
func DefaultPreferences() Preferences {
	return Preferences{Theme: "system", QuerylogLive: true}
}

// ProfileUpdate is a self-service profile change; nil fields keep their stored value.
type ProfileUpdate struct {
	Revision           int64
	Email, DisplayName *string
	Preferences        *Preferences
}

// throttled returns ErrTooManyAttempts when username from client failed too often recently.
func (s *Service) throttled(ctx context.Context, username, client string) error {
	var n int
	err := s.st.Pool.QueryRow(ctx, `select count(*) from auth_failures
		where username = lower(left($1, 64)) and client = $2 and at > now() - `+authFailureWindowSQL, username, client).Scan(&n)
	if err != nil {
		return store.MapError(err)
	}
	if n > maxAuthFailures {
		return ErrTooManyAttempts
	}
	return nil
}

// failed records a failed attempt of username from client, pruning expired rows in the same
// statement, and returns cause (or the database error).
func (s *Service) failed(ctx context.Context, username, client string, cause error) error {
	_, err := s.st.Pool.Exec(ctx, `with d as (delete from auth_failures where at < now() - `+authFailureWindowSQL+`)
		insert into auth_failures(username, client) values (lower(left($1, 64)), $2)`, username, client)
	if err != nil {
		return store.MapError(err)
	}
	return cause
}

func validatePreferences(p Preferences) error {
	switch p.Theme {
	case "system", "light", "dark":
	default:
		return ValidationError("theme must be system, light or dark")
	}
	if p.TimeZone == "" {
		return nil
	}
	if p.TimeZone == "Local" || len(p.TimeZone) > 64 {
		return ValidationError("time_zone must be an IANA time zone name")
	}
	if _, err := time.LoadLocation(p.TimeZone); err != nil {
		return ValidationError("time_zone must be an IANA time zone name")
	}
	return nil
}

// UpdateProfile changes the caller's email, display name and preferences. Users from the identity
// provider cannot change email or display name.
func (s *Service) UpdateProfile(ctx context.Context, p Principal, in ProfileUpdate) (User, error) {
	var after User
	err := s.st.InTx(ctx, func(tx pgx.Tx) error {
		before, err := ScanUser(tx.QueryRow(ctx, "select "+UserColumns+" from users where id = $1 for update", p.UserID))
		if err != nil {
			return store.MapError(err)
		}
		if before.Revision != in.Revision {
			return fmt.Errorf("%w: revision %d is stale (current %d); reload and retry", store.ErrConflict, in.Revision, before.Revision)
		}
		email, name, prefs := before.Email, before.DisplayName, before.Preferences
		if in.Email != nil {
			email = strings.TrimSpace(*in.Email)
		}
		if in.DisplayName != nil {
			name = strings.TrimSpace(*in.DisplayName)
		}
		if in.Preferences != nil {
			prefs = *in.Preferences
		}
		if before.Source != "local" && (email != before.Email || name != before.DisplayName) {
			return ErrManagedByIdentityProvider
		}
		if utf8.RuneCountInString(name) > maxDisplayNameRunes {
			return ValidationError(fmt.Sprintf("display_name must be at most %d characters", maxDisplayNameRunes))
		}
		if err := validatePreferences(prefs); err != nil {
			return err
		}
		after, err = ScanUser(tx.QueryRow(ctx, `update users set email = $2, display_name = $3, preferences = $4,
			revision = revision + 1, updated_at = now() where id = $1 returning `+UserColumns, p.UserID, email, name, prefs))
		if err != nil {
			return store.MapError(err)
		}
		profile := func(u User) map[string]any {
			return map[string]any{"email": u.Email, "display_name": u.DisplayName, "preferences": u.Preferences}
		}
		return WriteAudit(ctx, tx, p.Actor(), Change{Action: "updateCurrentUser", TargetType: "user", TargetID: before.ID,
			Before: profile(before), After: profile(after)}, nil)
	})
	return after, err
}

// ChangePassword replaces the caller's local password after verifying the current one. Only a
// session may do this; with revokeOthers every other session of the user ends (sessionToken is
// the caller's own). Wrong current passwords count against the login throttle for client.
func (s *Service) ChangePassword(ctx context.Context, p Principal, client, sessionToken, current, next string, revokeOthers bool) error {
	if p.Kind != "session" {
		return fmt.Errorf("%w: changing the password requires a signed-in session", ErrForbidden)
	}
	var source, username string
	var hash *string
	err := s.st.Pool.QueryRow(ctx, "select source, username, password_hash from users where id = $1", p.UserID).
		Scan(&source, &username, &hash)
	if err != nil {
		return store.MapError(err)
	}
	if source != "local" || hash == nil {
		return ErrManagedByIdentityProvider
	}
	if err := s.throttled(ctx, username, client); err != nil {
		return err
	}
	if ok, err := VerifyPassword(*hash, current); err != nil || !ok {
		return s.failed(ctx, username, client, ErrInvalidCurrentPassword)
	}
	if len(next) < MinPasswordLength {
		return ErrWeakPassword
	}
	if next == current {
		return ValidationError("the new password must differ from the current one")
	}
	newHash, err := HashPassword(next)
	if err != nil {
		return err
	}
	return s.st.InTx(ctx, func(tx pgx.Tx) error {
		// Conditional on the verified hash, so a concurrent change is not silently overwritten.
		tag, err := tx.Exec(ctx, "update users set password_hash = $2, updated_at = now() where id = $1 and password_hash = $3",
			p.UserID, newHash, *hash)
		if err != nil {
			return store.MapError(err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: the password changed concurrently; reload and retry", store.ErrConflict)
		}
		if revokeOthers {
			if _, err := tx.Exec(ctx, "delete from sessions where user_id = $1 and token_hash <> $2", p.UserID, hashToken(sessionToken)); err != nil {
				return store.MapError(err)
			}
		}
		return WriteAudit(ctx, tx, p.Actor(), Change{Action: "changeOwnPassword", TargetType: "user", TargetID: p.UserID,
			After: map[string]any{"revoked_other_sessions": revokeOthers}}, nil)
	})
}
