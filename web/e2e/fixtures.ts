import { test as base, expect, type Page } from "@playwright/test";
import { appendFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";

const coverageDir = process.env.NEXORA_E2E_COVERAGE_DIR;

export const test = base.extend<{ page: Page }>({
  page: async ({ page }, use, testInfo) => {
    if (coverageDir) {
      mkdirSync(coverageDir, { recursive: true });
      const file = join(coverageDir, `requests-${testInfo.workerIndex}.jsonl`);
      page.on("request", (req) => {
        const url = new URL(req.url());
        if (url.pathname.startsWith("/api/v1/")) {
          appendFileSync(
            file,
            JSON.stringify({
              method: req.method(),
              path: url.pathname.slice("/api/v1".length),
              test: testInfo.titlePath.join(" > "),
            }) + "\n",
          );
        }
      });
    }
    await use(page);
  },
});

export { expect };

export function env(name: string): string {
  const v = process.env[name];
  if (!v) throw new Error(`missing environment variable ${name}`);
  return v;
}

export async function login(page: Page, username: string, password: string) {
  await page.goto("/login");
  await page.getByTestId("login-username").fill(username);
  await page.getByTestId("login-password").fill(password);
  await page.getByTestId("login-submit").click();
  await expect(page.getByTestId("user-menu")).toContainText(username);
}

export async function logout(page: Page) {
  await page.getByTestId("user-menu").click();
  await page.getByTestId("logout").click();
  await expect(page).toHaveURL(/\/login/);
}
