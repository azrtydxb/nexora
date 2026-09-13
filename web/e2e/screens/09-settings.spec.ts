import { test, expect, env, login } from "../fixtures";

test("resolver settings and config versions", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-settings").click();
  await expect(page.getByTestId("version-row").first()).toBeVisible();
  const before = await page.getByTestId("version-row").count();
  await page.getByTestId("settings-block-ttl").fill("120");
  await page.getByTestId("settings-save").click();
  await expect(page.getByText("Settings saved")).toBeVisible();
  await page.reload();
  await expect(page.getByTestId("settings-block-ttl")).toHaveValue("120");
  await expect(page.getByTestId("version-row")).toHaveCount(before + 1);
});
