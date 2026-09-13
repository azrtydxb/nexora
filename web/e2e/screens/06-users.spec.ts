import { test, expect, env, login } from "../fixtures";

test("users create, edit, delete", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-users").click();
  await page.getByTestId("user-add").click();
  await page.getByTestId("user-username").fill("gui-user");
  await page.getByTestId("user-email").fill("gui@example.test");
  await page.getByTestId("user-password").fill("gui-user-password-1");
  await page.getByTestId("user-save").click();
  await expect(page.getByTestId("user-row-gui-user")).toContainText("viewer");
  await page.getByTestId("user-edit-gui-user").click();
  await page.getByTestId("user-role").click();
  await page.getByRole("option", { name: "operator" }).click();
  await page.getByTestId("user-save").click();
  await expect(page.getByTestId("user-row-gui-user")).toContainText("operator");
  await page.getByTestId("user-delete-gui-user").click();
  await page.getByTestId("confirm-delete").click();
  await expect(page.getByTestId("user-row-gui-user")).toHaveCount(0);
});
