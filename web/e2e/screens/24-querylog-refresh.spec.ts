import { test, expect, env, login } from "../fixtures";

const isQueryLogRequest = (url: string) => url.includes("/api/v1/query-log");

test("query log refresh button and live mode reload the newest page", async ({
  page,
}) => {
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.getByTestId("nav-query-log").click();
  await expect(page.getByTestId("querylog-updated")).toBeVisible();

  // Refresh reloads without any filter change.
  const byButton = page.waitForRequest((r) => isQueryLogRequest(r.url()));
  await page.getByTestId("querylog-refresh").click();
  await byButton;

  // Live mode (on by default) reloads by itself within the 5 s interval.
  await expect(page.getByTestId("querylog-live")).toBeChecked();
  await page.waitForRequest((r) => isQueryLogRequest(r.url()), {
    timeout: 8000,
  });

  // With live mode off, no request arrives in the same window.
  await page.getByTestId("querylog-live").uncheck();
  let reloaded = false;
  page.on("request", (r) => {
    if (isQueryLogRequest(r.url())) reloaded = true;
  });
  await page.waitForTimeout(7000);
  expect(reloaded).toBe(false);
});
