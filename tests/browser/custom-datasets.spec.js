const { test, expect } = require("@playwright/test");

const csv = { name: "ledger.csv", mimeType: "text/csv", buffer: Buffer.from("Name,Amount\nAlpha,10\nBeta,20\n") };

test("new CSV preview detects delimiter, header, inferred types, and SQL names", async ({ page }) => {
  await page.goto("/case/custom-datasets-new");
  await page.getByRole("link", { name: "New dataset" }).click();
  await page.getByLabel("CSV file").setInputFiles(csv);
  await page.getByRole("button", { name: "Upload and preview" }).click();
  await expect(page.getByLabel("Delimiter")).toHaveValue("comma");
  await expect(page.getByLabel("Header record")).toHaveValue("1");
  await expect(page.getByText("amount", { exact: true })).toBeVisible();
  await expect(page.locator('select[name="type_1"]')).toHaveValue("integer");
  await expect(page.getByText("Alpha", { exact: true })).toBeVisible();
  await page.getByLabel("Dataset name").fill("Uploaded ledger");
  await page.getByRole("button", { name: "Submit replace import" }).click();
  await expect(page.getByText("active", { exact: true })).toBeVisible();
  await expect(page.getByText("custom_dataset_view_1", { exact: true })).toBeVisible();
});

test("failed provisioning supports retained retry and a corrected immutable upload", async ({ page }) => {
  await page.goto("/case/custom-datasets-failed");
  await expect(page.getByText("CSV validation failed at record 3.")).toBeVisible();
  await page.getByRole("link", { name: "Retry retained CSV" }).click();
  await expect(page).toHaveURL(/uploads\/1\/configure/);
  await expect(page.locator('select[name^="type_"]')).toHaveCount(2);
  await page.getByRole("button", { name: "Submit replace import" }).click();
  await expect(page.getByText("active", { exact: true })).toBeVisible();

  await page.goto("/case/custom-datasets-failed");
  await page.getByRole("link", { name: "Upload corrected CSV" }).click();
  await page.getByLabel("CSV file").setInputFiles(csv);
  await page.getByRole("button", { name: "Upload and preview" }).click();
  await expect(page).toHaveURL(/uploads\/2\/configure/);
  await page.getByRole("button", { name: "Submit replace import" }).click();
  await expect(page.getByText("#3 · upload #2", { exact: true })).toBeVisible();
  await expect(page.getByText("#1 · upload #1", { exact: true })).toBeVisible();
});

test("active schema stays frozen through Append and archive remains queryable", async ({ page }) => {
  await page.goto("/case/custom-datasets-active");
  await page.getByRole("link", { name: "Append CSV" }).click();
  await page.getByLabel("CSV file").setInputFiles(csv);
  await page.getByRole("button", { name: "Upload and preview" }).click();
  await expect(page.locator('select[name^="type_"]')).toHaveCount(0);
  await expect(page.getByText("integer", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Submit append import" }).click();
  await expect(page.getByText("4", { exact: true })).toBeVisible();
  await expect(page.getByText("append", { exact: true })).toBeVisible();

  await page.getByLabel("Name").fill("Renamed ledger");
  await page.getByRole("button", { name: "Save metadata" }).click();
  await expect(page.getByRole("heading", { name: "Renamed ledger" })).toBeVisible();
  await page.getByRole("button", { name: "Archive dataset" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Archive" }).click();
  await expect(page.getByText("archived", { exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: /CSV/ })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Save metadata" })).toHaveCount(0);
  await expect(page.getByText("SELECT * FROM `custom_dataset_view_1`;", { exact: true })).toBeVisible();
});

test("view-only permission hides all custom dataset mutations", async ({ page }) => {
  await page.goto("/custom-datasets?persona=view");
  await expect(page.getByRole("link", { name: "New dataset" })).toHaveCount(0);
  await page.goto("/custom-datasets/1?persona=view");
  await expect(page.getByRole("link", { name: /CSV/ })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Save metadata" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Archive dataset" })).toHaveCount(0);
});
