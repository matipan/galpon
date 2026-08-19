import { expect, test } from "@playwright/test";

test("phone viewport keeps list and composer usable without horizontal overflow", async ({ page }) => {
  await page.goto("/?mock=1");
  await expect(page.getByRole("button", { name: /Mobile companion/ })).toBeVisible();

  const listMetrics = await page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
    viewportWidth: innerWidth,
  }));
  expect(listMetrics.clientWidth).toBe(listMetrics.viewportWidth);
  expect(listMetrics.scrollWidth).toBeLessThanOrEqual(listMetrics.clientWidth);

  await page.getByRole("button", { name: /Security reviewer/ }).click();
  const composer = page.getByRole("textbox", { name: "Send feedback" });
  await expect(composer).toBeVisible();
  await composer.fill("Mobile feedback draft");
  await expect(page.getByRole("button", { name: "Send feedback" })).toBeInViewport();

  const detailMetrics = await page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }));
  expect(detailMetrics.scrollWidth).toBeLessThanOrEqual(detailMetrics.clientWidth);
});
