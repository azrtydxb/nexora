import { test, expect, env, login } from "../fixtures";

test("query log shows the decision reason and row detail", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.getByTestId("nav-query-log").click();
  await page
    .getByTestId("querylog-name")
    .fill(env("NEXORA_E2E_CATEGORY_QUERY_NAME"));
  await page.getByTestId("querylog-search").click();
  const blocked = page.getByTestId("querylog-row").first();
  await expect(blocked.getByTestId("querylog-reason")).toContainText(
    /Category · .+ · malware/,
  );
  await blocked.getByTestId("querylog-row-toggle").click();
  await expect(page.getByTestId("querylog-detail")).toContainText(
    "malware.gui.test",
  );
  await expect(page.getByTestId("querylog-detail")).toContainText("Global");

  await page
    .getByTestId("querylog-name")
    .fill(env("NEXORA_E2E_ALLOW_QUERY_NAME"));
  await page.getByTestId("querylog-source").click();
  await page.getByTestId("querylog-source-option-allowlist").click();
  await page.keyboard.press("Escape");
  await page.getByTestId("querylog-search").click();
  await expect(
    page.getByTestId("querylog-row").first().getByTestId("querylog-reason"),
  ).toContainText("Allowlist · Global allowlist · allow.malware.gui.test");
  await expect(page).toHaveURL(/source=allowlist/);
});
