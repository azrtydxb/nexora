import { test, expect } from "./fixtures";
import type { Page } from "@playwright/test";

const longName = `${"a".repeat(63)}.${"b".repeat(63)}.example.test.`;
const followUp = `Review individual queries for ${longName} to verify the matching category and list before changing any policy.`;

async function mockUI(page: Page) {
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const values: Record<string, unknown> = {
      "/api/v1/auth/me": {
        id: "layout-user",
        username: "operator",
        role: "operator",
        source: "local",
        display_name: "Operator",
        email: "operator@example.test",
        revision: 1,
        preferences: {
          theme: "dark",
          clock_24h: true,
          querylog_live: false,
          time_zone: "",
        },
        created_at: "2026-09-16T12:00:00Z",
        last_login_at: null,
      },
      "/api/v1/version": {
        version: "fixture",
        engines: [],
        commit: "",
        build_date: "",
        repository_url: "",
      },
      "/api/v1/health": { status: "ok" },
      "/api/v1/ai/status": {
        enabled: true,
        features: { querylog_search: true },
      },
      "/api/v1/ai/findings": [],
      "/api/v1/filter-categories": [],
      "/api/v1/policy-groups": [],
      "/api/v1/engines": [],
      "/api/v1/ai/insights": {
        score: 10,
        insights: [],
        summary: "No active insights.",
        generated_at: null,
      },
      "/api/v1/dashboard": {
        qps: 0,
        cache_hit_ratio: 0,
        engines_connected: 2,
        engines_total: 2,
        blocked_total: 0,
        queries_total: 0,
        series: [],
        upstreams: [
          {
            name: "unused",
            total_engines: 2,
            up_engines: 0,
            unmeasured_engines: 2,
            rtt_ms: 0,
          },
          {
            name: "mixed",
            total_engines: 2,
            up_engines: 1,
            unmeasured_engines: 1,
            rtt_ms: 20,
          },
          {
            name: "down",
            total_engines: 2,
            up_engines: 0,
            unmeasured_engines: 0,
            rtt_ms: 0,
          },
        ],
      },
      "/api/v1/query-log": {
        backend: "builtin",
        next_cursor: "",
        records: [
          {
            time: "2026-09-16T12:00:00Z",
            name: longName,
            client: "2001:db8:1234:5678:abcd:ef01:2345:6789",
            qtype: "HTTPS",
            rcode: "NOERROR",
            cache: "none",
            filter: "blocked",
            source: "category",
            list_name: "A very long blocklist name with category attribution",
            rule: longName,
            category: "ads-tracking",
            duration_us: 1500,
            upstream: "forwarder.example.test",
            engine_id: "test-engine",
            policy_group_id: "",
            list_id: "",
            rpz_zone_name: "",
            rpz_action: "",
          },
        ],
      },
      "/api/v1/ai/query-log/search": { id: "layout-task", status: "queued" },
      "/api/v1/ai/tasks/layout-task": {
        id: "layout-task",
        status: "succeeded",
        result: {
          filters: {},
          summary: `**Observed traffic**\n\n- First observation\n- Second observation\n\n${longName}\n\n![untrusted](https://example.invalid/tracker.png)<script>alert(1)</script>`,
          explanation:
            "Category filtering is applied.\n\nReview the matching records.",
          suggestions: [followUp],
        },
      },
    };
    if (path in values)
      await route.fulfill({
        json: values[path],
        status: path.endsWith("/query-log/search") ? 202 : 200,
      });
    else
      await route.fulfill({
        status: 503,
        json: {
          code: "fixture_unavailable",
          message: "Not part of this layout fixture",
        },
      });
  });
}

for (const width of [400, 900, 1600]) {
  test(`query log and AI prose fit at ${width}px`, async ({
    page,
  }, testInfo) => {
    await page.setViewportSize({ width, height: 1000 });
    await mockUI(page);
    await page.goto("/query-log");
    const region = page.getByRole("region", { name: "DNS query log" });
    await expect(page.getByTestId("querylog-row")).toHaveCount(1);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(width);
    const bounds = await region.boundingBox();
    expect(bounds).not.toBeNull();
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width);
    if (width === 1600) {
      expect(
        await region.evaluate((el) => el.scrollWidth - el.clientWidth),
      ).toBeLessThanOrEqual(1);
    }
    await region.evaluate((el) => {
      el.scrollLeft = el.scrollWidth;
    });
    const duration = region.getByRole("columnheader", { name: "Duration" });
    const end = await duration.boundingBox();
    expect(end!.x).toBeGreaterThanOrEqual(bounds!.x);
    expect(end!.x + end!.width).toBeLessThanOrEqual(
      bounds!.x + bounds!.width + 1,
    );
    await region.evaluate((el) => {
      el.scrollLeft = 0;
    });
    await page.getByTestId("querylog-row-toggle").click();
    await expect(page.getByTestId("querylog-detail")).toContainText(longName);
    await page.getByTestId("querylog-ai-ask").fill("Show blocked clients");
    await page.getByTestId("querylog-ai-ask-submit").click();
    const summary = page.getByTestId("querylog-ai-summary");
    await expect(summary.locator("strong")).toHaveText("Observed traffic");
    await expect(summary.locator("li")).toHaveCount(2);
    await expect(summary.locator("img, script")).toHaveCount(0);
    const suggestion = page.getByTestId("querylog-ai-suggestion");
    await expect(suggestion).toBeVisible();
    expect(
      await suggestion.evaluate((el) => el.scrollWidth - el.clientWidth),
    ).toBeLessThanOrEqual(1);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(width);
    await suggestion.click();
    await expect(page.getByTestId("querylog-ai-ask")).toHaveValue(followUp);
    await page.screenshot({
      path: testInfo.outputPath("query-log.png"),
      fullPage: true,
    });
  });

  test(`profile fields align at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 1000 });
    await mockUI(page);
    await page.goto("/account");
    await expect(page.getByTestId("account-theme")).toBeVisible();
    const theme = await page.getByTestId("account-theme").boundingBox();
    const zone = await page.getByTestId("account-time-zone").boundingBox();
    if (width >= 640)
      expect(Math.abs(theme!.y - zone!.y)).toBeLessThanOrEqual(1);
    else expect(zone!.y).toBeGreaterThan(theme!.y + theme!.height);
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(width);
  });
}

test("health score and upstreams distinguish healthy, unmeasured and down", async ({
  page,
}) => {
  await mockUI(page);
  await page.goto("/");
  await expect(page.getByTestId("ai-score")).toHaveText("10/10");
  await expect(page.getByTestId("dashboard-upstream-unused")).toContainText(
    "Not measured",
  );
  await expect(page.getByTestId("dashboard-upstream-mixed")).toContainText(
    "1 unmeasured",
  );
  await expect(page.getByTestId("dashboard-upstream-mixed")).toContainText(
    "20.0 ms",
  );
  await expect(page.getByTestId("dashboard-upstream-down")).toContainText(
    "0 / 2 engines",
  );
  await expect(page.getByTestId("dashboard-upstream-down")).not.toContainText(
    "Not measured",
  );
});

test("missing insight data never displays a numeric health score", async ({
  page,
}) => {
  await mockUI(page);
  let release!: () => void;
  const pending = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/api/v1/ai/insights", async (route) => {
    await pending;
    await route.fulfill({
      status: 503,
      json: { code: "unavailable", message: "Unavailable" },
    });
  });
  await page.goto("/");
  try {
    await expect(page.getByTestId("ai-score")).toHaveText("Loading…");
  } finally {
    release();
  }
  await expect(page.getByTestId("ai-score")).toHaveText("Unavailable");
});
