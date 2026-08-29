const { test, expect } = require("@playwright/test");

const clockStart = new Date("2026-08-29T00:00:00Z");
const activeWave = "2026-08-27T18:00:00Z";
const animationFrame = 20;

function childResponse(id, status) {
  return (response) => {
    const url = new URL(response.url());
    return url.pathname === `/runs/${id}/children` && (status === undefined || response.status() === status);
  };
}

function waveResponse(key, status) {
  return (response) => {
    const url = new URL(response.url());
    return url.pathname === "/runs/scheduler-wave" && url.searchParams.get("scheduled_for") === key && (status === undefined || response.status() === status);
  };
}

async function openRuns(page) {
  await page.clock.install({ time: clockStart });
  await page.goto("/runs");
  await page.clock.pauseAt(new Date(clockStart.getTime() + 1000));
}

async function clickForResponse(page, button, predicate) {
  const response = page.waitForResponse(predicate);
  await button.click();
  const result = await response;
  await page.clock.runFor(animationFrame);
  return result;
}

async function pollOnce(page, predicate) {
  const response = page.waitForResponse(predicate);
  await page.clock.runFor(5000);
  const result = await response;
  await page.clock.runFor(animationFrame);
  return result;
}

async function submitFilters(page) {
  await page.getByRole("button", { name: "Filter" }).click();
  await expect(page.locator("#runs-table")).toBeVisible();
}

test("collapsed groups make zero polling requests", async ({ page }) => {
  let requests = 0;
  page.on("request", (request) => {
    const path = new URL(request.url()).pathname;
    if (path === "/runs/scheduler-wave" || /^\/runs\/\d+\/children$/.test(path)) requests++;
  });

  await page.setViewportSize({ width: 375, height: 640 });
  await openRuns(page);
  await page.clock.runFor(20_000);

  expect(requests).toBe(0);
  await expect(page.locator("[data-runs-accordion][data-open='true']")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Expand Run All #251 children" })).toHaveAttribute("aria-expanded", "false");
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);

  await page.evaluate(() => window.Alpine.store("theme").set("dark"));
  await expect(page.locator("html")).toHaveClass(/dark/);
  expect(await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)).toBeLessThanOrEqual(0);
});

test("Run All final refresh updates detail summary and status then stops", async ({ page }) => {
  let requests = 0;
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/runs/251/children") requests++;
  });
  await openRuns(page);

  const expand = page.locator('button[aria-controls="run-all-children-251"]');
  const owner = page.locator('tbody[data-runs-accordion]:has(button[aria-controls="run-all-children-251"])');
  await owner.evaluate((element) => (element.dataset.browserOwner = "stable"));
  expect((await clickForResponse(page, expand, childResponse(251, 200))).status()).toBe(200);

  const child = page.locator("#run-all-children-251 [data-child-position='2']");
  await expect(owner).toHaveAttribute("data-open", "true");
  await expect(expand).toHaveAttribute("aria-expanded", "true");
  await expect(child.getByLabel("Status: Running")).toBeVisible();
  await expect(page.locator("#run-all-summary-251")).toContainText("1 / 2 complete");

  expect((await pollOnce(page, childResponse(251, 200))).status()).toBe(200);
  await expect(child.getByLabel("Status: Succeeded")).toBeVisible();
  await expect(page.locator("#run-all-summary-251")).toContainText("2 / 2 complete");
  await expect(page.locator("#run-all-status-251").getByLabel("Status: Running")).toBeVisible();

  expect((await pollOnce(page, childResponse(251, 200))).status()).toBe(200);
  await expect(page.locator("#run-all-status-251").getByLabel("Status: Completed")).toBeVisible();
  await expect(page.locator("#run-all-children-251")).not.toHaveAttribute("hx-trigger", /every 5s/);
  await expect(owner).toHaveAttribute("data-browser-owner", "stable");
  expect(requests).toBe(3);

  await page.clock.runFor(15_000);
  expect(requests).toBe(3);
});

