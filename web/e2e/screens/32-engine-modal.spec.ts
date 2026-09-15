import { test, expect, env, login } from "../fixtures";

// covers getEngineMetrics, getEngineLogs
test("engine modal: open, deep link, step, tabs with data, close", async ({
  page,
}) => {
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.getByTestId("nav-engines").click();
  await page.getByTestId("engine-modal-open-gui-engine-3").click();
  const modal = page.getByTestId("engine-modal");
  await expect(modal).toContainText("gui-engine-3");
  await expect(page).toHaveURL(/[?&]engine=/);
  const url = page.url();
  await expect(page.getByTestId("engine-chart-qps")).toBeVisible();
  for (const w of ["5m", "1h", "24h"]) {
    await page.getByTestId("engine-metrics-window").click();
    await page.getByRole("option", { name: w, exact: true }).click();
    await expect(page.getByTestId("engine-chart-latency")).toBeVisible();
  }
  await page.getByTestId("engine-tab-logs").click();
  await expect(page.getByTestId("engine-log-line").first()).toBeVisible({
    timeout: 20_000,
  });
  await page.getByTestId("engine-logs-search").fill("applied version");
  await expect(page.getByTestId("engine-log-line").first()).toContainText(
    "applied version",
  );
  await page.getByTestId("engine-logs-pause").click();
  await page.getByTestId("engine-tab-queries").click();
  await expect(page.getByTestId("engine-queries-row").first()).toBeVisible();
  await page.getByTestId("engine-modal-next").click();
  await expect(modal).not.toContainText("gui-engine-3");
  await page.getByTestId("engine-modal-prev").click();
  await expect(modal).toContainText("gui-engine-3");
  await page.keyboard.press("Escape");
  await expect(modal).toBeHidden();
  await page.goto(url);
  await expect(page.getByTestId("engine-modal")).toBeVisible();
  await page.goBack();
  await expect(page.getByTestId("engine-modal")).toBeHidden();
  await page.setViewportSize({ width: 400, height: 800 });
  await page.goto(url);
  await expect(page.getByTestId("engine-tab-metrics")).toBeVisible();
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  );
  expect(overflow).toBe(false);
});

test("engine modal opens from an engine group's engine list", async ({
  page,
}) => {
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.goto("/engines/groups/00000000-0000-0000-0000-000000000001");
  await page.getByTestId("engine-modal-open-gui-engine-4").click();
  await expect(page.getByTestId("engine-modal")).toContainText("gui-engine-4");
});
