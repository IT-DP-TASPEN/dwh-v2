const { test, expect } = require("@playwright/test");

const pages = [
  ["challenge", "Verify your identity", false],
  ["error", "Verify your identity", false],
  ["enrollment", "Set up authenticator", false],
  ["recovery", "Save your recovery codes", false],
  ["logout-recovery", "Save your recovery codes", false],
  ["step-up", "Confirm your identity", true],
  ["impersonated-step-up", "Confirm your identity", true],
  ["rotation-authorize", "Confirm your identity", true],
  ["rotation-enrollment", "Set up new authenticator", true],
  ["regenerate", "Confirm your identity", true],
  ["security", "Security / MFA", true],
  ["admin-reset", "Reset MFA", true],
  ["user-detail", "Test User", true],
];

for (const width of [320, 768, 1440]) {
  for (const theme of ["light", "dark"]) {
    test(`MFA layouts at ${width}px in ${theme} mode`, async ({ page }, testInfo) => {
      await page.setViewportSize({ width, height: 1000 });
      await page.addInitScript((theme) => {
        localStorage.setItem("theme", theme);
        window.cspViolations = [];
        document.addEventListener("securitypolicyviolation", (event) => window.cspViolations.push(event.violatedDirective));
      }, theme);
      const errors = [];
      page.on("pageerror", (error) => errors.push(error.message));
      for (const [path, heading, authenticated] of pages) {
        const response = await page.goto(`/fixture/mfa/${path}`);
        expect(response.status()).toBe(200);
        expect(response.headers()["cache-control"]).toBe("no-store");
        expect(response.headers()["referrer-policy"]).toBe("strict-origin");
        await expect(page.getByRole("heading", { name: heading, exact: true })).toBeVisible();
        expect(await page.locator("html").evaluate((node) => node.classList.contains("dark"))).toBe(theme === "dark");
        await expect(page.locator("html[data-admin-shell]")).toHaveCount(authenticated ? 1 : 0);
        await expect(page.locator("#admin-sidebar")).toHaveCount(authenticated ? 1 : 0);
        expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
        if (path.includes("enrollment")) {
          const qr = page.getByAltText("Authenticator setup QR code");
          await expect.poll(() => qr.evaluate((node) => node.complete && node.naturalWidth > 0)).toBe(true);
          expect(await qr.evaluate((node) => getComputedStyle(node).backgroundColor)).toBe("rgb(255, 255, 255)");
          await expect(page.getByLabel("Manual setup key")).toHaveValue("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567");
          expect(await page.getByLabel("Manual setup key").evaluate((node) => node.scrollHeight <= node.clientHeight)).toBe(true);
        }
        if (path.includes("recovery")) {
          await expect(page.getByRole("list", { name: "Recovery codes" }).locator("code")).toHaveCount(10);
          await expect(page.getByText("These codes are shown only once.")).toBeVisible();
        }
        if (path === "impersonated-step-up") await expect(page.getByRole("status").filter({ hasText: "is impersonating" })).toBeVisible();
        if (path === "admin-reset") {
          await expect(page.getByRole("heading", { name: "Test User" })).toBeVisible();
          await expect(page.getByText("@test-user", { exact: true })).toBeVisible();
        }
        if (path === "user-detail") {
          const action = page.getByRole("link", { name: "Reset MFA", exact: true });
          await expect(action).toBeVisible();
          expect(await action.evaluate((node) => node.closest("section").querySelector("h1").compareDocumentPosition(node) & Node.DOCUMENT_POSITION_FOLLOWING)).toBeTruthy();
          await expect(page.locator(".btn-secondary")).toHaveCount(0);
        }
        expect(await page.evaluate(() => window.cspViolations)).toEqual([]);
        await page.screenshot({ path: testInfo.outputPath(`${path}.png`), fullPage: true });
      }
      expect(errors).toEqual([]);
    });
  }
}
