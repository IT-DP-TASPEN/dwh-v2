const { test, expect } = require("@playwright/test");

test("normal export viewer stays isolated", async ({ page }) => {
  await page.goto("/exports?persona=normal");
  await expect(page.getByRole("heading", { name: "My exports" })).toBeVisible();
  await expect(page.getByRole("link", { name: "#100" })).toBeVisible();
  await expect(page.getByRole("link", { name: "#200" })).toHaveCount(0);
  await expect(page.getByRole("navigation", { name: "Export scope" })).toHaveCount(0);

  let response = await page.goto("/exports?persona=normal&scope=all");
  expect(response.status()).toBe(403);
  response = await page.goto("/exports/200?persona=normal");
  expect(response.status()).toBe(403);
  response = await page.request.get("/exports/200/download?persona=normal");
  expect(response.status()).toBe(403);
});

test("privileged viewer switches scopes and downloads cross-user artifact", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 640 });
  await page.goto("/exports");
  await expect(page.getByRole("heading", { name: "All exports" })).toBeVisible();
  await expect(page.getByText("User B", { exact: true }).first()).toBeVisible();
  await expect(page.getByRole("link", { name: "#200" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);

  await page.getByRole("link", { name: "My exports", exact: true }).click();
  await expect(page).toHaveURL(/scope=mine/);
  await expect(page.getByRole("heading", { name: "My exports" })).toBeVisible();
  await expect(page.getByText("No exports found.")).toBeVisible();
  await page.reload();
  await expect(page.getByRole("heading", { name: "My exports" })).toBeVisible();

  await page.getByRole("link", { name: "All exports", exact: true }).click();
  await expect(page).toHaveURL(/scope=all/);
  await page.getByRole("link", { name: "#200" }).click();
  await expect(page.getByRole("heading", { name: "Export #200" })).toBeVisible();
  await expect(page.getByText("@user-b")).toBeVisible();

  await page.goto("/exports?scope=all");
  const download = page.waitForEvent("download");
  await page.locator("#export-200").getByRole("link", { name: "Download" }).click();
  expect((await download).suggestedFilename()).toBe("cross-user.xlsx");

  expect((await page.request.get("/exports/203/download")).status()).toBe(404);
  await page.goto("/exports/202");
  await expect(page.getByText("Artifact expired or was removed by normal retention cleanup.")).toBeVisible();
  expect((await page.request.get("/exports/202/download")).status()).toBe(403);

  await page.evaluate(() => window.Alpine.store("theme").set("dark"));
  await expect(page.locator("html")).toHaveClass(/dark/);
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);
});
