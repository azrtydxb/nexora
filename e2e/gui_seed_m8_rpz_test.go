package e2e

import (
	"net/http"

	"github.com/piwi3910/nexora/e2e/harness"
)

func init() { registerGUISeed(seedRPZZonemd) }

// seedRPZZonemd gives 44-rpz-zonemd.spec.ts a transfer RPZ zone with a failed ZONEMD check on the
// GUI engine. The zone is scoped to an engine group without engines, so no running engine loads it
// and overwrites the status row, which goes in by SQL because only engine reports write it.
func seedRPZZonemd(s guiSeedEnv) {
	const name = "gui-zonemd.rpz.test."
	group := s.Admin.CreateEngineGroup(map[string]any{"name": "gui-rpz-zonemd"})
	var zone struct {
		ID string `json:"id"`
	}
	s.Admin.Must(http.MethodPost, "/rpz-zones", map[string]any{
		"name": name, "source_type": "transfer", "primary": "127.0.0.1:5399", "engine_group_id": group.ID,
		"min_refresh_seconds": 60, "policy_override": "given", "zonemd_verify": "if_present",
	}, &zone, http.StatusCreated)
	engine := s.Admin.EngineByNode("gui-engine")
	harness.PGExec(s.T, s.PGURL, `insert into engine_rpz_status(engine_id, rpz_zone_id, serial, records, skipped, hits,
		last_success_at, last_error, stale, zonemd, zonemd_error, reported_at)
		values ($1, $2, 7, 2, 0, 0, now(), '', false, 'failed', 'digest mismatch', now())`, engine.ID, zone.ID)
	s.Vars["NEXORA_E2E_RPZ_ZONEMD_NAME"] = name
}
