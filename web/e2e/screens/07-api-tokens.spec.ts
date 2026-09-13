import { test, expect, env, login } from "../fixtures";

test("api tokens create and revoke", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-api-tokens").click();
  await page.getByTestId("token-add").click();
  await page.getByTestId("token-name").fill("gui-ci");
  await page.getByTestId("token-save").click();
  await expect(page.getByTestId("token-value")).toContainText("nxt_");
  await page.keyboard.press("Escape");
  await page.getByTestId("token-revoke-gui-ci").click();
  await page.getByTestId("confirm-delete").click();
  await expect(page.getByTestId("token-row-gui-ci")).toContainText("revoked");
});
