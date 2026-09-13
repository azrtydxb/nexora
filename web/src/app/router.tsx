import { createBrowserRouter, Navigate } from "react-router";

import { RequireAuth } from "@/auth/AuthProvider";
import { AppShell } from "@/components/layout/AppShell";
import { AccessControlPage } from "@/pages/AccessControlPage";
import { ApiTokensPage } from "@/pages/ApiTokensPage";
import { AuditPage } from "@/pages/AuditPage";
import { DashboardPage } from "@/pages/DashboardPage";
import { EnginesPage } from "@/pages/EnginesPage";
import { FilteringPage } from "@/pages/FilteringPage";
import { LoginPage } from "@/pages/LoginPage";
import { QueryLogPage } from "@/pages/QueryLogPage";
import { SettingsPage } from "@/pages/SettingsPage";
import { SetupPage } from "@/pages/SetupPage";
import { UpstreamsPage } from "@/pages/UpstreamsPage";
import { UsersPage } from "@/pages/UsersPage";

export const router = createBrowserRouter([
  { path: "/login", element: <LoginPage /> },
  { path: "/setup", element: <SetupPage /> },
  {
    path: "/",
    element: (
      <RequireAuth>
        <AppShell />
      </RequireAuth>
    ),
    children: [
      { index: true, element: <DashboardPage /> },
      { path: "query-log", element: <QueryLogPage /> },
      { path: "upstreams", element: <UpstreamsPage /> },
      { path: "access-control", element: <AccessControlPage /> },
      { path: "filtering", element: <FilteringPage /> },
      { path: "engines", element: <EnginesPage /> },
      { path: "users", element: <UsersPage /> },
      { path: "api-tokens", element: <ApiTokensPage /> },
      { path: "audit", element: <AuditPage /> },
      { path: "settings", element: <SettingsPage /> },
      { path: "*", element: <Navigate to="/" replace /> },
    ],
  },
]);
