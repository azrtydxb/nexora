package e2e

import (
	"net/http"

	"github.com/piwi3910/nexora/e2e/harness"
)

func init() { registerGUISeed(seedCatalogZones) }

// seedCatalogZones gives 41-catalog-zones.spec.ts a producer catalog with one member zone and a
// consumer catalog showing a member clash.
//
// The consumer's primary never answers, so no reconciliation runs and replaces the clash row: only
// reconciliation writes catalog_member_issues, so the row is inserted directly.
func seedCatalogZones(s guiSeedEnv) {
	var producer, consumer struct {
		ID string `json:"id"`
	}
	s.Admin.Must(http.MethodPost, "/catalog-zones", map[string]any{"name": "gui-seeded.test.", "role": "producer",
		"transfer": map[string]any{"allow_cidrs": []string{"127.0.0.1/32"}}}, &producer, http.StatusCreated)
	createPrimaryZone(s.T, s.Admin, "gui-member.test.", map[string]any{"catalog_zone_id": producer.ID})
	s.Admin.Must(http.MethodPost, "/catalog-zones", map[string]any{"name": "gui-remote.test.", "role": "consumer",
		"primaries": []map[string]any{{"address": "127.0.0.1:9"}}}, &consumer, http.StatusCreated)
	harness.PGExec(s.T, s.PGURL,
		`INSERT INTO catalog_member_issues (catalog_zone_id, member_name, label, issue)
		 VALUES ($1, 'gui-member.test.', 'gui-member', 'a zone with this name exists and was not created by this catalog')`,
		consumer.ID)
	// Reconciliation is the only writer of consumer membership. Seed its stored result so the
	// GUI can prove read-only controls without relying on a remote primary or live transfer.
	var managed zoneResp
	s.Admin.Must(http.MethodPost, "/zones", map[string]any{"name": "gui-managed.test.", "kind": "secondary",
		"primaries": []map[string]any{{"address": "127.0.0.1:9"}}}, &managed, http.StatusCreated)
	harness.PGExec(s.T, s.PGURL,
		`UPDATE zones SET catalog_zone_id = $1, catalog_member_label = 'gui-managed' WHERE id = $2`, consumer.ID, managed.ID)
	s.Vars["NEXORA_E2E_MANAGED_ZONE_ID"] = managed.ID

	s.Vars["NEXORA_E2E_SEEDED_CATALOG_ID"] = producer.ID
	s.Vars["NEXORA_E2E_CLASH_CATALOG_ID"] = consumer.ID
}
