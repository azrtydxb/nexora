import { expect, type Locator, type Page } from "@playwright/test";

/** Observe the actual write and assert its HTTP result, including failed writes. */
export async function saveM8(
  page: Page,
  button: Locator,
  method: string,
  path: RegExp,
) {
  const response = page
    .waitForResponse(
      (r) =>
        r.request().method() === method && path.test(new URL(r.url()).pathname),
    )
    .then(async (result) => {
      // Read errors as soon as the response arrives. Successful writes may navigate
      // away inside the click handler, making their response bodies unavailable.
      if (!result.ok()) {
        const detail = await result
          .text()
          .catch(
            (error: unknown) => `Response body unavailable: ${String(error)}`,
          );
        expect(
          result.ok(),
          `${method} ${result.url()}: HTTP ${result.status()} ${result.statusText()}\n${detail}`,
        ).toBeTruthy();
      }
      return result;
    });
  const [result] = await Promise.all([response, button.click()]);
  return result;
}

export async function expectNoPageOverflow(page: Page) {
  const layout = await page.evaluate(() => ({
    viewport: window.innerWidth,
    width: document.documentElement.scrollWidth,
    offenders: [...document.querySelectorAll("body *")].flatMap((node) => {
      const box = node.getBoundingClientRect();
      return box.width > 0 && box.right > window.innerWidth
        ? [
            {
              tag: node.tagName,
              id: node.id,
              class: node.getAttribute("class"),
              left: box.left,
              right: box.right,
              width: box.width,
            },
          ]
        : [];
    }),
  }));
  expect(layout.width <= layout.viewport, JSON.stringify(layout)).toBe(true);
}
