const { test, expect } = require("@playwright/test");

const contextPopovers = (page) => page.locator("[data-context-popover]");
const folderPopover = (page, id) => page.locator(`[data-folder-actions-popover="${id}"]`);

async function clickAndWaitForHTMX(page, button) {
  await page.evaluate(() => {
    window.__reportsAfterSettle = false;
    document.body.addEventListener("htmx:afterSettle", () => {
      window.__reportsAfterSettle = true;
    }, { once: true });
  });
  await button.click();
  await page.waitForFunction(() => window.__reportsAfterSettle === true);
}

async function expectAllContextPopoversHidden(page) {
  const popovers = contextPopovers(page);
  await expect(popovers).not.toHaveCount(0);
  for (const popover of await popovers.all()) {
    await expect(popover).toBeHidden();
  }
}

test("context popovers stay closed through repeated report-browser swaps", async ({ page }) => {
  await page.goto("/case/reports");
  await expectAllContextPopoversHidden(page);

  for (let cycle = 0; cycle < 2; cycle += 1) {
    await clickAndWaitForHTMX(page, page.getByRole("button", { name: "Star NPL per Cabang" }));
    await expectAllContextPopoversHidden(page);
    await clickAndWaitForHTMX(page, page.getByRole("button", { name: "Unstar NPL per Cabang" }).last());
    await expectAllContextPopoversHidden(page);
  }

  await page.getByRole("button", { name: "Actions for Kredit" }).click();
  await expect(folderPopover(page, 3)).toBeVisible();
  await page.getByRole("button", { name: "Actions for Deposito" }).click();
  await expect(folderPopover(page, 3)).toBeHidden();
  await expect(folderPopover(page, 4)).toBeVisible();

  await page.keyboard.press("Escape");
  await expect(folderPopover(page, 4)).toBeHidden();
  const card = page.getByRole("article").filter({ hasText: "NPL per Cabang" }).last();
  await card.getByRole("button", { name: "Actions for NPL per Cabang" }).click();
  await expect(card.locator("[data-context-popover]")).toBeVisible();
  await page.getByRole("heading", { name: "Reports", exact: true }).click();
  await expect(card.locator("[data-context-popover]")).toBeHidden();
});

test("folder popovers use compact keyboard-accessible controls", async ({ page }) => {
  await page.goto("/case/reports");
  await expect(page.locator("#report-browser details")).toHaveCount(0);
  await expect(page.getByText("Manage", { exact: true })).toHaveCount(0);

  const kreditTrigger = page.getByRole("button", { name: "Actions for Kredit" });
  const kreditPopover = folderPopover(page, 3);
  const controlledID = await kreditTrigger.getAttribute("aria-controls");
  expect(controlledID).toBeTruthy();
  await expect(kreditPopover).toHaveAttribute("id", controlledID);
  await kreditTrigger.focus();
  await page.keyboard.press("Enter");
  await expect(kreditPopover).toBeVisible();
  await expect(page.getByRole("button", { name: "Rename folder" }).filter({ visible: true })).toBeFocused();

  await page.keyboard.press("Escape");
  await expect(kreditPopover).toBeHidden();
  await expect(kreditTrigger).toBeFocused();

  await kreditTrigger.click();
  await page.getByRole("heading", { name: "Reports", exact: true }).click();
  await expect(kreditPopover).toBeHidden();

  await kreditTrigger.click();
  await page.getByRole("button", { name: "Actions for Deposito" }).click();
  await expect(kreditPopover).toBeHidden();
  await expect(folderPopover(page, 4)).toBeVisible();
});

test("rename draft survives unrelated HTMX and own response stays authoritative", async ({ page }) => {
  await page.goto("/case/reports");
  await page.getByRole("button", { name: "Actions for Kredit" }).click();
  await page.getByRole("button", { name: "Rename folder" }).filter({ visible: true }).click();

  const rename = page.getByLabel("Rename Kredit");
  await expect(rename).toBeVisible();
  await expect(rename).toHaveValue("Kredit");
  await rename.fill("Draft Kredit");

  await clickAndWaitForHTMX(page, page.getByRole("button", { name: "Star NPL per Cabang" }));
  await expect(rename).toBeVisible();
  await expect(rename).toHaveValue("Draft Kredit");

  const search = page.getByLabel("Search reports");
  await search.fill("NPL");
  await expect(page).toHaveURL(/\/reports\?q=NPL$/);
  await expect(rename).toHaveValue("Draft Kredit");

  await rename.fill("Deposito");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("alert")).toHaveText("Folder name already exists.");
  await expect(page.getByLabel("Rename Kredit")).toHaveValue("Deposito");

  await page.getByLabel("Rename Kredit").fill("Kredit Baru");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page).toHaveURL(/\/reports\?q=NPL$/);
  await expect(page.getByRole("button", { name: "Actions for Kredit Baru" })).toBeVisible();
  await expect(page.getByLabel("Rename Kredit Baru")).toBeHidden();
});

