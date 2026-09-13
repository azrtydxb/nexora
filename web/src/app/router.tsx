import { createBrowserRouter, Navigate } from "react-router";

import { RequireAuth } from "@/auth/AuthProvider";
import { AppShell } from "@/components/layout/AppShell";
import { AuditPage } from "@/pages/AuditPage";
import { LoginPage } from "@/pages/LoginPage";
import { SetupPage } from "@/pages/SetupPage";
import { UpstreamsPage } from "@/pages/UpstreamsPage";

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
      // debt: the dashboard lands with Task 20; until then / opens the upstreams screen.
      { index: true, element: <Navigate to="/upstreams" replace /> },
      { path: "upstreams", element: <UpstreamsPage /> },
      { path: "audit", element: <AuditPage /> },
      { path: "*", element: <Navigate to="/" replace /> },
    ],
  },
]);
