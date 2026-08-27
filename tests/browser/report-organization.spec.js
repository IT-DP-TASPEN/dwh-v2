const { test, expect } = require("@playwright/test");

test("star updates dedicated section without losing search", async ({ page }) => {
  await page.goto("/case/reports");
  const search = page.getByLabel("Search reports");
  await search.fill("NPL");
  await expect(page).toHaveURL(/\/reports\?q=NPL$/);
  await expect(page.locator("#starred-reports-heading")).toHaveCount(0);

  await page.getByRole("button", { name: "Star NPL per Cabang" }).click();
  await expect(page.locator("#starred-reports-heading")).toBeVisible();
  await expect(search).toHaveValue("NPL");
  await expect(page).toHaveURL(/\/reports\?q=NPL$/);

  await page.locator("#scoped-reports-heading").locator("..")
    .getByRole("button", { name: "Unstar NPL per Cabang" }).click();
  await expect(page.locator("#starred-reports-heading")).toHaveCount(0);
  await expect(search).toHaveValue("NPL");
});

test("moving from folder preserves filter and deletion keeps reports accessible", async ({ page }) => {
  await page.goto("/case/reports");
  await page.getByRole("link", { name: /Kredit 1/ }).click();
  await expect(page).toHaveURL(/\/reports\?folder=3$/);

  const search = page.getByLabel("Search reports");
  await search.fill("NPL");
  const card = page.getByRole("article").filter({ hasText: "NPL per Cabang" });
  await card.getByText("Move to folder").click();
  await card.getByLabel("Folder for NPL per Cabang").selectOption("");
  await card.getByRole("button", { name: "Move" }).click();
  await expect(page.getByText("No reports in this folder.")).toBeVisible();
  await expect(search).toHaveValue("NPL");
  await expect(page).toHaveURL(/folder=3.*q=NPL|q=NPL.*folder=3/);

  await page.goto("/case/reports");
  await page.getByText("Manage").click();
  await page.getByRole("button", { name: "Delete folder" }).click();
  await expect(page.getByText(/Reports will not be deleted\. 1 currently visible reports/)).toBeVisible();
  await page.getByRole("button", { name: "Delete folder" }).last().click();
  await expect(page).toHaveURL(/\/reports$/);
  await expect(page.getByRole("link", { name: "NPL per Cabang" })).toBeVisible();
  await expect(page.getByText("No personal folders yet.")).toBeVisible();
});

test("report organization follows dark theme", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("theme", "dark"));
  await page.goto("/case/reports");
  await expect(page.locator("html")).toHaveClass(/dark/);
  await expect(page.getByRole("article").first()).toHaveClass(/dark:bg-slate-900/);
});
