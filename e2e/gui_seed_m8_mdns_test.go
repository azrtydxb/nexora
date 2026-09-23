package e2e

func init() { registerGUISeed(seedMdns) }

// seedMdns creates the engine group without engines that 43-mdns.spec.ts configures.
func seedMdns(s guiSeedEnv) {
	g := s.Admin.CreateEngineGroup(map[string]any{"name": "gui-mdns"})
	s.Vars["NEXORA_E2E_MDNS_GROUP_ID"] = g.ID
}
