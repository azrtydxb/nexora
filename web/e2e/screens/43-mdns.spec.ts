import { saveM8, expectNoPageOverflow } from "../m8";
import type { Page } from "@playwright/test";
import { test, expect, env, login } from "../fixtures";

// Each writer establishes its own baseline using the current revision, including
// after a previous width failed before its cleanup.
async function resetMdns(page: Page, id: string) {
  const response = await page.request.get(`/api/v1/engine-groups/${id}`);
  expect(response.ok()).toBe(true);
  const group = await response.json();
  const reset = await page.request.put(`/api/v1/engine-groups/${id}`, {
    data: {
      revision: group.revision,
      name: group.name,
      mdns: {
        enabled: false,
        interfaces: [],
        reflect: false,
        reflect_interfaces: [],
        timeout_ms: 500,
      },
    },
  });
  expect(reset.ok(), await reset.text()).toBe(true);
}

for (const width of [1280, 400]) {
  test(`engine group mDNS settings at ${width}px`, async ({ page }) => {
    // TestGUICoverage counts: getEngineGroup, updateEngineGroup.
    await page.setViewportSize({ width, height: 900 });
    await login(
      page,
      env("NEXORA_E2E_OPERATOR_USER"),
      env("NEXORA_E2E_OPERATOR_PASSWORD"),
    );
    const id = env("NEXORA_E2E_MDNS_GROUP_ID");
    await resetMdns(page, id);
    await page.goto(`/engines/groups/${id}`);
    const section = page.getByRole("region", { name: "mDNS" });
    await section.getByTestId("mdns-enabled").click();
    await section.getByRole("button", { name: "Save mDNS" }).click();
    await expect(
      section.getByText("Name at least one interface"),
    ).toBeVisible();
    await section.getByTestId("mdns-interfaces").fill("vlan10, vlan20");
    await section.getByTestId("mdns-reflect").click();
    await section.getByTestId("mdns-reflect-interfaces").fill("vlan10, vlan20");
    await section.getByTestId("mdns-timeout").fill("700");
    await saveM8(
      page,
      section.getByRole("button", { name: "Save mDNS" }),
      "PUT",
      /\/api\/v1\/engine-groups\//,
    );
    await expect(
      section.getByText("mDNS saved", { exact: true }),
    ).toContainText("mDNS saved");
    await expectNoPageOverflow(page);
    await page.reload();
    await expect(section.getByTestId("mdns-enabled")).toBeChecked();
    await expect(section.getByTestId("mdns-interfaces")).toHaveValue(
      "vlan10, vlan20",
    );
    await expect(section.getByTestId("mdns-reflect")).toBeChecked();
    await expect(section.getByTestId("mdns-timeout")).toHaveValue("700");
    await expect(section.getByText("needs hostNetwork")).toBeVisible();
    await section.getByTestId("mdns-enabled").click();
    await section.getByTestId("mdns-reflect").click();
    await section.getByTestId("mdns-interfaces").fill("");
    await section.getByTestId("mdns-reflect-interfaces").fill("");
    await section.getByTestId("mdns-timeout").fill("500");
    await saveM8(
      page,
      section.getByRole("button", { name: "Save mDNS" }),
      "PUT",
      /\/api\/v1\/engine-groups\//,
    );
    await expect(
      section.getByText("mDNS saved", { exact: true }),
    ).toContainText("mDNS saved");
    await page.reload();
    await expect(section.getByTestId("mdns-enabled")).not.toBeChecked();
  });
}

test("mDNS rejects stale edits without overwriting a concurrent group change", async ({
  page,
}) => {
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  const id = env("NEXORA_E2E_MDNS_GROUP_ID");
  await resetMdns(page, id);
  await page.goto(`/engines/groups/${id}`);
  const section = page.getByRole("region", { name: "mDNS" });
  await expect(section.getByTestId("mdns-timeout")).toHaveValue("500");
  await section.getByTestId("mdns-timeout").fill("800");
  const before = await page.request.get(`/api/v1/engine-groups/${id}`);
  expect(before.ok()).toBe(true);
  const original = await before.json();
  const competing = await page.request.put(`/api/v1/engine-groups/${id}`, {
    data: {
      revision: original.revision,
      name: original.name,
      description: "concurrent M8 edit",
    },
  });
  expect(competing.ok()).toBe(true);
  const saved = await competing.json();
  const pending = page.waitForResponse(
    (r) =>
      r.request().method() === "PUT" &&
      new URL(r.url()).pathname === `/api/v1/engine-groups/${id}`,
  );
  await section.getByRole("button", { name: "Save mDNS", exact: true }).click();
  expect((await pending).status()).toBe(409);
  await expect(section.getByRole("alert")).toContainText(
    "changed by someone else",
  );
  await expect(section.getByTestId("mdns-timeout")).toHaveValue("800");
  const after = await page.request.get(`/api/v1/engine-groups/${id}`);
  expect(after.ok()).toBe(true);
  const unchanged = await after.json();
  expect(unchanged.revision).toBe(saved.revision);
  expect(unchanged.description).toBe("concurrent M8 edit");
  expect(unchanged.mdns).toEqual(saved.mdns);
  expect(unchanged.mdns.timeout_ms).toBe(500);
  await section
    .getByRole("button", { name: "Discard changes and reload" })
    .click();
  await expect(section.getByTestId("mdns-timeout")).toHaveValue("500");
  const restore = await page.request.put(`/api/v1/engine-groups/${id}`, {
    data: {
      revision: saved.revision,
      name: saved.name,
      description: original.description,
    },
  });
  expect(restore.ok()).toBe(true);
});

test("viewer sees mDNS without mutation controls", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_VIEWER_USER"),
    env("NEXORA_E2E_VIEWER_PASSWORD"),
  );
  await page.goto(`/engines/groups/${env("NEXORA_E2E_MDNS_GROUP_ID")}`);
  const section = page.getByRole("region", { name: "mDNS" });
  await expect(section.getByTestId("mdns-enabled")).toBeDisabled();
  await expect(section.getByTestId("mdns-reflect")).toBeDisabled();
  await expect(section.getByRole("button", { name: "Save mDNS" })).toHaveCount(
    0,
  );
});
