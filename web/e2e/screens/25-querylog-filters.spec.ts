import { test, expect, env, login } from "../fixtures";

test("query log multi-select filters combine and round-trip through the URL", async ({
  page,
}) => {
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  const prefix = env("NEXORA_E2E_QL_MULTI_PREFIX");
  await page.getByTestId("nav-query-log").click();
  await page.getByTestId("querylog-name").fill(prefix);
  await page.getByTestId("querylog-qtype").click();
  await page.getByTestId("querylog-qtype-option-A").click();
  await page.getByTestId("querylog-qtype-option-AAAA").click();
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("querylog-qtype")).toContainText("A, AAAA");
  await page.getByTestId("querylog-rcode").click();
  await page.getByTestId("querylog-rcode-option-NOERROR").click();
  await page.getByTestId("querylog-rcode-option-NXDOMAIN").click();
  await page.keyboard.press("Escape");
  await page.getByTestId("querylog-search").click();
  const rows = page.getByTestId("querylog-row").filter({ hasText: prefix });
  await expect(rows).toHaveCount(2);
  await expect(rows.filter({ hasText: "-mx." })).toHaveCount(0);
  await expect(page).toHaveURL(/qtype=A&qtype=AAAA/);
  await page.reload();
  await expect(page.getByTestId("querylog-qtype")).toContainText("A, AAAA");
  await expect(
    page.getByTestId("querylog-row").filter({ hasText: prefix }),
  ).toHaveCount(2);
  await page.getByTestId("querylog-qtype").click();
  await page.getByTestId("querylog-qtype-clear").click();
  await page.keyboard.press("Escape");
  await page.getByTestId("querylog-search").click();
  await expect(
    page.getByTestId("querylog-row").filter({ hasText: prefix }),
  ).toHaveCount(3);
  await expect(page).not.toHaveURL(/qtype=/);
  await page.setViewportSize({ width: 400, height: 800 });
  await expect(page.getByTestId("querylog-search")).toBeVisible();
});
