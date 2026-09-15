package proposal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"

	apispec "github.com/piwi3910/nexora/mgmt/api"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// Validator checks proposal actions before they are stored. PublicURL is NEXORA_PUBLIC_URL, whose host
// an RPZ rule may not name.
type Validator struct {
	Store     *store.Store
	PublicURL string
}

// revisionSQL reads the current revision of the resource an operation changes; the path parameter, when
// the operation has one, is $1. Operations without an entry (createPolicyGroup) have no revision.
var revisionSQL = map[string]struct{ param, sql, noun string }{
	"updatePolicyGroup":      {"id", "select revision from policy_groups where id = $1", "policy group"},
	"updateFilterCategory":   {"key", "select revision from filter_categories where key = $1", "filter category"},
	"updateUpstream":         {"id", "select revision from upstreams where id = $1", "upstream"},
	"updateEngineGroup":      {"id", "select revision from engine_groups where id = $1", "engine group"},
	"updateGlobalSafeSearch": {"", "select revision from global_safe_search", "safe search"},
	"updateAllowlist":        {"", "select revision from allowlist", "allowlist"},
	"updateResolverSettings": {"", "select revision from resolver_settings", "resolver settings"},
}

// Validate returns the first reason the actions cannot become a proposal, or nil.
func (v *Validator) Validate(ctx context.Context, actions []Action) error {
	if len(actions) == 0 || len(actions) > MaxActions {
		return fmt.Errorf("a proposal needs 1 to at most %d actions, got %d", MaxActions, len(actions))
	}
	rpzActions := 0
	for _, a := range actions {
		if a.OperationID == OpAppendAiRpzRules {
			rpzActions++
		}
	}
	if rpzActions > 0 && rpzActions != len(actions) {
		return fmt.Errorf("%s cannot be combined with other operations in one proposal", OpAppendAiRpzRules)
	}
	for i, a := range actions {
		var err error
		switch {
		case !AllowedOperations[a.OperationID]:
			err = fmt.Errorf("operation %s is not allowed", a.OperationID)
		case a.OperationID == OpAppendAiRpzRules:
			err = v.validateRPZ(ctx, a)
		default:
			err = v.validateOperation(ctx, a)
		}
		if err != nil {
			return fmt.Errorf("actions[%d] %s: %w", i, a.OperationID, err)
		}
	}
	return nil
}

func (v *Validator) validateOperation(ctx context.Context, a Action) error {
	op, ok := apispec.Operations()[a.OperationID]
	if !ok {
		return fmt.Errorf("operation %s is not in the API", a.OperationID)
	}
	known := map[string]bool{}
	for _, p := range op.Params {
		if p.In != "path" {
			continue
		}
		known[p.Name] = true
		val, ok := a.PathParams[p.Name]
		if !ok || val == "" {
			return fmt.Errorf("path parameter %s is required", p.Name)
		}
		if p.Schema != nil && p.Schema.Value != nil && p.Schema.Value.Format == "uuid" {
			if _, err := uuid.Parse(val); err != nil {
				return fmt.Errorf("path parameter %s: %q is not a UUID", p.Name, val)
			}
		}
	}
	for name := range a.PathParams {
		if !known[name] {
			return fmt.Errorf("unknown path parameter %s", name)
		}
	}
	if op.Body == nil || op.Body.Value == nil {
		return errors.New("operation takes no body")
	}
	var body any
	if len(a.Body) == 0 || json.Unmarshal(a.Body, &body) != nil {
		return errors.New("body must be a JSON object")
	}
	if err := unknownFields(op.Body.Value, body, "body"); err != nil {
		return err
	}
	if err := op.Body.Value.VisitJSON(body, openapi3.MultiErrors()); err != nil {
		return fmt.Errorf("body: %w", err)
	}
	return v.checkRevision(ctx, a, body)
}

func (v *Validator) checkRevision(ctx context.Context, a Action, body any) error {
	rs, ok := revisionSQL[a.OperationID]
	if !ok {
		return nil
	}
	var args []any
	subject := rs.noun
	if rs.param != "" {
		args = append(args, a.PathParams[rs.param])
		subject += " " + a.PathParams[rs.param]
	}
	var current int64
	err := v.Store.Pool.QueryRow(ctx, rs.sql, args...).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s not found", subject)
	}
	if err != nil {
		return store.MapError(err)
	}
	sent, ok := body.(map[string]any)["revision"].(float64)
	if !ok {
		return errors.New("body: revision is required")
	}
	if int64(sent) != current {
		return fmt.Errorf("stale revision %d for %s (current %d)", int64(sent), subject, current)
	}
	return nil
}

