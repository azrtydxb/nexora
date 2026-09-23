import { CatalogZonesPage } from "@/pages/CatalogZonesPage";
import { createBrowserRouter, Navigate } from "react-router";

import { RequireAuth } from "@/auth/AuthProvider";
import { AppShell } from "@/components/layout/AppShell";
import { AccessControlPage } from "@/pages/AccessControlPage";
import { AccountPage } from "@/pages/AccountPage";
import { ApiTokensPage } from "@/pages/ApiTokensPage";
import { AuditPage } from "@/pages/AuditPage";
import { DashboardPage } from "@/pages/DashboardPage";
import { DnssecPage } from "@/pages/DnssecPage";
import { EngineDetailPage } from "@/pages/EngineDetailPage";
import { EngineGroupPage } from "@/pages/EngineGroupPage";
import { EnginesPage } from "@/pages/EnginesPage";
import { FilterCategoriesPage } from "@/pages/FilterCategoriesPage";
import { FilteringPage } from "@/pages/FilteringPage";
import HelpPage from "@/pages/HelpPage";
import { LoginPage } from "@/pages/LoginPage";
import { PoliciesPage } from "@/pages/PoliciesPage";
import { QueryLogPage } from "@/pages/QueryLogPage";
import { RewritesPage } from "@/pages/RewritesPage";
import { RolloutPage } from "@/pages/RolloutPage";
import { RpzPage } from "@/pages/RpzPage";
import { SettingsPage } from "@/pages/SettingsPage";
import { SetupPage } from "@/pages/SetupPage";
import { TsigKeysPage } from "@/pages/TsigKeysPage";
import { UpstreamsPage } from "@/pages/UpstreamsPage";
import { UsersPage } from "@/pages/UsersPage";
import { ZoneDetailPage } from "@/pages/ZoneDetailPage";
import { ZonesPage } from "@/pages/ZonesPage";
import { AiAssistantPage } from "@/pages/ai/AiAssistantPage";
import { AiForecastsPage } from "@/pages/ai/AiForecastsPage";
import { AiInsightsPage } from "@/pages/ai/AiInsightsPage";
import { AiRecommendationsPage } from "@/pages/ai/AiRecommendationsPage";
import { AiStatusPage } from "@/pages/ai/AiStatusPage";

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
      { path: "resolution", element: <UpstreamsPage /> },
      { path: "upstreams", element: <Navigate to="/resolution" replace /> },
      { path: "access-control", element: <AccessControlPage /> },
      { path: "filtering", element: <FilteringPage /> },
      { path: "filtering/categories", element: <FilterCategoriesPage /> },
      { path: "policies", element: <PoliciesPage /> },
      { path: "rewrites", element: <RewritesPage /> },
      { path: "rpz", element: <RpzPage /> },
      { path: "dnssec", element: <DnssecPage /> },
      { path: "zones", element: <ZonesPage /> },
      { path: "zones/catalogs", element: <CatalogZonesPage /> },
      { path: "zones/tsig-keys", element: <TsigKeysPage /> },
      { path: "zones/:zoneId", element: <ZoneDetailPage /> },
      { path: "engines", element: <EnginesPage /> },
      { path: "engines/groups/:id", element: <EngineGroupPage /> },
      { path: "engines/nodes/:id", element: <EngineDetailPage /> },
      { path: "engines/rollouts/:id", element: <RolloutPage /> },
      { path: "users", element: <UsersPage /> },
      { path: "api-tokens", element: <ApiTokensPage /> },
      { path: "audit", element: <AuditPage /> },
      { path: "account", element: <AccountPage /> },
      { path: "help", element: <HelpPage /> },
      { path: "help/:topic", element: <HelpPage /> },
      { path: "settings", element: <SettingsPage /> },
      { path: "ai", element: <AiStatusPage /> },
      { path: "ai/insights", element: <AiInsightsPage /> },
      { path: "ai/recommendations", element: <AiRecommendationsPage /> },
      { path: "ai/assistant", element: <AiAssistantPage /> },
      { path: "ai/forecasts", element: <AiForecastsPage /> },
      { path: "*", element: <Navigate to="/" replace /> },
    ],
  },
]);
