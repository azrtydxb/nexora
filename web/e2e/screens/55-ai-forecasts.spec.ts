import { test, expect, env, login } from "../fixtures";

// Read-only: a viewer sees the forecasts the agents stored, so the spec is safe to repeat per width.
for (const width of [1280, 400]) {
  test(`AI forecasts at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await login(
      page,
      env("NEXORA_E2E_VIEWER_USER"),
      env("NEXORA_E2E_VIEWER_PASSWORD"),
    );
    const upstream = env("NEXORA_E2E_AI_UPSTREAM_FORECAST");

    await page.goto("/ai/forecasts");
    const prediction = page.getByTestId(`ai-forecast-${upstream}`);
    await expect(prediction).toContainText("degrading");
    await expect(prediction).toContainText("Switch the upstream strategy");
    await expect(prediction.getByTestId("ai-forecast-freshness")).toContainText(
      "valid until",
    );
    await expect(
      prediction.getByTestId("ai-forecast-freshness"),
    ).not.toContainText("stale");

    await page.getByTestId("ai-forecasts-kind").click();
    await page.getByTestId("ai-forecasts-kind-capacity").click();
    await expect(page).toHaveURL(/kind=capacity/);
    const capacity = page.getByTestId("ai-forecast-recursor_cache");
    await expect(capacity.getByTestId("ai-capacity-days")).toHaveText(
      "12 days",
    );
    await expect(capacity.getByTestId("ai-capacity-bar")).toBeVisible();
    await expect(prediction).toHaveCount(0);

    await page.goto("/resolution");
    const panel = page.getByTestId("ai-upstream-predictions");
    await expect(panel).toContainText("fixture");
    await expect(panel).toContainText("degrading");
    await expect(panel).toContainText("350 ms");
  });
}
