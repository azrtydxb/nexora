package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/operator/internal/mgmtapi"
	"github.com/piwi3910/nexora/operator/internal/mgmtapi/fake"
)

func TestFakeServesEngineGroupsAndTokens(t *testing.T) {
	ctx := context.Background()
	s := fake.New(t)
	c, err := mgmtapi.New(s.URL, s.Token, nil)
	if err != nil {
		t.Fatal(err)
	}

	groups, err := c.EngineGroups(ctx)
	if err != nil || len(groups) != 1 || groups[0].Name != "default" || groups[0].Id.String() != fake.DefaultGroupID {
		t.Fatalf("list: %v %+v", err, groups)
	}

	edge, err := c.CreateEngineGroup(ctx, mgmtapi.EngineGroupInput{Name: "edge"})
	if err != nil || edge.Revision != 1 || edge.Name != "edge" {
		t.Fatalf("create: %v %+v", err, edge)
	}
	if got, ok := s.Group("edge"); !ok || got.Id != edge.Id {
		t.Fatalf("fake does not hold edge: %+v", got)
	}

	desc := "edge sites"
	updated, err := c.UpdateEngineGroup(ctx, edge.Id, mgmtapi.EngineGroupUpdate{Name: "edge", Description: &desc, Revision: 1})
	if err != nil || updated.Revision != 2 || updated.Description != desc {
		t.Fatalf("update: %v %+v", err, updated)
	}
	if s.LastUpdate == nil || s.LastUpdate.Revision != 1 || *s.LastUpdate.Description != desc {
		t.Fatalf("LastUpdate: %+v", s.LastUpdate)
	}
	if _, err := c.UpdateEngineGroup(ctx, edge.Id, mgmtapi.EngineGroupUpdate{Name: "edge", Revision: 1}); !errors.Is(err, mgmtapi.ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}

	created, err := c.CreateJoinToken(ctx, mgmtapi.JoinTokenCreate{Name: "op/ns/edge/1", TtlSeconds: 3600, EngineGroupId: &edge.Id})
	if err != nil || created.Token == "" || s.TokenSecret(created.JoinToken.Id) != created.Token || created.JoinToken.State != "active" ||
		created.JoinToken.EngineGroupName != "edge" {
		t.Fatalf("create token: %v %+v", err, created)
	}
	if err := c.RevokeJoinToken(ctx, created.JoinToken.Id); err != nil {
		t.Fatal(err)
	}
	tokens, err := c.JoinTokens(ctx)
	if err != nil || len(tokens) != 1 || tokens[0].State != "revoked" || tokens[0].RevokedAt == nil {
		t.Fatalf("tokens after revoke: %v %+v", err, tokens)
	}
	if err := c.RevokeJoinToken(ctx, uuid.New()); !errors.Is(err, mgmtapi.ErrNotFound) {
		t.Fatalf("revoke unknown: %v", err)
	}

	wrong, _ := mgmtapi.New(s.URL, "nxt_WRONG", nil)
	if _, err := wrong.EngineGroups(ctx); !errors.Is(err, mgmtapi.ErrUnauthorized) {
		t.Fatalf("wrong bearer: %v", err)
	}

	s.NonEmpty = map[string]bool{"edge": true}
	if err := c.DeleteEngineGroup(ctx, edge.Id, 2); !errors.Is(err, mgmtapi.ErrConflict) {
		t.Fatalf("delete non-empty: %v", err)
	}
	s.NonEmpty = nil
	if err := c.DeleteEngineGroup(ctx, edge.Id, 2); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := s.Group("edge"); ok {
		t.Fatal("edge still present after delete")
	}

	err = c.DeleteEngineGroup(ctx, uuid.MustParse(fake.DefaultGroupID), 1)
	var apiErr *mgmtapi.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Code != "engine_group_protected" {
		t.Fatalf("delete default: %v", err)
	}
	if s.Requests[0] != "GET /api/v1/engine-groups" {
		t.Fatalf("requests: %v", s.Requests)
	}
}

func TestFakeKnobs(t *testing.T) {
	ctx := context.Background()
	s := fake.New(t)
	c, _ := mgmtapi.New(s.URL, s.Token, nil)

	if err := c.Health(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}
	if req, err := c.SetupRequired(ctx); err != nil || req {
		t.Fatalf("setup: %v %v", req, err)
	}
	s.SetHealth(false, true)
	if err := c.Health(ctx); !errors.Is(err, mgmtapi.ErrUnavailable) {
		t.Fatalf("degraded health: %v", err)
	}
	if req, err := c.SetupRequired(ctx); err != nil || !req {
		t.Fatalf("setup required: %v %v", req, err)
	}

	def, _ := s.Group("default")
	s.ConflictOnce = true
	if _, err := c.UpdateEngineGroup(ctx, def.Id, mgmtapi.EngineGroupUpdate{Name: "default", Revision: def.Revision}); !errors.Is(err, mgmtapi.ErrConflict) {
		t.Fatalf("conflict once: %v", err)
	}
	if _, err := c.UpdateEngineGroup(ctx, def.Id, mgmtapi.EngineGroupUpdate{Name: "default", Revision: def.Revision}); err != nil {
		t.Fatalf("after conflict once: %v", err)
	}

	s.MutateGroup("default", func(g *mgmtapi.EngineGroup) { g.Description = "changed" })
	if g, _ := s.Group("default"); g.Revision != def.Revision+2 || g.Description != "changed" {
		t.Fatalf("mutate: %+v", g)
	}

	created, err := c.CreateJoinToken(ctx, mgmtapi.JoinTokenCreate{Name: "t", TtlSeconds: 60})
	if err != nil || created.JoinToken.EngineGroupName != "default" {
		t.Fatalf("token on default: %v %+v", err, created)
	}
	s.SetTokenState(created.JoinToken.Id, "expired", created.JoinToken.CreatedAt)
	if tk := s.Tokens(); len(tk) != 1 || tk[0].State != "expired" || !tk[0].ExpiresAt.Equal(created.JoinToken.CreatedAt) {
		t.Fatalf("token state: %+v", tk)
	}

	s.Status = 503
	if _, err := c.EngineGroups(ctx); !errors.Is(err, mgmtapi.ErrUnavailable) {
		t.Fatalf("status knob: %v", err)
	}
}
