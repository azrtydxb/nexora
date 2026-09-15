import { test, expect, env, login } from "../fixtures";

// Applying and dismissing a proposal is not idempotent for the seeded ids, so the writes run once,
// at 1280px; the 400px test only reads the list and one proposal's details.
test("AI recommendations at 1280px", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  const applyId = env("NEXORA_E2E_AI_PROPOSAL_APPLY");
  const dismissId = env("NEXORA_E2E_AI_PROPOSAL_DISMISS");
  const applyCard = page.getByTestId(`ai-proposal-${applyId}`);
  const dismissCard = page.getByTestId(`ai-proposal-${dismissId}`);
  const status = async (value: string) => {
    await page.getByTestId("ai-proposals-status").click();
    await page.getByTestId(`ai-proposals-status-${value}`).click();
  };

  await page.goto("/ai/recommendations");
  await expect(applyCard).toBeVisible();
  await expect(dismissCard).toBeVisible();
  await expect(
    page.getByTestId("ai-proposal-impact").filter({ hasText: "27.3" }),
  ).toBeVisible();

  // The details of the proposal show the live resource beside the proposed body.
  await applyCard.getByTestId("ai-proposal-details").click();
  await expect(applyCard.getByTestId("json-diff").first()).toBeVisible();

  // Apply from the "all" list: the card keeps its apply dialog, and so its result, on the refetch.
  await status("all");
  await expect(applyCard).toBeVisible();
  await applyCard.getByTestId("ai-proposal-apply").click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByTestId("ai-apply-action")).toContainText(
    "updateFilterCategory",
  );
  await dialog.getByTestId("ai-apply-acknowledge-license").check();
  const applied = page.waitForResponse(
    (r) => r.url().includes("/api/v1/ai/proposals/apply") && r.status() === 200,
  );
  await dialog.getByTestId("ai-apply-confirm").click();
  await applied;
  await expect(dialog.getByTestId("ai-apply-result")).toContainText("applied");
  // first(): the dialog's own X button is also named "Close".
  await dialog.getByRole("button", { name: "Close" }).first().click();

  await status("open");
  await expect(applyCard).toHaveCount(0); // gone from the open list
  await expect(dismissCard).toBeVisible();

  // Dismiss the upstream prediction with a reason, which the dismissed list keeps.
  await dismissCard.getByTestId("ai-proposal-dismiss").click();
  await page.getByTestId("ai-dismiss-reason").fill("not now");
  const dismissed = page.waitForResponse(
    (r) =>
      r.url().includes("/api/v1/ai/proposals/dismiss") && r.status() === 200,
  );
  await page.getByTestId("ai-dismiss-confirm").click();
  await dismissed;
  await expect(dismissCard).toHaveCount(0);

  await status("dismissed");
  await expect(dismissCard).toContainText("not now");
});

test("AI recommendations at 400px", async ({ page }) => {
  await page.setViewportSize({ width: 400, height: 900 });
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  const applyCard = page.getByTestId(
    `ai-proposal-${env("NEXORA_E2E_AI_PROPOSAL_APPLY")}`,
  );

  await page.goto("/ai/recommendations?status=all");
  await expect(applyCard).toBeVisible();
  await applyCard.getByTestId("ai-proposal-details").click();
  await expect(applyCard.getByTestId("json-diff").first()).toBeVisible();

  // The source tabs narrow the list; RPZ suggestions live on the RPZ page.
  await page.getByTestId("ai-recommendations-tab-upstream_prediction").click();
  await expect(applyCard).toHaveCount(0);
  await page.getByTestId("ai-recommendations-tab-rpz_suggestions").click();
  await expect(page.getByTestId("ai-recommendations-rpz-link")).toHaveAttribute(
    "href",
    "/rpz?tab=ai",
  );
});
