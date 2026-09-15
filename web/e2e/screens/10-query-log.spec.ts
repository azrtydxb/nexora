import { test, expect, env, login } from "../fixtures";

test("query log search", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-query-log").click();
  await page.getByTestId("querylog-name").fill(env("NEXORA_E2E_QUERY_NAME"));
  await page.getByTestId("querylog-search").click();
  await expect(
    page
      .getByTestId("querylog-row")
      .filter({ hasText: env("NEXORA_E2E_QUERY_NAME") })
      .first(),
  ).toBeVisible();

  const full = env("NEXORA_E2E_QUERY_NAME");
  const fragment = full.slice(2, 10).toUpperCase();
  await page.getByTestId("querylog-name").fill(fragment);
  await expect(page.getByTestId("querylog-name")).toHaveAttribute(
    "placeholder",
    /part of a name/i,
  );
  await page.getByTestId("querylog-search").click();
  await expect(
    page.getByTestId("querylog-row").filter({ hasText: full }).first(),
  ).toBeVisible();
});
