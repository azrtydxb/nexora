import { test, expect, env, login } from "../fixtures";

for (const width of [1280, 400]) {
  test(`AI status page at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await login(
      page,
      env("NEXORA_E2E_OPERATOR_USER"),
      env("NEXORA_E2E_OPERATOR_PASSWORD"),
    );
    await page.goto("/ai");
    await expect(page.getByTestId("ai-status-model")).toHaveText("fake-qwen");
    await expect(page.getByTestId("ai-status-endpoint")).toHaveText(
      "127.0.0.1",
    );
    await expect(page.getByText("test-key-not-secret")).toHaveCount(0);
    await expect(page.getByTestId("ai-budget")).toBeVisible();
    const row = page.getByTestId("ai-agent-capacity_forecast");
    await expect(row).toBeVisible();
    const run = page.waitForResponse(
      (r) =>
        r.url().includes("/api/v1/ai/agents/capacity_forecast/run") &&
        r.status() === 202,
    );
    await row.getByTestId("ai-agent-run").click();
    await run;
    await expect(page.getByTestId("ai-mcp-state")).toContainText("MCP");
  });
}
