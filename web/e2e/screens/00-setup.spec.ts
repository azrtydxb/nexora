import { test, expect, env } from "../fixtures";

test("first-run setup creates the admin", async ({ page }) => {
  await page.goto("/");
  await expect(page).toHaveURL(/\/setup/);
  await page.getByTestId("setup-token").fill(env("NEXORA_E2E_SETUP_TOKEN"));
  await page.getByTestId("setup-username").fill(env("NEXORA_E2E_ADMIN_USER"));
  await page.getByTestId("setup-email").fill("admin@example.test");
  await page
    .getByTestId("setup-password")
    .fill(env("NEXORA_E2E_ADMIN_PASSWORD"));
  await page.getByTestId("setup-submit").click();
  await expect(page.getByTestId("user-menu")).toContainText(
    env("NEXORA_E2E_ADMIN_USER"),
  );
  await expect(page.getByTestId("health-badge")).toContainText("ok");
});
