package mcpserver

import (
	"encoding/json"
	"strings"
	"sync"

	apispec "github.com/piwi3910/nexora/mgmt/api"
)

// Tool is one MCP tool, bound to exactly one OpenAPI operation. MCP tool names allow only [a-z0-9_], so
// #52's rotate-certificate becomes _rotate_certificate and its combined resolver settings tool becomes
// _get and _update.
type Tool struct{ Name, OperationID, Description string }

// Tools is the #52 tool table.
var Tools = []Tool{
	{"nexora_query_log_query", "searchQueryLog", "Search the DNS query log (time range, client, name, type, rcode, filter outcome)."},
	{"nexora_filter_lists_list", "listFilterLists", "List blocklists and allowlists with their refresh state."},
	{"nexora_filter_lists_create", "createFilterList", "Create a filter list from a URL."},
	{"nexora_filter_lists_update", "updateFilterList", "Update a filter list."},
	{"nexora_filter_lists_refresh", "refreshFilterList", "Refresh a filter list from its URL now."},
	{"nexora_filter_categories_list", "listFilterCategories", "List the filter categories of the catalog and whether they are enabled."},
	{"nexora_filter_categories_update", "updateFilterCategory", "Enable, disable or change a filter category."},
	{"nexora_policy_groups_list", "listPolicyGroups", "List the per-client policy groups."},
	{"nexora_policy_groups_create", "createPolicyGroup", "Create a per-client policy group."},
	{"nexora_policy_groups_update", "updatePolicyGroup", "Update a per-client policy group (needs its current revision)."},
	{"nexora_policy_groups_delete", "deletePolicyGroup", "Delete a per-client policy group (needs its current revision)."},
	{"nexora_engines_list", "listEngines", "List the DNS engines of the fleet with their status."},
	{"nexora_engines_get", "getEngine", "Get one DNS engine."},
	{"nexora_engines_revoke", "revokeEngine", "Revoke a DNS engine's certificate and remove it from the fleet."},
	{"nexora_engines_rotate_certificate", "rotateEngineCertificate", "Rotate a DNS engine's client certificate."},
	{"nexora_zones_list", "listZones", "List the hosted authoritative zones."},
	{"nexora_zones_create", "createZone", "Create a hosted zone."},
	{"nexora_zones_update", "updateZone", "Update a hosted zone's settings."},
	{"nexora_zone_records_list", "listZoneRecords", "List the records of a hosted zone."},
	{"nexora_zone_records_create", "createZoneRecord", "Create a record in a hosted zone."},
	{"nexora_zone_records_update", "updateZoneRecord", "Update a record of a hosted zone."},
	{"nexora_zone_records_delete", "deleteZoneRecord", "Delete a record of a hosted zone."},
	{"nexora_dashboard_get", "getDashboard", "Get the dashboard: query rates, block ratio, top domains and clients."},
	{"nexora_fleet_summary", "getFleetSummary", "Get the fleet summary: engines by state and config version spread."},
	{"nexora_engine_stats", "getEngineStats", "Get the statistics of one DNS engine."},
	{"nexora_upstreams_list", "listUpstreams", "List the upstream resolvers."},
	{"nexora_upstreams_create", "createUpstream", "Create an upstream resolver."},
	{"nexora_upstreams_update", "updateUpstream", "Update an upstream resolver (needs its current revision)."},
	{"nexora_resolver_settings_get", "getResolverSettings", "Get the resolver settings (recursion, cache, upstream strategy)."},
	{"nexora_resolver_settings_update", "updateResolverSettings", "Update the resolver settings (needs the current revision)."},
	{"nexora_config_versions_list", "listConfigVersions", "List the published configuration versions."},
	{"nexora_rollouts_list", "listRollouts", "List configuration rollouts."},
	{"nexora_rollouts_get", "getRollout", "Get one configuration rollout with its waves."},
	{"nexora_ai_proposals_list", "listAiProposals", "List AI configuration proposals (suggestions a human may apply)."},
	{"nexora_ai_findings_list", "listAiFindings", "List AI findings: query-log anomalies and dashboard insights."},
}

// maxRefDepth bounds $ref inlining; a deeper (recursive) reference becomes an unconstrained schema.
const maxRefDepth = 8

var (
	schemasOnce sync.Once
	schemas     map[string]map[string]any // by operation id
)

// toolSchema is the cached input schema of the operation.
func toolSchema(operationID string) map[string]any {
	schemasOnce.Do(func() {
		schemas = map[string]map[string]any{}
		for _, t := range Tools {
			schemas[t.OperationID] = inputSchema(apispec.Operations()[t.OperationID])
		}
	})
	return schemas[operationID]
}

// inputSchema is {type:object, properties:{<path and query params>, body:<request schema>},
// required:[required params, "body" when the operation has a body]} with $refs inlined.
func inputSchema(op apispec.Operation) map[string]any {
	props := map[string]any{}
	var required []string
	for _, p := range op.Params {
		if p.In != "path" && p.In != "query" {
			continue
		}
		props[p.Name] = inlineRef(p.Schema, 0)
		if p.Required {
			required = append(required, p.Name)
		}
	}
	if op.Body != nil {
		props["body"] = inlineRef(op.Body, 0)
		required = append(required, "body")
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// inlineRef renders a schema as plain JSON Schema with every component reference replaced by the
// component itself, allOf object parts merged into one object.
func inlineRef(ref interface{ MarshalJSON() ([]byte, error) }, depth int) map[string]any {
	raw, err := ref.MarshalJSON()
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return map[string]any{}
	}
	return inline(m, depth).(map[string]any)
}

func inline(v any, depth int) any {
	switch x := v.(type) {
	case []any:
		for i := range x {
			x[i] = inline(x[i], depth)
		}
		return x
	case map[string]any:
		if r, ok := x["$ref"].(string); ok {
			name, ok := strings.CutPrefix(r, "#/components/schemas/")
			spec, err := apispec.Spec()
			if !ok || err != nil || depth >= maxRefDepth || spec.Components.Schemas[name] == nil || spec.Components.Schemas[name].Value == nil {
				return map[string]any{}
			}
			return inlineRef(spec.Components.Schemas[name].Value, depth+1)
		}
		for k, e := range x {
			x[k] = inline(e, depth)
		}
		mergeAllOf(x)
		return x
	default:
		return v
	}
}

// mergeAllOf folds allOf parts into x (properties and required unioned, other keywords kept when x has
// none), because MCP clients and models handle one flat object schema far better than a composition.
func mergeAllOf(x map[string]any) {
	parts, ok := x["allOf"].([]any)
	if !ok {
		return
	}
	props, _ := x["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	required, _ := x["required"].([]any)
	for _, p := range parts {
		pm, ok := p.(map[string]any)
		if !ok {
			return
		}
		if t, ok := pm["type"]; ok && t != "object" {
			return // not an object composition: leave it as allOf
		}
	}
	for _, p := range parts {
		pm := p.(map[string]any)
		for k, e := range pm {
			switch k {
			case "properties":
				for name, s := range e.(map[string]any) {
					props[name] = s
				}
			case "required":
				required = append(required, e.([]any)...)
			default:
				if _, ok := x[k]; !ok {
					x[k] = e
				}
			}
		}
	}
	delete(x, "allOf")
	x["type"] = "object"
	x["properties"] = props
	if len(required) > 0 {
		x["required"] = required
	}
}