// unknownFields rejects object keys that the schema does not declare, unless the object schema (with
// its allOf members merged) allows additional properties or declares no properties at all.
func unknownFields(s *openapi3.Schema, value any, path string) error {
	switch val := value.(type) {
	case map[string]any:
		props, closed := objectProperties(s)
		for k, child := range val {
			ps, ok := props[k]
			if !ok {
				if closed {
					return fmt.Errorf("%s: unknown field %s", path, k)
				}
				continue
			}
			if err := unknownFields(ps, child, path+"."+k); err != nil {
				return err
			}
		}
	case []any:
		if s.Items != nil && s.Items.Value != nil {
			for i, child := range val {
				if err := unknownFields(s.Items.Value, child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func objectProperties(s *openapi3.Schema) (map[string]*openapi3.Schema, bool) {
	props := map[string]*openapi3.Schema{}
	open := false
	var visit func(*openapi3.Schema)
	visit = func(s *openapi3.Schema) {
		if s == nil {
			return
		}
		for name, ref := range s.Properties {
			if ref != nil && ref.Value != nil {
				props[name] = ref.Value
			}
		}
		if (s.AdditionalProperties.Has != nil && *s.AdditionalProperties.Has) || s.AdditionalProperties.Schema != nil ||
			len(s.OneOf) > 0 || len(s.AnyOf) > 0 {
			open = true
		}
		for _, m := range s.AllOf {
			if m != nil {
				visit(m.Value)
			}
		}
	}
	visit(s)
	return props, !open && len(props) > 0
}

var rpzLabel = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?$`)

// validRecord checks the name syntax of an RPZ record: letters, digits, hyphen and underscore labels, and
// a `*` only as the first label with at least two labels under it.
func validRecord(record string) error {
	if record == "" || len(record) > 200 {
		return fmt.Errorf("invalid name %q", record)
	}
	if _, ok := dns.IsDomainName(record); !ok {
		return fmt.Errorf("invalid name %q", record)
	}
	labels := strings.Split(record, ".")
	for i, l := range labels {
		if l == "*" && i == 0 {
			if len(labels) < 3 {
				return fmt.Errorf("record %s: a wildcard needs two labels under *", record)
			}
			continue
		}
		if !rpzLabel.MatchString(l) {
			return fmt.Errorf("invalid name %q", record)
		}
	}
	return nil
}

func atOrUnder(name, parent string) bool {
	return name == parent || strings.HasSuffix(name, "."+parent)
}

// matchesHost reports whether rule record matches the host name exactly or by its wildcard.
func matchesHost(record, host string) bool {
	if base, ok := strings.CutPrefix(record, "*."); ok {
		return host != base && atOrUnder(host, base)
	}
	return record == host
}

// overlapsTree reports whether rule record matches a name at or under tree.
func overlapsTree(record, tree string) bool {
	if base, ok := strings.CutPrefix(record, "*."); ok {
		return atOrUnder(base, tree) || (tree != base && atOrUnder(tree, base))
	}
	return atOrUnder(record, tree)
}

func normalName(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

func (v *Validator) validateRPZ(ctx context.Context, a Action) error {
	if len(a.PathParams) > 0 {
		return errors.New("takes no path parameters")
	}
	rules, err := DecodeRules(a)
	if err != nil {
		return err
	}
	zones, err := v.names(ctx, "select name from zones")
	if err != nil {
		return err
	}
	upstreams, err := v.upstreamHosts(ctx)
	if err != nil {
		return err
	}
	allow, err := v.names(ctx, "select unnest(domains) from allowlist union select domain from policy_group_allowlist")
	if err != nil {
		return err
	}
	var mgmtHost string
	if u, err := url.Parse(v.PublicURL); err == nil {
		mgmtHost = normalName(u.Hostname())
	}
	for i, r := range rules {
		if err := validRecord(r.Record); err != nil {
			return fmt.Errorf("rules[%d]: %w", i, err)
		}
		for _, z := range zones {
			if overlapsTree(r.Record, z) {
				return fmt.Errorf("rules[%d]: record %s is at or under hosted zone %s.", i, r.Record, z)
			}
		}
		for _, h := range upstreams {
			if matchesHost(r.Record, h) {
				return fmt.Errorf("rules[%d]: record %s names the upstream host %s", i, r.Record, h)
			}
		}
		if mgmtHost != "" && matchesHost(r.Record, mgmtHost) {
			return fmt.Errorf("rules[%d]: record %s names the management host", i, r.Record)
		}
		for _, d := range allow {
			if overlapsTree(r.Record, d) {
				return fmt.Errorf("rules[%d]: record %s is allowlisted (%s)", i, r.Record, d)
			}
		}
	}
	return nil
}

func (v *Validator) names(ctx context.Context, sql string) ([]string, error) {
	rows, err := v.Store.Pool.Query(ctx, sql)
	if err != nil {
		return nil, store.MapError(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, store.MapError(err)
		}
		if n = normalName(n); n != "" {
			out = append(out, n)
		}
	}
	return out, store.MapError(rows.Err())
}

func (v *Validator) upstreamHosts(ctx context.Context) ([]string, error) {
	rows, err := v.Store.Pool.Query(ctx, "select tls_server_name, doh_url from upstreams")
	if err != nil {
		return nil, store.MapError(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sni, doh string
		if err := rows.Scan(&sni, &doh); err != nil {
			return nil, store.MapError(err)
		}
		if n := normalName(sni); n != "" {
			out = append(out, n)
		}
		if u, err := url.Parse(doh); err == nil && doh != "" {
			if n := normalName(u.Hostname()); n != "" {
				out = append(out, n)
			}
		}
	}
	return out, store.MapError(rows.Err())
}
