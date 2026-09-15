import { test, expect, env, login } from "../fixtures";

// The state this spec leaves behind (one acknowledged anomaly, one dismissed insight) survives into
// the 400px run, so every write step first checks whether it still has something to do.
for (const width of [1280, 400]) {
  test(`AI insights at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await login(
      page,
      env("NEXORA_E2E_OPERATOR_USER"),
      env("NEXORA_E2E_OPERATOR_PASSWORD"),
    );
    const anomaly = env("NEXORA_E2E_AI_ANOMALY");
    const client = env("NEXORA_E2E_AI_ANOMALY_CLIENT");
    const insight = env("NEXORA_E2E_AI_INSIGHT");
    const dismissed = env("NEXORA_E2E_AI_INSIGHT_DISMISS");
    const patched = () =>
      page.waitForResponse(
        (r) =>
          r.request().method() === "PATCH" &&
          r.url().includes("/api/v1/ai/findings/") &&
          r.status() === 200,
      );

    await page.goto("/ai/insights");
    const card = page.getByTestId(`ai-finding-${anomaly}`);
    const ack = card.getByTestId("ai-finding-ack");
    if (await ack.count()) {
      await expect(card).toContainText(client);
      await expect(card.getByTestId("ai-anomaly-query-log")).toHaveAttribute(
        "href",
        `/query-log?client=${client}`,
      );
      const done = patched();
      await ack.click();
      await done;
      await expect(card).toHaveCount(0); // gone from the open list
    }

    // The acknowledged anomaly keeps its state badge under the status filter.
    await page.getByTestId("ai-findings-status").click();
    await page.getByTestId("ai-findings-status-acknowledged").click();
    await expect(card.getByTestId("ai-finding-state")).toHaveText(
      /acknowledged/i,
    );
    await page.getByTestId("ai-findings-status").click();
    await page.getByTestId("ai-findings-status-open").click();

    await page.getByTestId("ai-insights-tab-insight").click();
    await expect(page).toHaveURL(/kind=insight/);
    const insightCard = page.getByTestId(`ai-finding-${insight}`);
    await expect(
      insightCard.getByTestId("ai-insight-cause").first(),
    ).toContainText("fixture upstream");

    const stale = page.getByTestId(`ai-finding-${dismissed}`);
    const dismiss = stale.getByTestId("ai-finding-dismiss");
    if (await dismiss.count()) {
      const done = patched();
      await dismiss.click();
      await done;
    }
    await expect(stale).toHaveCount(0);

    await page.goto("/dashboard");
    const aiCard = page.getByTestId("dashboard-ai-card");
    await expect(aiCard.getByTestId("ai-score")).toHaveText(/^\d+\/10$/);
    await expect(aiCard.getByTestId("ai-insight-top").first()).toContainText(
      "SERVFAIL",
    );
    await aiCard.getByTestId("dashboard-ai-card-all").click();
    await expect(page).toHaveURL(/\/ai\/insights\?kind=insight/);
    await expect(insightCard).toBeVisible();
  });
}
