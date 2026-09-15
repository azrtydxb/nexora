import { test, expect, env, login } from "../fixtures";

test("sidebar shows the version, details and the reload hint on a mismatch", async ({
  page,
}) => {
  await login(
    page,
    env("NEXORA_E2E_VIEWER_USER"),
    env("NEXORA_E2E_VIEWER_PASSWORD"),
  );
  const footer = page.getByTestId("version-footer");
  await expect(footer).toContainText("Nexora");
  await footer.click();
  const details = page.getByTestId("version-details");
  await expect(details).toContainText("Management plane");
  await expect(details).toContainText("GUI build");
  await expect(details).toContainText("Engines");
  await expect(page.getByTestId("version-reload-hint")).toHaveCount(0);

  await page.route("**/api/v1/version", async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    await route.fulfill({
      response: res,
      json: {
        ...body,
        version: "v9.9.9",
        commit: "0123456789abcdef0123456789abcdef01234567",
      },
    });
  });
  await page.reload();
  await expect(page.getByTestId("version-reload-hint")).toContainText(
    "New version available",
  );
  await expect(page.getByTestId("version-footer")).toContainText(
    "v9.9.9 · 0123456",
  );

  await page.setViewportSize({ width: 400, height: 800 });
  await page.getByTestId("version-info-button").click();
  await expect(page.getByTestId("version-details")).toBeVisible();
});
