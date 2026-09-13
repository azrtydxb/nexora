import { test, expect, env, login } from "../fixtures";

test("operator edits global safe search and manages a policy group", async ({
  page,
}) => {
  // TestGUICoverage counts the browser requests below: getGlobalSafeSearch, updateGlobalSafeSearch,
  // listPolicyGroups, createPolicyGroup, getPolicyGroup (Reload), updatePolicyGroup, deletePolicyGroup.
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.getByTestId("nav-policies").click();

  const global = page.getByRole("region", { name: "Global safe search" });
  await global.getByRole("switch", { name: "Bing" }).click();
  await global.getByRole("radio", { name: "Moderate" }).click();
  await global.getByRole("button", { name: "Save global safe search" }).click();
  await expect(page.getByText("Global safe search saved")).toBeVisible();
  await page.reload();
  await expect(
    page
      .getByRole("region", { name: "Global safe search" })
      .getByRole("switch", { name: "Bing" }),
  ).toBeChecked();

  const name = `kids-${Date.now()}`;
  await page.getByRole("button", { name: "New group" }).click();
  const dialog = page.getByRole("dialog", { name: "New policy group" });
  await dialog.getByLabel("Name").fill(name);
  await dialog
    .getByLabel("Client CIDRs")
    .fill("192.168.250.0/24\n192.168.251.1/24");
  await dialog.getByLabel("Allowlist").fill("school.example");
  await dialog.getByRole("switch", { name: "Google" }).click();
  await dialog.getByRole("radio", { name: "Strict" }).click();
  await expect(
    dialog.getByRole("group", { name: "Filter lists" }),
  ).toBeVisible();
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(
    dialog.getByText("cidr 192.168.251.1/24 has host bits set"),
  ).toBeVisible();
  await dialog.getByLabel("Client CIDRs").fill("192.168.250.0/24");
  await dialog.getByRole("button", { name: "Save" }).click();
  const row = page.getByRole("row", { name: new RegExp(name) });
  await expect(row).toContainText("192.168.250.0/24");

  await row.getByRole("button", { name: `Edit ${name}` }).click();
  const edit = page.getByRole("dialog", { name: "Edit policy group" });
  await expect(edit.getByLabel("Allowlist")).toHaveValue("school.example");
  // a concurrent edit through the API (page.request shares the session cookie) makes the dialog's revision stale
  const groups = await (await page.request.get("/api/v1/policy-groups")).json();
  const g = groups.find((x: { name: string }) => x.name === name);
  const res = await page.request.put(`/api/v1/policy-groups/${g.id}`, {
    data: { ...g, description: "changed elsewhere" },
    headers: { "Content-Type": "application/json" },
  });
  expect(res.status()).toBe(200);
  await edit.getByLabel("Description").fill("my change");
  await edit.getByRole("button", { name: "Save" }).click();
  await expect(
    edit.getByText(
      "This group was changed by someone else. Reload to see the latest version.",
    ),
  ).toBeVisible();
  await edit.getByRole("button", { name: "Reload" }).click();
  await expect(edit.getByLabel("Description")).toHaveValue("changed elsewhere");
  await edit.getByLabel("Description").fill("my change");
  await edit.getByRole("button", { name: "Save" }).click();
  await expect(edit).toBeHidden();

  await row.getByRole("button", { name: `Delete ${name}` }).click();
  await page.getByTestId("confirm-delete").click();
  await expect(page.getByRole("row", { name: new RegExp(name) })).toHaveCount(
    0,
  );
});

test("viewer sees policies read-only", async ({ page }) => {
  await login(
    page,
    env("NEXORA_E2E_VIEWER_USER"),
    env("NEXORA_E2E_VIEWER_PASSWORD"),
  );
  await page.getByTestId("nav-policies").click();
  await expect(
    page.getByRole("heading", { name: "Policy groups" }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "New group" })).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Save global safe search" }),
  ).toHaveCount(0);
});
