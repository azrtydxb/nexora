import { test, expect, env, login } from "../fixtures";

test("forwarding and recursion rename, redirect and the collapsible Filtering group", async ({
  page,
}) => {
  await login(
    page,
    env("NEXORA_E2E_VIEWER_USER"),
    env("NEXORA_E2E_VIEWER_PASSWORD"),
  );
  await expect(page.getByTestId("nav-resolution")).toHaveText(
    /Forwarding & recursion/,
  );
  await page.getByTestId("nav-resolution").click();
  await expect(page).toHaveURL(/\/resolution$/);
  await expect(page.getByRole("heading", { level: 1 })).toHaveText(
    "Forwarding & recursion",
  );
  await expect(page).toHaveTitle("Forwarding & recursion · Nexora");
  for (const h of ["Resolution mode", "Forward zones", "Upstream forwarders"]) {
    await expect(
      page.getByRole("heading", { name: h, exact: true }),
    ).toBeVisible();
  }
  await page.goto("/upstreams");
  await expect(page).toHaveURL(/\/resolution$/);

  const parent = page.getByTestId("nav-filtering-group");
  await expect(parent).toHaveAttribute("aria-expanded", "true");
  for (const id of [
    "nav-filtering",
    "nav-filter-categories",
    "nav-policies",
    "nav-rpz",
  ]) {
    await expect(page.getByTestId(id)).toBeVisible();
  }
  await expect(page.getByTestId("nav-filtering")).toHaveText(
    /Blocklist \/ allowlist/,
  );
  await parent.click();
  await expect(parent).toHaveAttribute("aria-expanded", "false");
  await expect(page.getByTestId("nav-rpz")).toBeHidden();
  await page.reload();
  await expect(page.getByTestId("nav-filtering-group")).toHaveAttribute(
    "aria-expanded",
    "false",
  );
  await page.goto("/rpz");
  await expect(page.getByTestId("nav-filtering-group")).toHaveAttribute(
    "aria-expanded",
    "true",
  );
  await expect(page.getByTestId("nav-rpz")).toHaveAttribute(
    "aria-current",
    "page",
  );
  await page.getByTestId("nav-filtering").click();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText(
    "Blocklist / allowlist",
  );
  await expect(page).toHaveTitle("Blocklist / allowlist · Nexora");

  await page.setViewportSize({ width: 400, height: 800 });
  await page.goto("/");
  await page.getByTestId("nav-filtering-group").scrollIntoViewIfNeeded();
  if (
    (await page
      .getByTestId("nav-filtering-group")
      .getAttribute("aria-expanded")) === "false"
  ) {
    await page.getByTestId("nav-filtering-group").click();
  }
  await page.getByTestId("nav-policies").scrollIntoViewIfNeeded();
  await page.getByTestId("nav-policies").click();
  await expect(page).toHaveURL(/\/policies$/);
  await page.evaluate(() => localStorage.removeItem("nexora-nav-filtering"));
});
