import { expectNoPageOverflow } from "../m8";
import { test, expect, env, login } from "../fixtures";

for (const width of [1280, 400]) {
  test(`catalog zones page at ${width}px`, async ({ page }) => {
    // TestGUICoverage counts: listCatalogZones, createCatalogZone, getCatalogZone, deleteCatalogZone.
    await page.setViewportSize({ width, height: 900 });
    await login(
      page,
      env("NEXORA_E2E_OPERATOR_USER"),
      env("NEXORA_E2E_OPERATOR_PASSWORD"),
    );
    await page.goto("/zones");
    await page.getByTestId("nav-catalog-zones").click();
    await expect(page).toHaveURL(/\/zones\/catalogs$/);
    await expect(
      page.getByRole("heading", { name: "Catalog zones" }),
    ).toBeVisible();

    await expectNoPageOverflow(page);
    const suffix = `${width}-${Date.now()}`;
    await page.getByTestId("catalog-create").click();
    let dialog = page.getByRole("dialog", { name: "New catalog zone" });
    await dialog.getByRole("button", { name: "Create", exact: true }).click();
    await expect(dialog.getByRole("alert")).toContainText(
      "at least one transfer CIDR",
    );
    await dialog
      .getByLabel("Catalog zone name", { exact: true })
      .fill(`prod-${suffix}.test.`);
    await dialog.getByLabel("Role", { exact: true }).click();
    await page.getByRole("option", { name: "Producer" }).click();
    await dialog
      .getByLabel("Transfer allowed from", { exact: true })
      .fill("127.0.0.1/32");
    await dialog.getByRole("button", { name: "Create" }).click();
    await expect(
      page.getByRole("row", { name: new RegExp(`prod-${suffix}`) }),
    ).toBeVisible();

    await page.getByTestId("catalog-create").click();
    dialog = page.getByRole("dialog", { name: "New catalog zone" });
    await dialog
      .getByLabel("Catalog zone name", { exact: true })
      .fill(`cons-${suffix}.test.`);
    await dialog.getByLabel("Role", { exact: true }).click();
    await page.getByRole("option", { name: "Consumer" }).click();
    await dialog
      .getByLabel("Primary address", { exact: true })
      .fill("127.0.0.1:9");
    await dialog.getByRole("button", { name: "Create" }).click();
    await expect(
      page.getByRole("row", { name: new RegExp(`cons-${suffix}`) }),
    ).toBeVisible();

    await page.goto(
      `/zones/catalogs?catalog=${env("NEXORA_E2E_SEEDED_CATALOG_ID")}`,
    );
    await expect(page.getByTestId("catalog-members")).toContainText(
      "gui-member.test.",
    );
    await page.goto(
      `/zones/catalogs?catalog=${env("NEXORA_E2E_CLASH_CATALOG_ID")}`,
    );
    await expect(page.getByTestId("catalog-members")).toContainText("Clash");

    for (const name of [`prod-${suffix}`, `cons-${suffix}`]) {
      await page.goto("/zones/catalogs");
      await page
        .getByRole("row", { name: new RegExp(name) })
        .getByRole("button", { name: "Delete" })
        .click();
      await page
        .getByRole("dialog")
        .getByRole("button", { name: "Delete" })
        .click();
      await expect(
        page.getByRole("row", { name: new RegExp(name) }),
      ).toHaveCount(0);
    }
  });
}

test("viewer can inspect catalogs but cannot create or delete", async ({
  page,
}) => {
  await login(
    page,
    env("NEXORA_E2E_VIEWER_USER"),
    env("NEXORA_E2E_VIEWER_PASSWORD"),
  );
  await page.goto(
    `/zones/catalogs?catalog=${env("NEXORA_E2E_SEEDED_CATALOG_ID")}`,
  );
  await expect(page.getByTestId("catalog-members")).toContainText(
    "gui-member.test.",
  );
  await expect(page.getByTestId("catalog-create")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Delete", exact: true }),
  ).toHaveCount(0);
  await expect(page).toHaveTitle("Catalog zones · Nexora");
});
