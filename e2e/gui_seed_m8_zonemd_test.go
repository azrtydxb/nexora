package e2e

import "net/http"

func init() { registerGUISeed(seedZonemd) }

// seedZonemd creates the zones 40-zonemd.spec.ts edits: a primary for ZONEMD generation and catalog
// membership, a secondary whose primary never answers (ZONEMD status stays not_checked) and a
// producer catalog the primary joins.
func seedZonemd(s guiSeedEnv) {
	primary := createPrimaryZone(s.T, s.Admin, "gui-zmd.test.", nil)
	var secondary zoneResp
	s.Admin.Must(http.MethodPost, "/zones", map[string]any{"name": "gui-zmd-sec.test.", "kind": "secondary",
		"primaries": []map[string]any{{"address": "127.0.0.1:9"}}}, &secondary, http.StatusCreated)
	s.Admin.Must(http.MethodPost, "/catalog-zones", map[string]any{"name": "gui-catalog.test.", "role": "producer",
		"transfer": map[string]any{"allow_cidrs": []string{"127.0.0.1/32"}}}, nil, http.StatusCreated)
	s.Vars["NEXORA_E2E_ZONEMD_PRIMARY_ID"] = primary.ID
	s.Vars["NEXORA_E2E_ZONEMD_SECONDARY_ID"] = secondary.ID
	s.Vars["NEXORA_E2E_PRODUCER_CATALOG_NAME"] = "gui-catalog.test."
}
