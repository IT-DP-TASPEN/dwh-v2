const { test, expect } = require("@playwright/test");

async function resetSchedules(page) {
  await page.goto("/case/schedules");
  await expect(page.getByRole("heading", { name: "Schedules", exact: true })).toBeVisible();
}

async function confirmBulk(page, action, count) {
  await page.getByRole("button", { name: action, exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: `${action} ${count} schedules?` })).toBeVisible();
  await expect(dialog).toHaveClass(/(^|\s)border-slate-200(\s|$)/);
  await expect(dialog).toHaveClass(/(^|\s)dark:border-slate-700(\s|$)/);
  if (action === "Archive") {
    await expect(dialog).toContainText("This action is permanent. Archived schedules cannot be enabled or restored.");
  }
  await dialog.getByRole("button", { name: `${action} ${count} schedules`, exact: true }).click();
  await page.waitForLoadState("domcontentloaded");
}

test("schedule selection state and all bulk transitions stay page-local", async ({ page }) => {
  await resetSchedules(page);
  await expect(page.locator("html")).not.toHaveClass(/dark/);

  const selectAll = page.getByRole("checkbox", { name: "Select all schedules on this page" });
  const disabled = page.getByRole("checkbox", { name: "Select schedule: Disabled schedule" });
  const enabled = page.getByRole("checkbox", { name: "Select schedule: Enabled schedule" });
  await expect(page.getByRole("checkbox", { name: "Select schedule: Archived schedule" })).toHaveCount(0);
  await expect(selectAll).not.toBeChecked();
  expect(await selectAll.evaluate((element) => element.indeterminate)).toBe(false);

  await disabled.check();
  await expect(page.getByText("1 schedule selected", { exact: true })).toBeVisible();
  expect(await selectAll.evaluate((element) => element.indeterminate)).toBe(true);

  await selectAll.check();
  await expect(disabled).toBeChecked();
  await expect(enabled).toBeChecked();
  await expect(selectAll).toBeChecked();
  expect(await selectAll.evaluate((element) => element.indeterminate)).toBe(false);

  await enabled.uncheck();
  expect(await selectAll.evaluate((element) => element.indeterminate)).toBe(true);
  await disabled.uncheck();
  await expect(selectAll).not.toBeChecked();
  expect(await selectAll.evaluate((element) => element.indeterminate)).toBe(false);

  await selectAll.check();
  await confirmBulk(page, "Enable", 2);
  await expect(page.getByRole("row").filter({ hasText: "Disabled schedule" })).toContainText("Enabled");
  await expect(page.getByText(/schedules selected/)).toBeHidden();

  await page.getByRole("checkbox", { name: "Select all schedules on this page" }).check();
  await confirmBulk(page, "Disable", 2);
  await expect(page.getByRole("row").filter({ hasText: "Enabled schedule" })).toContainText("Disabled");

  await page.getByRole("checkbox", { name: "Select all schedules on this page" }).check();
  await confirmBulk(page, "Archive", 2);
  await expect(page.getByRole("checkbox", { name: "Select all schedules on this page" })).toHaveCount(0);
  await expect(page.locator("[data-schedule-checkbox]")).toHaveCount(0);
  await expect(page.getByText(/schedules selected/)).toBeHidden();
  await page.getByRole("link", { name: "Schedules", exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "Schedules", exact: true })).toBeVisible();
});

test("filtered selection resets after mutation and remains usable at 375px in dark mode", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 700 });
  await resetSchedules(page);
  await page.goto("/schedules?enabled=false&archived=false");
  await expect(page.getByRole("row").filter({ hasText: "Disabled schedule" })).toBeVisible();
  await expect(page.getByRole("row").filter({ hasText: "Enabled schedule" })).toHaveCount(0);

  await page.evaluate(() => window.Alpine.store("theme").set("dark"));
  await expect(page.locator("html")).toHaveClass(/dark/);
  await page.getByRole("checkbox", { name: "Select all schedules on this page" }).check();
  await page.getByRole("button", { name: "Enable", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Enable 1 schedule?" })).toBeVisible();
  const box = await dialog.boundingBox();
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(375);
  await dialog.getByRole("button", { name: "Enable 1 schedule", exact: true }).click();
  await page.waitForLoadState("domcontentloaded");

  expect(new URL(page.url()).searchParams.get("enabled")).toBe("false");
  expect(new URL(page.url()).searchParams.get("archived")).toBe("false");
  await expect(page.getByText("No schedules found.", { exact: true })).toBeVisible();
  await expect(page.locator("[data-schedule-checkbox]")).toHaveCount(0);
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);
});
