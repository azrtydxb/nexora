import type React from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Navigate, useLocation, useNavigate } from "react-router";

import { api, unwrap, type Schemas } from "@/api/client";
import { roleCan, type OperationId } from "@/auth/permissions";

const meKey = ["me"] as const;

/** The signed-in user, or null when the session is missing or expired. */
export function useCurrentUser(): {
  user: Schemas["User"] | null;
  loading: boolean;
} {
  const q = useQuery({
    queryKey: meKey,
    queryFn: async () => {
      const r = await api.GET("/auth/me");
      if (r.response.status === 401) return null;
      return unwrap(r);
    },
    staleTime: 60_000,
  });
  return { user: q.data ?? null, loading: q.isPending };
}

/** Whether the signed-in user's role allows operationId (the server still enforces it). */
export function useCan(operationId: OperationId): boolean {
  const { user } = useCurrentUser();
  return user !== null && roleCan(user.role, operationId);
}

export function useSetupStatus(enabled = true) {
  return useQuery({
    queryKey: ["setup-status"],
    queryFn: async () => unwrap(await api.GET("/setup")),
    enabled,
  });
}

/** Records the user returned by login or setup as the current session. */
export function useSignedIn() {
  const qc = useQueryClient();
  return (user: Schemas["User"]) => {
    qc.clear();
    qc.setQueryData(meKey, user);
  };
}

export function useLogout() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  return async () => {
    await api.POST("/auth/logout");
    qc.clear();
    qc.setQueryData(meKey, null);
    navigate("/login", { replace: true });
  };
}

/** A local path safe to navigate to after login, or "/". */
export function safeReturnTo(p: string | null): string {
  if (!p || !p.startsWith("/") || p.startsWith("//") || /[\\\r\n]/.test(p))
    return "/";
  return p;
}

export function RequireAuth({ children }: { children: React.ReactNode }) {
  const { user, loading } = useCurrentUser();
  const location = useLocation();
  const setup = useSetupStatus(!loading && user === null);
  if (loading || (user === null && setup.isPending)) {
    return <div className="text-muted-foreground p-8 text-sm">Loading…</div>;
  }
  if (user === null) {
    if (setup.data?.required) return <Navigate to="/setup" replace />;
    const returnTo = location.pathname + location.search;
    return (
      <Navigate
        to={`/login?return_to=${encodeURIComponent(returnTo)}`}
        replace
      />
    );
  }
  return <>{children}</>;
}
