// Package fake is an in-memory management API for operator tests: engine groups, join tokens, health
// and setup status, with knobs for conflicts, failures and non-empty groups.
package fake

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/operator/internal/keys"
	"github.com/piwi3910/nexora/operator/internal/mgmtapi"
)

// DefaultGroupID is the id of the engine group every installation has.
const DefaultGroupID = "00000000-0000-0000-0000-000000000001"

var groupName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Server is a running fake. The exported knobs may be set between requests; handlers read them under
// the lock.
type Server struct {
	URL, Token string

	ConflictOnce bool            // the next PUT /engine-groups/{id} answers 409 conflict
	Status       int             // non-zero: every request answers this status
	RevokeStatus int             // non-zero: every DELETE /join-tokens/{id} answers this status
	NonEmpty     map[string]bool // group name -> DELETE answers 409 engine_group_not_empty
	Requests     []string        // "METHOD /path" of every request, in order
	LastUpdate   *mgmtapi.EngineGroupUpdate

	mu            sync.Mutex
	groups        []mgmtapi.EngineGroup // creation order
	tokens        []mgmtapi.JoinToken   // creation order
	secrets       map[uuid.UUID]string
	healthy       bool
	setupRequired bool
}

// New starts an httptest server holding the default group, requiring "Bearer "+Token (a random nxt_
// token). The server is closed by t.Cleanup.
func New(t *testing.T) *Server {
	t.Helper()
	token, err := keys.GenerateBootstrapToken()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s := &Server{Token: token, secrets: map[uuid.UUID]string{}, healthy: true}
	s.groups = []mgmtapi.EngineGroup{withDefaults(mgmtapi.EngineGroup{Id: uuid.MustParse(DefaultGroupID), Name: "default",
		Revision: 1, CreatedAt: now, UpdatedAt: now})}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

// withDefaults fills the engine_groups column defaults of the management plane.
func withDefaults(g mgmtapi.EngineGroup) mgmtapi.EngineGroup {
	g.UpstreamMode = "inherit"
	g.RolloutStrategy = "all_at_once"
	g.AckTimeoutSeconds, g.HealthWindowSeconds, g.MaxServfailRatio, g.MinHealthQueries = 60, 30, 0.05, 100
	g.ExtraAclCidrs = []string{}
	return g
}

// Group returns the engine group called name.
func (s *Server) Group(name string) (mgmtapi.EngineGroup, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.groupIndex(func(g mgmtapi.EngineGroup) bool { return g.Name == name }); i >= 0 {
		return s.groups[i], true
	}
	return mgmtapi.EngineGroup{}, false
}

// MutateGroup changes the group called name as another API client would, bumping its revision.
func (s *Server) MutateGroup(name string, f func(*mgmtapi.EngineGroup)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := s.groupIndex(func(g mgmtapi.EngineGroup) bool { return g.Name == name }); i >= 0 {
		f(&s.groups[i])
		s.groups[i].Revision++
		s.groups[i].UpdatedAt = time.Now().UTC()
	}
}

// Tokens returns every join token.
func (s *Server) Tokens() []mgmtapi.JoinToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]mgmtapi.JoinToken(nil), s.tokens...)
}

// TokenSecret returns the token value handed out when the join token id was created.
func (s *Server) TokenSecret(id uuid.UUID) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.secrets[id]
}

// SetTokenState overrides a join token's state and expiry.
func (s *Server) SetTokenState(id uuid.UUID, state string, expiresAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.tokens {
		if s.tokens[i].Id == id {
			s.tokens[i].State = mgmtapi.JoinTokenState(state)
			s.tokens[i].ExpiresAt = expiresAt
		}
	}
}

// SetHealth sets what /health (ok: 200, else 503 degraded) and /setup answer.
func (s *Server) SetHealth(ok bool, setupRequired bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.healthy, s.setupRequired = ok, setupRequired
}

