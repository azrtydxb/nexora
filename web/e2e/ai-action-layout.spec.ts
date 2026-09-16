import { test, expect } from "./fixtures";

// Isolate layout from model availability and never start real background agents.
for (const width of [400, 900, 1280]) {
  for (const theme of ["light", "dark"]) {
    test(`agent action layout at ${width}px in ${theme}`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 });
      await page.route("**/api/v1/**", async (route) => {
        const path = new URL(route.request().url()).pathname;
        if (path === "/api/v1/auth/me") {
          await route.fulfill({
            json: {
              id: "layout-operator",
              username: "operator",
              display_name: "Layout operator",
              email: "operator@example.test",
              source: "local",
              role: "operator",
              revision: 1,
              preferences: { theme },
            },
          });
        } else if (path === "/api/v1/ai/status") {
          await route.fulfill({
            json: {
              enabled: true,
              reason: "",
              model: "layout-fixture",
              endpoint_host: "127.0.0.1",
              structured_output: "json_schema",
              budget: {
                day: "2026-09-16",
                used_tokens: 0,
                limit_tokens: 1000,
                background_limit_tokens: 800,
              },
              agents: ["querylog_anomalies", "capacity_forecast"].map(
                (name) => ({
                  name,
                  enabled: true,
                  running: false,
                  interval_seconds: 300,
                  last_outcome: "succeeded",
                }),
              ),
              features: {},
              mcp: { enabled: false, read_only: true },
            },
          });
        } else if (path.endsWith("/capacity_forecast/run")) {
          expect(route.request().method()).toBe("POST");
          await route.fulfill({ status: 202, body: "" });
        } else if (path === "/api/v1/version") {
          await route.fulfill({
            json: {
              version: "layout-fixture",
              commit: "",
              build_date: "",
              repository_url: "",
              engines: [],
            },
          });
        } else if (path === "/api/v1/health") {
          await route.fulfill({ json: { status: "ok" } });
        } else {
          await route.abort();
        }
      });
      await page.goto("/ai");
      if (theme === "dark") {
        await expect(page.locator("html")).toHaveClass(/dark/);
      } else {
        await expect(page.locator("html")).not.toHaveClass(/dark/);
      }
      const buttons = page.getByTestId("ai-agent-run");
      await expect(buttons).toHaveCount(2);
      for (const button of await buttons.all()) {
        await expect(button).toBeVisible();
        const geometry = await button.evaluate((element) => {
          const text = element.querySelector("span")!;
          const range = document.createRange();
          range.selectNodeContents(text);
          return {
            lines: range.getClientRects().length,
            height: element.getBoundingClientRect().height,
            iconWidth: element.querySelector("svg")!.getBoundingClientRect()
              .width,
          };
        });
        expect(geometry.lines).toBe(1);
        expect(geometry.height).toBeLessThanOrEqual(36);
        expect(geometry.iconWidth).toBeGreaterThanOrEqual(14);
      }
      const row = page.getByTestId("ai-agent-capacity_forecast");
      const button = row.getByTestId("ai-agent-run");
      const before = await button.boundingBox();
      await button.click();
      await expect(row.getByRole("status")).toHaveText("Requested");
      const after = await button.boundingBox();
      expect(after?.width).toBe(before?.width);
      expect(after?.height).toBe(before?.height);
    });
  }
}
