import { test, expect, env, login } from "../fixtures";

for (const width of [1280, 400]) {
  test(`viewer sees AI read-only at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await login(
      page,
      env("NEXORA_E2E_VIEWER_USER"),
      env("NEXORA_E2E_VIEWER_PASSWORD"),
    );
    await page.goto("/ai/insights");
    await expect(page.getByTestId(/^ai-finding-/).first()).toBeVisible();
    await expect(page.getByTestId("ai-finding-ack")).toHaveCount(0);
    await page.goto("/ai/recommendations");
    await expect(page.getByTestId(/^ai-proposal-/).first()).toBeVisible();
    await expect(page.getByTestId("ai-proposal-apply")).toHaveCount(0);
    await expect(page.getByTestId("ai-proposal-dismiss")).toHaveCount(0);
    await expect(page.getByTestId("nav-ai-assistant")).toHaveCount(0);
    await page.goto("/ai/assistant");
    await expect(page).toHaveURL(/\/ai$/);
  });
}
