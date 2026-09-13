import { test, expect, env, login } from "./fixtures";

test("a query made against the engine appears in the query log within 10 s", async ({
  page,
}) => {
  const queryAt = Number(env("NEXORA_E2E_QUERY_AT"));
  const name = env("NEXORA_E2E_QUERY_NAME");
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-query-log").click();
  await expect(page.getByTestId("querylog-backend")).toHaveText(
    env("NEXORA_E2E_QUERYLOG_BACKEND"),
  );
  await page.getByTestId("querylog-name").fill(name);
  const row = page
    .getByTestId("querylog-row")
    .filter({ hasText: name })
    .first();
  while (Date.now() < queryAt + 10_000) {
    await page.getByTestId("querylog-search").click();
    if (await row.isVisible()) break;
    await page.waitForTimeout(500);
  }
  await expect(row).toBeVisible({ timeout: 1 });
  expect(Date.now() - queryAt).toBeLessThanOrEqual(12_000);
  await expect(row).toContainText("NOERROR");
});
