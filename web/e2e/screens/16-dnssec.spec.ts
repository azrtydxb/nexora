import { test, expect, env, login } from "../fixtures";

test("operator edits DNSSEC settings, trust anchors and negative trust anchors", async ({
  page,
}) => {
  // covers getDnssecSettings, updateDnssecSettings, getDnssecStatus, listTrustAnchors, createTrustAnchor,
  // deleteTrustAnchor, listNegativeTrustAnchors, createNegativeTrustAnchor, deleteNegativeTrustAnchor
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.getByTestId("nav-dnssec").click();
  await expect(page.getByRole("heading", { name: "DNSSEC" })).toBeVisible();

  const settings = page.getByRole("region", { name: "Validation settings" });
  const rfc5011 = settings.getByRole("switch", {
    name: "Automated trust anchor updates (RFC 5011)",
  });
  await expect(rfc5011).toBeChecked();
  await rfc5011.click();
  // The e2e harness may have turned forwarded validation off; flip whatever state it is in.
  const forwarded = settings.getByRole("switch", {
    name: "Validate forwarded answers",
  });
  const forwardedWas = await forwarded.isChecked();
  await forwarded.click();
  await settings.getByRole("button", { name: "Save settings" }).click();
  await expect(settings.getByText("Settings saved")).toBeVisible();
  await page.reload();
  const reloaded = page.getByRole("region", { name: "Validation settings" });
  await expect(
    reloaded.getByRole("switch", {
      name: "Automated trust anchor updates (RFC 5011)",
    }),
  ).not.toBeChecked();
  await expect(
    reloaded.getByRole("switch", { name: "Validate forwarded answers" }),
  ).toBeChecked({ checked: !forwardedWas });

  const anchors = page.getByRole("region", { name: "Trust anchors" });
  await expect(anchors.getByRole("row", { name: /20326 8 2/ })).toContainText(
    "IANA",
  );
  await expect(anchors.getByRole("row", { name: /38696 8 2/ })).toContainText(
    "IANA",
  );
  await anchors.getByRole("button", { name: "Add trust anchor" }).click();
  let dialog = page.getByRole("dialog", { name: "Add trust anchor" });
  await dialog.getByLabel("Zone").fill("example.");
  await dialog.getByLabel("DS record").fill("1 13 2 ZZ");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog.getByRole("alert")).toContainText("digest");
  await dialog
    .getByLabel("DS record")
    .fill(
      "12345 13 2 0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
    );
  await dialog.getByRole("button", { name: "Save" }).click();
  const anchor = anchors.getByRole("row", { name: /12345 13 2/ });
  await expect(anchor).toContainText("Operator");
  await anchor
    .getByRole("button", { name: "Delete trust anchor 12345 for example." })
    .click();
  await page.getByTestId("confirm-delete").click();
  await expect(anchor).toHaveCount(0);

  const ntas = page.getByRole("region", { name: "Negative trust anchors" });
  await ntas.getByRole("button", { name: "Add negative trust anchor" }).click();
  dialog = page.getByRole("dialog", { name: "Add negative trust anchor" });
  await dialog.getByLabel("Domain").fill("broken.example");
  await dialog.getByLabel("Reason").fill("expired signatures at provider");
  await dialog.getByLabel("Expires in").click();
  await page.getByRole("option", { name: "1 day", exact: true }).click();
  await dialog.getByRole("button", { name: "Save" }).click();
  const nta = ntas.getByRole("row", { name: /broken\.example\./ });
  await expect(nta).toContainText("expired signatures at provider");
  await nta
    .getByRole("button", {
      name: "Delete negative trust anchor broken.example.",
    })
    .click();
  await page.getByTestId("confirm-delete").click();
  await expect(nta).toHaveCount(0);

  await expect(
    page.getByRole("region", { name: "Validation by engine" }),
  ).toContainText("gui-engine");
});