test("rename cancel restores compact row and trigger focus", async ({ page }) => {
  await page.goto("/case/reports");
  const trigger = page.getByRole("button", { name: "Actions for Kredit" });
  await trigger.click();
  await page.getByRole("button", { name: "Rename folder" }).filter({ visible: true }).click();
  await page.getByLabel("Rename Kredit").fill("Discard me");
  await page.getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByLabel("Rename Kredit")).toBeHidden();
  await expect(trigger).toBeFocused();

  await trigger.click();
  await page.getByRole("button", { name: "Rename folder" }).filter({ visible: true }).click();
  await expect(page.getByLabel("Rename Kredit")).toHaveValue("Kredit");
});

test("report move popover updates badge and preserves folder search scope", async ({ page }) => {
  await page.goto("/case/reports");
  await page.getByRole("link", { name: "Kredit", exact: true }).click();
  const search = page.getByLabel("Search reports");
  await search.fill("NPL");
  const card = page.getByRole("article").filter({ hasText: "NPL per Cabang" });
  await expect(card.getByLabel("Folder: Kredit")).toBeVisible();
  await card.getByRole("button", { name: "Actions for NPL per Cabang" }).click();
  await expect(card.getByText("Move to folder", { exact: true })).toBeVisible();
  await expect(card.getByRole("button", { name: /Kredit.*Current folder/ })).toBeVisible();
  await card.getByRole("button", { name: "No Folder" }).click();
  await expect(page.getByText("No reports in this folder.")).toBeVisible();
  await expect(page).toHaveURL(/folder=3.*q=NPL|q=NPL.*folder=3/);
  await expect(search).toHaveValue("NPL");

  await page.getByRole("link", { name: "All Reports", exact: true }).click();
  const unfiledCard = page.getByRole("article").filter({ hasText: "NPL per Cabang" }).last();
  await expect(unfiledCard.getByLabel("Folder: Kredit")).toHaveCount(0);
  await unfiledCard.getByRole("button", { name: "Actions for NPL per Cabang" }).click();
  await unfiledCard.getByRole("button", { name: "Deposito" }).click();
  await expect(unfiledCard.getByLabel("Folder: Deposito")).toBeVisible();
});

test("folder deletion reuses dialog and leaves reports accessible", async ({ page }) => {
  await page.goto("/case/reports");
  const trigger = page.getByRole("button", { name: "Actions for Kredit" });
  await trigger.click();
  await page.getByRole("button", { name: "Delete folder" }).filter({ visible: true }).click();
  await expect(page.getByText(/Reports will not be deleted\. 1 currently visible reports/)).toBeVisible();
  await page.getByRole("button", { name: "Cancel" }).click();
  await expect(trigger).toBeFocused();

  await trigger.click();
  await page.getByRole("button", { name: "Delete folder" }).filter({ visible: true }).click();
  await page.getByRole("button", { name: "Delete folder" }).last().click();
  await expect(page).toHaveURL(/\/reports$/);
  await expect(page.getByRole("link", { name: "NPL per Cabang" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Actions for Kredit" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Actions for Deposito" })).toBeVisible();
});

test("popovers remain bounded and close on viewport changes", async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 500 });
  await page.goto("/case/reports");
  const card = page.getByRole("article").filter({ hasText: "NPL per Cabang" }).last();
  const trigger = card.getByRole("button", { name: "Actions for NPL per Cabang" });
  await trigger.click();
  const popover = card.locator("[x-ref=menu]");
  await expect(popover).toBeVisible();
  const box = await popover.boundingBox();
  expect(box.x).toBeGreaterThanOrEqual(8);
  expect(box.x + box.width).toBeLessThanOrEqual(367);

  await page.setViewportSize({ width: 390, height: 500 });
  await expect(popover).toBeHidden();
});

test("star keeps search and contextual UI supports dark and light themes", async ({ page }) => {
  await page.goto("/case/reports");
  const search = page.getByLabel("Search reports");
  await search.fill("NPL");
  await expect(page).toHaveURL(/\/reports\?q=NPL$/);
  await page.getByRole("button", { name: "Star NPL per Cabang" }).click();
  await expect(page.locator("#starred-reports-heading")).toBeVisible();
  await expect(search).toHaveValue("NPL");
  await page.getByRole("link", { name: /Starred/ }).click();
  await expect(page).toHaveURL(/starred=1.*q=NPL|q=NPL.*starred=1/);
  await page.getByRole("button", { name: "Unstar NPL per Cabang" }).click();
  await expect(page.getByText("No starred reports match this search.")).toBeVisible();
  await expect(search).toHaveValue("NPL");
  await expect(page.locator("html")).not.toHaveClass(/dark/);

  await page.evaluate(() => window.Alpine.store("theme").set("dark"));
  await expect(page.locator("html")).toHaveClass(/dark/);
  await page.getByRole("link", { name: "All Reports", exact: true }).click();
  const card = page.getByRole("article").filter({ hasText: "NPL per Cabang" }).last();
  await card.getByRole("button", { name: "Actions for NPL per Cabang" }).click();
  await expect(card.locator("[x-ref=menu]")).toHaveClass(/dark:bg-slate-900/);
});
