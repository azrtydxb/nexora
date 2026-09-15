import { test, expect, env, login } from "../fixtures";

// Catches a threat dialog that never runs the check, a verdict that is not rendered, and a block
// list whose AI classification is missing from its details.
for (const width of [1280, 400]) {
  test(`threat check and list classification at ${width}px`, async ({
    page,
  }) => {
    await page.setViewportSize({ width, height: 900 });
    await login(
      page,
      env("NEXORA_E2E_VIEWER_USER"),
      env("NEXORA_E2E_VIEWER_PASSWORD"),
    );
    const domain = env("NEXORA_E2E_AI_THREAT_DOMAIN");
    const listId = env("NEXORA_E2E_AI_LIST_ID");
    const listName = env("NEXORA_E2E_AI_LIST_NAME");

    await page.getByTestId("nav-filtering").scrollIntoViewIfNeeded();
    await page.getByTestId("nav-filtering").click();
    await page.getByTestId("ai-threat-open").click();
    await page.getByTestId("ai-threat-domains").fill(domain);

    const succeeded = page.waitForResponse(async (r) => {
      if (!r.url().includes("/api/v1/ai/tasks/") || r.status() !== 200)
        return false;
      const body = await r.json().catch(() => null);
      return body?.status === "succeeded";
    });
    await page.getByTestId("ai-threat-submit").click();
    await succeeded;

    const verdict = page.getByTestId("ai-threat-result").first();
    await expect(verdict).toContainText(domain);
    await expect(verdict).toContainText("phishing");

    await page.keyboard.press("Escape");
    await expect(page.getByTestId("ai-threat-domains")).toHaveCount(0);

    await page.getByTestId(`list-open-${listName}`).click();
    const classification = page.getByTestId(`ai-list-classification-${listId}`);
    await expect(classification).toContainText("tracking");
    await expect(classification).toContainText("200 sampled names");
  });
}
