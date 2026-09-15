package e2e

func init() { registerGUISeed(seedAccess) }

// seedAccess creates the hosted zone whose access override 30-access-control-split.spec.ts edits.
func seedAccess(s guiSeedEnv) {
	createPrimaryZone(s.T, s.Admin, "gui-acl.test.", nil)
	s.Vars["NEXORA_E2E_ACL_ZONE"] = "gui-acl.test."
}
