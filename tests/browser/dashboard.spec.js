const { test, expect } = require("@playwright/test");

test("operational landing is compact, grouped, responsive, and theme-safe", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 700 });
  await page.goto("/");

  await expect(page.getByRole("heading", { name: "Operational Dashboard" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Dashboard" })).toHaveCount(1);
  for (const heading of ["Active ingestion", "Failed ingestion · 24h", "Needs attention", "Reporting export health", "Recent activity"]) {
    await expect(page.getByRole("heading", { name: heading, exact: true }).first()).toBeVisible();
  }

  const attention = page.locator('section[aria-labelledby="needs-attention-heading"]');
  await expect(attention.getByRole("link", { name: /Run All #240/ })).toHaveAttribute("href", "/runs/240");
  await expect(attention.getByRole("link", { name: /Schedule A/ })).toHaveAttribute("href", "/schedules/10/occurrences/700");
  await expect(attention.getByRole("link", { name: /NPL Report/ })).toHaveAttribute("href", "/exports#export-12");

  const active = page.locator('section[aria-labelledby="active-ingestion-heading"]');
  await expect(active).toContainText("Run All #251");
  await expect(active).toContainText("1 / 2 complete");
  await expect(active).toContainText("Scheduled wave");
  const recent = page.locator('section[aria-labelledby="recent-activity-heading"]');
  await expect(recent).toContainText("CIF Opening Report");
  await expect(recent).toContainText("Run All #240");
  await expect(recent.getByText("#253", { exact: true })).toHaveCount(0);

  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);
  await expect(page.locator("html")).not.toHaveClass(/dark/);
  await page.evaluate(() => window.Alpine.store("theme").set("dark"));
  await expect(page.locator("html")).toHaveClass(/dark/);
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);
});

test("reporting-only root resolves to Reports without Dashboard navigation", async ({ page }) => {
  await page.goto("/?persona=reporting");
  await expect(page).toHaveURL(/\/reports\?persona=reporting$/);
  await expect(page.getByRole("heading", { name: "Reports", exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Dashboard" })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "Reports", exact: true }).first()).toBeVisible();
});

test("ingestion Overview keeps controls and replaces history noise with Needs attention", async ({ page }) => {
  await page.goto("/ingestion");
  for (const text of ["Executable runs", "Sources", "Schedule backlog", "Needs attention"]) {
    await expect(page.getByText(text, { exact: true })).toBeVisible();
  }
  await expect(page.getByRole("link", { name: "Run All", exact: true })).toHaveAttribute("href", "/runs/run-all");
  await expect(page.getByText("Recent successes", { exact: true })).toHaveCount(0);
  await expect(page.getByText("Recent failures / abandoned", { exact: true })).toHaveCount(0);

  await page.goto("/ingestion?healthy=1");
  await expect(page.getByText("No ingestion issues currently require attention.", { exact: true })).toBeVisible();
});
