import { test, expect, env, login } from "../fixtures";

const sections = [
  "traffic",
  "latency",
  "cache",
  "resolution",
  "filtering",
  "dnssec",
  "top",
  "fleet",
  "health",
];

test("dashboard ranges and sections render with data", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_VIEWER_USER"),
    env("NEXORA_E2E_VIEWER_PASSWORD"),
  );
  await page.getByTestId("nav-dashboard").click();
  for (const s of sections) {
    await expect(page.getByTestId(`dashboard-section-${s}`)).toBeVisible();
  }
  for (const r of ["15m", "1h", "6h", "24h", "7d"]) {
    await page.getByTestId("dashboard-range").click();
    await page.getByTestId(`dashboard-range-${r}`).click();
    await expect(page).toHaveURL(new RegExp(`range=${r}`));
    await expect(
      page
        .getByTestId("dashboard-section-traffic")
        .locator(".recharts-surface")
        .first(),
    ).toBeVisible();
  }
  await page.getByTestId("dashboard-range").click();
  await page.getByTestId("dashboard-range-15m").click();
  await expect(page.getByTestId("dashboard-section-top")).toContainText(
    env("NEXORA_E2E_QUERY_NAME").split(".")[0],
  );
  await expect(
    page
      .getByTestId("dashboard-fleet-row-gui-engine")
      .or(page.getByTestId("dashboard-fleet-row-gui-engine-2"))
      .first(),
  ).toBeVisible();
  await expect(page.getByTestId("dashboard-alert").first()).toBeVisible();
  await page.setViewportSize({ width: 400, height: 900 });
  for (const s of sections) {
    await expect(page.getByTestId(`dashboard-section-${s}`)).toBeVisible();
  }
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  );
  expect(overflow).toBe(false);
});
