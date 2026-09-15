import { test, expect, env, login } from "../fixtures";

test("operator creates, filters, edits and deletes rewrites", async ({
  page,
}) => {
  // covers listRewrites, createRewrite, updateRewrite, deleteRewrite through browser requests
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.getByTestId("nav-rewrites").click();
  const host = `nas-${Date.now()}.home.test`;

  await page.getByRole("button", { name: "New rewrite" }).click();
  let dialog = page.getByRole("dialog", { name: "New rewrite" });
  await dialog.getByLabel("Name", { exact: true }).fill(host);
  await dialog.getByLabel("Type", { exact: true }).click();
  await page.getByRole("option", { name: "A", exact: true }).click();
  await dialog.getByLabel("Value", { exact: true }).fill("fd00::1");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(
    dialog.getByText("value fd00::1 is not an IPv4 address"),
  ).toBeVisible();
  await dialog.getByLabel("Value", { exact: true }).fill("192.168.1.50");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(
    page.getByRole("row", {
      name: new RegExp(`${host} A 192.168.1.50 300 Global`),
    }),
  ).toBeVisible();

  await page.getByRole("button", { name: "New rewrite" }).click();
  dialog = page.getByRole("dialog", { name: "New rewrite" });
  await dialog.getByLabel("Name", { exact: true }).fill(host);
  await dialog.getByLabel("Type", { exact: true }).click();
  await page.getByRole("option", { name: "CNAME", exact: true }).click();
  await dialog.getByLabel("Value", { exact: true }).fill("other.home.test");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog.getByText(/CNAME rewrite cannot coexist/)).toBeVisible();
  await dialog.getByRole("button", { name: "Cancel" }).click();

  await page.getByLabel("Scope", { exact: true }).click();
  await page.getByRole("option", { name: "Global", exact: true }).click();
  await page
    .getByRole("button", { name: `Edit ${host} A 192.168.1.50` })
    .click();
  const edit = page.getByRole("dialog", { name: "Edit rewrite" });
  await edit.getByLabel("TTL", { exact: true }).fill("120");
  await edit.getByRole("button", { name: "Save" }).click();
  await expect(
    page.getByRole("row", { name: new RegExp(`${host} A 192.168.1.50 120`) }),
  ).toBeVisible();

  await page
    .getByRole("button", { name: `Delete ${host} A 192.168.1.50` })
    .click();
  await page.getByTestId("confirm-delete").click();
  await expect(page.getByRole("row", { name: new RegExp(host) })).toHaveCount(
    0,
  );
});
