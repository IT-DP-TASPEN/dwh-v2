const { defineConfig } = require("@playwright/test");
module.exports = defineConfig({
  testDir: "./tests/browser",
  testMatch: "mfa.spec.js",
  outputDir: "./output/playwright/mfa-results",
  timeout: 60_000,
  workers: 1,
  use: { baseURL: "http://127.0.0.1:4174", headless: true },
  webServer: {
    command: "go run ./internal/browserauth/testdata/browser",
    url: "http://127.0.0.1:4174/health",
    reuseExistingServer: false,
    timeout: 120_000,
  },
});
