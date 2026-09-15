import { test, expect, env, login } from "../fixtures";

test("operator scopes an upstream and a rewrite to an engine group", async ({
  page,
}) => {
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  const host = `scoped-${Date.now()}.home.test`;
  await page.getByTestId("nav-rewrites").click();
  await page.getByRole("button", { name: "New rewrite" }).click();
  const dialog = page.getByRole("dialog", { name: "New rewrite" });
  await dialog.getByLabel("Name", { exact: true }).fill(host);
  await dialog.getByLabel("Type", { exact: true }).click();
  await page.getByRole("option", { name: "A", exact: true }).click();
  await dialog.getByLabel("Value", { exact: true }).fill("192.168.1.77");
  await dialog.getByTestId("rewrite-engine-group").click();
  await page.getByRole("option", { name: "gui-edge", exact: true }).click();
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("row", { name: new RegExp(host) })).toContainText(
    "gui-edge",
  );

  await page.getByTestId("nav-resolution").click();
  await expect(
    page
      .getByRole("region", { name: "Upstream forwarders", exact: true })
      .getByRole("columnheader", { name: "Engine group" }),
  ).toBeVisible();
});
