import { test, expect, env, login } from "../fixtures";

const ZONE = `$TTL 60
@ SOA ns.rpz. hostmaster.rpz. 7 60 60 86400 60
@ NS ns.rpz.
bad.example CNAME .
local.example A 10.9.9.9
`;

const rowName = (name: string) => new RegExp(name.replaceAll(".", "\\."));

test("operator manages RPZ zones: file upload, transfer zone, order, refresh, delete", async ({
  page,
}) => {
  // TestGUICoverage counts the browser requests below: listRpzZones, createRpzZone, uploadRpzZoneFile,
  // getRpzZone (Edit), updateRpzZone, reorderRpzZones, refreshRpzZone, deleteRpzZone.
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.getByTestId("nav-rpz").click();
  await expect(
    page.getByRole("heading", { name: "Response policy zones" }),
  ).toBeVisible();
  const suffix = Date.now();
  const fileName = `rpz-file-${suffix}.test.`;
  const axfrName = `rpz-axfr-${suffix}.test.`;

  await page.getByRole("button", { name: "New zone" }).click();
  let dialog = page.getByRole("dialog", { name: "New RPZ zone" });
  await dialog.getByLabel("Zone name", { exact: true }).fill(fileName);
  await dialog.getByLabel("Source", { exact: true }).click();
  await page.getByRole("option", { name: "File", exact: true }).click();
  await dialog.getByRole("button", { name: "Save" }).click();
  const fileRow = page.getByRole("row", { name: rowName(fileName) });
  await expect(fileRow).toContainText("no file");

  await fileRow
    .getByRole("button", { name: `Upload file for ${fileName}` })
    .click();
  const upload = page.getByRole("dialog", { name: "Upload zone file" });
  await upload.getByLabel("Zone file", { exact: true }).setInputFiles({
    name: "bad.zone",
    mimeType: "text/plain",
    buffer: Buffer.from("$INCLUDE /etc/passwd\n"),
  });
  await upload.getByRole("button", { name: "Upload" }).click();
  await expect(upload.getByRole("alert")).toContainText(
    "$INCLUDE is not allowed",
  );
  await upload.getByLabel("Zone file", { exact: true }).setInputFiles({
    name: "rpz.zone",
    mimeType: "text/plain",
    buffer: Buffer.from(ZONE),
  });
  await upload.getByRole("button", { name: "Upload" }).click();
  await expect(fileRow).toContainText("2 records");

  await page.getByRole("button", { name: "New zone" }).click();
  dialog = page.getByRole("dialog", { name: "New RPZ zone" });
  await dialog.getByLabel("Zone name", { exact: true }).fill(axfrName);
  await dialog.getByLabel("Source", { exact: true }).click();
  await page
    .getByRole("option", { name: "Zone transfer", exact: true })
    .click();
  await dialog.getByLabel("Primary", { exact: true }).fill("127.0.0.1:5300");
  await dialog.getByLabel("TSIG algorithm", { exact: true }).click();
  await page.getByRole("option", { name: "hmac-sha256", exact: true }).click();
  await dialog.getByLabel("TSIG key name", { exact: true }).fill("rpz-key.");
  await dialog
    .getByLabel("TSIG secret (base64)", { exact: true })
    .fill(btoa("fixture-tsig-key"));
  // TestGUICoverage's management plane has key storage since M4, so the TSIG secret is sealed and
  // saved; the 503 path without NEXORA_KEK_FILE stays covered by mgmt/internal/api/resolution_test.go.
  await dialog.getByLabel("Policy override", { exact: true }).click();
  await page.getByRole("option", { name: "NXDOMAIN", exact: true }).click();
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog).toBeHidden();
  const axfrRow = page.getByRole("row", { name: rowName(axfrName) });
  await expect(axfrRow).toContainText("127.0.0.1:5300");

  const ours = page.getByRole("row", {
    name: new RegExp(`rpz-(file|axfr)-${suffix}`),
  });
  await expect(ours.first()).toContainText(fileName);
  await axfrRow.getByRole("button", { name: `Move ${axfrName} up` }).click();
  await expect(ours.first()).toContainText(axfrName);

  await axfrRow.getByRole("button", { name: `Edit ${axfrName}` }).click();
  const edit = page.getByRole("dialog", { name: "Edit RPZ zone" });
  await expect(edit.getByLabel("Primary", { exact: true })).toHaveValue(
    "127.0.0.1:5300",
  );
  await edit
    .getByLabel("Minimum refresh (seconds)", { exact: true })
    .fill("120");
  await edit.getByRole("button", { name: "Save" }).click();
  await expect(edit).toBeHidden();
  await expect(axfrRow).toContainText("120 s");

  await axfrRow.getByRole("button", { name: `Refresh ${axfrName}` }).click();
  await expect(page.getByText("Refresh requested")).toBeVisible();

  await fileRow.getByRole("button", { name: `Delete ${fileName}` }).click();
  await page.getByTestId("confirm-delete").click();
  await expect(fileRow).toHaveCount(0);
});

test("viewer sees RPZ zones read-only", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_VIEWER_USER"),
    env("NEXORA_E2E_VIEWER_PASSWORD"),
  );
  await page.getByTestId("nav-rpz").click();
  await expect(
    page.getByRole("heading", { name: "Response policy zones" }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "New zone" })).toHaveCount(0);
});
