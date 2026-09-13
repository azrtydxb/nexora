// Mirrors mgmt/internal/auth/permissions.go; `pnpm lint` (scripts/check-permissions.mjs) fails when
// the two differ. The server is the authority: this map only decides what the GUI offers.

export type Role = "viewer" | "operator" | "admin";

export const permissions = {
  getHealth: "public",
  getSetupStatus: "public",
  completeSetup: "public",
  login: "public",
  listAuthProviders: "public",
  startOidcLogin: "public",
  oidcCallback: "public",

  logout: "viewer",
  getCurrentUser: "viewer",
  getDashboard: "viewer",
  listUpstreams: "viewer",
  getResolverSettings: "viewer",
  getAccessControl: "viewer",
  listFilterLists: "viewer",
  getFilterList: "viewer",
  getAllowlist: "viewer",
  listEngines: "viewer",
  getEngine: "viewer",
  listConfigVersions: "viewer",
  searchQueryLog: "viewer",

  createUpstream: "operator",
  updateUpstream: "operator",
  deleteUpstream: "operator",
  updateResolverSettings: "operator",
  updateAccessControl: "operator",
  createFilterList: "operator",
  updateFilterList: "operator",
  deleteFilterList: "operator",
  refreshFilterList: "operator",
  updateAllowlist: "operator",

  listUsers: "admin",
  createUser: "admin",
  updateUser: "admin",
  deleteUser: "admin",
  listApiTokens: "admin",
  createApiToken: "admin",
  revokeApiToken: "admin",
  listAuditEvents: "admin",
  listJoinTokens: "admin",
  createJoinToken: "admin",
  revokeJoinToken: "admin",
  deleteEngine: "admin",
} as const satisfies Record<string, Role | "public">;

export type OperationId = keyof typeof permissions;

const rank: Record<Role, number> = { viewer: 1, operator: 2, admin: 3 };

/** Reports whether role may call operationId. */
export function roleCan(
  role: Role | undefined,
  operationId: OperationId,
): boolean {
  const need: Role | "public" = permissions[operationId];
  if (need === "public") return true;
  return role !== undefined && rank[role] >= rank[need];
}
