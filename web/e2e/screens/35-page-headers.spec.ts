import { test, expect, env, login } from "../fixtures";

const jargonFree = [
  "/",
  "/query-log",
  "/resolution",
  "/access-control",
  "/filtering",
  "/filtering/categories",
  "/policies",
  "/rewrites",
  "/rpz",
  "/dnssec",
  "/zones",
  "/zones/tsig-keys",
  "/users",
  "/api-tokens",
  "/audit",
  "/settings",
  "/account",
  "/help",
];

// Catches a page header that talks about internals ("management plane", "engine") instead of what
// the operator does, and a filtering page that lost its lead text or its links to categories and
// policies.
test("page headers use operator wording", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  for (const path of jargonFree) {
    await page.goto(path);
    const title = page.locator("main h1").first();
    await expect(title, path).toBeVisible();
    const text = (await title.locator("xpath=..").innerText()).toLowerCase();
    expect(text, path).not.toContain("management plane");
    expect(text, path).not.toMatch(/\bengines?\b/);
  }
  await page.goto("/filtering");
  await expect(
    page.getByText("Block unwanted domains by subscribing to blocklists"),
  ).toBeVisible();
  await expect(page.getByTestId("filtering-link-categories")).toBeVisible();
  await expect(page.getByTestId("filtering-link-policies")).toBeVisible();
});
