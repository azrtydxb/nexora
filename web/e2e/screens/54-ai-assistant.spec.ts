import { test, expect, env, login } from "../fixtures";

// The plan e2e/gui_seed_ai_assistant_test.go scripts the assistant with.
const group = "guest-wifi-gui";

// Catches an assistant that never shows the model's reply, a plan panel without the action it would
// run, an apply that does not reach the configuration API, and a session lost on reload.
test("operator plans a policy group with the assistant and applies it", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.goto("/ai/assistant");
  await page.getByTestId("ai-assistant-new").click();
  await expect(page).toHaveURL(/session=/);
  const session = page.url();

  await page
    .getByTestId("ai-assistant-message")
    .fill(`Block malware for 10.99.0.0/24 as ${group}`);
  await page.getByTestId("ai-assistant-send").click();
  await expect(page.getByTestId("ai-task-status")).toContainText("Thinking");
  await expect(page.getByTestId("ai-assistant-msg-user")).toContainText(group);
  await expect(page.getByTestId("ai-assistant-msg-assistant")).toContainText(
    `I will create ${group}.`,
  );

  const plan = page.getByTestId("ai-assistant-plan");
  await expect(plan).toContainText("createPolicyGroup");
  await expect(
    page.getByText("Nothing changes until you apply the plan."),
  ).toBeVisible();

  await plan.getByTestId("ai-proposal-apply").click();
  await expect(page.getByTestId("ai-apply-action")).toContainText(
    "createPolicyGroup",
  );
  // The plan's group enables the malware category, whose sources are non-commercial.
  await page.getByTestId("ai-apply-acknowledge-license").check();
  await page.getByTestId("ai-apply-confirm").click();
  await expect(page.getByTestId("ai-apply-result")).toContainText("applied");
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);

  await page.getByTestId("nav-policies").click();
  await expect(
    page.getByRole("row", { name: new RegExp(group) }),
  ).toContainText("10.99.0.0/24");

  // The session lives in the URL: reopening it keeps the conversation.
  await page.goto(session);
  await expect(page.getByTestId("ai-assistant-msg-assistant")).toContainText(
    `I will create ${group}.`,
  );
});

test("assistant at 400px", async ({ page }) => {
  await page.setViewportSize({ width: 400, height: 900 });
  await login(
    page,
    env("NEXORA_E2E_OPERATOR_USER"),
    env("NEXORA_E2E_OPERATOR_PASSWORD"),
  );
  await page.goto("/ai/assistant");
  await expect(page.getByTestId("ai-assistant-message")).toBeVisible();
  await page.getByTestId("ai-assistant-new").click();
  await expect(page).toHaveURL(/session=/);
  await expect(page.getByTestId("ai-assistant-send")).toBeVisible();
});
