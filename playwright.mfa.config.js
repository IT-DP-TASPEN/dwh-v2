const { defineConfig } = require("@playwright/test");
module.exports = defineConfig({
  testDir: "./tests/browser",
  testMatch: "mfa.spec.js",
  outputDir: "./output/playwright/mfa-results",
  timeout: 60_000,
  workers: 1,
  use: {
    // A LAN-style HTTP origin omits Fetch Metadata, unlike trusted localhost.
    baseURL: "http://mfa-lan.test:4174",
    headless: true,
    launchOptions: {
      args: ["--host-resolver-rules=MAP mfa-lan.test 127.0.0.1", "--no-proxy-server"],
    },
  },
  webServer: {
    command: "go run ./internal/browserauth/testdata/browser",
    url: "http://127.0.0.1:4174/health",
    reuseExistingServer: false,
    timeout: 120_000,
  },
});