func (s *Server) groupIndex(match func(mgmtapi.EngineGroup) bool) int {
	for i, g := range s.groups {
		if match(g) {
			return i
		}
	}
	return -1
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, format string, args ...any) {
	writeJSON(w, status, mgmtapi.Error{Code: code, Message: fmt.Sprintf(format, args...)})
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests = append(s.Requests, r.Method+" "+r.URL.Path)
	// Like the management plane's jsonOnly middleware: every request other than GET and HEAD, bodiless
	// DELETEs included, must say Content-Type: application/json.
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
			writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "requests must send Content-Type: application/json")
			return
		}
	}
	if s.Status != 0 {
		writeError(w, s.Status, "fake_status", "status forced by the fake")
		return
	}
	route := r.Method + " " + strings.TrimPrefix(r.URL.Path, "/api/v1")
	switch route {
	case "GET /health":
		if s.healthy {
			writeJSON(w, http.StatusOK, mgmtapi.Health{Status: "ok", Database: "ok", Version: "fake"})
		} else {
			writeJSON(w, http.StatusServiceUnavailable, mgmtapi.Health{Status: "degraded", Database: "unavailable", Version: "fake"})
		}
		return
	case "GET /setup":
		writeJSON(w, http.StatusOK, mgmtapi.SetupStatus{Required: s.setupRequired})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.Token {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
		return
	}
	switch {
	case route == "GET /engine-groups":
		writeJSON(w, http.StatusOK, s.groups)
	case route == "POST /engine-groups":
		s.createGroup(w, r)
	case route == "GET /join-tokens":
		writeJSON(w, http.StatusOK, s.tokens)
	case route == "POST /join-tokens":
		s.createToken(w, r)
	case strings.HasPrefix(route, "GET /engine-groups/"), strings.HasPrefix(route, "PUT /engine-groups/"),
		strings.HasPrefix(route, "DELETE /engine-groups/"):
		id, err := uuid.Parse(strings.TrimPrefix(r.URL.Path, "/api/v1/engine-groups/"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid", "invalid id")
			return
		}
		i := s.groupIndex(func(g mgmtapi.EngineGroup) bool { return g.Id == id })
		switch {
		case r.Method == http.MethodDelete && id.String() == DefaultGroupID:
			writeError(w, http.StatusConflict, "engine_group_protected", "the default engine group cannot be deleted")
		case i < 0:
			writeError(w, http.StatusNotFound, "not_found", "not found")
		case r.Method == http.MethodGet:
			writeJSON(w, http.StatusOK, s.groups[i])
		case r.Method == http.MethodPut:
			s.updateGroup(w, r, i)
		default:
			s.deleteGroup(w, r, i)
		}
	case strings.HasPrefix(route, "DELETE /join-tokens/"):
		s.revokeToken(w, r)
	default:
		writeError(w, http.StatusNotFound, "not_found", "no route %s", route)
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "invalid body: %v", err)
		return false
	}
	return true
}

// overlay applies the set fields of an EngineGroupInput (or the input part of an update) to g.
func overlay(g *mgmtapi.EngineGroup, in mgmtapi.EngineGroupInput) {
	g.Name = in.Name
	if in.Description != nil {
		g.Description = *in.Description
	}
	if in.UpstreamMode != nil {
		g.UpstreamMode = mgmtapi.EngineGroupUpstreamMode(*in.UpstreamMode)
	}
	if in.ExtraAclCidrs != nil {
		g.ExtraAclCidrs = append([]string{}, *in.ExtraAclCidrs...)
	}
	if in.OtlpEndpoint != nil {
		g.OtlpEndpoint = *in.OtlpEndpoint
	}
	if in.RolloutStrategy != nil {
		g.RolloutStrategy = mgmtapi.EngineGroupRolloutStrategy(*in.RolloutStrategy)
	}
	setInt := func(dst *int, src *int) {
		if src != nil {
			*dst = *src
		}
	}
	setInt(&g.CanaryCount, in.CanaryCount)
	setInt(&g.CanaryPercent, in.CanaryPercent)
	setInt(&g.AckTimeoutSeconds, in.AckTimeoutSeconds)
	setInt(&g.HealthWindowSeconds, in.HealthWindowSeconds)
	setInt(&g.MinHealthQueries, in.MinHealthQueries)
	if in.MaxServfailRatio != nil {
		g.MaxServfailRatio = *in.MaxServfailRatio
	}
	if in.FilterIndexMaxBytes != nil {
		g.FilterIndexMaxBytes = *in.FilterIndexMaxBytes
	}
}

