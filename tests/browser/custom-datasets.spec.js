const { test, expect } = require("@playwright/test");

const csv = { name: "ledger.csv", mimeType: "text/csv", buffer: Buffer.from("Name,Amount\nAlpha,10\nBeta,20\n") };

test("CSV dropzone selects, replaces, removes, and drops without auto-upload", async ({ page }) => {
  await page.goto("/case/custom-datasets-new");
  await page.getByRole("link", { name: "New dataset" }).click();

  const dropzone = page.locator("[data-csv-dropzone]");
  const input = page.getByLabel("CSV file");
  const submit = page.getByRole("button", { name: "Upload and preview" });
  await expect(input).toHaveAttribute("type", "file");
  await expect(input).toHaveAttribute("accept", ".csv,text/csv");
  await expect(input).not.toHaveAttribute("multiple", "");
  await expect(submit).toBeDisabled();

  let chooserPromise = page.waitForEvent("filechooser");
  await dropzone.click();
  await (await chooserPromise).setFiles(csv);
  await expect(page.getByText("ledger.csv", { exact: true })).toBeVisible();
  await expect(page.getByText(`${csv.buffer.length} bytes`, { exact: true })).toBeVisible();
  await expect(submit).toBeEnabled();
  await expect(page).toHaveURL(/\/custom-datasets\/new$/);

  const replacement = { ...csv, name: "replacement.csv", buffer: Buffer.alloc(27_955) };
  chooserPromise = page.waitForEvent("filechooser");
  await page.getByRole("button", { name: "Replace" }).click();
  await (await chooserPromise).setFiles(replacement);
  await expect(page.getByText("replacement.csv", { exact: true })).toBeVisible();
  await expect(page.getByText("27.3 KB", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Remove" }).click();
  await expect(page.getByText("Drop CSV file here", { exact: true })).toBeVisible();
  await expect(submit).toBeDisabled();

  chooserPromise = page.waitForEvent("filechooser");
  await dropzone.press("Enter");
  await (await chooserPromise).setFiles({ ...csv, name: "keyboard.csv" });
  await expect(page.getByText("keyboard.csv", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Remove" }).click();

  const dataTransfer = await page.evaluateHandle(() => {
    const transfer = new DataTransfer();
    transfer.items.add(new File(["Name,Amount\nGamma,30\n"], "dropped.csv", { type: "text/csv" }));
    return transfer;
  });
  await dropzone.dispatchEvent("drop", { dataTransfer });
  await expect(page.getByText("dropped.csv", { exact: true })).toBeVisible();
  await expect(submit).toBeEnabled();
  await expect(page).toHaveURL(/\/custom-datasets\/new$/);
});

test("new CSV preview detects delimiter, header, inferred types, and SQL names", async ({ page }) => {
  await page.goto("/case/custom-datasets-new");
  await page.getByRole("link", { name: "New dataset" }).click();
  await page.getByLabel("CSV file").setInputFiles(csv);
  await page.getByRole("button", { name: "Upload and preview" }).click();
  await expect(page.getByLabel("Delimiter")).toHaveValue("comma");
  await expect(page.getByLabel("Header record")).toHaveValue("1");
  await expect(page.getByText("amount", { exact: true })).toBeVisible();
  const amountType = page.locator('select[name="type_1"]');
  const amountDateFormat = page.locator('select[name="date_format_1"]');
  await expect(amountType).toHaveValue("integer");
  await expect(amountDateFormat).toBeHidden();
  await expect(amountDateFormat).toBeDisabled();
  await amountType.selectOption("datetime");
  await expect(amountDateFormat).toBeVisible();
  await expect(amountDateFormat).toBeEnabled();
  await amountType.selectOption("integer");
  await expect(amountDateFormat).toBeHidden();
  await expect(amountDateFormat).toBeDisabled();
  for (const control of [page.getByLabel("Delimiter"), page.getByLabel("Header record"), page.getByLabel("Dataset name"), amountType]) {
    await expect(control).toHaveClass(/border-slate-300/);
    await expect(control).toHaveClass(/dark:border-slate-700/);
  }
  await expect(page.locator("[data-column-configuration]")).toHaveClass(/border-slate-200/);
  await expect(page.locator("[data-column-configuration]")).toHaveClass(/dark:border-slate-800/);
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
