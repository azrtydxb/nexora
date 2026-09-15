import { test, expect, env, login } from "../fixtures";

test("operator edits resolution settings and forward zones", async ({
  page,
}) => {
  // covers getResolutionSettings, updateResolutionSettings, listForwardZones, createForwardZone,
  // updateForwardZone, deleteForwardZone
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.getByTestId("nav-resolution").click();

  const card = page.getByRole("region", { name: "Resolution mode" });
  await expect(card.getByLabel("Mode", { exact: true })).toContainText(
    "Forward",
  );
  await card
    .getByLabel("Maximum upstream queries per client query", { exact: true })
    .fill("150");
  await card.getByRole("button", { name: "Add root hint" }).click();
  await card.getByLabel("Root hint name 1").fill("a.root.test.");
  await card.getByLabel("Root hint addresses 1").fill("127.0.53.1:53");
  await card.getByRole("button", { name: "Save resolution settings" }).click();
  await expect(card.getByRole("alert")).toContainText("not an IP address");
  await card.getByLabel("Root hint addresses 1").fill("127.0.53.1");
  await card.getByRole("button", { name: "Save resolution settings" }).click();
  await expect(card.getByText("Resolution settings saved")).toBeVisible();
  await page.reload();
  await expect(
    page
      .getByRole("region", { name: "Resolution mode" })
      .getByLabel("Maximum upstream queries per client query", { exact: true }),
  ).toHaveValue("150");

  const zones = page.getByRole("region", { name: "Forward zones" });
  const domain = `corp-${Date.now()}.example`;
  await zones.getByRole("button", { name: "New forward zone" }).click();
  let dialog = page.getByRole("dialog", { name: "New forward zone" });
  await dialog.getByLabel("Domain", { exact: true }).fill(domain);
  await dialog.getByLabel("Servers", { exact: true }).fill("10.0.0.1");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog.getByRole("alert")).toContainText("not ip:port");
  await dialog
    .getByLabel("Servers", { exact: true })
    .fill("10.0.0.1:53, 10.0.0.2:53");
  await dialog.getByRole("button", { name: "Save" }).click();
  const row = zones.getByRole("row", {
    name: new RegExp(domain.replaceAll(".", "\\.")),
  });
  await expect(row).toContainText("10.0.0.2:53");

  await row.getByRole("button", { name: `Edit ${domain}.` }).click();
  dialog = page.getByRole("dialog", { name: "Edit forward zone" });
  await dialog.getByRole("switch", { name: "Validate DNSSEC" }).click();
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(row).toContainText("validated");

  await row.getByRole("button", { name: `Delete ${domain}.` }).click();
  await page.getByTestId("confirm-delete").click();
  await expect(row).toHaveCount(0);
});
