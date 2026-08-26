const { defineConfig } = require("@playwright/test");

module.exports = defineConfig({
  testDir: "./tests/browser",
  outputDir: "./output/playwright/test-results",
  timeout: 30_000,
  workers: 1,
  use: {
    baseURL: "http://127.0.0.1:4173",
    headless: true,
  },
  webServer: {
    command: "go run ./internal/features/ingestion/testdata/browser",
    url: "http://127.0.0.1:4173/healthz",
    reuseExistingServer: false,
    timeout: 120_000,
  },
});
