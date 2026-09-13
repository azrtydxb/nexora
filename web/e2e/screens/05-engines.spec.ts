import { test, expect, env, login } from "../fixtures";

test("engines and join tokens", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-engines").click();
  await expect(page.getByTestId("engine-row-gui-engine")).toContainText(
    "current",
  );
  await page.getByTestId("engine-open-gui-engine-2").click();
  await expect(page.getByTestId("engine-detail")).toContainText("gui-engine-2");
  await page.getByTestId("engine-delete").click();
  await page.getByTestId("confirm-delete").click();
  await expect(page.getByTestId("engine-row-gui-engine-2")).toHaveCount(0);
  await page.getByTestId("jointoken-add").click();
  await page.getByTestId("jointoken-name").fill("gui-token");
  await page.getByTestId("jointoken-save").click();
  await expect(page.getByTestId("jointoken-value")).toContainText("nxj1.");
  await page.keyboard.press("Escape");
  await page.getByTestId("jointoken-revoke-gui-token").click();
  await page.getByTestId("confirm-delete").click();
  await expect(page.getByTestId("jointoken-row-gui-token")).toContainText(
    "revoked",
  );
});
