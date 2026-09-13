import { test, expect, env, login, logout } from "./fixtures";

test("local login still works while the OIDC provider is down", async ({
  page,
}) => {
  await page.goto("/login");
  await page.getByTestId("login-oidc").click();
  await expect(page.getByTestId("login-error")).toContainText(
    "identity provider unavailable",
  );
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await logout(page);
});
