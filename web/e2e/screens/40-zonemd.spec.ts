import { saveM8, expectNoPageOverflow } from "../m8";
import { test, expect, env, login } from "../fixtures";

for (const width of [1280, 400]) {
  test(`ZONEMD settings and catalog membership at ${width}px`, async ({
    page,
  }) => {
    // TestGUICoverage counts: getZone, updateZone, listCatalogZones.
    await page.setViewportSize({ width, height: 900 });
    await login(
      page,
      env("NEXORA_E2E_OPERATOR_USER"),
      env("NEXORA_E2E_OPERATOR_PASSWORD"),
    );
    await page.goto(`/zones/${env("NEXORA_E2E_ZONEMD_PRIMARY_ID")}`);
    const generate = page.getByTestId("zonemd-generate");
    await expect(generate).toBeVisible();
    await generate.click();
    await page.getByTestId("zone-catalog-select").click();
    await page
      .getByRole("option", { name: env("NEXORA_E2E_PRODUCER_CATALOG_NAME") })
      .click();
    await saveM8(
      page,
      page.getByRole("button", { name: "Save ZONEMD and catalog" }),
      "PATCH",
      /\/api\/v1\/zones\//,
    );
    await expect(
      page.getByText("ZONEMD and catalog saved", { exact: true }),
    ).toContainText("ZONEMD and catalog saved");
    await expectNoPageOverflow(page);
    await page.reload();
    await expect(page.getByTestId("zonemd-generate")).toBeChecked();
    await expect(page.getByTestId("zone-catalog-select")).toContainText(
      env("NEXORA_E2E_PRODUCER_CATALOG_NAME"),
    );
    await expect(page.getByTestId("zonemd-verify")).toHaveCount(0); // primaries have no verify mode

    await page.goto(`/zones/${env("NEXORA_E2E_ZONEMD_SECONDARY_ID")}`);
    await page.getByTestId("zonemd-verify").click();
    await page.getByRole("option", { name: "Required" }).click();
    await saveM8(
      page,
      page.getByRole("button", { name: "Save ZONEMD and catalog" }),
      "PATCH",
      /\/api\/v1\/zones\//,
    );
    await expect(
      page.getByText("ZONEMD and catalog saved", { exact: true }),
    ).toContainText("ZONEMD and catalog saved");
    await page.reload();
    await expect(page.getByTestId("zonemd-verify")).toContainText("Required");
    await expect(page.getByTestId("zonemd-status")).toContainText(
      "Not checked",
    );
    await expect(page.getByTestId("zonemd-generate")).toHaveCount(0);

    // Restore for the other width.
    await page.getByTestId("zonemd-verify").click();
    await page.getByRole("option", { name: "If present" }).click();
    await saveM8(
      page,
      page.getByRole("button", { name: "Save ZONEMD and catalog" }),
      "PATCH",
      /\/api\/v1\/zones\//,
    );
    await expect(
      page.getByText("ZONEMD and catalog saved", { exact: true }),
    ).toContainText("ZONEMD and catalog saved");
    await page.goto(`/zones/${env("NEXORA_E2E_ZONEMD_PRIMARY_ID")}`);
    await page.getByTestId("zonemd-generate").click();
    await page.getByTestId("zone-catalog-select").click();
    await page.getByRole("option", { name: "No catalog" }).click();
    await saveM8(
      page,
      page.getByRole("button", { name: "Save ZONEMD and catalog" }),
      "PATCH",
      /\/api\/v1\/zones\//,
    );
    await expect(
      page.getByText("ZONEMD and catalog saved", { exact: true }),
    ).toContainText("ZONEMD and catalog saved");
    await expect(page.getByTestId("zonemd-generate")).not.toBeChecked();
  });
}

test("viewer sees ZONEMD settings read-only", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_VIEWER_USER"),
    env("NEXORA_E2E_VIEWER_PASSWORD"),
  );
  await page.goto(`/zones/${env("NEXORA_E2E_ZONEMD_PRIMARY_ID")}`);
  await expect(page.getByTestId("zonemd-generate")).toBeDisabled();
  await expect(page.getByTestId("zone-catalog-select")).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "Save ZONEMD and catalog" }),
  ).toHaveCount(0);
});

test("catalog-managed members are read-only and catalog zones cannot join catalogs", async ({
  page,
}) => {
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.goto(`/zones/${env("NEXORA_E2E_MANAGED_ZONE_ID")}`);
  const card = page.getByRole("region", { name: "ZONEMD and catalog" });
  await expect(card).toContainText("Managed by catalog gui-remote.test.");
  await expect(card.getByTestId("zonemd-verify")).toBeDisabled();
  await expect(card.getByTestId("zone-catalog-select")).toHaveCount(0);
  await expect(
    card.getByRole("button", { name: "Save ZONEMD and catalog" }),
  ).toHaveCount(0);
  const response = await page.request.get(
    `/api/v1/catalog-zones/${env("NEXORA_E2E_SEEDED_CATALOG_ID")}`,
  );
  expect(response.ok()).toBe(true);
  const catalog = await response.json();
  await page.goto(`/zones/${catalog.zone_id}`);
  await expect(card).toContainText("Catalog zones cannot be members");
  await expect(card.getByTestId("zone-catalog-select")).toHaveCount(0);
});
