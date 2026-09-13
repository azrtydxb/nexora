import { test, expect, env, login } from "../fixtures";

test("settings shows the DNS encryption certificate state", async ({
  page,
}) => {
  // covers getDnsTlsStatus through the page's browser request
  await login(
    page,
    env("NEXORA_E2E_VIEWER_USER"),
    env("NEXORA_E2E_VIEWER_PASSWORD"),
  );
  await page.getByTestId("nav-settings").click();
  const section = page.getByRole("region", {
    name: "DNS encryption certificate",
  });
  await expect(section).toBeVisible();
  const status = await (
    await page.request.get("/api/v1/settings/dns-tls")
  ).json();
  if (status.configured) {
    await expect(
      section.getByText(status.certificate.fingerprint_sha256),
    ).toBeVisible();
    await expect(section.getByRole("table", { name: "Engines" })).toBeVisible();
  } else {
    await expect(
      section.getByText(
        /Not configured\. Set NEXORA_DNS_TLS_CERT_FILE and NEXORA_DNS_TLS_KEY_FILE/,
      ),
    ).toBeVisible();
  }
});