test("refresh failure retains DOM; collapse and resume keep one timer", async ({ page }) => {
  let requests = 0;
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/runs/220/children") requests++;
  });
  await openRuns(page);

  const expand = page.locator('button[aria-controls="run-all-children-220"]');
  const owner = page.locator('tbody[data-runs-accordion]:has(button[aria-controls="run-all-children-220"])');
  await owner.evaluate((element) => (element.dataset.browserOwner = "stable"));
  await clickForResponse(page, expand, childResponse(220, 200));
  const child = page.locator("#run-all-children-220 [data-run-all-child]");
  await expect(child.getByLabel("Status: Running")).toBeVisible();

  expect((await pollOnce(page, childResponse(220, 500))).status()).toBe(500);
  await expect(child.getByLabel("Status: Running")).toBeVisible();
  await expect(page.locator("#run-all-children-220").getByText("Refresh failed · Retrying…", { exact: true })).toBeVisible();
  await expect(owner).toHaveAttribute("data-open", "true");
  await expect(owner).toHaveAttribute("data-browser-owner", "stable");
  await expect(page.locator("#run-all-children-220")).toHaveAttribute("data-loaded", "true");

  await page.getByRole("button", { name: "Collapse Run All #220 children" }).click();
  await page.clock.runFor(animationFrame);
  await expect(owner).toHaveAttribute("data-open", "false");
  await page.clock.runFor(15_000);
  expect(requests).toBe(2);

  await page.getByRole("button", { name: "Expand Run All #220 children" }).click();
  await page.clock.runFor(animationFrame);
  await expect(owner).toHaveAttribute("data-open", "true");
  expect(requests).toBe(2);
  expect((await pollOnce(page, childResponse(220, 200))).status()).toBe(200);
  await expect(child.getByLabel("Status: Succeeded")).toBeVisible();
  await expect(page.locator("#run-all-children-220").getByText("Refresh failed · Retrying…", { exact: true })).toBeHidden();

  expect((await pollOnce(page, childResponse(220, 200))).status()).toBe(200);
  expect(requests).toBe(4);
  await expect(owner).toHaveAttribute("data-browser-owner", "stable");

  await page.getByRole("button", { name: "Collapse Run All #220 children" }).click();
  await page.clock.runFor(animationFrame);
  await page.clock.runFor(10_000);
  expect(requests).toBe(4);
  await page.getByRole("button", { name: "Expand Run All #220 children" }).click();
  await page.clock.runFor(animationFrame);
  await pollOnce(page, childResponse(220, 200));
  expect(requests).toBe(5);

  await submitFilters(page);
  await page.clock.runFor(15_000);
  expect(requests).toBe(5);
});

test("failed first load stays unloaded until real Retry then polls", async ({ page }) => {
  let requests = 0;
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/runs/230/children") requests++;
  });
  await openRuns(page);

  const expand = page.getByRole("button", { name: "Expand Run All #230 children" });
  expect((await clickForResponse(page, expand, childResponse(230, 500))).status()).toBe(500);
  const host = page.locator("#run-all-children-230");
  const failure = page.getByRole("alert").filter({ hasText: "Could not load Run All children" });
  await expect(failure).toBeVisible();
  await expect(host).toHaveAttribute("data-loaded", "false");
  await expect(host.locator("[data-run-all-child]")).toHaveCount(0);

  await page.clock.runFor(10_000);
  expect(requests).toBe(1);

  const retried = page.waitForResponse(childResponse(230, 200));
  await failure.getByRole("button", { name: "Retry" }).click();
  await retried;
  await page.clock.runFor(animationFrame);
  await expect(page.locator("#run-all-children-230")).toHaveAttribute("data-loaded", "true");
  await expect(page.locator("#run-all-children-230").getByLabel("Status: Queued")).toBeVisible();

  await pollOnce(page, childResponse(230, 200));
  await expect(page.locator("#run-all-children-230").getByLabel("Status: Running")).toBeVisible();
  expect(requests).toBe(3);
});

