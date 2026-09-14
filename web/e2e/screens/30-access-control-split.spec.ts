import { test, expect, env, login } from "../fixtures";

test("operator edits recursion and authoritative access and a zone override", async ({
  page,
}) => {
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.getByTestId("nav-access-control").click();
  await expect(
    page.getByRole("heading", { name: "Recursion and resolver access" }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Authoritative query access" }),
  ).toBeVisible();
  await expect(page.getByTestId("authacl-row-0.0.0.0/0")).toBeVisible();
  await page.getByTestId("authacl-input").fill("198.51.100.0/24");
  await page.getByTestId("authacl-add").click();
  await page.getByTestId("authacl-save").click();
  await expect(page.getByText("Authoritative access saved")).toBeVisible();
  await page.reload();
  await expect(page.getByTestId("authacl-row-198.51.100.0/24")).toBeVisible();
  const zone = env("NEXORA_E2E_ACL_ZONE");
  await expect(
    page
      .getByTestId("zone-access-summary")
      .getByTestId(`zone-access-row-${zone}`),
  ).toContainText("Inherits authoritative access");

  await page
    .getByTestId("zone-access-summary")
    .getByRole("link", { name: zone })
    .click();
  await page.getByRole("tab", { name: "Transfers" }).click();
  await page.getByTestId("zone-allow-query").fill("10.0.0.0/8, 192.168.0.0/16");
  await page.getByTestId("zone-update-allow").fill("127.0.0.1/32");
  await page.getByRole("button", { name: "Save settings" }).click();
  await expect(page.getByText("Settings saved")).toBeVisible();
  await page.getByTestId("nav-access-control").click();
  await expect(page.getByTestId(`zone-access-row-${zone}`)).toContainText(
    "10.0.0.0/8, 192.168.0.0/16",
  );
  await expect(page.getByTestId(`zone-access-row-${zone}`)).toContainText(
    "127.0.0.1/32",
  );
  await page.setViewportSize({ width: 400, height: 800 });
  await expect(page.getByTestId("authacl-input")).toBeVisible();
});
