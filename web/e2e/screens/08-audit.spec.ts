import { test, expect, env, login } from "../fixtures";

test("audit log lists changes with diffs", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-audit").click();
  const row = page
    .locator('[data-testid="audit-row"][data-action="createUser"]')
    .first();
  await expect(row).toBeVisible();
  await row.click();
  await expect(
    page.locator('[data-testid^="audit-diff-"]').first(),
  ).toContainText("gui-user");
});
