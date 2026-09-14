import { test, expect, env, login } from "../fixtures";

test("access control list edit", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-access-control").click();
  await expect(
    page.getByRole("heading", { name: "Recursion and resolver access" }),
  ).toBeVisible();
  await expect(page.getByTestId("acl-row-127.0.0.0/8")).toBeVisible();
  await page.getByTestId("acl-cidr-input").fill("198.51.100.0/24");
  await page.getByTestId("acl-add").click();
  await page.getByTestId("acl-save").click();
  await expect(page.getByText("Access control saved")).toBeVisible();
  await page.reload();
  await expect(page.getByTestId("acl-row-198.51.100.0/24")).toBeVisible();
  await page.getByTestId("acl-cidr-input").fill("not-a-cidr");
  await page.getByTestId("acl-add").click();
  await expect(page.getByText("Invalid CIDR")).toBeVisible();
});
