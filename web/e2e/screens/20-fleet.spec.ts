import { test, expect, env, login } from "../fixtures";

test("admin manages engine groups, engines, rollouts and certificates", async ({
  page,
}) => {
  // covers getFleetSummary, listEngineGroups, createEngineGroup, getEngineGroup, updateEngineGroup,
  // updateEngine, getEngineStats, rotateEngineCertificate, listRollouts, getRollout,
  // rollbackEngineGroup, resumeEngineGroupRollouts, revokeEngine, deleteEngineGroup
  await login(
    page,
    env("NEXORA_E2E_ADMIN_USER"),
    env("NEXORA_E2E_ADMIN_PASSWORD"),
  );
  await page.getByTestId("nav-engines").click();
  await expect(page.getByTestId("fleet-summary")).toContainText("default");

  await page.getByTestId("enginegroup-add").click();
  await page.getByTestId("enginegroup-name").fill("gui-edge");
  await page.getByTestId("enginegroup-save").click();
  await expect(page.getByTestId("enginegroup-row-gui-edge")).toContainText(
    "all_at_once",
  );

  await page.getByTestId("engine-open-gui-engine").click();
  await expect(page.getByTestId("engine-detail")).toContainText("gui-engine");
  await expect(page.getByTestId("engine-stats-chart")).toBeVisible();
  await page.getByTestId("engine-group-select").click();
  await page.getByRole("option", { name: "gui-edge", exact: true }).click();
  await page.getByTestId("engine-label-add").click();
  await page.getByTestId("engine-label-key-0").fill("nexora.io/canary");
  await page.getByTestId("engine-label-value-0").fill("true");
  await page.getByTestId("engine-save").click();
  await expect(page.getByTestId("engine-group-name")).toHaveText("gui-edge");
  await expect(page.getByTestId("engine-status")).toContainText("current", {
    timeout: 30_000,
  });
  await page.getByTestId("engine-rotate").click();
  await page.getByTestId("confirm-rotate").click();
  await expect(page.getByText("Rotation requested")).toBeVisible();

  await page.getByTestId("nav-engines").click();
  await page.getByTestId("enginegroup-open-gui-edge").click();
  await expect(page.getByTestId("enginegroup-detail")).toContainText(
    "gui-edge",
  );
  await page.getByTestId("enginegroup-description").fill("from the gui");
  await page.getByTestId("enginegroup-save-settings").click();
  await expect(page.getByText("Saved")).toBeVisible();

  await page.getByTestId("rollout-open").first().click();
  await expect(page.getByTestId("rollout-detail")).toBeVisible();
  await expect(page.getByTestId("rollout-progress")).toHaveAttribute(
    "role",
    "progressbar",
  );
  await expect(page.getByTestId("rollout-engines")).toContainText("gui-engine");
  await page.goBack();

  await page.getByTestId("enginegroup-rollback").click();
  await page.getByTestId("rollback-version").click();
  await page.getByRole("option").last().click();
  await page.getByTestId("rollback-confirm").click();
  await expect(page.getByTestId("enginegroup-paused")).toBeVisible();
  await page.getByTestId("enginegroup-resume").click();
  await expect(page.getByTestId("enginegroup-paused")).toHaveCount(0);

  await page.getByTestId("nav-engines").click();
  await page.getByTestId("engine-open-gui-engine").click();
  await page.getByTestId("engine-revoke").click();
  await page.getByTestId("confirm-revoke").click();
  await expect(page.getByTestId("engine-status")).toContainText("revoked");

  await page.getByTestId("nav-engines").click();
  await page.getByTestId("enginegroup-add").click();
  await page.getByTestId("enginegroup-name").fill("gui-tmp");
  await page.getByTestId("enginegroup-save").click();
  await page.getByTestId("enginegroup-open-gui-tmp").click();
  await page.getByTestId("enginegroup-delete").click();
  await page.getByTestId("confirm-delete").click();
  await expect(page.getByTestId("enginegroup-row-gui-tmp")).toHaveCount(0);
});
