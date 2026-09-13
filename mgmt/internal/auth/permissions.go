package auth

import (
	"fmt"
)

// Role is a user's or token's authorization level.
type Role string

// Roles, from least to most privileged.
const (
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
)

var roleRank = map[Role]int{RoleViewer: 1, RoleOperator: 2, RoleAdmin: 3}

// ParseRole validates s as a role.
func ParseRole(s string) (Role, error) {
	r := Role(s)
	if _, ok := roleRank[r]; !ok {
		return "", fmt.Errorf("unknown role %q", s)
	}
	return r, nil
}

// AtLeast reports whether r is min or more privileged. Unknown roles are never at least anything.
func (r Role) AtLeast(min Role) bool {
	rank, ok := roleRank[r]
	return ok && rank >= roleRank[min]
}

// Public lists the OpenAPI operations that need no authentication.
var Public = map[string]bool{
	"getHealth":         true,
	"getSetupStatus":    true,
	"completeSetup":     true,
	"login":             true,
	"listAuthProviders": true,
	"startOidcLogin":    true,
	"oidcCallback":      true,
}

// Permissions maps every authenticated OpenAPI operation to the minimum role it requires.
var Permissions = map[string]Role{
	"logout":              RoleViewer,
	"getCurrentUser":      RoleViewer,
	"getDashboard":        RoleViewer,
	"listUpstreams":       RoleViewer,
	"getResolverSettings": RoleViewer,
	"getAccessControl":    RoleViewer,
	"listFilterLists":     RoleViewer,
	"getFilterList":       RoleViewer,
	"getAllowlist":        RoleViewer,
	"listEngines":         RoleViewer,
	"getEngine":           RoleViewer,
	"listConfigVersions":  RoleViewer,
	"searchQueryLog":      RoleViewer,
	"listPolicyGroups":    RoleViewer,
	"getPolicyGroup":      RoleViewer,
	"getGlobalSafeSearch": RoleViewer,
	"listRewrites":        RoleViewer,

	"createUpstream":         RoleOperator,
	"updateUpstream":         RoleOperator,
	"deleteUpstream":         RoleOperator,
	"updateResolverSettings": RoleOperator,
	"updateAccessControl":    RoleOperator,
	"createFilterList":       RoleOperator,
	"updateFilterList":       RoleOperator,
	"deleteFilterList":       RoleOperator,
	"refreshFilterList":      RoleOperator,
	"updateAllowlist":        RoleOperator,
	"createPolicyGroup":      RoleOperator,
	"updatePolicyGroup":      RoleOperator,
	"deletePolicyGroup":      RoleOperator,
	"updateGlobalSafeSearch": RoleOperator,
	"createRewrite":          RoleOperator,
	"updateRewrite":          RoleOperator,
	"deleteRewrite":          RoleOperator,

	"listUsers":       RoleAdmin,
	"createUser":      RoleAdmin,
	"updateUser":      RoleAdmin,
	"deleteUser":      RoleAdmin,
	"listApiTokens":   RoleAdmin,
	"createApiToken":  RoleAdmin,
	"revokeApiToken":  RoleAdmin,
	"listAuditEvents": RoleAdmin,
	"listJoinTokens":  RoleAdmin,
	"createJoinToken": RoleAdmin,
	"revokeJoinToken": RoleAdmin,
	"deleteEngine":    RoleAdmin,
}

// Authorize returns nil when p may call operationID and ErrForbidden otherwise. Operations
// absent from Permissions are forbidden (Public operations never reach Authorize).
func Authorize(p Principal, operationID string) error {
	min, ok := Permissions[operationID]
	if !ok || !p.Role.AtLeast(min) {
		return fmt.Errorf("%w: %s requires %s", ErrForbidden, operationID, orUnknown(min))
	}
	return nil
}

func orUnknown(r Role) string {
	if r == "" {
		return "an unknown permission"
	}
	return "role " + string(r)
}
