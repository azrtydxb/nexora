import { test, expect, env, logout } from "../fixtures";

test("OIDC sign-in round trip", async ({ page }) => {
  await page.goto("/login");
  await page.getByTestId("login-oidc").click();
  await page
    .getByRole("button", { name: `Sign in as ${env("NEXORA_E2E_OIDC_USER")}` })
    .click();
  await expect(page.getByTestId("user-menu")).toContainText(
    env("NEXORA_E2E_OIDC_USER"),
  );
  await logout(page);
});
