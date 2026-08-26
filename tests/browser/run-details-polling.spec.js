const { test, expect } = require("@playwright/test");

test("cancellation input and expanded diagnostics survive polling", async ({ page }) => {
  await page.goto("/case/cancel");

  const reason = "cancel exactly after current source row";
  const input = page.getByLabel("Cancellation reason (optional)");
  await input.fill(reason);

  const details = page.locator("#run-technical-details");
  await details.locator("summary").click();
  await expect(details).toHaveJSProperty("open", true);

  await expect(page.getByText("3 / 10 items", { exact: true })).toBeVisible({ timeout: 12_000 });
  await expect(input).toHaveValue(reason);
  await expect(details).toHaveJSProperty("open", true);
  await expect(page.locator("#run-technical-count")).toHaveText("(3 events)");
});

test("recovery action swaps once then preserves operator input", async ({ page }) => {
  let recoverySwaps = 0;
  page.on("response", (response) => {
    if (response.url().includes("/runs/2/status") && response.headers()["x-recover-action-swap"] === "true") recoverySwaps++;
  });

  await page.goto("/case/recover");
  const reason = page.getByLabel("Required reason");
  const verified = page.getByLabel("I verified the owning worker process is permanently stopped.");
  await expect(reason).toBeVisible({ timeout: 6_000 });

  const value = "worker PID verified stopped; lease owner unreachable";
  await reason.fill(value);
  await verified.check();

  await expect(page.getByText("4 / 10 items", { exact: true })).toBeVisible({ timeout: 12_000 });
  await expect(reason).toHaveValue(value);
  await expect(verified).toBeChecked();
  expect(recoverySwaps).toBe(1);
});

test("terminal transition removes actions and stops polling", async ({ page }) => {
  await page.goto("/case/terminal");
  await expect(page.getByLabel("Cancellation reason (optional)")).toBeVisible();
  await expect(page.getByLabel("Required reason")).toBeVisible();

  await expect(page.getByText("Succeeded", { exact: true })).toBeVisible({ timeout: 9_000 });
  await expect(page.getByLabel("Cancellation reason (optional)")).toHaveCount(0);
  await expect(page.getByLabel("Required reason")).toHaveCount(0);
  await expect(page.locator("#run-live-state")).not.toHaveAttribute("hx-get", /.+/);
});
