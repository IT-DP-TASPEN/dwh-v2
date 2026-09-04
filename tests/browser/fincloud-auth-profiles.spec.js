const { test, expect } = require("@playwright/test");

test.beforeEach(async ({ page }) => {
  await page.goto("/case/fincloud-auth");
});

test("create uses current role/location choices and edit retains selected IDs", async ({ page }) => {
  await page.getByRole("link", { name: "New profile" }).click();
  await expect(page.getByLabel("Role")).toContainText("Role-A — Operations");
  await expect(page.getByLabel("Location")).toContainText("Location-02 — Branch");
  await page.getByLabel("Name", { exact: true }).fill("Created profile");
  await page.getByLabel("Username").fill("ExactCase");
  await page.getByLabel("Role").selectOption("Role-B");
  await page.getByLabel("Location").selectOption("Location-02");
  await page.getByLabel("Password").fill("BrowserSecret");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText("Role-B", { exact: true })).toBeVisible();
  await expect(page.locator("body")).not.toContainText("BrowserSecret");
  await page.getByRole("link", { name: "Edit" }).click();
  await expect(page.getByLabel("Role")).toHaveValue("Role-B");
  await expect(page.getByLabel("Location")).toHaveValue("Location-02");
  await expect(page.getByLabel("Password (leave blank to preserve)")).toHaveValue("");
});

test("edit exposes and preserves unavailable stored values", async ({ page }) => {
  await page.goto("/case/fincloud-auth-stale");
  await expect(page.getByLabel("Role")).toHaveValue("Missing-Role");
  await expect(page.getByLabel("Role")).toContainText("Missing-Role — Currently configured (unavailable)");
  await expect(page.getByLabel("Location")).toHaveValue("Missing-Location");
  await page.getByLabel("Name", { exact: true }).fill("Renamed");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText("Missing-Role", { exact: true })).toBeVisible();
  await expect(page.getByText("Missing-Location", { exact: true })).toBeVisible();
});

test("profile credentials stay write-only and lifecycle actions preserve exact identifiers", async ({ page }) => {
  await page.getByRole("link", { name: "Operations" }).click();
  await expect(page.getByText("CaseSensitive", { exact: true })).toBeVisible();
  await expect(page.locator("body")).not.toContainText("BrowserSecret");

  await page.getByRole("link", { name: "Edit" }).click();
  await expect(page.getByLabel("Password (leave blank to preserve)")).toHaveValue("");
  await expect(page.locator("body")).not.toContainText("BrowserSecret");
  await page.getByLabel("Username").fill("CaseSensitiveV2");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByText("CaseSensitiveV2", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Test Connection" }).click();
  await expect(page.getByRole("heading", { name: "Operations" })).toBeVisible();
  await page.getByRole("button", { name: "Test and Activate" }).click();
  await expect(page.getByText(/active · 3/)).toBeVisible();
  await page.getByRole("button", { name: "Disable" }).click();
  await expect(page.getByText(/disabled · 4/)).toBeVisible();
  await page.getByRole("button", { name: "Archive" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Archive" }).click();
  await expect(page.getByText(/archived · 5/)).toBeVisible();
  await expect(page.getByRole("button", { name: "Test Connection" })).toHaveCount(0);
});

test("source binding clears configuration-required and read-only users cannot manage profiles", async ({ page }) => {
  await page.goto("/fincloud-auth-profiles/1");
  await page.getByRole("button", { name: "Test and Activate" }).click();
  await page.goto("/sources");
  await expect(page.getByText("Configuration required", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Save" })).toHaveCount(0);
  await page.locator('select[name="profile_id"]').selectOption("1");
  await expect(page.getByText("Configuration required", { exact: true })).toHaveCount(0);
  await page.reload();
  await expect(page.locator('select[name="profile_id"]')).toHaveValue("1");
  await page.locator('select[name="profile_id"]').selectOption("2");
  await expect(page.getByRole("alert")).toContainText("persisted selection restored");
  await expect(page.locator('select[name="profile_id"]')).toHaveValue("1");

  await page.goto("/fincloud-auth-profiles?persona=view");
  await expect(page.getByRole("link", { name: "New profile" })).toHaveCount(0);
  await page.goto("/fincloud-auth-profiles/1?persona=view");
  await expect(page.getByRole("link", { name: "Edit" })).toHaveCount(0);
  await expect(page.locator("main form button")).toHaveCount(0);
});

test("representative borders stay canonical in light and dark modes", async ({ page }) => {
  await page.goto("/fincloud-auth-profiles/1/edit");
  await expect(page.getByLabel("Name", { exact: true })).toHaveClass(/border-slate-300/);
  await expect(page.getByLabel("Name", { exact: true })).toHaveClass(/dark:border-slate-700/);
  await expect(page.locator("main form")).toHaveClass(/border-slate-200/);
  await expect(page.locator("main form")).toHaveClass(/dark:border-slate-800/);
  await page.goto("/sources");
  await expect(page.locator('select[name="profile_id"]')).toHaveClass(/border-slate-300/);
  await expect(page.locator('select[name="profile_id"]')).toHaveClass(/dark:border-slate-700/);
  await expect(page.locator("main table").locator("..")).toHaveClass(/border-slate-200/);
  await expect(page.locator("main table").locator("..")).toHaveClass(/dark:border-slate-800/);
  await page.evaluate(() => localStorage.setItem("theme", "dark"));
  await page.reload();
  await expect(page.locator("html")).toHaveClass(/dark/);
});

test("run details show only frozen nonsecret profile context", async ({ page }) => {
  await page.goto("/case/cancel");
  await expect(page.getByRole("heading", { name: "Frozen Fincloud authentication" })).toBeVisible();
  await expect(page.getByText("Operations · revision 7", { exact: true })).toBeVisible();
  await expect(page.getByText("CaseSensitive", { exact: true })).toBeVisible();
  await expect(page.locator("body")).not.toContainText("BrowserSecret");
});
