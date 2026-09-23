import {
  test,
  expect,
  type Locator,
  type Page,
  type Response,
} from "@playwright/test";
import { saveM8 } from "../m8";

// Unit doubles only: these exercise the response lifecycle without a browser or
// intercepting any acceptance API request.
function write(status: number, readBody: () => Promise<string>) {
  let observed = false;
  const response = {
    request: () => ({ method: () => "DELETE" }),
    url: () => "http://localhost/api/v1/rpz/zones/seed",
    ok: () => status >= 200 && status < 300,
    status: () => status,
    statusText: () => (status === 403 ? "Forbidden" : ""),
    text: readBody,
  } as unknown as Response;
  const page = {
    waitForResponse: async (matches: (r: Response) => boolean) => {
      expect(matches(response)).toBe(true);
      observed = true;
      return response;
    },
  } as unknown as Page;
  const button = {
    click: async () => {
      expect(observed).toBe(true);
    },
  } as unknown as Locator;
  return () => saveM8(page, button, "DELETE", /\/api\/v1\/rpz\/zones\/seed$/);
}

test("saveM8 accepts a successful navigation without reading its discarded body", async () => {
  let reads = 0;
  const save = write(204, async () => {
    reads++;
    throw new Error("No data found for resource with given identifier");
  });
  expect((await save()).status()).toBe(204);
  expect(reads).toBe(0);
});

test("saveM8 rejects a failed write with its HTTP status and server error", async () => {
  const save = write(403, async () => '{"message":"Permission denied"}');
  await expect(save()).rejects.toThrow(
    /DELETE .*HTTP 403 Forbidden[\s\S]*Permission denied/,
  );
});

test("saveM8 still rejects a failed write when navigation discarded its error body", async () => {
  const save = write(500, async () => {
    throw new Error("resource disappeared");
  });
  await expect(save()).rejects.toThrow(
    /HTTP 500[\s\S]*Response body unavailable: Error: resource disappeared/,
  );
});
