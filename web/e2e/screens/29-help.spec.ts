import { test, expect, env, login } from "../fixtures";

// One sample per page group: [page, help id, test id of the button that opens the dialog holding
// the control when it is not on the page itself].
const samples: [string, string, string?][] = [
  ["/settings", "settings-strategy"],
  ["/resolution", "upstreams-col-position"],
  ["/access-control", "authacl-input"],
  ["/filtering", "allowlist-input"],
  ["/zones", "zones-col-serial"],
  ["/engines", "engines-col-status"],
  ["/query-log", "querylog-name"],
  ["/users", "user-username", "user-add"],
  ["/ai/forecasts", "ai-forecasts-kind"],
];

// Catches a help tip that opens only on hover (keyboard users get nothing), a tip not tied to its
// text for screen readers, a "Learn more" link that misses its topic anchor, a help page that
// overflows a phone screen in either theme, and a topic missing from the help index.
test("help tips open on hover and focus and link to help pages", async ({
  page,
}) => {
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  for (const [path, id, opener] of samples) {
    await page.goto(path);
    if (opener) await page.getByTestId(opener).first().click();
    const tip = page.getByTestId(`help-${id}`).first();
    await expect(tip, `${path} ${id}`).toBeVisible();
    await tip.hover();
    await expect(page.getByRole("tooltip")).toBeVisible();
    // Several steps: the tooltip closes once the pointer moves outside its hover grace area.
    await page.mouse.move(0, 0, { steps: 10 });
    await expect(page.getByRole("tooltip")).toHaveCount(0);
    // A dialog may already have focused the tip on open; focus must arrive for the check to mean anything.
    await tip.blur();
    await tip.focus();
    await expect(page.getByRole("tooltip")).toBeVisible();
    await expect(tip).toHaveAttribute("aria-describedby", `help-text-${id}`);
    await page.keyboard.press("Escape");
  }

  await page.goto("/settings");
  await page.getByTestId("help-settings-strategy").hover();
  await page
    .getByRole("tooltip")
    .getByRole("link", { name: "Learn more" })
    .click();
  await expect(page).toHaveURL(/\/help\/resolution#strategy$/);
  await expect(page.locator("#strategy")).toBeInViewport();

  await page.setViewportSize({ width: 400, height: 800 });
  for (const theme of ["light", "dark"]) {
    await page.goto("/help/filtering#allowlist");
    await page.evaluate((t) => {
      document.documentElement.classList.toggle("dark", t === "dark");
    }, theme);
    if (theme === "dark")
      await expect(page.locator("html")).toHaveClass(/\bdark\b/);
    else await expect(page.locator("html")).not.toHaveClass(/\bdark\b/);
    await expect(
      page.getByRole("heading", { name: /Allowlist/i }),
    ).toBeVisible();
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    );
    expect(overflow, theme).toBe(false);
  }

  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto("/");
  await page.getByTestId("nav-help").click();
  await expect(page).toHaveURL(/\/help$/);
  const index = page.getByTestId("help-topics");
  for (const t of [
    "Forwarding & recursion",
    "DNSSEC",
    "Filtering",
    "Authoritative zones",
    "Fleet",
    "Access control",
    "Users and API tokens",
    "Query log and observability",
    "AI",
  ]) {
    await expect(
      index.getByRole("link", { name: t, exact: true }),
    ).toBeVisible();
  }
});
