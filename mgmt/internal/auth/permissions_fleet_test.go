package auth

import "testing"

func TestFleetPermissions(t *testing.T) {
	want := map[string]Role{
		"listEngineGroups": RoleViewer, "getEngineGroup": RoleViewer, "listRollouts": RoleViewer, "getRollout": RoleViewer,
		"getFleetSummary": RoleViewer, "listEngines": RoleViewer, "getEngine": RoleViewer, "getEngineStats": RoleViewer,
		"createEngineGroup": RoleOperator, "updateEngineGroup": RoleOperator, "rollbackEngineGroup": RoleOperator,
		"resumeEngineGroupRollouts": RoleOperator, "updateEngine": RoleOperator,
		"deleteEngineGroup": RoleAdmin, "deleteEngine": RoleAdmin, "listJoinTokens": RoleAdmin,
		"createJoinToken": RoleAdmin, "revokeJoinToken": RoleAdmin, "revokeEngine": RoleAdmin, "rotateEngineCertificate": RoleAdmin,
	}
	for op, role := range want {
		got, ok := Permissions[op]
		if !ok {
			t.Errorf("%s has no permission entry", op)
			continue
		}
		if got != role {
			t.Errorf("%s requires %v, want %v", op, got, role)
		}
	}
}
