import { test, expect, env, login, logout } from "../fixtures";

test("dashboard shows fleet stats", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-dashboard").click();
  await expect(page.getByTestId("dashboard-engines")).toContainText(
    /\d+ \/ \d+/,
  );
  await expect(page.getByTestId("dashboard-qps")).toBeVisible();
  await expect(page.getByTestId("dashboard-cache-hit-ratio")).toBeVisible();
  await expect(page.getByTestId("dashboard-chart")).toBeVisible();
  await logout(page);
});
