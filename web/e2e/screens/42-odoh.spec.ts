import { saveM8, expectNoPageOverflow } from "../m8";
import { test, expect, env, login } from "../fixtures";

for (const width of [1280, 400]) {
  test(`Oblivious DoH settings at ${width}px`, async ({ page }) => {
    // TestGUICoverage counts: getOdohSettings, updateOdohSettings, rotateOdohKey.
    await page.setViewportSize({ width, height: 900 });
    await login(
      page,
      env("NEXORA_E2E_ADMIN_USER"),
      env("NEXORA_E2E_ADMIN_PASSWORD"),
    );
    await page.goto("/settings");
    const section = page.getByRole("region", { name: "Oblivious DoH" });
    await section.getByTestId("odoh-target-enabled").click();
    await section.getByTestId("odoh-proxy-enabled").click();
    await section.getByRole("button", { name: "Save Oblivious DoH" }).click();
    await expect(
      section.getByText("Add at least one proxy target"),
    ).toBeVisible();
    await section
      .getByTestId("odoh-targets")
      .getByLabel("Target host", { exact: true })
      .fill("odoh.example:8443");
    await saveM8(
      page,
      section.getByRole("button", { name: "Save Oblivious DoH" }),
      "PUT",
      /\/api\/v1\/odoh$/,
    );
    await expect(
      section.getByText("Oblivious DoH saved", { exact: true }),
    ).toContainText("Oblivious DoH saved");
    await expectNoPageOverflow(page);
    await page.reload();
    await expect(section.getByTestId("odoh-target-enabled")).toBeChecked();
    await expect(
      section
        .getByTestId("odoh-targets")
        .getByLabel("Target host", { exact: true }),
    ).toHaveValue("odoh.example:8443");
    const keysBefore = await page.request.get("/api/v1/odoh");
    expect(keysBefore.ok()).toBe(true);
    const oldKeyCount = (await keysBefore.json()).keys.length;
    await section.getByTestId("odoh-rotate").click();
    await saveM8(
      page,
      page
        .getByRole("dialog")
        .getByRole("button", { name: "Rotate", exact: true }),
      "POST",
      /\/api\/v1\/odoh\/rotate-key$/,
    );
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await section.getByTestId("odoh-rotate").click();
    const rotated = await saveM8(
      page,
      page
        .getByRole("dialog")
        .getByRole("button", { name: "Rotate", exact: true }),
      "POST",
      /\/api\/v1\/odoh\/rotate-key$/,
    );
    await expect(page.getByRole("dialog")).toHaveCount(0);
    const keys = (await rotated.json()).keys;
    expect(keys.length).toBe(oldKeyCount + 2);
    const rows = section.getByTestId("odoh-keys").getByRole("row");
    await expect(rows.nth(2)).toBeVisible(); // header plus at least two keys
    await expect(rows).toHaveCount(keys.length + 1);
    await section.getByTestId("odoh-proxy-enabled").click();
    await section.getByTestId("odoh-target-enabled").click();
    await section
      .getByRole("button", { name: "Remove target 1", exact: true })
      .click();
    await saveM8(
      page,
      section.getByRole("button", { name: "Save Oblivious DoH" }),
      "PUT",
      /\/api\/v1\/odoh$/,
    );
    await expect(
      section.getByText("Oblivious DoH saved", { exact: true }),
    ).toContainText("Oblivious DoH saved");
    await page.reload();
    await expect(section.getByTestId("odoh-target-enabled")).not.toBeChecked();
  });
}

test("operator can edit ODoH but cannot rotate keys", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.goto("/settings");
  const section = page.getByRole("region", { name: "Oblivious DoH" });
  await expect(section.getByTestId("odoh-target-enabled")).toBeEnabled();
  await expect(section.getByTestId("odoh-rotate")).toHaveCount(0);
  const response = await page.request.post("/api/v1/odoh/rotate-key", {
    headers: { "Content-Type": "application/json" },
  });
  expect(response.status()).toBe(403);
});

test("viewer sees ODoH settings without mutation controls", async ({
  page,
}) => {
  await login(
    page,
    env("NEXORA_E2E_VIEWER_USER"),
    env("NEXORA_E2E_VIEWER_PASSWORD"),
  );
  await page.goto("/settings");
  const section = page.getByRole("region", { name: "Oblivious DoH" });
  await expect(section.getByTestId("odoh-target-enabled")).toBeDisabled();
  await expect(
    section.getByRole("button", { name: "Save Oblivious DoH" }),
  ).toHaveCount(0);
  await expect(section.getByTestId("odoh-rotate")).toHaveCount(0);
});
