import { test, expect, env, login } from "../fixtures";

test("admin manages TSIG keys, zone signing, rollovers, a secondary refresh and zone deletion", async ({
  page,
}) => {
  // TestGUICoverage counts: listTsigKeys, createTsigKey, deleteTsigKey, getZoneDnssec, updateZoneDnssec,
  // startZoneKeyRollover, confirmZoneKskDs, refreshZone, deleteZone.
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  const suffix = Date.now();
  const keyName = `gui-key-${suffix}.`;
  await page.getByTestId("nav-zones").click();
  await page.getByRole("link", { name: "TSIG keys" }).click();
  await page.getByRole("button", { name: "New TSIG key" }).click();
  const newKey = page.getByRole("dialog", { name: "New TSIG key" });
  await newKey.getByLabel("Key name").fill(keyName);
  await newKey.getByRole("button", { name: "Create" }).click();
  await expect(newKey).toContainText("This secret is shown once");
  await newKey.getByRole("button", { name: "Done" }).click();
  const keyRow = page.getByRole("row", {
    name: new RegExp(keyName.replaceAll(".", "\\.")),
  });
  await keyRow.getByRole("button", { name: "Delete" }).click();
  await page.getByTestId("confirm-delete").click();
  await expect(keyRow).toHaveCount(0);

  const signed = `signed-${suffix}.test.`;
  await page.getByTestId("nav-zones").click();
  await page.getByRole("button", { name: "New zone" }).click();
  let create = page.getByRole("dialog", { name: "New zone" });
  await create.getByLabel("Zone name").fill(signed);
  await create.getByLabel("Primary name server").fill(`ns1.${signed}`);
  await create.getByLabel("Responsible mailbox").fill(`hostmaster.${signed}`);
  await create.getByLabel("Name servers").fill(`ns1.${signed}`);
  await create.getByRole("button", { name: "Create" }).click();
  await expect(page.getByRole("heading", { name: signed })).toBeVisible();

  await page.getByRole("tab", { name: "DNSSEC" }).click();
  await page.getByRole("button", { name: "Enable signing" }).click();
  const keys = page.getByRole("table", { name: "Signing keys" });
  await expect(keys).toContainText("ksk");
  await expect(page.getByText(/IN DS \d+ 13 2/)).toBeVisible();
  await page.getByRole("button", { name: "Roll ZSK" }).click();
  await expect(keys).toContainText("published");
  await page
    .getByRole("button", { name: "Parent DS published" })
    .first()
    .click();
  await expect(keys).toContainText("seen");
  await page.getByRole("button", { name: "Roll KSK" }).click();
  await page
    .getByRole("alertdialog")
    .getByRole("button", { name: "Confirm" })
    .click();
  await expect(keys).toContainText("pending");

  const pulled = `pulled-${suffix}.test.`;
  await page.getByTestId("nav-zones").click();
  await page.getByRole("button", { name: "New zone" }).click();
  create = page.getByRole("dialog", { name: "New zone" });
  await create.getByLabel("Kind").click();
  await page.getByRole("option", { name: "Secondary", exact: true }).click();
  await create.getByLabel("Zone name").fill(pulled);
  await create.getByLabel("Primaries").fill("127.0.0.1:1");
  await create.getByRole("button", { name: "Create" }).click();
  await page.getByRole("tab", { name: "Transfers" }).click();
  await page.getByRole("button", { name: "Refresh now" }).click();
  await expect(page.getByText("Refresh requested")).toBeVisible();
  await page.getByRole("button", { name: "Delete zone" }).click();
  await page.getByTestId("confirm-delete").click();
  await expect(page).toHaveURL(/\/zones$/);
  await expect(page.getByRole("link", { name: pulled })).toHaveCount(0);
});
