// Package odoh keeps the Oblivious DoH (RFC 9230) settings and the rotating, sealed key seeds the
// management plane delivers to engines on the control stream.
package odoh

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// ProxyTarget is one ODoH target the proxy role may forward to.
type ProxyTarget struct {
	Host  string `json:"host"`
	CAPEM string `json:"ca_pem"`
}

// Settings is the single odoh_settings row.
type Settings struct {
	TargetEnabled, ProxyEnabled      bool
	ProxyTargets                     []ProxyTarget
	ProxyTimeoutMS, KeyRotationHours int32
	Revision                         int64
	UpdatedAt                        time.Time
}

// ValidationError names the settings field that is out of range.
type ValidationError struct {
	Field, Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

// Validate enforces the ranges of the odoh_settings migration and the proxy target syntax.
func Validate(s Settings) error {
	if s.ProxyTimeoutMS < 100 || s.ProxyTimeoutMS > 10000 {
		return &ValidationError{"proxy_timeout_ms", "must be between 100 and 10000"}
	}
	if s.KeyRotationHours < 1 || s.KeyRotationHours > 720 {
		return &ValidationError{"key_rotation_hours", "must be between 1 and 720"}
	}
	if s.ProxyEnabled && len(s.ProxyTargets) == 0 {
		return &ValidationError{"proxy_targets", "at least one target is required while the proxy is enabled"}
	}
	for i, t := range s.ProxyTargets {
		field := fmt.Sprintf("proxy_targets[%d]", i)
		if err := validateHost(t.Host); err != nil {
			return &ValidationError{field + ".host", err.Error()}
		}
		if err := validateCAPEM(t.CAPEM); err != nil {
			return &ValidationError{field + ".ca_pem", err.Error()}
		}
	}
	return nil
}

// validateHost accepts "name", "name:port" and "[v6]:port" with a port of 1..65535.
func validateHost(host string) error {
	if host == "" {
		return errors.New("is required")
	}
	u, err := url.Parse("https://" + host)
	if err != nil || u.Host != host || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errors.New(`must be "name", "name:port" or "[v6]:port"`)
	}
	if u.Hostname() == "" {
		return errors.New("host name is empty")
	}
	if _, portText, err := net.SplitHostPort(host); err == nil {
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return errors.New("port must be between 1 and 65535")
		}
	} else if u.Port() != "" || host[0] == '[' {
		return errors.New(`must be "name", "name:port" or "[v6]:port"`)
	}
	return nil
}

// validateCAPEM accepts "" or one or more PEM CERTIFICATE blocks that parse, and nothing else.
func validateCAPEM(caPEM string) error {
	if caPEM == "" {
		return nil
	}
	rest, n := []byte(caPEM), 0
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			return fmt.Errorf("PEM block %q is not a CERTIFICATE", block.Type)
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return fmt.Errorf("certificate %d: %v", n+1, err)
		}
		rest, n = next, n+1
	}
	if n == 0 || len(bytes.TrimSpace(rest)) != 0 {
		return errors.New("must be one or more PEM CERTIFICATE blocks")
	}
	return nil
}

const settingsColumns = `target_enabled, proxy_enabled, proxy_targets, proxy_timeout_ms, key_rotation_hours, revision, updated_at`

func scanSettings(row pgx.Row) (Settings, error) {
	var s Settings
	var targets []byte
	if err := row.Scan(&s.TargetEnabled, &s.ProxyEnabled, &targets, &s.ProxyTimeoutMS, &s.KeyRotationHours, &s.Revision, &s.UpdatedAt); err != nil {
		return Settings{}, err
	}
	if err := json.Unmarshal(targets, &s.ProxyTargets); err != nil {
		return Settings{}, fmt.Errorf("odoh proxy_targets: %w", err)
	}
	return s, nil
}

// GetSettings reads the settings row.
func GetSettings(ctx context.Context, q store.PolicyQuerier) (Settings, error) {
	s, err := scanSettings(q.QueryRow(ctx, `select `+settingsColumns+` from odoh_settings`))
	return s, store.MapError(err)
}

// UpdateSettings validates in and stores it when the row is still at revision; store.ErrConflict
// when the revision moved on.
func UpdateSettings(ctx context.Context, tx pgx.Tx, in Settings, revision int64) (Settings, error) {
	if err := Validate(in); err != nil {
		return Settings{}, err
	}
	targets := in.ProxyTargets
	if targets == nil {
		targets = []ProxyTarget{}
	}
	raw, err := json.Marshal(targets)
	if err != nil {
		return Settings{}, err
	}
	s, err := scanSettings(tx.QueryRow(ctx, `update odoh_settings set target_enabled = $1, proxy_enabled = $2,
		proxy_targets = $3, proxy_timeout_ms = $4, key_rotation_hours = $5, revision = revision + 1, updated_at = now()
		where revision = $6 returning `+settingsColumns,
		in.TargetEnabled, in.ProxyEnabled, string(raw), in.ProxyTimeoutMS, in.KeyRotationHours, revision))
	if errors.Is(err, pgx.ErrNoRows) {
		return Settings{}, fmt.Errorf("%w: odoh settings revision %d is stale; reload and retry", store.ErrConflict, revision)
	}
	return s, store.MapError(err)
}

// Config is the snapshot form of s; nil when both roles are off.
func Config(s Settings) *controlv1.OdohConfig {
	if !s.TargetEnabled && !s.ProxyEnabled {
		return nil
	}
	c := &controlv1.OdohConfig{TargetEnabled: s.TargetEnabled, ProxyEnabled: s.ProxyEnabled, ProxyTimeoutMs: uint32(max(s.ProxyTimeoutMS, 0))}
	for _, t := range s.ProxyTargets {
		c.ProxyTargets = append(c.ProxyTargets, &controlv1.OdohProxyTarget{Host: t.Host, CaPem: t.CAPEM})
	}
	return c
}
