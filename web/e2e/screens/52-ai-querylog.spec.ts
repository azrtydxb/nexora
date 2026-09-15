import { test, expect, env, login } from "../fixtures";

// Catches an Ask box that does not run the search, a result that does not become the table's
// filters, a missing anomaly banner and a cached threat verdict that is not labelled on the row.
for (const width of [1280, 400]) {
  test(`query log AI ask, anomaly banner and threat badge at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    await login(
      page,
      env("NEXORA_E2E_VIEWER_USER"),
      env("NEXORA_E2E_VIEWER_PASSWORD"),
    );
    const name = env("NEXORA_E2E_AI_THREAT_NAME");
    await page.getByTestId("nav-query-log").scrollIntoViewIfNeeded();
    await page.getByTestId("nav-query-log").click();
    await expect(page.getByTestId("querylog-ai-anomalies")).toBeVisible();

    const ask = page.getByTestId("querylog-ai-ask");
    await ask.scrollIntoViewIfNeeded();
    await ask.fill(`lookups of ${name}`);
    const succeeded = page.waitForResponse(async (r) => {
      if (!r.url().includes("/api/v1/ai/tasks/") || r.status() !== 200)
        return false;
      const body = await r.json().catch(() => null);
      return body?.status === "succeeded";
    });
    await page.getByTestId("querylog-ai-ask-submit").click();
    await succeeded;

    await expect(page.getByTestId("querylog-ai-summary")).toContainText(
      "1 lookup of threat.aiq-gui.test.",
    );
    await expect(page.getByTestId("querylog-ai-explanation")).toContainText(
      "Lookups of threat.aiq-gui.test",
    );
    await expect(page).toHaveURL(/name=threat\.aiq-gui/);

    const row = page
      .getByTestId("querylog-row")
      .filter({ hasText: name })
      .first();
    await expect(row.getByTestId("querylog-threat")).toContainText("malware");
  });
}
