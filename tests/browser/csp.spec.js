const { test, expect } = require("@playwright/test");

test("production CSP permits theme, navigation, HTMX, datasource, and CSV controls", async ({ page }) => {
  const violations = [];
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.exposeFunction("reportCSPViolation", (violation) => violations.push(violation));
  await page.addInitScript(() => {
    document.addEventListener("securitypolicyviolation", (event) => {
      window.reportCSPViolation({ directive: event.effectiveDirective, blockedURI: event.blockedURI });
    });
  });

  const response = await page.goto("/");
  expect(response.headers()["content-security-policy"]).toContain("script-src 'self';");
  await page.getByLabel("Theme", { exact: true }).selectOption("dark");
  await expect(page.locator("html")).toHaveClass(/dark/);
  await page.getByLabel("Theme", { exact: true }).selectOption("light");
  await expect(page.locator("html")).not.toHaveClass(/dark/);
  await page.getByRole("button", { name: "Toggle sidebar" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-sidebar-collapsed", "true");
  await page.getByRole("button", { name: "Toggle sidebar" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-sidebar-collapsed", "false");
  expect(violations).toEqual([]);
  expect(errors).toEqual([]);

  await page.request.get("/case/reports");
  await page.goto("/reports");
  await page.getByRole("button", { name: "Actions for Kredit", exact: true }).click();
  await page.getByRole("button", { name: "Rename folder", exact: true }).filter({ visible: true }).click();
  const rename = page.getByLabel("Rename Kredit");
  await expect(rename).toBeFocused();
  await rename.fill("CSP Kredit");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  expect(violations).toEqual([]);
  expect(errors).toEqual([]);
  await expect(page.getByRole("button", { name: "Actions for CSP Kredit", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Star NPL per Cabang", exact: true }).click();
  await expect(page.getByRole("button", { name: "Unstar NPL per Cabang", exact: true }).last()).toBeVisible();

  await page.goto("/datasources/new");
  await page.locator("#network").selectOption("unix");
  await expect(page.locator("#socket_path")).toBeVisible();
  await expect(page.locator("#host")).toBeHidden();
  await page.locator("#network").selectOption("tcp");
  await expect(page.locator("#host")).toBeVisible();
  await expect(page.locator("#socket_path")).toBeHidden();

  await page.goto("/case/custom-datasets-new");
  await page.getByRole("link", { name: "New dataset", exact: true }).click();
  const transfer = await page.evaluateHandle(() => {
    const value = new DataTransfer();
    value.items.add(new File(["Name,Amount\nAlpha,10\n"], "csp.csv", { type: "text/csv" }));
    return value;
  });
  await page.locator("[data-csv-dropzone]").dispatchEvent("drop", { dataTransfer: transfer });
  expect(violations).toEqual([]);
  expect(errors).toEqual([]);
  await expect(page.getByText("csp.csv", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Upload and preview", exact: true })).toBeEnabled();
  await page.getByRole("button", { name: "Remove", exact: true }).click();
  await expect(page.getByRole("button", { name: "Upload and preview", exact: true })).toBeDisabled();

  expect(violations).toEqual([]);
  expect(errors).toEqual([]);
});

test("CSP preserves report editor, JSON results, and dynamic option fetching", async ({ page }) => {
  const errors = [];
  const violations = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.exposeFunction("recordReportCSP", (directive) => violations.push(directive));
  await page.addInitScript(() => document.addEventListener("securitypolicyviolation", (event) => window.recordReportCSP(event.effectiveDirective)));

  await page.goto("/csp/report-template");
  await expect(page.locator("#parameter-key-1")).toHaveValue("branch");
  await expect(page.locator("#parameter-default-option-1")).toHaveValue("001");
  await page.getByRole("button", { name: "+ Add parameter", exact: true }).click();
  await expect(page.locator("#parameter-key-3")).toBeVisible();
  await page.locator("#parameter-key-3").fill("date");
  await page.locator("#parameter-label-3").fill("Date");
  await page.locator("#parameter-type-3").selectOption("date");
  await expect(page.locator("#parameter-default-scalar-3")).toHaveAttribute("type", "date");
  await expect(page.getByText("No rows returned.", { exact: true })).toBeVisible();

  await page.goto("/csp/report-result");
  await expect(page.locator("#param_branch")).toBeEnabled();
  await expect(page.locator("#param_branch")).toHaveValue("001");
  await expect(page.getByText("No rows returned.", { exact: true })).toBeVisible();
  await expect(page.locator("td[colspan='1']")).toBeVisible();
  expect(errors).toEqual([]);
  expect(violations).toEqual([]);
});

for (const lateLoad of [false, true]) {
  test(`Runs stays CSP-safe when HTMX loads ${lateLoad ? "after page load" : "normally"}`, async ({ page }) => {
    await page.addInitScript(() => {
      window.runsCSPViolations = [];
      document.addEventListener("securitypolicyviolation", (event) => {
        window.runsCSPViolations.push(event.effectiveDirective);
      });
    });
    if (lateLoad) {
      // Load the actual bundle after readyState=complete, when HTMX starts
      // immediately during import, before app.js can configure it.
      await page.route("**/runs", async (route) => {
        const response = await route.fetch();
        await route.fulfill({ response, body: (await response.text()).replace('<script defer src="/static/js/app.js"></script>', "") });
      });
    }
    const response = await page.goto("/runs");
    expect(response.headers()["content-security-policy"]).toContain("style-src 'self';");
    if (lateLoad) {
      expect(await page.evaluate(() => document.readyState)).toBe("complete");
      await page.addScriptTag({ url: "/static/js/app.js" });
    }
    await expect(page.getByRole("heading", { name: "Runs", exact: true })).toBeVisible();
    await expect.poll(() => page.evaluate(() => window.htmx?.config.includeIndicatorStyles)).toBe(false);
    await expect(page.locator("head style")).toHaveCount(0);
    await page.getByRole("button", { name: "Expand Run All #251 children" }).click();
    await expect(page.locator("#run-all-children-251 [data-child-position='2']")).toBeVisible();
    await page.getByRole("button", { name: "Filter", exact: true }).click();
    await expect(page.locator("#runs-table")).toBeVisible();
    expect(await page.evaluate(() => window.runsCSPViolations)).toEqual([]);
  });
}
