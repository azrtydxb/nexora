import { test, expect, env, login } from "../fixtures";

test("upstreams create, edit, delete", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-resolution").click();
  await page.getByTestId("upstream-add").click();
  await page.getByTestId("upstream-name").fill("gui-doh");
  await page.getByTestId("upstream-protocol").click();
  await page.getByRole("option", { name: "doh" }).click();
  await page
    .getByTestId("upstream-doh-url")
    .fill("https://dns.example.test/dns-query");
  await page.getByTestId("upstream-save").click();
  await expect(page.getByTestId("upstream-row-gui-doh")).toContainText(
    "https://dns.example.test/dns-query",
  );
  await page.getByTestId("upstream-edit-gui-doh").click();
  await page.getByTestId("upstream-timeout").fill("900");
  await page.getByTestId("upstream-save").click();
  await expect(page.getByTestId("upstream-row-gui-doh")).toContainText("900");
  await page.getByTestId("upstream-delete-gui-doh").click();
  await page.getByTestId("confirm-delete").click();
  await expect(page.getByTestId("upstream-row-gui-doh")).toHaveCount(0);
});
