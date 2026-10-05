const { test, expect } = require("@playwright/test");
const crypto = require("node:crypto");

// The ordinary UI fixture has no authentication DB. Run this file with the
// dedicated MFA config and a disposable MySQL 8.4 schema.
test.skip(process.env.MFA_BROWSER_TEST !== "1", "Requires real MySQL MFA fixture");

function totp(secret, offset = 0) {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const character of secret) bits += alphabet.indexOf(character).toString(2).padStart(5, "0");
  const key = Buffer.from(bits.match(/.{8}/g).map((byte) => parseInt(byte, 2)));
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 30000) + offset));
  const digest = crypto.createHmac("sha1", key).update(counter).digest();
  const index = digest[digest.length - 1] & 15;
  return String((digest.readUInt32BE(index) & 0x7fffffff) % 1000000).padStart(6, "0");
}

async function passwordLogin(page) {
  await page.goto("/login");
  await page.locator('[name="username"]').fill("browser-mfa");
  await page.locator('[name="password"]').fill("browser-password-only");
  await page.locator('[name="remember_me"]').check();
  await page.getByRole("button", { name: /sign in/i }).click();
  await expect(page).toHaveURL(/\/mfa$/);
}

test("mandatory enrollment, one-time codes, MFA login, stale POST and recovery step-up", async ({ page }) => {
  await page.addInitScript(() => {
    window.cspViolations = [];
    document.addEventListener("securitypolicyviolation", (event) => window.cspViolations.push(event.violatedDirective));
  });
  const qrLoaded = page.waitForResponse((response) => new URL(response.url()).pathname === "/mfa/qr");
  await passwordLogin(page);
  expect(await page.evaluate(() => window.isSecureContext)).toBe(false);
  await expect(page.getByRole("heading", { name: "Set up authenticator" })).toBeVisible();
  const secret = await page.locator("#manual-secret").inputValue();
  expect(secret).toMatch(/^[A-Z2-7]{32}$/);
  const image = page.getByAltText("Authenticator setup QR code");
  await expect(image).toBeVisible();
  await expect.poll(() => image.evaluate((node) => node.complete && node.naturalWidth > 0)).toBe(true);
  const qr = await qrLoaded;
  expect(qr.headers()["cache-control"]).toBe("no-store");
  expect(qr.headers()["content-type"]).toBe("image/png");
  // Previous counter leaves the current counter available for the next login.
  await expect(page.locator("html[data-admin-shell]")).toHaveCount(0);
  await page.getByLabel("Enter the 6-digit code from your authenticator", { exact: true }).fill(totp(secret, -1));
  const origin = new URL(page.url()).origin;
  const verification = page.waitForRequest((request) =>
    request.method() === "POST" && new URL(request.url()).pathname === "/mfa");
  await page.getByRole("button", { name: "Verify and continue", exact: true }).click();
  const verificationHeaders = await (await verification).allHeaders();
  expect(verificationHeaders["sec-fetch-site"]).toBeUndefined();
  expect(verificationHeaders.origin).toBe(origin);
  expect(verificationHeaders.referer).toBe(`${origin}/`);
  await expect(page.getByRole("heading", { name: "Save your recovery codes" })).toBeVisible();
  const codes = await page.getByRole("list", { name: "Recovery codes" }).locator("code").allTextContents();
  expect(codes).toHaveLength(10);
  await expect(page.locator("html[data-admin-shell]")).toHaveCount(0);
  expect(await page.evaluate(() => window.cspViolations)).toEqual([]);
  await page.getByRole("link", { name: "Continue", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Application", exact: true })).toBeVisible();
  await page.goto("/mfa");
  await expect(page.getByRole("heading", { name: "Application", exact: true })).toBeVisible();
  expect(await page.content()).not.toContain(codes[0]);
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await passwordLogin(page);
  await expect(page.locator("#manual-secret")).toHaveCount(0);
  await page.getByLabel("Verification code", { exact: true }).fill(totp(secret));
  await page.getByRole("button", { name: "Verify", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Application", exact: true })).toBeVisible();
  const security = await page.goto("/mfa/security");
  expect(security.headers()["cache-control"]).toBe("no-store");
  await expect(page.locator("html[data-admin-shell]")).toHaveCount(1);
  await expect(page.getByText("Enabled", { exact: true })).toBeVisible();
  await page.goto("/");
  await page.getByRole("link", { name: "Sensitive form", exact: true }).click();
  const staleStatus = await page.evaluate(async () => (await fetch("/fixture/stale", { method: "POST" })).status);
  expect(staleStatus).toBe(204);
  await page.getByRole("button", { name: "Submit sensitive action", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Confirm your identity", exact: true })).toBeVisible();
  await expect(page.getByText(/submit again/)).toBeVisible();
  await expect(page.locator("html[data-admin-shell]")).toHaveCount(1);
  await expect(page.locator("#admin-sidebar")).toHaveCount(1);
  await expect(page.locator("header").getByText("Browser MFA", { exact: true }).first()).toBeVisible();
  await page.getByLabel("Verification code", { exact: true }).fill(codes[0]);
  await page.getByRole("button", { name: "Verify", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Sensitive form", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Submit sensitive action", exact: true }).click();
  await expect(page.getByText("Sensitive action completed", { exact: true })).toBeVisible();
  await page.goto("/");
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await passwordLogin(page);
  await page.getByLabel("Verification code", { exact: true }).fill(codes[1]);
  await page.getByRole("button", { name: "Verify", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Application", exact: true })).toBeVisible();
});
