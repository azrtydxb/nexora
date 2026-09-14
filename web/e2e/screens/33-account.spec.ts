import { test, expect, env, login, logout } from "../fixtures";

test("user edits the profile and changes the password", async ({ page }) => {
  const user = env("NEXORA_E2E_ACCOUNT_USER");
  const old = env("NEXORA_E2E_ACCOUNT_PASSWORD");
  const next = "pat-password-e2e-2";
  await login(page, user, old);
  await page.getByTestId("user-menu").click();
  await page.getByTestId("menu-profile").click();
  await expect(page).toHaveURL(/\/account$/);
  await expect(page.getByTestId("account-page")).toContainText("viewer");
  await page.getByTestId("account-email").fill("pat.changed@example.test");
  await page.getByTestId("account-time-zone").fill("Europe/Brussels");
  await page.getByTestId("account-clock-24h").click();
  await page.getByTestId("account-save").click();
  await expect(page.getByText("Profile saved")).toBeVisible();
  await page.reload();
  await expect(page.getByTestId("account-email")).toHaveValue(
    "pat.changed@example.test",
  );
  await expect(page.getByTestId("account-time-zone")).toHaveValue(
    "Europe/Brussels",
  );

  await page.getByTestId("user-menu").click();
  await page.getByTestId("menu-change-password").click();
  await page.getByTestId("password-current").fill("definitely-wrong-1");
  await page.getByTestId("password-new").fill(next);
  await page.getByTestId("password-confirm").fill(next);
  await page.getByTestId("password-save").click();
  await expect(page.getByTestId("password-error")).toContainText(
    "Current password is wrong",
  );
  await page.getByTestId("password-current").fill(old);
  await page.getByTestId("password-save").click();
  await expect(page.getByText("Password changed")).toBeVisible();
  await logout(page);
  await login(page, user, next);
  await logout(page);
});

test("OIDC users cannot change their password here", async ({ page }) => {
  await page.goto("/login");
  await page.getByTestId("login-oidc").click();
  await page
    .getByRole("button", { name: `Sign in as ${env("NEXORA_E2E_OIDC_USER")}` })
    .click();
  await expect(page.getByTestId("user-menu")).toContainText(
    env("NEXORA_E2E_OIDC_USER"),
  );
  await page.getByTestId("user-menu").click();
  await expect(page.getByTestId("menu-profile")).toBeVisible();
  await expect(page.getByTestId("menu-change-password")).toHaveCount(0);
  await page.getByTestId("menu-profile").click();
  await expect(page.getByTestId("account-idp-note")).toContainText(
    "Managed by your identity provider",
  );
  await expect(page.getByTestId("account-email")).toBeDisabled();
});
