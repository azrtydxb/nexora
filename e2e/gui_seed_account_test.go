package e2e

func init() { registerGUISeed(seedAccount) }

// seedAccount creates the local viewer that 33-account.spec.ts signs in as and changes the password of.
func seedAccount(s guiSeedEnv) {
	s.Admin.Must("POST", "/users", map[string]any{"username": "pat", "email": "pat@example.test", "password": "pat-password-e2e-1", "role": "viewer"}, nil, 201)
	s.Vars["NEXORA_E2E_ACCOUNT_USER"] = "pat"
	s.Vars["NEXORA_E2E_ACCOUNT_PASSWORD"] = "pat-password-e2e-1"
}
