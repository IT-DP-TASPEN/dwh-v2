const { test, expect } = require("@playwright/test");

async function submitFilters(page) {
  await page.getByRole("button", { name: "Filter" }).click();
  await expect(page.locator("#runs-table")).toBeVisible();
}

test("Run All children load once after success and retain their DOM", async ({ page }) => {
  let childRequests = 0;
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/runs/251/children") childRequests++;
  });

  await page.goto("/runs");
  await expect(page.getByRole("link", { name: "#251" })).toBeVisible();
  await expect(page.getByRole("link", { name: "#253" })).toHaveCount(0);
  await expect(page.locator("[data-run-all-child]")).toHaveCount(0);

  const expand = page.getByRole("button", { name: "Expand Run All #251 children" });
  const firstResponse = page.waitForResponse((response) => new URL(response.url()).pathname === "/runs/251/children" && response.status() === 200);
  await expand.click();
  await firstResponse;
  await expect(page.getByRole("button", { name: "Collapse Run All #251 children" })).toHaveAttribute("aria-expanded", "true");
  const children = page.locator("#run-all-children-251 [data-run-all-child]");
  await expect(children).toHaveCount(2);
  await expect(children.nth(0)).toHaveAttribute("data-child-position", "1");
  await expect(children.nth(0).getByRole("link", { name: "#252" })).toHaveAttribute("href", "/runs/252");
  await expect(children.nth(1)).toHaveAttribute("data-child-position", "2");
  await expect(children.nth(1).getByRole("link", { name: "#253" })).toHaveAttribute("href", "/runs/253");
  expect(childRequests).toBe(1);

  await page.getByRole("button", { name: "Collapse Run All #251 children" }).click();
  await expect(children.nth(0)).toBeHidden();
  await page.getByRole("button", { name: "Expand Run All #251 children" }).click();
  await expect(children.nth(0)).toBeVisible();
  expect(childRequests).toBe(1);
});

test("failed child load exits loading state and retries", async ({ page }) => {
  let childRequests = 0;
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/runs/230/children") childRequests++;
  });

  await page.goto("/runs");
  const failedResponse = page.waitForResponse((response) => new URL(response.url()).pathname === "/runs/230/children" && response.status() === 500);
  await page.getByRole("button", { name: "Expand Run All #230 children" }).click();
  await failedResponse;
  const failure = page.getByRole("alert").filter({ hasText: "Could not load Run All children" });
  await expect(failure).toBeVisible();
  await expect(page.locator("#run-all-children-230").getByText("Loading children…")).toBeHidden();
  expect(childRequests).toBe(1);

  const retryResponse = page.waitForResponse((response) => new URL(response.url()).pathname === "/runs/230/children" && response.status() === 200);
  await failure.getByRole("button", { name: "Retry" }).click();
  await retryResponse;
  await expect(page.locator("#run-all-children-230 [data-run-all-child]")).toHaveCount(1);
  await expect(failure).toHaveCount(0);
  expect(childRequests).toBe(2);
});

test("multiple Run All parents remain expanded", async ({ page }) => {
  await page.goto("/runs");
  const firstResponse = page.waitForResponse((response) => new URL(response.url()).pathname === "/runs/251/children");
  await page.getByRole("button", { name: "Expand Run All #251 children" }).click();
  await firstResponse;
  const secondResponse = page.waitForResponse((response) => new URL(response.url()).pathname === "/runs/240/children");
  await page.getByRole("button", { name: "Expand Run All #240 children" }).click();
  await secondResponse;
  await expect(page.getByRole("button", { name: "Collapse Run All #251 children" })).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByRole("button", { name: "Collapse Run All #240 children" })).toHaveAttribute("aria-expanded", "true");
  await expect(page.locator("#run-all-children-251 [data-run-all-child]").first()).toBeVisible();
  await expect(page.locator("#run-all-children-240 [data-run-all-child]").first()).toBeVisible();
});

test("filters preserve top-level semantics and explicit child mode stays flat", async ({ page }) => {
  await page.goto("/runs");
  await expect(page.getByRole("link", { name: "#253" })).toHaveCount(0);

  await page.getByRole("combobox", { name: "Status" }).selectOption("failed");
  await submitFilters(page);
  await expect(page).toHaveURL(/status=failed/);
  await expect(page.getByRole("link", { name: "#253" })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "#259" })).toBeVisible();

  await page.getByRole("combobox", { name: "Status" }).selectOption("");
  await page.getByRole("combobox", { name: "Kind" }).selectOption("run_all_child");
  await submitFilters(page);
  await expect(page.getByRole("link", { name: "#253" })).toBeVisible();
  await expect(page.getByRole("link", { name: "#252" })).toBeVisible();
  await expect(page.getByRole("button", { name: /Run All #.* children/ })).toHaveCount(0);
  const childRow = page.locator("tr").filter({ has: page.getByRole("link", { name: "#253" }) });
  await expect(childRow.getByRole("link", { name: "Run All #251" })).toHaveAttribute("href", "/runs/251");

  await page.getByRole("combobox", { name: "Status" }).selectOption("failed");
  await submitFilters(page);
  await expect(page.getByRole("link", { name: "#253" })).toBeVisible();
  await expect(page.getByRole("link", { name: "#241" })).toBeVisible();
  await expect(page.getByRole("link", { name: "#252" })).toHaveCount(0);

  await page.setViewportSize({ width: 375, height: 640 });
  await page.goto("/runs");
  await expect(page.getByRole("button", { name: "Expand Run All #251 children" })).toBeVisible();
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(overflow).toBeLessThanOrEqual(0);
});
