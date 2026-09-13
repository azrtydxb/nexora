import { test, expect, env, login } from "../fixtures";

const rowName = (text: string) => new RegExp(text.replaceAll(".", "\\."));

test("operator creates a zone, edits records, resolves a revision conflict, imports and exports", async ({
  page,
}) => {
  // TestGUICoverage counts the browser requests below: listZones, createZone, getZone, listZoneRecords,
  // createZoneRecord, updateZoneRecord, deleteZoneRecord, updateZone, importZoneFile, exportZoneFile.
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  const zone = `gui-${Date.now()}.test.`;
  await page.getByTestId("nav-zones").click();
  await expect(page.getByRole("heading", { name: "Zones" })).toBeVisible();

  await page.getByRole("button", { name: "New zone" }).click();
  const create = page.getByRole("dialog", { name: "New zone" });
  await create.getByLabel("Zone name").fill(zone);
  await create.getByLabel("Primary name server").fill(`ns1.${zone}`);
  await create.getByLabel("Responsible mailbox").fill(`hostmaster.${zone}`);
  await create.getByLabel("Name servers").fill(`ns1.${zone}`);
  await create.getByRole("button", { name: "Create" }).click();
  await expect(page.getByRole("heading", { name: zone })).toBeVisible();

  await page.getByRole("button", { name: "Add record" }).click();
  let editor = page.getByRole("dialog", { name: "Record" });
  await editor.getByLabel("Name").fill("www");
  await editor.getByLabel("Type").click();
  await page.getByRole("option", { name: "A", exact: true }).click();
  await editor.getByLabel("Data").fill("192.0.2.10");
  await editor.getByRole("button", { name: "Save" }).click();
  await expect(
    page.getByRole("row", { name: /www.*192\.0\.2\.10/ }),
  ).toBeVisible();

  await page
    .getByRole("row", { name: /www.*192\.0\.2\.10/ })
    .getByRole("button", { name: "Edit" })
    .click();
  editor = page.getByRole("dialog", { name: "Record" });
  // another writer changes the record behind the open editor (page.request shares the session cookie)
  const zoneId = page.url().split("/zones/")[1];
  const list = await (
    await page.request.get(
      `/api/v1/zones/${zoneId}/records?name=www.${zone}&type=A`,
    )
  ).json();
  const rec = list.items[0];
  const bump = await page.request.put(
    `/api/v1/zones/${zoneId}/records/${rec.id}`,
    {
      data: {
        name: rec.name,
        type: "A",
        ttl: 300,
        data: "192.0.2.99",
        revision: rec.revision,
      },
    },
  );
  expect(bump.status()).toBe(200);
  await editor.getByLabel("Data").fill("192.0.2.11");
  await editor.getByRole("button", { name: "Save" }).click();
  const conflict = page.getByRole("alertdialog");
  await expect(conflict).toContainText("changed by someone else");
  await expect(conflict).toContainText("192.0.2.99");
  await conflict.getByRole("button", { name: "Reload" }).click();
  await editor.getByLabel("Data").fill("192.0.2.11");
  await editor.getByRole("button", { name: "Save" }).click();
  const www = page.getByRole("row", { name: /www.*192\.0\.2\.11/ });
  await expect(www).toBeVisible();
  await www.getByRole("button", { name: "Delete" }).click();
  await page.getByTestId("confirm-delete").click();
  await expect(page.getByRole("row", { name: /www/ })).toHaveCount(0);

  await page.getByRole("tab", { name: "Transfers" }).click();
  await page.getByLabel("Allowed transfer networks").fill("192.0.2.0/24");
  await page.getByRole("button", { name: "Save settings" }).click();
  await expect(page.getByText("Settings saved")).toBeVisible();

  await page.getByRole("tab", { name: "Import/Export" }).click();
  const origin = `$ORIGIN ${zone}\n$TTL 300\n@ SOA ns1 hostmaster 5 7200 3600 1209600 300\n@ NS ns1\nns1 A 192.0.2.1\n`;
  await page.getByLabel("Zone file").fill(`${origin}bad A 999.0.0.1\n`);
  await page.getByRole("button", { name: "Import" }).click();
  await expect(page.getByRole("alert")).toContainText("line 6:");
  await page.getByLabel("Zone file").fill(`${origin}imported A 192.0.2.50\n`);
  await page.getByRole("button", { name: "Import" }).click();
  await expect(page.getByText("Imported 3 records")).toBeVisible();
  const download = page.waitForEvent("download");
  await page.getByRole("button", { name: "Export" }).click();
  expect((await download).suggestedFilename()).toBe(`${zone}zone`);

  await page.getByTestId("nav-zones").click();
  await expect(page.getByRole("row", { name: rowName(zone) })).toBeVisible();
});

test("viewer sees zones read-only", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_VIEWER_USER"),
    env("NEXORA_E2E_VIEWER_PASSWORD"),
  );
  await page.getByTestId("nav-zones").click();
  await expect(page.getByRole("heading", { name: "Zones" })).toBeVisible();
  await expect(page.getByRole("button", { name: "New zone" })).toHaveCount(0);
});
