import { test, expect, env, login } from "../fixtures";

test("operator enables categories, toggles a source and acknowledges the OISD notice", async ({
  page,
}) => {
  // TestGUICoverage counts the browser requests below: listFilterCategories, updateFilterCategory.
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.getByTestId("nav-filter-categories").click();
  await expect(
    page.getByRole("heading", { name: "Filter categories" }),
  ).toBeVisible();

  const gambling = () => page.getByTestId("category-gambling");
  const gamblingSwitch = () =>
    gambling().getByRole("switch", { name: "Enable Gambling", exact: true });
  // Collapsed by default: the source table is hidden, the summary and switch are in the row.
  await page.evaluate(() =>
    localStorage.removeItem("nexora-categories-expanded"),
  );
  await page.reload();
  await expect(page.getByTestId("category-summary-gambling")).toContainText(
    /\d+ of \d+ sources on/,
  );
  await expect(page.getByTestId("source-row-hagezi-gambling")).toBeHidden();
  await expect(page.getByTestId("category-toggle-gambling")).toHaveAttribute(
    "aria-expanded",
    "false",
  );
  await gamblingSwitch().click(); // toggling from the collapsed row
  await expect(gamblingSwitch()).toBeChecked();
  await gamblingSwitch().click();
  await expect(gamblingSwitch()).not.toBeChecked();
  await expect(page.getByTestId("source-row-hagezi-gambling")).toBeHidden();
  await page.getByTestId("category-toggle-gambling").click();
  await expect(page.getByTestId("category-toggle-gambling")).toHaveAttribute(
    "aria-expanded",
    "true",
  );
  await expect(page.getByTestId("source-row-hagezi-gambling")).toBeVisible();

  await expect(gambling()).toContainText("GPL-3.0");
  await expect(gambling()).toContainText("HaGeZi DNS Blocklists");
  await gamblingSwitch().click();
  await expect(gamblingSwitch()).toBeChecked();
  await page.reload();
  await expect(gamblingSwitch()).toBeChecked();

  await page.getByTestId("source-toggle-blp-gambling").click();
  await expect(
    page.getByTestId("source-toggle-blp-gambling"),
  ).not.toBeChecked();
  await page.reload();
  await expect(
    page.getByTestId("source-toggle-blp-gambling"),
  ).not.toBeChecked();
  await expect(page.getByTestId("source-toggle-hagezi-gambling")).toBeChecked();

  await gamblingSwitch().click();
  await expect(gamblingSwitch()).not.toBeChecked();
  await page.reload();
  await expect(gamblingSwitch()).not.toBeChecked();

  const ads = () => page.getByTestId("category-ads-tracking");
  const adsSwitch = () =>
    ads().getByRole("switch", {
      name: "Enable Ads and tracking",
      exact: true,
    });
  await page.getByTestId("category-toggle-ads-tracking").click();
  await expect(
    ads().getByTestId("source-noncommercial-oisd-big"),
  ).toBeVisible();
  await adsSwitch().click();
  const notice = page.getByRole("dialog", { name: "License notice" });
  await expect(notice).toContainText(
    "OISD lists are not free for commercial use",
  );
  await notice.getByRole("button", { name: "Cancel" }).click();
  await expect(notice).toBeHidden();
  await expect(adsSwitch()).not.toBeChecked();
  await adsSwitch().click();
  await page
    .getByRole("dialog", { name: "License notice" })
    .getByRole("button", { name: "Acknowledge and enable" })
    .click();
  await expect(adsSwitch()).toBeChecked();
  await page.reload();
  await expect(adsSwitch()).toBeChecked();
});

test("viewer sees the catalog read-only", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_VIEWER_USER"),
    env("NEXORA_E2E_VIEWER_PASSWORD"),
  );
  await page.getByTestId("nav-filter-categories").click();
  // Source names live in the source table, which renders once the category is expanded.
  await page.getByTestId("category-toggle-malware").click();
  await expect(page.getByTestId("category-malware")).toContainText("abuse.ch");
  await expect(
    page.getByTestId("category-malware").getByRole("switch", {
      name: "Enable Malware and threat intelligence",
      exact: true,
    }),
  ).toBeDisabled();
});

test("categories in policy groups", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.getByTestId("nav-policies").click();
  const name = `cat-${Date.now()}`;
  await page.getByRole("button", { name: "New group" }).click();
  const dialog = page.getByRole("dialog", { name: "New policy group" });
  await dialog.getByLabel("Name", { exact: true }).fill(name);
  await dialog
    .getByLabel("Client CIDRs", { exact: true })
    .fill("192.168.252.0/24");
  await dialog
    .getByRole("group", { name: "Categories" })
    .getByLabel("Gambling", { exact: true })
    .check();
  await dialog.getByRole("button", { name: "Save" }).click();
  const row = page.getByRole("row", { name: new RegExp(name) });
  await expect(row).toContainText("Gambling");
  await row.getByRole("button", { name: `Edit ${name}` }).click();
  const edit = page.getByRole("dialog", { name: "Edit policy group" });
  await edit
    .getByRole("group", { name: "Categories" })
    .getByLabel("Adult content", { exact: true })
    .check();
  await edit.getByRole("button", { name: "Save" }).click();
  const notice = page.getByRole("dialog", { name: "License notice" });
  await expect(notice).toContainText(
    "OISD lists are not free for commercial use",
  );
  await notice.getByRole("button", { name: "Acknowledge and save" }).click();
  await expect(edit).toBeHidden();
  await expect(row).toContainText("Adult content");
});

test("categories expand all, collapse all, remember state, search and deep link", async ({
  page,
}) => {
  await login(
    page,
    env("NEXORA_E2E_VIEWER_USER"),
    env("NEXORA_E2E_VIEWER_PASSWORD"),
  );
  await page.goto("/filtering/categories");
  await page.getByTestId("categories-collapse-all").click();
  await expect(
    page.locator('[data-testid^="source-row-"]').first(),
  ).toBeHidden();
  await page.getByTestId("categories-expand-all").click();
  await expect(page.getByTestId("source-row-hagezi-gambling")).toBeVisible();
  await page.reload();
  await expect(page.getByTestId("source-row-hagezi-gambling")).toBeVisible();
  await page.getByTestId("categories-collapse-all").click();
  await page.reload();
  await expect(page.getByTestId("source-row-hagezi-gambling")).toBeHidden();
  await page.getByTestId("categories-search").fill("hagezi-tif");
  await expect(page.getByTestId("category-malware")).toBeVisible();
  await expect(page.getByTestId("category-gambling")).toBeHidden();
  await page.goto("/filtering/categories#category-malware");
  await expect(page.getByTestId("category-toggle-malware")).toHaveAttribute(
    "aria-expanded",
    "true",
  );
  await page.setViewportSize({ width: 400, height: 800 });
  await expect(page.getByTestId("category-toggle-malware")).toBeVisible();
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  );
  expect(overflow).toBe(false);
});
