import { test, expect, env, login } from "../fixtures";

test("system users show as System without edit or delete", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.route("**/api/v1/users", async (route) => {
    const res = await route.fetch();
    const users = await res.json();
    users.push({
      id: "00000000-0000-0000-0000-00000000cafe",
      username: "nexora-operator",
      email: "",
      role: "admin",
      source: "system",
      disabled: false,
      revision: 1,
      created_at: new Date().toISOString(),
      display_name: "",
      last_login_at: null,
      preferences: {
        theme: "system",
        time_zone: "",
        clock_24h: true,
        querylog_live: false,
      },
    });
    await route.fulfill({ response: res, json: users });
  });
  await page.goto("/users");
  const row = page.getByRole("row", { name: /nexora-operator/ });
  await expect(row).toContainText("System");
  await expect(row.getByRole("button")).toHaveCount(0);
  const admin = page.getByRole("row", { name: /admin@|\badmin\b/ }).first();
  await expect(admin.getByRole("button").first()).toBeVisible();
});
