import { defineConfig, devices } from "@playwright/test";

const executablePath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH || undefined;

export default defineConfig({
  testDir: "./website/browser-tests",
  fullyParallel: true,
  reporter: "list",
  outputDir: "test-results/website-playwright",
  use: {
    baseURL: "http://127.0.0.1:43189",
    screenshot: "only-on-failure",
    trace: "on-first-retry",
  },
  projects: [
    {
      name: "desktop-chromium",
      use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 960 }, launchOptions: { executablePath } },
    },
  ],
  webServer: {
    command: "python3 -m http.server 43189 --bind 127.0.0.1 --directory website",
    url: "http://127.0.0.1:43189/",
    reuseExistingServer: false,
    timeout: 10_000,
  },
});
