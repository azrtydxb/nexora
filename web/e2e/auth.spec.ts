import { test, expect, env, login, logout } from "./fixtures";

test("viewer cannot change config, operator can, audit shows actor and diff", async ({
  page,
}) => {
  await login(
    page,
    env("NEXORA_E2E_VIEWER_USER"),
    env("NEXORA_E2E_VIEWER_PASSWORD"),
  );
  await page.getByTestId("nav-upstreams").click();
  await expect(page.getByTestId("upstream-row-seed")).toBeVisible();
  await expect(page.getByTestId("upstream-add")).toHaveCount(0);
  await expect(page.getByTestId("upstream-edit-seed")).toHaveCount(0);
  await expect(page.getByTestId("nav-audit")).toHaveCount(0);
  const denied = await page.evaluate(async () => {
    const r = await fetch("/api/v1/upstreams", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        name: "viewer-write",
        protocol: "udp",
        address: "192.0.2.53:53",
        timeout_ms: 250,
        enabled: true,
        position: 9,
      }),
    });
    return r.status;
  });
  expect(denied).toBe(403);
  await logout(page);

  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.getByTestId("nav-upstreams").click();
  await page.getByTestId("upstream-add").click();
  await page.getByTestId("upstream-name").fill("operator-added");
  await page.getByTestId("upstream-address").fill("192.0.2.54:53");
  await page.getByTestId("upstream-save").click();
  await expect(page.getByTestId("upstream-row-operator-added")).toBeVisible();
  await page.getByTestId("upstream-edit-operator-added").click();
  await page.getByTestId("upstream-timeout").fill("400");
  await page.getByTestId("upstream-save").click();
  await expect(page.getByTestId("upstream-row-operator-added")).toContainText(
    "400",
  );
  await logout(page);

  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-audit").click();
  const row = page
    .locator('[data-testid="audit-row"][data-action="updateUpstream"]')
    .filter({ hasText: env("NEXORA_E2E_OPERATOR_USER") })
    .first();
  await expect(row).toBeVisible();
  await row.click();
  await expect(
    page.locator('[data-testid^="audit-diff-"]').first(),
  ).toContainText("400");
  await logout(page);
});

test("OIDC login works", async ({ page }) => {
  await page.goto("/login");
  await page.getByTestId("login-oidc").click();
  await page
    .getByRole("button", { name: `Sign in as ${env("NEXORA_E2E_OIDC_USER")}` })
    .click();
  await expect(page.getByTestId("user-menu")).toContainText(
    env("NEXORA_E2E_OIDC_USER"),
  );
  await page.getByTestId("nav-audit").click();
  await expect(page.getByTestId("audit-row").first()).toBeVisible();
  await logout(page);
});
