import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./internal/companion/web/browser-tests",
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.CI ? "github" : "list",
  outputDir: "test-results/playwright",
  use: {
    baseURL: "http://127.0.0.1:4173",
    trace: "on-first-retry",
  },
  projects: [
    {
      name: "desktop-chromium",
      use: { ...devices["Desktop Chrome"] },
      testIgnore: /mobile\.spec\.mjs/,
    },
    {
      name: "mobile-chromium",
      use: { ...devices["Pixel 7"] },
      testMatch: /mobile\.spec\.mjs/,
    },
  ],
  webServer: {
    command: "python3 -m http.server 4173 --bind 127.0.0.1 --directory internal/companion/web",
    url: "http://127.0.0.1:4173/",
    reuseExistingServer: !process.env.CI,
    timeout: 15_000,
  },
});
