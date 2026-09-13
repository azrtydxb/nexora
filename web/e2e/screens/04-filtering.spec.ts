import { test, expect, env, login } from "../fixtures";

test("filter lists and allowlist", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-filtering").click();
  await page.getByTestId("list-add").click();
  await page.getByTestId("list-name").fill("gui-list");
  await page.getByTestId("list-url").fill(env("NEXORA_E2E_LIST_URL"));
  await page.getByTestId("list-interval").fill("3600");
  await page.getByTestId("list-save").click();
  await expect(page.getByTestId("list-row-gui-list")).toBeVisible();
  await page.getByTestId("list-open-gui-list").click();
  await expect(page.getByTestId("list-detail")).toContainText("gui-list");
  await page.getByTestId("list-refresh").click();
  await expect(page.getByTestId("list-detail")).toContainText(/2 entries/);
  await page.getByTestId("list-edit").click();
  await page.getByTestId("list-interval").fill("7200");
  await page.getByTestId("list-save").click();
  await expect(page.getByTestId("list-row-gui-list")).toContainText("7200");
  await page.getByTestId("allowlist-input").fill("ok.gui.test");
  await page.getByTestId("allowlist-add").click();
  await page.getByTestId("allowlist-save").click();
  await expect(page.getByText("Allowlist saved")).toBeVisible();
  await page.reload();
  await expect(page.getByTestId("allowlist-row-ok.gui.test")).toBeVisible();
  await page.getByTestId("list-open-gui-list").click();
  await page.getByTestId("list-delete").click();
  await page.getByTestId("confirm-delete").click();
  await expect(page.getByTestId("list-row-gui-list")).toHaveCount(0);
});
