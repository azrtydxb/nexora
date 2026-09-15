import { test, expect, env, login } from "../fixtures";

// Read-only: a viewer reads the assessment the agent stored, so the spec is safe to repeat per width.
for (const width of [1280, 400]) {
  test(`AI rollout risk at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await login(
      page,
      env("NEXORA_E2E_VIEWER_USER"),
      env("NEXORA_E2E_VIEWER_PASSWORD"),
    );

    await page.goto(`/engines/rollouts/${env("NEXORA_E2E_AI_ROLLOUT_ID")}`);
    await expect(page.getByTestId("rollout-detail")).toBeVisible();

    const card = page.getByTestId("ai-rollout-risk");
    await expect(card.getByTestId("ai-risk-score")).toHaveText("5/10");
    await expect(card.getByTestId("ai-risk-level")).toHaveText("medium");
    await expect(card.getByTestId("ai-risk-analysis")).toContainText(
      "Similar changes halted once.",
    );
    await expect(card.getByTestId("ai-risk-history")).toHaveCount(1);
    await expect(card.getByTestId("ai-risk-history")).toContainText("v3");
    await expect(card).toContainText("canary");
  });
}
