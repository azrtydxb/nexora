import { test, expect, env, login } from "../fixtures";

test("operator selects the parallel strategy with a cap", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-strategy").click();
  await page.getByTestId("settings-strategy-parallel").click();
  await expect(page.getByTestId("settings-parallel-warning")).toContainText(
    "every upstream",
  );
  await expect(page.getByTestId("settings-parallel-warning")).toContainText(
    "privacy",
  );
  await page.getByTestId("settings-parallel-max").fill("9");
  await page.getByTestId("settings-save").click();
  await expect(page.getByText(/between 0 and 8/)).toBeVisible();
  await page.getByTestId("settings-parallel-max").fill("2");
  await page.getByTestId("settings-save").click();
  await expect(page.getByText("Settings saved")).toBeVisible();
  await page.reload();
  await expect(page.getByTestId("settings-strategy")).toContainText("Parallel");
  await expect(page.getByTestId("settings-parallel-max")).toHaveValue("2");
  await page.getByTestId("settings-strategy").click();
  await page.getByRole("option", { name: /Ordered/ }).click();
  await page.getByTestId("settings-save").click();
  await expect(page.getByText("Settings saved")).toBeVisible();
});