test("Scheduler retry history and summary refresh while Activity stays frozen", async ({ page }) => {
  let requests = 0;
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === "/runs/scheduler-wave" && url.searchParams.get("scheduled_for") === activeWave) requests++;
  });
  await openRuns(page);

  const expand = page.locator('button[aria-controls="scheduler-wave-1787853600000000"]');
  const owner = page.locator(`[data-scheduler-wave="${activeWave}"]`);
  const activity = owner.locator("tr").first().locator("td").nth(4);
  await owner.evaluate((element) => (element.dataset.browserOwner = "stable"));
  await clickForResponse(page, expand, waveResponse(activeWave, 200));

  const host = page.locator("#scheduler-wave-1787853600000000");
  await expect(host.locator("[data-scheduler-attempt]")).toHaveCount(1);
  await expect(host.getByLabel("Status: Failed")).toBeVisible();
  await expect(activity).toHaveText("2026-08-28 10:04:00 UTC");

  await pollOnce(page, waveResponse(activeWave, 200));
  await expect(host.locator("[data-scheduler-attempt]")).toHaveCount(2);
  await expect(host.locator("[data-scheduler-attempt='1']").getByLabel("Status: Failed")).toBeVisible();
  await expect(host.locator("[data-scheduler-attempt='2']").getByLabel("Status: Running")).toBeVisible();
  await expect(page.locator("#scheduler-wave-summary-1787853600000000")).toHaveText("1 occurrence · 1 unresolved · 2 attempts");
  await expect(activity).toHaveText("2026-08-28 10:04:00 UTC");

  await pollOnce(page, waveResponse(activeWave, 200));
  await expect(host.locator("[data-scheduler-attempt]")).toHaveCount(2);
  await expect(host.locator("[data-scheduler-attempt='2']").getByLabel("Status: Succeeded")).toBeVisible();
  await expect(page.locator("#scheduler-wave-summary-1787853600000000")).toHaveText("1 occurrence · 1 resolved · 2 attempts");
  await expect(activity).toHaveText("2026-08-28 10:04:00 UTC");
  await expect(host).not.toHaveAttribute("hx-trigger", /every 5s/);
  await expect(owner).toHaveAttribute("data-browser-owner", "stable");

  await page.clock.runFor(15_000);
  expect(requests).toBe(3);
});

test("multiple expanded groups poll independently", async ({ page }) => {
  const requests = { run: 0, wave: 0, terminal: 0 };
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === "/runs/220/children") requests.run++;
    if (url.pathname === "/runs/240/children") requests.terminal++;
    if (url.pathname === "/runs/scheduler-wave" && url.searchParams.get("scheduled_for") === activeWave) requests.wave++;
  });
  await openRuns(page);

  await clickForResponse(page, page.getByRole("button", { name: "Expand Run All #220 children" }), childResponse(220, 200));
  await clickForResponse(page, page.getByRole("button", { name: "Expand Scheduled 27 Aug 2026 18:00:00 UTC attempts" }), waveResponse(activeWave, 200));
  await clickForResponse(page, page.getByRole("button", { name: "Expand Run All #240 children" }), childResponse(240, 200));

  const runPoll = page.waitForResponse(childResponse(220, 500));
  const wavePoll = page.waitForResponse(waveResponse(activeWave, 200));
  await page.clock.runFor(5000);
  await Promise.all([runPoll, wavePoll]);
  await page.clock.runFor(animationFrame);
  expect(requests).toEqual({ run: 2, wave: 2, terminal: 1 });

  await page.getByRole("button", { name: "Collapse Run All #220 children" }).click();
  await page.clock.runFor(animationFrame);
  await pollOnce(page, waveResponse(activeWave, 200));
  expect(requests).toEqual({ run: 2, wave: 3, terminal: 1 });

  await page.clock.runFor(10_000);
  expect(requests).toEqual({ run: 2, wave: 3, terminal: 1 });
});

test("filters preserve top-level semantics and explicit child mode stays flat", async ({ page }) => {
  await page.goto("/runs");
  await expect(page.getByRole("link", { name: "#253" })).toHaveCount(0);

  await page.getByRole("combobox", { name: "Status" }).selectOption("failed");
  await submitFilters(page);
  await expect(page).toHaveURL(/status=failed/);
  await expect(page.getByRole("button", { name: "Expand Scheduled 27 Aug 2026 18:00:00 UTC attempts" })).toBeVisible();

  await page.getByRole("combobox", { name: "Trigger" }).selectOption("scheduler");
  await submitFilters(page);
  await expect(page.getByRole("link", { name: "#259" })).toBeVisible();
  await expect(page.getByRole("button", { name: /Expand Scheduled/ })).toHaveCount(0);

  await page.getByRole("combobox", { name: "Status" }).selectOption("");
  await page.getByRole("combobox", { name: "Trigger" }).selectOption("");
  await page.getByRole("combobox", { name: "Kind" }).selectOption("run_all_child");
  await submitFilters(page);
  await expect(page.getByRole("link", { name: "#253" })).toBeVisible();
  await expect(page.getByRole("button", { name: /Run All #.* children/ })).toHaveCount(0);

  await page.getByRole("combobox", { name: "Status" }).selectOption("failed");
  await submitFilters(page);
  await expect(page.getByRole("link", { name: "#241" })).toBeVisible();
  await expect(page.getByRole("link", { name: "#253" })).toHaveCount(0);
});
