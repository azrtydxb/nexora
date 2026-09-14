import { test, expect, env, login } from "../fixtures";

test("category in the query log and filter index in engine detail", async ({
  page,
}) => {
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );

  await page.getByTestId("nav-query-log").click();
  const blocked = env("NEXORA_E2E_CATEGORY_QUERY_NAME");
  await page.getByTestId("querylog-name").fill(blocked);
  await page.getByTestId("querylog-category").click();
  await page.getByRole("option", { name: "malware", exact: true }).click();
  await page.getByTestId("querylog-search").click();
  const record = page
    .getByTestId("querylog-row")
    .filter({ hasText: blocked })
    .first();
  await expect(record).toContainText("malware");
  await expect(record).toContainText("blocked");

  await page.getByTestId("nav-engines").click();
  await page.getByTestId("engine-open-gui-engine").click();
  const index = page.getByTestId("engine-filter-index");
  await expect(index).toContainText("Filter index memory");
  await expect(index).toContainText(/\d+(\.\d+)? (KiB|MiB|GiB)/);
});
