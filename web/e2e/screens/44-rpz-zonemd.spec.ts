import { test, expect, env, login } from "../fixtures";
import { saveM8, expectNoPageOverflow } from "../m8";

for (const width of [1280, 400]) {
  test(`RPZ ZONEMD verification at ${width}px`, async ({ page }) => {
    // Real API persistence and seeded engine report; no routed/mocked responses.
    await page.setViewportSize({ width, height: 900 });
    await login(
      page,
      env("NEXORA_E2E_OPERATOR_USER"),
      env("NEXORA_E2E_OPERATOR_PASSWORD"),
    );
    await page.goto("/rpz");
    const row = page.getByRole("row").filter({
      has: page.getByRole("cell", {
        name: env("NEXORA_E2E_RPZ_ZONEMD_NAME"),
        exact: true,
      }),
    });
    await row.getByRole("button", { name: /^Edit / }).click();
    let dialog = page.getByRole("dialog", { name: "Edit RPZ zone" });
    await dialog.getByTestId("rpz-zonemd-verify").click();
    await page.getByRole("option", { name: "Required", exact: true }).click();
    await saveM8(
      page,
      dialog.getByRole("button", { name: "Save", exact: true }),
      "PUT",
      /\/api\/v1\/rpz-zones\//,
    );
    await expect(dialog).toHaveCount(0);
    await page.reload();
    await row.getByRole("button", { name: /^Edit / }).click();
    dialog = page.getByRole("dialog", { name: "Edit RPZ zone" });
    await expect(dialog.getByTestId("rpz-zonemd-verify")).toContainText(
      "Required",
    );
    await dialog.getByTestId("rpz-zonemd-verify").click();
    await page.getByRole("option", { name: "If present", exact: true }).click();
    await saveM8(
      page,
      dialog.getByRole("button", { name: "Save", exact: true }),
      "PUT",
      /\/api\/v1\/rpz-zones\//,
    );
    await expect(dialog).toHaveCount(0);
    await expect(row.getByTestId("rpz-zonemd-status")).toContainText("Failed");
    await expect(row.getByTestId("rpz-zonemd-status")).toContainText(
      "digest mismatch",
    );
    await expectNoPageOverflow(page);

    // File-backed sources cannot submit a verification field, even "off".
    await page.getByRole("button", { name: "New zone", exact: true }).click();
    const create = page.getByRole("dialog", { name: "New RPZ zone" });
    await expect(create.getByLabel("Source", { exact: true })).toContainText(
      "File",
    );
    await expect(create.getByTestId("rpz-zonemd-verify")).toHaveCount(0);
    const name = `gui-file-zonemd-${width}-${Date.now()}.test.`;
    await create.getByLabel("Zone name", { exact: true }).fill(name);
    const response = await saveM8(
      page,
      create.getByRole("button", { name: "Save", exact: true }),
      "POST",
      /\/api\/v1\/rpz-zones$/,
    );
    expect(response.request().postDataJSON()).not.toHaveProperty(
      "zonemd_verify",
    );
    await expect(create).toHaveCount(0);
    const fileRow = page
      .getByRole("row")
      .filter({ has: page.getByRole("cell", { name, exact: true }) });
    await expect(fileRow).toBeVisible();
    await fileRow
      .getByRole("button", { name: `Delete ${name}`, exact: true })
      .click();
    await saveM8(
      page,
      page
        .getByRole("dialog")
        .getByRole("button", { name: "Delete", exact: true }),
      "DELETE",
      /\/api\/v1\/rpz-zones\//,
    );
    await expect(fileRow).toHaveCount(0);
  });
}

test("viewer sees RPZ failure without edit controls", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_VIEWER_USER"),
    env("NEXORA_E2E_VIEWER_PASSWORD"),
  );
  await page.goto("/rpz");
  const row = page.getByRole("row").filter({
    has: page.getByRole("cell", {
      name: env("NEXORA_E2E_RPZ_ZONEMD_NAME"),
      exact: true,
    }),
  });
  await expect(row.getByTestId("rpz-zonemd-status")).toContainText(
    "digest mismatch",
  );
  await expect(row.getByRole("button", { name: /^Edit / })).toHaveCount(0);
});
