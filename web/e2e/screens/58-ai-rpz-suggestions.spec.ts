import { test, expect, env, login } from "../fixtures";

// Applying and rejecting the seeded suggestions is not idempotent, so the writes run once, at
// 1280px; the 400px test only reads the tab.
test("AI RPZ suggestions at 1280px", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  const row = (record: string) => page.getByTestId(`ai-rpz-row-${record}`);

  await page.goto("/rpz");
  await page.getByTestId("rpz-tab-ai").click();
  await expect(row("c2.gui-rpz.test")).toContainText("malware");
  await expect(row("keep.gui-rpz.test")).toContainText("Third-party tracker");

  // Apply the two malicious records into the ai-suggested.rpz zone.
  await row("c2.gui-rpz.test").getByRole("checkbox").check();
  await row("phish.gui-rpz.test").getByRole("checkbox").check();
  await page.getByTestId("ai-rpz-apply-selected").click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByTestId("ai-apply-action").first()).toContainText(
    "appendAiRpzRules",
  );
  const applied = page.waitForResponse(
    (r) => r.url().includes("/api/v1/ai/proposals/apply") && r.status() === 200,
  );
  await dialog.getByTestId("ai-apply-confirm").click();
  await applied;
  await expect(dialog.getByTestId("ai-apply-result")).toHaveCount(2);
  await expect(dialog.getByTestId("ai-apply-result").first()).toContainText(
    "applied",
  );
  await expect(dialog.getByTestId("ai-apply-result").last()).toContainText(
    "applied",
  );
  // first(): the dialog's own X button is also named "Close".
  await dialog.getByRole("button", { name: "Close" }).first().click();
  await expect(row("c2.gui-rpz.test")).toHaveCount(0);

  // The applied rules live in a file zone the apply created.
  await page.getByTestId("rpz-tab-zones").click();
  await expect(
    page.getByRole("row", { name: /ai-suggested\.rpz/ }),
  ).toContainText("2 records");

  // The tracker is rejected with a reason instead.
  await page.getByTestId("rpz-tab-ai").click();
  await row("keep.gui-rpz.test").getByRole("checkbox").check();
  await page.getByTestId("ai-rpz-reject-selected").click();
  await page.getByTestId("ai-dismiss-reason").fill("the tool still needs it");
  const dismissed = page.waitForResponse(
    (r) =>
      r.url().includes("/api/v1/ai/proposals/dismiss") && r.status() === 200,
  );
  await page.getByTestId("ai-dismiss-confirm").click();
  await dismissed;
  await expect(row("keep.gui-rpz.test")).toHaveCount(0);
});

test("AI RPZ suggestions at 400px", async ({ page }) => {
  await page.setViewportSize({ width: 400, height: 900 });
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );

  await page.goto("/rpz?tab=ai");
  await expect(page.getByTestId("ai-rpz-zone-notice")).toContainText(
    "ai-suggested.rpz",
  );
  // The 1280px test applied or rejected every seeded suggestion, so the list is empty here.
  await expect(page.getByTestId("ai-rpz-empty")).toBeVisible();
});