func (s *Server) nameTaken(name string, except uuid.UUID) bool {
	return s.groupIndex(func(g mgmtapi.EngineGroup) bool { return g.Name == name && g.Id != except }) >= 0
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	var in mgmtapi.EngineGroupInput
	if !decode(w, r, &in) {
		return
	}
	if !groupName.MatchString(in.Name) {
		writeError(w, http.StatusBadRequest, "invalid", "invalid engine group name")
		return
	}
	if s.nameTaken(in.Name, uuid.Nil) {
		writeError(w, http.StatusConflict, "name_taken", "engine group %q already exists", in.Name)
		return
	}
	now := time.Now().UTC()
	g := withDefaults(mgmtapi.EngineGroup{Id: uuid.New(), Revision: 1, CreatedAt: now, UpdatedAt: now})
	overlay(&g, in)
	s.groups = append(s.groups, g)
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) updateGroup(w http.ResponseWriter, r *http.Request, i int) {
	var in mgmtapi.EngineGroupUpdate
	if !decode(w, r, &in) {
		return
	}
	last := in
	s.LastUpdate = &last
	g := s.groups[i]
	switch {
	case s.ConflictOnce:
		s.ConflictOnce = false
		writeError(w, http.StatusConflict, "conflict", "revision conflict")
		return
	case in.Revision != g.Revision:
		writeError(w, http.StatusConflict, "conflict", "revision conflict")
		return
	case !groupName.MatchString(in.Name):
		writeError(w, http.StatusBadRequest, "invalid", "invalid engine group name")
		return
	case g.Id.String() == DefaultGroupID && in.Name != g.Name:
		writeError(w, http.StatusConflict, "engine_group_protected", "the default engine group cannot be renamed")
		return
	case s.nameTaken(in.Name, g.Id):
		writeError(w, http.StatusConflict, "name_taken", "engine group %q already exists", in.Name)
		return
	}
	overlay(&g, mgmtapi.EngineGroupInput{Name: in.Name, Description: in.Description,
		UpstreamMode: (*mgmtapi.EngineGroupInputUpstreamMode)(in.UpstreamMode), ExtraAclCidrs: in.ExtraAclCidrs,
		OtlpEndpoint: in.OtlpEndpoint, RolloutStrategy: (*mgmtapi.EngineGroupInputRolloutStrategy)(in.RolloutStrategy),
		CanaryCount: in.CanaryCount, CanaryPercent: in.CanaryPercent, AckTimeoutSeconds: in.AckTimeoutSeconds,
		HealthWindowSeconds: in.HealthWindowSeconds, MaxServfailRatio: in.MaxServfailRatio,
		MinHealthQueries: in.MinHealthQueries, FilterIndexMaxBytes: in.FilterIndexMaxBytes})
	g.Revision++
	g.UpdatedAt = time.Now().UTC()
	s.groups[i] = g
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request, i int) {
	rev, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	g := s.groups[i]
	switch {
	case err != nil:
		writeError(w, http.StatusBadRequest, "invalid", "revision is required")
	case rev != g.Revision:
		writeError(w, http.StatusConflict, "conflict", "revision conflict")
	case s.NonEmpty[g.Name]:
		writeError(w, http.StatusConflict, "engine_group_not_empty", "engine group %q still has engines", g.Name)
	default:
		s.groups = append(s.groups[:i], s.groups[i+1:]...)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	var in mgmtapi.JoinTokenCreate
	if !decode(w, r, &in) {
		return
	}
	if n := len(strings.TrimSpace(in.Name)); n < 1 || n > 64 {
		writeError(w, http.StatusBadRequest, "invalid", "name must be 1-64 characters")
		return
	}
	if in.TtlSeconds < 60 || in.TtlSeconds > 31536000 {
		writeError(w, http.StatusBadRequest, "invalid", "ttl_seconds must be between 60 and 31536000")
		return
	}
	groupID := uuid.MustParse(DefaultGroupID)
	if in.EngineGroupId != nil {
		groupID = *in.EngineGroupId
	}
	gi := s.groupIndex(func(g mgmtapi.EngineGroup) bool { return g.Id == groupID })
	if gi < 0 {
		writeError(w, http.StatusUnprocessableEntity, "engine_group_not_found", "engine group %s does not exist", groupID)
		return
	}
	secret := make([]byte, 20)
	fingerprint := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "%v", err)
		return
	}
	if _, err := rand.Read(fingerprint); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "%v", err)
		return
	}
	value := "nxj1." + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret) + "." + hex.EncodeToString(fingerprint)
	now := time.Now().UTC()
	jt := mgmtapi.JoinToken{Id: uuid.New(), Name: in.Name, CreatedBy: "nexora-operator", CreatedAt: now,
		ExpiresAt: now.Add(time.Duration(in.TtlSeconds) * time.Second), EngineGroupId: groupID,
		EngineGroupName: s.groups[gi].Name, Labels: map[string]string{}, State: "active", MaxUses: in.MaxUses}
	if in.Labels != nil {
		jt.Labels = *in.Labels
	}
	s.tokens = append(s.tokens, jt)
	s.secrets[jt.Id] = value
	writeJSON(w, http.StatusCreated, mgmtapi.JoinTokenCreated{JoinToken: jt, Token: value})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	if s.RevokeStatus != 0 {
		writeError(w, s.RevokeStatus, "fake_revoke_status", "revocation refused by the fake")
		return
	}
	id, err := uuid.Parse(strings.TrimPrefix(r.URL.Path, "/api/v1/join-tokens/"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid", "invalid id")
		return
	}
	for i := range s.tokens {
		if s.tokens[i].Id == id {
			if s.tokens[i].RevokedAt == nil {
				now := time.Now().UTC()
				s.tokens[i].RevokedAt = &now
			}
			s.tokens[i].State = "revoked"
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "not found")
}
