import { test, expect, env, login } from "../fixtures";

test("lost trust appears, persists through hold-down, and clears on recovery", async ({
  page,
  request,
}) => {
  const zone = "lost-trust.gui.test.";
  const engineID = env("NEXORA_E2E_LOST_TRUST_ENGINE_ID");
  const transition = async (state: string, states: string[]) => {
    const response = await request.post(
      `${env("NEXORA_E2E_LOST_TRUST_CONTROL")}/${state}`,
      {
        headers: {
          Authorization: `Bearer ${env("NEXORA_E2E_LOST_TRUST_TOKEN")}`,
        },
      },
    );
    expect(response.status()).toBe(204);
    // Use the real, authenticated status API. No route interception or synthetic
    // browser response: verify the fixture survived storage and API conversion.
    await expect
      .poll(async () => {
        const status = await page.request.get("/api/v1/dnssec/status");
        expect(status.status()).toBe(200);
        const body = await status.json();
        const engine = body.engines.find(
          (e: { engine_id: string }) => e.engine_id === engineID,
        );
        expect(engine?.engine_name).toBe("gui-lost-trust-telemetry");
        return engine.trust_anchors
          .filter((a: { zone: string }) => a.zone === zone)
          .map((a: { state: string }) => a.state);
      })
      .toEqual(states);
    await page.reload();
    // Wait for the engine's status table, so banner absence cannot pass while
    // the status query is still loading.
    await expect(
      page.getByRole("region", { name: "Validation by engine" }),
    ).toContainText("gui-lost-trust-telemetry");
  };
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.getByTestId("nav-dnssec").click();
  const banner = page.getByTestId("dnssec-trust-point-lost");
  try {
    await transition("valid", ["valid"]);
    await expect(banner).toHaveCount(0);
    await transition("lost", ["revoked"]);
    await expect(banner).toBeVisible();
    await expect(banner).toContainText(`Trust point lost for ${zone}`);
    await expect(banner).toContainText("gui-lost-trust-telemetry");
    await expect(banner).toContainText("no longer validated");
    await transition("pending", ["revoked", "add_pend"]);
    await expect(banner).toBeVisible();
    await expect(banner).toContainText(`Trust point lost for ${zone}`);
    await transition("recovered", ["revoked", "valid"]);
    await expect(banner).toHaveCount(0);
  } finally {
    const response = await request.post(
      `${env("NEXORA_E2E_LOST_TRUST_CONTROL")}/valid`,
      {
        headers: {
          Authorization: `Bearer ${env("NEXORA_E2E_LOST_TRUST_TOKEN")}`,
        },
      },
    );
    expect(response.status()).toBe(204);
  }
});
