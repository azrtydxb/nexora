import { test, expect, env, login } from "../fixtures";

test("AI off hides every AI surface", async ({ page }) => {
  await page.route("**/api/v1/ai/status", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        enabled: false,
        reason: "not_configured",
        model: "",
        endpoint_host: "",
        structured_output: "json_schema",
        budget: {
          day: "2026-09-15",
          limit_tokens: 0,
          used_tokens: 0,
          background_limit_tokens: 0,
        },
        agents: [],
        features: {
          querylog_search: false,
          config_assistant: false,
          threat_check: false,
        },
        mcp: { enabled: false, read_only: true },
      }),
    }),
  );
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await expect(page.getByTestId("nav-dashboard")).toBeVisible(); // positive path first
  await expect(page.getByTestId("nav-ai")).toHaveCount(0);
  await expect(page.getByTestId("dashboard-ai-card")).toHaveCount(0);
  await page.getByTestId("nav-query-log").click();
  await expect(page.getByTestId("querylog-ai-ask")).toHaveCount(0);
  for (const path of [
    "/ai",
    "/ai/insights",
    "/ai/recommendations",
    "/ai/forecasts",
  ]) {
    await page.goto(path);
    await expect(page.getByTestId("ai-off")).toContainText("not_configured");
  }
});
