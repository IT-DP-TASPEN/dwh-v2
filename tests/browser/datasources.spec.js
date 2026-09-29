const { test, expect } = require("@playwright/test");

test("datasource mode controls required fields and submitted credentials", async ({ page }) => {
  await page.goto("/datasources/new");
  await expect(page.locator("#host")).toBeVisible();
  await expect(page.locator("#password")).toHaveJSProperty("required", true);
  await page.locator("#password").fill("old-tcp-secret");
  await page.locator("#network").selectOption("unix");
  await expect(page.locator("#socket_path")).toBeVisible();
  await expect(page.locator("#socket_path")).toHaveJSProperty("required", true);
  await expect(page.locator("#host")).toBeHidden();
  await expect(page.locator("#password")).toBeDisabled();
  await expect(page.locator("#tls_policy")).toBeDisabled();
  const values = await page.locator("form[data-network]").evaluate((form) => Object.fromEntries(new FormData(form)));
  expect(values.network).toBe("unix");
  for (const field of ["host", "port", "password", "tls_policy"]) expect(values).not.toHaveProperty(field);
  await page.locator("#network").selectOption("tcp");
  await expect(page.locator("#host")).toBeVisible();
  await expect(page.locator("#tls_policy")).toBeEnabled();
});

test("editing Unix datasource requires new TCP password", async ({ page }) => {
  await page.goto("/datasources/new?edit=unix");
  await expect(page.locator("#socket_path")).toBeVisible();
  await expect(page.locator("#password")).toBeDisabled();
  await expect(page.locator("#password")).toHaveValue("");
  await page.locator("#network").selectOption("tcp");
  await expect(page.locator("#password")).toBeVisible();
  await expect(page.locator("#password")).toHaveJSProperty("required", true);
  await expect(page.locator("#socket_path")).toBeDisabled();
});
